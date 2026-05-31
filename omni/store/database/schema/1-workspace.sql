CREATE TABLE IF NOT EXISTS workspaces (
    id            TEXT PRIMARY KEY,
    name          TEXT NOT NULL,
    remote        TEXT NOT NULL DEFAULT 'localhost',
    workspace_dir TEXT NOT NULL UNIQUE,
    UNIQUE (remote, name)
);
