package issuetest

import (
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
	ghclient "go.kenn.io/forge/internal/github"
	"go.kenn.io/forge/internal/platformdb"
	"go.kenn.io/forge/internal/server"
	"go.kenn.io/forge/internal/server/httpapi"
	"go.kenn.io/forge/internal/server/issueapi"
	"go.kenn.io/forge/internal/testutil"
	"go.kenn.io/forge/internal/testutil/dbtest"
	"go.kenn.io/forge/internal/testutil/gitsafe"
	"go.kenn.io/forge/internal/testutil/processjob"
	"go.kenn.io/forge/internal/testutil/testsignal"
	"go.kenn.io/forge/internal/testutil/testtmux"
	"go.kenn.io/forge/platform"
	giteaplatform "go.kenn.io/forge/platform/gitea"
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

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestAPIListIssuesFiltersPullRequestReferences(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	srv, database := setupTestServer(t)
	referencedID := seedIssue(t, database, "acme", "widget", 1, "open")
	seedIssue(t, database, "acme", "widget", 2, "open")
	require.NoError(database.UpsertIssueEvents(t.Context(), []db.IssueEvent{{
		IssueID:   referencedID,
		EventType: "cross_referenced",
		MetadataJSON: `{
			"source_type":"PullRequest",
			"source_owner":"acme",
			"source_repo":"client",
			"source_number":42,
			"source_url":"https://github.com/acme/client/pull/42"
		}`,
		CreatedAt: time.Now().UTC(),
		DedupeKey: "cross-reference-42",
	}}))

	rr := testutil.DoJSON(t, srv, http.MethodGet, "/api/v1/issues?state=all&referenced_by_pr=true", nil)
	require.Equal(http.StatusOK, rr.Code, rr.Body.String())
	var issues []issueapi.IssueResponse
	require.NoError(json.Unmarshal(rr.Body.Bytes(), &issues))
	require.Len(issues, 1)
	require.Equal(1, issues[0].Number)
}

func TestAPICreateIssue(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)

	createdAt := time.Date(2026, 4, 17, 12, 0, 0, 0, time.UTC)
	mock := &mockGH{
		createIssueFn: func(_ context.Context, owner, repo, title, body string) (*gh.Issue, error) {
			id := int64(9876)
			number := 27
			state := "open"
			url := fmt.Sprintf("https://github.com/%s/%s/issues/%d", owner, repo, number)
			login := "issue-bot"
			ts := gh.Timestamp{Time: createdAt}
			comments := 0
			labelID := int64(42)
			labelName := "enhancement"
			labelColor := "a2eeef"
			return &gh.Issue{
				ID:       &id,
				Number:   &number,
				Title:    &title,
				Body:     &body,
				State:    &state,
				HTMLURL:  &url,
				User:     &gh.User{Login: &login},
				Comments: &comments,
				Labels: []*gh.Label{{
					ID:    labelID,
					Name:  labelName,
					Color: labelColor,
				}},
				CreatedAt: &ts,
				UpdatedAt: &ts,
			}, nil
		},
	}

	srv, database := setupTestServerWithRepos(
		t,
		mock,
		[]ghclient.RepoRef{
			{Platform: "github", Owner: "acme", Name: "widgets", PlatformHost: "github.com"},
		},
	)
	client := setupTestClient(t, srv)

	_, err := database.UpsertRepo(context.Background(), verifiedGitHubRepoIdentity("github.com", "acme", "widgets"))
	require.NoError(err)

	resp, err := client.HTTP.CreateIssueWithResponse(context.Background(), &generated.CreateIssueRequestOptions{PathParams: &generated.CreateIssuePath{Provider: "gh", Owner: "acme", Name: "widgets"}, Body: &generated.CreateIssueBody{
		Title: "Ship repo summaries",
		Body:  "Add a top-level repository overview page.",
	}})
	require.NoError(err)
	require.Equal(http.StatusCreated, resp.StatusCode)
	require.NotNil(resp.JSON201)

	assert.Equal(int64(27), resp.JSON201.Number)
	assert.Equal("acme", resp.JSON201.RepoOwner)
	assert.Equal("widgets", resp.JSON201.RepoName)
	assert.Equal("Ship repo summaries", resp.JSON201.Title)
	require.NotNil(resp.JSON201.Labels)
	assert.Equal([]generated.Label{{
		Name:      "enhancement",
		Color:     "a2eeef",
		IsDefault: false,
	}}, resp.JSON201.Labels)

	issue, err := database.GetIssue(context.Background(), "github", "github.com", "acme", "widgets", 27)
	require.NoError(err)
	require.NotNil(issue)
	assert.Equal("Ship repo summaries", issue.Title)
	assert.Equal("Add a top-level repository overview page.", issue.Body)
	assert.Equal("open", issue.State)
	assert.Equal(createdAt, issue.CreatedAt.UTC())
	require.Len(issue.Labels, 1)
	assert.Equal("enhancement", issue.Labels[0].Name)
	assert.Equal("a2eeef", issue.Labels[0].Color)
}

func TestAPICreateIssueRejectsNilProviderPayload(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)

	mock := &mockGH{
		createIssueFn: func(context.Context, string, string, string, string) (*gh.Issue, error) {
			return nil, nil
		},
	}
	srv, database := setupTestServerWithRepos(
		t,
		mock,
		[]ghclient.RepoRef{
			{Platform: "github", Owner: "acme", Name: "widgets", PlatformHost: "github.com"},
		},
	)
	client := setupTestClient(t, srv)

	repoID, err := database.UpsertRepo(t.Context(), verifiedGitHubRepoIdentity("github.com", "acme", "widgets"))
	require.NoError(err)

	resp, err := client.HTTP.CreateIssueWithResponse(t.Context(), &generated.CreateIssueRequestOptions{PathParams: &generated.CreateIssuePath{Provider: "gh", Owner: "acme", Name: "widgets"}, Body: &generated.CreateIssueBody{Title: "Empty payload"}})
	require.Error(err)
	require.NotNil(resp)
	require.Equal(http.StatusBadGateway, resp.StatusCode)

	issue, err := database.GetIssueByRepoIDAndNumber(t.Context(), repoID, 0)
	require.NoError(err)
	require.Nil(issue)
}

func TestAPICreateIssueReportsUnknownOutcomeForUnverifiedProviderFailure(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	srv := setupGitLabIssueMutatorServer(t, errors.New("provider response unavailable"))
	client := setupTestClient(t, srv)

	resp, err := client.HTTP.CreateIssueOnHostWithResponse(t.Context(), &generated.CreateIssueOnHostRequestOptions{PathParams: &generated.CreateIssueOnHostPath{PlatformHost: "gitlab.example.com", Provider: "gl", Owner: "group", Name: "project"}, Body: &generated.CreateIssueOnHostBody{Title: "Unverified issue"}})
	require.Error(err)
	require.NotNil(resp)
	require.Equal(http.StatusBadGateway, resp.StatusCode, string(resp.Body))
	require.NotNil(resp.Error)

	assert.Equal(generated.ProblemErrorCodeMutationOutcomeUnknown, resp.Error.Code)
	require.NotNil(resp.Error.Details)
	assert.Equal("gitlab", resp.Error.Details["provider"])
	assert.Equal("gitlab.example.com", resp.Error.Details["platformHost"])
}

