package providerapi

import (
	"context"
	"errors"
	"net/http"

	"go.kenn.io/forge/internal/mcpserver"
	"go.kenn.io/forge/internal/server/httpapi"
	"go.kenn.io/forge/internal/server/itemapi"
	"go.kenn.io/forge/internal/server/spokeapi"
	"go.kenn.io/forge/internal/server/workspaceapi"
)

type FederationListWorkflowStatesInput struct {
	Body federationWorkflowQuery
}

type FederationListWorkflowStatesOutput = httpapi.BodyOutput[spokeapi.FederationWorkflowPage]

type federationWorkflowUpdate mcpserver.WorkflowUpdate

type federationWorkflowQuery struct {
	Repository    spokeapi.FederationWorkflowRepositoryIdentity `json:"repository"`
	ItemTypes     []string                                      `json:"item_types" nullable:"false"`
	States        []string                                      `json:"states" nullable:"false"`
	IncludeClosed bool                                          `json:"include_closed"`
	Limit         int                                           `json:"limit"`
	Cursor        string                                        `json:"cursor"`
}

type federationSetWorkflowStateRequest struct {
	Item   spokeapi.FederationWorkflowItemIdentity `json:"item"`
	Update federationWorkflowUpdate                `json:"update"`
}

type FederationSetWorkflowStateInput struct {
	Body federationSetWorkflowStateRequest
}

type FederationSetWorkflowStateOutput = httpapi.BodyOutput[spokeapi.FederationWorkflowMutation]

func (query federationWorkflowQuery) Mcp() mcpserver.WorkflowQuery {
	return mcpserver.WorkflowQuery{
		Repository:    mcpserver.RepositoryIdentity(query.Repository),
		ItemTypes:     query.ItemTypes,
		States:        query.States,
		IncludeClosed: query.IncludeClosed,
		Limit:         query.Limit,
		Cursor:        query.Cursor,
	}
}

func FederationWorkflowPageFromMCP(page mcpserver.WorkflowPage) spokeapi.FederationWorkflowPage {
	items := make([]spokeapi.FederationWorkflowItem, 0, len(page.Items))
	for _, item := range page.Items {
		items = append(items, spokeapi.FederationWorkflowItem{
			Identity:       spokeapi.FederationWorkflowItemIdentity(item.Identity),
			Repository:     spokeapi.FederationWorkflowRepositoryIdentity(item.Repository),
			Title:          item.Title,
			State:          item.State,
			URL:            item.URL,
			Author:         item.Author,
			IsDraft:        item.IsDraft,
			LastActivityAt: item.LastActivityAt,
			Workflow:       spokeapi.FederationWorkflowState(item.Workflow),
		})
	}
	return spokeapi.FederationWorkflowPage{Items: items, NextCursor: page.NextCursor}
}

func FederationWorkflowMutationFromMCP(
	mutation mcpserver.WorkflowMutation,
) spokeapi.FederationWorkflowMutation {
	return spokeapi.FederationWorkflowMutation{
		PreviousStatus: mutation.PreviousStatus,
		State:          spokeapi.FederationWorkflowState(mutation.State),
	}
}

func FederationWorkflowProblem(err error) error {
	backendErr, ok := errors.AsType[*mcpserver.Error](err)
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

func repoNumberFromHost(input *repoNumberHostInput) itemapi.RepoNumberInput {
	return itemapi.RepoNumberInput{
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

func (s *Handlers) resolveItemOnHost(ctx context.Context, input *resolveItemHostInput) (*itemapi.ResolveItemOutput, error) {
	next := itemapi.ResolveItemInput{
		Provider:     input.Provider,
		PlatformHost: input.PlatformHost,
		Owner:        input.Owner,
		Name:         input.Name,
		Number:       input.Number,
		ItemType:     input.ItemType,
	}
	return s.ResolveItem(ctx, &next)
}

func (s *Handlers) getRepoOnHost(ctx context.Context, input *getRepoHostInput) (*itemapi.GetRepoOutput, error) {
	next := itemapi.GetRepoInput{
		Provider:     input.Provider,
		PlatformHost: input.PlatformHost,
		Owner:        input.Owner,
		Name:         input.Name,
	}
	return s.GetRepo(ctx, &next)
}

func (s *Handlers) listRepoLabelsOnHost(ctx context.Context, input *getRepoHostInput) (*itemapi.ListRepoLabelsOutput, error) {
	next := itemapi.GetRepoInput{
		Provider:     input.Provider,
		PlatformHost: input.PlatformHost,
		Owner:        input.Owner,
		Name:         input.Name,
	}
	return s.ListRepoLabels(ctx, &next)
}

func (s *Handlers) getCommentAutocompleteOnHost(ctx context.Context, input *commentAutocompleteHostInput) (*itemapi.CommentAutocompleteOutput, error) {
	next := itemapi.CommentAutocompleteInput{
		Provider:     input.Provider,
		PlatformHost: input.PlatformHost,
		Owner:        input.Owner,
		Name:         input.Name,
		Trigger:      input.Trigger,
		Q:            input.Q,
		Limit:        input.Limit,
	}
	return s.GetCommentAutocomplete(ctx, &next)
}

func (s *Handlers) syncPROnHost(ctx context.Context, input *repoNumberHostInput) (*itemapi.SyncPROutput, error) {
	next := repoNumberFromHost(input)
	return s.SyncPR(ctx, &next)
}

func (s *Handlers) syncPRCIOnHost(ctx context.Context, input *repoNumberHostInput) (*itemapi.SyncPRCIOutput, error) {
	next := repoNumberFromHost(input)
	return s.SyncPRCI(ctx, &next)
}

func (s *Handlers) enqueuePRSyncOnHost(ctx context.Context, input *repoNumberHostInput) (*itemapi.AcceptedOutput, error) {
	next := repoNumberFromHost(input)
	return s.EnqueuePRSync(ctx, &next)
}

func (s *Handlers) syncIssueOnHost(ctx context.Context, input *repoNumberHostInput) (*itemapi.SyncIssueOutput, error) {
	next := itemapi.IssueRepoNumberInput(repoNumberFromHost(input))
	return s.SyncIssue(ctx, &next)
}

func (s *Handlers) enqueueIssueSyncOnHost(ctx context.Context, input *repoNumberHostInput) (*itemapi.AcceptedOutput, error) {
	next := itemapi.IssueRepoNumberInput(repoNumberFromHost(input))
	return s.EnqueueIssueSync(ctx, &next)
}

func (s *Handlers) createIssueWorkspaceOnHost(ctx context.Context, input *createIssueWorkspaceHostInput) (*workspaceapi.CreateWorkspaceOutput, error) {
	next := workspaceapi.CreateIssueWorkspaceInput{
		Provider:     input.Provider,
		PlatformHost: input.PlatformHost,
		Owner:        input.Owner,
		Name:         input.Name,
		Number:       input.Number,
		Body:         input.Body,
	}
	return (*s.WorkspaceAPI).CreateIssueWorkspace(ctx, &next)
}

func (s *Handlers) createAdHocWorkspaceOnHost(ctx context.Context, input *createAdHocWorkspaceHostInput) (*workspaceapi.CreateWorkspaceOutput, error) {
	next := workspaceapi.CreateAdHocWorkspaceInput{
		Provider:     input.Provider,
		PlatformHost: input.PlatformHost,
		Owner:        input.Owner,
		Name:         input.Name,
		Body:         input.Body,
	}
	return (*s.WorkspaceAPI).CreateAdHocWorkspace(ctx, &next)
}
