package localruntime

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cenkalti/backoff/v5"
	"github.com/creack/pty/v2"
	shellquote "github.com/kballard/go-shellquote"
	Assert "github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/middleman/internal/procutil"
	"go.kenn.io/middleman/internal/ptyowner"
	ptyownerruntime "go.kenn.io/middleman/internal/ptyowner/runtime"
)

func TestMain(m *testing.M) {
	if os.Getenv("MIDDLEMAN_LOCALRUNTIME_HELPER") == "1" {
		os.Exit(m.Run())
	}
	envDir, err := os.MkdirTemp("", "middleman-localruntime-tmux-env-*")
	if err == nil {
		_ = os.Setenv("MIDDLEMAN_TMUX_ENV_DIR", envDir)
	}
	code := m.Run()
	if err == nil {
		_ = os.RemoveAll(envDir)
	}
	os.Exit(code)
}

func requirePTYAvailable(t *testing.T) {
	t.Helper()
	ptmx, tty, err := pty.Open()
	if err != nil {
		t.Skipf("pty unavailable in this test environment: %v", err)
	}
	_ = ptmx.Close()
	_ = tty.Close()
}

func withTestPtyOwnerRuntime(t *testing.T, options Options) Options {
	t.Helper()
	if options.PtyOwnerRuntime != nil {
		return options
	}
	options.PtyOwnerRuntime = ptyownerruntime.New(&ptyowner.Client{
		Root:      filepath.Join(t.TempDir(), "pty-owner"),
		InProcess: true,
	}, nil)
	return options
}

func TestManagerLaunchesIndependentSessionsPerWorkspaceTarget(t *testing.T) {
	requirePTYAvailable(t)
	t.Setenv("MIDDLEMAN_LOCALRUNTIME_HELPER", "1")

	ctx := context.Background()
	mgr := NewManager(withTestPtyOwnerRuntime(t, Options{Targets: []LaunchTarget{
		helperTarget("helper", "sleep"),
	}}))
	t.Cleanup(mgr.Shutdown)

	session1, err := mgr.Launch(ctx, "ws-1", t.TempDir(), "helper")
	require.NoError(t, err)
	session2, err := mgr.Launch(ctx, "ws-1", t.TempDir(), "helper")
	require.NoError(t, err)

	sessions := mgr.ListSessions("ws-1")
	assert := Assert.New(t)
	assert.NotEqual(session1.Key, session2.Key)
	assert.Equal("helper", session1.Label)
	assert.Equal("helper 2", session2.Label)
	assert.Equal(SessionStatusRunning, session1.Status)
	assert.Len(sessions, 2)
}

func TestManagerRenamesSessionMetadata(t *testing.T) {
	requirePTYAvailable(t)
	t.Setenv("MIDDLEMAN_LOCALRUNTIME_HELPER", "1")

	ctx := context.Background()
	mgr := NewManager(withTestPtyOwnerRuntime(t, Options{Targets: []LaunchTarget{
		helperTarget("helper", "sleep"),
	}}))
	t.Cleanup(mgr.Shutdown)

	session, err := mgr.Launch(ctx, "ws-1", t.TempDir(), "helper")
	require.NoError(t, err)

	renamed, err := mgr.RenameSession("ws-1", session.Key, "Review helper")
	require.NoError(t, err)

	assert := Assert.New(t)
	assert.Equal(session.Key, renamed.Key)
	assert.Equal("Review helper", renamed.Label)
	sessions := mgr.ListSessions("ws-1")
	require.Len(t, sessions, 1)
	assert.Equal("Review helper", sessions[0].Label)
}

func TestManagerLaunchConcurrentStartsIndependentProcesses(t *testing.T) {
	requirePTYAvailable(t)
	t.Setenv("MIDDLEMAN_LOCALRUNTIME_HELPER", "1")
	require := require.New(t)
	assert := Assert.New(t)

	ctx := context.Background()
	record := filepath.Join(t.TempDir(), "starts")
	mgr := NewManager(withTestPtyOwnerRuntime(t, Options{Targets: []LaunchTarget{
		{
			Key: "helper", Label: "helper", Kind: LaunchTargetAgent,
			Source: "config", Command: helperRecordCommand(record),
			Available: true,
		},
	}}))
	t.Cleanup(mgr.Shutdown)

	const launches = 12
	var wg sync.WaitGroup
	errs := make(chan error, launches)
	infos := make(chan SessionInfo, launches)
	cwd := t.TempDir()
	for range launches {
		wg.Go(func() {
			info, err := mgr.Launch(ctx, "ws-1", cwd, "helper")
			errs <- err
			infos <- info
		})
	}
	wg.Wait()
	close(errs)
	close(infos)

	for err := range errs {
		require.NoError(err)
	}
	keys := make(map[string]bool)
	labels := make(map[string]bool)
	for info := range infos {
		keys[info.Key] = true
		labels[info.Label] = true
	}
	assert.Len(keys, launches)
	assert.Len(labels, launches)
	assert.True(labels["helper"])
	require.Eventually(func() bool {
		data, err := os.ReadFile(record)
		if err != nil {
			return false
		}
		return strings.Count(string(data), "\n") == launches
	}, 2*time.Second, 20*time.Millisecond)
	assert.Len(mgr.ListSessions("ws-1"), launches)
}

func TestNewSessionKeyUsesWorkspacePrefixAndRandomSuffix(t *testing.T) {
	first, err := NewSessionKey("ws-1")
	require.NoError(t, err)
	second, err := NewSessionKey("ws-1")
	require.NoError(t, err)

	assert := Assert.New(t)
	assert.True(strings.HasPrefix(first, "ws-1_"))
	assert.True(strings.HasPrefix(second, "ws-1_"))
	assert.NotEqual(first, second)
	assert.Len(strings.TrimPrefix(first, "ws-1_"), 16)
}

func TestManagerLaunchUnavailableTarget(t *testing.T) {
	ctx := context.Background()
	mgr := NewManager(Options{Targets: []LaunchTarget{{
		Key: "missing", Label: "Missing", Kind: LaunchTargetAgent,
		Available: false, DisabledReason: "not found",
	}}})
	t.Cleanup(mgr.Shutdown)

	_, err := mgr.Launch(ctx, "ws-1", t.TempDir(), "missing")
	require.Error(t, err)
	require.Contains(t, err.Error(), "not available")
}

func TestManagerLaunchMissingTarget(t *testing.T) {
	ctx := context.Background()
	mgr := NewManager(Options{})
	t.Cleanup(mgr.Shutdown)

	_, err := mgr.Launch(ctx, "ws-1", t.TempDir(), "missing")
	require.Error(t, err)
	require.Contains(t, err.Error(), "target not found")
}

func TestManagerUpdateTargetsAffectsFutureLaunches(t *testing.T) {
	requirePTYAvailable(t)
	t.Setenv("MIDDLEMAN_LOCALRUNTIME_HELPER", "1")
	assert := Assert.New(t)

	ctx := context.Background()
	mgr := NewManager(withTestPtyOwnerRuntime(t, Options{Targets: []LaunchTarget{
		helperTarget("helper", "sleep"),
	}}))
	t.Cleanup(mgr.Shutdown)

	mgr.UpdateTargets([]LaunchTarget{{
		Key: "custom", Label: "Custom", Kind: LaunchTargetAgent,
		Source: "config", Command: helperCommand("sleep"),
		Available: true,
	}})

	_, err := mgr.Launch(ctx, "ws-1", t.TempDir(), "helper")
	require.Error(t, err)
	assert.Contains(err.Error(), "target not found")

	session, err := mgr.Launch(ctx, "ws-1", t.TempDir(), "custom")
	require.NoError(t, err)
	assert.Equal("custom", session.TargetKey)
	assert.Equal("Custom", session.Label)
}

func TestManagerTmuxSessionsReturnsWrappedAgentSessions(t *testing.T) {
	assert := Assert.New(t)
	mgr := NewManager(Options{})
	mgr.sessions["ws-1:codex"] = &session{
		info: SessionInfo{
			Key:         "ws-1:codex",
			WorkspaceID: "ws-1",
			TargetKey:   "codex",
			Kind:        LaunchTargetAgent,
		},
		tmuxSession: "middleman-ws-1-codex",
	}
	mgr.sessions["ws-1:other"] = &session{
		info: SessionInfo{
			Key:         "ws-1:other",
			WorkspaceID: "ws-1",
			TargetKey:   "other",
			Kind:        LaunchTargetAgent,
		},
	}
	mgr.sessions["ws-2:codex"] = &session{
		info: SessionInfo{
			Key:         "ws-2:codex",
			WorkspaceID: "ws-2",
			TargetKey:   "codex",
			Kind:        LaunchTargetAgent,
		},
		tmuxSession: "middleman-ws-2-codex",
	}

	assert.Equal(
		[]string{"middleman-ws-1-codex"},
		mgr.TmuxSessions("ws-1"),
	)
}

func TestStartTmuxAttachSessionKeepsBackingTmuxSession(t *testing.T) {
	requirePTYAvailable(t)
	t.Setenv("MIDDLEMAN_LOCALRUNTIME_HELPER", "1")
	require := require.New(t)
	assert := Assert.New(t)

	info := SessionInfo{
		Key:         "session-1",
		WorkspaceID: "ws-1",
		TargetKey:   "codex",
		Kind:        LaunchTargetAgent,
		TmuxSession: "middleman-ws-1-codex",
	}
	s, err := startTmuxAttachSession(info, helperCommand("sleep"), t.TempDir(), nil)
	require.NoError(err)
	go s.watch()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		require.NoError(s.stop(ctx))
		waitSessionDone(s)
	})

	assert.Equal("middleman-ws-1-codex", s.tmuxSession)
	assert.Equal("middleman-ws-1-codex", s.snapshot().TmuxSession)
}

func TestManagerLaunchCommandWrapsAgentsInTmuxWhenEnabled(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)
	t.Setenv("XDG_RUNTIME_DIR", "argv-visible-value")
	dir := t.TempDir()
	record := filepath.Join(dir, "tmux-record")
	tmuxPath := filepath.Join(dir, "tmux")
	require.NoError(os.WriteFile(tmuxPath, fmt.Appendf(nil, `#!/bin/sh
printf '%%s\0' "$#" "$@" >> %s
if [ "$1" = "has-session" ]; then
  echo "can't find session: $3" >&2
  exit 1
fi
exit 0
`, shellquote.Join(record)), 0o755))
	agent := helperTarget("codex", "sleep")
	agent.Label = "Codex"
	mgr := NewManager(Options{
		Targets: []LaunchTarget{
			agent,
			{
				Key: "shell", Label: "Shell", Kind: LaunchTargetShell,
				Source: "system", Command: []string{tmuxPath},
				Available: true,
			},
		},
		TmuxCommand:             []string{tmuxPath},
		WrapAgentSessionsInTmux: true,
	})
	t.Cleanup(mgr.Shutdown)

	launch, err := mgr.launchCommand(
		context.Background(), agent, "ws:alpha", "/tmp/work tree",
	)
	require.NoError(err)
	sessionName := tmuxSessionName("ws:alpha", "codex")

	assert.Equal([]string{tmuxPath, "attach-session", "-t", sessionName}, launch.Command)
	assert.Equal(sessionName, launch.TmuxSession)
	records := readNullArgvRecord(t, record)
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
	assert.Contains(newSession, "-E")
	assert.NotContains(newSession, "-e")
	assert.Contains(newSession, "-c")
	assert.Contains(newSession, "/tmp/work tree")
	assert.Contains(newSessionText, "__middleman_env_file=")
	assert.Contains(newSessionText, "exec env -i")
	assert.Contains(newSessionText, `XDG_RUNTIME_DIR="${XDG_RUNTIME_DIR-}"`)
	assert.Contains(newSessionText, shellquote.Join(agent.Command[0]))
	assert.NotContains(newSessionText, "argv-visible-value")
}

