package config

import (
	"fmt"
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
	require := require.New(t)
	root := t.TempDir()
	require.NoError(os.Mkdir(filepath.Join(root, "state"), 0o700))
	require.NoError(os.WriteFile(
		filepath.Join(root, "config.toml"),
		[]byte("data_dir = \"./state\"\n"),
		0o600,
	))
	t.Chdir(root)

	cfg, err := Load(filepath.Join(root, "config.toml"))
	require.NoError(err)
	expected, err := filepath.EvalSymlinks(filepath.Join(root, "state"))
	require.NoError(err)

	assert.Equal(t, expected, cfg.DataDir)
}

// TestLoadCanonicalizesMissingDataDirUnderSymlink protects startup identity.
// If loading resolves the same configured directory differently before and
// after creation, a background parent cannot discover the child it started.
func TestLoadCanonicalizesMissingDataDirUnderSymlink(t *testing.T) {
	require := require.New(t)
	root := t.TempDir()
	target := filepath.Join(root, "target")
	alias := filepath.Join(root, "alias")
	require.NoError(os.Mkdir(target, 0o700))
	if err := os.Symlink(target, alias); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	resolvedTarget, err := filepath.EvalSymlinks(target)
	require.NoError(err)
	configured := filepath.Join(alias, "missing", "state")
	configPath := filepath.Join(root, "config.toml")
	require.NoError(os.WriteFile(
		configPath,
		fmt.Appendf(nil, "data_dir = %q\n", configured),
		0o600,
	))

	before, err := Load(configPath)
	require.NoError(err)
	require.NoError(os.MkdirAll(before.DataDir, 0o700))
	after, err := Load(configPath)
	require.NoError(err)

	require.Equal(filepath.Join(resolvedTarget, "missing", "state"), before.DataDir)
	require.Equal(before.DataDir, after.DataDir)
}
