package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.kenn.io/forge/internal/platformdb"

	gh "github.com/google/go-github/v93/github"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/forge/internal/archive"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/testutil/dbtest"
	"go.kenn.io/forge/internal/testutil/reposeed"
	"go.kenn.io/forge/platform"
)

type blockingArchiveRunner struct {
	calls   atomic.Int64
	started chan struct{}
	done    chan struct{}
}

type signalingArchiveRunner struct {
	*archive.Service
	passed chan struct{}
}

func (r *signalingArchiveRunner) RunPass(ctx context.Context) (bool, error) {
	worked, err := r.Service.RunPass(ctx)
	select {
	case r.passed <- struct{}{}:
	default:
	}
	return worked, err
}

type archiveLifecycleRecorder struct {
	ensured []platform.RepoRef
	retried []platform.RepoRef
	// ensureResult, when set, is returned from EnsureConfigured in place of
	// the full ref list to simulate refs skipped by seeding.
	ensureResult []platform.RepoRef
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
		Ref:           p.ref,
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

// heldArchiveProvider holds its first issue read open until the test releases
// it, so live sync can run while an archive request is in flight. A read whose
// context is canceled fails. Subsequent reads succeed at once.
type heldArchiveProvider struct {
	*archiveWorkerProvider
	readStarted chan struct{}
	release     chan struct{}
	holdOnce    sync.Once
}

func (p *heldArchiveProvider) GetIssue(
	ctx context.Context, ref platform.RepoRef, number int,
) (platform.Issue, error) {
	held := false
	p.holdOnce.Do(func() { held = true })
	if held {
		close(p.readStarted)
		select {
		case <-p.release:
		case <-ctx.Done():
			return platform.Issue{}, ctx.Err()
		}
	}
	return p.archiveWorkerProvider.GetIssue(ctx, ref, number)
}

func TestArchiveHydrationPRShapedIssueBecomesTerminalInSQLite(t *testing.T) {
	testArchiveHydrationMissingGitHubIssueBecomesTerminalInSQLite(
		t,
		http.StatusOK,
		`{"id":11,"node_id":"PR_11","number":11,"repository_url":"https://api.github.com/repos/acme/widget","html_url":"https://github.com/acme/widget/pull/11","title":"actually a pull request","state":"closed","user":{"login":"author"},"pull_request":{"url":"https://api.github.com/repos/acme/widget/pulls/11"},"created_at":"2025-01-01T00:00:00Z","updated_at":"2025-01-02T00:00:00Z","closed_at":"2025-01-02T00:00:00Z"}`,
		"terminal archive lookup must not hydrate the PR-shaped issue again",
	)
}

func TestArchiveHydrationDeletedGitHubIssueBecomesTerminalInSQLite(t *testing.T) {
	testArchiveHydrationMissingGitHubIssueBecomesTerminalInSQLite(
		t,
		http.StatusGone,
		`{"message":"This issue was deleted"}`,
		"terminal archive lookup must not hydrate the deleted issue again",
	)
}

func testArchiveHydrationMissingGitHubIssueBecomesTerminalInSQLite(
	t *testing.T,
	hydrationCode int,
	hydrationBody string,
	retryAssertion string,
) {
	t.Helper()
	assert := assert.New(t)
	require := require.New(t)
	database := dbtest.Open(t)
	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	ref := platform.RepoRef{
		Platform: platform.KindGitHub, Host: "github.com",
		Owner: "acme", Name: "widget", RepoPath: "acme/widget",
		Key: platform.RepositoryIDKey(1),
	}
	var hydrationCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/graphql":
			var request struct {
				Query string `json:"query"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				http.Error(w, `{"message":"invalid GraphQL request"}`, http.StatusBadRequest)
				return
			}
			switch {
			case strings.Contains(request.Query, "issues(first: 100"):
				_, _ = w.Write([]byte(`{"data":{"repository":{"issues":{"nodes":[{"id":"I_11","databaseId":11,"number":11,"title":"historical issue identity","state":"CLOSED","body":"","url":"https://github.com/acme/widget/issues/11","author":{"login":"author"},"createdAt":"2025-01-01T00:00:00Z","updatedAt":"2025-01-02T00:00:00Z","closedAt":"2025-01-02T00:00:00Z","comments":{"totalCount":0},"labels":{"nodes":[]},"assignees":{"nodes":[]}}],"pageInfo":{"hasNextPage":false,"endCursor":null}}}}}`))
			case strings.Contains(request.Query, "pullRequests(first: 100"):
				_, _ = w.Write([]byte(`{"data":{"repository":{"pullRequests":{"nodes":[],"pageInfo":{"hasNextPage":false,"endCursor":null}}}}}`))
			default:
				http.Error(w, `{"message":"unexpected GraphQL query"}`, http.StatusBadRequest)
			}
		case "/api/v3/repos/acme/widget", "/api/v3/repositories/1":
			_, _ = w.Write([]byte(`{"id":1,"name":"widget","full_name":"acme/widget","owner":{"login":"acme"}}`))
		case "/api/v3/repos/acme/widget/pulls":
			_, _ = w.Write([]byte(`[]`))
		case "/api/v3/repos/acme/widget/issues/11":
			hydrationCalls.Add(1)
			w.WriteHeader(hydrationCode)
			_, _ = w.Write([]byte(hydrationBody))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	provider := newArchiveTestGitHubProvider(t, server.URL)
	registry, err := platform.NewRegistry(provider)
	require.NoError(err)
	syncer := NewSyncerWithRegistry(
		registry, database, nil,
		[]RepoRef{{
			Platform: ref.Platform, PlatformHost: ref.Host,
			Owner: ref.Owner, Name: ref.Name, RepoPath: ref.RepoPath,
			Key: ref.Key,
		}},
		time.Hour, nil, nil,
	)
	syncer.now = func() time.Time { return now }
	service, err := archive.NewService(
		database, registry, nil, syncer, nil,
		archiveLifecycleClock{now: func() time.Time { return now }},
	)
	require.NoError(err)
	requireEnsureConfigured(t, service, []platform.RepoRef{ref})
	_, err = service.Start(t.Context(), []platform.RepoRef{ref})
	require.NoError(err)

	require.NoError(service.RunEligible(t.Context())) // issue inventory
	require.NoError(service.RunEligible(t.Context())) // pull-request inventory
	require.NoError(service.RunEligible(t.Context())) // issue hydration

	repo, err := database.GetRepoByIdentity(t.Context(), platformdb.DBRepoIdentity(ref))
	require.NoError(err)
	require.NotNil(repo)
	progress, err := database.GetDatasetProgress(
		t.Context(), repo.ID, db.ArchiveItemTypeIssue, 11, db.ArchiveDatasetLookup,
	)
	require.NoError(err)
	assert.Equal(db.ArchiveDatasetProgressTerminal, progress.Status)
	assert.Zero(progress.AttemptCount)
	assert.Nil(progress.NextRetryAt)
	require.NotNil(progress.LastErrorCode)
	assert.Equal(string(platform.ErrCodeNotFound), *progress.LastErrorCode)
	assert.NotNil(progress.CompletedAt)

	var lifecycle db.ArchiveLifecycleState
	require.NoError(database.ReadDB().QueryRowContext(t.Context(), `
		SELECT lifecycle_state FROM forge_archive_items
		WHERE repo_id = ? AND item_type = ? AND item_number = ?`,
		repo.ID, db.ArchiveItemTypeIssue, 11,
	).Scan(&lifecycle))
	assert.Equal(db.ArchiveLifecycleStateRemovedUpstream, lifecycle)
	assert.Equal(int32(1), hydrationCalls.Load())

	require.NoError(service.RunEligible(t.Context()))
	assert.Equal(int32(1), hydrationCalls.Load(), retryAssertion)
}

func TestArchiveHydrationKeepsIncompleteMergedGitHubPRFailed(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	database := dbtest.Open(t)
	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	ref := platform.RepoRef{
		Platform: platform.KindGitHub, Host: "github.com",
		Owner: "acme", Name: "widget", RepoPath: "acme/widget",
	}
	mergedAt := now.Add(-time.Hour)
	item := buildOpenPR(7, mergedAt)
	item.State = new("closed")
	item.Merged = new(true)
	item.MergedAt = makeTimestamp(mergedAt)
	item.ClosedAt = makeTimestamp(mergedAt)
	item.MergeCommitSHA = new("merge-sha")
	item.ChangedFiles = nil
	client := &archivePageMockClient{
		mockClient: &mockClient{singlePR: item},
		listInventoryIssuesPageFn: func(
			context.Context, string, string, string, string, string,
		) ([]*gh.Issue, string, bool, error) {
			return nil, "", true, nil
		},
		listInventoryPullRequestsPageFn: func(
			context.Context, string, string, string, int,
		) ([]*gh.PullRequest, bool, error) {
			return []*gh.PullRequest{item}, false, nil
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
			Owner: ref.Owner, Name: ref.Name,
		}},
		time.Hour, map[string]*RateTracker{"github.com": tracker}, testBudget(5000),
	)
	syncer.now = func() time.Time { return now }
	service, err := archive.NewService(
		database, syncer.clients, syncer, syncer, nil,
		archiveLifecycleClock{now: func() time.Time { return now }},
	)
	require.NoError(err)
	requireEnsureConfigured(t, service, []platform.RepoRef{ref})
	_, err = service.Start(t.Context(), []platform.RepoRef{ref})
	require.NoError(err)

	for range 2 {
		require.NoError(service.RunEligible(t.Context()))
	}
	require.ErrorContains(service.RunEligible(t.Context()), "files_changed")

	repo, err := database.GetRepoByIdentity(t.Context(), platformdb.DBRepoIdentity(ref))
	require.NoError(err)
	require.NotNil(repo)
	progress, err := database.GetDatasetProgress(
		t.Context(), repo.ID, db.ArchiveItemTypeMergeRequest, 7,
		db.ArchiveDatasetLookup,
	)
	require.NoError(err)
	assert.Equal(db.ArchiveDatasetProgressFailed, progress.Status)
	assert.Equal(1, progress.AttemptCount)
	assert.Nil(progress.CompletedAt)
	require.NotNil(progress.LastErrorDetail)
	assert.Contains(*progress.LastErrorDetail, "files_changed")
}

// A sync pass that notices an item closed records the closure and saves no
// comments or events. When that write lands while the archive is reading the
// same item, the archive's history write is refused. The item must stay
// pending until a later read saves the history.
func TestArchiveItemStaysPendingWhenALiveCloseUpdateSupersedesItsHistory(t *testing.T) {
	closedAt := time.Date(2026, 8, 4, 11, 0, 0, 0, time.UTC)
	const closingBody = "fixed in the last release"
	closing := &gh.IssueComment{
		ID: new(int64(9001)), Body: new(closingBody),
		HTMLURL:   new("https://github.com/acme/widget/issues/7#issuecomment-9001"),
		User:      &gh.User{Login: new("maintainer")},
		CreatedAt: makeTimestamp(closedAt), UpdatedAt: makeTimestamp(closedAt),
	}
	issue := buildOpenIssue(7, closedAt)
	issue.State = new("closed")
	issue.ClosedAt = makeTimestamp(closedAt)
	pr := buildOpenPR(7, closedAt)
	pr.State = new("closed")
	pr.ClosedAt = makeTimestamp(closedAt)

	tests := []struct {
		name            string
		itemType        db.ArchiveItemType
		client          *mockClient
		issues          []*gh.Issue
		pullRequests    []*gh.PullRequest
		liveCloseUpdate func(context.Context, *Syncer, RepoRef, int64) error
		storedBodies    func(context.Context, *db.DB, int64) ([]string, error)
	}{
		{
			name:     "issue",
			itemType: db.ArchiveItemTypeIssue,
			client: &mockClient{getIssueFn: func(context.Context, string, string, int) (*gh.Issue, error) {
				return issue, nil
			}},
			issues: []*gh.Issue{issue},
			liveCloseUpdate: func(ctx context.Context, s *Syncer, repo RepoRef, repoID int64) error {
				return s.fetchAndUpdateClosedIssue(ctx, repo, repoID, 7)
			},
			storedBodies: func(ctx context.Context, database *db.DB, repoID int64) ([]string, error) {
				stored, err := database.GetIssueByRepoIDAndNumber(ctx, repoID, 7)
				if err != nil || stored == nil {
					return nil, err
				}
				events, err := database.ListIssueEvents(ctx, stored.ID)
				bodies := make([]string, 0, len(events))
				for _, event := range events {
					bodies = append(bodies, event.Body)
				}
				return bodies, err
			},
		},
		{
			name:         "merge request closed without merging",
			itemType:     db.ArchiveItemTypeMergeRequest,
			client:       &mockClient{singlePR: pr},
			pullRequests: []*gh.PullRequest{pr},
			liveCloseUpdate: func(ctx context.Context, s *Syncer, repo RepoRef, repoID int64) error {
				return s.fetchAndUpdateClosed(ctx, repo, repoID, 7, false)
			},
			storedBodies: func(ctx context.Context, database *db.DB, repoID int64) ([]string, error) {
				stored, err := database.GetMergeRequestByRepoIDAndNumber(ctx, repoID, 7)
				if err != nil || stored == nil {
					return nil, err
				}
				events, err := database.ListMREvents(ctx, stored.ID)
				bodies := make([]string, 0, len(events))
				for _, event := range events {
					bodies = append(bodies, event.Body)
				}
				return bodies, err
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert := assert.New(t)
			require := require.New(t)
			database := dbtest.Open(t)
			now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
			ref := platform.RepoRef{
				Platform: platform.KindGitHub, Host: "github.com",
				Owner: "acme", Name: "widget", RepoPath: "acme/widget",
			}
			tracked := RepoRef{
				Platform: platform.KindGitHub, PlatformHost: "github.com",
				Owner: ref.Owner, Name: ref.Name,
			}
			client := &archivePageMockClient{
				mockClient: tt.client,
				listInventoryIssuesPageFn: func(
					context.Context, string, string, string, string, string,
				) ([]*gh.Issue, string, bool, error) {
					return tt.issues, "", true, nil
				},
				listInventoryPullRequestsPageFn: func(
					context.Context, string, string, string, int,
				) ([]*gh.PullRequest, bool, error) {
					return tt.pullRequests, false, nil
				},
			}
			tracker := NewPlatformRateTracker(database, "github", "github.com", "host", "rest")
			tracker.UpdateFromRate(Rate{
				Limit: 5000, Remaining: 4999, Reset: now.Add(30 * time.Minute),
			})
			syncer := NewSyncer(
				map[string]Client{"github.com": client}, database, nil,
				[]RepoRef{tracked}, time.Hour,
				map[string]*RateTracker{"github.com": tracker}, testBudget(5000),
			)
			syncer.now = func() time.Time { return now }
			service, err := archive.NewService(
				database, syncer.clients, syncer, syncer, nil,
				archiveLifecycleClock{now: func() time.Time { return now }},
			)
			require.NoError(err)
			requireEnsureConfigured(t, service, []platform.RepoRef{ref})
			_, err = service.Start(t.Context(), []platform.RepoRef{ref})
			require.NoError(err)
			for range 2 {
				require.NoError(service.RunEligible(t.Context())) // inventories
			}
			repo, err := database.GetRepoByIdentity(t.Context(), platformdb.DBRepoIdentity(ref))
			require.NoError(err)
			require.NotNil(repo)

			// The archive lists comments after it has saved the item itself, so a
			// write made here lands between the two.
			liveCloseUpdates := 0
			tt.client.listIssueCommentsFn = func(
				context.Context, string, string, int,
			) ([]*gh.IssueComment, error) {
				if liveCloseUpdates == 0 {
					liveCloseUpdates++
					if err := tt.liveCloseUpdate(t.Context(), syncer, tracked, repo.ID); err != nil {
						return nil, err
					}
				}
				return []*gh.IssueComment{closing}, nil
			}

			require.Error(service.RunEligible(t.Context()))
			require.Equal(1, liveCloseUpdates)
			progress, err := database.GetDatasetProgress(
				t.Context(), repo.ID, tt.itemType, 7, db.ArchiveDatasetLookup,
			)
			require.NoError(err)
			bodies, err := tt.storedBodies(t.Context(), database, repo.ID)
			require.NoError(err)
			assert.NotContains(bodies, closingBody)
			assert.NotEqual(db.ArchiveDatasetProgressComplete, progress.Status)
			assert.Nil(progress.CompletedAt)
			require.NotNil(progress.NextRetryAt)

			now = *progress.NextRetryAt
			require.NoError(service.RunEligible(t.Context()))
			progress, err = database.GetDatasetProgress(
				t.Context(), repo.ID, tt.itemType, 7, db.ArchiveDatasetLookup,
			)
			require.NoError(err)
			bodies, err = tt.storedBodies(t.Context(), database, repo.ID)
			require.NoError(err)
			assert.Contains(bodies, closingBody)
			assert.Equal(db.ArchiveDatasetProgressComplete, progress.Status)
		})
	}
}

func TestArchiveItemCompletesWhileLiveSyncUsesTheSameCredential(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	database := dbtest.Open(t)
	ref := platform.RepoRef{
		Platform: platform.KindGitHub, Host: "github.test",
		Owner: "acme", Name: "widget", RepoPath: "acme/widget",
		Key: platform.RepositoryIDKey(1001),
	}
	_, err := reposeed.Seed(t.Context(), database, platformdb.DBRepoIdentity(ref))
	require.NoError(err)
	provider := &heldArchiveProvider{
		archiveWorkerProvider: &archiveWorkerProvider{ref: ref},
		readStarted:           make(chan struct{}),
		release:               make(chan struct{}),
	}
	registry, err := platform.NewRegistry(provider)
	require.NoError(err)

	now := time.Now().UTC()
	key := RateBucketKey("github", ref.Host, "host")
	tracker := NewPlatformRateTracker(database, "github", ref.Host, "host", "rest")
	tracker.UpdateFromRate(Rate{Limit: 5000, Remaining: 4999, Reset: now.Add(time.Minute)})
	budget := NewSyncBudget(5000)
	tracked := RepoRef{
		Platform: ref.Platform, PlatformHost: ref.Host,
		Owner: ref.Owner, Name: ref.Name, RepoPath: ref.RepoPath,
		Key: ref.Key,
	}
	syncer := NewSyncerWithRegistry(
		registry, database, nil, []RepoRef{tracked}, time.Hour,
		map[string]*RateTracker{key: tracker}, map[string]*SyncBudget{key: budget},
	)
	syncer.now = func() time.Time { return now }

	service, err := archive.NewService(database, registry, syncer, syncer, nil, nil)
	require.NoError(err)
	requireEnsureConfigured(t, service, []platform.RepoRef{ref})
	_, err = service.Start(t.Context(), []platform.RepoRef{ref})
	require.NoError(err)

	require.NoError(service.RunEligible(t.Context())) // issue inventory
	require.NoError(service.RunEligible(t.Context())) // merge-request inventory

	repo, err := database.GetRepoByIdentity(t.Context(), platformdb.DBRepoIdentity(ref))
	require.NoError(err)
	require.NotNil(repo)

	// A sync pass that overruns its interval is followed at once by the next
	// one, so a busy instance always has a pass in flight. The archive must be
	// admitted anyway.
	syncer.running.Store(true)
	hydrationDone := make(chan error, 1)
	go func() { hydrationDone <- service.RunEligible(t.Context()) }()
	select {
	case <-provider.readStarted:
	case <-time.After(2 * time.Second):
		require.Fail("archive item sync did not start while a sync pass was running")
	}

	// Live sync of the same item on the same credential runs to completion
	// beside the archive request instead of canceling it.
	providerAttempted := false
	require.NoError(syncer.syncIssueForRepo(t.Context(), tracked, 1, &providerAttempted))
	require.True(providerAttempted)
	select {
	case err := <-hydrationDone:
		require.Failf("archive request ended early", "live sync must not end the archive request: %v", err)
	default:
	}

	close(provider.release)
	require.NoError(<-hydrationDone)

	lookup, err := database.GetDatasetProgress(
		t.Context(), repo.ID, db.ArchiveItemTypeIssue, 1, db.ArchiveDatasetLookup,
	)
	require.NoError(err)
	assert.Equal(db.ArchiveDatasetProgressComplete, lookup.Status)
	assert.NotNil(lookup.CompletedAt)

	var lastErrorCode *string
	require.NoError(database.ReadDB().QueryRowContext(t.Context(), `
		SELECT last_error_code FROM forge_archive_repos WHERE repo_id = ?`, repo.ID,
	).Scan(&lastErrorCode))
	assert.Nil(lastErrorCode)
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
	requireEnsureConfigured(t, service, []platform.RepoRef{ref})
	_, err = service.Start(t.Context(), []platform.RepoRef{ref})
	require.NoError(err)

	require.NoError(service.RunEligible(t.Context()))
	require.NoError(service.RunEligible(t.Context()))
	require.NoError(service.RunEligible(t.Context()))

	assert.Equal(int32(1), issueInventoryCalls.Load())
	assert.Greater(issueCalls.Load(), issueInventoryCalls.Load(),
		"maintenance must verify the provider stream despite the inventory cooldown")
	assert.GreaterOrEqual(mergeRequestCalls.Load(), int32(1))
	repo, err := database.GetRepoByIdentity(t.Context(), platformdb.DBRepoIdentity(ref))
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
	requireEnsureConfigured(t, fixture.service, []platform.RepoRef{fixture.ref})
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
	repo, err := f.database.GetRepoByIdentity(t.Context(), platformdb.DBRepoIdentity(f.ref))
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

func (*archiveLifecycleRecorder) RunPass(context.Context) (bool, error) { return false, nil }

func (r *archiveLifecycleRecorder) EnsureConfigured(_ context.Context, refs []platform.RepoRef) ([]platform.RepoRef, error) {
	r.ensured = append(r.ensured, refs...)
	if r.ensureResult != nil {
		return r.ensureResult, nil
	}
	return refs, nil
}

func (r *archiveLifecycleRecorder) RetryAuthentication(_ context.Context, refs []platform.RepoRef) error {
	r.retried = append(r.retried, refs...)
	return nil
}

func newBlockingArchiveRunner() *blockingArchiveRunner {
	return &blockingArchiveRunner{started: make(chan struct{}), done: make(chan struct{})}
}

func (r *blockingArchiveRunner) RunPass(ctx context.Context) (bool, error) {
	if r.calls.Add(1) == 1 {
		close(r.started)
	}
	<-ctx.Done()
	select {
	case <-r.done:
	default:
		close(r.done)
	}
	return false, ctx.Err()
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
		Key: platform.RepositoryIDKey(1001),
	}
	_, err := reposeed.Seed(t.Context(), database, platformdb.DBRepoIdentity(ref))
	require.NoError(err)
	provider := &archiveWorkerProvider{ref: ref}
	registry, err := platform.NewRegistry(provider)
	require.NoError(err)
	syncer := NewSyncerWithRegistry(registry, database, nil, []RepoRef{{
		Platform: ref.Platform, PlatformHost: ref.Host,
		Owner: ref.Owner, Name: ref.Name, RepoPath: ref.RepoPath,
		Key: ref.Key,
	}}, time.Hour, nil, nil)
	service, err := archive.NewService(database, registry, nil, syncer, nil, nil)
	require.NoError(err)
	service.SetWake(syncer.WakeArchive)
	passes := make(chan struct{}, 1)
	syncer.SetArchiveService(&signalingArchiveRunner{Service: service, passed: passes})
	syncer.SetArchivePollIntervalForTesting(time.Millisecond)
	requireEnsureConfigured(t, service, []platform.RepoRef{ref})
	_, err = service.Start(t.Context(), []platform.RepoRef{ref})
	require.NoError(err)
	syncer.Start(t.Context())
	t.Cleanup(syncer.Stop)

	repo, err := database.GetRepoByIdentity(t.Context(), platformdb.DBRepoIdentity(ref))
	require.NoError(err)
	require.NotNil(repo)
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case <-passes:
		case <-deadline.C:
			require.FailNow("archive worker did not advance the real archive service")
		}
		states, err := database.ListArchiveRepoStates(t.Context(), []int64{repo.ID})
		require.NoError(err)
		if len(states) == 1 && states[0].IssueInventory.Complete() {
			assert.Equal(db.ArchiveCollectionModeFull, states[0].CollectionMode)
			break
		}
	}
	var itemCount int
	require.NoError(database.ReadDB().QueryRowContext(t.Context(), `
		SELECT COUNT(*) FROM forge_archive_items
		WHERE repo_id = ? AND item_type = 'issue'`, repo.ID,
	).Scan(&itemCount))
	assert.Equal(1, itemCount)
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

func TestSetReposSeedsActiveArchiveForArchivedRepo(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	database := dbtest.Open(t)
	registry, err := platform.NewRegistry(syncTestProvider{
		kind: platform.KindGitHub, host: "github.test",
	})
	require.NoError(err)
	syncer := NewSyncerWithRegistry(registry, database, nil, nil, time.Hour, nil, nil)
	service, err := archive.NewService(database, registry, nil, syncer, nil, nil)
	require.NoError(err)
	syncer.SetArchiveService(service)
	ref := RepoRef{
		Platform: platform.KindGitHub, PlatformHost: "github.test",
		Owner: "acme", Name: "frozen", RepoPath: "acme/frozen",
		Key: platform.RepositoryIDKey(1003), Archived: true,
	}
	_, err = reposeed.Seed(
		t.Context(), database, platformdb.DBRepoIdentity(platformRepoRef(ref)),
	)
	require.NoError(err)

	require.NoError(syncer.SetReposWithContext(t.Context(), []RepoRef{ref}, false))

	repo, err := database.GetRepoByIdentity(
		t.Context(), platformdb.DBRepoIdentity(platformRepoRef(ref)),
	)
	require.NoError(err)
	require.NotNil(repo)
	states, err := database.ListArchiveRepoStates(t.Context(), []int64{repo.ID})
	require.NoError(err)
	require.Len(states, 1)
	assert.Equal(db.ArchiveOperatorStateActive, states[0].OperatorState,
		"an archived configured repo keeps an active archive")
}

func TestSetReposPassesOnlySeededRefsToRetryAuthentication(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	database := dbtest.Open(t)
	syncer := NewSyncerWithRegistry(nil, database, nil, nil, time.Hour, nil, nil)
	seeded := platform.RepoRef{
		Platform: platform.KindGitHub, Host: "github.com",
		Owner: "acme", Name: "good", RepoPath: "acme/good",
	}
	recorder := &archiveLifecycleRecorder{
		ensureResult: []platform.RepoRef{seeded},
	}
	syncer.SetArchiveService(recorder)
	repos := []RepoRef{
		{Owner: "acme", Name: "good", PlatformHost: "github.com"},
		{Owner: "acme", Name: "ghost", PlatformHost: "github.com"},
	}

	require.NoError(syncer.SetReposWithContext(t.Context(), repos, true))
	require.Len(recorder.ensured, 2)
	assert.Equal([]platform.RepoRef{seeded}, recorder.retried,
		"refs skipped by seeding must not reach authentication retry")
}

func TestSyncRepoReplacementReconcilesArchiveLifecycle(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	database := dbtest.Open(t)
	configured := RepoRef{
		Platform: platform.KindGitLab, PlatformHost: "gitlab.test",
		Owner: "group", Name: "project", RepoPath: "group/project",
		Key: platform.RepositoryIDKey(1001),
	}
	provider := &syncTestRepositoryReadProvider{
		syncTestReadProvider: &syncTestReadProvider{
			kind: platform.KindGitLab, host: "gitlab.test",
		},
		repository: platform.Repository{
			Ref: platform.RepoRef{
				Platform: platform.KindGitLab, Host: "gitlab.test",
				Owner: "group", Name: "project", RepoPath: "group/project",
				Key: platform.RepositoryIDKey(1001),
			},
		},
	}
	registry, err := platform.NewRegistry(provider)
	require.NoError(err)
	syncer := NewSyncerWithRegistry(
		registry, database, nil, []RepoRef{configured}, time.Hour, nil, nil,
	)
	service, err := archive.NewService(database, registry, nil, syncer, nil, nil)
	require.NoError(err)
	syncer.SetArchiveService(service)
	_, err = service.EnsureConfigured(
		ctx, []platform.RepoRef{platformRepoRef(configured)},
	)
	require.NoError(err)
	oldEntry, err := database.GetRepositoryByProviderID(
		ctx, platform.RepositoryIdentity{Provider: "gitlab", PlatformHost: "gitlab.test", Key: platform.RepositoryIDKey(1001)},
	)
	require.NoError(err)
	require.NotNil(oldEntry)

	provider.repository.Ref.Key = platform.RepositoryIDKey(1002)
	require.NoError(syncer.syncRepo(ctx, configured))

	newEntry, err := database.GetRepositoryByProviderID(
		ctx, platform.RepositoryIdentity{Provider: "gitlab", PlatformHost: "gitlab.test", Key: platform.RepositoryIDKey(1002)},
	)
	require.NoError(err)
	require.NotNil(newEntry)
	states, err := database.ListArchiveRepoStates(ctx, nil)
	require.NoError(err)
	require.Len(states, 2)
	stateByRepoID := map[int64]db.ArchiveRepoState{}
	for _, state := range states {
		stateByRepoID[state.RepoID] = state
	}
	assert.Equal(
		db.ArchiveOperatorStatePaused,
		stateByRepoID[oldEntry.Repository.ID].OperatorState,
	)
	require.NotNil(stateByRepoID[oldEntry.Repository.ID].LastErrorCode)
	assert.Equal(
		string(db.ArchiveErrorCodeConfigurationRemoved),
		*stateByRepoID[oldEntry.Repository.ID].LastErrorCode,
	)
	assert.Equal(
		db.ArchiveOperatorStateActive,
		stateByRepoID[newEntry.Repository.ID].OperatorState,
	)
}

func TestSyncStaleRouteSnapshotFollowsRepositoryIDKeepsBothReposTracked(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	database := dbtest.Open(t)
	// The renamed repository moved to a new route; a different repository
	// reused its old route. Both are tracked. A sync of the old route whose
	// snapshot still carries the renamed repository's id resolves that id
	// to its current route — neither repository may be lost or duplicated.
	renamed := RepoRef{
		Platform: platform.KindGitLab, PlatformHost: "gitlab.test",
		Owner: "group", Name: "project-moved", RepoPath: "group/project-moved",
		Key: platform.RepositoryIDKey(1001),
	}
	successor := RepoRef{
		Platform: platform.KindGitLab, PlatformHost: "gitlab.test",
		Owner: "group", Name: "project", RepoPath: "group/project",
		Key: platform.RepositoryIDKey(1002),
	}
	providerRepos := map[platform.RepositoryKey]RepoRef{
		platform.RepositoryIDKey(1001): renamed, platform.RepositoryIDKey(1002): successor,
	}
	provider := &syncTestRepositoryReadProvider{
		syncTestReadProvider: &syncTestReadProvider{
			kind: platform.KindGitLab, host: "gitlab.test",
		},
		getRepositoryFn: func(_ context.Context, ref platform.RepoRef) (platform.Repository, error) {
			repo, ok := providerRepos[ref.Key]
			if !ok {
				return platform.Repository{}, platform.ErrNotFound
			}
			return platform.Repository{Ref: platformRepoRef(repo)}, nil
		},
	}
	registry, err := platform.NewRegistry(provider)
	require.NoError(err)
	syncer := NewSyncerWithRegistry(
		registry, database, nil, []RepoRef{renamed, successor}, time.Hour, nil, nil,
	)
	service, err := archive.NewService(database, registry, nil, syncer, nil, nil)
	require.NoError(err)
	syncer.SetArchiveService(service)
	_, err = service.EnsureConfigured(ctx, []platform.RepoRef{
		platformRepoRef(renamed), platformRepoRef(successor),
	})
	require.NoError(err)

	stale := successor
	stale.Key = renamed.Key
	require.NoError(syncer.syncRepo(ctx, stale))

	tracked := syncer.TrackedRepos()
	require.Len(tracked, 2, "neither repository may be lost or duplicated")
	byID := map[platform.RepositoryKey]RepoRef{}
	for _, repo := range tracked {
		byID[repo.Key] = repo
	}
	assert.Equal("project-moved", byID[platform.RepositoryIDKey(1001)].Name,
		"the renamed repository keeps its tracked entry")
	assert.Equal("project", byID[platform.RepositoryIDKey(1002)].Name)

	oldEntry, err := database.GetRepositoryByProviderID(
		ctx, platform.RepositoryIdentity{Provider: "gitlab", PlatformHost: "gitlab.test", Key: platform.RepositoryIDKey(1001)},
	)
	require.NoError(err)
	require.NotNil(oldEntry)
	newEntry, err := database.GetRepositoryByProviderID(
		ctx, platform.RepositoryIdentity{Provider: "gitlab", PlatformHost: "gitlab.test", Key: platform.RepositoryIDKey(1002)},
	)
	require.NoError(err)
	require.NotNil(newEntry)
	assert.NotEqual(oldEntry.Repository.ID, newEntry.Repository.ID)
}

func TestSyncRouteReplacementIgnoresDisplacedArchivedFlipE2E(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	database := dbtest.Open(t)
	// The tracked occupant of the configured route was archived after this
	// sync pass snapshotted it, and the route meanwhile resolves to an
	// untracked replacement repository. The displaced repo's archived flip
	// must not stamp the replacement, which would silently exclude it from
	// live sync.
	displaced := RepoRef{
		Platform: platform.KindGitLab, PlatformHost: "gitlab.test",
		Owner: "group", Name: "project", RepoPath: "group/project",
		Key: platform.RepositoryIDKey(1001), Archived: true,
	}
	provider := &syncTestRepositoryReadProvider{
		syncTestReadProvider: &syncTestReadProvider{
			kind: platform.KindGitLab, host: "gitlab.test",
		},
		repository: platform.Repository{
			Ref: platform.RepoRef{
				Platform: platform.KindGitLab, Host: "gitlab.test",
				Owner: "group", Name: "project", RepoPath: "group/project",
				Key: platform.RepositoryIDKey(1002),
			},
			Archived: false,
		},
	}
	registry, err := platform.NewRegistry(provider)
	require.NoError(err)
	syncer := NewSyncerWithRegistry(
		registry, database, nil, []RepoRef{displaced}, time.Hour, nil, nil,
	)
	service, err := archive.NewService(database, registry, nil, syncer, nil, nil)
	require.NoError(err)
	syncer.SetArchiveService(service)
	_, err = service.EnsureConfigured(
		ctx, []platform.RepoRef{platformRepoRef(displaced)},
	)
	require.NoError(err)

	snapshot := displaced
	snapshot.Archived = false
	require.NoError(syncer.syncRepo(ctx, snapshot))

	tracked := syncer.TrackedRepos()
	require.Len(tracked, 1)
	assert.Equal(platform.RepositoryIDKey(1002), tracked[0].Key)
	assert.False(tracked[0].Archived,
		"the replacement keeps its authoritative unarchived state")
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
	syncer.hosts().rateTrackers[reserveKey] = reserveTracker
	syncer.hosts().budgets[reserveKey] = NewSyncBudget(100)
	denied, err = syncer.Admit(t.Context(), reserveRef, db.ArchiveItemTypeIssue, 1)
	require.NoError(err)
	assert.False(denied.Allowed)
	assert.Contains(denied.Detail, "reserve")
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

func TestGitHubArchiveAdmissionAllowsAStartedItemToSpendDownToTheReserve(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	database := dbtest.Open(t)
	now := time.Date(2026, 7, 28, 18, 30, 0, 0, time.UTC)
	reset := now.Add(30 * time.Minute)
	registry := NewQuotaRegistry()
	registry.now = func() time.Time { return now }
	client := &credentialRateLimitSnapshotMockClient{mockClient: &mockClient{}}
	identity := IdentityKey{Host: "github.test", Principal: "user:7"}
	bucket := RateBucketKey("github", "github.test", "user:7")
	syncer := NewSyncer(
		map[string]Client{"github.test": client},
		database, nil,
		[]RepoRef{{
			Platform: platform.KindGitHub, PlatformHost: "github.test",
			Owner: "acme", Name: "widget",
		}},
		time.Hour, nil,
		map[string]*SyncBudget{bucket: NewSyncBudget(100000)},
	)
	router, err := NewHostRouter("github.test", &Route{
		Key: RouteKey{Host: "github.test", Owner: "acme"}, Client: client,
		ReadIdentity: identity, WriteIdentity: identity,
	})
	require.NoError(err)
	syncer.SetGitHubRouters(map[string]*HostRouter{"github.test": router})
	syncer.SetQuotaRegistry(registry)
	syncer.now = func() time.Time { return now }
	registry.UpdateSnapshot(identity, QuotaResourceREST,
		Rate{Limit: 5000, Remaining: 4500, Reset: reset})
	registry.UpdateSnapshot(identity, QuotaResourceGraphQL,
		Rate{Limit: 5000, Remaining: 4500, Reset: reset})
	ref := platform.RepoRef{
		Platform: platform.KindGitHub, Host: "github.test",
		Owner: "acme", Name: "widget",
	}

	admission, err := syncer.Admit(t.Context(), ref, db.ArchiveItemTypeIssue, 1)
	require.NoError(err)
	require.True(admission.Allowed)
	t.Cleanup(func() { admission.Complete(nil, false) })
	// Halfway through the window the archive floor is 3000, so 1500 is free
	// to start items on. A started item is held to the limit/5 reserve of
	// 1000 instead, so one too large for that 1500 still finishes.
	for range 3500 {
		assert.True(ConsumeArchiveAttemptAllowance(admission.Context))
	}
	assert.False(ConsumeArchiveAttemptAllowance(admission.Context))
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

	syncer.hosts().budgets[key].Spend(100)
	denied, err := syncer.Admit(t.Context(), ref, db.ArchiveItemTypeIssue, 1)
	require.NoError(err)
	require.False(denied.Allowed)

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
	result, syncErr := syncer.SyncArchiveItem(
		admission.Context, ref, db.ArchiveItemTypeIssue, 7,
	)
	require.Error(syncErr)
	require.False(result.ProviderAttempted)
	require.Nil(admission.Complete(syncErr, result.ProviderAttempted))

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

func TestArchiveAdmissionProceedsWhileLiveSyncIsInFlight(t *testing.T) {
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
				Key: platform.RepositoryIDKey(1001),
			}
			provider := &blockingPriorityProvider{
				kind: ref.Platform, host: ref.Host,
				ref: ref, operation: tc.operation,
				started: make(chan struct{}), release: make(chan struct{}),
			}
			registry, err := platform.NewRegistry(provider)
			require.NoError(err)
			key := RateBucketKey(string(ref.Platform), ref.Host, "host")
			tracker := NewPlatformRateTracker(database, string(ref.Platform), ref.Host, "host", "rest")
			tracker.UpdateFromRate(Rate{
				Limit: 5000, Remaining: 4999, Reset: time.Now().UTC().Add(time.Minute),
			})
			syncer := NewSyncerWithRegistry(
				registry, database, nil, []RepoRef{{
					Platform: ref.Platform, PlatformHost: ref.Host,
					Owner: ref.Owner, Name: ref.Name, RepoPath: ref.RepoPath,
				}}, time.Hour,
				map[string]*RateTracker{key: tracker}, map[string]*SyncBudget{key: NewSyncBudget(100)},
			)
			t.Cleanup(syncer.Stop)

			done := make(chan error, 1)
			go func() { done <- tc.run(t.Context(), syncer, ref) }()
			select {
			case <-provider.started:
			case <-time.After(5 * time.Second):
				require.Fail("live provider call did not start")
			}
			admission, err := syncer.Admit(
				t.Context(), ref, db.ArchiveItemTypeIssue, 1,
			)
			require.NoError(err)
			assert.True(admission.Allowed, admission.Detail)
			if admission.Complete != nil {
				admission.Complete(nil, false)
			}
			close(provider.release)
			require.NoError(<-done)
		})
	}
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

func TestArchiveAdmitNotDeferredByDisplacedRepositoryCooldown(t *testing.T) {
	require := require.New(t)
	database := dbtest.Open(t)
	key := RateBucketKey("github", "github.test", "host")
	tracker := NewPlatformRateTracker(database, "github", "github.test", "host", "rest")
	now := time.Now().UTC()
	tracker.UpdateFromRate(Rate{Limit: 5000, Remaining: 4999, Reset: now.Add(time.Minute)})
	syncer := NewSyncerWithRegistry(
		nil, database, nil, nil, time.Hour,
		map[string]*RateTracker{key: tracker},
		map[string]*SyncBudget{key: NewSyncBudget(100)},
	)
	syncer.now = func() time.Time { return now }
	displaced := RepoRef{
		Platform: platform.KindGitHub, PlatformHost: "github.test",
		Owner: "acme", Name: "widget", Key: platform.RepositoryIDKey(1001),
	}
	syncer.featureCooldowns.deferUntil(
		displaced, platform.RepositoryFeatureIssues, now.Add(time.Hour),
	)

	ref := platform.RepoRef{
		Platform: platform.KindGitHub, Host: "github.test",
		Owner: "acme", Name: "widget", Key: platform.RepositoryIDKey(1002),
	}
	admission, err := syncer.Admit(t.Context(), ref, db.ArchiveItemTypeIssue, 1)
	require.NoError(err)
	require.Nil(admission.FeatureDeferred,
		"a replacement repository must not inherit the displaced repository's cooldown")
	require.True(admission.Allowed)
	t.Cleanup(func() { admission.Complete(nil, true) })
}

func TestConfiguredRepositoriesCarryStableProviderIdentity(t *testing.T) {
	require := require.New(t)
	database := dbtest.Open(t)
	syncer := NewSyncer(
		nil, database, nil, []RepoRef{{
			Platform: platform.KindGitHub, PlatformHost: "github.test",
			Owner: "acme", Name: "widget", Key: platform.RepositoryIDKey(1001),
		}}, time.Minute, nil, nil,
	)

	refs, err := syncer.ConfiguredRepositories(t.Context())
	require.NoError(err)
	require.Len(refs, 1)
	require.Equal(platform.RepositoryIDKey(1001), refs[0].Key,
		"archive scheduling needs the stable provider identity for cooldown keys")
}

func newProviderQuotaAdmissionSyncer(
	t *testing.T, now time.Time, budget *SyncBudget,
) (*Syncer, *QuotaRegistry, platform.RepoRef) {
	t.Helper()
	database := dbtest.Open(t)
	registry := NewQuotaRegistry()
	registry.now = func() time.Time { return now }
	client := &credentialRateLimitSnapshotMockClient{mockClient: &mockClient{}}
	identity := IdentityKey{Host: "github.test", Principal: "user:7"}
	bucket := RateBucketKey("github", "github.test", "user:7")
	syncer := NewSyncer(
		map[string]Client{"github.test": client},
		database, nil,
		[]RepoRef{{
			Platform: platform.KindGitHub, PlatformHost: "github.test",
			Owner: "acme", Name: "widget",
		}},
		time.Hour, nil,
		map[string]*SyncBudget{bucket: budget},
	)
	router, err := NewHostRouter("github.test", &Route{
		Key: RouteKey{Host: "github.test", Owner: "acme"}, Client: client,
		ReadIdentity: identity, WriteIdentity: identity,
	})
	require.NoError(t, err)
	syncer.SetGitHubRouters(map[string]*HostRouter{"github.test": router})
	syncer.SetQuotaRegistry(registry)
	syncer.now = func() time.Time { return now }
	ref := platform.RepoRef{
		Platform: platform.KindGitHub, Host: "github.test",
		Owner: "acme", Name: "widget",
	}
	return syncer, registry, ref
}

// Archive work runs beside live sync, so it must not take in the first
// minutes of a window what live sync needs for the rest of it. Early in the
// window, quota that live sync has already used is not surplus yet, and the
// archive waits only until its floor has fallen far enough, not for the reset.
func TestGitHubArchiveAdmissionWaitsForSurplusEarlyInTheWindow(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	now := time.Date(2026, 7, 28, 18, 30, 0, 0, time.UTC)
	reset := now.Add(54 * time.Minute)
	syncer, registry, ref := newProviderQuotaAdmissionSyncer(
		t, now, NewSyncBudget(100000),
	)
	identity := IdentityKey{Host: "github.test", Principal: "user:7"}
	// Six minutes in, live sync has used 500 of 5000. The floor has fallen 400
	// from the limit, so the archive is 100 short, plus the 2 it asks for.
	registry.UpdateSnapshot(identity, QuotaResourceREST,
		Rate{Limit: 5000, Remaining: 4500, Reset: reset})
	registry.UpdateSnapshot(identity, QuotaResourceGraphQL,
		Rate{Limit: 5000, Remaining: 4500, Reset: reset})

	admission, err := syncer.Admit(t.Context(), ref, db.ArchiveItemTypeIssue, 2)
	require.NoError(err)
	require.False(admission.Allowed)
	assert.Equal("provider rate reserve reached", admission.Detail)
	require.NotNil(admission.RetryAt)
	// The floor falls 4000 an hour: 102 takes about 92 seconds.
	assert.WithinDuration(now.Add(92*time.Second), *admission.RetryAt, time.Second)
}

// The local sync budget meters live sync only; provider-paced archive
// admission must not defer because live sync spent the configured hourly
// ceiling while provider quota still has surplus above the archive reserve.
// In steady state the room above the falling floor is about one item's
// declared cost. An item that needs more requests than that must still finish
// in one pass: cut off at the floor, it would lose the pages it had read and
// be cut off again on every retry. It borrows from the rest of the window
// instead, and the next item waits until the floor has fallen past the debt.
func TestGitHubArchiveItemLargerThanThePacedSurplusFinishesAndTheNextWaits(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	start := time.Date(2026, 7, 28, 18, 30, 0, 0, time.UTC)
	reset := start.Add(30 * time.Minute)
	current := start
	syncer, registry, ref := newProviderQuotaAdmissionSyncer(
		t, start, NewSyncBudget(100000),
	)
	registry.now = func() time.Time { return current }
	syncer.now = func() time.Time { return current }
	identity := IdentityKey{Host: "github.test", Principal: "user:7"}
	// Halfway through the window the archive floor is 3000, so there is room
	// to start exactly one item of the declared cost.
	const cost = 26
	for _, resource := range []QuotaResource{QuotaResourceREST, QuotaResourceGraphQL} {
		registry.UpdateSnapshot(identity, resource,
			Rate{Limit: 5000, Remaining: 3000 + cost, Reset: reset})
	}
	remaining := 3000 + cost
	transport := &quotaTransport{
		base: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			remaining--
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       http.NoBody,
				Header:     quotaTestHeaders(5000, remaining, reset),
				Request:    req,
			}, nil
		}),
		registry: registry, identity: identity, resource: QuotaResourceREST,
	}

	admission, err := syncer.Admit(t.Context(), ref, db.ArchiveItemTypeMergeRequest, cost)
	require.NoError(err)
	require.True(admission.Allowed)
	for page := 1; page <= 60; page++ {
		req, err := http.NewRequestWithContext(
			admission.Context, http.MethodGet, "https://api.github.test/repos/acme/widget", nil,
		)
		require.NoError(err)
		resp, err := transport.RoundTrip(req)
		require.NoError(err, "page %d of a started item", page)
		resp.Body.Close()
	}
	admission.Complete(nil, true)

	// The item left the pool 34 below the floor. The next one needs that back
	// plus its own 26, and the floor falls 4000 an hour: 54 seconds.
	next, err := syncer.Admit(t.Context(), ref, db.ArchiveItemTypeMergeRequest, cost)
	require.NoError(err)
	require.False(next.Allowed)
	require.NotNil(next.RetryAt)
	assert.WithinDuration(start.Add(54*time.Second), *next.RetryAt, time.Second)

	current = *next.RetryAt
	next, err = syncer.Admit(t.Context(), ref, db.ArchiveItemTypeMergeRequest, cost)
	require.NoError(err)
	require.True(next.Allowed)
	next.Complete(nil, false)
}

func TestGitHubArchiveAdmissionIgnoresLocalSyncBudget(t *testing.T) {
	require := require.New(t)
	now := time.Date(2026, 7, 28, 18, 30, 0, 0, time.UTC)
	reset := now.Add(30 * time.Minute)
	budget := NewSyncBudget(100)
	budget.Spend(100)
	syncer, registry, ref := newProviderQuotaAdmissionSyncer(t, now, budget)
	identity := IdentityKey{Host: "github.test", Principal: "user:7"}
	registry.UpdateSnapshot(identity, QuotaResourceREST,
		Rate{Limit: 5000, Remaining: 4500, Reset: reset})
	registry.UpdateSnapshot(identity, QuotaResourceGraphQL,
		Rate{Limit: 5000, Remaining: 4500, Reset: reset})

	admission, err := syncer.Admit(t.Context(), ref, db.ArchiveItemTypeIssue, 1)
	require.NoError(err)
	require.True(admission.Allowed)
	t.Cleanup(func() { admission.Complete(nil, false) })
}

// Remaining quota at or below the archive reserve defers admission until the
// provider window resets, even though the global rate reserve buffer still
// has headroom.
func TestGitHubArchiveAdmissionDefersAtArchiveReserve(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	now := time.Date(2026, 7, 28, 18, 30, 0, 0, time.UTC)
	reset := now.Add(30 * time.Minute)
	syncer, registry, ref := newProviderQuotaAdmissionSyncer(
		t, now, NewSyncBudget(100000),
	)
	identity := IdentityKey{Host: "github.test", Principal: "user:7"}
	registry.UpdateSnapshot(identity, QuotaResourceREST,
		Rate{Limit: 5000, Remaining: 1000, Reset: reset})
	registry.UpdateSnapshot(identity, QuotaResourceGraphQL,
		Rate{Limit: 5000, Remaining: 4500, Reset: reset})

	denied, err := syncer.Admit(t.Context(), ref, db.ArchiveItemTypeIssue, 1)
	require.NoError(err)
	assert.False(denied.Allowed)
	assert.Contains(denied.Detail, "provider rate reserve")
	require.NotNil(denied.RetryAt)
	assert.Equal(reset, *denied.RetryAt)
}

// A pool sitting at its own limit/5 reserve blocks admission even when the
// smallest-limit pool still has headroom: reserves are per pool, and the
// min-limit pool's reserve must not be applied to a larger pool.
func TestGitHubArchiveAdmissionHonorsEachPoolsOwnReserve(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	now := time.Date(2026, 7, 28, 18, 30, 0, 0, time.UTC)
	reset := now.Add(30 * time.Minute)
	syncer, registry, ref := newProviderQuotaAdmissionSyncer(
		t, now, NewSyncBudget(100000),
	)
	identity := IdentityKey{Host: "github.test", Principal: "user:7"}
	registry.UpdateSnapshot(identity, QuotaResourceREST,
		Rate{Limit: 15000, Remaining: 3000, Reset: reset})
	registry.UpdateSnapshot(identity, QuotaResourceGraphQL,
		Rate{Limit: 5000, Remaining: 4800, Reset: reset})

	denied, err := syncer.Admit(t.Context(), ref, db.ArchiveItemTypeIssue, 1)
	require.NoError(err)
	assert.False(denied.Allowed)
	assert.Contains(denied.Detail, "provider rate reserve")
	require.NotNil(denied.RetryAt)
	assert.Equal(reset, *denied.RetryAt)
}

// When admission defers on the archive reserve, the retry time comes from the
// pools that actually lack headroom. Waiting for the latest reset across all
// pools would leave archives paused after the exhausted pool has reset.
func TestGitHubArchiveAdmissionRetriesWhenDeficientPoolResets(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	now := time.Date(2026, 7, 28, 18, 30, 0, 0, time.UTC)
	restReset := now.Add(10 * time.Minute)
	graphQLReset := now.Add(30 * time.Minute)
	syncer, registry, ref := newProviderQuotaAdmissionSyncer(
		t, now, NewSyncBudget(100000),
	)
	identity := IdentityKey{Host: "github.test", Principal: "user:7"}
	// REST sits at its own reserve and resets first; GraphQL has headroom
	// and resets later.
	registry.UpdateSnapshot(identity, QuotaResourceREST,
		Rate{Limit: 15000, Remaining: 3000, Reset: restReset})
	registry.UpdateSnapshot(identity, QuotaResourceGraphQL,
		Rate{Limit: 5000, Remaining: 4800, Reset: graphQLReset})

	denied, err := syncer.Admit(t.Context(), ref, db.ArchiveItemTypeIssue, 1)
	require.NoError(err)
	assert.False(denied.Allowed)
	assert.Contains(denied.Detail, "provider rate reserve")
	require.NotNil(denied.RetryAt)
	assert.Equal(restReset, *denied.RetryAt)
}

func requireEnsureConfigured(t *testing.T, s *archive.Service, refs []platform.RepoRef) {
	t.Helper()
	_, err := s.EnsureConfigured(t.Context(), refs)
	require.NoError(t, err)
}

var (
	_ platform.IssuePageReader        = (*archiveWorkerProvider)(nil)
	_ platform.IssueReader            = (*archiveWorkerProvider)(nil)
	_ platform.MergeRequestPageReader = (*archiveWorkerProvider)(nil)
	_ platform.IssueReader            = (*blockingPriorityProvider)(nil)
	_ platform.MergeRequestReader     = (*blockingPriorityProvider)(nil)
	_ platform.RepositoryReader       = (*blockingPriorityProvider)(nil)
	_ platform.IssueReader            = (*heldArchiveProvider)(nil)
)
