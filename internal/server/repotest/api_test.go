package repotest

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
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
	"go.kenn.io/forge/internal/server/httpapi"
	"go.kenn.io/forge/internal/server/issueapi"
	"go.kenn.io/forge/internal/server/pullapi"
	"go.kenn.io/forge/internal/testutil"
	"go.kenn.io/forge/internal/testutil/dbtest"
	"go.kenn.io/forge/internal/testutil/gitfixture"
	"go.kenn.io/forge/internal/testutil/gitsafe"
	"go.kenn.io/forge/internal/testutil/processjob"
	"go.kenn.io/forge/internal/testutil/testsignal"
	"go.kenn.io/forge/internal/testutil/testtmux"
	"go.kenn.io/forge/internal/tokenauth"
	"go.kenn.io/forge/platform"
	"go.kenn.io/forge/platform/gitealike"
	platformgithub "go.kenn.io/forge/platform/github"
	gitcmd "go.kenn.io/kit/git/cmd"
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

func markArchiveItemRemovedUpstreamForServerTest(
	t *testing.T,
	database *db.DB,
	repoID int64,
	itemType db.ArchiveItemType,
	number int,
) {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Second)
	_, err := database.WriteDB().ExecContext(t.Context(), `
		INSERT INTO forge_archive_items (
			repo_id, item_type, item_number, provider_item_id,
			provider_created_at, provider_updated_at, lifecycle_state
		) VALUES (?, ?, ?, ?, ?, ?, 'removed_upstream')`,
		repoID, itemType, number, fmt.Sprintf("%s-%d", itemType, number), now, now,
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

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type seedPROpt func(*db.MergeRequest)

func withSeedPRTitle(title string) seedPROpt {
	return func(pr *db.MergeRequest) { pr.Title = title }
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

func TestAPIGetVersionReturnsBuildMetadata(t *testing.T) {
	runParallelServerTest(t)
	srv, _ := setupTestServer(t)
	srv.SetBuildInfo(server.BuildInfo{
		Name:      "kenn-forge",
		Version:   "1.2.3",
		Commit:    "abc1234",
		BuildDate: "2026-07-12T12:00:00Z",
	})

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/version", nil)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
	var body map[string]any
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&body))
	assert := assert.New(t)
	assert.Equal("kenn-forge", body["name"])
	assert.Equal("1.2.3", body["version"])
	assert.Equal("abc1234", body["commit"])
	assert.Equal("2026-07-12T12:00:00Z", body["buildDate"])
}

func TestAPIEnqueueItemSyncRejectsRemovedUpstreamWithoutProviderCalls(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	var pullCalls atomic.Int64
	var issueCalls atomic.Int64
	mock := &mockGH{
		getPullRequestFn: func(context.Context, string, string, int) (*gh.PullRequest, error) {
			pullCalls.Add(1)
			return nil, errors.New("removed pull must not be fetched")
		},
		getIssueFn: func(context.Context, string, string, int) (*gh.Issue, error) {
			issueCalls.Add(1)
			return nil, errors.New("removed issue must not be fetched")
		},
	}

	srv, database := setupTestServerWithMock(t, mock)
	ctx := t.Context()
	seedPR(t, database, "acme", "widget", 1)
	seedIssue(t, database, "acme", "widget", 2, "open")
	repo, err := database.GetRepoByIdentity(
		ctx, verifiedGitHubRepoIdentity("github.com", "acme", "widget"),
	)
	require.NoError(err)
	require.NotNil(repo)
	markArchiveItemRemovedUpstreamForServerTest(
		t, database, repo.ID, db.ArchiveItemTypeMergeRequest, 1,
	)
	markArchiveItemRemovedUpstreamForServerTest(
		t, database, repo.ID, db.ArchiveItemTypeIssue, 2,
	)
	client := setupTestClient(t, srv)

	pullResp, err := client.HTTP.EnqueuePrSyncWithResponse(ctx, &generated.EnqueuePrSyncRequestOptions{PathParams: &generated.EnqueuePrSyncPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}})
	require.Error(err)
	require.NotNil(pullResp)
	require.Equal(http.StatusNotFound, pullResp.StatusCode, string(pullResp.Body))
	require.NotNil(pullResp.Error)
	require.Equal(
		generated.ProblemErrorCode("pullNotFound"),
		pullResp.Error.Code,
	)

	issueResp, err := client.HTTP.EnqueueIssueSyncWithResponse(ctx, &generated.EnqueueIssueSyncRequestOptions{PathParams: &generated.EnqueueIssueSyncPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(2)}})
	require.Error(err)
	require.NotNil(issueResp)
	require.Equal(http.StatusNotFound, issueResp.StatusCode, string(issueResp.Body))
	require.NotNil(issueResp.Error)
	require.Equal(
		generated.ProblemErrorCode("issueNotFound"),
		issueResp.Error.Code,
	)
	require.Zero(pullCalls.Load())
	require.Zero(issueCalls.Load())
}

func TestAPIRouteReuseServesOnlyCurrentRepository(t *testing.T) {
	runParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	srv, database := setupTestServer(t)
	ctx := t.Context()

	oldEntry, err := database.GetRepositoryByProviderID(
		ctx, "github", "github.com", "repo-acme-widget",
	)
	require.NoError(err)
	require.NotNil(oldEntry)
	seedPRForRepo(
		t, database, oldEntry.Repository.ID,
		"github.com", "acme", "widget", 7,
		withSeedPRTitle("historical pull request"),
	)
	seedIssueForRepo(
		t, database, oldEntry.Repository.ID,
		"github.com", "acme", "widget", 8, "open", "historical issue",
	)

	newEntry, _, err := database.ReconcileRepositoryObservation(ctx, db.RepoIdentity{
		Platform:       "github",
		PlatformHost:   "github.com",
		PlatformRepoID: "repo-acme-widget-replacement",
		Owner:          "acme",
		Name:           "widget",
	}, time.Now().UTC().Add(time.Second))
	require.NoError(err)
	require.NotNil(newEntry)
	seedPRForRepo(
		t, database, newEntry.Repository.ID,
		"github.com", "acme", "widget", 7,
		withSeedPRTitle("current pull request"),
	)
	seedIssueForRepo(
		t, database, newEntry.Repository.ID,
		"github.com", "acme", "widget", 8, "open", "current issue",
	)

	repos := testutil.DoJSON(t, srv, http.MethodGet, "/api/v1/repos", nil)
	require.Equal(http.StatusOK, repos.Code, repos.Body.String())
	var repoBody []map[string]any
	require.NoError(json.NewDecoder(repos.Body).Decode(&repoBody))
	require.Len(repoBody, 1)

	pulls := testutil.DoJSON(t, srv, http.MethodGet, "/api/v1/pulls", nil)
	require.Equal(http.StatusOK, pulls.Code, pulls.Body.String())
	var pullBody []pullapi.MergeRequestResponse
	require.NoError(json.NewDecoder(pulls.Body).Decode(&pullBody))
	require.Len(pullBody, 1)
	assert.Equal("current pull request", pullBody[0].Title)
	pullDetail := testutil.DoJSON(
		t, srv, http.MethodGet, "/api/v1/pulls/gh/acme/widget/7", nil)

	require.Equal(http.StatusOK, pullDetail.Code, pullDetail.Body.String())
	var pullDetailBody pullapi.MergeRequestDetailResponse
	require.NoError(json.NewDecoder(pullDetail.Body).Decode(&pullDetailBody))
	require.NotNil(pullDetailBody.MergeRequest)
	assert.Equal("current pull request", pullDetailBody.MergeRequest.Title)

	issues := testutil.DoJSON(t, srv, http.MethodGet, "/api/v1/issues", nil)
	require.Equal(http.StatusOK, issues.Code, issues.Body.String())
	var issueBody []issueapi.IssueResponse
	require.NoError(json.NewDecoder(issues.Body).Decode(&issueBody))
	require.Len(issueBody, 1)
	assert.Equal("current issue", issueBody[0].Title)

	historical, err := database.GetMergeRequestByRepoIDAndNumber(
		ctx, oldEntry.Repository.ID, 7,
	)
	require.NoError(err)
	require.NotNil(historical)
	assert.Equal("historical pull request", historical.Title)
}

func TestProviderRefSyncEndpointsUseGitLabNestedRepoPath(t *testing.T) {
	runParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Second)

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
	provider := &apiTestGitLabProvider{
		ref: ref,
		mergeRequests: []platform.MergeRequest{
			{
				Repo:               ref,
				PlatformID:         7001,
				PlatformExternalID: "gid://gitlab/MergeRequest/7001",
				Number:             7,
				URL:                ref.WebURL + "/-/merge_requests/7",
				Title:              "Sync direct provider MR",
				Author:             "ada",
				State:              "open",
				Body:               "MR body",
				HeadBranch:         "feature/direct",
				BaseBranch:         "main",
				HeadSHA:            "abc123",
				BaseSHA:            "def456",
				CreatedAt:          now,
				UpdatedAt:          now,
				LastActivityAt:     now,
			},
			{
				Repo:               ref,
				PlatformID:         7002,
				PlatformExternalID: "gid://gitlab/MergeRequest/7002",
				Number:             8,
				URL:                ref.WebURL + "/-/merge_requests/8",
				Title:              "Sync async provider MR",
				Author:             "ada",
				State:              "open",
				Body:               "MR body",
				HeadBranch:         "feature/async",
				BaseBranch:         "main",
				HeadSHA:            "abc124",
				BaseSHA:            "def457",
				CreatedAt:          now,
				UpdatedAt:          now,
				LastActivityAt:     now,
			},
		},
		mergeRequestEvents: map[int][]platform.MergeRequestEvent{
			7: {{
				Repo:               ref,
				PlatformID:         9101,
				PlatformExternalID: "gid://gitlab/Note/9101",
				MergeRequestNumber: 7,
				EventType:          "issue_comment",
				Author:             "ada",
				Body:               "Direct MR event",
				CreatedAt:          now.Add(time.Minute),
				DedupeKey:          "gitlab:mr-note:9101",
			}},
			8: {{
				Repo:               ref,
				PlatformID:         9102,
				PlatformExternalID: "gid://gitlab/Note/9102",
				MergeRequestNumber: 8,
				EventType:          "issue_comment",
				Author:             "ada",
				Body:               "Async MR event",
				CreatedAt:          now.Add(2 * time.Minute),
				DedupeKey:          "gitlab:mr-note:9102",
			}},
		},
		issues: []platform.Issue{
			{
				Repo:               ref,
				PlatformID:         8001,
				PlatformExternalID: "gid://gitlab/Issue/8001",
				Number:             11,
				URL:                ref.WebURL + "/-/issues/11",
				Title:              "Sync direct provider issue",
				Author:             "grace",
				State:              "open",
				Body:               "Issue body",
				CreatedAt:          now,
				UpdatedAt:          now,
				LastActivityAt:     now,
			},
			{
				Repo:               ref,
				PlatformID:         8002,
				PlatformExternalID: "gid://gitlab/Issue/8002",
				Number:             12,
				URL:                ref.WebURL + "/-/issues/12",
				Title:              "Sync async provider issue",
				Author:             "grace",
				State:              "open",
				Body:               "Issue body",
				CreatedAt:          now,
				UpdatedAt:          now,
				LastActivityAt:     now,
			},
		},
		issueEvents: map[int][]platform.IssueEvent{
			11: {{
				Repo:               ref,
				PlatformID:         9201,
				PlatformExternalID: "gid://gitlab/Note/9201",
				IssueNumber:        11,
				EventType:          "issue_comment",
				Author:             "grace",
				Body:               "Direct issue event",
				CreatedAt:          now.Add(time.Minute),
				DedupeKey:          "gitlab:issue-note:9201",
			}},
			12: {{
				Repo:               ref,
				PlatformID:         9202,
				PlatformExternalID: "gid://gitlab/Note/9202",
				IssueNumber:        12,
				EventType:          "issue_comment",
				Author:             "grace",
				Body:               "Async issue event",
				CreatedAt:          now.Add(2 * time.Minute),
				DedupeKey:          "gitlab:issue-note:9202",
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
	_, err = database.UpsertRepo(ctx, platformdb.DBRepoIdentity(ref))
	require.NoError(err)
	syncer := ghclient.NewSyncerWithRegistry(
		registry, database, nil, []ghclient.RepoRef{repo}, time.Minute, nil, nil,
	)
	t.Cleanup(syncer.Stop)
	srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{})
	t.Cleanup(func() { gracefulShutdown(t, srv) })
	client := setupTestClient(t, srv)

	providerName := "gitlab"
	providerHost := "gitlab.example.com:8443"
	repoPath := "Group/SubGroup/Project.Special"
	mrDirect := int64(7)
	mrAsync := int64(8)
	issueDirect := int64(11)
	issueAsync := int64(12)

	prResp, err := client.HTTP.SyncPullOnHostWithResponse(ctx, &generated.SyncPullOnHostRequestOptions{PathParams: &generated.SyncPullOnHostPath{PlatformHost: providerHost, Provider: providerName, Owner: "Group/SubGroup", Name: "Project.Special", Number: int64(mrDirect)}})
	require.NoError(err)
	require.Equal(http.StatusOK, prResp.StatusCode, string(prResp.Body))
	require.NotNil(prResp.JSON200)
	assert.Equal("gitlab", prResp.JSON200.Repo.Provider)
	assert.Equal(repoPath, prResp.JSON200.Repo.RepoPath)
	assert.Equal("Sync direct provider MR", prResp.JSON200.MergeRequest.Title)
	assert.Len(prResp.JSON200.Events, 1)

	issueResp, err := client.HTTP.SyncIssueOnHostWithResponse(ctx, &generated.SyncIssueOnHostRequestOptions{PathParams: &generated.SyncIssueOnHostPath{PlatformHost: providerHost, Provider: providerName, Owner: "Group/SubGroup", Name: "Project.Special", Number: int64(issueDirect)}})
	require.NoError(err)
	require.Equal(http.StatusOK, issueResp.StatusCode, string(issueResp.Body))
	require.NotNil(issueResp.JSON200)
	assert.Equal("gitlab", issueResp.JSON200.Repo.Provider)
	assert.Equal(repoPath, issueResp.JSON200.Repo.RepoPath)
	assert.Equal("Sync direct provider issue", issueResp.JSON200.Issue.Title)
	assert.Len(issueResp.JSON200.Events, 1)

	asyncPRResp, err := client.HTTP.EnqueuePrSyncOnHostWithResponse(ctx, &generated.EnqueuePrSyncOnHostRequestOptions{PathParams: &generated.EnqueuePrSyncOnHostPath{PlatformHost: providerHost, Provider: providerName, Owner: "Group/SubGroup", Name: "Project.Special", Number: int64(mrAsync)}})
	require.NoError(err)
	require.Equal(http.StatusAccepted, asyncPRResp.StatusCode, string(asyncPRResp.Body))
	require.Eventually(func() bool {
		repoRow, rowErr := database.GetRepoByIdentity(ctx, platformdb.DBRepoIdentity(ref))
		if rowErr != nil || repoRow == nil {
			return false
		}
		mr, rowErr := database.GetMergeRequestByRepoIDAndNumber(ctx, repoRow.ID, 8)
		return rowErr == nil && mr != nil && mr.Title == "Sync async provider MR"
	}, 2*time.Second, 20*time.Millisecond)

	asyncIssueResp, err := client.HTTP.EnqueueIssueSyncOnHostWithResponse(ctx, &generated.EnqueueIssueSyncOnHostRequestOptions{PathParams: &generated.EnqueueIssueSyncOnHostPath{PlatformHost: providerHost, Provider: providerName, Owner: "Group/SubGroup", Name: "Project.Special", Number: int64(issueAsync)}})
	require.NoError(err)
	require.Equal(http.StatusAccepted, asyncIssueResp.StatusCode, string(asyncIssueResp.Body))
	require.Eventually(func() bool {
		repoRow, rowErr := database.GetRepoByIdentity(ctx, platformdb.DBRepoIdentity(ref))
		if rowErr != nil || repoRow == nil {
			return false
		}
		issue, rowErr := database.GetIssueByRepoIDAndNumber(ctx, repoRow.ID, 12)
		return rowErr == nil && issue != nil && issue.Title == "Sync async provider issue"
	}, 2*time.Second, 20*time.Millisecond)
}

func TestGitLabSyncUsesTagsForRepoOverviewWhenReleasesAreAbsent(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	ctx := t.Context()
	database := dbtest.Open(t)

	ref := platform.RepoRef{
		Platform:           platform.KindGitLab,
		Host:               "gitlab-tags.example.com",
		Owner:              "team",
		Name:               "service",
		RepoPath:           "team/service",
		PlatformID:         5150,
		PlatformExternalID: "gid://gitlab/Project/5150",
		WebURL:             "https://gitlab-tags.example.com/team/service",
		CloneURL:           "https://gitlab-tags.example.com/team/service.git",
		DefaultBranch:      "main",
	}
	provider := &apiTestGitLabProvider{
		ref: ref,
		tags: []platform.Tag{{
			Repo: ref,
			Name: "v0.9.0",
			SHA:  "tagsha",
			URL:  "https://gitlab-tags.example.com/team/service/-/tree/v0.9.0",
		}},
	}
	registry, err := platform.NewRegistry(provider)
	require.NoError(err)

	syncer := ghclient.NewSyncerWithRegistry(
		registry,
		database,
		nil,
		[]ghclient.RepoRef{{
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
		}},
		time.Minute,
		nil,
		nil,
	)
	t.Cleanup(syncer.Stop)
	srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{})
	t.Cleanup(func() { gracefulShutdown(t, srv) })
	client := setupTestClient(t, srv)

	syncer.RunOnce(ctx)

	resp, err := client.HTTP.ListRepoSummariesWithResponse(ctx)
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)
	require.NotNil(resp.JSON200)
	require.Len(*resp.JSON200, 1)
	require.NotNil((*resp.JSON200)[0].LatestRelease)

	assert := assert.New(t)
	assert.Equal("v0.9.0", (*resp.JSON200)[0].LatestRelease.TagName)
	assert.Equal("https://gitlab-tags.example.com/team/service/-/tree/v0.9.0", (*resp.JSON200)[0].LatestRelease.URL)
}

