package fleetapi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	gitcmd "go.kenn.io/kit/git/cmd"

	"go.kenn.io/middleman/internal/config"
	"go.kenn.io/middleman/internal/db"
	"go.kenn.io/middleman/internal/server/workspaceapi"
	"go.kenn.io/middleman/internal/testutil/dbtest"
	"go.kenn.io/middleman/internal/workspace"
)

func setupTestServer(t *testing.T) (*Handler, *db.DB) {
	t.Helper()
	database := dbtest.Open(t)
	return newTestHandler(t, database, config.Fleet{}), database
}

func newTestHandlerWithWorkspaceManager(t *testing.T, database *db.DB) *Handler {
	t.Helper()
	h := newTestHandler(t, database, config.Fleet{})
	manager := workspace.NewManager(database, t.TempDir())
	h.workspaceSnapshot = workspaceSnapshotFromManager(manager)
	return h
}

func newTestHandler(t *testing.T, database *db.DB, fleetConfig config.Fleet) *Handler {
	t.Helper()
	var h *Handler
	workspaceAPI := workspaceapi.New(workspaceapi.Deps{
		DB: database,
		RecomputeWorktreeLinks: func(ctx context.Context) {
			h.RecomputeWorktreeLinks(ctx)
		},
		RefreshWorktreeStats: func(
			ctx context.Context, path, defaultBranch string,
		) error {
			return h.RefreshWorktreeStats(ctx, path, defaultBranch)
		},
		RefreshProjectInventory: func(ctx context.Context, projectID string) error {
			return h.RefreshProjectInventory(ctx, projectID)
		},
	})
	mux := http.NewServeMux()
	var local http.Handler
	h = New(Deps{
		DB: database,
		Config: ConfigSnapshot{
			Fleet:       fleetConfig,
			TmuxCommand: []string{"middleman-no-such-tmux"},
		},
		BasePath:          "/",
		LocalHandler:      func() http.Handler { return local },
		WorkspaceSnapshot: workspaceAPI.FleetSnapshot,
		RuntimeSnapshot:   workspaceAPI.RuntimeSnapshot,
	})
	apiConfig := huma.DefaultConfig("fleet test", "0.0.0")
	apiConfig.OpenAPIPath = ""
	apiConfig.DocsPath = ""
	apiConfig.SchemasPath = ""
	api := humago.NewWithPrefix(mux, "/api/v1", apiConfig)
	h.Register(api)
	workspaceAPI.Register(api)
	wsConfig := huma.DefaultConfig("fleet websocket test", "0.0.0")
	wsConfig.OpenAPIPath = ""
	wsConfig.DocsPath = ""
	wsConfig.SchemasPath = ""
	wsAPI := humago.NewWithPrefix(mux, "/ws/v1", wsConfig)
	h.RegisterTerminal(wsAPI)
	workspaceAPI.RegisterTerminal(wsAPI)
	local = mux
	return h
}

