CREATE TABLE forge_relay_cursors (
    relay_url TEXT PRIMARY KEY,
    cursor TEXT NOT NULL
);

CREATE TABLE forge_relay_pending (
    relay_url TEXT NOT NULL REFERENCES forge_relay_cursors(relay_url) ON DELETE CASCADE,
    repository_id TEXT NOT NULL CHECK (length(repository_id) BETWEEN 1 AND 256),
    target TEXT NOT NULL CHECK (target IN ('pull_request', 'pull_request_checks', 'issue', 'repository_refs', 'repository')),
    number INTEGER NOT NULL,
    PRIMARY KEY (relay_url, repository_id, target, number),
    CHECK ((target IN ('pull_request', 'pull_request_checks', 'issue') AND number > 0)
        OR (target IN ('repository_refs', 'repository') AND number = 0))
);
