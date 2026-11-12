package github

import (
	"fmt"
	"strings"
	"time"

	"go.kenn.io/forge/platform"
)

// SyncErrorCode identifies a sync failure that clients can recover from
// without parsing LastError.
type SyncErrorCode string

const (
	SyncErrorCodeLocalCeilingExhausted SyncErrorCode = "localSyncCeilingExhausted"
)

// SyncStatus holds the current state of the sync engine.
type SyncStatus struct {
	Relay *RelayStatus `json:"relay,omitempty"`
	// GitAccess lists repositories whose clone-store Git currently fails
	// for a credential reason.
	GitAccess []GitAccessProblem `json:"git_access,omitempty"`
	// RepoErrors lists repositories whose last attempted sync failed.
	RepoErrors          []RepoSyncProblem `json:"repo_errors,omitempty"`
	Running             bool              `json:"running"`
	CurrentRepo         string            `json:"current_repo,omitempty"`
	Progress            string            `json:"progress,omitempty"`
	LastRunAt           time.Time         `json:"last_run_at,omitzero"`
	LastError           string            `json:"last_error,omitempty"`
	LastErrorCode       SyncErrorCode     `json:"last_error_code,omitempty" enum:"localSyncCeilingExhausted"`
	LastErrorCeilingKey string            `json:"last_error_ceiling_key,omitempty"`
	// LastErrorCeilingResetAt identifies the exact local budget window that
	// produced LastError. Clients must match it against the live ceiling row
	// before displaying counters or reset details from that row.
	LastErrorCeilingResetAt string `json:"last_error_ceiling_reset_at,omitempty" format:"date-time"`
	// DetailRefreshOverdue counts open items whose last successful detail
	// check is at least a day old (including items never checked).
	DetailRefreshOverdue int `json:"detail_refresh_overdue,omitempty"`
}

func formatRateLimitWait(wait time.Duration) string {
	if wait <= 0 {
		return "0s"
	}
	if wait < time.Minute {
		seconds := int((wait + time.Second - time.Nanosecond) / time.Second)
		return fmt.Sprintf("%ds", seconds)
	}

	minutes := int((wait + time.Minute - time.Nanosecond) / time.Minute)
	hours := minutes / 60
	remainingMinutes := minutes % 60
	if hours == 0 {
		return fmt.Sprintf("%dm", minutes)
	}
	if remainingMinutes == 0 {
		return fmt.Sprintf("%dh", hours)
	}
	return fmt.Sprintf("%dh%dm", hours, remainingMinutes)
}

// DiffSyncErrorCode categorizes the reason a diff sync failed. The frontend
// uses this category to render a user-facing message that does not leak local
// clone paths, refs, SHAs, or git stderr.
type DiffSyncErrorCode string

const (
	// DiffSyncCodeCloneUnavailable means the local bare clone could not be
	// created or updated (network failure, disk full, permission denied).
	DiffSyncCodeCloneUnavailable DiffSyncErrorCode = "clone_unavailable"
	// DiffSyncCodeCommitUnreachable means a commit needed to compute the diff
	// (PR head, merge commit, or its first parent) is not present in the local
	// clone and could not be fetched.
	DiffSyncCodeCommitUnreachable DiffSyncErrorCode = "commit_unreachable"
	// DiffSyncCodeMergeBaseFailed means git merge-base could not compute the
	// fork point between the PR head and the base.
	DiffSyncCodeMergeBaseFailed DiffSyncErrorCode = "merge_base_failed"
	// DiffSyncCodeInternal covers database failures and other unexpected
	// internal errors during diff computation.
	DiffSyncCodeInternal DiffSyncErrorCode = "internal"
)

// DiffSyncError reports a non-fatal failure to compute or update the diff SHAs
// for a PR. SyncMR returns this when only the diff portion of the sync failed:
// the PR row, timeline, and CI status were updated successfully, so callers
// should still treat the PR data as fresh, but the diff view will be stale or
// missing until the underlying problem is fixed.
//
// Code categorizes the failure for client-facing messaging via UserMessage.
// Err preserves the underlying detail for server-side logging only — never
// expose Err.Error() to API clients, since it can contain clone paths, refs,
// SHAs, and git stderr.
type DiffSyncError struct {
	Code DiffSyncErrorCode
	Err  error
}

