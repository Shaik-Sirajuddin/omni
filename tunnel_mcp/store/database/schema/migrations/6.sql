CREATE TABLE IF NOT EXISTS task_deliveries (
    task_id         TEXT    NOT NULL,
    agent_id        TEXT    NOT NULL,
    last_message_id TEXT    NOT NULL,
    status          TEXT    NOT NULL DEFAULT 'in_progress',
    updated_at      INTEGER NOT NULL,
    PRIMARY KEY (task_id, agent_id)
);
