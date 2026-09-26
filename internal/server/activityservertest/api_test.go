package activityservertest

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	serverfake "go.kenn.io/forge/internal/testutil/serverfake"
	servertest "go.kenn.io/forge/internal/testutil/servertest"

	gh "github.com/google/go-github/v91/github"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/apiclient/generated"
	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/db"

	ghclient "go.kenn.io/forge/internal/github"
	"go.kenn.io/forge/internal/platformdb"
	"go.kenn.io/forge/internal/server"
	"go.kenn.io/forge/internal/server/itemapi"
	"go.kenn.io/forge/internal/testutil"
	"go.kenn.io/forge/internal/testutil/dbtest"
	"go.kenn.io/forge/platform"
)

func TestMain(m *testing.M) {
	os.Exit(serverfake.RunMain(m))
}

// setupTestServer opens a temp DB, builds a Server, and returns both.
func setupTestServer(t *testing.T) (*server.Server, *db.DB, *ghclient.Syncer) {
	t.Helper()
	return setupTestServerWithMock(t, &serverfake.MockGH{})
}

func setupNotificationsEnabledTestServer(t *testing.T) (*server.Server, *db.DB) {
	t.Helper()
	database := dbtest.Open(t)
	syncer := ghclient.NewSyncer(
		map[string]ghclient.Client{"github.com": &serverfake.MockGH{}},
		database,
		nil,
		serverfake.DefaultTestRepos,
		time.Minute,
		nil,
		nil,
	)
	t.Cleanup(syncer.Stop)
	srv := server.New(
		database, syncer, nil, "/",
		notificationsEnabledConfig(), server.ServerOptions{},
	)
	t.Cleanup(func() { serverfake.GracefulShutdown(t, srv) })
	return srv, database
}

func setupTestServerWithMock(t *testing.T, mock *serverfake.MockGH) (*server.Server, *db.DB, *ghclient.Syncer) {
	t.Helper()
	return setupTestServerWithRepos(t, mock, serverfake.DefaultTestRepos)
}

func setupTestServerWithRepos(
	t *testing.T, mock *serverfake.MockGH, repos []ghclient.RepoRef,
) (*server.Server, *db.DB, *ghclient.Syncer) {
	return setupTestServerWithReposAndOptions(t, mock, repos, server.ServerOptions{})
}

func setupTestServerWithReposAndOptions(
	t *testing.T, mock *serverfake.MockGH, repos []ghclient.RepoRef, options server.ServerOptions,
) (*server.Server, *db.DB, *ghclient.Syncer) {
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
	t.Cleanup(func() { serverfake.GracefulShutdown(t, srv) })
	return srv, database, syncer
}

func TestAPIListPullsUsesProviderActivityAfterIndexSync(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	base := time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC)
	staleDerivedActivity := base.Add(2 * time.Hour)
	otherActivity := base.Add(time.Hour)

	str := func(v string) *string { return &v }
	mock := &serverfake.MockGH{
		ListOpenPullRequestsFn: func(_ context.Context, _, _ string) ([]*gh.PullRequest, error) {
			firstNumber := 1
			firstID := int64(1001)
			secondNumber := 2
			secondID := int64(1002)
			return []*gh.PullRequest{
				{
					ID:        &firstID,
					Number:    &firstNumber,
					Title:     str("Preserve activity"),
					State:     str("open"),
					HTMLURL:   str("https://github.com/acme/widget/pull/1"),
					User:      &gh.User{Login: str("octocat")},
					CreatedAt: &gh.Timestamp{Time: base},
					UpdatedAt: &gh.Timestamp{Time: base},
					Head:      &gh.PullRequestBranch{Ref: str("feature-one"), SHA: str("head-one")},
					Base:      &gh.PullRequestBranch{Ref: str("main"), SHA: str("base-one")},
				},
				{
					ID:        &secondID,
					Number:    &secondNumber,
					Title:     str("Other activity"),
					State:     str("open"),
					HTMLURL:   str("https://github.com/acme/widget/pull/2"),
					User:      &gh.User{Login: str("octocat")},
					CreatedAt: &gh.Timestamp{Time: base},
					UpdatedAt: &gh.Timestamp{Time: otherActivity},
					Head:      &gh.PullRequestBranch{Ref: str("feature-two"), SHA: str("head-two")},
					Base:      &gh.PullRequestBranch{Ref: str("main"), SHA: str("base-two")},
				},
			}, nil
		},
	}
	srv, database, syncer := setupTestServerWithMock(t, mock)
	serverfake.SeedPR(t, database, "acme", "widget", 1,
		serverfake.WithSeedPRTitle("Preserve activity"),
		serverfake.WithSeedPRTimes(base, base, staleDerivedActivity),
	)
	serverfake.SeedPR(t, database, "acme", "widget", 2,
		serverfake.WithSeedPRTitle("Other activity"),
		serverfake.WithSeedPRTimes(base, otherActivity, otherActivity),
	)
	client := servertest.SetupTestClient(t, srv)

	syncer.RunOnce(ctx)

	resp, err := client.HTTP.ListPullsWithResponse(ctx, &generated.ListPullsRequestOptions{})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)
	require.NotNil(resp.JSON200)
	require.Len(*resp.JSON200, 2)
	assert.Equal(int64(2), (*resp.JSON200)[0].Number)
	assert.Equal(otherActivity, (*resp.JSON200)[0].LastActivityAt.UTC())
	assert.Equal(int64(1), (*resp.JSON200)[1].Number)
	assert.Equal(base, (*resp.JSON200)[1].LastActivityAt.UTC())
}

