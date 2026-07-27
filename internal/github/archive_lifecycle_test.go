package github

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	gh "github.com/google/go-github/v88/github"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/middleman/internal/archive"
	"go.kenn.io/middleman/internal/db"
	"go.kenn.io/middleman/internal/platform"
	"go.kenn.io/middleman/internal/testutil/dbtest"
)

type blockingArchiveRunner struct {
	calls   atomic.Int64
	started chan struct{}
	done    chan struct{}
}

type archiveLifecycleRecorder struct {
	ensured []platform.RepoRef
	retried []platform.RepoRef
}

type archiveWorkerProvider struct{ ref platform.RepoRef }

type archiveLifecycleClock struct{ now func() time.Time }

func (c archiveLifecycleClock) Now() time.Time { return c.now() }

type archivePageMockClient struct {
	*mockClient
	listInventoryIssuesPageFn func(
		context.Context, string, string, string, string, string,
	) ([]*gh.Issue, string, bool, error)
	listInventoryPullRequestsPageFn func(
		context.Context, string, string, string, int,
	) ([]*gh.PullRequest, bool, error)
}

func (m *archivePageMockClient) ListInventoryIssuesPage(
	ctx context.Context,
	owner string,
	repo string,
	sortBy string,
	cursor string,
	since string,
) ([]*gh.Issue, string, bool, error) {
	m.trackCall()
	return m.listInventoryIssuesPageFn(ctx, owner, repo, sortBy, cursor, since)
}

func (m *archivePageMockClient) ListInventoryPullRequestsPage(
	ctx context.Context,
	owner string,
	repo string,
	sortBy string,
	page int,
) ([]*gh.PullRequest, bool, error) {
	m.trackCall()
	return m.listInventoryPullRequestsPageFn(ctx, owner, repo, sortBy, page)
}

type priorityWorkOperation string

const (
	priorityWorkIndex priorityWorkOperation = "index"
	priorityWorkMR    priorityWorkOperation = "merge_request"
	priorityWorkIssue priorityWorkOperation = "issue"
)

type blockingPriorityProvider struct {
	syncTestProvider
	ref       platform.RepoRef
	operation priorityWorkOperation
	started   chan struct{}
	release   chan struct{}
	startOnce sync.Once
}

func (p *blockingPriorityProvider) Capabilities() platform.Capabilities {
	return platform.Capabilities{
		ReadRepositories: true, ReadMergeRequests: true, ReadIssues: true,
	}
}

func (p *blockingPriorityProvider) GetRepository(
	context.Context, platform.RepoRef,
) (platform.Repository, error) {
	return platform.Repository{
		Ref: p.ref, PlatformID: 1, PlatformExternalID: "repo-1",
		DefaultBranch: "main",
	}, nil
}

func (*blockingPriorityProvider) ListRepositories(
	context.Context, string, platform.RepositoryListOptions,
) ([]platform.Repository, error) {
	return nil, nil
}

func (p *blockingPriorityProvider) wait(operation priorityWorkOperation) {
	if p.operation != operation {
		return
	}
	p.startOnce.Do(func() { close(p.started) })
	<-p.release
}

func (p *blockingPriorityProvider) ListOpenMergeRequests(
	context.Context, platform.RepoRef,
) ([]platform.MergeRequest, error) {
	p.wait(priorityWorkIndex)
	return nil, nil
}

func (p *blockingPriorityProvider) GetMergeRequest(
	context.Context, platform.RepoRef, int,
) (platform.MergeRequest, error) {
	p.wait(priorityWorkMR)
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	return platform.MergeRequest{
		Repo: p.ref, PlatformID: 7, PlatformExternalID: "mr-7", Number: 7,
		Title: "Synthetic MR", State: "open", CreatedAt: now, UpdatedAt: now,
		LastActivityAt: now,
	}, nil
}

func (*blockingPriorityProvider) ListMergeRequestEvents(
	context.Context, platform.RepoRef, int,
) ([]platform.MergeRequestEvent, error) {
	return nil, nil
}

func (*blockingPriorityProvider) ListOpenIssues(
	context.Context, platform.RepoRef,
) ([]platform.Issue, error) {
	return nil, nil
}

func (p *blockingPriorityProvider) GetIssue(
	context.Context, platform.RepoRef, int,
) (platform.Issue, error) {
	p.wait(priorityWorkIssue)
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	return platform.Issue{
		Repo: p.ref, PlatformID: 8, PlatformExternalID: "issue-8", Number: 8,
		Title: "Synthetic issue", State: "open", CreatedAt: now, UpdatedAt: now,
		LastActivityAt: now,
	}, nil
}

func (*blockingPriorityProvider) ListIssueEvents(
	context.Context, platform.RepoRef, int,
) ([]platform.IssueEvent, error) {
	return nil, nil
}

func (p *archiveWorkerProvider) Platform() platform.Kind { return p.ref.Platform }
func (p *archiveWorkerProvider) Host() string            { return p.ref.Host }
func (*archiveWorkerProvider) Capabilities() platform.Capabilities {
	return platform.Capabilities{
		ReadIssues: true,
		Archive: platform.ArchiveCapabilities{
			HistoricalIssues: true, HistoricalMergeRequests: true,
		},
	}
}
func (p *archiveWorkerProvider) archivedIssue() platform.Issue {
	now := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	return platform.Issue{
		Repo: p.ref, PlatformID: 1, PlatformExternalID: "issue-1", Number: 1,
		URL: "https://github.test/acme/widget/issues/1", Title: "Archived issue",
		Author: "alice", State: "closed", CreatedAt: now, UpdatedAt: now, LastActivityAt: now,
	}
}
func (p *archiveWorkerProvider) ListIssuesPage(_ context.Context, _ platform.RepoRef, query platform.ItemPageQuery) (platform.Page[platform.Issue], error) {
	if query.UpdatedSince != nil {
		return platform.Page[platform.Issue]{Exhausted: true}, nil
	}
	return platform.Page[platform.Issue]{Items: []platform.Issue{p.archivedIssue()}, Exhausted: true}, nil
}
func (*archiveWorkerProvider) ListMergeRequestsPage(context.Context, platform.RepoRef, platform.ItemPageQuery) (platform.Page[platform.MergeRequest], error) {
	return platform.Page[platform.MergeRequest]{Exhausted: true}, nil
}
func (*archiveWorkerProvider) ListOpenIssues(context.Context, platform.RepoRef) ([]platform.Issue, error) {
	return nil, nil
}
func (p *archiveWorkerProvider) GetIssue(context.Context, platform.RepoRef, int) (platform.Issue, error) {
	return p.archivedIssue(), nil
}
func (*archiveWorkerProvider) ListIssueEvents(
	context.Context,
	platform.RepoRef,
	int,
) ([]platform.IssueEvent, error) {
	return nil, nil
}

