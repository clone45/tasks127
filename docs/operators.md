# Operator cookbook

This is a short guide for humans running tasks127 directly, aimed at the tasks the MCP server does not expose and the moments when something has gone sideways and you need to poke at the database.

Everything here uses curl because it is the most portable shell. Translate to your favorite client as needed. The examples assume two shell variables:

```bash
A='http://127.0.0.1:8080'
ADMIN='t127_...'   # the admin key from your first boot
```

## First-time setup

After a fresh start, you have one admin key and an empty database. Here is a sensible bootstrap sequence.

### Create a team

A team is the unit of visibility in tasks127. Users in the same team can see each other's tickets. Everything hangs off a team, so this is your first real move. The three letter key becomes the prefix for ticket display IDs if no project is assigned.

```bash
curl -H "Authorization: Bearer $ADMIN" -H "Content-Type: application/json" \
  -d '{"key":"ENG","name":"Engineering"}' \
  $A/v1/teams
```

### Create a project

Projects are optional subdivisions within a team. Tickets in a project inherit its key for display IDs, so create a project when you want a different prefix than the team's.

```bash
TEAM_ID='01...'   # id from the previous response
curl -H "Authorization: Bearer $ADMIN" -H "Content-Type: application/json" \
  -d "{\"key\":\"BAK\",\"team_id\":\"$TEAM_ID\",\"name\":\"Backend\"}" \
  $A/v1/projects
```

### Create a user

Users in tasks127 represent real people. You need users before you can add anyone to a team or assign tickets. Emails must be unique across non-deleted users.

```bash
curl -H "Authorization: Bearer $ADMIN" -H "Content-Type: application/json" \
  -d '{"name":"Alice","email":"alice@example.com"}' \
  $A/v1/users
```

### Add a user to a team

Membership is how visibility is enforced. A user sees only the teams they belong to, and only the projects and tickets within those teams. Memberships are managed separately from the team and user records so you can add or remove people without touching either.

```bash
USER_ID='01...'
curl -H "Authorization: Bearer $ADMIN" -H "Content-Type: application/json" \
  -d "{\"team_id\":\"$TEAM_ID\",\"user_id\":\"$USER_ID\"}" \
  $A/v1/team-members
```

### If you lose the admin key before you have real data

In development, the easiest recovery is to stop the server, delete the SQLite file, and start over. A fresh boot generates and prints a new admin key just like the first one. This only works as a recovery path while the database is disposable. Once you have tickets you care about, issue a second admin key ahead of time and keep it safe, because the file-delete trick will nuke everything.

```bash
# Development only. Wipes everything, including users, tickets, subscriptions, and audit log.
rm tasks127.db tasks127.db-wal tasks127.db-shm
./tasks127
# Copy the new ADMIN API KEY from stdout.
```

## Managing API keys

Every request to tasks127 carries a bearer token. How you issue, scope, and revoke those tokens is the core of the authorization story.

### Issue a user-tier key

User tier keys are the right shape for a specific human whose agent or script should only see their own teams. The key inherits the visibility of the user it is bound to.

```bash
curl -H "Authorization: Bearer $ADMIN" -H "Content-Type: application/json" \
  -d "{\"tier\":\"user\",\"user_id\":\"$USER_ID\",\"name\":\"Alice's laptop\"}" \
  $A/v1/api-keys
```

The plaintext key is in the `key` field of the response, shown exactly once. Save it immediately. On subsequent reads, only the prefix is visible, which is intentional.

### Issue a second admin key

Sometimes you want an admin key for a specific tool or integration rather than reusing the bootstrap key. Admin keys do not have an associated user.

```bash
curl -H "Authorization: Bearer $ADMIN" -H "Content-Type: application/json" \
  -d '{"tier":"admin","name":"CI deploy"}' \
  $A/v1/api-keys
```

### Rotate the bootstrap key

There is no explicit rotation endpoint. The idiomatic rotation is to issue a fresh admin key, switch your agent or scripts over to it, then revoke the old one. As long as you hold an admin key, you can always issue another.

### Revoke a key

Revocation is a soft delete, just like any other resource. The key stops authenticating immediately.

```bash
KEY_ID='01...'
curl -X DELETE -H "Authorization: Bearer $ADMIN" $A/v1/api-keys/$KEY_ID
```

