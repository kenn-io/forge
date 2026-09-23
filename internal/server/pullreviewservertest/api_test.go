package pullreviewservertest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
	"go.kenn.io/forge/internal/server/authapi"
	"go.kenn.io/forge/internal/server/itemapi"
	"go.kenn.io/forge/internal/server/operationapi"
	"go.kenn.io/forge/internal/server/pullapi"
	"go.kenn.io/forge/internal/stacks"
	"go.kenn.io/forge/internal/testutil"
	"go.kenn.io/forge/internal/testutil/dbtest"
	"go.kenn.io/forge/internal/testutil/gitsafe"
	"go.kenn.io/forge/internal/testutil/processjob"
	"go.kenn.io/forge/internal/testutil/testsignal"
	"go.kenn.io/forge/internal/testutil/testtmux"
	"go.kenn.io/forge/platform"
	platformgithub "go.kenn.io/forge/platform/github"
	platformgitlab "go.kenn.io/forge/platform/gitlab"
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

func TestAPIGitHubSyncPersistsReviewThreadsThroughPullDetail(t *testing.T) {
	runParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	now := time.Date(2026, 5, 27, 16, 1, 31, 0, time.UTC)
	reviewUpdatedAt := now.Add(time.Minute)
	providerUpdatedAt := now.Add(2 * time.Minute)
	prNumber := 42
	line := 1
	headSHA := "head-sha"
	commentCommitSHA := "comment-sha"
	baseSHA := "base-sha"
	mock := &mockGH{
		getPullRequestFn: func(_ context.Context, _, _ string, number int) (*gh.PullRequest, error) {
			require.Equal(prNumber, number)
			prID := int64(9001)
			prNodeID := "PR_kwDO123"
			title := "inline review"
			state := "open"
			url := "https://github.com/acme/widget/pull/42"
			author := "ada"
			headRef := "feature"
			baseRef := "main"
			return &gh.PullRequest{
				ID:        &prID,
				NodeID:    &prNodeID,
				Number:    &number,
				HTMLURL:   &url,
				Title:     &title,
				State:     &state,
				User:      &gh.User{Login: &author},
				CreatedAt: &gh.Timestamp{Time: now},
				UpdatedAt: &gh.Timestamp{Time: providerUpdatedAt},
				Head: &gh.PullRequestBranch{
					Ref: &headRef,
					SHA: &headSHA,
				},
				Base: &gh.PullRequestBranch{
					Ref: &baseRef,
					SHA: &baseSHA,
				},
			}, nil
		},
		listIssueCommentsFn: func(context.Context, string, string, int) ([]*gh.IssueComment, error) {
			return nil, nil
		},
		listReviewThreadsFn: func(context.Context, string, string, int) ([]platformgithub.PullRequestReviewThread, error) {
			return []platformgithub.PullRequestReviewThread{{
				NodeID:     "PRRT_1",
				IsOutdated: false,
				Path:       ".golangci.yml",
				Side:       "RIGHT",
				Line:       line,
				Comments: []platformgithub.PullRequestReviewThreadComment{{
					NodeID:           "PRRC_1",
					DatabaseID:       3312100450,
					ReviewDatabaseID: 4373946198,
					Body:             "inline note",
					AuthorLogin:      "reviewer",
					CommitID:         commentCommitSHA,
					IsMinimized:      true,
					MinimizedReason:  "OFF_TOPIC",
					CreatedAt:        now,
					UpdatedAt:        reviewUpdatedAt,
				}},
			}}, nil
		},
	}
	srv, _, syncer := setupTestServerWithMock(t, mock)
	client := setupTestClient(t, srv)

	require.NoError(syncer.SyncMR(ctx, "acme", "widget", prNumber))

	resp, err := client.HTTP.GetPullWithResponse(ctx, &generated.GetPullRequestOptions{PathParams: &generated.GetPullPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(int64(prNumber))}})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)
	require.NotNil(resp.JSON200)
	require.NotNil(resp.JSON200.Events)
	assert.Equal(providerUpdatedAt, resp.JSON200.MergeRequest.LastActivityAt)
	require.Len(resp.JSON200.Events, 1)
	event := resp.JSON200.Events[0]
	assert.Equal("review_comment", event.EventType)
	assert.JSONEq(`{"provider_hidden":true,"provider_hidden_reason":"OFF_TOPIC"}`, event.MetadataJSON)
	assert.Equal("3312100450", event.PlatformExternalID)
	require.NotNil(event.ThreadID)
	assert.Equal("PRRT_1", *event.ThreadID)
	require.NotNil(event.DiffThread)
	assert.Equal(".golangci.yml", event.DiffThread.Path)
	assert.Equal("right", event.DiffThread.Side)
	assert.Equal(int64(line), event.DiffThread.Line)
	assert.Equal("inline note", event.DiffThread.Body)
	require.NotNil(event.DiffThread.MetadataJSON)
	assert.JSONEq(`{"provider_hidden":true,"provider_hidden_reason":"OFF_TOPIC"}`, *event.DiffThread.MetadataJSON)
	require.NotNil(event.DiffThread.DiffHeadSha)
	assert.Equal(commentCommitSHA, *event.DiffThread.DiffHeadSha)
	require.NotNil(event.DiffThread.CommitSha)
	assert.Equal(commentCommitSHA, *event.DiffThread.CommitSha)
	require.NotNil(event.DiffThread.ProviderCommentID)
	assert.Equal("3312100450", *event.DiffThread.ProviderCommentID)
}

func TestAPICommentAutocomplete(t *testing.T) {
	runParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	srv, database, _ := setupTestServer(t)
	ctx := t.Context()

	repoID, err := database.UpsertRepo(ctx, verifiedGitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	prID, err := database.UpsertMergeRequest(ctx, &db.MergeRequest{
		RepoID:         repoID,
		PlatformID:     12000,
		Number:         12,
		URL:            "https://github.com/acme/widget/pull/12",
		Title:          "Polish mentions",
		Author:         "alice",
		State:          "open",
		HeadBranch:     "feature-12",
		BaseBranch:     "main",
		CreatedAt:      time.Now().UTC().Add(-3 * time.Hour).Truncate(time.Second),
		UpdatedAt:      time.Now().UTC().Add(-3 * time.Hour).Truncate(time.Second),
		LastActivityAt: time.Now().UTC().Add(-3 * time.Hour).Truncate(time.Second),
	})
	require.NoError(err)
	require.NoError(database.EnsureKanbanState(ctx, prID))
	_, err = database.UpsertIssue(ctx, &db.Issue{
		RepoID:         repoID,
		PlatformID:     17000,
		Number:         17,
		URL:            "https://github.com/acme/widget/issues/17",
		Title:          "Mention bug",
		Author:         "alex",
		State:          "open",
		CreatedAt:      time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Second),
		UpdatedAt:      time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Second),
		LastActivityAt: time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Second),
	})
	require.NoError(err)
	require.NoError(database.UpsertMREvents(ctx, []db.MREvent{{
		MergeRequestID: prID,
		EventType:      "comment",
		Author:         "albert",
		CreatedAt:      time.Now().UTC().Add(-time.Hour).Truncate(time.Second),
		DedupeKey:      "autocomplete-mr-comment",
	}}))

	userReq := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/repo/gh/acme/widget/comment-autocomplete?trigger=@&q=al&limit=10", nil)
	userRR := httptest.NewRecorder()
	srv.ServeHTTP(userRR, userReq)
	require.Equal(http.StatusOK, userRR.Code, userRR.Body.String())

	var userBody itemapi.CommentAutocompleteResponse
	require.NoError(json.NewDecoder(userRR.Body).Decode(&userBody))
	assert.Equal([]string{"albert", "alex", "alice"}, userBody.Users)
	assert.Empty(userBody.References)

	// Naming the target item promotes its author and participants ahead of
	// the recency ordering.
	itemReq := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/repo/gh/acme/widget/comment-autocomplete?trigger=@&q=al&limit=10&item_type=issue&item_number=17", nil)
	itemRR := httptest.NewRecorder()
	srv.ServeHTTP(itemRR, itemReq)
	require.Equal(http.StatusOK, itemRR.Code, itemRR.Body.String())
	var itemBody itemapi.CommentAutocompleteResponse
	require.NoError(json.NewDecoder(itemRR.Body).Decode(&itemBody))
	assert.Equal([]string{"alex", "albert", "alice"}, itemBody.Users)

	halfReq := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/repo/gh/acme/widget/comment-autocomplete?trigger=@&q=al&item_number=17", nil)
	halfRR := httptest.NewRecorder()
	srv.ServeHTTP(halfRR, halfReq)
	assert.Equal(http.StatusBadRequest, halfRR.Code, halfRR.Body.String())

	refReq := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/repo/gh/acme/widget/comment-autocomplete?trigger=%23&q=1&limit=10", nil)
	refRR := httptest.NewRecorder()
	srv.ServeHTTP(refRR, refReq)
	require.Equal(http.StatusOK, refRR.Code, refRR.Body.String())

	var refBody itemapi.CommentAutocompleteResponse
	require.NoError(json.NewDecoder(refRR.Body).Decode(&refBody))
	assert.Equal([]db.CommentAutocompleteReference{
		{Kind: "issue", Number: 17, Title: "Mention bug", State: "open"},
		{Kind: "pull", Number: 12, Title: "Polish mentions", State: "open"},
	}, refBody.References)
	assert.Empty(refBody.Users)

	bangReq := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/repo/gh/acme/widget/comment-autocomplete?trigger=!&q=1&limit=10", nil)
	bangRR := httptest.NewRecorder()
	srv.ServeHTTP(bangRR, bangReq)
	assert.Equal(http.StatusBadRequest, bangRR.Code, bangRR.Body.String())

	gitlabRepoID, err := database.UpsertRepo(ctx, db.RepoIdentity{
		Platform:       "gitlab",
		PlatformHost:   "gitlab.example.com",
		PlatformRepoID: "gid://gitlab/Project/42",
		Owner:          "group",
		Name:           "project",
	})
	require.NoError(err)
	_, err = database.UpsertMergeRequest(ctx, &db.MergeRequest{
		RepoID:         gitlabRepoID,
		PlatformID:     12001,
		Number:         12,
		URL:            "https://gitlab.example.com/group/project/-/merge_requests/12",
		Title:          "Polish merge request mentions",
		Author:         "alice",
		State:          "open",
		HeadBranch:     "feature-12",
		BaseBranch:     "main",
		CreatedAt:      time.Now().UTC().Add(-3 * time.Hour).Truncate(time.Second),
		UpdatedAt:      time.Now().UTC().Add(-3 * time.Hour).Truncate(time.Second),
		LastActivityAt: time.Now().UTC().Add(-3 * time.Hour).Truncate(time.Second),
	})
	require.NoError(err)
	_, err = database.UpsertIssue(ctx, &db.Issue{
		RepoID:         gitlabRepoID,
		PlatformID:     17001,
		Number:         17,
		URL:            "https://gitlab.example.com/group/project/-/issues/17",
		Title:          "Mention issue",
		Author:         "alex",
		State:          "open",
		CreatedAt:      time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Second),
		UpdatedAt:      time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Second),
		LastActivityAt: time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Second),
	})
	require.NoError(err)

	gitlabIssueReq := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/host/gitlab.example.com/repo/gitlab/group/project/comment-autocomplete?trigger=%23&q=1&limit=10", nil)
	gitlabIssueRR := httptest.NewRecorder()
	srv.ServeHTTP(gitlabIssueRR, gitlabIssueReq)
	require.Equal(http.StatusOK, gitlabIssueRR.Code, gitlabIssueRR.Body.String())

	var gitlabIssueBody itemapi.CommentAutocompleteResponse
	require.NoError(json.NewDecoder(gitlabIssueRR.Body).Decode(&gitlabIssueBody))
	assert.Equal([]db.CommentAutocompleteReference{
		{Kind: "issue", Number: 17, Title: "Mention issue", State: "open"},
	}, gitlabIssueBody.References)

	gitlabMRReq := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/host/gitlab.example.com/repo/gitlab/group/project/comment-autocomplete?trigger=!&q=1&limit=10", nil)
	gitlabMRRR := httptest.NewRecorder()
	srv.ServeHTTP(gitlabMRRR, gitlabMRReq)
	require.Equal(http.StatusOK, gitlabMRRR.Code, gitlabMRRR.Body.String())

	var gitlabMRBody itemapi.CommentAutocompleteResponse
	require.NoError(json.NewDecoder(gitlabMRRR.Body).Decode(&gitlabMRBody))
	assert.Equal([]db.CommentAutocompleteReference{
		{Kind: "pull", Number: 12, Title: "Polish merge request mentions", State: "open"},
	}, gitlabMRBody.References)
}

