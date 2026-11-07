package server

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/mcpserver"
	"go.kenn.io/forge/internal/providerplane"
	"go.kenn.io/forge/internal/server/httpapi"
	"go.kenn.io/forge/internal/server/issueapi"
	"go.kenn.io/forge/internal/server/itemapi"
	"go.kenn.io/forge/internal/server/mcpapi"
	"go.kenn.io/forge/internal/server/pullapi"
	"go.kenn.io/forge/internal/server/workspaceapi"
	"go.kenn.io/forge/platform"
)

func (s *Server) MCPBackend() mcpserver.Backend {
	backend := mcpBackend{server: s}
	return mcpserver.NewFederatedBackend(backend, backend)
}

type mcpBackend struct {
	server *Server
}

// localPulls returns the hub's pull request handler. Execution workers never
// have a hub, so they serve MCP without this handler.
func (b mcpBackend) localPulls() (*pullapi.Handler, error) {
	if b.server.pullAPI == nil {
		return nil, mcpapi.McpBackendError(httpapi.ServiceUnavailable(
			"pull requests are not available on this server",
		))
	}
	return b.server.pullAPI, nil
}

// localIssues returns the hub's issue handler. Execution workers never
// have a hub, so they serve MCP without this handler.
func (b mcpBackend) localIssues() (*issueapi.Handler, error) {
	if b.server.issueAPI == nil {
		return nil, mcpapi.McpBackendError(httpapi.ServiceUnavailable(
			"issues are not available on this server",
		))
	}
	return b.server.issueAPI, nil
}

func (b mcpBackend) ListRepositories(ctx context.Context) ([]mcpserver.RepositorySummary, error) {
	var (
		rows []itemapi.RepoSummaryResponse
		err  error
	)
	if b.server.providerSource != nil {
		rows, err = b.server.providerSource.ListRepositorySummaries(ctx)
	} else {
		if b.server.syncer == nil {
			return nil, mcpapi.McpBackendError(httpapi.ServiceUnavailable(
				"repositories are not available on this server",
			))
		}
		rows, err = b.server.activityapi.ListRepoSummariesService(ctx)
	}
	if err != nil {
		return nil, mcpapi.McpBackendError(err)
	}
	out := make([]mcpserver.RepositorySummary, 0, len(rows))
	for _, row := range rows {
		out = append(out, mcpserver.RepositorySummary{
			Repository:  mcpapi.RepositoryIdentityFromResponse(row.Repo),
			OpenPRCount: row.OpenPRCount, OpenIssueCount: row.OpenIssueCount,
			LastSyncCompletedAt: row.LastSyncCompletedAt, LastSyncError: row.LastSyncError,
		})
	}
	return out, nil
}

// ResolveRepositoryRoute resolves a route an MCP caller named without a
// provider ID to the active repository occupying it, through the hub when
// this node has one.
func (b mcpBackend) ResolveRepositoryRoute(
	ctx context.Context, identity mcpserver.RepositoryIdentity,
) (mcpserver.RepositoryIdentity, error) {
	if b.server.providerSource != nil {
		repo, err := b.server.providerSource.ResolveRepository(ctx, identity)
		if err != nil {
			return mcpserver.RepositoryIdentity{}, mcpapi.McpBackendError(err)
		}
		return mcpapi.RepositoryIdentityFromRepo(*repo), nil
	}
	entry, err := b.server.db.ResolveActiveRepositoryRoute(ctx, db.RepoIdentity{
		Platform: identity.Provider, PlatformHost: identity.PlatformHost,
		Owner: identity.Owner, Name: identity.Name, RepoPath: identity.RepoPath,
	})
	if err != nil {
		return mcpserver.RepositoryIdentity{}, mcpapi.McpBackendError(httpapi.Internal("resolve repository route failed"))
	}
	if entry == nil {
		return mcpserver.RepositoryIdentity{}, mcpapi.McpRepositoryRouteNotFoundError(identity)
	}
	return mcpapi.RepositoryIdentityFromRepo(entry.Repository), nil
}

