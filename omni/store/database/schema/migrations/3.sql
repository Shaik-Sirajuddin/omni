ALTER TABLE workspaces ADD COLUMN remote TEXT NOT NULL DEFAULT 'localhost';
-- Rename all but the oldest (MIN rowid) workspace in each (remote, name) group.
-- After ALTER TABLE every pre-existing row has remote='localhost' so duplicates
-- only arise from workspaces that shared a name before this migration.
-- MIN(rowid) preserves the first-created workspace and others get an 8-char hex tag.
-- Suffix collision probability is ~1/2^32 per pair. If it occurs the UPDATE re-runs
-- on the next DB open (ALTER TABLE is skipped as duplicate column and UPDATE is a
-- no-op for already-unique rows before CREATE INDEX retries) -- fully self-healing.
UPDATE workspaces
    SET name = name || '_' || hex(randomblob(4))
    WHERE rowid NOT IN (
        SELECT MIN(rowid) FROM workspaces GROUP BY remote, name
    );
-- No explicit transaction: if the process exits between UPDATE and CREATE INDEX
-- the migration pointer is not advanced (setPointer runs only after all statements
-- succeed). On the next open ALTER TABLE is skipped and UPDATE is a no-op so
-- CREATE UNIQUE INDEX IF NOT EXISTS retries -- recoverable with no manual intervention.
CREATE UNIQUE INDEX IF NOT EXISTS idx_workspaces_remote_name ON workspaces(remote, name);
