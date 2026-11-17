-- A recent-pull export resolves cross-references between pull requests. This
-- holds everything that resolution reads, so the export no longer reads every
-- event in the store to find them.
CREATE INDEX idx_mr_events_cross_references
    ON forge_mr_events(merge_request_id, repo_id, dedupe_key, created_at, metadata_json)
    WHERE event_type = 'cross_referenced';
-- The export takes each actor's latest recorded role in a repository.
CREATE INDEX idx_mr_events_repo_roles
    ON forge_mr_events(repo_id, author, created_at, author_association)
    WHERE author_association <> '';
