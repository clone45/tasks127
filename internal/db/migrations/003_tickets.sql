-- +goose Up

CREATE TABLE tickets (
    id                TEXT PRIMARY KEY,
    team_id           TEXT NOT NULL REFERENCES teams(id),
    project_id        TEXT REFERENCES projects(id),
    parent_id         TEXT REFERENCES tickets(id),
    title             TEXT NOT NULL,
    description       TEXT NOT NULL DEFAULT '',
    status            TEXT NOT NULL DEFAULT 'open'
        CHECK (status IN ('open', 'in_progress', 'blocked', 'done', 'canceled')),
    assignee_user_id  TEXT REFERENCES users(id),
    created_at        TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at        TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    deleted_at        TIMESTAMP
);

CREATE INDEX idx_tickets_team     ON tickets(team_id);
CREATE INDEX idx_tickets_project  ON tickets(project_id)        WHERE project_id IS NOT NULL;
CREATE INDEX idx_tickets_parent   ON tickets(parent_id)         WHERE parent_id IS NOT NULL;
CREATE INDEX idx_tickets_status   ON tickets(status)            WHERE deleted_at IS NULL;
CREATE INDEX idx_tickets_assignee ON tickets(assignee_user_id)  WHERE assignee_user_id IS NOT NULL;

-- +goose Down

DROP TABLE tickets;
