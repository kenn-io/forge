package server

import (
	"context"
	"encoding/base64"
	"errors"
	"log/slog"
	"mime"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
	gh "github.com/google/go-github/v84/github"
	"github.com/wesm/middleman/internal/db"
	"github.com/wesm/middleman/internal/gitclone"
	ghclient "github.com/wesm/middleman/internal/github"
	"github.com/wesm/middleman/internal/platform"
	"github.com/wesm/middleman/internal/platform/gitealike"
	"github.com/wesm/middleman/internal/workspace"
	"github.com/wesm/middleman/internal/workspace/localruntime"
)

type listPullsInput struct {
	Repo    string `query:"repo"`
	State   string `query:"state"`
	Kanban  string `query:"kanban"`
	Starred bool   `query:"starred"`
	Q       string `query:"q"`
	Limit   int    `query:"limit"`
	Offset  int    `query:"offset"`
}

type listPullsOutput = bodyOutput[[]mergeRequestResponse]

type repoNumberInput struct {
	Provider     string `path:"provider"`
	PlatformHost string
	Owner        string `path:"owner"`
	Name         string `path:"name"`
	Number       int    `path:"number"`
}

type getPullOutput = bodyOutput[mergeRequestDetailResponse]

func providerRouteLookupError(err error) error {
	if errors.Is(err, errRepoPathRequired) {
		return huma.Error400BadRequest(err.Error())
	}
	if errors.Is(err, errRepoNotFound) {
		return huma.Error404NotFound("repo not found")
	}
	if strings.Contains(err.Error(), "platform_host is required") ||
		strings.Contains(err.Error(), "unsupported platform") {
		return huma.Error400BadRequest(err.Error())
	}
	return huma.Error500InternalServerError("get repo failed")
}

type getMRImportMetadataOutput = bodyOutput[mrImportMetadataResponse]

type setKanbanStateInput struct {
	Provider     string `path:"provider"`
	PlatformHost string
	Owner        string `path:"owner"`
	Name         string `path:"name"`
	Number       int    `path:"number"`
	Body         struct {
		Status string `json:"status"`
	}
}

type statusOnlyOutput = okStatusOutput

type postCommentInput struct {
	Provider     string `path:"provider"`
	PlatformHost string
	Owner        string `path:"owner"`
	Name         string `path:"name"`
	Number       int    `path:"number"`
	Body         struct {
		Body string `json:"body"`
	}
}

type postCommentOutput = createdOutput[db.MREvent]

type editCommentInput struct {
	Provider     string `path:"provider"`
	PlatformHost string
	Owner        string `path:"owner"`
	Name         string `path:"name"`
	Number       int    `path:"number"`
	CommentID    int64  `path:"comment_id"`
	Body         struct {
		Body string `json:"body"`
	}
}

type editCommentOutput = bodyOutput[db.MREvent]

type listIssuesInput struct {
	Repo    string `query:"repo"`
	State   string `query:"state"`
	Starred bool   `query:"starred"`
	Q       string `query:"q"`
	Limit   int    `query:"limit"`
	Offset  int    `query:"offset"`
}

type listIssuesOutput = bodyOutput[[]issueResponse]

type issueRepoNumberInput struct {
	Provider     string `path:"provider"`
	PlatformHost string
	Owner        string `path:"owner"`
	Name         string `path:"name"`
	Number       int    `path:"number"`
}

type getIssueOutput = bodyOutput[issueDetailResponse]

type postIssueCommentInput struct {
	Provider     string `path:"provider"`
	PlatformHost string
	Owner        string `path:"owner"`
	Name         string `path:"name"`
	Number       int    `path:"number"`
	Body         struct {
		Body string `json:"body"`
	}
}

type postIssueCommentOutput = createdOutput[db.IssueEvent]

type editIssueCommentInput struct {
	Provider     string `path:"provider"`
	PlatformHost string
	Owner        string `path:"owner"`
	Name         string `path:"name"`
	Number       int    `path:"number"`
	CommentID    int64  `path:"comment_id"`
	Body         struct {
		Body string `json:"body"`
	}
}

type editIssueCommentOutput = bodyOutput[db.IssueEvent]

type createIssueInput struct {
	Provider     string `path:"provider"`
	PlatformHost string
	Owner        string `path:"owner"`
	Name         string `path:"name"`
	Body         struct {
		Title string `json:"title"`
		Body  string `json:"body"`
	}
}

type createIssueOutput = createdOutput[issueResponse]

type starredInput struct {
	Body starredRequest
}

type getRepoInput struct {
	Provider     string `path:"provider"`
	PlatformHost string
	Owner        string `path:"owner"`
	Name         string `path:"name"`
}

type getRepoOutput = bodyOutput[repoResponse]

type commentAutocompleteInput struct {
	Provider     string `path:"provider"`
	PlatformHost string
	Owner        string `path:"owner"`
	Name         string `path:"name"`
	Trigger      string `query:"trigger"`
	Q            string `query:"q"`
	Limit        int    `query:"limit"`
}

type commentAutocompleteOutput = bodyOutput[commentAutocompleteResponse]

type approvePRInput struct {
	Provider     string `path:"provider"`
	PlatformHost string
	Owner        string `path:"owner"`
	Name         string `path:"name"`
	Number       int    `path:"number"`
	Body         struct {
		Body string `json:"body"`
	}
}

type actionStatusBody struct {
	Status        string `json:"status"`
	ApprovedCount int    `json:"approved_count,omitempty"`
}

type actionStatusOutput = bodyOutput[actionStatusBody]

type mergePRInput struct {
	Provider     string `path:"provider"`
	PlatformHost string
	Owner        string `path:"owner"`
	Name         string `path:"name"`
	Number       int    `path:"number"`
	Body         struct {
		CommitTitle   string `json:"commit_title"`
		CommitMessage string `json:"commit_message"`
		Method        string `json:"method"`
	}
}

type mergePRBody struct {
	Merged  bool   `json:"merged"`
	SHA     string `json:"sha"`
	Message string `json:"message"`
}

type mergePROutput = bodyOutput[mergePRBody]

type editPRContentInput struct {
	Provider     string `path:"provider"`
	PlatformHost string
	Owner        string `path:"owner"`
	Name         string `path:"name"`
	Number       int    `path:"number"`
	Body         struct {
		Title *string `json:"title,omitempty"`
		Body  *string `json:"body,omitempty"`
	}
}

type editPRContentOutput = bodyOutput[mergeRequestDetailResponse]

type editIssueContentInput struct {
	Provider     string `path:"provider"`
	PlatformHost string
	Owner        string `path:"owner"`
	Name         string `path:"name"`
	Number       int    `path:"number"`
	Body         struct {
		Title *string `json:"title,omitempty"`
		Body  *string `json:"body,omitempty"`
	}
}

type editIssueContentOutput = bodyOutput[issueDetailResponse]

type githubStateInput struct {
	Provider     string `path:"provider"`
	PlatformHost string
	Owner        string `path:"owner"`
	Name         string `path:"name"`
	Number       int    `path:"number"`
	Body         struct {
		State string `json:"state"`
	}
}

type githubStateOutputBody struct {
	State string `json:"state"`
}

type githubStateOutput = bodyOutput[githubStateOutputBody]

type listReposOutput = bodyOutput[[]repoResponse]

type listRepoSummariesOutput = bodyOutput[[]repoSummaryResponse]

type acceptedOutput = acceptedStatusOutput

type syncPROutput = bodyOutput[mergeRequestDetailResponse]

type syncPRCIOutput = bodyOutput[mergeRequestDetailResponse]

type syncIssueOutput = bodyOutput[issueDetailResponse]

type resolveItemOutput = bodyOutput[resolveItemResponse]

type syncStatusOutput = bodyOutput[*ghclient.SyncStatus]

type rateLimitsOutput = bodyOutput[rateLimitsResponse]

type listStacksInput struct {
	Repo string `query:"repo"`
}

type listStacksOutput = bodyOutput[[]stackResponse]

type getStackForPROutput = bodyOutput[stackContextResponse]

type createWorkspaceInput struct {
	Body struct {
		PlatformHost string `json:"platform_host"`
		Owner        string `json:"owner"`
		Name         string `json:"name"`
		MRNumber     int    `json:"mr_number"`
	}
}

type createIssueWorkspaceInput struct {
	Provider     string `path:"provider"`
	PlatformHost string
	Owner        string `path:"owner"`
	Name         string `path:"name"`
	Number       int    `path:"number"`
	Body         struct {
		GitHeadRef          *string `json:"git_head_ref,omitempty"`
		ReuseExistingBranch bool    `json:"reuse_existing_branch,omitempty"`
	}
}

const issueWorkspaceBranchConflictType = "urn:middleman:error:issue-workspace-branch-conflict"

type getWorkspaceInput struct {
	ID string `path:"id"`
}

type getWorkspaceFilesInput struct {
	ID         string `path:"id"`
	Base       string `query:"base"      doc:"Diff base: head, pushed, or merge-target"`
	Whitespace string `query:"whitespace" doc:"Set to hide to ignore whitespace-only changes"`
	Commit     string `query:"commit" doc:"Scope to a single commit SHA"`
	From       string `query:"from"   doc:"Start SHA for range diff (inclusive)"`
	To         string `query:"to"     doc:"End SHA for range diff (inclusive)"`
}

type getWorkspaceDiffInput struct {
	ID         string `path:"id"`
	Base       string `query:"base"      doc:"Diff base: head, pushed, or merge-target"`
	Whitespace string `query:"whitespace" doc:"Set to hide to ignore whitespace-only changes"`
	Path       string `query:"path"      doc:"Optional file path to limit the returned patch"`
	Commit     string `query:"commit" doc:"Scope to a single commit SHA"`
	From       string `query:"from"   doc:"Start SHA for range diff (inclusive)"`
	To         string `query:"to"     doc:"End SHA for range diff (inclusive)"`
}

type getWorkspaceCommitsInput struct {
	ID string `path:"id"`
}

type retryWorkspaceInput struct {
	ID string `path:"id"`
}

type getWorkspaceRuntimeInput struct {
	ID string `path:"id"`
}

type launchWorkspaceRuntimeSessionInput struct {
	ID   string `path:"id"`
	Body struct {
		TargetKey string `json:"target_key"`
	}
}

type stopWorkspaceRuntimeSessionInput struct {
	ID         string `path:"id"`
	SessionKey string `path:"session_key"`
}

type deleteWorkspaceInput struct {
	ID    string `path:"id"`
	Force bool   `query:"force"`
}

type listWorkspacesOutputBody struct {
	Workspaces []workspaceResponse `json:"workspaces"`
}

type listWorkspacesOutput = bodyOutput[listWorkspacesOutputBody]

type getWorkspaceOutput = bodyOutput[workspaceResponse]

type getWorkspaceDiffOutput = bodyOutput[diffResponse]
type getWorkspaceFilesOutput = bodyOutput[filesResponse]
type getWorkspaceCommitsOutput = bodyOutput[commitsResponse]

type getWorkspaceRuntimeOutput = bodyOutput[workspaceRuntimeResponse]

type workspaceRuntimeSessionOutput = bodyOutput[localruntime.SessionInfo]

type createWorkspaceOutput = acceptedBodyOutput[workspaceResponse]

type workspaceDiffRequest struct {
	Summary           *db.WorkspaceSummary
	Base              workspace.WorktreeDiffBase
	MergeTargetBranch string
	FromSHA           string
	ToSHA             string
}

type listActivityInput struct {
	Repo   string   `query:"repo"`
	Types  []string `query:"types"`
	Search string   `query:"search"`
	After  string   `query:"after"`
	Since  string   `query:"since"`
}

type listActivityOutput = bodyOutput[activityResponse]

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

