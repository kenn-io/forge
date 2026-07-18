ALTER TABLE middleman_repos DROP COLUMN backfill_pr_page;
ALTER TABLE middleman_repos DROP COLUMN backfill_pr_complete;
ALTER TABLE middleman_repos DROP COLUMN backfill_pr_completed_at;
ALTER TABLE middleman_repos DROP COLUMN backfill_issue_page;
ALTER TABLE middleman_repos DROP COLUMN backfill_issue_complete;
ALTER TABLE middleman_repos DROP COLUMN backfill_issue_completed_at;

ALTER TABLE middleman_merge_requests
    ADD COLUMN snapshot_revision INTEGER NOT NULL DEFAULT 0
        CHECK (snapshot_revision >= 0);

ALTER TABLE middleman_issues
    ADD COLUMN snapshot_revision INTEGER NOT NULL DEFAULT 0
        CHECK (snapshot_revision >= 0);

-- Existing domain rows predate revision guards. Start them at one so the
-- next parent upsert advances the revision even when timestamps are equal.
UPDATE middleman_merge_requests SET snapshot_revision = 1;
UPDATE middleman_issues SET snapshot_revision = 1;

CREATE TABLE middleman_archive_repos (
    repo_id                              INTEGER PRIMARY KEY
                                             REFERENCES middleman_repos(id)
                                             ON DELETE CASCADE,
    collection_mode                      TEXT NOT NULL
                                             CHECK (collection_mode IN ('discovery', 'full')),
    operator_state                       TEXT NOT NULL
                                             CHECK (operator_state IN ('active', 'paused')),
    initial_started_at                   DATETIME,
    initial_completed_at                 DATETIME,
    maintenance_watermark                DATETIME,
    maintenance_succeeded_at             DATETIME,
    comments_coverage                    TEXT NOT NULL DEFAULT 'unknown'
                                             CHECK (comments_coverage IN (
                                                 'unknown', 'supported', 'unsupported'
                                             )),
    reviews_coverage                     TEXT NOT NULL DEFAULT 'unknown'
                                             CHECK (reviews_coverage IN (
                                                 'unknown', 'supported', 'unsupported'
                                             )),
    inline_comments_coverage             TEXT NOT NULL DEFAULT 'unknown'
                                             CHECK (inline_comments_coverage IN (
                                                 'unknown', 'supported', 'unsupported'
                                             )),
    last_error_code                      TEXT,
    last_error_detail                    TEXT,
    next_retry_at                        DATETIME,
    created_at                           DATETIME NOT NULL,
    updated_at                           DATETIME NOT NULL,
    prompt_scan_started_at               DATETIME,
    prompt_since                         DATETIME
);

CREATE INDEX idx_archive_repos_due_work
    ON middleman_archive_repos(operator_state, next_retry_at, updated_at, repo_id);

CREATE TABLE middleman_archive_items (
    repo_id                       INTEGER NOT NULL
                                      REFERENCES middleman_repos(id)
                                      ON DELETE CASCADE,
    item_type                     TEXT NOT NULL
                                      CHECK (item_type IN ('issue', 'merge_request')),
    item_number                   INTEGER NOT NULL CHECK (item_number > 0),
    provider_item_id              TEXT NOT NULL,
    provider_created_at           DATETIME NOT NULL,
    provider_updated_at           DATETIME NOT NULL,
    lifecycle_state               TEXT NOT NULL
                                      CHECK (lifecycle_state IN (
                                          'active',
                                          'removed_upstream',
                                          'inaccessible'
                                      )),
    refresh_reason                TEXT NOT NULL DEFAULT 'initial'
                                      CHECK (refresh_reason IN (
                                          'initial',
                                          'prompt'
                                      )),
    PRIMARY KEY (repo_id, item_type, item_number),
    UNIQUE (repo_id, item_type, provider_item_id)
);

-- Dataset-level due state lives in middleman_archive_dataset_progress; the
-- item indexes only provide stable created-order traversal, with the partial
-- due-work index restricted to items whose progress rows can still be due.
CREATE INDEX idx_archive_items_due_work
    ON middleman_archive_items(
        repo_id,
        provider_created_at,
        item_type,
        item_number
    )
    WHERE lifecycle_state = 'active';

CREATE INDEX idx_archive_items_stable_order
    ON middleman_archive_items(
        repo_id,
        provider_created_at,
        item_type,
        item_number
    );

CREATE TABLE middleman_archive_repo_scans (
    repo_id INTEGER NOT NULL REFERENCES middleman_repos(id) ON DELETE CASCADE,
    scan TEXT NOT NULL CHECK (scan IN ('issue_inventory','merge_request_inventory','maintenance_issues','maintenance_merge_requests')),
    scan_generation INTEGER NOT NULL DEFAULT 1,
    next_cursor TEXT,
    last_input_cursor TEXT,
    page_count INTEGER NOT NULL DEFAULT 0 CHECK (page_count >= 0),
    status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','running','complete','blocked','failed')),
    last_error_code TEXT,
    last_error_detail TEXT,
    updated_at TEXT NOT NULL,
    PRIMARY KEY (repo_id, scan)
);

CREATE TABLE middleman_archive_dataset_progress (
    repo_id INTEGER NOT NULL,
    item_type TEXT NOT NULL CHECK (item_type IN ('issue','merge_request')),
    item_number INTEGER NOT NULL CHECK (item_number > 0),
    dataset TEXT NOT NULL CHECK (dataset IN ('lookup','comments','reviews','inline_comments')),
    parent_revision INTEGER NOT NULL DEFAULT 0,
    scan_generation INTEGER NOT NULL DEFAULT 1,
    next_cursor TEXT,
    last_input_cursor TEXT,
    page_count INTEGER NOT NULL DEFAULT 0 CHECK (page_count >= 0),
    status TEXT NOT NULL CHECK (status IN ('pending','running','complete','unsupported','blocked','failed','terminal')),
    observed_count INTEGER NOT NULL DEFAULT 0 CHECK (observed_count >= 0),
    attempt_count INTEGER NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
    next_retry_at TEXT,
    last_error_code TEXT,
    last_error_detail TEXT,
    started_at TEXT,
    completed_at TEXT,
    updated_at TEXT NOT NULL,
    PRIMARY KEY (repo_id, item_type, item_number, dataset),
    FOREIGN KEY (repo_id, item_type, item_number)
        REFERENCES middleman_archive_items(repo_id, item_type, item_number) ON DELETE CASCADE
);
CREATE INDEX idx_archive_dataset_progress_due
    ON middleman_archive_dataset_progress (repo_id, status, next_retry_at, item_type, item_number);

ALTER TABLE middleman_issue_events ADD COLUMN ingest_generation INTEGER;
ALTER TABLE middleman_mr_events ADD COLUMN ingest_generation INTEGER;
