package localruntime

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"net"
	"net/rpc"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"syscall"
	"time"

	"go.kenn.io/forge/internal/agentactivity"
	"go.kenn.io/forge/internal/ptyowner"
	"go.kenn.io/kit/atomicfile"
)

// The owner runs beneath tmux/ptyowner. Only this process owns the SDK pipes,
// so detaching the daemon cannot cancel a turn or a pending permission.
type acpOwnerConfig struct {
	Root        string
	Info        SessionInfo
	Command     []string
	CWD         string
	Strip       []string
	Preferences string
	Activity    string
	MCP         ACPMCPBinding
}

type acpSavedSession struct {
	SessionID string
	State     ACPState
}

func (m *Manager) acpSessionPath(key string) string {
	paths, err := ptyowner.NewSessionPaths(m.acpSessionsDir, key)
	if err != nil {
		return ""
	}
	return filepath.Join(paths.Dir, "session.json")
}

func (a *ACP) persistLocked() error {
	if a.recordPath == "" {
		return nil
	}
	data, err := json.Marshal(acpSavedSession{SessionID: a.sessionID, State: a.state}, json.Deterministic(true))
	if err != nil {
		return err
	}
	err = atomicfile.WriteFile(a.recordPath, data, atomicfile.WithPerm(0o600))
	if errors.Is(err, atomicfile.ErrPublished) {
		return nil
	}
	return err
}

// restoreTranscriptLocked makes the saved transcript the conversation of
// record after a reload. A reloaded conversation never starts queued work on
// its own.
func (a *ACP) restoreTranscriptLocked(saved ACPState) {
	a.state.Messages = saved.Messages
	a.state.Queue = saved.Queue
	a.state.QueuePaused = len(a.state.Queue) > 0
	a.state.Plan = saved.Plan
	// Commands the agent advertised while reloading are current; otherwise
	// keep the last set until it sends a new one.
	if a.state.Commands == nil {
		a.state.Commands = saved.Commands
	}
}

func (m *Manager) acpLaunchCommand(ctx context.Context, target LaunchTarget, workspaceID, key, cwd string) (launchCommand, error) {
	if m.acpSessionsDir == "" {
		return launchCommand{}, errors.New("ACP owner state directory is required")
	}
	paths, err := ptyowner.NewSessionPaths(m.acpSessionsDir, key)
	if err != nil {
		return launchCommand{}, err
	}
	if err := os.MkdirAll(paths.Dir, 0o700); err != nil {
		return launchCommand{}, err
	}
	cfg := acpOwnerConfig{Root: m.acpSessionsDir, Info: SessionInfo{Key: key, WorkspaceID: workspaceID, TargetKey: target.Key, Kind: LaunchTargetACP}, Command: target.Command, CWD: cwd, Strip: m.currentStripEnvVars(), Preferences: m.acpPreferencesPath, Activity: m.agentActivityDir, MCP: ACPMCPBinding{URL: m.agentMCPURL, Token: m.agentMCPToken}}
	data, err := json.Marshal(cfg)
	if err != nil {
		return launchCommand{}, err
	}
	configPath := filepath.Join(paths.Dir, "config.json")
	if err := atomicfile.WriteFile(configPath, data, atomicfile.WithPerm(0o600)); err != nil && !errors.Is(err, atomicfile.ErrPublished) {
		return launchCommand{}, err
	}
	command := slices.Clone(m.acpOwnerCommand)
	if len(command) == 0 {
		executable, err := os.Executable()
		if err != nil {
			return launchCommand{}, err
		}
		command = []string{executable, "acp-owner"}
	}
	command = append(command, configPath)
	return m.shellLaunchCommand(ctx, command, workspaceID, key, cwd)
}

func (m *Manager) startACPOwner(ctx context.Context, info SessionInfo, command []string, cwd string, strip []string) (*session, error) {
	var backend *session
	if info.TmuxSession == "" {
		if m.ptyOwnerRuntime == nil {
			return nil, errors.New("ACP requires tmux or ptyowner")
		}
		var err error
		backend, err = startPtyOwnerSession(ctx, m.ptyOwnerRuntime, info, command, cwd, strip)
		if err != nil {
			return nil, err
		}
		// Drain/wait for the PTY just like a terminal. ACP traffic uses the socket.
		go backend.watch()
	}
	attached, err := m.attachACPOwner(ctx, info)
	if err != nil {
		if backend != nil {
			_ = backend.stop(context.WithoutCancel(ctx))
		}
		return nil, err
	}
	if backend != nil {
		backend.detach()
	}
	return attached, nil
}

