package sshfleet

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"go.kenn.io/middleman/internal/procutil"
)

// Package sshfleet owns the SSH transport for fleet peers the hub
// reaches over ssh(1) rather than HTTP: one ControlMaster socket per
// peer, multiplexing every remote CLI execution (snapshot reads,
// write relays, daemon probes) and any terminal attach a client runs
// through the same socket path. The hub is the single owner of this
// lifecycle — clients consume socket paths and connection state, they
// never spawn their own masters.

// Connection states, surfaced verbatim in connection events and the
// fleet snapshot's connectionState mapping.
const (
	StateConnecting   = "connecting"
	StateConnected    = "connected"
	StateProbeFailed  = "probe_failed"
	StateDisconnected = "disconnected"
	StateError        = "error"
)

const (
	establishPollInterval = 250 * time.Millisecond
	establishTimeout      = 10 * time.Second
	idleCheckInterval     = 30 * time.Second
)

// Event is a connection-state transition for one peer.
type Event struct {
	HostKey string `json:"host_key"`
	State   string `json:"state"`
	Message string `json:"message,omitempty"`
}

// Config controls ConnectionManager behavior.
type Config struct {
	// IdleTimeout disconnects masters with no activity for this long;
	// zero disables the idle monitor.
	IdleTimeout time.Duration
	// RunSSH executes ssh(1) with args and returns its exit code.
	// Injectable for tests; nil uses the real binary.
	RunSSH func(args []string) (int, error)
	// OnEvent observes state transitions; nil drops them.
	OnEvent func(Event)
}

type hostEntry struct {
	state       string
	destination string
	message     string
	lastActive  time.Time
}

// ConnectionManager manages per-peer SSH ControlMaster sockets under
// socketDir.
type ConnectionManager struct {
	socketDir string
	cfg       Config

	mu    sync.Mutex
	hosts map[string]*hostEntry

	// connectMu serializes Connect per host key so concurrent callers
	// coalesce on one master spawn instead of racing the socket file.
	connectMuMu sync.Mutex
	connectMu   map[string]*sync.Mutex
}

func NewConnectionManager(socketDir string, cfg Config) *ConnectionManager {
	if cfg.RunSSH == nil {
		cfg.RunSSH = runSSH
	}
	if cfg.OnEvent == nil {
		cfg.OnEvent = func(Event) {}
	}
	return &ConnectionManager{
		socketDir: socketDir,
		cfg:       cfg,
		hosts:     make(map[string]*hostEntry),
		connectMu: make(map[string]*sync.Mutex),
	}
}

// hostConnectMu returns the per-host Connect mutex for hostKey.
func (m *ConnectionManager) hostConnectMu(hostKey string) *sync.Mutex {
	m.connectMuMu.Lock()
	defer m.connectMuMu.Unlock()
	mu := m.connectMu[hostKey]
	if mu == nil {
		mu = &sync.Mutex{}
		m.connectMu[hostKey] = mu
	}
	return mu
}

// SocketPath returns the deterministic ControlMaster socket path for
// hostKey: a short content hash keeps it inside the unix-socket path
// length budget regardless of key length.
func (m *ConnectionManager) SocketPath(hostKey string) string {
	h := sha256.Sum256([]byte(hostKey))
	return filepath.Join(
		m.socketDir, fmt.Sprintf("%x.sock", h[:6]),
	)
}

// Connect establishes (or adopts) the ControlMaster for hostKey. A
// live master for the same destination is reused; a destination
// change tears the old master down first. Socket files surviving a
// daemon restart are adopted when still alive.
func (m *ConnectionManager) Connect(hostKey, destination string) error {
	// Serialize per host: a concurrent second caller waits for the
	// first spawn instead of racing it, then reuses the live master.
	connectMu := m.hostConnectMu(hostKey)
	connectMu.Lock()
	defer connectMu.Unlock()

	m.mu.Lock()
	entry := m.hosts[hostKey]
	socketPath := m.SocketPath(hostKey)

	if entry != nil && m.isAliveLocked(hostKey) {
		if entry.destination == destination {
			entry.lastActive = time.Now()
			entry.state = StateConnected
			entry.message = ""
			m.mu.Unlock()
			return nil
		}
		m.mu.Unlock()
		_ = m.Disconnect(hostKey)
		m.mu.Lock()
		entry = m.hosts[hostKey]
	}

	if entry == nil {
		if _, err := os.Stat(socketPath); err == nil &&
			m.checkSocketAlive(socketPath, destination) {
			m.hosts[hostKey] = &hostEntry{
				state:       StateConnected,
				destination: destination,
				lastActive:  time.Now(),
			}
			m.mu.Unlock()
			return nil
		}
	}

	if entry == nil {
		entry = &hostEntry{}
		m.hosts[hostKey] = entry
	}
	entry.state = StateConnecting
	entry.destination = destination
	entry.message = ""
	entry.lastActive = time.Now()
	m.mu.Unlock()

	m.emit(hostKey, StateConnecting, "")
	_ = os.Remove(socketPath)
	if err := os.MkdirAll(m.socketDir, 0o700); err != nil {
		m.setState(hostKey, StateError, err.Error())
		m.emit(hostKey, StateError, err.Error())
		return fmt.Errorf("create socket dir: %w", err)
	}

	// BatchMode fails fast instead of prompting, and ConnectTimeout
	// bounds a blackholed destination so a relay cannot hang behind
	// an unbounded master spawn.
	_, err := m.cfg.RunSSH([]string{
		"-MNf",
		"-o", "ControlPath=" + socketPath,
		"-o", "ServerAliveInterval=15",
		"-o", "ServerAliveCountMax=3",
		"-o", "BatchMode=yes",
		"-o", "ConnectTimeout=10",
		destination,
	})
	if err != nil {
		m.setState(hostKey, StateError, err.Error())
		m.emit(hostKey, StateError, err.Error())
		return fmt.Errorf("ssh master spawn for %s: %w", destination, err)
	}

	deadline := time.Now().Add(establishTimeout)
	for time.Now().Before(deadline) {
		if _, statErr := os.Stat(socketPath); statErr == nil {
			m.setState(hostKey, StateConnected, "")
			m.emit(hostKey, StateConnected, "")
			return nil
		}
		time.Sleep(establishPollInterval)
	}

	msg := "timeout waiting for control socket " + socketPath
	m.setState(hostKey, StateError, msg)
	m.emit(hostKey, StateError, msg)
	return fmt.Errorf("ssh establish for %s: %s", destination, msg)
}