func TestManagerLaunchCommandResolvesTmuxBeforeEmbeddingScript(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)
	dir := t.TempDir()
	binDir := filepath.Join(dir, "bin")
	require.NoError(os.MkdirAll(binDir, 0o755))
	tmuxPath := filepath.Join(binDir, "tmux")
	require.NoError(os.WriteFile(tmuxPath, []byte(`#!/bin/sh
if [ "$1" = "has-session" ]; then
  echo "can't find session: $3" >&2
  exit 1
fi
exit 0
`), 0o755))
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	agent := helperTarget("codex", "sleep")
	mgr := NewManager(Options{
		Targets: []LaunchTarget{
			agent,
			{
				Key: "shell", Label: "Shell", Kind: LaunchTargetShell,
				Source: "system", Command: []string{"tmux"},
				Available: true,
			},
		},
		TmuxCommand:             []string{"tmux"},
		WrapAgentSessionsInTmux: true,
	})
	t.Cleanup(mgr.Shutdown)

	launch, err := mgr.launchCommand(context.Background(), agent, "ws-1", t.TempDir())
	require.NoError(err)

	assert.Equal(tmuxPath, launch.Command[0])
	assert.Equal("attach-session", launch.Command[1])
}

func TestManagerLaunchCommandRejectsRelativeTmuxCommandWhenWrapped(t *testing.T) {
	agent := helperTarget("codex", "sleep")
	mgr := NewManager(Options{
		Targets: []LaunchTarget{
			agent,
			{
				Key: "shell", Label: "Shell", Kind: LaunchTargetShell,
				Source: "system", Command: []string{"./tmux"},
				Available: true,
			},
		},
		TmuxCommand:             []string{"./tmux"},
		WrapAgentSessionsInTmux: true,
	})
	t.Cleanup(mgr.Shutdown)

	_, err := mgr.launchCommand(context.Background(), agent, "ws-1", t.TempDir())

	require.Error(t, err)
	require.Contains(t, err.Error(), "resolve tmux command")
	require.Contains(t, err.Error(), "relative paths")
}

func TestManagerLaunchCommandMarksWrappedAgentTmuxSession(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	dir := t.TempDir()
	record := filepath.Join(dir, "tmux-record")
	tmuxPath := filepath.Join(dir, "tmux")
	require.NoError(os.WriteFile(tmuxPath, fmt.Appendf(nil, `#!/bin/sh
printf '%%s\0' "$#" "$@" >> %s
if [ "$1" = "has-session" ]; then
  echo "can't find session: $3" >&2
  exit 1
fi
exit 0
`, shellquote.Join(record)), 0o755))
	agent := helperTarget("codex", "sleep")
	mgr := NewManager(Options{
		Targets: []LaunchTarget{
			agent,
			{
				Key: "shell", Label: "Shell", Kind: LaunchTargetShell,
				Source: "system", Command: []string{tmuxPath},
				Available: true,
			},
		},
		TmuxCommand:             []string{tmuxPath},
		TmuxOwnerMarker:         "middleman:test-owner",
		WrapAgentSessionsInTmux: true,
	})
	t.Cleanup(mgr.Shutdown)

	launch, err := mgr.launchCommand(context.Background(), agent, "ws-1", "/tmp/work tree")
	require.NoError(err)
	sessionName := tmuxSessionName("ws-1", "codex")

	assert.Equal([]string{tmuxPath, "attach-session", "-t", sessionName}, launch.Command)
	assert.Equal(sessionName, launch.TmuxSession)
	records := readNullArgvRecord(t, record)
	require.Len(records, 2)
	newSession := records[1]
	assert.Contains(newSession, ";")
	assert.Contains(newSession, "set-option")
	assert.Contains(newSession, "-t")
	assert.Contains(newSession, sessionName)
	assert.Contains(newSession, "@middleman_owner")
	assert.Contains(newSession, "middleman:test-owner")
}

func TestManagerLaunchPlainShellWrapsInTmuxWhenAvailable(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)
	t.Setenv("XDG_RUNTIME_DIR", "argv-visible-value")
	t.Setenv("MIDDLEMAN_TEST_CUSTOM_SHELL_ENV", "custom-visible-value")
	dir := t.TempDir()
	record := filepath.Join(dir, "tmux-record")
	tmuxPath := filepath.Join(dir, "tmux")
	require.NoError(os.WriteFile(tmuxPath, fmt.Appendf(nil, `#!/bin/sh
printf '%%s\0' "$#" "$@" >> %s
if [ "$1" = "attach-session" ]; then
  trap 'exit 0' HUP INT TERM
  while :; do sleep 1; done
fi
if [ "$1" = "has-session" ]; then
  echo "can't find session: $3" >&2
  exit 1
fi
exit 0
`, shellquote.Join(record)), 0o755))
	mgr := NewManager(Options{
		Targets: []LaunchTarget{{
			Key: "shell", Label: "Shell", Kind: LaunchTargetShell,
			Source: "system", Command: []string{tmuxPath},
			Available: true,
		}, plainShellTarget()},
		ShellCommand:    helperCommand("sleep"),
		TmuxCommand:     []string{tmuxPath},
		TmuxOwnerMarker: "middleman:test-owner",
	})
	t.Cleanup(mgr.Shutdown)

	session, err := mgr.Launch(
		context.Background(), "ws:alpha", "/tmp/work tree",
		string(LaunchTargetPlainShell),
	)
	require.NoError(err)
	sessionName := tmuxSessionName("ws:alpha", session.Key)

	assert.Equal(sessionName, session.TmuxSession)
	assert.Equal(string(LaunchTargetPlainShell), session.TargetKey)
	assert.Equal("Shell", session.Label)
	records := readNullArgvRecord(t, record)
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
	assert.Contains(newSession, "/tmp/work tree")
	assert.Contains(newSessionText, "exec env -i")
	assert.Contains(newSessionText, shellquote.Join(os.Args[0]))
	assert.Contains(
		newSessionText,
		"XDG_RUNTIME_DIR=\"${XDG_RUNTIME_DIR-}\"",
	)
	assert.Contains(
		newSessionText,
		"MIDDLEMAN_TEST_CUSTOM_SHELL_ENV=\"${MIDDLEMAN_TEST_CUSTOM_SHELL_ENV-}\"",
	)
	assert.Contains(newSession, "-E")
	assert.NotContains(newSession, "-e")
	assert.Contains(newSessionText, "__middleman_env_file=")
	assert.Contains(newSession, ";")
	assert.Contains(newSession, "set-option")
	assert.Contains(newSession, "-t")
	assert.Contains(newSession, sessionName)
	assert.Contains(newSession, "@middleman_owner")
	assert.Contains(newSession, "middleman:test-owner")
	assert.NotContains(newSessionText, "argv-visible-value")
	assert.NotContains(newSessionText, "custom-visible-value")
	assert.Len(mgr.ListSessions("ws:alpha"), 1)
}

func TestManagerRestoreTmuxSessionRestoresPlainShellRuntimeSession(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)
	tmuxPath := writeLongRunningAttachTmux(t)
	mgr := NewManager(Options{
		TmuxCommand: []string{tmuxPath},
	})
	t.Cleanup(mgr.Shutdown)
	createdAt := time.Date(2026, 5, 28, 12, 0, 0, 0, time.UTC)

	err := mgr.RestoreRuntimeSessions(context.Background(), []RestoredRuntimeSession{{
		WorkspaceID: "ws-1",
		SessionKey:  "ws-1_shell-restored",
		TmuxSession: "middleman-ws-1-shell",
		TargetKey:   string(LaunchTargetPlainShell),
		CreatedAt:   createdAt,
	}})
	require.NoError(err)

	sessions := mgr.ListSessions("ws-1")
	require.Len(sessions, 1)
	shell := sessions[0]
	assert.Equal("ws-1_shell-restored", shell.Key)
	assert.Equal(string(LaunchTargetPlainShell), shell.TargetKey)
	assert.Equal(LaunchTargetPlainShell, shell.Kind)
	assert.Equal("Shell", shell.Label)
	assert.Equal("middleman-ws-1-shell", shell.TmuxSession)
	assert.Equal(createdAt, shell.CreatedAt)
}

func TestManagerRestoreTmuxSessionReusesExistingPlainShellRuntimeSession(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)
	tmuxPath := writeLongRunningAttachTmux(t)
	mgr := NewManager(Options{
		TmuxCommand: []string{tmuxPath},
	})
	t.Cleanup(mgr.Shutdown)
	restored := RestoredRuntimeSession{
		WorkspaceID: "ws-1",
		SessionKey:  "ws-1_shell-restored",
		TmuxSession: "middleman-ws-1-shell",
		TargetKey:   string(LaunchTargetPlainShell),
		CreatedAt:   time.Date(2026, 5, 28, 12, 0, 0, 0, time.UTC),
	}

	require.NoError(mgr.RestoreRuntimeSessions(
		context.Background(), []RestoredRuntimeSession{restored},
	))
	require.NoError(mgr.RestoreRuntimeSessions(
		context.Background(), []RestoredRuntimeSession{restored},
	))

	assert.Len(mgr.ListSessions("ws-1"), 1)
}

func TestManagerRestorePtyOwnerSessionIgnoresRemovedTarget(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)
	sessionKey := "ws-1_removedtarget"
	owner := newFakeRuntimePtyOwner()
	owner.startedSession = sessionKey
	owner.startedPTY = &fakeRuntimePTY{
		output: make(chan []byte, 64),
		done:   make(chan struct{}),
	}
	mgr := NewManager(Options{
		PtyOwnerRuntime: owner,
	})
	t.Cleanup(mgr.Shutdown)
	createdAt := time.Date(2026, 5, 28, 12, 0, 0, 0, time.UTC)

	err := mgr.RestoreRuntimeSessions(context.Background(), []RestoredRuntimeSession{{
		WorkspaceID: "ws-1",
		SessionKey:  sessionKey,
		TargetKey:   "removed-agent",
		Label:       "Removed Agent",
		Kind:        LaunchTargetAgent,
		CWD:         t.TempDir(),
		CreatedAt:   createdAt,
	}})
	require.NoError(err)

	sessions := mgr.ListSessions("ws-1")
	require.Len(sessions, 1)
	assert.Equal(0, owner.starts)
	assert.Equal(1, owner.attaches)
	assert.Equal(sessionKey, sessions[0].Key)
	assert.Equal("removed-agent", sessions[0].TargetKey)
	assert.Equal("Removed Agent", sessions[0].Label)
	assert.Equal(LaunchTargetAgent, sessions[0].Kind)
	assert.Equal(createdAt, sessions[0].CreatedAt)
}

