CREATE TABLE IF NOT EXISTS prompt_sessions (
    session_id  TEXT    NOT NULL,
    prompt_id   TEXT    NOT NULL,
    delivered   INTEGER NOT NULL DEFAULT 0,
    created_at  INTEGER NOT NULL,
    PRIMARY KEY (session_id, prompt_id)
);
CREATE INDEX IF NOT EXISTS idx_prompt_sessions_created_at ON prompt_sessions(created_at);
