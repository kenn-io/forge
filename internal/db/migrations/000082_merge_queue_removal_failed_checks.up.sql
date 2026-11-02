-- The checks that failed on the merge group commit when the merge queue
-- removed a pull request, as a JSON array of {"name", "url"} objects. Rows
-- stored before this column read as no known failed checks until their next
-- queue read.
ALTER TABLE forge_merge_queue_states ADD COLUMN removal_failed_checks TEXT NOT NULL DEFAULT '[]';