// preemptibleArchiveProvider blocks its first archive issue read until the
// admitted request context is canceled, so a test can drive real live-work
// preemption of an in-flight archive request. Subsequent reads succeed.
type preemptibleArchiveProvider struct {
	*archiveWorkerProvider
	readStarted chan struct{}
	blockOnce   sync.Once
}

func (p *preemptibleArchiveProvider) GetIssue(
	ctx context.Context, ref platform.RepoRef, number int,
) (platform.Issue, error) {
	blocked := false
	p.blockOnce.Do(func() { blocked = true })
	if blocked {
		close(p.readStarted)
		<-ctx.Done()
		return platform.Issue{}, ctx.Err()
	}
	return p.archiveWorkerProvider.GetIssue(ctx, ref, number)
}

func TestArchivePreemptedItemRecordsNoFailureAndCompletesOnNextPass(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	database := dbtest.Open(t)
	ref := platform.RepoRef{
		Platform: platform.KindGitHub, Host: "github.test",
		Owner: "acme", Name: "widget", RepoPath: "acme/widget",
	}
	provider := &preemptibleArchiveProvider{
		archiveWorkerProvider: &archiveWorkerProvider{ref: ref},
		readStarted:           make(chan struct{}),
	}
	registry, err := platform.NewRegistry(provider)
	require.NoError(err)

	now := time.Now().UTC()
	key := RateBucketKey("github", ref.Host, "host")
	tracker := NewPlatformRateTracker(database, "github", ref.Host, "host", "rest")
	tracker.UpdateFromRate(Rate{Limit: 5000, Remaining: 4999, Reset: now.Add(time.Minute)})
	budget := NewSyncBudget(5000)
	syncer := NewSyncerWithRegistry(registry, database, nil, []RepoRef{{
		Platform: ref.Platform, PlatformHost: ref.Host,
		Owner: ref.Owner, Name: ref.Name, RepoPath: ref.RepoPath,
	}}, time.Hour, map[string]*RateTracker{key: tracker}, map[string]*SyncBudget{key: budget})
	syncer.now = func() time.Time { return now }

	// Real Syncer admission (not nil): the admitted archive request gets a real
	// cancellable provider lease that live work can preempt, unlike a fake
	// already-canceled admission.
	service, err := archive.NewService(database, registry, syncer, syncer, nil, nil)
	require.NoError(err)
	require.NoError(service.EnsureConfigured(t.Context(), []platform.RepoRef{ref}))
	_, err = service.Start(t.Context(), []platform.RepoRef{ref})
	require.NoError(err)

	require.NoError(service.RunEligible(t.Context())) // issue inventory
	require.NoError(service.RunEligible(t.Context())) // merge-request inventory

	repo, err := database.GetRepoByIdentity(t.Context(), platform.DBRepoIdentity(ref))
	require.NoError(err)
	require.NotNil(repo)

	// The archive pass delegates to the existing issue sync, which blocks in
	// its canonical lookup until live work begins.
	hydrationDone := make(chan error, 1)
	go func() { hydrationDone <- service.RunEligible(t.Context()) }()
	select {
	case <-provider.readStarted:
	case <-time.After(2 * time.Second):
		require.Fail("archive item sync did not start")
	}

	// beginProviderWork cancels the in-flight archive lease and waits for it to
	// release; the hydration pass observes preemption and records nothing.
	releaseLive := syncer.beginProviderWork(key, archive.PriorityActiveDetail)
	require.NoError(<-hydrationDone)

	lookup, err := database.GetDatasetProgress(
		t.Context(), repo.ID, db.ArchiveItemTypeIssue, 1, db.ArchiveDatasetLookup,
	)
	require.NoError(err)
	assert.Equal(db.ArchiveDatasetProgressPending, lookup.Status)
	assert.Zero(lookup.AttemptCount)

	var lastErrorCode *string
	var nextRetryAt *time.Time
	require.NoError(database.ReadDB().QueryRowContext(t.Context(), `
		SELECT last_error_code, next_retry_at FROM middleman_archive_repos WHERE repo_id = ?`, repo.ID,
	).Scan(&lastErrorCode, &nextRetryAt))
	assert.Nil(lastErrorCode)
	assert.Nil(nextRetryAt)

	// Live work releases; the next archive pass claims the still-pending item
	// and completes hydration.
	releaseLive()
	require.NoError(service.RunEligible(t.Context()))

	lookup, err = database.GetDatasetProgress(
		t.Context(), repo.ID, db.ArchiveItemTypeIssue, 1, db.ArchiveDatasetLookup,
	)
	require.NoError(err)
	assert.Equal(db.ArchiveDatasetProgressComplete, lookup.Status)
	assert.NotNil(lookup.CompletedAt)
}

func TestArchiveDisabledIssueInventoryCompletesUnsupportedWithoutBlockingMergeRequests(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	database := dbtest.Open(t)
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	ref := platform.RepoRef{
		Platform: platform.KindGitHub, Host: "github.com",
		Owner: "acme", Name: "widget", RepoPath: "acme/widget",
	}
	rawDisabled := fmt.Errorf("list archive issues: %w", &gh.ErrorResponse{
		Response: &http.Response{StatusCode: http.StatusGone},
		Message:  "Issues are disabled for this repo",
	})
	var issueCalls atomic.Int32
	var issueInventoryCalls atomic.Int32
	var mergeRequestCalls atomic.Int32
	client := &archivePageMockClient{
		mockClient: &mockClient{},
		listInventoryIssuesPageFn: func(
			_ context.Context, _, _, sortBy, _, _ string,
		) ([]*gh.Issue, string, bool, error) {
			issueCalls.Add(1)
			if sortBy == "created" {
				issueInventoryCalls.Add(1)
			}
			return nil, "", false, rawDisabled
		},
		listInventoryPullRequestsPageFn: func(
			context.Context, string, string, string, int,
		) ([]*gh.PullRequest, bool, error) {
			mergeRequestCalls.Add(1)
			return nil, false, nil
		},
	}
	tracker := NewPlatformRateTracker(database, "github", "github.com", "host", "rest")
	tracker.UpdateFromRate(Rate{
		Limit: 5000, Remaining: 4999, Reset: now.Add(time.Minute),
	})
	syncer := NewSyncer(
		map[string]Client{"github.com": client}, database, nil,
		[]RepoRef{{
			Platform: platform.KindGitHub, PlatformHost: "github.com",
			Owner: "acme", Name: "widget",
		}},
		time.Hour, map[string]*RateTracker{"github.com": tracker}, testBudget(5000),
	)
	syncer.now = func() time.Time { return now }
	clock := archiveLifecycleClock{now: func() time.Time { return now }}
	service, err := archive.NewService(
		database, syncer.clients, syncer, syncer, nil, clock,
	)
	require.NoError(err)
	require.NoError(service.EnsureConfigured(t.Context(), []platform.RepoRef{ref}))
	_, err = service.Start(t.Context(), []platform.RepoRef{ref})
	require.NoError(err)

	require.NoError(service.RunEligible(t.Context()))
	require.NoError(service.RunEligible(t.Context()))
	require.NoError(service.RunEligible(t.Context()))

	assert.Equal(int32(1), issueCalls.Load())
	assert.Equal(int32(1), issueInventoryCalls.Load())
	assert.GreaterOrEqual(mergeRequestCalls.Load(), int32(1))
	repo, err := database.GetRepoByIdentity(t.Context(), platform.DBRepoIdentity(ref))
	require.NoError(err)
	require.NotNil(repo)
	states, err := database.ListArchiveRepoStates(t.Context(), []int64{repo.ID})
	require.NoError(err)
	require.Len(states, 1)
	assert.True(states[0].IssueInventory.Complete())
	assert.True(states[0].MergeRequestInventory.Complete())
	assert.Equal(db.ArchiveCoverageUnsupported, states[0].IssuesCoverage)
	assert.Equal(db.ArchiveCoverageSupported, states[0].MergeRequestsCoverage)

	now = now.Add(repositoryFeatureProbeInterval)
	tracker.UpdateFromRate(Rate{
		Limit: 5000, Remaining: 4999, Reset: now.Add(time.Minute),
	})
	require.NoError(service.RunEligible(t.Context()))
	assert.Equal(int32(1), issueInventoryCalls.Load(), "unsupported inventory must not be retried")
	assert.Greater(issueCalls.Load(), issueInventoryCalls.Load(),
		"maintenance may continue probing the independently disabled live feature")
}

