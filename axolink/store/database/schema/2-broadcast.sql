CREATE TABLE IF NOT EXISTS broadcast_mcp_clients (
    server_id          TEXT PRIMARY KEY,
    agent_id           TEXT NOT NULL,
    callback_tool_name TEXT NOT NULL DEFAULT '',
    callback_type      TEXT NOT NULL DEFAULT 'http',
    endpoint           TEXT NOT NULL DEFAULT '',
    authentication_ref TEXT NOT NULL DEFAULT '',
    updated_at         INTEGER NOT NULL DEFAULT 0,
    FOREIGN KEY (agent_id) REFERENCES agents(id)
);

CREATE TABLE IF NOT EXISTS broadcast_callback_attempts (
    id           TEXT PRIMARY KEY,
    message_id   TEXT NOT NULL,
    server_id    TEXT NOT NULL,
    status       TEXT NOT NULL DEFAULT 'pending',
    error        TEXT NOT NULL DEFAULT '',
    attempted_at INTEGER NOT NULL DEFAULT 0,
    FOREIGN KEY (message_id) REFERENCES messages(id),
    FOREIGN KEY (server_id)  REFERENCES broadcast_mcp_clients(server_id)
);

CREATE INDEX IF NOT EXISTS idx_bca_message  ON broadcast_callback_attempts(message_id);
CREATE INDEX IF NOT EXISTS idx_bca_server   ON broadcast_callback_attempts(server_id);
