package server

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestServerBootAppliesInvalidCatalogTokenNames pins boot-time catalog
// stripping: a decoded-but-invalid Kata catalog still declares token
// env names, and terminals created before any Kata request must strip
// them.
func TestServerBootAppliesInvalidCatalogTokenNames(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("KATA_HOME", dir)
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, "config.toml"), []byte(`
[[daemon]]
name = "prod"
url = "https://kata.example.com"
token_env = "KATA_BOOT_REJECTED_TOKEN"

[[daemon]]
name = "prod"
url = "https://kata2.example.com"
`), 0o600))

	srv, _, _ := setupTestServerWithConfigContentAndOptions(
		t, validReloadConfig, &mockGH{}, ServerOptions{
			HostCheckAllowLoopbackAnyPort:      true,
			WorktreeDir:                        t.TempDir(),
			DisableWorkspaceBackgroundMonitors: true,
		},
	)
	assert.Contains(t, srv.workspaces.TmuxStripEnvVars(),
		"KATA_BOOT_REJECTED_TOKEN",
		"invalid catalogs' declared names must strip from boot")
}
