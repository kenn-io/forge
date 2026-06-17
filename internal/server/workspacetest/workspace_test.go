package workspacetest

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	Assert "github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/middleman/internal/apiclient/generated"
	"go.kenn.io/middleman/internal/config"
	"go.kenn.io/middleman/internal/db"
	"go.kenn.io/middleman/internal/workspace/localruntime"
)

func TestWorkspaceRuntimeTargetsE2E(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)

	fixture := setupWorkspaceServerFixture(t, nil)
	ctx := t.Context()
	ws := createReadyWorkspace(t, ctx, fixture.client)

	resp, err := fixture.client.HTTP.GetWorkspaceRuntimeWithResponse(ctx, ws.Id)
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode())
	require.NotNil(resp.JSON200)
	require.NotNil(resp.JSON200.LaunchTargets)
	require.NotNil(resp.JSON200.Sessions)
	assert.NotEmpty(*resp.JSON200.LaunchTargets)
	assert.Empty(*resp.JSON200.Sessions)
	assertWorkspaceRuntimeTarget(
		t, *resp.JSON200.LaunchTargets, "plain_shell",
	)
	assertWorkspaceRuntimeTargetAbsent(t, *resp.JSON200.LaunchTargets, "shell")
}

func TestWorkspaceRuntimeTargetsHideInternalShellTargetE2E(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)

	dir := t.TempDir()
	tmuxPath := filepath.Join(dir, "tmux-wrapper")
	require.NoError(os.WriteFile(
		tmuxPath,
		[]byte("#!/bin/sh\nexit 0\n"),
		0o755,
	))
	cfg := &config.Config{Tmux: config.Tmux{
		Command: []string{tmuxPath, "--scope", "tmux"},
	}}
	fixture := setupWorkspaceServerFixture(t, cfg)
	ctx := t.Context()
	ws := createReadyWorkspace(t, ctx, fixture.client)

	resp, err := fixture.client.HTTP.GetWorkspaceRuntimeWithResponse(ctx, ws.Id)
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode())
	require.NotNil(resp.JSON200)
	require.NotNil(resp.JSON200.LaunchTargets)

	foundPlainShell := false
	for _, target := range *resp.JSON200.LaunchTargets {
		if target.Key == string(localruntime.LaunchTargetShell) {
			require.Fail("internal shell target should not be exposed")
		}
		if target.Key == string(localruntime.LaunchTargetPlainShell) {
			foundPlainShell = true
			assert.True(target.Available)
		}
	}
	assert.True(foundPlainShell, "plain shell target should be exposed")
}

func TestWorkspaceRuntimeLaunchUnavailableTargetE2E(t *testing.T) {
	disabled := false
	cfg := &config.Config{Agents: []config.Agent{{
		Key:     "disabled",
		Label:   "Disabled",
		Enabled: &disabled,
	}}}
	fixture := setupWorkspaceServerFixture(t, cfg)
	ctx := t.Context()
	ws := createReadyWorkspace(t, ctx, fixture.client)

	resp, err := fixture.client.HTTP.LaunchWorkspaceRuntimeSessionWithResponse(
		ctx, ws.Id,
		generated.LaunchWorkspaceRuntimeSessionInputBody{
			TargetKey: "disabled",
		},
	)

	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, resp.StatusCode())
	require.Contains(t, string(resp.Body), "not available")
}

func TestWorkspaceRuntimeLaunchPlainShellCreatesRuntimeSessionE2E(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)

	fixture := setupWorkspaceServerFixture(t, nil)
	ctx := t.Context()
	ws := createReadyWorkspace(t, ctx, fixture.client)

	resp, err := fixture.client.HTTP.LaunchWorkspaceRuntimeSessionWithResponse(
		ctx, ws.Id,
		generated.LaunchWorkspaceRuntimeSessionInputBody{
			TargetKey: "plain_shell",
		},
	)
	require.NoError(err)
	require.Equal(http.StatusOK, resp.StatusCode())
	require.NotNil(resp.JSON200)
	shell := resp.JSON200
	assert.Equal("plain_shell", shell.TargetKey)
	assert.Equal(string(localruntime.LaunchTargetPlainShell), shell.Kind)
	assert.Equal(string(localruntime.SessionStatusRunning), shell.Status)

	getResp, err := fixture.client.HTTP.GetWorkspaceRuntimeWithResponse(ctx, ws.Id)
	require.NoError(err)
	require.Equal(http.StatusOK, getResp.StatusCode())
	require.NotNil(getResp.JSON200)
	require.NotNil(getResp.JSON200.Sessions)
	require.Len(*getResp.JSON200.Sessions, 1)
	assert.Equal(shell.Key, (*getResp.JSON200.Sessions)[0].Key)
}

