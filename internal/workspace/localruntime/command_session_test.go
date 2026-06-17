package localruntime

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	shellquote "github.com/kballard/go-shellquote"
	Assert "github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeCommandSessionFakeTmux writes a fake tmux that records argv, tracks
// session existence in a state file, reports the given owner marker, and
// blocks on attach so launched sessions stay running for the test's duration.
func writeCommandSessionFakeTmux(
	t *testing.T,
	ownerMarker string,
) (tmuxPath string, recordPath string, statePath string) {
	t.Helper()
	dir := t.TempDir()
	recordPath = filepath.Join(dir, "tmux-record")
	statePath = filepath.Join(dir, "tmux-session-exists")
	tmuxPath = filepath.Join(dir, "tmux")
	script := fmt.Sprintf(`#!/bin/sh
printf '%%s\0' "$#" "$@" >> %s
case "$1" in
  has-session)
    if [ -f %s ]; then exit 0; fi
    echo "can't find session: $3" >&2
    exit 1
    ;;
  new-session)
    touch %s
    exit 0
    ;;
  show-options)
    printf '%%s\n' %s
    exit 0
    ;;
  attach-session)
    exec sleep 60
    ;;
esac
exit 0
`,
		shellquote.Join(recordPath),
		shellquote.Join(statePath),
		shellquote.Join(statePath),
		shellquote.Join(ownerMarker),
	)
	require.NoError(t, os.WriteFile(tmuxPath, []byte(script), 0o755))
	return tmuxPath, recordPath, statePath
}

func commandSessionTestSpec(key string, cwd string) CommandLaunchSpec {
	return CommandLaunchSpec{
		SessionKey: key,
		Command:    []string{"/bin/sh", "-lc", `exec "${SHELL:-/bin/sh}" -l`},
		Env:        map[string]string{"CUSTOM_SESSION_VAR": "custom-value"},
		Label:      "My Shell",
		CWD:        cwd,
	}
}

func TestEnsureCommandSessionLaunchesTmuxBackedCommand(t *testing.T) {
	requirePTYAvailable(t)
	require := require.New(t)
	assert := Assert.New(t)
	tmuxPath, recordPath, _ := writeCommandSessionFakeTmux(
		t, "middleman:test-owner",
	)
	mgr := NewManager(Options{
		TmuxCommand:     []string{tmuxPath},
		TmuxOwnerMarker: "middleman:test-owner",
	})
	t.Cleanup(mgr.Shutdown)

	cwd := t.TempDir()
	info, err := mgr.EnsureCommandSession(
		context.Background(), "scope-1",
		commandSessionTestSpec("client:key one", cwd),
	)
	require.NoError(err)

	sessionName := tmuxSessionName("scope-1", "client:key one")
	assert.Equal("client:key one", info.Key)
	assert.Equal("scope-1", info.WorkspaceID)
	assert.Equal("My Shell", info.Label)
	assert.Equal(LaunchTargetCommand, info.Kind)
	assert.Equal(sessionName, info.TmuxSession)

	records := readNullArgvRecord(t, recordPath)
	assert.Contains(records, []string{"has-session", "-t", sessionName})
	var newSession []string
	for _, record := range records {
		if len(record) > 0 && record[0] == "new-session" {
			newSession = record
			break
		}
	}
	require.NotEmpty(newSession)
	newSessionText := strings.Join(newSession, "\n")
	assert.Contains(newSession, "-c")
	assert.Contains(newSession, cwd)
	assert.Contains(newSessionText, `CUSTOM_SESSION_VAR="${CUSTOM_SESSION_VAR-}"`)
	assert.Contains(newSessionText, shellquote.Join("/bin/sh"))

	sessions := mgr.ListSessions("scope-1")
	require.Len(sessions, 1)
	assert.Equal("client:key one", sessions[0].Key)
}

