package db

import (
	"context"
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
	for _, subject := range []struct {
		itemType string
		table    string
		alias    string
	}{
		{WorkspaceItemTypePullRequest, "forge_merge_requests", "p"},
		{WorkspaceItemTypeIssue, "forge_issues", "i"},
	} {
		var args []any
		candidateCondition := workspaceSubjectCandidateCondition(
			subject.alias, subject.itemType, candidates, &args,
		)
		if candidateCondition == "" {
			continue
		}
		query := fmt.Sprintf(`
			SELECT %[1]s.repo_id, %[1]s.number
			FROM %[2]s %[1]s
			JOIN forge_repos r ON r.id = %[1]s.repo_id
			WHERE r.lifecycle_state = 'active'
			  AND %[3]s
			  AND %[4]s`, subject.alias, subject.table, candidateCondition, unassignedCondition(subject.alias))
		rows, err := d.ro.QueryContext(ctx, query, args...)
		if err != nil {
			return nil, fmt.Errorf("list unassigned workspace %s subjects: %w", subject.itemType, err)
		}
		for rows.Next() {
			var key WorkspaceSubjectKey
			key.ItemType = subject.itemType
			if err := rows.Scan(&key.RepoID, &key.ItemNumber); err != nil {
				rows.Close()
				return nil, fmt.Errorf("scan unassigned workspace %s subject: %w", subject.itemType, err)
			}
			keys[key] = struct{}{}
		}
		if err := rows.Close(); err != nil {
			return nil, fmt.Errorf("close unassigned workspace %s subjects: %w", subject.itemType, err)
		}
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("list unassigned workspace %s subject rows: %w", subject.itemType, err)
		}
	}
	return keys, nil
}
