package mcpserver

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetItemContextPullRequestLimitsEventsAndEscapesPath(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	mux := http.NewServeMux()
	handleEmptyWorkflowState(mux)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal("/api/v1/host/git.example.com/pulls/gitlab/Group%2FSub/Project/42", r.URL.EscapedPath())
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"merge_request":{
				"Number":42,"Title":"Retry budget","State":"open","Author":"alice",
				"URL":"https://git.example.com/Group/Sub/Project/-/merge_requests/42",
				"IsDraft":false,"Body":"full PR body","KanbanStatus":"reviewing",
				"LastActivityAt":"2026-07-01T16:00:00Z"
			},
			"events":[
				{"EventType":"comment","Author":"old","Summary":"old","Body":"old body","CreatedAt":"2026-07-01T12:00:00Z"},
				{"EventType":"commit","Author":"newest","Summary":"newest","Body":"` + strings.Repeat("x", 620) + `","CreatedAt":"2026-07-01T16:00:00Z"},
				{"EventType":"review","Author":"middle","Summary":"middle","Body":"middle body","CreatedAt":"2026-07-01T14:00:00Z"},
				{"EventType":"comment","Author":"older","Summary":"older","Body":"older body","CreatedAt":"2026-07-01T13:00:00Z"},
				{"EventType":"comment","Author":"second","Summary":"second","Body":"second body","CreatedAt":"2026-07-01T15:00:00Z"}
			],
			"repo":{"provider":"gitlab","platform_host":"git.example.com","repo_path":"Group/Sub/Project","owner":"Group/Sub","name":"Project"},
			"platform_host":"git.example.com","repo_owner":"Group/Sub","repo_name":"Project",
			"detail_loaded":true,"detail_fetched_at":"2026-07-01T16:05:00Z",
			"workspace":{"id":"ws-pr","status":"ready"},
			"stack":{"stack_id":9,"stack_name":"feature","position":2,"size":3,"health":"blocked","members":[]},
			"checks":[{"name":"unit","status":"completed","conclusion":"success","url":"https://ci.example.test","app":"ci"}]
		}`))
	})
	s := newMCPTestServer(t, mux)

	out, err := s.getItemContext(t.Context(), getItemContextInput{
		Item: itemRefInput{
			Type: "pr", Provider: "gitlab", PlatformHost: "git.example.com",
			Owner: "Group/Sub", Name: "Project", Number: 42,
		},
		EventLimit: 2,
	})
	require.NoError(err)
	assert.Equal("pr", out.Item.Type)
	assert.Equal(42, out.Item.Number)
	assert.Equal("full PR body", out.Body)
	assert.Equal("reviewing", out.Workflow.Status)
	assert.Equal("2026-07-01T16:00:00Z", out.LastActivityAt)
	require.Len(out.Events, 2)
	assert.Equal("newest", out.Events[0].Author)
	assert.Len(out.Events[0].BodyPreview, 500)
	assert.Equal("second", out.Events[1].Author)
	require.NotNil(out.Workspace)
	assert.Equal("ws-pr", out.Workspace.ID)
	assert.True(out.Stack.Present)
	assert.Equal(2, out.Stack.Position)
	require.Len(out.Checks, 1)
	assert.Equal("unit", out.Checks[0].Name)
	assert.True(out.Cache.DetailLoaded)
	assert.Equal("2026-07-01T16:05:00Z", out.Cache.DetailFetchedAt)
}

func TestGetItemContextPullRequestUsesWorkflowMetadata(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/pulls/github/acme/widget/42", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"merge_request":{
				"Number":42,"Title":"Retry budget","State":"open","Author":"alice",
				"URL":"https://example.test/pr/42","Body":"full PR body",
				"KanbanStatus":"reviewing","LastActivityAt":"2026-07-01T16:00:00Z"
			},
			"repo":{"provider":"github","platform_host":"github.com","repo_path":"acme/widget","owner":"acme","name":"widget"},
			"platform_host":"github.com","repo_owner":"acme","repo_name":"widget",
			"detail_loaded":true
		}`))
	})
	mux.HandleFunc("/api/v1/workflow-state", func(w http.ResponseWriter, r *http.Request) {
		if writeWorkflowProbeResponse(w, r) {
			return
		}
		query := r.URL.Query()
		assert.Equal("github|github.com/acme/widget", query.Get("repo"))
		assert.Equal([]string{"pr"}, query["item_type"])
		assert.Equal("true", query.Get("include_closed"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[{
			"provider":"github","platform_host":"github.com","owner":"acme","name":"widget","repo_path":"acme/widget",
			"item_type":"pr","number":42,"workflow":{
				"status":"reviewing","updated_at":"2026-07-01T16:10:00Z",
				"updated_source":"mcp","updated_actor":"agent","updated_reason":"claim"
			}
		}]}`))
	})
	s := newMCPTestServer(t, mux)

	out, err := s.getItemContext(t.Context(), getItemContextInput{
		Item: itemRefInput{Type: "pr", Provider: "github", Owner: "acme", Name: "widget", Number: 42},
	})

	require.NoError(err)
	assert.Equal("reviewing", out.Workflow.Status)
	assert.Equal("2026-07-01T16:10:00Z", out.Workflow.UpdatedAt)
	assert.Equal("mcp", out.Workflow.UpdatedSource)
	assert.Equal("agent", out.Workflow.UpdatedActor)
	assert.Equal("claim", out.Workflow.UpdatedReason)
}

func TestGetItemContextCanOmitEvents(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/issues/github/acme/widget/7", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"issue":{
				"Number":7,"Title":"Retry docs","State":"open","Author":"bob",
				"URL":"https://example.test/issues/7","Body":"full issue body",
				"WorkflowStatus":"waiting","LastActivityAt":"2026-07-01T15:00:00Z"
			},
			"events":[{"EventType":"comment","Author":"carol","Summary":"note","Body":"body","CreatedAt":"2026-07-01T15:00:00Z"}],
			"repo":{"provider":"github","platform_host":"github.com","repo_path":"acme/widget","owner":"acme","name":"widget"},
			"platform_host":"github.com","repo_owner":"acme","repo_name":"widget",
			"detail_loaded":false,
			"workflow":{"status":"waiting","updated_at":"2026-07-01T15:02:00Z","updated_source":"mcp","updated_actor":"agent","updated_reason":"checking docs"}
		}`))
	})
	s := newMCPTestServer(t, mux)
	includeEvents := false

	out, err := s.getItemContext(t.Context(), getItemContextInput{
		Item:          itemRefInput{Type: "issue", Provider: "github", Owner: "acme", Name: "widget", Number: 7},
		IncludeEvents: &includeEvents,
	})
	require.NoError(err)
	assert.Equal("issue", out.Item.Type)
	assert.Equal("full issue body", out.Body)
	assert.Equal("waiting", out.Workflow.Status)
	assert.Empty(out.Events)
	raw, err := json.Marshal(out)
	require.NoError(err)
	assert.NotContains(string(raw), `"events"`)
}

