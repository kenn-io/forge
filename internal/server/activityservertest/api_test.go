package activityservertest

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
	"testing"
	"time"

	gh "github.com/google/go-github/v91/github"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/apiclient"
	"go.kenn.io/forge/internal/apiclient/generated"
	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/db"
	ghclient "go.kenn.io/forge/internal/github"
	"go.kenn.io/forge/internal/platformdb"
	"go.kenn.io/forge/internal/server"
	"go.kenn.io/forge/internal/server/itemapi"
	"go.kenn.io/forge/internal/testutil"
	"go.kenn.io/forge/internal/testutil/dbtest"
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

// setupTestServer opens a temp DB, builds a Server, and returns both.
func setupTestServer(t *testing.T) (*server.Server, *db.DB, *ghclient.Syncer) {
	t.Helper()
	return setupTestServerWithMock(t, &mockGH{})
}

func setupNotificationsEnabledTestServer(t *testing.T) (*server.Server, *db.DB) {
	t.Helper()
	database := dbtest.Open(t)
	syncer := ghclient.NewSyncer(
		map[string]ghclient.Client{"github.com": &mockGH{}},
		database,
		nil,
		defaultTestRepos,
		time.Minute,
		nil,
		nil,
	)
	t.Cleanup(syncer.Stop)
	srv := server.New(
		database, syncer, nil, "/",
		notificationsEnabledConfig(), server.ServerOptions{},
	)
	t.Cleanup(func() { gracefulShutdown(t, srv) })
	return srv, database
}

func setupTestServerWithMock(t *testing.T, mock *mockGH) (*server.Server, *db.DB, *ghclient.Syncer) {
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
) (*server.Server, *db.DB, *ghclient.Syncer) {
	return setupTestServerWithReposAndOptions(t, mock, repos, server.ServerOptions{})
}

func setupTestServerWithReposAndOptions(
	t *testing.T, mock *mockGH, repos []ghclient.RepoRef, options server.ServerOptions,
) (*server.Server, *db.DB, *ghclient.Syncer) {
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
	return srv, database, syncer
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

func withSeedPRAuthor(author string) seedPROpt {
	return func(pr *db.MergeRequest) { pr.Author = author }
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

func TestAPIListPullsUsesProviderActivityAfterIndexSync(t *testing.T) {
	runParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	base := time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC)
	staleDerivedActivity := base.Add(2 * time.Hour)
	otherActivity := base.Add(time.Hour)

	str := func(v string) *string { return &v }
	mock := &mockGH{
		listOpenPullRequestsFn: func(_ context.Context, _, _ string) ([]*gh.PullRequest, error) {
			firstNumber := 1
			firstID := int64(1001)
			secondNumber := 2
			secondID := int64(1002)
			return []*gh.PullRequest{
				{
					ID:        &firstID,
					Number:    &firstNumber,
					Title:     str("Preserve activity"),
					State:     str("open"),
					HTMLURL:   str("https://github.com/acme/widget/pull/1"),
					User:      &gh.User{Login: str("octocat")},
					CreatedAt: &gh.Timestamp{Time: base},
					UpdatedAt: &gh.Timestamp{Time: base},
					Head:      &gh.PullRequestBranch{Ref: str("feature-one"), SHA: str("head-one")},
					Base:      &gh.PullRequestBranch{Ref: str("main"), SHA: str("base-one")},
				},
				{
					ID:        &secondID,
					Number:    &secondNumber,
					Title:     str("Other activity"),
					State:     str("open"),
					HTMLURL:   str("https://github.com/acme/widget/pull/2"),
					User:      &gh.User{Login: str("octocat")},
					CreatedAt: &gh.Timestamp{Time: base},
					UpdatedAt: &gh.Timestamp{Time: otherActivity},
					Head:      &gh.PullRequestBranch{Ref: str("feature-two"), SHA: str("head-two")},
					Base:      &gh.PullRequestBranch{Ref: str("main"), SHA: str("base-two")},
				},
			}, nil
		},
	}
	srv, database, syncer := setupTestServerWithMock(t, mock)
	seedPR(t, database, "acme", "widget", 1,
		withSeedPRTitle("Preserve activity"),
		withSeedPRTimes(base, base, staleDerivedActivity),
	)
	seedPR(t, database, "acme", "widget", 2,
		withSeedPRTitle("Other activity"),
		withSeedPRTimes(base, otherActivity, otherActivity),
	)
	client := setupTestClient(t, srv)

	syncer.RunOnce(ctx)

	resp, err := client.HTTP.ListPullsWithResponse(ctx, &generated.ListPullsRequestOptions{})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)
	require.NotNil(resp.JSON200)
	require.Len(*resp.JSON200, 2)
	assert.Equal(int64(2), (*resp.JSON200)[0].Number)
	assert.Equal(otherActivity, (*resp.JSON200)[0].LastActivityAt.UTC())
	assert.Equal(int64(1), (*resp.JSON200)[1].Number)
	assert.Equal(base, (*resp.JSON200)[1].LastActivityAt.UTC())
}

