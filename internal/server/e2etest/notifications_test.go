package e2etest

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	gh "github.com/google/go-github/v91/github"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/apiclient"
	"go.kenn.io/forge/internal/apiclient/generated"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/github"
	"go.kenn.io/forge/internal/testutil/reposeed"
	"go.kenn.io/forge/platform"
)

func TestNotificationsTriageFlowE2E(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	srv, database, _ := setupTestServerWithConfigContent(t, `
sync_interval = "5m"
github_token_env = "KENN_FORGE_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091

[notifications]
enabled = true
sync_interval = "2m"
propagation_interval = "1m"
batch_size = 25

[[repos]]
owner = "acme"
name = "widget"

[[repos]]
owner = "acme"
name = "tools"
`, &mockGH{})
	ts := httptest.NewServer(srv)
	defer ts.Close()

	client, err := apiclient.NewWithHTTPClient(ts.URL, ts.Client())
	require.NoError(err)

	widgetRepoID, err := reposeed.Seed(t.Context(), database, verifiedRepoIdentity(
		db.GitHubRepoIdentity("github.com", "acme", "widget"),
	))
	require.NoError(err)
	toolsRepoID, err := reposeed.Seed(t.Context(), database, verifiedRepoIdentity(
		db.GitHubRepoIdentity("github.com", "acme", "tools"),
	))
	require.NoError(err)

	now := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
	widgetNumber := 42
	toolsNumber := 5
	require.NoError(database.UpsertNotifications(t.Context(), []db.Notification{
		{
			Platform:               "github",
			PlatformHost:           "github.com",
			PlatformNotificationID: "thread-widget-42",
			RepoID:                 &widgetRepoID,
			RepoOwner:              "acme",
			RepoName:               "widget",
			SubjectType:            "PullRequest",
			SubjectTitle:           "Review requested",
			WebURL:                 "https://github.com/acme/widget/pull/42",
			ItemNumber:             &widgetNumber,
			ItemType:               "pr",
			ItemAuthor:             "octocat",
			Reason:                 "review_requested",
			Unread:                 true,
			Participating:          true,
			SourceUpdatedAt:        now,
			SyncedAt:               now,
		},
		{
			Platform:               "github",
			PlatformHost:           "github.com",
			PlatformNotificationID: "thread-tools-5",
			RepoID:                 &toolsRepoID,
			RepoOwner:              "acme",
			RepoName:               "tools",
			SubjectType:            "Issue",
			SubjectTitle:           "Mentioned in issue",
			WebURL:                 "https://github.com/acme/tools/issues/5",
			ItemNumber:             &toolsNumber,
			ItemType:               "issue",
			ItemAuthor:             "hubot",
			Reason:                 "mention",
			Unread:                 true,
			Participating:          true,
			SourceUpdatedAt:        now.Add(-time.Hour),
			SyncedAt:               now,
		},
	}))

	listResp, err := client.HTTP.ListNotificationsWithResponse(t.Context(), &generated.ListNotificationsRequestOptions{Query: &generated.ListNotificationsQuery{State: new("unread")}})
	require.NoError(err)
	require.NotNil(listResp.JSON200)
	require.NotNil(listResp.JSON200.Items)
	require.Len(listResp.JSON200.Items, 2)
	assert.Equal(int64(2), listResp.JSON200.Summary.Unread)
	assert.Equal(int64(2), listResp.JSON200.Summary.TotalActive)

	ids := []int64{listResp.JSON200.Items[0].ID, listResp.JSON200.Items[1].ID, 999999}
	readResp, err := client.HTTP.MarkNotificationsReadWithResponse(t.Context(), &generated.MarkNotificationsReadRequestOptions{Body: &generated.MarkNotificationsReadBody{Ids: ids}})
	require.NoError(err)
	require.NotNil(readResp.JSON200)
	require.ElementsMatch(ids[:2], readResp.JSON200.Succeeded)
	require.ElementsMatch(ids[:2], readResp.JSON200.Queued)
	require.Len(readResp.JSON200.Failed, 1)
	assert.Equal(int64(999999), readResp.JSON200.Failed[0].ID)

	readList, err := client.HTTP.ListNotificationsWithResponse(t.Context(), &generated.ListNotificationsRequestOptions{Query: &generated.ListNotificationsQuery{State: new("read")}})
	require.NoError(err)
	require.NotNil(readList.JSON200)
	require.NotNil(readList.JSON200.Items)
	require.Len(readList.JSON200.Items, 2)
	for _, item := range readList.JSON200.Items {
		assert.False(item.Unread)
		require.NotNil(item.GithubReadQueuedAt)
	}

	doneResp, err := client.HTTP.MarkNotificationsDoneWithResponse(t.Context(), &generated.MarkNotificationsDoneRequestOptions{Body: &generated.MarkNotificationsDoneBody{Ids: ids}})
	require.NoError(err)
	require.NotNil(doneResp.JSON200)
	require.ElementsMatch(ids[:2], doneResp.JSON200.Succeeded)
	require.ElementsMatch(ids[:2], doneResp.JSON200.Queued)
	require.Len(doneResp.JSON200.Failed, 1)

	doneList, err := client.HTTP.ListNotificationsWithResponse(t.Context(), &generated.ListNotificationsRequestOptions{Query: &generated.ListNotificationsQuery{State: new("done")}})
	require.NoError(err)
	require.NotNil(doneList.JSON200)
	require.NotNil(doneList.JSON200.Items)
	require.Len(doneList.JSON200.Items, 2)
	for _, item := range doneList.JSON200.Items {
		require.NotNil(item.DoneAt)
	}

	undoneIDs := []int64{ids[0], ids[1]}
	undoneResp, err := client.HTTP.MarkNotificationsUndoneWithResponse(t.Context(), &generated.MarkNotificationsUndoneRequestOptions{Body: &generated.MarkNotificationsUndoneBody{Ids: undoneIDs}})
	require.NoError(err)
	require.NotNil(undoneResp.JSON200)
	require.ElementsMatch(undoneIDs, undoneResp.JSON200.Succeeded)

	activeList, err := client.HTTP.ListNotificationsWithResponse(t.Context(), &generated.ListNotificationsRequestOptions{Query: &generated.ListNotificationsQuery{State: new("active")}})
	require.NoError(err)
	require.NotNil(activeList.JSON200)
	require.NotNil(activeList.JSON200.Items)
	require.Len(activeList.JSON200.Items, 2)
	for _, item := range activeList.JSON200.Items {
		assert.Nil(item.DoneAt)
	}
}

