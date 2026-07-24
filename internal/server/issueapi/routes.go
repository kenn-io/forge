package issueapi

import (
	"context"
	"net/http"
	"strings"

	"go.kenn.io/middleman/internal/db"
	"go.kenn.io/middleman/internal/platform"
	"go.kenn.io/middleman/internal/server/httpapi"
	"go.kenn.io/middleman/internal/server/workspaceapi"
)

func (s *Handler) listIssues(ctx context.Context, input *listIssuesInput) (*listIssuesOutput, error) {
	if input.State != "" && input.State != "open" && input.State != "closed" && input.State != "all" {
		return nil, httpapi.Validation("query.state", "state must be one of: open, closed, all", "open", "closed", "all")
	}
	if hasInvalidRepoFilter(input.Repo) {
		return nil, httpapi.Validation("query.repo", "repo filter must be provider|platform_host/repo_path")
	}
	issues, err := s.db.ListIssues(ctx, db.ListIssuesOpts{
		State: input.State, Search: input.Q, Starred: input.Starred,
		Assignee: input.Assignee, Limit: input.Limit, Offset: input.Offset,
		RepoFilters: parseRepoFilters(input.Repo),
	})
	if err != nil {
		return nil, httpapi.Internal("list issues failed")
	}
	repos, err := s.lookupRepoMap(ctx)
	if err != nil {
		return nil, httpapi.Internal("repo lookup failed")
	}
	workspaces, err := s.buildWorkspaceRefLookup(ctx)
	if err != nil {
		return nil, httpapi.Internal("load workspace refs failed")
	}
	out := make([]IssueResponse, 0, len(issues))
	for _, issue := range issues {
		repo, ok := repos[issue.RepoID]
		if !ok {
			continue
		}
		response := IssueResponse{
			Issue: issueResponseModel(issue), Repo: s.resolver.Ref(repo),
			PlatformHost: repo.PlatformHost, RepoOwner: repo.Owner, RepoName: repo.Name,
			Workspace:    workspaceRefForIssue(workspaces, repo, issue.Number),
			DetailLoaded: issue.DetailFetchedAt != nil,
		}
		if issue.DetailFetchedAt != nil {
			response.DetailFetchedAt = formatUTCRFC3339(*issue.DetailFetchedAt)
		}
		out = append(out, response)
	}
	return &listIssuesOutput{Body: out}, nil
}

func (s *Handler) createIssue(ctx context.Context, input *createIssueInput) (*createIssueOutput, error) {
	title := strings.TrimSpace(input.Body.Title)
	if title == "" {
		return nil, httpapi.Validation("body.title", "issue title must not be empty")
	}
	repo, err := s.resolver.RequireRouteCapability(
		ctx, input.Provider, input.PlatformHost, input.Owner, input.Name, capabilityIssueMutation,
	)
	if err != nil {
		return nil, err
	}
	if err := s.requireSyncerCapability(*repo, capabilityIssueMutation); err != nil {
		return nil, err
	}
	mutator, err := s.syncer.IssueMutator(httpapi.ProviderKind(*repo), httpapi.ProviderHost(*repo))
	if err != nil {
		return nil, httpapi.UnsupportedCapability(*repo, capabilityIssueMutation)
	}
	providerIssue, err := mutator.CreateIssue(ctx, httpapi.PlatformRepoRef(*repo), title, input.Body.Body)
	if err != nil {
		return nil, httpapi.ProviderCallProblem(err, string(httpapi.ProviderKind(*repo)), httpapi.ProviderHost(*repo))
	}
	issue := platform.DBIssue(repo.ID, providerIssue)
	issueID, err := s.db.UpsertIssue(ctx, issue)
	if err != nil {
		return nil, httpapi.Internal("save issue failed")
	}
	if err := s.db.ReplaceIssueLabels(ctx, repo.ID, issueID, issue.Labels); err != nil {
		return nil, httpapi.Internal("save issue labels failed")
	}
	saved, err := s.db.GetIssueByRepoIDAndNumber(ctx, repo.ID, issue.Number)
	if err != nil || saved == nil {
		return nil, httpapi.Internal("re-read issue failed")
	}
	saved.ID = issueID
	response := IssueResponse{
		Issue: issueResponseModel(*saved), Repo: s.resolver.Ref(*repo),
		PlatformHost: repo.PlatformHost, RepoOwner: repo.Owner, RepoName: repo.Name,
		DetailLoaded: saved.DetailFetchedAt != nil,
	}
	if saved.DetailFetchedAt != nil {
		response.DetailFetchedAt = formatUTCRFC3339(*saved.DetailFetchedAt)
	}
	return &createIssueOutput{Status: http.StatusCreated, Body: response}, nil
}

func (s *Handler) getIssue(ctx context.Context, input *issueRepoNumberInput) (*getIssueOutput, error) {
	repo, err := s.resolver.LookupRoute(ctx, input.Provider, input.PlatformHost, input.Owner, input.Name)
	if err != nil {
		return nil, httpapi.ProviderRouteLookupError(err)
	}
	issue, err := s.db.GetIssueByRepoIDAndNumber(ctx, repo.ID, input.Number)
	if err != nil {
		return nil, httpapi.Internal("get issue failed")
	}
	if issue == nil {
		return nil, httpapi.NotFound(httpapi.CodeIssueNotFound, "issue not found", nil)
	}
	response, err := s.BuildDetail(ctx, repo, issue)
	if err != nil {
		return nil, err
	}
	return &getIssueOutput{Body: response}, nil
}

