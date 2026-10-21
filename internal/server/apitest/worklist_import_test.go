package apitest

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/forge/internal/apiclient/generated"
	serverfake "go.kenn.io/forge/internal/testutil/serverfake"
	"go.kenn.io/forge/worklist"
)

func TestAPIWorklistImportPreviewsThenCreatesTrackedItems(t *testing.T) {
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
	body := &generated.ImportWorklistBody{Fragment: fragment}
	listCount := func() int {
		listed, err := client.HTTP.ListWorklistsWithResponse(ctx, &generated.ListWorklistsRequestOptions{})
		require.NoError(err)
		require.NotNil(listed.JSON200, string(listed.Body))
		return len(listed.JSON200.Worklists)
	}

	preview, err := client.HTTP.PreviewWorklistImportWithResponse(ctx, &generated.PreviewWorklistImportRequestOptions{Body: body})

	require.NoError(err)
	require.NotNil(preview.JSON200, string(preview.Body))
	assert.Equal("Merge today", preview.JSON200.Title)
	assert.Equal("triage-bot", preview.JSON200.CreatedBy)
	assert.EqualValues(2, preview.JSON200.ImportableCount)
	statuses := []generated.WorklistImportItemResponseStatus{}
	for _, item := range preview.JSON200.Items {
		statuses = append(statuses, item.Status)
	}
	assert.Equal([]generated.WorklistImportItemResponseStatus{
		generated.WorklistImportItemResponseStatusImportable,
		generated.WorklistImportItemResponseStatusRepoNotTracked,
		generated.WorklistImportItemResponseStatusImportable,
		generated.WorklistImportItemResponseStatusItemNotFound,
		generated.WorklistImportItemResponseStatusDuplicate,
	}, statuses)
	require.NotNil(preview.JSON200.Items[0].Pull, "a tracked item carries its list row")
	assert.EqualValues(42, preview.JSON200.Items[0].Pull.Number)
	assert.Nil(preview.JSON200.Items[0].Issue, "a pull request item carries no issue row")
	assert.Nil(preview.JSON200.Items[1].Pull, "an untracked item carries no row")
	assert.Equal("repository this Forge does not track", preview.JSON200.Items[1].Note)
	assert.Zero(listCount(), "a preview writes nothing")

	imported, err := client.HTTP.ImportWorklistWithResponse(ctx, &generated.ImportWorklistRequestOptions{Body: body})

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
