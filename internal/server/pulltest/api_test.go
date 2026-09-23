package pulltest

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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
	"go.kenn.io/forge/internal/testutil/gitfake"
	"go.kenn.io/forge/internal/testutil/gitfixture"
	"go.kenn.io/forge/internal/testutil/gitsafe"
	"go.kenn.io/forge/internal/testutil/processjob"
	"go.kenn.io/forge/internal/testutil/testsignal"
	"go.kenn.io/forge/internal/testutil/testtmux"
	"go.kenn.io/forge/internal/tokenauth"
	"go.kenn.io/forge/platform"
	"go.kenn.io/forge/platform/gitealike"
	platformgithub "go.kenn.io/forge/platform/github"
	platformgitlab "go.kenn.io/forge/platform/gitlab"
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

type seedPROpt func(*db.MergeRequest)

func withSeedPRHeadSHA(headSHA string) seedPROpt {
	return func(pr *db.MergeRequest) { pr.PlatformHeadSHA = headSHA }
}

func withSeedPRTitle(title string) seedPROpt {
	return func(pr *db.MergeRequest) { pr.Title = title }
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

func seedPRWithHeadSHA(t *testing.T, database *db.DB, owner, name string, number int, headSHA string) int64 {
	t.Helper()
	return seedPR(t, database, owner, name, number, withSeedPRHeadSHA(headSHA))
}

func TestAPIListPulls(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	srv, database := setupTestServer(t)
	seedPR(t, database, "acme", "widget", 1)
	client := setupTestClient(t, srv)

	resp, err := client.HTTP.ListPullsWithResponse(t.Context(), &generated.ListPullsRequestOptions{})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)
	require.NotNil(resp.JSON200)
	require.Len(*resp.JSON200, 1)
	assert := assert.New(t)
	assert.Equal("acme", (*resp.JSON200)[0].RepoOwner)
	assert.Equal("widget", (*resp.JSON200)[0].RepoName)
	assert.Equal("github.com", (*resp.JSON200)[0].PlatformHost)

	raw := testutil.DoJSON(t, srv, http.MethodGet, "/api/v1/pulls", nil)
	require.Equal(http.StatusOK, raw.Code)
	var body []struct {
		Repo httpapi.RepoRefResponse `json:"repo"`
	}
	require.NoError(json.Unmarshal(raw.Body.Bytes(), &body))
	require.Len(body, 1)
	assert.Equal("github", body[0].Repo.Provider)
	assert.Equal("github.com", body[0].Repo.PlatformHost)
	assert.Equal("acme/widget", body[0].Repo.RepoPath)
}

func TestAPIPullResponsesNormalizeMissingKanbanStateToNew(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	srv, database := setupTestServer(t)
	ctx := t.Context()

	repoID, err := database.UpsertRepo(ctx, verifiedGitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	now := time.Now().UTC().Truncate(time.Second)
	mrID, err := database.UpsertMergeRequest(ctx, &db.MergeRequest{
		RepoID:         repoID,
		PlatformID:     7000,
		Number:         7,
		URL:            "https://github.com/acme/widget/pull/7",
		Title:          "Default kanban PR",
		Author:         "alice",
		State:          "open",
		HeadBranch:     "feature/default-kanban",
		BaseBranch:     "main",
		CreatedAt:      now,
		UpdatedAt:      now,
		LastActivityAt: now,
	})
	require.NoError(err)
	kanbanState, err := database.GetKanbanState(ctx, mrID)
	require.NoError(err)
	require.Nil(kanbanState)

	rawList := testutil.DoJSON(t, srv, http.MethodGet, "/api/v1/pulls", nil)
	require.Equal(http.StatusOK, rawList.Code)
	var list []pullapi.MergeRequestResponse
	require.NoError(json.Unmarshal(rawList.Body.Bytes(), &list))
	require.Len(list, 1)
	assert.Equal(db.KanbanStatusNew, list[0].KanbanStatus)

	rawNewList := testutil.DoJSON(t, srv, http.MethodGet, "/api/v1/pulls?kanban=new", nil)
	require.Equal(http.StatusOK, rawNewList.Code)
	var newList []pullapi.MergeRequestResponse
	require.NoError(json.Unmarshal(rawNewList.Body.Bytes(), &newList))
	require.Len(newList, 1)
	assert.Equal(db.KanbanStatusNew, newList[0].KanbanStatus)

	rawDetail := testutil.DoJSON(t, srv, http.MethodGet, "/api/v1/pulls/gh/acme/widget/7", nil)
	require.Equal(http.StatusOK, rawDetail.Code)
	var detail pullapi.MergeRequestDetailResponse
	require.NoError(json.Unmarshal(rawDetail.Body.Bytes(), &detail))
	require.NotNil(detail.MergeRequest)
	assert.Equal(db.KanbanStatusNew, detail.MergeRequest.KanbanStatus)
}

// TestAPIGetPullIncludesCIChecks confirms the PR-detail response decodes the
// merge request's cached ci_checks_json into a top-level checks array.
func TestAPIGetPullIncludesCIChecks(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	srv, database := setupTestServer(t)
	ctx := t.Context()

	repoID, err := database.UpsertRepo(ctx, verifiedGitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	now := time.Now().UTC().Truncate(time.Second)
	checksJSON := `[{"name":"build","status":"completed","conclusion":"success","url":"https://ci.example/build"},` +
		`{"name":"lint","status":"in_progress","conclusion":"","url":"https://ci.example/lint"}]`
	_, err = database.UpsertMergeRequest(ctx, &db.MergeRequest{
		RepoID:         repoID,
		PlatformID:     7000,
		Number:         7,
		URL:            "https://github.com/acme/widget/pull/7",
		Title:          "Checks PR",
		Author:         "alice",
		State:          "open",
		HeadBranch:     "feature/checks",
		BaseBranch:     "main",
		CIStatus:       "pending",
		CIChecksJSON:   checksJSON,
		CreatedAt:      now,
		UpdatedAt:      now,
		LastActivityAt: now,
	})
	require.NoError(err)

	rawDetail := testutil.DoJSON(t, srv, http.MethodGet, "/api/v1/pulls/gh/acme/widget/7", nil)
	require.Equal(http.StatusOK, rawDetail.Code)
	var detail pullapi.MergeRequestDetailResponse
	require.NoError(json.Unmarshal(rawDetail.Body.Bytes(), &detail))
	require.Len(detail.Checks, 2)
	assert.Equal("build", detail.Checks[0].Name)
	assert.Equal("completed", detail.Checks[0].Status)
	assert.Equal("success", detail.Checks[0].Conclusion)
	assert.Equal("https://ci.example/build", detail.Checks[0].URL)
	assert.Equal("lint", detail.Checks[1].Name)
	assert.Equal("in_progress", detail.Checks[1].Status)
}

// TestAPIGetPullToleratesMalformedCIChecks confirms a corrupt ci_checks_json
// cache does not fail the detail response: checks are simply omitted.
func TestAPIGetPullToleratesMalformedCIChecks(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	srv, database := setupTestServer(t)
	ctx := t.Context()

	repoID, err := database.UpsertRepo(ctx, verifiedGitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	now := time.Now().UTC().Truncate(time.Second)
	_, err = database.UpsertMergeRequest(ctx, &db.MergeRequest{
		RepoID:         repoID,
		PlatformID:     7000,
		Number:         7,
		URL:            "https://github.com/acme/widget/pull/7",
		Title:          "Corrupt checks PR",
		Author:         "alice",
		State:          "open",
		HeadBranch:     "feature/corrupt",
		BaseBranch:     "main",
		CIChecksJSON:   "not valid json",
		CreatedAt:      now,
		UpdatedAt:      now,
		LastActivityAt: now,
	})
	require.NoError(err)

	rawDetail := testutil.DoJSON(t, srv, http.MethodGet, "/api/v1/pulls/gh/acme/widget/7", nil)
	require.Equal(http.StatusOK, rawDetail.Code)
	var detail pullapi.MergeRequestDetailResponse
	require.NoError(json.Unmarshal(rawDetail.Body.Bytes(), &detail))
	require.Empty(detail.Checks, "malformed checks cache yields no checks, not an error")
}

func TestAPIGetPullIsDBOnly(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	mock := &mockGH{
		getPullRequestFn: func(_ context.Context, _, _ string, _ int) (*gh.PullRequest, error) {
			require.Fail("GET pull detail should not call GitHub API")
			return nil, nil
		},
		listWorkflowRunsForHeadFn: func(_ context.Context, _, _, _ string) ([]*gh.WorkflowRun, error) {
			require.Fail("GET pull detail should not call ListWorkflowRunsForHeadSHA")
			return nil, nil
		},
	}

	srv, database := setupTestServerWithMock(t, mock)
	seedPRWithHeadSHA(t, database, "acme", "widget", 1, "deadbeef")
	client := setupTestClient(t, srv)

	resp, err := client.HTTP.GetPullWithResponse(t.Context(), &generated.GetPullRequestOptions{PathParams: &generated.GetPullPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)
	require.NotNil(resp.JSON200)
	require.NotNil(resp.JSON200.MergeRequest)
	// Seeded PR has no DetailFetchedAt, so detail_loaded should be false.
	assert.False(resp.JSON200.DetailLoaded)
	assert.Nil(resp.JSON200.DetailFetchedAt)
	// GET path uses DB state (useLivePR=false) and must not make
	// any live GitHub calls, including ListWorkflowRunsForHeadSHA.
	// WorkflowApproval is empty (zero value) since the DB-only path
	// returns early without checking workflows.
	require.NotNil(resp.JSON200.WorkflowApproval)
	assert.False(resp.JSON200.WorkflowApproval.Checked)
}

func TestAPISyncPRIncludesWorkflowApproval(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	mock := &mockGH{
		getPullRequestFn: func(_ context.Context, owner, repo string, number int) (*gh.PullRequest, error) {
			id := int64(1001)
			sha := "abc123"
			state := "open"
			title := "Synced PR"
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
					ID:           new(int64(77)),
					HeadSHA:      new("abc123"),
					Event:        new("pull_request"),
					PullRequests: []*gh.PullRequest{{Number: new(1)}},
				},
			}, nil
		},
	}

	srv, database := setupTestServerWithMock(t, mock)
	seedPR(t, database, "acme", "widget", 1)
	client := setupTestClient(t, srv)

	resp, err := client.HTTP.SyncPullWithResponse(t.Context(), &generated.SyncPullRequestOptions{PathParams: &generated.SyncPullPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)
	require.NotNil(resp.JSON200)
	// Sync response uses workflowCheckRuns mode: reads PR state
	// from DB (just synced) and fetches workflow runs live.
	require.NotNil(resp.JSON200.WorkflowApproval)
	assert.True(resp.JSON200.WorkflowApproval.Checked)
	assert.True(resp.JSON200.WorkflowApproval.Required)
	assert.Equal(int64(1), resp.JSON200.WorkflowApproval.Count)
}

func TestAPIEnqueuePRSyncQueuesOneRerun(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	secondDone := make(chan struct{})
	var calls atomic.Int64

	mock := &mockGH{
		getPullRequestFn: func(ctx context.Context, _, _ string, number int) (*gh.PullRequest, error) {
			call := calls.Add(1)
			if call == 1 {
				close(firstStarted)
				select {
				case <-releaseFirst:
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			}

			id := int64(2000 + call)
			sha := fmt.Sprintf("head-%d", call)
			state := "open"
			title := "Async synced PR"
			url := "https://github.com/acme/widget/pull/1"
			now := gh.Timestamp{Time: time.Now().UTC()}
			pr := &gh.PullRequest{
				ID: &id, Number: &number, State: &state, Title: &title,
				HTMLURL: &url, UpdatedAt: &now, CreatedAt: &now,
				Head: &gh.PullRequestBranch{SHA: &sha, Ref: new("feature")},
				Base: &gh.PullRequestBranch{Ref: new("main")},
			}
			if call == 2 {
				close(secondDone)
			}
			return pr, nil
		},
	}

	srv, database := setupTestServerWithMock(t, mock)
	seedPR(t, database, "acme", "widget", 1)
	client := setupTestClient(t, srv)

	resp, err := client.HTTP.EnqueuePrSyncWithResponse(t.Context(), &generated.EnqueuePrSyncRequestOptions{PathParams: &generated.EnqueuePrSyncPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}})
	require.NoError(err)
	require.Equal(http.StatusAccepted, resp.StatusCode)
	select {
	case <-firstStarted:
	case <-time.After(10 * time.Second):
		require.Fail("Condition never satisfied")
	}

	for range 3 {
		resp, err = client.HTTP.EnqueuePrSyncWithResponse(t.Context(), &generated.EnqueuePrSyncRequestOptions{PathParams: &generated.EnqueuePrSyncPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}})
		require.NoError(err)
		require.Equal(http.StatusAccepted, resp.StatusCode)
	}
	close(releaseFirst)

	// The rerun re-verifies repository identity and persists the settings
	// observation before its PR fetch; give the loaded CI runner headroom.
	select {
	case <-secondDone:
	case <-time.After(10 * time.Second):
		require.Fail("Condition never satisfied")
	}
	assert.Equal(int64(2), calls.Load())
	assert.Never(
		func() bool { return calls.Load() > 2 },
		100*time.Millisecond,
		time.Millisecond,
		"duplicate async sync requests must collapse to one pending rerun",
	)
}

// TestAPIGetPullClearsWorkflowApprovalWhenHeadMoves verifies that a
// persisted "required" approval from a prior head SHA does not bleed
// onto the new head. After a sync that moves the head forward (and
// finds no pending runs), GET must report checked=true, required=false.
func TestAPIGetPullClearsWorkflowApprovalWhenHeadMoves(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)

	var headSHA atomic.Value
	headSHA.Store("abc123")

	mock := &mockGH{
		getPullRequestFn: func(_ context.Context, _, _ string, number int) (*gh.PullRequest, error) {
			id := int64(2002)
			sha := headSHA.Load().(string)
			state := "open"
			title := "Force-pushed PR"
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
		listWorkflowRunsForHeadFn: func(_ context.Context, _, _, sha string) ([]*gh.WorkflowRun, error) {
			if sha == "abc123" {
				return []*gh.WorkflowRun{
					{
						ID:           new(int64(77)),
						HeadSHA:      new("abc123"),
						Event:        new("pull_request"),
						PullRequests: []*gh.PullRequest{{Number: new(1)}},
					},
				}, nil
			}
			return nil, nil
		},
	}

	srv, database := setupTestServerWithMock(t, mock)
	seedPR(t, database, "acme", "widget", 1)
	client := setupTestClient(t, srv)

	// First sync: persists required=true for abc123.
	syncResp, err := client.HTTP.SyncPullWithResponse(t.Context(), &generated.SyncPullRequestOptions{PathParams: &generated.SyncPullPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}})
	require.NoError(err)
	require.Equal(http.StatusOK, syncResp.StatusCode)
	require.True(syncResp.JSON200.WorkflowApproval.Required)

	// Head moves forward (force-push); new SHA has no action_required runs.
	headSHA.Store("def456")
	syncResp2, err := client.HTTP.SyncPullWithResponse(t.Context(), &generated.SyncPullRequestOptions{PathParams: &generated.SyncPullPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}})
	require.NoError(err)
	require.Equal(http.StatusOK, syncResp2.StatusCode)

	detail, err := client.HTTP.GetPullWithResponse(t.Context(), &generated.GetPullRequestOptions{PathParams: &generated.GetPullPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}})
	require.NoError(err)
	require.NotNil(detail.JSON200)
	wa := detail.JSON200.WorkflowApproval
	assert.True(wa.Checked, "head re-synced: approval state was rechecked")
	assert.False(wa.Required, "new head has no pending runs")
	assert.Equal(int64(0), wa.Count)
}

// TestAPISyncPRIncludesWorkflowApprovalForForkPR covers the regression where
// runs from fork-based PRs have an empty pull_requests array in GitHub's API.
// The sync path must still flag workflow approval as required, otherwise the
// UI never shows the approve button for the exact case it was built for.
func TestAPISyncPRIncludesWorkflowApprovalForForkPR(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	mock := &mockGH{
		getPullRequestFn: func(_ context.Context, _, _ string, number int) (*gh.PullRequest, error) {
			id := int64(2001)
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
					ID:             new(int64(55)),
					HeadSHA:        new("forkhead"),
					Event:          new("pull_request"),
					HeadBranch:     new("feature"),
					HeadRepository: &gh.Repository{FullName: new("fork/widget")},
					PullRequests:   []*gh.PullRequest{},
				},
			}, nil
		},
	}

	srv, database := setupTestServerWithMock(t, mock)
	seedPR(t, database, "acme", "widget", 1)
	client := setupTestClient(t, srv)

	resp, err := client.HTTP.SyncPullWithResponse(t.Context(), &generated.SyncPullRequestOptions{PathParams: &generated.SyncPullPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)
	require.NotNil(resp.JSON200)
	require.NotNil(resp.JSON200.WorkflowApproval)
	assert.True(resp.JSON200.WorkflowApproval.Checked)
	assert.True(resp.JSON200.WorkflowApproval.Required)
	assert.Equal(int64(1), resp.JSON200.WorkflowApproval.Count)
}