func TestManagerRestorePtyOwnerSessionRetriesAttach(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)
	sessionKey := "ws-1_retry-attach"
	owner := newFakeRuntimePtyOwner()
	owner.startedSession = sessionKey
	owner.startedPTY = &fakeRuntimePTY{
		output: make(chan []byte, 64),
		done:   make(chan struct{}),
	}
	owner.attachErrs = []error{
		errors.New("owner socket not ready"),
		errors.New("owner still starting"),
	}
	previousBackOff := newPtyOwnerAttachBackOff
	newPtyOwnerAttachBackOff = func() backoff.BackOff {
		expo := backoff.NewExponentialBackOff()
		expo.InitialInterval = time.Millisecond
		expo.MaxInterval = time.Millisecond
		expo.RandomizationFactor = 0
		return expo
	}
	t.Cleanup(func() { newPtyOwnerAttachBackOff = previousBackOff })
	mgr := NewManager(Options{
		PtyOwnerRuntime: owner,
	})
	t.Cleanup(mgr.Shutdown)

	err := mgr.RestoreRuntimeSessions(context.Background(), []RestoredRuntimeSession{{
		WorkspaceID: "ws-1",
		SessionKey:  sessionKey,
		TargetKey:   "helper",
		Label:       "Helper",
		Kind:        LaunchTargetAgent,
		CWD:         t.TempDir(),
		CreatedAt:   time.Date(2026, 5, 28, 12, 0, 0, 0, time.UTC),
	}})
	require.NoError(err)

	sessions := mgr.ListSessions("ws-1")
	require.Len(sessions, 1)
	assert.Equal(3, owner.attaches)
	assert.Equal(sessionKey, sessions[0].Key)
	assert.Equal(SessionStatusRunning, sessions[0].Status)
}

func TestManagerRestorePtyOwnerAttachFailureIsUnavailable(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)
	sessionKey := "ws-1_unavailable-attach"
	owner := newFakeRuntimePtyOwner()
	owner.startedSession = sessionKey
	owner.startedPTY = &fakeRuntimePTY{
		output: make(chan []byte, 64),
		done:   make(chan struct{}),
	}
	owner.attachErrs = []error{
		errors.New("owner socket not ready"),
		errors.New("owner still starting"),
		errors.New("owner still absent"),
		errors.New("owner gone"),
	}
	previousBackOff := newPtyOwnerAttachBackOff
	newPtyOwnerAttachBackOff = func() backoff.BackOff {
		expo := backoff.NewExponentialBackOff()
		expo.InitialInterval = time.Millisecond
		expo.MaxInterval = time.Millisecond
		expo.RandomizationFactor = 0
		return expo
	}
	t.Cleanup(func() { newPtyOwnerAttachBackOff = previousBackOff })
	mgr := NewManager(Options{
		PtyOwnerRuntime: owner,
	})
	t.Cleanup(mgr.Shutdown)

	err := mgr.RestoreRuntimeSessions(context.Background(), []RestoredRuntimeSession{{
		WorkspaceID: "ws-1",
		SessionKey:  sessionKey,
		TargetKey:   "helper",
		Label:       "Helper",
		Kind:        LaunchTargetAgent,
		CWD:         t.TempDir(),
		CreatedAt:   time.Date(2026, 5, 28, 12, 0, 0, 0, time.UTC),
	}})
	require.ErrorIs(err, ErrSessionUnavailable)
	require.NotErrorIs(err, ErrSessionNotFound)
	assert.Equal(4, owner.attaches)
	assert.Empty(mgr.ListSessions("ws-1"))
}

func TestManagerRestoreTmuxSessionAttachesStoredSessionWithoutOwnerValidation(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)

	dir := t.TempDir()
	tmuxPath := filepath.Join(dir, "tmux")
	require.NoError(os.WriteFile(tmuxPath, []byte(`#!/bin/sh
if [ "$1" = "show-options" ]; then
  exit 99
fi
if [ "$1" = "attach-session" ]; then
  trap 'exit 0' HUP INT TERM
  while :; do sleep 1; done
fi
exit 0
`), 0o755))
	mgr := NewManager(Options{
		TmuxCommand:     []string{tmuxPath},
		TmuxOwnerMarker: "middleman:test-owner",
	})
	t.Cleanup(mgr.Shutdown)

	err := mgr.RestoreRuntimeSessions(context.Background(), []RestoredRuntimeSession{{
		WorkspaceID: "ws-1",
		SessionKey:  "ws-1_shell-restored",
		TmuxSession: "middleman-ws-1-shell",
		TargetKey:   string(LaunchTargetPlainShell),
		CreatedAt:   time.Date(2026, 5, 28, 12, 0, 0, 0, time.UTC),
	}})

	require.NoError(err)
	assert.Len(mgr.ListSessions("ws-1"), 1)
}

func TestManagerRestoreTmuxSessionUnavailableWhenCommandCannotResolve(
	t *testing.T,
) {
	require := require.New(t)

	mgr := NewManager(Options{
		TmuxCommand:     []string{"/missing/middleman-test-tmux"},
		TmuxOwnerMarker: "middleman:test-owner",
	})
	t.Cleanup(mgr.Shutdown)

	err := mgr.RestoreRuntimeSessions(context.Background(), []RestoredRuntimeSession{{
		WorkspaceID: "ws-1",
		SessionKey:  "ws-1_shell-restored",
		TmuxSession: "middleman-ws-1-shell",
		TargetKey:   string(LaunchTargetPlainShell),
		CreatedAt:   time.Date(2026, 5, 28, 12, 0, 0, 0, time.UTC),
	}})

	require.ErrorIs(err, ErrSessionUnavailable)
}

func TestTmuxSessionNameUsesOpaqueTargetHash(t *testing.T) {
	assert := Assert.New(t)

	fooSlash := tmuxSessionName("ws:alpha", "foo/bar")
	fooColon := tmuxSessionName("ws:alpha", "foo:bar")

	assert.NotEqual(fooSlash, fooColon)
	assert.NotContains(fooSlash, "foo")
	assert.NotContains(fooSlash, "/")
	assert.NotContains(fooColon, ":")
	assert.Contains(fooSlash, "middleman-ws-alpha-")
}

func TestManagerLaunchCommandFailsWhenOwnerMarkingFailsDuringCreate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("tmux owner shell wrapper uses Unix shell semantics")
	}

	assert := Assert.New(t)
	require := require.New(t)
	dir := t.TempDir()
	record := filepath.Join(dir, "record")
	tmuxPath := filepath.Join(dir, "tmux-fails-owner-create")
	require.NoError(os.WriteFile(tmuxPath, fmt.Appendf(nil, `#!/bin/sh
printf '%%s\0' "$@" >> %s
case "$1" in
  has-session)
    echo "can't find session: $3" >&2
    exit 1
    ;;
  new-session)
    for a in "$@"; do
      if [ "$a" = "@middleman_owner" ]; then
        exit 42
      fi
    done
    exit 0
    ;;
  kill-session)
    exit 0
    ;;
esac
exit 0
`, shellquote.Join(record)), 0o755))

	agent := helperTarget("codex", "sleep")
	mgr := NewManager(Options{
		Targets: []LaunchTarget{
			agent,
			{
				Key: "shell", Label: "Shell", Kind: LaunchTargetShell,
				Source: "system", Command: []string{tmuxPath},
				Available: true,
			},
		},
		TmuxCommand:             []string{tmuxPath},
		TmuxOwnerMarker:         "middleman:test-owner",
		WrapAgentSessionsInTmux: true,
	})
	t.Cleanup(mgr.Shutdown)

	_, err := mgr.launchCommand(context.Background(), agent, "ws-1", t.TempDir())
	require.Error(err)
	data, err := os.ReadFile(record)
	require.NoError(err)
	recorded := string(data)
	assert.Contains(recorded, "new-session")
	assert.Contains(recorded, "@middleman_owner")
	assert.NotContains(recorded, "kill-session")
}

func TestManagerLaunchCommandDoesNotKillSessionWhenTmuxCreateFails(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("tmux owner shell wrapper uses Unix shell semantics")
	}

	assert := Assert.New(t)
	require := require.New(t)
	dir := t.TempDir()
	record := filepath.Join(dir, "record")
	tmuxPath := filepath.Join(dir, "tmux-fails-new-session")
	require.NoError(os.WriteFile(tmuxPath, fmt.Appendf(nil, `#!/bin/sh
printf '%%s\0' "$@" >> %s
case "$1" in
  has-session)
    echo "can't find session: $3" >&2
    exit 1
    ;;
  new-session)
    exit 42
    ;;
  kill-session)
    exit 0
    ;;
esac
exit 0
`, shellquote.Join(record)), 0o755))

	agent := helperTarget("codex", "sleep")
	mgr := NewManager(Options{
		Targets: []LaunchTarget{
			agent,
			{
				Key: "shell", Label: "Shell", Kind: LaunchTargetShell,
				Source: "system", Command: []string{tmuxPath},
				Available: true,
			},
		},
		TmuxCommand:             []string{tmuxPath},
		TmuxOwnerMarker:         "middleman:test-owner",
		WrapAgentSessionsInTmux: true,
	})
	t.Cleanup(mgr.Shutdown)

	_, err := mgr.launchCommand(context.Background(), agent, "ws-1", t.TempDir())
	require.Error(err)
	data, err := os.ReadFile(record)
	require.NoError(err)
	recorded := string(data)
	assert.Contains(recorded, "new-session")
	assert.NotContains(recorded, "kill-session")
}

func TestManagerLaunchCommandRejectsRelativeAgentCommandWhenWrapped(t *testing.T) {
	agent := helperTarget("codex", "sleep")
	agent.Command = []string{"./codex"}
	mgr := NewManager(Options{
		Targets: []LaunchTarget{
			agent,
			{
				Key: "shell", Label: "Shell", Kind: LaunchTargetShell,
				Source: "system", Command: []string{"/usr/bin/tmux"},
				Available: true,
			},
		},
		TmuxCommand:             []string{"/usr/bin/tmux"},
		WrapAgentSessionsInTmux: true,
	})
	t.Cleanup(mgr.Shutdown)

	_, err := mgr.launchCommand(context.Background(), agent, "ws-1", t.TempDir())

	require.Error(t, err)
	require.Contains(t, err.Error(), "absolute path")
}

func TestManagerLaunchCommandDoesNotEmbedEnvForWrappedAgent(t *testing.T) {
	t.Setenv("MIDDLEMAN_GITHUB_TOKEN", "secret-token")
	t.Setenv("CONTEXT7_API_KEY", "context7-secret")
	t.Setenv("XDG_RUNTIME_DIR", "not-carried")
	assert := Assert.New(t)
	resolvedShell, err := resolveExecutable("sh")
	require.NoError(t, err)

	paneCommand := tmuxAgentEnvPolicy.paneEnvironment(
		os.Environ(), []string{resolvedShell, "-c", "echo ok"}, nil,
	).paneCommand
	assert.Contains(paneCommand, "exec ")
	assert.Contains(paneCommand, "env -i")
	assert.Contains(paneCommand, shellquote.Join(resolvedShell))
	assert.Contains(
		paneCommand,
		"XDG_RUNTIME_DIR=\"${XDG_RUNTIME_DIR-}\"",
	)
	assert.NotContains(paneCommand, "TERM=xterm-256color")
	assert.NotContains(paneCommand, "secret-token")
	assert.NotContains(paneCommand, "context7-secret")
	assert.NotContains(paneCommand, "not-carried")
}

