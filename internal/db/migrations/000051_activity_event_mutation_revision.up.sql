ALTER TABLE forge_merge_requests
    ADD COLUMN activity_event_revision INTEGER NOT NULL DEFAULT 0;

ALTER TABLE forge_issues
    ADD COLUMN activity_event_revision INTEGER NOT NULL DEFAULT 0;

CREATE INDEX idx_forge_notification_items_activity_parent
    ON forge_notification_items(repo_id, item_type, item_number)
    WHERE repo_id IS NOT NULL;

UPDATE forge_merge_requests
SET activity_event_revision =
    (SELECT COUNT(*)
     FROM forge_mr_events
     WHERE merge_request_id = forge_merge_requests.id)
    +
    (SELECT COUNT(*)
     FROM forge_notification_items n
     JOIN forge_repos r ON r.id = forge_merge_requests.repo_id
     WHERE n.item_type = 'pr'
       AND n.item_number = forge_merge_requests.number
       AND n.reason != 'author'
       AND (
           n.repo_id = r.id
           OR (n.repo_id IS NULL
               AND n.platform = r.platform
               AND n.platform_host = r.platform_host
               AND n.repo_owner = r.owner_key
               AND n.repo_name = r.name_key)
       ));

UPDATE forge_issues
SET activity_event_revision =
    (SELECT COUNT(*)
     FROM forge_issue_events
     WHERE issue_id = forge_issues.id)
    +
    (SELECT COUNT(*)
     FROM forge_notification_items n
     JOIN forge_repos r ON r.id = forge_issues.repo_id
     WHERE n.item_type = 'issue'
       AND n.item_number = forge_issues.number
       AND n.reason != 'author'
       AND (
           n.repo_id = r.id
           OR (n.repo_id IS NULL
               AND n.platform = r.platform
               AND n.platform_host = r.platform_host
               AND n.repo_owner = r.owner_key
               AND n.repo_name = r.name_key)
       ));

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

CREATE TRIGGER forge_notification_items_activity_revision_insert
AFTER INSERT ON forge_notification_items
WHEN NEW.reason != 'author'
  AND NEW.item_type IN ('pr', 'issue')
  AND NEW.item_number IS NOT NULL
BEGIN
    UPDATE forge_merge_requests
    SET activity_event_revision = activity_event_revision + 1
    WHERE NEW.item_type = 'pr'
      AND number = NEW.item_number
      AND repo_id IN (
          SELECT r.id
          FROM forge_repos r
          WHERE r.id = NEW.repo_id
             OR (NEW.repo_id IS NULL
                 AND r.platform = NEW.platform
                 AND r.platform_host = NEW.platform_host
                 AND r.owner_key = NEW.repo_owner
                 AND r.name_key = NEW.repo_name)
      );

    UPDATE forge_issues
    SET activity_event_revision = activity_event_revision + 1
    WHERE NEW.item_type = 'issue'
      AND number = NEW.item_number
      AND repo_id IN (
          SELECT r.id
          FROM forge_repos r
          WHERE r.id = NEW.repo_id
             OR (NEW.repo_id IS NULL
                 AND r.platform = NEW.platform
                 AND r.platform_host = NEW.platform_host
                 AND r.owner_key = NEW.repo_owner
                 AND r.name_key = NEW.repo_name)
      );
END;

CREATE TRIGGER forge_notification_items_activity_revision_update
AFTER UPDATE ON forge_notification_items
WHEN OLD.repo_id IS NOT NEW.repo_id
  OR OLD.platform IS NOT NEW.platform
  OR OLD.platform_host IS NOT NEW.platform_host
  OR OLD.repo_owner IS NOT NEW.repo_owner
  OR OLD.repo_name IS NOT NEW.repo_name
  OR OLD.subject_title IS NOT NEW.subject_title
  OR OLD.web_url IS NOT NEW.web_url
  OR OLD.item_number IS NOT NEW.item_number
  OR OLD.item_type IS NOT NEW.item_type
  OR OLD.item_author IS NOT NEW.item_author
  OR OLD.reason IS NOT NEW.reason
  OR OLD.unread IS NOT NEW.unread
  OR OLD.source_updated_at IS NOT NEW.source_updated_at
