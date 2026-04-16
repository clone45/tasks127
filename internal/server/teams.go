package server

import (
	"database/sql"
	"fmt"
	"net/http"
	"strings"

	"github.com/clone45/tasks127/internal/filter"
	"github.com/oklog/ulid/v2"
)

type teamResponse struct {
	ID        string  `json:"id"`
	Key       string  `json:"key"`
	Name      string  `json:"name"`
	CreatedAt string  `json:"created_at"`
	UpdatedAt string  `json:"updated_at"`
	DeletedAt *string `json:"deleted_at"`
}

func (s *Server) scanTeam(r *http.Request, id string, includeDeleted bool) (*teamResponse, error) {
	query := `SELECT id, key, name, created_at, updated_at, deleted_at FROM teams WHERE id = ?`
	if !includeDeleted {
		query += ` AND deleted_at IS NULL`
	}
	var t teamResponse
	var key, deletedAt sql.NullString
	err := s.db.QueryRowContext(r.Context(), query, id).Scan(
		&t.ID, &key, &t.Name, &t.CreatedAt, &t.UpdatedAt, &deletedAt,
	)
	t.Key = key.String
	t.DeletedAt = nullStr(deletedAt)
	return &t, err
}

var teamFields = map[string]filter.FieldSpec{
	"id":         {Column: "id"},
	"key":        {Column: "key"},
	"name":       {Column: "name"},
	"created_at": {Column: "created_at"},
	"updated_at": {Column: "updated_at"},
}

func (s *Server) handleSearchTeams(w http.ResponseWriter, r *http.Request) {
	params, err := filter.ParseRequest(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_filter", err.Error())
		return
	}
	q, err := filter.Build(params, teamFields)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_filter", err.Error())
		return
	}

	scopeFrag, scopeArgs, unrestricted := s.scopeTeam(r.Context(), "id")
	q.WhereClause, q.Args = applyScope(q.WhereClause, q.Args, scopeFrag, scopeArgs, unrestricted)

	var total int
	if err := s.db.QueryRowContext(r.Context(),
		"SELECT COUNT(*) FROM teams WHERE "+q.WhereClause, q.Args...,
	).Scan(&total); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "count failed")
		return
	}

	rows, err := s.db.QueryContext(r.Context(),
		fmt.Sprintf("SELECT id, key, name, created_at, updated_at, deleted_at FROM teams WHERE %s%s LIMIT %d OFFSET %d",
			q.WhereClause, q.OrderClause, q.Limit, q.Offset),
		q.Args...,
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "search failed")
		return
	}
	defer rows.Close()

	var results []teamResponse
	for rows.Next() {
		var t teamResponse
		var key, deletedAt sql.NullString
		if err := rows.Scan(&t.ID, &key, &t.Name, &t.CreatedAt, &t.UpdatedAt, &deletedAt); err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "scan failed")
			return
		}
		t.Key = key.String
		t.DeletedAt = nullStr(deletedAt)
		results = append(results, t)
	}
	if results == nil {
		results = []teamResponse{}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"data": results, "total": total,
		"limit": q.Limit, "offset": q.Offset,
	})
}

func (s *Server) handleCreateTeam(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	var input struct {
		Key  string `json:"key"`
		Name string `json:"name"`
	}
	if err := readJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "invalid JSON")
		return
	}
	if input.Name == "" || input.Key == "" {
		writeError(w, http.StatusBadRequest, "missing_field", "key and name are required")
		return
	}
	if !isValidKey(input.Key) {
		writeError(w, http.StatusBadRequest, "invalid_field", "key must be exactly 3 uppercase A-Z letters")
		return
	}

	id := ulid.Make().String()
	now := nowRFC3339()

	tx, err := s.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "failed to start transaction")
		return
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(r.Context(),
		`INSERT INTO resource_keys (key, owner_type, owner_id) VALUES (?, 'team', ?)`,
		input.Key, id,
	); err != nil {
		if isUniqueViolation(err) {
			writeError(w, http.StatusConflict, "key_conflict", "key is already taken by another team or project")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal", "failed to reserve key")
		return
	}

	if _, err := tx.ExecContext(r.Context(),
		`INSERT INTO teams (id, key, name, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`,
		id, input.Key, input.Name, now, now,
	); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "failed to create team")
		return
	}

	if err := tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "failed to commit")
		return
	}

	s.audit(r.Context(), "create", "teams", id, input)
	s.fireEvents(r.Context(), "teams", "create", []string{id})

	writeJSON(w, http.StatusCreated, &teamResponse{
		ID: id, Key: input.Key, Name: input.Name, CreatedAt: now, UpdatedAt: now,
	})
}

