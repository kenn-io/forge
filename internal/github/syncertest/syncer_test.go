package syncertest

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	gh "github.com/google/go-github/v84/github"
	Assert "github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/middleman/internal/db"
	ghclient "go.kenn.io/middleman/internal/github"
	"go.kenn.io/middleman/internal/testutil/dbtest"
)

func openTestDB(t *testing.T) *db.DB {
	t.Helper()
	return dbtest.Open(t)
}

type mockClient struct {
	openPRs           []*gh.PullRequest
	listOpenPRsFn     func(context.Context, string, string) ([]*gh.PullRequest, error)
	listOpenPRsCalled bool
	getRepositoryFn   func(context.Context, string, string) (*gh.Repository, error)
}

func (m *mockClient) ListOpenPullRequests(
	ctx context.Context, owner, repo string,
) ([]*gh.PullRequest, error) {
	m.listOpenPRsCalled = true
	if m.listOpenPRsFn != nil {
		return m.listOpenPRsFn(ctx, owner, repo)
	}
	return m.openPRs, nil
}

func (m *mockClient) GetRepository(
	ctx context.Context, owner, repo string,
) (*gh.Repository, error) {
	if m.getRepositoryFn != nil {
		return m.getRepositoryFn(ctx, owner, repo)
	}
	id := int64(1)
	nodeID := "repo-" + owner + "-" + repo
	return &gh.Repository{
		ID:       &id,
		NodeID:   &nodeID,
		Name:     &repo,
		Owner:    &gh.User{Login: &owner},
		Archived: new(bool),
	}, nil
}

func (m *mockClient) GetPullRequest(context.Context, string, string, int) (*gh.PullRequest, error) {
	return nil, nil
}
func (m *mockClient) GetUser(context.Context, string) (*gh.User, error) { return nil, nil }
func (m *mockClient) ListRepositoriesByOwner(context.Context, string) ([]*gh.Repository, error) {
	return nil, nil
}
func (m *mockClient) ListReleases(context.Context, string, string, int) ([]*gh.RepositoryRelease, error) {
	return nil, nil
}
func (m *mockClient) ListTags(context.Context, string, string, int) ([]*gh.RepositoryTag, error) {
	return nil, nil
}
func (m *mockClient) ListOpenIssues(context.Context, string, string) ([]*gh.Issue, error) {
	return nil, nil
}
func (m *mockClient) GetIssue(context.Context, string, string, int) (*gh.Issue, error) {
	return nil, nil
}
func (m *mockClient) CreateIssue(context.Context, string, string, string, string) (*gh.Issue, error) {
	return nil, nil
}
func (m *mockClient) ListIssueComments(context.Context, string, string, int) ([]*gh.IssueComment, error) {
	return nil, nil
}
func (m *mockClient) ListIssueCommentsIfChanged(context.Context, string, string, int) ([]*gh.IssueComment, error) {
	return nil, nil
}
func (m *mockClient) ListReviews(context.Context, string, string, int) ([]*gh.PullRequestReview, error) {
	return nil, nil
}
func (m *mockClient) ListPullRequestReviewThreads(context.Context, string, string, int) ([]ghclient.PullRequestReviewThread, error) {
	return nil, nil
}
func (m *mockClient) ListCommits(context.Context, string, string, int) ([]*gh.RepositoryCommit, error) {
	return nil, nil
}
func (m *mockClient) ListPullRequestTimelineEvents(context.Context, string, string, int) ([]ghclient.PullRequestTimelineEvent, error) {
	return nil, nil
}
func (m *mockClient) ListForcePushEvents(context.Context, string, string, int) ([]ghclient.ForcePushEvent, error) {
	return nil, nil
}
func (m *mockClient) GetCombinedStatus(context.Context, string, string, string) (*gh.CombinedStatus, error) {
	return nil, nil
}
func (m *mockClient) ListCheckRunsForRef(context.Context, string, string, string) ([]*gh.CheckRun, error) {
	return nil, nil
}
func (m *mockClient) ListWorkflowRunsForHeadSHA(context.Context, string, string, string) ([]*gh.WorkflowRun, error) {
	return nil, nil
}
func (m *mockClient) ApproveWorkflowRun(context.Context, string, string, int64) error {
	return nil
}
func (m *mockClient) CreateIssueComment(context.Context, string, string, int, string) (*gh.IssueComment, error) {
	return nil, nil
}
func (m *mockClient) EditIssueComment(context.Context, string, string, int64, string) (*gh.IssueComment, error) {
	return nil, nil
}
func (m *mockClient) CreatePullRequestReviewCommentReply(
	context.Context, string, string, int, string, int64,
) (*gh.PullRequestComment, error) {
	return nil, nil
}
func (m *mockClient) CreateReview(context.Context, string, string, int, string, string) (*gh.PullRequestReview, error) {
	return nil, nil
}
func (m *mockClient) CreateReviewWithComments(
	context.Context,
	string,
	string,
	int,
	string,
	string,
	string,
	[]*gh.DraftReviewComment,
) (*gh.PullRequestReview, error) {
	return nil, nil
}
func (m *mockClient) MarkPullRequestReadyForReview(context.Context, string, string, int) (*gh.PullRequest, error) {
	return nil, nil
}
func (m *mockClient) MergePullRequest(context.Context, string, string, int, string, string, string) (*gh.PullRequestMergeResult, error) {
	return nil, nil
}
func (m *mockClient) EditPullRequest(context.Context, string, string, int, ghclient.EditPullRequestOpts) (*gh.PullRequest, error) {
	return nil, nil
}
func (m *mockClient) EditIssue(context.Context, string, string, int, string) (*gh.Issue, error) {
	return nil, nil
}
func (m *mockClient) EditIssueContent(context.Context, string, string, int, *string, *string) (*gh.Issue, error) {
	return nil, nil
}
func (m *mockClient) ListPullRequestsPage(context.Context, string, string, string, int) ([]*gh.PullRequest, bool, error) {
	return nil, false, nil
}
func (m *mockClient) ListIssuesPage(context.Context, string, string, string, int) ([]*gh.Issue, bool, error) {
	return nil, false, nil
}
func (m *mockClient) InvalidateListETagsForRepo(string, string, ...string) {}