func TestAPIListActivity(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	srv, database, _ := setupTestServer(t)
	client := servertest.SetupTestClient(t, srv)

	prID := serverfake.SeedPR(t, database, "acme", "widget", 1)
	ctx := t.Context()
	repo, err := database.GetRepoByIdentity(ctx, serverfake.VerifiedGitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	require.NotNil(repo)
	require.NoError(database.UpdateMergeRequestAssignees(ctx, repo.ID, prID, nil))

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
	resp, err := client.HTTP.ListActivityWithResponse(ctx, &generated.ListActivityRequestOptions{Query: &generated.ListActivityQuery{Since: &since}})
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode)
	require.NotNil(resp.JSON200)
	require.NotNil(resp.JSON200.Items)
	assert.NotEmpty(resp.JSON200.Items,
		"activity feed should contain PR and comment items")
	assert.Equal("github.com", resp.JSON200.Items[0].PlatformHost)

	collapsed := testutil.DoJSON(
		t,
		srv,
		http.MethodGet,
		"/api/v1/activity?since="+url.QueryEscape(since)+"&projection=collapsed&unassigned=true",
		nil,
	)
	require.Equal(http.StatusOK, collapsed.Code)
	var collapsedBody struct {
		Items        []itemapi.ActivityItemResponse    `json:"items"`
		ItemActivity []itemapi.ActivitySubjectResponse `json:"item_activity"`
		EventCursor  string                            `json:"event_cursor"`
	}
	require.NoError(json.NewDecoder(collapsed.Body).Decode(&collapsedBody))
	assert.Empty(collapsedBody.Items, "collapsed projection must omit pull request child events")
	require.Len(collapsedBody.ItemActivity, 1)
	assert.NotEmpty(collapsedBody.ItemActivity[0].EventLedgerRevision,
		"collapsed parents must identify the exact child ledger snapshot")
	assert.NotEmpty(collapsedBody.EventCursor, "cursor must cover omitted child events")

	delta := testutil.DoJSON(
		t,
		srv,
		http.MethodGet,
		"/api/v1/activity?since="+url.QueryEscape(since)+"&projection=events&after="+
			url.QueryEscape(collapsedBody.EventCursor),
		nil)

	require.Equal(http.StatusOK, delta.Code)
	var deltaBody struct {
		Items        []itemapi.ActivityItemResponse    `json:"items"`
		ItemActivity []itemapi.ActivitySubjectResponse `json:"item_activity"`
		EventCursor  string                            `json:"event_cursor"`
	}
	require.NoError(json.NewDecoder(delta.Body).Decode(&deltaBody))
	assert.Empty(deltaBody.Items)
	assert.Empty(deltaBody.ItemActivity, "event deltas must not resend parent summaries")
	assert.Equal(collapsedBody.EventCursor, deltaBody.EventCursor)

	thread := testutil.DoJSON(
		t,
		srv,
		http.MethodGet,
		"/api/v1/activity/thread-events?provider=github&platform_host=github.com"+
			"&platform_repo_id=repo-acme-widget&item_type=pr&item_number=1&since="+
			url.QueryEscape(since),
		nil)

	require.Equal(http.StatusOK, thread.Code)
	var threadBody itemapi.ActivityResponse
	require.NoError(json.NewDecoder(thread.Body).Decode(&threadBody))
	require.Len(threadBody.Items, 2)
	assert.Empty(threadBody.ItemActivity)

	require.NoError(database.UpdateMergeRequestAssignees(ctx, repo.ID, prID, []string{"alice"}))
	assignedThread := testutil.DoJSON(
		t,
		srv,
		http.MethodGet,
		"/api/v1/activity/thread-events?provider=github&platform_host=github.com"+
			"&platform_repo_id=repo-acme-widget&item_type=pr&item_number=1&since="+
			url.QueryEscape(since)+"&unassigned=true",
		nil,
	)
	require.Equal(http.StatusOK, assignedThread.Code)
	var assignedThreadBody itemapi.ActivityResponse
	require.NoError(json.NewDecoder(assignedThread.Body).Decode(&assignedThreadBody))
	assert.Empty(assignedThreadBody.Items)

	filteredThread := testutil.DoJSON(
		t,
		srv,
		http.MethodGet,
		"/api/v1/activity/thread-events?provider=github&platform_host=github.com"+
			"&platform_repo_id=repo-acme-widget&item_type=pr&item_number=1&since="+
			url.QueryEscape(since)+"&types=comment&search="+url.QueryEscape("Looks good"),
		nil)

	require.Equal(http.StatusOK, filteredThread.Code)
	var filteredThreadBody itemapi.ActivityResponse
	require.NoError(json.NewDecoder(filteredThread.Body).Decode(&filteredThreadBody))
	require.Len(filteredThreadBody.Items, 1)
	assert.Equal("comment", filteredThreadBody.Items[0].ActivityType)

	search := "reviewer"
	filtered, err := client.HTTP.ListActivityWithResponse(ctx, &generated.ListActivityRequestOptions{Query: &generated.ListActivityQuery{Since: &since, Search: &search}})
	require.NoError(err)
	require.Equal(http.StatusOK, filtered.StatusCode)
	require.NotNil(filtered.JSON200)
	require.NotNil(filtered.JSON200.Items)
	require.Len(filtered.JSON200.Items, 1)
	assert.Equal("comment", filtered.JSON200.Items[0].ActivityType)
	assert.Equal("reviewer", filtered.JSON200.Items[0].Author)

	itemNumber := "#1"
	byNumber, err := client.HTTP.ListActivityWithResponse(ctx, &generated.ListActivityRequestOptions{Query: &generated.ListActivityQuery{Since: &since, Search: &itemNumber}})
	require.NoError(err)
	require.Equal(http.StatusOK, byNumber.StatusCode)
	require.NotNil(byNumber.JSON200)
	require.NotNil(byNumber.JSON200.Items)
	require.Len(byNumber.JSON200.Items, 2)
	for _, item := range byNumber.JSON200.Items {
		assert.Equal(int64(1), item.ItemNumber)
	}

	whitespace := " \t "
	unfiltered, err := client.HTTP.ListActivityWithResponse(ctx, &generated.ListActivityRequestOptions{Query: &generated.ListActivityQuery{Since: &since, Search: &whitespace}})
	require.NoError(err)
	require.Equal(http.StatusOK, unfiltered.StatusCode)
	require.NotNil(unfiltered.JSON200)
	require.NotNil(unfiltered.JSON200.Items)
	assert.Len(unfiltered.JSON200.Items, len(resp.JSON200.Items))
}