func TestAPICreateIssueReportsUnknownOutcomeWhenPersistenceFailsAfterProviderSuccess(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	var database *db.DB
	var persistenceSetupErr error
	createdAt := time.Date(2026, 4, 17, 12, 0, 0, 0, time.UTC)
	mock := &mockGH{
		createIssueFn: func(ctx context.Context, owner, repo, title, body string) (*gh.Issue, error) {
			_, persistenceSetupErr = database.WriteDB().ExecContext(ctx, "DROP TABLE forge_issue_labels")
			return &gh.Issue{
				ID:        new(int64(9876)),
				Number:    new(27),
				Title:     &title,
				Body:      &body,
				State:     new("open"),
				HTMLURL:   new(fmt.Sprintf("https://github.com/%s/%s/issues/27", owner, repo)),
				User:      &gh.User{Login: new("issue-bot")},
				CreatedAt: &gh.Timestamp{Time: createdAt},
				UpdatedAt: &gh.Timestamp{Time: createdAt},
			}, nil
		},
	}
	srv, openedDatabase := setupTestServerWithRepos(
		t,
		mock,
		[]ghclient.RepoRef{{
			Platform: "github", Owner: "acme", Name: "widgets", PlatformHost: "github.com",
		}},
	)
	database = openedDatabase
	client := setupTestClient(t, srv)

	resp, err := client.HTTP.CreateIssueWithResponse(t.Context(), &generated.CreateIssueRequestOptions{PathParams: &generated.CreateIssuePath{Provider: "gh", Owner: "acme", Name: "widgets"}, Body: &generated.CreateIssueBody{Title: "Persist this issue"}})
	require.Error(err)
	require.NotNil(resp)
	require.NoError(persistenceSetupErr)
	require.Equal(http.StatusBadGateway, resp.StatusCode, string(resp.Body))
	require.NotNil(resp.Error)

	assert.Equal(generated.ProblemErrorCodeMutationOutcomeUnknown, resp.Error.Code)
	require.NotNil(resp.Error.Details)
	assert.Equal("github", resp.Error.Details["provider"])
	assert.Equal("github.com", resp.Error.Details["platformHost"])
}

func TestAPIPostIssueCommentRejectsNilProviderPayload(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)

	mock := &mockGH{
		createIssueCommentFn: func(context.Context, string, string, int, string) (*gh.IssueComment, error) {
			return nil, nil
		},
	}
	srv, database := setupTestServerWithMock(t, mock)
	issueID := seedIssue(t, database, "acme", "widget", 5, "open")
	client := setupTestClient(t, srv)

	resp, err := client.HTTP.PostIssueCommentWithResponse(t.Context(), &generated.PostIssueCommentRequestOptions{PathParams: &generated.PostIssueCommentPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(5)}, Body: &generated.PostIssueCommentBody{Body: "Looks good"}})
	require.Error(err)
	require.NotNil(resp)
	require.Equal(http.StatusBadGateway, resp.StatusCode)

	events, err := database.ListIssueEvents(t.Context(), issueID)
	require.NoError(err)
	require.Empty(events)
}

func TestAPIEditIssueCommentRejectsNilProviderPayload(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)

	mock := &mockGH{
		editIssueCommentFn: func(context.Context, string, string, int64, string) (*gh.IssueComment, error) {
			return nil, nil
		},
	}
	srv, database := setupTestServerWithMock(t, mock)
	issueID := seedIssue(t, database, "acme", "widget", 5, "open")
	commentID := int64(42)
	require.NoError(database.UpsertIssueEvents(t.Context(), []db.IssueEvent{{
		IssueID:    issueID,
		PlatformID: &commentID,
		EventType:  "issue_comment",
		Body:       "original body",
		CreatedAt:  time.Now().UTC(),
		DedupeKey:  "issue-comment-42",
	}}))
	client := setupTestClient(t, srv)

	resp, err := client.HTTP.EditIssueCommentWithResponse(t.Context(), &generated.EditIssueCommentRequestOptions{PathParams: &generated.EditIssueCommentPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(5), CommentID: int64(commentID)}, Body: &generated.EditIssueCommentBody{Body: "edited body"}})
	require.Error(err)
	require.NotNil(resp)
	require.Equal(http.StatusBadGateway, resp.StatusCode)

	events, err := database.ListIssueEvents(t.Context(), issueID)
	require.NoError(err)
	require.Len(events, 1)
	assert.Equal("original body", events[0].Body)
}

func TestAPICreateIssueUsesPlatformHost(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)

	database := dbtest.Open(t)

	githubCalled := false
	enterpriseCalled := false
	githubClient := &mockGH{
		createIssueFn: func(_ context.Context, _, _, _, _ string) (*gh.Issue, error) {
			githubCalled = true
			return nil, errors.New("wrong host")
		},
	}
	enterpriseClient := &mockGH{
		createIssueFn: func(_ context.Context, owner, repo, title, body string) (*gh.Issue, error) {
			enterpriseCalled = true
			number := 44
			state := "open"
			url := fmt.Sprintf("https://ghe.example.com/%s/%s/issues/%d", owner, repo, number)
			login := "issue-bot"
			ts := gh.Timestamp{Time: time.Date(2026, 4, 18, 12, 0, 0, 0, time.UTC)}
			return &gh.Issue{
				Number:    &number,
				Title:     &title,
				Body:      &body,
				State:     &state,
				HTMLURL:   &url,
				User:      &gh.User{Login: &login},
				CreatedAt: &ts,
				UpdatedAt: &ts,
			}, nil
		},
	}
	repos := []ghclient.RepoRef{
		{Platform: "github", Owner: "acme", Name: "widgets", PlatformHost: "github.com"},
		{Owner: "acme", Name: "widgets", PlatformHost: "ghe.example.com"},
	}
	syncer := ghclient.NewSyncer(
		map[string]ghclient.Client{
			"github.com":      githubClient,
			"ghe.example.com": enterpriseClient,
		},
		database,
		nil,
		repos,
		time.Minute,
		nil,
		nil,
	)
	t.Cleanup(syncer.Stop)
	srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{})
	t.Cleanup(func() { gracefulShutdown(t, srv) })

	_, err := database.UpsertRepo(context.Background(), verifiedGitHubRepoIdentity("github.com", "acme", "widgets"))
	require.NoError(err)
	enterpriseRepoID, err := database.UpsertRepo(context.Background(), verifiedGitHubRepoIdentity("ghe.example.com", "acme", "widgets"))
	require.NoError(err)

	client := setupTestClient(t, srv)
	resp, err := client.HTTP.CreateIssueOnHostWithResponse(t.Context(), &generated.CreateIssueOnHostRequestOptions{PathParams: &generated.CreateIssueOnHostPath{PlatformHost: "ghe.example.com", Provider: "gh", Owner: "acme", Name: "widgets"}, Body: &generated.CreateIssueOnHostBody{
		Title: "Ship enterprise issue",
		Body:  "Route to the selected host.",
	}})
	require.NoError(err)
	require.Equal(http.StatusCreated, resp.StatusCode)
	assert.False(githubCalled)
	assert.True(enterpriseCalled)
	issue, err := database.GetIssueByRepoIDAndNumber(
		context.Background(),
		enterpriseRepoID,
		44,
	)
	require.NoError(err)
	require.NotNil(issue)
	assert.Equal("Ship enterprise issue", issue.Title)
}