func TestAPIListActivity(t *testing.T) {
	runParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	srv, database, _ := setupTestServer(t)
	client := setupTestClient(t, srv)

	prID := seedPR(t, database, "acme", "widget", 1)
	ctx := t.Context()
	repo, err := database.GetRepoByIdentity(ctx, verifiedGitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	require.NotNil(repo)
	require.NoError(database.UpdateMergeRequestAssignees(ctx, repo.ID, prID, nil))

	require.NoError(database.UpsertMREvents(ctx, []db.MREvent{
		{
			MergeRequestID: prID,
			EventType:      "issue_comment",
			Author:         "reviewer",
			Body:           "Looks good",
			CreatedAt:      time.Now().UTC(),
			DedupeKey:      "comment-1",
		},
	}))

	since := time.Now().UTC().AddDate(0, 0, -7).Format(time.RFC3339)
	resp, err := client.HTTP.ListActivityWithResponse(ctx, &generated.ListActivityRequestOptions{Query: &generated.ListActivityQuery{Since: &since}})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)
	require.NotNil(resp.JSON200)
	require.NotNil(resp.JSON200.Items)
	assert.NotEmpty(resp.JSON200.Items,
		"activity feed should contain PR and comment items")
	assert.Equal("github.com", resp.JSON200.Items[0].PlatformHost)

	collapsed := testutil.DoJSON(
		t,
		srv,
		http.MethodGet,
		"/api/v1/activity?since="+url.QueryEscape(since)+"&projection=collapsed&unassigned=true",
		nil,
	)
	require.Equal(http.StatusOK, collapsed.Code)
	var collapsedBody struct {
		Items        []itemapi.ActivityItemResponse    `json:"items"`
		ItemActivity []itemapi.ActivitySubjectResponse `json:"item_activity"`
		EventCursor  string                            `json:"event_cursor"`
	}
	require.NoError(json.NewDecoder(collapsed.Body).Decode(&collapsedBody))
	assert.Empty(collapsedBody.Items, "collapsed projection must omit pull request child events")
	require.Len(collapsedBody.ItemActivity, 1)
	assert.NotEmpty(collapsedBody.ItemActivity[0].EventLedgerRevision,
		"collapsed parents must identify the exact child ledger snapshot")
	assert.NotEmpty(collapsedBody.EventCursor, "cursor must cover omitted child events")

	delta := testutil.DoJSON(
		t,
		srv,
		http.MethodGet,
		"/api/v1/activity?since="+url.QueryEscape(since)+"&projection=events&after="+
			url.QueryEscape(collapsedBody.EventCursor),
		nil)

	require.Equal(http.StatusOK, delta.Code)
	var deltaBody struct {
		Items        []itemapi.ActivityItemResponse    `json:"items"`
		ItemActivity []itemapi.ActivitySubjectResponse `json:"item_activity"`
		EventCursor  string                            `json:"event_cursor"`
	}
	require.NoError(json.NewDecoder(delta.Body).Decode(&deltaBody))
	assert.Empty(deltaBody.Items)
	assert.Empty(deltaBody.ItemActivity, "event deltas must not resend parent summaries")
	assert.Equal(collapsedBody.EventCursor, deltaBody.EventCursor)

	thread := testutil.DoJSON(
		t,
		srv,
		http.MethodGet,
		"/api/v1/activity/thread-events?provider=github&platform_host=github.com"+
			"&platform_repo_id=repo-acme-widget&item_type=pr&item_number=1&since="+
			url.QueryEscape(since),
		nil)

	require.Equal(http.StatusOK, thread.Code)
	var threadBody itemapi.ActivityResponse
	require.NoError(json.NewDecoder(thread.Body).Decode(&threadBody))
	require.Len(threadBody.Items, 2)
	assert.Empty(threadBody.ItemActivity)

	require.NoError(database.UpdateMergeRequestAssignees(ctx, repo.ID, prID, []string{"alice"}))
	assignedThread := testutil.DoJSON(
		t,
		srv,
		http.MethodGet,
		"/api/v1/activity/thread-events?provider=github&platform_host=github.com"+
			"&platform_repo_id=repo-acme-widget&item_type=pr&item_number=1&since="+
			url.QueryEscape(since)+"&unassigned=true",
		nil,
	)
	require.Equal(http.StatusOK, assignedThread.Code)
	var assignedThreadBody itemapi.ActivityResponse
	require.NoError(json.NewDecoder(assignedThread.Body).Decode(&assignedThreadBody))
	assert.Empty(assignedThreadBody.Items)

	filteredThread := testutil.DoJSON(
		t,
		srv,
		http.MethodGet,
		"/api/v1/activity/thread-events?provider=github&platform_host=github.com"+
			"&platform_repo_id=repo-acme-widget&item_type=pr&item_number=1&since="+
			url.QueryEscape(since)+"&types=comment&search="+url.QueryEscape("Looks good"),
		nil)

	require.Equal(http.StatusOK, filteredThread.Code)
	var filteredThreadBody itemapi.ActivityResponse
	require.NoError(json.NewDecoder(filteredThread.Body).Decode(&filteredThreadBody))
	require.Len(filteredThreadBody.Items, 1)
	assert.Equal("comment", filteredThreadBody.Items[0].ActivityType)

	search := "reviewer"
	filtered, err := client.HTTP.ListActivityWithResponse(ctx, &generated.ListActivityRequestOptions{Query: &generated.ListActivityQuery{Since: &since, Search: &search}})
	require.NoError(err)
	require.Equal(http.StatusOK, filtered.StatusCode)
	require.NotNil(filtered.JSON200)
	require.NotNil(filtered.JSON200.Items)
	require.Len(filtered.JSON200.Items, 1)
	assert.Equal("comment", filtered.JSON200.Items[0].ActivityType)
	assert.Equal("reviewer", filtered.JSON200.Items[0].Author)

	itemNumber := "#1"
	byNumber, err := client.HTTP.ListActivityWithResponse(ctx, &generated.ListActivityRequestOptions{Query: &generated.ListActivityQuery{Since: &since, Search: &itemNumber}})
	require.NoError(err)
	require.Equal(http.StatusOK, byNumber.StatusCode)
	require.NotNil(byNumber.JSON200)
	require.NotNil(byNumber.JSON200.Items)
	require.Len(byNumber.JSON200.Items, 2)
	for _, item := range byNumber.JSON200.Items {
		assert.Equal(int64(1), item.ItemNumber)
	}

	whitespace := " \t "
	unfiltered, err := client.HTTP.ListActivityWithResponse(ctx, &generated.ListActivityRequestOptions{Query: &generated.ListActivityQuery{Since: &since, Search: &whitespace}})
	require.NoError(err)
	require.Equal(http.StatusOK, unfiltered.StatusCode)
	require.NotNil(unfiltered.JSON200)
	require.NotNil(unfiltered.JSON200.Items)
	assert.Len(unfiltered.JSON200.Items, len(resp.JSON200.Items))
}

