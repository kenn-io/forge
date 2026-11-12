package db

import (
	"context"
	"database/sql"
	"encoding/json/v2"
	"fmt"
	"time"
)

// ArchiveSnapshotItem is a cached projection, with no provider or mutation path.
type ArchiveSnapshotItem struct {
	ID, RepoID                                           int64
	Kind                                                 string
	Number                                               int
	URL, Title, Author, State, Body                      string
	AuthorAccountType, AuthorAssociation                 *string
	CreatedAt, UpdatedAt                                 time.Time
	DetailFetchedAt                                      *time.Time
	LabelsJSON                                           string
	Draft                                                bool
	HeadRepoIdentityStale                                bool
	CommentAuthorAssociationUnknown                      bool
	HeadSHA, HeadBranch, BaseBranch, HeadRepoCloneURL    string
	Additions, Deletions                                 int
	FilesChanged                                         *int
	ReviewDecision, CIStatus, ChecksJSON, MergeableState string
	// AssigneesJSON and ReviewersJSON carry the raw ownership columns: ""
	// means the provider never reported the list, "[]" a confirmed-empty
	// one. ReviewersJSON is always "" for issues.
	AssigneesJSON, ReviewersJSON string
	LastActivityAt               time.Time
	ClosedAt, MergedAt           *time.Time
	// CIObservedAt, ReviewDecisionObservedAt, and MergeableStateObservedAt
	// are NULL for issues, and for PRs whose sibling value is unknown
	// provenance. The snapshot exporter computes merge_status_observed_at
	// as their minimum, not SQL, so a driver-fragile computed column is
	// never scanned.
	CIObservedAt, ReviewDecisionObservedAt, MergeableStateObservedAt *time.Time
}

type ArchiveSnapshotReview struct {
	ID, MergeRequestID            int64
	Author, Body, State, URL, Key string
	AuthorAssociation             *string
	CreatedAt                     time.Time
}

type ArchiveSnapshotReference struct {
	IssueID, MergeRequestID int64
	URL, EventKey           string
	ObservedAt              time.Time
}

func LoadArchiveSnapshotRepository(ctx context.Context, tx *sql.Tx, identity RepoIdentity) (*Repo, error) {
	identity = canonicalRepoIdentity(identity)
	var repo Repo
	args := []any{identity.Platform, identity.PlatformHost}
	match := "repo_path_key=?"
	if identity.Key.IsZero() {
		args = append(args, identity.RepoPathKey)
	} else {
		match = repositoryKeyCondition("", identity.Key, &args)
	}
	keyID, keyUUID := repositoryKeyColumns(&repo.Key)
	err := tx.QueryRowContext(ctx, `SELECT id,platform,platform_host,platform_repo_id,bitbucket_repository_uuid,owner,name,repo_path,web_url,clone_url,default_branch,last_sync_completed_at,COALESCE(last_sync_error,'')
 FROM forge_repos WHERE lifecycle_state='active' AND platform=? AND platform_host=? AND `+match,
		args...,
	).Scan(&repo.ID, &repo.Platform, &repo.PlatformHost, keyID, keyUUID, &repo.Owner, &repo.Name, &repo.RepoPath, &repo.WebURL, &repo.CloneURL, &repo.DefaultBranch, &repo.LastSyncCompletedAt, &repo.LastSyncError)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load snapshot repository: %w", err)
	}
	return &repo, nil
}

// A reference records the repository that held its source route when it was
// observed, and links only to that repository's pull. Routes never decide the
// link, so renames keep it and a later occupant of the route never gains it.
const archiveSnapshotLinks = `link_candidates AS (
 SELECT f.*, p.id AS merge_request_id
 FROM forge_issue_pr_references f
 JOIN forge_issues i ON i.id=f.issue_id
 JOIN scoped_repos r ON r.id=i.repo_id
 JOIN pulls p ON p.repo_id = f.source_repo_id AND p.number = f.source_number
), links AS (
 SELECT * FROM (
 SELECT *, ROW_NUMBER() OVER (PARTITION BY issue_id,merge_request_id,observed_event_key
 ORDER BY observed_at DESC,source_url) AS reference_rank
 FROM link_candidates
 ) WHERE reference_rank=1
)`

