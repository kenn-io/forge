package mcpserver

import (
	"context"
	"encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPutWorklistForwardsTypedInputWithACPWorkspaceOverHTTP(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	var got []WorklistPut
	s := newMCPTestServer(t, &fakeBackend{putWorklistFn: func(_ context.Context, put WorklistPut) (WorklistPutResult, error) {
		got = append(got, put)
		return WorklistPutResult{WorklistID: 12, ItemCount: len(put.Items), Created: false}, nil
	}})
	handler := s.HTTPHandler()
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Header.Set("X-Kenn-Forge-Workspace-ID", "ws-chat")
		handler.ServeHTTP(w, r)
	}))
	defer endpoint.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "test"}, nil)
	session, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{Endpoint: endpoint.URL + "/mcp", DisableStandaloneSSE: true}, nil)
	require.NoError(err)
	defer session.Close()

	response, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "kenn_forge_put_worklist", Arguments: map[string]any{
		"worklist_id": 12, "title": "  Merge today  ", "recipe": "open PRs with green CI", "created_by": "triage-agent",
		"items": []map[string]any{
			{
				"item":  itemRefInput{Type: "pr", Provider: "github", PlatformHost: "github.com", PlatformRepoID: 1001, Owner: "acme", Name: "widget", Number: 42},
				"score": 9, "section": "Ready", "note": "small fix", "caveat": "check docs",
			},
			{"item": itemRefInput{Type: "issue", Provider: "github", PlatformHost: "github.com", PlatformRepoID: 1001, Owner: "acme", Name: "widget", Number: 7}},
		},
	}})

	require.NoError(err)
	require.False(response.IsError)
	require.Len(got, 1)
	assert.Equal(WorklistPut{
		WorklistID: 12, Title: "Merge today", Recipe: "open PRs with green CI",
		CreatedBy: "triage-agent", WorkspaceID: "ws-chat",
		Items: []WorklistPutItem{
			{Item: testItemIdentity("pr", 42), Score: new(9), Section: "Ready", Note: "small fix", Caveat: "check docs"},
			{Item: testItemIdentity("issue", 7)},
		},
	}, got[0])
	var result WorklistPutResult
	require.NoError(json.Unmarshal([]byte(response.Content[0].(*mcp.TextContent).Text), &result))
	assert.Equal(WorklistPutResult{WorklistID: 12, ItemCount: 2}, result)
}

func TestPutWorklistRejectsInvalidInputWithFieldDetails(t *testing.T) {
	calls := 0
	s := newMCPTestServer(t, &fakeBackend{putWorklistFn: func(context.Context, WorklistPut) (WorklistPutResult, error) {
		calls++
		return WorklistPutResult{}, nil
	}})
	pull := func(number int) putWorklistItemInput {
		return putWorklistItemInput{Item: itemRefInput{
			Type: "pr", Provider: "github", PlatformHost: "github.com", PlatformRepoID: 1001,
			Owner: "acme", Name: "widget", Number: number,
		}}
	}
	valid := func(mutate func(*putWorklistInput)) putWorklistInput {
		in := putWorklistInput{Title: "List", Recipe: "criteria", Items: []putWorklistItemInput{pull(1)}}
		mutate(&in)
		return in
	}
	tooMany := make([]putWorklistItemInput, 0, 201)
	for number := 1; number <= 201; number++ {
		tooMany = append(tooMany, pull(number))
	}
	tests := []struct {
		name      string
		in        putWorklistInput
		wantField string
	}{
		{"blank title", valid(func(in *putWorklistInput) { in.Title = "   " }), "title"},
		{"long title", valid(func(in *putWorklistInput) { in.Title = strings.Repeat("t", 201) }), "title"},
		{"long recipe", valid(func(in *putWorklistInput) { in.Recipe = strings.Repeat("r", 10001) }), "recipe"},
		{"no items", valid(func(in *putWorklistInput) { in.Items = nil }), "items"},
		{"too many items", valid(func(in *putWorklistInput) { in.Items = tooMany }), "items"},
		{"score below range", valid(func(in *putWorklistInput) { in.Items[0].Score = new(0) }), "items[0].score"},
		{"score above range", valid(func(in *putWorklistInput) { in.Items[0].Score = new(11) }), "items[0].score"},
		{"long section", valid(func(in *putWorklistInput) { in.Items[0].Section = strings.Repeat("s", 81) }), "items[0].section"},
		{"long note", valid(func(in *putWorklistInput) { in.Items[0].Note = strings.Repeat("n", 4001) }), "items[0].note"},
		{"long caveat", valid(func(in *putWorklistInput) { in.Items[0].Caveat = strings.Repeat("c", 4001) }), "items[0].caveat"},
		{"kata item", valid(func(in *putWorklistInput) { in.Items[0].Item.Type = "kata" }), "items[0].item"},
		{"missing repository key", valid(func(in *putWorklistInput) { in.Items[0].Item.PlatformRepoID = 0 }), "items[0].item"},
		{
			"duplicate item with default host omitted",
			valid(func(in *putWorklistInput) {
				duplicate := pull(1)
				duplicate.Item.PlatformHost = ""
				in.Items = append(in.Items, pull(2), duplicate)
			}),
			"items[2].item",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := s.putWorklist(t.Context(), tt.in)

			var backendErr *Error
			require.ErrorAs(t, err, &backendErr)
			assert.Equal(t, "invalid_request", backendErr.Kind)
			assert.Equal(t, ErrorCodeValidation, backendErr.Code)
			assert.Equal(t, tt.wantField, backendErr.Details["field"])
		})
	}
	assert.Zero(t, calls, "invalid puts never reach the provider owner")

	_, err := s.putWorklist(t.Context(), valid(func(in *putWorklistInput) {
		in.Title = strings.Repeat("t", 200)
		in.Items[0].Score = new(10)
		in.Items[0].Section = strings.Repeat("s", 80)
	}))
	require.NoError(t, err)
	assert.Equal(t, 1, calls)
}

