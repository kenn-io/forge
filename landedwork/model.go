package landedwork

import (
	"context"
	"go.kenn.io/forge/platform"
	"time"
)

const Schema = "forge-landed-work/1"
const AnalyzerVersion = "forge-analyzer/1"

type GitSource interface {
	Commit(context.Context, string, *platform.Meter) (Commit, error)
	Introduced(context.Context, string, string, *platform.Meter) ([]string, error)
	Diff(context.Context, string, string, *platform.Meter) (TreeDiff, error)
	Attributes(context.Context, string, []string, *platform.Meter) (map[string]bool, error)
	Patch(context.Context, string, string, *platform.Meter) (Patch, error)
}
type Request struct {
	Snapshot      platform.LandingSnapshot `json:"snapshot"`
	DefaultBranch string                   `json:"default_branch"`
	BaseSHA       string                   `json:"base_sha"`
	HeadSHA       string                   `json:"head_sha"`
	Policy        string                   `json:"policy"`
}
type LineCounts struct {
	Additions int64 `json:"additions"`
	Deletions int64 `json:"deletions"`
}
type ExcludedFile struct {
	Path   platform.RawBytes `json:"path"`
	Side   string            `json:"side"`
	Reason string            `json:"reason"`
}
type Churn struct {
	Raw        *LineCounts    `json:"raw"`
	Code       *LineCounts    `json:"code"`
	Files      []FileChange   `json:"files"`
	Exclusions []ExcludedFile `json:"exclusions"`
}
type CommitEvidence struct {
	ID              string    `json:"id"`
	AuthorTime      time.Time `json:"author_time"`
	CommitterTime   time.Time `json:"committer_time"`
	Claims          []Claim   `json:"claims"`
	DeclaredReverts []string  `json:"declared_reverts"`
}
type Landing struct {
	Origin           string                     `json:"origin"`
	ChangeID         string                     `json:"change_id"`
	Method           platform.LandingMethod     `json:"method"`
	Parent           string                     `json:"parent"`
	Terminal         string                     `json:"terminal"`
	Spine            []string                   `json:"spine"`
	Introduced       []CommitEvidence           `json:"introduced"`
	Sources          []CommitEvidence           `json:"sources"`
	Claims           []Claim                    `json:"claims"`
	MergedAt         *time.Time                 `json:"merged_at"`
	Churn            Churn                      `json:"churn"`
	ProviderEvidence *platform.LandingCandidate `json:"provider_evidence"`
	TerminalCommit   CommitEvidence             `json:"terminal_commit"`
}
type Gap struct {
	FirstSHA string `json:"first_sha"`
	LastSHA  string `json:"last_sha"`
	Reason   string `json:"reason"`
	ChangeID string `json:"change_id"`
}
type Presence struct {
	ID      string `json:"id"`
	Present bool   `json:"present"`
}
type Result struct {
	Schema        string                      `json:"schema"`
	Analyzer      string                      `json:"analyzer"`
	Policy        string                      `json:"policy"`
	Repository    platform.RepositoryIdentity `json:"repository"`
	BaseSHA       string                      `json:"base_sha"`
	HeadSHA       string                      `json:"head_sha"`
	CertifiedHead string                      `json:"certified_head"`
	Complete      bool                        `json:"complete"`
	Diverged      bool                        `json:"diverged"`
	Landings      []Landing                   `json:"landings"`
	Gaps          []Gap                       `json:"gaps"`
	Presence      []Presence                  `json:"presence"`
	Digest        string                      `json:"digest"`
}
