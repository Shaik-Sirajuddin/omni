ALTER TABLE messages ADD COLUMN queue_time INTEGER NOT NULL DEFAULT 0;
CREATE INDEX IF NOT EXISTS idx_messages_queue_time ON messages(queue_time) WHERE queue_time > 0;