func TestAPIEditIssueCommentUpdatesGitHubAndLocalTimeline(t *testing.T) {
	runParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	commentID := int64(1234)
	createdAt := time.Date(2026, 4, 29, 13, 0, 0, 0, time.UTC)
	mock := &mockGH{
		editIssueCommentFn: func(_ context.Context, owner, repo string, gotCommentID int64, body string) (*gh.IssueComment, error) {
			assert.Equal("acme", owner)
			assert.Equal("widget", repo)
			assert.Equal(commentID, gotCommentID)
			assert.Equal("edited issue body", body)
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
	issueID := seedIssue(t, database, "acme", "widget", 5, "open")
	require.NoError(database.UpsertIssueEvents(t.Context(), []db.IssueEvent{{
		IssueID:      issueID,
		PlatformID:   &commentID,
		EventType:    "issue_comment",
		Author:       "maintainer",
		Body:         "original issue body",
		MetadataJSON: `{"provider_hidden":true,"provider_hidden_reason":"OFF_TOPIC"}`,
		CreatedAt:    createdAt,
		DedupeKey:    "issue-comment-1234",
	}}))

	req := httptest.NewRequestWithContext(t.Context(),
		http.MethodPatch,
		"/api/v1/issues/gh/acme/widget/5/comments/1234",
		strings.NewReader(`{"body":"edited issue body"}`),
	)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	srv.ServeHTTP(rec, req)

	require.Equal(http.StatusOK, rec.Code)
	events, err := database.ListIssueEvents(t.Context(), issueID)
	require.NoError(err)
	require.Len(events, 1)
	assert.Equal("edited issue body", events[0].Body)
	assert.Equal("maintainer", events[0].Author)
	assert.JSONEq(`{"provider_hidden":true,"provider_hidden_reason":"OFF_TOPIC"}`, events[0].MetadataJSON)
	require.NotNil(events[0].PlatformID)
	assert.Equal(commentID, *events[0].PlatformID)
}

func TestAPIEditIssueCommentRejectsCommentFromDifferentIssue(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	commentID := int64(6666)
	var editCalls atomic.Int32
	mock := &mockGH{
		editIssueCommentFn: func(_ context.Context, _, _ string, _ int64, _ string) (*gh.IssueComment, error) {
			editCalls.Add(1)
			return nil, nil
		},
	}
	srv, database := setupTestServerWithMock(t, mock)
	routeIssueID := seedIssue(t, database, "acme", "widget", 5, "open")
	otherIssueID := seedIssue(t, database, "acme", "widget", 6, "open")
	require.NotEqual(routeIssueID, otherIssueID)
	require.NoError(database.UpsertIssueEvents(t.Context(), []db.IssueEvent{{
		IssueID:    otherIssueID,
		PlatformID: &commentID,
		EventType:  "issue_comment",
		Author:     "maintainer",
		Body:       "other issue body",
		CreatedAt:  time.Now().UTC(),
		DedupeKey:  "issue-comment-6666",
	}}))

	req := httptest.NewRequestWithContext(t.Context(),
		http.MethodPatch,
		"/api/v1/issues/gh/acme/widget/5/comments/6666",
		strings.NewReader(`{"body":"wrong target"}`),
	)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	srv.ServeHTTP(rec, req)

	require.Equal(http.StatusNotFound, rec.Code)
	require.Equal(int32(0), editCalls.Load())
}

func TestAPIDeleteIssueCommentKeepsLocalCommentWhenProviderRejects(t *testing.T) {
	runParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	commentID := int64(1234)
	mock := &mockGH{
		deleteIssueCommentFn: func(context.Context, string, string, int64) error {
			return errors.New("provider denied deletion")
		},
	}
	srv, database := setupTestServerWithMock(t, mock)
	issueID := seedIssue(t, database, "acme", "widget", 5, "open")
	require.NoError(database.UpsertIssueEvents(t.Context(), []db.IssueEvent{{
		IssueID:    issueID,
		PlatformID: &commentID,
		EventType:  "issue_comment",
		Author:     "maintainer",
		Body:       "keep me",
		CreatedAt:  time.Now().UTC(),
		DedupeKey:  "issue-comment-1234",
	}}))

	req := httptest.NewRequestWithContext(t.Context(), http.MethodDelete, "/api/v1/issues/gh/acme/widget/5/comments/1234", nil)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	assert.Equal(http.StatusBadGateway, rec.Code)
	events, err := database.ListIssueEvents(t.Context(), issueID)
	require.NoError(err)
	require.Len(events, 1)
	assert.Equal("keep me", events[0].Body)
}

func TestAPIDeleteIssueCommentKeepsLocalCommentWhenProviderReportsNotFound(t *testing.T) {
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
	issueID := seedIssue(t, database, "acme", "widget", 5, "open")
	require.NoError(database.UpsertIssueEvents(t.Context(), []db.IssueEvent{{
		IssueID:    issueID,
		PlatformID: &commentID,
		EventType:  "issue_comment",
		Author:     "maintainer",
		Body:       "keep until deletion is confirmed",
		CreatedAt:  time.Now().UTC(),
		DedupeKey:  "issue-comment-4321",
	}}))

	req := httptest.NewRequestWithContext(t.Context(), http.MethodDelete, "/api/v1/issues/gh/acme/widget/5/comments/4321", nil)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	assert.Equal(http.StatusNotFound, rec.Code)
	events, err := database.ListIssueEvents(t.Context(), issueID)
	require.NoError(err)
	require.Len(events, 1)
	assert.Equal("keep until deletion is confirmed", events[0].Body)
}

func TestAPIDeleteIssueCommentLeavesLocalStateForSync(t *testing.T) {
	runParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	commentID := int64(5432)
	var deleteCalls atomic.Int32
	mock := &mockGH{
		deleteIssueCommentFn: func(context.Context, string, string, int64) error {
			deleteCalls.Add(1)
			return nil
		},
	}
	srv, database := setupTestServerWithMock(t, mock)
	issueID := seedIssue(t, database, "acme", "widget", 5, "open")
	require.NoError(database.UpsertIssueEvents(t.Context(), []db.IssueEvent{{
		IssueID:    issueID,
		PlatformID: &commentID,
		EventType:  "issue_comment",
		Author:     "maintainer",
		Body:       "remove from issue detail",
		CreatedAt:  time.Now().UTC(),
		DedupeKey:  "issue-comment-5432",
	}}))

	deleteReq := httptest.NewRequestWithContext(t.Context(), http.MethodDelete, "/api/v1/issues/gh/acme/widget/5/comments/5432", nil)
	deleteReq.Header.Set("Content-Type", "application/json")
	deleteRec := httptest.NewRecorder()
	srv.ServeHTTP(deleteRec, deleteReq)
	require.Equal(http.StatusNoContent, deleteRec.Code, deleteRec.Body.String())
	assert.Equal(int32(1), deleteCalls.Load())

	detailReq := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/issues/gh/acme/widget/5", nil)
	detailRec := httptest.NewRecorder()
	srv.ServeHTTP(detailRec, detailReq)
	require.Equal(http.StatusOK, detailRec.Code, detailRec.Body.String())
	var detail issueapi.IssueDetailResponse
	require.NoError(json.NewDecoder(detailRec.Body).Decode(&detail))
	require.Len(detail.Events, 1)
	assert.Equal("remove from issue detail", detail.Events[0].Body)
}

func TestAPIGitLabDisabledIssueCooldownPersistsThroughHTTPAndSQLite(t *testing.T) {
	runParallelServerTest(t)

	assert := assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	var metadataCalls atomic.Int32
	var issueCalls atomic.Int32
	var mergeRequestCalls atomic.Int32
	providerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.EscapedPath() {
		case "/api/v4/projects/42":
			metadataCalls.Add(1)
			_, _ = io.WriteString(w, `{
				"id":42,"path":"project","path_with_namespace":"group/project",
				"web_url":"https://gitlab.test/group/project",
				"http_url_to_repo":"https://gitlab.test/group/project.git",
				"default_branch":"main","issues_access_level":"disabled",
				"merge_requests_access_level":"enabled"
			}`)
		case "/api/v4/projects/42/issues":
			issueCalls.Add(1)
			http.Error(w, "issues are disabled", http.StatusNotFound)
		case "/api/v4/projects/42/merge_requests":
			mergeRequestCalls.Add(1)
			_, _ = io.WriteString(w, `[{
				"id":7001,"iid":7,"project_id":42,"source_project_id":42,
				"title":"unaffected merge request","state":"opened",
				"web_url":"https://gitlab.test/group/project/-/merge_requests/7",
				"author":{"username":"ada"},"source_branch":"feature",
				"target_branch":"main","sha":"head-sha",
				"created_at":"2026-07-22T12:00:00Z",
				"updated_at":"2026-07-22T12:01:00Z"
			}]`)
		case "/api/v4/projects/42/releases",
			"/api/v4/projects/42/repository/tags",
			"/api/v4/projects/42/labels":
			_, _ = io.WriteString(w, `[]`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(providerServer.Close)

	provider, err := platformgitlab.NewClient(
		"gitlab.test",
		testTokenSource("token"),
		platformgitlab.WithBaseURLForTesting(providerServer.URL+"/api/v4"),
		platformgitlab.WithoutRetriesForTesting(), platformgitlab.
			WithTransport(http.DefaultTransport),
	)
	require.NoError(err)
	registry, err := platform.NewRegistry(provider)
	require.NoError(err)
	database := dbtest.Open(t)
	ref := ghclient.RepoRef{
		Platform: platform.KindGitLab, PlatformHost: "gitlab.test",
		Owner: "group", Name: "project", RepoPath: "group/project",
		PlatformRepoID: 42, PlatformExternalID: "42",
		WebURL:   "https://gitlab.test/group/project",
		CloneURL: "https://gitlab.test/group/project.git", DefaultBranch: "main",
	}
	syncer := ghclient.NewSyncerWithRegistry(
		registry, database, nil, []ghclient.RepoRef{ref}, time.Minute, nil, nil,
	)
	t.Cleanup(syncer.Stop)
	srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{})
	t.Cleanup(func() { gracefulShutdown(t, srv) })

	syncer.RunOnce(ctx)
	syncer.RunOnce(ctx)

	assert.Equal(int32(1), issueCalls.Load())
	assert.Equal(int32(2), mergeRequestCalls.Load())
	assert.Equal(int32(3), metadataCalls.Load())
	repo, err := database.GetRepoByIdentity(ctx, db.RepoIdentity{
		Platform: "gitlab", PlatformHost: "gitlab.test", RepoPath: "group/project",
	})
	require.NoError(err)
	require.NotNil(repo)
	assert.Empty(repo.LastSyncError)
	stored, err := database.GetMergeRequestByRepoIDAndNumber(ctx, repo.ID, 7)
	require.NoError(err)
	require.NotNil(stored)
	assert.Equal("unaffected merge request", stored.Title)
}

func TestAPIGiteaDisabledIssueCooldownPersistsThroughHTTPAndSQLite(t *testing.T) {
	runParallelServerTest(t)

	assert := assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	var metadataCalls atomic.Int32
	var issueCalls atomic.Int32
	var mergeRequestCalls atomic.Int32
	providerServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.EscapedPath() {
		case "/api/v1/repos/tea/kettle":
			metadataCalls.Add(1)
			_, _ = io.WriteString(w, `{
				"id":101,"name":"kettle","full_name":"tea/kettle",
				"html_url":"https://gitea.test/tea/kettle",
				"clone_url":"http://gitea.test/tea/kettle.git",
				"default_branch":"main","owner":{"login":"tea"},
				"has_issues":false,"has_pull_requests":true,
				"created_at":"2026-07-22T12:00:00Z",
				"updated_at":"2026-07-22T12:01:00Z"
			}`)
		case "/api/v1/repos/tea/kettle/issues":
			issueCalls.Add(1)
			http.Error(w, "issues are disabled", http.StatusNotFound)
		case "/api/v1/repos/tea/kettle/pulls":
			mergeRequestCalls.Add(1)
			_, _ = io.WriteString(w, `[{
				"id":201,"number":7,
				"html_url":"https://gitea.test/tea/kettle/pulls/7",
				"title":"unaffected pull request","state":"open",
				"user":{"login":"ada"},
				"head":{"ref":"feature","sha":"head-sha"},
				"base":{"ref":"main","sha":"base-sha"},
				"created_at":"2026-07-22T12:00:00Z",
				"updated_at":"2026-07-22T12:01:00Z"
			}]`)
		case "/api/v1/repos/tea/kettle/releases",
			"/api/v1/repos/tea/kettle/tags",
			"/api/v1/repos/tea/kettle/labels":
			_, _ = io.WriteString(w, `[]`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(providerServer.Close)

	provider, err := giteaplatform.NewClient(
		"gitea.test",
		testTokenSource("token"),
		giteaplatform.WithBaseURL(providerServer.URL, true),
		giteaplatform.WithServerVersion("1.26.0"), giteaplatform.WithTransport(http.DefaultTransport))
	require.NoError(err)
	registry, err := platform.NewRegistry(provider)
	require.NoError(err)
	database := dbtest.Open(t)
	ref := ghclient.RepoRef{
		Platform: platform.KindGitea, PlatformHost: "gitea.test",
		Owner: "tea", Name: "kettle", RepoPath: "tea/kettle",
		PlatformRepoID: 101, PlatformExternalID: "101",
		WebURL:   "https://gitea.test/tea/kettle",
		CloneURL: "https://gitea.test/tea/kettle.git", DefaultBranch: "main",
	}
	syncer := ghclient.NewSyncerWithRegistry(
		registry, database, nil, []ghclient.RepoRef{ref}, time.Minute, nil, nil,
	)
	t.Cleanup(syncer.Stop)
	srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{})
	t.Cleanup(func() { gracefulShutdown(t, srv) })

	syncer.RunOnce(ctx)
	syncer.RunOnce(ctx)

	assert.Equal(int32(1), issueCalls.Load())
	assert.Equal(int32(2), mergeRequestCalls.Load())
	assert.Equal(int32(3), metadataCalls.Load())
	repo, err := database.GetRepoByIdentity(ctx, db.RepoIdentity{
		Platform: "gitea", PlatformHost: "gitea.test", RepoPath: "tea/kettle",
	})
	require.NoError(err)
	require.NotNil(repo)
	assert.Empty(repo.LastSyncError)
	assert.Equal("http://gitea.test/tea/kettle.git", repo.CloneURL)
	stored, err := database.GetMergeRequestByRepoIDAndNumber(ctx, repo.ID, 7)
	require.NoError(err)
	require.NotNil(stored)
	assert.Equal("unaffected pull request", stored.Title)
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

func TestAPIReopenIssue(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	srv, database := setupTestServer(t)
	seedIssue(t, database, "acme", "widget", 5, "closed")
	client := setupTestClient(t, srv)

	resp, err := client.HTTP.SetIssueGithubStateWithResponse(t.Context(), &generated.SetIssueGithubStateRequestOptions{PathParams: &generated.SetIssueGithubStatePath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(5)}, Body: &generated.SetIssueGithubStateBody{State: "open"}})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)

	issue, err := database.GetIssue(t.Context(), "github", "github.com", "acme", "widget", 5)
	require.NoError(err)
	require.Equal("open", issue.State)
	require.Nil(issue.ClosedAt, "closed_at should be cleared on reopen")
}

func TestAPIEnqueueIssueSyncReturnsBeforeGitHubFetchCompletes(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)

	syncStarted := make(chan struct{}, 1)
	releaseSync := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() {
		releaseOnce.Do(func() { close(releaseSync) })
	})

	mock := &mockGH{
		getIssueFn: func(_ context.Context, _, _ string, number int) (*gh.Issue, error) {
			syncStarted <- struct{}{}
			<-releaseSync

			id := int64(202)
			state := "open"
			title := "fresh async issue"
			url := "https://github.com/acme/widget/issues/5"
			author := "alice"
			now := gh.Timestamp{Time: time.Now().UTC()}
			return &gh.Issue{
				ID:        &id,
				Number:    &number,
				State:     &state,
				Title:     &title,
				HTMLURL:   &url,
				User:      &gh.User{Login: &author},
				CreatedAt: &now,
				UpdatedAt: &now,
			}, nil
		},
	}

	srv, database := setupTestServerWithMock(t, mock)
	seedIssue(t, database, "acme", "widget", 5, "open")
	client := setupTestClient(t, srv)

	ctx, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
	defer cancel()
	resp, err := client.HTTP.EnqueueIssueSyncWithResponse(ctx, &generated.EnqueueIssueSyncRequestOptions{PathParams: &generated.EnqueueIssueSyncPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(5)}})
	require.NoError(err)
	require.Equal(http.StatusAccepted, resp.StatusCode)

	select {
	case <-syncStarted:
	case <-time.After(2 * time.Second):
		require.Fail("background issue sync did not start")
	}
	releaseOnce.Do(func() { close(releaseSync) })
}

