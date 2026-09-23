package pullreviewtest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
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
	"go.kenn.io/forge/internal/server/pullapi"
	"go.kenn.io/forge/internal/stacks"
	"go.kenn.io/forge/internal/testutil"
	"go.kenn.io/forge/internal/testutil/dbtest"
	"go.kenn.io/forge/internal/testutil/gitsafe"
	"go.kenn.io/forge/internal/testutil/processjob"
	"go.kenn.io/forge/internal/testutil/testsignal"
	"go.kenn.io/forge/internal/testutil/testtmux"
	"go.kenn.io/forge/platform"
	forgejoplatform "go.kenn.io/forge/platform/forgejo"
	giteaplatform "go.kenn.io/forge/platform/gitea"
	"go.kenn.io/forge/platform/gitealike"
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

func testEDTTime(hour, minute int) time.Time {
	//nolint:forbidigo // Test fixture intentionally uses a non-UTC timestamp to verify UTC normalization.
	return time.Date(2026, 4, 11, hour, minute, 0, 0, time.FixedZone("EDT", -4*60*60))
}

func assertTimePtrUTC(t *testing.T, got *time.Time) {
	t.Helper()
	require.NotNil(t, got)
	assert.Equal(t, time.UTC, got.Location())
}

func assertTimePtrEqualsUTC(t *testing.T, got *time.Time, want time.Time) {
	t.Helper()
	assertTimePtrUTC(t, got)
	assert.Equal(t, want.UTC(), got.UTC())
}

// providerStatePR builds the provider's view of a seeded test PR after a
// state mutation, the way the real provider returns it from an edit or a
// post-merge fetch. The handler commits this snapshot through the canonical
// parent-snapshot path, so updatedAt must be newer than the seeded row's or
// the monotonic guard rejects it; merged/closed timestamps come from here,
// not from any local clock.
func providerStatePR(
	number int,
	state string,
	updatedAt time.Time,
	closedAt, mergedAt *time.Time,
	headSHA string,
) *gh.PullRequest {
	toTimestamp := func(value *time.Time) *gh.Timestamp {
		if value == nil {
			return nil
		}
		return &gh.Timestamp{Time: *value}
	}
	numberText := strconv.Itoa(number)
	pr := &gh.PullRequest{
		ID:        new(int64(number) * 1000),
		Number:    &number,
		State:     &state,
		Title:     new("Test PR #" + numberText),
		HTMLURL:   new("https://github.com/acme/widget/pull/" + numberText),
		User:      &gh.User{Login: new("testuser")},
		CreatedAt: &gh.Timestamp{Time: updatedAt.Add(-2 * time.Hour)},
		UpdatedAt: &gh.Timestamp{Time: updatedAt},
		ClosedAt:  toTimestamp(closedAt),
		MergedAt:  toTimestamp(mergedAt),
		Head: &gh.PullRequestBranch{
			Ref: new("feature"), SHA: &headSHA,
			Repo: &gh.Repository{ID: new(int64(1)), FullName: new("acme/widget")},
		},
		Base: &gh.PullRequestBranch{
			Ref: new("main"), SHA: new("base-sha"),
			Repo: &gh.Repository{ID: new(int64(1)), FullName: new("acme/widget")},
		},
	}
	if mergedAt != nil {
		pr.Merged = new(true)
		pr.MergedBy = &gh.User{Login: new("merger")}
	}
	return pr
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type staleReadyForReviewError struct{ err error }

func (e *staleReadyForReviewError) Error() string { return e.err.Error() }

func (e *staleReadyForReviewError) Unwrap() error { return e.err }

func (e *staleReadyForReviewError) StatusCode() int { return http.StatusNotFound }

func (e *staleReadyForReviewError) IsStaleState() bool { return true }

type seedPROpt func(*db.MergeRequest)

func withSeedPRHeadSHA(headSHA string) seedPROpt {
	return func(pr *db.MergeRequest) { pr.PlatformHeadSHA = headSHA }
}

func withSeedPRBaseSHA(baseSHA string) seedPROpt {
	return func(pr *db.MergeRequest) { pr.PlatformBaseSHA = baseSHA }
}

func withSeedPRTitle(title string) seedPROpt {
	return func(pr *db.MergeRequest) { pr.Title = title }
}

func withSeedPRAuthor(author string) seedPROpt {
	return func(pr *db.MergeRequest) { pr.Author = author }
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

func withSeedPRCI(status, checksJSON string) seedPROpt {
	return func(pr *db.MergeRequest) {
		pr.CIStatus = status
		pr.CIChecksJSON = checksJSON
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

func TestAPIReplyToGitHubReviewThreadUsesProviderCommentID(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	ctx := t.Context()

	providerUpdatedAt := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	var gotCommentID int64
	var gotBody string
	mock := &mockGH{
		createReviewCommentReplyFn: func(
			_ context.Context, owner, repo string, number int, body string, commentID int64,
		) (*gh.PullRequestComment, error) {
			assert.Equal("acme", owner)
			assert.Equal("widget", repo)
			assert.Equal(7, number)
			gotCommentID = commentID
			gotBody = body
			id := int64(222)
			login := "fixture-bot"
			now := gh.Timestamp{Time: providerUpdatedAt.Add(-time.Second)}
			return &gh.PullRequestComment{
				ID:        &id,
				Body:      &body,
				User:      &gh.User{Login: &login},
				CreatedAt: &now,
			}, nil
		},
		getPullRequestFn: func(_ context.Context, _, _ string, number int) (*gh.PullRequest, error) {
			return providerStatePR(number, "open", providerUpdatedAt, nil, nil, "head-sha"), nil
		},
	}
	srv, database := setupTestServerWithMock(t, mock)
	seededAt := providerUpdatedAt.Add(-2 * time.Minute)
	mrID := seedPR(t, database, "acme", "widget", 7,
		withSeedPRTimes(seededAt, seededAt, seededAt),
	)

	now := time.Now().UTC().Truncate(time.Second)
	newLine := 11
	require.NoError(database.UpsertMRReviewThreads(ctx, mrID, []db.MRReviewThread{{
		ProviderThreadID:  "PRRT_1",
		ProviderCommentID: "101",
		Body:              "Please keep this explicit.",
		AuthorLogin:       "reviewer",
		Range: db.ReviewLineRange{
			Path:        "src/review.ts",
			Side:        "right",
			Line:        11,
			NewLine:     &newLine,
			LineType:    "add",
			DiffHeadSHA: "head-sha",
		},
		CreatedAt: now,
		UpdatedAt: now,
	}}))
	threads, err := database.ListMRReviewThreads(ctx, mrID)
	require.NoError(err)
	require.Len(threads, 1)

	localThreadID := strconv.FormatInt(threads[0].ID, 10)
	req := httptest.NewRequestWithContext(t.Context(),
		http.MethodPost,
		"/api/v1/pulls/github/acme/widget/7/discussions/"+localThreadID+"/reply",
		strings.NewReader(`{"body":"Reply from kenn-forge"}`),
	)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	require.Equal(http.StatusCreated, rr.Code, "response: %s", rr.Body.String())
	assert.Equal(int64(101), gotCommentID)
	assert.Equal("Reply from kenn-forge", gotBody)

	var result struct {
		PlatformExternalID string  `json:"PlatformExternalID"`
		EventType          string  `json:"EventType"`
		Author             string  `json:"Author"`
		Body               string  `json:"Body"`
		ThreadID           *string `json:"ThreadID"`
	}
	require.NoError(json.NewDecoder(rr.Body).Decode(&result))
	assert.Equal("222", result.PlatformExternalID)
	assert.Equal("review_comment", result.EventType)
	assert.Equal("fixture-bot", result.Author)
	assert.Equal("Reply from kenn-forge", result.Body)
	require.NotNil(result.ThreadID)
	assert.Equal("PRRT_1", *result.ThreadID)

	events, err := database.ListMREvents(ctx, mrID)
	require.NoError(err)
	require.Len(events, 1)
	assert.Equal("review_comment", events[0].EventType)
	assert.Equal("222", events[0].PlatformExternalID)
	assert.Equal("review_comment:222", events[0].DedupeKey)
	require.NotNil(events[0].ThreadID)
	assert.Equal("PRRT_1", *events[0].ThreadID)
	storedMR, err := database.GetMergeRequest(ctx, "github", "github.com", "acme", "widget", 7)
	require.NoError(err)
	require.NotNil(storedMR)
	assert.Equal(providerUpdatedAt, storedMR.UpdatedAt)
	assert.Equal(providerUpdatedAt, storedMR.LastActivityAt)
}

func TestAPIMergePR405ReturnsGitHubMessage(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)

	mock := &mockGH{
		mergePullRequestFn: func(_ context.Context, _, _ string, _ int, _, _, _ string) (*gh.PullRequestMergeResult, error) {
			return nil, &gh.ErrorResponse{
				Response: &http.Response{StatusCode: 405},
				Message:  "Pull Request is not mergeable",
			}
		},
	}

	srv, database := setupTestServerWithMock(t, mock)
	expectedHeadSHA := "reviewed-sha"
	seedPR(t, database, "acme", "widget", 1, withSeedPRHeadSHA(expectedHeadSHA))
	client := setupTestClient(t, srv)

	resp, err := client.HTTP.MergePullWithResponse(t.Context(), &generated.MergePullRequestOptions{PathParams: &generated.MergePullPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}, Body: &generated.MergePRInputBody{
		CommitTitle:     "title",
		CommitMessage:   "msg",
		Method:          "squash",
		ExpectedHeadSha: &expectedHeadSHA,
	}})
	require.Error(err)
	require.NotNil(resp)
	require.Equal(http.StatusConflict, resp.StatusCode)
	require.Contains(string(resp.Body), "Pull Request is not mergeable")
}

func TestAPIMergePR409ReturnsGitHubMessage(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)

	mock := &mockGH{
		mergePullRequestFn: func(_ context.Context, _, _ string, _ int, _, _, _ string) (*gh.PullRequestMergeResult, error) {
			return nil, &gh.ErrorResponse{
				Response: &http.Response{StatusCode: 409},
				Message:  "Head branch was modified",
			}
		},
	}

	srv, database := setupTestServerWithMock(t, mock)
	expectedHeadSHA := "reviewed-sha"
	seedPR(t, database, "acme", "widget", 1, withSeedPRHeadSHA(expectedHeadSHA))
	client := setupTestClient(t, srv)

	resp, err := client.HTTP.MergePullWithResponse(t.Context(), &generated.MergePullRequestOptions{PathParams: &generated.MergePullPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}, Body: &generated.MergePRInputBody{
		CommitTitle:     "title",
		CommitMessage:   "msg",
		Method:          "squash",
		ExpectedHeadSha: &expectedHeadSHA,
	}})
	require.Error(err)
	require.NotNil(resp)
	require.Equal(http.StatusConflict, resp.StatusCode)
	require.Contains(string(resp.Body), "Head branch was modified")
}

func TestAPIMergePRNetworkErrorReturns502(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)

	mock := &mockGH{
		mergePullRequestFn: func(_ context.Context, _, _ string, _ int, _, _, _ string) (*gh.PullRequestMergeResult, error) {
			return nil, fmt.Errorf("connection refused")
		},
	}

	srv, database := setupTestServerWithMock(t, mock)
	expectedHeadSHA := "reviewed-sha"
	seedPR(t, database, "acme", "widget", 1, withSeedPRHeadSHA(expectedHeadSHA))
	client := setupTestClient(t, srv)

	resp, err := client.HTTP.MergePullWithResponse(t.Context(), &generated.MergePullRequestOptions{PathParams: &generated.MergePullPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}, Body: &generated.MergePRInputBody{
		CommitTitle:     "title",
		CommitMessage:   "msg",
		Method:          "squash",
		ExpectedHeadSha: &expectedHeadSHA,
	}})
	require.Error(err)
	require.NotNil(resp)
	require.Equal(http.StatusBadGateway, resp.StatusCode)
	require.Contains(string(resp.Body), "connection refused")
}

func TestAPIMergePR422ForwardsGitHubMessage(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)

	mock := &mockGH{
		mergePullRequestFn: func(_ context.Context, _, _ string, _ int, _, _, _ string) (*gh.PullRequestMergeResult, error) {
			return nil, &gh.ErrorResponse{
				Response: &http.Response{StatusCode: http.StatusUnprocessableEntity},
				Message:  "Required status check is failing",
			}
		},
	}

	srv, database := setupTestServerWithMock(t, mock)
	expectedHeadSHA := "reviewed-sha"
	seedPR(t, database, "acme", "widget", 1, withSeedPRHeadSHA(expectedHeadSHA))
	client := setupTestClient(t, srv)

	resp, err := client.HTTP.MergePullWithResponse(t.Context(), &generated.MergePullRequestOptions{PathParams: &generated.MergePullPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}, Body: &generated.MergePRInputBody{
		CommitTitle:     "title",
		CommitMessage:   "msg",
		Method:          "squash",
		ExpectedHeadSha: &expectedHeadSHA,
	}})
	require.Error(err)
	require.NotNil(resp)
	require.Equal(http.StatusUnprocessableEntity, resp.StatusCode)
	require.Contains(string(resp.Body), "Required status check is failing")
}

func TestAPIMergePR403ForwardsGitHubMessage(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)

	mock := &mockGH{
		mergePullRequestFn: func(_ context.Context, _, _ string, _ int, _, _, _ string) (*gh.PullRequestMergeResult, error) {
			return nil, &gh.ErrorResponse{
				Response: &http.Response{StatusCode: http.StatusForbidden},
				Message:  "Resource not accessible by integration",
			}
		},
	}

	srv, database := setupTestServerWithMock(t, mock)
	expectedHeadSHA := "reviewed-sha"
	seedPR(t, database, "acme", "widget", 1, withSeedPRHeadSHA(expectedHeadSHA))
	client := setupTestClient(t, srv)

	resp, err := client.HTTP.MergePullWithResponse(t.Context(), &generated.MergePullRequestOptions{PathParams: &generated.MergePullPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}, Body: &generated.MergePRInputBody{
		CommitTitle:     "title",
		CommitMessage:   "msg",
		Method:          "squash",
		ExpectedHeadSha: &expectedHeadSHA,
	}})
	require.Error(err)
	require.NotNil(resp)
	require.Equal(http.StatusForbidden, resp.StatusCode)
	require.Contains(string(resp.Body), "Resource not accessible by integration")
}

func TestAPIMergePR5xxReturns502WithGitHubMessage(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)

	mock := &mockGH{
		mergePullRequestFn: func(_ context.Context, _, _ string, _ int, _, _, _ string) (*gh.PullRequestMergeResult, error) {
			return nil, &gh.ErrorResponse{
				Response: &http.Response{StatusCode: http.StatusServiceUnavailable},
				Message:  "Service unavailable",
			}
		},
	}

	srv, database := setupTestServerWithMock(t, mock)
	expectedHeadSHA := "reviewed-sha"
	seedPR(t, database, "acme", "widget", 1, withSeedPRHeadSHA(expectedHeadSHA))
	client := setupTestClient(t, srv)

	resp, err := client.HTTP.MergePullWithResponse(t.Context(), &generated.MergePullRequestOptions{PathParams: &generated.MergePullPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}, Body: &generated.MergePRInputBody{
		CommitTitle:     "title",
		CommitMessage:   "msg",
		Method:          "squash",
		ExpectedHeadSha: &expectedHeadSHA,
	}})
	require.Error(err)
	require.NotNil(resp)
	require.Equal(http.StatusBadGateway, resp.StatusCode)
	require.Contains(string(resp.Body), "Service unavailable")
}

func TestAPIMergePRForwardsGitHubErrorDetailsAndLogsError(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)

	logBuf := &lockedBuffer{}
	origLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(logBuf, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	})))
	t.Cleanup(func() { slog.SetDefault(origLogger) })

	mock := &mockGH{
		mergePullRequestFn: func(_ context.Context, _, _ string, _ int, _, _, _ string) (*gh.PullRequestMergeResult, error) {
			return nil, &gh.ErrorResponse{
				Response: &http.Response{StatusCode: http.StatusInternalServerError},
				Message:  "GitHub Server Error",
				Errors: []gh.Error{{
					Resource: "PullRequest",
					Field:    "merge",
					Code:     "custom",
					Message:  "Required status check \"build\" is failing",
				}},
			}
		},
	}

	srv, database := setupTestServerWithMock(t, mock)
	expectedHeadSHA := "reviewed-sha"
	seedPR(t, database, "acme", "widget", 1, withSeedPRHeadSHA(expectedHeadSHA))
	client := setupTestClient(t, srv)

	resp, err := client.HTTP.MergePullWithResponse(t.Context(), &generated.MergePullRequestOptions{PathParams: &generated.MergePullPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}, Body: &generated.MergePRInputBody{
		CommitTitle:     "title",
		CommitMessage:   "msg",
		Method:          "squash",
		ExpectedHeadSha: &expectedHeadSHA,
	}})
	require.Error(err)
	require.NotNil(resp)
	require.Equal(http.StatusBadGateway, resp.StatusCode)

	var body generated.ProblemError
	require.NoError(json.Unmarshal(resp.Body, &body))
	require.NotNil(body.Detail)
	assert.Contains(*body.Detail, "Required status check \"build\" is failing")
	assert.NotEqual("GitHub Server Error", *body.Detail)

	logText := logBuf.String()
	assert.Contains(logText, "level=ERROR")
	assert.Contains(logText, "provider merge failed")
	assert.Contains(logText, "Required status check")
}

func TestAPIMergePRStoresUTCTimestamps(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)

	// The provider reports the merge in a non-UTC zone; the canonical
	// post-merge resync must store it as UTC.
	providerNow := testEDTTime(8, 30)
	expectedHeadSHA := "reviewed-sha"
	mock := &mockGH{
		getPullRequestFn: func(
			context.Context, string, string, int,
		) (*gh.PullRequest, error) {
			return providerStatePR(
				1, "closed", time.Now().UTC().Add(time.Hour),
				&providerNow, &providerNow, expectedHeadSHA,
			), nil
		},
	}
	srv, database := setupTestServerWithMock(t, mock)
	seedPR(t, database, "acme", "widget", 1, withSeedPRHeadSHA(expectedHeadSHA))
	client := setupTestClient(t, srv)

	resp, err := client.HTTP.MergePullWithResponse(t.Context(), &generated.MergePullRequestOptions{PathParams: &generated.MergePullPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}, Body: &generated.MergePRInputBody{
		CommitTitle:     "title",
		CommitMessage:   "msg",
		Method:          "squash",
		ExpectedHeadSha: &expectedHeadSHA,
	}})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)

	pr, err := database.GetMergeRequest(t.Context(), "github", "github.com", "acme", "widget", 1)
	require.NoError(err)
	require.Equal(db.MergeRequestStateMerged, pr.State)
	assertTimePtrEqualsUTC(t, pr.MergedAt, providerNow)
	assertTimePtrEqualsUTC(t, pr.ClosedAt, providerNow)
}

