package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/middleman/internal/db"
)

func startKataTaskDetailDaemon(t *testing.T, detailBody, projectsBody string) *httptest.Server {
	t.Helper()
	daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/issues/issue-kata-1":
			w.Header().Set("ETag", `"rev-4"`)
			_, _ = w.Write([]byte(detailBody))
		case "/api/v1/projects":
			_, _ = w.Write([]byte(projectsBody))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(daemon.Close)
	return daemon
}

func kataTaskDetailTestConfig(t *testing.T, kataTOML string) string {
	t.Helper()
	cloneDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(cloneDir, ".kata.toml"), []byte(kataTOML), 0o644))
	return fmt.Sprintf(`
sync_interval = "5m"
github_token_env = "MIDDLEMAN_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091

[[repos]]
owner = "acme"
name = "widget"
worktree_base_path = %q
`, cloneDir)
}

// kataWorkspaceTargetViaTaskDetail resolves a workspace target through the
// combined task-detail endpoint, standing up a fake daemon that serves the
// supplied identity fields as its issue detail and projects listing.
func kataWorkspaceTargetViaTaskDetail(t *testing.T, srv *Server, identity map[string]string) kataWorkspaceTargetResponse {
	t.Helper()
	issueUID := identity["issue_uid"]
	issue := map[string]any{
		"uid":         issueUID,
		"project_id":  7,
		"project_uid": identity["project_uid"],
		"revision":    1,
	}
	for _, key := range []string{"short_id", "qualified_id", "title"} {
		if value := identity[key]; value != "" {
			issue[key] = value
		}
	}
	detailBody, err := json.Marshal(map[string]any{"issue": issue, "comments": []any{}, "labels": []any{}, "links": []any{}})
	require.NoError(t, err)
	projectsBody, err := json.Marshal(map[string]any{"projects": []map[string]any{{
		"id": 7, "uid": identity["project_uid"], "name": identity["project_name"],
	}}})
	require.NoError(t, err)

	daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/issues/" + issueUID:
			_, _ = w.Write(detailBody)
		case "/api/v1/projects":
			_, _ = w.Write(projectsBody)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(daemon.Close)
	home := t.TempDir()
	t.Setenv("KATA_HOME", home)
	writeKataProxyCatalog(t, home, "\n[[daemon]]\nname = \""+identity["daemon_id"]+"\"\nurl = \""+daemon.URL+"\"\n")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/kata/tasks/"+url.PathEscape(issueUID), nil)
	req.Host = "127.0.0.1:8091"
	req.Header.Set("X-Middleman-Kata-Daemon", identity["daemon_id"])
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var resp struct {
		WorkspaceTarget kataWorkspaceTargetResponse `json:"workspace_target"`
	}
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&resp))
	return resp.WorkspaceTarget
}

func TestKataTaskDetailReturnsDetailAndWorkspaceTarget(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	daemon := startKataTaskDetailDaemon(t,
		`{"issue":{"uid":"issue-kata-1","project_id":7,"project_uid":"project-kata","short_id":"task-123","title":"Fix widget","revision":4},"comments":[],"labels":[],"links":[]}`,
		`{"projects":[{"id":7,"uid":"project-kata","name":"Kata"}]}`,
	)
	home := t.TempDir()
	t.Setenv("KATA_HOME", home)
	writeKataProxyCatalog(t, home, `
[[daemon]]
name = "desktop"
url = "`+daemon.URL+`"
`)
	// Name-based mapping: only the daemon's projects list carries the
	// project name, so a passing test proves the endpoint fetched it.
	srv, database, _ := setupTestServerWithConfigContent(t, kataTaskDetailTestConfig(t, "[project]\nname = \"Kata\"\n"), &mockGH{})
	_, err := database.UpsertRepo(t.Context(), db.GitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)

	rr := doJSON(t, srv, http.MethodGet, "/api/v1/kata/tasks/issue-kata-1", nil)
	require.Equal(http.StatusOK, rr.Code, rr.Body.String())

	var resp struct {
		Detail struct {
			Issue struct {
				UID   string `json:"uid"`
				Title string `json:"title"`
			} `json:"issue"`
		} `json:"detail"`
		ETag            string                      `json:"etag"`
		WorkspaceTarget kataWorkspaceTargetResponse `json:"workspace_target"`
	}
	require.NoError(json.NewDecoder(rr.Body).Decode(&resp))
	assert.Equal("issue-kata-1", resp.Detail.Issue.UID)
	assert.Equal("Fix widget", resp.Detail.Issue.Title)
	assert.Equal(`"rev-4"`, resp.ETag)
	assert.True(resp.WorkspaceTarget.Available)
	require.NotNil(resp.WorkspaceTarget.Repo)
	assert.Equal("acme", resp.WorkspaceTarget.Repo.Owner)
	assert.Equal("widget", resp.WorkspaceTarget.Repo.Name)
	assert.Equal(db.KataWorkspaceItemKey(db.WorkspaceKataMetadata{
		DaemonID:   "desktop",
		ProjectUID: "project-kata",
		IssueUID:   "issue-kata-1",
	}), resp.WorkspaceTarget.ItemKey)
}

