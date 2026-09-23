package activityservertest

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/db"
	serverfake "go.kenn.io/forge/internal/testutil/serverfake"

	ghclient "go.kenn.io/forge/internal/github"
	"go.kenn.io/forge/internal/server"
	"go.kenn.io/forge/internal/server/itemapi"
	"go.kenn.io/forge/internal/server/notificationapi"
)

func notificationsEnabledConfig() *config.Config {
	// Notifications are always on; a non-nil config is all the server
	// needs to serve the notification APIs.
	return &config.Config{}
}

func TestNotificationsAPIListsAndQueuesReadWithoutDone(t *testing.T) {
	require := require.New(t)
	database := serverfake.OpenTestDB(t)
	id := serverfake.SeedServerNotification(t, database)
	s := server.New(database, nil, nil, "/", notificationsEnabledConfig(), server.ServerOptions{})
	ts := httptest.NewServer(s)
	defer ts.Close()

	respReq, err := http.NewRequestWithContext(t.Context(), http.MethodGet, ts.URL+"/api/v1/notifications?state=unread", nil)
	require.NoError(err)
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(respReq)
	require.NoError(err)
	defer resp.Body.Close()
	require.Equal(http.StatusOK, resp.StatusCode)
	var listed itemapi.NotificationsResponse
	require.NoError(json.NewDecoder(resp.Body).Decode(&listed))
	require.Len(listed.Items, 1)
	assert := assert.New(t)
	assert.Equal("review_requested", listed.Items[0].Reason)
	assert.Equal("pr", listed.Items[0].ItemType)
	assert.Equal("github", listed.Items[0].Provider)
	assert.Equal("acme/widget", listed.Items[0].RepoPath)

	body, err := json.Marshal(map[string]any{"ids": []int64{id}})
	require.NoError(err)
	markRespReq, err := http.NewRequestWithContext(t.Context(), http.MethodPost, ts.URL+"/api/v1/notifications/read", bytes.NewReader(body))
	require.NoError(err)
	markRespReq.Header.Set("Content-Type", "application/json")
	markResp, err := (&http.Client{Timeout: 5 * time.Second}).Do(markRespReq)
	require.NoError(err)
	defer markResp.Body.Close()
	require.Equal(http.StatusOK, markResp.StatusCode)
	var bulk itemapi.NotificationBulkResponse
	require.NoError(json.NewDecoder(markResp.Body).Decode(&bulk))
	assert.Equal([]int64{id}, bulk.Succeeded)
	assert.Equal([]int64{id}, bulk.Queued)

	readItems, err := database.ListNotifications(t.Context(), db.ListNotificationsOpts{State: "read"})
	require.NoError(err)
	require.Len(readItems, 1)
	assert.Nil(readItems[0].DoneAt)
	assert.NotNil(readItems[0].SourceAckQueuedAt)
}

func TestNotificationRepoFiltersRejectBlankProvider(t *testing.T) {
	require := require.New(t)

	_, err := notificationapi.NotificationRepoFilters([]ghclient.RepoRef{{
		PlatformHost: "github.com",
		Owner:        "acme",
		Name:         "widget",
	}})
	require.ErrorContains(err, "notification repo provider is required")
}

func TestNotificationsAPIMapsNeutralFieldsToExistingGitHubJSON(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	database := serverfake.OpenTestDB(t)
	repoID, err := database.UpsertRepo(t.Context(), serverfake.VerifiedGitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)

	number := 42
	now := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
	lastRead := now.Add(-time.Minute)
	queued := now.Add(time.Minute)
	require.NoError(database.UpsertNotifications(t.Context(), []db.Notification{{
		Platform:                 "github",
		PlatformHost:             "github.com",
		PlatformNotificationID:   "thread-42",
		RepoID:                   &repoID,
		RepoOwner:                "acme",
		RepoName:                 "widget",
		SubjectType:              "PullRequest",
		SubjectTitle:             "Review requested",
		WebURL:                   "https://github.com/acme/widget/pull/42",
		ItemNumber:               &number,
		ItemType:                 "pr",
		ItemAuthor:               "octocat",
		Reason:                   "review_requested",
		Unread:                   true,
		Participating:            true,
		SourceUpdatedAt:          now,
		SourceLastAcknowledgedAt: &lastRead,
		SourceAckQueuedAt:        &queued,
		SourceAckError:           "rate limited",
		SourceAckAttempts:        2,
		SyncedAt:                 now,
	}}))

	s := server.New(database, nil, nil, "/", notificationsEnabledConfig(), server.ServerOptions{})
	ts := httptest.NewServer(s)
	defer ts.Close()

	listed := getNotificationsForTest(t, ts.URL, "all")
	require.Len(listed.Items, 1)
	item := listed.Items[0]
	assert.Equal("thread-42", item.PlatformThreadID)
	assert.Equal(now.Format(time.RFC3339), item.GitHubUpdatedAt)
	assert.Equal(lastRead.Format(time.RFC3339), item.GitHubLastReadAt)
	assert.Equal(queued.Format(time.RFC3339), item.GitHubReadQueuedAt)
	assert.Equal("rate limited", item.GitHubReadError)
	assert.Equal(2, item.GitHubReadAttempts)
}