func TestAPICommentAutocompleteUsesRepoPlatformHost(t *testing.T) {
	runParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	srv, database, _ := setupTestServer(t)
	ctx := t.Context()

	githubRepoID, err := database.UpsertRepo(ctx, verifiedGitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	_, err = database.UpsertMergeRequest(ctx, &db.MergeRequest{
		RepoID:         githubRepoID,
		PlatformID:     12001,
		Number:         12,
		URL:            "https://github.com/acme/widget/pull/12",
		Title:          "Wrong host mention",
		Author:         "alice",
		State:          "open",
		HeadBranch:     "feature-12",
		BaseBranch:     "main",
		CreatedAt:      time.Now().UTC().Add(-4 * time.Hour).Truncate(time.Second),
		UpdatedAt:      time.Now().UTC().Add(-4 * time.Hour).Truncate(time.Second),
		LastActivityAt: time.Now().UTC().Add(-4 * time.Hour).Truncate(time.Second),
	})
	require.NoError(err)

	repoID, err := database.UpsertRepo(ctx, verifiedGitHubRepoIdentity("ghe.example.com", "acme", "widget"))
	require.NoError(err)
	_, err = database.UpsertMergeRequest(ctx, &db.MergeRequest{
		RepoID:         repoID,
		PlatformID:     12000,
		Number:         12,
		URL:            "https://ghe.example.com/acme/widget/pull/12",
		Title:          "Polish mentions",
		Author:         "alice",
		State:          "open",
		HeadBranch:     "feature-12",
		BaseBranch:     "main",
		CreatedAt:      time.Now().UTC().Add(-3 * time.Hour).Truncate(time.Second),
		UpdatedAt:      time.Now().UTC().Add(-3 * time.Hour).Truncate(time.Second),
		LastActivityAt: time.Now().UTC().Add(-3 * time.Hour).Truncate(time.Second),
	})
	require.NoError(err)

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/host/ghe.example.com/repo/gh/acme/widget/comment-autocomplete?trigger=%23&q=1&limit=10", nil)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	require.Equal(http.StatusOK, rr.Code, rr.Body.String())

	var body itemapi.CommentAutocompleteResponse
	require.NoError(json.NewDecoder(rr.Body).Decode(&body))
	assert.Equal([]db.CommentAutocompleteReference{{Kind: "pull", Number: 12, Title: "Polish mentions", State: "open"}}, body.References)
}

func TestAPICommentAutocompleteReferencesScopesByProvider(t *testing.T) {
	runParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	srv, database, _ := setupTestServer(t)
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Second)

	githubRepoID, err := database.UpsertRepo(ctx, db.RepoIdentity{
		Platform:       "github",
		PlatformHost:   "github.com",
		PlatformRepoID: "repo-github-widget",
		Owner:          "acme",
		Name:           "widget",
		RepoPath:       "acme/widget",
	})
	require.NoError(err)
	giteaRepoID, err := database.UpsertRepo(ctx, db.RepoIdentity{
		Platform:       "gitea",
		PlatformHost:   "github.com",
		PlatformRepoID: "repo-gitea-widget",
		Owner:          "acme",
		Name:           "widget",
		RepoPath:       "acme/widget",
	})
	require.NoError(err)

	_, err = database.UpsertIssue(ctx, &db.Issue{
		RepoID:         githubRepoID,
		PlatformID:     17001,
		Number:         1,
		URL:            "https://github.com/acme/widget/issues/1",
		Title:          "Provider collision issue",
		Author:         "alice",
		State:          "open",
		CreatedAt:      now.Add(-2 * time.Hour),
		UpdatedAt:      now.Add(-2 * time.Hour),
		LastActivityAt: now.Add(-2 * time.Hour),
	})
	require.NoError(err)
	_, err = database.UpsertIssue(ctx, &db.Issue{
		RepoID:         giteaRepoID,
		PlatformID:     17901,
		Number:         901,
		URL:            "https://github.com/acme/widget/issues/901",
		Title:          "Provider collision issue",
		Author:         "gina",
		State:          "open",
		CreatedAt:      now.Add(-time.Hour),
		UpdatedAt:      now.Add(-time.Hour),
		LastActivityAt: now.Add(-time.Hour),
	})
	require.NoError(err)

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/host/github.com/repo/gitea/acme/widget/comment-autocomplete?trigger=%23&q=collision&limit=10", nil)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	require.Equal(http.StatusOK, rr.Code, rr.Body.String())

	var body itemapi.CommentAutocompleteResponse
	require.NoError(json.NewDecoder(rr.Body).Decode(&body))
	assert.Equal([]db.CommentAutocompleteReference{
		{Kind: "issue", Number: 901, Title: "Provider collision issue", State: "open"},
	}, body.References)
	assert.Empty(body.Users)
}

func TestAPICommentAutocompleteGitLabMergeRequestReferencesScopesByProvider(t *testing.T) {
	runParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	srv, database, _ := setupTestServer(t)
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Second)

	giteaRepoID, err := database.UpsertRepo(ctx, db.RepoIdentity{
		Platform:       "gitea",
		PlatformHost:   "gitlab.example.com",
		PlatformRepoID: "repo-gitea-widget",
		Owner:          "acme",
		Name:           "widget",
		RepoPath:       "acme/widget",
	})
	require.NoError(err)
	gitlabRepoID, err := database.UpsertRepo(ctx, db.RepoIdentity{
		Platform:       "gitlab",
		PlatformHost:   "gitlab.example.com",
		PlatformRepoID: "repo-gitlab-widget",
		Owner:          "acme",
		Name:           "widget",
		RepoPath:       "acme/widget",
	})
	require.NoError(err)

	_, err = database.UpsertMergeRequest(ctx, &db.MergeRequest{
		RepoID:         giteaRepoID,
		PlatformID:     12001,
		Number:         1,
		URL:            "https://gitlab.example.com/acme/widget/pulls/1",
		Title:          "Provider collision merge request",
		Author:         "gina",
		State:          "open",
		HeadBranch:     "feature-gitea",
		BaseBranch:     "main",
		CreatedAt:      now.Add(-2 * time.Hour),
		UpdatedAt:      now.Add(-2 * time.Hour),
		LastActivityAt: now.Add(-2 * time.Hour),
	})
	require.NoError(err)
	_, err = database.UpsertMergeRequest(ctx, &db.MergeRequest{
		RepoID:         gitlabRepoID,
		PlatformID:     12901,
		Number:         901,
		URL:            "https://gitlab.example.com/acme/widget/-/merge_requests/901",
		Title:          "Provider collision merge request",
		Author:         "glenda",
		State:          "open",
		HeadBranch:     "feature-gitlab",
		BaseBranch:     "main",
		CreatedAt:      now.Add(-time.Hour),
		UpdatedAt:      now.Add(-time.Hour),
		LastActivityAt: now.Add(-time.Hour),
	})
	require.NoError(err)

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/host/gitlab.example.com/repo/gitlab/acme/widget/comment-autocomplete?trigger=!&q=collision&limit=10", nil)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	require.Equal(http.StatusOK, rr.Code, rr.Body.String())

	var body itemapi.CommentAutocompleteResponse
	require.NoError(json.NewDecoder(rr.Body).Decode(&body))
	assert.Equal([]db.CommentAutocompleteReference{
		{Kind: "pull", Number: 901, Title: "Provider collision merge request", State: "open"},
	}, body.References)
	assert.Empty(body.Users)
}

