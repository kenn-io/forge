package server

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"go.kenn.io/middleman/internal/db"
	"go.kenn.io/middleman/internal/workspace/localruntime"
)

// hostRuntimeScope is the runtime manager scope for sessions launched at
// host level, i.e. not tied to a registered project worktree. Workspace IDs
// are prefixed ("ws_") and worktree scopes use "project-worktree:", so the
// bare name cannot collide.
const hostRuntimeScope = "host"

// expandHomeCWD resolves a "~" or "~/"-prefixed working directory against
// this daemon's own home. Session cwds cross the fleet as home-relative
// paths because only the executing host knows its home directory; any other
// path is returned unchanged.
func expandHomeCWD(cwd string) string {
	if cwd != "~" && !strings.HasPrefix(cwd, "~/") {
		return cwd
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return cwd
	}
	if cwd == "~" {
		return home
	}
	return filepath.Join(home, cwd[2:])
}

type launchHostRuntimeSessionInput struct {
	Body struct {
		// Command is the argv to run in a tmux-backed session. Required.
		Command []string `json:"command"`
		// CWD is the pane working directory. Required: there is no
		// worktree path to fall back to at host scope.
		CWD string `json:"cwd"`
		// SessionKey optionally names the session with a caller-owned
		// durable key with ensure semantics (re-launching with the
		// same key returns the existing live session).
		SessionKey string `json:"session_key,omitempty"`
		// Env names extra environment variables for the pane. Keys
		// must be shell identifiers.
		Env map[string]string `json:"env,omitempty"`
		// Label is the display label for the session.
		Label string `json:"label,omitempty"`
	}
}

type hostRuntimeSession struct {
	Key         string                        `json:"key"`
	Label       string                        `json:"label"`
	Kind        localruntime.LaunchTargetKind `json:"kind"`
	Status      localruntime.SessionStatus    `json:"status"`
	TmuxSession string                        `json:"tmux_session,omitempty"`
	CreatedAt   time.Time                     `json:"created_at"`
	ExitedAt    *time.Time                    `json:"exited_at,omitempty"`
	ExitCode    *int                          `json:"exit_code,omitempty"`
}

type hostRuntimeSessionOutput struct {
	Body hostRuntimeSession
}

type listHostRuntimeSessionsOutput struct {
	Body struct {
		Sessions []hostRuntimeSession `json:"sessions"`
	}
}

type hostRuntimeSessionKeyInput struct {
	SessionKey string `path:"session_key"`
}

func (s *Server) launchHostRuntimeSession(
	ctx context.Context,
	input *launchHostRuntimeSessionInput,
) (*hostRuntimeSessionOutput, error) {
	if s.runtime == nil {
		return nil, problemServiceUnavailable("host runtime not configured")
	}
	if len(input.Body.Command) == 0 {
		return nil, problemValidation("body.command", "command is required")
	}
	if strings.TrimSpace(input.Body.Command[0]) == "" {
		return nil, problemValidation(
			"body.command", "command executable must not be empty",
		)
	}
	cwd := expandHomeCWD(strings.TrimSpace(input.Body.CWD))
	if cwd == "" {
		return nil, problemValidation("body.cwd", "cwd is required")
	}
	for key := range input.Body.Env {
		if !localruntime.IsShellIdentifier(key) {
			return nil, problemValidation(
				"body.env",
				"env var "+strconv.Quote(key)+" is not a valid shell identifier",
			)
		}
	}

	sessionKey := strings.TrimSpace(input.Body.SessionKey)
	session, err := s.runtime.EnsureCommandSession(
		ctx, hostRuntimeScope, localruntime.CommandLaunchSpec{
			SessionKey: sessionKey,
			Command:    input.Body.Command,
			Env:        input.Body.Env,
			Label:      input.Body.Label,
			CWD:        cwd,
		},
	)
	if err != nil {
		return nil, projectWorktreeRuntimeLaunchError(err)
	}
	// Always upsert with the returned session's generation: ensure semantics
	// can return a live reused session or a brand-new one, and the stored
	// row must carry the live created_at so a stale generation's async exit
	// cleanup cannot delete it.
	if session.TmuxSession != "" {
		if err := s.db.UpsertHostRuntimeTmuxSession(
			ctx, &db.HostRuntimeTmuxSession{
				SessionKey:  session.Key,
				SessionName: session.TmuxSession,
				Label:       session.Label,
				CWD:         cwd,
				CreatedAt:   session.CreatedAt,
			},
		); err != nil {
			// Only roll back a session this request launched: stopping
			// a reused live session would kill someone else's work
			// over a bookkeeping failure.
			if !session.Reused {
				_ = s.runtime.Stop(ctx, hostRuntimeScope, session.Key)
			}
			return nil, problemInternal(
				"record host runtime tmux session: " + err.Error(),
			)
		}
	}
	return &hostRuntimeSessionOutput{
		Body: hostRuntimeSessionFromRuntime(session),
	}, nil
}