func TestAPIListCollapsedActivityHonorsLimit(t *testing.T) {
	runParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	srv, database, _ := setupTestServer(t)
	now := time.Now().UTC().Truncate(time.Second)
	for number := 1; number <= 11; number++ {
		activityAt := now.Add(-time.Duration(number) * time.Minute)
		seedPR(
			t, database, "acme", "widget", number,
			withSeedPRTimes(activityAt, activityAt, activityAt),
		)
	}

	since := url.QueryEscape(now.Add(-7 * 24 * time.Hour).Format(time.RFC3339))
	rr := testutil.DoJSON(
		t,
		srv,
		http.MethodGet,
		"/api/v1/activity?since="+since+"&projection=collapsed&limit=10",
		nil)

	require.Equal(http.StatusOK, rr.Code)
	var body itemapi.ActivityResponse
	require.NoError(json.NewDecoder(rr.Body).Decode(&body))
	require.Len(body.ItemActivity, 10)
	assert.True(body.ItemActivityCapped)
	assert.Equal(1, body.ItemActivity[0].ItemNumber)
}

func TestAPIListCollapsedActivitySearchLimitCountsDistinctParents(t *testing.T) {
	runParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	srv, database, _ := setupTestServer(t)
	ctx := t.Context()
	base := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Second)

	olderPRID := seedPR(
		t, database, "acme", "widget", 1,
		withSeedPRTitle("Unrelated older parent"),
		withSeedPRTimes(base, base, base),
	)
	require.NoError(database.UpsertMREvents(ctx, []db.MREvent{{
		MergeRequestID: olderPRID,
		EventType:      "issue_comment",
		Author:         "reviewer",
		Body:           "needle in an older event",
		CreatedAt:      base.Add(time.Minute),
		DedupeKey:      "older-search-match",
	}}))
	newerPRID := seedPR(
		t, database, "acme", "widget", 2,
		withSeedPRTitle("Unrelated newer parent"),
		withSeedPRTimes(base, base, base),
	)
	newerEvents := make([]db.MREvent, 31)
	for i := range newerEvents {
		newerEvents[i] = db.MREvent{
			MergeRequestID: newerPRID,
			EventType:      "issue_comment",
			Author:         "reviewer",
			Body:           "needle repeated",
			CreatedAt:      base.Add(2*time.Minute + time.Duration(i)*time.Second),
			DedupeKey:      fmt.Sprintf("newer-search-match-%d", i),
		}
	}
	require.NoError(database.UpsertMREvents(ctx, newerEvents))

	since := url.QueryEscape(base.Add(-time.Minute).Format(time.RFC3339))
	rr := testutil.DoJSON(
		t,
		srv,
		http.MethodGet,
		"/api/v1/activity?since="+since+"&projection=collapsed&limit=30&search=needle",
		nil)

	require.Equal(http.StatusOK, rr.Code)
	var body itemapi.ActivityResponse
	require.NoError(json.NewDecoder(rr.Body).Decode(&body))
	require.Len(body.ItemActivity, 2)
	assert.Equal([]int{2, 1}, []int{body.ItemActivity[0].ItemNumber, body.ItemActivity[1].ItemNumber})
	assert.False(body.ItemActivityCapped)
}

