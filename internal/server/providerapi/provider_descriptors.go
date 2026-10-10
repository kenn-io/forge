package providerapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"go.kenn.io/forge/internal/db"
	ghclient "go.kenn.io/forge/internal/github"
	"go.kenn.io/forge/internal/providerplane"
	"go.kenn.io/forge/internal/server/httpapi"
	"go.kenn.io/forge/platform"
)

type FederationRepositoryDescriptorInput struct {
	Body providerplane.RepositoryDescriptorRequest
}

type FederationRepositoryDescriptorOutput = httpapi.BodyOutput[providerplane.RepositoryDescriptor]

type federationDiffDescriptorRequest struct {
	Repository providerplane.RepositoryRoute `json:"repository"`
	PullNumber int                           `json:"pull_number" minimum:"1"`
}

type federationDiffDescriptorInput struct {
	Body federationDiffDescriptorRequest
}

type federationDiffDescriptorOutput = httpapi.BodyOutput[providerplane.DiffDescriptor]

// GitHubRepositoryIDRequest names a GitHub repository row stored before
// repository identity became the integer ID. owner/name select the hub's
// credential; the node ID alone decides which repository answers.
type GitHubRepositoryIDRequest struct {
	PlatformHost string `json:"platform_host" minLength:"1"`
	Owner        string `json:"owner" minLength:"1"`
	Name         string `json:"name" minLength:"1"`
	NodeID       string `json:"node_id" minLength:"1"`
}

// GitHubRepositoryIDResponse carries the integer ID GitHub reports for a node
// ID. Found is false when GitHub no longer resolves the node.
type GitHubRepositoryIDResponse struct {
	PlatformRepoID int64 `json:"platform_repo_id"`
	Found          bool  `json:"found"`
}

type federationGitHubRepositoryIDInput struct {
	Body GitHubRepositoryIDRequest
}

type federationGitHubRepositoryIDOutput = httpapi.BodyOutput[GitHubRepositoryIDResponse]

func (s *Handlers) RegisterProviderDescriptorAPI(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID: "federation-get-repository-descriptor",
		Method:      http.MethodPost,
		Path:        "/federation/provider/repository-descriptor",
		Summary:     "Resolve a repository descriptor for a Forge spoke",
		Tags:        []string{"Fleet"},
	}, s.FederationRepositoryDescriptor)
	huma.Register(api, huma.Operation{
		OperationID: "federation-get-diff-descriptor",
		Method:      http.MethodPost,
		Path:        "/federation/provider/diff-descriptor",
		Summary:     "Resolve a pull diff descriptor for a Forge spoke",
		Tags:        []string{"Fleet"},
	}, s.federationDiffDescriptor)
	huma.Register(api, huma.Operation{
		OperationID: "federation-resolve-github-repository-id",
		Method:      http.MethodPost,
		Path:        "/federation/provider/github-repository-id",
		Summary:     "Resolve a stored GitHub node ID to its integer repository ID for a Forge spoke",
		Tags:        []string{"Fleet"},
	}, s.federationResolveGitHubRepositoryID)
}

// federationResolveGitHubRepositoryID lets a spoke finish the integer
// repository-ID conversion for rows it stored under GitHub node IDs. Spokes
// hold no GitHub credentials, so they cannot ask GitHub themselves.
func (s *Handlers) federationResolveGitHubRepositoryID(
	ctx context.Context, input *federationGitHubRepositoryIDInput,
) (*federationGitHubRepositoryIDOutput, error) {
	request := input.Body
	if (*s.Syncer) == nil {
		return nil, httpapi.ServiceUnavailable("github sync is not running on this hub")
	}
	id, found, err := (*s.Syncer).ResolveRepositoryNodeID(
		ctx, request.PlatformHost, request.Owner, request.Name, request.NodeID,
	)
	if errors.Is(err, ghclient.ErrNoGitHubFetcher) {
		return nil, httpapi.NotFound(
			httpapi.CodeNotFound, "no github credential covers this repository", nil,
		)
	}
	if err != nil {
		slog.Warn("resolve github repository id for spoke",
			"host", request.PlatformHost, "repo", request.Owner+"/"+request.Name, "err", err,
		)
		return nil, httpapi.Upstream("resolve github repository id failed", "github", request.PlatformHost)
	}
	return &federationGitHubRepositoryIDOutput{
		Body: GitHubRepositoryIDResponse{PlatformRepoID: id, Found: found},
	}, nil
}

