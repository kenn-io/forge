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
	"strings"
	"sync"
	"time"

	"go.kenn.io/forge/internal/agentactivity"
	"go.kenn.io/forge/internal/ptyowner"
	"go.kenn.io/kit/atomicfile"
)

// The owner runs beneath tmux/ptyowner. Only this process owns the SDK pipes,
// so detaching the daemon cannot cancel a turn or a pending permission.
type acpOwnerConfig struct {
	DisableForgeTools bool
	Root              string
	Info              SessionInfo
	Command           []string
	CWD               string
	Strip             []string
	Preferences       string
	Activity          string
	MCP               ACPMCPBinding
	// Options are this chat's launch options, applied over the remembered ones.
	Options map[string]string
	// IdleTimeoutMillis is how long the owner keeps an idle, unviewed chat's
	// agent running before parking it; zero uses acpOwnerIdleTimeout.
	IdleTimeoutMillis int64
}

type acpSavedSession struct {
	SessionID string
	State     ACPState
	// LoadSession records that the agent can load this session back, so a
	// replacement owner keeps the agent's context.
	LoadSession bool
}

func (m *Manager) acpSessionPath(key string) string {
	paths, err := ptyowner.NewSessionPaths(m.acpSessionsDir, key)
	if err != nil {
		return ""
	}
	return filepath.Join(paths.Dir, "session.json")
}

// ACPReloadable reports whether a saved ACP session exists and its agent can
// load it, so stopping the owner loses neither transcript nor agent context.
func (m *Manager) ACPReloadable(key string) bool {
	saved, err := m.loadACPSaved(key)
	return err == nil && saved != nil && saved.LoadSession
}

// loadACPSaved reads a key's saved ACP session, or nil when none is saved.
func (m *Manager) loadACPSaved(key string) (*acpSavedSession, error) {
	data, err := os.ReadFile(m.acpSessionPath(key))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	saved := &acpSavedSession{}
	if err := json.Unmarshal(data, saved); err != nil {
		return nil, err
	}
	return saved, nil
}

func (a *ACP) persistLocked() error {
	if a.recordPath == "" {
		return nil
	}
	a.initializeMessagesLocked()
	data, err := json.Marshal(acpSavedSession{SessionID: a.sessionID, State: a.state, LoadSession: a.loadSession}, json.Deterministic(true))
	if err != nil {
		return err
	}
	err = atomicfile.WriteFile(a.recordPath, data, atomicfile.WithPerm(0o600))
	if errors.Is(err, atomicfile.ErrPublished) {
		return nil
	}
	return err
}

// saveTakenEffectLocked saves a change that has already taken effect, such
// as a prompt the agent received or an answer it was sent. A failed save
// cannot undo the change, so the command still succeeds and the chat reports
// the error; a refusal would tell the sender that the change never happened.
func (a *ACP) saveTakenEffectLocked(change string) {
	if err := a.persistLocked(); err != nil {
		a.setErrorLocked(fmt.Errorf("save chat after %s: %w", change, err))
	}
}

// restoreSendersLocked gives queued prompts and turn records saved before
// senders were recorded the person sender. Their real sender is unknown;
// untagged transcript messages read as a person's too, and clients accept
// no empty sender.
func (a *ACP) restoreSendersLocked() {
	person := ACPSender{Kind: ACPSenderUser}
	for i := range a.state.Queue {
		if a.state.Queue[i].Sender.Kind == "" {
			a.state.Queue[i].Sender = person
		}
	}
	for i := range a.state.Turns {
		if a.state.Turns[i].Sender.Kind == "" {
			a.state.Turns[i].Sender = person
		}
	}
}

// restoreTranscriptLocked makes the saved transcript the conversation of
// record after a reload. A reloaded conversation never starts queued work on
// its own.
func (a *ACP) restoreTranscriptLocked(saved ACPState) {
	a.state.Messages = saved.Messages
	a.messageIndex = nil
	a.messageChanges.Init()
	a.messageCursor = 0
	a.initializeMessagesLocked()
	a.state.Queue = saved.Queue
	a.restoreSubmissionsLocked(saved)
	a.restoreSendersLocked()
	a.state.QueuePaused = len(a.state.Queue) > 0
	a.state.Plan = saved.Plan
	a.state.Answered = saved.Answered
	a.state.Supervision = saved.Supervision
	// Commands the agent advertised while reloading are current; otherwise
	// keep the last set until it sends a new one.
	if a.state.Commands == nil {
		a.state.Commands = saved.Commands
	}
}