func TestAPIListCollapsedActivityIncludesParentRecentOnlyByNotification(t *testing.T) {
	runParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	srv, database := setupNotificationsEnabledTestServer(t)
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Second)
	oldActivity := now.Add(-30 * 24 * time.Hour)
	seedPR(
		t, database, "acme", "widget", 71,
		withSeedPRTitle("Old pull with recent notification"),
		withSeedPRTimes(oldActivity, oldActivity, oldActivity),
	)
	number := 71
	notificationAt := now.Add(-time.Hour)
	require.NoError(database.UpsertNotifications(ctx, []db.Notification{{
		Platform: "github", PlatformHost: "github.com",
		PlatformNotificationID: "api-recent-old-pull",
		RepoOwner:              "acme", RepoName: "widget",
		SubjectType: "PullRequest", SubjectTitle: "Old pull with recent notification",
		WebURL:     "https://github.com/acme/widget/pull/71",
		ItemNumber: &number, ItemType: "pr", ItemAuthor: "contributor",
		Reason: "mention", Unread: true,
		SourceUpdatedAt: notificationAt, SyncedAt: notificationAt,
	}}))

	since := url.QueryEscape(now.Add(-7 * 24 * time.Hour).Format(time.RFC3339))
	rr := testutil.DoJSON(
		t,
		srv,
		http.MethodGet,
		"/api/v1/activity?since="+since+"&projection=collapsed",
		nil)

	require.Equal(http.StatusOK, rr.Code)
	var body struct {
		Items        []itemapi.ActivityItemResponse    `json:"items"`
		ItemActivity []itemapi.ActivitySubjectResponse `json:"item_activity"`
	}
	require.NoError(json.NewDecoder(rr.Body).Decode(&body))
	assert.Empty(body.Items)
	require.Len(body.ItemActivity, 1)
	assert.Equal(71, body.ItemActivity[0].ItemNumber)
	assert.Equal(itemapi.FormatUTCRFC3339(notificationAt), body.ItemActivity[0].ActivityAt)

	filtered := testutil.DoJSON(
		t,
		srv,
		http.MethodGet,
		"/api/v1/activity?since="+since+"&projection=collapsed&types=comment",
		nil)

	require.Equal(http.StatusOK, filtered.Code)
	var filteredBody struct {
		Items        []itemapi.ActivityItemResponse    `json:"items"`
		ItemActivity []itemapi.ActivitySubjectResponse `json:"item_activity"`
	}
	require.NoError(json.NewDecoder(filtered.Body).Decode(&filteredBody))
	assert.Empty(filteredBody.Items)
	assert.Empty(filteredBody.ItemActivity,
		"hidden notifications must not pull otherwise-old parents into the window")
}

func TestAPIListCollapsedActivityRetainsVisibleEventsForBotParents(t *testing.T) {
	runParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	srv, database, _ := setupTestServer(t)
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Second)
	prID := seedPR(
		t, database, "acme", "widget", 81,
		withSeedPRTitle("Bot-authored pull request"),
		withSeedPRAuthor("dependabot[bot]"),
		withSeedPRTimes(now.Add(-time.Hour), now.Add(-time.Hour), now.Add(-time.Hour)),
	)
	require.NoError(database.UpsertMREvents(ctx, []db.MREvent{{
		MergeRequestID: prID,
		EventType:      "issue_comment",
		Author:         "human-reviewer",
		Body:           "Visible human comment",
		CreatedAt:      now,
		DedupeKey:      "api-human-comment-on-bot-parent",
	}}))

	since := url.QueryEscape(now.Add(-7 * 24 * time.Hour).Format(time.RFC3339))
	rr := testutil.DoJSON(
		t,
		srv,
		http.MethodGet,
		"/api/v1/activity?since="+since+"&projection=collapsed&hide_bots=true",
		nil)

	require.Equal(http.StatusOK, rr.Code)
	var body struct {
		Items        []itemapi.ActivityItemResponse    `json:"items"`
		ItemActivity []itemapi.ActivitySubjectResponse `json:"item_activity"`
	}
	require.NoError(json.NewDecoder(rr.Body).Decode(&body))
	require.Len(body.Items, 1)
	assert.Equal("comment", body.Items[0].ActivityType)
	assert.Equal(81, body.Items[0].ItemNumber)
	assert.Equal("human-reviewer", body.Items[0].Author)
	assert.Empty(body.ItemActivity)
}