func TestE2EPRDetailRefreshesEditedCommentBody(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	ctx := t.Context()

	now := time.Now().UTC().Truncate(time.Second).Add(-time.Hour)
	prNumber := 160
	prID := int64(160000)
	prTitle := "Edited comment refresh"
	prState := "open"
	prURL := "https://github.com/acme/widget/pull/160"
	headRef := "feature/edited-comment"
	headSHA := "deadbeef"
	baseRef := "main"
	commentID := int64(9001)
	commentAuthor := "reviewer"
	commentCreatedAt := now.Add(2 * time.Minute)
	commentBody := "original body"

	mock := &mockGH{
		listOpenIssuesFn: func(_ context.Context, _, _ string) ([]*gh.Issue, error) {
			return nil, nil
		},
		getPullRequestFn: func(_ context.Context, _, _ string, number int) (*gh.PullRequest, error) {
			require.Equal(prNumber, number)
			return &gh.PullRequest{
				ID:        &prID,
				Number:    &prNumber,
				Title:     &prTitle,
				HTMLURL:   &prURL,
				State:     &prState,
				UpdatedAt: &gh.Timestamp{Time: now},
				CreatedAt: &gh.Timestamp{Time: now},
				Head: &gh.PullRequestBranch{
					Ref: &headRef,
					SHA: &headSHA,
				},
				Base: &gh.PullRequestBranch{
					Ref: &baseRef,
				},
			}, nil
		},
	}
	prListCalls := 0
	mock.listOpenPullRequestsFn = func(_ context.Context, _, _ string) ([]*gh.PullRequest, error) {
		prListCalls++
		if prListCalls == 1 {
			return []*gh.PullRequest{{
				ID:        &prID,
				Number:    &prNumber,
				Title:     &prTitle,
				HTMLURL:   &prURL,
				State:     &prState,
				UpdatedAt: &gh.Timestamp{Time: now},
				CreatedAt: &gh.Timestamp{Time: now},
				Head: &gh.PullRequestBranch{
					Ref: &headRef,
					SHA: &headSHA,
				},
				Base: &gh.PullRequestBranch{
					Ref: &baseRef,
				},
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
	// A complete provider snapshot can contain overlapping pages or duplicate
	// identities. Detail count must follow the unique synchronized event row.
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

	firstResp, err := client.HTTP.GetPullWithResponse(ctx, &generated.GetPullRequestOptions{PathParams: &generated.GetPullPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(int64(prNumber))}})
	require.NoError(err)
	require.Equal(http.StatusOK, firstResp.StatusCode)
	require.NotNil(firstResp.JSON200)
	require.NotNil(firstResp.JSON200.Events)
	require.Len(firstResp.JSON200.Events, 1)
	assert.Equal("original body", firstResp.JSON200.Events[0].Body)

	editedBody := "edited body"
	mockComments = []*gh.IssueComment{{
		ID:        &commentID,
		Body:      &editedBody,
		User:      &gh.User{Login: &commentAuthor},
		CreatedAt: &gh.Timestamp{Time: commentCreatedAt},
		UpdatedAt: &gh.Timestamp{Time: now.Add(4 * time.Minute)},
	}}

	syncer.RunOnce(ctx)

	secondResp, err := client.HTTP.GetPullWithResponse(ctx, &generated.GetPullRequestOptions{PathParams: &generated.GetPullPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(int64(prNumber))}})
	require.NoError(err)
	require.Equal(http.StatusOK, secondResp.StatusCode)
	require.NotNil(secondResp.JSON200)
	require.NotNil(secondResp.JSON200.Events)
	require.Len(secondResp.JSON200.Events, 1)
	assert.Equal("edited body", secondResp.JSON200.Events[0].Body)
}

func TestE2EPRDetailRemovesDeletedCommentWhenPRListIsUnchanged(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	ctx := t.Context()

	now := time.Now().UTC().Truncate(time.Second).Add(-time.Hour)
	prNumber := 160
	prID := int64(160000)
	prTitle := "Deleted comment refresh"
	prState := "open"
	prURL := "https://github.com/acme/widget/pull/160"
	headRef := "feature/deleted-comment"
	headSHA := "deadbeef"
	baseRef := "main"
	commentID := int64(9001)
	commentAuthor := "reviewer"
	commentCreatedAt := now.Add(2 * time.Minute)
	providerUpdatedAt := now.Add(3 * time.Minute)
	commentBody := "body to remove"

	mock := &mockGH{
		listOpenIssuesFn: func(_ context.Context, _, _ string) ([]*gh.Issue, error) {
			return nil, nil
		},
		getPullRequestFn: func(_ context.Context, _, _ string, number int) (*gh.PullRequest, error) {
			require.Equal(prNumber, number)
			return &gh.PullRequest{
				ID:        &prID,
				Number:    &prNumber,
				Title:     &prTitle,
				HTMLURL:   &prURL,
				State:     &prState,
				UpdatedAt: &gh.Timestamp{Time: providerUpdatedAt},
				CreatedAt: &gh.Timestamp{Time: now},
				Head: &gh.PullRequestBranch{
					Ref: &headRef,
					SHA: &headSHA,
				},
				Base: &gh.PullRequestBranch{
					Ref: &baseRef,
				},
			}, nil
		},
	}
	prListCalls := 0
	mock.listOpenPullRequestsFn = func(_ context.Context, _, _ string) ([]*gh.PullRequest, error) {
		prListCalls++
		if prListCalls == 1 {
			return []*gh.PullRequest{{
				ID:        &prID,
				Number:    &prNumber,
				Title:     &prTitle,
				HTMLURL:   &prURL,
				State:     &prState,
				UpdatedAt: &gh.Timestamp{Time: providerUpdatedAt},
				CreatedAt: &gh.Timestamp{Time: now},
				Head: &gh.PullRequestBranch{
					Ref: &headRef,
					SHA: &headSHA,
				},
				Base: &gh.PullRequestBranch{
					Ref: &baseRef,
				},
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
	// Exercise the PR detail API with the same duplicate-identity snapshot
	// that the atomic replacement layer must collapse.
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

	firstResp, err := client.HTTP.GetPullWithResponse(ctx, &generated.GetPullRequestOptions{PathParams: &generated.GetPullPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(int64(prNumber))}})
	require.NoError(err)
	require.Equal(http.StatusOK, firstResp.StatusCode)
	require.NotNil(firstResp.JSON200)
	require.Equal(int64(1), firstResp.JSON200.MergeRequest.CommentCount)
	require.Equal(providerUpdatedAt.UTC(), firstResp.JSON200.MergeRequest.LastActivityAt.UTC())
	require.NotNil(firstResp.JSON200.Events)
	require.Len(firstResp.JSON200.Events, 1)
	assert.Equal("body to remove", firstResp.JSON200.Events[0].Body)

	mockComments = []*gh.IssueComment{}

	syncer.RunOnce(ctx)

	secondResp, err := client.HTTP.GetPullWithResponse(ctx, &generated.GetPullRequestOptions{PathParams: &generated.GetPullPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(int64(prNumber))}})
	require.NoError(err)
	require.Equal(http.StatusOK, secondResp.StatusCode)
	require.NotNil(secondResp.JSON200)
	require.Equal(int64(0), secondResp.JSON200.MergeRequest.CommentCount)
	require.Equal(providerUpdatedAt.UTC(), secondResp.JSON200.MergeRequest.LastActivityAt.UTC())
	require.NotNil(secondResp.JSON200.Events)
	require.Empty(secondResp.JSON200.Events)
}

func TestE2EPRDetailRemovesDeletedCommentWhenAnotherPRChanges(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	ctx := t.Context()

	now := time.Now().UTC().Truncate(time.Second).Add(-time.Hour)
	targetNumber := 160
	targetID := int64(160000)
	targetTitle := "Target PR keeps stale comment"
	targetURL := "https://github.com/acme/widget/pull/160"
	otherNumber := 161
	otherID := int64(161000)
	otherTitle := "Other PR changes"
	otherURL := "https://github.com/acme/widget/pull/161"
	prState := "open"
	headRef := "feature/comments"
	headSHA := "deadbeef"
	baseRef := "main"
	commentID := int64(9050)
	commentAuthor := "reviewer"
	commentCreatedAt := now.Add(2 * time.Minute)
	targetCommentBody := "target comment"
	targetComments := []*gh.IssueComment{{
		ID:        &commentID,
		Body:      &targetCommentBody,
		User:      &gh.User{Login: &commentAuthor},
		CreatedAt: &gh.Timestamp{Time: commentCreatedAt},
		UpdatedAt: &gh.Timestamp{Time: commentCreatedAt},
	}}
	otherUpdatedAt := now

	prListCalls := 0
	mock := &mockGH{
		listOpenIssuesFn: func(_ context.Context, _, _ string) ([]*gh.Issue, error) {
			return nil, nil
		},
		listOpenPullRequestsFn: func(_ context.Context, _, _ string) ([]*gh.PullRequest, error) {
			prListCalls++
			if prListCalls > 1 {
				otherUpdatedAt = now.Add(5 * time.Minute)
			}
			return []*gh.PullRequest{
				{
					ID:        &targetID,
					Number:    &targetNumber,
					Title:     &targetTitle,
					HTMLURL:   &targetURL,
					State:     &prState,
					UpdatedAt: &gh.Timestamp{Time: now},
					CreatedAt: &gh.Timestamp{Time: now},
					Head:      &gh.PullRequestBranch{Ref: &headRef, SHA: &headSHA},
					Base:      &gh.PullRequestBranch{Ref: &baseRef},
				},
				{
					ID:        &otherID,
					Number:    &otherNumber,
					Title:     &otherTitle,
					HTMLURL:   &otherURL,
					State:     &prState,
					UpdatedAt: &gh.Timestamp{Time: otherUpdatedAt},
					CreatedAt: &gh.Timestamp{Time: now},
					Head:      &gh.PullRequestBranch{Ref: &headRef, SHA: &headSHA},
					Base:      &gh.PullRequestBranch{Ref: &baseRef},
				},
			}, nil
		},
		getPullRequestFn: func(_ context.Context, _, _ string, number int) (*gh.PullRequest, error) {
			switch number {
			case targetNumber:
				return &gh.PullRequest{
					ID:        &targetID,
					Number:    &targetNumber,
					Title:     &targetTitle,
					HTMLURL:   &targetURL,
					State:     &prState,
					UpdatedAt: &gh.Timestamp{Time: now},
					CreatedAt: &gh.Timestamp{Time: now},
					Head:      &gh.PullRequestBranch{Ref: &headRef, SHA: &headSHA},
					Base:      &gh.PullRequestBranch{Ref: &baseRef},
				}, nil
			case otherNumber:
				return &gh.PullRequest{
					ID:        &otherID,
					Number:    &otherNumber,
					Title:     &otherTitle,
					HTMLURL:   &otherURL,
					State:     &prState,
					UpdatedAt: &gh.Timestamp{Time: otherUpdatedAt},
					CreatedAt: &gh.Timestamp{Time: now},
					Head:      &gh.PullRequestBranch{Ref: &headRef, SHA: &headSHA},
					Base:      &gh.PullRequestBranch{Ref: &baseRef},
				}, nil
			default:
				return nil, fmt.Errorf("unexpected pull request %d", number)
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

	firstResp, err := client.HTTP.GetPullWithResponse(ctx, &generated.GetPullRequestOptions{PathParams: &generated.GetPullPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(int64(targetNumber))}})
	require.NoError(err)
	require.Equal(http.StatusOK, firstResp.StatusCode)
	require.NotNil(firstResp.JSON200)
	require.Equal(int64(1), firstResp.JSON200.MergeRequest.CommentCount)
	require.NotNil(firstResp.JSON200.Events)
	require.Len(firstResp.JSON200.Events, 1)
	assert.Equal("target comment", firstResp.JSON200.Events[0].Body)

	targetComments = []*gh.IssueComment{}

	syncer.RunOnce(ctx)

	secondResp, err := client.HTTP.GetPullWithResponse(ctx, &generated.GetPullRequestOptions{PathParams: &generated.GetPullPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(int64(targetNumber))}})
	require.NoError(err)
	require.Equal(http.StatusOK, secondResp.StatusCode)
	require.NotNil(secondResp.JSON200)
	require.Equal(int64(0), secondResp.JSON200.MergeRequest.CommentCount)
	require.NotNil(secondResp.JSON200.Events)
	require.Empty(secondResp.JSON200.Events)
}

func TestE2EPRDetailRemovesDeletedCommentOnFullRefresh(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	ctx := t.Context()

	now := time.Date(2026, 4, 13, 10, 0, 0, 0, time.UTC)
	prNumber := 170
	prID := int64(170000)
	prTitle := "Full refresh deleted comment"
	prState := "open"
	prURL := "https://github.com/acme/widget/pull/170"
	headRef := "feature/full-refresh-delete"
	headSHA := "feedface"
	baseRef := "main"
	commentID := int64(9101)
	commentAuthor := "reviewer"
	commentCreatedAt := now.Add(2 * time.Minute)
	commentBody := "comment removed on full refresh"
	currentUpdatedAt := now.Add(3 * time.Minute)
	currentComments := []*gh.IssueComment{{
		ID:        &commentID,
		Body:      &commentBody,
		User:      &gh.User{Login: &commentAuthor},
		CreatedAt: &gh.Timestamp{Time: commentCreatedAt},
		UpdatedAt: &gh.Timestamp{Time: commentCreatedAt},
	}}

	mock := &mockGH{
		listOpenIssuesFn: func(_ context.Context, _, _ string) ([]*gh.Issue, error) {
			return nil, nil
		},
		getPullRequestFn: func(_ context.Context, _, _ string, number int) (*gh.PullRequest, error) {
			require.Equal(prNumber, number)
			return &gh.PullRequest{
				ID:        &prID,
				Number:    &prNumber,
				Title:     &prTitle,
				HTMLURL:   &prURL,
				State:     &prState,
				UpdatedAt: &gh.Timestamp{Time: currentUpdatedAt},
				CreatedAt: &gh.Timestamp{Time: now},
				Head: &gh.PullRequestBranch{
					Ref: &headRef,
					SHA: &headSHA,
				},
				Base: &gh.PullRequestBranch{
					Ref: &baseRef,
				},
			}, nil
		},
		listIssueCommentsFn: func(_ context.Context, _, _ string, number int) ([]*gh.IssueComment, error) {
			require.Equal(prNumber, number)
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

	require.NoError(syncer.SyncMR(ctx, "acme", "widget", prNumber))

	firstResp, err := client.HTTP.GetPullWithResponse(ctx, &generated.GetPullRequestOptions{PathParams: &generated.GetPullPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(int64(prNumber))}})
	require.NoError(err)
	require.Equal(http.StatusOK, firstResp.StatusCode)
	require.NotNil(firstResp.JSON200)
	require.Equal(int64(1), firstResp.JSON200.MergeRequest.CommentCount)
	require.Equal(currentUpdatedAt.UTC(), firstResp.JSON200.MergeRequest.LastActivityAt.UTC())
	require.NotNil(firstResp.JSON200.Events)
	require.Len(firstResp.JSON200.Events, 1)
	assert.Equal("comment removed on full refresh", firstResp.JSON200.Events[0].Body)

	currentUpdatedAt = now.Add(4 * time.Minute)
	currentComments = []*gh.IssueComment{}

	require.NoError(syncer.SyncMR(ctx, "acme", "widget", prNumber))

	secondResp, err := client.HTTP.GetPullWithResponse(ctx, &generated.GetPullRequestOptions{PathParams: &generated.GetPullPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(int64(prNumber))}})
	require.NoError(err)
	require.Equal(http.StatusOK, secondResp.StatusCode)
	require.NotNil(secondResp.JSON200)
	require.Equal(int64(0), secondResp.JSON200.MergeRequest.CommentCount)
	require.Equal(currentUpdatedAt.UTC(), secondResp.JSON200.MergeRequest.LastActivityAt.UTC())
	require.NotNil(secondResp.JSON200.Events)
	require.Empty(secondResp.JSON200.Events)
}

func TestE2EPRDetailRemovesDeletedCommentOnGraphQLBulkSync(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	ctx := t.Context()

	now := time.Date(2026, 4, 13, 11, 30, 0, 0, time.UTC)
	createdAt := now.Format(time.RFC3339)
	firstUpdatedAt := now.Add(3 * time.Minute).Format(time.RFC3339)
	secondUpdatedAt := now.Add(4 * time.Minute).Format(time.RFC3339)
	commentCreatedAt := now.Add(2 * time.Minute).Format(time.RFC3339)
	currentUpdatedAt := firstUpdatedAt
	currentCommentsJSON := `{"nodes":[{"databaseId":9222,"author":{"login":"commenter"},"body":"bulk PR comment removed","createdAt":"` + commentCreatedAt + `","updatedAt":"` + commentCreatedAt + `"}],"pageInfo":{"hasNextPage":false,"endCursor":""}}`

	gqlSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if bytes.Contains(body, []byte("pullRequests")) {
			resp := `{"data":{"repository":{"pullRequests":{"nodes":[{
				"databaseId":172100,
				"number":173,
				"title":"Bulk deleted comment PR",
				"state":"OPEN",
				"isDraft":false,
				"body":"GraphQL bulk PR",
				"url":"https://github.com/acme/widget/pull/173",
				"author":{"login":"heidi"},
				"createdAt":"` + createdAt + `",
				"updatedAt":"` + currentUpdatedAt + `",
				"mergedAt":null,
				"closedAt":null,
				"additions":1,
				"deletions":0,
				"mergeable":"MERGEABLE",
				"reviewDecision":"",
				"headRefName":"feature/bulk-pr",
				"baseRefName":"main",
				"headRefOid":"deadbeef",
				"baseRefOid":"feedface",
				"headRepository":{"url":"https://github.com/acme/widget"},
				"labels":{"nodes":[]},
				"comments":` + currentCommentsJSON + `,
				"reviews":{"nodes":[],"pageInfo":{"hasNextPage":true,"endCursor":"review-cursor"}},
				"allCommits":{"nodes":[],"pageInfo":{"hasNextPage":true,"endCursor":"commit-cursor"}},
				"lastCommit":{"nodes":[]}
			}],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}`
			_, _ = w.Write([]byte(resp))
			return
		}
		_, _ = w.Write([]byte(`{"data":{"repository":{"issues":{"nodes":[],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}`))
	}))
	defer gqlSrv.Close()

	prID := int64(172100)
	prNumber := 173
	prTitle := "Bulk deleted comment PR"
	prState := "open"
	prURL := "https://github.com/acme/widget/pull/173"
	headRef := "feature/bulk-pr"
	headSHA := "deadbeef"
	baseRef := "main"
	prTime := gh.Timestamp{Time: now}
	mock := &mockGH{
		listOpenPullRequestsFn: func(_ context.Context, _, _ string) ([]*gh.PullRequest, error) {
			updatedAt, parseErr := time.Parse(time.RFC3339, currentUpdatedAt)
			require.NoError(parseErr)
			updatedStamp := gh.Timestamp{Time: updatedAt}
			return []*gh.PullRequest{{
				ID:        &prID,
				Number:    &prNumber,
				Title:     &prTitle,
				State:     &prState,
				HTMLURL:   &prURL,
				User:      &gh.User{Login: new("heidi")},
				CreatedAt: &prTime,
				UpdatedAt: &updatedStamp,
				Head:      &gh.PullRequestBranch{Ref: &headRef, SHA: &headSHA},
				Base:      &gh.PullRequestBranch{Ref: &baseRef},
			}}, nil
		},
		listOpenIssuesFn: func(_ context.Context, _, _ string) ([]*gh.Issue, error) {
			return nil, &gh.ErrorResponse{
				Response: &http.Response{StatusCode: http.StatusNotModified},
			}
		},
	}

	srv, _, syncer := setupTestServerWithMock(t, mock)
	gqlClient := githubv4.NewEnterpriseClient(gqlSrv.URL, gqlSrv.Client())
	syncer.SetFetchers(map[string]*ghclient.GraphQLFetcher{
		"github.com": ghclient.NewGraphQLFetcherWithClient(gqlClient, nil),
	})
	client := setupTestClient(t, srv)

	syncer.RunOnce(ctx)

	firstResp, err := client.HTTP.GetPullWithResponse(ctx, &generated.GetPullRequestOptions{PathParams: &generated.GetPullPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(int64(prNumber))}})
	require.NoError(err)
	require.Equal(http.StatusOK, firstResp.StatusCode)
	require.NotNil(firstResp.JSON200)
	require.Equal(int64(1), firstResp.JSON200.MergeRequest.CommentCount)
	require.Equal(now.Add(3*time.Minute).UTC(), firstResp.JSON200.MergeRequest.LastActivityAt.UTC())
	require.NotNil(firstResp.JSON200.Events)
	require.Len(firstResp.JSON200.Events, 1)
	assert.Equal("bulk PR comment removed", firstResp.JSON200.Events[0].Body)

	currentUpdatedAt = secondUpdatedAt
	currentCommentsJSON = `{"nodes":[],"pageInfo":{"hasNextPage":false,"endCursor":""}}`

	syncer.RunOnce(ctx)

	secondResp, err := client.HTTP.GetPullWithResponse(ctx, &generated.GetPullRequestOptions{PathParams: &generated.GetPullPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(int64(prNumber))}})
	require.NoError(err)
	require.Equal(http.StatusOK, secondResp.StatusCode)
	require.NotNil(secondResp.JSON200)
	require.Equal(int64(0), secondResp.JSON200.MergeRequest.CommentCount)
	require.Equal(now.Add(4*time.Minute).UTC(), secondResp.JSON200.MergeRequest.LastActivityAt.UTC())
	require.NotNil(secondResp.JSON200.Events)
	require.Empty(secondResp.JSON200.Events)
}

