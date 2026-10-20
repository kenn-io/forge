package server

import (
	"context"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/federationauth"
	"go.kenn.io/forge/internal/mcpserver"
	"go.kenn.io/forge/internal/providerplane"
	"go.kenn.io/forge/internal/server/httpapi"
	"go.kenn.io/forge/internal/server/spokeapi"
	"go.kenn.io/forge/internal/server/worklistapi"
	"go.kenn.io/forge/internal/testutil"
	"go.kenn.io/forge/internal/testutil/dbtest"
	serverfake "go.kenn.io/forge/internal/testutil/serverfake"
	"go.kenn.io/forge/platform"
)

func worklistTestItem(repo *db.ActiveRepo, itemType string, number int) mcpserver.ItemIdentity {
	return mcpserver.ItemIdentity{
		Type: itemType, Provider: "github", PlatformHost: "github.com",
		RepoKey: repo.Key, Owner: repo.Owner, Name: repo.Name, Number: number,
	}
}

func TestMCPWorklistPutWritesHubStateAndBroadcasts(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	srv, database, _ := setupTestServer(t)
	ctx := t.Context()
	serverfake.SeedPR(t, database, "acme", "widget", 42, serverfake.WithSeedPRHeadSHA("abc123"))
	serverfake.SeedIssue(t, database, "acme", "widget", 5, "open")
	repo, err := database.GetRepoByIdentity(ctx, serverfake.VerifiedGitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	require.NotNil(repo)
	backend := srv.MCPBackend()

	result, err := backend.PutWorklist(ctx, mcpserver.WorklistPut{
		Title: "Merge today", Recipe: "green CI", CreatedBy: "triage-agent", WorkspaceID: "ws-chat",
		Items: []mcpserver.WorklistPutItem{
			{Item: worklistTestItem(repo, "pr", 42), Score: new(9), Section: "Ready"},
			{Item: worklistTestItem(repo, "issue", 5), Note: "repro attached"},
		},
	})

	require.NoError(err)
	assert.True(result.Created)
	assert.Equal(2, result.ItemCount)
	stored, err := database.GetWorklist(ctx, result.WorklistID, nil)
	require.NoError(err)
	assert.Equal("ws-chat", stored.WorkspaceID)
	require.Len(stored.Items, 2)
	assert.Equal("abc123", stored.Items[0].Snapshot.HeadSHA)
	assert.Equal(db.ItemTypeIssue, stored.Items[1].ItemType)
	events, _, stale := srv.Hub().ReplaySnapshotSince(0)
	require.False(stale)
	var published []string
	for _, event := range events {
		if event.Event.Type == "worklists_changed" {
			payload, err := jsonv2.Marshal(event.Event.Data)
			require.NoError(err)
			published = append(published, string(payload))
		}
	}
	require.Len(published, 1)
	assert.JSONEq(`{"worklist_id":`+strconv.FormatInt(result.WorklistID, 10)+`}`, published[0])

	read, err := backend.GetWorklist(ctx, result.WorklistID)
	require.NoError(err)
	require.Len(read.Items, 2)
	assert.Equal(worklistTestItem(repo, "pr", 42), read.Items[0].Item)
	assert.Equal("fresh", read.Items[0].Freshness)
}

func countWorklistChangedEvents(t *testing.T, srv *Server) int {
	t.Helper()
	events, _, stale := srv.Hub().ReplaySnapshotSince(0)
	require.False(t, stale)
	count := 0
	for _, event := range events {
		if event.Event.Type == worklistapi.ChangedEvent {
			count++
		}
	}
	return count
}

func TestMCPClearWorklistMarksUnmarksItemsAndBroadcasts(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	srv, database, _ := setupTestServer(t)
	ctx := t.Context()
	serverfake.SeedPR(t, database, "acme", "widget", 42)
	serverfake.SeedIssue(t, database, "acme", "widget", 5, "open")
	repo, err := database.GetRepoByIdentity(ctx, serverfake.VerifiedGitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	require.NotNil(repo)
	backend := srv.MCPBackend()
	put, err := backend.PutWorklist(ctx, mcpserver.WorklistPut{
		Title: "Queue", Items: []mcpserver.WorklistPutItem{
			{Item: worklistTestItem(repo, "pr", 42)}, {Item: worklistTestItem(repo, "issue", 5)},
		},
	})
	require.NoError(err)
	require.NoError(database.SetWorklistItemsHandled(ctx, put.WorklistID, []db.WorklistItemKey{
		{RepoID: repo.ID, ItemType: db.ItemTypePR, ItemNumber: 42},
		{RepoID: repo.ID, ItemType: db.ItemTypeIssue, ItemNumber: 5},
	}, true, time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)))
	before := countWorklistChangedEvents(t, srv)

	cleared, err := backend.ClearWorklistMarks(ctx, put.WorklistID)

	require.NoError(err)
	assert.Equal(mcpserver.WorklistMarksCleared{WorklistID: put.WorklistID, Cleared: 2}, cleared)
	read, err := backend.GetWorklist(ctx, put.WorklistID)
	require.NoError(err)
	assert.Nil(read.Items[0].HandledAt)
	assert.Nil(read.Items[1].HandledAt)
	assert.Equal(before+1, countWorklistChangedEvents(t, srv))
	_, err = backend.ClearWorklistMarks(ctx, put.WorklistID+10)
	var backendErr *mcpserver.Error
	require.ErrorAs(err, &backendErr)
	assert.Equal(string(httpapi.CodeWorklistNotFound), backendErr.Code)
}

func TestMCPWorklistPutRejectsWholeListForUnknownItem(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	srv, database, _ := setupTestServer(t)
	ctx := t.Context()
	serverfake.SeedPR(t, database, "acme", "widget", 42)
	repo, err := database.GetRepoByIdentity(ctx, serverfake.VerifiedGitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(t, err)
	require.NotNil(t, repo)
	backend := srv.MCPBackend()
	existing, err := backend.PutWorklist(ctx, mcpserver.WorklistPut{
		Title: "Original", Items: []mcpserver.WorklistPutItem{{Item: worklistTestItem(repo, "pr", 42)}},
	})
	require.NoError(t, err)
	unknownRepo := worklistTestItem(repo, "pr", 42)
	unknownRepo.RepoKey = platform.RepositoryIDKey(987654)

	tests := []struct {
		name     string
		put      mcpserver.WorklistPut
		wantCode httpapi.ProblemCode
		details  map[string]any
	}{
		{
			name: "pull not synced",
			put: mcpserver.WorklistPut{WorklistID: existing.WorklistID, Title: "Replaced", Items: []mcpserver.WorklistPutItem{
				{Item: worklistTestItem(repo, "pr", 42)}, {Item: worklistTestItem(repo, "pr", 77)},
			}},
			wantCode: httpapi.CodePullNotFound, details: map[string]any{"item_index": 1},
		},
		{
			name: "repository not tracked",
			put: mcpserver.WorklistPut{Title: "New", Items: []mcpserver.WorklistPutItem{
				{Item: worklistTestItem(repo, "pr", 42)}, {Item: unknownRepo},
			}},
			wantCode: httpapi.CodeRepoNotFound, details: map[string]any{"item_index": 1},
		},
		{
			name: "missing worklist",
			put: mcpserver.WorklistPut{WorklistID: existing.WorklistID + 50, Title: "Ghost", Items: []mcpserver.WorklistPutItem{
				{Item: worklistTestItem(repo, "pr", 42)},
			}},
			wantCode: httpapi.CodeWorklistNotFound, details: map[string]any{"worklist_id": existing.WorklistID + 50},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := backend.PutWorklist(ctx, tt.put)

			var backendErr *mcpserver.Error
			require.ErrorAs(t, err, &backendErr)
			assert.Equal(t, "not_found", backendErr.Kind)
			assert.Equal(t, string(tt.wantCode), backendErr.Code)
			for key, value := range tt.details {
				assert.EqualValues(t, value, backendErr.Details[key], key)
			}
		})
	}

	summaries, err := database.ListWorklists(ctx, true, nil)
	require.NoError(t, err)
	require.Len(t, summaries, 1)
	assert.Equal(t, "Original", summaries[0].Title)
	assert.Equal(t, 1, summaries[0].ItemCount)
}

func TestFederatedWorklistsHaveOneHubOwner(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	hubDB := dbtest.Open(t)
	serverfake.SeedPR(t, hubDB, "acme", "widget", 42)
	repo, err := hubDB.GetRepoByIdentity(
		t.Context(), serverfake.VerifiedGitHubRepoIdentity("github.com", "acme", "widget"),
	)
	require.NoError(err)
	require.NotNil(repo)
	hubServer := New(hubDB, nil, nil, "/", nil, ServerOptions{
		DisableWorkspaceBackgroundMonitors: true,
	})
	t.Cleanup(func() { serverfake.GracefulShutdown(t, hubServer) })
	hub := httptest.NewTLSServer(hubServer)
	t.Cleanup(hub.Close)
	nodeA, nodeADB := newFederatedProviderNodeForTest(
		t, "99999999999999999999999999999991", hub.URL, hub.Client(),
	)
	nodeB, _ := newFederatedProviderNodeForTest(
		t, "99999999999999999999999999999992", hub.URL, hub.Client(),
	)

	put, err := nodeA.MCPBackend().PutWorklist(t.Context(), mcpserver.WorklistPut{
		Title: "Merge today", Recipe: "green CI", CreatedBy: "spoke-a", WorkspaceID: "ws-a",
		Items: []mcpserver.WorklistPutItem{{Item: worklistTestItem(repo, "pr", 42), Score: new(7), Caveat: "check docs"}},
	})
	require.NoError(err)
	assert.True(put.Created)

	listed, err := nodeB.MCPBackend().ListWorklists(t.Context(), false)
	require.NoError(err)
	require.Len(listed, 1)
	assert.Equal("Merge today", listed[0].Title)
	read, err := nodeB.MCPBackend().GetWorklist(t.Context(), put.WorklistID)
	require.NoError(err)
	require.Len(read.Items, 1)
	assert.Equal(worklistTestItem(repo, "pr", 42), read.Items[0].Item)
	assert.Equal(new(7), read.Items[0].Score)
	assert.Equal("check docs", read.Items[0].Caveat)
	assert.Nil(read.Items[0].HandledAt)

	markedAt := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	require.NoError(hubDB.SetWorklistItemsHandled(t.Context(), put.WorklistID, []db.WorklistItemKey{
		{RepoID: repo.ID, ItemType: db.ItemTypePR, ItemNumber: 42},
	}, true, markedAt))
	read, err = nodeB.MCPBackend().GetWorklist(t.Context(), put.WorklistID)
	require.NoError(err)
	assert.Equal(new("2026-03-04T05:06:07Z"), read.Items[0].HandledAt)
	listed, err = nodeB.MCPBackend().ListWorklists(t.Context(), false)
	require.NoError(err)
	assert.Equal(1, listed[0].HandledCount)
	assert.Equal(0, listed[0].RemainingCount)
	cleared, err := nodeA.MCPBackend().ClearWorklistMarks(t.Context(), put.WorklistID)
	require.NoError(err)
	assert.Equal(mcpserver.WorklistMarksCleared{WorklistID: put.WorklistID, Cleared: 1}, cleared)
	hubCopy, err := hubDB.GetWorklist(t.Context(), put.WorklistID, nil)
	require.NoError(err)
	assert.Nil(hubCopy.Items[0].HandledAt, "the spoke clears the hub's marks")
	_, err = nodeA.MCPBackend().ClearWorklistMarks(t.Context(), put.WorklistID+10)
	var clearErr *mcpserver.Error
	require.ErrorAs(err, &clearErr)
	assert.Equal(string(httpapi.CodeWorklistNotFound), clearErr.Code)

	dismissal, err := nodeB.MCPBackend().DismissWorklist(t.Context(), put.WorklistID)
	require.NoError(err)
	assert.NotEmpty(dismissal.DismissedAt)
	stored, err := hubDB.GetWorklistSummary(t.Context(), put.WorklistID, nil)
	require.NoError(err)
	assert.NotNil(stored.DismissedAt)
	hidden, err := nodeA.MCPBackend().ListWorklists(t.Context(), false)
	require.NoError(err)
	assert.Empty(hidden)
	all, err := nodeA.MCPBackend().ListWorklists(t.Context(), true)
	require.NoError(err)
	assert.Len(all, 1)

	_, err = nodeA.MCPBackend().PutWorklist(t.Context(), mcpserver.WorklistPut{
		WorklistID: put.WorklistID, Title: "Replaced",
		Items: []mcpserver.WorklistPutItem{{Item: worklistTestItem(repo, "pr", 42)}, {Item: worklistTestItem(repo, "pr", 77)}},
	})
	var putErr *mcpserver.Error
	require.ErrorAs(err, &putErr)
	assert.Equal(string(httpapi.CodePullNotFound), putErr.Code)
	assert.EqualValues(1, putErr.Details["item_index"], "the hub's item index survives the spoke")
	unchanged, err := hubDB.GetWorklistSummary(t.Context(), put.WorklistID, nil)
	require.NoError(err)
	assert.Equal("Merge today", unchanged.Title)

	local, err := nodeADB.ListWorklists(t.Context(), true, nil)
	require.NoError(err)
	assert.Empty(local, "spokes never write worklists locally")
	_, err = nodeA.MCPBackend().GetWorklist(t.Context(), put.WorklistID+10)
	var backendErr *mcpserver.Error
	require.ErrorAs(err, &backendErr)
	assert.Equal(string(httpapi.CodeWorklistNotFound), backendErr.Code)
}

func TestSpokeWorklistsDoNotFallBackWhenHubIsUnavailable(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	database := dbtest.Open(t)
	serverfake.SeedPR(t, database, "acme", "widget", 42)
	repo, err := database.GetRepoByIdentity(t.Context(), serverfake.VerifiedGitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	require.NotNil(repo)
	local, err := database.PutWorklist(t.Context(), db.PutWorklistParams{
		Title: "Stale local copy", Now: time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC),
		Items: []db.WorklistItemParams{{RepoID: repo.ID, ItemType: db.ItemTypePR, ItemNumber: 42}},
	})
	require.NoError(err)
	localMark := time.Date(2026, 3, 4, 6, 0, 0, 0, time.UTC)
	require.NoError(database.SetWorklistItemsHandled(t.Context(), local.ID, []db.WorklistItemKey{
		{RepoID: repo.ID, ItemType: db.ItemTypePR, ItemNumber: 42},
	}, true, localMark))
	var scopes []federationauth.Scope
	client := providerPlaneClientFunc(func(
		_ context.Context, scope federationauth.Scope, _ *http.Request,
	) (*http.Response, error) {
		scopes = append(scopes, scope)
		return nil, providerplane.ErrHubUnavailable
	})
	srv := wiredServer(&Server{db: database, providerSource: &spokeapi.HubProviderSource{Client: client}})
	backend := srv.MCPBackend()

	_, err = backend.GetWorklist(t.Context(), local.ID)
	var readErr *mcpserver.Error
	require.ErrorAs(err, &readErr)
	assert.Equal(string(httpapi.CodeHubUnavailable), readErr.Code)
	assert.True(readErr.Retryable)

	_, err = backend.ListWorklists(t.Context(), false)
	var listErr *mcpserver.Error
	require.ErrorAs(err, &listErr)
	assert.Equal(string(httpapi.CodeHubUnavailable), listErr.Code)
	assert.True(listErr.Retryable)

	_, err = backend.PutWorklist(t.Context(), mcpserver.WorklistPut{
		Title: "New", Items: []mcpserver.WorklistPutItem{{Item: worklistTestItem(repo, "pr", 42)}},
	})
	var writeErr *mcpserver.Error
	require.ErrorAs(err, &writeErr)
	assert.Equal(string(httpapi.CodeMutationOutcomeUnknown), writeErr.Code)
	assert.True(writeErr.Ambiguous)

	_, err = backend.DismissWorklist(t.Context(), local.ID)
	var dismissErr *mcpserver.Error
	require.ErrorAs(err, &dismissErr)
	assert.Equal(string(httpapi.CodeMutationOutcomeUnknown), dismissErr.Code)
	assert.True(dismissErr.Ambiguous)

	_, err = backend.ClearWorklistMarks(t.Context(), local.ID)
	var clearErr *mcpserver.Error
	require.ErrorAs(err, &clearErr)
	assert.Equal(string(httpapi.CodeMutationOutcomeUnknown), clearErr.Code)
	assert.True(clearErr.Ambiguous)
	assert.Equal([]federationauth.Scope{
		federationauth.ScopeProviderRead, federationauth.ScopeProviderRead,
		federationauth.ScopeProviderWrite, federationauth.ScopeProviderWrite, federationauth.ScopeProviderWrite,
	}, scopes)
	stored, err := database.GetWorklist(t.Context(), local.ID, nil)
	require.NoError(err)
	assert.Nil(stored.DismissedAt, "a spoke never dismisses its local copy")
	assert.Equal(new(localMark), stored.Items[0].HandledAt, "a spoke never clears its local copy's marks")
}

func TestSpokeWorklistRowsCarryOnlySpokeWorkspaces(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	hubDB := dbtest.Open(t)
	serverfake.SeedPR(t, hubDB, "acme", "widget", 42)
	serverfake.SeedIssue(t, hubDB, "acme", "widget", 5, "open")
	serverfake.SeedWorkspace(t, hubDB, "ws-hub", "acme", "widget", db.WorkspaceItemTypePullRequest, 42)
	repo, err := hubDB.GetRepoByIdentity(
		t.Context(), serverfake.VerifiedGitHubRepoIdentity("github.com", "acme", "widget"),
	)
	require.NoError(err)
	require.NotNil(repo)
	hubServer := New(hubDB, nil, nil, "/", nil, ServerOptions{DisableWorkspaceBackgroundMonitors: true})
	t.Cleanup(func() { serverfake.GracefulShutdown(t, hubServer) })
	// The hub authenticates spokes in production; this hub has no
	// enrollments, so mark every request through it as a spoke's.
	hub := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hubServer.ServeHTTP(w, r.WithContext(federationauth.WithPrincipal(r.Context(), federationauth.Principal{
			NodeID: "99999999999999999999999999999991",
			Scopes: map[federationauth.Scope]struct{}{
				federationauth.ScopeProviderRead: {}, federationauth.ScopeProviderWrite: {},
			},
		})))
	}))
	t.Cleanup(hub.Close)
	spoke, spokeDB := newFederatedProviderNodeForTest(
		t, "99999999999999999999999999999991", hub.URL, hub.Client(),
	)
	serverfake.SeedIssue(t, spokeDB, "acme", "widget", 5, "open")
	serverfake.SeedWorkspace(t, spokeDB, "ws-spoke", "acme", "widget", db.WorkspaceItemTypeIssue, 5)
	put, err := spoke.MCPBackend().PutWorklist(t.Context(), mcpserver.WorklistPut{
		Title: "Triage", WorkspaceID: "ws-chat",
		Items: []mcpserver.WorklistPutItem{
			{Item: worklistTestItem(repo, "pr", 42)}, {Item: worklistTestItem(repo, "issue", 5)},
		},
	})
	require.NoError(err)
	read := func(server http.Handler) worklistapi.WorklistResponse {
		t.Helper()
		response := testutil.DoJSON(t, server, http.MethodGet,
			"/api/v1/worklists/"+strconv.FormatInt(put.WorklistID, 10), nil)
		require.Equal(http.StatusOK, response.Code, response.Body.String())
		var worklist worklistapi.WorklistResponse
		require.NoError(json.Unmarshal(response.Body.Bytes(), &worklist))
		require.Len(worklist.Items, 2)
		require.NotNil(worklist.Items[0].Pull)
		require.NotNil(worklist.Items[1].Issue)
		return worklist
	}

	federated, err := spoke.providerSource.GetWorklist(t.Context(), put.WorklistID)
	require.NoError(err)
	require.Len(federated.Items, 2)
	require.NotNil(federated.Items[0].Pull)
	assert.Nil(federated.Items[0].Pull.Workspace, "the hub strips its own workspace")
	assert.Empty(federated.WorkspaceID)

	onSpoke := read(spoke)
	assert.Nil(onSpoke.Items[0].Pull.Workspace)
	require.NotNil(onSpoke.Items[1].Issue.Workspace, "the spoke adds its own workspace")
	assert.Equal("ws-spoke", onSpoke.Items[1].Issue.Workspace.ID)
	assert.Empty(onSpoke.WorkspaceID)

	onHub := read(hubServer)
	require.NotNil(onHub.Items[0].Pull.Workspace)
	assert.Equal("ws-hub", onHub.Items[0].Pull.Workspace.ID)
	assert.Nil(onHub.Items[1].Issue.Workspace)
	assert.Empty(onHub.WorkspaceID, "the spoke's workspace names a workspace the hub cannot open")
	stored, err := hubDB.GetWorklistSummary(t.Context(), put.WorklistID, nil)
	require.NoError(err)
	assert.Empty(stored.WorkspaceID, "the hub records no workspace from a forwarded put")

	marked := testutil.DoJSON(t, spoke, http.MethodPost,
		"/api/v1/worklists/"+strconv.FormatInt(put.WorklistID, 10)+"/handled",
		worklistapi.SetWorklistItemsHandledBody{Handled: true, Items: []worklistapi.WorklistItemRef{{
			Provider: "github", PlatformHost: "github.com", RepoKey: repo.Key, ItemType: "issue", Number: 5,
		}}})
	require.Equal(http.StatusOK, marked.Code, marked.Body.String())
	var handled worklistapi.WorklistHandledResponse
	require.NoError(json.Unmarshal(marked.Body.Bytes(), &handled))
	assert.Equal(1, handled.Worklist.HandledCount)
	assert.Empty(handled.Worklist.WorkspaceID, "the hub strips its workspace from a spoke's mark")
	require.Len(handled.Items, 1)
	assert.NotNil(handled.Items[0].HandledAt)
	hubCopy, err := hubDB.GetWorklist(t.Context(), put.WorklistID, nil)
	require.NoError(err)
	assert.Nil(hubCopy.Items[0].HandledAt)
	assert.NotNil(hubCopy.Items[1].HandledAt, "a spoke's mark lands on the hub")
	local, err := spokeDB.ListWorklists(t.Context(), true, nil)
	require.NoError(err)
	assert.Empty(local)
}

