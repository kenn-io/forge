package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
	gh "github.com/google/go-github/v89/github"
	"go.kenn.io/middleman/internal/db"
	"go.kenn.io/middleman/internal/gitclone"
	ghclient "go.kenn.io/middleman/internal/github"
	"go.kenn.io/middleman/internal/platform"
	"go.kenn.io/middleman/internal/ratelimit"
	"go.kenn.io/middleman/internal/server/httpapi"
	"go.kenn.io/middleman/internal/server/issueapi"
	"go.kenn.io/middleman/internal/server/pullapi"
)

type repoNumberInput struct {
	Provider     string `path:"provider"`
	PlatformHost string
	Owner        string `path:"owner"`
	Name         string `path:"name"`
	Number       int    `path:"number"`
}

type statusOnlyOutput = httpapi.OKStatusOutput

type issueRepoNumberInput struct {
	Provider     string `path:"provider"`
	PlatformHost string
	Owner        string `path:"owner"`
	Name         string `path:"name"`
	Number       int    `path:"number"`
}

type resolveItemInput struct {
	Provider     string `path:"provider"`
	PlatformHost string
	Owner        string `path:"owner"`
	Name         string `path:"name"`
	Number       int    `path:"number"`
	ItemType     string `query:"item_type" enum:"pr,issue" doc:"Optional item type hint for providers whose issues and merge requests have separate number spaces."`
}

type starredInput struct {
	Body starredRequest
}

type getRepoInput struct {
	Provider     string `path:"provider"`
	PlatformHost string
	Owner        string `path:"owner"`
	Name         string `path:"name"`
}

type getRepoOutput = httpapi.BodyOutput[repoResponse]

type getRepoCommitDiffInput struct {
	Provider     string `path:"provider"`
	PlatformHost string
	Owner        string `path:"owner"`
	Name         string `path:"name"`
	SHA          string `path:"sha"`
	Whitespace   string `query:"whitespace"`
}

type getRepoCommitDiffHostInput struct {
	Provider     string `path:"provider"`
	PlatformHost string `path:"platform_host"`
	Owner        string `path:"owner"`
	Name         string `path:"name"`
	SHA          string `path:"sha"`
	Whitespace   string `query:"whitespace"`
}

type getRepoCommitDiffOutput = httpapi.BodyOutput[diffResponse]

type commentAutocompleteInput struct {
	Provider     string `path:"provider"`
	PlatformHost string
	Owner        string `path:"owner"`
	Name         string `path:"name"`
	Trigger      string `query:"trigger"`
	Q            string `query:"q"`
	Limit        int    `query:"limit"`
}

type commentAutocompleteOutput = httpapi.BodyOutput[commentAutocompleteResponse]

type listReposOutput = httpapi.BodyOutput[[]repoResponse]

type listRepoSummariesOutput = httpapi.BodyOutput[[]repoSummaryResponse]

type acceptedOutput = httpapi.AcceptedStatusOutput

type syncPROutput = httpapi.BodyOutput[pullapi.MergeRequestDetailResponse]

type syncPRCIOutput = httpapi.BodyOutput[pullapi.MergeRequestDetailResponse]

type syncIssueOutput = httpapi.BodyOutput[issueapi.IssueDetailResponse]

type resolveItemOutput = httpapi.BodyOutput[resolveItemResponse]

type syncStatusOutput = httpapi.BodyOutput[*ghclient.SyncStatus]

type rateLimitsOutput = httpapi.BodyOutput[rateLimitsResponse]

type streamEventsInput struct {
	WorkspaceID string `query:"workspace_id" doc:"Optional selected local workspace to prewarm and validate while this stream is connected"`
}

type listActivityInput struct {
	Repo      string   `query:"repo" doc:"Repository filter. Accepts provider|platform_host/repo_path, with comma-separated values for multiple repositories."`
	Types     []string `query:"types"`
	ItemTypes []string `query:"item_types" doc:"Item scopes included before limiting activity results: pr, issue, or repo."`
	Search    string   `query:"search"`
	After     string   `query:"after"`
	Since     string   `query:"since"`
}

type triggerSyncInput struct {
	PriorityRepos []string `query:"priority_repo" doc:"Optional repository filters to sync first. Accepts repeated provider|platform_host/repo_path values or comma-separated values."`
	OnlyRepos     []string `query:"only_repo" doc:"Optional repository filters to sync exclusively. Accepts repeated provider|platform_host/repo_path values or comma-separated values."`
}

type listActivityOutput = httpapi.BodyOutput[activityResponse]

type listNotificationsInput struct {
	State  string   `query:"state"`
	Reason []string `query:"reason"`
	Type   []string `query:"type"`
	Repo   string   `query:"repo"`
	Search string   `query:"q"`
	Sort   string   `query:"sort"`
	Limit  int      `query:"limit"`
	Offset int      `query:"offset"`
}

type listNotificationsOutput struct {
	Body notificationsResponse
}

type notificationBulkInput struct {
	Body struct {
		IDs      []int64 `json:"ids"`
		MarkRead *bool   `json:"mark_read,omitempty"`
	}
}

type notificationBulkOutput struct {
	Body notificationBulkResponse
}

func apiConfig(basePath string) huma.Config {
	config := huma.DefaultConfig("middleman API", "0.1.0")
	config.OpenAPIPath = "/openapi"
	config.DocsPath = "/docs"
	config.SchemasPath = "/schemas"
	config.Servers = []*huma.Server{{
		URL: strings.TrimSuffix(basePath, "/") + "/api/v1",
	}}
	return config
}