func (b mcpBackend) ListActivity(
	ctx context.Context, query mcpserver.ActivityQuery,
) (mcpserver.ActivityPage, error) {
	var resolved mcpapi.ResolvedMCPRepository
	if query.Repository.Provider != "" {
		var err error
		resolved, err = b.resolveProviderRepository(ctx, query.Repository)
		if err != nil {
			return mcpserver.ActivityPage{}, err
		}
		query.Repository = mcpapi.RepositoryAtResolvedRoute(query.Repository, resolved.Repo)
	}
	body, err := b.server.activityapi.ListActivityService(ctx, &itemapi.ListActivityInput{
		Repo: mcpapi.McpRepositoryFilter(query.Repository), Types: query.ActivityTypes,
		ItemTypes: query.ItemTypes, Search: query.Search, After: query.After, Since: query.Since,
	})
	if err != nil {
		return mcpserver.ActivityPage{}, mcpapi.McpBackendError(err)
	}
	out := mcpserver.ActivityPage{
		Items: make([]mcpserver.ActivityItem, 0, len(body.Items)), Capped: body.Capped,
	}
	for _, row := range body.Items {
		item := mcpserver.ActivityItem{
			ID: row.ID, Cursor: row.Cursor, ActivityType: row.ActivityType,
			Repository: mcpserver.RepositoryIdentity{
				Provider: row.Repo.Provider, PlatformHost: row.Repo.PlatformHost,
				Key:      row.Repo.Key,
				RepoPath: row.Repo.RepoPath, Owner: row.Repo.Owner, Name: row.Repo.Name,
			},
			ItemType: row.ItemType, ItemNumber: row.ItemNumber,
			ItemTitle: row.ItemTitle, ItemURL: row.ItemURL, ItemState: row.ItemState,
			Author: row.Author, CommitAuthor: row.CommitAuthor, ItemAuthor: row.ItemAuthor, CreatedAt: row.CreatedAt,
			BodyPreview: row.BodyPreview, BranchName: row.BranchName,
			CommitSHA: row.CommitSHA, BeforeSHA: row.BeforeSHA, AfterSHA: row.AfterSHA,
			AuthorName: row.AuthorName, AuthorEmail: row.AuthorEmail,
			CommitterName: row.CommitterName, CommitterEmail: row.CommitterEmail,
			AuthoredAt: row.AuthoredAt, CommittedAt: row.CommittedAt,
			ActivityURL: row.ActivityURL, SubjectState: row.SubjectState,
		}
		item.Workspace = mcpapi.McpWorkspaceRef(row.Workspace)
		out.Items = append(out.Items, item)
	}
	return out, nil
}

func (b mcpBackend) ListPulls(
	ctx context.Context, query mcpserver.ItemListQuery,
) ([]mcpserver.Pull, error) {
	pulls, err := b.localPulls()
	if err != nil {
		return nil, err
	}
	var resolved mcpapi.ResolvedMCPRepository
	if query.Repository.Provider != "" {
		resolved, err = b.resolveProviderRepository(ctx, query.Repository)
		if err != nil {
			return nil, err
		}
		query.Repository = mcpapi.RepositoryAtResolvedRoute(query.Repository, resolved.Repo)
	}
	rows, err := pulls.ListService(ctx, pullapi.ListQuery{
		Numbers: query.Numbers,
		Repo:    mcpapi.McpRepositoryFilter(query.Repository), State: query.State,
		Text: query.Text, Semantic: query.Semantic, Label: query.Label, Attributes: pullListAttributes(query), Limit: query.Limit, Offset: query.Offset,
	})
	if err != nil {
		return nil, mcpapi.McpBackendError(err)
	}
	out := make([]mcpserver.Pull, 0, len(rows))
	for _, row := range rows {
		out = append(out, mcpapi.McpPull(row))
	}
	return out, nil
}

