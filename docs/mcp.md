# MCP reference

This document describes every MCP tool tasks127 exposes, with argument schemas, return shapes, and an example of each. It is meant to be read in order the first time and searched afterwards.

If you are new to the system, read the README's section on the MCP server first for the big picture of what it is for and how to configure a client to use it. If you want to understand what the tools translate into under the hood, [api.md](api.md) is the companion REST reference.

## Conventions

### Transports

The `tasks127 mcp` subcommand speaks two transports. By default it reads and writes JSON-RPC on stdin and stdout. That is what most MCP clients spawn. Passing `--http 127.0.0.1:8090` starts a Streamable HTTP server at `http://127.0.0.1:8090/mcp` for clients that prefer a network-accessible MCP endpoint, or when several clients want to share one long-running MCP process. Both transports expose the same tools; only the wire format differs.

The MCP server binds to localhost by default in HTTP mode. If you set a non-loopback `--http` address, you are on your own for TLS and authentication at the transport layer. For the common case (agent and tasks127 on the same machine), stdio or loopback HTTP is what you want.

### Environment

Two variables configure the MCP server's REST client:

```
TASKS127_URL      default http://127.0.0.1:8080
TASKS127_API_KEY  required, no default
```

The MCP server uses this key when making REST calls on behalf of agent tool calls. In the typical deployment the key is admin-tier, and the agent uses per-call `on_behalf_of` arguments to scope individual operations to specific users.

### Acting on behalf of a user

Every tool where visibility or authorship matters accepts an optional `on_behalf_of` argument. When set, the MCP server adds an `X-On-Behalf-Of` header to the REST call, which scopes the request as if the named user had made it. This lets one MCP process with one admin key serve many users. If the configured key is not admin-tier, using `on_behalf_of` returns a 400.

Passing `on_behalf_of` also restricts what admin actions the call can perform, just as it does at the REST layer. Admin-only operations (creating teams, users, API keys) are not exposed through MCP at all, so this restriction rarely surfaces in practice through MCP tools.

### Result format

Every tool returns a single content block containing pretty-printed JSON with the REST API's response as its body. The MCP client receives it as a text content part. Agents are expected to parse the JSON if they need to extract specific fields.

Errors from the REST API propagate as MCP tool errors with the API's code and message, so the agent sees something like `tasks127 API error (404 not_found): ticket not found`. This lets the agent make sensible recovery decisions.

### Examples

Throughout this document, examples show the tool's JSON arguments as they would appear in an MCP `tools/call` request's `arguments` field. Any MCP client can submit them; the shape of the arguments does not depend on transport.

## Tools

### get_config

Return deployment configuration the agent is allowed to see. Currently the only field is `default_webhook_url`, which is either the URL the operator has configured as the agent's webhook receiver, or `null` if the operator did not set one.

Arguments: none.

Returns:

```json
{
  "default_webhook_url": "http://r1n-bridge:8080/tasks127-events"
}
```

Call this before `watch` when you need to know where to send webhook deliveries in this deployment. If the field is null, fall back to asking the operator for a URL.

### whoami

Return the effective identity for the current session. Useful as a first call to verify that the MCP server can reach the REST API, that the configured key is valid, and that on-behalf-of is behaving as expected.

Arguments:

```json
{
  "on_behalf_of": "01HX..."
}
```

All fields optional. Omitting `on_behalf_of` returns the raw identity of the configured API key.

Returns:

```json
{
  "api_key_id": "01HX...",
  "tier": "admin",
  "user_id": null,
  "on_behalf_of": "01HX..."
}
```

### list_teams

Enumerate teams visible to the caller. Call this first if you need a team_id or a team key before creating other resources.

Arguments:

```json
{
  "limit": 20,
  "on_behalf_of": "01HX..."
}
```

All optional. `limit` defaults to 50 and caps at 200.

Returns a paginated search envelope:

```json
{
  "data": [
    {"id": "01HX...", "key": "ENG", "name": "Engineering", "created_at": "...", "updated_at": "...", "deleted_at": null}
  ],
  "total": 1,
  "limit": 50,
  "offset": 0
}
```

### list_projects

Enumerate projects, optionally filtered by team. The `team` argument accepts either a project's owning team_id (ULID) or a three letter team key; the tool resolves the key for you.

Arguments:

```json
{
  "team": "ENG",
  "limit": 50,
  "on_behalf_of": "01HX..."
}
```

Returns a search envelope of project rows.

### search_users

Find users by name or email substring.

Arguments:

```json
{
  "contains": "alice",
  "limit": 10,
  "on_behalf_of": "01HX..."
}
```

`contains` is case-insensitive and matches either the name or email. Returns a search envelope of user rows (without email when scoping makes it inappropriate to show).

### list_team_members

List team memberships, optionally filtered to a specific team.

Arguments:

```json
{
  "team": "ENG",
  "limit": 50,
  "on_behalf_of": "01HX..."
}
```

All optional. `team` accepts a team_id or a 3-letter key. Returns a search envelope of membership rows, each with its own id, team_id, and user_id. Use this to see who belongs to a team.

## User management

### create_user

Create a new user. Admin only (requires the configured API key to be admin-tier without on-behalf-of).

Arguments:

```json
{
  "name": "Alice",
  "email": "alice@example.com"
}
```

A common reason to call this is to provision an identity for an agent or service account that needs to author comments or be assigned tickets.

Returns the created user row.

### update_user

Update a user's name or email. A user can update themselves (pass `on_behalf_of` when calling as admin); admin-unrestricted can update anyone.

Arguments:

```json
{
  "id": "01HX...",
  "name": "Alice Smith",
  "on_behalf_of": "01HX..."
}
```

Pass only the fields you want to change. Returns the updated row.

### delete_user

Soft-delete a user. Admin only.

Arguments:

```json
{"id": "01HX..."}
```

The user's tickets and comments remain in place; only the user row is marked deleted. Reversible via `restore_user`.

### restore_user

Restore a soft-deleted user. Admin only. Takes the raw user id, since deleted users are not findable through `search_users`.

Arguments:

```json
{"id": "01HX..."}
```

Returns 409 if the user's email now collides with an active user.

## Team management

### create_team

Create a new team. Admin only. The key must be exactly 3 uppercase letters and globally unique across teams and projects.

Arguments:

```json
{
  "key": "ENG",
  "name": "Engineering"
}
```

### update_team

Rename a team. Admin only. The key itself is immutable (changing it would break ticket display IDs).

Arguments:

```json
{
  "team": "ENG",
  "name": "Engineering (org-wide)"
}
```

`team` accepts id or 3-letter key.

### delete_team

Soft-delete a team. Admin only. Tickets and projects in the team remain; only the team row is hidden. Reversible via `restore_team`.

Arguments:

```json
{"team": "ENG"}
```

### restore_team

Restore a soft-deleted team. Admin only. Takes the raw team id, since deleted teams are not findable by key.

Arguments:

```json
{"id": "01HX..."}
```

## Project management

### create_project

Create a new project within a team. Admin only. `key` must be 3 uppercase letters, globally unique. `team` accepts id or key.

Arguments:

```json
{
  "key": "BAK",
  "team": "ENG",
  "name": "Backend"
}
```

### update_project

Rename a project. Admin only.

Arguments:

```json
{
  "project": "BAK",
  "name": "Backend Services"
}
```

### delete_project