func TestTmuxLauncherCopiesClientEnvWithoutGlobalUpdateEnvironment(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("tmux environment handoff uses Unix tmux")
	}
	tmuxPath, err := exec.LookPath("tmux")
	if err != nil {
		t.Skipf("tmux unavailable in this test environment: %v", err)
	}

	require := require.New(t)
	t.Setenv("XDG_RUNTIME_DIR", "client-visible-value")
	t.Setenv("MIDDLEMAN_GITHUB_TOKEN", "client-secret")
	t.Setenv("MIDDLEMAN_STRIPPED_ENV", "client-stripped")

	dir, err := os.MkdirTemp("/tmp", "middleman-tmux-env-*")
	require.NoError(err)
	t.Cleanup(func() {
		_ = os.RemoveAll(dir)
	})
	socket := filepath.Join(dir, "tmux.sock")
	output := filepath.Join(dir, "env-output")
	seed := "middleman-seed-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	sessionName := "middleman-test-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	t.Cleanup(func() {
		_ = procutil.Command(
			tmuxPath, "-f", "/dev/null", "-S", socket, "kill-server",
		).Run()
	})

	seedCmd := procutil.Command(
		tmuxPath, "-f", "/dev/null", "-S", socket,
		"new-session", "-d", "-s", seed, "sleep 10",
	)
	seedCmd.Env = append(
		sessionEnvironment(os.Environ(), []string{"MIDDLEMAN_STRIPPED_ENV"}),
		"XDG_RUNTIME_DIR=server-visible-value",
		"MIDDLEMAN_GITHUB_TOKEN=server-secret",
		"MIDDLEMAN_STRIPPED_ENV=server-stripped",
		"TERM=xterm-256color",
	)
	runOutput, err := seedCmd.CombinedOutput()
	require.NoError(err, string(runOutput))

	printCommand := fmt.Sprintf(
		"printf '%%s\\n%%s\\n%%s\\n' "+
			"\"$XDG_RUNTIME_DIR\" "+
			"\"${MIDDLEMAN_GITHUB_TOKEN-unset}\" "+
			"\"${MIDDLEMAN_STRIPPED_ENV-unset}\" > %s",
		shellquote.Join(output),
	)
	paneEnv := tmuxAgentEnvPolicy.paneEnvironment(
		os.Environ(),
		[]string{"/bin/sh", "-c", printCommand},
		[]string{"MIDDLEMAN_STRIPPED_ENV"},
	)
	paneCommand := paneEnv.paneCommand
	require.NotContains(paneCommand, "client-visible-value")
	require.NotContains(paneCommand, "client-secret")
	require.NotContains(paneCommand, "client-stripped")
	require.NotContains(paneCommand, "server-visible-value")
	require.NotContains(paneCommand, "server-secret")
	require.NotContains(paneCommand, "server-stripped")

	tmuxCommand := []string{tmuxPath, "-f", "/dev/null", "-S", socket}
	_, err = tmuxLauncher{
		TmuxCommand: tmuxCommand,
		Session:     sessionName,
		Pane:        paneEnv,
	}.prepare(context.Background())
	require.NoError(err)

	require.Eventually(func() bool {
		_, err := os.Stat(output)
		return err == nil
	}, 2*time.Second, 20*time.Millisecond)
	data, err := os.ReadFile(output)
	require.NoError(err)
	require.Equal("client-visible-value\nunset\nunset\n", string(data))

	cmd := procutil.Command(
		tmuxPath, "-f", "/dev/null", "-S", socket,
		"show-option", "-gqv", "update-environment",
	)
	globalEnv, err := cmd.CombinedOutput()
	require.NoError(err, string(globalEnv))
	require.NotContains(string(globalEnv), "XDG_RUNTIME_DIR")
}

func TestManagerLaunchCommandFallsBackWhenTmuxUnavailable(t *testing.T) {
	assert := Assert.New(t)
	agent := helperTarget("codex", "sleep")
	mgr := NewManager(Options{
		Targets: []LaunchTarget{
			agent,
			{
				Key: "shell", Label: "Shell", Kind: LaunchTargetShell,
				Source: "system", Command: []string{"tmux"},
				Available: false, DisabledReason: "tmux not found",
			},
		},
		TmuxCommand:             []string{"tmux"},
		WrapAgentSessionsInTmux: true,
	})
	t.Cleanup(mgr.Shutdown)

	launch, err := mgr.launchCommand(context.Background(), agent, "ws-1", t.TempDir())
	require.NoError(t, err)

	assert.Equal(agent.Command, launch.Command)
	assert.Empty(launch.TmuxSession)
}

func TestManagerLaunchUsesPtyOwnerWhenConfigured(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)
	ctx := context.Background()
	backend := newFakeRuntimePtyOwner()
	agent := helperTarget("codex", "exit")
	mgr := NewManager(Options{
		Targets:         []LaunchTarget{agent},
		PtyOwnerRuntime: backend,
	})
	t.Cleanup(mgr.Shutdown)

	info, err := mgr.Launch(ctx, "ws-1", t.TempDir(), "codex")
	require.NoError(err)

	assert.Equal(SessionStatusRunning, info.Status)
	assert.Equal(info.Key, backend.startedSession)
	assert.NotContains(backend.startedSession, ":")
	assert.Equal(agent.Command, backend.startedCommand)
	assert.Len(mgr.ListSessions("ws-1"), 1)
}

func TestManagerLaunchPassesStripEnvVarsToPtyOwner(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)
	ctx := context.Background()
	backend := newFakeRuntimePtyOwner()
	agent := helperTarget("codex", "exit")
	mgr := NewManager(Options{
		Targets:         []LaunchTarget{agent},
		PtyOwnerRuntime: backend,
		StripEnvVars:    []string{"WORKSPACE_TOKEN", "WORKSPACE_TOKEN"},
	})
	t.Cleanup(mgr.Shutdown)

	_, err := mgr.Launch(ctx, "ws-1", t.TempDir(), "codex")
	require.NoError(err)

	assert.Equal([]string{"WORKSPACE_TOKEN"}, backend.startedStripEnvVars)
}

func TestManagerUpdateStripEnvVarsPreservesPreviousNamesForFutureLaunches(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)
	ctx := context.Background()
	backend := newFakeRuntimePtyOwner()
	agent := helperTarget("codex", "exit")
	mgr := NewManager(Options{
		Targets:         []LaunchTarget{agent},
		PtyOwnerRuntime: backend,
		StripEnvVars:    []string{"OLD_TOKEN"},
	})
	t.Cleanup(mgr.Shutdown)

	mgr.UpdateStripEnvVars([]string{"NEW_TOKEN", "NEW_TOKEN"})
	_, err := mgr.Launch(ctx, "ws-1", t.TempDir(), "codex")
	require.NoError(err)

	assert.Equal([]string{"OLD_TOKEN", "NEW_TOKEN"}, backend.startedStripEnvVars)
}

func TestManagerUpdateTargetsAndStripEnvVarsPreservesPreviousNames(t *testing.T) {
	assert := Assert.New(t)
	oldAgent := helperTarget("old", "exit")
	newAgent := helperTarget("new", "exit")
	mgr := NewManager(Options{
		Targets:      []LaunchTarget{oldAgent},
		StripEnvVars: []string{"OLD_TOKEN"},
	})
	t.Cleanup(mgr.Shutdown)

	mgr.UpdateTargetsAndStripEnvVars(
		[]LaunchTarget{newAgent},
		[]string{"NEW_TOKEN", "NEW_TOKEN"},
	)

	targets := mgr.LaunchTargets()
	assert.Len(targets, 1)
	assert.Equal("new", targets[0].Key)
	assert.Equal([]string{"OLD_TOKEN", "NEW_TOKEN"}, mgr.currentStripEnvVars())
}

func TestManagerLaunchTargetsHideInternalShellTarget(t *testing.T) {
	mgr := NewManager(Options{Targets: []LaunchTarget{
		{
			Key: "shell", Label: "Shell", Kind: LaunchTargetShell,
			Source: "system", Command: []string{"tmux"}, Available: true,
		},
		plainShellTarget(),
		helperTarget("codex", "sleep"),
	}})
	t.Cleanup(mgr.Shutdown)

	targets := mgr.LaunchTargets()
	assert := Assert.New(t)
	assert.Len(targets, 2)
	assert.Equal(string(LaunchTargetPlainShell), targets[0].Key)
	assert.Equal("codex", targets[1].Key)
}

func TestManagerLaunchCommandDoesNotWrapWhenConfigDisabled(t *testing.T) {
	assert := Assert.New(t)
	agent := helperTarget("codex", "sleep")
	mgr := NewManager(Options{
		Targets: []LaunchTarget{
			agent,
			{
				Key: "shell", Label: "Shell", Kind: LaunchTargetShell,
				Source: "system", Command: []string{"/usr/bin/tmux"},
				Available: true,
			},
		},
		TmuxCommand:             []string{"/usr/bin/tmux"},
		WrapAgentSessionsInTmux: false,
	})
	t.Cleanup(mgr.Shutdown)

	launch, err := mgr.launchCommand(context.Background(), agent, "ws-1", t.TempDir())
	require.NoError(t, err)

	assert.Equal(agent.Command, launch.Command)
	assert.Empty(launch.TmuxSession)
}

func TestManagerStopReportsTmuxCleanupFailure(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)
	tmuxPath := filepath.Join(t.TempDir(), "tmux-fails")
	require.NoError(os.WriteFile(
		tmuxPath,
		[]byte("#!/bin/sh\nexit 42\n"),
		0o755,
	))
	done := make(chan struct{})
	close(done)
	mgr := NewManager(Options{TmuxCommand: []string{tmuxPath}})
	mgr.sessions["ws-1:codex"] = &session{
		info: SessionInfo{
			Key:         "ws-1:codex",
			WorkspaceID: "ws-1",
			TargetKey:   "codex",
			Kind:        LaunchTargetAgent,
		},
		cmd:         &exec.Cmd{},
		tmuxSession: "middleman-ws-1-codex",
		done:        done,
	}

	err := mgr.Stop(context.Background(), "ws-1", "ws-1:codex")

	require.Error(err)
	require.Contains(err.Error(), "kill tmux session")
	assert.Empty(mgr.ListSessions("ws-1"))
}