// TestE2EGraphQLBulkSyncAppliesAuthoritativeReviewDecisionOverIncompleteReviews
// drives the real GraphQL bulk sync twice against a mocked GraphQL backend with
// real SQLite. The first pass persists an APPROVED review decision; the second
// pass reports a CHANGED authoritative reviewDecision (CHANGES_REQUESTED)
// alongside an incomplete reviews connection (hasNextPage=true, so
// ReviewsComplete is false). GitHub's reviewDecision scalar is authoritative
// over the PR's whole review history, so nested-connection truncation must not
// gate it: the changed decision must reach the HTTP API.
func TestE2EGraphQLBulkSyncAppliesAuthoritativeReviewDecisionOverIncompleteReviews(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	ctx := t.Context()

	now := time.Date(2026, 5, 2, 9, 0, 0, 0, time.UTC)
	firstUpdatedAt := now.Format(time.RFC3339)
	secondUpdatedAt := now.Add(time.Minute).Format(time.RFC3339)
	currentUpdatedAt := firstUpdatedAt
	currentReviewDecision := "APPROVED"
	// First pass: reviews connection complete (empty). The second pass
	// overrides these to an incomplete connection carrying a changed decision.
	currentReviewsConn := `{"nodes":[],"pageInfo":{"hasNextPage":false,"endCursor":""}}`

	gqlSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if bytes.Contains(body, []byte("pullRequests")) {
			resp := `{"data":{"repository":{"pullRequests":{"nodes":[{
				"databaseId":181100,
				"number":181,
				"title":"Authoritative review decision PR",
				"state":"OPEN",
				"isDraft":false,
				"body":"GraphQL bulk PR",
				"url":"https://github.com/acme/widget/pull/181",
				"author":{"login":"heidi"},
				"createdAt":"` + firstUpdatedAt + `",
				"updatedAt":"` + currentUpdatedAt + `",
				"mergedAt":null,
				"closedAt":null,
				"additions":1,
				"deletions":0,
				"mergeable":"MERGEABLE",
				"reviewDecision":"` + currentReviewDecision + `",
				"headRefName":"feature/decision",
				"baseRefName":"main",
				"headRefOid":"cafebabe",
				"baseRefOid":"feedface",
				"headRepository":{"url":"https://github.com/acme/widget"},
				"labels":{"nodes":[]},
				"comments":{"nodes":[],"pageInfo":{"hasNextPage":false,"endCursor":""}},
				"reviews":` + currentReviewsConn + `,
				"allCommits":{"nodes":[],"pageInfo":{"hasNextPage":false,"endCursor":""}},
				"lastCommit":{"nodes":[]}
			}],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}`
			_, _ = w.Write([]byte(resp))
			return
		}
		_, _ = w.Write([]byte(`{"data":{"repository":{"issues":{"nodes":[],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}`))
	}))
	defer gqlSrv.Close()

	prID := int64(181100)
	prNumber := 181
	prTitle := "Authoritative review decision PR"
	prState := "open"
	prURL := "https://github.com/acme/widget/pull/181"
	headRef := "feature/decision"
	headSHA := "cafebabe"
	baseRef := "main"
	prCreated := gh.Timestamp{Time: now}
	mock := &mockGH{
		listOpenPullRequestsFn: func(_ context.Context, _, _ string) ([]*gh.PullRequest, error) {
			updatedAt, parseErr := time.Parse(time.RFC3339, currentUpdatedAt)
			require.NoError(parseErr)
			updatedStamp := gh.Timestamp{Time: updatedAt}
			return []*gh.PullRequest{{
				ID:        &prID,
				Number:    &prNumber,
				Title:     &prTitle,
				State:     &prState,
				HTMLURL:   &prURL,
				User:      &gh.User{Login: new("heidi")},
				CreatedAt: &prCreated,
				UpdatedAt: &updatedStamp,
				Head:      &gh.PullRequestBranch{Ref: &headRef, SHA: &headSHA},
				Base:      &gh.PullRequestBranch{Ref: &baseRef},
			}}, nil
		},
		listOpenIssuesFn: func(_ context.Context, _, _ string) ([]*gh.Issue, error) {
			return nil, &gh.ErrorResponse{
				Response: &http.Response{StatusCode: http.StatusNotModified},
			}
		},
	}

	srv, _, syncer := setupTestServerWithMock(t, mock)
	gqlClient := githubv4.NewEnterpriseClient(gqlSrv.URL, gqlSrv.Client())
	syncer.SetFetchers(map[string]*ghclient.GraphQLFetcher{
		"github.com": ghclient.NewGraphQLFetcherWithClient(gqlClient, nil),
	})
	client := setupTestClient(t, srv)

	// First pass persists the APPROVED decision through the real pipeline.
	syncer.RunOnce(ctx)
	firstResp, err := client.HTTP.GetPullWithResponse(ctx, &generated.GetPullRequestOptions{PathParams: &generated.GetPullPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(int64(prNumber))}})
	require.NoError(err)
	require.Equal(http.StatusOK, firstResp.StatusCode)
	require.NotNil(firstResp.JSON200)
	require.Equal("approved", firstResp.JSON200.MergeRequest.ReviewDecision)

	// Second pass: the provider reports a CHANGED authoritative decision while
	// the reviews connection is truncated (incomplete). The authoritative
	// scalar must still reach the API.
	currentUpdatedAt = secondUpdatedAt
	currentReviewDecision = "CHANGES_REQUESTED"
	currentReviewsConn = `{"nodes":[],"pageInfo":{"hasNextPage":true,"endCursor":"review-cursor"}}`

	syncer.RunOnce(ctx)
	secondResp, err := client.HTTP.GetPullWithResponse(ctx, &generated.GetPullRequestOptions{PathParams: &generated.GetPullPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(int64(prNumber))}})
	require.NoError(err)
	require.Equal(http.StatusOK, secondResp.StatusCode)
	require.NotNil(secondResp.JSON200)
	assert.Equal("changes_requested", secondResp.JSON200.MergeRequest.ReviewDecision)
}

