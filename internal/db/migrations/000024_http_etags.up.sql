CREATE TABLE IF NOT EXISTS middleman_http_etags (
    platform TEXT NOT NULL,
    platform_host TEXT NOT NULL,
    owner_key TEXT NOT NULL,
    name_key TEXT NOT NULL,
    resource_type TEXT NOT NULL,
    resource_number INTEGER NOT NULL,
    etag TEXT NOT NULL,
    fetched_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (
        platform,
        platform_host,
        owner_key,
        name_key,
        resource_type,
        resource_number
    )
);
