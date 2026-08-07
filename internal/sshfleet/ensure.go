package sshfleet

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrRemoteDaemonUnavailable marks a relay that never reached the
// remote daemon (the api verb's exit-2 contract). Callers match it
// with errors.Is to trigger EnsureDaemon and retry.
var ErrRemoteDaemonUnavailable = errors.New("remote daemon unavailable")

const (
	defaultEnsurePollInterval = 500 * time.Millisecond
	defaultEnsureTimeout      = 15 * time.Second
)

// EnsureDaemon makes sure the peer's local daemon is up: a status
// probe short-circuits when it already runs; otherwise the remote CLI
// is started detached (`nohup ... serve`) and the probe polls until
// the daemon reports running or the ensure timeout expires. The
// remote daemon keeps its own lifecycle — a concurrent start loses
// the runtime lock and exits, so double-starts are harmless.
func (r *Runner) EnsureDaemon(
	ctx context.Context, connection Connection, remoteCommand string,
) error {
	running, err := r.probeDaemon(ctx, connection, remoteCommand)
	if err != nil {
		return err
	}
	if running {
		return nil
	}

	if err := r.startDaemonDetached(
		ctx, connection, remoteCommand,
	); err != nil {
		return err
	}

	deadline := time.Now().Add(r.ensureTimeout)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(r.ensurePollInterval):
		}
		running, err := r.probeDaemon(
			ctx, connection, remoteCommand,
		)
		if err == nil && running {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf(
				"daemon on %s did not come up within %s",
				connection.Target.String(), r.ensureTimeout,
			)
		}
	}
}

// probeDaemon runs `daemon status --json` on the peer and reports whether
// the daemon is ready to serve api-verb relays (running AND runtime
// metadata published).
func (r *Runner) probeDaemon(
	ctx context.Context, connection Connection, remoteCommand string,
) (bool, error) {
	out, err := r.RunVerb(
		ctx, connection, remoteCommand,
		[]string{"daemon", "status", "--json"},
	)
	if err != nil {
		return false, fmt.Errorf("probe daemon: %w", err)
	}
	var st struct {
		Running  bool            `json:"running"`
		Metadata json.RawMessage `json:"metadata"`
	}
	if err := json.Unmarshal(out, &st); err != nil {
		return false, fmt.Errorf("decode daemon status: %w", err)
	}
	// Running alone is not ready: the api verb also needs the runtime
	// metadata (listen address), which trails the lock early in
	// startup. Treat a null metadata as still warming.
	hasMetadata := len(st.Metadata) > 0 &&
		string(st.Metadata) != "null"
	return st.Running && hasMetadata, nil
}

// startDaemonDetached launches `serve` on the peer with nohup and all
// stdio detached so the ssh exec returns immediately while the daemon
// survives the session.
func (r *Runner) startDaemonDetached(
	ctx context.Context, connection Connection, remoteCommand string,
) error {
	fragment := normalizedPATH + "; nohup " + remoteCommand +
		" serve >/dev/null 2>&1 </dev/null &"
	argv, err := r.SSHCommand(connection, false)
	if err != nil {
		return err
	}
	argv = append(argv, "sh", "-lc", shellQuote(fragment))
	execCtx, cancel := context.WithTimeout(ctx, remoteExecTimeout)
	defer cancel()
	_, stderr, exitCode, err := r.execCommand(execCtx, argv, nil)
	if err != nil {
		return fmt.Errorf("start daemon on %s: %w", connection.Target.String(), err)
	}
	if exitCode != 0 {
		return fmt.Errorf(
			"start daemon on %s exited %d: %s",
			connection.Target.String(), exitCode, strings.TrimSpace(string(stderr)),
		)
	}
	return nil
}
