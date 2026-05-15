package db

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func seedNotificationRepo(t *testing.T, d *DB) int64 {
	t.Helper()
	require := require.New(t)
	repoID, err := d.UpsertRepo(t.Context(), GitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	return repoID
}

func notificationFixture(threadID, reason string, updated time.Time) Notification {
	number := 7
	return Notification{
		Platform:               "github",
		PlatformHost:           "github.com",
		PlatformNotificationID: threadID,
		RepoOwner:              "acme",
		RepoName:               "widget",
		SubjectType:            "PullRequest",
		SubjectTitle:           "Please review the widget",
		WebURL:                 "https://github.com/acme/widget/pull/7",
		ItemNumber:             &number,
		ItemType:               "pr",
		ItemAuthor:             "octocat",
		Reason:                 reason,
		Unread:                 true,
		Participating:          true,
		SourceUpdatedAt:        updated,
		SyncedAt:               updated,
	}
}

func TestNotificationsListFiltersSearchAndPriority(t *testing.T) {
	require := require.New(t)
	d := openTestDB(t)
	seedNotificationRepo(t, d)
	now := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
	doneAt := now.Add(-time.Hour)
	notifications := []Notification{
		notificationFixture("comment", "comment", now.Add(-time.Minute)),
		notificationFixture("mention", "mention", now.Add(-2*time.Minute)),
		notificationFixture("review", "review_requested", now.Add(-3*time.Minute)),
		notificationFixture("done", "mention", now.Add(-4*time.Minute)),
	}
	notifications[3].DoneAt = &doneAt
	notifications[3].DoneReason = "user"
	require.NoError(d.UpsertNotifications(t.Context(), notifications))

	items, err := d.ListNotifications(t.Context(), ListNotificationsOpts{State: "unread", Sort: "priority"})
	require.NoError(err)
	require.Len(items, 3)
	check := assert.New(t)
	check.Equal("mention", items[0].PlatformNotificationID)
	check.Equal("review", items[1].PlatformNotificationID)
	check.Equal("comment", items[2].PlatformNotificationID)

	items, err = d.ListNotifications(t.Context(), ListNotificationsOpts{State: "done"})
	require.NoError(err)
	require.Len(items, 1)
	check.Equal("done", items[0].PlatformNotificationID)

	notifications[1].SubjectTitle = "Needle migration plan"
	require.NoError(d.UpsertNotifications(t.Context(), []Notification{notifications[1]}))
	items, err = d.ListNotifications(t.Context(), ListNotificationsOpts{State: "all", Search: "needle"})
	require.NoError(err)
	require.Len(items, 1)
	check.Equal("mention", items[0].PlatformNotificationID)
}

func TestNotificationSummaryIgnoresListState(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	d := openTestDB(t)
	seedNotificationRepo(t, d)
	now := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
	doneAt := now.Add(-time.Hour)
	unread := notificationFixture("unread", "mention", now)
	read := notificationFixture("read", "comment", now.Add(-time.Minute))
	read.Unread = false
	done := notificationFixture("done", "review_requested", now.Add(-2*time.Minute))
	done.DoneAt = &doneAt
	done.DoneReason = "user"
	require.NoError(d.UpsertNotifications(t.Context(), []Notification{unread, read, done}))

	summary, err := d.NotificationSummary(t.Context(), ListNotificationsOpts{State: "unread"})
	require.NoError(err)
	assert.Equal(2, summary.TotalActive)
	assert.Equal(1, summary.Unread)
	assert.Equal(1, summary.Done)
	assert.Equal(map[string]int{
		"mention":          1,
		"comment":          1,
		"review_requested": 1,
	}, summary.ByReason)
}

func TestNotificationsReadQueuesWithoutDone(t *testing.T) {
	require := require.New(t)
	d := openTestDB(t)
	seedNotificationRepo(t, d)
	now := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
	require.NoError(d.UpsertNotifications(t.Context(), []Notification{notificationFixture("mention", "mention", now)}))
	items, err := d.ListNotifications(t.Context(), ListNotificationsOpts{State: "unread"})
	require.NoError(err)
	require.Len(items, 1)

	readAt := now.Add(time.Minute)
	queued, err := d.QueueNotificationIDsRead(t.Context(), []int64{items[0].ID}, readAt)
	require.NoError(err)
	assert.Equal(t, []int64{items[0].ID}, queued)

	readItems, err := d.ListNotifications(t.Context(), ListNotificationsOpts{State: "read"})
	require.NoError(err)
	require.Len(readItems, 1)
	check := assert.New(t)
	check.Nil(readItems[0].DoneAt)
	check.Nil(readItems[0].SourceLastAcknowledgedAt)
	if check.NotNil(readItems[0].SourceAckQueuedAt) {
		check.True(readAt.Equal(*readItems[0].SourceAckQueuedAt))
	}

	doneItems, err := d.ListNotifications(t.Context(), ListNotificationsOpts{State: "done"})
	require.NoError(err)
	check.Empty(doneItems)
}

func TestMarkNotificationsAcknowledgedScopesThreadIDsToPlatformHost(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	d := openTestDB(t)
	seedNotificationRepo(t, d)
	repoID, err := d.UpsertRepo(t.Context(), GitHubRepoIdentity("ghe.example.com", "acme", "widget"))
	require.NoError(err)
	now := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
	githubNotification := notificationFixture("shared-thread", "mention", now)
	gheNotification := notificationFixture("shared-thread", "mention", now)
	gheNotification.PlatformHost = "ghe.example.com"
	gheNotification.RepoID = &repoID
	require.NoError(d.UpsertNotifications(t.Context(), []Notification{githubNotification, gheNotification}))

	readAt := now.Add(time.Minute)
	require.NoError(d.MarkNotificationsAcknowledged(t.Context(), "github", "github.com", []string{"shared-thread"}, readAt))

	items, err := d.ListNotifications(t.Context(), ListNotificationsOpts{State: "all"})
	require.NoError(err)
	require.Len(items, 2)
	readByHost := map[string]bool{}
	for _, item := range items {
		readByHost[item.PlatformHost] = !item.Unread
	}
	assert.True(readByHost["github.com"])
	assert.False(readByHost["ghe.example.com"])
}

func TestNotificationsQueueReadPropagation(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	d := openTestDB(t)
	seedNotificationRepo(t, d)
	now := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
	require.NoError(d.UpsertNotifications(t.Context(), []Notification{notificationFixture("mention", "mention", now)}))
	items, err := d.ListNotifications(t.Context(), ListNotificationsOpts{State: "unread"})
	require.NoError(err)
	require.Len(items, 1)

	done, err := d.MarkNotificationsDone(t.Context(), []int64{items[0].ID}, now.Add(time.Minute), true)
	require.NoError(err)
	assert.Equal([]int64{items[0].ID}, done)
	queued, err := d.ListQueuedNotificationAcks(t.Context(), "github", "github.com", 10, now.Add(2*time.Minute))
	require.NoError(err)
	require.Len(queued, 1)
	assert.Equal("mention", queued[0].PlatformNotificationID)

	next := now.Add(10 * time.Minute)
	require.NoError(d.MarkNotificationAckPropagationResult(t.Context(), queued[0].ID, queued[0].SourceAckQueuedAt, queued[0].SourceUpdatedAt, nil, "rate limited", &next))
	queued, err = d.ListQueuedNotificationAcks(t.Context(), "github", "github.com", 10, now.Add(2*time.Minute))
	require.NoError(err)
	assert.Empty(queued)

	queued, err = d.ListQueuedNotificationAcks(t.Context(), "github", "github.com", 10, now.Add(11*time.Minute))
	require.NoError(err)
	require.Len(queued, 1)
	assert.Equal("rate limited", queued[0].SourceAckError)
	assert.Equal(1, queued[0].SourceAckAttempts)
	assert.NotNil(queued[0].SourceAckLastAttemptAt)
	if assert.NotNil(queued[0].SourceAckNextAttemptAt) {
		assert.True(next.Equal(*queued[0].SourceAckNextAttemptAt))
	}

	synced := now.Add(12 * time.Minute)
	require.NoError(d.MarkNotificationAckPropagationResult(t.Context(), queued[0].ID, queued[0].SourceAckQueuedAt, queued[0].SourceUpdatedAt, &synced, "", nil))
	queued, err = d.ListQueuedNotificationAcks(t.Context(), "github", "github.com", 10, now.Add(13*time.Minute))
	require.NoError(err)
	assert.Empty(queued)

	readItems, err := d.ListNotifications(t.Context(), ListNotificationsOpts{State: "all"})
	require.NoError(err)
	require.Len(readItems, 1)
	if assert.NotNil(readItems[0].SourceLastAcknowledgedAt) {
		assert.True(synced.Equal(*readItems[0].SourceLastAcknowledgedAt))
	}
	if assert.NotNil(readItems[0].SourceAckSyncedAt) {
		assert.True(synced.Equal(*readItems[0].SourceAckSyncedAt))
	}
	if assert.NotNil(readItems[0].SourceAckGenerationAt) {
		assert.True(now.Equal(*readItems[0].SourceAckGenerationAt))
	}
	assert.Empty(readItems[0].SourceAckError)
}

func TestReadPropagationGenerationPreservesReadStateForStaleUnreadSync(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	d := openTestDB(t)
	seedNotificationRepo(t, d)
	now := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
	require.NoError(d.UpsertNotifications(t.Context(), []Notification{notificationFixture("mention", "mention", now)}))
	items, err := d.ListNotifications(t.Context(), ListNotificationsOpts{State: "unread"})
	require.NoError(err)
	require.Len(items, 1)

	queuedAt := now.Add(time.Minute)
	queuedIDs, err := d.QueueNotificationIDsRead(t.Context(), []int64{items[0].ID}, queuedAt)
	require.NoError(err)
	require.Len(queuedIDs, 1)
	queued, err := d.ListQueuedNotificationAcks(t.Context(), "github", "github.com", 10, queuedAt)
	require.NoError(err)
	require.Len(queued, 1)

	syncedAt := queuedAt.Add(time.Minute)
	require.NoError(d.MarkNotificationAckPropagationResult(t.Context(), queued[0].ID, queued[0].SourceAckQueuedAt, queued[0].SourceUpdatedAt, &syncedAt, "", nil))
	staleUnread := notificationFixture("mention", "mention", now)
	staleUnread.Unread = true
	require.NoError(d.UpsertNotifications(t.Context(), []Notification{staleUnread}))

	read, err := d.ListNotifications(t.Context(), ListNotificationsOpts{State: "read"})
	require.NoError(err)
	require.Len(read, 1)
	assert.False(read[0].Unread)
	if assert.NotNil(read[0].SourceAckSyncedAt) {
		assert.True(syncedAt.Equal(*read[0].SourceAckSyncedAt))
	}
	if assert.NotNil(read[0].SourceAckGenerationAt) {
		assert.True(now.Equal(*read[0].SourceAckGenerationAt))
	}
	unread, err := d.ListNotifications(t.Context(), ListNotificationsOpts{State: "unread"})
	require.NoError(err)
	assert.Empty(unread)
}

func TestReadPropagationGenerationKeepsDoneWhenGitHubShowsNewerReadActivity(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	d := openTestDB(t)
	seedNotificationRepo(t, d)
	now := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
	require.NoError(d.UpsertNotifications(t.Context(), []Notification{notificationFixture("mention", "mention", now)}))
	items, err := d.ListNotifications(t.Context(), ListNotificationsOpts{State: "unread"})
	require.NoError(err)
	require.Len(items, 1)

	doneAt := now.Add(time.Minute)
	done, err := d.MarkNotificationsDone(t.Context(), []int64{items[0].ID}, doneAt, true)
	require.NoError(err)
	assert.Equal([]int64{items[0].ID}, done)
	queued, err := d.ListQueuedNotificationAcks(t.Context(), "github", "github.com", 10, doneAt)
	require.NoError(err)
	require.Len(queued, 1)

	syncedAt := doneAt.Add(time.Minute)
	require.NoError(d.MarkNotificationAckPropagationResult(t.Context(), queued[0].ID, queued[0].SourceAckQueuedAt, queued[0].SourceUpdatedAt, &syncedAt, "", nil))

	newRead := notificationFixture("mention", "mention", doneAt.Add(time.Minute))
	newRead.Unread = false
	lastReadAt := doneAt.Add(2 * time.Minute)
	newRead.SourceLastAcknowledgedAt = &lastReadAt
	require.NoError(d.UpsertNotifications(t.Context(), []Notification{newRead}))

	active, err := d.ListNotifications(t.Context(), ListNotificationsOpts{State: "active"})
	require.NoError(err)
	assert.Empty(active)

	doneItems, err := d.ListNotifications(t.Context(), ListNotificationsOpts{State: "done"})
	require.NoError(err)
	require.Len(doneItems, 1)
	assert.Equal("mention", doneItems[0].PlatformNotificationID)
	assert.False(doneItems[0].Unread)
	if assert.NotNil(doneItems[0].DoneAt) {
		assert.True(doneAt.Equal(*doneItems[0].DoneAt))
	}
	if assert.NotNil(doneItems[0].SourceAckGenerationAt) {
		assert.True(newRead.SourceUpdatedAt.Equal(*doneItems[0].SourceAckGenerationAt))
	}
}

func TestGitHubReportedReadRecordsGenerationForStaleUnreadSync(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	d := openTestDB(t)
	seedNotificationRepo(t, d)
	now := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
	reportedReadAt := now.Add(time.Minute)
	read := notificationFixture("mention", "mention", now)
	read.Unread = false
	read.SourceLastAcknowledgedAt = &reportedReadAt
	require.NoError(d.UpsertNotifications(t.Context(), []Notification{read}))

	staleUnread := notificationFixture("mention", "mention", now)
	staleUnread.Unread = true
	require.NoError(d.UpsertNotifications(t.Context(), []Notification{staleUnread}))

	readItems, err := d.ListNotifications(t.Context(), ListNotificationsOpts{State: "read"})
	require.NoError(err)
	require.Len(readItems, 1)
	assert.False(readItems[0].Unread)
	if assert.NotNil(readItems[0].SourceAckGenerationAt) {
		assert.True(now.Equal(*readItems[0].SourceAckGenerationAt))
	}
	unreadItems, err := d.ListNotifications(t.Context(), ListNotificationsOpts{State: "unread"})
	require.NoError(err)
	assert.Empty(unreadItems)
}

func TestReadPropagationFailureDoesNotMarkNewerUnreadActivityForRetry(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	d := openTestDB(t)
	seedNotificationRepo(t, d)
	now := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
	require.NoError(d.UpsertNotifications(t.Context(), []Notification{notificationFixture("mention", "mention", now)}))
	items, err := d.ListNotifications(t.Context(), ListNotificationsOpts{State: "unread"})
	require.NoError(err)
	require.Len(items, 1)

	queuedAt := now.Add(time.Minute)
	queuedIDs, err := d.QueueNotificationIDsRead(t.Context(), []int64{items[0].ID}, queuedAt)
	require.NoError(err)
	require.Len(queuedIDs, 1)
	queued, err := d.ListQueuedNotificationAcks(t.Context(), "github", "github.com", 10, queuedAt)
	require.NoError(err)
	require.Len(queued, 1)

	newUnread := notificationFixture("mention", "mention", queuedAt.Add(time.Minute))
	newUnread.Unread = true
	require.NoError(d.UpsertNotifications(t.Context(), []Notification{newUnread}))
	nextAttempt := queuedAt.Add(2 * time.Minute)
	require.NoError(d.MarkNotificationAckPropagationResult(t.Context(), queued[0].ID, queued[0].SourceAckQueuedAt, queued[0].SourceUpdatedAt, nil, "temporary failure", &nextAttempt))

	unread, err := d.ListNotifications(t.Context(), ListNotificationsOpts{State: "unread"})
	require.NoError(err)
	require.Len(unread, 1)
	assert.Empty(unread[0].SourceAckError)
	assert.Equal(0, unread[0].SourceAckAttempts)
	assert.Nil(unread[0].SourceAckNextAttemptAt)
}

func TestReadPropagationSuccessDoesNotClearNewerUnreadActivity(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	d := openTestDB(t)
	seedNotificationRepo(t, d)
	now := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
	require.NoError(d.UpsertNotifications(t.Context(), []Notification{notificationFixture("mention", "mention", now)}))
	items, err := d.ListNotifications(t.Context(), ListNotificationsOpts{State: "unread"})
	require.NoError(err)
	require.Len(items, 1)

	queuedAt := now.Add(time.Minute)
	queuedIDs, err := d.QueueNotificationIDsRead(t.Context(), []int64{items[0].ID}, queuedAt)
	require.NoError(err)
	require.Len(queuedIDs, 1)
	queued, err := d.ListQueuedNotificationAcks(t.Context(), "github", "github.com", 10, queuedAt)
	require.NoError(err)
	require.Len(queued, 1)

	newUnread := notificationFixture("mention", "mention", queuedAt.Add(time.Minute))
	newUnread.Unread = true
	require.NoError(d.UpsertNotifications(t.Context(), []Notification{newUnread}))
	syncedAt := queuedAt.Add(2 * time.Minute)
	require.NoError(d.MarkNotificationAckPropagationResult(t.Context(), queued[0].ID, queued[0].SourceAckQueuedAt, queued[0].SourceUpdatedAt, &syncedAt, "", nil))

	unread, err := d.ListNotifications(t.Context(), ListNotificationsOpts{State: "unread"})
	require.NoError(err)
	require.Len(unread, 1)
	assert.Equal("mention", unread[0].PlatformNotificationID)
	assert.Nil(unread[0].SourceAckQueuedAt)
}

func TestNotificationMutationsReturnOnlyUpdatedIDs(t *testing.T) {
	require := require.New(t)
	d := openTestDB(t)
	seedNotificationRepo(t, d)
	now := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
	require.NoError(d.UpsertNotifications(t.Context(), []Notification{notificationFixture("mention", "mention", now)}))
	items, err := d.ListNotifications(t.Context(), ListNotificationsOpts{State: "unread"})
	require.NoError(err)
	require.Len(items, 1)
	id := items[0].ID
	missingID := id + 999

	queued, err := d.QueueNotificationIDsRead(t.Context(), []int64{id, missingID}, now.Add(time.Minute))
	require.NoError(err)
	undone, err := d.MarkNotificationsUndone(t.Context(), []int64{id, missingID})
	require.NoError(err)
	done, err := d.MarkNotificationsDone(t.Context(), []int64{id, missingID}, now.Add(2*time.Minute), false)
	require.NoError(err)

	check := assert.New(t)
	check.ElementsMatch([]int64{id}, queued)
	check.ElementsMatch([]int64{id}, undone)
	check.ElementsMatch([]int64{id}, done)
}

func TestNotificationsHideUnmonitoredRepos(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	d := openTestDB(t)
	seedNotificationRepo(t, d)
	otherRepoID, err := d.UpsertRepo(t.Context(), GitHubRepoIdentity("github.com", "acme", "tools"))
	require.NoError(err)
	now := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
	tracked := notificationFixture("tracked", "mention", now)
	otherTracked := notificationFixture("other-tracked", "mention", now)
	otherTracked.RepoID = &otherRepoID
	otherTracked.RepoName = "tools"
	untracked := notificationFixture("untracked", "mention", now)
	untracked.RepoName = "removed"
	require.NoError(d.UpsertNotifications(t.Context(), []Notification{tracked, otherTracked, untracked}))

	allTracked, err := d.ListNotifications(t.Context(), ListNotificationsOpts{State: "all"})
	require.NoError(err)
	require.Len(allTracked, 2)

	trackedRepos := []NotificationRepoFilter{{
		Platform:     "github",
		PlatformHost: "github.com",
		RepoOwner:    "acme",
		RepoName:     "widget",
	}}
	items, err := d.ListNotifications(t.Context(), ListNotificationsOpts{State: "all", Repos: trackedRepos})
	require.NoError(err)
	require.Len(items, 1)
	assert.Equal("tracked", items[0].PlatformNotificationID)

	summary, err := d.NotificationSummary(t.Context(), ListNotificationsOpts{State: "all", Repos: trackedRepos})
	require.NoError(err)
	assert.Equal(1, summary.Unread)
	assert.Equal(map[string]int{"github.com/acme/widget": 1}, summary.ByRepo)
}

func TestNotificationSummaryRepoFacetsIncludePlatformHost(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	d := openTestDB(t)
	seedNotificationRepo(t, d)
	repoID, err := d.UpsertRepo(t.Context(), GitHubRepoIdentity("ghe.example.com", "acme", "widget"))
	require.NoError(err)
	now := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
	githubNotification := notificationFixture("github", "mention", now)
	gheNotification := notificationFixture("ghe", "mention", now)
	gheNotification.PlatformHost = "ghe.example.com"
	gheNotification.RepoID = &repoID
	require.NoError(d.UpsertNotifications(t.Context(), []Notification{githubNotification, gheNotification}))

	summary, err := d.NotificationSummary(t.Context(), ListNotificationsOpts{State: "all"})
	require.NoError(err)
	assert.Equal(map[string]int{
		"github.com/acme/widget":      1,
		"ghe.example.com/acme/widget": 1,
	}, summary.ByRepo)
}

func TestUpsertNotificationsPreservesQueuedReadUntilNewerActivity(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	d := openTestDB(t)
	seedNotificationRepo(t, d)
	now := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
	require.NoError(d.UpsertNotifications(t.Context(), []Notification{notificationFixture("mention", "mention", now)}))
	items, err := d.ListNotifications(t.Context(), ListNotificationsOpts{State: "unread"})
	require.NoError(err)
	require.Len(items, 1)

	queuedAt := now.Add(time.Minute)
	queued, err := d.QueueNotificationIDsRead(t.Context(), []int64{items[0].ID}, queuedAt)
	require.NoError(err)
	assert.Equal([]int64{items[0].ID}, queued)

	staleUnread := notificationFixture("mention", "mention", now)
	staleUnread.Unread = true
	require.NoError(d.UpsertNotifications(t.Context(), []Notification{staleUnread}))
	readItems, err := d.ListNotifications(t.Context(), ListNotificationsOpts{State: "read"})
	require.NoError(err)
	require.Len(readItems, 1)
	if assert.NotNil(readItems[0].SourceAckQueuedAt) {
		assert.True(queuedAt.Equal(*readItems[0].SourceAckQueuedAt))
	}
	unreadItems, err := d.ListNotifications(t.Context(), ListNotificationsOpts{State: "unread"})
	require.NoError(err)
	assert.Empty(unreadItems)

	newUnread := notificationFixture("mention", "mention", queuedAt.Add(time.Minute))
	newUnread.Unread = true
	require.NoError(d.UpsertNotifications(t.Context(), []Notification{newUnread}))
	unreadItems, err = d.ListNotifications(t.Context(), ListNotificationsOpts{State: "unread"})
	require.NoError(err)
	require.Len(unreadItems, 1)
	assert.Nil(unreadItems[0].SourceAckQueuedAt)
}

func TestUpsertNotificationsClearsQueuedReadForActivityAfterQueuedGeneration(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	d := openTestDB(t)
	seedNotificationRepo(t, d)
	now := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
	require.NoError(d.UpsertNotifications(t.Context(), []Notification{notificationFixture("mention", "mention", now)}))
	items, err := d.ListNotifications(t.Context(), ListNotificationsOpts{State: "unread"})
	require.NoError(err)
	require.Len(items, 1)

	queuedAt := now.Add(time.Minute)
	queued, err := d.QueueNotificationIDsRead(t.Context(), []int64{items[0].ID}, queuedAt)
	require.NoError(err)
	assert.Equal([]int64{items[0].ID}, queued)

	newUnread := notificationFixture("mention", "mention", now.Add(30*time.Second))
	newUnread.Unread = true
	require.NoError(d.UpsertNotifications(t.Context(), []Notification{newUnread}))

	unreadItems, err := d.ListNotifications(t.Context(), ListNotificationsOpts{State: "unread"})
	require.NoError(err)
	require.Len(unreadItems, 1)
	assert.Nil(unreadItems[0].SourceAckQueuedAt)
	assert.Nil(unreadItems[0].SourceAckGenerationAt)
}

func TestUpsertNotificationsRejectsBlankPlatform(t *testing.T) {
	require := require.New(t)
	d := openTestDB(t)
	repoID := seedNotificationRepo(t, d)
	now := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
	itemNumber := 42

	err := d.UpsertNotifications(t.Context(), []Notification{{
		Platform:               "  ",
		PlatformHost:           "github.com",
		PlatformNotificationID: "thread-42",
		RepoID:                 &repoID,
		RepoOwner:              "acme",
		RepoName:               "widget",
		SubjectType:            "PullRequest",
		SubjectTitle:           "Review requested",
		WebURL:                 "https://github.com/acme/widget/pull/42",
		ItemNumber:             &itemNumber,
		ItemType:               "pr",
		ItemAuthor:             "octocat",
		Reason:                 "review_requested",
		Unread:                 true,
		Participating:          true,
		SourceUpdatedAt:        now,
		SyncedAt:               now,
	}})
	require.ErrorContains(err, "notification platform is required")
}

func TestNotificationPlatformScopedOperationsRejectBlankPlatform(t *testing.T) {
	require := require.New(t)
	d := openTestDB(t)
	now := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)

	_, err := d.GetNotificationSyncWatermark(t.Context(), "", "github.com", "tracked")
	require.ErrorContains(err, "notification platform is required")

	err = d.UpdateNotificationSyncWatermark(t.Context(), "", "github.com", now, nil, "", "tracked")
	require.ErrorContains(err, "notification platform is required")

	err = d.MarkNotificationsAcknowledged(t.Context(), "", "github.com", []string{"thread-1"}, now)
	require.ErrorContains(err, "notification platform is required")

	_, err = d.ListQueuedNotificationAcks(t.Context(), "", "github.com", 10, now)
	require.ErrorContains(err, "notification platform is required")

	err = d.DeferQueuedNotificationAcks(t.Context(), "", "github.com", now, "later")
	require.ErrorContains(err, "notification platform is required")
}