func TestAPIListRepoSummariesIncludesSyncedReleaseTimeline(t *testing.T) {
	runParallelServerTest(t)
	acquireRootWorkspaceGitSlot(t)
	require := require.New(t)
	assert := assert.New(t)
	ctx := t.Context()
	dir := t.TempDir()
	database := dbtest.Open(t)

	remote := filepath.Join(dir, "remote.git")
	work := filepath.Join(dir, "work")
	gitfixture.Run(t, dir, "init", "--bare", "--initial-branch=main", remote)
	gitfixture.Run(t, dir, "clone", remote, work)
	gitfixture.Run(t, work, "config", "user.email", "test@test.com")
	gitfixture.Run(t, work, "config", "user.name", "Test")

	commitFile := func(name, message string) {
		t.Helper()
		require.NoError(os.WriteFile(
			filepath.Join(work, name),
			[]byte(message+"\n"),
			0o644,
		))
		gitfixture.Run(t, work, "add", ".")
		gitfixture.Run(t, work, "commit", "-m", message)
	}

	commitFile("base.txt", "release v1")
	gitfixture.Run(t, work, "tag", "v1.0.0")
	commitFile("v2.txt", "prepare v2")
	gitfixture.Run(t, work, "tag", "v2.0.0")
	commitFile("v3.txt", "prepare v3")
	gitfixture.Run(t, work, "tag", "v3.0.0")
	commitFile("post-1.txt", "post latest 1")
	commitFile("post-2.txt", "post latest 2")
	gitfixture.Run(t, work, "push", "--tags", "origin", "main")

	clones := gitclone.New(filepath.Join(dir, "clones"), nil)
	clonePath, err := clones.ClonePathForContext(
		gitclone.WithRepositoryIdentity(ctx, "repo-acme-widgets"),
		"github", "github.com", "acme", "widgets",
	)
	require.NoError(err)
	require.NoError(os.MkdirAll(filepath.Dir(clonePath), 0o755))
	gitfixture.Run(t, dir, "clone", "--bare", remote, clonePath)

	releaseForTag := func(tag string, publishedAt time.Time) *gh.RepositoryRelease {
		t.Helper()
		name := "Release " + tag
		url := "https://github.com/acme/widgets/releases/tag/" + tag
		return &gh.RepositoryRelease{
			TagName:         tag,
			Name:            &name,
			HTMLURL:         url,
			TargetCommitish: "main",
			Prerelease:      false,
			Draft:           false,
			PublishedAt:     &gh.Timestamp{Time: publishedAt},
		}
	}

	releases := []*gh.RepositoryRelease{
		releaseForTag("v3.0.0", time.Date(2026, 4, 3, 12, 0, 0, 0, time.UTC)),
		releaseForTag("v2.0.0", time.Date(2026, 4, 2, 12, 0, 0, 0, time.UTC)),
		releaseForTag("v1.0.0", time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC)),
	}
	mock := &mockGH{
		listReleasesFn: func(
			_ context.Context, owner, repo string, perPage int,
		) ([]*gh.RepositoryRelease, error) {
			assert.Equal("acme", owner)
			assert.Equal("widgets", repo)
			assert.Equal(10, perPage)
			return releases, nil
		},
	}
	repos := []ghclient.RepoRef{{
		Owner: "acme", Name: "widgets", PlatformHost: "github.com",
	}}
	syncer := ghclient.NewSyncer(
		map[string]ghclient.Client{"github.com": mock},
		database, clones, repos, time.Minute, nil, nil,
	)
	t.Cleanup(syncer.Stop)

	syncer.RunOnce(ctx)

	srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{Clones: clones})
	t.Cleanup(func() { gracefulShutdown(t, srv) })
	client := setupTestClient(t, srv)

	resp, err := client.HTTP.ListRepoSummariesWithResponse(ctx)
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)
	require.NotNil(resp.JSON200)
	require.Len(*resp.JSON200, 1)

	widgets := (*resp.JSON200)[0]
	require.NotNil(widgets.LatestRelease)
	require.NotNil(widgets.Releases)
	require.NotNil(widgets.CommitsSinceRelease)
	require.NotNil(widgets.CommitTimeline)
	require.NotNil(widgets.TimelineUpdatedAt)

	assert.Equal("v3.0.0", widgets.LatestRelease.TagName)
	assert.Len(widgets.Releases, 3)
	assert.Equal("v1.0.0", widgets.Releases[2].TagName)
	assert.Equal(int64(2), *widgets.CommitsSinceRelease)
	assert.Len(widgets.CommitTimeline, 4)
	assert.Equal("post latest 2", widgets.CommitTimeline[0].Message)
	assert.Equal("post latest 1", widgets.CommitTimeline[1].Message)
	assert.Len(widgets.CommitTimeline[0].Sha, 40)

	gitfixture.Run(t, work, "tag", "-f", "v3.0.0", "HEAD")
	gitfixture.Run(t, work, "tag", "-f", "v1.0.0", "HEAD")
	gitfixture.Run(t, work, "push", "--force", "origin", "refs/tags/v3.0.0")
	gitfixture.Run(t, work, "push", "--force", "origin", "refs/tags/v1.0.0")
	syncer.RunOnce(ctx)

	resp, err = client.HTTP.ListRepoSummariesWithResponse(ctx)
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)
	require.NotNil(resp.JSON200)
	require.Len(*resp.JSON200, 1)

	widgets = (*resp.JSON200)[0]
	require.NotNil(widgets.LatestRelease)
	require.NotNil(widgets.CommitsSinceRelease)
	require.NotNil(widgets.CommitTimeline)
	assert.Equal("v3.0.0", widgets.LatestRelease.TagName)
	assert.Equal(int64(0), *widgets.CommitsSinceRelease)
	assert.Empty(widgets.CommitTimeline)
}

func TestAPIListRepoSummariesUsesTagsWhenNoReleases(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	ctx := t.Context()
	database := dbtest.Open(t)

	tagName := "v0.5.0"
	sha := "1234567890abcdef1234567890abcdef12345678"
	mock := &mockGH{
		listReleasesFn: func(
			_ context.Context, owner, repo string, perPage int,
		) ([]*gh.RepositoryRelease, error) {
			assert.Equal("acme", owner)
			assert.Equal("tagged", repo)
			assert.Equal(10, perPage)
			return nil, nil
		},
		listTagsFn: func(
			_ context.Context, owner, repo string, perPage int,
		) ([]*gh.RepositoryTag, error) {
			assert.Equal("acme", owner)
			assert.Equal("tagged", repo)
			assert.Equal(3, perPage)
			return []*gh.RepositoryTag{{
				Name: &tagName,
				Commit: &gh.Commit{
					SHA: &sha,
				},
			}}, nil
		},
	}
	repos := []ghclient.RepoRef{{
		Owner: "acme", Name: "tagged", PlatformHost: "github.com",
	}}
	syncer := ghclient.NewSyncer(
		map[string]ghclient.Client{"github.com": mock},
		database, nil, repos, time.Minute, nil, nil,
	)
	t.Cleanup(syncer.Stop)

	syncer.RunOnce(ctx)

	srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{})
	t.Cleanup(func() { gracefulShutdown(t, srv) })
	client := setupTestClient(t, srv)

	resp, err := client.HTTP.ListRepoSummariesWithResponse(ctx)
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)
	require.NotNil(resp.JSON200)
	require.Len(*resp.JSON200, 1)

	tagged := (*resp.JSON200)[0]
	require.NotNil(tagged.LatestRelease)
	require.NotNil(tagged.Releases)

	assert.Equal("v0.5.0", tagged.LatestRelease.TagName)
	assert.Equal("v0.5.0", tagged.LatestRelease.Name)
	assert.Equal("https://github.com/acme/tagged/tree/v0.5.0", tagged.LatestRelease.URL)
	assert.Equal(sha, tagged.LatestRelease.TargetCommitish)
	assert.Nil(tagged.LatestRelease.PublishedAt)
	assert.False(tagged.LatestRelease.Prerelease)
	assert.Len(tagged.Releases, 1)
	assert.Equal("v0.5.0", tagged.Releases[0].TagName)
	assert.Nil(tagged.CommitsSinceRelease)
	assert.Empty(tagged.CommitTimeline)
}

func TestAPIListRepoSummariesClearsStaleOverviewWhenTagFallbackFails(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	ctx := t.Context()

	database := dbtest.Open(t)

	repoID, err := database.UpsertRepo(ctx, verifiedGitHubRepoIdentity("github.com", "acme", "tagless"))
	require.NoError(err)

	publishedAt := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	timelineUpdatedAt := time.Date(2026, 3, 2, 12, 0, 0, 0, time.UTC)
	commitsSince := 9
	err = database.UpsertRepoOverview(ctx, repoID, db.RepoOverview{
		LatestRelease: &db.RepoRelease{
			TagName:     "v1.0.0",
			Name:        "Version 1.0.0",
			URL:         "https://github.com/acme/tagless/releases/tag/v1.0.0",
			PublishedAt: &publishedAt,
		},
		Releases: []db.RepoRelease{{
			TagName:     "v1.0.0",
			Name:        "Version 1.0.0",
			URL:         "https://github.com/acme/tagless/releases/tag/v1.0.0",
			PublishedAt: &publishedAt,
		}},
		CommitsSinceRelease: &commitsSince,
		CommitTimeline: []db.RepoCommitTimelinePoint{{
			SHA:         "abc123",
			Message:     "Old release timeline",
			CommittedAt: time.Date(2026, 3, 2, 10, 0, 0, 0, time.UTC),
		}},
		TimelineUpdatedAt: &timelineUpdatedAt,
	})
	require.NoError(err)

	mock := &mockGH{
		listReleasesFn: func(
			_ context.Context, owner, repo string, perPage int,
		) ([]*gh.RepositoryRelease, error) {
			assert.Equal("acme", owner)
			assert.Equal("tagless", repo)
			assert.Equal(10, perPage)
			return []*gh.RepositoryRelease{}, nil
		},
		listTagsFn: func(
			_ context.Context, owner, repo string, perPage int,
		) ([]*gh.RepositoryTag, error) {
			assert.Equal("acme", owner)
			assert.Equal("tagless", repo)
			assert.Equal(3, perPage)
			return nil, errors.New("tags unavailable")
		},
	}
	repos := []ghclient.RepoRef{{
		Owner: "acme", Name: "tagless", PlatformHost: "github.com",
	}}
	syncer := ghclient.NewSyncer(
		map[string]ghclient.Client{"github.com": mock},
		database, nil, repos, time.Minute, nil, nil,
	)
	t.Cleanup(syncer.Stop)

	syncer.RunOnce(ctx)

	srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{})
	t.Cleanup(func() { gracefulShutdown(t, srv) })
	client := setupTestClient(t, srv)

	resp, err := client.HTTP.ListRepoSummariesWithResponse(ctx)
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)
	require.NotNil(resp.JSON200)
	require.Len(*resp.JSON200, 1)

	tagless := (*resp.JSON200)[0]
	require.NotNil(tagless.Releases)
	require.NotNil(tagless.CommitTimeline)

	assert.Equal("acme", tagless.Owner)
	assert.Equal("tagless", tagless.Name)
	assert.Nil(tagless.LatestRelease)
	assert.Empty(tagless.Releases)
	assert.Nil(tagless.CommitsSinceRelease)
	assert.Empty(tagless.CommitTimeline)
	assert.Nil(tagless.TimelineUpdatedAt)
}

