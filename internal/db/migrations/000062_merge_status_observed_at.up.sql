ALTER TABLE forge_merge_requests ADD COLUMN ci_observed_at DATETIME;
ALTER TABLE forge_merge_requests ADD COLUMN review_decision_observed_at DATETIME;
ALTER TABLE forge_merge_requests ADD COLUMN mergeable_state_observed_at DATETIME;