// TestAPISyncPRIgnoresWorkflowRunsForOtherPRAtSameSHA covers the regression
// where two PRs share a head SHA and a populated pull_requests association
// points at the other PR. The sync path must not flag workflow approval as
// required for the wrong PR.
func TestAPISyncPRIgnoresWorkflowRunsForOtherPRAtSameSHA(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	mock := &mockGH{
		getPullRequestFn: func(_ context.Context, _, _ string, number int) (*gh.PullRequest, error) {
			id := int64(3001)
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
			}, nil
		},
	}

	srv, database := setupTestServerWithMock(t, mock)
	seedPR(t, database, "acme", "widget", 1)
	client := setupTestClient(t, srv)

	resp, err := client.HTTP.SyncPullWithResponse(t.Context(), &generated.SyncPullRequestOptions{PathParams: &generated.SyncPullPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)
	require.NotNil(resp.JSON200)
	require.NotNil(resp.JSON200.WorkflowApproval)
	assert.True(resp.JSON200.WorkflowApproval.Checked)
	assert.False(resp.JSON200.WorkflowApproval.Required)
	assert.Equal(int64(0), resp.JSON200.WorkflowApproval.Count)
}

// TestAPIGetPullEmitsDiffWarningWhenSHAsMissing covers the case where a
// previous diff sync failed and left the PR row without diff SHAs. The
// resolveItem path treats DiffSyncError as success and the resolve
// response has no warnings field, so the only place a client can learn
// the diff is unavailable is the next getPull call. This regression
// test pins that behavior so the warning can't silently disappear.
func TestAPIGetPullEmitsDiffWarningWhenSHAsMissing(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)

	database := dbtest.Open(t)

	// HasDiffSync gates the inferred warning, so the syncer must be
	// constructed with a non-nil clone manager. The manager itself is
	// never invoked by getPull.
	clonesDir := t.TempDir()
	clones := gitclone.New(clonesDir, nil)
	syncer := ghclient.NewSyncer(
		map[string]ghclient.Client{"github.com": &mockGH{}},
		database, clones, defaultTestRepos, time.Minute, nil, nil,
	)
	t.Cleanup(syncer.Stop)
	srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{})

	seedPR(t, database, "acme", "widget", 1)

	client := setupTestClient(t, srv)
	resp, err := client.HTTP.GetPullWithResponse(t.Context(), &generated.GetPullRequestOptions{PathParams: &generated.GetPullPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)
	require.NotNil(resp.JSON200)
	require.NotNil(resp.JSON200.Warnings, "warnings field should be set when diff is missing")
	warnings := resp.JSON200.Warnings
	require.Len(warnings, 1)
	warning := warnings[0]
	assert.Contains(warning, "Diff data is unavailable")

	// Sanitization invariants: the warning must not leak any internal
	// detail even when emitted from the read path.
	assert.NotContains(warning, clonesDir)
	assert.NotContains(warning, "refs/")
	assert.NotContains(warning, "rev-parse")
}

// TestAPIGetPullNoDiffWarningWhenSHAsPresent verifies the warning does
// not fire when the row already carries valid diff SHAs that match the
// latest platform head.
func TestAPIGetPullNoDiffWarningWhenSHAsPresent(t *testing.T) {
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

	seedPR(t, database, "acme", "widget", 2)

	repoID, err := database.UpsertRepo(ctx, verifiedGitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	headSHA := "deadbeef00000000000000000000000000000001"
	baseSHA := "deadbeef00000000000000000000000000000010"
	require.NoError(database.UpdatePlatformSHAs(
		ctx, repoID, 2, headSHA, baseSHA,
	))
	require.NoError(database.UpdateDiffSHAs(
		ctx, repoID, 2,
		headSHA,
		baseSHA,
		"deadbeef00000000000000000000000000000003",
	))

	client := setupTestClient(t, srv)
	resp, err := client.HTTP.GetPullWithResponse(t.Context(), &generated.GetPullRequestOptions{PathParams: &generated.GetPullPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(2)}})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)
	require.NotNil(resp.JSON200)
	if resp.JSON200.Warnings != nil {
		assert.Empty(resp.JSON200.Warnings)
	}
}

// TestAPIGetPullEmitsStaleDiffWarning covers the case where a diff sync
// populated the row but a later push advanced the platform head while
// the next diff sync failed. The recorded DiffHeadSHA is valid but no
// longer matches PlatformHeadSHA, so the UI would show a diff from the
// previous revision without any indication of drift. The warning must
// fire in that case.
func TestAPIGetPullEmitsStaleDiffWarning(t *testing.T) {
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

	seedPR(t, database, "acme", "widget", 3)

	repoID, err := database.UpsertRepo(ctx, verifiedGitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	// Platform reports the latest head; the recorded diff SHAs are from
	// an earlier push that no longer matches.
	require.NoError(database.UpdatePlatformSHAs(
		ctx, repoID, 3,
		"deadbeef00000000000000000000000000000099",
		"deadbeef00000000000000000000000000000010",
	))
	require.NoError(database.UpdateDiffSHAs(
		ctx, repoID, 3,
		"deadbeef00000000000000000000000000000001",
		"deadbeef00000000000000000000000000000002",
		"deadbeef00000000000000000000000000000003",
	))

	client := setupTestClient(t, srv)
	resp, err := client.HTTP.GetPullWithResponse(t.Context(), &generated.GetPullRequestOptions{PathParams: &generated.GetPullPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(3)}})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)
	require.NotNil(resp.JSON200)
	require.NotNil(resp.JSON200.Warnings, "warnings field should be set when diff is stale")
	warnings := resp.JSON200.Warnings
	require.Len(warnings, 1)
	assert.Contains(warnings[0], "out of date")
}

// TestAPIGetPullEmitsStaleDiffWarningOnBaseDrift covers the symmetric
// case to the head-drift test: the PR head is unchanged but the base
// branch advanced and the next diff sync failed. diffWarnings must
// mirror getDiff staleness logic, which treats base drift as stale
// for open PRs.
func TestAPIGetPullEmitsStaleDiffWarningOnBaseDrift(t *testing.T) {
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

	seedPR(t, database, "acme", "widget", 4)

	repoID, err := database.UpsertRepo(ctx, verifiedGitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	// Head matches, but the platform base advanced past the recorded
	// diff base — for example a merge landed on main after the diff
	// sync ran.
	headSHA := "deadbeef00000000000000000000000000000001"
	require.NoError(database.UpdatePlatformSHAs(
		ctx, repoID, 4,
		headSHA,
		"deadbeef00000000000000000000000000000099",
	))
	require.NoError(database.UpdateDiffSHAs(
		ctx, repoID, 4,
		headSHA,
		"deadbeef00000000000000000000000000000010",
		"deadbeef00000000000000000000000000000020",
	))

	client := setupTestClient(t, srv)
	resp, err := client.HTTP.GetPullWithResponse(t.Context(), &generated.GetPullRequestOptions{PathParams: &generated.GetPullPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(4)}})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)
	require.NotNil(resp.JSON200)
	require.NotNil(resp.JSON200.Warnings, "warnings field should be set when base drifted")
	warnings := resp.JSON200.Warnings
	require.Len(warnings, 1)
	assert.Contains(warnings[0], "out of date")
}

// TestAPISyncPRSanitizesDiffFailureWarning drives the syncPR handler
// through a real diff-sync failure and asserts the HTTP response body
// contains only the sanitized UserMessage. Previous roborev reviews
// flagged that nothing pins the boundary between the raw Error() chain
// (which may carry clone paths, refs, SHAs, and git stderr) and the
// sanitized client-facing string; a future refactor could reintroduce
// the leak without breaking any lower-level test. This test closes
// that gap by wiring a real Syncer to a clone Manager whose base dir
// is unreadable, so EnsureClone fails and the handler must surface
// only the sanitized warning.
func TestAPISyncPRSanitizesDiffFailureWarning(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)

	database := dbtest.Open(t)

	// Create a clone base dir that cannot be used: 0o000 blocks every
	// git command rooted under it, so syncMRDiff fails at the clone
	// stage. The exact error message will contain the locked path,
	// which is precisely the detail that must NOT reach the client.
	lockedBase := filepath.Join(t.TempDir(), "locked-clones")
	require.NoError(os.MkdirAll(lockedBase, 0o755))
	require.NoError(os.Chmod(lockedBase, 0o000))
	t.Cleanup(func() { _ = os.Chmod(lockedBase, 0o755) })
	clones := gitclone.New(lockedBase, nil)

	// Mock returns a live open PR with head and base SHAs populated,
	// so syncMRDiff enters the merge-base path rather than the early
	// return for missing SHAs.
	now := gh.Timestamp{Time: time.Now().UTC().Truncate(time.Second)}
	prState := "open"
	prID := int64(9001)
	prNumber := 9
	title := "sync-warning repro"
	body := "body"
	url := "https://github.com/acme/widget/pull/9"
	headSHA := "deadbeef00000000000000000000000000000099"
	baseSHA := "deadbeef00000000000000000000000000000088"
	login := "author"
	headRef := "feature"
	baseRef := "main"
	mock := &mockGH{
		getPullRequestFn: func(_ context.Context, _, _ string, _ int) (*gh.PullRequest, error) {
			return &gh.PullRequest{
				ID:        &prID,
				Number:    &prNumber,
				State:     &prState,
				Title:     &title,
				Body:      &body,
				HTMLURL:   &url,
				User:      &gh.User{Login: &login},
				Head:      &gh.PullRequestBranch{Ref: &headRef, SHA: &headSHA},
				Base:      &gh.PullRequestBranch{Ref: &baseRef, SHA: &baseSHA},
				CreatedAt: &now,
				UpdatedAt: &now,
			}, nil
		},
	}

	syncer := ghclient.NewSyncer(
		map[string]ghclient.Client{"github.com": mock},
		database, clones, defaultTestRepos, time.Minute, nil, nil,
	)
	t.Cleanup(syncer.Stop)
	srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{})

	_, err := database.UpsertRepo(t.Context(), verifiedGitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	client := setupTestClient(t, srv)
	resp, err := client.HTTP.SyncPullWithResponse(t.Context(), &generated.SyncPullRequestOptions{PathParams: &generated.SyncPullPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(int64(prNumber))}})
	require.NoError(err)
	// Diff-sync failures are non-fatal: the handler must return 200
	// with the PR row and a warning, not a 502.
	require.Equal(http.StatusOK, resp.StatusCode)
	require.NotNil(resp.JSON200)
	require.NotNil(resp.JSON200.Warnings)
	warnings := resp.JSON200.Warnings
	require.Len(warnings, 1)
	warning := warnings[0]
	assert.Contains(warning, "Diff data is unavailable")

	// Sanitization invariants: the warning must not leak any internal
	// detail from the underlying error chain. This is the regression
	// test the reviewer asked for.
	assert.NotContains(warning, lockedBase, "warning must not leak clone path")
	assert.NotContains(warning, "chdir", "warning must not leak chdir stderr")
	assert.NotContains(warning, "fetch", "warning must not leak git command name")
	assert.NotContains(warning, "ensure bare clone", "warning must not leak fmt.Errorf chain")
	assert.NotContains(warning, "github.com/acme", "warning must not leak remote URL path")
}

