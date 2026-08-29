package server

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/danielgtaylor/huma/v2"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/providerplane"
	"go.kenn.io/forge/internal/server/httpapi"
	"go.kenn.io/forge/internal/workspace"
)

type federationImportReviewDraftInput struct {
	Body db.ProviderStateReviewDraftPayload
}

type federationImportWorkflowStateInput struct {
	Body db.ProviderStateWorkflowPayload
}

type federationProviderStateImportOutput = httpapi.BodyOutput[db.ProviderStateImportResult]

type federationResolveWorkspaceLaunchSpecInput struct {
	Body providerplane.WorkspaceLaunchRequest
}

type federationResolveWorkspaceLaunchSpecOutput = httpapi.BodyOutput[db.WorkspaceLaunchSpec]

type federationRefreshWorkspaceLaunchSpecInput struct {
	Body providerplane.WorkspaceLaunchRequest
}

func (s *Server) registerProviderStateHandoffAPI(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID: "federation-import-review-draft",
		Method:      http.MethodPost,
		Path:        "/federation/provider-state/review-drafts/import",
		Summary:     "Import one review draft while preparing a Forge spoke",
		Tags:        []string{"Fleet"},
	}, s.federationImportReviewDraft)
	huma.Register(api, huma.Operation{
		OperationID: "federation-import-workflow-state",
		Method:      http.MethodPost,
		Path:        "/federation/provider-state/workflow-states/import",
		Summary:     "Import one workflow row while preparing a Forge spoke",
		Tags:        []string{"Fleet"},
	}, s.federationImportWorkflowState)
	huma.Register(api, huma.Operation{
		OperationID: "federation-resolve-workspace-launch-spec",
		Method:      http.MethodPost,
		Path:        "/federation/provider/workspace-launch-spec",
		Summary:     "Resolve current provider facts for a workspace launch",
		Tags:        []string{"Fleet"},
	}, s.federationResolveWorkspaceLaunchSpec)
	huma.Register(api, huma.Operation{
		OperationID: "federation-refresh-workspace-launch-spec",
		Method:      http.MethodPost,
		Path:        "/federation/provider/workspace-launch-spec/refresh",
		Summary:     "Refresh provider facts for a Forge spoke workspace",
		Tags:        []string{"Fleet"},
	}, s.federationRefreshWorkspaceLaunchSpec)
}

func (s *Server) federationImportReviewDraft(
	ctx context.Context,
	input *federationImportReviewDraftInput,
) (*federationProviderStateImportOutput, error) {
	result, err := s.db.ImportProviderReviewDraft(ctx, input.Body)
	if err != nil {
		return nil, providerStateHandoffProblem(err)
	}
	return &federationProviderStateImportOutput{Body: result}, nil
}

func (s *Server) federationImportWorkflowState(
	ctx context.Context,
	input *federationImportWorkflowStateInput,
) (*federationProviderStateImportOutput, error) {
	result, err := s.db.ImportProviderWorkflowState(ctx, input.Body)
	if err != nil {
		return nil, providerStateHandoffProblem(err)
	}
	return &federationProviderStateImportOutput{Body: result}, nil
}

func providerStateHandoffProblem(err error) error {
	if errors.Is(err, db.ErrSpokePreparationConflict) {
		return httpapi.Conflict(httpapi.CodeConflict, err.Error(), map[string]any{
			"reason": "providerStateConflict",
		})
	}
	return httpapi.BadRequest(httpapi.CodeBadRequest, err.Error(), nil)
}

func (s *Server) federationResolveWorkspaceLaunchSpec(
	ctx context.Context,
	input *federationResolveWorkspaceLaunchSpecInput,
) (*federationResolveWorkspaceLaunchSpecOutput, error) {
	spec, err := s.ResolveWorkspaceLaunchSpec(ctx, input.Body)
	if err != nil {
		return nil, err
	}
	if err := providerplane.ValidateFederationWorkspaceLaunchSpecResponse(input.Body, spec); err != nil {
		return nil, httpapi.Conflict(httpapi.CodeConflict, err.Error(), map[string]any{
			"reason": "launchSpecUnavailable",
		})
	}
	return &federationResolveWorkspaceLaunchSpecOutput{Body: spec}, nil
}

