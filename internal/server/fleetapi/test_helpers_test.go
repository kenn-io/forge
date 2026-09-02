package fleetapi

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
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

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	gitcmd "go.kenn.io/kit/git/cmd"

	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/federation"
	"go.kenn.io/forge/internal/federationauth"
	"go.kenn.io/forge/internal/fleet"
	"go.kenn.io/forge/internal/server/workspaceapi"
	"go.kenn.io/forge/internal/testutil/dbtest"
	"go.kenn.io/forge/internal/workspace"
)

const (
	testHubNodeID    = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	testMemberNodeID = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

func configureTestMembers(
	t *testing.T,
	h *Handler,
	client *http.Client,
	members ...config.FleetMember,
) map[string]string {
	t.Helper()
	credentials, err := federationauth.Open(filepath.Join(
		t.TempDir(), "federation-credentials.json",
	))
	require.NoError(t, err)
	enrollments, err := federation.Open(
		filepath.Join(t.TempDir(), "federation-enrollments.json"),
		federation.StoreOptions{},
	)
	require.NoError(t, err)
	tokens := make(map[string]string, len(members))
	for index, member := range members {
		if member.State == "" {
			member.State = federation.EnrollmentActive
			members[index] = member
		}
		token := strings.Repeat(string(rune('a'+index)), 48)
		require.NoError(t, credentials.StoreOutbound(
			member.NodeID, token, federationauth.HubToSpokeScopes(),
		))
		enrollmentToken, createErr := enrollments.CreateOneTimeToken(
			federation.Identity{
				NodeID: testHubNodeID, BaseURL: "https://hub.example",
			},
			time.Now().Add(time.Hour),
		)
		require.NoError(t, createErr)
		enrollment, beginErr := enrollments.Begin(t.Context(), enrollmentToken.Token,
			federation.JoinRequest{
				EnrollmentID:    fmt.Sprintf("%032x", index+1),
				NodeID:          member.NodeID,
				Platform:        "linux",
				BaseURL:         member.BaseURL,
				ProtocolVersion: federation.ProtocolVersion,
				HubCredential:   token,
			},
		)
		require.NoError(t, beginErr)
		require.NoError(t, enrollments.Activate(
			t.Context(), enrollment.ID, time.Now().Add(time.Hour),
		))
		tokens[member.NodeID] = token
	}
	h.nodeID = testHubNodeID
	h.credentials = credentials
	h.enrollments = enrollments
	if client != nil {
		h.federationHTTPClient = client
	}
	h.memberClientsMu.Lock()
	h.memberClients = make(map[string]federationMemberClients)
	h.memberClientsMu.Unlock()
	snapshot := h.configSnapshot()
	snapshot.Fleet = config.Fleet{
		Enabled: true, Role: config.FleetRoleHub,
		BaseURL: "https://hub.example", Members: members,
	}
	h.ApplyConfig(snapshot)
	return tokens
}

func testTLSClient(t *testing.T, servers ...*httptest.Server) *http.Client {
	t.Helper()
	roots := x509.NewCertPool()
	for _, server := range servers {
		require.NotNil(t, server.Certificate())
		roots.AddCert(server.Certificate())
	}
	return &http.Client{Transport: &http.Transport{
		TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12},
	}}
}

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
			TmuxCommand: []string{"kenn-forge-no-such-tmux"},
		},
		BasePath:               "/",
		LocalHandler:           func() http.Handler { return local },
		WorkspaceSnapshot:      workspaceAPI.FleetSnapshot,
		WorkspaceStatsSnapshot: workspaceAPI.FleetStatsSnapshot,
		RuntimeSnapshot:        workspaceAPI.RuntimeSnapshot,
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
		if err != nil {
			return workspaceapi.FleetSnapshot{}, err
		}
		workspaces := make([]fleet.RawWorkspace, len(summaries))
		for index, summary := range summaries {
			workspaces[index] = rawWorkspaceForAdapterTest(summary)
		}
		return workspaceapi.FleetSnapshot{Workspaces: workspaces}, nil
	}
}

func rawWorkspaceForAdapterTest(summary db.WorkspaceSummary) fleet.RawWorkspace {
	backend := ""
	switch summary.TerminalBackend {
	case workspace.TerminalBackendPtyOwner:
		backend = fleet.SessionBackendLocalPTY
	case workspace.TerminalBackendTmux:
		backend = fleet.SessionBackendLocalTmux
	}
	associatedPR := summary.AssociatedPRNumber
	if !summary.AssociatedPRVisible {
		associatedPR = nil
	}
	return fleet.RawWorkspace{
		ID: summary.ID,
		Repository: fleet.RepositoryIdentity{
			Provider: summary.Platform, PlatformHost: summary.PlatformHost,
			PlatformRepoID: summary.RepoPlatformID,
			Owner:          summary.RepoOwner, Name: summary.RepoName,
		},
		ItemType: summary.ItemType, ItemNumber: summary.ItemNumber,
		SourceItemVisible: summary.SourceItemVisible,
		ItemKey:           summary.ItemKey, GitHeadRef: summary.GitHeadRef,
		WorktreePath: summary.WorktreePath, TmuxSession: summary.TmuxSession,
		SessionBackend: backend, Status: summary.Status,
		ErrorMessage:       summary.ErrorMessage,
		CreatedAt:          summary.CreatedAt.UTC().Format(time.RFC3339),
		AssociatedPRNumber: associatedPR,
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
