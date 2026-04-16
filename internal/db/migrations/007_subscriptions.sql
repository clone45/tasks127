-- +goose Up

CREATE TABLE subscriptions (
    id              TEXT PRIMARY KEY,
    api_key_id      TEXT NOT NULL,        -- who registered it; events are delivered to this key's holder
    scope_user_id   TEXT,                  -- effective user at creation; NULL means unrestricted (admin-no-OBO)
    name            TEXT,
    resource        TEXT NOT NULL,
    event_types     TEXT NOT NULL,        -- JSON array of action names, e.g. ["create","update"]
    where_json      TEXT NOT NULL,        -- JSON of the filter DSL predicate
    max_fires       INTEGER,               -- NULL = unlimited
    fire_count      INTEGER NOT NULL DEFAULT 0,
    expires_at      TIMESTAMP,
    created_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    deleted_at      TIMESTAMP
);

CREATE INDEX idx_subscriptions_active ON subscriptions(resource, deleted_at)
    WHERE deleted_at IS NULL;
CREATE INDEX idx_subscriptions_owner  ON subscriptions(api_key_id) WHERE deleted_at IS NULL;

CREATE TABLE subscription_events (
    id              TEXT PRIMARY KEY,
    subscription_id TEXT NOT NULL,
    sequence        INTEGER NOT NULL,     -- monotonic per subscription; used as cursor
    timestamp       TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    resource        TEXT NOT NULL,
    resource_id     TEXT NOT NULL,
    action          TEXT NOT NULL,
    payload         TEXT NOT NULL,        -- JSON snapshot of the row at event time
    acked_at        TIMESTAMP
);

CREATE UNIQUE INDEX idx_sub_events_cursor ON subscription_events(subscription_id, sequence);
CREATE INDEX idx_sub_events_unacked       ON subscription_events(subscription_id, sequence)
    WHERE acked_at IS NULL;

-- +goose Down

DROP TABLE subscription_events;
DROP TABLE subscriptions;
