DROP INDEX IF EXISTS idx_messages_conv;
CREATE INDEX IF NOT EXISTS idx_messages_conv ON messages("to", "from");
