package spokeapi

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"go.kenn.io/forge/internal/apiclient/generated"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/federationauth"
	"go.kenn.io/forge/internal/fleet"
	"go.kenn.io/forge/internal/gitclone"
	"go.kenn.io/forge/internal/mcpserver"
	"go.kenn.io/forge/internal/providerplane"
	"go.kenn.io/forge/internal/server/httpapi"
	"go.kenn.io/forge/internal/server/issueapi"
	"go.kenn.io/forge/internal/server/itemapi"
	"go.kenn.io/forge/internal/server/pullapi"
	"go.kenn.io/forge/internal/server/workspaceapi"
	"go.kenn.io/forge/internal/workspace"
)

type HubProviderSource struct {
	Client  providerplane.Client
	Db      *db.DB
	Clones  *gitclone.Manager
	Enabled func() bool
}

func (s *HubProviderSource) ResolveWorkspaceLaunchSpec(
	ctx context.Context, request providerplane.WorkspaceLaunchRequest,
) (db.WorkspaceLaunchSpec, error) {
	return s.resolveWorkspaceLaunchSpec(
		ctx, request, false,
		federationauth.ScopeProviderRead,
	)
}

func (s *HubProviderSource) resolveWorkspaceLaunchSpec(
	ctx context.Context, request providerplane.WorkspaceLaunchRequest,
	refresh bool, scope federationauth.Scope,
) (db.WorkspaceLaunchSpec, error) {
	canonicalRoute, err := providerplane.CanonicalRepositoryRoute(request.Repository)
	if err != nil {
		return db.WorkspaceLaunchSpec{}, httpapi.BadRequest(
			httpapi.CodeValidationError, err.Error(), nil,
		)
	}
	request.Repository = canonicalRoute
	body := providerLaunchRequestBody(request)
	var httpRequest *http.Request
	if refresh {
		httpRequest, err = generated.NewFederationRefreshWorkspaceLaunchSpecRequest(ctx, "/api/v1", &generated.FederationRefreshWorkspaceLaunchSpecRequestOptions{Body: body})
	} else {
		httpRequest, err = generated.NewFederationResolveWorkspaceLaunchSpecRequest(ctx, "/api/v1", &generated.FederationResolveWorkspaceLaunchSpecRequestOptions{Body: body})
	}
	if err != nil {
		return db.WorkspaceLaunchSpec{}, err
	}
	var spec db.WorkspaceLaunchSpec
	if err := s.exchange(ctx, scope, httpRequest, &spec); err != nil {
		return db.WorkspaceLaunchSpec{}, err
	}
	if err := providerplane.ValidateFederationWorkspaceLaunchSpecResponse(request, spec); err != nil {
		return db.WorkspaceLaunchSpec{}, InvalidHubDescriptor(err)
	}
	credentialRoute, err := requireWorkspaceLaunchSpecCredentials(ctx, s.Clones, spec)
	if err != nil {
		if errors.Is(err, gitclone.ErrCredentialUnavailable) {
			return db.WorkspaceLaunchSpec{}, httpapi.GitCredentialUnavailable(
				credentialRoute.Provider, credentialRoute.PlatformHost,
				credentialRoute.Owner+"/"+credentialRoute.Name,
			)
		}
		return db.WorkspaceLaunchSpec{}, err
	}
	if err := s.observeWorkspaceLaunchSpec(ctx, spec); err != nil {
		return db.WorkspaceLaunchSpec{}, err
	}
	return spec, nil
}

func requireWorkspaceLaunchSpecCredentials(
	ctx context.Context,
	clones *gitclone.Manager,
	spec db.WorkspaceLaunchSpec,
) (providerplane.RepositoryRoute, error) {
	base := providerplane.RepositoryRoute{
		Provider: spec.Repository.Provider, PlatformHost: spec.Repository.PlatformHost,
		Owner: spec.Repository.Owner, Name: spec.Repository.Name,
	}
	if clones == nil {
		return base, gitclone.ErrCredentialUnavailable
	}
	if err := clones.RequireCredentialRoute(
		ctx, base.Provider, base.PlatformHost, base.Owner, base.Name,
	); err != nil {
		return base, err
	}
	if spec.Pull == nil || spec.Pull.HeadRepoKind != "fork" {
		return providerplane.RepositoryRoute{}, nil
	}
	fork, err := providerplane.FederationRemoteRepositoryRoute(
		spec.Repository.Provider, spec.Repository.PlatformHost,
		spec.Pull.HeadRepoCloneURL,
	)
	if err != nil {
		return fork, err
	}
	if err := clones.RequireCredentialRoute(
		ctx, fork.Provider, fork.PlatformHost, fork.Owner, fork.Name,
	); err != nil {
		return fork, err
	}
	return providerplane.RepositoryRoute{}, nil
}

