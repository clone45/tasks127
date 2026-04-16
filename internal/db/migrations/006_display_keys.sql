-- +goose Up

-- Global registry of 3-letter keys used by teams and projects.
-- Ticket display IDs are "{key}-{number}" where key belongs to a team or project
-- and number comes from this row's monotonic counter.
CREATE TABLE resource_keys (
    key         TEXT PRIMARY KEY CHECK (length(key) = 3 AND key GLOB '[A-Z][A-Z][A-Z]'),
    owner_type  TEXT NOT NULL CHECK (owner_type IN ('team', 'project')),
    owner_id    TEXT NOT NULL,
    next_number INTEGER NOT NULL DEFAULT 1
);

CREATE UNIQUE INDEX idx_resource_keys_owner ON resource_keys(owner_type, owner_id);

-- Keys on the owning tables (nullable at DB level; app enforces non-null for new records).
ALTER TABLE teams    ADD COLUMN key TEXT;
ALTER TABLE projects ADD COLUMN key TEXT;

-- Tickets record the key they got at creation (sticky — doesn't change if project_id moves).
ALTER TABLE tickets ADD COLUMN key    TEXT;
ALTER TABLE tickets ADD COLUMN number INTEGER;

-- Display IDs must be unique among non-deleted tickets.
CREATE UNIQUE INDEX idx_tickets_display ON tickets(key, number) WHERE deleted_at IS NULL;

-- +goose Down

DROP INDEX idx_tickets_display;
ALTER TABLE tickets  DROP COLUMN number;
ALTER TABLE tickets  DROP COLUMN key;
ALTER TABLE projects DROP COLUMN key;
ALTER TABLE teams    DROP COLUMN key;
DROP TABLE resource_keys;
