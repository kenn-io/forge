CREATE TABLE middleman_mr_review_hydration_stages (
    merge_request_id   INTEGER PRIMARY KEY REFERENCES middleman_merge_requests(id) ON DELETE CASCADE,
    provider_updated_at TEXT NOT NULL,
    head_sha           TEXT NOT NULL,
    generation         INTEGER NOT NULL,
    review_ids_json    TEXT NOT NULL,
    next_review_index  INTEGER NOT NULL DEFAULT 0,
    created_at         TEXT NOT NULL,
    updated_at         TEXT NOT NULL,
    UNIQUE(merge_request_id, generation)
);

CREATE TABLE middleman_mr_review_hydration_threads (
    id                  INTEGER PRIMARY KEY,
    merge_request_id    INTEGER NOT NULL,
    generation          INTEGER NOT NULL,
    provider_thread_id  TEXT NOT NULL,
    provider_review_id  TEXT,
    provider_comment_id TEXT,
    path                TEXT NOT NULL,
    old_path            TEXT,
    side                TEXT NOT NULL,
    start_side          TEXT,
    start_line          INTEGER,
    line                INTEGER NOT NULL,
    old_line            INTEGER,
    new_line            INTEGER,
    line_type           TEXT NOT NULL,
    diff_head_sha       TEXT NOT NULL,
    commit_sha          TEXT NOT NULL,
    body                TEXT NOT NULL,
    author_login        TEXT,
    direct_url          TEXT NOT NULL,
    resolved            BOOLEAN NOT NULL DEFAULT false,
    created_at          TEXT NOT NULL,
    updated_at          TEXT NOT NULL,
    resolved_at         TEXT,
    metadata_json       TEXT,
    FOREIGN KEY(merge_request_id, generation)
        REFERENCES middleman_mr_review_hydration_stages(merge_request_id, generation)
        ON DELETE CASCADE,
    UNIQUE(merge_request_id, generation, provider_thread_id)
);

CREATE INDEX idx_mr_review_hydration_threads_stage
    ON middleman_mr_review_hydration_threads(merge_request_id, generation, created_at, id);