func (s *HubProviderSource) observeWorkspaceLaunchSpec(
	ctx context.Context, spec db.WorkspaceLaunchSpec,
) error {
	if s.Db == nil {
		return httpapi.Internal("repository catalog is unavailable")
	}
	entry, accepted, err := s.Db.ReconcileRepositoryObservation(ctx, db.RepoIdentity{
		Platform: spec.Repository.Provider, PlatformHost: spec.Repository.PlatformHost,
		PlatformRepoID: spec.Repository.PlatformRepoID,
		Owner:          spec.Repository.Owner, Name: spec.Repository.Name,
		RepoPath: spec.Repository.Owner + "/" + spec.Repository.Name,
	}, spec.IssuedAt)
	if err != nil {
		return InvalidHubDescriptor(err)
	}
	if !accepted {
		return InvalidHubDescriptor(
			errors.New("workspace launch repository observation is older than the local route"),
		)
	}
	applied, err := s.Db.UpdateRepoProviderObservation(
		ctx, entry.Repository.ID, spec.IssuedAt,
		db.RepoProviderMetadata{
			PlatformRepoID: spec.Repository.PlatformRepoID,
			CloneURL:       spec.Repository.CloneURL,
			DefaultBranch:  spec.Repository.DefaultBranch,
		}, nil, nil,
	)
	if err != nil {
		return InvalidHubDescriptor(err)
	}
	if !applied {
		return InvalidHubDescriptor(
			errors.New("workspace launch repository observation lost its freshness fence"),
		)
	}
	return nil
}

func (s *HubProviderSource) RefreshWorkspaceLaunchSpec(
	ctx context.Context, current db.WorkspaceLaunchSpec,
) (db.WorkspaceLaunchSpec, error) {
	refreshed, err := s.resolveWorkspaceLaunchSpec(
		ctx, providerplane.WorkspaceLaunchRequest{
			Repository: providerplane.RepositoryRoute{
				Provider:     current.Repository.Provider,
				PlatformHost: current.Repository.PlatformHost,
				Owner:        current.Repository.Owner,
				Name:         current.Repository.Name,
			},
			PlatformRepoID: current.Repository.PlatformRepoID,
			ItemType:       current.ItemType, ItemNumber: current.ItemNumber,
			ItemKey: current.ItemKey, GitHeadRef: current.GitHeadRef,
		}, true,
		federationauth.ScopeProviderWrite)
	if err != nil {
		return db.WorkspaceLaunchSpec{}, err
	}
	if refreshed.Repository.PlatformRepoID != current.Repository.PlatformRepoID ||
		refreshed.GitHeadRef != current.GitHeadRef {
		return db.WorkspaceLaunchSpec{}, InvalidHubDescriptor(
			errors.New("refreshed workspace launch specification changed durable identity"),
		)
	}
	return refreshed, nil
}

func (s *HubProviderSource) ListOpenPullCandidates(
	ctx context.Context, local workspace.Workspace,
) ([]db.MergeRequest, error) {
	rows, err := s.ListPulls(ctx, pullapi.ListQuery{
		Repo: fmt.Sprintf(
			"%s|%s/%s/%s",
			workspaceProviderName(local), local.PlatformHost, local.RepoOwner, local.RepoName,
		),
		State: string(db.MergeRequestStateOpen), Limit: 1000,
	})
	if err != nil {
		return nil, err
	}
	candidates := make([]db.MergeRequest, 0, len(rows))
	for _, row := range rows {
		if !strings.EqualFold(row.Repo.Provider, workspaceProviderName(local)) ||
			!strings.EqualFold(row.PlatformHost, local.PlatformHost) ||
			!strings.EqualFold(row.RepoOwner, local.RepoOwner) ||
			!strings.EqualFold(row.RepoName, local.RepoName) {
			continue
		}
		candidates = append(candidates, row.MergeRequest)
	}
	return candidates, nil
}

func workspaceProviderName(local workspace.Workspace) string {
	provider := strings.TrimSpace(local.Platform)
	if provider == "" {
		return "github"
	}
	return provider
}

func (s *HubProviderSource) GetRepositoryDescriptor(
	ctx context.Context, route providerplane.RepositoryRoute,
) (providerplane.RepositoryDescriptor, error) {
	route, err := providerplane.CanonicalRepositoryRoute(route)
	if err != nil {
		return providerplane.RepositoryDescriptor{}, httpapi.BadRequest(
			httpapi.CodeValidationError, err.Error(), nil,
		)
	}
	var descriptor providerplane.RepositoryDescriptor
	httpRequest, err := generated.NewFederationGetRepositoryDescriptorRequest(ctx, "/api/v1", &generated.FederationGetRepositoryDescriptorRequestOptions{Body: new(generated.RepositoryRoute{Provider: route.Provider, PlatformHost: route.PlatformHost, Owner: route.Owner, Name: route.Name})})
	if err != nil {
		return providerplane.RepositoryDescriptor{}, err
	}
	if err := s.exchange(ctx, federationauth.ScopeProviderRead, httpRequest, &descriptor); err != nil {
		return providerplane.RepositoryDescriptor{}, err
	}
	if err := descriptor.ValidateRoute(route); err != nil {
		return providerplane.RepositoryDescriptor{}, InvalidHubDescriptor(err)
	}
	if err := s.ObserveRepositoryDescriptor(ctx, descriptor); err != nil {
		return providerplane.RepositoryDescriptor{}, err
	}
	return descriptor, nil
}

func (s *HubProviderSource) ResolveRepositoryRoute(
	ctx context.Context, route providerplane.RepositoryRoute,
) (*db.Repo, error) {
	descriptor, err := s.GetRepositoryDescriptor(ctx, route)
	if err != nil {
		return nil, err
	}
	entry, err := s.Db.GetRepositoryByProviderID(
		ctx, descriptor.Provider, descriptor.PlatformHost, descriptor.PlatformRepoID,
	)
	if err != nil {
		return nil, httpapi.Internal("read reconciled repository identity failed")
	}
	if entry == nil {
		return nil, httpapi.Internal("reconciled repository identity is unavailable")
	}
	return &entry.Repository, nil
}