func (s *Server) federationRefreshWorkspaceLaunchSpec(
	ctx context.Context, input *federationRefreshWorkspaceLaunchSpecInput,
) (*federationResolveWorkspaceLaunchSpecOutput, error) {
	if err := s.workspaceAPI.RefreshProviderWorkspaceFacts(ctx, input.Body); err != nil {
		return nil, err
	}
	return s.federationResolveWorkspaceLaunchSpec(
		ctx, &federationResolveWorkspaceLaunchSpecInput{Body: input.Body},
	)
}

// ResolveWorkspaceLaunchSpec resolves provider facts from the hub's
// authoritative store. It also implements providerplane's local resolver for
// hub-owned workspace lifecycle operations.
func (s *Server) ResolveWorkspaceLaunchSpec(
	ctx context.Context, request providerplane.WorkspaceLaunchRequest,
) (db.WorkspaceLaunchSpec, error) {
	route, err := providerplane.CanonicalRepositoryRoute(request.Repository)
	if err != nil {
		return db.WorkspaceLaunchSpec{}, httpapi.BadRequest(
			httpapi.CodeValidationError, err.Error(), nil,
		)
	}
	request.Repository = route
	var repo *db.Repo
	if platformRepoID := strings.TrimSpace(request.PlatformRepoID); platformRepoID != "" {
		entry, lookupErr := s.db.GetRepositoryByProviderID(
			ctx, route.Provider, route.PlatformHost, platformRepoID,
		)
		if lookupErr != nil {
			return db.WorkspaceLaunchSpec{}, httpapi.ProviderRouteLookupError(lookupErr)
		}
		if entry != nil && entry.Lifecycle == db.RepositoryLifecycleActive {
			repo = &entry.Repository
		}
	} else {
		repo, err = s.repoResolver.LookupRoute(
			ctx, route.Provider, route.PlatformHost, route.Owner, route.Name,
		)
		if err != nil {
			return db.WorkspaceLaunchSpec{}, httpapi.ProviderRouteLookupError(err)
		}
	}
	if repo == nil || strings.TrimSpace(repo.PlatformRepoID) == "" ||
		strings.TrimSpace(repo.CloneURL) == "" ||
		strings.TrimSpace(repo.DefaultBranch) == "" {
		return db.WorkspaceLaunchSpec{}, httpapi.NotFound(
			httpapi.CodeRepoNotFound,
			"repository does not have complete provider launch metadata", nil,
		)
	}
	issuedAt := s.now().UTC()
	spec := db.WorkspaceLaunchSpec{
		Version: db.WorkspaceLaunchSpecVersion,
		Repository: db.WorkspaceLaunchRepository{
			Provider: repo.Platform, PlatformHost: repo.PlatformHost,
			PlatformRepoID: repo.PlatformRepoID, Owner: repo.Owner, Name: repo.Name,
			CloneURL: repo.CloneURL, DefaultBranch: repo.DefaultBranch,
		},
		ItemType: request.ItemType, ItemNumber: request.ItemNumber,
		ItemKey: request.ItemKey, GitHeadRef: strings.TrimSpace(request.GitHeadRef),
		IssuedAt:           issuedAt,
		SourceVisibleUntil: issuedAt.Add(db.WorkspaceLaunchSpecVisibilityLease),
	}
	if spec.ItemKey == "" {
		spec.ItemKey = strconv.Itoa(spec.ItemNumber)
	}
	switch spec.ItemType {
	case db.WorkspaceItemTypePullRequest:
		pull, readErr := s.db.GetMergeRequestByRepoIDAndNumber(ctx, repo.ID, spec.ItemNumber)
		if readErr != nil {
			return db.WorkspaceLaunchSpec{}, httpapi.Internal("read pull request launch facts: " + readErr.Error())
		}
		if pull == nil {
			return db.WorkspaceLaunchSpec{}, httpapi.NotFound(httpapi.CodePullNotFound, "pull request not found", nil)
		}
		visible, readErr := s.db.GetVisibleMergeRequestByRepoIDAndNumber(ctx, repo.ID, spec.ItemNumber)
		if readErr != nil {
			return db.WorkspaceLaunchSpec{}, httpapi.Internal("read pull request visibility: " + readErr.Error())
		}
		spec.SourceVisible = visible != nil
		spec.SourceTitle = pull.Title
		spec.SourceURL = pull.URL
		if spec.GitHeadRef == "" {
			spec.GitHeadRef = pull.HeadBranch
		}
		headRepo := workspace.WorkspaceHeadRepo(
			repo.Platform, repo.PlatformHost, repo.Owner, repo.Name,
			pull.HeadRepoCloneURL,
		)
		launchPull := &db.WorkspaceLaunchPull{
			HeadBranch: pull.HeadBranch, SnapshotRevision: pull.SnapshotRevision,
		}
		switch {
		case headRepo == nil:
			launchPull.HeadRepoKind = "same_repo"
		case *headRepo == "":
			launchPull.HeadRepoKind = "unknown"
		default:
			launchPull.HeadRepoKind = "fork"
			launchPull.HeadRepoCloneURL = *headRepo
		}
		spec.Pull = launchPull
	case db.WorkspaceItemTypeIssue:
		issue, readErr := s.db.GetIssueByRepoIDAndNumber(ctx, repo.ID, spec.ItemNumber)
		if readErr != nil {
			return db.WorkspaceLaunchSpec{}, httpapi.Internal("read issue launch facts: " + readErr.Error())
		}
		if issue == nil {
			return db.WorkspaceLaunchSpec{}, httpapi.NotFound(httpapi.CodeIssueNotFound, "issue not found", nil)
		}
		visible, readErr := s.db.GetVisibleIssueByRepoIDAndNumber(ctx, repo.ID, spec.ItemNumber)
		if readErr != nil {
			return db.WorkspaceLaunchSpec{}, httpapi.Internal("read issue visibility: " + readErr.Error())
		}
		spec.SourceVisible = visible != nil
		spec.SourceTitle = issue.Title
		spec.SourceURL = issue.URL
		if spec.GitHeadRef == "" {
			spec.GitHeadRef = workspace.IssueWorkspaceBranch(
				spec.ItemNumber, issue.Title, request.IssueBranchSlug,
			)
		}
	default:
		return db.WorkspaceLaunchSpec{}, httpapi.Validation("body.item_type", "item type must be pull_request or issue")
	}
	if err := providerplane.ValidateWorkspaceLaunchSpecResponse(request, spec); err != nil {
		return db.WorkspaceLaunchSpec{}, httpapi.Conflict(httpapi.CodeConflict, err.Error(), map[string]any{
			"reason": "launchSpecUnavailable",
		})
	}
	return spec, nil
}