// pullListAttributes returns the pull list inclusion filters a query asks
// for.
func pullListAttributes(query mcpserver.ItemListQuery) string {
	if query.MergeQueue {
		return "merge_queue"
	}
	return ""
}

func (b mcpBackend) ListIssues(
	ctx context.Context, query mcpserver.ItemListQuery,
) ([]mcpserver.Issue, error) {
	issues, err := b.localIssues()
	if err != nil {
		return nil, err
	}
	var resolved mcpapi.ResolvedMCPRepository
	if query.Repository.Provider != "" {
		resolved, err = b.resolveProviderRepository(ctx, query.Repository)
		if err != nil {
			return nil, err
		}
		query.Repository = mcpapi.RepositoryAtResolvedRoute(query.Repository, resolved.Repo)
	}
	rows, err := issues.ListService(ctx, issueapi.ListQuery{
		Numbers: query.Numbers,
		Repo:    mcpapi.McpRepositoryFilter(query.Repository), State: query.State,
		Text: query.Text, Semantic: query.Semantic, Limit: query.Limit, Offset: query.Offset,
	})
	if err != nil {
		return nil, mcpapi.McpBackendError(err)
	}
	out := make([]mcpserver.Issue, 0, len(rows))
	for _, row := range rows {
		out = append(out, mcpapi.McpIssue(row))
	}
	return out, nil
}

func (b mcpBackend) GetPull(
	ctx context.Context, item mcpserver.ItemIdentity,
) (mcpserver.PullDetail, error) {
	pulls, err := b.localPulls()
	if err != nil {
		return mcpserver.PullDetail{}, err
	}
	resolved, err := b.resolveProviderRepository(ctx, item.Repository())
	if err != nil {
		return mcpserver.PullDetail{}, err
	}
	detail, err := pulls.GetService(ctx, mcpapi.PullServiceIdentity(mcpapi.ItemAtResolvedRoute(item, resolved.Repo)))
	if err != nil {
		return mcpserver.PullDetail{}, mcpapi.McpBackendError(err)
	}
	return mcpPullDetail(detail), nil
}

// GetPulls resolves each distinct repository once. On a spoke every
// resolution is a hub round trip, so resolving per pull doubled the requests.
func (b mcpBackend) GetPulls(
	ctx context.Context, items []mcpserver.ItemIdentity,
) ([]mcpserver.PullDetail, error) {
	pulls, err := b.localPulls()
	if err != nil {
		return nil, err
	}
	routes := map[mcpserver.RepositoryIdentity]*db.Repo{}
	identities := make([]pullapi.ItemIdentity, 0, len(items))
	for _, item := range items {
		identity := item.Repository()
		repo, ok := routes[identity]
		if !ok {
			resolved, err := b.resolveProviderRepository(ctx, identity)
			if err != nil {
				return nil, fmt.Errorf("PR %d context: %w", item.Number, err)
			}
			repo = resolved.Repo
			routes[identity] = repo
		}
		identities = append(identities, mcpapi.PullServiceIdentity(mcpapi.ItemAtResolvedRoute(item, repo)))
	}
	details, err := pulls.GetServices(ctx, identities)
	if err != nil {
		return nil, mcpapi.McpBackendError(err)
	}
	out := make([]mcpserver.PullDetail, 0, len(details))
	for _, detail := range details {
		out = append(out, mcpPullDetail(detail))
	}
	return out, nil
}

