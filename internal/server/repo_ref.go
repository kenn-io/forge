package server

import (
	"context"
	"strconv"
	"strings"

	"go.kenn.io/middleman/internal/db"
	"go.kenn.io/middleman/internal/platform"
	"go.kenn.io/middleman/internal/server/httpapi"
)

type repoRefInput struct {
	Provider     string `query:"provider"`
	PlatformHost string `query:"platform_host"`
	RepoPath     string `query:"repo_path"`
}

func (s *Server) lookupRepoByRefInput(
	ctx context.Context,
	input repoRefInput,
) (*db.Repo, error) {
	return s.repoResolver.Lookup(ctx, input.Provider, input.PlatformHost, input.RepoPath)
}

func (s *Server) lookupRepoByProviderRoute(
	ctx context.Context,
	provider, platformHost, owner, name string,
) (*db.Repo, error) {
	owner = strings.Trim(owner, "/ ")
	name = strings.Trim(name, "/ ")
	if owner == "" || name == "" {
		return nil, httpapi.ErrRepoPathRequired
	}
	return s.lookupRepoByRefInput(ctx, repoRefInput{
		Provider:     provider,
		PlatformHost: platformHost,
		RepoPath:     owner + "/" + name,
	})
}

func (s *Server) repoRefFromRepo(repo db.Repo) httpapi.RepoRefResponse {
	return s.repoResolver.Ref(repo)
}

// repoRefWithOperations is repoRefFromRepo plus the per-operation
// mutation availability detail views need to gate their action
// buttons and palette commands.
func (s *Server) repoRefWithOperations(repo db.Repo) httpapi.RepoRefResponse {
	resp := s.repoRefFromRepo(repo)
	ops := s.repoOperations(repo)
	resp.Operations = &ops
	return resp
}

func (s *Server) repoResponse(repo db.Repo) repoResponse {
	return repoResponse{
		ID:                  repo.ID,
		Platform:            repo.Platform,
		PlatformHost:        repo.PlatformHost,
		Owner:               repo.Owner,
		Name:                repo.Name,
		LastSyncStartedAt:   repo.LastSyncStartedAt,
		LastSyncCompletedAt: repo.LastSyncCompletedAt,
		LastSyncError:       repo.LastSyncError,
		AllowSquashMerge:    repo.AllowSquashMerge,
		AllowMergeCommit:    repo.AllowMergeCommit,
		AllowRebaseMerge:    repo.AllowRebaseMerge,
		ViewerCanMerge:      repo.ViewerCanMerge,
		CreatedAt:           repo.CreatedAt,
		Capabilities:        s.capabilitiesForRepo(repo),
		Operations:          s.repoOperations(repo),
	}
}

func repoRefFromParts(provider, host, owner, name string) httpapi.RepoRefResponse {
	provider = strings.TrimSpace(provider)
	if provider == "" {
		provider = string(platform.KindGitHub)
	}
	return httpapi.RepoRefResponse{
		Provider:     provider,
		PlatformHost: host,
		RepoPath:     owner + "/" + name,
		Owner:        owner,
		Name:         name,
	}
}

func (s *Server) repoRefFromParts(
	provider, host, owner, name string,
) httpapi.RepoRefResponse {
	resp := repoRefFromParts(provider, host, owner, name)
	resp.Capabilities = s.capabilitiesForProvider(provider, host)
	return resp
}

func repoProviderKind(repo db.Repo) platform.Kind {
	if strings.TrimSpace(repo.Platform) == "" {
		return platform.KindGitHub
	}
	return platform.Kind(repo.Platform)
}

func repoProviderHost(repo db.Repo) string {
	if strings.TrimSpace(repo.PlatformHost) != "" {
		return repo.PlatformHost
	}
	if host, ok := platform.DefaultHost(repoProviderKind(repo)); ok {
		return host
	}
	return platform.DefaultGitHubHost
}

func platformRepoRefFromDB(repo db.Repo) platform.RepoRef {
	repoPath := strings.TrimSpace(repo.RepoPath)
	if repoPath == "" {
		repoPath = repo.Owner + "/" + repo.Name
	}
	return platform.RepoRef{
		Platform:           repoProviderKind(repo),
		Host:               repoProviderHost(repo),
		Owner:              repo.Owner,
		Name:               repo.Name,
		RepoPath:           repoPath,
		PlatformID:         repoNumericPlatformID(repo),
		PlatformExternalID: repo.PlatformRepoID,
		WebURL:             repo.WebURL,
		CloneURL:           repo.CloneURL,
		DefaultBranch:      repo.DefaultBranch,
	}
}

func repoNumericPlatformID(repo db.Repo) int64 {
	id, err := strconv.ParseInt(strings.TrimSpace(repo.PlatformRepoID), 10, 64)
	if err != nil || id <= 0 {
		return 0
	}
	return id
}

func (s *Server) capabilitiesForRepo(repo db.Repo) httpapi.ProviderCapabilitiesResponse {
	return s.capabilitiesForProvider(
		string(repoProviderKind(repo)), repoProviderHost(repo),
	)
}

func (s *Server) capabilitiesForProvider(
	provider, host string,
) httpapi.ProviderCapabilitiesResponse {
	kind, err := platform.NormalizeKind(provider)
	if err != nil {
		return httpapi.ProviderCapabilitiesResponse{}
	}
	host = strings.TrimSpace(host)
	if host == "" {
		var ok bool
		host, ok = platform.DefaultHost(kind)
		if !ok {
			return httpapi.ProviderCapabilitiesResponse{}
		}
	}
	return s.repoResolver.Capabilities(kind, host)
}
