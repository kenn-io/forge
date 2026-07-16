ALTER TABLE middleman_merge_requests
    ADD COLUMN snapshot_revision INTEGER NOT NULL DEFAULT 0
        CHECK (snapshot_revision >= 0);

ALTER TABLE middleman_issues
    ADD COLUMN snapshot_revision INTEGER NOT NULL DEFAULT 0
        CHECK (snapshot_revision >= 0);

-- Existing domain rows predate revision guards. Start them at one so any
-- staged archive pages migrated below remain guarded against the next parent
-- upsert, which advances the revision even when provider timestamps are equal.
UPDATE middleman_merge_requests SET snapshot_revision = 1;
UPDATE middleman_issues SET snapshot_revision = 1;

ALTER TABLE middleman_archive_dataset_pages
    RENAME TO middleman_archive_dataset_pages_without_revision;

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
    payload             BLOB NOT NULL,
    created_at          DATETIME NOT NULL,
    PRIMARY KEY (repo_id, item_type, item_number, dataset, domain_revision, page_number),
    FOREIGN KEY (repo_id, item_type, item_number)
        REFERENCES middleman_archive_items(repo_id, item_type, item_number)
        ON DELETE CASCADE
);

INSERT INTO middleman_archive_dataset_pages (
    repo_id, item_type, item_number, dataset, snapshot_updated_at,
    domain_revision, page_number, input_cursor, next_cursor, exhausted,
    record_count, payload, created_at
)
SELECT repo_id, item_type, item_number, dataset, snapshot_updated_at,
       1, page_number, input_cursor, next_cursor, exhausted,
       record_count, payload, created_at
FROM middleman_archive_dataset_pages_without_revision;

DROP TABLE middleman_archive_dataset_pages_without_revision;