func TestSyncerStopIsIdempotent(t *testing.T) {
	syncer := ghclient.NewSyncer(
		map[string]ghclient.Client{"github.com": &mockClient{}},
		nil, nil, nil, time.Minute, nil, nil,
	)
	syncer.Stop()
	syncer.Stop()
}

type blockingMockClient struct {
	mockClient
	entered chan struct{}
	blocked chan struct{}
}

func (b *blockingMockClient) ListOpenPullRequests(
	_ context.Context, _, _ string,
) ([]*gh.PullRequest, error) {
	if b.entered != nil {
		select {
		case b.entered <- struct{}{}:
		default:
		}
	}
	<-b.blocked
	return nil, nil
}

func TestSyncerStopWaitsForRunOnce(t *testing.T) {
	entered := make(chan struct{})
	blocked := make(chan struct{})
	mock := &blockingMockClient{
		entered: entered,
		blocked: blocked,
	}

	database := openTestDB(t)
	syncer := ghclient.NewSyncer(
		map[string]ghclient.Client{"github.com": mock}, database, nil,
		[]ghclient.RepoRef{{
			Owner:              "o",
			Name:               "r",
			PlatformHost:       "github.com",
			PlatformExternalID: "repo-o-r",
		}},
		time.Hour, nil, nil,
	)

	syncer.Start(t.Context())
	<-entered

	stopped := make(chan struct{})
	go func() {
		syncer.Stop()
		close(stopped)
	}()

	select {
	case <-stopped:
		require.Fail(t, "Stop returned while RunOnce was still in flight")
	case <-time.After(100 * time.Millisecond):
	}

	close(blocked)

	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		require.Fail(t, "Stop did not return within timeout")
	}
}