func (s *Server) registerAPI(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID: "get-version",
		Method:      http.MethodGet,
		Path:        "/version",
		Summary:     "Get server version",
		Tags:        []string{"System"},
	}, s.getVersion)

	huma.Get(api, "/activity", s.listActivity,
		httpapi.DocumentOperation("list-activity", "List activity", "Activity"))
	s.kataAPI.Register(api)
	s.registerKataFrontendAPI(api)
	s.docsAPI.Register(api)
	s.registerArchiveAPI(api)
	huma.Register(api, huma.Operation{
		OperationID:   "list-notifications",
		Method:        http.MethodGet,
		Path:          "/notifications",
		DefaultStatus: http.StatusOK,
		Summary:       "List notifications",
		Tags:          []string{"Activity"},
	}, s.listNotifications)
	huma.Register(api, huma.Operation{
		OperationID:   "sync-notifications",
		Method:        http.MethodPost,
		Path:          "/notifications/sync",
		DefaultStatus: http.StatusAccepted,
		Summary:       "Sync notifications",
		Tags:          []string{"Sync"},
	}, s.syncNotifications)
	huma.Register(api, huma.Operation{
		OperationID:   "mark-notifications-read",
		Method:        http.MethodPost,
		Path:          "/notifications/read",
		DefaultStatus: http.StatusOK,
		Summary:       "Mark notifications read",
		Tags:          []string{"Activity"},
	}, s.markNotificationsRead)
	huma.Register(api, huma.Operation{
		OperationID:   "mark-notifications-done",
		Method:        http.MethodPost,
		Path:          "/notifications/done",
		DefaultStatus: http.StatusOK,
		Summary:       "Mark notifications done",
		Tags:          []string{"Activity"},
	}, s.markNotificationsDone)
	huma.Register(api, huma.Operation{
		OperationID:   "mark-notifications-undone",
		Method:        http.MethodPost,
		Path:          "/notifications/undone",
		DefaultStatus: http.StatusOK,
		Summary:       "Mark notifications undone",
		Tags:          []string{"Activity"},
	}, s.markNotificationsUndone)
	s.pullAPI.Register(api)
	s.issueAPI.Register(api)
	s.registerProviderRepoAPI(api)
	s.repoBrowserAPI.Register(api)
	s.fleetAPI.Register(api)

	huma.Register(api, huma.Operation{
		OperationID:   "list-repo-summaries",
		Method:        http.MethodGet,
		Path:          "/repos/summary",
		DefaultStatus: http.StatusOK,
		Summary:       "List repository summaries",
		Tags:          []string{"Repositories"},
	}, s.listRepoSummaries)
	huma.Register(api, huma.Operation{
		OperationID:   "set-starred",
		Method:        http.MethodPut,
		Path:          "/starred",
		DefaultStatus: http.StatusOK,
		Summary:       "Star repository",
		Tags:          []string{"Settings"},
	}, s.setStarred)
	huma.Register(api, huma.Operation{
		OperationID:   "unset-starred",
		Method:        http.MethodDelete,
		Path:          "/starred",
		DefaultStatus: http.StatusOK,
		Summary:       "Unstar repository",
		Tags:          []string{"Settings"},
	}, s.unsetStarred)

	huma.Get(api, "/repos", s.listRepos,
		httpapi.DocumentOperation("list-repos", "List repositories", "Repositories"))
	huma.Register(api, huma.Operation{
		OperationID:   "preview-repos",
		Method:        http.MethodPost,
		Path:          "/repos/preview",
		DefaultStatus: http.StatusOK,
		Summary:       "Preview repositories",
		Tags:          []string{"Repositories"},
	}, s.previewRepos)
	huma.Register(api, huma.Operation{
		OperationID:   "bulk-add-repos",
		Method:        http.MethodPost,
		Path:          "/repos/bulk",
		DefaultStatus: http.StatusCreated,
		Summary:       "Bulk add repositories",
		Tags:          []string{"Repositories"},
	}, s.bulkAddRepos)
	s.registerSettingsAPI(api)
	huma.Register(api, huma.Operation{
		OperationID:   "trigger-sync",
		Method:        http.MethodPost,
		Path:          "/sync",
		DefaultStatus: http.StatusAccepted,
		Summary:       "Trigger sync",
		Tags:          []string{"Sync"},
	}, s.triggerSync)
	huma.Register(api, huma.Operation{
		OperationID: "stream-events",
		Method:      http.MethodGet,
		Path:        "/events",
		Summary:     "Stream server events",
		Tags:        []string{"System"},
		Responses: map[string]*huma.Response{
			"200": {
				Description: "Server-sent event stream",
				Content: map[string]*huma.MediaType{
					"text/event-stream": {},
				},
			},
		},
	}, s.streamEvents)
	huma.Get(api, "/sync/status", s.syncStatus,
		httpapi.DocumentOperation("get-sync-status", "Get sync status", "Sync"))
	huma.Get(api, "/rate-limits", s.getRateLimits,
		httpapi.DocumentOperation("get-rate-limits", "Get rate limits", "Sync"))
	huma.Register(api, huma.Operation{
		OperationID:   "capture-telemetry-event",
		Method:        http.MethodPost,
		Path:          "/telemetry/events",
		DefaultStatus: http.StatusAccepted,
		Summary:       "Capture telemetry event",
		Tags:          []string{"System"},
	}, s.captureTelemetryEvent)
	huma.Register(api, huma.Operation{
		OperationID: "get-roborev-status",
		Method:      http.MethodGet,
		Path:        "/roborev/status",
		Summary:     "Get roborev status",
		Tags:        []string{"Roborev"},
	}, s.getRoborevStatus)

	s.workspaceAPI.Register(api)

	huma.Register(api, huma.Operation{
		OperationID: "complete-filesystem-path",
		Method:      http.MethodGet,
		Path:        "/filesystem/complete",
		Summary:     "Complete a local filesystem path",
		Tags:        []string{"System"},
	}, s.completeFilesystemPath)
	huma.Register(api, huma.Operation{
		OperationID: "validate-filesystem-repo",
		Method:      http.MethodGet,
		Path:        "/filesystem/validate-repo",
		Summary:     "Resolve a path to a repository root",
		Tags:        []string{"System"},
	}, s.validateFilesystemRepo)
	huma.Register(api, huma.Operation{
		OperationID: "list-user-repositories",
		Method:      http.MethodGet,
		Path:        "/platform/user-repositories",
		Summary:     "List the authenticated platform CLI user's repositories",
		Tags:        []string{"System"},
	}, s.listUserRepositories)
	huma.Register(api, huma.Operation{
		OperationID: "get-tooling-status",
		Method:      http.MethodGet,
		Path:        "/tooling-status",
		Summary:     "Report git/gh/glab CLI availability and auth",
		Tags:        []string{"System"},
	}, s.getToolingStatus)
	huma.Register(api, huma.Operation{
		OperationID: "launch-host-runtime-session",
		Method:      http.MethodPost,
		Path:        "/runtime/sessions",
		Summary:     "Launch host runtime session",
		Tags:        []string{"Runtime"},
	}, s.launchHostRuntimeSession)
	huma.Register(api, huma.Operation{
		OperationID: "list-host-runtime-sessions",
		Method:      http.MethodGet,
		Path:        "/runtime/sessions",
		Summary:     "List host runtime sessions",
		Tags:        []string{"Runtime"},
	}, s.listHostRuntimeSessions)
	huma.Register(api, huma.Operation{
		OperationID:   "stop-host-runtime-session",
		Method:        http.MethodDelete,
		Path:          "/runtime/sessions/{session_key}",
		DefaultStatus: http.StatusNoContent,
		Summary:       "Stop host runtime session",
		Tags:          []string{"Runtime"},
	}, s.stopHostRuntimeSession)
	huma.Register(api, huma.Operation{
		OperationID: "get-host-runtime-session-attach-spec",
		Method:      http.MethodGet,
		Path:        "/runtime/sessions/{session_key}/attach-spec",
		Summary:     "Get host runtime session attach spec",
		Tags:        []string{"Runtime"},
	}, s.getHostRuntimeSessionAttachSpec)
}

