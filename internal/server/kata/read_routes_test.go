package kata

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/server/httpapi"
)

func TestKataIssueDetailResponseTransformsDetailToFreeFormObject(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	response := &kataIssueDetailResponse{}
	assert.Nil(response.TransformSchema(nil, nil))

	schema := &huma.Schema{}
	transformed := response.TransformSchema(nil, schema)

	require.Same(schema, transformed)
	require.Contains(transformed.Properties, "detail")
	assert.Equal("object", transformed.Properties["detail"].Type)
	assert.Equal(true, transformed.Properties["detail"].AdditionalProperties)
}

func TestKataClientRejectsBlankPinnedDaemon(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	srv, _ := setupTestServer(t)

	client, problem := srv.kataClientForDaemon(" \t ")

	assert.Nil(client)
	require.NotNil(problem)
	assert.Equal(http.StatusBadRequest, problem.GetStatus())
	assert.Equal(httpapi.CodeValidationError, problem.Code)
}

func TestKataPinnedReadRoutesForwardOnlyToPathDaemon(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	var mu sync.Mutex
	var paths []string
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.RequestURI())
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/health":
			_, _ = w.Write([]byte(`{"ok":true,"api_schema_version":"0.10.4"}`))
		case "/api/v1/ui/references":
			if r.URL.Query().Get("issue_uid") == "issue-closed" {
				assert.Empty(r.URL.Query().Get("q"))
				assert.Equal("2", r.URL.Query().Get("limit"))
				_, _ = w.Write([]byte(`{"issues":[{"uid":"issue-closed","project_uid":"project-a","project_name":"Project A","short_id":"A-2","qualified_id":"project-a:A-2","title":"Completed task","status":"closed"}]}`))
				return
			}
			if r.URL.Query().Get("q") == "Project A#closed-task" {
				assert.Equal("200", r.URL.Query().Get("limit"))
				_, _ = w.Write([]byte(`{"issues":[{"uid":"issue-closed","project_uid":"project-a","project_name":"Project display name","short_id":"closed-task","qualified_id":"Project A#closed-task","title":"Completed task","status":"closed"}]}`))
				return
			}
			if r.URL.Query().Get("q") == "closed-task" {
				assert.Empty(r.URL.Query().Get("project_uid"))
				assert.Equal("200", r.URL.Query().Get("limit"))
				_, _ = w.Write([]byte(`{"issues":[{"uid":"issue-closed","project_uid":"project-a","project_name":"Project A","short_id":"closed-task","qualified_id":"Project A#closed-task","title":"Completed task","status":"closed"}]}`))
				return
			}
			if r.URL.Query().Get("q") == "ambiguous" {
				_, _ = w.Write([]byte(`{"issues":[{"uid":"issue-one","project_uid":"project-a","short_id":"ambiguous","status":"closed"},{"uid":"issue-two","project_uid":"project-b","short_id":"ambiguous","status":"open"}]}`))
				return
			}
			if r.URL.Query().Get("q") != "" {
				assert.Equal("needle", r.URL.Query().Get("q"))
				assert.Equal([]string{"issue-a", "issue-b"}, r.URL.Query()["issue_uid"])
				assert.Equal("200", r.URL.Query().Get("limit"))
				assert.Equal("project-a", r.URL.Query().Get("project_uid"))
				_, _ = w.Write([]byte(`{"issues":[{"uid":"issue-a","project_uid":"project-a","project_name":"Project A","short_id":"A-1","qualified_id":"project-a:A-1","title":"Linked task","status":"open"}]}`))
				return
			} else {
				assert.Empty(r.URL.Query()["issue_uid"])
				assert.Equal("200", r.URL.Query().Get("limit"))
			}
			assert.Equal("project-a", r.URL.Query().Get("project_uid"))
			_, _ = w.Write([]byte(`{"issues":[{"uid":"issue-a","project_uid":"project-a","project_name":"Project A","short_id":"A-1","qualified_id":"project-a:A-1","title":"Linked task","status":"open"},{"uid":"issue-closed","project_uid":"project-a","project_name":"Project A","short_id":"A-2","qualified_id":"project-a:A-2","title":"Completed task","status":"closed"}]}`))
		case "/api/v1/projects":
			_, _ = w.Write([]byte(`{"projects":[{"id":17,"uid":"project-a","name":"Project A"},{"id":18,"uid":"project-b","name":"Project B"}]}`))
		case "/api/v1/ui/issue-reference":
			projectID := r.URL.Query().Get("project_id")
			switch r.URL.Query().Get("ref") {
			case "closed-task":
				if projectID != "17" {
					http.NotFound(w, r)
					return
				}
				_, _ = w.Write([]byte(`{"issue":{"uid":"issue-closed","project_uid":"project-a"}}`))
			case "ambiguous":
				_, _ = w.Write([]byte(`{"issue":{"uid":"issue-` + projectID + `","project_uid":"project-` + projectID + `"}}`))
			default:
				http.NotFound(w, r)
			}
		case "/api/v1/issues/issue-a":
			_, _ = w.Write([]byte(`{"uid":"issue-a","title":"Linked task","future_field":{"nested":true}}`))
		case "/api/v1/ui/launch-target":
			assert.Equal("issue-a", r.URL.Query().Get("issue_uid"))
			_, _ = w.Write([]byte(`{"available":true,"url":"https://kata.example.test/issues/issue-a"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer primary.Close()
	var secondaryCalls int
	secondary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		secondaryCalls++
		http.Error(w, "wrong daemon", http.StatusInternalServerError)
	}))
	defer secondary.Close()

	home := t.TempDir()
	t.Setenv("KATA_HOME", home)
	writeKataServerCatalog(t, home, `
[[daemon]]
name = "primary"
url = "`+primary.URL+`"

[[daemon]]
name = "secondary"
url = "`+secondary.URL+`"
`)
	srv, _ := setupTestServer(t)

	references := doJSON(t, srv, http.MethodGet,
		"/api/v1/kata/daemons/primary/references?q=needle&project_uid=project-a&issue_uid=issue-a&issue_uid=issue-b&limit=500", nil)
	require.Equal(http.StatusOK, references.Code, references.Body.String())
	var referenceBody struct {
		Issues []kataIssueReference `json:"issues"`
	}
	require.NoError(json.NewDecoder(references.Body).Decode(&referenceBody))
	require.Len(referenceBody.Issues, 1)
	assert.Equal("issue-a", referenceBody.Issues[0].UID)

	closedUIDReferences := doJSON(t, srv, http.MethodGet,
		"/api/v1/kata/daemons/primary/references?issue_uid=issue-closed&limit=2", nil)
	require.Equal(http.StatusOK, closedUIDReferences.Code, closedUIDReferences.Body.String())
	require.NoError(json.NewDecoder(closedUIDReferences.Body).Decode(&referenceBody))
	require.Len(referenceBody.Issues, 1)
	assert.Equal("issue-closed", referenceBody.Issues[0].UID)
	assert.Equal("closed", referenceBody.Issues[0].Status)

	emptyQueryReferences := doJSON(t, srv, http.MethodGet,
		"/api/v1/kata/daemons/primary/references?project_uid=project-a", nil)
	require.Equal(http.StatusOK, emptyQueryReferences.Code, emptyQueryReferences.Body.String())
	require.NoError(json.NewDecoder(emptyQueryReferences.Body).Decode(&referenceBody))
	require.Len(referenceBody.Issues, 1)
	assert.Equal("issue-a", referenceBody.Issues[0].UID)

	resolved := doJSON(t, srv, http.MethodGet,
		"/api/v1/kata/daemons/primary/issue-reference?project=Project%20A&ref=closed-task", nil)
	require.Equal(http.StatusOK, resolved.Code, resolved.Body.String())
	var resolvedBody struct {
		UID        string `json:"uid"`
		ProjectUID string `json:"project_uid"`
	}
	require.NoError(json.NewDecoder(resolved.Body).Decode(&resolvedBody))
	assert.Equal("issue-closed", resolvedBody.UID)
	assert.Equal("project-a", resolvedBody.ProjectUID)

	ambiguous := doJSON(t, srv, http.MethodGet,
		"/api/v1/kata/daemons/primary/issue-reference?ref=ambiguous", nil)
	assert.Equal(http.StatusNotFound, ambiguous.Code, ambiguous.Body.String())

	bareResolved := doJSON(t, srv, http.MethodGet,
		"/api/v1/kata/daemons/primary/issue-reference?ref=closed-task", nil)
	require.Equal(http.StatusOK, bareResolved.Code, bareResolved.Body.String())
	require.NoError(json.NewDecoder(bareResolved.Body).Decode(&resolvedBody))
	assert.Equal("issue-closed", resolvedBody.UID)
	assert.Equal("project-a", resolvedBody.ProjectUID)

	detail := doJSON(t, srv, http.MethodGet,
		"/api/v1/kata/daemons/primary/issues/issue-a", nil)
	require.Equal(http.StatusOK, detail.Code, detail.Body.String())
	var detailBody struct {
		DaemonHealth     string                     `json:"daemon_health"`
		APISchemaVersion string                     `json:"api_schema_version"`
		Detail           map[string]json.RawMessage `json:"detail"`
	}
	require.NoError(json.NewDecoder(detail.Body).Decode(&detailBody))
	assert.Equal("connected", detailBody.DaemonHealth)
	assert.Equal("0.10.4", detailBody.APISchemaVersion)
	assert.JSONEq(`{"nested":true}`, string(detailBody.Detail["future_field"]))

	launch := doJSON(t, srv, http.MethodGet,
		"/api/v1/kata/daemons/primary/issues/issue-a/launch-target", nil)
	require.Equal(http.StatusOK, launch.Code, launch.Body.String())
	var launchBody kataLaunchTarget
	require.NoError(json.NewDecoder(launch.Body).Decode(&launchBody))
	assert.True(launchBody.Available)
	assert.Equal("https://kata.example.test/issues/issue-a", launchBody.URL)
	assert.Zero(secondaryCalls)

	mu.Lock()
	defer mu.Unlock()
	assert.Equal([]string{
		"/api/v1/health",
		"/api/v1/ui/references?issue_uid=issue-a&issue_uid=issue-b&limit=200&project_uid=project-a&q=needle",
		"/api/v1/ui/references?issue_uid=issue-closed&limit=2",
		"/api/v1/ui/references?limit=200&project_uid=project-a",
		"/api/v1/ui/references?limit=200&q=Project+A%23closed-task",
		"/api/v1/projects",
		"/api/v1/ui/issue-reference?project_id=17&ref=ambiguous",
		"/api/v1/ui/issue-reference?project_id=18&ref=ambiguous",
		"/api/v1/projects",
		"/api/v1/ui/issue-reference?project_id=17&ref=closed-task",
		"/api/v1/ui/issue-reference?project_id=18&ref=closed-task",
		"/api/v1/health",
		"/api/v1/issues/issue-a",
		"/api/v1/ui/launch-target?issue_uid=issue-a",
	}, paths)
}

func TestKataIssueDetailRouteStopsWhenPathDaemonIsDown(t *testing.T) {
	assert := assert.New(t)
	var detailCalls int
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/health" {
			http.Error(w, "down", http.StatusInternalServerError)
			return
		}
		detailCalls++
		http.NotFound(w, r)
	}))
	defer down.Close()

	home := t.TempDir()
	t.Setenv("KATA_HOME", home)
	writeKataServerCatalog(t, home, `
[[daemon]]
name = "down"
url = "`+down.URL+`"
`)
	srv, _ := setupTestServer(t)

	rr := doJSON(t, srv, http.MethodGet,
		"/api/v1/kata/daemons/down/issues/issue-a", nil)
	require.Equal(t, http.StatusServiceUnavailable, rr.Code, rr.Body.String())
	problem := decodeProblem(t, rr)
	assert.Equal(httpapi.CodeServiceUnavailable, problem.Code)
	assert.Equal("down", problem.Details["daemon"])
	assert.Zero(detailCalls)
}

func TestKataReferencesRouteReportsDaemonOnUpstreamFailure(t *testing.T) {
	assert := assert.New(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unavailable", http.StatusInternalServerError)
	}))
	defer upstream.Close()

	home := t.TempDir()
	t.Setenv("KATA_HOME", home)
	writeKataServerCatalog(t, home, `
[[daemon]]
name = "failing"
url = "`+upstream.URL+`"
`)
	srv, _ := setupTestServer(t)

	rr := doJSON(t, srv, http.MethodGet,
		"/api/v1/kata/daemons/failing/references", nil)
	require.Equal(t, http.StatusBadGateway, rr.Code, rr.Body.String())
	problem := decodeProblem(t, rr)
	assert.Equal(httpapi.CodeUpstreamError, problem.Code)
	assert.Equal("failing", problem.Details["daemon"])
	assert.NotContains(problem.Details, "platformHost")
}

func TestKataReferencesRouteExplainsIncompatibleDaemonBeforeNarrowRead(t *testing.T) {
	assert := assert.New(t)
	var referenceCalls int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/v1/health" {
			_, _ = w.Write([]byte(`{"ok":true,"api_schema_version":"0.7.0"}`))
			return
		}
		referenceCalls++
		http.NotFound(w, r)
	}))
	defer upstream.Close()

	home := t.TempDir()
	t.Setenv("KATA_HOME", home)
	writeKataServerCatalog(t, home, `
[[daemon]]
name = "old"
url = "`+upstream.URL+`"
`)
	srv, _ := setupTestServer(t)

	rr := doJSON(t, srv, http.MethodGet,
		"/api/v1/kata/daemons/old/references?q=task", nil)
	require.Equal(t, http.StatusServiceUnavailable, rr.Code, rr.Body.String())
	problem := decodeProblem(t, rr)
	assert.Equal(httpapi.CodeServiceUnavailable, problem.Code)
	assert.Equal("incompatible_api_schema", problem.Details["reason"])
	assert.Equal("0.7.0", problem.Details["api_schema_version"])
	assert.Equal(">=0.9.0 and <0.11.0", problem.Details["supported_api_schema"])
	assert.Contains(problem.Detail, "Upgrade Kata")
	assert.Zero(referenceCalls)
}

func TestKataReferencesRouteCollectsRequestedOpenResultsBeyondClosedMatches(t *testing.T) {
	assert := assert.New(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/health" {
			_, _ = w.Write([]byte(`{"ok":true,"api_schema_version":"0.10.0"}`))
			return
		}
		assert.Equal("/api/v1/ui/references", r.URL.Path)
		assert.Equal("task", r.URL.Query().Get("q"))
		assert.Equal("200", r.URL.Query().Get("limit"))
		w.Header().Set("Content-Type", "application/json")
		issues := make([]kataIssueReference, 0, 100)
		for i := range 50 {
			issues = append(issues, kataIssueReference{
				UID: "closed-" + strconv.Itoa(i), Status: "closed",
			})
		}
		for i := range 50 {
			issues = append(issues, kataIssueReference{
				UID: "open-" + strconv.Itoa(i), Status: "open",
			})
		}
		assert.NoError(json.NewEncoder(w).Encode(map[string]any{"issues": issues}))
	}))
	defer upstream.Close()

	home := t.TempDir()
	t.Setenv("KATA_HOME", home)
	writeKataServerCatalog(t, home, `
[[daemon]]
name = "primary"
url = "`+upstream.URL+`"
`)
	srv, _ := setupTestServer(t)

	rr := doJSON(t, srv, http.MethodGet,
		"/api/v1/kata/daemons/primary/references?q=task&limit=50", nil)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var body kataReferencesResponse
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&body))
	require.Len(t, body.Issues, 50)
	for _, issue := range body.Issues {
		assert.Equal("open", issue.Status)
	}
}

func TestKataReferencesRouteFailsClosedWhenOpenResultsExceedDaemonWindow(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/health" {
			_, _ = w.Write([]byte(`{"ok":true,"api_schema_version":"0.10.0"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		issues := make([]kataIssueReference, 200)
		for i := range issues {
			issues[i] = kataIssueReference{UID: "closed-" + strconv.Itoa(i), Status: "closed"}
		}
		assert.NoError(t, json.NewEncoder(w).Encode(map[string]any{"issues": issues}))
	}))
	defer upstream.Close()

	home := t.TempDir()
	t.Setenv("KATA_HOME", home)
	writeKataServerCatalog(t, home, `
[[daemon]]
name = "primary"
url = "`+upstream.URL+`"
`)
	srv, _ := setupTestServer(t)

	rr := doJSON(t, srv, http.MethodGet,
		"/api/v1/kata/daemons/primary/references?q=task&limit=50", nil)
	require.Equal(t, http.StatusBadGateway, rr.Code, rr.Body.String())
	problem := decodeProblem(t, rr)
	assert.Equal(t, httpapi.CodeUpstreamError, problem.Code)
}
