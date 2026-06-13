package localruntime

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	shellquote "github.com/kballard/go-shellquote"
	Assert "github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/middleman/internal/workspace/panebootstrap"
)

// readPaneHandoff parses the pane launch token,
// `exec '<exe>' __tmux-pane-bootstrap '<handoff>'`, asserts its shape, and
// returns the env and argv recorded in the handoff file.
func readPaneHandoff(t *testing.T, token string) (env, argv []string) {
	t.Helper()
	rest, ok := strings.CutPrefix(token, "exec ")
	require.True(t, ok, "pane token must start with exec: %q", token)
	words, err := shellquote.Split(rest)
	require.NoError(t, err)
	require.Len(t, words, 3, "pane token: %q", token)
	require.Equal(t, panebootstrap.Subcommand, words[1])
	env, argv, err = panebootstrap.ReadHandoff(words[2])
	require.NoError(t, err)
	return env, argv
}

func TestTmuxLauncherAgentOperationsKeepEnvValuesOutOfArgv(t *testing.T) {
	assert := Assert.New(t)
	requireT := require.New(t)
	t.Setenv("XDG_RUNTIME_DIR", "argv-visible-value")
	t.Setenv("MIDDLEMAN_GITHUB_TOKEN", "secret-value")

	paneEnv := tmuxAgentEnvPolicy.paneEnvironment(
		os.Environ(), []string{"/bin/sh", "-lc", "sleep 10"}, nil,
	)
	launcher := tmuxLauncher{
		TmuxCommand: []string{"/usr/bin/tmux"},
		Session:     "middleman-test",
		CWD:         "/tmp/work tree",
		Pane:        paneEnv,
		OwnerMarker: "middleman:test-owner",
	}

	token, cleanup, err := launcher.newSessionPaneCommand()
	requireT.NoError(err)
	t.Cleanup(cleanup)
	newSession := launcher.newSessionCommand(token)
	newSessionText := strings.Join(newSession, "\n")

	assert.Equal("new-session", newSession[1])
	assert.Contains(newSession, "-E")
	assert.NotContains(newSession, "-e")
	assert.Contains(newSession, "-c")
	assert.Contains(newSession, "/tmp/work tree")
	assert.Contains(newSession, ";")
	assert.Contains(newSession, "set-option")
	assert.Contains(newSession, "@middleman_owner")
	assert.Contains(newSession, "middleman:test-owner")

	// Preserved env values live in the handoff file, never in tmux argv.
	assert.NotContains(newSessionText, "argv-visible-value")
	assert.NotContains(newSessionText, "secret-value")

	// The pane command must be one dialect-neutral token: tmux hands a
	// lone shell-command argument to the user's default shell, so anything
	// but a single quoted-words token breaks on fish hosts.
	assert.Contains(newSession, token)
	assert.True(strings.HasPrefix(token, "exec "), token)

	env, argv := readPaneHandoff(t, token)
	assert.Equal([]string{"/bin/sh", "-lc", "sleep 10"}, argv)
	// The agent policy preserves XDG_RUNTIME_DIR but strips the token.
	assert.Contains(env, "XDG_RUNTIME_DIR=argv-visible-value")
	assert.NotContains(strings.Join(env, "\n"), "secret-value")
}

func TestTmuxLauncherShellPolicyPreservesCustomEnvByKey(t *testing.T) {
	assert := Assert.New(t)
	requireT := require.New(t)
	t.Setenv("MIDDLEMAN_TEST_CUSTOM_SHELL_ENV", "custom-visible-value")

	shellKeys := tmuxShellEnvPolicy.keys(nil)
	agentKeys := tmuxAgentEnvPolicy.keys(nil)

	assert.Contains(shellKeys, "MIDDLEMAN_TEST_CUSTOM_SHELL_ENV")
	assert.NotContains(agentKeys, "MIDDLEMAN_TEST_CUSTOM_SHELL_ENV")

	paneEnv := tmuxShellEnvPolicy.paneEnvironment(
		os.Environ(), []string{"/bin/sh"}, nil,
	)
	launcher := tmuxLauncher{
		TmuxCommand: []string{"/usr/bin/tmux"},
		Session:     "middleman-test",
		Pane:        paneEnv,
	}
	token, cleanup, err := launcher.newSessionPaneCommand()
	requireT.NoError(err)
	t.Cleanup(cleanup)

	// The shell policy carries the custom value in the handoff env; it
	// never appears in the tmux launch token.
	env, _ := readPaneHandoff(t, token)
	assert.Contains(env, "MIDDLEMAN_TEST_CUSTOM_SHELL_ENV=custom-visible-value")
	assert.NotContains(token, "custom-visible-value")
}

func TestTmuxLauncherRejectsUnownedExistingSession(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)
	dir := t.TempDir()
	record := filepath.Join(dir, "tmux-record")
	tmuxPath := filepath.Join(dir, "tmux")
	require.NoError(os.WriteFile(tmuxPath, []byte(`#!/bin/sh
printf '%s\0' "$#" "$@" >> "$TMUX_RECORD"
case "$1" in
  has-session)
    exit 0
    ;;
  show-options)
    printf '%s\n' "$TMUX_EXISTING_OWNER"
    exit 0
    ;;
  attach-session)
    exit 0
    ;;
esac
exit 0
`), 0o755))

	launcher := tmuxLauncher{
		TmuxCommand: []string{tmuxPath},
		Session:     "middleman-test",
		Pane: tmuxPaneEnvironment{
			command: []string{"/bin/sh"},
			keys:    []string{"PATH", "TERM"},
			commandEnv: append(
				os.Environ(),
				"TMUX_RECORD="+record,
				"TMUX_EXISTING_OWNER=other-owner",
			),
		},
		OwnerMarker: "middleman:test-owner",
	}

	_, err := launcher.prepare(context.Background())

	require.Error(err)
	records := readNullArgvRecord(t, record)
	assert.Contains(records, []string{
		"has-session", "-t", "middleman-test",
	})
	assert.Contains(records, []string{
		"show-options", "-qv", "-t", "middleman-test", "@middleman_owner",
	})
	assert.NotContains(records, []string{
		"attach-session", "-t", "middleman-test",
	})
	assert.NotContains(records, []string{
		"new-session", "-E", "-d", "-s", "middleman-test",
	})
}

func readNullArgvRecord(t *testing.T, path string) [][]string {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	if len(data) == 0 {
		return nil
	}
	fields := strings.Split(string(data), "\x00")
	var records [][]string
	for i := 0; i < len(fields); {
		if fields[i] == "" && i == len(fields)-1 {
			break
		}
		count, err := strconv.Atoi(fields[i])
		require.NoError(t, err)
		i++
		require.LessOrEqual(t, i+count, len(fields))
		records = append(records, fields[i:i+count])
		i += count
	}
	return records
}
