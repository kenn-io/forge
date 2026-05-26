package server

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/creack/pty/v2"
	gh "github.com/google/go-github/v84/github"
	"github.com/shurcooL/githubv4"
	Assert "github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/middleman/internal/apiclient"
	"go.kenn.io/middleman/internal/apiclient/generated"
	"go.kenn.io/middleman/internal/config"
	"go.kenn.io/middleman/internal/db"
	"go.kenn.io/middleman/internal/gitclone"
	"go.kenn.io/middleman/internal/gitenv"
	ghclient "go.kenn.io/middleman/internal/github"
	"go.kenn.io/middleman/internal/platform"
	forgejoplatform "go.kenn.io/middleman/internal/platform/forgejo"
	giteaplatform "go.kenn.io/middleman/internal/platform/gitea"
	"go.kenn.io/middleman/internal/platform/gitealike"
	"go.kenn.io/middleman/internal/procutil"
	"go.kenn.io/middleman/internal/ptyowner"
	"go.kenn.io/middleman/internal/stacks"
	"go.kenn.io/middleman/internal/testutil"
	"go.kenn.io/middleman/internal/testutil/dbtest"
	"go.kenn.io/middleman/internal/workspace"
	"go.kenn.io/middleman/internal/workspace/localruntime"
	"golang.org/x/sync/semaphore"
)

const serverRuntimeHelperMarker = "middleman-runtime-helper"

var ptyE2ESemaphore = semaphore.NewWeighted(4)

func runParallelPTYE2E(t *testing.T) {
	t.Helper()
	t.Parallel()
	releasePTYSlot := acquirePTYE2ESlot(t)
	t.Cleanup(releasePTYSlot)
}

func acquirePTYE2ESlot(t *testing.T) func() {
	t.Helper()
	require.NoError(t, ptyE2ESemaphore.Acquire(t.Context(), 1))
	return func() {
		ptyE2ESemaphore.Release(1)
	}
}

func requirePTYAvailable(t *testing.T) {
	t.Helper()
	ptmx, tty, err := pty.Open()
	if err != nil {
		t.Skipf("pty unavailable in this test environment: %v", err)
	}
	_ = ptmx.Close()
	_ = tty.Close()
}

func cleanupContext(t *testing.T) (context.Context, context.CancelFunc) {
	t.Helper()
	return context.WithTimeout(context.Background(), 5*time.Second)
}

func gracefulShutdown(t *testing.T, srv interface{ Shutdown(context.Context) error }) {
	t.Helper()
	ctx, cancel := cleanupContext(t)
	defer cancel()
	require.NoError(t, srv.Shutdown(ctx))
}

// mockGH implements ghclient.Client for testing.
type mockGH struct {
	getRepositoryFn           func(context.Context, string, string) (*gh.Repository, error)
	getPullRequestFn          func(context.Context, string, string, int) (*gh.PullRequest, error)
	getPullRequestIfChangedFn func(context.Context, string, string, int, string) (*gh.PullRequest, string, bool, error)
	getIssueFn                func(context.Context, string, string, int) (*gh.Issue, error)
	getIssueIfChangedFn       func(context.Context, string, string, int, string) (*gh.Issue, string, bool, error)
	createIssueFn             func(context.Context, string, string, string, string) (*gh.Issue, error)
	getUserFn                 func(context.Context, string) (*gh.User, error)
	markReadyForReviewFn      func(context.Context, string, string, int) (*gh.PullRequest, error)
	editPullRequestFn         func(context.Context, string, string, int, ghclient.EditPullRequestOpts) (*gh.PullRequest, error)
	editIssueFn               func(context.Context, string, string, int, string) (*gh.Issue, error)
	editIssueContentFn        func(context.Context, string, string, int, *string, *string) (*gh.Issue, error)
	createIssueCommentFn      func(context.Context, string, string, int, string) (*gh.IssueComment, error)
	editIssueCommentFn        func(context.Context, string, string, int64, string) (*gh.IssueComment, error)
	createReviewFn            func(context.Context, string, string, int, string, string) (*gh.PullRequestReview, error)
	mergePullRequestFn        func(context.Context, string, string, int, string, string, string) (*gh.PullRequestMergeResult, error)
	listWorkflowRunsForHeadFn func(context.Context, string, string, string) ([]*gh.WorkflowRun, error)
	approveWorkflowRunFn      func(context.Context, string, string, int64) error
	listReposByOwnerFn        func(context.Context, string) ([]*gh.Repository, error)
	listReleasesFn            func(context.Context, string, string, int) ([]*gh.RepositoryRelease, error)
	listTagsFn                func(context.Context, string, string, int) ([]*gh.RepositoryTag, error)
	listOpenPullRequestsFn    func(context.Context, string, string) ([]*gh.PullRequest, error)
	listPullRequestsPageFn    func(context.Context, string, string, string, int) ([]*gh.PullRequest, bool, error)
	listIssuesPageFn          func(context.Context, string, string, string, int) ([]*gh.Issue, bool, error)
	listCheckRunsForRefFn     func(context.Context, string, string, string) ([]*gh.CheckRun, error)
	getCombinedStatusFn       func(context.Context, string, string, string) (*gh.CombinedStatus, error)
	listOpenPRsErr            error
	listOpenIssuesFn          func(context.Context, string, string) ([]*gh.Issue, error)
	listIssueCommentsFn       func(context.Context, string, string, int) ([]*gh.IssueComment, error)
	listIssueCommentsErr      error
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

func (m *mockGH) ListCommits(
	_ context.Context, _, _ string, _ int,
) ([]*gh.RepositoryCommit, error) {
	return nil, nil
}

func (m *mockGH) ListForcePushEvents(
	_ context.Context, _, _ string, _ int,
) ([]ghclient.ForcePushEvent, error) {
	return nil, nil
}

func (m *mockGH) ListPullRequestTimelineEvents(
	_ context.Context, _, _ string, _ int,
) ([]ghclient.PullRequestTimelineEvent, error) {
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

func (m *mockGH) MarkPullRequestReadyForReview(
	ctx context.Context, owner, repo string, number int,
) (*gh.PullRequest, error) {
	if m.markReadyForReviewFn != nil {
		return m.markReadyForReviewFn(ctx, owner, repo, number)
	}
	draft := false
	return &gh.PullRequest{Number: &number, Draft: &draft}, nil
}

func (m *mockGH) MergePullRequest(
	ctx context.Context, owner, repo string, number int,
	commitTitle, commitMessage, method string,
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
	ctx context.Context, owner, repo string, number int, opts ghclient.EditPullRequestOpts,
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

// InvalidateListETagsForRepo is a no-op for the server test mock,
// which has no underlying HTTP cache.
func (m *mockGH) InvalidateListETagsForRepo(_, _ string, _ ...string) {}

type apiTestGitLabProvider struct {
	ref                platform.RepoRef
	mergeRequests      []platform.MergeRequest
	mergeRequestEvents map[int][]platform.MergeRequestEvent
	issues             []platform.Issue
	issueEvents        map[int][]platform.IssueEvent
	releases           []platform.Release
	tags               []platform.Tag
	ciChecks           map[string][]platform.CICheck
	ciErr              error
}

func (p *apiTestGitLabProvider) Platform() platform.Kind {
	return platform.KindGitLab
}

func (p *apiTestGitLabProvider) Host() string {
	return p.ref.Host
}

func (p *apiTestGitLabProvider) Capabilities() platform.Capabilities {
	return platform.Capabilities{
		ReadRepositories:  true,
		ReadMergeRequests: true,
		ReadIssues:        true,
		ReadComments:      true,
		ReadReleases:      true,
		ReadCI:            true,
	}
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
	return p.mergeRequests, nil
}

func (p *apiTestGitLabProvider) GetMergeRequest(
	_ context.Context,
	_ platform.RepoRef,
	number int,
) (platform.MergeRequest, error) {
	for _, mr := range p.mergeRequests {
		if mr.Number == number {
			return mr, nil
		}
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
	return p.issues, nil
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
func setupTestServer(t *testing.T) (*Server, *db.DB) {
	t.Helper()
	return setupTestServerWithMock(t, &mockGH{})
}

func setupTestServerWithDatabase(
	t *testing.T, database *db.DB, repos []ghclient.RepoRef,
) *Server {
	t.Helper()

	syncer := ghclient.NewSyncer(map[string]ghclient.Client{"github.com": &mockGH{}}, database, nil, repos, time.Minute, nil, nil)
	t.Cleanup(syncer.Stop)
	srv := New(
		database, syncer, nil, "/",
		nil, ServerOptions{},
	)
	t.Cleanup(func() { gracefulShutdown(t, srv) })
	return srv
}

func setupTestServerWithMock(t *testing.T, mock *mockGH) (*Server, *db.DB) {
	t.Helper()
	return setupTestServerWithRepos(t, mock, defaultTestRepos)
}

var defaultTestRepos = []ghclient.RepoRef{
	{Owner: "acme", Name: "widget", PlatformHost: "github.com"},
}

func setupTestServerWithRepos(
	t *testing.T, mock *mockGH, repos []ghclient.RepoRef,
) (*Server, *db.DB) {
	t.Helper()

	database := dbtest.Open(t)

	syncer := ghclient.NewSyncer(map[string]ghclient.Client{"github.com": mock}, database, nil, repos, time.Minute, nil, nil)
	// Drain any TriggerRun goroutines (fired by handlers like
	// POST /sync) before tests tear down. Registered after the DB
	// cleanup so LIFO ordering runs Stop first: without this, a
	// leaked goroutine from one test's handler can outlive its DB.
	t.Cleanup(syncer.Stop)
	srv := New(
		database, syncer, nil, "/",
		nil, ServerOptions{},
	)
	// Registered after the DB cleanup so LIFO ordering runs Shutdown
	// first and lets background goroutines finish before DB close.
	t.Cleanup(func() { gracefulShutdown(t, srv) })
	return srv, database
}

func setupTestClient(t *testing.T, srv *Server) *apiclient.Client {
	t.Helper()
	return setupTestClientWithBaseURL(t, srv, "http://middleman.test")
}

func setupTestClientWithBaseURL(
	t *testing.T,
	srv *Server,
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

			serverReq := httptest.NewRequest(req.Method, req.URL.String(), body)
			serverReq.Header = req.Header.Clone()
			// Ensure mutation requests have Content-Type for CSRF.
			if req.Method != http.MethodGet && serverReq.Header.Get("Content-Type") == "" {
				serverReq.Header.Set("Content-Type", "application/json")
			}
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
	Assert.Equal(t, want.UTC(), parsed.UTC())
	Assert.True(t, strings.HasSuffix(got, "Z"), "expected UTC RFC3339 with trailing Z: %s", got)
}

func setTestServerNow(t *testing.T, srv *Server, now time.Time) {
	t.Helper()
	srv.now = func() time.Time { return now }
}

func testEDTTime(hour, minute int) time.Time {
	//nolint:forbidigo // Test fixture intentionally uses a non-UTC timestamp to verify UTC normalization.
	return time.Date(2026, 4, 11, hour, minute, 0, 0, time.FixedZone("EDT", -4*60*60))
}

func assertTimePtrUTC(t *testing.T, got *time.Time) {
	t.Helper()
	require.NotNil(t, got)
	Assert.Equal(t, time.UTC, got.Location())
}

func assertTimePtrEqualsUTC(t *testing.T, got *time.Time, want time.Time) {
	t.Helper()
	assertTimePtrUTC(t, got)
	Assert.Equal(t, want.UTC(), got.UTC())
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

type staleReadyForReviewError struct{ err error }

func (e *staleReadyForReviewError) Error() string      { return e.err.Error() }
func (e *staleReadyForReviewError) Unwrap() error      { return e.err }
func (e *staleReadyForReviewError) StatusCode() int    { return http.StatusNotFound }
func (e *staleReadyForReviewError) IsStaleState() bool { return true }

type seedPROpt func(*db.MergeRequest)

func withSeedPRHeadSHA(headSHA string) seedPROpt {
	return func(pr *db.MergeRequest) { pr.PlatformHeadSHA = headSHA }
}

func withSeedPRBaseSHA(baseSHA string) seedPROpt {
	return func(pr *db.MergeRequest) { pr.PlatformBaseSHA = baseSHA }
}

func withSeedPRHeadRepoCloneURL(cloneURL string) seedPROpt {
	return func(pr *db.MergeRequest) { pr.HeadRepoCloneURL = cloneURL }
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

	repoID, err := database.UpsertRepo(ctx, db.GitHubRepoIdentity("github.com", owner, name))
	require.NoError(t, err)

	numberText := strconv.Itoa(number)
	now := time.Now().UTC().Truncate(time.Second)
	pr := &db.MergeRequest{
		RepoID:         repoID,
		PlatformID:     int64(number) * 1000,
		Number:         number,
		URL:            "https://github.com/" + owner + "/" + name + "/pull/" + numberText,
		Title:          "Test PR #" + numberText,
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

func TestAPIMergePR405ReturnsGitHubMessage(t *testing.T) {
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
	seedPR(t, database, "acme", "widget", 1)
	client := setupTestClient(t, srv)

	resp, err := client.HTTP.MergePullWithResponse(
		t.Context(), "gh", "acme", "widget", 1,
		generated.MergePRInputBody{
			CommitTitle:   "title",
			CommitMessage: "msg",
			Method:        "squash",
		},
	)
	require.NoError(err)
	require.Equal(http.StatusConflict, resp.StatusCode())
	require.Contains(string(resp.Body), "Pull Request is not mergeable")
}

func TestAPIMergePR409ReturnsGitHubMessage(t *testing.T) {
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
	seedPR(t, database, "acme", "widget", 1)
	client := setupTestClient(t, srv)

	resp, err := client.HTTP.MergePullWithResponse(
		t.Context(), "gh", "acme", "widget", 1,
		generated.MergePRInputBody{
			CommitTitle:   "title",
			CommitMessage: "msg",
			Method:        "squash",
		},
	)
	require.NoError(err)
	require.Equal(http.StatusConflict, resp.StatusCode())
	require.Contains(string(resp.Body), "Head branch was modified")
}

func TestAPIMergePRNetworkErrorReturns502(t *testing.T) {
	require := require.New(t)

	mock := &mockGH{
		mergePullRequestFn: func(_ context.Context, _, _ string, _ int, _, _, _ string) (*gh.PullRequestMergeResult, error) {
			return nil, fmt.Errorf("connection refused")
		},
	}

	srv, database := setupTestServerWithMock(t, mock)
	seedPR(t, database, "acme", "widget", 1)
	client := setupTestClient(t, srv)

	resp, err := client.HTTP.MergePullWithResponse(
		t.Context(), "gh", "acme", "widget", 1,
		generated.MergePRInputBody{
			CommitTitle:   "title",
			CommitMessage: "msg",
			Method:        "squash",
		},
	)
	require.NoError(err)
	require.Equal(http.StatusBadGateway, resp.StatusCode())
	require.Contains(string(resp.Body), "connection refused")
}

func TestAPIMergePR422ForwardsGitHubMessage(t *testing.T) {
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
	seedPR(t, database, "acme", "widget", 1)
	client := setupTestClient(t, srv)

	resp, err := client.HTTP.MergePullWithResponse(
		t.Context(), "gh", "acme", "widget", 1,
		generated.MergePRInputBody{
			CommitTitle:   "title",
			CommitMessage: "msg",
			Method:        "squash",
		},
	)
	require.NoError(err)
	require.Equal(http.StatusUnprocessableEntity, resp.StatusCode())
	require.Contains(string(resp.Body), "Required status check is failing")
}

func TestAPIMergePR403ForwardsGitHubMessage(t *testing.T) {
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
	seedPR(t, database, "acme", "widget", 1)
	client := setupTestClient(t, srv)

	resp, err := client.HTTP.MergePullWithResponse(
		t.Context(), "gh", "acme", "widget", 1,
		generated.MergePRInputBody{
			CommitTitle:   "title",
			CommitMessage: "msg",
			Method:        "squash",
		},
	)
	require.NoError(err)
	require.Equal(http.StatusForbidden, resp.StatusCode())
	require.Contains(string(resp.Body), "Resource not accessible by integration")
}

func TestAPIMergePR5xxReturns502WithGitHubMessage(t *testing.T) {
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
	seedPR(t, database, "acme", "widget", 1)
	client := setupTestClient(t, srv)

	resp, err := client.HTTP.MergePullWithResponse(
		t.Context(), "gh", "acme", "widget", 1,
		generated.MergePRInputBody{
			CommitTitle:   "title",
			CommitMessage: "msg",
			Method:        "squash",
		},
	)
	require.NoError(err)
	require.Equal(http.StatusBadGateway, resp.StatusCode())
	require.Contains(string(resp.Body), "Service unavailable")
}

func TestAPIMergePRForwardsGitHubErrorDetailsAndLogsError(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)

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
	seedPR(t, database, "acme", "widget", 1)
	client := setupTestClient(t, srv)

	resp, err := client.HTTP.MergePullWithResponse(
		t.Context(), "gh", "acme", "widget", 1,
		generated.MergePRInputBody{
			CommitTitle:   "title",
			CommitMessage: "msg",
			Method:        "squash",
		},
	)
	require.NoError(err)
	require.Equal(http.StatusBadGateway, resp.StatusCode())

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
	require := require.New(t)

	srv, database := setupTestServer(t)
	handlerNow := testEDTTime(8, 30)
	setTestServerNow(t, srv, handlerNow)
	seedPR(t, database, "acme", "widget", 1)
	client := setupTestClient(t, srv)

	resp, err := client.HTTP.MergePullWithResponse(
		t.Context(), "gh", "acme", "widget", 1,
		generated.MergePRInputBody{
			CommitTitle:   "title",
			CommitMessage: "msg",
			Method:        "squash",
		},
	)
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode())

	pr, err := database.GetMergeRequest(t.Context(), "acme", "widget", 1)
	require.NoError(err)
	require.Equal(db.MergeRequestStateMerged, pr.State)
	assertTimePtrEqualsUTC(t, pr.MergedAt, handlerNow)
	assertTimePtrEqualsUTC(t, pr.ClosedAt, handlerNow)
}

func TestAPIClientConstruction(t *testing.T) {
	srv, _ := setupTestServer(t)
	client := setupTestClient(t, srv)
	require.NotNil(t, client)
	require.NotNil(t, client.HTTP)
}

func TestAPIListPulls(t *testing.T) {
	require := require.New(t)
	srv, database := setupTestServer(t)
	seedPR(t, database, "acme", "widget", 1)
	client := setupTestClient(t, srv)

	resp, err := client.HTTP.ListPullsWithResponse(t.Context(), nil)
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode())
	require.NotNil(resp.JSON200)
	require.Len(*resp.JSON200, 1)
	assert := Assert.New(t)
	assert.Equal("acme", (*resp.JSON200)[0].RepoOwner)
	assert.Equal("widget", (*resp.JSON200)[0].RepoName)
	assert.Equal("github.com", (*resp.JSON200)[0].PlatformHost)

	raw := doJSON(t, srv, http.MethodGet, "/api/v1/pulls", nil)
	require.Equal(http.StatusOK, raw.Code)
	var body []struct {
		Repo repoRefResponse `json:"repo"`
	}
	require.NoError(json.Unmarshal(raw.Body.Bytes(), &body))
	require.Len(body, 1)
	assert.Equal("github", body[0].Repo.Provider)
	assert.Equal("github.com", body[0].Repo.PlatformHost)
	assert.Equal("acme/widget", body[0].Repo.RepoPath)
}

func TestAPIListPullsKeepsCachedCIDecorationsAfterIndexSync(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	now := time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC)
	headSHA := "same-head"
	baseSHA := "base-sha"
	checksJSON := `[{"name":"build","status":"completed","conclusion":"failure"}]`

	str := func(v string) *string { return &v }
	mock := &mockGH{
		listOpenPullRequestsFn: func(_ context.Context, _, _ string) ([]*gh.PullRequest, error) {
			number := 1
			id := int64(1001)
			return []*gh.PullRequest{{
				ID:        &id,
				Number:    &number,
				Title:     str("Cached CI PR"),
				State:     str("open"),
				HTMLURL:   str("https://github.com/acme/widget/pull/1"),
				User:      &gh.User{Login: str("octocat")},
				CreatedAt: &gh.Timestamp{Time: now},
				UpdatedAt: &gh.Timestamp{Time: now},
				Head:      &gh.PullRequestBranch{Ref: str("feature"), SHA: &headSHA},
				Base:      &gh.PullRequestBranch{Ref: str("main"), SHA: &baseSHA},
			}}, nil
		},
	}
	srv, database := setupTestServerWithMock(t, mock)
	seedPR(t, database, "acme", "widget", 1,
		withSeedPRTitle("Cached CI PR"),
		withSeedPRHeadSHA(headSHA),
		withSeedPRBaseSHA(baseSHA),
		withSeedPRCI("failure", checksJSON),
		withSeedPRTimes(now, now, now),
	)
	client := setupTestClient(t, srv)

	srv.syncer.RunOnce(ctx)

	resp, err := client.HTTP.ListPullsWithResponse(ctx, nil)
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode())
	require.NotNil(resp.JSON200)
	require.Len(*resp.JSON200, 1)
	pull := (*resp.JSON200)[0]
	assert.Equal("failure", pull.CIStatus)
	assert.JSONEq(checksJSON, pull.CIChecksJSON)
}

func TestAPIRepoFilterAcceptsMultipleRepos(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)
	srv, database := setupTestServer(t)

	seedPROnHost(t, database, "github.com", "acme", "widget", 1)
	seedPROnHost(t, database, "github.com", "acme", "worker", 2)
	seedPROnHost(t, database, "github.com", "acme", "ignored", 3)
	seedIssueOnHost(t, database, "github.com", "acme", "widget", 11, "open", "widget issue")
	seedIssueOnHost(t, database, "github.com", "acme", "worker", 12, "open", "worker issue")
	seedIssueOnHost(t, database, "github.com", "acme", "ignored", 13, "open", "ignored issue")

	filter := url.QueryEscape("github.com/acme/widget,github.com/acme/worker")

	rawPulls := doJSON(t, srv, http.MethodGet, "/api/v1/pulls?repo="+filter, nil)
	require.Equal(http.StatusOK, rawPulls.Code)
	var pulls []mergeRequestResponse
	require.NoError(json.Unmarshal(rawPulls.Body.Bytes(), &pulls))
	require.Len(pulls, 2)
	assert.ElementsMatch([]string{"widget", "worker"}, []string{
		pulls[0].RepoName,
		pulls[1].RepoName,
	})

	rawIssues := doJSON(t, srv, http.MethodGet, "/api/v1/issues?repo="+filter, nil)
	require.Equal(http.StatusOK, rawIssues.Code)
	var issues []issueResponse
	require.NoError(json.Unmarshal(rawIssues.Body.Bytes(), &issues))
	require.Len(issues, 2)
	assert.ElementsMatch([]string{"widget", "worker"}, []string{
		issues[0].RepoName,
		issues[1].RepoName,
	})

	since := url.QueryEscape(time.Now().UTC().Add(-time.Hour).Format(time.RFC3339))
	rawActivity := doJSON(t, srv, http.MethodGet, "/api/v1/activity?since="+since+"&repo="+filter, nil)
	require.Equal(http.StatusOK, rawActivity.Code)
	var activity activityResponse
	require.NoError(json.Unmarshal(rawActivity.Body.Bytes(), &activity))
	require.NotEmpty(activity.Items)
	for _, item := range activity.Items {
		assert.Contains([]string{"widget", "worker"}, item.RepoName)
	}
}

func TestAPIGetPullIsDBOnly(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)
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

	resp, err := client.HTTP.GetPullWithResponse(
		t.Context(), "gh", "acme", "widget", 1,
	)
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode())
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

func TestAPIGetPullIncludesDeletedCommentTimelineEvent(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)
	srv, database := setupTestServer(t)
	mrID := seedPRWithHeadSHA(t, database, "acme", "widget", 1, "deadbeef")
	createdAt := time.Date(2024, 6, 1, 12, 18, 0, 0, time.UTC)
	require.NoError(database.UpsertMREvents(t.Context(), []db.MREvent{{
		MergeRequestID: mrID,
		EventType:      "comment_deleted",
		Author:         "maintainer",
		Summary:        "deleted a comment from reviewer",
		MetadataJSON:   `{"deleted_comment_author":"reviewer"}`,
		CreatedAt:      createdAt,
		DedupeKey:      "timeline-CDE_1",
	}}))
	client := setupTestClient(t, srv)

	resp, err := client.HTTP.GetPullWithResponse(
		t.Context(), "gh", "acme", "widget", 1,
	)
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode())
	require.NotNil(resp.JSON200)
	require.NotNil(resp.JSON200.Events)
	require.Len(*resp.JSON200.Events, 1)
	event := (*resp.JSON200.Events)[0]
	assert.Equal("comment_deleted", event.EventType)
	assert.Equal("maintainer", event.Author)
	assert.Equal("deleted a comment from reviewer", event.Summary)
	assert.JSONEq(`{"deleted_comment_author":"reviewer"}`, event.MetadataJSON)
	assert.Equal(createdAt, event.CreatedAt)
}

func TestAPISyncPRIncludesWorkflowApproval(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)
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

	resp, err := client.HTTP.SyncPullWithResponse(
		t.Context(), "gh", "acme", "widget", 1,
	)
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode())
	require.NotNil(resp.JSON200)
	// Sync response uses workflowCheckRuns mode: reads PR state
	// from DB (just synced) and fetches workflow runs live.
	require.NotNil(resp.JSON200.WorkflowApproval)
	assert.True(resp.JSON200.WorkflowApproval.Checked)
	assert.True(resp.JSON200.WorkflowApproval.Required)
	assert.Equal(int64(1), resp.JSON200.WorkflowApproval.Count)
}

func TestAPISyncPRPersistsMergeableState(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)
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

	resp, err := client.HTTP.SyncPullWithResponse(
		t.Context(), "gh", "acme", "widget", 1,
	)
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode(), string(resp.Body))
	require.NotNil(resp.JSON200)
	assert.Equal("dirty", resp.JSON200.MergeRequest.MergeableState)

	stored, err := database.GetMergeRequest(t.Context(), "acme", "widget", 1)
	require.NoError(err)
	require.NotNil(stored)
	assert.Equal("dirty", stored.MergeableState)
}

func TestAPISyncPRPreservesMergeableStateWhenRefreshHasNoAnswer(t *testing.T) {
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
			assert := Assert.New(t)
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

			resp, err := client.HTTP.SyncPullWithResponse(
				t.Context(), "gh", "acme", "widget", 1,
			)
			require.NoError(err)
			require.Equal(http.StatusOK, resp.StatusCode(), string(resp.Body))
			require.NotNil(resp.JSON200)
			assert.Equal("dirty", resp.JSON200.MergeRequest.MergeableState)

			stored, err := database.GetMergeRequest(t.Context(), "acme", "widget", 1)
			require.NoError(err)
			require.NotNil(stored)
			assert.Equal("dirty", stored.MergeableState)
		})
	}
}

// TestAPIEnqueuePRSyncPersistsWorkflowApproval verifies that the
// background sync path (POST /sync/async) computes and persists
// workflow approval state so a subsequent DB-only GET sees it. The
// frontend's default detail-load flow uses this path, so without
// persistence the Approve Workflows button never appears.
func TestAPIEnqueuePRSyncPersistsWorkflowApproval(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)
	mock := &mockGH{
		getPullRequestFn: func(_ context.Context, _, _ string, number int) (*gh.PullRequest, error) {
			id := int64(2001)
			sha := "abc123"
			state := "open"
			title := "Async synced PR"
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

	resp, err := client.HTTP.EnqueuePrSyncWithResponse(
		t.Context(), "gh", "acme", "widget", 1,
	)
	require.NoError(err)
	require.Equal(http.StatusAccepted, resp.StatusCode())

	require.Eventually(func() bool {
		detail, dErr := client.HTTP.GetPullWithResponse(
			t.Context(), "gh", "acme", "widget", 1,
		)
		if dErr != nil || detail.JSON200 == nil {
			return false
		}
		wa := detail.JSON200.WorkflowApproval
		return wa.Checked && wa.Required && wa.Count == 1
	}, 3*time.Second, 25*time.Millisecond,
		"GET should return persisted workflow_approval after async sync")

	detail, err := client.HTTP.GetPullWithResponse(
		t.Context(), "gh", "acme", "widget", 1,
	)
	require.NoError(err)
	require.NotNil(detail.JSON200)
	assert.True(detail.JSON200.WorkflowApproval.Checked)
	assert.True(detail.JSON200.WorkflowApproval.Required)
	assert.Equal(int64(1), detail.JSON200.WorkflowApproval.Count)
}

// TestAPIGetPullClearsWorkflowApprovalWhenHeadMoves verifies that a
// persisted "required" approval from a prior head SHA does not bleed
// onto the new head. After a sync that moves the head forward (and
// finds no pending runs), GET must report checked=true, required=false.
func TestAPIGetPullClearsWorkflowApprovalWhenHeadMoves(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)

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
	syncResp, err := client.HTTP.SyncPullWithResponse(
		t.Context(), "gh", "acme", "widget", 1,
	)
	require.NoError(err)
	require.Equal(http.StatusOK, syncResp.StatusCode())
	require.True(syncResp.JSON200.WorkflowApproval.Required)

	// Head moves forward (force-push); new SHA has no action_required runs.
	headSHA.Store("def456")
	syncResp2, err := client.HTTP.SyncPullWithResponse(
		t.Context(), "gh", "acme", "widget", 1,
	)
	require.NoError(err)
	require.Equal(http.StatusOK, syncResp2.StatusCode())

	detail, err := client.HTTP.GetPullWithResponse(
		t.Context(), "gh", "acme", "widget", 1,
	)
	require.NoError(err)
	require.NotNil(detail.JSON200)
	wa := detail.JSON200.WorkflowApproval
	assert.True(wa.Checked, "head re-synced: approval state was rechecked")
	assert.False(wa.Required, "new head has no pending runs")
	assert.Equal(int64(0), wa.Count)
}

func TestAPIApproveWorkflows(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)
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
	repo, err := database.GetRepoByHostOwnerName(t.Context(), "github.com", "acme", "widget")
	require.NoError(err)
	require.NotNil(repo)
	require.NoError(database.UpdateMRWorkflowApproval(
		t.Context(), repo.ID, 1, time.Now().UTC(), "abc123", true, 2,
	))
	client := setupTestClient(t, srv)

	resp, err := client.HTTP.ApprovePullWorkflowsWithResponse(
		t.Context(), "gh", "acme", "widget", 1,
	)
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode())
	require.NotNil(resp.JSON200)
	require.NotNil(resp.JSON200.ApprovedCount)
	assert.Equal("approved_workflows", resp.JSON200.Status)
	assert.EqualValues(2, *resp.JSON200.ApprovedCount)
	assert.Equal([]int64{81, 82}, approvedRunIDs)

	pr, err := database.GetMergeRequest(t.Context(), "acme", "widget", 1)
	require.NoError(err)
	require.NotNil(pr)
	assert.Equal("abc123", pr.PlatformHeadSHA)
	require.NotNil(pr.WorkflowApprovalCheckedAt)
	assert.Equal("abc123", pr.WorkflowApprovalHeadSHA)
	assert.False(pr.WorkflowApprovalRequired)
	assert.Equal(0, pr.WorkflowApprovalCount)
}

func TestAPIApproveWorkflowsZeroMatchesStillSyncsPR(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)
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

	resp, err := client.HTTP.ApprovePullWorkflowsWithResponse(
		t.Context(), "gh", "acme", "widget", 1,
	)
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode())
	require.NotNil(resp.JSON200)
	assert.Equal("approved_workflows", resp.JSON200.Status)

	pr, err := database.GetMergeRequest(t.Context(), "acme", "widget", 1)
	require.NoError(err)
	require.NotNil(pr)
	assert.Equal("abc123", pr.PlatformHeadSHA)
}

func TestAPIApproveWorkflowsReturnsUnderlyingApprovalErrorAfterPartialFailure(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)
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

	resp, err := client.HTTP.ApprovePullWorkflowsWithResponse(
		t.Context(), "gh", "acme", "widget", 1,
	)
	require.NoError(err)
	require.Equal(http.StatusBadGateway, resp.StatusCode())
	require.NotNil(resp.ApplicationproblemJSONDefault)
	require.NotNil(resp.ApplicationproblemJSONDefault.Detail)
	assert.Contains(*resp.ApplicationproblemJSONDefault.Detail, "permission denied")
	assert.Equal([]int64{91, 92}, approvedRunIDs)

	pr, err := database.GetMergeRequest(t.Context(), "acme", "widget", 1)
	require.NoError(err)
	require.NotNil(pr)
	assert.Equal("abc123", pr.PlatformHeadSHA)
}

// TestAPISyncPRIncludesWorkflowApprovalForForkPR covers the regression where
// runs from fork-based PRs have an empty pull_requests array in GitHub's API.
// The sync path must still flag workflow approval as required, otherwise the
// UI never shows the approve button for the exact case it was built for.
func TestAPISyncPRIncludesWorkflowApprovalForForkPR(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)
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

	resp, err := client.HTTP.SyncPullWithResponse(
		t.Context(), "gh", "acme", "widget", 1,
	)
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode())
	require.NotNil(resp.JSON200)
	require.NotNil(resp.JSON200.WorkflowApproval)
	assert.True(resp.JSON200.WorkflowApproval.Checked)
	assert.True(resp.JSON200.WorkflowApproval.Required)
	assert.Equal(int64(1), resp.JSON200.WorkflowApproval.Count)
}

// TestAPIApproveWorkflowsForForkPR verifies the approve endpoint reaches
// ApproveWorkflowRun for a fork-triggered run when the run's head repo and
// branch match the PR.
func TestAPIApproveWorkflowsForForkPR(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)
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

	resp, err := client.HTTP.ApprovePullWorkflowsWithResponse(
		t.Context(), "gh", "acme", "widget", 1,
	)
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode())
	require.NotNil(resp.JSON200)
	require.NotNil(resp.JSON200.ApprovedCount)
	assert.Equal("approved_workflows", resp.JSON200.Status)
	assert.EqualValues(1, *resp.JSON200.ApprovedCount)
	assert.Equal([]int64{71}, approvedRunIDs)
}

// TestAPISyncPRIgnoresWorkflowRunsForOtherPRAtSameSHA covers the regression
// where two PRs share a head SHA and a populated pull_requests association
// points at the other PR. The sync path must not flag workflow approval as
// required for the wrong PR.
func TestAPISyncPRIgnoresWorkflowRunsForOtherPRAtSameSHA(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)
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

	resp, err := client.HTTP.SyncPullWithResponse(
		t.Context(), "gh", "acme", "widget", 1,
	)
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode())
	require.NotNil(resp.JSON200)
	require.NotNil(resp.JSON200.WorkflowApproval)
	assert.True(resp.JSON200.WorkflowApproval.Checked)
	assert.False(resp.JSON200.WorkflowApproval.Required)
	assert.Equal(int64(0), resp.JSON200.WorkflowApproval.Count)
}

// TestAPIApproveWorkflowsIgnoresRunsForOtherPRAtSameSHA verifies the approve
// endpoint does not call ApproveWorkflowRun for runs whose pull_requests
// association points at a different PR sharing the same head SHA.
func TestAPIApproveWorkflowsIgnoresRunsForOtherPRAtSameSHA(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)
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

	resp, err := client.HTTP.ApprovePullWorkflowsWithResponse(
		t.Context(), "gh", "acme", "widget", 1,
	)
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode())
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
	require := require.New(t)
	assert := Assert.New(t)
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

	resp, err := client.HTTP.ApprovePullWorkflowsWithResponse(
		t.Context(), "gh", "acme", "widget", 1,
	)
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode())
	require.NotNil(resp.JSON200)
	assert.Empty(approvedRunIDs)
}

func TestAPIGetPullNotFound(t *testing.T) {
	srv, _ := setupTestServer(t)
	client := setupTestClient(t, srv)

	resp, err := client.HTTP.GetPullWithResponse(
		t.Context(), "gh", "acme", "widget", 999,
	)
	require.NoError(t, err)
	require.Equal(t, http.StatusNotFound, resp.StatusCode())
	require.NotNil(t, resp.ApplicationproblemJSONDefault)
}

// TestAPIGetPullEmitsDiffWarningWhenSHAsMissing covers the case where a
// previous diff sync failed and left the PR row without diff SHAs. The
// resolveItem path treats DiffSyncError as success and the resolve
// response has no warnings field, so the only place a client can learn
// the diff is unavailable is the next getPull call. This regression
// test pins that behavior so the warning can't silently disappear.
func TestAPIGetPullEmitsDiffWarningWhenSHAsMissing(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)

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
	srv := New(database, syncer, nil, "/", nil, ServerOptions{})

	seedPR(t, database, "acme", "widget", 1)

	client := setupTestClient(t, srv)
	resp, err := client.HTTP.GetPullWithResponse(
		t.Context(), "gh", "acme", "widget", 1,
	)
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode())
	require.NotNil(resp.JSON200)
	require.NotNil(resp.JSON200.Warnings, "warnings field should be set when diff is missing")
	warnings := *resp.JSON200.Warnings
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
	require := require.New(t)
	assert := Assert.New(t)
	ctx := t.Context()

	database := dbtest.Open(t)

	clones := gitclone.New(t.TempDir(), nil)
	syncer := ghclient.NewSyncer(
		map[string]ghclient.Client{"github.com": &mockGH{}},
		database, clones, defaultTestRepos, time.Minute, nil, nil,
	)
	t.Cleanup(syncer.Stop)
	srv := New(database, syncer, nil, "/", nil, ServerOptions{})

	seedPR(t, database, "acme", "widget", 2)

	repoID, err := database.UpsertRepo(ctx, db.GitHubRepoIdentity("github.com", "acme", "widget"))
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
	resp, err := client.HTTP.GetPullWithResponse(
		t.Context(), "gh", "acme", "widget", 2,
	)
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode())
	require.NotNil(resp.JSON200)
	if resp.JSON200.Warnings != nil {
		assert.Empty(*resp.JSON200.Warnings)
	}
}

// TestAPIGetPullEmitsStaleDiffWarning covers the case where a diff sync
// populated the row but a later push advanced the platform head while
// the next diff sync failed. The recorded DiffHeadSHA is valid but no
// longer matches PlatformHeadSHA, so the UI would show a diff from the
// previous revision without any indication of drift. The warning must
// fire in that case.
func TestAPIGetPullEmitsStaleDiffWarning(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)
	ctx := t.Context()

	database := dbtest.Open(t)

	clones := gitclone.New(t.TempDir(), nil)
	syncer := ghclient.NewSyncer(
		map[string]ghclient.Client{"github.com": &mockGH{}},
		database, clones, defaultTestRepos, time.Minute, nil, nil,
	)
	t.Cleanup(syncer.Stop)
	srv := New(database, syncer, nil, "/", nil, ServerOptions{})

	seedPR(t, database, "acme", "widget", 3)

	repoID, err := database.UpsertRepo(ctx, db.GitHubRepoIdentity("github.com", "acme", "widget"))
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
	resp, err := client.HTTP.GetPullWithResponse(
		t.Context(), "gh", "acme", "widget", 3,
	)
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode())
	require.NotNil(resp.JSON200)
	require.NotNil(resp.JSON200.Warnings, "warnings field should be set when diff is stale")
	warnings := *resp.JSON200.Warnings
	require.Len(warnings, 1)
	assert.Contains(warnings[0], "out of date")
}

// TestAPIGetPullEmitsStaleDiffWarningOnBaseDrift covers the symmetric
// case to the head-drift test: the PR head is unchanged but the base
// branch advanced and the next diff sync failed. diffWarnings must
// mirror getDiff staleness logic, which treats base drift as stale
// for open PRs.
func TestAPIGetPullEmitsStaleDiffWarningOnBaseDrift(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)
	ctx := t.Context()

	database := dbtest.Open(t)

	clones := gitclone.New(t.TempDir(), nil)
	syncer := ghclient.NewSyncer(
		map[string]ghclient.Client{"github.com": &mockGH{}},
		database, clones, defaultTestRepos, time.Minute, nil, nil,
	)
	t.Cleanup(syncer.Stop)
	srv := New(database, syncer, nil, "/", nil, ServerOptions{})

	seedPR(t, database, "acme", "widget", 4)

	repoID, err := database.UpsertRepo(ctx, db.GitHubRepoIdentity("github.com", "acme", "widget"))
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
	resp, err := client.HTTP.GetPullWithResponse(
		t.Context(), "gh", "acme", "widget", 4,
	)
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode())
	require.NotNil(resp.JSON200)
	require.NotNil(resp.JSON200.Warnings, "warnings field should be set when base drifted")
	warnings := *resp.JSON200.Warnings
	require.Len(warnings, 1)
	assert.Contains(warnings[0], "out of date")
}

// TestAPIGetPullEmitsStaleDiffWarningOnMergedPR pins the staleness
// branch for merged PRs. getDiff treats merged PRs as stale when the
// recorded DiffHeadSHA no longer matches PlatformHeadSHA, so the
// warning must fire in the same case. Without this coverage a merged
// PR with a stale recorded diff would render outdated content with no
// indication.
func TestAPIGetPullEmitsStaleDiffWarningOnMergedPR(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)
	ctx := t.Context()

	database := dbtest.Open(t)

	clones := gitclone.New(t.TempDir(), nil)
	syncer := ghclient.NewSyncer(
		map[string]ghclient.Client{"github.com": &mockGH{}},
		database, clones, defaultTestRepos, time.Minute, nil, nil,
	)
	t.Cleanup(syncer.Stop)
	srv := New(database, syncer, nil, "/", nil, ServerOptions{})

	seedPR(t, database, "acme", "widget", 5)

	repoID, err := database.UpsertRepo(ctx, db.GitHubRepoIdentity("github.com", "acme", "widget"))
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
	resp, err := client.HTTP.GetPullWithResponse(
		t.Context(), "gh", "acme", "widget", 5,
	)
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode())
	require.NotNil(resp.JSON200)
	require.NotNil(resp.JSON200.Warnings, "warnings field should be set when merged diff is stale")
	warnings := *resp.JSON200.Warnings
	require.Len(warnings, 1)
	assert.Contains(warnings[0], "out of date")
}

// TestAPIGetPullEmitsDiffWarningWhenSHAsMissingClosed covers a closed
// (not merged) PR whose fetchAndUpdateClosed path failed to populate
// diff SHAs - for example because the clone fetch errored out. The
// previous diffWarnings implementation suppressed warnings for any
// non-open/non-merged state and the user would silently see no diff.
func TestAPIGetPullEmitsDiffWarningWhenSHAsMissingClosed(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)
	ctx := t.Context()

	database := dbtest.Open(t)

	clones := gitclone.New(t.TempDir(), nil)
	syncer := ghclient.NewSyncer(
		map[string]ghclient.Client{"github.com": &mockGH{}},
		database, clones, defaultTestRepos, time.Minute, nil, nil,
	)
	t.Cleanup(syncer.Stop)
	srv := New(database, syncer, nil, "/", nil, ServerOptions{})

	seedPR(t, database, "acme", "widget", 6)

	repoID, err := database.UpsertRepo(ctx, db.GitHubRepoIdentity("github.com", "acme", "widget"))
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
	resp, err := client.HTTP.GetPullWithResponse(
		t.Context(), "gh", "acme", "widget", 6,
	)
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode())
	require.NotNil(resp.JSON200)
	require.NotNil(resp.JSON200.Warnings, "warnings field should be set when closed PR diff is missing")
	warnings := *resp.JSON200.Warnings
	require.Len(warnings, 1)
	assert.Contains(warnings[0], "unavailable")
}

// TestAPIGetPullEmitsStaleDiffWarningOnClosedPR covers a closed (not
// merged) PR whose head or base advanced after the diff sync recorded
// SHAs. getDiff treats this as stale; diffWarnings must agree so the
// detail page shows a warning instead of silently rendering an old
// diff.
func TestAPIGetPullEmitsStaleDiffWarningOnClosedPR(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)
	ctx := t.Context()

	database := dbtest.Open(t)

	clones := gitclone.New(t.TempDir(), nil)
	syncer := ghclient.NewSyncer(
		map[string]ghclient.Client{"github.com": &mockGH{}},
		database, clones, defaultTestRepos, time.Minute, nil, nil,
	)
	t.Cleanup(syncer.Stop)
	srv := New(database, syncer, nil, "/", nil, ServerOptions{})

	seedPR(t, database, "acme", "widget", 7)

	repoID, err := database.UpsertRepo(ctx, db.GitHubRepoIdentity("github.com", "acme", "widget"))
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
	resp, err := client.HTTP.GetPullWithResponse(
		t.Context(), "gh", "acme", "widget", 7,
	)
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode())
	require.NotNil(resp.JSON200)
	require.NotNil(resp.JSON200.Warnings, "warnings field should be set when closed PR diff is stale")
	warnings := *resp.JSON200.Warnings
	require.Len(warnings, 1)
	assert.Contains(warnings[0], "out of date")
}

// TestAPIGetPullNoDiffWarningOnMergedPRWithBaseDrift pins the
// asymmetry between merged and open/closed staleness: merged PRs only
// care about head SHA drift because the base never advances after
// merge. A merged PR whose head matches but base differs must NOT
// emit a warning.
func TestAPIGetPullNoDiffWarningOnMergedPRWithBaseDrift(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)
	ctx := t.Context()

	database := dbtest.Open(t)

	clones := gitclone.New(t.TempDir(), nil)
	syncer := ghclient.NewSyncer(
		map[string]ghclient.Client{"github.com": &mockGH{}},
		database, clones, defaultTestRepos, time.Minute, nil, nil,
	)
	t.Cleanup(syncer.Stop)
	srv := New(database, syncer, nil, "/", nil, ServerOptions{})

	seedPR(t, database, "acme", "widget", 8)

	repoID, err := database.UpsertRepo(ctx, db.GitHubRepoIdentity("github.com", "acme", "widget"))
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
	resp, err := client.HTTP.GetPullWithResponse(
		t.Context(), "gh", "acme", "widget", 8,
	)
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode())
	require.NotNil(resp.JSON200)
	if resp.JSON200.Warnings != nil {
		assert.Empty(*resp.JSON200.Warnings)
	}
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
	require := require.New(t)
	assert := Assert.New(t)

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
	srv := New(database, syncer, nil, "/", nil, ServerOptions{})

	_, err := database.UpsertRepo(t.Context(), db.GitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	client := setupTestClient(t, srv)
	resp, err := client.HTTP.SyncPullWithResponse(
		t.Context(), "gh", "acme", "widget", int64(prNumber),
	)
	require.NoError(err)
	// Diff-sync failures are non-fatal: the handler must return 200
	// with the PR row and a warning, not a 502.
	require.Equal(http.StatusOK, resp.StatusCode())
	require.NotNil(resp.JSON200)
	require.NotNil(resp.JSON200.Warnings)
	warnings := *resp.JSON200.Warnings
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

func TestAPISetKanbanState(t *testing.T) {
	require := require.New(t)
	srv, database := setupTestServer(t)
	seedPR(t, database, "acme", "widget", 1)
	client := setupTestClient(t, srv)

	resp, err := client.HTTP.SetKanbanStateWithResponse(
		t.Context(), "gh",
		"acme",
		"widget",
		1,
		generated.SetKanbanStateJSONRequestBody{Status: "reviewing"},
	)
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode())

	pr, err := database.GetMergeRequest(t.Context(), "acme", "widget", 1)
	require.NoError(err)
	require.NotNil(pr)
	require.Equal(db.KanbanStatusReviewing, pr.KanbanStatus)
}

func TestAPISetKanbanStateRejectsInvalidStatus(t *testing.T) {
	srv, database := setupTestServer(t)
	seedPR(t, database, "acme", "widget", 1)
	client := setupTestClient(t, srv)

	resp, err := client.HTTP.SetKanbanStateWithResponse(
		t.Context(), "gh",
		"acme",
		"widget",
		1,
		generated.SetKanbanStateJSONRequestBody{Status: "nonsense"},
	)
	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, resp.StatusCode())
	require.NotNil(t, resp.ApplicationproblemJSONDefault)
}

func TestAPIListRepos(t *testing.T) {
	require := require.New(t)
	srv, database := setupTestServer(t)
	client := setupTestClient(t, srv)

	_, err := database.UpsertRepo(t.Context(), db.GitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)

	resp, err := client.HTTP.ListReposWithResponse(t.Context())
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode())
	require.NotNil(resp.JSON200)
	require.Len(*resp.JSON200, 1)
	require.Equal("acme", (*resp.JSON200)[0].Owner)
	require.Equal("widget", (*resp.JSON200)[0].Name)
}

func TestAPIGitLabConfiguredRepoSyncThroughProviderRegistry(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Second)

	database := dbtest.Open(t)

	ref := platform.RepoRef{
		Platform:           platform.KindGitLab,
		Host:               "gitlab.example.com",
		Owner:              "group/subgroup",
		Name:               "project",
		RepoPath:           "group/subgroup/project",
		PlatformID:         4242,
		PlatformExternalID: "gid://gitlab/Project/4242",
		WebURL:             "https://gitlab.example.com/group/subgroup/project",
		CloneURL:           "https://gitlab.example.com/group/subgroup/project.git",
		DefaultBranch:      "main",
	}
	provider := &apiTestGitLabProvider{
		ref: ref,
		mergeRequests: []platform.MergeRequest{{
			Repo:               ref,
			PlatformID:         7001,
			PlatformExternalID: "gid://gitlab/MergeRequest/7001",
			Number:             7,
			URL:                "https://gitlab.example.com/group/subgroup/project/-/merge_requests/7",
			Title:              "GitLab provider MR",
			Author:             "ada",
			State:              "open",
			HeadBranch:         "feature/gitlab",
			BaseBranch:         "main",
			HeadSHA:            "abc123",
			BaseSHA:            "def456",
			MergeableState:     "dirty",
			CreatedAt:          now,
			UpdatedAt:          now,
			LastActivityAt:     now,
		}},
	}
	registry, err := platform.NewRegistry(provider)
	require.NoError(err)

	cfg := &config.Config{
		BasePath: "/",
		Repos: []config.Repo{{
			Platform:     "gitlab",
			PlatformHost: "gitlab.example.com",
			Owner:        "group/subgroup",
			Name:         "project",
		}},
	}
	_, repos, err := ghclient.ResolveConfiguredRepoWithRegistry(
		ctx, registry, cfg.Repos[0],
	)
	require.NoError(err)
	require.Equal([]ghclient.RepoRef{{
		Platform:           platform.KindGitLab,
		Owner:              "group/subgroup",
		Name:               "project",
		PlatformHost:       "gitlab.example.com",
		RepoPath:           "group/subgroup/project",
		PlatformRepoID:     4242,
		PlatformExternalID: "gid://gitlab/Project/4242",
		WebURL:             "https://gitlab.example.com/group/subgroup/project",
		CloneURL:           "https://gitlab.example.com/group/subgroup/project.git",
		DefaultBranch:      "main",
	}}, repos)

	syncer := ghclient.NewSyncerWithRegistry(
		registry, database, nil, repos, time.Minute, nil, nil,
	)
	t.Cleanup(syncer.Stop)
	srv := NewWithConfig(
		database, syncer, nil, nil, cfg,
		filepath.Join(t.TempDir(), "config.toml"), ServerOptions{},
	)
	t.Cleanup(func() { gracefulShutdown(t, srv) })
	client := setupTestClient(t, srv)

	syncer.RunOnce(ctx)

	repoRow, err := database.GetRepoByIdentity(ctx, platform.DBRepoIdentity(ref))
	require.NoError(err)
	require.NotNil(repoRow)
	require.NotNil(repoRow.LastSyncCompletedAt)
	assert.Equal("gitlab", repoRow.Platform)
	assert.Equal("gitlab.example.com", repoRow.PlatformHost)

	reposResp, err := client.HTTP.ListReposWithResponse(ctx)
	require.NoError(err)
	require.Equal(http.StatusOK, reposResp.StatusCode())
	require.NotNil(reposResp.JSON200)
	require.Len(*reposResp.JSON200, 1)
	assert.Equal("gitlab", (*reposResp.JSON200)[0].Platform)
	assert.Equal("gitlab.example.com", (*reposResp.JSON200)[0].PlatformHost)
	assert.Equal("group/subgroup", (*reposResp.JSON200)[0].Owner)
	assert.Equal("project", (*reposResp.JSON200)[0].Name)

	pullsResp, err := client.HTTP.ListPullsWithResponse(
		ctx, &generated.ListPullsParams{},
	)
	require.NoError(err)
	require.Equal(http.StatusOK, pullsResp.StatusCode())
	require.NotNil(pullsResp.JSON200)
	require.Len(*pullsResp.JSON200, 1)
	assert.Equal("gitlab.example.com", (*pullsResp.JSON200)[0].PlatformHost)
	assert.Equal("group/subgroup", (*pullsResp.JSON200)[0].RepoOwner)
	assert.Equal("project", (*pullsResp.JSON200)[0].RepoName)
	assert.Equal("GitLab provider MR", (*pullsResp.JSON200)[0].Title)
	assert.Equal("dirty", (*pullsResp.JSON200)[0].MergeableState)
}

func TestGitLabSyncCoversRepositoryItemsEventsOverviewAndCI(t *testing.T) {
	assert := Assert.New(t)
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
	srv := New(database, syncer, nil, "/", nil, ServerOptions{})
	t.Cleanup(func() { gracefulShutdown(t, srv) })
	client := setupTestClient(t, srv)

	syncer.RunOnce(ctx)
	require.NoError(syncer.SyncMR(ctx, ref.Owner, ref.Name, 7))
	require.NoError(syncer.SyncIssue(ctx, ref.Owner, ref.Name, 11))

	repoRow, err := database.GetRepoByIdentity(ctx, platform.DBRepoIdentity(ref))
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
	pullResp, err := client.HTTP.GetPullOnHostWithResponse(
		ctx, providerHost, providerName, "Group/SubGroup", "Project.Special", mrNumber,
	)
	require.NoError(err)
	require.Equal(http.StatusOK, pullResp.StatusCode())
	require.NotNil(pullResp.JSON200)
	assert.Equal("gitlab", pullResp.JSON200.Repo.Provider)
	assert.Equal("gitlab.example.com:8443", pullResp.JSON200.Repo.PlatformHost)
	assert.Equal("Group/SubGroup/Project.Special", pullResp.JSON200.Repo.RepoPath)
	assert.Equal("success", pullResp.JSON200.MergeRequest.CIStatus)
	assert.Len(*pullResp.JSON200.Events, 1)

	issueNumber := int64(11)
	issueResp, err := client.HTTP.GetIssueOnHostWithResponse(
		ctx, providerHost, providerName, "Group/SubGroup", "Project.Special", issueNumber,
	)
	require.NoError(err)
	require.Equal(http.StatusOK, issueResp.StatusCode())
	require.NotNil(issueResp.JSON200)
	assert.Equal("gitlab", issueResp.JSON200.Repo.Provider)
	assert.Equal("gitlab.example.com:8443", issueResp.JSON200.Repo.PlatformHost)
	assert.Len(*issueResp.JSON200.Events, 1)

	summaryResp, err := client.HTTP.ListRepoSummariesWithResponse(ctx)
	require.NoError(err)
	require.Equal(http.StatusOK, summaryResp.StatusCode())
	require.NotNil(summaryResp.JSON200)
	require.Len(*summaryResp.JSON200, 1)
	summary := (*summaryResp.JSON200)[0]
	assert.Equal("gitlab", summary.Repo.Provider)
	assert.Equal("Group/SubGroup/Project.Special", summary.Repo.RepoPath)
	require.NotNil(summary.LatestRelease)
	assert.Equal("v1.2.0", summary.LatestRelease.TagName)
	assert.Equal(
		"https://gitlab.example.com:8443/Group/SubGroup/Project.Special/-/releases/v1.2.0",
		summary.LatestRelease.Url,
	)
}

func TestAPICIRefreshWarnsAndPreservesCIWhenProviderFails(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)
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
	repoID, err := database.UpsertRepo(ctx, platform.DBRepoIdentity(ref))
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
	srv := New(database, syncer, nil, "/", nil, ServerOptions{})
	t.Cleanup(func() { gracefulShutdown(t, srv) })
	client := setupTestClient(t, srv)

	resp, err := client.HTTP.RefreshPullCiOnHostWithResponse(
		ctx, ref.Host, "gitlab", ref.Owner, ref.Name, 7,
	)
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode(), string(resp.Body))
	require.NotNil(resp.JSON200)
	assert.Equal("pending", resp.JSON200.MergeRequest.CIStatus)
	assert.JSONEq(
		`[{"name":"pipeline","status":"in_progress","conclusion":""}]`,
		resp.JSON200.MergeRequest.CIChecksJSON,
	)
	require.NotNil(resp.JSON200.Warnings)
	require.Len(*resp.JSON200.Warnings, 1)
	assert.Contains((*resp.JSON200.Warnings)[0], "Could not refresh CI checks")

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
	require := require.New(t)
	assert := Assert.New(t)
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
	repoID, err := database.UpsertRepo(ctx, platform.DBRepoIdentity(ref))
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
			ghclient.RateBucketKey("gitlab", ref.Host): ghclient.NewSyncBudget(100),
		},
	)
	t.Cleanup(syncer.Stop)
	srv := New(database, syncer, nil, "/", nil, ServerOptions{})
	t.Cleanup(func() { gracefulShutdown(t, srv) })
	client := setupTestClient(t, srv)

	syncer.RunOnce(ctx)

	resp, err := client.HTTP.GetPullOnHostWithResponse(
		ctx, ref.Host, "gitlab", ref.Owner, ref.Name, 7,
	)
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode(), string(resp.Body))
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
	require := require.New(t)
	assert := Assert.New(t)
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
	repoID, err := database.UpsertRepo(ctx, platform.DBRepoIdentity(ref))
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
			ghclient.RateBucketKey("gitlab", ref.Host): ghclient.NewSyncBudget(100),
		},
	)
	t.Cleanup(syncer.Stop)
	srv := New(database, syncer, nil, "/", nil, ServerOptions{})
	t.Cleanup(func() { gracefulShutdown(t, srv) })
	client := setupTestClient(t, srv)

	syncer.RunOnce(ctx)

	resp, err := client.HTTP.GetPullOnHostWithResponse(
		ctx, ref.Host, "gitlab", ref.Owner, ref.Name, 8,
	)
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode(), string(resp.Body))
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

func TestProviderRefSyncEndpointsUseGitLabNestedRepoPath(t *testing.T) {
	assert := Assert.New(t)
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
	_, err = database.UpsertRepo(ctx, platform.DBRepoIdentity(ref))
	require.NoError(err)
	syncer := ghclient.NewSyncerWithRegistry(
		registry, database, nil, []ghclient.RepoRef{repo}, time.Minute, nil, nil,
	)
	t.Cleanup(syncer.Stop)
	srv := New(database, syncer, nil, "/", nil, ServerOptions{})
	t.Cleanup(func() { gracefulShutdown(t, srv) })
	client := setupTestClient(t, srv)

	providerName := "gitlab"
	providerHost := "gitlab.example.com:8443"
	repoPath := "Group/SubGroup/Project.Special"
	mrDirect := int64(7)
	mrAsync := int64(8)
	issueDirect := int64(11)
	issueAsync := int64(12)

	prResp, err := client.HTTP.SyncPullOnHostWithResponse(
		ctx, providerHost, providerName, "Group/SubGroup", "Project.Special", mrDirect,
	)
	require.NoError(err)
	require.Equal(http.StatusOK, prResp.StatusCode(), string(prResp.Body))
	require.NotNil(prResp.JSON200)
	assert.Equal("gitlab", prResp.JSON200.Repo.Provider)
	assert.Equal(repoPath, prResp.JSON200.Repo.RepoPath)
	assert.Equal("Sync direct provider MR", prResp.JSON200.MergeRequest.Title)
	assert.Len(*prResp.JSON200.Events, 1)

	issueResp, err := client.HTTP.SyncIssueOnHostWithResponse(
		ctx, providerHost, providerName, "Group/SubGroup", "Project.Special", issueDirect,
	)
	require.NoError(err)
	require.Equal(http.StatusOK, issueResp.StatusCode(), string(issueResp.Body))
	require.NotNil(issueResp.JSON200)
	assert.Equal("gitlab", issueResp.JSON200.Repo.Provider)
	assert.Equal(repoPath, issueResp.JSON200.Repo.RepoPath)
	assert.Equal("Sync direct provider issue", issueResp.JSON200.Issue.Title)
	assert.Len(*issueResp.JSON200.Events, 1)

	asyncPRResp, err := client.HTTP.EnqueuePrSyncOnHostWithResponse(
		ctx, providerHost, providerName, "Group/SubGroup", "Project.Special", mrAsync,
	)
	require.NoError(err)
	require.Equal(http.StatusAccepted, asyncPRResp.StatusCode(), string(asyncPRResp.Body))
	require.Eventually(func() bool {
		repoRow, rowErr := database.GetRepoByIdentity(ctx, platform.DBRepoIdentity(ref))
		if rowErr != nil || repoRow == nil {
			return false
		}
		mr, rowErr := database.GetMergeRequestByRepoIDAndNumber(ctx, repoRow.ID, 8)
		return rowErr == nil && mr != nil && mr.Title == "Sync async provider MR"
	}, 2*time.Second, 20*time.Millisecond)

	asyncIssueResp, err := client.HTTP.EnqueueIssueSyncOnHostWithResponse(
		ctx, providerHost, providerName, "Group/SubGroup", "Project.Special", issueAsync,
	)
	require.NoError(err)
	require.Equal(http.StatusAccepted, asyncIssueResp.StatusCode(), string(asyncIssueResp.Body))
	require.Eventually(func() bool {
		repoRow, rowErr := database.GetRepoByIdentity(ctx, platform.DBRepoIdentity(ref))
		if rowErr != nil || repoRow == nil {
			return false
		}
		issue, rowErr := database.GetIssueByRepoIDAndNumber(ctx, repoRow.ID, 12)
		return rowErr == nil && issue != nil && issue.Title == "Sync async provider issue"
	}, 2*time.Second, 20*time.Millisecond)
}

func TestGitLabSyncUsesTagsForRepoOverviewWhenReleasesAreAbsent(t *testing.T) {
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
	srv := New(database, syncer, nil, "/", nil, ServerOptions{})
	t.Cleanup(func() { gracefulShutdown(t, srv) })
	client := setupTestClient(t, srv)

	syncer.RunOnce(ctx)

	resp, err := client.HTTP.ListRepoSummariesWithResponse(ctx)
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode())
	require.NotNil(resp.JSON200)
	require.Len(*resp.JSON200, 1)
	require.NotNil((*resp.JSON200)[0].LatestRelease)

	assert := Assert.New(t)
	assert.Equal("v0.9.0", (*resp.JSON200)[0].LatestRelease.TagName)
	assert.Equal("https://gitlab-tags.example.com/team/service/-/tree/v0.9.0", (*resp.JSON200)[0].LatestRelease.Url)
}

func TestAPIListRepoSummaries(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)

	repos := []ghclient.RepoRef{
		{Owner: "acme", Name: "widgets", PlatformHost: "github.com"},
		{Owner: "acme", Name: "tools", PlatformHost: "github.com"},
		{Owner: "acme", Name: "archived", PlatformHost: "github.com"},
	}
	srv, database := setupTestServerWithRepos(t, &mockGH{}, repos)
	client := setupTestClient(t, srv)

	_, err := testutil.SeedFixtures(context.Background(), database)
	require.NoError(err)
	widgetsRepo, err := database.GetRepoByOwnerName(context.Background(), "acme", "widgets")
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
	require.Equal(http.StatusOK, resp.StatusCode())
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
	assert.Len(*widgets.Releases, 2)
	assert.Equal("v2.7.0", (*widgets.Releases)[1].TagName)
	assert.True((*widgets.Releases)[1].Prerelease)
	assert.Equal(
		formatUTCRFC3339(previousPublishedAt),
		*(*widgets.Releases)[1].PublishedAt,
	)
	assert.Equal(int64(42), *widgets.CommitsSinceRelease)
	require.NotNil(widgets.CommitTimeline)
	assert.Len(*widgets.CommitTimeline, 1)
	assert.Equal("Ship repo overview", (*widgets.CommitTimeline)[0].Message)
	assert.Len(*widgets.ActiveAuthors, 3)
	assert.Equal("alice", (*widgets.ActiveAuthors)[0].Login)
	assert.Equal(int64(3), (*widgets.ActiveAuthors)[0].ItemCount)
	assert.NotEmpty((*widgets.RecentIssues)[0].Title)
}

func TestAPIListRepoSummariesIncludesDefaultPlatformHost(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)

	srv, database, _ := setupTestServerWithConfigContent(t, `
default_platform_host = "ghe.example.com"

[[repos]]
owner = "acme"
name = "widgets"
platform_host = "ghe.example.com"
`, &mockGH{})

	_, err := database.UpsertRepo(
		t.Context(), db.GitHubRepoIdentity("ghe.example.com", "acme", "widgets"),
	)
	require.NoError(err)
	srv.syncer.SetRepos([]ghclient.RepoRef{{
		Owner:        "acme",
		Name:         "widgets",
		PlatformHost: "ghe.example.com",
	}})

	rr := doJSON(t, srv, http.MethodGet, "/api/v1/repos/summary", nil)
	require.Equal(http.StatusOK, rr.Code)

	var summaries []repoSummaryResponse
	require.NoError(json.NewDecoder(rr.Body).Decode(&summaries))
	require.Len(summaries, 1)
	assert.Equal("ghe.example.com", summaries[0].PlatformHost)
	assert.Equal("ghe.example.com", summaries[0].DefaultPlatformHost)
}

func TestAPIListRepoSummariesIncludesSyncedReleaseTimeline(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)
	ctx := t.Context()
	dir := t.TempDir()
	database := dbtest.Open(t)

	remote := filepath.Join(dir, "remote.git")
	work := filepath.Join(dir, "work")
	runGit(t, dir, "init", "--bare", "--initial-branch=main", remote)
	runGit(t, dir, "clone", remote, work)
	runGit(t, work, "config", "user.email", "test@test.com")
	runGit(t, work, "config", "user.name", "Test")

	commitFile := func(name, message string) {
		t.Helper()
		require.NoError(os.WriteFile(
			filepath.Join(work, name),
			[]byte(message+"\n"),
			0o644,
		))
		runGit(t, work, "add", ".")
		runGit(t, work, "commit", "-m", message)
	}

	commitFile("base.txt", "release v1")
	runGit(t, work, "tag", "v1.0.0")
	commitFile("v2.txt", "prepare v2")
	runGit(t, work, "tag", "v2.0.0")
	commitFile("v3.txt", "prepare v3")
	runGit(t, work, "tag", "v3.0.0")
	commitFile("post-1.txt", "post latest 1")
	commitFile("post-2.txt", "post latest 2")
	runGit(t, work, "push", "--tags", "origin", "main")

	clones := gitclone.New(filepath.Join(dir, "clones"), nil)
	clonePath, err := clones.ClonePath("github.com", "acme", "widgets")
	require.NoError(err)
	require.NoError(os.MkdirAll(filepath.Dir(clonePath), 0o755))
	runGit(t, dir, "clone", "--bare", remote, clonePath)

	releaseForTag := func(tag string, publishedAt time.Time) *gh.RepositoryRelease {
		t.Helper()
		name := "Release " + tag
		url := "https://github.com/acme/widgets/releases/tag/" + tag
		targetCommitish := "main"
		prerelease := false
		draft := false
		return &gh.RepositoryRelease{
			TagName:         &tag,
			Name:            &name,
			HTMLURL:         &url,
			TargetCommitish: &targetCommitish,
			Prerelease:      &prerelease,
			Draft:           &draft,
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

	srv := New(database, syncer, nil, "/", nil, ServerOptions{Clones: clones})
	t.Cleanup(func() { gracefulShutdown(t, srv) })
	client := setupTestClient(t, srv)

	resp, err := client.HTTP.ListRepoSummariesWithResponse(ctx)
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode())
	require.NotNil(resp.JSON200)
	require.Len(*resp.JSON200, 1)

	widgets := (*resp.JSON200)[0]
	require.NotNil(widgets.LatestRelease)
	require.NotNil(widgets.Releases)
	require.NotNil(widgets.CommitsSinceRelease)
	require.NotNil(widgets.CommitTimeline)
	require.NotNil(widgets.TimelineUpdatedAt)

	assert.Equal("v3.0.0", widgets.LatestRelease.TagName)
	assert.Len(*widgets.Releases, 3)
	assert.Equal("v1.0.0", (*widgets.Releases)[2].TagName)
	assert.Equal(int64(2), *widgets.CommitsSinceRelease)
	assert.Len(*widgets.CommitTimeline, 4)
	assert.Equal("post latest 2", (*widgets.CommitTimeline)[0].Message)
	assert.Equal("post latest 1", (*widgets.CommitTimeline)[1].Message)
	assert.Len((*widgets.CommitTimeline)[0].Sha, 40)
}

func TestAPIListRepoSummariesUsesTagsWhenNoReleases(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)
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

	srv := New(database, syncer, nil, "/", nil, ServerOptions{})
	t.Cleanup(func() { gracefulShutdown(t, srv) })
	client := setupTestClient(t, srv)

	resp, err := client.HTTP.ListRepoSummariesWithResponse(ctx)
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode())
	require.NotNil(resp.JSON200)
	require.Len(*resp.JSON200, 1)

	tagged := (*resp.JSON200)[0]
	require.NotNil(tagged.LatestRelease)
	require.NotNil(tagged.Releases)

	assert.Equal("v0.5.0", tagged.LatestRelease.TagName)
	assert.Equal("v0.5.0", tagged.LatestRelease.Name)
	assert.Equal("https://github.com/acme/tagged/tree/v0.5.0", tagged.LatestRelease.Url)
	assert.Equal(sha, tagged.LatestRelease.TargetCommitish)
	assert.Nil(tagged.LatestRelease.PublishedAt)
	assert.False(tagged.LatestRelease.Prerelease)
	assert.Len(*tagged.Releases, 1)
	assert.Equal("v0.5.0", (*tagged.Releases)[0].TagName)
	assert.Nil(tagged.CommitsSinceRelease)
	assert.Empty(*tagged.CommitTimeline)
}

func TestAPIListRepoSummariesClearsStaleOverviewWhenTagFallbackFails(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)
	ctx := t.Context()

	database := dbtest.Open(t)

	repoID, err := database.UpsertRepo(ctx, db.GitHubRepoIdentity("github.com", "acme", "tagless"))
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

	srv := New(database, syncer, nil, "/", nil, ServerOptions{})
	t.Cleanup(func() { gracefulShutdown(t, srv) })
	client := setupTestClient(t, srv)

	resp, err := client.HTTP.ListRepoSummariesWithResponse(ctx)
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode())
	require.NotNil(resp.JSON200)
	require.Len(*resp.JSON200, 1)

	tagless := (*resp.JSON200)[0]
	require.NotNil(tagless.Releases)
	require.NotNil(tagless.CommitTimeline)

	assert.Equal("acme", tagless.Owner)
	assert.Equal("tagless", tagless.Name)
	assert.Nil(tagless.LatestRelease)
	assert.Empty(*tagless.Releases)
	assert.Nil(tagless.CommitsSinceRelease)
	assert.Empty(*tagless.CommitTimeline)
	assert.Nil(tagless.TimelineUpdatedAt)
}

func TestAPICreateIssue(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)

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
					ID:    &labelID,
					Name:  &labelName,
					Color: &labelColor,
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
			{Owner: "acme", Name: "widgets", PlatformHost: "github.com"},
		},
	)
	client := setupTestClient(t, srv)

	_, err := database.UpsertRepo(context.Background(), db.GitHubRepoIdentity("github.com", "acme", "widgets"))
	require.NoError(err)

	resp, err := client.HTTP.CreateIssueWithResponse(
		context.Background(), "gh",
		"acme",
		"widgets",
		generated.CreateIssueJSONRequestBody{
			Title: "Ship repo summaries",
			Body:  "Add a top-level repository overview page.",
		},
	)
	require.NoError(err)
	require.Equal(http.StatusCreated, resp.StatusCode())
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
	}}, *resp.JSON201.Labels)

	issue, err := database.GetIssue(context.Background(), "acme", "widgets", 27)
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
			{Owner: "acme", Name: "widgets", PlatformHost: "github.com"},
		},
	)
	client := setupTestClient(t, srv)

	repoID, err := database.UpsertRepo(t.Context(), db.GitHubRepoIdentity("github.com", "acme", "widgets"))
	require.NoError(err)

	resp, err := client.HTTP.CreateIssueWithResponse(
		t.Context(), "gh",
		"acme",
		"widgets",
		generated.CreateIssueJSONRequestBody{Title: "Empty payload"},
	)
	require.NoError(err)
	require.Equal(http.StatusBadGateway, resp.StatusCode())

	issue, err := database.GetIssueByRepoIDAndNumber(t.Context(), repoID, 0)
	require.NoError(err)
	require.Nil(issue)
}

func TestAPIEditPRContentRejectsNilProviderPayload(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)

	mock := &mockGH{
		editPullRequestFn: func(
			context.Context,
			string,
			string,
			int,
			ghclient.EditPullRequestOpts,
		) (*gh.PullRequest, error) {
			return nil, nil
		},
	}
	srv, database := setupTestServerWithMock(t, mock)
	seedPR(t, database, "acme", "widget", 1)
	client := setupTestClient(t, srv)

	title := "Updated title"
	resp, err := client.HTTP.EditPrContentWithResponse(
		t.Context(), "gh",
		"acme",
		"widget",
		1,
		generated.EditPrContentJSONRequestBody{Title: &title},
	)
	require.NoError(err)
	require.Equal(http.StatusBadGateway, resp.StatusCode())

	mr, err := database.GetMergeRequest(t.Context(), "acme", "widget", 1)
	require.NoError(err)
	require.NotNil(mr)
	assert.Equal("Test PR #1", mr.Title)
	assert.Equal("test body", mr.Body)
}

func TestAPIPostPRCommentRejectsNilProviderPayload(t *testing.T) {
	require := require.New(t)

	mock := &mockGH{
		createIssueCommentFn: func(context.Context, string, string, int, string) (*gh.IssueComment, error) {
			return nil, nil
		},
	}
	srv, database := setupTestServerWithMock(t, mock)
	mrID := seedPR(t, database, "acme", "widget", 1)
	client := setupTestClient(t, srv)

	resp, err := client.HTTP.PostPrCommentWithResponse(
		t.Context(), "gh",
		"acme",
		"widget",
		1,
		generated.PostPrCommentJSONRequestBody{Body: "Looks good"},
	)
	require.NoError(err)
	require.Equal(http.StatusBadGateway, resp.StatusCode())

	events, err := database.ListMREvents(t.Context(), mrID)
	require.NoError(err)
	require.Empty(events)
}

func TestAPIPostIssueCommentRejectsNilProviderPayload(t *testing.T) {
	require := require.New(t)

	mock := &mockGH{
		createIssueCommentFn: func(context.Context, string, string, int, string) (*gh.IssueComment, error) {
			return nil, nil
		},
	}
	srv, database := setupTestServerWithMock(t, mock)
	issueID := seedIssue(t, database, "acme", "widget", 5, "open")
	client := setupTestClient(t, srv)

	resp, err := client.HTTP.PostIssueCommentWithResponse(
		t.Context(), "gh",
		"acme",
		"widget",
		5,
		generated.PostIssueCommentJSONRequestBody{Body: "Looks good"},
	)
	require.NoError(err)
	require.Equal(http.StatusBadGateway, resp.StatusCode())

	events, err := database.ListIssueEvents(t.Context(), issueID)
	require.NoError(err)
	require.Empty(events)
}

func TestAPIEditPRCommentRejectsNilProviderPayload(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)

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

	resp, err := client.HTTP.EditPrCommentWithResponse(
		t.Context(), "gh",
		"acme",
		"widget",
		1,
		commentID,
		generated.EditPrCommentJSONRequestBody{Body: "edited body"},
	)
	require.NoError(err)
	require.Equal(http.StatusBadGateway, resp.StatusCode())

	events, err := database.ListMREvents(t.Context(), mrID)
	require.NoError(err)
	require.Len(events, 1)
	assert.Equal("original body", events[0].Body)
}

func TestAPIEditIssueCommentRejectsNilProviderPayload(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)

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

	resp, err := client.HTTP.EditIssueCommentWithResponse(
		t.Context(), "gh",
		"acme",
		"widget",
		5,
		commentID,
		generated.EditIssueCommentJSONRequestBody{Body: "edited body"},
	)
	require.NoError(err)
	require.Equal(http.StatusBadGateway, resp.StatusCode())

	events, err := database.ListIssueEvents(t.Context(), issueID)
	require.NoError(err)
	require.Len(events, 1)
	assert.Equal("original body", events[0].Body)
}

func TestAPIApprovePRRejectsNilProviderPayload(t *testing.T) {
	require := require.New(t)

	mock := &mockGH{
		createReviewFn: func(context.Context, string, string, int, string, string) (*gh.PullRequestReview, error) {
			return nil, nil
		},
	}
	srv, database := setupTestServerWithMock(t, mock)
	mrID := seedPR(t, database, "acme", "widget", 1)
	client := setupTestClient(t, srv)

	resp, err := client.HTTP.ApprovePullWithResponse(
		t.Context(), "gh",
		"acme",
		"widget",
		1,
		generated.ApprovePullJSONRequestBody{},
	)
	require.NoError(err)
	require.Equal(http.StatusBadGateway, resp.StatusCode())

	events, err := database.ListMREvents(t.Context(), mrID)
	require.NoError(err)
	require.Empty(events)
}

func TestAPIMergePRRejectsNilProviderPayload(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)

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
	seedPR(t, database, "acme", "widget", 1)
	client := setupTestClient(t, srv)

	resp, err := client.HTTP.MergePullWithResponse(
		t.Context(), "gh",
		"acme",
		"widget",
		1,
		generated.MergePullJSONRequestBody{
			Method: "squash",
		},
	)
	require.NoError(err)
	require.Equal(http.StatusBadGateway, resp.StatusCode())

	mr, err := database.GetMergeRequest(t.Context(), "acme", "widget", 1)
	require.NoError(err)
	require.NotNil(mr)
	assert.Equal(db.MergeRequestStateOpen, mr.State)
	assert.Nil(mr.MergedAt)
}

func TestAPICreateIssueUsesPlatformHost(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)

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
		{Owner: "acme", Name: "widgets", PlatformHost: "github.com"},
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
	srv := New(database, syncer, nil, "/", nil, ServerOptions{})
	t.Cleanup(func() { gracefulShutdown(t, srv) })

	_, err := database.UpsertRepo(context.Background(), db.GitHubRepoIdentity("github.com", "acme", "widgets"))
	require.NoError(err)
	enterpriseRepoID, err := database.UpsertRepo(context.Background(), db.GitHubRepoIdentity("ghe.example.com", "acme", "widgets"))
	require.NoError(err)

	client := setupTestClient(t, srv)
	resp, err := client.HTTP.CreateIssueOnHostWithResponse(
		t.Context(),
		"ghe.example.com",
		"gh",
		"acme",
		"widgets",
		generated.CreateIssueOnHostJSONRequestBody{
			Title: "Ship enterprise issue",
			Body:  "Route to the selected host.",
		},
	)
	require.NoError(err)
	require.Equal(http.StatusCreated, resp.StatusCode())
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

func TestAPIPostPrCommentAllowsMixedCaseTrackedRepo(t *testing.T) {
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

	resp, err := client.HTTP.PostPrCommentWithResponse(
		t.Context(), "gh",
		"acme",
		"widget",
		7,
		generated.PostPrCommentJSONRequestBody{Body: "looks good"},
	)
	require.NoError(err)
	require.Equal(http.StatusCreated, resp.StatusCode())
	require.NotNil(resp.JSON201)
}

func TestAPIEditPrCommentUpdatesGitHubAndLocalTimeline(t *testing.T) {
	assert := Assert.New(t)
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
		CreatedAt:      createdAt,
		DedupeKey:      "comment-9876",
	}}))

	req := httptest.NewRequest(
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
	require.NotNil(events[0].PlatformID)
	assert.Equal(commentID, *events[0].PlatformID)
}

func TestAPIEditPrCommentRejectsCommentFromDifferentPR(t *testing.T) {
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

	req := httptest.NewRequest(
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

func TestAPIEditIssueCommentUpdatesGitHubAndLocalTimeline(t *testing.T) {
	assert := Assert.New(t)
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
		IssueID:    issueID,
		PlatformID: &commentID,
		EventType:  "issue_comment",
		Author:     "maintainer",
		Body:       "original issue body",
		CreatedAt:  createdAt,
		DedupeKey:  "issue-comment-1234",
	}}))

	req := httptest.NewRequest(
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
	require.NotNil(events[0].PlatformID)
	assert.Equal(commentID, *events[0].PlatformID)
}

func TestAPIEditIssueCommentRejectsCommentFromDifferentIssue(t *testing.T) {
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

	req := httptest.NewRequest(
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

func TestAPICommentAutocomplete(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	srv, database := setupTestServer(t)
	ctx := t.Context()

	repoID, err := database.UpsertRepo(ctx, db.GitHubRepoIdentity("github.com", "acme", "widget"))
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

	userReq := httptest.NewRequest(http.MethodGet, "/api/v1/repo/gh/acme/widget/comment-autocomplete?trigger=@&q=al&limit=10", nil)
	userRR := httptest.NewRecorder()
	srv.ServeHTTP(userRR, userReq)
	require.Equal(http.StatusOK, userRR.Code, userRR.Body.String())

	var userBody commentAutocompleteResponse
	require.NoError(json.NewDecoder(userRR.Body).Decode(&userBody))
	assert.Equal([]string{"albert", "alex", "alice"}, userBody.Users)
	assert.Empty(userBody.References)

	refReq := httptest.NewRequest(http.MethodGet, "/api/v1/repo/gh/acme/widget/comment-autocomplete?trigger=%23&q=1&limit=10", nil)
	refRR := httptest.NewRecorder()
	srv.ServeHTTP(refRR, refReq)
	require.Equal(http.StatusOK, refRR.Code, refRR.Body.String())

	var refBody commentAutocompleteResponse
	require.NoError(json.NewDecoder(refRR.Body).Decode(&refBody))
	assert.Equal([]db.CommentAutocompleteReference{
		{Kind: "issue", Number: 17, Title: "Mention bug", State: "open"},
		{Kind: "pull", Number: 12, Title: "Polish mentions", State: "open"},
	}, refBody.References)
	assert.Empty(refBody.Users)

	bangReq := httptest.NewRequest(http.MethodGet, "/api/v1/repo/gh/acme/widget/comment-autocomplete?trigger=!&q=1&limit=10", nil)
	bangRR := httptest.NewRecorder()
	srv.ServeHTTP(bangRR, bangReq)
	assert.Equal(http.StatusBadRequest, bangRR.Code, bangRR.Body.String())

	gitlabRepoID, err := database.UpsertRepo(ctx, db.RepoIdentity{
		Platform:     "gitlab",
		PlatformHost: "gitlab.example.com",
		Owner:        "group",
		Name:         "project",
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

	gitlabIssueReq := httptest.NewRequest(http.MethodGet, "/api/v1/host/gitlab.example.com/repo/gitlab/group/project/comment-autocomplete?trigger=%23&q=1&limit=10", nil)
	gitlabIssueRR := httptest.NewRecorder()
	srv.ServeHTTP(gitlabIssueRR, gitlabIssueReq)
	require.Equal(http.StatusOK, gitlabIssueRR.Code, gitlabIssueRR.Body.String())

	var gitlabIssueBody commentAutocompleteResponse
	require.NoError(json.NewDecoder(gitlabIssueRR.Body).Decode(&gitlabIssueBody))
	assert.Equal([]db.CommentAutocompleteReference{
		{Kind: "issue", Number: 17, Title: "Mention issue", State: "open"},
	}, gitlabIssueBody.References)

	gitlabMRReq := httptest.NewRequest(http.MethodGet, "/api/v1/host/gitlab.example.com/repo/gitlab/group/project/comment-autocomplete?trigger=!&q=1&limit=10", nil)
	gitlabMRRR := httptest.NewRecorder()
	srv.ServeHTTP(gitlabMRRR, gitlabMRReq)
	require.Equal(http.StatusOK, gitlabMRRR.Code, gitlabMRRR.Body.String())

	var gitlabMRBody commentAutocompleteResponse
	require.NoError(json.NewDecoder(gitlabMRRR.Body).Decode(&gitlabMRBody))
	assert.Equal([]db.CommentAutocompleteReference{
		{Kind: "pull", Number: 12, Title: "Polish merge request mentions", State: "open"},
	}, gitlabMRBody.References)
}

func TestAPICommentAutocompleteUsesRepoPlatformHost(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	srv, database := setupTestServer(t)
	ctx := t.Context()

	githubRepoID, err := database.UpsertRepo(ctx, db.GitHubRepoIdentity("github.com", "acme", "widget"))
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

	repoID, err := database.UpsertRepo(ctx, db.GitHubRepoIdentity("ghe.example.com", "acme", "widget"))
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

	req := httptest.NewRequest(http.MethodGet, "/api/v1/host/ghe.example.com/repo/gh/acme/widget/comment-autocomplete?trigger=%23&q=1&limit=10", nil)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	require.Equal(http.StatusOK, rr.Code, rr.Body.String())

	var body commentAutocompleteResponse
	require.NoError(json.NewDecoder(rr.Body).Decode(&body))
	assert.Equal([]db.CommentAutocompleteReference{{Kind: "pull", Number: 12, Title: "Polish mentions", State: "open"}}, body.References)
}

func TestAPISyncStatus(t *testing.T) {
	require := require.New(t)

	srv, _ := setupTestServer(t)
	client := setupTestClient(t, srv)
	srv.syncer.RunOnce(t.Context())

	resp, err := client.HTTP.GetSyncStatusWithResponse(t.Context())
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode())
	require.NotNil(resp.JSON200)
	require.False(resp.JSON200.Running)
	require.NotNil(resp.JSON200.LastRunAt)
	Assert.Equal(t, time.UTC, resp.JSON200.LastRunAt.Location())
}

func TestAPITriggerSyncIgnoresRequestCancellation(t *testing.T) {
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
	srv := New(
		database, syncer, nil, "/",
		nil, ServerOptions{},
	)
	t.Cleanup(syncer.Stop)

	ctx, cancel := context.WithCancel(t.Context())
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sync", nil).WithContext(ctx)
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
	Assert.Equal(t, "acme", repos[0].Owner)
	Assert.Equal(t, "widget", repos[0].Name)
}

func TestAPITriggerSyncBypassesNextSyncAfter(t *testing.T) {
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
			database, "github.com", "rest",
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

	srv := New(database, syncer, nil, "/", nil, ServerOptions{})
	t.Cleanup(func() { gracefulShutdown(t, srv) })

	// Seed the host cooldown window exactly like a recent background sync.
	syncer.RunOnce(t.Context())
	require.Equal(int32(1), listCalls.Load())

	client := setupTestClient(t, srv)
	resp, err := client.HTTP.TriggerSyncWithResponse(
		t.Context(),
	)
	require.NoError(err)
	require.Equal(http.StatusAccepted, resp.StatusCode())

	select {
	case <-secondSync:
	case <-time.After(2 * time.Second):
		require.Fail("expected explicit sync request to bypass background cooldown")
	}
}

func TestAPIReadyForReview(t *testing.T) {
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
	srv := New(
		database, syncer, nil, "/",
		nil, ServerOptions{},
	)
	client := setupTestClient(t, srv)

	repoID, err := database.UpsertRepo(t.Context(), db.GitHubRepoIdentity("github.com", "acme", "widget"))
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

	resp, err := client.HTTP.MarkPullReadyForReviewWithResponse(
		t.Context(), "gh", "acme", "widget", 1,
	)
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode())
	require.NotNil(resp.JSON200)

	pr, err := database.GetMergeRequest(t.Context(), "acme", "widget", 1)
	require.NoError(err)
	require.NotNil(pr)
	require.False(pr.IsDraft)
}

func TestAPISetStarred(t *testing.T) {
	require := require.New(t)
	srv, database := setupTestServer(t)
	seedPR(t, database, "acme", "widget", 1)
	client := setupTestClient(t, srv)

	resp, err := client.HTTP.SetStarredWithResponse(t.Context(), generated.SetStarredJSONRequestBody{
		ItemType: "pr",
		Owner:    "acme",
		Name:     "widget",
		Number:   1,
	})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode())

	starred, err := database.IsStarred(t.Context(), "pr", 1, 1)
	require.NoError(err)
	require.True(starred)
}

func TestAPIUnsetStarred(t *testing.T) {
	require := require.New(t)
	srv, database := setupTestServer(t)
	seedPR(t, database, "acme", "widget", 1)
	require.NoError(database.SetStarred(t.Context(), "pr", 1, 1))
	client := setupTestClient(t, srv)

	resp, err := client.HTTP.UnsetStarredWithResponse(t.Context(), generated.UnsetStarredJSONRequestBody{
		ItemType: "pr",
		Owner:    "acme",
		Name:     "widget",
		Number:   1,
	})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode())

	starred, err := database.IsStarred(t.Context(), "pr", 1, 1)
	require.NoError(err)
	require.False(starred)
}

func TestAPISetStarredRejectsInvalidItemType(t *testing.T) {
	require := require.New(t)
	srv, _ := setupTestServer(t)
	client := setupTestClient(t, srv)

	resp, err := client.HTTP.SetStarredWithResponse(t.Context(), generated.SetStarredJSONRequestBody{
		ItemType: "repo",
		Owner:    "acme",
		Name:     "widget",
		Number:   1,
	})
	require.NoError(err)
	require.Equal(http.StatusBadRequest, resp.StatusCode())
	require.NotNil(resp.ApplicationproblemJSONDefault)
	require.NotNil(resp.ApplicationproblemJSONDefault.Detail)
	require.Contains(*resp.ApplicationproblemJSONDefault.Detail, "item_type must be 'pr' or 'issue'")
}

func TestOpenAPIEndpointReflectsHumaContract(t *testing.T) {
	require := require.New(t)
	srv, _ := setupTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/openapi.json", nil)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	require.Equal(http.StatusOK, rr.Code, rr.Body.String())

	body := rr.Body.String()
	require.Contains(body, `"/activity"`)
	require.Contains(body, `"name":"since"`)
	require.Contains(body, `"capped"`)
	require.NotContains(body, `"name":"before"`)
	require.NotContains(body, `"has_more"`)
}

// seedIssue inserts a repo and an issue into the DB.
func seedIssue(t *testing.T, database *db.DB, owner, name string, number int, state string) int64 {
	t.Helper()
	ctx := t.Context()
	repoID, err := database.UpsertRepo(ctx, db.GitHubRepoIdentity("github.com", owner, name))
	require.NoError(t, err)

	now := time.Now().UTC().Truncate(time.Second)
	issue := &db.Issue{
		RepoID: repoID, PlatformID: int64(number) * 1000, Number: number,
		URL:   "https://github.com/" + owner + "/" + name + "/issues/1",
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

func seedIssueWithLabels(t *testing.T, database *db.DB, owner, name string, number int, state string, labels []db.Label) int64 {
	t.Helper()
	ctx := t.Context()
	issueID := seedIssue(t, database, owner, name, number, state)
	repo, err := database.GetRepoByOwnerName(ctx, owner, name)
	require.NoError(t, err)
	require.NoError(t, database.ReplaceIssueLabels(ctx, repo.ID, issueID, labels))
	return issueID
}

func seedIssueOnHost(
	t *testing.T, database *db.DB,
	host, owner, name string, number int,
	state, title string,
) int64 {
	t.Helper()
	ctx := context.Background()

	repoID, err := database.UpsertRepo(ctx, db.GitHubRepoIdentity(host, owner, name))
	require.NoError(t, err)

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

func TestAPIClosePR(t *testing.T) {
	require := require.New(t)

	srv, database := setupTestServer(t)
	handlerNow := testEDTTime(9, 15)
	setTestServerNow(t, srv, handlerNow)
	seedPR(t, database, "acme", "widget", 1)
	client := setupTestClient(t, srv)

	resp, err := client.HTTP.SetPrGithubStateWithResponse(
		t.Context(), "gh", "acme", "widget", 1,
		generated.SetPrGithubStateJSONRequestBody{State: "closed"},
	)
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode())

	pr, err := database.GetMergeRequest(t.Context(), "acme", "widget", 1)
	require.NoError(err)
	require.Equal(db.MergeRequestStateClosed, pr.State)
	assertTimePtrEqualsUTC(t, pr.ClosedAt, handlerNow)
}

func TestAPIReopenPR(t *testing.T) {
	require := require.New(t)
	srv, database := setupTestServer(t)
	seedPR(t, database, "acme", "widget", 1)
	ctx := t.Context()

	// Close it first.
	repo, err := database.GetRepoByOwnerName(ctx, "acme", "widget")
	require.NoError(err)
	now := time.Now()
	require.NoError(database.UpdateMRState(ctx, repo.ID, 1, "closed", nil, &now))

	client := setupTestClient(t, srv)
	resp, err := client.HTTP.SetPrGithubStateWithResponse(
		ctx, "gh", "acme", "widget", 1,
		generated.SetPrGithubStateJSONRequestBody{State: "open"},
	)
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode())

	pr, err := database.GetMergeRequest(ctx, "acme", "widget", 1)
	require.NoError(err)
	require.Equal(db.MergeRequestStateOpen, pr.State)
	require.Nil(pr.ClosedAt, "closed_at should be cleared on reopen")
}

func TestAPIClosePRRejectsMerged(t *testing.T) {
	require := require.New(t)
	srv, database := setupTestServer(t)
	seedPR(t, database, "acme", "widget", 1)
	ctx := t.Context()

	repo, err := database.GetRepoByOwnerName(ctx, "acme", "widget")
	require.NoError(err)
	now := time.Now()
	require.NoError(database.UpdateMRState(ctx, repo.ID, 1, "merged", &now, &now))

	client := setupTestClient(t, srv)
	resp, err := client.HTTP.SetPrGithubStateWithResponse(
		ctx, "gh", "acme", "widget", 1,
		generated.SetPrGithubStateJSONRequestBody{State: "open"},
	)
	require.NoError(err)
	require.Equal(http.StatusConflict, resp.StatusCode())
}

func TestAPIClosePRInvalidState(t *testing.T) {
	srv, database := setupTestServer(t)
	seedPR(t, database, "acme", "widget", 1)
	client := setupTestClient(t, srv)

	resp, err := client.HTTP.SetPrGithubStateWithResponse(
		t.Context(), "gh", "acme", "widget", 1,
		generated.SetPrGithubStateJSONRequestBody{State: "nonsense"},
	)
	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, resp.StatusCode())
}

func TestAPICloseIssue(t *testing.T) {
	require := require.New(t)

	srv, database := setupTestServer(t)
	handlerNow := testEDTTime(10, 45)
	setTestServerNow(t, srv, handlerNow)
	seedIssue(t, database, "acme", "widget", 5, "open")
	client := setupTestClient(t, srv)

	resp, err := client.HTTP.SetIssueGithubStateWithResponse(
		t.Context(), "gh", "acme", "widget", 5,
		generated.SetIssueGithubStateJSONRequestBody{State: "closed"},
	)
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode())

	issue, err := database.GetIssue(t.Context(), "acme", "widget", 5)
	require.NoError(err)
	require.Equal("closed", issue.State)
	assertTimePtrEqualsUTC(t, issue.ClosedAt, handlerNow)
}

func TestAPIReopenIssue(t *testing.T) {
	require := require.New(t)
	srv, database := setupTestServer(t)
	seedIssue(t, database, "acme", "widget", 5, "closed")
	client := setupTestClient(t, srv)

	resp, err := client.HTTP.SetIssueGithubStateWithResponse(
		t.Context(), "gh", "acme", "widget", 5,
		generated.SetIssueGithubStateJSONRequestBody{State: "open"},
	)
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode())

	issue, err := database.GetIssue(t.Context(), "acme", "widget", 5)
	require.NoError(err)
	require.Equal("open", issue.State)
	require.Nil(issue.ClosedAt, "closed_at should be cleared on reopen")
}

func TestAPISyncPRDoesNotOverwriteNewerStateChange(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)

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

	syncDone := make(chan *generated.SyncPullResponse, 1)
	syncErr := make(chan error, 1)
	go func() {
		resp, err := client.HTTP.SyncPullWithResponse(
			t.Context(), "gh", "acme", "widget", 1,
		)
		if err != nil {
			syncErr <- err
			return
		}
		syncDone <- resp
	}()

	<-syncStarted

	resp, err := client.HTTP.SetPrGithubStateWithResponse(
		t.Context(), "gh", "acme", "widget", 1,
		generated.SetPrGithubStateJSONRequestBody{State: "closed"},
	)
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode())

	closedPR, err := database.GetMergeRequest(t.Context(), "acme", "widget", 1)
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
		require.Equal(http.StatusOK, resp.StatusCode())
		completed = true
	case <-time.After(5 * time.Second):
	}
	require.True(completed, "timed out waiting for stale PR sync")

	finalPR, err := database.GetMergeRequest(t.Context(), "acme", "widget", 1)
	require.NoError(err)
	assert.Equal(db.MergeRequestStateClosed, finalPR.State)
	assert.NotNil(finalPR.ClosedAt)
	assert.Equal("Test PR #1", finalPR.Title)
	assert.True(finalPR.UpdatedAt.After(staleUpdatedAt))
}

func TestAPISyncPRPreservesCIStatusWhileRefreshingCI(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)

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
	repo, err := database.GetRepoByOwnerName(t.Context(), "acme", "widget")
	require.NoError(err)
	require.NotNil(repo)
	existingChecksJSON := `[{"name":"tests","status":"completed","conclusion":"success"}]`
	require.NoError(database.UpdateMRCIStatus(
		t.Context(), repo.ID, 1, "success", existingChecksJSON,
	))
	client := setupTestClient(t, srv)

	syncDone := make(chan *generated.SyncPullResponse, 1)
	syncErr := make(chan error, 1)
	go func() {
		resp, err := client.HTTP.SyncPullWithResponse(
			t.Context(), "gh", "acme", "widget", 1,
		)
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

	detailResp, err := client.HTTP.GetPullWithResponse(
		t.Context(), "gh", "acme", "widget", 1,
	)
	require.NoError(err)
	require.Equal(http.StatusOK, detailResp.StatusCode())
	require.NotNil(detailResp.JSON200)
	require.NotNil(detailResp.JSON200.MergeRequest)
	assert.Equal("success", detailResp.JSON200.MergeRequest.CIStatus)
	assert.JSONEq(existingChecksJSON, detailResp.JSON200.MergeRequest.CIChecksJSON)

	releaseOnce.Do(func() { close(releaseCIRefresh) })
	select {
	case err := <-syncErr:
		require.NoError(err)
	case resp := <-syncDone:
		require.Equal(http.StatusOK, resp.StatusCode())
	case <-time.After(5 * time.Second):
		require.Fail("timed out waiting for PR sync")
	}
}

// When the head SHA changes, previously-recorded CI is tied to the old
// commit and must not be carried forward. If the in-flight CI refresh
// then fails, the detail must show "no CI" rather than stale checks
// attached to a different commit.
func TestAPISyncPRClearsCIWhenHeadSHAChanges(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)

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
	repo, err := database.GetRepoByOwnerName(t.Context(), "acme", "widget")
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
		t.Context(), "github.com", "acme", "widget", 1, true,
	))
	client := setupTestClient(t, srv)

	syncResp, err := client.HTTP.SyncPullWithResponse(
		t.Context(), "gh", "acme", "widget", 1,
	)
	require.NoError(err)
	require.Equal(http.StatusOK, syncResp.StatusCode())

	detailResp, err := client.HTTP.GetPullWithResponse(
		t.Context(), "gh", "acme", "widget", 1,
	)
	require.NoError(err)
	require.Equal(http.StatusOK, detailResp.StatusCode())
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
	resp, err := client.HTTP.EnqueuePrSyncWithResponse(
		ctx, "gh", "acme", "widget", 1,
	)
	require.NoError(err)
	require.Equal(http.StatusAccepted, resp.StatusCode())

	select {
	case <-syncStarted:
	case <-time.After(2 * time.Second):
		require.Fail("background sync did not start")
	}
	releaseOnce.Do(func() { close(releaseSync) })
}

func TestAPIEnqueueIssueSyncReturnsBeforeGitHubFetchCompletes(t *testing.T) {
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
	resp, err := client.HTTP.EnqueueIssueSyncWithResponse(
		ctx, "gh", "acme", "widget", 5,
	)
	require.NoError(err)
	require.Equal(http.StatusAccepted, resp.StatusCode())

	select {
	case <-syncStarted:
	case <-time.After(2 * time.Second):
		require.Fail("background issue sync did not start")
	}
	releaseOnce.Do(func() { close(releaseSync) })
}

func TestAPIReadyForReviewDoesNotGetRevertedByStaleSync(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)

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

	repoID, err := database.UpsertRepo(t.Context(), db.GitHubRepoIdentity("github.com", "acme", "widget"))
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

	syncDone := make(chan *generated.SyncPullResponse, 1)
	syncErr := make(chan error, 1)
	go func() {
		resp, err := client.HTTP.SyncPullWithResponse(
			t.Context(), "gh", "acme", "widget", 1,
		)
		if err != nil {
			syncErr <- err
			return
		}
		syncDone <- resp
	}()

	<-syncStarted

	resp, err := client.HTTP.MarkPullReadyForReviewWithResponse(
		t.Context(), "gh", "acme", "widget", 1,
	)
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode())

	readyPR, err := database.GetMergeRequest(t.Context(), "acme", "widget", 1)
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
		require.Equal(http.StatusOK, resp.StatusCode())
		completed = true
	case <-time.After(5 * time.Second):
	}
	require.True(completed, "timed out waiting for stale draft sync")

	finalPR, err := database.GetMergeRequest(t.Context(), "acme", "widget", 1)
	require.NoError(err)
	assert.False(finalPR.IsDraft)
	assert.Equal("ready for review", finalPR.Title)
	assert.True(finalPR.UpdatedAt.Equal(readyUpdatedAt))
}

func TestAPISyncIssueDoesNotOverwriteNewerStateChange(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)

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

	syncDone := make(chan *generated.SyncIssueResponse, 1)
	syncErr := make(chan error, 1)
	go func() {
		resp, err := client.HTTP.SyncIssueWithResponse(
			t.Context(), "gh", "acme", "widget", 5,
		)
		if err != nil {
			syncErr <- err
			return
		}
		syncDone <- resp
	}()

	<-syncStarted

	resp, err := client.HTTP.SetIssueGithubStateWithResponse(
		t.Context(), "gh", "acme", "widget", 5,
		generated.SetIssueGithubStateJSONRequestBody{State: "closed"},
	)
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode())

	closedIssue, err := database.GetIssue(t.Context(), "acme", "widget", 5)
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
		require.Equal(http.StatusOK, resp.StatusCode())
		completed = true
	case <-time.After(5 * time.Second):
	}
	require.True(completed, "timed out waiting for stale issue sync")

	finalIssue, err := database.GetIssue(t.Context(), "acme", "widget", 5)
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
	require := require.New(t)
	assert := Assert.New(t)
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
	client := setupTestClient(t, srv)

	// Before the nil guard, refreshIssueTimeline panicked on
	// ghIssue.UpdatedAt.Time and the handler returned 502.
	syncResp, err := client.HTTP.SyncIssueWithResponse(
		ctx, "gh", "acme", "widget", 9,
	)
	require.NoError(err)
	require.Equal(http.StatusOK, syncResp.StatusCode())
	require.NotNil(syncResp.JSON200)
	// LastActivityAt must equal CreatedAt, not Go's zero time.
	// Without the fallback, activity-ordered views would sort
	// this issue at 0001-01-01 instead of its creation date.
	assert.False(syncResp.JSON200.Issue.LastActivityAt.IsZero())
	assert.Equal(createdAt, syncResp.JSON200.Issue.LastActivityAt.UTC())

	// Verify the persisted value round-trips through the read
	// endpoint so the storage -> serializer path is covered.
	getResp, err := client.HTTP.GetIssueWithResponse(
		ctx, "gh", "acme", "widget", 9,
	)
	require.NoError(err)
	require.Equal(http.StatusOK, getResp.StatusCode())
	require.NotNil(getResp.JSON200)
	assert.Equal(createdAt, getResp.JSON200.Issue.LastActivityAt.UTC())
}

func TestAPIListPullsSearchByNumber(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)
	srv, database := setupTestServer(t)
	ctx := t.Context()

	seedPR(t, database, "acme", "widget", 12, withSeedPRTitle("add feature"))
	prID := seedPR(t, database, "acme", "widget", 278, withSeedPRTitle("fix bug"))
	seedPR(t, database, "acme", "widget", 290, withSeedPRTitle("another change"))
	repo, err := database.GetRepoByOwnerName(ctx, "acme", "widget")
	require.NoError(err)
	require.NoError(database.ReplaceMergeRequestLabels(ctx, repo.ID, prID, []db.Label{{
		PlatformID: 200,
		Name:       "needs-review",
		Color:      "fbca04",
		UpdatedAt:  time.Now().UTC(),
	}}))

	client := setupTestClient(t, srv)

	pullNumbers := func(params *generated.ListPullsParams) []int {
		t.Helper()
		resp, err := client.HTTP.ListPullsWithResponse(ctx, params)
		require.NoError(err)
		require.Equal(http.StatusOK, resp.StatusCode())
		require.NotNil(resp.JSON200)
		nums := make([]int, 0, len(*resp.JSON200))
		for _, pr := range *resp.JSON200 {
			nums = append(nums, int(pr.Number))
		}
		return nums
	}

	q := "278"
	assert.ElementsMatch([]int{278}, pullNumbers(&generated.ListPullsParams{Q: &q}))

	q = "#278"
	assert.ElementsMatch([]int{278}, pullNumbers(&generated.ListPullsParams{Q: &q}))

	// Title still matches.
	q = "fix"
	assert.ElementsMatch([]int{278}, pullNumbers(&generated.ListPullsParams{Q: &q}))

	q = "needs-review"
	assert.ElementsMatch([]int{278}, pullNumbers(&generated.ListPullsParams{Q: &q}))

	// Substring of number matches multiple.
	q = "2"
	assert.ElementsMatch([]int{12, 278, 290}, pullNumbers(&generated.ListPullsParams{Q: &q}))
}

func TestAPIListIssuesSearchByNumber(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)
	srv, database := setupTestServer(t)
	ctx := t.Context()

	seedIssueOnHost(t, database, "github.com", "acme", "widget", 12, "open", "report a bug")
	issueID := seedIssueOnHost(t, database, "github.com", "acme", "widget", 278, "open", "filter broken")
	seedIssueOnHost(t, database, "github.com", "acme", "widget", 290, "open", "another change")
	repo, err := database.GetRepoByOwnerName(ctx, "acme", "widget")
	require.NoError(err)
	require.NoError(database.ReplaceIssueLabels(ctx, repo.ID, issueID, []db.Label{{
		PlatformID: 300,
		Name:       "needs-triage",
		Color:      "d73a4a",
		UpdatedAt:  time.Now().UTC(),
	}}))

	client := setupTestClient(t, srv)

	issueNumbers := func(params *generated.ListIssuesParams) []int {
		t.Helper()
		resp, err := client.HTTP.ListIssuesWithResponse(ctx, params)
		require.NoError(err)
		require.Equal(http.StatusOK, resp.StatusCode())
		require.NotNil(resp.JSON200)
		nums := make([]int, 0, len(*resp.JSON200))
		for _, issue := range *resp.JSON200 {
			nums = append(nums, int(issue.Number))
		}
		return nums
	}

	q := "278"
	assert.ElementsMatch([]int{278}, issueNumbers(&generated.ListIssuesParams{Q: &q}))

	q = "#278"
	assert.ElementsMatch([]int{278}, issueNumbers(&generated.ListIssuesParams{Q: &q}))

	// Title still matches.
	q = "broken"
	assert.ElementsMatch([]int{278}, issueNumbers(&generated.ListIssuesParams{Q: &q}))

	q = "needs-triage"
	assert.ElementsMatch([]int{278}, issueNumbers(&generated.ListIssuesParams{Q: &q}))

	// Substring of number matches multiple.
	q = "2"
	assert.ElementsMatch([]int{12, 278, 290}, issueNumbers(&generated.ListIssuesParams{Q: &q}))
}

func TestAPIListItemsHonorsLimit(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)
	srv, database := setupTestServer(t)
	ctx := t.Context()

	seedPR(t, database, "acme", "widget", 12)
	seedPR(t, database, "acme", "widget", 278)
	seedIssue(t, database, "acme", "widget", 12, "open")
	seedIssue(t, database, "acme", "widget", 278, "open")

	client := setupTestClient(t, srv)
	limit := int64(1)

	pullsResp, err := client.HTTP.ListPullsWithResponse(ctx, &generated.ListPullsParams{Limit: &limit})
	require.NoError(err)
	require.Equal(http.StatusOK, pullsResp.StatusCode())
	require.NotNil(pullsResp.JSON200)
	require.Len(*pullsResp.JSON200, 1)
	assert.EqualValues(278, (*pullsResp.JSON200)[0].Number)

	offset := int64(1)
	secondPullResp, err := client.HTTP.ListPullsWithResponse(ctx, &generated.ListPullsParams{Limit: &limit, Offset: &offset})
	require.NoError(err)
	require.Equal(http.StatusOK, secondPullResp.StatusCode())
	require.NotNil(secondPullResp.JSON200)
	require.Len(*secondPullResp.JSON200, 1)
	assert.EqualValues(12, (*secondPullResp.JSON200)[0].Number)

	issuesResp, err := client.HTTP.ListIssuesWithResponse(ctx, &generated.ListIssuesParams{Limit: &limit})
	require.NoError(err)
	require.Equal(http.StatusOK, issuesResp.StatusCode())
	require.NotNil(issuesResp.JSON200)
	require.Len(*issuesResp.JSON200, 1)
	assert.EqualValues(278, (*issuesResp.JSON200)[0].Number)

	secondIssueResp, err := client.HTTP.ListIssuesWithResponse(ctx, &generated.ListIssuesParams{Limit: &limit, Offset: &offset})
	require.NoError(err)
	require.Equal(http.StatusOK, secondIssueResp.StatusCode())
	require.NotNil(secondIssueResp.JSON200)
	require.Len(*secondIssueResp.JSON200, 1)
	assert.EqualValues(12, (*secondIssueResp.JSON200)[0].Number)
}

func TestAPIListPullsReportsBackfilledMergedPRFromMergedAt(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	ctx := t.Context()

	now := time.Date(2024, 6, 7, 12, 0, 0, 0, time.UTC)
	mergedAt := now.Add(time.Hour)
	number := 42
	platformID := int64(42000)
	title := "Backfilled merged PR"
	state := "closed"
	url := "https://github.com/acme/widget/pull/42"
	author := "alice"
	headRef := "feature"
	headSHA := "abc123def456"
	baseRef := "main"
	baseSHA := "def456abc123"
	mock := &mockGH{
		listPullRequestsPageFn: func(_ context.Context, owner, repo, listState string, page int) ([]*gh.PullRequest, bool, error) {
			require.Equal("acme", owner)
			require.Equal("widget", repo)
			require.Equal("closed", listState)
			require.Equal(1, page)
			return []*gh.PullRequest{{
				ID:        &platformID,
				Number:    &number,
				Title:     &title,
				State:     &state,
				HTMLURL:   &url,
				User:      &gh.User{Login: &author},
				CreatedAt: &gh.Timestamp{Time: now},
				UpdatedAt: &gh.Timestamp{Time: now},
				ClosedAt:  &gh.Timestamp{Time: mergedAt},
				MergedAt:  &gh.Timestamp{Time: mergedAt},
				Head: &gh.PullRequestBranch{
					Ref: &headRef,
					SHA: &headSHA,
				},
				Base: &gh.PullRequestBranch{
					Ref: &baseRef,
					SHA: &baseSHA,
				},
			}}, false, nil
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
	srv := New(database, syncer, nil, "/", nil, ServerOptions{})
	t.Cleanup(func() { gracefulShutdown(t, srv) })

	srv.syncer.RunOnce(ctx)

	client := setupTestClient(t, srv)
	filterState := "closed"
	resp, err := client.HTTP.ListPullsWithResponse(ctx, &generated.ListPullsParams{State: &filterState})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode())
	require.NotNil(resp.JSON200)
	require.Len(*resp.JSON200, 1)

	apiPR := (*resp.JSON200)[0]
	assert.Equal(int64(number), apiPR.Number)
	assert.Equal(title, apiPR.Title)
	assert.Equal(generated.MergeRequestResponseStateMerged, apiPR.State)
	require.NotNil(apiPR.MergedAt)
	assert.True(apiPR.MergedAt.Equal(mergedAt))
}

func TestAPIListPullsCasefoldsRepoNames(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	srv, database := setupTestServerWithRepos(t, &mockGH{}, []ghclient.RepoRef{
		{Owner: "org", Name: "foo", PlatformHost: "github.com"},
	})

	seedPR(t, database, "Org", "Foo", 1)
	seedPR(t, database, "org", "foo", 1)

	client := setupTestClient(t, srv)
	resp, err := client.HTTP.ListPullsWithResponse(t.Context(), nil)
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode())
	require.NotNil(resp.JSON200)
	require.Len(*resp.JSON200, 1)
	assert.Equal("org", (*resp.JSON200)[0].RepoOwner)
	assert.Equal("foo", (*resp.JSON200)[0].RepoName)
}

func TestAPIListPullsFiltersHostedNestedRepoPath(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	srv, database := setupTestServerWithRepos(t, &mockGH{}, []ghclient.RepoRef{
		{Owner: "Group/SubGroup", Name: "Project.Special", PlatformHost: "ghe.example.com"},
		{Owner: "other", Name: "repo", PlatformHost: "ghe.example.com"},
	})

	seedPROnHost(t, database, "ghe.example.com", "Group/SubGroup", "Project.Special", 1)
	seedPROnHost(t, database, "ghe.example.com", "other", "repo", 2)

	client := setupTestClient(t, srv)
	repo := "ghe.example.com/Group/SubGroup/Project.Special"
	resp, err := client.HTTP.ListPullsWithResponse(t.Context(), &generated.ListPullsParams{
		Repo: &repo,
	})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode())
	require.NotNil(resp.JSON200)
	require.Len(*resp.JSON200, 1)
	assert.Equal("group/subgroup", (*resp.JSON200)[0].RepoOwner)
	assert.Equal("project.special", (*resp.JSON200)[0].RepoName)
}

func TestAPIListIssuesIncludesLabels(t *testing.T) {
	require := require.New(t)
	srv, database := setupTestServer(t)
	seedIssueWithLabels(t, database, "acme", "widget", 5, "open", []db.Label{{
		Name:      "triage",
		Color:     "fbca04",
		IsDefault: false,
	}})
	client := setupTestClient(t, srv)

	resp, err := client.HTTP.ListIssuesWithResponse(t.Context(), nil)
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode())
	require.NotNil(resp.JSON200)
	require.Len(*resp.JSON200, 1)
	require.NotNil((*resp.JSON200)[0].Labels)
	require.Equal([]generated.Label{{
		Name:      "triage",
		Color:     "fbca04",
		IsDefault: false,
	}}, *(*resp.JSON200)[0].Labels)
}

func TestAPIGetIssueAcceptsMixedCaseRepoPath(t *testing.T) {
	require := require.New(t)
	srv, database := setupTestServer(t)
	seedIssue(t, database, "acme", "widget", 5, "open")
	client := setupTestClient(t, srv)

	resp, err := client.HTTP.GetIssueWithResponse(
		t.Context(), "gh", "Acme", "Widget", 5,
	)
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode())
	require.NotNil(resp.JSON200)
	require.Equal("acme", resp.JSON200.RepoOwner)
	require.Equal("widget", resp.JSON200.RepoName)
}

func TestAPIListIssuesAcceptsMixedCaseRepoFilter(t *testing.T) {
	require := require.New(t)
	srv, database := setupTestServer(t)
	seedIssue(t, database, "acme", "widget", 5, "open")
	client := setupTestClient(t, srv)

	repo := "Acme/Widget"
	resp, err := client.HTTP.ListIssuesWithResponse(
		t.Context(), &generated.ListIssuesParams{Repo: &repo},
	)
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode())
	require.NotNil(resp.JSON200)
	require.Len(*resp.JSON200, 1)
	require.Equal("acme", (*resp.JSON200)[0].RepoOwner)
	require.Equal("widget", (*resp.JSON200)[0].RepoName)
}

func TestAPIListIssuesAcceptsHostQualifiedRepoFilter(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)

	srv, database := setupTestServer(t)
	seedIssueOnHost(t, database, "github.com", "acme", "widget", 5, "open", "GitHub issue")
	seedIssueOnHost(t, database, "ghe.example.com", "acme", "widget", 7, "open", "Enterprise issue")
	client := setupTestClient(t, srv)

	repo := "ghe.example.com/acme/widget"
	resp, err := client.HTTP.ListIssuesWithResponse(
		t.Context(), &generated.ListIssuesParams{Repo: &repo},
	)
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode())
	require.NotNil(resp.JSON200)
	require.Len(*resp.JSON200, 1)
	assert.Equal("ghe.example.com", (*resp.JSON200)[0].PlatformHost)
	assert.Equal("acme", (*resp.JSON200)[0].RepoOwner)
	assert.Equal("widget", (*resp.JSON200)[0].RepoName)
	assert.EqualValues(7, (*resp.JSON200)[0].Number)
}

func TestAPIListIssuesFiltersHostedNestedRepoPath(t *testing.T) {
	assert := Assert.New(t)
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

	repo := "ghe.example.com/Group/SubGroup/Project.Special"
	resp, err := client.HTTP.ListIssuesWithResponse(
		t.Context(), &generated.ListIssuesParams{Repo: &repo},
	)
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode())
	require.NotNil(resp.JSON200)
	require.Len(*resp.JSON200, 1)
	assert.Equal("ghe.example.com", (*resp.JSON200)[0].PlatformHost)
	assert.Equal("group/subgroup", (*resp.JSON200)[0].RepoOwner)
	assert.Equal("project.special", (*resp.JSON200)[0].RepoName)
	assert.EqualValues(1, (*resp.JSON200)[0].Number)
}

func TestAPIGetIssueUsesPlatformHostQuery(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)

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
			{Owner: "acme", Name: "widget", PlatformHost: "github.com"},
			{Owner: "acme", Name: "widget", PlatformHost: "ghe.example.com"},
		},
		time.Minute,
		nil,
		nil,
	)
	t.Cleanup(syncer.Stop)

	srv := New(
		database, syncer, nil, "/", nil, ServerOptions{},
	)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(
			context.Background(), 5*time.Second,
		)
		defer cancel()
		_ = srv.Shutdown(ctx)
	})

	req := httptest.NewRequest(
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
	require := require.New(t)
	assert := Assert.New(t)
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
			{Owner: "acme", Name: "widget", PlatformHost: "github.com"},
			{Owner: "acme", Name: "widget", PlatformHost: "ghe.example.com"},
		},
		time.Minute,
		nil,
		nil,
	)
	t.Cleanup(syncer.Stop)

	srv := New(
		database, syncer, nil, "/", nil, ServerOptions{},
	)
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(
			context.Background(), 5*time.Second,
		)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	})

	req := httptest.NewRequest(
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

	githubRepo, err := database.GetRepoByHostOwnerName(
		ctx, "github.com", "acme", "widget",
	)
	require.NoError(err)
	require.NotNil(githubRepo)
	githubIssue, err := database.GetIssueByRepoIDAndNumber(
		ctx, githubRepo.ID, 7,
	)
	require.NoError(err)
	require.NotNil(githubIssue)
	assert.Equal("GitHub stale issue", githubIssue.Title)

	ghesRepo, err := database.GetRepoByHostOwnerName(
		ctx, "ghe.example.com", "acme", "widget",
	)
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
	require := require.New(t)
	assert := Assert.New(t)
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
			{Owner: "acme", Name: "widget", PlatformHost: "github.com"},
			{Owner: "acme", Name: "widget", PlatformHost: "ghe.example.com"},
		},
		time.Minute,
		nil,
		nil,
	)
	t.Cleanup(syncer.Stop)

	srv := New(
		database, syncer, nil, "/", nil, ServerOptions{},
	)
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(
			context.Background(), 5*time.Second,
		)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	})

	client := setupTestClient(t, srv)
	resp, err := client.HTTP.SetIssueGithubStateOnHostWithResponse(
		ctx,
		"ghe.example.com",
		"gh",
		"acme",
		"widget",
		7,
		generated.SetIssueGithubStateOnHostJSONRequestBody{State: "closed"},
	)
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode())

	githubRepo, err := database.GetRepoByHostOwnerName(
		ctx, "github.com", "acme", "widget",
	)
	require.NoError(err)
	require.NotNil(githubRepo)
	githubIssue, err := database.GetIssueByRepoIDAndNumber(
		ctx, githubRepo.ID, 7,
	)
	require.NoError(err)
	require.NotNil(githubIssue)
	assert.Equal("open", githubIssue.State)

	ghesRepo, err := database.GetRepoByHostOwnerName(
		ctx, "ghe.example.com", "acme", "widget",
	)
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
	require := require.New(t)
	assert := Assert.New(t)
	ctx := t.Context()

	mock := &mockGH{}
	srv, database := setupTestServerWithMock(t, mock)
	client := setupTestClient(t, srv)

	// Seed DB directly — same shape as GraphQL sync output.
	repoID, err := database.UpsertRepo(ctx, db.GitHubRepoIdentity("github.com", "acme", "widget"))
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
	resp, err := client.HTTP.ListIssuesWithResponse(ctx, nil)
	require.NoError(err)
	require.Equal(200, resp.StatusCode())
	require.NotNil(resp.JSON200)
	require.Len(*resp.JSON200, 1)

	apiIssue := (*resp.JSON200)[0]
	assert.Equal(int64(60), apiIssue.Number)
	assert.Equal("GraphQL synced issue", apiIssue.Title)
	assert.Equal("testuser", apiIssue.Author)
	assert.Equal("open", apiIssue.State)
	require.NotNil(apiIssue.Labels)
	require.Len(*apiIssue.Labels, 1)
	assert.Equal("bug", (*apiIssue.Labels)[0].Name)

	// Verify via GetIssue API
	detailResp, err := client.HTTP.GetIssueWithResponse(
		ctx, "gh", "acme", "widget", 60,
	)
	require.NoError(err)
	require.Equal(200, detailResp.StatusCode())
	require.NotNil(detailResp.JSON200)
	assert.Equal("Synced via GraphQL", detailResp.JSON200.Issue.Body)
	assert.Equal(int64(1), detailResp.JSON200.Issue.CommentCount)
}

// TestE2EGraphQLIssueSyncThroughAPI is a full-stack test that runs the
// real GraphQL issue sync path against a mocked GraphQL HTTP backend
// with real SQLite, then verifies the resulting issue data through
// the HTTP API. Exercises: GraphQL HTTP → adapter → NormalizeIssue →
// UpsertIssue → HTTP API handler → JSON response.
func TestE2EGraphQLIssueSyncThroughAPI(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)
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
			"author":{"login":"ivy"},
			"createdAt":"` + now + `",
			"updatedAt":"` + now + `",
			"closedAt":null,
			"labels":{"nodes":[{"name":"bug","color":"d73a4a","description":"","isDefault":false}]},
			"comments":{"totalCount":1,"nodes":[{"databaseId":801,"author":{"login":"judy"},"body":"full stack comment","createdAt":"` + now + `","updatedAt":"` + now + `"}],"pageInfo":{"hasNextPage":false,"endCursor":""}}
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
	srv, _ := setupTestServerWithMock(t, mock)

	// Wire a real GraphQLFetcher pointing at the mock GraphQL server
	// into the syncer.
	gqlClient := githubv4.NewEnterpriseClient(gqlSrv.URL, gqlSrv.Client())
	srv.syncer.SetFetchers(map[string]*ghclient.GraphQLFetcher{
		"github.com": ghclient.NewGraphQLFetcherWithClient(gqlClient, nil),
	})

	// Trigger the real sync pipeline.
	srv.syncer.RunOnce(ctx)

	// Verify through the HTTP API that issue data flowed end-to-end.
	client := setupTestClient(t, srv)

	listResp, err := client.HTTP.ListIssuesWithResponse(ctx, nil)
	require.NoError(err)
	require.Equal(200, listResp.StatusCode())
	require.NotNil(listResp.JSON200)
	require.Len(*listResp.JSON200, 1)

	apiIssue := (*listResp.JSON200)[0]
	assert.Equal(int64(80), apiIssue.Number)
	assert.Equal("Full stack GraphQL issue", apiIssue.Title)
	assert.Equal("ivy", apiIssue.Author)
	assert.Equal("open", apiIssue.State)
	require.NotNil(apiIssue.Labels)
	require.Len(*apiIssue.Labels, 1)
	assert.Equal("bug", (*apiIssue.Labels)[0].Name)

	detailResp, err := client.HTTP.GetIssueWithResponse(
		ctx, "gh", "acme", "widget", 80,
	)
	require.NoError(err)
	require.Equal(200, detailResp.StatusCode())
	require.NotNil(detailResp.JSON200)
	assert.Equal("Synced through the HTTP API", detailResp.JSON200.Issue.Body)
	assert.Equal(int64(1), detailResp.JSON200.Issue.CommentCount)
}

func TestE2ELargeRepoSkipsGraphQLAndUsesConditionalPRDetail(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)
	ctx := t.Context()

	unchangedAt := time.Date(2026, 4, 12, 10, 0, 0, 0, time.UTC)
	detailFetchedAt := time.Date(2026, 4, 12, 11, 0, 0, 0, time.UTC)
	changedAt := time.Date(2026, 4, 12, 12, 0, 0, 0, time.UTC)

	buildPR := func(number int, updatedAt time.Time, title string) *gh.PullRequest {
		id := int64(number * 1000)
		state := "open"
		url := fmt.Sprintf("https://github.com/acme/widget/pull/%d", number)
		author := "alice"
		headSHA := fmt.Sprintf("head-%d", number)
		headRef := fmt.Sprintf("feature-%d", number)
		baseRef := "main"
		created := gh.Timestamp{Time: unchangedAt}
		updated := gh.Timestamp{Time: updatedAt}
		comments := 1
		return &gh.PullRequest{
			ID:        &id,
			Number:    &number,
			State:     &state,
			Title:     &title,
			HTMLURL:   &url,
			User:      &gh.User{Login: &author},
			CreatedAt: &created,
			UpdatedAt: &updated,
			Comments:  &comments,
			Head:      &gh.PullRequestBranch{SHA: &headSHA, Ref: &headRef},
			Base:      &gh.PullRequestBranch{Ref: &baseRef},
		}
	}

	openPRs := make([]*gh.PullRequest, 0, 100)
	for number := 1; number <= 100; number++ {
		updatedAt := unchangedAt
		title := fmt.Sprintf("existing PR %d", number)
		if number == 1 {
			updatedAt = changedAt
			title = "changed PR from list"
		}
		openPRs = append(openPRs, buildPR(number, updatedAt, title))
	}

	var conditionalCalls atomic.Int32
	var graphQLPRCalls atomic.Int32
	mock := &mockGH{
		listOpenPullRequestsFn: func(_ context.Context, _, _ string) ([]*gh.PullRequest, error) {
			return openPRs, nil
		},
		listOpenIssuesFn: func(_ context.Context, _, _ string) ([]*gh.Issue, error) {
			return nil, &gh.ErrorResponse{
				Response: &http.Response{StatusCode: http.StatusNotModified},
			}
		},
		getPullRequestIfChangedFn: func(_ context.Context, _, _ string, number int, etag string) (*gh.PullRequest, string, bool, error) {
			conditionalCalls.Add(1)
			require.Equal(1, number)
			require.Equal(`"etag-v1"`, etag)
			return buildPR(number, changedAt, "changed PR detail"), `"etag-v2"`, false, nil
		},
		listIssueCommentsFn: func(_ context.Context, _, _ string, number int) ([]*gh.IssueComment, error) {
			if number != 1 {
				return nil, nil
			}
			id := int64(9001)
			body := "detail comment"
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
		if bytes.Contains(body, []byte("pullRequests")) {
			graphQLPRCalls.Add(1)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"errors":[{"message":"bulk PR fetch should be skipped"}]}`))
	}))
	defer gqlSrv.Close()

	database := dbtest.Open(t)
	repoID, err := database.UpsertRepo(ctx, db.GitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	for number := 1; number <= 100; number++ {
		_, err := database.UpsertMergeRequest(ctx, &db.MergeRequest{
			RepoID:          repoID,
			PlatformID:      int64(number * 1000),
			Number:          number,
			URL:             fmt.Sprintf("https://github.com/acme/widget/pull/%d", number),
			Title:           fmt.Sprintf("existing PR %d", number),
			Author:          "alice",
			State:           "open",
			HeadBranch:      fmt.Sprintf("feature-%d", number),
			BaseBranch:      "main",
			PlatformHeadSHA: fmt.Sprintf("head-%d", number),
			CreatedAt:       unchangedAt,
			UpdatedAt:       unchangedAt,
			LastActivityAt:  unchangedAt,
			DetailFetchedAt: &detailFetchedAt,
		})
		require.NoError(err)
	}
	require.NoError(database.UpsertHTTPEtag(
		ctx, "github", "github.com", "acme", "widget",
		"pull_request", 1, `"etag-v1"`,
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

	srv := New(database, syncer, nil, "/", nil, ServerOptions{})
	t.Cleanup(func() { gracefulShutdown(t, srv) })

	srv.syncer.RunOnce(ctx)

	assert.Zero(int(graphQLPRCalls.Load()),
		"large existing repo refresh should not bulk-fetch PRs through GraphQL")
	assert.Equal(int32(1), conditionalCalls.Load(),
		"only the changed PR should run a conditional detail fetch")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/pulls/gh/acme/widget/1", nil)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	require.Equal(http.StatusOK, rr.Code)

	var detailResp mergeRequestDetailResponse
	require.NoError(json.Unmarshal(rr.Body.Bytes(), &detailResp))
	require.NotNil(detailResp.MergeRequest)
	assert.Equal("changed PR detail", detailResp.MergeRequest.Title)
	assert.Equal(1, detailResp.MergeRequest.CommentCount)
	require.Len(detailResp.Events, 1)
	assert.Equal("detail comment", detailResp.Events[0].Body)

	etag, err := database.GetHTTPEtag(
		ctx, "github", "github.com", "acme", "widget",
		"pull_request", 1,
	)
	require.NoError(err)
	assert.Equal(`"etag-v2"`, etag)
}

func TestE2ELargeRepoSkipsGraphQLAndUsesConditionalIssueDetail(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)
	ctx := t.Context()

	unchangedAt := time.Date(2026, 4, 12, 10, 0, 0, 0, time.UTC)
	detailFetchedAt := time.Date(2026, 4, 12, 11, 0, 0, 0, time.UTC)
	changedAt := time.Date(2026, 4, 12, 12, 0, 0, 0, time.UTC)

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
	repoID, err := database.UpsertRepo(ctx, db.GitHubRepoIdentity("github.com", "acme", "widget"))
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

	srv := New(database, syncer, nil, "/", nil, ServerOptions{})
	t.Cleanup(func() { gracefulShutdown(t, srv) })

	srv.syncer.RunOnce(ctx)

	assert.Zero(int(graphQLIssueCalls.Load()),
		"large existing repo refresh should not bulk-fetch issues through GraphQL")
	assert.Equal(int32(1), conditionalCalls.Load(),
		"only the missing-detail issue should run a conditional detail fetch")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/issues/gh/acme/widget/1", nil)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	require.Equal(http.StatusOK, rr.Code)

	var detailResp issueDetailResponse
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
	require := require.New(t)
	assert := Assert.New(t)
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
	srv, database := setupTestServerWithMock(t, mock)

	// Pre-seed DB with a stale CommentCount (5). REST fallback fails,
	// so UpsertIssue's value is what survives. With the bug, it's 5.
	// Without the bug, it's TotalCount=42.
	//
	// The pre-seed UpdatedAt must be strictly older than the
	// GraphQL mock's updatedAt (`now` above). UpsertIssue's
	// stale-snapshot guard skips the update when
	// excluded.updated_at < middleman_issues.updated_at, so if
	// `stale` rolls forward past `now` (common under the race
	// detector's slower execution) the fresh GraphQL data would be
	// blocked and the assertion below would read back the stale 5
	// — a test-only flake, not a production bug.
	repoID, err := database.UpsertRepo(ctx, db.GitHubRepoIdentity("github.com", "acme", "widget"))
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
	srv.syncer.SetFetchers(map[string]*ghclient.GraphQLFetcher{
		"github.com": ghclient.NewGraphQLFetcherWithClient(gqlClient, nil),
	})

	srv.syncer.RunOnce(ctx)

	// API must expose GraphQL TotalCount (42), not stale DB (5).
	// With the preservation bug, count would remain 5.
	client := setupTestClient(t, srv)
	detailResp, err := client.HTTP.GetIssueWithResponse(
		ctx, "gh", "acme", "widget", 90,
	)
	require.NoError(err)
	require.Equal(200, detailResp.StatusCode())
	require.NotNil(detailResp.JSON200)
	assert.Equal(int64(42), detailResp.JSON200.Issue.CommentCount)
}

func TestE2EPRDetailRefreshesEditedCommentBody(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)
	ctx := t.Context()

	now := time.Date(2026, 4, 12, 14, 0, 0, 0, time.UTC)
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

	srv := New(database, syncer, nil, "/", nil, ServerOptions{})
	t.Cleanup(func() { gracefulShutdown(t, srv) })
	client := setupTestClient(t, srv)

	srv.syncer.RunOnce(ctx)

	firstResp, err := client.HTTP.GetPullWithResponse(
		ctx, "gh", "acme", "widget", int64(prNumber),
	)
	require.NoError(err)
	require.Equal(http.StatusOK, firstResp.StatusCode())
	require.NotNil(firstResp.JSON200)
	require.NotNil(firstResp.JSON200.Events)
	require.Len(*firstResp.JSON200.Events, 1)
	assert.Equal("original body", (*firstResp.JSON200.Events)[0].Body)

	editedBody := "edited body"
	mockComments = []*gh.IssueComment{{
		ID:        &commentID,
		Body:      &editedBody,
		User:      &gh.User{Login: &commentAuthor},
		CreatedAt: &gh.Timestamp{Time: commentCreatedAt},
		UpdatedAt: &gh.Timestamp{Time: now.Add(4 * time.Minute)},
	}}

	srv.syncer.RunOnce(ctx)

	secondResp, err := client.HTTP.GetPullWithResponse(
		ctx, "gh", "acme", "widget", int64(prNumber),
	)
	require.NoError(err)
	require.Equal(http.StatusOK, secondResp.StatusCode())
	require.NotNil(secondResp.JSON200)
	require.NotNil(secondResp.JSON200.Events)
	require.Len(*secondResp.JSON200.Events, 1)
	assert.Equal("edited body", (*secondResp.JSON200.Events)[0].Body)
}

func TestE2EPRDetailRemovesDeletedCommentWhenPRListIsUnchanged(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)
	ctx := t.Context()

	now := time.Date(2026, 4, 12, 14, 0, 0, 0, time.UTC)
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

	srv := New(database, syncer, nil, "/", nil, ServerOptions{})
	t.Cleanup(func() { gracefulShutdown(t, srv) })
	client := setupTestClient(t, srv)

	srv.syncer.RunOnce(ctx)

	firstResp, err := client.HTTP.GetPullWithResponse(
		ctx, "gh", "acme", "widget", int64(prNumber),
	)
	require.NoError(err)
	require.Equal(http.StatusOK, firstResp.StatusCode())
	require.NotNil(firstResp.JSON200)
	require.Equal(int64(1), firstResp.JSON200.MergeRequest.CommentCount)
	require.Equal(commentCreatedAt.UTC(), firstResp.JSON200.MergeRequest.LastActivityAt.UTC())
	require.NotNil(firstResp.JSON200.Events)
	require.Len(*firstResp.JSON200.Events, 1)
	assert.Equal("body to remove", (*firstResp.JSON200.Events)[0].Body)

	mockComments = []*gh.IssueComment{}

	srv.syncer.RunOnce(ctx)

	secondResp, err := client.HTTP.GetPullWithResponse(
		ctx, "gh", "acme", "widget", int64(prNumber),
	)
	require.NoError(err)
	require.Equal(http.StatusOK, secondResp.StatusCode())
	require.NotNil(secondResp.JSON200)
	require.Equal(int64(0), secondResp.JSON200.MergeRequest.CommentCount)
	require.Equal(now.UTC(), secondResp.JSON200.MergeRequest.LastActivityAt.UTC())
	require.NotNil(secondResp.JSON200.Events)
	require.Empty(*secondResp.JSON200.Events)
}

func TestE2EPRDetailRemovesDeletedCommentWhenAnotherPRChanges(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)
	ctx := t.Context()

	now := time.Date(2026, 4, 12, 15, 0, 0, 0, time.UTC)
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

	srv := New(database, syncer, nil, "/", nil, ServerOptions{})
	t.Cleanup(func() { gracefulShutdown(t, srv) })
	client := setupTestClient(t, srv)

	srv.syncer.RunOnce(ctx)

	firstResp, err := client.HTTP.GetPullWithResponse(
		ctx, "gh", "acme", "widget", int64(targetNumber),
	)
	require.NoError(err)
	require.Equal(http.StatusOK, firstResp.StatusCode())
	require.NotNil(firstResp.JSON200)
	require.Equal(int64(1), firstResp.JSON200.MergeRequest.CommentCount)
	require.NotNil(firstResp.JSON200.Events)
	require.Len(*firstResp.JSON200.Events, 1)
	assert.Equal("target comment", (*firstResp.JSON200.Events)[0].Body)

	targetComments = []*gh.IssueComment{}

	srv.syncer.RunOnce(ctx)

	secondResp, err := client.HTTP.GetPullWithResponse(
		ctx, "gh", "acme", "widget", int64(targetNumber),
	)
	require.NoError(err)
	require.Equal(http.StatusOK, secondResp.StatusCode())
	require.NotNil(secondResp.JSON200)
	require.Equal(int64(0), secondResp.JSON200.MergeRequest.CommentCount)
	require.NotNil(secondResp.JSON200.Events)
	require.Empty(*secondResp.JSON200.Events)
}

func TestE2EIssueDetailRefreshesEditedCommentBody(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)
	ctx := t.Context()

	now := time.Date(2026, 4, 12, 14, 0, 0, 0, time.UTC)
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

	srv := New(database, syncer, nil, "/", nil, ServerOptions{})
	t.Cleanup(func() { gracefulShutdown(t, srv) })
	client := setupTestClient(t, srv)

	srv.syncer.RunOnce(ctx)

	firstResp, err := client.HTTP.GetIssueWithResponse(
		ctx, "gh", "acme", "widget", int64(issueNumber),
	)
	require.NoError(err)
	require.Equal(http.StatusOK, firstResp.StatusCode())
	require.NotNil(firstResp.JSON200)
	require.NotNil(firstResp.JSON200.Events)
	require.Len(*firstResp.JSON200.Events, 1)
	assert.Equal("original issue body", (*firstResp.JSON200.Events)[0].Body)

	editedBody := "edited issue body"
	mockComments = []*gh.IssueComment{{
		ID:        &commentID,
		Body:      &editedBody,
		User:      &gh.User{Login: &commentAuthor},
		CreatedAt: &gh.Timestamp{Time: commentCreatedAt},
		UpdatedAt: &gh.Timestamp{Time: now.Add(4 * time.Minute)},
	}}

	srv.syncer.RunOnce(ctx)

	secondResp, err := client.HTTP.GetIssueWithResponse(
		ctx, "gh", "acme", "widget", int64(issueNumber),
	)
	require.NoError(err)
	require.Equal(http.StatusOK, secondResp.StatusCode())
	require.NotNil(secondResp.JSON200)
	require.NotNil(secondResp.JSON200.Events)
	require.Len(*secondResp.JSON200.Events, 1)
	assert.Equal("edited issue body", (*secondResp.JSON200.Events)[0].Body)
}

func TestE2EIssueDetailRemovesDeletedCommentWhenIssueListIsUnchanged(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)
	ctx := t.Context()

	now := time.Date(2026, 4, 12, 14, 0, 0, 0, time.UTC)
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

	srv := New(database, syncer, nil, "/", nil, ServerOptions{})
	t.Cleanup(func() { gracefulShutdown(t, srv) })
	client := setupTestClient(t, srv)

	srv.syncer.RunOnce(ctx)

	firstResp, err := client.HTTP.GetIssueWithResponse(
		ctx, "gh", "acme", "widget", int64(issueNumber),
	)
	require.NoError(err)
	require.Equal(http.StatusOK, firstResp.StatusCode())
	require.NotNil(firstResp.JSON200)
	require.Equal(int64(1), firstResp.JSON200.Issue.CommentCount)
	require.Equal(commentCreatedAt.UTC(), firstResp.JSON200.Issue.LastActivityAt.UTC())
	require.NotNil(firstResp.JSON200.Events)
	require.Len(*firstResp.JSON200.Events, 1)
	assert.Equal("issue body to remove", (*firstResp.JSON200.Events)[0].Body)

	mockComments = []*gh.IssueComment{}

	srv.syncer.RunOnce(ctx)

	secondResp, err := client.HTTP.GetIssueWithResponse(
		ctx, "gh", "acme", "widget", int64(issueNumber),
	)
	require.NoError(err)
	require.Equal(http.StatusOK, secondResp.StatusCode())
	require.NotNil(secondResp.JSON200)
	require.Equal(int64(0), secondResp.JSON200.Issue.CommentCount)
	require.Equal(now.UTC(), secondResp.JSON200.Issue.LastActivityAt.UTC())
	require.NotNil(secondResp.JSON200.Events)
	require.Empty(*secondResp.JSON200.Events)
}

func TestE2EIssueDetailRemovesDeletedCommentWhenAnotherIssueChanges(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)
	ctx := t.Context()

	now := time.Date(2026, 4, 12, 16, 0, 0, 0, time.UTC)
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

	srv := New(database, syncer, nil, "/", nil, ServerOptions{})
	t.Cleanup(func() { gracefulShutdown(t, srv) })
	client := setupTestClient(t, srv)

	srv.syncer.RunOnce(ctx)

	firstResp, err := client.HTTP.GetIssueWithResponse(
		ctx, "gh", "acme", "widget", int64(targetNumber),
	)
	require.NoError(err)
	require.Equal(http.StatusOK, firstResp.StatusCode())
	require.NotNil(firstResp.JSON200)
	require.Equal(int64(1), firstResp.JSON200.Issue.CommentCount)
	require.NotNil(firstResp.JSON200.Events)
	require.Len(*firstResp.JSON200.Events, 1)
	assert.Equal("target issue comment", (*firstResp.JSON200.Events)[0].Body)

	targetComments = []*gh.IssueComment{}

	srv.syncer.RunOnce(ctx)

	secondResp, err := client.HTTP.GetIssueWithResponse(
		ctx, "gh", "acme", "widget", int64(targetNumber),
	)
	require.NoError(err)
	require.Equal(http.StatusOK, secondResp.StatusCode())
	require.NotNil(secondResp.JSON200)
	require.Equal(int64(0), secondResp.JSON200.Issue.CommentCount)
	require.NotNil(secondResp.JSON200.Events)
	require.Empty(*secondResp.JSON200.Events)
}

func TestE2EPRDetailRemovesDeletedCommentOnFullRefresh(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)
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
	currentUpdatedAt := now
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

	srv := New(database, syncer, nil, "/", nil, ServerOptions{})
	t.Cleanup(func() { gracefulShutdown(t, srv) })
	client := setupTestClient(t, srv)

	require.NoError(srv.syncer.SyncMR(ctx, "acme", "widget", prNumber))

	firstResp, err := client.HTTP.GetPullWithResponse(
		ctx, "gh", "acme", "widget", int64(prNumber),
	)
	require.NoError(err)
	require.Equal(http.StatusOK, firstResp.StatusCode())
	require.NotNil(firstResp.JSON200)
	require.Equal(int64(1), firstResp.JSON200.MergeRequest.CommentCount)
	require.Equal(commentCreatedAt.UTC(), firstResp.JSON200.MergeRequest.LastActivityAt.UTC())
	require.NotNil(firstResp.JSON200.Events)
	require.Len(*firstResp.JSON200.Events, 1)
	assert.Equal("comment removed on full refresh", (*firstResp.JSON200.Events)[0].Body)

	currentUpdatedAt = now.Add(time.Minute)
	currentComments = []*gh.IssueComment{}

	require.NoError(srv.syncer.SyncMR(ctx, "acme", "widget", prNumber))

	secondResp, err := client.HTTP.GetPullWithResponse(
		ctx, "gh", "acme", "widget", int64(prNumber),
	)
	require.NoError(err)
	require.Equal(http.StatusOK, secondResp.StatusCode())
	require.NotNil(secondResp.JSON200)
	require.Equal(int64(0), secondResp.JSON200.MergeRequest.CommentCount)
	require.Equal(currentUpdatedAt.UTC(), secondResp.JSON200.MergeRequest.LastActivityAt.UTC())
	require.NotNil(secondResp.JSON200.Events)
	require.Empty(*secondResp.JSON200.Events)
}

func TestE2EIssueDetailRemovesDeletedCommentOnFullRefresh(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)
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

	srv := New(database, syncer, nil, "/", nil, ServerOptions{})
	t.Cleanup(func() { gracefulShutdown(t, srv) })
	client := setupTestClient(t, srv)

	require.NoError(srv.syncer.SyncIssue(ctx, "acme", "widget", issueNumber))

	firstResp, err := client.HTTP.GetIssueWithResponse(
		ctx, "gh", "acme", "widget", int64(issueNumber),
	)
	require.NoError(err)
	require.Equal(http.StatusOK, firstResp.StatusCode())
	require.NotNil(firstResp.JSON200)
	require.Equal(int64(1), firstResp.JSON200.Issue.CommentCount)
	require.Equal(commentCreatedAt.UTC(), firstResp.JSON200.Issue.LastActivityAt.UTC())
	require.NotNil(firstResp.JSON200.Events)
	require.Len(*firstResp.JSON200.Events, 1)
	assert.Equal("issue comment removed on full refresh", (*firstResp.JSON200.Events)[0].Body)

	currentUpdatedAt = now.Add(time.Minute)
	currentComments = []*gh.IssueComment{}

	require.NoError(srv.syncer.SyncIssue(ctx, "acme", "widget", issueNumber))

	secondResp, err := client.HTTP.GetIssueWithResponse(
		ctx, "gh", "acme", "widget", int64(issueNumber),
	)
	require.NoError(err)
	require.Equal(http.StatusOK, secondResp.StatusCode())
	require.NotNil(secondResp.JSON200)
	require.Equal(int64(0), secondResp.JSON200.Issue.CommentCount)
	require.Equal(currentUpdatedAt.UTC(), secondResp.JSON200.Issue.LastActivityAt.UTC())
	require.NotNil(secondResp.JSON200.Events)
	require.Empty(*secondResp.JSON200.Events)
}

func TestE2EIssueDetailRemovesDeletedCommentOnGraphQLBulkSync(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)
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

	srv := New(database, syncer, nil, "/", nil, ServerOptions{})
	t.Cleanup(func() { gracefulShutdown(t, srv) })
	client := setupTestClient(t, srv)

	srv.syncer.RunOnce(ctx)

	firstResp, err := client.HTTP.GetIssueWithResponse(
		ctx, "gh", "acme", "widget", int64(issueNumber),
	)
	require.NoError(err)
	require.Equal(http.StatusOK, firstResp.StatusCode())
	require.NotNil(firstResp.JSON200)
	require.Equal(int64(1), firstResp.JSON200.Issue.CommentCount)
	require.Equal(now.UTC(), firstResp.JSON200.Issue.LastActivityAt.UTC())
	require.NotNil(firstResp.JSON200.Events)
	require.Len(*firstResp.JSON200.Events, 1)
	assert.Equal("bulk comment removed", (*firstResp.JSON200.Events)[0].Body)

	currentUpdatedAt = secondUpdatedAt
	currentCommentJSON = `{"totalCount":0,"nodes":[],"pageInfo":{"hasNextPage":false,"endCursor":""}}`

	srv.syncer.RunOnce(ctx)

	secondResp, err := client.HTTP.GetIssueWithResponse(
		ctx, "gh", "acme", "widget", int64(issueNumber),
	)
	require.NoError(err)
	require.Equal(http.StatusOK, secondResp.StatusCode())
	require.NotNil(secondResp.JSON200)
	require.Equal(int64(0), secondResp.JSON200.Issue.CommentCount)
	require.Equal(now.Add(time.Minute).UTC(), secondResp.JSON200.Issue.LastActivityAt.UTC())
	require.NotNil(secondResp.JSON200.Events)
	require.Empty(*secondResp.JSON200.Events)
}

func TestE2EPRDetailRemovesDeletedCommentOnGraphQLBulkSync(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)
	ctx := t.Context()

	now := time.Date(2026, 4, 13, 11, 30, 0, 0, time.UTC)
	firstUpdatedAt := now.Format(time.RFC3339)
	secondUpdatedAt := now.Add(time.Minute).Format(time.RFC3339)
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
				"createdAt":"` + firstUpdatedAt + `",
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

	srv, _ := setupTestServerWithMock(t, mock)
	gqlClient := githubv4.NewEnterpriseClient(gqlSrv.URL, gqlSrv.Client())
	srv.syncer.SetFetchers(map[string]*ghclient.GraphQLFetcher{
		"github.com": ghclient.NewGraphQLFetcherWithClient(gqlClient, nil),
	})
	client := setupTestClient(t, srv)

	srv.syncer.RunOnce(ctx)

	firstResp, err := client.HTTP.GetPullWithResponse(
		ctx, "gh", "acme", "widget", int64(prNumber),
	)
	require.NoError(err)
	require.Equal(http.StatusOK, firstResp.StatusCode())
	require.NotNil(firstResp.JSON200)
	require.Equal(int64(1), firstResp.JSON200.MergeRequest.CommentCount)
	require.Equal(now.Add(2*time.Minute).UTC(), firstResp.JSON200.MergeRequest.LastActivityAt.UTC())
	require.NotNil(firstResp.JSON200.Events)
	require.Len(*firstResp.JSON200.Events, 1)
	assert.Equal("bulk PR comment removed", (*firstResp.JSON200.Events)[0].Body)

	currentUpdatedAt = secondUpdatedAt
	currentCommentsJSON = `{"nodes":[],"pageInfo":{"hasNextPage":false,"endCursor":""}}`

	srv.syncer.RunOnce(ctx)

	secondResp, err := client.HTTP.GetPullWithResponse(
		ctx, "gh", "acme", "widget", int64(prNumber),
	)
	require.NoError(err)
	require.Equal(http.StatusOK, secondResp.StatusCode())
	require.NotNil(secondResp.JSON200)
	require.Equal(int64(0), secondResp.JSON200.MergeRequest.CommentCount)
	require.Equal(now.Add(time.Minute).UTC(), secondResp.JSON200.MergeRequest.LastActivityAt.UTC())
	require.NotNil(secondResp.JSON200.Events)
	require.Empty(*secondResp.JSON200.Events)
}

// TestE2EGraphQLBulkSyncPersistsWorkflowApproval drives the periodic
// sync through the GraphQL bulk path and verifies the persisted
// workflow approval snapshot reaches the HTTP API. Regression test
// for the gap where fully-synced bulk PRs would mark detail_fetched_at
// without ever populating workflow_approval_checked_at, leaving the
// DB-only GET unable to surface the Approve workflows button.
func TestE2EGraphQLBulkSyncPersistsWorkflowApproval(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)
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

	srv, _ := setupTestServerWithMock(t, mock)
	gqlClient := githubv4.NewEnterpriseClient(gqlSrv.URL, gqlSrv.Client())
	srv.syncer.SetFetchers(map[string]*ghclient.GraphQLFetcher{
		"github.com": ghclient.NewGraphQLFetcherWithClient(gqlClient, nil),
	})
	client := setupTestClient(t, srv)

	srv.syncer.RunOnce(ctx)

	resp, err := client.HTTP.GetPullWithResponse(
		ctx, "gh", "acme", "widget", int64(prNumber),
	)
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode())
	require.NotNil(resp.JSON200)
	require.NotNil(resp.JSON200.WorkflowApproval)
	assert.True(resp.JSON200.WorkflowApproval.Checked,
		"GraphQL bulk path should persist a workflow approval snapshot")
	assert.True(resp.JSON200.WorkflowApproval.Required,
		"head SHA has a pending workflow run; button must be live")
	assert.Equal(int64(1), resp.JSON200.WorkflowApproval.Count)
}

// TestE2EGraphQLBulkSyncPersistsWorkflowApprovalForForkPR pins the
// fork-fallback matching path through the GraphQL bulk sync. The
// workflow run mock returns an empty PullRequests array (mirroring
// what GitHub returns for fork-triggered runs) and identifies the PR
// only by head repository full name and head branch. The GraphQL
// adapter never populates Head.Repo.FullName, so this exercises
// ParseHeadRepoFullName against the persisted clone URL; a regression
// removing that fallback would leave the Approve workflows button
// hidden for every fork PR synced through bulk.
func TestE2EGraphQLBulkSyncPersistsWorkflowApprovalForForkPR(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)
	ctx := t.Context()

	now := time.Date(2026, 4, 14, 10, 0, 0, 0, time.UTC)
	nowRFC3339 := now.Format(time.RFC3339)
	const prNumber = 174
	const prID int64 = 174000
	const headSHA = "abc123"
	const forkFullName = "ericdill/widget"
	const headRef = "feature/fork-pr"

	gqlSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if bytes.Contains(body, []byte("pullRequests")) {
			resp := `{"data":{"repository":{"pullRequests":{"nodes":[{
				"databaseId":174000,
				"number":174,
				"title":"Fork PR awaiting workflow approval",
				"state":"OPEN",
				"isDraft":false,
				"body":"",
				"url":"https://github.com/acme/widget/pull/174",
				"author":{"login":"ericdill"},
				"createdAt":"` + nowRFC3339 + `",
				"updatedAt":"` + nowRFC3339 + `",
				"mergedAt":null,
				"closedAt":null,
				"additions":1,
				"deletions":0,
				"mergeable":"MERGEABLE",
				"reviewDecision":"",
				"headRefName":"` + headRef + `",
				"baseRefName":"main",
				"headRefOid":"` + headSHA + `",
				"baseRefOid":"def456",
				"headRepository":{"url":"https://github.com/` + forkFullName + `"},
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
				Title:     new("Fork PR awaiting workflow approval"),
				State:     new("open"),
				HTMLURL:   new("https://github.com/acme/widget/pull/174"),
				User:      &gh.User{Login: new("ericdill")},
				CreatedAt: &prTime,
				UpdatedAt: &prTime,
				Head:      &gh.PullRequestBranch{Ref: new(headRef), SHA: new(headSHA)},
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
			// Fork-triggered runs return an empty PullRequests array.
			// The matcher must fall back to HeadRepository.FullName +
			// HeadBranch, identifying the PR via its persisted clone
			// URL.
			fullName := forkFullName
			branch := headRef
			return []*gh.WorkflowRun{{
				ID:             new(int64(7778)),
				HeadSHA:        new(headSHA),
				Event:          new("pull_request"),
				PullRequests:   nil,
				HeadRepository: &gh.Repository{FullName: &fullName},
				HeadBranch:     &branch,
			}}, nil
		},
	}

	srv, _ := setupTestServerWithMock(t, mock)
	gqlClient := githubv4.NewEnterpriseClient(gqlSrv.URL, gqlSrv.Client())
	srv.syncer.SetFetchers(map[string]*ghclient.GraphQLFetcher{
		"github.com": ghclient.NewGraphQLFetcherWithClient(gqlClient, nil),
	})
	client := setupTestClient(t, srv)

	srv.syncer.RunOnce(ctx)

	resp, err := client.HTTP.GetPullWithResponse(
		ctx, "gh", "acme", "widget", int64(prNumber),
	)
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode())
	require.NotNil(resp.JSON200)
	require.NotNil(resp.JSON200.WorkflowApproval)
	assert.True(resp.JSON200.WorkflowApproval.Checked,
		"GraphQL bulk path should persist a workflow approval snapshot")
	assert.True(resp.JSON200.WorkflowApproval.Required,
		"fork PR must match via HeadRepository.FullName + HeadBranch fallback")
	assert.Equal(int64(1), resp.JSON200.WorkflowApproval.Count)
}

func TestE2EGraphQLBulkSyncKeepsNewestCICheckBySuiteCreatedAt(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)
	ctx := context.Background()

	older := time.Date(2026, 4, 9, 12, 0, 0, 0, time.UTC)
	newer := older.Add(10 * time.Minute)
	prUpdatedAt := newer.Add(time.Minute)
	prID := int64(173100)
	prNumber := 174
	prTitle := "GraphQL check dedupe"
	prState := "open"
	prURL := "https://github.com/acme/widget/pull/174"
	prAuthor := "alice"
	headRef := "feature/graphql-check-dedupe"
	baseRef := "main"
	headSHA := "abc123"
	baseSHA := "def456"
	checkName := "build"
	oldCheckURL := "https://ci.example.com/runs/old"
	newCheckURL := "https://ci.example.com/runs/new"

	gqlSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if bytes.Contains(body, []byte("pullRequests")) {
			resp := `{"data":{"repository":{"pullRequests":{"nodes":[{
				"databaseId":173100,
				"number":174,
				"title":"GraphQL check dedupe",
				"state":"OPEN",
				"isDraft":false,
				"body":"",
				"url":"https://github.com/acme/widget/pull/174",
				"author":{"login":"alice"},
				"createdAt":"` + older.Format(time.RFC3339) + `",
				"updatedAt":"` + prUpdatedAt.Format(time.RFC3339) + `",
				"mergedAt":null,
				"closedAt":null,
				"additions":1,
				"deletions":0,
				"mergeable":"MERGEABLE",
				"reviewDecision":"",
				"headRefName":"feature/graphql-check-dedupe",
				"baseRefName":"main",
				"headRefOid":"abc123",
				"baseRefOid":"def456",
				"headRepository":{"url":"https://github.com/acme/widget"},
				"labels":{"nodes":[]},
				"comments":{"nodes":[],"pageInfo":{"hasNextPage":false,"endCursor":""}},
				"reviews":{"nodes":[],"pageInfo":{"hasNextPage":false,"endCursor":""}},
				"allCommits":{"nodes":[],"pageInfo":{"hasNextPage":false,"endCursor":""}},
				"lastCommit":{"nodes":[{"commit":{"statusCheckRollup":{"contexts":{"nodes":[
					{"__typename":"CheckRun","name":"build","status":"COMPLETED","conclusion":"FAILURE","detailsUrl":"https://ci.example.com/runs/old","startedAt":null,"completedAt":null,"checkSuite":{"createdAt":"` + older.Format(time.RFC3339) + `","app":{"name":"GitHub Actions"}}},
					{"__typename":"CheckRun","name":"build","status":"COMPLETED","conclusion":"SUCCESS","detailsUrl":"https://ci.example.com/runs/new","startedAt":null,"completedAt":null,"checkSuite":{"createdAt":"` + newer.Format(time.RFC3339) + `","app":{"name":"GitHub Actions"}}}
				],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}]}
			}],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}`
			_, _ = w.Write([]byte(resp))
			return
		}
		_, _ = w.Write([]byte(`{"data":{"repository":{"issues":{"nodes":[],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}`))
	}))
	defer gqlSrv.Close()

	prTime := gh.Timestamp{Time: prUpdatedAt}
	mock := &mockGH{
		listOpenPullRequestsFn: func(_ context.Context, _, _ string) ([]*gh.PullRequest, error) {
			return []*gh.PullRequest{{
				ID:        &prID,
				Number:    &prNumber,
				Title:     &prTitle,
				State:     &prState,
				HTMLURL:   &prURL,
				User:      &gh.User{Login: &prAuthor},
				CreatedAt: &gh.Timestamp{Time: older},
				UpdatedAt: &prTime,
				Head:      &gh.PullRequestBranch{Ref: &headRef, SHA: &headSHA},
				Base:      &gh.PullRequestBranch{Ref: &baseRef, SHA: &baseSHA},
			}}, nil
		},
		listOpenIssuesFn: func(_ context.Context, _, _ string) ([]*gh.Issue, error) {
			return nil, &gh.ErrorResponse{
				Response: &http.Response{StatusCode: http.StatusNotModified},
			}
		},
	}

	srv, _ := setupTestServerWithMock(t, mock)
	gqlClient := githubv4.NewEnterpriseClient(gqlSrv.URL, gqlSrv.Client())
	srv.syncer.SetFetchers(map[string]*ghclient.GraphQLFetcher{
		"github.com": ghclient.NewGraphQLFetcherWithClient(gqlClient, nil),
	})
	client := setupTestClient(t, srv)

	srv.syncer.RunOnce(ctx)

	resp, err := client.HTTP.GetPullWithResponse(
		ctx, "gh", "acme", "widget", int64(prNumber),
	)
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode())
	require.NotNil(resp.JSON200)
	require.NotNil(resp.JSON200.MergeRequest)
	require.Equal("success", resp.JSON200.MergeRequest.CIStatus)

	var checks []db.CICheck
	require.NoError(json.Unmarshal(
		[]byte(resp.JSON200.MergeRequest.CIChecksJSON),
		&checks,
	))
	require.Len(checks, 1)
	assert.Equal(checkName, checks[0].Name)
	assert.Equal("completed", checks[0].Status)
	assert.Equal("success", checks[0].Conclusion)
	assert.Equal(newCheckURL, checks[0].URL)
	assert.Equal("GitHub Actions", checks[0].App)
	assert.NotEqual(oldCheckURL, checks[0].URL)
}

func make422Error() error {
	return &gh.ErrorResponse{
		Response: &http.Response{StatusCode: http.StatusUnprocessableEntity},
		Message:  "Validation Failed",
	}
}

func TestAPISetIssueGitHubStateReturns404WhenNoClientConfigured(t *testing.T) {
	require := require.New(t)
	repos := []ghclient.RepoRef{{Owner: "acme", Name: "widget", PlatformHost: "ghe.corp.com"}}
	srv, database := setupTestServerWithRepos(t, &mockGH{}, repos)
	ctx := t.Context()

	repoID, err := database.UpsertRepo(ctx, db.GitHubRepoIdentity("ghe.corp.com", "acme", "widget"))
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
	resp, err := client.HTTP.SetIssueGithubStateWithResponse(
		ctx, "gh", "acme", "widget", 5,
		generated.SetIssueGithubStateJSONRequestBody{State: "closed"},
	)
	require.NoError(err)
	require.Equal(http.StatusNotFound, resp.StatusCode())
}

func TestAPIClosePR422NilFallbackPayloadDoesNotCorruptDB(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)
	mock := &mockGH{
		editPullRequestFn: func(_ context.Context, _, _ string, _ int, _ ghclient.EditPullRequestOpts) (*gh.PullRequest, error) {
			return nil, make422Error()
		},
		getPullRequestFn: func(_ context.Context, _, _ string, _ int) (*gh.PullRequest, error) {
			return nil, nil
		},
	}
	srv, database := setupTestServerWithMock(t, mock)
	seedPR(t, database, "acme", "widget", 1)
	before, err := database.GetMergeRequest(t.Context(), "acme", "widget", 1)
	require.NoError(err)
	require.NotNil(before)

	client := setupTestClient(t, srv)
	resp, err := client.HTTP.SetPrGithubStateWithResponse(
		t.Context(), "gh", "acme", "widget", 1,
		generated.SetPrGithubStateJSONRequestBody{State: "closed"},
	)
	require.NoError(err)
	require.Equal(http.StatusBadGateway, resp.StatusCode())

	after, err := database.GetMergeRequest(t.Context(), "acme", "widget", 1)
	require.NoError(err)
	require.NotNil(after)
	assert.Equal(before.State, after.State)
	assert.Equal(before.UpdatedAt, after.UpdatedAt)
	assert.Nil(after.ClosedAt)
}

func TestAPICloseIssue422NilFallbackPayloadDoesNotCorruptDB(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)
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
	before, err := database.GetIssue(t.Context(), "acme", "widget", 5)
	require.NoError(err)
	require.NotNil(before)

	client := setupTestClient(t, srv)
	resp, err := client.HTTP.SetIssueGithubStateWithResponse(
		t.Context(), "gh", "acme", "widget", 5,
		generated.SetIssueGithubStateJSONRequestBody{State: "closed"},
	)
	require.NoError(err)
	require.Equal(http.StatusBadGateway, resp.StatusCode())

	after, err := database.GetIssue(t.Context(), "acme", "widget", 5)
	require.NoError(err)
	require.NotNil(after)
	assert.Equal(before.State, after.State)
	assert.Equal(before.UpdatedAt, after.UpdatedAt)
	assert.Nil(after.ClosedAt)
}

func TestAPIClosePR422AlreadyClosed(t *testing.T) {
	require := require.New(t)
	// EditPullRequest returns 422, but re-fetch shows PR is already closed.
	// Should succeed since the requested state matches.
	state := "closed"
	mock := &mockGH{
		editPullRequestFn: func(_ context.Context, _, _ string, _ int, _ ghclient.EditPullRequestOpts) (*gh.PullRequest, error) {
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

	resp, err := client.HTTP.SetPrGithubStateWithResponse(
		t.Context(), "gh", "acme", "widget", 1,
		generated.SetPrGithubStateJSONRequestBody{State: "closed"},
	)
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode())

	pr, _ := database.GetMergeRequest(t.Context(), "acme", "widget", 1)
	require.Equal(db.MergeRequestStateClosed, pr.State)
}

// When MarkPullRequestReadyForReview returns (nil, nil) the handler
// must return 502 rather than claiming success with no PR payload.
func TestAPIReadyForReview502OnNilPR(t *testing.T) {
	require := require.New(t)
	mock := &mockGH{
		markReadyForReviewFn: func(_ context.Context, _, _ string, _ int) (*gh.PullRequest, error) {
			return nil, nil
		},
	}
	srv, database := setupTestServerWithMock(t, mock)
	seedPR(t, database, "acme", "widget", 1)
	client := setupTestClient(t, srv)

	resp, err := client.HTTP.MarkPullReadyForReviewWithResponse(
		t.Context(), "gh", "acme", "widget", 1,
	)
	require.NoError(err)
	require.Equal(http.StatusBadGateway, resp.StatusCode())
}

func TestAPIReadyForReviewReturnsUnderlyingErrorDetail(t *testing.T) {
	require := require.New(t)
	mock := &mockGH{
		markReadyForReviewFn: func(_ context.Context, _, _ string, _ int) (*gh.PullRequest, error) {
			return nil, errors.New("marking acme/widget#1 ready for review: draft review threads still pending")
		},
	}
	srv, database := setupTestServerWithMock(t, mock)
	seedPR(t, database, "acme", "widget", 1)
	client := setupTestClient(t, srv)

	resp, err := client.HTTP.MarkPullReadyForReviewWithResponse(
		t.Context(), "gh", "acme", "widget", 1,
	)
	require.NoError(err)
	require.Equal(http.StatusBadGateway, resp.StatusCode())
	require.NotNil(resp.ApplicationproblemJSONDefault)
	require.NotNil(resp.ApplicationproblemJSONDefault.Detail)
	require.Equal(
		"marking acme/widget#1 ready for review: draft review threads still pending",
		*resp.ApplicationproblemJSONDefault.Detail,
	)
}

func TestAPIReadyForReviewStaleStateRefreshesAndReturnsSuccess(t *testing.T) {
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

	pr, err := database.GetMergeRequest(t.Context(), "acme", "widget", 1)
	require.NoError(err)
	require.NotNil(pr)
	pr.IsDraft = true
	_, err = database.UpsertMergeRequest(t.Context(), pr)
	require.NoError(err)

	client := setupTestClient(t, srv)

	resp, err := client.HTTP.MarkPullReadyForReviewWithResponse(
		t.Context(), "gh", "acme", "widget", 1,
	)
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode())

	pr, err = database.GetMergeRequest(t.Context(), "acme", "widget", 1)
	require.NoError(err)
	require.NotNil(pr)
	require.False(pr.IsDraft)
}

func TestAPIReadyForReview404RefreshesStaleDraftState(t *testing.T) {
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

	resp, err := client.HTTP.MarkPullReadyForReviewWithResponse(
		t.Context(), "gh", "acme", "widget", 1,
	)
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode())

	pr, err := database.GetMergeRequest(t.Context(), "acme", "widget", 1)
	require.NoError(err)
	require.NotNil(pr)
	require.False(pr.IsDraft)
}

func TestAPIClosePR422Merged(t *testing.T) {
	// EditPullRequest returns 422, re-fetch shows PR is merged.
	// Should return 409.
	merged := "closed"
	mock := &mockGH{
		editPullRequestFn: func(_ context.Context, _, _ string, _ int, _ ghclient.EditPullRequestOpts) (*gh.PullRequest, error) {
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

	resp, err := client.HTTP.SetPrGithubStateWithResponse(
		t.Context(), "gh", "acme", "widget", 1,
		generated.SetPrGithubStateJSONRequestBody{State: "closed"},
	)
	require.NoError(t, err)
	require.Equal(t, http.StatusConflict, resp.StatusCode())
}

func TestResolveItem_PR(t *testing.T) {
	require := require.New(t)
	repos := []ghclient.RepoRef{{Owner: "acme", Name: "widget"}}
	srv, database := setupTestServerWithRepos(t, &mockGH{}, repos)
	seedPR(t, database, "acme", "widget", 42)
	client := setupTestClient(t, srv)

	resp, err := client.HTTP.ResolveRepoItemWithResponse(
		t.Context(), "gh", "acme", "widget", 42, nil,
	)
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode())
	require.NotNil(resp.JSON200)
	require.Equal("pr", resp.JSON200.ItemType)
	require.EqualValues(42, resp.JSON200.Number)
	require.True(resp.JSON200.RepoTracked)
}

func TestResolveItem_Issue(t *testing.T) {
	require := require.New(t)
	repos := []ghclient.RepoRef{{Owner: "acme", Name: "widget"}}
	srv, database := setupTestServerWithRepos(t, &mockGH{}, repos)
	seedIssue(t, database, "acme", "widget", 7, "open")
	client := setupTestClient(t, srv)

	resp, err := client.HTTP.ResolveRepoItemWithResponse(
		t.Context(), "gh", "acme", "widget", 7, nil,
	)
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode())
	require.NotNil(resp.JSON200)
	require.Equal("issue", resp.JSON200.ItemType)
	require.EqualValues(7, resp.JSON200.Number)
	require.True(resp.JSON200.RepoTracked)
}

func TestResolveItem_UsesItemTypeHintForGitLab(t *testing.T) {
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
	itemType := generated.ResolveRepoItemOnHostParamsItemTypeIssue

	resp, err := client.HTTP.ResolveRepoItemOnHostWithResponse(
		t.Context(), "gitlab.example.com", "gitlab", "group", "project", 10,
		&generated.ResolveRepoItemOnHostParams{ItemType: &itemType},
	)
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode())
	require.NotNil(resp.JSON200)
	require.Equal("issue", resp.JSON200.ItemType)
	require.EqualValues(10, resp.JSON200.Number)
	require.True(resp.JSON200.RepoTracked)
}

func TestResolveItem_UntrackedRepo(t *testing.T) {
	require := require.New(t)
	srv, _ := setupTestServer(t)
	client := setupTestClient(t, srv)

	resp, err := client.HTTP.ResolveRepoItemWithResponse(
		t.Context(), "gh", "unknown", "repo", 1, nil,
	)
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode())
	require.NotNil(resp.JSON200)
	require.False(resp.JSON200.RepoTracked)
	require.EqualValues(1, resp.JSON200.Number)
	require.Empty(resp.JSON200.ItemType)
}

func TestResolveItem_NotFoundOnGitHub(t *testing.T) {
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
	_, err := database.UpsertRepo(t.Context(), db.GitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	client := setupTestClient(t, srv)

	resp, err := client.HTTP.ResolveRepoItemWithResponse(
		t.Context(), "gh", "acme", "widget", 999, nil,
	)
	require.NoError(err)
	require.Equal(http.StatusNotFound, resp.StatusCode())
}

func TestResolveItem_GitHubServerError(t *testing.T) {
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
	_, err := database.UpsertRepo(t.Context(), db.GitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	client := setupTestClient(t, srv)

	resp, err := client.HTTP.ResolveRepoItemWithResponse(
		t.Context(), "gh", "acme", "widget", 999, nil,
	)
	require.NoError(err)
	require.Equal(http.StatusBadGateway, resp.StatusCode())
}

func TestAPICloseIssue422AlreadyClosed(t *testing.T) {
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

	resp, err := client.HTTP.SetIssueGithubStateWithResponse(
		t.Context(), "gh", "acme", "widget", 5,
		generated.SetIssueGithubStateJSONRequestBody{State: "closed"},
	)
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode())

	issue, _ := database.GetIssue(t.Context(), "acme", "widget", 5)
	require.Equal("closed", issue.State)
}

func TestAPIGetMRImportMetadata(t *testing.T) {
	require := require.New(t)
	srv, database := setupTestServer(t)
	ctx := t.Context()

	repoID, err := database.UpsertRepo(ctx, db.GitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)

	now := time.Now().UTC().Truncate(time.Second)
	pr := &db.MergeRequest{
		RepoID:           repoID,
		PlatformID:       42000,
		Number:           42,
		URL:              "https://github.com/acme/widget/pull/42",
		Title:            "Add feature X",
		Author:           "octocat",
		State:            "open",
		IsDraft:          true,
		Body:             "body",
		HeadBranch:       "feature-x",
		BaseBranch:       "main",
		PlatformHeadSHA:  "abc123def456",
		HeadRepoCloneURL: "https://github.com/fork/widget.git",
		CreatedAt:        now,
		UpdatedAt:        now,
		LastActivityAt:   now,
	}
	prID, err := database.UpsertMergeRequest(ctx, pr)
	require.NoError(err)
	require.NoError(database.EnsureKanbanState(ctx, prID))

	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/pulls/gh/acme/widget/42/import-metadata", nil)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	require.Equal(http.StatusOK, rr.Code)
	body := rr.Body.String()
	require.Contains(body, `"number":42`)
	require.Contains(body, `"head_branch":"feature-x"`)
	require.Contains(body, `"platform_head_sha":"abc123def456"`)
	require.Contains(body, `"head_repo_clone_url":"https://github.com/fork/widget.git"`)
	require.Contains(body, `"state":"open"`)
	require.Contains(body, `"is_draft":true`)
	require.Contains(body, `"title":"Add feature X"`)
}

func TestAPIGetMRImportMetadataNotFound(t *testing.T) {
	srv, _ := setupTestServer(t)

	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/pulls/gh/acme/widget/999/import-metadata", nil)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	require.Equal(t, http.StatusNotFound, rr.Code)
}

func TestOpenAPIDocumentsCustomStatusCodes(t *testing.T) {
	require := require.New(t)
	srv, _ := setupTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/openapi.json", nil)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	require.Equal(http.StatusOK, rr.Code, rr.Body.String())

	spec := rr.Body.String()
	require.Contains(spec, `"/sync":{"post":{"operationId":"trigger-sync"`)
	require.Contains(spec, `"/starred":{"delete":{"operationId":"unset-starred"`)
	require.Contains(spec, `"/pulls/{provider}/{owner}/{name}/{number}/comments":{"post":{"operationId":"post-pr-comment"`)
	require.Contains(spec, `"trigger-sync","responses":{"202":{"description":"Accepted"}`)
	require.Contains(spec, `"set-starred","requestBody"`)
	require.Contains(spec, `"responses":{"200":{"description":"OK"}`)
	require.True(
		strings.Contains(spec, `"operationId":"post-pr-comment","parameters"`) ||
			strings.Contains(spec, `"operationId":"post-pr-comment","requestBody"`),
		"expected post-pr-comment operation to be present",
	)
	require.Contains(spec, `"responses":{"201":{"content":{"application/json":{"schema":{"$ref":"#/components/schemas/MREvent"}}},"description":"Created"}`)
	require.Contains(spec, `"operationId":"post-issue-comment"`)
	require.Contains(spec, `"responses":{"201":{"content":{"application/json":{"schema":{"$ref":"#/components/schemas/IssueEvent"}}},"description":"Created"}`)
	for _, operationID := range []string{
		"get-version",
		"get-settings",
		"update-settings",
		"add-repo",
		"refresh-repo",
		"delete-repo",
		"stream-events",
		"get-roborev-status",
	} {
		require.Contains(spec, `"operationId":"`+operationID+`"`)
	}
}

func TestMRListIncludesWorktreeLinks(t *testing.T) {
	require := require.New(t)
	srv, database := setupTestServer(t)
	prID := seedPR(t, database, "acme", "widget", 1)

	now := time.Now().UTC().Truncate(time.Second)
	require.NoError(database.SetWorktreeLinks(
		t.Context(),
		[]db.WorktreeLink{
			{
				MergeRequestID: prID,
				WorktreeKey:    "wt-abc",
				WorktreePath:   "/tmp/wt",
				WorktreeBranch: "feature",
				LinkedAt:       now,
			},
		}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/pulls", nil)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	require.Equal(http.StatusOK, rr.Code)
	body := rr.Body.String()
	require.Contains(body, `"worktree_links"`)
	require.Contains(body, `"worktree_key":"wt-abc"`)
	require.Contains(body, `"worktree_path":"/tmp/wt"`)
	require.Contains(body, `"worktree_branch":"feature"`)
}

func TestMRDetailIncludesWorktreeLinks(t *testing.T) {
	require := require.New(t)
	srv, database := setupTestServer(t)
	prID := seedPR(t, database, "acme", "widget", 1)

	now := time.Now().UTC().Truncate(time.Second)
	require.NoError(database.SetWorktreeLinks(
		t.Context(),
		[]db.WorktreeLink{
			{
				MergeRequestID: prID,
				WorktreeKey:    "wt-detail",
				WorktreePath:   "/tmp/detail",
				LinkedAt:       now,
			},
		}))

	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/pulls/gh/acme/widget/1", nil)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	require.Equal(http.StatusOK, rr.Code)
	body := rr.Body.String()
	require.Contains(body, `"worktree_links"`)
	require.Contains(body, `"worktree_key":"wt-detail"`)
}

func TestProviderPullRouteResolvesEscapedGitLabRepoPath(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)
	srv, database := setupTestServer(t)
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Second)

	repoPath := "Group/SubGroup/SubGroup 2/My_Project.v2"
	repoID, err := database.UpsertRepo(ctx, db.RepoIdentity{
		Platform:     "gitlab",
		PlatformHost: "gitlab.example.com:8443",
		Owner:        "Group/SubGroup/SubGroup 2",
		Name:         "My_Project.v2",
		RepoPath:     repoPath,
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
	rr := doJSON(t, srv, http.MethodGet, path, nil)
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

func TestAPIGitLabProviderCapabilitiesExposeOnResponses(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)
	srv, database := setupGitLabCapabilityServer(t)
	ctx := t.Context()

	rawPulls := doJSON(t, srv, http.MethodGet, "/api/v1/pulls", nil)
	require.Equal(http.StatusOK, rawPulls.Code, rawPulls.Body.String())
	var pulls []map[string]any
	require.NoError(json.NewDecoder(rawPulls.Body).Decode(&pulls))
	require.Len(pulls, 1)
	pullRepo := pulls[0]["repo"].(map[string]any)
	pullCaps := pullRepo["capabilities"].(map[string]any)
	assert.Equal(true, pullCaps["read_merge_requests"])
	assert.Equal(true, pullCaps["read_issues"])
	assert.Equal(false, pullCaps["comment_mutation"])
	assert.Equal(false, pullCaps["state_mutation"])
	assert.Equal(false, pullCaps["merge_mutation"])
	assert.Equal(false, pullCaps["review_mutation"])
	assert.Equal(false, pullCaps["workflow_approval"])
	assert.Equal(false, pullCaps["ready_for_review"])
	assert.Equal(false, pullCaps["issue_mutation"])

	rawDetail := doJSON(
		t,
		srv,
		http.MethodGet,
		"/api/v1/host/gitlab.example.com/pulls/gl/group/project/7",
		nil,
	)
	require.Equal(http.StatusOK, rawDetail.Code, rawDetail.Body.String())
	var detail map[string]any
	require.NoError(json.NewDecoder(rawDetail.Body).Decode(&detail))
	detailRepo := detail["repo"].(map[string]any)
	detailCaps := detailRepo["capabilities"].(map[string]any)
	assert.Equal(true, detailCaps["read_merge_requests"])
	assert.Equal(false, detailCaps["merge_mutation"])

	rawSummaries := doJSON(t, srv, http.MethodGet, "/api/v1/repos/summary", nil)
	require.Equal(http.StatusOK, rawSummaries.Code, rawSummaries.Body.String())
	var summaries []map[string]any
	require.NoError(json.NewDecoder(rawSummaries.Body).Decode(&summaries))
	require.Len(summaries, 1)
	summaryRepo := summaries[0]["repo"].(map[string]any)
	summaryCaps := summaryRepo["capabilities"].(map[string]any)
	assert.Equal(true, summaryCaps["read_repositories"])
	assert.Equal(false, summaryCaps["issue_mutation"])

	rawRepos := doJSON(t, srv, http.MethodGet, "/api/v1/repos", nil)
	require.Equal(http.StatusOK, rawRepos.Code, rawRepos.Body.String())
	var repos []map[string]any
	require.NoError(json.NewDecoder(rawRepos.Body).Decode(&repos))
	require.Len(repos, 1)
	repoCaps := repos[0]["capabilities"].(map[string]any)
	assert.Equal(true, repoCaps["read_repositories"])
	assert.Equal(false, repoCaps["comment_mutation"])

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
	require.NoError(database.UpsertMREvents(ctx, []db.MREvent{
		{
			MergeRequestID: mr.ID,
			EventType:      "issue_comment",
			Author:         "ada",
			Body:           "GitLab activity comment",
			CreatedAt:      time.Now().UTC(),
			DedupeKey:      "gitlab-activity-comment",
		},
	}))

	since := time.Now().UTC().AddDate(0, 0, -7).Format(time.RFC3339)
	rawActivity := doJSON(t, srv, http.MethodGet, "/api/v1/activity?since="+since, nil)
	require.Equal(http.StatusOK, rawActivity.Code, rawActivity.Body.String())
	var activity map[string]any
	require.NoError(json.NewDecoder(rawActivity.Body).Decode(&activity))
	activityItems := activity["items"].([]any)
	require.NotEmpty(activityItems)
	activityRepo := activityItems[0].(map[string]any)["repo"].(map[string]any)
	activityCaps := activityRepo["capabilities"].(map[string]any)
	assert.Equal("gitlab", activityRepo["provider"])
	assert.Equal(true, activityCaps["read_merge_requests"])
	assert.Equal(false, activityCaps["comment_mutation"])

	srv.workspaces = workspace.NewManager(
		database, filepath.Join(t.TempDir(), "worktrees"),
	)
	srv.workspaces.SetTmuxCommand([]string{"sh", "-c", "exit 0"})
	require.NoError(database.InsertWorkspace(ctx, &db.Workspace{
		ID:           "gitlabcap0000001",
		Platform:     "gitlab",
		PlatformHost: "gitlab.example.com",
		RepoOwner:    "group",
		RepoName:     "project",
		ItemType:     db.WorkspaceItemTypePullRequest,
		ItemNumber:   7,
		GitHeadRef:   "feature/gitlab",
		WorktreePath: filepath.Join(t.TempDir(), "workspace"),
		TmuxSession:  "middleman-gitlabcap0000001",
		Status:       "creating",
	}))
	rawWorkspaces := doJSON(t, srv, http.MethodGet, "/api/v1/workspaces", nil)
	require.Equal(http.StatusOK, rawWorkspaces.Code, rawWorkspaces.Body.String())
	var workspaces map[string]any
	require.NoError(json.NewDecoder(rawWorkspaces.Body).Decode(&workspaces))
	workspaceItems := workspaces["workspaces"].([]any)
	require.Len(workspaceItems, 1)
	workspaceRepo := workspaceItems[0].(map[string]any)["repo"].(map[string]any)
	workspaceCaps := workspaceRepo["capabilities"].(map[string]any)
	assert.Equal("gitlab", workspaceRepo["provider"])
	assert.Equal(true, workspaceCaps["read_repositories"])
	assert.Equal(false, workspaceCaps["merge_mutation"])
}

func TestAPIGitLabUnsupportedMutationsReturnCodedCapabilityErrors(t *testing.T) {
	srv, _ := setupGitLabCapabilityServer(t)

	tests := []struct {
		name       string
		method     string
		path       string
		body       any
		capability string
	}{
		{
			name:       "PR comment",
			method:     http.MethodPost,
			path:       "/api/v1/host/gitlab.example.com/pulls/gl/group/project/7/comments",
			body:       map[string]string{"body": "hello"},
			capability: "comment_mutation",
		},
		{
			name:       "PR content",
			method:     http.MethodPatch,
			path:       "/api/v1/host/gitlab.example.com/pulls/gl/group/project/7",
			body:       map[string]string{"title": "Updated title"},
			capability: "state_mutation",
		},
		{
			name:       "review approval",
			method:     http.MethodPost,
			path:       "/api/v1/host/gitlab.example.com/pulls/gl/group/project/7/approve",
			body:       map[string]string{"body": "looks good"},
			capability: "review_mutation",
		},
		{
			name:       "workflow approval",
			method:     http.MethodPost,
			path:       "/api/v1/host/gitlab.example.com/pulls/gl/group/project/7/approve-workflows",
			body:       nil,
			capability: "workflow_approval",
		},
		{
			name:       "ready for review",
			method:     http.MethodPost,
			path:       "/api/v1/host/gitlab.example.com/pulls/gl/group/project/7/ready-for-review",
			body:       nil,
			capability: "ready_for_review",
		},
		{
			name:   "merge",
			method: http.MethodPost,
			path:   "/api/v1/host/gitlab.example.com/pulls/gl/group/project/7/merge",
			body: map[string]string{
				"method":         "squash",
				"commit_title":   "Merge MR",
				"commit_message": "Merge GitLab MR",
			},
			capability: "merge_mutation",
		},
		{
			name:       "PR state",
			method:     http.MethodPost,
			path:       "/api/v1/host/gitlab.example.com/pulls/gl/group/project/7/github-state",
			body:       map[string]string{"state": "closed"},
			capability: "state_mutation",
		},
		{
			name:       "issue creation",
			method:     http.MethodPost,
			path:       "/api/v1/host/gitlab.example.com/issues/gl/group/project",
			body:       map[string]string{"title": "New issue", "body": "Issue body"},
			capability: "issue_mutation",
		},
		{
			name:       "issue comment",
			method:     http.MethodPost,
			path:       "/api/v1/host/gitlab.example.com/issues/gl/group/project/11/comments",
			body:       map[string]string{"body": "hello"},
			capability: "comment_mutation",
		},
		{
			name:       "issue state",
			method:     http.MethodPost,
			path:       "/api/v1/host/gitlab.example.com/issues/gl/group/project/11/github-state",
			body:       map[string]string{"state": "closed"},
			capability: "state_mutation",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require := require.New(t)
			rr := doJSON(t, srv, tt.method, tt.path, tt.body)
			require.Equal(http.StatusConflict, rr.Code, rr.Body.String())
			assertUnsupportedCapabilityProblem(
				t, rr.Body, "gitlab", "gitlab.example.com", tt.capability,
			)
		})
	}
}

// TestAPIUnsupportedCapabilityEnvelope is the wire-level guarantee that
// the gitlab capability gate emits an RFC 9457 envelope with a top-level
// `code = "unsupportedCapability"` and `details.capability` carrying the
// capability the route required. Frontend callers branch on `code`.
func TestAPIUnsupportedCapabilityEnvelope(t *testing.T) {
	srv, _ := setupGitLabCapabilityServer(t)
	require := require.New(t)
	assert := Assert.New(t)

	rr := doJSON(
		t,
		srv,
		http.MethodPost,
		"/api/v1/host/gitlab.example.com/pulls/gl/group/project/7/approve-workflows",
		nil,
	)
	require.Equal(http.StatusConflict, rr.Code, rr.Body.String())

	var problem rawProblemDetail
	require.NoError(json.NewDecoder(rr.Body).Decode(&problem))
	assert.Equal("unsupportedCapability", problem.Code)
	require.NotNil(problem.Details)
	assert.Equal("workflow_approval", problem.Details["capability"])
	assert.Equal("gitlab", problem.Details["provider"])
	assert.Equal("gitlab.example.com", problem.Details["platformHost"])
}

func TestAPICapabilityGatedRouteReturnsLookupProblemBeforeCapabilityProblem(t *testing.T) {
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
			assert := Assert.New(t)

			rr := doJSON(
				t,
				srv,
				http.MethodPatch,
				tt.path,
				map[string]string{"title": "Updated title"},
			)
			require.Equal(tt.wantCode, rr.Code, rr.Body.String())

			var problem rawProblemDetail
			require.NoError(json.NewDecoder(rr.Body).Decode(&problem))
			assert.Equal(tt.wantWire, problem.Code)
		})
	}
}

func TestAPICapabilityGatedMutationsHandleMissingSyncer(t *testing.T) {
	database := dbtest.Open(t)
	srv := New(database, nil, nil, "/", nil, ServerOptions{})
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

			rr := doJSON(t, srv, tt.method, tt.path, tt.body)
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
			assert := Assert.New(t)

			rr := doJSON(t, srv, tt.method, tt.path, tt.body)
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
	require := require.New(t)
	assert := Assert.New(t)

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

	rr := doJSON(
		t,
		srv,
		http.MethodPut,
		"/api/v1/pulls/gh/acme/widget/42/state",
		map[string]string{"status": "frobnicated"},
	)
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
func setupGitLabIssueMutatorServer(t *testing.T, createIssueErr error) *Server {
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
		apiTestGitLabProvider: apiTestGitLabProvider{
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
		},
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
	srv := New(database, syncer, nil, "/", nil, ServerOptions{})
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

func TestAPIGitealikeReadSyncPersistsThroughServer(t *testing.T) {
	assert := Assert.New(t)
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
	srv := New(database, syncer, nil, "/", nil, ServerOptions{})
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

	pullResp, err := client.HTTP.GetPullOnHostWithResponse(
		ctx, "codeberg.test", "forgejo", "forgejo", "tea", 7,
	)
	require.NoError(err)
	require.Equal(http.StatusOK, pullResp.StatusCode())
	require.NotNil(pullResp.JSON200)
	assert.True(pullResp.JSON200.MergeRequest.IsLocked)
	assert.Equal("forgejo", pullResp.JSON200.Repo.Provider)
	assert.True(pullResp.JSON200.Repo.Capabilities.ReadMergeRequests)
	assert.True(pullResp.JSON200.Repo.Capabilities.ReadCi)
	require.NotNil(pullResp.JSON200.Events)
	require.Len(*pullResp.JSON200.Events, 1)
	assert.Equal("looks good", (*pullResp.JSON200.Events)[0].Body)

	issueResp, err := client.HTTP.GetIssueOnHostWithResponse(
		ctx, "codeberg.test", "forgejo", "forgejo", "tea", 8,
	)
	require.NoError(err)
	require.Equal(http.StatusOK, issueResp.StatusCode())
	require.NotNil(issueResp.JSON200)
	assert.Equal("Missing cup", issueResp.JSON200.Issue.Title)
	require.NotNil(issueResp.JSON200.Events)
	require.Len(*issueResp.JSON200.Events, 1)
	assert.Equal("confirmed", (*issueResp.JSON200.Events)[0].Body)
}

func TestAPIGitealikeHTTPMergeabilityPersistsThroughServer(t *testing.T) {
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
					host, token, giteaplatform.WithBaseURLForTesting(baseURL),
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
					host, token, forgejoplatform.WithBaseURLForTesting(baseURL),
				)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert := Assert.New(t)
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
			srv := New(database, syncer, nil, "/", nil, ServerOptions{})
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

			detailResp, err := client.HTTP.GetPullOnHostWithResponse(
				ctx, tt.host, string(tt.kind), "tea", "kettle", 7,
			)
			require.NoError(err)
			require.Equal(http.StatusOK, detailResp.StatusCode(), string(detailResp.Body))
			require.NotNil(detailResp.JSON200)
			assert.Equal("dirty", detailResp.JSON200.MergeRequest.MergeableState)
		})
	}
}

func TestAPIGitealikeMutationsPersistThroughServer(t *testing.T) {
	assert := Assert.New(t)
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
	srv := New(database, syncer, nil, "/", nil, ServerOptions{})
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

	editedTitle := "Edited kettle"
	editedBody := "Updated kettle body"
	editContentResp, err := client.HTTP.EditPrContentOnHostWithResponse(
		ctx, "gitea.test", "gitea", "tea", "kettle", 7,
		generated.EditPrContentOnHostJSONRequestBody{
			Title: &editedTitle,
			Body:  &editedBody,
		},
	)
	require.NoError(err)
	require.Equal(http.StatusOK, editContentResp.StatusCode())
	require.NotNil(editContentResp.JSON200)
	assert.Equal(editedTitle, editContentResp.JSON200.MergeRequest.Title)
	assert.Equal(editedBody, editContentResp.JSON200.MergeRequest.Body)
	mrSeven := requireMR(t, database, repo.ID, 7)
	assert.Equal(editedTitle, mrSeven.Title)
	assert.Equal(editedBody, mrSeven.Body)

	commentResp, err := client.HTTP.PostPrCommentOnHostWithResponse(
		ctx, "gitea.test", "gitea", "tea", "kettle", 7,
		generated.PostPrCommentOnHostJSONRequestBody{Body: "Looks good"},
	)
	require.NoError(err)
	require.Equal(http.StatusCreated, commentResp.StatusCode())
	mrEvents, err := database.ListMREvents(ctx, mrSeven.ID)
	require.NoError(err)
	require.Len(mrEvents, 1)
	require.NotNil(mrEvents[0].PlatformID)
	commentID := *mrEvents[0].PlatformID
	assert.Equal("Looks good", mrEvents[0].Body)

	editCommentResp, err := client.HTTP.EditPrCommentOnHostWithResponse(
		ctx, "gitea.test", "gitea", "tea", "kettle", 7, commentID,
		generated.EditPrCommentOnHostJSONRequestBody{Body: "Still good"},
	)
	require.NoError(err)
	require.Equal(http.StatusOK, editCommentResp.StatusCode())
	mrEvents, err = database.ListMREvents(ctx, mrSeven.ID)
	require.NoError(err)
	require.Len(mrEvents, 1)
	assert.Equal("Still good", mrEvents[0].Body)

	approveResp, err := client.HTTP.ApprovePullOnHostWithResponse(
		ctx, "gitea.test", "gitea", "tea", "kettle", 7,
		generated.ApprovePullOnHostJSONRequestBody{Body: "approved"},
	)
	require.NoError(err)
	require.Equal(http.StatusOK, approveResp.StatusCode())
	mrEvents, err = database.ListMREvents(ctx, mrSeven.ID)
	require.NoError(err)
	assert.Len(mrEvents, 2)

	mergeResp, err := client.HTTP.MergePullOnHostWithResponse(
		ctx, "gitea.test", "gitea", "tea", "kettle", 7,
		generated.MergePullOnHostJSONRequestBody{
			Method:        "squash",
			CommitTitle:   "Merge kettle",
			CommitMessage: "Merge Gitea MR",
		},
	)
	require.NoError(err)
	require.Equal(http.StatusOK, mergeResp.StatusCode())
	mrSeven = requireMR(t, database, repo.ID, 7)
	assert.Equal(db.MergeRequestStateMerged, mrSeven.State)
	require.NotNil(mrSeven.MergedAt)

	stateResp, err := client.HTTP.SetPrGithubStateOnHostWithResponse(
		ctx, "gitea.test", "gitea", "tea", "kettle", 9,
		generated.SetPrGithubStateOnHostJSONRequestBody{State: "closed"},
	)
	require.NoError(err)
	require.Equal(http.StatusOK, stateResp.StatusCode())
	mrNine := requireMR(t, database, repo.ID, 9)
	assert.Equal(db.MergeRequestStateClosed, mrNine.State)
	require.NotNil(mrNine.ClosedAt)

	createIssueResp, err := client.HTTP.CreateIssueOnHostWithResponse(
		ctx, "gitea.test", "gitea", "tea", "kettle",
		generated.CreateIssueOnHostJSONRequestBody{Title: "New issue", Body: "New issue body"},
	)
	require.NoError(err)
	require.Equal(http.StatusCreated, createIssueResp.StatusCode())
	createdIssue, err := database.GetIssueByRepoIDAndNumber(ctx, repo.ID, 81)
	require.NoError(err)
	require.NotNil(createdIssue)
	assert.Equal("New issue", createdIssue.Title)

	issueCommentResp, err := client.HTTP.PostIssueCommentOnHostWithResponse(
		ctx, "gitea.test", "gitea", "tea", "kettle", 8,
		generated.PostIssueCommentOnHostJSONRequestBody{Body: "Confirmed"},
	)
	require.NoError(err)
	require.Equal(http.StatusCreated, issueCommentResp.StatusCode())
	issueEight := requireIssue(t, database, repo.ID, 8)
	issueEvents, err := database.ListIssueEvents(ctx, issueEight.ID)
	require.NoError(err)
	require.Len(issueEvents, 1)
	require.NotNil(issueEvents[0].PlatformID)
	issueCommentID := *issueEvents[0].PlatformID
	assert.Equal("Confirmed", issueEvents[0].Body)

	editIssueCommentResp, err := client.HTTP.EditIssueCommentOnHostWithResponse(
		ctx, "gitea.test", "gitea", "tea", "kettle", 8, issueCommentID,
		generated.EditIssueCommentOnHostJSONRequestBody{Body: "Confirmed again"},
	)
	require.NoError(err)
	require.Equal(http.StatusOK, editIssueCommentResp.StatusCode())
	issueEvents, err = database.ListIssueEvents(ctx, issueEight.ID)
	require.NoError(err)
	require.Len(issueEvents, 1)
	assert.Equal("Confirmed again", issueEvents[0].Body)

	issueStateResp, err := client.HTTP.SetIssueGithubStateOnHostWithResponse(
		ctx, "gitea.test", "gitea", "tea", "kettle", 8,
		generated.SetIssueGithubStateOnHostJSONRequestBody{State: "closed"},
	)
	require.NoError(err)
	require.Equal(http.StatusOK, issueStateResp.StatusCode())
	issueEight = requireIssue(t, database, repo.ID, 8)
	assert.Equal("closed", issueEight.State)
	require.NotNil(issueEight.ClosedAt)

	assert.Subset(transport.mutationCalls, []string{
		"edit_pull_content:7:Edited kettle:Updated kettle body",
		"create_comment:7:Looks good",
		"edit_comment:900:Still good",
		"review:7:approved",
		"merge:7:squash",
		"edit_pull:9:closed",
		"create_issue:New issue",
		"create_comment:8:Confirmed",
		"edit_comment:901:Confirmed again",
		"edit_issue:8:closed",
	})
}

func TestAPIGiteaActionsSyncPersistsThroughServer(t *testing.T) {
	assert := Assert.New(t)
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
	srv := New(database, syncer, nil, "/", nil, ServerOptions{})
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

	pullResp, err := client.HTTP.GetPullOnHostWithResponse(
		ctx, "gitea.test", "gitea", "tea", "actions", 5,
	)
	require.NoError(err)
	require.Equal(http.StatusOK, pullResp.StatusCode())
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

func TestAPIGitealikeMergeConflictReturnsConflict(t *testing.T) {
	assert := Assert.New(t)
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
	srv := New(database, syncer, nil, "/", nil, ServerOptions{})
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
	assert.Equal("dirty", requireMR(t, database, repo.ID, 7).MergeableState)
	transport.pulls[0].Title = "Refreshed kettle after conflict"

	resp, err := client.HTTP.MergePullOnHostWithResponse(
		ctx, "gitea.test", "gitea", "tea", "kettle", 7,
		generated.MergePullOnHostJSONRequestBody{
			Method:        "squash",
			CommitTitle:   "Merge kettle",
			CommitMessage: "Merge Gitea MR",
		},
	)
	require.NoError(err)
	require.Equal(http.StatusConflict, resp.StatusCode(), string(resp.Body))
	assert.Contains(string(resp.Body), "pull request is out of date")
	assert.Contains(transport.mutationCalls, "merge:7:squash")
	require.Eventually(func() bool {
		mr := requireMR(t, database, repo.ID, 7)
		return mr.State == "open" && mr.Title == "Refreshed kettle after conflict"
	}, time.Second, 10*time.Millisecond)
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
	body string,
) (gitealike.ReviewDTO, error) {
	t.mutationCalls = append(t.mutationCalls, fmt.Sprintf("review:%d:%s", number, body))
	return gitealike.ReviewDTO{
		ID:        980,
		User:      gitealike.UserDTO{UserName: "reviewer"},
		State:     "approved",
		Body:      body,
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

func setupGitLabCapabilityServer(t *testing.T) (*Server, *db.DB) {
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
		ref: ref,
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
	srv := New(database, syncer, nil, "/", nil, ServerOptions{})
	t.Cleanup(func() { gracefulShutdown(t, srv) })

	syncer.RunOnce(ctx)
	return srv, database
}

func assertUnsupportedCapabilityProblem(
	t *testing.T,
	body io.Reader,
	provider, host, capability string,
) {
	t.Helper()
	require := require.New(t)
	assert := Assert.New(t)

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

func TestAPIGitealikeLockedPRPersistsThroughServer(t *testing.T) {
	assert := Assert.New(t)
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
	srv := New(database, syncer, nil, "/", nil, ServerOptions{})
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

	pullResp, err := client.HTTP.GetPullOnHostWithResponse(
		ctx, "codeberg.test", "forgejo", "forgejo", "tea", 7,
	)
	require.NoError(err)
	require.Equal(http.StatusOK, pullResp.StatusCode(), string(pullResp.Body))
	require.NotNil(pullResp.JSON200)
	assert.False(pullResp.JSON200.MergeRequest.IsDraft)
	assert.True(pullResp.JSON200.MergeRequest.IsLocked)
}

func TestAPIGitealikeDraftPRFieldsPersistThroughServer(t *testing.T) {
	assert := Assert.New(t)
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
	srv := New(database, syncer, nil, "/", nil, ServerOptions{})
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

	pullResp, err := client.HTTP.GetPullOnHostWithResponse(
		ctx, "gitea.test", "gitea", "gitea", "tea", 8,
	)
	require.NoError(err)
	require.Equal(http.StatusOK, pullResp.StatusCode(), string(pullResp.Body))
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
	require.Len(*apiMR.Labels, 1)
	assert.Equal("bug", (*apiMR.Labels)[0].Name)
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

func TestProviderIssueRouteGeneratedClientEscapesGitLabRepoPath(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)
	srv, database := setupTestServer(t)
	client := setupTestClient(t, srv)
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Second)

	provider := "gitlab"
	host := "gitlab.example.test:8443"
	repoPath := "Team One/Sub Team/project+#1"
	number := int64(7)
	repoID, err := database.UpsertRepo(ctx, db.RepoIdentity{
		Platform:     provider,
		PlatformHost: host,
		Owner:        "Team One/Sub Team",
		Name:         "project+#1",
		RepoPath:     repoPath,
	})
	require.NoError(err)
	_, err = database.UpsertIssue(ctx, &db.Issue{
		RepoID:         repoID,
		PlatformID:     7000,
		Number:         int(number),
		URL:            "https://gitlab.example.test/Team%20One/Sub%20Team/project%2B%231/-/issues/7",
		Title:          "Special chars issue",
		Author:         "testuser",
		State:          "open",
		CreatedAt:      now,
		UpdatedAt:      now,
		LastActivityAt: now,
	})
	require.NoError(err)

	resp, err := client.HTTP.GetIssueOnHostWithResponse(
		ctx, host, provider, "Team One/Sub Team", "project+#1", number,
	)
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode(), string(resp.Body))
	require.NotNil(resp.JSON200)

	assert.Equal(provider, resp.JSON200.Repo.Provider)
	assert.Equal(host, resp.JSON200.Repo.PlatformHost)
	assert.Equal(repoPath, resp.JSON200.Repo.RepoPath)
	assert.Equal("Team One/Sub Team", resp.JSON200.Repo.Owner)
	assert.Equal("project+#1", resp.JSON200.Repo.Name)
	assert.Equal(number, resp.JSON200.Issue.Number)
}

func TestProviderIssueRouteHandlesNestedGitLabRepoPathOverHTTP(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)
	srv, database := setupTestServer(t)
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Second)

	repoID, err := database.UpsertRepo(ctx, db.RepoIdentity{
		Platform:     "gitlab",
		PlatformHost: "git.example.com",
		Owner:        "group/subgroup",
		Name:         "project",
		RepoPath:     "group/subgroup/project",
	})
	require.NoError(err)
	_, err = database.UpsertIssue(ctx, &db.Issue{
		RepoID:         repoID,
		PlatformID:     7007,
		Number:         7,
		URL:            "https://git.example.com/group/subgroup/project/-/issues/7",
		Title:          "Nested GitLab issue",
		Author:         "testuser",
		State:          "open",
		CreatedAt:      now,
		UpdatedAt:      now,
		LastActivityAt: now,
	})
	require.NoError(err)

	req := httptest.NewRequest(
		http.MethodGet,
		"/api/v1/host/git.example.com/issues/gitlab/group%2Fsubgroup/project/7",
		nil,
	)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	require.Equal(http.StatusOK, rr.Code, rr.Body.String())

	var body struct {
		Issue struct {
			Number int64  `json:"number"`
			Title  string `json:"title"`
		} `json:"issue"`
		Repo struct {
			Provider     string `json:"provider"`
			PlatformHost string `json:"platform_host"`
			Owner        string `json:"owner"`
			Name         string `json:"name"`
			RepoPath     string `json:"repo_path"`
		} `json:"repo"`
	}
	require.NoError(json.Unmarshal(rr.Body.Bytes(), &body))
	assert.Equal(int64(7), body.Issue.Number)
	assert.Equal("Nested GitLab issue", body.Issue.Title)
	assert.Equal("gitlab", body.Repo.Provider)
	assert.Equal("git.example.com", body.Repo.PlatformHost)
	assert.Equal("group/subgroup", body.Repo.Owner)
	assert.Equal("project", body.Repo.Name)
	assert.Equal("group/subgroup/project", body.Repo.RepoPath)
}

func TestMRListEmptyLinksWhenNone(t *testing.T) {
	require := require.New(t)
	srv, database := setupTestServer(t)
	seedPR(t, database, "acme", "widget", 1)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/pulls", nil)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	require.Equal(http.StatusOK, rr.Code)
	body := rr.Body.String()
	// Should contain an empty array, not null.
	require.Contains(body, `"worktree_links":[]`)
}

func TestAPIGetFiles503WhenCloneManagerNil(t *testing.T) {
	require := require.New(t)

	srv, database := setupTestServer(t)
	seedPR(t, database, "acme", "widget", 1)
	client := setupTestClient(t, srv)

	resp, err := client.HTTP.GetPullFilesWithResponse(
		t.Context(), "gh", "acme", "widget", 1,
	)
	require.NoError(err)
	require.Equal(http.StatusServiceUnavailable, resp.StatusCode())
}

func TestAPIGetFilesAndDiffMarkGeneratedFilesE2E(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)
	ctx := t.Context()

	dir := t.TempDir()
	database := dbtest.Open(t)

	bareDir := filepath.Join(dir, "clones")
	bare := filepath.Join(bareDir, "github.com", "acme", "widget.git")
	require.NoError(os.MkdirAll(filepath.Dir(bare), 0o755))

	work := filepath.Join(dir, "work")
	runGit(t, dir, "init", "--bare", "--initial-branch=main", bare)
	runGit(t, dir, "clone", bare, work)
	runGit(t, work, "config", "user.email", "test@test.com")
	runGit(t, work, "config", "user.name", "Test")

	require.NoError(os.WriteFile(
		filepath.Join(work, "base.txt"),
		[]byte("base\n"), 0o644,
	))
	runGit(t, work, "add", ".")
	runGit(t, work, "commit", "-m", "base commit")
	runGit(t, work, "push", "origin", "main")
	mergeBase := testGitSHA(t, work, "HEAD")

	runGit(t, work, "checkout", "-b", "feature")
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
	runGit(t, work, "add", ".")
	runGit(t, work, "commit", "-m", "feature commit")
	runGit(t, work, "push", "origin", "feature")
	headSHA := testGitSHA(t, work, "HEAD")

	clones := gitclone.New(bareDir, nil)
	syncer := ghclient.NewSyncer(
		map[string]ghclient.Client{"github.com": &mockGH{}},
		database, nil, defaultTestRepos, time.Minute, nil, nil,
	)
	t.Cleanup(syncer.Stop)
	srv := New(database, syncer, nil, "/", nil, ServerOptions{Clones: clones})
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

	filesResp, err := client.HTTP.GetPullFilesWithResponse(
		ctx, "gh", "acme", "widget", 1,
	)
	require.NoError(err)
	require.Equal(http.StatusOK, filesResp.StatusCode(), string(filesResp.Body))
	require.NotNil(filesResp.JSON200)
	require.NotNil(filesResp.JSON200.Files)
	assert.True(requireWorkspaceDiffFile(t, *filesResp.JSON200.Files, "dist/api.ts").IsGenerated)
	assert.False(requireWorkspaceDiffFile(t, *filesResp.JSON200.Files, "bun.lock").IsGenerated)
	assert.False(requireWorkspaceDiffFile(t, *filesResp.JSON200.Files, "src.ts").IsGenerated)

	diffResp, err := client.HTTP.GetPullDiffWithResponse(
		ctx, "gh", "acme", "widget", 1,
		nil,
	)
	require.NoError(err)
	require.Equal(http.StatusOK, diffResp.StatusCode(), string(diffResp.Body))
	require.NotNil(diffResp.JSON200)
	require.NotNil(diffResp.JSON200.Files)
	assert.True(requireWorkspaceDiffFile(t, *diffResp.JSON200.Files, "dist/api.ts").IsGenerated)
	assert.False(requireWorkspaceDiffFile(t, *diffResp.JSON200.Files, "bun.lock").IsGenerated)
	assert.False(requireWorkspaceDiffFile(t, *diffResp.JSON200.Files, "src.ts").IsGenerated)
}

func TestSetActiveWorktreeKey(t *testing.T) {
	assert := Assert.New(t)
	srv, _ := setupTestServer(t)

	key, set := srv.ActiveWorktreeKey()
	assert.Empty(key)
	assert.False(set)

	srv.SetActiveWorktreeKey("wt-abc")
	key, set = srv.ActiveWorktreeKey()
	assert.Equal("wt-abc", key)
	assert.True(set)

	srv.SetActiveWorktreeKey("")
	key, set = srv.ActiveWorktreeKey()
	assert.Empty(key)
	assert.True(set, "should still be 'set' even when cleared")
}

func TestAPIRateLimits(t *testing.T) {
	assert := Assert.New(t)

	database := dbtest.Open(t)

	rt := ghclient.NewRateTracker(database, "github.com", "rest")

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

	srv := New(database, syncer, nil, "/", nil, ServerOptions{})
	ts := httptest.NewServer(srv)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/v1/rate-limits")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(200, resp.StatusCode)

	var body rateLimitsResponse
	err = json.NewDecoder(resp.Body).Decode(&body)
	require.NoError(t, err)

	gh, ok := body.Hosts["github.com"]
	assert.True(ok)
	assert.Equal(0, gh.RequestsHour)
	assert.Equal(-1, gh.RateRemaining)
	assert.False(gh.Known)
	assert.Equal(1, gh.SyncThrottleFactor)
	assert.False(gh.SyncPaused)
	assert.Equal(200, gh.ReserveBuffer)
	// Budget fields default to zero when budgetPerHour=0.
	assert.Equal(0, gh.BudgetLimit)
	assert.Equal(0, gh.BudgetSpent)
	assert.Equal(0, gh.BudgetRemaining)
}

func TestAPISyncPRIncrementsRequestCount(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)

	database := dbtest.Open(t)

	rt := ghclient.NewRateTracker(database, "github.com", "rest")

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

	srv := New(database, syncer, nil, "/", nil, ServerOptions{})
	ts := httptest.NewServer(srv)
	defer ts.Close()

	// Before any requests: requests_hour should be 0.
	resp, err := http.Get(ts.URL + "/api/v1/rate-limits")
	require.NoError(err)
	defer resp.Body.Close()
	assert.Equal(200, resp.StatusCode)

	var before rateLimitsResponse
	err = json.NewDecoder(resp.Body).Decode(&before)
	require.NoError(err)

	gh0, ok := before.Hosts["github.com"]
	assert.True(ok)
	assert.Equal(0, gh0.RequestsHour)

	// Simulate 5 API calls via RecordRequest.
	for range 5 {
		rt.RecordRequest()
	}

	// After recording: requests_hour should be 5.
	resp2, err := http.Get(ts.URL + "/api/v1/rate-limits")
	require.NoError(err)
	defer resp2.Body.Close()
	assert.Equal(200, resp2.StatusCode)

	var after rateLimitsResponse
	err = json.NewDecoder(resp2.Body).Decode(&after)
	require.NoError(err)

	gh5, ok := after.Hosts["github.com"]
	assert.True(ok)
	assert.Equal(5, gh5.RequestsHour)
}

func TestAPIRateLimitsWithBudget(t *testing.T) {
	assert := Assert.New(t)

	database := dbtest.Open(t)

	rt := ghclient.NewRateTracker(database, "github.com", "rest")

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

	srv := New(database, syncer, nil, "/", nil, ServerOptions{})
	ts := httptest.NewServer(srv)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/v1/rate-limits")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(200, resp.StatusCode)

	var body rateLimitsResponse
	err = json.NewDecoder(resp.Body).Decode(&body)
	require.NoError(t, err)

	gh, ok := body.Hosts["github.com"]
	assert.True(ok)
	assert.Equal(500, gh.BudgetLimit)
	assert.Equal(42, gh.BudgetSpent)
	assert.Equal(458, gh.BudgetRemaining)
}

func TestAPIRateLimitsResetExpiredBudgetWindow(t *testing.T) {
	assert := Assert.New(t)

	database := dbtest.Open(t)

	rt := ghclient.NewRateTracker(database, "github.com", "rest")
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

	srv := New(database, syncer, nil, "/", nil, ServerOptions{})
	ts := httptest.NewServer(srv)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/v1/rate-limits")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(200, resp.StatusCode)

	var body rateLimitsResponse
	err = json.NewDecoder(resp.Body).Decode(&body)
	require.NoError(t, err)

	gh, ok := body.Hosts["github.com"]
	assert.True(ok)
	assert.Equal(500, gh.BudgetLimit)
	assert.Equal(0, gh.BudgetSpent)
	assert.Equal(500, gh.BudgetRemaining)
}

func TestAPIRateLimitsWithGQL(t *testing.T) {
	assert := Assert.New(t)

	database := dbtest.Open(t)

	restRT := ghclient.NewRateTracker(database, "github.com", "rest")
	gqlRT := ghclient.NewRateTracker(database, "github.com", "graphql")

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

	fetcher := ghclient.NewGraphQLFetcher("token", "github.com", gqlRT, nil)
	syncer.SetFetchers(map[string]*ghclient.GraphQLFetcher{
		"github.com": fetcher,
	})

	// Simulate GraphQL rate data.
	gqlRT.UpdateFromRate(ghclient.Rate{
		Limit:     5000,
		Remaining: 4800,
		Reset:     time.Now().Add(30 * time.Minute),
	})

	srv := New(database, syncer, nil, "/", nil, ServerOptions{})
	ts := httptest.NewServer(srv)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/v1/rate-limits")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(200, resp.StatusCode)

	var body rateLimitsResponse
	err = json.NewDecoder(resp.Body).Decode(&body)
	require.NoError(t, err)

	host, ok := body.Hosts["github.com"]
	assert.True(ok)

	// GQL fields should be populated.
	assert.Equal(4800, host.GQLRemaining)
	assert.Equal(5000, host.GQLLimit)
	assert.True(host.GQLKnown)
	assert.NotEmpty(host.GQLResetAt)
}

func TestAPIRateLimitsGQLDefaultsUnknown(t *testing.T) {
	assert := Assert.New(t)

	database := dbtest.Open(t)

	rt := ghclient.NewRateTracker(database, "github.com", "rest")
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

	srv := New(database, syncer, nil, "/", nil, ServerOptions{})
	ts := httptest.NewServer(srv)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/v1/rate-limits")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(200, resp.StatusCode)

	var body rateLimitsResponse
	err = json.NewDecoder(resp.Body).Decode(&body)
	require.NoError(t, err)

	host := body.Hosts["github.com"]
	assert.Equal(-1, host.GQLRemaining)
	assert.Equal(-1, host.GQLLimit)
	assert.False(host.GQLKnown)
	assert.Empty(host.GQLResetAt)
}

func TestAPIRateLimitsMultiHostMixed(t *testing.T) {
	assert := Assert.New(t)

	database := dbtest.Open(t)

	// Two hosts: github.com has GQL data, ghe.example.com does not.
	ghRT := ghclient.NewRateTracker(database, "github.com", "rest")
	gheRT := ghclient.NewRateTracker(database, "ghe.example.com", "rest")
	gqlRT := ghclient.NewRateTracker(database, "github.com", "graphql")
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
			{Owner: "acme", Name: "widget", PlatformHost: "github.com"},
			{Owner: "corp", Name: "internal", PlatformHost: "ghe.example.com"},
		},
		time.Minute,
		map[string]*ghclient.RateTracker{
			"github.com":      ghRT,
			"ghe.example.com": gheRT,
		},
		nil,
	)

	fetcher := ghclient.NewGraphQLFetcher("token", "github.com", gqlRT, nil)
	syncer.SetFetchers(map[string]*ghclient.GraphQLFetcher{
		"github.com": fetcher,
	})

	srv := New(database, syncer, nil, "/", nil, ServerOptions{})
	ts := httptest.NewServer(srv)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/v1/rate-limits")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(200, resp.StatusCode)

	var body rateLimitsResponse
	err = json.NewDecoder(resp.Body).Decode(&body)
	require.NoError(t, err)

	// Both hosts present.
	assert.Len(body.Hosts, 2)

	// github.com has GQL data.
	ghHost := body.Hosts["github.com"]
	assert.True(ghHost.GQLKnown)
	assert.Equal(4500, ghHost.GQLRemaining)
	assert.Equal(5000, ghHost.GQLLimit)

	// ghe.example.com has no GQL fetcher — defaults to unknown.
	gheHost := body.Hosts["ghe.example.com"]
	assert.Equal(-1, gheHost.GQLRemaining)
	assert.Equal(-1, gheHost.GQLLimit)
	assert.False(gheHost.GQLKnown)
}

func TestAPIRateLimitsScopesSameHostByProvider(t *testing.T) {
	assert := Assert.New(t)

	database := dbtest.Open(t)

	host := "code.example.com"
	ghRT := ghclient.NewPlatformRateTracker(database, "github", host, "rest")
	glRT := ghclient.NewPlatformRateTracker(database, "gitlab", host, "rest")
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
			ghclient.RateBucketKey("github", host): ghRT,
			ghclient.RateBucketKey("gitlab", host): glRT,
		},
		nil,
	)
	t.Cleanup(syncer.Stop)

	srv := New(database, syncer, nil, "/", nil, ServerOptions{})
	ts := httptest.NewServer(srv)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/v1/rate-limits")
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(200, resp.StatusCode)

	var body rateLimitsResponse
	err = json.NewDecoder(resp.Body).Decode(&body)
	require.NoError(t, err)

	ghStatus, ok := body.Hosts[host]
	assert.True(ok)
	assert.Equal("github", ghStatus.Provider)
	assert.Equal(host, ghStatus.PlatformHost)
	assert.Equal(1, ghStatus.RequestsHour)

	glStatus, ok := body.Hosts["gitlab:"+host]
	assert.True(ok)
	assert.Equal("gitlab", glStatus.Provider)
	assert.Equal(host, glStatus.PlatformHost)
	assert.Equal(2, glStatus.RequestsHour)
}

func TestAPIGetPullDetailLoaded(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)

	srv, database := setupTestServer(t)
	seedPR(t, database, "acme", "widget", 1)
	client := setupTestClient(t, srv)

	// Before detail fetch: detail_loaded=false.
	resp, err := client.HTTP.GetPullWithResponse(
		t.Context(), "gh", "acme", "widget", 1,
	)
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode())
	require.NotNil(resp.JSON200)
	assert.False(resp.JSON200.DetailLoaded)
	assert.Nil(resp.JSON200.DetailFetchedAt)

	// Insert a second PR with DetailFetchedAt set.
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Second)
	repoID, err := database.UpsertRepo(ctx, db.GitHubRepoIdentity("github.com", "acme", "widget"))
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

	resp2, err := client.HTTP.GetPullWithResponse(
		t.Context(), "gh", "acme", "widget", 2,
	)
	require.NoError(err)
	require.Equal(http.StatusOK, resp2.StatusCode())
	require.NotNil(resp2.JSON200)
	assert.True(resp2.JSON200.DetailLoaded)
	require.NotNil(resp2.JSON200.DetailFetchedAt)
	assertRFC3339UTC(t, *resp2.JSON200.DetailFetchedAt, now)
}

func TestAPIGetPullDetailIncludesDiffSummaryRevisionFields(t *testing.T) {
	require := require.New(t)

	srv, database := setupTestServer(t)
	ctx := t.Context()
	repoID, err := database.UpsertRepo(ctx, db.GitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)

	now := time.Now().UTC().Truncate(time.Second)
	_, err = database.UpsertMergeRequest(ctx, &db.MergeRequest{
		RepoID:          repoID,
		PlatformID:      1000,
		Number:          1,
		URL:             "https://github.com/acme/widget/pull/1",
		Title:           "Test PR #1",
		Author:          "testuser",
		State:           "open",
		HeadBranch:      "feature",
		BaseBranch:      "main",
		PlatformHeadSHA: "platform-head",
		PlatformBaseSHA: "platform-base",
		CreatedAt:       now,
		UpdatedAt:       now,
		LastActivityAt:  now,
	})
	require.NoError(err)
	require.NoError(database.UpdateDiffSHAs(ctx, repoID, 1, "diff-head", "diff-base", "merge-base"))

	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/pulls/gh/acme/widget/1", nil)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	require.Equal(http.StatusOK, rr.Code)
	body := rr.Body.String()
	require.Contains(body, `"platform_head_sha":"platform-head"`)
	require.Contains(body, `"platform_base_sha":"platform-base"`)
	require.Contains(body, `"diff_head_sha":"diff-head"`)
	require.Contains(body, `"merge_base_sha":"merge-base"`)
}

func TestAPIActivityReturnsUTCCreatedAt(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)

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
	resp, err := client.HTTP.ListActivityWithResponse(
		ctx, &generated.ListActivityParams{Since: &since},
	)
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode())
	require.NotNil(resp.JSON200)
	require.NotNil(resp.JSON200.Items)
	require.NotEmpty(*resp.JSON200.Items)

	var commentItem *generated.ActivityItemResponse
	for i := range *resp.JSON200.Items {
		item := &(*resp.JSON200.Items)[i]
		if item.Author == "reviewer" && item.ActivityType == "comment" {
			commentItem = item
			break
		}
	}
	require.NotNil(commentItem)
	assertRFC3339UTC(t, commentItem.CreatedAt, createdAt)
	assert.Equal("reviewer", commentItem.Author)
	assert.Equal("comment", commentItem.ActivityType)
}

func TestAPIActivityStartupRepairsLegacyTimestampStorage(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)
	ctx := t.Context()
	dir := t.TempDir()
	path := filepath.Join(dir, "test.db")

	database := dbtest.OpenAt(t, path)

	repoID, err := database.UpsertRepo(ctx, db.GitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	prID, err := database.UpsertMergeRequest(ctx, &db.MergeRequest{
		RepoID:            repoID,
		PlatformID:        101,
		Number:            1,
		URL:               "https://github.com/acme/widget/pull/1",
		Title:             "Legacy PR",
		Author:            "octocat",
		AuthorDisplayName: "octocat",
		State:             "open",
		HeadBranch:        "feature",
		BaseBranch:        "main",
		CreatedAt:         time.Date(2026, 4, 11, 12, 0, 0, 0, time.UTC),
		UpdatedAt:         time.Date(2026, 4, 11, 12, 0, 0, 0, time.UTC),
		LastActivityAt:    time.Date(2026, 4, 11, 12, 0, 0, 0, time.UTC),
	})
	require.NoError(err)
	issueID, err := database.UpsertIssue(ctx, &db.Issue{
		RepoID:         repoID,
		PlatformID:     201,
		Number:         2,
		URL:            "https://github.com/acme/widget/issues/2",
		Title:          "Legacy issue",
		Author:         "octocat",
		State:          "open",
		CreatedAt:      time.Date(2026, 4, 11, 12, 0, 0, 0, time.UTC),
		UpdatedAt:      time.Date(2026, 4, 11, 12, 0, 0, 0, time.UTC),
		LastActivityAt: time.Date(2026, 4, 11, 12, 0, 0, 0, time.UTC),
	})
	require.NoError(err)
	require.NoError(database.UpsertMREvents(ctx, []db.MREvent{{
		MergeRequestID: prID,
		EventType:      "issue_comment",
		Author:         "pr-reviewer",
		Body:           "PR comment",
		CreatedAt:      time.Date(2026, 4, 11, 12, 0, 0, 0, time.UTC),
		DedupeKey:      "comment-pr-legacy",
	}}))
	require.NoError(database.UpsertIssueEvents(ctx, []db.IssueEvent{{
		IssueID:   issueID,
		EventType: "issue_comment",
		Author:    "issue-reporter",
		Body:      "Issue comment",
		CreatedAt: time.Date(2026, 4, 11, 13, 0, 0, 0, time.UTC),
		DedupeKey: "comment-issue-legacy",
	}}))
	require.NoError(database.Close())

	raw, err := sql.Open("sqlite", path)
	require.NoError(err)
	_, err = raw.ExecContext(ctx,
		`UPDATE middleman_mr_events SET created_at = ? WHERE dedupe_key = ?`,
		"2026-04-11 08:00:00 -0400 EDT",
		"comment-pr-legacy",
	)
	require.NoError(err)
	_, err = raw.ExecContext(ctx,
		`UPDATE middleman_issue_events SET created_at = ? WHERE dedupe_key = ?`,
		"2026-04-11 09:00:00 -0400 EDT",
		"comment-issue-legacy",
	)
	require.NoError(err)
	_, err = raw.ExecContext(ctx, `
		DROP TRIGGER IF EXISTS middleman_workspaces_casefold_update;
		DROP TRIGGER IF EXISTS middleman_workspaces_casefold_insert;

		DROP INDEX IF EXISTS middleman_workspace_setup_events_workspace_id_idx;
		DROP TABLE IF EXISTS middleman_workspace_setup_events;

		ALTER TABLE middleman_workspaces
			RENAME TO middleman_workspaces_v11;

		CREATE TABLE middleman_workspaces (
		    id            TEXT PRIMARY KEY,
		    platform_host TEXT NOT NULL,
		    repo_owner    TEXT NOT NULL,
		    repo_name     TEXT NOT NULL,
		    mr_number     INTEGER NOT NULL,
		    mr_head_ref   TEXT NOT NULL,
		    mr_head_repo  TEXT,
		    worktree_path TEXT NOT NULL,
		    tmux_session  TEXT NOT NULL,
		    status        TEXT NOT NULL DEFAULT 'creating',
		    error_message TEXT,
		    created_at    DATETIME NOT NULL DEFAULT (datetime('now')),
		    UNIQUE(platform_host, repo_owner, repo_name, mr_number)
		);

		INSERT INTO middleman_workspaces (
		    id, platform_host, repo_owner, repo_name,
		    mr_number, mr_head_ref, mr_head_repo,
		    worktree_path, tmux_session, status,
		    error_message, created_at
		)
		SELECT
		    id, platform_host, repo_owner, repo_name,
		    item_number, git_head_ref, mr_head_repo,
		    worktree_path, tmux_session, status,
		    error_message, created_at
		FROM middleman_workspaces_v11;

		DROP TABLE middleman_workspaces_v11;

		CREATE TRIGGER middleman_workspaces_casefold_insert
		BEFORE INSERT ON middleman_workspaces
		WHEN NEW.platform_host <> lower(NEW.platform_host)
		  OR NEW.repo_owner <> lower(NEW.repo_owner)
		  OR NEW.repo_name <> lower(NEW.repo_name)
		BEGIN
		    SELECT RAISE(ABORT, 'workspace repo identifiers must be lowercase');
		END;

		CREATE TRIGGER middleman_workspaces_casefold_update
		BEFORE UPDATE OF platform_host, repo_owner, repo_name ON middleman_workspaces
		WHEN NEW.platform_host <> lower(NEW.platform_host)
		  OR NEW.repo_owner <> lower(NEW.repo_owner)
		  OR NEW.repo_name <> lower(NEW.repo_name)
			BEGIN
			    SELECT RAISE(ABORT, 'workspace repo identifiers must be lowercase');
			END;

			DROP TRIGGER IF EXISTS middleman_repos_casefold_update;
			DROP TRIGGER IF EXISTS middleman_repos_casefold_insert;
			DROP INDEX IF EXISTS idx_issue_events_platform_external_id;
			DROP INDEX IF EXISTS idx_mr_events_platform_external_id;
			DROP INDEX IF EXISTS idx_labels_repo_platform_external_id;
			DROP INDEX IF EXISTS idx_issues_repo_platform_external_id;
			DROP INDEX IF EXISTS idx_merge_requests_repo_platform_external_id;
			DROP INDEX IF EXISTS idx_repos_provider_path_key;
			DROP INDEX IF EXISTS idx_repos_platform_repo_id;
			DROP INDEX IF EXISTS idx_labels_repo_catalog_name;
			ALTER TABLE middleman_repos DROP COLUMN label_catalog_sync_error;
			ALTER TABLE middleman_repos DROP COLUMN label_catalog_checked_at;
			ALTER TABLE middleman_repos DROP COLUMN label_catalog_synced_at;
			ALTER TABLE middleman_repos DROP COLUMN viewer_can_merge;
			ALTER TABLE middleman_labels DROP COLUMN catalog_seen_at;
			ALTER TABLE middleman_labels DROP COLUMN catalog_present;
			ALTER TABLE middleman_merge_requests DROP COLUMN is_locked;
			ALTER TABLE middleman_mr_events DROP COLUMN platform_external_id;
			ALTER TABLE middleman_labels DROP COLUMN platform_external_id;
			ALTER TABLE middleman_issues DROP COLUMN platform_external_id;
			ALTER TABLE middleman_merge_requests DROP COLUMN platform_external_id;
			ALTER TABLE middleman_merge_requests DROP COLUMN workflow_approval_checked_at;
			ALTER TABLE middleman_merge_requests DROP COLUMN workflow_approval_head_sha;
			ALTER TABLE middleman_merge_requests DROP COLUMN workflow_approval_required;
			ALTER TABLE middleman_merge_requests DROP COLUMN workflow_approval_count;
			ALTER TABLE middleman_repos DROP COLUMN default_branch;
			ALTER TABLE middleman_repos DROP COLUMN clone_url;
			ALTER TABLE middleman_repos DROP COLUMN web_url;
			ALTER TABLE middleman_repos DROP COLUMN repo_path_key;
			ALTER TABLE middleman_repos DROP COLUMN name_key;
			ALTER TABLE middleman_repos DROP COLUMN owner_key;
			ALTER TABLE middleman_repos DROP COLUMN repo_path;
			ALTER TABLE middleman_repos DROP COLUMN platform_repo_id;

			DROP INDEX IF EXISTS idx_mr_events_thread;
			ALTER TABLE middleman_mr_events DROP COLUMN thread_id;
			ALTER TABLE middleman_mr_events DROP COLUMN position_json;
			ALTER TABLE middleman_mr_events DROP COLUMN resolvable;
			ALTER TABLE middleman_mr_events DROP COLUMN resolved;
			ALTER TABLE middleman_issue_events DROP COLUMN thread_id;
		`)
	require.NoError(err)
	_, err = raw.ExecContext(ctx,
		`UPDATE schema_migrations SET version = ?, dirty = FALSE`,
		9,
	)
	require.NoError(err)
	require.NoError(raw.Close())

	reopened := dbtest.OpenWithMigrationsAt(t, path)

	srv := setupTestServerWithDatabase(t, reopened, defaultTestRepos)
	client := setupTestClient(t, srv)

	since := "2026-04-11T11:30:00Z"
	resp, err := client.HTTP.ListActivityWithResponse(
		ctx, &generated.ListActivityParams{Since: &since},
	)
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode())
	require.NotNil(resp.JSON200)
	require.NotNil(resp.JSON200.Items)
	commentItems := make([]generated.ActivityItemResponse, 0, 2)
	for _, item := range *resp.JSON200.Items {
		if item.ActivityType == "comment" {
			commentItems = append(commentItems, item)
		}
	}
	require.Len(commentItems, 2)
	assert.Equal("issue-reporter", commentItems[0].Author)
	assert.Equal("pr-reviewer", commentItems[1].Author)
	assertRFC3339UTC(t, commentItems[0].CreatedAt, time.Date(2026, 4, 11, 13, 0, 0, 0, time.UTC))
	assertRFC3339UTC(t, commentItems[1].CreatedAt, time.Date(2026, 4, 11, 12, 0, 0, 0, time.UTC))

	since = "2026-04-11T12:30:00Z"
	resp, err = client.HTTP.ListActivityWithResponse(
		ctx, &generated.ListActivityParams{Since: &since},
	)
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode())
	require.NotNil(resp.JSON200)
	require.NotNil(resp.JSON200.Items)
	require.Len(*resp.JSON200.Items, 1)
	assert.Equal("issue-reporter", (*resp.JSON200.Items)[0].Author)
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := procutil.Command("git", append([]string{"-c", "init.defaultBranch=main"}, args...)...)
	cmd.Dir = dir
	cmd.Env = append(gitenv.StripAll(os.Environ()), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %v failed: %s", args, out)
}

func testGitSHA(t *testing.T, dir, ref string) string {
	t.Helper()
	cmd := procutil.Command("git", "rev-parse", ref)
	cmd.Dir = dir
	cmd.Env = append(gitenv.StripAll(os.Environ()), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	out, err := cmd.Output()
	require.NoError(t, err)
	return strings.TrimSpace(string(out))
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
	srv *Server,
) {
	t.Helper()

	dir := t.TempDir()
	database = dbtest.Open(t)

	bareDir := filepath.Join(dir, "clones")
	require.NoError(t, os.MkdirAll(bareDir, 0o755))
	bare := filepath.Join(bareDir, "github.com", "acme", "widget.git")

	tmpWork := filepath.Join(dir, "work")
	runGit(t, dir, "init", "--bare", "--initial-branch=main", bare)
	runGit(t, dir, "clone", bare, tmpWork)
	runGit(t, tmpWork, "config", "user.email", "test@test.com")
	runGit(t, tmpWork, "config", "user.name", "Test")

	require.NoError(t, os.WriteFile(filepath.Join(tmpWork, "base.txt"), []byte("base\n"), 0o644))
	runGit(t, tmpWork, "add", ".")
	runGit(t, tmpWork, "commit", "-m", "base commit")
	runGit(t, tmpWork, "push", "origin", "main")
	mergeBase = testGitSHA(t, tmpWork, "HEAD")

	runGit(t, tmpWork, "checkout", "-b", "pr")
	for i := 1; i <= 5; i++ {
		fname := fmt.Sprintf("file%d.txt", i)
		require.NoError(t, os.WriteFile(filepath.Join(tmpWork, fname), fmt.Appendf(nil, "content %d\n", i), 0o644))
		runGit(t, tmpWork, "add", ".")
		runGit(t, tmpWork, "commit", "-m", fmt.Sprintf("commit %d", i))
	}
	runGit(t, tmpWork, "push", "origin", "pr")
	headSHA = testGitSHA(t, tmpWork, "HEAD")

	// Collect SHAs newest-first.
	commitSHAs = make([]string, 5)
	sha := headSHA
	for i := range 5 {
		commitSHAs[i] = sha
		sha = testGitSHA(t, tmpWork, sha+"^1")
	}

	clones := gitclone.New(bareDir, nil)
	mock := &mockGH{}
	repos := []ghclient.RepoRef{{Owner: "acme", Name: "widget", PlatformHost: "github.com"}}
	syncer := ghclient.NewSyncer(map[string]ghclient.Client{"github.com": mock}, database, nil, repos, time.Minute, nil, nil)
	t.Cleanup(syncer.Stop)
	srv = New(database, syncer, nil, "/", nil, ServerOptions{Clones: clones})

	seedPR(t, database, "acme", "widget", 1)
	ctx := t.Context()
	repoID, err := database.UpsertRepo(ctx, db.GitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(t, err)
	require.NoError(t, database.UpdateDiffSHAs(ctx, repoID, 1, headSHA, mergeBase, mergeBase))

	client = setupTestClient(t, srv)
	return client, database, mergeBase, headSHA, commitSHAs, srv
}

func TestAPIGetCommits(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)

	client, _, _, _, commitSHAs := setupTestServerWithClones(t)
	resp, err := client.HTTP.GetPullCommitsWithResponse(
		t.Context(), "gh", "acme", "widget", 1,
	)
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode())
	require.NotNil(resp.JSON200)
	assert.Len(*resp.JSON200.Commits, 5)
	assert.Equal(commitSHAs[0], (*resp.JSON200.Commits)[0].Sha)
	assert.Equal("commit 5", (*resp.JSON200.Commits)[0].Message)
	assert.Equal(time.UTC, (*resp.JSON200.Commits)[0].AuthoredAt.Location())
}

func TestAPIGetCommits_NotFound(t *testing.T) {
	client, _, _, _, _ := setupTestServerWithClones(t)

	resp, err := client.HTTP.GetPullCommitsWithResponse(
		t.Context(), "gh", "acme", "widget", 999,
	)
	require.NoError(t, err)
	require.Equal(t, http.StatusNotFound, resp.StatusCode())
}

func TestAPIGetDiff_SingleCommit(t *testing.T) {
	require := require.New(t)

	client, _, _, _, commitSHAs := setupTestServerWithClones(t)
	resp, err := client.HTTP.GetPullDiffWithResponse(
		t.Context(), "gh", "acme", "widget", 1,
		&generated.GetPullDiffParams{Commit: &commitSHAs[2]},
	)
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode())
	require.NotNil(resp.JSON200)
	require.Len(*resp.JSON200.Files, 1)
}

func TestAPIGetRepoCommitDiff(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)

	_, _, _, _, commitSHAs, srv := setupTestServerWithClonesAndServer(t)
	req := httptest.NewRequest(
		http.MethodGet,
		"/api/v1/repo/gh/acme/widget/commits/"+commitSHAs[2]+"/diff",
		nil,
	)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	resp := rr.Result()
	defer resp.Body.Close()

	require.Equal(http.StatusOK, resp.StatusCode)
	var body diffResponse
	require.NoError(json.NewDecoder(resp.Body).Decode(&body))
	require.Len(body.Files, 1)
	assert.False(body.Stale)
	assert.Equal("file3.txt", body.Files[0].Path)
	assert.Equal("added", body.Files[0].Status)
	require.NotEmpty(body.Files[0].Hunks)
	require.NotEmpty(body.Files[0].Hunks[0].Lines)
	assert.Equal("content 3", body.Files[0].Hunks[0].Lines[0].Content)
}

func TestAPIGetRepoCommitDiffRejectsOptionLikeSHA(t *testing.T) {
	require := require.New(t)

	_, _, _, _, _, srv := setupTestServerWithClonesAndServer(t)
	clonePath, err := srv.clones.ClonePath("github.com", "acme", "widget")
	require.NoError(err)
	configPath := filepath.Join(clonePath, "config")
	before, err := os.ReadFile(configPath)
	require.NoError(err)

	req := httptest.NewRequest(
		http.MethodGet,
		"/api/v1/repo/gh/acme/widget/commits/--output=config/diff",
		nil,
	)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	require.Equal(http.StatusBadRequest, rr.Code)
	after, err := os.ReadFile(configPath)
	require.NoError(err)
	require.Equal(before, after)
}

func TestAPIGetFilePreview_ReturnsHeadContent(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)

	_, _, _, _, _, srv := setupTestServerWithClonesAndServer(t)
	req := httptest.NewRequest(
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
	require := require.New(t)
	assert := Assert.New(t)
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
	srv := New(database, syncer, nil, "/", nil, ServerOptions{
		Clones: diffRepo.Manager,
	})
	t.Cleanup(func() { gracefulShutdown(t, srv) })
	client := setupTestClient(t, srv)

	path := "config.yaml"
	resp, err := client.HTTP.GetPullFilePreviewWithResponse(
		ctx, "gh", "acme", "widgets", 1,
		&generated.GetPullFilePreviewParams{Path: &path},
	)

	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode())
	require.NotNil(resp.JSON200)
	assert.Equal(path, resp.JSON200.Path)
	decoded, err := base64.StdEncoding.DecodeString(resp.JSON200.Content)
	require.NoError(err)
	assert.Contains(string(decoded), "wal_mode: true")
}

func TestAPIGetDiff_Range(t *testing.T) {
	require := require.New(t)

	client, _, _, _, commitSHAs := setupTestServerWithClones(t)
	from := commitSHAs[4] // commit 1 (oldest)
	to := commitSHAs[2]   // commit 3
	resp, err := client.HTTP.GetPullDiffWithResponse(
		t.Context(), "gh", "acme", "widget", 1,
		&generated.GetPullDiffParams{From: &from, To: &to},
	)
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode())
	require.NotNil(resp.JSON200)
	require.Len(*resp.JSON200.Files, 3)
}

func TestAPIGetDiff_InvalidScope(t *testing.T) {
	client, _, _, _, commitSHAs := setupTestServerWithClones(t)
	from := commitSHAs[0]
	resp, err := client.HTTP.GetPullDiffWithResponse(
		t.Context(), "gh", "acme", "widget", 1,
		&generated.GetPullDiffParams{Commit: &commitSHAs[0], From: &from},
	)
	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, resp.StatusCode())
}

func TestAPIGetDiff_UnknownSHA(t *testing.T) {
	client, _, _, _, _ := setupTestServerWithClones(t)
	bogus := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	resp, err := client.HTTP.GetPullDiffWithResponse(
		t.Context(), "gh", "acme", "widget", 1,
		&generated.GetPullDiffParams{Commit: &bogus},
	)
	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, resp.StatusCode())
}

func TestAPIGetDiff_ReversedRange(t *testing.T) {
	client, _, _, _, commitSHAs := setupTestServerWithClones(t)
	from := commitSHAs[0] // newest
	to := commitSHAs[4]   // oldest
	resp, err := client.HTTP.GetPullDiffWithResponse(
		t.Context(), "gh", "acme", "widget", 1,
		&generated.GetPullDiffParams{From: &from, To: &to},
	)
	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, resp.StatusCode())
}

func TestAPIGetDiff_FromWithoutTo(t *testing.T) {
	client, _, _, _, commitSHAs := setupTestServerWithClones(t)
	from := commitSHAs[0]
	resp, err := client.HTTP.GetPullDiffWithResponse(
		t.Context(), "gh", "acme", "widget", 1,
		&generated.GetPullDiffParams{From: &from},
	)
	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, resp.StatusCode())
}

func TestAPIGetDiff_RootCommit(t *testing.T) {
	require := require.New(t)

	dir := t.TempDir()
	database := dbtest.Open(t)

	bareDir := filepath.Join(dir, "clones")
	require.NoError(os.MkdirAll(bareDir, 0o755))
	bare := filepath.Join(bareDir, "github.com", "acme", "rootrepo.git")
	tmpWork := filepath.Join(dir, "work")
	runGit(t, dir, "init", "--bare", "--initial-branch=main", bare)
	runGit(t, dir, "clone", bare, tmpWork)
	runGit(t, tmpWork, "config", "user.email", "test@test.com")
	runGit(t, tmpWork, "config", "user.name", "Test")

	require.NoError(os.WriteFile(filepath.Join(tmpWork, "root.txt"), []byte("root\n"), 0o644))
	runGit(t, tmpWork, "add", ".")
	runGit(t, tmpWork, "commit", "-m", "root commit")
	rootSHA := testGitSHA(t, tmpWork, "HEAD")

	require.NoError(os.WriteFile(filepath.Join(tmpWork, "second.txt"), []byte("second\n"), 0o644))
	runGit(t, tmpWork, "add", ".")
	runGit(t, tmpWork, "commit", "-m", "second commit")
	runGit(t, tmpWork, "push", "origin", "main")
	headSHA := testGitSHA(t, tmpWork, "HEAD")

	clones := gitclone.New(bareDir, nil)
	mock := &mockGH{}
	repos := []ghclient.RepoRef{{Owner: "acme", Name: "rootrepo", PlatformHost: "github.com"}}
	syncer := ghclient.NewSyncer(map[string]ghclient.Client{"github.com": mock}, database, nil, repos, time.Minute, nil, nil)
	t.Cleanup(syncer.Stop)
	srv := New(database, syncer, nil, "/", nil, ServerOptions{Clones: clones})

	seedPR(t, database, "acme", "rootrepo", 1)
	ctx := t.Context()
	repoID, err := database.UpsertRepo(ctx, db.GitHubRepoIdentity("github.com", "acme", "rootrepo"))
	require.NoError(err)
	require.NoError(database.UpdateDiffSHAs(ctx, repoID, 1, headSHA, "4b825dc642cb6eb9a060e54bf8d69288fbee4904", "4b825dc642cb6eb9a060e54bf8d69288fbee4904"))

	client := setupTestClient(t, srv)
	resp, err := client.HTTP.GetPullDiffWithResponse(
		t.Context(), "gh", "acme", "rootrepo", 1,
		&generated.GetPullDiffParams{Commit: &rootSHA},
	)
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode())
	require.NotNil(resp.JSON200)
}

func TestAPIListActivity(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	srv, database := setupTestServer(t)
	client := setupTestClient(t, srv)

	prID := seedPR(t, database, "acme", "widget", 1)
	ctx := t.Context()

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
	resp, err := client.HTTP.ListActivityWithResponse(
		ctx, &generated.ListActivityParams{Since: &since},
	)
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode())
	require.NotNil(resp.JSON200)
	require.NotNil(resp.JSON200.Items)
	assert.NotEmpty(*resp.JSON200.Items,
		"activity feed should contain PR and comment items")
	assert.Equal("github.com", (*resp.JSON200.Items)[0].PlatformHost)
}

func TestAPIListActivityReturnsDefaultBranchActivity(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	srv, database := setupTestServer(t)
	ctx := t.Context()
	base := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	repoID, err := database.UpsertRepo(ctx, db.GitHubRepoIdentity("github.com", "acme", "widget"))
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
	rr := doJSON(t, srv, http.MethodGet, "/api/v1/activity?since="+since, nil)
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
	assert.Equal(formatUTCRFC3339(detectedAt), forcePush["created_at"])

	commit := body.Items[1]
	assert.Equal("default_branch_commit", commit["activity_type"])
	assert.Equal("main", commit["branch_name"])
	assert.Equal("abc123def456abc123def456abc123def456abcd", commit["commit_sha"])
	assert.Equal("Commit Author", commit["author_name"])
	assert.Equal("author@example.com", commit["author_email"])
	assert.Equal("Committer Person", commit["committer_name"])
	assert.Equal("committer@example.com", commit["committer_email"])
	assert.Equal(formatUTCRFC3339(committedAt), commit["committed_at"])
	assert.Equal("https://github.com/acme/widget/commit/abc123def456abc123def456abc123def456abcd", commit["activity_url"])
	assert.Empty(commit["item_type"])
	assert.Zero(commit["item_number"])
}

func TestAPIListActivityCapsDefaultBranchCommitMetadata(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	srv, database := setupTestServer(t)
	ctx := t.Context()
	base := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	repoID, err := database.UpsertRepo(ctx, db.GitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)

	committedAt := base.Add(10 * time.Minute)
	_, err = database.WriteDB().ExecContext(ctx, `
		INSERT INTO middleman_branch_commits (
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
	rr := doJSON(t, srv, http.MethodGet, "/api/v1/activity?since="+since, nil)
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
	assert := Assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	dir := t.TempDir()
	remote := filepath.Join(dir, "remote.git")
	work := filepath.Join(dir, "work")
	runGit(t, dir, "init", "--bare", "--initial-branch=main", remote)
	runGit(t, dir, "clone", remote, work)
	runGit(t, work, "config", "user.email", "alice@example.com")
	runGit(t, work, "config", "user.name", "Alice")

	shas := map[string]string{}
	for _, subject := range []string{"oldest", "third", "second", "newest"} {
		require.NoError(os.WriteFile(
			filepath.Join(work, subject+".txt"),
			[]byte(subject+"\n"),
			0o644,
		))
		runGit(t, work, "add", ".")
		runGit(t, work, "commit", "-m", subject)
		shas[subject] = testGitSHA(t, work, "HEAD")
	}
	runGit(t, work, "push", "origin", "main")

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
	srv := New(database, syncer, nil, "/", nil, ServerOptions{Clones: clones})
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
	resp, err := client.HTTP.ListActivityWithResponse(
		ctx, &generated.ListActivityParams{Since: &since, Types: &types},
	)
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode())
	require.NotNil(resp.JSON200)
	require.NotNil(resp.JSON200.Items)
	require.Len(*resp.JSON200.Items, 2)
	gotAPI := []string{
		*(*resp.JSON200.Items)[0].CommitSha,
		*(*resp.JSON200.Items)[1].CommitSha,
	}
	assert.ElementsMatch([]string{shas["newest"], shas["second"]}, gotAPI)
}

func TestAPIListActivityReturnsProviderCompareURLsForDefaultBranchForcePushes(t *testing.T) {
	assert := Assert.New(t)
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
			identity: db.GitHubRepoIdentity("github.com", "acme", "github-widget"),
			wantURL:  "https://github.com/acme/github-widget/compare/" + beforeSHA + "..." + afterSHA,
		},
		{
			name: "forgejo",
			identity: db.RepoIdentity{
				Platform:     "forgejo",
				PlatformHost: "codeberg.org",
				Owner:        "acme",
				Name:         "forgejo-widget",
			},
			wantURL: "https://codeberg.org/acme/forgejo-widget/compare/" + beforeSHA + "..." + afterSHA,
		},
		{
			name: "gitea",
			identity: db.RepoIdentity{
				Platform:     "gitea",
				PlatformHost: "gitea.com",
				Owner:        "acme",
				Name:         "gitea-widget",
			},
			wantURL: "https://gitea.com/acme/gitea-widget/compare/" + beforeSHA + "..." + afterSHA,
		},
		{
			name: "gitlab",
			identity: db.RepoIdentity{
				Platform:     "gitlab",
				PlatformHost: "gitlab.com",
				Owner:        "acme/platform",
				Name:         "gitlab-widget",
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
	rr := doJSON(t, srv, http.MethodGet, "/api/v1/activity?since="+since, nil)
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
	assert := Assert.New(t)
	require := require.New(t)
	srv, database := setupTestServer(t)
	client := setupTestClient(t, srv)
	ctx := t.Context()
	base := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	repoID, err := database.UpsertRepo(ctx, db.GitHubRepoIdentity("github.com", "acme", "widget"))
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
	resp, err := client.HTTP.ListActivityWithResponse(
		ctx, &generated.ListActivityParams{Since: &since, Types: &types},
	)
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode())
	require.NotNil(resp.JSON200)
	require.NotNil(resp.JSON200.Items)
	require.Len(*resp.JSON200.Items, 1)
	assert.Equal("new_pr", (*resp.JSON200.Items)[0].ActivityType)
	assert.Equal(int64(1), (*resp.JSON200.Items)[0].ItemNumber)
}

func TestAPIListActivityAcceptsHostQualifiedRepoFilter(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	srv, database := setupTestServer(t)
	client := setupTestClient(t, srv)

	seedPROnHost(t, database, "github.com", "acme", "widget", 1)
	seedPROnHost(t, database, "ghe.example.com", "acme", "widget", 2)

	since := time.Now().UTC().AddDate(0, 0, -7).Format(time.RFC3339)
	repo := "ghe.example.com/acme/widget"
	resp, err := client.HTTP.ListActivityWithResponse(
		t.Context(), &generated.ListActivityParams{Since: &since, Repo: &repo},
	)
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode())
	require.NotNil(resp.JSON200)
	require.NotNil(resp.JSON200.Items)
	require.NotEmpty(*resp.JSON200.Items)
	for _, item := range *resp.JSON200.Items {
		assert.Equal("ghe.example.com", item.PlatformHost)
		assert.Equal("acme", item.RepoOwner)
		assert.Equal("widget", item.RepoName)
	}
}

func TestAPIListActivityFiltersConfiguredReposByHost(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	srv, database, _ := setupTestServerWithConfig(t)

	seedPROnHost(t, database, "github.com", "acme", "widget", 1)
	seedPROnHost(t, database, "ghe.example.com", "acme", "widget", 2)

	since := time.Now().UTC().AddDate(0, 0, -7).Format(time.RFC3339)
	rr := doJSON(t, srv, http.MethodGet, "/api/v1/activity?since="+since, nil)
	require.Equal(http.StatusOK, rr.Code)
	var body activityResponse
	require.NoError(json.NewDecoder(rr.Body).Decode(&body))
	require.NotEmpty(body.Items)
	for _, item := range body.Items {
		assert.Equal("github.com", item.PlatformHost)
		assert.Equal("acme", item.RepoOwner)
		assert.Equal("widget", item.RepoName)
	}
}

// --- Stacks E2E ---

func seedStackedPR(
	t *testing.T, database *db.DB,
	owner, name string, number int,
	head, base string, state db.MergeRequestState, ci, review string,
) int64 {
	return seedStackedPRDraft(t, database, owner, name, number, head, base, state, ci, review, false)
}

func seedStackedPRDraft(
	t *testing.T, database *db.DB,
	owner, name string, number int,
	head, base string, state db.MergeRequestState, ci, review string,
	isDraft bool,
) int64 {
	t.Helper()
	ctx := t.Context()
	repoID, err := database.UpsertRepo(ctx, db.GitHubRepoIdentity("github.com", owner, name))
	require.NoError(t, err)
	now := time.Now().UTC().Truncate(time.Second)
	pr := &db.MergeRequest{
		RepoID:         repoID,
		PlatformID:     int64(number) * 1000,
		Number:         number,
		Title:          fmt.Sprintf("PR #%d: %s", number, head),
		Author:         "testuser",
		State:          state,
		IsDraft:        isDraft,
		HeadBranch:     head,
		BaseBranch:     base,
		CIStatus:       ci,
		ReviewDecision: review,
		CreatedAt:      now,
		UpdatedAt:      now,
		LastActivityAt: now,
	}
	prID, err := database.UpsertMergeRequest(ctx, pr)
	require.NoError(t, err)
	require.NoError(t, database.EnsureKanbanState(ctx, prID))
	return prID
}

func runStackDetection(t *testing.T, database *db.DB, owner, name string) {
	t.Helper()
	ctx := t.Context()
	repo, err := database.GetRepoByOwnerName(ctx, owner, name)
	require.NoError(t, err)
	require.NotNil(t, repo)
	require.NoError(t, stacks.RunDetection(ctx, database, repo.ID))
}

func TestAPIListStacks(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	srv, database := setupTestServer(t)
	client := setupTestClient(t, srv)

	seedStackedPR(t, database, "acme", "widget", 10, "feat/auth", "main", db.MergeRequestStateOpen, "success", "APPROVED")
	seedStackedPR(t, database, "acme", "widget", 11, "feat/auth-retry", "feat/auth", db.MergeRequestStateOpen, "success", "APPROVED")
	seedStackedPR(t, database, "acme", "widget", 12, "feat/auth-ui", "feat/auth-retry", db.MergeRequestStateOpen, "pending", "")
	runStackDetection(t, database, "acme", "widget")

	resp, err := client.HTTP.ListStacksWithResponse(t.Context(), &generated.ListStacksParams{})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode())

	var stks []generated.StackResponse
	require.NoError(json.Unmarshal(resp.Body, &stks))
	assert.Len(stks, 1)
	assert.Equal("auth", stks[0].Name)
	require.NotNil(stks[0].Members)
	assert.Len(*stks[0].Members, 3)
	assert.Equal(int64(10), (*stks[0].Members)[0].Number)
}

func TestAPIListStacks_RepoFilter(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	repos := []ghclient.RepoRef{
		{Owner: "acme", Name: "widget", PlatformHost: "github.com"},
		{Owner: "acme", Name: "tools", PlatformHost: "github.com"},
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

	respAll, err := client.HTTP.ListStacksWithResponse(ctx, &generated.ListStacksParams{})
	require.NoError(err)
	var allStks []generated.StackResponse
	require.NoError(json.Unmarshal(respAll.Body, &allStks))
	assert.Len(allStks, 2)

	repo := "acme/widget"
	resp, err := client.HTTP.ListStacksWithResponse(ctx, &generated.ListStacksParams{Repo: &repo})
	require.NoError(err)
	assert.Equal(http.StatusOK, resp.StatusCode())
	var filtered []generated.StackResponse
	require.NoError(json.Unmarshal(resp.Body, &filtered))
	assert.Len(filtered, 1)
	assert.Equal("widget", filtered[0].RepoName)

	bad := "noslash"
	resp2, err := client.HTTP.ListStacksWithResponse(ctx, &generated.ListStacksParams{Repo: &bad})
	require.NoError(err)
	assert.Equal(http.StatusBadRequest, resp2.StatusCode())
	assert.Contains(string(resp2.Body), "invalid repo filter")
}

func TestAPIGetStackForPR(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	srv, database := setupTestServer(t)
	client := setupTestClient(t, srv)
	ctx := t.Context()

	// Failing base with an open descendant is blocked.
	seedStackedPR(t, database, "acme", "widget", 10, "feat/api-base", "main", db.MergeRequestStateOpen, "failure", "")
	seedStackedPR(t, database, "acme", "widget", 11, "feat/api-retry", "feat/api-base", db.MergeRequestStateOpen, "success", "APPROVED")
	runStackDetection(t, database, "acme", "widget")

	resp, err := client.HTTP.GetPullStackWithResponse(ctx, "gh", "acme", "widget", 10)
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode())
	require.NotNil(resp.JSON200)
	assert.Equal("api", resp.JSON200.StackName)
	assert.Equal(int64(2), resp.JSON200.Size)
	assert.Equal("blocked", resp.JSON200.Health)

	seedPR(t, database, "acme", "widget", 99)
	resp2, err := client.HTTP.GetPullStackWithResponse(ctx, "gh", "acme", "widget", 99)
	require.NoError(err)
	assert.Equal(http.StatusNotFound, resp2.StatusCode())
}

func TestAPIGetStackForPR_DraftNotBaseReady(t *testing.T) {
	assert := Assert.New(t)
	srv, database := setupTestServer(t)
	client := setupTestClient(t, srv)

	// Draft base with green CI + approval; non-draft tip pending.
	seedStackedPRDraft(t, database, "acme", "widget", 10, "feat/x", "main", db.MergeRequestStateOpen, "success", "APPROVED", true)
	seedStackedPR(t, database, "acme", "widget", 11, "feat/y", "feat/x", db.MergeRequestStateOpen, "pending", "")
	runStackDetection(t, database, "acme", "widget")

	resp, err := client.HTTP.GetPullStackWithResponse(t.Context(), "gh", "acme", "widget", 10)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode())
	require.NotNil(t, resp.JSON200)
	assert.NotEqual("base_ready", resp.JSON200.Health, "draft base must not be base_ready")
	assert.NotEqual("all_green", resp.JSON200.Health, "draft stack must not be all_green")
}

func TestAPIListStacks_DraftNotAllGreen(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	srv, database := setupTestServer(t)
	client := setupTestClient(t, srv)

	// Both draft, green CI + approved — must not be all_green.
	seedStackedPRDraft(t, database, "acme", "widget", 10, "feat/a", "main", db.MergeRequestStateOpen, "success", "APPROVED", true)
	seedStackedPRDraft(t, database, "acme", "widget", 11, "feat/b", "feat/a", db.MergeRequestStateOpen, "success", "APPROVED", true)
	runStackDetection(t, database, "acme", "widget")

	resp, err := client.HTTP.ListStacksWithResponse(t.Context(), &generated.ListStacksParams{})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode())

	var stks []generated.StackResponse
	require.NoError(json.Unmarshal(resp.Body, &stks))
	require.Len(stks, 1)
	assert.NotEqual("all_green", stks[0].Health, "all-draft stack must not be all_green")
	assert.NotEqual("base_ready", stks[0].Health, "draft base must not be base_ready")
}

// TestAPIStacks_DetectionViaSyncHook exercises the production wiring:
// SetOnSyncCompleted(stacks.SyncCompletedHook) fires after RunOnce and
// populates stacks without calling RunDetection directly. Verifies that
// GET /stacks and GET /repos/{owner}/{name}/pulls/{number}/stack return
// data produced entirely by the sync-completion callback path.
func TestAPIStacks_DetectionViaSyncHook(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	ctx := t.Context()

	// Build GitHub PRs the mock will return; the sync will persist these
	// into DB as open PRs forming a linear chain.
	now := time.Now().UTC().Truncate(time.Second)
	stringPtr := func(s string) *string { return &s }
	makeGHPR := func(id int64, number int, head, base string) *gh.PullRequest {
		sha := fmt.Sprintf("sha%d", number)
		title := fmt.Sprintf("PR #%d: %s", number, head)
		return &gh.PullRequest{
			ID:        &id,
			Number:    &number,
			State:     stringPtr("open"),
			Title:     &title,
			Body:      stringPtr(""),
			User:      &gh.User{Login: stringPtr("testuser")},
			CreatedAt: &gh.Timestamp{Time: now},
			UpdatedAt: &gh.Timestamp{Time: now},
			Head:      &gh.PullRequestBranch{Ref: &head, SHA: &sha},
			Base:      &gh.PullRequestBranch{Ref: &base, SHA: stringPtr("basesha")},
		}
	}
	mock := &mockGH{
		listOpenPullRequestsFn: func(_ context.Context, _, _ string) ([]*gh.PullRequest, error) {
			return []*gh.PullRequest{
				makeGHPR(1001, 10, "feat/hook-base", "main"),
				makeGHPR(1011, 11, "feat/hook-tip", "feat/hook-base"),
			}, nil
		},
	}
	srv, database := setupTestServerWithMock(t, mock)
	client := setupTestClient(t, srv)

	// Wire the production hook and run one sync pass. RunOnce will fetch
	// from the mock, persist PRs into DB, then invoke OnSyncCompleted,
	// which runs stack detection.
	srv.syncer.SetOnSyncCompleted(stacks.SyncCompletedHook(ctx, database, nil))
	srv.syncer.RunOnce(ctx)

	// Stacks should be populated purely by the hook path.
	listResp, err := client.HTTP.ListStacksWithResponse(ctx, &generated.ListStacksParams{})
	require.NoError(err)
	require.Equal(http.StatusOK, listResp.StatusCode())
	var stks []generated.StackResponse
	require.NoError(json.Unmarshal(listResp.Body, &stks))
	require.Len(stks, 1, "sync-hook detection should produce one stack")
	assert.Equal("hook", stks[0].Name)

	ctxResp, err := client.HTTP.GetPullStackWithResponse(ctx, "gh", "acme", "widget", 10)
	require.NoError(err)
	require.Equal(http.StatusOK, ctxResp.StatusCode())
	require.NotNil(ctxResp.JSON200)
	assert.Equal("hook", ctxResp.JSON200.StackName)
	assert.Equal(int64(2), ctxResp.JSON200.Size)
}

func TestAPIGetStackForPR_SingleFailingIsInProgress(t *testing.T) {
	assert := Assert.New(t)
	srv, database := setupTestServer(t)
	client := setupTestClient(t, srv)

	// 2-PR chain where tip is failing but has no descendants.
	// Per blocked semantics, this is partial_merge when base is merged.
	seedStackedPR(t, database, "acme", "widget", 10, "feat/base", "main", db.MergeRequestStateMerged, "success", "APPROVED")
	seedStackedPR(t, database, "acme", "widget", 11, "feat/tip", "feat/base", db.MergeRequestStateOpen, "failure", "")
	runStackDetection(t, database, "acme", "widget")

	resp, err := client.HTTP.GetPullStackWithResponse(t.Context(), "gh", "acme", "widget", 11)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode())
	require.NotNil(t, resp.JSON200)
	assert.Equal("partial_merge", resp.JSON200.Health,
		"failing tip with merged base and no open descendant is partial_merge, not blocked")
}

func TestAPIGetStackForPR_BaseBranchNotMain(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	srv, database := setupTestServer(t)
	client := setupTestClient(t, srv)

	// Base PR targets "master" not "main" — API must return real base_branch.
	seedStackedPR(t, database, "acme", "widget", 10, "feat/base", "master", db.MergeRequestStateOpen, "success", "APPROVED")
	seedStackedPR(t, database, "acme", "widget", 11, "feat/tip", "feat/base", db.MergeRequestStateOpen, "pending", "")
	runStackDetection(t, database, "acme", "widget")

	resp, err := client.HTTP.GetPullStackWithResponse(t.Context(), "gh", "acme", "widget", 10)
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode())
	require.NotNil(resp.JSON200)
	require.NotNil(resp.JSON200.Members)
	assert.Len(*resp.JSON200.Members, 2)
	assert.Equal("master", (*resp.JSON200.Members)[0].BaseBranch)
	assert.Equal("feat/base", (*resp.JSON200.Members)[1].BaseBranch)
}

func TestAPIListStacks_Empty(t *testing.T) {
	assert := Assert.New(t)
	srv, _ := setupTestServer(t)
	client := setupTestClient(t, srv)

	resp, err := client.HTTP.ListStacksWithResponse(t.Context(), &generated.ListStacksParams{})
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode())

	var stks []generated.StackResponse
	require.NoError(t, json.Unmarshal(resp.Body, &stks))
	assert.Empty(stks)
}

// TestDisplayNameCacheE2E verifies the display-name cache
// through the full stack: sync → SQLite → HTTP API. Two
// RunOnce passes populate and then cache-hit the display name;
// the test asserts /api/v1/pulls returns the expected
// AuthorDisplayName after each pass, and that GetUser is only
// called during the first sync.
func TestDisplayNameCacheE2E(t *testing.T) {
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

	srv, _ := setupTestServerWithMock(t, mock)

	// First sync: populates display name via GetUser.
	srv.syncer.RunOnce(t.Context())
	require.Positive(getUserCalls, "first sync should call GetUser")
	firstCalls := getUserCalls

	// GET /api/v1/pulls — display name must appear.
	rr := doJSON(t, srv, http.MethodGet, "/api/v1/pulls", nil)
	require.Equal(http.StatusOK, rr.Code)
	require.Contains(rr.Body.String(), `"AuthorDisplayName":"Alice Smith"`)

	// Second sync: cache hit, no new GetUser calls.
	srv.syncer.RunOnce(t.Context())
	require.Equal(firstCalls, getUserCalls,
		"second sync must not re-fetch cached display names")

	// GET /api/v1/pulls — display name still present.
	rr2 := doJSON(t, srv, http.MethodGet, "/api/v1/pulls", nil)
	require.Equal(http.StatusOK, rr2.Code)
	require.Contains(rr2.Body.String(), `"AuthorDisplayName":"Alice Smith"`)
}

func TestCICheckDedupLatestRunWinsE2E(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)

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
	seedPR(t, database, "acme", "widget", prNumber)

	resp, err := client.HTTP.SyncPullWithResponse(
		context.Background(), "gh", "acme", "widget", int64(prNumber),
	)
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode())
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

// setupTestServerWithWorkspaces creates a test server wired with
// both a gitclone.Manager and a workspace.Manager backed by a
// bare repo that has a "pr" branch. It seeds a PR in the DB
// and returns the API client and database.
func setupTestServerWithWorkspaces(
	t *testing.T,
) (*apiclient.Client, *db.DB, string, string) {
	t.Helper()
	fixture := setupWorkspaceServerFixture(t, nil)
	return fixture.client, fixture.database, fixture.bare, fixture.remote
}

func setupTestServerWithWorkspacesServer(
	t *testing.T,
	cfg *config.Config,
) (*apiclient.Client, *db.DB, string, string, *Server) {
	t.Helper()
	fixture := setupWorkspaceServerFixture(t, cfg)
	return fixture.client, fixture.database, fixture.bare, fixture.remote, fixture.server
}

type workspaceServerFixture struct {
	server    *Server
	client    *apiclient.Client
	database  *db.DB
	clones    *gitclone.Manager
	worktrees string
	bare      string
	remote    string
}

func setupWorkspaceServerFixture(
	t *testing.T,
	cfg *config.Config,
) workspaceServerFixture {
	return setupWorkspaceServerFixtureWithOptions(t, cfg, ServerOptions{})
}

func setupWorkspaceServerFixtureWithOptions(
	t *testing.T,
	cfg *config.Config,
	options ServerOptions,
) workspaceServerFixture {
	t.Helper()
	return setupWorkspaceServerFixtureWithHostAndOptions(
		t, cfg, "github.com", options,
	)
}

func setupWorkspaceServerFixtureWithHost(
	t *testing.T,
	cfg *config.Config,
	platformHost string,
) workspaceServerFixture {
	t.Helper()
	return setupWorkspaceServerFixtureWithHostAndOptions(
		t, cfg, platformHost, ServerOptions{},
	)
}

func setupWorkspaceServerFixtureWithHostAndOptions(
	t *testing.T,
	cfg *config.Config,
	platformHost string,
	options ServerOptions,
) workspaceServerFixture {
	t.Helper()

	if testing.Short() {
		t.Skip("workspace e2e tests skipped in short mode")
	}

	dir := t.TempDir()
	database := dbtest.Open(t)

	remoteDir := filepath.Join(dir, "remote")
	require.NoError(t, os.MkdirAll(remoteDir, 0o755))
	remote := filepath.Join(remoteDir, "widget.git")
	runGit(
		t, dir, "init", "--bare", "--initial-branch=main", remote,
	)

	tmpWork := filepath.Join(dir, "work")
	runGit(t, dir, "clone", remote, tmpWork)
	runGit(t, tmpWork, "config", "user.email", "test@test.com")
	runGit(t, tmpWork, "config", "user.name", "Test")

	require.NoError(t, os.WriteFile(
		filepath.Join(tmpWork, "base.txt"),
		[]byte("base\n"), 0o644,
	))
	runGit(t, tmpWork, "add", ".")
	runGit(t, tmpWork, "commit", "-m", "base commit")
	runGit(t, tmpWork, "push", "origin", "main")

	runGit(t, tmpWork, "checkout", "-b", "feature")
	require.NoError(t, os.WriteFile(
		filepath.Join(tmpWork, "new.txt"),
		[]byte("new\n"), 0o644,
	))
	runGit(t, tmpWork, "add", ".")
	runGit(t, tmpWork, "commit", "-m", "feature commit")
	runGit(t, tmpWork, "push", "origin", "feature")

	bareDir := filepath.Join(dir, "clones")
	require.NoError(t, os.MkdirAll(bareDir, 0o755))
	bare := filepath.Join(
		bareDir, platformHost, "acme", "widget.git",
	)
	runGit(t, dir, "clone", "--bare", remote, bare)
	runGit(t, bare, "remote", "set-url", "origin", gitLocalRemoteURL(remote))

	clones := gitclone.New(bareDir, nil)
	worktreeDir := filepath.Join(dir, "worktrees")
	mock := &mockGH{}
	repos := []ghclient.RepoRef{
		{Owner: "acme", Name: "widget", PlatformHost: platformHost},
	}
	syncer := ghclient.NewSyncer(
		map[string]ghclient.Client{"github.com": mock},
		database, nil, repos, time.Minute, nil, nil,
	)
	t.Cleanup(syncer.Stop)
	basePath := "/"
	if cfg != nil && cfg.BasePath != "" {
		basePath = cfg.BasePath
	}
	options.Clones = clones
	options.WorktreeDir = worktreeDir
	srv := New(database, syncer, nil, basePath, cfg, options)
	// Cleanup callbacks run LIFO. Drain the server first so async
	// workspace setup cannot create a tmux session after fixture
	// artifact cleanup has listed workspaces. The DB cleanup was
	// registered earlier, so it remains open for artifact cleanup.
	t.Cleanup(func() { cleanupWorkspaceServerFixtureArtifacts(t, srv, database) })
	t.Cleanup(func() { gracefulShutdown(t, srv) })

	seedPROnHost(
		t, database, platformHost, "acme", "widget", 1,
		withSeedPRHeadRepoCloneURL("https://github.com/acme/widget.git"),
	)

	clientBaseURL := "http://middleman.test"
	if basePath != "/" {
		clientBaseURL += strings.TrimSuffix(basePath, "/")
	}
	client := setupTestClientWithBaseURL(t, srv, clientBaseURL)
	return workspaceServerFixture{
		server:    srv,
		client:    client,
		database:  database,
		clones:    clones,
		worktrees: worktreeDir,
		bare:      bare,
		remote:    remote,
	}
}

func cleanupWorkspaceServerFixtureArtifacts(
	t *testing.T,
	srv *Server,
	database *db.DB,
) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(
		t,
		cleanupWorkspaceServerFixtureArtifactsWithContext(ctx, srv, database),
	)
}

func cleanupWorkspaceServerFixtureArtifactsWithContext(
	ctx context.Context,
	srv *Server,
	database *db.DB,
) error {
	if srv.workspaces == nil {
		return nil
	}

	workspaces, err := database.ListWorkspaces(ctx)
	if err != nil {
		return fmt.Errorf("list workspaces: %w", err)
	}
	var errs []error
	for _, ws := range workspaces {
		_, err := func() ([]string, error) {
			beforeDestructive := func(stopCtx context.Context) {
				if srv.runtime != nil {
					srv.runtime.StopWorkspace(stopCtx, ws.ID)
				}
			}
			if srv.runtime != nil {
				srv.runtime.BeginStopping(ws.ID)
				defer srv.runtime.EndStopping(ws.ID)
			}
			return srv.workspaces.Delete(ctx, ws.ID, true, beforeDestructive)
		}()
		if err != nil {
			errs = append(
				errs,
				fmt.Errorf("delete workspace %s: %w", ws.ID, err),
			)
		}
	}
	if err := srv.workspaces.ReapOrphanTmuxSessions(ctx); err != nil {
		errs = append(errs, fmt.Errorf("reap orphan tmux sessions: %w", err))
	}
	return errors.Join(errs...)
}

func waitForWorkspaceReady(
	t *testing.T,
	ctx context.Context,
	client *apiclient.Client,
	wsID string,
) *generated.WorkspaceResponse {
	t.Helper()
	return waitForWorkspaceStatus(t, ctx, client, wsID, "ready")
}

func waitForWorkspaceStatus(
	t *testing.T,
	ctx context.Context,
	client *apiclient.Client,
	wsID string,
	status string,
) *generated.WorkspaceResponse {
	t.Helper()

	waitCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()

	for {
		getResp, err := client.HTTP.GetWorkspaceWithResponse(
			ctx, wsID,
		)
		require.NoError(t, err)
		if getResp.StatusCode() == http.StatusOK &&
			getResp.JSON200 != nil &&
			getResp.JSON200.Status == status {
			return getResp.JSON200
		}

		select {
		case <-waitCtx.Done():
			require.NoError(
				t, waitCtx.Err(),
				"workspace %s never reached %q status",
				wsID, status,
			)
		case <-ticker.C:
		}
	}
}

func TestWorkspaceServerFixtureCleansUpTmuxSessions(t *testing.T) {
	require := require.New(t)
	if testing.Short() {
		t.Skip("workspace e2e tests skipped in short mode")
	}

	dir := t.TempDir()
	record := filepath.Join(dir, "record")
	script := filepath.Join(dir, "fake-tmux")
	body := "#!/bin/sh\n" +
		`printf '%s\0' "$#" "$@" >> "$TMUX_RECORD"` + "\n" +
		`for a in "$@"; do` + "\n" +
		`  if [ "$a" = "has-session" ]; then` + "\n" +
		`    echo "can't find session: sim" >&2` + "\n" +
		`    exit 1` + "\n" +
		`  fi` + "\n" +
		"done\n" +
		"exit 0\n"
	require.NoError(os.WriteFile(script, []byte(body), 0o755))

	t.Run("fixture", func(t *testing.T) {
		t.Setenv("TMUX_RECORD", record)
		cfg := &config.Config{
			Tmux: config.Tmux{Command: []string{script}},
		}
		client, _, _, _, _ := setupTestServerWithWorkspacesServer(t, cfg)

		createReadyWorkspace(t, context.Background(), client)
	})

	var killed bool
	for _, argv := range readTmuxRecord(t, record) {
		if len(argv) >= 3 &&
			argv[0] == "kill-session" &&
			argv[1] == "-t" &&
			strings.HasPrefix(argv[2], "middleman-") {
			killed = true
			break
		}
	}
	require.True(killed, "fixture cleanup did not kill workspace tmux session")
}

func TestCleanupWorkspaceServerFixtureArtifactsKeepsDeletingAfterError(
	t *testing.T,
) {
	require := require.New(t)

	dir := t.TempDir()
	record := filepath.Join(dir, "record")
	script := filepath.Join(dir, "fake-tmux")
	body := "#!/bin/sh\n" +
		`printf '%s\0' "$#" "$@" >> "$TMUX_RECORD"` + "\n" +
		`if [ "$1" = "kill-session" ] && [ "$3" = "middleman-fails" ]; then` + "\n" +
		`  echo "permission denied" >&2` + "\n" +
		`  exit 1` + "\n" +
		`fi` + "\n" +
		"exit 0\n"
	require.NoError(os.WriteFile(script, []byte(body), 0o755))
	t.Setenv("TMUX_RECORD", record)

	database := dbtest.Open(t)

	manager := workspace.NewManager(database, filepath.Join(dir, "worktrees"))
	manager.SetTmuxCommand([]string{script})
	srv := &Server{workspaces: manager}
	ctx := context.Background()
	require.NoError(database.InsertWorkspace(ctx, &workspace.Workspace{
		ID:              "ws-succeeds",
		PlatformHost:    "github.com",
		RepoOwner:       "acme",
		RepoName:        "widget",
		ItemType:        db.WorkspaceItemTypePullRequest,
		ItemNumber:      1,
		GitHeadRef:      "feature/succeeds",
		WorkspaceBranch: "middleman/pr-1",
		WorktreePath:    filepath.Join(dir, "succeeds"),
		TmuxSession:     "middleman-succeeds",
		Status:          "ready",
	}))
	require.NoError(database.InsertWorkspace(ctx, &workspace.Workspace{
		ID:              "ws-fails",
		PlatformHost:    "github.com",
		RepoOwner:       "acme",
		RepoName:        "widget",
		ItemType:        db.WorkspaceItemTypePullRequest,
		ItemNumber:      2,
		GitHeadRef:      "feature/fails",
		WorkspaceBranch: "middleman/pr-2",
		WorktreePath:    filepath.Join(dir, "fails"),
		TmuxSession:     "middleman-fails",
		Status:          "ready",
	}))
	_, err := database.WriteDB().ExecContext(ctx, `
		UPDATE middleman_workspaces
		SET created_at = CASE id
			WHEN 'ws-succeeds' THEN datetime('now')
			WHEN 'ws-fails' THEN datetime('now', '+1 second')
		END
		WHERE id IN ('ws-succeeds', 'ws-fails')`)
	require.NoError(err)

	err = cleanupWorkspaceServerFixtureArtifactsWithContext(ctx, srv, database)
	require.Error(err)
	require.Contains(err.Error(), "ws-fails")
	require.Contains(err.Error(), "permission denied")

	killedSessions := map[string]bool{}
	for _, argv := range readTmuxRecord(t, record) {
		if len(argv) >= 3 &&
			argv[0] == "kill-session" {
			killedSessions[argv[2]] = true
		}
	}
	require.True(
		killedSessions["middleman-succeeds"],
		"cleanup stopped before later workspace tmux session",
	)
}

func TestWorkspaceRuntimeTargetsRefreshAfterSettingsUpdateE2E(t *testing.T) {
	t.Parallel()

	require := require.New(t)
	assert := Assert.New(t)

	dir := t.TempDir()
	cfg := &config.Config{
		SyncInterval:   "5m",
		GitHubTokenEnv: "MIDDLEMAN_GITHUB_TOKEN",
		Host:           "127.0.0.1",
		Port:           8091,
		BasePath:       "/",
		DataDir:        dir,
		Activity: config.Activity{
			ViewMode:  "threaded",
			TimeRange: "7d",
		},
	}
	cfgPath := filepath.Join(dir, "config.toml")
	require.NoError(cfg.Save(cfgPath))

	agentPath := filepath.Join(dir, "codex-custom")
	require.NoError(os.WriteFile(
		agentPath,
		[]byte("#!/bin/sh\nexit 0\n"),
		0o755,
	))
	client, _, _, _, srv := setupTestServerWithWorkspacesServer(t, cfg)
	srv.cfgPath = cfgPath
	ctx := context.Background()
	ws := createReadyWorkspace(t, ctx, client)

	agents := []config.Agent{{
		Key:     "codex",
		Label:   "Custom Codex",
		Command: []string{agentPath, "--full-auto"},
	}}
	updateResp := doJSON(
		t, srv, http.MethodPut, "/api/v1/settings",
		updateSettingsRequest{Agents: &agents},
	)
	require.Equal(http.StatusOK, updateResp.Code, updateResp.Body.String())

	reloaded, err := config.Load(cfgPath)
	require.NoError(err)
	require.Len(reloaded.Agents, 1)
	assert.Equal([]string{agentPath, "--full-auto"}, reloaded.Agents[0].Command)

	runtimeResp, err := client.HTTP.GetWorkspaceRuntimeWithResponse(ctx, ws.Id)
	require.NoError(err)
	require.Equal(http.StatusOK, runtimeResp.StatusCode())
	require.NotNil(runtimeResp.JSON200)
	require.NotNil(runtimeResp.JSON200.LaunchTargets)

	var codex generated.LaunchTarget
	for _, target := range *runtimeResp.JSON200.LaunchTargets {
		if target.Key == "codex" {
			codex = target
			break
		}
	}
	assert.Equal("Custom Codex", codex.Label)
	assert.True(codex.Available)
	require.NotNil(codex.Command)
	assert.Equal([]string{agentPath, "--full-auto"}, *codex.Command)
}

func TestWorkspaceCreatesPtyOwnerSessionWhenTmuxUnavailableE2E(t *testing.T) {
	runParallelPTYE2E(t)

	require := require.New(t)
	assert := Assert.New(t)

	fixture, dir, ptyOwnerDir := setupPtyOwnerWorkspaceFixture(t)
	ctx := context.Background()
	ws := createReadyWorkspace(t, ctx, fixture.client)
	cleanupPtyOwnerWorkspace(t, ptyOwnerDir, ws.TmuxSession)

	require.Equal("ready", ws.Status)
	assert.NotEmpty(ws.TmuxSession)

	stored, err := fixture.database.GetWorkspace(ctx, ws.Id)
	require.NoError(err)
	require.NotNil(stored)
	assert.Equal(workspace.TerminalBackendPtyOwner, stored.TerminalBackend)

	ts := httptest.NewServer(fixture.server)
	t.Cleanup(ts.Close)
	workspaceTerminalWriteRead(
		t, ctx, ts.URL, ws.Id, "printf 'owner-one\n'\n", "owner-one",
	)

	snapshot, err := fixture.server.workspaces.TerminalPaneSnapshot(
		ctx, stored, ws.TmuxSession,
	)
	require.NoError(err)
	assert.Contains(snapshot.Output, "owner-one")

	_, err = fixture.database.WriteDB().ExecContext(
		ctx,
		`UPDATE middleman_workspaces SET terminal_backend = '' WHERE id = ?`,
		ws.Id,
	)
	require.NoError(err)
	legacyStored, err := fixture.database.GetWorkspace(ctx, ws.Id)
	require.NoError(err)
	require.NotNil(legacyStored)
	assert.Empty(legacyStored.TerminalBackend)

	ts.Close()
	gracefulShutdown(t, fixture.server)

	availableTmux := filepath.Join(dir, "available-tmux")
	require.NoError(os.WriteFile(
		availableTmux,
		[]byte("#!/bin/sh\nexit 0\n"),
		0o755,
	))
	restartedCfg := &config.Config{Tmux: config.Tmux{
		Command: []string{availableTmux},
	}}
	restarted := New(
		fixture.database, fixture.server.syncer, nil, "/", restartedCfg,
		ServerOptions{
			Clones:      fixture.clones,
			WorktreeDir: fixture.worktrees,
			PtyOwnerDir: ptyOwnerDir,
		},
	)
	t.Cleanup(func() { gracefulShutdown(t, restarted) })
	restartedClient := setupTestClient(t, restarted)
	restartedTS := httptest.NewServer(restarted)
	t.Cleanup(restartedTS.Close)

	workspaceTerminalWriteRead(
		t, ctx, restartedTS.URL, ws.Id, "printf 'owner-two\n'\n", "owner-two",
	)

	force := true
	delResp, err := restartedClient.HTTP.DeleteWorkspaceWithResponse(
		ctx, ws.Id, &generated.DeleteWorkspaceParams{Force: &force},
	)
	require.NoError(err)
	require.Equal(http.StatusNoContent, delResp.StatusCode())

	deleted, err := fixture.database.GetWorkspace(ctx, ws.Id)
	require.NoError(err)
	assert.Nil(deleted)
	_, err = os.Stat(filepath.Join(ptyOwnerDir, ws.TmuxSession))
	assert.True(os.IsNotExist(err))
}

func TestWorkspacePtyOwnerTitleMarksWorkspaceWorkingE2E(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("workspace clone fixture uses Unix-style local remotes")
	}
	runParallelPTYE2E(t)

	require := require.New(t)
	assert := Assert.New(t)

	fixture, _, ptyOwnerDir := setupPtyOwnerWorkspaceFixture(t)
	ctx := context.Background()
	ws := createReadyWorkspace(t, ctx, fixture.client)
	cleanupPtyOwnerWorkspace(t, ptyOwnerDir, ws.TmuxSession)

	ts := httptest.NewServer(fixture.server)
	t.Cleanup(ts.Close)

	conn, _, err := workspaceTerminalDial(ctx, ts.URL, ws.Id)
	require.NoError(err)
	defer conn.Close(websocket.StatusNormalClosure, "done")
	workspaceTerminalConnWriteRead(
		t, ctx, conn, "stty -echo\rprintf '%s\\n' $((40+2))\r", "42",
	)
	workspaceTerminalConnWriteRead(
		t, ctx, conn,
		"printf 'title-sent\\n'; printf '\\033]0;⠴ t3code-b5014b03\\007'\r",
		"t3code-b5014b03",
	)

	var got *generated.WorkspaceResponse
	require.Eventually(func() bool {
		resp, err := fixture.client.HTTP.GetWorkspaceWithResponse(ctx, ws.Id)
		if err != nil || resp.StatusCode() != http.StatusOK || resp.JSON200 == nil {
			return false
		}
		got = resp.JSON200
		return got.TmuxWorking &&
			got.TmuxActivitySource == tmuxActivitySourceTitle &&
			got.TmuxPaneTitle != nil
	}, 6*time.Second, 50*time.Millisecond)
	require.NotNil(got)
	assert.True(got.TmuxWorking)
	assert.Equal(tmuxActivitySourceTitle, got.TmuxActivitySource)
	require.NotNil(got.TmuxPaneTitle)
	assert.Equal("⠴ t3code-b5014b03", *got.TmuxPaneTitle)
}

func TestWorkspaceCreatesRustPtyManagerSessionE2E(t *testing.T) {
	if runtime.GOOS != "windows" {
		requirePTYAvailable(t)
	}
	releasePTYSlot := acquirePTYE2ESlot(t)
	t.Cleanup(releasePTYSlot)

	require := require.New(t)
	assert := Assert.New(t)

	managerPath := buildRustPtyManagerForTest(t)
	ptyOwnerDir := longRustPtyOwnerDirForTest(t)
	setLongUnixTempDirForTest(t)
	cfg := &config.Config{
		Tmux: config.Tmux{
			Command: []string{filepath.Join(t.TempDir(), "missing-tmux")},
		},
		Shell: config.Shell{Command: rustPtyManagerShellCommandForTest(t)},
	}
	fixture := setupWorkspaceServerFixtureWithOptions(t, cfg, ServerOptions{
		PtyOwnerDir:         ptyOwnerDir,
		PtyOwnerManagerPath: managerPath,
	})
	ctx := context.Background()
	ws := createReadyWorkspace(t, ctx, fixture.client)
	cleanupPtyOwnerWorkspace(t, ptyOwnerDir, ws.TmuxSession)

	stored, err := fixture.database.GetWorkspace(ctx, ws.Id)
	require.NoError(err)
	require.NotNil(stored)
	assert.Equal(workspace.TerminalBackendPtyOwner, stored.TerminalBackend)

	ts := httptest.NewServer(fixture.server)
	t.Cleanup(ts.Close)
	conn, _, err := workspaceTerminalDialWithQuery(
		ctx, ts.URL, ws.Id, "cols=120&rows=30",
	)
	require.NoError(err)
	defer conn.Close(websocket.StatusNormalClosure, "done")

	if runtime.GOOS == "windows" {
		workspaceTerminalConnWriteRead(
			t, ctx, conn, "echo rust-owner-one\r", "rust-owner-one",
		)
	} else {
		workspaceTerminalConnWriteRead(
			t, ctx, conn, "printf 'rust-owner-one\n'\r", "rust-owner-one",
		)
		require.NoError(conn.Write(
			ctx,
			websocket.MessageText,
			[]byte(`{"type":"resize","cols":133,"rows":37}`),
		))
		workspaceTerminalConnWriteRead(
			t, ctx, conn, "printf 'size:'; stty size\r", "size:37 133",
		)
	}

	require.NoError(conn.Close(websocket.StatusNormalClosure, "done"))
	force := true
	delResp, err := fixture.client.HTTP.DeleteWorkspaceWithResponse(
		ctx, ws.Id, &generated.DeleteWorkspaceParams{Force: &force},
	)
	require.NoError(err)
	require.Equal(http.StatusNoContent, delResp.StatusCode())

	_, err = os.Stat(filepath.Join(ptyOwnerDir, ws.TmuxSession))
	assert.True(os.IsNotExist(err))
}

func TestWorkspaceRuntimeLaunchesRustPtyManagerSessionE2E(t *testing.T) {
	if runtime.GOOS != "windows" {
		requirePTYAvailable(t)
	}
	releasePTYSlot := acquirePTYE2ESlot(t)
	t.Cleanup(releasePTYSlot)

	require := require.New(t)
	assert := Assert.New(t)

	managerPath := buildRustPtyManagerForTest(t)
	ptyOwnerDir := longRustPtyOwnerDirForTest(t)
	setLongUnixTempDirForTest(t)
	disableTmuxAgentSessions := false
	cfg := &config.Config{
		Agents: []config.Agent{{
			Key:     "helper",
			Label:   "Helper",
			Command: serverRuntimeHelperCommand("echo"),
		}},
		Tmux: config.Tmux{
			Command:       []string{filepath.Join(t.TempDir(), "missing-tmux")},
			AgentSessions: &disableTmuxAgentSessions,
		},
	}
	fixture := setupWorkspaceServerFixtureWithOptions(t, cfg, ServerOptions{
		PtyOwnerDir:         ptyOwnerDir,
		PtyOwnerManagerPath: managerPath,
	})
	ctx := context.Background()
	ws := createReadyWorkspace(t, ctx, fixture.client)

	launchResp, err := fixture.client.HTTP.LaunchWorkspaceRuntimeSessionWithResponse(
		ctx, ws.Id,
		generated.LaunchWorkspaceRuntimeSessionInputBody{TargetKey: "helper"},
	)
	require.NoError(err)
	require.Equal(http.StatusOK, launchResp.StatusCode())
	require.NotNil(launchResp.JSON200)
	session := launchResp.JSON200
	cleanupPtyOwnerWorkspace(t, ptyOwnerDir, session.Key)
	assert.Equal("helper", session.TargetKey)
	assert.Equal(string(localruntime.SessionStatusRunning), session.Status)

	ts := httptest.NewServer(fixture.server)
	t.Cleanup(ts.Close)
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") +
		"/ws/v1/workspaces/" + ws.Id +
		"/runtime/sessions/" + session.Key + "/terminal?cols=80&rows=24"
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	require.NoError(err)
	defer conn.Close(websocket.StatusNormalClosure, "done")

	workspaceTerminalConnWriteRead(t, ctx, conn, "ping\r", "echo:ping")

	stopResp, err := fixture.client.HTTP.StopWorkspaceRuntimeSessionWithResponse(
		ctx, ws.Id, session.Key,
	)
	require.NoError(err)
	require.Equal(http.StatusNoContent, stopResp.StatusCode())
}

func TestRustPtyManagerRejectsConcurrentAttachmentsE2E(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("concurrent attach coverage is exercised by the Rust owner tests on Windows")
	}
	requirePTYAvailable(t)
	runParallelPTYE2E(t)

	require := require.New(t)
	assert := Assert.New(t)

	managerPath := buildRustPtyManagerForTest(t)
	ptyOwnerDir := longRustPtyOwnerDirForTest(t)
	session := "middleman-rust-concurrent"
	command := []string{
		"sh", "-c",
		"printf ready; while IFS= read -r line; do echo got:$line; done",
	}
	readyNeedle := "ready"
	firstNeedle := "got:before-second"
	thirdNeedle := "got:after-close"
	if runtime.GOOS == "windows" {
		command = serverRuntimeHelperCommand("echo")
		readyNeedle = ""
		firstNeedle = "echo:before-second"
		thirdNeedle = "echo:after-close"
	}
	client := ptyowner.Client{
		Root:        ptyOwnerDir,
		ManagerPath: managerPath,
		Command:     command,
	}
	require.NoError(client.Ensure(t.Context(), session, t.TempDir()))
	t.Cleanup(func() {
		_ = client.Stop(context.Background(), session)
	})

	first, err := client.Attach(context.Background(), session, 120, 30)
	require.NoError(err)
	defer first.Close()
	if readyNeedle != "" {
		require.Contains(
			readPtyOwnerOutputUntil(t, first.Output, readyNeedle),
			readyNeedle,
		)
	}
	require.NoError(first.Write([]byte("before-second\r")))
	require.Contains(
		readPtyOwnerOutputUntil(t, first.Output, firstNeedle),
		firstNeedle,
	)

	second, err := client.Attach(context.Background(), session, 100, 20)
	if second != nil {
		second.Close()
	}
	require.Error(err)
	assert.Contains(err.Error(), "already has an active attachment")

	first.Close()
	var third *ptyowner.Attachment
	require.Eventually(func() bool {
		var attachErr error
		third, attachErr = client.Attach(context.Background(), session, 80, 24)
		return attachErr == nil
	}, 2*time.Second, 20*time.Millisecond)
	defer third.Close()
	require.NoError(third.Write([]byte("after-close\n")))
	require.Contains(
		readPtyOwnerOutputUntil(t, third.Output, thirdNeedle),
		thirdNeedle,
	)
}

func readPtyOwnerOutputUntil(
	t *testing.T,
	output <-chan []byte,
	needle string,
) string {
	t.Helper()

	deadline := time.After(2 * time.Second)
	var builder strings.Builder
	for {
		select {
		case chunk, ok := <-output:
			if !ok {
				return builder.String()
			}
			builder.Write(chunk)
			if strings.Contains(builder.String(), needle) {
				return builder.String()
			}
		case <-deadline:
			require.New(t).Failf(
				"timed out waiting for output",
				"wanted %q in %q", needle, builder.String(),
			)
		}
	}
}

func TestWorkspacePtyOwnerTerminalRejectsConcurrentAttachmentsE2E(t *testing.T) {
	runParallelPTYE2E(t)

	require := require.New(t)
	assert := Assert.New(t)

	fixture, _, ptyOwnerDir := setupPtyOwnerWorkspaceFixture(t)
	ctx := context.Background()
	ws := createReadyWorkspace(t, ctx, fixture.client)
	cleanupPtyOwnerWorkspace(t, ptyOwnerDir, ws.TmuxSession)

	ts := httptest.NewServer(fixture.server)
	t.Cleanup(ts.Close)

	first, _, err := workspaceTerminalDial(ctx, ts.URL, ws.Id)
	require.NoError(err)
	defer first.Close(websocket.StatusNormalClosure, "done")

	second, resp, err := workspaceTerminalDial(ctx, ts.URL, ws.Id)
	require.Error(err)
	if second != nil {
		second.Close(websocket.StatusNormalClosure, "done")
	}
	require.NotNil(resp)
	assert.Equal(http.StatusConflict, resp.StatusCode)
	if resp.Body != nil {
		resp.Body.Close()
	}

	require.NoError(first.Close(websocket.StatusNormalClosure, "done"))
	third := workspaceTerminalDialEventually(t, ctx, ts.URL, ws.Id)
	defer third.Close(websocket.StatusNormalClosure, "done")
	workspaceTerminalConnWriteRead(
		t, ctx, third, "printf 'owner-after-close\n'\n", "owner-after-close",
	)
}

func TestWorkspacePtyOwnerTerminalFlushesFinalOutputOnExitE2E(t *testing.T) {
	runParallelPTYE2E(t)

	require := require.New(t)

	fixture, _, ptyOwnerDir := setupPtyOwnerWorkspaceFixture(t)
	ctx := context.Background()
	ws := createReadyWorkspace(t, ctx, fixture.client)
	cleanupPtyOwnerWorkspace(t, ptyOwnerDir, ws.TmuxSession)

	ts := httptest.NewServer(fixture.server)
	t.Cleanup(ts.Close)

	conn, _, err := workspaceTerminalDial(ctx, ts.URL, ws.Id)
	require.NoError(err)
	defer conn.Close(websocket.StatusNormalClosure, "done")

	require.NoError(conn.Write(
		ctx, websocket.MessageBinary,
		[]byte("printf 'final-owner-output\n'; exit\n"),
	))
	readCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	var got strings.Builder
	for {
		typ, data, readErr := conn.Read(readCtx)
		if readErr != nil {
			break
		}
		if typ == websocket.MessageBinary {
			got.WriteString(string(data))
		}
		if strings.Contains(got.String(), "final-owner-output") {
			return
		}
	}
	require.Contains(got.String(), "final-owner-output")
}

func setupPtyOwnerWorkspaceFixture(
	t *testing.T,
) (workspaceServerFixture, string, string) {
	t.Helper()

	dir := t.TempDir()
	ptyOwnerDir := filepath.Join(dir, "pty-owner")
	cfg := &config.Config{Tmux: config.Tmux{
		Command: []string{filepath.Join(dir, "missing-tmux")},
	}}
	return setupWorkspaceServerFixtureWithOptions(
		t, cfg, ptyOwnerServerOptions(ptyOwnerDir),
	), dir, ptyOwnerDir
}

func ptyOwnerServerOptions(ptyOwnerDir string) ServerOptions {
	return ServerOptions{
		PtyOwnerDir:     ptyOwnerDir,
		PtyOwnerExePath: os.Args[0],
		PtyOwnerExeArgs: []string{
			"-test.run=TestServerPtyOwnerHelperProcess",
			"--",
		},
		PtyOwnerCommand: []string{"/bin/sh"},
	}
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

func rustPtyManagerShellCommandForTest(t *testing.T) []string {
	t.Helper()
	if runtime.GOOS == "windows" {
		return serverRuntimeHelperCommand("echo")
	}
	return []string{"/bin/sh"}
}

func buildRustPtyManagerForTest(t *testing.T) string {
	t.Helper()

	cargo, err := exec.LookPath("cargo")
	if err != nil {
		t.Skip("cargo not available")
	}
	root := repoRootForTest(t)
	cmd := procutil.Command(cargo, "build", "-p", "middleman-pty-manager")
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
	exe := filepath.Join(root, "target", "debug", "middleman-pty-manager")
	if runtime.GOOS == "windows" {
		exe += ".exe"
	}
	return exe
}

func repoRootForTest(t *testing.T) string {
	t.Helper()

	root, err := filepath.Abs(filepath.Join("..", ".."))
	require.NoError(t, err)
	_, err = os.Stat(filepath.Join(root, "Cargo.toml"))
	require.NoError(t, err)
	return root
}

func longRustPtyOwnerDirForTest(t *testing.T) string {
	t.Helper()

	dir := filepath.Join(t.TempDir(), strings.Repeat("long-owner-root-", 8))
	require.NoError(t, os.MkdirAll(dir, 0o755))
	return dir
}

func setLongUnixTempDirForTest(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		return
	}
	dir := filepath.Join(t.TempDir(), strings.Repeat("long-temp-root-", 8))
	require.NoError(t, os.MkdirAll(dir, 0o755))
	t.Setenv("TMPDIR", dir)
}

func cleanupPtyOwnerWorkspace(
	t *testing.T,
	ptyOwnerDir string,
	session string,
) {
	t.Helper()
	t.Cleanup(func() {
		_ = (&ptyowner.Client{Root: ptyOwnerDir}).Stop(
			context.Background(), session,
		)
	})
}

func TestWorkspaceRuntimeLaunchUnavailableTargetE2E(t *testing.T) {
	t.Parallel()

	disabled := false
	cfg := &config.Config{Agents: []config.Agent{{
		Key:     "disabled",
		Label:   "Disabled",
		Enabled: &disabled,
	}}}
	client, _, _, _, _ := setupTestServerWithWorkspacesServer(t, cfg)
	ctx := context.Background()
	ws := createReadyWorkspace(t, ctx, client)

	resp, err := client.HTTP.LaunchWorkspaceRuntimeSessionWithResponse(
		ctx, ws.Id,
		generated.LaunchWorkspaceRuntimeSessionInputBody{
			TargetKey: "disabled",
		},
	)

	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, resp.StatusCode())
	require.Contains(t, string(resp.Body), "not available")
}

func TestWorkspaceRuntimeLaunchPlainShellUsesShellSessionE2E(t *testing.T) {
	t.Parallel()

	require := require.New(t)
	assert := Assert.New(t)

	client, _, _, _, _ := setupTestServerWithWorkspacesServer(t, nil)
	ctx := context.Background()
	ws := createReadyWorkspace(t, ctx, client)

	resp, err := client.HTTP.LaunchWorkspaceRuntimeSessionWithResponse(
		ctx, ws.Id,
		generated.LaunchWorkspaceRuntimeSessionInputBody{
			TargetKey: "plain_shell",
		},
	)
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode())
	require.NotNil(resp.JSON200)
	shell := resp.JSON200
	assert.Equal("plain_shell", shell.TargetKey)
	assert.Equal(string(localruntime.LaunchTargetPlainShell), shell.Kind)
	assert.Equal(string(localruntime.SessionStatusRunning), shell.Status)

	getResp, err := client.HTTP.GetWorkspaceRuntimeWithResponse(ctx, ws.Id)
	require.NoError(err)
	require.Equal(http.StatusOK, getResp.StatusCode())
	require.NotNil(getResp.JSON200)
	require.NotNil(getResp.JSON200.ShellSession)
	require.NotNil(getResp.JSON200.Sessions)
	assert.Equal(shell.Key, getResp.JSON200.ShellSession.Key)
	assert.Empty(*getResp.JSON200.Sessions)
}

func TestWorkspaceRuntimeLaunchSingletonAndStopE2E(t *testing.T) {
	runParallelPTYE2E(t)

	require := require.New(t)
	assert := Assert.New(t)
	disableTmuxAgentSessions := false
	cfg := &config.Config{Agents: []config.Agent{{
		Key:     "helper",
		Label:   "Helper",
		Command: serverRuntimeHelperCommand("sleep"),
	}}, Tmux: config.Tmux{AgentSessions: &disableTmuxAgentSessions}}
	client, _, _, _, _ := setupTestServerWithWorkspacesServer(t, cfg)
	ctx := context.Background()
	ws := createReadyWorkspace(t, ctx, client)

	firstResp, err := client.HTTP.LaunchWorkspaceRuntimeSessionWithResponse(
		ctx, ws.Id,
		generated.LaunchWorkspaceRuntimeSessionInputBody{
			TargetKey: "helper",
		},
	)
	require.NoError(err)
	require.Equal(http.StatusOK, firstResp.StatusCode())
	require.NotNil(firstResp.JSON200)
	first := firstResp.JSON200

	secondResp, err := client.HTTP.LaunchWorkspaceRuntimeSessionWithResponse(
		ctx, ws.Id,
		generated.LaunchWorkspaceRuntimeSessionInputBody{
			TargetKey: "helper",
		},
	)
	require.NoError(err)
	require.Equal(http.StatusOK, secondResp.StatusCode())
	require.NotNil(secondResp.JSON200)
	second := secondResp.JSON200
	assert.Equal(first.Key, second.Key)
	assert.Equal(string(localruntime.SessionStatusRunning), first.Status)

	listResp, err := client.HTTP.GetWorkspaceRuntimeWithResponse(
		ctx, ws.Id,
	)
	require.NoError(err)
	require.Equal(http.StatusOK, listResp.StatusCode())
	require.NotNil(listResp.JSON200)
	require.NotNil(listResp.JSON200.Sessions)
	require.Len(*listResp.JSON200.Sessions, 1)
	assert.Equal(first.Key, (*listResp.JSON200.Sessions)[0].Key)

	stopResp, err := client.HTTP.StopWorkspaceRuntimeSessionWithResponse(
		ctx, ws.Id, first.Key,
	)
	require.NoError(err)
	require.Equal(http.StatusNoContent, stopResp.StatusCode())

	afterStopResp, err := client.HTTP.GetWorkspaceRuntimeWithResponse(
		ctx, ws.Id,
	)
	require.NoError(err)
	require.Equal(http.StatusOK, afterStopResp.StatusCode())
	require.NotNil(afterStopResp.JSON200)
	require.NotNil(afterStopResp.JSON200.Sessions)
	assert.Empty(*afterStopResp.JSON200.Sessions)
}

func TestWorkspaceRuntimeNaturalAgentExitRemovesSessionE2E(t *testing.T) {
	runParallelPTYE2E(t)

	require := require.New(t)
	assert := Assert.New(t)
	disableTmuxAgentSessions := false
	cfg := &config.Config{Agents: []config.Agent{{
		Key:     "helper",
		Label:   "Helper",
		Command: serverRuntimeHelperCommand("exit"),
	}}, Tmux: config.Tmux{AgentSessions: &disableTmuxAgentSessions}}
	client, _, _, _, _ := setupTestServerWithWorkspacesServer(t, cfg)
	ctx := context.Background()
	ws := createReadyWorkspace(t, ctx, client)

	launchResp, err := client.HTTP.LaunchWorkspaceRuntimeSessionWithResponse(
		ctx, ws.Id,
		generated.LaunchWorkspaceRuntimeSessionInputBody{
			TargetKey: "helper",
		},
	)
	require.NoError(err)
	require.Equal(http.StatusOK, launchResp.StatusCode())
	require.NotNil(launchResp.JSON200)

	require.Eventually(func() bool {
		runtimeResp, runtimeErr := client.HTTP.GetWorkspaceRuntimeWithResponse(
			ctx, ws.Id,
		)
		if runtimeErr != nil ||
			runtimeResp.StatusCode() != http.StatusOK ||
			runtimeResp.JSON200 == nil ||
			runtimeResp.JSON200.Sessions == nil {
			return false
		}
		return len(*runtimeResp.JSON200.Sessions) == 0
	}, 2*time.Second, 20*time.Millisecond)
	assert.NotEmpty(launchResp.JSON200.Key)
}

func TestWorkspaceRuntimeNaturalTmuxAgentExitForgetsStoredSessionE2E(
	t *testing.T,
) {
	require := require.New(t)
	assert := Assert.New(t)
	dir := t.TempDir()
	record := filepath.Join(dir, "tmux-record")
	tmuxPath := filepath.Join(dir, "fake-tmux")
	require.NoError(os.WriteFile(tmuxPath, []byte(`#!/bin/sh
printf '%s\0' "$@" >> "$TMUX_RECORD"
case "$1" in
  has-session)
    echo "can't find session: $3" >&2
    exit 1
    ;;
  new-session|set-option|attach-session)
    exit 0
    ;;
esac
exit 0
`), 0o755))
	t.Setenv("TMUX_RECORD", record)

	cfg := &config.Config{
		Agents: []config.Agent{{
			Key:     "helper",
			Label:   "Helper",
			Command: []string{"/bin/sh", "-lc", "exit 0"},
		}},
		Tmux: config.Tmux{Command: []string{tmuxPath}},
	}
	client, database, _, _, _ := setupTestServerWithWorkspacesServer(t, cfg)
	ctx := context.Background()
	ws := createReadyWorkspace(t, ctx, client)

	launchResp, err := client.HTTP.LaunchWorkspaceRuntimeSessionWithResponse(
		ctx, ws.Id,
		generated.LaunchWorkspaceRuntimeSessionInputBody{
			TargetKey: "helper",
		},
	)
	require.NoError(err)
	require.Equal(http.StatusOK, launchResp.StatusCode())
	require.NotNil(launchResp.JSON200)

	require.Eventually(func() bool {
		runtimeResp, runtimeErr := client.HTTP.GetWorkspaceRuntimeWithResponse(
			ctx, ws.Id,
		)
		if runtimeErr != nil ||
			runtimeResp.StatusCode() != http.StatusOK ||
			runtimeResp.JSON200 == nil ||
			runtimeResp.JSON200.Sessions == nil {
			return false
		}
		return len(*runtimeResp.JSON200.Sessions) == 0
	}, 2*time.Second, 20*time.Millisecond)

	require.Eventually(func() bool {
		stored, storedErr := database.ListWorkspaceTmuxSessions(ctx, ws.Id)
		return storedErr == nil && len(stored) == 0
	}, 2*time.Second, 20*time.Millisecond)
	assert.NotEmpty(launchResp.JSON200.Key)
}

func TestWorkspaceRuntimeIncludesStoredTmuxSessionsAfterReloadE2E(t *testing.T) {
	requirePTYAvailable(t)
	require := require.New(t)
	assert := Assert.New(t)
	dir := t.TempDir()
	tmuxPath := filepath.Join(dir, "fake-tmux")
	require.NoError(os.WriteFile(tmuxPath, []byte(`#!/bin/sh
case "$1" in
  list-sessions)
    printf '%s\n' middleman-0000000000000001
    printf '%s\n' "$RESTORED_TMUX_SESSION"
    exit 0
    ;;
  attach-session)
    sleep 30
    exit 0
    ;;
  kill-session)
    exit 0
    ;;
esac
exit 0
`), 0o755))

	database := dbtest.Open(t)
	seedPR(t, database, "acme", "widget", 1)
	worktreeDir := filepath.Join(dir, "worktrees")
	cfg := &config.Config{Agents: []config.Agent{{
		Key:     "helper",
		Label:   "Helper",
		Command: serverRuntimeHelperCommand("sleep"),
	}}, Tmux: config.Tmux{Command: []string{tmuxPath}}}
	ctx := context.Background()
	ws := &workspace.Workspace{
		ID:              "0000000000000001",
		PlatformHost:    "github.com",
		RepoOwner:       "acme",
		RepoName:        "widget",
		ItemType:        db.WorkspaceItemTypePullRequest,
		ItemNumber:      1,
		GitHeadRef:      "feature",
		WorkspaceBranch: "feature",
		WorktreePath:    filepath.Join(worktreeDir, "acme-widget-1"),
		TmuxSession:     "middleman-0000000000000001",
		Status:          "ready",
	}
	require.NoError(database.InsertWorkspace(ctx, ws))
	tmuxSession := runtimeTmuxSessionNameForTest(ws.ID, "helper")
	t.Setenv("RESTORED_TMUX_SESSION", tmuxSession)
	require.NoError(database.UpsertWorkspaceTmuxSession(
		ctx,
		&db.WorkspaceTmuxSession{
			WorkspaceID: ws.ID,
			SessionName: tmuxSession,
			TargetKey:   "helper",
		},
	))
	srv := New(database, nil, nil, "/", cfg, ServerOptions{
		WorktreeDir: worktreeDir,
	})
	t.Cleanup(func() { gracefulShutdown(t, srv) })
	client := setupTestClient(t, srv)

	require.Len(srv.runtime.ListSessions(ws.ID), 1)

	resp, err := client.HTTP.GetWorkspaceRuntimeWithResponse(ctx, ws.ID)
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode())
	require.NotNil(resp.JSON200)
	require.NotNil(resp.JSON200.Sessions)
	require.Len(*resp.JSON200.Sessions, 1)

	session := (*resp.JSON200.Sessions)[0]
	assert.NotEmpty(session.Key)
	assert.NotContains(session.Key, ":")
	assert.Equal(ws.ID, session.WorkspaceId)
	assert.Equal("helper", session.TargetKey)
	assert.Equal("Helper", session.Label)
	assert.Equal(string(localruntime.LaunchTargetAgent), session.Kind)
	assert.Equal(string(localruntime.SessionStatusRunning), session.Status)
	assert.False(session.CreatedAt.IsZero())
	assert.Equal(time.UTC, session.CreatedAt.Location())
}

func TestWorkspaceRuntimeLaunchAgentCreatesProbeableTmuxSessionE2E(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)
	dir := t.TempDir()
	record := filepath.Join(dir, "record")
	tmuxPath := filepath.Join(dir, "fake-tmux")
	agentPath := filepath.Join(dir, "helper-agent")
	require.NoError(os.WriteFile(agentPath, []byte("#!/bin/sh\nexit 0\n"), 0o755))
	require.NoError(os.WriteFile(tmuxPath, []byte(`#!/bin/sh
printf '%s\0' "$#" "$@" >> "$TMUX_RECORD"
session_file="${TMUX_RECORD}.sessions"
target=""
mode=""
new_session=""
prev=""
for a in "$@"; do
  if [ "$prev" = "-t" ]; then target="$a"; fi
  if [ "$prev" = "-s" ]; then new_session="$a"; fi
  if [ "$a" = "display-message" ]; then mode="display-message"; fi
  if [ "$a" = "capture-pane" ]; then mode="capture-pane"; fi
  if [ "$a" = "list-sessions" ]; then
    [ -f "$session_file" ] && cat "$session_file"
    exit 0
  fi
  prev="$a"
done
if [ "$mode" = "display-message" ]; then
  case "$target" in
    middleman-????????????????-*) printf '⠴ t3code-b5014b03\n' ;;
    *) printf 'idle\n' ;;
  esac
  exit 0
fi
if [ "$mode" = "capture-pane" ]; then
  printf 'stable\n'
  exit 0
fi
if [ "$1" = "has-session" ]; then
  exit 1
fi
if [ "$1" = "attach-session" ]; then
  cat >/dev/null
  exit 0
fi
if [ -n "$new_session" ]; then
  printf '%s\n' "$new_session" >> "$session_file"
fi
exit 0
`), 0o755))
	t.Setenv("TMUX_RECORD", record)
	cfg := &config.Config{
		Agents: []config.Agent{{
			Key:     "helper",
			Label:   "Helper",
			Command: []string{agentPath, "--flag"},
		}},
		Tmux: config.Tmux{Command: []string{tmuxPath}},
	}
	client, database, _, _, srv := setupTestServerWithWorkspacesServer(t, cfg)
	ctx := context.Background()
	ws := createReadyWorkspace(t, ctx, client)

	resp, err := client.HTTP.LaunchWorkspaceRuntimeSessionWithResponse(
		ctx, ws.Id,
		generated.LaunchWorkspaceRuntimeSessionInputBody{
			TargetKey: "helper",
		},
	)
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode())
	require.NotNil(resp.JSON200)

	var newSession []string
	require.Eventually(func() bool {
		for _, argv := range readTmuxRecord(t, record) {
			if len(argv) > 0 &&
				argv[0] == "new-session" &&
				strings.Contains(strings.Join(argv, "\n"), agentPath) {
				newSession = argv
				return true
			}
		}
		return false
	}, 2*time.Second, 20*time.Millisecond)

	session, ok := argAfter(newSession, "-s")
	require.True(ok, "new-session should name a tmux session")
	assert.Equal(runtimeTmuxSessionNameForTest(ws.Id, "helper"), session)
	assert.Contains(newSession, "-d")
	assert.Contains(newSession, "-c")
	assert.Contains(strings.Join(newSession, "\n"), agentPath)
	assert.Contains(strings.Join(newSession, "\n"), "--flag")
	assert.Contains(newSession, "@middleman_owner")
	assert.Contains(newSession, srv.workspaces.TmuxOwnerMarker())

	listResp, err := client.HTTP.ListWorkspacesWithResponse(ctx)
	require.NoError(err)
	require.Equal(http.StatusOK, listResp.StatusCode())
	require.NotNil(listResp.JSON200)
	require.NotNil(listResp.JSON200.Workspaces)

	var listed *generated.WorkspaceResponse
	for i := range *listResp.JSON200.Workspaces {
		if (*listResp.JSON200.Workspaces)[i].Id == ws.Id {
			listed = &(*listResp.JSON200.Workspaces)[i]
			break
		}
	}
	require.NotNil(listed)
	assert.True(listed.TmuxWorking)
	assert.Equal(tmuxActivitySourceTitle, listed.TmuxActivitySource)
	require.NotNil(listed.TmuxPaneTitle)
	assert.Equal("⠴ t3code-b5014b03", *listed.TmuxPaneTitle)
	assert.Contains(readTmuxRecord(t, record), []string{
		"display-message", "-p", "-t", session, "#{pane_title}",
	})
	stored, err := database.ListWorkspaceTmuxSessions(ctx, ws.Id)
	require.NoError(err)
	require.Len(stored, 1)
	assert.Equal(session, stored[0].SessionName)
	assert.Equal("helper", stored[0].TargetKey)
}

func TestServerStartupReapsUnrecordedRuntimeTmuxSessionE2E(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("runtime tmux startup cleanup is Unix-only")
	}

	require := require.New(t)
	assert := Assert.New(t)
	dir := t.TempDir()
	record := filepath.Join(dir, "record")
	tmuxPath := filepath.Join(dir, "fake-tmux")
	require.NoError(os.WriteFile(tmuxPath, []byte(`#!/bin/sh
printf '%s\0' "$#" "$@" >> "$TMUX_RECORD"
target=""
prev=""
for a in "$@"; do
  if [ "$prev" = "-t" ]; then target="$a"; fi
  prev="$a"
done
case "$1" in
  list-sessions)
    printf 'middleman-0000000000000001\nmiddleman-0000000000000001-0123456789abcdef\n'
    exit 0
    ;;
  show-options)
    printf '%s\n' "$MIDDLEMAN_TMUX_OWNER"
    exit 0
    ;;
  kill-session)
    exit 0
    ;;
esac
exit 0
`), 0o755))
	t.Setenv("TMUX_RECORD", record)

	database := dbtest.Open(t)
	seedPR(t, database, "acme", "widget", 1)

	worktreeDir := filepath.Join(dir, "worktrees")
	ownerMarker := workspace.NewManager(database, worktreeDir).TmuxOwnerMarker()
	t.Setenv("MIDDLEMAN_TMUX_OWNER", ownerMarker)
	ws := &workspace.Workspace{
		ID:              "0000000000000001",
		PlatformHost:    "github.com",
		RepoOwner:       "acme",
		RepoName:        "widget",
		ItemType:        db.WorkspaceItemTypePullRequest,
		ItemNumber:      1,
		GitHeadRef:      "feature",
		WorkspaceBranch: "feature",
		WorktreePath:    filepath.Join(worktreeDir, "acme-widget-1"),
		TmuxSession:     "middleman-0000000000000001",
		Status:          "ready",
	}
	require.NoError(database.InsertWorkspace(t.Context(), ws))

	cfg := &config.Config{Tmux: config.Tmux{Command: []string{tmuxPath}}}
	srv := New(database, nil, nil, "/", cfg, ServerOptions{
		WorktreeDir: worktreeDir,
	})
	t.Cleanup(func() { gracefulShutdown(t, srv) })
	client := setupTestClient(t, srv)
	resp, err := client.HTTP.GetWorkspaceWithResponse(t.Context(), ws.ID)
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode())

	argvs := readTmuxRecord(t, record)
	assert.Contains(argvs, []string{
		"kill-session", "-t", "middleman-0000000000000001-0123456789abcdef",
	})
	assert.NotContains(argvs, []string{
		"kill-session", "-t", "middleman-0000000000000001",
	})
}

func TestWorkspaceResponseProbesStoredRuntimeTmuxSessionWithoutBaseE2E(
	t *testing.T,
) {
	if runtime.GOOS == "windows" {
		t.Skip("stored runtime tmux probing is Unix-only")
	}

	require := require.New(t)
	assert := Assert.New(t)
	dir := t.TempDir()
	record := filepath.Join(dir, "record")
	tmuxPath := filepath.Join(dir, "fake-tmux")
	require.NoError(os.WriteFile(tmuxPath, []byte(`#!/bin/sh
printf '%s\0' "$#" "$@" >> "$TMUX_RECORD"
target=""
mode=""
prev=""
for a in "$@"; do
  if [ "$prev" = "-t" ]; then target="$a"; fi
  if [ "$a" = "display-message" ]; then mode="display-message"; fi
  if [ "$a" = "capture-pane" ]; then mode="capture-pane"; fi
  prev="$a"
done
case "$1" in
  list-sessions)
    printf '%s\n' 'middleman-0000000000000001-e81d3b0e9d82feaa'
    exit 0
    ;;
  attach-session)
    cat >/dev/null
    exit 0
    ;;
esac
if [ "$mode" = "display-message" ]; then
  case "$target" in
    middleman-????????????????-*) printf '⠴ t3code-b5014b03\n' ;;
    *) printf 'idle\n' ;;
  esac
  exit 0
fi
if [ "$mode" = "capture-pane" ]; then
  printf 'stable\n'
  exit 0
fi
exit 0
`), 0o755))
	t.Setenv("TMUX_RECORD", record)

	database := dbtest.Open(t)
	seedPR(t, database, "acme", "widget", 1)

	worktreeDir := filepath.Join(dir, "worktrees")
	ws := &workspace.Workspace{
		ID:              "0000000000000001",
		PlatformHost:    "github.com",
		RepoOwner:       "acme",
		RepoName:        "widget",
		ItemType:        db.WorkspaceItemTypePullRequest,
		ItemNumber:      1,
		GitHeadRef:      "feature",
		WorkspaceBranch: "feature",
		WorktreePath:    filepath.Join(worktreeDir, "acme-widget-1"),
		Status:          "ready",
	}
	require.NoError(database.InsertWorkspace(t.Context(), ws))
	require.NoError(database.UpsertWorkspaceTmuxSession(
		t.Context(),
		&db.WorkspaceTmuxSession{
			WorkspaceID: ws.ID,
			SessionName: runtimeTmuxSessionNameForTest(
				"0000000000000001", "helper",
			),
			TargetKey: "helper",
		},
	))
	sessionName := runtimeTmuxSessionNameForTest("0000000000000001", "helper")

	cfg := &config.Config{Tmux: config.Tmux{Command: []string{tmuxPath}}}
	srv := New(database, nil, nil, "/", cfg, ServerOptions{
		WorktreeDir: worktreeDir,
	})
	t.Cleanup(func() { gracefulShutdown(t, srv) })
	client := setupTestClient(t, srv)
	resp, err := client.HTTP.GetWorkspaceWithResponse(t.Context(), ws.ID)
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode())
	require.NotNil(resp.JSON200)
	assert.True(resp.JSON200.TmuxWorking)
	assert.Equal(tmuxActivitySourceTitle, resp.JSON200.TmuxActivitySource)
	require.NotNil(resp.JSON200.TmuxPaneTitle)
	assert.Equal("⠴ t3code-b5014b03", *resp.JSON200.TmuxPaneTitle)
	assert.Contains(readTmuxRecord(t, record), []string{
		"display-message", "-p", "-t",
		sessionName, "#{pane_title}",
	})
}

func TestWorkspaceRuntimeLaunchTmuxOwnerMarkerFailureCleansSessionE2E(
	t *testing.T,
) {
	require := require.New(t)
	assert := Assert.New(t)
	dir := t.TempDir()
	record := filepath.Join(dir, "record")
	tmuxPath := filepath.Join(dir, "fake-tmux")
	agentPath := filepath.Join(dir, "helper-agent")
	require.NoError(os.WriteFile(agentPath, []byte("#!/bin/sh\nexit 0\n"), 0o755))
	require.NoError(os.WriteFile(tmuxPath, []byte(`#!/bin/sh
printf '%s\0' "$#" "$@" >> "$TMUX_RECORD"
target=""
prev=""
for a in "$@"; do
  if [ "$prev" = "-t" ]; then target="$a"; fi
  prev="$a"
done
case "$1" in
  has-session)
    echo "can't find session: $3" >&2
    exit 1
    ;;
  new-session)
    for a in "$@"; do
      if [ "$a" = "@middleman_owner" ]; then
        echo "owner marker denied" >&2
        exit 42
      fi
    done
    exit 0
    ;;
  set-option)
    case "$target" in
      middleman-????????????????-*)
        echo "owner marker denied" >&2
        exit 42
        ;;
    esac
    exit 0
    ;;
  kill-session)
    exit 0
    ;;
esac
exit 0
`), 0o755))
	t.Setenv("TMUX_RECORD", record)
	cfg := &config.Config{
		Agents: []config.Agent{{
			Key:     "helper",
			Label:   "Helper",
			Command: []string{agentPath},
		}},
		Tmux: config.Tmux{Command: []string{tmuxPath}},
	}
	client, database, _, _, srv := setupTestServerWithWorkspacesServer(t, cfg)
	ctx := context.Background()
	ws := createReadyWorkspace(t, ctx, client)

	launchResp, err := client.HTTP.LaunchWorkspaceRuntimeSessionWithResponse(
		ctx, ws.Id,
		generated.LaunchWorkspaceRuntimeSessionInputBody{
			TargetKey: "helper",
		},
	)
	require.NoError(err)
	require.Equal(http.StatusOK, launchResp.StatusCode())
	require.NotNil(launchResp.JSON200)
	sessionName := runtimeTmuxSessionNameForTest(ws.Id, "helper")

	require.Eventually(func() bool {
		return tmuxRecordContains(readTmuxRecord(t, record), []string{
			"kill-session", "-t", sessionName,
		})
	}, 2*time.Second, 20*time.Millisecond)
	var runtimeNewSession []string
	for _, argv := range readTmuxRecord(t, record) {
		if len(argv) > 0 &&
			argv[0] == "new-session" &&
			slices.Contains(argv, sessionName) {
			runtimeNewSession = argv
			break
		}
	}
	require.NotNil(runtimeNewSession)
	assert.Contains(runtimeNewSession, "@middleman_owner")
	assert.Contains(runtimeNewSession, srv.workspaces.TmuxOwnerMarker())

	require.Eventually(func() bool {
		runtimeResp, err := client.HTTP.GetWorkspaceRuntimeWithResponse(ctx, ws.Id)
		if err != nil ||
			runtimeResp.StatusCode() != http.StatusOK ||
			runtimeResp.JSON200 == nil ||
			runtimeResp.JSON200.Sessions == nil {
			return false
		}
		return len(*runtimeResp.JSON200.Sessions) == 0
	}, 2*time.Second, 20*time.Millisecond)

	require.Eventually(func() bool {
		stored, err := database.ListWorkspaceTmuxSessions(ctx, ws.Id)
		return err == nil && len(stored) == 0
	}, 2*time.Second, 20*time.Millisecond)

	stopResp, err := client.HTTP.StopWorkspaceRuntimeSessionWithResponse(
		ctx, ws.Id, launchResp.JSON200.Key,
	)
	require.NoError(err)
	require.Equal(http.StatusNotFound, stopResp.StatusCode())
}

func tmuxRecordContains(argvs [][]string, want []string) bool {
	return slices.ContainsFunc(argvs, func(argv []string) bool {
		return slices.Equal(argv, want)
	})
}

func runtimeTmuxSessionNameForTest(workspaceID string, targetKey string) string {
	sum := sha256.Sum256([]byte(targetKey))
	return "middleman-" + workspaceID + "-" + hex.EncodeToString(sum[:8])
}

func TestWorkspaceRuntimeTmuxSessionsHashUnsafeTargetKeysE2E(
	t *testing.T,
) {
	require := require.New(t)
	assert := Assert.New(t)
	dir := t.TempDir()
	record := filepath.Join(dir, "record")
	tmuxPath := filepath.Join(dir, "fake-tmux")
	agentPath := filepath.Join(dir, "helper-agent")
	require.NoError(os.WriteFile(agentPath, []byte("#!/bin/sh\nexit 0\n"), 0o755))
	require.NoError(os.WriteFile(tmuxPath, []byte(`#!/bin/sh
printf '%s\0' "$#" "$@" >> "$TMUX_RECORD"
case "$1" in
  has-session)
    exit 1
    ;;
  attach-session)
    cat >/dev/null
    exit 0
    ;;
  new-session|set-option|kill-session)
    exit 0
    ;;
esac
exit 0
`), 0o755))
	t.Setenv("TMUX_RECORD", record)
	cfg := &config.Config{
		Agents: []config.Agent{
			{Key: "foo/bar", Label: "Foo Slash", Command: []string{agentPath}},
			{Key: "foo:bar", Label: "Foo Colon", Command: []string{agentPath}},
		},
		Tmux: config.Tmux{Command: []string{tmuxPath}},
	}
	client, database, _, _, _ := setupTestServerWithWorkspacesServer(t, cfg)
	ctx := context.Background()
	ws := createReadyWorkspace(t, ctx, client)

	var launched []generated.SessionInfo
	for _, targetKey := range []string{"foo/bar", "foo:bar"} {
		resp, err := client.HTTP.LaunchWorkspaceRuntimeSessionWithResponse(
			ctx, ws.Id,
			generated.LaunchWorkspaceRuntimeSessionInputBody{
				TargetKey: targetKey,
			},
		)
		require.NoError(err)
		require.Equal(http.StatusOK, resp.StatusCode())
		require.NotNil(resp.JSON200)
		launched = append(launched, *resp.JSON200)
	}

	stored, err := database.ListWorkspaceTmuxSessions(ctx, ws.Id)
	require.NoError(err)
	require.Len(stored, 2)
	sessionsByTarget := map[string]string{}
	for _, session := range stored {
		sessionsByTarget[session.TargetKey] = session.SessionName
	}
	slashSession := runtimeTmuxSessionNameForTest(ws.Id, "foo/bar")
	colonSession := runtimeTmuxSessionNameForTest(ws.Id, "foo:bar")
	assert.Equal(slashSession, sessionsByTarget["foo/bar"])
	assert.Equal(colonSession, sessionsByTarget["foo:bar"])
	assert.NotEqual(slashSession, colonSession)
	for _, sessionName := range []string{slashSession, colonSession} {
		assert.NotContains(sessionName, "foo")
		assert.NotContains(sessionName, "/")
		assert.NotContains(sessionName, ":")
	}

	for _, session := range launched {
		stopResp, err := client.HTTP.StopWorkspaceRuntimeSessionWithResponse(
			ctx, ws.Id, session.Key,
		)
		require.NoError(err)
		require.Equal(http.StatusNoContent, stopResp.StatusCode())
	}
	stored, err = database.ListWorkspaceTmuxSessions(ctx, ws.Id)
	require.NoError(err)
	assert.Empty(stored)
	assert.Contains(readTmuxRecord(t, record), []string{
		"kill-session", "-t", slashSession,
	})
	assert.Contains(readTmuxRecord(t, record), []string{
		"kill-session", "-t", colonSession,
	})
}

func TestWorkspaceRuntimeStopClearsStoredShellKeyTmuxSessionAfterRuntimeForgetE2E(
	t *testing.T,
) {
	require := require.New(t)
	assert := Assert.New(t)
	dir := t.TempDir()
	record := filepath.Join(dir, "record")
	tmuxPath := filepath.Join(dir, "fake-tmux")
	agentPath := filepath.Join(dir, "helper-agent")
	require.NoError(os.WriteFile(agentPath, []byte("#!/bin/sh\nexit 0\n"), 0o755))
	require.NoError(os.WriteFile(tmuxPath, []byte(`#!/bin/sh
printf '%s\0' "$#" "$@" >> "$TMUX_RECORD"
case "$1" in
  has-session)
    exit 1
    ;;
  attach-session)
    cat >/dev/null
    exit 0
    ;;
  new-session|set-option|kill-session)
    exit 0
    ;;
esac
exit 0
`), 0o755))
	t.Setenv("TMUX_RECORD", record)
	cfg := &config.Config{
		Agents: []config.Agent{{
			Key:     "shell",
			Label:   "Shell Agent",
			Command: []string{agentPath},
		}},
		Tmux: config.Tmux{Command: []string{tmuxPath}},
	}
	client, database, _, _, srv := setupTestServerWithWorkspacesServer(t, cfg)
	ctx := context.Background()
	ws := createReadyWorkspace(t, ctx, client)

	launchResp, err := client.HTTP.LaunchWorkspaceRuntimeSessionWithResponse(
		ctx, ws.Id,
		generated.LaunchWorkspaceRuntimeSessionInputBody{
			TargetKey: "shell",
		},
	)
	require.NoError(err)
	require.Equal(http.StatusOK, launchResp.StatusCode())
	require.NotNil(launchResp.JSON200)
	sessionName := runtimeTmuxSessionNameForTest(ws.Id, "shell")

	stored, err := database.ListWorkspaceTmuxSessions(ctx, ws.Id)
	require.NoError(err)
	require.Len(stored, 1)
	assert.Equal(sessionName, stored[0].SessionName)

	require.NoError(srv.runtime.Stop(ctx, ws.Id, launchResp.JSON200.Key))
	stored, err = database.ListWorkspaceTmuxSessions(ctx, ws.Id)
	require.NoError(err)
	require.Len(stored, 1)

	stopResp, err := client.HTTP.StopWorkspaceRuntimeSessionWithResponse(
		ctx, ws.Id, launchResp.JSON200.Key,
	)
	require.NoError(err)
	require.Equal(http.StatusNoContent, stopResp.StatusCode())
	stored, err = database.ListWorkspaceTmuxSessions(ctx, ws.Id)
	require.NoError(err)
	assert.Empty(stored)
	assert.Contains(readTmuxRecord(t, record), []string{
		"kill-session", "-t", sessionName,
	})
}

func TestWorkspaceRuntimeStopTmuxCleanupFailureCleansExitedSessionE2E(
	t *testing.T,
) {
	require := require.New(t)
	assert := Assert.New(t)
	dir := t.TempDir()
	tmuxPath := filepath.Join(dir, "fake-tmux")
	agentPath := filepath.Join(dir, "helper-agent")
	require.NoError(os.WriteFile(agentPath, []byte("#!/bin/sh\nexit 0\n"), 0o755))
	require.NoError(os.WriteFile(tmuxPath, []byte(`#!/bin/sh
target=""
prev=""
for a in "$@"; do
  if [ "$prev" = "-t" ]; then target="$a"; fi
  prev="$a"
done
if [ "$1" = "attach-session" ]; then
  cat >/dev/null
  exit 0
fi
if [ "$1" = "kill-session" ]; then
  case "$target" in
    middleman-????????????????-*)
      echo "permission denied" >&2
      exit 42
      ;;
  esac
fi
exit 0
`), 0o755))
	cfg := &config.Config{
		Agents: []config.Agent{{
			Key:     "helper",
			Label:   "Helper",
			Command: []string{agentPath},
		}},
		Tmux: config.Tmux{Command: []string{tmuxPath}},
	}
	client, database, _, _, _ := setupTestServerWithWorkspacesServer(t, cfg)
	ctx := context.Background()
	ws := createReadyWorkspace(t, ctx, client)
	t.Cleanup(func() {
		_ = database.DeleteWorkspaceTmuxSessions(context.Background(), ws.Id)
	})

	launchResp, err := client.HTTP.LaunchWorkspaceRuntimeSessionWithResponse(
		ctx, ws.Id,
		generated.LaunchWorkspaceRuntimeSessionInputBody{
			TargetKey: "helper",
		},
	)
	require.NoError(err)
	require.Equal(http.StatusOK, launchResp.StatusCode())
	require.NotNil(launchResp.JSON200)

	stopResp, err := client.HTTP.StopWorkspaceRuntimeSessionWithResponse(
		ctx, ws.Id, launchResp.JSON200.Key,
	)
	require.NoError(err)
	require.Equal(http.StatusInternalServerError, stopResp.StatusCode())

	assert.Eventually(func() bool {
		getResp, err := client.HTTP.GetWorkspaceRuntimeWithResponse(
			ctx, ws.Id,
		)
		if err != nil ||
			getResp.StatusCode() != http.StatusOK ||
			getResp.JSON200 == nil ||
			getResp.JSON200.Sessions == nil {
			return false
		}
		return len(*getResp.JSON200.Sessions) == 0
	}, 2*time.Second, 20*time.Millisecond)

	stored, err := database.ListWorkspaceTmuxSessions(ctx, ws.Id)
	require.NoError(err)
	require.Len(stored, 1)
	assert.Equal(
		runtimeTmuxSessionNameForTest(ws.Id, "helper"),
		stored[0].SessionName,
	)
}

func TestWorkspaceResponseUsesStoredRuntimeTmuxSessionsAfterRestartE2E(
	t *testing.T,
) {
	require := require.New(t)
	assert := Assert.New(t)
	dir := t.TempDir()
	tmuxPath := filepath.Join(dir, "fake-tmux")
	require.NoError(os.WriteFile(tmuxPath, []byte(`#!/bin/sh
target=""
mode=""
prev=""
for a in "$@"; do
  if [ "$prev" = "-t" ]; then target="$a"; fi
  if [ "$a" = "display-message" ]; then mode="display-message"; fi
  if [ "$a" = "capture-pane" ]; then mode="capture-pane"; fi
  if [ "$a" = "list-sessions" ]; then
    printf '%s\n' "$TMUX_LIVE_SESSIONS"
    exit 0
  fi
  prev="$a"
done
if [ "$mode" = "display-message" ]; then
  case "$target" in
    *-claude) printf '⠴ claude-activity\n' ;;
    *) printf 'idle\n' ;;
  esac
  exit 0
fi
if [ "$mode" = "capture-pane" ]; then
  printf 'stable\n'
  exit 0
fi
exit 0
`), 0o755))
	cfg := &config.Config{Tmux: config.Tmux{Command: []string{tmuxPath}}}
	client, database, _, _, _ := setupTestServerWithWorkspacesServer(t, cfg)
	ctx := context.Background()
	ws := createReadyWorkspace(t, ctx, client)
	require.NotEmpty(ws.TmuxSession)
	t.Setenv(
		"TMUX_LIVE_SESSIONS",
		strings.Join([]string{
			ws.TmuxSession,
			ws.TmuxSession + "-codex",
			ws.TmuxSession + "-claude",
		}, "\n"),
	)
	require.NoError(database.UpsertWorkspaceTmuxSession(
		ctx,
		&db.WorkspaceTmuxSession{
			WorkspaceID: ws.Id,
			SessionName: ws.TmuxSession + "-codex",
			TargetKey:   "codex",
		},
	))
	require.NoError(database.UpsertWorkspaceTmuxSession(
		ctx,
		&db.WorkspaceTmuxSession{
			WorkspaceID: ws.Id,
			SessionName: ws.TmuxSession + "-claude",
			TargetKey:   "claude",
		},
	))

	listResp, err := client.HTTP.ListWorkspacesWithResponse(ctx)
	require.NoError(err)
	require.Equal(http.StatusOK, listResp.StatusCode())
	require.NotNil(listResp.JSON200)
	require.NotNil(listResp.JSON200.Workspaces)

	var listed *generated.WorkspaceResponse
	for i := range *listResp.JSON200.Workspaces {
		if (*listResp.JSON200.Workspaces)[i].Id == ws.Id {
			listed = &(*listResp.JSON200.Workspaces)[i]
			break
		}
	}
	require.NotNil(listed)
	assert.True(listed.TmuxWorking)
	assert.Equal(tmuxActivitySourceTitle, listed.TmuxActivitySource)
	require.NotNil(listed.TmuxPaneTitle)
	assert.Equal("⠴ claude-activity", *listed.TmuxPaneTitle)
}

func TestWorkspaceDeleteStopsRuntimeSessionsE2E(t *testing.T) {
	runParallelPTYE2E(t)

	require := require.New(t)
	assert := Assert.New(t)
	disableTmuxAgentSessions := false
	cfg := &config.Config{Agents: []config.Agent{{
		Key:     "helper",
		Label:   "Helper",
		Command: serverRuntimeHelperCommand("sleep"),
	}}, Tmux: config.Tmux{AgentSessions: &disableTmuxAgentSessions}}
	client, _, _, _, srv := setupTestServerWithWorkspacesServer(t, cfg)
	ctx := context.Background()
	ws := createReadyWorkspace(t, ctx, client)

	launchResp, err := client.HTTP.LaunchWorkspaceRuntimeSessionWithResponse(
		ctx, ws.Id,
		generated.LaunchWorkspaceRuntimeSessionInputBody{
			TargetKey: "helper",
		},
	)
	require.NoError(err)
	require.Equal(http.StatusOK, launchResp.StatusCode())
	require.NotNil(launchResp.JSON200)

	shellResp, err := client.HTTP.EnsureWorkspaceRuntimeShellWithResponse(
		ctx, ws.Id,
	)
	require.NoError(err)
	require.Equal(http.StatusOK, shellResp.StatusCode())

	require.Len(srv.runtime.ListSessions(ws.Id), 1)
	require.NotNil(srv.runtime.ShellSession(ws.Id))

	force := true
	delResp, err := client.HTTP.DeleteWorkspaceWithResponse(
		ctx, ws.Id,
		&generated.DeleteWorkspaceParams{Force: &force},
	)
	require.NoError(err)
	require.Equal(http.StatusNoContent, delResp.StatusCode())

	assert.Empty(srv.runtime.ListSessions(ws.Id))
	assert.Nil(srv.runtime.ShellSession(ws.Id))
}

// TestWorkspaceDeleteDirtyKeepsRuntimeSessionsE2E covers the case where the
// workspace is dirty and delete is rejected with 409. Runtime sessions must
// survive — killing them on a delete that didn't actually happen would leave
// the user with a workspace whose agent and shell were silently terminated.
func TestWorkspaceDeleteDirtyKeepsRuntimeSessionsE2E(t *testing.T) {
	runParallelPTYE2E(t)

	require := require.New(t)
	assert := Assert.New(t)
	disableTmuxAgentSessions := false
	cfg := &config.Config{Agents: []config.Agent{{
		Key:     "helper",
		Label:   "Helper",
		Command: serverRuntimeHelperCommand("sleep"),
	}}, Tmux: config.Tmux{AgentSessions: &disableTmuxAgentSessions}}
	client, _, _, _, srv := setupTestServerWithWorkspacesServer(t, cfg)
	ctx := context.Background()
	ws := createReadyWorkspace(t, ctx, client)

	launchResp, err := client.HTTP.LaunchWorkspaceRuntimeSessionWithResponse(
		ctx, ws.Id,
		generated.LaunchWorkspaceRuntimeSessionInputBody{
			TargetKey: "helper",
		},
	)
	require.NoError(err)
	require.Equal(http.StatusOK, launchResp.StatusCode())
	shellResp, err := client.HTTP.EnsureWorkspaceRuntimeShellWithResponse(
		ctx, ws.Id,
	)
	require.NoError(err)
	require.Equal(http.StatusOK, shellResp.StatusCode())
	require.Len(srv.runtime.ListSessions(ws.Id), 1)
	require.NotNil(srv.runtime.ShellSession(ws.Id))

	// Make the worktree dirty so a non-forced delete will be rejected.
	require.NoError(os.WriteFile(
		filepath.Join(ws.WorktreePath, "dirty.txt"),
		[]byte("uncommitted\n"), 0o644,
	))

	delResp, err := client.HTTP.DeleteWorkspaceWithResponse(
		ctx, ws.Id, &generated.DeleteWorkspaceParams{},
	)
	require.NoError(err)
	require.Equal(http.StatusConflict, delResp.StatusCode())

	// The 409 must not have killed the runtime sessions.
	assert.Len(srv.runtime.ListSessions(ws.Id), 1)
	assert.NotNil(srv.runtime.ShellSession(ws.Id))

	stopResp, err := client.HTTP.StopWorkspaceRuntimeSessionWithResponse(
		ctx, ws.Id, launchResp.JSON200.Key,
	)
	require.NoError(err)
	require.Equal(http.StatusNoContent, stopResp.StatusCode())

	launchAfterRejectResp, err := client.HTTP.LaunchWorkspaceRuntimeSessionWithResponse(
		ctx, ws.Id,
		generated.LaunchWorkspaceRuntimeSessionInputBody{TargetKey: "helper"},
	)
	require.NoError(err)
	require.Equal(http.StatusOK, launchAfterRejectResp.StatusCode())
	assert.Len(srv.runtime.ListSessions(ws.Id), 1)
}

// TestWorkspaceListReportsCommitsAheadBehindE2E verifies that the
// /api/v1/workspaces list response includes commits_ahead /
// commits_behind for ready workspaces, computed against the worktree's
// `@{upstream}` tracking branch. The sidebar's push-state pills depend
// on these fields, so a regression here would silently turn the pills
// off without any test failure at the unit-test layer.
func TestWorkspaceListReportsCommitsAheadBehindE2E(t *testing.T) {
	t.Parallel()

	require := require.New(t)
	assert := Assert.New(t)

	client, _, _, _, _ := setupTestServerWithWorkspacesServer(t, nil)
	ctx := context.Background()
	ws := createReadyWorkspace(t, ctx, client)

	// runGit strips global/system git config, so the workspace's
	// worktree has no committer identity. Set one locally so the
	// commits below succeed in CI as well as on developer machines.
	runGit(t, ws.WorktreePath, "config", "user.email", "test@test.com")
	runGit(t, ws.WorktreePath, "config", "user.name", "Test")

	// Add two local commits in the worktree so HEAD is ahead of
	// origin/feature by 2.
	require.NoError(os.WriteFile(
		filepath.Join(ws.WorktreePath, "ahead-1.txt"),
		[]byte("a1\n"), 0o644,
	))
	runGit(t, ws.WorktreePath, "add", ".")
	runGit(t, ws.WorktreePath, "commit", "-m", "ahead 1")
	require.NoError(os.WriteFile(
		filepath.Join(ws.WorktreePath, "ahead-2.txt"),
		[]byte("a2\n"), 0o644,
	))
	runGit(t, ws.WorktreePath, "add", ".")
	runGit(t, ws.WorktreePath, "commit", "-m", "ahead 2")

	listResp, err := client.HTTP.ListWorkspacesWithResponse(ctx)
	require.NoError(err)
	require.Equal(http.StatusOK, listResp.StatusCode())
	require.NotNil(listResp.JSON200)
	require.NotNil(listResp.JSON200.Workspaces)

	var found *generated.WorkspaceResponse
	for i := range *listResp.JSON200.Workspaces {
		entry := &(*listResp.JSON200.Workspaces)[i]
		if entry.Id == ws.Id {
			found = entry
			break
		}
	}
	require.NotNil(found, "workspace %s missing from list", ws.Id)
	require.NotNil(
		found.CommitsAhead,
		"commits_ahead must be populated for a ready workspace",
	)
	require.NotNil(
		found.CommitsBehind,
		"commits_behind must be populated for a ready workspace",
	)
	assert.Equal(int64(2), *found.CommitsAhead)
	assert.Equal(int64(0), *found.CommitsBehind)
}

func TestWorkspaceDiffEndpointsReportHeadAndPushedE2E(t *testing.T) {
	t.Parallel()

	require := require.New(t)
	assert := Assert.New(t)

	client, _, _, _, srv := setupTestServerWithWorkspacesServer(t, nil)
	ctx := context.Background()
	ws := createReadyWorkspace(t, ctx, client)

	runGit(t, ws.WorktreePath, "config", "user.email", "test@test.com")
	runGit(t, ws.WorktreePath, "config", "user.name", "Test")

	require.NoError(os.WriteFile(
		filepath.Join(ws.WorktreePath, "committed.go"),
		[]byte("package committed\n"), 0o644,
	))
	runGit(t, ws.WorktreePath, "add", ".")
	runGit(t, ws.WorktreePath, "commit", "-m", "local workspace commit")
	require.NoError(os.WriteFile(
		filepath.Join(ws.WorktreePath, "dirty.go"),
		[]byte("package dirty\n"), 0o644,
	))
	require.NoError(os.WriteFile(
		filepath.Join(ws.WorktreePath, ".workspace-state.json"),
		[]byte("{}\n"), 0o644,
	))
	require.NoError(os.WriteFile(
		filepath.Join(ws.WorktreePath, "z-blank.txt"),
		[]byte(" \t\n"), 0o644,
	))
	require.NoError(os.WriteFile(
		filepath.Join(ws.WorktreePath, "z-empty.txt"),
		nil,
		0o644,
	))
	require.NoError(os.WriteFile(
		filepath.Join(ws.WorktreePath, "base.txt"),
		[]byte("base  \n"), 0o644,
	))

	headFiles := requestWorkspaceFiles(t, srv, ws.Id, "head")
	require.NotNil(headFiles.Files)
	assertWorkspaceDiffPaths(
		t,
		*headFiles.Files,
		[]string{
			".workspace-state.json",
			"base.txt",
			"dirty.go",
			"z-blank.txt",
			"z-empty.txt",
		},
	)

	headFilesHideWhitespace := requestWorkspaceFiles(
		t, srv, ws.Id, "head", "hide",
	)
	require.NotNil(headFilesHideWhitespace.Files)
	assertWorkspaceDiffPaths(
		t,
		*headFilesHideWhitespace.Files,
		[]string{".workspace-state.json", "dirty.go", "z-empty.txt"},
	)

	headDiffHideWhitespace := requestWorkspaceDiff(
		t, srv, ws.Id, "head", "hide",
	)
	require.NotNil(headDiffHideWhitespace.Files)
	assertWorkspaceDiffPaths(
		t,
		*headDiffHideWhitespace.Files,
		[]string{".workspace-state.json", "dirty.go", "z-empty.txt"},
	)

	pushedDiff := requestWorkspaceDiff(t, srv, ws.Id, "pushed")
	require.NotNil(pushedDiff.Files)
	assertWorkspaceDiffPaths(
		t,
		*pushedDiff.Files,
		[]string{
			".workspace-state.json",
			"base.txt",
			"committed.go",
			"dirty.go",
			"z-blank.txt",
			"z-empty.txt",
		},
	)
	assert.Equal(int64(1), pushedDiff.WhitespaceOnlyCount)
}

func TestWorkspaceCommitsEndpointListsBranchCommitsE2E(t *testing.T) {
	t.Parallel()

	require := require.New(t)
	assert := Assert.New(t)

	client, _, _, _, srv := setupTestServerWithWorkspacesServer(t, nil)
	ctx := context.Background()
	ws := createReadyWorkspace(t, ctx, client)

	runGit(t, ws.WorktreePath, "config", "user.email", "test@test.com")
	runGit(t, ws.WorktreePath, "config", "user.name", "Test")

	require.NoError(os.WriteFile(
		filepath.Join(ws.WorktreePath, "local-one.go"),
		[]byte("package one\n"), 0o644,
	))
	runGit(t, ws.WorktreePath, "add", ".")
	runGit(t, ws.WorktreePath, "commit", "-m", "local one")
	require.NoError(os.WriteFile(
		filepath.Join(ws.WorktreePath, "local-two.go"),
		[]byte("package two\n"), 0o644,
	))
	runGit(t, ws.WorktreePath, "add", ".")
	runGit(t, ws.WorktreePath, "commit", "-m", "local two")

	commits := requestWorkspaceCommits(t, srv, ws.Id)
	require.Len(commits.Commits, 3)
	assert.Equal("local two", commits.Commits[0].Message)
	assert.Equal("local one", commits.Commits[1].Message)
	assert.Equal("feature commit", commits.Commits[2].Message)
}

func TestWorkspaceDiffEndpointsAcceptCommitAndRangeScopesE2E(t *testing.T) {
	t.Parallel()

	require := require.New(t)

	client, _, _, _, srv := setupTestServerWithWorkspacesServer(t, nil)
	ctx := context.Background()
	ws := createReadyWorkspace(t, ctx, client)

	runGit(t, ws.WorktreePath, "config", "user.email", "test@test.com")
	runGit(t, ws.WorktreePath, "config", "user.name", "Test")

	require.NoError(os.WriteFile(
		filepath.Join(ws.WorktreePath, "local-one.go"),
		[]byte("package one\n"), 0o644,
	))
	runGit(t, ws.WorktreePath, "add", ".")
	runGit(t, ws.WorktreePath, "commit", "-m", "local one")
	require.NoError(os.WriteFile(
		filepath.Join(ws.WorktreePath, "local-two.go"),
		[]byte("package two\n"), 0o644,
	))
	runGit(t, ws.WorktreePath, "add", ".")
	runGit(t, ws.WorktreePath, "commit", "-m", "local two")
	require.NoError(os.WriteFile(
		filepath.Join(ws.WorktreePath, "dirty.go"),
		[]byte("package dirty\n"), 0o644,
	))

	commits := requestWorkspaceCommits(t, srv, ws.Id)
	require.Len(commits.Commits, 3)
	newest := commits.Commits[0].SHA
	older := commits.Commits[1].SHA

	singleFiles := requestWorkspaceFilesQuery(
		t, srv, ws.Id, "base=head&commit="+url.QueryEscape(newest),
	)
	require.NotNil(singleFiles.Files)
	assertWorkspaceDiffPaths(t, *singleFiles.Files, []string{"local-two.go"})

	singleDiff := requestWorkspaceDiffQuery(
		t, srv, ws.Id, "base=head&commit="+url.QueryEscape(newest),
	)
	require.NotNil(singleDiff.Files)
	assertWorkspaceDiffPaths(t, *singleDiff.Files, []string{"local-two.go"})

	rangeFiles := requestWorkspaceFilesQuery(
		t,
		srv,
		ws.Id,
		"base=head&from="+url.QueryEscape(older)+"&to="+url.QueryEscape(newest),
	)
	require.NotNil(rangeFiles.Files)
	assertWorkspaceDiffPaths(
		t,
		*rangeFiles.Files,
		[]string{"local-one.go", "local-two.go"},
	)

	rangeDiff := requestWorkspaceDiffQuery(
		t,
		srv,
		ws.Id,
		"base=head&from="+url.QueryEscape(older)+"&to="+url.QueryEscape(newest),
	)
	require.NotNil(rangeDiff.Files)
	assertWorkspaceDiffPaths(
		t,
		*rangeDiff.Files,
		[]string{"local-one.go", "local-two.go"},
	)
}

func TestWorkspaceDiffEndpointReportsMergeTargetE2E(t *testing.T) {
	t.Parallel()

	require := require.New(t)
	assert := Assert.New(t)

	client, _, _, remote, srv := setupTestServerWithWorkspacesServer(t, nil)
	ctx := context.Background()
	ws := createReadyWorkspace(t, ctx, client)

	targetWork := filepath.Join(t.TempDir(), "target")
	runGit(t, filepath.Dir(targetWork), "clone", remote, targetWork)
	runGit(t, targetWork, "config", "user.email", "test@test.com")
	runGit(t, targetWork, "config", "user.name", "Test")
	require.NoError(os.WriteFile(
		filepath.Join(targetWork, "target-only.txt"),
		[]byte("target\n"), 0o644,
	))
	runGit(t, targetWork, "add", ".")
	runGit(t, targetWork, "commit", "-m", "advance main")
	runGit(t, targetWork, "push", "origin", "main")
	runGit(t, ws.WorktreePath, "fetch", "origin", "main")

	runGit(t, ws.WorktreePath, "config", "user.email", "test@test.com")
	runGit(t, ws.WorktreePath, "config", "user.name", "Test")
	require.NoError(os.WriteFile(
		filepath.Join(ws.WorktreePath, "committed.go"),
		[]byte("package committed\n"), 0o644,
	))
	runGit(t, ws.WorktreePath, "add", ".")
	runGit(t, ws.WorktreePath, "commit", "-m", "local workspace commit")
	require.NoError(os.WriteFile(
		filepath.Join(ws.WorktreePath, "dirty.go"),
		[]byte("package dirty\n"), 0o644,
	))

	mergeTargetFiles := requestWorkspaceFiles(t, srv, ws.Id, "merge-target")
	require.NotNil(mergeTargetFiles.Files)
	filePaths := workspaceDiffPaths(*mergeTargetFiles.Files)
	assert.Contains(filePaths, "new.txt")
	assert.Contains(filePaths, "committed.go")
	assert.Contains(filePaths, "dirty.go")
	assert.NotContains(filePaths, "target-only.txt")

	mergeTargetDiff := requestWorkspaceDiff(t, srv, ws.Id, "merge-target")
	require.NotNil(mergeTargetDiff.Files)
	diffPaths := workspaceDiffPaths(*mergeTargetDiff.Files)
	assert.Contains(diffPaths, "new.txt")
	assert.Contains(diffPaths, "committed.go")
	assert.Contains(diffPaths, "dirty.go")
	assert.NotContains(diffPaths, "target-only.txt")
}

func TestWorkspaceDiffEndpointRejectsOriginBaseE2E(t *testing.T) {
	t.Parallel()

	require := require.New(t)

	client, _, _, _, srv := setupTestServerWithWorkspacesServer(t, nil)
	ctx := context.Background()
	ws := createReadyWorkspace(t, ctx, client)

	req := httptest.NewRequest(
		http.MethodGet,
		"/api/v1/workspaces/"+ws.Id+"/diff?base=origin",
		nil,
	)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	resp := rr.Result()
	defer resp.Body.Close()

	require.Equal(http.StatusBadRequest, resp.StatusCode)

	var body rawProblemDetail
	require.NoError(json.NewDecoder(resp.Body).Decode(&body))
	require.Contains(body.Detail, "base must be head, pushed, or merge-target")
}

func TestWorkspaceDiffEndpointHandlesUntrackedSymlinkAndLargeFileE2E(t *testing.T) {
	t.Parallel()

	require := require.New(t)
	assert := Assert.New(t)

	client, _, _, _, srv := setupTestServerWithWorkspacesServer(t, nil)
	ctx := context.Background()
	ws := createReadyWorkspace(t, ctx, client)

	secretDir := t.TempDir()
	secretPath := filepath.Join(secretDir, "secret.txt")
	require.NoError(os.WriteFile(secretPath, []byte("do not expose\n"), 0o644))
	require.NoError(os.Symlink(
		secretPath,
		filepath.Join(ws.WorktreePath, "secret-link"),
	))
	require.NoError(os.WriteFile(
		filepath.Join(ws.WorktreePath, "large.txt"),
		bytes.Repeat([]byte("x"), 2<<20),
		0o644,
	))

	diff := requestWorkspaceDiff(t, srv, ws.Id, "head")
	require.NotNil(diff.Files)

	symlink := requireWorkspaceDiffFile(t, *diff.Files, "secret-link")
	assert.Equal("added", symlink.Status)
	assert.False(symlink.IsBinary)
	assert.Equal(int64(1), symlink.Additions)
	require.NotNil(symlink.Hunks)
	require.Len(*symlink.Hunks, 1)
	require.NotNil((*symlink.Hunks)[0].Lines)
	require.Len(*(*symlink.Hunks)[0].Lines, 1)
	line := (*(*symlink.Hunks)[0].Lines)[0]
	assert.Equal(secretPath, line.Content)
	assert.NotContains(line.Content, "do not expose")

	large := requireWorkspaceDiffFile(t, *diff.Files, "large.txt")
	assert.Equal("added", large.Status)
	assert.True(large.IsBinary)
	assert.Zero(large.Additions)
	require.NotNil(large.Hunks)
	assert.Empty(*large.Hunks)
}

func TestWorkspaceDiffEndpointMarksGeneratedFilesE2E(t *testing.T) {
	t.Parallel()

	require := require.New(t)
	assert := Assert.New(t)

	client, _, _, _, srv := setupTestServerWithWorkspacesServer(t, nil)
	ctx := context.Background()
	ws := createReadyWorkspace(t, ctx, client)

	require.NoError(os.WriteFile(
		filepath.Join(ws.WorktreePath, ".gitattributes"),
		[]byte("dist/** linguist-generated\nbun.lock -linguist-generated\n"), 0o644,
	))
	require.NoError(os.MkdirAll(filepath.Join(ws.WorktreePath, "dist"), 0o755))
	require.NoError(os.WriteFile(
		filepath.Join(ws.WorktreePath, "dist", "api.ts"),
		[]byte("export const generated = true;\n"), 0o644,
	))
	require.NoError(os.WriteFile(
		filepath.Join(ws.WorktreePath, "bun.lock"),
		[]byte("# lock\n"), 0o644,
	))
	require.NoError(os.WriteFile(
		filepath.Join(ws.WorktreePath, "src.ts"),
		[]byte("export const source = true;\n"), 0o644,
	))

	files := requestWorkspaceFiles(t, srv, ws.Id, "head")
	require.NotNil(files.Files)
	assert.True(requireWorkspaceDiffFile(t, *files.Files, "dist/api.ts").IsGenerated)
	assert.False(requireWorkspaceDiffFile(t, *files.Files, "bun.lock").IsGenerated)
	assert.False(requireWorkspaceDiffFile(t, *files.Files, "src.ts").IsGenerated)

	diff := requestWorkspaceDiff(t, srv, ws.Id, "head")
	require.NotNil(diff.Files)
	assert.True(requireWorkspaceDiffFile(t, *diff.Files, "dist/api.ts").IsGenerated)
	assert.False(requireWorkspaceDiffFile(t, *diff.Files, "bun.lock").IsGenerated)
	assert.False(requireWorkspaceDiffFile(t, *diff.Files, "src.ts").IsGenerated)
}

func TestWorkspaceDiffEndpointScopesPatchByPathE2E(t *testing.T) {
	t.Parallel()

	require := require.New(t)
	assert := Assert.New(t)

	client, _, _, _, srv := setupTestServerWithWorkspacesServer(t, nil)
	ctx := context.Background()
	ws := createReadyWorkspace(t, ctx, client)

	require.NoError(os.WriteFile(
		filepath.Join(ws.WorktreePath, "first.go"),
		[]byte("package first\n"), 0o644,
	))
	require.NoError(os.WriteFile(
		filepath.Join(ws.WorktreePath, "second.go"),
		[]byte("package second\n"), 0o644,
	))

	diff := requestWorkspaceDiffForPath(t, srv, ws.Id, "head", "first.go")
	require.NotNil(diff.Files)
	require.Len(*diff.Files, 1)

	file := (*diff.Files)[0]
	assert.Equal("first.go", file.Path)
	assert.Equal("added", file.Status)
	require.NotNil(file.Hunks)
	require.Len(*file.Hunks, 1)
	assert.NotContains(workspaceDiffPaths(*diff.Files), "second.go")
}

func requestWorkspaceFiles(
	t *testing.T,
	srv *Server,
	workspaceID string,
	base string,
	whitespace ...string,
) generated.FilesResponse {
	t.Helper()

	query := "/api/v1/workspaces/" + workspaceID + "/files?base=" + base
	if len(whitespace) > 0 {
		query += "&whitespace=" + whitespace[0]
	}
	return requestWorkspaceFilesPath(t, srv, query)
}

func requestWorkspaceFilesQuery(
	t *testing.T,
	srv *Server,
	workspaceID string,
	query string,
) generated.FilesResponse {
	t.Helper()

	return requestWorkspaceFilesPath(
		t, srv, "/api/v1/workspaces/"+workspaceID+"/files?"+query,
	)
}

func requestWorkspaceFilesPath(
	t *testing.T,
	srv *Server,
	query string,
) generated.FilesResponse {
	t.Helper()

	req := httptest.NewRequest(
		http.MethodGet,
		query,
		nil,
	)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	resp := rr.Result()
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var body generated.FilesResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	return body
}

func requestWorkspaceCommits(
	t *testing.T,
	srv *Server,
	workspaceID string,
) commitsResponse {
	t.Helper()

	req := httptest.NewRequest(
		http.MethodGet,
		"/api/v1/workspaces/"+workspaceID+"/commits",
		nil,
	)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	resp := rr.Result()
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var body commitsResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	return body
}

func requestWorkspaceDiff(
	t *testing.T,
	srv *Server,
	workspaceID string,
	base string,
	whitespace ...string,
) generated.DiffResponse {
	t.Helper()

	query := "/api/v1/workspaces/" + workspaceID + "/diff?base=" + base
	if len(whitespace) > 0 {
		query += "&whitespace=" + whitespace[0]
	}
	return requestWorkspaceDiffPath(t, srv, query)
}

func requestWorkspaceDiffQuery(
	t *testing.T,
	srv *Server,
	workspaceID string,
	query string,
) generated.DiffResponse {
	t.Helper()

	return requestWorkspaceDiffPath(
		t, srv, "/api/v1/workspaces/"+workspaceID+"/diff?"+query,
	)
}

func requestWorkspaceDiffPath(
	t *testing.T,
	srv *Server,
	query string,
) generated.DiffResponse {
	t.Helper()

	req := httptest.NewRequest(
		http.MethodGet,
		query,
		nil,
	)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	resp := rr.Result()
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var body generated.DiffResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	return body
}

func requestWorkspaceDiffForPath(
	t *testing.T,
	srv *Server,
	workspaceID string,
	base string,
	path string,
) generated.DiffResponse {
	t.Helper()

	query := "/api/v1/workspaces/" + workspaceID +
		"/diff?base=" + base + "&path=" + url.QueryEscape(path)
	req := httptest.NewRequest(
		http.MethodGet,
		query,
		nil,
	)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	resp := rr.Result()
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	var body generated.DiffResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&body))
	return body
}

func requireWorkspaceDiffFile(
	t *testing.T,
	files []generated.DiffFile,
	path string,
) generated.DiffFile {
	t.Helper()

	for _, file := range files {
		if file.Path == path {
			return file
		}
	}
	require.Failf(t, "workspace diff file not found", "path %q", path)
	return generated.DiffFile{}
}

func assertWorkspaceDiffPaths(
	t *testing.T,
	files []generated.DiffFile,
	want []string,
) {
	t.Helper()

	Assert.Equal(t, want, workspaceDiffPaths(files))
}

func workspaceDiffPaths(files []generated.DiffFile) []string {
	paths := make([]string, 0, len(files))
	for _, file := range files {
		paths = append(paths, file.Path)
	}
	return paths
}

func TestWorkspaceListPrunesMissingTmuxSessionsE2E(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)
	if testing.Short() {
		t.Skip("workspace e2e tests skipped in short mode")
	}

	dir := t.TempDir()
	script := filepath.Join(dir, "fake-tmux")
	body := "#!/bin/sh\n" +
		`for a in "$@"; do` + "\n" +
		`  if [ "$a" = "list-sessions" ]; then` + "\n" +
		`    printf 'middleman-0000000000000001\nmiddleman-0000000000000002-e81d3b0e9d82feaa\n'` + "\n" +
		`    exit 0` + "\n" +
		`  fi` + "\n" +
		"done\n" +
		"exit 0\n"
	require.NoError(os.WriteFile(script, []byte(body), 0o755))
	cfg := &config.Config{
		Tmux: config.Tmux{Command: []string{script}},
	}
	client, database, _, _, _ := setupTestServerWithWorkspacesServer(t, cfg)
	ctx := context.Background()

	require.NoError(database.InsertWorkspace(ctx, &db.Workspace{
		ID:           "0000000000000002",
		PlatformHost: "github.com",
		RepoOwner:    "acme",
		RepoName:     "widget",
		ItemType:     db.WorkspaceItemTypePullRequest,
		ItemNumber:   1,
		GitHeadRef:   "feature/stale",
		WorktreePath: filepath.Join(dir, "stale"),
		TmuxSession:  "middleman-0000000000000002",
		Status:       "ready",
	}))
	runtimeSession := runtimeTmuxSessionNameForTest(
		"0000000000000002", "helper",
	)
	require.NoError(database.UpsertWorkspaceTmuxSession(
		ctx,
		&db.WorkspaceTmuxSession{
			WorkspaceID: "0000000000000002",
			SessionName: runtimeSession,
			TargetKey:   "helper",
		},
	))

	listResp, err := client.HTTP.ListWorkspacesWithResponse(ctx)
	require.NoError(err)
	require.Equal(http.StatusOK, listResp.StatusCode())
	require.NotNil(listResp.JSON200)
	require.NotNil(listResp.JSON200.Workspaces)
	require.Len(*listResp.JSON200.Workspaces, 1)
	got := (*listResp.JSON200.Workspaces)[0]
	assert.Equal("0000000000000002", got.Id)
	assert.Equal("error", got.Status)
	require.NotNil(got.ErrorMessage)
	assert.Contains(*got.ErrorMessage, "tmux session is no longer running")

	stored, err := database.GetWorkspace(ctx, "0000000000000002")
	require.NoError(err)
	require.NotNil(stored)
	assert.Equal("error", stored.Status)
	runtimeRows, err := database.ListWorkspaceTmuxSessions(
		ctx, "0000000000000002",
	)
	require.NoError(err)
	require.Len(runtimeRows, 1)
	assert.Equal(runtimeSession, runtimeRows[0].SessionName)
}

func TestWorkspaceRuntimeEnsureShellE2E(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)

	client, _, _, _, _ := setupTestServerWithWorkspacesServer(t, nil)
	ctx := context.Background()
	ws := createReadyWorkspace(t, ctx, client)

	shellResp, err := client.HTTP.EnsureWorkspaceRuntimeShellWithResponse(
		ctx, ws.Id,
	)
	require.NoError(err)
	require.Equal(http.StatusOK, shellResp.StatusCode())
	require.NotNil(shellResp.JSON200)
	shell := shellResp.JSON200
	assert.Equal("plain_shell", shell.TargetKey)
	assert.Equal(string(localruntime.LaunchTargetPlainShell), shell.Kind)
	assert.Equal(string(localruntime.SessionStatusRunning), shell.Status)

	getResp, err := client.HTTP.GetWorkspaceRuntimeWithResponse(ctx, ws.Id)
	require.NoError(err)
	require.Equal(http.StatusOK, getResp.StatusCode())
	require.NotNil(getResp.JSON200)
	require.NotNil(getResp.JSON200.ShellSession)
	require.NotNil(getResp.JSON200.Sessions)
	assert.Equal(shell.Key, getResp.JSON200.ShellSession.Key)
	assert.Empty(*getResp.JSON200.Sessions)
}

// TestWorkspaceRuntimeShellTerminalWebSocketE2E exercises the
// /ws/v1/.../runtime/shell/terminal upgrade path end-to-end with a
// custom Shell.Command. Hardened deployments (e.g. systemd services
// with SystemCallFilter=~@privileged) need the override so that
// zsh's startup setresuid is not SIGSYS'd by the parent's seccomp
// filter; this test guards both the websocket route and the
// config.Shell.Command -> manager.Options.ShellCommand wiring.
func TestWorkspaceRuntimeShellTerminalWebSocketE2E(t *testing.T) {
	runParallelPTYE2E(t)

	require := require.New(t)
	cfg := &config.Config{
		Shell: config.Shell{
			Command: serverRuntimeHelperCommand("echo"),
		},
	}
	client, _, _, _, srv := setupTestServerWithWorkspacesServer(t, cfg)
	ctx := context.Background()
	ws := createReadyWorkspace(t, ctx, client)

	shellResp, err := client.HTTP.EnsureWorkspaceRuntimeShellWithResponse(
		ctx, ws.Id,
	)
	require.NoError(err)
	require.Equal(http.StatusOK, shellResp.StatusCode())

	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") +
		"/ws/v1/workspaces/" + ws.Id +
		"/runtime/shell/terminal?cols=80&rows=24"
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	require.NoError(err)
	defer conn.Close(websocket.StatusNormalClosure, "done")

	require.NoError(conn.Write(
		ctx, websocket.MessageBinary, []byte("ping\n"),
	))
	readCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	var got strings.Builder
	for {
		typ, data, readErr := conn.Read(readCtx)
		if readErr != nil {
			break
		}
		if typ != websocket.MessageBinary {
			continue
		}
		got.WriteString(string(data))
		if strings.Contains(got.String(), "echo:ping") {
			return
		}
	}
	require.Contains(got.String(), "echo:ping")
}

// TestWorkspaceRuntimeShellTerminalDeliversExitFrameE2E pins the
// websocket "exited" text frame contract. The frontend's ShellDrawer
// only fires onExit when this frame arrives; without it the drawer
// doesn't auto-close on shell exit and the user is stranded on a dead
// session (TerminalPane reconnect-loops on a still-listed-but-
// output-dead session, which looks like a hang).
//
// Uses the "pty-close-then-sleep" helper to deterministically open the
// race window where drainOutput's PTY EOF precedes watchSession's
// cmd.Wait return by hundreds of milliseconds. Without the bridge fix
// (always send exit frame on outputDone), this test fails because the
// 100ms timeout fires before attachment.Done — exactly the systemd-
// run-wrapped shell case the user hit.
func TestWorkspaceRuntimeShellTerminalDeliversExitFrameE2E(t *testing.T) {
	runParallelPTYE2E(t)

	require := require.New(t)
	cfg := &config.Config{
		Shell: config.Shell{
			Command: serverRuntimeHelperCommand("pty-close-then-sleep"),
		},
	}
	client, _, _, _, srv := setupTestServerWithWorkspacesServer(t, cfg)
	ctx := context.Background()
	ws := createReadyWorkspace(t, ctx, client)

	shellResp, err := client.HTTP.EnsureWorkspaceRuntimeShellWithResponse(
		ctx, ws.Id,
	)
	require.NoError(err)
	require.Equal(http.StatusOK, shellResp.StatusCode())

	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") +
		"/ws/v1/workspaces/" + ws.Id +
		"/runtime/shell/terminal?cols=80&rows=24"
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	require.NoError(err)
	defer conn.Close(websocket.StatusNormalClosure, "done")

	// Helper closes its PTY end immediately, then sleeps long enough
	// before exiting that a regression which gates the exit frame on
	// cmd.Wait would push delivery well past our promptness budget.
	// The bridge's outputDone path must deliver the frame within a
	// few hundred ms of attach; cmd.Wait can only return when the
	// helper finishes its sleep, so the gap between "right" and
	// "wrong" is the helper's sleep duration. Helper sleeps 2 s; we
	// allow up to 800 ms for the PTY-EOF path even on slow CI.
	readCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	const exitFrameBudget = 800 * time.Millisecond
	attachStart := time.Now()
	for {
		typ, data, readErr := conn.Read(readCtx)
		if readErr != nil {
			require.Failf(
				"never received exit frame",
				"read err before exit frame: %v", readErr,
			)
		}
		if typ != websocket.MessageText {
			continue
		}
		elapsed := time.Since(attachStart)
		require.Lessf(elapsed, exitFrameBudget,
			"exit frame took %s after attach; bridge appears "+
				"gated on cmd.Wait rather than PTY EOF (helper "+
				"sleeps 2 s, so a regression would clock in "+
				"around there)",
			elapsed,
		)
		var msg struct {
			Type string `json:"type"`
			Code int    `json:"code"`
		}
		require.NoError(json.Unmarshal(data, &msg))
		require.Equal("exited", msg.Type)
		// ExitCode may be 7 (cmd.Wait completed before writeRuntimeExit
		// reads the snapshot) or -1 (writeRuntimeExit read snapshot
		// before watchSession populated ExitCode). Both are "the
		// session ended" signals the frontend treats identically;
		// pinning a specific value would be timing-dependent. Reject
		// 0 (success) — that would mean the frame leaked from a
		// successful exit path that doesn't apply here.
		require.NotEqual(0, msg.Code, "non-success exit must report non-zero (or -1)")
		return
	}
}

// TestWorkspaceRuntimeEnsureShellAfterExitStartsFreshE2E pins the
// behavior that an EnsureShell call hitting the post-PTY-EOF /
// pre-cmd.Wait-return window returns a fresh session, never the
// zombie. Without the runningSession outputClosed check, EnsureShell
// would hand the next caller a Status=Running snapshot whose Output
// channel was already closed; the frontend would mount a TerminalPane
// against it, immediately receive the exit frame (per the bridge
// fix), auto-close — and on the next click do it all over again.
//
// Uses the "pty-close-then-sleep" helper so the zombie window is
// deterministic (~750ms) and the second EnsureShell is guaranteed to
// land inside it.
func TestWorkspaceRuntimeEnsureShellAfterExitStartsFreshE2E(t *testing.T) {
	runParallelPTYE2E(t)

	require := require.New(t)
	assert := Assert.New(t)
	cfg := &config.Config{
		Shell: config.Shell{
			Command: serverRuntimeHelperCommand("pty-close-then-sleep"),
		},
	}
	client, _, _, _, srv := setupTestServerWithWorkspacesServer(t, cfg)
	ctx := context.Background()
	ws := createReadyWorkspace(t, ctx, client)

	first, err := client.HTTP.EnsureWorkspaceRuntimeShellWithResponse(
		ctx, ws.Id,
	)
	require.NoError(err)
	require.Equal(http.StatusOK, first.StatusCode())
	require.NotNil(first.JSON200)
	firstCreatedAt := first.JSON200.CreatedAt

	// Attach + drain to drive the helper through its PTY-close. The
	// helper then sleeps ~750 ms before exiting; we want EnsureShell
	// to fire inside that sleep, when m.shells[key] still holds the
	// zombie (Status=Running, outputClosed=true).
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") +
		"/ws/v1/workspaces/" + ws.Id +
		"/runtime/shell/terminal?cols=80&rows=24"
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	require.NoError(err)
	// Read until WS closes (server sends exit frame and drops conn).
	for {
		readCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		_, _, readErr := conn.Read(readCtx)
		cancel()
		if readErr != nil {
			break
		}
	}
	conn.Close(websocket.StatusNormalClosure, "done")

	// Inside the zombie window: helper still sleeping, so cmd.Wait
	// hasn't returned and watchSession hasn't run.
	second, err := client.HTTP.EnsureWorkspaceRuntimeShellWithResponse(
		ctx, ws.Id,
	)
	require.NoError(err)
	require.Equal(http.StatusOK, second.StatusCode())
	require.NotNil(second.JSON200)
	assert.NotEqual(
		firstCreatedAt, second.JSON200.CreatedAt,
		"second EnsureShell must return a fresh session, not the zombie",
	)
	assert.Equal(string(localruntime.SessionStatusRunning), second.JSON200.Status)
}

// TestBridgeRuntimeAttachmentSubscriberDropDoesNotEmitExitFrame
// pins the bridge's branch that distinguishes a subscriber drop from
// a real session exit. broadcast closes a subscriber's Output channel
// when its 64-slot buffer fills (slow client); without this branch
// the bridge would emit "exited" on a healthy shell and auto-close
// the drawer in front of a still-running session.
//
// We exercise the bridge directly with an Attachment whose Output is
// pre-closed and whose SessionOutputClosed reports false — exactly
// the post-broadcast-drop state. Constructing that state via real
// PTY traffic would be timing-fragile (it requires saturating the
// TCP send buffer faster than the bridge can drain it), so this is
// a focused unit test on the bridge's branching logic.
func TestBridgeRuntimeAttachmentSubscriberDropDoesNotEmitExitFrame(t *testing.T) {
	require := require.New(t)
	closedOutput := make(chan []byte)
	close(closedOutput)
	stillRunning := make(chan struct{}) // never closed
	attach := localruntime.NewAttachmentForTesting(
		localruntime.AttachmentForTestingOptions{
			Output:              closedOutput,
			Done:                stillRunning,
			SessionOutputClosed: func() bool { return false },
		},
	)

	bridgeReturn := make(chan bool, 1)
	acceptErr := make(chan error, 1)
	srv := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
				InsecureSkipVerify: true,
			})
			if err != nil {
				acceptErr <- err
				return
			}
			exited := bridgeRuntimeAttachment(r.Context(), conn, attach)
			bridgeReturn <- exited
			conn.Close(websocket.StatusNormalClosure, "test done")
		},
	))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithTimeout(
		context.Background(), 4*time.Second,
	)
	defer cancel()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	require.NoError(err)
	defer conn.Close(websocket.StatusNormalClosure, "done")

	// Read until close. With the bug present (always-emit on
	// outputDone), we'd see a MessageText "exited" frame here.
	for {
		typ, data, readErr := conn.Read(ctx)
		if readErr != nil {
			break
		}
		if typ == websocket.MessageText {
			require.Failf(
				"unexpected exit frame on subscriber drop",
				"frame: %s", data,
			)
		}
	}

	select {
	case exited := <-bridgeReturn:
		require.False(exited,
			"bridge must report not-exited when only the "+
				"subscriber's Output closed")
	case err := <-acceptErr:
		require.NoError(err, "websocket accept failed")
	case <-time.After(2 * time.Second):
		require.Fail("bridge did not return")
	}
}

func TestWorkspaceRuntimeSessionTerminalWebSocketE2E(t *testing.T) {
	runParallelPTYE2E(t)

	require := require.New(t)
	disableTmuxAgentSessions := false
	cfg := &config.Config{Agents: []config.Agent{{
		Key:     "helper",
		Label:   "Helper",
		Command: serverRuntimeHelperCommand("echo"),
	}}, Tmux: config.Tmux{AgentSessions: &disableTmuxAgentSessions}}
	client, _, _, _, srv := setupTestServerWithWorkspacesServer(t, cfg)
	ctx := context.Background()
	ws := createReadyWorkspace(t, ctx, client)

	launchResp, err := client.HTTP.LaunchWorkspaceRuntimeSessionWithResponse(
		ctx, ws.Id,
		generated.LaunchWorkspaceRuntimeSessionInputBody{
			TargetKey: "helper",
		},
	)
	require.NoError(err)
	require.Equal(http.StatusOK, launchResp.StatusCode())
	require.NotNil(launchResp.JSON200)
	session := launchResp.JSON200

	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") +
		"/ws/v1/workspaces/" + ws.Id +
		"/runtime/sessions/" + session.Key + "/terminal"
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	require.NoError(err)
	defer conn.Close(websocket.StatusNormalClosure, "done")

	require.NoError(conn.Write(
		ctx, websocket.MessageBinary, []byte("ping\n"),
	))
	readCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	var got strings.Builder
	for {
		typ, data, readErr := conn.Read(readCtx)
		if readErr != nil {
			break
		}
		if typ != websocket.MessageBinary {
			continue
		}
		got.WriteString(string(data))
		if strings.Contains(got.String(), "echo:ping") {
			return
		}
	}
	require.Contains(got.String(), "echo:ping")
}

func TestWorkspaceRuntimeSessionTerminalWebSocketBasePathE2E(t *testing.T) {
	runParallelPTYE2E(t)

	require := require.New(t)
	disableTmuxAgentSessions := false
	cfg := &config.Config{
		BasePath: "/middleman/",
		Agents: []config.Agent{{
			Key:     "helper",
			Label:   "Helper",
			Command: serverRuntimeHelperCommand("echo"),
		}},
		Tmux: config.Tmux{AgentSessions: &disableTmuxAgentSessions},
	}
	client, _, _, _, srv := setupTestServerWithWorkspacesServer(t, cfg)
	ctx := context.Background()
	ws := createReadyWorkspace(t, ctx, client)

	launchResp, err := client.HTTP.LaunchWorkspaceRuntimeSessionWithResponse(
		ctx, ws.Id,
		generated.LaunchWorkspaceRuntimeSessionInputBody{
			TargetKey: "helper",
		},
	)
	require.NoError(err)
	require.Equal(http.StatusOK, launchResp.StatusCode())
	require.NotNil(launchResp.JSON200)
	session := launchResp.JSON200

	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") +
		"/middleman/ws/v1/workspaces/" + ws.Id +
		"/runtime/sessions/" + session.Key + "/terminal"
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	require.NoError(err)
	defer conn.Close(websocket.StatusNormalClosure, "done")

	require.NoError(conn.Write(
		ctx, websocket.MessageBinary, []byte("ping\n"),
	))
	readCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	var got strings.Builder
	for {
		typ, data, readErr := conn.Read(readCtx)
		if readErr != nil {
			break
		}
		if typ != websocket.MessageBinary {
			continue
		}
		got.WriteString(string(data))
		if strings.Contains(got.String(), "echo:ping") {
			return
		}
	}
	require.Contains(got.String(), "echo:ping")
}

func workspaceTerminalWriteRead(
	t *testing.T,
	ctx context.Context,
	serverURL string,
	workspaceID string,
	input string,
	needle string,
) {
	t.Helper()

	conn, resp, err := workspaceTerminalDial(ctx, serverURL, workspaceID)
	if err != nil && resp != nil && resp.Body != nil {
		body, readErr := io.ReadAll(resp.Body)
		require.NoError(t, readErr)
		require.NoError(t, err, string(body))
	}
	require.NoError(t, err)
	defer conn.Close(websocket.StatusNormalClosure, "done")

	workspaceTerminalConnWriteRead(t, ctx, conn, input, needle)
}

func workspaceTerminalConnWriteRead(
	t *testing.T,
	ctx context.Context,
	conn *websocket.Conn,
	input string,
	needle string,
) {
	t.Helper()

	require.NoError(t, conn.Write(
		ctx, websocket.MessageBinary, []byte(input),
	))
	readCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	var got strings.Builder
	for {
		typ, data, readErr := conn.Read(readCtx)
		if readErr != nil {
			break
		}
		if typ != websocket.MessageBinary {
			continue
		}
		got.WriteString(string(data))
		if strings.Contains(got.String(), needle) {
			return
		}
	}
	require.Contains(t, got.String(), needle)
}

func workspaceTerminalDialEventually(
	t *testing.T,
	ctx context.Context,
	serverURL string,
	workspaceID string,
) *websocket.Conn {
	t.Helper()

	var conn *websocket.Conn
	require.Eventually(t, func() bool {
		var resp *http.Response
		var err error
		conn, resp, err = workspaceTerminalDial(ctx, serverURL, workspaceID)
		if err != nil && conn != nil {
			conn.Close(websocket.StatusNormalClosure, "done")
		}
		if resp != nil && resp.Body != nil {
			resp.Body.Close()
		}
		return err == nil
	}, 2*time.Second, 20*time.Millisecond)
	return conn
}

func workspaceTerminalDial(
	ctx context.Context,
	serverURL string,
	workspaceID string,
) (*websocket.Conn, *http.Response, error) {
	return workspaceTerminalDialWithQuery(ctx, serverURL, workspaceID, "")
}

func workspaceTerminalDialWithQuery(
	ctx context.Context,
	serverURL string,
	workspaceID string,
	query string,
) (*websocket.Conn, *http.Response, error) {
	wsURL := "ws" + strings.TrimPrefix(serverURL, "http") +
		"/api/v1/workspaces/" + workspaceID + "/terminal"
	if query != "" {
		wsURL += "?" + query
	}
	return websocket.Dial(ctx, wsURL, nil)
}

func TestWorkspaceRuntimeSessionTerminalSkipsAltScreenReplayE2E(t *testing.T) {
	runParallelPTYE2E(t)

	require := require.New(t)
	assert := Assert.New(t)
	disableTmuxAgentSessions := false
	cfg := &config.Config{Agents: []config.Agent{{
		Key:     "helper",
		Label:   "Helper",
		Command: serverRuntimeHelperCommand("altscreen"),
	}}, Tmux: config.Tmux{AgentSessions: &disableTmuxAgentSessions}}
	client, _, _, _, srv := setupTestServerWithWorkspacesServer(t, cfg)
	ctx := context.Background()
	ws := createReadyWorkspace(t, ctx, client)

	launchResp, err := client.HTTP.LaunchWorkspaceRuntimeSessionWithResponse(
		ctx, ws.Id,
		generated.LaunchWorkspaceRuntimeSessionInputBody{
			TargetKey: "helper",
		},
	)
	require.NoError(err)
	require.Equal(http.StatusOK, launchResp.StatusCode())
	require.NotNil(launchResp.JSON200)
	session := launchResp.JSON200

	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") +
		"/ws/v1/workspaces/" + ws.Id +
		"/runtime/sessions/" + session.Key + "/terminal"

	primingConn, _, err := websocket.Dial(ctx, wsURL, nil)
	require.NoError(err)
	var primed strings.Builder
	primingCtx, primingCancel := context.WithTimeout(ctx, 2*time.Second)
	defer primingCancel()
	for {
		typ, data, readErr := primingConn.Read(primingCtx)
		require.NoError(readErr)
		if typ == websocket.MessageBinary {
			primed.WriteString(string(data))
		}
		if strings.Contains(primed.String(), "codex screen") {
			break
		}
	}
	require.NoError(primingConn.Close(websocket.StatusNormalClosure, "primed"))

	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	require.NoError(err)
	defer conn.Close(websocket.StatusNormalClosure, "done")

	type terminalRead struct {
		typ  websocket.MessageType
		data []byte
		err  error
	}
	reads := make(chan terminalRead, 1)
	readOnce := func() {
		go func() {
			typ, data, readErr := conn.Read(context.Background())
			reads <- terminalRead{typ: typ, data: data, err: readErr}
		}()
	}
	readOnce()
	select {
	case read := <-reads:
		require.NoError(read.err)
		require.Empty(
			string(read.data),
			"late attach must not replay stale alternate-screen output",
		)
	case <-time.After(100 * time.Millisecond):
	}

	require.NoError(conn.Write(
		ctx, websocket.MessageBinary, []byte("paint\n"),
	))
	var got strings.Builder
	deadline := time.After(2 * time.Second)
	for {
		select {
		case read := <-reads:
			require.NoError(read.err)
			if read.typ == websocket.MessageBinary {
				got.WriteString(string(read.data))
			}
			if strings.Contains(got.String(), "live:paint") {
				break
			}
			readOnce()
			continue
		case <-deadline:
			require.Contains(got.String(), "live:paint")
		}
		break
	}
	assert.NotContains(got.String(), "codex screen")
	require.Contains(got.String(), "live:paint")
}

func TestWorkspaceRuntimeSessionTerminalAppliesInitialSizeE2E(t *testing.T) {
	runParallelPTYE2E(t)

	require := require.New(t)
	// This intentionally goes through the generated HTTP client, the real
	// httptest server, and the terminal websocket rather than attaching to
	// localruntime directly. The helper exits quickly after printing the
	// observed PTY size, so receiving size:41:177 exercises the full path
	// that must preserve final terminal output before the exit frame wins.
	disableTmuxAgentSessions := false
	cfg := &config.Config{Agents: []config.Agent{{
		Key:     "helper",
		Label:   "Helper",
		Command: serverRuntimeHelperCommand("size"),
	}}, Tmux: config.Tmux{AgentSessions: &disableTmuxAgentSessions}}
	client, _, _, _, srv := setupTestServerWithWorkspacesServer(t, cfg)
	ctx := context.Background()
	ws := createReadyWorkspace(t, ctx, client)

	launchResp, err := client.HTTP.LaunchWorkspaceRuntimeSessionWithResponse(
		ctx, ws.Id,
		generated.LaunchWorkspaceRuntimeSessionInputBody{
			TargetKey: "helper",
		},
	)
	require.NoError(err)
	require.Equal(http.StatusOK, launchResp.StatusCode())
	require.NotNil(launchResp.JSON200)
	session := launchResp.JSON200

	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") +
		"/ws/v1/workspaces/" + ws.Id +
		"/runtime/sessions/" + session.Key +
		"/terminal?cols=177&rows=41"
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	require.NoError(err)
	defer conn.Close(websocket.StatusNormalClosure, "done")

	require.NoError(conn.Write(
		ctx, websocket.MessageBinary, []byte("size\n"),
	))
	readCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	var got strings.Builder
	for {
		typ, data, readErr := conn.Read(readCtx)
		if readErr != nil {
			break
		}
		if typ != websocket.MessageBinary {
			continue
		}
		got.WriteString(string(data))
		if strings.Contains(got.String(), "size:41:177") {
			return
		}
	}
	require.Contains(got.String(), "size:41:177")
}

func TestWorkspaceRuntimeSessionTerminalTmuxBackedWebSocketE2E(
	t *testing.T,
) {
	tmuxPath, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux not available")
	}
	runParallelPTYE2E(t)

	require := require.New(t)
	assert := Assert.New(t)
	dir := t.TempDir()
	agentPath := filepath.Join(dir, "size-agent")
	require.NoError(os.WriteFile(agentPath, []byte(`#!/bin/sh
while IFS= read -r line; do
	set -- $(stty size 2>/dev/null || printf '0 0')
	printf 'size:%s:%s:%s\n' "$1" "$2" "$line"
	if [ "$1:$2:$line" = "40:177:size" ]; then
		exit 0
	fi
done
`), 0o755))
	cfg := &config.Config{
		Agents: []config.Agent{{
			Key:     "helper",
			Label:   "Helper",
			Command: []string{agentPath},
		}},
		Tmux: config.Tmux{Command: []string{tmuxPath}},
	}
	client, database, _, _, srv := setupTestServerWithWorkspacesServer(t, cfg)
	ctx := context.Background()
	ws := createReadyWorkspace(t, ctx, client)

	launchResp, err := client.HTTP.LaunchWorkspaceRuntimeSessionWithResponse(
		ctx, ws.Id,
		generated.LaunchWorkspaceRuntimeSessionInputBody{
			TargetKey: "helper",
		},
	)
	require.NoError(err)
	require.Equal(http.StatusOK, launchResp.StatusCode())
	require.NotNil(launchResp.JSON200)
	session := launchResp.JSON200
	stored, err := database.ListWorkspaceTmuxSessions(ctx, ws.Id)
	require.NoError(err)
	require.Len(stored, 1)
	assert.Equal(
		runtimeTmuxSessionNameForTest(ws.Id, "helper"),
		stored[0].SessionName,
	)

	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") +
		"/ws/v1/workspaces/" + ws.Id +
		"/runtime/sessions/" + session.Key +
		"/terminal?cols=177&rows=41"
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	require.NoError(err)
	defer conn.Close(websocket.StatusNormalClosure, "done")

	requestSize := func() {
		t.Helper()
		resize, err := json.Marshal(map[string]any{
			"type": "resize",
			"cols": 177,
			"rows": 41,
		})
		require.NoError(err)
		require.NoError(conn.Write(ctx, websocket.MessageText, resize))
		require.NoError(conn.Write(ctx, websocket.MessageBinary, []byte("size\n")))
	}
	requestSize()
	readCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	var got strings.Builder
	for {
		typ, data, readErr := conn.Read(readCtx)
		if readErr != nil {
			break
		}
		if typ != websocket.MessageBinary {
			continue
		}
		got.WriteString(string(data))
		// tmux keeps one row for its status line by default, so the
		// pane sees one fewer row than the attached terminal while
		// preserving the requested column count.
		if strings.Contains(got.String(), "size:40:177:size") {
			return
		}
		if strings.Contains(got.String(), "size:") {
			requestSize()
		}
	}
	require.Contains(got.String(), "size:40:177:size")
}

func createReadyWorkspace(
	t *testing.T,
	ctx context.Context,
	client *apiclient.Client,
) *generated.WorkspaceResponse {
	t.Helper()

	createResp, err := client.HTTP.CreateWorkspaceWithResponse(
		ctx,
		generated.CreateWorkspaceInputBody{
			PlatformHost: "github.com",
			Owner:        "acme",
			Name:         "widget",
			MrNumber:     1,
		},
	)
	require.NoError(t, err)
	require.Equal(t, http.StatusAccepted, createResp.StatusCode())
	require.NotNil(t, createResp.JSON202)
	return waitForWorkspaceReady(t, ctx, client, createResp.JSON202.Id)
}

func serverRuntimeHelperCommand(mode string) []string {
	return []string{
		os.Args[0],
		"-test.run=TestServerRuntimeHelperProcess",
		"--",
		serverRuntimeHelperMarker,
		mode,
	}
}

func TestServerRuntimeHelperProcess(t *testing.T) {
	args := os.Args
	if sep := slices.Index(args, "--"); sep >= 0 {
		args = args[sep+1:]
	}
	if len(args) > 0 && args[0] == serverRuntimeHelperMarker {
		args = args[1:]
	} else if os.Getenv("MIDDLEMAN_SERVER_RUNTIME_HELPER") != "1" {
		return
	}
	if len(args) == 0 {
		os.Exit(2)
	}
	mode := args[0]
	switch mode {
	case "sleep":
		blockServerRuntimeHelper()
	case "echo":
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err == nil {
			fmt.Print("echo:" + line)
		}
		blockServerRuntimeHelper()
	case "altscreen":
		fmt.Print("\x1b[?1049h\x1b[Hcodex screen")
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err == nil {
			fmt.Print("\x1b[Hlive:" + line)
		}
		blockServerRuntimeHelper()
	case "size":
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err == nil {
			rows, cols, sizeErr := pty.Getsize(os.Stdin)
			if sizeErr == nil {
				fmt.Printf("size:%d:%d:%s", rows, cols, line)
			}
		}
		return
	case "exit":
		os.Exit(3)
	case "pty-close-then-sleep":
		// Simulate the systemd-run-wrapper window the bridge has to
		// survive: PTY EOF observed (drainOutput exits) well before
		// cmd.Wait returns. Ignoring SIGHUP keeps us alive when the
		// runtime closes the PTY master in response to our slave
		// close — without that, the kernel SIGHUPs the session leader
		// and cmd.Wait returns alongside drainOutput, which hides the
		// race we're trying to exercise. Closing stdin/stdout/stderr
		// drops every slave fd; the master sees EOF immediately. The
		// 2 s sleep gives the exit-frame promptness assertion clear
		// daylight: a regression that gates on cmd.Wait would deliver
		// at ~2 s, well past the test's promptness budget.
		signal.Ignore(syscall.SIGHUP, syscall.SIGTERM, syscall.SIGINT)
		_ = os.Stdin.Close()
		_ = os.Stdout.Close()
		_ = os.Stderr.Close()
		time.Sleep(2 * time.Second)
		os.Exit(7)
	default:
		os.Exit(2)
	}
}

func blockServerRuntimeHelper() {
	for {
		time.Sleep(time.Hour)
	}
}

func TestServerPtyOwnerHelperProcess(t *testing.T) {
	args := os.Args
	sep := slices.Index(args, "--")
	if sep >= 0 {
		args = args[sep+1:]
	}
	if os.Getenv("MIDDLEMAN_SERVER_PTY_OWNER_HELPER") != "1" &&
		(len(args) == 0 || args[0] != "pty-owner") {
		return
	}
	if len(args) > 0 && args[0] == "pty-owner" {
		args = args[1:]
	}
	fs := flag.NewFlagSet("test pty-owner", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	root := fs.String("root", "", "pty owner state root")
	session := fs.String("session", "", "session name")
	cwd := fs.String("cwd", "", "working directory")
	commandJSON := fs.String("command-json", "", "JSON command argv")
	if err := fs.Parse(args); err != nil {
		os.Exit(2)
	}
	var command []string
	if *commandJSON != "" {
		if err := json.Unmarshal([]byte(*commandJSON), &command); err != nil {
			os.Exit(2)
		}
	}
	if err := ptyowner.RunOwner(context.Background(), ptyowner.Options{
		Root:    *root,
		Session: *session,
		Cwd:     *cwd,
		Command: command,
	}); err != nil {
		os.Exit(1)
	}
	os.Exit(0)
}

func gitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := procutil.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(
		gitenv.StripAll(os.Environ()),
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
	)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %v failed: %s", args, out)
	return strings.TrimSpace(string(out))
}

type rawWorkspaceStatusResponse struct {
	ID                 string  `json:"id"`
	PlatformHost       string  `json:"platform_host"`
	RepoOwner          string  `json:"repo_owner"`
	RepoName           string  `json:"repo_name"`
	ItemType           string  `json:"item_type"`
	ItemNumber         int     `json:"item_number"`
	GitHeadRef         string  `json:"git_head_ref"`
	WorktreePath       string  `json:"worktree_path"`
	TmuxSession        string  `json:"tmux_session"`
	Status             string  `json:"status"`
	ErrorMessage       *string `json:"error_message"`
	AssociatedPRNumber *int    `json:"associated_pr_number"`
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

func TestWorkspaceCRUDE2E(t *testing.T) {
	t.Parallel()

	require := require.New(t)
	assert := Assert.New(t)

	client, _, _, _ := setupTestServerWithWorkspaces(t)
	ctx := t.Context()

	// 1. List workspaces -- initially empty.
	listResp, err := client.HTTP.ListWorkspacesWithResponse(ctx)
	require.NoError(err)
	require.Equal(http.StatusOK, listResp.StatusCode())
	require.NotNil(listResp.JSON200)
	require.NotNil(listResp.JSON200.Workspaces)
	assert.Empty(*listResp.JSON200.Workspaces)

	// 2. Create workspace.
	createResp, err := client.HTTP.CreateWorkspaceWithResponse(
		ctx,
		generated.CreateWorkspaceInputBody{
			PlatformHost: "github.com",
			Owner:        "acme",
			Name:         "widget",
			MrNumber:     1,
		},
	)
	require.NoError(err)
	require.Equal(http.StatusAccepted, createResp.StatusCode())
	require.NotNil(createResp.JSON202)
	wsID := createResp.JSON202.Id
	assert.NotEmpty(wsID)
	assert.Equal("github.com", createResp.JSON202.PlatformHost)
	assert.Equal("acme", createResp.JSON202.RepoOwner)
	assert.Equal("widget", createResp.JSON202.RepoName)
	assert.Equal(db.WorkspaceItemTypePullRequest, createResp.JSON202.ItemType)
	assert.Equal(int64(1), createResp.JSON202.ItemNumber)

	// Wait for the async clone to finish before exercising the rest of the
	// flow. Deleting (or letting the test end) while the clone subprocess is
	// still writing into the workspace's TempDir races t.TempDir cleanup,
	// which then fails with "directory not empty".
	waitForWorkspaceReady(t, ctx, client, wsID)

	// 3. Get workspace by ID.
	getResp, err := client.HTTP.GetWorkspaceWithResponse(
		ctx, wsID,
	)
	require.NoError(err)
	require.Equal(http.StatusOK, getResp.StatusCode())
	require.NotNil(getResp.JSON200)
	assert.Equal(wsID, getResp.JSON200.Id)

	// 4. List workspaces -- now has one.
	listResp2, err := client.HTTP.ListWorkspacesWithResponse(ctx)
	require.NoError(err)
	require.Equal(http.StatusOK, listResp2.StatusCode())
	require.NotNil(listResp2.JSON200)
	require.NotNil(listResp2.JSON200.Workspaces)
	assert.Len(*listResp2.JSON200.Workspaces, 1)

	// 5. Delete workspace (force).
	force := true
	delResp, err := client.HTTP.DeleteWorkspaceWithResponse(
		ctx, wsID, &generated.DeleteWorkspaceParams{Force: &force},
	)
	require.NoError(err)
	require.Equal(http.StatusNoContent, delResp.StatusCode())

	// 6. Verify deleted -- GET returns 404.
	getResp2, err := client.HTTP.GetWorkspaceWithResponse(
		ctx, wsID,
	)
	require.NoError(err)
	require.Equal(http.StatusNotFound, getResp2.StatusCode())
}

func TestWorkspaceRetryErroredWorkspaceE2E(t *testing.T) {
	t.Parallel()

	require := require.New(t)
	assert := Assert.New(t)

	client, database, _, _ := setupTestServerWithWorkspaces(t)
	ctx := context.Background()

	createResp, err := client.HTTP.CreateWorkspaceWithResponse(
		ctx,
		generated.CreateWorkspaceInputBody{
			PlatformHost: "github.com",
			Owner:        "acme",
			Name:         "widget",
			MrNumber:     1,
		},
	)
	require.NoError(err)
	require.Equal(http.StatusAccepted, createResp.StatusCode())
	require.NotNil(createResp.JSON202)
	wsID := createResp.JSON202.Id
	waitForWorkspaceReady(t, ctx, client, wsID)

	msg := "ensure clone: git fetch: fork/exec /opt/homebrew/bin/git: resource temporarily unavailable"
	err = database.UpdateWorkspaceStatus(ctx, wsID, "error", &msg)
	require.NoError(err)

	retryResp, err := client.HTTP.RetryWorkspaceWithResponse(ctx, wsID)
	require.NoError(err)
	require.Equal(http.StatusAccepted, retryResp.StatusCode())
	require.NotNil(retryResp.JSON202)
	retryBody := retryResp.JSON202
	assert.Equal(wsID, retryBody.Id)
	assert.Equal("creating", retryBody.Status)
	assert.Nil(retryBody.ErrorMessage)

	ready := waitForWorkspaceReady(t, ctx, client, wsID)
	assert.Equal(wsID, ready.Id)
	assert.Nil(ready.ErrorMessage)
}

func TestWorkspaceRetryReadyWorkspaceConflictE2E(t *testing.T) {
	t.Parallel()

	require := require.New(t)
	assert := Assert.New(t)

	client, database, _, _ := setupTestServerWithWorkspaces(t)
	ctx := context.Background()

	createResp, err := client.HTTP.CreateWorkspaceWithResponse(
		ctx,
		generated.CreateWorkspaceInputBody{
			PlatformHost: "github.com",
			Owner:        "acme",
			Name:         "widget",
			MrNumber:     1,
		},
	)
	require.NoError(err)
	require.Equal(http.StatusAccepted, createResp.StatusCode())
	require.NotNil(createResp.JSON202)
	wsID := createResp.JSON202.Id

	waitForWorkspaceReady(t, ctx, client, wsID)
	before, err := database.GetWorkspace(ctx, wsID)
	require.NoError(err)
	require.NotNil(before)
	require.Equal("ready", before.Status)
	require.Nil(before.ErrorMessage)
	require.NotEmpty(before.WorktreePath)
	beforeEvents, err := database.ListWorkspaceSetupEvents(ctx, wsID)
	require.NoError(err)

	retryResp, err := client.HTTP.RetryWorkspaceWithResponse(ctx, wsID)
	require.NoError(err)
	require.Equal(http.StatusConflict, retryResp.StatusCode())

	after, err := database.GetWorkspace(ctx, wsID)
	require.NoError(err)
	require.NotNil(after)
	assert.Equal("ready", after.Status)
	assert.Nil(after.ErrorMessage)
	assert.Equal(before.WorktreePath, after.WorktreePath)
	assert.Equal(before.WorkspaceBranch, after.WorkspaceBranch)

	afterEvents, err := database.ListWorkspaceSetupEvents(ctx, wsID)
	require.NoError(err)
	assert.Len(afterEvents, len(beforeEvents))
}

func TestWorkspaceCreateNotFound(t *testing.T) {
	t.Parallel()

	require := require.New(t)

	client, _, _, _ := setupTestServerWithWorkspaces(t)
	ctx := t.Context()

	// Non-existent repo.
	resp, err := client.HTTP.CreateWorkspaceWithResponse(
		ctx,
		generated.CreateWorkspaceInputBody{
			PlatformHost: "github.com",
			Owner:        "nope",
			Name:         "missing",
			MrNumber:     1,
		},
	)
	require.NoError(err)
	require.Equal(http.StatusNotFound, resp.StatusCode())

	// Existing repo, non-existent MR.
	resp2, err := client.HTTP.CreateWorkspaceWithResponse(
		ctx,
		generated.CreateWorkspaceInputBody{
			PlatformHost: "github.com",
			Owner:        "acme",
			Name:         "widget",
			MrNumber:     999,
		},
	)
	require.NoError(err)
	require.Equal(http.StatusNotFound, resp2.StatusCode())
}

func TestWorkspaceMRDetailHasWorkspace(t *testing.T) {
	t.Parallel()

	require := require.New(t)
	assert := Assert.New(t)

	client, _, _, _ := setupTestServerWithWorkspaces(t)
	ctx := t.Context()

	// Create a workspace for PR #1.
	createResp, err := client.HTTP.CreateWorkspaceWithResponse(
		ctx,
		generated.CreateWorkspaceInputBody{
			PlatformHost: "github.com",
			Owner:        "acme",
			Name:         "widget",
			MrNumber:     1,
		},
	)
	require.NoError(err)
	require.Equal(http.StatusAccepted, createResp.StatusCode())
	require.NotNil(createResp.JSON202)
	wsID := createResp.JSON202.Id

	// MR detail should include the workspace reference.
	mrResp, err := client.HTTP.GetPullWithResponse(
		ctx, "gh", "acme", "widget", 1,
	)
	require.NoError(err)
	require.Equal(http.StatusOK, mrResp.StatusCode())
	require.NotNil(mrResp.JSON200)
	require.NotNil(mrResp.JSON200.Workspace)
	assert.Equal(wsID, mrResp.JSON200.Workspace.Id)
	assert.NotEmpty(mrResp.JSON200.Workspace.Status)

	waitForWorkspaceReady(t, ctx, client, wsID)

	// Clean up: delete the workspace.
	force := true
	delResp, err := client.HTTP.DeleteWorkspaceWithResponse(
		ctx, wsID,
		&generated.DeleteWorkspaceParams{Force: &force},
	)
	require.NoError(err)
	require.Equal(http.StatusNoContent, delResp.StatusCode())
}

func TestWorkspaceCreateDuplicate(t *testing.T) {
	t.Parallel()

	require := require.New(t)

	client, _, _, _ := setupTestServerWithWorkspaces(t)
	ctx := t.Context()

	body := generated.CreateWorkspaceInputBody{
		PlatformHost: "github.com",
		Owner:        "acme",
		Name:         "widget",
		MrNumber:     1,
	}

	// First create succeeds.
	resp1, err := client.HTTP.CreateWorkspaceWithResponse(ctx, body)
	require.NoError(err)
	require.Equal(http.StatusAccepted, resp1.StatusCode())

	// Duplicate create returns 409.
	resp2, err := client.HTTP.CreateWorkspaceWithResponse(ctx, body)
	require.NoError(err)
	require.Equal(http.StatusConflict, resp2.StatusCode())
}

func TestWorkspaceCreateFetchesCloneThroughAPI(t *testing.T) {
	t.Parallel()

	require := require.New(t)
	assert := Assert.New(t)

	fixture := setupWorkspaceServerFixture(t, nil)
	ctx := t.Context()

	remoteWork := filepath.Join(t.TempDir(), "remote-work")
	runGit(t, t.TempDir(), "clone", fixture.remote, remoteWork)
	runGit(t, remoteWork, "config", "user.email", "test@test.com")
	runGit(t, remoteWork, "config", "user.name", "Test")
	runGit(t, remoteWork, "checkout", "feature")
	require.NoError(os.WriteFile(
		filepath.Join(remoteWork, "after-fetch.txt"),
		[]byte("fetched through workspace API\n"),
		0o644,
	))
	runGit(t, remoteWork, "add", ".")
	runGit(t, remoteWork, "commit", "-m", "feature after fixture clone")
	runGit(t, remoteWork, "push", "origin", "feature")

	createResp, err := fixture.client.HTTP.CreateWorkspaceWithResponse(
		ctx,
		generated.CreateWorkspaceInputBody{
			PlatformHost: "github.com",
			Owner:        "acme",
			Name:         "widget",
			MrNumber:     1,
		},
	)
	require.NoError(err)
	require.Equal(http.StatusAccepted, createResp.StatusCode())
	require.NotNil(createResp.JSON202)

	ready := waitForWorkspaceReady(t, ctx, fixture.client, createResp.JSON202.Id)
	assert.Equal("ready", ready.Status)
	assert.FileExists(filepath.Join(ready.WorktreePath, "after-fetch.txt"))
}

func TestWorkspaceCreateIssueE2E(t *testing.T) {
	t.Parallel()

	assert := Assert.New(t)
	require := require.New(t)

	fixture := setupWorkspaceServerFixture(t, nil)
	ctx := context.Background()

	seedIssue(t, fixture.database, "acme", "widget", 7, "open")

	createRR := doJSON(
		t,
		fixture.server,
		http.MethodPost,
		"/api/v1/issues/gh/acme/widget/7/workspace",
		map[string]string{},
	)
	require.Equal(http.StatusAccepted, createRR.Code, createRR.Body.String())

	var created rawWorkspaceStatusResponse
	require.NoError(json.NewDecoder(createRR.Body).Decode(&created))
	require.NotEmpty(created.ID)
	assert.Equal("issue", created.ItemType)
	assert.Equal(7, created.ItemNumber)
	// seedIssue uses title "Test Issue" → slug style appends "-test-issue".
	assert.Equal("middleman/issue-7-test-issue", created.GitHeadRef)

	ready := waitForWorkspaceReady(t, ctx, fixture.client, created.ID)
	assert.Equal(
		"middleman/issue-7-test-issue",
		gitOutput(t, ready.WorktreePath, "branch", "--show-current"),
	)
	assert.Equal(
		testGitSHA(t, fixture.remote, "refs/heads/main"),
		testGitSHA(t, ready.WorktreePath, "HEAD"),
	)

	getIssueRR := doJSON(
		t,
		fixture.server,
		http.MethodGet,
		"/api/v1/issues/gh/acme/widget/7",
		nil,
	)
	require.Equal(http.StatusOK, getIssueRR.Code, getIssueRR.Body.String())

	var issueDetail rawIssueDetailResponse
	require.NoError(json.NewDecoder(getIssueRR.Body).Decode(&issueDetail))
	require.NotNil(issueDetail.Workspace)
	assert.Equal(created.ID, issueDetail.Workspace.ID)
	assert.NotEmpty(issueDetail.Workspace.Status)
}

func TestWorkspaceCreateIssueUsesTitleSlugInBranch(t *testing.T) {
	t.Parallel()

	assert := Assert.New(t)
	require := require.New(t)

	fixture := setupWorkspaceServerFixture(t, nil)
	ctx := context.Background()

	// Replace the seed title with a multi-word issue title to make
	// sure the slug appears in the issue-workspace branch name.
	seedIssueOnHost(
		t, fixture.database, "github.com", "acme", "widget", 8,
		"open", "Add foo to bar",
	)

	createRR := doJSON(
		t,
		fixture.server,
		http.MethodPost,
		"/api/v1/issues/gh/acme/widget/8/workspace",
		map[string]string{},
	)
	require.Equal(http.StatusAccepted, createRR.Code, createRR.Body.String())

	var created rawWorkspaceStatusResponse
	require.NoError(json.NewDecoder(createRR.Body).Decode(&created))
	assert.Equal("middleman/issue-8-add-foo-to-bar", created.GitHeadRef)

	ready := waitForWorkspaceReady(t, ctx, fixture.client, created.ID)
	assert.Equal(
		"middleman/issue-8-add-foo-to-bar",
		gitOutput(t, ready.WorktreePath, "branch", "--show-current"),
	)
}

func TestWorkspaceCreateIssueBareStyleConfigOptOut(t *testing.T) {
	t.Parallel()

	assert := Assert.New(t)
	require := require.New(t)

	cfg := &config.Config{
		IssueWorkspaceBranchStyle: config.IssueWorkspaceBranchStyleBare,
	}
	fixture := setupWorkspaceServerFixture(t, cfg)
	ctx := context.Background()

	seedIssueOnHost(
		t, fixture.database, "github.com", "acme", "widget", 9,
		"open", "Add foo to bar",
	)

	createRR := doJSON(
		t,
		fixture.server,
		http.MethodPost,
		"/api/v1/issues/gh/acme/widget/9/workspace",
		map[string]string{},
	)
	require.Equal(http.StatusAccepted, createRR.Code, createRR.Body.String())

	var created rawWorkspaceStatusResponse
	require.NoError(json.NewDecoder(createRR.Body).Decode(&created))
	assert.Equal("middleman/issue-9", created.GitHeadRef)

	ready := waitForWorkspaceReady(t, ctx, fixture.client, created.ID)
	assert.Equal(
		"middleman/issue-9",
		gitOutput(t, ready.WorktreePath, "branch", "--show-current"),
	)
}

func TestWorkspaceCreateIssueIsIdempotent(t *testing.T) {
	t.Parallel()

	assert := Assert.New(t)
	require := require.New(t)

	fixture := setupWorkspaceServerFixture(t, nil)
	ctx := context.Background()
	seedIssue(t, fixture.database, "acme", "widget", 7, "open")

	path := "/api/v1/issues/gh/acme/widget/7/workspace"

	firstRR := doJSON(
		t, fixture.server, http.MethodPost, path, map[string]string{},
	)
	require.Equal(http.StatusAccepted, firstRR.Code, firstRR.Body.String())

	var first rawWorkspaceStatusResponse
	require.NoError(json.NewDecoder(firstRR.Body).Decode(&first))
	require.NotEmpty(first.ID)

	secondRR := doJSON(
		t, fixture.server, http.MethodPost, path, map[string]string{},
	)
	require.Equal(http.StatusAccepted, secondRR.Code, secondRR.Body.String())

	var second rawWorkspaceStatusResponse
	require.NoError(json.NewDecoder(secondRR.Body).Decode(&second))
	assert.Equal(first.ID, second.ID)
	assert.Equal("issue", second.ItemType)
	assert.Equal(7, second.ItemNumber)

	waitForWorkspaceReady(t, ctx, fixture.client, second.ID)
}

func TestWorkspaceCreateIssueAfterDeleteRecreatesBranch(t *testing.T) {
	t.Parallel()

	assert := Assert.New(t)
	require := require.New(t)

	fixture := setupWorkspaceServerFixture(t, nil)
	ctx := context.Background()

	seedIssue(t, fixture.database, "acme", "widget", 7, "open")

	createRR := doJSON(
		t,
		fixture.server,
		http.MethodPost,
		"/api/v1/issues/gh/acme/widget/7/workspace",
		map[string]string{},
	)
	require.Equal(http.StatusAccepted, createRR.Code, createRR.Body.String())

	var created rawWorkspaceStatusResponse
	require.NoError(json.NewDecoder(createRR.Body).Decode(&created))
	ready := waitForWorkspaceReady(t, ctx, fixture.client, created.ID)
	assert.Equal(
		"middleman/issue-7-test-issue",
		gitOutput(t, ready.WorktreePath, "branch", "--show-current"),
	)

	force := true
	deleteResp, err := fixture.client.HTTP.DeleteWorkspaceWithResponse(
		ctx,
		created.ID,
		&generated.DeleteWorkspaceParams{Force: &force},
	)
	require.NoError(err)
	require.Equal(http.StatusNoContent, deleteResp.StatusCode())

	recreateRR := doJSON(
		t,
		fixture.server,
		http.MethodPost,
		"/api/v1/issues/gh/acme/widget/7/workspace",
		map[string]string{},
	)
	require.Equal(http.StatusAccepted, recreateRR.Code, recreateRR.Body.String())

	var recreated rawWorkspaceStatusResponse
	require.NoError(json.NewDecoder(recreateRR.Body).Decode(&recreated))
	recreatedReady := waitForWorkspaceReady(t, ctx, fixture.client, recreated.ID)
	assert.Equal(
		"middleman/issue-7-test-issue",
		gitOutput(t, recreatedReady.WorktreePath, "branch", "--show-current"),
	)
}

func TestWorkspaceCreatePRAndIssueCanCoexistForSameRepoNumber(t *testing.T) {
	t.Parallel()

	assert := Assert.New(t)
	require := require.New(t)

	fixture := setupWorkspaceServerFixture(t, nil)
	ctx := context.Background()

	seedIssue(t, fixture.database, "acme", "widget", 1, "open")

	prResp, err := fixture.client.HTTP.CreateWorkspaceWithResponse(
		ctx,
		generated.CreateWorkspaceInputBody{
			PlatformHost: "github.com",
			Owner:        "acme",
			Name:         "widget",
			MrNumber:     1,
		},
	)
	require.NoError(err)
	require.Equal(http.StatusAccepted, prResp.StatusCode())
	require.NotNil(prResp.JSON202)
	assert.Equal("pull_request", prResp.JSON202.ItemType)
	assert.Equal(int64(1), prResp.JSON202.ItemNumber)

	issueResp, err := fixture.client.HTTP.CreateIssueWorkspaceWithResponse(
		ctx,
		"gh",
		"acme",
		"widget",
		1,
		generated.CreateIssueWorkspaceInputBody{},
	)
	require.NoError(err)
	require.Equal(http.StatusAccepted, issueResp.StatusCode())
	require.NotNil(issueResp.JSON202)
	assert.Equal("issue", issueResp.JSON202.ItemType)
	assert.Equal(int64(1), issueResp.JSON202.ItemNumber)
	assert.NotEqual(prResp.JSON202.Id, issueResp.JSON202.Id)

	listResp, err := fixture.client.HTTP.ListWorkspacesWithResponse(ctx)
	require.NoError(err)
	require.Equal(http.StatusOK, listResp.StatusCode())
	require.NotNil(listResp.JSON200)
	require.NotNil(listResp.JSON200.Workspaces)
	require.Len(*listResp.JSON200.Workspaces, 2)
}

func TestWorkspaceCreateIssueBranchConflictReturnsTyped409(t *testing.T) {
	t.Parallel()

	assert := Assert.New(t)
	require := require.New(t)

	fixture := setupWorkspaceServerFixture(t, nil)
	ctx := context.Background()

	seedIssue(t, fixture.database, "acme", "widget", 7, "open")

	// seedIssue uses the title "Test Issue", which the slug style
	// turns into "middleman/issue-7-test-issue". Pre-create that
	// branch so CreateIssue surfaces a branch conflict.
	const slugBranch = "middleman/issue-7-test-issue"
	mainSHA := testGitSHA(t, fixture.remote, "refs/heads/main")
	runGit(
		t,
		fixture.bare,
		"update-ref",
		"refs/heads/"+slugBranch,
		mainSHA,
	)

	conflictRR := doJSON(
		t,
		fixture.server,
		http.MethodPost,
		"/api/v1/issues/gh/acme/widget/7/workspace",
		map[string]string{},
	)
	require.Equal(http.StatusConflict, conflictRR.Code, conflictRR.Body.String())

	var problem rawProblemDetail
	require.NoError(json.NewDecoder(conflictRR.Body).Decode(&problem))
	assert.Equal(
		"urn:middleman:error:issue-workspace-branch-conflict",
		problem.Type,
	)
	assert.Equal(http.StatusConflict, problem.Status)
	assert.NotEmpty(problem.Detail)
	// Wire-typed envelope: code branchConflict, details carry the
	// conflicting branch and a suggested alternative so the UI can
	// branch on code rather than message text.
	assert.Equal("branchConflict", problem.Code)
	require.NotNil(problem.Details)
	assert.Equal(slugBranch, problem.Details["branch"])
	assert.Equal(slugBranch+"-2", problem.Details["suggestedBranch"])

	// The legacy Errors[] entries stay populated for clients that still
	// introspect per-field huma details.
	locations := map[string]any{}
	for _, errDetail := range problem.Errors {
		locations[errDetail.Location] = errDetail.Value
	}
	assert.Equal(slugBranch, locations["body.git_head_ref"])
	assert.Equal(
		slugBranch+"-2",
		locations["body.suggested_git_head_ref"],
	)

	reuseRR := doJSON(
		t,
		fixture.server,
		http.MethodPost,
		"/api/v1/issues/gh/acme/widget/7/workspace",
		map[string]any{
			"git_head_ref":          slugBranch,
			"reuse_existing_branch": true,
		},
	)
	require.Equal(http.StatusAccepted, reuseRR.Code, reuseRR.Body.String())

	var reused rawWorkspaceStatusResponse
	require.NoError(json.NewDecoder(reuseRR.Body).Decode(&reused))
	reusedReady := waitForWorkspaceReady(t, ctx, fixture.client, reused.ID)
	assert.Equal(
		slugBranch,
		gitOutput(t, reusedReady.WorktreePath, "branch", "--show-current"),
	)

	stored, err := fixture.database.GetWorkspace(ctx, reused.ID)
	require.NoError(err)
	require.NotNil(stored)
	assert.Equal(slugBranch, stored.WorkspaceBranch)
}

func prepareIssueWorkspaceAssociationFixture(
	t *testing.T,
) (workspaceServerFixture, rawWorkspaceStatusResponse) {
	t.Helper()
	require := require.New(t)

	fixture := setupWorkspaceServerFixture(t, nil)
	ctx := context.Background()

	seedIssue(t, fixture.database, "acme", "widget", 7, "open")
	repo, err := fixture.database.GetRepoByHostOwnerName(
		ctx, "github.com", "acme", "widget",
	)
	require.NoError(err)
	require.NotNil(repo)

	now := time.Now().UTC().Truncate(time.Second)
	mr := &db.MergeRequest{
		RepoID:         repo.ID,
		PlatformID:     7000,
		Number:         42,
		URL:            "https://github.com/acme/widget/pull/42",
		Title:          "Workspace monitor association",
		Author:         "alice",
		State:          "open",
		HeadBranch:     "issue-feature-7",
		BaseBranch:     "main",
		CIStatus:       "success",
		ReviewDecision: "APPROVED",
		CreatedAt:      now,
		UpdatedAt:      now,
		LastActivityAt: now,
	}
	_, err = fixture.database.UpsertMergeRequest(ctx, mr)
	require.NoError(err)

	createRR := doJSON(
		t,
		fixture.server,
		http.MethodPost,
		"/api/v1/issues/gh/acme/widget/7/workspace",
		map[string]string{},
	)
	require.Equal(http.StatusAccepted, createRR.Code, createRR.Body.String())

	var created rawWorkspaceStatusResponse
	require.NoError(json.NewDecoder(createRR.Body).Decode(&created))
	require.NotEmpty(created.ID)

	ready := waitForWorkspaceReady(t, ctx, fixture.client, created.ID)
	runGit(t, ready.WorktreePath, "checkout", "-b", "issue-feature-7")
	mr.PlatformHeadSHA = testGitSHA(t, ready.WorktreePath, "HEAD")
	_, err = fixture.database.UpsertMergeRequest(ctx, mr)
	require.NoError(err)

	return fixture, created
}

func TestWorkspaceIssueMonitorAssociatesPRAndKeepsIssueOwnership(t *testing.T) {
	t.Parallel()

	assert := Assert.New(t)
	require := require.New(t)
	ctx := context.Background()

	fixture, created := prepareIssueWorkspaceAssociationFixture(t)

	updates, err := fixture.server.workspacePRMonitor.RunOnce(ctx)
	require.NoError(err)
	require.Len(updates, 1)
	assert.Equal(created.ID, updates[0].WorkspaceID)
	assert.Equal(42, updates[0].PRNumber)

	getRR := doJSON(
		t,
		fixture.server,
		http.MethodGet,
		"/api/v1/workspaces/"+created.ID,
		nil,
	)
	require.Equal(http.StatusOK, getRR.Code, getRR.Body.String())

	var got rawWorkspaceStatusResponse
	require.NoError(json.NewDecoder(getRR.Body).Decode(&got))
	assert.Equal("issue", got.ItemType)
	assert.Equal(7, got.ItemNumber)
	require.NotNil(got.AssociatedPRNumber)
	assert.Equal(42, *got.AssociatedPRNumber)

	getIssueRR := doJSON(
		t,
		fixture.server,
		http.MethodGet,
		"/api/v1/issues/gh/acme/widget/7",
		nil,
	)
	require.Equal(http.StatusOK, getIssueRR.Code, getIssueRR.Body.String())

	var issueDetail rawIssueDetailResponse
	require.NoError(json.NewDecoder(getIssueRR.Body).Decode(&issueDetail))
	require.NotNil(issueDetail.Workspace)
	assert.Equal(created.ID, issueDetail.Workspace.ID)
}

func TestWorkspaceMonitorPassBroadcastsInvalidationEvents(t *testing.T) {
	t.Parallel()

	assert := Assert.New(t)
	ctx := t.Context()

	fixture, created := prepareIssueWorkspaceAssociationFixture(t)
	ch, _ := fixture.server.Hub().Subscribe(ctx, true)

	fixture.server.runWorkspacePRMonitorPass(ctx)

	status := readEventMatching(t, ch, func(ev Event) bool {
		data, ok := ev.Data.(map[string]string)
		return ev.Type == "workspace_status" && ok && data["id"] == created.ID
	})
	changed := readEventMatching(t, ch, func(ev Event) bool {
		return ev.Type == "data_changed"
	})

	assert.Equal("workspace_status", status.Type)
	assert.Equal(map[string]string{"id": created.ID}, status.Data)
	assert.Equal("data_changed", changed.Type)
}

func readEventMatching(
	t *testing.T,
	ch <-chan RecordedEvent,
	matches func(Event) bool,
) Event {
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

func TestWorkspaceCreateUsesPRBranchAndFallbackBranch(t *testing.T) {
	t.Parallel()

	assert := Assert.New(t)
	require := require.New(t)

	client, database, clonePath, _ := setupTestServerWithWorkspaces(t)
	ctx := t.Context()

	seedPR(t, database, "acme", "widget", 2)

	createResp1, err := client.HTTP.CreateWorkspaceWithResponse(
		ctx,
		generated.CreateWorkspaceInputBody{
			PlatformHost: "github.com",
			Owner:        "acme",
			Name:         "widget",
			MrNumber:     1,
		},
	)
	require.NoError(err)
	require.Equal(http.StatusAccepted, createResp1.StatusCode())
	require.NotNil(createResp1.JSON202)

	ws1 := waitForWorkspaceReady(t, ctx, client, createResp1.JSON202.Id)
	assert.Equal(
		"feature",
		gitOutput(t, ws1.WorktreePath, "branch", "--show-current"),
	)
	assert.Equal(
		"origin",
		gitOutput(
			t, ws1.WorktreePath,
			"config", "--get", "branch.feature.remote",
		),
	)
	assert.Equal(
		"refs/heads/feature",
		gitOutput(
			t, ws1.WorktreePath,
			"config", "--get", "branch.feature.merge",
		),
	)
	runGit(t, clonePath, "fetch", "--prune", "origin")

	createResp2, err := client.HTTP.CreateWorkspaceWithResponse(
		ctx,
		generated.CreateWorkspaceInputBody{
			PlatformHost: "github.com",
			Owner:        "acme",
			Name:         "widget",
			MrNumber:     2,
		},
	)
	require.NoError(err)
	require.Equal(http.StatusAccepted, createResp2.StatusCode())
	require.NotNil(createResp2.JSON202)

	ws2 := waitForWorkspaceReady(t, ctx, client, createResp2.JSON202.Id)
	assert.Equal(
		"middleman/pr-2",
		gitOutput(t, ws2.WorktreePath, "branch", "--show-current"),
	)
	assert.Equal(
		testGitSHA(t, ws1.WorktreePath, "HEAD"),
		testGitSHA(t, ws2.WorktreePath, "HEAD"),
	)
}

func TestWorkspaceCreateSameRepoHeadCloneURLTracksOriginBranchE2E(t *testing.T) {
	t.Parallel()

	require := require.New(t)
	assert := Assert.New(t)

	client, database, clonePath, remotePath := setupTestServerWithWorkspaces(t)
	ctx := t.Context()

	headSHA := testGitSHA(t, remotePath, "refs/heads/feature")
	runGit(t, remotePath, "update-ref", "refs/pull/2/head", headSHA)
	runGit(t, clonePath, "update-ref", "refs/pull/2/head", headSHA)
	seedPR(
		t,
		database,
		"acme", "widget", 2,
		withSeedPRHeadRepoCloneURL("https://github.com/acme/widget.git"),
	)

	createResp, err := client.HTTP.CreateWorkspaceWithResponse(
		ctx,
		generated.CreateWorkspaceInputBody{
			PlatformHost: "github.com",
			Owner:        "acme",
			Name:         "widget",
			MrNumber:     2,
		},
	)
	require.NoError(err)
	require.Equal(http.StatusAccepted, createResp.StatusCode())
	require.NotNil(createResp.JSON202)

	ws := waitForWorkspaceReady(t, ctx, client, createResp.JSON202.Id)
	stored, err := database.GetWorkspace(ctx, ws.Id)
	require.NoError(err)
	require.NotNil(stored)
	assert.Nil(stored.MRHeadRepo)
	assert.Empty(stored.WorkspaceBranch)
	assert.Equal("feature", gitOutput(t, ws.WorktreePath, "branch", "--show-current"))
	assert.Equal(headSHA, testGitSHA(t, ws.WorktreePath, "HEAD"))
	assert.Equal(
		"origin/feature",
		gitOutput(
			t, ws.WorktreePath,
			"rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{u}",
		),
	)
	assert.Equal(
		"refs/heads/feature",
		gitOutput(
			t, ws.WorktreePath,
			"config", "--get", "branch.feature.merge",
		),
	)
}

func TestWorkspaceCreatePortQualifiedHostTracksOriginBranchE2E(t *testing.T) {
	t.Parallel()

	require := require.New(t)
	assert := Assert.New(t)

	fixture := setupWorkspaceServerFixtureWithHost(
		t, nil, "ghe.example.com:8443",
	)
	ctx := t.Context()

	headSHA := testGitSHA(t, fixture.remote, "refs/heads/feature")
	runGit(t, fixture.remote, "update-ref", "refs/pull/1/head", headSHA)
	_, err := fixture.database.WriteDB().ExecContext(
		ctx,
		`UPDATE middleman_merge_requests
		 SET head_repo_clone_url = ?
		 WHERE number = ?`,
		"https://ghe.example.com:8443/acme/widget.git", 1,
	)
	require.NoError(err)

	createResp, err := fixture.client.HTTP.CreateWorkspaceWithResponse(
		ctx,
		generated.CreateWorkspaceInputBody{
			PlatformHost: "ghe.example.com:8443",
			Owner:        "acme",
			Name:         "widget",
			MrNumber:     1,
		},
	)
	require.NoError(err)
	require.Equal(http.StatusAccepted, createResp.StatusCode())
	require.NotNil(createResp.JSON202)

	ws := waitForWorkspaceReady(t, ctx, fixture.client, createResp.JSON202.Id)
	stored, err := fixture.database.GetWorkspace(ctx, ws.Id)
	require.NoError(err)
	require.NotNil(stored)
	assert.Nil(stored.MRHeadRepo)
	assert.Equal("feature", gitOutput(t, ws.WorktreePath, "branch", "--show-current"))
	assert.Equal(
		"origin/feature",
		gitOutput(
			t, ws.WorktreePath,
			"rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{u}",
		),
	)
}

func TestWorkspaceDeleteRecreatesForkBranchName(t *testing.T) {
	t.Parallel()

	assert := Assert.New(t)
	require := require.New(t)

	client, database, _, remotePath := setupTestServerWithWorkspaces(t)
	ctx := t.Context()

	repo, err := database.GetRepoByHostOwnerName(
		ctx, "github.com", "acme", "widget",
	)
	require.NoError(err)
	require.NotNil(repo)

	headSHA := testGitSHA(t, remotePath, "feature")
	runGit(t, remotePath, "update-ref", "refs/pull/2/head", headSHA)

	now := time.Now().UTC().Truncate(time.Second)
	forkPR := &db.MergeRequest{
		RepoID:           repo.ID,
		PlatformID:       2000,
		Number:           2,
		URL:              "https://github.com/acme/widget/pull/2",
		Title:            "Fork PR #2",
		Author:           "fork-user",
		State:            "open",
		Body:             "fork test body",
		HeadBranch:       "fork-feature",
		BaseBranch:       "main",
		HeadRepoCloneURL: "https://github.com/fork/widget.git",
		CreatedAt:        now,
		UpdatedAt:        now,
		LastActivityAt:   now,
	}
	prID, err := database.UpsertMergeRequest(ctx, forkPR)
	require.NoError(err)
	require.NoError(database.EnsureKanbanState(ctx, prID))

	create1, err := client.HTTP.CreateWorkspaceWithResponse(
		ctx,
		generated.CreateWorkspaceInputBody{
			PlatformHost: "github.com",
			Owner:        "acme",
			Name:         "widget",
			MrNumber:     2,
		},
	)
	require.NoError(err)
	require.Equal(http.StatusAccepted, create1.StatusCode())
	require.NotNil(create1.JSON202)

	ws1 := waitForWorkspaceReady(t, ctx, client, create1.JSON202.Id)
	assert.Equal(
		"fork-feature",
		gitOutput(t, ws1.WorktreePath, "branch", "--show-current"),
	)

	force := true
	delete1, err := client.HTTP.DeleteWorkspaceWithResponse(
		ctx, create1.JSON202.Id,
		&generated.DeleteWorkspaceParams{Force: &force},
	)
	require.NoError(err)
	require.Equal(http.StatusNoContent, delete1.StatusCode())

	create2, err := client.HTTP.CreateWorkspaceWithResponse(
		ctx,
		generated.CreateWorkspaceInputBody{
			PlatformHost: "github.com",
			Owner:        "acme",
			Name:         "widget",
			MrNumber:     2,
		},
	)
	require.NoError(err)
	require.Equal(http.StatusAccepted, create2.StatusCode())
	require.NotNil(create2.JSON202)

	ws2 := waitForWorkspaceReady(t, ctx, client, create2.JSON202.Id)
	assert.Equal(
		"fork-feature",
		gitOutput(t, ws2.WorktreePath, "branch", "--show-current"),
	)
	assert.Equal(
		headSHA,
		testGitSHA(t, ws2.WorktreePath, "HEAD"),
	)
}

func TestWorkspaceDeletePreservesUserCreatedBranch(t *testing.T) {
	t.Parallel()

	assert := Assert.New(t)
	require := require.New(t)

	client, _, clonePath, _ := setupTestServerWithWorkspaces(t)
	ctx := t.Context()

	createResp, err := client.HTTP.CreateWorkspaceWithResponse(
		ctx,
		generated.CreateWorkspaceInputBody{
			PlatformHost: "github.com",
			Owner:        "acme",
			Name:         "widget",
			MrNumber:     1,
		},
	)
	require.NoError(err)
	require.Equal(http.StatusAccepted, createResp.StatusCode())
	require.NotNil(createResp.JSON202)

	ws := waitForWorkspaceReady(t, ctx, client, createResp.JSON202.Id)
	runGit(t, ws.WorktreePath, "checkout", "-b", "user-scratch")
	scratchSHA := testGitSHA(t, ws.WorktreePath, "HEAD")

	force := true
	deleteResp, err := client.HTTP.DeleteWorkspaceWithResponse(
		ctx, createResp.JSON202.Id,
		&generated.DeleteWorkspaceParams{Force: &force},
	)
	require.NoError(err)
	require.Equal(http.StatusNoContent, deleteResp.StatusCode())

	assert.Equal(
		scratchSHA,
		testGitSHA(t, clonePath, "refs/heads/user-scratch"),
	)
}

func TestWorkspaceCreatePreservesExistingLocalPreferredBranch(t *testing.T) {
	t.Parallel()

	assert := Assert.New(t)
	require := require.New(t)

	client, _, clonePath, remotePath := setupTestServerWithWorkspaces(t)
	ctx := t.Context()

	privateClone := filepath.Join(t.TempDir(), "private-clone")
	runGit(t, filepath.Dir(privateClone), "clone", clonePath, privateClone)
	runGit(t, privateClone, "config", "user.email", "test@test.com")
	runGit(t, privateClone, "config", "user.name", "Test")
	runGit(t, privateClone, "checkout", "feature")

	require.NoError(os.WriteFile(
		filepath.Join(privateClone, "private.txt"),
		[]byte("private\n"), 0o644,
	))
	runGit(t, privateClone, "add", "private.txt")
	runGit(t, privateClone, "commit", "-m", "private commit")
	privateSHA := testGitSHA(t, privateClone, "HEAD")
	runGit(t, privateClone, "push", clonePath, "HEAD:feature")

	originSHA := testGitSHA(t, remotePath, "refs/heads/feature")
	assert.NotEqual(originSHA, privateSHA)
	assert.Equal(privateSHA, testGitSHA(t, clonePath, "refs/heads/feature"))

	createResp, err := client.HTTP.CreateWorkspaceWithResponse(
		ctx,
		generated.CreateWorkspaceInputBody{
			PlatformHost: "github.com",
			Owner:        "acme",
			Name:         "widget",
			MrNumber:     1,
		},
	)
	require.NoError(err)
	require.Equal(http.StatusAccepted, createResp.StatusCode())
	require.NotNil(createResp.JSON202)

	ws := waitForWorkspaceReady(t, ctx, client, createResp.JSON202.Id)
	assert.Equal(
		"middleman/pr-1",
		gitOutput(t, ws.WorktreePath, "branch", "--show-current"),
	)
	assert.Equal(originSHA, testGitSHA(t, ws.WorktreePath, "HEAD"))
	assert.Equal(privateSHA, testGitSHA(t, clonePath, "refs/heads/feature"))

	force := true
	deleteResp, err := client.HTTP.DeleteWorkspaceWithResponse(
		ctx, createResp.JSON202.Id,
		&generated.DeleteWorkspaceParams{Force: &force},
	)
	require.NoError(err)
	require.Equal(http.StatusNoContent, deleteResp.StatusCode())

	assert.Equal(privateSHA, testGitSHA(t, clonePath, "refs/heads/feature"))
}

func TestWorkspaceDeleteLegacySyntheticBranchAllowsRecreate(t *testing.T) {
	t.Parallel()

	assert := Assert.New(t)
	require := require.New(t)

	client, database, clonePath, remotePath := setupTestServerWithWorkspaces(t)
	ctx := t.Context()

	privateClone := filepath.Join(t.TempDir(), "legacy-private-clone")
	runGit(t, filepath.Dir(privateClone), "clone", clonePath, privateClone)
	runGit(t, privateClone, "config", "user.email", "test@test.com")
	runGit(t, privateClone, "config", "user.name", "Test")
	runGit(t, privateClone, "checkout", "feature")
	require.NoError(os.WriteFile(
		filepath.Join(privateClone, "legacy-private.txt"),
		[]byte("legacy private\n"), 0o644,
	))
	runGit(t, privateClone, "add", "legacy-private.txt")
	runGit(t, privateClone, "commit", "-m", "legacy private commit")
	privateSHA := testGitSHA(t, privateClone, "HEAD")
	runGit(t, privateClone, "push", clonePath, "HEAD:feature")
	originSHA := testGitSHA(t, remotePath, "refs/heads/feature")
	assert.NotEqual(originSHA, privateSHA)

	createResp, err := client.HTTP.CreateWorkspaceWithResponse(
		ctx,
		generated.CreateWorkspaceInputBody{
			PlatformHost: "github.com",
			Owner:        "acme",
			Name:         "widget",
			MrNumber:     1,
		},
	)
	require.NoError(err)
	require.Equal(http.StatusAccepted, createResp.StatusCode())
	require.NotNil(createResp.JSON202)

	ws := waitForWorkspaceReady(t, ctx, client, createResp.JSON202.Id)
	assert.Equal(
		"middleman/pr-1",
		gitOutput(t, ws.WorktreePath, "branch", "--show-current"),
	)

	_, err = database.WriteDB().ExecContext(ctx, `
		UPDATE middleman_workspaces
		SET workspace_branch = '__middleman_unknown__'
		WHERE id = ?`,
		createResp.JSON202.Id,
	)
	require.NoError(err)

	force := true
	deleteResp, err := client.HTTP.DeleteWorkspaceWithResponse(
		ctx, createResp.JSON202.Id,
		&generated.DeleteWorkspaceParams{Force: &force},
	)
	require.NoError(err)
	require.Equal(http.StatusNoContent, deleteResp.StatusCode())

	runGit(t, clonePath, "fetch", "--prune", "origin")

	recreateResp, err := client.HTTP.CreateWorkspaceWithResponse(
		ctx,
		generated.CreateWorkspaceInputBody{
			PlatformHost: "github.com",
			Owner:        "acme",
			Name:         "widget",
			MrNumber:     1,
		},
	)
	require.NoError(err)
	require.Equal(http.StatusAccepted, recreateResp.StatusCode())
	require.NotNil(recreateResp.JSON202)

	recreated := waitForWorkspaceReady(t, ctx, client, recreateResp.JSON202.Id)
	assert.Equal(
		"middleman/pr-1",
		gitOutput(t, recreated.WorktreePath, "branch", "--show-current"),
	)
	assert.Equal(originSHA, testGitSHA(t, recreated.WorktreePath, "HEAD"))
}

func TestWorkspacePRDetailPlatformHost(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	database := dbtest.Open(t)

	// Seed same owner/name on different hosts to test ambiguity.
	seedPROnHost(
		t, database,
		"github.com", "acme", "widget", 10,
	)
	seedPROnHost(
		t, database,
		"ghe.example.com", "acme", "widget", 20,
	)

	mock := &mockGH{}
	repos := []ghclient.RepoRef{
		{
			Owner: "acme", Name: "widget",
			PlatformHost: "github.com",
		},
		{
			Owner: "acme", Name: "widget",
			PlatformHost: "ghe.example.com",
		},
	}
	syncer := ghclient.NewSyncer(
		map[string]ghclient.Client{
			"github.com":      mock,
			"ghe.example.com": mock,
		},
		database, nil, repos, time.Minute, nil, nil,
	)
	t.Cleanup(syncer.Stop)

	srv := New(
		database, syncer, nil, "/", nil, ServerOptions{},
	)
	t.Cleanup(func() { gracefulShutdown(t, srv) })
	client := setupTestClient(t, srv)
	ctx := t.Context()

	// PR on github.com
	r1, err := client.HTTP.GetPullWithResponse(
		ctx, "gh", "acme", "widget", 10,
	)
	require.NoError(err)
	require.Equal(http.StatusOK, r1.StatusCode())
	require.NotNil(r1.JSON200)
	assert.Equal("github.com", r1.JSON200.PlatformHost)

	// PR on ghe.example.com (same owner/name, different number)
	r2, err := client.HTTP.GetPullOnHostWithResponse(
		ctx, "ghe.example.com", "gh", "acme", "widget", 20,
	)
	require.NoError(err)
	require.Equal(http.StatusOK, r2.StatusCode())
	require.NotNil(r2.JSON200)
	assert.Equal("ghe.example.com", r2.JSON200.PlatformHost)
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

	repoID, err := database.UpsertRepo(ctx, db.GitHubRepoIdentity(host, owner, name))
	require.NoError(t, err)

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

func TestWorkspaceDeleteDirty(t *testing.T) {
	t.Parallel()

	require := require.New(t)
	assert := Assert.New(t)

	client, database, _, _ := setupTestServerWithWorkspaces(t)
	ctx := t.Context()

	// Create workspace.
	createResp, err := client.HTTP.CreateWorkspaceWithResponse(
		ctx,
		generated.CreateWorkspaceInputBody{
			PlatformHost: "github.com",
			Owner:        "acme",
			Name:         "widget",
			MrNumber:     1,
		},
	)
	require.NoError(err)
	require.Equal(http.StatusAccepted, createResp.StatusCode())
	require.NotNil(createResp.JSON202)
	wsID := createResp.JSON202.Id

	ready := waitForWorkspaceReady(t, ctx, client, wsID)
	wsPath := ready.WorktreePath

	// Write a dirty file into the worktree.
	require.NoError(os.WriteFile(
		filepath.Join(wsPath, "dirty.txt"),
		[]byte("uncommitted\n"), 0o644,
	))

	// DELETE without force -> 409.
	delResp, err := client.HTTP.DeleteWorkspaceWithResponse(
		ctx, wsID, &generated.DeleteWorkspaceParams{},
	)
	require.NoError(err)
	assert.Equal(http.StatusConflict, delResp.StatusCode())

	// DELETE with force -> 204.
	force := true
	delResp2, err := client.HTTP.DeleteWorkspaceWithResponse(
		ctx, wsID,
		&generated.DeleteWorkspaceParams{Force: &force},
	)
	require.NoError(err)
	assert.Equal(http.StatusNoContent, delResp2.StatusCode())

	// Verify deleted.
	getResp, err := client.HTTP.GetWorkspaceWithResponse(
		ctx, wsID,
	)
	require.NoError(err)
	assert.Equal(http.StatusNotFound, getResp.StatusCode())

	// --- Second scenario: corrupt/missing worktree ---
	// Seed a second PR and create a workspace for it.
	seedPR(t, database, "acme", "widget", 2)
	create2, err := client.HTTP.CreateWorkspaceWithResponse(
		ctx,
		generated.CreateWorkspaceInputBody{
			PlatformHost: "github.com",
			Owner:        "acme",
			Name:         "widget",
			MrNumber:     2,
		},
	)
	require.NoError(err)
	require.Equal(http.StatusAccepted, create2.StatusCode())
	ws2ID := create2.JSON202.Id

	ready2 := waitForWorkspaceReady(t, ctx, client, ws2ID)
	ws2Path := ready2.WorktreePath

	// Nuke the worktree directory to simulate corruption.
	require.NoError(os.RemoveAll(ws2Path))

	// DELETE without force → 409 (dirty check fails on missing dir).
	del3, err := client.HTTP.DeleteWorkspaceWithResponse(
		ctx, ws2ID, &generated.DeleteWorkspaceParams{},
	)
	require.NoError(err)
	assert.Equal(http.StatusConflict, del3.StatusCode())

	// DELETE with force → 204.
	del4, err := client.HTTP.DeleteWorkspaceWithResponse(
		ctx, ws2ID,
		&generated.DeleteWorkspaceParams{Force: &force},
	)
	require.NoError(err)
	assert.Equal(http.StatusNoContent, del4.StatusCode())

	// Verify deleted.
	get2, err := client.HTTP.GetWorkspaceWithResponse(ctx, ws2ID)
	require.NoError(err)
	assert.Equal(http.StatusNotFound, get2.StatusCode())
}

// --- edit-pr-content (PATCH) tests ---

func TestAPIEditPRTitleAndBody(t *testing.T) {
	require := require.New(t)
	srv, database := setupTestServer(t)
	seedPR(t, database, "acme", "widget", 1)

	rr := doJSON(t, srv, http.MethodPatch,
		"/api/v1/pulls/gh/acme/widget/1",
		map[string]string{"title": "updated title", "body": "updated body"})
	require.Equal(http.StatusOK, rr.Code)

	mr, err := database.GetMergeRequest(
		t.Context(), "acme", "widget", 1,
	)
	require.NoError(err)
	require.Equal("updated title", mr.Title)
	require.Equal("updated body", mr.Body)
}

func TestAPIEditPRTitleOnly(t *testing.T) {
	require := require.New(t)
	srv, database := setupTestServer(t)
	seedPR(t, database, "acme", "widget", 1)

	rr := doJSON(t, srv, http.MethodPatch,
		"/api/v1/pulls/gh/acme/widget/1",
		map[string]string{"title": "new title"})
	require.Equal(http.StatusOK, rr.Code)

	mr, err := database.GetMergeRequest(
		t.Context(), "acme", "widget", 1,
	)
	require.NoError(err)
	require.Equal("new title", mr.Title)
	require.Equal("test body", mr.Body)
}

func TestAPIEditPRBodyOnly(t *testing.T) {
	require := require.New(t)
	srv, database := setupTestServer(t)
	seedPR(t, database, "acme", "widget", 1)

	rr := doJSON(t, srv, http.MethodPatch,
		"/api/v1/pulls/gh/acme/widget/1",
		map[string]string{"body": "new body"})
	require.Equal(http.StatusOK, rr.Code)

	mr, err := database.GetMergeRequest(
		t.Context(), "acme", "widget", 1,
	)
	require.NoError(err)
	require.Equal("Test PR #1", mr.Title)
	require.Equal("new body", mr.Body)
}

func TestAPIEditPRClearBody(t *testing.T) {
	require := require.New(t)
	srv, database := setupTestServer(t)
	seedPR(t, database, "acme", "widget", 1)

	rr := doJSON(t, srv, http.MethodPatch,
		"/api/v1/pulls/gh/acme/widget/1",
		map[string]string{"body": ""})
	require.Equal(http.StatusOK, rr.Code)

	mr, err := database.GetMergeRequest(
		t.Context(), "acme", "widget", 1,
	)
	require.NoError(err)
	require.Equal("Test PR #1", mr.Title)
	require.Empty(mr.Body)
}

func TestAPIEditPRNoFields400(t *testing.T) {
	require := require.New(t)
	srv, database := setupTestServer(t)
	seedPR(t, database, "acme", "widget", 1)

	rr := doJSON(t, srv, http.MethodPatch,
		"/api/v1/pulls/gh/acme/widget/1",
		map[string]any{})
	require.Equal(http.StatusBadRequest, rr.Code)
}

func TestAPIEditPRBlankTitle400(t *testing.T) {
	require := require.New(t)
	srv, database := setupTestServer(t)
	seedPR(t, database, "acme", "widget", 1)

	rr := doJSON(t, srv, http.MethodPatch,
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
	repo, err := database.GetRepoByOwnerName(ctx, "acme", "widget")
	require.NoError(err)
	now := time.Now().UTC().Truncate(time.Second)
	require.NoError(database.UpdateMRDerivedFields(ctx, repo.ID, 1, db.MRDerivedFields{
		ReviewDecision: "APPROVED",
		CommentCount:   7,
		LastActivityAt: now,
	}))
	require.NoError(database.UpdateMRCIStatus(ctx, repo.ID, 1, "success", "[]"))

	rr := doJSON(t, srv, http.MethodPatch,
		"/api/v1/pulls/gh/acme/widget/1",
		map[string]string{"title": "changed title"})
	require.Equal(http.StatusOK, rr.Code)

	after, err := database.GetMergeRequest(ctx, "acme", "widget", 1)
	require.NoError(err)
	require.Equal("changed title", after.Title)
	require.Equal(7, after.CommentCount)
	require.Equal("success", after.CIStatus)
	require.Equal("APPROVED", after.ReviewDecision)
	require.Equal(db.MergeRequestStateOpen, after.State)
}

// --- edit-issue-content (PATCH) tests ---

func TestAPIEditIssueBodyOnly(t *testing.T) {
	require := require.New(t)
	srv, database := setupTestServer(t)
	seedIssue(t, database, "acme", "widget", 5, "open")

	rr := doJSON(t, srv, http.MethodPatch,
		"/api/v1/issues/gh/acme/widget/5",
		map[string]string{"body": "- [x] task done"})
	require.Equal(http.StatusOK, rr.Code)

	issue, err := database.GetIssue(t.Context(), "acme", "widget", 5)
	require.NoError(err)
	require.NotNil(issue)
	require.Equal("Test Issue", issue.Title)
	require.Equal("- [x] task done", issue.Body)
}

func TestAPIEditIssueTitleAndBody(t *testing.T) {
	require := require.New(t)
	srv, database := setupTestServer(t)
	seedIssue(t, database, "acme", "widget", 5, "open")

	rr := doJSON(t, srv, http.MethodPatch,
		"/api/v1/issues/gh/acme/widget/5",
		map[string]string{"title": "new title", "body": "new body"})
	require.Equal(http.StatusOK, rr.Code)

	issue, err := database.GetIssue(t.Context(), "acme", "widget", 5)
	require.NoError(err)
	require.NotNil(issue)
	require.Equal("new title", issue.Title)
	require.Equal("new body", issue.Body)
}

func TestAPIEditIssueNoFields400(t *testing.T) {
	require := require.New(t)
	srv, database := setupTestServer(t)
	seedIssue(t, database, "acme", "widget", 5, "open")

	rr := doJSON(t, srv, http.MethodPatch,
		"/api/v1/issues/gh/acme/widget/5",
		map[string]any{})
	require.Equal(http.StatusBadRequest, rr.Code)
}

func TestAPIEditIssueBlankTitle400(t *testing.T) {
	require := require.New(t)
	srv, database := setupTestServer(t)
	seedIssue(t, database, "acme", "widget", 5, "open")

	rr := doJSON(t, srv, http.MethodPatch,
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
		db.GitHubRepoIdentity("github.com", "acme", "widget"),
	)
	require.NoError(err)

	rr := doJSON(t, srv, http.MethodPatch,
		"/api/v1/issues/gh/acme/widget/999",
		map[string]string{"body": "anything"})
	require.Equal(http.StatusNotFound, rr.Code)
}
