-- Raise-only upper bound on every opening, merge, close, event, and visible
-- notification time. Exact Activity recency filters trim stale high bounds.

ALTER TABLE forge_merge_requests ADD COLUMN activity_signal_at DATETIME;

UPDATE forge_merge_requests SET activity_signal_at = MAX(forge_merge_requests.created_at, COALESCE(forge_merge_requests.merged_at, forge_merge_requests.created_at), COALESCE(forge_merge_requests.closed_at, forge_merge_requests.created_at), COALESCE((SELECT MAX(e.created_at) FROM forge_mr_events e WHERE e.merge_request_id = forge_merge_requests.id), forge_merge_requests.created_at), COALESCE((SELECT MAX(n.source_updated_at) FROM forge_notification_items n
        JOIN forge_repos r ON r.id = forge_merge_requests.repo_id
        WHERE n.item_type = 'pr' AND n.item_number = forge_merge_requests.number
          AND n.reason != 'author' AND (n.repo_id = r.id OR (n.repo_id IS NULL
              AND n.platform = r.platform AND n.platform_host = r.platform_host
              AND n.repo_owner = r.owner_key AND n.repo_name = r.name_key))), forge_merge_requests.created_at));

CREATE INDEX idx_mr_repo_signal ON forge_merge_requests(repo_id, activity_signal_at) WHERE activity_signal_at IS NOT NULL;

CREATE TRIGGER forge_merge_requests_signal_insert
AFTER INSERT ON forge_merge_requests
BEGIN
    UPDATE forge_merge_requests SET activity_signal_at = MAX(forge_merge_requests.created_at, COALESCE(forge_merge_requests.merged_at, forge_merge_requests.created_at), COALESCE(forge_merge_requests.closed_at, forge_merge_requests.created_at), COALESCE((SELECT MAX(n.source_updated_at) FROM forge_notification_items n
        JOIN forge_repos r ON r.id = forge_merge_requests.repo_id
        WHERE n.item_type = 'pr' AND n.item_number = forge_merge_requests.number
          AND n.reason != 'author' AND (n.repo_id = r.id OR (n.repo_id IS NULL
              AND n.platform = r.platform AND n.platform_host = r.platform_host
              AND n.repo_owner = r.owner_key AND n.repo_name = r.name_key))), forge_merge_requests.created_at)) WHERE id = NEW.id;
END;

CREATE TRIGGER forge_merge_requests_signal_update
AFTER UPDATE OF repo_id, created_at, merged_at, closed_at ON forge_merge_requests
WHEN OLD.repo_id IS NOT NEW.repo_id OR OLD.created_at IS NOT NEW.created_at OR OLD.merged_at IS NOT NEW.merged_at OR OLD.closed_at IS NOT NEW.closed_at
BEGIN
    UPDATE forge_merge_requests SET activity_signal_at = MAX(activity_signal_at, NEW.created_at, COALESCE(NEW.merged_at, NEW.created_at), COALESCE(NEW.closed_at, NEW.created_at), COALESCE((SELECT MAX(n.source_updated_at) FROM forge_notification_items n
        JOIN forge_repos r ON r.id = NEW.repo_id
        WHERE n.item_type = 'pr' AND n.item_number = NEW.number
          AND n.reason != 'author' AND (n.repo_id = r.id OR (n.repo_id IS NULL
              AND n.platform = r.platform AND n.platform_host = r.platform_host
              AND n.repo_owner = r.owner_key AND n.repo_name = r.name_key))), NEW.created_at)) WHERE id = NEW.id;
END;

CREATE TRIGGER forge_mr_events_signal_insert
AFTER INSERT ON forge_mr_events
BEGIN
    UPDATE forge_merge_requests SET activity_signal_at = NEW.created_at WHERE id = NEW.merge_request_id AND activity_signal_at < NEW.created_at;
END;

CREATE TRIGGER forge_mr_events_signal_update
AFTER UPDATE OF created_at, merge_request_id ON forge_mr_events
BEGIN
    UPDATE forge_merge_requests SET activity_signal_at = NEW.created_at WHERE id = NEW.merge_request_id AND activity_signal_at < NEW.created_at;
END;

