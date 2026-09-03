package db

import (
	"context"
	"encoding/json"
	"fmt"
)

func unassignedCondition(alias string) string {
	return fmt.Sprintf(`%s.assignees_json = '[]'`, alias)
}

func activityUnassignedCondition() string {
	return `(
		(unified.item_type = 'pr' AND EXISTS (
			SELECT 1 FROM forge_merge_requests unassigned_pr
			WHERE unassigned_pr.repo_id = unified.repo_id
			  AND unassigned_pr.number = unified.item_number
			  AND ` + unassignedCondition("unassigned_pr") + `
		))
		OR (unified.item_type = 'issue' AND EXISTS (
			SELECT 1 FROM forge_issues unassigned_issue
			WHERE unassigned_issue.repo_id = unified.repo_id
			  AND unassigned_issue.number = unified.item_number
			  AND ` + unassignedCondition("unassigned_issue") + `
		))
	)`
}

// ListUnassignedWorkspaceSubjectKeys returns workspace subject identities whose
// synchronized pull request or issue has no assignees.
func (d *DB) ListUnassignedWorkspaceSubjectKeys(
	ctx context.Context, candidates []WorkspaceSubjectKey,
) (map[WorkspaceSubjectKey]struct{}, error) {
	keys := make(map[WorkspaceSubjectKey]struct{})
	if len(candidates) == 0 {
		return keys, nil
	}
	candidatesJSON, err := json.Marshal(candidates)
	if err != nil {
		return nil, fmt.Errorf("encode unassigned workspace subjects: %w", err)
	}
	query := `WITH requested(repo_id, item_type, item_number) AS (
			SELECT CAST(json_extract(value, '$.repo_id') AS INTEGER),
			       json_extract(value, '$.item_type'),
			       CAST(json_extract(value, '$.item_number') AS INTEGER)
			FROM json_each(?)
		)
		SELECT DISTINCT q.repo_id, q.item_type, q.item_number
		FROM requested q
		JOIN forge_repos r ON r.id = q.repo_id AND r.lifecycle_state = 'active'
		LEFT JOIN forge_merge_requests p
		  ON q.item_type = 'pull_request' AND p.repo_id = q.repo_id AND p.number = q.item_number
		LEFT JOIN forge_issues i
		  ON q.item_type = 'issue' AND i.repo_id = q.repo_id AND i.number = q.item_number
		WHERE ((p.number IS NOT NULL AND ` + unassignedCondition("p") + `)
		       OR (i.number IS NOT NULL AND ` + unassignedCondition("i") + `))
		  AND NOT EXISTS (
			SELECT 1 FROM forge_archive_items ai
			WHERE ai.repo_id = q.repo_id
			  AND ai.item_type = CASE q.item_type
			      WHEN 'pull_request' THEN 'merge_request' ELSE 'issue' END
			  AND ai.item_number = q.item_number
			  AND ai.lifecycle_state = 'removed_upstream'
		  )`
	rows, err := d.ro.QueryContext(ctx, query, candidatesJSON)
	if err != nil {
		return nil, fmt.Errorf("list unassigned workspace subjects: %w", err)
	}
	for rows.Next() {
		var key WorkspaceSubjectKey
		if err := rows.Scan(&key.RepoID, &key.ItemType, &key.ItemNumber); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan unassigned workspace subject: %w", err)
		}
		keys[key] = struct{}{}
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close unassigned workspace subjects: %w", err)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list unassigned workspace subject rows: %w", err)
	}
	return keys, nil
}
