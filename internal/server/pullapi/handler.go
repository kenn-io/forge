// Package pullapi owns pull-request, review, comment, merge, check, and diff
// HTTP behavior.
package pullapi

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/gitclone"
	ghclient "go.kenn.io/forge/internal/github"
	"go.kenn.io/forge/internal/server/httpapi"
	"go.kenn.io/forge/internal/workspace"
)

type Event struct {
	Type string
	Data any
}

type ConfigSnapshot struct {
	AllowMidStackMerges bool
}

type Deps struct {
	DB                   *db.DB
	Resolver             *httpapi.RepositoryResolver
	Syncer               *ghclient.Syncer
	Clones               *gitclone.Manager
	Workspaces           *workspace.Manager
	Config               ConfigSnapshot
	Now                  func() time.Time
	DeferredMergeMaxWait time.Duration
	DeleteWorkspace      func(context.Context, string, bool) ([]string, error)

	FleetSelfKey                  func(string) string
	FilterRepos                   func([]db.Repo) []db.Repo
	RepoOperations                func(db.Repo) httpapi.RepoOperations
	RepoOperationsForMergeRequest func(
		context.Context, db.Repo, db.MergeRequest,
	) httpapi.RepoOperations
	EnqueueDetailSyncOrRerun func(
		string, []any, func(context.Context) error,
	) bool
	Broadcast                         func(Event) uint64
	MarkClosedLinkedNotificationsDone func(context.Context)
}

type Handler struct {
	db              *db.DB
	resolver        *httpapi.RepositoryResolver
	syncer          *ghclient.Syncer
	clones          *gitclone.Manager
	workspaces      *workspace.Manager
	deleteWorkspace func(context.Context, string, bool) ([]string, error)
	now             func() time.Time

	fleetSelfKey                  func(string) string
	filterRepos                   func([]db.Repo) []db.Repo
	repoOperations                func(db.Repo) httpapi.RepoOperations
	repoOperationsForMergeRequest func(
		context.Context, db.Repo, db.MergeRequest,
	) httpapi.RepoOperations
	enqueueDetailSyncRerun func(
		string, []any, func(context.Context) error,
	) bool
	broadcast               func(Event) uint64
	markClosedNotifications func(context.Context)

	configMu sync.RWMutex
	config   ConfigSnapshot

	bgCtx        context.Context
	bgCancel     context.CancelFunc
	bgMu         sync.Mutex
	bgWG         sync.WaitGroup
	stopping     bool
	shutdownDone chan struct{}

	deferredMergeMu       sync.Mutex
	deferredMergeInFlight map[string]*deferredMergeHandle
	deferredMergeMaxWait  time.Duration
}

func New(deps Deps) *Handler {
	now := deps.Now
	if now == nil {
		now = time.Now
	}
	bgCtx, bgCancel := context.WithCancel(context.Background())
	maxWait := deps.DeferredMergeMaxWait
	if maxWait <= 0 {
		maxWait = defaultDeferredMergeMaxWait
	}
	return &Handler{
		db:                            deps.DB,
		resolver:                      deps.Resolver,
		syncer:                        deps.Syncer,
		clones:                        deps.Clones,
		workspaces:                    deps.Workspaces,
		deleteWorkspace:               deps.DeleteWorkspace,
		now:                           now,
		fleetSelfKey:                  deps.FleetSelfKey,
		filterRepos:                   deps.FilterRepos,
		repoOperations:                deps.RepoOperations,
		repoOperationsForMergeRequest: deps.RepoOperationsForMergeRequest,
		enqueueDetailSyncRerun:        deps.EnqueueDetailSyncOrRerun,
		broadcast:                     deps.Broadcast,
		markClosedNotifications:       deps.MarkClosedLinkedNotificationsDone,
		config:                        deps.Config,
		bgCtx:                         bgCtx,
		bgCancel:                      bgCancel,
		shutdownDone:                  make(chan struct{}),
		deferredMergeInFlight:         make(map[string]*deferredMergeHandle),
		deferredMergeMaxWait:          maxWait,
	}
}