func (s *Server) registerProviderRepoAPI(api huma.API) {
	repoPath := "/repo/{provider}/{owner}/{name}"
	hostRepoPath := "/host/{platform_host}/repo/{provider}/{owner}/{name}"
	pullRepoPath := "/pulls/{provider}/{owner}/{name}"
	hostPullRepoPath := "/host/{platform_host}/pulls/{provider}/{owner}/{name}"
	pullPath := pullRepoPath + "/{number}"
	hostPullPath := hostPullRepoPath + "/{number}"
	issueRepoPath := "/issues/{provider}/{owner}/{name}"
	hostIssueRepoPath := "/host/{platform_host}/issues/{provider}/{owner}/{name}"
	issuePath := issueRepoPath + "/{number}"
	hostIssuePath := hostIssueRepoPath + "/{number}"

	huma.Post(api, repoPath+"/resolve/{number}", s.resolveItem,
		httpapi.DocumentOperation("resolve-repo-item", "Resolve repository item", "Repositories"))
	huma.Post(api, hostRepoPath+"/resolve/{number}", s.resolveItemOnHost,
		httpapi.DocumentOperation("resolve-repo-item-on-host", "Resolve repository item", "Repositories"))
	huma.Register(api, huma.Operation{OperationID: "create-repo-workspace", Method: http.MethodPost, Path: repoPath + "/workspaces", DefaultStatus: http.StatusAccepted, Summary: "Create workspace for new work", Tags: []string{"Workspaces"}}, s.workspaceAPI.CreateAdHocWorkspace)
	huma.Register(api, huma.Operation{OperationID: "create-repo-workspace-on-host", Method: http.MethodPost, Path: hostRepoPath + "/workspaces", DefaultStatus: http.StatusAccepted, Summary: "Create workspace for new work", Tags: []string{"Workspaces"}}, s.createAdHocWorkspaceOnHost)
	huma.Get(api, repoPath, s.getRepo,
		httpapi.DocumentOperation("get-repo", "Get repository", "Repositories"))
	huma.Get(api, hostRepoPath, s.getRepoOnHost,
		httpapi.DocumentOperation("get-repo-on-host", "Get repository", "Repositories"))
	huma.Register(api, huma.Operation{
		OperationID: "get-markdown-image", Method: http.MethodGet, Path: repoPath + "/markdown-image",
		DefaultStatus: http.StatusOK, Summary: "Get markdown image", Tags: []string{"Repositories"},
		Responses: markdownImageResponses(),
	}, s.getMarkdownImage)
	huma.Register(api, huma.Operation{
		OperationID: "get-markdown-image-on-host", Method: http.MethodGet, Path: hostRepoPath + "/markdown-image",
		DefaultStatus: http.StatusOK, Summary: "Get markdown image", Tags: []string{"Repositories"},
		Responses: markdownImageResponses(),
	}, s.getMarkdownImageOnHost)
	huma.Get(api, repoPath+"/commits/{sha}/diff", s.getRepoCommitDiff,
		httpapi.DocumentOperation("get-repo-commit-diff", "Get repository commit diff", "Repositories"))
	huma.Get(api, hostRepoPath+"/commits/{sha}/diff", s.getRepoCommitDiffOnHost,
		httpapi.DocumentOperation("get-repo-commit-diff-on-host", "Get repository commit diff", "Repositories"))
	huma.Register(api, huma.Operation{OperationID: "list-repo-labels", Method: http.MethodGet, Path: repoPath + "/labels", DefaultStatus: http.StatusOK, Summary: "List repository labels", Tags: []string{"Repositories"}}, s.listRepoLabels)
	huma.Register(api, huma.Operation{OperationID: "list-repo-labels-on-host", Method: http.MethodGet, Path: hostRepoPath + "/labels", DefaultStatus: http.StatusOK, Summary: "List repository labels", Tags: []string{"Repositories"}}, s.listRepoLabelsOnHost)
	huma.Get(api, repoPath+"/comment-autocomplete", s.getCommentAutocomplete,
		httpapi.DocumentOperation("get-comment-autocomplete", "Get comment autocomplete", "Repositories"))
	huma.Get(api, hostRepoPath+"/comment-autocomplete", s.getCommentAutocompleteOnHost,
		httpapi.DocumentOperation("get-comment-autocomplete-on-host", "Get comment autocomplete", "Repositories"))

	huma.Post(api, pullPath+"/sync", s.syncPR,
		httpapi.DocumentOperation("sync-pull", "Sync pull request", "Pull Requests"))
	huma.Post(api, hostPullPath+"/sync", s.syncPROnHost,
		httpapi.DocumentOperation("sync-pull-on-host", "Sync pull request", "Pull Requests"))
	huma.Post(api, pullPath+"/ci-refresh", s.syncPRCI,
		httpapi.DocumentOperation("refresh-pull-ci", "Refresh pull request CI", "Pull Requests"))
	huma.Post(api, hostPullPath+"/ci-refresh", s.syncPRCIOnHost,
		httpapi.DocumentOperation("refresh-pull-ci-on-host", "Refresh pull request CI", "Pull Requests"))
	huma.Register(api, huma.Operation{OperationID: "enqueue-pr-sync", Method: http.MethodPost, Path: pullPath + "/sync/async", DefaultStatus: http.StatusAccepted, Summary: "Enqueue pull request sync", Tags: []string{"Pull Requests"}}, s.enqueuePRSync)
	huma.Register(api, huma.Operation{OperationID: "enqueue-pr-sync-on-host", Method: http.MethodPost, Path: hostPullPath + "/sync/async", DefaultStatus: http.StatusAccepted, Summary: "Enqueue pull request sync", Tags: []string{"Pull Requests"}}, s.enqueuePRSyncOnHost)
	huma.Post(api, issuePath+"/sync", s.syncIssue,
		httpapi.DocumentOperation("sync-issue", "Sync issue", "Issues"))
	huma.Post(api, hostIssuePath+"/sync", s.syncIssueOnHost,
		httpapi.DocumentOperation("sync-issue-on-host", "Sync issue", "Issues"))
	huma.Register(api, huma.Operation{OperationID: "enqueue-issue-sync", Method: http.MethodPost, Path: issuePath + "/sync/async", DefaultStatus: http.StatusAccepted, Summary: "Enqueue issue sync", Tags: []string{"Issues"}}, s.enqueueIssueSync)
	huma.Register(api, huma.Operation{OperationID: "enqueue-issue-sync-on-host", Method: http.MethodPost, Path: hostIssuePath + "/sync/async", DefaultStatus: http.StatusAccepted, Summary: "Enqueue issue sync", Tags: []string{"Issues"}}, s.enqueueIssueSyncOnHost)
	huma.Register(api, huma.Operation{OperationID: "create-issue-workspace", Method: http.MethodPost, Path: issuePath + "/workspace", DefaultStatus: http.StatusAccepted, Summary: "Create issue workspace", Tags: []string{"Issues"}}, s.workspaceAPI.CreateIssueWorkspace)
	huma.Register(api, huma.Operation{OperationID: "create-issue-workspace-on-host", Method: http.MethodPost, Path: hostIssuePath + "/workspace", DefaultStatus: http.StatusAccepted, Summary: "Create issue workspace", Tags: []string{"Issues"}}, s.createIssueWorkspaceOnHost)
}

func NewOpenAPI() *huma.OpenAPI {
	mux := http.NewServeMux()
	s := &Server{}
	api := humago.NewWithPrefix(mux, "/api/v1", apiConfig("/"))
	s.registerAPI(api)
	return api.OpenAPI()
}

func (s *Server) getRepoCommitDiff(
	ctx context.Context,
	input *getRepoCommitDiffInput,
) (*getRepoCommitDiffOutput, error) {
	if s.clones == nil {
		return nil, httpapi.ServiceUnavailable("diff view not available: clone manager not configured")
	}

	repo, err := s.repoResolver.LookupRoute(
		ctx, input.Provider, input.PlatformHost, input.Owner, input.Name,
	)
	if err != nil {
		return nil, httpapi.ProviderRouteLookupError(err)
	}

	host := httpapi.ProviderHost(*repo)
	if !isFullGitObjectID(input.SHA) {
		return nil, httpapi.Validation("path.sha", "commit SHA must be a full object ID")
	}

	sha, err := s.clones.ResolveCommit(ctx, string(httpapi.ProviderKind(*repo)), host, repo.Owner, repo.Name, input.SHA)
	if err != nil {
		if errors.Is(err, gitclone.ErrNotFound) {
			return nil, httpapi.NotFound(httpapi.CodeNotFound, "diff not available: referenced commit not found", nil)
		}
		slog.Error("failed to resolve repo commit", "owner", input.Owner, "name", input.Name, "sha", input.SHA, "err", err)
		return nil, httpapi.Upstream("failed to compute diff", "", "")
	}

	parent, err := s.clones.ParentOf(ctx, string(httpapi.ProviderKind(*repo)), host, repo.Owner, repo.Name, sha)
	if err != nil {
		if errors.Is(err, gitclone.ErrNotFound) {
			return nil, httpapi.NotFound(httpapi.CodeNotFound, "diff not available: referenced commit not found", nil)
		}
		slog.Error("failed to resolve commit parent", "owner", input.Owner, "name", input.Name, "sha", sha, "err", err)
		return nil, httpapi.Upstream("failed to compute diff", "", "")
	}

	hideWhitespace := input.Whitespace == "hide"
	result, err := s.clones.Diff(ctx, string(httpapi.ProviderKind(*repo)), host, repo.Owner, repo.Name, parent, sha, hideWhitespace)
	if err != nil {
		if errors.Is(err, gitclone.ErrNotFound) {
			return nil, httpapi.NotFound(httpapi.CodeNotFound, "diff not available: referenced commit not found", nil)
		}
		slog.Error("failed to compute repo commit diff", "owner", input.Owner, "name", input.Name, "sha", sha, "err", err)
		return nil, httpapi.Upstream("failed to compute diff", "", "")
	}

	return &getRepoCommitDiffOutput{Body: diffResponse{
		Stale:               false,
		WhitespaceOnlyCount: result.WhitespaceOnlyCount,
		Files:               result.Files,
	}}, nil
}

