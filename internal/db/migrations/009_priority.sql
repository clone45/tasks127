-- +goose Up

-- Priority field on tickets, matching Linear's 0-4 convention:
--   0 = None (the default, meaning "no priority set")
--   1 = Urgent
--   2 = High
--   3 = Medium (also called "Normal" in some Linear views)
--   4 = Low
--
-- Note on sort order: Linear displays 1 first and 0 last-of-all, which
-- cannot be expressed as a plain ORDER BY on the stored int. Callers who
-- want that ordering should either exclude priority=0 from queries or
-- re-order client-side. The field itself is a natural integer.

ALTER TABLE tickets ADD COLUMN priority INTEGER NOT NULL DEFAULT 0
    CHECK (priority >= 0 AND priority <= 4);

CREATE INDEX idx_tickets_priority ON tickets(priority) WHERE deleted_at IS NULL;

-- +goose Down

DROP INDEX idx_tickets_priority;
ALTER TABLE tickets DROP COLUMN priority;
