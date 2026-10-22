package apitest

import (
	"bytes"
	"compress/flate"
	"encoding/base64"
	"encoding/json"
	json2 "encoding/json/v2"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/forge/internal/apiclient"
	"go.kenn.io/forge/internal/apiclient/generated"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/mcpserver"
	serverfake "go.kenn.io/forge/internal/testutil/serverfake"
	"go.kenn.io/forge/platform"
	"go.kenn.io/forge/worklist"
)

func TestAPIWorklistImportCreatesTrackedItemsAndSkipsTheRest(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	srv, database := setupTestServer(t)
	client := setupTestClient(t, srv)
	ctx := t.Context()
	seedPR(t, database, "acme", "widget", 42)
	seedIssue(t, database, "acme", "widget", 5, "open")
	ref := func(itemType, owner, name string, number int) worklist.ItemRef {
		return worklist.ItemRef{Type: itemType, Provider: "github", Owner: owner, Name: name, Number: number}
	}
	fragment, err := worklist.Fragment(worklist.List{
		Title: "Merge today", Recipe: "open PRs with green CI", CreatedBy: "triage-bot",
		Items: []worklist.Item{
			{Item: ref("pr", "acme", "widget", 42), Score: new(9), Section: "Ready", Note: "small fix"},
			{Item: ref("pr", "other", "repo", 1), Note: "repository this Forge does not track"},
			{Item: ref("issue", "acme", "widget", 5), Caveat: "confirm repro"},
			{Item: ref("pr", "acme", "widget", 999)},
			{Item: ref("pr", "acme", "widget", 42), Note: "listed twice"},
		},
	})
	require.NoError(err)

	imported, err := client.HTTP.ImportWorklistWithResponse(ctx, &generated.ImportWorklistRequestOptions{
		Body: &generated.ImportWorklistBody{Fragment: fragment},
	})

	require.NoError(err)
	require.NotNil(imported.JSON200, string(imported.Body))
	assert.EqualValues(2, imported.JSON200.ItemCount)
	assert.EqualValues(3, imported.JSON200.SkippedCount)
	got, err := client.HTTP.GetWorklistWithResponse(ctx, &generated.GetWorklistRequestOptions{
		PathParams: &generated.GetWorklistPath{ID: imported.JSON200.WorklistID},
	})
	require.NoError(err)
	require.NotNil(got.JSON200, string(got.Body))
	assert.Equal("Merge today", got.JSON200.Title)
	assert.Equal("open PRs with green CI", got.JSON200.Recipe)
	assert.Equal("triage-bot", got.JSON200.CreatedBy)
	require.Len(got.JSON200.Items, 2)
	assert.EqualValues(42, got.JSON200.Items[0].Number)
	assert.Equal("Ready", got.JSON200.Items[0].Section)
	assert.Equal("small fix", got.JSON200.Items[0].Note)
	assert.EqualValues(5, got.JSON200.Items[1].Number)
	assert.Equal("confirm repro", got.JSON200.Items[1].Caveat)
}

func TestAPIWorklistImportReturnsTheSameListForTheSameLink(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	srv, database := setupTestServer(t)
	client := setupTestClient(t, srv)
	ctx := t.Context()
	seedPR(t, database, "acme", "widget", 42)
	list := worklist.List{Title: "Merge today", Items: []worklist.Item{{
		Item: worklist.ItemRef{Type: "pr", Provider: "github", Owner: "acme", Name: "widget", Number: 42},
	}}}
	fragment, err := worklist.Fragment(list)
	require.NoError(err)
	// The same list as another producer might encode it, without compression.
	data, err := json2.Marshal(list)
	require.NoError(err)
	var stored bytes.Buffer
	writer, err := flate.NewWriter(&stored, flate.NoCompression)
	require.NoError(err)
	_, err = writer.Write(data)
	require.NoError(err)
	require.NoError(writer.Close())
	uncompressed := "v1." + base64.RawURLEncoding.EncodeToString(stored.Bytes())
	require.NotEqual(fragment, uncompressed)
	other, err := worklist.Fragment(worklist.List{Title: "Merge tomorrow", Items: list.Items})
	require.NoError(err)
	importLink := func(fragment string) int64 {
		imported, err := client.HTTP.ImportWorklistWithResponse(ctx, &generated.ImportWorklistRequestOptions{
			Body: &generated.ImportWorklistBody{Fragment: fragment},
		})
		require.NoError(err)
		require.NotNil(imported.JSON200, string(imported.Body))
		return imported.JSON200.WorklistID
	}

	first := importLink(fragment)
	assert.Equal(first, importLink(fragment), "opening a link again returns its list")
	dismissed, err := client.HTTP.DismissWorklistWithResponse(ctx, &generated.DismissWorklistRequestOptions{
		PathParams: &generated.DismissWorklistPath{ID: first},
	})
	require.NoError(err)
	require.NotNil(dismissed.JSON200, string(dismissed.Body))
	assert.Equal(first, importLink(uncompressed), "the same list encoded differently is the same link")
	got, err := client.HTTP.GetWorklistWithResponse(ctx, &generated.GetWorklistRequestOptions{
		PathParams: &generated.GetWorklistPath{ID: first},
	})
	require.NoError(err)
	require.NotNil(got.JSON200, string(got.Body))
	assert.Nil(got.JSON200.DismissedAt, "reopening a dismissed list's link restores it")
	assert.NotEqual(first, importLink(other), "a different list is a new list")
	listed, err := client.HTTP.ListWorklistsWithResponse(ctx, &generated.ListWorklistsRequestOptions{})
	require.NoError(err)
	require.NotNil(listed.JSON200, string(listed.Body))
	assert.Len(listed.JSON200.Worklists, 2)
}

