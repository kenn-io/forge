package main

import (
	"bufio"
	"encoding/json/v2"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/procutil"
	"go.kenn.io/forge/internal/runtimelock"
	"go.kenn.io/forge/internal/workspace/localruntime"
)

// This foreign ACP peer accepts a session only after using the injected Forge
// connection. A capability declaration alone does not prove MCP reachability.
func TestACPAgentMCPHelper(t *testing.T) {
	if os.Getenv("KENN_FORGE_AGENT_MCP_HELPER") != "1" {
		return
	}
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		var request struct {
			ID     int    `json:"id"`
			Method string `json:"method"`
			Params struct {
				MCPServers []localruntime.ACPMCPServer `json:"mcpServers"`
			} `json:"params"`
		}
		require.NoError(t, json.Unmarshal(scanner.Bytes(), &request))
		switch request.Method {
		case "initialize":
			fmt.Printf(`{"jsonrpc":"2.0","id":%d,"result":{"protocolVersion":1,"agentCapabilities":{"mcpCapabilities":{"http":true}}}}`+"\n", request.ID)
		case "session/new":
			require.Len(t, request.Params.MCPServers, 1)
			connection := request.Params.MCPServers[0]
			require.Equal(t, "kenn-forge", connection.Name)
			require.True(t, strings.HasPrefix(connection.URL, "http://127.0.0.1:"))
			call, err := http.NewRequestWithContext(t.Context(), http.MethodPost, connection.URL, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`))
			require.NoError(t, err)
			for _, header := range connection.Headers {
				call.Header.Set(header.Name, header.Value)
			}
			call.Header.Set("Content-Type", "application/json")
			call.Header.Set("Accept", "application/json, text/event-stream")
			client := &http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: 5 * time.Second}
			response, err := client.Do(call)
			require.NoError(t, err)
			body, err := io.ReadAll(response.Body)
			_ = response.Body.Close()
			client.CloseIdleConnections()
			require.NoError(t, err)
			require.Equal(t, http.StatusOK, response.StatusCode)
			require.Contains(t, string(body), "kenn_forge_list_repos")
			require.NoError(t, os.WriteFile(os.Getenv("KENN_FORGE_AGENT_MCP_ENDPOINT"), []byte(connection.URL), 0o600))
			fmt.Printf(`{"jsonrpc":"2.0","id":%d,"result":{"sessionId":"verified-local-tools"}}`+"\n", request.ID)
		}
	}
	os.Exit(0)
}

func TestAgentMCPLoopbackWithPublicListenerE2E(t *testing.T) {
	bin := buildForge(t)
	executable, err := os.Executable()
	require.NoError(t, err)
	addresses, err := net.InterfaceAddrs()
	require.NoError(t, err)
	nonLoopback := ""
	for _, address := range addresses {
		ip, _, err := net.ParseCIDR(address.String())
		if err == nil && ip.To4() != nil && !ip.IsLoopback() {
			nonLoopback = ip.String()
			break
		}
	}
	for _, tc := range []struct {
		name, host string
		proxy      bool
	}{
		{"reverse proxy", "127.0.0.1", true},
		{"exact non-loopback bind", nonLoopback, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.host == "" {
				t.Skip("no non-loopback IPv4 interface available")
			}
			root := t.TempDir()
			dataDir, configPath := filepath.Join(root, "data"), filepath.Join(root, "config.toml")
			require.NoError(t, os.MkdirAll(dataDir, 0o700))
			port := reserveFreePort(t)
			writeMinimalConfigWithBasePath(t, configPath, dataDir, port, "/forge")
			config, err := os.ReadFile(configPath)
			require.NoError(t, err)
			config = []byte(strings.Replace(string(config), `host = "127.0.0.1"`, fmt.Sprintf("host = %q", tc.host), 1))
			config = append(fmt.Appendf(nil, "trust_reverse_proxy = %t\n", tc.proxy), config...)
			require.NoError(t, os.WriteFile(configPath, append(config, []byte("\n[api]\nrequire_auth = true\n")...), 0o600))
			endpointPath := filepath.Join(root, "agent-endpoint")
			serve := procutil.Command(bin, "serve", "--config", configPath, "--disable-sync")
			serve.Stdout, serve.Stderr = os.Stderr, os.Stderr
			serve.Env = append(os.Environ(), "KENN_FORGE_AGENT_MCP_HELPER=1", "KENN_FORGE_AGENT_MCP_ENDPOINT="+endpointPath)
			require.NoError(t, serve.Start())
			stopped := false
			t.Cleanup(func() {
				if !stopped {
					_ = serve.Process.Kill()
					_ = serve.Wait()
				}
			})
			waitForFile(t, runtimelock.AuthTokenPath(dataDir), 10*time.Second)
			token, err := os.ReadFile(runtimelock.AuthTokenPath(dataDir))
			require.NoError(t, err)
			client := &http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: 10 * time.Second}
			t.Cleanup(client.CloseIdleConnections)
			url := fmt.Sprintf("http://%s/forge/api/v1/settings/agents/test-acp", net.JoinHostPort(tc.host, fmt.Sprint(port)))
			payload, err := json.Marshal(map[string]any{"command": []string{executable, "-test.run=^TestACPAgentMCPHelper$"}})
			require.NoError(t, err)
			var result struct {
				Valid bool `json:"valid"`
			}
			require.Eventually(t, func() bool {
				request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, url, strings.NewReader(string(payload)))
				if err != nil {
					return false
				}
				request.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(token)))
				request.Header.Set("Content-Type", "application/json")
				response, err := client.Do(request)
				if err != nil {
					return false
				}
				defer response.Body.Close()
				return response.StatusCode == http.StatusOK && json.UnmarshalRead(response.Body, &result) == nil && result.Valid
			}, 30*time.Second, 100*time.Millisecond)
			endpoint, err := os.ReadFile(endpointPath)
			require.NoError(t, err)
			require.NoError(t, serve.Process.Signal(syscall.SIGTERM))
			require.NoError(t, serve.Wait())
			stopped = true
			response, err := client.Get(string(endpoint))
			if response != nil {
				_ = response.Body.Close()
			}
			assert.Error(t, err, "agent listener must close with its daemon")
		})
	}
}