func mcpPullDetail(detail pullapi.MergeRequestDetailResponse) mcpserver.PullDetail {
	out := mcpserver.PullDetail{
		DetailLoaded: detail.DetailLoaded, DetailFetchedAt: detail.DetailFetchedAt,
		Workspace: mcpapi.McpWorkspaceRef(detail.Workspace),
		Events:    make([]mcpserver.DetailEvent, 0, len(detail.Events)),
		Checks:    make([]mcpserver.Check, 0, len(detail.Checks)),
	}
	if detail.MergeRequest != nil {
		pull := mcpserver.Pull{
			Labels:         mcpapi.McpLabelNames(detail.MergeRequest.Labels),
			MergeableState: detail.MergeRequest.MergeableState,
			ReviewDecision: detail.MergeRequest.ReviewDecision,
			CIStatus:       detail.MergeRequest.CIStatus, HeadSHA: detail.MergeRequest.PlatformHeadSHA,
			Number: detail.MergeRequest.Number, Title: detail.MergeRequest.Title,
			State: string(detail.MergeRequest.State), Author: detail.MergeRequest.Author,
			URL: detail.MergeRequest.URL, IsDraft: detail.MergeRequest.IsDraft,
			Body:           detail.MergeRequest.Body,
			LastActivityAt: detail.MergeRequest.LastActivityAt,
			Repository:     mcpapi.RepositoryIdentityFromResponse(detail.Repo),
			Workspace:      mcpapi.McpWorkspaceRef(detail.Workspace),
			DetailLoaded:   detail.DetailLoaded, DetailFetchedAt: detail.DetailFetchedAt,
		}
		pull.MergeQueue, pull.MergeQueueRemoval = mcpapi.McpMergeQueue(detail.MergeQueue, detail.MergeQueueRemoval)
		out.Pull = &pull
	}
	if detail.MergeQueueTarget != nil {
		out.MergeQueueTarget = &mcpserver.MergeQueueTarget{
			Branch: detail.MergeQueueTarget.Branch, MergeMethod: detail.MergeQueueTarget.MergeMethod,
		}
	}
	for _, event := range detail.Events {
		out.Events = append(out.Events, mcpserver.DetailEvent{
			EventType: event.EventType, Author: event.Author, Summary: event.Summary,
			Body: event.Body, CreatedAt: event.CreatedAt,
		})
	}
	if detail.Stack != nil {
		stack := mcpserver.Stack{
			Position: detail.Stack.Position, Size: detail.Stack.Size,
			Health:  detail.Stack.Health,
			Members: make([]mcpserver.StackMember, 0, len(detail.Stack.Members)),
		}
		for _, member := range detail.Stack.Members {
			stack.Members = append(stack.Members, mcpserver.StackMember{
				Number: member.Number, Title: member.Title, State: member.State,
				Position: member.Position, IsDraft: member.IsDraft,
			})
		}
		out.Stack = &stack
	}
	out.Checks = mcpapi.McpChecks(detail.Checks)
	return out
}

func (b mcpBackend) GetIssue(
	ctx context.Context, item mcpserver.ItemIdentity,
) (mcpserver.IssueDetail, error) {
	issues, err := b.localIssues()
	if err != nil {
		return mcpserver.IssueDetail{}, err
	}
	resolved, err := b.resolveProviderRepository(ctx, item.Repository())
	if err != nil {
		return mcpserver.IssueDetail{}, err
	}
	item = mcpapi.ItemAtResolvedRoute(item, resolved.Repo)
	detail, err := issues.GetService(ctx, mcpapi.IssueServiceIdentity(item))
	if err != nil {
		return mcpserver.IssueDetail{}, mcpapi.McpBackendError(err)
	}
	out := mcpserver.IssueDetail{
		DetailLoaded: detail.DetailLoaded, DetailFetchedAt: detail.DetailFetchedAt,
		Workspace: mcpapi.McpWorkspaceRef(detail.Workspace),
		Events:    make([]mcpserver.DetailEvent, 0, len(detail.Events)),
	}
	if detail.Issue != nil {
		issue := mcpserver.Issue{
			Number: detail.Issue.Number, Title: detail.Issue.Title,
			State: detail.Issue.State, Author: detail.Issue.Author, URL: detail.Issue.URL,
			Body:           detail.Issue.Body,
			LastActivityAt: detail.Issue.LastActivityAt,
			Repository:     mcpapi.RepositoryIdentityFromResponse(detail.Repo),
			Workspace:      mcpapi.McpWorkspaceRef(detail.Workspace),
			DetailLoaded:   detail.DetailLoaded, DetailFetchedAt: detail.DetailFetchedAt,
		}
		out.Issue = &issue
	}
	for _, event := range detail.Events {
		out.Events = append(out.Events, mcpserver.DetailEvent{
			EventType: event.EventType, Author: event.Author, Summary: event.Summary,
			Body: event.Body, CreatedAt: event.CreatedAt,
		})
	}
	return out, nil
}

