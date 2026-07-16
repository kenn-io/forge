ALTER TABLE middleman_archive_dataset_pages
    RENAME TO middleman_archive_dataset_pages_with_revision;

CREATE TABLE middleman_archive_dataset_pages (
    repo_id             INTEGER NOT NULL,
    item_type           TEXT NOT NULL,
    item_number         INTEGER NOT NULL,
    dataset             TEXT NOT NULL CHECK (dataset IN ('comments', 'reviews', 'inline_comments')),
    snapshot_updated_at DATETIME NOT NULL,
    page_number         INTEGER NOT NULL CHECK (page_number >= 0),
    input_cursor        TEXT NOT NULL,
    next_cursor         TEXT NOT NULL,
    exhausted           INTEGER NOT NULL CHECK (exhausted IN (0, 1)),
    record_count        INTEGER NOT NULL CHECK (record_count >= 0),
    payload             BLOB NOT NULL,
    created_at          DATETIME NOT NULL,
    PRIMARY KEY (repo_id, item_type, item_number, dataset, page_number),
    FOREIGN KEY (repo_id, item_type, item_number)
        REFERENCES middleman_archive_items(repo_id, item_type, item_number)
        ON DELETE CASCADE
);

INSERT INTO middleman_archive_dataset_pages (
    repo_id, item_type, item_number, dataset, snapshot_updated_at,
    page_number, input_cursor, next_cursor, exhausted, record_count, payload, created_at
)
SELECT p.repo_id, p.item_type, p.item_number, p.dataset, p.snapshot_updated_at,
       p.page_number, p.input_cursor, p.next_cursor, p.exhausted, p.record_count,
       p.payload, p.created_at
FROM middleman_archive_dataset_pages_with_revision p
WHERE p.domain_revision = (
    SELECT MAX(candidate.domain_revision)
    FROM middleman_archive_dataset_pages_with_revision candidate
    WHERE candidate.repo_id = p.repo_id
      AND candidate.item_type = p.item_type
      AND candidate.item_number = p.item_number
      AND candidate.dataset = p.dataset
);

DROP TABLE middleman_archive_dataset_pages_with_revision;

ALTER TABLE middleman_issues DROP COLUMN snapshot_revision;
ALTER TABLE middleman_merge_requests DROP COLUMN snapshot_revision;