func TestAPISyncPRPersistsMergeableState(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	now := time.Now().UTC().Truncate(time.Second)
	mergeableState := "dirty"
	mock := &mockGH{
		getPullRequestFn: func(_ context.Context, _ string, _ string, number int) (*gh.PullRequest, error) {
			id := int64(1001)
			sha := "abc123"
			state := "open"
			title := "Conflicted PR"
			url := "https://github.com/acme/widget/pull/1"
			updatedAt := gh.Timestamp{Time: now}
			createdAt := gh.Timestamp{Time: now}
			return &gh.PullRequest{
				ID:             &id,
				Number:         &number,
				State:          &state,
				Title:          &title,
				HTMLURL:        &url,
				UpdatedAt:      &updatedAt,
				CreatedAt:      &createdAt,
				MergeableState: &mergeableState,
				Head:           &gh.PullRequestBranch{SHA: &sha, Ref: new("feature")},
				Base:           &gh.PullRequestBranch{Ref: new("main")},
			}, nil
		},
	}

	srv, database := setupTestServerWithMock(t, mock)
	seedPR(t, database, "acme", "widget", 1, func(pr *db.MergeRequest) {
		pr.UpdatedAt = now.Add(-time.Second)
		pr.LastActivityAt = now.Add(-time.Second)
	})
	client := setupTestClient(t, srv)

	resp, err := client.HTTP.SyncPullWithResponse(t.Context(), &generated.SyncPullRequestOptions{PathParams: &generated.SyncPullPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode, string(resp.Body))
	require.NotNil(resp.JSON200)
	assert.Equal("dirty", resp.JSON200.MergeRequest.MergeableState)

	stored, err := database.GetMergeRequest(t.Context(), "github", "github.com", "acme", "widget", 1)
	require.NoError(err)
	require.NotNil(stored)
	assert.Equal("dirty", stored.MergeableState)
}

func TestAPISyncPRPersistsMergedActorInDetail(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Second)
	mergedAt := now.Add(time.Minute)
	mergedBy := "merge-admin"
	mock := &mockGH{
		getPullRequestFn: func(_ context.Context, _ string, _ string, number int) (*gh.PullRequest, error) {
			id := int64(1001)
			sha := "abc123"
			baseSHA := "def456"
			state := "closed"
			title := "Merged PR"
			url := "https://github.com/acme/widget/pull/1"
			updatedAt := gh.Timestamp{Time: mergedAt}
			createdAt := gh.Timestamp{Time: now}
			mergedAtTimestamp := gh.Timestamp{Time: mergedAt}
			merged := true
			return &gh.PullRequest{
				ID:        &id,
				Number:    &number,
				State:     &state,
				Title:     &title,
				HTMLURL:   &url,
				UpdatedAt: &updatedAt,
				CreatedAt: &createdAt,
				Merged:    &merged,
				MergedAt:  &mergedAtTimestamp,
				ClosedAt:  &mergedAtTimestamp,
				MergedBy:  &gh.User{Login: &mergedBy},
				Head:      &gh.PullRequestBranch{SHA: &sha, Ref: new("feature")},
				Base:      &gh.PullRequestBranch{SHA: &baseSHA, Ref: new("main")},
			}, nil
		},
	}

	srv, database := setupTestServerWithMock(t, mock)
	seedPR(t, database, "acme", "widget", 1)
	client := setupTestClient(t, srv)

	syncResp, err := client.HTTP.SyncPullWithResponse(ctx, &generated.SyncPullRequestOptions{PathParams: &generated.SyncPullPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}})
	require.NoError(err)
	require.Equal(http.StatusOK, syncResp.StatusCode, string(syncResp.Body))

	detailResp, err := client.HTTP.GetPullWithResponse(ctx, &generated.GetPullRequestOptions{PathParams: &generated.GetPullPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}})
	require.NoError(err)
	require.Equal(http.StatusOK, detailResp.StatusCode, string(detailResp.Body))
	require.NotNil(detailResp.JSON200)
	require.NotNil(detailResp.JSON200.Events)
	require.Len(detailResp.JSON200.Events, 1)
	event := detailResp.JSON200.Events[0]
	assert.Equal("merged", event.EventType)
	assert.Equal("merge-admin", event.Author)
	assert.Equal("merged this", event.Summary)
	assert.True(event.CreatedAt.Equal(mergedAt))
}

func TestAPIIndexSyncPersistsMergedActorForImmediateDetail(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Second)
	mergedAt := now.Add(time.Minute)
	mergedBy := "merge-admin"
	var getPullCalls atomic.Int32
	mock := &mockGH{
		listOpenPullRequestsFn: func(_ context.Context, _, _ string) ([]*gh.PullRequest, error) {
			number := 1
			id := int64(1001)
			sha := "abc123"
			baseSHA := "def456"
			headRef := "feature"
			baseRef := "main"
			state := "closed"
			title := "Merged PR"
			url := "https://github.com/acme/widget/pull/1"
			updatedAt := gh.Timestamp{Time: mergedAt}
			createdAt := gh.Timestamp{Time: now}
			mergedAtTimestamp := gh.Timestamp{Time: mergedAt}
			merged := true
			return []*gh.PullRequest{{
				ID:        &id,
				Number:    &number,
				State:     &state,
				Title:     &title,
				HTMLURL:   &url,
				UpdatedAt: &updatedAt,
				CreatedAt: &createdAt,
				Merged:    &merged,
				MergedAt:  &mergedAtTimestamp,
				ClosedAt:  &mergedAtTimestamp,
				MergedBy:  &gh.User{Login: &mergedBy},
				Head:      &gh.PullRequestBranch{SHA: &sha, Ref: &headRef},
				Base:      &gh.PullRequestBranch{SHA: &baseSHA, Ref: &baseRef},
			}}, nil
		},
		getPullRequestFn: func(_ context.Context, _ string, _ string, _ int) (*gh.PullRequest, error) {
			getPullCalls.Add(1)
			return nil, errors.New("detail lookup should not run")
		},
	}

	srv, database := setupTestServerWithMock(t, mock)
	client := setupTestClient(t, srv)

	syncResp, err := client.HTTP.TriggerSyncWithResponse(ctx, &generated.TriggerSyncRequestOptions{})
	require.NoError(err)
	require.Equal(http.StatusAccepted, syncResp.StatusCode, string(syncResp.Body))

	require.Eventually(func() bool {
		mr, mrErr := database.GetMergeRequest(ctx, "github", "github.com", "acme", "widget", 1)
		if mrErr != nil || mr == nil {
			return false
		}
		events, eventsErr := database.ListMREvents(ctx, mr.ID)
		if eventsErr != nil {
			return false
		}
		for _, event := range events {
			if event.EventType == "merged" && event.Author == "merge-admin" {
				return true
			}
		}
		return false
	}, 3*time.Second, 25*time.Millisecond)

	detailResp, err := client.HTTP.GetPullWithResponse(ctx, &generated.GetPullRequestOptions{PathParams: &generated.GetPullPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}})
	require.NoError(err)
	require.Equal(http.StatusOK, detailResp.StatusCode, string(detailResp.Body))
	require.NotNil(detailResp.JSON200)
	require.NotNil(detailResp.JSON200.Events)
	require.Len(detailResp.JSON200.Events, 1)
	event := detailResp.JSON200.Events[0]
	assert.Equal("merged", event.EventType)
	assert.Equal("merge-admin", event.Author)
	assert.Equal("merged this", event.Summary)
	assert.True(event.CreatedAt.Equal(mergedAt))
	assert.Equal(int32(0), getPullCalls.Load())
}

func TestAPIGetPullDoesNotFetchProviderForMissingMergedActor(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Second)
	mergedAt := now.Add(time.Minute)
	var getPullCalls atomic.Int32
	var timelineCalls atomic.Int32
	mock := &mockGH{
		getPullRequestFn: func(_ context.Context, _ string, _ string, _ int) (*gh.PullRequest, error) {
			getPullCalls.Add(1)
			return nil, errors.New("detail reads must not fetch pull request data")
		},
		listPRTimelineEventsFn: func(_ context.Context, _ string, _ string, _ int) ([]platformgithub.PullRequestTimelineEvent, error) {
			timelineCalls.Add(1)
			return nil, errors.New("detail reads must not fetch timeline data")
		},
	}

	srv, database := setupTestServerWithMock(t, mock)
	seedPR(t, database, "acme", "widget", 1,
		withSeedPRLifecycle(db.MergeRequestStateMerged, &mergedAt, &mergedAt),
	)
	client := setupTestClient(t, srv)

	detailResp, err := client.HTTP.GetPullWithResponse(ctx, &generated.GetPullRequestOptions{PathParams: &generated.GetPullPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}})
	require.NoError(err)
	require.Equal(http.StatusOK, detailResp.StatusCode, string(detailResp.Body))
	require.NotNil(detailResp.JSON200)
	require.NotNil(detailResp.JSON200.Events)
	require.Len(detailResp.JSON200.Events, 1)
	event := detailResp.JSON200.Events[0]
	assert.Equal("merged", event.EventType)
	assert.Empty(event.Author)
	assert.Equal("merged this", event.Summary)
	assert.True(event.CreatedAt.Equal(mergedAt))
	assert.Equal(int32(0), getPullCalls.Load())
	assert.Equal(int32(0), timelineCalls.Load())
}

func TestAPISyncPRPreservesMergeableStateWhenRefreshHasNoAnswer(t *testing.T) {
	runParallelServerTest(t)
	tests := []struct {
		name  string
		state *string
	}{
		{name: "omitted", state: nil},
		{name: "unknown", state: new("unknown")},
	}
	now := time.Now().UTC().Truncate(time.Second)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require := require.New(t)
			assert := assert.New(t)
			mock := &mockGH{
				getPullRequestFn: func(_ context.Context, _ string, _ string, number int) (*gh.PullRequest, error) {
					id := int64(1001)
					sha := "abc123"
					baseSHA := "def456"
					state := "open"
					title := "Conflicted PR"
					url := "https://github.com/acme/widget/pull/1"
					updatedAt := gh.Timestamp{Time: now}
					createdAt := gh.Timestamp{Time: now}
					return &gh.PullRequest{
						ID:             &id,
						Number:         &number,
						State:          &state,
						Title:          &title,
						HTMLURL:        &url,
						UpdatedAt:      &updatedAt,
						CreatedAt:      &createdAt,
						MergeableState: tt.state,
						Head:           &gh.PullRequestBranch{SHA: &sha, Ref: new("feature")},
						Base:           &gh.PullRequestBranch{Ref: new("main"), SHA: &baseSHA},
					}, nil
				},
			}

			srv, database := setupTestServerWithMock(t, mock)
			seedPR(t, database, "acme", "widget", 1, func(pr *db.MergeRequest) {
				pr.MergeableState = "dirty"
			}, withSeedPRHeadSHA("abc123"), withSeedPRBaseSHA("def456"))
			client := setupTestClient(t, srv)

			resp, err := client.HTTP.SyncPullWithResponse(t.Context(), &generated.SyncPullRequestOptions{PathParams: &generated.SyncPullPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}})
			require.NoError(err)
			require.Equal(http.StatusOK, resp.StatusCode, string(resp.Body))
			require.NotNil(resp.JSON200)
			assert.Equal("dirty", resp.JSON200.MergeRequest.MergeableState)

			stored, err := database.GetMergeRequest(t.Context(), "github", "github.com", "acme", "widget", 1)
			require.NoError(err)
			require.NotNil(stored)
			assert.Equal("dirty", stored.MergeableState)
		})
	}
}