func TestAPIListCollapsedActivityHonorsLimit(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	srv, database, _ := setupTestServer(t)
	now := time.Now().UTC().Truncate(time.Second)
	for number := 1; number <= 11; number++ {
		activityAt := now.Add(-time.Duration(number) * time.Minute)
		serverfake.SeedPR(
			t, database, "acme", "widget", number,
			serverfake.WithSeedPRTimes(activityAt, activityAt, activityAt),
		)
	}

	since := url.QueryEscape(now.Add(-7 * 24 * time.Hour).Format(time.RFC3339))
	rr := testutil.DoJSON(
		t,
		srv,
		http.MethodGet,
		"/api/v1/activity?since="+since+"&projection=collapsed&limit=10",
		nil)

	require.Equal(http.StatusOK, rr.Code)
	var body itemapi.ActivityResponse
	require.NoError(json.NewDecoder(rr.Body).Decode(&body))
	require.Len(body.ItemActivity, 10)
	assert.True(body.ItemActivityCapped)
	assert.Equal(1, body.ItemActivity[0].ItemNumber)
}

func TestAPIListCollapsedActivitySearchLimitCountsDistinctParents(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	srv, database, _ := setupTestServer(t)
	ctx := t.Context()
	base := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Second)

	olderPRID := serverfake.SeedPR(
		t, database, "acme", "widget", 1,
		serverfake.WithSeedPRTitle("Unrelated older parent"),
		serverfake.WithSeedPRTimes(base, base, base),
	)
	require.NoError(database.UpsertMREvents(ctx, []db.MREvent{{
		MergeRequestID: olderPRID,
		EventType:      "issue_comment",
		Author:         "reviewer",
		Body:           "needle in an older event",
		CreatedAt:      base.Add(time.Minute),
		DedupeKey:      "older-search-match",
	}}))
	newerPRID := serverfake.SeedPR(
		t, database, "acme", "widget", 2,
		serverfake.WithSeedPRTitle("Unrelated newer parent"),
		serverfake.WithSeedPRTimes(base, base, base),
	)
	newerEvents := make([]db.MREvent, 31)
	for i := range newerEvents {
		newerEvents[i] = db.MREvent{
			MergeRequestID: newerPRID,
			EventType:      "issue_comment",
			Author:         "reviewer",
			Body:           "needle repeated",
			CreatedAt:      base.Add(2*time.Minute + time.Duration(i)*time.Second),
			DedupeKey:      fmt.Sprintf("newer-search-match-%d", i),
		}
	}
	require.NoError(database.UpsertMREvents(ctx, newerEvents))

	since := url.QueryEscape(base.Add(-time.Minute).Format(time.RFC3339))
	rr := testutil.DoJSON(
		t,
		srv,
		http.MethodGet,
		"/api/v1/activity?since="+since+"&projection=collapsed&limit=30&search=needle",
		nil)

	require.Equal(http.StatusOK, rr.Code)
	var body itemapi.ActivityResponse
	require.NoError(json.NewDecoder(rr.Body).Decode(&body))
	require.Len(body.ItemActivity, 2)
	assert.Equal([]int{2, 1}, []int{body.ItemActivity[0].ItemNumber, body.ItemActivity[1].ItemNumber})
	assert.False(body.ItemActivityCapped)
}