func TestAPITriggerSyncIgnoresRequestCancellation(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	database := dbtest.Open(t)

	syncReachedGitHub := make(chan struct{})
	var syncReachedGitHubOnce sync.Once
	mock := &mockGH{
		listOpenPullRequestsFn: func(
			_ context.Context, _, _ string,
		) ([]*gh.PullRequest, error) {
			syncReachedGitHubOnce.Do(func() { close(syncReachedGitHub) })
			return nil, nil
		},
	}
	syncer := ghclient.NewSyncer(map[string]ghclient.Client{"github.com": mock}, database, nil, []ghclient.RepoRef{{
		Owner:        "acme",
		Name:         "widget",
		PlatformHost: "github.com",
	}}, time.Minute, nil, nil)
	t.Cleanup(func() { syncer.Stop() })
	srv := server.New(
		database, syncer, nil, "/",
		nil, server.ServerOptions{},
	)
	t.Cleanup(syncer.Stop)

	ctx, cancel := context.WithCancel(t.Context())
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/sync", nil).WithContext(ctx)
	req.Header.Set("Content-Type", "application/json")
	cancel()

	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	require.Equal(http.StatusAccepted, rr.Code, rr.Body.String())

	select {
	case <-syncReachedGitHub:
	case <-time.After(2 * time.Second):
		require.Fail("expected sync to reach GitHub despite request context cancellation")
	}

	repos, err := database.ListRepos(t.Context())
	require.NoError(err)
	require.Len(repos, 1)
	assert.Equal(t, "acme", repos[0].Owner)
	assert.Equal(t, "widget", repos[0].Name)
}

// If the route returns 202 after admission is refused, the client believes a
// sync was retained even though no worker can execute it.
func TestAPITriggerSyncRejectsAfterSyncerStops(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	database := dbtest.Open(t)
	syncer := ghclient.NewSyncer(nil, database, nil, nil, time.Minute, nil, nil)
	syncer.Stop()

	srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{})
	t.Cleanup(func() { gracefulShutdown(t, srv) })
	rr := testutil.DoJSON(t, srv, http.MethodPost, "/api/v1/sync", nil)
	require.Equal(http.StatusServiceUnavailable, rr.Code, rr.Body.String())
}

func TestAPITriggerSyncOnlyRepoRestrictsRun(t *testing.T) {
	runParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)

	database := dbtest.Open(t)
	var mu sync.Mutex
	var calls []string
	mock := &mockGH{
		listOpenPullRequestsFn: func(
			_ context.Context, owner, repo string,
		) ([]*gh.PullRequest, error) {
			mu.Lock()
			defer mu.Unlock()
			calls = append(calls, owner+"/"+repo)
			return nil, nil
		},
	}
	syncer := ghclient.NewSyncer(
		map[string]ghclient.Client{"github.com": mock},
		database,
		nil,
		[]ghclient.RepoRef{
			{Platform: platform.KindGitHub, Owner: "acme", Name: "first", PlatformHost: "github.com"},
			{Platform: platform.KindGitHub, Owner: "acme", Name: "second", PlatformHost: "github.com"},
		},
		time.Minute,
		nil,
		nil,
	)
	done := make(chan struct{}, 1)
	syncer.SetOnStatusChange(func(status *ghclient.SyncStatus) {
		if !status.Running {
			select {
			case done <- struct{}{}:
			default:
			}
		}
	})
	t.Cleanup(syncer.Stop)
	srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{})
	t.Cleanup(func() { gracefulShutdown(t, srv) })

	rr := testutil.DoJSON(
		t,
		srv,
		http.MethodPost,
		"/api/v1/sync?only_repo=gh|github.com/acme/second",
		nil)

	require.Equal(http.StatusAccepted, rr.Code, rr.Body.String())
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		require.Fail("expected repository-only sync to complete")
	}

	mu.Lock()
	got := slices.Clone(calls)
	mu.Unlock()
	assert.Equal([]string{"acme/second"}, got)
}

func TestAPITriggerSyncRejectsUnknownOnlyRepo(t *testing.T) {
	runParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)

	srv, _ := setupTestServer(t)
	rr := testutil.DoJSON(
		t,
		srv,
		http.MethodPost,
		"/api/v1/sync?only_repo=github|github.com/acme/missing",
		nil)

	require.Equal(http.StatusBadRequest, rr.Code, rr.Body.String())

	var problem httpapi.ProblemError
	require.NoError(json.NewDecoder(rr.Body).Decode(&problem))
	assert.Equal(httpapi.CodeValidationError, problem.Code)
	assert.Equal("query.only_repo", problem.Details["field"])
}

func TestAPITriggerSyncBypassesNextSyncAfter(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)

	database := dbtest.Open(t)

	var listCalls atomic.Int32
	secondSync := make(chan struct{})
	var secondSyncOnce sync.Once
	mock := &mockGH{
		listOpenPullRequestsFn: func(
			_ context.Context, _, _ string,
		) ([]*gh.PullRequest, error) {
			if listCalls.Add(1) == 2 {
				secondSyncOnce.Do(func() { close(secondSync) })
			}
			return nil, nil
		},
	}
	trackers := map[string]*ghclient.RateTracker{
		"github.com": ghclient.NewRateTracker(
			database, "github.com", "host", "rest",
		),
	}
	syncer := ghclient.NewSyncer(
		map[string]ghclient.Client{"github.com": mock},
		database,
		nil,
		[]ghclient.RepoRef{{
			Owner:        "acme",
			Name:         "widget",
			PlatformHost: "github.com",
		}},
		time.Minute,
		trackers,
		nil,
	)
	t.Cleanup(syncer.Stop)

	srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{})
	t.Cleanup(func() { gracefulShutdown(t, srv) })

	// Seed the host cooldown window exactly like a recent background sync.
	syncer.RunOnce(t.Context())
	require.Equal(int32(1), listCalls.Load())

	client := setupTestClient(t, srv)
	resp, err := client.HTTP.TriggerSyncWithResponse(t.Context(), &generated.TriggerSyncRequestOptions{})
	require.NoError(err)
	require.Equal(http.StatusAccepted, resp.StatusCode)

	select {
	case <-secondSync:
	case <-time.After(2 * time.Second):
		require.Fail("expected explicit sync request to bypass background cooldown")
	}
}

// seedIssue inserts a repo and an issue into the DB.
func seedIssue(t *testing.T, database *db.DB, owner, name string, number int, state string) int64 {
	t.Helper()
	ctx := t.Context()
	repoID, err := database.UpsertRepo(ctx, verifiedGitHubRepoIdentity("github.com", owner, name))
	require.NoError(t, err)
	seedRepoLaunchMetadata(t, database, repoID)

	now := time.Now().UTC().Truncate(time.Second)
	issue := &db.Issue{
		RepoID: repoID, PlatformID: int64(number) * 1000, Number: number,
		URL:   fmt.Sprintf("https://github.com/%s/%s/issues/%d", owner, name, number),
		Title: "Test Issue", Author: "testuser", State: state,
		CreatedAt: now, UpdatedAt: now, LastActivityAt: now,
	}
	if state == "closed" {
		issue.ClosedAt = &now
	}
	issueID, err := database.UpsertIssue(ctx, issue)
	require.NoError(t, err)
	return issueID
}

func seedIssueForRepo(
	t *testing.T, database *db.DB,
	repoID int64, host, owner, name string, number int,
	state, title string,
) int64 {
	t.Helper()
	ctx := t.Context()
	seedRepoLaunchMetadata(t, database, repoID)

	now := time.Now().UTC().Truncate(time.Second)
	issue := &db.Issue{
		RepoID:         repoID,
		PlatformID:     int64(number) * 1000,
		Number:         number,
		URL:            fmt.Sprintf("https://%s/%s/%s/issues/%d", host, owner, name, number),
		Title:          title,
		Author:         "testuser",
		State:          state,
		CreatedAt:      now,
		UpdatedAt:      now,
		LastActivityAt: now,
	}
	if state == "closed" {
		issue.ClosedAt = &now
	}

	issueID, err := database.UpsertIssue(ctx, issue)
	require.NoError(t, err)
	return issueID
}

func TestAPIMarkDraftDoesNotGetRevertedByStaleSync(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)

	staleUpdatedAt := time.Date(2026, 4, 12, 1, 0, 0, 0, time.UTC)
	syncStarted := make(chan struct{}, 1)
	releaseSync := make(chan struct{})
	mock := &mockGH{
		getPullRequestFn: func(_ context.Context, owner, repo string, number int) (*gh.PullRequest, error) {
			syncStarted <- struct{}{}
			<-releaseSync

			id := int64(101)
			state := "open"
			title := "stale sync"
			url := "https://github.com/acme/widget/pull/1"
			author := "alice"
			draft := false
			headSHA := "abc123"
			baseSHA := "def456"
			featureRef := "feature"
			mainRef := "main"
			createdAt := gh.Timestamp{Time: staleUpdatedAt.Add(-time.Hour)}
			updatedAt := gh.Timestamp{Time: staleUpdatedAt}
			return &gh.PullRequest{
				ID:        &id,
				Number:    &number,
				State:     &state,
				Title:     &title,
				HTMLURL:   &url,
				Draft:     &draft,
				User:      &gh.User{Login: &author},
				CreatedAt: &createdAt,
				UpdatedAt: &updatedAt,
				Head:      &gh.PullRequestBranch{SHA: &headSHA, Ref: &featureRef},
				Base:      &gh.PullRequestBranch{SHA: &baseSHA, Ref: &mainRef},
			}, nil
		},
		convertToDraftFn: func(_ context.Context, owner, repo string, number int) (*gh.PullRequest, error) {
			id := int64(101)
			state := "open"
			draft := true
			updatedAt := gh.Timestamp{Time: staleUpdatedAt.Add(time.Minute)}
			return &gh.PullRequest{
				ID:        &id,
				Number:    &number,
				State:     &state,
				Draft:     &draft,
				UpdatedAt: &updatedAt,
			}, nil
		},
	}

	srv, database := setupTestServerWithMock(t, mock)
	client := setupTestClient(t, srv)

	repoID, err := database.UpsertRepo(t.Context(), verifiedGitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)

	prID, err := database.UpsertMergeRequest(t.Context(), &db.MergeRequest{
		RepoID:          repoID,
		PlatformID:      101,
		Number:          1,
		URL:             "https://github.com/acme/widget/pull/1",
		Title:           "ready PR",
		Author:          "alice",
		State:           "open",
		IsDraft:         false,
		Body:            "",
		HeadBranch:      "feature",
		BaseBranch:      "main",
		PlatformHeadSHA: "abc123",
		PlatformBaseSHA: "def456",
		Additions:       0,
		Deletions:       0,
		CommentCount:    0,
		ReviewDecision:  "",
		CIStatus:        "",
		CreatedAt:       staleUpdatedAt.Add(-time.Hour),
		UpdatedAt:       staleUpdatedAt,
		LastActivityAt:  staleUpdatedAt,
	})
	require.NoError(err)
	require.NoError(database.EnsureKanbanState(t.Context(), prID))

	syncDone := make(chan *generated.SyncPullResp, 1)
	syncErr := make(chan error, 1)
	go func() {
		resp, err := client.HTTP.SyncPullWithResponse(t.Context(), &generated.SyncPullRequestOptions{PathParams: &generated.SyncPullPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}})
		if err != nil {
			syncErr <- err
			return
		}
		syncDone <- resp
	}()

	<-syncStarted

	resp, err := client.HTTP.SetPrGithubStateWithResponse(t.Context(), &generated.SetPrGithubStateRequestOptions{PathParams: &generated.SetPrGithubStatePath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}, Body: &generated.SetPrGithubStateBody{State: "draft"}})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)

	draftPR, err := database.GetMergeRequest(t.Context(), "github", "github.com", "acme", "widget", 1)
	require.NoError(err)
	require.True(draftPR.IsDraft)
	assert.True(draftPR.UpdatedAt.After(staleUpdatedAt))

	close(releaseSync)

	completed := false
	select {
	case err := <-syncErr:
		require.NoError(err)
		completed = true
	case resp := <-syncDone:
		require.Equal(http.StatusOK, resp.StatusCode)
		completed = true
	case <-time.After(5 * time.Second):
	}
	require.True(completed, "timed out waiting for stale draft sync")

	finalPR, err := database.GetMergeRequest(t.Context(), "github", "github.com", "acme", "widget", 1)
	require.NoError(err)
	assert.True(finalPR.IsDraft)
	assert.Equal("ready PR", finalPR.Title)
	assert.True(finalPR.UpdatedAt.Equal(draftPR.UpdatedAt))
}

func TestResolveItem_UsesItemTypeHintForGitLab(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	repos := []ghclient.RepoRef{{
		Platform:     platform.KindGitLab,
		PlatformHost: "gitlab.example.com",
		Owner:        "group",
		Name:         "project",
	}}
	srv, database := setupTestServerWithRepos(t, &mockGH{}, repos)
	repoID, err := database.UpsertRepo(t.Context(), db.RepoIdentity{
		Platform:     "gitlab",
		PlatformHost: "gitlab.example.com",
		Owner:        "group",
		Name:         "project",
	})
	require.NoError(err)
	now := time.Now().UTC().Truncate(time.Second)
	_, err = database.UpsertMergeRequest(t.Context(), &db.MergeRequest{
		RepoID:         repoID,
		PlatformID:     10000,
		Number:         10,
		URL:            "https://gitlab.example.com/group/project/-/merge_requests/10",
		Title:          "Test MR",
		Author:         "testuser",
		State:          "open",
		HeadBranch:     "feature",
		BaseBranch:     "main",
		CreatedAt:      now,
		UpdatedAt:      now,
		LastActivityAt: now,
	})
	require.NoError(err)
	_, err = database.UpsertIssue(t.Context(), &db.Issue{
		RepoID:         repoID,
		PlatformID:     10001,
		Number:         10,
		URL:            "https://gitlab.example.com/group/project/-/issues/10",
		Title:          "Test Issue",
		Author:         "testuser",
		State:          "open",
		CreatedAt:      now,
		UpdatedAt:      now,
		LastActivityAt: now,
	})
	require.NoError(err)
	client := setupTestClient(t, srv)
	itemType := generated.ResolveRepoItemOnHostQueryItemTypeIssue

	resp, err := client.HTTP.ResolveRepoItemOnHostWithResponse(t.Context(), &generated.ResolveRepoItemOnHostRequestOptions{PathParams: &generated.ResolveRepoItemOnHostPath{PlatformHost: "gitlab.example.com", Provider: "gitlab", Owner: "group", Name: "project", Number: int64(10)}, Query: &generated.ResolveRepoItemOnHostQuery{ItemType: &itemType}})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)
	require.NotNil(resp.JSON200)
	require.Equal("issue", resp.JSON200.ItemType)
	require.EqualValues(10, resp.JSON200.Number)
	require.True(resp.JSON200.RepoTracked)
}