func TestAPIApproveWorkflows(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	approvedRunIDs := []int64{}
	mock := &mockGH{
		getPullRequestFn: func(_ context.Context, owner, repo string, number int) (*gh.PullRequest, error) {
			id := int64(1001)
			sha := "abc123"
			state := "open"
			title := "Workflow PR"
			url := "https://github.com/acme/widget/pull/1"
			updatedAt := gh.Timestamp{Time: time.Now().UTC()}
			createdAt := gh.Timestamp{Time: time.Now().UTC()}
			return &gh.PullRequest{
				ID:        &id,
				Number:    &number,
				State:     &state,
				Title:     &title,
				HTMLURL:   &url,
				UpdatedAt: &updatedAt,
				CreatedAt: &createdAt,
				Head:      &gh.PullRequestBranch{SHA: &sha, Ref: new("feature")},
				Base:      &gh.PullRequestBranch{Ref: new("main")},
			}, nil
		},
		listWorkflowRunsForHeadFn: func(_ context.Context, _, _, headSHA string) ([]*gh.WorkflowRun, error) {
			require.Equal("abc123", headSHA)
			return []*gh.WorkflowRun{
				{
					ID:           new(int64(81)),
					HeadSHA:      new("abc123"),
					Event:        new("pull_request"),
					PullRequests: []*gh.PullRequest{{Number: new(1)}},
				},
				{
					ID:           new(int64(82)),
					HeadSHA:      new("abc123"),
					Event:        new("pull_request"),
					PullRequests: []*gh.PullRequest{{Number: new(1)}},
				},
				{
					ID:           new(int64(99)),
					HeadSHA:      new("zzz999"),
					Event:        new("pull_request"),
					PullRequests: []*gh.PullRequest{{Number: new(1)}},
				},
			}, nil
		},
		approveWorkflowRunFn: func(_ context.Context, owner, repo string, runID int64) error {
			require.Equal("acme", owner)
			require.Equal("widget", repo)
			approvedRunIDs = append(approvedRunIDs, runID)
			return nil
		},
	}

	srv, database := setupTestServerWithMock(t, mock)
	seedPR(t, database, "acme", "widget", 1)
	repo, err := database.GetRepoByIdentity(t.Context(), verifiedGitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	require.NotNil(repo)
	require.NoError(database.UpdateMRWorkflowApproval(
		t.Context(), repo.ID, 1, time.Now().UTC(), "abc123", true, 2,
	))
	client := setupTestClient(t, srv)

	resp, err := client.HTTP.ApprovePullWorkflowsWithResponse(t.Context(), &generated.ApprovePullWorkflowsRequestOptions{PathParams: &generated.ApprovePullWorkflowsPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)
	require.NotNil(resp.JSON200)
	require.NotNil(resp.JSON200.ApprovedCount)
	assert.Equal("approved_workflows", resp.JSON200.Status)
	assert.EqualValues(2, *resp.JSON200.ApprovedCount)
	assert.Equal([]int64{81, 82}, approvedRunIDs)

	pr, err := database.GetMergeRequest(t.Context(), "github", "github.com", "acme", "widget", 1)
	require.NoError(err)
	require.NotNil(pr)
	assert.Equal("abc123", pr.PlatformHeadSHA)
	require.NotNil(pr.WorkflowApprovalCheckedAt)
	assert.Equal("abc123", pr.WorkflowApprovalHeadSHA)
	assert.False(pr.WorkflowApprovalRequired)
	assert.Equal(0, pr.WorkflowApprovalCount)
}

func TestAPIApproveWorkflowsZeroMatchesStillSyncsPR(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	mock := &mockGH{
		getPullRequestFn: func(_ context.Context, _, _ string, number int) (*gh.PullRequest, error) {
			id := int64(1002)
			sha := "abc123"
			state := "open"
			title := "Workflow PR"
			url := "https://github.com/acme/widget/pull/1"
			updatedAt := gh.Timestamp{Time: time.Now().UTC()}
			createdAt := gh.Timestamp{Time: time.Now().UTC()}
			return &gh.PullRequest{
				ID:        &id,
				Number:    &number,
				State:     &state,
				Title:     &title,
				HTMLURL:   &url,
				UpdatedAt: &updatedAt,
				CreatedAt: &createdAt,
				Head:      &gh.PullRequestBranch{SHA: &sha, Ref: new("feature")},
				Base:      &gh.PullRequestBranch{Ref: new("main")},
			}, nil
		},
		listWorkflowRunsForHeadFn: func(_ context.Context, _, _, headSHA string) ([]*gh.WorkflowRun, error) {
			require.Equal("abc123", headSHA)
			return nil, nil
		},
	}

	srv, database := setupTestServerWithMock(t, mock)
	seedPR(t, database, "acme", "widget", 1)
	client := setupTestClient(t, srv)

	resp, err := client.HTTP.ApprovePullWorkflowsWithResponse(t.Context(), &generated.ApprovePullWorkflowsRequestOptions{PathParams: &generated.ApprovePullWorkflowsPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)
	require.NotNil(resp.JSON200)
	assert.Equal("approved_workflows", resp.JSON200.Status)

	pr, err := database.GetMergeRequest(t.Context(), "github", "github.com", "acme", "widget", 1)
	require.NoError(err)
	require.NotNil(pr)
	assert.Equal("abc123", pr.PlatformHeadSHA)
}

func TestAPIApproveWorkflowsReturnsUnderlyingApprovalErrorAfterPartialFailure(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	approvedRunIDs := []int64{}
	mock := &mockGH{
		getPullRequestFn: func(_ context.Context, _, _ string, number int) (*gh.PullRequest, error) {
			id := int64(1003)
			sha := "abc123"
			state := "open"
			title := "Workflow PR"
			url := "https://github.com/acme/widget/pull/1"
			updatedAt := gh.Timestamp{Time: time.Now().UTC()}
			createdAt := gh.Timestamp{Time: time.Now().UTC()}
			return &gh.PullRequest{
				ID:        &id,
				Number:    &number,
				State:     &state,
				Title:     &title,
				HTMLURL:   &url,
				UpdatedAt: &updatedAt,
				CreatedAt: &createdAt,
				Head:      &gh.PullRequestBranch{SHA: &sha, Ref: new("feature")},
				Base:      &gh.PullRequestBranch{Ref: new("main")},
			}, nil
		},
		listWorkflowRunsForHeadFn: func(_ context.Context, _, _, headSHA string) ([]*gh.WorkflowRun, error) {
			require.Equal("abc123", headSHA)
			return []*gh.WorkflowRun{
				{
					ID:           new(int64(91)),
					HeadSHA:      new("abc123"),
					Event:        new("pull_request"),
					PullRequests: []*gh.PullRequest{{Number: new(1)}},
				},
				{
					ID:           new(int64(92)),
					HeadSHA:      new("abc123"),
					Event:        new("pull_request"),
					PullRequests: []*gh.PullRequest{{Number: new(1)}},
				},
			}, nil
		},
		approveWorkflowRunFn: func(_ context.Context, _, _ string, runID int64) error {
			approvedRunIDs = append(approvedRunIDs, runID)
			if runID == 92 {
				return fmt.Errorf("permission denied")
			}
			return nil
		},
	}

	srv, database := setupTestServerWithMock(t, mock)
	seedPR(t, database, "acme", "widget", 1)
	client := setupTestClient(t, srv)

	resp, err := client.HTTP.ApprovePullWorkflowsWithResponse(t.Context(), &generated.ApprovePullWorkflowsRequestOptions{PathParams: &generated.ApprovePullWorkflowsPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}})
	require.Error(err)
	require.NotNil(resp)
	require.Equal(http.StatusBadGateway, resp.StatusCode)
	require.NotNil(resp.Error)
	require.NotNil(resp.Error.Detail)
	assert.Contains(*resp.Error.Detail, "permission denied")
	assert.Equal([]int64{91, 92}, approvedRunIDs)

	pr, err := database.GetMergeRequest(t.Context(), "github", "github.com", "acme", "widget", 1)
	require.NoError(err)
	require.NotNil(pr)
	assert.Equal("abc123", pr.PlatformHeadSHA)
}

// TestAPIApproveWorkflowsForForkPR verifies the approve endpoint reaches
// ApproveWorkflowRun for a fork-triggered run when the run's head repo and
// branch match the PR.
func TestAPIApproveWorkflowsForForkPR(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	approvedRunIDs := []int64{}
	mock := &mockGH{
		getPullRequestFn: func(_ context.Context, _, _ string, number int) (*gh.PullRequest, error) {
			id := int64(2002)
			sha := "forkhead"
			state := "open"
			title := "Fork PR"
			url := "https://github.com/acme/widget/pull/1"
			cloneURL := "https://github.com/fork/widget.git"
			updatedAt := gh.Timestamp{Time: time.Now().UTC()}
			createdAt := gh.Timestamp{Time: time.Now().UTC()}
			return &gh.PullRequest{
				ID:        &id,
				Number:    &number,
				State:     &state,
				Title:     &title,
				HTMLURL:   &url,
				UpdatedAt: &updatedAt,
				CreatedAt: &createdAt,
				Head: &gh.PullRequestBranch{
					SHA:  &sha,
					Ref:  new("feature"),
					Repo: &gh.Repository{CloneURL: &cloneURL, FullName: new("fork/widget")},
				},
				Base: &gh.PullRequestBranch{Ref: new("main")},
			}, nil
		},
		listWorkflowRunsForHeadFn: func(_ context.Context, _, _, headSHA string) ([]*gh.WorkflowRun, error) {
			require.Equal("forkhead", headSHA)
			return []*gh.WorkflowRun{
				{
					ID:             new(int64(71)),
					HeadSHA:        new("forkhead"),
					Event:          new("pull_request"),
					HeadBranch:     new("feature"),
					HeadRepository: &gh.Repository{FullName: new("fork/widget")},
				},
			}, nil
		},
		approveWorkflowRunFn: func(_ context.Context, _, _ string, runID int64) error {
			approvedRunIDs = append(approvedRunIDs, runID)
			return nil
		},
	}

	srv, database := setupTestServerWithMock(t, mock)
	seedPR(t, database, "acme", "widget", 1)
	client := setupTestClient(t, srv)

	resp, err := client.HTTP.ApprovePullWorkflowsWithResponse(t.Context(), &generated.ApprovePullWorkflowsRequestOptions{PathParams: &generated.ApprovePullWorkflowsPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)
	require.NotNil(resp.JSON200)
	require.NotNil(resp.JSON200.ApprovedCount)
	assert.Equal("approved_workflows", resp.JSON200.Status)
	assert.EqualValues(1, *resp.JSON200.ApprovedCount)
	assert.Equal([]int64{71}, approvedRunIDs)
}

// TestAPIApproveWorkflowsIgnoresRunsForOtherPRAtSameSHA verifies the approve
// endpoint does not call ApproveWorkflowRun for runs whose pull_requests
// association points at a different PR sharing the same head SHA.
func TestAPIApproveWorkflowsIgnoresRunsForOtherPRAtSameSHA(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	approvedRunIDs := []int64{}
	mock := &mockGH{
		getPullRequestFn: func(_ context.Context, _, _ string, number int) (*gh.PullRequest, error) {
			id := int64(3002)
			sha := "sharedsha"
			state := "open"
			title := "Shared SHA PR"
			url := "https://github.com/acme/widget/pull/1"
			updatedAt := gh.Timestamp{Time: time.Now().UTC()}
			createdAt := gh.Timestamp{Time: time.Now().UTC()}
			return &gh.PullRequest{
				ID:        &id,
				Number:    &number,
				State:     &state,
				Title:     &title,
				HTMLURL:   &url,
				UpdatedAt: &updatedAt,
				CreatedAt: &createdAt,
				Head:      &gh.PullRequestBranch{SHA: &sha, Ref: new("feature")},
				Base:      &gh.PullRequestBranch{Ref: new("main")},
			}, nil
		},
		listWorkflowRunsForHeadFn: func(_ context.Context, _, _, headSHA string) ([]*gh.WorkflowRun, error) {
			require.Equal("sharedsha", headSHA)
			return []*gh.WorkflowRun{
				{
					ID:           new(int64(88)),
					HeadSHA:      new("sharedsha"),
					Event:        new("pull_request"),
					PullRequests: []*gh.PullRequest{{Number: new(99)}},
				},
				{
					ID:           new(int64(89)),
					HeadSHA:      new("sharedsha"),
					Event:        new("pull_request"),
					PullRequests: []*gh.PullRequest{{Number: new(1)}},
				},
			}, nil
		},
		approveWorkflowRunFn: func(_ context.Context, _, _ string, runID int64) error {
			approvedRunIDs = append(approvedRunIDs, runID)
			return nil
		},
	}

	srv, database := setupTestServerWithMock(t, mock)
	seedPR(t, database, "acme", "widget", 1)
	client := setupTestClient(t, srv)

	resp, err := client.HTTP.ApprovePullWorkflowsWithResponse(t.Context(), &generated.ApprovePullWorkflowsRequestOptions{PathParams: &generated.ApprovePullWorkflowsPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)
	require.NotNil(resp.JSON200)
	require.NotNil(resp.JSON200.ApprovedCount)
	assert.EqualValues(1, *resp.JSON200.ApprovedCount)
	assert.Equal([]int64{89}, approvedRunIDs)
}

// TestAPIApproveWorkflowsRejectsRunFromDifferentForkAtSameSHA exercises the
// safety guarantee that two distinct forks sharing a head SHA do not
// cross-approve. The PR's head repo is alice/widget; the run's head repo is
// bob/widget. ApproveWorkflowRun must not be called.
func TestAPIApproveWorkflowsRejectsRunFromDifferentForkAtSameSHA(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	approvedRunIDs := []int64{}
	mock := &mockGH{
		getPullRequestFn: func(_ context.Context, _, _ string, number int) (*gh.PullRequest, error) {
			id := int64(4001)
			sha := "sharedsha"
			state := "open"
			title := "Alice Fork PR"
			url := "https://github.com/acme/widget/pull/1"
			cloneURL := "https://github.com/alice/widget.git"
			updatedAt := gh.Timestamp{Time: time.Now().UTC()}
			createdAt := gh.Timestamp{Time: time.Now().UTC()}
			return &gh.PullRequest{
				ID:        &id,
				Number:    &number,
				State:     &state,
				Title:     &title,
				HTMLURL:   &url,
				UpdatedAt: &updatedAt,
				CreatedAt: &createdAt,
				Head: &gh.PullRequestBranch{
					SHA:  &sha,
					Ref:  new("feature"),
					Repo: &gh.Repository{CloneURL: &cloneURL, FullName: new("alice/widget")},
				},
				Base: &gh.PullRequestBranch{Ref: new("main")},
			}, nil
		},
		listWorkflowRunsForHeadFn: func(_ context.Context, _, _, headSHA string) ([]*gh.WorkflowRun, error) {
			require.Equal("sharedsha", headSHA)
			return []*gh.WorkflowRun{
				{
					ID:             new(int64(123)),
					HeadSHA:        new("sharedsha"),
					Event:          new("pull_request"),
					HeadBranch:     new("feature"),
					HeadRepository: &gh.Repository{FullName: new("bob/widget")},
				},
			}, nil
		},
		approveWorkflowRunFn: func(_ context.Context, _, _ string, runID int64) error {
			approvedRunIDs = append(approvedRunIDs, runID)
			return nil
		},
	}

	srv, database := setupTestServerWithMock(t, mock)
	seedPR(t, database, "acme", "widget", 1)
	client := setupTestClient(t, srv)

	resp, err := client.HTTP.ApprovePullWorkflowsWithResponse(t.Context(), &generated.ApprovePullWorkflowsRequestOptions{PathParams: &generated.ApprovePullWorkflowsPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)
	require.NotNil(resp.JSON200)
	assert.Empty(approvedRunIDs)
}

// TestAPIGetPullEmitsStaleDiffWarningOnMergedPR pins the staleness
// branch for merged PRs. getDiff treats merged PRs as stale when the
// recorded DiffHeadSHA no longer matches PlatformHeadSHA, so the
// warning must fire in the same case. Without this coverage a merged
// PR with a stale recorded diff would render outdated content with no
// indication.
func TestAPIGetPullEmitsStaleDiffWarningOnMergedPR(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	ctx := t.Context()

	database := dbtest.Open(t)

	clones := gitclone.New(t.TempDir(), nil)
	syncer := ghclient.NewSyncer(
		map[string]ghclient.Client{"github.com": &mockGH{}},
		database, clones, defaultTestRepos, time.Minute, nil, nil,
	)
	t.Cleanup(syncer.Stop)
	srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{})

	seedPR(t, database, "acme", "widget", 5)

	repoID, err := database.UpsertRepo(ctx, verifiedGitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	now := time.Now().UTC().Truncate(time.Second)
	mergedAt := now
	require.NoError(database.UpdateClosedMRState(
		ctx, repoID, 5, "merged", now, &mergedAt, &mergedAt,
		"deadbeef00000000000000000000000000000099",
		"deadbeef00000000000000000000000000000010",
	))
	// Recorded diff was computed against an earlier head; the merge
	// commit advanced the platform head past it.
	require.NoError(database.UpdateDiffSHAs(
		ctx, repoID, 5,
		"deadbeef00000000000000000000000000000001",
		"deadbeef00000000000000000000000000000010",
		"deadbeef00000000000000000000000000000003",
	))

	client := setupTestClient(t, srv)
	resp, err := client.HTTP.GetPullWithResponse(t.Context(), &generated.GetPullRequestOptions{PathParams: &generated.GetPullPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(5)}})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)
	require.NotNil(resp.JSON200)
	require.NotNil(resp.JSON200.Warnings, "warnings field should be set when merged diff is stale")
	warnings := resp.JSON200.Warnings
	require.Len(warnings, 1)
	assert.Contains(warnings[0], "out of date")
}

// TestAPIGetPullEmitsDiffWarningWhenSHAsMissingClosed covers a closed
// (not merged) PR whose fetchAndUpdateClosed path failed to populate
// diff SHAs - for example because the clone fetch errored out. The
// previous diffWarnings implementation suppressed warnings for any
// non-open/non-merged state and the user would silently see no diff.
func TestAPIGetPullEmitsDiffWarningWhenSHAsMissingClosed(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	ctx := t.Context()

	database := dbtest.Open(t)

	clones := gitclone.New(t.TempDir(), nil)
	syncer := ghclient.NewSyncer(
		map[string]ghclient.Client{"github.com": &mockGH{}},
		database, clones, defaultTestRepos, time.Minute, nil, nil,
	)
	t.Cleanup(syncer.Stop)
	srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{})

	seedPR(t, database, "acme", "widget", 6)

	repoID, err := database.UpsertRepo(ctx, verifiedGitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	now := time.Now().UTC().Truncate(time.Second)
	closedAt := now
	require.NoError(database.UpdateClosedMRState(
		ctx, repoID, 6, "closed", now, nil, &closedAt,
		"deadbeef00000000000000000000000000000001",
		"deadbeef00000000000000000000000000000010",
	))
	// Diff SHAs intentionally left empty to simulate a closed PR whose
	// diff sync errored out.

	client := setupTestClient(t, srv)
	resp, err := client.HTTP.GetPullWithResponse(t.Context(), &generated.GetPullRequestOptions{PathParams: &generated.GetPullPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(6)}})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)
	require.NotNil(resp.JSON200)
	require.NotNil(resp.JSON200.Warnings, "warnings field should be set when closed PR diff is missing")
	warnings := resp.JSON200.Warnings
	require.Len(warnings, 1)
	assert.Contains(warnings[0], "unavailable")
}

// TestAPIGetPullEmitsStaleDiffWarningOnClosedPR covers a closed (not
// merged) PR whose head or base advanced after the diff sync recorded
// SHAs. getDiff treats this as stale; diffWarnings must agree so the
// detail page shows a warning instead of silently rendering an old
// diff.
func TestAPIGetPullEmitsStaleDiffWarningOnClosedPR(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	ctx := t.Context()

	database := dbtest.Open(t)

	clones := gitclone.New(t.TempDir(), nil)
	syncer := ghclient.NewSyncer(
		map[string]ghclient.Client{"github.com": &mockGH{}},
		database, clones, defaultTestRepos, time.Minute, nil, nil,
	)
	t.Cleanup(syncer.Stop)
	srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{})

	seedPR(t, database, "acme", "widget", 7)

	repoID, err := database.UpsertRepo(ctx, verifiedGitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	now := time.Now().UTC().Truncate(time.Second)
	closedAt := now
	require.NoError(database.UpdateClosedMRState(
		ctx, repoID, 7, "closed", now, nil, &closedAt,
		"deadbeef00000000000000000000000000000099",
		"deadbeef00000000000000000000000000000010",
	))
	require.NoError(database.UpdateDiffSHAs(
		ctx, repoID, 7,
		"deadbeef00000000000000000000000000000001",
		"deadbeef00000000000000000000000000000010",
		"deadbeef00000000000000000000000000000003",
	))

	client := setupTestClient(t, srv)
	resp, err := client.HTTP.GetPullWithResponse(t.Context(), &generated.GetPullRequestOptions{PathParams: &generated.GetPullPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(7)}})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)
	require.NotNil(resp.JSON200)
	require.NotNil(resp.JSON200.Warnings, "warnings field should be set when closed PR diff is stale")
	warnings := resp.JSON200.Warnings
	require.Len(warnings, 1)
	assert.Contains(warnings[0], "out of date")
}

// TestAPIGetPullNoDiffWarningOnMergedPRWithBaseDrift pins the
// asymmetry between merged and open/closed staleness: merged PRs only
// care about head SHA drift because the base never advances after
// merge. A merged PR whose head matches but base differs must NOT
// emit a warning.
func TestAPIGetPullNoDiffWarningOnMergedPRWithBaseDrift(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	ctx := t.Context()

	database := dbtest.Open(t)

	clones := gitclone.New(t.TempDir(), nil)
	syncer := ghclient.NewSyncer(
		map[string]ghclient.Client{"github.com": &mockGH{}},
		database, clones, defaultTestRepos, time.Minute, nil, nil,
	)
	t.Cleanup(syncer.Stop)
	srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{})

	seedPR(t, database, "acme", "widget", 8)

	repoID, err := database.UpsertRepo(ctx, verifiedGitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	now := time.Now().UTC().Truncate(time.Second)
	mergedAt := now
	headSHA := "deadbeef00000000000000000000000000000001"
	require.NoError(database.UpdateClosedMRState(
		ctx, repoID, 8, "merged", now, &mergedAt, &mergedAt,
		headSHA,
		"deadbeef00000000000000000000000000000099",
	))
	require.NoError(database.UpdateDiffSHAs(
		ctx, repoID, 8,
		headSHA,
		"deadbeef00000000000000000000000000000010",
		"deadbeef00000000000000000000000000000003",
	))

	client := setupTestClient(t, srv)
	resp, err := client.HTTP.GetPullWithResponse(t.Context(), &generated.GetPullRequestOptions{PathParams: &generated.GetPullPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(8)}})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)
	require.NotNil(resp.JSON200)
	if resp.JSON200.Warnings != nil {
		assert.Empty(resp.JSON200.Warnings)
	}
}

func TestAPIGitLabDirectSyncPersistsMergedActorForImmediateDetail(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Second)
	mergedAt := now.Add(time.Minute)

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
	openMR := platform.MergeRequest{
		Repo:               ref,
		PlatformID:         7001,
		PlatformExternalID: "gid://gitlab/MergeRequest/7001",
		Number:             7,
		URL:                "https://gitlab.example.com/group/project/-/merge_requests/7",
		Title:              "GitLab provider MR",
		Author:             "ada",
		State:              "open",
		HeadBranch:         "feature/gitlab",
		BaseBranch:         "main",
		HeadSHA:            "abc123",
		BaseSHA:            "def456",
		CreatedAt:          now,
		UpdatedAt:          now,
		LastActivityAt:     now,
	}
	mergedMR := openMR
	mergedMR.State = "merged"
	mergedMR.MergedAt = &mergedAt
	mergedMR.ClosedAt = &mergedAt
	mergedMR.MergedBy = "merge-admin"
	mergedMR.UpdatedAt = mergedAt
	mergedMR.LastActivityAt = mergedAt
	provider := &apiTestGitLabProvider{
		ref:                ref,
		mergeRequests:      []platform.MergeRequest{openMR},
		mergeRequestDetail: map[int]platform.MergeRequest{7: mergedMR},
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

	syncResp, err := client.HTTP.SyncPullOnHostWithResponse(ctx, &generated.SyncPullOnHostRequestOptions{PathParams: &generated.SyncPullOnHostPath{PlatformHost: ref.Host, Provider: "gitlab", Owner: ref.Owner, Name: ref.Name, Number: int64(7)}})
	require.NoError(err)
	require.Equal(http.StatusOK, syncResp.StatusCode, string(syncResp.Body))
	require.NotNil(syncResp.JSON200)
	require.NotNil(syncResp.JSON200.Events)
	require.Len(syncResp.JSON200.Events, 1)
	event := syncResp.JSON200.Events[0]
	assert.Equal("merged", event.EventType)
	assert.Equal("merge-admin", event.Author)
	assert.Equal("merged this", event.Summary)
	assert.True(event.CreatedAt.Equal(mergedAt))

	detailResp, err := client.HTTP.GetPullOnHostWithResponse(ctx, &generated.GetPullOnHostRequestOptions{PathParams: &generated.GetPullOnHostPath{PlatformHost: ref.Host, Provider: "gitlab", Owner: ref.Owner, Name: ref.Name, Number: int64(7)}})
	require.NoError(err)
	require.Equal(http.StatusOK, detailResp.StatusCode, string(detailResp.Body))
	require.NotNil(detailResp.JSON200)
	require.NotNil(detailResp.JSON200.Events)
	require.Len(detailResp.JSON200.Events, 1)
	assert.Equal("merge-admin", detailResp.JSON200.Events[0].Author)
}

