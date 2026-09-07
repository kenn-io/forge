package server

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"go.kenn.io/forge/internal/mcpserver"
	"go.kenn.io/forge/internal/server/httpapi"
	"go.kenn.io/forge/internal/server/workspaceapi"
)

type federationListWorkflowStatesInput struct {
	Body federationWorkflowQuery
}

type federationListWorkflowStatesOutput = httpapi.BodyOutput[federationWorkflowPage]

type federationWorkflowRepositoryIdentity mcpserver.RepositoryIdentity

type federationWorkflowItemIdentity mcpserver.ItemIdentity

type federationWorkflowState mcpserver.WorkflowState

type federationWorkflowUpdate mcpserver.WorkflowUpdate

type federationWorkflowQuery struct {
	Repository    federationWorkflowRepositoryIdentity `json:"repository"`
	ItemTypes     []string                             `json:"item_types" nullable:"false"`
	States        []string                             `json:"states" nullable:"false"`
	IncludeClosed bool                                 `json:"include_closed"`
	Limit         int                                  `json:"limit"`
	Cursor        string                               `json:"cursor"`
}

type federationWorkflowPage struct {
	Items      []federationWorkflowItem `json:"items" nullable:"false"`
	NextCursor string                   `json:"next_cursor"`
}

type federationWorkflowItem struct {
	Identity       federationWorkflowItemIdentity       `json:"identity"`
	Repository     federationWorkflowRepositoryIdentity `json:"repository"`
	Title          string                               `json:"title"`
	State          string                               `json:"state"`
	URL            string                               `json:"url"`
	Author         string                               `json:"author"`
	IsDraft        bool                                 `json:"is_draft"`
	LastActivityAt string                               `json:"last_activity_at"`
	Workflow       federationWorkflowState              `json:"workflow"`
}

type federationSetWorkflowStateRequest struct {
	Item   federationWorkflowItemIdentity `json:"item"`
	Update federationWorkflowUpdate       `json:"update"`
}

type federationSetWorkflowStateInput struct {
	Body federationSetWorkflowStateRequest
}

type federationWorkflowMutation struct {
	PreviousStatus string                  `json:"previous_status"`
	State          federationWorkflowState `json:"state"`
}

type federationSetWorkflowStateOutput = httpapi.BodyOutput[federationWorkflowMutation]

func federationWorkflowQueryFromMCP(query mcpserver.WorkflowQuery) federationWorkflowQuery {
	return federationWorkflowQuery{
		Repository:    federationWorkflowRepositoryIdentity(query.Repository),
		ItemTypes:     append([]string{}, query.ItemTypes...),
		States:        append([]string{}, query.States...),
		IncludeClosed: query.IncludeClosed,
		Limit:         query.Limit,
		Cursor:        query.Cursor,
	}
}

func (query federationWorkflowQuery) mcp() mcpserver.WorkflowQuery {
	return mcpserver.WorkflowQuery{
		Repository:    mcpserver.RepositoryIdentity(query.Repository),
		ItemTypes:     query.ItemTypes,
		States:        query.States,
		IncludeClosed: query.IncludeClosed,
		Limit:         query.Limit,
		Cursor:        query.Cursor,
	}
}

func federationWorkflowPageFromMCP(page mcpserver.WorkflowPage) federationWorkflowPage {
	items := make([]federationWorkflowItem, 0, len(page.Items))
	for _, item := range page.Items {
		items = append(items, federationWorkflowItem{
			Identity:       federationWorkflowItemIdentity(item.Identity),
			Repository:     federationWorkflowRepositoryIdentity(item.Repository),
			Title:          item.Title,
			State:          item.State,
			URL:            item.URL,
			Author:         item.Author,
			IsDraft:        item.IsDraft,
			LastActivityAt: item.LastActivityAt,
			Workflow:       federationWorkflowState(item.Workflow),
		})
	}
	return federationWorkflowPage{Items: items, NextCursor: page.NextCursor}
}

func (page federationWorkflowPage) mcp() mcpserver.WorkflowPage {
	items := make([]mcpserver.WorkflowItem, 0, len(page.Items))
	for _, item := range page.Items {
		items = append(items, mcpserver.WorkflowItem{
			Identity:       mcpserver.ItemIdentity(item.Identity),
			Repository:     mcpserver.RepositoryIdentity(item.Repository),
			Title:          item.Title,
			State:          item.State,
			URL:            item.URL,
			Author:         item.Author,
			IsDraft:        item.IsDraft,
			LastActivityAt: item.LastActivityAt,
			Workflow:       mcpserver.WorkflowState(item.Workflow),
		})
	}
	return mcpserver.WorkflowPage{Items: items, NextCursor: page.NextCursor}
}

func federationWorkflowMutationFromMCP(
	mutation mcpserver.WorkflowMutation,
) federationWorkflowMutation {
	return federationWorkflowMutation{
		PreviousStatus: mutation.PreviousStatus,
		State:          federationWorkflowState(mutation.State),
	}
}

