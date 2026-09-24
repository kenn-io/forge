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
	AuthorAssociation                                    *string
	CreatedAt, UpdatedAt                                 time.Time
	DetailFetchedAt                                      *time.Time
	LabelsJSON                                           string
	Draft                                                bool
	HeadRepoIdentityStale                                bool
	HeadSHA, HeadBranch, BaseBranch, HeadRepoCloneURL    string
	Additions, Deletions                                 int
	FilesChanged                                         *int
	ReviewDecision, CIStatus, ChecksJSON, MergeableState string
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
	Resolved                bool
}

func LoadArchiveSnapshotRepository(ctx context.Context, tx *sql.Tx, identity RepoIdentity) (*Repo, error) {
	identity = canonicalRepoIdentity(identity)
	var repo Repo
	err := tx.QueryRowContext(ctx, `SELECT id,platform,platform_host,platform_repo_id,owner,name,repo_path,web_url,clone_url,default_branch,last_sync_completed_at,COALESCE(last_sync_error,'')
 FROM forge_repos WHERE lifecycle_state='active' AND platform=? AND platform_host=?
 AND ((?<>'' AND platform_repo_id=?) OR (?='' AND repo_path_key=?))`, identity.Platform, identity.PlatformHost, identity.PlatformRepoID, identity.PlatformRepoID, identity.PlatformRepoID, identity.RepoPathKey).Scan(&repo.ID, &repo.Platform, &repo.PlatformHost, &repo.PlatformRepoID, &repo.Owner, &repo.Name, &repo.RepoPath, &repo.WebURL, &repo.CloneURL, &repo.DefaultBranch, &repo.LastSyncCompletedAt, &repo.LastSyncError)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load snapshot repository: %w", err)
	}
	return &repo, nil
}

// Historical routes can recover renamed references only when one catalog identity
// has ever owned the route. Reuse is ambiguous even if only one owner is active.
const archiveSnapshotLinks = `link_candidates AS (
 SELECT f.*, p.id AS merge_request_id, route.is_current AS current_route,
 NOT EXISTS (SELECT 1 FROM forge_repo_routes other
   WHERE other.platform=route.platform AND other.platform_host=route.platform_host
   AND other.repo_path_key=route.repo_path_key AND other.repo_id<>route.repo_id) AS resolved
 FROM forge_issue_pr_references f
 JOIN forge_repo_routes route ON route.platform=f.source_provider
   AND route.platform_host=f.source_platform_host
   AND route.repo_path_key=lower(f.source_owner || '/' || f.source_repo)
 JOIN pulls p ON p.repo_id=route.repo_id AND p.number=f.source_number
), links AS (
 SELECT * FROM (
 SELECT *, ROW_NUMBER() OVER (PARTITION BY issue_id,merge_request_id,observed_event_key
 ORDER BY resolved DESC,current_route DESC,observed_at DESC,source_url) AS reference_rank
 FROM link_candidates
 ) WHERE reference_rank=1
)`

// Reuse exactly the same candidate set for preflight and materialization.
const archiveSnapshotScope = `WITH scoped_repos AS (SELECT value AS id FROM json_each(?)),
 pulls AS (
 SELECT p.id,p.repo_id,p.number FROM forge_merge_requests p JOIN scoped_repos r ON r.id=p.repo_id
 WHERE p.state='open' AND NOT EXISTS (SELECT 1 FROM forge_archive_items a WHERE a.repo_id=p.repo_id AND a.item_type='merge_request' AND a.item_number=p.number AND a.lifecycle_state='removed_upstream')
 ), ` + archiveSnapshotLinks + `,
 issues AS (
 SELECT i.id FROM forge_issues i JOIN scoped_repos r ON r.id=i.repo_id
 WHERE ((i.created_at>=? AND i.created_at<?) OR i.id IN (SELECT issue_id FROM links))
 AND NOT EXISTS (SELECT 1 FROM forge_archive_items a WHERE a.repo_id=i.repo_id AND a.item_type='issue' AND a.item_number=i.number AND a.lifecycle_state='removed_upstream')
 )`

