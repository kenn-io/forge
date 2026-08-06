package localruntime

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"strings"
	"time"
)

// ErrSessionKeyConflict reports a caller-supplied session key that is
// already owned by a different scope or a different session kind, so ensure
// semantics cannot return it as the requested command session.
var ErrSessionKeyConflict = errors.New("session key conflict")

// CommandLaunchSpec describes a tmux-backed session launched from a
// caller-supplied command line instead of a configured launch target.
type CommandLaunchSpec struct {
	// SessionKey optionally names the session with a caller-owned durable
	// key. When a live command session with the same key already exists in
	// the same scope, EnsureCommandSessionAndPersist returns it instead of
	// launching again; a key owned by a different session kind (agent, shell)
	// is a conflict. Empty means a random session key is generated.
	SessionKey string
	// Command is the argv to run inside the tmux pane. Required.
	Command []string
	// Env names extra environment variables for the pane. These always
	// reach the command, even when the sanitizing allowlist would drop
	// them. Keys must be shell identifiers.
	Env map[string]string
	// Label is the display label; defaults to Command[0].
	Label string
	// CWD is the working directory for the pane.
	CWD string
}

// CommandSessionPersistenceError reports that a command session generation
// could not be recorded durably. PersistenceErr remains the caller-visible
// cause; RollbackErr is diagnostic only.
type CommandSessionPersistenceError struct {
	PersistenceErr error
	RollbackErr    error
}

func (e *CommandSessionPersistenceError) Error() string {
	if e == nil || e.PersistenceErr == nil {
		return "command session persistence failed"
	}
	return e.PersistenceErr.Error()
}

func (e *CommandSessionPersistenceError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.PersistenceErr
}

// EnsureCommandSessionAndPersist launches spec.Command in a tmux session owned
// by this manager, or returns the existing live session when spec.SessionKey
// names one in the same scope. Same-key ensure, persistence, and compensation
// share one manager-owned transaction, so no request can reuse a generation
// while another request still owns rollback authority over it.
func (m *Manager) EnsureCommandSessionAndPersist(
	ctx context.Context,
	workspaceID string,
	spec CommandLaunchSpec,
	persist func(context.Context, SessionInfo) error,
) (SessionInfo, error) {
	if err := ctx.Err(); err != nil {
		return SessionInfo{}, err
	}
	if strings.TrimSpace(workspaceID) == "" {
		return SessionInfo{}, errors.New("session scope is required")
	}
	if len(spec.Command) == 0 || strings.TrimSpace(spec.Command[0]) == "" {
		return SessionInfo{}, errors.New("command is required")
	}
	for key := range spec.Env {
		if !isShellIdentifier(key) {
			return SessionInfo{}, fmt.Errorf(
				"env var %q is not a valid shell identifier", key,
			)
		}
	}

	key := strings.TrimSpace(spec.SessionKey)
	if key != "" {
		startMu := m.startLock(key)
		startMu.Lock()
		defer startMu.Unlock()
		return m.ensureCommandSessionAndPersistLocked(
			ctx, workspaceID, spec, key, persist,
		)
	}

	session, err := m.ensureCommandSessionLocked(ctx, workspaceID, spec, "")
	if err != nil {
		return SessionInfo{}, err
	}
	startMu := m.startLock(session.Key)
	startMu.Lock()
	defer startMu.Unlock()
	return m.persistCommandSessionLocked(ctx, session, persist)
}

func (m *Manager) ensureCommandSessionAndPersistLocked(
	ctx context.Context,
	workspaceID string,
	spec CommandLaunchSpec,
	key string,
	persist func(context.Context, SessionInfo) error,
) (SessionInfo, error) {
	session, err := m.ensureCommandSessionLocked(ctx, workspaceID, spec, key)
	if err != nil {
		return SessionInfo{}, err
	}
	return m.persistCommandSessionLocked(ctx, session, persist)
}

func (m *Manager) persistCommandSessionLocked(
	ctx context.Context,
	session SessionInfo,
	persist func(context.Context, SessionInfo) error,
) (SessionInfo, error) {
	persistenceErr := persist(ctx, session)
	if persistenceErr == nil {
		return session, nil
	}

	var rollbackErr error
	if !session.Reused {
		cleanupCtx, cancel := context.WithTimeout(
			context.WithoutCancel(ctx), 5*time.Second,
		)
		rollbackErr = m.rollbackLaunchLocked(cleanupCtx, session)
		cancel()
	}
	return session, &CommandSessionPersistenceError{
		PersistenceErr: persistenceErr,
		RollbackErr:    rollbackErr,
	}
}

