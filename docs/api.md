# REST API reference

This document describes every endpoint tasks127 exposes, with request and response shapes, required authorization tier, and a curl example of each. It is meant to be read linearly the first time and searched by Ctrl-F afterwards.

If you are looking for the higher-level shape of the system (concepts like teams, display IDs, soft deletion, the filter language), the [README](../README.md) is the better starting point. If you want recipes for the handful of things operators do through the REST API directly rather than through the MCP server, see [operators.md](operators.md).

## Conventions

### Authentication

Every request except `GET /healthz` requires an `Authorization: Bearer <api_key>` header. Keys come in two tiers.

Admin keys can do anything. They are the right shape for the server operator and for service accounts (like an AI agent's backend) that need broad capabilities. User keys are bound to a specific user and inherit that user's visibility through team memberships. They can see and modify only the teams, projects, and tickets that user has access to.

Admin keys can optionally add an `X-On-Behalf-Of: <user_id>` header. When present, the request is evaluated as if the named user had made it. Using on-behalf-of deliberately turns off admin-only capabilities for that specific request; you get the scoping of a user without the privileges of an admin. User-tier keys that try to send this header get a 400 back.

### Content type

All request and response bodies are JSON. Set `Content-Type: application/json` on requests that have a body.

### IDs

Resource IDs are ULIDs, which are 26 character time-ordered strings that look like `01HX7MCFR2ZPAN90A87YVKVK91`. You never need to generate them yourself; the server returns them from create operations.

Tickets additionally have a display ID of the form `KEY-N` where `KEY` is the three letter key of the owning project or team and `N` is a per-key monotonic integer. Most ticket endpoints accept either form interchangeably in the path, so `/v1/tickets/FOO-14` and `/v1/tickets/01HX7MCFR2ZPAN90A87YVKVK91` work for the same ticket.

### Errors

Non-2xx responses use a consistent envelope.

```json
{
  "error": {
    "code": "not_found",
    "message": "ticket not found"
  }
}
```

Common error codes include `missing_token`, `invalid_token`, `missing_field`, `invalid_field`, `invalid_filter`, `invalid_reference`, `not_found`, `conflict`, `key_conflict`, `forbidden`, `immutable_field`, `impersonation_denied`, and `internal`. HTTP status codes follow the usual conventions: 400 for bad input, 401 for missing or invalid credentials, 403 for forbidden actions (like using on-behalf-of with a user-tier key), 404 for missing or invisible resources, 409 for key conflicts, 422 for violations of the two-level sub-ticket rule, and 500 for server errors.

One important policy call: when a user-tier caller asks about a resource that exists but is not in any of their teams, the server returns 404 rather than 403. This avoids leaking existence information across team boundaries.

### Soft deletion

Every resource has a `deleted_at` timestamp. DELETE just sets that field. The row stays in the database but is hidden from default queries. To see soft-deleted rows, pass `"$include_deleted": true` in a search. To un-delete a row, call `POST /v1/<resource>/<id>/restore`.

### The filter language

Several endpoints (search and bulk operations) take a filter. A filter is a JSON object in which keys are field names and values describe comparisons. A bare value is equality. An operator object expresses something else.

```json
{"status": "open"}
{"status": {"in": ["open", "in_progress"]}}
{"created_at": {"gte": "2026-04-01T00:00:00Z"}}
{"title": {"contains": "bug"}}
{"parent_id": {"is_null": true}}
```

The supported operators are `eq`, `ne`, `gt`, `gte`, `lt`, `lte`, `in`, `nin`, `contains` (case insensitive substring), and `is_null` (takes a boolean). Multiple top-level keys are implicitly joined with AND. Use `$or` or `$and` arrays for explicit grouping.

```json
{
  "$or": [
    {"status": "open"},
    {"$and": [
      {"status": "in_progress"},
      {"assignee_user_id": "01HX..."}
    ]}
  ]
}
```

Filters are validated against a per-resource allowlist of fields. Attempting to filter on a field that is not allowed returns 400 with `invalid_filter`.

Search requests accept a common envelope that wraps the filter.

```json
{
  "where": { ... },
  "order_by": [{"field": "created_at", "dir": "desc"}],
  "limit": 50,
  "offset": 0,
  "$include_deleted": false
}
```

`limit` defaults to 50 and is capped at 200. `offset` defaults to 0. `order_by` defaults to `created_at DESC, id DESC`. Search responses are paginated:

```json
{
  "data": [ ... ],
  "total": 127,
  "limit": 50,
  "offset": 0
}
```

Bulk update and bulk delete use the same filter grammar. See [Tickets](#tickets) for the canonical example.

### Examples

Throughout this document the examples assume two shell variables:

```bash
A='http://127.0.0.1:8080'
KEY='t127_...'
```

## Health

### GET /healthz

Returns `{"status": "ok"}` without requiring authentication. Use it for Docker healthchecks or uptime monitors.

```bash
curl $A/healthz
# {"status":"ok"}
```

## Identity

### GET /v1/whoami

Returns the effective identity for the current request. Useful for verifying that your API key works and for checking how the server sees an on-behalf-of header.

```bash
curl -H "Authorization: Bearer $KEY" $A/v1/whoami
```

Response:

```json
{
  "api_key_id": "01HX...",
  "tier": "admin",
  "user_id": null,
  "on_behalf_of": null
}
```

With `X-On-Behalf-Of`:

```bash
curl -H "Authorization: Bearer $KEY" -H "X-On-Behalf-Of: 01HX..." $A/v1/whoami
```

```json
{
  "api_key_id": "01HX...",
  "tier": "admin",
  "user_id": null,
  "on_behalf_of": "01HX..."
}
```

## Users

A user represents a real person. Emails are unique across non-deleted users. User management (create, delete, restore, bulk) is admin-only; read and update are available to the user themselves.

### POST /v1/users

Create a user. Admin only.

Request:

```json
{
  "name": "Alice",
  "email": "alice@example.com"
}
```

```bash
curl -H "Authorization: Bearer $KEY" -H "Content-Type: application/json" \
  -d '{"name":"Alice","email":"alice@example.com"}' \
  $A/v1/users
```

Response (201):

```json
{
  "id": "01HX...",
  "name": "Alice",
  "email": "alice@example.com",
  "created_at": "2026-04-16T14:00:00Z",
  "updated_at": "2026-04-16T14:00:00Z",
  "deleted_at": null
}
```

409 is returned if the email is already taken by a non-deleted user.

### GET /v1/users/{id}

Read one user. Visible to admin (any user) or to the user themselves. Other user-tier callers must share at least one team with the target user.

```bash
curl -H "Authorization: Bearer $KEY" $A/v1/users/01HX...
```

### PATCH /v1/users/{id}

Update a user. A user can update themselves; admin can update anyone. All fields are optional; only the ones you supply are changed.

Request:

```json
{
  "name": "Alice Smith",
  "email": "alice.smith@example.com"
}
```

```bash
curl -X PATCH -H "Authorization: Bearer $KEY" -H "Content-Type: application/json" \
  -d '{"name":"Alice Smith"}' \
  $A/v1/users/01HX...
```

Returns the updated user row in the same shape as the create response.

### DELETE /v1/users/{id}

Soft-delete a user. Admin only.

```bash
curl -X DELETE -H "Authorization: Bearer $KEY" $A/v1/users/01HX...
```

Returns the now-deleted user row (the `deleted_at` field will be populated).

### POST /v1/users/{id}/restore

Restore a soft-deleted user. Admin only. Returns 409 if the user's email now collides with someone else's active user.

```bash
curl -X POST -H "Authorization: Bearer $KEY" $A/v1/users/01HX.../restore
```

### POST /v1/users/search

Search users with the filter DSL. Visible to callers via the same visibility rule as individual reads (self or shared-team users).

Filterable fields: `id`, `name`, `email`, `created_at`, `updated_at`.

```bash
curl -H "Authorization: Bearer $KEY" -H "Content-Type: application/json" \
  -d '{"where":{"name":{"contains":"alice"}},"limit":10}' \
  $A/v1/users/search
```

### PATCH /v1/users

Bulk update. Admin only. Requires a non-empty `where` filter.

Request:

```json
{
  "where": {"email": {"contains": "@oldcompany.com"}},
  "set": {"name": "Placeholder"}
}
```

```bash
curl -X PATCH -H "Authorization: Bearer $KEY" -H "Content-Type: application/json" \
  -d '{"where":{"email":{"contains":"@oldcompany.com"}},"set":{"name":"Placeholder"}}' \
  $A/v1/users
```

Settable fields: `name`, `email`. Response:

```json
{"affected": 3, "ids": ["01HX...", "01HX...", "01HX..."]}
```

### DELETE /v1/users

Bulk soft-delete. Admin only. Requires a non-empty `where`.

```bash
curl -X DELETE -H "Authorization: Bearer $KEY" -H "Content-Type: application/json" \
  -d '{"where":{"email":{"contains":"@departed.com"}}}' \
  $A/v1/users
```

Response is the same `{"affected": N, "ids": [...]}` shape as bulk update.

## Teams

A team is a visibility group. Users in the same team can see each other's tickets. Teams have a three letter key (unique across teams and projects) that becomes the display ID prefix for tickets with no project. Team management (create, update, delete, restore, bulk) is admin only; read and search are scoped to the caller's team memberships.

### POST /v1/teams

Create a team. Admin only.

Request:

```json
{
  "key": "ENG",
  "name": "Engineering"
}
```

```bash
curl -H "Authorization: Bearer $KEY" -H "Content-Type: application/json" \
  -d '{"key":"ENG","name":"Engineering"}' \
  $A/v1/teams
```

Key must be exactly three uppercase A-Z letters. 409 is returned if the key is already in use by another team or project.

Response (201):

```json
{
  "id": "01HX...",
  "key": "ENG",
  "name": "Engineering",
  "created_at": "...",
  "updated_at": "...",
  "deleted_at": null
}
```

### GET /v1/teams/{id}

Read one team.

```bash
curl -H "Authorization: Bearer $KEY" $A/v1/teams/01HX...
```

### PATCH /v1/teams/{id}

Update a team. Admin only. Only `name` is settable; the `key` is immutable (changing it would break all outstanding ticket display IDs).

```bash
curl -X PATCH -H "Authorization: Bearer $KEY" -H "Content-Type: application/json" \
  -d '{"name":"Engineering (renamed)"}' \
  $A/v1/teams/01HX...
```

### DELETE /v1/teams/{id}

Soft-delete a team. Admin only.

```bash
curl -X DELETE -H "Authorization: Bearer $KEY" $A/v1/teams/01HX...
```

### POST /v1/teams/{id}/restore

Restore. Admin only.

```bash
curl -X POST -H "Authorization: Bearer $KEY" $A/v1/teams/01HX.../restore
```

### POST /v1/teams/search

Search. Scoped to teams the caller belongs to.

Filterable fields: `id`, `key`, `name`, `created_at`, `updated_at`.

```bash
curl -H "Authorization: Bearer $KEY" -H "Content-Type: application/json" \
  -d '{"where":{"key":"ENG"}}' \
  $A/v1/teams/search
```

Returns the standard search envelope `{"data": [...], "total": N, "limit": 50, "offset": 0}`.

### PATCH /v1/teams

Bulk update. Admin only. Settable: `name`. Requires a non-empty `where`.

```bash
curl -X PATCH -H "Authorization: Bearer $KEY" -H "Content-Type: application/json" \
  -d '{"where":{"name":{"contains":"old"}},"set":{"name":"Renamed"}}' \
  $A/v1/teams
```

### DELETE /v1/teams

Bulk soft-delete. Admin only. Requires a non-empty `where`.

```bash
curl -X DELETE -H "Authorization: Bearer $KEY" -H "Content-Type: application/json" \
  -d '{"where":{"name":{"contains":"deprecated"}}}' \
  $A/v1/teams
```

## Projects

A project is a subdivision within a team. Tickets in a project use the project's key for their display ID.

### POST /v1/projects

Create a project. Caller must have access to the target team.

Request:

```json
{
  "key": "BAK",
  "team_id": "01HX...",
  "name": "Backend"
}
```

```bash
curl -H "Authorization: Bearer $KEY" -H "Content-Type: application/json" \
  -d '{"key":"BAK","team_id":"01HX...","name":"Backend"}' \
  $A/v1/projects
```

Same key rules as teams (three uppercase letters, globally unique). Response shape mirrors the team response with the addition of `team_id`.

### GET /v1/projects/{id}

Read. Scoped by team visibility.

```bash
curl -H "Authorization: Bearer $KEY" $A/v1/projects/01HX...
```

### PATCH /v1/projects/{id}

Update. Only `name` is settable.

```bash
curl -X PATCH -H "Authorization: Bearer $KEY" -H "Content-Type: application/json" \
  -d '{"name":"Backend Services"}' \
  $A/v1/projects/01HX...
```

### DELETE /v1/projects/{id}

Soft-delete. Caller must have access to the team.

```bash
curl -X DELETE -H "Authorization: Bearer $KEY" $A/v1/projects/01HX...
```

### POST /v1/projects/{id}/restore

```bash
curl -X POST -H "Authorization: Bearer $KEY" $A/v1/projects/01HX.../restore
```

### POST /v1/projects/search

Search. Scoped by team visibility. Filterable: `id`, `key`, `team_id`, `name`, `created_at`, `updated_at`.

```bash
curl -H "Authorization: Bearer $KEY" -H "Content-Type: application/json" \
  -d '{"where":{"team_id":"01HX..."}}' \
  $A/v1/projects/search
```

### PATCH /v1/projects

Bulk update. Settable: `name`. Team visibility scope applied automatically.

```bash
curl -X PATCH -H "Authorization: Bearer $KEY" -H "Content-Type: application/json" \
  -d '{"where":{"team_id":"01HX..."},"set":{"name":"Archived"}}' \
  $A/v1/projects
```

### DELETE /v1/projects

Bulk soft-delete. Team visibility scope applied automatically.

```bash
curl -X DELETE -H "Authorization: Bearer $KEY" -H "Content-Type: application/json" \
  -d '{"where":{"name":{"contains":"deprecated"}}}' \
  $A/v1/projects
```

## Team members

Team memberships are how visibility is enforced. Membership management (create, delete, restore, bulk) is admin only; read and search are scoped to team visibility.

### POST /v1/team-members

Add a user to a team. Admin only.

Request:

```json
{
  "team_id": "01HX...",
  "user_id": "01HX..."
}
```

```bash
curl -H "Authorization: Bearer $KEY" -H "Content-Type: application/json" \
  -d '{"team_id":"01HX...","user_id":"01HX..."}' \
  $A/v1/team-members
```

Response (201):

```json
{
  "id": "01HX...",
  "team_id": "01HX...",
  "user_id": "01HX...",
  "created_at": "...",
  "deleted_at": null
}
```

409 is returned if the user is already an active member of that team.

### GET /v1/team-members/{id}

Read one membership row.

```bash
curl -H "Authorization: Bearer $KEY" $A/v1/team-members/01HX...
```

### DELETE /v1/team-members/{id}

Soft-delete the membership (remove user from team). Admin only.

```bash
curl -X DELETE -H "Authorization: Bearer $KEY" $A/v1/team-members/01HX...
```

### POST /v1/team-members/{id}/restore

Admin only.

```bash
curl -X POST -H "Authorization: Bearer $KEY" $A/v1/team-members/01HX.../restore
```

### POST /v1/team-members/search

Filterable fields: `id`, `team_id`, `user_id`, `created_at`.

```bash
curl -H "Authorization: Bearer $KEY" -H "Content-Type: application/json" \
  -d '{"where":{"team_id":"01HX..."}}' \
  $A/v1/team-members/search
```

A common pattern is listing everyone in a team, which is exactly what the example above does.

### DELETE /v1/team-members

Bulk remove. Admin only. There is no bulk PATCH because memberships have no mutable fields.

```bash
curl -X DELETE -H "Authorization: Bearer $KEY" -H "Content-Type: application/json" \
  -d '{"where":{"user_id":"01HX..."}}' \
  $A/v1/team-members
```

The example above removes one user from every team they belong to, which is useful for offboarding.

## Tickets

The core entity. Every ticket belongs to exactly one team and optionally to a project within that team. Tickets have a two level nesting limit: a ticket can have a `parent_id`, but that parent itself cannot be a sub-ticket.

Tickets get a display ID like `FOO-14` at creation time. The ID is sticky: it does not change even if the ticket is later moved between projects.

### POST /v1/tickets

Create a ticket. Caller must have access to the team.

Request:

```json
{
  "team_id": "01HX...",
  "title": "Set up CI pipeline",
  "description": "Wire up GitHub Actions for the main repo",
  "project_id": "01HX...",
  "parent_id": null,
  "status": "open",
  "assignee_user_id": null
}
```

Only `team_id` and `title` are required. `status` defaults to `open` and must be one of: `open`, `in_progress`, `blocked`, `done`, `canceled`. `parent_id` enforces the two-level rule at create time. `assignee_user_id`, if set, must be an active member of the ticket's team.

Response (201):

```json
{
  "id": "01HX...",
  "display_id": "BAK-1",
  "key": "BAK",
  "number": 1,
  "team_id": "01HX...",
  "project_id": "01HX...",
  "parent_id": null,
  "title": "Set up CI pipeline",
  "description": "Wire up GitHub Actions for the main repo",
  "status": "open",
  "assignee_user_id": null,
  "created_at": "...",
  "updated_at": "...",
  "deleted_at": null
}
```

### GET /v1/tickets/{id}

Read one ticket. Accepts either a ULID or a display ID in the path.

```bash
curl -H "Authorization: Bearer $KEY" $A/v1/tickets/BAK-1
curl -H "Authorization: Bearer $KEY" $A/v1/tickets/01HX...
```

### PATCH /v1/tickets/{id}

Update one ticket. Accepts ULID or display ID in path.

Request:

```json
{
  "status": "in_progress",
  "assignee_user_id": "01HX..."
}
```

```bash
curl -X PATCH -H "Authorization: Bearer $KEY" -H "Content-Type: application/json" \
  -d '{"status":"in_progress","assignee_user_id":"01HX..."}' \
  $A/v1/tickets/BAK-1
```

All fields optional. Settable fields: `title`, `description`, `status`, `project_id`, `parent_id`, `assignee_user_id`. The `team_id` is immutable after creation. Setting `parent_id` revalidates the two-level rule. Setting nullable fields to `null` explicitly clears them (for example, `{"assignee_user_id": null}` unassigns).

### DELETE /v1/tickets/{id}

Soft-delete one ticket. Accepts display ID or ULID in the path.

```bash
curl -X DELETE -H "Authorization: Bearer $KEY" $A/v1/tickets/BAK-1
```

### POST /v1/tickets/{id}/restore

Un-delete a soft-deleted ticket.

```bash
curl -X POST -H "Authorization: Bearer $KEY" $A/v1/tickets/BAK-1/restore
```

### POST /v1/tickets/search

Search. Scoped by team visibility. Filterable fields: `id`, `key`, `number`, `team_id`, `project_id`, `parent_id`, `title`, `description`, `status`, `assignee_user_id`, `created_at`, `updated_at`.

Example: find all open tickets assigned to a user.

```json
{
  "where": {
    "status": {"in": ["open", "in_progress"]},
    "assignee_user_id": "01HX..."
  },
  "order_by": [{"field": "created_at", "dir": "desc"}],
  "limit": 50
}
```

```bash
curl -H "Authorization: Bearer $KEY" -H "Content-Type: application/json" \
  -d '{"where":{"status":{"in":["open","in_progress"]},"assignee_user_id":"01HX..."},"order_by":[{"field":"created_at","dir":"desc"}],"limit":50}' \
  $A/v1/tickets/search
```

### PATCH /v1/tickets

Bulk update. Requires non-empty `where`. Team scope is applied automatically.

```json
{
  "where": {"project_id": "01HX...", "status": "open"},
  "set": {"status": "done"}
}
```

```bash
curl -X PATCH -H "Authorization: Bearer $KEY" -H "Content-Type: application/json" \
  -d '{"where":{"project_id":"01HX...","status":"open"},"set":{"status":"done"}}' \
  $A/v1/tickets
```

Response:

```json
{"affected": 17, "ids": ["01HX...", "..."]}
```

Settable in bulk: `title`, `description`, `status`, `project_id`, `assignee_user_id`. Not settable in bulk: `parent_id` (the two-level rule requires per-ticket validation), `team_id` (immutable).

### DELETE /v1/tickets

Bulk soft-delete. Requires non-empty `where`.

```bash
curl -X DELETE -H "Authorization: Bearer $KEY" -H "Content-Type: application/json" \
  -d '{"where":{"status":"canceled"}}' \
  $A/v1/tickets
```

Response is the same `{"affected": N, "ids": [...]}` shape as bulk update.

## Comments

A comment is attached to a ticket. Visibility inherits from the ticket's team. The author can edit or delete their own comments; an unrestricted admin can edit or delete anyone's.

### POST /v1/comments

Create a comment.

Request:

```json
{
  "ticket_id": "01HX...",
  "author_user_id": "01HX...",
  "body": "Looks good to me."
}
```

```bash
curl -H "Authorization: Bearer $KEY" -H "Content-Type: application/json" \
  -d '{"ticket_id":"01HX...","author_user_id":"01HX...","body":"Looks good to me."}' \
  $A/v1/comments
```

Response (201):

```json
{
  "id": "01HX...",
  "ticket_id": "01HX...",
  "team_id": "01HX...",
  "author_user_id": "01HX...",
  "body": "Looks good to me.",
  "created_at": "...",
  "updated_at": "...",
  "deleted_at": null
}
```

`author_user_id` is required only for unrestricted admin callers. When calling as a user (directly or via on-behalf-of), the author is the effective user automatically and supplying a different `author_user_id` returns 403.

### GET /v1/comments/{id}

Read. Visibility inherits from the ticket's team.

```bash
curl -H "Authorization: Bearer $KEY" $A/v1/comments/01HX...
```

### PATCH /v1/comments/{id}

Update. Author or admin only. Only `body` is settable.

```bash
curl -X PATCH -H "Authorization: Bearer $KEY" -H "Content-Type: application/json" \
  -d '{"body":"Edited: looks good to me."}' \
  $A/v1/comments/01HX...
```

### DELETE /v1/comments/{id}

Soft-delete. Author or admin only.

```bash
curl -X DELETE -H "Authorization: Bearer $KEY" $A/v1/comments/01HX...
```

### POST /v1/comments/{id}/restore

Un-delete. Author or admin only.

```bash
curl -X POST -H "Authorization: Bearer $KEY" $A/v1/comments/01HX.../restore
```

### POST /v1/comments/search

Filterable fields: `id`, `ticket_id`, `team_id`, `author_user_id`, `body`, `created_at`, `updated_at`.

Typical use: list comments on one ticket.

```bash
curl -H "Authorization: Bearer $KEY" -H "Content-Type: application/json" \
  -d '{"where":{"ticket_id":"01HX..."},"order_by":[{"field":"created_at","dir":"asc"}]}' \
  $A/v1/comments/search
```

### PATCH /v1/comments

Bulk update. Settable: `body`. Team scope applied automatically.

```bash
curl -X PATCH -H "Authorization: Bearer $KEY" -H "Content-Type: application/json" \
  -d '{"where":{"author_user_id":"01HX..."},"set":{"body":"[redacted]"}}' \
  $A/v1/comments
```

### DELETE /v1/comments

Bulk soft-delete. Team scope applied automatically.

```bash
curl -X DELETE -H "Authorization: Bearer $KEY" -H "Content-Type: application/json" \
  -d '{"where":{"ticket_id":"01HX..."}}' \
  $A/v1/comments
```

## API keys

API keys authenticate requests. Management is strictly admin-only; the MCP server does not expose these operations at all.

### POST /v1/api-keys

Create a key. Admin only. The plaintext key is returned exactly once.

Request:

```json
{
  "tier": "user",
  "user_id": "01HX...",
  "name": "Alice's laptop",
  "expires_at": null
}
```

```bash
curl -H "Authorization: Bearer $KEY" -H "Content-Type: application/json" \
  -d '{"tier":"user","user_id":"01HX...","name":"Alice'\''s laptop"}' \
  $A/v1/api-keys
```

`tier` is `admin` or `user`. If `user`, `user_id` is required. If `admin`, `user_id` must be null.

Response (201):

```json
{
  "id": "01HX...",
  "key": "t127_AbC123...",
  "key_prefix": "t127_AbC1",
  "tier": "user",
  "user_id": "01HX...",
  "name": "Alice's laptop",
  "expires_at": null,
  "last_used_at": null,
  "created_at": "...",
  "deleted_at": null
}
```

Save the `key` field immediately. Subsequent reads only return `key_prefix`.

### GET /v1/api-keys/{id}

Read. Never returns the plaintext key. Admin only.

```bash
curl -H "Authorization: Bearer $KEY" $A/v1/api-keys/01HX...
```

### PATCH /v1/api-keys/{id}

Update. Settable: `name`, `expires_at`. `tier`, `user_id`, and the hash are immutable.

```bash
curl -X PATCH -H "Authorization: Bearer $KEY" -H "Content-Type: application/json" \
  -d '{"name":"Alice'\''s laptop (new)","expires_at":"2026-12-31T23:59:59Z"}' \
  $A/v1/api-keys/01HX...
```

### DELETE /v1/api-keys/{id}

Revoke the key. The key stops authenticating immediately.

```bash
curl -X DELETE -H "Authorization: Bearer $KEY" $A/v1/api-keys/01HX...
```

### POST /v1/api-keys/{id}/restore

Un-revoke.

```bash
curl -X POST -H "Authorization: Bearer $KEY" $A/v1/api-keys/01HX.../restore
```

### POST /v1/api-keys/search

Search. Admin only. Filterable fields: `id`, `key_prefix`, `tier`, `user_id`, `name`, `expires_at`, `last_used_at`, `created_at`.

List all currently-active admin keys:

```bash
curl -H "Authorization: Bearer $KEY" -H "Content-Type: application/json" \
  -d '{"where":{"tier":"admin"}}' \
  $A/v1/api-keys/search
```

Find keys belonging to a specific user:

```bash
curl -H "Authorization: Bearer $KEY" -H "Content-Type: application/json" \
  -d '{"where":{"user_id":"01HX..."}}' \
  $A/v1/api-keys/search
```

Include revoked keys in the results:

```bash
curl -H "Authorization: Bearer $KEY" -H "Content-Type: application/json" \
  -d '{"$include_deleted":true,"where":{"tier":"user"}}' \
  $A/v1/api-keys/search
```

### PATCH /v1/api-keys

Bulk update. Admin only. Settable fields: `name`, `expires_at`. Requires a non-empty `where`.

Expire every key named "temp" at the end of the year:

```bash
curl -X PATCH -H "Authorization: Bearer $KEY" -H "Content-Type: application/json" \
  -d '{"where":{"name":"temp"},"set":{"expires_at":"2026-12-31T23:59:59Z"}}' \
  $A/v1/api-keys
```

Response:

```json
{"affected": 3, "ids": ["01HX...", "01HX...", "01HX..."]}
```

### DELETE /v1/api-keys

Bulk revoke. Admin only. Requires a non-empty `where`.

Revoke every key for a departed user:

```bash
curl -X DELETE -H "Authorization: Bearer $KEY" -H "Content-Type: application/json" \
  -d '{"where":{"user_id":"01HX..."}}' \
  $A/v1/api-keys
```

Revoke every key that has never been used:

```bash
curl -X DELETE -H "Authorization: Bearer $KEY" -H "Content-Type: application/json" \
  -d '{"where":{"last_used_at":{"is_null":true}}}' \
  $A/v1/api-keys
```

Response is the same `{"affected": N, "ids": [...]}` shape as bulk update.

## Subscriptions

A subscription registers interest in events matching a filter. Events accumulate in a per-subscription inbox and are optionally pushed to a webhook URL.

### POST /v1/subscriptions

Create a subscription. Available to any authenticated caller. The subscription inherits the caller's visibility (events outside their scope never fire).

Request:

```json
{
  "name": "watch FOO-14",
  "resource": "tickets",
  "event_types": ["update"],
  "where": {"id": "01HX..."},
  "max_fires": 1,
  "expires_at": "2026-05-01T00:00:00Z",
  "webhook_url": "http://127.0.0.1:8090/events"
}
```

```bash
curl -H "Authorization: Bearer $KEY" -H "Content-Type: application/json" \
  -d '{"resource":"tickets","event_types":["update"],"where":{"id":"01HX..."},"max_fires":1}' \
  $A/v1/subscriptions
```

`resource` is one of `tickets`, `comments`, `projects`, `teams`, `team_members`, `users`. `event_types` is an array of any of `create`, `update`, `delete`, `restore`. `where` uses the filter DSL, validated against the resource's filterable fields. `max_fires` auto-cancels the subscription after N matches. `expires_at` is an RFC3339 cutoff.

`webhook_url` is optional. If set, its host must be loopback (`localhost`, `127.0.0.1`, `::1`, or a literal loopback IP) or a hostname listed in the `TASKS127_WEBHOOK_ALLOWED_HOSTS` environment variable. Rejected URLs return 400.

Response (201):

```json
{
  "id": "01HX...",
  "api_key_id": "01HX...",
  "name": "watch FOO-14",
  "resource": "tickets",
  "event_types": ["update"],
  "where": {"id": "01HX..."},
  "max_fires": 1,
  "fire_count": 0,
  "expires_at": "2026-05-01T00:00:00Z",
  "webhook_url": "http://127.0.0.1:8090/events",
  "webhook_secret": "whsec_...",
  "created_at": "...",
  "updated_at": "...",
  "deleted_at": null
}
```

The `webhook_secret` is shown exactly once and is the HMAC signing key the receiver uses to verify deliveries.

### GET /v1/subscriptions/{id}

Read. Caller sees their own subscriptions or, if admin-unrestricted, anyone's. Never returns the webhook secret.

```bash
curl -H "Authorization: Bearer $KEY" $A/v1/subscriptions/01HX...
```

### PATCH /v1/subscriptions/{id}

Update. Settable: `name`, `expires_at`, `max_fires`, `webhook_url`. The `resource`, `event_types`, `where`, and `webhook_secret` are all immutable. To rotate the secret, create a new subscription and cancel the old one.

```bash
curl -X PATCH -H "Authorization: Bearer $KEY" -H "Content-Type: application/json" \
  -d '{"name":"watch FOO-14 (extended)","expires_at":"2026-06-01T00:00:00Z"}' \
  $A/v1/subscriptions/01HX...
```

### DELETE /v1/subscriptions/{id}

Cancel. Does not delete accumulated events; they remain readable so agents can drain what they have.

```bash
curl -X DELETE -H "Authorization: Bearer $KEY" $A/v1/subscriptions/01HX...
```

### POST /v1/subscriptions/search

Filterable fields: `id`, `api_key_id`, `name`, `resource`, `max_fires`, `fire_count`, `expires_at`, `created_at`, `updated_at`.

List all of the caller's active subscriptions:

```bash
curl -H "Authorization: Bearer $KEY" -H "Content-Type: application/json" \
  -d '{}' \
  $A/v1/subscriptions/search
```

Find subscriptions watching a specific resource type:

```bash
curl -H "Authorization: Bearer $KEY" -H "Content-Type: application/json" \
  -d '{"where":{"resource":"tickets"}}' \
  $A/v1/subscriptions/search
```

### PATCH /v1/subscriptions

Bulk update. Scoped to the caller's own subscriptions unless admin-unrestricted. Settable fields match the single-item PATCH. Requires a non-empty `where`.

```bash
curl -X PATCH -H "Authorization: Bearer $KEY" -H "Content-Type: application/json" \
  -d '{"where":{"resource":"tickets"},"set":{"expires_at":"2026-12-31T23:59:59Z"}}' \
  $A/v1/subscriptions
```

### DELETE /v1/subscriptions

Bulk cancel. Scoped to the caller's own subscriptions unless admin-unrestricted. Requires a non-empty `where`.

```bash
curl -X DELETE -H "Authorization: Bearer $KEY" -H "Content-Type: application/json" \
  -d '{"where":{"fire_count":{"gte":100}}}' \
  $A/v1/subscriptions
```

The example above cancels any subscription that has fired 100 or more times, which is a reasonable cleanup pass after a burst of activity. Response is the same `{"affected": N, "ids": [...]}` shape as bulk update elsewhere.

### GET /v1/subscriptions/{id}/events

Read pending events from the subscription's inbox. Query parameters: `after` (integer, default 0) returns events with sequence greater than this value. `limit` (integer, default 50, max 200).

Readable after a subscription is cancelled, so agents can finish draining an inbox after a `max_fires: 1` subscription auto-completes.

```bash
curl -H "Authorization: Bearer $KEY" "$A/v1/subscriptions/01HX.../events?after=0"
```

Response:

```json
{
  "data": [
    {
      "id": "01HX...",
      "subscription_id": "01HX...",
      "sequence": 1,
      "timestamp": "2026-04-16T14:00:00Z",
      "resource": "tickets",
      "resource_id": "01HX...",
      "action": "update",
      "payload": { "... full ticket row ..." }
    }
  ],
  "count": 1
}
```

### POST /v1/subscriptions/{id}/ack

Acknowledge events up to and including `cursor`. Marks matching rows with an `acked_at` timestamp so they no longer return from `/events`.

Request:

```json
{"cursor": 42}
```

Response:

```json
{"acked": 42, "cursor": 42}
```

### GET /v1/subscriptions/{id}/deliveries

Read webhook delivery history for this subscription. Shows the most recent 50 deliveries with state, attempt count, last status code, last error, and next retry time.

Readable by admin-unrestricted callers or by the subscription owner. User-tier callers cannot see other users' delivery histories.

```bash
curl -H "Authorization: Bearer $KEY" $A/v1/subscriptions/01HX.../deliveries
```

Response:

```json
{
  "data": [
    {
      "id": "01HX...",
      "event_id": "01HX...",
      "subscription_id": "01HX...",
      "url": "http://127.0.0.1:8090/events",
      "state": "delivered",
      "attempts": 1,
      "last_status_code": 200,
      "last_error": null,
      "next_retry_at": null,
      "delivered_at": "2026-04-16T14:00:00Z",
      "created_at": "...",
      "updated_at": "..."
    }
  ],
  "count": 1
}
```

Delivery state progresses through `pending` (not yet attempted), `retrying` (attempt failed, waiting to try again), `delivered` (success), and `failed` (exhausted retries, event still readable from the inbox). The retry schedule is 30 seconds, then 2 minutes, then 10 minutes, then 1 hour, then 4 hours, for a total of up to six attempts over roughly five and a half hours.

## Webhook delivery format

When a subscription has a `webhook_url`, each matching event triggers an outbound POST. The body is a JSON object with the event metadata and the affected row's snapshot.

```json
{
  "event_id": "01HX...",
  "subscription_id": "01HX...",
  "sequence": 42,
  "resource": "tickets",
  "resource_id": "01HX...",
  "action": "update",
  "payload": { "... row data ..." }
}
```

Headers on the request:

```
Content-Type: application/json
X-Tasks127-Event-Id: 01HX...
X-Tasks127-Subscription-Id: 01HX...
X-Tasks127-Timestamp: 1744812345
X-Tasks127-Signature: sha256=<hex>
```

The signature is `HMAC-SHA256(key=webhook_secret, message=timestamp + "." + raw_body)` as a hex string prefixed with `sha256=`. Receivers should recompute this and verify with a constant-time comparison, and should reject the request if the timestamp is more than a few minutes stale.

The server retries failed deliveries with exponential backoff (30s, 2m, 10m, 1h, 4h), so a receiver may see the same event more than once after a transient failure. The `X-Tasks127-Event-Id` header is stable across retries; use it to deduplicate if retry semantics matter.

Receivers should return a 2xx status within 10 seconds to acknowledge successful delivery. Any other status, or a timeout, schedules a retry.
