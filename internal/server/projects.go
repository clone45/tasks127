package server

import (
	"database/sql"
	"fmt"
	"net/http"
	"strings"

	"github.com/clone45/tasks127/internal/filter"
	"github.com/oklog/ulid/v2"
)

type projectResponse struct {
	ID        string  `json:"id"`
	Key       string  `json:"key"`
	TeamID    string  `json:"team_id"`
	Name      string  `json:"name"`
	CreatedAt string  `json:"created_at"`
	UpdatedAt string  `json:"updated_at"`
	DeletedAt *string `json:"deleted_at"`
}

func (s *Server) scanProject(r *http.Request, id string, includeDeleted bool) (*projectResponse, error) {
	query := `SELECT id, key, team_id, name, created_at, updated_at, deleted_at FROM projects WHERE id = ?`
	if !includeDeleted {
		query += ` AND deleted_at IS NULL`
	}
	var p projectResponse
	var key, deletedAt sql.NullString
	err := s.db.QueryRowContext(r.Context(), query, id).Scan(
		&p.ID, &key, &p.TeamID, &p.Name, &p.CreatedAt, &p.UpdatedAt, &deletedAt,
	)
	p.Key = key.String
	p.DeletedAt = nullStr(deletedAt)
	return &p, err
}

var projectFields = map[string]filter.FieldSpec{
	"id":         {Column: "id"},
	"key":        {Column: "key"},
	"team_id":    {Column: "team_id"},
	"name":       {Column: "name"},
	"created_at": {Column: "created_at"},
	"updated_at": {Column: "updated_at"},
}

func (s *Server) handleSearchProjects(w http.ResponseWriter, r *http.Request) {
	params, err := filter.ParseRequest(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_filter", err.Error())
		return
	}
	q, err := filter.Build(params, projectFields)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_filter", err.Error())
		return
	}

	scopeFrag, scopeArgs, unrestricted := s.scopeTeam(r.Context(), "team_id")
	q.WhereClause, q.Args = applyScope(q.WhereClause, q.Args, scopeFrag, scopeArgs, unrestricted)

	var total int
	if err := s.db.QueryRowContext(r.Context(),
		"SELECT COUNT(*) FROM projects WHERE "+q.WhereClause, q.Args...,
	).Scan(&total); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "count failed")
		return
	}

	rows, err := s.db.QueryContext(r.Context(),
		fmt.Sprintf("SELECT id, key, team_id, name, created_at, updated_at, deleted_at FROM projects WHERE %s%s LIMIT %d OFFSET %d",
			q.WhereClause, q.OrderClause, q.Limit, q.Offset),
		q.Args...,
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "search failed")
		return
	}
	defer rows.Close()

	var results []projectResponse
	for rows.Next() {
		var p projectResponse
		var key, deletedAt sql.NullString
		if err := rows.Scan(&p.ID, &key, &p.TeamID, &p.Name, &p.CreatedAt, &p.UpdatedAt, &deletedAt); err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "scan failed")
			return
		}
		p.Key = key.String
		p.DeletedAt = nullStr(deletedAt)
		results = append(results, p)
	}
	if results == nil {
		results = []projectResponse{}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"data": results, "total": total,
		"limit": q.Limit, "offset": q.Offset,
	})
}

func (s *Server) handleCreateProject(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Key    string `json:"key"`
		TeamID string `json:"team_id"`
		Name   string `json:"name"`
	}
	if err := readJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "invalid JSON")
		return
	}
	if input.TeamID == "" || input.Name == "" || input.Key == "" {
		writeError(w, http.StatusBadRequest, "missing_field", "key, team_id, and name are required")
		return
	}
	if !isValidKey(input.Key) {
		writeError(w, http.StatusBadRequest, "invalid_field", "key must be exactly 3 uppercase A-Z letters")
		return
	}

	if !s.canAccessTeam(r.Context(), input.TeamID) {
		writeError(w, http.StatusBadRequest, "invalid_reference", "team not found")
		return
	}
	if ok, _ := s.activeExists(r.Context(), "teams", input.TeamID); !ok {
		writeError(w, http.StatusBadRequest, "invalid_reference", "team not found")
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
		`INSERT INTO resource_keys (key, owner_type, owner_id) VALUES (?, 'project', ?)`,
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
		`INSERT INTO projects (id, key, team_id, name, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)`,
		id, input.Key, input.TeamID, input.Name, now, now,
	); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "failed to create project")
		return
	}

	if err := tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "failed to commit")
		return
	}

	s.audit(r.Context(), "create", "projects", id, input)
	s.fireEvents(r.Context(), "projects", "create", []string{id})

	writeJSON(w, http.StatusCreated, &projectResponse{
		ID: id, Key: input.Key, TeamID: input.TeamID, Name: input.Name,
		CreatedAt: now, UpdatedAt: now,
	})
}

