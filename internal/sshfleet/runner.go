package sshfleet

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"go.kenn.io/middleman/internal/procutil"
)

// The runner relays HTTP requests to a peer's local daemon by
// executing its CLI `api` verb over the ControlMaster: the remote
// listener stays private to its host, no ports are forwarded, and
// auth rides the remote's own token file. The verb's contract is the
// transport contract: response bytes on stdout (with -i, the exact
// status line first), exit 0/1/2 for success / HTTP error /
// no-request-made.

// remoteExecTimeout bounds one relayed request end to end.
const remoteExecTimeout = 90 * time.Second

const (
	verbExitHTTPError = 1
	verbExitNoRequest = 2
)

// normalizedPATH prepends the conventional user-install locations so
// a non-interactive ssh shell finds the remote CLI even when its
// profile does not export them.
const normalizedPATH = `PATH="$HOME/.local/bin:$HOME/bin:/opt/homebrew/bin:/usr/local/bin:$PATH"`

// Response is one relayed HTTP exchange.
type Response struct {
	Status int
	Body   []byte
}

// Runner executes remote CLI verbs over a peer's ControlMaster.
type Runner struct {
	conns *ConnectionManager
	// execCommand runs argv with stdin and returns stdout, stderr,
	// and the exit code. Injectable for tests.
	execCommand func(
		ctx context.Context, argv []string, stdin []byte,
	) (stdout, stderr []byte, exitCode int, err error)

	// EnsureDaemon pacing; defaults suit real ssh latency, tests
	// shrink them.
	ensurePollInterval time.Duration
	ensureTimeout      time.Duration
}

func NewRunner(conns *ConnectionManager) *Runner {
	return NewRunnerWithExec(conns, execLocal)
}

// NewRunnerWithExec builds a Runner with a custom executor — the
// seam consumers use to fake the ssh subprocess in tests.
func NewRunnerWithExec(
	conns *ConnectionManager,
	exec func(
		ctx context.Context, argv []string, stdin []byte,
	) (stdout, stderr []byte, exitCode int, err error),
) *Runner {
	return &Runner{
		conns:              conns,
		execCommand:        exec,
		ensurePollInterval: defaultEnsurePollInterval,
		ensureTimeout:      defaultEnsureTimeout,
	}
}

// Relay performs METHOD path with body against hostKey's remote
// daemon via its CLI. remoteCommand is the peer's configured CLI
// invocation (default "middleman"); it may carry flags and is
// embedded as a shell fragment.
func (r *Runner) Relay(
	ctx context.Context,
	hostKey, destination, remoteCommand string,
	method, path string,
	body []byte,
) (Response, error) {
	r.conns.TouchActivity(hostKey)

	verbArgs := []string{"api", "-i", method, path}
	var stdin []byte
	if len(body) > 0 {
		verbArgs = []string{"api", "-i", "-d", "@-", method, path}
		stdin = body
	}

	argv := r.sshArgv(hostKey, destination, remoteCommand, verbArgs)
	execCtx, cancel := context.WithTimeout(ctx, remoteExecTimeout)
	defer cancel()
	stdout, stderr, exitCode, err := r.execCommand(execCtx, argv, stdin)
	if err != nil {
		return Response{}, fmt.Errorf(
			"ssh relay %s %s on %s: %w", method, path, destination, err,
		)
	}
	switch exitCode {
	case 0, verbExitHTTPError:
		return parseStatusFramedResponse(stdout)
	case verbExitNoRequest:
		return Response{}, fmt.Errorf(
			"%w on %s: %s",
			ErrRemoteDaemonUnavailable,
			destination, strings.TrimSpace(string(stderr)),
		)
	default:
		return Response{}, fmt.Errorf(
			"remote CLI exited %d on %s: %s",
			exitCode, destination, strings.TrimSpace(string(stderr)),
		)
	}
}

// RunVerb executes an arbitrary CLI verb (e.g. "status --json") on
// the peer and returns stdout. Non-zero exits return an error
// carrying stderr.
func (r *Runner) RunVerb(
	ctx context.Context,
	hostKey, destination, remoteCommand string,
	verbArgs []string,
) ([]byte, error) {
	r.conns.TouchActivity(hostKey)
	argv := r.sshArgv(hostKey, destination, remoteCommand, verbArgs)
	execCtx, cancel := context.WithTimeout(ctx, remoteExecTimeout)
	defer cancel()
	stdout, stderr, exitCode, err := r.execCommand(execCtx, argv, nil)
	if err != nil {
		return nil, fmt.Errorf(
			"ssh verb on %s: %w", destination, err,
		)
	}
	if exitCode != 0 {
		return nil, fmt.Errorf(
			"remote verb exited %d on %s: %s",
			exitCode, destination, strings.TrimSpace(string(stderr)),
		)
	}
	return stdout, nil
}

// sshArgv builds the ssh(1) invocation that runs the remote CLI verb
// through the peer's ControlMaster.
func (r *Runner) sshArgv(
	hostKey, destination, remoteCommand string,
	verbArgs []string,
) []string {
	var b strings.Builder
	b.WriteString(normalizedPATH)
	b.WriteString("; ")
	b.WriteString(remoteCommand)
	for _, arg := range verbArgs {
		b.WriteByte(' ')
		b.WriteString(shellQuote(arg))
	}
	return []string{
		"ssh",
		"-o", "ControlPath=" + r.conns.SocketPath(hostKey),
		"-o", "ControlMaster=no",
		destination,
		"sh", "-lc", shellQuote(b.String()),
	}
}

// parseStatusFramedResponse decodes the verb's -i framing: a status
// line, a blank line, then the body verbatim.
func parseStatusFramedResponse(out []byte) (Response, error) {
	reader := bufio.NewReader(bytes.NewReader(out))
	statusLine, err := reader.ReadString('\n')
	if err != nil {
		return Response{}, fmt.Errorf(
			"malformed relay output: missing status line",
		)
	}
	fields := strings.Fields(statusLine)
	if len(fields) < 2 || !strings.HasPrefix(fields[0], "HTTP/") {
		return Response{}, fmt.Errorf(
			"malformed relay status line %q", strings.TrimSpace(statusLine),
		)
	}
	status, err := strconv.Atoi(fields[1])
	if err != nil {
		return Response{}, fmt.Errorf(
			"malformed relay status code %q", fields[1],
		)
	}
	// Consume the blank separator line.
	if _, err := reader.ReadString('\n'); err != nil {
		return Response{Status: status}, nil
	}
	var body bytes.Buffer
	if _, err := body.ReadFrom(reader); err != nil {
		return Response{}, fmt.Errorf("read relay body: %w", err)
	}
	return Response{Status: status, Body: body.Bytes()}, nil
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func execLocal(
	ctx context.Context, argv []string, stdin []byte,
) ([]byte, []byte, int, error) {
	cmd := procutil.CommandContext(ctx, argv[0], argv[1:]...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := procutil.Run(ctx, cmd, "ssh fleet relay")
	exitCode := 0
	if err != nil {
		// ProcessState is nil when the process never started (missing
		// ssh binary, limiter failure): that is a transport error, not
		// an exit code.
		if cmd.ProcessState == nil {
			return nil, stderr.Bytes(), -1, err
		}
		exitCode = cmd.ProcessState.ExitCode()
		if exitCode < 0 {
			return nil, stderr.Bytes(), -1, err
		}
		err = nil
	}
	return stdout.Bytes(), stderr.Bytes(), exitCode, err
}
