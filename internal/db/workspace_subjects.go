package db

import (
	"context"
	"encoding/json"
	"fmt"
)

func (d *DB) ListWorkspaceSubjectMetadata(
	ctx context.Context,
	keys []WorkspaceSubjectKey,
) (map[WorkspaceSubjectKey]WorkspaceSubjectMetadata, error) {
	out := make(map[WorkspaceSubjectKey]WorkspaceSubjectMetadata)
	if len(keys) == 0 {
		return out, nil
	}
	unique := make([]WorkspaceSubjectKey, 0, len(keys))
	seen := make(map[WorkspaceSubjectKey]struct{}, len(keys))
	for _, key := range keys {
		if key.RepoID == 0 || key.ItemNumber <= 0 {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		unique = append(unique, key)
	}
	if len(unique) == 0 {
		return out, nil
	}
	requestedJSON, err := json.Marshal(unique)
	if err != nil {
		return nil, fmt.Errorf("encode workspace subject metadata request: %w", err)
	}
	query := `WITH requested(repo_id, item_type, item_number) AS (
			SELECT CAST(json_extract(value, '$.repo_id') AS INTEGER),
			       json_extract(value, '$.item_type'),
			       CAST(json_extract(value, '$.item_number') AS INTEGER)
			FROM json_each(?)
		)
		SELECT q.repo_id, q.item_type, q.item_number,
		       r.platform, r.platform_host, r.platform_repo_id, r.owner, r.name, r.repo_path,
		       p.title, p.state, p.url, p.author
		FROM requested q
		JOIN forge_repos r ON r.id = q.repo_id AND r.lifecycle_state = 'active'
		JOIN forge_merge_requests p
		  ON q.item_type = 'pull_request' AND p.repo_id = q.repo_id AND p.number = q.item_number
		WHERE NOT EXISTS (
			SELECT 1 FROM forge_archive_items ai
			WHERE ai.repo_id = p.repo_id
			  AND ai.item_type = 'merge_request'
			  AND ai.item_number = p.number
			  AND ai.lifecycle_state = 'removed_upstream'
		)
		UNION ALL
		SELECT q.repo_id, q.item_type, q.item_number,
		       r.platform, r.platform_host, r.platform_repo_id, r.owner, r.name, r.repo_path,
		       i.title, i.state, i.url, i.author
		FROM requested q
		JOIN forge_repos r ON r.id = q.repo_id AND r.lifecycle_state = 'active'
		JOIN forge_issues i
		  ON q.item_type = 'issue' AND i.repo_id = q.repo_id AND i.number = q.item_number
		WHERE NOT EXISTS (
			SELECT 1 FROM forge_archive_items ai
			WHERE ai.repo_id = i.repo_id
			  AND ai.item_type = 'issue'
			  AND ai.item_number = i.number
			  AND ai.lifecycle_state = 'removed_upstream'
		)`
	rows, err := d.ro.QueryContext(ctx, query, string(requestedJSON))
	if err != nil {
		return nil, fmt.Errorf("list workspace subject metadata: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var item WorkspaceSubjectMetadata
		if err := rows.Scan(
			&item.Key.RepoID, &item.Key.ItemType, &item.Key.ItemNumber,
			&item.Platform, &item.PlatformHost, &item.PlatformRepoID, &item.RepoOwner, &item.RepoName,
			&item.RepoPath, &item.Title, &item.State, &item.URL, &item.Author,
		); err != nil {
			return nil, fmt.Errorf("scan workspace subject metadata: %w", err)
		}
		out[item.Key] = item
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list workspace subject metadata rows: %w", err)
	}
	return out, nil
}