func (m *Manager) acpLaunchCommand(ctx context.Context, target LaunchTarget, workspaceID, key, cwd string, options map[string]string) (launchCommand, error) {
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
	cfg.DisableForgeTools = target.DisableForgeTools
	cfg.Options = options
	cfg.IdleTimeoutMillis = m.acpIdleTimeout.Milliseconds()
	if cfg.DisableForgeTools {
		cfg.MCP = ACPMCPBinding{}
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		return launchCommand{}, err
	}
	configPath := filepath.Join(paths.Dir, "config.json")
	if err := atomicfile.WriteFile(configPath, data, atomicfile.WithPerm(0o600)); err != nil && !errors.Is(err, atomicfile.ErrPublished) {
		return launchCommand{}, err
	}
	// A start failure recorded by an earlier owner must not fail this one.
	if err := os.Remove(filepath.Join(paths.Dir, acpStartErrorFile)); err != nil && !errors.Is(err, os.ErrNotExist) {
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

// acpStartErrorFile records why an owner failed to start. No owner listens
// after such a failure, so the daemon waiting to attach reads it instead. Its
// first line is the owner RPC code of a failure the daemon branches on, such
// as ErrACPCannotReload, or empty; the rest is the failure's message.
const acpStartErrorFile = "start-error"

// acpAttachTimeout bounds how long the daemon waits for a new owner's socket.
const acpAttachTimeout = 35*time.Second + codexACPInitialModeTimeout

func recordACPStartError(dir string, err error) error {
	record := ACPErrorCode(err) + "\n" + err.Error()
	if err := atomicfile.WriteFile(filepath.Join(dir, acpStartErrorFile), []byte(record), atomicfile.WithPerm(0o600)); err != nil && !errors.Is(err, atomicfile.ErrPublished) {
		return err
	}
	return nil
}

// acpStartError restores a recorded start failure: the owner's message,
// wrapping the sentinel the daemon branches on, if any. It is nil for an
// empty record.
func acpStartError(record string) error {
	code, message, _ := strings.Cut(record, "\n")
	message = strings.TrimSpace(message)
	sentinel := acpCodeError(code)
	switch {
	case message == "":
		return sentinel
	case sentinel == nil:
		return errors.New(message)
	default:
		return &acpRecordedStartError{message: message, err: sentinel}
	}
}

// acpRecordedStartError keeps an owner's start failure message while
// matching its sentinel.
type acpRecordedStartError struct {
	message string
	err     error
}

func (e *acpRecordedStartError) Error() string { return e.message }
func (e *acpRecordedStartError) Unwrap() error { return e.err }

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
		attached, attachErr := m.acpAttachment(ctx, info, conn)
		mismatch, incompatible := errors.AsType[*acpOwnerMismatchError](attachErr)
		if !incompatible {
			return attached, attachErr
		}
		if err := m.replaceACPOwner(ctx, info, mismatch); err != nil {
			return nil, fmt.Errorf("resume chat %q after %w: %w", info.Key, mismatch, err)
		}
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
	launch, err := m.acpLaunchCommand(ctx, LaunchTarget{Key: info.TargetKey, Kind: LaunchTargetACP, Command: cfg.Command, DisableForgeTools: cfg.DisableForgeTools}, info.WorkspaceID, info.Key, cwd, cfg.Options)
	if err != nil {
		return nil, err
	}
	info.TmuxSession = launch.TmuxSession
	return m.startACPOwner(ctx, info, launch.Command, cwd, m.currentStripEnvVars())
}

// RestoreSupervisedACP restores a supervised chat from the daemon's stored
// record, even after this daemon saw its agent exit on its own; ordinary
// restoration skips such sessions. Like workspace reopen it attaches a running
// owner and relaunches only once the owner's backend is gone, loading the saved
// native session. It refuses chats no coordinator holds, including taken-over
// chats, running or not (ErrSessionUnavailable), and agents that cannot reload
// the session (ErrACPCannotReload); the saved session stays.
func (m *Manager) RestoreSupervisedACP(ctx context.Context, restored RestoredRuntimeSession) error {
	restored.WorkspaceID = strings.TrimSpace(restored.WorkspaceID)
	restored.SessionKey = strings.TrimSpace(restored.SessionKey)
	restored.TargetKey = strings.TrimSpace(restored.TargetKey)
	restored.TmuxSession = strings.TrimSpace(restored.TmuxSession)
	restored.Kind = LaunchTargetACP
	if m.acpSessionsDir == "" || restored.WorkspaceID == "" || restored.SessionKey == "" {
		return ErrSessionNotFound
	}
	if restored.TargetKey == "" {
		return errors.New("restore supervised ACP: target key is required")
	}
	key := restored.SessionKey
	if err := m.requireACPWorkspace(restored.WorkspaceID, key); err != nil {
		return err
	}
	startMu := m.startLock(key)
	startMu.Lock()
	defer startMu.Unlock()
	if err := m.ensureOpen(); err != nil {
		return err
	}
	saved, err := m.readSavedACP(key)
	if err != nil {
		return fmt.Errorf("%w: saved ACP session: %w", ErrSessionUnavailable, err)
	}
	if !saved.State.Supervision.held() {
		return fmt.Errorf("%w: %q is not supervised", ErrSessionUnavailable, key)
	}
	if m.runningSession(m.sessions, key) != nil {
		return nil
	}
	return m.startRestoredSession(ctx, restored)
}

// SupervisedACP reports whether a coordinator holds the saved chat key,
// running or not. A chat with no saved session, or one people took over, is
// not supervised. It returns ErrSessionNotFound when workspaceID has no such
// chat.
func (m *Manager) SupervisedACP(workspaceID, key string) (bool, error) {
	if m.acpSessionsDir == "" || workspaceID == "" || key == "" {
		return false, ErrSessionNotFound
	}
	if err := m.requireACPWorkspace(workspaceID, key); err != nil {
		return false, err
	}
	saved, err := m.readSavedACP(key)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return saved.State.Supervision.held(), nil
}

// requireACPWorkspace returns ErrSessionNotFound unless key names a chat
// owner configured for workspaceID.
func (m *Manager) requireACPWorkspace(workspaceID, key string) error {
	paths, err := ptyowner.NewSessionPaths(m.acpSessionsDir, key)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(filepath.Join(paths.Dir, "config.json"))
	if errors.Is(err, os.ErrNotExist) {
		return ErrSessionNotFound
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
	return nil
}

func (m *Manager) readSavedACP(key string) (acpSavedSession, error) {
	var saved acpSavedSession
	data, err := os.ReadFile(m.acpSessionPath(key))
	if err != nil {
		return saved, err
	}
	err = json.Unmarshal(data, &saved)
	return saved, err
}

func (m *Manager) attachACPOwner(ctx context.Context, info SessionInfo) (*session, error) {
	paths, err := ptyowner.NewSessionPaths(m.acpSessionsDir, info.Key)
	if err != nil {
		return nil, err
	}
	parent := ctx
	ctx, cancel := context.WithTimeout(ctx, acpAttachTimeout)
	defer cancel()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		conn, err := (&net.Dialer{}).DialContext(ctx, "unix", paths.Socket)
		if err == nil {
			return m.acpAttachment(ctx, info, conn)
		}
		if record, err := os.ReadFile(filepath.Join(paths.Dir, acpStartErrorFile)); err == nil {
			if startErr := acpStartError(string(record)); startErr != nil {
				return nil, fmt.Errorf("start ACP owner: %w", startErr)
			}
		}
		select {
		case <-ctx.Done():
			if parent.Err() == nil && errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return nil, fmt.Errorf("attach ACP owner: the agent did not finish starting within %s: %w", acpAttachTimeout, ctx.Err())
			}
			return nil, fmt.Errorf("attach ACP owner: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}

func (m *Manager) acpAttachment(ctx context.Context, info SessionInfo, conn net.Conn) (*session, error) {
	client := rpc.NewClient(conn)
	remote := &acpAttachment{client: client, done: make(chan struct{}), subscribers: make(map[chan struct{}]struct{}), workspaceID: info.WorkspaceID, key: info.Key}
	var snapshot ACPUpdate
	if err := remote.call(ctx, "ACP.Snapshot", struct{}{}, &snapshot); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("read ACP owner protocol: %w", err)
	}
	if snapshot.Protocol != ACPOwnerProtocol {
		_ = client.Close()
		return nil, &acpOwnerMismatchError{protocol: snapshot.Protocol, state: snapshot.State}
	}
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
func RunACPOwner(ctx context.Context, configPath string) (err error) {
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
	// No owner listens after a failed start, so the daemon waiting to attach
	// reads why instead of waiting out its deadline.
	listening := false
	defer func() {
		if err != nil && !listening {
			err = errors.Join(err, recordACPStartError(paths.Dir, err))
		}
	}()
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
	proxy, err := newACPMCPProxy(cfg.MCP, cfg.Info.WorkspaceID, cfg.DisableForgeTools)
	if err != nil {
		return err
	}
	defer proxy.Close()
	manager := NewManager(Options{ACPSessionsDir: cfg.Root, ACPPreferencesPath: cfg.Preferences, AgentMCPURL: proxy.URL(), AgentMCPToken: proxy.token})
	saved, err := manager.loadACPSaved(cfg.Info.Key)
	if err != nil {
		return err
	}
	agent, err := manager.startACP(ctx, cfg.Info, cfg.Command, cfg.CWD, cfg.Strip, saved, cfg.Options)
	if err != nil {
		return err
	}
	if err := os.Remove(filepath.Join(paths.Dir, acpStartErrorFile)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return errors.Join(err, agent.Stop(context.Background()))
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
	listening = true
	defer listener.Close()
	server := rpc.NewServer()
	service := &acpOwnerRPC{agent: agent, proxy: proxy, stop: make(chan struct{}), stopped: make(chan struct{})}
	if err := server.RegisterName("ACP", service); err != nil {
		return err
	}
	idleTimeout := time.Duration(cfg.IdleTimeoutMillis) * time.Millisecond
	if idleTimeout <= 0 {
		idleTimeout = acpOwnerIdleTimeout
	}
	go reapACPOwner(ctx, agent, service, paths.Dir, paths.Socket, idleTimeout)
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
	naturalExit := false
	select {
	case <-ctx.Done():
	case <-agent.Done():
		naturalExit = true
	case <-service.stop:
	}
	err = agent.Stop(context.Background())
	// Close the listener, which removes the socket, before acknowledging a
	// stop, so a caller that sees the reply finds no socket left to dial.
	_ = listener.Close()
	<-accepted
	drained := make(chan struct{})
	go func() { clients.Wait(); close(drained) }()
	if naturalExit {
		// Existing viewers drain final updates over their open connections.
		// An explicit stop still removes saved state before acknowledging it.
		select {
		case <-drained:
			return err
		case <-ctx.Done():
		case <-service.stop:
		}
	}
	select {
	case <-service.stop:
		if service.park {
			break
		}
		if removeErr := os.Remove(agent.recordPath); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			err = errors.Join(err, removeErr)
		}
	default:
	}
	service.stopErr = err
	close(service.stopped)

	select {
	case <-drained:
	case <-time.After(2 * time.Second):
		connections.Range(func(key, _ any) bool { _ = key.(net.Conn).Close(); return true })
		<-drained
	}
	return err
}

// callACPOwner calls an owner RPC; an owner that is not running is not an error.
func (m *Manager) callACPOwner(ctx context.Context, key, method string) error {
	if m.acpSessionsDir == "" {
		return nil
	}
	paths, err := ptyowner.NewSessionPaths(m.acpSessionsDir, key)
	if err != nil {
		return err
	}
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", paths.Socket)
	if err != nil {
		// A missing or refusing socket means the owner is dead.
		if ptyowner.IsAbsentOwner(err) {
			return nil
		}
		return err
	}
	client := rpc.NewClient(conn)
	defer client.Close()
	return (&acpAttachment{client: client}).call(ctx, method, struct{}{}, &struct{}{})
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
	if err := m.callACPOwner(ctx, key, "ACP.Stop"); err != nil {
		return err
	}
	if err := os.Remove(m.acpSessionPath(key)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	m.mu.Lock()
	delete(m.parked, key)
	m.mu.Unlock()
	return nil
}
