package issueservertest

import (
	"bytes"
	"context"
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

func TestAPIInvolvesMeFiltersPullsIssuesAndActivity(t *testing.T) {
	runParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	mock := &mockGH{
		authenticatedViewerLoginFn: func(context.Context) (string, error) {
			return "TestUser", nil
		},
	}
	srv, database, _ := setupTestServerWithMock(t, mock)
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Second)

	seedPR(t, database, "acme", "widget", 1,
		withSeedPRAuthor("testuser"), withSeedPRTimes(now, now, now))
	seedPR(t, database, "acme", "widget", 2,
		withSeedPRAuthor("someone-else"), withSeedPRTimes(now, now, now))
	seedIssue(t, database, "acme", "widget", 3, "open")
	repo, err := database.GetRepoByIdentity(ctx, verifiedGitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	require.NotNil(repo)
	_, err = database.UpsertIssue(ctx, &db.Issue{
		RepoID: repo.ID, PlatformID: 4000, Number: 4,
		URL: "https://github.com/acme/widget/issues/4", Title: "Unrelated issue",
		Author: "someone-else", State: "open",
		CreatedAt: now, UpdatedAt: now, LastActivityAt: now,
	})
	require.NoError(err)

	pullsRR := testutil.DoJSON(t, srv, http.MethodGet, "/api/v1/pulls?state=all&involves_me=true", nil)
	require.Equal(http.StatusOK, pullsRR.Code, pullsRR.Body.String())
	var pulls []pullapi.MergeRequestResponse
	require.NoError(json.Unmarshal(pullsRR.Body.Bytes(), &pulls))
	require.Len(pulls, 1)
	assert.Equal(1, pulls[0].Number)

	issuesRR := testutil.DoJSON(t, srv, http.MethodGet, "/api/v1/issues?state=all&involves_me=true", nil)
	require.Equal(http.StatusOK, issuesRR.Code, issuesRR.Body.String())
	var issues []issueapi.IssueResponse
	require.NoError(json.Unmarshal(issuesRR.Body.Bytes(), &issues))
	require.Len(issues, 1)
	assert.Equal(3, issues[0].Number)

	activityRR := testutil.DoJSON(t, srv, http.MethodGet, "/api/v1/activity?involves_me=true", nil)
	require.Equal(http.StatusOK, activityRR.Code, activityRR.Body.String())
	var activity itemapi.ActivityResponse
	require.NoError(json.Unmarshal(activityRR.Body.Bytes(), &activity))
	require.Len(activity.Items, 2)
	for _, item := range activity.Items {
		assert.Contains([]int{1, 3}, item.ItemNumber)
	}
	assert.Equal(1, mock.authenticatedViewerCalls,
		"viewer identity should be shared by concurrent view requests during the cache TTL")
}

func TestAPIUnassignedFiltersPullsIssuesAndActivity(t *testing.T) {
	runParallelServerTest(t)

	assert := assert.New(t)
	require := require.New(t)
	srv, database, _ := setupTestServer(t)
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Second)

	unassignedPRID := seedPR(t, database, "acme", "widget", 1, withSeedPRTimes(now, now, now))
	assignedPRID := seedPR(t, database, "acme", "widget", 2, withSeedPRTimes(now, now, now))
	seedPR(t, database, "acme", "widget", 5, withSeedPRTimes(now, now, now))
	repo, err := database.GetRepoByIdentity(ctx, verifiedGitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	require.NotNil(repo)
	require.NoError(database.UpdateMergeRequestAssignees(ctx, repo.ID, unassignedPRID, nil))
	require.NoError(database.UpdateMergeRequestAssignees(ctx, repo.ID, assignedPRID, []string{"alice"}))

	seedIssue(t, database, "acme", "widget", 3, "open")
	assignedIssueID := seedIssue(t, database, "acme", "widget", 4, "open")
	require.NoError(database.UpdateIssueAssignees(ctx, repo.ID, assignedIssueID, []string{"bob"}))

	pullsRR := testutil.DoJSON(t, srv, http.MethodGet, "/api/v1/pulls?state=all&unassigned=true", nil)
	require.Equal(http.StatusOK, pullsRR.Code, pullsRR.Body.String())
	var pulls []pullapi.MergeRequestResponse
	require.NoError(json.Unmarshal(pullsRR.Body.Bytes(), &pulls))
	require.Len(pulls, 1)
	assert.Equal(1, pulls[0].Number)

	issuesRR := testutil.DoJSON(t, srv, http.MethodGet, "/api/v1/issues?state=all&unassigned=true", nil)
	require.Equal(http.StatusOK, issuesRR.Code, issuesRR.Body.String())
	var issues []issueapi.IssueResponse
	require.NoError(json.Unmarshal(issuesRR.Body.Bytes(), &issues))
	require.Len(issues, 1)
	assert.Equal(3, issues[0].Number)

	activityRR := testutil.DoJSON(t, srv, http.MethodGet, "/api/v1/activity?unassigned=true", nil)
	require.Equal(http.StatusOK, activityRR.Code, activityRR.Body.String())
	var activity itemapi.ActivityResponse
	require.NoError(json.Unmarshal(activityRR.Body.Bytes(), &activity))
	require.Len(activity.Items, 2)
	assert.ElementsMatch([]int{1, 3}, []int{activity.Items[0].ItemNumber, activity.Items[1].ItemNumber})
	require.Len(activity.ItemActivity, 2)
	assert.ElementsMatch([]int{1, 3}, []int{
		activity.ItemActivity[0].ItemNumber,
		activity.ItemActivity[1].ItemNumber,
	})
}

func TestAPIRepositorySyncRecoversDisabledIssueScopeThroughSQLite(t *testing.T) {
	runParallelServerTest(t)

	assert := assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	disabledErr := platform.RepositoryFeatureDisabled(
		platform.KindGitHub, "github.com", platform.RepositoryFeatureIssues,
		errors.New("repository issues disabled"),
	)

	var issueListCalls atomic.Int32
	var issuesDisabled atomic.Bool
	issuesDisabled.Store(true)
	mock := &mockGH{
		listOpenIssuesFn: func(context.Context, string, string) ([]*gh.Issue, error) {
			issueListCalls.Add(1)
			if issuesDisabled.Load() {
				return nil, disabledErr
			}
			return []*gh.Issue{{
				ID:        new(int64(7001)),
				Number:    new(7),
				Title:     new("issues are enabled again"),
				State:     new("open"),
				HTMLURL:   new("https://github.com/acme/widget/issues/7"),
				User:      &gh.User{Login: new("ada")},
				CreatedAt: &gh.Timestamp{Time: now},
				UpdatedAt: &gh.Timestamp{Time: now},
			}}, nil
		},
	}
	srv, database, syncer := setupTestServerWithMock(t, mock)

	syncer.RunOnce(ctx)
	syncer.RunOnce(ctx)
	assert.Equal(int32(1), issueListCalls.Load(),
		"background sync must suppress the disabled issue scope")
	repo, err := database.GetRepoByIdentity(
		ctx, verifiedGitHubRepoIdentity("github.com", "acme", "widget"),
	)
	require.NoError(err)
	require.NotNil(repo)
	assert.Empty(repo.LastSyncError,
		"a disabled repository feature is not a transient sync failure")

	issuesDisabled.Store(false)
	done := make(chan struct{}, 1)
	syncer.SetOnStatusChange(func(status *ghclient.SyncStatus) {
		if !status.Running {
			select {
			case done <- struct{}{}:
			default:
			}
		}
	})
	rr := testutil.DoJSON(
		t,
		srv,
		http.MethodPost,
		"/api/v1/sync?only_repo=gh|github.com/acme/widget",
		nil)

	require.Equal(http.StatusAccepted, rr.Code, rr.Body.String())
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		require.Fail("expected explicit repository sync to complete")
	}

	assert.Equal(int32(2), issueListCalls.Load(),
		"explicit repository sync must bypass the feature cooldown")
	issue, err := database.GetIssue(
		ctx, "github", "github.com", "acme", "widget", 7,
	)
	require.NoError(err)
	require.NotNil(issue)
	assert.Equal("issues are enabled again", issue.Title)
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

// TestE2EGraphQLIssueSyncThroughAPI is a full-stack test that runs the
// real GraphQL issue sync path against a mocked GraphQL HTTP backend
// with real SQLite, then verifies the resulting issue data through
// the HTTP API. Exercises: GraphQL HTTP → adapter → NormalizeIssue →
// UpsertIssue → HTTP API handler → JSON response.
func TestE2EGraphQLIssueSyncThroughAPI(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	ctx := t.Context()

	now := time.Now().UTC().Truncate(time.Second).Format(time.RFC3339)

	// Mock GraphQL backend returning a single issue with a label
	// and a comment.
	gqlSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		if bytes.Contains(body, []byte("pullRequests")) {
			_, _ = w.Write([]byte(`{"data":{"repository":{"pullRequests":{"nodes":[],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}`))
			return
		}
		resp := `{"data":{"repository":{"issues":{"nodes":[{
			"databaseId":80000,
			"number":80,
			"title":"Full stack GraphQL issue",
			"state":"OPEN",
			"body":"Synced through the HTTP API",
			"url":"https://github.com/acme/widget/issues/80",
			"author":{"__typename":"Bot","login":"renovate"},
			"createdAt":"` + now + `",
			"updatedAt":"` + now + `",
			"closedAt":null,
			"labels":{"nodes":[{"name":"bug","color":"d73a4a","description":"","isDefault":false}]},
			"comments":{"totalCount":1,"nodes":[{"databaseId":801,"fullDatabaseId":"3714845345","author":{"login":"judy"},"body":"full stack comment","isMinimized":true,"minimizedReason":"ABUSE","createdAt":"` + now + `","updatedAt":"` + now + `"}],"pageInfo":{"hasNextPage":false,"endCursor":""}}
		}],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}`
		_, _ = w.Write([]byte(resp))
	}))
	defer gqlSrv.Close()

	// REST mock: PR list returns 304 (skip PR sync), issue list
	// returns minimal data to pass the ETag gate so GraphQL runs.
	issueID := int64(80000)
	issueNumber := 80
	issueTitle := "Full stack GraphQL issue"
	issueState := "open"
	issueURL := "https://github.com/acme/widget/issues/80"
	issueLogin := "ivy"
	issueTime := gh.Timestamp{Time: time.Now().UTC().Truncate(time.Second)}
	mock := &mockGH{
		listOpenPRsErr: &gh.ErrorResponse{
			Response: &http.Response{StatusCode: http.StatusNotModified},
		},
		listOpenIssuesFn: func(_ context.Context, _, _ string) ([]*gh.Issue, error) {
			return []*gh.Issue{{
				ID:        &issueID,
				Number:    &issueNumber,
				Title:     &issueTitle,
				State:     &issueState,
				HTMLURL:   &issueURL,
				User:      &gh.User{Login: &issueLogin},
				CreatedAt: &issueTime,
				UpdatedAt: &issueTime,
			}}, nil
		},
	}
	srv, _, syncer := setupTestServerWithMock(t, mock)

	// Wire a real GraphQLFetcher pointing at the mock GraphQL server
	// into the syncer.
	gqlClient := githubv4.NewEnterpriseClient(gqlSrv.URL, gqlSrv.Client())
	syncer.SetFetchers(map[string]*ghclient.GraphQLFetcher{
		"github.com": ghclient.NewGraphQLFetcherWithClient(gqlClient, nil),
	})

	// Trigger the real sync pipeline.
	syncer.RunOnce(ctx)

	// Verify through the HTTP API that issue data flowed end-to-end.
	client := setupTestClient(t, srv)

	listResp, err := client.HTTP.ListIssuesWithResponse(ctx, &generated.ListIssuesRequestOptions{})
	require.NoError(err)
	require.Equal(200, listResp.StatusCode)
	require.NotNil(listResp.JSON200)
	require.Len(*listResp.JSON200, 1)

	apiIssue := (*listResp.JSON200)[0]
	assert.Equal(int64(80), apiIssue.Number)
	assert.Equal("Full stack GraphQL issue", apiIssue.Title)
	assert.Equal("renovate[bot]", apiIssue.Author)
	assert.Equal("open", apiIssue.State)
	require.NotNil(apiIssue.Labels)
	require.Len(apiIssue.Labels, 1)
	assert.Equal("bug", apiIssue.Labels[0].Name)

	detailResp, err := client.HTTP.GetIssueWithResponse(ctx, &generated.GetIssueRequestOptions{PathParams: &generated.GetIssuePath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(80)}})
	require.NoError(err)
	require.Equal(200, detailResp.StatusCode)
	require.NotNil(detailResp.JSON200)
	assert.Equal("Synced through the HTTP API", detailResp.JSON200.Issue.Body)
	assert.Equal(int64(1), detailResp.JSON200.Issue.CommentCount)
	require.NotNil(detailResp.JSON200.Events)
	require.Len(detailResp.JSON200.Events, 1)
	require.NotNil(detailResp.JSON200.Events[0].PlatformID)
	assert.Equal(int64(3714845345), *detailResp.JSON200.Events[0].PlatformID)
	assert.JSONEq(
		`{"provider_hidden":true,"provider_hidden_reason":"ABUSE"}`,
		detailResp.JSON200.Events[0].MetadataJSON,
	)
}