func TestAPIGitLabSyncReadsTokenFileAfterRotation(t *testing.T) {
	runParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	dir := t.TempDir()
	tokenPath := filepath.Join(dir, "gitlab-token")
	require.NoError(os.WriteFile(tokenPath, []byte("first-token\n"), 0o600))

	var tokens []string
	gitlabAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tokens = append(tokens, r.Header.Get("Private-Token"))
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.EscapedPath() {
		case "/api/v4/projects/42":
			_, _ = fmt.Fprint(w, `{
				"id": 42,
				"path": "project",
				"path_with_namespace": "group/project",
				"web_url": "https://gitlab.example.com/group/project",
				"default_branch": "main"
			}`)
		case "/api/v4/projects/42/merge_requests/7":
			_, _ = fmt.Fprint(w, `{
				"id": 7001,
				"iid": 7,
				"project_id": 42,
				"title": "GitLab token-file rotation",
				"state": "opened",
				"web_url": "https://gitlab.example.com/group/project/-/merge_requests/7",
				"author": {"username": "ada", "name": "Ada Lovelace"},
				"source_branch": "feature/token-rotation",
				"target_branch": "main",
				"created_at": "2026-04-01T10:00:00Z",
				"updated_at": "2026-04-02T10:00:00Z"
			}`)
		case "/api/v4/projects/42/merge_requests/7/discussions",
			"/api/v4/projects/42/merge_requests/7/commits":
			_, _ = fmt.Fprint(w, `[]`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(gitlabAPI.Close)

	source := tokenauth.NewManagedSource(tokenauth.Descriptor{
		Key: tokenauth.Key{Platform: string(platform.KindGitLab), Host: "gitlab.example.com"},
		Candidates: []tokenauth.Candidate{{
			Kind:     tokenauth.SourceKindFile,
			FilePath: tokenPath,
		}},
	}, tokenauth.Options{})
	client, err := platformgitlab.NewClient(
		"gitlab.example.com",
		source,
		platformgitlab.WithBaseURLForTesting(gitlabAPI.URL+"/api/v4"), platformgitlab.
			WithTransport(http.DefaultTransport),
	)
	require.NoError(err)
	registry, err := platform.NewRegistry(client)
	require.NoError(err)

	database := dbtest.Open(t)
	ref := platform.RepoRef{
		Platform:           platform.KindGitLab,
		Host:               "gitlab.example.com",
		Owner:              "group",
		Name:               "project",
		RepoPath:           "group/project",
		PlatformID:         42,
		PlatformExternalID: "42",
		WebURL:             "https://gitlab.example.com/group/project",
		CloneURL:           "https://gitlab.example.com/group/project.git",
		DefaultBranch:      "main",
	}
	repoID, err := database.UpsertRepo(ctx, platformdb.DBRepoIdentity(ref))
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

	firstRR := testutil.DoJSON(
		t, srv, http.MethodPost,
		"/api/v1/host/gitlab.example.com/pulls/gl/group/project/7/sync",
		nil)

	require.Equal(http.StatusOK, firstRR.Code, firstRR.Body.String())
	firstCallCount := len(tokens)
	require.Positive(firstCallCount)
	for _, token := range tokens {
		assert.Equal("first-token", token)
	}

	require.NoError(os.WriteFile(tokenPath, []byte("second-token\n"), 0o600))
	secondRR := testutil.DoJSON(
		t, srv, http.MethodPost,
		"/api/v1/host/gitlab.example.com/pulls/gl/group/project/7/sync",
		nil)

	require.Equal(http.StatusOK, secondRR.Code, secondRR.Body.String())
	require.Greater(len(tokens), firstCallCount)
	for _, token := range tokens[firstCallCount:] {
		assert.Equal("second-token", token)
	}

	mr, err := database.GetMergeRequestByRepoIDAndNumber(ctx, repoID, 7)
	require.NoError(err)
	require.NotNil(mr)
	assert.Equal("GitLab token-file rotation", mr.Title)
	require.NotNil(mr.DetailFetchedAt)
}

func TestAPIGitHubSyncReadsCloneTokenFileAfterRotation(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	dir := t.TempDir()

	remote := filepath.Join(dir, "remote.git")
	gitfixture.Run(t, dir, "init", "--bare", "--initial-branch=main", remote)
	work := filepath.Join(dir, "work")
	gitfixture.Run(t, dir, "clone", remote, work)
	gitfixture.Run(t, work, "config", "user.email", "test@test.com")
	gitfixture.Run(t, work, "config", "user.name", "Test")
	require.NoError(os.WriteFile(filepath.Join(work, "base.txt"), []byte("base\n"), 0o644))
	gitfixture.Run(t, work, "add", ".")
	gitfixture.Run(t, work, "commit", "-m", "base commit")
	gitfixture.Run(t, work, "push", "origin", "main")
	baseSHA := gitOutput(t, work, "rev-parse", "HEAD")
	gitfixture.Run(t, work, "checkout", "-b", "feature/token-rotation")
	require.NoError(os.WriteFile(filepath.Join(work, "feature.txt"), []byte("feature\n"), 0o644))
	gitfixture.Run(t, work, "add", ".")
	gitfixture.Run(t, work, "commit", "-m", "feature commit")
	gitfixture.Run(t, work, "push", "origin", "feature/token-rotation")
	headSHA := gitOutput(t, work, "rev-parse", "HEAD")

	tokenPath := filepath.Join(dir, "github-token")
	require.NoError(os.WriteFile(tokenPath, []byte("first-token\n"), 0o600))
	source := tokenauth.NewManagedSource(tokenauth.Descriptor{
		Key: tokenauth.Key{Platform: string(platform.KindGitHub), Host: "github.com"},
		Candidates: []tokenauth.Candidate{{
			Kind:     tokenauth.SourceKindFile,
			FilePath: tokenPath,
		}},
	}, tokenauth.Options{})

	capturePath := installCredentialCapturingGit(t, dir)

	cloneURL := gitLocalRemoteURL(remote)
	number := 7
	title := "GitHub clone token-file rotation"
	openState := "open"
	featureBranch := "feature/token-rotation"
	mainBranch := "main"
	htmlURL := "https://github.com/acme/widget/pull/7"
	login := "ada"
	fullName := "acme/widget"
	now := gh.Timestamp{Time: time.Now().UTC().Truncate(time.Second)}
	mock := &mockGH{
		getPullRequestFn: func(context.Context, string, string, int) (*gh.PullRequest, error) {
			return &gh.PullRequest{
				Number:    &number,
				Title:     &title,
				State:     &openState,
				HTMLURL:   &htmlURL,
				User:      &gh.User{Login: &login},
				CreatedAt: &now,
				UpdatedAt: &now,
				Head: &gh.PullRequestBranch{
					Ref: &featureBranch,
					SHA: &headSHA,
					Repo: &gh.Repository{
						CloneURL: &cloneURL,
						FullName: &fullName,
					},
				},
				Base: &gh.PullRequestBranch{
					Ref: &mainBranch,
					SHA: &baseSHA,
					Repo: &gh.Repository{
						CloneURL: &cloneURL,
						FullName: &fullName,
					},
				},
			}, nil
		},
	}

	database := dbtest.Open(t)
	repoID, err := database.UpsertRepo(ctx, verifiedGitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	clones := gitclone.New(filepath.Join(dir, "clones"), gitclone.HostSources{
		"github.com": source,
	})
	syncer := ghclient.NewSyncer(
		map[string]ghclient.Client{"github.com": mock},
		database,
		clones,
		[]ghclient.RepoRef{{
			Owner:         "acme",
			Name:          "widget",
			PlatformHost:  "github.com",
			CloneURL:      cloneURL,
			DefaultBranch: "main",
		}},
		time.Minute,
		nil,
		nil,
	)
	t.Cleanup(syncer.Stop)
	srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{Clones: clones})
	t.Cleanup(func() { gracefulShutdown(t, srv) })

	firstRR := testutil.DoJSON(t, srv, http.MethodPost, "/api/v1/pulls/gh/acme/widget/7/sync", nil)
	require.Equal(http.StatusOK, firstRR.Code, firstRR.Body.String())
	firstCredentials := readCapturedCredentials(t, capturePath)
	require.NotEmpty(firstCredentials)
	for _, token := range firstCredentials {
		assert.Equal("first-token", token)
	}

	require.NoError(os.WriteFile(tokenPath, []byte("second-token\n"), 0o600))
	secondRR := testutil.DoJSON(t, srv, http.MethodPost, "/api/v1/pulls/gh/acme/widget/7/sync", nil)
	require.Equal(http.StatusOK, secondRR.Code, secondRR.Body.String())
	allCredentials := readCapturedCredentials(t, capturePath)
	require.Greater(len(allCredentials), len(firstCredentials))
	for _, token := range allCredentials[len(firstCredentials):] {
		assert.Equal("second-token", token)
	}

	mr, err := database.GetMergeRequestByRepoIDAndNumber(ctx, repoID, 7)
	require.NoError(err)
	require.NotNil(mr)
	assert.Equal(headSHA, mr.DiffHeadSHA)
	assert.Equal(baseSHA, mr.DiffBaseSHA)
	assert.Equal(baseSHA, mr.MergeBaseSHA)
}

// installCredentialCapturingGit prepends a git wrapper to PATH that, on
// every invocation carrying an injected credential.helper (i.e. every
// networked git command kenn-forge authenticates), runs the helper's get
// action and appends its output to the returned capture file before
// exec'ing the real git. Local reads run without a helper and leave no
// trace, so the file records exactly the credentials git was given.
func installCredentialCapturingGit(t *testing.T, dir string) string {
	t.Helper()
	realGit, err := exec.LookPath("git")
	require.NoError(t, err)
	capturePath := filepath.Join(dir, "git-credentials.txt")
	gitWrapperDir := filepath.Join(dir, "git-wrapper")
	require.NoError(t, os.MkdirAll(gitWrapperDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(gitWrapperDir, "git"), []byte(`#!/bin/sh
set -eu
`+gitfake.CredentialHelperRunner+`
out="${KENN_FORGE_TEST_GIT_CAPTURE:?}"
helper=""
i=0
count="${GIT_CONFIG_COUNT:-0}"
while [ "$i" -lt "$count" ]; do
	eval "key=\${GIT_CONFIG_KEY_$i:-}"
	eval "value=\${GIT_CONFIG_VALUE_$i:-}"
	if [ "$key" = "credential.helper" ]; then
		helper="$value"
	fi
	i=$((i + 1))
done
if [ -n "$helper" ]; then
	run_credential_helper "$helper" get >> "$out"
	echo "---" >> "$out"
fi
exec "$KENN_FORGE_TEST_REAL_GIT" "$@"
`), 0o755))
	t.Setenv("PATH", gitWrapperDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("KENN_FORGE_TEST_GIT_CAPTURE", capturePath)
	t.Setenv("KENN_FORGE_TEST_REAL_GIT", realGit)
	return capturePath
}

func readCapturedCredentials(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return parseCapturedCredentials(string(data))
}

func parseCapturedCredentials(raw string) []string {
	var tokens []string
	for line := range strings.SplitSeq(strings.TrimSpace(raw), "\n") {
		if token, ok := strings.CutPrefix(line, "password="); ok {
			tokens = append(tokens, token)
		}
	}
	return tokens
}

func TestAPICIRefreshWarnsAndPreservesCIWhenProviderFails(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
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
		DefaultBranch:      "main",
	}
	provider := &apiTestGitLabProvider{
		ref:   ref,
		ciErr: errors.New("gitlab pipeline API unavailable"),
	}
	registry, err := platform.NewRegistry(provider)
	require.NoError(err)
	repoID, err := database.UpsertRepo(ctx, platformdb.DBRepoIdentity(ref))
	require.NoError(err)
	_, err = database.UpsertMergeRequest(ctx, &db.MergeRequest{
		RepoID:          repoID,
		PlatformID:      7001,
		Number:          7,
		URL:             "https://gitlab.example.com/group/project/-/merge_requests/7",
		Title:           "Keep stale CI visible",
		Author:          "ada",
		State:           "open",
		HeadBranch:      "feature",
		BaseBranch:      "main",
		PlatformHeadSHA: "head-sha",
		CIStatus:        "pending",
		CIChecksJSON:    `[{"name":"pipeline","status":"in_progress","conclusion":""}]`,
		CIHadPending:    true,
		CreatedAt:       now,
		UpdatedAt:       now,
		LastActivityAt:  now,
	})
	require.NoError(err)

	syncer := ghclient.NewSyncerWithRegistry(
		registry,
		database,
		nil,
		[]ghclient.RepoRef{{
			Platform:     platform.KindGitLab,
			PlatformHost: ref.Host,
			Owner:        ref.Owner,
			Name:         ref.Name,
			RepoPath:     ref.RepoPath,
		}},
		time.Minute,
		nil,
		nil,
	)
	t.Cleanup(syncer.Stop)
	srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{})
	t.Cleanup(func() { gracefulShutdown(t, srv) })
	client := setupTestClient(t, srv)

	resp, err := client.HTTP.RefreshPullCiOnHostWithResponse(ctx, &generated.RefreshPullCiOnHostRequestOptions{PathParams: &generated.RefreshPullCiOnHostPath{PlatformHost: ref.Host, Provider: "gitlab", Owner: ref.Owner, Name: ref.Name, Number: int64(7)}})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode, string(resp.Body))
	require.NotNil(resp.JSON200)
	assert.Equal("pending", resp.JSON200.MergeRequest.CIStatus)
	assert.JSONEq(
		`[{"name":"pipeline","status":"in_progress","conclusion":""}]`,
		resp.JSON200.MergeRequest.CIChecksJSON,
	)
	require.NotNil(resp.JSON200.Warnings)
	require.Len(resp.JSON200.Warnings, 1)
	assert.Contains(resp.JSON200.Warnings[0], "Could not refresh CI checks")

	stored, err := database.GetMergeRequestByRepoIDAndNumber(ctx, repoID, 7)
	require.NoError(err)
	require.NotNil(stored)
	assert.Equal("pending", stored.CIStatus)
	assert.JSONEq(
		`[{"name":"pipeline","status":"in_progress","conclusion":""}]`,
		stored.CIChecksJSON,
	)
}

func TestAPISyncRefreshesStaleCachedChecksWhenAggregateCIChanges(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Second)
	fetchedAt := now
	database := dbtest.Open(t)

	ref := platform.RepoRef{
		Platform:           platform.KindGitLab,
		Host:               "gitlab.example.com",
		Owner:              "group",
		Name:               "project",
		RepoPath:           "group/project",
		PlatformID:         4242,
		PlatformExternalID: "gid://gitlab/Project/4242",
		DefaultBranch:      "main",
	}
	provider := &apiTestGitLabProvider{
		ref: ref,
		mergeRequests: []platform.MergeRequest{{
			Repo:           ref,
			PlatformID:     7001,
			Number:         7,
			URL:            "https://gitlab.example.com/group/project/-/merge_requests/7",
			Title:          "Refresh changed CI",
			Author:         "ada",
			State:          "open",
			HeadBranch:     "feature",
			BaseBranch:     "main",
			HeadSHA:        "head-sha",
			BaseSHA:        "base-sha",
			CIStatus:       "pending",
			CreatedAt:      now,
			UpdatedAt:      now,
			LastActivityAt: now,
		}},
		ciChecks: map[string][]platform.CICheck{
			"head-sha": {{
				Name:   "pipeline",
				Status: "in_progress",
			}},
		},
	}
	registry, err := platform.NewRegistry(provider)
	require.NoError(err)
	repoID, err := database.UpsertRepo(ctx, platformdb.DBRepoIdentity(ref))
	require.NoError(err)
	_, err = database.UpsertMergeRequest(ctx, &db.MergeRequest{
		RepoID:          repoID,
		PlatformID:      7001,
		Number:          7,
		URL:             "https://gitlab.example.com/group/project/-/merge_requests/7",
		Title:           "Refresh changed CI",
		Author:          "ada",
		State:           "open",
		HeadBranch:      "feature",
		BaseBranch:      "main",
		PlatformHeadSHA: "head-sha",
		PlatformBaseSHA: "base-sha",
		CIStatus:        "failure",
		CIChecksJSON:    `[{"name":"pipeline","status":"completed","conclusion":"failure"}]`,
		CIHadPending:    false,
		DetailFetchedAt: &fetchedAt,
		CreatedAt:       now,
		UpdatedAt:       now,
		LastActivityAt:  now,
	})
	require.NoError(err)

	syncer := ghclient.NewSyncerWithRegistry(
		registry,
		database,
		nil,
		[]ghclient.RepoRef{{
			Platform:     platform.KindGitLab,
			PlatformHost: ref.Host,
			Owner:        ref.Owner,
			Name:         ref.Name,
			RepoPath:     ref.RepoPath,
		}},
		time.Minute,
		nil,
		map[string]*ghclient.SyncBudget{
			ghclient.RateBucketKey("gitlab", ref.Host, "host"): ghclient.NewSyncBudget(100),
		},
	)
	t.Cleanup(syncer.Stop)
	srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{})
	t.Cleanup(func() { gracefulShutdown(t, srv) })
	client := setupTestClient(t, srv)

	syncer.RunOnce(ctx)

	resp, err := client.HTTP.GetPullOnHostWithResponse(ctx, &generated.GetPullOnHostRequestOptions{PathParams: &generated.GetPullOnHostPath{PlatformHost: ref.Host, Provider: "gitlab", Owner: ref.Owner, Name: ref.Name, Number: int64(7)}})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode, string(resp.Body))
	require.NotNil(resp.JSON200)
	assert.Equal("pending", resp.JSON200.MergeRequest.CIStatus)
	assert.JSONEq(
		`[{"name":"pipeline","status":"in_progress","conclusion":"","url":"","app":""}]`,
		resp.JSON200.MergeRequest.CIChecksJSON,
	)

	stored, err := database.GetMergeRequestByRepoIDAndNumber(ctx, repoID, 7)
	require.NoError(err)
	require.NotNil(stored)
	assert.NotNil(stored.DetailFetchedAt)
	assert.True(stored.CIHadPending)
	assert.JSONEq(
		`[{"name":"pipeline","status":"in_progress","conclusion":"","url":"","app":""}]`,
		stored.CIChecksJSON,
	)
}

func TestAPISyncRefreshesCachedPendingChecksThroughDetailDrain(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Second)
	fetchedAt := now
	database := dbtest.Open(t)

	ref := platform.RepoRef{
		Platform:           platform.KindGitLab,
		Host:               "gitlab.example.com",
		Owner:              "group",
		Name:               "project",
		RepoPath:           "group/project",
		PlatformID:         4343,
		PlatformExternalID: "gid://gitlab/Project/4343",
		DefaultBranch:      "main",
	}
	provider := &apiTestGitLabProvider{
		ref: ref,
		mergeRequests: []platform.MergeRequest{{
			Repo:           ref,
			PlatformID:     7008,
			Number:         8,
			URL:            "https://gitlab.example.com/group/project/-/merge_requests/8",
			Title:          "Refresh cached pending CI",
			Author:         "ada",
			State:          "open",
			HeadBranch:     "feature",
			BaseBranch:     "main",
			HeadSHA:        "pending-head",
			BaseSHA:        "base-sha",
			CIStatus:       "pending",
			CreatedAt:      now,
			UpdatedAt:      now,
			LastActivityAt: now,
		}},
		ciChecks: map[string][]platform.CICheck{
			"pending-head": {{
				Name:       "pipeline",
				Status:     "completed",
				Conclusion: "success",
			}},
		},
	}
	registry, err := platform.NewRegistry(provider)
	require.NoError(err)
	repoID, err := database.UpsertRepo(ctx, platformdb.DBRepoIdentity(ref))
	require.NoError(err)
	_, err = database.UpsertMergeRequest(ctx, &db.MergeRequest{
		RepoID:          repoID,
		PlatformID:      7008,
		Number:          8,
		URL:             "https://gitlab.example.com/group/project/-/merge_requests/8",
		Title:           "Refresh cached pending CI",
		Author:          "ada",
		State:           "open",
		HeadBranch:      "feature",
		BaseBranch:      "main",
		PlatformHeadSHA: "pending-head",
		PlatformBaseSHA: "base-sha",
		CIStatus:        "pending",
		CIChecksJSON:    `[{"name":"pipeline","status":"in_progress","conclusion":""}]`,
		CIHadPending:    false,
		DetailFetchedAt: &fetchedAt,
		CreatedAt:       now,
		UpdatedAt:       now,
		LastActivityAt:  now,
	})
	require.NoError(err)

	syncer := ghclient.NewSyncerWithRegistry(
		registry,
		database,
		nil,
		[]ghclient.RepoRef{{
			Platform:     platform.KindGitLab,
			PlatformHost: ref.Host,
			Owner:        ref.Owner,
			Name:         ref.Name,
			RepoPath:     ref.RepoPath,
		}},
		time.Minute,
		nil,
		map[string]*ghclient.SyncBudget{
			ghclient.RateBucketKey("gitlab", ref.Host, "host"): ghclient.NewSyncBudget(100),
		},
	)
	t.Cleanup(syncer.Stop)
	srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{})
	t.Cleanup(func() { gracefulShutdown(t, srv) })
	client := setupTestClient(t, srv)

	syncer.RunOnce(ctx)

	resp, err := client.HTTP.GetPullOnHostWithResponse(ctx, &generated.GetPullOnHostRequestOptions{PathParams: &generated.GetPullOnHostPath{PlatformHost: ref.Host, Provider: "gitlab", Owner: ref.Owner, Name: ref.Name, Number: int64(8)}})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode, string(resp.Body))
	require.NotNil(resp.JSON200)
	assert.Equal("success", resp.JSON200.MergeRequest.CIStatus)
	assert.JSONEq(
		`[{"name":"pipeline","status":"completed","conclusion":"success","url":"","app":""}]`,
		resp.JSON200.MergeRequest.CIChecksJSON,
	)

	stored, err := database.GetMergeRequestByRepoIDAndNumber(ctx, repoID, 8)
	require.NoError(err)
	require.NotNil(stored)
	assert.NotNil(stored.DetailFetchedAt)
	assert.False(stored.CIHadPending)
	assert.JSONEq(
		`[{"name":"pipeline","status":"completed","conclusion":"success","url":"","app":""}]`,
		stored.CIChecksJSON,
	)
}

func TestAPIEditPRContentRejectsNilProviderPayload(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)

	mock := &mockGH{
		editPullRequestFn: func(
			context.Context,
			string,
			string,
			int,
			platformgithub.EditPullRequestOpts,
		) (*gh.PullRequest, error) {
			return nil, nil
		},
	}
	srv, database := setupTestServerWithMock(t, mock)
	seedPR(t, database, "acme", "widget", 1)
	client := setupTestClient(t, srv)

	title := "Updated title"
	resp, err := client.HTTP.EditPrContentWithResponse(t.Context(), &generated.EditPrContentRequestOptions{PathParams: &generated.EditPrContentPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}, Body: &generated.EditPrContentBody{Title: &title}})
	require.Error(err)
	require.NotNil(resp)
	require.Equal(http.StatusBadGateway, resp.StatusCode)

	mr, err := database.GetMergeRequest(t.Context(), "github", "github.com", "acme", "widget", 1)
	require.NoError(err)
	require.NotNil(mr)
	assert.Equal("Test PR #1", mr.Title)
	assert.Equal("test body", mr.Body)
}

