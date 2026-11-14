package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/runtimelock"
)

func TestMCPQuickstartCommandPrintsAgentConnector(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	info := daemonMCPQuickstart(mcpSettingsInfo{
		Enabled:            true,
		ActiveURL:          "http://127.0.0.1:8092/mcp",
		ActiveRequiresAuth: true,
	}, "/tmp/forge/auth_token")
	var output bytes.Buffer
	cmd := newMCPCommand(&output, func(_ context.Context, _ string, _ time.Duration) (mcpQuickstartInfo, error) {
		return info, nil
	})
	cmd.SetArgs([]string{"quickstart"})

	require.NoError(cmd.Execute())

	text := output.String()
	assert.Contains(text, "OK mcp quickstart")
	assert.Contains(text, "endpoint: http://127.0.0.1:8092/mcp")
	assert.Contains(text, "token_path: /tmp/forge/auth_token")
	assert.Contains(text, `"Authorization": "Bearer ${KENN_FORGE_API_TOKEN}"`)
}

func TestMCPQuickstartStoppedGuidesSetup(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{name: "default", args: []string{"quickstart"}, want: "Run `kenn-forge agent setup` to connect"},
		{name: "custom", args: []string{"quickstart", "--config", "/tmp/custom config.toml"}, want: "kenn-forge agent setup --config '/tmp/custom config.toml'"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var output bytes.Buffer
			cmd := newMCPCommand(&output, func(_ context.Context, path string, _ time.Duration) (mcpQuickstartInfo, error) {
				return stoppedMCPQuickstart(true, path), nil
			})
			cmd.SetArgs(tc.args)
			require.NoError(t, cmd.Execute())
			assert.NotContains(t, output.String(), "OK mcp quickstart")
			assert.Contains(t, output.String(), tc.want)
		})
	}
}

func TestMCPQuickstartCommandPrintsStructuredConnector(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	info := daemonMCPQuickstart(mcpSettingsInfo{
		Enabled:   true,
		ActiveURL: "http://127.0.0.1:8092/mcp",
	}, "")
	var output bytes.Buffer
	cmd := newMCPCommand(&output, func(_ context.Context, _ string, _ time.Duration) (mcpQuickstartInfo, error) {
		return info, nil
	})
	cmd.SetArgs([]string{"quickstart", "--json"})

	require.NoError(cmd.Execute())

	var got mcpQuickstartInfo
	require.NoError(json.Unmarshal(output.Bytes(), &got))
	assert.True(got.Active)
	assert.Equal("http://127.0.0.1:8092/mcp", got.Endpoint)
	require.NotNil(got.ClientConfig)
	assert.Equal("http", got.ClientConfig.MCPServers["kenn-forge"].Type)
	assert.Empty(got.ClientConfig.MCPServers["kenn-forge"].Headers)
}

func TestLoadMCPQuickstartExplainsStoppedDaemonWithoutCreatingRuntimeState(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	root := filepath.Join(t.TempDir(), "custom config")
	require.NoError(os.Mkdir(root, 0o700))
	dataDir := filepath.Join(root, "data")
	configPath := filepath.Join(root, "config.toml")
	require.NoError(os.WriteFile(configPath, fmt.Appendf(nil, `
host = "127.0.0.1"
port = 8091
data_dir = %q

[mcp]
enabled = true
`, dataDir), 0o600))

	info, err := loadMCPQuickstart(t.Context(), configPath, time.Second)
	require.NoError(err)

	assert.True(info.Enabled)
	assert.False(info.Active)
	assert.Contains(
		info.NextSteps,
		"Start the Forge daemon with `kenn-forge daemon start --config '"+configPath+"'`.",
	)
	_, err = os.Stat(runtimelock.LockPath(dataDir))
	assert.ErrorIs(err, os.ErrNotExist)
}

func TestLoadMCPQuickstartMissingConfig(t *testing.T) {
	for _, tc := range []struct {
		name        string
		defaultPath bool
	}{
		{name: "default", defaultPath: true},
		{name: "custom"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("KENN_FORGE_HOME", dir)
			configPath := filepath.Join(dir, "missing.toml")
			if tc.defaultPath {
				configPath = config.DefaultConfigPath()
			}
			info, err := loadMCPQuickstart(t.Context(), configPath, time.Second)
			if !tc.defaultPath {
				require.ErrorIs(t, err, os.ErrNotExist)
				return
			}
			require.NoError(t, err)
			assert.False(t, info.Active)
			assert.Contains(t, info.NextSteps, "Start the Forge daemon with `kenn-forge daemon start`.")
		})
	}
}