func (s *HubProviderSource) GetDiffDescriptor(
	ctx context.Context, item pullapi.ItemIdentity,
) (providerplane.DiffDescriptor, error) {
	route, err := providerplane.CanonicalRepositoryRoute(providerplane.RepositoryRoute{
		Provider: item.Provider, PlatformHost: item.PlatformHost,
		Owner: item.Owner, Name: item.Name,
	})
	if err != nil {
		return providerplane.DiffDescriptor{}, httpapi.BadRequest(
			httpapi.CodeValidationError, err.Error(), nil,
		)
	}
	var descriptor providerplane.DiffDescriptor
	httpRequest, err := generated.NewFederationGetDiffDescriptorRequest(ctx, "/api/v1", &generated.FederationGetDiffDescriptorRequestOptions{Body: &generated.FederationGetDiffDescriptorBody{Repository: generated.RepositoryRoute{Provider: route.Provider, PlatformHost: route.PlatformHost, Owner: route.Owner, Name: route.Name}, PullNumber: int64(item.Number)}})
	if err != nil {
		return providerplane.DiffDescriptor{}, err
	}
	if err := s.exchange(ctx, federationauth.ScopeProviderRead, httpRequest, &descriptor); err != nil {
		return providerplane.DiffDescriptor{}, err
	}
	if err := descriptor.Validate(); err != nil {
		return providerplane.DiffDescriptor{}, InvalidHubDescriptor(err)
	}
	if err := descriptor.Repository.ValidateRoute(route); err != nil {
		return providerplane.DiffDescriptor{}, InvalidHubDescriptor(err)
	}
	if descriptor.PullNumber != item.Number {
		return providerplane.DiffDescriptor{}, InvalidHubDescriptor(
			fmt.Errorf("diff descriptor does not match requested pull number"),
		)
	}
	if err := s.ObserveRepositoryDescriptor(ctx, descriptor.Repository); err != nil {
		return providerplane.DiffDescriptor{}, err
	}
	return descriptor, nil
}

func (s *HubProviderSource) ObserveRepositoryDescriptor(
	ctx context.Context, descriptor providerplane.RepositoryDescriptor,
) error {
	return observeRepositoryDescriptor(ctx, s.Db, descriptor)
}

func observeRepositoryDescriptor(
	ctx context.Context, database *db.DB,
	descriptor providerplane.RepositoryDescriptor,
) error {
	if database == nil {
		return httpapi.Internal("repository catalog is unavailable")
	}
	entry, accepted, err := database.ReconcileRepositoryObservation(ctx, db.RepoIdentity{
		Platform: descriptor.Provider, PlatformHost: descriptor.PlatformHost,
		PlatformRepoID: descriptor.PlatformRepoID,
		Owner:          descriptor.Owner, Name: descriptor.Name,
		RepoPath: descriptor.Owner + "/" + descriptor.Name,
	}, descriptor.ObservedAt)
	if err != nil {
		return InvalidHubDescriptor(err)
	}
	if !accepted {
		if repositoryDescriptorMatchesEntry(descriptor, entry) {
			return nil
		}
		return InvalidHubDescriptor(
			errors.New("hub repository descriptor is older than the local route observation"),
		)
	}
	applied, err := database.UpdateRepoProviderObservation(
		ctx, entry.Repository.ID, descriptor.ObservedAt,
		db.RepoProviderMetadata{
			PlatformRepoID: descriptor.PlatformRepoID,
			CloneURL:       descriptor.CloneURL,
			DefaultBranch:  descriptor.DefaultBranch,
		}, nil, nil,
	)
	if err != nil {
		return InvalidHubDescriptor(err)
	}
	if !applied {
		current, lookupErr := database.GetRepositoryByProviderID(
			ctx, descriptor.Provider, descriptor.PlatformHost, descriptor.PlatformRepoID,
		)
		if lookupErr == nil && repositoryDescriptorMatchesEntry(descriptor, current) {
			return nil
		}
		return InvalidHubDescriptor(
			errors.New("hub repository descriptor lost its observation fence"),
		)
	}
	return nil
}

func repositoryDescriptorMatchesEntry(
	descriptor providerplane.RepositoryDescriptor,
	entry *db.RepositoryCatalogEntry,
) bool {
	return entry != nil && entry.Lifecycle == db.RepositoryLifecycleActive &&
		entry.Repository.Platform == descriptor.Provider &&
		entry.Repository.PlatformHost == descriptor.PlatformHost &&
		entry.Repository.PlatformRepoID == descriptor.PlatformRepoID &&
		entry.Repository.Owner == descriptor.Owner &&
		entry.Repository.Name == descriptor.Name
}

func InvalidHubDescriptor(error) error {
	return httpapi.NewProblem(
		http.StatusBadGateway,
		httpapi.CodeUpstreamError,
		"hub returned an invalid repository descriptor",
		map[string]any{"reason": "invalidDescriptor"},
	)
}

func (s *HubProviderSource) GetSettings(
	ctx context.Context,
) (ProviderSettingsProjection, error) {
	var response ProviderSettingsResponse
	httpRequest, err := generated.NewFederationGetProviderSettingsRequest(ctx, "/api/v1")
	if err != nil {
		return ProviderSettingsProjection{}, err
	}
	if err := s.exchange(ctx, federationauth.ScopeProviderRead, httpRequest, &response); err != nil {
		return ProviderSettingsProjection{}, err
	}
	return response.projection(), nil
}

