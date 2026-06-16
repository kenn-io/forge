CREATE TABLE IF NOT EXISTS middleman_notification_items (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    platform TEXT NOT NULL,
    platform_host TEXT NOT NULL,
    platform_notification_id TEXT NOT NULL,
    repo_id INTEGER REFERENCES middleman_repos(id) ON DELETE SET NULL,
    repo_owner TEXT NOT NULL,
    repo_name TEXT NOT NULL,
    subject_type TEXT NOT NULL,
    subject_title TEXT NOT NULL,
    subject_url TEXT NOT NULL DEFAULT '',
    subject_latest_comment_url TEXT NOT NULL DEFAULT '',
    web_url TEXT NOT NULL DEFAULT '',
    item_number INTEGER,
    item_type TEXT NOT NULL DEFAULT 'other',
    item_author TEXT NOT NULL DEFAULT '',
    reason TEXT NOT NULL,
    unread INTEGER NOT NULL DEFAULT 0,
    participating INTEGER NOT NULL DEFAULT 0,
    source_updated_at TEXT NOT NULL,
    source_last_acknowledged_at TEXT,
    synced_at TEXT NOT NULL,
    done_at TEXT,
    done_reason TEXT NOT NULL DEFAULT '',
    source_ack_queued_at TEXT,
    source_ack_synced_at TEXT,
    source_ack_generation_at TEXT,
    source_ack_error TEXT NOT NULL DEFAULT '',
    source_ack_attempts INTEGER NOT NULL DEFAULT 0,
    source_ack_last_attempt_at TEXT,
    source_ack_next_attempt_at TEXT,
    UNIQUE(platform, platform_host, platform_notification_id)
);

CREATE INDEX IF NOT EXISTS idx_middleman_notification_items_inbox
    ON middleman_notification_items(done_at, unread, source_updated_at DESC);
CREATE INDEX IF NOT EXISTS idx_middleman_notification_items_repo
    ON middleman_notification_items(platform, platform_host, repo_owner, repo_name, source_updated_at DESC);
CREATE INDEX IF NOT EXISTS idx_middleman_notification_items_reason
    ON middleman_notification_items(reason, source_updated_at DESC);
CREATE INDEX IF NOT EXISTS idx_middleman_notification_items_item
    ON middleman_notification_items(platform, platform_host, repo_owner, repo_name, item_type, item_number);
CREATE INDEX IF NOT EXISTS idx_middleman_notification_items_ack_queue
    ON middleman_notification_items(platform, platform_host, source_ack_queued_at, source_ack_next_attempt_at, source_ack_synced_at);

CREATE TABLE IF NOT EXISTS middleman_notification_sync_watermarks (
    platform TEXT NOT NULL,
    platform_host TEXT NOT NULL,
    last_successful_sync_at TEXT NOT NULL,
    last_full_sync_at TEXT,
    sync_cursor TEXT NOT NULL DEFAULT '',
    tracked_repos_key TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (platform, platform_host)
);