func TestKataTaskDetailWithoutMappingStillReturnsDetail(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	daemon := startKataTaskDetailDaemon(t,
		`{"issue":{"uid":"issue-kata-1","project_id":7,"project_uid":"project-unmapped","short_id":"task-123","title":"Fix widget","revision":4},"comments":[],"labels":[],"links":[]}`,
		`{"projects":[{"id":7,"uid":"project-unmapped","name":"Unmapped"}]}`,
	)
	home := t.TempDir()
	t.Setenv("KATA_HOME", home)
	writeKataProxyCatalog(t, home, `
[[daemon]]
name = "desktop"
url = "`+daemon.URL+`"
`)
	srv, _ := setupTestServer(t)

	rr := doJSON(t, srv, http.MethodGet, "/api/v1/kata/tasks/issue-kata-1", nil)
	require.Equal(http.StatusOK, rr.Code, rr.Body.String())

	var resp struct {
		Detail struct {
			Issue struct {
				UID string `json:"uid"`
			} `json:"issue"`
		} `json:"detail"`
		WorkspaceTarget kataWorkspaceTargetResponse `json:"workspace_target"`
	}
	require.NoError(json.NewDecoder(rr.Body).Decode(&resp))
	assert.Equal("issue-kata-1", resp.Detail.Issue.UID)
	assert.False(resp.WorkspaceTarget.Available)
}

func TestKataTaskDetailSetsDaemonVaryHeader(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	daemon := startKataTaskDetailDaemon(t,
		`{"issue":{"uid":"issue-kata-1","project_id":7,"project_uid":"project-kata","short_id":"task-123","title":"Fix widget","revision":4},"comments":[],"labels":[],"links":[]}`,
		`{"projects":[{"id":7,"uid":"project-kata","name":"Kata"}]}`,
	)
	home := t.TempDir()
	t.Setenv("KATA_HOME", home)
	writeKataProxyCatalog(t, home, `
[[daemon]]
name = "desktop"
url = "`+daemon.URL+`"
`)
	srv, _ := setupTestServer(t)

	rr := doJSON(t, srv, http.MethodGet, "/api/v1/kata/tasks/issue-kata-1", nil)

	require.Equal(http.StatusOK, rr.Code, rr.Body.String())
	assert.Contains(rr.Header().Values("Vary"), "X-Middleman-Kata-Daemon")
}

