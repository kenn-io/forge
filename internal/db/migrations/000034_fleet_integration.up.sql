-- Registered-worktree runtime sessions. runtime_backend identifies the
-- terminal/runtime owner and backend_session_key stores the owner-specific
-- session identifier when one exists (for example, a tmux session name). label
-- is the display label persisted at launch time. Command sessions
-- (caller-supplied argv, no launch target) store their stable command session
-- key in target_key so stored runtime rows keep a non-empty target dimension.
CREATE TABLE middleman_project_worktree_runtime_sessions (
    worktree_id         TEXT NOT NULL REFERENCES middleman_project_worktrees(id) ON DELETE CASCADE,
    session_key         TEXT NOT NULL,
    target_key          TEXT NOT NULL,
    runtime_backend     TEXT NOT NULL,
    backend_session_key TEXT,
    label               TEXT NOT NULL DEFAULT '',
    created_at          DATETIME NOT NULL DEFAULT (datetime('now')),
    PRIMARY KEY (worktree_id, session_key),
    UNIQUE (session_key),
    UNIQUE (runtime_backend, backend_session_key)
);

CREATE INDEX middleman_project_worktree_runtime_sessions_worktree_id_idx
    ON middleman_project_worktree_runtime_sessions(worktree_id);

-- Workspace runtime session placement is a branch-local schema addition for
-- federated viewers. Migration 000029 is already on main, so the new column is
-- added here, in this PR's single unapplied migration.
ALTER TABLE middleman_workspace_runtime_sessions
    ADD COLUMN display_region TEXT NOT NULL DEFAULT '';

UPDATE middleman_workspace_runtime_sessions
SET display_region = CASE
    WHEN target_key = 'plain_shell' THEN 'terminal'
    ELSE 'workflow'
END
WHERE display_region = '';

-- Project/worktree discovery columns, folded into this branch migration to
-- keep a single migration delta vs main. The background discovery refresher
-- reconciles on-disk git state into the registry tables: is_stale marks rows
-- that vanished from the last successful discovery (kept, not deleted, so
-- worktree IDs and their runtime-session links survive) and clears when the path
-- reappears; repository_kind distinguishes bare from standard checkouts.
ALTER TABLE middleman_projects
    ADD COLUMN is_stale INTEGER NOT NULL DEFAULT 0;
ALTER TABLE middleman_projects
    ADD COLUMN repository_kind TEXT NOT NULL DEFAULT 'standard';
ALTER TABLE middleman_project_worktrees
    ADD COLUMN is_stale INTEGER NOT NULL DEFAULT 0;

-- Worktree visibility: a user-hidden worktree stays out of the active list but
-- is not deleted. Discovery reconciliation never writes this column, so a hidden
-- worktree survives every discovery pass (the upsert refreshes branch/is_stale
-- only). Primary worktrees are synthesized from the project row and have no
-- registry row, so they can never be hidden.
ALTER TABLE middleman_project_worktrees
    ADD COLUMN is_hidden INTEGER NOT NULL DEFAULT 0;

-- Worktree session backend: a user override for how terminal sessions attach to
-- this worktree (e.g. localTmux). Empty means "no override" and the snapshot
-- producer's empty->localPTY default applies; a persisted value wins over that
-- default. Discovery reconciliation never writes this column, mirroring
-- is_hidden, so the override survives every discovery pass.
ALTER TABLE middleman_project_worktrees
    ADD COLUMN session_backend TEXT NOT NULL DEFAULT '';

-- Worktree linked issue numbers: explicit issue links a user attached to this
-- registered worktree, stored as a JSON array of ints ('[]' when none). The
-- snapshot producer merges these explicit links with any workspace-item issue
-- linkage at the same path, deduped. Discovery reconciliation never writes this
-- column, mirroring is_hidden/session_backend, so the links survive every
-- discovery pass. This is snapshot metadata attached to a worktree, not a
-- relational issue-link model.
ALTER TABLE middleman_project_worktrees
    ADD COLUMN linked_issue_numbers TEXT NOT NULL DEFAULT '[]';

-- Live worktree git stats, keyed by normalized worktree path so a single row
-- serves both registered-project worktrees and active-workspace worktrees. The
-- background stats sampler is the only writer; the fleet snapshot read path
-- overlays these without any git I/O. A present row means "sampled" and surfaces
-- all four counts (even zero); an absent row leaves the snapshot fields null.
CREATE TABLE middleman_worktree_stats (
    path         TEXT PRIMARY KEY,
    diff_added   INTEGER NOT NULL DEFAULT 0,
    diff_removed INTEGER NOT NULL DEFAULT 0,
    sync_ahead   INTEGER NOT NULL DEFAULT 0,
    sync_behind  INTEGER NOT NULL DEFAULT 0,
    sampled_at   DATETIME NOT NULL DEFAULT (datetime('now'))
);

-- Host-scoped runtime sessions: command sessions launched on the host itself
-- rather than from a registered project worktree (e.g. a machine-level console
-- terminal). Mirrors middleman_project_worktree_runtime_sessions minus the
-- worktree FK; cwd is recorded because there is no worktree path to fall back
-- to.
CREATE TABLE middleman_host_runtime_sessions (
    session_key         TEXT PRIMARY KEY,
    runtime_backend     TEXT NOT NULL,
    backend_session_key TEXT,
    label               TEXT NOT NULL DEFAULT '',
    cwd                 TEXT NOT NULL DEFAULT '',
    created_at          DATETIME NOT NULL DEFAULT (datetime('now')),
    UNIQUE (runtime_backend, backend_session_key)
);