func TestAPIListActivityReturnsRecentParentWhenItsVisibleEventsAreFiltered(t *testing.T) {
	runParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	srv, database, _ := setupTestServer(t)
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Second)
	createdAt := now.Add(-30 * 24 * time.Hour)
	activityAt := now.Add(-time.Hour)

	prID := seedPR(
		t, database, "acme", "widget", 77,
		withSeedPRTitle("Old pull with recent hidden activity"),
		withSeedPRTimes(createdAt, activityAt, activityAt),
	)
	require.NoError(database.InsertWorkspace(ctx, &db.Workspace{
		ID: "ws-hidden-parent", Platform: "github", PlatformHost: "github.com",
		RepoOwner: "acme", RepoName: "widget", ItemType: db.WorkspaceItemTypePullRequest,
		ItemNumber: 77, WorktreePath: t.TempDir(), Status: "ready",
	}))
	require.NoError(database.UpsertMREvents(ctx, []db.MREvent{{
		MergeRequestID: prID,
		EventType:      "issue_comment",
		Author:         "reviewer",
		CreatedAt:      activityAt,
		DedupeKey:      "api-hidden-parent-comment",
	}}))

	since := url.QueryEscape(now.Add(-7 * 24 * time.Hour).Format(time.RFC3339))
	rr := testutil.DoJSON(
		t,
		srv,
		http.MethodGet,
		"/api/v1/activity?since="+since+"&types=commit&item_types=pr,repo",
		nil)

	require.Equal(http.StatusOK, rr.Code)
	var body struct {
		Items        []itemapi.ActivityItemResponse    `json:"items"`
		ItemActivity []itemapi.ActivitySubjectResponse `json:"item_activity"`
	}
	require.NoError(json.NewDecoder(rr.Body).Decode(&body))
	assert.Empty(body.Items, "the comment must remain hidden by the event filter")
	require.Len(body.ItemActivity, 1)
	assert.Equal(77, body.ItemActivity[0].ItemNumber)
	assert.Equal("pr", body.ItemActivity[0].ItemType)
	assert.Equal("Old pull with recent hidden activity", body.ItemActivity[0].ItemTitle)
	assert.Equal(itemapi.FormatUTCRFC3339(activityAt), body.ItemActivity[0].ActivityAt)
	require.NotNil(body.ItemActivity[0].Workspace)
	assert.Equal("ws-hidden-parent", body.ItemActivity[0].Workspace.ID)
}

func TestAPIListActivityIncrementalSearchReturnsParentsMatchedByProviderEvents(t *testing.T) {
	runParallelServerTest(t)
	srv, database, _ := setupTestServer(t)
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Second)
	activityAt := now.Add(-time.Hour)

	bodyMatchID := seedPR(
		t, database, "acme", "widget", 78,
		withSeedPRTitle("Parent with an unrelated title"),
		withSeedPRTimes(activityAt, activityAt, activityAt),
	)
	actorMatchID := seedPR(
		t, database, "acme", "widget", 79,
		withSeedPRTitle("Another unrelated parent"),
		withSeedPRTimes(activityAt, activityAt, activityAt),
	)
	require.NoError(t, database.UpsertMREvents(ctx, []db.MREvent{
		{
			MergeRequestID: bodyMatchID,
			EventType:      "issue_comment",
			Author:         "reviewer",
			Body:           "contains body-match-term",
			CreatedAt:      activityAt,
			DedupeKey:      "incremental-search-body-match",
		},
		{
			MergeRequestID: actorMatchID,
			EventType:      "issue_comment",
			Author:         "actor-match-term",
			Body:           "unrelated comment",
			CreatedAt:      activityAt,
			DedupeKey:      "incremental-search-actor-match",
		},
	}))

	since := url.QueryEscape(now.Add(-7 * 24 * time.Hour).Format(time.RFC3339))
	after := url.QueryEscape(db.EncodeCursor(now, "mr_event", 1))
	for _, tc := range []struct {
		name       string
		search     string
		itemNumber int
	}{
		{name: "event body", search: "body-match-term", itemNumber: 78},
		{name: "event actor", search: "actor-match-term", itemNumber: 79},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			rr := testutil.DoJSON(
				t,
				srv,
				http.MethodGet,
				"/api/v1/activity?since="+since+"&after="+after+"&search="+url.QueryEscape(tc.search),
				nil)

			require.Equal(http.StatusOK, rr.Code)
			var body struct {
				Items        []itemapi.ActivityItemResponse    `json:"items"`
				ItemActivity []itemapi.ActivitySubjectResponse `json:"item_activity"`
			}
			require.NoError(json.NewDecoder(rr.Body).Decode(&body))
			require.Empty(body.Items, "the matching event is behind the incremental cursor")
			require.Len(body.ItemActivity, 1)
			require.Equal(tc.itemNumber, body.ItemActivity[0].ItemNumber)
		})
	}
}

