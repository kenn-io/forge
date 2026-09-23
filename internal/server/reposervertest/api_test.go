package reposervertest

import (
	"bytes"
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
	"sync/atomic"
	"testing"
	"time"

	gh "github.com/google/go-github/v91/github"
	"github.com/shurcooL/githubv4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/apiclient"
	"go.kenn.io/forge/internal/apiclient/generated"
	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/db"
	ghclient "go.kenn.io/forge/internal/github"
	"go.kenn.io/forge/internal/platformdb"
	"go.kenn.io/forge/internal/server"
	"go.kenn.io/forge/internal/server/activityapi"
	"go.kenn.io/forge/internal/server/issueapi"
	"go.kenn.io/forge/internal/server/itemapi"
	"go.kenn.io/forge/internal/server/pullapi"
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

func TestAPIRepoFilterAcceptsMultipleRepos(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	srv, database, _ := setupTestServer(t)

	seedPROnHost(t, database, "github.com", "acme", "widget", 1)
	seedPROnHost(t, database, "github.com", "acme", "worker", 2)
	seedPROnHost(t, database, "github.com", "acme", "ignored", 3)
	seedIssueOnHost(t, database, "github.com", "acme", "widget", 11, "open", "widget issue")
	seedIssueOnHost(t, database, "github.com", "acme", "worker", 12, "open", "worker issue")
	seedIssueOnHost(t, database, "github.com", "acme", "ignored", 13, "open", "ignored issue")

	filter := url.QueryEscape("github|github.com/acme/widget,github|github.com/acme/worker")

	rawPulls := testutil.DoJSON(t, srv, http.MethodGet, "/api/v1/pulls?repo="+filter, nil)
	require.Equal(http.StatusOK, rawPulls.Code)
	var pulls []pullapi.MergeRequestResponse
	require.NoError(json.Unmarshal(rawPulls.Body.Bytes(), &pulls))
	require.Len(pulls, 2)
	assert.ElementsMatch([]string{"widget", "worker"}, []string{
		pulls[0].RepoName,
		pulls[1].RepoName,
	})

	rawIssues := testutil.DoJSON(t, srv, http.MethodGet, "/api/v1/issues?repo="+filter, nil)
	require.Equal(http.StatusOK, rawIssues.Code)
	var issues []issueapi.IssueResponse
	require.NoError(json.Unmarshal(rawIssues.Body.Bytes(), &issues))
	require.Len(issues, 2)
	assert.ElementsMatch([]string{"widget", "worker"}, []string{
		issues[0].RepoName,
		issues[1].RepoName,
	})

	since := url.QueryEscape(time.Now().UTC().Add(-time.Hour).Format(time.RFC3339))
	rawActivity := testutil.DoJSON(t, srv, http.MethodGet, "/api/v1/activity?since="+since+"&repo="+filter, nil)
	require.Equal(http.StatusOK, rawActivity.Code)
	var activity itemapi.ActivityResponse
	require.NoError(json.Unmarshal(rawActivity.Body.Bytes(), &activity))
	require.NotEmpty(activity.Items)
	for _, item := range activity.Items {
		assert.Contains([]string{"widget", "worker"}, item.RepoName)
	}
}

func TestAPIConfiguredRepoFiltersUseProviderIdentity(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	ctx := t.Context()
	srv, database, _, syncer := setupTestServerWithConfig(t)
	client := setupTestClientWithBaseURL(t, srv, "http://127.0.0.1:8091")

	_, err := database.UpsertRepo(ctx, db.RepoIdentity{
		Platform:       "github",
		PlatformHost:   "github.com",
		PlatformRepoID: "github-widget",
		Owner:          "acme",
		Name:           "widget",
		RepoPath:       "acme/widget",
	})
	require.NoError(err)
	_, err = database.UpsertRepo(ctx, db.RepoIdentity{
		Platform:       "gitea",
		PlatformHost:   "github.com",
		PlatformRepoID: "gitea-widget",
		Owner:          "acme",
		Name:           "widget",
		RepoPath:       "acme/widget",
	})
	require.NoError(err)
	syncer.SetRepos([]ghclient.RepoRef{{
		Platform:     platform.KindGitHub,
		PlatformHost: "github.com",
		Owner:        "acme",
		Name:         "widget",
		RepoPath:     "acme/widget",
	}})

	reposResp, err := client.HTTP.ListReposWithResponse(ctx)
	require.NoError(err)
	require.Equal(http.StatusOK, reposResp.StatusCode)
	require.NotNil(reposResp.JSON200)
	require.Len(*reposResp.JSON200, 1)
	assert.Equal("github", (*reposResp.JSON200)[0].Platform)
	assert.Equal("github.com", (*reposResp.JSON200)[0].PlatformHost)
	assert.Equal("acme", (*reposResp.JSON200)[0].Owner)
	assert.Equal("widget", (*reposResp.JSON200)[0].Name)

	summariesResp, err := client.HTTP.ListRepoSummariesWithResponse(ctx)
	require.NoError(err)
	require.Equal(http.StatusOK, summariesResp.StatusCode)
	require.NotNil(summariesResp.JSON200)
	require.Len(*summariesResp.JSON200, 1)
	assert.Equal("github", (*summariesResp.JSON200)[0].Repo.Provider)
	assert.Equal("github.com", (*summariesResp.JSON200)[0].PlatformHost)
	assert.Equal("acme", (*summariesResp.JSON200)[0].Owner)
	assert.Equal("widget", (*summariesResp.JSON200)[0].Name)
}

func TestAPIListRepoSummaries(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)

	repos := []ghclient.RepoRef{
		{Platform: "github", Owner: "acme", Name: "widgets", PlatformHost: "github.com"},
		{Platform: "github", Owner: "acme", Name: "tools", PlatformHost: "github.com"},
		{Platform: "github", Owner: "acme", Name: "archived", PlatformHost: "github.com"},
	}
	srv, database, _ := setupTestServerWithRepos(t, &mockGH{}, repos)
	client := setupTestClient(t, srv)

	_, err := testutil.SeedFixtures(context.Background(), database)
	require.NoError(err)
	widgetsRepo, err := database.GetRepoByIdentity(context.Background(), verifiedGitHubRepoIdentity("github.com", "acme", "widgets"))
	require.NoError(err)
	require.NotNil(widgetsRepo)
	publishedAt := time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC)
	previousPublishedAt := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	commitsSince := 42
	err = database.UpsertRepoOverview(context.Background(), widgetsRepo.ID, db.RepoOverview{
		LatestRelease: &db.RepoRelease{
			TagName:         "v2.8.1",
			Name:            "Version 2.8.1",
			URL:             "https://github.com/acme/widgets/releases/tag/v2.8.1",
			TargetCommitish: "main",
			Prerelease:      false,
			PublishedAt:     &publishedAt,
		},
		Releases: []db.RepoRelease{
			{
				TagName:         "v2.8.1",
				Name:            "Version 2.8.1",
				URL:             "https://github.com/acme/widgets/releases/tag/v2.8.1",
				TargetCommitish: "main",
				Prerelease:      false,
				PublishedAt:     &publishedAt,
			},
			{
				TagName:         "v2.7.0",
				Name:            "Version 2.7.0",
				URL:             "https://github.com/acme/widgets/releases/tag/v2.7.0",
				TargetCommitish: "main",
				Prerelease:      true,
				PublishedAt:     &previousPublishedAt,
			},
		},
		CommitsSinceRelease: &commitsSince,
		CommitTimeline: []db.RepoCommitTimelinePoint{{
			SHA:         "abc123",
			Message:     "Ship repo overview",
			CommittedAt: time.Date(2026, 4, 2, 12, 0, 0, 0, time.UTC),
		}},
	})
	require.NoError(err)

	resp, err := client.HTTP.ListRepoSummariesWithResponse(context.Background())
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)
	require.NotNil(resp.JSON200)
	require.Len(*resp.JSON200, 3)

	var widgets *generated.RepoSummaryResponse
	for i := range *resp.JSON200 {
		summary := &(*resp.JSON200)[i]
		if summary.Name == "widgets" {
			widgets = summary
			break
		}
	}
	require.NotNil(widgets)
	require.NotNil(widgets.ActiveAuthors)
	require.NotNil(widgets.RecentIssues)

	assert.Equal("acme", widgets.Owner)
	assert.Equal(int64(4), widgets.OpenPrCount)
	assert.Equal(int64(1), widgets.DraftPrCount)
	assert.Equal(int64(3), widgets.OpenIssueCount)
	assert.Equal(int64(7), widgets.CachedPrCount)
	assert.Equal(int64(4), widgets.CachedIssueCount)
	assert.NotNil(widgets.MostRecentActivityAt)
	require.NotNil(widgets.LatestRelease)
	assert.Equal("v2.8.1", widgets.LatestRelease.TagName)
	require.NotNil(widgets.Releases)
	assert.Len(widgets.Releases, 2)
	assert.Equal("v2.7.0", widgets.Releases[1].TagName)
	assert.True(widgets.Releases[1].Prerelease)
	assert.Equal(
		itemapi.FormatUTCRFC3339(previousPublishedAt),
		*widgets.Releases[1].PublishedAt,
	)
	assert.Equal(int64(42), *widgets.CommitsSinceRelease)
	require.NotNil(widgets.CommitTimeline)
	assert.Len(widgets.CommitTimeline, 1)
	assert.Equal("Ship repo overview", widgets.CommitTimeline[0].Message)
	assert.Len(widgets.ActiveAuthors, 3)
	assert.Equal("alice", widgets.ActiveAuthors[0].Login)
	assert.Equal(int64(3), widgets.ActiveAuthors[0].ItemCount)
	assert.NotEmpty(widgets.RecentIssues[0].Title)
}

