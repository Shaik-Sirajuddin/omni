CREATE TABLE IF NOT EXISTS message_groups (
    id         TEXT PRIMARY KEY,
    created_at INTEGER NOT NULL DEFAULT 0,
    count      INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS messages (
    id            TEXT PRIMARY KEY,
    "to"          TEXT NOT NULL,
    "from"        TEXT NOT NULL,
    from_spec     TEXT NOT NULL DEFAULT 'omni_agent',
    to_spec       TEXT NOT NULL DEFAULT 'omni_agent',
    request_type  TEXT NOT NULL DEFAULT 'query',
    is_response   INTEGER NOT NULL DEFAULT 0,
    should_reply  INTEGER NOT NULL DEFAULT 1,
    responded_to  TEXT NOT NULL DEFAULT '',
    prompt        TEXT NOT NULL DEFAULT '',
    schema        TEXT NOT NULL DEFAULT '',
    refs          TEXT NOT NULL DEFAULT '{}',
    workspace     TEXT NOT NULL DEFAULT '',
    task_id       TEXT NOT NULL DEFAULT '',
    creator_agent_id TEXT NOT NULL DEFAULT '',
    status        TEXT NOT NULL DEFAULT 'in_queue',
    retries       INTEGER NOT NULL DEFAULT 0,
    queue_time    INTEGER NOT NULL DEFAULT 0,
    delivery_time INTEGER,
    sent_time     INTEGER NOT NULL DEFAULT 0,
    group_id      TEXT NOT NULL DEFAULT '',
    FOREIGN KEY (group_id) REFERENCES message_groups(id)
);

CREATE INDEX IF NOT EXISTS idx_messages_group  ON messages(group_id);
CREATE INDEX IF NOT EXISTS idx_messages_conv   ON messages("to", "from");
CREATE INDEX IF NOT EXISTS idx_messages_status ON messages(status);
CREATE INDEX IF NOT EXISTS idx_messages_task   ON messages("to", task_id, creator_agent_id, status, sent_time);
