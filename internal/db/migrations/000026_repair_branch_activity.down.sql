DROP INDEX IF EXISTS idx_branch_commits_repo_branch_sha;
DROP INDEX IF EXISTS idx_branch_commits_repo_sha;
DROP INDEX IF EXISTS idx_branch_commits_repo_committed;
DROP INDEX IF EXISTS idx_branch_commits_committed;

CREATE TABLE middleman_branch_commits_prev (
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
    created_at      DATETIME NOT NULL DEFAULT (datetime('now')),
    updated_at      DATETIME NOT NULL DEFAULT (datetime('now'))
);

-- The version-25 schema keyed commits by repo+sha. If multiple tracked
-- branches now contain the same SHA, keep the oldest row on downgrade.
INSERT INTO middleman_branch_commits_prev (
    id, repo_id, branch_name, commit_sha, author_name, author_email,
    authored_at, committer_name, committer_email, committed_at, subject,
    created_at, updated_at
)
SELECT id, repo_id, branch_name, commit_sha, author_name, author_email,
       authored_at, committer_name, committer_email, committed_at, subject,
       created_at, updated_at
FROM middleman_branch_commits
WHERE id IN (
    SELECT MIN(id)
    FROM middleman_branch_commits
    GROUP BY repo_id, commit_sha
);

DROP TABLE middleman_branch_commits;
ALTER TABLE middleman_branch_commits_prev RENAME TO middleman_branch_commits;

CREATE UNIQUE INDEX idx_branch_commits_repo_sha
    ON middleman_branch_commits(repo_id, commit_sha);
CREATE INDEX idx_branch_commits_repo_committed
    ON middleman_branch_commits(repo_id, committed_at DESC);
CREATE INDEX idx_branch_commits_committed
    ON middleman_branch_commits(committed_at DESC);

DROP INDEX IF EXISTS idx_branch_tips_repo_branch;

CREATE TABLE middleman_branch_tips_prev (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    repo_id     INTEGER NOT NULL REFERENCES middleman_repos(id) ON DELETE CASCADE,
    branch_name TEXT NOT NULL,
    tip_sha     TEXT NOT NULL,
    observed_at DATETIME NOT NULL,
    created_at  DATETIME NOT NULL DEFAULT (datetime('now')),
    updated_at  DATETIME NOT NULL DEFAULT (datetime('now'))
);

INSERT INTO middleman_branch_tips_prev (
    id, repo_id, branch_name, tip_sha, observed_at, created_at, updated_at
)
SELECT id, repo_id, branch_name, tip_sha, observed_at, created_at, updated_at
FROM middleman_branch_tips;

DROP TABLE middleman_branch_tips;
ALTER TABLE middleman_branch_tips_prev RENAME TO middleman_branch_tips;

CREATE UNIQUE INDEX idx_branch_tips_repo_branch
    ON middleman_branch_tips(repo_id, branch_name);

DROP INDEX IF EXISTS idx_branch_force_pushes_dedupe;
DROP INDEX IF EXISTS idx_branch_force_pushes_repo_detected;
DROP INDEX IF EXISTS idx_branch_force_pushes_detected;

CREATE TABLE middleman_branch_force_pushes_prev (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    repo_id     INTEGER NOT NULL REFERENCES middleman_repos(id) ON DELETE CASCADE,
    branch_name TEXT NOT NULL,
    before_sha  TEXT NOT NULL,
    after_sha   TEXT NOT NULL,
    detected_at DATETIME NOT NULL,
    created_at  DATETIME NOT NULL DEFAULT (datetime('now'))
);

-- The version-25 schema keyed force pushes by before+after SHA. If a branch
-- recorded repeated A->B rewrites, keep the oldest row on downgrade.
INSERT INTO middleman_branch_force_pushes_prev (
    id, repo_id, branch_name, before_sha, after_sha, detected_at, created_at
)
SELECT id, repo_id, branch_name, before_sha, after_sha, detected_at, created_at
FROM middleman_branch_force_pushes
WHERE id IN (
    SELECT MIN(id)
    FROM middleman_branch_force_pushes
    GROUP BY repo_id, branch_name, before_sha, after_sha
);

DROP TABLE middleman_branch_force_pushes;
ALTER TABLE middleman_branch_force_pushes_prev RENAME TO middleman_branch_force_pushes;

CREATE UNIQUE INDEX idx_branch_force_pushes_dedupe
    ON middleman_branch_force_pushes(repo_id, branch_name, before_sha, after_sha);
CREATE INDEX idx_branch_force_pushes_repo_detected
    ON middleman_branch_force_pushes(repo_id, detected_at DESC);
CREATE INDEX idx_branch_force_pushes_detected
    ON middleman_branch_force_pushes(detected_at DESC);