func TestAPIListRepoSummariesIncludesDefaultPlatformHost(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)

	srv, database, _, syncer := setupTestServerWithConfigContent(t, `
default_platform_host = "ghe.example.com"

[[repos]]
owner = "acme"
name = "widgets"
platform_host = "ghe.example.com"
`, &mockGH{})

	_, err := database.UpsertRepo(
		t.Context(), verifiedGitHubRepoIdentity("ghe.example.com", "acme", "widgets"),
	)
	require.NoError(err)
	syncer.SetRepos([]ghclient.RepoRef{{
		Owner:        "acme",
		Name:         "widgets",
		PlatformHost: "ghe.example.com",
	}})

	rr := testutil.DoJSON(t, srv, http.MethodGet, "/api/v1/repos/summary", nil)
	require.Equal(http.StatusOK, rr.Code)

	var summaries []itemapi.RepoSummaryResponse
	require.NoError(json.NewDecoder(rr.Body).Decode(&summaries))
	require.Len(summaries, 1)
	assert.Equal("ghe.example.com", summaries[0].PlatformHost)
	assert.Equal("ghe.example.com", summaries[0].DefaultPlatformHost)
}

func TestAPISyncStatus(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)

	srv, _, syncer := setupTestServer(t)
	client := setupTestClient(t, srv)
	syncer.RunOnce(t.Context())

	resp, err := client.HTTP.GetSyncStatusWithResponse(t.Context())
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)
	require.NotNil(resp.JSON200)
	require.False(resp.JSON200.Running)
	require.NotNil(resp.JSON200.LastRunAt)
	assert.Equal(t, time.UTC, resp.JSON200.LastRunAt.Location())
}

