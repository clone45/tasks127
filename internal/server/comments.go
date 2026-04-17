package server

import (
	"database/sql"
	"fmt"
	"net/http"

	"github.com/clone45/tasks127/internal/filter"
	"github.com/oklog/ulid/v2"
)

var commentFields = map[string]filter.FieldSpec{
	"id":             {Column: "id"},
	"ticket_id":      {Column: "ticket_id"},
	"team_id":        {Column: "team_id"},
	"author_user_id": {Column: "author_user_id"},
	"body":           {Column: "body"},
	"created_at":     {Column: "created_at"},
	"updated_at":     {Column: "updated_at"},
}

var commentSettable = map[string]bool{"body": true}

type commentResponse struct {
	ID           string  `json:"id"`
	TicketID     string  `json:"ticket_id"`
	TeamID       string  `json:"team_id"`
	AuthorUserID string  `json:"author_user_id"`
	Body         string  `json:"body"`
	CreatedAt    string  `json:"created_at"`
	UpdatedAt    string  `json:"updated_at"`
	DeletedAt    *string `json:"deleted_at"`
}

const commentCols = `id, ticket_id, team_id, author_user_id, body, created_at, updated_at, deleted_at`

func scanCommentRow(scanner interface{ Scan(dest ...any) error }) (*commentResponse, error) {
	var c commentResponse
	var deletedAt sql.NullString
	err := scanner.Scan(
		&c.ID, &c.TicketID, &c.TeamID, &c.AuthorUserID,
		&c.Body, &c.CreatedAt, &c.UpdatedAt, &deletedAt,
	)
	c.DeletedAt = nullStr(deletedAt)
	return &c, err
}

func (s *Server) getComment(r *http.Request, id string, includeDeleted bool) (*commentResponse, error) {
	query := "SELECT " + commentCols + " FROM comments WHERE id = ?"
	if !includeDeleted {
		query += " AND deleted_at IS NULL"
	}
	return scanCommentRow(s.db.QueryRowContext(r.Context(), query, id))
}

// canEditComment: author or admin-unrestricted.
func (s *Server) canEditComment(r *http.Request, c *commentResponse) bool {
	effectiveID, unrestricted := s.effectiveUserID(r.Context())
	return unrestricted || effectiveID == c.AuthorUserID
}

func (s *Server) handleSearchComments(w http.ResponseWriter, r *http.Request) {
	params, err := filter.ParseRequest(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_filter", err.Error())
		return
	}
	q, err := filter.Build(params, commentFields)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_filter", err.Error())
		return
	}

	scopeFrag, scopeArgs, unrestricted := s.scopeTeam(r.Context(), "team_id")
	q.WhereClause, q.Args = applyScope(q.WhereClause, q.Args, scopeFrag, scopeArgs, unrestricted)

	var total int
	if err := s.db.QueryRowContext(r.Context(),
		"SELECT COUNT(*) FROM comments WHERE "+q.WhereClause, q.Args...,
	).Scan(&total); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "count failed")
		return
	}

	rows, err := s.db.QueryContext(r.Context(),
		fmt.Sprintf("SELECT %s FROM comments WHERE %s%s LIMIT %d OFFSET %d",
			commentCols, q.WhereClause, q.OrderClause, q.Limit, q.Offset),
		q.Args...,
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "search failed")
		return
	}
	defer rows.Close()

	var results []commentResponse
	for rows.Next() {
		c, err := scanCommentRow(rows)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "scan failed")
			return
		}
		results = append(results, *c)
	}
	if results == nil {
		results = []commentResponse{}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"data": results, "total": total,
		"limit": q.Limit, "offset": q.Offset,
	})
}

