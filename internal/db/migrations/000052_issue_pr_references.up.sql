CREATE TABLE forge_issue_pr_references (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    issue_id INTEGER NOT NULL REFERENCES forge_issues(id) ON DELETE CASCADE,
    source_provider TEXT NOT NULL CHECK (trim(source_provider) <> ''),
    source_platform_host TEXT NOT NULL CHECK (trim(source_platform_host) <> ''),
    source_owner TEXT NOT NULL CHECK (trim(source_owner) <> ''),
    source_repo TEXT NOT NULL CHECK (trim(source_repo) <> ''),
    source_number INTEGER NOT NULL CHECK (source_number > 0),
    source_url TEXT NOT NULL CHECK (trim(source_url) <> ''),
    observed_event_key TEXT NOT NULL CHECK (trim(observed_event_key) <> ''),
    observed_at DATETIME NOT NULL,
    UNIQUE (
        issue_id,
        source_provider,
        source_platform_host,
        source_owner,
        source_repo,
        source_number
    )
);

CREATE INDEX idx_forge_issue_pr_references_issue
    ON forge_issue_pr_references(issue_id);

INSERT INTO forge_issue_pr_references (
    issue_id,
    source_provider,
    source_platform_host,
    source_owner,
    source_repo,
    source_number,
    source_url,
    observed_event_key,
    observed_at
)
SELECT
    e.issue_id,
    r.platform,
    r.platform_host,
    json_extract(e.metadata_json, '$.source_owner'),
    json_extract(e.metadata_json, '$.source_repo'),
    json_extract(e.metadata_json, '$.source_number'),
    json_extract(e.metadata_json, '$.source_url'),
    e.dedupe_key,
    e.created_at
FROM (
    SELECT
        issue_id,
        dedupe_key,
        created_at,
        CASE
            WHEN json_valid(metadata_json) THEN metadata_json
            ELSE '{}'
        END AS metadata_json
    FROM forge_issue_events
    WHERE event_type = 'cross_referenced'
) e
JOIN forge_issues i ON i.id = e.issue_id
JOIN forge_repos r ON r.id = i.repo_id
WHERE json_extract(e.metadata_json, '$.source_type') = 'PullRequest'
  AND trim(COALESCE(json_extract(e.metadata_json, '$.source_owner'), '')) <> ''
  AND trim(COALESCE(json_extract(e.metadata_json, '$.source_repo'), '')) <> ''
  AND COALESCE(json_extract(e.metadata_json, '$.source_number'), 0) > 0
  AND trim(COALESCE(json_extract(e.metadata_json, '$.source_url'), '')) <> ''
ON CONFLICT (
    issue_id,
    source_provider,
    source_platform_host,
    source_owner,
    source_repo,
    source_number
) DO UPDATE SET
    source_url = excluded.source_url,
    observed_event_key = excluded.observed_event_key,
    observed_at = MAX(observed_at, excluded.observed_at);
