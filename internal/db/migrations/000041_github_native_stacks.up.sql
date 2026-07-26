CREATE TABLE github_native_stacks (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    repo_id INTEGER NOT NULL REFERENCES middleman_repos(id) ON DELETE CASCADE,
    github_id INTEGER NOT NULL,
    stack_number INTEGER NOT NULL,
    size INTEGER NOT NULL,
    base_ref TEXT NOT NULL,
    is_open INTEGER NOT NULL,
    github_created_at DATETIME NOT NULL,
    content_fingerprint TEXT NOT NULL,
    last_observed_at DATETIME NOT NULL,
    UNIQUE (repo_id, stack_number)
);

CREATE TABLE github_native_stack_members (
    stack_id INTEGER NOT NULL REFERENCES github_native_stacks(id) ON DELETE CASCADE,
    position INTEGER NOT NULL,
    pull_request_number INTEGER NOT NULL,
    state TEXT NOT NULL,
    is_draft INTEGER NOT NULL,
    merged_at DATETIME,
    head_ref TEXT NOT NULL,
    head_sha TEXT NOT NULL,
    PRIMARY KEY (stack_id, position),
    UNIQUE (stack_id, pull_request_number)
);

CREATE INDEX idx_github_native_stacks_repo_open
    ON github_native_stacks(repo_id, is_open, stack_number DESC);