func (mutation federationWorkflowMutation) mcp() mcpserver.WorkflowMutation {
	return mcpserver.WorkflowMutation{
		PreviousStatus: mutation.PreviousStatus,
		State:          mcpserver.WorkflowState(mutation.State),
	}
}

func (s *Server) registerProviderFederationAPI(api huma.API) {
	s.registerProviderDescriptorAPI(api)
	s.registerProviderStateHandoffAPI(api)
	s.registerFederationProviderSettingsAPI(api)
	s.registerFederationProviderWorkspaceAPI(api)
	s.registerProviderActivitySubjectAPI(api)
	huma.Register(api, huma.Operation{
		OperationID: "federation-list-workflow-states",
		Method:      http.MethodPost,
		Path:        "/federation/provider/workflow-states/query",
		Summary:     "List hub workflow states for a Forge spoke",
		Tags:        []string{"Fleet"},
	}, s.federationListWorkflowStates)
	huma.Register(api, huma.Operation{
		OperationID: "federation-set-workflow-state",
		Method:      http.MethodPut,
		Path:        "/federation/provider/workflow-state",
		Summary:     "Set hub workflow state for a Forge spoke",
		Tags:        []string{"Fleet"},
	}, s.federationSetWorkflowState)
}

func (s *Server) federationListWorkflowStates(
	ctx context.Context,
	input *federationListWorkflowStatesInput,
) (*federationListWorkflowStatesOutput, error) {
	page, err := (mcpBackend{server: s}).listWorkflowStatesLocal(ctx, input.Body.mcp())
	if err != nil {
		return nil, federationWorkflowProblem(err)
	}
	return &federationListWorkflowStatesOutput{Body: federationWorkflowPageFromMCP(page)}, nil
}

func (s *Server) federationSetWorkflowState(
	ctx context.Context,
	input *federationSetWorkflowStateInput,
) (*federationSetWorkflowStateOutput, error) {
	mutation, err := (mcpBackend{server: s}).setWorkflowStateLocal(
		ctx,
		mcpserver.ItemIdentity(input.Body.Item),
		mcpserver.WorkflowUpdate(input.Body.Update),
	)
	if err != nil {
		return nil, federationWorkflowProblem(err)
	}
	return &federationSetWorkflowStateOutput{Body: federationWorkflowMutationFromMCP(mutation)}, nil
}

func federationWorkflowProblem(err error) error {
	backendErr, ok := err.(*mcpserver.Error)
	if !ok {
		return httpapi.Internal(err.Error())
	}
	status := http.StatusInternalServerError
	switch backendErr.Kind {
	case "invalid_request":
		status = http.StatusBadRequest
	case "unauthorized":
		status = http.StatusUnauthorized
	case "forbidden":
		status = http.StatusForbidden
	case "not_found":
		status = http.StatusNotFound
	case "conflict":
		status = http.StatusConflict
	case "rate_limited":
		status = http.StatusTooManyRequests
	case "unavailable":
		status = http.StatusServiceUnavailable
	}
	code := httpapi.ProblemCode(backendErr.Code)
	if code == "" {
		code = httpapi.CodeInternalError
	}
	return httpapi.NewProblem(status, code, backendErr.Message, backendErr.Details)
}

func repoNumberFromHost(input *repoNumberHostInput) repoNumberInput {
	return repoNumberInput{
		Provider:     input.Provider,
		PlatformHost: input.PlatformHost,
		Owner:        input.Owner,
		Name:         input.Name,
		Number:       input.Number,
	}
}

type repoNumberHostInput struct {
	Provider     string `path:"provider"`
	PlatformHost string `path:"platform_host"`
	Owner        string `path:"owner"`
	Name         string `path:"name"`
	Number       int    `path:"number"`
}

type resolveItemHostInput struct {
	Provider     string `path:"provider"`
	PlatformHost string `path:"platform_host"`
	Owner        string `path:"owner"`
	Name         string `path:"name"`
	Number       int    `path:"number"`
	ItemType     string `query:"item_type" enum:"pr,issue" doc:"Optional item type hint for providers whose issues and merge requests have separate number spaces."`
}

type getRepoHostInput struct {
	Provider     string `path:"provider"`
	PlatformHost string `path:"platform_host"`
	Owner        string `path:"owner"`
	Name         string `path:"name"`
}

type commentAutocompleteHostInput struct {
	Provider     string `path:"provider"`
	PlatformHost string `path:"platform_host"`
	Owner        string `path:"owner"`
	Name         string `path:"name"`
	Trigger      string `query:"trigger"`
	Q            string `query:"q"`
	Limit        int    `query:"limit"`
}