func (s *Server) getRepoCommitDiffOnHost(
	ctx context.Context,
	input *getRepoCommitDiffHostInput,
) (*getRepoCommitDiffOutput, error) {
	next := getRepoCommitDiffInput{
		Provider:     input.Provider,
		PlatformHost: input.PlatformHost,
		Owner:        input.Owner,
		Name:         input.Name,
		SHA:          input.SHA,
		Whitespace:   input.Whitespace,
	}
	return s.getRepoCommitDiff(ctx, &next)
}

func isFullGitObjectID(value string) bool {
	switch len(value) {
	case 40, 64:
	default:
		return false
	}
	for _, c := range value {
		if c >= '0' && c <= '9' {
			continue
		}
		if c >= 'a' && c <= 'f' {
			continue
		}
		if c >= 'A' && c <= 'F' {
			continue
		}
		return false
	}
	return true
}

func (s *Server) setStarred(ctx context.Context, input *starredInput) (*statusOnlyOutput, error) {
	repoID, err := s.lookupStarredRepoID(ctx, input.Body)
	if err != nil {
		return nil, err
	}
	if err := s.db.SetStarred(ctx, input.Body.ItemType, repoID, input.Body.Number); err != nil {
		return nil, httpapi.Internal("set starred failed")
	}
	return &statusOnlyOutput{Status: http.StatusOK}, nil
}

func (s *Server) unsetStarred(ctx context.Context, input *starredInput) (*statusOnlyOutput, error) {
	repoID, err := s.lookupStarredRepoID(ctx, input.Body)
	if err != nil {
		return nil, err
	}
	if err := s.db.UnsetStarred(ctx, input.Body.ItemType, repoID, input.Body.Number); err != nil {
		return nil, httpapi.Internal("unset starred failed")
	}
	return &statusOnlyOutput{Status: http.StatusOK}, nil
}

func (s *Server) getRepo(ctx context.Context, input *getRepoInput) (*getRepoOutput, error) {
	repo, err := s.repoResolver.LookupRoute(
		ctx, input.Provider, input.PlatformHost, input.Owner, input.Name,
	)
	if err != nil {
		return nil, httpapi.ProviderRouteLookupError(err)
	}
	return &getRepoOutput{Body: s.repoResponse(*repo)}, nil
}

func (s *Server) getCommentAutocomplete(
	ctx context.Context,
	input *commentAutocompleteInput,
) (*commentAutocompleteOutput, error) {
	repo, err := s.repoResolver.LookupRoute(
		ctx, input.Provider, input.PlatformHost, input.Owner, input.Name,
	)
	if err != nil {
		return nil, httpapi.ProviderRouteLookupError(err)
	}

	limit := input.Limit
	if limit <= 0 {
		limit = 10
	}
	if limit > 25 {
		limit = 25
	}

	switch input.Trigger {
	case "@":
		users, err := s.db.ListCommentAutocompleteUsers(
			ctx,
			repo.Platform,
			repo.PlatformHost,
			input.Owner,
			input.Name,
			input.Q,
			limit,
		)
		if err != nil {
			return nil, httpapi.Internal("list comment autocomplete users failed")
		}
		return &commentAutocompleteOutput{Body: commentAutocompleteResponse{Users: users}}, nil
	case "#", "!":
		itemKind := ""
		if httpapi.ProviderKind(*repo) == platform.KindGitLab {
			itemKind = "issue"
			if input.Trigger == "!" {
				itemKind = "pull"
			}
		} else if input.Trigger == "!" {
			return nil, httpapi.Validation(
				"query.trigger",
				"trigger ! is only supported for GitLab merge requests",
				"@",
				"#",
			)
		}
		references, err := s.db.ListCommentAutocompleteReferences(
			ctx,
			repo.Platform,
			repo.PlatformHost,
			input.Owner,
			input.Name,
			input.Q,
			itemKind,
			limit,
		)
		if err != nil {
			return nil, httpapi.Internal("list comment autocomplete references failed")
		}
		return &commentAutocompleteOutput{Body: commentAutocompleteResponse{References: references}}, nil
	default:
		return nil, httpapi.Validation("query.trigger", "trigger must be @, #, or GitLab !", "@", "#", "!")
	}
}

func (s *Server) listRepos(ctx context.Context, _ *struct{}) (*listReposOutput, error) {
	repos, err := s.db.ListRepos(ctx)
	if err != nil {
		return nil, httpapi.Internal("list repos failed")
	}
	if repos == nil {
		repos = []db.Repo{}
	}
	if s.cfg != nil {
		repos = s.filterConfiguredRepos(repos)
	}

	out := make([]repoResponse, 0, len(repos))
	for _, repo := range repos {
		out = append(out, s.repoResponse(repo))
	}

	return &listReposOutput{Body: out}, nil
}

func (s *Server) listRepoSummaries(
	ctx context.Context, _ *struct{},
) (*listRepoSummariesOutput, error) {
	summaries, err := s.db.ListRepoSummaries(ctx)
	if err != nil {
		return nil, httpapi.Internal("list repo summaries failed")
	}
	if s.cfg != nil {
		summaries = s.filterConfiguredRepoSummaries(summaries)
	}

	defaultPlatformHost := s.defaultPlatformHost()
	out := make([]repoSummaryResponse, 0, len(summaries))
	for _, summary := range summaries {
		out = append(out, s.toRepoSummaryResponse(
			summary, defaultPlatformHost,
		))
	}

	return &listRepoSummariesOutput{Body: out}, nil
}

func (s *Server) triggerSync(
	ctx context.Context,
	input *triggerSyncInput,
) (*acceptedOutput, error) {
	if s.syncer == nil {
		return nil, httpapi.ServiceUnavailable("syncer not configured")
	}
	if len(input.OnlyRepos) > 0 {
		repos, err := s.onlyReposFromFilter(input.OnlyRepos)
		if err != nil {
			return nil, httpapi.Validation("query.only_repo", err.Error())
		}
		s.syncer.TriggerRunForRepos(context.WithoutCancel(ctx), repos)
		return &acceptedOutput{Status: http.StatusAccepted}, nil
	}
	s.syncer.TriggerRunWithPriority(
		context.WithoutCancel(ctx),
		s.priorityReposFromFilter(input.PriorityRepos),
	)
	if s.notificationsEnabled() {
		s.runBackground(func(bgCtx context.Context) {
			if err := s.syncer.RunNotificationSync(bgCtx); err != nil {
				slog.Warn("notification sync failed", "err", err)
			}
		})
	}
	return &acceptedOutput{Status: http.StatusAccepted}, nil
}

