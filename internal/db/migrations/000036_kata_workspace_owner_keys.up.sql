-- The workspace owner-key artifacts are reconciled in Go after migrations run.
-- Some legacy repair tests synthesize intermediate schemas that already carry
-- item_key, and SQLite cannot conditionally ADD COLUMN in a plain SQL file.
SELECT 1;
