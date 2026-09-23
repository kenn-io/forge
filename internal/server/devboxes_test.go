package server

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/devbox"
	"go.kenn.io/forge/internal/fleet"
	"go.kenn.io/forge/internal/server/httpapi"
	"go.kenn.io/forge/internal/server/syncevents"
	"go.kenn.io/forge/internal/server/workspaceapi"
	"go.kenn.io/forge/internal/terminalwebsocket"
	"go.kenn.io/forge/internal/testutil"
	"go.kenn.io/forge/internal/testutil/dbtest"
)

func TestDevboxCreationFollowsCachedRepositoryRename(t *testing.T) {
	assert, require := assert.New(t), require.New(t)
	database := dbtest.Open(t)
	seedPR(t, database, "acme", "widget", 7)
	platformRepoID := testutil.FixtureRepoID("acme", "widget")
	entry, err := database.ObserveRepository(t.Context(), db.RepoIdentity{
		Platform: "github", PlatformHost: "github.com", PlatformRepoID: platformRepoID, Owner: "acme", Name: "widgets",
	})
	require.NoError(err)
	require.NoError(database.UpdateRepoProviderObservation(t.Context(), entry.Repository.ID, db.RepoProviderMetadata{
		CloneURL: "https://github.com/acme/widgets.git", DefaultBranch: "main",
	}, nil, nil))
	seedIssueForRepo(t, database, entry.Repository.ID, "github.com", "acme", "widgets", 7, "open", "Update project")
	var creations, contextRefreshes atomic.Int32
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /api/v1/worker":
			assert.NoError(json.MarshalWrite(w, devbox.WorkerIdentity{}))
		case "POST /api/v1/worker/workspaces":
			var request workspaceapi.WorkerCreateRequest
			if !assert.NoError(json.UnmarshalRead(r.Body, &request)) {
				return
			}
			assert.Equal(platformRepoID, request.Repository.PlatformRepoID)
			assert.Equal("widgets", request.Repository.Name)
			assert.Equal("https://github.com/acme/widgets.git", request.Repository.CloneURL)
			creations.Add(1)
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"id":"original-workspace"}`))
		case "GET /api/v1/workspaces/original-workspace":
			_, _ = fmt.Fprintf(w, `{"id":"original-workspace","repo":{"provider":"github","platform_repo_id":%d},"platform_host":"github.com","repo_owner":"acme","repo_name":"widget","item_type":"pull_request","item_number":7,"item_key":"7","git_head_ref":"feature"}`, platformRepoID)
		case "PUT /api/v1/worker/workspaces/original-workspace/context":
			var spec db.WorkspaceLaunchSpec
			if !assert.NoError(json.UnmarshalRead(r.Body, &spec)) {
				return
			}
			assert.Equal(platformRepoID, spec.Repository.PlatformRepoID)
			assert.Equal("widgets", spec.Repository.Name)
			contextRefreshes.Add(1)
			w.WriteHeader(http.StatusNoContent)
		case "POST /api/v1/workspaces/original-workspace/runtime/sessions":
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{"key":"agent-session"}`))
		default:
			assert.Fail("unexpected worker request", "%s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(worker.Close)
	directory := t.TempDir()
	raw, err := json.Marshal([]any{map[string]any{
		"id": "compute-a", "profile": devbox.Profile{Assignment: devbox.Assignment{URL: worker.URL}, Token: "worker-test-token"},
	}})
	require.NoError(err)
	require.NoError(os.WriteFile(filepath.Join(directory, "devbox-connections.json"), raw, 0o600))
	connections, err := devbox.OpenConnections(directory)
	require.NoError(err)
	t.Cleanup(connections.Close)
	controller := New(database, nil, nil, "/", nil, ServerOptions{Devboxes: connections, DisableWorkspaceBackgroundMonitors: true})
	t.Cleanup(func() { gracefulShutdown(t, controller) })
	for _, itemField := range []string{"branch", "mr_number", "issue_number"} {
		body := map[string]any{"provider": "github", "platform_host": "github.com", "owner": "acme", "name": "widget", "platform_repo_id": platformRepoID, itemField: 7}
		if itemField == "branch" {
			body[itemField] = "work/cached-rename"
		}
		response := testutil.DoJSON(t, controller, http.MethodPost, "/api/v1/devboxes/compute-a/workspaces", body)
		assert.Equal(http.StatusOK, response.Code, "%s: %s", itemField, response.Body.String())
	}
	assert.Equal(int32(3), creations.Load())
	_, err = database.ObserveRepository(t.Context(), db.RepoIdentity{
		Platform: "github", PlatformHost: "github.com", PlatformRepoID: 1002, Owner: "acme", Name: "widget",
	})
	require.NoError(err)
	response := testutil.DoJSON(t, controller, http.MethodPost, "/api/v1/devboxes/compute-a/workspaces/original-workspace/runtime/sessions", map[string]any{
		"target_key": "codex", "display_region": "workflow",
	})
	assert.Equal(http.StatusCreated, response.Code, response.Body.String())
	assert.Equal(int32(1), contextRefreshes.Load(), "existing workspace context must follow its ID even after route reuse")
}

