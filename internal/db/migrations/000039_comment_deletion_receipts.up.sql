CREATE TABLE middleman_comment_deletion_receipts (
    repo_id      INTEGER NOT NULL REFERENCES middleman_repos(id) ON DELETE CASCADE,
    item_type    TEXT NOT NULL CHECK (item_type IN ('pr', 'issue')),
    item_number  INTEGER NOT NULL,
    comment_id   INTEGER NOT NULL,
    created_at   DATETIME NOT NULL,
    PRIMARY KEY (repo_id, item_type, item_number, comment_id)
);

CREATE INDEX idx_comment_deletion_receipts_created_at
    ON middleman_comment_deletion_receipts(created_at);
