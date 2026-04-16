# tasks127

A lightweight, headless ticketing API built for AI agents. Think of it as a self-hosted alternative to Linear or Jira that you run on the same machine as whatever will be talking to it.

The name is a nod to 127.0.0.1. By default the server binds to localhost only, and webhooks can only point at localhost. The whole design assumes you are running tasks127 alongside the thing that is going to consume it, not exposing it to the open internet.

## Why this exists

AI agents that manage tickets, for example an agent that fields requests over Telegram and records them as work items, run into problems that traditional ticketing APIs were never designed for.

Two problems in particular come up again and again.

The first is iteration cost. If an agent wants to close every ticket in a project, a traditional API forces it to fetch those tickets one at a time and then issue N separate updates. Every one of those calls costs tokens and round trip time. tasks127 lets the agent do this in a single call by accepting a filter in place of a list of IDs.

The second is polling. If a user says "tell me when FOO-23 gets a comment", the naive approach is for the agent to poll that ticket every few minutes. That burns through inference budget fast. tasks127 solves this with a subscription system: the agent registers a filter once, and the server either pushes events to a webhook or accumulates them in an inbox that the agent can drain cheaply on its next heartbeat.

Most of the rest of the design supports those two ideas. Teams, projects, tickets, comments, users, and API keys exist so that filters and subscriptions have something meaningful to operate on.

## What it is not

tasks127 does not have a web UI and there are no plans to add one. If you want humans clicking around kanban boards, use Linear. If you want an API that your AI agent can talk to cheaply and efficiently, and that humans interact with indirectly through that agent, tasks127 is aimed at you.

## Quick start

Grab a release binary or build from source (see below), then just run it.

```
./tasks127
```

On first boot the server creates a SQLite database in the current directory and prints a single admin API key to stdout. Save that key immediately, because it will not be shown a second time.

```
=== ADMIN API KEY (shown once, save it now) ===
t127_AbC123...
================================================
tasks127 listening on 127.0.0.1:8080
```

If you lose the key while you are still in development, the easiest recovery is to stop the server, delete the SQLite file, and let it regenerate on the next start. Obviously that is not a recovery path once you have real data, so capture the key now while it is cheap.

From this point on you can talk to the API with any HTTP client. Every request needs an `Authorization: Bearer <key>` header.

A good first call is `GET /v1/whoami`. It confirms your key is valid and tells you exactly what the server thinks you are. The response includes your tier, your user ID if applicable, and the on-behalf-of value if you sent that header. This saves a lot of debugging time later when something is not behaving the way you expect, because you can always check whether the issue is "the server disagrees about who I am" before digging deeper.

```bash
KEY='t127_AbC123...'

curl -H "Authorization: Bearer $KEY" http://127.0.0.1:8080/v1/whoami
```

Now create a team. The three letter key is required and becomes the prefix for ticket display IDs like ENG-1.

```bash
curl -H "Authorization: Bearer $KEY" -H "Content-Type: application/json" \
  -d '{"key":"ENG","name":"Engineering"}' \
  http://127.0.0.1:8080/v1/teams
```

Create a ticket in that team. The response will include a `display_id` like `ENG-1` that you can use anywhere an ID is expected.

```bash
curl -H "Authorization: Bearer $KEY" -H "Content-Type: application/json" \
  -d '{"team_id":"<team id from above>","title":"Set up CI"}' \
  http://127.0.0.1:8080/v1/tickets
```

Fetch the ticket back by its display ID:

```bash
curl -H "Authorization: Bearer $KEY" http://127.0.0.1:8080/v1/tickets/ENG-1
```

That is the whole shape of the system. Everything else follows the same pattern.

## Configuration

tasks127 reads its configuration from environment variables, all of which have reasonable defaults.

