CREATE TABLE forge_workspace_launch_specs (
    workspace_id TEXT PRIMARY KEY
        REFERENCES forge_workspaces(id) ON DELETE CASCADE,
    version INTEGER NOT NULL CHECK (version = 1),
    spec_json TEXT NOT NULL CHECK (json_valid(spec_json)),
    source_visible_until DATETIME NOT NULL,
    created_at DATETIME NOT NULL
);

CREATE TABLE forge_spoke_preparation (
    singleton_id INTEGER PRIMARY KEY CHECK (singleton_id = 1),
    phase TEXT NOT NULL DEFAULT 'open'
        CHECK (phase IN ('open', 'quiescing', 'sealed')),
    enrollment_id TEXT NOT NULL DEFAULT '',
    hub_node_id TEXT NOT NULL DEFAULT '',
    local_node_id TEXT NOT NULL DEFAULT '',
    protocol_version INTEGER NOT NULL DEFAULT 0,
    migration_version INTEGER NOT NULL DEFAULT 54
        CHECK (migration_version = 54),
    ack_generation INTEGER NOT NULL DEFAULT 0
        CHECK (ack_generation >= 0),
    drain_ack_generation INTEGER
        CHECK (drain_ack_generation IS NULL OR drain_ack_generation >= 0),
    preparation_digest TEXT NOT NULL DEFAULT '',
    preparation_seal TEXT NOT NULL DEFAULT '',
    started_at DATETIME,
    sealed_at DATETIME,
    updated_at DATETIME NOT NULL DEFAULT (datetime('now'))
);

INSERT INTO forge_spoke_preparation (singleton_id) VALUES (1);

CREATE TABLE forge_spoke_preparation_receipts (
    state_kind TEXT NOT NULL
        CHECK (state_kind IN ('review_draft', 'workflow_state')),
    source_key TEXT NOT NULL,
    content_digest TEXT NOT NULL,
    hub_receipt TEXT NOT NULL,
    imported_at DATETIME NOT NULL,
    PRIMARY KEY (state_kind, source_key)
);

CREATE TABLE forge_spoke_preparation_seals (
    enrollment_id TEXT PRIMARY KEY,
    node_id TEXT NOT NULL,
    hub_node_id TEXT NOT NULL,
    protocol_version INTEGER NOT NULL,
    migration_version INTEGER NOT NULL CHECK (migration_version = 54),
    receipts_digest TEXT NOT NULL,
    drained_ack_generation INTEGER NOT NULL CHECK (drained_ack_generation >= 0),
    preparation_digest TEXT NOT NULL UNIQUE,
    preparation_seal TEXT NOT NULL UNIQUE,
    created_at DATETIME NOT NULL
);

CREATE TABLE forge_notification_ack_admissions (
    notification_id INTEGER PRIMARY KEY
        REFERENCES forge_notification_items(id) ON DELETE CASCADE,
    generation INTEGER NOT NULL CHECK (generation > 0),
    queued_at DATETIME NOT NULL
);

INSERT INTO forge_notification_ack_admissions (
    notification_id, generation, queued_at
)
SELECT
    id,
    ROW_NUMBER() OVER (ORDER BY source_ack_queued_at, id),
    source_ack_queued_at
FROM forge_notification_items
WHERE source_ack_queued_at IS NOT NULL
  AND source_ack_synced_at IS NULL;

UPDATE forge_spoke_preparation
SET ack_generation = (
    SELECT COUNT(*) FROM forge_notification_ack_admissions
)
WHERE singleton_id = 1;

CREATE TRIGGER forge_notification_ack_admission_insert
AFTER INSERT ON forge_notification_items
WHEN NEW.source_ack_queued_at IS NOT NULL
 AND NEW.source_ack_synced_at IS NULL
BEGIN
    UPDATE forge_spoke_preparation
    SET ack_generation = ack_generation + 1,
        updated_at = datetime('now')
    WHERE singleton_id = 1;

    INSERT INTO forge_notification_ack_admissions (
        notification_id, generation, queued_at
    )
    SELECT NEW.id, ack_generation, NEW.source_ack_queued_at
    FROM forge_spoke_preparation
    WHERE singleton_id = 1
    ON CONFLICT(notification_id) DO UPDATE SET
        generation = excluded.generation,
        queued_at = excluded.queued_at;
END;

CREATE TRIGGER forge_notification_ack_admission_update
AFTER UPDATE OF source_ack_queued_at, source_ack_synced_at
ON forge_notification_items
WHEN NEW.source_ack_queued_at IS NOT NULL
 AND NEW.source_ack_synced_at IS NULL
 AND (
     OLD.source_ack_queued_at IS NULL
     OR OLD.source_ack_synced_at IS NOT NULL
     OR OLD.source_ack_queued_at <> NEW.source_ack_queued_at
 )
BEGIN
    UPDATE forge_spoke_preparation
    SET ack_generation = ack_generation + 1,
        updated_at = datetime('now')
    WHERE singleton_id = 1;

    INSERT INTO forge_notification_ack_admissions (
        notification_id, generation, queued_at
    )
    SELECT NEW.id, ack_generation, NEW.source_ack_queued_at
    FROM forge_spoke_preparation
    WHERE singleton_id = 1
    ON CONFLICT(notification_id) DO UPDATE SET
        generation = excluded.generation,
        queued_at = excluded.queued_at;
END;

CREATE TRIGGER forge_notification_ack_admission_clear
AFTER UPDATE OF source_ack_queued_at, source_ack_synced_at
ON forge_notification_items
WHEN NEW.source_ack_queued_at IS NULL
  OR NEW.source_ack_synced_at IS NOT NULL