func TestAPIListActivitySeparatesEventAndParentSnapshotCaps(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	srv, database, _ := setupTestServer(t)
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Second)
	repoID, err := database.UpsertRepo(ctx, verifiedGitHubRepoIdentity("github.com", "acme", "many-parents"))
	require.NoError(err)

	_, err = database.WriteDB().ExecContext(ctx, `
		WITH digits(n) AS (
			VALUES (0), (1), (2), (3), (4), (5), (6), (7), (8), (9)
		), numbers(n) AS (
			SELECT ones.n + 10 * tens.n + 100 * hundreds.n + 1000 * thousands.n + 1
			FROM digits AS ones
			CROSS JOIN digits AS tens
			CROSS JOIN digits AS hundreds
			CROSS JOIN digits AS thousands
		)
		INSERT INTO forge_merge_requests (
			repo_id, platform_id, number, url, title, author, state,
			created_at, updated_at, last_activity_at
		)
		SELECT ?, n, n,
		       'https://github.com/acme/many-parents/pull/' || n,
		       'Parent ' || n, 'testuser', 'open', ?, ?, ?
		FROM numbers
		WHERE n <= ?`,
		repoID, now, now, now, itemapi.ActivitySafetyCap+1,
	)
	require.NoError(err)

	since := url.QueryEscape(now.Add(-time.Minute).Format(time.RFC3339))
	rr := testutil.DoJSON(
		t,
		srv,
		http.MethodGet,
		"/api/v1/activity?since="+since+"&types=commit&item_types=pr,repo",
		nil)

	require.Equal(http.StatusOK, rr.Code)
	var body struct {
		Items              []itemapi.ActivityItemResponse    `json:"items"`
		ItemActivity       []itemapi.ActivitySubjectResponse `json:"item_activity"`
		Capped             bool                              `json:"capped"`
		ItemActivityCapped bool                              `json:"item_activity_capped"`
	}
	require.NoError(json.NewDecoder(rr.Body).Decode(&body))
	assert.Empty(body.Items)
	require.Len(body.ItemActivity, itemapi.ActivitySafetyCap)
	assert.False(body.Capped, "parent snapshot overflow must not report event overflow")
	assert.True(body.ItemActivityCapped)
}

func TestAPIListActivityFiltersByAuthorAndListsScopedCandidates(t *testing.T) {
	runParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	srv, database, _ := setupTestServer(t)
	ctx := t.Context()
	base := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)

	trackedPRID := seedPR(
		t, database, "acme", "widget", 1,
		withSeedPRAuthor("Item Owner"),
		withSeedPRTimes(base, base, base),
	)
	require.NoError(database.UpsertMREvents(ctx, []db.MREvent{
		{
			MergeRequestID: trackedPRID,
			EventType:      "issue_comment",
			Author:         "Reviewer",
			CreatedAt:      base.Add(time.Minute),
			DedupeKey:      "tracked-reviewer",
		},
		{
			MergeRequestID: trackedPRID,
			EventType:      "issue_comment",
			Author:         "reviewer-bot",
			CreatedAt:      base.Add(2 * time.Minute),
			DedupeKey:      "tracked-reviewer-bot",
		},
	}))
	seedPR(
		t, database, "acme", "untracked", 1,
		withSeedPRAuthor("Hidden Actor"),
		withSeedPRTimes(base.Add(3*time.Minute), base.Add(3*time.Minute), base.Add(3*time.Minute)),
	)

	since := base.Add(-time.Minute).Format(time.RFC3339)
	feed := testutil.DoJSON(
		t,
		srv,
		http.MethodGet,
		"/api/v1/activity?since="+url.QueryEscape(since)+"&author=ITEM%20OWNER",
		nil)

	require.Equal(http.StatusOK, feed.Code)
	var feedBody itemapi.ActivityResponse
	require.NoError(json.Unmarshal(feed.Body.Bytes(), &feedBody))
	require.Len(feedBody.Items, 3)
	for _, item := range feedBody.Items {
		assert.Equal("Item Owner", item.ItemAuthor)
	}

	commenterFeed := testutil.DoJSON(
		t,
		srv,
		http.MethodGet,
		"/api/v1/activity?since="+url.QueryEscape(since)+"&author=REVIEWER",
		nil)

	require.Equal(http.StatusOK, commenterFeed.Code)
	var commenterFeedBody itemapi.ActivityResponse
	require.NoError(json.Unmarshal(commenterFeed.Body.Bytes(), &commenterFeedBody))
	assert.Empty(commenterFeedBody.Items)

	candidates := testutil.DoJSON(
		t,
		srv,
		http.MethodGet,
		"/api/v1/activity/authors?since="+url.QueryEscape(since),
		nil)

	require.Equal(http.StatusOK, candidates.Code)
	var candidateBody struct {
		Authors []string `json:"authors"`
	}
	require.NoError(json.Unmarshal(candidates.Body.Bytes(), &candidateBody))
	assert.Equal([]string{"Item Owner"}, candidateBody.Authors)
	assert.NotContains(candidateBody.Authors, "Hidden Actor")
}