// ensureCommandSessionLocked assumes the caller owns startLock(key) when key is
// non-empty. Empty keys are generated here and locked by the caller before
// persistence or compensation.
func (m *Manager) ensureCommandSessionLocked(
	ctx context.Context,
	workspaceID string,
	spec CommandLaunchSpec,
	key string,
) (SessionInfo, error) {
	if key != "" {
		if existing := m.runningSession(m.sessions, key); existing != nil {
			info := existing.snapshot()
			if info.WorkspaceID != workspaceID {
				return SessionInfo{}, fmt.Errorf(
					"%w: %q is owned by another scope",
					ErrSessionKeyConflict, key,
				)
			}
			if info.Kind != LaunchTargetCommand {
				return SessionInfo{}, fmt.Errorf(
					"%w: %q is owned by a %s session",
					ErrSessionKeyConflict, key, info.Kind,
				)
			}
			slog.Debug(
				"command session ensure reused live session",
				"workspace_id", workspaceID,
				"session_key", key,
			)
			info.Reused = true
			return info, nil
		}
	}

	if err := m.ensureOpen(); err != nil {
		return SessionInfo{}, err
	}
	if err := m.beginStart(); err != nil {
		return SessionInfo{}, err
	}
	defer m.finishStart()

	if err := m.claimInflight(workspaceID); err != nil {
		return SessionInfo{}, err
	}
	defer m.releaseInflight(workspaceID)

	if key == "" {
		generated, err := m.newSessionKey(workspaceID)
		if err != nil {
			return SessionInfo{}, err
		}
		key = generated
	}
	label, releaseLabel := m.reserveSessionLabel(
		workspaceID,
		fallbackSessionLabel(spec.Label, spec.Command[0]),
	)
	defer releaseLabel()

	stripEnvVars := m.currentStripEnvVars()
	launch, err := m.commandSessionLaunch(ctx, workspaceID, key, spec, stripEnvVars)
	if err != nil {
		return SessionInfo{}, err
	}

	started, err := m.startOwnedSession(ctx, SessionInfo{
		Key:          key,
		WorkspaceID:  workspaceID,
		Label:        label,
		Kind:         LaunchTargetCommand,
		Status:       SessionStatusStarting,
		CreatedAt:    time.Now().UTC(),
		TmuxSession:  launch.TmuxSession,
		tmuxLaunchID: launch.TmuxLaunchID,
		Reused:       !launch.TmuxCreated,
	}, launch.Command, spec.CWD, stripEnvVars)
	if err != nil {
		if launch.TmuxCreated {
			cleanupCtx, cancel := context.WithTimeout(
				context.WithoutCancel(ctx), 5*time.Second,
			)
			_ = m.killTmuxSession(cleanupCtx, launch.TmuxSession)
			cancel()
		}
		return SessionInfo{}, err
	}
	started.tmuxSession = launch.TmuxSession

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		go m.watchSession(started)
		_ = m.stopSession(ctx, started)
		waitSessionDone(started)
		return SessionInfo{}, errManagerShutdown
	}
	m.sessions[key] = started
	m.mu.Unlock()
	go m.watchSession(started)
	slog.Debug(
		"command session launched",
		"workspace_id", workspaceID,
		"session_key", key,
		"tmux_session", launch.TmuxSession,
	)
	return started.snapshot(), nil
}

func (m *Manager) commandSessionLaunch(
	ctx context.Context,
	workspaceID string,
	key string,
	spec CommandLaunchSpec,
	stripEnvVars []string,
) (launchCommand, error) {
	tmuxCommand := slices.Clone(m.tmuxCommand)
	if len(tmuxCommand) == 0 {
		tmuxCommand = []string{"tmux"}
	}
	tmuxCommand, err := resolveTmuxCommand(tmuxCommand)
	if err != nil {
		return launchCommand{}, err
	}
	command := slices.Clone(spec.Command)
	resolvedPath, err := resolveExecutable(command[0])
	if err != nil {
		return launchCommand{}, err
	}
	command[0] = resolvedPath

	tmuxSession := tmuxSessionName(workspaceID, key)
	launchID, err := newTmuxLaunchID()
	if err != nil {
		return launchCommand{}, err
	}
	paneEnv := tmuxAgentEnvPolicy.paneEnvironmentWithExtra(
		os.Environ(), command, stripEnvVars, spec.Env,
	)
	prepared, err := tmuxLauncher{
		TmuxCommand: tmuxCommand,
		Session:     tmuxSession,
		CWD:         spec.CWD,
		Pane:        paneEnv,
		OwnerMarker: m.tmuxOwnerMarker,
		LaunchID:    launchID,
		HideStatus:  m.currentHideTmuxStatus(),
	}.prepare(ctx)
	if err != nil {
		return launchCommand{}, err
	}
	result := launchCommand{
		Command:     prepared.AttachCommand,
		TmuxSession: tmuxSession,
		TmuxCreated: prepared.Created,
	}
	if prepared.Created {
		result.TmuxLaunchID = launchID
	}
	return result, nil
}
