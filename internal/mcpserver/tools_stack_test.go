package mcpserver

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetStackContextRejectsIssueAndTreatsNotFoundAsAbsent(t *testing.T) {
	assert := assert.New(t)
	s := newMCPTestServer(t, http.NewServeMux())

	_, err := s.getStackContext(t.Context(), getStackContextInput{
		Item: itemRefInput{Type: "issue", Provider: "github", Owner: "acme", Name: "widget", Number: 7},
	})
	assertDaemonErrorKind(t, err, "invalid_request")

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/pulls/github/acme/widget/42/stack", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"status":404,"code":"notFound","detail":"PR is not part of a stack"}`))
	})
	s = newMCPTestServer(t, mux)
	out, err := s.getStackContext(t.Context(), getStackContextInput{
		Item: itemRefInput{Type: "pr", Provider: "github", Owner: "acme", Name: "widget", Number: 42},
	})
	require.NoError(t, err)
	assert.False(out.Present)
	assert.Empty(out.Members)
}

func TestGetStackContextPropagatesMissingPull(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/pulls/github/acme/widget/99/stack", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"status":404,"code":"pullNotFound","detail":"pull request not found"}`))
	})
	s := newMCPTestServer(t, mux)

	_, err := s.getStackContext(t.Context(), getStackContextInput{
		Item: itemRefInput{Type: "pr", Provider: "github", Owner: "acme", Name: "widget", Number: 99},
	})

	var derr *daemonError
	require.ErrorAs(err, &derr)
	assert.Equal("not_found", derr.Kind)
	assert.Equal("pullNotFound", derr.Code)
}

func TestGetStackContextJoinsWorkflowStateAndMarksRequestedMember(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/pulls/github/acme/widget/42/stack", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"stack_id":9,"stack_name":"feature","position":2,"size":3,"health":"blocked",
			"members":[
				{"number":43,"title":"Tip","state":"open","is_draft":false,"position":3},
				{"number":41,"title":"Base","state":"open","is_draft":false,"position":1},
				{"number":42,"title":"Middle","state":"open","is_draft":true,"position":2}
			]
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
		assert.Equal("200", query.Get("limit"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[
			{"provider":"github","platform_host":"github.com","owner":"acme","name":"widget","repo_path":"acme/widget","item_type":"pr","number":42,"workflow":{"status":"reviewing"}},
			{"provider":"github","platform_host":"github.com","owner":"acme","name":"widget","repo_path":"acme/widget","item_type":"pr","number":41,"workflow":{"status":"waiting"}}
		]}`))
	})
	s := newMCPTestServer(t, mux)

	out, err := s.getStackContext(t.Context(), getStackContextInput{
		Item: itemRefInput{Type: "pr", Provider: "github", Owner: "acme", Name: "widget", Number: 42},
	})

	require.NoError(err)
	assert.True(out.Present)
	assert.Equal("blocked", out.Health)
	require.Len(out.Members, 3)
	assert.Equal(41, out.Members[0].Number)
	assert.Equal("waiting", out.Members[0].WorkflowStatus)
	assert.False(out.Members[0].IsRequested)
	assert.Equal(42, out.Members[1].Number)
	assert.True(out.Members[1].IsDraft)
	assert.True(out.Members[1].IsRequested)
	assert.Equal("reviewing", out.Members[1].WorkflowStatus)
	assert.Equal(43, out.Members[2].Number)
	assert.Equal("new", out.Members[2].WorkflowStatus)
}

func TestGetStackContextPagesWorkflowStateUntilMembersAreFound(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	cursors := []string{}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/pulls/github/acme/widget/42/stack", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"stack_id":9,"stack_name":"feature","position":2,"size":2,"health":"ok",
			"members":[
				{"number":41,"title":"Base","state":"open","is_draft":false,"position":1},
				{"number":42,"title":"Tip","state":"open","is_draft":false,"position":2}
			]
		}`))
	})
	mux.HandleFunc("/api/v1/workflow-state", func(w http.ResponseWriter, r *http.Request) {
		if writeWorkflowProbeResponse(w, r) {
			return
		}
		query := r.URL.Query()
		cursors = append(cursors, query.Get("cursor"))
		assert.Equal("200", query.Get("limit"))
		w.Header().Set("Content-Type", "application/json")
		if query.Get("cursor") == "" {
			_, _ = w.Write([]byte(`{"next_cursor":"page-2","items":[
				{"provider":"github","platform_host":"github.com","owner":"acme","name":"widget","repo_path":"acme/widget","item_type":"pr","number":1,"workflow":{"status":"waiting"}}
			]}`))
			return
		}
		assert.Equal("page-2", query.Get("cursor"))
		_, _ = w.Write([]byte(`{"items":[
			{"provider":"github","platform_host":"github.com","owner":"acme","name":"widget","repo_path":"acme/widget","item_type":"pr","number":41,"workflow":{"status":"waiting"}},
			{"provider":"github","platform_host":"github.com","owner":"acme","name":"widget","repo_path":"acme/widget","item_type":"pr","number":42,"workflow":{"status":"reviewing"}}
		]}`))
	})
	s := newMCPTestServer(t, mux)

	out, err := s.getStackContext(t.Context(), getStackContextInput{
		Item: itemRefInput{Type: "pr", Provider: "github", Owner: "acme", Name: "widget", Number: 42},
	})

	require.NoError(err)
	assert.Equal([]string{"", "page-2"}, cursors)
	require.Len(out.Members, 2)
	assert.Equal("waiting", out.Members[0].WorkflowStatus)
	assert.Equal("reviewing", out.Members[1].WorkflowStatus)
}