func TestAPIListActivityReturnsParentRecencyWhenCommitEventsAreFiltered(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	srv, database, _ := setupTestServer(t)
	ctx := t.Context()
	base := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	// The hidden commit is the newest rendered ledger event, so it defines
	// the parent's recency; the provider's later updated_at does not.
	parentActivityAt := base.Add(19 * time.Minute)

	prID := seedPR(
		t, database, "acme", "widget", 1,
		withSeedPRTimes(base, base.Add(20*time.Minute), base.Add(20*time.Minute)),
	)
	require.NoError(database.UpsertMREvents(ctx, []db.MREvent{
		{
			MergeRequestID: prID,
			EventType:      "issue_comment",
			Author:         "reviewer",
			CreatedAt:      base.Add(10 * time.Minute),
			DedupeKey:      "visible-comment",
		},
		{
			MergeRequestID: prID,
			EventType:      "commit",
			Author:         "owner",
			CreatedAt:      base.Add(19 * time.Minute),
			DedupeKey:      "filtered-commit",
		},
	}))

	since := base.Add(-time.Minute).Format(time.RFC3339)
	rr := testutil.DoJSON(
		t,
		srv,
		http.MethodGet,
		"/api/v1/activity?since="+url.QueryEscape(since)+"&types=comment",
		nil)

	require.Equal(http.StatusOK, rr.Code)
	var body itemapi.ActivityResponse
	require.NoError(json.Unmarshal(rr.Body.Bytes(), &body))
	require.Len(body.Items, 1)
	require.Equal("comment", body.Items[0].ActivityType)
	require.Equal(parentActivityAt.Format(time.RFC3339), body.Items[0].ItemLastActivityAt)
}

func TestAPIListActivityAppliesTrackedRepoScopeBeforeAuthorLimit(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	srv, database := setupNotificationsEnabledTestServer(t)
	ctx := t.Context()
	base := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Second)

	trackedPRID := seedPR(
		t, database, "acme", "widget", 1,
		withSeedPRAuthor("Item Owner"),
		withSeedPRTimes(base, base, base),
	)
	require.NoError(database.UpsertMREvents(ctx, []db.MREvent{{
		MergeRequestID: trackedPRID,
		EventType:      "issue_comment",
		Author:         "Reviewer",
		CreatedAt:      base.Add(time.Minute),
		DedupeKey:      "tracked-reviewer",
	}}))

	untrackedPRID := seedPR(
		t, database, "acme", "untracked", 1,
		withSeedPRAuthor("Item Owner"),
		withSeedPRTimes(base, base, base),
	)
	untrackedEvents := make([]db.MREvent, itemapi.ActivitySafetyCap+1)
	for i := range untrackedEvents {
		untrackedEvents[i] = db.MREvent{
			MergeRequestID: untrackedPRID,
			EventType:      "issue_comment",
			Author:         "Reviewer",
			CreatedAt:      base.Add(2*time.Minute + time.Duration(i)*time.Second),
			DedupeKey:      fmt.Sprintf("untracked-reviewer-%d", i),
		}
	}
	require.NoError(database.UpsertMREvents(ctx, untrackedEvents))

	since := base.Add(-time.Minute).Format(time.RFC3339)
	feed := testutil.DoJSON(
		t,
		srv,
		http.MethodGet,
		"/api/v1/activity?since="+url.QueryEscape(since)+"&author=ITEM%20OWNER",
		nil)

	require.Equal(http.StatusOK, feed.Code)
	var feedBody itemapi.ActivityResponse
	require.NoError(json.Unmarshal(feed.Body.Bytes(), &feedBody))
	require.Len(feedBody.Items, 2)
	for _, item := range feedBody.Items {
		assert.Equal("acme", item.RepoOwner)
		assert.Equal("widget", item.RepoName)
		assert.Equal("Item Owner", item.ItemAuthor)
	}
	assert.False(feedBody.Capped)
}

func TestAPIListActivitySearchReportsParentTruncationWhenMatchesOverflowEventCap(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	srv, database, _ := setupTestServer(t)
	ctx := t.Context()
	base := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Second)

	// The older parent matches the search only through its event body, and
	// that event sits behind more than a full event page of newer matches on
	// another parent, so it can only be recognised as a truncated parent.
	olderPRID := seedPR(
		t, database, "acme", "widget", 1,
		withSeedPRTitle("Unrelated older parent"),
		withSeedPRTimes(base, base, base),
	)
	require.NoError(database.UpsertMREvents(ctx, []db.MREvent{{
		MergeRequestID: olderPRID,
		EventType:      "issue_comment",
		Author:         "reviewer",
		Body:           "needle in an older event",
		CreatedAt:      base.Add(time.Minute),
		DedupeKey:      "older-needle",
	}}))
	noisyPRID := seedPR(
		t, database, "acme", "widget", 2,
		withSeedPRTitle("Unrelated noisy parent"),
		withSeedPRTimes(base, base, base),
	)
	noisyEvents := make([]db.MREvent, itemapi.ActivitySafetyCap+1)
	for i := range noisyEvents {
		noisyEvents[i] = db.MREvent{
			MergeRequestID: noisyPRID,
			EventType:      "issue_comment",
			Author:         "reviewer",
			Body:           "needle repeated",
			CreatedAt:      base.Add(2*time.Minute + time.Duration(i)*time.Second),
			DedupeKey:      fmt.Sprintf("noisy-needle-%d", i),
		}
	}
	require.NoError(database.UpsertMREvents(ctx, noisyEvents))

	since := url.QueryEscape(base.Add(-time.Minute).Format(time.RFC3339))
	rr := testutil.DoJSON(t, srv, http.MethodGet, "/api/v1/activity?since="+since+"&search=needle", nil)
	require.Equal(http.StatusOK, rr.Code)
	var body struct {
		Items              []itemapi.ActivityItemResponse    `json:"items"`
		ItemActivity       []itemapi.ActivitySubjectResponse `json:"item_activity"`
		Capped             bool                              `json:"capped"`
		ItemActivityCapped bool                              `json:"item_activity_capped"`
	}
	require.NoError(json.NewDecoder(rr.Body).Decode(&body))
	require.Len(body.Items, itemapi.ActivitySafetyCap)
	assert.True(body.Capped)
	assert.True(body.ItemActivityCapped,
		"parents matched only through truncated events must be reported as truncated")
	require.Len(body.ItemActivity, 1)
	assert.Equal(2, body.ItemActivity[0].ItemNumber)
}

