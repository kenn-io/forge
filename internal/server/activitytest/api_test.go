package activitytest

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	gh "github.com/google/go-github/v91/github"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/apiclient"
	"go.kenn.io/forge/internal/apiclient/generated"
	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/gitclone"
	ghclient "go.kenn.io/forge/internal/github"
	"go.kenn.io/forge/internal/platformdb"
	"go.kenn.io/forge/internal/server"
	"go.kenn.io/forge/internal/testutil"
	"go.kenn.io/forge/internal/testutil/dbtest"
	"go.kenn.io/forge/internal/testutil/gitfixture"
	"go.kenn.io/forge/internal/testutil/gitsafe"
	"go.kenn.io/forge/internal/testutil/processjob"
	"go.kenn.io/forge/internal/testutil/testsignal"
	"go.kenn.io/forge/internal/testutil/testtmux"
	"go.kenn.io/forge/platform"
	platformgithub "go.kenn.io/forge/platform/github"
	"golang.org/x/sync/semaphore"
)

const (
	serverRuntimeHelperMarker        = "kenn-forge-runtime-helper"
	serverPtyOwnerParentHelperMarker = "kenn-forge-pty-owner-parent-helper"
)

var privateTmuxOwner *testtmux.Owner

var parallelServerTestSlots = semaphore.NewWeighted(4)

var // Bound Git-heavy root-package tests independently from the workspacetest
// binary.
rootWorkspaceGitSemaphore = semaphore.NewWeighted(2)

func TestMain(m *testing.M) {
	if code, ok := testtmux.CommandWrapperExitCode(); ok {
		os.Exit(code)
	}
	if isServerHelperProcess() {
		os.Exit(m.Run())
	}
	if err := processjob.ContainCurrentProcessTree(); err != nil {
		fmt.Fprintf(os.Stderr, "contain server test process tree: %v\n", err)
		os.Exit(1)
	}
	if testtmux.Supported() {
		var ownerErr error
		privateTmuxOwner, ownerErr = testtmux.New()
		if ownerErr != nil {
			fmt.Fprintf(os.Stderr, "initialize private test tmux owner: %v\n", ownerErr)
			os.Exit(1)
		}
	}
	envDir, envDirErr := os.MkdirTemp("", "kenn-forge-server-tmux-env-*")
	if envDirErr == nil {
		_ = os.Setenv("KENN_FORGE_TMUX_ENV_DIR", envDir)
	}
	runCleanup, stopSignalCleanup := testsignal.Install(func() error {
		return cleanupServerTestTmux(privateTmuxOwner)
	}, func(err error) {
		fmt.Fprintf(os.Stderr, "cleanup kenn-forge test tmux sessions: %v\n", err)
	})
	code := gitsafe.RunIsolatedMain(m)
	if err := runCleanup(); err != nil {
		fmt.Fprintf(os.Stderr, "cleanup kenn-forge test tmux sessions: %v\n", err)
		if code == 0 {
			code = 1
		}
	}
	stopSignalCleanup()
	if envDirErr == nil {
		_ = os.RemoveAll(envDir)
	}
	os.Exit(code)
}

func isServerHelperProcess() bool {
	if os.Getenv("KENN_FORGE_SERVER_RUNTIME_HELPER") == "1" ||
		os.Getenv("KENN_FORGE_SERVER_PTY_OWNER_HELPER") == "1" {
		return true
	}
	args := os.Args
	if sep := slices.Index(args, "--"); sep >= 0 {
		args = args[sep+1:]
	}
	return len(args) > 0 &&
		(args[0] == serverRuntimeHelperMarker ||
			args[0] == serverPtyOwnerParentHelperMarker ||
			args[0] == "pty-owner")
}

func runParallelServerTest(t *testing.T) {
	t.Helper()
	t.Parallel()
	require.NoError(t, parallelServerTestSlots.Acquire(t.Context(), 1))
	t.Cleanup(func() { parallelServerTestSlots.Release(1) })
}

func acquireRootWorkspaceGitSlot(t *testing.T) {
	t.Helper()
	require.NoError(t, rootWorkspaceGitSemaphore.Acquire(t.Context(), 1))
	t.Cleanup(func() { rootWorkspaceGitSemaphore.Release(1) })
}

func cleanupContext(t *testing.T) (context.Context, context.CancelFunc) {
	t.Helper()
	return context.WithTimeout(context.Background(), 15*time.Second)
}

func gracefulShutdown(t *testing.T, srv interface{ Shutdown(context.Context) error }) {
	t.Helper()
	ctx, cancel := cleanupContext(t)
	defer cancel()
	require.NoError(t, srv.Shutdown(ctx))
}

// mockGH implements ghclient.Client for testing.
type mockGHNativeStackAPI struct {
	listOpenPullRequests func(
		context.Context, string, string,
	) ([]*gh.PullRequest, map[int]*platformgithub.NativeStackHint, error)
	listStackPage func(
		context.Context, string, string, int,
	) (platformgithub.NativeStackPage, error)
}

type mockGH struct {
	getRepositoryFn            func(context.Context, string, string) (*gh.Repository, error)
	getPullRequestFn           func(context.Context, string, string, int) (*gh.PullRequest, error)
	getPullRequestIfChangedFn  func(context.Context, string, string, int, string) (*gh.PullRequest, string, bool, error)
	getIssueFn                 func(context.Context, string, string, int) (*gh.Issue, error)
	getIssueIfChangedFn        func(context.Context, string, string, int, string) (*gh.Issue, string, bool, error)
	createIssueFn              func(context.Context, string, string, string, string) (*gh.Issue, error)
	getUserFn                  func(context.Context, string) (*gh.User, error)
	authenticatedViewerLoginFn func(context.Context) (string, error)
	authenticatedViewerCalls   int
	markReadyForReviewFn       func(context.Context, string, string, int) (*gh.PullRequest, error)
	convertToDraftFn           func(context.Context, string, string, int) (*gh.PullRequest, error)
	dismissReviewFn            func(context.Context, string, string, int, int64, string) (*gh.PullRequestReview, error)
	editPullRequestFn          func(context.Context, string, string, int, platformgithub.EditPullRequestOpts) (*gh.PullRequest, error)
	editIssueFn                func(context.Context, string, string, int, string) (*gh.Issue, error)
	editIssueContentFn         func(context.Context, string, string, int, *string, *string) (*gh.Issue, error)
	createIssueCommentFn       func(context.Context, string, string, int, string) (*gh.IssueComment, error)
	editIssueCommentFn         func(context.Context, string, string, int64, string) (*gh.IssueComment, error)
	deleteIssueCommentFn       func(context.Context, string, string, int64) error
	createReviewCommentReplyFn func(context.Context, string, string, int, string, int64) (*gh.PullRequestComment, error)
	createReviewFn             func(context.Context, string, string, int, string, string) (*gh.PullRequestReview, error)
	createReviewWithCommentsFn func(context.Context, string, string, int, string, string, string, []*gh.DraftReviewComment) (*gh.PullRequestReview, error)
	applyReviewSuggestionsFn   func(context.Context, string, string, int, platform.ApplyReviewSuggestionsInput) (*platform.AppliedReviewSuggestions, error)
	mergePullRequestFn         func(context.Context, string, string, int, string, string, string) (*gh.PullRequestMergeResult, error)
	listWorkflowRunsForHeadFn  func(context.Context, string, string, string) ([]*gh.WorkflowRun, error)
	approveWorkflowRunFn       func(context.Context, string, string, int64) error
	listReposByOwnerFn         func(context.Context, string) ([]*gh.Repository, error)
	listReleasesFn             func(context.Context, string, string, int) ([]*gh.RepositoryRelease, error)
	listTagsFn                 func(context.Context, string, string, int) ([]*gh.RepositoryTag, error)
	listOpenPullRequestsFn     func(context.Context, string, string) ([]*gh.PullRequest, error)
	nativeStackAPI             *mockGHNativeStackAPI
	listPullRequestsPageFn     func(context.Context, string, string, string, int) ([]*gh.PullRequest, bool, error)
	listIssuesPageFn           func(context.Context, string, string, string, int) ([]*gh.Issue, bool, error)
	listCheckRunsForRefFn      func(context.Context, string, string, string) ([]*gh.CheckRun, error)
	getCombinedStatusFn        func(context.Context, string, string, string) (*gh.CombinedStatus, error)
	listPRTimelineEventsFn     func(context.Context, string, string, int) ([]platformgithub.PullRequestTimelineEvent, error)
	listOpenPRsErr             error
	listOpenIssuesFn           func(context.Context, string, string) ([]*gh.Issue, error)
	listIssueCommentsFn        func(context.Context, string, string, int) ([]*gh.IssueComment, error)
	listReviewThreadsFn        func(context.Context, string, string, int) ([]platformgithub.PullRequestReviewThread, error)
	rateLimitSnapshotFn        func(context.Context) (*platformgithub.RateLimitSnapshot, error)
	rateLimitSnapshotCalls     int
	listIssueCommentsErr       error
	listNotificationsFn        func(context.Context, ghclient.NotificationListOptions) ([]ghclient.NotificationThread, bool, error)
	markNotificationReadFn     func(context.Context, string) error
	getMarkdownImageFn         func(context.Context, string, string, string) (platform.MarkdownImage, error)
}

func (m *mockGH) ListOpenPullRequests(ctx context.Context, owner, repo string) ([]*gh.PullRequest, error) {
	if m.listOpenPullRequestsFn != nil {
		return m.listOpenPullRequestsFn(ctx, owner, repo)
	}
	if m.listOpenPRsErr != nil {
		return nil, m.listOpenPRsErr
	}
	return nil, nil
}

func (m *mockGH) ListOpenPullRequestsWithNativeStackHints(
	ctx context.Context, owner, repo string,
) ([]*gh.PullRequest, map[int]*platformgithub.NativeStackHint, error) {
	if m.nativeStackAPI != nil && m.nativeStackAPI.listOpenPullRequests != nil {
		return m.nativeStackAPI.listOpenPullRequests(ctx, owner, repo)
	}
	prs, err := m.ListOpenPullRequests(ctx, owner, repo)
	return prs, nil, err
}

func (m *mockGH) ListNativeStacksPage(
	ctx context.Context, owner, repo string, page int,
) (platformgithub.NativeStackPage, error) {
	if m.nativeStackAPI != nil && m.nativeStackAPI.listStackPage != nil {
		return m.nativeStackAPI.listStackPage(ctx, owner, repo, page)
	}
	return platformgithub.NativeStackPage{}, nil
}