func TestAPISyncIssueDoesNotOverwriteNewerStateChange(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)

	staleUpdatedAt := time.Date(2026, 4, 12, 1, 0, 0, 0, time.UTC)
	syncStarted := make(chan struct{}, 1)
	releaseSync := make(chan struct{})
	mock := &mockGH{
		getIssueFn: func(_ context.Context, owner, repo string, number int) (*gh.Issue, error) {
			syncStarted <- struct{}{}
			<-releaseSync

			id := int64(202)
			state := "open"
			title := "stale issue sync"
			url := "https://github.com/acme/widget/issues/5"
			author := "alice"
			createdAt := gh.Timestamp{Time: staleUpdatedAt.Add(-time.Hour)}
			updatedAt := gh.Timestamp{Time: staleUpdatedAt}
			return &gh.Issue{
				ID:        &id,
				Number:    &number,
				State:     &state,
				Title:     &title,
				HTMLURL:   &url,
				User:      &gh.User{Login: &author},
				CreatedAt: &createdAt,
				UpdatedAt: &updatedAt,
			}, nil
		},
	}

	srv, database := setupTestServerWithMock(t, mock)
	seedIssue(t, database, "acme", "widget", 5, "open")
	client := setupTestClient(t, srv)

	syncDone := make(chan *generated.SyncIssueResp, 1)
	syncErr := make(chan error, 1)
	go func() {
		resp, err := client.HTTP.SyncIssueWithResponse(t.Context(), &generated.SyncIssueRequestOptions{PathParams: &generated.SyncIssuePath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(5)}})
		if err != nil {
			syncErr <- err
			return
		}
		syncDone <- resp
	}()

	<-syncStarted

	resp, err := client.HTTP.SetIssueGithubStateWithResponse(t.Context(), &generated.SetIssueGithubStateRequestOptions{PathParams: &generated.SetIssueGithubStatePath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(5)}, Body: &generated.SetIssueGithubStateBody{State: "closed"}})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)

	closedIssue, err := database.GetIssue(t.Context(), "github", "github.com", "acme", "widget", 5)
	require.NoError(err)
	require.Equal("closed", closedIssue.State)
	require.NotNil(closedIssue.ClosedAt)

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
	require.True(completed, "timed out waiting for stale issue sync")

	finalIssue, err := database.GetIssue(t.Context(), "github", "github.com", "acme", "widget", 5)
	require.NoError(err)
	assert.Equal("closed", finalIssue.State)
	assert.NotNil(finalIssue.ClosedAt)
	assert.Equal("Test Issue", finalIssue.Title)
	assert.True(finalIssue.UpdatedAt.After(staleUpdatedAt))
}