func TestE2ELargeRepoSkipsGraphQLAndUsesConditionalIssueDetail(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	ctx := t.Context()

	changedAt := time.Now().UTC().Truncate(time.Second)
	unchangedAt := changedAt.Add(-2 * time.Hour)
	detailFetchedAt := changedAt.Add(-time.Hour)

	buildIssue := func(number int, updatedAt time.Time, title string) *gh.Issue {
		id := int64(number * 1000)
		state := "open"
		url := fmt.Sprintf("https://github.com/acme/widget/issues/%d", number)
		author := "alice"
		body := fmt.Sprintf("issue body %d", number)
		comments := 1
		created := gh.Timestamp{Time: unchangedAt}
		updated := gh.Timestamp{Time: updatedAt}
		return &gh.Issue{
			ID:        &id,
			Number:    &number,
			State:     &state,
			Title:     &title,
			Body:      &body,
			HTMLURL:   &url,
			User:      &gh.User{Login: &author},
			Comments:  &comments,
			CreatedAt: &created,
			UpdatedAt: &updated,
		}
	}

	openIssues := make([]*gh.Issue, 0, 100)
	for number := 1; number <= 100; number++ {
		openIssues = append(openIssues,
			buildIssue(number, unchangedAt, fmt.Sprintf("existing issue %d", number)),
		)
	}

	var conditionalCalls atomic.Int32
	var graphQLIssueCalls atomic.Int32
	mock := &mockGH{
		listOpenPullRequestsFn: func(_ context.Context, _, _ string) ([]*gh.PullRequest, error) {
			return nil, &gh.ErrorResponse{
				Response: &http.Response{StatusCode: http.StatusNotModified},
			}
		},
		listOpenIssuesFn: func(_ context.Context, _, _ string) ([]*gh.Issue, error) {
			return openIssues, nil
		},
		getIssueIfChangedFn: func(_ context.Context, _, _ string, number int, etag string) (*gh.Issue, string, bool, error) {
			conditionalCalls.Add(1)
			require.Equal(1, number)
			require.Equal(`"issue-etag-v1"`, etag)
			return buildIssue(number, changedAt, "changed issue detail"), `"issue-etag-v2"`, false, nil
		},
		listIssueCommentsFn: func(_ context.Context, _, _ string, number int) ([]*gh.IssueComment, error) {
			if number != 1 {
				return nil, nil
			}
			id := int64(9101)
			body := "issue detail comment"
			author := "reviewer"
			created := gh.Timestamp{Time: changedAt}
			return []*gh.IssueComment{{
				ID:        &id,
				Body:      &body,
				User:      &gh.User{Login: &author},
				CreatedAt: &created,
				UpdatedAt: &created,
			}}, nil
		},
	}

	gqlSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if bytes.Contains(body, []byte("issues")) {
			graphQLIssueCalls.Add(1)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"errors":[{"message":"bulk issue fetch should be skipped"}]}`))
	}))
	defer gqlSrv.Close()

	database := dbtest.Open(t)
	repoID, err := database.UpsertRepo(ctx, verifiedGitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	for number := 1; number <= 100; number++ {
		var fetchedAt *time.Time
		if number != 1 {
			fetchedAt = &detailFetchedAt
		}
		_, err := database.UpsertIssue(ctx, &db.Issue{
			RepoID:          repoID,
			PlatformID:      int64(number * 1000),
			Number:          number,
			URL:             fmt.Sprintf("https://github.com/acme/widget/issues/%d", number),
			Title:           fmt.Sprintf("existing issue %d", number),
			Author:          "alice",
			State:           "open",
			CreatedAt:       unchangedAt,
			UpdatedAt:       unchangedAt,
			LastActivityAt:  unchangedAt,
			DetailFetchedAt: fetchedAt,
		})
		require.NoError(err)
	}
	require.NoError(database.UpsertHTTPEtag(
		ctx, "github", "github.com", "acme", "widget",
		"issue", 1, `"issue-etag-v1"`,
	))

	syncer := ghclient.NewSyncer(
		map[string]ghclient.Client{"github.com": mock},
		database,
		nil,
		defaultTestRepos,
		time.Minute,
		nil,
		map[string]*ghclient.SyncBudget{"github.com": ghclient.NewSyncBudget(10000)},
	)
	t.Cleanup(syncer.Stop)
	syncer.SetFetchers(map[string]*ghclient.GraphQLFetcher{
		"github.com": ghclient.NewGraphQLFetcherWithClient(
			githubv4.NewEnterpriseClient(gqlSrv.URL, gqlSrv.Client()),
			nil,
		),
	})

	srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{})
	t.Cleanup(func() { gracefulShutdown(t, srv) })

	syncer.RunOnce(ctx)

	assert.Zero(int(graphQLIssueCalls.Load()),
		"large existing repo refresh should not bulk-fetch issues through GraphQL")
	assert.Equal(int32(1), conditionalCalls.Load(),
		"only the missing-detail issue should run a conditional detail fetch")

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/issues/gh/acme/widget/1", nil)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	require.Equal(http.StatusOK, rr.Code)

	var detailResp issueapi.IssueDetailResponse
	require.NoError(json.Unmarshal(rr.Body.Bytes(), &detailResp))
	require.NotNil(detailResp.Issue)
	assert.Equal("changed issue detail", detailResp.Issue.Title)
	assert.Equal(1, detailResp.Issue.CommentCount)
	require.Len(detailResp.Events, 1)
	assert.Equal("issue detail comment", detailResp.Events[0].Body)

	etag, err := database.GetHTTPEtag(
		ctx, "github", "github.com", "acme", "widget",
		"issue", 1,
	)
	require.NoError(err)
	assert.Equal(`"issue-etag-v2"`, etag)
}

// TestE2EGraphQLIssueSyncTrustsTotalCount pre-seeds an issue with a
// stale CommentCount, runs a real GraphQL sync with truncated
// comments (totalCount > nodes, HasNextPage=true), and forces the
// REST fallback to fail. The only remaining count in the DB is
// whatever UpsertIssue wrote from NormalizeIssue — which must be
// GraphQL's TotalCount, not the stale existing.CommentCount.
// Regression test for the "preserve existing.CommentCount" overwrite.
func TestE2EGraphQLIssueSyncTrustsTotalCount(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	ctx := t.Context()

	now := time.Date(2026, 4, 12, 14, 0, 0, 0, time.UTC)
	nowRFC3339 := now.Format(time.RFC3339)

	// GraphQL: totalCount=42, HasNextPage=true → CommentsComplete=false.
	// REST ListIssueComments will error. Stale DB count is 5.
	// Post-sync count must be 42 (fresh GraphQL TotalCount), not 5.
	gqlSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		if bytes.Contains(body, []byte("issue(number:")) {
			_, _ = w.Write([]byte(`{"data":{"repository":{"issue":{"comments":{"nodes":[{
				"databaseId":902,
				"isMinimized":false,
				"minimizedReason":null
			}],"pageInfo":{"hasNextPage":false,"endCursor":null}}}}}}`))
			return
		}
		if bytes.Contains(body, []byte("pullRequests")) {
			_, _ = w.Write([]byte(`{"data":{"repository":{"pullRequests":{"nodes":[],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}`))
			return
		}
		resp := `{"data":{"repository":{"issues":{"nodes":[{
			"databaseId":90000,
			"number":90,
			"title":"Stale count issue",
			"state":"OPEN",
			"body":"GraphQL count must win",
			"url":"https://github.com/acme/widget/issues/90",
			"author":{"login":"kate"},
			"createdAt":"` + nowRFC3339 + `",
			"updatedAt":"` + nowRFC3339 + `",
			"closedAt":null,
			"labels":{"nodes":[]},
			"comments":{"totalCount":42,"nodes":[{"databaseId":901,"author":{"login":"leo"},"body":"one","createdAt":"` + nowRFC3339 + `","updatedAt":"` + nowRFC3339 + `"}],"pageInfo":{"hasNextPage":true,"endCursor":"cursor1"}}
		}],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}`
		_, _ = w.Write([]byte(resp))
	}))
	defer gqlSrv.Close()

	issueID := int64(90000)
	issueNumber := 90
	issueTitle := "Stale count issue"
	issueState := "open"
	issueURL := "https://github.com/acme/widget/issues/90"
	issueLogin := "kate"
	issueTime := gh.Timestamp{Time: now}
	mock := &mockGH{
		listOpenPRsErr: &gh.ErrorResponse{
			Response: &http.Response{StatusCode: http.StatusNotModified},
		},
		listIssueCommentsErr: fmt.Errorf("transient comments failure"),
		listOpenIssuesFn: func(_ context.Context, _, _ string) ([]*gh.Issue, error) {
			return []*gh.Issue{{
				ID:        &issueID,
				Number:    &issueNumber,
				Title:     &issueTitle,
				State:     &issueState,
				HTMLURL:   &issueURL,
				User:      &gh.User{Login: &issueLogin},
				CreatedAt: &issueTime,
				UpdatedAt: &issueTime,
			}}, nil
		},
	}
	srv, database, syncer := setupTestServerWithMock(t, mock)

	// Pre-seed DB with a stale CommentCount (5). REST fallback fails,
	// so UpsertIssue's value is what survives. With the bug, it's 5.
	// Without the bug, it's TotalCount=42.
	//
	// The pre-seed UpdatedAt must be strictly older than the
	// GraphQL mock's updatedAt (`now` above). UpsertIssue's
	// stale-snapshot guard skips the update when
	// excluded.updated_at < forge_issues.updated_at, so if
	// `stale` rolls forward past `now` (common under the race
	// detector's slower execution) the fresh GraphQL data would be
	// blocked and the assertion below would read back the stale 5
	// — a test-only flake, not a production bug.
	repoID, err := database.UpsertRepo(ctx, db.RepoIdentity{
		Platform:       "github",
		PlatformHost:   "github.com",
		PlatformRepoID: "repo-acme-widget",
		Owner:          "acme",
		Name:           "widget",
	})
	require.NoError(err)
	stale := now.Add(-time.Second)
	_, err = database.UpsertIssue(ctx, &db.Issue{
		RepoID:         repoID,
		PlatformID:     90000,
		Number:         90,
		URL:            issueURL,
		Title:          issueTitle,
		Author:         issueLogin,
		State:          "open",
		CommentCount:   5, // stale
		CreatedAt:      stale,
		UpdatedAt:      stale,
		LastActivityAt: stale,
	})
	require.NoError(err)

	gqlClient := githubv4.NewEnterpriseClient(gqlSrv.URL, gqlSrv.Client())
	syncer.SetFetchers(map[string]*ghclient.GraphQLFetcher{
		"github.com": ghclient.NewGraphQLFetcherWithClient(gqlClient, nil),
	})

	syncer.RunOnce(ctx)

	// API must expose GraphQL TotalCount (42), not stale DB (5).
	// With the preservation bug, count would remain 5.
	client := setupTestClient(t, srv)
	detailResp, err := client.HTTP.GetIssueWithResponse(ctx, &generated.GetIssueRequestOptions{PathParams: &generated.GetIssuePath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(90)}})
	require.NoError(err)
	require.Equal(200, detailResp.StatusCode)
	require.NotNil(detailResp.JSON200)
	assert.Equal(int64(42), detailResp.JSON200.Issue.CommentCount)
}

func TestE2EIssueDetailRefreshesEditedCommentBody(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	ctx := t.Context()

	now := time.Now().UTC().Truncate(time.Second).Add(-time.Hour)
	issueNumber := 161
	issueID := int64(161000)
	issueTitle := "Edited issue comment refresh"
	issueState := "open"
	issueURL := "https://github.com/acme/widget/issues/161"
	commentID := int64(9011)
	commentAuthor := "reviewer"
	commentCreatedAt := now.Add(2 * time.Minute)
	commentBody := "original issue body"

	mock := &mockGH{
		listOpenPullRequestsFn: func(_ context.Context, _, _ string) ([]*gh.PullRequest, error) {
			return nil, &gh.ErrorResponse{
				Response: &http.Response{StatusCode: http.StatusNotModified},
			}
		},
		getIssueFn: func(_ context.Context, _, _ string, number int) (*gh.Issue, error) {
			require.Equal(issueNumber, number)
			return &gh.Issue{
				ID:        &issueID,
				Number:    &issueNumber,
				Title:     &issueTitle,
				State:     &issueState,
				HTMLURL:   &issueURL,
				UpdatedAt: &gh.Timestamp{Time: now},
				CreatedAt: &gh.Timestamp{Time: now},
			}, nil
		},
	}
	issueListCalls := 0
	mock.listOpenIssuesFn = func(_ context.Context, _, _ string) ([]*gh.Issue, error) {
		issueListCalls++
		if issueListCalls == 1 {
			return []*gh.Issue{{
				ID:        &issueID,
				Number:    &issueNumber,
				Title:     &issueTitle,
				State:     &issueState,
				HTMLURL:   &issueURL,
				UpdatedAt: &gh.Timestamp{Time: now},
				CreatedAt: &gh.Timestamp{Time: now},
			}}, nil
		}
		return nil, &gh.ErrorResponse{
			Response: &http.Response{StatusCode: http.StatusNotModified},
		}
	}
	mockComments := []*gh.IssueComment{{
		ID:        &commentID,
		Body:      &commentBody,
		User:      &gh.User{Login: &commentAuthor},
		CreatedAt: &gh.Timestamp{Time: commentCreatedAt},
		UpdatedAt: &gh.Timestamp{Time: commentCreatedAt},
	}}
	mock.listIssueCommentsFn = func(_ context.Context, _, _ string, _ int) ([]*gh.IssueComment, error) {
		return mockComments, nil
	}

	database := dbtest.Open(t)

	syncer := ghclient.NewSyncer(
		map[string]ghclient.Client{"github.com": mock},
		database,
		nil,
		defaultTestRepos,
		time.Minute,
		nil,
		map[string]*ghclient.SyncBudget{"github.com": ghclient.NewSyncBudget(10000)},
	)
	t.Cleanup(syncer.Stop)

	srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{})
	t.Cleanup(func() { gracefulShutdown(t, srv) })
	client := setupTestClient(t, srv)

	syncer.RunOnce(ctx)

	firstResp, err := client.HTTP.GetIssueWithResponse(ctx, &generated.GetIssueRequestOptions{PathParams: &generated.GetIssuePath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(int64(issueNumber))}})
	require.NoError(err)
	require.Equal(http.StatusOK, firstResp.StatusCode)
	require.NotNil(firstResp.JSON200)
	require.NotNil(firstResp.JSON200.Events)
	require.Len(firstResp.JSON200.Events, 1)
	assert.Equal("original issue body", firstResp.JSON200.Events[0].Body)

	editedBody := "edited issue body"
	mockComments = []*gh.IssueComment{{
		ID:        &commentID,
		Body:      &editedBody,
		User:      &gh.User{Login: &commentAuthor},
		CreatedAt: &gh.Timestamp{Time: commentCreatedAt},
		UpdatedAt: &gh.Timestamp{Time: now.Add(4 * time.Minute)},
	}}

	syncer.RunOnce(ctx)

	secondResp, err := client.HTTP.GetIssueWithResponse(ctx, &generated.GetIssueRequestOptions{PathParams: &generated.GetIssuePath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(int64(issueNumber))}})
	require.NoError(err)
	require.Equal(http.StatusOK, secondResp.StatusCode)
	require.NotNil(secondResp.JSON200)
	require.NotNil(secondResp.JSON200.Events)
	require.Len(secondResp.JSON200.Events, 1)
	assert.Equal("edited issue body", secondResp.JSON200.Events[0].Body)
}

func TestE2EIssueDetailRemovesDeletedCommentWhenIssueListIsUnchanged(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	ctx := t.Context()

	now := time.Now().UTC().Truncate(time.Second).Add(-time.Hour)
	issueNumber := 161
	issueID := int64(161000)
	issueTitle := "Deleted issue comment refresh"
	issueState := "open"
	issueURL := "https://github.com/acme/widget/issues/161"
	commentID := int64(9011)
	commentAuthor := "reviewer"
	commentCreatedAt := now.Add(2 * time.Minute)
	commentBody := "issue body to remove"

	mock := &mockGH{
		listOpenPullRequestsFn: func(_ context.Context, _, _ string) ([]*gh.PullRequest, error) {
			return nil, &gh.ErrorResponse{
				Response: &http.Response{StatusCode: http.StatusNotModified},
			}
		},
		getIssueFn: func(_ context.Context, _, _ string, number int) (*gh.Issue, error) {
			require.Equal(issueNumber, number)
			return &gh.Issue{
				ID:        &issueID,
				Number:    &issueNumber,
				Title:     &issueTitle,
				State:     &issueState,
				HTMLURL:   &issueURL,
				UpdatedAt: &gh.Timestamp{Time: now},
				CreatedAt: &gh.Timestamp{Time: now},
			}, nil
		},
	}
	issueListCalls := 0
	mock.listOpenIssuesFn = func(_ context.Context, _, _ string) ([]*gh.Issue, error) {
		issueListCalls++
		if issueListCalls == 1 {
			return []*gh.Issue{{
				ID:        &issueID,
				Number:    &issueNumber,
				Title:     &issueTitle,
				State:     &issueState,
				HTMLURL:   &issueURL,
				UpdatedAt: &gh.Timestamp{Time: now},
				CreatedAt: &gh.Timestamp{Time: now},
			}}, nil
		}
		return nil, &gh.ErrorResponse{
			Response: &http.Response{StatusCode: http.StatusNotModified},
		}
	}
	mockComments := []*gh.IssueComment{{
		ID:        &commentID,
		Body:      &commentBody,
		User:      &gh.User{Login: &commentAuthor},
		CreatedAt: &gh.Timestamp{Time: commentCreatedAt},
		UpdatedAt: &gh.Timestamp{Time: commentCreatedAt},
	}}
	// The issue detail API must expose the unique persisted count even when a
	// complete provider snapshot repeats the same comment identity.
	mockComments = append(mockComments, mockComments[0])
	mock.listIssueCommentsFn = func(_ context.Context, _, _ string, _ int) ([]*gh.IssueComment, error) {
		return mockComments, nil
	}

	database := dbtest.Open(t)

	syncer := ghclient.NewSyncer(
		map[string]ghclient.Client{"github.com": mock},
		database,
		nil,
		defaultTestRepos,
		time.Minute,
		nil,
		map[string]*ghclient.SyncBudget{"github.com": ghclient.NewSyncBudget(10000)},
	)
	t.Cleanup(syncer.Stop)

	srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{})
	t.Cleanup(func() { gracefulShutdown(t, srv) })
	client := setupTestClient(t, srv)

	syncer.RunOnce(ctx)

	firstResp, err := client.HTTP.GetIssueWithResponse(ctx, &generated.GetIssueRequestOptions{PathParams: &generated.GetIssuePath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(int64(issueNumber))}})
	require.NoError(err)
	require.Equal(http.StatusOK, firstResp.StatusCode)
	require.NotNil(firstResp.JSON200)
	require.Equal(int64(1), firstResp.JSON200.Issue.CommentCount)
	require.Equal(commentCreatedAt.UTC(), firstResp.JSON200.Issue.LastActivityAt.UTC())
	require.NotNil(firstResp.JSON200.Events)
	require.Len(firstResp.JSON200.Events, 1)
	assert.Equal("issue body to remove", firstResp.JSON200.Events[0].Body)

	mockComments = []*gh.IssueComment{}

	syncer.RunOnce(ctx)

	secondResp, err := client.HTTP.GetIssueWithResponse(ctx, &generated.GetIssueRequestOptions{PathParams: &generated.GetIssuePath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(int64(issueNumber))}})
	require.NoError(err)
	require.Equal(http.StatusOK, secondResp.StatusCode)
	require.NotNil(secondResp.JSON200)
	require.Equal(int64(0), secondResp.JSON200.Issue.CommentCount)
	require.Equal(now.UTC(), secondResp.JSON200.Issue.LastActivityAt.UTC())
	require.NotNil(secondResp.JSON200.Events)
	require.Empty(secondResp.JSON200.Events)
}

func TestE2EIssueDetailRemovesDeletedCommentWhenAnotherIssueChanges(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	ctx := t.Context()

	now := time.Now().UTC().Truncate(time.Second).Add(-time.Hour)
	targetNumber := 161
	targetID := int64(161000)
	targetTitle := "Target issue keeps stale comment"
	targetURL := "https://github.com/acme/widget/issues/161"
	otherNumber := 162
	otherID := int64(162000)
	otherTitle := "Other issue changes"
	otherURL := "https://github.com/acme/widget/issues/162"
	issueState := "open"
	commentID := int64(9060)
	commentAuthor := "reviewer"
	commentCreatedAt := now.Add(2 * time.Minute)
	targetCommentBody := "target issue comment"
	targetComments := []*gh.IssueComment{{
		ID:        &commentID,
		Body:      &targetCommentBody,
		User:      &gh.User{Login: &commentAuthor},
		CreatedAt: &gh.Timestamp{Time: commentCreatedAt},
		UpdatedAt: &gh.Timestamp{Time: commentCreatedAt},
	}}
	otherUpdatedAt := now

	issueListCalls := 0
	mock := &mockGH{
		listOpenPullRequestsFn: func(_ context.Context, _, _ string) ([]*gh.PullRequest, error) {
			return nil, &gh.ErrorResponse{
				Response: &http.Response{StatusCode: http.StatusNotModified},
			}
		},
		listOpenIssuesFn: func(_ context.Context, _, _ string) ([]*gh.Issue, error) {
			issueListCalls++
			if issueListCalls > 1 {
				otherUpdatedAt = now.Add(5 * time.Minute)
			}
			return []*gh.Issue{
				{
					ID:        &targetID,
					Number:    &targetNumber,
					Title:     &targetTitle,
					State:     &issueState,
					HTMLURL:   &targetURL,
					UpdatedAt: &gh.Timestamp{Time: now},
					CreatedAt: &gh.Timestamp{Time: now},
				},
				{
					ID:        &otherID,
					Number:    &otherNumber,
					Title:     &otherTitle,
					State:     &issueState,
					HTMLURL:   &otherURL,
					UpdatedAt: &gh.Timestamp{Time: otherUpdatedAt},
					CreatedAt: &gh.Timestamp{Time: now},
				},
			}, nil
		},
		getIssueFn: func(_ context.Context, _, _ string, number int) (*gh.Issue, error) {
			switch number {
			case targetNumber:
				return &gh.Issue{
					ID:        &targetID,
					Number:    &targetNumber,
					Title:     &targetTitle,
					State:     &issueState,
					HTMLURL:   &targetURL,
					UpdatedAt: &gh.Timestamp{Time: now},
					CreatedAt: &gh.Timestamp{Time: now},
				}, nil
			case otherNumber:
				return &gh.Issue{
					ID:        &otherID,
					Number:    &otherNumber,
					Title:     &otherTitle,
					State:     &issueState,
					HTMLURL:   &otherURL,
					UpdatedAt: &gh.Timestamp{Time: otherUpdatedAt},
					CreatedAt: &gh.Timestamp{Time: now},
				}, nil
			default:
				return nil, fmt.Errorf("unexpected issue %d", number)
			}
		},
		listIssueCommentsFn: func(_ context.Context, _, _ string, number int) ([]*gh.IssueComment, error) {
			if number == targetNumber {
				return targetComments, nil
			}
			return []*gh.IssueComment{}, nil
		},
	}

	database := dbtest.Open(t)

	syncer := ghclient.NewSyncer(
		map[string]ghclient.Client{"github.com": mock},
		database,
		nil,
		defaultTestRepos,
		time.Minute,
		nil,
		map[string]*ghclient.SyncBudget{"github.com": ghclient.NewSyncBudget(10000)},
	)
	t.Cleanup(syncer.Stop)

	srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{})
	t.Cleanup(func() { gracefulShutdown(t, srv) })
	client := setupTestClient(t, srv)

	syncer.RunOnce(ctx)

	firstResp, err := client.HTTP.GetIssueWithResponse(ctx, &generated.GetIssueRequestOptions{PathParams: &generated.GetIssuePath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(int64(targetNumber))}})
	require.NoError(err)
	require.Equal(http.StatusOK, firstResp.StatusCode)
	require.NotNil(firstResp.JSON200)
	require.Equal(int64(1), firstResp.JSON200.Issue.CommentCount)
	require.NotNil(firstResp.JSON200.Events)
	require.Len(firstResp.JSON200.Events, 1)
	assert.Equal("target issue comment", firstResp.JSON200.Events[0].Body)

	targetComments = []*gh.IssueComment{}

	syncer.RunOnce(ctx)

	secondResp, err := client.HTTP.GetIssueWithResponse(ctx, &generated.GetIssueRequestOptions{PathParams: &generated.GetIssuePath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(int64(targetNumber))}})
	require.NoError(err)
	require.Equal(http.StatusOK, secondResp.StatusCode)
	require.NotNil(secondResp.JSON200)
	require.Equal(int64(0), secondResp.JSON200.Issue.CommentCount)
	require.NotNil(secondResp.JSON200.Events)
	require.Empty(secondResp.JSON200.Events)
}

func TestE2EIssueDetailRemovesDeletedCommentOnFullRefresh(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	ctx := t.Context()

	now := time.Date(2026, 4, 13, 10, 0, 0, 0, time.UTC)
	issueNumber := 171
	issueID := int64(171000)
	issueTitle := "Full refresh deleted issue comment"
	issueState := "open"
	issueURL := "https://github.com/acme/widget/issues/171"
	commentID := int64(9111)
	commentAuthor := "reviewer"
	commentCreatedAt := now.Add(2 * time.Minute)
	commentBody := "issue comment removed on full refresh"
	currentUpdatedAt := now
	currentComments := []*gh.IssueComment{{
		ID:        &commentID,
		Body:      &commentBody,
		User:      &gh.User{Login: &commentAuthor},
		CreatedAt: &gh.Timestamp{Time: commentCreatedAt},
		UpdatedAt: &gh.Timestamp{Time: commentCreatedAt},
	}}

	mock := &mockGH{
		listOpenPullRequestsFn: func(_ context.Context, _, _ string) ([]*gh.PullRequest, error) {
			return nil, &gh.ErrorResponse{
				Response: &http.Response{StatusCode: http.StatusNotModified},
			}
		},
		getIssueFn: func(_ context.Context, _, _ string, number int) (*gh.Issue, error) {
			require.Equal(issueNumber, number)
			return &gh.Issue{
				ID:        &issueID,
				Number:    &issueNumber,
				Title:     &issueTitle,
				State:     &issueState,
				HTMLURL:   &issueURL,
				UpdatedAt: &gh.Timestamp{Time: currentUpdatedAt},
				CreatedAt: &gh.Timestamp{Time: now},
			}, nil
		},
		listIssueCommentsFn: func(_ context.Context, _, _ string, number int) ([]*gh.IssueComment, error) {
			require.Equal(issueNumber, number)
			return currentComments, nil
		},
	}

	database := dbtest.Open(t)

	syncer := ghclient.NewSyncer(
		map[string]ghclient.Client{"github.com": mock},
		database,
		nil,
		defaultTestRepos,
		time.Minute,
		nil,
		map[string]*ghclient.SyncBudget{"github.com": ghclient.NewSyncBudget(10000)},
	)
	t.Cleanup(syncer.Stop)

	srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{})
	t.Cleanup(func() { gracefulShutdown(t, srv) })
	client := setupTestClient(t, srv)

	require.NoError(syncer.SyncIssue(ctx, "acme", "widget", issueNumber))

	firstResp, err := client.HTTP.GetIssueWithResponse(ctx, &generated.GetIssueRequestOptions{PathParams: &generated.GetIssuePath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(int64(issueNumber))}})
	require.NoError(err)
	require.Equal(http.StatusOK, firstResp.StatusCode)
	require.NotNil(firstResp.JSON200)
	require.Equal(int64(1), firstResp.JSON200.Issue.CommentCount)
	require.Equal(commentCreatedAt.UTC(), firstResp.JSON200.Issue.LastActivityAt.UTC())
	require.NotNil(firstResp.JSON200.Events)
	require.Len(firstResp.JSON200.Events, 1)
	assert.Equal("issue comment removed on full refresh", firstResp.JSON200.Events[0].Body)

	currentUpdatedAt = now.Add(time.Minute)
	currentComments = []*gh.IssueComment{}

	require.NoError(syncer.SyncIssue(ctx, "acme", "widget", issueNumber))

	secondResp, err := client.HTTP.GetIssueWithResponse(ctx, &generated.GetIssueRequestOptions{PathParams: &generated.GetIssuePath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(int64(issueNumber))}})
	require.NoError(err)
	require.Equal(http.StatusOK, secondResp.StatusCode)
	require.NotNil(secondResp.JSON200)
	require.Equal(int64(0), secondResp.JSON200.Issue.CommentCount)
	require.Equal(currentUpdatedAt.UTC(), secondResp.JSON200.Issue.LastActivityAt.UTC())
	require.NotNil(secondResp.JSON200.Events)
	require.Empty(secondResp.JSON200.Events)
}

func TestE2EIssueDetailRemovesDeletedCommentOnGraphQLBulkSync(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	ctx := t.Context()

	now := time.Date(2026, 4, 13, 11, 0, 0, 0, time.UTC)
	firstUpdatedAt := now.Format(time.RFC3339)
	secondUpdatedAt := now.Add(time.Minute).Format(time.RFC3339)
	currentUpdatedAt := firstUpdatedAt
	currentCommentJSON := `{"totalCount":1,"nodes":[{"databaseId":9122,"author":{"login":"commenter"},"body":"bulk comment removed","createdAt":"` + firstUpdatedAt + `","updatedAt":"` + firstUpdatedAt + `"}],"pageInfo":{"hasNextPage":false,"endCursor":""}}`

	gqlSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if bytes.Contains(body, []byte("pullRequests")) {
			_, _ = w.Write([]byte(`{"data":{"repository":{"pullRequests":{"nodes":[],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}`))
			return
		}
		resp := `{"data":{"repository":{"issues":{"nodes":[{
			"databaseId":171100,
			"number":172,
			"title":"Bulk deleted comment issue",
			"state":"OPEN",
			"body":"GraphQL bulk issue",
			"url":"https://github.com/acme/widget/issues/172",
			"author":{"login":"heidi"},
			"createdAt":"` + firstUpdatedAt + `",
			"updatedAt":"` + currentUpdatedAt + `",
			"closedAt":null,
			"labels":{"nodes":[]},
			"comments":` + currentCommentJSON + `
		}],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}`
		_, _ = w.Write([]byte(resp))
	}))
	defer gqlSrv.Close()

	issueID := int64(171100)
	issueNumber := 172
	issueTitle := "Bulk deleted comment issue"
	issueState := "open"
	issueURL := "https://github.com/acme/widget/issues/172"
	issueAuthor := "heidi"
	issueTime := gh.Timestamp{Time: now}
	mock := &mockGH{
		listOpenPullRequestsFn: func(_ context.Context, _, _ string) ([]*gh.PullRequest, error) {
			return nil, &gh.ErrorResponse{
				Response: &http.Response{StatusCode: http.StatusNotModified},
			}
		},
		listOpenIssuesFn: func(_ context.Context, _, _ string) ([]*gh.Issue, error) {
			return []*gh.Issue{{
				ID:        &issueID,
				Number:    &issueNumber,
				Title:     &issueTitle,
				State:     &issueState,
				HTMLURL:   &issueURL,
				User:      &gh.User{Login: &issueAuthor},
				CreatedAt: &issueTime,
				UpdatedAt: &issueTime,
			}}, nil
		},
	}

	database := dbtest.Open(t)

	syncer := ghclient.NewSyncer(
		map[string]ghclient.Client{"github.com": mock},
		database,
		nil,
		defaultTestRepos,
		time.Minute,
		nil,
		map[string]*ghclient.SyncBudget{"github.com": ghclient.NewSyncBudget(10000)},
	)
	t.Cleanup(syncer.Stop)

	gqlClient := githubv4.NewEnterpriseClient(gqlSrv.URL, gqlSrv.Client())
	syncer.SetFetchers(map[string]*ghclient.GraphQLFetcher{
		"github.com": ghclient.NewGraphQLFetcherWithClient(gqlClient, nil),
	})

	srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{})
	t.Cleanup(func() { gracefulShutdown(t, srv) })
	client := setupTestClient(t, srv)

	syncer.RunOnce(ctx)

	firstResp, err := client.HTTP.GetIssueWithResponse(ctx, &generated.GetIssueRequestOptions{PathParams: &generated.GetIssuePath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(int64(issueNumber))}})
	require.NoError(err)
	require.Equal(http.StatusOK, firstResp.StatusCode)
	require.NotNil(firstResp.JSON200)
	require.Equal(int64(1), firstResp.JSON200.Issue.CommentCount)
	require.Equal(now.UTC(), firstResp.JSON200.Issue.LastActivityAt.UTC())
	require.NotNil(firstResp.JSON200.Events)
	require.Len(firstResp.JSON200.Events, 1)
	assert.Equal("bulk comment removed", firstResp.JSON200.Events[0].Body)

	currentUpdatedAt = secondUpdatedAt
	currentCommentJSON = `{"totalCount":0,"nodes":[],"pageInfo":{"hasNextPage":false,"endCursor":""}}`

	syncer.RunOnce(ctx)

	secondResp, err := client.HTTP.GetIssueWithResponse(ctx, &generated.GetIssueRequestOptions{PathParams: &generated.GetIssuePath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(int64(issueNumber))}})
	require.NoError(err)
	require.Equal(http.StatusOK, secondResp.StatusCode)
	require.NotNil(secondResp.JSON200)
	require.Equal(int64(0), secondResp.JSON200.Issue.CommentCount)
	require.Equal(now.Add(time.Minute).UTC(), secondResp.JSON200.Issue.LastActivityAt.UTC())
	require.NotNil(secondResp.JSON200.Events)
	require.Empty(secondResp.JSON200.Events)
}

func TestE2EIssueDetailPreservesHiddenCommentsAcrossIncompleteGraphQLRefresh(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	ctx := t.Context()

	now := time.Date(2026, 4, 13, 11, 30, 0, 0, time.UTC)
	issueID := int64(171150)
	issueNumber := 176
	issueTitle := "Moderated comments issue"
	issueState := "open"
	issueURL := "https://github.com/acme/widget/issues/176"
	issueAuthor := "heidi"
	issueTime := gh.Timestamp{Time: now}
	firstCommentID := int64(9131)
	secondCommentID := int64(9132)
	firstCommentBody := "visible after moderation review"
	secondCommentBody := "outside the partial GraphQL page"
	commentAuthor := "commenter"
	firstCommentTime := gh.Timestamp{Time: now.Add(time.Minute)}
	secondCommentTime := gh.Timestamp{Time: now.Add(2 * time.Minute)}
	partialComments := false

	commentsJSON := func() string {
		firstVisibility := `"isMinimized":true,"minimizedReason":"OFF_TOPIC"`
		secondNode := `,{
			"databaseId":9132,
			"author":{"login":"commenter"},
			"body":"outside the partial GraphQL page",
			"url":"https://github.com/acme/widget/issues/176#issuecomment-9132",
			"createdAt":"` + secondCommentTime.Format(time.RFC3339) + `",
			"updatedAt":"` + secondCommentTime.Format(time.RFC3339) + `",
			"isMinimized":true,
			"minimizedReason":"OFF_TOPIC"
		}`
		hasNextPage := "false"
		if partialComments {
			firstVisibility = `"isMinimized":false,"minimizedReason":null`
			secondNode = ""
			hasNextPage = "true"
		}
		return `{
			"totalCount":2,
			"nodes":[{
				"databaseId":9131,
				"author":{"login":"commenter"},
				"body":"visible after moderation review",
				"url":"https://github.com/acme/widget/issues/176#issuecomment-9131",
				"createdAt":"` + firstCommentTime.Format(time.RFC3339) + `",
				"updatedAt":"` + firstCommentTime.Format(time.RFC3339) + `",
				` + firstVisibility + `
			}` + secondNode + `],
			"pageInfo":{"hasNextPage":` + hasNextPage + `,"endCursor":"cursor"}
		}`
	}

	gqlSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if bytes.Contains(body, []byte("issue(number:")) {
			_, _ = w.Write([]byte(`{"data":{"repository":{"issue":{"comments":{"nodes":[{
				"databaseId":9132,
				"isMinimized":true,
				"minimizedReason":"OFF_TOPIC"
			}],"pageInfo":{"hasNextPage":false,"endCursor":null}}}}}}`))
			return
		}
		if bytes.Contains(body, []byte("pullRequests")) {
			_, _ = w.Write([]byte(`{"data":{"repository":{"pullRequests":{"nodes":[],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}`))
			return
		}
		resp := `{"data":{"repository":{"issues":{"nodes":[{
			"databaseId":171150,
			"number":176,
			"title":"Moderated comments issue",
			"state":"OPEN",
			"body":"GraphQL moderation state",
			"url":"https://github.com/acme/widget/issues/176",
			"author":{"login":"heidi"},
			"createdAt":"` + now.Format(time.RFC3339) + `",
			"updatedAt":"` + now.Format(time.RFC3339) + `",
			"closedAt":null,
			"labels":{"nodes":[]},
			"comments":` + commentsJSON() + `,
			"timelineItems":{"nodes":[],"pageInfo":{"hasNextPage":false,"endCursor":""}}
		}],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}`
		_, _ = w.Write([]byte(resp))
	}))
	defer gqlSrv.Close()

	restComments := []*gh.IssueComment{
		{
			ID:        &firstCommentID,
			Body:      &firstCommentBody,
			User:      &gh.User{Login: &commentAuthor},
			CreatedAt: &firstCommentTime,
			UpdatedAt: &firstCommentTime,
		},
		{
			ID:        &secondCommentID,
			Body:      &secondCommentBody,
			User:      &gh.User{Login: &commentAuthor},
			CreatedAt: &secondCommentTime,
			UpdatedAt: &secondCommentTime,
		},
	}
	var restCommentCalls atomic.Int32
	mock := &mockGH{
		listOpenPullRequestsFn: func(_ context.Context, _, _ string) ([]*gh.PullRequest, error) {
			return nil, &gh.ErrorResponse{
				Response: &http.Response{StatusCode: http.StatusNotModified},
			}
		},
		listOpenIssuesFn: func(_ context.Context, _, _ string) ([]*gh.Issue, error) {
			return []*gh.Issue{{
				ID:        &issueID,
				Number:    &issueNumber,
				Title:     &issueTitle,
				State:     &issueState,
				HTMLURL:   &issueURL,
				User:      &gh.User{Login: &issueAuthor},
				CreatedAt: &issueTime,
				UpdatedAt: &issueTime,
			}}, nil
		},
		listIssueCommentsFn: func(_ context.Context, _, _ string, number int) ([]*gh.IssueComment, error) {
			require.Equal(issueNumber, number)
			restCommentCalls.Add(1)
			return restComments, nil
		},
	}

	database := dbtest.Open(t)
	syncer := ghclient.NewSyncer(
		map[string]ghclient.Client{"github.com": mock},
		database,
		nil,
		defaultTestRepos,
		time.Minute,
		nil,
		map[string]*ghclient.SyncBudget{"github.com": ghclient.NewSyncBudget(10000)},
	)
	t.Cleanup(syncer.Stop)
	syncer.SetFetchers(map[string]*ghclient.GraphQLFetcher{
		"github.com": ghclient.NewGraphQLFetcherWithClient(
			githubv4.NewEnterpriseClient(gqlSrv.URL, gqlSrv.Client()),
			nil,
		),
	})

	srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{})
	t.Cleanup(func() { gracefulShutdown(t, srv) })
	client := setupTestClient(t, srv)

	syncer.RunOnce(ctx)
	assert.Zero(restCommentCalls.Load())

	firstResp, err := client.HTTP.GetIssueWithResponse(ctx, &generated.GetIssueRequestOptions{PathParams: &generated.GetIssuePath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(int64(issueNumber))}})
	require.NoError(err)
	require.Equal(http.StatusOK, firstResp.StatusCode)
	require.NotNil(firstResp.JSON200)
	require.NotNil(firstResp.JSON200.Events)
	require.Len(firstResp.JSON200.Events, 2)
	for _, event := range firstResp.JSON200.Events {
		assert.Contains(event.MetadataJSON, `"provider_hidden":true`)
		assert.Contains(event.MetadataJSON, `"provider_hidden_reason":"OFF_TOPIC"`)
	}

	partialComments = true
	syncer.RunOnce(ctx)
	assert.Equal(int32(1), restCommentCalls.Load())

	secondResp, err := client.HTTP.GetIssueWithResponse(ctx, &generated.GetIssueRequestOptions{PathParams: &generated.GetIssuePath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(int64(issueNumber))}})
	require.NoError(err)
	require.Equal(http.StatusOK, secondResp.StatusCode)
	require.NotNil(secondResp.JSON200)
	require.NotNil(secondResp.JSON200.Events)
	require.Len(secondResp.JSON200.Events, 2)

	metadataByCommentID := make(map[int64]string, len(secondResp.JSON200.Events))
	for _, event := range secondResp.JSON200.Events {
		require.NotNil(event.PlatformID)
		metadataByCommentID[*event.PlatformID] = event.MetadataJSON
	}
	assert.NotContains(metadataByCommentID[firstCommentID], `"provider_hidden":true`)
	assert.Contains(metadataByCommentID[secondCommentID], `"provider_hidden":true`)
	assert.Contains(metadataByCommentID[secondCommentID], `"provider_hidden_reason":"OFF_TOPIC"`)
}

func TestE2EGraphQLBulkSyncPersistsIssueTimelineEvents(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	ctx := t.Context()

	now := time.Date(2026, 4, 13, 12, 0, 0, 0, time.UTC)
	nowRFC3339 := now.Format(time.RFC3339)

	gqlSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if bytes.Contains(body, []byte("pullRequests")) {
			_, _ = w.Write([]byte(`{"data":{"repository":{"pullRequests":{"nodes":[],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}`))
			return
		}
		resp := `{"data":{"repository":{"issues":{"nodes":[{
			"databaseId":171200,
			"number":173,
			"title":"Bulk timeline issue",
			"state":"OPEN",
			"body":"GraphQL bulk issue timeline",
			"url":"https://github.com/acme/widget/issues/173",
			"author":{"login":"heidi"},
			"createdAt":"` + nowRFC3339 + `",
			"updatedAt":"` + nowRFC3339 + `",
			"closedAt":null,
			"labels":{"nodes":[]},
			"comments":{"totalCount":0,"nodes":[],"pageInfo":{"hasNextPage":false,"endCursor":""}},
			"timelineItems":{"nodes":[{
				"__typename":"CrossReferencedEvent",
				"id":"CRE_issue_173",
				"actor":{"login":"reviewer"},
				"createdAt":"` + now.Add(time.Minute).Format(time.RFC3339) + `",
				"isCrossRepository":false,
				"willCloseTarget":true,
				"source":{
					"__typename":"PullRequest",
					"number":174,
					"title":"Fix bulk timeline issue",
					"url":"https://github.com/acme/widget/pull/174",
					"repository":{"owner":{"login":"acme"},"name":"widget"}
				}
			},{
				"__typename":"ClosedEvent",
				"id":"CE_issue_173",
				"actor":{"login":"closer"},
				"createdAt":"` + now.Add(2*time.Minute).Format(time.RFC3339) + `"
			},{
				"__typename":"ReopenedEvent",
				"id":"RE_issue_173",
				"actor":{"login":"opener"},
				"createdAt":"` + now.Add(3*time.Minute).Format(time.RFC3339) + `"
			}],"pageInfo":{"hasNextPage":false,"endCursor":""}}
		}],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}`
		_, _ = w.Write([]byte(resp))
	}))
	defer gqlSrv.Close()

	issueID := int64(171200)
	issueNumber := 173
	issueTitle := "Bulk timeline issue"
	issueState := "open"
	issueURL := "https://github.com/acme/widget/issues/173"
	issueAuthor := "heidi"
	issueTime := gh.Timestamp{Time: now}
	mock := &mockGH{
		listOpenPullRequestsFn: func(_ context.Context, _, _ string) ([]*gh.PullRequest, error) {
			return nil, &gh.ErrorResponse{
				Response: &http.Response{StatusCode: http.StatusNotModified},
			}
		},
		listOpenIssuesFn: func(_ context.Context, _, _ string) ([]*gh.Issue, error) {
			return []*gh.Issue{{
				ID:        &issueID,
				Number:    &issueNumber,
				Title:     &issueTitle,
				State:     &issueState,
				HTMLURL:   &issueURL,
				User:      &gh.User{Login: &issueAuthor},
				CreatedAt: &issueTime,
				UpdatedAt: &issueTime,
			}}, nil
		},
	}

	database := dbtest.Open(t)

	syncer := ghclient.NewSyncer(
		map[string]ghclient.Client{"github.com": mock},
		database,
		nil,
		defaultTestRepos,
		time.Minute,
		nil,
		map[string]*ghclient.SyncBudget{"github.com": ghclient.NewSyncBudget(10000)},
	)
	t.Cleanup(syncer.Stop)

	gqlClient := githubv4.NewEnterpriseClient(gqlSrv.URL, gqlSrv.Client())
	syncer.SetFetchers(map[string]*ghclient.GraphQLFetcher{
		"github.com": ghclient.NewGraphQLFetcherWithClient(gqlClient, nil),
	})

	srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{})
	t.Cleanup(func() { gracefulShutdown(t, srv) })
	client := setupTestClient(t, srv)

	syncer.RunOnce(ctx)

	resp, err := client.HTTP.GetIssueWithResponse(ctx, &generated.GetIssueRequestOptions{PathParams: &generated.GetIssuePath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(int64(issueNumber))}})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)
	require.NotNil(resp.JSON200)
	require.True(resp.JSON200.DetailLoaded)
	require.NotNil(resp.JSON200.Events)
	require.Len(resp.JSON200.Events, 3)

	byType := make(map[string]generated.IssueEvent, len(resp.JSON200.Events))
	for _, event := range resp.JSON200.Events {
		byType[event.EventType] = event
	}

	crossReferenced, ok := byType["cross_referenced"]
	require.True(ok)
	assert.Equal("reviewer", crossReferenced.Author)
	assert.Equal("Referenced from acme/widget#174", crossReferenced.Summary)
	assert.Contains(crossReferenced.MetadataJSON, `"source_title":"Fix bulk timeline issue"`)
	assert.Equal("timeline-CRE_issue_173", crossReferenced.DedupeKey)

	closed, ok := byType["closed"]
	require.True(ok)
	assert.Equal("closer", closed.Author)
	assert.Equal("closed this", closed.Summary)
	assert.Equal("timeline-CE_issue_173", closed.DedupeKey)

	reopened, ok := byType["reopened"]
	require.True(ok)
	assert.Equal("opener", reopened.Author)
	assert.Equal("reopened this", reopened.Summary)
	assert.Equal("timeline-RE_issue_173", reopened.DedupeKey)
}

func TestE2EGraphQLBulkSyncPersistsIssueLifecycleTimelineAfterReopen(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	ctx := t.Context()

	now := time.Now().UTC().Truncate(time.Second)
	const issueID int64 = 171300
	const issueNumber = 175
	const issueTitle = "Reopened timeline issue"
	const issueURL = "https://github.com/acme/widget/issues/175"
	const issueAuthor = "heidi"
	phase := "open"

	issueNode := func(updatedAt time.Time, timelineItems string) string {
		return `{
			"databaseId":171300,
			"number":175,
			"title":"Reopened timeline issue",
			"state":"OPEN",
			"body":"GraphQL reopened issue timeline",
			"url":"https://github.com/acme/widget/issues/175",
			"author":{"login":"heidi"},
			"createdAt":"` + now.Format(time.RFC3339) + `",
			"updatedAt":"` + updatedAt.Format(time.RFC3339) + `",
			"closedAt":null,
			"labels":{"nodes":[]},
			"comments":{"totalCount":0,"nodes":[],"pageInfo":{"hasNextPage":false,"endCursor":""}},
			"timelineItems":{"nodes":[` + timelineItems + `],"pageInfo":{"hasNextPage":false,"endCursor":""}}
		}`
	}

	gqlSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if bytes.Contains(body, []byte("pullRequests")) {
			_, _ = w.Write([]byte(`{"data":{"repository":{"pullRequests":{"nodes":[],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}`))
			return
		}

		nodes := "[]"
		switch phase {
		case "open":
			nodes = "[" + issueNode(now, "") + "]"
		case "closed":
			nodes = "[]"
		case "reopened":
			timelineItems := `{
				"__typename":"ClosedEvent",
				"id":"CE_issue_175",
				"actor":{"login":"closer"},
				"createdAt":"` + now.Add(time.Minute).Format(time.RFC3339) + `"
			},{
				"__typename":"ReopenedEvent",
				"id":"RE_issue_175",
				"actor":{"login":"opener"},
				"createdAt":"` + now.Add(2*time.Minute).Format(time.RFC3339) + `"
			}`
			nodes = "[" + issueNode(now.Add(3*time.Minute), timelineItems) + "]"
		}

		resp := `{"data":{"repository":{"issues":{"nodes":` + nodes + `,"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}`
		_, _ = w.Write([]byte(resp))
	}))
	defer gqlSrv.Close()

	mock := &mockGH{
		listOpenPullRequestsFn: func(_ context.Context, _, _ string) ([]*gh.PullRequest, error) {
			return nil, &gh.ErrorResponse{
				Response: &http.Response{StatusCode: http.StatusNotModified},
			}
		},
		listOpenIssuesFn: func(_ context.Context, _, _ string) ([]*gh.Issue, error) {
			if phase == "closed" {
				return nil, nil
			}
			state := "open"
			updatedAt := now
			if phase == "reopened" {
				updatedAt = now.Add(3 * time.Minute)
			}
			return []*gh.Issue{{
				ID:        new(issueID),
				Number:    new(issueNumber),
				Title:     new(issueTitle),
				State:     &state,
				HTMLURL:   new(issueURL),
				User:      &gh.User{Login: new(issueAuthor)},
				CreatedAt: &gh.Timestamp{Time: now},
				UpdatedAt: &gh.Timestamp{Time: updatedAt},
			}}, nil
		},
		getIssueFn: func(_ context.Context, _, _ string, number int) (*gh.Issue, error) {
			require.Equal(issueNumber, number)
			state := "closed"
			closedAt := gh.Timestamp{Time: now.Add(time.Minute)}
			return &gh.Issue{
				ID:        new(issueID),
				Number:    new(issueNumber),
				Title:     new(issueTitle),
				State:     &state,
				HTMLURL:   new(issueURL),
				User:      &gh.User{Login: new(issueAuthor)},
				CreatedAt: &gh.Timestamp{Time: now},
				UpdatedAt: &gh.Timestamp{Time: now.Add(time.Minute)},
				ClosedAt:  &closedAt,
			}, nil
		},
	}

	database := dbtest.Open(t)

	syncer := ghclient.NewSyncer(
		map[string]ghclient.Client{"github.com": mock},
		database,
		nil,
		defaultTestRepos,
		time.Minute,
		nil,
		map[string]*ghclient.SyncBudget{"github.com": ghclient.NewSyncBudget(10000)},
	)
	t.Cleanup(syncer.Stop)

	gqlClient := githubv4.NewEnterpriseClient(gqlSrv.URL, gqlSrv.Client())
	syncer.SetFetchers(map[string]*ghclient.GraphQLFetcher{
		"github.com": ghclient.NewGraphQLFetcherWithClient(gqlClient, nil),
	})

	srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{})
	t.Cleanup(func() { gracefulShutdown(t, srv) })
	client := setupTestClient(t, srv)

	syncer.RunOnce(ctx)

	phase = "closed"
	syncer.RunOnce(ctx)

	closedResp, err := client.HTTP.GetIssueWithResponse(ctx, &generated.GetIssueRequestOptions{PathParams: &generated.GetIssuePath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(int64(issueNumber))}})
	require.NoError(err)
	require.Equal(http.StatusOK, closedResp.StatusCode)
	require.NotNil(closedResp.JSON200)
	assert.Equal("closed", closedResp.JSON200.Issue.State)
	require.NotNil(closedResp.JSON200.Events)
	assert.Empty(closedResp.JSON200.Events)

	phase = "reopened"
	syncer.RunOnce(ctx)

	reopenedResp, err := client.HTTP.GetIssueWithResponse(ctx, &generated.GetIssueRequestOptions{PathParams: &generated.GetIssuePath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(int64(issueNumber))}})
	require.NoError(err)
	require.Equal(http.StatusOK, reopenedResp.StatusCode)
	require.NotNil(reopenedResp.JSON200)
	assert.Equal("open", reopenedResp.JSON200.Issue.State)
	require.NotNil(reopenedResp.JSON200.Events)
	require.Len(reopenedResp.JSON200.Events, 2)

	byType := make(map[string]generated.IssueEvent, len(reopenedResp.JSON200.Events))
	for _, event := range reopenedResp.JSON200.Events {
		byType[event.EventType] = event
	}

	closed, ok := byType["closed"]
	require.True(ok)
	assert.Equal("closer", closed.Author)
	assert.Equal("closed this", closed.Summary)
	assert.Equal("timeline-CE_issue_175", closed.DedupeKey)

	reopened, ok := byType["reopened"]
	require.True(ok)
	assert.Equal("opener", reopened.Author)
	assert.Equal("reopened this", reopened.Summary)
	assert.Equal("timeline-RE_issue_175", reopened.DedupeKey)
}

func cleanupServerTestTmux(owner *testtmux.Owner) error {
	if owner == nil {
		return nil
	}
	return owner.Cleanup()
}
