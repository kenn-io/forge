package fleet

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCommandsForDeps table-tests the pure dependency->capability
// derivation across git/gh/tmux combinations, independent of which tools
// happen to be installed on the test machine.
func TestCommandsForDeps(t *testing.T) {
	for _, tc := range []struct {
		name                            string
		git, gh, tmux                   bool
		wantGit, wantImportPR, wantTmux bool
	}{
		{name: "none"},
		{name: "git", git: true, wantGit: true},
		{name: "gh-only", gh: true},
		{name: "tmux-only", tmux: true, wantTmux: true},
		{name: "git+gh", git: true, gh: true, wantGit: true, wantImportPR: true},
		{name: "all", git: true, gh: true, tmux: true, wantGit: true, wantImportPR: true, wantTmux: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert := assert.New(t)
			cmds := commandsForDeps(DependencyCapabilities{Git: tc.git, Gh: tc.gh, Tmux: tc.tmux})

			// Unconditional capabilities are always available.
			assert.True(cmds.ProjectRemove)

			// git-gated capabilities.
			assert.Equal(tc.wantGit, cmds.WorktreeCreate, "worktreeCreate tracks git")
			assert.Equal(tc.wantGit, cmds.WorktreeDelete, "worktreeDelete tracks git")
			assert.Equal(tc.wantGit, cmds.RepositoryClone, "repositoryClone tracks git")
			assert.Equal(tc.wantGit, cmds.ProjectAdd, "projectAdd tracks git")

			// composite + single-dependency capabilities.
			assert.Equal(tc.wantImportPR, cmds.WorktreeImportPR, "worktreeImportPR requires git && gh")
			assert.Equal(tc.wantTmux, cmds.SessionEnsure, "sessionEnsure tracks tmux")
			assert.Equal(tc.wantTmux, cmds.SessionKill, "sessionKill tracks tmux")
		})
	}
}

// TestProbeDerivesCommandsFromDeps verifies that the live Probe() wrapper
// derives its command set from its own detected dependencies via the same
// pure rules, so the invariant holds regardless of what is installed here.
func TestProbeDerivesCommandsFromDeps(t *testing.T) {
	caps := Probe(context.Background(), nil)
	assert.Equal(t, commandsForDeps(caps.Dependencies), caps.Commands,
		"Probe() commands must equal the pure derivation of its detected deps")
	// tmux version is only set when tmux is present.
	if !caps.Dependencies.Tmux {
		assert.Empty(t, caps.Features.TmuxVersion, "tmuxVersion must be empty when tmux is absent")
	}
}

func TestTmuxCommandOrDefault(t *testing.T) {
	assert := assert.New(t)
	assert.Equal([]string{"tmux"}, tmuxCommandOrDefault(nil), "nil falls back to bare tmux")
	assert.Equal([]string{"tmux"}, tmuxCommandOrDefault([]string{}), "empty falls back to bare tmux")
	assert.Equal([]string{"tmux"}, tmuxCommandOrDefault([]string{""}), "blank executable falls back to bare tmux")
	wrapper := []string{"systemd-run", "--user", "--scope", "tmux"}
	assert.Equal(wrapper, tmuxCommandOrDefault(wrapper), "a configured command is used verbatim")
}

// TestProbeHonorsConfiguredTmuxCommand proves the probe shells out through
// the configured tmux command (executable plus argv prefix) rather than a
// hard-coded "tmux", so a server with a custom tmux.command reports tmux as
// available and keeps its session operations enabled. A fake script stands
// in for the tmux binary and is invoked via a "/bin/sh <script>" prefix,
// exercising both the executable and the prefix args.
func TestProbeHonorsConfiguredTmuxCommand(t *testing.T) {
	assert := assert.New(t)
	dir := t.TempDir()
	script := filepath.Join(dir, "faketmux.sh")
	require.NoError(t, os.WriteFile(
		script,
		[]byte("#!/bin/sh\nif [ \"$1\" = \"-V\" ]; then echo \"tmux 9.9\"; exit 0; fi\nexit 1\n"),
		0o755,
	))

	caps := Probe(context.Background(), []string{"/bin/sh", script})
	assert.True(caps.Dependencies.Tmux, "configured tmux command must drive availability")
	assert.Equal("9.9", caps.Features.TmuxVersion, "version must come from the configured command")
	assert.True(caps.Commands.SessionEnsure, "tmux-gated commands follow the configured command")
	assert.True(caps.Commands.SessionKill, "tmux-gated commands follow the configured command")
}