func TestAPIListCollapsedActivityIncludesParentRecentOnlyByNotification(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	srv, database := setupNotificationsEnabledTestServer(t)
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Second)
	oldActivity := now.Add(-30 * 24 * time.Hour)
	serverfake.SeedPR(
		t, database, "acme", "widget", 71,
		serverfake.WithSeedPRTitle("Old pull with recent notification"),
		serverfake.WithSeedPRTimes(oldActivity, oldActivity, oldActivity),
	)
	number := 71
	notificationAt := now.Add(-time.Hour)
	require.NoError(database.UpsertNotifications(ctx, []db.Notification{{
		Platform: "github", PlatformHost: "github.com",
		PlatformNotificationID: "api-recent-old-pull",
		RepoOwner:              "acme", RepoName: "widget",
		SubjectType: "PullRequest", SubjectTitle: "Old pull with recent notification",
		WebURL:     "https://github.com/acme/widget/pull/71",
		ItemNumber: &number, ItemType: "pr", ItemAuthor: "contributor",
		Reason: "mention", Unread: true,
		SourceUpdatedAt: notificationAt, SyncedAt: notificationAt,
	}}))

	since := url.QueryEscape(now.Add(-7 * 24 * time.Hour).Format(time.RFC3339))
	rr := testutil.DoJSON(
		t,
		srv,
		http.MethodGet,
		"/api/v1/activity?since="+since+"&projection=collapsed",
		nil)

	require.Equal(http.StatusOK, rr.Code)
	var body struct {
		Items        []itemapi.ActivityItemResponse    `json:"items"`
		ItemActivity []itemapi.ActivitySubjectResponse `json:"item_activity"`
	}
	require.NoError(json.NewDecoder(rr.Body).Decode(&body))
	assert.Empty(body.Items)
	require.Len(body.ItemActivity, 1)
	assert.Equal(71, body.ItemActivity[0].ItemNumber)
	assert.Equal(itemapi.FormatUTCRFC3339(notificationAt), body.ItemActivity[0].ActivityAt)

	filtered := testutil.DoJSON(
		t,
		srv,
		http.MethodGet,
		"/api/v1/activity?since="+since+"&projection=collapsed&types=comment",
		nil)

	require.Equal(http.StatusOK, filtered.Code)
	var filteredBody struct {
		Items        []itemapi.ActivityItemResponse    `json:"items"`
		ItemActivity []itemapi.ActivitySubjectResponse `json:"item_activity"`
	}
	require.NoError(json.NewDecoder(filtered.Body).Decode(&filteredBody))
	assert.Empty(filteredBody.Items)
	assert.Empty(filteredBody.ItemActivity,
		"hidden notifications must not pull otherwise-old parents into the window")
}

func TestAPIListCollapsedActivityRetainsVisibleEventsForBotParents(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	srv, database, _ := setupTestServer(t)
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Second)
	prID := serverfake.SeedPR(
		t, database, "acme", "widget", 81,
		serverfake.WithSeedPRTitle("Bot-authored pull request"),
		serverfake.WithSeedPRAuthor("dependabot[bot]"),
		serverfake.WithSeedPRTimes(now.Add(-time.Hour), now.Add(-time.Hour), now.Add(-time.Hour)),
	)
	require.NoError(database.UpsertMREvents(ctx, []db.MREvent{{
		MergeRequestID: prID,
		EventType:      "issue_comment",
		Author:         "human-reviewer",
		Body:           "Visible human comment",
		CreatedAt:      now,
		DedupeKey:      "api-human-comment-on-bot-parent",
	}}))

	since := url.QueryEscape(now.Add(-7 * 24 * time.Hour).Format(time.RFC3339))
	rr := testutil.DoJSON(
		t,
		srv,
		http.MethodGet,
		"/api/v1/activity?since="+since+"&projection=collapsed&hide_bots=true",
		nil)

	require.Equal(http.StatusOK, rr.Code)
	var body struct {
		Items        []itemapi.ActivityItemResponse    `json:"items"`
		ItemActivity []itemapi.ActivitySubjectResponse `json:"item_activity"`
	}
	require.NoError(json.NewDecoder(rr.Body).Decode(&body))
	require.Len(body.Items, 1)
	assert.Equal("comment", body.Items[0].ActivityType)
	assert.Equal(81, body.Items[0].ItemNumber)
	assert.Equal("human-reviewer", body.Items[0].Author)
	assert.Empty(body.ItemActivity)
}