// TestAPISyncIssueNilUpdatedAtFallsBackToCreatedAt drives the full
// HTTP handler -> syncer -> SQLite path with a GitHub response that
// has updated_at: null, and verifies last_activity_at falls back to
// created_at via the nil guard in refreshIssueTimeline. The sync_test
// unit tests cover the same logic at the syncer layer; this test
// covers the request path users actually hit in production.
func TestAPISyncIssueNilUpdatedAtFallsBackToCreatedAt(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	ctx := t.Context()

	createdAt := time.Date(2025, 3, 14, 9, 0, 0, 0, time.UTC)
	mock := &mockGH{
		getIssueFn: func(_ context.Context, _, _ string, number int) (*gh.Issue, error) {
			id := int64(9999)
			state := "open"
			title := "nil updated_at"
			url := "https://github.com/acme/widget/issues/9"
			author := "alice"
			createdTs := gh.Timestamp{Time: createdAt}
			return &gh.Issue{
				ID:        &id,
				Number:    &number,
				State:     &state,
				Title:     &title,
				HTMLURL:   &url,
				User:      &gh.User{Login: &author},
				CreatedAt: &createdTs,
				UpdatedAt: nil,
			}, nil
		},
	}

	srv, database := setupTestServerWithMock(t, mock)
	seedIssue(t, database, "acme", "widget", 9, "open")
	repo, err := database.GetRepoByIdentity(ctx, verifiedGitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	_, err = database.WriteDB().ExecContext(ctx, `
		UPDATE forge_issues
		SET created_at = ?, updated_at = ?, last_activity_at = ?
		WHERE repo_id = ? AND number = 9`,
		createdAt.Add(-time.Hour), createdAt.Add(-time.Hour), createdAt.Add(-time.Hour), repo.ID)
	require.NoError(err)
	client := setupTestClient(t, srv)

	// Before the nil guard, refreshIssueTimeline panicked on
	// ghIssue.UpdatedAt.Time and the handler returned 502.
	syncResp, err := client.HTTP.SyncIssueWithResponse(ctx, &generated.SyncIssueRequestOptions{PathParams: &generated.SyncIssuePath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(9)}})
	require.NoError(err)
	require.Equal(http.StatusOK, syncResp.StatusCode)
	require.NotNil(syncResp.JSON200)
	// LastActivityAt must equal CreatedAt, not Go's zero time.
	// Without the fallback, activity-ordered views would sort
	// this issue at 0001-01-01 instead of its creation date.
	assert.False(syncResp.JSON200.Issue.LastActivityAt.IsZero())
	assert.Equal(createdAt, syncResp.JSON200.Issue.LastActivityAt.UTC())

	// Verify the persisted value round-trips through the read
	// endpoint so the storage -> serializer path is covered.
	getResp, err := client.HTTP.GetIssueWithResponse(ctx, &generated.GetIssueRequestOptions{PathParams: &generated.GetIssuePath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(9)}})
	require.NoError(err)
	require.Equal(http.StatusOK, getResp.StatusCode)
	require.NotNil(getResp.JSON200)
	assert.Equal(createdAt, getResp.JSON200.Issue.LastActivityAt.UTC())
}

func TestAPIListIssuesSearchByNumber(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	srv, database := setupTestServer(t)
	ctx := t.Context()

	seedIssueOnHost(t, database, "github.com", "acme", "widget", 12, "open", "report a bug")
	issueID := seedIssueOnHost(t, database, "github.com", "acme", "widget", 278, "open", "filter broken")
	seedIssueOnHost(t, database, "github.com", "acme", "widget", 290, "open", "another change")
	seedIssueOnHost(t, database, "github.com", "tools", "worker", 301, "open", "triage bug")
	seedIssueOnHost(t, database, "github.com", "docs", "reader", 302, "open", "O'Reilly reference")
	repo, err := database.GetRepoByIdentity(ctx, verifiedGitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	require.NoError(database.ReplaceIssueLabels(ctx, repo.ID, issueID, []db.Label{{
		PlatformID: 300,
		Name:       "needs-triage",
		Color:      "d73a4a",
		UpdatedAt:  time.Now().UTC(),
	}}))

	client := setupTestClient(t, srv)

	issueNumbers := func(params *generated.ListIssuesQuery) []int {
		t.Helper()
		resp, err := client.HTTP.ListIssuesWithResponse(ctx, &generated.ListIssuesRequestOptions{Query: params})
		require.NoError(err)
		require.Equal(http.StatusOK, resp.StatusCode)
		require.NotNil(resp.JSON200)
		nums := make([]int, 0, len(*resp.JSON200))
		for _, issue := range *resp.JSON200 {
			nums = append(nums, int(issue.Number))
		}
		return nums
	}

	q := "278"
	assert.ElementsMatch([]int{278}, issueNumbers(&generated.ListIssuesQuery{Q: &q}))

	q = "#278"
	assert.ElementsMatch([]int{278}, issueNumbers(&generated.ListIssuesQuery{Q: &q}))

	// Title still matches.
	q = "broken"
	assert.ElementsMatch([]int{278}, issueNumbers(&generated.ListIssuesQuery{Q: &q}))

	q = "filter widget"
	assert.ElementsMatch([]int{278}, issueNumbers(&generated.ListIssuesQuery{Q: &q}))

	q = "work bug"
	assert.ElementsMatch([]int{301}, issueNumbers(&generated.ListIssuesQuery{Q: &q}))

	q = "needs-triage filter"
	assert.ElementsMatch([]int{278}, issueNumbers(&generated.ListIssuesQuery{Q: &q}))

	q = "O'Reilly"
	assert.ElementsMatch([]int{302}, issueNumbers(&generated.ListIssuesQuery{Q: &q}))

	q = "needs-triage"
	assert.ElementsMatch([]int{278}, issueNumbers(&generated.ListIssuesQuery{Q: &q}))

	// Substring of number matches multiple.
	q = "2"
	assert.ElementsMatch([]int{12, 278, 290, 302}, issueNumbers(&generated.ListIssuesQuery{Q: &q}))
}

func TestAPIListIssuesAcceptsProviderAndHostQualifiedRepoFilter(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)

	srv, database := setupTestServer(t)
	seedIssueOnHost(t, database, "github.com", "acme", "widget", 5, "open", "GitHub issue")
	seedIssueOnHost(t, database, "ghe.example.com", "acme", "widget", 7, "open", "Enterprise issue")
	client := setupTestClient(t, srv)

	repo := "github|ghe.example.com/acme/widget"
	resp, err := client.HTTP.ListIssuesWithResponse(t.Context(), &generated.ListIssuesRequestOptions{Query: &generated.ListIssuesQuery{Repo: &repo}})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)
	require.NotNil(resp.JSON200)
	require.Len(*resp.JSON200, 1)
	assert.Equal("ghe.example.com", (*resp.JSON200)[0].PlatformHost)
	assert.Equal("acme", (*resp.JSON200)[0].RepoOwner)
	assert.Equal("widget", (*resp.JSON200)[0].RepoName)
	assert.EqualValues(7, (*resp.JSON200)[0].Number)
}

