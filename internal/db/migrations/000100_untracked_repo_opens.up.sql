CREATE TABLE untracked_repo_opens (
    provider TEXT NOT NULL,
    host TEXT NOT NULL,
    owner TEXT NOT NULL COLLATE NOCASE,
    name TEXT NOT NULL COLLATE NOCASE,
    open_count INTEGER NOT NULL,
    last_opened_at TEXT NOT NULL,
    PRIMARY KEY (provider, host, owner, name)
);