func TestAPIListActivityReturnsRecentParentWhenItsVisibleEventsAreFiltered(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	srv, database, _ := setupTestServer(t)
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Second)
	createdAt := now.Add(-30 * 24 * time.Hour)
	activityAt := now.Add(-time.Hour)

	prID := serverfake.SeedPR(
		t, database, "acme", "widget", 77,
		serverfake.WithSeedPRTitle("Old pull with recent hidden activity"),
		serverfake.WithSeedPRTimes(createdAt, activityAt, activityAt),
	)
	require.NoError(database.InsertWorkspace(ctx, &db.Workspace{
		ID: "ws-hidden-parent", Platform: "github", PlatformHost: "github.com",
		RepoOwner: "acme", RepoName: "widget", ItemType: db.WorkspaceItemTypePullRequest,
		ItemNumber: 77, WorktreePath: t.TempDir(), Status: "ready",
	}))
	require.NoError(database.UpsertMREvents(ctx, []db.MREvent{{
		MergeRequestID: prID,
		EventType:      "issue_comment",
		Author:         "reviewer",
		CreatedAt:      activityAt,
		DedupeKey:      "api-hidden-parent-comment",
	}}))

	since := url.QueryEscape(now.Add(-7 * 24 * time.Hour).Format(time.RFC3339))
	rr := testutil.DoJSON(
		t,
		srv,
		http.MethodGet,
		"/api/v1/activity?since="+since+"&types=commit&item_types=pr,repo",
		nil)

	require.Equal(http.StatusOK, rr.Code)
	var body struct {
		Items        []itemapi.ActivityItemResponse    `json:"items"`
		ItemActivity []itemapi.ActivitySubjectResponse `json:"item_activity"`
	}
	require.NoError(json.NewDecoder(rr.Body).Decode(&body))
	assert.Empty(body.Items, "the comment must remain hidden by the event filter")
	require.Len(body.ItemActivity, 1)
	assert.Equal(77, body.ItemActivity[0].ItemNumber)
	assert.Equal("pr", body.ItemActivity[0].ItemType)
	assert.Equal("Old pull with recent hidden activity", body.ItemActivity[0].ItemTitle)
	assert.Equal(itemapi.FormatUTCRFC3339(activityAt), body.ItemActivity[0].ActivityAt)
	require.NotNil(body.ItemActivity[0].Workspace)
	assert.Equal("ws-hidden-parent", body.ItemActivity[0].Workspace.ID)
}

func TestAPIListActivityIncrementalSearchReturnsParentsMatchedByProviderEvents(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	srv, database, _ := setupTestServer(t)
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Second)
	activityAt := now.Add(-time.Hour)

	bodyMatchID := serverfake.SeedPR(
		t, database, "acme", "widget", 78,
		serverfake.WithSeedPRTitle("Parent with an unrelated title"),
		serverfake.WithSeedPRTimes(activityAt, activityAt, activityAt),
	)
	actorMatchID := serverfake.SeedPR(
		t, database, "acme", "widget", 79,
		serverfake.WithSeedPRTitle("Another unrelated parent"),
		serverfake.WithSeedPRTimes(activityAt, activityAt, activityAt),
	)
	require.NoError(t, database.UpsertMREvents(ctx, []db.MREvent{
		{
			MergeRequestID: bodyMatchID,
			EventType:      "issue_comment",
			Author:         "reviewer",
			Body:           "contains body-match-term",
			CreatedAt:      activityAt,
			DedupeKey:      "incremental-search-body-match",
		},
		{
			MergeRequestID: actorMatchID,
			EventType:      "issue_comment",
			Author:         "actor-match-term",
			Body:           "unrelated comment",
			CreatedAt:      activityAt,
			DedupeKey:      "incremental-search-actor-match",
		},
	}))

	since := url.QueryEscape(now.Add(-7 * 24 * time.Hour).Format(time.RFC3339))
	after := url.QueryEscape(db.EncodeCursor(now, "mr_event", 1))
	for _, tc := range []struct {
		name       string
		search     string
		itemNumber int
	}{
		{name: "event body", search: "body-match-term", itemNumber: 78},
		{name: "event actor", search: "actor-match-term", itemNumber: 79},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			rr := testutil.DoJSON(
				t,
				srv,
				http.MethodGet,
				"/api/v1/activity?since="+since+"&after="+after+"&search="+url.QueryEscape(tc.search),
				nil)

			require.Equal(http.StatusOK, rr.Code)
			var body struct {
				Items        []itemapi.ActivityItemResponse    `json:"items"`
				ItemActivity []itemapi.ActivitySubjectResponse `json:"item_activity"`
			}
			require.NoError(json.NewDecoder(rr.Body).Decode(&body))
			require.Empty(body.Items, "the matching event is behind the incremental cursor")
			require.Len(body.ItemActivity, 1)
			require.Equal(tc.itemNumber, body.ItemActivity[0].ItemNumber)
		})
	}
}

