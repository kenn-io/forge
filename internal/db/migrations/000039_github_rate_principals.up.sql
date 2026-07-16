CREATE TABLE middleman_rate_limits_v39 (
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
INSERT INTO middleman_rate_limits_v39
    (id, platform, platform_host, rate_principal, api_type, requests_hour,
     hour_start, rate_remaining, rate_limit, rate_reset_at, updated_at)
SELECT id, platform, platform_host, 'host', api_type, requests_hour,
       hour_start, rate_remaining, rate_limit, rate_reset_at, updated_at
FROM middleman_rate_limits
WHERE platform != 'github';

DROP TABLE middleman_rate_limits;
ALTER TABLE middleman_rate_limits_v39 RENAME TO middleman_rate_limits;