func TestNotificationSyncReconcilesReusedRouteE2E(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	ctx := t.Context()
	number := 7
	// Relative to now so the seeded activity stays inside the activity
	// endpoint's default 7d window regardless of the calendar date.
	firstActivityAt := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	var activityAt atomic.Int64
	activityAt.Store(firstActivityAt.UnixNano())
	var listCalls atomic.Int32
	mock := &mockGH{
		getRepositoryFn: func(_ context.Context, owner, name string) (*gh.Repository, error) {
			return &gh.Repository{
				ID: new(int64(1002)), Name: &name,
				Owner: &gh.User{Login: &owner}, Archived: new(bool),
				AllowSquashMerge: new(true), AllowMergeCommit: new(false),
				AllowRebaseMerge: new(false),
				Permissions:      &gh.RepositoryPermissions{Push: new(false)},
			}, nil
		},
		listNotificationsFn: func(_ context.Context, opts github.NotificationListOptions) ([]github.NotificationThread, bool, error) {
			listCalls.Add(1)
			if opts.Participating {
				return nil, false, nil
			}
			return []github.NotificationThread{{
				ID: "replacement-thread", RepoOwner: "acme", RepoName: "widget",
				SubjectType: "PullRequest", SubjectTitle: "Replacement notification",
				WebURL: "https://github.com/acme/widget/pull/7", ItemNumber: &number,
				ItemType: "pr", Reason: "review_requested", Unread: true,
				UpdatedAt: time.Unix(0, activityAt.Load()).UTC(),
			}}, false, nil
		},
	}
	srv, database, _ := setupTestServerWithConfigContent(t, `
sync_interval = "5m"
github_token_env = "KENN_FORGE_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091

[notifications]
enabled = true
sync_interval = "2m"
propagation_interval = "1m"
batch_size = 25

[[repos]]
owner = "acme"
name = "widget"
`, mock)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	client, err := apiclient.NewWithHTTPClient(ts.URL, ts.Client())
	require.NoError(err)
	withJSON := func(_ context.Context, req *http.Request) error {
		req.Header.Set("Content-Type", "application/json")
		return nil
	}

	oldRepoID, err := reposeed.Seed(ctx, database, db.RepoIdentity{
		Platform: "github", PlatformHost: "github.com", PlatformRepoID: 1001,
		Owner: "acme", Name: "widget",
	})
	require.NoError(err)
	syncResp, err := client.HTTP.SyncNotificationsWithResponse(ctx, withJSON)
	require.NoError(err)
	assert.Equal(http.StatusAccepted, syncResp.StatusCode)
	var firstSyncAt time.Time
	require.Eventually(func() bool {
		watermark, watermarkErr := database.GetNotificationSyncWatermark(
			ctx, "github", "github.com", "acme", "widget",
		)
		if watermarkErr != nil || watermark == nil || listCalls.Load() < 2 {
			return false
		}
		resp, callErr := client.HTTP.ListNotificationsWithResponse(ctx, &generated.ListNotificationsRequestOptions{Query: &generated.ListNotificationsQuery{State: new("unread")}})
		if callErr != nil || resp.JSON200 == nil || resp.JSON200.Sync.Running {
			return false
		}
		firstSyncAt = watermark.LastSuccessfulSyncAt
		return true
	}, 10*time.Second, 10*time.Millisecond)

	active, err := database.GetRepoByIdentity(ctx, db.RepoIdentity{
		Platform: "github", PlatformHost: "github.com", Owner: "acme", Name: "widget",
	})
	require.NoError(err)
	require.NotNil(active)
	assert.Equal(int64(1002), active.PlatformRepoID)
	assert.NotEqual(oldRepoID, active.ID)
	repoResp, err := client.HTTP.GetRepoWithResponse(ctx, &generated.GetRepoRequestOptions{PathParams: &generated.GetRepoPath{Provider: "github", Owner: "acme", Name: "widget"}})
	require.NoError(err)
	require.Equal(http.StatusOK, repoResp.StatusCode, string(repoResp.Body))
	require.NotNil(repoResp.JSON200)
	assert.True(repoResp.JSON200.AllowSquashMerge)
	assert.False(repoResp.JSON200.AllowMergeCommit)
	assert.False(repoResp.JSON200.AllowRebaseMerge)
	assert.False(repoResp.JSON200.ViewerCanMerge)
	assert.False(repoResp.JSON200.Operations.MergePr.Available)
	_, err = database.UpsertMergeRequest(ctx, &db.MergeRequest{
		RepoID: active.ID, PlatformID: 7, Number: number,
		URL: "https://github.com/acme/widget/pull/7", Title: "Replacement pull request",
		Author: "octocat", State: db.MergeRequestStateOpen,
		CreatedAt: firstActivityAt.Add(-time.Hour), UpdatedAt: firstActivityAt,
		LastActivityAt: firstActivityAt,
	})
	require.NoError(err)

	var notificationID int64
	require.Eventually(func() bool {
		resp, callErr := client.HTTP.ListNotificationsWithResponse(ctx, &generated.ListNotificationsRequestOptions{Query: &generated.ListNotificationsQuery{State: new("unread")}})
		if callErr != nil || resp.JSON200 == nil || resp.JSON200.Items == nil || len(resp.JSON200.Items) != 1 {
			return false
		}
		item := resp.JSON200.Items[0]
		notificationID = item.ID
		return item.PlatformThreadID == "replacement-thread" && item.RepoOwner == "acme" && item.RepoName == "widget"
	}, 3*time.Second, 10*time.Millisecond)

	since := firstActivityAt.Add(-time.Minute).Format(time.RFC3339)
	activityResp, err := client.HTTP.ListActivityWithResponse(ctx, &generated.ListActivityRequestOptions{Query: &generated.ListActivityQuery{
		Types: []string{"notification"}, Since: &since,
	}})
	require.NoError(err)
	require.NotNil(activityResp.JSON200)
	require.NotNil(activityResp.JSON200.Items)
	require.Len(activityResp.JSON200.Items, 1)
	activity := activityResp.JSON200.Items[0]
	assert.Equal("notification", activity.ActivityType)
	assert.Equal(int64(number), activity.ItemNumber)
	assert.Equal("acme", activity.RepoOwner)
	assert.Equal("widget", activity.RepoName)

	doneResp, err := client.HTTP.MarkNotificationsDoneWithResponse(ctx, &generated.MarkNotificationsDoneRequestOptions{Body: &generated.MarkNotificationsDoneBody{Ids: []int64{notificationID}}})
	require.NoError(err)
	require.NotNil(doneResp.JSON200)
	require.ElementsMatch([]int64{notificationID}, doneResp.JSON200.Succeeded)
	require.ElementsMatch([]int64{notificationID}, doneResp.JSON200.Queued)

	activityAt.Store(firstActivityAt.Add(time.Hour).UnixNano())
	syncResp, err = client.HTTP.SyncNotificationsWithResponse(ctx, withJSON)
	require.NoError(err)
	assert.Equal(http.StatusAccepted, syncResp.StatusCode)
	require.Eventually(func() bool {
		watermark, watermarkErr := database.GetNotificationSyncWatermark(
			ctx, "github", "github.com", "acme", "widget",
		)
		if watermarkErr != nil || watermark == nil ||
			!watermark.LastSuccessfulSyncAt.After(firstSyncAt) || listCalls.Load() < 4 {
			return false
		}
		resp, callErr := client.HTTP.ListNotificationsWithResponse(ctx, &generated.ListNotificationsRequestOptions{Query: &generated.ListNotificationsQuery{State: new("unread")}})
		if callErr != nil || resp.JSON200 == nil || resp.JSON200.Sync.Running ||
			resp.JSON200.Items == nil || len(resp.JSON200.Items) != 1 {
			return false
		}
		item := resp.JSON200.Items[0]
		return item.ID == notificationID && item.DoneAt == nil && item.GithubReadQueuedAt == nil
	}, 10*time.Second, 10*time.Millisecond)
}