func TestManagerStopFailedTmuxCleanupDoesNotSuppressExitCleanup(t *testing.T) {
	requirePTYAvailable(t)
	t.Setenv("MIDDLEMAN_LOCALRUNTIME_HELPER", "1")
	require := require.New(t)
	assert := Assert.New(t)

	tmuxPath := filepath.Join(t.TempDir(), "tmux-fails")
	require.NoError(os.WriteFile(
		tmuxPath,
		[]byte("#!/bin/sh\nexit 42\n"),
		0o755,
	))
	ctx := context.Background()
	mgr := NewManager(withTestPtyOwnerRuntime(t, Options{
		Targets: []LaunchTarget{
			helperTarget("helper", "sleep"),
		},
		TmuxCommand: []string{tmuxPath},
	}))
	t.Cleanup(mgr.Shutdown)

	info, err := mgr.Launch(ctx, "ws-1", t.TempDir(), "helper")
	require.NoError(err)

	mgr.mu.Lock()
	mgr.sessions[info.Key].tmuxSession = "middleman-ws-1-helper"
	mgr.mu.Unlock()

	err = mgr.Stop(ctx, "ws-1", info.Key)

	require.Error(err)
	require.Contains(err.Error(), "kill tmux session")
	assert.Eventually(func() bool {
		return len(mgr.ListSessions("ws-1")) == 0
	}, 2*time.Second, 20*time.Millisecond)
}

func TestManagerStopIgnoresAbsentTmuxSession(t *testing.T) {
	tmuxPath := filepath.Join(t.TempDir(), "tmux-absent")
	require.NoError(t, os.WriteFile(
		tmuxPath,
		[]byte("#!/bin/sh\necho \"can't find session: nope\" >&2\nexit 1\n"),
		0o755,
	))
	done := make(chan struct{})
	close(done)
	mgr := NewManager(Options{TmuxCommand: []string{tmuxPath}})
	mgr.sessions["ws-1:codex"] = &session{
		info: SessionInfo{
			Key:         "ws-1:codex",
			WorkspaceID: "ws-1",
			TargetKey:   "codex",
			Kind:        LaunchTargetAgent,
		},
		cmd:         &exec.Cmd{},
		tmuxSession: "middleman-ws-1-codex",
		done:        done,
	}

	err := mgr.Stop(context.Background(), "ws-1", "ws-1:codex")

	require.NoError(t, err)
}

func TestManagerShutdownLeavesTmuxSessionsRunning(t *testing.T) {
	requirePTYAvailable(t)
	t.Setenv("MIDDLEMAN_LOCALRUNTIME_HELPER", "1")

	require := require.New(t)
	assert := Assert.New(t)
	dir := t.TempDir()
	record := filepath.Join(dir, "record")
	tmuxPath := filepath.Join(dir, "tmux-records")
	require.NoError(os.WriteFile(
		tmuxPath,
		[]byte("#!/bin/sh\nprintf '%s\\0' \"$@\" >> \"$TMUX_RECORD\"\n"),
		0o755,
	))
	t.Setenv("TMUX_RECORD", record)
	mgr := NewManager(Options{TmuxCommand: []string{tmuxPath}})
	info := SessionInfo{
		Key:         "ws-1_codex",
		WorkspaceID: "ws-1",
		TargetKey:   "codex",
		Kind:        LaunchTargetAgent,
		TmuxSession: "middleman-ws-1-codex",
	}
	s, err := startTmuxAttachSession(
		info, helperCommand("sleep"), t.TempDir(), nil,
	)
	require.NoError(err)
	go s.watch()

	var pid int
	s.mu.Lock()
	if s.cmd != nil && s.cmd.Process != nil {
		pid = s.cmd.Process.Pid
	}
	s.mu.Unlock()
	require.Positive(pid)
	require.True(processAlive(pid), "local attach client should be alive")

	mgr.mu.Lock()
	mgr.sessions[info.Key] = s
	mgr.mu.Unlock()

	mgr.Shutdown()

	_, statErr := os.Stat(record)
	assert.True(os.IsNotExist(statErr), "shutdown should not invoke tmux cleanup")
	assert.Eventually(func() bool {
		return !processAlive(pid)
	}, 5*time.Second, 25*time.Millisecond)
	assert.Empty(mgr.ListSessions("ws-1"))
}

func TestManagerRejectsUnownedRuntimeSessions(t *testing.T) {
	mgr := NewManager(Options{Targets: []LaunchTarget{
		helperTarget("helper", "sleep"),
	}})
	t.Cleanup(mgr.Shutdown)

	_, err := mgr.Launch(context.Background(), "ws-1", t.TempDir(), "helper")
	require.Error(t, err)
	require.Contains(t, err.Error(), "runtime sessions require tmux or ptyowner")
}

func TestManagerShutdownDetachesPtyOwnerSessions(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)

	ctx := context.Background()
	owner := newFakeRuntimePtyOwner()
	mgr := NewManager(Options{
		PtyOwnerRuntime: owner,
		ShellCommand:    []string{"/bin/sh"},
		Targets:         []LaunchTarget{plainShellTarget()},
	})

	info, err := mgr.Launch(
		ctx, "ws-1", t.TempDir(), string(LaunchTargetPlainShell),
	)
	require.NoError(err)
	require.Equal(string(LaunchTargetPlainShell), info.TargetKey)

	mgr.Shutdown()

	assert.Empty(owner.stoppedSession)
	assert.Eventually(func() bool {
		select {
		case <-owner.startedPTY.Done():
			return true
		default:
			return false
		}
	}, 2*time.Second, 20*time.Millisecond)
	assert.Empty(mgr.ListSessions("ws-1"))
}

func TestManagerStopWorkspaceStopsKnownPtyOwnerSessionsAfterRestart(t *testing.T) {
	assert := Assert.New(t)

	owner := newFakeRuntimePtyOwner()
	mgr := NewManager(Options{
		PtyOwnerRuntime: owner,
		KnownPtyOwnerSessionKeys: func(
			context.Context,
			string,
		) ([]string, error) {
			return []string{"ws-1_a", "ws-1_b"}, nil
		},
	})

	mgr.StopWorkspace(context.Background(), "ws-1")

	assert.ElementsMatch([]string{"ws-1_a", "ws-1_b"}, owner.stoppedSessions)
}

func TestPtyOwnerLifecycleStopClosesAttachmentAfterOwnerStopFailure(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)

	owner := newFakeRuntimePtyOwner()
	owner.stopErr = errors.New("stop failed")
	ptySession := &fakeRuntimePTY{
		output: make(chan []byte, 64),
		done:   make(chan struct{}),
	}
	lifecycle := ptyOwnerLifecycle{
		owner:   owner,
		session: "session-1",
		pty:     ptySession,
	}

	err := lifecycle.Stop(context.Background())
	require.Error(err)
	assert.Contains(err.Error(), "stop failed")
	select {
	case <-ptySession.Done():
	case <-time.After(2 * time.Second):
		require.Fail("pty attachment was not closed")
	}
}

func TestManagerStopKeepsPtyOwnerSessionRetryableAfterStopFailure(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)

	ctx := context.Background()
	owner := newFakeRuntimePtyOwner()
	owner.stopErr = errors.New("stop failed")
	mgr := NewManager(Options{
		PtyOwnerRuntime: owner,
		ShellCommand:    []string{"/bin/sh"},
		Targets:         []LaunchTarget{plainShellTarget()},
	})
	t.Cleanup(mgr.Shutdown)

	info, err := mgr.Launch(
		ctx, "ws-1", t.TempDir(), string(LaunchTargetPlainShell),
	)
	require.NoError(err)

	err = mgr.Stop(ctx, "ws-1", info.Key)
	require.Error(err)
	require.Contains(err.Error(), "stop failed")
	_, ok := mgr.session("ws-1", info.Key)
	require.True(ok, "failed ptyowner stop should keep retry handle")

	owner.stopErr = nil
	err = mgr.Stop(ctx, "ws-1", info.Key)
	require.NoError(err)
	_, ok = mgr.session("ws-1", info.Key)
	assert.False(ok)
	assert.Equal([]string{info.Key, info.Key}, owner.stoppedSessions)
}

func TestManagerStopRemovesSession(t *testing.T) {
	requirePTYAvailable(t)
	t.Setenv("MIDDLEMAN_LOCALRUNTIME_HELPER", "1")

	ctx := context.Background()
	mgr := NewManager(withTestPtyOwnerRuntime(t, Options{Targets: []LaunchTarget{
		helperTarget("helper", "sleep"),
	}}))
	t.Cleanup(mgr.Shutdown)

	session, err := mgr.Launch(ctx, "ws-1", t.TempDir(), "helper")
	require.NoError(t, err)
	require.NoError(t, mgr.Stop(ctx, "ws-1", session.Key))

	assert := Assert.New(t)
	assert.Empty(mgr.ListSessions("ws-1"))
	assert.Error(mgr.Stop(ctx, "ws-1", session.Key))
}

func TestManagerLaunchRejectsWhileWorkspaceStopping(t *testing.T) {
	requirePTYAvailable(t)
	t.Setenv("MIDDLEMAN_LOCALRUNTIME_HELPER", "1")
	require := require.New(t)
	assert := Assert.New(t)

	record := filepath.Join(t.TempDir(), "pids")
	mgr := NewManager(withTestPtyOwnerRuntime(t, Options{Targets: []LaunchTarget{{
		Key: "helper", Label: "helper", Kind: LaunchTargetAgent,
		Source: "config", Available: true,
		Command: helperRecordCommand(record),
	}}}))
	t.Cleanup(mgr.Shutdown)

	mgr.mu.Lock()
	mgr.stoppingWS["ws-1"] = 1
	mgr.mu.Unlock()

	_, err := mgr.Launch(context.Background(), "ws-1", t.TempDir(), "helper")
	require.ErrorIs(err, errWorkspaceStopping)
	assert.Empty(mgr.ListSessions("ws-1"))

	// Whatever PID the helper recorded before being killed must be
	// gone — no orphan from the rejected launch.
	assert.Eventually(func() bool {
		data, err := os.ReadFile(record)
		if err != nil || len(data) == 0 {
			return true // helper died before recording
		}
		for line := range strings.SplitSeq(strings.TrimSpace(string(data)), "\n") {
			pid, _ := strconv.Atoi(line)
			if processAlive(pid) {
				return false
			}
		}
		return true
	}, 5*time.Second, 25*time.Millisecond,
		"rejected launch's helper process must be reaped")

	// Launches succeed again once the marker clears.
	mgr.mu.Lock()
	delete(mgr.stoppingWS, "ws-1")
	mgr.mu.Unlock()
	_, err = mgr.Launch(context.Background(), "ws-1", t.TempDir(), "helper")
	require.NoError(err)
}

func TestBeginStoppingRejectsLaunchUntilEnd(t *testing.T) {
	requirePTYAvailable(t)
	t.Setenv("MIDDLEMAN_LOCALRUNTIME_HELPER", "1")
	require := require.New(t)

	mgr := NewManager(withTestPtyOwnerRuntime(t, Options{Targets: []LaunchTarget{
		helperTarget("helper", "sleep"),
	}}))
	t.Cleanup(mgr.Shutdown)

	mgr.BeginStopping("ws-1")
	_, err := mgr.Launch(context.Background(), "ws-1", t.TempDir(), "helper")
	require.ErrorIs(err, errWorkspaceStopping)

	// Other workspaces are unaffected.
	_, err = mgr.Launch(context.Background(), "ws-2", t.TempDir(), "helper")
	require.NoError(err)

	mgr.EndStopping("ws-1")
	_, err = mgr.Launch(context.Background(), "ws-1", t.TempDir(), "helper")
	require.NoError(err)
}

