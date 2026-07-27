ALTER TABLE middleman_archive_repos DROP COLUMN merge_requests_coverage;
ALTER TABLE middleman_archive_repos DROP COLUMN issues_coverage;
ALTER TABLE middleman_merge_requests DROP COLUMN merge_commit_sha;
ALTER TABLE middleman_merge_requests DROP COLUMN files_changed;