func TestKataTaskDetailDoesNotWaitOnHungProjectsRead(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	release := make(chan struct{})
	daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/issues/issue-kata-1":
			_, _ = w.Write([]byte(`{"issue":{"uid":"issue-kata-1","project_id":7,"project_uid":"project-kata","project_name":"Kata","short_id":"task-123","title":"Fix widget","revision":4},"comments":[],"labels":[],"links":[]}`))
		case "/api/v1/projects":
			<-release
			_, _ = w.Write([]byte(`{"projects":[]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(func() {
		close(release)
		daemon.Close()
	})
	home := t.TempDir()
	t.Setenv("KATA_HOME", home)
	writeKataProxyCatalog(t, home, `
[[daemon]]
name = "desktop"
url = "`+daemon.URL+`"
`)
	srv, _ := setupTestServer(t)

	start := time.Now()
	rr := doJSON(t, srv, http.MethodGet, "/api/v1/kata/tasks/issue-kata-1", nil)
	elapsed := time.Since(start)

	require.Equal(http.StatusOK, rr.Code, rr.Body.String())
	// The issue payload already carries project_name, so the hung projects
	// read must not hold the detail response even for its own short budget.
	assert.Less(elapsed, kataDaemonProjectsReadTimeout)

	var resp struct {
		Detail struct {
			Issue struct {
				UID string `json:"uid"`
			} `json:"issue"`
		} `json:"detail"`
	}
	require.NoError(json.NewDecoder(rr.Body).Decode(&resp))
	assert.Equal("issue-kata-1", resp.Detail.Issue.UID)
}

func TestKataTaskDetailIssueErrorDoesNotWaitOnProjectsRead(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	release := make(chan struct{})
	daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/projects" {
			<-release
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(func() {
		close(release)
		daemon.Close()
	})
	home := t.TempDir()
	t.Setenv("KATA_HOME", home)
	writeKataProxyCatalog(t, home, `
[[daemon]]
name = "desktop"
url = "`+daemon.URL+`"
`)
	srv, _ := setupTestServer(t)

	start := time.Now()
	rr := doJSON(t, srv, http.MethodGet, "/api/v1/kata/tasks/issue-missing", nil)
	elapsed := time.Since(start)

	require.Equal(http.StatusNotFound, rr.Code, rr.Body.String())
	// Issue read outcomes return immediately; they never join the
	// best-effort projects read.
	assert.Less(elapsed, 2*time.Second)
}

func TestKataTaskDetailDoesNotFollowDaemonRedirects(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	// A daemon must not be able to bounce server-side reads to another
	// target; the redirect itself is treated as an upstream failure.
	var redirectTargetHits atomic.Int32
	redirectTarget := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		redirectTargetHits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"issue":{"uid":"issue-kata-1","project_id":7,"project_uid":"project-kata","title":"Fix widget","revision":4},"comments":[],"labels":[],"links":[]}`))
	}))
	t.Cleanup(redirectTarget.Close)
	daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, redirectTarget.URL+r.URL.Path, http.StatusFound)
	}))
	t.Cleanup(daemon.Close)
	home := t.TempDir()
	t.Setenv("KATA_HOME", home)
	writeKataProxyCatalog(t, home, `
[[daemon]]
name = "desktop"
url = "`+daemon.URL+`"
`)
	srv, _ := setupTestServer(t)

	rr := doJSON(t, srv, http.MethodGet, "/api/v1/kata/tasks/issue-kata-1", nil)

	require.Equal(http.StatusBadGateway, rr.Code, rr.Body.String())
	assert.Zero(redirectTargetHits.Load())
}

func TestKataTaskDetailUnknownIssueReturnsNotFound(t *testing.T) {
	require := require.New(t)

	daemon := startKataTaskDetailDaemon(t, `{}`, `{"projects":[]}`)
	home := t.TempDir()
	t.Setenv("KATA_HOME", home)
	writeKataProxyCatalog(t, home, `
[[daemon]]
name = "desktop"
url = "`+daemon.URL+`"
`)
	srv, _ := setupTestServer(t)

	rr := doJSON(t, srv, http.MethodGet, "/api/v1/kata/tasks/issue-missing", nil)
	require.Equal(http.StatusNotFound, rr.Code, rr.Body.String())
	// Error outcomes depend on the daemon selection just like successes, so
	// they must not be cache-shared across daemon headers.
	require.Contains(rr.Header().Values("Vary"), kataDaemonHeaderName)
}