// documentOperation returns an operation-builder callback that sets Summary,
// Tags, and OperationID on the resulting *huma.Operation. Use it with the
// huma.Get/Post/Put/Patch/Delete convenience helpers so routes registered
// through shorthand still carry the metadata that the OpenAPI contract test
// enforces.
func documentOperation(
	operationID, summary string, tags ...string,
) func(*huma.Operation) {
	return func(o *huma.Operation) {
		o.OperationID = operationID
		o.Summary = summary
		o.Tags = tags
	}
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
		documentOperation("list-activity", "List activity", "Activity"))
	huma.Get(api, "/pulls", s.listPulls,
		documentOperation("list-pulls", "List pull requests", "Pull Requests"))
	huma.Get(api, "/issues", s.listIssues,
		documentOperation("list-issues", "List issues", "Issues"))
	s.registerProviderRepoAPI(api)

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
		documentOperation("list-repos", "List repositories", "Repositories"))
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
		documentOperation("get-sync-status", "Get sync status", "Sync"))
	huma.Get(api, "/rate-limits", s.getRateLimits,
		documentOperation("get-rate-limits", "Get rate limits", "Sync"))
	huma.Register(api, huma.Operation{
		OperationID: "get-roborev-status",
		Method:      http.MethodGet,
		Path:        "/roborev/status",
		Summary:     "Get roborev status",
		Tags:        []string{"Roborev"},
	}, s.getRoborevStatus)

	huma.Get(api, "/stacks", s.listStacks,
		documentOperation("list-stacks", "List stacks", "Stacks"))

	huma.Register(api, huma.Operation{
		OperationID:   "create-workspace",
		Method:        http.MethodPost,
		Path:          "/workspaces",
		DefaultStatus: http.StatusAccepted,
		Summary:       "Create workspace",
		Tags:          []string{"Workspaces"},
	}, s.createWorkspace)
	huma.Get(api, "/workspaces", s.listWorkspaces,
		documentOperation("list-workspaces", "List workspaces", "Workspaces"))
	huma.Get(api, "/workspaces/{id}", s.getWorkspace,
		documentOperation("get-workspace", "Get workspace", "Workspaces"))
	huma.Get(api, "/workspaces/{id}/commits", s.getWorkspaceCommits,
		documentOperation("get-workspace-commits", "Get workspace commits", "Workspaces"))
	huma.Get(api, "/workspaces/{id}/diff", s.getWorkspaceDiff,
		documentOperation("get-workspace-diff", "Get workspace diff", "Workspaces"))
	huma.Get(api, "/workspaces/{id}/files", s.getWorkspaceFiles,
		documentOperation("get-workspace-files", "Get workspace files", "Workspaces"))
	huma.Register(api, huma.Operation{
		OperationID:   "retry-workspace",
		Method:        http.MethodPost,
		Path:          "/workspaces/{id}/retry",
		DefaultStatus: http.StatusAccepted,
		Summary:       "Retry workspace",
		Tags:          []string{"Workspaces"},
	}, s.retryWorkspace)
	huma.Register(api, huma.Operation{
		OperationID: "get-workspace-runtime",
		Method:      http.MethodGet,
		Path:        "/workspaces/{id}/runtime",
		Summary:     "Get workspace runtime",
		Tags:        []string{"Workspaces"},
	}, s.getWorkspaceRuntime)
	huma.Register(api, huma.Operation{
		OperationID: "launch-workspace-runtime-session",
		Method:      http.MethodPost,
		Path:        "/workspaces/{id}/runtime/sessions",
		Summary:     "Launch workspace runtime session",
		Tags:        []string{"Workspaces"},
	}, s.launchWorkspaceRuntimeSession)
	huma.Register(api, huma.Operation{
		OperationID:   "stop-workspace-runtime-session",
		Method:        http.MethodDelete,
		Path:          "/workspaces/{id}/runtime/sessions/{session_key}",
		DefaultStatus: http.StatusNoContent,
		Summary:       "Stop workspace runtime session",
		Tags:          []string{"Workspaces"},
	}, s.stopWorkspaceRuntimeSession)
	huma.Register(api, huma.Operation{
		OperationID: "ensure-workspace-runtime-shell",
		Method:      http.MethodPost,
		Path:        "/workspaces/{id}/runtime/shell",
		Summary:     "Ensure workspace runtime shell",
		Tags:        []string{"Workspaces"},
	}, s.ensureWorkspaceRuntimeShell)
	huma.Register(api, huma.Operation{
		OperationID:   "delete-workspace",
		Method:        http.MethodDelete,
		Path:          "/workspaces/{id}",
		DefaultStatus: http.StatusNoContent,
		Summary:       "Delete workspace",
		Tags:          []string{"Workspaces"},
	}, s.deleteWorkspace)

	huma.Register(api, huma.Operation{
		OperationID:   "register-project",
		Method:        http.MethodPost,
		Path:          "/projects",
		DefaultStatus: http.StatusCreated,
		Summary:       "Register project",
		Tags:          []string{"Projects"},
	}, s.registerProject)
	huma.Register(api, huma.Operation{
		OperationID: "list-projects",
		Method:      http.MethodGet,
		Path:        "/projects",
		Summary:     "List projects",
		Tags:        []string{"Projects"},
	}, s.listProjects)
	huma.Register(api, huma.Operation{
		OperationID: "get-project",
		Method:      http.MethodGet,
		Path:        "/projects/{project_id}",
		Summary:     "Get project",
		Tags:        []string{"Projects"},
	}, s.getProject)
	huma.Register(api, huma.Operation{
		OperationID:   "register-worktree",
		Method:        http.MethodPost,
		Path:          "/projects/{project_id}/worktrees",
		DefaultStatus: http.StatusCreated,
		Summary:       "Register worktree",
		Tags:          []string{"Projects"},
	}, s.registerWorktree)
	huma.Register(api, huma.Operation{
		OperationID: "list-worktrees",
		Method:      http.MethodGet,
		Path:        "/projects/{project_id}/worktrees",
		Summary:     "List worktrees",
		Tags:        []string{"Projects"},
	}, s.listWorktrees)
	huma.Register(api, huma.Operation{
		OperationID: "list-launch-targets",
		Method:      http.MethodGet,
		Path:        "/projects/{project_id}/launch-targets",
		Summary:     "List launch targets",
		Tags:        []string{"Projects"},
	}, s.listLaunchTargets)
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

	huma.Get(api, pullPath, s.getPull,
		documentOperation("get-pull", "Get pull request", "Pull Requests"))
	huma.Get(api, hostPullPath, s.getPullOnHost,
		documentOperation("get-pull-on-host", "Get pull request", "Pull Requests"))
	huma.Get(api, pullPath+"/import-metadata", s.getMRImportMetadata,
		documentOperation("get-pull-import-metadata", "Get pull request import metadata", "Pull Requests"))
	huma.Get(api, hostPullPath+"/import-metadata", s.getMRImportMetadataOnHost,
		documentOperation("get-pull-import-metadata-on-host", "Get pull request import metadata", "Pull Requests"))
	huma.Register(api, huma.Operation{OperationID: "set-kanban-state", Method: http.MethodPut, Path: pullPath + "/state", DefaultStatus: http.StatusOK, Summary: "Set pull request kanban state", Tags: []string{"Pull Requests"}}, s.setKanbanState)
	huma.Register(api, huma.Operation{OperationID: "set-kanban-state-on-host", Method: http.MethodPut, Path: hostPullPath + "/state", DefaultStatus: http.StatusOK, Summary: "Set pull request kanban state", Tags: []string{"Pull Requests"}}, s.setKanbanStateOnHost)
	huma.Register(api, huma.Operation{OperationID: "edit-pr-content", Method: http.MethodPatch, Path: pullPath, DefaultStatus: http.StatusOK, Summary: "Edit pull request content", Tags: []string{"Pull Requests"}}, s.editPRContent)
	huma.Register(api, huma.Operation{OperationID: "edit-pr-content-on-host", Method: http.MethodPatch, Path: hostPullPath, DefaultStatus: http.StatusOK, Summary: "Edit pull request content", Tags: []string{"Pull Requests"}}, s.editPRContentOnHost)
	huma.Register(api, huma.Operation{OperationID: "post-pr-comment", Method: http.MethodPost, Path: pullPath + "/comments", DefaultStatus: http.StatusCreated, Summary: "Post pull request comment", Tags: []string{"Pull Requests"}}, s.postComment)
	huma.Register(api, huma.Operation{OperationID: "post-pr-comment-on-host", Method: http.MethodPost, Path: hostPullPath + "/comments", DefaultStatus: http.StatusCreated, Summary: "Post pull request comment", Tags: []string{"Pull Requests"}}, s.postCommentOnHost)
	huma.Register(api, huma.Operation{OperationID: "edit-pr-comment", Method: http.MethodPatch, Path: pullPath + "/comments/{comment_id}", DefaultStatus: http.StatusOK, Summary: "Edit pull request comment", Tags: []string{"Pull Requests"}}, s.editComment)
	huma.Register(api, huma.Operation{OperationID: "edit-pr-comment-on-host", Method: http.MethodPatch, Path: hostPullPath + "/comments/{comment_id}", DefaultStatus: http.StatusOK, Summary: "Edit pull request comment", Tags: []string{"Pull Requests"}}, s.editCommentOnHost)
	huma.Register(api, huma.Operation{OperationID: "set-pr-labels", Method: http.MethodPut, Path: pullPath + "/labels", DefaultStatus: http.StatusOK, Summary: "Set pull request labels", Tags: []string{"Pull Requests"}}, s.setPullLabels)
	huma.Register(api, huma.Operation{OperationID: "set-pr-labels-on-host", Method: http.MethodPut, Path: hostPullPath + "/labels", DefaultStatus: http.StatusOK, Summary: "Set pull request labels", Tags: []string{"Pull Requests"}}, s.setPullLabelsOnHost)

	huma.Register(api, huma.Operation{OperationID: "create-issue", Method: http.MethodPost, Path: issueRepoPath, DefaultStatus: http.StatusCreated, Summary: "Create issue", Tags: []string{"Issues"}}, s.createIssue)
	huma.Register(api, huma.Operation{OperationID: "create-issue-on-host", Method: http.MethodPost, Path: hostIssueRepoPath, DefaultStatus: http.StatusCreated, Summary: "Create issue", Tags: []string{"Issues"}}, s.createIssueOnHost)
	huma.Get(api, issuePath, s.getIssue,
		documentOperation("get-issue", "Get issue", "Issues"))
	huma.Get(api, hostIssuePath, s.getIssueOnHost,
		documentOperation("get-issue-on-host", "Get issue", "Issues"))
	huma.Register(api, huma.Operation{OperationID: "post-issue-comment", Method: http.MethodPost, Path: issuePath + "/comments", DefaultStatus: http.StatusCreated, Summary: "Post issue comment", Tags: []string{"Issues"}}, s.postIssueComment)
	huma.Register(api, huma.Operation{OperationID: "post-issue-comment-on-host", Method: http.MethodPost, Path: hostIssuePath + "/comments", DefaultStatus: http.StatusCreated, Summary: "Post issue comment", Tags: []string{"Issues"}}, s.postIssueCommentOnHost)
	huma.Register(api, huma.Operation{OperationID: "edit-issue-content", Method: http.MethodPatch, Path: issuePath, DefaultStatus: http.StatusOK, Summary: "Edit issue content", Tags: []string{"Issues"}}, s.editIssueContent)
	huma.Register(api, huma.Operation{OperationID: "edit-issue-content-on-host", Method: http.MethodPatch, Path: hostIssuePath, DefaultStatus: http.StatusOK, Summary: "Edit issue content", Tags: []string{"Issues"}}, s.editIssueContentOnHost)
	huma.Register(api, huma.Operation{OperationID: "edit-issue-comment", Method: http.MethodPatch, Path: issuePath + "/comments/{comment_id}", DefaultStatus: http.StatusOK, Summary: "Edit issue comment", Tags: []string{"Issues"}}, s.editIssueComment)
	huma.Register(api, huma.Operation{OperationID: "edit-issue-comment-on-host", Method: http.MethodPatch, Path: hostIssuePath + "/comments/{comment_id}", DefaultStatus: http.StatusOK, Summary: "Edit issue comment", Tags: []string{"Issues"}}, s.editIssueCommentOnHost)
	huma.Register(api, huma.Operation{OperationID: "set-issue-labels", Method: http.MethodPut, Path: issuePath + "/labels", DefaultStatus: http.StatusOK, Summary: "Set issue labels", Tags: []string{"Issues"}}, s.setIssueLabels)
	huma.Register(api, huma.Operation{OperationID: "set-issue-labels-on-host", Method: http.MethodPut, Path: hostIssuePath + "/labels", DefaultStatus: http.StatusOK, Summary: "Set issue labels", Tags: []string{"Issues"}}, s.setIssueLabelsOnHost)

	huma.Post(api, repoPath+"/resolve/{number}", s.resolveItem,
		documentOperation("resolve-repo-item", "Resolve repository item", "Repositories"))
	huma.Post(api, hostRepoPath+"/resolve/{number}", s.resolveItemOnHost,
		documentOperation("resolve-repo-item-on-host", "Resolve repository item", "Repositories"))
	huma.Get(api, repoPath, s.getRepo,
		documentOperation("get-repo", "Get repository", "Repositories"))
	huma.Get(api, hostRepoPath, s.getRepoOnHost,
		documentOperation("get-repo-on-host", "Get repository", "Repositories"))
	huma.Register(api, huma.Operation{OperationID: "list-repo-labels", Method: http.MethodGet, Path: repoPath + "/labels", DefaultStatus: http.StatusOK, Summary: "List repository labels", Tags: []string{"Repositories"}}, s.listRepoLabels)
	huma.Register(api, huma.Operation{OperationID: "list-repo-labels-on-host", Method: http.MethodGet, Path: hostRepoPath + "/labels", DefaultStatus: http.StatusOK, Summary: "List repository labels", Tags: []string{"Repositories"}}, s.listRepoLabelsOnHost)
	huma.Get(api, repoPath+"/comment-autocomplete", s.getCommentAutocomplete,
		documentOperation("get-comment-autocomplete", "Get comment autocomplete", "Repositories"))
	huma.Get(api, hostRepoPath+"/comment-autocomplete", s.getCommentAutocompleteOnHost,
		documentOperation("get-comment-autocomplete-on-host", "Get comment autocomplete", "Repositories"))

	huma.Post(api, pullPath+"/approve", s.approvePR,
		documentOperation("approve-pull", "Approve pull request", "Pull Requests"))
	huma.Post(api, hostPullPath+"/approve", s.approvePROnHost,
		documentOperation("approve-pull-on-host", "Approve pull request", "Pull Requests"))
	huma.Post(api, pullPath+"/approve-workflows", s.approveWorkflows,
		documentOperation("approve-pull-workflows", "Approve pull request workflows", "Pull Requests"))
	huma.Post(api, hostPullPath+"/approve-workflows", s.approveWorkflowsOnHost,
		documentOperation("approve-pull-workflows-on-host", "Approve pull request workflows", "Pull Requests"))
	huma.Post(api, pullPath+"/ready-for-review", s.readyForReview,
		documentOperation("mark-pull-ready-for-review", "Mark pull request ready for review", "Pull Requests"))
	huma.Post(api, hostPullPath+"/ready-for-review", s.readyForReviewOnHost,
		documentOperation("mark-pull-ready-for-review-on-host", "Mark pull request ready for review", "Pull Requests"))
	huma.Post(api, pullPath+"/merge", s.mergePR,
		documentOperation("merge-pull", "Merge pull request", "Pull Requests"))
	huma.Post(api, hostPullPath+"/merge", s.mergePROnHost,
		documentOperation("merge-pull-on-host", "Merge pull request", "Pull Requests"))
	huma.Post(api, pullPath+"/sync", s.syncPR,
		documentOperation("sync-pull", "Sync pull request", "Pull Requests"))
	huma.Post(api, hostPullPath+"/sync", s.syncPROnHost,
		documentOperation("sync-pull-on-host", "Sync pull request", "Pull Requests"))
	huma.Post(api, pullPath+"/ci-refresh", s.syncPRCI,
		documentOperation("refresh-pull-ci", "Refresh pull request CI", "Pull Requests"))
	huma.Post(api, hostPullPath+"/ci-refresh", s.syncPRCIOnHost,
		documentOperation("refresh-pull-ci-on-host", "Refresh pull request CI", "Pull Requests"))
	huma.Register(api, huma.Operation{OperationID: "enqueue-pr-sync", Method: http.MethodPost, Path: pullPath + "/sync/async", DefaultStatus: http.StatusAccepted, Summary: "Enqueue pull request sync", Tags: []string{"Pull Requests"}}, s.enqueuePRSync)
	huma.Register(api, huma.Operation{OperationID: "enqueue-pr-sync-on-host", Method: http.MethodPost, Path: hostPullPath + "/sync/async", DefaultStatus: http.StatusAccepted, Summary: "Enqueue pull request sync", Tags: []string{"Pull Requests"}}, s.enqueuePRSyncOnHost)
	huma.Post(api, issuePath+"/sync", s.syncIssue,
		documentOperation("sync-issue", "Sync issue", "Issues"))
	huma.Post(api, hostIssuePath+"/sync", s.syncIssueOnHost,
		documentOperation("sync-issue-on-host", "Sync issue", "Issues"))
	huma.Register(api, huma.Operation{OperationID: "enqueue-issue-sync", Method: http.MethodPost, Path: issuePath + "/sync/async", DefaultStatus: http.StatusAccepted, Summary: "Enqueue issue sync", Tags: []string{"Issues"}}, s.enqueueIssueSync)
	huma.Register(api, huma.Operation{OperationID: "enqueue-issue-sync-on-host", Method: http.MethodPost, Path: hostIssuePath + "/sync/async", DefaultStatus: http.StatusAccepted, Summary: "Enqueue issue sync", Tags: []string{"Issues"}}, s.enqueueIssueSyncOnHost)
	huma.Register(api, huma.Operation{OperationID: "set-pr-github-state", Method: http.MethodPost, Path: pullPath + "/github-state", DefaultStatus: http.StatusOK, Summary: "Set pull request GitHub state", Tags: []string{"Pull Requests"}}, s.setPRGitHubState)
	huma.Register(api, huma.Operation{OperationID: "set-pr-github-state-on-host", Method: http.MethodPost, Path: hostPullPath + "/github-state", DefaultStatus: http.StatusOK, Summary: "Set pull request GitHub state", Tags: []string{"Pull Requests"}}, s.setPRGitHubStateOnHost)
	huma.Register(api, huma.Operation{OperationID: "set-issue-github-state", Method: http.MethodPost, Path: issuePath + "/github-state", DefaultStatus: http.StatusOK, Summary: "Set issue GitHub state", Tags: []string{"Issues"}}, s.setIssueGitHubState)
	huma.Register(api, huma.Operation{OperationID: "set-issue-github-state-on-host", Method: http.MethodPost, Path: hostIssuePath + "/github-state", DefaultStatus: http.StatusOK, Summary: "Set issue GitHub state", Tags: []string{"Issues"}}, s.setIssueGitHubStateOnHost)

	huma.Get(api, pullPath+"/commits", s.getCommits,
		documentOperation("get-pull-commits", "Get pull request commits", "Pull Requests"))
	huma.Get(api, hostPullPath+"/commits", s.getCommitsOnHost,
		documentOperation("get-pull-commits-on-host", "Get pull request commits", "Pull Requests"))
	huma.Get(api, pullPath+"/diff", s.getDiff,
		documentOperation("get-pull-diff", "Get pull request diff", "Pull Requests"))
	huma.Get(api, hostPullPath+"/diff", s.getDiffOnHost,
		documentOperation("get-pull-diff-on-host", "Get pull request diff", "Pull Requests"))
	huma.Get(api, pullPath+"/files", s.getFiles,
		documentOperation("get-pull-files", "Get pull request files", "Pull Requests"))
	huma.Get(api, hostPullPath+"/files", s.getFilesOnHost,
		documentOperation("get-pull-files-on-host", "Get pull request files", "Pull Requests"))
	huma.Get(api, pullPath+"/file-preview", s.getFilePreview,
		documentOperation("get-pull-file-preview", "Get pull request file preview", "Pull Requests"))
	huma.Get(api, hostPullPath+"/file-preview", s.getFilePreviewOnHost,
		documentOperation("get-pull-file-preview-on-host", "Get pull request file preview", "Pull Requests"))
	huma.Get(api, pullPath+"/stack", s.getStackForPR,
		documentOperation("get-pull-stack", "Get pull request stack", "Pull Requests"))
	huma.Get(api, hostPullPath+"/stack", s.getStackForPROnHost,
		documentOperation("get-pull-stack-on-host", "Get pull request stack", "Pull Requests"))
	huma.Register(api, huma.Operation{OperationID: "create-issue-workspace", Method: http.MethodPost, Path: issuePath + "/workspace", DefaultStatus: http.StatusAccepted, Summary: "Create issue workspace", Tags: []string{"Issues"}}, s.createIssueWorkspace)
	huma.Register(api, huma.Operation{OperationID: "create-issue-workspace-on-host", Method: http.MethodPost, Path: hostIssuePath + "/workspace", DefaultStatus: http.StatusAccepted, Summary: "Create issue workspace", Tags: []string{"Issues"}}, s.createIssueWorkspaceOnHost)
}

func NewOpenAPI() *huma.OpenAPI {
	mux := http.NewServeMux()
	s := &Server{}
	api := humago.NewWithPrefix(mux, "/api/v1", apiConfig("/"))
	s.registerAPI(api)
	return api.OpenAPI()
}

func (s *Server) listPulls(ctx context.Context, input *listPullsInput) (*listPullsOutput, error) {
	if input.State != "" {
		valid := map[string]bool{
			"open": true, "closed": true, "all": true,
		}
		if !valid[input.State] {
			return nil, huma.Error400BadRequest(
				"state must be one of: open, closed, all",
			)
		}
	}

	opts := db.ListMergeRequestsOpts{
		State:       input.State,
		KanbanState: input.Kanban,
		Starred:     input.Starred,
		Search:      input.Q,
		Limit:       input.Limit,
		Offset:      input.Offset,
	}
	if platformHost, owner, name := parseRepoFilter(input.Repo); owner != "" {
		opts.PlatformHost = platformHost
		opts.RepoOwner = owner
		opts.RepoName = name
	}

	mrs, err := s.db.ListMergeRequests(ctx, opts)
	if err != nil {
		return nil, huma.Error500InternalServerError("list pulls failed")
	}

	repoByID, err := s.lookupRepoMap(ctx)
	if err != nil {
		return nil, huma.Error500InternalServerError("repo lookup failed")
	}

	mrIDs := make([]int64, len(mrs))
	for i, mr := range mrs {
		mrIDs[i] = mr.ID
	}
	links, err := s.db.GetWorktreeLinksForMRs(ctx, mrIDs)
	if err != nil {
		return nil, huma.Error500InternalServerError("load worktree links failed")
	}
	linksByMR := indexWorktreeLinksByMR(links)

	out := make([]mergeRequestResponse, 0, len(mrs))
	for _, mr := range mrs {
		rp, ok := repoByID[mr.RepoID]
		if !ok {
			continue
		}
		wl := linksByMR[mr.ID]
		if wl == nil {
			wl = []worktreeLinkResponse{}
		}
		resp := mergeRequestResponse{
			MergeRequest:  mr,
			Repo:          s.repoRefFromRepo(rp),
			RepoOwner:     rp.Owner,
			RepoName:      rp.Name,
			PlatformHost:  rp.PlatformHost,
			WorktreeLinks: wl,
			DetailLoaded:  mr.DetailFetchedAt != nil,
		}
		if mr.DetailFetchedAt != nil {
			resp.DetailFetchedAt = formatUTCRFC3339(*mr.DetailFetchedAt)
		}
		out = append(out, resp)
	}

	return &listPullsOutput{Body: out}, nil
}

func (s *Server) getPull(ctx context.Context, input *repoNumberInput) (*getPullOutput, error) {
	repo, err := s.lookupRepoByProviderRoute(
		ctx, input.Provider, input.PlatformHost, input.Owner, input.Name,
	)
	if err != nil {
		return nil, providerRouteLookupError(err)
	}
	mr, err := s.db.GetMergeRequestByRepoIDAndNumber(ctx, repo.ID, input.Number)
	if err != nil {
		return nil, huma.Error500InternalServerError("get pull request failed")
	}
	if mr == nil {
		return nil, huma.Error404NotFound("pull request not found")
	}

	body, err := s.buildPullDetailResponse(ctx, mr)
	if err != nil {
		return nil, err
	}

	return &getPullOutput{Body: body}, nil
}

