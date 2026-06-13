package localruntime

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	shellquote "github.com/kballard/go-shellquote"
	Assert "github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// shellquoteSplitSingle unquotes a one-token shell string.
func shellquoteSplitSingle(quoted string) (string, error) {
	words, err := shellquote.Split(quoted)
	if err != nil {
		return "", err
	}
	if len(words) != 1 {
		return "", fmt.Errorf("expected one word, got %d in %q", len(words), quoted)
	}
	return words[0], nil
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

	paneCommand, cleanup, err := launcher.newSessionPaneCommand()
	requireT.NoError(err)
	t.Cleanup(cleanup)
	newSession := launcher.newSessionCommand(paneCommand)
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
	assert.NotContains(newSessionText, "argv-visible-value")
	assert.NotContains(newSessionText, "secret-value")

	// The pane command must be one dialect-neutral token: tmux hands a
	// lone shell-command argument to the user's default shell, so a
	// multi-statement inline script (or unquoted /bin/sh -c argv,
	// which tmux flattens) breaks on fish hosts.
	assert.Contains(newSession, paneCommand)
	assert.True(strings.HasPrefix(paneCommand, "exec /bin/sh "), paneCommand)
	assert.NotContains(newSessionText, "\nexport ")

	// The handoff script holds the real pane bootstrap.
	scriptPath, err := shellquoteSplitSingle(strings.TrimPrefix(paneCommand, "exec /bin/sh "))
	requireT.NoError(err)
	scriptBytes, err := os.ReadFile(scriptPath)
	requireT.NoError(err)
	script := string(scriptBytes)
	assert.Contains(script, "exec env -i")
	assert.Contains(script, "__middleman_env_file=")
	assert.Contains(script, `-f "$__middleman_env_file" "$__middleman_script_file"`)
	assert.Contains(script, "trap __middleman_cleanup_files EXIT")
	assert.Contains(script, "trap - EXIT")
	// Cleanup must not resolve rm from the workspace-CWD PATH: a
	// malicious repo can ship an executable named rm.
	cleanupLine := ""
	for line := range strings.SplitSeq(script, "\n") {
		if strings.Contains(line, "__middleman_cleanup_files()") {
			cleanupLine = line
		}
	}
	requireT.NotEmpty(cleanupLine)
	assert.Regexp(`__middleman_cleanup_files\(\) \{ '?/`, cleanupLine,
		"cleanup must invoke rm by absolute path")
	assert.NotContains(script, "argv-visible-value")
	assert.NotContains(script, "secret-value")

	// The sourced env file carries the preserved values.
	envLine := ""
	for line := range strings.SplitSeq(script, "\n") {
		if after, ok := strings.CutPrefix(line, "__middleman_env_file="); ok {
			envLine = after
		}
	}
	requireT.NotEmpty(envLine)
	envPath, err := shellquoteSplitSingle(envLine)
	requireT.NoError(err)
	envBytes, err := os.ReadFile(envPath)
	requireT.NoError(err)
	assert.Contains(string(envBytes), "export XDG_RUNTIME_DIR=argv-visible-value")
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