const archiveSnapshotPullLinks = `pull_links AS (
 SELECT e.merge_request_id AS target_id, source.id AS source_id, e.dedupe_key,
 COALESCE(json_extract(CASE WHEN json_valid(e.metadata_json) THEN e.metadata_json ELSE '{}' END, '$.source_url'), '') AS source_url, e.created_at
 FROM forge_mr_events e JOIN pulls target ON target.id=e.merge_request_id
 JOIN pulls source ON source.repo_id=e.repo_id AND source.number=json_extract(CASE WHEN json_valid(e.metadata_json) THEN e.metadata_json ELSE '{}' END,'$.source_number')
 WHERE e.event_type='cross_referenced' AND json_valid(e.metadata_json)
 AND json_extract(CASE WHEN json_valid(e.metadata_json) THEN e.metadata_json ELSE '{}' END,'$.source_type')='PullRequest'
 AND json_extract(CASE WHEN json_valid(e.metadata_json) THEN e.metadata_json ELSE '{}' END,'$.is_cross_repository')=0
)`

const (
	archiveSnapshotCommentBody   = `COALESCE(CAST(substr(CAST(COALESCE(e.body,'') AS BLOB),1,2049) AS TEXT),'')`
	archiveSnapshotCommentFilter = `(e.author=p.author OR upper(e.author_association) IN ('MEMBER','OWNER'))`
)

// Reuse exactly the same candidate set for preflight and materialization.
const archiveSnapshotScope = `WITH scoped_repos AS (SELECT value AS id FROM json_each(?)),
 roots AS (
 SELECT p.id,p.repo_id,p.number FROM forge_merge_requests p JOIN scoped_repos r ON r.id=p.repo_id
 WHERE (p.state='open' OR (? AND p.state IN ('closed','merged') AND COALESCE(p.merged_at,p.closed_at)>=? AND COALESCE(p.merged_at,p.closed_at)<=?)) AND NOT EXISTS (SELECT 1 FROM forge_archive_items a WHERE a.repo_id=p.repo_id AND a.item_type='merge_request' AND a.item_number=p.number AND a.lifecycle_state='removed_upstream')
 ), pulls AS (
 SELECT * FROM roots
 UNION
 SELECT source.id,source.repo_id,source.number FROM roots target
 JOIN forge_mr_events e ON e.merge_request_id=target.id AND e.event_type='cross_referenced' AND json_valid(e.metadata_json)
 JOIN forge_merge_requests source ON source.repo_id=e.repo_id AND source.number=json_extract(CASE WHEN json_valid(e.metadata_json) THEN e.metadata_json ELSE '{}' END,'$.source_number')
 WHERE ? AND json_extract(CASE WHEN json_valid(e.metadata_json) THEN e.metadata_json ELSE '{}' END,'$.source_type')='PullRequest' AND json_extract(CASE WHEN json_valid(e.metadata_json) THEN e.metadata_json ELSE '{}' END,'$.is_cross_repository')=0
 AND NOT EXISTS (SELECT 1 FROM forge_archive_items a WHERE a.repo_id=source.repo_id AND a.item_type='merge_request' AND a.item_number=source.number AND a.lifecycle_state='removed_upstream')
 ), ` + archiveSnapshotLinks + `, ` + archiveSnapshotPullLinks + `,
 issues AS (
 SELECT i.id FROM forge_issues i JOIN scoped_repos r ON r.id=i.repo_id
 WHERE ((CASE WHEN ? THEN i.state='open' ELSE i.created_at>=? AND i.created_at<? END) OR i.id IN (SELECT issue_id FROM links))
 AND NOT EXISTS (SELECT 1 FROM forge_archive_items a WHERE a.repo_id=i.repo_id AND a.item_type='issue' AND a.item_number=i.number AND a.lifecycle_state='removed_upstream')
 )`