func (s *HubProviderSource) UpdateSettings(
	ctx context.Context, update UpdateSettingsRequest,
) (SettingsResponse, error) {
	var response ProviderSettingsResponse
	httpRequest, err := generated.NewFederationUpdateProviderSettingsRequest(ctx, "/api/v1", &generated.FederationUpdateProviderSettingsRequestOptions{Body: providerSettingsRequestBody(update)})
	if err != nil {
		return SettingsResponse{}, err
	}
	if err := s.exchange(ctx, federationauth.ScopeProviderWrite, httpRequest, &response); err != nil {
		return SettingsResponse{}, err
	}
	return response.projection().Settings, nil
}

func (s *HubProviderSource) AutoAssignWorkspaceItem(
	ctx context.Context, request workspaceapi.ProviderWorkspaceItemRequest,
) error {
	var response struct{}
	httpRequest, err := generated.NewFederationAutoAssignWorkspaceItemRequest(ctx, "/api/v1", &generated.FederationAutoAssignWorkspaceItemRequestOptions{Body: &generated.FederationAutoAssignWorkspaceItemBody{Repository: generated.RepositoryRoute{Provider: request.Repository.Provider, PlatformHost: request.Repository.PlatformHost, Owner: request.Repository.Owner, Name: request.Repository.Name}, ItemType: request.ItemType, ItemNumber: int64(request.ItemNumber)}})
	if err != nil {
		return err
	}
	return s.exchangeMutation(ctx, federationauth.ScopeProviderWrite, httpRequest, &response)
}

func (s *HubProviderSource) ListWorkflowStates(
	ctx context.Context, query mcpserver.WorkflowQuery,
) (mcpserver.WorkflowPage, error) {
	var response FederationWorkflowPage
	httpRequest, err := generated.NewFederationListWorkflowStatesRequest(ctx, "/api/v1", &generated.FederationListWorkflowStatesRequestOptions{Body: &generated.FederationListWorkflowStatesBody{Repository: generated.FederationWorkflowRepositoryIdentity{Provider: query.Repository.Provider, PlatformHost: query.Repository.PlatformHost, PlatformRepoID: query.Repository.PlatformRepoID, RepoPath: query.Repository.RepoPath, Owner: query.Repository.Owner, Name: query.Repository.Name}, ItemTypes: append([]string{}, query.ItemTypes...), States: append([]string{}, query.States...), IncludeClosed: query.IncludeClosed, Limit: int64(query.Limit), Cursor: query.Cursor}})
	if err != nil {
		return mcpserver.WorkflowPage{}, err
	}
	if err := s.exchange(ctx, federationauth.ScopeProviderRead, httpRequest, &response); err != nil {
		return mcpserver.WorkflowPage{}, err
	}
	return response.mcp(), nil
}

func (s *HubProviderSource) SetWorkflowState(
	ctx context.Context,
	item mcpserver.ItemIdentity,
	update mcpserver.WorkflowUpdate,
) (mcpserver.WorkflowMutation, error) {
	var response FederationWorkflowMutation
	httpRequest, err := generated.NewFederationSetWorkflowStateRequest(ctx, "/api/v1", &generated.FederationSetWorkflowStateRequestOptions{Body: &generated.FederationSetWorkflowStateBody{Item: generated.FederationWorkflowItemIdentity{Type: item.Type, Provider: item.Provider, PlatformHost: item.PlatformHost, PlatformRepoID: item.PlatformRepoID, Owner: item.Owner, Name: item.Name, Number: int64(item.Number)}, Update: generated.FederationWorkflowUpdate{Status: update.Status, ExpectedStatus: update.ExpectedStatus, Force: update.Force, Source: update.Source, Actor: update.Actor, Reason: update.Reason}}})
	if err != nil {
		return mcpserver.WorkflowMutation{}, err
	}
	if err := s.exchangeMutation(ctx, federationauth.ScopeProviderWrite, httpRequest, &response); err != nil {
		return mcpserver.WorkflowMutation{}, err
	}
	return response.mcp(), nil
}

func (s *HubProviderSource) ListRepositorySummaries(
	ctx context.Context,
) ([]itemapi.RepoSummaryResponse, error) {
	var rows []itemapi.RepoSummaryResponse
	httpRequest, err := generated.NewListRepoSummariesRequest(ctx, "/api/v1")
	if err != nil {
		return nil, err
	}
	if err := s.exchange(ctx, federationauth.ScopeProviderRead, httpRequest, &rows); err != nil {
		return nil, err
	}
	return rows, nil
}

func (s *HubProviderSource) ResolveRepository(
	ctx context.Context, identity mcpserver.RepositoryIdentity,
) (*db.Repo, error) {
	var httpRequest *http.Request
	var err error
	if strings.TrimSpace(identity.PlatformHost) != "" {
		httpRequest, err = generated.NewGetRepoOnHostRequest(ctx, "/api/v1", &generated.GetRepoOnHostRequestOptions{PathParams: &generated.GetRepoOnHostPath{Provider: identity.Provider, Owner: identity.Owner, Name: identity.Name, PlatformHost: identity.PlatformHost}})
	} else {
		httpRequest, err = generated.NewGetRepoRequest(ctx, "/api/v1", &generated.GetRepoRequestOptions{PathParams: &generated.GetRepoPath{Provider: identity.Provider, Owner: identity.Owner, Name: identity.Name}})
	}
	if err != nil {
		return nil, err
	}
	var response itemapi.RepoResponse
	if err := s.exchange(ctx, federationauth.ScopeProviderRead, httpRequest, &response); err != nil {
		return nil, err
	}
	return &db.Repo{
		Platform: response.Platform, PlatformHost: response.PlatformHost,
		PlatformRepoID: response.PlatformRepoID,
		Owner:          response.Owner, Name: response.Name,
		RepoPath: response.Owner + "/" + response.Name,
	}, nil
}

