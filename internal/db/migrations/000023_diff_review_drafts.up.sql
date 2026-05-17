CREATE TABLE IF NOT EXISTS middleman_mr_review_drafts (
    id INTEGER PRIMARY KEY,
    merge_request_id INTEGER NOT NULL REFERENCES middleman_merge_requests(id) ON DELETE CASCADE,
    body TEXT NOT NULL DEFAULT '',
    action TEXT NOT NULL DEFAULT 'comment',
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    UNIQUE(merge_request_id)
);

CREATE TABLE IF NOT EXISTS middleman_mr_review_draft_comments (
    id INTEGER PRIMARY KEY,
    draft_id INTEGER NOT NULL REFERENCES middleman_mr_review_drafts(id) ON DELETE CASCADE,
    body TEXT NOT NULL,
    path TEXT NOT NULL,
    old_path TEXT,
    side TEXT NOT NULL,
    start_side TEXT,
    start_line INTEGER,
    line INTEGER NOT NULL,
    old_line INTEGER,
    new_line INTEGER,
    line_type TEXT NOT NULL,
    diff_head_sha TEXT NOT NULL,
    commit_sha TEXT NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS middleman_mr_review_threads (
    id INTEGER PRIMARY KEY,
    merge_request_id INTEGER NOT NULL REFERENCES middleman_merge_requests(id) ON DELETE CASCADE,
    provider_thread_id TEXT NOT NULL,
    provider_review_id TEXT,
    provider_comment_id TEXT,
    path TEXT NOT NULL,
    old_path TEXT,
    side TEXT NOT NULL,
    start_side TEXT,
    start_line INTEGER,
    line INTEGER NOT NULL,
    old_line INTEGER,
    new_line INTEGER,
    line_type TEXT NOT NULL,
    diff_head_sha TEXT NOT NULL,
    commit_sha TEXT NOT NULL,
    body TEXT NOT NULL,
    author_login TEXT,
    resolved BOOLEAN NOT NULL DEFAULT false,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    resolved_at TEXT,
    metadata_json TEXT,
    UNIQUE(merge_request_id, provider_thread_id)
);

CREATE INDEX IF NOT EXISTS idx_mr_review_draft_comments_draft_id
    ON middleman_mr_review_draft_comments(draft_id);

CREATE INDEX IF NOT EXISTS idx_mr_review_threads_mr_id
    ON middleman_mr_review_threads(merge_request_id);
