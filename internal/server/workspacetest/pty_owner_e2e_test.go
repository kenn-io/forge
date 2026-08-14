package workspacetest

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/creack/pty/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/apiclient/generated"
	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/procutil"
	"go.kenn.io/forge/internal/ptyowner"
	"go.kenn.io/forge/internal/server"
	"go.kenn.io/forge/internal/workspace"
	"go.kenn.io/forge/internal/workspace/localruntime"
)

const workspaceRuntimeHelperMarker = "kenn-forge-workspace-runtime-helper"

var rustPtyManagerBuild struct {
	once sync.Once
	path string
	out  []byte
	err  error
}

func TestWorkspaceCreatesRustPtyManagerSessionE2E(t *testing.T) {
	requirePTYAvailable(t)

	require := require.New(t)
	assert := assert.New(t)

	managerPath := buildRustPtyManagerForTest(t)
	ptyOwnerDir := longRustPtyOwnerDirForTest(t)
	setLongUnixTempDirForTest(t)
	cfg := &config.Config{
		Tmux: config.Tmux{
			Command: []string{filepath.Join(t.TempDir(), "missing-tmux")},
		},
		Shell: config.Shell{Command: rustPtyManagerShellCommandForTest()},
	}
	fixture := setupWorkspaceServerFixture(t, cfg, server.ServerOptions{
		DisableWorkspaceBackgroundMonitors: true,
		HostCheckAllowLoopbackAnyPort:      true,
		PtyOwnerDir:                        ptyOwnerDir,
		PtyOwnerManagerPath:                managerPath,
	})
	ctx := t.Context()
	ws := createReadyWorkspace(t, ctx, fixture.client)
	cleanupPtyOwnerWorkspace(t, ptyOwnerDir, ws.TmuxSession)

	stored, err := fixture.database.GetWorkspace(ctx, ws.Id)
	require.NoError(err)
	require.NotNil(stored)
	assert.Equal(workspace.TerminalBackendPtyOwner, stored.TerminalBackend)

	ts := httptest.NewServer(fixture.server)
	t.Cleanup(ts.Close)
	conn, _, err := workspaceTerminalDialWithQuery(
		ctx, ts.URL, ws.Id, "cols=120&rows=30",
	)
	require.NoError(err)
	defer conn.Close(websocket.StatusNormalClosure, "done")

	if runtime.GOOS == "windows" {
		workspaceTerminalConnWriteRead(
			t, ctx, conn, "echo rust-owner-one\r", "rust-owner-one",
		)
	} else {
		workspaceTerminalConnWriteRead(
			t, ctx, conn, "printf 'rust-owner-one\n'\r", "rust-owner-one",
		)
		require.NoError(conn.Write(
			ctx,
			websocket.MessageText,
			[]byte(`{"type":"resize","cols":133,"rows":37}`),
		))
		workspaceTerminalConnWriteRead(
			t, ctx, conn, "printf 'size:'; stty size\r", "size:37 133",
		)
	}

	require.NoError(conn.Close(websocket.StatusNormalClosure, "done"))
	deleteWorkspaceForPtyOwnerTest(t, ctx, fixture, ws.Id)

	_, err = os.Stat(filepath.Join(ptyOwnerDir, ws.TmuxSession))
	assert.True(os.IsNotExist(err))
}

func TestWorkspaceRuntimeLaunchesRustPtyManagerSessionE2E(t *testing.T) {
	requirePTYAvailable(t)

	require := require.New(t)
	assert := assert.New(t)

	managerPath := buildRustPtyManagerForTest(t)
	ptyOwnerDir := longRustPtyOwnerDirForTest(t)
	setLongUnixTempDirForTest(t)
	disableTmuxAgentSessions := false
	cfg := &config.Config{
		Agents: []config.Agent{{
			Key:     "helper",
			Label:   "Helper",
			Command: workspaceRuntimeHelperCommand("echo"),
		}},
		Tmux: config.Tmux{
			Command:       []string{filepath.Join(t.TempDir(), "missing-tmux")},
			AgentSessions: &disableTmuxAgentSessions,
		},
	}
	fixture := setupWorkspaceServerFixture(t, cfg, server.ServerOptions{
		DisableWorkspaceBackgroundMonitors: true,
		HostCheckAllowLoopbackAnyPort:      true,
		PtyOwnerDir:                        ptyOwnerDir,
		PtyOwnerManagerPath:                managerPath,
	})
	ctx := t.Context()
	ws := createReadyWorkspace(t, ctx, fixture.client)
	cleanupPtyOwnerWorkspace(t, ptyOwnerDir, ws.TmuxSession)

	launchResp, err := fixture.client.HTTP.LaunchWorkspaceRuntimeSessionWithResponse(
		ctx, ws.Id,
		generated.LaunchWorkspaceRuntimeSessionInputBody{TargetKey: "helper"},
	)
	require.NoError(err)
	require.Equal(http.StatusOK, launchResp.StatusCode(), string(launchResp.Body))
	require.NotNil(launchResp.JSON200)
	session := launchResp.JSON200
	cleanupPtyOwnerWorkspace(t, ptyOwnerDir, session.Key)
	assert.Equal("helper", session.TargetKey)
	assert.Equal(string(localruntime.SessionStatusRunning), session.Status)

	ts := httptest.NewServer(fixture.server)
	t.Cleanup(ts.Close)
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") +
		"/ws/v1/workspaces/" + ws.Id +
		"/runtime/sessions/" + session.Key + "/terminal?cols=80&rows=24"
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	require.NoError(err)
	defer conn.Close(websocket.StatusNormalClosure, "done")

	workspaceTerminalConnWriteRead(t, ctx, conn, "ping\r", "echo:ping")

	stopResp, err := fixture.client.HTTP.StopWorkspaceRuntimeSessionWithResponse(
		ctx, ws.Id, session.Key,
	)
	require.NoError(err)
	require.Equal(http.StatusNoContent, stopResp.StatusCode())
	deleteWorkspaceForPtyOwnerTest(t, ctx, fixture, ws.Id)
}

