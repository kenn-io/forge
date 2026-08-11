CREATE TABLE kata_issue_links (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    subject_kind TEXT NOT NULL
        CHECK (subject_kind IN ('pull_request', 'issue', 'workspace')),
    repo_id INTEGER REFERENCES forge_repos(id) ON DELETE CASCADE,
    provider_item_external_id TEXT,
    workspace_id TEXT REFERENCES forge_workspaces(id) ON DELETE CASCADE,
    daemon_id TEXT NOT NULL CHECK (trim(daemon_id) <> ''),
    project_uid TEXT NOT NULL CHECK (trim(project_uid) <> ''),
    issue_uid TEXT NOT NULL CHECK (trim(issue_uid) <> ''),
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CHECK (
        (subject_kind IN ('pull_request', 'issue') AND
         repo_id IS NOT NULL AND
         provider_item_external_id IS NOT NULL AND
         trim(provider_item_external_id) <> '' AND
         workspace_id IS NULL)
        OR
        (subject_kind = 'workspace' AND
         repo_id IS NULL AND
         provider_item_external_id IS NULL AND
         workspace_id IS NOT NULL)
    )
);

CREATE UNIQUE INDEX kata_issue_links_provider_identity
ON kata_issue_links(
    subject_kind, repo_id, provider_item_external_id, daemon_id, issue_uid
)
WHERE subject_kind IN ('pull_request', 'issue');

CREATE UNIQUE INDEX kata_issue_links_workspace_identity
ON kata_issue_links(workspace_id, daemon_id, issue_uid)
WHERE subject_kind = 'workspace';