func TestAPIGitHubPublishReviewDraftRejectsSelfApprovalBeforeProvider(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	ctx := t.Context()
	var publishCalls int
	mock := &mockGH{
		authenticatedViewerLoginFn: func(context.Context) (string, error) {
			return "marius", nil
		},
		createReviewWithCommentsFn: func(
			context.Context,
			string, string,
			int,
			string,
			string,
			string,
			[]*gh.DraftReviewComment,
		) (*gh.PullRequestReview, error) {
			publishCalls++
			return nil, errors.New("provider should not be called")
		},
	}
	srv, database, _ := setupTestServerWithMock(t, mock)
	seedPR(t, database, "acme", "widget", 42, withSeedPRAuthor("marius"))
	mr, err := database.GetMergeRequest(ctx, "github", "github.com", "acme", "widget", 42)
	require.NoError(err)
	require.NotNil(mr)
	require.NoError(database.UpdateDiffSHAs(ctx, mr.RepoID, 42, "github-head", "base", "merge-base"))

	basePath := "/api/v1/pulls/gh/acme/widget/42/review-draft"
	createRR := testutil.DoJSON(t, srv, http.MethodPost, basePath+"/comments", map[string]any{
		"body": "Please tighten this line.",
		"range": map[string]any{
			"path":          "src/main.go",
			"side":          "right",
			"line":          42,
			"new_line":      42,
			"line_type":     "add",
			"diff_head_sha": "github-head",
			"commit_sha":    "github-head",
		},
	})

	require.Equal(http.StatusCreated, createRR.Code, createRR.Body.String())

	publishRR := testutil.DoJSON(t, srv, http.MethodPost, basePath+"/publish", map[string]string{
		"action": "approve",
		"body":   "looks good",
	})

	require.Equal(http.StatusForbidden, publishRR.Code, publishRR.Body.String())
	var problem rawProblemDetail
	require.NoError(json.NewDecoder(publishRR.Body).Decode(&problem))
	assert.Equal("forbidden", problem.Code)
	require.NotNil(problem.Details)
	assert.Equal(operationapi.AvailabilityCodeSelfApproval, problem.Details["reason"])
	assert.Zero(publishCalls)

	storedDraft, err := database.GetMRReviewDraft(ctx, mr.ID)
	require.NoError(err)
	assert.NotNil(storedDraft, "rejected self-approval must leave the local draft intact")
}

