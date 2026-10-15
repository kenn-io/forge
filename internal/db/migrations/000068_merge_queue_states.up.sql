-- Merge queue state moves from repository-wide queue snapshots to one row per
-- pull request, read from the pull request itself. The snapshot tables only
-- cache provider state, so they are dropped rather than migrated.
DROP TRIGGER forge_merge_queue_terminal_insert_eviction;
DROP TRIGGER forge_merge_queue_terminal_eviction;
DROP TABLE forge_merge_queue_removals;
DROP TABLE forge_merge_queue_entries;
DROP TABLE forge_repo_merge_queues;

-- queue_enabled is GitHub's isMergeQueueEnabled for the pull request's base
-- branch. queued is set while the pull request is in the queue; position 0
-- means the queue has not reported its position yet. removal_reason is set
-- when the queue's latest action removed the pull request without merging it.
CREATE TABLE forge_merge_queue_states (
    repo_id INTEGER NOT NULL REFERENCES forge_repos(id) ON DELETE CASCADE,
    number INTEGER NOT NULL,
    base_branch TEXT NOT NULL,
    queue_enabled INTEGER NOT NULL,
    merge_method TEXT NOT NULL,
    queued INTEGER NOT NULL,
    position INTEGER NOT NULL,
    state TEXT NOT NULL,
    enqueued_at DATETIME,
    estimated_time_to_merge_seconds INTEGER,
    removed_at DATETIME,
    removal_reason TEXT NOT NULL,
    observed_at DATETIME NOT NULL,
    PRIMARY KEY (repo_id, number)
);

CREATE TRIGGER forge_merge_queue_states_terminal_eviction
AFTER UPDATE OF state ON forge_merge_requests
WHEN NEW.state IN ('closed', 'merged')
BEGIN
    DELETE FROM forge_merge_queue_states
    WHERE repo_id = NEW.repo_id AND number = NEW.number;
END;

CREATE TRIGGER forge_merge_queue_states_terminal_insert_eviction
AFTER INSERT ON forge_merge_requests
WHEN NEW.state IN ('closed', 'merged')
BEGIN
    DELETE FROM forge_merge_queue_states
    WHERE repo_id = NEW.repo_id AND number = NEW.number;
END;
