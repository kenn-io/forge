package server

import (
	"context"

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