func TestMatchPriorityRepoRequiresProviderQualifiedRepoPaths(t *testing.T) {
	runParallelServerTest(t)
	assert := assert.New(t)

	tracked := []ghclient.RepoRef{
		{
			Platform:     platform.KindGitLab,
			PlatformHost: "gitlab.com",
			Owner:        "group/subgroup",
			Name:         "project",
			RepoPath:     "group/subgroup/project",
		},
		{
			Platform:     platform.KindGitHub,
			PlatformHost: "github.com",
			Owner:        "acme",
			Name:         "widget",
			RepoPath:     "acme/widget",
		},
		{
			Platform:     platform.KindGitea,
			PlatformHost: "github.com",
			Owner:        "acme",
			Name:         "widget",
			RepoPath:     "acme/widget",
		},
	}

	_, ok := activityapi.MatchPriorityRepo("group/subgroup/project", tracked)
	assert.False(ok)

	_, ok = activityapi.MatchPriorityRepo("github.com/acme/widget", tracked)
	assert.False(ok)

	repo, ok := activityapi.MatchPriorityRepo("gitea|github.com/acme/widget", tracked)
	assert.True(ok)
	assert.Equal(platform.KindGitea, repo.Platform)
	assert.Equal("github.com", repo.PlatformHost)
	assert.Equal("acme/widget", repo.RepoPath)

	repo, ok = activityapi.MatchPriorityRepo("github|github.com/acme/widget", tracked)
	assert.True(ok)
	assert.Equal(platform.KindGitHub, repo.Platform)
	assert.Equal("github.com", repo.PlatformHost)
	assert.Equal("acme/widget", repo.RepoPath)

	repo, ok = activityapi.MatchPriorityRepo("gitlab|gitlab.com/group/subgroup/project", tracked)
	assert.True(ok)
	assert.Equal(platform.KindGitLab, repo.Platform)
	assert.Equal("gitlab.com", repo.PlatformHost)
	assert.Equal("group/subgroup/project", repo.RepoPath)
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

func seedIssueOnHost(
	t *testing.T, database *db.DB,
	host, owner, name string, number int,
	state, title string,
) int64 {
	t.Helper()
	ctx := context.Background()

	repoID, err := database.UpsertRepo(ctx, verifiedGitHubRepoIdentity(host, owner, name))
	require.NoError(t, err)

	return seedIssueForRepo(t, database, repoID, host, owner, name, number, state, title)
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

// TestE2EGraphQLBulkSyncPersistsWorkflowApproval drives the periodic
// sync through the GraphQL bulk path and verifies the persisted
// workflow approval snapshot reaches the HTTP API. Regression test
// for the gap where fully-synced bulk PRs would mark detail_fetched_at
// without ever populating workflow_approval_checked_at, leaving the
// DB-only GET unable to surface the Approve workflows button.
func TestE2EGraphQLBulkSyncPersistsWorkflowApproval(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	ctx := t.Context()

	now := time.Date(2026, 4, 14, 10, 0, 0, 0, time.UTC)
	nowRFC3339 := now.Format(time.RFC3339)
	const prNumber = 173
	const prID int64 = 173000
	const headSHA = "abc123"

	gqlSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if bytes.Contains(body, []byte("pullRequests")) {
			resp := `{"data":{"repository":{"pullRequests":{"nodes":[{
				"databaseId":173000,
				"number":173,
				"title":"PR awaiting workflow approval",
				"state":"OPEN",
				"isDraft":false,
				"body":"",
				"url":"https://github.com/acme/widget/pull/173",
				"author":{"login":"ericdill"},
				"createdAt":"` + nowRFC3339 + `",
				"updatedAt":"` + nowRFC3339 + `",
				"mergedAt":null,
				"closedAt":null,
				"additions":1,
				"deletions":0,
				"mergeable":"MERGEABLE",
				"reviewDecision":"",
				"headRefName":"feature/fork-pr",
				"baseRefName":"main",
				"headRefOid":"` + headSHA + `",
				"baseRefOid":"def456",
				"headRepository":{"url":"https://github.com/ericdill/widget"},
				"labels":{"nodes":[]},
				"comments":{"nodes":[],"pageInfo":{"hasNextPage":false,"endCursor":""}},
				"reviews":{"nodes":[],"pageInfo":{"hasNextPage":false,"endCursor":""}},
				"allCommits":{"nodes":[],"pageInfo":{"hasNextPage":false,"endCursor":""}},
				"lastCommit":{"nodes":[]},
				"timelineItems":{"nodes":[],"pageInfo":{"hasNextPage":false,"endCursor":""}}
			}],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}`
			_, _ = w.Write([]byte(resp))
			return
		}
		_, _ = w.Write([]byte(`{"data":{"repository":{"issues":{"nodes":[],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}`))
	}))
	defer gqlSrv.Close()

	prTime := gh.Timestamp{Time: now}
	mock := &mockGH{
		listOpenPullRequestsFn: func(_ context.Context, _, _ string) ([]*gh.PullRequest, error) {
			return []*gh.PullRequest{{
				ID:        new(prID),
				Number:    new(prNumber),
				Title:     new("PR awaiting workflow approval"),
				State:     new("open"),
				HTMLURL:   new("https://github.com/acme/widget/pull/173"),
				User:      &gh.User{Login: new("ericdill")},
				CreatedAt: &prTime,
				UpdatedAt: &prTime,
				Head:      &gh.PullRequestBranch{Ref: new("feature/fork-pr"), SHA: new(headSHA)},
				Base:      &gh.PullRequestBranch{Ref: new("main")},
			}}, nil
		},
		listOpenIssuesFn: func(_ context.Context, _, _ string) ([]*gh.Issue, error) {
			return nil, &gh.ErrorResponse{
				Response: &http.Response{StatusCode: http.StatusNotModified},
			}
		},
		listWorkflowRunsForHeadFn: func(_ context.Context, _, _, sha string) ([]*gh.WorkflowRun, error) {
			require.Equal(headSHA, sha)
			return []*gh.WorkflowRun{{
				ID:           new(int64(7777)),
				HeadSHA:      new(headSHA),
				Event:        new("pull_request"),
				PullRequests: []*gh.PullRequest{{Number: new(prNumber)}},
			}}, nil
		},
	}

	srv, _, syncer := setupTestServerWithMock(t, mock)
	gqlClient := githubv4.NewEnterpriseClient(gqlSrv.URL, gqlSrv.Client())
	syncer.SetFetchers(map[string]*ghclient.GraphQLFetcher{
		"github.com": ghclient.NewGraphQLFetcherWithClient(gqlClient, nil),
	})
	client := setupTestClient(t, srv)

	syncer.RunOnce(ctx)

	resp, err := client.HTTP.GetPullWithResponse(ctx, &generated.GetPullRequestOptions{PathParams: &generated.GetPullPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(int64(prNumber))}})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)
	require.NotNil(resp.JSON200)
	require.NotNil(resp.JSON200.WorkflowApproval)
	assert.True(resp.JSON200.WorkflowApproval.Checked,
		"GraphQL bulk path should persist a workflow approval snapshot")
	assert.True(resp.JSON200.WorkflowApproval.Required,
		"head SHA has a pending workflow run; button must be live")
	assert.Equal(int64(1), resp.JSON200.WorkflowApproval.Count)
}

