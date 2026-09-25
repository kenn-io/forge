// Package snapshot defines the cached archive export contract.
package snapshot

import (
	"errors"
	"time"

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
	ExportSchema string                `json:"schema"`
	ObservedAt   time.Time             `json:"observed_at"`
	Start        time.Time             `json:"start"`
	End          time.Time             `json:"end"`
	Repositories []SnapshotRepository  `json:"repositories"`
	PullRequests []SnapshotPullRequest `json:"pull_requests"`
	Issues       []SnapshotItem        `json:"issues"`
	Relations    []SnapshotRelation    `json:"relations"`
}

type SnapshotRepository struct {
	ID            string            `json:"id"`
	Provider      string            `json:"provider"`
	Host          string            `json:"host"`
	ProviderID    string            `json:"provider_id"`
	Path          string            `json:"path"`
	DefaultBranch string            `json:"default_branch"`
	LastSyncAt    *time.Time        `json:"last_sync_at"`
	SyncError     string            `json:"sync_error"`
	Coverage      *SnapshotCoverage `json:"coverage"`
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
}

type SnapshotPullRequest struct {
	SnapshotItem
	Draft                bool             `json:"draft"`
	HeadSHA              string           `json:"head_sha"`
	HeadBranch           string           `json:"head_branch"`
	BaseBranch           string           `json:"base_branch"`
	HeadInSameRepository *bool            `json:"head_in_same_repository" doc:"Null when either repository identity is unavailable or the head identity is stale; false means a known different repository."`
	Additions            *int             `json:"additions"`
	Deletions            *int             `json:"deletions"`
	ChangedFiles         *int             `json:"changed_files"`
	ReviewState          string           `json:"review_state"`
	CheckState           string           `json:"check_state"`
	MergeableState       string           `json:"mergeable_state"`
	Checks               []SnapshotCheck  `json:"checks"`
	Reviews              []SnapshotReview `json:"reviews"`
	Gaps                 []string         `json:"gaps"`
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

type SnapshotRelation struct {
	SourceID   string    `json:"source_id"`
	TargetID   string    `json:"target_id"`
	Kind       string    `json:"kind"`
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