func TestAPIGitHubApprovePullRejectsSelfApprovalBeforeProvider(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	var providerCalled atomic.Bool
	mock := &mockGH{
		authenticatedViewerLoginFn: func(context.Context) (string, error) {
			return "marius", nil
		},
		createReviewWithCommentsFn: func(
			context.Context,
			string, string,
			int,
			string, string, string,
			[]*gh.DraftReviewComment,
		) (*gh.PullRequestReview, error) {
			providerCalled.Store(true)
			return nil, errors.New("provider should not be called")
		},
	}
	srv, database, _ := setupTestServerWithMock(t, mock)
	seedPR(t, database, "acme", "widget", 42, withSeedPRAuthor("marius"))

	approveRR := testutil.DoJSON(
		t, srv, http.MethodPost,
		"/api/v1/pulls/gh/acme/widget/42/approve",
		map[string]any{"body": ""})

	require.Equal(http.StatusForbidden, approveRR.Code, approveRR.Body.String())
	var problem rawProblemDetail
	require.NoError(json.NewDecoder(approveRR.Body).Decode(&problem))
	assert.Equal("forbidden", problem.Code)
	require.NotNil(problem.Details)
	assert.Equal(operationapi.AvailabilityCodeSelfApproval, problem.Details["reason"])
	assert.False(providerCalled.Load(), "self-approval must be rejected before the provider call")
}