func TestAPIGitLabDirectSyncDoesNotDuplicateMergedActorAfterClosedFallback(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Second)
	mergedAt := now.Add(time.Minute)

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
	openMR := platform.MergeRequest{
		Repo:               ref,
		PlatformID:         7001,
		PlatformExternalID: "gid://gitlab/MergeRequest/7001",
		Number:             7,
		URL:                "https://gitlab.example.com/group/project/-/merge_requests/7",
		Title:              "GitLab provider MR",
		Author:             "ada",
		State:              "open",
		HeadBranch:         "feature/gitlab",
		BaseBranch:         "main",
		HeadSHA:            "abc123",
		BaseSHA:            "def456",
		CreatedAt:          now,
		UpdatedAt:          now,
		LastActivityAt:     now,
	}
	mergedMR := openMR
	mergedMR.State = "merged"
	mergedMR.MergedAt = &mergedAt
	mergedMR.ClosedAt = &mergedAt
	mergedMR.MergedBy = "merge-admin"
	mergedMR.UpdatedAt = mergedAt
	mergedMR.LastActivityAt = mergedAt
	provider := &apiTestGitLabProvider{
		ref:                ref,
		mergeRequests:      []platform.MergeRequest{openMR},
		mergeRequestDetail: map[int]platform.MergeRequest{7: mergedMR},
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
	provider.mergeRequests = nil
	syncer.RunOnce(ctx)

	stored, err := database.GetMergeRequest(
		ctx, string(ref.Platform), ref.Host, ref.Owner, ref.Name, 7,
	)
	require.NoError(err)
	require.NotNil(stored)
	events, err := database.ListMREvents(ctx, stored.ID)
	require.NoError(err)
	require.Len(events, 1)
	require.Equal("merged", events[0].EventType)
	require.Equal("merge-admin", events[0].Author)

	provider.mergeRequestEvents = map[int][]platform.MergeRequestEvent{7: {{
		Repo:               ref,
		PlatformID:         9001,
		PlatformExternalID: "gid://gitlab/Note/9001",
		MergeRequestNumber: 7,
		EventType:          "merged",
		Author:             "merge-admin",
		Summary:            "merged this",
		CreatedAt:          mergedAt,
		DedupeKey:          "gitlab:merged-note:9001",
	}}}
	syncResp, err := client.HTTP.SyncPullOnHostWithResponse(ctx, &generated.SyncPullOnHostRequestOptions{PathParams: &generated.SyncPullOnHostPath{PlatformHost: ref.Host, Provider: "gitlab", Owner: ref.Owner, Name: ref.Name, Number: int64(7)}})
	require.NoError(err)
	require.Equal(http.StatusOK, syncResp.StatusCode, string(syncResp.Body))
	require.NotNil(syncResp.JSON200)
	require.NotNil(syncResp.JSON200.Events)
	require.Len(syncResp.JSON200.Events, 1)
	assert.Equal("merged", syncResp.JSON200.Events[0].EventType)
	assert.Equal("merge-admin", syncResp.JSON200.Events[0].Author)

	events, err = database.ListMREvents(ctx, stored.ID)
	require.NoError(err)
	require.Len(events, 1)
	assert.Equal("merged", events[0].EventType)
	assert.Equal("merge-admin", events[0].Author)
	assert.Equal("merged this", events[0].Summary)
}

func TestAPIPostPRCommentRejectsNilProviderPayload(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)

	mock := &mockGH{
		createIssueCommentFn: func(context.Context, string, string, int, string) (*gh.IssueComment, error) {
			return nil, nil
		},
	}
	srv, database := setupTestServerWithMock(t, mock)
	mrID := seedPR(t, database, "acme", "widget", 1)
	client := setupTestClient(t, srv)

	resp, err := client.HTTP.PostPrCommentWithResponse(t.Context(), &generated.PostPrCommentRequestOptions{PathParams: &generated.PostPrCommentPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}, Body: &generated.PostPrCommentBody{Body: "Looks good"}})
	require.Error(err)
	require.NotNil(resp)
	require.Equal(http.StatusBadGateway, resp.StatusCode)

	events, err := database.ListMREvents(t.Context(), mrID)
	require.NoError(err)
	require.Empty(events)
}

func TestAPIEditPRCommentRejectsNilProviderPayload(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)

	mock := &mockGH{
		editIssueCommentFn: func(context.Context, string, string, int64, string) (*gh.IssueComment, error) {
			return nil, nil
		},
	}
	srv, database := setupTestServerWithMock(t, mock)
	mrID := seedPR(t, database, "acme", "widget", 1)
	commentID := int64(42)
	require.NoError(database.UpsertMREvents(t.Context(), []db.MREvent{{
		MergeRequestID: mrID,
		PlatformID:     &commentID,
		EventType:      "issue_comment",
		Body:           "original body",
		CreatedAt:      time.Now().UTC(),
		DedupeKey:      "comment-42",
	}}))
	client := setupTestClient(t, srv)

	resp, err := client.HTTP.EditPrCommentWithResponse(t.Context(), &generated.EditPrCommentRequestOptions{PathParams: &generated.EditPrCommentPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1), CommentID: int64(commentID)}, Body: &generated.EditPrCommentBody{Body: "edited body"}})
	require.Error(err)
	require.NotNil(resp)
	require.Equal(http.StatusBadGateway, resp.StatusCode)

	events, err := database.ListMREvents(t.Context(), mrID)
	require.NoError(err)
	require.Len(events, 1)
	assert.Equal("original body", events[0].Body)
}

func TestAPIApprovePRSubmitsGitHubReview(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)

	var providerCalled atomic.Bool
	var reviewCommitID string
	mock := &mockGH{
		createReviewWithCommentsFn: func(
			_ context.Context,
			_, _ string,
			_ int,
			event, body, commitID string,
			_ []*gh.DraftReviewComment,
		) (*gh.PullRequestReview, error) {
			providerCalled.Store(true)
			reviewCommitID = commitID
			id := int64(101)
			state := event
			now := gh.Timestamp{Time: time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)}
			return &gh.PullRequestReview{
				ID:          &id,
				State:       &state,
				Body:        &body,
				SubmittedAt: &now,
				User:        &gh.User{Login: new("reviewer")},
			}, nil
		},
	}
	srv, database := setupTestServerWithMock(t, mock)
	expectedHeadSHA := "reviewed-sha"
	mrID := seedPR(t, database, "acme", "widget", 1, withSeedPRHeadSHA(expectedHeadSHA))
	client := setupTestClient(t, srv)

	resp, err := client.HTTP.ApprovePullWithResponse(t.Context(), &generated.ApprovePullRequestOptions{PathParams: &generated.ApprovePullPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}, Body: &generated.ApprovePullBody{ExpectedHeadSha: &expectedHeadSHA}})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode, string(resp.Body))
	assert.True(providerCalled.Load())
	assert.Equal(expectedHeadSHA, reviewCommitID)

	events, err := database.ListMREvents(t.Context(), mrID)
	require.NoError(err)
	require.Len(events, 1)
	assert.Equal("review", events[0].EventType)
	assert.Equal("APPROVE", events[0].Summary)
}

func TestAPIMergePRRejectsNilProviderPayload(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)

	mock := &mockGH{
		mergePullRequestFn: func(
			context.Context,
			string,
			string,
			int,
			string,
			string,
			string,
		) (*gh.PullRequestMergeResult, error) {
			return nil, nil
		},
	}
	srv, database := setupTestServerWithMock(t, mock)
	expectedHeadSHA := "reviewed-sha"
	seedPR(t, database, "acme", "widget", 1, withSeedPRHeadSHA(expectedHeadSHA))
	client := setupTestClient(t, srv)

	resp, err := client.HTTP.MergePullWithResponse(t.Context(), &generated.MergePullRequestOptions{PathParams: &generated.MergePullPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}, Body: &generated.MergePullBody{
		Method:          "squash",
		ExpectedHeadSha: &expectedHeadSHA,
	}})
	require.Error(err)
	require.NotNil(resp)
	require.Equal(http.StatusBadGateway, resp.StatusCode)

	mr, err := database.GetMergeRequest(t.Context(), "github", "github.com", "acme", "widget", 1)
	require.NoError(err)
	require.NotNil(mr)
	assert.Equal(db.MergeRequestStateOpen, mr.State)
	assert.Nil(mr.MergedAt)
}

func TestAPIPostPrCommentAllowsMixedCaseTrackedRepo(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	srv, database := setupTestServerWithRepos(
		t,
		&mockGH{},
		[]ghclient.RepoRef{{
			Owner:        "Acme",
			Name:         "widget",
			PlatformHost: "github.com",
		}},
	)
	client := setupTestClient(t, srv)

	seedPR(t, database, "acme", "widget", 7)

	resp, err := client.HTTP.PostPrCommentWithResponse(t.Context(), &generated.PostPrCommentRequestOptions{PathParams: &generated.PostPrCommentPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(7)}, Body: &generated.PostPrCommentBody{Body: "looks good"}})
	require.NoError(err)
	require.Equal(http.StatusCreated, resp.StatusCode)
	require.NotNil(resp.JSON201)
}

func TestAPIEditPrCommentUpdatesGitHubAndLocalTimeline(t *testing.T) {
	runParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	commentID := int64(9876)
	createdAt := time.Date(2026, 4, 29, 12, 0, 0, 0, time.UTC)
	mock := &mockGH{
		editIssueCommentFn: func(_ context.Context, owner, repo string, gotCommentID int64, body string) (*gh.IssueComment, error) {
			assert.Equal("acme", owner)
			assert.Equal("widget", repo)
			assert.Equal(commentID, gotCommentID)
			assert.Equal("edited body", body)
			login := "maintainer"
			return &gh.IssueComment{
				ID:        &gotCommentID,
				Body:      &body,
				User:      &gh.User{Login: &login},
				CreatedAt: &gh.Timestamp{Time: createdAt},
				UpdatedAt: &gh.Timestamp{Time: createdAt.Add(time.Minute)},
			}, nil
		},
	}
	srv, database := setupTestServerWithMock(t, mock)
	mrID := seedPR(t, database, "acme", "widget", 7)
	require.NoError(database.UpsertMREvents(t.Context(), []db.MREvent{{
		MergeRequestID: mrID,
		PlatformID:     &commentID,
		EventType:      "issue_comment",
		Author:         "maintainer",
		Body:           "original body",
		MetadataJSON:   `{"provider_hidden":true,"provider_hidden_reason":"OFF_TOPIC"}`,
		CreatedAt:      createdAt,
		DedupeKey:      "comment-9876",
	}}))

	req := httptest.NewRequestWithContext(t.Context(),
		http.MethodPatch,
		"/api/v1/pulls/gh/acme/widget/7/comments/9876",
		strings.NewReader(`{"body":"edited body"}`),
	)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	srv.ServeHTTP(rec, req)

	require.Equal(http.StatusOK, rec.Code)
	events, err := database.ListMREvents(t.Context(), mrID)
	require.NoError(err)
	require.Len(events, 1)
	assert.Equal("edited body", events[0].Body)
	assert.Equal("maintainer", events[0].Author)
	assert.JSONEq(`{"provider_hidden":true,"provider_hidden_reason":"OFF_TOPIC"}`, events[0].MetadataJSON)
	require.NotNil(events[0].PlatformID)
	assert.Equal(commentID, *events[0].PlatformID)
}

func TestAPIEditPrCommentRejectsCommentFromDifferentPR(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	commentID := int64(5555)
	var editCalls atomic.Int32
	mock := &mockGH{
		editIssueCommentFn: func(_ context.Context, _, _ string, _ int64, _ string) (*gh.IssueComment, error) {
			editCalls.Add(1)
			return nil, nil
		},
	}
	srv, database := setupTestServerWithMock(t, mock)
	routeMRID := seedPR(t, database, "acme", "widget", 7)
	otherMRID := seedPR(t, database, "acme", "widget", 8)
	require.NotEqual(routeMRID, otherMRID)
	require.NoError(database.UpsertMREvents(t.Context(), []db.MREvent{{
		MergeRequestID: otherMRID,
		PlatformID:     &commentID,
		EventType:      "issue_comment",
		Author:         "maintainer",
		Body:           "other PR body",
		CreatedAt:      time.Now().UTC(),
		DedupeKey:      "comment-5555",
	}}))

	req := httptest.NewRequestWithContext(t.Context(),
		http.MethodPatch,
		"/api/v1/pulls/gh/acme/widget/7/comments/5555",
		strings.NewReader(`{"body":"wrong target"}`),
	)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	srv.ServeHTTP(rec, req)

	require.Equal(http.StatusNotFound, rec.Code)
	require.Equal(int32(0), editCalls.Load())
}

func TestAPIDeletePrCommentLeavesLocalStateForSync(t *testing.T) {
	runParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	commentID := int64(9876)
	var deleteCalls atomic.Int32
	mock := &mockGH{
		deleteIssueCommentFn: func(_ context.Context, owner, repo string, gotCommentID int64) error {
			deleteCalls.Add(1)
			assert.Equal("acme", owner)
			assert.Equal("widget", repo)
			assert.Equal(commentID, gotCommentID)
			return nil
		},
	}
	srv, database := setupTestServerWithMock(t, mock)
	mrID := seedPR(t, database, "acme", "widget", 7)
	require.NoError(database.UpsertMREvents(t.Context(), []db.MREvent{{
		MergeRequestID: mrID,
		PlatformID:     &commentID,
		EventType:      "issue_comment",
		Author:         "maintainer",
		Body:           "remove me",
		CreatedAt:      time.Now().UTC(),
		DedupeKey:      "comment-9876",
	}}))

	req := httptest.NewRequestWithContext(t.Context(), http.MethodDelete, "/api/v1/pulls/gh/acme/widget/7/comments/9876", nil)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	assert.Equal(http.StatusNoContent, rec.Code)
	assert.Equal(int32(1), deleteCalls.Load())
	events, err := database.ListMREvents(t.Context(), mrID)
	require.NoError(err)
	require.Len(events, 1)
	assert.Equal("remove me", events[0].Body)
}

func TestAPIDeletePrCommentKeepsLocalCommentWhenProviderReportsNotFound(t *testing.T) {
	runParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	commentID := int64(4321)
	mock := &mockGH{
		deleteIssueCommentFn: func(context.Context, string, string, int64) error {
			return platform.ErrNotFound
		},
	}
	srv, database := setupTestServerWithMock(t, mock)
	mrID := seedPR(t, database, "acme", "widget", 7)
	require.NoError(database.UpsertMREvents(t.Context(), []db.MREvent{{
		MergeRequestID: mrID,
		PlatformID:     &commentID,
		EventType:      "issue_comment",
		Author:         "maintainer",
		Body:           "already absent upstream",
		CreatedAt:      time.Now().UTC(),
		DedupeKey:      "comment-4321",
	}}))

	req := httptest.NewRequestWithContext(t.Context(), http.MethodDelete, "/api/v1/pulls/gh/acme/widget/7/comments/4321", nil)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	assert.Equal(http.StatusNotFound, rec.Code)
	events, err := database.ListMREvents(t.Context(), mrID)
	require.NoError(err)
	require.Len(events, 1)
	assert.Equal("already absent upstream", events[0].Body)
}

func TestAPIDeletePrCommentRejectsAnotherParentBeforeProviderCall(t *testing.T) {
	runParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	commentID := int64(6543)
	var deleteCalls atomic.Int32
	mock := &mockGH{
		deleteIssueCommentFn: func(context.Context, string, string, int64) error {
			deleteCalls.Add(1)
			return nil
		},
	}
	srv, database := setupTestServerWithMock(t, mock)
	seedPR(t, database, "acme", "widget", 7)
	otherMRID := seedPR(t, database, "acme", "widget", 8)
	require.NoError(database.UpsertMREvents(t.Context(), []db.MREvent{{
		MergeRequestID: otherMRID,
		PlatformID:     &commentID,
		EventType:      "issue_comment",
		Author:         "maintainer",
		Body:           "belongs to another pull request",
		CreatedAt:      time.Now().UTC(),
		DedupeKey:      "comment-6543",
	}}))

	req := httptest.NewRequestWithContext(t.Context(), http.MethodDelete, "/api/v1/pulls/gh/acme/widget/7/comments/6543", nil)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	assert.Equal(http.StatusNotFound, rec.Code)
	assert.Equal(int32(0), deleteCalls.Load())
}

