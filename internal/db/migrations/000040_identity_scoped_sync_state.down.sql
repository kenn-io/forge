CREATE TABLE middleman_rate_limits_v39 (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    platform       TEXT NOT NULL DEFAULT 'github',
    platform_host  TEXT NOT NULL,
    api_type       TEXT NOT NULL DEFAULT 'rest',
    requests_hour  INTEGER NOT NULL DEFAULT 0,
    hour_start     DATETIME NOT NULL,
    rate_remaining INTEGER NOT NULL DEFAULT -1,
    rate_limit     INTEGER NOT NULL DEFAULT -1,
    rate_reset_at  DATETIME,
    updated_at     DATETIME NOT NULL DEFAULT (datetime('now')),
    UNIQUE(platform, platform_host, api_type)
);

-- Downgrade is lossy: multiple identity rows collapse to the most recently
-- updated row for the old provider/host/API key.
INSERT INTO middleman_rate_limits_v39
    (platform, platform_host, api_type, requests_hour, hour_start,
     rate_remaining, rate_limit, rate_reset_at, updated_at)
SELECT r.platform, r.platform_host, r.api_type, r.requests_hour, r.hour_start,
       r.rate_remaining, r.rate_limit, r.rate_reset_at, r.updated_at
FROM middleman_rate_limits r
WHERE r.id = (
    SELECT r2.id
    FROM middleman_rate_limits r2
    WHERE r2.platform = r.platform
      AND r2.platform_host = r.platform_host
      AND r2.api_type = r.api_type
    ORDER BY r2.updated_at DESC, r2.id DESC
    LIMIT 1
);

DROP TABLE middleman_rate_limits;
ALTER TABLE middleman_rate_limits_v39 RENAME TO middleman_rate_limits;

-- Downgrade is lossy: per-repository watermarks cannot be collapsed into one
-- honest host-wide row, so the table returns empty and the next notification
-- pass runs as a full sync.
CREATE TABLE middleman_notification_sync_watermarks_v39 (
    platform TEXT NOT NULL,
    platform_host TEXT NOT NULL,
    last_successful_sync_at TEXT NOT NULL,
    last_full_sync_at TEXT,
    sync_cursor TEXT NOT NULL DEFAULT '',
    tracked_repos_key TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (platform, platform_host)
);

DROP TABLE middleman_notification_sync_watermarks;
ALTER TABLE middleman_notification_sync_watermarks_v39
    RENAME TO middleman_notification_sync_watermarks;