func TestNotificationReadPropagationDefersQueuedAcksOnRefetchRateLimitE2E(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	resetAt := time.Now().UTC().Add(time.Hour).Round(0)
	var fetchedThreads []string
	var markedThreads []string
	mock := &mockGH{
		getNotificationThreadFn: func(_ context.Context, threadID string) (github.NotificationThread, error) {
			fetchedThreads = append(fetchedThreads, threadID)
			return github.NotificationThread{}, &gh.RateLimitError{
				Rate: gh.Rate{Reset: gh.Timestamp{Time: resetAt}},
				Response: &http.Response{
					StatusCode: http.StatusForbidden,
					Request:    httptest.NewRequestWithContext(t.Context(), http.MethodGet, "https://api.github.com/notifications/threads/"+threadID, nil),
				},
				Message: "API rate limit exceeded",
			}
		},
		markNotificationReadFn: func(_ context.Context, threadID string) error {
			markedThreads = append(markedThreads, threadID)
			return nil
		},
	}
	srv, database, _, syncer := setupTestServerWithConfigContentAndSyncer(t, `
sync_interval = "5m"
github_token_env = "KENN_FORGE_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091

[notifications]
enabled = true
sync_interval = "2m"
propagation_interval = "1m"
batch_size = 25

[[repos]]
owner = "acme"
name = "widget"
`, mock)
	ts := httptest.NewServer(srv)
	defer ts.Close()

	client, err := apiclient.NewWithHTTPClient(ts.URL, ts.Client())
	require.NoError(err)

	repoID, err := reposeed.Seed(t.Context(), database, verifiedRepoIdentity(
		db.GitHubRepoIdentity("github.com", "acme", "widget"),
	))
	require.NoError(err)
	now := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
	numberOne := 7
	numberTwo := 8
	require.NoError(database.UpsertNotifications(t.Context(), []db.Notification{
		{
			Platform:               "github",
			PlatformHost:           "github.com",
			PlatformNotificationID: "thread-widget-7",
			RepoID:                 &repoID,
			RepoOwner:              "acme",
			RepoName:               "widget",
			SubjectType:            "PullRequest",
			SubjectTitle:           "Review requested",
			WebURL:                 "https://github.com/acme/widget/pull/7",
			ItemNumber:             &numberOne,
			ItemType:               "pr",
			Reason:                 "review_requested",
			Unread:                 true,
			SourceUpdatedAt:        now,
			SyncedAt:               now,
		},
		{
			Platform:               "github",
			PlatformHost:           "github.com",
			PlatformNotificationID: "thread-widget-8",
			RepoID:                 &repoID,
			RepoOwner:              "acme",
			RepoName:               "widget",
			SubjectType:            "PullRequest",
			SubjectTitle:           "Mentioned",
			WebURL:                 "https://github.com/acme/widget/pull/8",
			ItemNumber:             &numberTwo,
			ItemType:               "pr",
			Reason:                 "mention",
			Unread:                 true,
			SourceUpdatedAt:        now,
			SyncedAt:               now,
		},
	}))

	listResp, err := client.HTTP.ListNotificationsWithResponse(t.Context(), &generated.ListNotificationsRequestOptions{Query: &generated.ListNotificationsQuery{State: new("unread")}})
	require.NoError(err)
	require.NotNil(listResp.JSON200)
	require.NotNil(listResp.JSON200.Items)
	require.Len(listResp.JSON200.Items, 2)
	ids := []int64{listResp.JSON200.Items[0].ID, listResp.JSON200.Items[1].ID}
	readResp, err := client.HTTP.MarkNotificationsReadWithResponse(t.Context(), &generated.MarkNotificationsReadRequestOptions{Body: &generated.MarkNotificationsReadBody{Ids: ids}})
	require.NoError(err)
	require.NotNil(readResp.JSON200)
	require.ElementsMatch(ids, readResp.JSON200.Succeeded)
	require.ElementsMatch(ids, readResp.JSON200.Queued)

	err = syncer.ProcessQueuedNotificationReads(t.Context(), platform.KindGitHub, "github.com", 10)
	require.Error(err)
	require.ErrorContains(err, "notification read propagation rate limited")

	items, err := database.ListNotifications(t.Context(), db.ListNotificationsOpts{State: "all"})
	require.NoError(err)
	require.Len(items, 2)
	attemptsByThread := map[string]int{}
	errorsByThread := map[string]string{}
	nextAttemptByThread := map[string]*time.Time{}
	queuedByThread := map[string]*time.Time{}
	for _, item := range items {
		attemptsByThread[item.PlatformNotificationID] = item.SourceAckAttempts
		errorsByThread[item.PlatformNotificationID] = item.SourceAckError
		nextAttemptByThread[item.PlatformNotificationID] = item.SourceAckNextAttemptAt
		queuedByThread[item.PlatformNotificationID] = item.SourceAckQueuedAt
	}
	assert.Equal([]string{"thread-widget-7"}, fetchedThreads)
	assert.Empty(markedThreads)
	assert.Equal(map[string]int{"thread-widget-7": 0, "thread-widget-8": 0}, attemptsByThread)
	assert.Equal(map[string]string{"thread-widget-7": "rate_limited", "thread-widget-8": "rate_limited"}, errorsByThread)
	for _, threadID := range []string{"thread-widget-7", "thread-widget-8"} {
		if assert.NotNil(nextAttemptByThread[threadID], threadID) {
			assert.Equal(resetAt, *nextAttemptByThread[threadID])
		}
		assert.NotNil(queuedByThread[threadID], threadID)
	}
}