func make422Error() error {
	return &gh.ErrorResponse{
		Response: &http.Response{StatusCode: http.StatusUnprocessableEntity},
		Message:  "Validation Failed",
	}
}

func TestAPIStateMutationDoesNotRecoverFromProviderWhenSyncDisabled(t *testing.T) {
	runParallelServerTest(t)
	for _, itemType := range []string{"pull request", "issue"} {
		t.Run(itemType, func(t *testing.T) {
			require := require.New(t)
			var recoveryReads atomic.Int32
			mock := &mockGH{
				editPullRequestFn: func(context.Context, string, string, int, platformgithub.EditPullRequestOpts) (*gh.PullRequest, error) {
					return nil, make422Error()
				},
				getPullRequestFn: func(context.Context, string, string, int) (*gh.PullRequest, error) {
					recoveryReads.Add(1)
					return nil, nil
				},
				editIssueFn: func(context.Context, string, string, int, string) (*gh.Issue, error) {
					return nil, make422Error()
				},
				getIssueFn: func(context.Context, string, string, int) (*gh.Issue, error) {
					recoveryReads.Add(1)
					return nil, nil
				},
			}
			srv, database, syncer := setupTestServerWithMock(t, mock)
			syncer.DisableSync()
			client := setupTestClient(t, srv)
			var status int
			if itemType == "pull request" {
				seedPR(t, database, "acme", "widget", 1)
				resp, err := client.HTTP.SetPrGithubStateWithResponse(t.Context(), &generated.SetPrGithubStateRequestOptions{PathParams: &generated.SetPrGithubStatePath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}, Body: &generated.SetPrGithubStateBody{State: "closed"}})
				require.Error(err)
				status = resp.StatusCode
			} else {
				seedIssue(t, database, "acme", "widget", 5, "open")
				resp, err := client.HTTP.SetIssueGithubStateWithResponse(t.Context(), &generated.SetIssueGithubStateRequestOptions{PathParams: &generated.SetIssueGithubStatePath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(5)}, Body: &generated.SetIssueGithubStateBody{State: "closed"}})
				require.Error(err)
				status = resp.StatusCode
			}
			require.Equal(http.StatusServiceUnavailable, status)
			require.Zero(recoveryReads.Load())
		})
	}
}

func TestAPIRateLimits(t *testing.T) {
	runParallelServerTest(t)
	assert := assert.New(t)

	database := dbtest.Open(t)

	rt := ghclient.NewRateTracker(database, "github.com", "host", "rest")

	syncer := ghclient.NewSyncer(
		map[string]ghclient.Client{"github.com": &mockGH{}},
		database, nil,
		[]ghclient.RepoRef{{
			Owner: "acme", Name: "widget",
			PlatformHost: "github.com",
		}},
		time.Minute,
		map[string]*ghclient.RateTracker{"github.com": rt},
		nil,
	)
	t.Cleanup(syncer.Stop)

	srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{})
	ts := httptest.NewServer(srv)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/v1/rate-limits")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(200, resp.StatusCode)

	var body itemapi.RateLimitsResponse
	err = json.NewDecoder(resp.Body).Decode(&body)
	require.NoError(t, err)

	gh, ok := body.ProviderPools["github.com"]
	assert.True(ok)
	assert.Equal("host", gh.RatePrincipal)
	assert.Equal("Host credential", gh.PrincipalLabel)
	assert.Equal(0, gh.REST.Requests)
	assert.Equal(-1, gh.REST.Remaining)
	assert.False(gh.REST.Known)
	assert.Equal(200, gh.ReserveBuffer)
	assert.Empty(body.LocalCeilings)
}