If you revoked in error, the matching restore endpoint brings it back.

```bash
curl -X POST -H "Authorization: Bearer $ADMIN" $A/v1/api-keys/$KEY_ID/restore
```

### List your keys

Useful when you have accumulated keys for different agents and scripts and you want to prune the set.

```bash
curl -H "Authorization: Bearer $ADMIN" -H "Content-Type: application/json" \
  -d '{}' \
  $A/v1/api-keys/search
```

Add `{"$include_deleted": true}` to see revoked keys alongside active ones.

## Recovering from mistakes

Nothing in tasks127 is permanently deleted through the API. Every resource has a matching restore endpoint. The trick is finding the thing you deleted so you can restore it, because default queries skip soft-deleted rows.

### Find a soft-deleted resource

Use the search endpoint for the resource with the special flag that includes deleted rows.

```bash
curl -H "Authorization: Bearer $ADMIN" -H "Content-Type: application/json" \
  -d '{"$include_deleted": true, "where": {"title": {"contains": "oops"}}}' \
  $A/v1/tickets/search
```

The results will include a `deleted_at` timestamp for the ones that were removed.

### Restore it

Each resource has a `/restore` endpoint that accepts the id and flips `deleted_at` back to null.

```bash
curl -X POST -H "Authorization: Bearer $ADMIN" $A/v1/tickets/<id>/restore
```

The same pattern works for teams, projects, users, comments, team memberships, and API keys.

## Inspecting the audit log

Every mutation is recorded in the `audit_log` table at the moment of writing. There is no API endpoint for querying it yet, so this is a direct SQL exercise. On a default install that means opening the SQLite file.

```bash
sqlite3 tasks127.db
```

From there, the most useful queries tend to be these.

To see the last 20 mutations in chronological order:

```sql
SELECT timestamp, actor_api_key_id, on_behalf_of_user_id, resource, action, resource_id
FROM audit_log
ORDER BY timestamp DESC
LIMIT 20;
```

To find everything a specific API key has done:

```sql
SELECT timestamp, resource, action, resource_id, change
FROM audit_log
WHERE actor_api_key_id = '01...'
ORDER BY timestamp DESC;
```

To reconstruct what happened to a specific ticket:

```sql
SELECT timestamp, action, actor_api_key_id, change
FROM audit_log
WHERE resource = 'tickets' AND resource_id = '01...'
ORDER BY timestamp ASC;
```

The `change` column contains the JSON payload of the mutation. For bulk operations the `resource_id` column is empty and the filter and affected ids are in `change` instead.

## Health and debugging

### Is the server up?

The `/healthz` endpoint does not require authentication, which makes it convenient for uptime checks and container readiness probes.

```bash
curl $A/healthz
```

### Who am I?

If something is refusing to work the way you expect, the first question to answer is whether the server agrees with you about who you are.

```bash
curl -H "Authorization: Bearer $ADMIN" $A/v1/whoami
```

The response tells you your tier, your user id if you hold a user-tier key, and the on-behalf-of value if you sent that header. A surprising number of permission problems resolve themselves once you see that the server thinks you are scoped differently than you assumed.

### Why isn't my webhook firing?

Subscriptions with a webhook URL have a delivery history endpoint that shows recent attempts, their HTTP status codes, and any error messages. This is the right place to look when push notifications are not arriving. The endpoint is readable by two groups: admin-unrestricted callers (useful for you as the operator debugging any subscription on the system) and the owner of the subscription itself (useful for the agent or user checking their own subscriptions). User-tier callers cannot see other users' deliveries.

```bash
SUB_ID='01...'
curl -H "Authorization: Bearer $ADMIN" $A/v1/subscriptions/$SUB_ID/deliveries
```

If the state column shows `retrying` with a `next_retry_at` in the future, the worker will try again on its own. If it shows `failed`, the delivery has given up and the event is now sitting in the subscription's inbox waiting for a pull.

### Inspecting the database while the server is running

SQLite in WAL mode allows reads to happen concurrently with writes, so it is safe to open the database file read-only while the server is up. Just be careful not to hold a write lock.

```bash
sqlite3 -readonly tasks127.db
```
