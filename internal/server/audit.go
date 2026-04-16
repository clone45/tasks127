package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"log"

	"github.com/clone45/tasks127/internal/auth"
	"github.com/oklog/ulid/v2"
)

// audit records a mutation to the audit_log table. Best-effort: failures
// are logged but never surface to the caller, so an audit problem never
// breaks a user-facing request.
func (s *Server) audit(ctx context.Context, action, resource, resourceID string, change any) {
	p := auth.FromContext(ctx)
	if p == nil {
		return
	}

	var changeStr string
	if change != nil {
		b, err := json.Marshal(change)
		if err != nil {
			log.Printf("audit: marshal change: %v", err)
			changeStr = "{}"
		} else {
			changeStr = string(b)
		}
	} else {
		changeStr = "{}"
	}

	var onBehalf sql.NullString
	if p.OnBehalfOf != nil {
		onBehalf = sql.NullString{String: *p.OnBehalfOf, Valid: true}
	}

	_, err := s.db.ExecContext(ctx,
		`INSERT INTO audit_log (id, timestamp, actor_api_key_id, on_behalf_of_user_id, resource, resource_id, action, change)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		ulid.Make().String(), nowRFC3339(), p.APIKeyID, onBehalf, resource, resourceID, action, changeStr)
	if err != nil {
		log.Printf("audit: insert failed: %v", err)
	}
}