func getNotificationsForTest(t *testing.T, baseURL string, state string) itemapi.NotificationsResponse {
	t.Helper()
	require := require.New(t)
	respReq, err := http.NewRequestWithContext(t.Context(), http.MethodGet, baseURL+"/api/v1/notifications?state="+state, nil)
	require.NoError(err)
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(respReq)
	require.NoError(err)
	defer resp.Body.Close()
	require.Equal(http.StatusOK, resp.StatusCode)
	var listed itemapi.NotificationsResponse
	require.NoError(json.NewDecoder(resp.Body).Decode(&listed))
	return listed
}

func TestNotificationsAPIShowsClosedLinkedItemsAsDone(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	database := serverfake.OpenTestDB(t)
	repoID, err := database.UpsertRepo(t.Context(), serverfake.VerifiedGitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	now := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
	closedAt := now.Add(time.Hour)
	prNumber := 42
	issueNumber := 43
	_, err = database.UpsertMergeRequest(t.Context(), &db.MergeRequest{
		RepoID: repoID, PlatformID: 4200, Number: prNumber,
		URL: "https://github.com/acme/widget/pull/42", Title: "Closed PR", State: "closed",
		CreatedAt: now, UpdatedAt: closedAt, LastActivityAt: closedAt, ClosedAt: &closedAt,
		PlatformHeadSHA: "head", PlatformBaseSHA: "base",
	})
	require.NoError(err)
	_, err = database.UpsertIssue(t.Context(), &db.Issue{
		RepoID: repoID, PlatformID: 4300, Number: issueNumber,
		URL: "https://github.com/acme/widget/issues/43", Title: "Closed issue", State: "closed",
		CreatedAt: now, UpdatedAt: closedAt, LastActivityAt: closedAt, ClosedAt: &closedAt,
	})
	require.NoError(err)
	require.NoError(database.UpsertNotifications(t.Context(), []db.Notification{
		{
			Platform: "github", PlatformHost: "github.com", PlatformNotificationID: "thread-pr-closed", RepoID: &repoID,
			RepoOwner: "acme", RepoName: "widget", SubjectType: "PullRequest", SubjectTitle: "Closed PR",
			WebURL: "https://github.com/acme/widget/pull/42", ItemNumber: &prNumber, ItemType: "pr",
			Reason: "mention", Unread: true, SourceUpdatedAt: now, SyncedAt: now,
		},
		{
			Platform: "github", PlatformHost: "github.com", PlatformNotificationID: "thread-issue-closed", RepoID: &repoID,
			RepoOwner: "acme", RepoName: "widget", SubjectType: "Issue", SubjectTitle: "Closed issue",
			WebURL: "https://github.com/acme/widget/issues/43", ItemNumber: &issueNumber, ItemType: "issue",
			Reason: "mention", Unread: true, SourceUpdatedAt: now, SyncedAt: now,
		},
	}))
	require.NoError(database.MarkClosedLinkedNotificationsDone(t.Context(), now.Add(2*time.Hour)))
	s := server.New(database, nil, nil, "/", notificationsEnabledConfig(), server.ServerOptions{})
	ts := httptest.NewServer(s)
	defer ts.Close()

	active := getNotificationsForTest(t, ts.URL, "active")
	assert.Empty(active.Items)
	done := getNotificationsForTest(t, ts.URL, "done")
	require.Len(done.Items, 2)
	assert.Equal("closed", done.Items[0].DoneReason)
	assert.NotEmpty(done.Items[0].DoneAt)
	assert.Equal("closed", done.Items[1].DoneReason)
	assert.NotEmpty(done.Items[1].DoneAt)
}

func TestNotificationsAPIReclosesLinkedItemsAfterUndone(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	database := serverfake.OpenTestDB(t)
	repoID, err := database.UpsertRepo(t.Context(), serverfake.VerifiedGitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	now := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
	closedAt := now.Add(time.Hour)
	prNumber := 42
	_, err = database.UpsertMergeRequest(t.Context(), &db.MergeRequest{
		RepoID: repoID, PlatformID: 4200, Number: prNumber,
		URL: "https://github.com/acme/widget/pull/42", Title: "Closed PR", State: "closed",
		CreatedAt: now, UpdatedAt: closedAt, LastActivityAt: closedAt, ClosedAt: &closedAt,
		PlatformHeadSHA: "head", PlatformBaseSHA: "base",
	})
	require.NoError(err)
	require.NoError(database.UpsertNotifications(t.Context(), []db.Notification{{
		Platform: "github", PlatformHost: "github.com", PlatformNotificationID: "thread-pr-closed", RepoID: &repoID,
		RepoOwner: "acme", RepoName: "widget", SubjectType: "PullRequest", SubjectTitle: "Closed PR",
		WebURL: "https://github.com/acme/widget/pull/42", ItemNumber: &prNumber, ItemType: "pr",
		Reason: "mention", Unread: true, SourceUpdatedAt: now, SyncedAt: now,
	}}))
	require.NoError(database.MarkClosedLinkedNotificationsDone(t.Context(), now.Add(2*time.Hour)))
	ts := newTestNotificationServer(t, database)
	done := getNotificationsForTest(t, ts.URL, "done")
	require.Len(done.Items, 1)
	id := done.Items[0].ID

	body, err := json.Marshal(map[string]any{"ids": []int64{id}})
	require.NoError(err)
	respReq, err := http.NewRequestWithContext(t.Context(), http.MethodPost, ts.URL+"/api/v1/notifications/undone", bytes.NewReader(body))
	require.NoError(err)
	respReq.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(respReq)
	require.NoError(err)
	defer resp.Body.Close()
	require.Equal(http.StatusOK, resp.StatusCode)

	var bulk itemapi.NotificationBulkResponse
	require.NoError(json.NewDecoder(resp.Body).Decode(&bulk))
	assert.Equal([]int64{id}, bulk.Succeeded)
	assert.Empty(bulk.Failed)
	assert.Empty(getNotificationsForTest(t, ts.URL, "active").Items)
	done = getNotificationsForTest(t, ts.URL, "done")
	require.Len(done.Items, 1)
	assert.Equal(id, done.Items[0].ID)
	assert.Equal("closed", done.Items[0].DoneReason)
	assert.NotEmpty(done.Items[0].DoneAt)
}

func newTestNotificationServer(t *testing.T, database *db.DB) *httptest.Server {
	t.Helper()
	s := server.New(database, nil, nil, "/", notificationsEnabledConfig(), server.ServerOptions{})
	ts := httptest.NewServer(s)
	t.Cleanup(ts.Close)
	return ts
}

func TestNotificationsAPIUsesActiveTrackedRepos(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	database := serverfake.OpenTestDB(t)
	trackedRepoID, err := database.UpsertRepo(t.Context(), serverfake.VerifiedGitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	removedRepoID, err := database.UpsertRepo(t.Context(), serverfake.VerifiedGitHubRepoIdentity("github.com", "acme", "removed"))
	require.NoError(err)
	now := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
	require.NoError(database.UpsertNotifications(t.Context(), []db.Notification{
		{
			Platform: "github", PlatformHost: "github.com", PlatformNotificationID: "thread-tracked", RepoID: &trackedRepoID,
			RepoOwner: "acme", RepoName: "widget", SubjectType: "PullRequest", SubjectTitle: "Tracked",
			WebURL: "https://github.com/acme/widget/pull/1", Reason: "mention", Unread: true,
			SourceUpdatedAt: now, SyncedAt: now,
		},
		{
			Platform: "github", PlatformHost: "github.com", PlatformNotificationID: "thread-removed", RepoID: &removedRepoID,
			RepoOwner: "acme", RepoName: "removed", SubjectType: "PullRequest", SubjectTitle: "Removed",
			WebURL: "https://github.com/acme/removed/pull/1", Reason: "mention", Unread: true,
			SourceUpdatedAt: now, SyncedAt: now,
		},
	}))
	syncer := ghclient.NewSyncer(
		map[string]ghclient.Client{}, database, nil,
		[]ghclient.RepoRef{{Platform: "github", Owner: "acme", Name: "widget", PlatformHost: "github.com"}},
		time.Minute, nil, nil,
	)
	s := server.New(database, syncer, nil, "/", notificationsEnabledConfig(), server.ServerOptions{})
	ts := httptest.NewServer(s)
	defer ts.Close()

	listed := getNotificationsForTest(t, ts.URL, "all")
	require.Len(listed.Items, 1)
	assert.Equal("thread-tracked", listed.Items[0].PlatformThreadID)
	assert.Equal(1, listed.Summary.Unread)
	assert.Equal(map[string]int{"github.com/acme/widget": 1}, listed.Summary.ByRepo)
}

func TestNotificationsAPIAcceptsProviderAndHostQualifiedRepoFilter(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	database := serverfake.OpenTestDB(t)
	githubRepoID, err := database.UpsertRepo(t.Context(), serverfake.VerifiedGitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	gheRepoID, err := database.UpsertRepo(t.Context(), serverfake.VerifiedGitHubRepoIdentity("ghe.example.com", "acme", "widget"))
	require.NoError(err)
	now := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
	require.NoError(database.UpsertNotifications(t.Context(), []db.Notification{
		{
			Platform: "github", PlatformHost: "github.com", PlatformNotificationID: "thread-github", RepoID: &githubRepoID,
			RepoOwner: "acme", RepoName: "widget", SubjectType: "PullRequest", SubjectTitle: "GitHub",
			WebURL: "https://github.com/acme/widget/pull/1", Reason: "mention", Unread: true,
			SourceUpdatedAt: now, SyncedAt: now,
		},
		{
			Platform: "github", PlatformHost: "ghe.example.com", PlatformNotificationID: "thread-ghe", RepoID: &gheRepoID,
			RepoOwner: "acme", RepoName: "widget", SubjectType: "PullRequest", SubjectTitle: "GHE",
			WebURL: "https://ghe.example.com/acme/widget/pull/1", Reason: "mention", Unread: true,
			SourceUpdatedAt: now, SyncedAt: now,
		},
	}))
	syncer := ghclient.NewSyncer(
		map[string]ghclient.Client{}, database, nil,
		[]ghclient.RepoRef{
			{Platform: "github", Owner: "acme", Name: "widget", PlatformHost: "github.com"},
			{Platform: "github", Owner: "acme", Name: "widget", PlatformHost: "ghe.example.com"},
		},
		time.Minute, nil, nil,
	)
	s := server.New(database, syncer, nil, "/", notificationsEnabledConfig(), server.ServerOptions{})
	ts := httptest.NewServer(s)
	defer ts.Close()

	respReq, err := http.NewRequestWithContext(t.Context(), http.MethodGet, ts.URL+"/api/v1/notifications?state=all&repo=github|ghe.example.com/acme/widget", nil)
	require.NoError(err)
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(respReq)
	require.NoError(err)
	defer resp.Body.Close()
	require.Equal(http.StatusOK, resp.StatusCode)
	var listed itemapi.NotificationsResponse
	require.NoError(json.NewDecoder(resp.Body).Decode(&listed))
	require.Len(listed.Items, 1)
	assert.Equal("thread-ghe", listed.Items[0].PlatformThreadID)
	assert.Equal(map[string]int{"ghe.example.com/acme/widget": 1}, listed.Summary.ByRepo)
}

func TestNotificationsAPIExposesReadPropagationStatus(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	database := serverfake.OpenTestDB(t)
	id := serverfake.SeedServerNotification(t, database)
	s := server.New(database, nil, nil, "/", notificationsEnabledConfig(), server.ServerOptions{})
	ts := httptest.NewServer(s)
	defer ts.Close()

	body, err := json.Marshal(map[string]any{"ids": []int64{id}})
	require.NoError(err)
	markRespReq, err := http.NewRequestWithContext(t.Context(), http.MethodPost, ts.URL+"/api/v1/notifications/read", bytes.NewReader(body))
	require.NoError(err)
	markRespReq.Header.Set("Content-Type", "application/json")
	markResp, err := (&http.Client{Timeout: 5 * time.Second}).Do(markRespReq)
	require.NoError(err)
	defer markResp.Body.Close()
	require.Equal(http.StatusOK, markResp.StatusCode)

	read := getNotificationsForTest(t, ts.URL, "read")
	require.Len(read.Items, 1)
	dbRead, err := database.ListNotifications(t.Context(), db.ListNotificationsOpts{State: "read"})
	require.NoError(err)
	require.Len(dbRead, 1)
	queuedAt := dbRead[0].SourceAckQueuedAt
	require.NotNil(queuedAt)
	githubUpdatedAt := dbRead[0].SourceUpdatedAt
	nextAttempt := time.Date(2026, 5, 1, 11, 0, 0, 0, time.UTC)
	require.NoError(database.MarkNotificationAckPropagationResult(t.Context(), id, queuedAt, githubUpdatedAt, nil, "temporary failure", &nextAttempt))
	read = getNotificationsForTest(t, ts.URL, "read")
	require.Len(read.Items, 1)
	assert.Equal("temporary failure", read.Items[0].GitHubReadError)
	assert.Equal(1, read.Items[0].GitHubReadAttempts)
	assert.Equal(queuedAt.UTC().Format(time.RFC3339), read.Items[0].GitHubReadQueuedAt)
	assert.NotEmpty(read.Items[0].GitHubReadLastAttemptAt)
	assert.Equal(nextAttempt.UTC().Format(time.RFC3339), read.Items[0].GitHubReadNextAttemptAt)

	syncedAt := nextAttempt.Add(time.Minute)
	require.NoError(database.MarkNotificationAckPropagationResult(t.Context(), id, queuedAt, githubUpdatedAt, &syncedAt, "", nil))
	read = getNotificationsForTest(t, ts.URL, "read")
	require.Len(read.Items, 1)
	assert.Empty(read.Items[0].GitHubReadError)
	assert.Equal(0, read.Items[0].GitHubReadAttempts)
	assert.Empty(read.Items[0].GitHubReadQueuedAt)
	assert.Equal(syncedAt.UTC().Format(time.RFC3339), read.Items[0].GitHubReadSyncedAt)
}

func TestNotificationsAPIBulkMutationsScopeToTrackedRepos(t *testing.T) {
	require := require.New(t)
	database := serverfake.OpenTestDB(t)
	trackedRepoID, err := database.UpsertRepo(t.Context(), serverfake.VerifiedGitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	removedRepoID, err := database.UpsertRepo(t.Context(), serverfake.VerifiedGitHubRepoIdentity("github.com", "acme", "removed"))
	require.NoError(err)
	now := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
	require.NoError(database.UpsertNotifications(t.Context(), []db.Notification{
		{
			Platform: "github", PlatformHost: "github.com", PlatformNotificationID: "thread-tracked", RepoID: &trackedRepoID,
			RepoOwner: "acme", RepoName: "widget", SubjectType: "PullRequest", SubjectTitle: "Tracked",
			WebURL: "https://github.com/acme/widget/pull/1", Reason: "mention", Unread: true,
			SourceUpdatedAt: now, SyncedAt: now,
		},
		{
			Platform: "github", PlatformHost: "github.com", PlatformNotificationID: "thread-removed", RepoID: &removedRepoID,
			RepoOwner: "acme", RepoName: "removed", SubjectType: "PullRequest", SubjectTitle: "Removed",
			WebURL: "https://github.com/acme/removed/pull/1", Reason: "mention", Unread: true,
			SourceUpdatedAt: now, SyncedAt: now,
		},
	}))
	syncer := ghclient.NewSyncer(
		map[string]ghclient.Client{}, database, nil,
		[]ghclient.RepoRef{{Platform: "github", Owner: "acme", Name: "widget", PlatformHost: "github.com"}},
		time.Minute, nil, nil,
	)
	s := server.New(database, syncer, nil, "/", notificationsEnabledConfig(), server.ServerOptions{})
	ts := httptest.NewServer(s)
	defer ts.Close()
	listed := getNotificationsForTest(t, ts.URL, "all")
	require.Len(listed.Items, 1)
	trackedID := listed.Items[0].ID
	allItems, err := database.ListNotifications(t.Context(), db.ListNotificationsOpts{State: "all"})
	require.NoError(err)
	require.Len(allItems, 2)
	var removedID int64
	for _, item := range allItems {
		if item.PlatformNotificationID == "thread-removed" {
			removedID = item.ID
		}
	}
	require.NotZero(removedID)

	body, err := json.Marshal(map[string]any{"ids": []int64{trackedID, removedID}})
	require.NoError(err)
	respReq, err := http.NewRequestWithContext(t.Context(), http.MethodPost, ts.URL+"/api/v1/notifications/read", bytes.NewReader(body))
	require.NoError(err)
	respReq.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(respReq)
	require.NoError(err)
	defer resp.Body.Close()
	require.Equal(http.StatusOK, resp.StatusCode)

	var bulk itemapi.NotificationBulkResponse
	require.NoError(json.NewDecoder(resp.Body).Decode(&bulk))
	check := assert.New(t)
	check.Equal([]int64{trackedID}, bulk.Succeeded)
	check.Equal([]int64{trackedID}, bulk.Queued)
	check.Equal([]itemapi.NotificationBulkFailure{{ID: removedID, Error: "notification not found"}}, bulk.Failed)
	removedItems, err := database.ListNotifications(t.Context(), db.ListNotificationsOpts{State: "unread", PlatformHost: "github.com", RepoOwner: "acme", RepoName: "removed"})
	require.NoError(err)
	require.Len(removedItems, 1)
	check.Nil(removedItems[0].SourceAckQueuedAt)
}

func TestNotificationsAPIBulkReportsMissingIDs(t *testing.T) {
	cases := []struct {
		name       string
		path       string
		body       func(id int64, missingID int64) map[string]any
		setup      func(context.Context, *db.DB, int64, time.Time) error
		verify     func(context.Context, *assert.Assertions, *db.DB, int64)
		wantQueued bool
	}{
		{
			name: "read",
			path: "/api/v1/notifications/read",
			body: func(id int64, missingID int64) map[string]any {
				return map[string]any{"ids": []int64{id, missingID}}
			},
			verify: func(ctx context.Context, assert *assert.Assertions, database *db.DB, id int64) {
				items, err := database.ListNotifications(ctx, db.ListNotificationsOpts{State: "read"})
				require.NoError(t, err)
				require.Len(t, items, 1)
				assert.Equal(id, items[0].ID)
				assert.Nil(items[0].DoneAt)
				assert.NotNil(items[0].SourceAckQueuedAt)
			},
			wantQueued: true,
		},
		{
			name: "done",
			path: "/api/v1/notifications/done",
			body: func(id int64, missingID int64) map[string]any {
				return map[string]any{"ids": []int64{id, missingID}}
			},
			verify: func(ctx context.Context, assert *assert.Assertions, database *db.DB, id int64) {
				items, err := database.ListNotifications(ctx, db.ListNotificationsOpts{State: "done"})
				require.NoError(t, err)
				require.Len(t, items, 1)
				assert.Equal(id, items[0].ID)
				assert.NotNil(items[0].DoneAt)
				assert.NotNil(items[0].SourceAckQueuedAt)
			},
			wantQueued: true,
		},
		{
			name: "done without read",
			path: "/api/v1/notifications/done",
			body: func(id int64, missingID int64) map[string]any {
				return map[string]any{"ids": []int64{id, missingID}, "mark_read": false}
			},
			verify: func(ctx context.Context, assert *assert.Assertions, database *db.DB, id int64) {
				items, err := database.ListNotifications(ctx, db.ListNotificationsOpts{State: "done"})
				require.NoError(t, err)
				require.Len(t, items, 1)
				assert.Equal(id, items[0].ID)
				assert.NotNil(items[0].DoneAt)
				assert.Nil(items[0].SourceAckQueuedAt)
			},
		},
		{
			name: "undone",
			path: "/api/v1/notifications/undone",
			body: func(id int64, missingID int64) map[string]any {
				return map[string]any{"ids": []int64{id, missingID}}
			},
			setup: func(ctx context.Context, database *db.DB, id int64, now time.Time) error {
				_, err := database.MarkNotificationsDone(ctx, []int64{id}, now, false)
				return err
			},
			verify: func(ctx context.Context, assert *assert.Assertions, database *db.DB, id int64) {
				items, err := database.ListNotifications(ctx, db.ListNotificationsOpts{State: "unread"})
				require.NoError(t, err)
				require.Len(t, items, 1)
				assert.Equal(id, items[0].ID)
				assert.Nil(items[0].DoneAt)
				assert.Empty(items[0].DoneReason)
			},
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			require := require.New(t)
			database := serverfake.OpenTestDB(t)
			id := serverfake.SeedServerNotification(t, database)
			missingID := id + 999
			if tt.setup != nil {
				require.NoError(tt.setup(t.Context(), database, id, time.Date(2026, 5, 1, 10, 30, 0, 0, time.UTC)))
			}
			s := server.New(database, nil, nil, "/", notificationsEnabledConfig(), server.ServerOptions{})
			ts := httptest.NewServer(s)
			defer ts.Close()

			body, err := json.Marshal(tt.body(id, missingID))
			require.NoError(err)
			respReq, err := http.NewRequestWithContext(t.Context(), http.MethodPost, ts.URL+tt.path, bytes.NewReader(body))
			require.NoError(err)
			respReq.Header.Set("Content-Type", "application/json")
			resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(respReq)
			require.NoError(err)
			defer resp.Body.Close()
			require.Equal(http.StatusOK, resp.StatusCode)

			var bulk itemapi.NotificationBulkResponse
			require.NoError(json.NewDecoder(resp.Body).Decode(&bulk))
			assert := assert.New(t)
			assert.Equal([]int64{id}, bulk.Succeeded)
			if tt.wantQueued {
				assert.Equal([]int64{id}, bulk.Queued)
			} else {
				assert.Empty(bulk.Queued)
			}
			assert.Equal([]itemapi.NotificationBulkFailure{{ID: missingID, Error: "notification not found"}}, bulk.Failed)
			tt.verify(t.Context(), assert, database, id)
		})
	}
}

func TestNotificationsAPIRouteFieldsFollowRepositoryRename(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	database := serverfake.OpenTestDB(t)
	now := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
	_, _, err := database.ReconcileRepositoryObservation(t.Context(), db.RepoIdentity{
		Platform: "github", PlatformHost: "github.com",
		PlatformRepoID: "R_widget", Owner: "acme", Name: "widget",
	}, now)
	require.NoError(err)
	number := 42
	require.NoError(database.UpsertNotifications(t.Context(), []db.Notification{{
		Platform:               "github",
		PlatformHost:           "github.com",
		PlatformNotificationID: "thread-rename",
		RepoOwner:              "acme",
		RepoName:               "widget",
		SubjectType:            "PullRequest",
		SubjectTitle:           "Review requested",
		WebURL:                 "https://github.com/acme/widget/pull/42",
		ItemNumber:             &number,
		ItemType:               "pr",
		ItemAuthor:             "octocat",
		Reason:                 "review_requested",
		Unread:                 true,
		Participating:          true,
		SourceUpdatedAt:        now,
		SyncedAt:               now,
	}}))
	_, _, err = database.ReconcileRepositoryObservation(t.Context(), db.RepoIdentity{
		Platform: "github", PlatformHost: "github.com",
		PlatformRepoID: "R_widget", Owner: "acme", Name: "gadget",
	}, now.Add(time.Hour))
	require.NoError(err)

	s := server.New(database, nil, nil, "/", notificationsEnabledConfig(), server.ServerOptions{})
	ts := httptest.NewServer(s)
	defer ts.Close()

	respReq, err := http.NewRequestWithContext(t.Context(), http.MethodGet, ts.URL+"/api/v1/notifications?state=unread", nil)
	require.NoError(err)
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(respReq)
	require.NoError(err)
	defer resp.Body.Close()
	require.Equal(http.StatusOK, resp.StatusCode)
	var listed itemapi.NotificationsResponse
	require.NoError(json.NewDecoder(resp.Body).Decode(&listed))
	require.Len(listed.Items, 1)
	assert.Equal("acme", listed.Items[0].RepoOwner)
	assert.Equal("gadget", listed.Items[0].RepoName)
	assert.Equal("acme/gadget", listed.Items[0].RepoPath)
	assert.Equal(1, listed.Summary.ByRepo["github.com/acme/gadget"],
		"summary must group by the current route")
	assert.NotContains(listed.Summary.ByRepo, "github.com/acme/widget")
}
