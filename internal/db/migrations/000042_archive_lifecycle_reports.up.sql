ALTER TABLE middleman_merge_requests
    ADD COLUMN files_changed INTEGER;

ALTER TABLE middleman_merge_requests
    ADD COLUMN merge_commit_sha TEXT NOT NULL DEFAULT '';

ALTER TABLE middleman_archive_repos
    ADD COLUMN issues_coverage TEXT NOT NULL DEFAULT 'unknown'
        CHECK (issues_coverage IN ('unknown', 'supported', 'unsupported'));

ALTER TABLE middleman_archive_repos
    ADD COLUMN merge_requests_coverage TEXT NOT NULL DEFAULT 'unknown'
        CHECK (merge_requests_coverage IN ('unknown', 'supported', 'unsupported'));