func TestAPIListActivityReturnsDefaultBranchActivity(t *testing.T) {
	runParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	srv, database, _ := setupTestServer(t)
	ctx := t.Context()
	base := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	repoID, err := database.UpsertRepo(ctx, verifiedGitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)

	committedAt := base.Add(10 * time.Minute)
	require.NoError(database.UpsertBranchCommits(ctx, []db.BranchCommit{{
		RepoID:         repoID,
		BranchName:     "main",
		CommitSHA:      "abc123def456abc123def456abc123def456abcd",
		AuthorName:     "Commit Author",
		AuthorEmail:    "author@example.com",
		AuthoredAt:     committedAt.Add(-time.Minute),
		CommitterName:  "Committer Person",
		CommitterEmail: "committer@example.com",
		CommittedAt:    committedAt,
		Subject:        "ship default branch work",
	}}))
	detectedAt := base.Add(20 * time.Minute)
	require.NoError(database.InsertBranchForcePush(ctx, db.BranchForcePush{
		RepoID:     repoID,
		BranchName: "main",
		BeforeSHA:  "before1234567890",
		AfterSHA:   "after1234567890",
		DetectedAt: detectedAt,
	}))

	since := url.QueryEscape(base.Format(time.RFC3339))
	rr := testutil.DoJSON(t, srv, http.MethodGet, "/api/v1/activity?since="+since, nil)
	require.Equal(http.StatusOK, rr.Code)
	var body struct {
		Items []map[string]any `json:"items"`
	}
	require.NoError(json.NewDecoder(rr.Body).Decode(&body))
	require.Len(body.Items, 2)

	forcePush := body.Items[0]
	assert.Equal("default_branch_force_push", forcePush["activity_type"])
	assert.Equal("main", forcePush["branch_name"])
	assert.Equal("before1234567890", forcePush["before_sha"])
	assert.Equal("after1234567890", forcePush["after_sha"])
	assert.Empty(forcePush["item_type"])
	assert.Zero(forcePush["item_number"])
	assert.Equal(itemapi.FormatUTCRFC3339(detectedAt), forcePush["created_at"])

	commit := body.Items[1]
	assert.Equal("default_branch_commit", commit["activity_type"])
	assert.Equal("main", commit["branch_name"])
	assert.Equal("abc123def456abc123def456abc123def456abcd", commit["commit_sha"])
	assert.Equal("Commit Author", commit["author_name"])
	assert.Equal("author@example.com", commit["author_email"])
	assert.Equal("Committer Person", commit["committer_name"])
	assert.Equal("committer@example.com", commit["committer_email"])
	assert.Equal(itemapi.FormatUTCRFC3339(committedAt), commit["committed_at"])
	assert.Equal("https://github.com/acme/widget/commit/abc123def456abc123def456abc123def456abcd", commit["activity_url"])
	assert.Empty(commit["item_type"])
	assert.Zero(commit["item_number"])
}

func TestAPIListActivityFiltersConfiguredReposByHost(t *testing.T) {
	runParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	srv, database, _, _ := setupTestServerWithConfig(t)

	seedPROnHost(t, database, "github.com", "acme", "widget", 1)
	seedPROnHost(t, database, "ghe.example.com", "acme", "widget", 2)

	since := time.Now().UTC().AddDate(0, 0, -7).Format(time.RFC3339)
	rr := testutil.DoJSON(t, srv, http.MethodGet, "/api/v1/activity?since="+since, nil)
	require.Equal(http.StatusOK, rr.Code)
	var body itemapi.ActivityResponse
	require.NoError(json.NewDecoder(rr.Body).Decode(&body))
	require.NotEmpty(body.Items)
	for _, item := range body.Items {
		assert.Equal("github.com", item.PlatformHost)
		assert.Equal("acme", item.RepoOwner)
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