func (s *Server) onlyReposFromFilter(filters []string) ([]ghclient.RepoRef, error) {
	values := splitRepoFilterValues(filters)
	if len(values) == 0 {
		return nil, fmt.Errorf("repository must match a configured provider|platform_host/repo_path")
	}

	tracked := s.syncer.TrackedRepos()
	out := make([]ghclient.RepoRef, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		repo, ok := matchPriorityRepo(value, tracked)
		if !ok {
			return nil, fmt.Errorf("repository %q must match a configured provider|platform_host/repo_path", value)
		}
		key := priorityRepoIdentity(repo)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, repo)
	}
	return out, nil
}

func (s *Server) priorityReposFromFilter(filters []string) []ghclient.RepoRef {
	values := splitRepoFilterValues(filters)
	if len(values) == 0 {
		return nil
	}

	tracked := s.syncer.TrackedRepos()
	out := make([]ghclient.RepoRef, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if repo, ok := matchPriorityRepo(value, tracked); ok {
			key := priorityRepoIdentity(repo)
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			out = append(out, repo)
		}
	}
	return out
}

func splitRepoFilterValues(filters []string) []string {
	values := make([]string, 0, len(filters))
	for _, filter := range filters {
		for value := range strings.SplitSeq(filter, ",") {
			value = strings.Trim(value, "/ ")
			if value != "" {
				values = append(values, value)
			}
		}
	}
	return values
}

func matchPriorityRepo(
	filter string,
	tracked []ghclient.RepoRef,
) (ghclient.RepoRef, bool) {
	if provider, value, ok := strings.Cut(filter, "|"); ok {
		provider = strings.TrimSpace(provider)
		value = strings.Trim(value, "/ ")
		parts := strings.Split(value, "/")
		if provider == "" || len(parts) < 3 {
			return ghclient.RepoRef{}, false
		}
		kind, err := platform.NormalizeKind(provider)
		if err != nil {
			return ghclient.RepoRef{}, false
		}

		host := parts[0]
		path := strings.Join(parts[1:], "/")
		for _, repo := range tracked {
			if repoPlatformForPriority(repo) == kind &&
				strings.EqualFold(repoHostForPriority(repo), host) &&
				strings.EqualFold(repoPathForPriority(repo), path) {
				return repo, true
			}
		}
		return ghclient.RepoRef{}, false
	}
	return ghclient.RepoRef{}, false
}

func priorityRepoIdentity(repo ghclient.RepoRef) string {
	return strings.ToLower(
		string(repoPlatformForPriority(repo)) + "/" +
			repoHostForPriority(repo) + "/" +
			repoPathForPriority(repo),
	)
}

func repoPathForPriority(repo ghclient.RepoRef) string {
	path := strings.Trim(repo.RepoPath, "/ ")
	if path != "" {
		return path
	}
	return strings.Trim(repo.Owner, "/ ") + "/" + strings.Trim(repo.Name, "/ ")
}

func repoHostForPriority(repo ghclient.RepoRef) string {
	host := strings.TrimSpace(repo.PlatformHost)
	if host != "" {
		return strings.ToLower(host)
	}
	kind := repoPlatformForPriority(repo)
	if defaultHost, ok := platform.DefaultHost(kind); ok {
		return defaultHost
	}
	return platform.DefaultGitHubHost
}

func repoPlatformForPriority(repo ghclient.RepoRef) platform.Kind {
	if repo.Platform != "" {
		return repo.Platform
	}
	return platform.KindGitHub
}

func (s *Server) syncStatus(_ context.Context, _ *struct{}) (*syncStatusOutput, error) {
	if s.syncer == nil {
		return &syncStatusOutput{Body: &ghclient.SyncStatus{}}, nil
	}
	return &syncStatusOutput{Body: s.syncer.Status()}, nil
}

func (s *Server) getRateLimits(
	_ context.Context, _ *struct{},
) (*rateLimitsOutput, error) {
	if s.syncer == nil {
		return &rateLimitsOutput{Body: rateLimitsResponse{
			ProviderPools: map[string]rateLimitHostStatus{},
			LocalCeilings: map[string]localSyncCeilingStatus{},
		}}, nil
	}
	trackers := s.syncer.RateTrackers()
	gqlTrackers := s.syncer.GQLRateTrackers()
	for key, rt := range s.syncer.WriteGQLRateTrackers() {
		if gqlTrackers[key] == nil {
			gqlTrackers[key] = rt
		}
	}
	budgets := s.syncer.Budgets()
	principalLabels := s.syncer.RatePrincipalLabels()
	quotaRegistry := s.syncer.QuotaRegistry()

	labelFor := func(key, providerName, principal string) string {
		if label := principalLabels[key]; label != "" {
			return label
		}
		return ratePrincipalLabel(providerName, principal)
	}
	fromTracker := func(rt *ratelimit.RateTracker) rateLimitResourceStatus {
		if rt == nil {
			return rateLimitResourceStatus{Remaining: -1, Limit: -1}
		}
		resource := rateLimitResourceStatus{
			Remaining: rt.Remaining(), Limit: rt.RateLimit(),
			Known: rt.Known(), Requests: rt.RequestsThisHour(),
		}
		if resetAt := rt.ResetAt(); resetAt != nil {
			resource.ResetAt = formatUTCRFC3339(*resetAt)
		}
		return resource
	}

	hosts := make(map[string]rateLimitHostStatus, len(trackers))
	for key, rt := range trackers {
		statusKey := rateLimitStatusKey(rt)
		hosts[statusKey] = rateLimitHostStatus{
			Provider:           rt.Provider(),
			PlatformHost:       rt.PlatformHost(),
			RatePrincipal:      rt.Principal(),
			PrincipalLabel:     labelFor(key, rt.Provider(), rt.Principal()),
			ReserveBuffer:      ghclient.RateReserveBuffer,
			SyncThrottleFactor: rt.ThrottleFactor(),
			SyncPaused:         rt.IsPaused(),
			REST:               fromTracker(rt),
			GraphQL:            fromTracker(gqlTrackers[key]),
		}
	}
	// The registry records what GitHub actually reported per principal and
	// per resource, including pools no local tracker observed yet, so it
	// overrides the tracker-derived view wherever it holds a pool.
	if quotaRegistry != nil {
		for _, pool := range quotaRegistry.Snapshot() {
			key := rateLimitStatusKeyFor(
				string(platform.KindGitHub), pool.Identity.Host, pool.Identity.Principal,
			)
			status, ok := hosts[key]
			if !ok {
				status = rateLimitHostStatus{
					Provider:       string(platform.KindGitHub),
					PlatformHost:   pool.Identity.Host,
					RatePrincipal:  pool.Identity.Principal,
					PrincipalLabel: labelFor(key, string(platform.KindGitHub), pool.Identity.Principal),
					ReserveBuffer:  ghclient.RateReserveBuffer,
					REST:           rateLimitResourceStatus{Remaining: -1, Limit: -1},
					GraphQL:        rateLimitResourceStatus{Remaining: -1, Limit: -1},
				}
			}
			resource := rateLimitResourceStatus{
				Remaining: pool.Remaining, Limit: pool.Limit,
				Known: pool.Known, Requests: pool.Requests,
			}
			if !pool.ResetAt.IsZero() {
				resource.ResetAt = formatUTCRFC3339(pool.ResetAt)
			}
			if pool.Resource == ghclient.QuotaResourceGraphQL {
				status.GraphQL = resource
			} else {
				status.REST = resource
			}
			hosts[key] = status
		}
	}

	ceilings := make(map[string]localSyncCeilingStatus, len(budgets))
	for key, budget := range budgets {
		if budget == nil {
			continue
		}
		providerName := string(platform.KindGitHub)
		host := key
		principal := ""
		statusKey := key
		if tracker := trackers[key]; tracker != nil {
			providerName = tracker.Provider()
			host = tracker.PlatformHost()
			principal = tracker.Principal()
			statusKey = rateLimitStatusKey(tracker)
		} else if prefix, remainder, ok := strings.Cut(key, ":"); ok {
			providerName = prefix
			host = remainder
		}
		ceilings[statusKey] = localSyncCeilingStatus{
			Provider: providerName, PlatformHost: host,
			RatePrincipal:  principal,
			PrincipalLabel: labelFor(key, providerName, principal),
			Limit:          budget.Limit(),
			Spent:          budget.Spent(),
			Remaining:      budget.Remaining(),
		}
	}
	return &rateLimitsOutput{
		Body: rateLimitsResponse{ProviderPools: hosts, LocalCeilings: ceilings},
	}, nil
}