type disabledArchiveHydrationFixture struct {
	database       *db.DB
	now            time.Time
	ref            platform.RepoRef
	item           *gh.Issue
	disabled       atomic.Bool
	hydrationCalls atomic.Int32
	client         *archivePageMockClient
	tracker        *RateTracker
	syncer         *Syncer
	service        *archive.Service
}

func newDisabledArchiveHydrationFixture(t *testing.T) *disabledArchiveHydrationFixture {
	t.Helper()
	require := require.New(t)
	fixture := &disabledArchiveHydrationFixture{
		database: dbtest.Open(t),
		now:      time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC),
		ref: platform.RepoRef{
			Platform: platform.KindGitHub, Host: "github.com",
			Owner: "acme", Name: "widget", RepoPath: "acme/widget",
		},
	}
	fixture.disabled.Store(true)
	fixture.item = buildOpenIssue(7, fixture.now)
	state := "closed"
	fixture.item.State = &state
	rawDisabled := fmt.Errorf("get archive issue: %w", &gh.ErrorResponse{
		Response: &http.Response{StatusCode: http.StatusGone},
		Message:  "Issues are disabled for this repo",
	})
	fixture.client = &archivePageMockClient{
		mockClient: &mockClient{
			getIssueFn: func(context.Context, string, string, int) (*gh.Issue, error) {
				fixture.hydrationCalls.Add(1)
				if fixture.disabled.Load() {
					return nil, rawDisabled
				}
				return fixture.item, nil
			},
		},
		listInventoryIssuesPageFn: func(
			context.Context, string, string, string, string, string,
		) ([]*gh.Issue, string, bool, error) {
			return []*gh.Issue{fixture.item}, "", true, nil
		},
		listInventoryPullRequestsPageFn: func(
			context.Context, string, string, string, int,
		) ([]*gh.PullRequest, bool, error) {
			return nil, false, nil
		},
	}
	fixture.tracker = NewPlatformRateTracker(
		fixture.database, "github", "github.com", "host", "rest",
	)
	fixture.tracker.UpdateFromRate(Rate{
		Limit: 5000, Remaining: 4999, Reset: fixture.now.Add(time.Minute),
	})
	fixture.syncer = NewSyncer(
		map[string]Client{"github.com": fixture.client}, fixture.database, nil,
		[]RepoRef{{
			Platform: platform.KindGitHub, PlatformHost: "github.com",
			Owner: "acme", Name: "widget",
		}},
		time.Hour,
		map[string]*RateTracker{"github.com": fixture.tracker},
		testBudget(5000),
	)
	fixture.syncer.now = func() time.Time { return fixture.now }
	clock := archiveLifecycleClock{now: func() time.Time { return fixture.now }}
	var err error
	fixture.service, err = archive.NewService(
		fixture.database, fixture.syncer.clients,
		fixture.syncer, fixture.syncer, nil, clock,
	)
	require.NoError(err)
	require.NoError(fixture.service.EnsureConfigured(t.Context(), []platform.RepoRef{fixture.ref}))
	_, err = fixture.service.Start(t.Context(), []platform.RepoRef{fixture.ref})
	require.NoError(err)
	for range 3 {
		require.NoError(fixture.service.RunEligible(t.Context()))
	}
	return fixture
}

func (f *disabledArchiveHydrationFixture) progress(t *testing.T) db.ArchiveDatasetProgress {
	t.Helper()
	require := require.New(t)
	repo, err := f.database.GetRepoByIdentity(t.Context(), platform.DBRepoIdentity(f.ref))
	require.NoError(err)
	require.NotNil(repo)
	progress, err := f.database.GetDatasetProgress(
		t.Context(), repo.ID, db.ArchiveItemTypeIssue, 7, db.ArchiveDatasetLookup,
	)
	require.NoError(err)
	return progress
}

func TestArchiveDisabledIssueHydrationRecoversImmediatelyAfterRestart(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	fixture := newDisabledArchiveHydrationFixture(t)
	progress := fixture.progress(t)
	assert.Equal(db.ArchiveDatasetProgressPending, progress.Status)
	assert.Zero(progress.AttemptCount)
	assert.Nil(progress.NextRetryAt)
	assert.Equal(int32(1), fixture.hydrationCalls.Load())

	fixture.disabled.Store(false)
	fixture.now = fixture.now.Add(time.Minute)
	restartedTracker := NewPlatformRateTracker(
		fixture.database, "github", "github.com", "host", "rest",
	)
	restartedTracker.UpdateFromRate(Rate{
		Limit: 5000, Remaining: 4999, Reset: fixture.now.Add(time.Minute),
	})
	restarted := NewSyncer(
		map[string]Client{"github.com": fixture.client}, fixture.database, nil,
		[]RepoRef{{
			Platform: platform.KindGitHub, PlatformHost: "github.com",
			Owner: "acme", Name: "widget",
		}},
		time.Hour,
		map[string]*RateTracker{"github.com": restartedTracker},
		testBudget(5000),
	)
	restarted.now = func() time.Time { return fixture.now }
	service, err := archive.NewService(
		fixture.database, restarted.clients, restarted, restarted, nil,
		archiveLifecycleClock{now: func() time.Time { return fixture.now }},
	)
	require.NoError(err)
	require.NoError(service.RunEligible(t.Context()))
	assert.Equal(db.ArchiveDatasetProgressComplete, fixture.progress(t).Status)
	assert.Equal(int32(2), fixture.hydrationCalls.Load())
}