// RefreshWorkspaceLaunchSpec renews visibility while preserving the durable
// workspace identity chosen at creation time.
func (s *Server) RefreshWorkspaceLaunchSpec(
	ctx context.Context, current db.WorkspaceLaunchSpec,
) (db.WorkspaceLaunchSpec, error) {
	refreshed, err := s.ResolveWorkspaceLaunchSpec(ctx, providerplane.WorkspaceLaunchRequest{
		Repository: providerplane.RepositoryRoute{
			Provider:     current.Repository.Provider,
			PlatformHost: current.Repository.PlatformHost,
			Owner:        current.Repository.Owner,
			Name:         current.Repository.Name,
		},
		PlatformRepoID: current.Repository.PlatformRepoID,
		ItemType:       current.ItemType, ItemNumber: current.ItemNumber,
		ItemKey: current.ItemKey, GitHeadRef: current.GitHeadRef,
	})
	if err != nil {
		return db.WorkspaceLaunchSpec{}, err
	}
	if current.Repository.PlatformRepoID != "" &&
		refreshed.Repository.PlatformRepoID != current.Repository.PlatformRepoID {
		return db.WorkspaceLaunchSpec{}, httpapi.Conflict(
			httpapi.CodeConflict,
			"workspace repository identity changed while refreshing launch facts",
			map[string]any{"reason": "launchSpecRepositoryChanged"},
		)
	}
	return refreshed, nil
}