func TestAPITriggerSyncStopsDetailDrainAfterDisabledIndexResult(t *testing.T) {
	runParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	database := dbtest.Open(t)
	now := time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	disabledErr := platform.RepositoryFeatureDisabled(
		platform.KindGitHub, "github.com", platform.RepositoryFeatureIssues,
		errors.New("repository issues disabled"),
	)

	var issueListCalls atomic.Int32
	var issueDetailCalls atomic.Int32
	mock := &mockGH{
		listOpenIssuesFn: func(context.Context, string, string) ([]*gh.Issue, error) {
			issueListCalls.Add(1)
			return nil, disabledErr
		},
		getIssueFn: func(context.Context, string, string, int) (*gh.Issue, error) {
			issueDetailCalls.Add(1)
			return nil, disabledErr
		},
	}
	repo := ghclient.RepoRef{
		Platform: platform.KindGitHub, PlatformHost: "github.com",
		Owner: "acme", Name: "widget",
	}
	repoID, err := database.UpsertRepo(
		ctx, verifiedGitHubRepoIdentity("github.com", "acme", "widget"),
	)
	require.NoError(err)
	for _, number := range []int{1, 2} {
		_, err = database.UpsertIssue(ctx, &db.Issue{
			RepoID: repoID, PlatformID: int64(7000 + number), Number: number,
			URL:   fmt.Sprintf("https://github.com/acme/widget/issues/%d", number),
			Title: fmt.Sprintf("stale issue %d", number), Author: "ada", State: "open",
			CreatedAt: now, UpdatedAt: now, LastActivityAt: now,
		})
		require.NoError(err)
	}
	syncer := ghclient.NewSyncer(
		map[string]ghclient.Client{"github.com": mock}, database, nil,
		[]ghclient.RepoRef{repo}, time.Minute, nil,
		map[string]*ghclient.SyncBudget{"github.com": ghclient.NewSyncBudget(100)},
	)
	t.Cleanup(syncer.Stop)
	srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{})
	t.Cleanup(func() { gracefulShutdown(t, srv) })

	syncer.RunOnce(ctx)
	assert.Equal(int32(1), issueListCalls.Load())
	assert.Zero(int(issueDetailCalls.Load()))

	done := make(chan struct{}, 1)
	syncer.SetOnStatusChange(func(status *ghclient.SyncStatus) {
		if !status.Running {
			select {
			case done <- struct{}{}:
			default:
			}
		}
	})
	rr := testutil.DoJSON(t, srv, http.MethodPost, "/api/v1/sync", nil)
	require.Equal(http.StatusAccepted, rr.Code, rr.Body.String())
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		require.Fail("expected explicit global sync to complete")
	}

	assert.Equal(int32(2), issueListCalls.Load(),
		"explicit sync must bypass the cooldown that existed at run start")
	assert.Zero(int(issueDetailCalls.Load()),
		"the disabled index result must suppress detail work in the same explicit run")
}

func TestAPISyncPRDoesNotOverwriteNewerStateChange(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)

	staleUpdatedAt := time.Date(2026, 4, 12, 1, 0, 0, 0, time.UTC)
	closedAt := staleUpdatedAt.Add(time.Hour)
	syncStarted := make(chan struct{}, 1)
	releaseSync := make(chan struct{})
	mock := &mockGH{
		// The user's close commits the provider's edit response, whose
		// updated_at is newer than the in-flight stale sync's snapshot;
		// the monotonic snapshot guard then rejects the stale sync.
		editPullRequestFn: func(
			_ context.Context, _, _ string, number int, opts platformgithub.EditPullRequestOpts,
		) (*gh.PullRequest, error) {
			state := "closed"
			if opts.State != nil {
				state = *opts.State
			}
			return providerStatePR(
				number, state, time.Now().UTC().Add(time.Hour),
				&closedAt, nil, "abc123",
			), nil
		},
		getPullRequestFn: func(_ context.Context, owner, repo string, number int) (*gh.PullRequest, error) {
			syncStarted <- struct{}{}
			<-releaseSync

			id := int64(101)
			state := "open"
			title := "stale sync"
			url := "https://github.com/acme/widget/pull/1"
			author := "alice"
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
				User:      &gh.User{Login: &author},
				CreatedAt: &createdAt,
				UpdatedAt: &updatedAt,
				Head:      &gh.PullRequestBranch{SHA: &headSHA, Ref: &featureRef},
				Base:      &gh.PullRequestBranch{SHA: &baseSHA, Ref: &mainRef},
			}, nil
		},
	}

	srv, database := setupTestServerWithMock(t, mock)
	seedPR(t, database, "acme", "widget", 1)
	client := setupTestClient(t, srv)

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

	resp, err := client.HTTP.SetPrGithubStateWithResponse(t.Context(), &generated.SetPrGithubStateRequestOptions{PathParams: &generated.SetPrGithubStatePath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}, Body: &generated.SetPrGithubStateBody{State: "closed"}})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)

	closedPR, err := database.GetMergeRequest(t.Context(), "github", "github.com", "acme", "widget", 1)
	require.NoError(err)
	require.Equal(db.MergeRequestStateClosed, closedPR.State)
	require.NotNil(closedPR.ClosedAt)

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
	require.True(completed, "timed out waiting for stale PR sync")

	finalPR, err := database.GetMergeRequest(t.Context(), "github", "github.com", "acme", "widget", 1)
	require.NoError(err)
	assert.Equal(db.MergeRequestStateClosed, finalPR.State)
	assert.NotNil(finalPR.ClosedAt)
	assert.Equal("Test PR #1", finalPR.Title)
	assert.True(finalPR.UpdatedAt.After(staleUpdatedAt))
}

func TestAPISyncPRPreservesCIStatusWhileRefreshingCI(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)

	ciRefreshStarted := make(chan struct{}, 1)
	releaseCIRefresh := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() {
		releaseOnce.Do(func() { close(releaseCIRefresh) })
	})

	mock := &mockGH{
		getPullRequestFn: func(_ context.Context, owner, repo string, number int) (*gh.PullRequest, error) {
			id := int64(101)
			state := "open"
			title := "fresh sync"
			url := "https://github.com/acme/widget/pull/1"
			author := "alice"
			headSHA := "abc123"
			baseSHA := "def456"
			featureRef := "feature"
			mainRef := "main"
			now := gh.Timestamp{Time: time.Now().UTC()}
			return &gh.PullRequest{
				ID:        &id,
				Number:    &number,
				State:     &state,
				Title:     &title,
				HTMLURL:   &url,
				User:      &gh.User{Login: &author},
				CreatedAt: &now,
				UpdatedAt: &now,
				Head:      &gh.PullRequestBranch{SHA: &headSHA, Ref: &featureRef},
				Base:      &gh.PullRequestBranch{SHA: &baseSHA, Ref: &mainRef},
			}, nil
		},
		listCheckRunsForRefFn: func(_ context.Context, _, _, ref string) ([]*gh.CheckRun, error) {
			require.Equal("abc123", ref)
			ciRefreshStarted <- struct{}{}
			<-releaseCIRefresh
			name := "tests"
			status := "completed"
			conclusion := "success"
			return []*gh.CheckRun{{
				Name:       &name,
				Status:     &status,
				Conclusion: &conclusion,
			}}, nil
		},
	}

	srv, database := setupTestServerWithMock(t, mock)
	seedPR(t, database, "acme", "widget", 1, withSeedPRHeadSHA("abc123"))
	repo, err := database.GetRepoByIdentity(t.Context(), verifiedGitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	require.NotNil(repo)
	existingChecksJSON := `[{"name":"tests","status":"completed","conclusion":"success"}]`
	require.NoError(database.UpdateMRCIStatus(
		t.Context(), repo.ID, 1, "success", existingChecksJSON,
	))
	client := setupTestClient(t, srv)

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

	select {
	case <-ciRefreshStarted:
	case <-time.After(2 * time.Second):
		require.Fail("CI refresh did not start")
	}

	detailResp, err := client.HTTP.GetPullWithResponse(t.Context(), &generated.GetPullRequestOptions{PathParams: &generated.GetPullPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}})
	require.NoError(err)
	require.Equal(http.StatusOK, detailResp.StatusCode)
	require.NotNil(detailResp.JSON200)
	require.NotNil(detailResp.JSON200.MergeRequest)
	assert.Equal("success", detailResp.JSON200.MergeRequest.CIStatus)
	assert.JSONEq(existingChecksJSON, detailResp.JSON200.MergeRequest.CIChecksJSON)

	releaseOnce.Do(func() { close(releaseCIRefresh) })
	select {
	case err := <-syncErr:
		require.NoError(err)
	case resp := <-syncDone:
		require.Equal(http.StatusOK, resp.StatusCode)
	case <-time.After(5 * time.Second):
		require.Fail("timed out waiting for PR sync")
	}
}

func TestAPISyncPRBypassesPullRequestETagForCIRefresh(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)

	headSHA := "abc123"
	success := "success"
	getPRCalls := 0
	conditionalCalls := 0
	ciCalls := 0
	mock := &mockGH{
		getPullRequestFn: func(_ context.Context, _ string, _ string, number int) (*gh.PullRequest, error) {
			getPRCalls++
			state := "open"
			title := "manual sync"
			url := "https://github.com/acme/widget/pull/1"
			author := "alice"
			baseSHA := "def456"
			featureRef := "feature"
			mainRef := "main"
			now := gh.Timestamp{Time: time.Now().UTC()}
			return &gh.PullRequest{
				Number:    &number,
				State:     &state,
				Title:     &title,
				HTMLURL:   &url,
				User:      &gh.User{Login: &author},
				CreatedAt: &now,
				UpdatedAt: &now,
				Head:      &gh.PullRequestBranch{SHA: &headSHA, Ref: &featureRef},
				Base:      &gh.PullRequestBranch{SHA: &baseSHA, Ref: &mainRef},
			}, nil
		},
		getPullRequestIfChangedFn: func(_ context.Context, _ string, _ string, _ int, etag string) (*gh.PullRequest, string, bool, error) {
			conditionalCalls++
			require.Equal(`"etag-v1"`, etag)
			return nil, etag, true, nil
		},
		listCheckRunsForRefFn: func(_ context.Context, _, _ string, ref string) ([]*gh.CheckRun, error) {
			ciCalls++
			require.Equal(headSHA, ref)
			name := "tests"
			status := "completed"
			conclusion := "success"
			return []*gh.CheckRun{{
				Name:       &name,
				Status:     &status,
				Conclusion: &conclusion,
			}}, nil
		},
		getCombinedStatusFn: func(_ context.Context, _, _, ref string) (*gh.CombinedStatus, error) {
			require.Equal(headSHA, ref)
			return &gh.CombinedStatus{State: &success}, nil
		},
	}

	srv, database := setupTestServerWithMock(t, mock)
	seedPR(t, database, "acme", "widget", 1,
		withSeedPRHeadSHA(headSHA),
		withSeedPRCI("failure", `[{"name":"tests","status":"completed","conclusion":"failure"}]`),
	)
	repo, err := database.GetRepoByIdentity(t.Context(), verifiedGitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	require.NotNil(repo)
	require.NoError(database.UpsertHTTPEtag(
		t.Context(), "github", "github.com", "acme", "widget",
		"pull_request", 1, `"etag-v1"`,
	))

	rr := testutil.DoJSON(t, srv, http.MethodPost, "/api/v1/pulls/gh/acme/widget/1/sync", nil)
	require.Equal(http.StatusOK, rr.Code, rr.Body.String())

	stored, err := database.GetMergeRequestByRepoIDAndNumber(t.Context(), repo.ID, 1)
	require.NoError(err)
	require.NotNil(stored)
	assert.Equal("success", stored.CIStatus)
	assert.Equal(1, getPRCalls)
	assert.Zero(conditionalCalls)
	assert.Equal(1, ciCalls)
}

// When the head SHA changes, previously-recorded CI is tied to the old
// commit and must not be carried forward. If the in-flight CI refresh
// then fails, the detail must show "no CI" rather than stale checks
// attached to a different commit.
func TestAPISyncPRClearsCIWhenHeadSHAChanges(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)

	mock := &mockGH{
		getPullRequestFn: func(_ context.Context, _, _ string, number int) (*gh.PullRequest, error) {
			id := int64(101)
			state := "open"
			title := "fresh sync"
			url := "https://github.com/acme/widget/pull/1"
			author := "alice"
			newHeadSHA := "newhead"
			baseSHA := "def456"
			featureRef := "feature"
			mainRef := "main"
			now := gh.Timestamp{Time: time.Now().UTC()}
			return &gh.PullRequest{
				ID:        &id,
				Number:    &number,
				State:     &state,
				Title:     &title,
				HTMLURL:   &url,
				User:      &gh.User{Login: &author},
				CreatedAt: &now,
				UpdatedAt: &now,
				Head:      &gh.PullRequestBranch{SHA: &newHeadSHA, Ref: &featureRef},
				Base:      &gh.PullRequestBranch{SHA: &baseSHA, Ref: &mainRef},
			}, nil
		},
		listCheckRunsForRefFn: func(_ context.Context, _, _, _ string) ([]*gh.CheckRun, error) {
			return nil, errors.New("simulated CI refresh failure")
		},
	}

	srv, database := setupTestServerWithMock(t, mock)
	seedPR(t, database, "acme", "widget", 1, withSeedPRHeadSHA("oldhead"))
	repo, err := database.GetRepoByIdentity(t.Context(), verifiedGitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	require.NotNil(repo)
	existingChecksJSON := `[{"name":"tests","status":"completed","conclusion":"success"}]`
	require.NoError(database.UpdateMRCIStatus(
		t.Context(), repo.ID, 1, "success", existingChecksJSON,
	))
	// Mark the prior CI snapshot as having had pending checks so the
	// post-sync assertion below distinguishes "cleared" from "default
	// false". UpsertMergeRequest preserves ci_had_pending across
	// upserts, so without an explicit clear it would survive a
	// head-SHA change.
	require.NoError(database.UpdateMRDetailFetched(
		t.Context(), "github", "github.com", "acme", "widget", 1, true,
	))
	client := setupTestClient(t, srv)

	syncResp, err := client.HTTP.SyncPullWithResponse(t.Context(), &generated.SyncPullRequestOptions{PathParams: &generated.SyncPullPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}})
	require.NoError(err)
	require.Equal(http.StatusOK, syncResp.StatusCode)

	detailResp, err := client.HTTP.GetPullWithResponse(t.Context(), &generated.GetPullRequestOptions{PathParams: &generated.GetPullPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}})
	require.NoError(err)
	require.Equal(http.StatusOK, detailResp.StatusCode)
	require.NotNil(detailResp.JSON200)
	require.NotNil(detailResp.JSON200.MergeRequest)
	assert.Empty(detailResp.JSON200.MergeRequest.CIStatus)
	assert.Empty(detailResp.JSON200.MergeRequest.CIChecksJSON)

	mr, err := database.GetMergeRequestByRepoIDAndNumber(t.Context(), repo.ID, 1)
	require.NoError(err)
	require.NotNil(mr)
	assert.False(mr.CIHadPending)
}

func TestAPIEnqueuePRSyncReturnsBeforeGitHubFetchCompletes(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)

	syncStarted := make(chan struct{}, 1)
	releaseSync := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() {
		releaseOnce.Do(func() { close(releaseSync) })
	})

	mock := &mockGH{
		getPullRequestFn: func(_ context.Context, owner, repo string, number int) (*gh.PullRequest, error) {
			syncStarted <- struct{}{}
			<-releaseSync

			id := int64(101)
			state := "open"
			title := "fresh async sync"
			url := "https://github.com/acme/widget/pull/1"
			author := "alice"
			headSHA := "abc123"
			baseSHA := "def456"
			featureRef := "feature"
			mainRef := "main"
			now := gh.Timestamp{Time: time.Now().UTC()}
			return &gh.PullRequest{
				ID:        &id,
				Number:    &number,
				State:     &state,
				Title:     &title,
				HTMLURL:   &url,
				User:      &gh.User{Login: &author},
				CreatedAt: &now,
				UpdatedAt: &now,
				Head:      &gh.PullRequestBranch{SHA: &headSHA, Ref: &featureRef},
				Base:      &gh.PullRequestBranch{SHA: &baseSHA, Ref: &mainRef},
			}, nil
		},
	}

	srv, database := setupTestServerWithMock(t, mock)
	seedPR(t, database, "acme", "widget", 1)
	client := setupTestClient(t, srv)

	ctx, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
	defer cancel()
	resp, err := client.HTTP.EnqueuePrSyncWithResponse(ctx, &generated.EnqueuePrSyncRequestOptions{PathParams: &generated.EnqueuePrSyncPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}})
	require.NoError(err)
	require.Equal(http.StatusAccepted, resp.StatusCode)

	select {
	case <-syncStarted:
	case <-time.After(2 * time.Second):
		require.Fail("background sync did not start")
	}
	releaseOnce.Do(func() { close(releaseSync) })
}