func TestNotificationSyncWatermarksAreScopedByPlatformAndHost(t *testing.T) {
	require := require.New(t)
	d := openTestDB(t)
	now := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
	full := now.Add(-time.Hour)

	require.NoError(d.UpdateNotificationSyncWatermark(t.Context(), "github", "code.example.com", now, &full, "", "github/code.example.com/acme/widget"))
	require.NoError(d.UpdateNotificationSyncWatermark(t.Context(), "gitlab", "code.example.com", now.Add(time.Minute), nil, "cursor-2", "gitlab/code.example.com/acme/widget"))

	githubWatermark, err := d.GetNotificationSyncWatermark(t.Context(), "github", "code.example.com", "github/code.example.com/acme/widget")
	require.NoError(err)
	require.NotNil(githubWatermark)
	require.Equal("github", githubWatermark.Platform)
	require.Empty(githubWatermark.SyncCursor)

	gitlabWatermark, err := d.GetNotificationSyncWatermark(t.Context(), "gitlab", "code.example.com", "gitlab/code.example.com/acme/widget")
	require.NoError(err)
	require.NotNil(gitlabWatermark)
	require.Equal("gitlab", gitlabWatermark.Platform)
	require.Equal("cursor-2", gitlabWatermark.SyncCursor)
}