func (m *Manager) restoreACP(ctx context.Context, info SessionInfo, cwd string) (*session, error) {
	// A running owner is authoritative even if the configured executable changed.
	paths, err := ptyowner.NewSessionPaths(m.acpSessionsDir, info.Key)
	if err != nil {
		return nil, err
	}
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", paths.Socket)
	if err == nil {
		return m.acpAttachment(ctx, info, conn)
	}
	// Only start a replacement when the backend is definitely gone. Do not turn
	// a slow/unavailable owner into a second agent with the same conversation.
	if info.TmuxSession != "" {
		err := m.requireTmuxSession(ctx, info.TmuxSession)
		if err == nil {
			return nil, fmt.Errorf("%w: ACP owner unavailable", ErrSessionUnavailable)
		}
		if !errors.Is(err, ErrSessionNotFound) {
			return nil, err
		}
	} else if m.ptyOwnerRuntime != nil {
		backend, err := m.ptyOwnerRuntime.Attach(ctx, info.Key)
		if err == nil {
			defer backend.Close()
			select {
			case <-backend.Done():
				if err := m.ptyOwnerRuntime.Stop(ctx, info.Key); err != nil {
					return nil, err
				}
			default:
				return nil, fmt.Errorf("%w: ACP owner unavailable", ErrSessionUnavailable)
			}
		} else if !errors.Is(err, ptyowner.ErrOwnerGone) {
			return nil, err
		}
	}
	data, err := os.ReadFile(filepath.Join(paths.Dir, "config.json"))
	if err != nil {
		return nil, fmt.Errorf("%w: read ACP owner configuration: %w", ErrSessionUnavailable, err)
	}
	var cfg acpOwnerConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	// The owner's saved SDK session ID is loaded in the replacement process.
	if _, err := os.Stat(m.acpSessionPath(info.Key)); err != nil {
		return nil, fmt.Errorf("%w: saved ACP session: %w", ErrSessionUnavailable, err)
	}
	launch, err := m.acpLaunchCommand(ctx, LaunchTarget{Key: info.TargetKey, Kind: LaunchTargetACP, Command: cfg.Command}, info.WorkspaceID, info.Key, cwd)
	if err != nil {
		return nil, err
	}
	info.TmuxSession = launch.TmuxSession
	return m.startACPOwner(ctx, info, launch.Command, cwd, m.currentStripEnvVars())
}

