package github

import (
	"context"
	"errors"
	"net"
	"sync"

	bitbucket "github.com/ktrysmt/go-bitbucket"
	gitlab "gitlab.com/gitlab-org/api/client-go/v3"
	"go.kenn.io/forge/platform"
	"go.kenn.io/forge/platform/gitealike"
	platformgithub "go.kenn.io/forge/platform/github"
)

var (
	syncSources = []string{"merge_requests", "issues", "checks", "refs"}
	syncReasons = []string{"rate_limited", "auth", "not_found", "network", "provider", "other"}
)

type syncPassKey struct{}

// A missing result means no read; an empty reason means a successful read.
type syncPass struct {
	mu      sync.Mutex
	results map[string]map[string]string
}

func (p *syncPass) observe(repo RepoRef, source string, err error) {
	if syncBudgetWait(err) || errors.Is(err, platform.ErrSyncDisabled) || errors.Is(err, platform.ErrRepositoryFeatureDisabled) || errors.Is(err, context.Canceled) {
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
	key := repoFailKey(repo)
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
	if caps.ReadMergeRequests {
		s.observeSync(ctx, repo, "merge_requests", err)
	}
	if caps.ReadIssues {
		s.observeSync(ctx, repo, "issues", err)
	}
}

// finishSyncPass runs under the run slot after all observations have finished.
func (s *Syncer) finishSyncPass(pass *syncPass, tracked []RepoRef) {
	if pass == nil {
		return
	}
	live := make(map[string]bool, len(tracked))
	for _, repo := range tracked {
		if !repo.Archived {
			live[repoFailKey(repo)] = true
		}
	}
	if s.syncFailures == nil {
		s.syncFailures = make(map[string]map[string]string)
	}
	for _, source := range syncSources {
		failures := s.syncFailures[source]
		wasFailing := len(failures) > 0
		if failures == nil {
			failures = make(map[string]string)
			s.syncFailures[source] = failures
		}
		for key, reason := range pass.results[source] {
			if reason == "" {
				delete(failures, key)
			} else {
				failures[key] = reason
			}
		}
		for key := range failures {
			if !live[key] {
				delete(failures, key)
			}
		}
		if !wasFailing && len(failures) > 0 && s.onSyncFailed != nil {
			for _, reason := range syncReasons {
				found := false
				for _, failure := range failures {
					found = found || failure == reason
				}
				if found {
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
	_, networkError := errors.AsType[net.Error](err)
	switch {
	case errors.Is(err, platform.ErrRateLimited), status == 429:
		return "rate_limited"
	case errors.Is(err, platform.ErrPermissionDenied), errors.Is(err, platform.ErrMissingToken), gitAccessFailureReason(err) != "", status == 401, status == 403:
		return "auth"
	case errors.Is(err, platform.ErrNotFound), status == 404:
		return "not_found"
	case errors.Is(err, context.DeadlineExceeded), networkError:
		return "network"
	case errors.Is(err, platform.ErrProviderContract), status >= 500 && status < 600:
		return "provider"
	default:
		return "other"
	}
}
