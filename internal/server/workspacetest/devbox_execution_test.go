package workspacetest

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/kballard/go-shellquote"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/devbox"
	"go.kenn.io/forge/internal/fleet"
	"go.kenn.io/forge/internal/gitclone"
	"go.kenn.io/forge/internal/procutil"
	"go.kenn.io/forge/internal/server"
	"go.kenn.io/forge/internal/server/authapi"
	"go.kenn.io/forge/internal/server/workspaceapi"
	"go.kenn.io/forge/internal/testutil/dbtest"
	"go.kenn.io/forge/internal/testutil/gitfixture"
	"go.kenn.io/forge/internal/workspace/localruntime"
)

func TestControllerDevboxCreatesCommitsPushesAndReattachesAfterRestart(t *testing.T) {
	const repositoryNodeID = "R_kgDOExample"
	if len(workspaceTestTmuxCommand) == 0 {
		t.Skip("tmux is required")
	}
	assert, require := assert.New(t), require.New(t)
	ctx := t.Context()
	directory := t.TempDir()
	socket := filepath.Join(directory, "broker.sock")
	listener, err := (&net.ListenConfig{}).Listen(ctx, "unix", socket)
	require.NoError(err)
	broker := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request devbox.CredentialRequest
		if !assert.NoError(json.UnmarshalRead(r.Body, &request)) {
			return
		}
		assert.Equal("example-org/project", request.Repository)
		assert.NoError(json.MarshalWrite(w, devbox.Credential{Token: "fixture-token", ExpiresAt: time.Now().Add(time.Hour), Writable: true, GitHubUserID: 1234, RepositoryID: 42, RepositoryNodeID: repositoryNodeID, DefaultBranch: "main"}))
	})}
	go func() { _ = broker.Serve(listener) }()
	t.Cleanup(func() { _ = broker.Close() })
	credentials := devbox.NewBrokerClient(socket)
	t.Cleanup(credentials.Close)
	clones := gitclone.New(filepath.Join(directory, "clones"), credentials)
	bare, err := clones.ClonePathForContext(gitclone.WithRepositoryIdentity(ctx, repositoryNodeID), "github", "github.com", "example-org", "project")
	require.NoError(err)
	work := gitfixture.DivergenceWorktree(t)
	remote := filepath.Join(filepath.Dir(work), "remote.git")
	gitfixture.Run(t, directory, "clone", "--bare", remote, bare)
	gitfixture.Run(t, bare, "remote", "set-url", "origin", "https://github.com/example-org/project.git")
	// Run real Git against the fixture remote without putting a URL rewrite
	// into the managed repository, whose executable config guard rejects it.
	realGit, err := exec.LookPath("git")
	require.NoError(err)
	bin := filepath.Join(directory, "bin")
	require.NoError(os.Mkdir(bin, 0o700))
	require.NoError(os.WriteFile(filepath.Join(bin, "git"), []byte("#!/bin/sh\nexec "+shellquote.Join(realGit, "-c", "url."+remote+".insteadOf=https://github.com/example-org/project.git")+" \"$@\"\n"), 0o700))
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	database := dbtest.Open(t)
	cfg := &config.Config{
		DataDir: directory, Host: "127.0.0.1", Port: 8091, BasePath: "/",
		ExecutionWorker: config.ExecutionWorker{Enabled: true, UID: 1001, GitHubUserID: 1234, BrokerSocket: socket, CommitName: "Developer A", CommitEmail: "1234+developer-a@users.noreply.github.com"},
		Tmux:            config.Tmux{Command: workspaceTestTmuxCommand},
		Agents:          []config.Agent{{Key: "fixture", Label: "Fixture shell", Command: []string{"/bin/bash", "--noprofile", "--norc"}}},
	}
	opts := server.ServerOptions{ExecutionWorker: true, FederationSpokeID: "0123456789abcdef0123456789abcdef", Clones: clones, WorktreeDir: filepath.Join(directory, "worktrees"), HostCheckAllowLoopbackAnyPort: true,
		DaemonAccess: authapi.DaemonAccessOptions{Token: "worker-bearer", RequireAPIAuth: true}, DisableWorkspaceBackgroundMonitors: true, DetachRuntimeSessionsForRestart: true, PtyOwnerInProcess: true,
	}
	srv := server.New(database, nil, nil, "/", cfg, opts)
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { srv.ServeHTTP(w, r) }))
	controllerDir := t.TempDir()
	assignment := devbox.Assignment{
		NodeID: opts.FederationSpokeID, UID: 1001, GitHubUserID: 1234, Role: "execution", Protocol: 1,
		HostID: "compute-a", Name: "Compute A", Account: "developer-a", URL: httpServer.URL,
	}
	profile := devbox.Profile{Assignment: assignment, RegistryID: "example-registry", Token: "worker-bearer"}
	// Persisted connection fixture stands in for the separately exercised registry handshake.
	saved, err := json.Marshal([]any{map[string]any{"id": "fixture", "registry_url": "https://registry.example.com", "profile": profile}})
	require.NoError(err)
	require.NoError(os.WriteFile(filepath.Join(controllerDir, "devbox-connections.json"), saved, 0o600))
	connections, err := devbox.OpenConnections(controllerDir)
	require.NoError(err)
	t.Cleanup(connections.Close)
	controllerDB := dbtest.Open(t)
	identity := db.GitHubRepoIdentity("github.com", "example-org", "project")
	identity.PlatformRepoID = repositoryNodeID
	_, err = controllerDB.UpsertRepo(ctx, identity)
	require.NoError(err)
	controller := server.New(controllerDB, nil, nil, "/", &config.Config{DataDir: controllerDir, Host: "127.0.0.1", Port: 8092, BasePath: "/", Tmux: config.Tmux{Command: workspaceTestTmuxCommand}}, server.ServerOptions{
		Devboxes: connections, HostCheckAllowLoopbackAnyPort: true, DisableWorkspaceBackgroundMonitors: true,
		DaemonAccess: authapi.DaemonAccessOptions{Token: "controller-bearer", RequireAPIAuth: true},
	})
	controllerHTTP := httptest.NewServer(controller)
	t.Cleanup(func() { controllerHTTP.Close(); assert.NoError(controller.Shutdown(context.Background())) })
	t.Cleanup(func() { httpServer.Close(); assert.NoError(srv.Shutdown(context.Background())) })
	request := func(method, path string, body any, status int, result any) {
		t.Helper()
		payload, err := json.Marshal(body)
		require.NoError(err)
		req, err := http.NewRequestWithContext(ctx, method, controllerHTTP.URL+"/api/v1/devboxes/fixture"+path, bytes.NewReader(payload))
		require.NoError(err)
		req.Header.Set("Authorization", "Bearer controller-bearer")
		req.Header.Set("Content-Type", "application/json")
		response, err := httpServer.Client().Do(req)
		require.NoError(err)
		defer response.Body.Close()
		raw, err := io.ReadAll(response.Body)
		require.NoError(err)
		require.Equal(status, response.StatusCode, string(raw))
		if result != nil {
			require.NoError(json.Unmarshal(raw, result))
		}
	}
	var ws workspaceapi.WorkspaceResponse
	request("POST", "/workspaces", map[string]string{"provider": "github", "platform_host": "github.com", "owner": "example-org", "name": "project", "branch": "work/devbox-test"}, 200, &ws)
	require.Eventually(func() bool {
		request("GET", "/workspaces/"+ws.ID, nil, 200, &ws)
		return ws.Status == "ready" || ws.Status == "error"
	}, 10*time.Second, 20*time.Millisecond)
	require.Equal("ready", ws.Status, ws.ErrorMessage)
	getSnapshot, err := http.NewRequestWithContext(ctx, "GET", controllerHTTP.URL+"/api/v1/snapshot?include_peers=true", nil)
	require.NoError(err)
	getSnapshot.Header.Set("Authorization", "Bearer controller-bearer")
	snapshotResponse, err := controllerHTTP.Client().Do(getSnapshot)
	require.NoError(err)
	var snapshot fleet.Snapshot
	require.NoError(json.UnmarshalRead(snapshotResponse.Body, &snapshot))
	require.NoError(snapshotResponse.Body.Close())
	var found bool
	for _, host := range snapshot.Hosts {
		if host.ConfigKey == "devbox:fixture" {
			found = true
			assert.True(host.Reachable)
			assert.Equal(fleet.RoleDevbox, host.FederationRole)
		}
	}
	require.True(found, "controller snapshot must expose the connected devbox")
	require.Len(snapshot.Workspaces, 1)
	assert.Equal(ws.ID, snapshot.Workspaces[0].ID)
	var session localruntime.SessionInfo
	request("POST", "/workspaces/"+ws.ID+"/runtime/sessions", map[string]string{"target_key": "fixture"}, 200, &session)
	attach := func() *websocket.Conn {
		t.Helper()
		url := "ws" + strings.TrimPrefix(controllerHTTP.URL, "http") + "/ws/v1/devboxes/fixture/workspaces/" + ws.ID + "/runtime/sessions/" + session.Key + "/terminal?cols=100&rows=30"
		conn, _, err := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": {"Bearer controller-bearer"}}})
		require.NoError(err)
		return conn
	}
	conn := attach()
	workspaceTerminalConnWriteRead(t, ctx, conn, "printf 'created in a remote session\\n' > devbox.txt; git add devbox.txt; git commit -m 'devbox test'; printf 'COMMIT-%s\\n' DONE\r", "COMMIT-DONE")
	_ = conn.Close(websocket.StatusNormalClosure, "disconnect")
	request("POST", "/workspaces/"+ws.ID+"/push", nil, 200, &ws)
	assert.Equal(gitfixture.SHA(t, ws.WorktreePath, "HEAD"), gitfixture.SHA(t, remote, "refs/heads/work/devbox-test"))
	assert.Contains(string(gitfixture.Run(t, remote, "show", "-s", "--format=%ae|%ce", "refs/heads/work/devbox-test")), "1234+developer-a@users.noreply.github.com|1234+developer-a@users.noreply.github.com")
	var detail, refreshed workspaceapi.WorkspaceResponse
	request("GET", "/workspaces/"+ws.ID, nil, 200, &detail)
	require.NotNil(detail.PushState)
	require.True(detail.PushState.Pushed)
	require.NotNil(detail.CommitAttribution)
	assert.Equal("unverified", detail.CommitAttribution.Status)
	request("POST", "/workspaces/"+ws.ID+"/refresh", nil, 200, &refreshed)
	assert.Equal(detail.PushState, refreshed.PushState)
	assert.Equal(detail.CommitAttribution, refreshed.CommitAttribution)
	require.NoError(srv.Shutdown(context.Background()))
	srv = server.New(database, nil, nil, "/", cfg, opts)
	conn = attach()
	workspaceTerminalConnWriteRead(t, ctx, conn, "printf 'REATTACHED-%s\\n' OK\r", "REATTACHED-OK")
	_ = conn.Close(websocket.StatusNormalClosure, "done")
	request("DELETE", "/workspaces/"+ws.ID+"/runtime/sessions/"+session.Key, nil, 204, nil)
	// Only the fixture's dedicated tmux server is touched.
	_ = procutil.CommandContext(ctx, workspaceTestTmuxCommand[0], append(workspaceTestTmuxCommand[1:], "kill-session", "-t", ws.TmuxSession)...).Run()
}
