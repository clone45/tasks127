-- +goose Up

-- Webhook fields on the subscription itself. If webhook_url is NULL, the
-- subscription is pure pull (inbox only).
ALTER TABLE subscriptions ADD COLUMN webhook_url    TEXT;
ALTER TABLE subscriptions ADD COLUMN webhook_secret TEXT;

-- One delivery row per (event). Tracks cumulative state and retry schedule.
-- Separate from subscription_events so the inbox stays clean and webhooks
-- are purely additive — pure-inbox subscriptions never touch this table.
CREATE TABLE webhook_deliveries (
    id               TEXT PRIMARY KEY,
    event_id         TEXT NOT NULL UNIQUE,   -- references subscription_events.id
    subscription_id  TEXT NOT NULL,
    url              TEXT NOT NULL,           -- captured when event fires
    state            TEXT NOT NULL CHECK (state IN ('pending','retrying','delivered','failed')),
    attempts         INTEGER NOT NULL DEFAULT 0,
    last_status_code INTEGER,
    last_error       TEXT,
    next_retry_at    TIMESTAMP,               -- NULL once terminal (delivered or failed)
    delivered_at     TIMESTAMP,
    created_at       TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at       TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Index used by the retry scanner.
CREATE INDEX idx_webhook_due ON webhook_deliveries(next_retry_at)
    WHERE next_retry_at IS NOT NULL;

CREATE INDEX idx_webhook_sub ON webhook_deliveries(subscription_id);

-- One row per delivery attempt. Useful for debugging silent failures
-- ("why isn't my webhook firing?").
CREATE TABLE webhook_attempts (
    id             TEXT PRIMARY KEY,
    delivery_id    TEXT NOT NULL,
    attempt_number INTEGER NOT NULL,
    status_code    INTEGER,
    response_body  TEXT,           -- capped at insert time (~1KB)
    error          TEXT,
    duration_ms    INTEGER,
    started_at     TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX idx_webhook_attempts_delivery ON webhook_attempts(delivery_id);

-- +goose Down

DROP TABLE webhook_attempts;
DROP TABLE webhook_deliveries;
ALTER TABLE subscriptions DROP COLUMN webhook_secret;
ALTER TABLE subscriptions DROP COLUMN webhook_url;