```
# REST server
TASKS127_BIND                       default 127.0.0.1:8080
TASKS127_DATABASE_URL               default sqlite://./tasks127.db
TASKS127_LOG_LEVEL                  default info
TASKS127_MIGRATE_ON_START           default true
TASKS127_WEBHOOK_ALLOWED_HOSTS      default empty (loopback only; see webhooks section)

# MCP server (only read by the `tasks127 mcp` subcommand)
TASKS127_URL                        default http://127.0.0.1:8080
TASKS127_API_KEY                    required, no default
```

If you want the server reachable from outside localhost, set `TASKS127_BIND` to an external address. You will almost certainly want a TLS terminator like Caddy or nginx in front of it, since tasks127 does not do TLS on its own.

There is also a `GET /healthz` endpoint that requires no authentication and returns `{"status":"ok"}`. It is the right thing to point a Docker healthcheck or uptime monitor at.

## Key concepts

The rest of this section walks through the ideas that make the API feel the way it does. You do not need to read it cover to cover to get started, but it will help if something surprises you later.

### Teams, projects, and display IDs

A team is a group of users that shares visibility into a set of tickets. A project is a subdivision within a team, useful for organizing work but not required. Both get a three letter key at creation time, and that key becomes the prefix for ticket display IDs.

Keys are globally unique across teams and projects combined. That is because a display ID like `ENG-14` needs to unambiguously identify one ticket, and if two different resources shared the key `ENG` there would be no way to know which one to look in. If you try to create a second team or project with a key that is already in use, the server returns 409 Conflict.

A ticket always belongs to exactly one team, and optionally to a project within that team. When you create a ticket, it gets a display ID based on whichever key owns it. If the ticket is in a project, the project's key wins. If not, the team's key is used. Once the display ID is assigned, it does not change, even if you later move the ticket between projects.

### Two level sub-tickets

A ticket can have a parent ticket, but no deeper. This is a deliberate choice. Deep trees are hard to reason about in an API that has no visual representation, and they tend to turn ticketing systems into untrackable nested todo lists. The two level rule is enforced both when creating a sub-ticket and when modifying an existing one: you cannot attach a ticket to a parent that is itself a sub-ticket, and you cannot turn a ticket that already has children into a sub-ticket itself.

### API keys, tiers, and acting as another user

Every request has to carry an API key, presented as a bearer token. Keys come in two tiers.

Admin keys can do anything. Your agent will normally hold an admin key. User keys are scoped to a specific user, and can only see and modify things that user has access to through their team memberships.

Admin keys can also act on behalf of a specific user by adding an `X-On-Behalf-Of: <user_id>` header to a request. When that header is present, the request is evaluated as if the named user had made it, using their visibility and their capabilities rather than admin's. This lets your agent authenticate once as admin and then scope individual operations to specific users, without juggling multiple keys.

When acting on behalf of a user, admin capabilities that are not available to regular users (creating users, managing teams, issuing API keys) are deliberately turned off. The semantics are consistent: you are either acting as admin, or acting as that user. No partial mixing.

### Soft deletion

Nothing is ever permanently deleted through the API. A DELETE call just sets a `deleted_at` timestamp on the row, and default queries skip anything with that field set. You can see soft deleted rows by passing `"$include_deleted": true` in a search, and you can resurrect them by POSTing to `/v1/<resource>/<id>/restore`.

This behavior exists because mistakes happen, and because an AI agent executing bulk operations is exactly the kind of caller that benefits from an undo button.

### The filter language

Most of the interesting operations in tasks127 take a filter. The same grammar powers search endpoints, bulk updates, bulk deletes, and subscription predicates.

A filter is a JSON object. Keys are field names. Values describe the comparison. The simplest case is an equality check:

```json
{"where": {"status": "open"}}
```

Any field can also take an operator object in place of a bare value:

```json
{"where": {"created_at": {"gte": "2026-01-01T00:00:00Z"}}}
```

