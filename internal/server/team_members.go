package server

import (
	"database/sql"
	"fmt"
	"net/http"

	"github.com/clone45/tasks127/internal/filter"
	"github.com/oklog/ulid/v2"
)

type teamMemberResponse struct {
	ID        string  `json:"id"`
	TeamID    string  `json:"team_id"`
	UserID    string  `json:"user_id"`
	CreatedAt string  `json:"created_at"`
	DeletedAt *string `json:"deleted_at"`
}

func (s *Server) scanTeamMember(r *http.Request, id string, includeDeleted bool) (*teamMemberResponse, error) {
	query := `SELECT id, team_id, user_id, created_at, deleted_at FROM team_members WHERE id = ?`
	if !includeDeleted {
		query += ` AND deleted_at IS NULL`
	}
	var m teamMemberResponse
	var deletedAt sql.NullString
	err := s.db.QueryRowContext(r.Context(), query, id).Scan(
		&m.ID, &m.TeamID, &m.UserID, &m.CreatedAt, &deletedAt,
	)
	m.DeletedAt = nullStr(deletedAt)
	return &m, err
}

var teamMemberFields = map[string]filter.FieldSpec{
	"id":         {Column: "id"},
	"team_id":    {Column: "team_id"},
	"user_id":    {Column: "user_id"},
	"created_at": {Column: "created_at"},
}

func (s *Server) handleSearchTeamMembers(w http.ResponseWriter, r *http.Request) {
	params, err := filter.ParseRequest(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_filter", err.Error())
		return
	}
	q, err := filter.Build(params, teamMemberFields)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_filter", err.Error())
		return
	}

	scopeFrag, scopeArgs, unrestricted := s.scopeTeam(r.Context(), "team_id")
	q.WhereClause, q.Args = applyScope(q.WhereClause, q.Args, scopeFrag, scopeArgs, unrestricted)

	var total int
	if err := s.db.QueryRowContext(r.Context(),
		"SELECT COUNT(*) FROM team_members WHERE "+q.WhereClause, q.Args...,
	).Scan(&total); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "count failed")
		return
	}

	rows, err := s.db.QueryContext(r.Context(),
		fmt.Sprintf("SELECT id, team_id, user_id, created_at, deleted_at FROM team_members WHERE %s%s LIMIT %d OFFSET %d",
			q.WhereClause, q.OrderClause, q.Limit, q.Offset),
		q.Args...,
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "search failed")
		return
	}
	defer rows.Close()

	var results []teamMemberResponse
	for rows.Next() {
		var m teamMemberResponse
		var deletedAt sql.NullString
		if err := rows.Scan(&m.ID, &m.TeamID, &m.UserID, &m.CreatedAt, &deletedAt); err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "scan failed")
			return
		}
		m.DeletedAt = nullStr(deletedAt)
		results = append(results, m)
	}
	if results == nil {
		results = []teamMemberResponse{}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"data": results, "total": total,
		"limit": q.Limit, "offset": q.Offset,
	})
}

func (s *Server) handleCreateTeamMember(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	var input struct {
		TeamID string `json:"team_id"`
		UserID string `json:"user_id"`
	}
	if err := readJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "invalid JSON")
		return
	}
	if input.TeamID == "" || input.UserID == "" {
		writeError(w, http.StatusBadRequest, "missing_field", "team_id and user_id are required")
		return
	}

	if ok, _ := s.activeExists(r.Context(), "teams", input.TeamID); !ok {
		writeError(w, http.StatusBadRequest, "invalid_reference", "team not found")
		return
	}
	if ok, _ := s.activeExists(r.Context(), "users", input.UserID); !ok {
		writeError(w, http.StatusBadRequest, "invalid_reference", "user not found")
		return
	}

	id := ulid.Make().String()
	now := nowRFC3339()

	_, err := s.db.ExecContext(r.Context(),
		`INSERT INTO team_members (id, team_id, user_id, created_at) VALUES (?, ?, ?, ?)`,
		id, input.TeamID, input.UserID, now,
	)
	if err != nil {
		if isUniqueViolation(err) {
			writeError(w, http.StatusConflict, "conflict", "user is already a member of this team")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal", "failed to add team member")
		return
	}

	s.audit(r.Context(), "create", "team_members", id, input)
	s.fireEvents(r.Context(), "team_members", "create", []string{id})

	writeJSON(w, http.StatusCreated, &teamMemberResponse{
		ID: id, TeamID: input.TeamID, UserID: input.UserID, CreatedAt: now,
	})
}

func (s *Server) handleGetTeamMember(w http.ResponseWriter, r *http.Request) {
	m, err := s.scanTeamMember(r, r.PathValue("id"), false)
	if err == sql.ErrNoRows {
		writeError(w, http.StatusNotFound, "not_found", "team member not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "failed to read team member")
		return
	}
	if !s.canAccessTeam(r.Context(), m.TeamID) {
		writeError(w, http.StatusNotFound, "not_found", "team member not found")
		return
	}
	writeJSON(w, http.StatusOK, m)
}

func (s *Server) handleDeleteTeamMember(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	id := r.PathValue("id")
	now := nowRFC3339()

	res, err := s.db.ExecContext(r.Context(),
		`UPDATE team_members SET deleted_at = ? WHERE id = ? AND deleted_at IS NULL`,
		now, id,
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "failed to remove team member")
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		writeError(w, http.StatusNotFound, "not_found", "team member not found")
		return
	}

	s.audit(r.Context(), "delete", "team_members", id, nil)
	s.fireEvents(r.Context(), "team_members", "delete", []string{id})

	m, _ := s.scanTeamMember(r, id, true)
	writeJSON(w, http.StatusOK, m)
}

func (s *Server) handleRestoreTeamMember(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	id := r.PathValue("id")

	res, err := s.db.ExecContext(r.Context(),
		`UPDATE team_members SET deleted_at = NULL WHERE id = ? AND deleted_at IS NOT NULL`,
		id,
	)
	if err != nil {
		if isUniqueViolation(err) {
			writeError(w, http.StatusConflict, "conflict", "user is already an active member of this team")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal", "failed to restore team member")
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		writeError(w, http.StatusNotFound, "not_found", "team member not found or not deleted")
		return
	}

	s.audit(r.Context(), "restore", "team_members", id, nil)
	s.fireEvents(r.Context(), "team_members", "restore", []string{id})

	m, _ := s.scanTeamMember(r, id, false)
	writeJSON(w, http.StatusOK, m)
}

func (s *Server) handleBulkDeleteTeamMembers(w http.ResponseWriter, r *http.Request) {
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
	result, err := s.execBulkDelete(r.Context(), "team_members", teamMemberFields, body.Where, false, "", nil)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_filter", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
}