func TestWorkspaceRuntimeResizeOwnerFollowsLatestDeliberateClientE2E(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("stty-based terminal size probe requires a Unix PTY")
	}
	requirePTYAvailable(t)
	require := require.New(t)

	managerPath := buildRustPtyManagerForTest(t)
	ptyOwnerDir := longRustPtyOwnerDirForTest(t)
	setLongUnixTempDirForTest(t)
	disableTmuxAgentSessions := false
	cfg := &config.Config{
		Agents: []config.Agent{{
			Key:     "shell-size",
			Label:   "Shell size",
			Command: []string{"/bin/sh"},
		}},
		Tmux: config.Tmux{
			Command:       []string{filepath.Join(t.TempDir(), "missing-tmux")},
			AgentSessions: &disableTmuxAgentSessions,
		},
	}
	fixture := setupWorkspaceServerFixture(t, cfg, server.ServerOptions{
		DisableWorkspaceBackgroundMonitors: true,
		HostCheckAllowLoopbackAnyPort:      true,
		PtyOwnerDir:                        ptyOwnerDir,
		PtyOwnerManagerPath:                managerPath,
	})
	ctx := t.Context()
	ws := createReadyWorkspace(t, ctx, fixture.client)
	cleanupPtyOwnerWorkspace(t, ptyOwnerDir, ws.TmuxSession)

	launchResp, err := fixture.client.HTTP.LaunchWorkspaceRuntimeSessionWithResponse(
		ctx, ws.Id,
		generated.LaunchWorkspaceRuntimeSessionInputBody{TargetKey: "shell-size"},
	)
	require.NoError(err)
	require.Equal(http.StatusOK, launchResp.StatusCode(), string(launchResp.Body))
	require.NotNil(launchResp.JSON200)
	session := launchResp.JSON200
	cleanupPtyOwnerWorkspace(t, ptyOwnerDir, session.Key)

	ts := httptest.NewServer(fixture.server)
	t.Cleanup(ts.Close)
	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") +
		"/ws/v1/workspaces/" + ws.Id +
		"/runtime/sessions/" + session.Key + "/terminal"
	first, _, err := websocket.Dial(
		ctx, wsURL+"?cols=80&rows=24&resize_active=1", nil,
	)
	require.NoError(err)
	defer first.Close(websocket.StatusNormalClosure, "done")
	second, _, err := websocket.Dial(
		ctx, wsURL+"?cols=100&rows=30&resize_active=1", nil,
	)
	require.NoError(err)
	defer second.Close(websocket.StatusNormalClosure, "done")

	require.NoError(second.Write(
		ctx,
		websocket.MessageText,
		[]byte(`{"type":"claim_resize","cols":100,"rows":30}`),
	))
	workspaceTerminalConnWriteRead(
		t, ctx, second,
		"printf 'second-size:'; stty size\r",
		"second-size:30 100",
	)

	require.NoError(first.Write(
		ctx,
		websocket.MessageText,
		[]byte(`{"type":"resize","cols":120,"rows":40}`),
	))
	workspaceTerminalConnWriteRead(
		t, ctx, second,
		"printf 'still-second:'; stty size\r",
		"still-second:30 100",
	)

	require.NoError(first.Write(
		ctx,
		websocket.MessageText,
		[]byte(`{"type":"claim_resize","cols":120,"rows":40}`),
	))
	workspaceTerminalConnWriteRead(
		t, ctx, first,
		"printf 'first-size:'; stty size\r",
		"first-size:40 120",
	)

	stopResp, err := fixture.client.HTTP.StopWorkspaceRuntimeSessionWithResponse(
		ctx, ws.Id, session.Key,
	)
	require.NoError(err)
	require.Equal(http.StatusNoContent, stopResp.StatusCode())
	deleteWorkspaceForPtyOwnerTest(t, ctx, fixture, ws.Id)
}

