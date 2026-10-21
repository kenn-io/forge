-- A workspace deletion requested with a merge that went to the base branch's
-- merge queue. The queue merges the pull request later, so the request waits
-- here until sync observes the pull request merged (delete the workspace) or
-- closed or out of the queue (drop the request). host_key names the Fleet
-- daemon that owns the workspace; '' is this daemon. requested_at is when the
-- merge was submitted, so only queue reads made after it can drop the request.
CREATE TABLE forge_merge_workspace_cleanups (
    repo_id INTEGER NOT NULL REFERENCES forge_repos(id) ON DELETE CASCADE,
    number INTEGER NOT NULL,
    workspace_id TEXT NOT NULL,
    host_key TEXT NOT NULL,
    requested_at DATETIME NOT NULL,
    PRIMARY KEY (repo_id, number)
);