func (s *Handler) BuildDetail(ctx context.Context, repo *db.Repo, issue *db.Issue) (IssueDetailResponse, error) {
	events, err := s.db.ListIssueEvents(ctx, issue.ID)
	if err != nil {
		return IssueDetailResponse{}, httpapi.Internal("list issue events failed")
	}
	if events == nil {
		events = []db.IssueEvent{}
	}
	workflow, err := s.issueWorkflowMetaResponse(ctx, repo.ID, issue.Number)
	if err != nil {
		return IssueDetailResponse{}, err
	}
	model := issueResponseModel(*issue)
	ref := s.resolver.Ref(*repo)
	operations := s.operations(*repo)
	ref.Operations = &operations
	response := IssueDetailResponse{
		Issue: &model, Events: events, Repo: ref,
		PlatformHost: repo.PlatformHost, RepoOwner: repo.Owner, RepoName: repo.Name,
		DetailLoaded: issue.DetailFetchedAt != nil, Workflow: workflow,
	}
	if issue.DetailFetchedAt != nil {
		response.DetailFetchedAt = formatUTCRFC3339(*issue.DetailFetchedAt)
	}
	if s.workspaces != nil {
		workspace, workspaceErr := s.workspaces.GetByIssueForProvider(
			ctx, repo.Platform, repo.PlatformHost, repo.Owner, repo.Name, issue.Number,
		)
		if workspaceErr == nil && workspace != nil {
			response.Workspace = &workspaceapi.WorkspaceRef{ID: workspace.ID, Status: workspace.Status}
		}
	}
	return response, nil
}

func (s *Handler) issueWorkflowMetaResponse(ctx context.Context, repoID int64, number int) (*WorkflowStateMetaResponse, error) {
	row, err := s.db.GetItemWorkflowState(ctx, repoID, db.ItemTypeIssue, number)
	if err != nil {
		return nil, httpapi.Internal("read issue workflow state failed")
	}
	if row == nil {
		return &WorkflowStateMetaResponse{Status: db.KanbanStatusNew}, nil
	}
	return &WorkflowStateMetaResponse{
		Status:    normalizeWorkflowStatus(row.Status, "repo_id", repoID, "item_type", db.ItemTypeIssue, "item_number", number),
		UpdatedAt: formatUTCRFC3339(row.UpdatedAt), UpdatedSource: row.UpdatedSource,
		UpdatedActor: row.UpdatedActor, UpdatedReason: row.UpdatedReason,
	}, nil
}

func (s *Handler) editIssueContent(ctx context.Context, input *editIssueContentInput) (*editIssueContentOutput, error) {
	if input.Body.Title == nil && input.Body.Body == nil {
		return nil, httpapi.Validation("body", "at least one of title or body must be provided")
	}
	if input.Body.Title != nil && strings.TrimSpace(*input.Body.Title) == "" {
		return nil, httpapi.Validation("body.title", "title must not be blank")
	}
	repo, err := s.resolver.RequireRouteCapability(
		ctx, input.Provider, input.PlatformHost, input.Owner, input.Name, capabilityStateMutation,
	)
	if err != nil {
		return nil, err
	}
	if err := s.requireSyncerCapability(*repo, capabilityStateMutation); err != nil {
		return nil, err
	}
	mutator, err := s.syncer.IssueContentMutator(httpapi.ProviderKind(*repo), httpapi.ProviderHost(*repo))
	if err != nil {
		return nil, httpapi.UnsupportedCapability(*repo, capabilityStateMutation)
	}
	issue, err := s.db.GetIssueByRepoIDAndNumber(ctx, repo.ID, input.Number)
	if err != nil {
		return nil, httpapi.Internal("get issue failed")
	}
	if issue == nil {
		return nil, httpapi.NotFound(httpapi.CodeIssueNotFound, "issue not found", nil)
	}
	updated, err := mutator.EditIssueContent(ctx, httpapi.PlatformRepoRef(*repo), input.Number, input.Body.Title, input.Body.Body)
	if err != nil {
		return nil, httpapi.ProviderCallProblemWithDetail(err, string(httpapi.ProviderKind(*repo)), httpapi.ProviderHost(*repo), "provider API error: "+err.Error())
	}
	newTitle := issue.Title
	if updated.Title != "" {
		newTitle = updated.Title
	} else if input.Body.Title != nil {
		newTitle = *input.Body.Title
	}
	newBody := issue.Body
	if updated.Body != "" {
		newBody = updated.Body
	} else if input.Body.Body != nil {
		newBody = *input.Body.Body
	}
	updatedAt := s.now().UTC()
	if !updated.UpdatedAt.IsZero() {
		updatedAt = updated.UpdatedAt.UTC()
	}
	if err := s.db.UpdateIssueTitleBody(ctx, issue.ID, newTitle, newBody, updatedAt); err != nil {
		return nil, httpapi.Internal("update title/body failed")
	}
	issue, err = s.db.GetIssueByRepoIDAndNumber(ctx, repo.ID, input.Number)
	if err != nil || issue == nil {
		return nil, httpapi.Internal("re-read issue failed")
	}
	response, err := s.BuildDetail(ctx, repo, issue)
	if err != nil {
		return nil, err
	}
	return &editIssueContentOutput{Body: response}, nil
}