func TestQueuedNotificationAcksStayWithinPlatformAndHost(t *testing.T) {
	require := require.New(t)
	d := openTestDB(t)
	repoID := seedNotificationRepo(t, d)
	now := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)

	githubItem := notificationFixture("shared-thread", "mention", now)
	githubItem.Platform = "github"
	githubItem.PlatformNotificationID = "shared-thread"
	githubItem.RepoID = &repoID

	gitlabItem := notificationFixture("shared-thread", "mention", now)
	gitlabItem.Platform = "gitlab"
	gitlabItem.PlatformHost = "code.example.com"
	gitlabItem.PlatformNotificationID = "shared-thread"
	gitlabItem.RepoID = &repoID

	require.NoError(d.UpsertNotifications(t.Context(), []Notification{githubItem, gitlabItem}))
	items, err := d.ListNotifications(t.Context(), ListNotificationsOpts{State: "all", Repos: []NotificationRepoFilter{
		{Platform: "github", PlatformHost: "github.com", RepoOwner: "acme", RepoName: "widget"},
		{Platform: "gitlab", PlatformHost: "code.example.com", RepoOwner: "acme", RepoName: "widget"},
	}})
	require.NoError(err)
	require.Len(items, 2)

	var githubID int64
	for _, item := range items {
		if item.Platform == "github" {
			githubID = item.ID
		}
	}
	require.NotZero(githubID)

	queuedAt := now.Add(time.Minute)
	queuedIDs, err := d.QueueNotificationIDsRead(t.Context(), []int64{githubID}, queuedAt)
	require.NoError(err)
	require.Len(queuedIDs, 1)

	queued, err := d.ListQueuedNotificationAcks(t.Context(), "github", "github.com", 10, queuedAt)
	require.NoError(err)
	require.Len(queued, 1)
	require.Equal("github", queued[0].Platform)
	other, err := d.ListQueuedNotificationAcks(t.Context(), "gitlab", "code.example.com", 10, queuedAt)
	require.NoError(err)
	require.Empty(other)
}

