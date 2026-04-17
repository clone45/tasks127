package server

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/clone45/tasks127/internal/filter"
	"github.com/oklog/ulid/v2"
)

var validStatuses = map[string]bool{
	"open": true, "in_progress": true, "blocked": true,
	"done": true, "canceled": true,
}

var ticketFields = map[string]filter.FieldSpec{
	"id":               {Column: "id"},
	"key":              {Column: "key"},
	"number":           {Column: "number"},
	"team_id":          {Column: "team_id"},
	"project_id":       {Column: "project_id"},
	"parent_id":        {Column: "parent_id"},
	"title":            {Column: "title"},
	"description":      {Column: "description"},
	"status":           {Column: "status"},
	"priority":         {Column: "priority"},
	"assignee_user_id": {Column: "assignee_user_id"},
	"created_at":       {Column: "created_at"},
	"updated_at":       {Column: "updated_at"},
}

// Valid priority values. Matches Linear's convention:
//   0 = None (default), 1 = Urgent, 2 = High, 3 = Medium, 4 = Low.
// Sort order in Linear's UI is 1, 2, 3, 4, 0 (None shown last), which is not
// what a plain ORDER BY produces. Callers wanting that order should filter
// priority=0 out, or sort client-side.
const (
	priorityMin = 0
	priorityMax = 4
)

type ticketResponse struct {
	ID             string  `json:"id"`
	DisplayID      string  `json:"display_id"`
	Key            string  `json:"key"`
	Number         int     `json:"number"`
	TeamID         string  `json:"team_id"`
	ProjectID      *string `json:"project_id"`
	ParentID       *string `json:"parent_id"`
	Title          string  `json:"title"`
	Description    string  `json:"description"`
	Status         string  `json:"status"`
	Priority       int     `json:"priority"`
	AssigneeUserID *string `json:"assignee_user_id"`
	CreatedAt      string  `json:"created_at"`
	UpdatedAt      string  `json:"updated_at"`
	DeletedAt      *string `json:"deleted_at"`
}

const ticketCols = `id, key, number, team_id, project_id, parent_id, title, description, status, priority, assignee_user_id, created_at, updated_at, deleted_at`

func scanTicketRow(scanner interface{ Scan(dest ...any) error }) (*ticketResponse, error) {
	var t ticketResponse
	var key, projectID, parentID, assigneeID, deletedAt sql.NullString
	var number sql.NullInt64
	err := scanner.Scan(
		&t.ID, &key, &number, &t.TeamID, &projectID, &parentID,
		&t.Title, &t.Description, &t.Status, &t.Priority, &assigneeID,
		&t.CreatedAt, &t.UpdatedAt, &deletedAt,
	)
	t.Key = key.String
	if number.Valid {
		t.Number = int(number.Int64)
	}
	if t.Key != "" && t.Number > 0 {
		t.DisplayID = fmt.Sprintf("%s-%d", t.Key, t.Number)
	}
	t.ProjectID = nullStr(projectID)
	t.ParentID = nullStr(parentID)
	t.AssigneeUserID = nullStr(assigneeID)
	t.DeletedAt = nullStr(deletedAt)
	return &t, err
}

// resolveTicketID turns a path param into an internal ULID.
// Accepts either a raw ULID or a display ID like "FOO-14".
// Returns "" if the form is a display ID that doesn't match any ticket.
func (s *Server) resolveTicketID(ctx context.Context, idOrDisplay string) (string, error) {
	dash := strings.IndexByte(idOrDisplay, '-')
	if dash == -1 {
		return idOrDisplay, nil
	}
	key := idOrDisplay[:dash]
	numStr := idOrDisplay[dash+1:]
	if !isValidKey(key) {
		return "", nil
	}
	num, err := strconv.Atoi(numStr)
	if err != nil || num < 1 {
		return "", nil
	}
	var id string
	err = s.db.QueryRowContext(ctx,
		`SELECT id FROM tickets WHERE key = ? AND number = ? AND deleted_at IS NULL`,
		key, num,
	).Scan(&id)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return id, err
}

func (s *Server) getTicket(ctx context.Context, id string, includeDeleted bool) (*ticketResponse, error) {
	query := "SELECT " + ticketCols + " FROM tickets WHERE id = ?"
	if !includeDeleted {
		query += " AND deleted_at IS NULL"
	}
	return scanTicketRow(s.db.QueryRowContext(ctx, query, id))
}