func (s *Server) buildPullDetailResponse(
	ctx context.Context,
	mr *db.MergeRequest,
) (mergeRequestDetailResponse, error) {
	events, err := s.db.ListMREvents(ctx, mr.ID)
	if err != nil {
		return mergeRequestDetailResponse{}, huma.Error500InternalServerError("list mr events failed")
	}
	if events == nil {
		events = []db.MREvent{}
	}

	dbLinks, err := s.db.GetWorktreeLinksForMR(ctx, mr.ID)
	if err != nil {
		return mergeRequestDetailResponse{}, huma.Error500InternalServerError(
			"load worktree links failed",
		)
	}

	repo, err := s.db.GetRepoByID(ctx, mr.RepoID)
	if err != nil || repo == nil {
		return mergeRequestDetailResponse{}, huma.Error500InternalServerError(
			"load repo failed",
		)
	}
	resp := mergeRequestDetailResponse{
		MergeRequest:     mr,
		Events:           events,
		Repo:             s.repoRefFromRepo(*repo),
		RepoOwner:        repo.Owner,
		RepoName:         repo.Name,
		PlatformHost:     repo.PlatformHost,
		PlatformHeadSHA:  mr.PlatformHeadSHA,
		PlatformBaseSHA:  mr.PlatformBaseSHA,
		DiffHeadSHA:      mr.DiffHeadSHA,
		MergeBaseSHA:     mr.MergeBaseSHA,
		WorktreeLinks:    toWorktreeLinkResponses(dbLinks),
		WorkflowApproval: s.workflowApprovalState(ctx, repo.Owner, repo.Name, mr),
		Warnings:         s.diffWarnings(mr),
		DetailLoaded:     mr.DetailFetchedAt != nil,
	}
	if mr.DetailFetchedAt != nil {
		resp.DetailFetchedAt = formatUTCRFC3339(*mr.DetailFetchedAt)
	}

	if s.workspaces != nil {
		wsRef, wsErr := s.workspaces.GetByMR(
			ctx, repo.PlatformHost, repo.Owner, repo.Name, mr.Number,
		)
		if wsErr == nil && wsRef != nil {
			resp.Workspace = &workspaceRef{
				ID:     wsRef.ID,
				Status: wsRef.Status,
			}
		}
	}

	return resp, nil
}

// diffWarnings returns warnings inferred from the persisted PR row. The
// resolveItem and syncPR paths log diff sync failures via slog and (in
// syncPR's case) surface them in the immediate response, but neither
// persists the failure. Without inferring from the row state, a client
// that lands on the PR detail page after resolveItem (which has no
// warnings field) or after a refresh would see no indication that the
// diff is unavailable. We therefore emit a sanitized warning whenever a
// PR that should have diff data is missing it.
func (s *Server) diffWarnings(mr *db.MergeRequest) []string {
	if mr == nil {
		return nil
	}
	if !s.syncer.HasDiffSync() {
		return nil
	}
	// Closed (including merged) PRs also get diff SHAs populated via
	// fetchAndUpdateClosed, so the warning logic must cover every state
	// that getDiff would render, not just open and merged.
	if mr.DiffHeadSHA == "" {
		return []string{"Diff data is unavailable for this pull request."}
	}
	shas := db.DiffSHAs{
		PlatformHeadSHA: mr.PlatformHeadSHA,
		PlatformBaseSHA: mr.PlatformBaseSHA,
		DiffHeadSHA:     mr.DiffHeadSHA,
		DiffBaseSHA:     mr.DiffBaseSHA,
		State:           string(mr.State),
	}
	if shas.Stale() {
		return []string{"Diff data is out of date for this pull request."}
	}
	return nil
}

// workflowApprovalState reads the persisted workflow-approval
// snapshot from the merge request row. Sync (SyncMROnProvider) is
// the only writer; this read path makes no live calls so detail
// GETs stay cheap. The snapshot is keyed by head SHA: if the head
// has moved since the snapshot was taken, treat it as unchecked so
// the UI doesn't render an approve-workflows button against a SHA
// that no longer has pending runs.
func (s *Server) workflowApprovalState(
	_ context.Context,
	_, _ string,
	mr *db.MergeRequest,
) workflowApprovalResponse {
	if mr == nil {
		return workflowApprovalResponse{}
	}
	// Closed or merged PRs cannot have pending workflow approvals,
	// regardless of what the persisted snapshot says.
	if mr.State != "open" {
		return workflowApprovalResponse{Checked: true}
	}
	if mr.PlatformHeadSHA == "" {
		return workflowApprovalResponse{}
	}
	if mr.WorkflowApprovalCheckedAt == nil ||
		mr.WorkflowApprovalHeadSHA != mr.PlatformHeadSHA {
		return workflowApprovalResponse{}
	}
	return workflowApprovalResponse{
		Checked:  true,
		Required: mr.WorkflowApprovalRequired,
		Count:    mr.WorkflowApprovalCount,
	}
}

func (s *Server) getMRImportMetadata(
	ctx context.Context, input *repoNumberInput,
) (*getMRImportMetadataOutput, error) {
	repo, err := s.lookupRepoByProviderRoute(
		ctx, input.Provider, input.PlatformHost, input.Owner, input.Name,
	)
	if err != nil {
		return nil, providerRouteLookupError(err)
	}
	mr, err := s.db.GetMergeRequestByRepoIDAndNumber(ctx, repo.ID, input.Number)
	if err != nil {
		return nil, huma.Error500InternalServerError(
			"failed to query merge request",
		)
	}
	if mr == nil {
		return nil, huma.Error404NotFound("merge request not found")
	}
	return &getMRImportMetadataOutput{
		Body: mrImportMetadataResponse{
			Number:           mr.Number,
			HeadBranch:       mr.HeadBranch,
			PlatformHeadSHA:  mr.PlatformHeadSHA,
			HeadRepoCloneURL: mr.HeadRepoCloneURL,
			State:            string(mr.State),
			IsDraft:          mr.IsDraft,
			Title:            mr.Title,
		},
	}, nil
}

func (s *Server) setKanbanState(ctx context.Context, input *setKanbanStateInput) (*statusOnlyOutput, error) {
	if !validKanbanStates[input.Body.Status] {
		return nil, huma.Error400BadRequest("status must be one of: new, reviewing, waiting, awaiting_merge")
	}

	repo, err := s.lookupRepoByProviderRoute(
		ctx, input.Provider, input.PlatformHost, input.Owner, input.Name,
	)
	if err != nil {
		return nil, providerRouteLookupError(err)
	}
	ref := repoNumberPathRef{
		owner:        repo.Owner,
		name:         repo.Name,
		number:       input.Number,
		platformHost: repo.PlatformHost,
	}
	mrID, err := s.lookupMRID(ctx, ref)
	if err != nil {
		return nil, huma.Error404NotFound(err.Error())
	}
	if err := s.db.SetKanbanState(ctx, mrID, input.Body.Status); err != nil {
		return nil, huma.Error500InternalServerError("set kanban state failed")
	}

	return &statusOnlyOutput{Status: http.StatusOK}, nil
}

func (s *Server) editPRContent(
	ctx context.Context, input *editPRContentInput,
) (*editPRContentOutput, error) {
	if input.Body.Title == nil && input.Body.Body == nil {
		return nil, huma.Error400BadRequest(
			"at least one of title or body must be provided",
		)
	}
	if input.Body.Title != nil && strings.TrimSpace(*input.Body.Title) == "" {
		return nil, huma.Error400BadRequest("title must not be blank")
	}

	repo, err := s.requireRepoRouteCapability(
		ctx,
		input.Provider, input.PlatformHost, input.Owner, input.Name,
		capabilityStateMutation,
	)
	if err != nil {
		return nil, err
	}

	mutator, err := s.syncer.MergeRequestContentMutator(
		repoProviderKind(*repo), repoProviderHost(*repo),
	)
	if err != nil {
		return nil, huma.Error404NotFound(err.Error())
	}

	mr, err := s.db.GetMergeRequestByRepoIDAndNumber(ctx, repo.ID, input.Number)
	if err != nil {
		return nil, huma.Error500InternalServerError(
			"get pull request failed",
		)
	}
	if mr == nil {
		return nil, huma.Error404NotFound("pull request not found")
	}

	updatedMR, err := mutator.EditMergeRequestContent(
		ctx, platformRepoRefFromDB(*repo), input.Number, input.Body.Title, input.Body.Body,
	)
	if err != nil {
		return nil, huma.Error502BadGateway(
			"provider API error: " + err.Error(),
		)
	}

	newTitle := mr.Title
	if updatedMR.Title != "" {
		newTitle = updatedMR.Title
	} else if input.Body.Title != nil {
		newTitle = *input.Body.Title
	}
	newBody := mr.Body
	if updatedMR.Body != "" {
		newBody = updatedMR.Body
	} else if input.Body.Body != nil {
		newBody = *input.Body.Body
	}
	updatedAt := s.now().UTC()
	if !updatedMR.UpdatedAt.IsZero() {
		updatedAt = updatedMR.UpdatedAt.UTC()
	}
	if err := s.db.UpdateMRTitleBody(
		ctx, mr.ID, newTitle, newBody, updatedAt,
	); err != nil {
		return nil, huma.Error500InternalServerError(
			"update title/body failed",
		)
	}

	mr, err = s.db.GetMergeRequestByRepoIDAndNumber(ctx, repo.ID, input.Number)
	if err != nil || mr == nil {
		return nil, huma.Error500InternalServerError(
			"re-read pull request failed",
		)
	}

	body, err := s.buildPullDetailResponse(ctx, mr)
	if err != nil {
		return nil, err
	}

	return &editPRContentOutput{Body: body}, nil
}

func (s *Server) editIssueContent(
	ctx context.Context, input *editIssueContentInput,
) (*editIssueContentOutput, error) {
	if input.Body.Title == nil && input.Body.Body == nil {
		return nil, huma.Error400BadRequest(
			"at least one of title or body must be provided",
		)
	}
	if input.Body.Title != nil && strings.TrimSpace(*input.Body.Title) == "" {
		return nil, huma.Error400BadRequest("title must not be blank")
	}

	repo, err := s.requireRepoRouteCapability(
		ctx,
		input.Provider, input.PlatformHost, input.Owner, input.Name,
		capabilityStateMutation,
	)
	if err != nil {
		return nil, err
	}

	mutator, err := s.syncer.IssueContentMutator(
		repoProviderKind(*repo), repoProviderHost(*repo),
	)
	if err != nil {
		return nil, huma.Error404NotFound(err.Error())
	}

	issue, err := s.db.GetIssueByRepoIDAndNumber(ctx, repo.ID, input.Number)
	if err != nil {
		return nil, huma.Error500InternalServerError("get issue failed")
	}
	if issue == nil {
		return nil, huma.Error404NotFound("issue not found")
	}

	updatedIssue, err := mutator.EditIssueContent(
		ctx, platformRepoRefFromDB(*repo), input.Number, input.Body.Title, input.Body.Body,
	)
	if err != nil {
		return nil, huma.Error502BadGateway(
			"provider API error: " + err.Error(),
		)
	}

	newTitle := issue.Title
	if updatedIssue.Title != "" {
		newTitle = updatedIssue.Title
	} else if input.Body.Title != nil {
		newTitle = *input.Body.Title
	}
	newBody := issue.Body
	if updatedIssue.Body != "" {
		newBody = updatedIssue.Body
	} else if input.Body.Body != nil {
		newBody = *input.Body.Body
	}
	updatedAt := s.now().UTC()
	if !updatedIssue.UpdatedAt.IsZero() {
		updatedAt = updatedIssue.UpdatedAt.UTC()
	}
	if err := s.db.UpdateIssueTitleBody(
		ctx, issue.ID, newTitle, newBody, updatedAt,
	); err != nil {
		return nil, huma.Error500InternalServerError(
			"update title/body failed",
		)
	}

	issue, err = s.db.GetIssueByRepoIDAndNumber(ctx, repo.ID, input.Number)
	if err != nil || issue == nil {
		return nil, huma.Error500InternalServerError(
			"re-read issue failed",
		)
	}

	body, err := s.buildIssueDetailResponse(ctx, repo, issue)
	if err != nil {
		return nil, err
	}

	return &editIssueContentOutput{Body: body}, nil
}

func (s *Server) postComment(ctx context.Context, input *postCommentInput) (*postCommentOutput, error) {
	if strings.TrimSpace(input.Body.Body) == "" {
		return nil, huma.Error400BadRequest("comment body must not be empty")
	}

	repo, err := s.requireRepoRouteCapability(
		ctx,
		input.Provider, input.PlatformHost, input.Owner, input.Name,
		capabilityCommentMutation,
	)
	if err != nil {
		return nil, err
	}

	mutator, err := s.syncer.CommentMutator(
		repoProviderKind(*repo), repoProviderHost(*repo),
	)
	if err != nil {
		return nil, huma.Error404NotFound(err.Error())
	}

	platformEvent, err := mutator.CreateMergeRequestComment(
		ctx, platformRepoRefFromDB(*repo), input.Number, input.Body.Body,
	)
	if err != nil {
		return nil, huma.Error502BadGateway("create comment on provider failed")
	}

	ref := repoNumberPathRef{
		owner:        repo.Owner,
		name:         repo.Name,
		number:       input.Number,
		platformHost: repo.PlatformHost,
	}
	mrID, err := s.lookupMRID(ctx, ref)
	if err != nil {
		return nil, huma.Error404NotFound(err.Error())
	}

	event := platform.DBMREvent(mrID, platformEvent)
	if err := s.db.UpsertMREvents(ctx, []db.MREvent{event}); err != nil {
		_ = err
	}

	return &postCommentOutput{Status: http.StatusCreated, Body: event}, nil
}

func (s *Server) editComment(ctx context.Context, input *editCommentInput) (*editCommentOutput, error) {
	if strings.TrimSpace(input.Body.Body) == "" {
		return nil, huma.Error400BadRequest("comment body must not be empty")
	}

	repo, err := s.requireRepoRouteCapability(
		ctx,
		input.Provider, input.PlatformHost, input.Owner, input.Name,
		capabilityCommentMutation,
	)
	if err != nil {
		return nil, err
	}

	mutator, err := s.syncer.CommentMutator(
		repoProviderKind(*repo), repoProviderHost(*repo),
	)
	if err != nil {
		return nil, huma.Error404NotFound(err.Error())
	}

	ref := repoNumberPathRef{
		owner:        repo.Owner,
		name:         repo.Name,
		number:       input.Number,
		platformHost: repo.PlatformHost,
	}
	mrID, err := s.lookupMRID(ctx, ref)
	if err != nil {
		return nil, huma.Error404NotFound(err.Error())
	}

	exists, err := s.db.MRCommentEventExists(ctx, mrID, input.CommentID)
	if err != nil {
		return nil, huma.Error500InternalServerError("validate comment target failed")
	}
	if !exists {
		return nil, huma.Error404NotFound("comment not found for pull request")
	}

	platformEvent, err := mutator.EditMergeRequestComment(
		ctx, platformRepoRefFromDB(*repo), input.Number, input.CommentID, input.Body.Body,
	)
	if err != nil {
		return nil, huma.Error502BadGateway("edit comment on provider failed")
	}
	platformEvent.MergeRequestNumber = input.Number

	event := platform.DBMREvent(mrID, platformEvent)
	if err := s.db.UpsertMREvents(ctx, []db.MREvent{event}); err != nil {
		return nil, huma.Error500InternalServerError("persist edited comment failed")
	}

	return &editCommentOutput{Body: event}, nil
}

func (s *Server) listIssues(ctx context.Context, input *listIssuesInput) (*listIssuesOutput, error) {
	if input.State != "" {
		valid := map[string]bool{
			"open": true, "closed": true, "all": true,
		}
		if !valid[input.State] {
			return nil, huma.Error400BadRequest(
				"state must be one of: open, closed, all",
			)
		}
	}

	opts := db.ListIssuesOpts{
		State:   input.State,
		Search:  input.Q,
		Starred: input.Starred,
		Limit:   input.Limit,
		Offset:  input.Offset,
	}
	if platformHost, owner, name := parseRepoFilter(input.Repo); owner != "" {
		opts.PlatformHost = platformHost
		opts.RepoOwner = owner
		opts.RepoName = name
	}

	issues, err := s.db.ListIssues(ctx, opts)
	if err != nil {
		return nil, huma.Error500InternalServerError("list issues failed")
	}

	repoByID, err := s.lookupRepoMap(ctx)
	if err != nil {
		return nil, huma.Error500InternalServerError("repo lookup failed")
	}

	out := make([]issueResponse, 0, len(issues))
	for _, issue := range issues {
		rp, ok := repoByID[issue.RepoID]
		if !ok {
			continue
		}
		resp := issueResponse{
			Issue:        issue,
			Repo:         s.repoRefFromRepo(rp),
			PlatformHost: rp.PlatformHost,
			RepoOwner:    rp.Owner,
			RepoName:     rp.Name,
			DetailLoaded: issue.DetailFetchedAt != nil,
		}
		if issue.DetailFetchedAt != nil {
			resp.DetailFetchedAt = formatUTCRFC3339(*issue.DetailFetchedAt)
		}
		out = append(out, resp)
	}

	return &listIssuesOutput{Body: out}, nil
}

func (s *Server) createIssue(
	ctx context.Context, input *createIssueInput,
) (*createIssueOutput, error) {
	title := strings.TrimSpace(input.Body.Title)
	if title == "" {
		return nil, huma.Error400BadRequest("issue title must not be empty")
	}

	repo, err := s.lookupRepoByProviderRoute(
		ctx, input.Provider, input.PlatformHost, input.Owner, input.Name,
	)
	if err != nil {
		return nil, providerRouteLookupError(err)
	}
	if !capabilityEnabled(s.capabilitiesForRepo(*repo), capabilityIssueMutation) {
		return nil, unsupportedCapabilityProblem(*repo, capabilityIssueMutation)
	}

	mutator, err := s.syncer.IssueMutator(
		repoProviderKind(*repo), repoProviderHost(*repo),
	)
	if err != nil {
		return nil, huma.Error404NotFound(err.Error())
	}

	platformIssue, err := mutator.CreateIssue(
		ctx, platformRepoRefFromDB(*repo), title, input.Body.Body,
	)
	if err != nil {
		return nil, huma.Error502BadGateway(
			"provider API error: " + err.Error(),
		)
	}

	issue := platform.DBIssue(repo.ID, platformIssue)
	issueID, err := s.db.UpsertIssue(ctx, issue)
	if err != nil {
		return nil, huma.Error500InternalServerError(
			"save issue failed",
		)
	}
	if err := s.db.ReplaceIssueLabels(ctx, repo.ID, issueID, issue.Labels); err != nil {
		return nil, huma.Error500InternalServerError(
			"save issue labels failed",
		)
	}

	savedIssue, err := s.db.GetIssueByRepoIDAndNumber(
		ctx, repo.ID, issue.Number,
	)
	if err != nil || savedIssue == nil {
		return nil, huma.Error500InternalServerError(
			"re-read issue failed",
		)
	}
	savedIssue.ID = issueID

	out := issueResponse{
		Issue:        *savedIssue,
		Repo:         s.repoRefFromRepo(*repo),
		PlatformHost: repo.PlatformHost,
		RepoOwner:    repo.Owner,
		RepoName:     repo.Name,
		DetailLoaded: savedIssue.DetailFetchedAt != nil,
	}
	if savedIssue.DetailFetchedAt != nil {
		out.DetailFetchedAt = formatUTCRFC3339(*savedIssue.DetailFetchedAt)
	}

	return &createIssueOutput{
		Status: http.StatusCreated,
		Body:   out,
	}, nil
}