BEGIN
    UPDATE forge_merge_requests
    SET activity_event_revision = activity_event_revision + 1
    WHERE (
        OLD.reason != 'author'
        AND OLD.item_type = 'pr'
        AND number = OLD.item_number
        AND repo_id IN (
            SELECT r.id
            FROM forge_repos r
            WHERE r.id = OLD.repo_id
               OR (OLD.repo_id IS NULL
                   AND r.platform = OLD.platform
                   AND r.platform_host = OLD.platform_host
                   AND r.owner_key = OLD.repo_owner
                   AND r.name_key = OLD.repo_name)
        )
    ) OR (
        NEW.reason != 'author'
        AND NEW.item_type = 'pr'
        AND number = NEW.item_number
        AND repo_id IN (
            SELECT r.id
            FROM forge_repos r
            WHERE r.id = NEW.repo_id
               OR (NEW.repo_id IS NULL
                   AND r.platform = NEW.platform
                   AND r.platform_host = NEW.platform_host
                   AND r.owner_key = NEW.repo_owner
                   AND r.name_key = NEW.repo_name)
        )
    );

    UPDATE forge_issues
    SET activity_event_revision = activity_event_revision + 1
    WHERE (
        OLD.reason != 'author'
        AND OLD.item_type = 'issue'
        AND number = OLD.item_number
        AND repo_id IN (
            SELECT r.id
            FROM forge_repos r
            WHERE r.id = OLD.repo_id
               OR (OLD.repo_id IS NULL
                   AND r.platform = OLD.platform
                   AND r.platform_host = OLD.platform_host
                   AND r.owner_key = OLD.repo_owner
                   AND r.name_key = OLD.repo_name)
        )
    ) OR (
        NEW.reason != 'author'
        AND NEW.item_type = 'issue'
        AND number = NEW.item_number
        AND repo_id IN (
            SELECT r.id
            FROM forge_repos r
            WHERE r.id = NEW.repo_id
               OR (NEW.repo_id IS NULL
                   AND r.platform = NEW.platform
                   AND r.platform_host = NEW.platform_host
                   AND r.owner_key = NEW.repo_owner
                   AND r.name_key = NEW.repo_name)
        )
    );
END;

CREATE TRIGGER forge_notification_items_activity_revision_delete
AFTER DELETE ON forge_notification_items
WHEN OLD.reason != 'author'
  AND OLD.item_type IN ('pr', 'issue')
  AND OLD.item_number IS NOT NULL
BEGIN
    UPDATE forge_merge_requests
    SET activity_event_revision = activity_event_revision + 1
    WHERE OLD.item_type = 'pr'
      AND number = OLD.item_number
      AND repo_id IN (
          SELECT r.id
          FROM forge_repos r
          WHERE r.id = OLD.repo_id
             OR (OLD.repo_id IS NULL
                 AND r.platform = OLD.platform
                 AND r.platform_host = OLD.platform_host
                 AND r.owner_key = OLD.repo_owner
                 AND r.name_key = OLD.repo_name)
      );

    UPDATE forge_issues
    SET activity_event_revision = activity_event_revision + 1
    WHERE OLD.item_type = 'issue'
      AND number = OLD.item_number
      AND repo_id IN (
          SELECT r.id
          FROM forge_repos r
          WHERE r.id = OLD.repo_id
             OR (OLD.repo_id IS NULL
                 AND r.platform = OLD.platform
                 AND r.platform_host = OLD.platform_host
                 AND r.owner_key = OLD.repo_owner
                 AND r.name_key = OLD.repo_name)
      );
END;