func TestResolveItem_NotFoundOnGitHub(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	mock := &mockGH{
		getIssueFn: func(_ context.Context, _, _ string, _ int) (*gh.Issue, error) {
			return nil, &gh.ErrorResponse{
				Response: &http.Response{StatusCode: 404},
				Message:  "Not Found",
			}
		},
	}
	repos := []ghclient.RepoRef{{Owner: "acme", Name: "widget"}}
	srv, database := setupTestServerWithRepos(t, mock, repos)
	_, err := database.UpsertRepo(t.Context(), verifiedGitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	client := setupTestClient(t, srv)

	resp, err := client.HTTP.ResolveRepoItemWithResponse(t.Context(), &generated.ResolveRepoItemRequestOptions{PathParams: &generated.ResolveRepoItemPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(999)}})
	require.Error(err)
	require.NotNil(resp)
	require.Equal(http.StatusNotFound, resp.StatusCode)
}

func TestResolveItem_GitHubServerError(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	mock := &mockGH{
		getIssueFn: func(_ context.Context, _, _ string, _ int) (*gh.Issue, error) {
			return nil, &gh.ErrorResponse{
				Response: &http.Response{StatusCode: 500},
				Message:  "Internal Server Error",
			}
		},
	}
	repos := []ghclient.RepoRef{{Owner: "acme", Name: "widget"}}
	srv, database := setupTestServerWithRepos(t, mock, repos)
	_, err := database.UpsertRepo(t.Context(), verifiedGitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	client := setupTestClient(t, srv)

	resp, err := client.HTTP.ResolveRepoItemWithResponse(t.Context(), &generated.ResolveRepoItemRequestOptions{PathParams: &generated.ResolveRepoItemPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(999)}})
	require.Error(err)
	require.NotNil(resp)
	require.Equal(http.StatusBadGateway, resp.StatusCode)
}

func TestAPICapabilityGatedRouteReturnsLookupProblemBeforeCapabilityProblem(t *testing.T) {
	runParallelServerTest(t)
	srv, _ := setupTestServer(t)

	tests := []struct {
		name     string
		path     string
		wantCode int
		wantWire string
	}{
		{
			name:     "unknown repo",
			path:     "/api/v1/pulls/gh/acme/unknown/7",
			wantCode: http.StatusNotFound,
			wantWire: "repoNotFound",
		},
		{
			name:     "invalid provider",
			path:     "/api/v1/pulls/not-a-provider/acme/widget/7",
			wantCode: http.StatusBadRequest,
			wantWire: "badRequest",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require := require.New(t)
			assert := assert.New(t)

			rr := testutil.DoJSON(
				t,
				srv,
				http.MethodPatch,
				tt.path,
				map[string]string{"title": "Updated title"})

			require.Equal(tt.wantCode, rr.Code, rr.Body.String())

			var problem rawProblemDetail
			require.NoError(json.NewDecoder(rr.Body).Decode(&problem))
			assert.Equal(tt.wantWire, problem.Code)
		})
	}
}

func TestAPICapabilityGatedMutationsHandleMissingSyncer(t *testing.T) {
	runParallelServerTest(t)
	database := dbtest.Open(t)
	srv := server.New(database, nil, nil, "/", nil, server.ServerOptions{})
	t.Cleanup(func() { gracefulShutdown(t, srv) })
	seedPR(t, database, "acme", "widget", 7)
	seedIssue(t, database, "acme", "widget", 11, "open")

	tests := []struct {
		name       string
		method     string
		path       string
		body       any
		capability string
	}{
		{
			name:       "PR content",
			method:     http.MethodPatch,
			path:       "/api/v1/pulls/gh/acme/widget/7",
			body:       map[string]string{"title": "Updated title"},
			capability: "state_mutation",
		},
		{
			name:       "issue content",
			method:     http.MethodPatch,
			path:       "/api/v1/issues/gh/acme/widget/11",
			body:       map[string]string{"title": "Updated title"},
			capability: "state_mutation",
		},
		{
			name:       "PR comment",
			method:     http.MethodPost,
			path:       "/api/v1/pulls/gh/acme/widget/7/comments",
			body:       map[string]string{"body": "hello"},
			capability: "comment_mutation",
		},
		{
			name:       "issue comment",
			method:     http.MethodPost,
			path:       "/api/v1/issues/gh/acme/widget/11/comments",
			body:       map[string]string{"body": "hello"},
			capability: "comment_mutation",
		},
		{
			name:       "issue creation",
			method:     http.MethodPost,
			path:       "/api/v1/issues/gh/acme/widget",
			body:       map[string]string{"title": "New issue", "body": "Issue body"},
			capability: "issue_mutation",
		},
		{
			name:       "review approval",
			method:     http.MethodPost,
			path:       "/api/v1/pulls/gh/acme/widget/7/approve",
			body:       map[string]string{"body": "looks good"},
			capability: "review_mutation",
		},
		{
			name:       "request changes",
			method:     http.MethodPost,
			path:       "/api/v1/pulls/gh/acme/widget/7/request-changes",
			body:       map[string]string{"body": "needs work"},
			capability: "review_mutation",
		},
		{
			name:       "workflow approval",
			method:     http.MethodPost,
			path:       "/api/v1/pulls/gh/acme/widget/7/approve-workflows",
			body:       nil,
			capability: "workflow_approval",
		},
		{
			name:       "ready for review",
			method:     http.MethodPost,
			path:       "/api/v1/pulls/gh/acme/widget/7/ready-for-review",
			body:       nil,
			capability: "ready_for_review",
		},
		{
			name:   "merge",
			method: http.MethodPost,
			path:   "/api/v1/pulls/gh/acme/widget/7/merge",
			body: map[string]string{
				"method":         "squash",
				"commit_title":   "Merge PR",
				"commit_message": "Merge PR",
			},
			capability: "merge_mutation",
		},
		{
			name:       "PR state",
			method:     http.MethodPost,
			path:       "/api/v1/pulls/gh/acme/widget/7/github-state",
			body:       map[string]string{"state": "closed"},
			capability: "state_mutation",
		},
		{
			name:       "issue state",
			method:     http.MethodPost,
			path:       "/api/v1/issues/gh/acme/widget/11/github-state",
			body:       map[string]string{"state": "closed"},
			capability: "state_mutation",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require := require.New(t)

			rr := testutil.DoJSON(t, srv, tt.method, tt.path, tt.body)
			require.Equal(http.StatusConflict, rr.Code, rr.Body.String())
			assertUnsupportedCapabilityProblem(
				t, rr.Body, "github", "github.com", tt.capability,
			)
		})
	}
}

// TestAPIRateLimitedEnvelope drives a provider mutation through a fake
// gitlab provider that returns a platform.Error with ErrCodeRateLimited
// and a known ResetAt. The handler routes the failure through
// providerCallProblem / mapPlatformError, which builds the rateLimited
// problem with details.retryAfter populated as an RFC 3339 string.
func TestAPIRateLimitedEnvelope(t *testing.T) {
	runParallelServerTest(t)
	reset := time.Date(2026, 5, 19, 12, 0, 0, 0, time.UTC)
	srv := setupGitLabIssueMutatorServer(t, &platform.Error{
		Code:         platform.ErrCodeRateLimited,
		Provider:     platform.KindGitLab,
		PlatformHost: "gitlab.example.com",
		ResetAt:      &reset,
	})

	tests := []struct {
		name   string
		method string
		path   string
		body   any
	}{
		{
			name:   "issue create",
			method: http.MethodPost,
			path:   "/api/v1/host/gitlab.example.com/issues/gl/group/project",
			body:   map[string]string{"title": "Rate limited", "body": "test"},
		},
		{
			name:   "pull content edit",
			method: http.MethodPatch,
			path:   "/api/v1/host/gitlab.example.com/pulls/gl/group/project/7",
			body:   map[string]string{"title": "Rate limited"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require := require.New(t)
			assert := assert.New(t)

			rr := testutil.DoJSON(t, srv, tt.method, tt.path, tt.body)
			require.Equal(http.StatusTooManyRequests, rr.Code, rr.Body.String())

			var problem rawProblemDetail
			require.NoError(json.NewDecoder(rr.Body).Decode(&problem))
			assert.Equal("rateLimited", problem.Code)
			require.NotNil(problem.Details)
			assert.Equal("gitlab", problem.Details["provider"])
			assert.Equal("gitlab.example.com", problem.Details["platformHost"])
			retryAfter, ok := problem.Details["retryAfter"].(string)
			require.True(
				ok,
				"details.retryAfter must be a string, got %T",
				problem.Details["retryAfter"],
			)
			parsed, parseErr := time.Parse(time.RFC3339, retryAfter)
			require.NoError(parseErr)
			assert.Equal(reset.UTC(), parsed.UTC())
		})
	}
}

// TestAPIValidationErrorEnvelope sends an invalid kanban status and
// expects the typed validationError envelope with details.field and
// details.allowed.
func TestAPIValidationErrorEnvelope(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)

	srv, database := setupTestServer(t)
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Second)
	repoID, err := database.UpsertRepo(ctx, db.RepoIdentity{
		Platform:     "github",
		PlatformHost: "github.com",
		Owner:        "acme",
		Name:         "widget",
	})
	require.NoError(err)
	_, err = database.UpsertMergeRequest(ctx, &db.MergeRequest{
		RepoID:         repoID,
		PlatformID:     7777,
		Number:         42,
		URL:            "https://github.com/acme/widget/pull/42",
		Title:          "Validation test",
		Author:         "alice",
		State:          "open",
		HeadBranch:     "feature",
		BaseBranch:     "main",
		CreatedAt:      now,
		UpdatedAt:      now,
		LastActivityAt: now,
	})
	require.NoError(err)

	rr := testutil.DoJSON(
		t,
		srv,
		http.MethodPut,
		"/api/v1/pulls/gh/acme/widget/42/state",
		map[string]string{"status": "frobnicated"})

	require.Equal(http.StatusBadRequest, rr.Code, rr.Body.String())

	var problem rawProblemDetail
	require.NoError(json.NewDecoder(rr.Body).Decode(&problem))
	assert.Equal("validationError", problem.Code)
	require.NotNil(problem.Details)
	assert.Equal("body.status", problem.Details["field"])
	allowed, ok := problem.Details["allowed"].([]any)
	require.True(ok, "details.allowed must be an array, got %T", problem.Details["allowed"])
	expected := []any{"new", "reviewing", "waiting", "awaiting_merge"}
	assert.Equal(expected, allowed)
}

// setupGitLabIssueMutatorServer returns a server backed by a gitlab
// provider whose IssueMutator.CreateIssue returns the supplied error.
// The other capabilities are unchanged from setupGitLabCapabilityServer.
func setupGitLabIssueMutatorServer(t *testing.T, createIssueErr error) *server.Server {
	t.Helper()
	require := require.New(t)
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Second)

	database := dbtest.Open(t)

	ref := platform.RepoRef{
		Platform:           platform.KindGitLab,
		Host:               "gitlab.example.com",
		Owner:              "group",
		Name:               "project",
		RepoPath:           "group/project",
		PlatformID:         4242,
		PlatformExternalID: "gid://gitlab/Project/4242",
		WebURL:             "https://gitlab.example.com/group/project",
		CloneURL:           "https://gitlab.example.com/group/project.git",
		DefaultBranch:      "main",
	}
	provider := &issueMutatorGitLabProvider{
		ref: ref,
		mergeRequests: []platform.MergeRequest{{
			Repo:           ref,
			PlatformID:     7001,
			Number:         7,
			URL:            "https://gitlab.example.com/group/project/-/merge_requests/7",
			Title:          "Existing MR",
			Author:         "alice",
			State:          "open",
			HeadBranch:     "feature",
			BaseBranch:     "main",
			CreatedAt:      now,
			UpdatedAt:      now,
			LastActivityAt: now,
		}},
		issues: []platform.Issue{{
			Repo:           ref,
			PlatformID:     8001,
			Number:         11,
			URL:            "https://gitlab.example.com/group/project/-/issues/11",
			Title:          "Existing",
			Author:         "alice",
			State:          "open",
			CreatedAt:      now,
			UpdatedAt:      now,
			LastActivityAt: now,
		}},
		providerErr: createIssueErr,
	}
	registry, err := platform.NewRegistry(provider)
	require.NoError(err)

	repo := ghclient.RepoRef{
		Platform:           platform.KindGitLab,
		Owner:              "group",
		Name:               "project",
		PlatformHost:       "gitlab.example.com",
		RepoPath:           "group/project",
		PlatformRepoID:     4242,
		PlatformExternalID: "gid://gitlab/Project/4242",
		WebURL:             "https://gitlab.example.com/group/project",
		CloneURL:           "https://gitlab.example.com/group/project.git",
		DefaultBranch:      "main",
	}
	syncer := ghclient.NewSyncerWithRegistry(
		registry, database, nil, []ghclient.RepoRef{repo}, time.Minute, nil, nil,
	)
	t.Cleanup(syncer.Stop)
	srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{})
	t.Cleanup(func() { gracefulShutdown(t, srv) })

	syncer.RunOnce(ctx)
	return srv
}

// issueMutatorGitLabProvider embeds apiTestGitLabProvider but advertises
// issue_mutation capability and returns the supplied error from
// CreateIssue. Used by TestAPIRateLimitedEnvelope.
type issueMutatorGitLabProvider struct {
	apiTestGitLabProvider
	providerErr error
}

func (p *issueMutatorGitLabProvider) Capabilities() platform.Capabilities {
	caps := p.apiTestGitLabProvider.Capabilities()
	caps.IssueMutation = true
	caps.StateMutation = true
	return caps
}

func (p *issueMutatorGitLabProvider) CreateIssue(
	_ context.Context,
	_ platform.RepoRef,
	_, _ string,
) (platform.Issue, error) {
	return platform.Issue{}, p.providerErr
}

func (p *issueMutatorGitLabProvider) EditMergeRequestContent(
	_ context.Context,
	_ platform.RepoRef,
	_ int,
	_, _ *string,
) (platform.MergeRequest, error) {
	return platform.MergeRequest{}, p.providerErr
}