Soft-delete a project. Admin only. Tickets in the project keep their display IDs (the project's key is baked into each ticket at creation time), so deleting a project does not break existing ticket references.

Arguments:

```json
{"project": "BAK"}
```

### restore_project

Restore a soft-deleted project. Admin only. Takes the raw project id.

Arguments:

```json
{"id": "01HX..."}
```

## Team membership

### add_team_member

Add a user to a team. Admin only. `team` accepts id or 3-letter key; `user` is a user id.

Arguments:

```json
{
  "team": "ENG",
  "user": "01HX..."
}
```

This is how visibility is extended. A user-tier caller sees only the teams they belong to.

### remove_team_member

Remove a user from a team. Admin only. Takes the team (id or key) and user (user_id); looks up the membership row internally so you do not need to know its id.

Arguments:

```json
{
  "team": "ENG",
  "user": "01HX..."
}
```

### create_ticket

Create a new ticket. The `team` and `project` arguments accept either ULIDs or three letter keys, so the agent does not need a separate resolution round-trip after `list_teams` or `list_projects`.

Arguments:

```json
{
  "team": "ENG",
  "title": "Set up CI pipeline",
  "description": "Wire up GitHub Actions for the main repo",
  "project": "BAK",
  "parent": "BAK-3",
  "status": "open",
  "priority": 2,
  "assignee_user_id": "01HX...",
  "on_behalf_of": "01HX..."
}
```

`team` and `title` are required. `parent` accepts either a ULID or a display ID. `status` defaults to `open` and must be one of `open`, `in_progress`, `blocked`, `done`, `canceled`. `priority` is an optional integer 0-4 following Linear's convention (`0` = None, `1` = Urgent, `2` = High, `3` = Medium, `4` = Low) and defaults to `0` if omitted.

Returns the created ticket row, including its assigned `display_id`.

### get_ticket

Read one ticket. The `id` argument accepts either a ULID or a display ID like `FOO-14`.

Arguments:

```json
{
  "id": "BAK-1",
  "on_behalf_of": "01HX..."
}
```

Returns the ticket row.

### search_tickets

Query tickets using the filter DSL. The filter grammar is identical to the REST API's; see [api.md](api.md#the-filter-language) for the full operator list.

Arguments:

```json
{
  "where": {
    "status": {"in": ["open", "in_progress"]},
    "priority": {"lte": 2},
    "assignee_user_id": "01HX..."
  },
  "order_by": [{"field": "created_at", "dir": "desc"}],
  "limit": 50,
  "on_behalf_of": "01HX..."
}
```

Filterable fields: `id`, `key`, `number`, `team_id`, `project_id`, `parent_id`, `title`, `description`, `status`, `priority`, `assignee_user_id`, `created_at`, `updated_at`.

`priority` is an integer 0-4 using Linear's convention (0=None, 1=Urgent, 2=High, 3=Medium, 4=Low). The example above selects all open or in-progress tickets that are Urgent or High priority and assigned to a specific user. Note that a plain `order_by` on priority uses natural integer order (0 first), which is the opposite of Linear's UI convention (1 first, 0 last); exclude `priority = 0` from the filter if you want Linear's reading order.

Returns a search envelope of ticket rows.

### update_tickets

Update one ticket or many in a single call. Pass either `ticket` (for a single update by ULID or display ID) or `where` (for a bulk update by filter), never both. `changes` is a map of fields to new values and is always required.

Single-ticket form:

```json
{
  "ticket": "BAK-1",
  "changes": {"status": "in_progress", "priority": 1, "assignee_user_id": "01HX..."},
  "on_behalf_of": "01HX..."
}
```

Bulk form:

```json
{
  "where": {"project_id": "01HX...", "status": "open"},
  "changes": {"status": "done"},
  "on_behalf_of": "01HX..."
}
```

Settable fields: `title`, `description`, `status`, `priority`, `project_id`, `assignee_user_id`. `parent_id` is not settable in bulk (the two-level rule requires per-ticket validation); use the single form or the REST API for that. `team_id` is immutable once the ticket is created. `priority` must be an integer 0-4 (0=None, 1=Urgent, 2=High, 3=Medium, 4=Low).

Single form returns the updated ticket row. Bulk form returns an affected count:

```json
{"affected": 17, "ids": ["01HX...", "..."]}
```

### delete_tickets

Soft-delete one ticket or many. Same shape as `update_tickets`: either `ticket` or `where`.

Arguments (single):

```json
{
  "ticket": "BAK-1",
  "on_behalf_of": "01HX..."
}
```

Arguments (bulk):

```json
{
  "where": {"status": "canceled"},
  "on_behalf_of": "01HX..."
}
```

Returns the deleted ticket row (single form) or an affected count (bulk form). Reversible via `restore_ticket`.

### restore_ticket

Restore a soft-deleted ticket. Caller must have access to the ticket's team.

Arguments:

```json
{
  "id": "BAK-1",
  "on_behalf_of": "01HX..."
}
```

`id` accepts a ULID or a display ID.

### add_comment

Add a comment to a ticket. The `ticket` argument accepts either a ULID or a display ID.

Arguments:

```json
{
  "ticket": "BAK-1",
  "body": "Looks good to me.",
  "on_behalf_of": "01HX..."
}
```

When `on_behalf_of` is set, the comment is authored by that user automatically. When calling as unrestricted admin without on_behalf_of, `author_user_id` is required.

```json
{
  "ticket": "BAK-1",
  "body": "Looks good to me.",
  "author_user_id": "01HX..."
}
```

Returns the created comment row.

### list_comments

List comments on a specific ticket. Comments are returned in chronological order (oldest first) by default, which is the natural reading order for a discussion. Pass `order_by` to override, for example if you want only the most recent comments.

Arguments:

```json
{
  "ticket": "BAK-1",
  "limit": 50,
  "order_by": [{"field": "created_at", "dir": "desc"}],
  "on_behalf_of": "01HX..."
}
```

`order_by` is optional. When omitted the tool injects `[{"field": "created_at", "dir": "asc"}]` automatically. This differs from the REST `/v1/comments/search` default (created_at DESC), because the agent-facing reading order is usually the reverse of the REST search default.

Returns a search envelope of comment rows.

### edit_comment

Edit a comment's body. The comment's author can edit their own; admin-unrestricted can edit anyone's.

Arguments:

```json
{
  "id": "01HX...",
  "body": "Edited text",
  "on_behalf_of": "01HX..."
}
```

Returns the updated comment row.

### delete_comment

Soft-delete a comment. Author or admin-unrestricted.

Arguments:

```json
{
  "id": "01HX...",
  "on_behalf_of": "01HX..."
}
```

Reversible via `restore_comment`.

### restore_comment

Restore a soft-deleted comment. Author or admin-unrestricted.

Arguments:

```json
{
  "id": "01HX...",
  "on_behalf_of": "01HX..."
}
```

### watch

Register a subscription. Events matching the filter accumulate in the subscription's inbox and can optionally be pushed to a webhook.

Arguments:

```json
{
  "name": "watch FOO-14",
  "resource": "tickets",
  "event_types": ["update"],
  "where": {"id": "01HX..."},
  "max_fires": 1,
  "expires_at": "2026-05-01T00:00:00Z",
  "webhook_url": "http://127.0.0.1:8090/events",
  "on_behalf_of": "01HX..."
}
```

`resource` and `event_types` are required. `where` is required but can be `{}` if you really want every event. Valid resources: `tickets`, `comments`, `projects`, `teams`, `team_members`, `users`. Valid event types: `create`, `update`, `delete`, `restore`.

`max_fires` optionally auto-cancels the subscription after N matches (`1` is the right shape for single-shot watches like "tell me the first time X happens"). `expires_at` is an RFC3339 cutoff after which the subscription stops firing.

`webhook_url` is optional. If supplied, the host must be loopback or one of the hostnames listed in the server's `TASKS127_WEBHOOK_ALLOWED_HOSTS` environment variable, or the create is rejected.

Returns the subscription row, including a `webhook_secret` if `webhook_url` was set. The secret is returned exactly once; store it immediately.

### unwatch

Cancel a subscription. Any events already in the inbox remain readable via `read_events` so the agent can drain what it has.

Arguments:

```json
{
  "id": "01HX..."
}
```

Returns the now-deleted subscription row.

### read_events

Read pending events from a subscription's inbox. Events arrive in sequence order.

Arguments:

```json
{
  "subscription_id": "01HX...",
  "after": 0,
  "limit": 50
}
```

`after` is the sequence number of the last event already processed. Pass 0 on the first call; on subsequent calls pass the highest sequence from the previous batch.

Returns:

```json
{
  "data": [
    {
      "id": "01HX...",
      "subscription_id": "01HX...",
      "sequence": 1,
      "timestamp": "...",
      "resource": "tickets",
      "resource_id": "01HX...",
      "action": "update",
      "payload": { "... full row snapshot ..." }
    }
  ],
  "count": 1
}
```

This tool does not acknowledge events; they will be returned again until `ack_events` advances the cursor.

### ack_events

Acknowledge events up to and including `cursor`. Events with `sequence <= cursor` are marked delivered and will not be returned by subsequent `read_events` calls.

Arguments:

```json
{
  "subscription_id": "01HX...",
  "cursor": 42
}
```

Returns:

```json
{"acked": 42, "cursor": 42}
```

A typical agent heartbeat looks like: call `read_events` with the last-known cursor, process the batch, then call `ack_events` with the highest sequence number from the batch. The cursor survives across MCP sessions; the subscription itself owns it.

### get_subscription

Read one subscription by id. Returns full details including `webhook_url`, but never the `webhook_secret` (which is only ever shown in the `watch` response at creation time).

Arguments:

```json
{
  "id": "01HX...",
  "on_behalf_of": "01HX..."
}
```

### list_subscriptions

List subscriptions visible to the caller. Normally returns only your own; admin-unrestricted sees all. Useful for finding subscription ids and for inspecting what is currently being watched.

Arguments:

```json
{
  "where": {"resource": "tickets"},
  "limit": 50,
  "on_behalf_of": "01HX..."
}
```

All optional. Filterable fields include `id`, `api_key_id`, `name`, `resource`, `max_fires`, `fire_count`, `expires_at`, `created_at`, `updated_at`.

### list_deliveries

List recent webhook delivery attempts for a specific subscription, with status codes, error messages, and retry scheduling. This is the right tool when push notifications are not arriving and you need to debug why. Readable by the subscription's owner or by admin-unrestricted.

Arguments:

```json
{
  "id": "01HX...",
  "on_behalf_of": "01HX..."
}
```

Returns the most recent 50 deliveries, newest first. Each row shows `state` (pending, retrying, delivered, failed), `attempts`, `last_status_code`, `last_error`, and `next_retry_at`. A `state` of `retrying` with a future `next_retry_at` means the worker will try again on its own; `failed` means the retry budget was exhausted and the event can now only be picked up through `read_events`.

## Subscription lifecycle patterns

The `watch`, `unwatch`, `read_events`, and `ack_events` tools give you two meaningfully different integration shapes. The pull shape has the agent poll the inbox on a heartbeat and acknowledge batches. The push shape has tasks127 deliver events to a webhook URL the agent owns. Which one is right depends on how often events need to cause the agent to wake, and whether the agent can receive HTTP.

The pull shape is the simpler default. Create a subscription without `webhook_url`. On each heartbeat, call `read_events` with the last cursor you persisted, process the batch, then call `ack_events` with the highest sequence number from the batch. The cursor survives across MCP sessions; the subscription itself owns it. No secrets to manage, no receiver to keep alive. The only downside is latency, since events surface at heartbeat cadence rather than immediately.

The push shape trades complexity for immediacy. The webhook flow has one moving part the per-tool docs above do not spell out, so it is worth stating plainly: tasks127 returns `webhook_secret` exactly once, in the `watch` response, and never shows it again. The receiver needs that secret to verify deliveries. If the agent is the only thing holding it, a fresh agent session has no way to reconstruct it, and you are stuck cancelling and re-creating the subscription on every restart.

The idiomatic flow looks like this:

1. Agent calls `watch` with `webhook_url` pointing at the receiver.
2. Receiver exposes a small registration endpoint, something like `POST /register-subscription` with `{subscription_id, webhook_secret}` in the body, and stores the pair in whatever persistence layer is appropriate. In-process memory is fine for a throwaway receiver; a local SQLite file or a secret store is appropriate for anything that needs to survive a restart.
3. Immediately after `watch` returns, the agent hands `subscription_id` and `webhook_secret` to that endpoint in one call. This is the step that most commonly gets forgotten on first implementation. Its symptom is `401 bad signature` on every delivery because the receiver has no secret to verify against.
4. On `unwatch`, the agent calls a companion endpoint (`POST /forget-subscription` or similar) to drop the secret from the receiver. Stale secrets in the receiver are harmless but accumulate.

A few details worth knowing on the receiver side.

The secret is immutable. The `PATCH /v1/subscriptions/{id}` endpoint does not expose it, and there is no rotation endpoint. To change a secret, cancel the subscription and create a new one.

tasks127 retries failed deliveries with exponential backoff, and the inbox is the source of truth. A receiver that is briefly down does not lose events; it just gets them through `read_events` later instead of immediately via the webhook. This means you can treat the webhook path as an optimization. If the receiver is missing a secret and rejecting deliveries with 401, the agent can still pull the same events by calling `read_events` on the same subscription.

Rotating the webhook URL itself is supported. `PATCH /v1/subscriptions/{id}` accepts `webhook_url`, so you can move a receiver to a new address without recreating the subscription or the secret.

## Error handling

MCP tool errors carry the tasks127 REST API's error envelope in the message, so the agent sees structured information about what went wrong.

Common error patterns worth anticipating:

A `404 not_found` on `get_ticket` with a display ID means either the ticket does not exist or the effective principal cannot see it. The two cases are deliberately indistinguishable to avoid leaking existence across team boundaries. If the agent is confident the ticket exists, check `on_behalf_of`.

A `400 key_conflict` when creating a team or project means another resource already holds that key. The constraint is global across teams and projects, so a team key in use somewhere else (even a deleted team) blocks the creation.

A `400 invalid_filter` with `unknown field: <name>` on a search or bulk operation means the filter references a field that is not allowlisted for that resource. See the per-resource field list under each tool above.

A `422 invalid_parent` on `create_ticket` or `update_tickets` is the two-level sub-ticket rule firing. Either the proposed parent is itself a sub-ticket, or the ticket being updated already has children.

A `403 forbidden` on MCP operations typically means the configured API key is user-tier and the tool invoked an admin-only operation under the hood (for example, some subscription-read operations require admin tier when inspecting other users' subscriptions). Review the MCP server's `TASKS127_API_KEY` and decide whether it should be admin-tier.

## What is intentionally not exposed

Three categories of REST endpoints are deliberately not wrapped as MCP tools.

Administrative setup operations like creating teams, creating users, and issuing API keys are operator concerns, not agent concerns. Exposing them through MCP would invite agents to create infrastructure they should not be creating. Use the REST API directly or the recipes in [operators.md](operators.md).

Low-level lifecycle operations like restore and bulk update for non-ticket resources are rarely useful to an agent. The MCP surface is intentionally smaller than the REST surface and biased toward the ticketing and subscription workflows that are the agent's actual job.

Delivery history (`/v1/subscriptions/{id}/deliveries`) is a debugging endpoint primarily for operators, not agents. Agents that care whether a webhook is arriving already know because they are the receiver. If you need to inspect it, use the REST endpoint directly.
