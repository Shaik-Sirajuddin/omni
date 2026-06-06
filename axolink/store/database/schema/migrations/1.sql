ALTER TABLE messages ADD COLUMN workspace TEXT NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS idx_messages_workspace ON messages(workspace);
