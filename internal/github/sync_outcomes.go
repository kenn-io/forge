package github

import (
	"context"
	"errors"
	"io/fs"
	"slices"
	"sync"
	"syscall"
	"time"

	gh "github.com/google/go-github/v92/github"
	"go.kenn.io/forge/internal/telemetry"
	"go.kenn.io/forge/platform"
	platformgithub "go.kenn.io/forge/platform/github"
)

var syncScopeSources = map[failScope]string{failMR: telemetry.SyncSourceMergeRequests, failIssues: telemetry.SyncSourceIssues}

type syncPassKey struct{}

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
	if capsErr != nil {
		return
	}
	for scope, source := range syncScopeSources {
		s.featureCooldowns.mu.Lock()
		_, state, _ := s.featureCooldowns.lookupState(repositoryFeatureKeys(repo, source))
		s.featureCooldowns.mu.Unlock()
		if !state.nextProbe.After(s.nowUTC()) && (scope == failMR && caps.ReadMergeRequests || scope == failIssues && caps.ReadIssues) {
			s.observeSync(ctx, repo, source, err)
		}
	}
}

// deferRepositoryFeature ends the repository's outages for sources the
// cooldown governs, so a failure after the cooldown reports again.
func (s *Syncer) deferRepositoryFeature(repo RepoRef, feature string, nextProbe time.Time) {
	s.featureCooldowns.deferUntil(repo, feature, nextProbe)
	s.syncFailuresMu.Lock()
	defer s.syncFailuresMu.Unlock()
	for source, failures := range s.syncFailures {
		if syncSourceFeature(source) == feature {
			delete(failures, mergeQueueRepoKey(repo))
			delete(failures, repoFailKey(repo))
		}
	}
}

// syncSourceFeature names the feature whose cooldown governs a source. The
// detail drain skips CI reads under the merge request cooldown.
func syncSourceFeature(source string) string {
	if source == telemetry.SyncSourceChecks {
		return platform.RepositoryFeatureMergeRequests
	}
	return source
}

// finishSyncPass runs under the run slot and syncFailuresMu after all
// observations have finished.
func (s *Syncer) finishSyncPass(pass *syncPass, tracked []RepoRef) {
	if pass == nil {
		return
	}
	pass.mu.Lock()
	defer pass.mu.Unlock()
	if s.syncFailures == nil {
		s.syncFailures = make(map[string]map[string]bool)
	}
	for _, source := range telemetry.SyncSources() {
		live, failures := s.liveSyncFailures(source, tracked)
		reason := ""
		for key, result := range pass.results[source] {
			if !live[key] {
				continue
			}
			if result == "" {
				delete(failures, key)
			} else if !failures[key] {
				failures[key] = true
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

// liveSyncFailures returns the source's live repositories and its failure
// state, pruned to them.
func (s *Syncer) liveSyncFailures(source string, tracked []RepoRef) (map[string]bool, map[string]bool) {
	if s.syncFailures[source] == nil {
		s.syncFailures[source] = make(map[string]bool)
	}
	failures := s.syncFailures[source]
	live := make(map[string]bool, len(tracked))
	s.featureCooldowns.mu.Lock()
	for _, repo := range tracked {
		key := mergeQueueRepoKey(repo)
		// Resolving identity re-keys a repository without ending its outage.
		if failures[repoFailKey(repo)] {
			delete(failures, repoFailKey(repo))
			failures[key] = true
		}
		_, state, _ := s.featureCooldowns.lookupState(repositoryFeatureKeys(repo, syncSourceFeature(source)))
		if !repo.Archived && !state.nextProbe.After(s.nowUTC()) {
			live[key] = true
		}
	}
	s.featureCooldowns.mu.Unlock()
	for key := range failures {
		if !live[key] {
			delete(failures, key)
		}
	}
	return live, failures
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
