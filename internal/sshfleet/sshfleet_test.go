package sshfleet

import (
	"sync"
	"time"

	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeSSH records ssh invocations and scripts their results. The
// "-MNf" master spawn creates the socket file like a real master
// would, so Connect's establish poll succeeds.
type fakeSSH struct {
	mgr      *ConnectionManager
	calls    [][]string
	checkOK  bool
	spawnErr error
}

func (f *fakeSSH) run(args []string) (int, error) {
	f.calls = append(f.calls, args)
	joined := strings.Join(args, " ")
	switch {
	case strings.Contains(joined, "-MNf"):
		if f.spawnErr != nil {
			return 255, f.spawnErr
		}
		// Real masters create the socket; mirror that.
		for i, a := range args {
			if a == "-o" && strings.HasPrefix(args[i+1], "ControlPath=") {
				path := strings.TrimPrefix(args[i+1], "ControlPath=")
				_ = os.WriteFile(path, nil, 0o600)
			}
		}
		return 0, nil
	case strings.Contains(joined, "-O check"):
		if f.checkOK {
			return 0, nil
		}
		return 255, fmt.Errorf("no master running")
	}
	return 0, nil
}

// TestConnectionManagerLifecycle pins the state machine: connect
// emits connecting→connected and creates the master; a live same-
// destination reconnect reuses it without respawning; disconnect
// tears down and emits.
func TestConnectionManagerLifecycle(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)

	var events []Event
	fake := &fakeSSH{}
	mgr := NewConnectionManager(t.TempDir(), Config{
		RunSSH:  fake.run,
		OnEvent: func(e Event) { events = append(events, e) },
	})
	fake.mgr = mgr

	require.NoError(mgr.Connect("studio", "wes@studio.local"))
	assert.Equal(StateConnected, mgr.State("studio"))
	assert.Equal("wes@studio.local", mgr.Destination("studio"))

	spawns := 0
	for _, c := range fake.calls {
		if strings.Contains(strings.Join(c, " "), "-MNf") {
			spawns++
		}
	}
	assert.Equal(1, spawns)

	// Same destination with a live master: no respawn.
	fake.checkOK = true
	require.NoError(mgr.Connect("studio", "wes@studio.local"))
	spawns = 0
	for _, c := range fake.calls {
		if strings.Contains(strings.Join(c, " "), "-MNf") {
			spawns++
		}
	}
	assert.Equal(1, spawns, "live master must be reused")

	require.NoError(mgr.Disconnect("studio"))
	assert.Equal(StateDisconnected, mgr.State("studio"))
	_, statErr := os.Stat(mgr.SocketPath("studio"))
	assert.True(os.IsNotExist(statErr), "socket removed on disconnect")

	var states []string
	for _, e := range events {
		states = append(states, e.State)
	}
	assert.Equal(
		[]string{StateConnecting, StateConnected, StateDisconnected},
		states,
	)
}

// TestConnectionManagerSpawnFailure pins the error path: a failed
// master spawn lands in error state with the message preserved.
func TestConnectionManagerSpawnFailure(t *testing.T) {
	fake := &fakeSSH{spawnErr: fmt.Errorf("permission denied")}
	mgr := NewConnectionManager(t.TempDir(), Config{RunSSH: fake.run})
	err := mgr.Connect("studio", "wes@studio.local")
	require.Error(t, err)
	assert.Equal(t, StateError, mgr.State("studio"))
}

// TestRunnerRelayFramesStatusAndBody pins the relay contract: the
// remote api -i framing decodes into status + body for both success
// and HTTP-error exits, the ssh argv rides the peer's ControlMaster
// with ControlMaster=no, and the remote fragment normalizes PATH and
// quotes the verb.
func TestRunnerRelayFramesStatusAndBody(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)

	mgr := NewConnectionManager(t.TempDir(), Config{
		RunSSH: func([]string) (int, error) { return 0, nil },
	})
	runner := NewRunner(mgr)

	var gotArgv []string
	var gotStdin []byte
	runner.execCommand = func(
		_ context.Context, argv []string, stdin []byte,
	) ([]byte, []byte, int, error) {
		gotArgv = argv
		gotStdin = stdin
		return []byte("HTTP/1.1 201 Created\r\n\r\n{\"id\":\"wtr_1\"}"),
			nil, 0, nil
	}

	resp, err := runner.Relay(
		context.Background(),
		"studio", "wes@studio.local", "middleman",
		"POST", "/api/v1/projects/prj_1/worktrees",
		[]byte(`{"branch":"feat"}`),
	)
	require.NoError(err)
	assert.Equal(201, resp.Status)
	assert.JSONEq(`{"id":"wtr_1"}`, string(resp.Body))
	assert.JSONEq(`{"branch":"feat"}`, string(gotStdin))

	joined := strings.Join(gotArgv, " ")
	assert.Equal("ssh", gotArgv[0])
	assert.Contains(joined, "ControlPath="+mgr.SocketPath("studio"))
	assert.Contains(joined, "ControlMaster=no")
	assert.Contains(joined, "wes@studio.local")
	assert.Contains(joined, "middleman")
	assert.Contains(joined, "api")
	assert.Contains(joined, "PATH=")

	// HTTP-error exit still yields the framed problem body.
	runner.execCommand = func(
		context.Context, []string, []byte,
	) ([]byte, []byte, int, error) {
		return []byte("HTTP/1.1 404 Not Found\r\n\r\n{\"code\":\"notFound\"}"),
			nil, verbExitHTTPError, nil
	}
	resp, err = runner.Relay(
		context.Background(),
		"studio", "wes@studio.local", "middleman",
		"GET", "/api/v1/projects/prj_missing", nil,
	)
	require.NoError(err)
	assert.Equal(404, resp.Status)
	assert.Contains(string(resp.Body), "notFound")

	// No-request exit surfaces the remote stderr as the error.
	runner.execCommand = func(
		context.Context, []string, []byte,
	) ([]byte, []byte, int, error) {
		return nil, []byte("no middleman daemon is running on /data"),
			verbExitNoRequest, nil
	}
	_, err = runner.Relay(
		context.Background(),
		"studio", "wes@studio.local", "middleman",
		"GET", "/api/v1/snapshot/raw", nil,
	)
	require.Error(err)
	assert.Contains(err.Error(), "remote daemon unavailable")
	assert.Contains(err.Error(), "no middleman daemon is running")
}

