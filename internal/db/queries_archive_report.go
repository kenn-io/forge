package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

type ArchiveReportActivityKind string

const (
	ArchiveReportActivityIssue               ArchiveReportActivityKind = "issue"
	ArchiveReportActivityIssueClosed         ArchiveReportActivityKind = "issue_closed"
	ArchiveReportActivityMergeRequest        ArchiveReportActivityKind = "merge_request"
	ArchiveReportActivityMergeRequestMerged  ArchiveReportActivityKind = "merge_request_merged"
	ArchiveReportActivityOrdinaryComment     ArchiveReportActivityKind = "ordinary_comment"
	ArchiveReportActivityReview              ArchiveReportActivityKind = "review"
	ArchiveReportActivityInlineReviewComment ArchiveReportActivityKind = "inline_review_comment"
)

type ArchiveReportActivityRow struct {
	RepoID             int64
	Platform           string
	PlatformHost       string
	Owner              string
	Name               string
	RepoPath           string
	Kind               ArchiveReportActivityKind
	ItemNumber         int
	ProviderExternalID string
	Title              string
	Author             string
	Actor              string
	OccurredAt         time.Time
	Body               string
	URL                string
	Comments           int
	Additions          int
	Deletions          int
	FilesChanged       *int
	MergeCommitSHA     string
}

type ArchiveReportMeasurement struct {
	Records   int
	TextBytes int64
}

type ArchiveReportCountRow struct {
	RepoID       int64
	Platform     string
	PlatformHost string
	Kind         ArchiveReportActivityKind
	Author       string
	Count        int
}

type ArchiveReportRepositoryRow struct {
	RepoID       int64
	Platform     string
	PlatformHost string
	Owner        string
	Name         string
	RepoPath     string
	State        ArchiveRepoState
	Progress     ArchiveRepoProgress
}

func LoadArchiveReportRepositories(
	ctx context.Context,
	tx *sql.Tx,
	repoIDs []int64,
	now time.Time,
) ([]ArchiveReportRepositoryRow, error) {
	if tx == nil {
		return nil, errors.New("load archive report repositories: transaction is required")
	}
	repoIDs = normalizedArchiveRepoIDs(repoIDs)
	states, err := listArchiveRepoStates(ctx, tx, repoIDs)
	if err != nil {
		return nil, err
	}
	if len(repoIDs) > 0 && len(states) != len(repoIDs) {
		found := make(map[int64]struct{}, len(states))
		for _, state := range states {
			found[state.RepoID] = struct{}{}
		}
		missing := make([]int64, 0, len(repoIDs)-len(states))
		for _, repoID := range repoIDs {
			if _, ok := found[repoID]; !ok {
				missing = append(missing, repoID)
			}
		}
		return nil, &ArchiveRepoStateNotFoundError{RepoIDs: missing}
	}
	if len(states) == 0 {
		return []ArchiveReportRepositoryRow{}, nil
	}
	ids := make([]int64, len(states))
	stateByID := make(map[int64]ArchiveRepoState, len(states))
	for i, state := range states {
		ids[i] = state.RepoID
		stateByID[state.RepoID] = state
	}
	counts, err := loadArchiveProgressCounts(ctx, tx, ids, now.UTC())
	if err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, fmt.Sprintf(`
		SELECT id, platform, platform_host, owner, name, repo_path
		FROM middleman_repos
		WHERE id IN (%s)
		ORDER BY platform, platform_host, owner, name, id`, sqlPlaceholders(len(ids))),
		archiveRepoIDArgs(ids)...)
	if err != nil {
		return nil, fmt.Errorf("load archive report repository identities: %w", err)
	}
	defer rows.Close()
	result := make([]ArchiveReportRepositoryRow, 0, len(states))
	for rows.Next() {
		var row ArchiveReportRepositoryRow
		if err := rows.Scan(
			&row.RepoID, &row.Platform, &row.PlatformHost, &row.Owner, &row.Name, &row.RepoPath,
		); err != nil {
			return nil, fmt.Errorf("scan archive report repository identity: %w", err)
		}
		row.State = stateByID[row.RepoID]
		row.Progress = deriveArchiveProgress(row.State, counts[row.RepoID], now.UTC())
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("load archive report repository identities: %w", err)
	}
	return result, nil
}

