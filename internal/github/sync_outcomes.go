package github

import (
	"context"
	"errors"
	"io/fs"
	"slices"
	"sync"
	"syscall"

	gh "github.com/google/go-github/v92/github"
	"go.kenn.io/forge/internal/telemetry"
	"go.kenn.io/forge/platform"
	platformgithub "go.kenn.io/forge/platform/github"
)

var syncScopeSources = map[failScope]struct{ source, feature string }{
	failMR:     {telemetry.SyncSourceMergeRequests, platform.RepositoryFeatureMergeRequests},
	failIssues: {telemetry.SyncSourceIssues, platform.RepositoryFeatureIssues},
}

type syncPassKey struct{}

type syncFailureKey struct {
	source string
	repo   string
}

// A missing result means no read; an empty reason means a successful read.
type syncPass struct {
	mu      sync.Mutex
	results map[string]map[string]string
}

func (p *syncPass) observe(repo RepoRef, source string, err error) {
	if errors.Is(err, platform.ErrRepositoryFeatureDisabled) || errors.Is(err, platform.ErrProviderNotConfigured) || errors.Is(err, platform.ErrInvalidRepoRef) || isLocalFilesystemFailure(err) || syncBudgetWait(err) || errors.Is(err, context.Canceled) {
		return
	}
	reason := ""
	if err != nil && !platformgithub.IsNotModified(err) {
		reason = syncFailureReason(err)
	}
	if source == telemetry.SyncSourceChecks && reason == telemetry.SyncReasonNotFound {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.results == nil {
		p.results = make(map[string]map[string]string)
	}
	if p.results[source] == nil {
		p.results[source] = make(map[string]string)
	}
	key := mergeQueueRepoKey(repo)
	// One successful read establishes repository availability for the pass.
	if current, observed := p.results[source][key]; reason == "" || observed && current == "" {
		p.results[source][key] = ""
		return
	}
	if higherSyncFailureReason(reason, p.results[source][key]) {
		p.results[source][key] = reason
	}
}

func (s *Syncer) observeSync(ctx context.Context, repo RepoRef, source string, err error) {
	if pass, ok := ctx.Value(syncPassKey{}).(*syncPass); ok {
		pass.observe(repo, source, err)
	}
}

func (s *Syncer) observeRepoFailure(ctx context.Context, repo RepoRef, err error) {
	caps, capsErr := s.clients.Capabilities(repoPlatform(repo), repoHost(repo))
	if capsErr != nil || repo.Archived {
		return
	}
	for scope, names := range syncScopeSources {
		s.featureCooldowns.mu.Lock()
		_, state, _ := s.featureCooldowns.lookupState(repositoryFeatureKeys(repo, names.feature))
		s.featureCooldowns.mu.Unlock()
		if !state.nextProbe.After(s.nowUTC()) && (scope == failMR && caps.ReadMergeRequests || scope == failIssues && caps.ReadIssues) {
			s.observeSync(ctx, repo, names.source, err)
		}
	}
}

// finishSyncPass runs under the run slot after all observations have finished.
func (s *Syncer) finishSyncPass(pass *syncPass, tracked []RepoRef) {
	if pass == nil {
		return
	}
	if s.syncFailures == nil {
		s.syncFailures = make(map[syncFailureKey]bool)
	}
	for _, source := range telemetry.SyncSources() {
		for _, repo := range tracked {
			// Resolving identity re-keys a repository without ending its outage.
			key := syncFailureKey{source, repoFailKey(repo)}
			if s.syncFailures[key] {
				delete(s.syncFailures, key)
				s.syncFailures[syncFailureKey{source, mergeQueueRepoKey(repo)}] = true
			}
		}
		reason := ""
		for key, result := range pass.results[source] {
			failureKey := syncFailureKey{source, key}
			if result == "" {
				delete(s.syncFailures, failureKey)
			} else if !s.syncFailures[failureKey] {
				s.syncFailures[failureKey] = true
				if higherSyncFailureReason(result, reason) {
					reason = result
				}
			}
		}
		if reason != "" && s.onSyncFailed != nil {
			s.onSyncFailed(source, reason)
		}
	}
}

// An empty reason represents success and ranks below every failure.
func higherSyncFailureReason(reason, current string) bool {
	priority := []string{telemetry.SyncReasonRateLimited, telemetry.SyncReasonAuth, telemetry.SyncReasonNotFound, telemetry.SyncReasonOther}
	return reason != "" && (current == "" || slices.Index(priority, reason) < slices.Index(priority, current))
}

// Local filesystem and capacity failures do not describe provider availability.
func isLocalFilesystemFailure(err error) bool {
	_, pathErr := errors.AsType[*fs.PathError](err)
	return pathErr || errors.Is(err, syscall.EACCES) || errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.ENOSPC) || errors.Is(err, syscall.EROFS) || errors.Is(err, syscall.EMFILE) || errors.Is(err, syscall.ENFILE)
}

func syncFailureReason(err error) string {
	if gitAccessFailureReason(err) == GitAccessCredentialUnavailable {
		return telemetry.SyncReasonAuth
	}
	switch {
	case errors.Is(err, platform.ErrRateLimited):
		return telemetry.SyncReasonRateLimited
	case errors.Is(err, platform.ErrPermissionDenied), errors.Is(err, platform.ErrMissingToken):
		return telemetry.SyncReasonAuth
	case errors.Is(err, platform.ErrNotFound):
		return telemetry.SyncReasonNotFound
	}
	status := platformgithub.StatusCode(err)
	_, primaryLimit := errors.AsType[*gh.RateLimitError](err)
	_, secondaryLimit := errors.AsType[*gh.AbuseRateLimitError](err)
	switch {
	case primaryLimit, secondaryLimit, status == 429:
		return telemetry.SyncReasonRateLimited
	case status == 401, status == 403:
		return telemetry.SyncReasonAuth
	case status == 404:
		return telemetry.SyncReasonNotFound
	default:
		return telemetry.SyncReasonOther
	}
}
