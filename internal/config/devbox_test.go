package config

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExecutionWorkerDataDirSeparation(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	state := filepath.Join(root, "state")
	alias := filepath.Join(root, "alias")
	require.NoError(t, os.Mkdir(state, 0o700))
	require.NoError(t, os.Symlink(state, alias))
	for _, tc := range []struct {
		name, home, dataDir string
		separate            bool
	}{
		{"absolute default", state, state, false},
		{"relative default", "./state", state, false},
		{"symlinked default", alias, state, false},
		{"symlinked worker", state, alias, false},
		{"separate worker", alias, filepath.Join(root, "worker"), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("KENN_FORGE_HOME", tc.home)
			path := filepath.Join(t.TempDir(), "config.toml")
			require.NoError(t, os.WriteFile(path, fmt.Appendf(nil, `
host = "127.0.0.1"
data_dir = %q
[api]
require_auth = true
[execution_worker]
enabled = true
broker_socket = "/run/example/broker.sock"
uid = 1001
github_user_id = 1234
commit_name = "Developer A"
commit_email = "developer-a@example.org"
`, tc.dataDir), 0o600))
			_, err := Load(path)
			if tc.separate {
				assert.NoError(t, err)
			} else {
				assert.ErrorContains(t, err, "separate from the default Forge directory")
			}
		})
	}
}

func TestExecutionWorkerConfigRoundTrip(t *testing.T) {
	before, after := roundTripConfigString(t, `
host = "127.0.0.1"
data_dir = "/tmp/example-forge-worker"
[api]
require_auth = true
[execution_worker]
enabled = true
broker_socket = "/run/example/broker.sock"
uid = 1001
github_user_id = 1234
commit_name = "Developer A"
commit_email = "1234+developer-a@users.noreply.github.com"
worktree_dir = "/home/user-a/workspaces"
`)
	assert.Equal(t, before.ExecutionWorker, after.ExecutionWorker)
	assert.True(t, after.ExecutionWorker.Enabled)
	before, after = roundTripConfigString(t, `[devboxes]
registry_url = "https://boxes.example.org"
`)
	assert.Equal(t, before.Devboxes, after.Devboxes)
}

func TestDefaultExecutionTargetConfig(t *testing.T) {
	for _, tc := range []struct {
		name, target string
		valid        bool
	}{
		{"local", "", true},
		{"unavailable devbox", "devbox:connection-a", true},
		{"route-safe identifier", "devbox:Compute_1-A", true},
		{"unknown kind", "fleet:connection-a", false},
		{"unprefixed host", "connection-a", false},
		{"empty identifier", "devbox:", false},
		{"whitespace", "devbox:connection a", false},
		{"path", "devbox:connection/a", false},
		{"query", "devbox:connection?a", false},
		{"fragment", "devbox:connection#a", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			content := fmt.Sprintf("[workspaces]\ndefault_execution_target = %q\n", tc.target)
			if tc.valid {
				_, saved := roundTripConfigString(t, content)
				assert.Equal(t, tc.target, saved.Workspaces.DefaultExecutionTarget)
				return
			}
			_, err := Load(writeConfig(t, content))
			assert.ErrorContains(t, err, "workspaces.default_execution_target")
		})
	}
}