type parallelMockClient struct {
	mockClient
	inflight         atomic.Int32
	maxInflight      atomic.Int32
	saturationTarget int32
	saturated        chan struct{}
	saturatedOnce    sync.Once
	block            chan struct{}
}

func (p *parallelMockClient) ListOpenPullRequests(
	_ context.Context, _, _ string,
) ([]*gh.PullRequest, error) {
	n := p.inflight.Add(1)
	defer p.inflight.Add(-1)
	for {
		current := p.maxInflight.Load()
		if n <= current || p.maxInflight.CompareAndSwap(current, n) {
			break
		}
	}
	if n == p.saturationTarget && p.saturated != nil {
		p.saturatedOnce.Do(func() { close(p.saturated) })
	}
	<-p.block
	return nil, nil
}

func TestRunOnceSyncesReposInParallel(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	d := openTestDB(t)

	const parallelism = 3
	const repoCount = 5

	mc := &parallelMockClient{
		block:            make(chan struct{}),
		saturated:        make(chan struct{}),
		saturationTarget: parallelism,
	}
	repos := make([]ghclient.RepoRef, repoCount)
	for i := range repos {
		repos[i] = ghclient.RepoRef{
			Owner:        "o",
			Name:         fmt.Sprintf("r%d", i),
			PlatformHost: "github.com",
		}
	}

	syncer := ghclient.NewSyncer(
		map[string]ghclient.Client{"github.com": mc}, d, nil, repos,
		time.Minute, nil, nil,
	)
	syncer.SetParallelism(parallelism)

	done := make(chan struct{})
	go func() {
		syncer.RunOnce(t.Context())
		close(done)
	}()

	select {
	case <-mc.saturated:
	case <-time.After(2 * time.Second):
		require.Failf(
			"expected worker pool to saturate",
			"expected %d concurrent syncs, got %d",
			parallelism, mc.inflight.Load(),
		)
	}
	assert.LessOrEqual(mc.maxInflight.Load(), int32(parallelism),
		"max concurrency exceeded bound")

	close(mc.block)

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		require.Fail("RunOnce did not complete after unblocking workers")
	}

	assert.Equal(int32(parallelism), mc.maxInflight.Load(),
		"should have reached the parallelism bound exactly")
}

func TestRunOnceCancelDuringBackoffDoesNotReportSuccess(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	d := openTestDB(t)

	rt := ghclient.NewRateTracker(d, "github.com", "rest")
	resetAt := time.Now().Add(time.Hour)
	rt.UpdateFromRate(ghclient.Rate{
		Remaining: 0,
		Reset:     resetAt,
	})

	mc := &mockClient{}
	syncer := ghclient.NewSyncer(
		map[string]ghclient.Client{"github.com": mc}, d, nil,
		[]ghclient.RepoRef{{Owner: "o", Name: "r", PlatformHost: "github.com"}},
		time.Minute,
		map[string]*ghclient.RateTracker{"github.com": rt}, nil,
	)

	var completedCalled atomic.Bool
	syncer.SetOnSyncCompleted(func([]ghclient.RepoSyncResult) {
		completedCalled.Store(true)
	})
	backoffReached := make(chan struct{})
	var backoffReachedOnce sync.Once
	syncer.SetOnStatusChange(func(status *ghclient.SyncStatus) {
		if strings.Contains(status.Progress, "rate limited, waiting") {
			backoffReachedOnce.Do(func() { close(backoffReached) })
		}
	})

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		syncer.RunOnce(ctx)
		close(done)
	}()

	select {
	case <-backoffReached:
	case <-time.After(2 * time.Second):
		require.Fail("RunOnce did not reach rate-limit backoff")
	}
	cancel()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		require.Fail("RunOnce did not return after ctx cancel")
	}

	assert.False(completedCalled.Load(),
		"onSyncCompleted must not fire when RunOnce is canceled")
	status := syncer.Status()
	assert.False(status.Running)
	assert.NotEmpty(status.LastError,
		"LastError should reflect the cancellation")
}

