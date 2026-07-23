CREATE TABLE middleman_rate_limits_v40 (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    platform       TEXT NOT NULL DEFAULT 'github',
    platform_host  TEXT NOT NULL,
    rate_principal TEXT NOT NULL,
    api_type       TEXT NOT NULL DEFAULT 'rest',
    requests_hour  INTEGER NOT NULL DEFAULT 0,
    hour_start     DATETIME NOT NULL,
    rate_remaining INTEGER NOT NULL DEFAULT -1,
    rate_limit     INTEGER NOT NULL DEFAULT -1,
    rate_reset_at  DATETIME,
    updated_at     DATETIME NOT NULL DEFAULT (datetime('now')),
    UNIQUE(platform, platform_host, rate_principal, api_type)
);

-- Non-GitHub providers retain their existing host-scoped accounting under the
-- explicit compatibility principal "host". Existing GitHub rows cannot be
-- attributed safely to a user or installation and are intentionally dropped;
-- the next authenticated response repopulates identity-scoped state.
INSERT INTO middleman_rate_limits_v40
    (id, platform, platform_host, rate_principal, api_type, requests_hour,
     hour_start, rate_remaining, rate_limit, rate_reset_at, updated_at)
SELECT id, platform, platform_host, 'host', api_type, requests_hour,
       hour_start, rate_remaining, rate_limit, rate_reset_at, updated_at
FROM middleman_rate_limits
WHERE platform != 'github';

DROP TABLE middleman_rate_limits;
ALTER TABLE middleman_rate_limits_v40 RENAME TO middleman_rate_limits;

-- Notification sync watermarks move from host-wide to repository identity so
-- one unavailable credential route or exhausted PAT cannot block watermark
-- advancement for every healthy repository on the host. Existing host-wide
-- rows cannot be attributed to repositories and are intentionally dropped;
-- each repository's next notification pass runs as a full sync. The unused
-- sync_cursor and tracked_repos_key columns are retired: per-repository rows
-- make the tracked-set key meaningless and no code ever stored a cursor.
CREATE TABLE middleman_notification_sync_watermarks_v40 (
    platform TEXT NOT NULL,
    platform_host TEXT NOT NULL,
    repo_owner TEXT NOT NULL,
    repo_name TEXT NOT NULL,
    last_successful_sync_at TEXT NOT NULL,
    last_full_sync_at TEXT,
    PRIMARY KEY (platform, platform_host, repo_owner, repo_name)
);

DROP TABLE middleman_notification_sync_watermarks;
ALTER TABLE middleman_notification_sync_watermarks_v40
    RENAME TO middleman_notification_sync_watermarks;