func (m *mockGH) ListOpenIssues(ctx context.Context, owner, repo string) ([]*gh.Issue, error) {
	if m.listOpenIssuesFn != nil {
		return m.listOpenIssuesFn(ctx, owner, repo)
	}
	return nil, nil
}

func (m *mockGH) GetIssue(ctx context.Context, owner, repo string, number int) (*gh.Issue, error) {
	if m.getIssueFn != nil {
		return m.getIssueFn(ctx, owner, repo, number)
	}
	return nil, nil
}

func (m *mockGH) GetIssueIfChanged(
	ctx context.Context,
	owner, repo string,
	number int,
	etag string,
) (*gh.Issue, string, bool, error) {
	if m.getIssueIfChangedFn != nil {
		return m.getIssueIfChangedFn(ctx, owner, repo, number, etag)
	}
	issue, err := m.GetIssue(ctx, owner, repo, number)
	return issue, "", false, err
}

func (m *mockGH) CreateIssue(
	ctx context.Context, owner, repo, title, body string,
) (*gh.Issue, error) {
	if m.createIssueFn != nil {
		return m.createIssueFn(ctx, owner, repo, title, body)
	}
	number := 1
	now := gh.Timestamp{Time: time.Now().UTC()}
	state := "open"
	htmlURL := fmt.Sprintf("https://github.com/%s/%s/issues/%d", owner, repo, number)
	login := "fixture-bot"
	return &gh.Issue{
		Number:    &number,
		Title:     &title,
		Body:      &body,
		State:     &state,
		HTMLURL:   &htmlURL,
		User:      &gh.User{Login: &login},
		CreatedAt: &now,
		UpdatedAt: &now,
	}, nil
}

func (m *mockGH) GetUser(ctx context.Context, login string) (*gh.User, error) {
	if m.getUserFn != nil {
		return m.getUserFn(ctx, login)
	}
	return &gh.User{Login: &login}, nil
}

func (m *mockGH) AuthenticatedViewerLogin(ctx context.Context) (string, error) {
	m.authenticatedViewerCalls++
	if m.authenticatedViewerLoginFn != nil {
		return m.authenticatedViewerLoginFn(ctx)
	}
	return "", nil
}

func (m *mockGH) GetRateLimitSnapshot(ctx context.Context) (*platformgithub.RateLimitSnapshot, error) {
	m.rateLimitSnapshotCalls++
	if m.rateLimitSnapshotFn != nil {
		return m.rateLimitSnapshotFn(ctx)
	}
	return nil, nil
}

func (m *mockGH) ListRepositoriesByOwner(
	ctx context.Context, owner string,
) ([]*gh.Repository, error) {
	if m.listReposByOwnerFn != nil {
		return m.listReposByOwnerFn(ctx, owner)
	}
	return nil, nil
}

func (m *mockGH) ListReleases(
	ctx context.Context, owner, repo string, perPage int,
) ([]*gh.RepositoryRelease, error) {
	if m.listReleasesFn != nil {
		return m.listReleasesFn(ctx, owner, repo, perPage)
	}
	return nil, nil
}

func (m *mockGH) ListTags(
	ctx context.Context, owner, repo string, perPage int,
) ([]*gh.RepositoryTag, error) {
	if m.listTagsFn != nil {
		return m.listTagsFn(ctx, owner, repo, perPage)
	}
	return nil, nil
}

func (m *mockGH) GetPullRequest(ctx context.Context, owner, repo string, number int) (*gh.PullRequest, error) {
	if m.getPullRequestFn != nil {
		return m.getPullRequestFn(ctx, owner, repo, number)
	}
	return nil, nil
}

func (m *mockGH) GetPullRequestIfChanged(
	ctx context.Context,
	owner, repo string,
	number int,
	etag string,
) (*gh.PullRequest, string, bool, error) {
	if m.getPullRequestIfChangedFn != nil {
		return m.getPullRequestIfChangedFn(ctx, owner, repo, number, etag)
	}
	pr, err := m.GetPullRequest(ctx, owner, repo, number)
	return pr, "", false, err
}

func (m *mockGH) ListIssueComments(
	ctx context.Context, owner, repo string, number int,
) ([]*gh.IssueComment, error) {
	if m.listIssueCommentsFn != nil {
		return m.listIssueCommentsFn(ctx, owner, repo, number)
	}
	if m.listIssueCommentsErr != nil {
		return nil, m.listIssueCommentsErr
	}
	return nil, nil
}

func (m *mockGH) ListIssueCommentsIfChanged(
	ctx context.Context, owner, repo string, number int,
) ([]*gh.IssueComment, error) {
	if m.listIssueCommentsFn == nil && m.listIssueCommentsErr == nil {
		return nil, &gh.ErrorResponse{
			Response: &http.Response{StatusCode: http.StatusNotModified},
		}
	}
	return m.ListIssueComments(ctx, owner, repo, number)
}

func (m *mockGH) ListReviews(
	_ context.Context, _, _ string, _ int,
) ([]*gh.PullRequestReview, error) {
	return nil, nil
}

func (m *mockGH) ListPullRequestReviewThreads(
	ctx context.Context,
	owner string,
	repo string,
	number int,
) ([]platformgithub.PullRequestReviewThread, error) {
	if m.listReviewThreadsFn != nil {
		return m.listReviewThreadsFn(ctx, owner, repo, number)
	}
	return nil, nil
}

func (m *mockGH) ListCommits(
	_ context.Context, _, _ string, _ int,
) ([]*gh.RepositoryCommit, error) {
	return nil, nil
}

func (m *mockGH) ListForcePushEvents(
	_ context.Context, _, _ string, _ int,
) ([]platformgithub.ForcePushEvent, error) {
	return nil, nil
}

func (m *mockGH) ListPullRequestTimelineEvents(
	ctx context.Context, owner, repo string, number int,
) ([]platformgithub.PullRequestTimelineEvent, error) {
	if m.listPRTimelineEventsFn != nil {
		return m.listPRTimelineEventsFn(ctx, owner, repo, number)
	}
	return nil, nil
}

func (m *mockGH) GetCombinedStatus(
	ctx context.Context, owner, repo, ref string,
) (*gh.CombinedStatus, error) {
	if m.getCombinedStatusFn != nil {
		return m.getCombinedStatusFn(ctx, owner, repo, ref)
	}
	return nil, nil
}

func (m *mockGH) ListCheckRunsForRef(
	ctx context.Context, owner, repo, ref string,
) ([]*gh.CheckRun, error) {
	if m.listCheckRunsForRefFn != nil {
		return m.listCheckRunsForRefFn(ctx, owner, repo, ref)
	}
	return nil, nil
}

func (m *mockGH) ListWorkflowRunsForHeadSHA(
	ctx context.Context, owner, repo, headSHA string,
) ([]*gh.WorkflowRun, error) {
	if m.listWorkflowRunsForHeadFn != nil {
		return m.listWorkflowRunsForHeadFn(ctx, owner, repo, headSHA)
	}
	return nil, nil
}

func (m *mockGH) ApproveWorkflowRun(
	ctx context.Context, owner, repo string, runID int64,
) error {
	if m.approveWorkflowRunFn != nil {
		return m.approveWorkflowRunFn(ctx, owner, repo, runID)
	}
	return nil
}

func (m *mockGH) CreateIssueComment(
	ctx context.Context, owner, repo string, number int, body string,
) (*gh.IssueComment, error) {
	if m.createIssueCommentFn != nil {
		return m.createIssueCommentFn(ctx, owner, repo, number, body)
	}
	id := int64(42)
	return &gh.IssueComment{
		ID:   &id,
		Body: &body,
	}, nil
}

func (m *mockGH) EditIssueComment(
	ctx context.Context, owner, repo string, commentID int64, body string,
) (*gh.IssueComment, error) {
	if m.editIssueCommentFn != nil {
		return m.editIssueCommentFn(ctx, owner, repo, commentID, body)
	}
	login := "fixture-bot"
	now := gh.Timestamp{Time: time.Now().UTC()}
	return &gh.IssueComment{
		ID:        &commentID,
		Body:      &body,
		User:      &gh.User{Login: &login},
		CreatedAt: &now,
		UpdatedAt: &now,
	}, nil
}

func (m *mockGH) DeleteIssueComment(
	ctx context.Context, owner, repo string, commentID int64,
) error {
	if m.deleteIssueCommentFn != nil {
		return m.deleteIssueCommentFn(ctx, owner, repo, commentID)
	}
	return nil
}

func (m *mockGH) CreatePullRequestReviewCommentReply(
	ctx context.Context, owner, repo string, number int, body string, commentID int64,
) (*gh.PullRequestComment, error) {
	if m.createReviewCommentReplyFn != nil {
		return m.createReviewCommentReplyFn(ctx, owner, repo, number, body, commentID)
	}
	id := commentID + 1
	login := "fixture-bot"
	now := gh.Timestamp{Time: time.Now().UTC()}
	return &gh.PullRequestComment{
		ID:        &id,
		Body:      &body,
		User:      &gh.User{Login: &login},
		CreatedAt: &now,
	}, nil
}

func (m *mockGH) GetRepository(
	ctx context.Context, owner, repo string,
) (*gh.Repository, error) {
	if m.getRepositoryFn != nil {
		return m.getRepositoryFn(ctx, owner, repo)
	}
	nodeID := "repo-" + owner + "-" + repo
	return &gh.Repository{
		Name:     &repo,
		NodeID:   &nodeID,
		Owner:    &gh.User{Login: &owner},
		Archived: new(false),
	}, nil
}

func (m *mockGH) CreateReview(
	ctx context.Context, owner, repo string, number int, event string, body string,
) (*gh.PullRequestReview, error) {
	if m.createReviewFn != nil {
		return m.createReviewFn(ctx, owner, repo, number, event, body)
	}
	id := int64(99)
	state := "APPROVED"
	return &gh.PullRequestReview{ID: &id, State: &state}, nil
}

func (m *mockGH) CreateReviewWithComments(
	ctx context.Context,
	owner, repo string,
	number int,
	event string,
	body string,
	commitID string,
	comments []*gh.DraftReviewComment,
) (*gh.PullRequestReview, error) {
	if m.createReviewWithCommentsFn != nil {
		return m.createReviewWithCommentsFn(ctx, owner, repo, number, event, body, commitID, comments)
	}
	return m.CreateReview(ctx, owner, repo, number, event, body)
}

func (m *mockGH) ApplyReviewSuggestions(
	ctx context.Context,
	owner string,
	repo string,
	number int,
	input platform.ApplyReviewSuggestionsInput,
) (*platform.AppliedReviewSuggestions, error) {
	if m.applyReviewSuggestionsFn != nil {
		return m.applyReviewSuggestionsFn(ctx, owner, repo, number, input)
	}
	return &platform.AppliedReviewSuggestions{CommitSHA: "suggestion-commit-sha"}, nil
}

