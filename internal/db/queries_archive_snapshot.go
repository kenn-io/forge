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
	ReferenceIdentityUnknown                             bool
	BodyClass                                            int
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

// Snapshots use provider bot evidence; Activity intentionally uses broader login heuristics.
const (
	archiveSnapshotCommentBody   = `COALESCE(CAST(substr(CAST(COALESCE(e.body,'') AS BLOB),1,33554433) AS TEXT),'')`
	archiveSnapshotCommentFilter = `((p.author <> '' AND lower(e.author)=lower(p.author)) OR (upper(e.author_association) IN ('MEMBER','OWNER','COLLABORATOR') AND e.author<>'' AND NOT ` + archiveSnapshotKnownBot + `))`
	archiveSnapshotKnownBot      = `(substr(lower(e.author),-5)='[bot]' OR lower(COALESCE(json_extract(` + archiveSnapshotMetadata + `,'$.actor_account_type'),''))='bot')`
	archiveSnapshotHumanActor    = `(e.author<>'' AND NOT ` + archiveSnapshotKnownBot + ` AND lower(COALESCE(json_extract(` + archiveSnapshotMetadata + `,'$.actor_account_type'),'')) IN ('user','organization'))`
	archiveSnapshotMetadata      = `CASE WHEN json_valid(e.metadata_json) THEN e.metadata_json ELSE '{}' END`
)

// All three snapshot queries share the root-anchored pull and relation set.
const archiveSnapshotPullScope = `WITH scoped_repos AS (SELECT value AS id FROM json_each(?)),
 roots AS (
 SELECT p.id,p.repo_id,p.number FROM forge_merge_requests p JOIN scoped_repos r ON r.id=p.repo_id
 WHERE (p.state='open' OR (? AND p.state IN ('closed','merged') AND COALESCE(p.merged_at,p.closed_at)>=? AND COALESCE(p.merged_at,p.closed_at)<=?)) AND NOT EXISTS (SELECT 1 FROM forge_archive_items a WHERE a.repo_id=p.repo_id AND a.item_type='merge_request' AND a.item_number=p.number AND a.lifecycle_state='removed_upstream')
 ), reference_events AS MATERIALIZED (
 SELECT e.*, CASE WHEN json_valid(e.metadata_json) THEN e.metadata_json ELSE '{}' END AS metadata FROM forge_mr_events e WHERE e.event_type='cross_referenced'
 ), reference_candidates AS MATERIALIZED (
 SELECT source.id AS source_id, target.id AS target_id, target.repo_id, target.number AS target_number, json_extract(e.metadata,'$.source_number') AS source_number,
 CASE WHEN json_extract(e.metadata,'$.is_cross_repository')=0 THEN 0
 WHEN r.platform='github' AND json_type(e.metadata,'$.source_repo_id')='integer' AND json_extract(e.metadata,'$.source_repo_id')>0 THEN json_extract(e.metadata,'$.source_repo_id')
 ELSE NULL END AS source_provider_id, e.dedupe_key,
 COALESCE(json_extract(e.metadata,'$.source_url'),'') AS source_url, e.created_at
 FROM reference_events e
 JOIN forge_merge_requests target ON target.id=e.merge_request_id AND target.repo_id=e.repo_id
 JOIN forge_repos r ON r.id=target.repo_id
 LEFT JOIN forge_repos sr ON sr.platform=r.platform AND sr.platform_host=r.platform_host AND sr.platform_repo_id=json_extract(e.metadata,'$.source_repo_id') AND sr.platform_repo_id>0
 LEFT JOIN forge_merge_requests source ON source.repo_id=CASE WHEN json_extract(e.metadata,'$.is_cross_repository')=0 THEN target.repo_id ELSE sr.id END AND source.number=json_extract(e.metadata,'$.source_number')
 WHERE json_extract(e.metadata,'$.source_type')='PullRequest'
 AND json_type(e.metadata,'$.source_number')='integer'
 AND json_extract(e.metadata,'$.source_number')>0
 AND NOT EXISTS (SELECT 1 FROM forge_archive_items a WHERE a.repo_id=target.repo_id AND a.item_type='merge_request' AND a.item_number=target.number AND a.lifecycle_state='removed_upstream')
 AND NOT EXISTS (SELECT 1 FROM forge_archive_items a WHERE a.repo_id=source.repo_id AND a.item_type='merge_request' AND a.item_number=json_extract(e.metadata,'$.source_number') AND a.lifecycle_state='removed_upstream')
 ), pull_links AS (
 SELECT * FROM reference_candidates c WHERE ? AND c.source_provider_id IS NOT NULL AND (c.source_id IN (SELECT id FROM roots) OR c.target_id IN (SELECT id FROM roots))
 ), pulls AS (
 SELECT roots.*, CASE
 WHEN (p.author<>'' AND (substr(lower(p.author),-5)='[bot]' OR lower(COALESCE(p.author_account_type,''))='bot')) OR upper(p.author_association) IN ('MEMBER','OWNER') THEN 0
 WHEN p.author='' OR p.author_association IS NULL OR p.author_association='' OR lower(COALESCE(p.author_account_type,'')) NOT IN ('user','organization') THEN 2
 ELSE 1 END AS body_class
 FROM roots JOIN forge_merge_requests p ON p.id=roots.id
 )`