// MeasureArchiveSnapshot bounds records and projected text without loading bodies
// into Go. Include all variable text, including checks, labels and review metadata.
func MeasureArchiveSnapshot(ctx context.Context, tx *sql.Tx, repoIDs []int64, start, end time.Time, openIssuesOnly, recentPulls bool, observedAt time.Time) (ArchiveReportMeasurement, error) {
	ids, err := json.Marshal(repoIDs)
	if err != nil {
		return ArchiveReportMeasurement{}, err
	}
	var result ArchiveReportMeasurement
	err = tx.QueryRowContext(ctx, archiveSnapshotScope+`, texts AS (
 SELECT length(CAST(json_array(p.url,p.title,p.author,p.author_association,p.state,
 COALESCE(CAST(substr(CAST(COALESCE(p.body,'') AS BLOB),1,8193) AS TEXT),''),p.platform_head_sha,p.head_branch,p.base_branch,p.head_repo_clone_url,
 p.review_decision,p.ci_status,p.ci_checks_json,p.mergeable_state,p.assignees_json,p.reviewers_json,
 (SELECT json_group_array(l.name) FROM forge_merge_request_labels ml JOIN forge_labels l ON l.id=ml.label_id WHERE ml.merge_request_id=p.id)) AS BLOB)) + CASE WHEN ? THEN length(CAST(json_array(p.author_account_type) AS BLOB))-1 ELSE 0 END AS bytes
 FROM forge_merge_requests p JOIN pulls selected ON selected.id=p.id
 UNION ALL
 SELECT length(CAST(json_array(i.url,i.title,i.author,i.author_association,i.state,COALESCE(CAST(substr(CAST(COALESCE(i.body,'') AS BLOB),1,8193) AS TEXT),''),i.assignees_json,
 (SELECT json_group_array(l.name) FROM forge_issue_labels il JOIN forge_labels l ON l.id=il.label_id WHERE il.issue_id=i.id)) AS BLOB))
 FROM forge_issues i JOIN issues selected ON selected.id=i.id
 UNION ALL
 SELECT length(CAST(json_array(e.author,e.author_association,COALESCE(CAST(substr(CAST(COALESCE(e.body,'') AS BLOB),1,2049) AS TEXT),''),e.summary,e.direct_url,e.dedupe_key) AS BLOB))
 FROM forge_mr_events e JOIN pulls p ON p.id=e.merge_request_id WHERE e.event_type IN ('review','review_comment')
 UNION ALL
 SELECT length(CAST(json_array(source_url,observed_event_key) AS BLOB)) FROM links
 UNION ALL
 SELECT length(CAST(json_array(e.author,e.author_association,`+archiveSnapshotCommentBody+`,e.direct_url,e.dedupe_key) AS BLOB))
 FROM forge_mr_events e JOIN pulls selected ON selected.id=e.merge_request_id JOIN forge_merge_requests p ON p.id=selected.id
 WHERE ? AND p.state IN ('closed','merged') AND e.event_type='issue_comment' AND `+archiveSnapshotCommentFilter+`
 UNION ALL
 SELECT length(CAST(json_array(source_url,dedupe_key) AS BLOB)) FROM pull_links WHERE ?
 ) SELECT count(*),COALESCE(sum(bytes),0) FROM texts`, string(ids), recentPulls, observedAt.Add(-30*24*time.Hour).UTC(), observedAt.UTC(), recentPulls, openIssuesOnly, start.UTC(), end.UTC(), recentPulls, recentPulls, recentPulls).Scan(&result.Records, &result.TextBytes)
	if err != nil {
		return result, fmt.Errorf("measure archive snapshot: %w", err)
	}
	return result, nil
}

