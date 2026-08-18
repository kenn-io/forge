ALTER TABLE forge_merge_requests
    ADD COLUMN activity_event_revision INTEGER NOT NULL DEFAULT 0;

ALTER TABLE forge_issues
    ADD COLUMN activity_event_revision INTEGER NOT NULL DEFAULT 0;

UPDATE forge_merge_requests
SET activity_event_revision = (
    SELECT COUNT(*)
    FROM forge_mr_events
    WHERE merge_request_id = forge_merge_requests.id
);

UPDATE forge_issues
SET activity_event_revision = (
    SELECT COUNT(*)
    FROM forge_issue_events
    WHERE issue_id = forge_issues.id
);

CREATE TRIGGER forge_mr_events_activity_revision_insert
AFTER INSERT ON forge_mr_events
BEGIN
    UPDATE forge_merge_requests
    SET activity_event_revision = activity_event_revision + 1
    WHERE id = NEW.merge_request_id;
END;

CREATE TRIGGER forge_mr_events_activity_revision_update
AFTER UPDATE ON forge_mr_events
WHEN OLD.merge_request_id IS NOT NEW.merge_request_id
  OR OLD.platform_id IS NOT NEW.platform_id
  OR OLD.platform_external_id IS NOT NEW.platform_external_id
  OR OLD.event_type IS NOT NEW.event_type
  OR OLD.author IS NOT NEW.author
  OR OLD.summary IS NOT NEW.summary
  OR OLD.body IS NOT NEW.body
  OR OLD.metadata_json IS NOT NEW.metadata_json
  OR OLD.created_at IS NOT NEW.created_at
  OR OLD.dedupe_key IS NOT NEW.dedupe_key
  OR OLD.direct_url IS NOT NEW.direct_url
  OR OLD.thread_id IS NOT NEW.thread_id
  OR OLD.position_json IS NOT NEW.position_json
  OR OLD.resolvable IS NOT NEW.resolvable
  OR OLD.resolved IS NOT NEW.resolved
BEGIN
    UPDATE forge_merge_requests
    SET activity_event_revision = activity_event_revision + 1
    WHERE id = OLD.merge_request_id;

    UPDATE forge_merge_requests
    SET activity_event_revision = activity_event_revision + 1
    WHERE id = NEW.merge_request_id
      AND NEW.merge_request_id IS NOT OLD.merge_request_id;
END;

CREATE TRIGGER forge_mr_events_activity_revision_delete
AFTER DELETE ON forge_mr_events
BEGIN
    UPDATE forge_merge_requests
    SET activity_event_revision = activity_event_revision + 1
    WHERE id = OLD.merge_request_id;
END;

CREATE TRIGGER forge_issue_events_activity_revision_insert
AFTER INSERT ON forge_issue_events
BEGIN
    UPDATE forge_issues
    SET activity_event_revision = activity_event_revision + 1
    WHERE id = NEW.issue_id;
END;

CREATE TRIGGER forge_issue_events_activity_revision_update
AFTER UPDATE ON forge_issue_events
WHEN OLD.issue_id IS NOT NEW.issue_id
  OR OLD.platform_id IS NOT NEW.platform_id
  OR OLD.platform_external_id IS NOT NEW.platform_external_id
  OR OLD.event_type IS NOT NEW.event_type
  OR OLD.author IS NOT NEW.author
  OR OLD.summary IS NOT NEW.summary
  OR OLD.body IS NOT NEW.body
  OR OLD.metadata_json IS NOT NEW.metadata_json
  OR OLD.created_at IS NOT NEW.created_at
  OR OLD.dedupe_key IS NOT NEW.dedupe_key
  OR OLD.direct_url IS NOT NEW.direct_url
  OR OLD.thread_id IS NOT NEW.thread_id
BEGIN
    UPDATE forge_issues
    SET activity_event_revision = activity_event_revision + 1
    WHERE id = OLD.issue_id;

    UPDATE forge_issues
    SET activity_event_revision = activity_event_revision + 1
    WHERE id = NEW.issue_id
      AND NEW.issue_id IS NOT OLD.issue_id;
END;

CREATE TRIGGER forge_issue_events_activity_revision_delete
AFTER DELETE ON forge_issue_events
BEGIN
    UPDATE forge_issues
    SET activity_event_revision = activity_event_revision + 1
    WHERE id = OLD.issue_id;
END;