func TestArchiveDisabledIssueHydrationRecoversAfterManualProbe(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	fixture := newDisabledArchiveHydrationFixture(t)
	fixture.disabled.Store(false)

	require.NoError(fixture.syncer.SyncRepoOnProvider(
		t.Context(), platform.KindGitHub, "github.com", "acme", "widget",
	))
	require.NoError(fixture.service.RunEligible(t.Context()))
	assert.Equal(db.ArchiveDatasetProgressComplete, fixture.progress(t).Status)
	assert.Equal(int32(2), fixture.hydrationCalls.Load())
}

func (*archiveLifecycleRecorder) RunEligible(context.Context) error { return nil }

func (r *archiveLifecycleRecorder) EnsureConfigured(_ context.Context, refs []platform.RepoRef) error {
	r.ensured = append(r.ensured, refs...)
	return nil
}

func (r *archiveLifecycleRecorder) RetryAuthentication(_ context.Context, refs []platform.RepoRef) error {
	r.retried = append(r.retried, refs...)
	return nil
}

func newBlockingArchiveRunner() *blockingArchiveRunner {
	return &blockingArchiveRunner{started: make(chan struct{}), done: make(chan struct{})}
}

func (r *blockingArchiveRunner) RunEligible(ctx context.Context) error {
	if r.calls.Add(1) == 1 {
		close(r.started)
	}
	<-ctx.Done()
	select {
	case <-r.done:
	default:
		close(r.done)
	}
	return ctx.Err()
}

func TestArchiveWorkerJoinsSyncerShutdown(t *testing.T) {
	database := dbtest.Open(t)
	syncer := NewSyncerWithRegistry(nil, database, nil, nil, time.Hour, nil, nil)
	runner := newBlockingArchiveRunner()
	syncer.SetArchiveService(runner)
	syncer.Start(t.Context())
	select {
	case <-runner.started:
	case <-time.After(time.Second):
		require.Fail(t, "archive worker did not start")
	}

	syncer.Stop()
	select {
	case <-runner.done:
	case <-time.After(time.Second):
		require.Fail(t, "syncer stop returned before archive worker exited")
	}
}