func TestRunOnceCancelAfterCompleteReportsSuccess(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	d := openTestDB(t)

	mc := &mockClient{}
	syncer := ghclient.NewSyncer(
		map[string]ghclient.Client{"github.com": mc}, d, nil,
		[]ghclient.RepoRef{}, time.Minute, nil, nil,
	)

	var completedCalled atomic.Bool
	syncer.SetOnSyncCompleted(func([]ghclient.RepoSyncResult) {
		completedCalled.Store(true)
	})

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	done := make(chan struct{})
	go func() {
		syncer.RunOnce(ctx)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		require.Fail("RunOnce did not return")
	}

	assert.True(completedCalled.Load(),
		"onSyncCompleted should fire when no work was outstanding "+
			"at cancel time")
	status := syncer.Status()
	assert.False(status.Running)
	assert.Empty(status.LastError,
		"LastError should be empty when all work completed before cancel")
}

type cancelDuringSyncMockClient struct {
	mockClient
	entered chan struct{}
}

func (c *cancelDuringSyncMockClient) ListOpenPullRequests(
	ctx context.Context, _, _ string,
) ([]*gh.PullRequest, error) {
	select {
	case c.entered <- struct{}{}:
	default:
	}
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestRunOnceCancelDuringSyncRepoDoesNotReportSuccess(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	d := openTestDB(t)

	mc := &cancelDuringSyncMockClient{
		entered: make(chan struct{}, 1),
	}
	syncer := ghclient.NewSyncer(
		map[string]ghclient.Client{"github.com": mc}, d, nil,
		[]ghclient.RepoRef{{Owner: "o", Name: "r", PlatformHost: "github.com"}},
		time.Minute, nil, nil,
	)

	var completedCalled atomic.Bool
	syncer.SetOnSyncCompleted(func([]ghclient.RepoSyncResult) {
		completedCalled.Store(true)
	})

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		syncer.RunOnce(ctx)
		close(done)
	}()

	select {
	case <-mc.entered:
	case <-time.After(2 * time.Second):
		require.Fail("worker did not enter ListOpenPullRequests")
	}

	cancel()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		require.Fail("RunOnce did not return")
	}

	assert.False(completedCalled.Load(),
		"onSyncCompleted must not fire when syncRepo was canceled "+
			"mid-flight")
	status := syncer.Status()
	assert.False(status.Running)
	assert.NotEmpty(status.LastError,
		"LastError should reflect the cancellation")
}

type deadlineExceededMockClient struct {
	mockClient
}

func (c *deadlineExceededMockClient) ListOpenPullRequests(
	_ context.Context, _, _ string,
) ([]*gh.PullRequest, error) {
	return nil, fmt.Errorf("list timed out: %w", context.DeadlineExceeded)
}

func TestRunOncePerRequestDeadlineRecordsError(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	d := openTestDB(t)

	mc := &deadlineExceededMockClient{}
	syncer := ghclient.NewSyncer(
		map[string]ghclient.Client{"github.com": mc}, d, nil,
		[]ghclient.RepoRef{{Owner: "o", Name: "r", PlatformHost: "github.com"}},
		time.Minute, nil, nil,
	)

	var completedCalled atomic.Bool
	syncer.SetOnSyncCompleted(func([]ghclient.RepoSyncResult) {
		completedCalled.Store(true)
	})

	syncer.RunOnce(t.Context())

	status := syncer.Status()
	assert.False(status.Running)
	assert.NotEmpty(status.LastError,
		"per-request DeadlineExceeded should be recorded in LastError")
	assert.Contains(status.LastError, "list timed out",
		"LastError should preserve the wrapped error message")
	require.True(completedCalled.Load(),
		"onSyncCompleted should fire on a finished run with errors")
}

type syncedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (sw *syncedWriter) Write(p []byte) (int, error) {
	sw.mu.Lock()
	defer sw.mu.Unlock()
	return sw.w.Write(p)
}

func TestRunOnceDispatchHonorsCanceledCtx(t *testing.T) {
	assert := Assert.New(t)
	d := openTestDB(t)

	var buf bytes.Buffer
	sw := &syncedWriter{w: &buf}
	h := slog.NewTextHandler(sw, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	})
	orig := slog.Default()
	slog.SetDefault(slog.New(h))
	t.Cleanup(func() { slog.SetDefault(orig) })

	repos := make([]ghclient.RepoRef, 100)
	for i := range repos {
		repos[i] = ghclient.RepoRef{
			Owner:        "o",
			Name:         fmt.Sprintf("r%d", i),
			PlatformHost: "github.com",
		}
	}

	syncer := ghclient.NewSyncer(
		map[string]ghclient.Client{"github.com": &mockClient{}}, d, nil,
		repos, time.Minute, nil, nil,
	)
	syncer.SetParallelism(4)

	for range 20 {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		syncer.RunOnce(ctx)
	}

	sw.mu.Lock()
	output := buf.String()
	sw.mu.Unlock()

	count := strings.Count(output, `msg="syncing repo"`)
	assert.Zero(count,
		"dispatch must not enqueue repos when ctx is pre-canceled "+
			"(observed %d 'syncing repo' log lines)", count)
}

func TestSyncerTriggerRunRunsRunOnce(t *testing.T) {
	assert := Assert.New(t)
	mock := &mockClient{openPRs: []*gh.PullRequest{}}
	d := openTestDB(t)
	_, err := d.UpsertRepo(t.Context(), db.GitHubRepoIdentity("github.com", "o", "n"))
	require.NoError(t, err)
	repos := []ghclient.RepoRef{{Owner: "o", Name: "n", PlatformHost: "github.com"}}
	s := ghclient.NewSyncer(
		map[string]ghclient.Client{"github.com": mock},
		d, nil, repos, time.Hour, nil, nil,
	)

	done := make(chan struct{}, 1)
	s.SetOnStatusChange(func(status *ghclient.SyncStatus) {
		if !status.Running {
			select {
			case done <- struct{}{}:
			default:
			}
		}
	})

	s.TriggerRun(t.Context())

	select {
	case <-done:
	case <-time.After(time.Second):
		require.FailNow(t,
			"TriggerRun did not complete RunOnce within 1s")
	}
	s.Stop()
	assert.True(mock.listOpenPRsCalled,
		"TriggerRun should invoke ListOpenPullRequests")
}

type blockingCtxMockClient struct {
	mockClient
	entered chan struct{}
	release chan struct{}
}

func (b *blockingCtxMockClient) ListOpenPullRequests(
	ctx context.Context, _, _ string,
) ([]*gh.PullRequest, error) {
	if b.entered != nil {
		select {
		case b.entered <- struct{}{}:
		default:
		}
	}
	select {
	case <-b.release:
		return nil, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func TestSyncerStopCancelsTriggerRun(t *testing.T) {
	require := require.New(t)
	d := openTestDB(t)

	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	mock := &blockingCtxMockClient{
		entered: entered,
		release: release,
	}

	syncer := ghclient.NewSyncer(
		map[string]ghclient.Client{"github.com": mock},
		d, nil,
		[]ghclient.RepoRef{{Owner: "o", Name: "r", PlatformHost: "github.com"}},
		time.Hour, nil, nil,
	)

	syncer.TriggerRun(context.WithoutCancel(t.Context()))

	select {
	case <-entered:
	case <-time.After(time.Second):
		require.FailNow("TriggerRun did not start ListOpenPullRequests")
	}

	stopped := make(chan struct{})
	go func() {
		syncer.Stop()
		close(stopped)
	}()

	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		close(release)
		require.FailNow("Stop did not return after ctx cancellation")
	}
}

var _ ghclient.Client = (*mockClient)(nil)
