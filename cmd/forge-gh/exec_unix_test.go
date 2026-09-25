//go:build !windows

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/procutil"
)

func TestPassThroughPreservesArgumentsStreamsAndExit(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	if os.Getenv("FORGE_GH_TEST_CHILD") == "1" {
		os.Exit(run([]string{"pr", "view", "two words", "--unknown=value"}))
	}
	dir := t.TempDir()
	real := filepath.Join(dir, "gh")
	require.NoError(os.WriteFile(real, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\"\ncat\nprintf 'provider error\\n' >&2\nexit 7\n"), 0o700))
	binary, err := os.Executable()
	require.NoError(err)
	cmd := procutil.CommandContext(t.Context(), binary, "-test.run=^TestPassThroughPreservesArgumentsStreamsAndExit$")
	cmd.Env = append(os.Environ(), "FORGE_GH_TEST_CHILD=1", "FORGE_GH_REAL="+real, "KENN_FORGE_HOME="+dir)
	cmd.Stdin = strings.NewReader("input\n")
	var stderr strings.Builder
	cmd.Stderr = &stderr
	output, err := cmd.Output()
	require.Error(err)
	var exit *exec.ExitError
	require.ErrorAs(err, &exit)
	assert.Equal(7, exit.ExitCode())
	assert.Equal("pr\nview\ntwo words\n--unknown=value\ninput\n", string(output))
	assert.Equal("provider error\n", stderr.String())
	usage, err := os.ReadFile(filepath.Join(dir, "forge-gh-usage.jsonl"))
	require.NoError(err)
	assert.Contains(string(usage), `"command":"pr view"`)
	assert.Contains(string(usage), `"reason":"unsupported"`)
	assert.Contains(string(usage), `"argv":["pr","view","two words","--unknown=value"]`)
}

func TestRealGHSkipsShimSymlinkAndNonExecutable(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	dir := t.TempDir()
	other := t.TempDir()
	third := t.TempDir()
	self, err := os.Executable()
	require.NoError(err)
	require.NoError(os.Symlink(self, filepath.Join(dir, "gh")))
	require.NoError(os.WriteFile(filepath.Join(other, "gh"), []byte("not executable"), 0o600))
	real := filepath.Join(third, "gh")
	require.NoError(os.WriteFile(real, []byte("#!/bin/sh\nexit 0\n"), 0o700))
	t.Setenv("FORGE_GH_REAL", "")
	t.Setenv("PATH", strings.Join([]string{dir, other, third}, string(os.PathListSeparator)))
	got, err := realGH()
	require.NoError(err)
	assert.Equal(real, got)
}
