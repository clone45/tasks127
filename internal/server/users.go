package server

import (
	"database/sql"
	"fmt"
	"net/http"
	"strings"

	"github.com/clone45/tasks127/internal/filter"
	"github.com/oklog/ulid/v2"
)

type userResponse struct {
	ID        string  `json:"id"`
	Name      string  `json:"name"`
	Email     string  `json:"email"`
	CreatedAt string  `json:"created_at"`
	UpdatedAt string  `json:"updated_at"`
	DeletedAt *string `json:"deleted_at"`
}

func (s *Server) scanUser(r *http.Request, id string, includeDeleted bool) (*userResponse, error) {
	query := `SELECT id, name, email, created_at, updated_at, deleted_at FROM users WHERE id = ?`
	if !includeDeleted {
		query += ` AND deleted_at IS NULL`
	}
	var u userResponse
	var deletedAt sql.NullString
	err := s.db.QueryRowContext(r.Context(), query, id).Scan(
		&u.ID, &u.Name, &u.Email, &u.CreatedAt, &u.UpdatedAt, &deletedAt,
	)
	u.DeletedAt = nullStr(deletedAt)
	return &u, err
}

var userFields = map[string]filter.FieldSpec{
	"id":         {Column: "id"},
	"name":       {Column: "name"},
	"email":      {Column: "email"},
	"created_at": {Column: "created_at"},
	"updated_at": {Column: "updated_at"},
}

func (s *Server) handleSearchUsers(w http.ResponseWriter, r *http.Request) {
	params, err := filter.ParseRequest(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_filter", err.Error())
		return
	}
	q, err := filter.Build(params, userFields)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_filter", err.Error())
		return
	}

	scopeFrag, scopeArgs, unrestricted := s.scopeUser(r.Context(), "id")
	q.WhereClause, q.Args = applyScope(q.WhereClause, q.Args, scopeFrag, scopeArgs, unrestricted)

	var total int
	if err := s.db.QueryRowContext(r.Context(),
		"SELECT COUNT(*) FROM users WHERE "+q.WhereClause, q.Args...,
	).Scan(&total); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "count failed")
		return
	}

	rows, err := s.db.QueryContext(r.Context(),
		fmt.Sprintf("SELECT id, name, email, created_at, updated_at, deleted_at FROM users WHERE %s%s LIMIT %d OFFSET %d",
			q.WhereClause, q.OrderClause, q.Limit, q.Offset),
		q.Args...,
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "search failed")
		return
	}
	defer rows.Close()

	var results []userResponse
	for rows.Next() {
		var u userResponse
		var deletedAt sql.NullString
		if err := rows.Scan(&u.ID, &u.Name, &u.Email, &u.CreatedAt, &u.UpdatedAt, &deletedAt); err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "scan failed")
			return
		}
		u.DeletedAt = nullStr(deletedAt)
		results = append(results, u)
	}
	if results == nil {
		results = []userResponse{}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"data": results, "total": total,
		"limit": q.Limit, "offset": q.Offset,
	})
}

func (s *Server) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	var input struct {
		Name  string `json:"name"`
		Email string `json:"email"`
	}
	if err := readJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "invalid JSON")
		return
	}
	if input.Name == "" || input.Email == "" {
		writeError(w, http.StatusBadRequest, "missing_field", "name and email are required")
		return
	}

	id := ulid.Make().String()
	now := nowRFC3339()

	_, err := s.db.ExecContext(r.Context(),
		`INSERT INTO users (id, name, email, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`,
		id, input.Name, input.Email, now, now,
	)
	if err != nil {
		if isUniqueViolation(err) {
			writeError(w, http.StatusConflict, "conflict",
				"the email is already in use by another active user. Emails are unique across non-deleted users. "+
					"If the existing user was soft-deleted and you want to reuse their record, restore them via POST /v1/users/{id}/restore "+
					"and search for them first with a $include_deleted filter. Otherwise, pick a different email.")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal", "failed to create user")
		return
	}

	s.audit(r.Context(), "create", "users", id, input)
	s.fireEvents(r.Context(), "users", "create", []string{id})

	writeJSON(w, http.StatusCreated, &userResponse{
		ID: id, Name: input.Name, Email: input.Email,
		CreatedAt: now, UpdatedAt: now,
	})
}

