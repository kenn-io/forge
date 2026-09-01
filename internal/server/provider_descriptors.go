package server

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/providerplane"
	"go.kenn.io/forge/internal/server/httpapi"
)

type federationRepositoryDescriptorInput struct {
	Body providerplane.RepositoryRoute
}

type federationRepositoryDescriptorOutput = httpapi.BodyOutput[providerplane.RepositoryDescriptor]

type federationDiffDescriptorRequest struct {
	Repository providerplane.RepositoryRoute `json:"repository"`
	PullNumber int                           `json:"pull_number" minimum:"1"`
}

type federationDiffDescriptorInput struct {
	Body federationDiffDescriptorRequest
}

type federationDiffDescriptorOutput = httpapi.BodyOutput[providerplane.DiffDescriptor]

func (s *Server) registerProviderDescriptorAPI(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID: "federation-get-repository-descriptor",
		Method:      http.MethodPost,
		Path:        "/federation/provider/repository-descriptor",
		Summary:     "Resolve a repository descriptor for a Forge spoke",
		Tags:        []string{"Fleet"},
	}, s.federationRepositoryDescriptor)
	huma.Register(api, huma.Operation{
		OperationID: "federation-get-diff-descriptor",
		Method:      http.MethodPost,
		Path:        "/federation/provider/diff-descriptor",
		Summary:     "Resolve a pull diff descriptor for a Forge spoke",
		Tags:        []string{"Fleet"},
	}, s.federationDiffDescriptor)
}

func (s *Server) federationRepositoryDescriptor(
	ctx context.Context, input *federationRepositoryDescriptorInput,
) (*federationRepositoryDescriptorOutput, error) {
	if err := input.Body.Validate(); err != nil {
		return nil, httpapi.BadRequest(
			httpapi.CodeValidationError, err.Error(), nil,
		)
	}
	if s.providerDescriptorBeforeSnapshotForTest != nil {
		s.providerDescriptorBeforeSnapshotForTest()
	}
	release, err := s.db.LockRepositoryReconciliationRead(ctx)
	if err != nil {
		return nil, httpapi.Internal("lock repository descriptor snapshot failed")
	}
	defer release()
	// The timestamp and identity snapshot share one reconciliation read lease.
	// A queued route change therefore finishes before both, or after both.
	observedAt := s.now().UTC()
	snapshot, err := s.db.GetRepositoryProviderSnapshotUnderRepositoryReconciliationRead(
		ctx, descriptorDBIdentity(input.Body),
	)
	if err != nil {
		return nil, httpapi.Internal("resolve repository descriptor failed")
	}
	if snapshot == nil {
		return nil, httpapi.NotFound(
			httpapi.CodeRepoNotFound, "repository not found", nil,
		)
	}
	descriptor, err := providerplane.BuildRepositoryDescriptor(
		repositoryDescriptorSnapshot(snapshot, observedAt),
	)
	if err != nil {
		return nil, httpapi.Internal("build repository descriptor failed")
	}
	return &federationRepositoryDescriptorOutput{Body: descriptor}, nil
}

func (s *Server) federationDiffDescriptor(
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
	release, err := s.db.LockRepositoryReconciliationRead(ctx)
	if err != nil {
		return nil, httpapi.Internal("lock diff descriptor snapshot failed")
	}
	defer release()
	// See the repository endpoint: identity and time are ordered together
	// against repository reconciliation.
	observedAt := s.now().UTC()
	snapshot, err := s.db.GetPullDiffProviderSnapshotUnderRepositoryReconciliationRead(
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
		Repository: repositoryDescriptorSnapshot(&snapshot.Repository, observedAt),
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
	snapshot *db.RepositoryProviderSnapshot, observedAt time.Time,
) providerplane.RepositorySnapshot {
	result := providerRepositorySnapshot(snapshot)
	result.ObservedAt = observedAt.UTC()
	return result
}

func providerRepositorySnapshot(
	snapshot *db.RepositoryProviderSnapshot,
) providerplane.RepositorySnapshot {
	repo := snapshot.Repository
	return providerplane.RepositorySnapshot{
		Provider: repo.Platform, PlatformHost: repo.PlatformHost,
		PlatformRepoID: repo.PlatformRepoID,
		Owner:          repo.Owner, Name: repo.Name,
		CloneURL: repo.CloneURL, DefaultBranch: repo.DefaultBranch,
		SnapshotRevision: uint64(snapshot.Route.Generation),
		Stale:            strings.TrimSpace(repo.LastSyncError) != "",
	}
}
