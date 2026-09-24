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

func LoadArchiveSnapshotItems(ctx context.Context, tx *sql.Tx, repoIDs []int64, start, end time.Time) ([]ArchiveSnapshotItem, error) {
	ids, err := json.Marshal(repoIDs)
	if err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `
 WITH scoped_repos AS (SELECT value AS id FROM json_each(?)),
 pulls AS (
 SELECT p.* FROM forge_merge_requests p JOIN scoped_repos r ON r.id=p.repo_id
 WHERE p.state='open' AND NOT EXISTS (SELECT 1 FROM forge_archive_items a WHERE a.repo_id=p.repo_id AND a.item_type='merge_request' AND a.item_number=p.number AND a.lifecycle_state='removed_upstream')
 ),
 referenced AS (
 SELECT f.issue_id FROM forge_issue_pr_references f JOIN forge_repos r ON r.platform=f.source_provider AND r.platform_host=f.source_platform_host AND r.owner=f.source_owner AND r.name=f.source_repo
 JOIN pulls p ON p.repo_id=r.id AND p.number=f.source_number
 )
 SELECT p.id,p.repo_id,'pull_request',p.number,COALESCE(p.url,''),p.title,p.author,p.state,COALESCE(p.body,''),p.author_association,p.created_at,p.updated_at,p.detail_fetched_at,
 (SELECT json_group_array(l.name) FROM forge_merge_request_labels ml JOIN forge_labels l ON l.id=ml.label_id WHERE ml.merge_request_id=p.id),
 p.is_draft,COALESCE(p.platform_head_sha,''),COALESCE(p.head_branch,''),COALESCE(p.base_branch,''),COALESCE(p.head_repo_clone_url,''),p.head_repo_identity_stale,p.additions,p.deletions,p.files_changed,COALESCE(p.review_decision,''),COALESCE(p.ci_status,''),COALESCE(p.ci_checks_json,''),COALESCE(p.mergeable_state,'')
 FROM pulls p
 UNION ALL
 SELECT i.id,i.repo_id,'issue',i.number,COALESCE(i.url,''),i.title,i.author,i.state,COALESCE(i.body,''),i.author_association,i.created_at,i.updated_at,i.detail_fetched_at,
 (SELECT json_group_array(l.name) FROM forge_issue_labels il JOIN forge_labels l ON l.id=il.label_id WHERE il.issue_id=i.id),
 0,'','','','',0,0,0,NULL,'','','',''
 FROM forge_issues i JOIN scoped_repos r ON r.id=i.repo_id
 WHERE ((i.created_at>=? AND i.created_at<?) OR i.id IN (SELECT issue_id FROM referenced))
 AND NOT EXISTS (SELECT 1 FROM forge_archive_items a WHERE a.repo_id=i.repo_id AND a.item_type='issue' AND a.item_number=i.number AND a.lifecycle_state='removed_upstream')
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
	rows, err := tx.QueryContext(ctx, `SELECT id,merge_request_id,author,COALESCE(body,''),COALESCE(summary,''),COALESCE(direct_url,''),dedupe_key,author_association,created_at FROM forge_mr_events WHERE merge_request_id IN (SELECT value FROM json_each(?)) AND event_type IN ('review','review_comment') ORDER BY merge_request_id,created_at,id`, string(ids))
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
	rows, err := tx.QueryContext(ctx, `SELECT f.issue_id,p.id,f.source_url,f.observed_event_key,f.observed_at
 FROM forge_issue_pr_references f JOIN forge_repos r ON r.platform=f.source_provider AND r.platform_host=f.source_platform_host AND r.owner=f.source_owner AND r.name=f.source_repo
 JOIN forge_merge_requests p ON p.repo_id=r.id AND p.number=f.source_number WHERE p.id IN (SELECT value FROM json_each(?)) ORDER BY p.id,f.issue_id`, string(ids))
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