func TestAPIListActivitySeparatesEventAndParentSnapshotCaps(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	srv, database, _ := setupTestServer(t)
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Second)
	repoID, err := database.UpsertRepo(ctx, serverfake.VerifiedGitHubRepoIdentity("github.com", "acme", "many-parents"))
	require.NoError(err)

	_, err = database.WriteDB().ExecContext(ctx, `
		WITH digits(n) AS (
			VALUES (0), (1), (2), (3), (4), (5), (6), (7), (8), (9)
		), numbers(n) AS (
			SELECT ones.n + 10 * tens.n + 100 * hundreds.n + 1000 * thousands.n + 1
			FROM digits AS ones
			CROSS JOIN digits AS tens
			CROSS JOIN digits AS hundreds
			CROSS JOIN digits AS thousands
		)
		INSERT INTO forge_merge_requests (
			repo_id, platform_id, number, url, title, author, state,
			created_at, updated_at, last_activity_at
		)
		SELECT ?, n, n,
		       'https://github.com/acme/many-parents/pull/' || n,
		       'Parent ' || n, 'testuser', 'open', ?, ?, ?
		FROM numbers
		WHERE n <= ?`,
		repoID, now, now, now, itemapi.ActivitySafetyCap+1,
	)
	require.NoError(err)

	since := url.QueryEscape(now.Add(-time.Minute).Format(time.RFC3339))
	rr := testutil.DoJSON(
		t,
		srv,
		http.MethodGet,
		"/api/v1/activity?since="+since+"&types=commit&item_types=pr,repo",
		nil)

	require.Equal(http.StatusOK, rr.Code)
	var body struct {
		Items              []itemapi.ActivityItemResponse    `json:"items"`
		ItemActivity       []itemapi.ActivitySubjectResponse `json:"item_activity"`
		Capped             bool                              `json:"capped"`
		ItemActivityCapped bool                              `json:"item_activity_capped"`
	}
	require.NoError(json.NewDecoder(rr.Body).Decode(&body))
	assert.Empty(body.Items)
	require.Len(body.ItemActivity, itemapi.ActivitySafetyCap)
	assert.False(body.Capped, "parent snapshot overflow must not report event overflow")
	assert.True(body.ItemActivityCapped)
}

func TestAPIListActivityFiltersByAuthorAndListsScopedCandidates(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	srv, database, _ := setupTestServer(t)
	ctx := t.Context()
	base := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)

	trackedPRID := serverfake.SeedPR(
		t, database, "acme", "widget", 1,
		serverfake.WithSeedPRAuthor("Item Owner"),
		serverfake.WithSeedPRTimes(base, base, base),
	)
	require.NoError(database.UpsertMREvents(ctx, []db.MREvent{
		{
			MergeRequestID: trackedPRID,
			EventType:      "issue_comment",
			Author:         "Reviewer",
			CreatedAt:      base.Add(time.Minute),
			DedupeKey:      "tracked-reviewer",
		},
		{
			MergeRequestID: trackedPRID,
			EventType:      "issue_comment",
			Author:         "reviewer-bot",
			CreatedAt:      base.Add(2 * time.Minute),
			DedupeKey:      "tracked-reviewer-bot",
		},
	}))
	serverfake.SeedPR(
		t, database, "acme", "untracked", 1,
		serverfake.WithSeedPRAuthor("Hidden Actor"),
		serverfake.WithSeedPRTimes(base.Add(3*time.Minute), base.Add(3*time.Minute), base.Add(3*time.Minute)),
	)

	since := base.Add(-time.Minute).Format(time.RFC3339)
	feed := testutil.DoJSON(
		t,
		srv,
		http.MethodGet,
		"/api/v1/activity?since="+url.QueryEscape(since)+"&author=ITEM%20OWNER",
		nil)

	require.Equal(http.StatusOK, feed.Code)
	var feedBody itemapi.ActivityResponse
	require.NoError(json.Unmarshal(feed.Body.Bytes(), &feedBody))
	require.Len(feedBody.Items, 3)
	for _, item := range feedBody.Items {
		assert.Equal("Item Owner", item.ItemAuthor)
	}

	commenterFeed := testutil.DoJSON(
		t,
		srv,
		http.MethodGet,
		"/api/v1/activity?since="+url.QueryEscape(since)+"&author=REVIEWER",
		nil)

	require.Equal(http.StatusOK, commenterFeed.Code)
	var commenterFeedBody itemapi.ActivityResponse
	require.NoError(json.Unmarshal(commenterFeed.Body.Bytes(), &commenterFeedBody))
	assert.Empty(commenterFeedBody.Items)

	candidates := testutil.DoJSON(
		t,
		srv,
		http.MethodGet,
		"/api/v1/activity/authors?since="+url.QueryEscape(since),
		nil)

	require.Equal(http.StatusOK, candidates.Code)
	var candidateBody struct {
		Authors []string `json:"authors"`
	}
	require.NoError(json.Unmarshal(candidates.Body.Bytes(), &candidateBody))
	assert.Equal([]string{"Item Owner"}, candidateBody.Authors)
	assert.NotContains(candidateBody.Authors, "Hidden Actor")
}

