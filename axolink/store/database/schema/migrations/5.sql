ALTER TABLE messages ADD COLUMN schema TEXT NOT NULL DEFAULT '';
ALTER TABLE messages ADD COLUMN task_id TEXT NOT NULL DEFAULT '';
ALTER TABLE messages ADD COLUMN creator_agent_id TEXT NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS idx_messages_task ON messages("to", task_id, creator_agent_id, status, sent_time);