func (m *Manager) attachACPOwner(ctx context.Context, info SessionInfo) (*session, error) {
	paths, err := ptyowner.NewSessionPaths(m.acpSessionsDir, info.Key)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 35*time.Second)
	defer cancel()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		conn, err := (&net.Dialer{}).DialContext(ctx, "unix", paths.Socket)
		if err == nil {
			return m.acpAttachment(ctx, info, conn)
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("attach ACP owner: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}

func (m *Manager) acpAttachment(ctx context.Context, info SessionInfo, conn net.Conn) (*session, error) {
	client := rpc.NewClient(conn)
	remote := &acpAttachment{address: conn.RemoteAddr().String(), client: client, done: make(chan struct{}), subscribers: make(map[chan struct{}]struct{})}
	var reply struct{}
	if err := remote.call(ctx, "ACP.Bind", ACPMCPBinding{URL: m.agentMCPURL, Token: m.agentMCPToken}, &reply); err != nil {
		_ = client.Close()
		return nil, err
	}
	info.Status = SessionStatusRunning
	go remote.watch()
	return &session{info: info, tmuxSession: info.TmuxSession, acp: remote, lifecycle: remote, done: make(chan struct{})}, nil
}

// RunACPOwner is the hidden executable entry point, not a daemon-start task.
func RunACPOwner(ctx context.Context, configPath string) error {
	data, err := os.ReadFile(configPath)
	if err != nil {
		return err
	}
	var cfg acpOwnerConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return err
	}
	paths, err := ptyowner.NewSessionPaths(cfg.Root, cfg.Info.Key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(paths.Socket), 0o700); err != nil {
		return err
	}
	// The backend owns launch serialization. A stale socket can remain after an
	// owner crash; a live socket must never be replaced.
	if conn, err := (&net.Dialer{}).DialContext(ctx, "unix", paths.Socket); err == nil {
		_ = conn.Close()
		return errors.New("ACP owner already running")
	}
	if err := os.Remove(paths.Socket); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	proxy, err := newACPMCPProxy(cfg.MCP, cfg.Info.WorkspaceID)
	if err != nil {
		return err
	}
	defer proxy.Close()
	manager := NewManager(Options{ACPSessionsDir: cfg.Root, ACPPreferencesPath: cfg.Preferences, AgentMCPURL: proxy.URL(), AgentMCPToken: proxy.token})
	var saved *acpSavedSession
	data, err = os.ReadFile(manager.acpSessionPath(cfg.Info.Key))
	if err == nil {
		saved = &acpSavedSession{}
		if err := json.Unmarshal(data, saved); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	agent, err := manager.startACP(ctx, cfg.Info, cfg.Command, cfg.CWD, cfg.Strip, saved)
	if err != nil {
		return err
	}
	reported := make(chan struct{})
	go func() {
		defer close(reported)
		reportACPActivity(agentactivity.NewStore(cfg.Activity), agent, cfg.Info.Key, cfg.CWD)
	}()
	defer func() { _ = agent.Stop(context.Background()); <-reported }()
	listener, err := (&net.ListenConfig{}).Listen(ctx, "unix", paths.Socket)
	if err != nil {
		return err
	}
	defer listener.Close()
	server := rpc.NewServer()
	service := &acpOwnerRPC{agent: agent, proxy: proxy, stop: make(chan struct{}), stopped: make(chan struct{})}
	if err := server.RegisterName("ACP", service); err != nil {
		return err
	}
	var clients sync.WaitGroup
	var connections sync.Map
	accepted := make(chan struct{})
	go func() {
		defer close(accepted)
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			connections.Store(conn, struct{}{})
			clients.Go(func() { defer connections.Delete(conn); server.ServeConn(conn) })
		}
	}()
	select {
	case <-ctx.Done():
	case <-agent.Done():
	case <-service.stop:
	}
	err = agent.Stop(context.Background())
	select {
	case <-service.stop:
		if removeErr := os.Remove(agent.recordPath); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			err = errors.Join(err, removeErr)
		}
	default:
	}
	service.stopErr = err
	close(service.stopped)
	_ = listener.Close()
	<-accepted
	drained := make(chan struct{})
	go func() { clients.Wait(); close(drained) }()
	select {
	case <-drained:
	case <-time.After(2 * time.Second):
		connections.Range(func(key, _ any) bool { _ = key.(net.Conn).Close(); return true })
		<-drained
	}
	return err
}

// StopDormantACP stops an owner even before the workspace has been reopened.
// It never starts or loads an agent during workspace deletion.
func (m *Manager) StopDormantACP(ctx context.Context, workspaceID, key string) error {
	if _, err := m.ACP(workspaceID, key); err == nil {
		return nil
	}
	if m.acpSessionsDir == "" {
		return nil
	}
	paths, err := ptyowner.NewSessionPaths(m.acpSessionsDir, key)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(filepath.Join(paths.Dir, "config.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var cfg acpOwnerConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return err
	}
	if cfg.Info.WorkspaceID != workspaceID {
		return ErrSessionNotFound
	}
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", paths.Socket)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) && !errors.Is(err, syscall.ECONNREFUSED) {
			return err
		}
	} else {
		client := rpc.NewClient(conn)
		defer client.Close()
		remote := &acpAttachment{client: client}
		if err := remote.call(ctx, "ACP.Stop", struct{}{}, &struct{}{}); err != nil {
			return err
		}
	}
	if err := os.Remove(m.acpSessionPath(key)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
