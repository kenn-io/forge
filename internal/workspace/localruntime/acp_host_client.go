package localruntime

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/v2"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/gofrs/flock"

	"go.kenn.io/forge/internal/procutil"
	"go.kenn.io/forge/internal/ptyowner"
	"go.kenn.io/forge/internal/tracing"
	"go.kenn.io/kit/atomicfile"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// errACPHostUnshareable reports an agent that runs one chat per process.
var errACPHostUnshareable = errors.New("agent cannot share a process between chats")

// acpHostPointer names the current host for one agent command.
type acpHostPointer struct {
	Generation string
	// Unshareable is the fingerprint of an agent found unable to share. Its
	// chats start their own agent until the agent changes.
	Unshareable *acpHostFingerprint
}

func readACPHostPointer(path string) (acpHostPointer, error) {
	var pointer acpHostPointer
	data, err := os.ReadFile(path)
	if err != nil {
		return pointer, err
	}
	return pointer, json.Unmarshal(data, &pointer)
}

func writeACPHostPointer(path string, pointer acpHostPointer) error {
	data, err := json.Marshal(pointer)
	if err != nil {
		return err
	}
	if err := atomicfile.WriteFile(path, data, atomicfile.WithPerm(0o600)); err != nil && !errors.Is(err, atomicfile.ErrPublished) {
		return err
	}
	return nil
}

func (m *Manager) acpHostsDir() string {
	return filepath.Join(filepath.Dir(m.acpSessionsDir), "acp-hosts")
}

// acpHostKey names the host an agent command shares: chats launched the same
// way may share a process.
func acpHostKey(command, strip []string, env map[string]string) string {
	identity := strings.Join(command, "\x00") + "\x01" + strings.Join(strip, "\x00") + "\x01" + strings.Join(procutil.MergeEnvironment(nil, env), "\x00")
	sum := sha256.Sum256([]byte(identity))
	return "host-" + hex.EncodeToString(sum[:8])
}

// startACPAgent starts a chat's agent session, on a shared host when this
// agent shares one, and otherwise in a process of its own.
func (m *Manager) startACPAgent(ctx context.Context, command []string, cwd string, strip []string, agentEnv map[string]string, saved *acpSavedSession) (_ *ACP, err error) {
	ctx, span := acpTracer.Start(ctx, "acp.agent.start", trace.WithAttributes(attribute.Bool("acp.shared_host", m.shareACPAgents)))
	defer func() { tracing.EndSpan(span, err) }()
	if !m.shareACPAgents {
		return startACPSession(ctx, command, cwd, strip, agentEnv, m.agentMCPServers(), saved)
	}
	// One handshake's time covers the host, this chat's handshake, and a
	// start of its own, so the daemon's wait for this owner never runs out
	// first.
	shared, cancel := context.WithTimeout(ctx, acpHandshakeTimeout)
	defer cancel()
	conn, err := m.dialACPHost(shared, command, strip, agentEnv)
	if err == nil {
		var a *ACP
		a, err = startACPHostSession(shared, conn, cwd, m.agentMCPServers(), saved)
		if err == nil || errors.Is(err, ErrACPCannotReload) {
			return a, err
		}
	}
	if shared.Err() != nil {
		// A host too slow to finish leaves a start of its own no time.
		return nil, fmt.Errorf("start shared ACP agent host: %w", err)
	}
	if !errors.Is(err, errACPHostUnshareable) {
		// A start of its own reports the agent's own failure, if any.
		slog.Warn("start shared ACP agent host; starting the agent for this chat alone", "err", err)
	}
	return startACPSession(shared, command, cwd, strip, agentEnv, m.agentMCPServers(), saved)
}

// dialACPHost connects to the current host for command, starting a new one
// when there is none, or when the agent changed since the host started.
func (m *Manager) dialACPHost(ctx context.Context, command, strip []string, agentEnv map[string]string) (_ net.Conn, err error) {
	ctx, span := acpTracer.Start(ctx, "acp.host.dial")
	defer func() { tracing.EndSpan(span, err) }()
	root := m.acpHostsDir()
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, err
	}
	key := acpHostKey(command, strip, agentEnv)
	lock := flock.New(filepath.Join(root, key+".lock"))
	if _, err := lock.TryLockContext(ctx, 25*time.Millisecond); err != nil {
		return nil, err
	}
	span.AddEvent("host lock acquired")
	defer func() { _ = lock.Unlock() }()
	pointerPath := filepath.Join(root, key+".json")
	pointer, err := readACPHostPointer(pointerPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	env := procutil.MergeEnvironment(SessionEnvironment(os.Environ(), strip), agentEnv)
	if pointer.Unshareable != nil && acpHostStale(ctx, *pointer.Unshareable, command, root, env) == "" {
		return nil, errACPHostUnshareable
	}
	if pointer.Generation != "" {
		paths, err := ptyowner.NewSessionPaths(root, key+"-"+pointer.Generation)
		if err != nil {
			return nil, err
		}
		if conn, ready, err := dialReadyACPHost(ctx, paths); err == nil {
			reason := acpHostStale(ctx, ready.Fingerprint, command, paths.Dir, env)
			if reason == "" {
				return conn, nil
			}
			// The old host keeps its chats and exits after the last one.
			_ = conn.Close()
			slog.Info("starting a new shared ACP agent host", "reason", reason)
		}
	}
	return m.startACPHost(ctx, root, key, pointerPath, command, strip, agentEnv)
}