func TestAPIGitLabPublishReviewDraftSendsSummaryThroughServer(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Second)
	providerUpdatedAt := now.Add(time.Minute)
	var order []string
	gitlabServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.EscapedPath() {
		case "/api/v4/projects/4242/merge_requests/7/draft_notes":
			assert.Equal(http.MethodPost, r.Method)
			order = append(order, "create-draft")
			authapi.WriteJSON(w, http.StatusOK, map[string]any{"id": 55, "note": "inline note"})
		case "/api/v4/projects/4242/merge_requests/7/draft_notes/55/publish":
			assert.Equal(http.MethodPut, r.Method)
			order = append(order, "publish-draft")
			authapi.WriteJSON(w, http.StatusOK, map[string]any{})
		case "/api/v4/projects/4242/merge_requests/7/notes":
			assert.Equal(http.MethodPost, r.Method)
			order = append(order, "summary-note")
			var body struct {
				Body string `json:"body"`
			}
			if !assert.NoError(json.NewDecoder(r.Body).Decode(&body)) {
				http.Error(w, "invalid request body", http.StatusBadRequest)
				return
			}
			assert.Equal("review summary from ui", body.Body)
			authapi.WriteJSON(w, http.StatusOK, map[string]any{"id": 77, "body": body.Body})
		case "/api/v4/projects/4242/merge_requests/7/approve":
			assert.Equal(http.MethodPost, r.Method)
			order = append(order, "approve")
			http.Error(w, "approval failed", http.StatusBadRequest)
		case "/api/v4/projects/4242/merge_requests/7/discussions":
			assert.Equal(http.MethodGet, r.Method)
			writeRawJSONForTest(w, `[
				{
					"id": "discussion-55",
					"individual_note": false,
					"notes": [{
						"id": 55,
						"type": "DiscussionNote",
						"body": "inline note",
						"author": {"username": "reviewer"},
						"system": false,
						"resolvable": true,
						"resolved": false,
						"created_at": "`+now.Format(time.RFC3339)+`",
						"updated_at": "`+now.Format(time.RFC3339)+`",
						"position": {
							"base_sha": "base",
							"start_sha": "merge-base",
							"head_sha": "gitlab-head",
							"position_type": "text",
							"new_path": "src/main.go",
							"new_line": 41
						}
					}]
				}
			]`)
		case "/api/v4/projects/4242/merge_requests/7":
			assert.Equal(http.MethodGet, r.Method)
			authapi.WriteJSON(w, http.StatusOK, map[string]any{
				"id": 7001, "iid": 7,
				"updated_at": providerUpdatedAt.Format(time.RFC3339),
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer gitlabServer.Close()

	srv, database, repoID := setupActualGitLabReviewServer(t, gitlabServer.URL, now)
	require.NoError(database.UpdateDiffSHAs(ctx, repoID, 7, "gitlab-head", "base", "merge-base"))

	basePath := "/api/v1/host/gitlab.example.com/pulls/gl/group/project/7/review-draft"
	createRR := testutil.DoJSON(t, srv, http.MethodPost, basePath+"/comments", map[string]any{
		"body": "inline note",
		"range": map[string]any{
			"path":          "src/main.go",
			"side":          "right",
			"line":          41,
			"new_line":      41,
			"line_type":     "add",
			"diff_head_sha": "gitlab-head",
		},
	})

	require.Equal(http.StatusCreated, createRR.Code, createRR.Body.String())

	publishRR := testutil.DoJSON(t, srv, http.MethodPost, basePath+"/publish", map[string]string{
		"action": "approve",
		"body":   " review summary from ui ",
	})

	require.Equal(http.StatusOK, publishRR.Code, publishRR.Body.String())
	var publishStatus pullapi.ActionStatusBody
	require.NoError(json.NewDecoder(publishRR.Body).Decode(&publishStatus))
	assert.Equal("partially_published", publishStatus.Status)
	assert.Equal([]string{"create-draft", "publish-draft", "summary-note", "approve"}, order)

	mr, err := database.GetMergeRequestByRepoIDAndNumber(ctx, repoID, 7)
	require.NoError(err)
	require.NotNil(mr)
	draft, err := database.GetMRReviewDraft(ctx, mr.ID)
	require.NoError(err)
	assert.Nil(draft)
	threads, err := database.ListMRReviewThreads(ctx, mr.ID)
	require.NoError(err)
	require.Len(threads, 1)
	assert.Equal("discussion-55", threads[0].ProviderThreadID)
	assert.Equal(now, threads[0].CreatedAt)
	assert.Equal(now, threads[0].UpdatedAt)
	freshMR, err := database.GetMergeRequestByRepoIDAndNumber(ctx, repoID, 7)
	require.NoError(err)
	require.NotNil(freshMR)
	assert.Equal(providerUpdatedAt, freshMR.UpdatedAt)
	assert.Equal(providerUpdatedAt, freshMR.LastActivityAt)
}

func setupActualGitLabReviewServer(
	t *testing.T,
	gitlabServerURL string,
	now time.Time,
) (*server.Server, *db.DB, int64) {
	t.Helper()
	require := require.New(t)
	ctx := t.Context()
	database := dbtest.Open(t)
	client, err := platformgitlab.NewClient(
		"gitlab.example.com",
		testTokenSource("token"),
		platformgitlab.WithBaseURLForTesting(gitlabServerURL+"/api/v4"),
		platformgitlab.WithoutRetriesForTesting(), platformgitlab.
			WithTransport(http.DefaultTransport),
	)
	require.NoError(err)
	registry, err := platform.NewRegistry(client)
	require.NoError(err)
	repoRef := ghclient.RepoRef{
		Platform:           platform.KindGitLab,
		PlatformHost:       "gitlab.example.com",
		Owner:              "group",
		Name:               "project",
		RepoPath:           "group/project",
		PlatformRepoID:     4242,
		PlatformExternalID: "gid://gitlab/Project/4242",
		WebURL:             "https://gitlab.example.com/group/project",
		CloneURL:           "https://gitlab.example.com/group/project.git",
		DefaultBranch:      "main",
	}
	syncer := ghclient.NewSyncerWithRegistry(
		registry, database, nil, []ghclient.RepoRef{repoRef}, time.Minute, nil, nil,
	)
	t.Cleanup(syncer.Stop)
	srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{})
	t.Cleanup(func() { gracefulShutdown(t, srv) })

	repoID, err := database.UpsertRepo(ctx, db.RepoIdentity{
		Platform:       "gitlab",
		PlatformHost:   "gitlab.example.com",
		PlatformRepoID: "4242",
		Owner:          "group",
		Name:           "project",
		RepoPath:       "group/project",
	})
	require.NoError(err)
	require.NoError(database.UpdateRepoProviderMetadata(ctx, repoID, db.RepoProviderMetadata{
		PlatformRepoID: "4242",
		WebURL:         "https://gitlab.example.com/group/project",
		CloneURL:       "https://gitlab.example.com/group/project.git",
		DefaultBranch:  "main",
	}))
	_, err = database.UpsertMergeRequest(ctx, &db.MergeRequest{
		RepoID:             repoID,
		PlatformID:         7001,
		PlatformExternalID: "gid://gitlab/MergeRequest/7001",
		Number:             7,
		URL:                "https://gitlab.example.com/group/project/-/merge_requests/7",
		Title:              "GitLab provider MR",
		Author:             "ada",
		State:              "open",
		HeadBranch:         "feature/gitlab",
		BaseBranch:         "main",
		PlatformHeadSHA:    "gitlab-head",
		PlatformBaseSHA:    "base",
		CreatedAt:          now,
		UpdatedAt:          now,
		LastActivityAt:     now,
	})
	require.NoError(err)
	return srv, database, repoID
}

func writeRawJSONForTest(w http.ResponseWriter, body string) {
	w.WriteHeader(http.StatusOK)
	_, _ = io.WriteString(w, body)
}

func TestAPIApplyReviewSuggestionRejectsRateLimitedOperationBeforeProviderCall(t *testing.T) {
	runParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	caps := platform.Capabilities{
		ReadRepositories:            true,
		ReadMergeRequests:           true,
		ReadIssues:                  true,
		ReadComments:                true,
		ReviewSuggestionApplication: true,
		MutationHeadBinding:         true,
		ReadReviewThreads:           true,
	}
	srv, database, provider, syncer := setupGitLabCapabilityServerWithProvider(t, &caps)
	ctx := t.Context()
	provider.rateLimitBuckets = map[platform.OperationName][]platform.RateLimitBucket{
		platform.OperationApplyReviewSuggestion: {platform.RateLimitBucketREST},
	}
	rt := ghclient.NewPlatformRateTracker(database, "gitlab", "gitlab.example.com", "host", "rest")
	syncer.RateTrackers()[ghclient.RateBucketKey("gitlab", "gitlab.example.com", "host")] = rt
	rt.UpdateFromRate(ghclient.Rate{
		Limit:     5000,
		Remaining: 0,
		Reset:     time.Now().UTC().Add(30 * time.Minute),
	})

	repo, err := database.GetRepoByIdentity(ctx, db.RepoIdentity{
		Platform:     "gitlab",
		PlatformHost: "gitlab.example.com",
		RepoPath:     "group/project",
	})
	require.NoError(err)
	require.NotNil(repo)
	mr, err := database.GetMergeRequestByRepoIDAndNumber(ctx, repo.ID, 7)
	require.NoError(err)
	require.NotNil(mr)
	line := 11
	require.NoError(database.UpsertMRReviewThreads(ctx, mr.ID, []db.MRReviewThread{{
		ProviderThreadID:  "provider-thread-1",
		ProviderCommentID: "provider-comment-1",
		Body:              "Please apply this.\n\n```suggestion\nreturn client.publishThreads();\n```",
		AuthorLogin:       "ada",
		Range: db.ReviewLineRange{
			Path:        "src/review.ts",
			Side:        "right",
			Line:        line,
			NewLine:     &line,
			LineType:    "context",
			DiffHeadSHA: "abc123",
		},
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}}))
	threads, err := database.ListMRReviewThreads(ctx, mr.ID)
	require.NoError(err)
	require.Len(threads, 1)

	rr := testutil.DoJSON(
		t,
		srv,
		http.MethodPost,
		"/api/v1/host/gitlab.example.com/pulls/gl/group/project/7/review-suggestions/apply",
		map[string]any{
			"expected_head_sha": "abc123",
			"suggestions": []map[string]any{{
				"thread_id":   strconv.FormatInt(threads[0].ID, 10),
				"replacement": "return client.publishThreads();",
			}},
		})

	require.Equal(http.StatusTooManyRequests, rr.Code, rr.Body.String())
	var problem rawProblemDetail
	require.NoError(json.NewDecoder(rr.Body).Decode(&problem))
	assert.Equal("rateLimited", problem.Code)
	assert.Empty(provider.appliedSuggestions)
}

func setupGitLabCapabilityServerWithProvider(
	t *testing.T,
	caps *platform.Capabilities,
) (*server.Server, *db.DB, *apiTestGitLabProvider, *ghclient.Syncer) {
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
	provider := &apiTestGitLabProvider{
		ref:          ref,
		capabilities: caps,
		mergeRequests: []platform.MergeRequest{{
			Repo:               ref,
			PlatformID:         7001,
			PlatformExternalID: "gid://gitlab/MergeRequest/7001",
			Number:             7,
			URL:                "https://gitlab.example.com/group/project/-/merge_requests/7",
			Title:              "GitLab provider MR",
			Author:             "ada",
			State:              "open",
			IsDraft:            true,
			HeadBranch:         "feature/gitlab",
			HeadRepoCloneURL:   "https://gitlab.example.com/fork/project.git",
			BaseBranch:         "main",
			HeadSHA:            "abc123",
			BaseSHA:            "def456",
			CreatedAt:          now,
			UpdatedAt:          now,
			LastActivityAt:     now,
		}},
		issues: []platform.Issue{{
			Repo:               ref,
			PlatformID:         8001,
			PlatformExternalID: "gid://gitlab/Issue/8001",
			Number:             11,
			URL:                "https://gitlab.example.com/group/project/-/issues/11",
			Title:              "GitLab provider issue",
			Author:             "grace",
			State:              "open",
			CreatedAt:          now,
			UpdatedAt:          now,
			LastActivityAt:     now,
		}},
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
	srv := server.New(database, syncer, nil, "/", &config.Config{Tmux: config.Tmux{
		Command: []string{filepath.Join(t.TempDir(), "missing-tmux")},
	}}, server.ServerOptions{
		WorktreeDir:                        t.TempDir(),
		DisableWorkspaceBackgroundMonitors: true,
	})
	t.Cleanup(func() { gracefulShutdown(t, srv) })

	syncer.RunOnce(ctx)
	return srv, database, provider, syncer
}

func TestAPIRateLimitsUsesSafeIdentityKeyAndResolvedPrincipalLabel(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	database := dbtest.Open(t)
	identity := ghclient.IdentityKey{Host: "github.com", Principal: "user:123"}
	restRT := ghclient.NewRateTracker(database, "github.com", "user:123", "rest")
	bucket := ghclient.RateBucketKey("github", "github.com", "user:123")
	router, err := ghclient.NewHostRouter(
		"github.com",
		&ghclient.Route{
			Key:          ghclient.RouteKey{Host: "github.com", Owner: "acme"},
			ReadIdentity: identity, WriteIdentity: identity,
		},
	)
	require.NoError(err)
	syncer := ghclient.NewSyncer(
		map[string]ghclient.Client{"github.com": &mockGH{}},
		database, nil,
		[]ghclient.RepoRef{{Owner: "acme", Name: "widget", PlatformHost: "github.com"}},
		time.Minute,
		map[string]*ghclient.RateTracker{bucket: restRT}, nil,
	)
	syncer.SetGitHubRouters(map[string]*ghclient.HostRouter{"github.com": router})
	syncer.SetRatePrincipalLabels(map[string]string{bucket: "GitHub user maintainer"})

	srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{})
	ts := httptest.NewServer(srv)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/v1/rate-limits")
	require.NoError(err)
	defer resp.Body.Close()
	require.Equal(http.StatusOK, resp.StatusCode)
	var body itemapi.RateLimitsResponse
	require.NoError(json.NewDecoder(resp.Body).Decode(&body))
	require.Len(body.ProviderPools, 1)
	for key, status := range body.ProviderPools {
		assert.NotContains(key, "\x00")
		assert.Equal("github:github.com:user:123", key)
		assert.Equal("user:123", status.RatePrincipal)
		assert.Equal("GitHub user maintainer", status.PrincipalLabel)
	}
}

func seedStackedPR(
	t *testing.T, database *db.DB,
	owner, name string, number int,
	head, base string, state db.MergeRequestState, ci, review string,
) int64 {
	return seedStackedPRState(t, database, owner, name, number, head, base, state, ci, review, false, "")
}

func seedStackedPRState(
	t *testing.T, database *db.DB,
	owner, name string, number int,
	head, base string, state db.MergeRequestState, ci, review string,
	isDraft bool,
	mergeableState string,
) int64 {
	t.Helper()
	ctx := t.Context()
	repoID, err := database.UpsertRepo(ctx, verifiedGitHubRepoIdentity("github.com", owner, name))
	require.NoError(t, err)
	cloneURL := fmt.Sprintf("https://github.com/%s/%s.git", owner, name)
	require.NoError(t, database.UpdateRepoProviderMetadata(ctx, repoID, db.RepoProviderMetadata{
		CloneURL:      cloneURL,
		DefaultBranch: "main",
	}))
	now := time.Now().UTC().Truncate(time.Second)
	pr := &db.MergeRequest{
		RepoID:           repoID,
		PlatformID:       int64(number) * 1000,
		Number:           number,
		Title:            fmt.Sprintf("PR #%d: %s", number, head),
		Author:           "testuser",
		State:            state,
		IsDraft:          isDraft,
		HeadBranch:       head,
		BaseBranch:       base,
		HeadRepoCloneURL: cloneURL,
		CIStatus:         ci,
		ReviewDecision:   review,
		MergeableState:   mergeableState,
		CreatedAt:        now,
		UpdatedAt:        now,
		LastActivityAt:   now,
	}
	prID, err := database.UpsertMergeRequest(ctx, pr)
	require.NoError(t, err)
	require.NoError(t, database.EnsureKanbanState(ctx, prID))
	return prID
}

func stackMemberNumbers(members []generated.StackMemberResponse) []int64 {
	numbers := make([]int64, len(members))
	for i, member := range members {
		numbers[i] = member.Number
	}
	return numbers
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

// TestMergeBlocksPredecessorRestoredWhenNativeStackAgesOut is the full-stack
// consequence of the observation-based aging bound. Cache aging spans hours, so
// the syncer clock is injected rather than waited on. Under the stale native
// projection PR 101 follows a merged predecessor and would merge; once the
// observation ages out the projection returns to branch inference, which
// restores the open predecessor the merge safeguard must block on.
func TestMergeBlocksPredecessorRestoredWhenNativeStackAgesOut(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	observed := time.Now().UTC().Add(-24 * time.Hour).Truncate(time.Second)
	repoCloneURL := "https://github.com/acme/widget.git"
	makeGHPR := func(id int64, number int, head, base string) *gh.PullRequest {
		sha := fmt.Sprintf("sha%d", number)
		title := fmt.Sprintf("PR #%d", number)
		return &gh.PullRequest{
			ID: &id, Number: &number, State: new("open"), Title: &title,
			Body: new(""), User: &gh.User{Login: new("testuser")},
			CreatedAt: &gh.Timestamp{Time: observed}, UpdatedAt: &gh.Timestamp{Time: observed},
			Head: &gh.PullRequestBranch{
				Ref: &head, SHA: &sha,
				Repo: &gh.Repository{CloneURL: &repoCloneURL},
			},
			Base: &gh.PullRequestBranch{Ref: &base, SHA: new("basesha")},
		}
	}
	prs := []*gh.PullRequest{
		makeGHPR(1000, 100, "feature/a", "main"),
		makeGHPR(1001, 101, "feature/b", "feature/a"),
	}
	var listCalls atomic.Int32
	merged := false
	mock := &mockGH{
		getRepositoryFn: func(_ context.Context, owner, repo string) (*gh.Repository, error) {
			nodeID := "repo-" + owner + "-" + repo
			return &gh.Repository{
				Name: &repo, NodeID: &nodeID, Owner: &gh.User{Login: &owner},
				CloneURL: &repoCloneURL, Archived: new(false),
			}, nil
		},
		mergePullRequestFn: func(_ context.Context, _, _ string, _ int, _, _, _ string) (*gh.PullRequestMergeResult, error) {
			merged = true
			return &gh.PullRequestMergeResult{}, nil
		},
		nativeStackAPI: &mockGHNativeStackAPI{
			listOpenPullRequests: func(
				context.Context, string, string,
			) ([]*gh.PullRequest, map[int]*platformgithub.NativeStackHint, error) {
				if listCalls.Add(1) > 1 {
					// The open-PR list is byte-identical on the later sync, which
					// is the path a stale confirmation would survive on.
					return nil, nil, &gh.ErrorResponse{Response: &http.Response{
						StatusCode: http.StatusNotModified,
						Request: &http.Request{
							Method: http.MethodGet,
							URL:    &url.URL{Scheme: "https", Host: "api.github.com", Path: "/pulls"},
						},
					}}
				}
				// Only the tip is claimed by the stack, so no hint can attest to the
				// leading member the cached row names.
				return prs, map[int]*platformgithub.NativeStackHint{
					101: {Number: 42, Size: 2, Position: 2, BaseRef: "main"},
				}, nil
			},
			listStackPage: func(
				context.Context, string, string, int,
			) (platformgithub.NativeStackPage, error) {
				return platformgithub.NativeStackPage{}, errors.New("catalog must not be refetched while confirmed")
			},
		},
	}
	srv, database, syncer := setupTestServerWithMock(t, mock)
	_, err := database.UpsertRepo(ctx, db.RepoIdentity{
		Platform:       "github",
		PlatformHost:   "github.com",
		PlatformRepoID: "repo-acme-widget",
		Owner:          "acme",
		Name:           "widget",
	})
	require.NoError(err)
	// PR 900 is merged, so the stale native chain shows PR 101 following a
	// finished predecessor.
	seedStackedPR(t, database, "acme", "widget", 900, "feature/z", "main", db.MergeRequestStateMerged, "", "")
	repo, err := database.GetRepoByIdentity(ctx, verifiedGitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	require.NotNil(repo)
	require.NoError(database.ReplaceGitHubNativeStack(ctx, db.GitHubNativeStack{
		RepoID: repo.ID, GitHubID: 9042, Number: 42, Size: 2,
		BaseRef: "main", IsOpen: true, GitHubCreatedAt: observed,
		ContentFingerprint: "native-42", LastObservedAt: observed,
		Members: []db.GitHubNativeStackMember{
			{Position: 1, PullRequestNumber: 900, State: "merged", HeadRef: "feature/z", HeadSHA: "sha900"},
			{Position: 2, PullRequestNumber: 101, State: "open", HeadRef: "feature/b", HeadSHA: "sha101"},
		},
	}))
	clock := observed.Add(11 * time.Hour)
	syncer.SetClock(func() time.Time { return clock })
	syncer.SetPreferGitHubNativeStacks(true)
	syncer.SetOnSyncCompleted(stacks.SyncCompletedHook(ctx, database, nil))
	client := setupTestClient(t, srv)

	syncer.RunOnce(ctx)

	stackResp, err := client.HTTP.GetPullStackWithResponse(ctx, &generated.GetPullStackRequestOptions{PathParams: &generated.GetPullStackPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(101)}})
	require.NoError(err)
	require.Equal(http.StatusOK, stackResp.StatusCode, string(stackResp.Body))
	require.NotNil(stackResp.JSON200)
	require.NotNil(stackResp.JSON200.Members)
	require.Equal([]int64{900, 101}, stackMemberNumbers(stackResp.JSON200.Members),
		"inside its observation window the cached stack still owns the projection")

	// Two hours later the cached stack is past its own 12h window.
	clock = observed.Add(13 * time.Hour)
	syncer.RunOnce(ctx)

	stackResp, err = client.HTTP.GetPullStackWithResponse(ctx, &generated.GetPullStackRequestOptions{PathParams: &generated.GetPullStackPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(101)}})
	require.NoError(err)
	require.Equal(http.StatusOK, stackResp.StatusCode, string(stackResp.Body))
	require.NotNil(stackResp.JSON200)
	require.NotNil(stackResp.JSON200.Members)
	assert.Equal([]int64{100, 101}, stackMemberNumbers(stackResp.JSON200.Members),
		"an aged observation must hand the repository back to branch inference")

	tipHeadSHA := "sha101"
	mergeResp, err := client.HTTP.MergePullWithResponse(ctx, &generated.MergePullRequestOptions{PathParams: &generated.MergePullPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(101)}, Body: &generated.MergePRInputBody{Method: "squash", ExpectedHeadSha: &tipHeadSHA}})
	require.Error(err)
	require.NotNil(mergeResp)

	assert.Equal(http.StatusConflict, mergeResp.StatusCode, string(mergeResp.Body))
	assert.Contains(string(mergeResp.Body), `"reason":"mid_stack_merge_disallowed"`)
	assert.Contains(string(mergeResp.Body), `"blocking_number":100`)
	assert.False(merged, "the provider must not be asked to merge past an open predecessor")
}

