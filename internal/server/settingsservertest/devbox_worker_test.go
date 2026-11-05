package settingsservertest

import (
	"bufio"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/forge/internal/config"
	forgeserver "go.kenn.io/forge/internal/server"
	"go.kenn.io/forge/internal/server/authapi"
	"go.kenn.io/forge/internal/server/devboxapi"
	"go.kenn.io/forge/internal/testutil"
	"go.kenn.io/forge/internal/testutil/dbtest"
	serverfake "go.kenn.io/forge/internal/testutil/serverfake"
)

// newExecutionWorkerServer starts an execution worker. Given agents, the
// worker also runs a workspace runtime that offers them.
func newExecutionWorkerServer(t *testing.T, agents ...config.Agent) *httptest.Server {
	t.Helper()
	cfg := &config.Config{}
	cfg.DataDir = t.TempDir()
	cfg.ExecutionWorker = config.ExecutionWorker{Enabled: true, UID: 1001, GitHubUserID: 1234, BrokerSocket: "/run/example/broker.sock"}
	options := forgeserver.ServerOptions{
		ExecutionWorker:               true,
		DaemonAccess:                  authapi.DaemonAccessOptions{Token: "worker-test-secret", RequireAPIAuth: true},
		FederationSpokeID:             "0123456789abcdef0123456789abcdef",
		HostCheckAllowLoopbackAnyPort: true,
	}
	if len(agents) > 0 {
		cfg.Agents = agents
		cfg.Tmux = config.Tmux{Command: []string{filepath.Join(t.TempDir(), "missing-tmux")}}
		options.WorktreeDir = filepath.Join(cfg.DataDir, "worktrees")
	}
	srv := forgeserver.New(dbtest.Open(t), nil, nil, "/", cfg, options)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 5*time.Second)
		defer cancel()
		assert.NoError(t, srv.Shutdown(ctx))
	})
	server := httptest.NewServer(srv)
	t.Cleanup(server.Close)
	return server
}

func TestExecutionWorkerRoutesAndBearerBoundary(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	assert := assert.New(t)
	server := newExecutionWorkerServer(t)
	for _, test := range []struct {
		name, method, path, body, bearer, cookie, userHeader string
		status                                               int
	}{
		{name: "identity", method: "GET", path: "/api/v1/worker", bearer: "worker-test-secret", status: 200},
		{name: "workspace inventory", method: "GET", path: "/api/v1/workspaces", bearer: "worker-test-secret", status: 200},
		{name: "missing bearer", method: "GET", path: "/api/v1/worker", status: 401},
		{name: "wrong account", method: "GET", path: "/api/v1/worker", bearer: "other-account", status: 401},
		{name: "forged Serve user", method: "GET", path: "/api/v1/worker", userHeader: "developer@example.org", status: 401},
		{name: "browser cookie", method: "GET", path: "/api/v1/worker", cookie: "worker-test-secret", status: 401},
		{name: "ACP options", method: "POST", path: "/api/v1/settings/agents/acp-options", body: `{"target_key":"codex"}`, bearer: "worker-test-secret", status: 503},
		{name: "ACP options without bearer", method: "POST", path: "/api/v1/settings/agents/acp-options", body: `{"target_key":"codex"}`, status: 401},
		{name: "ACP options wrong account", method: "POST", path: "/api/v1/settings/agents/acp-options", body: `{"target_key":"codex"}`, bearer: "other-account", status: 401},
		{name: "settings", method: "PATCH", path: "/api/v1/settings", bearer: "worker-test-secret", status: 404},
		{name: "provider sync", method: "POST", path: "/api/v1/sync", bearer: "worker-test-secret", status: 404},
		{name: "enrollment", method: "POST", path: "/api/v1/fleet/enrollments", bearer: "worker-test-secret", status: 404},
	} {
		t.Run(test.name, func(t *testing.T) {
			request, err := http.NewRequestWithContext(t.Context(), test.method, server.URL+test.path, strings.NewReader(test.body))
			require.NoError(t, err)
			request.Header.Set("Content-Type", "application/json")
			if test.bearer != "" {
				request.Header.Set("Authorization", "Bearer "+test.bearer)
			}
			request.Header.Set("Tailscale-User-Login", test.userHeader)
			if test.cookie != "" {
				request.AddCookie(&http.Cookie{Name: "forge_auth", Value: test.cookie})
			}
			response, err := server.Client().Do(request)
			require.NoError(t, err)
			defer response.Body.Close()
			assert.Equal(test.status, response.StatusCode)
			if test.name == "identity" {
				var identity devboxapi.WorkerIdentity
				require.NoError(t, json.UnmarshalRead(response.Body, &identity))
				assert.Equal(int64(1234), identity.GitHubUserID)
				assert.Equal(uint32(1001), identity.UID)
				assert.Equal("execution", identity.Role)
			}
		})
	}
}

func TestDaemonServesACPOptions(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	server, _, _ := setupTestServer(t)
	response := testutil.DoJSON(t, server, http.MethodPost, "/api/v1/settings/agents/acp-options", map[string]string{"target_key": "codex"})
	assert.Equal(t, http.StatusServiceUnavailable, response.Code, response.Body.String())
}

