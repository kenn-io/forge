package server

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/wesm/middleman/internal/db"
	"github.com/wesm/middleman/internal/platform"
)

var errRepoPathRequired = errors.New("repo_path is required")

type repoRefInput struct {
	Provider     string `query:"provider"`
	PlatformHost string `query:"platform_host"`
	RepoPath     string `query:"repo_path"`
}

type repoRefResponse struct {
	Provider     string                       `json:"provider"`
	PlatformHost string                       `json:"platform_host"`
	RepoPath     string                       `json:"repo_path"`
	Owner        string                       `json:"owner"`
	Name         string                       `json:"name"`
	Capabilities providerCapabilitiesResponse `json:"capabilities"`
}

func (s *Server) lookupRepoByRefInput(
	ctx context.Context,
	input repoRefInput,
) (*db.Repo, error) {
	provider := strings.TrimSpace(input.Provider)
	host := strings.TrimSpace(input.PlatformHost)
	repoPath := strings.Trim(input.RepoPath, "/ ")
	kind, err := platform.NormalizeKind(provider)
	if err != nil {
		return nil, err
	}
	provider = string(kind)
	if host == "" {
		var ok bool
		host, ok = platform.DefaultHost(kind)
		if !ok {
			return nil, fmt.Errorf("platform_host is required for provider %q", kind)
		}
	}
	if repoPath == "" {
		return nil, errRepoPathRequired
	}
	repo, err := s.db.GetRepoByIdentity(ctx, db.RepoIdentity{
		Platform:     provider,
		PlatformHost: host,
		RepoPath:     repoPath,
	})
	if err != nil {
		return nil, fmt.Errorf("lookup repo: %w", err)
	}
	if repo == nil {
		return nil, errRepoNotFound
	}
	return repo, nil
}

func (s *Server) lookupRepoByProviderRoute(
	ctx context.Context,
	provider, platformHost, owner, name string,
) (*db.Repo, error) {
	owner = strings.Trim(owner, "/ ")
	name = strings.Trim(name, "/ ")
	if owner == "" || name == "" {
		return nil, errRepoPathRequired
	}
	return s.lookupRepoByRefInput(ctx, repoRefInput{
		Provider:     provider,
		PlatformHost: platformHost,
		RepoPath:     owner + "/" + name,
	})
}

func repoRefFromRepo(repo db.Repo) repoRefResponse {
	provider := strings.TrimSpace(repo.Platform)
	if provider == "" {
		provider = string(platform.KindGitHub)
	}
	repoPath := strings.TrimSpace(repo.RepoPath)
	if repoPath == "" {
		repoPath = repo.Owner + "/" + repo.Name
	}
	return repoRefResponse{
		Provider:     provider,
		PlatformHost: repo.PlatformHost,
		RepoPath:     repoPath,
		Owner:        repo.Owner,
		Name:         repo.Name,
	}
}

func (s *Server) repoRefFromRepo(repo db.Repo) repoRefResponse {
	resp := repoRefFromRepo(repo)
	resp.Capabilities = s.capabilitiesForRepo(repo)
	return resp
}

func (s *Server) repoResponse(repo db.Repo) repoResponse {
	return repoResponse{
		ID:                       repo.ID,
		Platform:                 repo.Platform,
		PlatformHost:             repo.PlatformHost,
		Owner:                    repo.Owner,
		Name:                     repo.Name,
		LastSyncStartedAt:        repo.LastSyncStartedAt,
		LastSyncCompletedAt:      repo.LastSyncCompletedAt,
		LastSyncError:            repo.LastSyncError,
		AllowSquashMerge:         repo.AllowSquashMerge,
		AllowMergeCommit:         repo.AllowMergeCommit,
		AllowRebaseMerge:         repo.AllowRebaseMerge,
		ViewerCanMerge:           repo.ViewerCanMerge,
		BackfillPRPage:           repo.BackfillPRPage,
		BackfillPRComplete:       repo.BackfillPRComplete,
		BackfillPRCompletedAt:    repo.BackfillPRCompletedAt,
		BackfillIssuePage:        repo.BackfillIssuePage,
		BackfillIssueComplete:    repo.BackfillIssueComplete,
		BackfillIssueCompletedAt: repo.BackfillIssueCompletedAt,
		CreatedAt:                repo.CreatedAt,
		Capabilities:             s.capabilitiesForRepo(repo),
		Operations:               s.repoOperations(repo),
	}
}

