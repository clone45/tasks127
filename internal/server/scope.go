package server

import (
	"context"
	"fmt"
	"net/http"

	"github.com/clone45/tasks127/internal/auth"
)

// Visibility & authorization helpers.
//
// Principle: the EFFECTIVE principal is determined by tier + X-On-Behalf-Of.
//   - admin-tier, no OBO        → unrestricted ("god mode")
//   - admin-tier, X-On-Behalf-Of → scoped to that user (admin capabilities NOT inherited)
//   - user-tier                  → scoped to that user
//
// Admin-only actions are gated by requireAdmin (rejects OBO).
// Object visibility returns 404 (not 403) per spec §8 to avoid leaking existence.

// effectiveUserID returns the user id whose permissions apply to this request.
// If unrestricted is true, the caller has full admin and no scoping is needed.
func (s *Server) effectiveUserID(ctx context.Context) (userID string, unrestricted bool) {
	p := auth.FromContext(ctx)
	if p == nil {
		return "", false
	}
	if p.Tier == "admin" && p.OnBehalfOf == nil {
		return "", true
	}
	if p.OnBehalfOf != nil {
		return *p.OnBehalfOf, false
	}
	if p.UserID != nil {
		return *p.UserID, false
	}
	return "", false
}

// requireAdmin writes a 403 and returns false if the effective principal
// is not an unscoped admin. Admin-with-OBO is NOT admin for this purpose;
// using OBO is voluntarily scoping yourself to a user's capabilities.
func (s *Server) requireAdmin(w http.ResponseWriter, r *http.Request) bool {
	if _, unrestricted := s.effectiveUserID(r.Context()); unrestricted {
		return true
	}
	writeError(w, http.StatusForbidden, "forbidden",
		"this operation requires an unrestricted admin (admin-tier API key with no X-On-Behalf-Of header). "+
			"If you are currently using X-On-Behalf-Of to act as a specific user, drop that header and retry. "+
			"If your key is user-tier, an admin needs to either issue an admin-tier key or perform this action for you.")
	return false
}

// canAccessTeam returns true if the effective principal can see/touch
// resources in the given team.
func (s *Server) canAccessTeam(ctx context.Context, teamID string) bool {
	userID, unrestricted := s.effectiveUserID(ctx)
	if unrestricted {
		return true
	}
	if userID == "" {
		return false
	}
	var exists bool
	s.db.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM team_members
		               WHERE user_id = ? AND team_id = ? AND deleted_at IS NULL)`,
		userID, teamID,
	).Scan(&exists)
	return exists
}

// canAccessUser returns true if the effective principal can see a given user:
// the user themselves, or a user who shares at least one team with them.
// Admin-unrestricted sees everyone.
func (s *Server) canAccessUser(ctx context.Context, userID string) bool {
	effective, unrestricted := s.effectiveUserID(ctx)
	if unrestricted {
		return true
	}
	if effective == "" {
		return false
	}
	if effective == userID {
		return true
	}
	var exists bool
	s.db.QueryRowContext(ctx,
		`SELECT EXISTS(
			SELECT 1 FROM team_members tm1
			JOIN team_members tm2 ON tm1.team_id = tm2.team_id
			WHERE tm1.user_id = ? AND tm2.user_id = ?
			  AND tm1.deleted_at IS NULL AND tm2.deleted_at IS NULL)`,
		effective, userID,
	).Scan(&exists)
	return exists
}

// scopeTeam returns (sqlFragment, args, unrestricted). If unrestricted is true,
// the caller is admin-unrestricted and no filtering is needed. Otherwise, the
// fragment constrains `column` to teams the effective user belongs to.
func (s *Server) scopeTeam(ctx context.Context, column string) (string, []any, bool) {
	userID, unrestricted := s.effectiveUserID(ctx)
	if unrestricted {
		return "", nil, true
	}
	frag := column + ` IN (SELECT team_id FROM team_members WHERE user_id = ? AND deleted_at IS NULL)`
	return frag, []any{userID}, false
}

// scopeUser returns a SQL fragment limiting a user-id column to users the
// effective principal can see (self + people in shared teams).
func (s *Server) scopeUser(ctx context.Context, column string) (string, []any, bool) {
	userID, unrestricted := s.effectiveUserID(ctx)
	if unrestricted {
		return "", nil, true
	}
	frag := fmt.Sprintf(`(%s = ? OR %s IN (
		SELECT DISTINCT tm2.user_id FROM team_members tm1
		JOIN team_members tm2 ON tm1.team_id = tm2.team_id
		WHERE tm1.user_id = ?
		  AND tm1.deleted_at IS NULL AND tm2.deleted_at IS NULL))`, column, column)
	return frag, []any{userID, userID}, false
}

// applyScope prepends a scope fragment onto an existing WHERE clause.
// If unrestricted, returns the inputs unchanged.
func applyScope(whereClause string, whereArgs []any, scopeFrag string, scopeArgs []any, unrestricted bool) (string, []any) {
	if unrestricted {
		return whereClause, whereArgs
	}
	return "(" + whereClause + ") AND " + scopeFrag, append(whereArgs, scopeArgs...)
}