func TestKataTaskDetailUnreachableDaemonReturnsUpstreamError(t *testing.T) {
	require := require.New(t)

	home := t.TempDir()
	t.Setenv("KATA_HOME", home)
	writeKataProxyCatalog(t, home, `
[[daemon]]
name = "desktop"
url = "http://127.0.0.1:1"
`)
	srv, _ := setupTestServer(t)

	rr := doJSON(t, srv, http.MethodGet, "/api/v1/kata/tasks/issue-kata-1", nil)
	require.Equal(http.StatusBadGateway, rr.Code, rr.Body.String())
	require.Contains(rr.Header().Values("Vary"), kataDaemonHeaderName)
}

func TestKataTaskDetailProjectsRedirectFallsBackToPayloadName(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	// Only the /projects route redirects; the unfollowed redirect must be
	// treated as a failed best-effort read that falls back to the issue
	// payload, not followed to another target and not an error.
	var redirectTargetHits atomic.Int32
	redirectTarget := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		redirectTargetHits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"projects":[{"id":7,"uid":"project-kata","name":"Kata"}]}`))
	}))
	t.Cleanup(redirectTarget.Close)
	daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/issues/issue-kata-1":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"issue":{"uid":"issue-kata-1","project_id":7,"project_uid":"project-kata","project_name":"Payload name","short_id":"task-123","title":"Fix widget","revision":4},"comments":[],"labels":[],"links":[]}`))
		case "/api/v1/projects":
			http.Redirect(w, r, redirectTarget.URL+r.URL.Path, http.StatusFound)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(daemon.Close)
	home := t.TempDir()
	t.Setenv("KATA_HOME", home)
	writeKataProxyCatalog(t, home, `
[[daemon]]
name = "desktop"
url = "`+daemon.URL+`"
`)
	// Name-based mapping against the payload name proves the fallback was
	// used: the projects listing (which the redirect withheld) is the only
	// other source of a project name.
	srv, database, _ := setupTestServerWithConfigContent(t, kataTaskDetailTestConfig(t, "[project]\nname = \"Payload name\"\n"), &mockGH{})
	_, err := database.UpsertRepo(t.Context(), db.GitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)

	rr := doJSON(t, srv, http.MethodGet, "/api/v1/kata/tasks/issue-kata-1", nil)

	require.Equal(http.StatusOK, rr.Code, rr.Body.String())
	var resp struct {
		WorkspaceTarget kataWorkspaceTargetResponse `json:"workspace_target"`
	}
	require.NoError(json.NewDecoder(rr.Body).Decode(&resp))
	assert.True(resp.WorkspaceTarget.Available)
	assert.Zero(redirectTargetHits.Load())
}

func TestKataTaskDetailSelectsDaemonFromHeader(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	daemon := startKataTaskDetailDaemon(t,
		`{"issue":{"uid":"issue-kata-1","project_id":7,"project_uid":"project-kata","short_id":"task-123","title":"Fix widget","revision":4},"comments":[],"labels":[],"links":[]}`,
		`{"projects":[{"id":7,"uid":"project-kata","name":"Kata"}]}`,
	)
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "wrong daemon", http.StatusInternalServerError)
	}))
	t.Cleanup(other.Close)
	home := t.TempDir()
	t.Setenv("KATA_HOME", home)
	writeKataProxyCatalog(t, home, `
[[daemon]]
name = "other"
url = "`+other.URL+`"

[[daemon]]
name = "work"
url = "`+daemon.URL+`"
`)
	srv, _ := setupTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/kata/tasks/issue-kata-1", nil)
	req.Host = "127.0.0.1:8091"
	req.Header.Set("X-Middleman-Kata-Daemon", "work")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	// The "other" daemon (the default) answers 500, so a 200 with the issue
	// payload proves the header routed the read to the "work" daemon.
	require.Equal(http.StatusOK, rr.Code, rr.Body.String())
	var resp struct {
		Detail struct {
			Issue struct {
				UID string `json:"uid"`
			} `json:"issue"`
		} `json:"detail"`
	}
	require.NoError(json.NewDecoder(rr.Body).Decode(&resp))
	assert.Equal("issue-kata-1", resp.Detail.Issue.UID)
}