func TestAPIListPullsSearchByNumber(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	srv, database := setupTestServer(t)
	ctx := t.Context()

	seedPR(t, database, "acme", "widget", 12, withSeedPRTitle("add feature"))
	prID := seedPR(t, database, "acme", "widget", 278, withSeedPRTitle("fix bug"))
	seedPR(t, database, "acme", "widget", 290, withSeedPRTitle("another change"))
	seedPR(t, database, "tools", "worker", 301, withSeedPRTitle("repair bug"))
	seedPR(t, database, "docs", "reader", 302, withSeedPRTitle("can't reproduce"))
	repo, err := database.GetRepoByIdentity(ctx, verifiedGitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	require.NoError(database.ReplaceMergeRequestLabels(ctx, repo.ID, prID, []db.Label{{
		PlatformID: 200,
		Name:       "needs-review",
		Color:      "fbca04",
		UpdatedAt:  time.Now().UTC(),
	}}))

	client := setupTestClient(t, srv)

	pullNumbers := func(params *generated.ListPullsQuery) []int {
		t.Helper()
		resp, err := client.HTTP.ListPullsWithResponse(ctx, &generated.ListPullsRequestOptions{Query: params})
		require.NoError(err)
		require.Equal(http.StatusOK, resp.StatusCode)
		require.NotNil(resp.JSON200)
		nums := make([]int, 0, len(*resp.JSON200))
		for _, pr := range *resp.JSON200 {
			nums = append(nums, int(pr.Number))
		}
		return nums
	}

	q := "278"
	assert.ElementsMatch([]int{278}, pullNumbers(&generated.ListPullsQuery{Q: &q}))

	q = "#278"
	assert.ElementsMatch([]int{278}, pullNumbers(&generated.ListPullsQuery{Q: &q}))

	// Title still matches.
	q = "fix"
	assert.ElementsMatch([]int{278}, pullNumbers(&generated.ListPullsQuery{Q: &q}))

	q = "fix widget"
	assert.ElementsMatch([]int{278}, pullNumbers(&generated.ListPullsQuery{Q: &q}))

	q = "work bug"
	assert.ElementsMatch([]int{301}, pullNumbers(&generated.ListPullsQuery{Q: &q}))

	q = "needs-review bug"
	assert.ElementsMatch([]int{278}, pullNumbers(&generated.ListPullsQuery{Q: &q}))

	q = "can't"
	assert.ElementsMatch([]int{302}, pullNumbers(&generated.ListPullsQuery{Q: &q}))

	q = "needs-review"
	assert.ElementsMatch([]int{278}, pullNumbers(&generated.ListPullsQuery{Q: &q}))

	// Substring of number matches multiple.
	q = "2"
	assert.ElementsMatch([]int{12, 278, 290, 302}, pullNumbers(&generated.ListPullsQuery{Q: &q}))
}

func TestAPIListPullsCasefoldsRepoNames(t *testing.T) {
	runParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	srv, database := setupTestServerWithRepos(t, &mockGH{}, []ghclient.RepoRef{
		{Owner: "org", Name: "foo", PlatformHost: "github.com"},
	})

	seedPR(t, database, "Org", "Foo", 1)
	seedPR(t, database, "org", "foo", 1)

	client := setupTestClient(t, srv)
	resp, err := client.HTTP.ListPullsWithResponse(t.Context(), &generated.ListPullsRequestOptions{})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)
	require.NotNil(resp.JSON200)
	require.Len(*resp.JSON200, 1)
	assert.Equal("org", (*resp.JSON200)[0].RepoOwner)
	assert.Equal("foo", (*resp.JSON200)[0].RepoName)
}

func TestAPIListPullsFiltersProviderQualifiedHostedNestedRepoPath(t *testing.T) {
	runParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	srv, database := setupTestServerWithRepos(t, &mockGH{}, []ghclient.RepoRef{
		{Owner: "Group/SubGroup", Name: "Project.Special", PlatformHost: "ghe.example.com"},
		{Owner: "other", Name: "repo", PlatformHost: "ghe.example.com"},
	})

	seedPROnHost(t, database, "ghe.example.com", "Group/SubGroup", "Project.Special", 1)
	seedPROnHost(t, database, "ghe.example.com", "other", "repo", 2)

	client := setupTestClient(t, srv)
	repo := "github|ghe.example.com/Group/SubGroup/Project.Special"
	resp, err := client.HTTP.ListPullsWithResponse(t.Context(), &generated.ListPullsRequestOptions{Query: &generated.ListPullsQuery{
		Repo: &repo,
	}})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)
	require.NotNil(resp.JSON200)
	require.Len(*resp.JSON200, 1)
	assert.Equal("group/subgroup", (*resp.JSON200)[0].RepoOwner)
	assert.Equal("project.special", (*resp.JSON200)[0].RepoName)
}

func TestAPIListPullsAcceptsProviderQualifiedRepoFilter(t *testing.T) {
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

	repo := "gitea|github.com/acme/widget"
	resp, err := client.HTTP.ListPullsWithResponse(ctx, &generated.ListPullsRequestOptions{Query: &generated.ListPullsQuery{Repo: &repo}})
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

func TestAPIMarkPRDraftPersistsDraftFlag(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	var gotOwner string
	var gotRepo string
	var gotNumber int
	mock := &mockGH{
		convertToDraftFn: func(_ context.Context, owner, repo string, number int) (*gh.PullRequest, error) {
			gotOwner = owner
			gotRepo = repo
			gotNumber = number
			id := int64(1001)
			title := "Draft PR"
			state := "open"
			url := "https://github.com/acme/widget/pull/1"
			author := "octocat"
			draft := true
			now := gh.Timestamp{Time: time.Now().UTC().Add(time.Minute)}
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
	before, err := database.GetMergeRequest(t.Context(), "github", "github.com", "acme", "widget", 1)
	require.NoError(err)
	require.NotNil(before)
	before.CommentCount = 7
	before.CIStatus = "success"
	before.CIChecksJSON = `[{"name":"ci","status":"success"}]`
	before.ReviewDecision = "APPROVED"
	before.AssigneesJSON = `["alice"]`
	before.ReviewersJSON = `["bob"]`
	before.MergeableState = "clean"
	_, err = database.UpsertMergeRequest(t.Context(), before)
	require.NoError(err)
	client := setupTestClient(t, srv)

	resp, err := client.HTTP.SetPrGithubStateWithResponse(t.Context(), &generated.SetPrGithubStateRequestOptions{PathParams: &generated.SetPrGithubStatePath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}, Body: &generated.SetPrGithubStateBody{State: "draft"}})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)

	assert.Equal("acme", gotOwner)
	assert.Equal("widget", gotRepo)
	assert.Equal(1, gotNumber)
	pr, err := database.GetMergeRequest(t.Context(), "github", "github.com", "acme", "widget", 1)
	require.NoError(err)
	require.NotNil(pr)
	assert.Equal(db.MergeRequestStateOpen, pr.State)
	assert.True(pr.IsDraft)
	assert.Nil(pr.ClosedAt)
	assert.Equal(7, pr.CommentCount)
	assert.Equal("success", pr.CIStatus)
	assert.JSONEq(`[{"name":"ci","status":"success"}]`, pr.CIChecksJSON)
	assert.Equal("APPROVED", pr.ReviewDecision)
	assert.Equal([]string{"alice"}, pr.Assignees)
	assert.Equal([]string{"bob"}, pr.RequestedReviewers)
	assert.Equal("clean", pr.MergeableState)
}

func TestResolveItem_PR(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	repos := []ghclient.RepoRef{{Owner: "acme", Name: "widget"}}
	srv, database := setupTestServerWithRepos(t, &mockGH{}, repos)
	seedPR(t, database, "acme", "widget", 42)
	client := setupTestClient(t, srv)

	resp, err := client.HTTP.ResolveRepoItemWithResponse(t.Context(), &generated.ResolveRepoItemRequestOptions{PathParams: &generated.ResolveRepoItemPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(42)}})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)
	require.NotNil(resp.JSON200)
	require.Equal("pr", resp.JSON200.ItemType)
	require.EqualValues(42, resp.JSON200.Number)
	require.True(resp.JSON200.RepoTracked)
}

func TestProviderPullRouteResolvesEscapedGitLabRepoPath(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	srv, database := setupTestServer(t)
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Second)

	repoPath := "Group/SubGroup/SubGroup 2/My_Project.v2"
	repoID, err := database.UpsertRepo(ctx, db.RepoIdentity{
		Platform:       "gitlab",
		PlatformHost:   "gitlab.example.com:8443",
		PlatformRepoID: "gid://gitlab/Project/12000",
		Owner:          "Group/SubGroup/SubGroup 2",
		Name:           "My_Project.v2",
		RepoPath:       repoPath,
	})
	require.NoError(err)
	_, err = database.UpsertMergeRequest(ctx, &db.MergeRequest{
		RepoID:         repoID,
		PlatformID:     12000,
		Number:         12,
		URL:            "https://gitlab.example.com/Group/SubGroup/SubGroup%202/My_Project.v2/-/merge_requests/12",
		Title:          "Nested GitLab MR",
		Author:         "testuser",
		State:          "open",
		HeadBranch:     "feature",
		BaseBranch:     "main",
		CreatedAt:      now,
		UpdatedAt:      now,
		LastActivityAt: now,
	})
	require.NoError(err)

	path := "/api/v1/host/gitlab.example.com:8443/pulls/gl/" +
		"Group%2FSubGroup%2FSubGroup%202/My_Project.v2/12"
	rr := testutil.DoJSON(t, srv, http.MethodGet, path, nil)
	require.Equal(http.StatusOK, rr.Code, rr.Body.String())

	var body generated.MergeRequestDetailResponse
	require.NoError(json.NewDecoder(rr.Body).Decode(&body))
	assert.Equal("gitlab", body.Repo.Provider)
	assert.Equal("gitlab.example.com:8443", body.Repo.PlatformHost)
	assert.Equal(repoPath, body.Repo.RepoPath)
	assert.Equal("Group/SubGroup/SubGroup 2", body.Repo.Owner)
	assert.Equal("My_Project.v2", body.Repo.Name)
	assert.Equal(int64(12), body.MergeRequest.Number)
}

func requireMR(t *testing.T, database *db.DB, repoID int64, number int) *db.MergeRequest {
	t.Helper()
	require := require.New(t)
	mr, err := database.GetMergeRequestByRepoIDAndNumber(t.Context(), repoID, number)
	require.NoError(err)
	require.NotNil(mr)
	return mr
}

func TestAPIGitealikeLockedPRPersistsThroughServer(t *testing.T) {
	runParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	base := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	transport := &lockedGitealikeTransport{
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
		pull: gitealike.PullRequestDTO{
			ID:       201,
			Index:    7,
			HTMLURL:  "https://codeberg.test/forgejo/tea/pulls/7",
			Title:    "Locked tea",
			User:     gitealike.UserDTO{UserName: "alice"},
			State:    "open",
			Draft:    false,
			IsLocked: true,
			Head:     gitealike.BranchDTO{Ref: "feature", SHA: "abc123"},
			Base:     gitealike.BranchDTO{Ref: "main", SHA: "def456"},
			Created:  base,
			Updated:  base.Add(time.Minute),
		},
	}
	provider := gitealike.NewProvider(platform.KindForgejo, "codeberg.test", transport)
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
	assert.False(mr.IsDraft)
	assert.True(mr.IsLocked)

	pullResp, err := client.HTTP.GetPullOnHostWithResponse(ctx, &generated.GetPullOnHostRequestOptions{PathParams: &generated.GetPullOnHostPath{PlatformHost: "codeberg.test", Provider: "forgejo", Owner: "forgejo", Name: "tea", Number: int64(7)}})
	require.NoError(err)
	require.Equal(http.StatusOK, pullResp.StatusCode, string(pullResp.Body))
	require.NotNil(pullResp.JSON200)
	assert.False(pullResp.JSON200.MergeRequest.IsDraft)
	assert.True(pullResp.JSON200.MergeRequest.IsLocked)
}

func TestAPIGitealikeDraftPRFieldsPersistThroughServer(t *testing.T) {
	runParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	base := time.Date(2026, 5, 2, 12, 0, 0, 0, time.UTC)
	merged := base.Add(2 * time.Hour)
	closed := base.Add(3 * time.Hour)
	transport := &lockedGitealikeTransport{
		repo: gitealike.RepositoryDTO{
			ID:            102,
			Owner:         gitealike.UserDTO{UserName: "gitea"},
			Name:          "tea",
			FullName:      "gitea/tea",
			HTMLURL:       "https://gitea.test/gitea/tea",
			CloneURL:      "https://gitea.test/gitea/tea.git",
			DefaultBranch: "main",
			Created:       base,
			Updated:       base,
		},
		pull: gitealike.PullRequestDTO{
			ID:      202,
			Index:   8,
			HTMLURL: "https://gitea.test/gitea/tea/pulls/8",
			Title:   "Draft tea",
			User:    gitealike.UserDTO{UserName: "bob"},
			State:   "closed",
			Draft:   true,
			Head:    gitealike.BranchDTO{Ref: "feature", SHA: "abc456"},
			Base:    gitealike.BranchDTO{Ref: "main", SHA: "def789"},
			Labels: []gitealike.LabelDTO{{
				ID:    301,
				Name:  "bug",
				Color: "cc0000",
			}},
			Created:  base,
			Updated:  base.Add(time.Minute),
			Merged:   true,
			MergedAt: &merged,
			Closed:   &closed,
		},
		statuses: []gitealike.StatusDTO{{
			ID:        401,
			Context:   "build",
			State:     "success",
			TargetURL: "javascript:alert(1)",
			Created:   base.Add(time.Minute),
			Updated:   base.Add(time.Minute),
		}},
	}
	provider := gitealike.NewProvider(platform.KindGitea, "gitea.test", transport)
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
			Owner:        "gitea",
			Name:         "tea",
			RepoPath:     "gitea/tea",
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
	require.NoError(syncer.SyncMR(ctx, "gitea", "tea", 8))

	repo, err := database.GetRepoByIdentity(ctx, db.RepoIdentity{
		Platform:     "gitea",
		PlatformHost: "gitea.test",
		RepoPath:     "gitea/tea",
	})
	require.NoError(err)
	require.NotNil(repo)
	mr, err := database.GetMergeRequestByRepoIDAndNumber(ctx, repo.ID, 8)
	require.NoError(err)
	require.NotNil(mr)
	assert.True(mr.IsDraft)
	assert.Equal("feature", mr.HeadBranch)
	assert.Equal("main", mr.BaseBranch)
	assert.Equal("abc456", mr.PlatformHeadSHA)
	assert.Equal("def789", mr.PlatformBaseSHA)
	assert.Equal("success", mr.CIStatus)
	assert.NotEmpty(mr.CIChecksJSON)
	assert.NotContains(mr.CIChecksJSON, "javascript:")
	require.NotNil(mr.MergedAt)
	require.NotNil(mr.ClosedAt)
	require.Len(mr.Labels, 1)
	assert.Equal("bug", mr.Labels[0].Name)

	pullResp, err := client.HTTP.GetPullOnHostWithResponse(ctx, &generated.GetPullOnHostRequestOptions{PathParams: &generated.GetPullOnHostPath{PlatformHost: "gitea.test", Provider: "gitea", Owner: "gitea", Name: "tea", Number: int64(8)}})
	require.NoError(err)
	require.Equal(http.StatusOK, pullResp.StatusCode, string(pullResp.Body))
	require.NotNil(pullResp.JSON200)
	apiMR := pullResp.JSON200.MergeRequest
	assert.True(apiMR.IsDraft)
	assert.Equal("feature", apiMR.HeadBranch)
	assert.Equal("main", apiMR.BaseBranch)
	assert.Equal("success", apiMR.CIStatus)
	assert.NotContains(apiMR.CIChecksJSON, "javascript:")
	require.NotNil(apiMR.MergedAt)
	require.NotNil(apiMR.ClosedAt)
	require.NotNil(apiMR.Labels)
	require.Len(apiMR.Labels, 1)
	assert.Equal("bug", apiMR.Labels[0].Name)
}

type lockedGitealikeTransport struct {
	repo     gitealike.RepositoryDTO
	pull     gitealike.PullRequestDTO
	statuses []gitealike.StatusDTO
}

func (t *lockedGitealikeTransport) GetRepository(
	context.Context,
	string,
	string,
) (gitealike.RepositoryDTO, error) {
	return t.repo, nil
}

func (t *lockedGitealikeTransport) ListUserRepositories(
	context.Context,
	string,
	gitealike.PageOptions,
) ([]gitealike.RepositoryDTO, gitealike.Page, error) {
	return []gitealike.RepositoryDTO{t.repo}, gitealike.Page{}, nil
}

func (t *lockedGitealikeTransport) ListOrgRepositories(
	context.Context,
	string,
	gitealike.PageOptions,
) ([]gitealike.RepositoryDTO, gitealike.Page, error) {
	return []gitealike.RepositoryDTO{t.repo}, gitealike.Page{}, nil
}

func (t *lockedGitealikeTransport) ListOpenPullRequests(
	context.Context,
	platform.RepoRef,
	gitealike.PageOptions,
) ([]gitealike.PullRequestDTO, gitealike.Page, error) {
	return []gitealike.PullRequestDTO{t.pull}, gitealike.Page{}, nil
}

func (t *lockedGitealikeTransport) GetPullRequest(
	context.Context,
	platform.RepoRef,
	int,
) (gitealike.PullRequestDTO, error) {
	return t.pull, nil
}

func (t *lockedGitealikeTransport) ListPullRequestComments(
	context.Context,
	platform.RepoRef,
	int,
	gitealike.PageOptions,
) ([]gitealike.CommentDTO, gitealike.Page, error) {
	return nil, gitealike.Page{}, nil
}

func (t *lockedGitealikeTransport) ListPullRequestReviews(
	context.Context,
	platform.RepoRef,
	int,
	gitealike.PageOptions,
) ([]gitealike.ReviewDTO, gitealike.Page, error) {
	return nil, gitealike.Page{}, nil
}

