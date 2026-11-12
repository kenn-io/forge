-- forge_head_repo_kind is platform.ClassifyHeadRepo, registered by internal/db; storing its
-- result lets the From fork filter run in SQL before list limits.
ALTER TABLE forge_merge_requests ADD COLUMN head_repo_kind TEXT NOT NULL DEFAULT 'unknown';

UPDATE forge_merge_requests
SET head_repo_kind = (
    SELECT forge_head_repo_kind(r.platform, r.platform_host, r.owner, r.name, forge_merge_requests.head_repo_clone_url)
    FROM forge_repos r
    WHERE r.id = forge_merge_requests.repo_id
);
