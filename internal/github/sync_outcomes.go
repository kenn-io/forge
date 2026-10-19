package github

import (
	"context"
	"errors"
	"maps"
	"net"
	"slices"
	"sync"

	gh "github.com/google/go-github/v92/github"
	bitbucket "github.com/ktrysmt/go-bitbucket"
	gitlab "gitlab.com/gitlab-org/api/client-go/v3"
	"go.kenn.io/forge/internal/gitclone"
	"go.kenn.io/forge/platform"
	"go.kenn.io/forge/platform/bitbucketdc"
	"go.kenn.io/forge/platform/gitealike"
	platformgithub "go.kenn.io/forge/platform/github"
)

var (
	syncSources = []string{"merge_requests", "issues", "checks", "refs"}
	syncReasons = []string{"rate_limited", "auth", "not_found", "network", "provider", "other"}
)

type syncPassKey struct{}

// syncSourceError keeps a failed read attributed to its source through callers
// that also refresh other sources. Unwrap preserves their retry behavior.
type syncSourceError struct {
	source string
	err    error
}

func (e *syncSourceError) Error() string { return e.err.Error() }
func (e *syncSourceError) Unwrap() error { return e.err }

// A missing result means no read; an empty reason means a successful read.
type syncPass struct {
	mu      sync.Mutex
	results map[string]map[string]string
}

func (p *syncPass) observe(repo RepoRef, source string, err error) {
	if failure, ok := errors.AsType[*syncSourceError](err); ok && failure.source != source {
		// The enclosing refresh reached this source successfully before the
		// other source failed.
		err = nil
	}
	if errors.Is(err, platform.ErrRepositoryFeatureDisabled) {
		p.observe(repo, source, nil)
		//nolint:errorlint // Inspect immediate causes so disabled nodes cannot hide sibling failures.
		switch wrapped := err.(type) {
		case *platform.Error:
			if wrapped != nil && wrapped.Code != platform.ErrCodeRepositoryFeatureDisabled && wrapped.Err != nil {
				p.observe(repo, source, wrapped.Err)
			}
		case interface{ Unwrap() []error }:
			for _, cause := range wrapped.Unwrap() {
				p.observe(repo, source, cause)
			}
		case interface{ Unwrap() error }:
			p.observe(repo, source, wrapped.Unwrap())
		}
		return
	}
	if syncBudgetWait(err) || errors.Is(err, context.Canceled) {
		return
	}
	reason := ""
	if err != nil {
		reason = syncFailureReason(err)
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
	if p.results[source][key] == "" {
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
	for source, supported := range map[string]bool{"merge_requests": caps.ReadMergeRequests, "issues": caps.ReadIssues} {
		if supported && !s.featureCooldowns.coolingDown(repo, source, s.now().UTC()) {
			cause := err
			if disabled := repositoryFeatureDisabledError(repo, source, err); disabled != nil {
				cause = disabled
			}
			s.observeSync(ctx, repo, source, cause)
		}
	}
}

// finishSyncPass runs under the run slot after all observations have finished.
func (s *Syncer) finishSyncPass(pass *syncPass, tracked []RepoRef) {
	if pass == nil {
		return
	}
	live := make(map[string]string, len(tracked))
	for _, repo := range tracked {
		if !repo.Archived {
			key := mergeQueueRepoKey(repo)
			live[key] = key
			live[repoFailKey(repo)] = key
		}
	}
	if s.syncFailures == nil {
		s.syncFailures = make(map[string]map[string]string)
	}
	for _, source := range syncSources {
		failures := s.syncFailures[source]
		if failures == nil {
			failures = make(map[string]string)
			s.syncFailures[source] = failures
		}
		// Repository lookup can resolve a route to its stable identity while
		// leaving a failing source unread.
		for key, reason := range failures {
			if resolved := live[key]; resolved != "" && resolved != key {
				if _, exists := failures[resolved]; !exists {
					failures[resolved] = reason
				}
				delete(failures, key)
			}
		}
		for key := range failures {
			if live[key] == "" {
				delete(failures, key)
			}
		}
		wasFailing := len(failures) > 0
		// A route and its resolved identity can both be observed in one pass.
		// Combine them before applying recovery so failures always win.
		observed := make(map[string]string, len(pass.results[source]))
		for key, reason := range pass.results[source] {
			resolved := live[key]
			if resolved == "" {
				continue
			}
			key = resolved
			if observed[key] == "" {
				observed[key] = reason
			}
		}
		for key, reason := range observed {
			if reason == "" {
				delete(failures, key)
			} else {
				failures[key] = reason
			}
		}
		if !wasFailing && len(failures) > 0 && s.onSyncFailed != nil {
			reasons := slices.Collect(maps.Values(failures))
			// Prefer rate limits, then auth, missing resources, network,
			// provider, and other failures when repositories disagree.
			for _, reason := range syncReasons {
				if slices.Contains(reasons, reason) {
					s.onSyncFailed(source, reason)
					break
				}
			}
		}
	}
}

func syncFailureReason(err error) string {
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
	if response, ok := errors.AsType[*bitbucketdc.HTTPError](err); ok {
		status = response.StatusCode
	}
	_, primaryLimit := errors.AsType[*gh.RateLimitError](err)
	_, secondaryLimit := errors.AsType[*gh.AbuseRateLimitError](err)
	_, networkError := errors.AsType[net.Error](err)
	switch {
	case primaryLimit, secondaryLimit, errors.Is(err, platform.ErrRateLimited), status == 429:
		return "rate_limited"
	case errors.Is(err, platform.ErrPermissionDenied), errors.Is(err, platform.ErrMissingToken), gitAccessFailureReason(err) != "", status == 401, status == 403:
		return "auth"
	case errors.Is(err, platform.ErrNotFound), status == 404:
		return "not_found"
	case errors.Is(err, context.DeadlineExceeded), networkError, gitclone.IsNetworkFailure(err):
		return "network"
	case status >= 400 && status < 500:
		return "other"
	case errors.Is(err, platform.ErrProviderContract), status >= 500 && status < 600, gitclone.IsProviderFailure(err):
		return "provider"
	default:
		return "other"
	}
}