func (s *Server) handleGetUser(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !s.canAccessUser(r.Context(), id) {
		writeError(w, http.StatusNotFound, "not_found", "user not found")
		return
	}
	u, err := s.scanUser(r, id, false)
	if err == sql.ErrNoRows {
		writeError(w, http.StatusNotFound, "not_found", "user not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "failed to read user")
		return
	}
	writeJSON(w, http.StatusOK, u)
}

func (s *Server) handleUpdateUser(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	// Admin-unrestricted can update anyone; others can only update themselves.
	effectiveID, unrestricted := s.effectiveUserID(r.Context())
	if !unrestricted && effectiveID != id {
		writeError(w, http.StatusNotFound, "not_found", "user not found")
		return
	}

	var input struct {
		Name  *string `json:"name"`
		Email *string `json:"email"`
	}
	if err := readJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "invalid JSON")
		return
	}

	var sets []string
	var args []any
	if input.Name != nil {
		sets = append(sets, "name = ?")
		args = append(args, *input.Name)
	}
	if input.Email != nil {
		sets = append(sets, "email = ?")
		args = append(args, *input.Email)
	}
	if len(sets) == 0 {
		writeError(w, http.StatusBadRequest, "no_fields", "no fields to update")
		return
	}

	now := nowRFC3339()
	sets = append(sets, "updated_at = ?")
	args = append(args, now)
	args = append(args, id)

	query := "UPDATE users SET " + strings.Join(sets, ", ") + " WHERE id = ? AND deleted_at IS NULL"
	res, err := s.db.ExecContext(r.Context(), query, args...)
	if err != nil {
		if isUniqueViolation(err) {
			writeError(w, http.StatusConflict, "conflict",
				"the email is already in use by another active user. Emails are unique across non-deleted users. "+
					"If the existing user was soft-deleted and you want to reuse their record, restore them via POST /v1/users/{id}/restore "+
					"and search for them first with a $include_deleted filter. Otherwise, pick a different email.")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal", "failed to update user")
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		writeError(w, http.StatusNotFound, "not_found", "user not found")
		return
	}

	s.audit(r.Context(), "update", "users", id, input)
	s.fireEvents(r.Context(), "users", "update", []string{id})

	u, _ := s.scanUser(r, id, false)
	writeJSON(w, http.StatusOK, u)
}

func (s *Server) handleDeleteUser(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	id := r.PathValue("id")
	now := nowRFC3339()

	res, err := s.db.ExecContext(r.Context(),
		`UPDATE users SET deleted_at = ?, updated_at = ? WHERE id = ? AND deleted_at IS NULL`,
		now, now, id,
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "failed to delete user")
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		writeError(w, http.StatusNotFound, "not_found", "user not found")
		return
	}

	s.audit(r.Context(), "delete", "users", id, nil)
	s.fireEvents(r.Context(), "users", "delete", []string{id})

	u, _ := s.scanUser(r, id, true)
	writeJSON(w, http.StatusOK, u)
}

func (s *Server) handleRestoreUser(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	id := r.PathValue("id")
	now := nowRFC3339()

	res, err := s.db.ExecContext(r.Context(),
		`UPDATE users SET deleted_at = NULL, updated_at = ? WHERE id = ? AND deleted_at IS NOT NULL`,
		now, id,
	)
	if err != nil {
		if isUniqueViolation(err) {
			writeError(w, http.StatusConflict, "conflict",
				"restoring this user would conflict with an existing active user who now holds the same email. "+
					"Either soft-delete the active user first, change the active user's email, or leave the deleted user deleted.")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal", "failed to restore user")
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		writeError(w, http.StatusNotFound, "not_found", "user not found or not deleted")
		return
	}

	s.audit(r.Context(), "restore", "users", id, nil)
	s.fireEvents(r.Context(), "users", "restore", []string{id})

	u, _ := s.scanUser(r, id, false)
	writeJSON(w, http.StatusOK, u)
}

var userSettable = map[string]bool{"name": true, "email": true}

func (s *Server) handleBulkUpdateUsers(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	var body bulkUpdateRequest
	if err := readJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "invalid JSON")
		return
	}
	if len(body.Where) == 0 {
		writeError(w, http.StatusBadRequest, "missing_filter", "bulk operations require a non-empty where clause")
		return
	}

	setClause, setArgs, err := buildBulkSet(body.Set, userSettable)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_set", err.Error())
		return
	}
	now := nowRFC3339()
	setClause += ", updated_at = ?"
	setArgs = append(setArgs, now)

	result, err := s.execBulkUpdate(r.Context(), "users", userFields, body.Where, setClause, setArgs, "", nil)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_filter", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleBulkDeleteUsers(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	var body bulkDeleteRequest
	if err := readJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "invalid JSON")
		return
	}
	if len(body.Where) == 0 {
		writeError(w, http.StatusBadRequest, "missing_filter", "bulk operations require a non-empty where clause")
		return
	}

	result, err := s.execBulkDelete(r.Context(), "users", userFields, body.Where, true, "", nil)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_filter", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
}

