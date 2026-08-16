package workspace

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestTmuxExecUsesAllowlistedClientEnvironment pins that manager tmux
// invocations carry only the non-secret allowlist. The client that
// spawns the tmux server seeds the server's permanently retained
// global environment (readable in panes via `show-environment -g`),
// and an allowlist cannot leak a credential the configuration never
// named.
func TestTmuxExecUsesAllowlistedClientEnvironment(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "builtin-secret")
	t.Setenv("KATA_AUTH_TOKEN", "kata-secret")
	t.Setenv("WKSP_UNDECLARED_SECRET", "undeclared-secret")

	m := NewManager(nil, t.TempDir())
	cmd := m.tmuxExec(t.Context(), "has-session", "-t", "forge-test")
	require.NotNil(t, cmd)
	env := strings.Join(cmd.Env, "\n")

	assert := assert.New(t)
	assert.Contains(env, "PATH=", "allowlisted variables must survive")
	assert.NotContains(env, "GITHUB_TOKEN")
	assert.NotContains(env, "KATA_AUTH_TOKEN")
	assert.NotContains(env, "WKSP_UNDECLARED_SECRET")
	assert.NotContains(env, "builtin-secret")
	assert.NotContains(env, "kata-secret")
	assert.NotContains(env, "undeclared-secret")
}
