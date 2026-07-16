CREATE TABLE middleman_archive_dataset_pages (
    repo_id          INTEGER NOT NULL,
    item_type        TEXT NOT NULL,
    item_number      INTEGER NOT NULL,
    dataset          TEXT NOT NULL CHECK (dataset IN ('comments', 'reviews', 'inline_comments')),
    page_number      INTEGER NOT NULL CHECK (page_number >= 0),
    input_cursor     TEXT NOT NULL,
    next_cursor      TEXT NOT NULL,
    exhausted        INTEGER NOT NULL CHECK (exhausted IN (0, 1)),
    record_count     INTEGER NOT NULL CHECK (record_count >= 0),
    payload          BLOB NOT NULL,
    created_at       DATETIME NOT NULL,
    PRIMARY KEY (repo_id, item_type, item_number, dataset, page_number),
    FOREIGN KEY (repo_id, item_type, item_number)
        REFERENCES middleman_archive_items(repo_id, item_type, item_number)
        ON DELETE CASCADE
);