func (m *mockGH) DismissReview(
	ctx context.Context, owner, repo string, number int, reviewID int64, message string,
) (*gh.PullRequestReview, error) {
	if m.dismissReviewFn != nil {
		return m.dismissReviewFn(ctx, owner, repo, number, reviewID, message)
	}
	return &gh.PullRequestReview{ID: &reviewID}, nil
}

func (m *mockGH) MarkPullRequestReadyForReview(
	ctx context.Context, owner, repo string, number int,
) (*gh.PullRequest, error) {
	if m.markReadyForReviewFn != nil {
		return m.markReadyForReviewFn(ctx, owner, repo, number)
	}
	draft := false
	return &gh.PullRequest{Number: &number, Draft: &draft}, nil
}

func (m *mockGH) ConvertPullRequestToDraft(
	ctx context.Context, owner, repo string, number int,
) (*gh.PullRequest, error) {
	if m.convertToDraftFn != nil {
		return m.convertToDraftFn(ctx, owner, repo, number)
	}
	draft := true
	state := "open"
	return &gh.PullRequest{Number: &number, State: &state, Draft: &draft}, nil
}

func (m *mockGH) MergePullRequest(
	ctx context.Context, owner, repo string, number int,
	commitTitle, commitMessage, method, _ string,
) (*gh.PullRequestMergeResult, error) {
	if m.mergePullRequestFn != nil {
		return m.mergePullRequestFn(ctx, owner, repo, number, commitTitle, commitMessage, method)
	}
	merged := true
	sha := "abc123"
	msg := "merged"
	return &gh.PullRequestMergeResult{
		Merged: &merged, SHA: &sha, Message: &msg,
	}, nil
}

func (m *mockGH) EditPullRequest(
	ctx context.Context, owner, repo string, number int, opts platformgithub.EditPullRequestOpts,
) (*gh.PullRequest, error) {
	if m.editPullRequestFn != nil {
		return m.editPullRequestFn(ctx, owner, repo, number, opts)
	}
	pr := &gh.PullRequest{}
	if opts.State != nil {
		pr.State = opts.State
	}
	if opts.Title != nil {
		pr.Title = opts.Title
	}
	if opts.Body != nil {
		pr.Body = opts.Body
	}
	now := time.Now().UTC()
	ghTime := gh.Timestamp{Time: now}
	pr.UpdatedAt = &ghTime
	return pr, nil
}

func (m *mockGH) EditIssue(
	ctx context.Context, owner, repo string, number int, state string,
) (*gh.Issue, error) {
	if m.editIssueFn != nil {
		return m.editIssueFn(ctx, owner, repo, number, state)
	}
	return &gh.Issue{State: &state}, nil
}

func (m *mockGH) EditIssueContent(
	ctx context.Context, owner, repo string, number int, title *string, body *string,
) (*gh.Issue, error) {
	if m.editIssueContentFn != nil {
		return m.editIssueContentFn(ctx, owner, repo, number, title, body)
	}
	out := &gh.Issue{}
	if title != nil {
		out.Title = title
	}
	if body != nil {
		out.Body = body
	}
	return out, nil
}

func (m *mockGH) ListPullRequestsPage(
	ctx context.Context, owner, repo, state string, page int,
) ([]*gh.PullRequest, bool, error) {
	if m.listPullRequestsPageFn != nil {
		return m.listPullRequestsPageFn(ctx, owner, repo, state, page)
	}
	return nil, false, nil
}

func (m *mockGH) ListIssuesPage(
	ctx context.Context, owner, repo, state string, page int,
) ([]*gh.Issue, bool, error) {
	if m.listIssuesPageFn != nil {
		return m.listIssuesPageFn(ctx, owner, repo, state, page)
	}
	return nil, false, nil
}

func (m *mockGH) ListNotifications(ctx context.Context, opts ghclient.NotificationListOptions) ([]ghclient.NotificationThread, bool, error) {
	if m.listNotificationsFn != nil {
		return m.listNotificationsFn(ctx, opts)
	}
	return nil, false, nil
}

func (m *mockGH) MarkNotificationThreadRead(ctx context.Context, threadID string) error {
	if m.markNotificationReadFn != nil {
		return m.markNotificationReadFn(ctx, threadID)
	}
	return nil
}

func (m *mockGH) GetMarkdownImage(
	ctx context.Context,
	owner, repo, sourceURL string,
) (platform.MarkdownImage, error) {
	if m.getMarkdownImageFn != nil {
		return m.getMarkdownImageFn(ctx, owner, repo, sourceURL)
	}
	return platform.MarkdownImage{}, nil
}

// InvalidateListETagsForRepo is a no-op for the server test mock,
// which has no underlying HTTP cache.
func (m *mockGH) InvalidateListETagsForRepo(_, _ string, _ ...string) {}

type apiTestGitLabProvider struct {
	mu                               sync.Mutex
	ref                              platform.RepoRef
	capabilities                     *platform.Capabilities
	mergeRequests                    []platform.MergeRequest
	mergeRequestDetail               map[int]platform.MergeRequest
	mergeRequestEvents               map[int][]platform.MergeRequestEvent
	issues                           []platform.Issue
	issueEvents                      map[int][]platform.IssueEvent
	releases                         []platform.Release
	tags                             []platform.Tag
	ciChecks                         map[string][]platform.CICheck
	ciErr                            error
	reviewThreads                    []platform.MergeRequestReviewThread
	reviewThreadsErr                 error
	reviewThreadsFn                  func(context.Context, platform.RepoRef, int) ([]platform.MergeRequestReviewThread, error)
	publishedReviews                 []platform.PublishDiffReviewDraftInput
	publishReviewErr                 error
	appliedSuggestions               []platform.ApplyReviewSuggestionsInput
	applySuggestionsErr              error
	applySuggestionsErrAfterMutation error
	applySuggestionResult            *platform.AppliedReviewSuggestions
	applySuggestionReturnsNil        bool
	applySuggestionHead              string
	applySuggestionsStarted          chan struct{}
	applySuggestionsRelease          <-chan struct{}
	cancelAfterApply                 func()
	blockNextMRFetch                 atomic.Bool
	mrFetchStarted                   chan struct{}
	mrFetchRelease                   <-chan struct{}
	rateLimitBuckets                 map[platform.OperationName][]platform.RateLimitBucket
	resolvedThreads                  []string
	unresolvedThreads                []string
}

func (p *apiTestGitLabProvider) Platform() platform.Kind {
	return p.ref.Platform
}

func (p *apiTestGitLabProvider) Host() string {
	return p.ref.Host
}

func (p *apiTestGitLabProvider) Capabilities() platform.Capabilities {
	if p.capabilities != nil {
		return *p.capabilities
	}
	return platform.Capabilities{
		ReadRepositories:  true,
		ReadMergeRequests: true,
		ReadIssues:        true,
		ReadComments:      true,
		ReadReleases:      true,
		ReadCI:            true,
	}
}

func (p *apiTestGitLabProvider) OperationRateLimitBuckets(
	operation platform.OperationName,
) ([]platform.RateLimitBucket, bool) {
	if p.rateLimitBuckets == nil {
		return nil, false
	}
	buckets, ok := p.rateLimitBuckets[operation]
	return buckets, ok
}

func (p *apiTestGitLabProvider) PublishDiffReviewDraft(
	_ context.Context,
	_ platform.RepoRef,
	_ int,
	input platform.PublishDiffReviewDraftInput,
) (*platform.PublishedDiffReview, error) {
	p.publishedReviews = append(p.publishedReviews, input)
	return &platform.PublishedDiffReview{SubmittedAt: time.Now().UTC()}, p.publishReviewErr
}

func (p *apiTestGitLabProvider) ApplyReviewSuggestions(
	_ context.Context,
	_ platform.RepoRef,
	number int,
	input platform.ApplyReviewSuggestionsInput,
) (*platform.AppliedReviewSuggestions, error) {
	if p.applySuggestionsStarted != nil {
		p.applySuggestionsStarted <- struct{}{}
	}
	if p.applySuggestionsRelease != nil {
		<-p.applySuggestionsRelease
	}
	if p.applySuggestionsErr != nil {
		return nil, p.applySuggestionsErr
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.appliedSuggestions = append(p.appliedSuggestions, input)
	result := platform.AppliedReviewSuggestions{CommitSHA: "suggestion-commit-sha"}
	if p.applySuggestionResult != nil {
		result = *p.applySuggestionResult
	}
	providerHead := result.CommitSHA
	if p.applySuggestionHead != "" {
		providerHead = p.applySuggestionHead
	}
	for i := range p.mergeRequests {
		if p.mergeRequests[i].Number == number && providerHead != "" {
			p.mergeRequests[i].HeadSHA = providerHead
		}
	}
	if p.cancelAfterApply != nil {
		p.cancelAfterApply()
	}
	if p.applySuggestionsErrAfterMutation != nil {
		return nil, p.applySuggestionsErrAfterMutation
	}
	if p.applySuggestionReturnsNil {
		return nil, nil
	}
	return &result, nil
}

func (p *apiTestGitLabProvider) ListMergeRequestReviewThreads(
	ctx context.Context,
	repo platform.RepoRef,
	number int,
) ([]platform.MergeRequestReviewThread, error) {
	if p.reviewThreadsFn != nil {
		return p.reviewThreadsFn(ctx, repo, number)
	}
	if p.reviewThreadsErr != nil {
		return nil, p.reviewThreadsErr
	}
	return p.reviewThreads, nil
}

func (p *apiTestGitLabProvider) ResolveDiffReviewThread(
	_ context.Context,
	_ platform.RepoRef,
	_ int,
	providerThreadID string,
) error {
	p.resolvedThreads = append(p.resolvedThreads, providerThreadID)
	return nil
}

func (p *apiTestGitLabProvider) UnresolveDiffReviewThread(
	_ context.Context,
	_ platform.RepoRef,
	_ int,
	providerThreadID string,
) error {
	p.unresolvedThreads = append(p.unresolvedThreads, providerThreadID)
	return nil
}

func (p *apiTestGitLabProvider) GetRepository(
	context.Context,
	platform.RepoRef,
) (platform.Repository, error) {
	return platform.Repository{
		Ref:                p.ref,
		PlatformID:         p.ref.PlatformID,
		PlatformExternalID: p.ref.PlatformExternalID,
		DefaultBranch:      p.ref.DefaultBranch,
		WebURL:             p.ref.WebURL,
		CloneURL:           p.ref.CloneURL,
	}, nil
}

func (p *apiTestGitLabProvider) ListRepositories(
	context.Context,
	string,
	platform.RepositoryListOptions,
) ([]platform.Repository, error) {
	repo, err := p.GetRepository(context.Background(), p.ref)
	if err != nil {
		return nil, err
	}
	return []platform.Repository{repo}, nil
}

func (p *apiTestGitLabProvider) ListOpenMergeRequests(
	context.Context,
	platform.RepoRef,
) ([]platform.MergeRequest, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.mergeRequests), nil
}