func TestGetWorklistReturnsKeyedItemIdentityThroughClientSession(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	var gotID int64
	s := newMCPTestServer(t, &fakeBackend{getWorklistFn: func(_ context.Context, id int64) (Worklist, error) {
		gotID = id
		return Worklist{
			ID: id, Title: "Merge today", Recipe: "green CI", CreatedAt: "2026-03-04T05:06:07Z", UpdatedAt: "2026-03-04T05:06:07Z",
			Items: []WorklistItem{{
				Item: testItemIdentity("pr", 42), Title: "Fix widget", Freshness: "changed",
				Snapshot: WorklistItemState{State: "open", HeadSHA: "aaa"},
				Live:     &WorklistItemState{State: "open", HeadSHA: "bbb"},
				Changes:  WorklistChanges{NewCommits: true},
			}},
		}, nil
	}})

	result, err := connectMCPTestSession(t, s).CallTool(t.Context(), &mcp.CallToolParams{
		Name: "kenn_forge_get_worklist", Arguments: map[string]any{"worklist_id": 5},
	})

	require.NoError(err)
	require.False(result.IsError)
	assert.Equal(int64(5), gotID)
	raw, err := json.Marshal(result.StructuredContent)
	require.NoError(err)
	var decoded struct {
		Items []struct {
			Item struct {
				Type           string `json:"type"`
				PlatformRepoID int64  `json:"platform_repo_id"`
			} `json:"item"`
			Freshness string         `json:"freshness"`
			Changes   map[string]any `json:"changes"`
		} `json:"items"`
	}
	require.NoError(json.Unmarshal(raw, &decoded))
	require.Len(decoded.Items, 1)
	assert.Equal(int64(1001), decoded.Items[0].Item.PlatformRepoID)
	assert.Equal("pr", decoded.Items[0].Item.Type)
	assert.Equal("changed", decoded.Items[0].Freshness)
	assert.Equal(map[string]any{"new_commits": true, "new_comments": float64(0)}, decoded.Items[0].Changes)
}

func TestListAndDismissWorklistsForwardToBackend(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	var includeDismissed []bool
	var dismissed []int64
	s := newMCPTestServer(t, &fakeBackend{
		listWorklistsFn: func(_ context.Context, include bool) ([]WorklistSummary, error) {
			includeDismissed = append(includeDismissed, include)
			return nil, nil
		},
		dismissWorklistFn: func(_ context.Context, id int64) (WorklistDismissal, error) {
			dismissed = append(dismissed, id)
			return WorklistDismissal{WorklistID: id, DismissedAt: "2026-03-04T05:06:07Z"}, nil
		},
	})
	session := connectMCPTestSession(t, s)

	listed, err := session.CallTool(t.Context(), &mcp.CallToolParams{
		Name: "kenn_forge_list_worklists", Arguments: map[string]any{"include_dismissed": true},
	})
	require.NoError(err)
	require.False(listed.IsError)
	raw, err := json.Marshal(listed.StructuredContent)
	require.NoError(err)
	assert.JSONEq(`{"worklists":[]}`, string(raw))

	out, err := s.dismissWorklist(t.Context(), worklistIDInput{WorklistID: 3})
	require.NoError(err)
	assert.Equal("2026-03-04T05:06:07Z", out.DismissedAt)
	_, err = s.dismissWorklist(t.Context(), worklistIDInput{})
	var backendErr *Error
	require.ErrorAs(err, &backendErr)
	assert.Equal("worklist_id", backendErr.Details["field"])

	assert.Equal([]bool{true}, includeDismissed)
	assert.Equal([]int64{3}, dismissed)
}