const archiveSnapshotScope = archiveSnapshotPullScope + `, ` + archiveSnapshotLinks + `,
 issues AS (
 SELECT i.id FROM forge_issues i JOIN scoped_repos r ON r.id=i.repo_id
 WHERE ((CASE WHEN ? THEN i.state='open' ELSE i.created_at>=? AND i.created_at<? END) OR i.id IN (SELECT issue_id FROM links))
 AND NOT EXISTS (SELECT 1 FROM forge_archive_items a WHERE a.repo_id=i.repo_id AND a.item_type='issue' AND a.item_number=i.number AND a.lifecycle_state='removed_upstream')
 )`

const archiveSnapshotActivity = `activity_events AS MATERIALIZED (
 SELECT e.id,e.repo_id,e.merge_request_id,e.author,e.created_at,p.author AS pull_author,NULLIF(p.author_association,'') AS pull_role,e.author_association,
 CASE e.event_type WHEN 'issue_comment' THEN 'comment' WHEN 'commit' THEN 'push' ELSE e.event_type END AS kind,
 CASE WHEN lower(e.author)=lower(p.author) AND lower(COALESCE(p.author_account_type,''))='bot' THEN 0 WHEN ` + archiveSnapshotHumanActor + ` THEN 1 WHEN ` + archiveSnapshotKnownBot + ` THEN 0 ELSE -1 END AS human
 FROM forge_mr_events e JOIN pulls selected ON selected.id=e.merge_request_id JOIN forge_merge_requests p ON p.id=selected.id
 WHERE e.event_type IN ('issue_comment','review','review_comment','assigned','commit','force_push')
 AND (e.event_type<>'commit' OR (p.platform_head_sha<>'' AND (e.summary=p.platform_head_sha OR e.platform_external_id=p.platform_head_sha)))
 ), actor_roles AS MATERIALIZED (
 SELECT repo_id,author,role FROM (
 SELECT r.repo_id,lower(r.author) AS author,r.author_association AS role,
 ROW_NUMBER() OVER (PARTITION BY r.repo_id,lower(r.author) ORDER BY r.created_at DESC,r.id DESC) AS rank
 FROM forge_mr_events r
 WHERE r.repo_id IN (SELECT DISTINCT repo_id FROM activity_events WHERE human=1 AND COALESCE(author_association,'')='') AND r.author_association<>''
 ) WHERE rank=1
 ), activity_candidates AS (
 SELECT e.*,COALESCE(NULLIF(e.author_association,''),r.role,CASE WHEN lower(e.author)=lower(e.pull_author) THEN e.pull_role END) AS role
 FROM activity_events e LEFT JOIN actor_roles r ON r.repo_id=e.repo_id AND r.author=lower(e.author)
 ), classified_activity AS (
 SELECT *, 'author' AS class FROM activity_candidates WHERE human=1 AND author<>'' AND lower(author)=lower(pull_author)
 UNION ALL SELECT *, 'maintainer' FROM activity_candidates WHERE human=1 AND (upper(role) IN ('MEMBER','OWNER') OR (upper(role)='COLLABORATOR' AND lower(author)<>lower(pull_author)))
 UNION ALL SELECT *, 'unknown' FROM activity_candidates WHERE human=-1 OR (human=1 AND (role IS NULL OR role=''))
 ), activity AS (
 SELECT * FROM (SELECT *,row_number() OVER (PARTITION BY merge_request_id,class ORDER BY created_at DESC,id DESC) AS rank FROM classified_activity) WHERE rank=1
 )`

