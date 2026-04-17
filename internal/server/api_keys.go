package server

import (
	"database/sql"
	"fmt"
	"net/http"
	"strings"

	"github.com/clone45/tasks127/internal/auth"
	"github.com/clone45/tasks127/internal/filter"
	"github.com/oklog/ulid/v2"
)

var apiKeyFields = map[string]filter.FieldSpec{
	"id":           {Column: "id"},
	"key_prefix":   {Column: "key_prefix"},
	"tier":         {Column: "tier"},
	"user_id":      {Column: "user_id"},
	"name":         {Column: "name"},
	"expires_at":   {Column: "expires_at"},
	"last_used_at": {Column: "last_used_at"},
	"created_at":   {Column: "created_at"},
}

var apiKeySettable = map[string]bool{
	"name":       true,
	"expires_at": true,
}

type apiKeyResponse struct {
	ID         string  `json:"id"`
	Key        string  `json:"key,omitempty"`
	KeyPrefix  string  `json:"key_prefix"`
	Tier       string  `json:"tier"`
	UserID     *string `json:"user_id"`
	Name       string  `json:"name"`
	ExpiresAt  *string `json:"expires_at"`
	LastUsedAt *string `json:"last_used_at"`
	CreatedAt  string  `json:"created_at"`
	DeletedAt  *string `json:"deleted_at"`
}

const apiKeyCols = `id, key_prefix, tier, user_id, name, expires_at, last_used_at, created_at, deleted_at`

func scanAPIKeyRow(scanner interface{ Scan(dest ...any) error }) (*apiKeyResponse, error) {
	var k apiKeyResponse
	var userID, expiresAt, lastUsedAt, deletedAt sql.NullString
	err := scanner.Scan(
		&k.ID, &k.KeyPrefix, &k.Tier, &userID, &k.Name,
		&expiresAt, &lastUsedAt, &k.CreatedAt, &deletedAt,
	)
	k.UserID = nullStr(userID)
	k.ExpiresAt = nullStr(expiresAt)
	k.LastUsedAt = nullStr(lastUsedAt)
	k.DeletedAt = nullStr(deletedAt)
	return &k, err
}

func (s *Server) getAPIKey(r *http.Request, id string, includeDeleted bool) (*apiKeyResponse, error) {
	query := "SELECT " + apiKeyCols + " FROM api_keys WHERE id = ?"
	if !includeDeleted {
		query += " AND deleted_at IS NULL"
	}
	return scanAPIKeyRow(s.db.QueryRowContext(r.Context(), query, id))
}

func (s *Server) handleSearchAPIKeys(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	params, err := filter.ParseRequest(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_filter", err.Error())
		return
	}
	q, err := filter.Build(params, apiKeyFields)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_filter", err.Error())
		return
	}

	var total int
	if err := s.db.QueryRowContext(r.Context(),
		"SELECT COUNT(*) FROM api_keys WHERE "+q.WhereClause, q.Args...,
	).Scan(&total); err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "count failed")
		return
	}

	rows, err := s.db.QueryContext(r.Context(),
		fmt.Sprintf("SELECT %s FROM api_keys WHERE %s%s LIMIT %d OFFSET %d",
			apiKeyCols, q.WhereClause, q.OrderClause, q.Limit, q.Offset),
		q.Args...,
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "search failed")
		return
	}
	defer rows.Close()

	var results []apiKeyResponse
	for rows.Next() {
		k, err := scanAPIKeyRow(rows)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal", "scan failed")
			return
		}
		results = append(results, *k)
	}
	if results == nil {
		results = []apiKeyResponse{}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"data": results, "total": total,
		"limit": q.Limit, "offset": q.Offset,
	})
}