func TestAPIReadyForReview(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	database := dbtest.Open(t)

	mock := &mockGH{
		markReadyForReviewFn: func(_ context.Context, _, _ string, number int) (*gh.PullRequest, error) {
			id := int64(1001)
			title := "Ready PR"
			state := "open"
			url := "https://github.com/acme/widget/pull/1"
			author := "octocat"
			draft := false
			now := gh.Timestamp{Time: time.Now().UTC()}
			return &gh.PullRequest{
				ID:        &id,
				Number:    &number,
				Title:     &title,
				State:     &state,
				HTMLURL:   &url,
				Draft:     &draft,
				CreatedAt: &now,
				UpdatedAt: &now,
				User:      &gh.User{Login: &author},
				Head:      &gh.PullRequestBranch{Ref: new("feature")},
				Base:      &gh.PullRequestBranch{Ref: new("main")},
			}, nil
		},
	}
	syncer := ghclient.NewSyncer(map[string]ghclient.Client{"github.com": mock}, database, nil, defaultTestRepos, time.Minute, nil, nil)
	t.Cleanup(syncer.Stop)
	srv := server.New(
		database, syncer, nil, "/",
		nil, server.ServerOptions{},
	)
	client := setupTestClient(t, srv)

	repoID, err := database.UpsertRepo(t.Context(), verifiedGitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)

	now := time.Now().UTC().Truncate(time.Second)
	prID, err := database.UpsertMergeRequest(t.Context(), &db.MergeRequest{
		RepoID:         repoID,
		PlatformID:     1001,
		Number:         1,
		URL:            "https://github.com/acme/widget/pull/1",
		Title:          "Ready PR",
		Author:         "octocat",
		State:          "open",
		IsDraft:        true,
		Body:           "",
		HeadBranch:     "feature",
		BaseBranch:     "main",
		Additions:      0,
		Deletions:      0,
		CommentCount:   0,
		ReviewDecision: "",
		CIStatus:       "",
		CreatedAt:      now,
		UpdatedAt:      now,
		LastActivityAt: now,
	})
	require.NoError(err)
	require.NoError(database.EnsureKanbanState(t.Context(), prID))

	resp, err := client.HTTP.MarkPullReadyForReviewWithResponse(t.Context(), &generated.MarkPullReadyForReviewRequestOptions{PathParams: &generated.MarkPullReadyForReviewPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)
	require.NotNil(resp.JSON200)

	pr, err := database.GetMergeRequest(t.Context(), "github", "github.com", "acme", "widget", 1)
	require.NoError(err)
	require.NotNil(pr)
	require.False(pr.IsDraft)
}

func TestAPIClosePR(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)

	// The provider reports the close in a non-UTC zone; the handler commits
	// the provider's own edit response, so closed_at comes from there.
	providerClosedAt := testEDTTime(9, 15)
	mock := &mockGH{
		editPullRequestFn: func(
			_ context.Context, _, _ string, number int, opts platformgithub.EditPullRequestOpts,
		) (*gh.PullRequest, error) {
			require.NotNil(opts.State)
			return providerStatePR(
				number, *opts.State, time.Now().UTC().Add(time.Hour),
				&providerClosedAt, nil, "head-sha",
			), nil
		},
	}
	srv, database := setupTestServerWithMock(t, mock)
	seedPR(t, database, "acme", "widget", 1,
		withSeedPRHeadSHA("head-sha"),
		withSeedPRCI("success", `[{"name":"build","status":"completed","conclusion":"success","url":"","app":"GitHub Actions"}]`),
	)
	repo, err := database.GetRepoByIdentity(t.Context(), verifiedGitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	updatedAt := providerClosedAt.Add(-time.Hour)
	number := 1
	require.NoError(database.UpsertNotifications(t.Context(), []db.Notification{{
		Platform:               "github",
		PlatformHost:           "github.com",
		PlatformNotificationID: "thread-pr-close",
		RepoID:                 &repo.ID,
		RepoOwner:              "acme",
		RepoName:               "widget",
		SubjectType:            "PullRequest",
		SubjectTitle:           "Test PR #1",
		WebURL:                 "https://github.com/acme/widget/pull/1",
		ItemNumber:             &number,
		ItemType:               "pr",
		Reason:                 "mention",
		Unread:                 true,
		SourceUpdatedAt:        updatedAt,
		SyncedAt:               updatedAt,
	}}))
	client := setupTestClient(t, srv)

	resp, err := client.HTTP.SetPrGithubStateWithResponse(t.Context(), &generated.SetPrGithubStateRequestOptions{PathParams: &generated.SetPrGithubStatePath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}, Body: &generated.SetPrGithubStateBody{State: "closed"}})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)

	pr, err := database.GetMergeRequest(t.Context(), "github", "github.com", "acme", "widget", 1)
	require.NoError(err)
	assert.Equal(db.MergeRequestStateClosed, pr.State)
	assertTimePtrEqualsUTC(t, pr.ClosedAt, providerClosedAt)
	// The edit response cannot carry CI state; closing must not erase the
	// cached columns of a row no later sync will refetch.
	assert.Equal("success", pr.CIStatus)
	assert.Contains(pr.CIChecksJSON, `"name":"build"`)
	doneNotifications, err := database.ListNotifications(t.Context(), db.ListNotificationsOpts{State: "done"})
	require.NoError(err)
	require.Len(doneNotifications, 1)
	assert.Equal("thread-pr-close", doneNotifications[0].PlatformNotificationID)
	assert.Equal("closed", doneNotifications[0].DoneReason)
}

func TestAPIReopenPR(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	mock := &mockGH{
		editPullRequestFn: func(
			_ context.Context, _, _ string, number int, opts platformgithub.EditPullRequestOpts,
		) (*gh.PullRequest, error) {
			require.NotNil(opts.State)
			return providerStatePR(
				number, *opts.State, time.Now().UTC().Add(time.Hour),
				nil, nil, "head-sha",
			), nil
		},
	}
	srv, database := setupTestServerWithMock(t, mock)
	seedPR(t, database, "acme", "widget", 1)
	ctx := t.Context()

	// Close it first.
	repo, err := database.GetRepoByIdentity(ctx, verifiedGitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	now := time.Now()
	require.NoError(database.UpdateMRState(ctx, repo.ID, 1, "closed", nil, &now))

	client := setupTestClient(t, srv)
	resp, err := client.HTTP.SetPrGithubStateWithResponse(ctx, &generated.SetPrGithubStateRequestOptions{PathParams: &generated.SetPrGithubStatePath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}, Body: &generated.SetPrGithubStateBody{State: "open"}})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)

	pr, err := database.GetMergeRequest(ctx, "github", "github.com", "acme", "widget", 1)
	require.NoError(err)
	require.Equal(db.MergeRequestStateOpen, pr.State)
	require.Nil(pr.ClosedAt, "closed_at should be cleared on reopen")
}

func TestAPIReadyForReviewDoesNotGetRevertedByStaleSync(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)

	staleUpdatedAt := time.Date(2026, 4, 12, 1, 0, 0, 0, time.UTC)
	readyUpdatedAt := staleUpdatedAt.Add(30 * time.Minute)
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
			draft := true
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
		markReadyForReviewFn: func(_ context.Context, owner, repo string, number int) (*gh.PullRequest, error) {
			id := int64(101)
			state := "open"
			title := "ready for review"
			url := "https://github.com/acme/widget/pull/1"
			author := "alice"
			draft := false
			headSHA := "abc123"
			baseSHA := "def456"
			featureRef := "feature"
			mainRef := "main"
			createdAt := gh.Timestamp{Time: staleUpdatedAt.Add(-time.Hour)}
			updatedAt := gh.Timestamp{Time: readyUpdatedAt}
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
		Title:           "draft PR",
		Author:          "alice",
		State:           "open",
		IsDraft:         true,
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
		UpdatedAt:       staleUpdatedAt.Add(-time.Minute),
		LastActivityAt:  staleUpdatedAt.Add(-time.Minute),
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

	resp, err := client.HTTP.MarkPullReadyForReviewWithResponse(t.Context(), &generated.MarkPullReadyForReviewRequestOptions{PathParams: &generated.MarkPullReadyForReviewPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)

	readyPR, err := database.GetMergeRequest(t.Context(), "github", "github.com", "acme", "widget", 1)
	require.NoError(err)
	require.False(readyPR.IsDraft)
	assert.True(readyPR.UpdatedAt.Equal(readyUpdatedAt))

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
	assert.False(finalPR.IsDraft)
	assert.Equal("ready for review", finalPR.Title)
	assert.True(finalPR.UpdatedAt.Equal(readyUpdatedAt))
}

func TestAPIListPullsReportsHistoricalMergedPRFromMergedAt(t *testing.T) {
	runParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	ctx := t.Context()

	now := time.Date(2024, 6, 7, 12, 0, 0, 0, time.UTC)
	mergedAt := now.Add(time.Hour)
	number := 42
	title := "Historical merged PR"
	headSHA := "abc123def456"
	baseSHA := "def456abc123"
	database := dbtest.Open(t)
	seedPR(
		t, database, "acme", "widget", number,
		withSeedPRTitle(title),
		withSeedPRAuthor("alice"),
		withSeedPRLifecycle(db.MergeRequestStateMerged, &mergedAt, &mergedAt),
		withSeedPRTimes(now, now, mergedAt),
		withSeedPRHeadSHA(headSHA),
		withSeedPRBaseSHA(baseSHA),
	)

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
	srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{})
	t.Cleanup(func() { gracefulShutdown(t, srv) })

	client := setupTestClient(t, srv)
	filterState := "closed"
	resp, err := client.HTTP.ListPullsWithResponse(ctx, &generated.ListPullsRequestOptions{Query: &generated.ListPullsQuery{State: &filterState}})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)
	require.NotNil(resp.JSON200)
	require.Len(*resp.JSON200, 1)

	apiPR := (*resp.JSON200)[0]
	assert.Equal(int64(number), apiPR.Number)
	assert.Equal(title, apiPR.Title)
	assert.Equal(generated.MergeRequestResponseStateMerged, apiPR.State)
	require.NotNil(apiPR.MergedAt)
	assert.True(apiPR.MergedAt.Equal(mergedAt))
}

func make422Error() error {
	return &gh.ErrorResponse{
		Response: &http.Response{StatusCode: http.StatusUnprocessableEntity},
		Message:  "Validation Failed",
	}
}

func TestAPIClosePR422NilFallbackPayloadDoesNotCorruptDB(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	mock := &mockGH{
		editPullRequestFn: func(_ context.Context, _, _ string, _ int, _ platformgithub.EditPullRequestOpts) (*gh.PullRequest, error) {
			return nil, make422Error()
		},
		getPullRequestFn: func(_ context.Context, _, _ string, _ int) (*gh.PullRequest, error) {
			return nil, nil
		},
	}
	srv, database := setupTestServerWithMock(t, mock)
	seedPR(t, database, "acme", "widget", 1)
	before, err := database.GetMergeRequest(t.Context(), "github", "github.com", "acme", "widget", 1)
	require.NoError(err)
	require.NotNil(before)

	client := setupTestClient(t, srv)
	resp, err := client.HTTP.SetPrGithubStateWithResponse(t.Context(), &generated.SetPrGithubStateRequestOptions{PathParams: &generated.SetPrGithubStatePath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}, Body: &generated.SetPrGithubStateBody{State: "closed"}})
	require.Error(err)
	require.NotNil(resp)
	require.Equal(http.StatusBadGateway, resp.StatusCode)

	after, err := database.GetMergeRequest(t.Context(), "github", "github.com", "acme", "widget", 1)
	require.NoError(err)
	require.NotNil(after)
	assert.Equal(before.State, after.State)
	assert.Equal(before.UpdatedAt, after.UpdatedAt)
	assert.Nil(after.ClosedAt)
}

func TestAPIClosePR422AlreadyClosed(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	// EditPullRequest returns 422, but re-fetch shows PR is already closed.
	// Should succeed since the requested state matches.
	state := "closed"
	mock := &mockGH{
		editPullRequestFn: func(_ context.Context, _, _ string, _ int, _ platformgithub.EditPullRequestOpts) (*gh.PullRequest, error) {
			return nil, make422Error()
		},
		getPullRequestFn: func(_ context.Context, _, _ string, _ int) (*gh.PullRequest, error) {
			id := int64(1000)
			now := gh.Timestamp{Time: time.Now().UTC()}
			closedAt := gh.Timestamp{Time: time.Now().UTC()}
			return &gh.PullRequest{
				ID: &id, Number: new(1), State: &state,
				Title: new("PR"), HTMLURL: new("https://example.com"),
				User:      &gh.User{Login: new("u")},
				Head:      &gh.PullRequestBranch{Ref: new("f")},
				Base:      &gh.PullRequestBranch{Ref: new("main")},
				CreatedAt: &now, UpdatedAt: &now, ClosedAt: &closedAt,
			}, nil
		},
	}
	srv, database := setupTestServerWithMock(t, mock)
	seedPR(t, database, "acme", "widget", 1)
	client := setupTestClient(t, srv)

	resp, err := client.HTTP.SetPrGithubStateWithResponse(t.Context(), &generated.SetPrGithubStateRequestOptions{PathParams: &generated.SetPrGithubStatePath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}, Body: &generated.SetPrGithubStateBody{State: "closed"}})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)

	pr, _ := database.GetMergeRequest(t.Context(), "github", "github.com", "acme", "widget", 1)
	require.Equal(db.MergeRequestStateClosed, pr.State)
}

// When MarkPullRequestReadyForReview returns (nil, nil) the handler
// must return 502 rather than claiming success with no PR payload.
func TestAPIReadyForReview502OnNilPR(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	mock := &mockGH{
		markReadyForReviewFn: func(_ context.Context, _, _ string, _ int) (*gh.PullRequest, error) {
			return nil, nil
		},
	}
	srv, database := setupTestServerWithMock(t, mock)
	seedPR(t, database, "acme", "widget", 1)
	client := setupTestClient(t, srv)

	resp, err := client.HTTP.MarkPullReadyForReviewWithResponse(t.Context(), &generated.MarkPullReadyForReviewRequestOptions{PathParams: &generated.MarkPullReadyForReviewPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}})
	require.Error(err)
	require.NotNil(resp)
	require.Equal(http.StatusBadGateway, resp.StatusCode)
}

func TestAPIReadyForReviewReturnsUnderlyingErrorDetail(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	mock := &mockGH{
		markReadyForReviewFn: func(_ context.Context, _, _ string, _ int) (*gh.PullRequest, error) {
			return nil, errors.New("marking acme/widget#1 ready for review: draft review threads still pending")
		},
	}
	srv, database := setupTestServerWithMock(t, mock)
	seedPR(t, database, "acme", "widget", 1)
	client := setupTestClient(t, srv)

	resp, err := client.HTTP.MarkPullReadyForReviewWithResponse(t.Context(), &generated.MarkPullReadyForReviewRequestOptions{PathParams: &generated.MarkPullReadyForReviewPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}})
	require.Error(err)
	require.NotNil(resp)
	require.Equal(http.StatusBadGateway, resp.StatusCode)
	require.NotNil(resp.Error)
	require.NotNil(resp.Error.Detail)
	require.Equal(
		"marking acme/widget#1 ready for review: draft review threads still pending",
		*resp.Error.Detail,
	)
}

func TestAPIReadyForReviewStaleStateRefreshesAndReturnsSuccess(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)

	staleErr := &staleReadyForReviewError{
		err: errors.New(
			"marking acme/widget#1 ready for review: graphql errors: Could not resolve to a PullRequest with the global id of 'PR_kwDOAAABc84'.",
		),
	}
	mock := &mockGH{
		markReadyForReviewFn: func(_ context.Context, _, _ string, _ int) (*gh.PullRequest, error) {
			return nil, staleErr
		},
		getPullRequestFn: func(_ context.Context, _, _ string, number int) (*gh.PullRequest, error) {
			id := int64(1001)
			title := "Already ready"
			state := "open"
			url := "https://github.com/acme/widget/pull/1"
			author := "octocat"
			draft := false
			now := gh.Timestamp{Time: time.Now().UTC()}
			headSHA := "abc123"
			baseSHA := "def456"
			featureRef := "feature"
			mainRef := "main"
			return &gh.PullRequest{
				ID:        &id,
				Number:    &number,
				Title:     &title,
				State:     &state,
				HTMLURL:   &url,
				Draft:     &draft,
				CreatedAt: &now,
				UpdatedAt: &now,
				User:      &gh.User{Login: &author},
				Head:      &gh.PullRequestBranch{SHA: &headSHA, Ref: &featureRef},
				Base:      &gh.PullRequestBranch{SHA: &baseSHA, Ref: &mainRef},
			}, nil
		},
	}
	srv, database := setupTestServerWithMock(t, mock)
	seedPR(t, database, "acme", "widget", 1)

	pr, err := database.GetMergeRequest(t.Context(), "github", "github.com", "acme", "widget", 1)
	require.NoError(err)
	require.NotNil(pr)
	pr.IsDraft = true
	_, err = database.UpsertMergeRequest(t.Context(), pr)
	require.NoError(err)

	client := setupTestClient(t, srv)

	resp, err := client.HTTP.MarkPullReadyForReviewWithResponse(t.Context(), &generated.MarkPullReadyForReviewRequestOptions{PathParams: &generated.MarkPullReadyForReviewPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)

	pr, err = database.GetMergeRequest(t.Context(), "github", "github.com", "acme", "widget", 1)
	require.NoError(err)
	require.NotNil(pr)
	require.False(pr.IsDraft)
}

func TestAPIReadyForReview404RefreshesStaleDraftState(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	notFound := &gh.ErrorResponse{
		Response: &http.Response{StatusCode: http.StatusNotFound, Status: "404 Not Found"},
		Message:  "Not Found",
	}
	mock := &mockGH{
		markReadyForReviewFn: func(_ context.Context, _, _ string, _ int) (*gh.PullRequest, error) {
			return nil, fmt.Errorf("marking acme/widget#1 ready for review: %w", notFound)
		},
		getPullRequestFn: func(_ context.Context, _, _ string, number int) (*gh.PullRequest, error) {
			id := int64(1001)
			title := "Already ready"
			state := "open"
			url := "https://github.com/acme/widget/pull/1"
			author := "octocat"
			draft := false
			now := gh.Timestamp{Time: time.Now().UTC()}
			headSHA := "abc123"
			baseSHA := "def456"
			featureRef := "feature"
			mainRef := "main"
			return &gh.PullRequest{
				ID:        &id,
				Number:    &number,
				Title:     &title,
				State:     &state,
				HTMLURL:   &url,
				Draft:     &draft,
				CreatedAt: &now,
				UpdatedAt: &now,
				User:      &gh.User{Login: &author},
				Head:      &gh.PullRequestBranch{SHA: &headSHA, Ref: &featureRef},
				Base:      &gh.PullRequestBranch{SHA: &baseSHA, Ref: &mainRef},
			}, nil
		},
	}
	srv, database := setupTestServerWithMock(t, mock)
	seedPR(t, database, "acme", "widget", 1)
	client := setupTestClient(t, srv)

	resp, err := client.HTTP.MarkPullReadyForReviewWithResponse(t.Context(), &generated.MarkPullReadyForReviewRequestOptions{PathParams: &generated.MarkPullReadyForReviewPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)

	pr, err := database.GetMergeRequest(t.Context(), "github", "github.com", "acme", "widget", 1)
	require.NoError(err)
	require.NotNil(pr)
	require.False(pr.IsDraft)
}

func TestAPIClosePR422Merged(t *testing.T) {
	runParallelServerTest(t)
	// EditPullRequest returns 422, re-fetch shows PR is merged.
	// Should return 409.
	merged := "closed"
	mock := &mockGH{
		editPullRequestFn: func(_ context.Context, _, _ string, _ int, _ platformgithub.EditPullRequestOpts) (*gh.PullRequest, error) {
			return nil, make422Error()
		},
		getPullRequestFn: func(_ context.Context, _, _ string, _ int) (*gh.PullRequest, error) {
			id := int64(1000)
			now := gh.Timestamp{Time: time.Now().UTC()}
			mergedBool := true
			return &gh.PullRequest{
				ID: &id, Number: new(1), State: &merged, Merged: &mergedBool,
				Title: new("PR"), HTMLURL: new("https://example.com"),
				User:      &gh.User{Login: new("u")},
				Head:      &gh.PullRequestBranch{Ref: new("f")},
				Base:      &gh.PullRequestBranch{Ref: new("main")},
				CreatedAt: &now, UpdatedAt: &now,
			}, nil
		},
	}
	srv, database := setupTestServerWithMock(t, mock)
	seedPR(t, database, "acme", "widget", 1)
	client := setupTestClient(t, srv)

	resp, err := client.HTTP.SetPrGithubStateWithResponse(t.Context(), &generated.SetPrGithubStateRequestOptions{PathParams: &generated.SetPrGithubStatePath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}, Body: &generated.SetPrGithubStateBody{State: "closed"}})
	require.Error(t, err)
	require.Equal(t, http.StatusConflict, resp.StatusCode)
}