func (p *apiTestGitLabProvider) GetMergeRequest(
	_ context.Context,
	_ platform.RepoRef,
	number int,
) (platform.MergeRequest, error) {
	p.mu.Lock()
	var found platform.MergeRequest
	foundOK := false
	if p.mergeRequestDetail != nil {
		if mr, ok := p.mergeRequestDetail[number]; ok {
			found = mr
			foundOK = true
		}
	}
	if !foundOK {
		for _, mr := range p.mergeRequests {
			if mr.Number == number {
				found = mr
				foundOK = true
				break
			}
		}
	}
	p.mu.Unlock()
	if foundOK {
		if p.blockNextMRFetch.CompareAndSwap(true, false) {
			if p.mrFetchStarted != nil {
				p.mrFetchStarted <- struct{}{}
			}
			if p.mrFetchRelease != nil {
				<-p.mrFetchRelease
			}
		}
		return found, nil
	}
	return platform.MergeRequest{}, fmt.Errorf("missing merge request %d", number)
}

func (p *apiTestGitLabProvider) ListMergeRequestEvents(
	_ context.Context,
	_ platform.RepoRef,
	number int,
) ([]platform.MergeRequestEvent, error) {
	return p.mergeRequestEvents[number], nil
}

func (p *apiTestGitLabProvider) ListOpenIssues(
	context.Context,
	platform.RepoRef,
) ([]platform.Issue, error) {
	return slices.Clone(p.issues), nil
}

func (p *apiTestGitLabProvider) GetIssue(
	_ context.Context,
	_ platform.RepoRef,
	number int,
) (platform.Issue, error) {
	for _, issue := range p.issues {
		if issue.Number == number {
			return issue, nil
		}
	}
	return platform.Issue{}, fmt.Errorf("missing issue %d", number)
}

func (p *apiTestGitLabProvider) ListIssueEvents(
	_ context.Context,
	_ platform.RepoRef,
	number int,
) ([]platform.IssueEvent, error) {
	return p.issueEvents[number], nil
}

func (p *apiTestGitLabProvider) ListReleases(
	context.Context,
	platform.RepoRef,
) ([]platform.Release, error) {
	return p.releases, nil
}

func (p *apiTestGitLabProvider) ListTags(
	context.Context,
	platform.RepoRef,
) ([]platform.Tag, error) {
	return p.tags, nil
}

func (p *apiTestGitLabProvider) ListCIChecks(
	_ context.Context,
	_ platform.RepoRef,
	sha string,
) ([]platform.CICheck, error) {
	if p.ciErr != nil {
		return nil, p.ciErr
	}
	return p.ciChecks[sha], nil
}

// setupTestServer opens a temp DB, builds a Server, and returns both.
func setupTestServer(t *testing.T) (*server.Server, *db.DB) {
	t.Helper()
	return setupTestServerWithMock(t, &mockGH{})
}

func setupTestServerWithMock(t *testing.T, mock *mockGH) (*server.Server, *db.DB) {
	t.Helper()
	return setupTestServerWithRepos(t, mock, defaultTestRepos)
}

var defaultTestRepos = []ghclient.RepoRef{
	{
		Platform:           "github",
		Owner:              "acme",
		Name:               "widget",
		PlatformHost:       "github.com",
		PlatformExternalID: "repo-acme-widget",
		CloneURL:           "https://github.com/acme/widget.git",
	},
}

func verifiedGitHubRepoIdentity(host, owner, name string) db.RepoIdentity {
	identity := db.GitHubRepoIdentity(host, owner, name)
	identity.PlatformRepoID = "repo-" + strings.ToLower(owner+"-"+name)
	return identity
}

func seedRepoLaunchMetadata(t *testing.T, database *db.DB, repoID int64) {
	t.Helper()
	ctx := t.Context()
	repo, err := database.GetRepoByID(ctx, repoID)
	require.NoError(t, err)
	require.NotNil(t, repo)
	require.NotEmpty(t, repo.PlatformRepoID)

	cloneURL := strings.TrimSpace(repo.CloneURL)
	if cloneURL == "" {
		repoPath := strings.Trim(strings.TrimSpace(repo.RepoPath), "/")
		if repoPath == "" {
			repoPath = strings.Trim(repo.Owner, "/") + "/" + strings.Trim(repo.Name, "/")
		}
		cloneURL = "https://" + repo.PlatformHost + "/" + repoPath + ".git"
	}
	defaultBranch := strings.TrimSpace(repo.DefaultBranch)
	if defaultBranch == "" {
		defaultBranch = "main"
	}
	err = database.UpdateRepoProviderMetadata(
		ctx, repoID,
		db.RepoProviderMetadata{
			PlatformRepoID: repo.PlatformRepoID,
			WebURL:         strings.TrimSuffix(cloneURL, ".git"),
			CloneURL:       cloneURL,
			DefaultBranch:  defaultBranch,
		},
	)
	require.NoError(t, err)
}

func setupTestServerWithRepos(
	t *testing.T, mock *mockGH, repos []ghclient.RepoRef,
) (*server.Server, *db.DB) {
	return setupTestServerWithReposAndOptions(t, mock, repos, server.ServerOptions{})
}

func setupTestServerWithReposAndOptions(
	t *testing.T, mock *mockGH, repos []ghclient.RepoRef, options server.ServerOptions,
) (*server.Server, *db.DB) {
	t.Helper()

	database := dbtest.Open(t)
	repos = append([]ghclient.RepoRef(nil), repos...)
	for i := range repos {
		repo := &repos[i]
		if repo.PlatformExternalID == "" {
			repo.PlatformExternalID = "repo-" + repo.Owner + "-" + repo.Name
		}
		_, err := database.UpsertRepo(
			t.Context(), platformdb.DBRepoIdentity(platform.RepoRef{
				Platform:           platform.Kind(repo.Platform),
				Host:               repo.PlatformHost,
				Owner:              repo.Owner,
				Name:               repo.Name,
				RepoPath:           repo.RepoPath,
				PlatformExternalID: repo.PlatformExternalID,
			}),
		)
		require.NoError(t, err)
	}

	syncer := ghclient.NewSyncer(map[string]ghclient.Client{"github.com": mock}, database, nil, repos, time.Minute, nil, nil)
	// Drain any TriggerRun goroutines (fired by handlers like
	// POST /sync) before tests tear down. Registered after the DB
	// cleanup so LIFO ordering runs Stop first: without this, a
	// leaked goroutine from one test's handler can outlive its DB.
	t.Cleanup(syncer.Stop)
	var cfg *config.Config
	if options.WorktreeDir != "" {
		cfg = &config.Config{Tmux: config.Tmux{
			Command: []string{filepath.Join(t.TempDir(), "missing-tmux")},
		}}
	}
	srv := server.New(
		database, syncer, nil, "/",
		cfg, options,
	)
	// Registered after the DB cleanup so LIFO ordering runs Shutdown
	// first and lets background goroutines finish before DB close.
	t.Cleanup(func() { gracefulShutdown(t, srv) })
	return srv, database
}

func setupTestClient(t *testing.T, srv *server.Server) *apiclient.Client {
	t.Helper()
	return setupTestClientWithBaseURL(t, srv, "http://forge.test")
}

func setupTestClientWithBaseURL(
	t *testing.T,
	srv *server.Server,
	baseURL string,
) *apiclient.Client {
	t.Helper()

	httpClient := &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			var body io.Reader = http.NoBody
			if req.Body != nil {
				payload, err := io.ReadAll(req.Body)
				if err != nil {
					return nil, err
				}
				_ = req.Body.Close()
				body = strings.NewReader(string(payload))
			}

			serverReq := httptest.NewRequestWithContext(t.Context(), req.Method, req.URL.String(), body)
			serverReq.Header = req.Header.Clone()
			serverReq = serverReq.WithContext(req.Context())

			rr := httptest.NewRecorder()
			srv.ServeHTTP(rr, serverReq)
			return rr.Result(), nil
		}),
	}

	client, err := apiclient.NewWithHTTPClient(baseURL, httpClient)
	require.NoError(t, err)

	return client
}

func assertRFC3339UTC(t *testing.T, got string, want time.Time) {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, got)
	require.NoError(t, err)
	assert.Equal(t, want.UTC(), parsed.UTC())
	assert.True(t, strings.HasSuffix(got, "Z"), "expected UTC RFC3339 with trailing Z: %s", got)
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type seedPROpt func(*db.MergeRequest)

func withSeedPRTitle(title string) seedPROpt {
	return func(pr *db.MergeRequest) { pr.Title = title }
}

func withSeedPRLifecycle(
	state db.MergeRequestState,
	mergedAt *time.Time,
	closedAt *time.Time,
) seedPROpt {
	return func(pr *db.MergeRequest) {
		pr.State = state
		pr.MergedAt = mergedAt
		pr.ClosedAt = closedAt
	}
}

func withSeedPRTimes(createdAt, updatedAt, lastActivityAt time.Time) seedPROpt {
	return func(pr *db.MergeRequest) {
		pr.CreatedAt = createdAt
		pr.UpdatedAt = updatedAt
		pr.LastActivityAt = lastActivityAt
	}
}

// seedPR inserts a repo and a PR into the DB, returning the PR's internal ID.
func seedPR(t *testing.T, database *db.DB, owner, name string, number int, opts ...seedPROpt) int64 {
	t.Helper()
	ctx := t.Context()

	repoID, err := database.UpsertRepo(ctx, verifiedGitHubRepoIdentity("github.com", owner, name))
	require.NoError(t, err)
	seedRepoLaunchMetadata(t, database, repoID)

	numberText := strconv.Itoa(number)
	now := time.Now().UTC().Truncate(time.Second)
	pr := &db.MergeRequest{
		RepoID:           repoID,
		PlatformID:       int64(number) * 1000,
		Number:           number,
		URL:              "https://github.com/" + owner + "/" + name + "/pull/" + numberText,
		Title:            "Test PR #" + numberText,
		Author:           "testuser",
		State:            "open",
		IsDraft:          false,
		Body:             "test body",
		HeadBranch:       "feature",
		HeadRepoCloneURL: "https://github.com/" + owner + "/" + name + ".git",
		BaseBranch:       "main",
		Additions:        5,
		Deletions:        2,
		CommentCount:     0,
		ReviewDecision:   "",
		CIStatus:         "",
		CreatedAt:        now,
		UpdatedAt:        now,
		LastActivityAt:   now,
	}
	for _, opt := range opts {
		opt(pr)
	}

	prID, err := database.UpsertMergeRequest(ctx, pr)
	require.NoError(t, err)
	if pr.PlatformHeadSHA != "" {
		require.NoError(t, database.UpdateDiffSHAs(
			ctx, repoID, number,
			pr.PlatformHeadSHA, pr.PlatformBaseSHA, "merge-base",
		))
	}
	if len(pr.Labels) > 0 {
		require.NoError(t, database.ReplaceMergeRequestLabels(ctx, repoID, prID, pr.Labels))
	}
	require.NoError(t, database.EnsureKanbanState(ctx, prID))

	return prID
}

