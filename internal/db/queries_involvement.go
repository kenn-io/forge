package db

import (
	"context"
	"fmt"
	"strings"
)

// ListInvolvedWorkspaceSubjectKeys returns the subject identities that should
// survive the Involves me filter in workspace-derived activity.
func (d *DB) ListInvolvedWorkspaceSubjectKeys(
	ctx context.Context, viewers []RepoViewerLogin, candidates []WorkspaceSubjectKey,
) (map[WorkspaceSubjectKey]struct{}, error) {
	keys := make(map[WorkspaceSubjectKey]struct{})
	for _, subject := range []struct {
		itemType string
		table    string
		alias    string
		involves func(string, []RepoViewerLogin, *[]any) string
	}{
		{WorkspaceItemTypePullRequest, "forge_merge_requests", "p", mergeRequestInvolvementCondition},
		{WorkspaceItemTypeIssue, "forge_issues", "i", issueInvolvementCondition},
	} {
		var args []any
		candidateCondition := workspaceSubjectCandidateCondition(
			subject.alias, subject.itemType, candidates, &args,
		)
		if candidateCondition == "" {
			continue
		}
		involvementCondition := subject.involves(subject.alias, viewers, &args)
		query := fmt.Sprintf(`
			SELECT %[1]s.repo_id, %[1]s.number
			FROM %[2]s %[1]s
			JOIN forge_repos r ON r.id = %[1]s.repo_id
			WHERE r.lifecycle_state = 'active'
			  AND %[3]s
			  AND %[4]s`, subject.alias, subject.table, candidateCondition, involvementCondition)
		rows, err := d.roQueryContext(ctx, query, args...)
		if err != nil {
			return nil, fmt.Errorf("list involved workspace %s subjects: %w", subject.itemType, err)
		}
		for rows.Next() {
			var key WorkspaceSubjectKey
			key.ItemType = subject.itemType
			if err := rows.Scan(&key.RepoID, &key.ItemNumber); err != nil {
				rows.Close()
				return nil, fmt.Errorf("scan involved workspace %s subject: %w", subject.itemType, err)
			}
			keys[key] = struct{}{}
		}
		if err := rows.Close(); err != nil {
			return nil, fmt.Errorf("close involved workspace %s subjects: %w", subject.itemType, err)
		}
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("list involved workspace %s subject rows: %w", subject.itemType, err)
		}
	}
	return keys, nil
}

func workspaceSubjectCandidateCondition(
	alias, itemType string, candidates []WorkspaceSubjectKey, args *[]any,
) string {
	groups := make([]string, 0, len(candidates))
	seen := make(map[[2]int64]struct{}, len(candidates))
	for _, candidate := range candidates {
		if candidate.ItemType != itemType || candidate.RepoID <= 0 || candidate.ItemNumber <= 0 {
			continue
		}
		key := [2]int64{candidate.RepoID, int64(candidate.ItemNumber)}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		groups = append(groups, fmt.Sprintf("(%s.repo_id = ? AND %s.number = ?)", alias, alias))
		*args = append(*args, candidate.RepoID, candidate.ItemNumber)
	}
	if len(groups) == 0 {
		return ""
	}
	return "(" + strings.Join(groups, " OR ") + ")"
}

func mergeRequestInvolvementCondition(
	alias string, viewers []RepoViewerLogin, args *[]any,
) string {
	if len(viewers) == 0 {
		return "0 = 1"
	}
	groups := make([]string, 0, len(viewers))
	for _, viewer := range viewers {
		login := strings.TrimSpace(viewer.Login)
		if viewer.RepoID <= 0 || login == "" {
			continue
		}
		groups = append(groups, fmt.Sprintf(`(%[1]s.repo_id = ? AND (
			LOWER(%[1]s.author) = LOWER(?)
			OR EXISTS (
				SELECT 1 FROM json_each(COALESCE(NULLIF(%[1]s.assignees_json, ''), '[]'))
				WHERE LOWER(value) = LOWER(?)
			)
			OR EXISTS (
				SELECT 1 FROM json_each(COALESCE(NULLIF(%[1]s.reviewers_json, ''), '[]'))
				WHERE LOWER(value) = LOWER(?)
			)
			OR EXISTS (
				SELECT 1 FROM forge_mr_events involvement_event
				WHERE involvement_event.merge_request_id = %[1]s.id
				  AND involvement_event.event_type IN ('issue_comment', 'review', 'review_comment')
				  AND LOWER(involvement_event.author) = LOWER(?)
			)
			OR EXISTS (
				SELECT 1 FROM forge_notification_items involvement_notification
				WHERE involvement_notification.repo_id = %[1]s.repo_id
				  AND involvement_notification.item_type = 'pr'
				  AND involvement_notification.item_number = %[1]s.number
				  AND (
					involvement_notification.participating = 1
					OR involvement_notification.reason IN (
						'assign', 'author', 'comment', 'invitation',
						'mention', 'review_requested', 'team_mention'
					)
				  )
			)
		))`, alias))
		*args = append(*args, viewer.RepoID, login, login, login, login)
	}
	if len(groups) == 0 {
		return "0 = 1"
	}
	return "(" + strings.Join(groups, " OR ") + ")"
}