func TestWorkspaceRuntimeAttachSpecUsesStoredTmuxSessionE2E(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)

	tmuxPath := writeWorkspaceRuntimeTmuxProbe(t, "workspace-runtime-live", 0, "")
	cfg := &config.Config{Tmux: config.Tmux{
		Command: []string{tmuxPath, "--socket", "workspace"},
	}}
	fixture := setupWorkspaceServerFixture(t, cfg)
	ctx := t.Context()
	ws := createReadyWorkspace(t, ctx, fixture.client)
	sessionKey := ws.Id + "_codex"
	require.NoError(fixture.database.UpsertWorkspaceRuntimeSession(
		ctx,
		&db.WorkspaceRuntimeSession{
			WorkspaceID: ws.Id,
			SessionKey:  sessionKey,
			TargetKey:   "codex",
			Label:       "codex",
			Kind:        "agent",
			Scope:       "session",
			TmuxSession: "workspace-runtime-live",
			CreatedAt:   time.Now().UTC(),
		},
	))

	req := httptest.NewRequest(
		http.MethodGet,
		"/api/v1/workspaces/"+ws.Id+"/runtime/sessions/"+
			sessionKey+"/attach-spec",
		nil,
	)
	req.Host = "middleman.test"
	rr := httptest.NewRecorder()
	fixture.server.ServeHTTP(rr, req)

	require.Equal(http.StatusOK, rr.Code)
	var spec struct {
		Version           int      `json:"version"`
		Kind              string   `json:"kind"`
		SessionKey        string   `json:"session_key"`
		TargetKey         string   `json:"target_key"`
		TmuxSession       string   `json:"tmux_session"`
		Command           []string `json:"command"`
		RequiresLocalHost bool     `json:"requires_local_host"`
	}
	require.NoError(json.NewDecoder(rr.Body).Decode(&spec))
	assert.Equal(1, spec.Version)
	assert.Equal("tmux", spec.Kind)
	assert.Equal(sessionKey, spec.SessionKey)
	assert.Equal("codex", spec.TargetKey)
	assert.Equal("workspace-runtime-live", spec.TmuxSession)
	assert.Equal(
		[]string{tmuxPath, "--socket", "workspace", "attach-session", "-t", "workspace-runtime-live"},
		spec.Command,
	)
	assert.True(spec.RequiresLocalHost)
}

func writeWorkspaceRuntimeTmuxProbe(
	t *testing.T,
	expectedSession string,
	exitCode int,
	stderr string,
) string {
	t.Helper()
	script := filepath.Join(t.TempDir(), "fake-tmux")
	body := "#!/bin/sh\n" +
		"if [ \"$1\" = \"--socket\" ]; then shift 2; fi\n" +
		"if [ \"$1\" != \"has-session\" ]; then exit 0; fi\n" +
		"if [ \"$1\" != \"has-session\" ] || [ \"$2\" != \"-t\" ] || [ \"$3\" != \"" + expectedSession + "\" ]; then\n" +
		"  echo unexpected tmux argv: \"$@\" >&2\n" +
		"  exit 2\n" +
		"fi\n"
	if stderr != "" {
		body += "echo " + shellQuoteTest(stderr) + " >&2\n"
	}
	body += "exit " + strconv.Itoa(exitCode) + "\n"
	require.NoError(t, os.WriteFile(script, []byte(body), 0o755))
	return script
}

func shellQuoteTest(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}