func (b mcpBackend) GetPullDiff(
	ctx context.Context, item mcpserver.ItemIdentity, includePatches bool,
) (mcpserver.Diff, error) {
	pulls, err := b.localPulls()
	if err != nil {
		return mcpserver.Diff{}, err
	}
	resolved, err := b.resolveMCPRepository(ctx, item.Repository())
	if err != nil {
		return mcpserver.Diff{}, err
	}
	item = mcpapi.ItemAtResolvedRoute(item, resolved.Repo)
	identity := mcpapi.PullServiceIdentity(item)
	var (
		stale bool
		files []mcpserver.DiffFile
	)
	if includePatches {
		result, err := pulls.GetDiffService(ctx, identity, pullapi.DiffQuery{})
		if err != nil {
			return mcpserver.Diff{}, mcpapi.McpBackendError(err)
		}
		stale = result.Stale
		files = mcpapi.McpDiffFiles(result.Files)
	} else {
		result, err := pulls.GetFilesService(ctx, identity)
		if err != nil {
			return mcpserver.Diff{}, mcpapi.McpBackendError(err)
		}
		stale = result.Stale
		files = mcpapi.McpDiffFiles(result.Files)
	}
	return mcpserver.Diff{Stale: stale, Files: files}, nil
}

func (b mcpBackend) GetPullStack(
	ctx context.Context, item mcpserver.ItemIdentity,
) (mcpserver.Stack, error) {
	pulls, err := b.localPulls()
	if err != nil {
		return mcpserver.Stack{}, err
	}
	resolved, err := b.resolveProviderRepository(ctx, item.Repository())
	if err != nil {
		return mcpserver.Stack{}, err
	}
	item = mcpapi.ItemAtResolvedRoute(item, resolved.Repo)
	var stack pullapi.StackContext
	if b.server.providerSource != nil {
		stack, err = b.server.providerSource.GetPullStack(ctx, item)
	} else {
		stack, err = pulls.GetStackService(ctx, mcpapi.PullServiceIdentity(item))
	}
	if err != nil {
		return mcpserver.Stack{}, mcpapi.McpBackendError(err)
	}
	return mcpapi.McpStack(stack), nil
}

func (b mcpBackend) ListLaunchTargets(context.Context) ([]mcpserver.LaunchTarget, error) {
	targets := b.server.runtime.LaunchTargets()
	out := make([]mcpserver.LaunchTarget, 0, len(targets))
	for _, target := range targets {
		out = append(out, mcpserver.LaunchTarget{
			Key: target.Key, Label: target.Label, Kind: string(target.Kind),
			Source: target.Source, Available: target.Available,
			DisabledReason: target.DisabledReason,
		})
	}
	return out, nil
}

func (b mcpBackend) ListWorkspaceAgentSessions(
	ctx context.Context, workspaceID string,
) ([]mcpserver.WorkspaceAgentSession, error) {
	sessions, err := b.server.workspaceAPI.ListWorkspaceAgentSessionsService(ctx, workspaceID)
	if err != nil {
		return nil, mcpapi.McpBackendError(err)
	}
	out := make([]mcpserver.WorkspaceAgentSession, 0, len(sessions))
	for _, session := range sessions {
		row := mcpserver.WorkspaceAgentSession{
			Agent: session.Agent, SessionID: session.SessionID,
			RuntimeSessionKey: session.RuntimeSessionKey, TargetKey: session.TargetKey,
			State: string(session.State), UpdatedAt: session.UpdatedAt,
		}
		if session.InitialMessage != nil {
			row.InitialMessage = mcpapi.McpInitialMessage(*session.InitialMessage)
		}
		out = append(out, row)
	}
	return out, nil
}

