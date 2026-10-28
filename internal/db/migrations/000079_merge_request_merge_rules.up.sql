-- How the rules treat the merge credential's merge, read as that credential:
-- '' (unknown), 'clear', 'blocked', or 'blocked_bypassable'. It replaces the
-- bypass flag; rows start unknown and refill on their next detail sync.
ALTER TABLE forge_merge_requests ADD COLUMN merge_rules TEXT NOT NULL DEFAULT '';
ALTER TABLE forge_merge_requests DROP COLUMN viewer_can_merge_as_admin;

-- The rules hold only for the pull request state they were read against.
CREATE TRIGGER forge_merge_requests_merge_rules_reset
AFTER UPDATE OF platform_head_sha, base_branch, mergeable_state, ci_status, review_decision
ON forge_merge_requests
WHEN NEW.merge_rules <> ''
 AND (NEW.platform_head_sha, NEW.base_branch, NEW.mergeable_state, NEW.ci_status, NEW.review_decision)
  <> (OLD.platform_head_sha, OLD.base_branch, OLD.mergeable_state, OLD.ci_status, OLD.review_decision)
BEGIN
    UPDATE forge_merge_requests SET merge_rules = '' WHERE id = NEW.id;
END;