func TestAPIListPullsOrdersByLastActivityDescending(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	srv, database := setupTestServer(t)
	base := time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC)
	seedPR(t, database, "acme", "widget", 1,
		withSeedPRTimes(base, base, base.Add(time.Hour)),
	)
	seedPR(t, database, "acme", "widget", 2,
		withSeedPRTimes(base, base, base.Add(3*time.Hour)),
	)
	seedPR(t, database, "acme", "widget", 3,
		withSeedPRTimes(base, base, base.Add(2*time.Hour)),
	)
	client := setupTestClient(t, srv)

	resp, err := client.HTTP.ListPullsWithResponse(t.Context(), &generated.ListPullsRequestOptions{})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)
	require.NotNil(resp.JSON200)
	require.Len(*resp.JSON200, 3)
	assert := assert.New(t)
	assert.Equal(int64(2), (*resp.JSON200)[0].Number)
	assert.Equal(int64(3), (*resp.JSON200)[1].Number)
	assert.Equal(int64(1), (*resp.JSON200)[2].Number)
}

func TestAPISyncPRUsesProviderActivityAfterForcePush(t *testing.T) {
	runParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	base := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	forcePushAt := base.Add(3 * time.Hour)
	otherActivity := base.Add(2 * time.Hour)
	headSHA := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

	str := func(v string) *string { return &v }
	mock := &mockGH{
		getPullRequestFn: func(_ context.Context, _, _ string, number int) (*gh.PullRequest, error) {
			id := int64(1001)
			return &gh.PullRequest{
				ID:        &id,
				Number:    &number,
				Title:     str("Force push activity"),
				State:     str("open"),
				HTMLURL:   str("https://github.com/acme/widget/pull/1"),
				User:      &gh.User{Login: str("octocat")},
				CreatedAt: &gh.Timestamp{Time: base.Add(-time.Hour)},
				UpdatedAt: &gh.Timestamp{Time: forcePushAt},
				Head:      &gh.PullRequestBranch{Ref: str("feature"), SHA: &headSHA},
				Base:      &gh.PullRequestBranch{Ref: str("main")},
			}, nil
		},
		listIssueCommentsFn: func(context.Context, string, string, int) ([]*gh.IssueComment, error) {
			return nil, nil
		},
		listPRTimelineEventsFn: func(context.Context, string, string, int) ([]platformgithub.PullRequestTimelineEvent, error) {
			return []platformgithub.PullRequestTimelineEvent{{
				EventType: "force_push",
				Actor:     "octocat",
				BeforeSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				AfterSHA:  headSHA,
				Ref:       "feature",
				CreatedAt: forcePushAt,
			}}, nil
		},
	}
	srv, database := setupTestServerWithMock(t, mock)
	seedPR(t, database, "acme", "widget", 1,
		withSeedPRTitle("Force push activity"),
		withSeedPRTimes(base.Add(-time.Hour), base, base),
	)
	seedPR(t, database, "acme", "widget", 2,
		withSeedPRTitle("Other activity"),
		withSeedPRTimes(base.Add(-time.Hour), otherActivity, otherActivity),
	)
	client := setupTestClient(t, srv)

	syncResp, err := client.HTTP.SyncPullWithResponse(ctx, &generated.SyncPullRequestOptions{PathParams: &generated.SyncPullPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}})
	require.NoError(err)
	require.Equal(http.StatusOK, syncResp.StatusCode, string(syncResp.Body))
	require.NotNil(syncResp.JSON200)
	assert.Equal(forcePushAt, syncResp.JSON200.MergeRequest.LastActivityAt.UTC())

	detailResp, err := client.HTTP.GetPullWithResponse(ctx, &generated.GetPullRequestOptions{PathParams: &generated.GetPullPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}})
	require.NoError(err)
	require.Equal(http.StatusOK, detailResp.StatusCode, string(detailResp.Body))
	require.NotNil(detailResp.JSON200)
	assert.Equal(forcePushAt, detailResp.JSON200.MergeRequest.LastActivityAt.UTC())

	listResp, err := client.HTTP.ListPullsWithResponse(ctx, &generated.ListPullsRequestOptions{})
	require.NoError(err)
	require.Equal(http.StatusOK, listResp.StatusCode, string(listResp.Body))
	require.NotNil(listResp.JSON200)
	require.Len(*listResp.JSON200, 2)
	assert.Equal(int64(1), (*listResp.JSON200)[0].Number)
	assert.Equal(forcePushAt, (*listResp.JSON200)[0].LastActivityAt.UTC())
	assert.Equal(int64(2), (*listResp.JSON200)[1].Number)
}

func TestAPIGetPullIncludesLifecycleTimelineEvents(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	srv, database := setupTestServer(t)
	client := setupTestClient(t, srv)
	ctx := t.Context()
	createdAt := time.Date(2024, 6, 1, 10, 0, 0, 0, time.UTC)
	mergedAt := createdAt.Add(2 * time.Hour)
	closedAt := createdAt.Add(3 * time.Hour)
	reopenedAt := createdAt.Add(4 * time.Hour)
	previousClosedAt := createdAt.Add(time.Hour)

	seedPR(t, database, "acme", "widget", 1,
		withSeedPRTimes(createdAt, mergedAt, mergedAt),
		withSeedPRLifecycle(db.MergeRequestStateMerged, &mergedAt, &mergedAt),
	)
	seedPR(t, database, "acme", "widget", 2,
		withSeedPRTimes(createdAt, closedAt, closedAt),
		withSeedPRLifecycle(db.MergeRequestStateClosed, nil, &closedAt),
	)
	seedPR(t, database, "acme", "widget", 3,
		withSeedPRTimes(createdAt, reopenedAt, reopenedAt),
		withSeedPRLifecycle(db.MergeRequestStateOpen, nil, &previousClosedAt),
	)
	duplicateMRID := seedPR(t, database, "acme", "widget", 4,
		withSeedPRTimes(createdAt, mergedAt, mergedAt),
		withSeedPRLifecycle(db.MergeRequestStateMerged, &mergedAt, &mergedAt),
	)
	repeatedClosedMRID := seedPR(t, database, "acme", "widget", 5,
		withSeedPRTimes(createdAt, closedAt, closedAt),
		withSeedPRLifecycle(db.MergeRequestStateClosed, nil, &closedAt),
	)
	repeatedReopenedMRID := seedPR(t, database, "acme", "widget", 6,
		withSeedPRTimes(createdAt, reopenedAt, reopenedAt),
		withSeedPRLifecycle(db.MergeRequestStateOpen, nil, &previousClosedAt),
	)
	duplicateReopenedMRID := seedPR(t, database, "acme", "widget", 7,
		withSeedPRTimes(createdAt, reopenedAt.Add(time.Hour), reopenedAt.Add(time.Hour)),
		withSeedPRLifecycle(db.MergeRequestStateOpen, nil, &previousClosedAt),
	)
	require.NoError(database.UpsertMREvents(ctx, []db.MREvent{{
		MergeRequestID: duplicateMRID,
		EventType:      "merged",
		Author:         "maintainer",
		Summary:        "merged by provider",
		CreatedAt:      mergedAt,
		DedupeKey:      "provider-merged",
	}, {
		MergeRequestID: repeatedClosedMRID,
		EventType:      "closed",
		Author:         "maintainer",
		Summary:        "previously closed by provider",
		CreatedAt:      previousClosedAt,
		DedupeKey:      "provider-closed",
	}, {
		MergeRequestID: repeatedReopenedMRID,
		EventType:      "reopened",
		Author:         "maintainer",
		Summary:        "previously reopened by provider",
		CreatedAt:      previousClosedAt.Add(-30 * time.Minute),
		DedupeKey:      "provider-reopened",
	}, {
		MergeRequestID: duplicateReopenedMRID,
		EventType:      "reopened",
		Author:         "maintainer",
		Summary:        "reopened by provider",
		CreatedAt:      previousClosedAt.Add(30 * time.Minute),
		DedupeKey:      "provider-current-reopened",
	}}))

	mergedResp, err := client.HTTP.GetPullWithResponse(ctx, &generated.GetPullRequestOptions{PathParams: &generated.GetPullPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}})
	require.NoError(err)
	require.Equal(http.StatusOK, mergedResp.StatusCode)
	require.NotNil(mergedResp.JSON200)
	require.NotNil(mergedResp.JSON200.Events)
	require.Len(mergedResp.JSON200.Events, 1)
	assert.Equal("merged", mergedResp.JSON200.Events[0].EventType)
	assert.Equal("merged this", mergedResp.JSON200.Events[0].Summary)
	assert.Empty(mergedResp.JSON200.Events[0].Author)
	assert.Equal(int64(-1), mergedResp.JSON200.Events[0].ID)
	assert.True(mergedResp.JSON200.Events[0].CreatedAt.Equal(mergedAt))

	closedResp, err := client.HTTP.GetPullWithResponse(ctx, &generated.GetPullRequestOptions{PathParams: &generated.GetPullPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(2)}})
	require.NoError(err)
	require.Equal(http.StatusOK, closedResp.StatusCode)
	require.NotNil(closedResp.JSON200)
	require.NotNil(closedResp.JSON200.Events)
	require.Len(closedResp.JSON200.Events, 1)
	assert.Equal("closed", closedResp.JSON200.Events[0].EventType)
	assert.Equal("closed this", closedResp.JSON200.Events[0].Summary)
	assert.Empty(closedResp.JSON200.Events[0].Author)
	assert.Equal(int64(-2), closedResp.JSON200.Events[0].ID)
	assert.True(closedResp.JSON200.Events[0].CreatedAt.Equal(closedAt))

	reopenedResp, err := client.HTTP.GetPullWithResponse(ctx, &generated.GetPullRequestOptions{PathParams: &generated.GetPullPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(3)}})
	require.NoError(err)
	require.Equal(http.StatusOK, reopenedResp.StatusCode)
	require.NotNil(reopenedResp.JSON200)
	require.NotNil(reopenedResp.JSON200.Events)
	require.Len(reopenedResp.JSON200.Events, 1)
	assert.Equal("reopened", reopenedResp.JSON200.Events[0].EventType)
	assert.Equal("reopened this", reopenedResp.JSON200.Events[0].Summary)
	assert.Empty(reopenedResp.JSON200.Events[0].Author)
	assert.Equal(int64(-3), reopenedResp.JSON200.Events[0].ID)
	assert.True(reopenedResp.JSON200.Events[0].CreatedAt.Equal(reopenedAt))

	duplicateResp, err := client.HTTP.GetPullWithResponse(ctx, &generated.GetPullRequestOptions{PathParams: &generated.GetPullPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(4)}})
	require.NoError(err)
	require.Equal(http.StatusOK, duplicateResp.StatusCode)
	require.NotNil(duplicateResp.JSON200)
	require.NotNil(duplicateResp.JSON200.Events)
	require.Len(duplicateResp.JSON200.Events, 1)
	assert.Equal("merged", duplicateResp.JSON200.Events[0].EventType)
	assert.Equal("merged by provider", duplicateResp.JSON200.Events[0].Summary)
	assert.NotEqual(int64(-1), duplicateResp.JSON200.Events[0].ID)

	repeatedClosedResp, err := client.HTTP.GetPullWithResponse(ctx, &generated.GetPullRequestOptions{PathParams: &generated.GetPullPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(5)}})
	require.NoError(err)
	require.Equal(http.StatusOK, repeatedClosedResp.StatusCode)
	require.NotNil(repeatedClosedResp.JSON200)
	require.NotNil(repeatedClosedResp.JSON200.Events)
	require.Len(repeatedClosedResp.JSON200.Events, 2)
	assert.Equal("closed this", repeatedClosedResp.JSON200.Events[0].Summary)
	assert.True(repeatedClosedResp.JSON200.Events[0].CreatedAt.Equal(closedAt))
	assert.Equal("previously closed by provider", repeatedClosedResp.JSON200.Events[1].Summary)
	assert.True(repeatedClosedResp.JSON200.Events[1].CreatedAt.Equal(previousClosedAt))

	repeatedReopenedResp, err := client.HTTP.GetPullWithResponse(ctx, &generated.GetPullRequestOptions{PathParams: &generated.GetPullPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(6)}})
	require.NoError(err)
	require.Equal(http.StatusOK, repeatedReopenedResp.StatusCode)
	require.NotNil(repeatedReopenedResp.JSON200)
	require.NotNil(repeatedReopenedResp.JSON200.Events)
	require.Len(repeatedReopenedResp.JSON200.Events, 2)
	assert.Equal("reopened this", repeatedReopenedResp.JSON200.Events[0].Summary)
	assert.True(repeatedReopenedResp.JSON200.Events[0].CreatedAt.Equal(reopenedAt))
	assert.Equal("previously reopened by provider", repeatedReopenedResp.JSON200.Events[1].Summary)
	assert.True(repeatedReopenedResp.JSON200.Events[1].CreatedAt.Equal(previousClosedAt.Add(-30 * time.Minute)))

	duplicateReopenedResp, err := client.HTTP.GetPullWithResponse(ctx, &generated.GetPullRequestOptions{PathParams: &generated.GetPullPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(7)}})
	require.NoError(err)
	require.Equal(http.StatusOK, duplicateReopenedResp.StatusCode)
	require.NotNil(duplicateReopenedResp.JSON200)
	require.NotNil(duplicateReopenedResp.JSON200.Events)
	require.Len(duplicateReopenedResp.JSON200.Events, 1)
	assert.Equal("reopened", duplicateReopenedResp.JSON200.Events[0].EventType)
	assert.Equal("reopened by provider", duplicateReopenedResp.JSON200.Events[0].Summary)
	assert.NotEqual(int64(-3), duplicateReopenedResp.JSON200.Events[0].ID)
}

