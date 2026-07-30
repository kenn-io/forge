// Package issueapi owns provider Issue list, detail, content, comment, label,
// assignee, and state HTTP behavior.
package issueapi

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"go.kenn.io/middleman/internal/db"
	ghclient "go.kenn.io/middleman/internal/github"
	"go.kenn.io/middleman/internal/server/httpapi"
	"go.kenn.io/middleman/internal/workspace"
)

const (
	capabilityAssigneeMutation = "assignee_mutation"
	capabilityCommentMutation  = "comment_mutation"
	capabilityIssueMutation    = "issue_mutation"
	capabilityLabelMutation    = "label_mutation"
	capabilityReadLabels       = "read_labels"
	capabilityStateMutation    = "state_mutation"
)

type Deps struct {
	DB         *db.DB
	Resolver   *httpapi.RepositoryResolver
	Syncer     *ghclient.Syncer
	Workspaces *workspace.Manager
	Now        func() time.Time

	FilterRepos                       func([]db.Repo) []db.Repo
	RepoOperations                    func(db.Repo) httpapi.RepoOperations
	MarkClosedLinkedNotificationsDone func(context.Context)
}

type Handler struct {
	db         *db.DB
	resolver   *httpapi.RepositoryResolver
	syncer     *ghclient.Syncer
	workspaces *workspace.Manager
	now        func() time.Time

	filterRepos             func([]db.Repo) []db.Repo
	repoOperations          func(db.Repo) httpapi.RepoOperations
	markClosedNotifications func(context.Context)
}

func New(deps Deps) *Handler {
	now := deps.Now
	if now == nil {
		now = time.Now
	}
	return &Handler{
		db:                      deps.DB,
		resolver:                deps.Resolver,
		syncer:                  deps.Syncer,
		workspaces:              deps.Workspaces,
		now:                     now,
		filterRepos:             deps.FilterRepos,
		repoOperations:          deps.RepoOperations,
		markClosedNotifications: deps.MarkClosedLinkedNotificationsDone,
	}
}

func (s *Handler) Register(api huma.API) {
	issueRepo := "/issues/{provider}/{owner}/{name}"
	hostIssueRepo := "/host/{platform_host}" + issueRepo
	issue := issueRepo + "/{number}"
	hostIssue := hostIssueRepo + "/{number}"

	huma.Get(api, "/issues", s.listIssues,
		httpapi.DocumentOperation("list-issues", "List issues", "Issues"))
	register(api, "create-issue", http.MethodPost, issueRepo, http.StatusCreated, "Create issue", s.createIssue)
	register(api, "create-issue-on-host", http.MethodPost, hostIssueRepo, http.StatusCreated, "Create issue", s.createIssueOnHost)
	huma.Get(api, issue, s.getIssue,
		httpapi.DocumentOperation("get-issue", "Get issue", "Issues"))
	huma.Get(api, hostIssue, s.getIssueOnHost,
		httpapi.DocumentOperation("get-issue-on-host", "Get issue", "Issues"))
	register(api, "post-issue-comment", http.MethodPost, issue+"/comments", http.StatusCreated, "Post issue comment", s.postIssueComment)
	register(api, "post-issue-comment-on-host", http.MethodPost, hostIssue+"/comments", http.StatusCreated, "Post issue comment", s.postIssueCommentOnHost)
	register(api, "edit-issue-content", http.MethodPatch, issue, http.StatusOK, "Edit issue content", s.editIssueContent)
	register(api, "edit-issue-content-on-host", http.MethodPatch, hostIssue, http.StatusOK, "Edit issue content", s.editIssueContentOnHost)
	register(api, "edit-issue-comment", http.MethodPatch, issue+"/comments/{comment_id}", http.StatusOK, "Edit issue comment", s.editIssueComment)
	register(api, "edit-issue-comment-on-host", http.MethodPatch, hostIssue+"/comments/{comment_id}", http.StatusOK, "Edit issue comment", s.editIssueCommentOnHost)
	register(api, "delete-issue-comment", http.MethodDelete, issue+"/comments/{comment_id}", http.StatusNoContent, "Delete issue comment", s.deleteIssueComment)
	register(api, "delete-issue-comment-on-host", http.MethodDelete, hostIssue+"/comments/{comment_id}", http.StatusNoContent, "Delete issue comment", s.deleteIssueCommentOnHost)
	register(api, "set-issue-labels", http.MethodPut, issue+"/labels", http.StatusOK, "Set issue labels", s.setIssueLabels)
	register(api, "set-issue-labels-on-host", http.MethodPut, hostIssue+"/labels", http.StatusOK, "Set issue labels", s.setIssueLabelsOnHost)
	register(api, "set-issue-assignees", http.MethodPut, issue+"/assignees", http.StatusOK, "Set issue assignees", s.setIssueAssignees)
	register(api, "set-issue-assignees-on-host", http.MethodPut, hostIssue+"/assignees", http.StatusOK, "Set issue assignees", s.setIssueAssigneesOnHost)
	register(api, "set-issue-github-state", http.MethodPost, issue+"/github-state", http.StatusOK, "Set issue GitHub state", s.setIssueGitHubState)
	register(api, "set-issue-github-state-on-host", http.MethodPost, hostIssue+"/github-state", http.StatusOK, "Set issue GitHub state", s.setIssueGitHubStateOnHost)
}

func register[I, O any](
	api huma.API,
	operationID, method, path string,
	status int,
	summary string,
	handler func(context.Context, *I) (*O, error),
) {
	huma.Register(api, huma.Operation{
		OperationID: operationID, Method: method, Path: path,
		DefaultStatus: status, Summary: summary, Tags: []string{"Issues"},
	}, handler)
}

func (s *Handler) markClosedLinkedNotificationsDone(ctx context.Context) {
	if s.markClosedNotifications != nil {
		s.markClosedNotifications(ctx)
	}
}

func (s *Handler) operations(repo db.Repo) httpapi.RepoOperations {
	if s.repoOperations == nil {
		return httpapi.RepoOperations{}
	}
	return s.repoOperations(repo)
}

func (s *Handler) requireSyncerCapability(
	ctx context.Context,
	repo db.Repo,
	capability string,
) (context.Context, func(), error) {
	if s.syncer == nil || !s.syncer.IsTrackedRepoOnProvider(
		httpapi.ProviderKind(repo),
		httpapi.ProviderHost(repo),
		repo.Owner,
		repo.Name,
	) {
		return ctx, func() {}, httpapi.UnsupportedCapability(repo, capability)
	}
	leaseCtx, release, err := s.syncer.HoldRepoMutationIncarnation(ctx, repo)
	if err == nil {
		return leaseCtx, release, nil
	}
	if errors.Is(err, ghclient.ErrConfiguredRepoIdentityChanged) {
		return ctx, func() {}, httpapi.Conflict(
			httpapi.CodeConflict,
			"repository changed; reload and try again",
			map[string]any{"reason": "repository_changed"},
		)
	}
	return ctx, func() {}, httpapi.Internal(
		"validate repository incarnation failed",
	)
}