func TestStopWorkspaceWaitsForInflightLaunches(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)

	mgr := NewManager(Options{})
	t.Cleanup(mgr.Shutdown)

	// Simulate a Launch that already passed claimInflight but has
	// not yet returned (i.e. still inside startSession). Without
	// the drain, StopWorkspace would snapshot empty sessions and
	// finish; the in-flight launch would then insert a session
	// after the workspace was supposedly stopped.
	mgr.mu.Lock()
	mgr.inflightWS["ws-1"] = 1
	mgr.mu.Unlock()

	stopReturned := make(chan struct{})
	go func() {
		mgr.StopWorkspace(context.Background(), "ws-1")
		close(stopReturned)
	}()

	select {
	case <-stopReturned:
		require.FailNow(
			"StopWorkspace returned before inflight launch drained",
		)
	case <-time.After(75 * time.Millisecond):
	}

	mgr.releaseInflight("ws-1")

	select {
	case <-stopReturned:
	case <-time.After(2 * time.Second):
		require.FailNow(
			"StopWorkspace did not return after inflight drained",
		)
	}

	// And the marker is cleared, so subsequent launches are not
	// permanently rejected.
	mgr.mu.Lock()
	stopping := mgr.stoppingWS["ws-1"]
	mgr.mu.Unlock()
	assert.Equal(0, stopping)
}

func TestManagerStopKillsDescendantProcesses(t *testing.T) {
	requirePTYAvailable(t)
	t.Setenv("MIDDLEMAN_LOCALRUNTIME_HELPER", "1")
	require := require.New(t)
	assert := Assert.New(t)

	record := filepath.Join(t.TempDir(), "pids")
	ctx := context.Background()
	mgr := NewManager(withTestPtyOwnerRuntime(t, Options{Targets: []LaunchTarget{{
		Key: "helper", Label: "helper", Kind: LaunchTargetAgent,
		Source: "config", Available: true,
		Command: []string{
			os.Args[0],
			"-test.run=TestHelperProcess",
			"--",
			"spawn-child",
			record,
		},
	}}}))
	t.Cleanup(mgr.Shutdown)

	session, err := mgr.Launch(ctx, "ws-1", t.TempDir(), "helper")
	require.NoError(err)

	var parentPID, childPID int
	require.Eventually(func() bool {
		data, err := os.ReadFile(record)
		if err != nil {
			return false
		}
		lines := strings.Split(strings.TrimSpace(string(data)), "\n")
		if len(lines) < 2 {
			return false
		}
		parentPID, _ = strconv.Atoi(lines[0])
		childPID, _ = strconv.Atoi(lines[1])
		return parentPID > 0 && childPID > 0
	}, 5*time.Second, 25*time.Millisecond, "helper should record both pids")

	require.True(processAlive(parentPID), "parent should be alive")
	require.True(processAlive(childPID), "child should be alive")

	require.NoError(mgr.Stop(ctx, "ws-1", session.Key))

	assert.Eventually(func() bool {
		return !processAlive(parentPID) && !processAlive(childPID)
	}, 5*time.Second, 25*time.Millisecond,
		"descendant child should die with the session leader")
}

func TestSessionWatchLeavesOutputOpenForDrain(t *testing.T) {
	require := require.New(t)

	readEnd, writeEnd, err := os.Pipe()
	require.NoError(err)
	defer writeEnd.Close()
	defer readEnd.Close()

	_, err = writeEnd.WriteString("final output")
	require.NoError(err)

	cmd := procutil.Command("sh", "-c", "exit 0")
	require.NoError(cmd.Start())
	outputDone := make(chan struct{})
	s := &session{
		cmd:        cmd,
		ptmx:       readEnd,
		done:       make(chan struct{}),
		outputDone: outputDone,
	}

	s.watch()

	buf := make([]byte, len("final output"))
	_, err = readEnd.Read(buf)
	require.NoError(err)
	close(outputDone)
	require.Equal("final output", string(buf))
}

func TestSessionWatchClosesPTYAfterPostExitDrainTimeout(t *testing.T) {
	require := require.New(t)

	readEnd, writeEnd, err := os.Pipe()
	require.NoError(err)
	defer readEnd.Close()
	defer writeEnd.Close()

	cmd := procutil.Command("sh", "-c", "exit 0")
	require.NoError(cmd.Start())
	outputDone := make(chan struct{})
	s := &session{
		cmd:        cmd,
		ptmx:       readEnd,
		done:       make(chan struct{}),
		outputDone: outputDone,
	}

	s.watch()
	defer close(outputDone)

	require.Eventually(func() bool {
		_, err := readEnd.Stat()
		return err != nil
	}, time.Second, 10*time.Millisecond)
}

func TestManagerRemovesNaturallyExitedSession(t *testing.T) {
	ctx := context.Background()
	exited := make(chan SessionInfo, 1)
	owner := newFakeRuntimePtyOwner()
	mgr := NewManager(Options{Targets: []LaunchTarget{
		helperTarget("helper", "sleep"),
	}, OnSessionExit: func(info SessionInfo) {
		exited <- info
	}, PtyOwnerRuntime: owner})
	t.Cleanup(mgr.Shutdown)

	session, err := mgr.Launch(ctx, "ws-1", t.TempDir(), "helper")
	require.NoError(t, err)
	owner.startedPTY.Close()

	var got SessionInfo
	require.Eventually(t, func() bool {
		select {
		case got = <-exited:
			return true
		default:
			return false
		}
	}, 2*time.Second, 20*time.Millisecond)

	assert := Assert.New(t)
	assert.Equal(session.Key, got.Key)
	assert.Equal(SessionStatusExited, got.Status)
	assert.NotNil(got.ExitedAt)
	assert.NotNil(got.ExitCode)
	assert.Equal(0, *got.ExitCode)
	assert.Empty(mgr.ListSessions("ws-1"))
}

func TestManagerRemovesNaturallyExitedShell(t *testing.T) {
	ctx := context.Background()
	exited := make(chan SessionInfo, 1)
	owner := newFakeRuntimePtyOwner()
	mgr := NewManager(Options{
		ShellCommand: []string{"/bin/sh"},
		Targets:      []LaunchTarget{plainShellTarget()},
		OnSessionExit: func(info SessionInfo) {
			exited <- info
		},
		PtyOwnerRuntime: owner,
	})
	t.Cleanup(mgr.Shutdown)

	shell, err := mgr.Launch(
		ctx, "ws-1", t.TempDir(), string(LaunchTargetPlainShell),
	)
	require.NoError(t, err)
	owner.startedPTY.Close()

	var got SessionInfo
	require.Eventually(t, func() bool {
		select {
		case got = <-exited:
			return true
		default:
			return false
		}
	}, 2*time.Second, 20*time.Millisecond)

	assert := Assert.New(t)
	assert.Equal(shell.Key, got.Key)
	assert.Equal(SessionStatusExited, got.Status)
	assert.NotNil(got.ExitedAt)
	assert.NotNil(got.ExitCode)
	assert.Equal(0, *got.ExitCode)
	assert.Empty(mgr.ListSessions("ws-1"))
}

func TestManagerLaunchPlainShellCreatesIndependentSessions(t *testing.T) {
	requirePTYAvailable(t)
	t.Setenv("MIDDLEMAN_LOCALRUNTIME_HELPER", "1")

	ctx := context.Background()
	mgr := NewManager(withTestPtyOwnerRuntime(t, Options{
		ShellCommand: helperCommand("sleep"),
		Targets:      []LaunchTarget{plainShellTarget()},
	}))
	t.Cleanup(mgr.Shutdown)

	shell1, err := mgr.Launch(
		ctx, "ws-1", t.TempDir(), string(LaunchTargetPlainShell),
	)
	require.NoError(t, err)
	shell2, err := mgr.Launch(
		ctx, "ws-1", t.TempDir(), string(LaunchTargetPlainShell),
	)
	require.NoError(t, err)

	assert := Assert.New(t)
	assert.NotEqual(shell1.Key, shell2.Key)
	assert.Equal(SessionStatusRunning, shell1.Status)
	assert.Equal(SessionStatusRunning, shell2.Status)
	assert.Equal("Shell", shell1.Label)
	assert.Equal("Shell 2", shell2.Label)
	assert.Len(mgr.ListSessions("ws-1"), 2)
}

// TestAttachmentSessionOutputClosedDistinguishesSubscriberDrop covers
// the contract bridges rely on to tell a real session exit from a
// dropped subscriber: a closed Output channel can mean either, and
// auto-closing the drawer on the latter would hang the user out on a
// healthy shell.
//
// broadcast drops a subscriber when its 64-slot buffer can't accept
// another chunk (slow client / congested writer). drainOutput's PTY
// EOF, in contrast, runs closeSubscribers which flips s.outputClosed
// before closing every subscriber channel. SessionOutputClosed
// exposes that distinction to bridge code.
func TestAttachmentSessionOutputClosedDistinguishesSubscriberDrop(t *testing.T) {
	requirePTYAvailable(t)
	t.Setenv("MIDDLEMAN_LOCALRUNTIME_HELPER", "1")

	require := require.New(t)
	assert := Assert.New(t)
	ctx := context.Background()
	mgr := NewManager(withTestPtyOwnerRuntime(t, Options{
		ShellCommand: helperCommand("sleep"),
		Targets:      []LaunchTarget{plainShellTarget()},
	}))
	t.Cleanup(mgr.Shutdown)

	shell, err := mgr.Launch(
		ctx, "ws-1", t.TempDir(), string(LaunchTargetPlainShell),
	)
	require.NoError(err)

	attach, err := mgr.AttachSession("ws-1", shell.Key)
	require.NoError(err)
	t.Cleanup(attach.Close)
	require.Equal(shell.Key, attach.Info().Key)

	// Healthy session: SessionOutputClosed must report false.
	assert.False(attach.SessionOutputClosed(),
		"freshly-attached session should not look output-closed")

	mgr.mu.Lock()
	s := mgr.sessions[shell.Key]
	mgr.mu.Unlock()
	require.NotNil(s)

	// Force the broadcast-drops-subscriber path: the channel buffer
	// is 64, so the 65th broadcast that can't enqueue takes the
	// `default` branch and closes the channel. Run the broadcasts
	// synchronously WITHOUT a concurrent consumer — a parallel
	// reader could drain the buffer faster than we fill it and the
	// drop would never trigger. Drain afterward to confirm closure.
	for range 200 {
		s.broadcast([]byte("x"))
	}
	drained := 0
drain:
	for {
		// Bound the receive: if broadcast regresses and never
		// closes the channel, the buffered messages drain and the
		// next receive would block forever, hanging the test
		// process instead of failing it.
		select {
		case _, ok := <-attach.Output:
			if !ok {
				break drain
			}
			drained++
			require.Less(drained, 200,
				"channel never closed; broadcast did not "+
					"drop the slow subscriber")
		case <-time.After(2 * time.Second):
			require.Fail(
				"timed out waiting for channel close; " +
					"broadcast did not drop the slow subscriber",
			)
		}
	}
	assert.LessOrEqual(drained, 64,
		"buffer is 64; drop should fire by the 65th broadcast")

	// Subscriber dropped, but the session itself is still healthy
	// (helperCommand("sleep") is still running and drainOutput has
	// not seen PTY EOF). SessionOutputClosed must NOT be true here —
	// otherwise the bridge would emit "exited" on a live shell.
	assert.False(attach.SessionOutputClosed(),
		"subscriber drop must not be misreported as session exit")

	// Now simulate the real session-exit path. closeSubscribers is
	// what drainOutput calls on PTY EOF; it flips outputClosed.
	s.closeSubscribers()
	assert.True(attach.SessionOutputClosed(),
		"after drainOutput's closeSubscribers, the bridge must see "+
			"the session as output-closed and emit the exit frame")
}

