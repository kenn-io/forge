//go:build !windows

package procutil

import (
	"os"
	"path/filepath"
	"testing"

	Assert "github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveBinaryRejectsRelativePathMatch(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	root := t.TempDir()
	require.NoError(os.Mkdir(filepath.Join(root, "bin"), 0o755))
	toolPath := filepath.Join(root, "bin", "fake-tool")
	require.NoError(os.WriteFile(toolPath, []byte("#!/bin/sh\nexit 0\n"), 0o755))
	t.Chdir(root)
	t.Setenv("PATH", "bin")

	got := ResolveBinary("fake-tool")

	assert.False(filepath.IsAbs(got), "relative PATH matches should not be returned: %s", got)
	assert.Equal("fake-tool", got)
}

func TestResolveBinarySkipsNonExecutablePathMatch(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	dir := t.TempDir()
	toolPath := filepath.Join(dir, "fake-tool")
	require.NoError(os.WriteFile(toolPath, []byte("not executable"), 0o644))
	t.Setenv("PATH", dir)

	got := ResolveBinary("fake-tool")

	assert.Equal("fake-tool", got)
}
