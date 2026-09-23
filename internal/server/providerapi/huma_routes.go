package providerapi

import (
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"go.kenn.io/forge/internal/server/httpapi"
)

func (s *Handlers) RegisterProviderRepoAPI(api huma.API) {
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

	huma.Post(api, repoPath+"/resolve/{number}", s.ResolveItem,
		httpapi.DocumentOperation("resolve-repo-item", "Resolve repository item", "Repositories"))
	huma.Post(api, hostRepoPath+"/resolve/{number}", s.resolveItemOnHost,
		httpapi.DocumentOperation("resolve-repo-item-on-host", "Resolve repository item", "Repositories"))
	huma.Register(api, huma.Operation{OperationID: "create-repo-workspace", Method: http.MethodPost, Path: repoPath + "/workspaces", DefaultStatus: http.StatusAccepted, Summary: "Create workspace for new work", Tags: []string{"Workspaces"}}, (*s.WorkspaceAPI).CreateAdHocWorkspace)
	huma.Register(api, huma.Operation{OperationID: "create-repo-workspace-on-host", Method: http.MethodPost, Path: hostRepoPath + "/workspaces", DefaultStatus: http.StatusAccepted, Summary: "Create workspace for new work", Tags: []string{"Workspaces"}}, s.createAdHocWorkspaceOnHost)
	huma.Get(api, repoPath, s.GetRepo,
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
	huma.Get(api, repoPath+"/commits/{sha}/diff", s.GetRepoCommitDiff,
		httpapi.DocumentOperation("get-repo-commit-diff", "Get repository commit diff", "Repositories"))
	huma.Get(api, hostRepoPath+"/commits/{sha}/diff", s.GetRepoCommitDiffOnHost,
		httpapi.DocumentOperation("get-repo-commit-diff-on-host", "Get repository commit diff", "Repositories"))
	huma.Register(api, huma.Operation{OperationID: "list-repo-labels", Method: http.MethodGet, Path: repoPath + "/labels", DefaultStatus: http.StatusOK, Summary: "List repository labels", Tags: []string{"Repositories"}}, s.ListRepoLabels)
	huma.Register(api, huma.Operation{OperationID: "list-repo-labels-on-host", Method: http.MethodGet, Path: hostRepoPath + "/labels", DefaultStatus: http.StatusOK, Summary: "List repository labels", Tags: []string{"Repositories"}}, s.listRepoLabelsOnHost)
	huma.Get(api, repoPath+"/comment-autocomplete", s.GetCommentAutocomplete,
		httpapi.DocumentOperation("get-comment-autocomplete", "Get comment autocomplete", "Repositories"))
	huma.Get(api, hostRepoPath+"/comment-autocomplete", s.getCommentAutocompleteOnHost,
		httpapi.DocumentOperation("get-comment-autocomplete-on-host", "Get comment autocomplete", "Repositories"))

	huma.Post(api, pullPath+"/sync", s.SyncPR,
		httpapi.DocumentOperation("sync-pull", "Sync pull request", "Pull Requests"))
	huma.Post(api, hostPullPath+"/sync", s.syncPROnHost,
		httpapi.DocumentOperation("sync-pull-on-host", "Sync pull request", "Pull Requests"))
	huma.Post(api, pullPath+"/ci-refresh", s.SyncPRCI,
		httpapi.DocumentOperation("refresh-pull-ci", "Refresh pull request CI", "Pull Requests"))
	huma.Post(api, hostPullPath+"/ci-refresh", s.syncPRCIOnHost,
		httpapi.DocumentOperation("refresh-pull-ci-on-host", "Refresh pull request CI", "Pull Requests"))
	huma.Register(api, huma.Operation{OperationID: "enqueue-pr-sync", Method: http.MethodPost, Path: pullPath + "/sync/async", DefaultStatus: http.StatusAccepted, Summary: "Enqueue pull request sync", Tags: []string{"Pull Requests"}}, s.EnqueuePRSync)
	huma.Register(api, huma.Operation{OperationID: "enqueue-pr-sync-on-host", Method: http.MethodPost, Path: hostPullPath + "/sync/async", DefaultStatus: http.StatusAccepted, Summary: "Enqueue pull request sync", Tags: []string{"Pull Requests"}}, s.enqueuePRSyncOnHost)
	huma.Post(api, issuePath+"/sync", s.SyncIssue,
		httpapi.DocumentOperation("sync-issue", "Sync issue", "Issues"))
	huma.Post(api, hostIssuePath+"/sync", s.syncIssueOnHost,
		httpapi.DocumentOperation("sync-issue-on-host", "Sync issue", "Issues"))
	huma.Register(api, huma.Operation{OperationID: "enqueue-issue-sync", Method: http.MethodPost, Path: issuePath + "/sync/async", DefaultStatus: http.StatusAccepted, Summary: "Enqueue issue sync", Tags: []string{"Issues"}}, s.EnqueueIssueSync)
	huma.Register(api, huma.Operation{OperationID: "enqueue-issue-sync-on-host", Method: http.MethodPost, Path: hostIssuePath + "/sync/async", DefaultStatus: http.StatusAccepted, Summary: "Enqueue issue sync", Tags: []string{"Issues"}}, s.enqueueIssueSyncOnHost)
	huma.Register(api, huma.Operation{OperationID: "create-issue-workspace", Method: http.MethodPost, Path: issuePath + "/workspace", DefaultStatus: http.StatusAccepted, Summary: "Create issue workspace", Tags: []string{"Issues"}}, (*s.WorkspaceAPI).CreateIssueWorkspace)
	huma.Register(api, huma.Operation{OperationID: "create-issue-workspace-on-host", Method: http.MethodPost, Path: hostIssuePath + "/workspace", DefaultStatus: http.StatusAccepted, Summary: "Create issue workspace", Tags: []string{"Issues"}}, s.createIssueWorkspaceOnHost)
}
