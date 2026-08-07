package sshfleet

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/kit/openssh"
)

type fakeConnectionManager struct {
	arguments      []string
	touchAllowed   bool
	touched        []string
	lastIdentity   string
	lastGeneration openssh.Generation
}

func (f *fakeConnectionManager) ConnectionArguments(
	identity string,
	generation openssh.Generation,
) ([]string, error) {
	f.lastIdentity = identity
	f.lastGeneration = generation
	return append([]string(nil), f.arguments...), nil
}

func (f *fakeConnectionManager) TouchActivity(
	identity string,
	generation openssh.Generation,
) bool {
	f.touched = append(f.touched, identity)
	f.lastGeneration = generation
	return f.touchAllowed
}

func testConnection() Connection {
	return Connection{
		Identity:   "studio",
		Generation: 7,
		Persistent: true,
		Target: openssh.Target{
			User: "wes", Hostname: "studio.local",
		},
	}
}

// TestRunnerRelayFramesStatusAndBody pins the relay contract: the
// remote api -i framing decodes into status + body for both success and
// HTTP-error exits, the ssh argv uses the generation-bound manager arguments,
// and the remote fragment normalizes PATH and quotes the verb.
func TestRunnerRelayFramesStatusAndBody(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)

	mgr := &fakeConnectionManager{
		arguments: []string{
			"-o", "ControlMaster=no", "-S", "/tmp/control.sock",
		},
		touchAllowed: true,
	}
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
		testConnection(), "kenn-forge",
		"POST", "/api/v1/projects/prj_1/worktrees",
		[]byte(`{"branch":"feat"}`),
	)
	require.NoError(err)
	assert.Equal(201, resp.Status)
	assert.JSONEq(`{"id":"wtr_1"}`, string(resp.Body))
	assert.JSONEq(`{"branch":"feat"}`, string(gotStdin))

	joined := strings.Join(gotArgv, " ")
	assert.Equal("ssh", gotArgv[0])
	assert.Contains(joined, "/tmp/control.sock")
	assert.Contains(joined, "ControlMaster=no")
	assert.Contains(joined, "wes@studio.local")
	assert.Contains(joined, "kenn-forge")
	assert.Contains(joined, "api")
	assert.Contains(joined, "PATH=")
	assert.Equal("studio", mgr.lastIdentity)
	assert.Equal(openssh.Generation(7), mgr.lastGeneration)
	assert.NotEmpty(mgr.touched)

	// HTTP-error exit still yields the framed problem body.
	runner.execCommand = func(
		context.Context, []string, []byte,
	) ([]byte, []byte, int, error) {
		return []byte("HTTP/1.1 404 Not Found\r\n\r\n{\"code\":\"notFound\"}"),
			nil, verbExitHTTPError, nil
	}
	resp, err = runner.Relay(
		context.Background(),
		testConnection(), "kenn-forge",
		"GET", "/api/v1/projects/prj_missing", nil,
	)
	require.NoError(err)
	assert.Equal(404, resp.Status)
	assert.Contains(string(resp.Body), "notFound")

	// No-request exit surfaces the remote stderr as the error.
	runner.execCommand = func(
		context.Context, []string, []byte,
	) ([]byte, []byte, int, error) {
		return nil, []byte("no kenn-forge daemon is running on /data"),
			verbExitNoRequest, nil
	}
	_, err = runner.Relay(
		context.Background(),
		testConnection(), "kenn-forge",
		"GET", "/api/v1/snapshot/raw", nil,
	)
	require.Error(err)
	assert.Contains(err.Error(), "remote daemon unavailable")
	assert.Contains(err.Error(), "no kenn-forge daemon is running")
}

// TestRelayNoRequestErrorIsTyped pins that an exit-2 relay (no
// request reached the remote daemon) is matchable with errors.Is so
// callers can trigger daemon auto-start on exactly this failure.
func TestRelayNoRequestErrorIsTyped(t *testing.T) {
	mgr := &fakeConnectionManager{touchAllowed: true}
	runner := NewRunnerWithExec(mgr, func(
		context.Context, []string, []byte,
	) ([]byte, []byte, int, error) {
		return nil, []byte("no kenn-forge daemon is running"),
			verbExitNoRequest, nil
	})
	_, err := runner.Relay(
		context.Background(),
		testConnection(), "kenn-forge",
		"GET", "/api/v1/snapshot/raw", nil,
	)
	require.ErrorIs(t, err, ErrRemoteDaemonUnavailable)
}

func TestSSHCommandRejectsChangedGeneration(t *testing.T) {
	runner := NewRunner(&fakeConnectionManager{touchAllowed: false})

	_, err := runner.SSHCommand(testConnection(), false)

	require.ErrorIs(t, err, openssh.ErrConnectionChanged)
}

func TestSSHCommandSupportsMasterlessConnection(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	connections := &fakeConnectionManager{}
	runner := NewRunner(connections)
	connection := testConnection()
	connection.Persistent = false

	arguments, err := runner.SSHCommand(connection, false)

	require.NoError(err)
	assert.Contains(arguments, "none")
	assert.Contains(arguments, "wes@studio.local")
	assert.Empty(connections.touched)
}

// ensureFakeExec scripts the remote side of EnsureDaemon: status
// probes answer from `running`, and a detached serve start flips
// `running` after startDelayProbes more probes.
type ensureFakeExec struct {
	mu          sync.Mutex
	running     bool
	startCalls  int
	probeCalls  int
	fragments   []string
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
	f.fragments = append(f.fragments, fragment)
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
	mgr := &fakeConnectionManager{touchAllowed: true}
	r := NewRunnerWithExec(mgr, f.exec)
	r.ensurePollInterval = 5 * time.Millisecond
	r.ensureTimeout = 250 * time.Millisecond
	return r
}

// TestEnsureDaemonAlreadyRunning: a positive probe short-circuits —
// no start command is issued.
func TestEnsureDaemonAlreadyRunning(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	f := &ensureFakeExec{running: true}
	r := newEnsureRunner(t, f)
	require.NoError(r.EnsureDaemon(
		context.Background(), testConnection(), "kenn-forge",
	))
	assert.Equal(0, f.startCalls)
	require.NotEmpty(f.fragments)
	assert.Equal(shellQuote(
		normalizedPATH+"; kenn-forge "+
			shellQuote("daemon")+" "+shellQuote("status")+" "+shellQuote("--json"),
	), f.fragments[0])
}

// TestEnsureDaemonStartsAndPolls: a cold daemon gets exactly one
// detached serve start, then the poll observes it running.
func TestEnsureDaemonStartsAndPolls(t *testing.T) {
	f := &ensureFakeExec{flipOnStart: true}
	r := newEnsureRunner(t, f)
	require.NoError(t, r.EnsureDaemon(
		context.Background(), testConnection(), "kenn-forge",
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
		context.Background(), testConnection(), "kenn-forge",
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
		context.Background(), testConnection(), "kenn-forge",
	))
	assert.Equal(t, 1, f.startCalls)
	assert.GreaterOrEqual(t, f.probeCalls, 5,
		"initial probe + lagging probes + the ready one")
}
