package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/clone45/tasks127/internal/auth"
	"github.com/clone45/tasks127/internal/config"
)

type Server struct {
	cfg           config.Config
	db            *sql.DB
	mux           *http.ServeMux
	webhookWorker *webhookWorker
}

func New(cfg config.Config, db *sql.DB) *Server {
	s := &Server{cfg: cfg, db: db, mux: http.NewServeMux()}
	s.webhookWorker = newWebhookWorker(db)
	s.routes()
	return s
}

// StartWebhookWorker begins the background retry loop. Call once after New().
func (s *Server) StartWebhookWorker() {
	s.webhookWorker.Start()
}

// ShutdownWebhookWorker stops the worker gracefully, waiting up to ctx deadline.
func (s *Server) ShutdownWebhookWorker(ctx context.Context) error {
	return s.webhookWorker.Shutdown(ctx)
}

func (s *Server) Handler() http.Handler {
	return s.mux
}

func (s *Server) routes() {
	s.mux.HandleFunc("GET /healthz", s.handleHealthz)

	v1 := http.NewServeMux()
	v1.HandleFunc("GET /v1/whoami", s.handleWhoami)

	// Users
	v1.HandleFunc("PATCH /v1/users", s.handleBulkUpdateUsers)
	v1.HandleFunc("DELETE /v1/users", s.handleBulkDeleteUsers)
	v1.HandleFunc("POST /v1/users/search", s.handleSearchUsers)
	v1.HandleFunc("POST /v1/users", s.handleCreateUser)
	v1.HandleFunc("GET /v1/users/{id}", s.handleGetUser)
	v1.HandleFunc("PATCH /v1/users/{id}", s.handleUpdateUser)
	v1.HandleFunc("DELETE /v1/users/{id}", s.handleDeleteUser)
	v1.HandleFunc("POST /v1/users/{id}/restore", s.handleRestoreUser)

	// Teams
	v1.HandleFunc("PATCH /v1/teams", s.handleBulkUpdateTeams)
	v1.HandleFunc("DELETE /v1/teams", s.handleBulkDeleteTeams)
	v1.HandleFunc("POST /v1/teams/search", s.handleSearchTeams)
	v1.HandleFunc("POST /v1/teams", s.handleCreateTeam)
	v1.HandleFunc("GET /v1/teams/{id}", s.handleGetTeam)
	v1.HandleFunc("PATCH /v1/teams/{id}", s.handleUpdateTeam)
	v1.HandleFunc("DELETE /v1/teams/{id}", s.handleDeleteTeam)
	v1.HandleFunc("POST /v1/teams/{id}/restore", s.handleRestoreTeam)

	// Projects
	v1.HandleFunc("PATCH /v1/projects", s.handleBulkUpdateProjects)
	v1.HandleFunc("DELETE /v1/projects", s.handleBulkDeleteProjects)
	v1.HandleFunc("POST /v1/projects/search", s.handleSearchProjects)
	v1.HandleFunc("POST /v1/projects", s.handleCreateProject)
	v1.HandleFunc("GET /v1/projects/{id}", s.handleGetProject)
	v1.HandleFunc("PATCH /v1/projects/{id}", s.handleUpdateProject)
	v1.HandleFunc("DELETE /v1/projects/{id}", s.handleDeleteProject)
	v1.HandleFunc("POST /v1/projects/{id}/restore", s.handleRestoreProject)

	// Subscriptions (event notifications)
	v1.HandleFunc("POST /v1/subscriptions/search", s.handleSearchSubscriptions)
	v1.HandleFunc("POST /v1/subscriptions", s.handleCreateSubscription)
	v1.HandleFunc("GET /v1/subscriptions/{id}", s.handleGetSubscription)
	v1.HandleFunc("PATCH /v1/subscriptions/{id}", s.handleUpdateSubscription)
	v1.HandleFunc("DELETE /v1/subscriptions/{id}", s.handleDeleteSubscription)
	v1.HandleFunc("GET /v1/subscriptions/{id}/events", s.handleListEvents)
	v1.HandleFunc("POST /v1/subscriptions/{id}/ack", s.handleAckEvents)
	v1.HandleFunc("GET /v1/subscriptions/{id}/deliveries", s.handleListDeliveries)

	// Comments
	v1.HandleFunc("PATCH /v1/comments", s.handleBulkUpdateComments)
	v1.HandleFunc("DELETE /v1/comments", s.handleBulkDeleteComments)
	v1.HandleFunc("POST /v1/comments/search", s.handleSearchComments)
	v1.HandleFunc("POST /v1/comments", s.handleCreateComment)
	v1.HandleFunc("GET /v1/comments/{id}", s.handleGetComment)
	v1.HandleFunc("PATCH /v1/comments/{id}", s.handleUpdateComment)
	v1.HandleFunc("DELETE /v1/comments/{id}", s.handleDeleteComment)
	v1.HandleFunc("POST /v1/comments/{id}/restore", s.handleRestoreComment)

	// Tickets
	v1.HandleFunc("PATCH /v1/tickets", s.handleBulkUpdateTickets)
	v1.HandleFunc("DELETE /v1/tickets", s.handleBulkDeleteTickets)
	v1.HandleFunc("POST /v1/tickets/search", s.handleSearchTickets)
	v1.HandleFunc("POST /v1/tickets", s.handleCreateTicket)
	v1.HandleFunc("GET /v1/tickets/{id}", s.handleGetTicket)
	v1.HandleFunc("PATCH /v1/tickets/{id}", s.handleUpdateTicket)
	v1.HandleFunc("DELETE /v1/tickets/{id}", s.handleDeleteTicket)
	v1.HandleFunc("POST /v1/tickets/{id}/restore", s.handleRestoreTicket)

	// API keys (admin only)
	v1.HandleFunc("PATCH /v1/api-keys", s.handleBulkUpdateAPIKeys)
	v1.HandleFunc("DELETE /v1/api-keys", s.handleBulkDeleteAPIKeys)
	v1.HandleFunc("POST /v1/api-keys/search", s.handleSearchAPIKeys)
	v1.HandleFunc("POST /v1/api-keys", s.handleCreateAPIKey)
	v1.HandleFunc("GET /v1/api-keys/{id}", s.handleGetAPIKey)
	v1.HandleFunc("PATCH /v1/api-keys/{id}", s.handleUpdateAPIKey)
	v1.HandleFunc("DELETE /v1/api-keys/{id}", s.handleDeleteAPIKey)
	v1.HandleFunc("POST /v1/api-keys/{id}/restore", s.handleRestoreAPIKey)

	// Team members
	v1.HandleFunc("DELETE /v1/team-members", s.handleBulkDeleteTeamMembers)
	v1.HandleFunc("POST /v1/team-members/search", s.handleSearchTeamMembers)
	v1.HandleFunc("POST /v1/team-members", s.handleCreateTeamMember)
	v1.HandleFunc("GET /v1/team-members/{id}", s.handleGetTeamMember)
	v1.HandleFunc("DELETE /v1/team-members/{id}", s.handleDeleteTeamMember)
	v1.HandleFunc("POST /v1/team-members/{id}/restore", s.handleRestoreTeamMember)

	s.mux.Handle("/v1/", auth.Middleware(s.db)(v1))
}

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleWhoami(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, auth.FromContext(r.Context()))
}

// --- shared helpers ---

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{
		"error": map[string]string{"code": code, "message": message},
	})
}

func readJSON(r *http.Request, v any) error {
	return json.NewDecoder(r.Body).Decode(v)
}

func nowRFC3339() string {
	return time.Now().UTC().Format(time.RFC3339)
}

func (s *Server) activeExists(ctx context.Context, table, id string) (bool, error) {
	var exists bool
	query := fmt.Sprintf(`SELECT EXISTS(SELECT 1 FROM %s WHERE id = ? AND deleted_at IS NULL)`, table)
	err := s.db.QueryRowContext(ctx, query, id).Scan(&exists)
	return exists, err
}

func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}

// isValidKey returns true if s is exactly three uppercase A-Z letters.
// Mirrors the CHECK constraint on resource_keys.key.
func isValidKey(s string) bool {
	if len(s) != 3 {
		return false
	}
	for i := 0; i < 3; i++ {
		if s[i] < 'A' || s[i] > 'Z' {
			return false
		}
	}
	return true
}

func nullStr(ns sql.NullString) *string {
	if ns.Valid {
		return &ns.String
	}
	return nil
}