func TestAPIRateLimitsSeparatesCredentialPoolsFromLocalCeilings(t *testing.T) {
	runParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	database := dbtest.Open(t)
	registry := ghclient.NewQuotaRegistry()
	reset := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	appIdentity := ghclient.IdentityKey{Host: "github.com", Principal: "installation:42"}
	userIdentity := ghclient.IdentityKey{Host: "github.com", Principal: "user:7"}
	registry.UpdateSnapshot(
		appIdentity, ghclient.QuotaResourceREST,
		ghclient.Rate{Limit: 15000, Remaining: 14900, Reset: reset},
	)
	registry.UpdateSnapshot(
		appIdentity, ghclient.QuotaResourceGraphQL,
		ghclient.Rate{Limit: 10000, Remaining: 9900, Reset: reset},
	)
	registry.UpdateSnapshot(
		userIdentity, ghclient.QuotaResourceREST,
		ghclient.Rate{Limit: 5000, Remaining: 4900, Reset: reset},
	)
	budget := ghclient.NewSyncBudgetWithEssentialReserve(3001)
	_, ok := budget.TrySpend(2701)
	require.True(ok)
	_, ok = budget.TrySpend(1)
	require.False(ok, "optional work must stop at the background boundary")
	expectedCeilingResetAt := budget.ResetAt().UTC().Format(time.RFC3339)
	syncer := ghclient.NewSyncer(
		map[string]ghclient.Client{"github.com": &mockGH{}},
		database, nil,
		[]ghclient.RepoRef{{Owner: "acme", Name: "widget", PlatformHost: "github.com"}},
		time.Minute, nil,
		map[string]*ghclient.SyncBudget{"github.com": budget},
	)
	syncer.SetQuotaRegistry(registry)
	t.Cleanup(syncer.Stop)
	srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{})
	ts := httptest.NewServer(srv)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/v1/rate-limits")
	require.NoError(err)
	defer resp.Body.Close()
	require.Equal(http.StatusOK, resp.StatusCode)
	var body itemapi.RateLimitsResponse
	require.NoError(json.NewDecoder(resp.Body).Decode(&body))

	app, ok := body.ProviderPools["github:github.com:installation:42"]
	require.True(ok)
	user, ok := body.ProviderPools["github:github.com:user:7"]
	require.True(ok)
	assert.Equal(14900, app.REST.Remaining)
	assert.Equal(9900, app.GraphQL.Remaining)
	assert.Equal(4900, user.REST.Remaining)
	assert.False(user.GraphQL.Known)
	ceiling, ok := body.LocalCeilings["github.com"]
	require.True(ok)
	assert.Equal(3001, ceiling.Limit)
	assert.Equal(2701, ceiling.BackgroundLimit)
	assert.Equal(2701, ceiling.Spent)
	assert.Equal(300, ceiling.Remaining)
	assert.Equal(expectedCeilingResetAt, ceiling.ResetAt)
}

func TestAPIRateLimitsMarksExpiredProviderQuotaUnknown(t *testing.T) {
	runParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	database := dbtest.Open(t)
	identity := ghclient.IdentityKey{Host: "github.com", Principal: "user:7"}
	registry := ghclient.NewQuotaRegistry()
	registry.UpdateSnapshot(identity, ghclient.QuotaResourceREST, ghclient.Rate{
		Limit: 5000, Remaining: 4800, Reset: time.Now().UTC().Add(-time.Second),
	})
	availability := registry.CheckReserve(
		identity, []ghclient.QuotaResource{ghclient.QuotaResourceREST}, 1,
		ghclient.RateReserveBuffer,
	)
	require.False(availability.Known)

	syncer := ghclient.NewSyncer(
		map[string]ghclient.Client{"github.com": &mockGH{}}, database, nil, nil,
		time.Minute, nil, nil,
	)
	syncer.SetQuotaRegistry(registry)
	t.Cleanup(syncer.Stop)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/rate-limits", nil)
	server.New(database, syncer, nil, "/", nil, server.ServerOptions{}).ServeHTTP(recorder, request)
	require.Equal(http.StatusOK, recorder.Code)

	var response itemapi.RateLimitsResponse
	require.NoError(json.NewDecoder(recorder.Body).Decode(&response))
	status, ok := response.ProviderPools["github:github.com:user:7"]
	require.True(ok)
	assert.False(status.REST.Known)
	assert.Equal(4800, status.REST.Remaining)
}

func TestAPIRateLimitsRollsExpiredTrackerBeforeResponse(t *testing.T) {
	runParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	database := dbtest.Open(t)
	tracker := ghclient.NewRateTracker(database, "github.com", "host", "rest")
	tracker.UpdateFromRate(ghclient.Rate{
		Limit: 5000, Remaining: 3000, Reset: time.Now().UTC().Add(time.Hour),
	})
	tracker.SetResetAtForTesting(time.Now().UTC().Add(-time.Second))
	syncer := ghclient.NewSyncer(
		map[string]ghclient.Client{"github.com": &mockGH{}}, database, nil, nil,
		time.Minute, map[string]*ghclient.RateTracker{"github.com": tracker}, nil,
	)
	t.Cleanup(syncer.Stop)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/rate-limits", nil)
	server.New(database, syncer, nil, "/", nil, server.ServerOptions{}).ServeHTTP(recorder, request)
	require.Equal(http.StatusOK, recorder.Code)

	var response itemapi.RateLimitsResponse
	require.NoError(json.NewDecoder(recorder.Body).Decode(&response))
	status, ok := response.ProviderPools["github.com"]
	require.True(ok)
	assert.False(status.REST.Known)
	assert.Equal(-1, status.REST.Remaining)
	assert.Empty(status.REST.ResetAt)
}

