package localruntime

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	shellquote "github.com/kballard/go-shellquote"
	Assert "github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTmuxLauncherAgentOperationsKeepEnvValuesOutOfArgv(t *testing.T) {
	assert := Assert.New(t)
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

	paneCommand, cleanup, err := launcher.newSessionPaneCommand()
	require.NoError(t, err)
	t.Cleanup(cleanup)
	scriptText := requireTmuxPaneScript(t, paneCommand)
	newSession := launcher.newSessionCommand(paneCommand)
	newSessionText := strings.Join(newSession, "\n")
	paneCommandArg := ""
	for i, arg := range newSession {
		if arg == ";" && i > 0 {
			paneCommandArg = newSession[i-1]
			break
		}
	}
	require.NotEmpty(t, paneCommandArg)

	assert.Equal("new-session", newSession[1])
	assert.Contains(newSession, "-E")
	assert.NotContains(newSession, "-e")
	assert.Equal(paneCommand, paneCommandArg)
	assert.Contains(newSession, "-c")
	assert.Contains(newSession, "/tmp/work tree")
	assert.Contains(scriptText, "exec env -i")
	assert.Contains(newSession, ";")
	assert.Contains(newSession, "set-option")
	assert.Contains(newSession, "@middleman_owner")
	assert.Contains(newSession, "middleman:test-owner")
	assert.Contains(scriptText, `XDG_RUNTIME_DIR="${XDG_RUNTIME_DIR-}"`)
	assert.Contains(scriptText, "__middleman_env_file=")
	assert.Contains(scriptText, "__middleman_script_file=")
	assert.Contains(scriptText, "trap __middleman_cleanup_tmux_files EXIT")
	assert.Contains(scriptText, "trap - EXIT")
	assert.NotContains(newSessionText, "argv-visible-value")
	assert.NotContains(newSessionText, "secret-value")
	assert.NotContains(scriptText, "argv-visible-value")
	assert.NotContains(scriptText, "secret-value")
}

func TestTmuxLauncherCanHideStatusOnNewSessions(t *testing.T) {
	assert := Assert.New(t)

	paneEnv := tmuxAgentEnvPolicy.paneEnvironment(
		os.Environ(), []string{"/bin/sh", "-lc", "sleep 10"}, nil,
	)
	launcher := tmuxLauncher{
		TmuxCommand: []string{"/usr/bin/tmux"},
		Session:     "middleman-test",
		CWD:         "/tmp/work tree",
		Pane:        paneEnv,
		OwnerMarker: "middleman:test-owner",
		HideStatus:  true,
	}

	paneCommand, cleanup, err := launcher.newSessionPaneCommand()
	require.NoError(t, err)
	t.Cleanup(cleanup)
	newSession := launcher.newSessionCommand(paneCommand)

	hideStatus := launcher.hideStatusCommand()

	assert.NotContains(newSession, "status")
	assert.NotContains(newSession, "off")
	assert.True(containsArgvSequence(hideStatus, []string{
		"set-option", "-q", "-t", "middleman-test", "status", "off",
	}))
	assert.NotContains(launcher.attachSessionCommand(), "status")
}

func TestTmuxLauncherCleansUpWhenHideStatusFails(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)
	dir := t.TempDir()
	record := filepath.Join(dir, "tmux-record")
	created := filepath.Join(dir, "created")
	tmuxPath := filepath.Join(dir, "tmux")
	require.NoError(os.WriteFile(tmuxPath, []byte(`#!/bin/sh
printf '%s\0' "$#" "$@" >> "$TMUX_RECORD"
case "$1" in
  has-session)
    if [ -f "$TMUX_CREATED" ]; then exit 0; fi
    echo "can't find session: $3" >&2
    exit 1
    ;;
  new-session)
    : > "$TMUX_CREATED"
    previous=""
    for arg in "$@"; do
      if [ "$previous" = "status" ] && [ "$arg" = "off" ]; then
        echo "status update failed" >&2
        exit 1
      fi
      previous="$arg"
    done
    exit 0
    ;;
  show-options)
    printf '%s\n' "$TMUX_EXISTING_OWNER"
    exit 0
    ;;
  set-option)
    if [ "$5" = "status" ] && [ "$6" = "off" ]; then
      echo "status update failed" >&2
      exit 1
    fi
    exit 0
    ;;
  kill-session)
    rm -f "$TMUX_CREATED"
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
			paneCommand: "exec /bin/sh",
			keys:        []string{"PATH", "TERM"},
			commandEnv: append(
				os.Environ(),
				"TMUX_RECORD="+record,
				"TMUX_CREATED="+created,
				"TMUX_EXISTING_OWNER=middleman:test-owner",
			),
		},
		OwnerMarker: "middleman:test-owner",
		HideStatus:  true,
	}

	_, err := launcher.prepare(context.Background())

	require.Error(err)
	assert.Contains(err.Error(), "hide tmux status")
	records := readNullArgvRecord(t, record)
	assert.Contains(records, []string{
		"kill-session", "-t", "middleman-test",
	})
	assert.NotContains(records, []string{
		"attach-session", "-t", "middleman-test",
	})
	assert.NoFileExists(created)
}

func TestTmuxLauncherShellPolicyPreservesCustomEnvByKey(t *testing.T) {
	assert := Assert.New(t)
	t.Setenv("MIDDLEMAN_TEST_CUSTOM_SHELL_ENV", "custom-visible-value")

	shellKeys := tmuxShellEnvPolicy.keys(nil)
	agentKeys := tmuxAgentEnvPolicy.keys(nil)

	assert.Contains(shellKeys, "MIDDLEMAN_TEST_CUSTOM_SHELL_ENV")
	assert.NotContains(agentKeys, "MIDDLEMAN_TEST_CUSTOM_SHELL_ENV")

	paneCommand := tmuxShellEnvPolicy.paneEnvironment(
		os.Environ(), []string{"/bin/sh"}, nil,
	).paneCommand
	assert.Contains(
		paneCommand,
		`MIDDLEMAN_TEST_CUSTOM_SHELL_ENV="${MIDDLEMAN_TEST_CUSTOM_SHELL_ENV-}"`,
	)
	assert.NotContains(paneCommand, "custom-visible-value")
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
			paneCommand: "exec /bin/sh",
			keys:        []string{"PATH", "TERM"},
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
		"new-session", "-e", "PATH", "-e", "TERM",
		"-d", "-s", "middleman-test", "exec /bin/sh",
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

func requireNewSessionPaneScript(t *testing.T, newSession []string) string {
	t.Helper()
	require.NotEmpty(t, newSession)
	command := newSession[len(newSession)-1]
	for i, arg := range newSession {
		if arg == ";" && i > 0 {
			command = newSession[i-1]
			break
		}
	}
	return requireTmuxPaneScript(t, command)
}

func requireTmuxPaneScript(t *testing.T, command string) string {
	t.Helper()
	words, err := shellquote.Split(command)
	require.NoError(t, err)
	require.Len(t, words, 2)
	require.Equal(t, "/bin/sh", words[0])
	data, err := os.ReadFile(words[1])
	require.NoError(t, err)
	return string(data)
}

func containsArgvSequence(argv []string, sequence []string) bool {
	if len(sequence) == 0 {
		return true
	}
	for i := 0; i+len(sequence) <= len(argv); i++ {
		if slices.Equal(argv[i:i+len(sequence)], sequence) {
			return true
		}
	}
	return false
}