func (s *Handler) markClosedLinkedNotificationsDone(ctx context.Context) {
	if s.markClosedNotifications != nil {
		s.markClosedNotifications(ctx)
	}
}

func (s *Handler) enqueueDetailSyncOrRerun(
	key string,
	attrs []any,
	fn func(context.Context) error,
) bool {
	if s.enqueueDetailSyncRerun == nil {
		return false
	}
	return s.enqueueDetailSyncRerun(key, attrs, fn)
}

func (s *Handler) publish(event Event) uint64 {
	if s.broadcast == nil {
		return 0
	}
	return s.broadcast(event)
}

func (s *Handler) selfFleetKey(hostKey string) string {
	if s.fleetSelfKey == nil {
		return ""
	}
	return s.fleetSelfKey(hostKey)
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

func (s *Handler) allowMidStackMerges() bool {
	return s.ConfigSnapshot().AllowMidStackMerges
}

func (s *Handler) runBackground(fn func(context.Context)) bool {
	s.bgMu.Lock()
	if s.stopping {
		s.bgMu.Unlock()
		return false
	}
	s.bgWG.Add(1)
	s.bgMu.Unlock()
	go func() {
		defer s.bgWG.Done()
		fn(s.bgCtx)
	}()
	return true
}

// Stop closes admission for new Pull background work and cancels active
// workers. It is idempotent and does not wait for workers to return.
func (s *Handler) Stop() {
	s.bgMu.Lock()
	if !s.stopping {
		s.stopping = true
		s.bgCancel()
		go func() {
			s.bgWG.Wait()
			close(s.shutdownDone)
		}()
	}
	s.bgMu.Unlock()
}

// Shutdown starts Pull shutdown if necessary and waits for active workers
// within the caller's context. A later call can retry the wait with a longer
// context after an earlier timeout.
func (s *Handler) Shutdown(ctx context.Context) error {
	s.Stop()
	s.bgMu.Lock()
	done := s.shutdownDone
	s.bgMu.Unlock()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Handler) Register(api huma.API) {
	pullRepoPath := "/pulls/{provider}/{owner}/{name}"
	hostPullRepoPath := "/host/{platform_host}/pulls/{provider}/{owner}/{name}"
	pullPath := pullRepoPath + "/{number}"
	hostPullPath := hostPullRepoPath + "/{number}"

	huma.Get(api, "/pulls", s.listPulls,
		httpapi.DocumentOperation("list-pulls", "List pull requests", "Pull Requests"))
	huma.Get(api, pullPath, s.getPull,
		httpapi.DocumentOperation("get-pull", "Get pull request", "Pull Requests"))
	huma.Get(api, hostPullPath, s.getPullOnHost,
		httpapi.DocumentOperation("get-pull-on-host", "Get pull request", "Pull Requests"))
	huma.Get(api, pullPath+"/import-metadata", s.getMRImportMetadata,
		httpapi.DocumentOperation("get-pull-import-metadata", "Get pull request import metadata", "Pull Requests"))
	huma.Get(api, hostPullPath+"/import-metadata", s.getMRImportMetadataOnHost,
		httpapi.DocumentOperation("get-pull-import-metadata-on-host", "Get pull request import metadata", "Pull Requests"))

	registerPullMutationRoutes(api, pullPath, hostPullPath, s)

	huma.Get(api, pullPath+"/commits", s.getCommits,
		httpapi.DocumentOperation("get-pull-commits", "Get pull request commits", "Pull Requests"))
	huma.Get(api, hostPullPath+"/commits", s.getCommitsOnHost,
		httpapi.DocumentOperation("get-pull-commits-on-host", "Get pull request commits", "Pull Requests"))
	huma.Get(api, pullPath+"/diff", s.getDiff,
		httpapi.DocumentOperation("get-pull-diff", "Get pull request diff", "Pull Requests"))
	huma.Get(api, hostPullPath+"/diff", s.getDiffOnHost,
		httpapi.DocumentOperation("get-pull-diff-on-host", "Get pull request diff", "Pull Requests"))
	huma.Get(api, pullPath+"/files", s.getFiles,
		httpapi.DocumentOperation("get-pull-files", "Get pull request files", "Pull Requests"))
	huma.Get(api, hostPullPath+"/files", s.getFilesOnHost,
		httpapi.DocumentOperation("get-pull-files-on-host", "Get pull request files", "Pull Requests"))
	huma.Get(api, pullPath+"/file-preview", s.getFilePreview,
		httpapi.DocumentOperation("get-pull-file-preview", "Get pull request file preview", "Pull Requests"))
	huma.Get(api, hostPullPath+"/file-preview", s.getFilePreviewOnHost,
		httpapi.DocumentOperation("get-pull-file-preview-on-host", "Get pull request file preview", "Pull Requests"))
	huma.Get(api, pullPath+"/stack", s.getStackForPR,
		httpapi.DocumentOperation("get-pull-stack", "Get pull request stack", "Pull Requests"))
	huma.Get(api, hostPullPath+"/stack", s.getStackForPROnHost,
		httpapi.DocumentOperation("get-pull-stack-on-host", "Get pull request stack", "Pull Requests"))
	huma.Get(api, "/stacks", s.listStacks,
		httpapi.DocumentOperation("list-stacks", "List stacks", "Stacks"))
}

func registerPullMutationRoutes(api huma.API, pullPath, hostPullPath string, s *Handler) {
	register(api, "set-kanban-state", http.MethodPut, pullPath+"/state", "Set pull request kanban state", s.setKanbanState)
	register(api, "set-kanban-state-on-host", http.MethodPut, hostPullPath+"/state", "Set pull request kanban state", s.setKanbanStateOnHost)
	register(api, "edit-pr-content", http.MethodPatch, pullPath, "Edit pull request content", s.editPRContent)
	register(api, "edit-pr-content-on-host", http.MethodPatch, hostPullPath, "Edit pull request content", s.editPRContentOnHost)
	register(api, "post-pr-comment", http.MethodPost, pullPath+"/comments", "Post pull request comment", s.postComment)
	register(api, "post-pr-comment-on-host", http.MethodPost, hostPullPath+"/comments", "Post pull request comment", s.postCommentOnHost)
	register(api, "edit-pr-comment", http.MethodPatch, pullPath+"/comments/{comment_id}", "Edit pull request comment", s.editComment)
	register(api, "edit-pr-comment-on-host", http.MethodPatch, hostPullPath+"/comments/{comment_id}", "Edit pull request comment", s.editCommentOnHost)
	register(api, "delete-pr-comment", http.MethodDelete, pullPath+"/comments/{comment_id}", "Delete pull request comment", s.deleteComment)
	register(api, "delete-pr-comment-on-host", http.MethodDelete, hostPullPath+"/comments/{comment_id}", "Delete pull request comment", s.deleteCommentOnHost)
	register(api, "reply-to-discussion", http.MethodPost, pullPath+"/discussions/{discussion_id}/reply", "Reply to pull request discussion", s.replyToDiscussion)
	register(api, "reply-to-discussion-on-host", http.MethodPost, hostPullPath+"/discussions/{discussion_id}/reply", "Reply to pull request discussion", s.replyToDiscussionOnHost)
	register(api, "resolve-discussion", http.MethodPost, pullPath+"/discussions/{discussion_id}/resolve", "Resolve pull request discussion", s.resolveDiscussion)
	register(api, "resolve-discussion-on-host", http.MethodPost, hostPullPath+"/discussions/{discussion_id}/resolve", "Resolve pull request discussion", s.resolveDiscussionOnHost)
	register(api, "set-pr-labels", http.MethodPut, pullPath+"/labels", "Set pull request labels", s.setPullLabels)
	register(api, "set-pr-labels-on-host", http.MethodPut, hostPullPath+"/labels", "Set pull request labels", s.setPullLabelsOnHost)
	register(api, "set-pr-assignees", http.MethodPut, pullPath+"/assignees", "Set pull request assignees", s.setPullAssignees)
	register(api, "set-pr-assignees-on-host", http.MethodPut, hostPullPath+"/assignees", "Set pull request assignees", s.setPullAssigneesOnHost)
	register(api, "set-pr-reviewers", http.MethodPut, pullPath+"/reviewers", "Set pull request reviewers", s.setPullReviewers)
	register(api, "set-pr-reviewers-on-host", http.MethodPut, hostPullPath+"/reviewers", "Set pull request reviewers", s.setPullReviewersOnHost)
	register(api, "approve-pull", http.MethodPost, pullPath+"/approve", "Approve pull request", s.approvePR)
	register(api, "approve-pull-on-host", http.MethodPost, hostPullPath+"/approve", "Approve pull request", s.approvePROnHost)
	register(api, "request-pull-changes", http.MethodPost, pullPath+"/request-changes", "Request pull request changes", s.requestChangesPR)
	register(api, "request-pull-changes-on-host", http.MethodPost, hostPullPath+"/request-changes", "Request pull request changes", s.requestChangesPROnHost)
	register(api, "approve-pull-workflows", http.MethodPost, pullPath+"/approve-workflows", "Approve pull request workflows", s.approveWorkflows)
	register(api, "approve-pull-workflows-on-host", http.MethodPost, hostPullPath+"/approve-workflows", "Approve pull request workflows", s.approveWorkflowsOnHost)
	register(api, "mark-pull-ready-for-review", http.MethodPost, pullPath+"/ready-for-review", "Mark pull request ready for review", s.readyForReview)
	register(api, "mark-pull-ready-for-review-on-host", http.MethodPost, hostPullPath+"/ready-for-review", "Mark pull request ready for review", s.readyForReviewOnHost)
	register(api, "merge-pull", http.MethodPost, pullPath+"/merge", "Merge pull request", s.mergePR)
	register(api, "merge-pull-on-host", http.MethodPost, hostPullPath+"/merge", "Merge pull request", s.mergePROnHost)
	register(api, "defer-merge-pull", http.MethodPost, pullPath+"/merge/deferred", "Defer pull request merge until pending CI passes", s.deferMergePR)
	register(api, "defer-merge-pull-on-host", http.MethodPost, hostPullPath+"/merge/deferred", "Defer pull request merge until pending CI passes", s.deferMergePROnHost)
	register(api, "set-pr-github-state", http.MethodPost, pullPath+"/github-state", "Set pull request GitHub state", s.setPRGitHubState)
	register(api, "set-pr-github-state-on-host", http.MethodPost, hostPullPath+"/github-state", "Set pull request GitHub state", s.setPRGitHubStateOnHost)
	registerReviewDraftRoutes(api, pullPath, hostPullPath, s)
}

func registerReviewDraftRoutes(api huma.API, pullPath, hostPullPath string, s *Handler) {
	register(api, "get-pr-review-draft", http.MethodGet, pullPath+"/review-draft", "Review pull request diff", s.getDiffReviewDraft)
	register(api, "get-pr-review-draft-on-host", http.MethodGet, hostPullPath+"/review-draft", "Review pull request diff", s.getDiffReviewDraftOnHost)
	register(api, "create-pr-review-draft-comment", http.MethodPost, pullPath+"/review-draft/comments", "Create pull request review draft comment", s.createDiffReviewDraftComment)
	register(api, "create-pr-review-draft-comment-on-host", http.MethodPost, hostPullPath+"/review-draft/comments", "Create pull request review draft comment", s.createDiffReviewDraftCommentOnHost)
	register(api, "edit-pr-review-draft-comment", http.MethodPatch, pullPath+"/review-draft/comments/{draft_comment_id}", "Review pull request diff", s.editDiffReviewDraftComment)
	register(api, "edit-pr-review-draft-comment-on-host", http.MethodPatch, hostPullPath+"/review-draft/comments/{draft_comment_id}", "Review pull request diff", s.editDiffReviewDraftCommentOnHost)
	register(api, "delete-pr-review-draft-comment", http.MethodDelete, pullPath+"/review-draft/comments/{draft_comment_id}", "Review pull request diff", s.deleteDiffReviewDraftComment)
	register(api, "delete-pr-review-draft-comment-on-host", http.MethodDelete, hostPullPath+"/review-draft/comments/{draft_comment_id}", "Review pull request diff", s.deleteDiffReviewDraftCommentOnHost)
	register(api, "publish-pr-review-draft", http.MethodPost, pullPath+"/review-draft/publish", "Review pull request diff", s.publishDiffReviewDraft)
	register(api, "publish-pr-review-draft-on-host", http.MethodPost, hostPullPath+"/review-draft/publish", "Review pull request diff", s.publishDiffReviewDraftOnHost)
	register(api, "discard-pr-review-draft", http.MethodDelete, pullPath+"/review-draft", "Review pull request diff", s.discardDiffReviewDraft)
	register(api, "discard-pr-review-draft-on-host", http.MethodDelete, hostPullPath+"/review-draft", "Review pull request diff", s.discardDiffReviewDraftOnHost)
	register(api, "apply-pr-review-suggestions", http.MethodPost, pullPath+"/review-suggestions/apply", "Apply pull request review suggestions", s.applyReviewSuggestions)
	register(api, "apply-pr-review-suggestions-on-host", http.MethodPost, hostPullPath+"/review-suggestions/apply", "Apply pull request review suggestions", s.applyReviewSuggestionsOnHost)
	register(api, "resolve-pr-review-thread", http.MethodPost, pullPath+"/review-threads/{thread_id}/resolve", "Review pull request diff", s.resolveDiffReviewThread)
	register(api, "resolve-pr-review-thread-on-host", http.MethodPost, hostPullPath+"/review-threads/{thread_id}/resolve", "Review pull request diff", s.resolveDiffReviewThreadOnHost)
	register(api, "unresolve-pr-review-thread", http.MethodPost, pullPath+"/review-threads/{thread_id}/unresolve", "Review pull request diff", s.unresolveDiffReviewThread)
	register(api, "unresolve-pr-review-thread-on-host", http.MethodPost, hostPullPath+"/review-threads/{thread_id}/unresolve", "Review pull request diff", s.unresolveDiffReviewThreadOnHost)
}

func register[I, O any](
	api huma.API,
	operationID, method, path, summary string,
	handler func(context.Context, *I) (*O, error),
) {
	status := http.StatusOK
	if operationID == "post-pr-comment" ||
		operationID == "post-pr-comment-on-host" ||
		operationID == "reply-to-discussion" ||
		operationID == "reply-to-discussion-on-host" ||
		operationID == "create-pr-review-draft-comment" ||
		operationID == "create-pr-review-draft-comment-on-host" {
		status = http.StatusCreated
	}
	if operationID == "delete-pr-comment" ||
		operationID == "delete-pr-comment-on-host" {
		status = http.StatusNoContent
	}
	if operationID == "defer-merge-pull" ||
		operationID == "defer-merge-pull-on-host" {
		status = http.StatusAccepted
	}
	huma.Register(api, huma.Operation{
		OperationID:   operationID,
		Method:        method,
		Path:          path,
		DefaultStatus: status,
		Summary:       summary,
		Tags:          []string{"Pull Requests"},
	}, handler)
}