const archiveSnapshotDefaultScope = `WITH scoped_repos AS (SELECT value AS id FROM json_each(?)),
 pulls AS (
 SELECT p.id,p.repo_id,p.number FROM forge_merge_requests p JOIN scoped_repos r ON r.id=p.repo_id
 WHERE p.state='open' AND NOT EXISTS (SELECT 1 FROM forge_archive_items a WHERE a.repo_id=p.repo_id AND a.item_type='merge_request' AND a.item_number=p.number AND a.lifecycle_state='removed_upstream')
 ), ` + archiveSnapshotLinks + `,
 issues AS (
 SELECT i.id FROM forge_issues i JOIN scoped_repos r ON r.id=i.repo_id
 WHERE ((CASE WHEN ? THEN i.state='open' ELSE i.created_at>=? AND i.created_at<? END) OR i.id IN (SELECT issue_id FROM links))
 AND NOT EXISTS (SELECT 1 FROM forge_archive_items a WHERE a.repo_id=i.repo_id AND a.item_type='issue' AND a.item_number=i.number AND a.lifecycle_state='removed_upstream')
 )`

// The same expression limits both the preflight measurement and the loaded text.
const archiveSnapshotBody = `COALESCE(CAST(substr(CAST(COALESCE(p.body,'') AS BLOB),1,CASE WHEN ? AND selected.body_class=1 THEN 33554433 ELSE 8193 END) AS TEXT),'')`