func (e *DiffSyncError) Error() string {
	return fmt.Sprintf("diff sync failed (%s): %v", e.Code, e.Err)
}

func (e *DiffSyncError) Unwrap() error {
	return e.Err
}

// UserMessage returns a sanitized message safe to surface to API clients.
// It never includes clone paths, refs, SHAs, or other internal details from
// the underlying error.
func (e *DiffSyncError) UserMessage() string {
	switch e.Code {
	case DiffSyncCodeCloneUnavailable:
		return "Diff data is unavailable: the local repository clone could not be prepared."
	case DiffSyncCodeCommitUnreachable:
		return "Diff data is unavailable: a required commit is missing from the local clone."
	case DiffSyncCodeMergeBaseFailed:
		return "Diff data is unavailable: could not determine the merge base for this pull request."
	case DiffSyncCodeInternal:
		return "Diff data is unavailable: internal error while updating diff data."
	default:
		return "Diff data is unavailable."
	}
}

// RepoRef identifies a repository on a configured provider.
type RepoRef struct {
	Platform     platform.Kind
	RepoID       int64
	Owner        string
	Name         string
	PlatformHost string
	RepoPath     string
	// Key is the provider's stable repository key, the repository's
	// identity. Zero until the provider has resolved the ref.
	Key           platform.RepositoryKey
	WebURL        string
	CloneURL      string
	DefaultBranch string
	// Archived marks a provider-archived repository: configured for archive
	// collection only, skipped by live sync.
	Archived bool
	// ConfiguredRepoPath is the config-entry path this ref was resolved
	// from. It correlates a tracked repository with its config entry after
	// a provider-side rename, so a transient resolve failure falls back to
	// the tracked ref instead of synthesizing an identity-less duplicate.
	ConfiguredRepoPath string
}

// PartialSyncError reports a repo sync cycle whose index scan completed but
// failed to refresh one or more items in the listed scopes. A refused bulk
// read lists no scopes because REST indexing still covered them. Budget-only
// waits preserve completed repo health; other failures update it. Consumers that
// depend only on an unaffected scope (stack detection over merge requests,
// workspace refresh flows) should proceed instead of treating the repository
// as failed wholesale.
type PartialSyncError struct {
	MergeRequests bool
	Issues        bool
	Cause         error
}

func (e *PartialSyncError) Error() string {
	var failedPaths []string
	if e.MergeRequests {
		failedPaths = append(failedPaths, "merge request")
	}
	if e.Issues {
		failedPaths = append(failedPaths, "issue")
	}
	if e.Cause != nil && failedPaths == nil {
		return e.Cause.Error()
	}
	message := fmt.Sprintf("one or more %s sync items failed", strings.Join(failedPaths, " and "))
	if e.Cause != nil {
		return message + ": " + e.Cause.Error()
	}
	return message
}

func (e *PartialSyncError) Unwrap() error {
	return e.Cause
}

// ExclusivePartialSyncFailure returns the typed partial failure only when the
// complete error is the partial failure itself. A partial error joined or
// wrapped with any other failure (for example a hard post-sync bookkeeping
// error) describes more than its scopes, so consumers must treat it as a
// hard failure rather than proceeding on the strength of a scope report
// that does not cover the whole error.
func ExclusivePartialSyncFailure(err error) (*PartialSyncError, bool) {
	//nolint:errorlint // exclusivity is deliberate: joined or wrapped errors must not be typed partial
	partial, ok := err.(*PartialSyncError)
	return partial, ok
}

// RepoSyncResult holds the outcome of syncing a single repo.
type RepoSyncResult struct {
	Platform     platform.Kind
	Owner        string
	Name         string
	PlatformHost string
	Error        string // empty on success
	// PartialFailure carries the failed item scopes when Error records a
	// partial per-item failure; nil on success and on hard repository
	// failures.
	PartialFailure *PartialSyncError
	// GitHubNativeStacks is non-nil only when the preview setting participated
	// in this repository sync. Completion hooks may project only these numbers.
	GitHubNativeStacks *GitHubNativeStackSyncResult
}

// WatchedMR identifies a merge request to sync on a fast interval.
type WatchedMR struct {
	Owner        string
	Name         string
	Number       int
	Platform     platform.Kind
	PlatformHost string
}
