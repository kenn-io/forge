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
	gitlab "gitlab.com/gitlab-org/api/client-go/v3"
	"go.kenn.io/forge/internal/gitclone"
	"go.kenn.io/forge/internal/telemetry"
	"go.kenn.io/forge/platform"
	"go.kenn.io/forge/platform/gitealike"
	platformgithub "go.kenn.io/forge/platform/github"
)

var (
	syncSources      = telemetry.SyncSources
	syncReasons      = telemetry.SyncReasons
	syncScopeSources = map[failScope]string{failMR: "merge_requests", failIssues: "issues"}
)

type syncPassKey struct{}

// A missing result means no read; an empty reason means a successful read.
type syncPass struct {
	mu      sync.Mutex
	results map[string]map[string]string
}

func (p *syncPass) observe(repo RepoRef, source string, err error) {
	if errors.Is(err, platform.ErrRepositoryFeatureDisabled) {
		return
	}
	reason := ""
	if err != nil && !platformgithub.IsNotModified(err) {
		reason = syncFailureReason(err)
	}
	if source != "refs" && isLocalFilesystemFailure(err) || syncBudgetWait(err) || errors.Is(err, context.Canceled) {
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
	if higherSyncFailureReason(reason, p.results[source][key]) || p.results[source][key] == "" {
		p.results[source][key] = reason
	}
}

func (s *Syncer) observeSync(ctx context.Context, repo RepoRef, source string, err error) {
	if pass, ok := ctx.Value(syncPassKey{}).(*syncPass); ok {
		pass.observe(repo, source, err)
	}
}

// observeSyncItem skips items the provider reports as missing or as
// individually inaccessible; neither says the source itself is failing.
func (s *Syncer) observeSyncItem(ctx context.Context, repo RepoRef, source string, err error) {
	if syncFailureReason(err) != "not_found" && !errors.Is(err, platform.ErrLookupInaccessible) {
		s.observeSync(ctx, repo, source, err)
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
	for _, source := range syncSources {
		live := make(map[string]bool, len(tracked))
		s.featureCooldowns.mu.Lock()
		for _, repo := range tracked {
			_, state, _ := s.featureCooldowns.lookupState(repositoryFeatureKeys(repo, syncSourceFeature(source)))
			if !repo.Archived && !state.nextProbe.After(s.nowUTC()) {
				live[mergeQueueRepoKey(repo)] = true
			}
		}
		s.featureCooldowns.mu.Unlock()
		failures := s.syncFailures[source]
		if failures == nil {
			failures = make(map[string]bool)
			s.syncFailures[source] = failures
		}
		for key := range failures {
			if !live[key] {
				delete(failures, key)
			}
		}
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

// An empty reason represents success and ranks below every failure.
func higherSyncFailureReason(reason, current string) bool {
	return reason != "" && (current == "" || slices.Index(syncReasons, reason) < slices.Index(syncReasons, current))
}

// syscall.Errno satisfies net.Error, so callers must rule out local
// filesystem failures before treating a net.Error as a network one.
func isLocalFilesystemFailure(err error) bool {
	_, pathErr := errors.AsType[*fs.PathError](err)
	return pathErr || errors.Is(err, syscall.EACCES) || errors.Is(err, syscall.EPERM) || errors.Is(err, syscall.ENOSPC) || errors.Is(err, syscall.EROFS)
}

func syncFailureReason(err error) string {
	switch {
	case errors.Is(err, platform.ErrRateLimited):
		return "rate_limited"
	case errors.Is(err, platform.ErrPermissionDenied), errors.Is(err, platform.ErrMissingToken):
		return "auth"
	case errors.Is(err, platform.ErrNotFound):
		return "not_found"
	case errors.Is(err, platform.ErrProviderContract):
		return "provider"
	}
	status := platformgithub.StatusCode(err)
	if response, ok := errors.AsType[*gitlab.ErrorResponse](err); ok {
		status = response.StatusCode
	}
	if response, ok := errors.AsType[*gitealike.HTTPError](err); ok {
		status = response.StatusCode
	}
	if response, ok := errors.AsType[*bitbucket.UnexpectedResponseStatusError](err); ok {
		status = response.StatusCode
	}
	_, primaryLimit := errors.AsType[*gh.RateLimitError](err)
	_, secondaryLimit := errors.AsType[*gh.AbuseRateLimitError](err)
	_, networkError := errors.AsType[net.Error](err)
	switch {
	case primaryLimit, secondaryLimit, status == 429:
		return "rate_limited"
	case gitAccessFailureReason(err) != "", status == 401, status == 403:
		return "auth"
	case status == 404:
		return "not_found"
	case errors.Is(err, context.DeadlineExceeded), networkError && !isLocalFilesystemFailure(err), gitclone.IsTransientGitError(err):
		return "network"
	case status >= 400 && status < 500:
		return "other"
	case status >= 500 && status < 600:
		return "provider"
	default:
		return "other"
	}
}
