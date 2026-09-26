ALTER TABLE forge_merge_requests ADD COLUMN author_association TEXT;
ALTER TABLE forge_issues ADD COLUMN author_association TEXT;
ALTER TABLE forge_mr_events ADD COLUMN author_association TEXT;