func (t *lockedGitealikeTransport) ListPullRequestCommits(
	context.Context,
	platform.RepoRef,
	int,
	gitealike.PageOptions,
) ([]gitealike.CommitDTO, gitealike.Page, error) {
	return nil, gitealike.Page{}, nil
}

func (t *lockedGitealikeTransport) ListOpenIssues(
	context.Context,
	platform.RepoRef,
	gitealike.PageOptions,
) ([]gitealike.IssueDTO, gitealike.Page, error) {
	return nil, gitealike.Page{}, nil
}

func (t *lockedGitealikeTransport) GetIssue(
	context.Context,
	platform.RepoRef,
	int,
) (gitealike.IssueDTO, error) {
	return gitealike.IssueDTO{}, platform.ErrNotFound
}

func (t *lockedGitealikeTransport) ListIssueComments(
	context.Context,
	platform.RepoRef,
	int,
	gitealike.PageOptions,
) ([]gitealike.CommentDTO, gitealike.Page, error) {
	return nil, gitealike.Page{}, nil
}

func (t *lockedGitealikeTransport) ListReleases(
	context.Context,
	platform.RepoRef,
	gitealike.PageOptions,
) ([]gitealike.ReleaseDTO, gitealike.Page, error) {
	return nil, gitealike.Page{}, nil
}

func (t *lockedGitealikeTransport) ListTags(
	context.Context,
	platform.RepoRef,
	gitealike.PageOptions,
) ([]gitealike.TagDTO, gitealike.Page, error) {
	return nil, gitealike.Page{}, nil
}

func (t *lockedGitealikeTransport) ListStatuses(
	context.Context,
	platform.RepoRef,
	string,
	gitealike.PageOptions,
) ([]gitealike.StatusDTO, gitealike.Page, error) {
	return t.statuses, gitealike.Page{}, nil
}

func TestAPIGetFilesAndDiffMarkGeneratedFilesE2E(t *testing.T) {
	runParallelServerTest(t)
	acquireRootWorkspaceGitSlot(t)
	require := require.New(t)
	assert := assert.New(t)
	ctx := t.Context()

	dir := t.TempDir()
	database := dbtest.Open(t)

	bareDir := filepath.Join(dir, "clones")
	clones := gitclone.New(bareDir, nil)
	bare, err := clones.ClonePathForContext(
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

	require.NoError(os.WriteFile(
		filepath.Join(work, "base.txt"),
		[]byte("base\n"), 0o644,
	))
	gitfixture.Run(t, work, "add", ".")
	gitfixture.Run(t, work, "commit", "-m", "base commit")
	gitfixture.Run(t, work, "push", "origin", "main")
	mergeBase := gitfixture.SHA(t, work, "HEAD")

	gitfixture.Run(t, work, "checkout", "-b", "feature")
	require.NoError(os.WriteFile(
		filepath.Join(work, ".gitattributes"),
		[]byte("dist/** linguist-generated\nbun.lock -linguist-generated\n"), 0o644,
	))
	require.NoError(os.MkdirAll(filepath.Join(work, "dist"), 0o755))
	require.NoError(os.WriteFile(
		filepath.Join(work, "dist", "api.ts"),
		[]byte("export const generated = true;\n"), 0o644,
	))
	require.NoError(os.WriteFile(
		filepath.Join(work, "bun.lock"),
		[]byte("# lock\n"), 0o644,
	))
	require.NoError(os.WriteFile(
		filepath.Join(work, "src.ts"),
		[]byte("export const source = true;\n"), 0o644,
	))
	gitfixture.Run(t, work, "add", ".")
	gitfixture.Run(t, work, "commit", "-m", "feature commit")
	gitfixture.Run(t, work, "push", "origin", "feature")
	headSHA := gitfixture.SHA(t, work, "HEAD")

	syncer := ghclient.NewSyncer(
		map[string]ghclient.Client{"github.com": &mockGH{}},
		database, nil, defaultTestRepos, time.Minute, nil, nil,
	)
	t.Cleanup(syncer.Stop)
	srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{Clones: clones})
	t.Cleanup(func() { gracefulShutdown(t, srv) })
	client := setupTestClient(t, srv)

	seedPR(t, database, "acme", "widget", 1)
	repoID, err := database.UpsertRepo(ctx, db.RepoIdentity{
		Platform:     "github",
		PlatformHost: "github.com",
		Owner:        "acme",
		Name:         "widget",
	})
	require.NoError(err)
	require.NoError(database.UpdateDiffSHAs(ctx, repoID, 1, headSHA, mergeBase, mergeBase))

	filesResp, err := client.HTTP.GetPullFilesWithResponse(ctx, &generated.GetPullFilesRequestOptions{PathParams: &generated.GetPullFilesPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}})
	require.NoError(err)
	require.Equal(http.StatusOK, filesResp.StatusCode, string(filesResp.Body))
	require.NotNil(filesResp.JSON200)
	require.NotNil(filesResp.JSON200.Files)
	assert.True(testutil.RequireWorkspaceDiffFile(t, filesResp.JSON200.Files, "dist/api.ts").IsGenerated)
	assert.False(testutil.RequireWorkspaceDiffFile(t, filesResp.JSON200.Files, "bun.lock").IsGenerated)
	assert.False(testutil.RequireWorkspaceDiffFile(t, filesResp.JSON200.Files, "src.ts").IsGenerated)

	diffResp, err := client.HTTP.GetPullDiffWithResponse(ctx, &generated.GetPullDiffRequestOptions{PathParams: &generated.GetPullDiffPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}})
	require.NoError(err)
	require.Equal(http.StatusOK, diffResp.StatusCode, string(diffResp.Body))
	require.NotNil(diffResp.JSON200)
	require.NotNil(diffResp.JSON200.Files)
	assert.True(testutil.RequireWorkspaceDiffFile(t, diffResp.JSON200.Files, "dist/api.ts").IsGenerated)
	assert.False(testutil.RequireWorkspaceDiffFile(t, diffResp.JSON200.Files, "bun.lock").IsGenerated)
	assert.False(testutil.RequireWorkspaceDiffFile(t, diffResp.JSON200.Files, "src.ts").IsGenerated)
}

func TestAPIGetPullDetailLoaded(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)

	srv, database := setupTestServer(t)
	seedPR(t, database, "acme", "widget", 1)
	client := setupTestClient(t, srv)

	// Before detail fetch: detail_loaded=false.
	resp, err := client.HTTP.GetPullWithResponse(t.Context(), &generated.GetPullRequestOptions{PathParams: &generated.GetPullPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)
	require.NotNil(resp.JSON200)
	assert.False(resp.JSON200.DetailLoaded)
	assert.Nil(resp.JSON200.DetailFetchedAt)

	// Insert a second PR with DetailFetchedAt set.
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Second)
	repoID, err := database.UpsertRepo(ctx, verifiedGitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	_, err = database.UpsertMergeRequest(ctx, &db.MergeRequest{
		RepoID:          repoID,
		PlatformID:      2000,
		Number:          2,
		URL:             "https://github.com/acme/widget/pull/2",
		Title:           "PR with detail",
		Author:          "testuser",
		State:           "open",
		HeadBranch:      "feature",
		BaseBranch:      "main",
		CreatedAt:       now,
		UpdatedAt:       now,
		LastActivityAt:  now,
		DetailFetchedAt: &now,
	})
	require.NoError(err)

	resp2, err := client.HTTP.GetPullWithResponse(t.Context(), &generated.GetPullRequestOptions{PathParams: &generated.GetPullPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(2)}})
	require.NoError(err)
	require.Equal(http.StatusOK, resp2.StatusCode)
	require.NotNil(resp2.JSON200)
	assert.True(resp2.JSON200.DetailLoaded)
	require.NotNil(resp2.JSON200.DetailFetchedAt)
	assertRFC3339UTC(t, *resp2.JSON200.DetailFetchedAt, now)
}

func setupTestServerWithClones(t *testing.T) (
	client *apiclient.Client,
	database *db.DB,
	mergeBase string,
	headSHA string,
	commitSHAs []string,
) {
	t.Helper()

	client, database, mergeBase, headSHA, commitSHAs, _ = setupTestServerWithClonesAndServer(t)
	return client, database, mergeBase, headSHA, commitSHAs
}

func setupTestServerWithClonesAndServer(t *testing.T) (
	client *apiclient.Client,
	database *db.DB,
	mergeBase string,
	headSHA string,
	commitSHAs []string,
	srv *server.Server,
) {
	t.Helper()
	acquireRootWorkspaceGitSlot(t)

	dir := t.TempDir()
	database = dbtest.Open(t)

	bareDir := filepath.Join(dir, "clones")
	require.NoError(t, os.MkdirAll(bareDir, 0o755))
	clones := gitclone.New(bareDir, nil)
	bare, err := clones.ClonePathForContext(
		gitclone.WithRepositoryIdentity(t.Context(), "repo-acme-widget"),
		"github", "github.com", "acme", "widget",
	)
	require.NoError(t, err)

	tmpWork := filepath.Join(dir, "work")
	gitfixture.Run(t, dir, "init", "--bare", "--initial-branch=main", bare)
	gitfixture.Run(t, dir, "clone", bare, tmpWork)
	gitfixture.Run(t, tmpWork, "config", "user.email", "test@test.com")
	gitfixture.Run(t, tmpWork, "config", "user.name", "Test")

	require.NoError(t, os.WriteFile(filepath.Join(tmpWork, "base.txt"), []byte("base\n"), 0o644))
	gitfixture.Run(t, tmpWork, "add", ".")
	gitfixture.Run(t, tmpWork, "commit", "-m", "base commit")
	gitfixture.Run(t, tmpWork, "push", "origin", "main")
	mergeBase = gitfixture.SHA(t, tmpWork, "HEAD")

	gitfixture.Run(t, tmpWork, "checkout", "-b", "pr")
	for i := 1; i <= 5; i++ {
		fname := fmt.Sprintf("file%d.txt", i)
		require.NoError(t, os.WriteFile(filepath.Join(tmpWork, fname), fmt.Appendf(nil, "content %d\n", i), 0o644))
		gitfixture.Run(t, tmpWork, "add", ".")
		gitfixture.Run(t, tmpWork, "commit", "-m", fmt.Sprintf("commit %d", i))
	}
	gitfixture.Run(t, tmpWork, "push", "origin", "pr")
	headSHA = gitfixture.SHA(t, tmpWork, "HEAD")

	// Collect SHAs newest-first.
	commitSHAs = make([]string, 5)
	sha := headSHA
	for i := range 5 {
		commitSHAs[i] = sha
		sha = gitfixture.SHA(t, tmpWork, sha+"^1")
	}

	mock := &mockGH{}
	repos := []ghclient.RepoRef{{Platform: "github", Owner: "acme", Name: "widget", PlatformHost: "github.com"}}
	syncer := ghclient.NewSyncer(map[string]ghclient.Client{"github.com": mock}, database, nil, repos, time.Minute, nil, nil)
	t.Cleanup(syncer.Stop)
	srv = server.New(database, syncer, nil, "/", nil, server.ServerOptions{Clones: clones})

	seedPR(t, database, "acme", "widget", 1)
	ctx := t.Context()
	repoID, err := database.UpsertRepo(ctx, verifiedGitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(t, err)
	require.NoError(t, database.UpdateDiffSHAs(ctx, repoID, 1, headSHA, mergeBase, mergeBase))

	client = setupTestClient(t, srv)
	return client, database, mergeBase, headSHA, commitSHAs, srv
}

func TestAPIGetCommits(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)

	client, _, _, _, commitSHAs := setupTestServerWithClones(t)
	resp, err := client.HTTP.GetPullCommitsWithResponse(t.Context(), &generated.GetPullCommitsRequestOptions{PathParams: &generated.GetPullCommitsPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)
	require.NotNil(resp.JSON200)
	assert.Len(resp.JSON200.Commits, 5)
	assert.Equal(commitSHAs[0], resp.JSON200.Commits[0].Sha)
	assert.Equal("commit 5", resp.JSON200.Commits[0].Message)
	require.NotNil(resp.JSON200.Commits[0].Stats)
	assert.Equal(int64(1), resp.JSON200.Commits[0].Stats.Additions)
	assert.Zero(resp.JSON200.Commits[0].Stats.Deletions)
	assert.Equal(time.UTC, resp.JSON200.Commits[0].AuthoredAt.Location())
}

func TestAPIGetCommits_NotFound(t *testing.T) {
	runParallelServerTest(t)
	client, _, _, _, _ := setupTestServerWithClones(t)

	resp, err := client.HTTP.GetPullCommitsWithResponse(t.Context(), &generated.GetPullCommitsRequestOptions{PathParams: &generated.GetPullCommitsPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(999)}})
	require.Error(t, err)
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
}

func TestAPIGetDiff_SingleCommit(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)

	client, _, _, _, commitSHAs := setupTestServerWithClones(t)
	resp, err := client.HTTP.GetPullDiffWithResponse(t.Context(), &generated.GetPullDiffRequestOptions{PathParams: &generated.GetPullDiffPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}, Query: &generated.GetPullDiffQuery{Commit: &commitSHAs[2]}})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)
	require.NotNil(resp.JSON200)
	require.Len(resp.JSON200.Files, 1)
}

func TestAPIGetDiffReportsSyncedDiffHeadSHA(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)

	client, _, _, headSHA, _ := setupTestServerWithClones(t)
	resp, err := client.HTTP.GetPullDiffWithResponse(t.Context(), &generated.GetPullDiffRequestOptions{PathParams: &generated.GetPullDiffPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)
	require.NotNil(resp.JSON200)
	require.NotNil(resp.JSON200.DiffHeadSha)
	assert.Equal(headSHA, *resp.JSON200.DiffHeadSha)
}

func TestAPIGetFilePreview_ReturnsHeadContent(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)

	_, _, _, _, _, srv := setupTestServerWithClonesAndServer(t)
	req := httptest.NewRequestWithContext(t.Context(),
		http.MethodGet,
		"/api/v1/pulls/gh/acme/widget/1/file-preview?path=file5.txt",
		nil,
	)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	require.Equal(http.StatusOK, rr.Code)
	assert.Contains(rr.Body.String(), `"path":"file5.txt"`)
	assert.Contains(rr.Body.String(), `"media_type":"text/plain; charset=utf-8"`)
	assert.Contains(rr.Body.String(), `"encoding":"base64"`)
	assert.Contains(rr.Body.String(), `"content":"Y29udGVudCA1Cg=="`)
}

func TestAPIGetFilePreview_ReturnsDeletedFileContent(t *testing.T) {
	runParallelServerTest(t)
	acquireRootWorkspaceGitSlot(t)
	require := require.New(t)
	assert := assert.New(t)
	ctx := t.Context()

	dir := t.TempDir()
	database := dbtest.Open(t)

	seedPR(t, database, "acme", "widgets", 1)
	diffRepo, err := testutil.SetupDiffRepo(ctx, dir, database)
	require.NoError(err)

	mock := &mockGH{}
	repos := []ghclient.RepoRef{{
		Owner: "acme", Name: "widgets", PlatformHost: "github.com",
	}}
	syncer := ghclient.NewSyncer(
		map[string]ghclient.Client{"github.com": mock},
		database, nil, repos, time.Minute, nil, nil,
	)
	t.Cleanup(syncer.Stop)
	srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{
		Clones: diffRepo.Manager,
	})
	t.Cleanup(func() { gracefulShutdown(t, srv) })
	client := setupTestClient(t, srv)

	path := "config.yaml"
	resp, err := client.HTTP.GetPullFilePreviewWithResponse(ctx, &generated.GetPullFilePreviewRequestOptions{PathParams: &generated.GetPullFilePreviewPath{Provider: "gh", Owner: "acme", Name: "widgets", Number: int64(1)}, Query: &generated.GetPullFilePreviewQuery{Path: &path}})

	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)
	require.NotNil(resp.JSON200)
	assert.Equal(path, resp.JSON200.Path)
	decoded, err := base64.StdEncoding.DecodeString(resp.JSON200.Content)
	require.NoError(err)
	assert.Contains(string(decoded), "wal_mode: true")
}

