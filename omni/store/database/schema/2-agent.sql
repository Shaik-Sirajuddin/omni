CREATE TABLE IF NOT EXISTS agents (
    id            TEXT PRIMARY KEY,
    name          TEXT NOT NULL,
    workspace_dir TEXT NOT NULL,
    memory_dir    TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS agent_settings (
    agent_id               TEXT PRIMARY KEY,
    sandbox                TEXT NOT NULL DEFAULT '{}',
    default_model_provider TEXT NOT NULL DEFAULT '',
    default_model_name     TEXT NOT NULL DEFAULT '',
    run_config             TEXT NOT NULL DEFAULT '{}',
    FOREIGN KEY (agent_id) REFERENCES agents(id)
);