BEGIN
    DELETE FROM forge_notification_ack_admissions
    WHERE notification_id = NEW.id;
END;

-- Preserve local identity facts, but expire their visibility lease so spoke
-- preparation must refresh provider authority through the hub.
INSERT INTO forge_workspace_launch_specs (
    workspace_id, version, spec_json, source_visible_until, created_at
)
SELECT
    w.id,
    1,
    json_object(
        'version', 1,
        'repository', json_object(
            'provider', r.platform,
            'platform_host', r.platform_host,
            'platform_repo_id', r.platform_repo_id,
            'owner', r.owner,
            'name', r.name,
            'clone_url', r.clone_url,
            'default_branch', r.default_branch
        ),
        'item_type', w.item_type,
        'item_number', w.item_number,
        'item_key', w.item_key,
        'git_head_ref', w.git_head_ref,
		'source_title', mr.title,
		'source_url', mr.url,
        'pull', json_object(
            'head_branch', mr.head_branch,
            'head_repo_kind', CASE
                WHEN w.mr_head_repo IS NULL THEN 'same_repo'
                WHEN trim(w.mr_head_repo) = '' THEN 'unknown'
                ELSE 'fork'
            END,
            'head_repo_clone_url', COALESCE(w.mr_head_repo, ''),
            'snapshot_revision', mr.snapshot_revision
        ),
        'source_visible', CASE WHEN EXISTS (
            SELECT 1
            FROM forge_archive_items ai
            WHERE ai.repo_id = r.id
              AND ai.item_type = 'merge_request'
              AND ai.item_number = w.item_number
              AND ai.lifecycle_state = 'removed_upstream'
        ) THEN json('false') ELSE json('true') END,
        'source_visible_until', strftime('%Y-%m-%dT%H:%M:%SZ', 'now', '-1 second'),
        'issued_at', strftime('%Y-%m-%dT%H:%M:%SZ', 'now', '-15 minutes', '-1 second')
    ),
    datetime('now', '-1 second'),
    datetime('now', '-15 minutes', '-1 second')
FROM forge_workspaces w
JOIN forge_repo_routes route
  ON route.platform = w.platform
 AND route.platform_host = w.platform_host
 AND route.repo_path_key = w.repo_path_key
 AND NOT EXISTS (
     SELECT 1
     FROM forge_repo_routes other
     WHERE other.platform = route.platform
       AND other.platform_host = route.platform_host
       AND other.repo_path_key = route.repo_path_key
       AND other.repo_id <> route.repo_id
 )
JOIN forge_repos r
  ON r.id = route.repo_id
 AND r.lifecycle_state = 'active'
JOIN forge_merge_requests mr
  ON mr.repo_id = r.id
 AND mr.number = w.item_number
WHERE w.item_type = 'pull_request'
  AND w.item_number > 0
  AND trim(w.item_key) <> ''
  AND trim(w.git_head_ref) <> ''
  AND trim(r.platform_repo_id) <> ''
  AND trim(r.clone_url) <> ''
  AND trim(r.default_branch) <> ''
  AND trim(mr.head_branch) <> ''
  AND mr.snapshot_revision > 0;

INSERT INTO forge_workspace_launch_specs (
    workspace_id, version, spec_json, source_visible_until, created_at
)
SELECT
    w.id,
    1,
    json_object(
        'version', 1,
        'repository', json_object(
            'provider', r.platform,
            'platform_host', r.platform_host,
            'platform_repo_id', r.platform_repo_id,
            'owner', r.owner,
            'name', r.name,
            'clone_url', r.clone_url,
            'default_branch', r.default_branch
        ),
        'item_type', w.item_type,
        'item_number', w.item_number,
        'item_key', w.item_key,
        'git_head_ref', w.git_head_ref,
		'source_title', issue.title,
		'source_url', issue.url,
        'source_visible', CASE WHEN EXISTS (
            SELECT 1
            FROM forge_archive_items ai
            WHERE ai.repo_id = r.id
              AND ai.item_type = 'issue'
              AND ai.item_number = w.item_number
              AND ai.lifecycle_state = 'removed_upstream'
        ) THEN json('false') ELSE json('true') END,
        'source_visible_until', strftime('%Y-%m-%dT%H:%M:%SZ', 'now', '-1 second'),
        'issued_at', strftime('%Y-%m-%dT%H:%M:%SZ', 'now', '-15 minutes', '-1 second')
    ),
    datetime('now', '-1 second'),
    datetime('now', '-15 minutes', '-1 second')
FROM forge_workspaces w
JOIN forge_repo_routes route
  ON route.platform = w.platform
 AND route.platform_host = w.platform_host
 AND route.repo_path_key = w.repo_path_key
 AND NOT EXISTS (
     SELECT 1
     FROM forge_repo_routes other
     WHERE other.platform = route.platform
       AND other.platform_host = route.platform_host
       AND other.repo_path_key = route.repo_path_key
       AND other.repo_id <> route.repo_id
 )
JOIN forge_repos r
  ON r.id = route.repo_id
 AND r.lifecycle_state = 'active'
JOIN forge_issues issue
  ON issue.repo_id = r.id
 AND issue.number = w.item_number
WHERE w.item_type = 'issue'
  AND w.item_number > 0
  AND trim(w.item_key) <> ''
  AND trim(w.git_head_ref) <> ''
  AND trim(r.platform_repo_id) <> ''
  AND trim(r.clone_url) <> ''
  AND trim(r.default_branch) <> '';
