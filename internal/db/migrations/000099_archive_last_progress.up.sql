ALTER TABLE forge_archive_repos ADD COLUMN last_progress_at DATETIME;

-- Completion times are stored in UTC. Parse the whole second and
-- compare the fractional part separately to preserve nanosecond ordering.
WITH progress_candidates AS (
    SELECT repo_id, initial_completed_at AS progress_at
    FROM forge_archive_repos
    WHERE initial_completed_at IS NOT NULL
    UNION ALL
    SELECT p.repo_id, p.completed_at AS progress_at
    FROM forge_archive_dataset_progress p
    JOIN forge_archive_items i ON i.repo_id = p.repo_id
        AND i.item_type = p.item_type AND i.item_number = p.item_number
    WHERE p.dataset = 'lookup' AND p.completed_at IS NOT NULL
      AND (p.status = 'complete' OR (p.status = 'terminal' AND i.lifecycle_state = 'inaccessible'))
    UNION ALL
    SELECT repo_id, updated_at AS progress_at
    FROM forge_archive_repo_scans
    WHERE scan IN ('issue_inventory', 'merge_request_inventory')
      AND page_count > 0 AND status IN ('running', 'complete')
)
UPDATE forge_archive_repos
SET last_progress_at = (
    SELECT replace(substr(p.progress_at, 1, 19), 'T', ' ') ||
           substr(rtrim(printf('%.9f', CAST('0' || substr(p.progress_at, 20) AS REAL)), '0.'), 2) ||
           ' +0000 UTC'
    FROM progress_candidates p
    WHERE p.repo_id = forge_archive_repos.repo_id
      AND datetime(substr(p.progress_at, 1, 19)) IS NOT NULL
    ORDER BY datetime(substr(p.progress_at, 1, 19)) DESC,
             CAST('0' || substr(p.progress_at, 20) AS REAL) DESC
    LIMIT 1
);