// MeasureArchiveSnapshot bounds records and projected text without loading bodies
// into Go. Include all variable text, including checks, labels and review metadata.
func MeasureArchiveSnapshot(ctx context.Context, tx *sql.Tx, repoIDs []int64, start, end time.Time) (ArchiveReportMeasurement, error) {
	ids, err := json.Marshal(repoIDs)
	if err != nil {
		return ArchiveReportMeasurement{}, err
	}
	var result ArchiveReportMeasurement
	err = tx.QueryRowContext(ctx, archiveSnapshotScope+`, texts AS (
 SELECT length(CAST(json_array(p.url,p.title,p.author,p.author_association,p.state,
 COALESCE(CAST(substr(CAST(COALESCE(p.body,'') AS BLOB),1,8193) AS TEXT),''),p.platform_head_sha,p.head_branch,p.base_branch,p.head_repo_clone_url,
 p.review_decision,p.ci_status,p.ci_checks_json,p.mergeable_state,
 (SELECT json_group_array(l.name) FROM forge_merge_request_labels ml JOIN forge_labels l ON l.id=ml.label_id WHERE ml.merge_request_id=p.id)) AS BLOB)) AS bytes
 FROM forge_merge_requests p JOIN pulls selected ON selected.id=p.id
 UNION ALL
 SELECT length(CAST(json_array(i.url,i.title,i.author,i.author_association,i.state,COALESCE(CAST(substr(CAST(COALESCE(i.body,'') AS BLOB),1,8193) AS TEXT),''),
 (SELECT json_group_array(l.name) FROM forge_issue_labels il JOIN forge_labels l ON l.id=il.label_id WHERE il.issue_id=i.id)) AS BLOB))
 FROM forge_issues i JOIN issues selected ON selected.id=i.id
 UNION ALL
 SELECT length(CAST(json_array(e.author,e.author_association,COALESCE(CAST(substr(CAST(COALESCE(e.body,'') AS BLOB),1,2049) AS TEXT),''),e.summary,e.direct_url,e.dedupe_key) AS BLOB))
 FROM forge_mr_events e JOIN pulls p ON p.id=e.merge_request_id WHERE e.event_type IN ('review','review_comment')
 UNION ALL
 SELECT length(CAST(json_array(source_url,observed_event_key) AS BLOB)) FROM links
 ) SELECT count(*),COALESCE(sum(bytes),0) FROM texts`, string(ids), start.UTC(), end.UTC()).Scan(&result.Records, &result.TextBytes)
	if err != nil {
		return result, fmt.Errorf("measure archive snapshot: %w", err)
	}
	return result, nil
}

func LoadArchiveSnapshotItems(ctx context.Context, tx *sql.Tx, repoIDs []int64, start, end time.Time) ([]ArchiveSnapshotItem, error) {
	ids, err := json.Marshal(repoIDs)
	if err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, archiveSnapshotScope+`
 SELECT p.id,p.repo_id,'pull_request',p.number,COALESCE(p.url,''),p.title,p.author,p.state,COALESCE(CAST(substr(CAST(COALESCE(p.body,'') AS BLOB),1,8193) AS TEXT),''),p.author_association,p.created_at,p.updated_at,p.detail_fetched_at,
 (SELECT json_group_array(l.name) FROM forge_merge_request_labels ml JOIN forge_labels l ON l.id=ml.label_id WHERE ml.merge_request_id=p.id),
 p.is_draft,COALESCE(p.platform_head_sha,''),COALESCE(p.head_branch,''),COALESCE(p.base_branch,''),COALESCE(p.head_repo_clone_url,''),p.head_repo_identity_stale,p.additions,p.deletions,p.files_changed,COALESCE(p.review_decision,''),COALESCE(p.ci_status,''),COALESCE(p.ci_checks_json,''),COALESCE(p.mergeable_state,'')
 FROM forge_merge_requests p JOIN pulls selected ON selected.id=p.id
 UNION ALL
 SELECT i.id,i.repo_id,'issue',i.number,COALESCE(i.url,''),i.title,i.author,i.state,COALESCE(CAST(substr(CAST(COALESCE(i.body,'') AS BLOB),1,8193) AS TEXT),''),i.author_association,i.created_at,i.updated_at,i.detail_fetched_at,
 (SELECT json_group_array(l.name) FROM forge_issue_labels il JOIN forge_labels l ON l.id=il.label_id WHERE il.issue_id=i.id),
 0,'','','','',0,0,0,NULL,'','','',''
 FROM forge_issues i JOIN issues selected ON selected.id=i.id
 ORDER BY 2,3,4`, string(ids), start.UTC(), end.UTC())
	if err != nil {
		return nil, fmt.Errorf("load snapshot items: %w", err)
	}
	defer rows.Close()
	result := []ArchiveSnapshotItem{}
	for rows.Next() {
		var item ArchiveSnapshotItem
		if err := rows.Scan(&item.ID, &item.RepoID, &item.Kind, &item.Number, &item.URL, &item.Title, &item.Author, &item.State, &item.Body, &item.AuthorAssociation, &item.CreatedAt, &item.UpdatedAt, &item.DetailFetchedAt, &item.LabelsJSON, &item.Draft, &item.HeadSHA, &item.HeadBranch, &item.BaseBranch, &item.HeadRepoCloneURL, &item.HeadRepoIdentityStale, &item.Additions, &item.Deletions, &item.FilesChanged, &item.ReviewDecision, &item.CIStatus, &item.ChecksJSON, &item.MergeableState); err != nil {
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

func LoadArchiveSnapshotReferences(ctx context.Context, tx *sql.Tx, mrIDs []int64) ([]ArchiveSnapshotReference, error) {
	ids, err := json.Marshal(mrIDs)
	if err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `WITH pulls AS (SELECT id,repo_id,number FROM forge_merge_requests WHERE id IN (SELECT value FROM json_each(?))), `+archiveSnapshotLinks+`
 SELECT issue_id,merge_request_id,source_url,observed_event_key,observed_at,resolved FROM links ORDER BY merge_request_id,issue_id`, string(ids))
	if err != nil {
		return nil, fmt.Errorf("load snapshot references: %w", err)
	}
	defer rows.Close()
	result := []ArchiveSnapshotReference{}
	for rows.Next() {
		var row ArchiveSnapshotReference
		if err := rows.Scan(&row.IssueID, &row.MergeRequestID, &row.URL, &row.EventKey, &row.ObservedAt, &row.Resolved); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}