func TestListItemsByWorkflowStateForwardsFiltersAndMapsItems(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/workflow-state", func(w http.ResponseWriter, r *http.Request) {
		if writeWorkflowProbeResponse(w, r) {
			return
		}
		query := r.URL.Query()
		assert.ElementsMatch([]string{"reviewing", "waiting"}, query["state"])
		assert.ElementsMatch([]string{"pr", "issue"}, query["item_type"])
		assert.Equal("gitlab|git.example.com/Group/Sub/Project", query.Get("repo"))
		assert.Equal("true", query.Get("include_closed"))
		assert.Equal("10", query.Get("limit"))
		assert.Equal("c123", query.Get("cursor"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"items":[{
				"provider":"gitlab","platform_host":"git.example.com",
				"owner":"Group/Sub","name":"Project","repo_path":"Group/Sub/Project",
				"item_type":"pr","number":42,"title":"Retry budget","state":"open",
				"url":"https://git.example.com/Group/Sub/Project/-/merge_requests/42",
				"author":"alice","is_draft":false,"last_activity_at":"2026-07-01T16:00:00Z",
				"workflow":{"status":"reviewing","updated_at":"2026-07-01T16:01:00Z","updated_source":"mcp","updated_actor":"agent","updated_reason":"claim"}
			}],
			"next_cursor":"next"
		}`))
	})
	s := newMCPTestServer(t, mux)

	out, err := s.listItemsByWorkflowState(t.Context(), listByWorkflowInput{
		States:        []string{"reviewing", "waiting"},
		ItemTypes:     []string{"pr", "issue"},
		Repo:          repoFilterInput{Provider: "gitlab", PlatformHost: "git.example.com", RepoPath: "Group/Sub/Project"},
		IncludeClosed: true,
		Limit:         10,
		Cursor:        "c123",
	})
	require.NoError(err)
	require.Len(out.Items, 1)
	assert.Equal("next", out.NextCursor)
	assert.Equal("pr", out.Items[0].Item.Type)
	assert.Equal("Group/Sub/Project", out.Items[0].Item.RepoPath)
	assert.Equal("reviewing", out.Items[0].Workflow.Status)
	assert.Equal("2026-07-01T16:00:00Z", out.Items[0].LastActivityAt)
}

func TestListItemsByWorkflowStateReportsVersionMismatch(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/workflow-state", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"status":404,"code":"notFound","detail":"route missing"}`))
	})
	s := newMCPTestServer(t, mux)

	_, err := s.listItemsByWorkflowState(t.Context(), listByWorkflowInput{
		States: []string{"reviewing"},
	})

	var derr *daemonError
	require.ErrorAs(err, &derr)
	assert.Equal("version_mismatch", derr.Kind)
	assert.Contains(derr.Message, "/workflow-state")
}

func TestTruncateBytesPreservesUTF8(t *testing.T) {
	assert := assert.New(t)

	got := truncateBytes(strings.Repeat("a", 499)+"é", 500)

	assert.True(utf8.ValidString(got))
	assert.LessOrEqual(len(got), 500)
	assert.Equal(strings.Repeat("a", 499), got)
}

func handleEmptyWorkflowState(mux *http.ServeMux) {
	mux.HandleFunc("/api/v1/workflow-state", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[]}`))
	})
}

func writeWorkflowProbeResponse(w http.ResponseWriter, r *http.Request) bool {
	query := r.URL.Query()
	if query.Get("limit") != "1" || len(query) != 1 {
		return false
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"items":[]}`))
	return true
}