func (s *Server) getIssue(ctx context.Context, input *issueRepoNumberInput) (*getIssueOutput, error) {
	repo, err := s.lookupRepoByProviderRoute(
		ctx, input.Provider, input.PlatformHost, input.Owner, input.Name,
	)
	if err != nil {
		return nil, providerRouteLookupError(err)
	}
	issue, err := s.db.GetIssueByRepoIDAndNumber(ctx, repo.ID, input.Number)
	if err != nil {
		return nil, huma.Error500InternalServerError("get issue failed")
	}
	if issue == nil {
		return nil, huma.Error404NotFound("issue not found")
	}

	issueResp, err := s.buildIssueDetailResponse(ctx, repo, issue)
	if err != nil {
		return nil, err
	}
	return &getIssueOutput{Body: issueResp}, nil
}

func (s *Server) buildIssueDetailResponse(
	ctx context.Context,
	repo *db.Repo,
	issue *db.Issue,
) (issueDetailResponse, error) {
	events, err := s.db.ListIssueEvents(ctx, issue.ID)
	if err != nil {
		return issueDetailResponse{}, huma.Error500InternalServerError("list issue events failed")
	}
	if events == nil {
		events = []db.IssueEvent{}
	}

	issueResp := issueDetailResponse{
		Issue:        issue,
		Events:       events,
		Repo:         s.repoRefFromRepo(*repo),
		PlatformHost: repo.PlatformHost,
		RepoOwner:    repo.Owner,
		RepoName:     repo.Name,
		DetailLoaded: issue.DetailFetchedAt != nil,
	}
	if issue.DetailFetchedAt != nil {
		issueResp.DetailFetchedAt = formatUTCRFC3339(*issue.DetailFetchedAt)
	}
	if s.workspaces != nil {
		wsRef, wsErr := s.workspaces.GetByIssue(
			ctx, repo.PlatformHost, repo.Owner, repo.Name, issue.Number,
		)
		if wsErr == nil && wsRef != nil {
			issueResp.Workspace = &workspaceRef{
				ID:     wsRef.ID,
				Status: wsRef.Status,
			}
		}
	}
	return issueResp, nil
}

func (s *Server) postIssueComment(ctx context.Context, input *postIssueCommentInput) (*postIssueCommentOutput, error) {
	if strings.TrimSpace(input.Body.Body) == "" {
		return nil, huma.Error400BadRequest("comment body must not be empty")
	}

	repo, err := s.lookupRepoByProviderRoute(
		ctx, input.Provider, input.PlatformHost, input.Owner, input.Name,
	)
	if err != nil {
		return nil, providerRouteLookupError(err)
	}
	if !capabilityEnabled(s.capabilitiesForRepo(*repo), capabilityCommentMutation) {
		return nil, unsupportedCapabilityProblem(*repo, capabilityCommentMutation)
	}

	mutator, err := s.syncer.CommentMutator(
		repoProviderKind(*repo), repoProviderHost(*repo),
	)
	if err != nil {
		return nil, huma.Error404NotFound(err.Error())
	}

	platformEvent, err := mutator.CreateIssueComment(
		ctx, platformRepoRefFromDB(*repo), input.Number, input.Body.Body,
	)
	if err != nil {
		return nil, huma.Error502BadGateway("create comment on provider failed")
	}

	ref := repoNumberPathRef{
		owner:        input.Owner,
		name:         input.Name,
		number:       input.Number,
		platformHost: repo.PlatformHost,
	}
	issueID, err := s.lookupIssueID(ctx, ref)
	if err != nil {
		return nil, huma.Error404NotFound(err.Error())
	}

	event := platform.DBIssueEvent(issueID, platformEvent)
	if err := s.db.UpsertIssueEvents(ctx, []db.IssueEvent{event}); err != nil {
		_ = err
	}

	return &postIssueCommentOutput{Status: http.StatusCreated, Body: event}, nil
}

func (s *Server) editIssueComment(ctx context.Context, input *editIssueCommentInput) (*editIssueCommentOutput, error) {
	if strings.TrimSpace(input.Body.Body) == "" {
		return nil, huma.Error400BadRequest("comment body must not be empty")
	}

	repo, err := s.lookupRepoByProviderRoute(
		ctx, input.Provider, input.PlatformHost, input.Owner, input.Name,
	)
	if err != nil {
		return nil, providerRouteLookupError(err)
	}
	if !capabilityEnabled(s.capabilitiesForRepo(*repo), capabilityCommentMutation) {
		return nil, unsupportedCapabilityProblem(*repo, capabilityCommentMutation)
	}

	mutator, err := s.syncer.CommentMutator(
		repoProviderKind(*repo), repoProviderHost(*repo),
	)
	if err != nil {
		return nil, huma.Error404NotFound(err.Error())
	}

	ref := repoNumberPathRef{
		owner:        input.Owner,
		name:         input.Name,
		number:       input.Number,
		platformHost: repo.PlatformHost,
	}
	issueID, err := s.lookupIssueID(ctx, ref)
	if err != nil {
		return nil, huma.Error404NotFound(err.Error())
	}

	exists, err := s.db.IssueCommentEventExists(ctx, issueID, input.CommentID)
	if err != nil {
		return nil, huma.Error500InternalServerError("validate comment target failed")
	}
	if !exists {
		return nil, huma.Error404NotFound("comment not found for issue")
	}

	platformEvent, err := mutator.EditIssueComment(
		ctx, platformRepoRefFromDB(*repo), input.Number, input.CommentID, input.Body.Body,
	)
	if err != nil {
		return nil, huma.Error502BadGateway("edit comment on provider failed")
	}
	platformEvent.IssueNumber = input.Number

	event := platform.DBIssueEvent(issueID, platformEvent)
	if err := s.db.UpsertIssueEvents(ctx, []db.IssueEvent{event}); err != nil {
		return nil, huma.Error500InternalServerError("persist edited comment failed")
	}

	return &editIssueCommentOutput{Body: event}, nil
}

func (s *Server) setStarred(ctx context.Context, input *starredInput) (*statusOnlyOutput, error) {
	repoID, err := s.lookupStarredRepoID(ctx, input.Body)
	if err != nil {
		return nil, err
	}
	if err := s.db.SetStarred(ctx, input.Body.ItemType, repoID, input.Body.Number); err != nil {
		return nil, huma.Error500InternalServerError("set starred failed")
	}
	return &statusOnlyOutput{Status: http.StatusOK}, nil
}

func (s *Server) unsetStarred(ctx context.Context, input *starredInput) (*statusOnlyOutput, error) {
	repoID, err := s.lookupStarredRepoID(ctx, input.Body)
	if err != nil {
		return nil, err
	}
	if err := s.db.UnsetStarred(ctx, input.Body.ItemType, repoID, input.Body.Number); err != nil {
		return nil, huma.Error500InternalServerError("unset starred failed")
	}
	return &statusOnlyOutput{Status: http.StatusOK}, nil
}

func (s *Server) getRepo(ctx context.Context, input *getRepoInput) (*getRepoOutput, error) {
	repo, err := s.lookupRepoByProviderRoute(
		ctx, input.Provider, input.PlatformHost, input.Owner, input.Name,
	)
	if err != nil {
		return nil, providerRouteLookupError(err)
	}
	return &getRepoOutput{Body: s.repoResponse(*repo)}, nil
}

func (s *Server) getCommentAutocomplete(
	ctx context.Context,
	input *commentAutocompleteInput,
) (*commentAutocompleteOutput, error) {
	repo, err := s.lookupRepoByProviderRoute(
		ctx, input.Provider, input.PlatformHost, input.Owner, input.Name,
	)
	if err != nil {
		return nil, providerRouteLookupError(err)
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
			repo.PlatformHost,
			input.Owner,
			input.Name,
			input.Q,
			limit,
		)
		if err != nil {
			return nil, huma.Error500InternalServerError("list comment autocomplete users failed")
		}
		return &commentAutocompleteOutput{Body: commentAutocompleteResponse{Users: users}}, nil
	case "#":
		references, err := s.db.ListCommentAutocompleteReferences(
			ctx,
			repo.PlatformHost,
			input.Owner,
			input.Name,
			input.Q,
			limit,
		)
		if err != nil {
			return nil, huma.Error500InternalServerError("list comment autocomplete references failed")
		}
		return &commentAutocompleteOutput{Body: commentAutocompleteResponse{References: references}}, nil
	default:
		return nil, huma.Error400BadRequest("trigger must be @ or #")
	}
}

func (s *Server) approvePR(ctx context.Context, input *approvePRInput) (*actionStatusOutput, error) {
	repo, err := s.requireRepoRouteCapability(
		ctx,
		input.Provider, input.PlatformHost, input.Owner, input.Name,
		capabilityReviewMutation,
	)
	if err != nil {
		return nil, err
	}

	mutator, err := s.syncer.ReviewMutator(
		repoProviderKind(*repo), repoProviderHost(*repo),
	)
	if err != nil {
		return nil, huma.Error404NotFound(err.Error())
	}

	platformEvent, err := mutator.ApproveMergeRequest(
		ctx, platformRepoRefFromDB(*repo), input.Number, input.Body.Body,
	)
	if err != nil {
		return nil, huma.Error502BadGateway("provider API error")
	}

	ref := repoNumberPathRef{
		owner:        repo.Owner,
		name:         repo.Name,
		number:       input.Number,
		platformHost: repo.PlatformHost,
	}
	mrID, lookupErr := s.lookupMRID(ctx, ref)
	if lookupErr == nil {
		event := platform.DBMREvent(mrID, platformEvent)
		_ = s.db.UpsertMREvents(ctx, []db.MREvent{event})
	}

	return &actionStatusOutput{Body: actionStatusBody{Status: "approved"}}, nil
}

func (s *Server) approveWorkflows(ctx context.Context, input *repoNumberInput) (*actionStatusOutput, error) {
	repo, err := s.requireRepoRouteCapability(
		ctx,
		input.Provider, input.PlatformHost, input.Owner, input.Name,
		capabilityWorkflowApproval,
	)
	if err != nil {
		return nil, err
	}

	mr, err := s.db.GetMergeRequestByRepoIDAndNumber(ctx, repo.ID, input.Number)
	if err != nil {
		return nil, huma.Error500InternalServerError("get pull request failed")
	}
	if mr == nil {
		return nil, huma.Error404NotFound("pull request not found")
	}

	client, err := s.syncer.ClientForHost(repo.PlatformHost)
	if err != nil {
		return nil, huma.Error404NotFound(err.Error())
	}
	mutator, err := s.syncer.WorkflowApprovalMutator(
		repoProviderKind(*repo), repoProviderHost(*repo),
	)
	if err != nil {
		return nil, huma.Error404NotFound(err.Error())
	}

	pr, err := client.GetPullRequest(ctx, input.Owner, input.Name, input.Number)
	if err != nil {
		return nil, huma.Error502BadGateway("GitHub API error")
	}
	if pr == nil {
		return nil, huma.Error404NotFound("pull request not found")
	}

	headSHA := pr.GetHead().GetSHA()
	if pr.GetState() != "open" || headSHA == "" {
		return &actionStatusOutput{Body: actionStatusBody{Status: "approved_workflows"}}, nil
	}

	runs, err := client.ListWorkflowRunsForHeadSHA(ctx, input.Owner, input.Name, headSHA)
	if err != nil {
		return nil, huma.Error502BadGateway("GitHub API error")
	}
	pending := ghclient.FilterWorkflowRunsAwaitingApproval(runs, ghclient.PRSource{
		Number:           input.Number,
		HeadSHA:          headSHA,
		HeadRepoFullName: pr.GetHead().GetRepo().GetFullName(),
		HeadRef:          pr.GetHead().GetRef(),
	})

	approvedCount := 0
	for _, run := range pending {
		if err := mutator.ApproveWorkflow(
			ctx, platformRepoRefFromDB(*repo), strconv.FormatInt(run.GetID(), 10),
		); err != nil {
			if approvedCount > 0 {
				if syncErr := s.syncer.SyncMROnProvider(
					context.WithoutCancel(ctx),
					repoProviderKind(*repo), repoProviderHost(*repo),
					repo.Owner, repo.Name, input.Number,
				); syncErr != nil {
					slog.Warn("sync after workflow approval failure", "err", syncErr)
				}
			}
			return nil, huma.Error502BadGateway(err.Error())
		}
		approvedCount++
	}

	if syncErr := s.syncer.SyncMROnProvider(
		context.WithoutCancel(ctx),
		repoProviderKind(*repo), repoProviderHost(*repo),
		repo.Owner, repo.Name, input.Number,
	); syncErr != nil {
		slog.Warn("sync after workflow approval", "err", syncErr)
	}
	if err := s.db.UpdateMRWorkflowApproval(
		ctx, repo.ID, input.Number, s.now().UTC(), headSHA, false, 0,
	); err != nil {
		slog.Warn("clear workflow approval state after approval",
			"repo", repo.Owner+"/"+repo.Name,
			"number", input.Number,
			"err", err,
		)
	}

	return &actionStatusOutput{Body: actionStatusBody{
		Status:        "approved_workflows",
		ApprovedCount: approvedCount,
	}}, nil
}

func (s *Server) readyForReview(ctx context.Context, input *repoNumberInput) (*actionStatusOutput, error) {
	repo, err := s.requireRepoRouteCapability(
		ctx,
		input.Provider, input.PlatformHost, input.Owner, input.Name,
		capabilityReadyForReview,
	)
	if err != nil {
		return nil, err
	}

	mutator, err := s.syncer.ReadyForReviewMutator(
		repoProviderKind(*repo), repoProviderHost(*repo),
	)
	if err != nil {
		return nil, huma.Error404NotFound(err.Error())
	}

	pr, err := mutator.MarkReadyForReview(ctx, platformRepoRefFromDB(*repo), input.Number)
	if err != nil {
		type readyForReviewFailure interface {
			StatusCode() int
			IsStaleState() bool
		}

		var readyErr readyForReviewFailure
		var ghErr *gh.ErrorResponse
		staleState := errors.As(err, &readyErr) && readyErr != nil && readyErr.IsStaleState()
		if !staleState {
			staleState = errors.As(err, &ghErr) && ghErr != nil && ghErr.Response != nil && ghErr.Response.StatusCode == http.StatusNotFound
		}
		if staleState {
			if syncErr := s.syncer.SyncMROnProvider(
				context.WithoutCancel(ctx),
				repoProviderKind(*repo), repoProviderHost(*repo),
				repo.Owner, repo.Name, input.Number,
			); syncErr != nil {
				slog.Warn(
					"sync after ready for review stale state failed",
					"owner", input.Owner,
					"repo", input.Name,
					"number", input.Number,
					"err", syncErr,
				)
			} else {
				return &actionStatusOutput{Body: actionStatusBody{Status: "ready_for_review"}}, nil
			}
		}
		slog.Warn(
			"ready for review failed",
			"owner", input.Owner,
			"repo", input.Name,
			"number", input.Number,
			"err", err,
		)
		return nil, huma.Error502BadGateway(err.Error())
	}
	if pr.Number == 0 {
		return nil, huma.Error502BadGateway("provider API returned no pull request")
	}

	if repo != nil {
		normalized := platform.DBMergeRequest(repo.ID, pr)
		if mrID, upsertErr := s.db.UpsertMergeRequest(ctx, normalized); upsertErr == nil {
			_ = s.db.EnsureKanbanState(ctx, mrID)
		}
	}

	return &actionStatusOutput{Body: actionStatusBody{Status: "ready_for_review"}}, nil
}

func (s *Server) mergePR(ctx context.Context, input *mergePRInput) (*mergePROutput, error) {
	validMethods := map[string]bool{"merge": true, "squash": true, "rebase": true}
	if !validMethods[input.Body.Method] {
		return nil, huma.Error400BadRequest("invalid merge method: must be merge, squash, or rebase")
	}

	repo, err := s.requireRepoRouteCapability(
		ctx,
		input.Provider, input.PlatformHost, input.Owner, input.Name,
		capabilityMergeMutation,
	)
	if err != nil {
		return nil, err
	}

	mutator, err := s.syncer.MergeMutator(
		repoProviderKind(*repo), repoProviderHost(*repo),
	)
	if err != nil {
		return nil, huma.Error404NotFound(err.Error())
	}

	result, err := mutator.MergeMergeRequest(
		ctx,
		platformRepoRefFromDB(*repo),
		input.Number,
		input.Body.CommitTitle,
		input.Body.CommitMessage,
		input.Body.Method,
	)
	if err != nil {
		if status, message, ok := mergeHTTPErrorStatus(err); ok {
			slog.Error("provider merge failed",
				"owner", input.Owner, "repo", input.Name,
				"number", input.Number, "method", input.Body.Method,
				"status", status,
				"message", message,
				"err", err)

			if status == http.StatusMethodNotAllowed || status == http.StatusConflict {
				s.runBackground(func(bgCtx context.Context) {
					if syncErr := s.syncer.SyncMROnProvider(
						bgCtx,
						repoProviderKind(*repo), repoProviderHost(*repo),
						repo.Owner, repo.Name, input.Number,
					); syncErr != nil {
						slog.Warn("background sync after merge failure", "err", syncErr)
					}
				})
				return nil, huma.Error409Conflict(message)
			}

			// Forward 4xx provider errors as-is so the user sees the real cause
			// (e.g. 422 validation, 403 forbidden). 5xx becomes 502.
			if status >= 400 && status < 500 {
				return nil, huma.NewError(status, message)
			}
			return nil, huma.Error502BadGateway("provider merge error: " + message)
		}
		slog.Error("provider merge transport error",
			"owner", input.Owner, "repo", input.Name,
			"number", input.Number, "method", input.Body.Method,
			"err", err)
		return nil, huma.Error502BadGateway("provider merge error: " + err.Error())
	}

	now := s.now().UTC()
	_ = s.db.UpdateMRState(ctx, repo.ID, input.Number, "merged", &now, &now)

	return &mergePROutput{
		Body: mergePRBody{
			Merged:  result.Merged,
			SHA:     result.SHA,
			Message: result.Message,
		},
	}, nil
}