// TestAPIResolveItemMapsLookupOutcomes drives GitHub item resolution
// (/repo/.../resolve/{number}) through a mock client whose type-probe fetch
// reports a removed, inaccessible, or transferred item. The problem envelope
// must carry the classified outcome — not a raw upstream failure or a 500 —
// and the moved outcome must include the destination extension members.
func TestAPIResolveItemMapsLookupOutcomes(t *testing.T) {
	runParallelServerTest(t)
	const movedRepoAPIURL = "https://api.github.com/repos/newowner/newname"

	statusErr := func(status int) error {
		return &gh.ErrorResponse{
			Response: &http.Response{StatusCode: status, Header: http.Header{}},
		}
	}

	cases := []struct {
		name            string
		getIssueErr     error
		wantStatus      int
		wantCode        string
		wantDestination bool
	}{
		{
			name:        "removed",
			getIssueErr: statusErr(http.StatusNotFound),
			wantStatus:  http.StatusNotFound,
			wantCode:    "notFound",
		},
		{
			name:        "inaccessible",
			getIssueErr: statusErr(http.StatusForbidden),
			wantStatus:  http.StatusForbidden,
			wantCode:    "forbidden",
		},
		{
			name:            "moved",
			wantStatus:      http.StatusNotFound,
			wantCode:        "notFound",
			wantDestination: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			assert := assert.New(t)
			ctx := t.Context()

			mock := &mockGH{
				getIssueFn: func(context.Context, string, string, int) (*gh.Issue, error) {
					if tc.getIssueErr != nil {
						return nil, tc.getIssueErr
					}
					return &gh.Issue{
						Number:        new(5),
						RepositoryURL: new(movedRepoAPIURL),
					}, nil
				},
			}
			srv, database := setupTestServerWithMock(t, mock)
			_, err := database.UpsertRepo(
				ctx, verifiedGitHubRepoIdentity("github.com", "acme", "widget"),
			)
			require.NoError(err)

			rr := testutil.DoJSON(t, srv, http.MethodPost, "/api/v1/repo/gh/acme/widget/resolve/5", nil)
			require.Equal(tc.wantStatus, rr.Code, rr.Body.String())

			var problem rawProblemDetail
			require.NoError(json.NewDecoder(rr.Body).Decode(&problem))
			assert.Equal(tc.wantCode, problem.Code)
			if !tc.wantDestination {
				return
			}
			require.NotNil(problem.Details)
			assert.Equal("github", problem.Details["destinationProvider"])
			assert.Equal("github.com", problem.Details["destinationPlatformHost"])
			assert.Equal("newowner", problem.Details["destinationOwner"])
			assert.Equal("newname", problem.Details["destinationName"])
		})
	}
}

// movedLookupGitLabProvider embeds apiTestGitLabProvider but reports every
// single-item read as moved to another repository via the supplied
// platform.Error. Used by TestAPIMovedLookupProblemCarriesDestination.
type movedLookupGitLabProvider struct {
	apiTestGitLabProvider
	lookupErr error
}

func (p *movedLookupGitLabProvider) GetIssue(
	_ context.Context,
	_ platform.RepoRef,
	_ int,
) (platform.Issue, error) {
	return platform.Issue{}, p.lookupErr
}

func (p *movedLookupGitLabProvider) GetMergeRequest(
	_ context.Context,
	_ platform.RepoRef,
	_ int,
) (platform.MergeRequest, error) {
	return platform.MergeRequest{}, p.lookupErr
}

// TestAPIMovedLookupProblemCarriesDestination drives item sync through a
// fake provider whose single-item read reports the item moved to another
// repository (a not_found platform.Error carrying Destination). The 404
// problem body must carry the full provider-aware destination identity as
// stable extension members so clients can retarget the reference.
func TestAPIMovedLookupProblemCarriesDestination(t *testing.T) {
	runParallelServerTest(t)
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Second)

	database := dbtest.Open(t)

	ref := platform.RepoRef{
		Platform:           platform.KindGitLab,
		Host:               "gitlab.example.com",
		Owner:              "group",
		Name:               "project",
		RepoPath:           "group/project",
		PlatformID:         4242,
		PlatformExternalID: "gid://gitlab/Project/4242",
		WebURL:             "https://gitlab.example.com/group/project",
		CloneURL:           "https://gitlab.example.com/group/project.git",
		DefaultBranch:      "main",
	}
	provider := &movedLookupGitLabProvider{
		ref: ref,
		mergeRequests: []platform.MergeRequest{{
			Repo:           ref,
			PlatformID:     7001,
			Number:         7,
			URL:            "https://gitlab.example.com/group/project/-/merge_requests/7",
			Title:          "Existing MR",
			Author:         "alice",
			State:          "open",
			HeadBranch:     "feature",
			BaseBranch:     "main",
			CreatedAt:      now,
			UpdatedAt:      now,
			LastActivityAt: now,
		}},
		issues: []platform.Issue{{
			Repo:           ref,
			PlatformID:     8001,
			Number:         11,
			URL:            "https://gitlab.example.com/group/project/-/issues/11",
			Title:          "Existing",
			Author:         "alice",
			State:          "open",
			CreatedAt:      now,
			UpdatedAt:      now,
			LastActivityAt: now,
		}},
		lookupErr: &platform.Error{
			Code:         platform.ErrCodeNotFound,
			Provider:     platform.KindGitLab,
			PlatformHost: "gitlab.example.com",
			Destination: &platform.RepoRef{
				Platform: platform.KindGitLab,
				Host:     "gitlab.example.com",
				Owner:    "newgroup",
				Name:     "project",
				RepoPath: "newgroup/project",
			},
			Err: errors.New("group/project item is not present (moved)"),
		},
	}
	registry, err := platform.NewRegistry(provider)
	require.NoError(t, err)

	repo := ghclient.RepoRef{
		Platform:           platform.KindGitLab,
		Owner:              "group",
		Name:               "project",
		PlatformHost:       "gitlab.example.com",
		RepoPath:           "group/project",
		PlatformRepoID:     4242,
		PlatformExternalID: "gid://gitlab/Project/4242",
		WebURL:             "https://gitlab.example.com/group/project",
		CloneURL:           "https://gitlab.example.com/group/project.git",
		DefaultBranch:      "main",
	}
	syncer := ghclient.NewSyncerWithRegistry(
		registry, database, nil, []ghclient.RepoRef{repo}, time.Minute, nil, nil,
	)
	t.Cleanup(syncer.Stop)
	srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{})
	t.Cleanup(func() { gracefulShutdown(t, srv) })

	syncer.RunOnce(ctx)

	tests := []struct {
		name string
		path string
	}{
		{
			name: "issue sync",
			path: "/api/v1/host/gitlab.example.com/issues/gl/group/project/11/sync",
		},
		{
			name: "pull sync",
			path: "/api/v1/host/gitlab.example.com/pulls/gl/group/project/7/sync",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require := require.New(t)
			assert := assert.New(t)

			rr := testutil.DoJSON(t, srv, http.MethodPost, tt.path, nil)
			require.Equal(http.StatusNotFound, rr.Code, rr.Body.String())

			var problem rawProblemDetail
			require.NoError(json.NewDecoder(rr.Body).Decode(&problem))
			assert.Equal("notFound", problem.Code)
			require.NotNil(problem.Details)
			assert.Equal("gitlab", problem.Details["provider"])
			assert.Equal("gitlab.example.com", problem.Details["platformHost"])
			assert.Equal("gitlab", problem.Details["destinationProvider"])
			assert.Equal(
				"gitlab.example.com", problem.Details["destinationPlatformHost"],
			)
			assert.Equal("newgroup", problem.Details["destinationOwner"])
			assert.Equal("project", problem.Details["destinationName"])
		})
	}
}

func TestAPIGitealikeReadSyncPersistsThroughServer(t *testing.T) {
	runParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	base := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	transport := &apiTestGitealikeTransport{
		repo: gitealike.RepositoryDTO{
			ID:            101,
			Owner:         gitealike.UserDTO{UserName: "forgejo"},
			Name:          "tea",
			FullName:      "forgejo/tea",
			HTMLURL:       "https://codeberg.test/forgejo/tea",
			CloneURL:      "https://codeberg.test/forgejo/tea.git",
			DefaultBranch: "main",
			Created:       base,
			Updated:       base,
		},
		pulls: []gitealike.PullRequestDTO{{
			ID:       201,
			Index:    7,
			HTMLURL:  "https://codeberg.test/forgejo/tea/pulls/7",
			Title:    "Add tea",
			User:     gitealike.UserDTO{UserName: "alice"},
			State:    "open",
			IsLocked: true,
			Head:     gitealike.BranchDTO{Ref: "feature", SHA: "abc123"},
			Base:     gitealike.BranchDTO{Ref: "main", SHA: "def456"},
			Created:  base,
			Updated:  base.Add(time.Minute),
		}},
		pullComments: []gitealike.CommentDTO{{
			ID:      301,
			User:    gitealike.UserDTO{UserName: "reviewer"},
			Body:    "looks good",
			Created: base.Add(2 * time.Minute),
			Updated: base.Add(2 * time.Minute),
		}},
		issues: []gitealike.IssueDTO{{
			ID:      401,
			Index:   8,
			HTMLURL: "https://codeberg.test/forgejo/tea/issues/8",
			Title:   "Missing cup",
			User:    gitealike.UserDTO{UserName: "bob"},
			State:   "open",
			Created: base,
			Updated: base.Add(time.Minute),
		}},
		issueComments: []gitealike.CommentDTO{{
			ID:      501,
			User:    gitealike.UserDTO{UserName: "triager"},
			Body:    "confirmed",
			Created: base.Add(3 * time.Minute),
			Updated: base.Add(3 * time.Minute),
		}},
		statuses: []gitealike.StatusDTO{{
			ID:        601,
			Context:   "build",
			State:     "success",
			TargetURL: "https://ci.test/build",
			Created:   base.Add(time.Minute),
			Updated:   base.Add(time.Minute),
		}},
	}
	provider := gitealike.NewProvider(
		platform.KindForgejo,
		"codeberg.test",
		transport,
	)
	registry, err := platform.NewRegistry(provider)
	require.NoError(err)

	database := dbtest.Open(t)

	syncer := ghclient.NewSyncerWithRegistry(
		registry,
		database,
		nil,
		[]ghclient.RepoRef{{
			Platform:     platform.KindForgejo,
			PlatformHost: "codeberg.test",
			Owner:        "forgejo",
			Name:         "tea",
			RepoPath:     "forgejo/tea",
		}},
		time.Minute,
		nil,
		nil,
	)
	t.Cleanup(syncer.Stop)
	srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{})
	t.Cleanup(func() { gracefulShutdown(t, srv) })
	client := setupTestClient(t, srv)

	syncer.RunOnce(ctx)
	require.NoError(syncer.SyncMR(ctx, "forgejo", "tea", 7))
	require.NoError(syncer.SyncIssue(ctx, "forgejo", "tea", 8))

	repo, err := database.GetRepoByIdentity(ctx, db.RepoIdentity{
		Platform:     "forgejo",
		PlatformHost: "codeberg.test",
		RepoPath:     "forgejo/tea",
	})
	require.NoError(err)
	require.NotNil(repo)
	mr, err := database.GetMergeRequestByRepoIDAndNumber(ctx, repo.ID, 7)
	require.NoError(err)
	require.NotNil(mr)
	assert.True(mr.IsLocked)
	assert.Equal("success", mr.CIStatus)

	pullResp, err := client.HTTP.GetPullOnHostWithResponse(ctx, &generated.GetPullOnHostRequestOptions{PathParams: &generated.GetPullOnHostPath{PlatformHost: "codeberg.test", Provider: "forgejo", Owner: "forgejo", Name: "tea", Number: int64(7)}})
	require.NoError(err)
	require.Equal(http.StatusOK, pullResp.StatusCode)
	require.NotNil(pullResp.JSON200)
	assert.True(pullResp.JSON200.MergeRequest.IsLocked)
	assert.Equal("forgejo", pullResp.JSON200.Repo.Provider)
	assert.True(pullResp.JSON200.Repo.Capabilities.ReadMergeRequests)
	assert.True(pullResp.JSON200.Repo.Capabilities.ReadCi)
	require.NotNil(pullResp.JSON200.Events)
	require.Len(pullResp.JSON200.Events, 1)
	assert.Equal("looks good", pullResp.JSON200.Events[0].Body)

	issueResp, err := client.HTTP.GetIssueOnHostWithResponse(ctx, &generated.GetIssueOnHostRequestOptions{PathParams: &generated.GetIssueOnHostPath{PlatformHost: "codeberg.test", Provider: "forgejo", Owner: "forgejo", Name: "tea", Number: int64(8)}})
	require.NoError(err)
	require.Equal(http.StatusOK, issueResp.StatusCode)
	require.NotNil(issueResp.JSON200)
	assert.Equal("Missing cup", issueResp.JSON200.Issue.Title)
	require.NotNil(issueResp.JSON200.Events)
	require.Len(issueResp.JSON200.Events, 1)
	assert.Equal("confirmed", issueResp.JSON200.Events[0].Body)
}

