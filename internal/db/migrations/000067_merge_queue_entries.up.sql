CREATE TABLE forge_repo_merge_queues (
    repo_id INTEGER PRIMARY KEY REFERENCES forge_repos(id) ON DELETE CASCADE,
    branch TEXT NOT NULL,
    merge_method TEXT NOT NULL,
    observed_at DATETIME NOT NULL
);

CREATE TABLE forge_merge_queue_entries (
    repo_id INTEGER NOT NULL REFERENCES forge_repos(id) ON DELETE CASCADE,
    number INTEGER NOT NULL,
    position INTEGER NOT NULL,
    state TEXT NOT NULL,
    enqueued_at DATETIME NOT NULL,
    estimated_time_to_merge_seconds INTEGER,
    observed_at DATETIME NOT NULL,
    PRIMARY KEY (repo_id, number)
);

-- A row is a pending departure until removed_at is set from the provider's
-- removal event; only resolved rows are shown as removals.
CREATE TABLE forge_merge_queue_removals (
    repo_id INTEGER NOT NULL REFERENCES forge_repos(id) ON DELETE CASCADE,
    number INTEGER NOT NULL,
    enqueued_at DATETIME NOT NULL,
    departed_at DATETIME NOT NULL,
    removed_at DATETIME,
    reason TEXT NOT NULL,
    PRIMARY KEY (repo_id, number)
);

CREATE TRIGGER forge_merge_queue_removals_terminal_eviction
AFTER UPDATE OF state ON forge_merge_requests
WHEN NEW.state IN ('closed', 'merged')
BEGIN
    DELETE FROM forge_merge_queue_removals
    WHERE repo_id = NEW.repo_id AND number = NEW.number;
END;
