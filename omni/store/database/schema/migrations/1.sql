ALTER TABLE code_sessions ADD COLUMN status TEXT NOT NULL DEFAULT 'ready';
ALTER TABLE code_sessions ADD COLUMN stop_reason TEXT NOT NULL DEFAULT '';
ALTER TABLE code_sessions ADD COLUMN tokens_input INTEGER NOT NULL DEFAULT 0;
ALTER TABLE code_sessions ADD COLUMN tokens_output INTEGER NOT NULL DEFAULT 0;
ALTER TABLE code_sessions ADD COLUMN tokens_cached_input INTEGER NOT NULL DEFAULT 0;
ALTER TABLE code_sessions ADD COLUMN tokens_cached_output INTEGER NOT NULL DEFAULT 0;
ALTER TABLE code_sessions ADD COLUMN tokens_max INTEGER NOT NULL DEFAULT 0;
ALTER TABLE code_sessions ADD COLUMN tokens_consumed_percent REAL NOT NULL DEFAULT 0;