func dialReadyACPHost(ctx context.Context, paths ptyowner.SessionPaths) (net.Conn, acpHostReady, error) {
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", paths.Socket)
	if err != nil {
		return nil, acpHostReady{}, err
	}
	ready, err := readACPHostReady(paths.Dir)
	if err != nil {
		_ = conn.Close()
		return nil, ready, err
	}
	return conn, ready, nil
}

// readACPHostReady reads what a host wrote before it listened.
func readACPHostReady(dir string) (acpHostReady, error) {
	var ready acpHostReady
	data, err := os.ReadFile(filepath.Join(dir, acpHostReadyFile))
	if err != nil {
		return ready, err
	}
	return ready, json.Unmarshal(data, &ready)
}

// startACPHost starts a host and makes it current once its agent is ready.
// An agent that cannot share is remembered until it changes, and the host
// ends.
func (m *Manager) startACPHost(ctx context.Context, root, key, pointerPath string, command, strip []string, agentEnv map[string]string) (_ net.Conn, err error) {
	ctx, span := acpTracer.Start(ctx, "acp.host.start")
	defer func() { tracing.EndSpan(span, err) }()
	generation := strings.ToLower(rand.Text()[:12])
	paths, err := ptyowner.NewSessionPaths(root, key+"-"+generation)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(paths.Dir, 0o700); err != nil {
		return nil, err
	}
	if paths.SocketDir != "" {
		if err := os.MkdirAll(paths.SocketDir, 0o700); err != nil {
			return nil, err
		}
	}
	cfg := acpHostConfig{Command: command, Strip: strip, Env: agentEnv, Dir: paths.Dir, Socket: paths.Socket, Pointer: pointerPath, Generation: generation, IdleTimeoutMillis: m.acpHostIdleTimeout.Milliseconds(), TraceContext: injectTraceContext(ctx)}
	data, err := json.Marshal(cfg)
	if err != nil {
		return nil, err
	}
	configPath := filepath.Join(paths.Dir, "config.json")
	if err := atomicfile.WriteFile(configPath, data, atomicfile.WithPerm(0o600)); err != nil && !errors.Is(err, atomicfile.ErrPublished) {
		return nil, err
	}
	hostCommand := m.acpHostCommand
	if len(hostCommand) == 0 {
		executable, err := os.Executable()
		if err != nil {
			return nil, err
		}
		hostCommand = []string{executable, "acp-host"}
	}
	// The host outlives this chat, so its start does not take the chat's
	// context. A failed start reaches this chat through the start-error
	// record, as an owner's does.
	cmd := procutil.Command(hostCommand[0], append(slices.Clone(hostCommand[1:]), configPath)...)
	cmd.Dir = paths.Dir
	procutil.DetachCommand(cmd)
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	exited := make(chan struct{})
	go func() { _ = cmd.Wait(); close(exited) }()
	conn, err := dialStartingACPProcess(ctx, paths.Dir, paths.Socket, exited)
	if err != nil {
		_ = cmd.Process.Kill()
		_ = os.RemoveAll(paths.Dir)
		return nil, fmt.Errorf("start ACP agent host: %w", err)
	}
	// The host writes this before it listens.
	ready, err := readACPHostReady(paths.Dir)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	if !ready.Shareable {
		// The probe is remembered until the agent changes. Closing the only
		// connection ends the host, and this chat starts in its own worktree
		// like every later one.
		_ = conn.Close()
		if err := writeACPHostPointer(pointerPath, acpHostPointer{Unshareable: &ready.Fingerprint}); err != nil {
			return nil, err
		}
		return nil, errACPHostUnshareable
	}
	if err := writeACPHostPointer(pointerPath, acpHostPointer{Generation: generation}); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return conn, nil
}
