package github

import (
	"context"
	"errors"
	"io/fs"
	"net"
	"slices"
	"sync"
	"syscall"

	gh "github.com/google/go-github/v92/github"
	bitbucket "github.com/ktrysmt/go-bitbucket"
	"go.kenn.io/forge/internal/gitclone"
	"go.kenn.io/forge/internal/telemetry"
	"go.kenn.io/forge/platform"
	"go.kenn.io/forge/platform/bitbucketdc"
	"go.kenn.io/forge/platform/gitealike"
	platformgithub "go.kenn.io/forge/platform/github"
)

var syncScopeSources = map[failScope]string{failMR: "merge_requests", failIssues: "issues"}

type syncPassKey struct{}

// A missing result means no read; an empty reason means a successful read.
type syncPass struct {
	mu      sync.Mutex
	results map[string]map[string]string
}

func (p *syncPass) observe(repo RepoRef, source string, err error) {
	if errors.Is(err, platform.ErrRepositoryFeatureDisabled) || isLocalFilesystemFailure(err) || syncBudgetWait(err) || errors.Is(err, context.Canceled) {
		return
	}
	reason := ""
	if err != nil && !platformgithub.IsNotModified(err) {
		if source == "refs" {
			if gitAccessFailureReason(err) != "" {
				reason = "auth"
			} else {
				reason = gitclone.FailureReason(err)
			}
			if reason == "" {
				return
			}
		} else {
			reason = syncFailureReason(err)
		}
	}
	if source == "checks" && (reason == "not_found" || errors.Is(err, platform.ErrLookupInaccessible)) {
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
	s.featureCooldowns.mu.Lock()
	defer s.featureCooldowns.mu.Unlock()
	for scope, source := range syncScopeSources {
		_, state, _ := s.featureCooldowns.lookupState(repositoryFeatureKeys(repo, source))
		if !state.nextProbe.After(s.nowUTC()) && (scope == failMR && caps.ReadMergeRequests || scope == failIssues && caps.ReadIssues) {
			s.observeSync(ctx, repo, source, err)
		}
	}
}

// syncSourceFeature names the feature whose cooldown governs a source. The
// detail drain skips CI reads under the merge request cooldown.
func syncSourceFeature(source string) string {
	if source == "checks" {
		return platform.RepositoryFeatureMergeRequests
	}
	return source
}

// finishSyncPass runs under the run slot after all observations have finished.
func (s *Syncer) finishSyncPass(pass *syncPass, tracked []RepoRef) {
	if pass == nil {
		return
	}
	if s.syncFailures == nil {
		s.syncFailures = make(map[string]map[string]bool)
	}
	for _, source := range telemetry.SyncSources() {
		live, failures := s.liveSyncFailures(source, tracked)
		// Only repositories that start failing count, so one repository that
		// keeps failing does not hide outages in the others.
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
	return reason != "" && (current == "" || slices.Index(telemetry.SyncReasons(), reason) < slices.Index(telemetry.SyncReasons(), current))
}

// syscall.Errno satisfies net.Error, so callers must rule out local
// filesystem failures before treating a net.Error as a network one.
func isLocalFilesystemFailure(err error) bool {
	_, pathErr := errors.AsType[*fs.PathError](err)
	return pathErr || errors.Is(err, syscall.EACCES) || errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.ENOSPC) || errors.Is(err, syscall.EROFS) || errors.Is(err, syscall.EMFILE) || errors.Is(err, syscall.ENFILE)
}

// providerStatusCode returns the HTTP status carried by a provider error
// that has no platform error code.
func providerStatusCode(err error) int {
	if response, ok := errors.AsType[*bitbucket.UnexpectedResponseStatusError](err); ok {
		return response.StatusCode
	}
	if response, ok := errors.AsType[*bitbucketdc.HTTPError](err); ok {
		return response.StatusCode
	}
	if response, ok := errors.AsType[*gitealike.HTTPError](err); ok {
		return response.StatusCode
	}
	return platformgithub.StatusCode(err)
}

func syncFailureReason(err error) string {
	_, networkError := errors.AsType[net.Error](err)
	gitReason := gitclone.FailureReason(err)
	switch {
	case errors.Is(err, platform.ErrRateLimited), gitReason == "rate_limited":
		return "rate_limited"
	case errors.Is(err, platform.ErrPermissionDenied), errors.Is(err, platform.ErrMissingToken), gitAccessFailureReason(err) != "", gitReason == "auth":
		return "auth"
	case errors.Is(err, platform.ErrNotFound):
		return "not_found"
	case errors.Is(err, context.DeadlineExceeded), networkError:
		return "network"
	case errors.Is(err, platform.ErrProviderContract):
		return "provider"
	}
	status := providerStatusCode(err)
	_, primaryLimit := errors.AsType[*gh.RateLimitError](err)
	_, secondaryLimit := errors.AsType[*gh.AbuseRateLimitError](err)
	switch {
	case primaryLimit, secondaryLimit, status == 429:
		return "rate_limited"
	case status == 401, status == 403:
		return "auth"
	case status == 404:
		return "not_found"
	case gitReason == "network":
		return "network"
	case status >= 400 && status < 500:
		return "other"
	case status >= 500 && status < 600:
		return "provider"
	default:
		if gitReason != "" {
			return gitReason
		}
		return "other"
	}
}
