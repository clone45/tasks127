package server

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/clone45/tasks127/internal/auth"
	"github.com/clone45/tasks127/internal/config"
	"github.com/clone45/tasks127/internal/db"
	"github.com/oklog/ulid/v2"
)

// newTestServer spins up an in-memory SQLite DB, runs migrations,
// seeds a bootstrap admin key, and returns (server, plaintext admin key).
// The server is wrapped with its full routing + auth middleware.
func newTestServer(t *testing.T) (http.Handler, string, *sql.DB) {
	t.Helper()

	// Use a per-test temp file — modernc.org/sqlite shares :memory: across
	// connections within a process, which leaks state between tests.
	dbPath := filepath.Join(t.TempDir(), "test.db")
	database, err := db.Open("sqlite://" + dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.Migrate(database); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	// Seed admin key with a fresh ULID to avoid collisions.
	plaintext, hash, prefix, err := auth.GenerateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	_, err = database.Exec(
		`INSERT INTO api_keys (id, key_hash, key_prefix, tier, name) VALUES (?, ?, ?, 'admin', 'test')`,
		ulid.Make().String(), hash, prefix,
	)
	if err != nil {
		t.Fatalf("seed key: %v", err)
	}

	s := New(config.Config{}, database)
	t.Cleanup(func() { database.Close() })
	return s.Handler(), plaintext, database
}

// doJSON performs an HTTP request against the handler and returns the status
// and parsed response body.
func doJSON(t *testing.T, h http.Handler, method, path, key string, body any, extraHeaders ...[2]string) (int, map[string]any) {
	t.Helper()

	var bodyReader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		bodyReader = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, path, bodyReader)
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	for _, hdr := range extraHeaders {
		req.Header.Set(hdr[0], hdr[1])
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	var parsed map[string]any
	raw := w.Body.Bytes()
	if len(raw) > 0 && !strings.HasPrefix(w.Body.String(), "[") {
		_ = json.Unmarshal(raw, &parsed)
	}
	return w.Code, parsed
}

func TestHealthz(t *testing.T) {
	h, _, _ := newTestServer(t)
	status, body := doJSON(t, h, "GET", "/healthz", "", nil)
	if status != 200 {
		t.Fatalf("status: got %d, want 200", status)
	}
	if body["status"] != "ok" {
		t.Errorf("body: got %v, want status=ok", body)
	}
}

func TestAuthRequired(t *testing.T) {
	h, _, _ := newTestServer(t)
	status, body := doJSON(t, h, "GET", "/v1/whoami", "", nil)
	if status != 401 {
		t.Fatalf("status: got %d, want 401", status)
	}
	if body["error"] == nil {
		t.Errorf("expected error body, got: %v", body)
	}
}

func TestWhoamiAdmin(t *testing.T) {
	h, key, _ := newTestServer(t)
	status, body := doJSON(t, h, "GET", "/v1/whoami", key, nil)
	if status != 200 {
		t.Fatalf("status: got %d, want 200", status)
	}
	if body["tier"] != "admin" {
		t.Errorf("tier: got %v, want admin", body["tier"])
	}
	if body["on_behalf_of"] != nil {
		t.Errorf("on_behalf_of: got %v, want nil", body["on_behalf_of"])
	}
}

func TestCreateAndGetUser(t *testing.T) {
	h, key, _ := newTestServer(t)

	status, body := doJSON(t, h, "POST", "/v1/users", key,
		map[string]string{"name": "Alice", "email": "alice@x.com"})
	if status != 201 {
		t.Fatalf("create: got %d, want 201, body=%v", status, body)
	}
	id := body["id"].(string)

	status, body = doJSON(t, h, "GET", "/v1/users/"+id, key, nil)
	if status != 200 {
		t.Fatalf("get: got %d, want 200", status)
	}
	if body["email"] != "alice@x.com" {
		t.Errorf("email: got %v", body["email"])
	}
}

func TestUserTierCannotCreateUser(t *testing.T) {
	h, adminKey, database := newTestServer(t)

	// Admin creates Alice and a user-tier key for her.
	_, u := doJSON(t, h, "POST", "/v1/users", adminKey,
		map[string]string{"name": "Alice", "email": "alice@x.com"})
	aliceID := u["id"].(string)

	plaintext, hash, prefix, _ := auth.GenerateKey()
	_, err := database.Exec(
		`INSERT INTO api_keys (id, key_hash, key_prefix, tier, user_id, name) VALUES (?, ?, ?, 'user', ?, 'alice')`,
		ulid.Make().String(), hash, prefix, aliceID,
	)
	if err != nil {
		t.Fatalf("seed alice key: %v", err)
	}

	// Alice tries to create a user → 403.
	status, body := doJSON(t, h, "POST", "/v1/users", plaintext,
		map[string]string{"name": "Bob", "email": "bob@x.com"})
	if status != 403 {
		t.Fatalf("status: got %d, want 403, body=%v", status, body)
	}
}

func TestVisibilityScope_TicketsAcrossTeams(t *testing.T) {
	h, adminKey, database := newTestServer(t)

	// Setup: Alice in TeamA, Bob in TeamB. One ticket per team.
	_, aliceBody := doJSON(t, h, "POST", "/v1/users", adminKey,
		map[string]string{"name": "Alice", "email": "a@x.com"})
	aliceID := aliceBody["id"].(string)
	_, bobBody := doJSON(t, h, "POST", "/v1/users", adminKey,
		map[string]string{"name": "Bob", "email": "b@x.com"})
	bobID := bobBody["id"].(string)

	_, teamABody := doJSON(t, h, "POST", "/v1/teams", adminKey, map[string]string{"key": "AAA", "name": "A"})
	teamAID := teamABody["id"].(string)
	_, teamBBody := doJSON(t, h, "POST", "/v1/teams", adminKey, map[string]string{"key": "BBB", "name": "B"})
	teamBID := teamBBody["id"].(string)

	doJSON(t, h, "POST", "/v1/team-members", adminKey,
		map[string]string{"team_id": teamAID, "user_id": aliceID})
	doJSON(t, h, "POST", "/v1/team-members", adminKey,
		map[string]string{"team_id": teamBID, "user_id": bobID})

	_, tkA := doJSON(t, h, "POST", "/v1/tickets", adminKey,
		map[string]string{"team_id": teamAID, "title": "A"})
	_, tkB := doJSON(t, h, "POST", "/v1/tickets", adminKey,
		map[string]string{"team_id": teamBID, "title": "B"})

	// Give Alice a user-tier key.
	plaintext, hash, prefix, _ := auth.GenerateKey()
	_, err := database.Exec(
		`INSERT INTO api_keys (id, key_hash, key_prefix, tier, user_id, name) VALUES (?, ?, ?, 'user', ?, 'alice')`,
		ulid.Make().String(), hash, prefix, aliceID,
	)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Alice searches — should see only her team's ticket.
	status, body := doJSON(t, h, "POST", "/v1/tickets/search", plaintext, map[string]any{})
	if status != 200 {
		t.Fatalf("search: got %d", status)
	}
	total := body["total"].(float64)
	if total != 1 {
		t.Errorf("total: got %v, want 1", total)
	}
	data := body["data"].([]any)
	if len(data) != 1 || data[0].(map[string]any)["title"] != "A" {
		t.Errorf("data: got %v, want one ticket titled 'A'", data)
	}

	// Alice GETs Bob's ticket → 404 (existence not leaked).
	status, _ = doJSON(t, h, "GET", "/v1/tickets/"+tkB["id"].(string), plaintext, nil)
	if status != 404 {
		t.Errorf("cross-team GET: got %d, want 404", status)
	}

	// Alice GETs her own → 200.
	status, _ = doJSON(t, h, "GET", "/v1/tickets/"+tkA["id"].(string), plaintext, nil)
	if status != 200 {
		t.Errorf("own-team GET: got %d, want 200", status)
	}
}

func TestAdminWithOBO_Scopes(t *testing.T) {
	h, adminKey, _ := newTestServer(t)

	_, aliceBody := doJSON(t, h, "POST", "/v1/users", adminKey,
		map[string]string{"name": "Alice", "email": "a@x.com"})
	aliceID := aliceBody["id"].(string)

	// Admin+OBO=alice → admin-only action should be rejected.
	status, _ := doJSON(t, h, "POST", "/v1/users", adminKey,
		map[string]string{"name": "Bob", "email": "b@x.com"},
		[2]string{"X-On-Behalf-Of", aliceID})
	if status != 403 {
		t.Errorf("admin+OBO admin action: got %d, want 403", status)
	}

	// Admin+OBO whoami should report OBO.
	status, body := doJSON(t, h, "GET", "/v1/whoami", adminKey, nil,
		[2]string{"X-On-Behalf-Of", aliceID})
	if status != 200 {
		t.Fatalf("whoami: got %d", status)
	}
	if body["on_behalf_of"] != aliceID {
		t.Errorf("on_behalf_of: got %v, want %s", body["on_behalf_of"], aliceID)
	}
}

func TestUserTierCannotUseOBO(t *testing.T) {
	h, adminKey, database := newTestServer(t)

	_, aliceBody := doJSON(t, h, "POST", "/v1/users", adminKey,
		map[string]string{"name": "Alice", "email": "a@x.com"})
	aliceID := aliceBody["id"].(string)

	plaintext, hash, prefix, _ := auth.GenerateKey()
	_, _ = database.Exec(
		`INSERT INTO api_keys (id, key_hash, key_prefix, tier, user_id, name) VALUES (?, ?, ?, 'user', ?, 'alice')`,
		ulid.Make().String(), hash, prefix, aliceID,
	)

	status, body := doJSON(t, h, "GET", "/v1/whoami", plaintext, nil,
		[2]string{"X-On-Behalf-Of", aliceID})
	if status != 400 {
		t.Errorf("user-tier + OBO: got %d, want 400, body=%v", status, body)
	}
}

func TestBulkUpdateRequiresNonEmptyWhere(t *testing.T) {
	h, adminKey, _ := newTestServer(t)

	_, teamBody := doJSON(t, h, "POST", "/v1/teams", adminKey, map[string]string{"key": "TTT", "name": "T"})
	teamID := teamBody["id"].(string)
	doJSON(t, h, "POST", "/v1/tickets", adminKey,
		map[string]string{"team_id": teamID, "title": "A"})

	status, body := doJSON(t, h, "PATCH", "/v1/tickets", adminKey,
		map[string]any{"where": map[string]any{}, "set": map[string]any{"status": "done"}})
	if status != 400 {
		t.Errorf("empty where: got %d, want 400, body=%v", status, body)
	}
}

func TestFilterDSL_RejectsUnknownField(t *testing.T) {
	h, adminKey, _ := newTestServer(t)

	status, body := doJSON(t, h, "POST", "/v1/tickets/search", adminKey,
		map[string]any{"where": map[string]any{"secret_column": "x"}})
	if status != 400 {
		t.Errorf("unknown field: got %d, want 400, body=%v", status, body)
	}
}

func TestTicket_TwoLevelEnforcement(t *testing.T) {
	h, adminKey, _ := newTestServer(t)

	_, teamBody := doJSON(t, h, "POST", "/v1/teams", adminKey, map[string]string{"key": "TTT", "name": "T"})
	teamID := teamBody["id"].(string)

	// Create parent, then sub-ticket.
	_, parent := doJSON(t, h, "POST", "/v1/tickets", adminKey,
		map[string]string{"team_id": teamID, "title": "parent"})
	parentID := parent["id"].(string)

	_, child := doJSON(t, h, "POST", "/v1/tickets", adminKey,
		map[string]string{"team_id": teamID, "title": "child", "parent_id": parentID})
	childID := child["id"].(string)

	// Try to create a sub-sub-ticket → should fail.
	status, body := doJSON(t, h, "POST", "/v1/tickets", adminKey,
		map[string]string{"team_id": teamID, "title": "grandchild", "parent_id": childID})
	if status != 422 {
		t.Errorf("sub-sub-ticket: got %d, want 422, body=%v", status, body)
	}
}

func TestAuditLogRecordsMutations(t *testing.T) {
	h, adminKey, database := newTestServer(t)

	doJSON(t, h, "POST", "/v1/users", adminKey,
		map[string]string{"name": "Alice", "email": "a@x.com"})
	doJSON(t, h, "POST", "/v1/teams", adminKey, map[string]string{"key": "TTT", "name": "T"})

	var count int
	database.QueryRow("SELECT COUNT(*) FROM audit_log").Scan(&count)
	if count < 2 {
		t.Errorf("audit entries: got %d, want >= 2", count)
	}

	var resources []string
	rows, _ := database.Query("SELECT DISTINCT resource FROM audit_log ORDER BY resource")
	for rows.Next() {
		var r string
		rows.Scan(&r)
		resources = append(resources, r)
	}
	rows.Close()

	gotUsers := false
	gotTeams := false
	for _, r := range resources {
		if r == "users" {
			gotUsers = true
		}
		if r == "teams" {
			gotTeams = true
		}
	}
	if !gotUsers || !gotTeams {
		t.Errorf("audit resources: got %v, want both users and teams", resources)
	}
}