func rateLimitStatusKey(rt *ratelimit.RateTracker) string {
	return rateLimitStatusKeyFor(rt.Provider(), rt.PlatformHost(), rt.Principal())
}

// rateLimitStatusKeyFor names one principal's entry in the rate-limit
// response. Tracker-derived rows and registry-derived pools must agree on it
// or the same principal would appear twice.
func rateLimitStatusKeyFor(providerName, host, principal string) string {
	if principal == "" || principal == "host" {
		return ghclient.RateBucketKey(providerName, host, "host")
	}
	return strings.Join([]string{providerName, host, principal}, ":")
}

func ratePrincipalLabel(providerName, principal string) string {
	if providerName != string(platform.KindGitHub) || principal == "host" {
		return "Host credential"
	}
	if id, ok := strings.CutPrefix(principal, "installation:"); ok {
		return "GitHub App installation " + id
	}
	if id, ok := strings.CutPrefix(principal, "user:"); ok {
		return "GitHub user " + id
	}
	return principal
}

func (s *Server) syncPRCI(ctx context.Context, input *repoNumberInput) (*syncPRCIOutput, error) {
	repo, err := s.repoResolver.LookupRoute(
		ctx, input.Provider, input.PlatformHost, input.Owner, input.Name,
	)
	if err != nil {
		return nil, httpapi.ProviderRouteLookupError(err)
	}

	mr, err := s.db.GetMergeRequestByRepoIDAndNumber(ctx, repo.ID, input.Number)
	if err != nil {
		return nil, httpapi.Internal("get pull request: " + err.Error())
	}
	if mr == nil {
		return nil, httpapi.NotFound(httpapi.CodePullNotFound, "pull request not found", nil)
	}
	warnings, err := s.syncer.RefreshMRCIStatusOnProvider(
		ctx,
		ghclient.RepoRef{
			Platform:           httpapi.ProviderKind(*repo),
			Owner:              repo.Owner,
			Name:               repo.Name,
			PlatformHost:       httpapi.ProviderHost(*repo),
			RepoPath:           repo.RepoPath,
			PlatformExternalID: repo.PlatformRepoID,
			WebURL:             repo.WebURL,
			CloneURL:           repo.CloneURL,
			DefaultBranch:      repo.DefaultBranch,
		},
		repo.ID,
		input.Number,
		mr.PlatformHeadSHA,
	)
	if err != nil {
		return nil, httpapi.ProviderCallProblemWithDetail(
			err,
			string(httpapi.ProviderKind(*repo)), httpapi.ProviderHost(*repo),
			"refresh PR CI: "+err.Error(),
		)
	}

	mr, err = s.db.GetMergeRequestByRepoIDAndNumber(ctx, repo.ID, input.Number)
	if err != nil {
		return nil, httpapi.Internal("get pull request: " + err.Error())
	}
	if mr == nil {
		return nil, httpapi.NotFound(httpapi.CodePullNotFound, "pull request not found after CI refresh", nil)
	}
	body, err := s.pullAPI.BuildDetail(ctx, mr)
	if err != nil {
		return nil, err
	}
	body.Warnings = append(body.Warnings, warnings...)
	return &syncPRCIOutput{Body: body}, nil
}

func (s *Server) syncPR(ctx context.Context, input *repoNumberInput) (*syncPROutput, error) {
	repo, err := s.repoResolver.LookupRoute(
		ctx, input.Provider, input.PlatformHost, input.Owner, input.Name,
	)
	if err != nil {
		return nil, httpapi.ProviderRouteLookupError(err)
	}
	// SyncMR distinguishes a non-fatal diff failure from a hard sync failure
	// via DiffSyncError. The PR row, timeline, and CI status are all current
	// in either case, so degrade gracefully: keep the response, but report
	// the diff problem as a warning so the UI can explain why the diff view
	// is stale or empty.
	var diffErr *ghclient.DiffSyncError
	syncErr := s.syncer.SyncMROnProvider(
		ctx, httpapi.ProviderKind(*repo), httpapi.ProviderHost(*repo),
		repo.Owner, repo.Name, input.Number,
	)
	if syncErr != nil && !errors.As(syncErr, &diffErr) {
		if strings.Contains(syncErr.Error(), "is not tracked") {
			return nil, httpapi.Forbidden(syncErr.Error(), nil)
		}
		return nil, httpapi.ProviderCallProblemWithDetail(
			syncErr,
			string(httpapi.ProviderKind(*repo)), httpapi.ProviderHost(*repo),
			"sync PR: "+syncErr.Error(),
		)
	}

	mr, err := s.db.GetMergeRequestByRepoIDAndNumber(ctx, repo.ID, input.Number)
	if err != nil {
		return nil, httpapi.Internal("get pull request: " + err.Error())
	}
	if mr == nil {
		return nil, httpapi.NotFound(httpapi.CodePullNotFound, "pull request not found after sync", nil)
	}

	body, err := s.pullAPI.BuildDetail(ctx, mr)
	if err != nil {
		return nil, err
	}

	if diffErr != nil {
		slog.Warn("diff sync failed during sync PR",
			"owner", input.Owner,
			"name", input.Name,
			"number", input.Number,
			"code", diffErr.Code,
			"err", diffErr.Err,
		)
		// Replace inferred warnings with the explicit error, which is
		// more specific than the row-state-based diffWarnings.
		body.Warnings = []string{diffErr.UserMessage()}
	}

	return &syncPROutput{Body: body}, nil
}

func (s *Server) enqueuePRSync(ctx context.Context, input *repoNumberInput) (*acceptedOutput, error) {
	repo, err := s.repoResolver.LookupRoute(
		ctx, input.Provider, input.PlatformHost, input.Owner, input.Name,
	)
	if err != nil {
		return nil, httpapi.ProviderRouteLookupError(err)
	}
	kind := httpapi.ProviderKind(*repo)
	host := httpapi.ProviderHost(*repo)
	key := "pr:" + string(kind) + ":" + host + ":" + repo.RepoPath +
		"#" + strconv.Itoa(input.Number)
	s.enqueueDetailSync(
		key,
		[]any{
			"type", "pr",
			"provider", string(kind),
			"platform_host", host,
			"repo_path", repo.RepoPath,
			"owner", repo.Owner,
			"name", repo.Name,
			"number", input.Number,
		},
		func(ctx context.Context) error {
			return s.syncer.SyncMROnProvider(
				ctx, kind, host, repo.Owner, repo.Name, input.Number,
			)
		},
	)
	return &acceptedOutput{Status: http.StatusAccepted}, nil
}