func TestAPIListIssuesFiltersProviderQualifiedHostedNestedRepoPath(t *testing.T) {
	runParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)

	srv, database := setupTestServerWithRepos(t, &mockGH{}, []ghclient.RepoRef{
		{Owner: "Group/SubGroup", Name: "Project.Special", PlatformHost: "ghe.example.com"},
		{Owner: "other", Name: "repo", PlatformHost: "ghe.example.com"},
	})
	seedIssueOnHost(
		t, database,
		"ghe.example.com", "Group/SubGroup", "Project.Special", 1,
		"open", "Nested issue",
	)
	seedIssueOnHost(
		t, database,
		"ghe.example.com", "other", "repo", 2,
		"open", "Other issue",
	)
	client := setupTestClient(t, srv)

	repo := "github|ghe.example.com/Group/SubGroup/Project.Special"
	resp, err := client.HTTP.ListIssuesWithResponse(t.Context(), &generated.ListIssuesRequestOptions{Query: &generated.ListIssuesQuery{Repo: &repo}})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)
	require.NotNil(resp.JSON200)
	require.Len(*resp.JSON200, 1)
	assert.Equal("ghe.example.com", (*resp.JSON200)[0].PlatformHost)
	assert.Equal("group/subgroup", (*resp.JSON200)[0].RepoOwner)
	assert.Equal("project.special", (*resp.JSON200)[0].RepoName)
	assert.EqualValues(1, (*resp.JSON200)[0].Number)
}

func TestAPIListIssuesAcceptsProviderQualifiedRepoFilter(t *testing.T) {
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
	seedIssueForRepo(t, database, githubRepo, "github.com", "acme", "widget", 1, "open", "GitHub issue")
	seedIssueForRepo(t, database, giteaRepo, "github.com", "acme", "widget", 2, "open", "Gitea issue")

	repo := "gitea|github.com/acme/widget"
	resp, err := client.HTTP.ListIssuesWithResponse(ctx, &generated.ListIssuesRequestOptions{Query: &generated.ListIssuesQuery{Repo: &repo}})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)
	require.NotNil(resp.JSON200)
	require.Len(*resp.JSON200, 1)
	assert.Equal("gitea", (*resp.JSON200)[0].Repo.Provider)
	assert.Equal("github.com", (*resp.JSON200)[0].PlatformHost)
	assert.Equal("acme", (*resp.JSON200)[0].RepoOwner)
	assert.Equal("widget", (*resp.JSON200)[0].RepoName)
	assert.EqualValues(2, (*resp.JSON200)[0].Number)
}

func TestAPIGetIssueUsesPlatformHostQuery(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)

	database := dbtest.Open(t)

	seedIssueOnHost(
		t, database,
		"github.com", "acme", "widget", 7,
		"open", "GitHub issue",
	)
	seedIssueOnHost(
		t, database,
		"ghe.example.com", "acme", "widget", 7,
		"open", "GHES issue",
	)

	syncer := ghclient.NewSyncer(
		map[string]ghclient.Client{
			"github.com":      &mockGH{},
			"ghe.example.com": &mockGH{},
		},
		database,
		nil,
		[]ghclient.RepoRef{
			{Platform: "github", Owner: "acme", Name: "widget", PlatformHost: "github.com"},
			{Platform: "github", Owner: "acme", Name: "widget", PlatformHost: "ghe.example.com"},
		},
		time.Minute,
		nil,
		nil,
	)
	t.Cleanup(syncer.Stop)

	srv := server.New(
		database, syncer, nil, "/", nil, server.ServerOptions{},
	)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(
			context.Background(), 5*time.Second,
		)
		defer cancel()
		_ = srv.Shutdown(ctx)
	})

	req := httptest.NewRequestWithContext(t.Context(),
		http.MethodGet,
		"/api/v1/host/ghe.example.com/issues/gh/acme/widget/7",
		nil,
	)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	require.Equal(http.StatusOK, rr.Code)
	var body rawIssueDetailResponse
	require.NoError(json.Unmarshal(rr.Body.Bytes(), &body))
	assert.Equal("ghe.example.com", body.PlatformHost)
	if assert.NotNil(body.Issue) {
		assert.Equal("GHES issue", body.Issue.Title)
	}
}

func TestAPISyncIssueUsesPlatformHostQuery(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	ctx := context.Background()

	database := dbtest.Open(t)

	seedIssueOnHost(
		t, database,
		"github.com", "acme", "widget", 7,
		"open", "GitHub stale issue",
	)
	seedIssueOnHost(
		t, database,
		"ghe.example.com", "acme", "widget", 7,
		"open", "GHES stale issue",
	)

	githubClient := &mockGH{
		getIssueFn: func(_ context.Context, owner, repo string, number int) (*gh.Issue, error) {
			title := "GitHub synced issue"
			state := "open"
			url := fmt.Sprintf("https://github.com/%s/%s/issues/%d", owner, repo, number)
			createdAt := gh.Timestamp{Time: time.Now().UTC()}
			updatedAt := gh.Timestamp{Time: time.Now().UTC()}
			return &gh.Issue{
				Number:    &number,
				Title:     &title,
				State:     &state,
				HTMLURL:   &url,
				CreatedAt: &createdAt,
				UpdatedAt: &updatedAt,
				User:      &gh.User{Login: new("github-user")},
			}, nil
		},
	}
	ghesClient := &mockGH{
		getIssueFn: func(_ context.Context, owner, repo string, number int) (*gh.Issue, error) {
			title := "GHES synced issue"
			state := "open"
			url := fmt.Sprintf("https://ghe.example.com/%s/%s/issues/%d", owner, repo, number)
			createdAt := gh.Timestamp{Time: time.Now().UTC()}
			updatedAt := gh.Timestamp{Time: time.Now().UTC()}
			return &gh.Issue{
				Number:    &number,
				Title:     &title,
				State:     &state,
				HTMLURL:   &url,
				CreatedAt: &createdAt,
				UpdatedAt: &updatedAt,
				User:      &gh.User{Login: new("ghes-user")},
			}, nil
		},
	}

	syncer := ghclient.NewSyncer(
		map[string]ghclient.Client{
			"github.com":      githubClient,
			"ghe.example.com": ghesClient,
		},
		database,
		nil,
		[]ghclient.RepoRef{
			{Platform: "github", Owner: "acme", Name: "widget", PlatformHost: "github.com"},
			{Platform: "github", Owner: "acme", Name: "widget", PlatformHost: "ghe.example.com"},
		},
		time.Minute,
		nil,
		nil,
	)
	t.Cleanup(syncer.Stop)

	srv := server.New(
		database, syncer, nil, "/", nil, server.ServerOptions{},
	)
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(
			context.Background(), 5*time.Second,
		)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	})

	req := httptest.NewRequestWithContext(t.Context(),
		http.MethodPost,
		"/api/v1/host/ghe.example.com/issues/gh/acme/widget/7/sync",
		http.NoBody,
	).WithContext(ctx)
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	require.Equal(http.StatusOK, rr.Code)
	var body rawIssueDetailResponse
	require.NoError(json.Unmarshal(rr.Body.Bytes(), &body))
	assert.Equal("ghe.example.com", body.PlatformHost)
	if assert.NotNil(body.Issue) {
		assert.Equal("GHES synced issue", body.Issue.Title)
	}
	// The frontend replaces the issue detail payload with this
	// response: it must carry operations or every mutation gate would
	// clear after a sync.
	var withOps struct {
		Repo struct {
			Operations *httpapi.RepoOperations `json:"operations"`
		} `json:"repo"`
	}
	require.NoError(json.Unmarshal(rr.Body.Bytes(), &withOps))
	require.NotNil(withOps.Repo.Operations,
		"issue sync response must include repo.operations")

	githubRepo, err := database.GetRepoByIdentity(ctx, verifiedGitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	require.NotNil(githubRepo)
	githubIssue, err := database.GetIssueByRepoIDAndNumber(
		ctx, githubRepo.ID, 7,
	)
	require.NoError(err)
	require.NotNil(githubIssue)
	assert.Equal("GitHub stale issue", githubIssue.Title)

	ghesRepo, err := database.GetRepoByIdentity(ctx, verifiedGitHubRepoIdentity("ghe.example.com", "acme", "widget"))
	require.NoError(err)
	require.NotNil(ghesRepo)
	ghesIssue, err := database.GetIssueByRepoIDAndNumber(
		ctx, ghesRepo.ID, 7,
	)
	require.NoError(err)
	require.NotNil(ghesIssue)
	assert.Equal("GHES synced issue", ghesIssue.Title)
}

