ALTER TABLE forge_merge_requests ADD COLUMN author_account_type TEXT;

UPDATE forge_merge_requests AS p SET detail_fetched_at = NULL
WHERE EXISTS (SELECT 1 FROM forge_archive_items a
      JOIN forge_repos r ON r.id = a.repo_id
      JOIN forge_archive_repos ar ON ar.repo_id = a.repo_id AND ar.collection_mode = 'full'
      JOIN forge_archive_dataset_progress progress ON progress.repo_id = a.repo_id AND progress.item_type = a.item_type AND progress.item_number = a.item_number
      WHERE a.repo_id = p.repo_id AND a.item_number = p.number AND a.item_type = 'merge_request'
        AND a.lifecycle_state = 'active' AND r.platform = 'github'
        AND progress.dataset = 'lookup' AND progress.status = 'complete'
        AND (p.state = 'open' OR (p.state IN ('closed','merged')
          AND julianday(replace(CAST(COALESCE(p.merged_at,p.closed_at) AS TEXT), ' +0000 UTC', '')) BETWEEN julianday('now','-30 days') AND julianday('now'))));

UPDATE forge_archive_dataset_progress AS progress
SET scan_generation = scan_generation + 2 - (scan_generation % 2),
    next_cursor = NULL, last_input_cursor = NULL, page_count = 0, observed_count = 0,
    status = 'pending', attempt_count = 0, next_retry_at = NULL,
    last_error_code = NULL, last_error_detail = NULL, started_at = NULL, completed_at = NULL,
    updated_at = strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
WHERE dataset = 'lookup' AND item_type = 'merge_request' AND status = 'complete'
  AND EXISTS (SELECT 1 FROM forge_archive_items a
      JOIN forge_repos r ON r.id = a.repo_id
      JOIN forge_archive_repos ar ON ar.repo_id = a.repo_id AND ar.collection_mode = 'full'
      JOIN forge_merge_requests p ON p.repo_id = a.repo_id AND p.number = a.item_number
      WHERE a.repo_id = progress.repo_id AND a.item_type = progress.item_type
        AND a.item_number = progress.item_number AND a.lifecycle_state = 'active'
        AND r.platform = 'github'
        AND (p.state = 'open' OR (p.state IN ('closed','merged')
          AND julianday(replace(CAST(COALESCE(p.merged_at,p.closed_at) AS TEXT), ' +0000 UTC', '')) BETWEEN julianday('now','-30 days') AND julianday('now'))));