func TestArchiveWorkerAdvancesRealServiceAfterStart(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	database := dbtest.Open(t)
	ref := platform.RepoRef{
		Platform: platform.KindGitHub, Host: "github.test",
		Owner: "acme", Name: "widget", RepoPath: "acme/widget",
	}
	provider := &archiveWorkerProvider{ref: ref}
	registry, err := platform.NewRegistry(provider)
	require.NoError(err)
	syncer := NewSyncerWithRegistry(registry, database, nil, []RepoRef{{
		Platform: ref.Platform, PlatformHost: ref.Host,
		Owner: ref.Owner, Name: ref.Name, RepoPath: ref.RepoPath,
	}}, time.Hour, nil, nil)
	service, err := archive.NewService(database, registry, nil, syncer, nil, nil)
	require.NoError(err)
	service.SetWake(syncer.WakeArchive)
	syncer.SetArchiveService(service)
	syncer.SetArchivePollIntervalForTesting(time.Millisecond)
	require.NoError(service.EnsureConfigured(t.Context(), []platform.RepoRef{ref}))
	_, err = service.Start(t.Context(), []platform.RepoRef{ref})
	require.NoError(err)
	syncer.Start(t.Context())
	t.Cleanup(syncer.Stop)

	repo, err := database.GetRepoByIdentity(t.Context(), platform.DBRepoIdentity(ref))
	require.NoError(err)
	require.NotNil(repo)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		states, stateErr := database.ListArchiveRepoStates(t.Context(), []int64{repo.ID})
		require.NoError(stateErr)
		if len(states) == 1 && states[0].IssueInventory.Complete() {
			assert.Equal(db.ArchiveCollectionModeFull, states[0].CollectionMode)
			var itemCount int
			require.NoError(database.ReadDB().QueryRowContext(t.Context(), `
				SELECT COUNT(*) FROM middleman_archive_items
				WHERE repo_id = ? AND item_type = 'issue'`, repo.ID,
			).Scan(&itemCount))
			assert.Equal(1, itemCount)
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	require.Fail("archive worker did not advance the real archive service")
}

func TestSetReposSeedsArchiveDiscoveryAndRetriesCredentialsBeforeCutover(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	database := dbtest.Open(t)
	syncer := NewSyncerWithRegistry(nil, database, nil, nil, time.Hour, nil, nil)
	recorder := &archiveLifecycleRecorder{}
	syncer.SetArchiveService(recorder)
	repos := []RepoRef{{
		Platform: platform.KindGitLab, PlatformHost: "gitlab.test",
		Owner: "group/subgroup", Name: "project", RepoPath: "group/subgroup/project",
	}}

	require.NoError(syncer.SetReposWithContext(t.Context(), repos, true))
	require.Len(recorder.ensured, 1)
	require.Len(recorder.retried, 1)
	assert.Equal(platform.KindGitLab, recorder.ensured[0].Platform)
	assert.Equal("gitlab.test", recorder.ensured[0].Host)
	assert.Equal("group/subgroup/project", recorder.ensured[0].RepoPath)
	assert.Equal(recorder.ensured, recorder.retried)
	assert.Equal(repos, syncer.TrackedRepos())
}

func TestArchiveAdmissionSharesSyncBudgetAndProviderReserve(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	database := dbtest.Open(t)
	key := RateBucketKey("github", "github.test", "host")
	budget := NewSyncBudget(100)
	tracker := NewPlatformRateTracker(database, "github", "github.test", "host", "rest")
	now := time.Now().UTC()
	reset := now.Add(time.Minute)
	tracker.UpdateFromRate(Rate{Limit: 5000, Remaining: 4999, Reset: reset})
	syncer := NewSyncerWithRegistry(
		nil, database, nil, nil, time.Hour,
		map[string]*RateTracker{key: tracker}, map[string]*SyncBudget{key: budget},
	)
	syncer.now = func() time.Time { return now }
	ref := platform.RepoRef{Platform: platform.KindGitHub, Host: "github.test", Owner: "acme", Name: "widget"}

	allowed, err := syncer.Admit(t.Context(), ref, db.ArchiveItemTypeIssue, 1)
	require.NoError(err)
	require.True(allowed.Allowed)
	assert.True(IsSyncBudgetContext(allowed.Context))
	assert.True(IsArchiveSyncBudgetContext(allowed.Context))

	budget.Spend(100)
	denied, err := syncer.Admit(t.Context(), ref, db.ArchiveItemTypeIssue, 1)
	require.NoError(err)
	assert.False(denied.Allowed)
	require.NotNil(denied.RetryAt)
	assert.Equal(reset, *denied.RetryAt)

	budget.Reset()
	reserveTracker := NewPlatformRateTracker(database, "github", "reserve.test", "host", "rest")
	reserveTracker.UpdateFromRate(Rate{Limit: 5000, Remaining: RateReserveBuffer, Reset: reset})
	reserveRef := platform.RepoRef{
		Platform: platform.KindGitHub, Host: "reserve.test", Owner: "acme", Name: "widget",
	}
	reserveKey := RateBucketKey("github", reserveRef.Host, "host")
	syncer.rateTrackers[reserveKey] = reserveTracker
	syncer.budgets[reserveKey] = NewSyncBudget(100)
	denied, err = syncer.Admit(t.Context(), reserveRef, db.ArchiveItemTypeIssue, 1)
	require.NoError(err)
	assert.False(denied.Allowed)
	assert.Contains(denied.Detail, "reserve")

	syncer.running.Store(true)
	denied, err = syncer.Admit(t.Context(), ref, db.ArchiveItemTypeIssue, 1)
	require.NoError(err)
	assert.False(denied.Allowed)
	assert.Contains(denied.Detail, "normal sync")
}

func TestGitHubArchiveAdmissionUsesCredentialRESTAndGraphQLPools(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	database := dbtest.Open(t)
	now := time.Now().UTC()
	reset := now.Add(time.Minute)
	registry := NewQuotaRegistry()
	client := &credentialRateLimitSnapshotMockClient{mockClient: &mockClient{}}
	appIdentity := IdentityKey{Host: "github.test", Principal: "installation:42"}
	userIdentity := IdentityKey{Host: "github.test", Principal: "user:7"}
	appBucket := RateBucketKey("github", "github.test", "installation:42")
	userBucket := RateBucketKey("github", "github.test", "user:7")
	syncer := NewSyncer(
		map[string]Client{"github.test": client},
		database, nil,
		[]RepoRef{
			{Platform: platform.KindGitHub, PlatformHost: "github.test", Owner: "acme", Name: "widget"},
			{Platform: platform.KindGitHub, PlatformHost: "github.test", Owner: "other", Name: "tool"},
		},
		time.Hour, nil,
		map[string]*SyncBudget{
			appBucket:  NewSyncBudget(100),
			userBucket: NewSyncBudget(100),
		},
	)
	router, err := NewHostRouter(
		"github.test",
		&Route{
			Key: RouteKey{Host: "github.test", Owner: "acme"}, Client: client,
			ReadIdentity: appIdentity, WriteIdentity: appIdentity,
		},
		&Route{
			Key: RouteKey{Host: "github.test", Owner: "other"}, Client: client,
			ReadIdentity: userIdentity, WriteIdentity: userIdentity,
		},
	)
	require.NoError(err)
	syncer.SetGitHubRouters(map[string]*HostRouter{"github.test": router})
	syncer.SetQuotaRegistry(registry)
	syncer.now = func() time.Time { return now }
	appRef := platform.RepoRef{
		Platform: platform.KindGitHub, Host: "github.test", Owner: "acme", Name: "widget",
	}
	userRef := platform.RepoRef{
		Platform: platform.KindGitHub, Host: "github.test", Owner: "other", Name: "tool",
	}
	registry.UpdateSnapshot(appIdentity, QuotaResourceREST,
		Rate{Limit: 15000, Remaining: 14000, Reset: reset})

	unknownGraphQL, err := syncer.Admit(
		t.Context(), appRef, db.ArchiveItemTypeIssue, 1,
	)
	require.NoError(err)
	assert.False(unknownGraphQL.Allowed)
	assert.Contains(unknownGraphQL.Detail, "provider quota unknown")

	registry.UpdateSnapshot(appIdentity, QuotaResourceGraphQL,
		Rate{Limit: 10000, Remaining: RateReserveBuffer, Reset: reset})
	appAtReserve, err := syncer.Admit(
		t.Context(), appRef, db.ArchiveItemTypeIssue, 1,
	)
	require.NoError(err)
	assert.False(appAtReserve.Allowed)
	assert.Contains(appAtReserve.Detail, "provider rate reserve")
	assert.Equal(reset, *appAtReserve.RetryAt)

	// The user credential has its own pools, so the App installation sitting
	// at its reserve must not hold back work routed to the user.
	registry.UpdateSnapshot(userIdentity, QuotaResourceREST,
		Rate{Limit: 5000, Remaining: 4900, Reset: reset})
	registry.UpdateSnapshot(userIdentity, QuotaResourceGraphQL,
		Rate{Limit: 5000, Remaining: 4800, Reset: reset})
	userAllowed, err := syncer.Admit(
		t.Context(), userRef, db.ArchiveItemTypeIssue, 1,
	)
	require.NoError(err)
	require.True(userAllowed.Allowed)
	userAllowed.Complete(nil, false)
}

func TestArchiveAdmissionAttemptAllowanceUsesAvailableSurplus(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	database := dbtest.Open(t)
	key := RateBucketKey("github", "github.test", "host")
	budget := NewSyncBudget(100)
	tracker := NewPlatformRateTracker(database, "github", "github.test", "host", "rest")
	now := time.Now().UTC()
	tracker.UpdateFromRate(Rate{Limit: 5000, Remaining: 4999, Reset: now.Add(time.Minute)})
	syncer := NewSyncerWithRegistry(
		nil, database, nil, nil, time.Hour,
		map[string]*RateTracker{key: tracker}, map[string]*SyncBudget{key: budget},
	)
	syncer.now = func() time.Time { return now }
	ref := platform.RepoRef{Platform: platform.KindGitHub, Host: "github.test", Owner: "acme", Name: "widget"}

	admission, err := syncer.Admit(t.Context(), ref, db.ArchiveItemTypeIssue, 1)
	require.NoError(err)
	require.True(admission.Allowed)
	t.Cleanup(func() { admission.Complete(nil, true) })
	for range PRDetailWorstCase {
		assert.True(ConsumeArchiveAttemptAllowance(admission.Context))
	}
}

func TestGitealikeArchiveAdmissionDoesNotTruncateAdmittedMergeRequest(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	database := dbtest.Open(t)
	budget := NewSyncBudget(50)
	giteaKey := RateBucketKey("gitea", "gitea.test", "host")
	giteaTracker := NewPlatformRateTracker(database, "gitea", "gitea.test", "host", "rest")
	giteaTracker.RecordRequest()
	syncer := NewSyncerWithRegistry(
		nil, database, nil, nil, time.Hour,
		map[string]*RateTracker{giteaKey: giteaTracker},
		map[string]*SyncBudget{giteaKey: budget},
	)
	giteaRef := platform.RepoRef{
		Platform: platform.KindGitea, Host: "gitea.test", Owner: "acme", Name: "widget",
	}

	admission, err := syncer.Admit(
		t.Context(), giteaRef, db.ArchiveItemTypeMergeRequest, 22,
	)
	require.NoError(err)
	require.True(admission.Allowed)
	t.Cleanup(func() { admission.Complete(nil, true) })
	for range 64 {
		assert.True(ConsumeArchiveAttemptAllowance(admission.Context))
	}

	githubKey := RateBucketKey("github", "github.test", "host")
	githubTracker := NewPlatformRateTracker(database, "github", "github.test", "host", "rest")
	githubTracker.RecordRequest()
	githubSyncer := NewSyncerWithRegistry(
		nil, database, nil, nil, time.Hour,
		map[string]*RateTracker{githubKey: githubTracker},
		map[string]*SyncBudget{githubKey: NewSyncBudget(50)},
	)
	githubRef := platform.RepoRef{
		Platform: platform.KindGitHub, Host: "github.test", Owner: "acme", Name: "widget",
	}
	denied, err := githubSyncer.Admit(
		t.Context(), githubRef, db.ArchiveItemTypeMergeRequest, 1,
	)
	require.NoError(err)
	assert.False(denied.Allowed)
}

func TestArchiveAdmissionPreservesProviderReserveForDeclaredCost(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	database := dbtest.Open(t)
	key := RateBucketKey("github", "github.test", "host")
	now := time.Now().UTC()
	reset := now.Add(time.Minute)
	tracker := NewPlatformRateTracker(database, "github", "github.test", "host", "rest")
	// Every wire attempt, including an authentication retry, is counted
	// against admission, so archive.archiveAttemptCost declares twice the
	// logical request count. Remaining sits exactly at the reserve margin
	// for the undoubled logical cost (2): before that doubling, a request
	// declaring cost 2 would have been wrongly allowed here. The doubled
	// retry-headroom cost (4) must be denied to preserve the reserve.
	logicalCost := 2
	retryHeadroomCost := logicalCost * 2
	tracker.UpdateFromRate(Rate{
		Limit: 5000, Remaining: RateReserveBuffer + logicalCost, Reset: reset,
	})
	syncer := NewSyncerWithRegistry(
		nil, database, nil, nil, time.Hour,
		map[string]*RateTracker{key: tracker},
		map[string]*SyncBudget{key: NewSyncBudget(100)},
	)
	syncer.now = func() time.Time { return now }
	ref := platform.RepoRef{Platform: platform.KindGitHub, Host: "github.test", Owner: "acme", Name: "widget"}

	denied, err := syncer.Admit(
		t.Context(), ref, db.ArchiveItemTypeIssue, retryHeadroomCost,
	)
	require.NoError(err)
	assert.False(denied.Allowed)
	assert.Contains(denied.Detail, "reserve")
	assert.Equal(reset, *denied.RetryAt)
}

func TestArchiveRampDenialRetriesWithinCurrentWindow(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	database := dbtest.Open(t)
	key := RateBucketKey("github", "github.test", "host")
	now := time.Now().UTC()
	reset := now.Add(59 * time.Minute)
	tracker := NewPlatformRateTracker(database, "github", "github.test", "host", "rest")
	tracker.UpdateFromRate(Rate{Limit: 5000, Remaining: 4999, Reset: reset})
	syncer := NewSyncerWithRegistry(
		nil, database, nil, nil, time.Hour,
		map[string]*RateTracker{key: tracker},
		map[string]*SyncBudget{key: NewSyncBudget(100)},
	)
	syncer.now = func() time.Time { return now }
	ref := platform.RepoRef{Platform: platform.KindGitHub, Host: "github.test", Owner: "acme", Name: "widget"}

	denied, err := syncer.Admit(t.Context(), ref, db.ArchiveItemTypeIssue, 1)
	require.NoError(err)
	assert.False(denied.Allowed)
	require.NotNil(denied.RetryAt)
	assert.Equal(now.Add(time.Minute), *denied.RetryAt)
	assert.True(denied.RetryAt.Before(reset))
}

func TestArchiveAdmissionDefersToNotificationAndActiveDetailWork(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	database := dbtest.Open(t)
	key := RateBucketKey("github", "github.test", "host")
	now := time.Now().UTC()
	tracker := NewPlatformRateTracker(database, "github", "github.test", "host", "rest")
	tracker.UpdateFromRate(Rate{Limit: 5000, Remaining: 4999, Reset: now.Add(time.Minute)})
	syncer := NewSyncerWithRegistry(
		nil, database, nil, nil, time.Hour,
		map[string]*RateTracker{key: tracker},
		map[string]*SyncBudget{key: NewSyncBudget(100)},
	)
	syncer.now = func() time.Time { return now }
	ref := platform.RepoRef{Platform: platform.KindGitHub, Host: "github.test", Owner: "acme", Name: "widget"}

	for _, priority := range []archive.WorkPriority{
		archive.PriorityNotificationRefresh,
		archive.PriorityActiveDetail,
	} {
		release := syncer.beginProviderWork(key, priority)
		denied, err := syncer.Admit(t.Context(), ref, db.ArchiveItemTypeIssue, 1)
		require.NoError(err)
		assert.False(denied.Allowed)
		assert.Contains(denied.Detail, "higher-priority sync work is active")
		release()
	}

	allowed, err := syncer.Admit(t.Context(), ref, db.ArchiveItemTypeIssue, 1)
	require.NoError(err)
	assert.True(allowed.Allowed)
}

func TestArchiveAdmissionDenialAbandonsExpiredFeatureProbeReservation(t *testing.T) {
	require := require.New(t)
	database := dbtest.Open(t)
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	key := RateBucketKey("github", "github.test", "host")
	syncer := NewSyncerWithRegistry(
		nil, database, nil, nil, time.Hour,
		nil, map[string]*SyncBudget{key: NewSyncBudget(100)},
	)
	syncer.now = func() time.Time { return now }
	ref := platform.RepoRef{
		Platform: platform.KindGitHub, Host: "github.test", Owner: "acme", Name: "widget",
	}
	repo := RepoRef{
		Platform: ref.Platform, PlatformHost: ref.Host, Owner: ref.Owner, Name: ref.Name,
	}
	disabledErr := platform.RepositoryFeatureDisabled(
		platform.KindGitHub, ref.Host, platform.RepositoryFeatureIssues,
		errors.New("repository issues disabled"),
	)
	require.True(syncer.recordRepositoryFeatureDisabled(
		repo, platform.RepositoryFeatureIssues, disabledErr,
	))
	now = now.Add(repositoryFeatureProbeInterval)

	syncer.running.Store(true)
	denied, err := syncer.Admit(t.Context(), ref, db.ArchiveItemTypeIssue, 1)
	require.NoError(err)
	require.False(denied.Allowed)
	syncer.running.Store(false)

	first, due := syncer.beginRepositoryFeatureProbe(
		t.Context(), repo, platform.RepositoryFeatureIssues,
	)
	require.True(due)
	defer first.release()
	_, due = syncer.beginRepositoryFeatureProbe(
		t.Context(), repo, platform.RepositoryFeatureIssues,
	)
	require.False(due)
}

func TestArchiveCompletionWithoutProviderAttemptAbandonsExpiredFeatureProbeReservation(t *testing.T) {
	require := require.New(t)
	database := dbtest.Open(t)
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	key := RateBucketKey("github", "github.test", "host")
	tracker := NewPlatformRateTracker(database, "github", "github.test", "host", "rest")
	tracker.UpdateFromRate(Rate{
		Limit: 5000, Remaining: 4999,
		Reset: now.Add(repositoryFeatureProbeInterval + time.Minute),
	})
	syncer := NewSyncerWithRegistry(
		nil, database, nil, nil, time.Hour,
		map[string]*RateTracker{key: tracker},
		map[string]*SyncBudget{key: NewSyncBudget(100)},
	)
	syncer.now = func() time.Time { return now }
	ref := platform.RepoRef{
		Platform: platform.KindGitHub, Host: "github.test", Owner: "acme", Name: "widget",
	}
	repo := RepoRef{
		Platform: ref.Platform, PlatformHost: ref.Host, Owner: ref.Owner, Name: ref.Name,
	}
	require.True(syncer.recordRepositoryFeatureDisabled(
		repo,
		platform.RepositoryFeatureIssues,
		platform.RepositoryFeatureDisabled(
			platform.KindGitHub, ref.Host, platform.RepositoryFeatureIssues,
			errors.New("repository issues disabled"),
		),
	))
	now = now.Add(repositoryFeatureProbeInterval)

	admission, err := syncer.Admit(t.Context(), ref, db.ArchiveItemTypeIssue, 1)
	require.NoError(err)
	require.True(admission.Allowed, admission.Detail)
	providerAttempted, syncErr := syncer.SyncArchiveItem(
		admission.Context, ref, db.ArchiveItemTypeIssue, 7,
	)
	require.Error(syncErr)
	require.False(providerAttempted)
	require.Nil(admission.Complete(syncErr, providerAttempted))

	first, due := syncer.beginRepositoryFeatureProbe(
		t.Context(), repo, platform.RepositoryFeatureIssues,
	)
	require.True(due)
	defer first.release()
	_, due = syncer.beginRepositoryFeatureProbe(
		t.Context(), repo, platform.RepositoryFeatureIssues,
	)
	require.False(due)
}

func TestArchiveAdmissionLeaseSerializesProviderRequests(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	database := dbtest.Open(t)
	key := RateBucketKey("github", "github.test", "host")
	now := time.Now().UTC()
	tracker := NewPlatformRateTracker(database, "github", "github.test", "host", "rest")
	tracker.UpdateFromRate(Rate{Limit: 5000, Remaining: 4999, Reset: now.Add(time.Minute)})
	syncer := NewSyncerWithRegistry(
		nil, database, nil, nil, time.Hour,
		map[string]*RateTracker{key: tracker},
		map[string]*SyncBudget{key: NewSyncBudget(100)},
	)
	syncer.now = func() time.Time { return now }
	ref := platform.RepoRef{
		Platform: platform.KindGitHub, Host: "github.test", Owner: "acme", Name: "widget",
	}

	first, err := syncer.Admit(t.Context(), ref, db.ArchiveItemTypeIssue, 1)
	require.NoError(err)
	require.True(first.Allowed)
	require.NotNil(first.Complete)

	second, err := syncer.Admit(t.Context(), ref, db.ArchiveItemTypeIssue, 1)
	require.NoError(err)
	assert.False(second.Allowed)
	assert.Contains(second.Detail, "higher-priority sync work is active")

	first.Complete(nil, true)
	first.Complete(nil, true)
	third, err := syncer.Admit(t.Context(), ref, db.ArchiveItemTypeIssue, 1)
	require.NoError(err)
	require.True(third.Allowed)
	require.NotNil(third.Complete)
	third.Complete(nil, true)
}

func TestLiveProviderWorkCancelsAndWaitsForArchiveRequest(t *testing.T) {
	require := require.New(t)
	syncer := NewSyncerWithRegistry(nil, dbtest.Open(t), nil, nil, time.Hour, nil, nil)
	key := RateBucketKey("github", "github.test", "host")
	archiveCtx, releaseArchive, allowed := syncer.tryBeginArchiveProviderRequest(t.Context(), key)
	require.True(allowed)

	liveStarted := make(chan struct{})
	liveDone := make(chan struct{})
	go func() {
		releaseLive := syncer.beginProviderWork(key, archive.PriorityActiveDetail)
		close(liveStarted)
		releaseLive()
		close(liveDone)
	}()

	select {
	case <-archiveCtx.Done():
	case <-time.After(time.Second):
		require.Fail("live work did not cancel archive request")
	}
	select {
	case <-liveStarted:
		require.Fail("live work started before archive lease released")
	case <-time.After(25 * time.Millisecond):
	}

	releaseArchive()
	select {
	case <-liveDone:
	case <-time.After(time.Second):
		require.Fail("live work did not proceed after archive lease released")
	}
}

func TestArchiveAdmissionDefersToForegroundSyncEntryPoints(t *testing.T) {
	for _, tc := range []struct {
		name      string
		operation priorityWorkOperation
		run       func(context.Context, *Syncer, platform.RepoRef) error
	}{
		{
			name: "manual repository refresh", operation: priorityWorkIndex,
			run: func(ctx context.Context, syncer *Syncer, ref platform.RepoRef) error {
				return syncer.SyncRepoOnProvider(ctx, ref.Platform, ref.Host, ref.Owner, ref.Name)
			},
		},
		{
			name: "merge request detail", operation: priorityWorkMR,
			run: func(ctx context.Context, syncer *Syncer, ref platform.RepoRef) error {
				return syncer.SyncMROnProvider(ctx, ref.Platform, ref.Host, ref.Owner, ref.Name, 7)
			},
		},
		{
			name: "issue detail", operation: priorityWorkIssue,
			run: func(ctx context.Context, syncer *Syncer, ref platform.RepoRef) error {
				return syncer.SyncIssueOnProvider(ctx, ref.Platform, ref.Host, ref.Owner, ref.Name, 8)
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert := assert.New(t)
			require := require.New(t)
			database := dbtest.Open(t)
			ref := platform.RepoRef{
				Platform: platform.KindGitLab, Host: "gitlab.test",
				Owner: "acme", Name: "widget", RepoPath: "acme/widget",
			}
			provider := &blockingPriorityProvider{
				syncTestProvider: syncTestProvider{kind: ref.Platform, host: ref.Host},
				ref:              ref, operation: tc.operation,
				started: make(chan struct{}), release: make(chan struct{}),
			}
			registry, err := platform.NewRegistry(provider)
			require.NoError(err)
			key := RateBucketKey(string(ref.Platform), ref.Host, "host")
			syncer := NewSyncerWithRegistry(
				registry, database, nil, []RepoRef{{
					Platform: ref.Platform, PlatformHost: ref.Host,
					Owner: ref.Owner, Name: ref.Name, RepoPath: ref.RepoPath,
				}}, time.Hour, nil, map[string]*SyncBudget{key: NewSyncBudget(10)},
			)
			t.Cleanup(syncer.Stop)

			done := make(chan error, 1)
			go func() { done <- tc.run(t.Context(), syncer, ref) }()
			select {
			case <-provider.started:
			case <-time.After(5 * time.Second):
				require.Fail("foreground provider call did not start")
			}
			admission, err := syncer.Admit(
				t.Context(), ref, db.ArchiveItemTypeIssue, 1,
			)
			require.NoError(err)
			assert.False(admission.Allowed)
			assert.Contains(admission.Detail, "higher-priority sync work is active")
			close(provider.release)
			require.NoError(<-done)
		})
	}
}

func TestSyncRepoRegistersReadAndWriteIdentityProviderWork(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	database := dbtest.Open(t)
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	mc := &mockClient{getRepositoryFn: func(context.Context, string, string) (*gh.Repository, error) {
		once.Do(func() { close(started) })
		<-release
		return nil, errors.New("stop after identity resolution")
	}}
	syncer := NewSyncer(map[string]Client{"github.com": mc}, database, nil, nil, time.Hour, nil, nil)
	t.Cleanup(syncer.Stop)
	router, err := NewHostRouter("github.com", &Route{
		Key:           RouteKey{Host: "github.com", Owner: "acme"},
		Client:        mc,
		ReadIdentity:  IdentityKey{Host: "github.com", Principal: "installation:11"},
		WriteIdentity: IdentityKey{Host: "github.com", Principal: "user:9"},
	})
	require.NoError(err)
	syncer.SetGitHubRouters(map[string]*HostRouter{"github.com": router})
	repo := RepoRef{
		Platform: platform.KindGitHub, PlatformHost: "github.com",
		Owner: "acme", Name: "widget",
	}

	done := make(chan error, 1)
	go func() { done <- syncer.syncRepo(t.Context(), repo) }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		require.Fail("repository sync did not reach the provider")
	}
	readBucket := RateBucketKey("github", "github.com", "installation:11")
	writeBucket := RateBucketKey("github", "github.com", "user:9")
	assert.True(
		syncer.higherPriorityProviderWorkActive(readBucket, archive.PriorityFullArchive),
		"read identity work must preempt archives during repository sync",
	)
	assert.True(
		syncer.higherPriorityProviderWorkActive(writeBucket, archive.PriorityFullArchive),
		"write identity work must preempt archives while the viewer permission overlay can run",
	)
	close(release)
	require.Error(<-done)
	assert.False(syncer.higherPriorityProviderWorkActive(readBucket, archive.PriorityFullArchive))
	assert.False(syncer.higherPriorityProviderWorkActive(writeBucket, archive.PriorityFullArchive))
}

