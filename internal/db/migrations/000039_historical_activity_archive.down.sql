-- Downgrading discards archive inventory and hydration progress because
-- older binaries cannot preserve them. Archived issue and merge request
-- content lives in the ordinary domain tables and survives untouched.
DROP INDEX idx_archive_items_stable_order;
DROP INDEX idx_archive_items_due_work;
DROP INDEX idx_archive_repos_due_work;

DROP TABLE middleman_archive_dataset_pages;
DROP TABLE middleman_archive_items;
DROP TABLE middleman_archive_repos;

ALTER TABLE middleman_issues DROP COLUMN snapshot_revision;
ALTER TABLE middleman_merge_requests DROP COLUMN snapshot_revision;

ALTER TABLE middleman_repos ADD COLUMN backfill_pr_page INTEGER NOT NULL DEFAULT 0;
ALTER TABLE middleman_repos ADD COLUMN backfill_pr_complete INTEGER NOT NULL DEFAULT 0;
ALTER TABLE middleman_repos ADD COLUMN backfill_pr_completed_at DATETIME;
ALTER TABLE middleman_repos ADD COLUMN backfill_issue_page INTEGER NOT NULL DEFAULT 0;
ALTER TABLE middleman_repos ADD COLUMN backfill_issue_complete INTEGER NOT NULL DEFAULT 0;
ALTER TABLE middleman_repos ADD COLUMN backfill_issue_completed_at DATETIME;