The supported operators are `eq`, `ne`, `gt`, `gte`, `lt`, `lte`, `in`, `nin`, `contains`, and `is_null`. Multiple conditions at the top level are implicitly joined with AND. For OR logic or explicit grouping, use `$or` or `$and`:

```json
{
  "where": {
    "$or": [
      {"status": "open"},
      {"status": "in_progress"}
    ]
  }
}
```

The payoff for having this grammar is that it works in bulk. If you want to close every open ticket in a specific project, you do not fetch them one at a time. You send one PATCH:

```bash
curl -X PATCH -H "Authorization: Bearer $KEY" -H "Content-Type: application/json" \
  -d '{"where":{"project_id":"...","status":"open"},"set":{"status":"done"}}' \
  http://127.0.0.1:8080/v1/tickets
```

The server responds with an affected count and a list of the IDs that were touched.

### Subscriptions and webhooks

A subscription tells the server that you want to be notified about events matching a filter. Events accumulate in a per-subscription inbox that you can drain by polling, and they can optionally be pushed to a webhook URL for immediate delivery.

Here is a subscription that watches for any update on a specific ticket:

```bash
curl -H "Authorization: Bearer $KEY" -H "Content-Type: application/json" \
  -d '{
    "resource": "tickets",
    "event_types": ["update"],
    "where": {"id": "..."}
  }' \
  http://127.0.0.1:8080/v1/subscriptions
```

You can also ask the server to watch one time only, by setting `max_fires: 1`. This is the right shape for a request like "tell me the first time Bret comments on FOO-12":

```json
{
  "resource": "comments",
  "event_types": ["create"],
  "where": {"ticket_id": "...", "author_user_id": "bret's id"},
  "max_fires": 1
}
```

Once the subscription fires, it auto cancels. Any events already in the inbox stay readable so you do not lose data.

To read the inbox:

```bash
curl -H "Authorization: Bearer $KEY" \
  "http://127.0.0.1:8080/v1/subscriptions/<id>/events?after=0"
```

When you have processed some events, acknowledge them to drop them from the inbox:

```bash
curl -X POST -H "Authorization: Bearer $KEY" -H "Content-Type: application/json" \
  -d '{"cursor": 42}' \
  http://127.0.0.1:8080/v1/subscriptions/<id>/ack
```

If your agent can receive HTTP, add a `webhook_url` to the subscription and the server will push events to it. The URL host must either be a loopback address (`localhost`, `127.0.0.1`, `::1`, or anything that parses as a loopback IP), or a hostname you have explicitly allowed by setting `TASKS127_WEBHOOK_ALLOWED_HOSTS` to a comma-separated list when starting the server. The check is purely lexical on the URL; no DNS is resolved. This second knob exists specifically for containerized deployments where the webhook target is a sibling service on a Docker network, for example a bridge service reachable at `http://r1n-bridge:8080/events`. You would run tasks127 with `TASKS127_WEBHOOK_ALLOWED_HOSTS=r1n-bridge` and that one hostname would be accepted while everything else stayed rejected.

