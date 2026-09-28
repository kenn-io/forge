package server

import (
	"context"
	"errors"

	"github.com/danielgtaylor/huma/v2"
	"go.kenn.io/forge/internal/externalcontext"
	"go.kenn.io/forge/internal/server/httpapi"
	"go.kenn.io/forge/internal/server/pullapi"
)

type externalContextSourcesResponse struct {
	Sources []externalcontext.ExternalContextSourceInfo `json:"sources"`
}

type (
	externalContextSourcesOutput = httpapi.BodyOutput[externalContextSourcesResponse]
	externalContextOutput        = httpapi.BodyOutput[externalcontext.ExternalContextResult]
)

type externalContextInput struct {
	Provider       string `path:"provider"`
	PlatformHost   string
	Owner          string `path:"owner"`
	Name           string `path:"name"`
	Number         int    `path:"number" minimum:"1"`
	SourceID       string `path:"source_id" minLength:"1" maxLength:"128"`
	PlatformRepoID string `query:"platform_repo_id" required:"true" minLength:"1"`
	Refresh        bool   `query:"refresh"`
}

type externalContextHostInput struct {
	Provider       string `path:"provider"`
	PlatformHost   string `path:"platform_host"`
	Owner          string `path:"owner"`
	Name           string `path:"name"`
	Number         int    `path:"number" minimum:"1"`
	SourceID       string `path:"source_id" minLength:"1" maxLength:"128"`
	PlatformRepoID string `query:"platform_repo_id" required:"true" minLength:"1"`
	Refresh        bool   `query:"refresh"`
}

type externalContextActionRequest struct {
	PlatformRepoID string `json:"platform_repo_id" minLength:"1"`
	HeadSHA        string `json:"head_sha" minLength:"1" maxLength:"128"`
}

type externalContextActionInput struct {
	Provider     string `path:"provider"`
	PlatformHost string
	Owner        string `path:"owner"`
	Name         string `path:"name"`
	Number       int    `path:"number" minimum:"1"`
	SourceID     string `path:"source_id" minLength:"1" maxLength:"128"`
	ActionID     string `path:"action_id" minLength:"1" maxLength:"128"`
	Body         externalContextActionRequest
}

type externalContextActionHostInput struct {
	Provider     string `path:"provider"`
	PlatformHost string `path:"platform_host"`
	Owner        string `path:"owner"`
	Name         string `path:"name"`
	Number       int    `path:"number" minimum:"1"`
	SourceID     string `path:"source_id" minLength:"1" maxLength:"128"`
	ActionID     string `path:"action_id" minLength:"1" maxLength:"128"`
	Body         externalContextActionRequest
}

func (s *Server) registerExternalContextAPI(api huma.API) {
	huma.Get(api, "/external-context/sources", s.listExternalContextSources,
		httpapi.DocumentOperation("list-external-context-sources", "List external context sources", "External Context"))
	path := "/pulls/{provider}/{owner}/{name}/{number}/external-context/{source_id}"
	hostPath := "/host/{platform_host}" + path
	huma.Get(api, path, s.getPullExternalContext,
		httpapi.DocumentOperation("get-pull-external-context", "Get external pull request context", "External Context"))
	huma.Get(api, hostPath, s.getPullExternalContextOnHost,
		httpapi.DocumentOperation("get-pull-external-context-on-host", "Get external pull request context", "External Context"))
	huma.Post(api, path+"/actions/{action_id}", s.runPullExternalContextAction,
		httpapi.DocumentOperation("run-pull-external-context-action", "Run external pull request context action", "External Context"))
	huma.Post(api, hostPath+"/actions/{action_id}", s.runPullExternalContextActionOnHost,
		httpapi.DocumentOperation("run-pull-external-context-action-on-host", "Run external pull request context action", "External Context"))
}

func (s *Server) listExternalContextSources(context.Context, *struct{}) (*externalContextSourcesOutput, error) {
	return &externalContextSourcesOutput{Body: externalContextSourcesResponse{Sources: s.externalContext.Sources()}}, nil
}