func (s *HubProviderSource) GetPullStack(
	ctx context.Context, item mcpserver.ItemIdentity,
) (pullapi.StackContext, error) {
	var httpRequest *http.Request
	var err error
	if strings.TrimSpace(item.PlatformHost) != "" {
		httpRequest, err = generated.NewGetPullStackOnHostRequest(ctx, "/api/v1", &generated.GetPullStackOnHostRequestOptions{PathParams: &generated.GetPullStackOnHostPath{Provider: item.Provider, Owner: item.Owner, Name: item.Name, Number: int64(item.Number), PlatformHost: item.PlatformHost}})
	} else {
		httpRequest, err = generated.NewGetPullStackRequest(ctx, "/api/v1", &generated.GetPullStackRequestOptions{PathParams: &generated.GetPullStackPath{Provider: item.Provider, Owner: item.Owner, Name: item.Name, Number: int64(item.Number)}})
	}
	if err != nil {
		return pullapi.StackContext{}, err
	}
	var response hubStackResponse
	if err := s.exchange(ctx, federationauth.ScopeProviderRead, httpRequest, &response); err != nil {
		return pullapi.StackContext{}, err
	}
	members := make([]pullapi.StackMember, 0, len(response.Members))
	for _, member := range response.Members {
		members = append(members, pullapi.StackMember{
			Number: member.Number, Title: member.Title, State: member.State,
			CIStatus: member.CIStatus, ReviewDecision: member.ReviewDecision,
			MergeableState: member.MergeableState, Position: member.Position,
			IsDraft: member.IsDraft, BaseBranch: member.BaseBranch,
			BlockedBy: member.BlockedBy,
		})
	}
	return pullapi.StackContext{
		ID: response.StackID, Name: response.StackName,
		Position: response.Position, Size: response.Size,
		Health: response.Health, Members: members,
	}, nil
}

type hubStackResponse struct {
	StackID   int64                    `json:"stack_id"`
	StackName string                   `json:"stack_name"`
	Position  int                      `json:"position"`
	Size      int                      `json:"size"`
	Health    string                   `json:"health"`
	Members   []hubStackMemberResponse `json:"members"`
}

type hubStackMemberResponse struct {
	Number         int    `json:"number"`
	Title          string `json:"title"`
	State          string `json:"state"`
	CIStatus       string `json:"ci_status"`
	ReviewDecision string `json:"review_decision"`
	MergeableState string `json:"mergeable_state"`
	Position       int    `json:"position"`
	IsDraft        bool   `json:"is_draft"`
	BaseBranch     string `json:"base_branch"`
	BlockedBy      *int   `json:"blocked_by"`
}

func (s *HubProviderSource) ListPulls(
	ctx context.Context, query pullapi.ListQuery,
) ([]pullapi.MergeRequestResponse, error) {
	var rows []pullapi.MergeRequestResponse
	httpRequest, err := generated.NewListPullsRequest(ctx, "/api/v1", &generated.ListPullsRequestOptions{Query: &generated.ListPullsQuery{Repo: optionalProviderQuery(query.Repo), State: optionalProviderQuery(query.State), Kanban: optionalProviderQuery(query.Kanban), Starred: optionalProviderQuery(query.Starred), InvolvesMe: optionalProviderQuery(query.InvolvesMe), Unassigned: optionalProviderQuery(query.Unassigned), Q: optionalProviderQuery(query.Text), Label: optionalProviderQuery(query.Label), Limit: optionalProviderQuery(int64(query.Limit)), Offset: optionalProviderQuery(int64(query.Offset))}})
	if err != nil {
		return nil, err
	}
	if err := s.exchange(ctx, federationauth.ScopeProviderRead, httpRequest, &rows); err != nil {
		return nil, err
	}
	return rows, nil
}

func (s *HubProviderSource) GetPull(
	ctx context.Context, item pullapi.ItemIdentity,
) (pullapi.MergeRequestDetailResponse, error) {
	var detail pullapi.MergeRequestDetailResponse
	var httpRequest *http.Request
	var err error
	if strings.TrimSpace(item.PlatformHost) != "" {
		httpRequest, err = generated.NewGetPullOnHostRequest(ctx, "/api/v1", &generated.GetPullOnHostRequestOptions{PathParams: &generated.GetPullOnHostPath{Provider: item.Provider, Owner: item.Owner, Name: item.Name, Number: int64(item.Number), PlatformHost: item.PlatformHost}})
	} else {
		httpRequest, err = generated.NewGetPullRequest(ctx, "/api/v1", &generated.GetPullRequestOptions{PathParams: &generated.GetPullPath{Provider: item.Provider, Owner: item.Owner, Name: item.Name, Number: int64(item.Number)}})
	}
	if err != nil {
		return pullapi.MergeRequestDetailResponse{}, err
	}
	if err := s.exchange(ctx, federationauth.ScopeProviderRead, httpRequest, &detail); err != nil {
		return pullapi.MergeRequestDetailResponse{}, err
	}
	return detail, nil
}

