package db

import "context"

// HasStoredWork reports whether an active repository has a visible PR or issue.
func (d *DB) HasStoredWork(ctx context.Context) (bool, error) {
	var found bool
	err := d.roQueryRowContext(ctx, `SELECT EXISTS (
		SELECT 1 FROM forge_merge_requests p JOIN forge_repos r ON r.id = p.repo_id
		WHERE r.lifecycle_state = 'active' AND NOT EXISTS (
			SELECT 1 FROM forge_archive_items a WHERE a.repo_id = p.repo_id
			AND a.item_type = 'merge_request' AND a.item_number = p.number
			AND a.lifecycle_state = 'removed_upstream')
		UNION ALL
		SELECT 1 FROM forge_issues i JOIN forge_repos r ON r.id = i.repo_id
		WHERE r.lifecycle_state = 'active' AND NOT EXISTS (
			SELECT 1 FROM forge_archive_items a WHERE a.repo_id = i.repo_id
			AND a.item_type = 'issue' AND a.item_number = i.number
			AND a.lifecycle_state = 'removed_upstream')
	)`).Scan(&found)
	return found, err
}