// TestRelayNoRequestErrorIsTyped pins that an exit-2 relay (no
// request reached the remote daemon) is matchable with errors.Is so
// callers can trigger daemon auto-start on exactly this failure.
func TestRelayNoRequestErrorIsTyped(t *testing.T) {
	mgr := NewConnectionManager(t.TempDir(), Config{
		RunSSH: func([]string) (int, error) { return 0, nil },
	})
	runner := NewRunnerWithExec(mgr, func(
		context.Context, []string, []byte,
	) ([]byte, []byte, int, error) {
		return nil, []byte("no middleman daemon is running"),
			verbExitNoRequest, nil
	})
	_, err := runner.Relay(
		context.Background(),
		"studio", "wes@studio.local", "middleman",
		"GET", "/api/v1/snapshot/raw", nil,
	)
	require.ErrorIs(t, err, ErrRemoteDaemonUnavailable)
}

// ensureFakeExec scripts the remote side of EnsureDaemon: status
// probes answer from `running`, and a detached serve start flips
// `running` after startDelayProbes more probes.
type ensureFakeExec struct {
	mu          sync.Mutex
	running     bool
	startCalls  int
	probeCalls  int
	startErr    error
	flipOnStart bool
	// metadataLagProbes keeps metadata null for this many probes
	// after running flips true (daemon early in startup).
	metadataLagProbes int
}

func (f *ensureFakeExec) exec(
	_ context.Context, argv []string, _ []byte,
) ([]byte, []byte, int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fragment := argv[len(argv)-1]
	switch {
	case strings.Contains(fragment, "status"):
		f.probeCalls++
		// Mirror the real status --json shape: metadata trails the
		// lock during startup, so a configurable lag keeps it null
		// for the first probes after running flips true.
		metadata := "null"
		if f.running {
			if f.metadataLagProbes > 0 {
				f.metadataLagProbes--
			} else {
				metadata = `{"pid":1234}`
			}
		}
		return fmt.Appendf(nil,
			`{"running":%v,"metadata":%s}`, f.running, metadata,
		), nil, 0, nil
	case strings.Contains(fragment, "serve"):
		f.startCalls++
		if f.startErr != nil {
			return nil, []byte(f.startErr.Error()), 1, nil
		}
		if f.flipOnStart {
			f.running = true
		}
		return nil, nil, 0, nil
	}
	return nil, []byte("unexpected fragment: " + fragment), 1, nil
}

func newEnsureRunner(t *testing.T, f *ensureFakeExec) *Runner {
	t.Helper()
	mgr := NewConnectionManager(t.TempDir(), Config{
		RunSSH: func([]string) (int, error) { return 0, nil },
	})
	r := NewRunnerWithExec(mgr, f.exec)
	r.ensurePollInterval = 5 * time.Millisecond
	r.ensureTimeout = 250 * time.Millisecond
	return r
}

// TestEnsureDaemonAlreadyRunning: a positive probe short-circuits —
// no start command is issued.
func TestEnsureDaemonAlreadyRunning(t *testing.T) {
	f := &ensureFakeExec{running: true}
	r := newEnsureRunner(t, f)
	require.NoError(t, r.EnsureDaemon(
		context.Background(), "studio", "wes@studio.local", "middleman",
	))
	assert.Equal(t, 0, f.startCalls)
}

// TestEnsureDaemonStartsAndPolls: a cold daemon gets exactly one
// detached serve start, then the poll observes it running.
func TestEnsureDaemonStartsAndPolls(t *testing.T) {
	f := &ensureFakeExec{flipOnStart: true}
	r := newEnsureRunner(t, f)
	require.NoError(t, r.EnsureDaemon(
		context.Background(), "studio", "wes@studio.local", "middleman",
	))
	assert.Equal(t, 1, f.startCalls)
	assert.GreaterOrEqual(t, f.probeCalls, 2,
		"probe before start and after")
}

// TestEnsureDaemonTimeout: a daemon that never comes up yields an
// error naming the destination instead of hanging.
func TestEnsureDaemonTimeout(t *testing.T) {
	f := &ensureFakeExec{}
	r := newEnsureRunner(t, f)
	err := r.EnsureDaemon(
		context.Background(), "studio", "wes@studio.local", "middleman",
	)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "wes@studio.local")
	assert.Equal(t, 1, f.startCalls)
}

// TestEnsureDaemonWaitsForMetadata: running with null metadata is not
// ready — the api verb needs the listen address, so ensure keeps
// polling until metadata appears.
func TestEnsureDaemonWaitsForMetadata(t *testing.T) {
	f := &ensureFakeExec{flipOnStart: true, metadataLagProbes: 3}
	r := newEnsureRunner(t, f)
	require.NoError(t, r.EnsureDaemon(
		context.Background(), "studio", "wes@studio.local", "middleman",
	))
	assert.Equal(t, 1, f.startCalls)
	assert.GreaterOrEqual(t, f.probeCalls, 5,
		"initial probe + lagging probes + the ready one")
}
