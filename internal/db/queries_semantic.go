package db

import (
	"context"
	"fmt"
)

// SemanticSourceKind names one table of prose the semantic index embeds.
type SemanticSourceKind string

const (
	SemanticSourcePull         SemanticSourceKind = "pr"
	SemanticSourceIssue        SemanticSourceKind = "issue"
	SemanticSourcePullComment  SemanticSourceKind = "pr_comment"
	SemanticSourceIssueComment SemanticSourceKind = "issue_comment"
)

// SemanticSourceKinds lists every kind the semantic index pages through.
var SemanticSourceKinds = []SemanticSourceKind{
	SemanticSourcePull, SemanticSourceIssue,
	SemanticSourcePullComment, SemanticSourceIssueComment,
}

// SemanticSource is one pull request, issue, or comment as the semantic index
// reads it. Title is empty for comments. ItemType is "pr" or "issue" and,
// with RepoID and ItemNumber, names the item a comment belongs to.
type SemanticSource struct {
	Kind       SemanticSourceKind
	ID         int64
	RepoID     int64
	ItemType   string
	ItemNumber int
	ItemState  string
	Title      string
	Body       string
}

// semanticSourceQueries select one page of each kind in ascending row ID.
// Only active repositories and items still present upstream are included,
// matching what the pull request, issue, and activity lists show. Comments
// are conversation and review prose; commits, pushes, and other timeline
// events are not embedded.
var semanticSourceQueries = map[SemanticSourceKind]string{
	SemanticSourcePull: `
		SELECT m.id, m.repo_id, 'pr', m.number, m.state, m.title, m.body
		FROM forge_merge_requests m
		JOIN forge_repos r ON r.id = m.repo_id AND r.lifecycle_state = 'active'
		WHERE m.id > ?
		  AND NOT EXISTS (
		      SELECT 1 FROM forge_archive_items ai
		      WHERE ai.repo_id = m.repo_id
		        AND ai.item_type = 'merge_request'
		        AND ai.item_number = m.number
		        AND ai.lifecycle_state = 'removed_upstream'
		  )
		ORDER BY m.id
		LIMIT ?`,
	SemanticSourceIssue: `
		SELECT i.id, i.repo_id, 'issue', i.number, i.state, i.title, i.body
		FROM forge_issues i
		JOIN forge_repos r ON r.id = i.repo_id AND r.lifecycle_state = 'active'
		WHERE i.id > ?
		  AND NOT EXISTS (
		      SELECT 1 FROM forge_archive_items ai
		      WHERE ai.repo_id = i.repo_id
		        AND ai.item_type = 'issue'
		        AND ai.item_number = i.number
		        AND ai.lifecycle_state = 'removed_upstream'
		  )
		ORDER BY i.id
		LIMIT ?`,
	SemanticSourcePullComment: `
		SELECT e.id, m.repo_id, 'pr', m.number, m.state, '', e.body
		FROM forge_mr_events e
		JOIN forge_merge_requests m ON m.id = e.merge_request_id
		JOIN forge_repos r ON r.id = m.repo_id AND r.lifecycle_state = 'active'
		WHERE e.id > ?
		  AND e.event_type IN ('issue_comment', 'review', 'review_comment')
		  AND trim(e.body, char(32, 9, 10, 13)) <> ''
		  AND NOT EXISTS (
		      SELECT 1 FROM forge_archive_items ai
		      WHERE ai.repo_id = m.repo_id
		        AND ai.item_type = 'merge_request'
		        AND ai.item_number = m.number
		        AND ai.lifecycle_state = 'removed_upstream'
		  )
		ORDER BY e.id
		LIMIT ?`,
	SemanticSourceIssueComment: `
		SELECT e.id, i.repo_id, 'issue', i.number, i.state, '', e.body
		FROM forge_issue_events e
		JOIN forge_issues i ON i.id = e.issue_id
		JOIN forge_repos r ON r.id = i.repo_id AND r.lifecycle_state = 'active'
		WHERE e.id > ?
		  AND e.event_type = 'issue_comment'
		  AND trim(e.body, char(32, 9, 10, 13)) <> ''
		  AND NOT EXISTS (
		      SELECT 1 FROM forge_archive_items ai
		      WHERE ai.repo_id = i.repo_id
		        AND ai.item_type = 'issue'
		        AND ai.item_number = i.number
		        AND ai.lifecycle_state = 'removed_upstream'
		  )
		ORDER BY e.id
		LIMIT ?`,
}

// ListSemanticSources returns up to limit sources of kind whose row ID is
// greater than afterID, in ascending ID order. Callers page by passing the
// last returned ID back as afterID.
func (d *DB) ListSemanticSources(
	ctx context.Context, kind SemanticSourceKind, afterID int64, limit int,
) ([]SemanticSource, error) {
	query, ok := semanticSourceQueries[kind]
	if !ok {
		return nil, fmt.Errorf("unknown semantic source kind %q", kind)
	}
	rows, err := d.roQueryContext(ctx, query, afterID, limit)
	if err != nil {
		return nil, fmt.Errorf("list semantic sources %s: %w", kind, err)
	}
	defer rows.Close()
	sources := make([]SemanticSource, 0, limit)
	for rows.Next() {
		source := SemanticSource{Kind: kind}
		if err := rows.Scan(
			&source.ID, &source.RepoID, &source.ItemType, &source.ItemNumber,
			&source.ItemState, &source.Title, &source.Body,
		); err != nil {
			return nil, fmt.Errorf("scan semantic source %s: %w", kind, err)
		}
		sources = append(sources, source)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read semantic sources %s: %w", kind, err)
	}
	return sources, nil
}