func (s *Server) syncIssue(ctx context.Context, input *issueRepoNumberInput) (*syncIssueOutput, error) {
	repo, err := s.repoResolver.LookupRoute(
		ctx, input.Provider, input.PlatformHost, input.Owner, input.Name,
	)
	if err != nil {
		return nil, httpapi.ProviderRouteLookupError(err)
	}
	err = s.syncer.SyncIssueOnProvider(
		ctx, httpapi.ProviderKind(*repo), httpapi.ProviderHost(*repo),
		repo.Owner, repo.Name, input.Number,
	)
	if err != nil {
		if strings.Contains(err.Error(), "is not tracked") {
			return nil, httpapi.Forbidden(err.Error(), nil)
		}
		return nil, httpapi.ProviderCallProblemWithDetail(
			err,
			string(httpapi.ProviderKind(*repo)), httpapi.ProviderHost(*repo),
			"sync issue: "+err.Error(),
		)
	}

	issue, err := s.db.GetIssueByRepoIDAndNumber(ctx, repo.ID, input.Number)
	if err != nil {
		return nil, httpapi.Internal("get issue: " + err.Error())
	}
	if issue == nil {
		return nil, httpapi.NotFound(httpapi.CodeIssueNotFound, "issue not found after sync", nil)
	}

	syncIssueResp, err := s.issueAPI.BuildDetail(ctx, repo, issue)
	if err != nil {
		return nil, err
	}
	return &syncIssueOutput{Body: syncIssueResp}, nil
}

func (s *Server) enqueueIssueSync(ctx context.Context, input *issueRepoNumberInput) (*acceptedOutput, error) {
	repo, err := s.repoResolver.LookupRoute(
		ctx, input.Provider, input.PlatformHost, input.Owner, input.Name,
	)
	if err != nil {
		return nil, httpapi.ProviderRouteLookupError(err)
	}
	kind := httpapi.ProviderKind(*repo)
	host := httpapi.ProviderHost(*repo)
	key := "issue:" + string(kind) + ":" + host + ":" + repo.RepoPath +
		"#" + strconv.Itoa(input.Number)
	s.enqueueDetailSync(
		key,
		[]any{
			"type", "issue",
			"provider", string(kind),
			"platform_host", host,
			"repo_path", repo.RepoPath,
			"owner", repo.Owner,
			"name", repo.Name,
			"number", input.Number,
		},
		func(ctx context.Context) error {
			return s.syncer.SyncIssueOnProvider(
				ctx, kind, host, repo.Owner, repo.Name, input.Number,
			)
		},
	)
	return &acceptedOutput{Status: http.StatusAccepted}, nil
}

func (s *Server) listActivity(ctx context.Context, input *listActivityInput) (*listActivityOutput, error) {
	if hasInvalidRepoFilter(input.Repo) {
		return nil, httpapi.Validation("query.repo", "repo filter must be provider|platform_host/repo_path")
	}

	opts := db.ListActivityOpts{
		Repo:        input.Repo,
		RepoFilters: parseRepoFilters(input.Repo),
		Types:       input.Types,
		ItemTypes:   input.ItemTypes,
		Search:      input.Search,
		// Notifications are always on; this only drops notification rows in
		// SQL when no config is loaded (the nil-config safety guard), so the
		// safety-cap window is filled by real activity, not stale notifications.
		ExcludeNotifications: !s.notificationsEnabled(),
	}

	opts.Limit = activitySafetyCap + 1

	if input.Since != "" {
		t, err := time.Parse(time.RFC3339, input.Since)
		if err != nil {
			return nil, httpapi.Validation("query.since", "invalid since: "+err.Error())
		}
		opts.Since = &t
	} else {
		defaultSince := s.now().UTC().AddDate(0, 0, -7)
		opts.Since = &defaultSince
	}

	if input.After != "" {
		t, source, sourceID, err := db.DecodeCursor(input.After)
		if err != nil {
			return nil, httpapi.Validation("query.after", "invalid after cursor: "+err.Error())
		}
		opts.AfterTime = &t
		opts.AfterSource = source
		opts.AfterSourceID = sourceID
	}

	if s.cfg != nil {
		trackedRepos, err := s.trackedNotificationRepoFilters()
		if err != nil {
			return nil, httpapi.Internal("load tracked notification repos failed")
		}
		opts.NotificationRepoFilters = trackedRepos
	}

	items, err := s.db.ListActivity(ctx, opts)
	if err != nil {
		slog.Error("list activity failed", "err", err)
		return nil, httpapi.Internal("list activity failed")
	}

	if s.cfg != nil {
		tracked := make(map[string]struct{})
		for _, repo := range s.syncer.TrackedRepos() {
			tracked[trackedRepoKey(repo)] = struct{}{}
		}
		filtered := make([]db.ActivityItem, 0, len(items))
		for _, it := range items {
			key := trackedRepoKey(ghclient.RepoRef{
				Platform:     platform.Kind(it.Platform),
				PlatformHost: it.PlatformHost,
				Owner:        it.RepoOwner,
				Name:         it.RepoName,
			})
			if _, ok := tracked[key]; ok {
				filtered = append(filtered, it)
			}
		}
		items = filtered
	}

	capped := len(items) > activitySafetyCap
	if capped {
		items = items[:activitySafetyCap]
	}

	workspacesByItem, err := s.buildWorkspaceRefLookup(ctx)
	if err != nil {
		return nil, httpapi.Internal("load workspace refs failed")
	}

	out := make([]activityItemResponse, len(items))
	for i, it := range items {
		item := activityItemResponse{
			ID:           it.Source + ":" + strconv.FormatInt(it.SourceID, 10),
			Cursor:       db.EncodeCursor(it.CreatedAt, it.Source, it.SourceID),
			ActivityType: it.ActivityType,
			Repo: s.repoResolver.RefFromParts(
				it.Platform, it.PlatformHost, it.RepoOwner, it.RepoName,
			),
			PlatformHost: it.PlatformHost,
			RepoOwner:    it.RepoOwner,
			RepoName:     it.RepoName,
			ItemType:     it.ItemType,
			ItemNumber:   it.ItemNumber,
			ItemTitle:    it.ItemTitle,
			ItemURL:      it.ItemURL,
			ItemState:    it.ItemState,
			Workspace:    workspaceRefForActivityItem(workspacesByItem, it),
			Author:       it.Author,
			ItemAuthor:   it.ItemAuthor,
			CreatedAt:    formatUTCRFC3339(it.CreatedAt),
			BodyPreview:  it.BodyPreview,
		}
		item.BranchName = it.BranchName
		item.CommitSHA = it.CommitSHA
		item.BeforeSHA = it.BeforeSHA
		item.AfterSHA = it.AfterSHA
		item.AuthorName = it.AuthorName
		item.AuthorEmail = it.AuthorEmail
		item.CommitterName = it.CommitterName
		item.CommitterEmail = it.CommitterEmail
		if it.AuthoredAt != nil {
			item.AuthoredAt = formatUTCRFC3339(*it.AuthoredAt)
		}
		if it.CommittedAt != nil {
			item.CommittedAt = formatUTCRFC3339(*it.CommittedAt)
		}
		item.ActivityURL = it.ActivityURL
		if item.ActivityURL == "" {
			item.ActivityURL = branchActivityURL(it)
		}
		item.SubjectState = it.SubjectState
		out[i] = item
	}

	return &listActivityOutput{
		Body: activityResponse{Items: out, Capped: capped},
	}, nil
}