func LoadArchiveReportActivity(
	ctx context.Context,
	tx *sql.Tx,
	repoIDs []int64,
	start time.Time,
	end time.Time,
) ([]ArchiveReportActivityRow, error) {
	query, args, err := archiveReportActivityQuery(repoIDs, start, end)
	if err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, query+`
		SELECT repo_id, platform, platform_host, owner, name, repo_path,
			kind, item_number, provider_external_id, title, author,
			actor, occurred_at, body, url, comments, additions, deletions,
			files_changed, merge_commit_sha
		FROM deduplicated
		WHERE identity_rank = 1
		ORDER BY platform, platform_host, owner, name, occurred_at,
			kind, item_number, provider_external_id`, args...)
	if err != nil {
		return nil, fmt.Errorf("load archive report activity: %w", err)
	}
	defer rows.Close()
	result := make([]ArchiveReportActivityRow, 0)
	for rows.Next() {
		var row ArchiveReportActivityRow
		if err := rows.Scan(
			&row.RepoID, &row.Platform, &row.PlatformHost, &row.Owner, &row.Name, &row.RepoPath,
			&row.Kind, &row.ItemNumber, &row.ProviderExternalID, &row.Title, &row.Author,
			&row.Actor, &row.OccurredAt, &row.Body, &row.URL, &row.Comments,
			&row.Additions, &row.Deletions, &row.FilesChanged, &row.MergeCommitSHA,
		); err != nil {
			return nil, fmt.Errorf("scan archive report activity: %w", err)
		}
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("load archive report activity: %w", err)
	}
	return result, nil
}

func LoadArchiveReportCounts(
	ctx context.Context,
	tx *sql.Tx,
	repoIDs []int64,
	start time.Time,
	end time.Time,
) ([]ArchiveReportCountRow, error) {
	query, args, err := archiveReportActivityQuery(repoIDs, start, end)
	if err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, query+`
		SELECT repo_id, platform, platform_host, kind,
			CASE kind
				WHEN 'issue_closed' THEN actor
				WHEN 'merge_request_merged' THEN actor
				ELSE author
			END AS contributor,
			COUNT(*)
		FROM deduplicated
		WHERE identity_rank = 1
		GROUP BY repo_id, platform, platform_host, kind, contributor
		ORDER BY platform, platform_host, contributor, kind, repo_id`, args...)
	if err != nil {
		return nil, fmt.Errorf("load archive report counts: %w", err)
	}
	defer rows.Close()
	result := make([]ArchiveReportCountRow, 0)
	for rows.Next() {
		var row ArchiveReportCountRow
		if err := rows.Scan(
			&row.RepoID, &row.Platform, &row.PlatformHost, &row.Kind, &row.Author, &row.Count,
		); err != nil {
			return nil, fmt.Errorf("scan archive report counts: %w", err)
		}
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("load archive report counts: %w", err)
	}
	return result, nil
}

func MeasureArchiveReportActivity(
	ctx context.Context,
	tx *sql.Tx,
	repoIDs []int64,
	start time.Time,
	end time.Time,
) (ArchiveReportMeasurement, error) {
	query, args, err := archiveReportActivityQuery(repoIDs, start, end)
	if err != nil {
		return ArchiveReportMeasurement{}, err
	}
	var measurement ArchiveReportMeasurement
	err = tx.QueryRowContext(ctx, query+`
		SELECT COUNT(*), COALESCE(SUM(
			length(CAST(title AS BLOB)) + length(CAST(body AS BLOB))
		), 0)
		FROM deduplicated
		WHERE identity_rank = 1`, args...).Scan(&measurement.Records, &measurement.TextBytes)
	if err != nil {
		return ArchiveReportMeasurement{}, fmt.Errorf("measure archive report activity: %w", err)
	}
	return measurement, nil
}