func TestAPIGetFilePreview_ReturnsRequestedDiffSideContent(t *testing.T) {
	runParallelServerTest(t)
	acquireRootWorkspaceGitSlot(t)
	require := require.New(t)
	assert := assert.New(t)
	ctx := t.Context()

	dir := t.TempDir()
	database := dbtest.Open(t)

	seedPR(t, database, "acme", "widgets", 1)
	diffRepo, err := testutil.SetupDiffRepo(ctx, dir, database)
	require.NoError(err)

	mock := &mockGH{}
	repos := []ghclient.RepoRef{{
		Owner: "acme", Name: "widgets", PlatformHost: "github.com",
	}}
	syncer := ghclient.NewSyncer(
		map[string]ghclient.Client{"github.com": mock},
		database, nil, repos, time.Minute, nil, nil,
	)
	t.Cleanup(syncer.Stop)
	srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{
		Clones: diffRepo.Manager,
	})
	t.Cleanup(func() { gracefulShutdown(t, srv) })
	client := setupTestClient(t, srv)

	path := "internal/handler.go"
	oldSide := generated.GetPullFilePreviewQuerySideOld
	oldResp, err := client.HTTP.GetPullFilePreviewWithResponse(ctx, &generated.GetPullFilePreviewRequestOptions{PathParams: &generated.GetPullFilePreviewPath{Provider: "gh", Owner: "acme", Name: "widgets", Number: int64(1)}, Query: &generated.GetPullFilePreviewQuery{Path: &path, Side: &oldSide}})
	require.NoError(err)
	require.Equal(http.StatusOK, oldResp.StatusCode)
	require.NotNil(oldResp.JSON200)
	oldDecoded, err := base64.StdEncoding.DecodeString(oldResp.JSON200.Content)
	require.NoError(err)

	newSide := generated.GetPullFilePreviewQuerySideNew
	newResp, err := client.HTTP.GetPullFilePreviewWithResponse(ctx, &generated.GetPullFilePreviewRequestOptions{PathParams: &generated.GetPullFilePreviewPath{Provider: "gh", Owner: "acme", Name: "widgets", Number: int64(1)}, Query: &generated.GetPullFilePreviewQuery{Path: &path, Side: &newSide}})
	require.NoError(err)
	require.Equal(http.StatusOK, newResp.StatusCode)
	require.NotNil(newResp.JSON200)
	newDecoded, err := base64.StdEncoding.DecodeString(newResp.JSON200.Content)
	require.NoError(err)

	assert.Contains(string(oldDecoded), `log.Println("handling request")`)
	assert.NotContains(string(oldDecoded), "slog.Info")
	assert.Contains(string(newDecoded), `slog.Info("handling request"`)
	assert.NotContains(string(newDecoded), "log.Println")
}

func TestAPIGetDiff_Range(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)

	client, _, _, _, commitSHAs := setupTestServerWithClones(t)
	from := commitSHAs[4] // commit 1 (oldest)
	to := commitSHAs[2]   // commit 3
	resp, err := client.HTTP.GetPullDiffWithResponse(t.Context(), &generated.GetPullDiffRequestOptions{PathParams: &generated.GetPullDiffPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}, Query: &generated.GetPullDiffQuery{From: &from, To: &to}})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)
	require.NotNil(resp.JSON200)
	require.Len(resp.JSON200.Files, 3)
}

func TestAPIGetDiff_InvalidScope(t *testing.T) {
	runParallelServerTest(t)
	client, _, _, _, commitSHAs := setupTestServerWithClones(t)
	from := commitSHAs[0]
	resp, err := client.HTTP.GetPullDiffWithResponse(t.Context(), &generated.GetPullDiffRequestOptions{PathParams: &generated.GetPullDiffPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}, Query: &generated.GetPullDiffQuery{Commit: &commitSHAs[0], From: &from}})
	require.Error(t, err)
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestAPIGetDiff_UnknownSHA(t *testing.T) {
	runParallelServerTest(t)
	client, _, _, _, _ := setupTestServerWithClones(t)
	bogus := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	resp, err := client.HTTP.GetPullDiffWithResponse(t.Context(), &generated.GetPullDiffRequestOptions{PathParams: &generated.GetPullDiffPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}, Query: &generated.GetPullDiffQuery{Commit: &bogus}})
	require.Error(t, err)
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestAPIGetDiff_ReversedRange(t *testing.T) {
	runParallelServerTest(t)
	client, _, _, _, commitSHAs := setupTestServerWithClones(t)
	from := commitSHAs[0] // newest
	to := commitSHAs[4]   // oldest
	resp, err := client.HTTP.GetPullDiffWithResponse(t.Context(), &generated.GetPullDiffRequestOptions{PathParams: &generated.GetPullDiffPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}, Query: &generated.GetPullDiffQuery{From: &from, To: &to}})
	require.Error(t, err)
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestAPIGetDiff_FromWithoutTo(t *testing.T) {
	runParallelServerTest(t)
	client, _, _, _, commitSHAs := setupTestServerWithClones(t)
	from := commitSHAs[0]
	resp, err := client.HTTP.GetPullDiffWithResponse(t.Context(), &generated.GetPullDiffRequestOptions{PathParams: &generated.GetPullDiffPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(1)}, Query: &generated.GetPullDiffQuery{From: &from}})
	require.Error(t, err)
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
}

func TestAPIGetDiff_RootCommit(t *testing.T) {
	runParallelServerTest(t)
	acquireRootWorkspaceGitSlot(t)
	require := require.New(t)

	dir := t.TempDir()
	database := dbtest.Open(t)

	bareDir := filepath.Join(dir, "clones")
	require.NoError(os.MkdirAll(bareDir, 0o755))
	clones := gitclone.New(bareDir, nil)
	bare, err := clones.ClonePathForContext(
		gitclone.WithRepositoryIdentity(t.Context(), "repo-acme-rootrepo"),
		"github", "github.com", "acme", "rootrepo",
	)
	require.NoError(err)
	tmpWork := filepath.Join(dir, "work")
	gitfixture.Run(t, dir, "init", "--bare", "--initial-branch=main", bare)
	gitfixture.Run(t, dir, "clone", bare, tmpWork)
	gitfixture.Run(t, tmpWork, "config", "user.email", "test@test.com")
	gitfixture.Run(t, tmpWork, "config", "user.name", "Test")

	require.NoError(os.WriteFile(filepath.Join(tmpWork, "root.txt"), []byte("root\n"), 0o644))
	gitfixture.Run(t, tmpWork, "add", ".")
	gitfixture.Run(t, tmpWork, "commit", "-m", "root commit")
	rootSHA := gitfixture.SHA(t, tmpWork, "HEAD")

	require.NoError(os.WriteFile(filepath.Join(tmpWork, "second.txt"), []byte("second\n"), 0o644))
	gitfixture.Run(t, tmpWork, "add", ".")
	gitfixture.Run(t, tmpWork, "commit", "-m", "second commit")
	gitfixture.Run(t, tmpWork, "push", "origin", "main")
	headSHA := gitfixture.SHA(t, tmpWork, "HEAD")

	mock := &mockGH{}
	repos := []ghclient.RepoRef{{Owner: "acme", Name: "rootrepo", PlatformHost: "github.com"}}
	syncer := ghclient.NewSyncer(map[string]ghclient.Client{"github.com": mock}, database, nil, repos, time.Minute, nil, nil)
	t.Cleanup(syncer.Stop)
	srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{Clones: clones})

	seedPR(t, database, "acme", "rootrepo", 1)
	ctx := t.Context()
	repoID, err := database.UpsertRepo(ctx, verifiedGitHubRepoIdentity("github.com", "acme", "rootrepo"))
	require.NoError(err)
	require.NoError(database.UpdateDiffSHAs(ctx, repoID, 1, headSHA, "4b825dc642cb6eb9a060e54bf8d69288fbee4904", "4b825dc642cb6eb9a060e54bf8d69288fbee4904"))

	client := setupTestClient(t, srv)
	resp, err := client.HTTP.GetPullDiffWithResponse(t.Context(), &generated.GetPullDiffRequestOptions{PathParams: &generated.GetPullDiffPath{Provider: "gh", Owner: "acme", Name: "rootrepo", Number: int64(1)}, Query: &generated.GetPullDiffQuery{Commit: &rootSHA}})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)
	require.NotNil(resp.JSON200)
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

func seedStackedPRMergeable(
	t *testing.T, database *db.DB,
	owner, name string, number int,
	head, base string, state db.MergeRequestState, ci, review, mergeableState string,
) int64 {
	return seedStackedPRState(t, database, owner, name, number, head, base, state, ci, review, false, mergeableState)
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

func TestAPIListStacks(t *testing.T) {
	runParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	srv, database := setupTestServer(t)
	client := setupTestClient(t, srv)

	seedStackedPR(t, database, "acme", "widget", 10, "feat/auth", "main", db.MergeRequestStateOpen, "success", "APPROVED")
	seedStackedPR(t, database, "acme", "widget", 11, "feat/auth-retry", "feat/auth", db.MergeRequestStateOpen, "success", "APPROVED")
	seedStackedPR(t, database, "acme", "widget", 12, "feat/auth-ui", "feat/auth-retry", db.MergeRequestStateOpen, "pending", "")
	runStackDetection(t, database, "acme", "widget")

	resp, err := client.HTTP.ListStacksWithResponse(t.Context(), &generated.ListStacksRequestOptions{Query: &generated.ListStacksQuery{}})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)

	var stks []generated.StackResponse
	require.NoError(json.Unmarshal(resp.Body, &stks))
	assert.Len(stks, 1)
	assert.Equal("auth", stks[0].Name)
	require.NotNil(stks[0].Members)
	assert.Len(stks[0].Members, 3)
	assert.Equal(int64(10), stks[0].Members[0].Number)
}

func TestAPIListStacks_RepoFilter(t *testing.T) {
	runParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	repos := []ghclient.RepoRef{
		{Platform: "github", Owner: "acme", Name: "widget", PlatformHost: "github.com"},
		{Platform: "github", Owner: "acme", Name: "tools", PlatformHost: "github.com"},
	}
	srv, database := setupTestServerWithRepos(t, &mockGH{}, repos)
	client := setupTestClient(t, srv)
	ctx := t.Context()

	seedStackedPR(t, database, "acme", "widget", 10, "feat/a", "main", db.MergeRequestStateOpen, "", "")
	seedStackedPR(t, database, "acme", "widget", 11, "feat/b", "feat/a", db.MergeRequestStateOpen, "", "")
	runStackDetection(t, database, "acme", "widget")

	seedStackedPR(t, database, "acme", "tools", 20, "feat/c", "main", db.MergeRequestStateOpen, "", "")
	seedStackedPR(t, database, "acme", "tools", 21, "feat/d", "feat/c", db.MergeRequestStateOpen, "", "")
	runStackDetection(t, database, "acme", "tools")

	respAll, err := client.HTTP.ListStacksWithResponse(ctx, &generated.ListStacksRequestOptions{Query: &generated.ListStacksQuery{}})
	require.NoError(err)
	var allStks []generated.StackResponse
	require.NoError(json.Unmarshal(respAll.Body, &allStks))
	assert.Len(allStks, 2)

	repo := "acme/widget"
	resp, err := client.HTTP.ListStacksWithResponse(ctx, &generated.ListStacksRequestOptions{Query: &generated.ListStacksQuery{Repo: &repo}})
	require.NoError(err)
	assert.Equal(http.StatusOK, resp.StatusCode)
	var filtered []generated.StackResponse
	require.NoError(json.Unmarshal(resp.Body, &filtered))
	assert.Len(filtered, 1)
	assert.Equal("widget", filtered[0].RepoName)

	bad := "noslash"
	resp2, err := client.HTTP.ListStacksWithResponse(ctx, &generated.ListStacksRequestOptions{Query: &generated.ListStacksQuery{Repo: &bad}})
	require.Error(err)
	require.NotNil(resp2)
	assert.Equal(http.StatusBadRequest, resp2.StatusCode)
	assert.Contains(string(resp2.Body), "invalid repo filter")
}

func TestAPIGetStackForPR(t *testing.T) {
	runParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	srv, database := setupTestServer(t)
	client := setupTestClient(t, srv)
	ctx := t.Context()

	// Failing base with an open descendant is blocked.
	seedStackedPR(t, database, "acme", "widget", 10, "feat/api-base", "main", db.MergeRequestStateOpen, "failure", "")
	seedStackedPR(t, database, "acme", "widget", 11, "feat/api-retry", "feat/api-base", db.MergeRequestStateOpen, "success", "APPROVED")
	runStackDetection(t, database, "acme", "widget")

	resp, err := client.HTTP.GetPullStackWithResponse(ctx, &generated.GetPullStackRequestOptions{PathParams: &generated.GetPullStackPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(10)}})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)
	require.NotNil(resp.JSON200)
	assert.Equal("api", resp.JSON200.StackName)
	assert.Equal(int64(2), resp.JSON200.Size)
	assert.Equal("blocked", resp.JSON200.Health)

	seedPR(t, database, "acme", "widget", 99)
	resp2, err := client.HTTP.GetPullStackWithResponse(ctx, &generated.GetPullStackRequestOptions{PathParams: &generated.GetPullStackPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(99)}})
	require.Error(err)
	require.NotNil(resp2)
	assert.Equal(http.StatusNotFound, resp2.StatusCode)
}

func TestAPIGetPullDetailIncludesStackContext(t *testing.T) {
	runParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	srv, database := setupTestServer(t)
	client := setupTestClient(t, srv)
	ctx := t.Context()

	seedStackedPR(t, database, "acme", "widget", 10, "feat/api-base", "main", db.MergeRequestStateOpen, "failure", "")
	seedStackedPR(t, database, "acme", "widget", 11, "feat/api-retry", "feat/api-base", db.MergeRequestStateOpen, "success", "APPROVED")
	runStackDetection(t, database, "acme", "widget")

	resp, err := client.HTTP.GetPullWithResponse(ctx, &generated.GetPullRequestOptions{PathParams: &generated.GetPullPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(11)}})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)
	require.NotNil(resp.JSON200)
	require.NotNil(resp.JSON200.Stack)
	assert.Equal("api", resp.JSON200.Stack.StackName)
	assert.Equal(int64(2), resp.JSON200.Stack.Position)
	assert.Equal(int64(2), resp.JSON200.Stack.Size)
	assert.Equal("blocked", resp.JSON200.Stack.Health)
	require.NotNil(resp.JSON200.Stack.Members)
	assert.Len(resp.JSON200.Stack.Members, 2)

	seedPR(t, database, "acme", "widget", 99)
	unstacked, err := client.HTTP.GetPullWithResponse(ctx, &generated.GetPullRequestOptions{PathParams: &generated.GetPullPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(99)}})
	require.NoError(err)
	require.Equal(http.StatusOK, unstacked.StatusCode)
	require.NotNil(unstacked.JSON200)
	assert.Nil(unstacked.JSON200.Stack)
}

func TestAPIStackBaseConflictMarksDownstreamPRsDirty(t *testing.T) {
	runParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	srv, database := setupTestServer(t)
	client := setupTestClient(t, srv)
	ctx := t.Context()

	seedStackedPRMergeable(
		t, database, "acme", "widget", 10,
		"feat/api-base", "main", db.MergeRequestStateOpen, "success", "APPROVED", "dirty",
	)
	seedStackedPR(t, database, "acme", "widget", 11, "feat/api-retry", "feat/api-base", db.MergeRequestStateOpen, "success", "APPROVED")
	runStackDetection(t, database, "acme", "widget")

	repo, err := database.GetRepoByIdentity(ctx, verifiedGitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	require.NotNil(repo)
	assert.Empty(requireMR(t, database, repo.ID, 11).MergeableState)

	listResp, err := client.HTTP.ListPullsWithResponse(ctx, &generated.ListPullsRequestOptions{Query: &generated.ListPullsQuery{}})
	require.NoError(err)
	require.Equal(http.StatusOK, listResp.StatusCode, string(listResp.Body))
	require.NotNil(listResp.JSON200)
	require.Len(*listResp.JSON200, 2)
	assert.Equal("dirty", (*listResp.JSON200)[0].MergeableState)
	assert.Equal("dirty", (*listResp.JSON200)[1].MergeableState)

	stackResp, err := client.HTTP.GetPullStackWithResponse(ctx, &generated.GetPullStackRequestOptions{PathParams: &generated.GetPullStackPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(11)}})
	require.NoError(err)
	require.Equal(http.StatusOK, stackResp.StatusCode, string(stackResp.Body))
	require.NotNil(stackResp.JSON200)
	require.NotNil(stackResp.JSON200.Members)
	assert.Equal("blocked", stackResp.JSON200.Health)
	assert.Equal("dirty", stackResp.JSON200.Members[0].MergeableState)
	assert.Equal("dirty", stackResp.JSON200.Members[1].MergeableState)
	require.NotNil(stackResp.JSON200.Members[1].BlockedBy)
	assert.Equal(int64(10), *stackResp.JSON200.Members[1].BlockedBy)

	detailResp, err := client.HTTP.GetPullWithResponse(ctx, &generated.GetPullRequestOptions{PathParams: &generated.GetPullPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(11)}})
	require.NoError(err)
	require.Equal(http.StatusOK, detailResp.StatusCode, string(detailResp.Body))
	require.NotNil(detailResp.JSON200)
	assert.Equal("dirty", detailResp.JSON200.MergeRequest.MergeableState)
	assert.Empty(requireMR(t, database, repo.ID, 11).MergeableState)
}

func TestAPIListStacks_DraftNotAllGreen(t *testing.T) {
	runParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	srv, database := setupTestServer(t)
	client := setupTestClient(t, srv)

	// Both draft, green CI + approved — must not be all_green.
	seedStackedPRDraft(t, database, "acme", "widget", 10, "feat/a", "main", db.MergeRequestStateOpen, "success", "APPROVED", true)
	seedStackedPRDraft(t, database, "acme", "widget", 11, "feat/b", "feat/a", db.MergeRequestStateOpen, "success", "APPROVED", true)
	runStackDetection(t, database, "acme", "widget")

	resp, err := client.HTTP.ListStacksWithResponse(t.Context(), &generated.ListStacksRequestOptions{Query: &generated.ListStacksQuery{}})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)

	var stks []generated.StackResponse
	require.NoError(json.Unmarshal(resp.Body, &stks))
	require.Len(stks, 1)
	assert.NotEqual("all_green", stks[0].Health, "all-draft stack must not be all_green")
	assert.NotEqual("base_ready", stks[0].Health, "draft base must not be base_ready")
}