// A coordinator drives supervised chats on an execution worker with the
// worker's bearer token, and only with it.
func TestExecutionWorkerServesChatRoutes(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	server := newExecutionWorkerServer(t)
	chat := server.URL + "/api/v1/workspaces/missing/runtime/sessions/chat/chat"
	for _, test := range []struct {
		name, method, path, body, bearer string
		status                           int
	}{
		{name: "snapshot", method: "GET", path: chat, bearer: "worker-test-secret", status: 503},
		{name: "command", method: "POST", path: chat + "/commands", body: `{"type":"cancel"}`, bearer: "worker-test-secret", status: 503},
		{name: "restore", method: "POST", path: chat + "/restore", bearer: "worker-test-secret", status: 503},
		{name: "snapshot without bearer", method: "GET", path: chat, status: 401},
		{name: "command without bearer", method: "POST", path: chat + "/commands", body: `{"type":"cancel"}`, status: 401},
		{name: "restore without bearer", method: "POST", path: chat + "/restore", status: 401},
	} {
		t.Run(test.name, func(t *testing.T) {
			request, err := http.NewRequestWithContext(t.Context(), test.method, test.path, strings.NewReader(test.body))
			require.NoError(t, err)
			request.Header.Set("Content-Type", "application/json")
			if test.bearer != "" {
				request.Header.Set("Authorization", "Bearer "+test.bearer)
			}
			response, err := server.Client().Do(request)
			require.NoError(t, err)
			defer response.Body.Close()
			require.Equal(t, test.status, response.StatusCode)
			if test.bearer != "" {
				// The chat route answered; this worker runs no agents.
				var problem struct {
					Code   string `json:"code"`
					Detail string `json:"detail"`
				}
				require.NoError(t, json.UnmarshalRead(response.Body, &problem))
				assert.Equal(t, "serviceUnavailable", problem.Code)
				assert.Equal(t, "workspace runtime not configured", problem.Detail)
			}
		})
	}
}

// TestWorkerACPAgentHelper is the ACP agent the worker probe tests start. It
// offers one model option and reports the model a client sets.
func TestWorkerACPAgentHelper(t *testing.T) { //nolint:paralleltest // runs only as the agent subprocess
	if os.Getenv("KENN_FORGE_WORKER_ACP_AGENT") != "1" {
		return
	}
	model := "fast"
	reply := func(id jsontext.Value) {
		fmt.Printf(`{"jsonrpc":"2.0","id":%s,"result":{"sessionId":"example-session","configOptions":[{"id":"model","name":"Model","type":"select","currentValue":%q,"options":[{"value":"fast","name":"Fast"},{"value":"deep","name":"Deep"}]}]}}`+"\n", id, model)
	}
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		var message struct {
			ID     jsontext.Value `json:"id"`
			Method string         `json:"method"`
			Params struct {
				ConfigID string `json:"configId"`
				Value    string `json:"value"`
			} `json:"params"`
		}
		if json.Unmarshal(scanner.Bytes(), &message) != nil {
			os.Exit(2)
		}
		switch message.Method {
		case "initialize":
			fmt.Printf(`{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":1,"agentCapabilities":{}}}`+"\n", message.ID)
		case "session/new":
			reply(message.ID)
		case "session/set_config_option":
			if message.Params.ConfigID != "model" {
				os.Exit(3)
			}
			model = message.Params.Value
			reply(message.ID)
		}
	}
	os.Exit(0)
}

// A coordinator asks an execution worker which options its ACP agents offer,
// with the worker's bearer token, and only with it.
func TestExecutionWorkerProbesACPOptions(t *testing.T) {
	serverfake.RunParallelServerTest(t)
	executable, err := os.Executable()
	require.NoError(t, err)
	server := newExecutionWorkerServer(t, config.Agent{
		Key: "chat", Protocol: "acp",
		Command: []string{executable, "-test.run=^TestWorkerACPAgentHelper$"},
		Env:     map[string]string{"KENN_FORGE_SERVER_RUNTIME_HELPER": "1", "KENN_FORGE_WORKER_ACP_AGENT": "1"},
	})
	probe := server.URL + "/api/v1/settings/agents/acp-options"
	for _, test := range []struct {
		name, body, bearer, detail string
		status                     int
	}{
		{name: "configured agent", body: `{"target_key":"chat","acp_options":{"model":"deep"}}`, bearer: "worker-test-secret", status: 200},
		{name: "unconfigured agent", body: `{"target_key":"other"}`, bearer: "worker-test-secret", status: 400, detail: "target_key is not a configured ACP agent"},
		{name: "missing bearer", body: `{"target_key":"chat"}`, status: 401},
		{name: "wrong bearer", body: `{"target_key":"chat"}`, bearer: "other-account", status: 401},
	} {
		t.Run(test.name, func(t *testing.T) {
			request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, probe, strings.NewReader(test.body))
			require.NoError(t, err)
			request.Header.Set("Content-Type", "application/json")
			if test.bearer != "" {
				request.Header.Set("Authorization", "Bearer "+test.bearer)
			}
			response, err := server.Client().Do(request)
			require.NoError(t, err)
			defer response.Body.Close()
			require.Equal(t, test.status, response.StatusCode)
			switch test.status {
			case 200:
				var result struct {
					Options []struct {
						ID           string `json:"id"`
						CurrentValue string `json:"currentValue"`
					} `json:"options"`
				}
				require.NoError(t, json.UnmarshalRead(response.Body, &result))
				require.Len(t, result.Options, 1)
				assert.Equal(t, "model", result.Options[0].ID)
				assert.Equal(t, "deep", result.Options[0].CurrentValue, "the probe applies the requested option before reporting")
			case 400:
				data, err := io.ReadAll(response.Body)
				require.NoError(t, err)
				assert.Contains(t, string(data), test.detail)
			}
		})
	}
}
