-- Assignees and requested reviewers for merge requests, stored as JSON
-- arrays of usernames. The empty-string default means "never reported by
-- the provider" so sync paths that lack the data (for example Forgejo
-- requested reviewers) preserve previously persisted values; '[]' records
-- a provider-confirmed empty set.
ALTER TABLE middleman_merge_requests ADD COLUMN assignees_json TEXT NOT NULL DEFAULT '';
ALTER TABLE middleman_merge_requests ADD COLUMN reviewers_json TEXT NOT NULL DEFAULT '';
