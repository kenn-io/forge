package mcpserver

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFindReviewCandidatesGroupsActivityAndEnrichesItems(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/activity", func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		assert.Equal("2026-07-01T00:00:00Z", query.Get("since"))
		assert.Equal("comment", query["types"][0])
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[{
			"id":"pr-comment","cursor":"c1","activity_type":"comment",
			"repo":{"provider":"github","platform_host":"github.com","repo_path":"acme/widget","owner":"acme","name":"widget"},
			"platform_host":"github.com","repo_owner":"acme","repo_name":"widget",
			"item_type":"pr","item_number":42,"item_title":"Retry budget",
			"item_url":"https://example.test/pr/42","item_state":"open",
			"author":"bob","item_author":"alice",
			"created_at":"2026-07-01T14:00:00Z","body_preview":"please retry"
		},{
			"id":"pr-commit","cursor":"c2","activity_type":"commit",
			"repo":{"provider":"github","platform_host":"github.com","repo_path":"acme/widget","owner":"acme","name":"widget"},
			"platform_host":"github.com","repo_owner":"acme","repo_name":"widget",
			"item_type":"pr","item_number":42,"item_title":"Retry budget",
			"item_url":"https://example.test/pr/42","item_state":"open",
			"author":"alice","item_author":"alice",
			"created_at":"2026-07-01T13:00:00Z","body_preview":"pushed retry fix"
		},{
			"id":"issue-comment","cursor":"c3","activity_type":"comment",
			"repo":{"provider":"github","platform_host":"github.com","repo_path":"acme/widget","owner":"acme","name":"widget"},
			"platform_host":"github.com","repo_owner":"acme","repo_name":"widget",
			"item_type":"issue","item_number":7,"item_title":"Retry docs",
			"item_url":"https://example.test/issues/7","item_state":"open",
			"author":"carol","item_author":"dave",
			"created_at":"2026-07-01T15:00:00Z","body_preview":"docs need retry"
		},{
			"id":"repo-row","cursor":"c4","activity_type":"default_branch_commit",
			"repo":{"provider":"github","platform_host":"github.com","repo_path":"acme/widget","owner":"acme","name":"widget"},
			"platform_host":"github.com","repo_owner":"acme","repo_name":"widget",
			"item_type":"","item_number":0,
			"author":"eve","created_at":"2026-07-01T16:00:00Z"
		}],"capped":false}`))
	})
	mux.HandleFunc("/api/v1/pulls", func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		assert.Equal("github|github.com/acme/widget", query.Get("repo"))
		assert.Equal("all", query.Get("state"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{
			"Number":42,"Title":"Retry budget","State":"open","Author":"alice",
			"URL":"https://example.test/pr/42","IsDraft":false,
			"KanbanStatus":"","LastActivityAt":"2026-07-01T14:00:00Z",
			"repo":{"provider":"github","platform_host":"github.com","repo_path":"acme/widget","owner":"acme","name":"widget"},
			"platform_host":"github.com","repo_owner":"acme","repo_name":"widget",
			"workspace":{"id":"ws-pr","status":"ready"},
			"detail_loaded":true,"detail_fetched_at":"2026-07-01T14:05:00Z"
		}]`))
	})
	mux.HandleFunc("/api/v1/issues", func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		assert.Equal("github|github.com/acme/widget", query.Get("repo"))
		assert.Equal("all", query.Get("state"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{
			"Number":7,"Title":"Retry docs","State":"open","Author":"dave",
			"URL":"https://example.test/issues/7","WorkflowStatus":"waiting",
			"LastActivityAt":"2026-07-01T15:00:00Z",
			"repo":{"provider":"github","platform_host":"github.com","repo_path":"acme/widget","owner":"acme","name":"widget"},
			"platform_host":"github.com","repo_owner":"acme","repo_name":"widget",
			"detail_loaded":false
		}]`))
	})
	handleEmptyWorkflowState(mux)
	mux.HandleFunc("/api/v1/host/github.com/pulls/github/acme/widget/42/stack", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"stack_id":1,"stack_name":"stack","position":2,"size":4,"health":"blocked","members":[]}`))
	})
	s := newMCPTestServer(t, mux)

	out, err := s.findReviewCandidates(t.Context(), findCandidatesInput{
		Since:         "2026-07-01T00:00:00Z",
		ActivityTypes: []string{"comment"},
	})
	require.NoError(err)
	require.Len(out.Candidates, 2)
	assert.False(out.Capped)

	issue := out.Candidates[0]
	assert.Equal("issue", issue.Item.Type)
	assert.Equal(7, issue.Item.Number)
	assert.Equal("waiting", issue.Workflow.Status)
	assert.Equal("2026-07-01T15:00:00Z", issue.Activity.LatestAt)
	assert.Equal(1, issue.Activity.EventCount)
	assert.Equal([]string{"comment"}, issue.Activity.Types)
	assert.Equal([]string{"carol"}, issue.Activity.Actors)
	assert.Equal([]string{"carol commented"}, issue.Activity.Reasons)
	assert.False(issue.Workspace.Exists)
	assert.False(issue.Stack.Present)
	assert.False(issue.Cache.DetailLoaded)

	pr := out.Candidates[1]
	assert.Equal("pr", pr.Item.Type)
	assert.Equal(42, pr.Item.Number)
	assert.Equal("new", pr.Workflow.Status)
	assert.Equal("2026-07-01T14:00:00Z", pr.Activity.LatestAt)
	assert.Equal(2, pr.Activity.EventCount)
	assert.Equal([]string{"comment", "commit"}, pr.Activity.Types)
	assert.Equal([]string{"bob", "alice"}, pr.Activity.Actors)
	assert.Equal([]string{"bob commented", "alice pushed commits"}, pr.Activity.Reasons)
	assert.True(pr.Workspace.Exists)
	assert.Equal("ws-pr", pr.Workspace.ID)
	assert.True(pr.Stack.Present)
	assert.Equal(2, pr.Stack.Position)
	assert.Equal(4, pr.Stack.Size)
	assert.Equal("blocked", pr.Stack.Health)
	assert.True(pr.Cache.DetailLoaded)
	assert.Equal("2026-07-01T14:05:00Z", pr.Cache.DetailFetchedAt)

	raw, err := json.Marshal(out)
	require.NoError(err)
	assert.Contains(string(raw), "latest_at")
	assert.NotContains(string(raw), "LatestAt")
	assert.NotContains(string(raw), "EventCount")
}