func TestAPIStacks_GitLabUnknownForkHeadSyncsButSkipsStackEdges(t *testing.T) {
	runParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	ctx := t.Context()

	now := time.Now().UTC().Truncate(time.Second)
	repoRef := platform.RepoRef{
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
	makeMR := func(platformID int64, number int, head, base, headRepoCloneURL string) platform.MergeRequest {
		return platform.MergeRequest{
			Repo:               repoRef,
			PlatformID:         platformID,
			PlatformExternalID: fmt.Sprintf("gid://gitlab/MergeRequest/%d", platformID),
			Number:             number,
			URL:                fmt.Sprintf("https://gitlab.example.com/group/project/-/merge_requests/%d", number),
			Title:              fmt.Sprintf("MR !%d: %s", number, head),
			Author:             "ada",
			State:              "open",
			HeadBranch:         head,
			BaseBranch:         base,
			HeadSHA:            fmt.Sprintf("sha%d", number),
			BaseSHA:            "basesha",
			HeadRepoCloneURL:   headRepoCloneURL,
			CreatedAt:          now,
			UpdatedAt:          now,
			LastActivityAt:     now,
		}
	}
	provider := &apiTestGitLabProvider{
		ref: repoRef,
		mergeRequests: []platform.MergeRequest{
			makeMR(9001, 90, "feature/fork-ui", "feature/auth", ""),
			makeMR(1001, 100, "feature/auth", "main", repoRef.CloneURL),
			makeMR(1011, 101, "feature/auth-ui", "feature/auth", repoRef.CloneURL),
		},
	}
	registry, err := platform.NewRegistry(provider)
	require.NoError(err)

	database := dbtest.Open(t)
	syncer := ghclient.NewSyncerWithRegistry(
		registry, database, nil, []ghclient.RepoRef{{
			Platform:           platform.KindGitLab,
			Owner:              "group",
			Name:               "project",
			PlatformHost:       "gitlab.example.com",
			RepoPath:           "group/project",
			PlatformRepoID:     4242,
			PlatformExternalID: "gid://gitlab/Project/4242",
			WebURL:             "https://gitlab.example.com/group/project",
			CloneURL:           repoRef.CloneURL,
			DefaultBranch:      "main",
		}}, time.Minute, nil, nil,
	)
	t.Cleanup(syncer.Stop)
	srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{})
	t.Cleanup(func() { gracefulShutdown(t, srv) })
	client := setupTestClient(t, srv)

	syncer.SetOnSyncCompleted(stacks.SyncCompletedHook(ctx, database, nil))
	syncer.RunOnce(ctx)

	pullsResp, err := client.HTTP.ListPullsWithResponse(ctx, &generated.ListPullsRequestOptions{Query: &generated.ListPullsQuery{}})
	require.NoError(err)
	require.Equal(http.StatusOK, pullsResp.StatusCode, string(pullsResp.Body))
	require.NotNil(pullsResp.JSON200)
	pullNumbers := make([]int64, 0, len(*pullsResp.JSON200))
	for _, pull := range *pullsResp.JSON200 {
		pullNumbers = append(pullNumbers, pull.Number)
	}
	assert.ElementsMatch([]int64{90, 100, 101}, pullNumbers)

	forkResp, err := client.HTTP.GetPullOnHostWithResponse(ctx, &generated.GetPullOnHostRequestOptions{PathParams: &generated.GetPullOnHostPath{PlatformHost: "gitlab.example.com", Provider: "gl", Owner: "group", Name: "project", Number: int64(90)}})
	require.NoError(err)
	require.Equal(http.StatusOK, forkResp.StatusCode, string(forkResp.Body))
	require.NotNil(forkResp.JSON200)
	assert.Empty(forkResp.JSON200.MergeRequest.HeadRepoCloneURL)
	assert.Nil(forkResp.JSON200.Stack)

	forkStackResp, err := client.HTTP.GetPullStackOnHostWithResponse(ctx, &generated.GetPullStackOnHostRequestOptions{PathParams: &generated.GetPullStackOnHostPath{PlatformHost: "gitlab.example.com", Provider: "gl", Owner: "group", Name: "project", Number: int64(90)}})
	require.Error(err)
	require.NotNil(forkStackResp)
	assert.Equal(http.StatusNotFound, forkStackResp.StatusCode, string(forkStackResp.Body))

	tipStackResp, err := client.HTTP.GetPullStackOnHostWithResponse(ctx, &generated.GetPullStackOnHostRequestOptions{PathParams: &generated.GetPullStackOnHostPath{PlatformHost: "gitlab.example.com", Provider: "gl", Owner: "group", Name: "project", Number: int64(101)}})
	require.NoError(err)
	require.Equal(http.StatusOK, tipStackResp.StatusCode, string(tipStackResp.Body))
	require.NotNil(tipStackResp.JSON200)
	require.NotNil(tipStackResp.JSON200.Members)
	assert.Equal(int64(2), tipStackResp.JSON200.Size)
	assert.Equal([]int64{100, 101}, stackMemberNumbers(tipStackResp.JSON200.Members))

	stacksResp, err := client.HTTP.ListStacksWithResponse(ctx, &generated.ListStacksRequestOptions{Query: &generated.ListStacksQuery{}})
	require.NoError(err)
	require.Equal(http.StatusOK, stacksResp.StatusCode, string(stacksResp.Body))
	require.NotNil(stacksResp.JSON200)
	require.Len(*stacksResp.JSON200, 1)
	require.NotNil((*stacksResp.JSON200)[0].Members)
	assert.Equal([]int64{100, 101}, stackMemberNumbers((*stacksResp.JSON200)[0].Members))
}

func stackMemberNumbers(members []generated.StackMemberResponse) []int64 {
	numbers := make([]int64, len(members))
	for i, member := range members {
		numbers[i] = member.Number
	}
	return numbers
}

func TestAPIGetStackForPR_SingleFailingIsInProgress(t *testing.T) {
	runParallelServerTest(t)
	assert := assert.New(t)
	srv, database := setupTestServer(t)
	client := setupTestClient(t, srv)

	// 2-PR chain where tip is failing but has no descendants.
	// Per blocked semantics, this is partial_merge when base is merged.
	seedStackedPR(t, database, "acme", "widget", 10, "feat/base", "main", db.MergeRequestStateMerged, "success", "APPROVED")
	seedStackedPR(t, database, "acme", "widget", 11, "feat/tip", "feat/base", db.MergeRequestStateOpen, "failure", "")
	runStackDetection(t, database, "acme", "widget")

	resp, err := client.HTTP.GetPullStackWithResponse(t.Context(), &generated.GetPullStackRequestOptions{PathParams: &generated.GetPullStackPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(11)}})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.NotNil(t, resp.JSON200)
	assert.Equal("partial_merge", resp.JSON200.Health,
		"failing tip with merged base and no open descendant is partial_merge, not blocked")
}

func TestAPIGetStackForPR_BaseBranchNotMain(t *testing.T) {
	runParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	srv, database := setupTestServer(t)
	client := setupTestClient(t, srv)

	// Base PR targets "master" not "main" — API must return real base_branch.
	seedStackedPR(t, database, "acme", "widget", 10, "feat/base", "master", db.MergeRequestStateOpen, "success", "APPROVED")
	seedStackedPR(t, database, "acme", "widget", 11, "feat/tip", "feat/base", db.MergeRequestStateOpen, "pending", "")
	runStackDetection(t, database, "acme", "widget")

	resp, err := client.HTTP.GetPullStackWithResponse(t.Context(), &generated.GetPullStackRequestOptions{PathParams: &generated.GetPullStackPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(10)}})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)
	require.NotNil(resp.JSON200)
	require.NotNil(resp.JSON200.Members)
	assert.Len(resp.JSON200.Members, 2)
	assert.Equal("master", resp.JSON200.Members[0].BaseBranch)
	assert.Equal("feat/base", resp.JSON200.Members[1].BaseBranch)
}

func TestCICheckDedupLatestRunWinsE2E(t *testing.T) {
	runParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)

	older := time.Date(2026, 4, 9, 12, 0, 0, 0, time.UTC)
	newer := older.Add(10 * time.Minute)
	prID := int64(1001)
	prNumber := 1
	prTitle := "check dedupe"
	prState := "open"
	prURL := "https://github.com/acme/widget/pull/1"
	prBody := ""
	prAuthor := "alice"
	headRef := "feature/check-dedupe"
	baseRef := "main"
	headSHA := "abc123"
	baseSHA := "def456"
	headCloneURL := "https://github.com/acme/widget.git"
	checkName := "build"
	checkStatus := "completed"
	oldConclusion := "failure"
	newConclusion := "success"
	oldCheckURL := "https://github.com/acme/widget/actions/runs/1"
	newCheckURL := "https://github.com/acme/widget/actions/runs/2"
	combinedTotal := 1
	combinedState := "success"

	pr := &gh.PullRequest{
		ID:        &prID,
		Number:    &prNumber,
		Title:     &prTitle,
		State:     &prState,
		HTMLURL:   &prURL,
		Body:      &prBody,
		User:      &gh.User{Login: &prAuthor},
		CreatedAt: &gh.Timestamp{Time: older},
		UpdatedAt: &gh.Timestamp{Time: newer},
		Head: &gh.PullRequestBranch{
			Ref: &headRef,
			SHA: &headSHA,
			Repo: &gh.Repository{
				CloneURL: &headCloneURL,
			},
		},
		Base: &gh.PullRequestBranch{
			Ref: &baseRef,
			SHA: &baseSHA,
		},
	}

	mock := &mockGH{
		getPullRequestFn: func(
			_ context.Context, _, _ string, _ int,
		) (*gh.PullRequest, error) {
			return pr, nil
		},
		listCheckRunsForRefFn: func(
			_ context.Context, owner, repo, ref string,
		) ([]*gh.CheckRun, error) {
			require.Equal("acme", owner)
			require.Equal("widget", repo)
			require.Equal(headSHA, ref)
			return []*gh.CheckRun{
				{
					ID:          new(int64(10)),
					Name:        &checkName,
					Status:      &checkStatus,
					Conclusion:  &oldConclusion,
					CompletedAt: &gh.Timestamp{Time: older},
					HTMLURL:     &oldCheckURL,
				},
				{
					ID:          new(int64(11)),
					Name:        &checkName,
					Status:      &checkStatus,
					Conclusion:  &newConclusion,
					CompletedAt: &gh.Timestamp{Time: newer},
					HTMLURL:     &newCheckURL,
				},
			}, nil
		},
		getCombinedStatusFn: func(
			_ context.Context, owner, repo, ref string,
		) (*gh.CombinedStatus, error) {
			require.Equal("acme", owner)
			require.Equal("widget", repo)
			require.Equal(headSHA, ref)
			return &gh.CombinedStatus{
				TotalCount: &combinedTotal,
				State:      &combinedState,
			}, nil
		},
	}

	srv, database := setupTestServerWithMock(t, mock)
	client := setupTestClient(t, srv)
	seedPR(
		t, database, "acme", "widget", prNumber,
		withSeedPRHeadSHA(headSHA),
		withSeedPRTimes(older, older, older),
	)

	resp, err := client.HTTP.SyncPullWithResponse(context.Background(), &generated.SyncPullRequestOptions{PathParams: &generated.SyncPullPath{Provider: "gh", Owner: "acme", Name: "widget", Number: int64(int64(prNumber))}})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)
	require.NotNil(resp.JSON200)
	require.NotNil(resp.JSON200.MergeRequest)
	require.Equal("success", resp.JSON200.MergeRequest.CIStatus)

	var checks []db.CICheck
	require.NoError(json.Unmarshal(
		[]byte(resp.JSON200.MergeRequest.CIChecksJSON),
		&checks,
	))
	require.Len(checks, 1)
	assert.Equal("build", checks[0].Name)
	assert.Equal("completed", checks[0].Status)
	assert.Equal("success", checks[0].Conclusion)
	assert.Equal(newCheckURL, checks[0].URL)
}

func cleanupServerTestTmux(owner *testtmux.Owner) error {
	if owner == nil {
		return nil
	}
	return owner.Cleanup()
}

func gitLocalRemoteURL(path string) string {
	if runtime.GOOS != "windows" {
		return path
	}
	slashPath := filepath.ToSlash(path)
	if !strings.HasPrefix(slashPath, "/") {
		slashPath = "/" + slashPath
	}
	return (&url.URL{Scheme: "file", Path: slashPath}).String()
}

func gitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, stderr, err := gitcmd.New().Run(t.Context(), dir, nil, args...)
	require.NoError(t, err, "git %v failed: %s%s", args, out, stderr)
	return strings.TrimSpace(string(out))
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

func TestAPIEditPRTitleAndBody(t *testing.T) {
	require := require.New(t)
	srv, database := setupTestServer(t)
	seedPR(t, database, "acme", "widget", 1)

	rr := testutil.DoJSON(t, srv, http.MethodPatch,
		"/api/v1/pulls/gh/acme/widget/1",
		map[string]string{"title": "updated title", "body": "updated body"})

	require.Equal(http.StatusOK, rr.Code)

	mr, err := database.GetMergeRequest(
		t.Context(), "github", "github.com", "acme", "widget", 1,
	)
	require.NoError(err)
	require.Equal("updated title", mr.Title)
	require.Equal("updated body", mr.Body)
}

func TestAPIEditPRTitleOnly(t *testing.T) {
	require := require.New(t)
	srv, database := setupTestServer(t)
	seedPR(t, database, "acme", "widget", 1)

	rr := testutil.DoJSON(t, srv, http.MethodPatch,
		"/api/v1/pulls/gh/acme/widget/1",
		map[string]string{"title": "new title"})

	require.Equal(http.StatusOK, rr.Code)

	mr, err := database.GetMergeRequest(
		t.Context(), "github", "github.com", "acme", "widget", 1,
	)
	require.NoError(err)
	require.Equal("new title", mr.Title)
	require.Equal("test body", mr.Body)
}

func TestAPIEditPRBodyOnly(t *testing.T) {
	require := require.New(t)
	srv, database := setupTestServer(t)
	seedPR(t, database, "acme", "widget", 1)

	rr := testutil.DoJSON(t, srv, http.MethodPatch,
		"/api/v1/pulls/gh/acme/widget/1",
		map[string]string{"body": "new body"})

	require.Equal(http.StatusOK, rr.Code)

	mr, err := database.GetMergeRequest(
		t.Context(), "github", "github.com", "acme", "widget", 1,
	)
	require.NoError(err)
	require.Equal("Test PR #1", mr.Title)
	require.Equal("new body", mr.Body)
}

func TestAPIEditPRClearBody(t *testing.T) {
	require := require.New(t)
	srv, database := setupTestServer(t)
	seedPR(t, database, "acme", "widget", 1)

	rr := testutil.DoJSON(t, srv, http.MethodPatch,
		"/api/v1/pulls/gh/acme/widget/1",
		map[string]string{"body": ""})

	require.Equal(http.StatusOK, rr.Code)

	mr, err := database.GetMergeRequest(
		t.Context(), "github", "github.com", "acme", "widget", 1,
	)
	require.NoError(err)
	require.Equal("Test PR #1", mr.Title)
	require.Empty(mr.Body)
}

func TestAPIEditPRNoFields400(t *testing.T) {
	require := require.New(t)
	srv, database := setupTestServer(t)
	seedPR(t, database, "acme", "widget", 1)

	rr := testutil.DoJSON(t, srv, http.MethodPatch,
		"/api/v1/pulls/gh/acme/widget/1",
		map[string]any{})

	require.Equal(http.StatusBadRequest, rr.Code)
}

func TestAPIEditPRBlankTitle400(t *testing.T) {
	require := require.New(t)
	srv, database := setupTestServer(t)
	seedPR(t, database, "acme", "widget", 1)

	rr := testutil.DoJSON(t, srv, http.MethodPatch,
		"/api/v1/pulls/gh/acme/widget/1",
		map[string]string{"title": "   "})

	require.Equal(http.StatusBadRequest, rr.Code)
}

func TestAPIEditPRPreservesDerivedFields(t *testing.T) {
	require := require.New(t)
	srv, database := setupTestServer(t)
	seedPR(t, database, "acme", "widget", 1)

	ctx := t.Context()

	// Seed non-default derived fields so we can detect clobbering.
	repo, err := database.GetRepoByIdentity(ctx, verifiedGitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	require.NoError(database.UpdateMRDerivedFields(ctx, repo.ID, 1, db.MRDerivedFields{
		ReviewDecision: "APPROVED",
		CommentCount:   7,
	}))
	require.NoError(database.UpdateMRCIStatus(ctx, repo.ID, 1, "success", "[]"))

	rr := testutil.DoJSON(t, srv, http.MethodPatch,
		"/api/v1/pulls/gh/acme/widget/1",
		map[string]string{"title": "changed title"})

	require.Equal(http.StatusOK, rr.Code)

	after, err := database.GetMergeRequest(ctx, "github", "github.com", "acme", "widget", 1)
	require.NoError(err)
	require.Equal("changed title", after.Title)
	require.Equal(7, after.CommentCount)
	require.Equal("success", after.CIStatus)
	require.Equal("APPROVED", after.ReviewDecision)
	require.Equal(db.MergeRequestStateOpen, after.State)
}
