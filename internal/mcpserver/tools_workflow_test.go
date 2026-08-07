package mcpserver

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSetItemWorkflowStateTool(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	probeCalls := 0
	defaultCalls := 0
	hostCalls := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/workflow-state", func(w http.ResponseWriter, r *http.Request) {
		probeCalls++
		assert.Equal(http.MethodGet, r.Method)
		assert.Equal("1", r.URL.Query().Get("limit"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[]}`))
	})
	mux.HandleFunc("/api/v1/workflow-state/pr/github/acme/widget/42", func(w http.ResponseWriter, r *http.Request) {
		defaultCalls++
		assert.Equal(http.MethodPut, r.Method)
		var body struct {
			Status         string `json:"status"`
			ExpectedStatus string `json:"expected_status"`
			Source         string `json:"source"`
			Reason         string `json:"reason"`
			Actor          string `json:"actor"`
		}
		if !assert.NoError(json.NewDecoder(r.Body).Decode(&body)) {
			return
		}
		assert.Equal("reviewing", body.Status)
		assert.Equal("new", body.ExpectedStatus)
		assert.Equal("mcp", body.Source)
		assert.Equal("checking docs", body.Reason)
		assert.Equal("agent", body.Actor)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"previous_status":"new",
			"status":"reviewing",
			"updated_at":"2026-07-01T16:01:00Z",
			"updated_source":"mcp",
			"updated_actor":"agent",
			"updated_reason":"checking docs"
		}`))
	})
	mux.HandleFunc("/api/v1/host/git.example.com/workflow-state/issue/gitlab/Group%2FSub/Project/7", func(w http.ResponseWriter, r *http.Request) {
		hostCalls++
		assert.Equal(http.MethodPut, r.Method)
		var body map[string]any
		if !assert.NoError(json.NewDecoder(r.Body).Decode(&body)) {
			return
		}
		assert.Equal("waiting", body["status"])
		assert.Equal("mcp", body["source"])
		assert.NotContains(body, "expected_status")
		assert.Equal(true, body["force"])
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"previous_status":"reviewing",
			"status":"waiting",
			"updated_at":"2026-07-01T17:01:00Z",
			"updated_source":"mcp"
		}`))
	})
	s := newMCPTestServer(t, mux)

	out, err := s.setItemWorkflowState(t.Context(), setWorkflowInput{
		Item: itemRefInput{
			Type: "pr", Provider: "github", Owner: "acme", Name: "widget", Number: 42,
		},
		Status:         "reviewing",
		ExpectedStatus: "new",
		Reason:         "checking docs",
		Actor:          "agent",
	})
	require.NoError(err)
	assert.Equal("new", out.PreviousStatus)
	assert.Equal("reviewing", out.Status)
	assert.Equal("2026-07-01T16:01:00Z", out.UpdatedAt)
	assert.Equal("mcp", out.UpdatedSource)
	assert.Equal("agent", out.UpdatedActor)
	assert.Equal("checking docs", out.UpdatedReason)

	hostOut, err := s.setItemWorkflowState(t.Context(), setWorkflowInput{
		Item: itemRefInput{
			Type: "issue", Provider: "gitlab", PlatformHost: "git.example.com",
			Owner: "Group/Sub", Name: "Project", Number: 7,
		},
		Status: "waiting",
		Force:  true,
	})
	require.NoError(err)
	assert.Equal("reviewing", hostOut.PreviousStatus)
	assert.Equal("waiting", hostOut.Status)
	assert.Equal(1, probeCalls)
	assert.Equal(1, defaultCalls)
	assert.Equal(1, hostCalls)
}

func TestSetItemWorkflowStateRequiresExpectedStatusOrForce(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	s := newMCPTestServer(t, http.NewServeMux())

	_, err := s.setItemWorkflowState(t.Context(), setWorkflowInput{
		Item:   itemRefInput{Type: "pr", Provider: "github", Owner: "acme", Name: "widget", Number: 42},
		Status: "reviewing",
	})

	require.Error(err)
	var derr *daemonError
	require.ErrorAs(err, &derr)
	assert.Equal("invalid_request", derr.Kind)
	assert.Contains(derr.Message, "expected_status")
}

func TestSetItemWorkflowStateRejectsExpectedStatusWithForce(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	s := newMCPTestServer(t, http.NewServeMux())

	_, err := s.setItemWorkflowState(t.Context(), setWorkflowInput{
		Item:           itemRefInput{Type: "pr", Provider: "github", Owner: "acme", Name: "widget", Number: 42},
		Status:         "reviewing",
		ExpectedStatus: "new",
		Force:          true,
	})

	require.Error(err)
	var derr *daemonError
	require.ErrorAs(err, &derr)
	assert.Equal("invalid_request", derr.Kind)
	assert.Contains(derr.Message, "force")
}

func TestSetItemWorkflowStateConflict(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/workflow-state", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"items":[]}`))
	})
	mux.HandleFunc("/api/v1/workflow-state/pr/github/acme/widget/42", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(`{
			"status":409,
			"code":"conflict",
			"detail":"workflow status conflict",
			"details":{"current_status":"reviewing","expected_status":"new"}
		}`))
	})
	s := newMCPTestServer(t, mux)

	_, err := s.setItemWorkflowState(t.Context(), setWorkflowInput{
		Item:           itemRefInput{Type: "pr", Provider: "github", Owner: "acme", Name: "widget", Number: 42},
		Status:         "waiting",
		ExpectedStatus: "new",
	})

	require.Error(err)
	var derr *daemonError
	require.ErrorAs(err, &derr)
	assert.Equal("conflict", derr.Kind)
	assert.Equal("reviewing", derr.Details["current_status"])
	assert.Equal("new", derr.Details["expected_status"])
}

func TestWorkflowToolIsOnlyPUTTool(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	_, file, _, ok := runtime.Caller(0)
	require.True(ok)
	paths, err := filepath.Glob(filepath.Join(filepath.Dir(file), "tools_*.go"))
	require.NoError(err)

	var callers []string
	for _, path := range paths {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		source, err := os.ReadFile(path)
		require.NoError(err)
		if bytes.Contains(source, []byte(".putJSON(")) {
			callers = append(callers, filepath.Base(path))
		}
	}
	slices.Sort(callers)
	assert.Equal([]string{"tools_workflow.go"}, callers)
}