func (b mcpBackend) GetWorkspace(
	ctx context.Context, workspaceID string,
) (mcpserver.Workspace, error) {
	result, err := b.server.workspaceAPI.GetWorkspaceService(ctx, workspaceID)
	if err != nil {
		return mcpserver.Workspace{}, mcpapi.McpBackendError(err)
	}
	return mcpapi.McpWorkspace(result), nil
}

func (b mcpBackend) CreatePullWorkspace(
	ctx context.Context, item mcpserver.ItemIdentity, suppressAutoAssign bool,
) (mcpserver.Workspace, error) {
	resolved, err := b.resolveWorkspaceRepository(ctx, item.Repository())
	if err != nil {
		return mcpserver.Workspace{}, err
	}
	item = mcpapi.ItemAtResolvedRoute(item, resolved.Repo)
	result, err := b.server.workspaceAPI.CreatePullWorkspace(ctx, workspaceapi.CreatePullWorkspaceRequest{
		Provider: item.Provider, PlatformHost: item.PlatformHost,
		RepoKey: resolved.Repo.Key,
		Owner:   item.Owner, Name: item.Name, Number: item.Number,
		SuppressAutoAssign: suppressAutoAssign,
	})
	if err != nil {
		return mcpserver.Workspace{}, mcpapi.McpBackendMutationError(err)
	}
	return mcpapi.McpWorkspace(result), nil
}

func (b mcpBackend) CreateIssueWorkspace(
	ctx context.Context, item mcpserver.ItemIdentity, suppressAutoAssign bool,
) (mcpserver.Workspace, error) {
	resolved, err := b.resolveWorkspaceRepository(ctx, item.Repository())
	if err != nil {
		return mcpserver.Workspace{}, err
	}
	item = mcpapi.ItemAtResolvedRoute(item, resolved.Repo)
	result, err := b.server.workspaceAPI.CreateIssueWorkspaceService(ctx, workspaceapi.CreateIssueWorkspaceRequest{
		Provider: item.Provider, PlatformHost: item.PlatformHost,
		RepoKey: resolved.Repo.Key,
		Owner:   item.Owner, Name: item.Name, Number: item.Number,
		SuppressAutoAssign: suppressAutoAssign,
	})
	if err != nil {
		return mcpserver.Workspace{}, mcpapi.McpBackendMutationError(err)
	}
	return mcpapi.McpWorkspace(result), nil
}

func (b mcpBackend) CreateAdHocWorkspace(
	ctx context.Context, repo mcpserver.RepositoryIdentity, branch string,
) (mcpserver.Workspace, error) {
	resolved, err := b.resolveWorkspaceRepository(ctx, repo)
	if err != nil {
		return mcpserver.Workspace{}, err
	}
	repo = mcpapi.RepositoryAtResolvedRoute(repo, resolved.Repo)
	var branchPtr *string
	if branch != "" {
		branchPtr = &branch
	}
	result, err := b.server.workspaceAPI.CreateAdHocWorkspaceService(ctx, workspaceapi.CreateAdHocWorkspaceRequest{
		Provider: repo.Provider, PlatformHost: repo.PlatformHost,
		RepoKey: resolved.Repo.Key,
		Owner:   repo.Owner, Name: repo.Name, Branch: branchPtr,
	})
	if err != nil {
		return mcpserver.Workspace{}, mcpapi.McpBackendMutationError(err)
	}
	return mcpapi.McpWorkspace(result), nil
}

func (b mcpBackend) LaunchWorkspaceRuntime(
	ctx context.Context, workspaceID, targetKey, initialMessage string,
) (mcpserver.RuntimeSession, error) {
	session, err := b.server.workspaceAPI.LaunchWorkspaceRuntimeService(ctx, workspaceID, targetKey, initialMessage)
	if err != nil {
		return mcpserver.RuntimeSession{}, mcpapi.McpBackendMutationError(err)
	}
	return mcpserver.RuntimeSession{
		Key: session.Key, TargetKey: session.TargetKey,
		Kind: string(session.Kind), Status: string(session.Status), CreatedAt: session.CreatedAt,
	}, nil
}

