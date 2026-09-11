package github

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	gh "github.com/google/go-github/v89/github"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/platform"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type workflowRateObserver struct{ requests, remaining int }

func (o *workflowRateObserver) RecordRequest()                 { o.requests++ }
func (o *workflowRateObserver) UpdateFromRate(r platform.Rate) { o.remaining = r.Remaining }
func TestWorkflowTransportShape(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	var readPaths, writePaths []string
	readServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		readPaths = append(readPaths, r.Method+" "+r.URL.RequestURI())
		w.Header().Set("X-RateLimit-Limit", "5000")
		w.Header().Set("X-RateLimit-Remaining", "4900")
		switch {
		case strings.Contains(r.URL.Path, "/contents/"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"type": "file", "sha": "blob-sha",
				"content":  base64.StdEncoding.EncodeToString([]byte("on: workflow_dispatch")),
				"encoding": "base64",
			})
		case strings.HasSuffix(r.URL.Path, "/environments"):
			if r.URL.Query().Get("page") == "" {
				w.Header().Set("Link", `<`+serverURL(r)+`?page=2>; rel="next"`)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"environments": []any{map[string]any{"name": "env-" + max(r.URL.Query().Get("page"), "1")}}})
		case strings.HasSuffix(r.URL.Path, "/jobs"):
			if r.URL.Query().Get("page") == "" {
				w.Header().Set("Link", `<`+serverURL(r)+`?page=2>; rel="next"`)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"jobs": []any{map[string]any{"id": 99}}})
		case r.URL.Path == "/api/v3/repos/acme/widgets/actions/runs/123":
			_ = json.NewEncoder(w).Encode(map[string]any{"id": 123, "status": "completed", "conclusion": "success"})
		case strings.HasSuffix(r.URL.Path, "/runs"):
			if r.URL.Query().Get("page") == "2" {
				w.Header().Set("Link", `<`+serverURL(r)+`?page=3>; rel="next"`)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"workflow_runs": []any{map[string]any{"id": 7}}})
		default:
			if r.URL.Query().Get("page") == "" {
				w.Header().Set("Link", `<`+serverURL(r)+`?page=2>; rel="next"`)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"workflows": []any{map[string]any{"id": 42}}})
		}
	}))
	defer readServer.Close()
	dispatchCount := 0
	var dispatchBodies []map[string]any
	writeServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writePaths = append(writePaths, r.Method+" "+r.URL.RequestURI())
		w.Header().Set("X-RateLimit-Limit", "5000")
		w.Header().Set("X-RateLimit-Remaining", "4800")
		var body map[string]any
		if !assert.NoError(json.NewDecoder(r.Body).Decode(&body)) {
			http.Error(w, "invalid request body", http.StatusBadRequest)
			return
		}
		dispatchBodies = append(dispatchBodies, body)
		dispatchCount++
		if dispatchCount == 1 {
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"workflow_run_id":123,"html_url":"https://example.test/runs/123"}`))
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer writeServer.Close()
	readGH, err := newEnterpriseGHClient(readServer.Client(), readServer.URL+"/", readServer.URL+"/")
	require.NoError(err)
	writeGH, err := newEnterpriseGHClient(writeServer.Client(), writeServer.URL+"/", writeServer.URL+"/")
	require.NoError(err)
	readTracker := &workflowRateObserver{}
	writeTracker := &workflowRateObserver{}
	client := &Client{
		gh: readGH, ghWrite: writeGH, rateTracker: readTracker, writeRateTracker: writeTracker,
	}

	workflows, err := client.ListRepositoryWorkflows(t.Context(), "acme", "widgets")
	require.NoError(err)
	require.Len(workflows, 2)
	content, sha, err := client.GetWorkflowDefinition(t.Context(), "acme", "widgets", ".github/workflows/release.yml", "main")
	require.NoError(err)
	assert.Equal("on: workflow_dispatch", content)
	assert.Equal("blob-sha", sha)
	environments, err := client.ListRepositoryEnvironments(t.Context(), "acme", "widgets")
	require.NoError(err)
	require.Len(environments, 2)
	page, err := client.ListManualWorkflowRuns(t.Context(), "acme", "widgets", 42, platform.WorkflowRunQuery{
		Cursor: "2", PerPage: 25, Event: "workflow_dispatch", Branch: "main",
	})
	require.NoError(err)
	assert.Equal("3", page.NextCursor)
	assert.False(page.Exhausted)
	finalPage, err := client.ListManualWorkflowRuns(t.Context(), "acme", "widgets", 42, platform.WorkflowRunQuery{PerPage: 10})
	require.NoError(err)
	assert.Empty(finalPage.NextCursor)
	assert.True(finalPage.Exhausted)
	run, err := client.GetManualWorkflowRun(t.Context(), "acme", "widgets", 123)
	require.NoError(err)
	assert.Equal(int64(123), run.GetID())
	assert.Equal("success", run.GetConclusion())
	assert.Contains(readPaths, "GET /api/v3/repos/acme/widgets/actions/runs/123")

	jobs, err := client.ListManualWorkflowJobs(t.Context(), "acme", "widgets", 99)
	require.NoError(err)
	require.Len(jobs, 2)
	details, err := client.DispatchManualWorkflow(t.Context(), "acme", "widgets", 42, gh.CreateWorkflowDispatchEventRequest{
		Ref: "main", Inputs: map[string]any{"version": "1.2.3"},
	})
	require.NoError(err)
	assert.Equal(int64(123), details.GetWorkflowRunID())
	details, err = client.DispatchManualWorkflow(t.Context(), "acme", "widgets", 42, gh.CreateWorkflowDispatchEventRequest{Ref: "main"})
	require.NoError(err)
	assert.Nil(details)

	assert.Equal([]map[string]any{
		{"ref": "main", "inputs": map[string]any{"version": "1.2.3"}, "return_run_details": true},
		{"ref": "main", "return_run_details": true},
	}, dispatchBodies)
	assert.Contains(readPaths, "GET /api/v3/repos/acme/widgets/actions/workflows?per_page=100")
	assert.Contains(readPaths, "GET /api/v3/repos/acme/widgets/actions/workflows?page=2&per_page=100")
	assert.Contains(readPaths, "GET /api/v3/repos/acme/widgets/contents/.github/workflows/release.yml?ref=main")
	assert.Contains(readPaths, "GET /api/v3/repos/acme/widgets/environments?per_page=100")
	assert.Contains(readPaths, "GET /api/v3/repos/acme/widgets/environments?page=2&per_page=100")
	assert.Contains(readPaths, "GET /api/v3/repos/acme/widgets/actions/workflows/42/runs?branch=main&event=workflow_dispatch&page=2&per_page=25")
	assert.Contains(readPaths, "GET /api/v3/repos/acme/widgets/actions/workflows/42/runs?per_page=10")
	assert.Contains(readPaths, "GET /api/v3/repos/acme/widgets/actions/runs/99/jobs?per_page=100")
	assert.Contains(readPaths, "GET /api/v3/repos/acme/widgets/actions/runs/99/jobs?page=2&per_page=100")
	assert.Equal([]string{
		"POST /api/v3/repos/acme/widgets/actions/workflows/42/dispatches",
		"POST /api/v3/repos/acme/widgets/actions/workflows/42/dispatches",
	}, writePaths)
	assert.Equal(10, readTracker.requests)
	assert.Equal(4900, readTracker.remaining)
	assert.Equal(2, writeTracker.requests)
	assert.Equal(4800, writeTracker.remaining)
}

func serverURL(r *http.Request) string {
	return "http://" + r.Host + r.URL.Path
}

func TestListManualWorkflowRunsRejectsInvalidCursor(t *testing.T) {
	client := &Client{platformHost: "github.com"}
	_, err := client.ListManualWorkflowRuns(t.Context(), "acme", "widgets", 42, platform.WorkflowRunQuery{Cursor: "zero"})
	require.ErrorIs(t, err, platform.ErrInvalidArgument)
}

func TestGetWorkflowDefinitionRejectsOversizedDecodedContent(t *testing.T) {
	oversized := bytes.Repeat([]byte("x"), MaxWorkflowDefinitionBytes+1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"type": "file", "sha": "too-large", "encoding": "base64",
			"content": base64.StdEncoding.EncodeToString(oversized),
		})
	}))
	defer server.Close()
	ghClient, err := newEnterpriseGHClient(server.Client(), server.URL+"/", server.URL+"/")
	require.NoError(t, err)
	client := &Client{gh: ghClient}
	_, _, err = client.GetWorkflowDefinition(t.Context(), "acme", "widgets", "release.yml", "main")
	require.ErrorContains(t, err, "exceeds")
}