func TestFindReviewCandidatesPopulatesWorkflowMetadata(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/activity", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[{
			"id":"pr-comment","activity_type":"comment",
			"repo":{"provider":"github","platform_host":"github.com","repo_path":"acme/widget","owner":"acme","name":"widget"},
			"platform_host":"github.com","repo_owner":"acme","repo_name":"widget",
			"item_type":"pr","item_number":42,"item_title":"Retry budget",
			"item_url":"https://example.test/pr/42","item_state":"open",
			"author":"bob","item_author":"alice","created_at":"2026-07-01T14:00:00Z"
		}],"capped":false}`))
	})
	mux.HandleFunc("/api/v1/pulls", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{
			"Number":42,"Title":"Retry budget","State":"open","Author":"alice",
			"URL":"https://example.test/pr/42","KanbanStatus":"reviewing",
			"LastActivityAt":"2026-07-01T14:00:00Z",
			"repo":{"provider":"github","platform_host":"github.com","repo_path":"acme/widget","owner":"acme","name":"widget"},
			"platform_host":"github.com","repo_owner":"acme","repo_name":"widget"
		}]`))
	})
	mux.HandleFunc("/api/v1/issues", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
	})
	mux.HandleFunc("/api/v1/workflow-state", func(w http.ResponseWriter, r *http.Request) {
		if writeWorkflowProbeResponse(w, r) {
			return
		}
		query := r.URL.Query()
		assert.Equal("github|github.com/acme/widget", query.Get("repo"))
		assert.ElementsMatch([]string{"pr"}, query["item_type"])
		assert.Equal("true", query.Get("include_closed"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[{
			"provider":"github","platform_host":"github.com","owner":"acme","name":"widget","repo_path":"acme/widget",
			"item_type":"pr","number":42,"workflow":{
				"status":"reviewing","updated_at":"2026-07-01T14:10:00Z",
				"updated_source":"mcp","updated_actor":"agent","updated_reason":"claim"
			}
		}]}`))
	})
	mux.HandleFunc("/api/v1/host/github.com/pulls/github/acme/widget/42/stack", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"status":404,"code":"not_found","detail":"not stacked"}`))
	})
	s := newMCPTestServer(t, mux)

	out, err := s.findReviewCandidates(t.Context(), findCandidatesInput{})

	require.NoError(err)
	require.Len(out.Candidates, 1)
	workflow := out.Candidates[0].Workflow
	assert.Equal("reviewing", workflow.Status)
	assert.Equal("2026-07-01T14:10:00Z", workflow.UpdatedAt)
	assert.Equal("mcp", workflow.UpdatedSource)
	assert.Equal("agent", workflow.UpdatedActor)
	assert.Equal("claim", workflow.UpdatedReason)
}

