package main

import (
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/forge/internal/procutil"
	"go.kenn.io/forge/internal/runtimelock"
)

// TestDaemonServesMCPEndpointThroughLifecycleE2E covers the daemon-owned MCP
// boundary end to end: enabling [mcp] publishes the listener address through
// runtime discovery, the endpoint swaps from startup-unavailable to serving a
// real tool call, and daemon shutdown releases both listeners.
func TestDaemonServesMCPEndpointThroughLifecycleE2E(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	bin := buildForge(t)

	root := t.TempDir()
	dataDir := filepath.Join(root, "data")
	require.NoError(os.MkdirAll(dataDir, 0o700))
	cfgPath := filepath.Join(root, "config.toml")
	appPort, mcpPort := reserveAdjacentPorts(t)
	writeMinimalConfig(t, cfgPath, dataDir, appPort)
	existing, err := os.ReadFile(cfgPath)
	require.NoError(err)
	require.NoError(os.WriteFile(
		cfgPath, append(existing, []byte("\n[mcp]\nenabled = true\n")...), 0o600,
	))

	serve := procutil.Command(bin, "serve", "--config", cfgPath)
	serve.Stdout = os.Stderr
	serve.Stderr = os.Stderr
	serve.Env = append(os.Environ(),
		"KENN_FORGE_LOG_LEVEL=warn",
		"KENN_FORGE_GITHUB_TOKEN_UNSET_FOR_LOCK_E2E=",
	)
	require.NoError(serve.Start())
	stopped := false
	t.Cleanup(func() {
		if !stopped && serve.Process != nil {
			_ = serve.Process.Signal(syscall.SIGKILL)
			_ = serve.Wait()
		}
	})

	waitForFile(t, runtimelock.MetadataPath(dataDir), 10*time.Second)
	status, err := runtimelock.Read(dataDir)
	require.NoError(err)
	require.NotNil(status.Metadata)
	mcpAddr := status.Metadata.MCPListenAddr
	assert.Equal(net.JoinHostPort("127.0.0.1", strconv.Itoa(mcpPort)), mcpAddr)

	// The published endpoint returns 503 until the full server swaps in;
	// a session eventually initializes against the discovered address.
	client := mcp.NewClient(
		&mcp.Implementation{Name: "lifecycle-test", Version: "test"}, nil,
	)
	var session *mcp.ClientSession
	require.Eventually(func() bool {
		cs, connectErr := client.Connect(
			t.Context(),
			&mcp.StreamableClientTransport{Endpoint: "http://" + mcpAddr + "/mcp"},
			nil,
		)
		if connectErr != nil {
			return false
		}
		session = cs
		return true
	}, 30*time.Second, 100*time.Millisecond)

	result, err := session.CallTool(
		t.Context(), &mcp.CallToolParams{Name: "kenn_forge_list_repos"},
	)
	require.NoError(err)
	require.NotNil(result)
	assert.False(result.IsError)

	quickstart := procutil.Command(
		bin, "mcp", "quickstart", "--config", cfgPath, "--json",
	)
	quickstartOutput, err := quickstart.Output()
	require.NoError(err)
	var connection mcpQuickstartInfo
	require.NoError(json.Unmarshal(quickstartOutput, &connection))
	assert.True(connection.Active)
	assert.Equal("http://"+mcpAddr+"/mcp", connection.Endpoint)
	require.NotNil(connection.ClientConfig)
	assert.Equal(
		connection.Endpoint,
		connection.ClientConfig.MCPServers["kenn-forge"].URL,
	)
	require.NoError(session.Close())

	require.NoError(serve.Process.Signal(syscall.SIGTERM))
	require.NoError(serve.Wait())
	stopped = true

	for _, port := range []int{appPort, mcpPort} {
		addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
		assert.Eventually(func() bool {
			ln, listenErr := net.Listen("tcp", addr)
			if listenErr != nil {
				return false
			}
			return ln.Close() == nil
		}, 5*time.Second, 50*time.Millisecond, "listener %s not released", addr)
	}
}
