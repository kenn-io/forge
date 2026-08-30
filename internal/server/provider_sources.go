package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/federationauth"
	"go.kenn.io/forge/internal/gitclone"
	"go.kenn.io/forge/internal/mcpserver"
	"go.kenn.io/forge/internal/providerplane"
	"go.kenn.io/forge/internal/server/httpapi"
	"go.kenn.io/forge/internal/server/issueapi"
	"go.kenn.io/forge/internal/server/pullapi"
	"go.kenn.io/forge/internal/server/workspaceapi"
	"go.kenn.io/forge/internal/workspace"
)

type hubProviderSource struct {
	client  providerplane.Client
	db      *db.DB
	clones  *gitclone.Manager
	enabled func() bool
}

func (s *hubProviderSource) ResolveWorkspaceLaunchSpec(
	ctx context.Context, request providerplane.WorkspaceLaunchRequest,
) (db.WorkspaceLaunchSpec, error) {
	return s.resolveWorkspaceLaunchSpec(
		ctx, request, "/api/v1/federation/provider/workspace-launch-spec",
		federationauth.ScopeProviderRead,
	)
}

func (s *hubProviderSource) resolveWorkspaceLaunchSpec(
	ctx context.Context, request providerplane.WorkspaceLaunchRequest,
	path string, scope federationauth.Scope,
) (db.WorkspaceLaunchSpec, error) {
	canonicalRoute, err := providerplane.CanonicalRepositoryRoute(request.Repository)
	if err != nil {
		return db.WorkspaceLaunchSpec{}, httpapi.BadRequest(
			httpapi.CodeValidationError, err.Error(), nil,
		)
	}
	request.Repository = canonicalRoute
	var spec db.WorkspaceLaunchSpec
	if err := s.exchange(
		ctx, http.MethodPost, path, scope, request, &spec,
	); err != nil {
		return db.WorkspaceLaunchSpec{}, err
	}
	if err := providerplane.ValidateFederationWorkspaceLaunchSpecResponse(request, spec); err != nil {
		return db.WorkspaceLaunchSpec{}, invalidHubDescriptor(err)
	}
	credentialRoute, err := requireWorkspaceLaunchSpecCredentials(ctx, s.clones, spec)
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

func (s *hubProviderSource) observeWorkspaceLaunchSpec(
	ctx context.Context, spec db.WorkspaceLaunchSpec,
) error {
	if s.db == nil {
		return httpapi.Internal("repository catalog is unavailable")
	}
	entry, accepted, err := s.db.ReconcileRepositoryObservation(ctx, db.RepoIdentity{
		Platform: spec.Repository.Provider, PlatformHost: spec.Repository.PlatformHost,
		PlatformRepoID: spec.Repository.PlatformRepoID,
		Owner:          spec.Repository.Owner, Name: spec.Repository.Name,
		RepoPath: spec.Repository.Owner + "/" + spec.Repository.Name,
	}, spec.IssuedAt)
	if err != nil {
		return invalidHubDescriptor(err)
	}
	if !accepted {
		return invalidHubDescriptor(
			errors.New("workspace launch repository observation is older than the local route"),
		)
	}
	applied, err := s.db.UpdateRepoProviderObservation(
		ctx, entry.Repository.ID, spec.IssuedAt,
		db.RepoProviderMetadata{
			PlatformRepoID: spec.Repository.PlatformRepoID,
			CloneURL:       spec.Repository.CloneURL,
			DefaultBranch:  spec.Repository.DefaultBranch,
		}, nil, nil,
	)
	if err != nil {
		return invalidHubDescriptor(err)
	}
	if !applied {
		return invalidHubDescriptor(
			errors.New("workspace launch repository observation lost its freshness fence"),
		)
	}
	return nil
}

func (s *hubProviderSource) RefreshWorkspaceLaunchSpec(
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
		}, "/api/v1/federation/provider/workspace-launch-spec/refresh",
		federationauth.ScopeProviderWrite)
	if err != nil {
		return db.WorkspaceLaunchSpec{}, err
	}
	if refreshed.Repository.PlatformRepoID != current.Repository.PlatformRepoID ||
		refreshed.GitHeadRef != current.GitHeadRef {
		return db.WorkspaceLaunchSpec{}, invalidHubDescriptor(
			errors.New("refreshed workspace launch specification changed durable identity"),
		)
	}
	return refreshed, nil
}