func TestWorklistItemsInUnconfiguredRepositoriesAreGone(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	assert := assert.New(t)
	require := require.New(t)
	srv, database, _, _ := setupTestServerWithConfig(t)
	ctx := t.Context()
	serverfake.SeedPR(t, database, "acme", "widget", 42)
	serverfake.SeedPR(t, database, "acme", "gadget", 3)
	configured, err := database.GetRepoByIdentity(ctx, serverfake.VerifiedGitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	require.NotNil(configured)
	unconfigured, err := database.GetRepoByIdentity(ctx, serverfake.VerifiedGitHubRepoIdentity("github.com", "acme", "gadget"))
	require.NoError(err)
	require.NotNil(unconfigured)
	_, err = srv.MCPBackend().PutWorklist(ctx, mcpserver.WorklistPut{
		Title: "Rejected", Items: []mcpserver.WorklistPutItem{
			{Item: worklistTestItem(configured, "pr", 42)}, {Item: worklistTestItem(unconfigured, "pr", 3)},
		},
	})
	var putErr *mcpserver.Error
	require.ErrorAs(err, &putErr)
	assert.Equal(string(httpapi.CodeRepoNotFound), putErr.Code)
	assert.EqualValues(1, putErr.Details["item_index"])

	// The repository leaves config after the agent wrote the list.
	put, err := database.PutWorklist(ctx, db.PutWorklistParams{
		Title: "Mixed", Now: time.Now(), Items: []db.WorklistItemParams{
			{RepoID: unconfigured.ID, ItemType: "pr", ItemNumber: 3},
			{RepoID: configured.ID, ItemType: "pr", ItemNumber: 42},
		},
	})
	require.NoError(err)

	response := testutil.DoJSON(t, srv, http.MethodGet, "/api/v1/worklists/"+strconv.FormatInt(put.ID, 10), nil)
	require.Equal(http.StatusOK, response.Code, response.Body.String())
	var worklist worklistapi.WorklistResponse
	require.NoError(json.Unmarshal(response.Body.Bytes(), &worklist))
	require.Len(worklist.Items, 2)
	assert.Equal("gone", worklist.Items[0].Freshness)
	assert.Nil(worklist.Items[0].Pull)
	assert.Equal("fresh", worklist.Items[1].Freshness)
	require.NotNil(worklist.Items[1].Pull)

	listed := testutil.DoJSON(t, srv, http.MethodGet, "/api/v1/worklists", nil)
	require.Equal(http.StatusOK, listed.Code, listed.Body.String())
	var summaries worklistapi.ListWorklistsResponse
	require.NoError(json.Unmarshal(listed.Body.Bytes(), &summaries))
	require.Len(summaries.Worklists, 1)
	assert.Equal(1, summaries.Worklists[0].GoneCount)

	read, err := srv.MCPBackend().GetWorklist(ctx, put.ID)
	require.NoError(err)
	require.Len(read.Items, 2)
	assert.Equal("gone", read.Items[0].Freshness)
	assert.Nil(read.Items[0].Live)
}

func TestExecutionWorkerWorklistOperationsReportUnavailable(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	cfg := &config.Config{}
	cfg.DataDir = t.TempDir()
	cfg.ExecutionWorker = config.ExecutionWorker{
		Enabled: true, UID: 1001, GitHubUserID: 1234, BrokerSocket: "/run/example/broker.sock",
	}
	worker := New(dbtest.Open(t), nil, nil, "/", cfg, ServerOptions{
		ExecutionWorker: true, FederationSpokeID: "0123456789abcdef0123456789abcdef",
		DisableWorkspaceBackgroundMonitors: true,
	})
	t.Cleanup(func() { serverfake.GracefulShutdown(t, worker) })
	backend := worker.MCPBackend()
	item := mcpserver.ItemIdentity{
		Type: "pr", Provider: "github", PlatformHost: "github.com",
		RepoKey: platform.RepositoryIDKey(1001), Owner: "acme", Name: "widget", Number: 42,
	}

	operations := map[string]func() error{
		"put": func() error {
			_, err := backend.PutWorklist(t.Context(), mcpserver.WorklistPut{
				Title: "Merge today", Items: []mcpserver.WorklistPutItem{{Item: item}},
			})
			return err
		},
		"get": func() error {
			_, err := backend.GetWorklist(t.Context(), 1)
			return err
		},
		"list": func() error {
			_, err := backend.ListWorklists(t.Context(), false)
			return err
		},
		"dismiss": func() error {
			_, err := backend.DismissWorklist(t.Context(), 1)
			return err
		},
		"clear marks": func() error {
			_, err := backend.ClearWorklistMarks(t.Context(), 1)
			return err
		},
	}
	for name, operation := range operations {
		t.Run(name, func(t *testing.T) {
			var err error
			require.NotPanics(t, func() { err = operation() })
			var backendErr *mcpserver.Error
			require.ErrorAs(t, err, &backendErr)
			assert.Equal(t, string(httpapi.CodeServiceUnavailable), backendErr.Code)
			assert.Equal(t, "unavailable", backendErr.Kind)
		})
	}
}