func mergeHTTPErrorStatus(err error) (int, string, bool) {
	var ghErr *gh.ErrorResponse
	if errors.As(err, &ghErr) && ghErr != nil && ghErr.Response != nil {
		return ghErr.Response.StatusCode, githubErrorResponseMessage(err, ghErr), true
	}
	var httpErr *gitealike.HTTPError
	if errors.As(err, &httpErr) && httpErr != nil && httpErr.StatusCode != 0 {
		return httpErr.StatusCode, httpErr.Error(), true
	}
	return 0, "", false
}

func githubErrorResponseMessage(err error, ghErr *gh.ErrorResponse) string {
	message := strings.TrimSpace(ghErr.Message)
	details := make([]string, 0, len(ghErr.Errors))
	seen := make(map[string]bool, len(ghErr.Errors)+1)
	if message != "" {
		seen[message] = true
	}
	for _, apiErr := range ghErr.Errors {
		detail := strings.TrimSpace(apiErr.Message)
		if detail == "" && strings.TrimSpace(apiErr.Code) != "" {
			detail = strings.TrimSpace(apiErr.Error())
		}
		if detail == "" || seen[detail] {
			continue
		}
		seen[detail] = true
		details = append(details, detail)
	}

	if len(details) > 0 {
		joined := strings.Join(details, "; ")
		if message == "" || isGenericGitHubErrorMessage(message, ghErr.Response.StatusCode) {
			return joined
		}
		return message + ": " + joined
	}
	if message != "" {
		return message
	}
	if err != nil {
		return err.Error()
	}
	return "GitHub API error"
}

func isGenericGitHubErrorMessage(message string, status int) bool {
	normalized := strings.ToLower(strings.TrimSpace(message))
	return normalized == "github server error" ||
		normalized == "server error" ||
		normalized == strings.ToLower(http.StatusText(status))
}

func (s *Server) setPRGitHubState(
	ctx context.Context, input *githubStateInput,
) (*githubStateOutput, error) {
	if input.Body.State != "open" && input.Body.State != "closed" {
		return nil, huma.Error400BadRequest(
			"state must be 'open' or 'closed'",
		)
	}

	repo, err := s.requireRepoRouteCapability(
		ctx,
		input.Provider, input.PlatformHost, input.Owner, input.Name,
		capabilityStateMutation,
	)
	if err != nil {
		return nil, err
	}

	mutator, err := s.syncer.StateMutator(
		repoProviderKind(*repo), repoProviderHost(*repo),
	)
	if err != nil {
		return nil, huma.Error404NotFound(err.Error())
	}

	mr, err := s.db.GetMergeRequestByRepoIDAndNumber(ctx, repo.ID, input.Number)
	if err != nil {
		return nil, huma.Error500InternalServerError(
			"get pull request: " + err.Error(),
		)
	}
	if mr == nil {
		return nil, huma.Error404NotFound("pull request not found")
	}
	if mr.State == "merged" {
		return nil, huma.Error409Conflict(
			"cannot change state of a merged pull request",
		)
	}

	if _, err := mutator.SetMergeRequestState(
		ctx, platformRepoRefFromDB(*repo), input.Number, input.Body.State,
	); err != nil {
		var ghErr *gh.ErrorResponse
		if errors.As(err, &ghErr) && ghErr != nil && ghErr.Response != nil &&
			ghErr.Response.StatusCode == http.StatusUnprocessableEntity {
			// Re-fetch to sync local state and determine the real cause.
			repoID := repo.ID
			{
				client, clientErr := s.syncer.ClientForHost(repo.PlatformHost)
				if clientErr != nil {
					return nil, huma.Error404NotFound(clientErr.Error())
				}
				ghPR, fetchErr := client.GetPullRequest(
					ctx, input.Owner, input.Name, input.Number,
				)
				if fetchErr == nil {
					if ghPR == nil {
						return nil, huma.Error502BadGateway("GitHub API returned no pull request")
					}
					normalized, normalizeErr := ghclient.NormalizePR(repoID, ghPR)
					if normalizeErr != nil {
						return nil, huma.Error502BadGateway("GitHub API error: " + normalizeErr.Error())
					}
					_, _ = s.db.UpsertMergeRequest(ctx, normalized)
					if ghPR.GetMerged() {
						return nil, huma.Error409Conflict(
							"cannot change state of a merged pull request",
						)
					}
					// Already in requested state (concurrent edit).
					if ghPR.GetState() == input.Body.State {
						out := &githubStateOutput{}
						out.Body.State = input.Body.State
						return out, nil
					}
				}
			}
		}
		return nil, huma.Error502BadGateway(
			"GitHub API error: " + err.Error(),
		)
	}

	repoID := repo.ID

	var closedAt *time.Time
	if input.Body.State == "closed" {
		now := s.now().UTC()
		closedAt = &now
	}
	if err := s.db.UpdateMRState(
		ctx, repoID, input.Number,
		input.Body.State, nil, closedAt,
	); err != nil {
		return nil, huma.Error500InternalServerError(
			"update mr state: " + err.Error(),
		)
	}

	out := &githubStateOutput{}
	out.Body.State = input.Body.State
	return out, nil
}

func (s *Server) setIssueGitHubState(
	ctx context.Context, input *githubStateInput,
) (*githubStateOutput, error) {
	if input.Body.State != "open" && input.Body.State != "closed" {
		return nil, huma.Error400BadRequest(
			"state must be 'open' or 'closed'",
		)
	}

	repo, err := s.requireRepoRouteCapability(
		ctx,
		input.Provider, input.PlatformHost, input.Owner, input.Name,
		capabilityStateMutation,
	)
	if err != nil {
		return nil, err
	}
	issue, err := s.db.GetIssueByRepoIDAndNumber(ctx, repo.ID, input.Number)
	if err != nil {
		return nil, huma.Error500InternalServerError("get issue: " + err.Error())
	}
	if issue == nil {
		return nil, huma.Error404NotFound("issue not found")
	}

	mutator, err := s.syncer.StateMutator(
		repoProviderKind(*repo), repoProviderHost(*repo),
	)
	if err != nil {
		return nil, huma.Error404NotFound(err.Error())
	}

	if _, err := mutator.SetIssueState(
		ctx, platformRepoRefFromDB(*repo), input.Number, input.Body.State,
	); err != nil {
		var ghErr *gh.ErrorResponse
		if errors.As(err, &ghErr) && ghErr != nil && ghErr.Response != nil &&
			ghErr.Response.StatusCode == http.StatusUnprocessableEntity {
			// Re-fetch to sync local state. If already in the
			// requested state (concurrent edit), treat as success.
			client, clientErr := s.syncer.ClientForHost(repo.PlatformHost)
			if clientErr != nil {
				return nil, huma.Error404NotFound(clientErr.Error())
			}
			ghIssue, fetchErr := client.GetIssue(
				ctx, input.Owner, input.Name, input.Number,
			)
			if fetchErr == nil {
				if ghIssue == nil {
					return nil, huma.Error502BadGateway("GitHub API returned no issue")
				}
				normalized, normalizeErr := ghclient.NormalizeIssue(
					repo.ID, ghIssue,
				)
				if normalizeErr != nil {
					return nil, huma.Error502BadGateway("GitHub API error: " + normalizeErr.Error())
				}
				_, _ = s.db.UpsertIssue(ctx, normalized)
				if ghIssue.GetState() == input.Body.State {
					out := &githubStateOutput{}
					out.Body.State = input.Body.State
					return out, nil
				}
			}
		}
		return nil, huma.Error502BadGateway(
			"GitHub API error: " + err.Error(),
		)
	}

	var closedAt *time.Time
	if input.Body.State == "closed" {
		now := s.now().UTC()
		closedAt = &now
	}
	if err := s.db.UpdateIssueState(
		ctx, repo.ID, issue.Number,
		input.Body.State, closedAt,
	); err != nil {
		return nil, huma.Error500InternalServerError(
			"update issue state: " + err.Error(),
		)
	}

	out := &githubStateOutput{}
	out.Body.State = input.Body.State
	return out, nil
}