The create response includes a `webhook_secret` exactly once; store it somewhere safe. Verify the HMAC SHA-256 signature on incoming deliveries, which is sent in the `X-Tasks127-Signature` header. The header value is the literal prefix `sha256=` followed by the hex-encoded digest of `HMAC-SHA256(webhook_secret, timestamp + "." + raw_body)`, where `timestamp` is the value of the accompanying `X-Tasks127-Timestamp` header. See [docs/api.md](docs/api.md#webhook-delivery-format) for the full delivery format including retry semantics.

The server retries failed deliveries with exponential backoff up to six attempts (30s, 2m, 10m, 1h, 4h, give up). If every attempt fails, the event is still sitting in the inbox waiting for your next heartbeat. The inbox is the source of truth. Webhooks are a fast-path optimization on top.

#### Receiver patterns

The `webhook_secret` returned from a subscription-creating call is shown exactly once. Your receiver needs that secret on every subsequent delivery to verify the HMAC, so the receiver has to persist it somewhere stable. Storing the secret only in agent memory is an antipattern: subscriptions outlive any single agent process, and if the agent restarts the receiver still needs the secret to validate incoming requests.

The common shape is: immediately after the agent creates a subscription, it hands the subscription ID, the URL, and the secret to the receiver over a private channel. The receiver stores the mapping from subscription ID to secret. When a delivery arrives, the receiver reads the `X-Tasks127-Subscription-Id` header, looks up the corresponding secret, and uses it to verify the signature. If you are running the receiver and the agent in the same process or the same container, "private channel" might just be an internal function call. If they are separate services, it is a small authenticated HTTP endpoint on the receiver side, or a shared secret store like a local SQLite file or a short-lived configuration secret.

### MCP server for AI agents

Running `tasks127 mcp` starts a Model Context Protocol server that wraps the REST API as tools an AI agent can call directly. This is the intended integration path for agent-based clients like OpenClaw, Claude Desktop, or Claude Code. Under the hood it is a thin translation layer that makes HTTP calls back to the running REST server, so everything the REST API enforces (auth, visibility, audit, subscription firing) applies without exception.

The MCP server needs two environment variables. `TASKS127_URL` points at the REST server and defaults to `http://127.0.0.1:8080`. `TASKS127_API_KEY` is the bearer token the MCP server will use when calling back into the REST API. The typical deployment is that the MCP server holds an admin key and individual tool calls scope themselves to specific users via an `on_behalf_of` argument (see below).

By default the server speaks MCP over stdin and stdout, which is how most MCP clients spawn it. Pass `--http 127.0.0.1:8090` to speak Streamable HTTP on a local port instead, which is useful when multiple clients want to share one long-running MCP process, or for debugging with curl.

#### Configuring an MCP client

Most MCP clients share the same configuration shape: an object keyed on a server name, with `command`, `args`, and `env` underneath. The file location varies per client (Claude Desktop, Claude Code, OpenClaw, Cursor, and Zed each have their own), but the entry you drop in is essentially the same. The example below works as-is for Claude Desktop and Claude Code; OpenClaw uses the same shape in `~/.openclaw/openclaw.json` with an optional `"type": "stdio"` you can add for clarity.

```json
{
  "mcpServers": {
    "tasks127": {
      "command": "/usr/local/bin/tasks127",
      "args": ["mcp"],
      "env": {
        "TASKS127_URL": "http://127.0.0.1:8080",
        "TASKS127_API_KEY": "t127_..."
      }
    }
  }
}
```

The `env` block above is not optional, and this is the single most common thing to trip over. When an MCP client spawns the tasks127 binary over stdio, it does not pass its own environment through to the child process. The official MCP SDKs restrict inheritance to a small set of basic variables (`HOME`, `LOGNAME`, `PATH`, `SHELL`, `TERM`, `USER` on Linux) and nothing else. `TASKS127_URL` and `TASKS127_API_KEY` have to be named explicitly inside `env`, or the child will not see them. Most MCP clients support `${VAR}` interpolation inside these values, which is the right shape when you want to pull runtime values in from a container's environment rather than hardcoding them.

If the `env` block is missing or misconfigured, the child process logs `tasks127 mcp: TASKS127_API_KEY is required` to stderr and exits. The client typically surfaces this as a generic "server failed to start" without showing the stderr message, so the failure mode is quieter than it should be.

For HTTP-based clients, run the MCP server as a separate long-lived process and point the client at it.

```bash
TASKS127_URL="http://127.0.0.1:8080" \
TASKS127_API_KEY="t127_..." \
  /usr/local/bin/tasks127 mcp --http 127.0.0.1:8090
```

Clients then connect to `http://127.0.0.1:8090/mcp` for the Streamable HTTP endpoint. The server also serves a `/healthz` on the MCP port for process supervision.

#### Acting on behalf of a user

The MCP tools accept an optional `on_behalf_of` argument on every operation where visibility or authorship matters. When set, the tool sends an `X-On-Behalf-Of` header with the REST call, which scopes the request to that user's visibility and capabilities. This is how a single MCP process with one admin API key can serve many different users, each call correctly scoped.

For example, if your agent receives a Telegram message from Alice and needs to create a ticket she should own, the tool call looks like `create_ticket` with `team: "ENG", title: "...", on_behalf_of: "<alice's user_id>"`. The ticket gets created as if Alice herself called the REST API. Only admin-tier keys can use this; user-tier keys that try will get a 400 back.

#### Tools

There are fifteen tools, small enough to keep the agent's system prompt lean while still covering the full day-to-day surface. Administrative work like creating teams, creating users, and issuing API keys is intentionally not here; operators do that directly through the REST API or using the recipes in [docs/operators.md](docs/operators.md).

```
whoami             return the effective identity (tier, user, on-behalf-of)
list_teams         list teams visible to the caller
list_projects      list projects, optionally filtered by team
search_users       find users by name or email substring

create_ticket      create a ticket; team and project accept ULIDs or 3-letter keys
get_ticket         read one ticket by ULID or display ID (FOO-14)
search_tickets     filter tickets using the Mongo-style DSL
update_tickets     patch one ticket (by id) or many (by filter) in one call
delete_tickets     soft-delete one ticket (by id) or many (by filter)

add_comment        add a comment to a ticket
list_comments      read comments on a ticket

watch              register a subscription (filter + event types)
unwatch            cancel a subscription
read_events        drain pending events from a subscription's inbox
ack_events         advance the cursor after processing a batch
```

The tool count is deliberately bounded. Current guidance is that oversized MCP tool sets measurably degrade agent accuracy and consume significant context budget, so this surface errs toward fewer, workflow-shaped tools rather than one-to-one coverage of the REST endpoints.

### Audit log

Every mutation is recorded in an internal `audit_log` table. There is no API for querying it yet. For now, if something goes wrong and you need to reconstruct what happened, query the database directly. An API for audit inspection is an obvious future addition.

## Further reading

Three supplementary documents live under [docs/](docs/).

The [REST API reference](docs/api.md) documents every endpoint with request and response shapes, required authorization tier, and a curl example of each. Read this when you want the precise contract for something the README only sketches at a conceptual level.

The [MCP reference](docs/mcp.md) documents each of the fifteen MCP tools with its argument schema, return shape, and an example invocation. Read this when integrating an MCP client, or when debugging why an agent is picking or misusing a particular tool.

The [operator cookbook](docs/operators.md) is a short collection of curl recipes for the admin-only workflows the MCP server does not expose: creating teams, issuing API keys, restoring soft-deleted resources, inspecting the audit log, and a few debugging tricks.

## Building from source

You need Go 1.22 or later. The project uses only pure Go dependencies, so no CGO is involved.

```
git clone https://github.com/clone45/tasks127.git
cd tasks127
go build -o tasks127 ./cmd/tasks127
./tasks127
```

The whole thing compiles to a single static binary. Cross compiling is just a matter of setting `GOOS` and `GOARCH`:

```
GOOS=linux GOARCH=amd64 go build -o tasks127 ./cmd/tasks127
```

Running the tests:

```
go test ./...
```

## Project status

This is early software, written primarily because I got tired of paying for Linear to do something I could run for free on the same box as my agent. It works, the tests pass, and I use it myself.

Major features are in place: teams, projects, tickets with two level nesting, comments, users, API keys, visibility scoping, bulk operations, subscriptions, and webhooks. The things still pending are mostly quality of life: labels, priority, per team custom statuses, and a TTL sweeper for old subscription events. None of those block real use.

If you find a bug or have an idea, open an issue. No promises on response time, but I will read it.

## License

MIT. See LICENSE for the full text.
