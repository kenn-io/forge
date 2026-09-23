package authapi

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/server/httpapi"
	"go.kenn.io/forge/platform"
)

const (
	AuthenticatedViewerLoginTTL            = time.Hour
	authenticatedViewerLoginRefreshTimeout = 30 * time.Second
)

type ViewerLoginCacheEntry struct {
	login     string
	FetchedAt time.Time
}

type ViewerLoginCall struct {
	done  chan struct{}
	login string
	err   error
}

func (s *Handlers) ResolveAuthenticatedViewerLogins(
	ctx context.Context, filters []db.RepoFilter,
) ([]db.RepoViewerLogin, error) {
	if (*s.Syncer) == nil {
		return nil, httpapi.Internal("authenticated viewer lookup unavailable")
	}
	repos, err := s.Db.ListRepos(ctx)
	if err != nil {
		return nil, httpapi.Internal("load repositories for authenticated viewer failed")
	}
	if (*s.Cfg) != nil {
		repos = s.FilterConfiguredRepos(repos)
	}
	repos = filterViewerLoginRepos(repos, filters)

	viewers := make([]db.RepoViewerLogin, 0, len(repos))
	for _, repo := range repos {
		kind := httpapi.ProviderKind(repo)
		host := httpapi.ProviderHost(repo)
		resolver, err := (*s.Syncer).Registry().AuthenticatedUserResolver(kind, host)
		if err != nil {
			continue
		}
		cacheKey := authenticatedViewerCacheKey(kind, host, repo, resolver)
		login, err := s.authenticatedViewerLogin(ctx, cacheKey, repo, resolver)
		if err != nil {
			continue
		}
		viewers = append(viewers, db.RepoViewerLogin{RepoID: repo.ID, Login: login})
	}
	return viewers, nil
}

func filterViewerLoginRepos(repos []db.Repo, filters []db.RepoFilter) []db.Repo {
	if len(filters) == 0 {
		return repos
	}
	filtered := make([]db.Repo, 0, len(repos))
	for _, repo := range repos {
		for _, filter := range filters {
			if viewerRepoMatchesFilter(repo, filter) {
				filtered = append(filtered, repo)
				break
			}
		}
	}
	return filtered
}

func viewerRepoMatchesFilter(repo db.Repo, filter db.RepoFilter) bool {
	if filter.Platform != "" && !strings.EqualFold(repo.Platform, filter.Platform) {
		return false
	}
	if filter.PlatformHost != "" && !strings.EqualFold(repo.PlatformHost, filter.PlatformHost) {
		return false
	}
	if filter.RepoPath != "" {
		return canonicalViewerRepoPath(repo.RepoPath) == canonicalViewerRepoPath(filter.RepoPath)
	}
	return filter.RepoOwner != "" && filter.RepoName != "" &&
		strings.EqualFold(repo.Owner, filter.RepoOwner) &&
		strings.EqualFold(repo.Name, filter.RepoName)
}

func canonicalViewerRepoPath(value string) string {
	parts := strings.Split(strings.Trim(value, "/ "), "/")
	for i := range parts {
		parts[i] = strings.ToLower(strings.TrimSpace(parts[i]))
	}
	return strings.Join(parts, "/")
}

func authenticatedViewerCacheKey(
	kind platform.Kind,
	host string,
	repo db.Repo,
	resolver platform.AuthenticatedUserResolver,
) string {
	credentialKey := ""
	if keyed, ok := resolver.(platform.AuthenticatedUserCacheKeyResolver); ok {
		credentialKey = strings.TrimSpace(keyed.AuthenticatedUserCacheKey(httpapi.PlatformRepoRef(repo)))
	}
	if credentialKey == "" {
		if kind == platform.KindGitHub {
			credentialKey = fmt.Sprintf("repository:%d", repo.ID)
		} else {
			credentialKey = "provider-host"
		}
	}
	return strings.Join([]string{string(kind), strings.ToLower(host), credentialKey}, "\x00")
}

func (s *Handlers) authenticatedViewerLogin(
	ctx context.Context,
	cacheKey string,
	repo db.Repo,
	resolver platform.AuthenticatedUserResolver,
) (string, error) {
	s.ViewerLoginMu.Lock()
	if (*s.ViewerLoginCache) == nil {
		(*s.ViewerLoginCache) = make(map[string]ViewerLoginCacheEntry)
	}
	if entry, ok := (*s.ViewerLoginCache)[cacheKey]; ok {
		if time.Since(entry.FetchedAt) >= AuthenticatedViewerLoginTTL {
			s.refreshAuthenticatedViewerLogin(ctx, cacheKey, repo, resolver)
		}
		s.ViewerLoginMu.Unlock()
		return entry.login, nil
	}
	if call, ok := (*s.ViewerLoginInFlight)[cacheKey]; ok {
		s.ViewerLoginMu.Unlock()
		select {
		case <-call.done:
			return call.login, call.err
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	if (*s.ViewerLoginInFlight) == nil {
		(*s.ViewerLoginInFlight) = make(map[string]*ViewerLoginCall)
	}
	call := &ViewerLoginCall{done: make(chan struct{})}
	(*s.ViewerLoginInFlight)[cacheKey] = call
	s.ViewerLoginMu.Unlock()
	s.resolveAuthenticatedViewerLogin(ctx, cacheKey, repo, resolver, call)
	return call.login, call.err
}

func (s *Handlers) refreshAuthenticatedViewerLogin(
	ctx context.Context,
	cacheKey string,
	repo db.Repo,
	resolver platform.AuthenticatedUserResolver,
) {
	if _, ok := (*s.ViewerLoginInFlight)[cacheKey]; ok {
		return
	}
	if (*s.ViewerLoginInFlight) == nil {
		(*s.ViewerLoginInFlight) = make(map[string]*ViewerLoginCall)
	}
	call := &ViewerLoginCall{done: make(chan struct{})}
	(*s.ViewerLoginInFlight)[cacheKey] = call
	refreshCtx, cancel := context.WithTimeout(
		context.WithoutCancel(ctx), authenticatedViewerLoginRefreshTimeout,
	)
	go func() {
		defer cancel()
		s.resolveAuthenticatedViewerLogin(refreshCtx, cacheKey, repo, resolver, call)
	}()
}

func (s *Handlers) resolveAuthenticatedViewerLogin(
	ctx context.Context,
	cacheKey string,
	repo db.Repo,
	resolver platform.AuthenticatedUserResolver,
	call *ViewerLoginCall,
) {
	kind := httpapi.ProviderKind(repo)
	host := httpapi.ProviderHost(repo)
	var err error
	call.login, err = resolver.AuthenticatedUser(ctx, httpapi.PlatformRepoRef(repo))
	call.login = strings.TrimSpace(call.login)
	if err == nil && call.login == "" {
		err = errors.New("provider returned an empty authenticated login")
	}
	if err != nil {
		call.err = httpapi.ProviderCallProblem(err, string(kind), host)
	}

	s.ViewerLoginMu.Lock()
	delete((*s.ViewerLoginInFlight), cacheKey)
	if call.err == nil {
		(*s.ViewerLoginCache)[cacheKey] = ViewerLoginCacheEntry{
			login: call.login, FetchedAt: time.Now(),
		}
	}
	close(call.done)
	s.ViewerLoginMu.Unlock()
}