func TestAPIRateLimitsWithBudget(t *testing.T) {
	runParallelServerTest(t)
	assert := assert.New(t)

	database := dbtest.Open(t)

	rt := ghclient.NewRateTracker(database, "github.com", "host", "rest")

	syncer := ghclient.NewSyncer(
		map[string]ghclient.Client{"github.com": &mockGH{}},
		database, nil,
		[]ghclient.RepoRef{{
			Owner: "acme", Name: "widget",
			PlatformHost: "github.com",
		}},
		time.Minute,
		map[string]*ghclient.RateTracker{"github.com": rt},
		map[string]*ghclient.SyncBudget{"github.com": ghclient.NewSyncBudget(500)},
	)
	t.Cleanup(syncer.Stop)

	// Simulate some budget spend.
	budgets := syncer.Budgets()
	budgets["github.com"].Spend(42)

	srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{})
	ts := httptest.NewServer(srv)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/v1/rate-limits")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(200, resp.StatusCode)

	var body itemapi.RateLimitsResponse
	err = json.NewDecoder(resp.Body).Decode(&body)
	require.NoError(t, err)

	gh, ok := body.LocalCeilings["github.com"]
	assert.True(ok)
	assert.Equal(500, gh.Limit)
	assert.Equal(42, gh.Spent)
	assert.Equal(458, gh.Remaining)
}

func TestAPIRateLimitsResetExpiredBudgetWindow(t *testing.T) {
	runParallelServerTest(t)
	assert := assert.New(t)

	database := dbtest.Open(t)

	rt := ghclient.NewRateTracker(database, "github.com", "host", "rest")
	budget := ghclient.NewSyncBudget(500)

	syncer := ghclient.NewSyncer(
		map[string]ghclient.Client{"github.com": &mockGH{}},
		database, nil,
		[]ghclient.RepoRef{{
			Owner: "acme", Name: "widget",
			PlatformHost: "github.com",
		}},
		time.Minute,
		map[string]*ghclient.RateTracker{"github.com": rt},
		map[string]*ghclient.SyncBudget{"github.com": budget},
	)
	t.Cleanup(syncer.Stop)

	budget.Spend(42)
	rt.UpdateFromRate(ghclient.Rate{
		Limit:     5000,
		Remaining: 3000,
		Reset:     time.Now().Add(time.Hour),
	})
	rt.SetResetAtForTesting(time.Now().Add(-time.Second))

	srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{})
	ts := httptest.NewServer(srv)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/v1/rate-limits")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(200, resp.StatusCode)

	var body itemapi.RateLimitsResponse
	err = json.NewDecoder(resp.Body).Decode(&body)
	require.NoError(t, err)

	gh, ok := body.LocalCeilings["github.com"]
	assert.True(ok)
	assert.Equal(500, gh.Limit)
	assert.Equal(0, gh.Spent)
	assert.Equal(500, gh.Remaining)
}

func TestAPIRateLimitsIncludesWriteOnlyGraphQLState(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	database := dbtest.Open(t)
	bucket := ghclient.RateBucketKey("github", "github.com", "user:123")
	restRT := ghclient.NewRateTracker(database, "github.com", "user:123", "rest")
	gqlRT := ghclient.NewRateTracker(database, "github.com", "user:123", "graphql")
	gqlRT.UpdateFromRate(ghclient.Rate{Limit: 5000, Remaining: 4300, Reset: time.Now().Add(time.Hour)})
	syncer := ghclient.NewSyncer(
		map[string]ghclient.Client{"github.com": &mockGH{}}, database, nil,
		nil, time.Minute,
		map[string]*ghclient.RateTracker{bucket: restRT}, nil,
	)
	syncer.SetWriteGQLRateTrackers(map[string]*ghclient.RateTracker{bucket: gqlRT})
	srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{})
	ts := httptest.NewServer(srv)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/v1/rate-limits")
	require.NoError(err)
	defer resp.Body.Close()
	var body itemapi.RateLimitsResponse
	require.NoError(json.NewDecoder(resp.Body).Decode(&body))
	status := body.ProviderPools["github:github.com:user:123"]
	assert.True(status.GraphQL.Known)
	assert.Equal(4300, status.GraphQL.Remaining)
}

func TestAPIRateLimitsWithGQL(t *testing.T) {
	runParallelServerTest(t)
	assert := assert.New(t)

	database := dbtest.Open(t)

	restRT := ghclient.NewRateTracker(database, "github.com", "host", "rest")
	gqlRT := ghclient.NewRateTracker(database, "github.com", "host", "graphql")

	syncer := ghclient.NewSyncer(
		map[string]ghclient.Client{"github.com": &mockGH{}},
		database, nil,
		[]ghclient.RepoRef{{
			Owner: "acme", Name: "widget",
			PlatformHost: "github.com",
		}},
		time.Minute,
		map[string]*ghclient.RateTracker{"github.com": restRT},
		nil,
	)

	fetcher := ghclient.NewGraphQLFetcher(testTokenSource("token"), "github.com", gqlRT, nil)
	syncer.SetFetchers(map[string]*ghclient.GraphQLFetcher{
		"github.com": fetcher,
	})

	// Simulate GraphQL rate data.
	gqlRT.UpdateFromRate(ghclient.Rate{
		Limit:     5000,
		Remaining: 4800,
		Reset:     time.Now().Add(30 * time.Minute),
	})

	srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{})
	ts := httptest.NewServer(srv)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/v1/rate-limits")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(200, resp.StatusCode)

	var body itemapi.RateLimitsResponse
	err = json.NewDecoder(resp.Body).Decode(&body)
	require.NoError(t, err)

	host, ok := body.ProviderPools["github.com"]
	assert.True(ok)

	// GQL fields should be populated.
	assert.Equal("host", host.RatePrincipal)
	assert.Equal("Host credential", host.PrincipalLabel)
	assert.Equal(4800, host.GraphQL.Remaining)
	assert.Equal(5000, host.GraphQL.Limit)
	assert.True(host.GraphQL.Known)
	assert.NotEmpty(host.GraphQL.ResetAt)
}

