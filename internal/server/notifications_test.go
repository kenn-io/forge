package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/middleman/internal/config"
	"go.kenn.io/middleman/internal/db"
	ghclient "go.kenn.io/middleman/internal/github"
)

func seedServerNotification(t *testing.T, database *db.DB) int64 {
	t.Helper()
	require := require.New(t)
	repoID, err := database.UpsertRepo(t.Context(), db.GitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	number := 42
	now := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
	require.NoError(database.UpsertNotifications(t.Context(), []db.Notification{{
		Platform:               "github",
		PlatformHost:           "github.com",
		PlatformNotificationID: "thread-42",
		RepoID:                 &repoID,
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
	items, err := database.ListNotifications(t.Context(), db.ListNotificationsOpts{State: "unread"})
	require.NoError(err)
	require.Len(items, 1)
	return items[0].ID
}

func notificationsEnabledConfig() *config.Config {
	// Notifications are always on; a non-nil config is all the server
	// needs to serve the notification APIs.
	return &config.Config{}
}

func TestNotificationsAPIListsAndQueuesReadWithoutDone(t *testing.T) {
	require := require.New(t)
	database := openTestDB(t)
	id := seedServerNotification(t, database)
	s := New(database, nil, nil, "/", notificationsEnabledConfig(), ServerOptions{})
	ts := httptest.NewServer(s)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/v1/notifications?state=unread")
	require.NoError(err)
	defer resp.Body.Close()
	require.Equal(http.StatusOK, resp.StatusCode)
	var listed notificationsResponse
	require.NoError(json.NewDecoder(resp.Body).Decode(&listed))
	require.Len(listed.Items, 1)
	check := assert.New(t)
	check.Equal("review_requested", listed.Items[0].Reason)
	check.Equal("pr", listed.Items[0].ItemType)
	check.Equal("github", listed.Items[0].Provider)
	check.Equal("acme/widget", listed.Items[0].RepoPath)

	body, err := json.Marshal(map[string]any{"ids": []int64{id}})
	require.NoError(err)
	markResp, err := http.Post(ts.URL+"/api/v1/notifications/read", "application/json", bytes.NewReader(body))
	require.NoError(err)
	defer markResp.Body.Close()
	require.Equal(http.StatusOK, markResp.StatusCode)
	var bulk notificationBulkResponse
	require.NoError(json.NewDecoder(markResp.Body).Decode(&bulk))
	check.Equal([]int64{id}, bulk.Succeeded)
	check.Equal([]int64{id}, bulk.Queued)

	readItems, err := database.ListNotifications(t.Context(), db.ListNotificationsOpts{State: "read"})
	require.NoError(err)
	require.Len(readItems, 1)
	check.Nil(readItems[0].DoneAt)
	check.NotNil(readItems[0].SourceAckQueuedAt)
}

func TestNotificationRepoFiltersRejectBlankProvider(t *testing.T) {
	require := require.New(t)

	_, err := notificationRepoFilters([]ghclient.RepoRef{{
		PlatformHost: "github.com",
		Owner:        "acme",
		Name:         "widget",
	}})
	require.ErrorContains(err, "notification repo provider is required")
}

func TestToNotificationResponseRejectsBlankProvider(t *testing.T) {
	require := require.New(t)
	s := &Server{}
	_, err := s.toNotificationResponse(t.Context(), db.Notification{
		PlatformHost:           "github.com",
		PlatformNotificationID: "thread-42",
		RepoOwner:              "acme",
		RepoName:               "widget",
		SubjectType:            "PullRequest",
		SubjectTitle:           "Review requested",
		Reason:                 "mention",
		Unread:                 true,
		Participating:          true,
		SourceUpdatedAt:        time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC),
		SyncedAt:               time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC),
	}, map[int64]*db.Repo{})
	require.ErrorContains(err, "notification provider is required")
}

func TestNotificationsAPIMapsNeutralFieldsToExistingGitHubJSON(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	database := openTestDB(t)
	repoID, err := database.UpsertRepo(t.Context(), db.GitHubRepoIdentity("github.com", "acme", "widget"))
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

	s := New(database, nil, nil, "/", notificationsEnabledConfig(), ServerOptions{})
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

func getNotificationsForTest(t *testing.T, baseURL string, state string) notificationsResponse {
	t.Helper()
	require := require.New(t)
	resp, err := http.Get(baseURL + "/api/v1/notifications?state=" + state)
	require.NoError(err)
	defer resp.Body.Close()
	require.Equal(http.StatusOK, resp.StatusCode)
	var listed notificationsResponse
	require.NoError(json.NewDecoder(resp.Body).Decode(&listed))
	return listed
}

func TestNotificationsAPIShowsClosedLinkedItemsAsDone(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	database := openTestDB(t)
	repoID, err := database.UpsertRepo(t.Context(), db.GitHubRepoIdentity("github.com", "acme", "widget"))
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
	s := New(database, nil, nil, "/", notificationsEnabledConfig(), ServerOptions{})
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
	check := assert.New(t)
	database := openTestDB(t)
	repoID, err := database.UpsertRepo(t.Context(), db.GitHubRepoIdentity("github.com", "acme", "widget"))
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
	resp, err := http.Post(ts.URL+"/api/v1/notifications/undone", "application/json", bytes.NewReader(body))
	require.NoError(err)
	defer resp.Body.Close()
	require.Equal(http.StatusOK, resp.StatusCode)

	var bulk notificationBulkResponse
	require.NoError(json.NewDecoder(resp.Body).Decode(&bulk))
	check.Equal([]int64{id}, bulk.Succeeded)
	check.Empty(bulk.Failed)
	check.Empty(getNotificationsForTest(t, ts.URL, "active").Items)
	done = getNotificationsForTest(t, ts.URL, "done")
	require.Len(done.Items, 1)
	check.Equal(id, done.Items[0].ID)
	check.Equal("closed", done.Items[0].DoneReason)
	check.NotEmpty(done.Items[0].DoneAt)
}

func newTestNotificationServer(t *testing.T, database *db.DB) *httptest.Server {
	t.Helper()
	s := New(database, nil, nil, "/", notificationsEnabledConfig(), ServerOptions{})
	ts := httptest.NewServer(s)
	t.Cleanup(ts.Close)
	return ts
}

func TestNotificationsAPIUsesActiveTrackedRepos(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	database := openTestDB(t)
	trackedRepoID, err := database.UpsertRepo(t.Context(), db.GitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	removedRepoID, err := database.UpsertRepo(t.Context(), db.GitHubRepoIdentity("github.com", "acme", "removed"))
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
	s := New(database, syncer, nil, "/", notificationsEnabledConfig(), ServerOptions{})
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
	database := openTestDB(t)
	githubRepoID, err := database.UpsertRepo(t.Context(), db.GitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	gheRepoID, err := database.UpsertRepo(t.Context(), db.GitHubRepoIdentity("ghe.example.com", "acme", "widget"))
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
	s := New(database, syncer, nil, "/", notificationsEnabledConfig(), ServerOptions{})
	ts := httptest.NewServer(s)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/v1/notifications?state=all&repo=github|ghe.example.com/acme/widget")
	require.NoError(err)
	defer resp.Body.Close()
	require.Equal(http.StatusOK, resp.StatusCode)
	var listed notificationsResponse
	require.NoError(json.NewDecoder(resp.Body).Decode(&listed))
	require.Len(listed.Items, 1)
	assert.Equal("thread-ghe", listed.Items[0].PlatformThreadID)
	assert.Equal(map[string]int{"ghe.example.com/acme/widget": 1}, listed.Summary.ByRepo)
}

func TestNotificationsAPIExposesBackgroundSyncStatus(t *testing.T) {
	require := require.New(t)
	database := openTestDB(t)
	syncer := ghclient.NewSyncer(
		map[string]ghclient.Client{
			"github.com": &mockGH{
				listNotificationsFn: func(context.Context, ghclient.NotificationListOptions) ([]ghclient.NotificationThread, bool, error) {
					return nil, false, errors.New("notification API unavailable")
				},
			},
		},
		database, nil,
		[]ghclient.RepoRef{{Platform: "github", Owner: "acme", Name: "widget", PlatformHost: "github.com"}},
		time.Minute, nil, nil,
	)
	s := New(database, syncer, nil, "/", notificationsEnabledConfig(), ServerOptions{})
	ts := httptest.NewServer(s)
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/api/v1/notifications/sync", "application/json", nil)
	require.NoError(err)
	defer resp.Body.Close()
	require.Equal(http.StatusAccepted, resp.StatusCode)

	require.Eventually(func() bool {
		resp, err := http.Get(ts.URL + "/api/v1/notifications?state=all")
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return false
		}
		var body struct {
			Sync struct {
				Running   bool   `json:"running"`
				LastError string `json:"last_error"`
			} `json:"sync"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			return false
		}
		return !body.Sync.Running && strings.Contains(body.Sync.LastError, "notification API unavailable")
	}, 2*time.Second, 20*time.Millisecond)
}

func TestGlobalSyncExposesNotificationSyncFailure(t *testing.T) {
	require := require.New(t)
	database := openTestDB(t)
	syncer := ghclient.NewSyncer(
		map[string]ghclient.Client{
			"github.com": &mockGH{
				listNotificationsFn: func(context.Context, ghclient.NotificationListOptions) ([]ghclient.NotificationThread, bool, error) {
					return nil, false, errors.New("notification API unavailable")
				},
			},
		},
		database, nil,
		[]ghclient.RepoRef{{Platform: "github", Owner: "acme", Name: "widget", PlatformHost: "github.com"}},
		time.Minute, nil, nil,
	)
	s := New(database, syncer, nil, "/", notificationsEnabledConfig(), ServerOptions{})
	t.Cleanup(func() { gracefulShutdown(t, s) })
	ts := httptest.NewServer(s)
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/api/v1/sync", "application/json", nil)
	require.NoError(err)
	defer resp.Body.Close()
	require.Equal(http.StatusAccepted, resp.StatusCode)
	require.Eventually(func() bool {
		resp, err := http.Get(ts.URL + "/api/v1/notifications?state=all")
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return false
		}
		var body struct {
			Sync struct {
				Running   bool   `json:"running"`
				LastError string `json:"last_error"`
			} `json:"sync"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			return false
		}
		return !body.Sync.Running && strings.Contains(body.Sync.LastError, "notification API unavailable")
	}, 2*time.Second, 20*time.Millisecond)
}

func TestNotificationsAPIExposesReadPropagationStatus(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	database := openTestDB(t)
	id := seedServerNotification(t, database)
	s := New(database, nil, nil, "/", notificationsEnabledConfig(), ServerOptions{})
	ts := httptest.NewServer(s)
	defer ts.Close()

	body, err := json.Marshal(map[string]any{"ids": []int64{id}})
	require.NoError(err)
	markResp, err := http.Post(ts.URL+"/api/v1/notifications/read", "application/json", bytes.NewReader(body))
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

func TestNotificationsAPIRejectsNilConfigAccess(t *testing.T) {
	require := require.New(t)
	database := openTestDB(t)
	id := seedServerNotification(t, database)
	s := New(database, nil, nil, "/", nil, ServerOptions{})
	ts := httptest.NewServer(s)
	defer ts.Close()

	body, err := json.Marshal(map[string]any{"ids": []int64{id}})
	require.NoError(err)
	resp, err := http.Post(ts.URL+"/api/v1/notifications/read", "application/json", bytes.NewReader(body))
	require.NoError(err)
	defer resp.Body.Close()
	require.Equal(http.StatusForbidden, resp.StatusCode)
}

func TestNotificationsAPIBulkMutationsScopeToTrackedRepos(t *testing.T) {
	require := require.New(t)
	database := openTestDB(t)
	trackedRepoID, err := database.UpsertRepo(t.Context(), db.GitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	removedRepoID, err := database.UpsertRepo(t.Context(), db.GitHubRepoIdentity("github.com", "acme", "removed"))
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
	s := New(database, syncer, nil, "/", notificationsEnabledConfig(), ServerOptions{})
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
	resp, err := http.Post(ts.URL+"/api/v1/notifications/read", "application/json", bytes.NewReader(body))
	require.NoError(err)
	defer resp.Body.Close()
	require.Equal(http.StatusOK, resp.StatusCode)

	var bulk notificationBulkResponse
	require.NoError(json.NewDecoder(resp.Body).Decode(&bulk))
	check := assert.New(t)
	check.Equal([]int64{trackedID}, bulk.Succeeded)
	check.Equal([]int64{trackedID}, bulk.Queued)
	check.Equal([]notificationBulkFailure{{ID: removedID, Error: "notification not found"}}, bulk.Failed)
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
			verify: func(ctx context.Context, check *assert.Assertions, database *db.DB, id int64) {
				items, err := database.ListNotifications(ctx, db.ListNotificationsOpts{State: "read"})
				require.NoError(t, err)
				require.Len(t, items, 1)
				check.Equal(id, items[0].ID)
				check.Nil(items[0].DoneAt)
				check.NotNil(items[0].SourceAckQueuedAt)
			},
			wantQueued: true,
		},
		{
			name: "done",
			path: "/api/v1/notifications/done",
			body: func(id int64, missingID int64) map[string]any {
				return map[string]any{"ids": []int64{id, missingID}}
			},
			verify: func(ctx context.Context, check *assert.Assertions, database *db.DB, id int64) {
				items, err := database.ListNotifications(ctx, db.ListNotificationsOpts{State: "done"})
				require.NoError(t, err)
				require.Len(t, items, 1)
				check.Equal(id, items[0].ID)
				check.NotNil(items[0].DoneAt)
				check.NotNil(items[0].SourceAckQueuedAt)
			},
			wantQueued: true,
		},
		{
			name: "done without read",
			path: "/api/v1/notifications/done",
			body: func(id int64, missingID int64) map[string]any {
				return map[string]any{"ids": []int64{id, missingID}, "mark_read": false}
			},
			verify: func(ctx context.Context, check *assert.Assertions, database *db.DB, id int64) {
				items, err := database.ListNotifications(ctx, db.ListNotificationsOpts{State: "done"})
				require.NoError(t, err)
				require.Len(t, items, 1)
				check.Equal(id, items[0].ID)
				check.NotNil(items[0].DoneAt)
				check.Nil(items[0].SourceAckQueuedAt)
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
			verify: func(ctx context.Context, check *assert.Assertions, database *db.DB, id int64) {
				items, err := database.ListNotifications(ctx, db.ListNotificationsOpts{State: "unread"})
				require.NoError(t, err)
				require.Len(t, items, 1)
				check.Equal(id, items[0].ID)
				check.Nil(items[0].DoneAt)
				check.Empty(items[0].DoneReason)
			},
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			require := require.New(t)
			database := openTestDB(t)
			id := seedServerNotification(t, database)
			missingID := id + 999
			if tt.setup != nil {
				require.NoError(tt.setup(t.Context(), database, id, time.Date(2026, 5, 1, 10, 30, 0, 0, time.UTC)))
			}
			s := New(database, nil, nil, "/", notificationsEnabledConfig(), ServerOptions{})
			ts := httptest.NewServer(s)
			defer ts.Close()

			body, err := json.Marshal(tt.body(id, missingID))
			require.NoError(err)
			resp, err := http.Post(ts.URL+tt.path, "application/json", bytes.NewReader(body))
			require.NoError(err)
			defer resp.Body.Close()
			require.Equal(http.StatusOK, resp.StatusCode)

			var bulk notificationBulkResponse
			require.NoError(json.NewDecoder(resp.Body).Decode(&bulk))
			check := assert.New(t)
			check.Equal([]int64{id}, bulk.Succeeded)
			if tt.wantQueued {
				check.Equal([]int64{id}, bulk.Queued)
			} else {
				check.Empty(bulk.Queued)
			}
			check.Equal([]notificationBulkFailure{{ID: missingID, Error: "notification not found"}}, bulk.Failed)
			tt.verify(t.Context(), check, database, id)
		})
	}
}
