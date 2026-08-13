package server

import (
	"context"
	"fmt"
	"strings"
	"time"

	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/server/httpapi"
)

const viewerLoginCacheTTL = time.Minute

type viewerLoginCacheEntry struct {
	login     string
	expiresAt time.Time
}

type viewerLoginCall struct {
	done  chan struct{}
	login string
	err   error
}

func (s *Server) resolveAuthenticatedViewerLogins(
	ctx context.Context,
) ([]db.RepoViewerLogin, error) {
	if s.syncer == nil {
		return nil, httpapi.Internal("authenticated viewer lookup unavailable")
	}
	repos, err := s.db.ListRepos(ctx)
	if err != nil {
		return nil, httpapi.Internal("load repositories for authenticated viewer failed")
	}
	if s.cfg != nil {
		repos = s.filterConfiguredRepos(repos)
	}

	viewers := make([]db.RepoViewerLogin, 0, len(repos))
	for _, repo := range repos {
		login, err := s.authenticatedViewerLogin(ctx, repo)
		if err != nil {
			return nil, err
		}
		viewers = append(viewers, db.RepoViewerLogin{RepoID: repo.ID, Login: login})
	}
	return viewers, nil
}

func (s *Server) authenticatedViewerLogin(ctx context.Context, repo db.Repo) (string, error) {
	now := s.now().UTC()
	s.viewerLoginMu.Lock()
	if s.viewerLoginCache == nil {
		s.viewerLoginCache = make(map[int64]viewerLoginCacheEntry)
	}
	for repoID, entry := range s.viewerLoginCache {
		if !entry.expiresAt.After(now) {
			delete(s.viewerLoginCache, repoID)
		}
	}
	if entry, ok := s.viewerLoginCache[repo.ID]; ok {
		s.viewerLoginMu.Unlock()
		return entry.login, nil
	}
	if call, ok := s.viewerLoginInFlight[repo.ID]; ok {
		s.viewerLoginMu.Unlock()
		select {
		case <-call.done:
			return call.login, call.err
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	if s.viewerLoginInFlight == nil {
		s.viewerLoginInFlight = make(map[int64]*viewerLoginCall)
	}
	call := &viewerLoginCall{done: make(chan struct{})}
	s.viewerLoginInFlight[repo.ID] = call
	s.viewerLoginMu.Unlock()

	kind := httpapi.ProviderKind(repo)
	host := httpapi.ProviderHost(repo)
	resolver, err := s.syncer.Registry().AuthenticatedUserResolver(kind, host)
	if err == nil {
		call.login, err = resolver.AuthenticatedUser(ctx, httpapi.PlatformRepoRef(repo))
		call.login = strings.TrimSpace(call.login)
		if err == nil && call.login == "" {
			err = fmt.Errorf("provider returned an empty authenticated login")
		}
	}
	if err != nil {
		call.err = httpapi.ProviderCallProblem(err, string(kind), host)
	}

	s.viewerLoginMu.Lock()
	delete(s.viewerLoginInFlight, repo.ID)
	if call.err == nil {
		s.viewerLoginCache[repo.ID] = viewerLoginCacheEntry{
			login: call.login, expiresAt: s.now().UTC().Add(viewerLoginCacheTTL),
		}
	}
	close(call.done)
	s.viewerLoginMu.Unlock()
	return call.login, call.err
}
