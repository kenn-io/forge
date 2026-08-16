// Package issueapi owns provider Issue list, detail, content, comment, label,
// assignee, and state HTTP behavior.
package issueapi

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"go.kenn.io/forge/internal/db"
	ghclient "go.kenn.io/forge/internal/github"
	"go.kenn.io/forge/internal/server/httpapi"
	"go.kenn.io/forge/internal/server/workspaceapi"
)

type ConfigSnapshot struct {
	UseWorkspaceActivityForRecency bool
}

const (
	capabilityAssigneeMutation = "assignee_mutation"
	capabilityCommentMutation  = "comment_mutation"
	capabilityIssueMutation    = "issue_mutation"
	capabilityLabelMutation    = "label_mutation"
	capabilityReadLabels       = "read_labels"
	capabilityStateMutation    = "state_mutation"
)

type Deps struct {
	DB                *db.DB
	Resolver          *httpapi.RepositoryResolver
	Syncer            *ghclient.Syncer
	Now               func() time.Time
	Config            ConfigSnapshot
	WorkspaceSubjects func(context.Context) (workspaceapi.WorkspaceSubjectSnapshot, error)
	ViewerLogins      func(context.Context, []db.RepoFilter) ([]db.RepoViewerLogin, error)

	FilterRepos                       func([]db.Repo) []db.Repo
	RepoOperations                    func(db.Repo) httpapi.RepoOperations
	MarkClosedLinkedNotificationsDone func(context.Context)
}

type Handler struct {
	db                *db.DB
	resolver          *httpapi.RepositoryResolver
	syncer            *ghclient.Syncer
	now               func() time.Time
	workspaceSubjects func(context.Context) (workspaceapi.WorkspaceSubjectSnapshot, error)
	viewerLogins      func(context.Context, []db.RepoFilter) ([]db.RepoViewerLogin, error)

	filterRepos             func([]db.Repo) []db.Repo
	repoOperations          func(db.Repo) httpapi.RepoOperations
	markClosedNotifications func(context.Context)

	configMu sync.RWMutex
	config   ConfigSnapshot
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
		now:                     now,
		workspaceSubjects:       deps.WorkspaceSubjects,
		viewerLogins:            deps.ViewerLogins,
		filterRepos:             deps.FilterRepos,
		repoOperations:          deps.RepoOperations,
		markClosedNotifications: deps.MarkClosedLinkedNotificationsDone,
		config:                  deps.Config,
	}
}

func (s *Handler) ApplyConfig(config ConfigSnapshot) {
	s.configMu.Lock()
	s.config = config
	s.configMu.Unlock()
}

func (s *Handler) ConfigSnapshot() ConfigSnapshot {
	s.configMu.RLock()
	defer s.configMu.RUnlock()
	return s.config
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

func (s *Handler) requireSyncerCapability(repo db.Repo, capability string) error {
	if s.syncer == nil {
		return httpapi.UnsupportedCapability(repo, capability)
	}
	return nil
}