func TestAPIWorklistImportReopensItsListAfterTheListChanges(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	srv, database := setupTestServer(t)
	client := setupTestClient(t, srv)
	ctx := t.Context()
	seedPR(t, database, "acme", "widget", 42)
	seedPR(t, database, "acme", "widget", 7)
	repo, err := database.GetRepoByIdentity(ctx, db.GitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	require.NotNil(repo)
	ref := func(number int) worklist.ItemRef {
		return worklist.ItemRef{Type: "pr", Provider: "github", Owner: "acme", Name: "widget", Number: number}
	}
	fragment, err := worklist.Fragment(worklist.List{
		Title: "Merge today", Items: []worklist.Item{{Item: ref(42)}, {Item: ref(7)}},
	})
	require.NoError(err)
	importLink := func() generated.WorklistImportResponse {
		imported, err := client.HTTP.ImportWorklistWithResponse(ctx, &generated.ImportWorklistRequestOptions{
			Body: &generated.ImportWorklistBody{Fragment: fragment},
		})
		require.NoError(err)
		require.NotNil(imported.JSON200, string(imported.Body))
		return *imported.JSON200
	}
	first := importLink()
	_, err = srv.MCPBackend().PutWorklist(ctx, mcpserver.WorklistPut{
		WorklistID: first.WorklistID, Title: "Merge today",
		Items: []mcpserver.WorklistPutItem{{Item: mcpserver.ItemIdentity{
			Type: "pr", Provider: "github", PlatformHost: "github.com",
			RepoKey: repo.Key, Owner: "acme", Name: "widget", Number: 42,
		}}},
	})
	require.NoError(err)

	rewritten := importLink()

	assert.Equal(first.WorklistID, rewritten.WorklistID, "the link follows its list after an agent rewrite")
	assert.EqualValues(2, rewritten.ItemCount, "counts describe the link, not the rewritten list")
	assert.EqualValues(0, rewritten.SkippedCount)

	dismissed, err := client.HTTP.DismissWorklistWithResponse(ctx, &generated.DismissWorklistRequestOptions{
		PathParams: &generated.DismissWorklistPath{ID: first.WorklistID},
	})
	require.NoError(err)
	require.NotNil(dismissed.JSON200, string(dismissed.Body))
	markArchiveItemLifecycle(t, database, repo.ID, db.ArchiveItemTypeMergeRequest, 42, db.ArchiveLifecycleStateRemovedUpstream)
	markArchiveItemLifecycle(t, database, repo.ID, db.ArchiveItemTypeMergeRequest, 7, db.ArchiveLifecycleStateRemovedUpstream)

	gone := importLink()

	assert.Equal(first.WorklistID, gone.WorklistID, "a link whose items are gone still opens its list")
	assert.EqualValues(0, gone.ItemCount)
	assert.EqualValues(2, gone.SkippedCount)
	got, err := client.HTTP.GetWorklistWithResponse(ctx, &generated.GetWorklistRequestOptions{
		PathParams: &generated.GetWorklistPath{ID: first.WorklistID},
	})
	require.NoError(err)
	require.NotNil(got.JSON200, string(got.Body))
	assert.Nil(got.JSON200.DismissedAt, "reopening the link restores the dismissed list")
}

func TestAPIWorklistImportRejectsLinksItCannotUse(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	srv, database := setupTestServer(t)
	client := setupTestClient(t, srv)
	ctx := t.Context()
	seedPR(t, database, "acme", "widget", 42)
	untracked, err := worklist.Fragment(worklist.List{Title: "Elsewhere", Items: []worklist.Item{{
		Item: worklist.ItemRef{Type: "pr", Provider: "github", Owner: "other", Name: "repo", Number: 1},
	}}})
	require.NoError(t, err)
	tests := []struct {
		name     string
		fragment string
	}{
		{"unreadable fragment", "v1.not-a-worklist"},
		{"unknown version", "v9.AAAA"},
		{"no tracked item", untracked},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			imported, err := client.HTTP.ImportWorklistWithResponse(ctx, &generated.ImportWorklistRequestOptions{
				Body: &generated.ImportWorklistBody{Fragment: tt.fragment},
			})

			require.Error(t, err)
			require.NotNil(t, imported)
			assert.Equal(t, http.StatusBadRequest, imported.StatusCode, string(imported.Body))
			var problem struct {
				Code    string         `json:"code"`
				Details map[string]any `json:"details"`
			}
			require.NoError(t, json.Unmarshal(imported.Body, &problem))
			assert.Equal(t, string(generated.ProblemErrorCodeValidationError), problem.Code)
			assert.Equal(t, "body.fragment", problem.Details["field"])
		})
	}
	listed, err := client.HTTP.ListWorklistsWithResponse(ctx, &generated.ListWorklistsRequestOptions{})
	require.NoError(t, err)
	require.NotNil(t, listed.JSON200, string(listed.Body))
	assert.Empty(t, listed.JSON200.Worklists, "a rejected import writes nothing")
}