// --- validation helpers ---

func (s *Server) validateProjectInTeam(ctx context.Context, projectID, teamID string) error {
	var ok bool
	s.db.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM projects WHERE id = ? AND team_id = ? AND deleted_at IS NULL)`,
		projectID, teamID,
	).Scan(&ok)
	if !ok {
		return fmt.Errorf("project not found or not in this team")
	}
	return nil
}

func (s *Server) validateParentTicket(ctx context.Context, parentID, teamID string) error {
	var pTeamID string
	var pParentID sql.NullString
	err := s.db.QueryRowContext(ctx,
		`SELECT team_id, parent_id FROM tickets WHERE id = ? AND deleted_at IS NULL`, parentID,
	).Scan(&pTeamID, &pParentID)
	if err == sql.ErrNoRows {
		return fmt.Errorf("parent ticket not found")
	}
	if err != nil {
		return err
	}
	if pTeamID != teamID {
		return fmt.Errorf("parent ticket is in a different team")
	}
	if pParentID.Valid {
		return fmt.Errorf("parent is already a sub-ticket (two-level limit)")
	}
	return nil
}

func (s *Server) validateAssigneeInTeam(ctx context.Context, userID, teamID string) error {
	var ok bool
	s.db.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM team_members WHERE user_id = ? AND team_id = ? AND deleted_at IS NULL)`,
		userID, teamID,
	).Scan(&ok)
	if !ok {
		return fmt.Errorf("assignee is not a member of the ticket's team")
	}
	return nil
}