ALTER TABLE forge_issues ADD COLUMN activity_signal_at DATETIME;

UPDATE forge_issues SET activity_signal_at = MAX(forge_issues.created_at, COALESCE(forge_issues.closed_at, forge_issues.created_at), COALESCE((SELECT MAX(e.created_at) FROM forge_issue_events e WHERE e.issue_id = forge_issues.id), forge_issues.created_at), COALESCE((SELECT MAX(n.source_updated_at) FROM forge_notification_items n
        JOIN forge_repos r ON r.id = forge_issues.repo_id
        WHERE n.item_type = 'issue' AND n.item_number = forge_issues.number
          AND n.reason != 'author' AND (n.repo_id = r.id OR (n.repo_id IS NULL
              AND n.platform = r.platform AND n.platform_host = r.platform_host
              AND n.repo_owner = r.owner_key AND n.repo_name = r.name_key))), forge_issues.created_at));

CREATE INDEX idx_issues_repo_signal ON forge_issues(repo_id, activity_signal_at) WHERE activity_signal_at IS NOT NULL;

CREATE TRIGGER forge_issues_signal_insert
AFTER INSERT ON forge_issues
BEGIN
    UPDATE forge_issues SET activity_signal_at = MAX(forge_issues.created_at, COALESCE(forge_issues.closed_at, forge_issues.created_at), COALESCE((SELECT MAX(n.source_updated_at) FROM forge_notification_items n
        JOIN forge_repos r ON r.id = forge_issues.repo_id
        WHERE n.item_type = 'issue' AND n.item_number = forge_issues.number
          AND n.reason != 'author' AND (n.repo_id = r.id OR (n.repo_id IS NULL
              AND n.platform = r.platform AND n.platform_host = r.platform_host
              AND n.repo_owner = r.owner_key AND n.repo_name = r.name_key))), forge_issues.created_at)) WHERE id = NEW.id;
END;

CREATE TRIGGER forge_issues_signal_update
AFTER UPDATE OF repo_id, created_at, closed_at ON forge_issues
WHEN OLD.repo_id IS NOT NEW.repo_id OR OLD.created_at IS NOT NEW.created_at OR OLD.closed_at IS NOT NEW.closed_at
BEGIN
    UPDATE forge_issues SET activity_signal_at = MAX(activity_signal_at, NEW.created_at, COALESCE(NEW.closed_at, NEW.created_at), COALESCE((SELECT MAX(n.source_updated_at) FROM forge_notification_items n
        JOIN forge_repos r ON r.id = NEW.repo_id
        WHERE n.item_type = 'issue' AND n.item_number = NEW.number
          AND n.reason != 'author' AND (n.repo_id = r.id OR (n.repo_id IS NULL
              AND n.platform = r.platform AND n.platform_host = r.platform_host
              AND n.repo_owner = r.owner_key AND n.repo_name = r.name_key))), NEW.created_at)) WHERE id = NEW.id;
END;

CREATE TRIGGER forge_issue_events_signal_insert
AFTER INSERT ON forge_issue_events
BEGIN
    UPDATE forge_issues SET activity_signal_at = NEW.created_at WHERE id = NEW.issue_id AND activity_signal_at < NEW.created_at;
END;

CREATE TRIGGER forge_issue_events_signal_update
AFTER UPDATE OF created_at, issue_id ON forge_issue_events
BEGIN
    UPDATE forge_issues SET activity_signal_at = NEW.created_at WHERE id = NEW.issue_id AND activity_signal_at < NEW.created_at;
END;

CREATE TRIGGER forge_notification_items_signal_insert
AFTER INSERT ON forge_notification_items
WHEN NEW.reason != 'author'
BEGIN
    UPDATE forge_merge_requests SET activity_signal_at = NEW.source_updated_at
    WHERE NEW.item_type = 'pr' AND number = NEW.item_number
      AND activity_signal_at < NEW.source_updated_at
      AND repo_id IN (SELECT r.id FROM forge_repos r
          WHERE r.id = NEW.repo_id OR (NEW.repo_id IS NULL
              AND r.platform = NEW.platform AND r.platform_host = NEW.platform_host
              AND r.owner_key = NEW.repo_owner AND r.name_key = NEW.repo_name));
    UPDATE forge_issues SET activity_signal_at = NEW.source_updated_at
    WHERE NEW.item_type = 'issue' AND number = NEW.item_number
      AND activity_signal_at < NEW.source_updated_at
      AND repo_id IN (SELECT r.id FROM forge_repos r
          WHERE r.id = NEW.repo_id OR (NEW.repo_id IS NULL
              AND r.platform = NEW.platform AND r.platform_host = NEW.platform_host
              AND r.owner_key = NEW.repo_owner AND r.name_key = NEW.repo_name));
