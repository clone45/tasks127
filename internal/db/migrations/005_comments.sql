-- +goose Up

CREATE TABLE comments (
    id              TEXT PRIMARY KEY,
    ticket_id       TEXT NOT NULL REFERENCES tickets(id),
    team_id         TEXT NOT NULL REFERENCES teams(id), -- denormalized for efficient visibility scoping
    author_user_id  TEXT NOT NULL REFERENCES users(id),
    body            TEXT NOT NULL,
    created_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at      TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    deleted_at      TIMESTAMP
);

CREATE INDEX idx_comments_ticket ON comments(ticket_id)      WHERE deleted_at IS NULL;
CREATE INDEX idx_comments_team   ON comments(team_id)        WHERE deleted_at IS NULL;
CREATE INDEX idx_comments_author ON comments(author_user_id) WHERE deleted_at IS NULL;

-- +goose Down

DROP TABLE comments;