func TestDevboxCreationRejectsCachedRepositoryRouteReplacement(t *testing.T) {
	for _, itemField := range []string{"mr_number", "issue_number"} {
		t.Run(itemField, func(t *testing.T) {
			assert, require := assert.New(t), require.New(t)
			database := dbtest.Open(t)
			original := db.RepoIdentity{
				Platform: "github", PlatformHost: "github.com", PlatformRepoID: 1001,
				Owner: "example-org", Name: "project",
			}
			_, err := database.ObserveRepository(t.Context(), original)
			require.NoError(err)
			replacement := original
			replacement.PlatformRepoID = 1002
			entry, err := database.ObserveRepository(t.Context(), replacement)
			require.NoError(err)
			if itemField == "mr_number" {
				seedPRForRepo(t, database, entry.Repository.ID, "github.com", "example-org", "project", 7)
			} else {
				seedIssueForRepo(t, database, entry.Repository.ID, "github.com", "example-org", "project", 7, "open", "Update project")
			}

			var creations atomic.Int32
			worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal("Bearer worker-test-token", r.Header.Get("Authorization"))
				switch r.Method + " " + r.URL.Path {
				case "GET /api/v1/worker":
					assert.NoError(json.MarshalWrite(w, devbox.WorkerIdentity{}))
				case "POST /api/v1/worker/workspaces":
					creations.Add(1)
					w.WriteHeader(http.StatusAccepted)
					_, _ = w.Write([]byte(`{"id":"replacement-workspace"}`))
				default:
					assert.Fail("unexpected worker request", "%s %s", r.Method, r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			t.Cleanup(worker.Close)
			directory := t.TempDir()
			raw, err := json.Marshal([]any{map[string]any{
				"id": "compute-a", "profile": devbox.Profile{
					Assignment: devbox.Assignment{URL: worker.URL}, Token: "worker-test-token",
				},
			}})
			require.NoError(err)
			require.NoError(os.WriteFile(filepath.Join(directory, "devbox-connections.json"), raw, 0o600))
			connections, err := devbox.OpenConnections(directory)
			require.NoError(err)
			t.Cleanup(connections.Close)
			controller := &Server{
				options: ServerOptions{Devboxes: connections}, db: database, now: time.Now, hub: syncevents.NewEventHub(),
				repoResolver: httpapi.NewRepositoryResolver(httpapi.RepositoryResolverDeps{DB: database}),
			}
			mux := http.NewServeMux()
			controller.registerDevboxAPI(humago.New(mux, huma.DefaultConfig("test", "1")))

			// The cached selection still names the original repository, while
			// the same route and item number now belong to the replacement.
			body := fmt.Sprintf(`{"provider":"github","platform_host":"github.com","owner":"example-org","name":"project","platform_repo_id":1001,%q:7}`, itemField)
			request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/devboxes/compute-a/workspaces", strings.NewReader(body))
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			mux.ServeHTTP(response, request)
			assert.Equal(http.StatusNotFound, response.Code, response.Body.String())
			assert.Zero(creations.Load(), "route replacement must not forward workspace creation to the worker")
		})
	}
}

func TestDevboxWorkspaceViewStatePersistsOnWorker(t *testing.T) {
	require := require.New(t)
	database := dbtest.Open(t)
	require.NoError(database.InsertWorkspace(t.Context(), &db.Workspace{
		ID: "work-a", Platform: "github", PlatformHost: "github.com", RepoOwner: "acme", RepoName: "widget",
		ItemType: db.WorkspaceItemTypeIssue, ItemNumber: 1, WorktreePath: t.TempDir(), Status: "ready",
	}))
	workerMux := http.NewServeMux()
	workspaceAPI := workspaceapi.New(workspaceapi.Deps{DB: database})
	t.Cleanup(func() { require.NoError(workspaceAPI.Shutdown(context.Background())) })
	workspaceAPI.RegisterExecution(humago.NewWithPrefix(workerMux, "/api/v1", huma.DefaultConfig("worker", "1")))
	worker := httptest.NewServer(workerMux)
	t.Cleanup(worker.Close)
	directory := t.TempDir()
	raw, err := json.Marshal([]any{map[string]any{
		"id": "compute-a", "profile": devbox.Profile{
			Assignment: devbox.Assignment{URL: worker.URL}, Token: "worker-test-token",
		},
	}})
	require.NoError(err)
	require.NoError(os.WriteFile(filepath.Join(directory, "devbox-connections.json"), raw, 0o600))
	connections, err := devbox.OpenConnections(directory)
	require.NoError(err)
	t.Cleanup(connections.Close)
	controller := &Server{options: ServerOptions{Devboxes: connections}}
	mux := http.NewServeMux()
	api := humago.New(mux, huma.DefaultConfig("controller", "1"))
	workspaceAPI.RegisterExecution(api)
	controller.registerDevboxAPI(api)
	for _, method := range []string{http.MethodPut, http.MethodGet} {
		body := ""
		if method == http.MethodPut {
			body = `{"active_tab":"session:agent-a"}`
		}
		request := httptest.NewRequestWithContext(t.Context(), method, "/devboxes/compute-a/workspaces/work-a/view-state", strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, request)
		require.Equal(http.StatusOK, response.Code, response.Body.String())
		var state workspaceapi.WorkspaceViewState
		require.NoError(json.Unmarshal(response.Body.Bytes(), &state))
		assert.Equal(t, "session:agent-a", state.ActiveTab)
	}
	activeTab, err := database.GetWorkspaceActiveTab(t.Context(), "work-a")
	require.NoError(err)
	assert.Equal(t, "session:agent-a", activeTab)
}

func TestDevboxShellLaunchDoesNotRefreshSourceContext(t *testing.T) {
	for _, target := range []string{"plain_shell", "shell", "codex"} {
		t.Run(target, func(t *testing.T) {
			assert, require := assert.New(t), require.New(t)
			body := fmt.Sprintf(`{"target_key":%q,"display_region":"workflow"}`, target)
			var launches, contextReads atomic.Int32
			worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal("Bearer worker-test-token", r.Header.Get("Authorization"))
				switch r.Method + " " + r.URL.Path {
				case "GET /api/v1/workspaces/work-a":
					contextReads.Add(1)
					http.Error(w, "source context unavailable", http.StatusServiceUnavailable)
				case "POST /api/v1/workspaces/work-a/runtime/sessions":
					launches.Add(1)
					raw, err := io.ReadAll(r.Body)
					if !assert.NoError(err) {
						return
					}
					assert.Equal(body, string(raw), "forward the complete original launch request")
					w.WriteHeader(http.StatusCreated)
					_, _ = w.Write([]byte(`{"key":"shell-session"}`))
				default:
					assert.Fail("unexpected worker request", "%s %s", r.Method, r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			t.Cleanup(worker.Close)
			directory := t.TempDir()
			raw, err := json.Marshal([]any{map[string]any{
				"id": "compute-a", "profile": devbox.Profile{
					Assignment: devbox.Assignment{URL: worker.URL}, Token: "worker-test-token",
				},
			}})
			require.NoError(err)
			require.NoError(os.WriteFile(filepath.Join(directory, "devbox-connections.json"), raw, 0o600))
			connections, err := devbox.OpenConnections(directory)
			require.NoError(err)
			t.Cleanup(connections.Close)
			controller := wiredServer(&Server{options: ServerOptions{Devboxes: connections}})
			mux := http.NewServeMux()
			controller.registerDevboxAPI(humago.New(mux, huma.DefaultConfig("test", "1")))
			request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/devboxes/compute-a/workspaces/work-a/runtime/sessions", strings.NewReader(body))
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			mux.ServeHTTP(response, request)
			if target == "codex" {
				assert.Equal(http.StatusConflict, response.Code, response.Body.String())
				assert.Zero(launches.Load())
				assert.Equal(int32(1), contextReads.Load())
			} else {
				assert.Equal(http.StatusCreated, response.Code, response.Body.String())
				assert.JSONEq(`{"key":"shell-session"}`, response.Body.String())
				assert.Equal(int32(1), launches.Load())
				assert.Zero(contextReads.Load())
			}
		})
	}
}

func TestDevboxSnapshotMaintenanceBlocksCreation(t *testing.T) {
	for _, maintenance := range []bool{false, true} {
		t.Run(fmt.Sprint(maintenance), func(t *testing.T) {
			assert, require := assert.New(t), require.New(t)
			worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal("Bearer worker-test-token", r.Header.Get("Authorization"))
				assert.NoError(json.MarshalWrite(w, fleet.RawSnapshot{NodeID: "worker-a"}))
			}))
			t.Cleanup(worker.Close)
			directory := t.TempDir()
			raw, err := json.Marshal([]any{map[string]any{
				"id": "compute-a", "profile": devbox.Profile{
					Assignment: devbox.Assignment{URL: worker.URL, Maintenance: maintenance, WorkerIdentity: devbox.WorkerIdentity{NodeID: "worker-a"}},
					Token:      "worker-test-token",
				},
			}})
			require.NoError(err)
			require.NoError(os.WriteFile(filepath.Join(directory, "devbox-connections.json"), raw, 0o600))
			connections, err := devbox.OpenConnections(directory)
			require.NoError(err)
			t.Cleanup(connections.Close)
			controller := wiredServer(&Server{options: ServerOptions{Devboxes: connections}})
			local := fleet.RawSnapshot{NodeID: "controller"}
			aggregate := fleet.BuildNeutralAggregate(local, controller.devboxSnapshots(t.Context(), time.Second))
			snapshot := fleet.ProjectForObserver(aggregate, local, fleet.Observer{NodeID: local.NodeID, Role: fleet.RoleHub})
			require.Len(snapshot.Hosts, 2)
			host := snapshot.Hosts[1]
			assert.True(host.Reachable)
			assert.Equal(!maintenance, host.OperationAvailability[fleet.OpWorkspaceWrite].Available)
			assert.True(host.OperationAvailability[fleet.OpWorkspaceRead].Available)
			if maintenance {
				require.NotNil(host.OperationAvailability[fleet.OpWorkspaceWrite].UnavailableReason)
				assert.Contains(*host.OperationAvailability[fleet.OpWorkspaceWrite].UnavailableReason, "maintenance")
			}
		})
	}
}

func TestDevboxTerminalsBypassDefaultHTTPProxy(t *testing.T) {
	assert, require := assert.New(t), require.New(t)
	var proxyRequests atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		proxyRequests.Add(1)
		w.WriteHeader(http.StatusBadGateway)
	}))
	t.Cleanup(proxy.Close)
	proxyURL, err := url.Parse(proxy.URL)
	require.NoError(err)
	// Exercise real proxy routing without Go's cached environment lookup or its
	// loopback exception; all fixture listeners remain on loopback.
	defaultTransport := http.DefaultTransport
	proxied := &http.Transport{Proxy: http.ProxyURL(proxyURL)}
	http.DefaultTransport = proxied
	t.Cleanup(func() { http.DefaultTransport = defaultTransport; proxied.CloseIdleConnections() })
	exchanges := []struct{ command, state string }{
		{`{"type":"config","id":"model","value":"deep"}`, `{"connected":true,"busy":false,"configuring":false,"error":"","configOptions":[{"id":"model","currentValue":"deep"}],"permissions":[],"messages":[]}`},
		{`{"type":"prompt","id":"next-turn","text":"inspect"}`, `{"connected":true,"busy":true,"permissions":[{"id":"approval","title":"Edit file","options":[{"optionId":"allow","name":"Allow once","kind":"allow_once"}]}],"messages":[{"role":"assistant","text":"` + strings.Repeat("reply", 40<<10) + `"}]}`},
		{`{"type":"permission","id":"approval","optionId":"allow"}`, `{"connected":true,"busy":false,"permissions":[],"messages":[{"role":"assistant","text":"Permission received"}]}`},
		{`{"type":"cancel"}`, `{"connected":true,"busy":false,"permissions":[],"messages":[]}`},
	}
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal("Bearer worker-test-token", r.Header.Get("Authorization"))
		conn, err := terminalwebsocket.Accept(w, r)
		if !assert.NoError(err) {
			return
		}
		defer conn.CloseNow()
		conn.SetReadLimit(terminalwebsocket.ACPReadLimit)
		assert.NoError(conn.Write(r.Context(), websocket.MessageText, []byte(r.URL.RequestURI())))
		typ, message, err := conn.Read(r.Context())
		if err == nil {
			assert.NoError(conn.Write(r.Context(), typ, message))
			for _, exchange := range exchanges {
				kind, command, err := conn.Read(r.Context())
				if !assert.NoError(err) {
					return
				}
				assert.Equal(websocket.MessageText, kind)
				assert.JSONEq(exchange.command, string(command))
				if !assert.NoError(conn.Write(r.Context(), websocket.MessageText, []byte(exchange.state))) {
					return
				}
			}
			_, _, _ = conn.Read(r.Context())
		}
	}))
	t.Cleanup(worker.Close)
	directory := t.TempDir()
	raw, err := json.Marshal([]any{map[string]any{
		"id": "compute-a", "profile": devbox.Profile{
			Assignment: devbox.Assignment{URL: worker.URL}, Token: "worker-test-token",
		},
	}})
	require.NoError(err)
	require.NoError(os.WriteFile(filepath.Join(directory, "devbox-connections.json"), raw, 0o600))
	connections, err := devbox.OpenConnections(directory)
	require.NoError(err)
	t.Cleanup(connections.Close)
	controller := wiredServer(&Server{options: ServerOptions{Devboxes: connections}})
	mux := http.NewServeMux()
	controller.registerDevboxTerminalAPI(humago.NewWithPrefix(mux, "/ws/v1", huma.DefaultConfig("test", "1")))
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	direct := &http.Transport{}
	t.Cleanup(direct.CloseIdleConnections)
	for _, path := range []string{"/workspaces/work-a/terminal", "/workspaces/work-a/runtime/sessions/session-a/terminal"} {
		conn, _, err := terminalwebsocket.Dial(t.Context(), "ws"+strings.TrimPrefix(server.URL, "http")+"/ws/v1/devboxes/compute-a"+path+"?protocol=acp", nil, &http.Client{Transport: direct})
		require.NoError(err)
		_, message, err := conn.Read(t.Context())
		require.NoError(err)
		assert.Equal("/ws/v1"+path+"?protocol=acp", string(message))
		prompt := `{"type":"prompt","id":"submission-1","text":"` + strings.Repeat(`\u0000`, 64<<10) + `"}`
		conn.SetReadLimit(terminalwebsocket.ACPReadLimit)
		require.NoError(conn.Write(t.Context(), websocket.MessageText, []byte(prompt)))
		typ, message, err := conn.Read(t.Context())
		require.NoError(err)
		assert.Equal(websocket.MessageText, typ)
		assert.JSONEq(prompt, string(message))
		for _, exchange := range exchanges {
			require.NoError(conn.Write(t.Context(), websocket.MessageText, []byte(exchange.command)))
			kind, state, err := conn.Read(t.Context())
			require.NoError(err)
			assert.Equal(websocket.MessageText, kind)
			assert.JSONEq(exchange.state, string(state))
		}
		require.NoError(conn.Close(websocket.StatusNormalClosure, "done"))
	}
	assert.Zero(proxyRequests.Load(), "worker credentials and terminal traffic must bypass the default proxy")
}