func TestFindReviewCandidatesWorkflowFilters(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/activity", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[{
			"id":"pr-comment","activity_type":"comment",
			"repo":{"provider":"github","platform_host":"github.com","repo_path":"acme/widget","owner":"acme","name":"widget"},
			"platform_host":"github.com","repo_owner":"acme","repo_name":"widget",
			"item_type":"pr","item_number":42,"item_title":"Retry budget",
			"item_url":"https://example.test/pr/42","item_state":"open",
			"author":"bob","item_author":"alice","created_at":"2026-07-01T14:00:00Z"
		}],"capped":false}`))
	})
	mux.HandleFunc("/api/v1/pulls", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{
			"Number":42,"Title":"Retry budget","State":"open","Author":"alice",
			"URL":"https://example.test/pr/42","IsDraft":false,
			"KanbanStatus":"reviewing","LastActivityAt":"2026-07-01T14:00:00Z",
			"repo":{"provider":"github","platform_host":"github.com","repo_path":"acme/widget","owner":"acme","name":"widget"},
			"platform_host":"github.com","repo_owner":"acme","repo_name":"widget"
		}]`))
	})
	mux.HandleFunc("/api/v1/issues", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
	})
	handleEmptyWorkflowState(mux)
	stackCalls := 0
	mux.HandleFunc("/api/v1/host/github.com/pulls/github/acme/widget/42/stack", func(w http.ResponseWriter, _ *http.Request) {
		stackCalls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"stack_id":1,"stack_name":"stack","position":1,"size":1,"health":"ok","members":[]}`))
	})
	s := newMCPTestServer(t, mux)

	out, err := s.findReviewCandidates(t.Context(), findCandidatesInput{
		ExcludeWorkflowStates: []string{"reviewing"},
	})
	require.NoError(err)
	require.Empty(out.Candidates)
	assert.Equal(0, stackCalls)

	out, err = s.findReviewCandidates(t.Context(), findCandidatesInput{
		WorkflowStates: []string{"reviewing"},
	})
	require.NoError(err)
	require.Len(out.Candidates, 1)
	assert.Equal(1, stackCalls)
}

func TestFindReviewCandidatesDraftHandling(t *testing.T) {
	require := require.New(t)
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/activity", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[{
			"id":"pr-comment","activity_type":"comment",
			"repo":{"provider":"github","platform_host":"github.com","repo_path":"acme/widget","owner":"acme","name":"widget"},
			"platform_host":"github.com","repo_owner":"acme","repo_name":"widget",
			"item_type":"pr","item_number":42,"item_title":"Retry budget",
			"item_url":"https://example.test/pr/42","item_state":"open",
			"author":"bob","item_author":"alice","created_at":"2026-07-01T14:00:00Z"
		}],"capped":false}`))
	})
	mux.HandleFunc("/api/v1/pulls", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{
			"Number":42,"Title":"Retry budget","State":"open","Author":"alice",
			"URL":"https://example.test/pr/42","IsDraft":true,
			"KanbanStatus":"","LastActivityAt":"2026-07-01T14:00:00Z",
			"repo":{"provider":"github","platform_host":"github.com","repo_path":"acme/widget","owner":"acme","name":"widget"},
			"platform_host":"github.com","repo_owner":"acme","repo_name":"widget"
		}]`))
	})
	mux.HandleFunc("/api/v1/issues", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
	})
	handleEmptyWorkflowState(mux)
	s := newMCPTestServer(t, mux)

	out, err := s.findReviewCandidates(t.Context(), findCandidatesInput{})
	require.NoError(err)
	require.Empty(out.Candidates)

	out, err = s.findReviewCandidates(t.Context(), findCandidatesInput{IncludeDrafts: true})
	require.NoError(err)
	require.Len(out.Candidates, 1)
}

func TestFindReviewCandidatesPagesUntilActivityItemsAreLoaded(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	pullOffsets := []string{}
	issueOffsets := []string{}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/activity", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[{
			"id":"late-pr","activity_type":"comment",
			"repo":{"provider":"github","platform_host":"github.com","repo_path":"acme/widget","owner":"acme","name":"widget"},
			"platform_host":"github.com","repo_owner":"acme","repo_name":"widget",
			"item_type":"pr","item_number":42,"item_title":"Late PR",
			"item_url":"https://example.test/pr/42","item_state":"open",
			"author":"alice","item_author":"bob","created_at":"2026-07-01T14:00:00Z"
		},{
			"id":"late-issue","activity_type":"comment",
			"repo":{"provider":"github","platform_host":"github.com","repo_path":"acme/widget","owner":"acme","name":"widget"},
			"platform_host":"github.com","repo_owner":"acme","repo_name":"widget",
			"item_type":"issue","item_number":7,"item_title":"Late issue",
			"item_url":"https://example.test/issues/7","item_state":"open",
			"author":"carol","item_author":"dave","created_at":"2026-07-01T15:00:00Z"
		}],"capped":false}`))
	})
	mux.HandleFunc("/api/v1/pulls", func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		assert.Equal("200", query.Get("limit"))
		pullOffsets = append(pullOffsets, query.Get("offset"))
		w.Header().Set("Content-Type", "application/json")
		if query.Get("offset") == "" {
			_, _ = w.Write([]byte("["))
			for i := 1; i <= 200; i++ {
				if i > 1 {
					_, _ = w.Write([]byte(","))
				}
				_, _ = fmt.Fprintf(w, `{
					"Number":%d,"Title":"First page","State":"open","Author":"first",
					"URL":"https://example.test/pr/%d","LastActivityAt":"2026-07-01T12:00:00Z",
					"repo":{"provider":"github","platform_host":"github.com","repo_path":"acme/widget","owner":"acme","name":"widget"},
					"platform_host":"github.com","repo_owner":"acme","repo_name":"widget"
				}`, i+1000, i+1000)
			}
			_, _ = w.Write([]byte("]"))
			return
		}
		assert.Equal("200", query.Get("offset"))
		_, _ = w.Write([]byte(`[{
			"Number":42,"Title":"Late PR","State":"open","Author":"bob",
			"URL":"https://example.test/pr/42","IsDraft":false,
			"KanbanStatus":"waiting","LastActivityAt":"2026-07-01T14:00:00Z",
			"repo":{"provider":"github","platform_host":"github.com","repo_path":"acme/widget","owner":"acme","name":"widget"},
			"platform_host":"github.com","repo_owner":"acme","repo_name":"widget"
		}]`))
	})
	mux.HandleFunc("/api/v1/issues", func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		assert.Equal("200", query.Get("limit"))
		issueOffsets = append(issueOffsets, query.Get("offset"))
		w.Header().Set("Content-Type", "application/json")
		if query.Get("offset") == "" {
			_, _ = w.Write([]byte("["))
			for i := 1; i <= 200; i++ {
				if i > 1 {
					_, _ = w.Write([]byte(","))
				}
				_, _ = fmt.Fprintf(w, `{
					"Number":%d,"Title":"First page","State":"open","Author":"first",
					"URL":"https://example.test/issues/%d","LastActivityAt":"2026-07-01T12:00:00Z",
					"repo":{"provider":"github","platform_host":"github.com","repo_path":"acme/widget","owner":"acme","name":"widget"},
					"platform_host":"github.com","repo_owner":"acme","repo_name":"widget"
				}`, i+1000, i+1000)
			}
			_, _ = w.Write([]byte("]"))
			return
		}
		assert.Equal("200", query.Get("offset"))
		_, _ = w.Write([]byte(`[{
			"Number":7,"Title":"Late issue","State":"open","Author":"dave",
			"URL":"https://example.test/issues/7","WorkflowStatus":"reviewing",
			"LastActivityAt":"2026-07-01T15:00:00Z",
			"repo":{"provider":"github","platform_host":"github.com","repo_path":"acme/widget","owner":"acme","name":"widget"},
			"platform_host":"github.com","repo_owner":"acme","repo_name":"widget"
		}]`))
	})
	handleEmptyWorkflowState(mux)
	mux.HandleFunc("/api/v1/host/github.com/pulls/github/acme/widget/42/stack", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"status":404,"code":"not_found","detail":"not stacked"}`))
	})
	s := newMCPTestServer(t, mux)

	out, err := s.findReviewCandidates(t.Context(), findCandidatesInput{})

	require.NoError(err)
	require.Len(out.Candidates, 2)
	assert.Equal([]string{"", "200"}, pullOffsets)
	assert.Equal([]string{"", "200"}, issueOffsets)
	assert.Equal("issue", out.Candidates[0].Item.Type)
	assert.Equal(7, out.Candidates[0].Item.Number)
	assert.Equal("reviewing", out.Candidates[0].Workflow.Status)
	assert.Equal("pr", out.Candidates[1].Item.Type)
	assert.Equal(42, out.Candidates[1].Item.Number)
	assert.Equal("waiting", out.Candidates[1].Workflow.Status)
}

func TestFindReviewCandidatesStopsStackLookupsAfterCappedResult(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	stackCalls := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/activity", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[{
			"id":"pr-3","activity_type":"comment",
			"repo":{"provider":"github","platform_host":"github.com","repo_path":"acme/widget","owner":"acme","name":"widget"},
			"platform_host":"github.com","repo_owner":"acme","repo_name":"widget",
			"item_type":"pr","item_number":3,"item_title":"Newest",
			"item_url":"https://example.test/pr/3","item_state":"open",
			"author":"reviewer","item_author":"alice","created_at":"2026-07-01T15:00:00Z"
		},{
			"id":"pr-2","activity_type":"comment",
			"repo":{"provider":"github","platform_host":"github.com","repo_path":"acme/widget","owner":"acme","name":"widget"},
			"platform_host":"github.com","repo_owner":"acme","repo_name":"widget",
			"item_type":"pr","item_number":2,"item_title":"Middle",
			"item_url":"https://example.test/pr/2","item_state":"open",
			"author":"reviewer","item_author":"alice","created_at":"2026-07-01T14:00:00Z"
		},{
			"id":"pr-1","activity_type":"comment",
			"repo":{"provider":"github","platform_host":"github.com","repo_path":"acme/widget","owner":"acme","name":"widget"},
			"platform_host":"github.com","repo_owner":"acme","repo_name":"widget",
			"item_type":"pr","item_number":1,"item_title":"Oldest",
			"item_url":"https://example.test/pr/1","item_state":"open",
			"author":"reviewer","item_author":"alice","created_at":"2026-07-01T13:00:00Z"
		}],"capped":false}`))
	})
	mux.HandleFunc("/api/v1/pulls", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{
			"Number":1,"Title":"Oldest","State":"open","Author":"alice",
			"URL":"https://example.test/pr/1","LastActivityAt":"2026-07-01T13:00:00Z",
			"repo":{"provider":"github","platform_host":"github.com","repo_path":"acme/widget","owner":"acme","name":"widget"},
			"platform_host":"github.com","repo_owner":"acme","repo_name":"widget"
		},{
			"Number":2,"Title":"Middle","State":"open","Author":"alice",
			"URL":"https://example.test/pr/2","LastActivityAt":"2026-07-01T14:00:00Z",
			"repo":{"provider":"github","platform_host":"github.com","repo_path":"acme/widget","owner":"acme","name":"widget"},
			"platform_host":"github.com","repo_owner":"acme","repo_name":"widget"
		},{
			"Number":3,"Title":"Newest","State":"open","Author":"alice",
			"URL":"https://example.test/pr/3","LastActivityAt":"2026-07-01T15:00:00Z",
			"repo":{"provider":"github","platform_host":"github.com","repo_path":"acme/widget","owner":"acme","name":"widget"},
			"platform_host":"github.com","repo_owner":"acme","repo_name":"widget"
		}]`))
	})
	mux.HandleFunc("/api/v1/issues", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
	})
	handleEmptyWorkflowState(mux)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/stack") {
			stackCalls++
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"position":1,"size":1,"health":"ok"}`))
			return
		}
		http.NotFound(w, r)
	})
	s := newMCPTestServer(t, mux)

	out, err := s.findReviewCandidates(t.Context(), findCandidatesInput{Limit: 1})

	require.NoError(err)
	require.Len(out.Candidates, 1)
	assert.True(out.Capped)
	assert.Equal(3, out.Candidates[0].Item.Number)
	assert.Equal(2, stackCalls)
}
