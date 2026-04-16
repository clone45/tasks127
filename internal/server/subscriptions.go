package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/clone45/tasks127/internal/auth"
	"github.com/clone45/tasks127/internal/filter"
	"github.com/oklog/ulid/v2"
)

// --- configuration: which resources can be subscribed to ---

type subscribable struct {
	Table  string
	Fields map[string]filter.FieldSpec
	// scope returns the SQL scope fragment + args for this resource given an
	// effective user id. unrestricted=true means no scope needed.
	scope func(s *Server, userID string, unrestricted bool) (string, []any, bool)
}

var subscribableResources = map[string]subscribable{
	"tickets":      {Table: "tickets", Fields: ticketFields, scope: scopeTeamByPrincipal("team_id")},
	"comments":     {Table: "comments", Fields: commentFields, scope: scopeTeamByPrincipal("team_id")},
	"projects":     {Table: "projects", Fields: projectFields, scope: scopeTeamByPrincipal("team_id")},
	"teams":        {Table: "teams", Fields: teamFields, scope: scopeTeamByPrincipal("id")},
	"team_members": {Table: "team_members", Fields: teamMemberFields, scope: scopeTeamByPrincipal("team_id")},
	"users":        {Table: "users", Fields: userFields, scope: scopeUserByPrincipal("id")},
}

var validEventTypes = map[string]bool{
	"create": true, "update": true, "delete": true, "restore": true,
}

// scopeTeamByPrincipal / scopeUserByPrincipal are principal-driven versions of
// the request-context scope helpers (see scope.go). They let the subscription
// evaluator scope a query without a live http.Request context.
func scopeTeamByPrincipal(column string) func(*Server, string, bool) (string, []any, bool) {
	return func(s *Server, userID string, unrestricted bool) (string, []any, bool) {
		if unrestricted {
			return "", nil, true
		}
		if userID == "" {
			return "", nil, false
		}
		frag := column + ` IN (SELECT team_id FROM team_members WHERE user_id = ? AND deleted_at IS NULL)`
		return frag, []any{userID}, false
	}
}

func scopeUserByPrincipal(column string) func(*Server, string, bool) (string, []any, bool) {
	return func(s *Server, userID string, unrestricted bool) (string, []any, bool) {
		if unrestricted {
			return "", nil, true
		}
		if userID == "" {
			return "", nil, false
		}
		frag := fmt.Sprintf(`(%s = ? OR %s IN (
			SELECT DISTINCT tm2.user_id FROM team_members tm1
			JOIN team_members tm2 ON tm1.team_id = tm2.team_id
			WHERE tm1.user_id = ?
			  AND tm1.deleted_at IS NULL AND tm2.deleted_at IS NULL))`, column, column)
		return frag, []any{userID, userID}, false
	}
}

// --- response types ---

type subscriptionResponse struct {
	ID            string   `json:"id"`
	APIKeyID      string   `json:"api_key_id"`
	Name          *string  `json:"name"`
	Resource      string   `json:"resource"`
	EventTypes    []string `json:"event_types"`
	Where         any      `json:"where"`
	MaxFires      *int     `json:"max_fires"`
	FireCount     int      `json:"fire_count"`
	ExpiresAt     *string  `json:"expires_at"`
	WebhookURL    *string  `json:"webhook_url"`
	WebhookSecret string   `json:"webhook_secret,omitempty"` // only on create
	CreatedAt     string   `json:"created_at"`
	UpdatedAt     string   `json:"updated_at"`
	DeletedAt     *string  `json:"deleted_at"`
}

type subscriptionEventResponse struct {
	ID             string `json:"id"`
	SubscriptionID string `json:"subscription_id"`
	Sequence       int64  `json:"sequence"`
	Timestamp      string `json:"timestamp"`
	Resource       string `json:"resource"`
	ResourceID     string `json:"resource_id"`
	Action         string `json:"action"`
	Payload        any    `json:"payload"`
}