func TestGitLabSyncCoversRepositoryItemsEventsOverviewAndCI(t *testing.T) {
	runParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Second)
	publishedAt := now.Add(-72 * time.Hour)

	database := dbtest.Open(t)

	ref := platform.RepoRef{
		Platform:           platform.KindGitLab,
		Host:               "gitlab.example.com:8443",
		Owner:              "Group/SubGroup",
		Name:               "Project.Special",
		RepoPath:           "Group/SubGroup/Project.Special",
		PlatformID:         4242,
		PlatformExternalID: "gid://gitlab/Project/4242",
		WebURL:             "https://gitlab.example.com:8443/Group/SubGroup/Project.Special",
		CloneURL:           "https://gitlab.example.com:8443/Group/SubGroup/Project.Special.git",
		DefaultBranch:      "main",
	}
	mrEvent := platform.MergeRequestEvent{
		Repo:               ref,
		PlatformID:         9101,
		PlatformExternalID: "gid://gitlab/Note/9101",
		MergeRequestNumber: 7,
		EventType:          "issue_comment",
		Author:             "ada",
		Body:               "Looks good from GitLab",
		CreatedAt:          now.Add(time.Minute),
		DedupeKey:          "gitlab:note:9101",
	}
	issueEvent := platform.IssueEvent{
		Repo:               ref,
		PlatformID:         9201,
		PlatformExternalID: "gid://gitlab/Note/9201",
		IssueNumber:        11,
		EventType:          "issue_comment",
		Author:             "grace",
		Body:               "Issue comment from GitLab",
		CreatedAt:          now.Add(2 * time.Minute),
		DedupeKey:          "gitlab:issue-note:9201",
	}
	provider := &apiTestGitLabProvider{
		ref: ref,
		mergeRequests: []platform.MergeRequest{{
			Repo:               ref,
			PlatformID:         7001,
			PlatformExternalID: "gid://gitlab/MergeRequest/7001",
			Number:             7,
			URL:                "https://gitlab.example.com:8443/Group/SubGroup/Project.Special/-/merge_requests/7",
			Title:              "GitLab provider MR",
			Author:             "ada",
			State:              "open",
			Body:               "MR body",
			HeadBranch:         "feature/gitlab",
			BaseBranch:         "main",
			HeadSHA:            "abc123",
			BaseSHA:            "def456",
			Additions:          12,
			Deletions:          3,
			CommentCount:       1,
			CreatedAt:          now,
			UpdatedAt:          now,
			LastActivityAt:     now,
			Labels: []platform.Label{{
				Repo:               ref,
				PlatformID:         9301,
				PlatformExternalID: "gid://gitlab/ProjectLabel/9301",
				Name:               "backend",
				Color:              "0052cc",
				Description:        "Backend work",
			}},
		}},
		mergeRequestEvents: map[int][]platform.MergeRequestEvent{
			7: {mrEvent, mrEvent},
		},
		issues: []platform.Issue{{
			Repo:               ref,
			PlatformID:         8001,
			PlatformExternalID: "gid://gitlab/Issue/8001",
			Number:             11,
			URL:                "https://gitlab.example.com:8443/Group/SubGroup/Project.Special/-/issues/11",
			Title:              "GitLab provider issue",
			Author:             "grace",
			State:              "open",
			Body:               "Issue body",
			CommentCount:       1,
			CreatedAt:          now,
			UpdatedAt:          now,
			LastActivityAt:     now,
			Labels: []platform.Label{{
				Repo:               ref,
				PlatformID:         9302,
				PlatformExternalID: "gid://gitlab/ProjectLabel/9302",
				Name:               "bug",
				Color:              "d73a4a",
			}},
		}},
		issueEvents: map[int][]platform.IssueEvent{
			11: {issueEvent, issueEvent},
		},
		releases: []platform.Release{{
			Repo:               ref,
			PlatformID:         9401,
			PlatformExternalID: "gid://gitlab/Release/v1.2.0",
			TagName:            "v1.2.0",
			Name:               "Version 1.2.0",
			URL:                "https://gitlab.example.com:8443/Group/SubGroup/Project.Special/-/releases/v1.2.0",
			TargetCommitish:    "main",
			PublishedAt:        &publishedAt,
		}},
		tags: []platform.Tag{{
			Repo: ref,
			Name: "v1.1.0",
			SHA:  "oldtagsha",
			URL:  "https://gitlab.example.com:8443/Group/SubGroup/Project.Special/-/tree/v1.1.0",
		}},
		ciChecks: map[string][]platform.CICheck{
			"abc123": {{
				Repo:       ref,
				Name:       "pipeline",
				Status:     "completed",
				Conclusion: "success",
				URL:        "https://gitlab.example.com:8443/Group/SubGroup/Project.Special/-/pipelines/123",
				App:        "gitlab-ci",
			}},
		},
	}
	registry, err := platform.NewRegistry(provider)
	require.NoError(err)

	repo := ghclient.RepoRef{
		Platform:           platform.KindGitLab,
		Owner:              ref.Owner,
		Name:               ref.Name,
		PlatformHost:       ref.Host,
		RepoPath:           ref.RepoPath,
		PlatformRepoID:     ref.PlatformID,
		PlatformExternalID: ref.PlatformExternalID,
		WebURL:             ref.WebURL,
		CloneURL:           ref.CloneURL,
		DefaultBranch:      ref.DefaultBranch,
	}
	syncer := ghclient.NewSyncerWithRegistry(
		registry, database, nil, []ghclient.RepoRef{repo}, time.Minute, nil, nil,
	)
	t.Cleanup(syncer.Stop)
	srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{})
	t.Cleanup(func() { gracefulShutdown(t, srv) })
	client := setupTestClient(t, srv)

	syncer.RunOnce(ctx)
	require.NoError(syncer.SyncMR(ctx, ref.Owner, ref.Name, 7))
	require.NoError(syncer.SyncIssue(ctx, ref.Owner, ref.Name, 11))

	repoRow, err := database.GetRepoByIdentity(ctx, platformdb.DBRepoIdentity(ref))
	require.NoError(err)
	require.NotNil(repoRow)
	assert.Equal("gitlab", repoRow.Platform)
	assert.Equal("gitlab.example.com:8443", repoRow.PlatformHost)
	assert.Equal("Group/SubGroup/Project.Special", repoRow.RepoPath)
	require.NotNil(repoRow.LastSyncCompletedAt)

	mr, err := database.GetMergeRequestByRepoIDAndNumber(ctx, repoRow.ID, 7)
	require.NoError(err)
	require.NotNil(mr)
	assert.Equal("gid://gitlab/MergeRequest/7001", mr.PlatformExternalID)
	assert.Equal("success", mr.CIStatus)
	assert.JSONEq(
		`[{"name":"pipeline","status":"completed","conclusion":"success","url":"https://gitlab.example.com:8443/Group/SubGroup/Project.Special/-/pipelines/123","app":"gitlab-ci"}]`,
		mr.CIChecksJSON,
	)
	require.Len(mr.Labels, 1)
	assert.Equal("backend", mr.Labels[0].Name)
	mrEvents, err := database.ListMREvents(ctx, mr.ID)
	require.NoError(err)
	require.Len(mrEvents, 1)
	assert.Equal("Looks good from GitLab", mrEvents[0].Body)

	issue, err := database.GetIssueByRepoIDAndNumber(ctx, repoRow.ID, 11)
	require.NoError(err)
	require.NotNil(issue)
	require.Len(issue.Labels, 1)
	assert.Equal("bug", issue.Labels[0].Name)
	issueEvents, err := database.ListIssueEvents(ctx, issue.ID)
	require.NoError(err)
	require.Len(issueEvents, 1)
	assert.Equal("Issue comment from GitLab", issueEvents[0].Body)

	providerName := "gitlab"
	providerHost := "gitlab.example.com:8443"
	mrNumber := int64(7)
	pullResp, err := client.HTTP.GetPullOnHostWithResponse(ctx, &generated.GetPullOnHostRequestOptions{PathParams: &generated.GetPullOnHostPath{PlatformHost: providerHost, Provider: providerName, Owner: "Group/SubGroup", Name: "Project.Special", Number: int64(mrNumber)}})
	require.NoError(err)
	require.Equal(http.StatusOK, pullResp.StatusCode)
	require.NotNil(pullResp.JSON200)
	assert.Equal("gitlab", pullResp.JSON200.Repo.Provider)
	assert.Equal("gitlab.example.com:8443", pullResp.JSON200.Repo.PlatformHost)
	assert.Equal("Group/SubGroup/Project.Special", pullResp.JSON200.Repo.RepoPath)
	assert.Equal("success", pullResp.JSON200.MergeRequest.CIStatus)
	assert.Len(pullResp.JSON200.Events, 1)

	issueNumber := int64(11)
	issueResp, err := client.HTTP.GetIssueOnHostWithResponse(ctx, &generated.GetIssueOnHostRequestOptions{PathParams: &generated.GetIssueOnHostPath{PlatformHost: providerHost, Provider: providerName, Owner: "Group/SubGroup", Name: "Project.Special", Number: int64(issueNumber)}})
	require.NoError(err)
	require.Equal(http.StatusOK, issueResp.StatusCode)
	require.NotNil(issueResp.JSON200)
	assert.Equal("gitlab", issueResp.JSON200.Repo.Provider)
	assert.Equal("gitlab.example.com:8443", issueResp.JSON200.Repo.PlatformHost)
	assert.Len(issueResp.JSON200.Events, 1)

	summaryResp, err := client.HTTP.ListRepoSummariesWithResponse(ctx)
	require.NoError(err)
	require.Equal(http.StatusOK, summaryResp.StatusCode)
	require.NotNil(summaryResp.JSON200)
	require.Len(*summaryResp.JSON200, 1)
	summary := (*summaryResp.JSON200)[0]
	assert.Equal("gitlab", summary.Repo.Provider)
	assert.Equal("Group/SubGroup/Project.Special", summary.Repo.RepoPath)
	require.NotNil(summary.LatestRelease)
	assert.Equal("v1.2.0", summary.LatestRelease.TagName)
	assert.Equal(
		"https://gitlab.example.com:8443/Group/SubGroup/Project.Special/-/releases/v1.2.0",
		summary.LatestRelease.URL,
	)
}