END;

CREATE TRIGGER forge_notification_items_signal_update
AFTER UPDATE OF repo_id, platform, platform_host, repo_owner, repo_name, subject_title, web_url, item_number, item_type, item_author, reason, unread, source_updated_at ON forge_notification_items
WHEN NEW.reason != 'author'
BEGIN
    UPDATE forge_merge_requests SET activity_signal_at = NEW.source_updated_at
    WHERE NEW.item_type = 'pr' AND number = NEW.item_number
      AND activity_signal_at < NEW.source_updated_at
      AND repo_id IN (SELECT r.id FROM forge_repos r
          WHERE r.id = NEW.repo_id OR (NEW.repo_id IS NULL
              AND r.platform = NEW.platform AND r.platform_host = NEW.platform_host
              AND r.owner_key = NEW.repo_owner AND r.name_key = NEW.repo_name));
    UPDATE forge_issues SET activity_signal_at = NEW.source_updated_at
    WHERE NEW.item_type = 'issue' AND number = NEW.item_number
      AND activity_signal_at < NEW.source_updated_at
      AND repo_id IN (SELECT r.id FROM forge_repos r
          WHERE r.id = NEW.repo_id OR (NEW.repo_id IS NULL
              AND r.platform = NEW.platform AND r.platform_host = NEW.platform_host
              AND r.owner_key = NEW.repo_owner AND r.name_key = NEW.repo_name));
END;

CREATE TRIGGER forge_repos_signal_update
AFTER UPDATE OF platform, platform_host, owner_key, name_key ON forge_repos
WHEN OLD.platform IS NOT NEW.platform OR OLD.platform_host IS NOT NEW.platform_host
  OR OLD.owner_key IS NOT NEW.owner_key OR OLD.name_key IS NOT NEW.name_key
BEGIN
    UPDATE forge_merge_requests SET activity_signal_at = (SELECT MAX(n.source_updated_at) FROM forge_notification_items n
        WHERE n.repo_id IS NULL AND n.item_type = 'pr'
          AND n.item_number = forge_merge_requests.number AND n.reason != 'author'
          AND n.platform = NEW.platform AND n.platform_host = NEW.platform_host
          AND n.repo_owner = NEW.owner_key AND n.repo_name = NEW.name_key)
    WHERE repo_id = NEW.id AND activity_signal_at < (SELECT MAX(n.source_updated_at) FROM forge_notification_items n
        WHERE n.repo_id IS NULL AND n.item_type = 'pr'
          AND n.item_number = forge_merge_requests.number AND n.reason != 'author'
          AND n.platform = NEW.platform AND n.platform_host = NEW.platform_host
          AND n.repo_owner = NEW.owner_key AND n.repo_name = NEW.name_key);
    UPDATE forge_issues SET activity_signal_at = (SELECT MAX(n.source_updated_at) FROM forge_notification_items n
        WHERE n.repo_id IS NULL AND n.item_type = 'issue'
          AND n.item_number = forge_issues.number AND n.reason != 'author'
          AND n.platform = NEW.platform AND n.platform_host = NEW.platform_host
          AND n.repo_owner = NEW.owner_key AND n.repo_name = NEW.name_key)
    WHERE repo_id = NEW.id AND activity_signal_at < (SELECT MAX(n.source_updated_at) FROM forge_notification_items n
        WHERE n.repo_id IS NULL AND n.item_type = 'issue'
          AND n.item_number = forge_issues.number AND n.reason != 'author'
          AND n.platform = NEW.platform AND n.platform_host = NEW.platform_host
          AND n.repo_owner = NEW.owner_key AND n.repo_name = NEW.name_key);
END;