func TestAPIGitealikeMutationsPersistThroughServer(t *testing.T) {
	runParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	base := time.Date(2026, 5, 3, 12, 0, 0, 0, time.UTC)
	transport := &apiTestGitealikeTransport{
		nextCommentID:  900,
		nextIssueID:    950,
		nextIssueIndex: 81,
		repo: gitealike.RepositoryDTO{
			ID:            101,
			Owner:         gitealike.UserDTO{UserName: "tea"},
			Name:          "kettle",
			FullName:      "tea/kettle",
			HTMLURL:       "https://gitea.test/tea/kettle",
			CloneURL:      "https://gitea.test/tea/kettle.git",
			DefaultBranch: "main",
			Created:       base,
			Updated:       base,
		},
		pulls: []gitealike.PullRequestDTO{
			{
				ID:      201,
				Index:   7,
				HTMLURL: "https://gitea.test/tea/kettle/pulls/7",
				Title:   "Add kettle",
				User:    gitealike.UserDTO{UserName: "alice"},
				State:   "open",
				Head:    gitealike.BranchDTO{Ref: "feature", SHA: "abc123"},
				Base:    gitealike.BranchDTO{Ref: "main", SHA: "def456"},
				Created: base,
				Updated: base,
			},
			{
				ID:      202,
				Index:   9,
				HTMLURL: "https://gitea.test/tea/kettle/pulls/9",
				Title:   "Close me",
				User:    gitealike.UserDTO{UserName: "alice"},
				State:   "open",
				Head:    gitealike.BranchDTO{Ref: "close", SHA: "abc999"},
				Base:    gitealike.BranchDTO{Ref: "main", SHA: "def456"},
				Created: base,
				Updated: base,
			},
		},
		issues: []gitealike.IssueDTO{{
			ID:      401,
			Index:   8,
			HTMLURL: "https://gitea.test/tea/kettle/issues/8",
			Title:   "Missing cup",
			User:    gitealike.UserDTO{UserName: "bob"},
			State:   "open",
			Created: base,
			Updated: base,
		}},
	}
	provider := gitealike.NewProvider(
		platform.KindGitea,
		"gitea.test",
		transport,
		gitealike.WithMutations(),
	)
	registry, err := platform.NewRegistry(provider)
	require.NoError(err)

	database := dbtest.Open(t)

	syncer := ghclient.NewSyncerWithRegistry(
		registry,
		database,
		nil,
		[]ghclient.RepoRef{{
			Platform:     platform.KindGitea,
			PlatformHost: "gitea.test",
			Owner:        "tea",
			Name:         "kettle",
			RepoPath:     "tea/kettle",
		}},
		time.Minute,
		nil,
		nil,
	)
	t.Cleanup(syncer.Stop)
	srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{})
	t.Cleanup(func() { gracefulShutdown(t, srv) })
	client := setupTestClient(t, srv)

	syncer.RunOnce(ctx)

	repo, err := database.GetRepoByIdentity(ctx, db.RepoIdentity{
		Platform:     "gitea",
		PlatformHost: "gitea.test",
		RepoPath:     "tea/kettle",
	})
	require.NoError(err)
	require.NotNil(repo)
	require.NoError(database.UpdateDiffSHAs(ctx, repo.ID, 7, "abc123", "def456", "merge-base"))

	editedTitle := "Edited kettle"
	editedBody := "Updated kettle body"
	editContentResp, err := client.HTTP.EditPrContentOnHostWithResponse(ctx, &generated.EditPrContentOnHostRequestOptions{PathParams: &generated.EditPrContentOnHostPath{PlatformHost: "gitea.test", Provider: "gitea", Owner: "tea", Name: "kettle", Number: int64(7)}, Body: &generated.EditPrContentOnHostBody{
		Title: &editedTitle,
		Body:  &editedBody,
	}})
	require.NoError(err)
	require.Equal(http.StatusOK, editContentResp.StatusCode)
	require.NotNil(editContentResp.JSON200)
	assert.Equal(editedTitle, editContentResp.JSON200.MergeRequest.Title)
	assert.Equal(editedBody, editContentResp.JSON200.MergeRequest.Body)
	mrSeven := requireMR(t, database, repo.ID, 7)
	assert.Equal(editedTitle, mrSeven.Title)
	assert.Equal(editedBody, mrSeven.Body)

	commentResp, err := client.HTTP.PostPrCommentOnHostWithResponse(ctx, &generated.PostPrCommentOnHostRequestOptions{PathParams: &generated.PostPrCommentOnHostPath{PlatformHost: "gitea.test", Provider: "gitea", Owner: "tea", Name: "kettle", Number: int64(7)}, Body: &generated.PostPrCommentOnHostBody{Body: "Looks good"}})
	require.NoError(err)
	require.Equal(http.StatusCreated, commentResp.StatusCode)
	mrEvents, err := database.ListMREvents(ctx, mrSeven.ID)
	require.NoError(err)
	require.Len(mrEvents, 1)
	require.NotNil(mrEvents[0].PlatformID)
	commentID := *mrEvents[0].PlatformID
	assert.Equal("Looks good", mrEvents[0].Body)

	editCommentResp, err := client.HTTP.EditPrCommentOnHostWithResponse(ctx, &generated.EditPrCommentOnHostRequestOptions{PathParams: &generated.EditPrCommentOnHostPath{PlatformHost: "gitea.test", Provider: "gitea", Owner: "tea", Name: "kettle", Number: int64(7), CommentID: int64(commentID)}, Body: &generated.EditPrCommentOnHostBody{Body: "Still good"}})
	require.NoError(err)
	require.Equal(http.StatusOK, editCommentResp.StatusCode)
	mrEvents, err = database.ListMREvents(ctx, mrSeven.ID)
	require.NoError(err)
	require.Len(mrEvents, 1)
	assert.Equal("Still good", mrEvents[0].Body)
	expectedHeadSHA := mrSeven.PlatformHeadSHA

	approveResp, err := client.HTTP.ApprovePullOnHostWithResponse(ctx, &generated.ApprovePullOnHostRequestOptions{PathParams: &generated.ApprovePullOnHostPath{PlatformHost: "gitea.test", Provider: "gitea", Owner: "tea", Name: "kettle", Number: int64(7)}, Body: &generated.ApprovePullOnHostBody{
		Body:            "approved",
		ExpectedHeadSha: &expectedHeadSHA,
	}})
	require.NoError(err)
	require.Equal(http.StatusOK, approveResp.StatusCode, string(approveResp.Body))
	mrEvents, err = database.ListMREvents(ctx, mrSeven.ID)
	require.NoError(err)
	require.Len(mrEvents, 2)
	var reviewEvent *db.MREvent
	for i := range mrEvents {
		if mrEvents[i].EventType == "review" {
			reviewEvent = &mrEvents[i]
			break
		}
	}
	require.NotNil(reviewEvent)
	assert.Equal("APPROVED", reviewEvent.Summary)

	mergeResp, err := client.HTTP.MergePullOnHostWithResponse(ctx, &generated.MergePullOnHostRequestOptions{PathParams: &generated.MergePullOnHostPath{PlatformHost: "gitea.test", Provider: "gitea", Owner: "tea", Name: "kettle", Number: int64(7)}, Body: &generated.MergePullOnHostBody{
		Method:          "squash",
		CommitTitle:     "Merge kettle",
		CommitMessage:   "Merge Gitea MR",
		ExpectedHeadSha: &expectedHeadSHA,
	}})
	require.NoError(err)
	require.Equal(http.StatusOK, mergeResp.StatusCode)
	mrSeven = requireMR(t, database, repo.ID, 7)
	assert.Equal(db.MergeRequestStateMerged, mrSeven.State)
	require.NotNil(mrSeven.MergedAt)

	stateResp, err := client.HTTP.SetPrGithubStateOnHostWithResponse(ctx, &generated.SetPrGithubStateOnHostRequestOptions{PathParams: &generated.SetPrGithubStateOnHostPath{PlatformHost: "gitea.test", Provider: "gitea", Owner: "tea", Name: "kettle", Number: int64(9)}, Body: &generated.SetPrGithubStateOnHostBody{State: "closed"}})
	require.NoError(err)
	require.Equal(http.StatusOK, stateResp.StatusCode)
	mrNine := requireMR(t, database, repo.ID, 9)
	assert.Equal(db.MergeRequestStateClosed, mrNine.State)
	require.NotNil(mrNine.ClosedAt)

	createIssueResp, err := client.HTTP.CreateIssueOnHostWithResponse(ctx, &generated.CreateIssueOnHostRequestOptions{PathParams: &generated.CreateIssueOnHostPath{PlatformHost: "gitea.test", Provider: "gitea", Owner: "tea", Name: "kettle"}, Body: &generated.CreateIssueOnHostBody{Title: "New issue", Body: "New issue body"}})
	require.NoError(err)
	require.Equal(http.StatusCreated, createIssueResp.StatusCode)
	createdIssue, err := database.GetIssueByRepoIDAndNumber(ctx, repo.ID, 81)
	require.NoError(err)
	require.NotNil(createdIssue)
	assert.Equal("New issue", createdIssue.Title)

	issueCommentResp, err := client.HTTP.PostIssueCommentOnHostWithResponse(ctx, &generated.PostIssueCommentOnHostRequestOptions{PathParams: &generated.PostIssueCommentOnHostPath{PlatformHost: "gitea.test", Provider: "gitea", Owner: "tea", Name: "kettle", Number: int64(8)}, Body: &generated.PostIssueCommentOnHostBody{Body: "Confirmed"}})
	require.NoError(err)
	require.Equal(http.StatusCreated, issueCommentResp.StatusCode)
	issueEight := requireIssue(t, database, repo.ID, 8)
	issueEvents, err := database.ListIssueEvents(ctx, issueEight.ID)
	require.NoError(err)
	require.Len(issueEvents, 1)
	require.NotNil(issueEvents[0].PlatformID)
	issueCommentID := *issueEvents[0].PlatformID
	assert.Equal("Confirmed", issueEvents[0].Body)

	editIssueCommentResp, err := client.HTTP.EditIssueCommentOnHostWithResponse(ctx, &generated.EditIssueCommentOnHostRequestOptions{PathParams: &generated.EditIssueCommentOnHostPath{PlatformHost: "gitea.test", Provider: "gitea", Owner: "tea", Name: "kettle", Number: int64(8), CommentID: int64(issueCommentID)}, Body: &generated.EditIssueCommentOnHostBody{Body: "Confirmed again"}})
	require.NoError(err)
	require.Equal(http.StatusOK, editIssueCommentResp.StatusCode)
	issueEvents, err = database.ListIssueEvents(ctx, issueEight.ID)
	require.NoError(err)
	require.Len(issueEvents, 1)
	assert.Equal("Confirmed again", issueEvents[0].Body)

	issueStateResp, err := client.HTTP.SetIssueGithubStateOnHostWithResponse(ctx, &generated.SetIssueGithubStateOnHostRequestOptions{PathParams: &generated.SetIssueGithubStateOnHostPath{PlatformHost: "gitea.test", Provider: "gitea", Owner: "tea", Name: "kettle", Number: int64(8)}, Body: &generated.SetIssueGithubStateOnHostBody{State: "closed"}})
	require.NoError(err)
	require.Equal(http.StatusOK, issueStateResp.StatusCode)
	issueEight = requireIssue(t, database, repo.ID, 8)
	assert.Equal("closed", issueEight.State)
	require.NotNil(issueEight.ClosedAt)

	assert.Subset(transport.mutationCalls, []string{
		"edit_pull_content:7:Edited kettle:Updated kettle body",
		"create_comment:7:Looks good",
		"edit_comment:900:Still good",
		"merge:7:squash",
		"edit_pull:9:closed",
		"create_issue:New issue",
		"create_comment:8:Confirmed",
		"edit_comment:901:Confirmed again",
		"edit_issue:8:closed",
	})
}

// setupGitealikeCloneFixture builds a local git history (base commit
// on main, head commit on feature) plus a bare clone usable as the
// sync remote, so a normal provider sync can compute the reviewed diff
// snapshot without any seeded SHAs.
func setupGitealikeCloneFixture(t *testing.T) (cloneURL, baseSHA, headSHA string) {
	t.Helper()
	require := require.New(t)
	dir := t.TempDir()
	work := filepath.Join(dir, "work")
	require.NoError(os.MkdirAll(work, 0o755))
	// gitcmd.New() strips inherited GIT_DIR/GIT_WORK_TREE: under the
	// pre-commit hook git exports them into test children, and a bare
	// procutil git here would re-init and reconfigure the HOST repo
	// instead of the temp fixture.
	run := func(args ...string) string {
		out, stderr, err := gitcmd.New().Run(t.Context(), work, nil, args...)
		require.NoError(err, "git %v: %s%s", args, out, stderr)
		return strings.TrimSpace(string(out))
	}
	run("init", "-b", "main")
	run("config", "user.email", "fixture@example.invalid")
	run("config", "user.name", "Fixture")
	require.NoError(os.WriteFile(filepath.Join(work, "a.txt"), []byte("base\n"), 0o644))
	run("add", "a.txt")
	run("commit", "-m", "base")
	baseSHA = run("rev-parse", "HEAD")
	run("checkout", "-b", "feature")
	require.NoError(os.WriteFile(filepath.Join(work, "b.txt"), []byte("head\n"), 0o644))
	run("add", "b.txt")
	run("commit", "-m", "head")
	headSHA = run("rev-parse", "HEAD")
	cloneURL = filepath.Join(dir, "origin.git")
	out, stderr, err := gitcmd.New().Run(t.Context(), dir, nil, "clone", "--bare", work, cloneURL)
	require.NoError(err, "%s%s", out, stderr)
	return cloneURL, baseSHA, headSHA
}

// A plain provider sync must produce the reviewed head snapshot on its
// own: head-binding providers gate merge/approve on DiffHeadSHA, so a
// sync path that never writes it would leave every head-bound action
// permanently rejected with 409 head_unknown.
func TestAPIGitealikeNormalSyncEnablesHeadBoundMutations(t *testing.T) {
	runParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	cloneURL, baseSHA, headSHA := setupGitealikeCloneFixture(t)

	base := time.Date(2026, 5, 3, 12, 0, 0, 0, time.UTC)
	transport := &apiTestGitealikeTransport{
		repo: gitealike.RepositoryDTO{
			ID:            101,
			Owner:         gitealike.UserDTO{UserName: "tea"},
			Name:          "kettle",
			FullName:      "tea/kettle",
			HTMLURL:       "https://gitea.test/tea/kettle",
			CloneURL:      cloneURL,
			DefaultBranch: "main",
			Created:       base,
			Updated:       base,
		},
		pulls: []gitealike.PullRequestDTO{{
			ID:      201,
			Index:   7,
			HTMLURL: "https://gitea.test/tea/kettle/pulls/7",
			Title:   "Add kettle",
			User:    gitealike.UserDTO{UserName: "alice"},
			State:   "open",
			Head:    gitealike.BranchDTO{Ref: "feature", SHA: headSHA},
			Base:    gitealike.BranchDTO{Ref: "main", SHA: baseSHA},
			Created: base,
			Updated: base,
		}},
	}

	provider := gitealike.NewProvider(
		platform.KindGitea, "gitea.test", transport, gitealike.WithMutations(),
	)
	registry, err := platform.NewRegistry(provider)
	require.NoError(err)
	database := dbtest.Open(t)
	clones := gitclone.New(t.TempDir(), nil)
	syncer := ghclient.NewSyncerWithRegistry(
		registry, database, clones,
		[]ghclient.RepoRef{{
			Platform:     platform.KindGitea,
			PlatformHost: "gitea.test",
			Owner:        "tea",
			Name:         "kettle",
			RepoPath:     "tea/kettle",
			CloneURL:     cloneURL,
		}},
		time.Minute, nil, nil,
	)
	t.Cleanup(syncer.Stop)
	srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{})
	t.Cleanup(func() { gracefulShutdown(t, srv) })
	syncer.RunOnce(ctx)
	client := setupTestClient(t, srv)

	detail, err := client.HTTP.GetPullOnHostWithResponse(ctx, &generated.GetPullOnHostRequestOptions{PathParams: &generated.GetPullOnHostPath{PlatformHost: "gitea.test", Provider: "gitea", Owner: "tea", Name: "kettle", Number: int64(7)}})
	require.NoError(err)
	require.Equal(http.StatusOK, detail.StatusCode)
	require.NotNil(detail.JSON200)
	assert.Equal(headSHA, detail.JSON200.ReviewedHeadSha,
		"a normal sync must expose the reviewed head for head-bound actions")

	mergeResp, err := client.HTTP.MergePullOnHostWithResponse(ctx, &generated.MergePullOnHostRequestOptions{PathParams: &generated.MergePullOnHostPath{PlatformHost: "gitea.test", Provider: "gitea", Owner: "tea", Name: "kettle", Number: int64(7)}, Body: &generated.MergePullOnHostBody{
		Method:          "squash",
		CommitTitle:     "t",
		CommitMessage:   "m",
		ExpectedHeadSha: &headSHA,
	}})
	require.NoError(err)
	require.Equal(http.StatusOK, mergeResp.StatusCode, string(mergeResp.Body))
	assert.Equal(headSHA, transport.lastMergeOpts.ExpectedHeadSHA,
		"the sync-derived reviewed head must reach the provider as the pin")
}