func TestAPISetIssueStateUsesPlatformHostBody(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	ctx := context.Background()

	database := dbtest.Open(t)

	seedIssueOnHost(
		t, database,
		"github.com", "acme", "widget", 7,
		"open", "GitHub issue",
	)
	seedIssueOnHost(
		t, database,
		"ghe.example.com", "acme", "widget", 7,
		"open", "GHES issue",
	)

	githubClient := &mockGH{
		editIssueFn: func(_ context.Context, _, _ string, number int, state string) (*gh.Issue, error) {
			url := fmt.Sprintf("https://github.com/acme/widget/issues/%d", number)
			createdAt := gh.Timestamp{Time: time.Now().UTC()}
			updatedAt := gh.Timestamp{Time: time.Now().UTC()}
			title := "GitHub issue"
			return &gh.Issue{
				Number:    &number,
				Title:     &title,
				State:     &state,
				HTMLURL:   &url,
				CreatedAt: &createdAt,
				UpdatedAt: &updatedAt,
				User:      &gh.User{Login: new("github-user")},
			}, nil
		},
	}
	ghesClient := &mockGH{
		editIssueFn: func(_ context.Context, _, _ string, number int, state string) (*gh.Issue, error) {
			url := fmt.Sprintf("https://ghe.example.com/acme/widget/issues/%d", number)
			createdAt := gh.Timestamp{Time: time.Now().UTC()}
			updatedAt := gh.Timestamp{Time: time.Now().UTC()}
			title := "GHES issue"
			return &gh.Issue{
				Number:    &number,
				Title:     &title,
				State:     &state,
				HTMLURL:   &url,
				CreatedAt: &createdAt,
				UpdatedAt: &updatedAt,
				User:      &gh.User{Login: new("ghes-user")},
			}, nil
		},
	}

	syncer := ghclient.NewSyncer(
		map[string]ghclient.Client{
			"github.com":      githubClient,
			"ghe.example.com": ghesClient,
		},
		database,
		nil,
		[]ghclient.RepoRef{
			{Platform: "github", Owner: "acme", Name: "widget", PlatformHost: "github.com"},
			{Platform: "github", Owner: "acme", Name: "widget", PlatformHost: "ghe.example.com"},
		},
		time.Minute,
		nil,
		nil,
	)
	t.Cleanup(syncer.Stop)

	srv := server.New(
		database, syncer, nil, "/", nil, server.ServerOptions{},
	)
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(
			context.Background(), 5*time.Second,
		)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	})

	client := setupTestClient(t, srv)
	resp, err := client.HTTP.SetIssueGithubStateOnHostWithResponse(ctx, &generated.SetIssueGithubStateOnHostRequestOptions{PathParams: &generated.SetIssueGithubStateOnHostPath{PlatformHost: "ghe.example.com", Provider: "gh", Owner: "acme", Name: "widget", Number: int64(7)}, Body: &generated.SetIssueGithubStateOnHostBody{State: "closed"}})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)

	githubRepo, err := database.GetRepoByIdentity(ctx, verifiedGitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	require.NotNil(githubRepo)
	githubIssue, err := database.GetIssueByRepoIDAndNumber(
		ctx, githubRepo.ID, 7,
	)
	require.NoError(err)
	require.NotNil(githubIssue)
	assert.Equal("open", githubIssue.State)

	ghesRepo, err := database.GetRepoByIdentity(ctx, verifiedGitHubRepoIdentity("ghe.example.com", "acme", "widget"))
	require.NoError(err)
	require.NotNil(ghesRepo)
	ghesIssue, err := database.GetIssueByRepoIDAndNumber(
		ctx, ghesRepo.ID, 7,
	)
	require.NoError(err)
	require.NotNil(ghesIssue)
	assert.Equal("closed", ghesIssue.State)
}

// TestAPIIssueDataFromGraphQLSync verifies the API correctly serves
// issue data that was persisted by the GraphQL sync path. The sync
// path itself (GraphQL fetch → normalize → DB upsert) is tested in
// internal/github/sync_test.go; this test covers the DB → API layer.
func TestAPIIssueDataFromGraphQLSync(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	ctx := t.Context()

	mock := &mockGH{}
	srv, database := setupTestServerWithMock(t, mock)
	client := setupTestClient(t, srv)

	// Seed DB directly — same shape as GraphQL sync output.
	repoID, err := database.UpsertRepo(ctx, verifiedGitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)

	now := time.Now().UTC().Truncate(time.Second)
	issueID, err := database.UpsertIssue(ctx, &db.Issue{
		RepoID:         repoID,
		PlatformID:     60000,
		Number:         60,
		URL:            "https://github.com/acme/widget/issues/60",
		Title:          "GraphQL synced issue",
		Author:         "testuser",
		State:          "open",
		Body:           "Synced via GraphQL",
		CommentCount:   1,
		CreatedAt:      now,
		UpdatedAt:      now,
		LastActivityAt: now,
	})
	require.NoError(err)

	// Add a label
	require.NoError(database.ReplaceIssueLabels(ctx, repoID, issueID, []db.Label{
		{PlatformID: 1, Name: "bug", Color: "d73a4a", UpdatedAt: now},
	}))

	// Add a comment event
	require.NoError(database.UpsertIssueEvents(ctx, []db.IssueEvent{
		{
			IssueID:   issueID,
			EventType: "issue_comment",
			Author:    "commenter",
			Body:      "I can reproduce",
			CreatedAt: now,
			DedupeKey: "issue-comment-601",
		},
	}))

	// Verify via ListIssues API
	resp, err := client.HTTP.ListIssuesWithResponse(ctx, &generated.ListIssuesRequestOptions{})
	require.NoError(err)
	require.Equal(200, resp.StatusCode)
	require.NotNil(resp.JSON200)
	require.Len(*resp.JSON200, 1)

	apiIssue := (*resp.JSON200)[0]
	assert.Equal(int64(60), apiIssue.Number)
	assert.Equal("GraphQL synced issue", apiIssue.Title)
	assert.Equal("testuser", apiIssue.Author)
	assert.Equal("open", apiIssue.State)
	require.NotNil(apiIssue.Labels)
	require.Len(apiIssue.Labels, 1)
	assert.Equal("bug", apiIssue.Labels[0].Name)

	// Verify via GetIssue API
	detailResp, err := client.HTTP.GetIssueWithResponse(ctx, &generated.GetIssueRequestOptions{PathParams: &generated.GetIssuePath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(60)}})
	require.NoError(err)
	require.Equal(200, detailResp.StatusCode)
	require.NotNil(detailResp.JSON200)
	assert.Equal("Synced via GraphQL", detailResp.JSON200.Issue.Body)
	assert.Equal(int64(1), detailResp.JSON200.Issue.CommentCount)
}

func make422Error() error {
	return &gh.ErrorResponse{
		Response: &http.Response{StatusCode: http.StatusUnprocessableEntity},
		Message:  "Validation Failed",
	}
}