func TestAPIActivityReturnsUTCCreatedAt(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)

	srv, database := setupTestServer(t)
	client := setupTestClient(t, srv)
	prID := seedPR(t, database, "acme", "widget", 1)
	ctx := t.Context()
	createdAtUTC := time.Now().UTC().Add(-2 * time.Hour).Round(time.Second)
	//nolint:forbidigo // Test fixture intentionally uses a non-UTC timestamp to verify UTC normalization.
	createdAt := createdAtUTC.In(time.FixedZone("EDT", -4*60*60))

	require.NoError(database.UpsertMREvents(ctx, []db.MREvent{{
		MergeRequestID: prID,
		EventType:      "issue_comment",
		Author:         "reviewer",
		Body:           "Looks good",
		CreatedAt:      createdAt,
		DedupeKey:      "comment-utc-created-at",
	}}))

	since := createdAtUTC.Add(-time.Hour).Format(time.RFC3339)
	resp, err := client.HTTP.ListActivityWithResponse(ctx, &generated.ListActivityRequestOptions{Query: &generated.ListActivityQuery{Since: &since}})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)
	require.NotNil(resp.JSON200)
	require.NotNil(resp.JSON200.Items)
	require.NotEmpty(resp.JSON200.Items)

	var commentItem *generated.ActivityItemResponse
	for i := range resp.JSON200.Items {
		item := resp.JSON200.Items[i]
		if item.Author == "reviewer" && item.ActivityType == "comment" {
			commentItem = &item
			break
		}
	}
	require.NotEmpty(commentItem.ActivityType)
	assertRFC3339UTC(t, commentItem.CreatedAt, createdAt)
	assert.Equal("reviewer", commentItem.Author)
	assert.Equal("comment", commentItem.ActivityType)
}