func branchActivityURL(it db.ActivityItem) string {
	if it.CommitSHA == "" && (it.BeforeSHA == "" || it.AfterSHA == "") {
		return ""
	}
	kind := platform.Kind(it.Platform)
	meta, ok := platform.MetadataFor(kind)
	if !ok {
		return ""
	}
	host, ok := platform.HostOrDefault(meta.Kind, it.PlatformHost)
	if !ok || host == "" {
		return ""
	}
	repoPath := escapedRepoPath(it.RepoOwner, it.RepoName)
	switch meta.Kind {
	case platform.KindGitHub, platform.KindForgejo, platform.KindGitea:
		if it.CommitSHA == "" {
			return "https://" + host + "/" + repoPath + "/compare/" +
				url.PathEscape(it.BeforeSHA) + "..." + url.PathEscape(it.AfterSHA)
		}
		return "https://" + host + "/" + repoPath + "/commit/" + url.PathEscape(it.CommitSHA)
	case platform.KindGitLab:
		if it.CommitSHA == "" {
			return "https://" + host + "/" + repoPath + "/-/compare/" +
				url.PathEscape(it.BeforeSHA) + "..." + url.PathEscape(it.AfterSHA)
		}
		return "https://" + host + "/" + repoPath + "/-/commit/" + url.PathEscape(it.CommitSHA)
	default:
		return ""
	}
}

func escapedRepoPath(owner, name string) string {
	parts := strings.Split(strings.Trim(owner+"/"+name, "/"), "/")
	escaped := make([]string, 0, len(parts))
	for _, part := range parts {
		if part != "" {
			escaped = append(escaped, url.PathEscape(part))
		}
	}
	return strings.Join(escaped, "/")
}

func (s *Server) resolveItem(
	ctx context.Context, input *resolveItemInput,
) (*resolveItemOutput, error) {
	number := input.Number
	requestedItemType := input.ItemType
	if requestedItemType != "" &&
		requestedItemType != "pr" &&
		requestedItemType != "issue" {
		return nil, httpapi.Validation("query.item_type",
			"item_type must be 'pr' or 'issue'", "pr", "issue")
	}
	repo, err := s.repoResolver.LookupRoute(
		ctx, input.Provider, input.PlatformHost, input.Owner, input.Name,
	)
	if errors.Is(err, httpapi.ErrRepoNotFound) {
		return &resolveItemOutput{
			Body: resolveItemResponse{
				Number:      number,
				RepoTracked: false,
			},
		}, nil
	}
	if err != nil {
		return nil, httpapi.ProviderRouteLookupError(err)
	}
	providerKind := httpapi.ProviderKind(*repo)
	providerHost := httpapi.ProviderHost(*repo)
	itemTypeHint := requestedItemType
	if providerKind != platform.KindGitLab {
		itemTypeHint = ""
	}
	if !s.isConfiguredRepoTracked(*repo) {
		return &resolveItemOutput{
			Body: resolveItemResponse{
				Number:      number,
				RepoTracked: false,
			},
		}, nil
	}
	var (
		itemType string
		found    bool
	)
	if itemTypeHint != "" {
		itemType, found, err = s.db.ResolveItemNumberOfType(
			ctx, repo.ID, number, itemTypeHint,
		)
	} else {
		itemType, found, err = s.db.ResolveItemNumber(ctx, repo.ID, number)
	}
	if err != nil {
		return nil, httpapi.Internal("resolve item: " + err.Error())
	}
	if found {
		return &resolveItemOutput{
			Body: resolveItemResponse{
				ItemType:    itemType,
				Number:      number,
				RepoTracked: true,
			},
		}, nil
	}

	if providerKind == platform.KindGitLab && itemTypeHint != "" {
		var syncErr error
		switch itemTypeHint {
		case "pr":
			syncErr = s.syncer.SyncMROnProvider(
				ctx, providerKind, providerHost, repo.Owner, repo.Name, number,
			)
		case "issue":
			syncErr = s.syncer.SyncIssueOnProvider(
				ctx, providerKind, providerHost, repo.Owner, repo.Name, number,
			)
		}
		var diffErr *ghclient.DiffSyncError
		if syncErr != nil && !errors.As(syncErr, &diffErr) {
			if strings.Contains(syncErr.Error(), "is not tracked") {
				return nil, httpapi.Forbidden(syncErr.Error(), nil)
			}
			return nil, httpapi.ProviderCallProblemWithDetail(
				syncErr, string(providerKind), providerHost,
				"resolve item: "+syncErr.Error(),
			)
		}
		itemType, found, err = s.db.ResolveItemNumberOfType(
			ctx, repo.ID, number, itemTypeHint,
		)
		if err != nil {
			return nil, httpapi.Internal("resolve item: " + err.Error())
		}
		if !found {
			return nil, httpapi.NotFound(httpapi.CodeNotFound, "item not found", nil)
		}
		if diffErr != nil {
			slog.Warn("resolve item: diff sync failed but PR row was synced",
				"owner", repo.Owner,
				"name", repo.Name,
				"number", number,
				"err", syncErr,
			)
		}
		return &resolveItemOutput{
			Body: resolveItemResponse{
				ItemType:    itemType,
				Number:      number,
				RepoTracked: true,
			},
		}, nil
	}

	if providerKind != platform.KindGitHub {
		return nil, httpapi.NotFound(httpapi.CodeNotFound, "item not found", nil)
	}

	itemType, err = s.syncer.SyncItemByNumber(
		ctx, repo.Owner, repo.Name, number,
	)
	if err == nil && itemTypeHint != "" && itemType != itemTypeHint {
		return nil, httpapi.NotFound(httpapi.CodeNotFound, "item not found", nil)
	}
	// A DiffSyncError means the PR row was upserted but the diff
	// computation failed. Resolution doesn't need diff data, so treat
	// the result as success here. The resolve response has no warnings
	// field, so the staleness reaches the client when they navigate to
	// the PR detail page: getPull infers the warning from the persisted
	// row state via diffWarnings.
	var diffErr *ghclient.DiffSyncError
	if err != nil && !errors.As(err, &diffErr) {
		// Classified lookup outcomes (removed, inaccessible, moved with
		// its destination) arrive as platform errors; map them to their
		// typed problems instead of collapsing into an internal error.
		var platformErr *platform.Error
		if errors.As(err, &platformErr) {
			return nil, httpapi.MapPlatformError(err)
		}
		var ghErr *gh.ErrorResponse
		if errors.As(err, &ghErr) {
			if ghErr.Response != nil &&
				ghErr.Response.StatusCode == 404 {
				return nil, httpapi.NotFound(httpapi.CodeNotFound,
					"item not found: "+err.Error(), nil)
			}
			return nil, httpapi.Upstream(
				"GitHub API error: "+err.Error(),
				string(httpapi.ProviderKind(*repo)), httpapi.ProviderHost(*repo),
			)
		}
		return nil, httpapi.Internal("resolve item: " + err.Error())
	}
	if diffErr != nil {
		slog.Warn("resolve item: diff sync failed but PR row was synced",
			"owner", repo.Owner,
			"name", repo.Name,
			"number", number,
			"err", err,
		)
	}

	return &resolveItemOutput{
		Body: resolveItemResponse{
			ItemType:    itemType,
			Number:      number,
			RepoTracked: true,
		},
	}, nil
}

func (s *Server) lookupStarredRepoID(ctx context.Context, body starredRequest) (int64, error) {
	if !validateStarredRequest(body) {
		return 0, httpapi.Validation("body.item_type",
			"item_type must be 'pr' or 'issue'", "pr", "issue")
	}
	if strings.TrimSpace(body.Provider) == "" {
		return 0, httpapi.Validation("body.provider", "provider is required")
	}

	repo, err := s.repoResolver.LookupRoute(
		ctx, body.Provider, body.PlatformHost, body.Owner, body.Name,
	)
	if err != nil {
		if errors.Is(err, httpapi.ErrRepoNotFound) {
			return 0, httpapi.NotFound(httpapi.CodeRepoNotFound, err.Error(), nil)
		}
		return 0, httpapi.ProviderRouteLookupError(err)
	}

	return repo.ID, nil
}

// --- Commits ---