func TestMarkClosedLinkedNotificationsDoneRespectsNotificationPlatform(t *testing.T) {
	require := require.New(t)
	d := openTestDB(t)
	now := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
	githubRepoID, err := d.UpsertRepo(t.Context(), RepoIdentity{Platform: "github", PlatformHost: "code.example.com", Owner: "acme", Name: "widget"})
	require.NoError(err)
	_, err = d.UpsertRepo(t.Context(), RepoIdentity{Platform: "gitlab", PlatformHost: "code.example.com", Owner: "acme", Name: "widget", RepoPath: "acme/widget"})
	require.NoError(err)
	_, err = d.UpsertMergeRequest(t.Context(), &MergeRequest{
		RepoID:         githubRepoID,
		PlatformID:     100,
		Number:         7,
		URL:            "https://code.example.com/acme/widget/pull/7",
		Title:          "Closed PR",
		Author:         "octocat",
		State:          "closed",
		CreatedAt:      now,
		UpdatedAt:      now,
		LastActivityAt: now,
		ClosedAt:       &now,
	})
	require.NoError(err)

	githubItem := notificationFixture("github-thread", "mention", now)
	githubItem.Platform = "github"
	githubItem.PlatformHost = "code.example.com"
	githubItem.PlatformNotificationID = "github-thread"

	gitlabItem := notificationFixture("gitlab-thread", "mention", now)
	gitlabItem.Platform = "gitlab"
	gitlabItem.PlatformHost = "code.example.com"
	gitlabItem.PlatformNotificationID = "gitlab-thread"

	require.NoError(d.UpsertNotifications(t.Context(), []Notification{githubItem, gitlabItem}))
	require.NoError(d.MarkClosedLinkedNotificationsDone(t.Context(), now.Add(time.Minute)))

	items, err := d.ListNotifications(t.Context(), ListNotificationsOpts{State: "all", Repos: []NotificationRepoFilter{
		{Platform: "github", PlatformHost: "code.example.com", RepoOwner: "acme", RepoName: "widget"},
		{Platform: "gitlab", PlatformHost: "code.example.com", RepoOwner: "acme", RepoName: "widget"},
	}})
	require.NoError(err)
	require.Len(items, 2)

	for _, item := range items {
		switch item.Platform {
		case "github":
			require.NotNil(item.DoneAt)
			require.Equal("closed", item.DoneReason)
		case "gitlab":
			require.Nil(item.DoneAt)
			require.Empty(item.DoneReason)
		}
	}
}