func TestAPIWorklistLinkOpensAnEquivalentListInAnotherForge(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	sender, senderDB := setupTestServer(t)
	senderClient := setupTestClient(t, sender)
	seedPR(t, senderDB, "acme", "widget", 42)
	seedIssue(t, senderDB, "acme", "widget", 5, "open")
	repo, err := senderDB.GetRepoByIdentity(ctx, db.GitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	require.NotNil(repo)
	identity := func(itemType string, number int) mcpserver.ItemIdentity {
		return mcpserver.ItemIdentity{
			Type: itemType, Provider: "github", PlatformHost: "github.com",
			RepoKey: repo.Key, Owner: "acme", Name: "widget", Number: number,
		}
	}
	put, err := sender.MCPBackend().PutWorklist(ctx, mcpserver.WorklistPut{
		Title: "Merge today", Recipe: "open PRs with green CI", CreatedBy: "triage-bot",
		Items: []mcpserver.WorklistPutItem{
			{Item: identity("pr", 42), Score: new(9), Section: "Ready", Note: "small fix", Caveat: "check the migration"},
			{Item: identity("issue", 5), Section: "Triage", Note: "needs a repro"},
		},
	})
	require.NoError(err)
	platformRepoID, ok := repo.Key.ID()
	require.True(ok)
	marked, err := senderClient.HTTP.SetWorklistItemsHandledWithResponse(ctx, &generated.SetWorklistItemsHandledRequestOptions{
		PathParams: &generated.SetWorklistItemsHandledPath{ID: put.WorklistID},
		Body: &generated.SetWorklistItemsHandledBody{Handled: true, Items: []generated.WorklistItemRef{{
			Provider: "github", PlatformHost: "github.com", PlatformRepoID: &platformRepoID,
			ItemType: generated.WorklistItemRefItemTypePr, Number: 42,
		}}},
	})
	require.NoError(err)
	require.NotNil(marked.JSON200, string(marked.Body))

	linked, err := senderClient.HTTP.GetWorklistLinkWithResponse(ctx, &generated.GetWorklistLinkRequestOptions{
		PathParams: &generated.GetWorklistLinkPath{ID: put.WorklistID},
	})
	require.NoError(err)
	require.NotNil(linked.JSON200, string(linked.Body))
	fragment, ok := strings.CutPrefix(linked.JSON200.Link, worklist.KennImportURL+"#")
	require.True(ok, linked.JSON200.Link)
	carried, err := worklist.ParseFragment(fragment)
	require.NoError(err)
	assert.Equal(worklist.ItemRef{
		Type: "pr", Provider: "github", PlatformHost: "github.com", Owner: "acme", Name: "widget", Number: 42,
	}, carried.Items[0].Item, "a link names the repository by route, not by this Forge's identity")

	recipient, recipientDB := setupTestServer(t)
	recipientClient := setupTestClient(t, recipient)
	seedPR(t, recipientDB, "acme", "widget", 42)
	seedIssue(t, recipientDB, "acme", "widget", 5, "open")
	imported, err := recipientClient.HTTP.ImportWorklistWithResponse(ctx, &generated.ImportWorklistRequestOptions{
		Body: &generated.ImportWorklistBody{Fragment: fragment},
	})
	require.NoError(err)
	require.NotNil(imported.JSON200, string(imported.Body))
	assert.EqualValues(2, imported.JSON200.ItemCount)
	assert.EqualValues(0, imported.JSON200.SkippedCount)

	read := func(client *apiclient.Client, id int64) generated.WorklistResponse {
		got, err := client.HTTP.GetWorklistWithResponse(ctx, &generated.GetWorklistRequestOptions{
			PathParams: &generated.GetWorklistPath{ID: id},
		})
		require.NoError(err)
		require.NotNil(got.JSON200, string(got.Body))
		return *got.JSON200
	}
	original, copied := read(senderClient, put.WorklistID), read(recipientClient, imported.JSON200.WorklistID)
	assert.Equal(original.Title, copied.Title)
	assert.Equal(original.Recipe, copied.Recipe)
	assert.Equal(original.CreatedBy, copied.CreatedBy)
	require.Len(copied.Items, len(original.Items))
	for i, item := range copied.Items {
		want := original.Items[i]
		assert.Equal(want.ItemType, item.ItemType)
		assert.Equal(want.Number, item.Number)
		assert.Equal(want.Repo.Owner+"/"+want.Repo.Name, item.Repo.Owner+"/"+item.Repo.Name)
		assert.Equal(want.Score, item.Score)
		assert.Equal(want.Section, item.Section)
		assert.Equal(want.Note, item.Note)
		assert.Equal(want.Caveat, item.Caveat)
		assert.Nil(item.HandledAt, "handled marks stay with the sender")
	}
	assert.NotNil(original.Items[0].HandledAt)
}

func TestAPIWorklistLinkRejectsAListWithoutItems(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	srv, database := setupTestServer(t)
	client := setupTestClient(t, srv)
	ctx := t.Context()
	seedPR(t, database, "acme", "widget", 42)
	fragment, err := worklist.Fragment(worklist.List{Title: "Merge today", Items: []worklist.Item{{
		Item: worklist.ItemRef{Type: "pr", Provider: "github", Owner: "acme", Name: "widget", Number: 42},
	}}})
	require.NoError(t, err)
	imported, err := client.HTTP.ImportWorklistWithResponse(ctx, &generated.ImportWorklistRequestOptions{
		Body: &generated.ImportWorklistBody{Fragment: fragment},
	})
	require.NoError(t, err)
	require.NotNil(t, imported.JSON200, string(imported.Body))
	// Deleting the repository row deletes its items from the list.
	_, err = database.WriteDB().ExecContext(ctx, `DELETE FROM forge_repos WHERE owner = 'acme' AND name = 'widget'`)
	require.NoError(t, err)

	linked, err := client.HTTP.GetWorklistLinkWithResponse(ctx, &generated.GetWorklistLinkRequestOptions{
		PathParams: &generated.GetWorklistLinkPath{ID: imported.JSON200.WorklistID},
	})

	require.Error(t, err)
	require.NotNil(t, linked)
	assert.Equal(t, http.StatusConflict, linked.StatusCode, string(linked.Body))
}

func TestAPIWorklistLinkRefusesARepositoryWhoseRouteWasReused(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	require := require.New(t)
	ctx := t.Context()
	srv, database := setupTestServer(t)
	client := setupTestClient(t, srv)
	seedPR(t, database, "acme", "widget", 42)
	repo, err := database.GetRepoByIdentity(ctx, db.GitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	require.NotNil(repo)
	put, err := srv.MCPBackend().PutWorklist(ctx, mcpserver.WorklistPut{
		Title: "Merge today",
		Items: []mcpserver.WorklistPutItem{{Item: mcpserver.ItemIdentity{
			Type: "pr", Provider: "github", PlatformHost: "github.com",
			RepoKey: repo.Key, Owner: "acme", Name: "widget", Number: 42,
		}}},
	})
	require.NoError(err)
	// Another repository takes acme/widget; the listed one keeps that route
	// only for display.
	platformRepoID, ok := repo.Key.ID()
	require.True(ok)
	replacement := db.GitHubRepoIdentity("github.com", "acme", "widget")
	replacement.Key = platform.RepositoryIDKey(platformRepoID + 1)
	_, err = database.ObserveRepository(ctx, replacement)
	require.NoError(err)

	linked, err := client.HTTP.GetWorklistLinkWithResponse(ctx, &generated.GetWorklistLinkRequestOptions{
		PathParams: &generated.GetWorklistLinkPath{ID: put.WorklistID},
	})

	require.Error(err)
	require.NotNil(linked)
	assert.Equal(t, http.StatusConflict, linked.StatusCode, string(linked.Body))
	assert.Contains(t, string(linked.Body), "no longer holds its route")
}