func TestAPIListActivityReturnsParentRecencyWhenCommitEventsAreFiltered(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	srv, database, _ := setupTestServer(t)
	ctx := t.Context()
	base := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	// The hidden commit is the newest rendered ledger event, so it defines
	// the parent's recency; the provider's later updated_at does not.
	parentActivityAt := base.Add(19 * time.Minute)

	prID := serverfake.SeedPR(
		t, database, "acme", "widget", 1,
		serverfake.WithSeedPRTimes(base, base.Add(20*time.Minute), base.Add(20*time.Minute)),
	)
	require.NoError(database.UpsertMREvents(ctx, []db.MREvent{
		{
			MergeRequestID: prID,
			EventType:      "issue_comment",
			Author:         "reviewer",
			CreatedAt:      base.Add(10 * time.Minute),
			DedupeKey:      "visible-comment",
		},
		{
			MergeRequestID: prID,
			EventType:      "commit",
			Author:         "owner",
			CreatedAt:      base.Add(19 * time.Minute),
			DedupeKey:      "filtered-commit",
		},
	}))

	since := base.Add(-time.Minute).Format(time.RFC3339)
	rr := testutil.DoJSON(
		t,
		srv,
		http.MethodGet,
		"/api/v1/activity?since="+url.QueryEscape(since)+"&types=comment",
		nil)

	require.Equal(http.StatusOK, rr.Code)
	var body itemapi.ActivityResponse
	require.NoError(json.Unmarshal(rr.Body.Bytes(), &body))
	require.Len(body.Items, 1)
	require.Equal("comment", body.Items[0].ActivityType)
	require.Equal(parentActivityAt.Format(time.RFC3339), body.Items[0].ItemLastActivityAt)
}

func TestAPIListActivityAppliesTrackedRepoScopeBeforeAuthorLimit(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	srv, database := setupNotificationsEnabledTestServer(t)
	ctx := t.Context()
	base := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Second)

	trackedPRID := serverfake.SeedPR(
		t, database, "acme", "widget", 1,
		serverfake.WithSeedPRAuthor("Item Owner"),
		serverfake.WithSeedPRTimes(base, base, base),
	)
	require.NoError(database.UpsertMREvents(ctx, []db.MREvent{{
		MergeRequestID: trackedPRID,
		EventType:      "issue_comment",
		Author:         "Reviewer",
		CreatedAt:      base.Add(time.Minute),
		DedupeKey:      "tracked-reviewer",
	}}))

	untrackedPRID := serverfake.SeedPR(
		t, database, "acme", "untracked", 1,
		serverfake.WithSeedPRAuthor("Item Owner"),
		serverfake.WithSeedPRTimes(base, base, base),
	)
	untrackedEvents := make([]db.MREvent, itemapi.ActivitySafetyCap+1)
	for i := range untrackedEvents {
		untrackedEvents[i] = db.MREvent{
			MergeRequestID: untrackedPRID,
			EventType:      "issue_comment",
			Author:         "Reviewer",
			CreatedAt:      base.Add(2*time.Minute + time.Duration(i)*time.Second),
			DedupeKey:      fmt.Sprintf("untracked-reviewer-%d", i),
		}
	}
	require.NoError(database.UpsertMREvents(ctx, untrackedEvents))

	since := base.Add(-time.Minute).Format(time.RFC3339)
	feed := testutil.DoJSON(
		t,
		srv,
		http.MethodGet,
		"/api/v1/activity?since="+url.QueryEscape(since)+"&author=ITEM%20OWNER",
		nil)

	require.Equal(http.StatusOK, feed.Code)
	var feedBody itemapi.ActivityResponse
	require.NoError(json.Unmarshal(feed.Body.Bytes(), &feedBody))
	require.Len(feedBody.Items, 2)
	for _, item := range feedBody.Items {
		assert.Equal("acme", item.RepoOwner)
		assert.Equal("widget", item.RepoName)
		assert.Equal("Item Owner", item.ItemAuthor)
	}
	assert.False(feedBody.Capped)
}