func (s *HubProviderSource) ResolveMergeRequestWorktreeFacts(
	ctx context.Context, route providerplane.RepositoryRoute, number int,
) (workspaceapi.MergeRequestWorktreeFacts, error) {
	detail, err := s.GetPull(ctx, pullapi.ItemIdentity{
		Provider: route.Provider, PlatformHost: route.PlatformHost,
		Owner: route.Owner, Name: route.Name, Number: number,
	})
	if err != nil {
		return workspaceapi.MergeRequestWorktreeFacts{}, err
	}
	if detail.MergeRequest == nil || detail.MergeRequest.Number != number ||
		!strings.EqualFold(detail.Repo.Provider, route.Provider) ||
		!strings.EqualFold(detail.PlatformHost, route.PlatformHost) ||
		!strings.EqualFold(detail.RepoOwner, route.Owner) ||
		!strings.EqualFold(detail.RepoName, route.Name) {
		return workspaceapi.MergeRequestWorktreeFacts{}, InvalidHubDescriptor(
			errors.New("merge request worktree facts do not match the requested item"),
		)
	}
	mr := detail.MergeRequest
	return workspaceapi.MergeRequestWorktreeFacts{
		Number: mr.Number, URL: mr.URL, State: string(mr.State),
		Title: mr.Title, IsDraft: mr.IsDraft, HeadBranch: mr.HeadBranch,
		HeadRepoCloneURL: mr.HeadRepoCloneURL,
		ExpectedHeadSHA:  detail.PlatformHeadSHA,
	}, nil
}

func (s *HubProviderSource) ListIssues(
	ctx context.Context, query issueapi.ListQuery,
) ([]issueapi.IssueResponse, error) {
	var rows []issueapi.IssueResponse
	httpRequest, err := generated.NewListIssuesRequest(ctx, "/api/v1", &generated.ListIssuesRequestOptions{Query: &generated.ListIssuesQuery{Repo: optionalProviderQuery(query.Repo), State: optionalProviderQuery(query.State), Starred: optionalProviderQuery(query.Starred), InvolvesMe: optionalProviderQuery(query.InvolvesMe), Unassigned: optionalProviderQuery(query.Unassigned), ReferencedByPr: optionalProviderQuery(query.ReferencedByPR), Q: optionalProviderQuery(query.Text), Assignee: optionalProviderQuery(query.Assignee), Limit: optionalProviderQuery(int64(query.Limit)), Offset: optionalProviderQuery(int64(query.Offset))}})
	if err != nil {
		return nil, err
	}
	if err := s.exchange(ctx, federationauth.ScopeProviderRead, httpRequest, &rows); err != nil {
		return nil, err
	}
	return rows, nil
}

func (s *HubProviderSource) GetIssue(
	ctx context.Context, item issueapi.ItemIdentity,
) (issueapi.IssueDetailResponse, error) {
	var detail issueapi.IssueDetailResponse
	var httpRequest *http.Request
	var err error
	if strings.TrimSpace(item.PlatformHost) != "" {
		httpRequest, err = generated.NewGetIssueOnHostRequest(ctx, "/api/v1", &generated.GetIssueOnHostRequestOptions{PathParams: &generated.GetIssueOnHostPath{Provider: item.Provider, Owner: item.Owner, Name: item.Name, Number: int64(item.Number), PlatformHost: item.PlatformHost}})
	} else {
		httpRequest, err = generated.NewGetIssueRequest(ctx, "/api/v1", &generated.GetIssueRequestOptions{PathParams: &generated.GetIssuePath{Provider: item.Provider, Owner: item.Owner, Name: item.Name, Number: int64(item.Number)}})
	}
	if err != nil {
		return issueapi.IssueDetailResponse{}, err
	}
	if err := s.exchange(ctx, federationauth.ScopeProviderRead, httpRequest, &detail); err != nil {
		return issueapi.IssueDetailResponse{}, err
	}
	return detail, nil
}

func (s *HubProviderSource) ListActivity(
	ctx context.Context, input *itemapi.ListActivityInput,
) (itemapi.ActivityResponse, error) {
	var response itemapi.ActivityResponse
	httpRequest, err := generated.NewListActivityRequest(ctx, "/api/v1", &generated.ListActivityRequestOptions{Query: &generated.ListActivityQuery{Repo: optionalProviderQuery(input.Repo), Search: optionalProviderQuery(input.Search), Author: optionalProviderQuery(input.Author), InvolvesMe: optionalProviderQuery(input.InvolvesMe), Unassigned: optionalProviderQuery(input.Unassigned), After: optionalProviderQuery(input.After), Before: optionalProviderQuery(input.Before), AtOrBefore: optionalProviderQuery(input.AtOrBefore), Since: optionalProviderQuery(input.Since), Projection: optionalProviderQuery(generated.ListActivityQueryProjection(input.Projection)), Limit: optionalProviderQuery(int64(input.Limit)), HideClosedMerged: optionalProviderQuery(input.HideClosedMerged), HideBots: optionalProviderQuery(input.HideBots), HideDefaultBranch: optionalProviderQuery(input.HideDefaultBranch), Types: input.Types, ItemTypes: input.ItemTypes}})
	if err != nil {
		return itemapi.ActivityResponse{}, err
	}
	if err := s.exchange(ctx, federationauth.ScopeProviderRead, httpRequest, &response); err != nil {
		return itemapi.ActivityResponse{}, err
	}
	return response, nil
}