func TestAPISetIssueGitHubStateReturns404WhenNoClientConfigured(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	repos := []ghclient.RepoRef{{Owner: "acme", Name: "widget", PlatformHost: "ghe.corp.com"}}
	srv, database := setupTestServerWithRepos(t, &mockGH{}, repos)
	ctx := t.Context()

	repoID, err := database.UpsertRepo(ctx, verifiedGitHubRepoIdentity("ghe.corp.com", "acme", "widget"))
	require.NoError(err)
	_, err = database.UpsertIssue(ctx, &db.Issue{
		RepoID:         repoID,
		PlatformID:     5000,
		Number:         5,
		URL:            "https://ghe.corp.com/acme/widget/issues/5",
		Title:          "Issue",
		Author:         "u",
		State:          "open",
		CreatedAt:      time.Now().UTC().Truncate(time.Second),
		UpdatedAt:      time.Now().UTC().Truncate(time.Second),
		LastActivityAt: time.Now().UTC().Truncate(time.Second),
	})
	require.NoError(err)

	client := setupTestClient(t, srv)
	resp, err := client.HTTP.SetIssueGithubStateWithResponse(ctx, &generated.SetIssueGithubStateRequestOptions{PathParams: &generated.SetIssueGithubStatePath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(5)}, Body: &generated.SetIssueGithubStateBody{State: "closed"}})
	require.Error(err)
	require.NotNil(resp)
	require.Equal(http.StatusNotFound, resp.StatusCode)
}

func TestAPICloseIssue422NilFallbackPayloadDoesNotCorruptDB(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	mock := &mockGH{
		editIssueFn: func(_ context.Context, _, _ string, _ int, _ string) (*gh.Issue, error) {
			return nil, make422Error()
		},
		getIssueFn: func(_ context.Context, _, _ string, _ int) (*gh.Issue, error) {
			return nil, nil
		},
	}
	srv, database := setupTestServerWithMock(t, mock)
	seedIssue(t, database, "acme", "widget", 5, "open")
	before, err := database.GetIssue(t.Context(), "github", "github.com", "acme", "widget", 5)
	require.NoError(err)
	require.NotNil(before)

	client := setupTestClient(t, srv)
	resp, err := client.HTTP.SetIssueGithubStateWithResponse(t.Context(), &generated.SetIssueGithubStateRequestOptions{PathParams: &generated.SetIssueGithubStatePath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(5)}, Body: &generated.SetIssueGithubStateBody{State: "closed"}})
	require.Error(err)
	require.NotNil(resp)
	require.Equal(http.StatusBadGateway, resp.StatusCode)

	after, err := database.GetIssue(t.Context(), "github", "github.com", "acme", "widget", 5)
	require.NoError(err)
	require.NotNil(after)
	assert.Equal(before.State, after.State)
	assert.Equal(before.UpdatedAt, after.UpdatedAt)
	assert.Nil(after.ClosedAt)
}

func TestResolveItem_Issue(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	repos := []ghclient.RepoRef{{Owner: "acme", Name: "widget"}}
	srv, database := setupTestServerWithRepos(t, &mockGH{}, repos)
	seedIssue(t, database, "acme", "widget", 7, "open")
	client := setupTestClient(t, srv)

	resp, err := client.HTTP.ResolveRepoItemWithResponse(t.Context(), &generated.ResolveRepoItemRequestOptions{PathParams: &generated.ResolveRepoItemPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(7)}})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)
	require.NotNil(resp.JSON200)
	require.Equal("issue", resp.JSON200.ItemType)
	require.EqualValues(7, resp.JSON200.Number)
	require.True(resp.JSON200.RepoTracked)
}

func TestAPICloseIssue422AlreadyClosed(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	state := "closed"
	mock := &mockGH{
		editIssueFn: func(_ context.Context, _, _ string, _ int, _ string) (*gh.Issue, error) {
			return nil, make422Error()
		},
		getIssueFn: func(_ context.Context, _, _ string, _ int) (*gh.Issue, error) {
			id := int64(5000)
			now := gh.Timestamp{Time: time.Now().UTC()}
			closedAt := gh.Timestamp{Time: time.Now().UTC()}
			return &gh.Issue{
				ID: &id, Number: new(5), State: &state,
				Title: new("Issue"), HTMLURL: new("https://example.com"),
				User:      &gh.User{Login: new("u")},
				CreatedAt: &now, UpdatedAt: &now, ClosedAt: &closedAt,
			}, nil
		},
	}
	srv, database := setupTestServerWithMock(t, mock)
	seedIssue(t, database, "acme", "widget", 5, "open")
	client := setupTestClient(t, srv)

	resp, err := client.HTTP.SetIssueGithubStateWithResponse(t.Context(), &generated.SetIssueGithubStateRequestOptions{PathParams: &generated.SetIssueGithubStatePath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(5)}, Body: &generated.SetIssueGithubStateBody{State: "closed"}})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)

	issue, _ := database.GetIssue(t.Context(), "github", "github.com", "acme", "widget", 5)
	require.Equal("closed", issue.State)
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

func cleanupServerTestTmux(owner *testtmux.Owner) error {
	if owner == nil {
		return nil
	}
	return owner.Cleanup()
}

type rawIssueWorkspaceRef struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

type rawIssueSummary struct {
	Title string `json:"title"`
	State string `json:"state"`
}

type rawIssueDetailResponse struct {
	Issue        *rawIssueSummary      `json:"issue"`
	PlatformHost string                `json:"platform_host"`
	RepoOwner    string                `json:"repo_owner"`
	RepoName     string                `json:"repo_name"`
	Workspace    *rawIssueWorkspaceRef `json:"workspace"`
}

func TestAPIEditIssueBodyOnly(t *testing.T) {
	require := require.New(t)
	srv, database := setupTestServer(t)
	seedIssue(t, database, "acme", "widget", 5, "open")

	rr := testutil.DoJSON(t, srv, http.MethodPatch,
		"/api/v1/issues/gh/acme/widget/5",
		map[string]string{"body": "- [x] task done"})

	require.Equal(http.StatusOK, rr.Code)

	issue, err := database.GetIssue(t.Context(), "github", "github.com", "acme", "widget", 5)
	require.NoError(err)
	require.NotNil(issue)
	require.Equal("Test Issue", issue.Title)
	require.Equal("- [x] task done", issue.Body)
}

func TestAPIEditIssueTitleAndBody(t *testing.T) {
	require := require.New(t)
	srv, database := setupTestServer(t)
	seedIssue(t, database, "acme", "widget", 5, "open")

	rr := testutil.DoJSON(t, srv, http.MethodPatch,
		"/api/v1/issues/gh/acme/widget/5",
		map[string]string{"title": "new title", "body": "new body"})

	require.Equal(http.StatusOK, rr.Code)

	issue, err := database.GetIssue(t.Context(), "github", "github.com", "acme", "widget", 5)
	require.NoError(err)
	require.NotNil(issue)
	require.Equal("new title", issue.Title)
	require.Equal("new body", issue.Body)
}

func TestAPIEditIssueNoFields400(t *testing.T) {
	require := require.New(t)
	srv, database := setupTestServer(t)
	seedIssue(t, database, "acme", "widget", 5, "open")

	rr := testutil.DoJSON(t, srv, http.MethodPatch,
		"/api/v1/issues/gh/acme/widget/5",
		map[string]any{})

	require.Equal(http.StatusBadRequest, rr.Code)
}

func TestAPIEditIssueBlankTitle400(t *testing.T) {
	require := require.New(t)
	srv, database := setupTestServer(t)
	seedIssue(t, database, "acme", "widget", 5, "open")

	rr := testutil.DoJSON(t, srv, http.MethodPatch,
		"/api/v1/issues/gh/acme/widget/5",
		map[string]string{"title": "   "})

	require.Equal(http.StatusBadRequest, rr.Code)
}

func TestAPIEditIssueMissing404(t *testing.T) {
	require := require.New(t)
	srv, database := setupTestServer(t)
	// Register the repo without the issue.
	_, err := database.UpsertRepo(
		t.Context(),
		verifiedGitHubRepoIdentity("github.com", "acme", "widget"),
	)
	require.NoError(err)

	rr := testutil.DoJSON(t, srv, http.MethodPatch,
		"/api/v1/issues/gh/acme/widget/999",
		map[string]string{"body": "anything"})

	require.Equal(http.StatusNotFound, rr.Code)
}