// Disconnect tears down the master for hostKey (best effort).
func (m *ConnectionManager) Disconnect(hostKey string) error {
	socketPath := m.SocketPath(hostKey)
	m.mu.Lock()
	dest := ""
	if entry := m.hosts[hostKey]; entry != nil {
		dest = entry.destination
	}
	m.mu.Unlock()

	if dest != "" {
		_, _ = m.cfg.RunSSH([]string{
			"-o", "ControlPath=" + socketPath,
			"-O", "exit",
			dest,
		})
	}
	_ = os.Remove(socketPath)
	m.setState(hostKey, StateDisconnected, "")
	m.emit(hostKey, StateDisconnected, "")
	return nil
}

// IsAlive reports whether the master answers ssh -O check.
func (m *ConnectionManager) IsAlive(hostKey string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.isAliveLocked(hostKey)
}

func (m *ConnectionManager) isAliveLocked(hostKey string) bool {
	entry := m.hosts[hostKey]
	if entry == nil {
		return false
	}
	return m.checkSocketAlive(m.SocketPath(hostKey), entry.destination)
}

func (m *ConnectionManager) checkSocketAlive(
	socketPath, destination string,
) bool {
	exitCode, err := m.cfg.RunSSH([]string{
		"-o", "ControlPath=" + socketPath,
		"-O", "check",
		destination,
	})
	return err == nil && exitCode == 0
}

// State returns the tracked connection state for hostKey.
func (m *ConnectionManager) State(hostKey string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if entry := m.hosts[hostKey]; entry != nil {
		return entry.state
	}
	return StateDisconnected
}

// Destination returns the configured destination for hostKey.
func (m *ConnectionManager) Destination(hostKey string) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if entry := m.hosts[hostKey]; entry != nil {
		return entry.destination
	}
	return ""
}

// TouchActivity resets the idle timer for hostKey.
func (m *ConnectionManager) TouchActivity(hostKey string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if entry := m.hosts[hostKey]; entry != nil {
		entry.lastActive = time.Now()
	}
}

// SetProbeFailed marks hostKey as connected-but-unhealthy: the
// transport works but the remote daemon probe failed.
func (m *ConnectionManager) SetProbeFailed(hostKey, message string) {
	m.setState(hostKey, StateProbeFailed, message)
	m.emit(hostKey, StateProbeFailed, message)
}

// StartIdleMonitor disconnects idle masters in the background until
// done closes. No-op when IdleTimeout is zero.
func (m *ConnectionManager) StartIdleMonitor(done <-chan struct{}) {
	if m.cfg.IdleTimeout <= 0 {
		return
	}
	go func() {
		ticker := time.NewTicker(idleCheckInterval)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				m.disconnectIdle()
			}
		}
	}()
}

func (m *ConnectionManager) disconnectIdle() {
	m.mu.Lock()
	var idle []string
	for key, entry := range m.hosts {
		if (entry.state == StateConnected ||
			entry.state == StateProbeFailed) &&
			time.Since(entry.lastActive) > m.cfg.IdleTimeout {
			idle = append(idle, key)
		}
	}
	m.mu.Unlock()
	for _, key := range idle {
		_ = m.Disconnect(key)
	}
}

func (m *ConnectionManager) setState(hostKey, state, message string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	entry := m.hosts[hostKey]
	if entry == nil {
		entry = &hostEntry{}
		m.hosts[hostKey] = entry
	}
	entry.state = state
	entry.message = message
}

func (m *ConnectionManager) emit(hostKey, state, message string) {
	m.cfg.OnEvent(Event{
		HostKey: hostKey, State: state, Message: message,
	})
}

func runSSH(args []string) (int, error) {
	cmd := procutil.Command("ssh", args...)
	cmd.Stdout = nil
	cmd.Stderr = nil
	err := cmd.Run()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return exitErr.ExitCode(), err
		}
		return -1, err
	}
	return 0, nil
}
