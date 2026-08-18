package workspaceapi

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestRuntimeAttachCommandPinsTmuxTmpdir pins socket routing for
// externally returned attach commands: tmux resolves -L sockets under
// TMUX_TMPDIR, and the caller's shell may not share the daemon's value,
// so the command must carry it explicitly.
func TestRuntimeAttachCommandPinsTmuxTmpdir(t *testing.T) {
	t.Setenv("TMUX_TMPDIR", "/custom/socket-dir")
	assert.Equal(t,
		[]string{
			"env", "-u", "TMUX", "TMUX_TMPDIR=/custom/socket-dir",
			"tmux", "-L", "kenn-forge",
			"-u", "attach-session", "-E", "-t", "forge-abc",
		},
		runtimeAttachCommand(
			[]string{"tmux", "-L", "kenn-forge"}, "forge-abc",
		),
	)
}

// TestRuntimeAttachCommandWithoutTmpdirUnsetsCallerValue pins the
// symmetric case: the caller's shell may carry TMUX_TMPDIR (or run
// inside tmux with TMUX set) while the daemon does not, which would
// route the attach to a different socket directory or refuse nesting.
func TestRuntimeAttachCommandWithoutTmpdirUnsetsCallerValue(t *testing.T) {
	t.Setenv("TMUX_TMPDIR", "")
	assert.Equal(t,
		[]string{
			"env", "-u", "TMUX", "-u", "TMUX_TMPDIR",
			"tmux", "-L", "kenn-forge",
			"-u", "attach-session", "-E", "-t", "forge-abc",
		},
		runtimeAttachCommand(
			[]string{"tmux", "-L", "kenn-forge"}, "forge-abc",
		),
	)
}
