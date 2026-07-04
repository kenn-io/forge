package mcpserver

import (
	"encoding/json"
	"net/http"
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

func TestFindReviewCandidatesWorkflowFilters(t *testing.T) {
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
	s := newMCPTestServer(t, mux)

	out, err := s.findReviewCandidates(t.Context(), findCandidatesInput{
		ExcludeWorkflowStates: []string{"reviewing"},
	})
	require.NoError(err)
	require.Empty(out.Candidates)

	out, err = s.findReviewCandidates(t.Context(), findCandidatesInput{
		WorkflowStates: []string{"reviewing"},
	})
	require.NoError(err)
	require.Len(out.Candidates, 1)
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
	s := newMCPTestServer(t, mux)

	out, err := s.findReviewCandidates(t.Context(), findCandidatesInput{})
	require.NoError(err)
	require.Empty(out.Candidates)

	out, err = s.findReviewCandidates(t.Context(), findCandidatesInput{IncludeDrafts: true})
	require.NoError(err)
	require.Len(out.Candidates, 1)
}
