package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMCPCLIRejectsUnknownTransport(t *testing.T) {
	err := runMCPCLI([]string{"--transport", "bad-transport"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported transport")
}

func TestMCPCLIHTTPTransportFailsExplicitlyUntilImplemented(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")
	require.NoError(t, os.WriteFile(cfgPath, fmt.Appendf(nil, "data_dir = %q\n", dir), 0o600))

	err := runMCPCLI([]string{
		"--config", cfgPath,
		"--transport", "http",
		"--http-token-env", "MIDDLEMAN_MCP_TOKEN",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "http transport not yet available")
}