func repoRefFromParts(provider, host, owner, name string) repoRefResponse {
	provider = strings.TrimSpace(provider)
	if provider == "" {
		provider = string(platform.KindGitHub)
	}
	return repoRefResponse{
		Provider:     provider,
		PlatformHost: host,
		RepoPath:     owner + "/" + name,
		Owner:        owner,
		Name:         name,
	}
}

func (s *Server) repoRefFromParts(
	provider, host, owner, name string,
) repoRefResponse {
	resp := repoRefFromParts(provider, host, owner, name)
	resp.Capabilities = s.capabilitiesForProvider(provider, host)
	return resp
}

func providerCapabilitiesFromPlatform(caps platform.Capabilities) providerCapabilitiesResponse {
	return providerCapabilitiesResponse{
		ReadRepositories:  caps.ReadRepositories,
		ReadMergeRequests: caps.ReadMergeRequests,
		ReadIssues:        caps.ReadIssues,
		ReadComments:      caps.ReadComments,
		ReadReleases:      caps.ReadReleases,
		ReadCI:            caps.ReadCI,
		ReadLabels:        caps.ReadLabels,
		CommentMutation:   caps.CommentMutation,
		StateMutation:     caps.StateMutation,
		MergeMutation:     caps.MergeMutation,
		ReviewMutation:    caps.ReviewMutation,
		WorkflowApproval:  caps.WorkflowApproval,
		ReadyForReview:    caps.ReadyForReview,
		IssueMutation:     caps.IssueMutation,
		LabelMutation:     caps.LabelMutation,
	}
}

func defaultGitHubProviderCapabilities() providerCapabilitiesResponse {
	return providerCapabilitiesFromPlatform(platform.Capabilities{
		ReadRepositories:  true,
		ReadMergeRequests: true,
		ReadIssues:        true,
		ReadComments:      true,
		ReadReleases:      true,
		ReadCI:            true,
		ReadLabels:        false,
		CommentMutation:   true,
		StateMutation:     true,
		MergeMutation:     true,
		ReviewMutation:    true,
		WorkflowApproval:  true,
		ReadyForReview:    true,
		IssueMutation:     true,
		LabelMutation:     false,
	})
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
		PlatformExternalID: repo.PlatformRepoID,
		WebURL:             repo.WebURL,
		CloneURL:           repo.CloneURL,
		DefaultBranch:      repo.DefaultBranch,
	}
}

func (s *Server) capabilitiesForRepo(repo db.Repo) providerCapabilitiesResponse {
	return s.capabilitiesForProvider(
		string(repoProviderKind(repo)), repoProviderHost(repo),
	)
}

func (s *Server) capabilitiesForProvider(
	provider, host string,
) providerCapabilitiesResponse {
	kind, err := platform.NormalizeKind(provider)
	if err != nil {
		return providerCapabilitiesResponse{}
	}
	host = strings.TrimSpace(host)
	if host == "" {
		var ok bool
		host, ok = platform.DefaultHost(kind)
		if !ok {
			return providerCapabilitiesResponse{}
		}
	}
	if s != nil && s.syncer != nil {
		caps, err := s.syncer.ProviderCapabilities(kind, host)
		if err == nil {
			return providerCapabilitiesFromPlatform(caps)
		}
	}
	if kind == platform.KindGitHub {
		return defaultGitHubProviderCapabilities()
	}
	return providerCapabilitiesResponse{}
}
