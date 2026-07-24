package httpapi

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"go.kenn.io/middleman/internal/db"
	"go.kenn.io/middleman/internal/platform"
)

var ErrRepoPathRequired = errors.New("repo_path is required")
var ErrRepoNotFound = errors.New("repo not found")
var ErrRepositoryStoreUnavailable = errors.New("repository store unavailable")

type RepositoryResolver struct {
	db                   *db.DB
	providerCapabilities func(platform.Kind, string) (platform.Capabilities, error)
}

type RepositoryResolverDeps struct {
	DB                   *db.DB
	ProviderCapabilities func(platform.Kind, string) (platform.Capabilities, error)
}

func NewRepositoryResolver(deps RepositoryResolverDeps) *RepositoryResolver {
	return &RepositoryResolver{
		db:                   deps.DB,
		providerCapabilities: deps.ProviderCapabilities,
	}
}

func (r *RepositoryResolver) Lookup(
	ctx context.Context,
	provider, platformHost, repoPath string,
) (*db.Repo, error) {
	if r == nil || r.db == nil {
		return nil, ErrRepositoryStoreUnavailable
	}
	provider = strings.TrimSpace(provider)
	platformHost = strings.TrimSpace(platformHost)
	repoPath = strings.Trim(repoPath, "/ ")
	kind, err := platform.NormalizeKind(provider)
	if err != nil {
		return nil, err
	}
	provider = string(kind)
	if platformHost == "" {
		var ok bool
		platformHost, ok = platform.DefaultHost(kind)
		if !ok {
			return nil, fmt.Errorf("platform_host is required for provider %q", kind)
		}
	}
	if repoPath == "" {
		return nil, ErrRepoPathRequired
	}
	repo, err := r.db.GetRepoByIdentity(ctx, db.RepoIdentity{
		Platform:     provider,
		PlatformHost: platformHost,
		RepoPath:     repoPath,
	})
	if err != nil {
		return nil, fmt.Errorf("lookup repo: %w", err)
	}
	if repo == nil {
		return nil, ErrRepoNotFound
	}
	return repo, nil
}

func (r *RepositoryResolver) List(ctx context.Context) ([]db.Repo, error) {
	if r == nil || r.db == nil {
		return nil, ErrRepositoryStoreUnavailable
	}
	return r.db.ListRepos(ctx)
}

func (r *RepositoryResolver) Ref(repo db.Repo) RepoRefResponse {
	provider := strings.TrimSpace(repo.Platform)
	if provider == "" {
		provider = string(platform.KindGitHub)
	}
	host := strings.TrimSpace(repo.PlatformHost)
	if host == "" {
		host, _ = platform.DefaultHost(platform.Kind(provider))
	}
	repoPath := strings.TrimSpace(repo.RepoPath)
	if repoPath == "" {
		repoPath = repo.Owner + "/" + repo.Name
	}
	return RepoRefResponse{
		Provider:     provider,
		PlatformHost: host,
		RepoPath:     repoPath,
		Owner:        repo.Owner,
		Name:         repo.Name,
		Capabilities: r.Capabilities(platform.Kind(provider), host),
	}
}

// Capabilities preserves the server's established fallback policy: a missing
// live registry still exposes the baseline GitHub feature set, while unknown
// non-GitHub providers report no capabilities. Every HTTP domain uses this
// method so lookup failures cannot drift between route packages.
func (r *RepositoryResolver) Capabilities(kind platform.Kind, host string) ProviderCapabilitiesResponse {
	if r != nil && r.providerCapabilities != nil {
		caps, err := r.providerCapabilities(kind, host)
		if err == nil {
			return ProviderCapabilitiesFromPlatform(caps)
		}
	}
	if kind == platform.KindGitHub {
		return ProviderCapabilitiesFromPlatform(platform.Capabilities{
			ReadRepositories:            true,
			ReadMergeRequests:           true,
			ReadIssues:                  true,
			ReadComments:                true,
			ReadReleases:                true,
			ReadCI:                      true,
			CommentMutation:             true,
			StateMutation:               true,
			MergeMutation:               true,
			ReviewMutation:              true,
			WorkflowApproval:            true,
			ReadyForReview:              true,
			DraftMutation:               true,
			IssueMutation:               true,
			ReviewSuggestionApplication: true,
		})
	}
	return ProviderCapabilitiesResponse{}
}

func ProviderCapabilitiesFromPlatform(caps platform.Capabilities) ProviderCapabilitiesResponse {
	reviewActions := make([]string, 0, len(caps.SupportedReviewActions))
	for _, action := range caps.SupportedReviewActions {
		reviewActions = append(reviewActions, string(action))
	}
	return ProviderCapabilitiesResponse{
		ReadRepositories:            caps.ReadRepositories,
		ReadMergeRequests:           caps.ReadMergeRequests,
		ReadIssues:                  caps.ReadIssues,
		ReadComments:                caps.ReadComments,
		ReadReleases:                caps.ReadReleases,
		ReadCI:                      caps.ReadCI,
		ReadLabels:                  caps.ReadLabels,
		CommentMutation:             caps.CommentMutation,
		StateMutation:               caps.StateMutation,
		MergeMutation:               caps.MergeMutation,
		ReviewMutation:              caps.ReviewMutation,
		WorkflowApproval:            caps.WorkflowApproval,
		ReadyForReview:              caps.ReadyForReview,
		DraftMutation:               caps.DraftMutation,
		IssueMutation:               caps.IssueMutation,
		LabelMutation:               caps.LabelMutation,
		AssigneeMutation:            caps.AssigneeMutation,
		ReviewerMutation:            caps.ReviewerMutation,
		ThreadReply:                 caps.ThreadReply,
		ThreadResolve:               caps.ThreadResolve,
		ReviewDraftMutation:         caps.ReviewDraftMutation,
		ReviewThreadResolution:      caps.ReviewThreadResolution,
		ReviewSuggestionApplication: caps.ReviewSuggestionApplication,
		ReadReviewThreads:           caps.ReadReviewThreads,
		NativeMultilineRanges:       caps.NativeMultilineRanges,
		MutationHeadBinding:         caps.MutationHeadBinding,
		SupportedReviewActions:      reviewActions,
	}
}