func (s *hubProviderSource) ListOpenPullCandidates(
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

func (s *hubProviderSource) GetRepositoryDescriptor(
	ctx context.Context, route providerplane.RepositoryRoute,
) (providerplane.RepositoryDescriptor, error) {
	route, err := providerplane.CanonicalRepositoryRoute(route)
	if err != nil {
		return providerplane.RepositoryDescriptor{}, httpapi.BadRequest(
			httpapi.CodeValidationError, err.Error(), nil,
		)
	}
	var descriptor providerplane.RepositoryDescriptor
	if err := s.exchange(
		ctx, http.MethodPost,
		"/api/v1/federation/provider/repository-descriptor",
		federationauth.ScopeProviderRead, route, &descriptor,
	); err != nil {
		return providerplane.RepositoryDescriptor{}, err
	}
	if err := descriptor.ValidateRoute(route); err != nil {
		return providerplane.RepositoryDescriptor{}, invalidHubDescriptor(err)
	}
	if err := s.observeRepositoryDescriptor(ctx, descriptor); err != nil {
		return providerplane.RepositoryDescriptor{}, err
	}
	return descriptor, nil
}

func (s *hubProviderSource) ResolveProjectRepository(
	ctx context.Context, route providerplane.RepositoryRoute,
) (*db.Repo, error) {
	descriptor, err := s.GetRepositoryDescriptor(ctx, route)
	if err != nil {
		return nil, err
	}
	entry, err := s.db.GetRepositoryByProviderID(
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

func (s *hubProviderSource) GetDiffDescriptor(
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
	if err := s.exchange(
		ctx, http.MethodPost, "/api/v1/federation/provider/diff-descriptor",
		federationauth.ScopeProviderRead,
		federationDiffDescriptorRequest{
			Repository: route, PullNumber: item.Number,
		},
		&descriptor,
	); err != nil {
		return providerplane.DiffDescriptor{}, err
	}
	if err := descriptor.Validate(); err != nil {
		return providerplane.DiffDescriptor{}, invalidHubDescriptor(err)
	}
	if err := descriptor.Repository.ValidateRoute(route); err != nil {
		return providerplane.DiffDescriptor{}, invalidHubDescriptor(err)
	}
	if descriptor.PullNumber != item.Number {
		return providerplane.DiffDescriptor{}, invalidHubDescriptor(
			fmt.Errorf("diff descriptor does not match requested pull number"),
		)
	}
	if err := s.observeRepositoryDescriptor(ctx, descriptor.Repository); err != nil {
		return providerplane.DiffDescriptor{}, err
	}
	return descriptor, nil
}

func (s *hubProviderSource) observeRepositoryDescriptor(
	ctx context.Context, descriptor providerplane.RepositoryDescriptor,
) error {
	return observeRepositoryDescriptor(ctx, s.db, descriptor)
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
		return invalidHubDescriptor(err)
	}
	if !accepted {
		return invalidHubDescriptor(
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
		return invalidHubDescriptor(err)
	}
	if !applied {
		return invalidHubDescriptor(
			errors.New("hub repository descriptor lost its observation fence"),
		)
	}
	return nil
}

func invalidHubDescriptor(error) error {
	return httpapi.NewProblem(
		http.StatusBadGateway,
		httpapi.CodeUpstreamError,
		"hub returned an invalid repository descriptor",
		map[string]any{"reason": "invalidDescriptor"},
	)
}

func (s *hubProviderSource) GetSettings(
	ctx context.Context,
) (providerSettingsProjection, error) {
	var response providerSettingsResponse
	if err := s.read(ctx, "/api/v1/federation/provider/settings", nil, &response); err != nil {
		return providerSettingsProjection{}, err
	}
	return response.projection(), nil
}

func (s *hubProviderSource) UpdateSettings(
	ctx context.Context, update updateSettingsRequest,
) (settingsResponse, error) {
	var response providerSettingsResponse
	if err := s.exchange(
		ctx, http.MethodPut, "/api/v1/federation/provider/settings",
		federationauth.ScopeProviderWrite, providerSettingsUpdateFrom(update), &response,
	); err != nil {
		return settingsResponse{}, err
	}
	return response.projection().Settings, nil
}

func (s *hubProviderSource) AutoAssignWorkspaceItem(
	ctx context.Context, request workspaceapi.ProviderWorkspaceItemRequest,
) error {
	var response struct{}
	return s.exchangeMutation(
		ctx, http.MethodPost, "/api/v1/federation/provider/workspace-auto-assign",
		federationauth.ScopeProviderWrite, request, &response,
	)
}

func (s *hubProviderSource) ListWorkflowStates(
	ctx context.Context, query mcpserver.WorkflowQuery,
) (mcpserver.WorkflowPage, error) {
	var response federationWorkflowPage
	if err := s.exchange(
		ctx, http.MethodPost, "/api/v1/federation/provider/workflow-states/query",
		federationauth.ScopeProviderRead, federationWorkflowQueryFromMCP(query), &response,
	); err != nil {
		return mcpserver.WorkflowPage{}, err
	}
	return response.mcp(), nil
}

func (s *hubProviderSource) SetWorkflowState(
	ctx context.Context,
	item mcpserver.ItemIdentity,
	update mcpserver.WorkflowUpdate,
) (mcpserver.WorkflowMutation, error) {
	body := federationSetWorkflowStateRequest{
		Item: federationWorkflowItemIdentity(item), Update: federationWorkflowUpdate(update),
	}
	var response federationWorkflowMutation
	if err := s.exchangeMutation(
		ctx, http.MethodPut, "/api/v1/federation/provider/workflow-state",
		federationauth.ScopeProviderWrite, body, &response,
	); err != nil {
		return mcpserver.WorkflowMutation{}, err
	}
	return response.mcp(), nil
}

func (s *hubProviderSource) ListRepositorySummaries(
	ctx context.Context,
) ([]repoSummaryResponse, error) {
	var rows []repoSummaryResponse
	if err := s.read(ctx, "/api/v1/repos/summary", nil, &rows); err != nil {
		return nil, err
	}
	return rows, nil
}

func (s *hubProviderSource) ResolveRepository(
	ctx context.Context, identity mcpserver.RepositoryIdentity,
) (*db.Repo, error) {
	path := providerRepositoryPath(
		identity.PlatformHost, identity.Provider, identity.Owner, identity.Name,
	)
	var response repoResponse
	if err := s.read(ctx, path, nil, &response); err != nil {
		return nil, err
	}
	return &db.Repo{
		Platform: response.Platform, PlatformHost: response.PlatformHost,
		PlatformRepoID: response.PlatformRepoID,
		Owner:          response.Owner, Name: response.Name,
		RepoPath: response.Owner + "/" + response.Name,
	}, nil
}

func (s *hubProviderSource) GetPullStack(
	ctx context.Context, item mcpserver.ItemIdentity,
) (pullapi.StackContext, error) {
	path := providerItemPath(
		item.PlatformHost, "pulls", item.Provider, item.Owner, item.Name, item.Number,
	) + "/stack"
	var response hubStackResponse
	if err := s.read(ctx, path, nil, &response); err != nil {
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

func (s *hubProviderSource) ListPulls(
	ctx context.Context, query pullapi.ListQuery,
) ([]pullapi.MergeRequestResponse, error) {
	values := make(url.Values)
	setProviderQuery(values, "repo", query.Repo)
	setProviderQuery(values, "state", query.State)
	setProviderQuery(values, "kanban", query.Kanban)
	setProviderBoolQuery(values, "starred", query.Starred)
	setProviderBoolQuery(values, "involves_me", query.InvolvesMe)
	setProviderQuery(values, "q", query.Text)
	setProviderIntQuery(values, "limit", query.Limit)
	setProviderIntQuery(values, "offset", query.Offset)
	var rows []pullapi.MergeRequestResponse
	if err := s.read(ctx, "/api/v1/pulls", values, &rows); err != nil {
		return nil, err
	}
	return rows, nil
}

func (s *hubProviderSource) GetPull(
	ctx context.Context, item pullapi.ItemIdentity,
) (pullapi.MergeRequestDetailResponse, error) {
	var detail pullapi.MergeRequestDetailResponse
	path := providerItemPath(
		item.PlatformHost, "pulls", item.Provider, item.Owner, item.Name, item.Number,
	)
	if err := s.read(ctx, path, nil, &detail); err != nil {
		return pullapi.MergeRequestDetailResponse{}, err
	}
	return detail, nil
}

func (s *hubProviderSource) ResolveMergeRequestWorktreeFacts(
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
		return workspaceapi.MergeRequestWorktreeFacts{}, invalidHubDescriptor(
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

func (s *hubProviderSource) ListIssues(
	ctx context.Context, query issueapi.ListQuery,
) ([]issueapi.IssueResponse, error) {
	values := make(url.Values)
	setProviderQuery(values, "repo", query.Repo)
	setProviderQuery(values, "state", query.State)
	setProviderBoolQuery(values, "starred", query.Starred)
	setProviderBoolQuery(values, "involves_me", query.InvolvesMe)
	setProviderBoolQuery(values, "referenced_by_pr", query.ReferencedByPR)
	setProviderQuery(values, "q", query.Text)
	setProviderQuery(values, "assignee", query.Assignee)
	setProviderIntQuery(values, "limit", query.Limit)
	setProviderIntQuery(values, "offset", query.Offset)
	var rows []issueapi.IssueResponse
	if err := s.read(ctx, "/api/v1/issues", values, &rows); err != nil {
		return nil, err
	}
	return rows, nil
}

func (s *hubProviderSource) GetIssue(
	ctx context.Context, item issueapi.ItemIdentity,
) (issueapi.IssueDetailResponse, error) {
	var detail issueapi.IssueDetailResponse
	path := providerItemPath(
		item.PlatformHost, "issues", item.Provider, item.Owner, item.Name, item.Number,
	)
	if err := s.read(ctx, path, nil, &detail); err != nil {
		return issueapi.IssueDetailResponse{}, err
	}
	return detail, nil
}

func (s *hubProviderSource) ListActivity(
	ctx context.Context, input *listActivityInput,
) (activityResponse, error) {
	values := make(url.Values)
	setProviderQuery(values, "repo", input.Repo)
	for _, value := range input.Types {
		values.Add("types", value)
	}
	for _, value := range input.ItemTypes {
		values.Add("item_types", value)
	}
	setProviderQuery(values, "search", input.Search)
	setProviderQuery(values, "author", input.Author)
	setProviderBoolQuery(values, "involves_me", input.InvolvesMe)
	setProviderQuery(values, "after", input.After)
	setProviderQuery(values, "before", input.Before)
	setProviderQuery(values, "at_or_before", input.AtOrBefore)
	setProviderQuery(values, "since", input.Since)
	setProviderQuery(values, "projection", input.Projection)
	setProviderIntQuery(values, "limit", input.Limit)
	setProviderBoolQuery(values, "hide_closed_merged", input.HideClosedMerged)
	setProviderBoolQuery(values, "hide_bots", input.HideBots)
	setProviderBoolQuery(values, "hide_default_branch", input.HideDefaultBranch)
	var response activityResponse
	if err := s.read(ctx, "/api/v1/activity", values, &response); err != nil {
		return activityResponse{}, err
	}
	return response, nil
}

func (s *hubProviderSource) ListActivityAuthors(
	ctx context.Context, input *listActivityAuthorsInput,
) (activityAuthorsResponse, error) {
	values := make(url.Values)
	setProviderQuery(values, "repo", input.Repo)
	setProviderQuery(values, "since", input.Since)
	var response activityAuthorsResponse
	if err := s.read(ctx, "/api/v1/activity/authors", values, &response); err != nil {
		return activityAuthorsResponse{}, err
	}
	return response, nil
}

func (s *hubProviderSource) read(
	ctx context.Context, path string, query url.Values, target any,
) error {
	if len(query) > 0 {
		path += "?" + query.Encode()
	}
	return s.exchange(
		ctx, http.MethodGet, path, federationauth.ScopeProviderRead, nil, target,
	)
}

func (s *hubProviderSource) exchange(
	ctx context.Context,
	method, path string,
	scope federationauth.Scope,
	body any,
	target any,
) error {
	return s.exchangeWithProblem(
		ctx, method, path, scope, body, target, hubProviderProblem,
	)
}

func (s *hubProviderSource) exchangeMutation(
	ctx context.Context,
	method, path string,
	scope federationauth.Scope,
	body any,
	target any,
) error {
	return s.exchangeWithProblem(
		ctx, method, path, scope, body, target, hubProviderMutationProblem,
	)
}

func (s *hubProviderSource) exchangeWithProblem(
	ctx context.Context,
	method, path string,
	scope federationauth.Scope,
	body any,
	target any,
	problem func(error) error,
) error {
	if s.client == nil || (s.enabled != nil && !s.enabled()) {
		return httpapi.HubUnavailable(
			"provider data is unavailable because fleet federation is disabled or inactive",
		)
	}
	var requestBody *bytes.Reader
	if body == nil {
		requestBody = bytes.NewReader(nil)
	} else {
		encoded, err := json.Marshal(body)
		if err != nil {
			return httpapi.Internal("encode hub provider request failed")
		}
		requestBody = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, path, requestBody)
	if err != nil {
		return httpapi.Internal("build hub provider request failed")
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if err := providerplane.ReadJSON(
		ctx, s.client, scope, request, target,
	); err != nil {
		return problem(err)
	}
	return nil
}

func hubProviderMutationProblem(err error) error {
	var responseErr *providerplane.ResponseError
	if errors.As(err, &responseErr) ||
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

func providerItemPath(
	platformHost, collection, provider, owner, name string, number int,
) string {
	parts := []string{"api", "v1"}
	if strings.TrimSpace(platformHost) != "" {
		parts = append(parts, "host", platformHost)
	}
	parts = append(parts, collection, provider, owner, name, strconv.Itoa(number))
	for i := range parts {
		parts[i] = url.PathEscape(parts[i])
	}
	return "/" + strings.Join(parts, "/")
}

func providerRepositoryPath(platformHost, provider, owner, name string) string {
	parts := []string{"api", "v1"}
	if strings.TrimSpace(platformHost) != "" {
		parts = append(parts, "host", platformHost)
	}
	parts = append(parts, "repo", provider, owner, name)
	for i := range parts {
		parts[i] = url.PathEscape(parts[i])
	}
	return "/" + strings.Join(parts, "/")
}

func setProviderQuery(values url.Values, key, value string) {
	if value != "" {
		values.Set(key, value)
	}
}

func setProviderBoolQuery(values url.Values, key string, value bool) {
	if value {
		values.Set(key, "true")
	}
}

func setProviderIntQuery(values url.Values, key string, value int) {
	if value != 0 {
		values.Set(key, strconv.Itoa(value))
	}
}