func TestEnsureCommandSessionReturnsExistingLiveSession(t *testing.T) {
	requirePTYAvailable(t)
	require := require.New(t)
	assert := Assert.New(t)
	tmuxPath, recordPath, _ := writeCommandSessionFakeTmux(
		t, "middleman:test-owner",
	)
	mgr := NewManager(Options{
		TmuxCommand:     []string{tmuxPath},
		TmuxOwnerMarker: "middleman:test-owner",
	})
	t.Cleanup(mgr.Shutdown)

	ctx := context.Background()
	cwd := t.TempDir()
	first, err := mgr.EnsureCommandSession(
		ctx, "scope-1", commandSessionTestSpec("client:key one", cwd),
	)
	require.NoError(err)
	require.Eventually(func() bool {
		data, err := os.ReadFile(recordPath)
		if err != nil {
			return false
		}
		return strings.Contains(string(data), "attach-session\x00")
	}, 2*time.Second, 20*time.Millisecond)
	callsAfterFirst := len(readNullArgvRecord(t, recordPath))

	second, err := mgr.EnsureCommandSession(
		ctx, "scope-1", commandSessionTestSpec("client:key one", cwd),
	)
	require.NoError(err)

	assert.Equal(first.Key, second.Key)
	assert.Equal(first.TmuxSession, second.TmuxSession)
	assert.False(first.Reused, "first ensure launches")
	assert.True(second.Reused,
		"second ensure must report reuse so callers do not stop"+
			" a session they did not start")
	assert.Len(readNullArgvRecord(t, recordPath), callsAfterFirst)
	assert.Len(mgr.ListSessions("scope-1"), 1)
}

func TestEnsureCommandSessionRejectsKeyOwnedByAnotherScope(t *testing.T) {
	requirePTYAvailable(t)
	require := require.New(t)
	tmuxPath, _, _ := writeCommandSessionFakeTmux(t, "middleman:test-owner")
	mgr := NewManager(Options{
		TmuxCommand:     []string{tmuxPath},
		TmuxOwnerMarker: "middleman:test-owner",
	})
	t.Cleanup(mgr.Shutdown)

	ctx := context.Background()
	_, err := mgr.EnsureCommandSession(
		ctx, "scope-1", commandSessionTestSpec("client:key one", t.TempDir()),
	)
	require.NoError(err)

	_, err = mgr.EnsureCommandSession(
		ctx, "scope-2", commandSessionTestSpec("client:key one", t.TempDir()),
	)
	require.Error(err)
	require.Contains(err.Error(), "another scope")
}

func TestEnsureCommandSessionRequiresCommand(t *testing.T) {
	tmuxPath, _, _ := writeCommandSessionFakeTmux(t, "middleman:test-owner")
	mgr := NewManager(Options{TmuxCommand: []string{tmuxPath}})
	t.Cleanup(mgr.Shutdown)

	_, err := mgr.EnsureCommandSession(
		context.Background(), "scope-1",
		CommandLaunchSpec{SessionKey: "client:key one"},
	)
	require.Error(t, err)
	require.Contains(t, err.Error(), "command is required")
}

func TestEnsureCommandSessionReattachesToSurvivingTmuxSession(t *testing.T) {
	requirePTYAvailable(t)
	require := require.New(t)
	assert := Assert.New(t)
	tmuxPath, recordPath, statePath := writeCommandSessionFakeTmux(
		t, "middleman:test-owner",
	)
	// Simulate a tmux session that survived a manager restart.
	require.NoError(os.WriteFile(statePath, nil, 0o644))
	mgr := NewManager(Options{
		TmuxCommand:     []string{tmuxPath},
		TmuxOwnerMarker: "middleman:test-owner",
	})
	t.Cleanup(mgr.Shutdown)

	info, err := mgr.EnsureCommandSession(
		context.Background(), "scope-1",
		commandSessionTestSpec("client:key one", t.TempDir()),
	)
	require.NoError(err)

	sessionName := tmuxSessionName("scope-1", "client:key one")
	assert.Equal(sessionName, info.TmuxSession)
	for _, record := range readNullArgvRecord(t, recordPath) {
		if len(record) > 0 {
			assert.NotEqual("new-session", record[0])
		}
	}
}

func TestEnsureCommandSessionRejectsForeignTmuxSessionOwner(t *testing.T) {
	requirePTYAvailable(t)
	require := require.New(t)
	tmuxPath, _, statePath := writeCommandSessionFakeTmux(
		t, "someone-else",
	)
	require.NoError(os.WriteFile(statePath, nil, 0o644))
	mgr := NewManager(Options{
		TmuxCommand:     []string{tmuxPath},
		TmuxOwnerMarker: "middleman:test-owner",
	})
	t.Cleanup(mgr.Shutdown)

	_, err := mgr.EnsureCommandSession(
		context.Background(), "scope-1",
		commandSessionTestSpec("client:key one", t.TempDir()),
	)
	require.Error(err)
	require.Contains(err.Error(), "not owned")
}