func (s *HubProviderSource) FilterUnassignedActivitySubjects(
	ctx context.Context, subjects []providerplane.ItemIdentity,
) ([]providerplane.ItemIdentity, error) {
	const batchSize = 500
	result := make([]providerplane.ItemIdentity, 0, len(subjects))
	for start := 0; start < len(subjects); start += batchSize {
		end := min(start+batchSize, len(subjects))
		requestSubjects := make([]generated.FederationActivitySubjectIdentity, 0, end-start)
		for _, subject := range subjects[start:end] {
			requestSubjects = append(
				requestSubjects, generated.FederationActivitySubjectIdentity{Repository: generated.FederationActivityRepositoryIdentity{Provider: subject.Repository.Provider, PlatformHost: subject.Repository.PlatformHost, PlatformRepoID: subject.Repository.PlatformRepoID}, ItemType: subject.ItemType, ItemNumber: int64(subject.ItemNumber)},
			)
		}
		body := &generated.FederationFilterUnassignedActivitySubjectsBody{Subjects: requestSubjects}
		var response FederationUnassignedActivitySubjectsResponse
		httpRequest, err := generated.NewFederationFilterUnassignedActivitySubjectsRequest(ctx, "/api/v1", &generated.FederationFilterUnassignedActivitySubjectsRequestOptions{Body: body})
		if err != nil {
			return nil, err
		}
		if err := s.exchange(ctx, federationauth.ScopeProviderRead, httpRequest, &response); err != nil {
			return nil, err
		}
		for _, subject := range response.Subjects {
			result = append(result, subject.Provider())
		}
	}
	return result, nil
}

func (s *HubProviderSource) ListActivityAuthors(
	ctx context.Context, input *itemapi.ListActivityAuthorsInput,
) (itemapi.ActivityAuthorsResponse, error) {
	var response itemapi.ActivityAuthorsResponse
	httpRequest, err := generated.NewListActivityAuthorsRequest(ctx, "/api/v1", &generated.ListActivityAuthorsRequestOptions{Query: &generated.ListActivityAuthorsQuery{Repo: optionalProviderQuery(input.Repo), Since: optionalProviderQuery(input.Since)}})
	if err != nil {
		return itemapi.ActivityAuthorsResponse{}, err
	}
	if err := s.exchange(ctx, federationauth.ScopeProviderRead, httpRequest, &response); err != nil {
		return itemapi.ActivityAuthorsResponse{}, err
	}
	return response, nil
}

type workspaceProviderStateResponse struct {
	Workspaces []workspaceProviderState `json:"workspaces"`
}

type workspaceProviderState struct {
	ID                 string  `json:"id"`
	ItemLastActivityAt *string `json:"item_last_activity_at,omitempty"`
	MRTitle            *string `json:"mr_title,omitempty"`
	MRState            *string `json:"mr_state,omitempty"`
	MRIsDraft          *bool   `json:"mr_is_draft,omitempty"`
	MRCIStatus         *string `json:"mr_ci_status,omitempty"`
	MRReviewDecision   *string `json:"mr_review_decision,omitempty"`
	MRAdditions        *int    `json:"mr_additions,omitempty"`
	MRDeletions        *int    `json:"mr_deletions,omitempty"`
}

// WorkspaceProviderState returns a copy of workspaces with the hub's provider
// state applied. Workspaces without a stable repository identity have no
// provider item the hub can resolve, so they are returned unchanged.
func (s *HubProviderSource) WorkspaceProviderState(
	ctx context.Context, workspaces []fleet.RawWorkspace,
) ([]fleet.RawWorkspace, error) {
	out := append([]fleet.RawWorkspace(nil), workspaces...)
	subjects := make([]generated.FederationWorkspaceProviderSubject, 0, len(workspaces))
	for _, workspace := range workspaces {
		if strings.TrimSpace(workspace.Repository.PlatformRepoID) == "" {
			continue
		}
		subject := generated.FederationWorkspaceProviderSubject{
			ID: workspace.ID,
			Repository: generated.FederationActivityRepositoryIdentity{
				Provider:       workspace.Repository.Provider,
				PlatformHost:   workspace.Repository.PlatformHost,
				PlatformRepoID: workspace.Repository.PlatformRepoID,
			},
			ItemType: workspace.ItemType, ItemNumber: int64(workspace.ItemNumber),
		}
		if workspace.AssociatedPRNumber != nil {
			subject.AssociatedPrNumber = new(int64(*workspace.AssociatedPRNumber))
		}
		subjects = append(subjects, subject)
	}
	states := make(map[string]workspaceProviderState, len(subjects))
	for start := 0; start < len(subjects); start += 500 {
		end := min(start+500, len(subjects))
		body := &generated.FederationWorkspaceProviderStateRequest{Workspaces: subjects[start:end]}
		httpRequest, err := generated.NewFederationQueryWorkspaceProviderStateRequest(ctx, "/api/v1", &generated.FederationQueryWorkspaceProviderStateRequestOptions{Body: body})
		if err != nil {
			return nil, err
		}
		var response workspaceProviderStateResponse
		if err := s.exchange(ctx, federationauth.ScopeProviderRead, httpRequest, &response); err != nil {
			return nil, err
		}
		for _, state := range response.Workspaces {
			states[state.ID] = state
		}
	}
	for index := range out {
		state, ok := states[out[index].ID]
		if !ok {
			continue
		}
		workspace := &out[index]
		workspace.ItemLastActivityAt = state.ItemLastActivityAt
		workspace.MRTitle, workspace.MRState = state.MRTitle, state.MRState
		workspace.MRIsDraft, workspace.MRCIStatus = state.MRIsDraft, state.MRCIStatus
		workspace.MRReviewDecision = state.MRReviewDecision
		workspace.MRAdditions, workspace.MRDeletions = state.MRAdditions, state.MRDeletions
	}
	return out, nil
}