func TestAPIGitHubPublishReviewDraftSendsCommentsThroughServer(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	ctx := t.Context()
	type capturedReview struct {
		owner    string
		repo     string
		number   int
		event    string
		body     string
		commitID string
		comments []*gh.DraftReviewComment
	}
	var captured capturedReview
	submittedAt := gh.Timestamp{Time: time.Now().UTC().Truncate(time.Second)}
	mock := &mockGH{
		createReviewWithCommentsFn: func(
			_ context.Context,
			owner, repo string,
			number int,
			event string,
			body string,
			commitID string,
			comments []*gh.DraftReviewComment,
		) (*gh.PullRequestReview, error) {
			captured = capturedReview{
				owner:    owner,
				repo:     repo,
				number:   number,
				event:    event,
				body:     body,
				commitID: commitID,
				comments: comments,
			}
			id := int64(501)
			state := "COMMENTED"
			return &gh.PullRequestReview{ID: &id, State: &state, SubmittedAt: &submittedAt}, nil
		},
	}
	srv, database := setupTestServerWithMock(t, mock)
	seedPR(t, database, "acme", "widget", 42)
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
			"start_side":    "right",
			"start_line":    40,
			"line":          42,
			"new_line":      42,
			"line_type":     "add",
			"diff_head_sha": "github-head",
			"commit_sha":    "github-commit",
		},
	})

	require.Equal(http.StatusCreated, createRR.Code, createRR.Body.String())

	publishRR := testutil.DoJSON(t, srv, http.MethodPost, basePath+"/publish", map[string]string{
		"action": "request_changes",
		"body":   " Needs changes. ",
	})

	require.Equal(http.StatusOK, publishRR.Code, publishRR.Body.String())
	var publishStatus pullapi.ActionStatusBody
	require.NoError(json.NewDecoder(publishRR.Body).Decode(&publishStatus))
	assert.Equal("published", publishStatus.Status)

	assert.Equal("acme", captured.owner)
	assert.Equal("widget", captured.repo)
	assert.Equal(42, captured.number)
	assert.Equal("REQUEST_CHANGES", captured.event)
	assert.Equal("Needs changes.", captured.body)
	assert.Equal("github-head", captured.commitID)
	require.Len(captured.comments, 1)
	comment := captured.comments[0]
	assert.Equal("src/main.go", comment.GetPath())
	assert.Equal("Please tighten this line.", comment.GetBody())
	assert.Equal("RIGHT", comment.GetSide())
	assert.Equal(40, comment.GetStartLine())
	assert.Equal("RIGHT", comment.GetStartSide())
	assert.Equal(42, comment.GetLine())

	storedDraft, err := database.GetMRReviewDraft(ctx, mr.ID)
	require.NoError(err)
	assert.Nil(storedDraft)
}

func TestAPIGitHubRequestChangesDoesNotPublishSavedDraftComments(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	ctx := t.Context()
	var captured platform.PublishDiffReviewDraftInput
	// No getPullRequestFn: the default mock returns no PR, so this test
	// also proves direct request-changes performs no provider head reads
	// beyond the review submission — the same call shape as /approve.
	mock := &mockGH{
		createReviewWithCommentsFn: func(
			_ context.Context,
			_, _ string,
			_ int,
			event string,
			body string,
			commitID string,
			comments []*gh.DraftReviewComment,
		) (*gh.PullRequestReview, error) {
			captured = platform.PublishDiffReviewDraftInput{
				Body:    body,
				Action:  platform.ReviewActionRequestChanges,
				HeadSHA: commitID,
			}
			assert.Equal("REQUEST_CHANGES", event)
			assert.Empty(comments)
			id := int64(502)
			state := "CHANGES_REQUESTED"
			return &gh.PullRequestReview{ID: &id, State: &state}, nil
		},
	}
	srv, database := setupTestServerWithMock(t, mock)
	seedPR(t, database, "acme", "widget", 42)
	mr, err := database.GetMergeRequest(ctx, "github", "github.com", "acme", "widget", 42)
	require.NoError(err)
	require.NotNil(mr)
	require.NoError(database.UpdateDiffSHAs(ctx, mr.RepoID, 42, "reviewed-head", "base", "merge-base"))

	basePath := "/api/v1/pulls/gh/acme/widget/42/review-draft"
	createRR := testutil.DoJSON(t, srv, http.MethodPost, basePath+"/comments", map[string]any{
		"body": "Saved for the later inline review.",
		"range": map[string]any{
			"path": "src/main.go", "side": "right", "line": 42,
			"new_line": 42, "line_type": "add", "diff_head_sha": "reviewed-head",
		},
	})

	require.Equal(http.StatusCreated, createRR.Code, createRR.Body.String())

	requestRR := testutil.DoJSON(t, srv, http.MethodPost, "/api/v1/pulls/gh/acme/widget/42/request-changes", map[string]string{
		"body":              " Please cover the empty state. ",
		"expected_head_sha": "provider-head",
	})

	require.Equal(http.StatusOK, requestRR.Code, requestRR.Body.String())
	assert.Equal("Please cover the empty state.", captured.Body)
	assert.Equal(platform.ReviewActionRequestChanges, captured.Action)
	assert.Equal("provider-head", captured.HeadSHA)

	storedDraft, err := database.GetMRReviewDraft(ctx, mr.ID)
	require.NoError(err)
	require.NotNil(storedDraft)
	comments, err := database.ListMRReviewDraftComments(ctx, storedDraft.ID)
	require.NoError(err)
	require.Len(comments, 1)
	assert.Equal("Saved for the later inline review.", comments[0].Body)
}

func TestAPIGitHubReviewDraftHidesApproveForSelfAuthoredPR(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	mock := &mockGH{
		authenticatedViewerLoginFn: func(context.Context) (string, error) {
			return "marius", nil
		},
	}
	srv, database := setupTestServerWithMock(t, mock)
	seedPR(t, database, "acme", "widget", 42, withSeedPRAuthor("marius"))
	seedPR(t, database, "acme", "widget", 43, withSeedPRAuthor("someone-else"))

	selfRR := testutil.DoJSON(t, srv, http.MethodGet, "/api/v1/pulls/gh/acme/widget/42/review-draft", nil)
	require.Equal(http.StatusOK, selfRR.Code, selfRR.Body.String())
	var selfDraft map[string]any
	require.NoError(json.NewDecoder(selfRR.Body).Decode(&selfDraft))
	assert.Equal(
		[]any{"comment", "request_changes"},
		selfDraft["supported_actions"],
		"self-authored PR draft must not advertise approve",
	)

	otherRR := testutil.DoJSON(t, srv, http.MethodGet, "/api/v1/pulls/gh/acme/widget/43/review-draft", nil)
	require.Equal(http.StatusOK, otherRR.Code, otherRR.Body.String())
	var otherDraft map[string]any
	require.NoError(json.NewDecoder(otherRR.Body).Decode(&otherDraft))
	assert.Equal(
		[]any{"comment", "approve", "request_changes"},
		otherDraft["supported_actions"],
		"other-authored PR draft must still advertise approve",
	)
}