func LoadArchiveSnapshotItems(ctx context.Context, tx *sql.Tx, repoIDs []int64, start, end time.Time, openIssuesOnly, recentPulls bool, observedAt time.Time) ([]ArchiveSnapshotItem, error) {
	ids, err := json.Marshal(repoIDs)
	if err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, archiveSnapshotScope+`
 SELECT p.id,p.repo_id,'pull_request',p.number,COALESCE(p.url,''),p.title,p.author,p.state,COALESCE(CAST(substr(CAST(COALESCE(p.body,'') AS BLOB),1,8193) AS TEXT),''),p.author_account_type,p.author_association,p.created_at,p.updated_at,p.detail_fetched_at,
 (SELECT json_group_array(l.name) FROM forge_merge_request_labels ml JOIN forge_labels l ON l.id=ml.label_id WHERE ml.merge_request_id=p.id),
 p.is_draft,COALESCE(p.platform_head_sha,''),COALESCE(p.head_branch,''),COALESCE(p.base_branch,''),COALESCE(p.head_repo_clone_url,''),p.head_repo_identity_stale,p.additions,p.deletions,p.files_changed,COALESCE(p.review_decision,''),COALESCE(p.ci_status,''),COALESCE(p.ci_checks_json,''),COALESCE(p.mergeable_state,''),
 p.assignees_json,p.reviewers_json,p.last_activity_at,p.closed_at,p.merged_at,p.ci_observed_at,p.review_decision_observed_at,p.mergeable_state_observed_at,
 EXISTS (SELECT 1 FROM forge_mr_events e WHERE e.merge_request_id=p.id AND p.state IN ('closed','merged') AND e.event_type='issue_comment' AND e.author != p.author AND e.author_association IS NULL)
 FROM forge_merge_requests p JOIN pulls selected ON selected.id=p.id
 UNION ALL
 SELECT i.id,i.repo_id,'issue',i.number,COALESCE(i.url,''),i.title,i.author,i.state,COALESCE(CAST(substr(CAST(COALESCE(i.body,'') AS BLOB),1,8193) AS TEXT),''),NULL,i.author_association,i.created_at,i.updated_at,i.detail_fetched_at,
 (SELECT json_group_array(l.name) FROM forge_issue_labels il JOIN forge_labels l ON l.id=il.label_id WHERE il.issue_id=i.id),
 0,'','','','',0,0,0,NULL,'','','','',
 i.assignees_json,'',i.last_activity_at,i.closed_at,NULL,NULL,NULL,NULL,0
 FROM forge_issues i JOIN issues selected ON selected.id=i.id
 ORDER BY 2,3,4`, string(ids), recentPulls, observedAt.Add(-30*24*time.Hour).UTC(), observedAt.UTC(), recentPulls, openIssuesOnly, start.UTC(), end.UTC())
	if err != nil {
		return nil, fmt.Errorf("load snapshot items: %w", err)
	}
	defer rows.Close()
	result := []ArchiveSnapshotItem{}
	for rows.Next() {
		var item ArchiveSnapshotItem
		if err := rows.Scan(&item.ID, &item.RepoID, &item.Kind, &item.Number, &item.URL, &item.Title, &item.Author, &item.State, &item.Body, &item.AuthorAccountType, &item.AuthorAssociation, &item.CreatedAt, &item.UpdatedAt, &item.DetailFetchedAt, &item.LabelsJSON, &item.Draft, &item.HeadSHA, &item.HeadBranch, &item.BaseBranch, &item.HeadRepoCloneURL, &item.HeadRepoIdentityStale, &item.Additions, &item.Deletions, &item.FilesChanged, &item.ReviewDecision, &item.CIStatus, &item.ChecksJSON, &item.MergeableState, &item.AssigneesJSON, &item.ReviewersJSON, &item.LastActivityAt, &item.ClosedAt, &item.MergedAt, &item.CIObservedAt, &item.ReviewDecisionObservedAt, &item.MergeableStateObservedAt, &item.CommentAuthorAssociationUnknown); err != nil {
			return nil, fmt.Errorf("scan snapshot item: %w", err)
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func LoadArchiveSnapshotReviews(ctx context.Context, tx *sql.Tx, mrIDs []int64) ([]ArchiveSnapshotReview, error) {
	ids, err := json.Marshal(mrIDs)
	if err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id,merge_request_id,author,COALESCE(CAST(substr(CAST(COALESCE(body,'') AS BLOB),1,2049) AS TEXT),''),COALESCE(summary,''),COALESCE(direct_url,''),dedupe_key,author_association,created_at FROM forge_mr_events WHERE merge_request_id IN (SELECT value FROM json_each(?)) AND event_type IN ('review','review_comment') ORDER BY merge_request_id,created_at,id`, string(ids))
	if err != nil {
		return nil, fmt.Errorf("load snapshot reviews: %w", err)
	}
	defer rows.Close()
	result := []ArchiveSnapshotReview{}
	for rows.Next() {
		var row ArchiveSnapshotReview
		if err := rows.Scan(&row.ID, &row.MergeRequestID, &row.Author, &row.Body, &row.State, &row.URL, &row.Key, &row.AuthorAssociation, &row.CreatedAt); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func LoadArchiveSnapshotReferences(ctx context.Context, tx *sql.Tx, repoIDs, mrIDs []int64) ([]ArchiveSnapshotReference, error) {
	ids, err := json.Marshal(mrIDs)
	if err != nil {
		return nil, err
	}
	repos, err := json.Marshal(repoIDs)
	if err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `WITH scoped_repos AS (SELECT value AS id FROM json_each(?)),
 pulls AS (SELECT id,repo_id,number FROM forge_merge_requests WHERE id IN (SELECT value FROM json_each(?))), `+archiveSnapshotLinks+`
 SELECT issue_id,merge_request_id,source_url,observed_event_key,observed_at FROM links ORDER BY merge_request_id,issue_id`, string(repos), string(ids))
	if err != nil {
		return nil, fmt.Errorf("load snapshot references: %w", err)
	}
	defer rows.Close()
	result := []ArchiveSnapshotReference{}
	for rows.Next() {
		var row ArchiveSnapshotReference
		if err := rows.Scan(&row.IssueID, &row.MergeRequestID, &row.URL, &row.EventKey, &row.ObservedAt); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

// LoadArchiveSnapshotPullEvidence reads comments and PR references within the selected pulls.
func LoadArchiveSnapshotPullEvidence(ctx context.Context, tx *sql.Tx, mrIDs []int64) ([]ArchiveSnapshotReview, []ArchiveSnapshotPullReference, error) {
	ids, err := json.Marshal(mrIDs)
	if err != nil {
		return nil, nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT e.id,e.merge_request_id,e.author,`+archiveSnapshotCommentBody+`,COALESCE(e.direct_url,''),e.dedupe_key,e.author_association,e.created_at FROM forge_mr_events e JOIN forge_merge_requests p ON p.id=e.merge_request_id WHERE p.id IN (SELECT value FROM json_each(?)) AND p.state IN ('closed','merged') AND e.event_type='issue_comment' AND `+archiveSnapshotCommentFilter+` ORDER BY e.merge_request_id,e.created_at,e.id`, string(ids))
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var comments []ArchiveSnapshotReview
	for rows.Next() {
		var c ArchiveSnapshotReview
		if err := rows.Scan(&c.ID, &c.MergeRequestID, &c.Author, &c.Body, &c.URL, &c.Key, &c.AuthorAssociation, &c.CreatedAt); err != nil {
			return nil, nil, err
		}
		comments = append(comments, c)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	refsRows, err := tx.QueryContext(ctx, `WITH pulls AS (SELECT id,repo_id,number FROM forge_merge_requests WHERE id IN (SELECT value FROM json_each(?))), `+archiveSnapshotPullLinks+` SELECT source_id,target_id,source_url,dedupe_key,created_at FROM pull_links ORDER BY target_id,source_id,dedupe_key`, string(ids))
	if err != nil {
		return nil, nil, err
	}
	defer refsRows.Close()
	var refs []ArchiveSnapshotPullReference
	for refsRows.Next() {
		var r ArchiveSnapshotPullReference
		if err := refsRows.Scan(&r.SourceID, &r.TargetID, &r.URL, &r.Key, &r.ObservedAt); err != nil {
			return nil, nil, err
		}
		refs = append(refs, r)
	}
	return comments, refs, refsRows.Err()
}

type ArchiveSnapshotPullReference struct {
	SourceID   int64
	TargetID   int64
	URL, Key   string
	ObservedAt time.Time
}
