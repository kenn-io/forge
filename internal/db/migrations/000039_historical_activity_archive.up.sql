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
    issue_cursor                         TEXT,
    issue_inventory_complete             INTEGER NOT NULL DEFAULT 0
                                             CHECK (issue_inventory_complete IN (0, 1)),
    merge_request_cursor                 TEXT,
    merge_request_inventory_complete     INTEGER NOT NULL DEFAULT 0
                                             CHECK (merge_request_inventory_complete IN (0, 1)),
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
    prompt_since                         DATETIME,
    prompt_issue_cursor                  TEXT,
    prompt_issue_complete                INTEGER NOT NULL DEFAULT 0
                                             CHECK (prompt_issue_complete IN (0, 1)),
    prompt_merge_request_cursor          TEXT,
    prompt_merge_request_complete        INTEGER NOT NULL DEFAULT 0
                                             CHECK (prompt_merge_request_complete IN (0, 1))
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
    comments_status               TEXT NOT NULL
                                      CHECK (comments_status IN (
                                          'pending',
                                          'complete',
                                          'unsupported',
                                          'failed',
                                          'not_applicable'
                                      )),
    reviews_status                TEXT NOT NULL
                                      CHECK (reviews_status IN (
                                          'pending',
                                          'complete',
                                          'unsupported',
                                          'failed',
                                          'not_applicable'
                                      )),
    inline_comments_status        TEXT NOT NULL
                                      CHECK (inline_comments_status IN (
                                          'pending',
                                          'complete',
                                          'unsupported',
                                          'failed',
                                          'not_applicable'
                                      )),
    mirrored_provider_updated_at  DATETIME,
    attempt_count                 INTEGER NOT NULL DEFAULT 0
                                      CHECK (attempt_count >= 0),
    next_retry_at                 DATETIME,
    last_error_code               TEXT,
    last_error_detail             TEXT,
    hydrated_at                   DATETIME,
    hydration_snapshot_updated_at DATETIME,
    CHECK (
        (
            item_type = 'issue'
            AND comments_status <> 'not_applicable'
            AND reviews_status = 'not_applicable'
            AND inline_comments_status = 'not_applicable'
        )
        OR
        (
            item_type = 'merge_request'
            AND comments_status <> 'not_applicable'
            AND reviews_status <> 'not_applicable'
            AND inline_comments_status <> 'not_applicable'
        )
    ),
    PRIMARY KEY (repo_id, item_type, item_number),
    UNIQUE (repo_id, item_type, provider_item_id)
);

CREATE INDEX idx_archive_items_due_work
    ON middleman_archive_items(
        repo_id,
        next_retry_at,
        provider_created_at,
        item_type,
        item_number
    )
    WHERE lifecycle_state = 'active'
      AND (
          comments_status IN ('pending', 'failed')
          OR reviews_status IN ('pending', 'failed')
          OR inline_comments_status IN ('pending', 'failed')
      );

CREATE INDEX idx_archive_items_stable_order
    ON middleman_archive_items(
        repo_id,
        provider_created_at,
        item_type,
        item_number
    );

CREATE TABLE middleman_archive_dataset_pages (
    repo_id             INTEGER NOT NULL,
    item_type           TEXT NOT NULL,
    item_number         INTEGER NOT NULL,
    dataset             TEXT NOT NULL CHECK (dataset IN ('comments', 'reviews', 'inline_comments')),
    snapshot_updated_at DATETIME NOT NULL,
    domain_revision     INTEGER NOT NULL CHECK (domain_revision >= 0),
    page_number         INTEGER NOT NULL CHECK (page_number >= 0),
    input_cursor        TEXT NOT NULL,
    next_cursor         TEXT NOT NULL,
    exhausted           INTEGER NOT NULL CHECK (exhausted IN (0, 1)),
    record_count        INTEGER NOT NULL CHECK (record_count >= 0),
    payload              BLOB NOT NULL,
    created_at           DATETIME NOT NULL,
    PRIMARY KEY (repo_id, item_type, item_number, dataset, domain_revision, page_number),
    FOREIGN KEY (repo_id, item_type, item_number)
        REFERENCES middleman_archive_items(repo_id, item_type, item_number)
        ON DELETE CASCADE
);