// MeasureArchiveSnapshot bounds records and projected text without loading bodies
// into Go. Include all variable text, including checks, labels and review metadata.
func MeasureArchiveSnapshot(ctx context.Context, tx *sql.Tx, repoIDs []int64, start, end time.Time, openIssuesOnly, recentPulls bool, observedAt time.Time) (ArchiveReportMeasurement, error) {
	ids, err := json.Marshal(repoIDs)
	if err != nil {
		return ArchiveReportMeasurement{}, err
	}
	var result ArchiveReportMeasurement
	scope, body, extra := archiveSnapshotDefaultScope, `COALESCE(CAST(substr(CAST(COALESCE(p.body,'') AS BLOB),1,8193) AS TEXT),'')`, ""
	args := []any{string(ids), openIssuesOnly, start.UTC(), end.UTC()}
	if recentPulls {
		scope, body = archiveSnapshotScope+`, `+archiveSnapshotActivity, archiveSnapshotBody
		args = []any{string(ids), true, observedAt.Add(-30 * 24 * time.Hour).UTC(), observedAt.UTC(), true, openIssuesOnly, start.UTC(), end.UTC(), true}
		extra = `
 UNION ALL
 SELECT length(CAST(json_array(e.author,e.author_association,` + archiveSnapshotCommentBody + `,e.direct_url,e.dedupe_key) AS BLOB))
 FROM forge_mr_events e JOIN pulls selected ON selected.id=e.merge_request_id JOIN forge_merge_requests p ON p.id=selected.id
 WHERE ? AND p.state IN ('closed','merged') AND e.event_type='issue_comment' AND ` + archiveSnapshotCommentFilter + `
 UNION ALL
 SELECT length(CAST(json_array(source_url,dedupe_key) AS BLOB)) FROM pull_links WHERE ?
 UNION ALL
 SELECT length(CAST(json_array(kind,author,author_association) AS BLOB)) FROM activity WHERE ? AND class<>'unknown'`
		args = append(args, true, true, true)
	}
	err = tx.QueryRowContext(ctx, scope+`, texts AS (
 SELECT length(CAST(json_array(p.url,p.title,p.author,p.author_association,p.state,
 `+body+`,p.platform_head_sha,p.head_branch,p.base_branch,p.head_repo_clone_url,
 p.review_decision,p.ci_status,p.ci_checks_json,p.mergeable_state,p.assignees_json,p.reviewers_json,
 (SELECT json_group_array(l.name) FROM forge_merge_request_labels ml JOIN forge_labels l ON l.id=ml.label_id WHERE ml.merge_request_id=p.id)) AS BLOB)) AS bytes
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
`+extra+`
 ) SELECT count(*),COALESCE(sum(bytes),0) FROM texts`, args...).Scan(&result.Records, &result.TextBytes)
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
	scope, body, authorType, extra := archiveSnapshotDefaultScope, `COALESCE(CAST(substr(CAST(COALESCE(p.body,'') AS BLOB),1,8193) AS TEXT),'')`, `NULL`, `NULL,0,0,0`
	args := []any{string(ids), openIssuesOnly, start.UTC(), end.UTC()}
	if recentPulls {
		scope, body, authorType = archiveSnapshotScope, archiveSnapshotBody, `p.author_account_type`
		args = []any{string(ids), true, observedAt.Add(-30 * 24 * time.Hour).UTC(), observedAt.UTC(), true, openIssuesOnly, start.UTC(), end.UTC(), true}
		extra = `p.merged_at,
 EXISTS (SELECT 1 FROM forge_mr_events e WHERE e.merge_request_id=p.id AND p.state IN ('closed','merged') AND e.event_type='issue_comment' AND (p.author = '' OR lower(e.author) != lower(p.author)) AND (e.author_association IS NULL OR e.author_association='')),
 EXISTS (SELECT 1 FROM reference_candidates c WHERE c.target_id=p.id AND c.source_provider_id IS NULL),selected.body_class`
	}
	rows, err := tx.QueryContext(ctx, scope+`
 SELECT p.id,p.repo_id,'pull_request',p.number,COALESCE(p.url,''),p.title,p.author,p.state,`+body+`,`+authorType+`,p.author_association,p.created_at,p.updated_at,p.detail_fetched_at,
 (SELECT json_group_array(l.name) FROM forge_merge_request_labels ml JOIN forge_labels l ON l.id=ml.label_id WHERE ml.merge_request_id=p.id),
 p.is_draft,COALESCE(p.platform_head_sha,''),COALESCE(p.head_branch,''),COALESCE(p.base_branch,''),COALESCE(p.head_repo_clone_url,''),p.head_repo_identity_stale,p.additions,p.deletions,p.files_changed,COALESCE(p.review_decision,''),COALESCE(p.ci_status,''),COALESCE(p.ci_checks_json,''),COALESCE(p.mergeable_state,''),
 p.assignees_json,p.reviewers_json,p.last_activity_at,p.closed_at,p.ci_observed_at,p.review_decision_observed_at,p.mergeable_state_observed_at,`+extra+`
 FROM forge_merge_requests p JOIN pulls selected ON selected.id=p.id
 UNION ALL
 SELECT i.id,i.repo_id,'issue',i.number,COALESCE(i.url,''),i.title,i.author,i.state,COALESCE(CAST(substr(CAST(COALESCE(i.body,'') AS BLOB),1,8193) AS TEXT),''),NULL,i.author_association,i.created_at,i.updated_at,i.detail_fetched_at,
 (SELECT json_group_array(l.name) FROM forge_issue_labels il JOIN forge_labels l ON l.id=il.label_id WHERE il.issue_id=i.id),
 0,'','','','',0,0,0,NULL,'','','','',
 i.assignees_json,'',i.last_activity_at,i.closed_at,NULL,NULL,NULL,NULL,0,0,0
 FROM forge_issues i JOIN issues selected ON selected.id=i.id
 ORDER BY 2,3,4`, args...)
	if err != nil {
		return nil, fmt.Errorf("load snapshot items: %w", err)
	}
	defer rows.Close()
	result := []ArchiveSnapshotItem{}
	for rows.Next() {
		var item ArchiveSnapshotItem
		if err := rows.Scan(&item.ID, &item.RepoID, &item.Kind, &item.Number, &item.URL, &item.Title, &item.Author, &item.State, &item.Body, &item.AuthorAccountType, &item.AuthorAssociation, &item.CreatedAt, &item.UpdatedAt, &item.DetailFetchedAt, &item.LabelsJSON, &item.Draft, &item.HeadSHA, &item.HeadBranch, &item.BaseBranch, &item.HeadRepoCloneURL, &item.HeadRepoIdentityStale, &item.Additions, &item.Deletions, &item.FilesChanged, &item.ReviewDecision, &item.CIStatus, &item.ChecksJSON, &item.MergeableState, &item.AssigneesJSON, &item.ReviewersJSON, &item.LastActivityAt, &item.ClosedAt, &item.CIObservedAt, &item.ReviewDecisionObservedAt, &item.MergeableStateObservedAt, &item.MergedAt, &item.CommentAuthorAssociationUnknown, &item.ReferenceIdentityUnknown, &item.BodyClass); err != nil {
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
func LoadArchiveSnapshotPullEvidence(ctx context.Context, tx *sql.Tx, repoIDs, mrIDs []int64, observedAt time.Time) ([]ArchiveSnapshotReview, []ArchiveSnapshotPullReference, error) {
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
	repos, err := json.Marshal(repoIDs)
	if err != nil {
		return nil, nil, err
	}
	refsRows, err := tx.QueryContext(ctx, archiveSnapshotPullScope+` SELECT l.repo_id,l.source_provider_id,l.source_number,l.target_number,l.source_url,l.dedupe_key,l.created_at,r.platform,r.platform_host,r.platform_repo_id,r.bitbucket_repository_uuid FROM pull_links l JOIN forge_repos r ON r.id=l.repo_id ORDER BY l.target_id,l.source_id,l.dedupe_key`, string(repos), true, observedAt.Add(-30*24*time.Hour).UTC(), observedAt.UTC(), true)
	if err != nil {
		return nil, nil, err
	}
	defer refsRows.Close()
	var refs []ArchiveSnapshotPullReference
	for refsRows.Next() {
		var r ArchiveSnapshotPullReference
		keyID, keyUUID := repositoryKeyColumns(&r.TargetRepo.Key)
		if err := refsRows.Scan(&r.RepoID, &r.SourceProviderID, &r.SourceNumber, &r.TargetNumber, &r.URL, &r.Key, &r.ObservedAt, &r.TargetRepo.Platform, &r.TargetRepo.PlatformHost, keyID, keyUUID); err != nil {
			return nil, nil, err
		}
		refs = append(refs, r)
	}
	return comments, refs, refsRows.Err()
}

type ArchiveSnapshotPullReference struct {
	TargetRepo                 Repo
	RepoID, SourceProviderID   int64
	SourceNumber, TargetNumber int
	URL, Key                   string
	ObservedAt                 time.Time
}

type ArchiveSnapshotActivity struct {
	ID, MergeRequestID int64
	Kind, Actor, Class string
	OccurredAt         time.Time
}

func LoadArchiveSnapshotActivity(ctx context.Context, tx *sql.Tx, mrIDs []int64) ([]ArchiveSnapshotActivity, error) {
	ids, err := json.Marshal(mrIDs)
	if err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `WITH pulls AS (SELECT id,repo_id,number FROM forge_merge_requests WHERE id IN (SELECT value FROM json_each(?))), `+archiveSnapshotActivity+` SELECT id,merge_request_id,kind,author,created_at,class FROM activity ORDER BY merge_request_id,CASE class WHEN 'unknown' THEN 1 ELSE 0 END,created_at,id`, string(ids))
	if err != nil {
		return nil, fmt.Errorf("load snapshot activity: %w", err)
	}
	defer rows.Close()
	var result []ArchiveSnapshotActivity
	for rows.Next() {
		var a ArchiveSnapshotActivity
		if err := rows.Scan(&a.ID, &a.MergeRequestID, &a.Kind, &a.Actor, &a.OccurredAt, &a.Class); err != nil {
			return nil, err
		}
		result = append(result, a)
	}
	return result, rows.Err()
}

func LoadArchiveSnapshotLifecycle(ctx context.Context, tx *sql.Tx, mrIDs []int64) ([]ArchiveSnapshotActivity, error) {
	ids, err := json.Marshal(mrIDs)
	if err != nil {
		return nil, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT merge_request_id,event_type,CASE WHEN e.author<>'' AND substr(lower(e.author),-5)<>'[bot]' AND lower(COALESCE(json_extract(`+archiveSnapshotMetadata+`,'$.actor_account_type'),''))='bot' THEN e.author||'[bot]' ELSE e.author END,created_at FROM forge_mr_events e WHERE merge_request_id IN (SELECT value FROM json_each(?)) AND event_type IN ('closed','merged','reopened') ORDER BY created_at,id`, string(ids))
	if err != nil {
		return nil, fmt.Errorf("load snapshot lifecycle: %w", err)
	}
	defer rows.Close()
	var result []ArchiveSnapshotActivity
	for rows.Next() {
		var a ArchiveSnapshotActivity
		if err := rows.Scan(&a.MergeRequestID, &a.Kind, &a.Actor, &a.OccurredAt); err != nil {
			return nil, err
		}
		result = append(result, a)
	}
	return result, rows.Err()
}
