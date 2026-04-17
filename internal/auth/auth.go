package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Principal represents the authenticated caller for a single request.
type Principal struct {
	APIKeyID   string  `json:"api_key_id"`
	Tier       string  `json:"tier"`
	UserID     *string `json:"user_id"`
	OnBehalfOf *string `json:"on_behalf_of"`
}

type contextKey struct{}

func FromContext(ctx context.Context) *Principal {
	p, _ := ctx.Value(contextKey{}).(*Principal)
	return p
}

// Middleware returns HTTP middleware that authenticates requests via Bearer token.
// Uses SHA-256 hashing (not argon2id) because API keys are high-entropy random
// values, not user-chosen passwords. This is what GitHub, Stripe, and AWS do.
func Middleware(db *sql.DB) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			token := extractBearer(r)
			if token == "" {
				WriteError(w, http.StatusUnauthorized, "missing_token",
					"Authorization header is missing or malformed. Every request except GET /healthz requires "+
						"'Authorization: Bearer <token>' with a valid API key. If you do not have one, an admin can mint one via POST /v1/api-keys.")
				return
			}

			hash := HashKey(token)

			var (
				keyID     string
				tier      string
				userID    sql.NullString
				expiresAt sql.NullTime
			)
			err := db.QueryRowContext(r.Context(),
				`SELECT id, tier, user_id, expires_at FROM api_keys
				 WHERE key_hash = ? AND deleted_at IS NULL`, hash,
			).Scan(&keyID, &tier, &userID, &expiresAt)

			if err == sql.ErrNoRows {
				WriteError(w, http.StatusUnauthorized, "invalid_token",
					"the API key does not exist or has been revoked. Revoked keys can be restored via POST /v1/api-keys/{id}/restore "+
						"if the revocation was accidental. Otherwise an admin can mint a new key via POST /v1/api-keys.")
				return
			}
			if err != nil {
				WriteError(w, http.StatusInternalServerError, "internal", "authentication error")
				return
			}

			if expiresAt.Valid && expiresAt.Time.Before(time.Now()) {
				WriteError(w, http.StatusUnauthorized, "expired_token",
					"this API key's expires_at timestamp is in the past. An admin can extend it via PATCH /v1/api-keys/{id} "+
						"with {\"expires_at\": \"<new RFC3339 timestamp>\"}, or mint a replacement key via POST /v1/api-keys.")
				return
			}

			p := Principal{APIKeyID: keyID, Tier: tier}
			if userID.Valid {
				p.UserID = &userID.String
			}

			if obo := r.Header.Get("X-On-Behalf-Of"); obo != "" {
				if p.Tier != "admin" {
					WriteError(w, http.StatusBadRequest, "impersonation_denied",
						"X-On-Behalf-Of can only be set by admin-tier API keys. It scopes a request to the named user's "+
							"visibility and capabilities, which only admins are allowed to do. If you need this behavior, "+
							"use an admin-tier key. User-tier keys authenticate as the user they are bound to and cannot impersonate others.")
					return
				}
				var exists bool
				err := db.QueryRowContext(r.Context(),
					`SELECT EXISTS(SELECT 1 FROM users WHERE id = ? AND deleted_at IS NULL)`, obo,
				).Scan(&exists)
				if err != nil || !exists {
					WriteError(w, http.StatusBadRequest, "invalid_user", "X-On-Behalf-Of user not found")
					return
				}
				p.OnBehalfOf = &obo
			}

			// Update last_used_at synchronously. With SQLite and MaxOpenConns=1,
			// going async doesn't win anything (it just serializes behind the next
			// write) and it leaks goroutines past shutdown.
			if _, err := db.ExecContext(r.Context(),
				`UPDATE api_keys SET last_used_at = ? WHERE id = ?`,
				time.Now().UTC().Format(time.RFC3339), keyID); err != nil {
				// Log but don't fail the request — last_used_at is best-effort tracking.
				// Using stdlib log avoids pulling in a logger dep here.
			}

			ctx := context.WithValue(r.Context(), contextKey{}, &p)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func GenerateKey() (plaintext, hash, prefix string, err error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", "", "", fmt.Errorf("generate key: %w", err)
	}
	plaintext = "t127_" + base64.RawURLEncoding.EncodeToString(b)
	hash = HashKey(plaintext)
	prefix = plaintext[:12]
	return plaintext, hash, prefix, nil
}

func HashKey(key string) string {
	h := sha256.Sum256([]byte(key))
	return hex.EncodeToString(h[:])
}

func extractBearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if !strings.HasPrefix(h, "Bearer ") {
		return ""
	}
	return h[7:]
}

func WriteError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]string{"code": code, "message": message},
	})
}