func TestAPIListActivitySearchReportsParentTruncationWhenMatchesOverflowEventCap(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	assert := assert.New(t)
	srv, database, _ := setupTestServer(t)
	ctx := t.Context()
	base := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Second)

	// The older parent matches the search only through its event body, and
	// that event sits behind more than a full event page of newer matches on
	// another parent, so it can only be recognised as a truncated parent.
	olderPRID := serverfake.SeedPR(
		t, database, "acme", "widget", 1,
		serverfake.WithSeedPRTitle("Unrelated older parent"),
		serverfake.WithSeedPRTimes(base, base, base),
	)
	require.NoError(database.UpsertMREvents(ctx, []db.MREvent{{
		MergeRequestID: olderPRID,
		EventType:      "issue_comment",
		Author:         "reviewer",
		Body:           "needle in an older event",
		CreatedAt:      base.Add(time.Minute),
		DedupeKey:      "older-needle",
	}}))
	noisyPRID := serverfake.SeedPR(
		t, database, "acme", "widget", 2,
		serverfake.WithSeedPRTitle("Unrelated noisy parent"),
		serverfake.WithSeedPRTimes(base, base, base),
	)
	noisyEvents := make([]db.MREvent, itemapi.ActivitySafetyCap+1)
	for i := range noisyEvents {
		noisyEvents[i] = db.MREvent{
			MergeRequestID: noisyPRID,
			EventType:      "issue_comment",
			Author:         "reviewer",
			Body:           "needle repeated",
			CreatedAt:      base.Add(2*time.Minute + time.Duration(i)*time.Second),
			DedupeKey:      fmt.Sprintf("noisy-needle-%d", i),
		}
	}
	require.NoError(database.UpsertMREvents(ctx, noisyEvents))

	since := url.QueryEscape(base.Add(-time.Minute).Format(time.RFC3339))
	rr := testutil.DoJSON(t, srv, http.MethodGet, "/api/v1/activity?since="+since+"&search=needle", nil)
	require.Equal(http.StatusOK, rr.Code)
	var body struct {
		Items              []itemapi.ActivityItemResponse    `json:"items"`
		ItemActivity       []itemapi.ActivitySubjectResponse `json:"item_activity"`
		Capped             bool                              `json:"capped"`
		ItemActivityCapped bool                              `json:"item_activity_capped"`
	}
	require.NoError(json.NewDecoder(rr.Body).Decode(&body))
	require.Len(body.Items, itemapi.ActivitySafetyCap)
	assert.True(body.Capped)
	assert.True(body.ItemActivityCapped,
		"parents matched only through truncated events must be reported as truncated")
	require.Len(body.ItemActivity, 1)
	assert.Equal(2, body.ItemActivity[0].ItemNumber)
}

func TestAPIListActivityReturnsDefaultBranchActivity(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	srv, database, _ := setupTestServer(t)
	ctx := t.Context()
	base := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	repoID, err := database.UpsertRepo(ctx, serverfake.VerifiedGitHubRepoIdentity("github.com", "acme", "widget"))
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
	rr := testutil.DoJSON(t, srv, http.MethodGet, "/api/v1/activity?since="+since, nil)
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
	assert.Equal(itemapi.FormatUTCRFC3339(detectedAt), forcePush["created_at"])

	commit := body.Items[1]
	assert.Equal("default_branch_commit", commit["activity_type"])
	assert.Equal("main", commit["branch_name"])
	assert.Equal("abc123def456abc123def456abc123def456abcd", commit["commit_sha"])
	assert.Equal("Commit Author", commit["author_name"])
	assert.Equal("author@example.com", commit["author_email"])
	assert.Equal("Committer Person", commit["committer_name"])
	assert.Equal("committer@example.com", commit["committer_email"])
	assert.Equal(itemapi.FormatUTCRFC3339(committedAt), commit["committed_at"])
	assert.Equal("https://github.com/acme/widget/commit/abc123def456abc123def456abc123def456abcd", commit["activity_url"])
	assert.Empty(commit["item_type"])
	assert.Zero(commit["item_number"])
}

func TestAPIListActivityFiltersConfiguredReposByHost(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	srv, database, _, _ := servertest.SetupTestServerWithConfig(t)

	serverfake.SeedPROnHost(t, database, "github.com", "acme", "widget", 1)
	serverfake.SeedPROnHost(t, database, "ghe.example.com", "acme", "widget", 2)

	since := time.Now().UTC().AddDate(0, 0, -7).Format(time.RFC3339)
	rr := testutil.DoJSON(t, srv, http.MethodGet, "/api/v1/activity?since="+since, nil)
	require.Equal(http.StatusOK, rr.Code)
	var body itemapi.ActivityResponse
	require.NoError(json.NewDecoder(rr.Body).Decode(&body))
	require.NotEmpty(body.Items)
	for _, item := range body.Items {
		assert.Equal("github.com", item.PlatformHost)
		assert.Equal("acme", item.RepoOwner)
		assert.Equal("widget", item.RepoName)
	}
}