func (s *Server) handleCreateAPIKey(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}

	var input struct {
		Tier      string  `json:"tier"`
		UserID    *string `json:"user_id"`
		Name      string  `json:"name"`
		ExpiresAt *string `json:"expires_at"`
	}
	if err := readJSON(r, &input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "invalid JSON")
		return
	}
	if input.Name == "" {
		writeError(w, http.StatusBadRequest, "missing_field", "name is required")
		return
	}
	if input.Tier != "admin" && input.Tier != "user" {
		writeError(w, http.StatusBadRequest, "invalid_field", "tier must be 'admin' or 'user'")
		return
	}
	if input.Tier == "user" && input.UserID == nil {
		writeError(w, http.StatusBadRequest, "missing_field", "user_id is required for tier=user")
		return
	}
	if input.Tier == "admin" && input.UserID != nil {
		writeError(w, http.StatusBadRequest, "invalid_field", "user_id must be null for tier=admin")
		return
	}
	if input.UserID != nil {
		if ok, _ := s.activeExists(r.Context(), "users", *input.UserID); !ok {
			writeError(w, http.StatusBadRequest, "invalid_reference",
				"user not found. The user_id you passed does not exist or is soft-deleted. "+
					"Verify it with search_users. If you meant to create an admin-tier key (which has no associated user), "+
					"omit user_id and set tier to \"admin\" instead.")
			return
		}
	}

	plaintext, hash, prefix, err := auth.GenerateKey()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "failed to generate key")
		return
	}

	id := ulid.Make().String()
	now := nowRFC3339()

	_, err = s.db.ExecContext(r.Context(),
		`INSERT INTO api_keys (id, key_hash, key_prefix, tier, user_id, name, expires_at, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		id, hash, prefix, input.Tier, input.UserID, input.Name, input.ExpiresAt, now,
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "failed to create api key")
		return
	}

	s.audit(r.Context(), "create", "api_keys", id, map[string]any{
		"tier": input.Tier, "user_id": input.UserID, "name": input.Name,
	})

	writeJSON(w, http.StatusCreated, &apiKeyResponse{
		ID: id, Key: plaintext, KeyPrefix: prefix,
		Tier: input.Tier, UserID: input.UserID, Name: input.Name,
		ExpiresAt: input.ExpiresAt, CreatedAt: now,
	})
}

func (s *Server) handleGetAPIKey(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	k, err := s.getAPIKey(r, r.PathValue("id"), false)
	if err == sql.ErrNoRows {
		writeError(w, http.StatusNotFound, "not_found", "api key not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "failed to read api key")
		return
	}
	writeJSON(w, http.StatusOK, k)
}

func (s *Server) handleUpdateAPIKey(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	id := r.PathValue("id")

	var body map[string]any
	if err := readJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_body", "invalid JSON")
		return
	}

	var sets []string
	var args []any

	if val, ok := body["name"]; ok {
		s, ok := val.(string)
		if !ok || s == "" {
			writeError(w, http.StatusBadRequest, "invalid_field", "name must be a non-empty string")
			return
		}
		sets = append(sets, "name = ?")
		args = append(args, s)
	}

	if val, ok := body["expires_at"]; ok {
		if val == nil {
			sets = append(sets, "expires_at = NULL")
		} else {
			str, ok := val.(string)
			if !ok {
				writeError(w, http.StatusBadRequest, "invalid_field", "expires_at must be a string or null")
				return
			}
			sets = append(sets, "expires_at = ?")
			args = append(args, str)
		}
	}

	for _, forbidden := range []string{"tier", "user_id", "key_hash", "key_prefix", "last_used_at"} {
		if _, ok := body[forbidden]; ok {
			writeError(w, http.StatusBadRequest, "immutable_field", forbidden+" cannot be changed")
			return
		}
	}

	if len(sets) == 0 {
		writeError(w, http.StatusBadRequest, "no_fields", "no fields to update")
		return
	}

	args = append(args, id)
	query := "UPDATE api_keys SET " + strings.Join(sets, ", ") + " WHERE id = ? AND deleted_at IS NULL"
	res, err := s.db.ExecContext(r.Context(), query, args...)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "failed to update api key")
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		writeError(w, http.StatusNotFound, "not_found", "api key not found")
		return
	}

	s.audit(r.Context(), "update", "api_keys", id, body)

	k, _ := s.getAPIKey(r, id, false)
	writeJSON(w, http.StatusOK, k)
}

func (s *Server) handleDeleteAPIKey(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	id := r.PathValue("id")
	now := nowRFC3339()

	res, err := s.db.ExecContext(r.Context(),
		`UPDATE api_keys SET deleted_at = ? WHERE id = ? AND deleted_at IS NULL`, now, id,
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "failed to revoke api key")
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		writeError(w, http.StatusNotFound, "not_found", "api key not found")
		return
	}

	s.audit(r.Context(), "delete", "api_keys", id, nil)

	k, _ := s.getAPIKey(r, id, true)
	writeJSON(w, http.StatusOK, k)
}

func (s *Server) handleRestoreAPIKey(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	id := r.PathValue("id")

	res, err := s.db.ExecContext(r.Context(),
		`UPDATE api_keys SET deleted_at = NULL WHERE id = ? AND deleted_at IS NOT NULL`, id,
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal", "failed to restore api key")
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		writeError(w, http.StatusNotFound, "not_found", "api key not found or not deleted")
		return
	}

	s.audit(r.Context(), "restore", "api_keys", id, nil)

	k, _ := s.getAPIKey(r, id, false)
	writeJSON(w, http.StatusOK, k)
}

func (s *Server) handleBulkUpdateAPIKeys(w http.ResponseWriter, r *http.Request) {
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
	setClause, setArgs, err := buildBulkSet(body.Set, apiKeySettable)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_set", err.Error())
		return
	}

	result, err := s.execBulkUpdate(r.Context(), "api_keys", apiKeyFields, body.Where, setClause, setArgs, "", nil)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_filter", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleBulkDeleteAPIKeys(w http.ResponseWriter, r *http.Request) {
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
	result, err := s.execBulkDelete(r.Context(), "api_keys", apiKeyFields, body.Where, false, "", nil)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_filter", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
}
