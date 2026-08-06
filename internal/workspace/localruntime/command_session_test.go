package localruntime

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	shellquote "github.com/kballard/go-shellquote"
	"github.com/stretchr/testify/assert"
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
	launchPath := filepath.Join(dir, "tmux-launch-id")
	killFailurePath := filepath.Join(dir, "tmux-kill-failure")
	tmuxPath = filepath.Join(dir, "tmux")
	script := fmt.Sprintf(`#!/bin/sh
printf '%%s\0' "$#" "$@" >> %s
if [ "$1" = "-u" ]; then shift; fi
case "$1" in
  has-session)
    if [ -f %s ]; then exit 0; fi
    echo "can't find session: $3" >&2
    exit 1
    ;;
  new-session)
    touch %s
    marker=""
    for arg in "$@"; do
      if [ "$marker" = "next" ]; then
        printf '%%s\n' "$arg" > %s
        marker="done"
      fi
      if [ "$arg" = "@forge_launch" ]; then marker="next"; fi
    done
    exit 0
    ;;
  set-option)
    marker=""
    for arg in "$@"; do
      if [ "$marker" = "next" ]; then
        printf '%%s\n' "$arg" > %s
        marker="done"
      fi
      if [ "$arg" = "@forge_launch" ]; then marker="next"; fi
    done
    exit 0
    ;;
  show-options)
    option=""
    for arg in "$@"; do option="$arg"; done
    if [ "$option" = "@forge_launch" ]; then
      if [ -f %s ]; then cat %s; fi
    else
      printf '%%s\n' %s
    fi
    exit 0
    ;;
  attach-session)
    exec sleep 60
    ;;
  kill-session)
    if [ -f %s ]; then
      echo "forced kill-session failure" >&2
      exit 1
    fi
    rm -f %s
    exit 0
    ;;
esac
exit 0
`,
		shellquote.Join(recordPath),
		shellquote.Join(statePath),
		shellquote.Join(statePath),
		shellquote.Join(launchPath),
		shellquote.Join(launchPath),
		shellquote.Join(launchPath),
		shellquote.Join(launchPath),
		shellquote.Join(ownerMarker),
		shellquote.Join(killFailurePath),
		shellquote.Join(statePath),
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

func persistCommandSessionNoop(context.Context, SessionInfo) error {
	return nil
}

func TestEnsureCommandSessionAndPersistLaunchesTmuxBackedCommand(t *testing.T) {
	requirePTYAvailable(t)
	require := require.New(t)
	assert := assert.New(t)
	tmuxPath, recordPath, _ := writeCommandSessionFakeTmux(
		t, "kenn-forge:test-owner",
	)
	mgr := NewManager(Options{
		TmuxCommand:     []string{tmuxPath},
		TmuxOwnerMarker: "kenn-forge:test-owner",
	})
	t.Cleanup(mgr.Shutdown)

	cwd := t.TempDir()
	info, err := mgr.EnsureCommandSessionAndPersist(
		context.Background(), "scope-1",
		commandSessionTestSpec("client:key one", cwd),
		persistCommandSessionNoop,
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
	scriptText := requireNewSessionPaneScript(t, newSession)
	assert.Contains(newSession, "-c")
	assert.Contains(newSession, cwd)
	assert.Contains(scriptText, `CUSTOM_SESSION_VAR="${CUSTOM_SESSION_VAR-}"`)
	assert.Contains(scriptText, shellquote.Join("/bin/sh"))

	sessions := mgr.ListSessions("scope-1")
	require.Len(sessions, 1)
	assert.Equal("client:key one", sessions[0].Key)
}

func TestEnsureCommandSessionAndPersistReturnsExistingLiveSession(t *testing.T) {
	requirePTYAvailable(t)
	require := require.New(t)
	assert := assert.New(t)
	tmuxPath, recordPath, _ := writeCommandSessionFakeTmux(
		t, "kenn-forge:test-owner",
	)
	mgr := NewManager(Options{
		TmuxCommand:     []string{tmuxPath},
		TmuxOwnerMarker: "kenn-forge:test-owner",
	})
	t.Cleanup(mgr.Shutdown)

	ctx := context.Background()
	cwd := t.TempDir()
	first, err := mgr.EnsureCommandSessionAndPersist(
		ctx, "scope-1", commandSessionTestSpec("client:key one", cwd),
		persistCommandSessionNoop,
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

	second, err := mgr.EnsureCommandSessionAndPersist(
		ctx, "scope-1", commandSessionTestSpec("client:key one", cwd),
		persistCommandSessionNoop,
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

func TestEnsureCommandSessionAndPersistRejectsKeyOwnedByAnotherScope(t *testing.T) {
	requirePTYAvailable(t)
	require := require.New(t)
	tmuxPath, _, _ := writeCommandSessionFakeTmux(t, "kenn-forge:test-owner")
	mgr := NewManager(Options{
		TmuxCommand:     []string{tmuxPath},
		TmuxOwnerMarker: "kenn-forge:test-owner",
	})
	t.Cleanup(mgr.Shutdown)

	ctx := context.Background()
	_, err := mgr.EnsureCommandSessionAndPersist(
		ctx, "scope-1", commandSessionTestSpec("client:key one", t.TempDir()),
		persistCommandSessionNoop,
	)
	require.NoError(err)

	_, err = mgr.EnsureCommandSessionAndPersist(
		ctx, "scope-2", commandSessionTestSpec("client:key one", t.TempDir()),
		persistCommandSessionNoop,
	)
	require.Error(err)
	require.Contains(err.Error(), "another scope")
}

func TestEnsureCommandSessionAndPersistRequiresCommand(t *testing.T) {
	tmuxPath, _, _ := writeCommandSessionFakeTmux(t, "kenn-forge:test-owner")
	mgr := NewManager(Options{TmuxCommand: []string{tmuxPath}})
	t.Cleanup(mgr.Shutdown)

	_, err := mgr.EnsureCommandSessionAndPersist(
		context.Background(), "scope-1",
		CommandLaunchSpec{SessionKey: "client:key one"},
		persistCommandSessionNoop,
	)
	require.Error(t, err)
	require.Contains(t, err.Error(), "command is required")
}

func TestEnsureCommandSessionAndPersistReattachesToSurvivingTmuxSession(t *testing.T) {
	requirePTYAvailable(t)
	require := require.New(t)
	assert := assert.New(t)
	tmuxPath, recordPath, statePath := writeCommandSessionFakeTmux(
		t, "kenn-forge:test-owner",
	)
	// Simulate a tmux session that survived a manager restart.
	require.NoError(os.WriteFile(statePath, nil, 0o644))
	mgr := NewManager(Options{
		TmuxCommand:     []string{tmuxPath},
		TmuxOwnerMarker: "kenn-forge:test-owner",
	})
	t.Cleanup(mgr.Shutdown)

	info, err := mgr.EnsureCommandSessionAndPersist(
		context.Background(), "scope-1",
		commandSessionTestSpec("client:key one", t.TempDir()),
		persistCommandSessionNoop,
	)
	require.NoError(err)

	sessionName := tmuxSessionName("scope-1", "client:key one")
	assert.Equal(sessionName, info.TmuxSession)
	assert.True(info.Reused,
		"reattaching to a surviving backend must not claim launch ownership")
	for _, record := range readNullArgvRecord(t, recordPath) {
		if len(record) > 0 {
			assert.NotEqual("new-session", record[0])
		}
	}
}

func TestRollbackLaunchPreservesNewerCommandSessionGeneration(t *testing.T) {
	requirePTYAvailable(t)
	require := require.New(t)
	assert := assert.New(t)
	tmuxPath, recordPath, statePath := writeCommandSessionFakeTmux(
		t, "kenn-forge:test-owner",
	)
	mgr := NewManager(Options{
		TmuxCommand:     []string{tmuxPath},
		TmuxOwnerMarker: "kenn-forge:test-owner",
	})
	t.Cleanup(mgr.Shutdown)

	ctx := context.Background()
	cwd := t.TempDir()
	original, err := mgr.EnsureCommandSessionAndPersist(
		ctx, "scope-1", commandSessionTestSpec("client:key one", cwd),
		persistCommandSessionNoop,
	)
	require.NoError(err)
	require.NoError(mgr.Detach(original.WorkspaceID, original.Key))
	require.NoError(os.Remove(statePath))

	replacement, err := mgr.EnsureCommandSessionAndPersist(
		ctx, "scope-1", commandSessionTestSpec("client:key one", cwd),
		persistCommandSessionNoop,
	)
	require.NoError(err)
	require.NotEqual(original.CreatedAt, replacement.CreatedAt)
	require.NoError(mgr.Detach(replacement.WorkspaceID, replacement.Key))
	require.Empty(mgr.ListSessions("scope-1"))

	require.NoError(mgr.RollbackLaunch(ctx, original))

	assert.FileExists(statePath)
	record, err := os.ReadFile(recordPath)
	require.NoError(err)
	assert.NotContains(
		string(record), "kill-session\x00-t\x00"+replacement.TmuxSession,
	)
}

func TestRollbackLaunchPreservesAdoptedCommandSessionBackend(t *testing.T) {
	requirePTYAvailable(t)
	require := require.New(t)
	assert := assert.New(t)
	tmuxPath, recordPath, statePath := writeCommandSessionFakeTmux(
		t, "kenn-forge:test-owner",
	)
	mgr := NewManager(Options{
		TmuxCommand:     []string{tmuxPath},
		TmuxOwnerMarker: "kenn-forge:test-owner",
	})
	t.Cleanup(mgr.Shutdown)

	ctx := context.Background()
	cwd := t.TempDir()
	original, err := mgr.EnsureCommandSessionAndPersist(
		ctx, "scope-1", commandSessionTestSpec("client:key one", cwd),
		persistCommandSessionNoop,
	)
	require.NoError(err)
	require.False(original.Reused)
	require.NoError(mgr.Detach(original.WorkspaceID, original.Key))

	// The backend survived the detach, so the next ensure adopts it instead
	// of creating a new generation.
	adopted, err := mgr.EnsureCommandSessionAndPersist(
		ctx, "scope-1", commandSessionTestSpec("client:key one", cwd),
		persistCommandSessionNoop,
	)
	require.NoError(err)
	require.True(adopted.Reused,
		"a surviving backend must be adopted, not recreated")
	require.NoError(mgr.Detach(adopted.WorkspaceID, adopted.Key))
	require.Empty(mgr.ListSessions("scope-1"))

	require.NoError(mgr.RollbackLaunch(ctx, original))

	assert.FileExists(statePath,
		"a stale creator rollback must not kill an adopted backend")
	record, err := os.ReadFile(recordPath)
	require.NoError(err)
	assert.NotContains(string(record), "kill-session")
}

func TestEnsureCommandSessionAndPersistSerializesSameKeyOwnership(t *testing.T) {
	requirePTYAvailable(t)
	require := require.New(t)
	assert := assert.New(t)
	tmuxPath, recordPath, statePath := writeCommandSessionFakeTmux(
		t, "kenn-forge:test-owner",
	)
	mgr := NewManager(Options{
		TmuxCommand:     []string{tmuxPath},
		TmuxOwnerMarker: "kenn-forge:test-owner",
	})
	t.Cleanup(mgr.Shutdown)

	type ensureResult struct {
		info SessionInfo
		err  error
	}
	ctx := context.Background()
	spec := commandSessionTestSpec("surface:shared", t.TempDir())
	aPersistEntered := make(chan struct{})
	failAPersist := make(chan struct{})
	aResults := make(chan ensureResult, 1)
	persistenceErr := errors.New("metadata write failed")
	go func() {
		info, err := mgr.EnsureCommandSessionAndPersist(
			ctx, "scope-1", spec,
			func(_ context.Context, persisted SessionInfo) error {
				assert.Equal("surface:shared", persisted.Key)
				close(aPersistEntered)
				<-failAPersist
				return persistenceErr
			},
		)
		aResults <- ensureResult{info: info, err: err}
	}()
	<-aPersistEntered
	assert.True(
		mgr.CommandSessionStartLockHeldForTest(spec.SessionKey),
		"A must hold the keyed transaction lock during persistence",
	)

	bPersistEntered := make(chan struct{})
	finishBPersist := make(chan struct{})
	bResults := make(chan ensureResult, 1)
	go func() {
		info, err := mgr.EnsureCommandSessionAndPersist(
			ctx, "scope-1", spec,
			func(_ context.Context, persisted SessionInfo) error {
				assert.Equal("surface:shared", persisted.Key)
				close(bPersistEntered)
				<-finishBPersist
				return nil
			},
		)
		bResults <- ensureResult{info: info, err: err}
	}()
	require.Never(func() bool {
		select {
		case <-bPersistEntered:
			return true
		default:
			return false
		}
	}, 100*time.Millisecond, 10*time.Millisecond)

	close(failAPersist)
	require.Eventually(func() bool {
		select {
		case <-bPersistEntered:
			return true
		default:
			return false
		}
	}, 2*time.Second, 10*time.Millisecond)
	require.Eventually(func() bool { return len(aResults) == 1 }, time.Second, 10*time.Millisecond)
	aResult := <-aResults
	require.Error(aResult.err)
	require.ErrorIs(aResult.err, persistenceErr)
	assert.False(aResult.info.Reused)

	records := readNullArgvRecord(t, recordPath)
	var newSessionIndexes []int
	var killSessionIndexes []int
	for index, record := range records {
		if len(record) == 0 {
			continue
		}
		switch record[0] {
		case "new-session":
			newSessionIndexes = append(newSessionIndexes, index)
		case "kill-session":
			killSessionIndexes = append(killSessionIndexes, index)
		}
	}
	require.Len(newSessionIndexes, 2)
	require.NotEmpty(killSessionIndexes)
	for _, index := range killSessionIndexes {
		assert.Less(index, newSessionIndexes[1],
			"A's compensation must finish before B creates its generation")
	}
	assert.FileExists(statePath)

	close(finishBPersist)
	require.Eventually(func() bool { return len(bResults) == 1 }, time.Second, 10*time.Millisecond)
	bResult := <-bResults
	require.NoError(bResult.err)
	assert.False(bResult.info.Reused)
	assert.NotEqual(aResult.info.tmuxLaunchID, bResult.info.tmuxLaunchID)
	sessions := mgr.ListSessions("scope-1")
	require.Len(sessions, 1)
	assert.Equal(bResult.info.Key, sessions[0].Key)
	assert.Equal(bResult.info.CreatedAt, sessions[0].CreatedAt)
	assert.Equal(bResult.info.TmuxSession, sessions[0].TmuxSession)
	assert.FileExists(statePath)
}

func TestEnsureCommandSessionAndPersistPreservesPersistenceCauseAndRollbackDiagnostics(
	t *testing.T,
) {
	requirePTYAvailable(t)
	require := require.New(t)
	assert := assert.New(t)
	tmuxPath, _, statePath := writeCommandSessionFakeTmux(
		t, "kenn-forge:test-owner",
	)
	require.NoError(os.WriteFile(
		filepath.Join(filepath.Dir(statePath), "tmux-kill-failure"), nil, 0o600,
	))
	mgr := NewManager(Options{
		TmuxCommand:     []string{tmuxPath},
		TmuxOwnerMarker: "kenn-forge:test-owner",
	})
	t.Cleanup(mgr.Shutdown)
	persistenceErr := errors.New("metadata write failed")

	_, err := mgr.EnsureCommandSessionAndPersist(
		context.Background(), "scope-1",
		commandSessionTestSpec("surface:typed-error", t.TempDir()),
		func(context.Context, SessionInfo) error { return persistenceErr },
	)
	require.Error(err)
	var transactionErr *CommandSessionPersistenceError
	require.ErrorAs(err, &transactionErr)
	assert.Same(persistenceErr, transactionErr.PersistenceErr)
	require.Error(transactionErr.RollbackErr)
	assert.Contains(transactionErr.RollbackErr.Error(), "forced kill-session failure")
	require.ErrorIs(err, persistenceErr)
	require.NotErrorIs(err, transactionErr.RollbackErr)
	assert.Equal(persistenceErr.Error(), err.Error())
	assert.NotContains(err.Error(), "forced kill-session failure")
}

func TestEnsureCommandSessionAndPersistRejectsForeignTmuxSessionOwner(t *testing.T) {
	requirePTYAvailable(t)
	require := require.New(t)
	tmuxPath, _, statePath := writeCommandSessionFakeTmux(
		t, "someone-else",
	)
	require.NoError(os.WriteFile(statePath, nil, 0o644))
	mgr := NewManager(Options{
		TmuxCommand:     []string{tmuxPath},
		TmuxOwnerMarker: "kenn-forge:test-owner",
	})
	t.Cleanup(mgr.Shutdown)

	_, err := mgr.EnsureCommandSessionAndPersist(
		context.Background(), "scope-1",
		commandSessionTestSpec("client:key one", t.TempDir()),
		persistCommandSessionNoop,
	)
	require.Error(err)
	require.Contains(err.Error(), "not owned")
}
