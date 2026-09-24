package server

import (
	"context"
	"errors"
	"strings"
	"time"

	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/mcpserver"
	"go.kenn.io/forge/internal/providerplane"
	"go.kenn.io/forge/internal/server/httpapi"
	"go.kenn.io/forge/internal/server/issueapi"
	"go.kenn.io/forge/internal/server/itemapi"
	"go.kenn.io/forge/internal/server/mcpapi"
	"go.kenn.io/forge/internal/server/providerapi"
	"go.kenn.io/forge/internal/server/pullapi"
	"go.kenn.io/forge/internal/server/workspaceapi"
)

func (s *Server) MCPBackend() mcpserver.Backend {
	backend := mcpBackend{server: s}
	return mcpserver.NewFederatedBackend(backend, backend)
}

type mcpBackend struct {
	server *Server
}

func (b mcpBackend) ListRepositories(ctx context.Context) ([]mcpserver.RepositorySummary, error) {
	var (
		rows []itemapi.RepoSummaryResponse
		err  error
	)
	if b.server.providerSource != nil {
		rows, err = b.server.providerSource.ListRepositorySummaries(ctx)
	} else {
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

func (b mcpBackend) ListActivity(
	ctx context.Context, query mcpserver.ActivityQuery,
) (mcpserver.ActivityPage, error) {
	var resolved mcpapi.ResolvedMCPRepository
	if query.Repository.Provider != "" {
		var err error
		resolved, err = b.resolveProviderRepositoryFence(ctx, query.Repository)
		if err != nil {
			return mcpserver.ActivityPage{}, err
		}
	}
	body, err := b.server.activityapi.ListActivityService(ctx, &itemapi.ListActivityInput{
		Repo: mcpapi.McpRepositoryFilter(query.Repository), Types: query.ActivityTypes,
		ItemTypes: query.ItemTypes, Search: query.Search, After: query.After, Since: query.Since,
	})
	if err != nil {
		return mcpserver.ActivityPage{}, mcpapi.McpBackendError(err)
	}
	if resolved.Repo != nil {
		if err := b.confirmProviderRepositoryRoute(ctx, resolved); err != nil {
			return mcpserver.ActivityPage{}, err
		}
	}
	out := mcpserver.ActivityPage{
		Items: make([]mcpserver.ActivityItem, 0, len(body.Items)), Capped: body.Capped,
	}
	for _, row := range body.Items {
		item := mcpserver.ActivityItem{
			ID: row.ID, Cursor: row.Cursor, ActivityType: row.ActivityType,
			Repository: mcpserver.RepositoryIdentity{
				Provider: row.Repo.Provider, PlatformHost: row.Repo.PlatformHost,
				PlatformRepoID: row.Repo.PlatformRepoID,
				RepoPath:       row.Repo.RepoPath, Owner: row.Repo.Owner, Name: row.Repo.Name,
			},
			ItemType: row.ItemType, ItemNumber: row.ItemNumber,
			ItemTitle: row.ItemTitle, ItemURL: row.ItemURL, ItemState: row.ItemState,
			Author: row.Author, ItemAuthor: row.ItemAuthor, CreatedAt: row.CreatedAt,
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
	var resolved mcpapi.ResolvedMCPRepository
	if query.Repository.Provider != "" {
		var err error
		resolved, err = b.resolveProviderRepositoryFence(ctx, query.Repository)
		if err != nil {
			return nil, err
		}
	}
	rows, err := b.server.pullAPI.ListService(ctx, pullapi.ListQuery{
		Repo: mcpapi.McpRepositoryFilter(query.Repository), State: query.State,
		Text: query.Text, Label: query.Label, Limit: query.Limit, Offset: query.Offset,
	})
	if err != nil {
		return nil, mcpapi.McpBackendError(err)
	}
	if resolved.Repo != nil {
		if err := b.confirmProviderRepositoryRoute(ctx, resolved); err != nil {
			return nil, err
		}
	}
	out := make([]mcpserver.Pull, 0, len(rows))
	for _, row := range rows {
		out = append(out, mcpapi.McpPull(row))
	}
	return out, nil
}

func (b mcpBackend) ListIssues(
	ctx context.Context, query mcpserver.ItemListQuery,
) ([]mcpserver.Issue, error) {
	var resolved mcpapi.ResolvedMCPRepository
	if query.Repository.Provider != "" {
		var err error
		resolved, err = b.resolveProviderRepositoryFence(ctx, query.Repository)
		if err != nil {
			return nil, err
		}
	}
	rows, err := b.server.issueAPI.ListService(ctx, issueapi.ListQuery{
		Repo: mcpapi.McpRepositoryFilter(query.Repository), State: query.State,
		Text: query.Text, Limit: query.Limit, Offset: query.Offset,
	})
	if err != nil {
		return nil, mcpapi.McpBackendError(err)
	}
	if resolved.Repo != nil {
		if err := b.confirmProviderRepositoryRoute(ctx, resolved); err != nil {
			return nil, err
		}
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
	resolved, err := b.resolveProviderRepositoryFence(ctx, mcpapi.ItemRepositoryIdentity(item))
	if err != nil {
		return mcpserver.PullDetail{}, err
	}
	detail, err := b.server.pullAPI.GetService(ctx, mcpapi.PullServiceIdentity(item))
	if err != nil {
		return mcpserver.PullDetail{}, mcpapi.McpBackendError(err)
	}
	if err := b.confirmProviderRepositoryRoute(ctx, resolved); err != nil {
		return mcpserver.PullDetail{}, err
	}
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
			WorkflowStatus: string(detail.MergeRequest.KanbanStatus),
			LastActivityAt: detail.MergeRequest.LastActivityAt,
			Repository:     mcpapi.RepositoryIdentityFromResponse(detail.Repo),
			Workspace:      mcpapi.McpWorkspaceRef(detail.Workspace),
			DetailLoaded:   detail.DetailLoaded, DetailFetchedAt: detail.DetailFetchedAt,
		}
		out.Pull = &pull
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
	return out, nil
}

func (b mcpBackend) GetIssue(
	ctx context.Context, item mcpserver.ItemIdentity,
) (mcpserver.IssueDetail, error) {
	resolved, err := b.resolveProviderRepositoryFence(ctx, mcpapi.ItemRepositoryIdentity(item))
	if err != nil {
		return mcpserver.IssueDetail{}, err
	}
	detail, err := b.server.issueAPI.GetService(ctx, mcpapi.IssueServiceIdentity(item))
	if err != nil {
		return mcpserver.IssueDetail{}, mcpapi.McpBackendError(err)
	}
	if err := b.confirmProviderRepositoryRoute(ctx, resolved); err != nil {
		return mcpserver.IssueDetail{}, err
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
			Body: detail.Issue.Body, WorkflowStatus: string(detail.Issue.WorkflowStatus),
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
	if detail.Workflow != nil {
		workflow := mcpserver.WorkflowState{
			Status: string(detail.Workflow.Status), UpdatedAt: detail.Workflow.UpdatedAt,
			UpdatedSource: detail.Workflow.UpdatedSource,
			UpdatedActor:  detail.Workflow.UpdatedActor,
			UpdatedReason: detail.Workflow.UpdatedReason,
		}
		out.Workflow = &workflow
	}
	return out, nil
}

func (b mcpBackend) GetPullDiff(
	ctx context.Context, item mcpserver.ItemIdentity, includePatches bool,
) (mcpserver.Diff, error) {
	resolved, err := b.resolveRepositoryFence(ctx, mcpapi.ItemRepositoryIdentity(item))
	if err != nil {
		return mcpserver.Diff{}, err
	}
	identity := mcpapi.PullServiceIdentity(item)
	var (
		stale bool
		files []mcpserver.DiffFile
	)
	if includePatches {
		result, err := b.server.pullAPI.GetDiffService(ctx, identity, pullapi.DiffQuery{})
		if err != nil {
			return mcpserver.Diff{}, mcpapi.McpBackendError(err)
		}
		stale = result.Stale
		files = mcpapi.McpDiffFiles(result.Files)
	} else {
		result, err := b.server.pullAPI.GetFilesService(ctx, identity)
		if err != nil {
			return mcpserver.Diff{}, mcpapi.McpBackendError(err)
		}
		stale = result.Stale
		files = mcpapi.McpDiffFiles(result.Files)
	}
	if err := b.confirmRepositoryRoute(ctx, resolved); err != nil {
		return mcpserver.Diff{}, err
	}
	return mcpserver.Diff{Stale: stale, Files: files}, nil
}

func (b mcpBackend) GetPullStack(
	ctx context.Context, item mcpserver.ItemIdentity,
) (mcpserver.Stack, error) {
	resolved, err := b.resolveProviderRepositoryFence(ctx, mcpapi.ItemRepositoryIdentity(item))
	if err != nil {
		return mcpserver.Stack{}, err
	}
	var stack pullapi.StackContext
	if b.server.providerSource != nil {
		stack, err = b.server.providerSource.GetPullStack(ctx, item)
	} else {
		stack, err = b.server.pullAPI.GetStackService(ctx, mcpapi.PullServiceIdentity(item))
	}
	if err != nil {
		return mcpserver.Stack{}, mcpapi.McpBackendError(err)
	}
	if err := b.confirmProviderRepositoryRoute(ctx, resolved); err != nil {
		return mcpserver.Stack{}, err
	}
	return mcpapi.McpStack(stack), nil
}

func (b mcpBackend) ListWorkflowStates(
	ctx context.Context, query mcpserver.WorkflowQuery,
) (mcpserver.WorkflowPage, error) {
	if b.server.providerSource != nil {
		page, err := b.server.providerSource.ListWorkflowStates(ctx, query)
		if err != nil {
			return mcpserver.WorkflowPage{}, mcpapi.McpBackendError(err)
		}
		return page, nil
	}
	return b.listWorkflowStatesLocal(ctx, query)
}

func (b mcpBackend) listWorkflowStatesLocal(
	ctx context.Context, query mcpserver.WorkflowQuery,
) (mcpserver.WorkflowPage, error) {
	var resolved mcpapi.ResolvedMCPRepository
	if query.Repository.Provider != "" {
		var err error
		resolved, err = b.resolveRepositoryFence(ctx, query.Repository)
		if err != nil {
			return mcpserver.WorkflowPage{}, err
		}
	}
	rows, next, err := b.server.db.ListItemWorkflowStates(ctx, db.ListWorkflowStatesOpts{
		RepoFilters: mcpapi.McpRepoFilters(query.Repository), ItemTypes: query.ItemTypes,
		States: query.States, IncludeClosed: query.IncludeClosed,
		ExcludeRemovedUpstream: true, Limit: query.Limit, Cursor: query.Cursor,
	})
	if err != nil {
		if errors.Is(err, db.ErrInvalidWorkflowCursor) {
			return mcpserver.WorkflowPage{}, &mcpserver.Error{
				Kind: "invalid_request", Code: string(httpapi.CodeValidationError),
				Message: "invalid cursor",
			}
		}
		return mcpserver.WorkflowPage{}, mcpapi.McpBackendError(err)
	}
	if resolved.Repo != nil {
		if err := b.confirmRepositoryRoute(ctx, resolved); err != nil {
			return mcpserver.WorkflowPage{}, err
		}
	}
	out := mcpserver.WorkflowPage{
		Items: make([]mcpserver.WorkflowItem, 0, len(rows)), NextCursor: next,
	}
	for _, row := range rows {
		workflow := mcpserver.WorkflowState{Status: string(mcpapi.NormalizeMCPWorkflowStatus(row.Status))}
		if row.HasRow && row.UpdatedAt != nil {
			workflow.UpdatedAt = itemapi.FormatUTCRFC3339(*row.UpdatedAt)
			workflow.UpdatedSource = row.UpdatedSource
			workflow.UpdatedActor = row.UpdatedActor
			workflow.UpdatedReason = row.UpdatedReason
		}
		repository := mcpserver.RepositoryIdentity{
			Provider: row.Platform, PlatformHost: row.PlatformHost,
			PlatformRepoID: row.PlatformRepoID,
			RepoPath:       row.RepoPath, Owner: row.Owner, Name: row.Name,
		}
		out.Items = append(out.Items, mcpserver.WorkflowItem{
			Identity: mcpserver.ItemIdentity{
				Type: row.ItemType, Provider: row.Platform, PlatformHost: row.PlatformHost,
				PlatformRepoID: row.PlatformRepoID,
				Owner:          row.Owner, Name: row.Name, Number: row.Number,
			},
			Repository: repository, Title: row.Title, State: row.State,
			URL: row.URL, Author: row.Author, IsDraft: row.IsDraft,
			LastActivityAt: itemapi.FormatUTCRFC3339(row.LastActivityAt), Workflow: workflow,
		})
	}
	return out, nil
}

func (b mcpBackend) SetWorkflowState(
	ctx context.Context, item mcpserver.ItemIdentity, update mcpserver.WorkflowUpdate,
) (mcpserver.WorkflowMutation, error) {
	if b.server.providerSource != nil {
		mutation, err := b.server.providerSource.SetWorkflowState(ctx, item, update)
		if err != nil {
			return mcpserver.WorkflowMutation{}, mcpapi.McpBackendMutationError(err)
		}
		return mutation, nil
	}
	return b.setWorkflowStateLocal(ctx, item, update)
}

func (b mcpBackend) setWorkflowStateLocal(
	ctx context.Context, item mcpserver.ItemIdentity, update mcpserver.WorkflowUpdate,
) (mcpserver.WorkflowMutation, error) {
	if b.server.providerWriteGate != nil {
		release, err := b.server.providerWriteGate.Admit(ctx)
		if err != nil {
			if errors.Is(err, providerplane.ErrSpokePreparationInProgress) {
				return mcpserver.WorkflowMutation{}, mcpapi.McpBackendError(providerapi.SpokePreparationProblem())
			}
			return mcpserver.WorkflowMutation{}, mcpapi.McpBackendMutationError(err)
		}
		defer release()
	}
	repo, err := b.resolveRepository(ctx, mcpapi.ItemRepositoryIdentity(item))
	if err != nil {
		return mcpserver.WorkflowMutation{}, err
	}
	switch item.Type {
	case db.ItemTypePR:
		visible, readErr := b.server.db.GetVisibleMergeRequestByRepoIDAndNumber(ctx, repo.ID, item.Number)
		if readErr != nil {
			return mcpserver.WorkflowMutation{}, mcpapi.McpBackendError(httpapi.Internal("read pull request failed"))
		}
		if visible == nil {
			return mcpserver.WorkflowMutation{}, mcpapi.McpBackendError(httpapi.NotFound(
				httpapi.CodePullNotFound, "pull request not found", nil,
			))
		}
	case db.ItemTypeIssue:
		visible, readErr := b.server.db.GetVisibleIssueByRepoIDAndNumber(ctx, repo.ID, item.Number)
		if readErr != nil {
			return mcpserver.WorkflowMutation{}, mcpapi.McpBackendError(httpapi.Internal("read issue failed"))
		}
		if visible == nil {
			return mcpserver.WorkflowMutation{}, mcpapi.McpBackendError(httpapi.NotFound(
				httpapi.CodeIssueNotFound, "issue not found", nil,
			))
		}
	default:
		return mcpserver.WorkflowMutation{}, &mcpserver.Error{
			Kind: "invalid_request", Code: string(httpapi.CodeValidationError),
			Message: "item type must be pr or issue",
		}
	}
	result, err := b.server.db.SetItemWorkflowState(ctx, db.SetItemWorkflowStateParams{
		RepoID: repo.ID, ItemType: item.Type, ItemNumber: item.Number,
		Status: update.Status, ExpectedStatus: update.ExpectedStatus,
		Source: update.Source, Actor: update.Actor, Reason: update.Reason,
	})
	if conflict, ok := errors.AsType[*db.WorkflowStateConflictError](err); ok {
		return mcpserver.WorkflowMutation{}, &mcpserver.Error{
			Kind: "conflict", Code: string(httpapi.CodeConflict),
			Message: "workflow state changed", Details: map[string]any{
				"current_status": conflict.Current, "expected_status": conflict.Expected,
			},
		}
	}
	if err != nil {
		return mcpserver.WorkflowMutation{}, mcpapi.McpBackendMutationError(err)
	}
	return mcpserver.WorkflowMutation{
		PreviousStatus: string(mcpapi.NormalizeMCPWorkflowStatus(result.PreviousStatus)),
		State: mcpserver.WorkflowState{
			Status:        string(mcpapi.NormalizeMCPWorkflowStatus(result.State.Status)),
			UpdatedAt:     itemapi.FormatUTCRFC3339(result.State.UpdatedAt),
			UpdatedSource: result.State.UpdatedSource,
			UpdatedActor:  result.State.UpdatedActor,
			UpdatedReason: result.State.UpdatedReason,
		},
	}, nil
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
	resolved, err := b.resolveWorkspaceRepositoryFence(ctx, mcpapi.ItemRepositoryIdentity(item))
	if err != nil {
		return mcpserver.Workspace{}, err
	}
	ctx = b.routeFenceContext(ctx, resolved)
	result, err := b.server.workspaceAPI.CreatePullWorkspace(ctx, workspaceapi.CreatePullWorkspaceRequest{
		Provider: item.Provider, PlatformHost: item.PlatformHost,
		Owner: item.Owner, Name: item.Name, Number: item.Number,
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
	resolved, err := b.resolveWorkspaceRepositoryFence(ctx, mcpapi.ItemRepositoryIdentity(item))
	if err != nil {
		return mcpserver.Workspace{}, err
	}
	ctx = b.routeFenceContext(ctx, resolved)
	result, err := b.server.workspaceAPI.CreateIssueWorkspaceService(ctx, workspaceapi.CreateIssueWorkspaceRequest{
		Provider: item.Provider, PlatformHost: item.PlatformHost,
		Owner: item.Owner, Name: item.Name, Number: item.Number,
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
	resolved, err := b.resolveWorkspaceRepositoryFence(ctx, repo)
	if err != nil {
		return mcpserver.Workspace{}, err
	}
	ctx = b.routeFenceContext(ctx, resolved)
	var branchPtr *string
	if branch != "" {
		branchPtr = &branch
	}
	result, err := b.server.workspaceAPI.CreateAdHocWorkspaceService(ctx, workspaceapi.CreateAdHocWorkspaceRequest{
		Provider: repo.Provider, PlatformHost: repo.PlatformHost,
		Owner: repo.Owner, Name: repo.Name, Branch: branchPtr,
	})
	if err != nil {
		return mcpserver.Workspace{}, mcpapi.McpBackendMutationError(err)
	}
	return mcpapi.McpWorkspace(result), nil
}

func (b mcpBackend) LaunchWorkspaceRuntime(
	ctx context.Context, workspaceID, targetKey string,
) (mcpserver.RuntimeSession, error) {
	session, err := b.server.workspaceAPI.LaunchWorkspaceRuntimeService(ctx, workspaceID, targetKey)
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
	result, err := b.server.workspaceAPI.GetWorkspaceRuntimeService(ctx, workspaceID)
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
		TargetKey: req.TargetKey, Message: req.Message,
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

func (b mcpBackend) resolveRepositoryFence(
	ctx context.Context, identity mcpserver.RepositoryIdentity,
) (mcpapi.ResolvedMCPRepository, error) {
	repo, err := b.resolveRepository(ctx, identity)
	if err != nil {
		return mcpapi.ResolvedMCPRepository{}, err
	}
	fence, found, err := b.server.repoResolver.CaptureRepositoryRouteFence(ctx, *repo)
	if err != nil {
		return mcpapi.ResolvedMCPRepository{}, mcpapi.McpBackendError(err)
	}
	if !found {
		return mcpapi.ResolvedMCPRepository{}, mcpapi.McpRepositoryIdentityChangedError()
	}
	return mcpapi.ResolvedMCPRepository{Repo: repo, Fence: fence}, nil
}

func (b mcpBackend) resolveWorkspaceRepositoryFence(
	ctx context.Context, identity mcpserver.RepositoryIdentity,
) (mcpapi.ResolvedMCPRepository, error) {
	if b.server.providerSource == nil {
		return b.resolveRepositoryFence(ctx, identity)
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
	if descriptor.PlatformRepoID != strings.TrimSpace(identity.PlatformRepoID) {
		return mcpapi.ResolvedMCPRepository{}, mcpapi.McpRepositoryIdentityChangedError()
	}
	identity.Provider = descriptor.Provider
	identity.PlatformHost = descriptor.PlatformHost
	identity.PlatformRepoID = descriptor.PlatformRepoID
	identity.Owner = descriptor.Owner
	identity.Name = descriptor.Name
	return b.resolveRepositoryFence(ctx, identity)
}

func (b mcpBackend) resolveProviderRepositoryFence(
	ctx context.Context, identity mcpserver.RepositoryIdentity,
) (mcpapi.ResolvedMCPRepository, error) {
	if b.server.providerSource == nil {
		return b.resolveRepositoryFence(ctx, identity)
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

// confirmRepositoryRoute fails a route-addressed read closed when repository
// reconciliation reassigned the validated route while the read was running.
// The fence generation changes on every ownership change, including
// A -> B -> A reuse, so equal captures before and after the read prove the
// read observed only the validated repository.
func (b mcpBackend) confirmRepositoryRoute(
	ctx context.Context, resolved mcpapi.ResolvedMCPRepository,
) error {
	matches, err := b.server.repoResolver.RepositoryRouteFenceMatches(
		ctx, *resolved.Repo, resolved.Fence,
	)
	if err != nil {
		return mcpapi.McpBackendError(err)
	}
	if !matches {
		return mcpapi.McpRepositoryIdentityChangedError()
	}
	return nil
}

func (b mcpBackend) confirmProviderRepositoryRoute(
	ctx context.Context, resolved mcpapi.ResolvedMCPRepository,
) error {
	if !resolved.Hub {
		return b.confirmRepositoryRoute(ctx, resolved)
	}
	identity := mcpserver.RepositoryIdentity{
		Provider: resolved.Repo.Platform, PlatformHost: resolved.Repo.PlatformHost,
		PlatformRepoID: resolved.Repo.PlatformRepoID,
		Owner:          resolved.Repo.Owner, Name: resolved.Repo.Name,
	}
	repo, err := b.server.providerSource.ResolveRepository(ctx, identity)
	if err != nil {
		return mcpapi.McpBackendError(err)
	}
	if !mcpapi.McpRepositoryStableIdentityMatches(*repo, identity) {
		return mcpapi.McpRepositoryIdentityChangedError()
	}
	return nil
}

// routeFenceContext binds the validated route generation to the request so
// every downstream database write re-validates it under the reconciliation
// read lock, failing workspace mutations closed instead of persisting rows
// for a replacement repository that took over the route mid-request.
func (b mcpBackend) routeFenceContext(
	ctx context.Context, resolved mcpapi.ResolvedMCPRepository,
) context.Context {
	repo := resolved.Repo
	return b.server.db.WithRepositoryRouteFence(ctx, db.RepoIdentity{
		Platform: repo.Platform, PlatformHost: repo.PlatformHost,
		PlatformRepoID: repo.PlatformRepoID,
		Owner:          repo.Owner, Name: repo.Name, RepoPath: repo.RepoPath,
	}, resolved.Fence)
}

func (b mcpBackend) resolveRepository(
	ctx context.Context, identity mcpserver.RepositoryIdentity,
) (*db.Repo, error) {
	if err := mcpapi.ValidateMCPRepositoryIdentity(identity); err != nil {
		return nil, err
	}
	repo, err := b.server.repoResolver.LookupRoute(
		ctx, identity.Provider, identity.PlatformHost, identity.Owner, identity.Name,
	)
	if err != nil {
		return nil, mcpapi.McpBackendError(httpapi.ProviderRouteLookupError(err))
	}
	if repo.PlatformRepoID != strings.TrimSpace(identity.PlatformRepoID) {
		return nil, &mcpserver.Error{
			Kind: "not_found", Code: string(httpapi.CodeRepoNotFound),
			Message: "repository identity no longer matches this route",
		}
	}
	return repo, nil
}
