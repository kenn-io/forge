ALTER TABLE forge_merge_requests ADD COLUMN author_account_type TEXT;
ALTER TABLE forge_mr_events ADD COLUMN source_repo_id INTEGER REFERENCES forge_repos(id) ON DELETE SET NULL;

UPDATE forge_mr_events AS e
SET source_repo_id = CASE
    WHEN json_extract(e.metadata_json, '$.is_cross_repository') = 0 THEN e.repo_id
    ELSE (
        SELECT MIN(source.id)
        FROM forge_repos target
        JOIN forge_repos source ON source.platform = target.platform
            AND source.platform_host = target.platform_host
            AND source.repo_path_key = lower(json_extract(e.metadata_json, '$.source_owner') || '/' || json_extract(e.metadata_json, '$.source_repo'))
        WHERE target.id = e.repo_id
          AND (SELECT COUNT(DISTINCT candidate.id) FROM forge_repos candidate
               WHERE candidate.platform=target.platform AND candidate.platform_host=target.platform_host
                 AND candidate.repo_path_key=source.repo_path_key) = 1
          AND EXISTS (
              SELECT 1 FROM forge_merge_requests p
              WHERE p.repo_id = source.id
                AND p.number = json_extract(e.metadata_json, '$.source_number')
                AND p.url = json_extract(e.metadata_json, '$.source_url')
                AND p.created_at <= e.created_at AND e.created_at <= p.updated_at
          )
    ) END
WHERE e.event_type = 'cross_referenced' AND json_valid(e.metadata_json)
  AND json_extract(e.metadata_json, '$.source_type') = 'PullRequest'
  AND json_extract(e.metadata_json, '$.source_number') > 0
  AND trim(COALESCE(json_extract(e.metadata_json, '$.source_url'), '')) != ''
  AND trim(COALESCE(json_extract(e.metadata_json, '$.source_owner'), '')) != ''
  AND trim(COALESCE(json_extract(e.metadata_json, '$.source_repo'), '')) != '';
