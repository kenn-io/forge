package server

import (
	"context"

	"go.kenn.io/middleman/internal/server/httpapi"
	"go.kenn.io/middleman/internal/server/workspaceapi"
)

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

type createIssueHostInput struct {
	Provider     string `path:"provider"`
	PlatformHost string `path:"platform_host"`
	Owner        string `path:"owner"`
	Name         string `path:"name"`
	Body         struct {
		Title string `json:"title"`
		Body  string `json:"body"`
	}
}

type postIssueCommentHostInput struct {
	Provider     string `path:"provider"`
	PlatformHost string `path:"platform_host"`
	Owner        string `path:"owner"`
	Name         string `path:"name"`
	Number       int    `path:"number"`
	Body         struct {
		Body string `json:"body"`
	}
}

type editIssueCommentHostInput struct {
	Provider     string `path:"provider"`
	PlatformHost string `path:"platform_host"`
	Owner        string `path:"owner"`
	Name         string `path:"name"`
	Number       int    `path:"number"`
	CommentID    int64  `path:"comment_id"`
	Body         struct {
		Body string `json:"body"`
	}
}

type deleteIssueCommentHostInput struct {
	Provider     string `path:"provider"`
	PlatformHost string `path:"platform_host"`
	Owner        string `path:"owner"`
	Name         string `path:"name"`
	Number       int    `path:"number"`
	CommentID    int64  `path:"comment_id"`
}

type getRepoHostInput struct {
	Provider     string `path:"provider"`
	PlatformHost string `path:"platform_host"`
	Owner        string `path:"owner"`
	Name         string `path:"name"`
}

type setIssueLabelsHostInput struct {
	Provider     string `path:"provider"`
	PlatformHost string `path:"platform_host"`
	Owner        string `path:"owner"`
	Name         string `path:"name"`
	Number       int    `path:"number"`
	Body         httpapi.SetLabelsRequest
}

type setIssueAssigneesHostInput struct {
	Provider     string `path:"provider"`
	PlatformHost string `path:"platform_host"`
	Owner        string `path:"owner"`
	Name         string `path:"name"`
	Number       int    `path:"number"`
	Body         httpapi.SetAssigneesRequest
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

type editIssueContentHostInput struct {
	Provider     string `path:"provider"`
	PlatformHost string `path:"platform_host"`
	Owner        string `path:"owner"`
	Name         string `path:"name"`
	Number       int    `path:"number"`
	Body         struct {
		Title *string `json:"title,omitempty"`
		Body  *string `json:"body,omitempty"`
	}
}

type githubStateHostInput struct {
	Provider     string `path:"provider"`
	PlatformHost string `path:"platform_host"`
	Owner        string `path:"owner"`
	Name         string `path:"name"`
	Number       int    `path:"number"`
	Body         struct {
		State string `json:"state"`
	}
}

type createIssueWorkspaceHostInput struct {
	Provider     string `path:"provider"`
	PlatformHost string `path:"platform_host"`
	Owner        string `path:"owner"`
	Name         string `path:"name"`
	Number       int    `path:"number"`
	Body         struct {
		GitHeadRef          *string `json:"git_head_ref,omitempty"`
		ReuseExistingBranch bool    `json:"reuse_existing_branch,omitempty"`
	}
}

func (s *Server) editIssueContentOnHost(ctx context.Context, input *editIssueContentHostInput) (*editIssueContentOutput, error) {
	next := editIssueContentInput{
		Provider:     input.Provider,
		PlatformHost: input.PlatformHost,
		Owner:        input.Owner,
		Name:         input.Name,
		Number:       input.Number,
		Body:         input.Body,
	}
	return s.editIssueContent(ctx, &next)
}

func (s *Server) setIssueAssigneesOnHost(ctx context.Context, input *setIssueAssigneesHostInput) (*setAssigneesOutput, error) {
	next := setIssueAssigneesInput{
		Provider:     input.Provider,
		PlatformHost: input.PlatformHost,
		Owner:        input.Owner,
		Name:         input.Name,
		Number:       input.Number,
		Body:         input.Body,
	}
	return s.setIssueAssignees(ctx, &next)
}

func (s *Server) createIssueOnHost(ctx context.Context, input *createIssueHostInput) (*createIssueOutput, error) {
	next := createIssueInput{
		Provider:     input.Provider,
		PlatformHost: input.PlatformHost,
		Owner:        input.Owner,
		Name:         input.Name,
		Body:         input.Body,
	}
	return s.createIssue(ctx, &next)
}

func (s *Server) getIssueOnHost(ctx context.Context, input *repoNumberHostInput) (*getIssueOutput, error) {
	next := issueRepoNumberInput(repoNumberFromHost(input))
	return s.getIssue(ctx, &next)
}

func (s *Server) postIssueCommentOnHost(ctx context.Context, input *postIssueCommentHostInput) (*postIssueCommentOutput, error) {
	next := postIssueCommentInput{
		Provider:     input.Provider,
		PlatformHost: input.PlatformHost,
		Owner:        input.Owner,
		Name:         input.Name,
		Number:       input.Number,
		Body:         input.Body,
	}
	return s.postIssueComment(ctx, &next)
}

func (s *Server) editIssueCommentOnHost(ctx context.Context, input *editIssueCommentHostInput) (*editIssueCommentOutput, error) {
	next := editIssueCommentInput{
		Provider:     input.Provider,
		PlatformHost: input.PlatformHost,
		Owner:        input.Owner,
		Name:         input.Name,
		Number:       input.Number,
		CommentID:    input.CommentID,
		Body:         input.Body,
	}
	return s.editIssueComment(ctx, &next)
}

func (s *Server) deleteIssueCommentOnHost(
	ctx context.Context,
	input *deleteIssueCommentHostInput,
) (*deleteIssueCommentOutput, error) {
	return s.deleteIssueComment(ctx, &deleteIssueCommentInput{
		Provider: input.Provider, PlatformHost: input.PlatformHost,
		Owner: input.Owner, Name: input.Name, Number: input.Number, CommentID: input.CommentID,
	})
}

func (s *Server) setIssueLabelsOnHost(ctx context.Context, input *setIssueLabelsHostInput) (*setLabelsOutput, error) {
	next := setIssueLabelsInput{
		Provider:     input.Provider,
		PlatformHost: input.PlatformHost,
		Owner:        input.Owner,
		Name:         input.Name,
		Number:       input.Number,
		Body:         input.Body,
	}
	return s.setIssueLabels(ctx, &next)
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

func (s *Server) setIssueGitHubStateOnHost(ctx context.Context, input *githubStateHostInput) (*githubStateOutput, error) {
	next := githubStateInput{
		Provider:     input.Provider,
		PlatformHost: input.PlatformHost,
		Owner:        input.Owner,
		Name:         input.Name,
		Number:       input.Number,
		Body:         input.Body,
	}
	return s.setIssueGitHubState(ctx, &next)
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
