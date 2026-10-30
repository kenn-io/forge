package github

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync/atomic"

	"go.kenn.io/forge/platform"
)

// failScope is a bitmask indicating which sync paths failed.
type failScope uint8

const (
	failMR     failScope = 1 << iota // PR/MR sync path failed
	failIssues                       // issue sync path failed
)

func preservePartialSyncFailure(scope failScope, cause, priorFailures error) error {
	if priorFailures == nil {
		return cause
	}
	return &PartialSyncError{
		MergeRequests: scope&failMR != 0,
		Issues:        scope&failIssues != 0,
		// Disabled is an expected feature state, not a new failure. Keep it
		// first so feature classification still sees it before item errors.
		Cause: errors.Join(cause, priorFailures),
	}
}

// mixedSyncError retains both a local ceiling refusal and an independent
// failure. The ceiling alone is a scheduling wait; the mixed result is not.
type mixedSyncError struct {
	cause error
}

func (e *mixedSyncError) Error() string { return e.cause.Error() }
func (e *mixedSyncError) Unwrap() error { return e.cause }

func syncBudgetWait(err error) bool {
	_, mixed := errors.AsType[*mixedSyncError](err)
	return errors.Is(err, platform.ErrSyncBudgetExhausted) && !mixed
}

type degradedSyncKey struct{}

// withDegradedSyncTracking marks ctx so a sync that drops a child refresh
// and keeps stored data instead, such as a failed CI read, reports itself
// incomplete. Every such drop logs through logSyncFailure.
func withDegradedSyncTracking(ctx context.Context) (context.Context, *atomic.Bool) {
	degraded := new(atomic.Bool)
	return context.WithValue(ctx, degradedSyncKey{}, degraded), degraded
}

// withoutDegradedSyncTracking detaches reads a detail sync makes for
// datasets no freshness baseline vouches for, such as merge queue state,
// so their failures do not mark the sync incomplete.
func withoutDegradedSyncTracking(ctx context.Context) context.Context {
	return context.WithValue(ctx, degradedSyncKey{}, nil)
}

func logSyncFailure(ctx context.Context, level slog.Level, msg string, err error, attrs ...any) {
	if degraded, ok := ctx.Value(degradedSyncKey{}).(*atomic.Bool); ok {
		degraded.Store(true)
	}
	if syncBudgetWait(err) {
		return
	}
	slog.Log(ctx, level, msg, append(attrs, "err", err)...)
}

// retainSyncFailureCause keeps the first failure of each kind, so aggregation
// preserves real failures and the original ceiling window without accumulating
// an error for every item in a repository.
func retainSyncFailureCause(current, candidate error) error {
	if current == nil {
		return candidate
	}
	if candidate == nil {
		return current
	}
	if _, mixed := errors.AsType[*mixedSyncError](current); mixed {
		return current
	}
	if _, mixed := errors.AsType[*mixedSyncError](candidate); mixed {
		return errors.Join(current, candidate)
	}
	if syncBudgetWait(current) == syncBudgetWait(candidate) {
		return current
	}
	return &mixedSyncError{cause: errors.Join(current, candidate)}
}

// Hard returns retain every cause, including feature-disabled and bookkeeping
// errors whose types control retry behavior and partial-failure consumers.
func joinPartialFailureCause(current, cause error) error {
	if current == nil {
		return cause
	}
	joined := errors.Join(current, cause)
	if syncBudgetWait(current) != syncBudgetWait(cause) {
		return &mixedSyncError{cause: joined}
	}
	return joined
}

func partialItemFailureError(message string, cause error) error {
	if cause == nil {
		return errors.New(message)
	}
	return fmt.Errorf("%s: %w", message, cause)
}

func partialSyncFailureScope(err error) failScope {
	partial, ok := errors.AsType[*PartialSyncError](err)
	if !ok {
		return 0
	}
	var scope failScope
	if partial.MergeRequests {
		scope |= failMR
	}
	if partial.Issues {
		scope |= failIssues
	}
	return scope
}

// markRepoFailed records that the most recent sync of this repo hit
// a partial failure after the ETag cache may have been populated, so
// the next cycle must force an unconditional refetch of the affected
// list endpoints. Successful attempted scopes clear their own bits.
func (s *Syncer) markRepoFailed(repo RepoRef, scope failScope) {
	key := repoFailKey(repo)
	for {
		prev, ok := s.failedRepos.Load(key)
		merged := scope
		if ok {
			merged |= prev.(failScope)
		}
		if ok {
			if s.failedRepos.CompareAndSwap(key, prev, merged) {
				return
			}
		} else {
			if _, loaded := s.failedRepos.LoadOrStore(key, merged); !loaded {
				return
			}
		}
		// Another goroutine raced us; retry.
	}
}

func (s *Syncer) clearRepoFailedScope(repo RepoRef, scope failScope) {
	key := repoFailKey(repo)
	for {
		value, ok := s.failedRepos.Load(key)
		if !ok {
			return
		}
		remaining := value.(failScope) &^ scope
		if remaining == 0 {
			if s.failedRepos.CompareAndDelete(key, value) {
				return
			}
			continue
		}
		if s.failedRepos.CompareAndSwap(key, value, remaining) {
			return
		}
	}
}

// repoFailKey returns the sync.Map key for a repo. Includes provider
// and host so multi-provider and multi-host setups don't cross-invalidate.
func repoFailKey(repo RepoRef) string {
	return string(repoPlatform(repo)) + "/" + repoHost(repo) + "/" +
		strings.ToLower(repo.Owner) + "/" + strings.ToLower(repo.Name)
}

// consumeRepoFailed returns the failScope bitmask for a previously
// failed repo. Returns 0 if the repo had no failure. The flag remains
// set until a subsequent successful sync explicitly clears it.
func (s *Syncer) consumeRepoFailed(repo RepoRef) failScope {
	v, ok := s.failedRepos.Load(repoFailKey(repo))
	if !ok {
		return 0
	}
	return v.(failScope)
}
