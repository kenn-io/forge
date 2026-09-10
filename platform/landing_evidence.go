package platform

import (
	"context"
	"errors"
	"time"
)

// LandingChangeRef separates immutable REST identity from the mutable API route.
type LandingChangeRef struct {
	ID     int64
	Number int
	// TargetID is the observed base repository, not necessarily the queried
	// repository. Zero means the association did not establish its target.
	TargetID int64
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
	// SingleParentCorrespondence supplies evidence for all automatic alternatives,
	// not a historical squash/rebase label.
	SingleParentCorrespondence bool
	Reason                     string
	Sources                    LandingSourcePolicy
}

// AccountType is the provider-reported account classification, not authorship assurance.
type AccountType string

const (
	AccountTypeUser         AccountType = "user"
	AccountTypeBot          AccountType = "bot"
	AccountTypeOrganization AccountType = "organization"
	AccountTypeUnknown      AccountType = "unknown"
)

// Account is an observed account, not a complete identity on its own: the
// enclosing query supplies the provider kind and instance. Only positive IDs
// are usable identity claims; Login is display metadata. Absent and nonpositive
// IDs are retained without guessing. Type does not establish a human author.
type Account struct {
	ID    *int64
	Login *string
	Type  AccountType
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
	// Roles and UTC lifecycle times are observations, not landing proof.
	// Nil means unreported; complete landing coverage does not certify these fields.
	Author, Merger     *Account
	OpenedAt, MergedAt *time.Time
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
	ErrLandingIdentityMismatch = errors.New("landing identity mismatch")
)
