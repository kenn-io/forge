package platform

import (
	"context"
	"errors"
)

// LandingChangeRef separates immutable REST identity from the mutable API route.
type LandingChangeRef struct {
	ID     int64
	Number int
}

// LandingSourcePolicy describes the provider's source-list completeness proof.
// MaxCommits zero means no known endpoint cap, not an unlimited caller budget.
type LandingSourcePolicy struct {
	RequireCount bool
	MaxCommits   int64
}

// LandingEvidenceSupport is an endpoint contract, not the outcome of one sweep.
type LandingEvidenceSupport struct {
	Inventory, OrdinaryMerge bool
	Reason                   string
	Sources                  LandingSourcePolicy
}

// LandingChange preserves field absence independently of application projections.
// SourceID, when present, belongs to the same provider instance as TargetID.
// TerminalEvidence names a provider fact; it does not infer a merge method.
type LandingChange struct {
	Ref                             LandingChangeRef
	TargetID                        int64
	TargetBranch                    string
	SourceID                        *int64
	Merged                          *bool
	MergeSHA, SquashSHA, SourceHead *string
	SourceCount                     *int64
	Terminal, TerminalEvidence      string
}

// LandingEvidenceReader reads bounded pages for an exact prepared Git interval.
// Callers own credentials, admission, deadlines, and a wire-attempt/body limiter
// below authentication and SDK retries. One reader call may make multiple HTTP
// attempts. An empty exhausted association page must establish observed absence.
type LandingEvidenceReader interface {
	Provider
	RepositoryReader
	LandingEvidenceSupport() LandingEvidenceSupport
	ListLandingAssociations(ctx context.Context, repo RepoRef, commit, cursor string) (Page[LandingChangeRef], error)
	GetLandingChange(ctx context.Context, repo RepoRef, change LandingChangeRef) (LandingChange, error)
	ListLandingSource(ctx context.Context, repo RepoRef, change LandingChangeRef, cursor string) (Page[string], error)
}

var (
	ErrLandingAbsenceAmbiguous = errors.New("landing association absence unconfirmed")
	ErrLandingTransportLimit   = errors.New("landing transport limit exhausted")
)