func deleteWorkspaceForPtyOwnerTest(
	t *testing.T,
	ctx context.Context,
	fixture workspaceServerFixture,
	workspaceID string,
) {
	t.Helper()

	force := true
	resp, err := fixture.client.HTTP.DeleteWorkspaceWithResponse(
		ctx, workspaceID, &generated.DeleteWorkspaceParams{Force: &force},
	)
	require.NoError(t, err)
	require.Equal(t, http.StatusNoContent, resp.StatusCode())
}

func requirePTYAvailable(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		return
	}
	ptmx, tty, err := pty.Open()
	if err != nil {
		t.Skipf("pty unavailable in this test environment: %v", err)
	}
	_ = ptmx.Close()
	_ = tty.Close()
}

func rustPtyManagerShellCommandForTest() []string {
	if runtime.GOOS == "windows" {
		return workspaceRuntimeHelperCommand("echo")
	}
	return []string{"/bin/sh"}
}

func buildRustPtyManagerForTest(t *testing.T) string {
	t.Helper()

	cargo, err := exec.LookPath("cargo")
	if err != nil {
		t.Skip("cargo not available")
	}
	if err := procutil.Command(cargo, "--version").Run(); err != nil {
		t.Skipf("cargo not usable: %v", err)
	}
	rustPtyManagerBuild.once.Do(func() {
		root := repoRootForPtyOwnerTest(t)
		cmd := procutil.Command(cargo, "build", "-p", "kenn-forge-pty-manager")
		cmd.Dir = root
		rustPtyManagerBuild.out, rustPtyManagerBuild.err = cmd.CombinedOutput()
		rustPtyManagerBuild.path = filepath.Join(
			root, "target", "debug", "kenn-forge-pty-manager",
		)
		if runtime.GOOS == "windows" {
			rustPtyManagerBuild.path += ".exe"
		}
	})
	require.NoError(t, rustPtyManagerBuild.err, string(rustPtyManagerBuild.out))
	return rustPtyManagerBuild.path
}

func repoRootForPtyOwnerTest(t *testing.T) string {
	t.Helper()

	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	require.NoError(t, err)
	_, err = os.Stat(filepath.Join(root, "Cargo.toml"))
	require.NoError(t, err)
	return root
}

func longRustPtyOwnerDirForTest(t *testing.T) string {
	t.Helper()

	dir := filepath.Join(t.TempDir(), strings.Repeat("long-owner-root-", 8))
	require.NoError(t, os.MkdirAll(dir, 0o755))
	return dir
}

func setLongUnixTempDirForTest(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		return
	}
	dir := filepath.Join(t.TempDir(), strings.Repeat("long-temp-root-", 8))
	require.NoError(t, os.MkdirAll(dir, 0o755))
	t.Setenv("TMPDIR", dir)
}

func cleanupPtyOwnerWorkspace(
	t *testing.T,
	ptyOwnerDir string,
	session string,
) {
	t.Helper()
	t.Cleanup(func() {
		_ = (&ptyowner.Client{Root: ptyOwnerDir}).Stop(
			context.Background(), session,
		)
	})
}

func workspaceTerminalConnWriteRead(
	t *testing.T,
	ctx context.Context,
	conn *websocket.Conn,
	input string,
	needle string,
) {
	t.Helper()

	require.NoError(t, conn.Write(
		ctx, websocket.MessageBinary, []byte(input),
	))
	readCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	var got strings.Builder
	for {
		typ, data, readErr := conn.Read(readCtx)
		if readErr != nil {
			break
		}
		if typ != websocket.MessageBinary {
			continue
		}
		got.WriteString(string(data))
		if strings.Contains(got.String(), needle) {
			return
		}
	}
	require.Contains(t, got.String(), needle)
}

func workspaceTerminalDialWithQuery(
	ctx context.Context,
	serverURL string,
	workspaceID string,
	query string,
) (*websocket.Conn, *http.Response, error) {
	wsURL := "ws" + strings.TrimPrefix(serverURL, "http") +
		"/api/v1/workspaces/" + workspaceID + "/terminal"
	if query != "" {
		wsURL += "?" + query
	}
	return websocket.Dial(ctx, wsURL, nil)
}

func workspaceRuntimeHelperCommand(mode string) []string {
	return []string{
		os.Args[0],
		"-test.run=TestWorkspaceRuntimeHelperProcess",
		"--",
		workspaceRuntimeHelperMarker,
		mode,
	}
}

func TestWorkspaceRuntimeHelperProcess(t *testing.T) {
	args := os.Args
	sep := slices.Index(args, "--")
	if sep < 0 || len(args) <= sep+2 || args[sep+1] != workspaceRuntimeHelperMarker {
		return
	}
	switch args[sep+2] {
	case "echo":
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err == nil {
			fmt.Print("echo:" + line)
		}
		for {
			time.Sleep(time.Hour)
		}
	default:
		require.Failf(t, "unknown helper mode", "mode %q", args[sep+2])
	}
}
