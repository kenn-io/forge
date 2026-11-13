// Package snapshot defines the cached archive export contract.
package snapshot

import (
	"errors"
	"time"
	"uuid"

	"github.com/danielgtaylor/huma/v2"
	"go.kenn.io/forge/internal/archive/report"
)

const (
	Schema     = "kenn-forge-archive-snapshot/1"
	MaxBytes   = 32 << 20
	MaxRecords = 10_000
)

var ErrTooLarge = errors.New("cached archive snapshot exceeds the 10,000-record or 32 MiB text/response budget; narrow the repo scope; no items were dropped")

type ArchiveSnapshot struct {
	PullRequestScope string                `json:"pull_request_scope,omitempty" enum:"recently_closed"`
	IssueScope       string                `json:"issue_scope,omitempty" enum:"open" doc:"When open, includes all cached open issues and linked closed context regardless of creation time."`
	ExportSchema     string                `json:"schema"`
	ObservedAt       time.Time             `json:"observed_at"`
	Start            time.Time             `json:"start"`
	End              time.Time             `json:"end"`
	Repositories     []SnapshotRepository  `json:"repositories"`
	PullRequests     []SnapshotPullRequest `json:"pull_requests"`
	Issues           []SnapshotItem        `json:"issues"`
	Relations        []SnapshotRelation    `json:"relations"`
}

type SnapshotRepository struct {
	ID         string `json:"id"`
	Provider   string `json:"provider"`
	Host       string `json:"host"`
	ProviderID int64  `json:"provider_id"`
	// BitbucketRepositoryUUID is Bitbucket Cloud's repository key; Cloud
	// repositories have ProviderID 0.
	BitbucketRepositoryUUID uuid.UUID         `json:"bitbucket_repository_uuid,omitzero"`
	Path                    string            `json:"path"`
	DefaultBranch           string            `json:"default_branch"`
	LastSyncAt              *time.Time        `json:"last_sync_at"`
	SyncError               string            `json:"sync_error"`
	Coverage                *SnapshotCoverage `json:"coverage"`
}

type SnapshotItem struct {
	ID                string     `json:"id"`
	RepositoryID      string     `json:"repository_id"`
	Number            int        `json:"number"`
	URL               string     `json:"url"`
	Title             string     `json:"title"`
	Author            string     `json:"author"`
	AuthorAssociation *string    `json:"author_association"`
	Body              string     `json:"body"`
	BodyTruncated     bool       `json:"body_truncated"`
	State             string     `json:"state"`
	Labels            []string   `json:"labels"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
	DetailFetchedAt   *time.Time `json:"detail_fetched_at" doc:"Time of the latest completed detail fetch. May be cleared after incomplete refreshes; does not date every readiness fact."`
	// Assignees is omitted when the provider never reported the list, and
	// "[]" when it confirmed an empty one.
	Assignees      []string   `json:"assignees,omitzero"`
	LastActivityAt *time.Time `json:"last_activity_at"`
	// ClosedAt is omitted for open pull requests.
	ClosedAt *time.Time `json:"closed_at,omitzero"`
}

type SnapshotPullRequest struct {
	SnapshotItem
	LastAuthorAction     *SnapshotAction   `json:"last_author_action,omitempty"`
	LastMaintainerAction *SnapshotAction   `json:"last_maintainer_action,omitempty"`
	ClosedBy             *string           `json:"closed_by,omitempty"`
	Comments             []SnapshotComment `json:"comments,omitzero" doc:"For terminal pull requests, comments by the author or known human MEMBER, OWNER, and COLLABORATOR accounts."`
	Draft                bool              `json:"draft"`
	HeadSHA              string            `json:"head_sha"`
	HeadBranch           string            `json:"head_branch"`
	BaseBranch           string            `json:"base_branch"`
	HeadInSameRepository *bool             `json:"head_in_same_repository" doc:"Null when either repository identity is unavailable or the head identity is stale; false means a known different repository."`
	Additions            *int              `json:"additions"`
	Deletions            *int              `json:"deletions"`
	ChangedFiles         *int              `json:"changed_files"`
	ReviewState          string            `json:"review_state"`
	CheckState           string            `json:"check_state"`
	MergeableState       string            `json:"mergeable_state"`
	// RequestedReviewers is omitted when the provider never reported the
	// list, and "[]" when it confirmed an empty one.
	RequestedReviewers []string `json:"requested_reviewers,omitzero"`
	// MergeStatusObservedAt is a lower bound on when review, CI, and
	// mergeable state were last observed: the oldest of the observation
	// times the repository's provider can produce, null when any counted
	// time is unknown. Review decision counts only for providers that
	// report one (GitHub today).
	MergeStatusObservedAt *time.Time       `json:"merge_status_observed_at"`
	Checks                []SnapshotCheck  `json:"checks"`
	Reviews               []SnapshotReview `json:"reviews"`
	// Gaps includes merge_status_observation_time_unknown when
	// MergeStatusObservedAt is null.
	Gaps []string `json:"gaps" doc:"Includes comment_author_association_unknown when a comment lacks role evidence and its author cannot be identified as the pull request author."`
}

type SnapshotAction struct {
	Kind       string    `json:"kind"`
	Actor      string    `json:"actor"`
	OccurredAt time.Time `json:"occurred_at"`
}

type SnapshotCheck struct {
	Name       string `json:"name"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	URL        string `json:"url"`
}

type SnapshotReview struct {
	ID                string    `json:"id"`
	Author            string    `json:"author"`
	AuthorAssociation *string   `json:"author_association"`
	Body              string    `json:"body"`
	BodyTruncated     bool      `json:"body_truncated"`
	State             string    `json:"state"`
	URL               string    `json:"url"`
	CreatedAt         time.Time `json:"created_at"`
}

type SnapshotComment struct {
	ID                string    `json:"id"`
	Author            string    `json:"author"`
	AuthorAssociation *string   `json:"author_association"`
	Body              string    `json:"body" doc:"Full comment text within the snapshot byte budget."`
	BodyTruncated     bool      `json:"body_truncated"`
	URL               string    `json:"url"`
	CreatedAt         time.Time `json:"created_at"`
}

type SnapshotRelation struct {
	SourceID   string    `json:"source_id"`
	TargetID   string    `json:"target_id"`
	Kind       string    `json:"kind" doc:"pull_request_reference points from the mentioning pull request to the referenced pull request."`
	EvidenceID string    `json:"evidence_id"`
	URL        string    `json:"url"`
	ObservedAt time.Time `json:"observed_at"`
}

type SnapshotCoverage report.Coverage

// Coverage is unknown until archive state exists for the repository.
func (*SnapshotCoverage) TransformSchema(_ huma.Registry, schema *huma.Schema) *huma.Schema {
	schema.Nullable = true
	return schema
}