func TestUpsertNotificationsReopensDoneReadForActivityAfterDoneGeneration(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	d := openTestDB(t)
	seedNotificationRepo(t, d)
	now := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
	require.NoError(d.UpsertNotifications(t.Context(), []Notification{notificationFixture("mention", "mention", now)}))
	items, err := d.ListNotifications(t.Context(), ListNotificationsOpts{State: "unread"})
	require.NoError(err)
	require.Len(items, 1)

	doneAt := now.Add(time.Minute)
	done, err := d.MarkNotificationsDone(t.Context(), []int64{items[0].ID}, doneAt, true)
	require.NoError(err)
	assert.Equal([]int64{items[0].ID}, done)

	newUnread := notificationFixture("mention", "mention", now.Add(30*time.Second))
	newUnread.Unread = true
	require.NoError(d.UpsertNotifications(t.Context(), []Notification{newUnread}))

	active, err := d.ListNotifications(t.Context(), ListNotificationsOpts{State: "active"})
	require.NoError(err)
	require.Len(active, 1)
	assert.Equal("mention", active[0].PlatformNotificationID)
	assert.Nil(active[0].DoneAt)
	assert.Nil(active[0].SourceAckQueuedAt)
	assert.Nil(active[0].SourceAckGenerationAt)
}