func (b mcpBackend) PreferredWorkspaceAgentTarget(
	ctx context.Context, since time.Time, targetKeys []string,
) (string, bool, error) {
	target, found, err := b.server.workspaceAPI.PreferredWorkspaceAgentTargetService(
		ctx, since, targetKeys,
	)
	if err != nil {
		return "", false, mcpapi.McpBackendError(err)
	}
	return target, found, nil
}

func (b mcpBackend) GetWorkspaceRuntime(
	ctx context.Context, workspaceID string,
) (mcpserver.WorkspaceRuntime, error) {
	result, err := b.server.workspaceAPI.GetWorkspaceRuntimeService(ctx, workspaceID, false)
	if err != nil {
		return mcpserver.WorkspaceRuntime{}, mcpapi.McpBackendError(err)
	}
	out := mcpserver.WorkspaceRuntime{Sessions: make([]mcpserver.RuntimeSession, 0, len(result.Sessions))}
	for _, session := range result.Sessions {
		out.Sessions = append(out.Sessions, mcpserver.RuntimeSession{
			Key: session.Key, TargetKey: session.TargetKey,
			Kind: string(session.Kind), Status: string(session.Status), CreatedAt: session.CreatedAt,
		})
	}
	return out, nil
}

func (b mcpBackend) SubmitAgentMessage(
	ctx context.Context, req mcpserver.AgentMessageRequest,
) (mcpserver.AgentMessageResult, error) {
	result, err := b.server.workspaceAPI.SubmitAgentMessageService(
		ctx, req.WorkspaceID, req.RuntimeSessionKey, req.Message,
	)
	if errors.Is(err, workspaceapi.ErrInitialMessageInputModeNotReady) {
		return mcpserver.AgentMessageResult{}, &mcpserver.Error{
			Kind: "unavailable", Code: mcpserver.ErrorCodeInitialMessageInputModeNotReady,
			Message: err.Error(), Retryable: true,
		}
	}
	if err != nil {
		return mcpserver.AgentMessageResult{}, mcpapi.McpBackendError(err)
	}
	return mcpserver.AgentMessageResult{
		TargetKey: result.TargetKey, MessageBytes: result.MessageBytes,
		SubmittedAt: result.SubmittedAt,
	}, nil
}

func (b mcpBackend) SubmitInitialMessage(
	ctx context.Context, req mcpserver.InitialMessageRequest,
) (mcpserver.InitialMessageStatus, error) {
	result, err := b.server.workspaceAPI.SubmitInitialMessageService(ctx, workspaceapi.InitialMessageRequest{
		WorkspaceID: req.WorkspaceID, RuntimeSessionKey: req.RuntimeSessionKey,
		TargetKey: req.TargetKey, Message: req.Message, FromAgent: true,
	})
	status := *mcpapi.McpInitialMessage(result)
	if errors.Is(err, workspaceapi.ErrInitialMessageInputModeNotReady) {
		return status, &mcpserver.Error{
			Kind: "unavailable", Code: mcpserver.ErrorCodeInitialMessageInputModeNotReady,
			Message: err.Error(), Retryable: true,
		}
	}
	if err != nil {
		converted := mcpapi.McpBackendError(err)
		if result.State == "pending" || result.State == "uncertain" {
			if backendErr, ok := errors.AsType[*mcpserver.Error](converted); ok {
				annotated := *backendErr
				annotated.Ambiguous = true
				annotated.Retryable = false
				annotated.Details = mcpapi.CloneMCPErrorDetails(backendErr.Details)
				annotated.Details["initial_message_state"] = result.State
				converted = &annotated
			}
		}
		return status, converted
	}
	return status, nil
}