func TestAPIGiteaActionsSyncPersistsThroughServer(t *testing.T) {
	runParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	base := time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC)
	stopped := base.Add(2 * time.Minute)
	transport := &apiTestGitealikeTransport{
		repo: gitealike.RepositoryDTO{
			ID:            301,
			Owner:         gitealike.UserDTO{UserName: "tea"},
			Name:          "actions",
			FullName:      "tea/actions",
			HTMLURL:       "https://gitea.test/tea/actions",
			CloneURL:      "https://gitea.test/tea/actions.git",
			DefaultBranch: "main",
			Created:       base,
			Updated:       base,
		},
		pulls: []gitealike.PullRequestDTO{{
			ID:      302,
			Index:   5,
			HTMLURL: "https://gitea.test/tea/actions/pulls/5",
			Title:   "Wire actions",
			User:    gitealike.UserDTO{UserName: "alice"},
			State:   "open",
			Head:    gitealike.BranchDTO{Ref: "feature", SHA: "sha-actions"},
			Base:    gitealike.BranchDTO{Ref: "main", SHA: "base-sha"},
			Created: base,
			Updated: base,
		}},
		statuses: []gitealike.StatusDTO{
			{
				ID:        401,
				Context:   "Build",
				State:     "success",
				TargetURL: "https://ci.test/build",
				Created:   base,
				Updated:   stopped,
			},
			{
				ID:        402,
				Context:   "Lint",
				State:     "pending",
				TargetURL: "https://ci.test/lint",
				Created:   base,
			},
		},
		actionRuns: []gitealike.ActionRunDTO{
			{
				ID:         501,
				Title:      "Build",
				Status:     "completed",
				Conclusion: "success",
				CommitSHA:  "sha-actions",
				HTMLURL:    "https://ci.test/build",
				Started:    &base,
				Stopped:    &stopped,
				WorkflowID: "build.yml",
			},
			{
				ID:         502,
				Title:      "Build",
				Status:     "completed",
				Conclusion: "cancelled",
				CommitSHA:  "sha-actions",
				HTMLURL:    "https://gitea.test/tea/actions/actions/runs/502",
				Started:    &base,
				Stopped:    &stopped,
				WorkflowID: "build-action.yml",
			},
			{
				ID:         503,
				RunNumber:  1,
				Title:      "Deploy",
				Status:     "completed",
				Conclusion: "failure",
				CommitSHA:  "sha-actions",
				HTMLURL:    "https://gitea.test/tea/actions/actions/runs/503",
				Created:    base,
				Updated:    base,
				Started:    &base,
				Stopped:    &base,
				WorkflowID: "deploy.yml",
			},
			{
				ID:         504,
				RunNumber:  2,
				Title:      "Deploy",
				Status:     "queued",
				CommitSHA:  "sha-actions",
				HTMLURL:    "https://gitea.test/tea/actions/actions/runs/504",
				Created:    stopped,
				Updated:    stopped,
				WorkflowID: "deploy.yml",
			},
		},
	}
	provider := gitealike.NewProvider(
		platform.KindGitea,
		"gitea.test",
		transport,
		gitealike.WithReadActions(),
	)
	registry, err := platform.NewRegistry(provider)
	require.NoError(err)

	database := dbtest.Open(t)

	syncer := ghclient.NewSyncerWithRegistry(
		registry,
		database,
		nil,
		[]ghclient.RepoRef{{
			Platform:     platform.KindGitea,
			PlatformHost: "gitea.test",
			Owner:        "tea",
			Name:         "actions",
			RepoPath:     "tea/actions",
		}},
		time.Minute,
		nil,
		nil,
	)
	t.Cleanup(syncer.Stop)
	srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{})
	t.Cleanup(func() { gracefulShutdown(t, srv) })
	client := setupTestClient(t, srv)

	syncer.RunOnce(ctx)
	require.NoError(syncer.SyncMROnProvider(ctx, platform.KindGitea, "gitea.test", "tea", "actions", 5))

	repo, err := database.GetRepoByIdentity(ctx, db.RepoIdentity{
		Platform:     "gitea",
		PlatformHost: "gitea.test",
		RepoPath:     "tea/actions",
	})
	require.NoError(err)
	require.NotNil(repo)
	mr := requireMR(t, database, repo.ID, 5)
	require.Equal("failure", mr.CIStatus)

	pullResp, err := client.HTTP.GetPullOnHostWithResponse(ctx, &generated.GetPullOnHostRequestOptions{PathParams: &generated.GetPullOnHostPath{PlatformHost: "gitea.test", Provider: "gitea", Owner: "tea", Name: "actions", Number: int64(5)}})
	require.NoError(err)
	require.Equal(http.StatusOK, pullResp.StatusCode)
	require.NotNil(pullResp.JSON200)

	var checks []db.CICheck
	require.NoError(json.Unmarshal([]byte(pullResp.JSON200.MergeRequest.CIChecksJSON), &checks))
	require.Len(checks, 4)
	assert.Equal([]string{"Build/status/success", "Lint/status/", "Build/action/failure", "Deploy/action/"}, ciCheckSummaries(checks))
	assert.Equal("failure", pullResp.JSON200.MergeRequest.CIStatus)
}

func ciCheckSummaries(checks []db.CICheck) []string {
	out := make([]string, 0, len(checks))
	for _, check := range checks {
		out = append(out, check.Name+"/"+check.App+"/"+check.Conclusion)
	}
	return out
}

func requireMR(t *testing.T, database *db.DB, repoID int64, number int) *db.MergeRequest {
	t.Helper()
	require := require.New(t)
	mr, err := database.GetMergeRequestByRepoIDAndNumber(t.Context(), repoID, number)
	require.NoError(err)
	require.NotNil(mr)
	return mr
}

func requireIssue(t *testing.T, database *db.DB, repoID int64, number int) *db.Issue {
	t.Helper()
	require := require.New(t)
	issue, err := database.GetIssueByRepoIDAndNumber(t.Context(), repoID, number)
	require.NoError(err)
	require.NotNil(issue)
	return issue
}

type apiTestGitealikeTransport struct {
	repo           gitealike.RepositoryDTO
	pulls          []gitealike.PullRequestDTO
	pullComments   []gitealike.CommentDTO
	issues         []gitealike.IssueDTO
	issueComments  []gitealike.CommentDTO
	statuses       []gitealike.StatusDTO
	mergeErr       error
	actionRuns     []gitealike.ActionRunDTO
	nextCommentID  int64
	nextIssueID    int64
	nextIssueIndex int
	mutationCalls  []string
	mergeHeadPins  []string
	lastReviewOpts gitealike.ReviewOptions

	lastMergeOpts gitealike.MergeOptions
	// headOverrides, when non-empty, replaces the head SHA returned by
	// successive GetPullRequest calls (the final entry repeats),
	// simulating a push racing an in-flight mutation.
	headOverrides []string
	headCalls     int
}

func (t *apiTestGitealikeTransport) GetRepository(
	context.Context,
	string,
	string,
) (gitealike.RepositoryDTO, error) {
	return t.repo, nil
}

func (t *apiTestGitealikeTransport) ListUserRepositories(
	context.Context,
	string,
	gitealike.PageOptions,
) ([]gitealike.RepositoryDTO, gitealike.Page, error) {
	return []gitealike.RepositoryDTO{t.repo}, gitealike.Page{}, nil
}

func (t *apiTestGitealikeTransport) ListOrgRepositories(
	context.Context,
	string,
	gitealike.PageOptions,
) ([]gitealike.RepositoryDTO, gitealike.Page, error) {
	return []gitealike.RepositoryDTO{t.repo}, gitealike.Page{}, nil
}

func (t *apiTestGitealikeTransport) ListOpenPullRequests(
	context.Context,
	platform.RepoRef,
	gitealike.PageOptions,
) ([]gitealike.PullRequestDTO, gitealike.Page, error) {
	return t.pulls, gitealike.Page{}, nil
}

func (t *apiTestGitealikeTransport) GetPullRequest(
	_ context.Context,
	_ platform.RepoRef,
	number int,
) (gitealike.PullRequestDTO, error) {
	for _, pr := range t.pulls {
		if pr.Index == number {
			t.headCalls++
			if len(t.headOverrides) > 0 {
				i := t.headCalls - 1
				if i >= len(t.headOverrides) {
					i = len(t.headOverrides) - 1
				}
				pr.Head.SHA = t.headOverrides[i]
			}
			return pr, nil
		}
	}
	return gitealike.PullRequestDTO{}, platform.ErrNotFound
}

func (t *apiTestGitealikeTransport) ListPullRequestComments(
	context.Context,
	platform.RepoRef,
	int,
	gitealike.PageOptions,
) ([]gitealike.CommentDTO, gitealike.Page, error) {
	return t.pullComments, gitealike.Page{}, nil
}

func (t *apiTestGitealikeTransport) ListPullRequestReviews(
	context.Context,
	platform.RepoRef,
	int,
	gitealike.PageOptions,
) ([]gitealike.ReviewDTO, gitealike.Page, error) {
	return nil, gitealike.Page{}, nil
}

func (t *apiTestGitealikeTransport) ListPullRequestCommits(
	context.Context,
	platform.RepoRef,
	int,
	gitealike.PageOptions,
) ([]gitealike.CommitDTO, gitealike.Page, error) {
	return nil, gitealike.Page{}, nil
}

func (t *apiTestGitealikeTransport) ListOpenIssues(
	context.Context,
	platform.RepoRef,
	gitealike.PageOptions,
) ([]gitealike.IssueDTO, gitealike.Page, error) {
	return t.issues, gitealike.Page{}, nil
}

func (t *apiTestGitealikeTransport) GetIssue(
	_ context.Context,
	_ platform.RepoRef,
	number int,
) (gitealike.IssueDTO, error) {
	for _, issue := range t.issues {
		if issue.Index == number {
			return issue, nil
		}
	}
	return gitealike.IssueDTO{}, platform.ErrNotFound
}

func (t *apiTestGitealikeTransport) ListIssueComments(
	context.Context,
	platform.RepoRef,
	int,
	gitealike.PageOptions,
) ([]gitealike.CommentDTO, gitealike.Page, error) {
	return t.issueComments, gitealike.Page{}, nil
}

func (t *apiTestGitealikeTransport) ListReleases(
	context.Context,
	platform.RepoRef,
	gitealike.PageOptions,
) ([]gitealike.ReleaseDTO, gitealike.Page, error) {
	return nil, gitealike.Page{}, nil
}

func (t *apiTestGitealikeTransport) ListTags(
	context.Context,
	platform.RepoRef,
	gitealike.PageOptions,
) ([]gitealike.TagDTO, gitealike.Page, error) {
	return nil, gitealike.Page{}, nil
}

func (t *apiTestGitealikeTransport) ListStatuses(
	context.Context,
	platform.RepoRef,
	string,
	gitealike.PageOptions,
) ([]gitealike.StatusDTO, gitealike.Page, error) {
	return t.statuses, gitealike.Page{}, nil
}

func (t *apiTestGitealikeTransport) ListActionRuns(
	context.Context,
	platform.RepoRef,
	string,
	gitealike.PageOptions,
) ([]gitealike.ActionRunDTO, gitealike.Page, error) {
	return t.actionRuns, gitealike.Page{}, nil
}

func (t *apiTestGitealikeTransport) CreateIssueComment(
	_ context.Context,
	_ platform.RepoRef,
	number int,
	body string,
) (gitealike.CommentDTO, error) {
	if t.nextCommentID == 0 {
		t.nextCommentID = 1
	}
	id := t.nextCommentID
	t.nextCommentID++
	t.mutationCalls = append(t.mutationCalls, fmt.Sprintf("create_comment:%d:%s", number, body))
	comment := gitealike.CommentDTO{
		ID:      id,
		User:    gitealike.UserDTO{UserName: "mutation-bot"},
		Body:    body,
		Created: time.Now().UTC().Truncate(time.Second),
		Updated: time.Now().UTC().Truncate(time.Second),
	}
	if issue := t.findIssue(number); issue != nil {
		t.issueComments = upsertComment(t.issueComments, comment)
		return comment, nil
	}
	t.pullComments = upsertComment(t.pullComments, comment)
	return comment, nil
}

func (t *apiTestGitealikeTransport) EditIssueComment(
	_ context.Context,
	_ platform.RepoRef,
	commentID int64,
	body string,
) (gitealike.CommentDTO, error) {
	t.mutationCalls = append(t.mutationCalls, fmt.Sprintf("edit_comment:%d:%s", commentID, body))
	comment := gitealike.CommentDTO{
		ID:      commentID,
		User:    gitealike.UserDTO{UserName: "mutation-bot"},
		Body:    body,
		Created: time.Now().UTC().Truncate(time.Second),
		Updated: time.Now().UTC().Truncate(time.Second),
	}
	t.pullComments = upsertComment(t.pullComments, comment)
	t.issueComments = upsertComment(t.issueComments, comment)
	return comment, nil
}

func (t *apiTestGitealikeTransport) DeleteIssueComment(
	_ context.Context,
	_ platform.RepoRef,
	commentID int64,
) error {
	t.mutationCalls = append(t.mutationCalls, fmt.Sprintf("delete_comment:%d", commentID))
	t.pullComments = slices.DeleteFunc(t.pullComments, func(comment gitealike.CommentDTO) bool {
		return comment.ID == commentID
	})
	t.issueComments = slices.DeleteFunc(t.issueComments, func(comment gitealike.CommentDTO) bool {
		return comment.ID == commentID
	})
	return nil
}

func (t *apiTestGitealikeTransport) CreateIssue(
	_ context.Context,
	ref platform.RepoRef,
	title string,
	body string,
) (gitealike.IssueDTO, error) {
	if t.nextIssueID == 0 {
		t.nextIssueID = 1
	}
	if t.nextIssueIndex == 0 {
		t.nextIssueIndex = 1
	}
	id := t.nextIssueID
	t.nextIssueID++
	number := t.nextIssueIndex
	t.nextIssueIndex++
	t.mutationCalls = append(t.mutationCalls, "create_issue:"+title)
	issue := gitealike.IssueDTO{
		ID:      id,
		Index:   number,
		HTMLURL: fmt.Sprintf("https://%s/%s/%s/issues/%d", ref.Host, ref.Owner, ref.Name, number),
		Title:   title,
		User:    gitealike.UserDTO{UserName: "mutation-bot"},
		State:   "open",
		Body:    body,
		Created: time.Now().UTC().Truncate(time.Second),
		Updated: time.Now().UTC().Truncate(time.Second),
	}
	t.issues = append(t.issues, issue)
	return issue, nil
}

