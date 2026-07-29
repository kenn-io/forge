package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLoadCanonicalizesDataDir protects config identity at its owner. If
// loading leaves a relative path untouched, startup and reload can compare the
// same directory as two different instances after the working directory moves.
func TestLoadCanonicalizesDataDir(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(root, "state"), 0o700))
	require.NoError(t, os.WriteFile(
		filepath.Join(root, "config.toml"),
		[]byte("data_dir = \"./state\"\n"),
		0o600,
	))
	t.Chdir(root)

	cfg, err := Load(filepath.Join(root, "config.toml"))
	require.NoError(t, err)
	expected, err := filepath.EvalSymlinks(filepath.Join(root, "state"))
	require.NoError(t, err)

	assert.Equal(t, expected, cfg.DataDir)
}
