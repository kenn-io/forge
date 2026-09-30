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

func TestGHSymlinkToKennForgePassesUnsupportedCallsToRealGH(t *testing.T) {
	bin := buildForge(t)
	shimDir := t.TempDir()
	require.NoError(t, os.Symlink(bin, filepath.Join(shimDir, "gh")))
	realDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(realDir, "gh"), []byte("#!/bin/sh\nprintf '%s\\n' \"$@\"\nexit 7\n"), 0o700))
	home := t.TempDir()

	for _, tc := range []struct {
		name string
		path string
		args []string
	}{
		{"gh symlink", filepath.Join(shimDir, "gh"), []string{"pr", "list", "-L5", "--jq", ".[].number"}},
		{"gh subcommand", bin, []string{"gh", "pr", "list", "-L5", "--jq", ".[].number"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert := assert.New(t)
			cmd := procutil.CommandContext(t.Context(), tc.path, tc.args...)
			cmd.Env = append(os.Environ(),
				"PATH="+strings.Join([]string{shimDir, realDir, os.Getenv("PATH")}, string(os.PathListSeparator)),
				"FORGE_GH_REAL=", "KENN_FORGE_HOME="+home)
			output, err := cmd.Output()
			var exit *exec.ExitError
			require.ErrorAs(t, err, &exit)
			assert.Equal(7, exit.ExitCode())
			assert.Equal("pr\nlist\n-L5\n--jq\n.[].number\n", string(output))
		})
	}

	usage, err := os.ReadFile(filepath.Join(home, "gh-shim-usage.jsonl"))
	require.NoError(t, err)
	assert.Equal(t, 2, strings.Count(string(usage), `"argv":["pr","list","-L5","--jq",".[].number"]`))
}