func (s *HubProviderSource) exchange(ctx context.Context, scope federationauth.Scope, request *http.Request, target any) error {
	return s.exchangeWithProblem(ctx, scope, request, target, hubProviderProblem)
}

func (s *HubProviderSource) exchangeMutation(ctx context.Context, scope federationauth.Scope, request *http.Request, target any) error {
	return s.exchangeWithProblem(ctx, scope, request, target, hubProviderMutationProblem)
}

func (s *HubProviderSource) exchangeWithProblem(ctx context.Context, scope federationauth.Scope, request *http.Request, target any, problem func(error) error) error {
	if s.Client == nil || (s.Enabled != nil && !s.Enabled()) {
		return httpapi.HubUnavailable("provider data is unavailable because fleet federation is disabled or inactive")
	}
	if err := providerplane.ReadJSON(ctx, s.Client, scope, request, target); err != nil {
		return problem(err)
	}
	return nil
}

func hubProviderMutationProblem(err error) error {
	if _, ok := errors.AsType[*providerplane.ResponseError](err); ok ||
		errors.Is(err, providerplane.ErrCredentialUnavailable) ||
		errors.Is(err, providerplane.ErrRequestBodyTooLarge) ||
		errors.Is(err, providerplane.ErrInvalidScope) {
		return hubProviderProblem(err)
	}
	return httpapi.MutationOutcomeUnknown(
		"The federation hub could not confirm whether the provider mutation was applied.",
		"", "",
	)
}

func hubProviderProblem(err error) error {
	if responseErr, ok := errors.AsType[*providerplane.ResponseError](err); ok {
		var problem httpapi.ProblemError
		if json.Unmarshal(responseErr.Body, &problem) == nil && problem.Code != "" {
			problem.Status = responseErr.Status
			return &problem
		}
		return httpapi.NewProblem(
			http.StatusBadGateway,
			httpapi.CodeUpstreamError,
			fmt.Sprintf("hub returned HTTP %d without a valid problem", responseErr.Status),
			nil,
		)
	}
	if errors.Is(err, providerplane.ErrHubUnavailable) ||
		errors.Is(err, providerplane.ErrCredentialUnavailable) {
		return httpapi.HubUnavailable(
			"provider data is unavailable because the federation hub cannot be reached",
		)
	}
	return httpapi.NewProblem(
		http.StatusBadGateway,
		httpapi.CodeUpstreamError,
		"hub returned an invalid provider response",
		nil,
	)
}

func optionalProviderQuery[T comparable](value T) *T {
	var zero T
	if value == zero {
		return nil
	}
	return &value
}

func providerLaunchRequestBody(request providerplane.WorkspaceLaunchRequest) *generated.WorkspaceLaunchRequest {
	return &generated.WorkspaceLaunchRequest{
		Repository: generated.RepositoryRoute{Provider: request.Repository.Provider, PlatformHost: request.Repository.PlatformHost, Owner: request.Repository.Owner, Name: request.Repository.Name}, ItemType: request.ItemType, ItemNumber: int64(request.ItemNumber),
		ItemKey: optionalProviderQuery(request.ItemKey), GitHeadRef: optionalProviderQuery(request.GitHeadRef),
		PlatformRepoID: optionalProviderQuery(request.PlatformRepoID), IssueBranchSlug: optionalProviderQuery(request.IssueBranchSlug),
	}
}

func providerSettingsRequestBody(update UpdateSettingsRequest) *generated.ProviderSettingsUpdate {
	body := &generated.ProviderSettingsUpdate{}
	if value := update.Activity; value != nil {
		body.Activity = &generated.Activity{
			CollapseThreads: value.CollapseThreads, DefaultBranchMaxCommits: int64(value.DefaultBranchMaxCommits),
			DefaultBranchRetentionDays: int64(value.DefaultBranchRetentionDays), HideBots: value.HideBots, HideClosed: value.HideClosed,
			TimeRange: generated.ActivityTimeRange(value.TimeRange), ViewMode: generated.ActivityViewMode(value.ViewMode),
			UseWorkspaceActivityForRecency: value.UseWorkspaceActivityForRecency,
		}
	}
	if value := update.Detail; value != nil {
		body.Detail = &generated.Detail{CollapseSingleLineBreaks: value.CollapseSingleLineBreaks, InitialTimelineEntryLimit: int64(value.InitialTimelineEntryLimit), RenderCommitMessagesAsMarkdown: value.RenderCommitMessagesAsMarkdown}
	}
	if value := update.PullRequests; value != nil {
		body.PullRequests = &generated.PullRequests{AllowMidStackMerges: value.AllowMidStackMerges, PreferGithubNativeStacks: value.PreferGitHubNativeStacks}
	}
	if value := update.Issues; value != nil {
		body.Issues = new(generated.Issues(*value))
	}
	if value := update.Sync; value != nil && value.BudgetPerHour != nil {
		body.Sync = &generated.SyncSettingsUpdate{BudgetPerHour: new(int64(*value.BudgetPerHour))}
	}
	return body
}