func doJSON(
	t *testing.T, h *Handler, method, path string, body any,
) *httptest.ResponseRecorder {
	t.Helper()
	var req *http.Request
	if body == nil {
		req = httptest.NewRequest(method, path, nil)
	} else {
		raw, err := json.Marshal(body)
		require.NoError(t, err)
		req = httptest.NewRequest(method, path, bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	h.localHandler().ServeHTTP(w, req)
	return w
}

type testEventHub struct {
	mu     sync.Mutex
	events []Event
	ch     chan Event
}

func newTestEventHub() *testEventHub { return &testEventHub{ch: make(chan Event, 16)} }

func (h *testEventHub) Broadcast(event Event) uint64 {
	h.mu.Lock()
	h.events = append(h.events, event)
	id := uint64(len(h.events))
	h.mu.Unlock()
	select {
	case h.ch <- event:
	default:
	}
	return id
}

func (h *testEventHub) count(kind string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	count := 0
	for _, event := range h.events {
		if event.Type == kind {
			count++
		}
	}
	return count
}

func workspaceSnapshotFromManager(manager interface {
	ListSummaries(context.Context) ([]db.WorkspaceSummary, error)
}) func(context.Context) (workspaceapi.FleetSnapshot, error) {
	return func(ctx context.Context) (workspaceapi.FleetSnapshot, error) {
		summaries, err := manager.ListSummaries(ctx)
		return workspaceapi.FleetSnapshot{Workspaces: summaries}, err
	}
}

func envOrDefault(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	require.NoError(t, err)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		require.NotEqual(t, dir, parent, "repository root not found")
		dir = parent
	}
}

func freeLoopbackPort(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer listener.Close()
	addr, ok := listener.Addr().(*net.TCPAddr)
	require.True(t, ok)
	return fmt.Sprint(addr.Port)
}

func containerLogs(ctx context.Context, container testcontainers.Container) string {
	logs, err := container.Logs(ctx)
	if err != nil {
		return fmt.Sprintf("failed to read fleet container logs: %v", err)
	}
	defer logs.Close()
	body, err := io.ReadAll(io.LimitReader(logs, 128*1024))
	if err != nil {
		return fmt.Sprintf("failed to read fleet container logs: %v", err)
	}
	return string(body)
}

func initLocalOnlyGitRepo(ctx context.Context, dir string) error {
	return gitcmd.New().Command(ctx, dir, "init", "-q").Run()
}

func mustMarshal(t *testing.T, value any) []byte {
	t.Helper()
	out, err := json.Marshal(value)
	require.NoError(t, err)
	return out
}

func httpDo(
	t *testing.T, server *httptest.Server, method, path string, body []byte,
) *http.Response {
	t.Helper()
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(t.Context(), method, server.URL+path, reader)
	require.NoError(t, err)
	if body != nil || method != http.MethodGet {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := server.Client().Do(req)
	require.NoError(t, err)
	return resp
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	runner := gitcmd.New().WithConfig("init.defaultBranch", "main")
	out, stderr, err := runner.Run(t.Context(), dir, nil, args...)
	require.NoError(t, err, "git %v failed: %s%s", args, out, stderr)
}

const serverRuntimeHelperMarker = "middleman-fleet-runtime-helper"

func serverRuntimeHelperCommand(mode string) []string {
	return []string{
		os.Args[0],
		"-test.run=TestServerRuntimeHelperProcess$",
		"--",
		serverRuntimeHelperMarker,
		mode,
	}
}

func TestServerRuntimeHelperProcess(t *testing.T) {
	args := os.Args
	for i, arg := range args {
		if arg == "--" {
			args = args[i+1:]
			break
		}
	}
	if len(args) < 2 || args[0] != serverRuntimeHelperMarker {
		return
	}
	switch args[1] {
	case "echo":
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err == nil {
			fmt.Print("echo:" + line)
		}
		select {}
	default:
		os.Exit(2)
	}
}

func readWebSocketBinaryUntil(
	t *testing.T,
	ctx context.Context,
	conn *websocket.Conn,
	timeout time.Duration,
	needle string,
) string {
	t.Helper()
	readCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var got strings.Builder
	for {
		typ, data, err := conn.Read(readCtx)
		require.NoError(t, err)
		if typ == websocket.MessageBinary {
			got.Write(data)
		}
		if strings.Contains(got.String(), needle) {
			return got.String()
		}
	}
}

func registerProjectForTest(
	t *testing.T, server *httptest.Server, localPath string,
) string {
	t.Helper()
	resp := httpDo(t, server, http.MethodPost, "/api/v1/projects",
		mustMarshal(t, map[string]any{"local_path": localPath}))
	defer resp.Body.Close()
	require.Equal(t, http.StatusCreated, resp.StatusCode)
	var project struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&project))
	require.NotEmpty(t, project.ID)
	return project.ID
}

func registerWorktreeForTest(
	t *testing.T,
	server *httptest.Server,
	projectID, branch, path string,
	wantStatus int,
) string {
	t.Helper()
	resp := httpDo(t, server, http.MethodPost,
		"/api/v1/projects/"+projectID+"/worktrees",
		mustMarshal(t, map[string]any{"branch": branch, "path": path}))
	defer resp.Body.Close()
	require.Equal(t, wantStatus, resp.StatusCode)
	if wantStatus < 200 || wantStatus >= 300 {
		return ""
	}
	var worktree struct {
		ID string `json:"id"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&worktree))
	require.NotEmpty(t, worktree.ID)
	return worktree.ID
}