func (s *Server) handleGetTeam(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !s.canAccessTeam(r.Context(), id) {
		writeError(w, http.StatusNotFound, "not_found", "team not found")
		return
	}
	t, err := s.scanTeam(r, id, false)
	if err == sql.ErrNoRows {
		writeError(w, http.StatusNotFound, "not_found", "team not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "failed to read team")
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (s *Server) handleUpdateTeam(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	id := r.PathValue("id")

	var input struct {
		Name *string `json:"name"`
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
	if len(sets) == 0 {
		writeError(w, http.StatusBadRequest, "no_fields", "no fields to update")
		return
	}

	now := nowRFC3339()
	sets = append(sets, "updated_at = ?")
	args = append(args, now)
	args = append(args, id)

	query := "UPDATE teams SET " + strings.Join(sets, ", ") + " WHERE id = ? AND deleted_at IS NULL"
	res, err := s.db.ExecContext(r.Context(), query, args...)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "failed to update team")
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		writeError(w, http.StatusNotFound, "not_found", "team not found")
		return
	}

	s.audit(r.Context(), "update", "teams", id, input)
	s.fireEvents(r.Context(), "teams", "update", []string{id})

	t, _ := s.scanTeam(r, id, false)
	writeJSON(w, http.StatusOK, t)
}

func (s *Server) handleDeleteTeam(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	id := r.PathValue("id")
	now := nowRFC3339()

	res, err := s.db.ExecContext(r.Context(),
		`UPDATE teams SET deleted_at = ?, updated_at = ? WHERE id = ? AND deleted_at IS NULL`,
		now, now, id,
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "failed to delete team")
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		writeError(w, http.StatusNotFound, "not_found", "team not found")
		return
	}

	s.audit(r.Context(), "delete", "teams", id, nil)
	s.fireEvents(r.Context(), "teams", "delete", []string{id})

	t, _ := s.scanTeam(r, id, true)
	writeJSON(w, http.StatusOK, t)
}

func (s *Server) handleRestoreTeam(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	id := r.PathValue("id")
	now := nowRFC3339()

	res, err := s.db.ExecContext(r.Context(),
		`UPDATE teams SET deleted_at = NULL, updated_at = ? WHERE id = ? AND deleted_at IS NOT NULL`,
		now, id,
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "failed to restore team")
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		writeError(w, http.StatusNotFound, "not_found", "team not found or not deleted")
		return
	}

	s.audit(r.Context(), "restore", "teams", id, nil)
	s.fireEvents(r.Context(), "teams", "restore", []string{id})

	t, _ := s.scanTeam(r, id, false)
	writeJSON(w, http.StatusOK, t)
}

var teamSettable = map[string]bool{"name": true}

func (s *Server) handleBulkUpdateTeams(w http.ResponseWriter, r *http.Request) {
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
	setClause, setArgs, err := buildBulkSet(body.Set, teamSettable)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_set", err.Error())
		return
	}
	now := nowRFC3339()
	setClause += ", updated_at = ?"
	setArgs = append(setArgs, now)

	result, err := s.execBulkUpdate(r.Context(), "teams", teamFields, body.Where, setClause, setArgs, "", nil)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_filter", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleBulkDeleteTeams(w http.ResponseWriter, r *http.Request) {
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
	result, err := s.execBulkDelete(r.Context(), "teams", teamFields, body.Where, true, "", nil)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_filter", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
}
