ALTER TABLE forge_archive_repos ADD COLUMN last_progress_at DATETIME;

-- Lookup completion times are stored in UTC. Parse the whole second and
-- compare the fractional part separately to preserve nanosecond ordering.
UPDATE forge_archive_repos
SET last_progress_at = (
    SELECT replace(substr(p.completed_at, 1, 19), 'T', ' ') ||
           substr(rtrim(printf('%.9f', CAST('0' || substr(p.completed_at, 20) AS REAL)), '0.'), 2) ||
           ' +0000 UTC'
    FROM forge_archive_dataset_progress p
    WHERE p.repo_id = forge_archive_repos.repo_id
      AND p.dataset = 'lookup' AND p.status = 'complete'
      AND p.completed_at IS NOT NULL
      AND datetime(substr(p.completed_at, 1, 19)) IS NOT NULL
    ORDER BY datetime(substr(p.completed_at, 1, 19)) DESC,
             CAST('0' || substr(p.completed_at, 20) AS REAL) DESC
    LIMIT 1
);
