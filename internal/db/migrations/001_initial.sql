-- +goose Up

CREATE TABLE users (
    id         TEXT PRIMARY KEY,
    name       TEXT NOT NULL,
    email      TEXT NOT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    deleted_at TIMESTAMP
);

CREATE UNIQUE INDEX idx_users_email_active ON users(email) WHERE deleted_at IS NULL;

CREATE TABLE api_keys (
    id           TEXT PRIMARY KEY,
    key_hash     TEXT NOT NULL,
    key_prefix   TEXT NOT NULL,
    tier         TEXT NOT NULL CHECK (tier IN ('admin', 'user')),
    user_id      TEXT REFERENCES users(id),
    name         TEXT NOT NULL,
    expires_at   TIMESTAMP,
    last_used_at TIMESTAMP,
    created_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    deleted_at   TIMESTAMP,
    CHECK (tier != 'user' OR user_id IS NOT NULL)
);

CREATE INDEX idx_api_keys_hash ON api_keys(key_hash);

-- +goose Down

DROP TABLE api_keys;
DROP TABLE users;