func (s *Server) getPullExternalContext(ctx context.Context, input *externalContextInput) (*externalContextOutput, error) {
	pull, err := s.externalContextPull(ctx, repoNumberInput{Provider: input.Provider, PlatformHost: input.PlatformHost, Owner: input.Owner, Name: input.Name, Number: input.Number}, input.PlatformRepoID)
	if err != nil {
		return nil, err
	}
	result, err := s.externalContext.Read(ctx, input.SourceID, pull, input.Refresh)
	if err != nil {
		return nil, externalContextProblem(err)
	}
	return &externalContextOutput{Body: result}, nil
}

func (s *Server) getPullExternalContextOnHost(ctx context.Context, input *externalContextHostInput) (*externalContextOutput, error) {
	return s.getPullExternalContext(ctx, &externalContextInput{
		Provider: input.Provider, PlatformHost: input.PlatformHost, Owner: input.Owner, Name: input.Name, Number: input.Number,
		SourceID: input.SourceID, PlatformRepoID: input.PlatformRepoID, Refresh: input.Refresh,
	})
}

func (s *Server) runPullExternalContextAction(ctx context.Context, input *externalContextActionInput) (*externalContextOutput, error) {
	pull, err := s.externalContextPull(ctx, repoNumberInput{Provider: input.Provider, PlatformHost: input.PlatformHost, Owner: input.Owner, Name: input.Name, Number: input.Number}, input.Body.PlatformRepoID)
	if err != nil {
		return nil, err
	}
	if pull.HeadSHA == "" {
		return nil, httpapi.Conflict(httpapi.CodeConflict, "No pull request head is synced. Refresh the pull request.", map[string]any{"reason": "head_unknown"})
	}
	if pull.HeadSHA != input.Body.HeadSHA {
		return nil, httpapi.Conflict(httpapi.CodeConflict, "The synced pull request head changed. Refresh before running this action.", map[string]any{"reason": "stale_state"})
	}
	result, err := s.externalContext.Action(ctx, input.SourceID, pull, input.ActionID)
	if err != nil {
		return nil, externalContextProblem(err)
	}
	return &externalContextOutput{Body: result}, nil
}

func (s *Server) runPullExternalContextActionOnHost(ctx context.Context, input *externalContextActionHostInput) (*externalContextOutput, error) {
	return s.runPullExternalContextAction(ctx, &externalContextActionInput{
		Provider: input.Provider, PlatformHost: input.PlatformHost, Owner: input.Owner, Name: input.Name, Number: input.Number,
		SourceID: input.SourceID, ActionID: input.ActionID, Body: input.Body,
	})
}

// Both paths read the hub's last synced snapshot, never a live provider head.
func (s *Server) externalContextPull(ctx context.Context, input repoNumberInput, expectedRepoID string) (externalcontext.PullRequest, error) {
	item := pullapi.ItemIdentity{Provider: input.Provider, PlatformHost: input.PlatformHost, Owner: input.Owner, Name: input.Name, Number: input.Number}
	var detail pullapi.MergeRequestDetailResponse
	var err error
	if s.providerSource != nil {
		detail, err = s.providerSource.GetPull(ctx, item)
	} else {
		detail, err = s.pullAPI.GetProviderService(ctx, item)
	}
	if err != nil {
		return externalcontext.PullRequest{}, err
	}
	if detail.Repo.PlatformRepoID == "" || detail.Repo.PlatformRepoID != expectedRepoID {
		return externalcontext.PullRequest{}, httpapi.Conflict(httpapi.CodeConflict, "The repository identity changed. Reload the pull request.", map[string]any{"reason": "stale_state"})
	}
	if detail.MergeRequest == nil {
		return externalcontext.PullRequest{}, httpapi.NotFound(httpapi.CodePullNotFound, "Pull request not found", nil)
	}
	return externalcontext.PullRequest{
		Provider: detail.Repo.Provider, PlatformHost: detail.PlatformHost,
		PlatformRepoID: detail.Repo.PlatformRepoID, RepoPath: detail.Repo.RepoPath,
		Number: detail.MergeRequest.Number, URL: detail.MergeRequest.URL,
		State: string(detail.MergeRequest.State), HeadSHA: detail.PlatformHeadSHA,
		BaseSHA: detail.PlatformBaseSHA,
	}, nil
}

func externalContextProblem(err error) error {
	if errors.Is(err, externalcontext.ErrUnknownSource) {
		return httpapi.NotFound(httpapi.CodeNotFound, "External context source is not configured", nil)
	}
	// Runner errors are categorical and exclude command paths and output.
	return httpapi.Upstream(err.Error(), "", "")
}