func (t *apiTestGitealikeTransport) EditIssue(
	_ context.Context,
	_ platform.RepoRef,
	number int,
	opts gitealike.IssueMutationOptions,
) (gitealike.IssueDTO, error) {
	issue := t.findIssue(number)
	if issue == nil {
		return gitealike.IssueDTO{}, platform.ErrNotFound
	}
	if opts.State != nil {
		issue.State = *opts.State
		t.mutationCalls = append(t.mutationCalls, fmt.Sprintf("edit_issue:%d:%s", number, *opts.State))
	}
	if opts.Title != nil {
		issue.Title = *opts.Title
	}
	if opts.Body != nil {
		issue.Body = *opts.Body
	}
	issue.Updated = time.Now().UTC().Truncate(time.Second)
	if issue.State == "closed" {
		closed := issue.Updated
		issue.Closed = &closed
	}
	return *issue, nil
}

func (t *apiTestGitealikeTransport) EditPullRequest(
	_ context.Context,
	_ platform.RepoRef,
	number int,
	opts gitealike.PullRequestMutationOptions,
) (gitealike.PullRequestDTO, error) {
	pr := t.findPull(number)
	if pr == nil {
		return gitealike.PullRequestDTO{}, platform.ErrNotFound
	}
	if opts.State != nil {
		pr.State = *opts.State
		t.mutationCalls = append(t.mutationCalls, fmt.Sprintf("edit_pull:%d:%s", number, *opts.State))
	}
	if opts.Title != nil {
		pr.Title = *opts.Title
	}
	if opts.Body != nil {
		pr.Body = *opts.Body
	}
	if opts.Title != nil || opts.Body != nil {
		t.mutationCalls = append(t.mutationCalls, fmt.Sprintf("edit_pull_content:%d:%s:%s", number, pr.Title, pr.Body))
	}
	pr.Updated = time.Now().UTC().Truncate(time.Second)
	if pr.State == "closed" {
		closed := pr.Updated
		pr.Closed = &closed
	}
	return *pr, nil
}

func (t *apiTestGitealikeTransport) MergePullRequest(
	_ context.Context,
	_ platform.RepoRef,
	number int,
	opts gitealike.MergeOptions,
) (gitealike.MergeResultDTO, error) {
	pr := t.findPull(number)
	if pr == nil {
		return gitealike.MergeResultDTO{}, platform.ErrNotFound
	}
	t.mutationCalls = append(t.mutationCalls, fmt.Sprintf("merge:%d:%s", number, opts.Method))
	t.mergeHeadPins = append(t.mergeHeadPins, opts.ExpectedHeadSHA)
	t.lastMergeOpts = opts
	if t.mergeErr != nil {
		return gitealike.MergeResultDTO{}, t.mergeErr
	}
	now := time.Now().UTC().Truncate(time.Second)
	pr.State = "merged"
	pr.Merged = true
	pr.MergedAt = &now
	pr.Updated = now
	return gitealike.MergeResultDTO{Merged: true, SHA: "merged-sha", Message: "merged"}, nil
}

func (t *apiTestGitealikeTransport) CreatePullReview(
	_ context.Context,
	_ platform.RepoRef,
	number int,
	opts gitealike.ReviewOptions,
) (gitealike.ReviewDTO, error) {
	if t.findPull(number) == nil {
		return gitealike.ReviewDTO{}, platform.ErrNotFound
	}
	t.mutationCalls = append(t.mutationCalls, fmt.Sprintf("review:%d:%s:%s", number, opts.Body, opts.CommitID))
	t.lastReviewOpts = opts
	return gitealike.ReviewDTO{
		ID:        980,
		User:      gitealike.UserDTO{UserName: "mutation-bot"},
		State:     opts.State,
		Body:      opts.Body,
		Submitted: time.Now().UTC().Truncate(time.Second),
	}, nil
}

func (t *apiTestGitealikeTransport) findPull(number int) *gitealike.PullRequestDTO {
	for i := range t.pulls {
		if t.pulls[i].Index == number {
			return &t.pulls[i]
		}
	}
	return nil
}

func (t *apiTestGitealikeTransport) findIssue(number int) *gitealike.IssueDTO {
	for i := range t.issues {
		if t.issues[i].Index == number {
			return &t.issues[i]
		}
	}
	return nil
}

func upsertComment(comments []gitealike.CommentDTO, comment gitealike.CommentDTO) []gitealike.CommentDTO {
	for i := range comments {
		if comments[i].ID == comment.ID {
			comments[i] = comment
			return comments
		}
	}
	return append(comments, comment)
}

func assertUnsupportedCapabilityProblem(
	t *testing.T,
	body io.Reader,
	provider, host, capability string,
) {
	t.Helper()
	require := require.New(t)
	assert := assert.New(t)

	var problem struct {
		Title   string         `json:"title"`
		Status  int            `json:"status"`
		Detail  string         `json:"detail"`
		Code    string         `json:"code"`
		Details map[string]any `json:"details"`
	}
	require.NoError(json.NewDecoder(body).Decode(&problem))
	assert.Equal(http.StatusText(http.StatusConflict), problem.Title)
	assert.Equal(http.StatusConflict, problem.Status)
	assert.Contains(problem.Detail, "Unsupported provider capability")
	assert.Equal("unsupportedCapability", problem.Code,
		"top-level RFC 9457 code must be the camelCase wire literal")
	require.NotNil(problem.Details, "details must be present on unsupportedCapability problem")
	assert.Equal(capability, problem.Details["capability"])
	assert.Equal(provider, problem.Details["provider"])
	assert.Equal(host, problem.Details["platformHost"])
}

// countingTokenFileSource wraps a managed token-file source and records how
// many times the credential is resolved. Local-read endpoints must never
// resolve it, so the count stays at zero even after the file is emptied.
type countingTokenFileSource struct {
	inner *tokenauth.ManagedSource
	calls atomic.Int64
}

func (s *countingTokenFileSource) Token(ctx context.Context) (string, error) {
	s.calls.Add(1)
	return s.inner.Token(ctx)
}

func (s *countingTokenFileSource) Invalidate(rejectedToken string) {
	s.inner.Invalidate(rejectedToken)
}

func (s *countingTokenFileSource) Descriptor() tokenauth.Descriptor {
	return s.inner.Descriptor()
}

// TestAPILocalReadEndpointsServeDuringTokenRotationE2E proves that an
// already-cloned repo keeps serving diff, files, file-preview, and commits
// after its host token file is emptied mid-rotation. Those endpoints only run
// local git reads, so they must never resolve the credential; if they did, the
// briefly empty file would surface a missing-token error and break commit and
// diff views for repos that are already on disk.
func TestAPILocalReadEndpointsServeDuringTokenRotationE2E(t *testing.T) {
	runParallelServerTest(t)
	acquireRootWorkspaceGitSlot(t)
	require := require.New(t)
	assert := assert.New(t)
	ctx := t.Context()

	dir := t.TempDir()
	database := dbtest.Open(t)

	bareDir := filepath.Join(dir, "clones")
	bare, err := gitclone.New(bareDir, nil).ClonePathForContext(
		gitclone.WithRepositoryIdentity(ctx, "repo-acme-widget"),
		"github", "github.com", "acme", "widget",
	)
	require.NoError(err)
	require.NoError(os.MkdirAll(filepath.Dir(bare), 0o755))

	work := filepath.Join(dir, "work")
	gitfixture.Run(t, dir, "init", "--bare", "--initial-branch=main", bare)
	gitfixture.Run(t, dir, "clone", bare, work)
	gitfixture.Run(t, work, "config", "user.email", "test@test.com")
	gitfixture.Run(t, work, "config", "user.name", "Test")

	require.NoError(os.WriteFile(filepath.Join(work, "base.txt"), []byte("base\n"), 0o644))
	gitfixture.Run(t, work, "add", ".")
	gitfixture.Run(t, work, "commit", "-m", "base commit")
	gitfixture.Run(t, work, "push", "origin", "main")
	mergeBase := gitfixture.SHA(t, work, "HEAD")

	gitfixture.Run(t, work, "checkout", "-b", "feature")
	require.NoError(os.WriteFile(filepath.Join(work, "feature.txt"), []byte("feature\n"), 0o644))
	gitfixture.Run(t, work, "add", ".")
	gitfixture.Run(t, work, "commit", "-m", "feature commit")
	gitfixture.Run(t, work, "push", "origin", "feature")
	headSHA := gitfixture.SHA(t, work, "HEAD")

	// A token-file source modeling the credential that was valid when the
	// clone was created. Rotation empties the file before the reads below.
	tokenPath := filepath.Join(dir, "github-token")
	require.NoError(os.WriteFile(tokenPath, []byte("ghp_local_rotation_token\n"), 0o600))
	source := &countingTokenFileSource{
		inner: tokenauth.NewManagedSource(tokenauth.Descriptor{
			Key: tokenauth.Key{Platform: string(platform.KindGitHub), Host: "github.com"},
			Candidates: []tokenauth.Candidate{
				{Kind: tokenauth.SourceKindFile, FilePath: tokenPath},
			},
		}, tokenauth.Options{}),
	}

	clones := gitclone.New(bareDir, gitclone.HostSources{"github.com": source})
	syncer := ghclient.NewSyncer(
		map[string]ghclient.Client{"github.com": &mockGH{}},
		database, nil, defaultTestRepos, time.Minute, nil, nil,
	)
	t.Cleanup(syncer.Stop)
	srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{Clones: clones})
	t.Cleanup(func() { gracefulShutdown(t, srv) })
	client := setupTestClient(t, srv)

	seedPR(t, database, "acme", "widget", 1)
	repoID, err := database.UpsertRepo(ctx, verifiedGitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	require.NoError(database.UpdateDiffSHAs(ctx, repoID, 1, headSHA, mergeBase, mergeBase))

	// Rotation in progress: the token file is briefly empty. Any attempt to
	// resolve it now fails, so the local-read endpoints below must not try.
	require.NoError(os.WriteFile(tokenPath, []byte("\n"), 0o600))

	commitsResp, err := client.HTTP.GetPullCommitsWithResponse(ctx, &generated.GetPullCommitsRequestOptions{PathParams: &generated.GetPullCommitsPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}})
	require.NoError(err)
	require.Equal(http.StatusOK, commitsResp.StatusCode, string(commitsResp.Body))
	require.NotNil(commitsResp.JSON200)
	require.Len(commitsResp.JSON200.Commits, 1)
	assert.Equal(headSHA, commitsResp.JSON200.Commits[0].Sha)

	diffResp, err := client.HTTP.GetPullDiffWithResponse(ctx, &generated.GetPullDiffRequestOptions{PathParams: &generated.GetPullDiffPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}})
	require.NoError(err)
	require.Equal(http.StatusOK, diffResp.StatusCode, string(diffResp.Body))
	require.NotNil(diffResp.JSON200)
	require.Len(diffResp.JSON200.Files, 1)

	filesResp, err := client.HTTP.GetPullFilesWithResponse(ctx, &generated.GetPullFilesRequestOptions{PathParams: &generated.GetPullFilesPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}})
	require.NoError(err)
	require.Equal(http.StatusOK, filesResp.StatusCode, string(filesResp.Body))
	require.NotNil(filesResp.JSON200)
	require.Len(filesResp.JSON200.Files, 1)

	previewPath := "feature.txt"
	previewResp, err := client.HTTP.GetPullFilePreviewWithResponse(ctx, &generated.GetPullFilePreviewRequestOptions{PathParams: &generated.GetPullFilePreviewPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}, Query: &generated.GetPullFilePreviewQuery{Path: &previewPath}})
	require.NoError(err)
	require.Equal(http.StatusOK, previewResp.StatusCode, string(previewResp.Body))
	require.NotNil(previewResp.JSON200)
	assert.Equal(previewPath, previewResp.JSON200.Path)
	decoded, err := base64.StdEncoding.DecodeString(previewResp.JSON200.Content)
	require.NoError(err)
	assert.Equal("feature\n", string(decoded))

	// The local-read endpoints above never resolved the rotated-out token.
	assert.Zero(source.calls.Load())

	// Sanity: resolving the emptied file really does fail, so the zero count
	// means the reads skipped the source rather than finding a usable token.
	_, err = source.Token(ctx)
	require.ErrorIs(err, tokenauth.ErrMissingToken)
}

func cleanupServerTestTmux(owner *testtmux.Owner) error {
	if owner == nil {
		return nil
	}
	return owner.Cleanup()
}

type rawProblemDetail struct {
	Type    string         `json:"type"`
	Title   string         `json:"title"`
	Status  int            `json:"status"`
	Detail  string         `json:"detail"`
	Code    string         `json:"code"`
	Details map[string]any `json:"details"`
	Errors  []struct {
		Message  string `json:"message"`
		Location string `json:"location"`
		Value    any    `json:"value"`
	} `json:"errors"`
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

func TestAPIHeadRepoKindClassifiesSameRepoForkAndUnknown(t *testing.T) {
	require := require.New(t)
	srv, database := setupTestServer(t)
	repoID, err := database.UpsertRepo(
		t.Context(),
		verifiedGitHubRepoIdentity("github.com", "acme", "widget"),
	)
	require.NoError(err)
	now := time.Now().UTC().Truncate(time.Second)
	for index, test := range []struct {
		cloneURL string
		want     string
	}{
		{cloneURL: "https://github.com/acme/widget.git", want: "same_repo"},
		{cloneURL: "https://github.com/contributor/widget.git", want: "fork"},
		{cloneURL: "", want: "unknown"},
	} {
		number := 900 + index
		_, err := database.UpsertMergeRequest(t.Context(), &db.MergeRequest{
			RepoID: repoID, PlatformID: int64(number), Number: number,
			URL:   "https://github.com/acme/widget/pull/" + strconv.Itoa(number),
			Title: "Head repository classification", Author: "alice", State: "open",
			HeadBranch: "feature/head-repo", BaseBranch: "main",
			HeadRepoCloneURL: test.cloneURL,
			CreatedAt:        now, UpdatedAt: now, LastActivityAt: now,
		})
		require.NoError(err)
		response := testutil.DoJSON(
			t, srv, http.MethodGet,
			"/api/v1/pulls/gh/acme/widget/"+strconv.Itoa(number),
			nil,
		)
		require.Equal(http.StatusOK, response.Code, response.Body.String())
		var detail pullapi.MergeRequestDetailResponse
		require.NoError(json.Unmarshal(response.Body.Bytes(), &detail))
		assert.Equal(t, test.want, string(detail.HeadRepoKind))
	}
}
