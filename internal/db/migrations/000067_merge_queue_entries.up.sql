CREATE TABLE forge_repo_merge_queues (
    repo_id INTEGER PRIMARY KEY REFERENCES forge_repos(id) ON DELETE CASCADE,
    branch TEXT NOT NULL,
    merge_method TEXT NOT NULL,
    observed_at DATETIME NOT NULL
);

-- attempt is the pull request's count of GitHub AddedToMergeQueue events
-- when the entry was observed, identifying the queue attempt; 0 is unknown.
CREATE TABLE forge_merge_queue_entries (
    repo_id INTEGER NOT NULL REFERENCES forge_repos(id) ON DELETE CASCADE,
    number INTEGER NOT NULL,
    attempt INTEGER NOT NULL,
    position INTEGER NOT NULL,
    state TEXT NOT NULL,
    enqueued_at DATETIME NOT NULL,
    estimated_time_to_merge_seconds INTEGER,
    observed_at DATETIME NOT NULL,
    PRIMARY KEY (repo_id, number)
);

-- A row is a pending departure until removed_at is set from the provider's
-- removal event; only resolved rows are shown as removals. attempt is the
-- departed entry's attempt.
CREATE TABLE forge_merge_queue_removals (
    repo_id INTEGER NOT NULL REFERENCES forge_repos(id) ON DELETE CASCADE,
    number INTEGER NOT NULL,
    attempt INTEGER NOT NULL,
    departed_at DATETIME NOT NULL,
    removed_at DATETIME,
    reason TEXT NOT NULL,
    PRIMARY KEY (repo_id, number)
);

-- A closed or merged pull request leaves the queue, and its earlier queue
-- attempt must not reappear if it is reopened before the next queue read.
CREATE TRIGGER forge_merge_queue_terminal_eviction
AFTER UPDATE OF state ON forge_merge_requests
WHEN NEW.state IN ('closed', 'merged')
BEGIN
    DELETE FROM forge_merge_queue_entries
    WHERE repo_id = NEW.repo_id AND number = NEW.number;
    DELETE FROM forge_merge_queue_removals
    WHERE repo_id = NEW.repo_id AND number = NEW.number;
END;

-- A queue snapshot can store an entry before the pull request row exists, so
-- a row first inserted as closed or merged evicts it too.
CREATE TRIGGER forge_merge_queue_terminal_insert_eviction
AFTER INSERT ON forge_merge_requests
WHEN NEW.state IN ('closed', 'merged')
BEGIN
    DELETE FROM forge_merge_queue_entries
    WHERE repo_id = NEW.repo_id AND number = NEW.number;
    DELETE FROM forge_merge_queue_removals
    WHERE repo_id = NEW.repo_id AND number = NEW.number;
END;
