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
	"go.kenn.io/kit/atomicfile"
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
func acpHostKey(command, strip []string) string {
	sum := sha256.Sum256([]byte(strings.Join(command, "\x00") + "\x01" + strings.Join(strip, "\x00")))
	return "host-" + hex.EncodeToString(sum[:8])
}

// startACPAgent starts a chat's agent session, on a shared host when this
// agent shares one, and otherwise in a process of its own.
func (m *Manager) startACPAgent(ctx context.Context, command []string, cwd string, strip []string, saved *acpSavedSession) (*ACP, error) {
	if m.shareACPAgents {
		conn, err := m.dialACPHost(ctx, command, strip)
		if err == nil {
			var a *ACP
			a, err = startACPHostSession(ctx, conn, cwd, m.agentMCPServers(), saved)
			if err == nil || errors.Is(err, ErrACPCannotReload) || ctx.Err() != nil {
				return a, err
			}
		}
		if !errors.Is(err, errACPHostUnshareable) {
			// A start of its own reports the agent's own failure, if any.
			slog.Warn("start shared ACP agent host; starting the agent for this chat alone", "err", err)
		}
	}
	return startACPSession(ctx, command, cwd, strip, m.agentMCPServers(), saved)
}

// dialACPHost connects to the current host for command, starting a new one
// when there is none, or when the agent changed since the host started.
func (m *Manager) dialACPHost(ctx context.Context, command, strip []string) (net.Conn, error) {
	root := m.acpHostsDir()
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, err
	}
	key := acpHostKey(command, strip)
	lock := flock.New(filepath.Join(root, key+".lock"))
	if _, err := lock.TryLockContext(ctx, 25*time.Millisecond); err != nil {
		return nil, err
	}
	defer func() { _ = lock.Unlock() }()
	pointerPath := filepath.Join(root, key+".json")
	pointer, err := readACPHostPointer(pointerPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	env := SessionEnvironment(os.Environ(), strip)
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
	return m.startACPHost(ctx, root, key, pointerPath, command, strip)
}

func dialReadyACPHost(ctx context.Context, paths ptyowner.SessionPaths) (net.Conn, acpHostReady, error) {
	var ready acpHostReady
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", paths.Socket)
	if err != nil {
		return nil, ready, err
	}
	data, err := os.ReadFile(filepath.Join(paths.Dir, acpHostReadyFile))
	if err == nil {
		err = json.Unmarshal(data, &ready)
	}
	if err != nil {
		_ = conn.Close()
		return nil, ready, err
	}
	return conn, ready, nil
}

// startACPHost starts a host and makes it current once its agent is ready.
// An agent that cannot share is remembered, and this chat alone uses it.
func (m *Manager) startACPHost(ctx context.Context, root, key, pointerPath string, command, strip []string) (net.Conn, error) {
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
	cfg := acpHostConfig{Command: command, Strip: strip, Dir: paths.Dir, Socket: paths.Socket, Pointer: pointerPath, Generation: generation, IdleTimeoutMillis: m.acpHostIdleTimeout.Milliseconds()}
	data, err := json.Marshal(cfg)
	if err != nil {
		return nil, err
	}
	configPath := filepath.Join(paths.Dir, "config.json")
	if err := atomicfile.WriteFile(configPath, data, atomicfile.WithPerm(0o600)); err != nil && !errors.Is(err, atomicfile.ErrPublished) {
		return nil, err
	}
	logFile, err := os.OpenFile(filepath.Join(paths.Dir, "host.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	hostCommand := m.acpHostCommand
	if len(hostCommand) == 0 {
		executable, err := os.Executable()
		if err != nil {
			_ = logFile.Close()
			return nil, err
		}
		hostCommand = []string{executable, "acp-host"}
	}
	// The host outlives this chat, so the start does not take its context.
	cmd := procutil.Command(hostCommand[0], append(slices.Clone(hostCommand[1:]), configPath)...)
	cmd.Dir = paths.Dir
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	detachACPHost(cmd)
	err = cmd.Start()
	_ = logFile.Close()
	if err != nil {
		return nil, err
	}
	exited := make(chan struct{})
	go func() { _ = cmd.Wait(); close(exited) }()
	waitCtx, cancel := context.WithTimeout(ctx, acpHandshakeTimeout+5*time.Second)
	defer cancel()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		conn, ready, err := dialReadyACPHost(waitCtx, paths)
		if err == nil {
			pointer := acpHostPointer{Generation: generation}
			if !ready.Shareable {
				pointer = acpHostPointer{Unshareable: &ready.Fingerprint}
			}
			if err := writeACPHostPointer(pointerPath, pointer); err != nil {
				_ = conn.Close()
				return nil, err
			}
			return conn, nil
		}
		select {
		case <-exited:
			record, _ := os.ReadFile(filepath.Join(paths.Dir, acpStartErrorFile))
			_ = os.RemoveAll(paths.Dir)
			if startErr := acpStartError(string(record)); startErr != nil {
				return nil, fmt.Errorf("start ACP agent host: %w", startErr)
			}
			return nil, errors.New("ACP agent host exited before it was ready")
		case <-waitCtx.Done():
			_ = cmd.Process.Kill()
			return nil, fmt.Errorf("ACP agent host did not start: %w", waitCtx.Err())
		case <-ticker.C:
		}
	}
}