func TestAPIRateLimitsReadsLocalStateWithoutRefreshingGitHubRateLimit(t *testing.T) {
	runParallelServerTest(t)
	assert := assert.New(t)

	database := dbtest.Open(t)
	now := time.Now().UTC().Truncate(time.Second)
	localRestReset := now.Add(30 * time.Minute)
	localGQLReset := now.Add(45 * time.Minute)
	gitHubRestReset := now.Add(time.Hour)
	gitHubGQLReset := now.Add(90 * time.Minute)

	restRT := ghclient.NewRateTracker(database, "github.com", "host", "rest")
	gqlRT := ghclient.NewRateTracker(database, "github.com", "host", "graphql")
	restRT.UpdateFromRate(ghclient.Rate{
		Limit:     5000,
		Remaining: 3000,
		Reset:     localRestReset,
	})
	gqlRT.UpdateFromRate(ghclient.Rate{
		Limit:     5000,
		Remaining: 4000,
		Reset:     localGQLReset,
	})
	mock := &mockGH{
		rateLimitSnapshotFn: func(context.Context) (*platformgithub.RateLimitSnapshot, error) {
			return &platformgithub.RateLimitSnapshot{
				Core: &ghclient.Rate{
					Limit:     5000,
					Remaining: 4991,
					Reset:     gitHubRestReset,
				},
				GraphQL: &ghclient.Rate{
					Limit:     5000,
					Remaining: 4988,
					Reset:     gitHubGQLReset,
				},
			}, nil
		},
	}
	syncer := ghclient.NewSyncer(
		map[string]ghclient.Client{"github.com": mock},
		database, nil,
		[]ghclient.RepoRef{{
			Owner: "acme", Name: "widget",
			PlatformHost: "github.com",
		}},
		time.Minute,
		map[string]*ghclient.RateTracker{"github.com": restRT},
		nil,
	)
	syncer.SetFetchers(map[string]*ghclient.GraphQLFetcher{
		"github.com": ghclient.NewGraphQLFetcher(testTokenSource("token"), "github.com", gqlRT, nil),
	})
	t.Cleanup(syncer.Stop)

	srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{})
	ts := httptest.NewServer(srv)
	defer ts.Close()

	fetch := func() itemapi.RateLimitsResponse {
		resp, err := http.Get(ts.URL + "/api/v1/rate-limits")
		require.NoError(t, err)
		defer resp.Body.Close()
		assert.Equal(200, resp.StatusCode)

		var body itemapi.RateLimitsResponse
		err = json.NewDecoder(resp.Body).Decode(&body)
		require.NoError(t, err)
		return body
	}

	body := fetch()
	user, ok := body.ProviderPools["github.com"]
	assert.True(ok)
	assert.Equal(0, mock.rateLimitSnapshotCalls)
	assert.Equal(0, user.REST.Requests)
	assert.Equal(3000, user.REST.Remaining)
	assert.Equal(5000, user.REST.Limit)
	assert.Equal(itemapi.FormatUTCRFC3339(localRestReset), user.REST.ResetAt)
	assert.True(user.REST.Known)
	assert.Equal(4000, user.GraphQL.Remaining)
	assert.Equal(5000, user.GraphQL.Limit)
	assert.Equal(itemapi.FormatUTCRFC3339(localGQLReset), user.GraphQL.ResetAt)
	assert.True(user.GraphQL.Known)

	_ = fetch()
	assert.Equal(0, mock.rateLimitSnapshotCalls)
}

func TestAPIRateLimitsGQLDefaultsUnknown(t *testing.T) {
	runParallelServerTest(t)
	assert := assert.New(t)

	database := dbtest.Open(t)

	rt := ghclient.NewRateTracker(database, "github.com", "host", "rest")
	syncer := ghclient.NewSyncer(
		map[string]ghclient.Client{"github.com": &mockGH{}},
		database, nil,
		[]ghclient.RepoRef{{
			Owner: "acme", Name: "widget",
			PlatformHost: "github.com",
		}},
		time.Minute,
		map[string]*ghclient.RateTracker{"github.com": rt},
		nil,
	)
	// No SetFetchers call — GQL data should be unknown.

	srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{})
	ts := httptest.NewServer(srv)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/v1/rate-limits")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(200, resp.StatusCode)

	var body itemapi.RateLimitsResponse
	err = json.NewDecoder(resp.Body).Decode(&body)
	require.NoError(t, err)

	user := body.ProviderPools["github.com"]
	assert.Equal(-1, user.GraphQL.Remaining)
	assert.Equal(-1, user.GraphQL.Limit)
	assert.False(user.GraphQL.Known)
	assert.Empty(user.GraphQL.ResetAt)
}