// TestNotificationAckPropagatesAfterRepositoryRenameE2E queues a read
// acknowledgement, completes a repository rename with no intervening
// notification refresh, then propagates. The notification still caches the
// historical owner/name; the acknowledgement must follow the stable
// repository identity to its current route instead of being dropped with the
// upstream thread left unread.
func TestNotificationAckPropagatesAfterRepositoryRenameE2E(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	ctx := t.Context()
	number := 7
	sourceUpdatedAt := time.Date(2026, 8, 3, 10, 0, 0, 0, time.UTC)
	var markedThreads []string
	mock := &mockGH{
		getNotificationThreadFn: func(_ context.Context, threadID string) (github.NotificationThread, error) {
			return github.NotificationThread{
				ID: threadID, RepoOwner: "acme", RepoName: "renamed",
				SubjectType: "PullRequest", SubjectTitle: "Please review",
				WebURL: "https://github.com/acme/renamed/pull/7", ItemNumber: &number,
				ItemType: "pr", Reason: "mention", Unread: false,
				UpdatedAt: sourceUpdatedAt,
			}, nil
		},
		markNotificationReadFn: func(_ context.Context, threadID string) error {
			markedThreads = append(markedThreads, threadID)
			return nil
		},
	}
	srv, database, _, syncer := setupTestServerWithConfigContentAndSyncer(t, `
sync_interval = "5m"
github_token_env = "KENN_FORGE_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091

[notifications]
enabled = true
sync_interval = "2m"
propagation_interval = "1m"
batch_size = 25

[[repos]]
owner = "acme"
name = "widget"
`, mock)
	ts := httptest.NewServer(srv)
	defer ts.Close()
	client, err := apiclient.NewWithHTTPClient(ts.URL, ts.Client())
	require.NoError(err)

	entry, err := database.ObserveRepository(ctx, db.RepoIdentity{
		Platform: "github", PlatformHost: "github.com", PlatformRepoID: 1001,
		Owner: "acme", Name: "widget",
	})
	require.NoError(err)
	repoID := entry.Repository.ID
	require.NoError(database.UpsertNotifications(ctx, []db.Notification{{
		Platform: "github", PlatformHost: "github.com", PlatformNotificationID: "thread-1",
		RepoID: &repoID, RepoOwner: "acme", RepoName: "widget",
		SubjectType: "PullRequest", SubjectTitle: "Please review",
		WebURL: "https://github.com/acme/widget/pull/7", ItemNumber: &number, ItemType: "pr",
		Reason: "mention", Unread: true, SourceUpdatedAt: sourceUpdatedAt, SyncedAt: sourceUpdatedAt,
	}}))

	unreadResp, err := client.HTTP.ListNotificationsWithResponse(ctx, &generated.ListNotificationsRequestOptions{Query: &generated.ListNotificationsQuery{State: new("unread")}})
	require.NoError(err)
	require.NotNil(unreadResp.JSON200)
	require.NotNil(unreadResp.JSON200.Items)
	require.Len(unreadResp.JSON200.Items, 1)
	notificationID := unreadResp.JSON200.Items[0].ID
	doneResp, err := client.HTTP.MarkNotificationsDoneWithResponse(ctx, &generated.MarkNotificationsDoneRequestOptions{Body: &generated.MarkNotificationsDoneBody{Ids: []int64{notificationID}}})
	require.NoError(err)
	require.NotNil(doneResp.JSON200)
	require.ElementsMatch([]int64{notificationID}, doneResp.JSON200.Queued)

	_, err = database.ObserveRepository(ctx, db.RepoIdentity{
		Platform: "github", PlatformHost: "github.com", PlatformRepoID: 1001,
		Owner: "acme", Name: "renamed",
	})
	require.NoError(err)

	require.NoError(syncer.ProcessQueuedNotificationReads(
		ctx, platform.KindGitHub, "github.com", 10,
	))

	assert.Equal([]string{"thread-1"}, markedThreads,
		"the queued acknowledgement must reach the upstream thread")
	// Verified at the database layer: the API listing scopes by the
	// configured route, which stops matching after a rename until a sync
	// republishes the tracked repository — separate behavior from ack
	// propagation.
	items, err := database.ListNotifications(ctx, db.ListNotificationsOpts{State: "all"})
	require.NoError(err)
	require.Len(items, 1)
	assert.Equal(notificationID, items[0].ID)
	assert.False(items[0].Unread)
	assert.NotNil(items[0].DoneAt)
	assert.NotNil(items[0].SourceAckSyncedAt)
	assert.Empty(items[0].SourceAckError)
	assert.Nil(items[0].SourceAckQueuedAt)
}