type createIssueWorkspaceHostInput struct {
	Provider     string `path:"provider"`
	PlatformHost string `path:"platform_host"`
	Owner        string `path:"owner"`
	Name         string `path:"name"`
	Number       int    `path:"number"`
	Body         struct {
		GitHeadRef             *string `json:"git_head_ref,omitempty"`
		ReuseExistingBranch    bool    `json:"reuse_existing_branch,omitempty"`
		ReuseExistingDirectory bool    `json:"reuse_existing_directory,omitempty"`
		SuppressAutoAssign     bool    `json:"suppress_auto_assign,omitempty"`
	}
}

type createAdHocWorkspaceHostInput struct {
	Provider     string `path:"provider"`
	PlatformHost string `path:"platform_host"`
	Owner        string `path:"owner"`
	Name         string `path:"name"`
	Body         struct {
		Branch              *string `json:"branch,omitempty" doc:"Branch for the new worktree; generated when empty"`
		ReuseExistingBranch bool    `json:"reuse_existing_branch,omitempty"`
	}
}

func (s *Server) resolveItemOnHost(ctx context.Context, input *resolveItemHostInput) (*resolveItemOutput, error) {
	next := resolveItemInput{
		Provider:     input.Provider,
		PlatformHost: input.PlatformHost,
		Owner:        input.Owner,
		Name:         input.Name,
		Number:       input.Number,
		ItemType:     input.ItemType,
	}
	return s.resolveItem(ctx, &next)
}

func (s *Server) getRepoOnHost(ctx context.Context, input *getRepoHostInput) (*getRepoOutput, error) {
	next := getRepoInput{
		Provider:     input.Provider,
		PlatformHost: input.PlatformHost,
		Owner:        input.Owner,
		Name:         input.Name,
	}
	return s.getRepo(ctx, &next)
}

func (s *Server) listRepoLabelsOnHost(ctx context.Context, input *getRepoHostInput) (*listRepoLabelsOutput, error) {
	next := getRepoInput{
		Provider:     input.Provider,
		PlatformHost: input.PlatformHost,
		Owner:        input.Owner,
		Name:         input.Name,
	}
	return s.listRepoLabels(ctx, &next)
}

func (s *Server) getCommentAutocompleteOnHost(ctx context.Context, input *commentAutocompleteHostInput) (*commentAutocompleteOutput, error) {
	next := commentAutocompleteInput{
		Provider:     input.Provider,
		PlatformHost: input.PlatformHost,
		Owner:        input.Owner,
		Name:         input.Name,
		Trigger:      input.Trigger,
		Q:            input.Q,
		Limit:        input.Limit,
	}
	return s.getCommentAutocomplete(ctx, &next)
}

func (s *Server) syncPROnHost(ctx context.Context, input *repoNumberHostInput) (*syncPROutput, error) {
	next := repoNumberFromHost(input)
	return s.syncPR(ctx, &next)
}

func (s *Server) syncPRCIOnHost(ctx context.Context, input *repoNumberHostInput) (*syncPRCIOutput, error) {
	next := repoNumberFromHost(input)
	return s.syncPRCI(ctx, &next)
}

func (s *Server) enqueuePRSyncOnHost(ctx context.Context, input *repoNumberHostInput) (*acceptedOutput, error) {
	next := repoNumberFromHost(input)
	return s.enqueuePRSync(ctx, &next)
}

func (s *Server) syncIssueOnHost(ctx context.Context, input *repoNumberHostInput) (*syncIssueOutput, error) {
	next := issueRepoNumberInput(repoNumberFromHost(input))
	return s.syncIssue(ctx, &next)
}

func (s *Server) enqueueIssueSyncOnHost(ctx context.Context, input *repoNumberHostInput) (*acceptedOutput, error) {
	next := issueRepoNumberInput(repoNumberFromHost(input))
	return s.enqueueIssueSync(ctx, &next)
}

func (s *Server) createIssueWorkspaceOnHost(ctx context.Context, input *createIssueWorkspaceHostInput) (*workspaceapi.CreateWorkspaceOutput, error) {
	next := workspaceapi.CreateIssueWorkspaceInput{
		Provider:     input.Provider,
		PlatformHost: input.PlatformHost,
		Owner:        input.Owner,
		Name:         input.Name,
		Number:       input.Number,
		Body:         input.Body,
	}
	return s.workspaceAPI.CreateIssueWorkspace(ctx, &next)
}

func (s *Server) createAdHocWorkspaceOnHost(ctx context.Context, input *createAdHocWorkspaceHostInput) (*workspaceapi.CreateWorkspaceOutput, error) {
	next := workspaceapi.CreateAdHocWorkspaceInput{
		Provider:     input.Provider,
		PlatformHost: input.PlatformHost,
		Owner:        input.Owner,
		Name:         input.Name,
		Body:         input.Body,
	}
	return s.workspaceAPI.CreateAdHocWorkspace(ctx, &next)
}