func TestProcessQueuedNotificationReadsHoldsWriteIdentityProviderWork(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	database := dbtest.Open(t)
	repoID, err := database.UpsertRepo(
		t.Context(), db.GitHubRepoIdentity("github.com", "acme", "widget"),
	)
	require.NoError(err)
	now := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
	queuedAt := now.Add(time.Minute)
	number := 7
	require.NoError(database.UpsertNotifications(t.Context(), []db.Notification{{
		Platform:                 "github",
		PlatformHost:             "github.com",
		PlatformNotificationID:   "thread-1",
		RepoID:                   &repoID,
		RepoOwner:                "acme",
		RepoName:                 "widget",
		SubjectType:              "PullRequest",
		SubjectTitle:             "Please review",
		WebURL:                   "https://github.com/acme/widget/pull/7",
		ItemNumber:               &number,
		ItemType:                 "pr",
		Reason:                   "mention",
		SourceUpdatedAt:          now,
		SyncedAt:                 now,
		SourceAckQueuedAt:        &queuedAt,
		SourceLastAcknowledgedAt: &queuedAt,
	}}))
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	mc := &mockClient{
		getNotificationThreadFn: func(context.Context, string) (NotificationThread, error) {
			once.Do(func() { close(started) })
			<-release
			return NotificationThread{}, errors.New("refetch failed")
		},
	}
	syncer := NewSyncer(map[string]Client{"github.com": mc}, database, nil, nil, time.Hour, nil, nil)
	t.Cleanup(syncer.Stop)
	router, err := NewHostRouter("github.com", &Route{
		Key:           RouteKey{Host: "github.com", Owner: "acme"},
		Client:        mc,
		ReadIdentity:  IdentityKey{Host: "github.com", Principal: "installation:11"},
		WriteIdentity: IdentityKey{Host: "github.com", Principal: "user:9"},
	})
	require.NoError(err)
	syncer.SetGitHubRouters(map[string]*HostRouter{"github.com": router})

	done := make(chan error, 1)
	go func() {
		done <- syncer.ProcessQueuedNotificationReads(
			t.Context(), platform.KindGitHub, "github.com", 10,
		)
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		require.Fail("queued read propagation did not reach the provider")
	}
	writeBucket := RateBucketKey("github", "github.com", "user:9")
	assert.True(
		syncer.higherPriorityProviderWorkActive(writeBucket, archive.PriorityFullArchive),
		"queued acknowledgment propagation must preempt archives on the write identity",
	)
	close(release)
	require.NoError(<-done)
	assert.False(syncer.higherPriorityProviderWorkActive(writeBucket, archive.PriorityFullArchive))
}

func TestSyncerConfiguredRepositoriesCarryFullProviderIdentity(t *testing.T) {
	database := dbtest.Open(t)
	syncer := NewSyncerWithRegistry(nil, database, nil, []RepoRef{{
		Platform: platform.KindGitLab, PlatformHost: "gitlab.test",
		Owner: "group/subgroup", Name: "project",
	}}, time.Hour, nil, nil)

	refs, err := syncer.ConfiguredRepositories(t.Context())
	require.NoError(t, err)
	require.Len(t, refs, 1)
	assert.Equal(t, platform.RepoRef{
		Platform: platform.KindGitLab, Host: "gitlab.test",
		Owner: "group/subgroup", Name: "project", RepoPath: "group/subgroup/project",
	}, refs[0])
}
