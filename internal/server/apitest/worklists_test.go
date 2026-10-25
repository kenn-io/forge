package apitest

import (
	"encoding/json"
	"net/http"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/forge/internal/apiclient/generated"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/mcpserver"
	"go.kenn.io/forge/internal/server/syncevents"
	serverfake "go.kenn.io/forge/internal/testutil/serverfake"
	"go.kenn.io/forge/worklist"
)

func TestAPIWorklistsEmbedListRowsAndReportGoneItems(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	srv, database := setupTestServer(t)
	client := setupTestClient(t, srv)
	ctx := t.Context()
	seedPR(t, database, "acme", "widget", 42, withSeedPRHeadSHA("abc123"))
	seedPR(t, database, "acme", "widget", 7)
	seedIssue(t, database, "acme", "widget", 5, "open")
	repo, err := database.GetRepoByIdentity(ctx, db.GitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	require.NotNil(repo)
	identity := func(itemType string, number int) mcpserver.ItemIdentity {
		return mcpserver.ItemIdentity{
			Type: itemType, Provider: "github", PlatformHost: "github.com",
			RepoKey: repo.Key, Owner: "acme", Name: "widget", Number: number,
		}
	}
	put, err := srv.MCPBackend().PutWorklist(ctx, mcpserver.WorklistPut{
		Title: "Merge today", Recipe: "open PRs with green CI", CreatedBy: "triage-agent",
		Items: []mcpserver.WorklistPutItem{
			{Item: identity("pr", 42), Score: new(9), Section: "Ready", Note: "small fix", Caveat: "breaks the API", CaveatLevel: worklist.CaveatCritical},
			{Item: identity("issue", 5), Caveat: "confirm repro"},
			{Item: identity("pr", 7)},
		},
	})
	require.NoError(err)
	markArchiveItemLifecycle(t, database, repo.ID, db.ArchiveItemTypeMergeRequest, 7, db.ArchiveLifecycleStateRemovedUpstream)

	got, err := client.HTTP.GetWorklistWithResponse(ctx, &generated.GetWorklistRequestOptions{
		PathParams: &generated.GetWorklistPath{ID: put.WorklistID},
	})

	require.NoError(err)
	require.NotNil(got.JSON200, string(got.Body))
	assert.Equal("Merge today", got.JSON200.Title)
	assert.Equal("open PRs with green CI", got.JSON200.Recipe)
	require.Len(got.JSON200.Items, 3)
	ready := got.JSON200.Items[0]
	assert.Equal(generated.WorklistItemResponseItemTypePr, ready.ItemType)
	assert.EqualValues(42, ready.Pull.Number)
	assert.Equal("acme", ready.Repo.Owner)
	assert.Equal("abc123", ready.Snapshot.HeadSha)
	assert.Equal(generated.WorklistItemResponseFreshnessFresh, ready.Freshness)
	assert.EqualValues(5, got.JSON200.Items[1].Issue.Number)
	assert.Equal(generated.WorklistItemResponseCaveatLevelCritical, ready.CaveatLevel)
	assert.Equal("confirm repro", got.JSON200.Items[1].Caveat)
	assert.Equal(generated.WorklistItemResponseCaveatLevelInfo, got.JSON200.Items[1].CaveatLevel, "a caveat without a level is info")
	assert.Equal(generated.WorklistItemResponseFreshnessGone, got.JSON200.Items[2].Freshness)

	var raw struct {
		Items []map[string]json.RawMessage `json:"items"`
	}
	require.NoError(json.Unmarshal(got.Body, &raw))
	require.Len(raw.Items, 3)
	assert.JSONEq(`null`, string(raw.Items[0]["issue"]), "a pull request item carries no issue row")
	assert.JSONEq(`null`, string(raw.Items[1]["pull"]), "an issue item carries no pull row")
	for _, field := range []string{"pull", "issue", "live"} {
		assert.JSONEq(`null`, string(raw.Items[2][field]), "gone item %s", field)
	}
	assert.JSONEq(`9`, string(raw.Items[0]["score"]))
	assert.JSONEq(`null`, string(raw.Items[1]["score"]))
}

func TestAPIWorklistDismissalHidesListUntilRestored(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	srv, database := setupTestServer(t)
	client := setupTestClient(t, srv)
	ctx := t.Context()
	seedIssue(t, database, "acme", "widget", 5, "open")
	repo, err := database.GetRepoByIdentity(ctx, db.GitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	require.NotNil(repo)
	put, err := srv.MCPBackend().PutWorklist(ctx, mcpserver.WorklistPut{
		Title: "Triage", Items: []mcpserver.WorklistPutItem{{Item: mcpserver.ItemIdentity{
			Type: "issue", Provider: "github", PlatformHost: "github.com",
			RepoKey: repo.Key, Owner: "acme", Name: "widget", Number: 5,
		}}},
	})
	require.NoError(err)
	listIDs := func(includeDismissed bool) []int64 {
		listed, err := client.HTTP.ListWorklistsWithResponse(ctx, &generated.ListWorklistsRequestOptions{
			Query: &generated.ListWorklistsQuery{IncludeDismissed: new(includeDismissed)},
		})
		require.NoError(err)
		require.NotNil(listed.JSON200, string(listed.Body))
		ids := []int64{}
		for _, worklist := range listed.JSON200.Worklists {
			ids = append(ids, worklist.ID)
		}
		return ids
	}
	path := func(id int64) *generated.DismissWorklistPath { return &generated.DismissWorklistPath{ID: id} }
	var seen uint64
	changedEvents := func() []string {
		events, _, stale := srv.Hub().ReplaySnapshotSince(seen)
		require.False(stale)
		var payloads []string
		for _, event := range events {
			seen = event.ID
			if event.Event.Type == "worklists_changed" {
				payload, err := json.Marshal(event.Event.Data)
				require.NoError(err)
				payloads = append(payloads, string(payload))
			}
		}
		return payloads
	}
	changedPayload := `{"worklist_id":` + strconv.FormatInt(put.WorklistID, 10) + `}`
	require.Len(changedEvents(), 1, "the put broadcasts")

	dismissed, err := client.HTTP.DismissWorklistWithResponse(ctx, &generated.DismissWorklistRequestOptions{PathParams: path(put.WorklistID)})
	require.NoError(err)
	require.NotNil(dismissed.JSON200, string(dismissed.Body))
	require.NotNil(dismissed.JSON200.DismissedAt)
	assert.Equal([]string{changedPayload}, changedEvents(), "dismissing broadcasts")
	assert.Empty(listIDs(false))
	assert.Equal([]int64{put.WorklistID}, listIDs(true))

	restored, err := client.HTTP.UndismissWorklistWithResponse(ctx, &generated.UndismissWorklistRequestOptions{
		PathParams: &generated.UndismissWorklistPath{ID: put.WorklistID},
	})
	require.NoError(err)
	require.NotNil(restored.JSON200, string(restored.Body))
	assert.Nil(restored.JSON200.DismissedAt)
	assert.Equal([]string{changedPayload}, changedEvents(), "restoring broadcasts")
	assert.Equal([]int64{put.WorklistID}, listIDs(false))

	missing := put.WorklistID + 100
	gotMissing, err := client.HTTP.GetWorklistWithResponse(ctx, &generated.GetWorklistRequestOptions{
		PathParams: &generated.GetWorklistPath{ID: missing},
	})
	require.Error(err)
	require.NotNil(gotMissing)
	require.NotNil(gotMissing.Error, string(gotMissing.Body))
	assert.Equal(http.StatusNotFound, gotMissing.StatusCode)
	assert.Equal(generated.ProblemErrorCode("worklistNotFound"), gotMissing.Error.Code)
	dismissMissing, err := client.HTTP.DismissWorklistWithResponse(ctx, &generated.DismissWorklistRequestOptions{PathParams: path(missing)})
	require.Error(err)
	require.NotNil(dismissMissing)
	assert.Equal(http.StatusNotFound, dismissMissing.StatusCode)
	require.NotNil(dismissMissing.Error, string(dismissMissing.Body))
	assert.Equal(generated.ProblemErrorCode("worklistNotFound"), dismissMissing.Error.Code)
}

func TestAPIWorklistHandledMarksApplyToEveryRequestedItem(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	srv, database := setupTestServer(t)
	client := setupTestClient(t, srv)
	ctx := t.Context()
	seedPR(t, database, "acme", "widget", 42)
	seedPR(t, database, "acme", "widget", 43)
	seedIssue(t, database, "acme", "widget", 5, "open")
	repo, err := database.GetRepoByIdentity(ctx, db.GitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	require.NotNil(repo)
	identity := func(itemType string, number int) mcpserver.ItemIdentity {
		return mcpserver.ItemIdentity{
			Type: itemType, Provider: "github", PlatformHost: "github.com",
			RepoKey: repo.Key, Owner: "acme", Name: "widget", Number: number,
		}
	}
	put, err := srv.MCPBackend().PutWorklist(ctx, mcpserver.WorklistPut{
		Title: "Queue", Items: []mcpserver.WorklistPutItem{
			{Item: identity("pr", 42)}, {Item: identity("issue", 5)}, {Item: identity("pr", 43)},
		},
	})
	require.NoError(err)
	read, err := client.HTTP.GetWorklistWithResponse(ctx, &generated.GetWorklistRequestOptions{
		PathParams: &generated.GetWorklistPath{ID: put.WorklistID},
	})
	require.NoError(err)
	require.NotNil(read.JSON200, string(read.Body))
	ref := func(item generated.WorklistItemResponse) generated.WorklistItemRef {
		return generated.WorklistItemRef{
			Provider: item.Repo.Provider, PlatformHost: item.Repo.PlatformHost,
			PlatformRepoID: item.Repo.PlatformRepoID, BitbucketRepositoryUUID: item.Repo.BitbucketRepositoryUUID,
			ItemType: generated.WorklistItemRefItemType(item.ItemType), Number: item.Number,
		}
	}
	items := read.JSON200.Items
	setHandled := func(handled bool, refs ...generated.WorklistItemRef) (*generated.SetWorklistItemsHandledResp, error) {
		return client.HTTP.SetWorklistItemsHandledWithResponse(ctx, &generated.SetWorklistItemsHandledRequestOptions{
			PathParams: &generated.SetWorklistItemsHandledPath{ID: put.WorklistID},
			Body:       &generated.SetWorklistItemsHandledBody{Handled: handled, Items: refs},
		})
	}
	handledNumbers := func() []int64 {
		got, err := client.HTTP.GetWorklistWithResponse(ctx, &generated.GetWorklistRequestOptions{
			PathParams: &generated.GetWorklistPath{ID: put.WorklistID},
		})
		require.NoError(err)
		require.NotNil(got.JSON200, string(got.Body))
		numbers := []int64{}
		for _, item := range got.JSON200.Items {
			if item.HandledAt != nil {
				numbers = append(numbers, item.Number)
			}
		}
		return numbers
	}
	before := len(worklistChangedEvents(t, srv))

	marked, err := setHandled(true, ref(items[0]), ref(items[1]))

	require.NoError(err)
	require.NotNil(marked.JSON200, string(marked.Body))
	assert.EqualValues(2, marked.JSON200.Worklist.HandledCount)
	assert.EqualValues(1, marked.JSON200.Worklist.RemainingCount)
	require.Len(marked.JSON200.Items, 2)
	assert.EqualValues(42, marked.JSON200.Items[0].Number)
	assert.NotNil(marked.JSON200.Items[0].HandledAt)
	assert.EqualValues(5, marked.JSON200.Items[1].Number)
	assert.Equal(generated.WorklistItemMarkResponseItemTypeIssue, marked.JSON200.Items[1].ItemType)
	assert.NotNil(marked.JSON200.Items[1].HandledAt)
	assert.Equal([]int64{42, 5}, handledNumbers())
	changed := worklistChangedEvents(t, srv)
	require.Len(changed, before+1, "a mark broadcasts")
	assert.JSONEq(`{"worklist_id":`+strconv.FormatInt(put.WorklistID, 10)+`}`, changed[before])

	unmarked, err := setHandled(false, ref(items[1]))
	require.NoError(err)
	require.NotNil(unmarked.JSON200, string(unmarked.Body))
	assert.EqualValues(1, unmarked.JSON200.Worklist.HandledCount)
	assert.EqualValues(2, unmarked.JSON200.Worklist.RemainingCount)
	assert.Nil(unmarked.JSON200.Items[0].HandledAt)
	assert.Equal([]int64{42}, handledNumbers())

	notOnList := ref(items[2])
	notOnList.Number = 99
	rejected, err := setHandled(true, ref(items[2]), notOnList)
	require.Error(err)
	require.NotNil(rejected)
	assert.Equal(http.StatusNotFound, rejected.StatusCode)
	require.NotNil(rejected.Error, string(rejected.Body))
	assert.Equal(generated.ProblemErrorCode("notFound"), rejected.Error.Code)
	require.NotNil(rejected.Error.Details)
	assert.EqualValues(1, rejected.Error.Details["item_index"])
	assert.Equal([]int64{42}, handledNumbers(), "a rejected request marks nothing")
	assert.Len(worklistChangedEvents(t, srv), before+2, "a rejected request does not broadcast")

	unknownRepo := ref(items[0])
	unknownRepo.PlatformRepoID = new(int64(987654))
	rejected, err = setHandled(true, unknownRepo)
	require.Error(err)
	require.NotNil(rejected.Error, string(rejected.Body))
	assert.Equal(generated.ProblemErrorCode("repoNotFound"), rejected.Error.Code)
	assert.EqualValues(0, rejected.Error.Details["item_index"])
}

func worklistChangedEvents(t *testing.T, srv interface {
	Hub() *syncevents.EventHub
},
) []string {
	t.Helper()
	events, _, stale := srv.Hub().ReplaySnapshotSince(0)
	require.False(t, stale)
	var payloads []string
	for _, event := range events {
		if event.Event.Type == "worklists_changed" {
			payload, err := json.Marshal(event.Event.Data)
			require.NoError(t, err)
			payloads = append(payloads, string(payload))
		}
	}
	return payloads
}