func TestAPIGitLabPublishReviewDraftSurfacesCleanupFailureAsPartial(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Second)
	providerUpdatedAt := now.Add(time.Minute)
	var createAttempts atomic.Int32
	var publishAttempts atomic.Int32
	var deleteAttempts atomic.Int32
	writeRawJSON := func(w http.ResponseWriter, body string) {
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, body)
	}
	gitlabServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.EscapedPath() {
		case "/api/v4/projects/4242/merge_requests/7/draft_notes":
			assert.Equal(http.MethodPost, r.Method)
			next := createAttempts.Add(1)
			var body struct {
				Note     string `json:"note"`
				CommitID string `json:"commit_id"`
				Position struct {
					NewPath string `json:"new_path"`
					NewLine int64  `json:"new_line"`
				} `json:"position"`
			}
			if !assert.NoError(json.NewDecoder(r.Body).Decode(&body)) {
				http.Error(w, "invalid request body", http.StatusBadRequest)
				return
			}
			assert.Equal("gitlab-head", body.CommitID)
			assert.Equal("src/main.go", body.Position.NewPath)
			assert.Equal(int64(40+next), body.Position.NewLine)
			writeRawJSON(w, fmt.Sprintf(`{"id": %d, "note": %q}`, 54+next, body.Note))
		case "/api/v4/projects/4242/merge_requests/7/draft_notes/55/publish":
			assert.Equal(http.MethodPut, r.Method)
			publishAttempts.Add(1)
			writeRawJSON(w, `{}`)
		case "/api/v4/projects/4242/merge_requests/7/draft_notes/56/publish":
			assert.Equal(http.MethodPut, r.Method)
			publishAttempts.Add(1)
			http.Error(w, "publish failed", http.StatusBadRequest)
		case "/api/v4/projects/4242/merge_requests/7/draft_notes/56":
			assert.Equal(http.MethodDelete, r.Method)
			deleteAttempts.Add(1)
			http.Error(w, "delete failed", http.StatusBadRequest)
		case "/api/v4/projects/4242/merge_requests/7/discussions":
			assert.Equal(http.MethodGet, r.Method)
			writeRawJSON(w, `[
				{
					"id": "discussion-55",
					"individual_note": false,
					"notes": [{
						"id": 55,
						"type": "DiscussionNote",
						"body": "first line",
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
			writeRawJSON(w, `{"id":7001,"iid":7,"updated_at":"`+
				providerUpdatedAt.Format(time.RFC3339)+`"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer gitlabServer.Close()

	database := dbtest.Open(t)
	client, err := platformgitlab.NewClient(
		"gitlab.example.com",
		testTokenSource("token"),
		platformgitlab.WithBaseURLForTesting(gitlabServer.URL+"/api/v4"), platformgitlab.
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
	require.NoError(database.UpdateDiffSHAs(ctx, repoID, 7, "gitlab-head", "base", "merge-base"))

	basePath := "/api/v1/host/gitlab.example.com/pulls/gl/group/project/7/review-draft"
	for _, line := range []int{41, 42} {
		createRR := testutil.DoJSON(t, srv, http.MethodPost, basePath+"/comments", map[string]any{
			"body": fmt.Sprintf("line %d", line),
			"range": map[string]any{
				"path":          "src/main.go",
				"side":          "right",
				"line":          line,
				"new_line":      line,
				"line_type":     "add",
				"diff_head_sha": "gitlab-head",
			},
		})

		require.Equal(http.StatusCreated, createRR.Code, createRR.Body.String())
	}

	publishRR := testutil.DoJSON(t, srv, http.MethodPost, basePath+"/publish", map[string]string{
		"action": "comment",
	})

	require.Equal(http.StatusOK, publishRR.Code, publishRR.Body.String())
	var publishStatus pullapi.ActionStatusBody
	require.NoError(json.NewDecoder(publishRR.Body).Decode(&publishStatus))
	assert.Equal("partially_published", publishStatus.Status)
	assert.Equal(int32(2), createAttempts.Load())
	assert.Equal(int32(2), publishAttempts.Load())
	assert.Equal(int32(1), deleteAttempts.Load())

	mr, err := database.GetMergeRequestByRepoIDAndNumber(ctx, repoID, 7)
	require.NoError(err)
	require.NotNil(mr)
	draft, err := database.GetMRReviewDraft(ctx, mr.ID)
	require.NoError(err)
	require.NotNil(draft)
	comments, err := database.ListMRReviewDraftComments(ctx, draft.ID)
	require.NoError(err)
	require.Len(comments, 1)
	assert.Equal("line 42", comments[0].Body)
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

func TestAPIForgejoPublishReviewDraftIngestsTimelineThread(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	caps := platform.Capabilities{
		ReadRepositories:       true,
		ReadMergeRequests:      true,
		ReadIssues:             true,
		ReadComments:           true,
		ReadReviewThreads:      true,
		ReviewDraftMutation:    true,
		SupportedReviewActions: []platform.ReviewAction{platform.ReviewActionComment},
	}
	srv, database, provider := setupForgejoCapabilityServerWithProvider(t, &caps)
	ctx := t.Context()

	repo, err := database.GetRepoByIdentity(ctx, db.RepoIdentity{
		Platform:     "forgejo",
		PlatformHost: "codeberg.org",
		RepoPath:     "acme/widgets",
	})
	require.NoError(err)
	require.NotNil(repo)
	mr, err := database.GetMergeRequestByRepoIDAndNumber(ctx, repo.ID, 42)
	require.NoError(err)
	require.NotNil(mr)
	require.NoError(database.UpdateDiffSHAs(ctx, repo.ID, 42, "forgejo-head", "base", "merge"))
	now := time.Now().UTC().Truncate(time.Second)
	line := 7
	provider.reviewThreads = []platform.MergeRequestReviewThread{{
		ProviderThreadID:  "comment-42",
		ProviderReviewID:  "review-42",
		ProviderCommentID: "comment-42",
		Body:              "Forgejo inline note",
		AuthorLogin:       "ada",
		Range: platform.DiffReviewLineRange{
			Path:        "src/main.go",
			Side:        "right",
			Line:        7,
			NewLine:     &line,
			LineType:    "add",
			DiffHeadSHA: "forgejo-head",
			CommitSHA:   "forgejo-head",
		},
		CreatedAt: now,
		UpdatedAt: now,
	}}

	basePath := "/api/v1/pulls/forgejo/acme/widgets/42/review-draft"
	createRR := testutil.DoJSON(t, srv, http.MethodPost, basePath+"/comments", map[string]any{
		"body": "Forgejo inline note",
		"range": map[string]any{
			"path":          "src/main.go",
			"side":          "right",
			"line":          7,
			"new_line":      7,
			"line_type":     "add",
			"diff_head_sha": "forgejo-head",
			"commit_sha":    "forgejo-head",
		},
	})

	require.Equal(http.StatusCreated, createRR.Code, createRR.Body.String())

	publishRR := testutil.DoJSON(t, srv, http.MethodPost, basePath+"/publish", map[string]string{
		"action": "comment",
	})

	require.Equal(http.StatusOK, publishRR.Code, publishRR.Body.String())
	require.Len(provider.publishedReviews, 1)

	detailRR := testutil.DoJSON(t, srv, http.MethodGet, "/api/v1/pulls/forgejo/acme/widgets/42", nil)
	require.Equal(http.StatusOK, detailRR.Code, detailRR.Body.String())
	var detail pullapi.MergeRequestDetailResponse
	require.NoError(json.NewDecoder(detailRR.Body).Decode(&detail))
	require.Len(detail.Events, 1)
	require.NotNil(detail.Events[0].DiffThread)
	assert.Equal("Forgejo inline note", detail.Events[0].DiffThread.Body)
	assert.Equal("src/main.go", detail.Events[0].DiffThread.Path)
	assert.Equal(7, detail.Events[0].DiffThread.Line)
}

func TestAPIForgejoSyncPersistsCompleteLargeReviewDatasetBeforeSuccess(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	caps := platform.Capabilities{
		ReadRepositories:       true,
		ReadMergeRequests:      true,
		ReadIssues:             true,
		ReadComments:           true,
		ReadReviewThreads:      true,
		ReviewDraftMutation:    true,
		SupportedReviewActions: []platform.ReviewAction{platform.ReviewActionComment},
	}
	srv, database, provider := setupForgejoCapabilityServerWithProvider(t, &caps)
	ctx := t.Context()

	repo, err := database.GetRepoByIdentity(ctx, db.RepoIdentity{
		Platform:     "forgejo",
		PlatformHost: "codeberg.org",
		RepoPath:     "acme/widgets",
	})
	require.NoError(err)
	require.NotNil(repo)
	mr, err := database.GetMergeRequestByRepoIDAndNumber(ctx, repo.ID, 42)
	require.NoError(err)
	require.NotNil(mr)
	events, err := database.ListMREvents(ctx, mr.ID)
	require.NoError(err)
	require.Empty(events)

	now := time.Now().UTC().Truncate(time.Second)
	provider.reviewThreads = make([]platform.MergeRequestReviewThread, 101)
	for i := range provider.reviewThreads {
		line := i + 1
		id := fmt.Sprintf("%d", line)
		provider.reviewThreads[i] = platform.MergeRequestReviewThread{
			ProviderThreadID: "thread-" + id, ProviderReviewID: "review-" + id,
			ProviderCommentID: "comment-" + id, Body: "inline note " + id,
			AuthorLogin: "ada",
			Range: platform.DiffReviewLineRange{
				Path: "src/recovered.go", Side: "right", Line: line,
				NewLine: &line, LineType: "add",
				DiffHeadSHA: "abc123", CommitSHA: "abc123",
			},
			CreatedAt: now, UpdatedAt: now,
		}
	}

	syncRR := testutil.DoJSON(t, srv, http.MethodPost, "/api/v1/pulls/forgejo/acme/widgets/42/sync", nil)
	require.Equal(http.StatusOK, syncRR.Code, syncRR.Body.String())
	threads, err := database.ListMRReviewThreads(ctx, mr.ID)
	require.NoError(err)
	require.Len(threads, 101)

	detailRR := testutil.DoJSON(t, srv, http.MethodGet, "/api/v1/pulls/forgejo/acme/widgets/42", nil)
	require.Equal(http.StatusOK, detailRR.Code, detailRR.Body.String())
	var detail pullapi.MergeRequestDetailResponse
	require.NoError(json.NewDecoder(detailRR.Body).Decode(&detail))
	require.Len(detail.Events, 101)
	for _, event := range detail.Events {
		assert.Equal("review_comment", event.EventType)
		require.NotNil(event.DiffThread)
		assert.Equal("src/recovered.go", event.DiffThread.Path)
	}
}

func seedApplySuggestionReviewThread(t *testing.T, database *db.DB, mrID int64) db.MRReviewThread {
	t.Helper()
	require := require.New(t)
	line := 11
	require.NoError(database.UpsertMRReviewThreads(t.Context(), mrID, []db.MRReviewThread{{
		ProviderThreadID:  "provider-thread-1",
		ProviderCommentID: "provider-comment-1",
		Body:              "Inline suggestion\n\n```suggestion\nreturn client.publishThreads();\n```",
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
	threads, err := database.ListMRReviewThreads(t.Context(), mrID)
	require.NoError(err)
	require.Len(threads, 1)
	return threads[0]
}

func TestAPIApplyReviewSuggestionRejectsUnknownHeadRepoOnGitHub(t *testing.T) {
	runParallelServerTest(t)
	tests := []struct {
		name             string
		headRepoCloneURL string
	}{
		{name: "missing clone URL"},
		{name: "unparseable clone URL", headRepoCloneURL: "not-a-url"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
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
			srv, _, provider := setupGitHubCapabilityServerWithProvider(t, &caps, tt.headRepoCloneURL)

			rr := testutil.DoJSON(
				t,
				srv,
				http.MethodPost,
				"/api/v1/host/github.example.com/pulls/gh/acme/widget/7/review-suggestions/apply",
				map[string]any{
					"expected_head_sha": "abc123",
					"suggestions": []map[string]any{{
						"thread_id":   "1",
						"replacement": "return client.publishThreads();",
					}},
				})

			require.Equal(http.StatusConflict, rr.Code, rr.Body.String())
			var problem rawProblemDetail
			require.NoError(json.NewDecoder(rr.Body).Decode(&problem))
			assert.Equal(string(httpapi.CodeConflict), problem.Code)
			assert.Equal("pull request head repository is unknown", problem.Detail)
			require.NotNil(problem.Details)
			assert.Equal("head_repo_unknown", problem.Details["reason"])
			assert.Empty(provider.appliedSuggestions)
		})
	}
}

func TestAPIApplyReviewSuggestionPassesHeadRepoToGitHubProvider(t *testing.T) {
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
	srv, database, provider := setupGitHubCapabilityServerWithProvider(
		t, &caps, "https://github.example.com/fork/widget.git",
	)
	ctx := t.Context()

	repo, err := database.GetRepoByIdentity(ctx, db.RepoIdentity{
		Platform:     "github",
		PlatformHost: "github.example.com",
		RepoPath:     "acme/widget",
	})
	require.NoError(err)
	require.NotNil(repo)
	mr, err := database.GetMergeRequestByRepoIDAndNumber(ctx, repo.ID, 7)
	require.NoError(err)
	require.NotNil(mr)
	thread := seedApplySuggestionReviewThread(t, database, mr.ID)

	rr := testutil.DoJSON(
		t,
		srv,
		http.MethodPost,
		"/api/v1/host/github.example.com/pulls/gh/acme/widget/7/review-suggestions/apply",
		map[string]any{
			"expected_head_sha": "abc123",
			"suggestions": []map[string]any{{
				"thread_id":   strconv.FormatInt(thread.ID, 10),
				"replacement": "return client.publishThreads();",
			}},
		})

	require.Equal(http.StatusOK, rr.Code, rr.Body.String())
	require.Len(provider.appliedSuggestions, 1)
	assert.Equal(
		"https://github.example.com/fork/widget.git",
		provider.appliedSuggestions[0].HeadRepoCloneURL,
	)
}

func TestAPIApplyReviewSuggestionMapsProviderConflictReason(t *testing.T) {
	runParallelServerTest(t)
	tests := []struct {
		name   string
		reason string
	}{
		{name: "closed upstream after sync", reason: "not_open"},
		{name: "head repo lost after sync", reason: "head_repo_unknown"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
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
			srv, database, provider := setupGitHubCapabilityServerWithProvider(
				t, &caps, "https://github.example.com/fork/widget.git",
			)
			ctx := t.Context()
			provider.applySuggestionsErr = &platform.Error{
				Code:         platform.ErrCodeConflict,
				Provider:     platform.KindGitHub,
				PlatformHost: "github.example.com",
				Details:      map[string]string{"reason": tt.reason},
				Err:          errors.New("live pull request state rejected the apply"),
			}

			repo, err := database.GetRepoByIdentity(ctx, db.RepoIdentity{
				Platform:     "github",
				PlatformHost: "github.example.com",
				RepoPath:     "acme/widget",
			})
			require.NoError(err)
			require.NotNil(repo)
			mr, err := database.GetMergeRequestByRepoIDAndNumber(ctx, repo.ID, 7)
			require.NoError(err)
			require.NotNil(mr)
			thread := seedApplySuggestionReviewThread(t, database, mr.ID)

			rr := testutil.DoJSON(
				t,
				srv,
				http.MethodPost,
				"/api/v1/host/github.example.com/pulls/gh/acme/widget/7/review-suggestions/apply",
				map[string]any{
					"expected_head_sha": "abc123",
					"suggestions": []map[string]any{{
						"thread_id":   strconv.FormatInt(thread.ID, 10),
						"replacement": "return client.publishThreads();",
					}},
				})

			require.Equal(http.StatusConflict, rr.Code, rr.Body.String())
			var problem rawProblemDetail
			require.NoError(json.NewDecoder(rr.Body).Decode(&problem))
			assert.Equal(string(httpapi.CodeConflict), problem.Code)
			require.NotNil(problem.Details)
			assert.Equal(tt.reason, problem.Details["reason"])
		})
	}
}

func TestAPIApplyReviewSuggestionMapsProviderStaleStateAndRefreshesDetail(t *testing.T) {
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
	srv, database, provider := setupGitHubCapabilityServerWithProvider(
		t, &caps, "https://github.example.com/fork/widget.git",
	)
	ctx := t.Context()
	provider.applySuggestionsErr = &platform.Error{
		Code:         platform.ErrCodeStaleState,
		Provider:     platform.KindGitHub,
		PlatformHost: "github.example.com",
		Err:          errors.New("pull request head branch changed since it was reviewed"),
	}

	repo, err := database.GetRepoByIdentity(ctx, db.RepoIdentity{
		Platform:     "github",
		PlatformHost: "github.example.com",
		RepoPath:     "acme/widget",
	})
	require.NoError(err)
	require.NotNil(repo)
	mr, err := database.GetMergeRequestByRepoIDAndNumber(ctx, repo.ID, 7)
	require.NoError(err)
	require.NotNil(mr)
	thread := seedApplySuggestionReviewThread(t, database, mr.ID)
	ch, _ := srv.Hub().Subscribe(ctx, false)

	rr := testutil.DoJSON(
		t,
		srv,
		http.MethodPost,
		"/api/v1/host/github.example.com/pulls/gh/acme/widget/7/review-suggestions/apply",
		map[string]any{
			"expected_head_sha": "abc123",
			"suggestions": []map[string]any{{
				"thread_id":   strconv.FormatInt(thread.ID, 10),
				"replacement": "return client.publishThreads();",
			}},
		})

	require.Equal(http.StatusConflict, rr.Code, rr.Body.String())
	var problem rawProblemDetail
	require.NoError(json.NewDecoder(rr.Body).Decode(&problem))
	assert.Equal(string(httpapi.CodeConflict), problem.Code)
	assert.Equal("target changed since it was reviewed; refresh and retry", problem.Detail)
	require.NotNil(problem.Details)
	assert.Equal("stale_state", problem.Details["reason"])
	assert.Empty(provider.appliedSuggestions)
	changed := readEventMatching(t, ch, func(ev server.Event) bool {
		return ev.Type == "data_changed"
	})
	assert.Equal("data_changed", changed.Type)
}

func TestAPIGitealikeHTTPMergeabilityPersistsThroughServer(t *testing.T) {
	runParallelServerTest(t)
	tests := []struct {
		name      string
		kind      platform.Kind
		host      string
		token     string
		newClient func(host, token, baseURL string) (platform.Provider, error)
	}{
		{
			name:  "gitea",
			kind:  platform.KindGitea,
			host:  "gitea.test",
			token: "gitea-token",
			newClient: func(host, token, baseURL string) (platform.Provider, error) {
				return giteaplatform.NewClient(
					host,
					testTokenSource(token),
					giteaplatform.WithBaseURL(baseURL, true),
					giteaplatform.WithServerVersion("1.26.0"),
					giteaplatform.WithTransport(http.DefaultTransport),
				)
			},
		},
		{
			name:  "forgejo",
			kind:  platform.KindForgejo,
			host:  "codeberg.test",
			token: "forgejo-token",
			newClient: func(host, token, baseURL string) (platform.Provider, error) {
				return forgejoplatform.NewClient(
					host,
					testTokenSource(token),
					forgejoplatform.WithBaseURLForTesting(baseURL),
					forgejoplatform.WithTransport(http.DefaultTransport),
				)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert := assert.New(t)
			require := require.New(t)
			ctx := t.Context()
			base := time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC)
			pull := func(number int, title, headSHA string) map[string]any {
				return map[string]any{
					"id":         number + 1000,
					"number":     number,
					"url":        "https://" + tt.host + "/tea/kettle/pulls/" + strconv.Itoa(number),
					"html_url":   "https://" + tt.host + "/tea/kettle/pulls/" + strconv.Itoa(number),
					"title":      title,
					"state":      "open",
					"user":       map[string]any{"login": "alice"},
					"head":       map[string]any{"ref": "feature", "sha": headSHA},
					"base":       map[string]any{"ref": "main", "sha": "base-sha"},
					"created_at": base,
					"updated_at": base.Add(time.Minute),
				}
			}
			dirtyPull := pull(7, "Conflicted kettle", "head-dirty")
			dirtyPull["mergeable"] = false
			nullPull := pull(8, "Unknown kettle", "head-null")
			nullPull["mergeable"] = nil
			omittedPull := pull(9, "Omitted kettle", "head-omitted")

			providerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal("token "+tt.token, r.Header.Get("Authorization"))
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/api/v1/repos/tea/kettle":
					assert.NoError(json.NewEncoder(w).Encode(map[string]any{
						"id":             101,
						"name":           "kettle",
						"full_name":      "tea/kettle",
						"html_url":       "https://" + tt.host + "/tea/kettle",
						"clone_url":      "https://" + tt.host + "/tea/kettle.git",
						"default_branch": "main",
						"owner":          map[string]any{"login": "tea"},
						"created_at":     base,
						"updated_at":     base,
					}))
				case "/api/v1/repos/tea/kettle/pulls":
					assert.Equal("open", r.URL.Query().Get("state"))
					assert.NoError(json.NewEncoder(w).Encode([]map[string]any{
						dirtyPull, nullPull, omittedPull,
					}))
				case "/api/v1/repos/tea/kettle/issues",
					"/api/v1/repos/tea/kettle/releases",
					"/api/v1/repos/tea/kettle/tags":
					assert.NoError(json.NewEncoder(w).Encode([]map[string]any{}))
				default:
					http.NotFound(w, r)
				}
			}))
			defer providerServer.Close()

			provider, err := tt.newClient(tt.host, tt.token, providerServer.URL)
			require.NoError(err)
			registry, err := platform.NewRegistry(provider)
			require.NoError(err)

			database := dbtest.Open(t)
			syncer := ghclient.NewSyncerWithRegistry(
				registry,
				database,
				nil,
				[]ghclient.RepoRef{{
					Platform:     tt.kind,
					PlatformHost: tt.host,
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
				Platform:     string(tt.kind),
				PlatformHost: tt.host,
				RepoPath:     "tea/kettle",
			})
			require.NoError(err)
			require.NotNil(repo)

			assert.Equal("dirty", requireMR(t, database, repo.ID, 7).MergeableState)
			assert.Empty(requireMR(t, database, repo.ID, 8).MergeableState)
			assert.Empty(requireMR(t, database, repo.ID, 9).MergeableState)

			detailResp, err := client.HTTP.GetPullOnHostWithResponse(ctx, &generated.GetPullOnHostRequestOptions{PathParams: &generated.GetPullOnHostPath{PlatformHost: tt.host, Provider: string(tt.kind), Owner: "tea", Name: "kettle", Number: int64(7)}})
			require.NoError(err)
			require.Equal(http.StatusOK, detailResp.StatusCode, string(detailResp.Body))
			require.NotNil(detailResp.JSON200)
			assert.Equal("dirty", detailResp.JSON200.MergeRequest.MergeableState)
		})
	}
}

// setupGitealikeHeadPinServer boots the HTTP API with real SQLite over a
// Gitea provider whose PR 7 syncs with head "abc123", for head-pin
// safety coverage through the real route path.
func setupGitealikeHeadPinServer(
	t *testing.T,
	transport *apiTestGitealikeTransport,
) *apiclient.Client {
	t.Helper()
	require := require.New(t)
	base := time.Date(2026, 5, 3, 12, 0, 0, 0, time.UTC)
	transport.repo = gitealike.RepositoryDTO{
		ID:            101,
		Owner:         gitealike.UserDTO{UserName: "tea"},
		Name:          "kettle",
		FullName:      "tea/kettle",
		HTMLURL:       "https://gitea.test/tea/kettle",
		CloneURL:      "https://gitea.test/tea/kettle.git",
		DefaultBranch: "main",
		Created:       base,
		Updated:       base,
	}
	transport.pulls = []gitealike.PullRequestDTO{{
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
	}}

	provider := gitealike.NewProvider(
		platform.KindGitea, "gitea.test", transport, gitealike.WithMutations(),
	)
	registry, err := platform.NewRegistry(provider)
	require.NoError(err)
	database := dbtest.Open(t)
	syncer := ghclient.NewSyncerWithRegistry(
		registry, database, nil,
		[]ghclient.RepoRef{{
			Platform:     platform.KindGitea,
			PlatformHost: "gitea.test",
			Owner:        "tea",
			Name:         "kettle",
			RepoPath:     "tea/kettle",
		}},
		time.Minute, nil, nil,
	)
	t.Cleanup(syncer.Stop)
	srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{})
	t.Cleanup(func() { gracefulShutdown(t, srv) })
	syncer.RunOnce(t.Context())
	repo, err := database.GetRepoByIdentity(t.Context(), db.RepoIdentity{
		Platform:     "gitea",
		PlatformHost: "gitea.test",
		RepoPath:     "tea/kettle",
	})
	require.NoError(err)
	require.NotNil(repo)
	require.NoError(database.UpdateDiffSHAs(t.Context(), repo.ID, 7, "abc123", "def456", "merge-base"))
	return setupTestClient(t, srv)
}

func decodeGitealikeConflict(t *testing.T, body []byte) (string, map[string]any) {
	t.Helper()
	var problem struct {
		Code    string         `json:"code"`
		Details map[string]any `json:"details"`
	}
	require.NoError(t, json.Unmarshal(body, &problem))
	return problem.Code, problem.Details
}

func TestAPIGitealikePinnedMergeHeadMismatchIsStale(t *testing.T) {
	runParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	transport := &apiTestGitealikeTransport{
		mergeErr: &gitealike.HTTPError{
			StatusCode: 409,
			Message:    "head out of date",
		},
	}
	client := setupGitealikeHeadPinServer(t, transport)

	pin := "abc123"
	resp, err := client.HTTP.MergePullOnHostWithResponse(t.Context(), &generated.MergePullOnHostRequestOptions{PathParams: &generated.MergePullOnHostPath{PlatformHost: "gitea.test", Provider: "gitea", Owner: "tea", Name: "kettle", Number: int64(7)}, Body: &generated.MergePullOnHostBody{
		Method:          "squash",
		CommitTitle:     "t",
		CommitMessage:   "m",
		ExpectedHeadSha: &pin,
	}})
	require.Error(err)
	require.NotNil(resp)
	require.Equal(http.StatusConflict, resp.StatusCode)
	code, details := decodeGitealikeConflict(t, resp.Body)
	assert.Equal("conflict", code)
	require.NotNil(details)
	assert.Equal("stale_state", details["reason"],
		"the provider's head-mismatch rejection must surface as stale_state")
	assert.Equal("abc123", transport.lastMergeOpts.ExpectedHeadSHA,
		"the reviewed head must reach the provider as head_commit_id")
}

func TestAPIGitealikePinnedMergeGenericConflictStaysConflict(t *testing.T) {
	runParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	transport := &apiTestGitealikeTransport{
		mergeErr: &gitealike.HTTPError{
			StatusCode: 409,
			Message:    "merge conflict detected",
		},
	}
	client := setupGitealikeHeadPinServer(t, transport)

	pin := "abc123"
	resp, err := client.HTTP.MergePullOnHostWithResponse(t.Context(), &generated.MergePullOnHostRequestOptions{PathParams: &generated.MergePullOnHostPath{PlatformHost: "gitea.test", Provider: "gitea", Owner: "tea", Name: "kettle", Number: int64(7)}, Body: &generated.MergePullOnHostBody{
		Method:          "squash",
		CommitTitle:     "t",
		CommitMessage:   "m",
		ExpectedHeadSha: &pin,
	}})
	require.Error(err)
	require.NotNil(resp)
	require.Equal(http.StatusConflict, resp.StatusCode)
	code, details := decodeGitealikeConflict(t, resp.Body)
	assert.Equal("conflict", code)
	require.NotNil(details)
	assert.Equal("conflict", details["reason"],
		"an unrelated 409 must not present as a stale-head re-review flow")
}

func TestAPIGitealikeApproveSubmitsReview(t *testing.T) {
	runParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	transport := &apiTestGitealikeTransport{}
	client := setupGitealikeHeadPinServer(t, transport)

	pin := "abc123"
	resp, err := client.HTTP.ApprovePullOnHostWithResponse(t.Context(), &generated.ApprovePullOnHostRequestOptions{PathParams: &generated.ApprovePullOnHostPath{PlatformHost: "gitea.test", Provider: "gitea", Owner: "tea", Name: "kettle", Number: int64(7)}, Body: &generated.ApprovePullOnHostBody{
		Body:            "lgtm",
		ExpectedHeadSha: &pin,
	}})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode, string(resp.Body))
	assert.Contains(transport.mutationCalls, "review:7:lgtm:abc123")
}

func TestAPIGitealikeApproveRefreshesAfterMutation(t *testing.T) {
	runParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	transport := &apiTestGitealikeTransport{}
	client := setupGitealikeHeadPinServer(t, transport)
	transport.headCalls = 0

	pin := "abc123"
	resp, err := client.HTTP.ApprovePullOnHostWithResponse(t.Context(), &generated.ApprovePullOnHostRequestOptions{PathParams: &generated.ApprovePullOnHostPath{PlatformHost: "gitea.test", Provider: "gitea", Owner: "tea", Name: "kettle", Number: int64(7)}, Body: &generated.ApprovePullOnHostBody{
		Body:            "lgtm",
		ExpectedHeadSha: &pin,
	}})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode, string(resp.Body))
	assert.Equal(1, transport.headCalls)
	assert.Contains(transport.mutationCalls, "review:7:lgtm:abc123")
}

func requireMR(t *testing.T, database *db.DB, repoID int64, number int) *db.MergeRequest {
	t.Helper()
	require := require.New(t)
	mr, err := database.GetMergeRequestByRepoIDAndNumber(t.Context(), repoID, number)
	require.NoError(err)
	require.NotNil(mr)
	return mr
}

func TestAPIGitealikeMergeConflictReturnsConflict(t *testing.T) {
	runParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	base := time.Date(2026, 5, 3, 12, 0, 0, 0, time.UTC)
	unmergeable := false
	transport := &apiTestGitealikeTransport{
		mergeErr: &gitealike.HTTPError{StatusCode: http.StatusConflict, Message: "pull request is out of date"},
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
		pulls: []gitealike.PullRequestDTO{{
			ID:        201,
			Index:     7,
			HTMLURL:   "https://gitea.test/tea/kettle/pulls/7",
			Title:     "Add kettle",
			User:      gitealike.UserDTO{UserName: "alice"},
			State:     "open",
			Head:      gitealike.BranchDTO{Ref: "feature", SHA: "abc123"},
			Base:      gitealike.BranchDTO{Ref: "main", SHA: "def456"},
			Mergeable: &unmergeable,
			Created:   base,
			Updated:   base,
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
	expectedHeadSHA := requireMR(t, database, repo.ID, 7).PlatformHeadSHA
	assert.Equal("dirty", requireMR(t, database, repo.ID, 7).MergeableState)

	resp, err := client.HTTP.MergePullOnHostWithResponse(ctx, &generated.MergePullOnHostRequestOptions{PathParams: &generated.MergePullOnHostPath{PlatformHost: "gitea.test", Provider: "gitea", Owner: "tea", Name: "kettle", Number: int64(7)}, Body: &generated.MergePullOnHostBody{
		Method:          "squash",
		CommitTitle:     "Merge kettle",
		CommitMessage:   "Merge Gitea MR",
		ExpectedHeadSha: &expectedHeadSHA,
	}})
	require.Error(err)
	require.NotNil(resp)
	require.Equal(http.StatusConflict, resp.StatusCode, string(resp.Body))
	assert.Contains(string(resp.Body), "pull request is out of date")
	assert.Contains(transport.mutationCalls, "merge:7:squash")
	assert.Equal("Add kettle", requireMR(t, database, repo.ID, 7).Title)
}

func setupAPIGitealikeHeadPinServer(
	t *testing.T,
	transport *apiTestGitealikeTransport,
) (*apiclient.Client, *db.DB) {
	t.Helper()
	require := require.New(t)
	ctx := t.Context()
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
	return client, database
}

func newAPIGitealikeHeadPinTransport(t *testing.T) *apiTestGitealikeTransport {
	t.Helper()
	base := time.Date(2026, 5, 3, 12, 0, 0, 0, time.UTC)
	mergeable := true
	return &apiTestGitealikeTransport{
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
		pulls: []gitealike.PullRequestDTO{{
			ID:        201,
			Index:     7,
			HTMLURL:   "https://gitea.test/tea/kettle/pulls/7",
			Title:     "Add kettle",
			User:      gitealike.UserDTO{UserName: "alice"},
			State:     "open",
			Head:      gitealike.BranchDTO{Ref: "feature", SHA: "abc123"},
			Base:      gitealike.BranchDTO{Ref: "main", SHA: "def456"},
			Mergeable: &mergeable,
			Created:   base,
			Updated:   base,
		}},
	}
}

func TestAPIGitealikeMergePassesReviewedHeadPinToProvider(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	ctx := t.Context()
	transport := newAPIGitealikeHeadPinTransport(t)
	client, database := setupAPIGitealikeHeadPinServer(t, transport)
	repo, err := database.GetRepoByIdentity(ctx, db.RepoIdentity{
		Platform:     "gitea",
		PlatformHost: "gitea.test",
		RepoPath:     "tea/kettle",
	})
	require.NoError(err)
	require.NotNil(repo)
	require.Equal("abc123", requireMR(t, database, repo.ID, 7).PlatformHeadSHA)
	expectedHeadSHA := "abc123"

	resp, err := client.HTTP.MergePullOnHostWithResponse(ctx, &generated.MergePullOnHostRequestOptions{PathParams: &generated.MergePullOnHostPath{PlatformHost: "gitea.test", Provider: "gitea", Owner: "tea", Name: "kettle", Number: int64(7)}, Body: &generated.MergePullOnHostBody{
		Method:          "squash",
		CommitTitle:     "Merge kettle",
		CommitMessage:   "Merge Gitea PR",
		ExpectedHeadSha: &expectedHeadSHA,
	}})

	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode, string(resp.Body))
	assert.Equal([]string{"abc123"}, transport.mergeHeadPins)
}

func TestAPIGitealikeMergeHeadMismatchMapsToStaleState(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	ctx := t.Context()
	transport := newAPIGitealikeHeadPinTransport(t)
	transport.mergeErr = &gitealike.HTTPError{
		StatusCode: http.StatusConflict,
		Message:    "head target does not match",
	}
	client, _ := setupAPIGitealikeHeadPinServer(t, transport)
	expectedHeadSHA := "abc123"

	resp, err := client.HTTP.MergePullOnHostWithResponse(ctx, &generated.MergePullOnHostRequestOptions{PathParams: &generated.MergePullOnHostPath{PlatformHost: "gitea.test", Provider: "gitea", Owner: "tea", Name: "kettle", Number: int64(7)}, Body: &generated.MergePullOnHostBody{
		Method:          "squash",
		CommitTitle:     "Merge kettle",
		CommitMessage:   "Merge Gitea PR",
		ExpectedHeadSha: &expectedHeadSHA,
	}})

	require.Error(err)

	require.NotNil(resp)
	require.Equal(http.StatusConflict, resp.StatusCode, string(resp.Body))
	require.NotNil(resp.Error)
	assert.Equal("conflict", string(resp.Error.Code))
	require.NotNil(resp.Error.Details)
	assert.Equal("stale_state", resp.Error.Details["reason"])
	assert.Equal([]string{"abc123"}, transport.mergeHeadPins)
}

func TestAPIGitealikeApproveBeforeHeadRace(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	ctx := t.Context()
	transport := newAPIGitealikeHeadPinTransport(t)
	client, _ := setupAPIGitealikeHeadPinServer(t, transport)
	expectedHeadSHA := "abc123"

	resp, err := client.HTTP.ApprovePullOnHostWithResponse(ctx, &generated.ApprovePullOnHostRequestOptions{PathParams: &generated.ApprovePullOnHostPath{PlatformHost: "gitea.test", Provider: "gitea", Owner: "tea", Name: "kettle", Number: int64(7)}, Body: &generated.ApprovePullOnHostBody{
		Body:            "approved",
		ExpectedHeadSha: &expectedHeadSHA,
	}})

	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode, string(resp.Body))
	assert.Contains(transport.mutationCalls, "review:7:approved:abc123")
}

func TestAPIGitealikeRequestChangesPassesReviewedHeadPinToProvider(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	ctx := t.Context()
	transport := newAPIGitealikeHeadPinTransport(t)
	client, _ := setupAPIGitealikeHeadPinServer(t, transport)
	expectedHeadSHA := "abc123"

	resp, err := client.HTTP.RequestPullChangesOnHostWithResponse(ctx, &generated.RequestPullChangesOnHostRequestOptions{PathParams: &generated.RequestPullChangesOnHostPath{PlatformHost: "gitea.test", Provider: "gitea", Owner: "tea", Name: "kettle", Number: int64(7)}, Body: &generated.RequestPullChangesOnHostBody{
		Body:            "needs work",
		ExpectedHeadSha: &expectedHeadSHA,
	}})

	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode, string(resp.Body))
	assert.Equal("REQUEST_CHANGES", transport.lastReviewOpts.State)
	assert.Equal("needs work", transport.lastReviewOpts.Body)
	assert.Equal("abc123", transport.lastReviewOpts.CommitID)
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

func setupGitHubCapabilityServerWithProvider(
	t *testing.T,
	caps *platform.Capabilities,
	headRepoCloneURL string,
) (*server.Server, *db.DB, *apiTestGitLabProvider) {
	t.Helper()
	require := require.New(t)
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Second)

	database := dbtest.Open(t)

	ref := platform.RepoRef{
		Platform:           platform.KindGitHub,
		Host:               "github.example.com",
		Owner:              "acme",
		Name:               "widget",
		RepoPath:           "acme/widget",
		PlatformID:         6262,
		PlatformExternalID: "6262",
		WebURL:             "https://github.example.com/acme/widget",
		CloneURL:           "https://github.example.com/acme/widget.git",
		DefaultBranch:      "main",
	}
	provider := &apiTestGitLabProvider{
		ref:          ref,
		capabilities: caps,
		mergeRequests: []platform.MergeRequest{{
			Repo:               ref,
			PlatformID:         7101,
			PlatformExternalID: "7101",
			Number:             7,
			URL:                "https://github.example.com/acme/widget/pull/7",
			Title:              "GitHub provider PR",
			Author:             "ada",
			State:              "open",
			HeadBranch:         "feature/github",
			HeadRepoCloneURL:   headRepoCloneURL,
			BaseBranch:         "main",
			HeadSHA:            "abc123",
			BaseSHA:            "def456",
			CreatedAt:          now,
			UpdatedAt:          now,
			LastActivityAt:     now,
		}},
	}
	registry, err := platform.NewRegistry(provider)
	require.NoError(err)

	repo := ghclient.RepoRef{
		Platform:           platform.KindGitHub,
		Owner:              "acme",
		Name:               "widget",
		PlatformHost:       "github.example.com",
		RepoPath:           "acme/widget",
		PlatformRepoID:     6262,
		PlatformExternalID: "6262",
		WebURL:             "https://github.example.com/acme/widget",
		CloneURL:           "https://github.example.com/acme/widget.git",
		DefaultBranch:      "main",
	}
	syncer := ghclient.NewSyncerWithRegistry(
		registry, database, nil, []ghclient.RepoRef{repo}, time.Minute, nil, nil,
	)
	t.Cleanup(syncer.Stop)
	srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{})
	t.Cleanup(func() { gracefulShutdown(t, srv) })

	syncer.RunOnce(ctx)
	return srv, database, provider
}

func setupForgejoCapabilityServerWithProvider(
	t *testing.T,
	caps *platform.Capabilities,
) (*server.Server, *db.DB, *apiTestGitLabProvider) {
	t.Helper()
	require := require.New(t)
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Second)

	database := dbtest.Open(t)

	ref := platform.RepoRef{
		Platform:           platform.KindForgejo,
		Host:               "codeberg.org",
		Owner:              "acme",
		Name:               "widgets",
		RepoPath:           "acme/widgets",
		PlatformID:         5252,
		PlatformExternalID: "5252",
		WebURL:             "https://codeberg.org/acme/widgets",
		CloneURL:           "https://codeberg.org/acme/widgets.git",
		DefaultBranch:      "main",
	}
	provider := &apiTestGitLabProvider{
		ref:          ref,
		capabilities: caps,
		mergeRequests: []platform.MergeRequest{{
			Repo:               ref,
			PlatformID:         9001,
			PlatformExternalID: "9001",
			Number:             42,
			URL:                "https://codeberg.org/acme/widgets/pulls/42",
			Title:              "Forgejo provider PR",
			Author:             "ada",
			State:              "open",
			HeadBranch:         "feature/forgejo",
			BaseBranch:         "main",
			HeadSHA:            "abc123",
			BaseSHA:            "def456",
			CreatedAt:          now,
			UpdatedAt:          now,
			LastActivityAt:     now,
		}},
	}
	registry, err := platform.NewRegistry(provider)
	require.NoError(err)

	repo := ghclient.RepoRef{
		Platform:           platform.KindForgejo,
		Owner:              "acme",
		Name:               "widgets",
		PlatformHost:       "codeberg.org",
		RepoPath:           "acme/widgets",
		PlatformRepoID:     5252,
		PlatformExternalID: "5252",
		WebURL:             "https://codeberg.org/acme/widgets",
		CloneURL:           "https://codeberg.org/acme/widgets.git",
		DefaultBranch:      "main",
	}
	syncer := ghclient.NewSyncerWithRegistry(
		registry, database, nil, []ghclient.RepoRef{repo}, time.Minute, nil, nil,
	)
	t.Cleanup(syncer.Stop)
	srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{})
	t.Cleanup(func() { gracefulShutdown(t, srv) })

	syncer.RunOnce(ctx)
	return srv, database, provider
}

func seedStackedPR(
	t *testing.T, database *db.DB,
	owner, name string, number int,
	head, base string, state db.MergeRequestState, ci, review string,
) int64 {
	return seedStackedPRState(t, database, owner, name, number, head, base, state, ci, review, false, "")
}

func seedStackedPRDraft(
	t *testing.T, database *db.DB,
	owner, name string, number int,
	head, base string, state db.MergeRequestState, ci, review string,
	isDraft bool,
) int64 {
	return seedStackedPRState(t, database, owner, name, number, head, base, state, ci, review, isDraft, "")
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

func runStackDetection(t *testing.T, database *db.DB, owner, name string) {
	t.Helper()
	ctx := t.Context()
	repo, err := database.GetRepoByIdentity(ctx, verifiedGitHubRepoIdentity("github.com", owner, name))
	require.NoError(t, err)
	require.NotNil(t, repo)
	require.NoError(t, stacks.RunDetection(ctx, database, repo.ID))
}

func TestAPIGetStackForPR_DraftNotBaseReady(t *testing.T) {
	runParallelServerTest(t)
	assert := assert.New(t)
	srv, database := setupTestServer(t)
	client := setupTestClient(t, srv)

	// Draft base with green CI + approval; non-draft tip pending.
	seedStackedPRDraft(t, database, "acme", "widget", 10, "feat/x", "main", db.MergeRequestStateOpen, "success", "APPROVED", true)
	seedStackedPR(t, database, "acme", "widget", 11, "feat/y", "feat/x", db.MergeRequestStateOpen, "pending", "")
	runStackDetection(t, database, "acme", "widget")

	resp, err := client.HTTP.GetPullStackWithResponse(t.Context(), &generated.GetPullStackRequestOptions{PathParams: &generated.GetPullStackPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(10)}})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.NotNil(t, resp.JSON200)
	assert.NotEqual("base_ready", resp.JSON200.Health, "draft base must not be base_ready")
	assert.NotEqual("all_green", resp.JSON200.Health, "draft stack must not be all_green")
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

func readEventMatching(
	t *testing.T,
	ch <-chan server.RecordedEvent,
	matches func(server.Event) bool,
) server.Event {
	t.Helper()
	timeout := time.After(2 * time.Second)
	for {
		select {
		case rec := <-ch:
			if matches(rec.Event) {
				return rec.Event
			}
		case <-timeout:
			require.FailNow(t, "timed out waiting for matching event")
		}
	}
}

// TestMergeBlocksPredecessorPreservedByNativeStackOverlapFallback closes the
// seam between stack detection and the merge safeguard. Detection tests prove
// the projection and pullapi tests prove the guard blocks on hand-seeded rows;
// neither proves the rows detection writes for an overlap actually preserve the
// preceding blocker the guard reads.
func TestMergeBlocksPredecessorPreservedByNativeStackOverlapFallback(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	merged := false
	mock := &mockGH{
		mergePullRequestFn: func(_ context.Context, _, _ string, _ int, _, _, _ string) (*gh.PullRequestMergeResult, error) {
			merged = true
			return &gh.PullRequestMergeResult{}, nil
		},
	}
	srv, database := setupTestServerWithMock(t, mock)
	ctx := t.Context()
	seedStackedPR(t, database, "acme", "widget", 100, "feature/a", "main", db.MergeRequestStateOpen, "", "")
	seedStackedPR(t, database, "acme", "widget", 101, "feature/b", "feature/a", db.MergeRequestStateMerged, "", "")
	tipHeadSHA := "sha102"
	seedStackedPR(t, database, "acme", "widget", 102, "feature/c", "feature/b", db.MergeRequestStateOpen, "", "")
	repo, err := database.GetRepoByIdentity(ctx, verifiedGitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	require.NotNil(repo)
	now := time.Now().UTC()
	// Two confirmed stacks share merged PR 101, and stack 42's leading member
	// has no row yet, so the overlap is only visible from declared membership.
	require.NoError(database.ReplaceGitHubNativeStack(ctx, db.GitHubNativeStack{
		RepoID: repo.ID, GitHubID: 9043, Number: 43, Size: 2,
		BaseRef: "main", IsOpen: true, GitHubCreatedAt: now,
		ContentFingerprint: "native-43", LastObservedAt: now,
		Members: []db.GitHubNativeStackMember{
			{Position: 1, PullRequestNumber: 101, State: "merged", HeadRef: "feature/b", HeadSHA: "sha101"},
			{Position: 2, PullRequestNumber: 102, State: "open", HeadRef: "feature/c", HeadSHA: tipHeadSHA},
		},
	}))
	require.NoError(database.ReplaceGitHubNativeStack(ctx, db.GitHubNativeStack{
		RepoID: repo.ID, GitHubID: 9042, Number: 42, Size: 2,
		BaseRef: "main", IsOpen: true, GitHubCreatedAt: now,
		ContentFingerprint: "native-42", LastObservedAt: now,
		Members: []db.GitHubNativeStackMember{
			{Position: 1, PullRequestNumber: 900, State: "open", HeadRef: "feature/z", HeadSHA: "sha900"},
			{Position: 2, PullRequestNumber: 101, State: "merged", HeadRef: "feature/b", HeadSHA: "sha101"},
		},
	}))
	require.NoError(stacks.RunDetectionWithNativeStacks(ctx, database, repo.ID, []int{42, 43}))
	client := setupTestClient(t, srv)

	resp, err := client.HTTP.MergePullWithResponse(ctx, &generated.MergePullRequestOptions{PathParams: &generated.MergePullPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(102)}, Body: &generated.MergePRInputBody{
		Method:          "squash",
		ExpectedHeadSha: &tipHeadSHA,
	}})
	require.Error(err)
	require.NotNil(resp)

	assert.Equal(http.StatusConflict, resp.StatusCode)
	assert.Contains(string(resp.Body), `"reason":"mid_stack_merge_disallowed"`)
	assert.Contains(string(resp.Body), `"blocking_number":100`)
	assert.False(merged, "the provider must not be asked to merge past an open predecessor")
}