func TestAttachmentResizeOwnerPrefersActiveLocalUntilInactive(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)

	pty := &fakeRuntimePTY{
		output: make(chan []byte),
		done:   make(chan struct{}),
	}
	s := &session{
		info: SessionInfo{
			Key:         "session-1",
			WorkspaceID: "ws-1",
			Status:      SessionStatusRunning,
		},
		pty:         pty,
		done:        make(chan struct{}),
		outputDone:  make(chan struct{}),
		subscribers: make(map[chan []byte]struct{}),
	}

	remote, err := attachToSession(
		s, "ws-1", "session-1", nil,
		AttachSessionOptions{
			ResizePriority: ResizePriorityRemote,
			ResizeActive:   true,
		},
	)
	require.NoError(err)
	defer remote.Close()
	require.NoError(remote.Resize(80, 24))

	local, err := attachToSession(
		s, "ws-1", "session-1", nil,
		AttachSessionOptions{ResizePriority: ResizePriorityLocal},
	)
	require.NoError(err)
	require.NoError(remote.Resize(90, 25))
	local.SetResizeActive(true)
	require.NoError(remote.Resize(95, 26))
	require.NoError(local.Resize(100, 30))

	local.SetResizeActive(false)
	require.NoError(remote.Resize(120, 40))

	assert.Equal([]terminalResize{
		{cols: 80, rows: 24},
		{cols: 90, rows: 25},
		{cols: 100, rows: 30},
		{cols: 120, rows: 40},
	}, pty.resizes())
}

type fakeRuntimePtyOwner struct {
	startedSession      string
	startedCwd          string
	startedCommand      []string
	startedStripEnvVars []string
	startedPTY          *fakeRuntimePTY
	stoppedSession      string
	stoppedSessions     []string
	stopErr             error
	attachErrs          []error
	starts              int
	attaches            int
}

func newFakeRuntimePtyOwner() *fakeRuntimePtyOwner {
	return &fakeRuntimePtyOwner{}
}

func (f *fakeRuntimePtyOwner) HasState(session string) bool {
	return f.startedSession == session
}

func writeLongRunningAttachTmux(t *testing.T) string {
	t.Helper()
	tmuxPath := filepath.Join(t.TempDir(), "tmux")
	require.NoError(t, os.WriteFile(tmuxPath, []byte(`#!/bin/sh
if [ "$1" = "attach-session" ]; then
  trap 'exit 0' HUP INT TERM
  while :; do sleep 1; done
fi
exit 0
`), 0o755))
	return tmuxPath
}

func (f *fakeRuntimePtyOwner) Start(
	_ context.Context,
	session string,
	cwd string,
	command []string,
	stripEnvVars []string,
) (ptyownerruntime.PTY, error) {
	f.starts++
	f.startedSession = session
	f.startedCwd = cwd
	f.startedCommand = slices.Clone(command)
	f.startedStripEnvVars = slices.Clone(stripEnvVars)
	f.startedPTY = &fakeRuntimePTY{
		output: make(chan []byte, 64),
		done:   make(chan struct{}),
	}
	return f.startedPTY, nil
}

func (f *fakeRuntimePtyOwner) Attach(
	_ context.Context,
	session string,
) (ptyownerruntime.PTY, error) {
	f.attaches++
	if len(f.attachErrs) > 0 {
		err := f.attachErrs[0]
		f.attachErrs = f.attachErrs[1:]
		return nil, err
	}
	if !f.HasState(session) || f.startedPTY == nil {
		return nil, errors.New("missing pty owner state")
	}
	return f.startedPTY, nil
}

func (f *fakeRuntimePtyOwner) Stop(_ context.Context, session string) error {
	f.stoppedSession = session
	f.stoppedSessions = append(f.stoppedSessions, session)
	if f.stopErr != nil {
		return f.stopErr
	}
	if f.startedPTY != nil {
		f.startedPTY.Close()
	}
	return nil
}

type fakeRuntimePTY struct {
	mu          sync.Mutex
	output      chan []byte
	done        chan struct{}
	resizeCalls []terminalResize
}

type terminalResize struct {
	cols int
	rows int
}

func (f *fakeRuntimePTY) Output() <-chan []byte { return f.output }

func (f *fakeRuntimePTY) Done() <-chan struct{} { return f.done }

func (f *fakeRuntimePTY) Write([]byte) error { return nil }

func (f *fakeRuntimePTY) Resize(cols, rows int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resizeCalls = append(f.resizeCalls, terminalResize{cols: cols, rows: rows})
	return nil
}

func (f *fakeRuntimePTY) resizes() []terminalResize {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.resizeCalls)
}

func (f *fakeRuntimePTY) ExitCode() int { return 0 }

func (f *fakeRuntimePTY) Close() {
	select {
	case <-f.done:
	default:
		close(f.output)
		close(f.done)
	}
}

func TestManagerPlainShellConcurrentLaunchesStartIndependentProcesses(t *testing.T) {
	requirePTYAvailable(t)
	t.Setenv("MIDDLEMAN_LOCALRUNTIME_HELPER", "1")
	require := require.New(t)
	assert := Assert.New(t)

	ctx := context.Background()
	record := filepath.Join(t.TempDir(), "shell-starts")
	mgr := NewManager(withTestPtyOwnerRuntime(t, Options{
		ShellCommand: helperRecordCommand(record),
		Targets:      []LaunchTarget{plainShellTarget()},
	}))
	t.Cleanup(mgr.Shutdown)

	const launches = 12
	var wg sync.WaitGroup
	errs := make(chan error, launches)
	infos := make(chan SessionInfo, launches)
	cwd := t.TempDir()
	for range launches {
		wg.Go(func() {
			info, err := mgr.Launch(
				ctx, "ws-1", cwd, string(LaunchTargetPlainShell),
			)
			errs <- err
			infos <- info
		})
	}
	wg.Wait()
	close(errs)
	close(infos)

	for err := range errs {
		require.NoError(err)
	}
	keys := make(map[string]bool, launches)
	for info := range infos {
		keys[info.Key] = true
	}
	assert.Len(keys, launches)
	require.Eventually(func() bool {
		data, err := os.ReadFile(record)
		if err != nil {
			return false
		}
		return strings.Count(string(data), "\n") == launches
	}, 2*time.Second, 20*time.Millisecond)
	assert.Len(mgr.ListSessions("ws-1"), launches)
}

func TestManagerShutdownRejectsNewLaunches(t *testing.T) {
	t.Setenv("MIDDLEMAN_LOCALRUNTIME_HELPER", "1")

	mgr := NewManager(Options{Targets: []LaunchTarget{
		helperTarget("helper", "sleep"),
	}})
	t.Cleanup(mgr.Shutdown)

	mgr.Shutdown()

	_, err := mgr.Launch(
		context.Background(), "ws-1", t.TempDir(), "helper",
	)
	require.Error(t, err)
	require.Contains(t, err.Error(), "runtime manager is shut down")
}

func TestSessionBroadcastClosesSlowSubscriber(t *testing.T) {
	s := &session{
		subscribers: make(map[chan []byte]struct{}),
	}
	ch := make(chan []byte, 1)
	ch <- []byte("queued")
	s.subscribers[ch] = struct{}{}

	s.broadcast([]byte("new"))

	got := <-ch
	assert := Assert.New(t)
	assert.Equal([]byte("queued"), got)
	select {
	case _, ok := <-ch:
		assert.False(ok)
	case <-time.After(100 * time.Millisecond):
		assert.Fail("slow subscriber was not closed")
	}
	s.mu.Lock()
	_, subscribed := s.subscribers[ch]
	s.mu.Unlock()
	assert.False(subscribed)
}

func TestSessionSubscribeReplaysBufferedOutput(t *testing.T) {
	s := &session{
		subscribers: make(map[chan []byte]struct{}),
	}
	s.broadcast([]byte("startup-banner\r\n"))
	s.broadcast([]byte("$ "))

	ch, cancel := s.subscribe()
	t.Cleanup(cancel)

	assert := Assert.New(t)
	select {
	case data := <-ch:
		assert.Equal("startup-banner\r\n$ ", string(data))
	case <-time.After(100 * time.Millisecond):
		assert.Fail("subscriber did not receive replay")
	}

	s.broadcast([]byte("ls\r\n"))
	select {
	case data := <-ch:
		assert.Equal("ls\r\n", string(data))
	case <-time.After(100 * time.Millisecond):
		assert.Fail("subscriber did not receive new output after replay")
	}
}

func TestSessionSubscribeSkipsReplayWhileAlternateScreenActive(t *testing.T) {
	s := &session{
		subscribers: make(map[chan []byte]struct{}),
	}
	s.broadcast([]byte("startup-banner\r\n$ "))
	s.broadcast([]byte("\x1b[?1049h\x1b[Hcodex screen"))

	ch, cancel := s.subscribe()
	t.Cleanup(cancel)

	assert := Assert.New(t)
	select {
	case data := <-ch:
		assert.Failf(
			"subscriber received alternate screen replay",
			"unexpected replay: %q",
			string(data),
		)
	case <-time.After(25 * time.Millisecond):
	}

	s.broadcast([]byte("\x1b[Hupdated screen"))
	select {
	case data := <-ch:
		assert.Equal("\x1b[Hupdated screen", string(data))
	case <-time.After(100 * time.Millisecond):
		assert.Fail("subscriber did not receive live output")
	}
}

func TestSessionSubscribeReplaysNormalOutputAfterAlternateScreenExit(t *testing.T) {
	s := &session{
		subscribers: make(map[chan []byte]struct{}),
	}
	s.broadcast([]byte("startup-banner\r\n$ "))
	s.broadcast([]byte("\x1b[?1049h\x1b[Hcodex screen"))
	s.broadcast([]byte("\x1b[?1049l\r\n$ "))

	ch, cancel := s.subscribe()
	t.Cleanup(cancel)

	assert := Assert.New(t)
	select {
	case data := <-ch:
		assert.Equal("\r\n$ ", string(data))
	case <-time.After(100 * time.Millisecond):
		assert.Fail("subscriber did not receive normal replay after exit")
	}
}

func TestSessionAlternateScreenTrackingHandlesSplitEscapeSequences(t *testing.T) {
	s := &session{
		subscribers: make(map[chan []byte]struct{}),
	}
	s.broadcast([]byte("startup-banner\r\n$ \x1b[?104"))
	s.broadcast([]byte("9h\x1b[Hcodex screen"))

	ch, cancel := s.subscribe()
	t.Cleanup(cancel)

	assert := Assert.New(t)
	select {
	case data := <-ch:
		assert.Failf(
			"subscriber received split alternate screen replay",
			"unexpected replay: %q",
			string(data),
		)
	case <-time.After(25 * time.Millisecond):
	}

	s.broadcast([]byte("\x1b[?104"))
	s.broadcast([]byte("9l\r\n$ "))
	var live strings.Builder
	select {
	case data := <-ch:
		live.Write(data)
	case <-time.After(100 * time.Millisecond):
		assert.Fail("subscriber did not receive live split exit prefix")
	}
	select {
	case data := <-ch:
		live.Write(data)
	case <-time.After(100 * time.Millisecond):
		assert.Fail("subscriber did not receive live split exit suffix")
	}
	assert.Equal("\x1b[?1049l\r\n$ ", live.String())

	ch2, cancel2 := s.subscribe()
	t.Cleanup(cancel2)
	select {
	case data := <-ch2:
		assert.Equal("\r\n$ ", string(data))
	case <-time.After(100 * time.Millisecond):
		assert.Fail("subscriber did not receive replay after split exit")
	}
}

