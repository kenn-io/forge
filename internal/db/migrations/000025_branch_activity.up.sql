CREATE TABLE IF NOT EXISTS middleman_branch_commits (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    repo_id         INTEGER NOT NULL REFERENCES middleman_repos(id) ON DELETE CASCADE,
    branch_name     TEXT NOT NULL,
    commit_sha      TEXT NOT NULL,
    author_name     TEXT NOT NULL DEFAULT '',
    author_email    TEXT NOT NULL DEFAULT '',
    authored_at     DATETIME NOT NULL,
    committer_name  TEXT NOT NULL DEFAULT '',
    committer_email TEXT NOT NULL DEFAULT '',
    committed_at    DATETIME NOT NULL,
    subject         TEXT NOT NULL DEFAULT '',
    observed_order  INTEGER NOT NULL DEFAULT 0,
    created_at      DATETIME NOT NULL DEFAULT (datetime('now')),
    updated_at      DATETIME NOT NULL DEFAULT (datetime('now'))
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_branch_commits_repo_branch_sha
    ON middleman_branch_commits(repo_id, branch_name, commit_sha);
CREATE INDEX IF NOT EXISTS idx_branch_commits_repo_committed
    ON middleman_branch_commits(repo_id, committed_at DESC, observed_order DESC);
CREATE INDEX IF NOT EXISTS idx_branch_commits_committed
    ON middleman_branch_commits(committed_at DESC, observed_order DESC);

CREATE TABLE IF NOT EXISTS middleman_branch_tips (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    repo_id     INTEGER NOT NULL REFERENCES middleman_repos(id) ON DELETE CASCADE,
    branch_name TEXT NOT NULL,
    tip_sha     TEXT NOT NULL,
    observed_at DATETIME NOT NULL,
    created_at  DATETIME NOT NULL DEFAULT (datetime('now')),
    updated_at  DATETIME NOT NULL DEFAULT (datetime('now'))
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_branch_tips_repo_branch
    ON middleman_branch_tips(repo_id, branch_name);

CREATE TABLE IF NOT EXISTS middleman_branch_force_pushes (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    repo_id     INTEGER NOT NULL REFERENCES middleman_repos(id) ON DELETE CASCADE,
    branch_name TEXT NOT NULL,
    before_sha  TEXT NOT NULL,
    after_sha   TEXT NOT NULL,
    before_observed_at DATETIME NOT NULL,
    detected_at DATETIME NOT NULL,
    created_at  DATETIME NOT NULL DEFAULT (datetime('now'))
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_branch_force_pushes_dedupe
    ON middleman_branch_force_pushes(repo_id, branch_name, before_sha, after_sha, before_observed_at);
CREATE INDEX IF NOT EXISTS idx_branch_force_pushes_repo_detected
    ON middleman_branch_force_pushes(repo_id, detected_at DESC);
CREATE INDEX IF NOT EXISTS idx_branch_force_pushes_detected
    ON middleman_branch_force_pushes(detected_at DESC);