func (s *Server) handleGetProject(w http.ResponseWriter, r *http.Request) {
	p, err := s.scanProject(r, r.PathValue("id"), false)
	if err == sql.ErrNoRows {
		writeError(w, http.StatusNotFound, "not_found", "project not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "failed to read project")
		return
	}
	if !s.canAccessTeam(r.Context(), p.TeamID) {
		writeError(w, http.StatusNotFound, "not_found", "project not found")
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) handleUpdateProject(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	current, err := s.scanProject(r, id, false)
	if err == sql.ErrNoRows || (err == nil && !s.canAccessTeam(r.Context(), current.TeamID)) {
		writeError(w, http.StatusNotFound, "not_found", "project not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "failed to read project")
		return
	}

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

	query := "UPDATE projects SET " + strings.Join(sets, ", ") + " WHERE id = ? AND deleted_at IS NULL"
	res, err := s.db.ExecContext(r.Context(), query, args...)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "failed to update project")
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		writeError(w, http.StatusNotFound, "not_found", "project not found")
		return
	}

	s.audit(r.Context(), "update", "projects", id, input)
	s.fireEvents(r.Context(), "projects", "update", []string{id})

	p, _ := s.scanProject(r, id, false)
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) handleDeleteProject(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	current, err := s.scanProject(r, id, false)
	if err == sql.ErrNoRows || (err == nil && !s.canAccessTeam(r.Context(), current.TeamID)) {
		writeError(w, http.StatusNotFound, "not_found", "project not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "failed to read project")
		return
	}

	now := nowRFC3339()
	res, err := s.db.ExecContext(r.Context(),
		`UPDATE projects SET deleted_at = ?, updated_at = ? WHERE id = ? AND deleted_at IS NULL`,
		now, now, id,
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "failed to delete project")
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		writeError(w, http.StatusNotFound, "not_found", "project not found")
		return
	}

	s.audit(r.Context(), "delete", "projects", id, nil)
	s.fireEvents(r.Context(), "projects", "delete", []string{id})

	p, _ := s.scanProject(r, id, true)
	writeJSON(w, http.StatusOK, p)
}

func (s *Server) handleRestoreProject(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	current, err := s.scanProject(r, id, true)
	if err == sql.ErrNoRows || (err == nil && !s.canAccessTeam(r.Context(), current.TeamID)) {
		writeError(w, http.StatusNotFound, "not_found", "project not found or not deleted")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "failed to read project")
		return
	}

	now := nowRFC3339()
	res, err := s.db.ExecContext(r.Context(),
		`UPDATE projects SET deleted_at = NULL, updated_at = ? WHERE id = ? AND deleted_at IS NOT NULL`,
		now, id,
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "failed to restore project")
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		writeError(w, http.StatusNotFound, "not_found", "project not found or not deleted")
		return
	}

	s.audit(r.Context(), "restore", "projects", id, nil)
	s.fireEvents(r.Context(), "projects", "restore", []string{id})

	p, _ := s.scanProject(r, id, false)
	writeJSON(w, http.StatusOK, p)
}

var projectSettable = map[string]bool{"name": true}

func (s *Server) handleBulkUpdateProjects(w http.ResponseWriter, r *http.Request) {
	var body bulkUpdateRequest
	if err := readJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "invalid JSON")
		return
	}
	if len(body.Where) == 0 {
		writeError(w, http.StatusBadRequest, "missing_filter", "bulk operations require a non-empty where clause")
		return
	}
	setClause, setArgs, err := buildBulkSet(body.Set, projectSettable)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_set", err.Error())
		return
	}
	now := nowRFC3339()
	setClause += ", updated_at = ?"
	setArgs = append(setArgs, now)

	scopeFrag, scopeArgs, _ := s.scopeTeam(r.Context(), "team_id")
	result, err := s.execBulkUpdate(r.Context(), "projects", projectFields, body.Where, setClause, setArgs, scopeFrag, scopeArgs)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_filter", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleBulkDeleteProjects(w http.ResponseWriter, r *http.Request) {
	var body bulkDeleteRequest
	if err := readJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "invalid JSON")
		return
	}
	if len(body.Where) == 0 {
		writeError(w, http.StatusBadRequest, "missing_filter", "bulk operations require a non-empty where clause")
		return
	}
	scopeFrag, scopeArgs, _ := s.scopeTeam(r.Context(), "team_id")
	result, err := s.execBulkDelete(r.Context(), "projects", projectFields, body.Where, true, scopeFrag, scopeArgs)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_filter", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
}