func archiveReportActivityQuery(
	repoIDs []int64,
	start time.Time,
	end time.Time,
) (string, []any, error) {
	repoIDs = normalizedArchiveRepoIDs(repoIDs)
	if len(repoIDs) == 0 {
		return "", nil, errors.New("archive report activity: repository scope is empty")
	}
	if start.IsZero() || end.IsZero() || !start.Before(end) {
		return "", nil, errors.New("archive report activity: start must precede end")
	}
	values := make([]string, len(repoIDs))
	args := make([]any, 0, len(repoIDs)+2)
	for i, repoID := range repoIDs {
		values[i] = "(?)"
		args = append(args, repoID)
	}
	args = append(args, start.UTC(), end.UTC())
	query := fmt.Sprintf(`
		WITH scope(repo_id) AS (VALUES %s),
		bounds(start_at, end_at) AS (VALUES (?, ?)),
		activity AS (
			SELECT r.id AS repo_id, r.platform, r.platform_host, r.owner, r.name, r.repo_path,
				'issue' AS kind, i.number AS item_number,
				COALESCE(NULLIF(i.platform_external_id, ''), 'issue:number:' || i.number) AS provider_external_id,
				i.title, i.author, '' AS actor, i.created_at AS occurred_at, i.body, i.url,
				i.comment_count AS comments, 0 AS additions, 0 AS deletions,
				NULL AS files_changed, '' AS merge_commit_sha,
				1 AS source_rank, i.id AS source_row_id
			FROM middleman_issues i
			JOIN scope s ON s.repo_id = i.repo_id
			JOIN middleman_repos r ON r.id = i.repo_id
			CROSS JOIN bounds b
			WHERE i.created_at >= b.start_at AND i.created_at < b.end_at
			UNION ALL
			SELECT r.id, r.platform, r.platform_host, r.owner, r.name, r.repo_path,
				'issue_closed', i.number,
				COALESCE(NULLIF(i.platform_external_id, ''), 'issue:number:' || i.number),
				i.title, i.author,
				COALESCE((
					SELECT e.author
					FROM middleman_issue_events e
					WHERE e.issue_id = i.id AND e.event_type = 'closed' AND e.author <> ''
					ORDER BY e.created_at DESC, e.id DESC
					LIMIT 1
				), ''),
				i.closed_at, '', i.url, i.comment_count, 0, 0, NULL, '',
				2, i.id
			FROM middleman_issues i
			JOIN scope s ON s.repo_id = i.repo_id
			JOIN middleman_repos r ON r.id = i.repo_id
			CROSS JOIN bounds b
			WHERE i.closed_at >= b.start_at AND i.closed_at < b.end_at
			UNION ALL
			SELECT r.id, r.platform, r.platform_host, r.owner, r.name, r.repo_path,
				'merge_request', mr.number,
				COALESCE(NULLIF(mr.platform_external_id, ''), 'merge_request:number:' || mr.number),
				mr.title, mr.author, '', mr.created_at, mr.body, mr.url,
				mr.comment_count, mr.additions, mr.deletions, mr.files_changed, mr.merge_commit_sha,
				3, mr.id
			FROM middleman_merge_requests mr
			JOIN scope s ON s.repo_id = mr.repo_id
			JOIN middleman_repos r ON r.id = mr.repo_id
			CROSS JOIN bounds b
			WHERE mr.created_at >= b.start_at AND mr.created_at < b.end_at
			UNION ALL
			SELECT r.id, r.platform, r.platform_host, r.owner, r.name, r.repo_path,
				'merge_request_merged', mr.number,
				COALESCE(NULLIF(mr.platform_external_id, ''), 'merge_request:number:' || mr.number),
				mr.title, mr.author,
				COALESCE((
					SELECT e.author
					FROM middleman_mr_events e
					WHERE e.merge_request_id = mr.id AND e.event_type = 'merged' AND e.author <> ''
					ORDER BY e.created_at DESC, e.id DESC
					LIMIT 1
				), ''),
				mr.merged_at, '', mr.url, mr.comment_count, mr.additions, mr.deletions,
				mr.files_changed, mr.merge_commit_sha,
				4, mr.id
			FROM middleman_merge_requests mr
			JOIN scope s ON s.repo_id = mr.repo_id
			JOIN middleman_repos r ON r.id = mr.repo_id
			CROSS JOIN bounds b
			WHERE mr.merged_at >= b.start_at AND mr.merged_at < b.end_at
			UNION ALL
			SELECT r.id, r.platform, r.platform_host, r.owner, r.name, r.repo_path,
				'ordinary_comment', i.number,
				COALESCE(NULLIF(e.platform_external_id, ''), e.dedupe_key),
				i.title, e.author, '', e.created_at, e.body,
				COALESCE(NULLIF(e.direct_url, ''), i.url),
				0, 0, 0, NULL, '',
				5, e.id
			FROM middleman_issue_events e
			JOIN middleman_issues i ON i.id = e.issue_id
			JOIN scope s ON s.repo_id = i.repo_id
			JOIN middleman_repos r ON r.id = i.repo_id
			CROSS JOIN bounds b
			WHERE e.event_type = 'issue_comment'
			  AND e.created_at >= b.start_at AND e.created_at < b.end_at
			UNION ALL
			SELECT r.id, r.platform, r.platform_host, r.owner, r.name, r.repo_path,
				CASE e.event_type
					WHEN 'issue_comment' THEN 'ordinary_comment'
					WHEN 'review' THEN 'review'
					ELSE 'inline_review_comment'
				END,
				mr.number, COALESCE(NULLIF(e.platform_external_id, ''), e.dedupe_key),
				mr.title, e.author, '', e.created_at, e.body,
				COALESCE(NULLIF(e.direct_url, ''), mr.url),
				0, 0, 0, NULL, '',
				6, e.id
			FROM middleman_mr_events e
			JOIN middleman_merge_requests mr ON mr.id = e.merge_request_id
			JOIN scope s ON s.repo_id = mr.repo_id
			JOIN middleman_repos r ON r.id = mr.repo_id
			CROSS JOIN bounds b
			WHERE e.event_type IN ('issue_comment', 'review', 'review_comment')
			  AND e.created_at >= b.start_at AND e.created_at < b.end_at
		),
		deduplicated AS (
			SELECT activity.*,
				ROW_NUMBER() OVER (
					PARTITION BY repo_id, kind, provider_external_id
					ORDER BY occurred_at, item_number, provider_external_id,
						source_rank, source_row_id
				) AS identity_rank
			FROM activity
		)`, strings.Join(values, ","))
	return query, args, nil
}
