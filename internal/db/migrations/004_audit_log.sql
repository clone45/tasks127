-- +goose Up

-- Append-only audit trail. Intentionally no foreign keys so a soft-deleted
-- api_key or user doesn't break the ability to inspect what they did.
CREATE TABLE audit_log (
    id                    TEXT PRIMARY KEY,
    timestamp             TIMESTAMP NOT NULL,
    actor_api_key_id      TEXT NOT NULL,
    on_behalf_of_user_id  TEXT,
    resource              TEXT NOT NULL,
    resource_id           TEXT NOT NULL,
    action                TEXT NOT NULL,
    change                TEXT NOT NULL DEFAULT '{}'
);

CREATE INDEX idx_audit_timestamp ON audit_log(timestamp);
CREATE INDEX idx_audit_actor     ON audit_log(actor_api_key_id);
CREATE INDEX idx_audit_resource  ON audit_log(resource, resource_id);

-- +goose Down

DROP TABLE audit_log;
