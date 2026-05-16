package e2etest

import (
	"net/http/httptest"
	"testing"
	"time"

	Assert "github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/wesm/middleman/internal/apiclient"
	"github.com/wesm/middleman/internal/apiclient/generated"
	"github.com/wesm/middleman/internal/db"
)

func TestNotificationsTriageFlowE2E(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	srv, database, _ := setupTestServerWithConfigContent(t, `
sync_interval = "5m"
github_token_env = "MIDDLEMAN_GITHUB_TOKEN"
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

	widgetRepoID, err := database.UpsertRepo(t.Context(), db.GitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	toolsRepoID, err := database.UpsertRepo(t.Context(), db.GitHubRepoIdentity("github.com", "acme", "tools"))
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

	listResp, err := client.HTTP.ListNotificationsWithResponse(t.Context(), &generated.ListNotificationsParams{State: new("unread")})
	require.NoError(err)
	require.NotNil(listResp.JSON200)
	require.NotNil(listResp.JSON200.Items)
	require.Len(*listResp.JSON200.Items, 2)
	assert.Equal(int64(2), listResp.JSON200.Summary.Unread)
	assert.Equal(int64(2), listResp.JSON200.Summary.TotalActive)

	ids := []int64{(*listResp.JSON200.Items)[0].Id, (*listResp.JSON200.Items)[1].Id, 999999}
	readResp, err := client.HTTP.MarkNotificationsReadWithResponse(
		t.Context(),
		generated.MarkNotificationsReadJSONRequestBody{Ids: &ids},
	)
	require.NoError(err)
	require.NotNil(readResp.JSON200)
	require.ElementsMatch(ids[:2], *readResp.JSON200.Succeeded)
	require.ElementsMatch(ids[:2], *readResp.JSON200.Queued)
	require.Len(*readResp.JSON200.Failed, 1)
	assert.Equal(int64(999999), (*readResp.JSON200.Failed)[0].Id)

	readList, err := client.HTTP.ListNotificationsWithResponse(t.Context(), &generated.ListNotificationsParams{State: new("read")})
	require.NoError(err)
	require.NotNil(readList.JSON200)
	require.NotNil(readList.JSON200.Items)
	require.Len(*readList.JSON200.Items, 2)
	for _, item := range *readList.JSON200.Items {
		assert.False(item.Unread)
		require.NotNil(item.GithubReadQueuedAt)
	}

	doneResp, err := client.HTTP.MarkNotificationsDoneWithResponse(
		t.Context(),
		generated.MarkNotificationsDoneJSONRequestBody{Ids: &ids},
	)
	require.NoError(err)
	require.NotNil(doneResp.JSON200)
	require.ElementsMatch(ids[:2], *doneResp.JSON200.Succeeded)
	require.ElementsMatch(ids[:2], *doneResp.JSON200.Queued)
	require.Len(*doneResp.JSON200.Failed, 1)

	doneList, err := client.HTTP.ListNotificationsWithResponse(t.Context(), &generated.ListNotificationsParams{State: new("done")})
	require.NoError(err)
	require.NotNil(doneList.JSON200)
	require.NotNil(doneList.JSON200.Items)
	require.Len(*doneList.JSON200.Items, 2)
	for _, item := range *doneList.JSON200.Items {
		require.NotNil(item.DoneAt)
	}

	undoneIDs := []int64{ids[0], ids[1]}
	undoneResp, err := client.HTTP.MarkNotificationsUndoneWithResponse(
		t.Context(),
		generated.MarkNotificationsUndoneJSONRequestBody{Ids: &undoneIDs},
	)
	require.NoError(err)
	require.NotNil(undoneResp.JSON200)
	require.ElementsMatch(undoneIDs, *undoneResp.JSON200.Succeeded)

	activeList, err := client.HTTP.ListNotificationsWithResponse(t.Context(), &generated.ListNotificationsParams{State: new("active")})
	require.NoError(err)
	require.NotNil(activeList.JSON200)
	require.NotNil(activeList.JSON200.Items)
	require.Len(*activeList.JSON200.Items, 2)
	for _, item := range *activeList.JSON200.Items {
		assert.Nil(item.DoneAt)
	}
}