var subscriptionFields = map[string]filter.FieldSpec{
	"id":         {Column: "id"},
	"api_key_id": {Column: "api_key_id"},
	"name":       {Column: "name"},
	"resource":   {Column: "resource"},
	"max_fires":  {Column: "max_fires"},
	"fire_count": {Column: "fire_count"},
	"expires_at": {Column: "expires_at"},
	"created_at": {Column: "created_at"},
	"updated_at": {Column: "updated_at"},
}

var subscriptionSettable = map[string]bool{
	"name":        true,
	"expires_at":  true,
	"max_fires":   true,
	"webhook_url": true,
}

const subscriptionCols = `id, api_key_id, scope_user_id, name, resource, event_types, where_json, max_fires, fire_count, expires_at, webhook_url, created_at, updated_at, deleted_at`

func scanSubscriptionRow(scanner interface{ Scan(dest ...any) error }) (*subscriptionResponse, error) {
	var (
		id, apiKeyID, resource, eventTypesJSON, whereJSON   string
		name, expiresAt, deletedAt, scopeUserID, webhookURL sql.NullString
		maxFires                                            sql.NullInt64
		fireCount                                           int
		createdAt, updatedAt                                string
	)
	if err := scanner.Scan(&id, &apiKeyID, &scopeUserID, &name, &resource,
		&eventTypesJSON, &whereJSON, &maxFires, &fireCount,
		&expiresAt, &webhookURL, &createdAt, &updatedAt, &deletedAt); err != nil {
		return nil, err
	}

	sub := &subscriptionResponse{
		ID: id, APIKeyID: apiKeyID, Resource: resource,
		Name: nullStr(name), ExpiresAt: nullStr(expiresAt), DeletedAt: nullStr(deletedAt),
		WebhookURL: nullStr(webhookURL),
		FireCount:  fireCount, CreatedAt: createdAt, UpdatedAt: updatedAt,
	}
	if maxFires.Valid {
		n := int(maxFires.Int64)
		sub.MaxFires = &n
	}
	_ = json.Unmarshal([]byte(eventTypesJSON), &sub.EventTypes)
	_ = json.Unmarshal([]byte(whereJSON), &sub.Where)
	return sub, nil
}

// --- CRUD handlers ---