func TestSessionSubscribeAfterCloseStillReplays(t *testing.T) {
	s := &session{
		subscribers: make(map[chan []byte]struct{}),
	}
	s.broadcast([]byte("hello\r\nbye\r\n"))
	s.closeSubscribers()

	ch, cancel := s.subscribe()
	t.Cleanup(cancel)

	assert := Assert.New(t)
	select {
	case data, ok := <-ch:
		assert.True(ok)
		assert.Equal("hello\r\nbye\r\n", string(data))
	case <-time.After(100 * time.Millisecond):
		assert.Fail("expected replay before channel close")
	}
	select {
	case _, ok := <-ch:
		assert.False(ok)
	case <-time.After(100 * time.Millisecond):
		assert.Fail("expected channel to close after replay")
	}
}

func TestSessionOutputBufferIsBounded(t *testing.T) {
	s := &session{
		subscribers: make(map[chan []byte]struct{}),
	}
	chunk := make([]byte, 8*1024)
	for i := range chunk {
		chunk[i] = 'x'
	}
	for range 12 {
		s.broadcast(chunk)
	}

	s.mu.Lock()
	bufLen := len(s.outputBuffer)
	s.mu.Unlock()
	Assert.New(t).LessOrEqual(bufLen, maxSessionOutputReplay)
}

func TestManagerStopWorkspaceStopsAllSessions(t *testing.T) {
	requirePTYAvailable(t)
	t.Setenv("MIDDLEMAN_LOCALRUNTIME_HELPER", "1")

	require := require.New(t)
	assert := Assert.New(t)

	ctx := context.Background()
	mgr := NewManager(withTestPtyOwnerRuntime(t, Options{
		Targets: []LaunchTarget{
			helperTarget("agent-a", "sleep"),
			helperTarget("agent-b", "sleep"),
			plainShellTarget(),
		},
		ShellCommand: helperCommand("sleep"),
	}))
	t.Cleanup(mgr.Shutdown)

	_, err := mgr.Launch(ctx, "ws-1", t.TempDir(), "agent-a")
	require.NoError(err)
	_, err = mgr.Launch(ctx, "ws-1", t.TempDir(), "agent-b")
	require.NoError(err)
	_, err = mgr.Launch(ctx, "ws-1", t.TempDir(), string(LaunchTargetPlainShell))
	require.NoError(err)

	// A second workspace's sessions must survive.
	_, err = mgr.Launch(ctx, "ws-2", t.TempDir(), "agent-a")
	require.NoError(err)

	mgr.StopWorkspace(ctx, "ws-1")

	assert.Empty(mgr.ListSessions("ws-1"))
	assert.Len(mgr.ListSessions("ws-2"), 1)
}

func plainShellTarget() LaunchTarget {
	return LaunchTarget{
		Key:       string(LaunchTargetPlainShell),
		Label:     "Shell",
		Kind:      LaunchTargetPlainShell,
		Source:    "system",
		Available: true,
	}
}

func helperTarget(key, mode string) LaunchTarget {
	return LaunchTarget{
		Key: key, Label: key, Kind: LaunchTargetAgent,
		Source: "config", Command: helperCommand(mode),
		Available: true,
	}
}

func helperRecordCommand(record string) []string {
	return []string{
		os.Args[0],
		"-test.run=TestHelperProcess",
		"--",
		"sleep-record",
		record,
	}
}

func helperCommand(mode string) []string {
	return []string{
		os.Args[0],
		"-test.run=TestHelperProcess",
		"--",
		mode,
	}
}

// TestResolveExecutableRejectsRelativePaths ensures startSession
// refuses commands that would resolve inside the workspace worktree
// (PR-controlled content). Absolute paths and PATH-resolvable
// names are accepted; relative names with separators are rejected.
func TestResolveExecutableRejectsRelativePaths(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)

	// Absolute path: pass through unchanged.
	absCommand := "/usr/local/bin/codex"
	if runtime.GOOS == "windows" {
		absCommand = `C:\tools\codex.exe`
	}
	got, err := resolveExecutable(absCommand)
	require.NoError(err)
	assert.Equal(absCommand, got)

	// PATH-resolvable: returns the full path.
	exeName, exePath := writeFakeRuntimeTool(t, t.TempDir(), "fake-runtime-tool")
	t.Setenv("PATH", filepath.Dir(exePath))
	got, err = resolveExecutable(exeName)
	require.NoError(err)
	assert.True(filepath.IsAbs(got), "expected absolute path, got %q", got)
	if runtime.GOOS == "windows" {
		assert.True(
			strings.EqualFold(exePath, got),
			"expected %q, got %q",
			exePath,
			got,
		)
	} else {
		assert.Equal(exePath, got)
	}

	// Relative paths must be rejected.
	for _, rel := range []string{
		"./agent",
		"../scripts/codex",
		"scripts/codex",
		"a/b",
	} {
		_, err := resolveExecutable(rel)
		require.Error(err, "expected error for %q", rel)
		assert.Contains(err.Error(), "absolute path")
	}

	// Empty name.
	_, err = resolveExecutable("")
	require.Error(err)

	// Bare name not on PATH should surface a LookPath error.
	_, err = resolveExecutable(
		"middleman-localruntime-bogus-name-zzz",
	)
	require.Error(err)
}

func TestResolveExecutableForcesAbsoluteFromRelativePATH(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)

	dir := t.TempDir()
	binDir := filepath.Join(dir, "bin")
	require.NoError(os.MkdirAll(binDir, 0o755))
	exeName, exe := writeFakeRuntimeTool(t, binDir, "fake-runtime-tool")

	t.Chdir(dir)
	t.Setenv("PATH", "bin")
	// Recent Go versions wrap LookPath results from relative PATH
	// entries with ErrDot. With execerrdot=0 they're returned with
	// no error — that's exactly the case where the worktree-cwd
	// rebinding is dangerous, so verify the absolute fallback runs.
	t.Setenv("GODEBUG", "execerrdot=0")

	got, err := resolveExecutable(exeName)
	require.NoError(err)
	assert.True(
		filepath.IsAbs(got),
		"expected absolute path, got %q (relative would resolve "+
			"inside cmd.Dir = the workspace worktree)",
		got,
	)
	if runtime.GOOS == "windows" {
		assert.True(
			strings.EqualFold(exe, got),
			"expected %q, got %q",
			exe,
			got,
		)
	} else {
		assert.Equal(exe, got)
	}
}

func writeFakeRuntimeTool(
	t *testing.T,
	dir string,
	name string,
) (string, string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(dir, 0o755))
	if runtime.GOOS == "windows" {
		t.Setenv("PATHEXT", ".COM;.EXE;.BAT;.CMD")
		path := filepath.Join(dir, name+".cmd")
		require.NoError(t, os.WriteFile(
			path,
			[]byte("@echo off\r\nexit /b 0\r\n"),
			0o755,
		))
		return name, path
	}
	path := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(
		path,
		[]byte("#!/bin/sh\nexit 0\n"),
		0o755,
	))
	return name, path
}

// TestSessionEnvironmentStripsCredentials verifies that the
// environment passed to runtime sessions has GitHub-token-shaped
// variables removed so that launched agents cannot exfiltrate
// the maintainer's credentials.
func TestSessionEnvironmentStripsCredentials(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)

	in := []string{
		"PATH=/usr/bin",
		"HOME=/home/me",
		"MIDDLEMAN_GITHUB_TOKEN=secret-1",
		"GITHUB_TOKEN=secret-2",
		"GH_TOKEN=secret-3",
		"GITHUB_PAT=secret-4",
		"GH_PAT=secret-5",
		"GITHUB_ENTERPRISE_TOKEN=secret-6",
		"GH_ENTERPRISE_TOKEN=secret-7",
		"GITHUB_TOKEN_GHE=secret-8",
		"MAINTAINER_PERSONAL_GH_PAT=secret-9",
		"NOTSECRET=ok",
	}
	out := sessionEnvironment(in, []string{
		"MAINTAINER_PERSONAL_GH_PAT",
	})

	require.Contains(out, "PATH=/usr/bin")
	require.Contains(out, "HOME=/home/me")
	require.Contains(out, "NOTSECRET=ok")

	for _, kv := range out {
		assert.NotContains(
			kv, "secret-",
			"credential leaked through sessionEnvironment: %q", kv,
		)
	}
}

func TestSessionEnvironmentStripsConfiguredTokenEnv(t *testing.T) {
	require := require.New(t)
	in := []string{
		"PATH=/usr/bin",
		"WORK_GH_BOT_TOKEN=top-secret",
	}
	out := sessionEnvironment(in, []string{"WORK_GH_BOT_TOKEN"})
	require.Contains(out, "PATH=/usr/bin")
	for _, kv := range out {
		require.NotContains(
			kv, "top-secret",
			"configured token env leaked: %q", kv,
		)
	}
}

func TestHelperProcess(t *testing.T) {
	if os.Getenv("MIDDLEMAN_LOCALRUNTIME_HELPER") != "1" {
		return
	}
	args := os.Args
	helperArgs := args[len(args)-1:]
	for i, arg := range args {
		if arg == "--" && i+1 < len(args) {
			helperArgs = args[i+1:]
			break
		}
	}
	mode := helperArgs[0]
	switch mode {
	case "sleep":
		time.Sleep(time.Hour)
	case "sleep-record":
		if len(helperArgs) < 2 {
			os.Exit(2)
		}
		f, err := os.OpenFile(
			helperArgs[1],
			os.O_APPEND|os.O_CREATE|os.O_WRONLY,
			0o644,
		)
		if err != nil {
			os.Exit(2)
		}
		_, _ = fmt.Fprintf(f, "%d\n", os.Getpid())
		_ = f.Close()
		time.Sleep(time.Hour)
	case "spawn-child":
		if len(helperArgs) < 2 {
			os.Exit(2)
		}
		child := procutil.Command(
			os.Args[0], "-test.run=TestHelperProcess", "--", "sleep",
		)
		if err := child.Start(); err != nil {
			os.Exit(2)
		}
		f, err := os.OpenFile(
			helperArgs[1],
			os.O_APPEND|os.O_CREATE|os.O_WRONLY,
			0o644,
		)
		if err != nil {
			os.Exit(2)
		}
		_, _ = fmt.Fprintf(
			f, "%d\n%d\n", os.Getpid(), child.Process.Pid,
		)
		_ = f.Close()
		time.Sleep(time.Hour)
	case "exit":
		os.Exit(3)
	default:
		os.Exit(2)
	}
}