func (s *Server) listRepos(ctx context.Context, _ *struct{}) (*listReposOutput, error) {
	repos, err := s.db.ListRepos(ctx)
	if err != nil {
		return nil, huma.Error500InternalServerError("list repos failed")
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
		return nil, huma.Error500InternalServerError(
			"list repo summaries failed",
		)
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

func (s *Server) triggerSync(ctx context.Context, _ *struct{}) (*acceptedOutput, error) {
	s.syncer.TriggerRun(context.WithoutCancel(ctx))
	return &acceptedOutput{Status: http.StatusAccepted}, nil
}

func (s *Server) syncStatus(_ context.Context, _ *struct{}) (*syncStatusOutput, error) {
	return &syncStatusOutput{Body: s.syncer.Status()}, nil
}

func (s *Server) getRateLimits(
	_ context.Context, _ *struct{},
) (*rateLimitsOutput, error) {
	trackers := s.syncer.RateTrackers()
	gqlTrackers := s.syncer.GQLRateTrackers()
	budgets := s.syncer.Budgets()
	hosts := make(map[string]rateLimitHostStatus, len(trackers))
	for key, rt := range trackers {
		resetStr := ""
		if resetAt := rt.ResetAt(); resetAt != nil {
			resetStr = formatUTCRFC3339(*resetAt)
		}
		status := rateLimitHostStatus{
			Provider:           rt.Provider(),
			PlatformHost:       rt.PlatformHost(),
			RequestsHour:       rt.RequestsThisHour(),
			RateRemaining:      rt.Remaining(),
			RateLimit:          rt.RateLimit(),
			RateResetAt:        resetStr,
			HourStart:          formatUTCRFC3339(rt.HourStart()),
			SyncThrottleFactor: rt.ThrottleFactor(),
			SyncPaused:         rt.IsPaused(),
			ReserveBuffer:      ghclient.RateReserveBuffer,
			Known:              rt.Known(),
			GQLRemaining:       -1,
			GQLLimit:           -1,
		}
		if gqlRT := gqlTrackers[key]; gqlRT != nil {
			status.GQLRemaining = gqlRT.Remaining()
			status.GQLLimit = gqlRT.RateLimit()
			status.GQLKnown = gqlRT.Known()
			if resetAt := gqlRT.ResetAt(); resetAt != nil {
				status.GQLResetAt = resetAt.UTC().Format(time.RFC3339)
			}
		}
		if b := budgets[key]; b != nil {
			status.BudgetLimit = b.Limit()
			status.BudgetSpent = b.Spent()
			status.BudgetRemaining = b.Remaining()
		}
		hosts[key] = status
	}
	return &rateLimitsOutput{
		Body: rateLimitsResponse{Hosts: hosts},
	}, nil
}

func (s *Server) syncPRCI(ctx context.Context, input *repoNumberInput) (*syncPRCIOutput, error) {
	repo, err := s.lookupRepoByProviderRoute(
		ctx, input.Provider, input.PlatformHost, input.Owner, input.Name,
	)
	if err != nil {
		return nil, providerRouteLookupError(err)
	}

	mr, err := s.db.GetMergeRequestByRepoIDAndNumber(ctx, repo.ID, input.Number)
	if err != nil {
		return nil, huma.Error500InternalServerError("get pull request: " + err.Error())
	}
	if mr == nil {
		return nil, huma.Error404NotFound("pull request not found")
	}
	warnings, err := s.syncer.RefreshMRCIStatusOnProvider(
		ctx,
		ghclient.RepoRef{
			Platform:           repoProviderKind(*repo),
			Owner:              repo.Owner,
			Name:               repo.Name,
			PlatformHost:       repoProviderHost(*repo),
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
		return nil, huma.Error502BadGateway("refresh PR CI: " + err.Error())
	}

	mr, err = s.db.GetMergeRequestByRepoIDAndNumber(ctx, repo.ID, input.Number)
	if err != nil {
		return nil, huma.Error500InternalServerError("get pull request: " + err.Error())
	}
	if mr == nil {
		return nil, huma.Error404NotFound("pull request not found after CI refresh")
	}
	body, err := s.buildPullDetailResponse(ctx, mr)
	if err != nil {
		return nil, err
	}
	body.Warnings = append(body.Warnings, warnings...)
	return &syncPRCIOutput{Body: body}, nil
}

func (s *Server) syncPR(ctx context.Context, input *repoNumberInput) (*syncPROutput, error) {
	repo, err := s.lookupRepoByProviderRoute(
		ctx, input.Provider, input.PlatformHost, input.Owner, input.Name,
	)
	if err != nil {
		return nil, providerRouteLookupError(err)
	}
	// SyncMR distinguishes a non-fatal diff failure from a hard sync failure
	// via DiffSyncError. The PR row, timeline, and CI status are all current
	// in either case, so degrade gracefully: keep the response, but report
	// the diff problem as a warning so the UI can explain why the diff view
	// is stale or empty.
	var diffErr *ghclient.DiffSyncError
	syncErr := s.syncer.SyncMROnProvider(
		ctx, repoProviderKind(*repo), repoProviderHost(*repo),
		repo.Owner, repo.Name, input.Number,
	)
	if syncErr != nil && !errors.As(syncErr, &diffErr) {
		if strings.Contains(syncErr.Error(), "is not tracked") {
			return nil, huma.Error403Forbidden(syncErr.Error())
		}
		return nil, huma.Error502BadGateway("sync PR: " + syncErr.Error())
	}

	mr, err := s.db.GetMergeRequestByRepoIDAndNumber(ctx, repo.ID, input.Number)
	if err != nil {
		return nil, huma.Error500InternalServerError("get pull request: " + err.Error())
	}
	if mr == nil {
		return nil, huma.Error404NotFound("pull request not found after sync")
	}

	body, err := s.buildPullDetailResponse(ctx, mr)
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
	repo, err := s.lookupRepoByProviderRoute(
		ctx, input.Provider, input.PlatformHost, input.Owner, input.Name,
	)
	if err != nil {
		return nil, providerRouteLookupError(err)
	}
	kind := repoProviderKind(*repo)
	host := repoProviderHost(*repo)
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
	repo, err := s.lookupRepoByProviderRoute(
		ctx, input.Provider, input.PlatformHost, input.Owner, input.Name,
	)
	if err != nil {
		return nil, providerRouteLookupError(err)
	}
	err = s.syncer.SyncIssueOnProvider(
		ctx, repoProviderKind(*repo), repoProviderHost(*repo),
		repo.Owner, repo.Name, input.Number,
	)
	if err != nil {
		if strings.Contains(err.Error(), "is not tracked") {
			return nil, huma.Error403Forbidden(err.Error())
		}
		return nil, huma.Error502BadGateway("sync issue: " + err.Error())
	}

	issue, err := s.db.GetIssueByRepoIDAndNumber(ctx, repo.ID, input.Number)
	if err != nil {
		return nil, huma.Error500InternalServerError("get issue: " + err.Error())
	}
	if issue == nil {
		return nil, huma.Error404NotFound("issue not found after sync")
	}

	events, err := s.db.ListIssueEvents(ctx, issue.ID)
	if err != nil {
		return nil, huma.Error500InternalServerError("list issue events: " + err.Error())
	}
	if events == nil {
		events = []db.IssueEvent{}
	}

	syncIssueResp := issueDetailResponse{
		Issue:        issue,
		Events:       events,
		Repo:         s.repoRefFromRepo(*repo),
		PlatformHost: repo.PlatformHost,
		RepoOwner:    repo.Owner,
		RepoName:     repo.Name,
		DetailLoaded: issue.DetailFetchedAt != nil,
	}
	if issue.DetailFetchedAt != nil {
		syncIssueResp.DetailFetchedAt = formatUTCRFC3339(*issue.DetailFetchedAt)
	}
	if s.workspaces != nil {
		wsRef, wsErr := s.workspaces.GetByIssue(
			ctx, repo.PlatformHost, repo.Owner, repo.Name, issue.Number,
		)
		if wsErr == nil && wsRef != nil {
			syncIssueResp.Workspace = &workspaceRef{
				ID:     wsRef.ID,
				Status: wsRef.Status,
			}
		}
	}
	return &syncIssueOutput{Body: syncIssueResp}, nil
}

func (s *Server) enqueueIssueSync(ctx context.Context, input *issueRepoNumberInput) (*acceptedOutput, error) {
	repo, err := s.lookupRepoByProviderRoute(
		ctx, input.Provider, input.PlatformHost, input.Owner, input.Name,
	)
	if err != nil {
		return nil, providerRouteLookupError(err)
	}
	kind := repoProviderKind(*repo)
	host := repoProviderHost(*repo)
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
	opts := db.ListActivityOpts{
		Repo:   input.Repo,
		Types:  input.Types,
		Search: input.Search,
	}

	opts.Limit = activitySafetyCap + 1

	if input.Since != "" {
		t, err := time.Parse(time.RFC3339, input.Since)
		if err != nil {
			return nil, huma.Error400BadRequest("invalid since: " + err.Error())
		}
		opts.Since = &t
	} else {
		defaultSince := s.now().UTC().AddDate(0, 0, -7)
		opts.Since = &defaultSince
	}

	if input.After != "" {
		t, source, sourceID, err := db.DecodeCursor(input.After)
		if err != nil {
			return nil, huma.Error400BadRequest("invalid after cursor: " + err.Error())
		}
		opts.AfterTime = &t
		opts.AfterSource = source
		opts.AfterSourceID = sourceID
	}

	items, err := s.db.ListActivity(ctx, opts)
	if err != nil {
		slog.Error("list activity failed", "err", err)
		return nil, huma.Error500InternalServerError("list activity failed")
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

	out := make([]activityItemResponse, len(items))
	for i, it := range items {
		out[i] = activityItemResponse{
			ID:           it.Source + ":" + strconv.FormatInt(it.SourceID, 10),
			Cursor:       db.EncodeCursor(it.CreatedAt, it.Source, it.SourceID),
			ActivityType: it.ActivityType,
			Repo: s.repoRefFromParts(
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
			Author:       it.Author,
			CreatedAt:    formatUTCRFC3339(it.CreatedAt),
			BodyPreview:  it.BodyPreview,
		}
	}

	return &listActivityOutput{
		Body: activityResponse{Items: out, Capped: capped},
	}, nil
}

func (s *Server) resolveItem(
	ctx context.Context, input *repoNumberInput,
) (*resolveItemOutput, error) {
	number := input.Number
	repo, err := s.lookupRepoByProviderRoute(
		ctx, input.Provider, input.PlatformHost, input.Owner, input.Name,
	)
	if errors.Is(err, errRepoNotFound) {
		return &resolveItemOutput{
			Body: resolveItemResponse{
				Number:      number,
				RepoTracked: false,
			},
		}, nil
	}
	if err != nil {
		return nil, providerRouteLookupError(err)
	}
	if !s.syncer.IsTrackedRepoOnHost(repo.Owner, repo.Name, repoProviderHost(*repo)) {
		return &resolveItemOutput{
			Body: resolveItemResponse{
				Number:      number,
				RepoTracked: false,
			},
		}, nil
	}
	itemType, found, err := s.db.ResolveItemNumber(ctx, repo.ID, number)
	if err != nil {
		return nil, huma.Error500InternalServerError(
			"resolve item: " + err.Error(),
		)
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

	if repoProviderKind(*repo) != platform.KindGitHub {
		return nil, huma.Error404NotFound("item not found")
	}

	itemType, err = s.syncer.SyncItemByNumber(
		ctx, repo.Owner, repo.Name, number,
	)
	// A DiffSyncError means the PR row was upserted but the diff
	// computation failed. Resolution doesn't need diff data, so treat
	// the result as success here. The resolve response has no warnings
	// field, so the staleness reaches the client when they navigate to
	// the PR detail page: getPull infers the warning from the persisted
	// row state via diffWarnings.
	var diffErr *ghclient.DiffSyncError
	if err != nil && !errors.As(err, &diffErr) {
		var ghErr *gh.ErrorResponse
		if errors.As(err, &ghErr) {
			if ghErr.Response != nil &&
				ghErr.Response.StatusCode == 404 {
				return nil, huma.Error404NotFound(
					"item not found: " + err.Error(),
				)
			}
			return nil, huma.Error502BadGateway(
				"GitHub API error: " + err.Error(),
			)
		}
		return nil, huma.Error500InternalServerError(
			"resolve item: " + err.Error(),
		)
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
		return 0, huma.Error400BadRequest("item_type must be 'pr' or 'issue'")
	}

	var (
		repoID int64
		err    error
	)
	if body.PlatformHost != "" {
		repoID, err = s.lookupRepoIDOnHost(
			ctx, body.Owner, body.Name, body.PlatformHost,
		)
	} else {
		repoID, err = s.lookupRepoID(ctx, body.Owner, body.Name)
	}
	if err != nil {
		if errors.Is(err, errRepoNotFound) {
			return 0, huma.Error404NotFound(err.Error())
		}
		return 0, huma.Error500InternalServerError("repo lookup failed")
	}

	return repoID, nil
}

// --- Commits ---

type getCommitsOutput = bodyOutput[commitsResponse]

func (s *Server) getCommits(ctx context.Context, input *repoNumberInput) (*getCommitsOutput, error) {
	if s.clones == nil {
		return nil, huma.Error503ServiceUnavailable("commits not available: clone manager not configured")
	}

	repo, err := s.lookupRepoByProviderRoute(
		ctx, input.Provider, input.PlatformHost, input.Owner, input.Name,
	)
	if err != nil {
		return nil, providerRouteLookupError(err)
	}
	shas, err := s.db.GetDiffSHAsByRepoID(ctx, repo.ID, input.Number)
	if err != nil {
		return nil, huma.Error500InternalServerError("failed to look up PR")
	}
	if shas == nil {
		return nil, huma.Error404NotFound("pull request not found")
	}
	if shas.DiffHeadSHA == "" || shas.MergeBaseSHA == "" {
		return nil, huma.Error404NotFound("commits not available for this pull request")
	}

	host := repoProviderHost(*repo)
	commits, err := s.clones.ListCommits(ctx, host, repo.Owner, repo.Name, shas.MergeBaseSHA, shas.DiffHeadSHA)
	if err != nil {
		if errors.Is(err, gitclone.ErrNotFound) {
			return nil, huma.Error404NotFound("commits not available: referenced commit not found")
		}
		return nil, huma.Error502BadGateway("failed to list commits: " + err.Error())
	}

	resp := commitsResponse{Commits: make([]commitResponse, len(commits))}
	for i, c := range commits {
		resp.Commits[i] = commitResponse{
			SHA:        c.SHA,
			Message:    c.Message,
			AuthorName: c.AuthorName,
			AuthoredAt: c.AuthoredAt.UTC(),
		}
	}
	return &getCommitsOutput{Body: resp}, nil
}

// --- Diff ---

type getDiffInput struct {
	Provider     string `path:"provider"`
	PlatformHost string
	Owner        string `path:"owner"`
	Name         string `path:"name"`
	Number       int    `path:"number"`
	Whitespace   string `query:"whitespace"`
	Commit       string `query:"commit" doc:"Scope to a single commit SHA"`
	From         string `query:"from"   doc:"Start SHA for range diff (inclusive)"`
	To           string `query:"to"     doc:"End SHA for range diff (inclusive)"`
}

type getDiffOutput = bodyOutput[diffResponse]

type resolvedDiffRange struct {
	host     string
	owner    string
	name     string
	fromSHA  string
	toSHA    string
	diffSHAs *db.DiffSHAs
}

func (s *Server) resolveDiffRange(
	ctx context.Context,
	input *getDiffInput,
) (*resolvedDiffRange, error) {
	repo, err := s.lookupRepoByProviderRoute(
		ctx, input.Provider, input.PlatformHost, input.Owner, input.Name,
	)
	if err != nil {
		return nil, providerRouteLookupError(err)
	}
	shas, err := s.db.GetDiffSHAsByRepoID(ctx, repo.ID, input.Number)
	if err != nil {
		return nil, huma.Error500InternalServerError("failed to look up PR")
	}
	if shas == nil {
		return nil, huma.Error404NotFound("pull request not found")
	}
	if shas.DiffHeadSHA == "" || shas.MergeBaseSHA == "" {
		return nil, huma.Error404NotFound("diff not available for this pull request")
	}

	host := repoProviderHost(*repo)
	diffFrom := shas.MergeBaseSHA
	diffTo := shas.DiffHeadSHA

	hasCommit := input.Commit != ""
	hasFrom := input.From != ""
	hasTo := input.To != ""

	switch {
	case !hasCommit && !hasFrom && !hasTo:
		// Default: full PR diff. diffFrom/diffTo already set.

	case hasCommit && !hasFrom && !hasTo:
		if _, err := s.validateSHAs(ctx, host, input, shas, input.Commit); err != nil {
			return nil, err
		}
		parent, err := s.clones.ParentOf(ctx, host, repo.Owner, repo.Name, input.Commit)
		if err != nil {
			return nil, huma.Error500InternalServerError("failed to resolve parent: " + err.Error())
		}
		diffFrom = parent
		diffTo = input.Commit

	case !hasCommit && hasFrom && hasTo:
		indexMap, err := s.validateSHAs(ctx, host, input, shas, input.From, input.To)
		if err != nil {
			return nil, err
		}
		// In newest-first order, "from" (older) must have a higher index than "to" (newer).
		if indexMap[input.From] <= indexMap[input.To] {
			return nil, huma.Error400BadRequest("invalid range: 'from' must be older than 'to'")
		}
		parent, err := s.clones.ParentOf(ctx, host, repo.Owner, repo.Name, input.From)
		if err != nil {
			return nil, huma.Error500InternalServerError("failed to resolve parent: " + err.Error())
		}
		diffFrom = parent
		diffTo = input.To

	default:
		return nil, huma.Error400BadRequest("invalid scope: use 'commit' alone or 'from'+'to' together")
	}

	return &resolvedDiffRange{
		host:     host,
		owner:    repo.Owner,
		name:     repo.Name,
		fromSHA:  diffFrom,
		toSHA:    diffTo,
		diffSHAs: shas,
	}, nil
}

func (s *Server) getDiff(ctx context.Context, input *getDiffInput) (*getDiffOutput, error) {
	if s.clones == nil {
		return nil, huma.Error503ServiceUnavailable("diff view not available: clone manager not configured")
	}

	resolved, err := s.resolveDiffRange(ctx, input)
	if err != nil {
		return nil, err
	}

	hideWhitespace := input.Whitespace == "hide"
	result, err := s.clones.Diff(ctx, resolved.host, resolved.owner, resolved.name, resolved.fromSHA, resolved.toSHA, hideWhitespace)
	if err != nil {
		if errors.Is(err, gitclone.ErrNotFound) {
			return nil, huma.Error404NotFound("diff not available: referenced commit not found")
		}
		slog.Error("failed to compute diff", "owner", input.Owner, "name", input.Name, "number", input.Number, "err", err)
		return nil, huma.Error502BadGateway("failed to compute diff")
	}

	result.Stale = resolved.diffSHAs.Stale()

	return &getDiffOutput{Body: diffResponse{
		Stale:               result.Stale,
		WhitespaceOnlyCount: result.WhitespaceOnlyCount,
		Files:               result.Files,
	}}, nil
}

// --- File preview ---

const maxFilePreviewBytes int64 = 4 * 1024 * 1024

type getFilePreviewInput struct {
	Provider     string `path:"provider"`
	PlatformHost string
	Owner        string `path:"owner"`
	Name         string `path:"name"`
	Number       int    `path:"number"`
	Path         string `query:"path" doc:"Changed file path to preview"`
	Commit       string `query:"commit" doc:"Scope to a single commit SHA"`
	From         string `query:"from"   doc:"Start SHA for range diff (inclusive)"`
	To           string `query:"to"     doc:"End SHA for range diff (inclusive)"`
}

type getFilePreviewOutput = bodyOutput[filePreviewResponse]

func (s *Server) getFilePreview(ctx context.Context, input *getFilePreviewInput) (*getFilePreviewOutput, error) {
	if s.clones == nil {
		return nil, huma.Error503ServiceUnavailable("file preview not available: clone manager not configured")
	}
	if strings.TrimSpace(input.Path) == "" {
		return nil, huma.Error400BadRequest("path is required")
	}

	resolved, err := s.resolveDiffRange(ctx, &getDiffInput{
		Provider:     input.Provider,
		PlatformHost: input.PlatformHost,
		Owner:        input.Owner,
		Name:         input.Name,
		Number:       input.Number,
		Commit:       input.Commit,
		From:         input.From,
		To:           input.To,
	})
	if err != nil {
		return nil, err
	}

	previewRef := resolved.toSHA
	previewPath := input.Path
	files, err := s.clones.DiffFiles(
		ctx,
		resolved.host,
		resolved.owner,
		resolved.name,
		resolved.fromSHA,
		resolved.toSHA,
	)
	if err != nil {
		if errors.Is(err, gitclone.ErrNotFound) {
			return nil, huma.Error404NotFound("file preview not available: referenced commit not found")
		}
		slog.Error("failed to validate preview path", "owner", input.Owner, "name", input.Name, "number", input.Number, "path", input.Path, "err", err)
		return nil, huma.Error502BadGateway("failed to validate file preview")
	}
	found := false
	for _, file := range files {
		if file.Path != input.Path {
			continue
		}
		found = true
		if file.Status == "deleted" {
			previewRef = resolved.fromSHA
			previewPath = file.OldPath
			if previewPath == "" {
				previewPath = file.Path
			}
		}
		break
	}
	if !found {
		return nil, huma.Error404NotFound("file preview not available: file is not changed in this diff")
	}

	content, err := s.clones.FileContent(
		ctx,
		resolved.host,
		resolved.owner,
		resolved.name,
		previewRef,
		previewPath,
		maxFilePreviewBytes,
	)
	if err != nil {
		if errors.Is(err, gitclone.ErrNotFound) {
			return nil, huma.Error404NotFound("file preview not available: referenced file not found")
		}
		if errors.Is(err, gitclone.ErrTooLarge) {
			return nil, huma.Error413RequestEntityTooLarge("file preview is too large")
		}
		slog.Error("failed to read file preview", "owner", input.Owner, "name", input.Name, "number", input.Number, "path", input.Path, "err", err)
		return nil, huma.Error502BadGateway("failed to read file preview")
	}

	return &getFilePreviewOutput{Body: filePreviewResponse{
		Path:      content.Path,
		MediaType: previewMediaType(content.Path, content.Data),
		Encoding:  "base64",
		Content:   base64.StdEncoding.EncodeToString(content.Data),
		Size:      content.Size,
	}}, nil
}

func previewMediaType(path string, data []byte) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".md", ".markdown", ".mdown", ".mkd":
		return "text/markdown; charset=utf-8"
	case ".json":
		return "application/json; charset=utf-8"
	case ".jsonc":
		return "application/jsonc; charset=utf-8"
	case ".toml":
		return "application/toml; charset=utf-8"
	case ".yaml", ".yml":
		return "application/yaml; charset=utf-8"
	case ".svg":
		return "image/svg+xml"
	}
	if mediaType := mime.TypeByExtension(filepath.Ext(path)); mediaType != "" {
		return mediaType
	}
	return http.DetectContentType(data)
}

// --- Files (lightweight) ---

type getFilesInput struct {
	Provider     string `path:"provider"`
	PlatformHost string
	Owner        string `path:"owner"`
	Name         string `path:"name"`
	Number       int    `path:"number"`
}

type getFilesOutput = bodyOutput[filesResponse]

func (s *Server) getFiles(ctx context.Context, input *getFilesInput) (*getFilesOutput, error) {
	if s.clones == nil {
		return nil, huma.Error503ServiceUnavailable("files view not available: clone manager not configured")
	}

	repo, err := s.lookupRepoByProviderRoute(
		ctx, input.Provider, input.PlatformHost, input.Owner, input.Name,
	)
	if err != nil {
		return nil, providerRouteLookupError(err)
	}
	shas, err := s.db.GetDiffSHAsByRepoID(ctx, repo.ID, input.Number)
	if err != nil {
		return nil, huma.Error500InternalServerError("failed to look up PR")
	}
	if shas == nil {
		return nil, huma.Error404NotFound("pull request not found")
	}
	if shas.DiffHeadSHA == "" || shas.MergeBaseSHA == "" {
		return nil, huma.Error404NotFound("file list not available for this pull request")
	}

	host := repoProviderHost(*repo)
	files, err := s.clones.DiffFiles(ctx, host, repo.Owner, repo.Name, shas.MergeBaseSHA, shas.DiffHeadSHA)
	if err != nil {
		if errors.Is(err, gitclone.ErrNotFound) {
			return nil, huma.Error404NotFound("file list not available: referenced commit not found")
		}
		slog.Error("failed to list files", "owner", input.Owner, "name", input.Name, "number", input.Number, "err", err)
		return nil, huma.Error502BadGateway("failed to list files")
	}

	return &getFilesOutput{Body: filesResponse{
		Stale: shas.Stale(),
		Files: files,
	}}, nil
}

// validateSHAs checks that all provided SHAs are in the PR's first-parent commit list.
// Returns a SHA -> index map (newest-first order) so callers can check range ordering.
func (s *Server) validateSHAs(
	ctx context.Context,
	host string,
	input *getDiffInput,
	shas *db.DiffSHAs,
	userSHAs ...string,
) (map[string]int, error) {
	commits, err := s.clones.ListCommits(ctx, host, input.Owner, input.Name, shas.MergeBaseSHA, shas.DiffHeadSHA)
	if err != nil {
		return nil, huma.Error500InternalServerError("failed to list commits for validation: " + err.Error())
	}
	indexMap := make(map[string]int, len(commits))
	for i, c := range commits {
		indexMap[c.SHA] = i
	}
	for _, sha := range userSHAs {
		if _, ok := indexMap[sha]; !ok {
			return nil, huma.Error400BadRequest("sha not in pull request: " + sha)
		}
	}
	return indexMap, nil
}

// --- Stacks ---

func (s *Server) listStacks(ctx context.Context, input *listStacksInput) (*listStacksOutput, error) {
	if input.Repo != "" {
		if strings.Count(input.Repo, "/") != 1 {
			return nil, huma.Error400BadRequest("invalid repo filter: expected owner/name")
		}
		owner, name, _ := strings.Cut(input.Repo, "/")
		if owner == "" || name == "" {
			return nil, huma.Error400BadRequest("invalid repo filter: expected owner/name")
		}
	}
	stackList, memberMap, err := s.db.ListStacksWithMembers(ctx, input.Repo)
	if err != nil {
		return nil, huma.Error500InternalServerError("list stacks failed")
	}

	out := make([]stackResponse, 0, len(stackList))
	for _, st := range stackList {
		members := memberMap[st.ID]
		out = append(out, stackResponse{
			ID:        st.ID,
			Name:      st.Name,
			RepoOwner: st.RepoOwner,
			RepoName:  st.RepoName,
			Health:    computeStackHealth(members),
			Members:   toStackMemberResponses(members),
		})
	}

	return &listStacksOutput{Body: out}, nil
}

func (s *Server) getStackForPR(ctx context.Context, input *repoNumberInput) (*getStackForPROutput, error) {
	repo, err := s.lookupRepoByProviderRoute(
		ctx, input.Provider, input.PlatformHost, input.Owner, input.Name,
	)
	if err != nil {
		return nil, providerRouteLookupError(err)
	}
	stack, members, err := s.db.GetStackForPRByRepoID(ctx, repo.ID, input.Number)
	if err != nil {
		return nil, huma.Error500InternalServerError("get stack for pr failed")
	}
	if stack == nil {
		return nil, huma.Error404NotFound("PR is not part of a stack")
	}

	var position int
	for _, m := range members {
		if m.Number == input.Number {
			position = m.Position
			break
		}
	}

	return &getStackForPROutput{
		Body: stackContextResponse{
			StackID:   stack.ID,
			StackName: stack.Name,
			Position:  position,
			Size:      len(members),
			Health:    computeStackHealth(members),
			Members:   toStackMemberResponses(members),
		},
	}, nil
}

// --- Workspaces ---

// createWorkspace creates or reuses a PR-backed middleman workspace.
//
// This API exists so a tracked pull request can have a durable local execution
// context that middleman owns and can reopen later. It is not a generic
// worktree-creation endpoint for arbitrary branches.
func (s *Server) createWorkspace(
	ctx context.Context, input *createWorkspaceInput,
) (*createWorkspaceOutput, error) {
	if s.workspaces == nil {
		return nil, huma.Error503ServiceUnavailable(
			"workspace manager not configured",
		)
	}

	ws, err := s.workspaces.Create(
		ctx,
		input.Body.PlatformHost,
		input.Body.Owner,
		input.Body.Name,
		input.Body.MRNumber,
	)
	if err != nil {
		if errors.Is(err, workspace.ErrWorkspaceNotFound) {
			return nil, huma.Error404NotFound(err.Error())
		}
		if errors.Is(err, workspace.ErrWorkspaceNotSynced) {
			return nil, huma.Error404NotFound(err.Error())
		}
		if errors.Is(err, workspace.ErrWorkspaceDuplicate) {
			return nil, huma.Error409Conflict(
				"workspace already exists for this MR")
		}
		return nil, huma.Error500InternalServerError(
			"create workspace: " + err.Error(),
		)
	}

	s.runWorkspaceSetup(ws)

	summary, err := s.workspaces.GetSummary(ctx, ws.ID)
	if err != nil {
		return nil, huma.Error500InternalServerError(
			"get workspace summary: " + err.Error(),
		)
	}
	if summary == nil {
		return nil, huma.Error500InternalServerError(
			"workspace summary missing after create",
		)
	}
	return &createWorkspaceOutput{
		Status: http.StatusAccepted,
		Body:   s.toWorkspaceResponse(ctx, summary),
	}, nil
}

func (s *Server) runWorkspaceSetup(ws *workspace.Workspace) {
	s.runBackground(func(bgCtx context.Context) {
		for {
			setupErr := s.workspaces.Setup(bgCtx, ws)
			summary, getErr := s.workspaces.GetSummary(
				bgCtx, ws.ID,
			)
			if getErr != nil {
				slog.Warn("get workspace summary after setup",
					"id", ws.ID, "err", getErr,
				)
				return
			}
			if summary == nil {
				return
			}
			resp := s.toWorkspaceResponse(bgCtx, summary)
			if setupErr != nil {
				slog.Warn("workspace setup failed",
					"id", ws.ID, "err", setupErr,
				)
			}
			s.hub.Broadcast(Event{
				Type: "workspace_status",
				Data: resp,
			})

			next, queued, queueErr := s.workspaces.StartQueuedRetryIfErrored(
				bgCtx, ws.ID,
			)
			if queueErr != nil {
				slog.Warn("start queued workspace retry",
					"id", ws.ID, "err", queueErr,
				)
				summary, getErr = s.workspaces.GetSummary(bgCtx, ws.ID)
				if getErr != nil {
					slog.Warn("get workspace summary after queued retry failure",
						"id", ws.ID, "err", getErr,
					)
					return
				}
				if summary != nil {
					s.hub.Broadcast(Event{
						Type: "workspace_status",
						Data: s.toWorkspaceResponse(bgCtx, summary),
					})
				}
				return
			}
			if !queued {
				return
			}
			if next == nil {
				return
			}
			ws = next
			summary, getErr = s.workspaces.GetSummary(bgCtx, ws.ID)
			if getErr != nil {
				slog.Warn("get workspace summary after queued retry",
					"id", ws.ID, "err", getErr,
				)
				return
			}
			if summary == nil {
				return
			}
			s.hub.Broadcast(Event{
				Type: "workspace_status",
				Data: s.toWorkspaceResponse(bgCtx, summary),
			})
		}
	})
}

// createIssueWorkspace creates or reuses an issue-backed middleman workspace.
//
// This API exists so an issue can have its own durable local execution context
// even when there is no PR branch yet. These workspaces start from the repo's
// current origin/HEAD and are presented in the UI with issue-specific sidebar
// behavior instead of PR/reviews affordances.
func (s *Server) createIssueWorkspace(
	ctx context.Context, input *createIssueWorkspaceInput,
) (*createWorkspaceOutput, error) {
	if s.workspaces == nil {
		return nil, huma.Error503ServiceUnavailable(
			"workspace manager not configured",
		)
	}
	repo, err := s.lookupRepoByProviderRoute(
		ctx, input.Provider, input.PlatformHost, input.Owner, input.Name,
	)
	if err != nil {
		return nil, providerRouteLookupError(err)
	}

	existing, err := s.workspaces.GetByIssue(
		ctx,
		repo.PlatformHost,
		repo.Owner,
		repo.Name,
		input.Number,
	)
	if err != nil {
		return nil, huma.Error500InternalServerError(
			"lookup existing issue workspace: " + err.Error(),
		)
	}
	if existing != nil {
		summary, getErr := s.workspaces.GetSummary(ctx, existing.ID)
		if getErr != nil {
			return nil, huma.Error500InternalServerError(
				"get workspace summary: " + getErr.Error(),
			)
		}
		if summary == nil {
			return nil, huma.Error500InternalServerError(
				"workspace summary missing for existing workspace",
			)
		}
		return &createWorkspaceOutput{
			Status: http.StatusAccepted,
			Body:   s.toWorkspaceResponse(ctx, summary),
		}, nil
	}

	ws, err := s.workspaces.CreateIssue(
		ctx,
		repo.PlatformHost,
		repo.Owner,
		repo.Name,
		input.Number,
		workspace.CreateIssueOptions{
			GitHeadRef:          strings.TrimSpace(derefString(input.Body.GitHeadRef)),
			ReuseExistingBranch: input.Body.ReuseExistingBranch,
		},
	)
	if err != nil {
		msg := err.Error()
		var branchConflict *workspace.IssueWorkspaceBranchConflictError
		if errors.As(err, &branchConflict) {
			conflict := &huma.ErrorModel{
				Type:   issueWorkspaceBranchConflictType,
				Title:  "Issue workspace branch conflict",
				Status: http.StatusConflict,
				Detail: "A local branch with the requested name already exists.",
				Errors: []*huma.ErrorDetail{
					{
						Message:  "Requested branch already exists",
						Location: "body.git_head_ref",
						Value:    branchConflict.Branch,
					},
					{
						Message:  "Suggested alternative branch name",
						Location: "body.suggested_git_head_ref",
						Value:    branchConflict.SuggestedBranch,
					},
				},
			}
			return nil, conflict
		}
		if strings.Contains(msg, "not tracked") {
			return nil, huma.Error404NotFound(msg)
		}
		if strings.Contains(msg, "not synced") {
			return nil, huma.Error404NotFound(msg)
		}
		if strings.Contains(msg, "invalid branch name") {
			return nil, huma.Error400BadRequest(msg)
		}
		if strings.Contains(msg, "UNIQUE constraint") {
			existing, getErr := s.workspaces.GetByIssue(
				ctx,
				repo.PlatformHost,
				repo.Owner,
				repo.Name,
				input.Number,
			)
			if getErr == nil && existing != nil {
				summary, summaryErr := s.workspaces.GetSummary(ctx, existing.ID)
				if summaryErr == nil && summary != nil {
					return &createWorkspaceOutput{
						Status: http.StatusAccepted,
						Body:   s.toWorkspaceResponse(ctx, summary),
					}, nil
				}
			}
		}
		return nil, huma.Error500InternalServerError(
			"create issue workspace: " + msg,
		)
	}

	s.runWorkspaceSetup(ws)

	summary, err := s.workspaces.GetSummary(ctx, ws.ID)
	if err != nil {
		return nil, huma.Error500InternalServerError(
			"get workspace summary: " + err.Error(),
		)
	}
	if summary == nil {
		return nil, huma.Error500InternalServerError(
			"workspace summary missing after create",
		)
	}

	return &createWorkspaceOutput{
		Status: http.StatusAccepted,
		Body:   s.toWorkspaceResponse(ctx, summary),
	}, nil
}

// listWorkspaces returns middleman's persisted workspace records.
//
// Its purpose is to drive the workspaces page and terminal picker from
// middleman's own database model, rather than from ad hoc discovery of host
// worktrees.
func (s *Server) listWorkspaces(
	ctx context.Context, _ *struct{},
) (*listWorkspacesOutput, error) {
	if s.workspaces == nil {
		out := &listWorkspacesOutput{}
		out.Body.Workspaces = []workspaceResponse{}
		return out, nil
	}

	if err := s.workspaces.PruneMissingTmuxSessions(ctx); err != nil {
		slog.Debug("prune missing tmux sessions", "err", err)
	}

	summaries, err := s.workspaces.ListSummaries(ctx)
	if err != nil {
		return nil, huma.Error500InternalServerError(
			"list workspaces failed",
		)
	}

	list := make([]workspaceResponse, len(summaries))
	if len(summaries) == 1 {
		list[0] = s.toWorkspaceResponse(ctx, &summaries[0])
	} else {
		workers := min(len(summaries), tmuxProbeMaxConcurrency)
		jobs := make(chan int)
		var wg sync.WaitGroup
		wg.Add(workers)
		for range workers {
			go func() {
				defer wg.Done()
				for i := range jobs {
					list[i] = s.toWorkspaceResponse(ctx, &summaries[i])
				}
			}()
		}
		for i := range summaries {
			jobs <- i
		}
		close(jobs)
		wg.Wait()
	}

	out := &listWorkspacesOutput{}
	out.Body.Workspaces = list
	return out, nil
}

func derefString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

// getWorkspace returns one persisted middleman workspace.
//
// The terminal view uses this to reopen an existing local execution context and
// determine whether the workspace is PR-backed or issue-backed.
func (s *Server) getWorkspace(
	ctx context.Context, input *getWorkspaceInput,
) (*getWorkspaceOutput, error) {
	if s.workspaces == nil {
		return nil, huma.Error503ServiceUnavailable(
			"workspace manager not configured",
		)
	}

	summary, err := s.workspaces.GetSummary(ctx, input.ID)
	if err != nil {
		return nil, huma.Error500InternalServerError(
			"get workspace failed",
		)
	}
	if summary == nil {
		return nil, huma.Error404NotFound("workspace not found")
	}

	return &getWorkspaceOutput{
		Body: s.toWorkspaceResponse(ctx, summary),
	}, nil
}

func (s *Server) getWorkspaceCommits(
	ctx context.Context, input *getWorkspaceCommitsInput,
) (*getWorkspaceCommitsOutput, error) {
	req, err := s.workspaceDiffRequest(ctx, input.ID, "")
	if err != nil {
		return nil, err
	}

	commits, ok, err := s.workspaceCommits(ctx, req)
	if err != nil {
		slog.Error(
			"failed to list workspace commits",
			"workspace_id", input.ID,
			"err", err,
		)
		return nil, huma.Error502BadGateway("failed to list workspace commits")
	}
	if !ok {
		return nil, huma.Error404NotFound(
			"commits not available for this workspace",
		)
	}

	resp := commitsResponse{Commits: make([]commitResponse, len(commits))}
	for i, c := range commits {
		resp.Commits[i] = commitResponse{
			SHA:        c.SHA,
			Message:    c.Message,
			AuthorName: c.AuthorName,
			AuthoredAt: c.AuthoredAt.UTC(),
		}
	}
	return &getWorkspaceCommitsOutput{Body: resp}, nil
}

func (s *Server) getWorkspaceFiles(
	ctx context.Context, input *getWorkspaceFilesInput,
) (*getWorkspaceFilesOutput, error) {
	req, err := s.workspaceDiffRequest(ctx, input.ID, input.Base)
	if err != nil {
		return nil, err
	}
	if err := s.applyWorkspaceDiffScope(
		ctx, &req, input.Commit, input.From, input.To,
	); err != nil {
		return nil, err
	}

	hideWhitespace := input.Whitespace == "hide"
	files, ok, diffErr := s.workspaceDiffFiles(
		ctx, req, hideWhitespace,
	)
	if diffErr != nil {
		slog.Error(
			"failed to list workspace diff files",
			"workspace_id", input.ID,
			"base", req.Base,
			"err", diffErr,
		)
		return nil, huma.Error502BadGateway(
			"failed to list workspace files",
		)
	}
	if !ok {
		return nil, workspaceDiffBaseUnavailable(req.Base)
	}
	whitespaceOnlyCount, countOK, countErr := s.workspaceDiffWhitespaceOnlyCount(
		ctx, req,
	)
	if countErr != nil {
		slog.Warn(
			"failed to count workspace whitespace-only diff files",
			"workspace_id", input.ID,
			"base", req.Base,
			"err", countErr,
		)
	}
	if !countOK {
		return nil, workspaceDiffBaseUnavailable(req.Base)
	}
	return &getWorkspaceFilesOutput{Body: filesResponse{
		Stale:               false,
		WhitespaceOnlyCount: whitespaceOnlyCount,
		Files:               files,
	}}, nil
}

func (s *Server) getWorkspaceDiff(
	ctx context.Context, input *getWorkspaceDiffInput,
) (*getWorkspaceDiffOutput, error) {
	req, err := s.workspaceDiffRequest(ctx, input.ID, input.Base)
	if err != nil {
		return nil, err
	}
	if err := s.applyWorkspaceDiffScope(
		ctx, &req, input.Commit, input.From, input.To,
	); err != nil {
		return nil, err
	}

	hideWhitespace := input.Whitespace == "hide"
	result, ok, diffErr := s.workspaceDiff(
		ctx, req, hideWhitespace, input.Path,
	)
	if diffErr != nil {
		slog.Error(
			"failed to compute workspace diff",
			"workspace_id", input.ID,
			"base", req.Base,
			"err", diffErr,
		)
		return nil, huma.Error502BadGateway(
			"failed to compute workspace diff",
		)
	}
	if !ok {
		return nil, workspaceDiffBaseUnavailable(req.Base)
	}
	return &getWorkspaceDiffOutput{Body: diffResponse{
		Stale:               false,
		WhitespaceOnlyCount: result.WhitespaceOnlyCount,
		Files:               result.Files,
	}}, nil
}

func (s *Server) workspaceDiffRequest(
	ctx context.Context,
	id string,
	baseInput string,
) (workspaceDiffRequest, error) {
	if s.workspaces == nil {
		return workspaceDiffRequest{}, huma.Error503ServiceUnavailable(
			"workspace manager not configured",
		)
	}

	summary, err := s.workspaces.GetSummary(ctx, id)
	if err != nil {
		return workspaceDiffRequest{}, huma.Error500InternalServerError(
			"get workspace failed",
		)
	}
	if summary == nil {
		return workspaceDiffRequest{}, huma.Error404NotFound("workspace not found")
	}
	if summary.Status != "ready" {
		return workspaceDiffRequest{}, huma.Error409Conflict(
			"workspace is not ready",
		)
	}

	base := workspace.WorktreeDiffBase(baseInput)
	if base == "" {
		base = workspace.WorktreeDiffBaseHead
	}
	switch base {
	case workspace.WorktreeDiffBaseHead, workspace.WorktreeDiffBasePushed:
		return workspaceDiffRequest{Summary: summary, Base: base}, nil
	case workspace.WorktreeDiffBaseMergeTarget:
		targetBranch, ok, err := s.workspaceMergeTargetBranch(ctx, summary)
		if err != nil {
			return workspaceDiffRequest{}, err
		}
		if !ok {
			return workspaceDiffRequest{}, workspaceDiffBaseUnavailable(base)
		}
		return workspaceDiffRequest{
			Summary:           summary,
			Base:              base,
			MergeTargetBranch: targetBranch,
		}, nil
	default:
		return workspaceDiffRequest{}, huma.Error400BadRequest(
			"base must be head, pushed, or merge-target",
		)
	}
}

func (s *Server) workspaceCommits(
	ctx context.Context,
	req workspaceDiffRequest,
) ([]gitclone.Commit, bool, error) {
	targetBranch, ok, err := s.workspaceMergeTargetBranch(ctx, req.Summary)
	if err != nil || !ok {
		return nil, ok, err
	}
	return workspace.WorktreeCommitsAgainstMergeTarget(
		ctx,
		req.Summary.WorktreePath,
		targetBranch,
	)
}

func (s *Server) applyWorkspaceDiffScope(
	ctx context.Context,
	req *workspaceDiffRequest,
	commit string,
	from string,
	to string,
) error {
	hasCommit := commit != ""
	hasFrom := from != ""
	hasTo := to != ""

	switch {
	case !hasCommit && !hasFrom && !hasTo:
		return nil

	case hasCommit && !hasFrom && !hasTo:
		if _, err := s.validateWorkspaceSHAs(ctx, *req, commit); err != nil {
			return err
		}
		parent, err := workspace.WorktreeParentOf(
			ctx, req.Summary.WorktreePath, commit,
		)
		if err != nil {
			return huma.Error500InternalServerError(
				"failed to resolve parent: " + err.Error(),
			)
		}
		req.FromSHA = parent
		req.ToSHA = commit
		return nil

	case !hasCommit && hasFrom && hasTo:
		indexMap, err := s.validateWorkspaceSHAs(ctx, *req, from, to)
		if err != nil {
			return err
		}
		if indexMap[from] < indexMap[to] {
			return huma.Error400BadRequest(
				"invalid range: 'from' must be older than or equal to 'to'",
			)
		}
		parent, err := workspace.WorktreeParentOf(
			ctx, req.Summary.WorktreePath, from,
		)
		if err != nil {
			return huma.Error500InternalServerError(
				"failed to resolve parent: " + err.Error(),
			)
		}
		req.FromSHA = parent
		req.ToSHA = to
		return nil

	default:
		return huma.Error400BadRequest(
			"invalid scope: use 'commit' alone or 'from'+'to' together",
		)
	}
}

func (s *Server) validateWorkspaceSHAs(
	ctx context.Context,
	req workspaceDiffRequest,
	shas ...string,
) (map[string]int, error) {
	commits, ok, err := s.workspaceCommits(ctx, req)
	if err != nil {
		return nil, huma.Error502BadGateway(
			"failed to list workspace commits: " + err.Error(),
		)
	}
	if !ok {
		return nil, huma.Error404NotFound(
			"commits not available for this workspace",
		)
	}
	indexMap := make(map[string]int, len(commits))
	for i, c := range commits {
		indexMap[c.SHA] = i
	}
	for _, sha := range shas {
		if _, ok := indexMap[sha]; !ok {
			return nil, huma.Error400BadRequest(
				"invalid scope: commit is not in this workspace branch",
			)
		}
	}
	return indexMap, nil
}

func (s *Server) workspaceDiffFiles(
	ctx context.Context,
	req workspaceDiffRequest,
	hideWhitespace bool,
) ([]gitclone.DiffFile, bool, error) {
	if req.FromSHA != "" && req.ToSHA != "" {
		return workspace.WorktreeDiffFilesBetween(
			ctx,
			req.Summary.WorktreePath,
			req.FromSHA,
			req.ToSHA,
			hideWhitespace,
		)
	}
	if req.Base == workspace.WorktreeDiffBaseMergeTarget {
		return workspace.WorktreeDiffFilesAgainstMergeTarget(
			ctx,
			req.Summary.WorktreePath,
			req.MergeTargetBranch,
			hideWhitespace,
		)
	}
	return workspace.WorktreeDiffFiles(
		ctx, req.Summary.WorktreePath, req.Base, hideWhitespace,
	)
}

func (s *Server) workspaceDiff(
	ctx context.Context,
	req workspaceDiffRequest,
	hideWhitespace bool,
	path string,
) (*gitclone.DiffResult, bool, error) {
	if req.FromSHA != "" && req.ToSHA != "" {
		if path != "" {
			return workspace.WorktreeFileDiffBetween(
				ctx,
				req.Summary.WorktreePath,
				req.FromSHA,
				req.ToSHA,
				hideWhitespace,
				path,
			)
		}
		return workspace.WorktreeDiffBetween(
			ctx,
			req.Summary.WorktreePath,
			req.FromSHA,
			req.ToSHA,
			hideWhitespace,
		)
	}
	if req.Base == workspace.WorktreeDiffBaseMergeTarget {
		if path != "" {
			return workspace.WorktreeFileDiffAgainstMergeTarget(
				ctx,
				req.Summary.WorktreePath,
				req.MergeTargetBranch,
				hideWhitespace,
				path,
			)
		}
		return workspace.WorktreeDiffAgainstMergeTarget(
			ctx,
			req.Summary.WorktreePath,
			req.MergeTargetBranch,
			hideWhitespace,
		)
	}
	if path != "" {
		return workspace.WorktreeFileDiff(
			ctx, req.Summary.WorktreePath, req.Base, hideWhitespace, path,
		)
	}
	return workspace.WorktreeDiff(
		ctx, req.Summary.WorktreePath, req.Base, hideWhitespace,
	)
}

func (s *Server) workspaceDiffWhitespaceOnlyCount(
	ctx context.Context,
	req workspaceDiffRequest,
) (int, bool, error) {
	if req.FromSHA != "" && req.ToSHA != "" {
		return workspace.WorktreeDiffWhitespaceOnlyCountBetween(
			ctx,
			req.Summary.WorktreePath,
			req.FromSHA,
			req.ToSHA,
		)
	}
	if req.Base == workspace.WorktreeDiffBaseMergeTarget {
		return workspace.WorktreeDiffWhitespaceOnlyCountAgainstMergeTarget(
			ctx,
			req.Summary.WorktreePath,
			req.MergeTargetBranch,
		)
	}
	return workspace.WorktreeDiffWhitespaceOnlyCount(
		ctx, req.Summary.WorktreePath, req.Base,
	)
}

func (s *Server) workspaceMergeTargetBranch(
	ctx context.Context,
	summary *db.WorkspaceSummary,
) (string, bool, error) {
	prNumber := summary.ItemNumber
	if summary.ItemType == db.WorkspaceItemTypeIssue {
		if summary.AssociatedPRNumber == nil {
			return "", false, nil
		}
		prNumber = *summary.AssociatedPRNumber
	}

	repo, err := s.db.GetRepoByHostOwnerName(
		ctx,
		summary.PlatformHost,
		summary.RepoOwner,
		summary.RepoName,
	)
	if err != nil {
		return "", false, huma.Error500InternalServerError(
			"get workspace repo failed",
		)
	}
	if repo == nil {
		return "", false, nil
	}

	mr, err := s.db.GetMergeRequestByRepoIDAndNumber(
		ctx, repo.ID, prNumber,
	)
	if err != nil {
		return "", false, huma.Error500InternalServerError(
			"get workspace pull request failed",
		)
	}
	if mr == nil || strings.TrimSpace(mr.BaseBranch) == "" {
		return "", false, nil
	}
	return strings.TrimSpace(mr.BaseBranch), true, nil
}

func workspaceDiffBaseUnavailable(
	base workspace.WorktreeDiffBase,
) error {
	if base == workspace.WorktreeDiffBaseMergeTarget {
		return huma.Error404NotFound(
			"workspace merge target branch not available",
		)
	}
	return huma.Error404NotFound(
		"workspace pushed branch not available",
	)
}

func (s *Server) retryWorkspace(
	ctx context.Context, input *retryWorkspaceInput,
) (*createWorkspaceOutput, error) {
	if s.workspaces == nil {
		return nil, huma.Error503ServiceUnavailable(
			"workspace manager not configured",
		)
	}

	ws, startNow, err := s.workspaces.RequestRetry(ctx, input.ID)
	if err != nil {
		if errors.Is(err, workspace.ErrWorkspaceNotFound) {
			return nil, huma.Error404NotFound(err.Error())
		}
		if errors.Is(err, workspace.ErrWorkspaceInvalidState) {
			return nil, huma.Error409Conflict(err.Error())
		}
		return nil, huma.Error500InternalServerError(
			"retry workspace: " + err.Error(),
		)
	}

	if startNow {
		s.runWorkspaceSetup(ws)
	}

	summary, err := s.workspaces.GetSummary(ctx, ws.ID)
	if err != nil {
		return nil, huma.Error500InternalServerError(
			"get workspace summary: " + err.Error(),
		)
	}
	if summary == nil {
		return nil, huma.Error500InternalServerError(
			"workspace summary missing after retry",
		)
	}
	resp := s.toWorkspaceResponse(ctx, summary)
	s.hub.Broadcast(Event{
		Type: "workspace_status",
		Data: resp,
	})
	return &createWorkspaceOutput{
		Status: http.StatusAccepted,
		Body:   resp,
	}, nil
}

func (s *Server) toWorkspaceResponse(
	ctx context.Context,
	summary *db.WorkspaceSummary,
) workspaceResponse {
	resp := toWorkspaceResponse(summary)
	resp.Repo = s.repoRefFromParts(
		summary.Platform, summary.PlatformHost, summary.RepoOwner, summary.RepoName,
	)
	if s.workspaces == nil ||
		summary.Status != "ready" {
		return resp
	}

	applyWorktreeDivergence(ctx, &resp, summary.WorktreePath)
	if activity, ok := s.probeWorkspaceTmuxActivity(
		ctx, summary, s.workspaceTmuxActivitySessions(ctx, summary),
	); ok {
		applyTmuxActivity(&resp, activity)
	}
	return resp
}

func (s *Server) workspaceTmuxActivitySessions(
	ctx context.Context,
	summary *db.WorkspaceSummary,
) []string {
	sessions := make([]string, 0, 1)
	seen := map[string]bool{}
	if s.workspaces != nil {
		stored, err := s.workspaces.TmuxSessionsForWorkspace(
			ctx, summary.ID, summary.TmuxSession,
		)
		if err != nil {
			slog.Debug(
				"list workspace tmux sessions",
				"workspace_id", summary.ID,
				"tmux_session", summary.TmuxSession,
				"err", err,
			)
		}
		for _, session := range stored {
			if session == "" || seen[session] {
				continue
			}
			sessions = append(sessions, session)
			seen[session] = true
		}
	}
	if summary.TmuxSession != "" && !seen[summary.TmuxSession] {
		sessions = append(sessions, summary.TmuxSession)
		seen[summary.TmuxSession] = true
	}
	if s.runtime == nil {
		return sessions
	}
	for _, session := range s.runtime.TmuxSessions(summary.ID) {
		if session == "" || seen[session] {
			continue
		}
		sessions = append(sessions, session)
		seen[session] = true
	}
	return sessions
}

func (s *Server) probeWorkspaceTmuxActivity(
	ctx context.Context,
	summary *db.WorkspaceSummary,
	sessions []string,
) (tmuxActivityResult, bool) {
	if len(sessions) == 0 {
		return tmuxActivityResult{}, false
	}
	tracker := s.tmuxActivity
	if tracker == nil {
		tracker = newTmuxActivityTracker(nil)
	}
	probeCtx, cancelProbe := context.WithTimeout(ctx, tmuxActivityProbeTimeout)
	defer cancelProbe()

	results := make([]tmuxActivityResult, 0, len(sessions))
	for _, session := range sessions {
		if s.tmuxActivity != nil {
			if result, ok := tracker.Cached(session); ok {
				results = append(results, result)
				continue
			}
		}
		result, ok := s.probeOneTmuxSession(
			probeCtx, tracker, summary, session,
		)
		if ok {
			results = append(results, result)
		}
	}
	return mergeTmuxActivityResults(results)
}

func (s *Server) probeOneTmuxSession(
	ctx context.Context,
	tracker *tmuxActivityTracker,
	summary *db.WorkspaceSummary,
	session string,
) (tmuxActivityResult, bool) {
	probe := tracker.StartProbe(ctx, session)
	if !probe.Started {
		if probe.HasFallback {
			return probe.Fallback, true
		}
		if probe.Wait != nil {
			select {
			case <-probe.Wait:
				return tracker.Cached(session)
			case <-ctx.Done():
			}
		}
		return tmuxActivityResult{}, false
	}

	snapshot, err := s.workspaces.TerminalPaneSnapshot(
		ctx, &summary.Workspace, session,
	)
	if err != nil {
		probe.Probe.Cancel()
		slog.Debug(
			"read tmux pane snapshot",
			"workspace_id", summary.ID,
			"tmux_session", session,
			"err", err,
		)
		if probe.HasFallback {
			return probe.Fallback, true
		}
		return tmuxActivityResult{}, false
	}

	return probe.Probe.Finish(tmuxActivityObservation{
		PaneTitle: snapshot.Title,
		Output:    snapshot.Output,
		HasOutput: true,
	}), true
}

func applyTmuxActivity(resp *workspaceResponse, activity tmuxActivityResult) {
	if activity.PaneTitle != "" {
		title := activity.PaneTitle
		resp.TmuxPaneTitle = &title
	}
	resp.TmuxWorking = activity.Working
	resp.TmuxActivitySource = activity.Source
	if activity.LastOutputAt != nil {
		lastOutputAt := activity.LastOutputAt.UTC().Format(time.RFC3339)
		resp.TmuxLastOutputAt = &lastOutputAt
	}
}

// worktreeDivergenceTimeout caps how long a single workspace's
// rev-list probe can run before the workspace list response moves
// on. Picked to be small enough that a stalled git won't hold up
// the whole list (probes already run in parallel).
const worktreeDivergenceTimeout = 750 * time.Millisecond

func applyWorktreeDivergence(
	ctx context.Context,
	resp *workspaceResponse,
	worktreePath string,
) {
	if worktreePath == "" {
		return
	}
	probeCtx, cancel := context.WithTimeout(ctx, worktreeDivergenceTimeout)
	defer cancel()

	div, ok, err := workspace.WorktreeDivergence(probeCtx, worktreePath)
	if err != nil {
		slog.Debug(
			"worktree divergence probe failed",
			"workspace_id", resp.ID,
			"path", worktreePath,
			"err", err,
		)
		return
	}
	if !ok {
		return
	}
	ahead := div.Ahead
	behind := div.Behind
	resp.CommitsAhead = &ahead
	resp.CommitsBehind = &behind
}

func isWorkingTmuxTitle(title string) bool {
	normalized := strings.TrimSpace(title)
	if normalized == "" {
		return false
	}

	for _, frame := range "⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏" {
		if strings.HasPrefix(normalized, string(frame)+" ") {
			return true
		}
	}

	return false
}

func (s *Server) getWorkspaceRuntime(
	ctx context.Context,
	input *getWorkspaceRuntimeInput,
) (*getWorkspaceRuntimeOutput, error) {
	summary, err := s.getReadyRuntimeWorkspace(ctx, input.ID)
	if err != nil {
		return nil, err
	}

	return &getWorkspaceRuntimeOutput{
		Body: workspaceRuntimeResponse{
			LaunchTargets: s.runtime.LaunchTargets(),
			Sessions:      s.runtime.ListSessions(summary.ID),
			ShellSession:  s.runtime.ShellSession(summary.ID),
		},
	}, nil
}

func (s *Server) launchWorkspaceRuntimeSession(
	ctx context.Context,
	input *launchWorkspaceRuntimeSessionInput,
) (*workspaceRuntimeSessionOutput, error) {
	summary, err := s.getReadyRuntimeWorkspace(ctx, input.ID)
	if err != nil {
		return nil, err
	}
	targetKey := strings.TrimSpace(input.Body.TargetKey)
	if targetKey == "" {
		return nil, huma.Error400BadRequest("target_key is required")
	}

	if targetKey == string(localruntime.LaunchTargetPlainShell) {
		session, err := s.runtime.EnsureShell(
			ctx, summary.ID, summary.WorktreePath,
		)
		if err != nil {
			return nil, huma.Error500InternalServerError(
				"ensure shell: " + err.Error(),
			)
		}
		return &workspaceRuntimeSessionOutput{Body: session}, nil
	}

	session, err := s.runtime.Launch(
		ctx, summary.ID, summary.WorktreePath, targetKey,
	)
	if err != nil {
		return nil, workspaceRuntimeLaunchError(err)
	}
	if session.TmuxSession != "" {
		if err := s.workspaces.RecordRuntimeTmuxSession(
			ctx, summary.ID, session.TmuxSession, session.TargetKey,
			session.CreatedAt,
		); err != nil {
			_ = s.runtime.Stop(ctx, summary.ID, session.Key)
			return nil, huma.Error500InternalServerError(
				"record runtime tmux session: " + err.Error(),
			)
		}
		if runtimeSessionTmuxSession(
			s.runtime.ListSessions(summary.ID), session.Key,
		) == "" {
			if _, err := s.workspaces.ForgetMissingRuntimeTmuxSession(
				ctx, summary.ID, session.TmuxSession,
				session.CreatedAt,
			); err != nil {
				return nil, huma.Error500InternalServerError(
					"forget missing runtime tmux session: " + err.Error(),
				)
			}
		}
	}
	return &workspaceRuntimeSessionOutput{Body: session}, nil
}

func (s *Server) stopWorkspaceRuntimeSession(
	ctx context.Context,
	input *stopWorkspaceRuntimeSessionInput,
) (*struct{}, error) {
	summary, err := s.getReadyRuntimeWorkspace(ctx, input.ID)
	if err != nil {
		return nil, err
	}
	tmuxSession := runtimeSessionTmuxSession(
		s.runtime.ListSessions(summary.ID), input.SessionKey,
	)
	if err := s.runtime.Stop(
		ctx, summary.ID, input.SessionKey,
	); err != nil {
		if errors.Is(err, localruntime.ErrSessionNotFound) {
			if targetKey, ok := legacyRuntimeTargetKeyFromSessionKey(
				summary.ID, input.SessionKey,
			); ok {
				stopped, stopErr := s.workspaces.StopStoredRuntimeTmuxSession(
					ctx, summary.ID, targetKey,
				)
				if stopErr != nil {
					return nil, huma.Error500InternalServerError(
						"stop stored runtime tmux session: " +
							stopErr.Error(),
					)
				}
				if stopped {
					return nil, nil
				}
			}
			stopped, stopErr := s.workspaces.StopStoredRuntimeTmuxSessionByKey(
				ctx, summary.ID, input.SessionKey,
			)
			if stopErr != nil {
				return nil, huma.Error500InternalServerError(
					"stop stored runtime tmux session: " + stopErr.Error(),
				)
			}
			if stopped {
				return nil, nil
			}
			return nil, huma.Error404NotFound(err.Error())
		}
		return nil, huma.Error500InternalServerError(
			"stop runtime session: " + err.Error(),
		)
	}
	if tmuxSession != "" {
		if err := s.workspaces.ForgetRuntimeTmuxSession(
			ctx, summary.ID, tmuxSession,
		); err != nil {
			return nil, huma.Error500InternalServerError(
				"forget runtime tmux session: " + err.Error(),
			)
		}
	}
	return nil, nil
}

func runtimeSessionTmuxSession(
	sessions []localruntime.SessionInfo,
	key string,
) string {
	for _, session := range sessions {
		if session.Key == key {
			return session.TmuxSession
		}
	}
	return ""
}

func legacyRuntimeTargetKeyFromSessionKey(
	workspaceID string,
	key string,
) (string, bool) {
	targetKey, ok := strings.CutPrefix(key, workspaceID+":")
	return targetKey, ok && targetKey != ""
}

func (s *Server) ensureWorkspaceRuntimeShell(
	ctx context.Context,
	input *getWorkspaceRuntimeInput,
) (*workspaceRuntimeSessionOutput, error) {
	summary, err := s.getReadyRuntimeWorkspace(ctx, input.ID)
	if err != nil {
		return nil, err
	}

	session, err := s.runtime.EnsureShell(
		ctx, summary.ID, summary.WorktreePath,
	)
	if err != nil {
		return nil, huma.Error500InternalServerError(
			"ensure shell: " + err.Error(),
		)
	}
	return &workspaceRuntimeSessionOutput{Body: session}, nil
}

func (s *Server) getReadyRuntimeWorkspace(
	ctx context.Context,
	id string,
) (*db.WorkspaceSummary, error) {
	if s.workspaces == nil || s.runtime == nil {
		return nil, huma.Error503ServiceUnavailable(
			"workspace runtime not configured",
		)
	}

	summary, err := s.workspaces.GetSummary(ctx, id)
	if err != nil {
		return nil, huma.Error500InternalServerError(
			"get workspace failed",
		)
	}
	if summary == nil {
		return nil, huma.Error404NotFound("workspace not found")
	}
	if summary.Status != "ready" {
		return nil, huma.Error409Conflict(
			"workspace not ready (status: " + summary.Status + ")",
		)
	}
	return summary, nil
}

func workspaceRuntimeLaunchError(err error) error {
	msg := err.Error()
	if strings.Contains(msg, "target not found") {
		return huma.Error404NotFound(msg)
	}
	if strings.Contains(msg, "not available") ||
		strings.Contains(msg, "no command") {
		return huma.Error400BadRequest(msg)
	}
	return huma.Error500InternalServerError("launch session: " + msg)
}

// deleteWorkspace tears down a middleman-managed workspace.
//
// This exists to remove the persisted workspace entry plus its managed local
// resources. It is not intended to delete arbitrary worktrees on disk.
func (s *Server) deleteWorkspace(
	ctx context.Context, input *deleteWorkspaceInput,
) (*struct{}, error) {
	if s.workspaces == nil {
		return nil, huma.Error503ServiceUnavailable(
			"workspace manager not configured",
		)
	}

	if s.runtime != nil {
		// Block new launches before the dirty preflight; existing
		// sessions are stopped only after the preflight passes.
		s.runtime.BeginStopping(input.ID)
	}
	defer func() {
		if s.runtime != nil {
			s.runtime.EndStopping(input.ID)
		}
	}()
	dirty, err := s.workspaces.Delete(
		ctx, input.ID, input.Force,
		func(stopCtx context.Context) {
			if s.runtime != nil {
				s.runtime.StopWorkspace(stopCtx, input.ID)
			}
		},
	)
	if err != nil {
		if errors.Is(err, workspace.ErrWorkspaceNotFound) {
			return nil, huma.Error404NotFound(err.Error())
		}
		return nil, huma.Error500InternalServerError(
			"delete workspace: " + err.Error(),
		)
	}
	if len(dirty) > 0 {
		return nil, huma.Error409Conflict(
			"workspace has uncommitted changes: " +
				strings.Join(dirty, ", "),
		)
	}

	return nil, nil
}
