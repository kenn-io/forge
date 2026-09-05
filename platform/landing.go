package platform

import (
	"context"
	"time"
)

const LandingSnapshotSchema = "forge-landing-snapshot/1"

type RepositoryIdentity struct {
	Provider Kind   `json:"provider"`
	Instance string `json:"instance"`
	ID       string `json:"id"`
}

type LandingRepository struct {
	Identity      RepositoryIdentity `json:"identity"`
	Owner         string             `json:"owner"`
	Name          string             `json:"name"`
	DefaultBranch string             `json:"default_branch"`
	HeadSHA       string             `json:"head_sha"`
	CloneURL      string             `json:"clone_url"`
}

// LandingCapabilities report independently proven provider contracts. They are
// not inferred from which generic proof algorithms the analyzer implements.
type LandingCapabilities struct {
	OrdinaryMerge              bool `json:"ordinary_merge"`
	Squash                     bool `json:"squash"`
	RebaseRange                bool `json:"rebase_range"`
	FastForwardRange           bool `json:"fast_forward_range"`
	CompleteCandidateInventory bool `json:"complete_candidate_inventory"`
	AccountType                bool `json:"account_type"`
	TrustedRefUpdateTime       bool `json:"trusted_ref_update_time"`
}

type LandingMethod string

const (
	LandingMerge       LandingMethod = "merge"
	LandingSquash      LandingMethod = "squash"
	LandingRebase      LandingMethod = "rebase"
	LandingFastForward LandingMethod = "fast_forward"
)

// SHAField retains field presence separately from an empty/null observation.
type SHAField struct {
	Present bool   `json:"present"`
	Value   string `json:"value"`
}
type LandingSpan struct {
	FirstSHA string `json:"first_sha"`
	LastSHA  string `json:"last_sha"`
}

type LandingCandidate struct {
	ID               string              `json:"id"`
	Number           int                 `json:"number"`
	TargetBranch     string              `json:"target_branch"`
	SourceRepository *RepositoryIdentity `json:"source_repository"`
	BaseRepository   *RepositoryIdentity `json:"base_repository"`
	Author           *Account            `json:"author"`
	Merger           *Account            `json:"merger"`
	OpenedAt         *time.Time          `json:"opened_at"`
	MergedAt         *time.Time          `json:"merged_at"`
	MergeSHA         SHAField            `json:"merge_sha"`
	SquashSHA        SHAField            `json:"squash_sha"`
	SourceHeadSHA    SHAField            `json:"source_head_sha"`
	SourceCommits    []string            `json:"source_commits"`
	SourceComplete   bool                `json:"source_complete"`
	Method           LandingMethod       `json:"method"`
	MethodProof      string              `json:"method_proof"`
	TerminalProof    string              `json:"terminal_proof"`
	TerminalSHA      string              `json:"terminal_sha"`
	PossibleSpan     *LandingSpan        `json:"possible_span"`
	Additions        *int64              `json:"additions"`
	Deletions        *int64              `json:"deletions"`
	FilesChanged     *int64              `json:"files_changed"`
}

// LandingReader collects provider facts, not Git contents. Callers own
// admission and a deadline; exceeding a finite inventory leaves coverage open.
type LandingReader interface {
	LandingCapabilities() LandingCapabilities
	CollectLandingEvidence(context.Context, RepoRef, Budget) (LandingSnapshot, error)
}

type LandingCoverage struct {
	Complete   bool   `json:"complete"`
	Reason     string `json:"reason"`
	NextCursor string `json:"next_cursor"`
}
type LandingSnapshot struct {
	Schema       string              `json:"schema"`
	Repository   LandingRepository   `json:"repository"`
	Capabilities LandingCapabilities `json:"capabilities"`
	Coverage     LandingCoverage     `json:"coverage"`
	Candidates   []LandingCandidate  `json:"candidates"`
	StartedAt    time.Time           `json:"started_at"`
	CompletedAt  time.Time           `json:"completed_at"`
}
