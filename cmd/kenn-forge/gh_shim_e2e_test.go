//go:build !windows

package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/forge/internal/procutil"
)

func TestGHShimPassesUnsupportedCallsToRealGH(t *testing.T) {
	bin := buildForge(t)
	realDir := t.TempDir()
	// The real gh calls gh again for "nested", as gh extensions do.
	require.NoError(t, os.WriteFile(filepath.Join(realDir, "gh"), []byte("#!/bin/sh\nprintf '%s\\n' \"$@\"\nif [ \"$1\" = nested ]; then gh inner; fi\nexit 7\n"), 0o700))

	for _, tc := range []struct {
		name    string
		install func(t *testing.T, shimDir string)
		direct  bool
		args    []string
		want    string
		logged  []string
	}{
		{
			name:    "gh symlink",
			install: func(t *testing.T, dir string) { require.NoError(t, os.Symlink(bin, filepath.Join(dir, "gh"))) },
			args:    []string{"pr", "list", "-L5", "--jq", ".[].number"},
			want:    "pr\nlist\n-L5\n--jq\n.[].number\n",
			logged:  []string{`"argv":["pr","list","-L5","--jq",".[].number"]`},
		},
		{
			name:   "gh subcommand",
			direct: true,
			args:   []string{"pr", "list", "-L5", "--jq", ".[].number"},
			want:   "pr\nlist\n-L5\n--jq\n.[].number\n",
			logged: []string{`"argv":["pr","list","-L5","--jq",".[].number"]`},
		},
		{
			name:    "exec wrapper script",
			install: writeGHWrapper(bin, "exec ", "", 4),
			args:    []string{"pr", "view", "--web"},
			want:    "pr\nview\n--web\n",
			logged:  []string{`"argv":["pr","view","--web"]`},
		},
		{
			name:    "child wrapper script with nested gh call",
			install: writeGHWrapper(bin, "", "", 4),
			args:    []string{"nested"},
			want:    "nested\ninner\n",
			logged:  []string{`"argv":["nested"]`, `"argv":["inner"]`},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert := assert.New(t)
			shimDir, home := t.TempDir(), t.TempDir()
			path, args := filepath.Join(shimDir, "gh"), tc.args
			if tc.direct {
				path, args = bin, append([]string{"gh"}, tc.args...)
			} else {
				tc.install(t, shimDir)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			cmd := procutil.CommandContext(ctx, path, args...)
			cmd.Env = append(os.Environ(),
				"PATH="+strings.Join([]string{shimDir, realDir, os.Getenv("PATH")}, string(os.PathListSeparator)),
				"FORGE_GH_REAL=", "KENN_FORGE_GH_HANDOFF=", "KENN_FORGE_GH_SKIP=", "KENN_FORGE_GH_DEPTH=", "TEST_GH_HOPS=", "KENN_FORGE_HOME="+home)
			output, err := cmd.Output()
			require.NoError(t, ctx.Err(), "shim looped instead of reaching the real gh")
			var exit *exec.ExitError
			require.ErrorAs(t, err, &exit)
			assert.Equal(7, exit.ExitCode())
			assert.Equal(tc.want, string(output))

			usage, err := os.ReadFile(filepath.Join(home, "gh-shim-usage.jsonl"))
			require.NoError(t, err)
			lines := strings.Split(strings.TrimSpace(string(usage)), "\n")
			require.Len(t, lines, len(tc.logged), "each call is logged once")
			for i, argv := range tc.logged {
				assert.Contains(lines[i], argv)
			}
		})
	}
}

// writeGHWrapper installs a gh script that runs `kenn-forge gh`, with or
// without replacing the shell process. The script stops after a few hops so a
// regression fails the test instead of spawning processes without bound.
func writeGHWrapper(bin, prefix, extraArg string, maxHops int) func(*testing.T, string) {
	return func(t *testing.T, dir string) {
		script := fmt.Sprintf("#!/bin/sh\nhops=$((${TEST_GH_HOPS:-0}+1))\n[ \"$hops\" -gt %d ] && exit 99\nexport TEST_GH_HOPS=$hops\n%s'%s' gh \"$@\" %s\n", maxHops, prefix, bin, extraArg)
		require.NoError(t, os.WriteFile(filepath.Join(dir, "gh"), []byte(script), 0o700))
	}
}

func TestGHShimStopsWrapperThatChangesArguments(t *testing.T) {
	bin := buildForge(t)
	shimDir, home := t.TempDir(), t.TempDir()
	// Each hop appends an argument, so the call never matches its handoff.
	// The script's own limit sits above the shim's depth limit.
	writeGHWrapper(bin, "exec ", "extra", 40)(t, shimDir)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	cmd := procutil.CommandContext(ctx, filepath.Join(shimDir, "gh"), "pr", "view")
	cmd.Env = append(os.Environ(),
		"PATH="+strings.Join([]string{shimDir, t.TempDir()}, string(os.PathListSeparator)),
		"FORGE_GH_REAL=", "KENN_FORGE_GH_HANDOFF=", "KENN_FORGE_GH_SKIP=", "KENN_FORGE_GH_DEPTH=", "TEST_GH_HOPS=", "KENN_FORGE_HOME="+home)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	err := cmd.Run()
	require.NoError(t, ctx.Err())
	var exit *exec.ExitError
	require.ErrorAs(t, err, &exit)
	assert.Equal(t, 1, exit.ExitCode())
	assert.Contains(t, stderr.String(), "gh keeps calling back into kenn-forge gh")
}