func (s *Server) handleCreateSubscription(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Name       *string        `json:"name"`
		Resource   string         `json:"resource"`
		EventTypes []string       `json:"event_types"`
		Where      map[string]any `json:"where"`
		MaxFires   *int           `json:"max_fires"`
		ExpiresAt  *string        `json:"expires_at"`
		WebhookURL *string        `json:"webhook_url"`
	}
	if err := readJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "invalid JSON")
		return
	}

	res, ok := subscribableResources[input.Resource]
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid_field", "resource is not subscribable")
		return
	}
	if len(input.EventTypes) == 0 {
		writeError(w, http.StatusBadRequest, "missing_field", "event_types is required")
		return
	}
	for _, et := range input.EventTypes {
		if !validEventTypes[et] {
			writeError(w, http.StatusBadRequest, "invalid_field", "invalid event_type: "+et)
			return
		}
	}
	if input.Where == nil {
		input.Where = map[string]any{}
	}
	// Validate the filter parses against the resource's allowed fields.
	if _, err := filter.Build(filter.SearchParams{Where: input.Where}, res.Fields); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_filter", err.Error())
		return
	}
	if input.MaxFires != nil && *input.MaxFires < 1 {
		writeError(w, http.StatusBadRequest, "invalid_field", "max_fires must be >= 1")
		return
	}

	// Webhook: validate URL if provided, generate a secret.
	var webhookURL, webhookSecret string
	if input.WebhookURL != nil && *input.WebhookURL != "" {
		if err := validateWebhookURL(*input.WebhookURL); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_field", err.Error())
			return
		}
		webhookURL = *input.WebhookURL
		secret, err := generateWebhookSecret()
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "failed to generate webhook secret")
			return
		}
		webhookSecret = secret
	}

	p := auth.FromContext(r.Context())
	userID, unrestricted := s.effectiveUserID(r.Context())
	var scopeUserID sql.NullString
	if !unrestricted {
		scopeUserID = sql.NullString{String: userID, Valid: true}
	}

	eventTypesJSON, _ := json.Marshal(input.EventTypes)
	whereJSON, _ := json.Marshal(input.Where)

	id := ulid.Make().String()
	now := nowRFC3339()

	var webhookURLArg, webhookSecretArg any
	if webhookURL != "" {
		webhookURLArg = webhookURL
		webhookSecretArg = webhookSecret
	}

	_, err := s.db.ExecContext(r.Context(),
		`INSERT INTO subscriptions (id, api_key_id, scope_user_id, name, resource, event_types, where_json, max_fires, expires_at, webhook_url, webhook_secret, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, p.APIKeyID, scopeUserID, input.Name, input.Resource,
		string(eventTypesJSON), string(whereJSON), input.MaxFires, input.ExpiresAt,
		webhookURLArg, webhookSecretArg, now, now,
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "failed to create subscription")
		return
	}

	s.audit(r.Context(), "create", "subscriptions", id, input)

	sub, _ := s.getSubscription(r.Context(), id, false)
	// Show the secret exactly once (like api keys).
	if webhookSecret != "" {
		sub.WebhookSecret = webhookSecret
	}
	writeJSON(w, http.StatusCreated, sub)
}

func (s *Server) getSubscription(ctx context.Context, id string, includeDeleted bool) (*subscriptionResponse, error) {
	query := "SELECT " + subscriptionCols + " FROM subscriptions WHERE id = ?"
	if !includeDeleted {
		query += " AND deleted_at IS NULL"
	}
	return scanSubscriptionRow(s.db.QueryRowContext(ctx, query, id))
}

// canViewSubscription: only the registering key (or admin-unrestricted) can see it.
func (s *Server) canViewSubscription(ctx context.Context, sub *subscriptionResponse) bool {
	p := auth.FromContext(ctx)
	if p == nil {
		return false
	}
	if _, unrestricted := s.effectiveUserID(ctx); unrestricted {
		return true
	}
	return sub.APIKeyID == p.APIKeyID
}

func (s *Server) handleGetSubscription(w http.ResponseWriter, r *http.Request) {
	sub, err := s.getSubscription(r.Context(), r.PathValue("id"), false)
	if err == sql.ErrNoRows {
		writeError(w, http.StatusNotFound, "not_found", "subscription not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "failed to read subscription")
		return
	}
	if !s.canViewSubscription(r.Context(), sub) {
		writeError(w, http.StatusNotFound, "not_found", "subscription not found")
		return
	}
	writeJSON(w, http.StatusOK, sub)
}

func (s *Server) handleUpdateSubscription(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	current, err := s.getSubscription(r.Context(), id, false)
	if err == sql.ErrNoRows || (err == nil && !s.canViewSubscription(r.Context(), current)) {
		writeError(w, http.StatusNotFound, "not_found", "subscription not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "failed to read subscription")
		return
	}

	var body map[string]any
	if err := readJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "invalid JSON")
		return
	}

	var sets []string
	var args []any
	for field, val := range body {
		if !subscriptionSettable[field] {
			writeError(w, http.StatusBadRequest, "immutable_field", field+" cannot be changed")
			return
		}
		if field == "webhook_url" && val != nil {
			urlStr, ok := val.(string)
			if !ok {
				writeError(w, http.StatusBadRequest, "invalid_field", "webhook_url must be a string or null")
				return
			}
			if err := validateWebhookURL(urlStr); err != nil {
				writeError(w, http.StatusBadRequest, "invalid_field", err.Error())
				return
			}
		}
		if val == nil {
			sets = append(sets, field+" = NULL")
		} else {
			sets = append(sets, field+" = ?")
			args = append(args, val)
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

	if _, err := s.db.ExecContext(r.Context(),
		"UPDATE subscriptions SET "+strings.Join(sets, ", ")+" WHERE id = ? AND deleted_at IS NULL",
		args...); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "failed to update subscription")
		return
	}

	s.audit(r.Context(), "update", "subscriptions", id, body)

	updated, _ := s.getSubscription(r.Context(), id, false)
	writeJSON(w, http.StatusOK, updated)
}

func (s *Server) handleDeleteSubscription(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")

	current, err := s.getSubscription(r.Context(), id, false)
	if err == sql.ErrNoRows || (err == nil && !s.canViewSubscription(r.Context(), current)) {
		writeError(w, http.StatusNotFound, "not_found", "subscription not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "failed to read subscription")
		return
	}

	now := nowRFC3339()
	if _, err := s.db.ExecContext(r.Context(),
		`UPDATE subscriptions SET deleted_at = ?, updated_at = ? WHERE id = ? AND deleted_at IS NULL`,
		now, now, id); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "failed to cancel subscription")
		return
	}

	s.audit(r.Context(), "delete", "subscriptions", id, nil)

	deleted, _ := s.getSubscription(r.Context(), id, true)
	writeJSON(w, http.StatusOK, deleted)
}

func (s *Server) handleSearchSubscriptions(w http.ResponseWriter, r *http.Request) {
	params, err := filter.ParseRequest(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_filter", err.Error())
		return
	}
	q, err := filter.Build(params, subscriptionFields)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_filter", err.Error())
		return
	}

	// Scope to the caller's own subscriptions (unless admin-unrestricted).
	p := auth.FromContext(r.Context())
	if _, unrestricted := s.effectiveUserID(r.Context()); !unrestricted {
		q.WhereClause = "(" + q.WhereClause + ") AND api_key_id = ?"
		q.Args = append(q.Args, p.APIKeyID)
	}

	var total int
	if err := s.db.QueryRowContext(r.Context(),
		"SELECT COUNT(*) FROM subscriptions WHERE "+q.WhereClause, q.Args...,
	).Scan(&total); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "count failed")
		return
	}

	rows, err := s.db.QueryContext(r.Context(),
		fmt.Sprintf("SELECT %s FROM subscriptions WHERE %s%s LIMIT %d OFFSET %d",
			subscriptionCols, q.WhereClause, q.OrderClause, q.Limit, q.Offset),
		q.Args...)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "search failed")
		return
	}
	defer rows.Close()

	var results []subscriptionResponse
	for rows.Next() {
		sub, err := scanSubscriptionRow(rows)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "scan failed")
			return
		}
		results = append(results, *sub)
	}
	if results == nil {
		results = []subscriptionResponse{}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"data": results, "total": total,
		"limit": q.Limit, "offset": q.Offset,
	})
}

// --- events endpoints ---

func (s *Server) handleListEvents(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	// includeDeleted=true so agents can still drain the inbox of a cancelled
	// subscription (e.g. one that just hit max_fires).
	sub, err := s.getSubscription(r.Context(), id, true)
	if err == sql.ErrNoRows || (err == nil && !s.canViewSubscription(r.Context(), sub)) {
		writeError(w, http.StatusNotFound, "not_found", "subscription not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "failed to read subscription")
		return
	}

	after := int64(0)
	if a := r.URL.Query().Get("after"); a != "" {
		n, err := strconv.ParseInt(a, 10, 64)
		if err != nil || n < 0 {
			writeError(w, http.StatusBadRequest, "invalid_field", "after must be a non-negative integer")
			return
		}
		after = n
	}
	limit := 50
	if l := r.URL.Query().Get("limit"); l != "" {
		n, err := strconv.Atoi(l)
		if err == nil && n > 0 && n <= 200 {
			limit = n
		}
	}

	rows, err := s.db.QueryContext(r.Context(),
		`SELECT id, subscription_id, sequence, timestamp, resource, resource_id, action, payload
		 FROM subscription_events
		 WHERE subscription_id = ? AND sequence > ? AND acked_at IS NULL
		 ORDER BY sequence ASC LIMIT ?`,
		id, after, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "failed to read events")
		return
	}
	defer rows.Close()

	var events []subscriptionEventResponse
	for rows.Next() {
		var ev subscriptionEventResponse
		var payloadJSON string
		if err := rows.Scan(&ev.ID, &ev.SubscriptionID, &ev.Sequence, &ev.Timestamp,
			&ev.Resource, &ev.ResourceID, &ev.Action, &payloadJSON); err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "scan failed")
			return
		}
		_ = json.Unmarshal([]byte(payloadJSON), &ev.Payload)
		events = append(events, ev)
	}
	if events == nil {
		events = []subscriptionEventResponse{}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"data":  events,
		"count": len(events),
	})
}

func (s *Server) handleAckEvents(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	// Same as listEvents: allow acking even if subscription is cancelled.
	sub, err := s.getSubscription(r.Context(), id, true)
	if err == sql.ErrNoRows || (err == nil && !s.canViewSubscription(r.Context(), sub)) {
		writeError(w, http.StatusNotFound, "not_found", "subscription not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "failed to read subscription")
		return
	}

	var body struct {
		Cursor int64 `json:"cursor"`
	}
	if err := readJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "invalid JSON")
		return
	}
	if body.Cursor < 0 {
		writeError(w, http.StatusBadRequest, "invalid_field", "cursor must be non-negative")
		return
	}

	now := nowRFC3339()
	res, err := s.db.ExecContext(r.Context(),
		`UPDATE subscription_events SET acked_at = ?
		 WHERE subscription_id = ? AND sequence <= ? AND acked_at IS NULL`,
		now, id, body.Cursor)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "ack failed")
		return
	}
	acked, _ := res.RowsAffected()

	writeJSON(w, http.StatusOK, map[string]any{
		"acked":  acked,
		"cursor": body.Cursor,
	})
}

// --- the evaluator: called from every mutation path ---

// fireEvents evaluates all active subscriptions against the given affected IDs
// and inserts matching events. Best-effort: errors are logged, never propagated
// to the caller. include_deleted is set automatically for "delete" actions.
func (s *Server) fireEvents(ctx context.Context, resource, action string, ids []string) {
	if len(ids) == 0 {
		return
	}
	res, ok := subscribableResources[resource]
	if !ok {
		return
	}

	now := time.Now().UTC().Format(time.RFC3339)

	rows, err := s.db.QueryContext(ctx,
		`SELECT id, api_key_id, scope_user_id, event_types, where_json, max_fires, fire_count, webhook_url
		 FROM subscriptions
		 WHERE resource = ? AND deleted_at IS NULL
		   AND (expires_at IS NULL OR expires_at > ?)`,
		resource, now)
	if err != nil {
		log.Printf("fireEvents: list subs: %v", err)
		return
	}

	type subInfo struct {
		id         string
		apiKeyID   string
		scopeUser  sql.NullString
		eventTypes []string
		where      map[string]any
		maxFires   sql.NullInt64
		fireCount  int
		webhookURL sql.NullString
	}
	var subs []subInfo
	for rows.Next() {
		var info subInfo
		var etJSON, whereJSON string
		if err := rows.Scan(&info.id, &info.apiKeyID, &info.scopeUser,
			&etJSON, &whereJSON, &info.maxFires, &info.fireCount, &info.webhookURL); err != nil {
			log.Printf("fireEvents: scan sub: %v", err)
			continue
		}
		_ = json.Unmarshal([]byte(etJSON), &info.eventTypes)
		_ = json.Unmarshal([]byte(whereJSON), &info.where)
		subs = append(subs, info)
	}
	rows.Close()

	for _, sub := range subs {
		// Check event type match.
		matched := false
		for _, et := range sub.eventTypes {
			if et == action {
				matched = true
				break
			}
		}
		if !matched {
			continue
		}

		// Build filtered query: rows with matching IDs that pass the subscription's filter AND scope.
		q, err := filter.Build(filter.SearchParams{
			Where:          sub.where,
			IncludeDeleted: action == "delete",
		}, res.Fields)
		if err != nil {
			log.Printf("fireEvents: build filter for %s: %v", sub.id, err)
			continue
		}

		// Apply scope (caller's visibility at creation time).
		scopeFrag, scopeArgs, unrestricted := res.scope(s, sub.scopeUser.String, !sub.scopeUser.Valid)
		q.WhereClause, q.Args = applyScope(q.WhereClause, q.Args, scopeFrag, scopeArgs, unrestricted)

		// Restrict to the affected row IDs.
		placeholders := make([]string, len(ids))
		for i := range ids {
			placeholders[i] = "?"
		}
		idFrag := "id IN (" + strings.Join(placeholders, ", ") + ")"
		fullWhere := "(" + q.WhereClause + ") AND " + idFrag
		fullArgs := append(q.Args, idsToAny(ids)...)

		// Query matching rows.
		matchRows, err := s.db.QueryContext(ctx,
			fmt.Sprintf(`SELECT * FROM %s WHERE %s`, res.Table, fullWhere), fullArgs...)
		if err != nil {
			log.Printf("fireEvents: select matches for %s: %v", sub.id, err)
			continue
		}

		matchedData, err := rowsToMaps(matchRows)
		matchRows.Close()
		if err != nil {
			log.Printf("fireEvents: scan matches for %s: %v", sub.id, err)
			continue
		}

		if len(matchedData) == 0 {
			continue
		}

		// Insert one event per matched row. Respect max_fires (stop once hit).
		remaining := -1
		if sub.maxFires.Valid {
			remaining = int(sub.maxFires.Int64) - sub.fireCount
			if remaining <= 0 {
				continue
			}
		}

		inserted := 0
		for _, row := range matchedData {
			if remaining == 0 {
				break
			}
			rowID, _ := row["id"].(string)
			payloadJSON, _ := json.Marshal(row)

			// Allocate the next sequence for this subscription.
			var seq int64
			err := s.db.QueryRowContext(ctx,
				`SELECT COALESCE(MAX(sequence), 0) + 1 FROM subscription_events WHERE subscription_id = ?`,
				sub.id).Scan(&seq)
			if err != nil {
				log.Printf("fireEvents: allocate sequence: %v", err)
				break
			}

			eventID := ulid.Make().String()
			_, err = s.db.ExecContext(ctx,
				`INSERT INTO subscription_events (id, subscription_id, sequence, resource, resource_id, action, payload)
				 VALUES (?, ?, ?, ?, ?, ?, ?)`,
				eventID, sub.id, seq, resource, rowID, action, string(payloadJSON))
			if err != nil {
				log.Printf("fireEvents: insert event: %v", err)
				continue
			}
			// Trigger webhook delivery if the subscription has a URL.
			if sub.webhookURL.Valid {
				s.maybeCreateDelivery(ctx, sub.id, eventID, sub.webhookURL.String)
			}
			inserted++
			if remaining > 0 {
				remaining--
			}
		}

		if inserted == 0 {
			continue
		}

		// Update fire_count; soft-delete if we've hit max_fires.
		newCount := sub.fireCount + inserted
		if sub.maxFires.Valid && newCount >= int(sub.maxFires.Int64) {
			_, _ = s.db.ExecContext(ctx,
				`UPDATE subscriptions SET fire_count = ?, deleted_at = ?, updated_at = ? WHERE id = ?`,
				newCount, now, now, sub.id)
		} else {
			_, _ = s.db.ExecContext(ctx,
				`UPDATE subscriptions SET fire_count = ?, updated_at = ? WHERE id = ?`,
				newCount, now, sub.id)
		}
	}
}

// --- utilities ---

func idsToAny(ids []string) []any {
	out := make([]any, len(ids))
	for i, id := range ids {
		out[i] = id
	}
	return out
}

// rowsToMaps converts *sql.Rows into a slice of column→value maps.
// Used by the subscription evaluator to build generic event payloads without
// needing a per-resource scan function.
func rowsToMaps(rows *sql.Rows) ([]map[string]any, error) {
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}

	var results []map[string]any
	for rows.Next() {
		values := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range values {
			ptrs[i] = &values[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		m := make(map[string]any, len(cols))
		for i, col := range cols {
			m[col] = values[i]
		}
		results = append(results, m)
	}
	return results, rows.Err()
}