func TestAPIRateLimitsMultiHostMixed(t *testing.T) {
	runParallelServerTest(t)
	assert := assert.New(t)

	database := dbtest.Open(t)

	// Two hosts: github.com has GQL data, ghe.example.com does not.
	ghRT := ghclient.NewRateTracker(database, "github.com", "host", "rest")
	gheRT := ghclient.NewRateTracker(database, "ghe.example.com", "host", "rest")
	gqlRT := ghclient.NewRateTracker(database, "github.com", "host", "graphql")
	gqlRT.UpdateFromRate(ghclient.Rate{
		Limit:     5000,
		Remaining: 4500,
		Reset:     time.Now().Add(30 * time.Minute),
	})

	syncer := ghclient.NewSyncer(
		map[string]ghclient.Client{
			"github.com":      &mockGH{},
			"ghe.example.com": &mockGH{},
		},
		database, nil,
		[]ghclient.RepoRef{
			{Platform: "github", Owner: "acme", Name: "widget", PlatformHost: "github.com"},
			{Owner: "corp", Name: "internal", PlatformHost: "ghe.example.com"},
		},
		time.Minute,
		map[string]*ghclient.RateTracker{
			"github.com":      ghRT,
			"ghe.example.com": gheRT,
		},
		nil,
	)

	fetcher := ghclient.NewGraphQLFetcher(testTokenSource("token"), "github.com", gqlRT, nil)
	syncer.SetFetchers(map[string]*ghclient.GraphQLFetcher{
		"github.com": fetcher,
	})

	srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{})
	ts := httptest.NewServer(srv)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/v1/rate-limits")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(200, resp.StatusCode)

	var body itemapi.RateLimitsResponse
	err = json.NewDecoder(resp.Body).Decode(&body)
	require.NoError(t, err)

	// Both hosts present.
	assert.Len(body.ProviderPools, 2)

	// github.com has GQL data.
	ghHost := body.ProviderPools["github.com"]
	assert.True(ghHost.GraphQL.Known)
	assert.Equal(4500, ghHost.GraphQL.Remaining)
	assert.Equal(5000, ghHost.GraphQL.Limit)

	// ghe.example.com has no GQL fetcher — defaults to unknown.
	gheHost := body.ProviderPools["ghe.example.com"]
	assert.Equal(-1, gheHost.GraphQL.Remaining)
	assert.Equal(-1, gheHost.GraphQL.Limit)
	assert.False(gheHost.GraphQL.Known)
}

func TestAPIRateLimitsScopesSameHostByProvider(t *testing.T) {
	runParallelServerTest(t)
	assert := assert.New(t)

	database := dbtest.Open(t)

	host := "code.example.com"
	ghRT := ghclient.NewPlatformRateTracker(database, "github", host, "host", "rest")
	glRT := ghclient.NewPlatformRateTracker(database, "gitlab", host, "host", "rest")
	ghRT.RecordRequest()
	glRT.RecordRequest()
	glRT.RecordRequest()

	syncer := ghclient.NewSyncer(
		map[string]ghclient.Client{host: &mockGH{}},
		database, nil,
		[]ghclient.RepoRef{
			{Platform: platform.KindGitHub, Owner: "acme", Name: "widget", PlatformHost: host},
			{Platform: platform.KindGitLab, Owner: "acme", Name: "widget", PlatformHost: host},
		},
		time.Minute,
		map[string]*ghclient.RateTracker{
			ghclient.RateBucketKey("github", host, "host"): ghRT,
			ghclient.RateBucketKey("gitlab", host, "host"): glRT,
		},
		nil,
	)
	t.Cleanup(syncer.Stop)

	srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{})
	ts := httptest.NewServer(srv)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/v1/rate-limits")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(200, resp.StatusCode)

	var body itemapi.RateLimitsResponse
	err = json.NewDecoder(resp.Body).Decode(&body)
	require.NoError(t, err)

	ghStatus, ok := body.ProviderPools[host]
	assert.True(ok)
	assert.Equal("github", ghStatus.Provider)
	assert.Equal(host, ghStatus.PlatformHost)
	assert.Equal(1, ghStatus.REST.Requests)

	glStatus, ok := body.ProviderPools["gitlab:"+host]
	assert.True(ok)
	assert.Equal("gitlab", glStatus.Provider)
	assert.Equal(host, glStatus.PlatformHost)
	assert.Equal(2, glStatus.REST.Requests)
}

// TestDisplayNameCacheE2E verifies the display-name cache
// through the full stack: sync → SQLite → HTTP API. Two
// RunOnce passes populate and then cache-hit the display name;
// the test asserts /api/v1/pulls returns the expected
// AuthorDisplayName after each pass, and that GetUser is only
// called during the first sync.
func TestDisplayNameCacheE2E(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)

	now := time.Now().UTC().Truncate(time.Second)
	prID := int64(1000)
	prNumber := 1
	prTitle := "test pr"
	prState := "open"
	prURL := "https://github.com/acme/widget/pull/1"
	prBody := ""
	prAuthor := "alice"
	displayName := "Alice Smith"
	getUserCalls := 0

	mock := &mockGH{
		listOpenPullRequestsFn: func(
			_ context.Context, _, _ string,
		) ([]*gh.PullRequest, error) {
			return []*gh.PullRequest{{
				ID:        &prID,
				Number:    &prNumber,
				Title:     &prTitle,
				State:     &prState,
				HTMLURL:   &prURL,
				Body:      &prBody,
				User:      &gh.User{Login: &prAuthor},
				CreatedAt: &gh.Timestamp{Time: now},
				UpdatedAt: &gh.Timestamp{Time: now},
			}}, nil
		},
		getUserFn: func(_ context.Context, login string) (*gh.User, error) {
			getUserCalls++
			return &gh.User{Login: &login, Name: &displayName}, nil
		},
	}

	srv, _, syncer := setupTestServerWithMock(t, mock)

	// First sync: populates display name via GetUser.
	syncer.RunOnce(t.Context())
	require.Positive(getUserCalls, "first sync should call GetUser")
	firstCalls := getUserCalls

	// GET /api/v1/pulls — display name must appear.
	rr := testutil.DoJSON(t, srv, http.MethodGet, "/api/v1/pulls", nil)
	require.Equal(http.StatusOK, rr.Code)
	require.Contains(rr.Body.String(), `"AuthorDisplayName":"Alice Smith"`)

	// Second sync: cache hit, no new GetUser calls.
	syncer.RunOnce(t.Context())
	require.Equal(firstCalls, getUserCalls,
		"second sync must not re-fetch cached display names")

	// GET /api/v1/pulls — display name still present.
	rr2 := testutil.DoJSON(t, srv, http.MethodGet, "/api/v1/pulls", nil)
	require.Equal(http.StatusOK, rr2.Code)
	require.Contains(rr2.Body.String(), `"AuthorDisplayName":"Alice Smith"`)
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