func (b mcpBackend) GetInitialMessage(
	ctx context.Context, workspaceID, runtimeSessionKey string,
) (mcpserver.InitialMessageStatus, error) {
	result, err := b.server.workspaceAPI.GetInitialMessageService(ctx, workspaceID, runtimeSessionKey)
	if err != nil {
		return mcpserver.InitialMessageStatus{}, mcpapi.McpBackendError(err)
	}
	return *mcpapi.McpInitialMessage(result), nil
}

func (b mcpBackend) resolveMCPRepository(
	ctx context.Context, identity mcpserver.RepositoryIdentity,
) (mcpapi.ResolvedMCPRepository, error) {
	repo, err := b.resolveRepository(ctx, identity)
	if err != nil {
		return mcpapi.ResolvedMCPRepository{}, err
	}
	return mcpapi.ResolvedMCPRepository{Repo: repo}, nil
}

func (b mcpBackend) resolveWorkspaceRepository(
	ctx context.Context, identity mcpserver.RepositoryIdentity,
) (mcpapi.ResolvedMCPRepository, error) {
	if b.server.providerSource == nil {
		return b.resolveMCPRepository(ctx, identity)
	}
	if err := mcpapi.ValidateMCPRepositoryIdentity(identity); err != nil {
		return mcpapi.ResolvedMCPRepository{}, err
	}
	descriptor, err := b.server.providerSource.GetRepositoryDescriptor(
		ctx, providerplane.RepositoryRoute{
			Provider: identity.Provider, PlatformHost: identity.PlatformHost,
			Owner: identity.Owner, Name: identity.Name,
		},
	)
	if err != nil {
		return mcpapi.ResolvedMCPRepository{}, mcpapi.McpBackendError(err)
	}
	if descriptor.Key != identity.Key {
		return mcpapi.ResolvedMCPRepository{}, mcpapi.McpRepositoryIdentityChangedError()
	}
	identity.Provider = descriptor.Provider
	identity.PlatformHost = descriptor.PlatformHost
	identity.Key = descriptor.Key
	identity.Owner = descriptor.Owner
	identity.Name = descriptor.Name
	return b.resolveMCPRepository(ctx, identity)
}

func (b mcpBackend) resolveProviderRepository(
	ctx context.Context, identity mcpserver.RepositoryIdentity,
) (mcpapi.ResolvedMCPRepository, error) {
	if b.server.providerSource == nil {
		return b.resolveMCPRepository(ctx, identity)
	}
	if err := mcpapi.ValidateMCPRepositoryIdentity(identity); err != nil {
		return mcpapi.ResolvedMCPRepository{}, err
	}
	repo, err := b.server.providerSource.ResolveRepository(ctx, identity)
	if err != nil {
		return mcpapi.ResolvedMCPRepository{}, mcpapi.McpBackendError(err)
	}
	if !mcpapi.McpRepositoryStableIdentityMatches(*repo, identity) {
		return mcpapi.ResolvedMCPRepository{}, mcpapi.McpRepositoryIdentityChangedError()
	}
	return mcpapi.ResolvedMCPRepository{Repo: repo, Hub: true}, nil
}

func (b mcpBackend) resolveRepository(
	ctx context.Context, identity mcpserver.RepositoryIdentity,
) (*db.Repo, error) {
	if err := mcpapi.ValidateMCPRepositoryIdentity(identity); err != nil {
		return nil, err
	}
	// The provider ID names the repository; owner/name in the request are
	// only its last known route.
	repo, err := b.server.db.GetActiveRepoByProviderID(ctx, platform.RepositoryIdentity{
		Provider: identity.Provider, PlatformHost: identity.PlatformHost,
		Key: identity.Key,
	})
	if err != nil {
		return nil, mcpapi.McpBackendError(httpapi.Internal("look up repository failed"))
	}
	if repo == nil {
		return nil, mcpapi.McpRepositoryIdentityChangedError()
	}
	return repo.Row(), nil
}
