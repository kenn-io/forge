package localruntime

import (
	"context"
	"encoding/json/v2"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/config"
)

func TestACPMCPRebindsAfterDaemonRestart(t *testing.T) {
	t.Setenv("KENN_FORGE_LOCALRUNTIME_HELPER", "1")
	t.Setenv("KENN_FORGE_ACP_FIXTURE", "1")
	dir := t.TempDir()
	t.Setenv("KENN_FORGE_ACP_CONTINUE_DIR", dir)
	executable, err := os.Executable()
	require.NoError(t, err)
	firstUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "workspace", r.Header.Get("X-Kenn-Forge-Workspace-ID"))
		assert.Empty(t, r.Header.Get("Origin"), "proxy origin must not reach the daemon guard")
		if r.URL.Path != "/agent-mcp" || r.Header.Get("Authorization") != "Bearer first-token" {
			http.Error(w, "wrong binding", http.StatusUnauthorized)
			return
		}
		_, _ = io.WriteString(w, "first daemon")
	}))
	t.Cleanup(firstUpstream.Close)
	options := withTestPtyOwnerRuntime(t, Options{ACPSessionsDir: filepath.Join(dir, "acp"), AgentMCPURL: firstUpstream.URL + "/agent-mcp", AgentMCPToken: "first-token", Targets: ResolveLaunchTargets([]config.Agent{{Key: "chat", Protocol: "acp", Command: []string{executable, "-test.run=^TestACPStdioHelper$"}}}, nil, nil)})
	first := newACPTestManager(t, options)
	info, err := first.Launch(t.Context(), "workspace", dir, "chat")
	require.NoError(t, err)
	// Read the actual session/new payload received by the agent. The request
	// below follows that supplied URL, not a reconstructed proxy address.
	data, err := os.ReadFile(filepath.Join(dir, "mcp.json"))
	require.NoError(t, err)
	var servers []struct {
		URL     string `json:"url"`
		Headers []struct {
			Name  string `json:"name"`
			Value string `json:"value"`
		} `json:"headers"`
	}
	require.NoError(t, json.Unmarshal(data, &servers))
	require.Len(t, servers, 1)
	assert.NotEqual(t, firstUpstream.URL+"/agent-mcp", servers[0].URL)
	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, servers[0].URL, strings.NewReader(`{"jsonrpc":"2.0","method":"tools/list","id":1}`))
	require.NoError(t, err)
	request.Header.Set("X-Kenn-Forge-Workspace-ID", "wrong-workspace")
	request.Header.Set("Origin", request.URL.Scheme+"://"+request.URL.Host)
	for _, header := range servers[0].Headers {
		request.Header.Set(header.Name, header.Value)
	}
	response, err := http.DefaultClient.Do(request)
	require.NoError(t, err)
	data, err = io.ReadAll(response.Body)
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())
	assert.Equal(t, "first daemon", string(data))
	first.Shutdown()
	firstUpstream.Close()
	secondUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "workspace", r.Header.Get("X-Kenn-Forge-Workspace-ID"))
		assert.Empty(t, r.Header.Get("Origin"), "proxy origin must not reach the rebound daemon guard")
		if r.URL.Path != "/agent-mcp" || r.Header.Get("Authorization") != "Bearer second-token" {
			http.Error(w, "stale binding", http.StatusUnauthorized)
			return
		}
		_, _ = io.WriteString(w, "second daemon")
	}))
	defer secondUpstream.Close()
	options.AgentMCPURL, options.AgentMCPToken = secondUpstream.URL+"/agent-mcp", "second-token"
	second := newACPTestManager(t, options)
	require.NoError(t, second.RestoreRuntimeSessions(t.Context(), []RestoredRuntimeSession{{WorkspaceID: "workspace", SessionKey: info.Key, TargetKey: "chat", Kind: LaunchTargetACP, CWD: dir, CreatedAt: info.CreatedAt}}))
	request = request.Clone(context.Background())
	request.Body, err = request.GetBody()
	require.NoError(t, err)
	response, err = http.DefaultClient.Do(request)
	require.NoError(t, err)
	data, err = io.ReadAll(response.Body)
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())
	assert.Equal(t, "second daemon", string(data))
	request.Header.Set("Authorization", "Bearer wrong-token")
	request.Body, err = request.GetBody()
	require.NoError(t, err)
	response, err = http.DefaultClient.Do(request)
	require.NoError(t, err)
	require.NoError(t, response.Body.Close())
	assert.Equal(t, http.StatusUnauthorized, response.StatusCode)
}