func (s *Server) listHostRuntimeSessions(
	ctx context.Context,
	_ *struct{},
) (*listHostRuntimeSessionsOutput, error) {
	if s.runtime == nil {
		return nil, problemServiceUnavailable("host runtime not configured")
	}
	runtimeSessions := s.runtime.ListSessions(hostRuntimeScope)
	runtimeByKey := make(
		map[string]localruntime.SessionInfo, len(runtimeSessions),
	)
	for _, session := range runtimeSessions {
		runtimeByKey[session.Key] = session
	}
	stored, err := s.db.ListHostRuntimeTmuxSessions(ctx)
	if err != nil {
		return nil, problemInternal(
			"list host runtime tmux sessions: " + err.Error(),
		)
	}
	out := &listHostRuntimeSessionsOutput{}
	out.Body.Sessions = make(
		[]hostRuntimeSession, 0, len(stored)+len(runtimeSessions),
	)
	seen := make(map[string]struct{}, len(stored)+len(runtimeSessions))
	for _, row := range stored {
		seen[row.SessionKey] = struct{}{}
		if runtimeSession, ok := runtimeByKey[row.SessionKey]; ok {
			out.Body.Sessions = append(
				out.Body.Sessions,
				hostRuntimeSessionFromRuntime(runtimeSession),
			)
			continue
		}
		out.Body.Sessions = append(
			out.Body.Sessions, hostRuntimeSessionFromStored(row),
		)
	}
	for _, runtimeSession := range runtimeSessions {
		if _, ok := seen[runtimeSession.Key]; ok {
			continue
		}
		out.Body.Sessions = append(
			out.Body.Sessions,
			hostRuntimeSessionFromRuntime(runtimeSession),
		)
	}
	return out, nil
}

func (s *Server) stopHostRuntimeSession(
	ctx context.Context,
	input *hostRuntimeSessionKeyInput,
) (*struct{}, error) {
	if s.runtime == nil {
		return nil, problemServiceUnavailable("host runtime not configured")
	}
	if err := s.runtime.Stop(
		ctx, hostRuntimeScope, input.SessionKey,
	); err != nil {
		if errors.Is(err, localruntime.ErrSessionNotFound) {
			stopped, stopErr := s.stopStoredHostRuntimeTmuxSession(
				ctx, input.SessionKey,
			)
			if stopErr != nil {
				return nil, problemInternal(
					"stop stored host runtime session: " + stopErr.Error(),
				)
			}
			if stopped {
				return nil, nil
			}
			return nil, problemNotFound(CodeNotFound, err.Error(), nil)
		}
		return nil, problemInternal(
			"stop host runtime session: " + err.Error(),
		)
	}
	if err := s.db.DeleteHostRuntimeTmuxSession(
		ctx, input.SessionKey,
	); err != nil {
		return nil, problemInternal(
			"forget host runtime tmux session: " + err.Error(),
		)
	}
	return nil, nil
}

func (s *Server) stopStoredHostRuntimeTmuxSession(
	ctx context.Context,
	sessionKey string,
) (bool, error) {
	rows, err := s.db.ListHostRuntimeTmuxSessions(ctx)
	if err != nil {
		return false, err
	}
	for _, row := range rows {
		if row.SessionKey != sessionKey {
			continue
		}
		if err := killProjectRuntimeTmuxSession(
			ctx, s.cfg.TmuxCommand(), row.SessionName,
		); err != nil {
			return true, err
		}
		if err := s.db.DeleteHostRuntimeTmuxSession(
			ctx, sessionKey,
		); err != nil {
			return true, err
		}
		return true, nil
	}
	return false, nil
}

func (s *Server) getHostRuntimeSessionAttachSpec(
	ctx context.Context,
	input *hostRuntimeSessionKeyInput,
) (*runtimeAttachSpecOutput, error) {
	rows, err := s.db.ListHostRuntimeTmuxSessions(ctx)
	if err != nil {
		return nil, problemInternal(
			"list host runtime tmux sessions: " + err.Error(),
		)
	}
	for _, row := range rows {
		if row.SessionKey != input.SessionKey {
			continue
		}
		spec, err := runtimeAttachSpec(
			ctx, s.cfg.TmuxCommand(), input.SessionKey, "",
			row.SessionName,
		)
		if err != nil {
			return nil, err
		}
		return &runtimeAttachSpecOutput{Body: spec}, nil
	}
	return nil, problemNotFound(CodeNotFound, "runtime session not found", nil)
}

func hostRuntimeSessionFromRuntime(
	session localruntime.SessionInfo,
) hostRuntimeSession {
	return hostRuntimeSession{
		Key:         session.Key,
		Label:       session.Label,
		Kind:        session.Kind,
		Status:      session.Status,
		TmuxSession: session.TmuxSession,
		CreatedAt:   session.CreatedAt,
		ExitedAt:    session.ExitedAt,
		ExitCode:    session.ExitCode,
	}
}

func hostRuntimeSessionFromStored(
	row db.HostRuntimeTmuxSession,
) hostRuntimeSession {
	label := row.Label
	if label == "" {
		label = row.SessionKey
	}
	return hostRuntimeSession{
		Key:         row.SessionKey,
		Label:       label,
		Kind:        localruntime.LaunchTargetCommand,
		Status:      localruntime.SessionStatusRunning,
		TmuxSession: row.SessionName,
		CreatedAt:   row.CreatedAt,
	}
}