func (s *Handlers) FederationRepositoryDescriptor(
	ctx context.Context, input *FederationRepositoryDescriptorInput,
) (*FederationRepositoryDescriptorOutput, error) {
	if err := input.Body.Validate(); err != nil {
		return nil, httpapi.BadRequest(
			httpapi.CodeValidationError, err.Error(), nil,
		)
	}
	observedAt := (*s.Now)().UTC()
	var repo *db.ActiveRepo
	var err error
	if input.Body.Owner == "" && input.Body.Name == "" {
		repo, err = s.Db.GetActiveRepoByProviderID(ctx, platform.RepositoryIdentity{
			Provider: input.Body.Provider, PlatformHost: input.Body.PlatformHost, Key: input.Body.Key,
		})
	} else {
		repo, err = s.RepoResolver.LookupSelection(
			ctx, input.Body.Provider, input.Body.PlatformHost,
			input.Body.Owner, input.Body.Name, input.Body.Key,
		)
	}
	if err != nil && !errors.Is(err, httpapi.ErrRepoNotFound) {
		return nil, httpapi.Internal("resolve repository descriptor failed")
	}
	if repo == nil || errors.Is(err, httpapi.ErrRepoNotFound) {
		return nil, httpapi.NotFound(
			httpapi.CodeRepoNotFound, "repository not found", nil,
		)
	}
	descriptor, err := providerplane.BuildRepositoryDescriptor(
		repositoryDescriptorSnapshot(repo.Repo, observedAt),
	)
	if err != nil {
		return nil, httpapi.Internal("build repository descriptor failed")
	}
	return &FederationRepositoryDescriptorOutput{Body: descriptor}, nil
}

func (s *Handlers) federationDiffDescriptor(
	ctx context.Context, input *federationDiffDescriptorInput,
) (*federationDiffDescriptorOutput, error) {
	if err := input.Body.Repository.Validate(); err != nil {
		return nil, httpapi.BadRequest(
			httpapi.CodeValidationError, err.Error(), nil,
		)
	}
	if input.Body.PullNumber < 1 {
		return nil, httpapi.Validation(
			"body.pull_number", "pull number must be positive",
		)
	}
	observedAt := (*s.Now)().UTC()
	snapshot, err := s.Db.GetPullDiffProviderSnapshot(
		ctx, descriptorDBIdentity(input.Body.Repository), input.Body.PullNumber,
	)
	if err != nil {
		return nil, httpapi.Internal("resolve diff descriptor failed")
	}
	if snapshot == nil {
		return nil, httpapi.NotFound(
			httpapi.CodePullNotFound, "pull request not found", nil,
		)
	}
	diffSHAs := db.DiffSHAs{
		PlatformHeadSHA: snapshot.PlatformHeadSHA,
		PlatformBaseSHA: snapshot.PlatformBaseSHA,
		DiffHeadSHA:     snapshot.DiffHeadSHA,
		DiffBaseSHA:     snapshot.DiffBaseSHA,
		MergeBaseSHA:    snapshot.MergeBaseSHA,
		State:           snapshot.State,
	}
	if strings.TrimSpace(snapshot.PlatformHeadSHA) == "" ||
		strings.TrimSpace(snapshot.PlatformBaseSHA) == "" ||
		strings.TrimSpace(snapshot.DiffHeadSHA) == "" ||
		strings.TrimSpace(snapshot.DiffBaseSHA) == "" ||
		strings.TrimSpace(snapshot.MergeBaseSHA) == "" {
		return nil, httpapi.NotFound(
			httpapi.CodeNotFound,
			"diff metadata is not available for this pull request",
			nil,
		)
	}
	descriptor, err := providerplane.BuildDiffDescriptor(providerplane.DiffSnapshot{
		Repository: repositoryDescriptorSnapshot(snapshot.Repository, observedAt),
		PullNumber: snapshot.PullNumber, SnapshotRevision: uint64(snapshot.SnapshotRevision),
		PlatformHeadSHA: snapshot.PlatformHeadSHA,
		PlatformBaseSHA: snapshot.PlatformBaseSHA,
		DiffHeadSHA:     snapshot.DiffHeadSHA, DiffBaseSHA: snapshot.DiffBaseSHA,
		MergeBaseSHA: snapshot.MergeBaseSHA, Stale: diffSHAs.Stale(),
	})
	if err != nil {
		return nil, httpapi.Internal("build diff descriptor failed")
	}
	return &federationDiffDescriptorOutput{Body: descriptor}, nil
}

func descriptorDBIdentity(route providerplane.RepositoryRoute) db.RepoIdentity {
	return db.RepoIdentity{
		Platform: route.Provider, PlatformHost: route.PlatformHost,
		Owner: route.Owner, Name: route.Name,
		RepoPath: route.Owner + "/" + route.Name,
	}
}

func repositoryDescriptorSnapshot(
	repo db.Repo, observedAt time.Time,
) providerplane.RepositorySnapshot {
	result := providerRepositorySnapshot(repo)
	result.ObservedAt = observedAt.UTC()
	return result
}

func providerRepositorySnapshot(repo db.Repo) providerplane.RepositorySnapshot {
	return providerplane.RepositorySnapshot{
		Provider: repo.Platform, PlatformHost: repo.PlatformHost,
		Key:   repo.Key,
		Owner: repo.Owner, Name: repo.Name,
		CloneURL: repo.CloneURL, DefaultBranch: repo.DefaultBranch,
		Stale: strings.TrimSpace(repo.LastSyncError) != "",
	}
}