func (s *Server) handleCreateComment(w http.ResponseWriter, r *http.Request) {
	var input struct {
		TicketID     string `json:"ticket_id"`
		AuthorUserID string `json:"author_user_id"`
		Body         string `json:"body"`
	}
	if err := readJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "invalid JSON")
		return
	}
	if input.TicketID == "" || input.Body == "" {
		writeError(w, http.StatusBadRequest, "missing_field", "ticket_id and body are required")
		return
	}

	ctx := r.Context()

	// Resolve author: unrestricted admin must supply; everyone else = effective user.
	effectiveID, unrestricted := s.effectiveUserID(ctx)
	if unrestricted {
		if input.AuthorUserID == "" {
			writeError(w, http.StatusBadRequest, "missing_field",
				"author_user_id is required when calling as an unrestricted admin (admin-tier API key with no X-On-Behalf-Of header), "+
					"because there is no implicit identity to attribute the comment to. "+
					"Two remedies: pass author_user_id in the request body to specify who the comment is from, "+
					"or set the X-On-Behalf-Of header to a user_id to scope the whole request as that user. "+
					"If you do not yet have a user to attribute to (for example, you are an agent that needs its own identity), "+
					"provision one first via the create_user MCP tool or POST /v1/users, "+
					"and add it to the relevant team with add_team_member or POST /v1/team-members.")
			return
		}
	} else {
		if input.AuthorUserID != "" && input.AuthorUserID != effectiveID {
			writeError(w, http.StatusForbidden, "forbidden", "cannot author comments as another user")
			return
		}
		input.AuthorUserID = effectiveID
	}

	// Find the ticket and verify access.
	var ticketTeamID string
	var ticketDeleted sql.NullString
	err := s.db.QueryRowContext(ctx,
		`SELECT team_id, deleted_at FROM tickets WHERE id = ?`, input.TicketID,
	).Scan(&ticketTeamID, &ticketDeleted)
	if err == sql.ErrNoRows || (err == nil && ticketDeleted.Valid) {
		writeError(w, http.StatusBadRequest, "invalid_reference", "ticket not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "failed to read ticket")
		return
	}
	if !s.canAccessTeam(ctx, ticketTeamID) {
		writeError(w, http.StatusBadRequest, "invalid_reference", "ticket not found")
		return
	}

	// Validate author exists.
	if ok, _ := s.activeExists(ctx, "users", input.AuthorUserID); !ok {
		writeError(w, http.StatusBadRequest, "invalid_reference", "author user not found")
		return
	}

	id := ulid.Make().String()
	now := nowRFC3339()

	_, err = s.db.ExecContext(ctx,
		`INSERT INTO comments (id, ticket_id, team_id, author_user_id, body, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		id, input.TicketID, ticketTeamID, input.AuthorUserID, input.Body, now, now,
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "failed to create comment")
		return
	}

	s.audit(ctx, "create", "comments", id, input)
	s.fireEvents(ctx, "comments", "create", []string{id})

	writeJSON(w, http.StatusCreated, &commentResponse{
		ID: id, TicketID: input.TicketID, TeamID: ticketTeamID,
		AuthorUserID: input.AuthorUserID, Body: input.Body,
		CreatedAt: now, UpdatedAt: now,
	})
}

func (s *Server) handleGetComment(w http.ResponseWriter, r *http.Request) {
	c, err := s.getComment(r, r.PathValue("id"), false)
	if err == sql.ErrNoRows {
		writeError(w, http.StatusNotFound, "not_found", "comment not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "failed to read comment")
		return
	}
	if !s.canAccessTeam(r.Context(), c.TeamID) {
		writeError(w, http.StatusNotFound, "not_found", "comment not found")
		return
	}
	writeJSON(w, http.StatusOK, c)
}

func (s *Server) handleUpdateComment(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	current, err := s.getComment(r, id, false)
	if err == sql.ErrNoRows || (err == nil && !s.canAccessTeam(r.Context(), current.TeamID)) {
		writeError(w, http.StatusNotFound, "not_found", "comment not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "failed to read comment")
		return
	}
	if !s.canEditComment(r, current) {
		writeError(w, http.StatusForbidden, "forbidden", "only the author can edit this comment")
		return
	}

	var input struct {
		Body *string `json:"body"`
	}
	if err := readJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "invalid JSON")
		return
	}
	if input.Body == nil {
		writeError(w, http.StatusBadRequest, "no_fields", "body is required")
		return
	}
	if *input.Body == "" {
		writeError(w, http.StatusBadRequest, "invalid_field", "body must be non-empty")
		return
	}

	now := nowRFC3339()
	_, err = s.db.ExecContext(r.Context(),
		`UPDATE comments SET body = ?, updated_at = ? WHERE id = ? AND deleted_at IS NULL`,
		*input.Body, now, id,
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "failed to update comment")
		return
	}

	s.audit(r.Context(), "update", "comments", id, input)
	s.fireEvents(r.Context(), "comments", "update", []string{id})

	updated, _ := s.getComment(r, id, false)
	writeJSON(w, http.StatusOK, updated)
}

func (s *Server) handleDeleteComment(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	current, err := s.getComment(r, id, false)
	if err == sql.ErrNoRows || (err == nil && !s.canAccessTeam(r.Context(), current.TeamID)) {
		writeError(w, http.StatusNotFound, "not_found", "comment not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "failed to read comment")
		return
	}
	if !s.canEditComment(r, current) {
		writeError(w, http.StatusForbidden, "forbidden", "only the author can delete this comment")
		return
	}

	now := nowRFC3339()
	_, err = s.db.ExecContext(r.Context(),
		`UPDATE comments SET deleted_at = ?, updated_at = ? WHERE id = ? AND deleted_at IS NULL`,
		now, now, id,
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "failed to delete comment")
		return
	}

	s.audit(r.Context(), "delete", "comments", id, nil)
	s.fireEvents(r.Context(), "comments", "delete", []string{id})

	deleted, _ := s.getComment(r, id, true)
	writeJSON(w, http.StatusOK, deleted)
}

func (s *Server) handleRestoreComment(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	current, err := s.getComment(r, id, true)
	if err == sql.ErrNoRows || (err == nil && !s.canAccessTeam(r.Context(), current.TeamID)) {
		writeError(w, http.StatusNotFound, "not_found", "comment not found or not deleted")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "failed to read comment")
		return
	}
	if !s.canEditComment(r, current) {
		writeError(w, http.StatusForbidden, "forbidden", "only the author can restore this comment")
		return
	}

	now := nowRFC3339()
	_, err = s.db.ExecContext(r.Context(),
		`UPDATE comments SET deleted_at = NULL, updated_at = ? WHERE id = ? AND deleted_at IS NOT NULL`,
		now, id,
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "failed to restore comment")
		return
	}

	s.audit(r.Context(), "restore", "comments", id, nil)
	s.fireEvents(r.Context(), "comments", "restore", []string{id})

	restored, _ := s.getComment(r, id, false)
	writeJSON(w, http.StatusOK, restored)
}

func (s *Server) handleBulkUpdateComments(w http.ResponseWriter, r *http.Request) {
	var body bulkUpdateRequest
	if err := readJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "invalid JSON")
		return
	}
	if len(body.Where) == 0 {
		writeError(w, http.StatusBadRequest, "missing_filter", "bulk operations require a non-empty where clause")
		return
	}
	setClause, setArgs, err := buildBulkSet(body.Set, commentSettable)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_set", err.Error())
		return
	}
	now := nowRFC3339()
	setClause += ", updated_at = ?"
	setArgs = append(setArgs, now)

	scopeFrag, scopeArgs, _ := s.scopeTeam(r.Context(), "team_id")
	result, err := s.execBulkUpdate(r.Context(), "comments", commentFields, body.Where, setClause, setArgs, scopeFrag, scopeArgs)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_filter", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleBulkDeleteComments(w http.ResponseWriter, r *http.Request) {
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
	result, err := s.execBulkDelete(r.Context(), "comments", commentFields, body.Where, true, scopeFrag, scopeArgs)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_filter", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
}