func TestAPIListActivityCapsDefaultBranchCommitMetadata(t *testing.T) {
	runParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	srv, database := setupTestServer(t)
	ctx := t.Context()
	base := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	repoID, err := database.UpsertRepo(ctx, verifiedGitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)

	committedAt := base.Add(10 * time.Minute)
	_, err = database.WriteDB().ExecContext(ctx, `
		INSERT INTO forge_branch_commits (
		    repo_id, branch_name, commit_sha, author_name, author_email,
		    authored_at, committer_name, committer_email, committed_at,
		    subject
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		repoID,
		"main",
		"abc123def456abc123def456abc123def456abcd",
		strings.Repeat("a", 300),
		strings.Repeat("e", 300),
		committedAt.Add(-time.Minute),
		strings.Repeat("c", 300),
		strings.Repeat("m", 300),
		committedAt,
		strings.Repeat("s", 700),
	)
	require.NoError(err)

	since := url.QueryEscape(base.Format(time.RFC3339))
	rr := testutil.DoJSON(t, srv, http.MethodGet, "/api/v1/activity?since="+since, nil)
	require.Equal(http.StatusOK, rr.Code)
	var body struct {
		Items []map[string]any `json:"items"`
	}
	require.NoError(json.NewDecoder(rr.Body).Decode(&body))
	require.Len(body.Items, 1)

	commit := body.Items[0]
	assert.Equal("default_branch_commit", commit["activity_type"])
	assert.Len(commit["body_preview"], 200)
	assert.Len(commit["author"], 256)
	assert.Len(commit["author_name"], 256)
	assert.Len(commit["author_email"], 256)
	assert.Len(commit["committer_name"], 256)
	assert.Len(commit["committer_email"], 256)
}

func TestAPIListActivityReflectsConfiguredDefaultBranchCommitCap(t *testing.T) {
	runParallelServerTest(t)
	acquireRootWorkspaceGitSlot(t)
	assert := assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	dir := t.TempDir()
	remote := filepath.Join(dir, "remote.git")
	work := filepath.Join(dir, "work")
	gitfixture.Run(t, dir, "init", "--bare", "--initial-branch=main", remote)
	gitfixture.Run(t, dir, "clone", remote, work)
	gitfixture.Run(t, work, "config", "user.email", "alice@example.com")
	gitfixture.Run(t, work, "config", "user.name", "Alice")

	shas := map[string]string{}
	for _, subject := range []string{"oldest", "third", "second", "newest"} {
		require.NoError(os.WriteFile(
			filepath.Join(work, subject+".txt"),
			[]byte(subject+"\n"),
			0o644,
		))
		gitfixture.Run(t, work, "add", ".")
		gitfixture.Run(t, work, "commit", "-m", subject)
		shas[subject] = gitfixture.SHA(t, work, "HEAD")
	}
	gitfixture.Run(t, work, "push", "origin", "main")

	database := dbtest.Open(t)
	clones := gitclone.New(filepath.Join(dir, "clones"), nil)
	repoRef := platform.RepoRef{
		Platform:           platform.KindGitLab,
		Host:               "gitlab.example.com",
		Owner:              "group",
		Name:               "project",
		RepoPath:           "group/project",
		PlatformExternalID: "gid://gitlab/Project/branch-activity-cap",
		CloneURL:           remote,
		DefaultBranch:      "main",
	}
	provider := &apiTestGitLabProvider{ref: repoRef}
	registry, err := platform.NewRegistry(provider)
	require.NoError(err)
	tracked := []ghclient.RepoRef{{
		Platform:           platform.KindGitLab,
		PlatformHost:       repoRef.Host,
		Owner:              repoRef.Owner,
		Name:               repoRef.Name,
		RepoPath:           repoRef.RepoPath,
		PlatformExternalID: repoRef.PlatformExternalID,
		CloneURL:           repoRef.CloneURL,
		DefaultBranch:      repoRef.DefaultBranch,
	}}
	syncer := ghclient.NewSyncerWithRegistry(
		registry, database, clones, tracked, time.Minute, nil, nil,
	)
	t.Cleanup(syncer.Stop)
	cfg := &config.Config{Activity: config.Activity{
		DefaultBranchRetentionDays: 90,
		DefaultBranchMaxCommits:    2,
	}}
	syncer.SetBranchActivityLimits(
		cfg.BranchActivityRetention(),
		cfg.Activity.DefaultBranchMaxCommits,
	)
	srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{Clones: clones})
	t.Cleanup(func() { gracefulShutdown(t, srv) })
	client := setupTestClient(t, srv)

	syncer.RunOnce(ctx)

	types := []string{"default_branch_commit"}
	dbItems, err := database.ListActivity(ctx, db.ListActivityOpts{
		Limit: 10,
		Types: types,
	})
	require.NoError(err)
	require.Len(dbItems, 2)
	gotPersisted := []string{dbItems[0].CommitSHA, dbItems[1].CommitSHA}
	assert.ElementsMatch([]string{shas["newest"], shas["second"]}, gotPersisted)

	since := time.Now().UTC().Add(-24 * time.Hour).Format(time.RFC3339)
	resp, err := client.HTTP.ListActivityWithResponse(ctx, &generated.ListActivityRequestOptions{Query: &generated.ListActivityQuery{Since: &since, Types: types}})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)
	require.NotNil(resp.JSON200)
	require.NotNil(resp.JSON200.Items)
	require.Len(resp.JSON200.Items, 2)
	gotAPI := []string{
		*resp.JSON200.Items[0].CommitSha,
		*resp.JSON200.Items[1].CommitSha,
	}
	assert.ElementsMatch([]string{shas["newest"], shas["second"]}, gotAPI)
}

func TestAPIListActivityReturnsProviderCompareURLsForDefaultBranchForcePushes(t *testing.T) {
	runParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	srv, database := setupTestServer(t)
	ctx := t.Context()
	base := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	beforeSHA := "before1234567890abcdef"
	afterSHA := "after1234567890abcdef"

	tests := []struct {
		name     string
		identity db.RepoIdentity
		wantURL  string
	}{
		{
			name:     "github",
			identity: verifiedGitHubRepoIdentity("github.com", "acme", "github-widget"),
			wantURL:  "https://github.com/acme/github-widget/compare/" + beforeSHA + "..." + afterSHA,
		},
		{
			name: "forgejo",
			identity: db.RepoIdentity{
				Platform:       "forgejo",
				PlatformHost:   "codeberg.org",
				PlatformRepoID: "forgejo-widget",
				Owner:          "acme",
				Name:           "forgejo-widget",
			},
			wantURL: "https://codeberg.org/acme/forgejo-widget/compare/" + beforeSHA + "..." + afterSHA,
		},
		{
			name: "gitea",
			identity: db.RepoIdentity{
				Platform:       "gitea",
				PlatformHost:   "gitea.com",
				PlatformRepoID: "gitea-widget",
				Owner:          "acme",
				Name:           "gitea-widget",
			},
			wantURL: "https://gitea.com/acme/gitea-widget/compare/" + beforeSHA + "..." + afterSHA,
		},
		{
			name: "gitlab",
			identity: db.RepoIdentity{
				Platform:       "gitlab",
				PlatformHost:   "gitlab.com",
				PlatformRepoID: "gitlab-widget",
				Owner:          "acme/platform",
				Name:           "gitlab-widget",
			},
			wantURL: "https://gitlab.com/acme/platform/gitlab-widget/-/compare/" + beforeSHA + "..." + afterSHA,
		},
	}

	for i, tt := range tests {
		repoID, err := database.UpsertRepo(ctx, tt.identity)
		require.NoError(err)
		require.NoError(database.InsertBranchForcePush(ctx, db.BranchForcePush{
			RepoID:     repoID,
			BranchName: "main",
			BeforeSHA:  beforeSHA,
			AfterSHA:   afterSHA,
			DetectedAt: base.Add(time.Duration(i) * time.Minute),
		}))
	}

	since := url.QueryEscape(base.Add(-time.Minute).Format(time.RFC3339))
	rr := testutil.DoJSON(t, srv, http.MethodGet, "/api/v1/activity?since="+since, nil)
	require.Equal(http.StatusOK, rr.Code)
	var body struct {
		Items []map[string]any `json:"items"`
	}
	require.NoError(json.NewDecoder(rr.Body).Decode(&body))

	gotURLs := make(map[string]string)
	for _, item := range body.Items {
		if item["activity_type"] != "default_branch_force_push" {
			continue
		}
		repo, ok := item["repo"].(map[string]any)
		require.True(ok)
		repoPath, ok := repo["repo_path"].(string)
		require.True(ok)
		activityURL, _ := item["activity_url"].(string)
		gotURLs[repoPath] = activityURL
	}

	for _, tt := range tests {
		repo := tt.identity.RepoPath
		if repo == "" {
			repo = tt.identity.Owner + "/" + tt.identity.Name
		}
		assert.Equal(tt.wantURL, gotURLs[repo], tt.name)
	}
}

func TestAPIListActivityCanHideDefaultBranchActivity(t *testing.T) {
	runParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	srv, database := setupTestServer(t)
	client := setupTestClient(t, srv)
	ctx := t.Context()
	base := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	repoID, err := database.UpsertRepo(ctx, verifiedGitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	seedPR(t, database, "acme", "widget", 1, withSeedPRTimes(base, base, base))
	require.NoError(database.UpsertBranchCommits(ctx, []db.BranchCommit{{
		RepoID:         repoID,
		BranchName:     "main",
		CommitSHA:      "abc123def456abc123def456abc123def456abcd",
		AuthorName:     "Commit Author",
		AuthorEmail:    "author@example.com",
		AuthoredAt:     base.Add(9 * time.Minute),
		CommitterName:  "Committer Person",
		CommitterEmail: "committer@example.com",
		CommittedAt:    base.Add(10 * time.Minute),
		Subject:        "ship default branch work",
	}}))
	require.NoError(database.InsertBranchForcePush(ctx, db.BranchForcePush{
		RepoID:     repoID,
		BranchName: "main",
		BeforeSHA:  "before1234567890",
		AfterSHA:   "after1234567890",
		DetectedAt: base.Add(20 * time.Minute),
	}))

	since := base.Add(-time.Minute).Format(time.RFC3339)
	types := []string{"new_pr"}
	resp, err := client.HTTP.ListActivityWithResponse(ctx, &generated.ListActivityRequestOptions{Query: &generated.ListActivityQuery{Since: &since, Types: types}})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)
	require.NotNil(resp.JSON200)
	require.NotNil(resp.JSON200.Items)
	require.Len(resp.JSON200.Items, 1)
	assert.Equal("new_pr", resp.JSON200.Items[0].ActivityType)
	assert.Equal(int64(1), resp.JSON200.Items[0].ItemNumber)
}

func TestAPIListActivityAcceptsProviderQualifiedRepoFilter(t *testing.T) {
	runParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	srv, database := setupTestServer(t)
	client := setupTestClient(t, srv)
	ctx := t.Context()

	githubRepo, err := database.UpsertRepo(ctx, db.RepoIdentity{
		Platform:       "github",
		PlatformHost:   "github.com",
		PlatformRepoID: "github-widget",
		Owner:          "acme",
		Name:           "widget",
	})
	require.NoError(err)
	giteaRepo, err := database.UpsertRepo(ctx, db.RepoIdentity{
		Platform:       "gitea",
		PlatformHost:   "github.com",
		PlatformRepoID: "gitea-widget",
		Owner:          "acme",
		Name:           "widget",
	})
	require.NoError(err)
	seedPRForRepo(t, database, githubRepo, "github.com", "acme", "widget", 1)
	seedPRForRepo(t, database, giteaRepo, "github.com", "acme", "widget", 2)

	since := time.Now().UTC().AddDate(0, 0, -7).Format(time.RFC3339)
	repo := "gitea|github.com/acme/widget"
	resp, err := client.HTTP.ListActivityWithResponse(ctx, &generated.ListActivityRequestOptions{Query: &generated.ListActivityQuery{Since: &since, Repo: &repo}})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)
	require.NotNil(resp.JSON200)
	require.NotNil(resp.JSON200.Items)
	require.NotEmpty(resp.JSON200.Items)
	for _, item := range resp.JSON200.Items {
		assert.Equal("gitea", item.Repo.Provider)
		assert.Equal("github.com", item.PlatformHost)
		assert.Equal("acme", item.RepoOwner)
		assert.Equal("widget", item.RepoName)
	}
}

func TestAPIListActivityKeepsProviderNamedHostsProviderQualified(t *testing.T) {
	runParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	srv, database := setupTestServer(t)
	client := setupTestClient(t, srv)
	ctx := t.Context()

	repoID, err := database.UpsertRepo(ctx, db.RepoIdentity{
		Platform:       "github",
		PlatformHost:   "gitea",
		PlatformRepoID: "github-widget",
		Owner:          "acme/team",
		Name:           "widget",
	})
	require.NoError(err)
	seedPRForRepo(t, database, repoID, "gitea", "acme/team", "widget", 1)
	seedPROnHost(t, database, "github.com", "acme", "widget", 2)

	since := time.Now().UTC().AddDate(0, 0, -7).Format(time.RFC3339)
	repo := "github|gitea/acme/team/widget"
	resp, err := client.HTTP.ListActivityWithResponse(ctx, &generated.ListActivityRequestOptions{Query: &generated.ListActivityQuery{Since: &since, Repo: &repo}})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)
	require.NotNil(resp.JSON200)
	require.NotNil(resp.JSON200.Items)
	require.NotEmpty(resp.JSON200.Items)
	for _, item := range resp.JSON200.Items {
		assert.Equal("github", item.Repo.Provider)
		assert.Equal("gitea", item.PlatformHost)
		assert.Equal("acme/team", item.RepoOwner)
		assert.Equal("widget", item.RepoName)
	}
}

func cleanupServerTestTmux(owner *testtmux.Owner) error {
	if owner == nil {
		return nil
	}
	return owner.Cleanup()
}

// seedPROnHost seeds a repo on a specific platform host and
// inserts a PR for it.
func seedPROnHost(
	t *testing.T, database *db.DB,
	host, owner, name string, number int,
	opts ...seedPROpt,
) int64 {
	t.Helper()
	ctx := t.Context()

	repoID, err := database.UpsertRepo(ctx, verifiedGitHubRepoIdentity(host, owner, name))
	require.NoError(t, err)

	return seedPRForRepo(t, database, repoID, host, owner, name, number, opts...)
}

func seedPRForRepo(
	t *testing.T, database *db.DB,
	repoID int64, host, owner, name string, number int,
	opts ...seedPROpt,
) int64 {
	t.Helper()
	ctx := t.Context()
	seedRepoLaunchMetadata(t, database, repoID)
	now := time.Now().UTC().Truncate(time.Second)
	pr := &db.MergeRequest{
		RepoID:         repoID,
		PlatformID:     int64(number) * 1000,
		Number:         number,
		URL:            fmt.Sprintf("https://%s/%s/%s/pull/%d", host, owner, name, number),
		Title:          fmt.Sprintf("Test PR #%d", number),
		Author:         "testuser",
		State:          "open",
		IsDraft:        false,
		Body:           "test body",
		HeadBranch:     "feature",
		BaseBranch:     "main",
		Additions:      5,
		Deletions:      2,
		CommentCount:   0,
		ReviewDecision: "",
		CIStatus:       "",
		CreatedAt:      now,
		UpdatedAt:      now,
		LastActivityAt: now,
	}
	for _, opt := range opts {
		opt(pr)
	}

	prID, err := database.UpsertMergeRequest(ctx, pr)
	require.NoError(t, err)
	require.NoError(t, database.EnsureKanbanState(ctx, prID))

	return prID
}
