-- Kept workspace deletions are dropped; their pull requests' workspaces are
-- no longer deleted when the merge queue merges them.
DROP TABLE forge_merge_workspace_cleanups;