func issueInvolvementCondition(
	alias string, viewers []RepoViewerLogin, args *[]any,
) string {
	if len(viewers) == 0 {
		return "0 = 1"
	}
	groups := make([]string, 0, len(viewers))
	for _, viewer := range viewers {
		login := strings.TrimSpace(viewer.Login)
		if viewer.RepoID <= 0 || login == "" {
			continue
		}
		groups = append(groups, fmt.Sprintf(`(%[1]s.repo_id = ? AND (
			LOWER(%[1]s.author) = LOWER(?)
			OR EXISTS (
				SELECT 1 FROM json_each(COALESCE(NULLIF(%[1]s.assignees_json, ''), '[]'))
				WHERE LOWER(value) = LOWER(?)
			)
			OR EXISTS (
				SELECT 1 FROM forge_issue_events involvement_event
				WHERE involvement_event.issue_id = %[1]s.id
				  AND involvement_event.event_type = 'issue_comment'
				  AND LOWER(involvement_event.author) = LOWER(?)
			)
			OR EXISTS (
				SELECT 1 FROM forge_notification_items involvement_notification
				WHERE involvement_notification.repo_id = %[1]s.repo_id
				  AND involvement_notification.item_type = 'issue'
				  AND involvement_notification.item_number = %[1]s.number
				  AND (
					involvement_notification.participating = 1
					OR involvement_notification.reason IN (
						'assign', 'author', 'comment', 'invitation',
						'mention', 'review_requested', 'team_mention'
					)
				  )
			)
		))`, alias))
		*args = append(*args, viewer.RepoID, login, login, login)
	}
	if len(groups) == 0 {
		return "0 = 1"
	}
	return "(" + strings.Join(groups, " OR ") + ")"
}

func activityInvolvementCondition(viewers []RepoViewerLogin, args *[]any) string {
	if len(viewers) == 0 {
		return "0 = 1"
	}
	groups := make([]string, 0, len(viewers))
	for _, viewer := range viewers {
		login := strings.TrimSpace(viewer.Login)
		if viewer.RepoID <= 0 || login == "" {
			continue
		}

		var prArgs []any
		prCondition := mergeRequestInvolvementCondition(
			"involved_pr", []RepoViewerLogin{viewer}, &prArgs,
		)
		var issueArgs []any
		issueCondition := issueInvolvementCondition(
			"involved_issue", []RepoViewerLogin{viewer}, &issueArgs,
		)
		groups = append(groups, `(unified.repo_id = ? AND (
			(unified.item_type = 'pr' AND EXISTS (
				SELECT 1 FROM forge_merge_requests involved_pr
				WHERE involved_pr.repo_id = unified.repo_id
				  AND involved_pr.number = unified.item_number
				  AND `+prCondition+`
			))
			OR (unified.item_type = 'issue' AND EXISTS (
				SELECT 1 FROM forge_issues involved_issue
				WHERE involved_issue.repo_id = unified.repo_id
				  AND involved_issue.number = unified.item_number
				  AND `+issueCondition+`
			))
		))`)
		*args = append(*args, viewer.RepoID)
		*args = append(*args, prArgs...)
		*args = append(*args, issueArgs...)
	}
	if len(groups) == 0 {
		return "0 = 1"
	}
	return "(" + strings.Join(groups, " OR ") + ")"
}
