package workspacetest

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/procutil"
	"go.kenn.io/forge/internal/testutil/testtmux"
	"go.kenn.io/forge/internal/workspace/localruntime"
)

func TestMain(m *testing.M) {
	if code, ok := testtmux.CommandWrapperExitCode(); ok {
		os.Exit(code)
	}
	os.Exit(m.Run())
}

func TestTmuxRuntimeFixtureStopsAgentsBeforeServerCleanup(t *testing.T) {
	tmuxPath, err := exec.LookPath("tmux")
	if err != nil || !testtmux.Supported() {
		t.Skip("requires tmux on a supported host")
	}
	forgeHome := t.TempDir()
	t.Setenv("KENN_FORGE_HOME", forgeHome)
	t.Setenv("KENN_FORGE_TMUX_ENV_DIR", "")
	owner, err := testtmux.New()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, owner.Cleanup()) })
	for _, tc := range []struct {
		name     string
		detached bool
	}{{name: "active agent"}, {name: "detached agent", detached: true}} {
		t.Run(tc.name, func(t *testing.T) {
			cwd := t.TempDir()
			var command []string
			var fixture *localruntime.TmuxRuntimeFixture
			var sessions []string
			checkStopped := func() {
				if !tc.detached {
					require.NoError(t, procutil.Command(command[0], append(command[1:], "has-session", "-t", "keeper")...).Run())
				}
				for _, session := range sessions {
					require.Error(t, procutil.Command(command[0], append(command[1:], "has-session", "-t", session)...).Run())
				}
			}
			if tc.detached {
				command = owner.Command(t, tmuxPath)
				// Split construction to observe fixture cleanup before owner release.
				t.Cleanup(checkStopped)
				fixture = localruntime.NewTmuxRuntimeFixture(t, command)
			} else {
				fixture = NewTmuxRuntimeFixture(t, owner, tmuxPath)
				command = fixture.ServerCommand
			}
			if !tc.detached {
				require.NoError(t, procutil.CommandContext(t.Context(), command[0], append(command[1:], "new-session", "-d", "-s", "keeper", "sleep 600")...).Run())
				// Observe manager cleanup before private-server cleanup.
				t.Cleanup(checkStopped)
			}
			manager := fixture.NewAgentManager(command, localruntime.Options{
				Targets: []localruntime.LaunchTarget{{Key: "agent", Kind: localruntime.LaunchTargetAgent, Available: true, Command: []string{"/bin/sh", "-c", "exec sleep 600"}}, {Key: "shell", Kind: localruntime.LaunchTargetShell, Available: true, Command: command}},
			})
			for _, workspaceID := range []string{"workspace-a", "workspace-b"} {
				info, err := manager.Launch(t.Context(), workspaceID, cwd, "agent")
				require.NoError(t, err)
				require.NotEmpty(t, info.TmuxSession)
				sessions = append(sessions, info.TmuxSession)
			}
			require.NoDirExists(t, filepath.Join(forgeHome, "tmux-env"))
			if tc.detached {
				manager.Shutdown()
			}
			for _, session := range sessions {
				require.NoError(t, procutil.CommandContext(t.Context(), command[0], append(command[1:], "has-session", "-t", session)...).Run())
			}
		})
	}
}