func (s *Server) ticketHasChildren(ctx context.Context, ticketID string) (bool, error) {
	var has bool
	err := s.db.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM tickets WHERE parent_id = ? AND deleted_at IS NULL)`, ticketID,
	).Scan(&has)
	return has, err
}

// --- handlers ---

func (s *Server) handleSearchTickets(w http.ResponseWriter, r *http.Request) {
	params, err := filter.ParseRequest(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_filter", err.Error())
		return
	}
	q, err := filter.Build(params, ticketFields)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_filter", err.Error())
		return
	}

	scopeFrag, scopeArgs, unrestricted := s.scopeTeam(r.Context(), "team_id")
	q.WhereClause, q.Args = applyScope(q.WhereClause, q.Args, scopeFrag, scopeArgs, unrestricted)

	var total int
	if err := s.db.QueryRowContext(r.Context(),
		"SELECT COUNT(*) FROM tickets WHERE "+q.WhereClause, q.Args...,
	).Scan(&total); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "count failed")
		return
	}

	rows, err := s.db.QueryContext(r.Context(),
		fmt.Sprintf("SELECT %s FROM tickets WHERE %s%s LIMIT %d OFFSET %d",
			ticketCols, q.WhereClause, q.OrderClause, q.Limit, q.Offset),
		q.Args...,
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "search failed")
		return
	}
	defer rows.Close()

	var results []ticketResponse
	for rows.Next() {
		t, err := scanTicketRow(rows)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "scan failed")
			return
		}
		results = append(results, *t)
	}
	if results == nil {
		results = []ticketResponse{}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"data": results, "total": total,
		"limit": q.Limit, "offset": q.Offset,
	})
}

func (s *Server) handleCreateTicket(w http.ResponseWriter, r *http.Request) {
	var input struct {
		TeamID         string  `json:"team_id"`
		ProjectID      *string `json:"project_id"`
		ParentID       *string `json:"parent_id"`
		Title          string  `json:"title"`
		Description    string  `json:"description"`
		Status         string  `json:"status"`
		Priority       *int    `json:"priority"`
		AssigneeUserID *string `json:"assignee_user_id"`
	}
	if err := readJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "invalid JSON")
		return
	}
	if input.TeamID == "" || input.Title == "" {
		writeError(w, http.StatusBadRequest, "missing_field", "team_id and title are required")
		return
	}
	if input.Status == "" {
		input.Status = "open"
	}
	if !validStatuses[input.Status] {
		writeError(w, http.StatusBadRequest, "invalid_field", "status must be one of: open, in_progress, blocked, done, canceled")
		return
	}
	priority := 0
	if input.Priority != nil {
		priority = *input.Priority
		if priority < priorityMin || priority > priorityMax {
			writeError(w, http.StatusBadRequest, "invalid_field", "priority must be an integer 0-4 (0=None, 1=Urgent, 2=High, 3=Medium, 4=Low)")
			return
		}
	}

	ctx := r.Context()

	if !s.canAccessTeam(ctx, input.TeamID) {
		writeError(w, http.StatusBadRequest, "invalid_reference", "team not found")
		return
	}
	if ok, _ := s.activeExists(ctx, "teams", input.TeamID); !ok {
		writeError(w, http.StatusBadRequest, "invalid_reference", "team not found")
		return
	}
	if input.ProjectID != nil {
		if err := s.validateProjectInTeam(ctx, *input.ProjectID, input.TeamID); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_reference", err.Error())
			return
		}
	}
	if input.ParentID != nil {
		if err := s.validateParentTicket(ctx, *input.ParentID, input.TeamID); err != nil {
			writeError(w, http.StatusUnprocessableEntity, "invalid_parent", err.Error())
			return
		}
	}
	if input.AssigneeUserID != nil {
		if err := s.validateAssigneeInTeam(ctx, *input.AssigneeUserID, input.TeamID); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_reference", err.Error())
			return
		}
	}

	// Determine effective key: project's if assigned, else team's.
	var ownerType, ownerID string
	if input.ProjectID != nil {
		ownerType, ownerID = "project", *input.ProjectID
	} else {
		ownerType, ownerID = "team", input.TeamID
	}

	id := ulid.Make().String()
	now := nowRFC3339()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "failed to start transaction")
		return
	}
	defer tx.Rollback()

	// Atomically allocate a number and read the key in one go.
	var key string
	var number int
	err = tx.QueryRowContext(ctx,
		`UPDATE resource_keys SET next_number = next_number + 1
		 WHERE owner_type = ? AND owner_id = ?
		 RETURNING key, next_number - 1`,
		ownerType, ownerID,
	).Scan(&key, &number)
	if err == sql.ErrNoRows {
		writeError(w, http.StatusBadRequest, "invalid_reference", "owning team or project has no key")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "failed to allocate ticket number")
		return
	}

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO tickets (id, key, number, team_id, project_id, parent_id, title, description, status, priority, assignee_user_id, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, key, number, input.TeamID, input.ProjectID, input.ParentID,
		input.Title, input.Description, input.Status, priority, input.AssigneeUserID,
		now, now,
	); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "failed to create ticket")
		return
	}

	if err := tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "failed to commit")
		return
	}

	s.audit(ctx, "create", "tickets", id, input)
	s.fireEvents(ctx, "tickets", "create", []string{id})

	t, _ := s.getTicket(ctx, id, false)
	writeJSON(w, http.StatusCreated, t)
}

func (s *Server) handleGetTicket(w http.ResponseWriter, r *http.Request) {
	id, err := s.resolveTicketID(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "failed to resolve ticket id")
		return
	}
	if id == "" {
		writeError(w, http.StatusNotFound, "not_found", "ticket not found")
		return
	}
	t, err := s.getTicket(r.Context(), id, false)
	if err == sql.ErrNoRows {
		writeError(w, http.StatusNotFound, "not_found", "ticket not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "failed to read ticket")
		return
	}
	if !s.canAccessTeam(r.Context(), t.TeamID) {
		writeError(w, http.StatusNotFound, "not_found", "ticket not found")
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (s *Server) handleUpdateTicket(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, err := s.resolveTicketID(ctx, r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "failed to resolve ticket id")
		return
	}
	if id == "" {
		writeError(w, http.StatusNotFound, "not_found", "ticket not found")
		return
	}

	current, err := s.getTicket(ctx, id, false)
	if err == sql.ErrNoRows {
		writeError(w, http.StatusNotFound, "not_found", "ticket not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "failed to read ticket")
		return
	}
	if !s.canAccessTeam(ctx, current.TeamID) {
		writeError(w, http.StatusNotFound, "not_found", "ticket not found")
		return
	}

	var body map[string]any
	if err := readJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "invalid JSON")
		return
	}

	if _, ok := body["team_id"]; ok {
		writeError(w, http.StatusBadRequest, "immutable_field", "team_id cannot be changed after creation")
		return
	}

	var sets []string
	var args []any

	if val, ok := body["title"]; ok {
		s, ok := val.(string)
		if !ok || s == "" {
			writeError(w, http.StatusBadRequest, "invalid_field", "title must be a non-empty string")
			return
		}
		sets = append(sets, "title = ?")
		args = append(args, s)
	}

	if val, ok := body["description"]; ok {
		s, ok := val.(string)
		if !ok {
			writeError(w, http.StatusBadRequest, "invalid_field", "description must be a string")
			return
		}
		sets = append(sets, "description = ?")
		args = append(args, s)
	}

	if val, ok := body["status"]; ok {
		s, ok := val.(string)
		if !ok || !validStatuses[s] {
			writeError(w, http.StatusBadRequest, "invalid_field", "status must be one of: open, in_progress, blocked, done, canceled")
			return
		}
		sets = append(sets, "status = ?")
		args = append(args, s)
	}

	if val, ok := body["priority"]; ok {
		// JSON numbers decode into float64 by default.
		f, ok := val.(float64)
		if !ok || f != float64(int(f)) {
			writeError(w, http.StatusBadRequest, "invalid_field", "priority must be an integer 0-4")
			return
		}
		p := int(f)
		if p < priorityMin || p > priorityMax {
			writeError(w, http.StatusBadRequest, "invalid_field", "priority must be 0-4 (0=None, 1=Urgent, 2=High, 3=Medium, 4=Low)")
			return
		}
		sets = append(sets, "priority = ?")
		args = append(args, p)
	}

	if val, ok := body["project_id"]; ok {
		if val == nil {
			sets = append(sets, "project_id = NULL")
		} else {
			pid, ok := val.(string)
			if !ok {
				writeError(w, http.StatusBadRequest, "invalid_field", "project_id must be a string or null")
				return
			}
			if err := s.validateProjectInTeam(ctx, pid, current.TeamID); err != nil {
				writeError(w, http.StatusBadRequest, "invalid_reference", err.Error())
				return
			}
			sets = append(sets, "project_id = ?")
			args = append(args, pid)
		}
	}

	if val, ok := body["parent_id"]; ok {
		if val == nil {
			sets = append(sets, "parent_id = NULL")
		} else {
			pid, ok := val.(string)
			if !ok {
				writeError(w, http.StatusBadRequest, "invalid_field", "parent_id must be a string or null")
				return
			}
			if pid == id {
				writeError(w, http.StatusUnprocessableEntity, "invalid_parent", "a ticket cannot be its own parent")
				return
			}
			if err := s.validateParentTicket(ctx, pid, current.TeamID); err != nil {
				writeError(w, http.StatusUnprocessableEntity, "invalid_parent", err.Error())
				return
			}
			hasChildren, _ := s.ticketHasChildren(ctx, id)
			if hasChildren {
				writeError(w, http.StatusUnprocessableEntity, "invalid_parent", "ticket has sub-tickets and cannot become a sub-ticket itself (two-level limit)")
				return
			}
			sets = append(sets, "parent_id = ?")
			args = append(args, pid)
		}
	}

	if val, ok := body["assignee_user_id"]; ok {
		if val == nil {
			sets = append(sets, "assignee_user_id = NULL")
		} else {
			uid, ok := val.(string)
			if !ok {
				writeError(w, http.StatusBadRequest, "invalid_field", "assignee_user_id must be a string or null")
				return
			}
			if err := s.validateAssigneeInTeam(ctx, uid, current.TeamID); err != nil {
				writeError(w, http.StatusBadRequest, "invalid_reference", err.Error())
				return
			}
			sets = append(sets, "assignee_user_id = ?")
			args = append(args, uid)
		}
	}

	if len(sets) == 0 {
		writeError(w, http.StatusBadRequest, "no_fields", "no fields to update")
		return
	}

	now := nowRFC3339()
	sets = append(sets, "updated_at = ?")
	args = append(args, now)
	args = append(args, id)

	query := "UPDATE tickets SET " + strings.Join(sets, ", ") + " WHERE id = ? AND deleted_at IS NULL"
	if _, err := s.db.ExecContext(ctx, query, args...); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "failed to update ticket")
		return
	}

	s.audit(ctx, "update", "tickets", id, body)
	s.fireEvents(ctx, "tickets", "update", []string{id})

	t, _ := s.getTicket(ctx, id, false)
	writeJSON(w, http.StatusOK, t)
}

func (s *Server) handleDeleteTicket(w http.ResponseWriter, r *http.Request) {
	id, err := s.resolveTicketID(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "failed to resolve ticket id")
		return
	}
	if id == "" {
		writeError(w, http.StatusNotFound, "not_found", "ticket not found")
		return
	}

	t, err := s.getTicket(r.Context(), id, false)
	if err == sql.ErrNoRows || (err == nil && !s.canAccessTeam(r.Context(), t.TeamID)) {
		writeError(w, http.StatusNotFound, "not_found", "ticket not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "failed to read ticket")
		return
	}

	now := nowRFC3339()
	res, err := s.db.ExecContext(r.Context(),
		`UPDATE tickets SET deleted_at = ?, updated_at = ? WHERE id = ? AND deleted_at IS NULL`,
		now, now, id,
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "failed to delete ticket")
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		writeError(w, http.StatusNotFound, "not_found", "ticket not found")
		return
	}

	s.audit(r.Context(), "delete", "tickets", id, nil)
	s.fireEvents(r.Context(), "tickets", "delete", []string{id})

	deleted, _ := s.getTicket(r.Context(), id, true)
	writeJSON(w, http.StatusOK, deleted)
}

func (s *Server) handleRestoreTicket(w http.ResponseWriter, r *http.Request) {
	id, err := s.resolveTicketID(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "failed to resolve ticket id")
		return
	}
	if id == "" {
		// Soft-deleted tickets aren't resolvable by display ID since resolver
		// filters on deleted_at IS NULL. Fall back to the raw path value.
		id = r.PathValue("id")
	}

	t, err := s.getTicket(r.Context(), id, true)
	if err == sql.ErrNoRows || (err == nil && !s.canAccessTeam(r.Context(), t.TeamID)) {
		writeError(w, http.StatusNotFound, "not_found", "ticket not found or not deleted")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "failed to read ticket")
		return
	}

	now := nowRFC3339()
	res, err := s.db.ExecContext(r.Context(),
		`UPDATE tickets SET deleted_at = NULL, updated_at = ? WHERE id = ? AND deleted_at IS NOT NULL`,
		now, id,
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "failed to restore ticket")
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		writeError(w, http.StatusNotFound, "not_found", "ticket not found or not deleted")
		return
	}

	s.audit(r.Context(), "restore", "tickets", id, nil)
	s.fireEvents(r.Context(), "tickets", "restore", []string{id})

	restored, _ := s.getTicket(r.Context(), id, false)
	writeJSON(w, http.StatusOK, restored)
}

var ticketSettable = map[string]bool{
	"title": true, "description": true, "status": true, "priority": true,
	"project_id": true, "assignee_user_id": true,
}

func (s *Server) handleBulkUpdateTickets(w http.ResponseWriter, r *http.Request) {
	var body bulkUpdateRequest
	if err := readJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "invalid JSON")
		return
	}
	if len(body.Where) == 0 {
		writeError(w, http.StatusBadRequest, "missing_filter", "bulk operations require a non-empty where clause")
		return
	}
	if _, ok := body.Set["parent_id"]; ok {
		writeError(w, http.StatusBadRequest, "invalid_set", "parent_id cannot be changed in bulk (two-level rule requires per-ticket validation)")
		return
	}
	if v, ok := body.Set["status"]; ok {
		s, isStr := v.(string)
		if !isStr || !validStatuses[s] {
			writeError(w, http.StatusBadRequest, "invalid_set", "status must be one of: open, in_progress, blocked, done, canceled")
			return
		}
	}
	if v, ok := body.Set["priority"]; ok {
		f, isNum := v.(float64)
		if !isNum || f != float64(int(f)) || int(f) < priorityMin || int(f) > priorityMax {
			writeError(w, http.StatusBadRequest, "invalid_set", "priority must be an integer 0-4")
			return
		}
	}

	setClause, setArgs, err := buildBulkSet(body.Set, ticketSettable)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_set", err.Error())
		return
	}
	now := nowRFC3339()
	setClause += ", updated_at = ?"
	setArgs = append(setArgs, now)

	scopeFrag, scopeArgs, _ := s.scopeTeam(r.Context(), "team_id")
	result, err := s.execBulkUpdate(r.Context(), "tickets", ticketFields, body.Where, setClause, setArgs, scopeFrag, scopeArgs)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_filter", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleBulkDeleteTickets(w http.ResponseWriter, r *http.Request) {
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
	result, err := s.execBulkDelete(r.Context(), "tickets", ticketFields, body.Where, true, scopeFrag, scopeArgs)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_filter", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
}
