-- Reference copy only. This file is NOT executed automatically.
-- Migrations are applied by applyMigrations() in internal/store.go via the
-- schemaMigrations slice. Add new migrations there, not here.
ALTER TABLE pty_sessions ADD COLUMN submit_key TEXT NOT NULL DEFAULT '';