// TestMergeBlocksPredecessorWhenNativeStackRefreshIsPartial is the full-stack
// consequence of refusing to project a partial refresh. Stack 42 is confirmable
// from cache and would place PR 101 behind a merged predecessor; stack 43 is
// hinted but its catalog row is rejected, so nothing about it -- including
// whether it also claims PR 101 -- is known. The pass must fall back to branch
// inference, which restores the open predecessor the safeguard blocks on.
func TestMergeBlocksPredecessorWhenNativeStackRefreshIsPartial(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Second)
	repoCloneURL := "https://github.com/acme/widget.git"
	makeGHPR := func(id int64, number int, head, base string) *gh.PullRequest {
		sha := fmt.Sprintf("sha%d", number)
		title := fmt.Sprintf("PR #%d", number)
		return &gh.PullRequest{
			ID: &id, Number: &number, State: new("open"), Title: &title,
			Body: new(""), User: &gh.User{Login: new("testuser")},
			CreatedAt: &gh.Timestamp{Time: now}, UpdatedAt: &gh.Timestamp{Time: now},
			Head: &gh.PullRequestBranch{
				Ref: &head, SHA: &sha,
				Repo: &gh.Repository{CloneURL: &repoCloneURL},
			},
			Base: &gh.PullRequestBranch{Ref: &base, SHA: new("basesha")},
		}
	}
	prs := []*gh.PullRequest{
		makeGHPR(1000, 100, "feature/a", "main"),
		makeGHPR(1001, 101, "feature/b", "feature/a"),
		makeGHPR(1003, 103, "feature/c", "main"),
	}
	merged := false
	mock := &mockGH{
		getRepositoryFn: func(_ context.Context, owner, repo string) (*gh.Repository, error) {
			nodeID := "repo-" + owner + "-" + repo
			return &gh.Repository{
				Name: &repo, NodeID: &nodeID, Owner: &gh.User{Login: &owner},
				CloneURL: &repoCloneURL, Archived: new(false),
			}, nil
		},
		mergePullRequestFn: func(_ context.Context, _, _ string, _ int, _, _, _ string) (*gh.PullRequestMergeResult, error) {
			merged = true
			return &gh.PullRequestMergeResult{}, nil
		},
		nativeStackAPI: &mockGHNativeStackAPI{
			listOpenPullRequests: func(
				context.Context, string, string,
			) ([]*gh.PullRequest, map[int]*platformgithub.NativeStackHint, error) {
				return prs, map[int]*platformgithub.NativeStackHint{
					101: {Number: 42, Size: 2, Position: 2, BaseRef: "main"},
					103: {Number: 43, Size: 1, Position: 1, BaseRef: "main"},
				}, nil
			},
			listStackPage: func(
				context.Context, string, string, int,
			) (platformgithub.NativeStackPage, error) {
				// Stack 43 comes back naming a different pull request than the hint,
				// so the row is rejected and the target stays unresolved.
				return platformgithub.NativeStackPage{Stacks: []platformgithub.NativeStack{{
					ID: 9043, Number: 43, BaseRef: "main", Open: true, CreatedAt: now,
					Members: []platformgithub.NativeStackMember{
						{Position: 1, PullRequestNumber: 999, State: "open", HeadRef: "feature/x", HeadSHA: "sha999"},
					},
				}}}, nil
			},
		},
	}
	srv, database, syncer := setupTestServerWithMock(t, mock)
	seedStackedPR(t, database, "acme", "widget", 900, "feature/z", "main", db.MergeRequestStateMerged, "", "")
	repo, err := database.GetRepoByIdentity(ctx, verifiedGitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	require.NotNil(repo)
	require.NoError(database.ReplaceGitHubNativeStack(ctx, db.GitHubNativeStack{
		RepoID: repo.ID, GitHubID: 9042, Number: 42, Size: 2,
		BaseRef: "main", IsOpen: true, GitHubCreatedAt: now,
		ContentFingerprint: "native-42", LastObservedAt: now,
		Members: []db.GitHubNativeStackMember{
			{Position: 1, PullRequestNumber: 900, State: "merged", HeadRef: "feature/z", HeadSHA: "sha900"},
			{Position: 2, PullRequestNumber: 101, State: "open", HeadRef: "feature/b", HeadSHA: "sha101"},
		},
	}))
	syncer.SetPreferGitHubNativeStacks(true)
	syncer.SetOnSyncCompleted(stacks.SyncCompletedHook(ctx, database, nil))
	client := setupTestClient(t, srv)

	syncer.RunOnce(ctx)

	stackResp, err := client.HTTP.GetPullStackWithResponse(ctx, &generated.GetPullStackRequestOptions{PathParams: &generated.GetPullStackPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(101)}})
	require.NoError(err)
	require.Equal(http.StatusOK, stackResp.StatusCode, string(stackResp.Body))
	require.NotNil(stackResp.JSON200)
	require.NotNil(stackResp.JSON200.Members)
	assert.Equal([]int64{100, 101}, stackMemberNumbers(stackResp.JSON200.Members),
		"a pass that could not resolve every stack must project none of them")

	tipHeadSHA := "sha101"
	mergeResp, err := client.HTTP.MergePullWithResponse(ctx, &generated.MergePullRequestOptions{PathParams: &generated.MergePullPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(101)}, Body: &generated.MergePRInputBody{Method: "squash", ExpectedHeadSha: &tipHeadSHA}})
	require.Error(err)
	require.NotNil(mergeResp)

	assert.Equal(http.StatusConflict, mergeResp.StatusCode, string(mergeResp.Body))
	assert.Contains(string(mergeResp.Body), `"reason":"mid_stack_merge_disallowed"`)
	assert.Contains(string(mergeResp.Body), `"blocking_number":100`)
	assert.False(merged, "the provider must not be asked to merge past an open predecessor")
}
