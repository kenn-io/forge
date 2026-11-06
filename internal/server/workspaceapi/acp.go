package workspaceapi

import (
	"cmp"
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"

	"github.com/coder/websocket"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/terminalwebsocket"
	"go.kenn.io/forge/internal/tracing"
	"go.kenn.io/forge/internal/workspace/localruntime"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// acpCommandError reports a failed command to the client that sent it, naming
// the command and its ID so the client settles only that request.
type acpCommandError struct {
	Message string `json:"commandError"`
	Command string `json:"command"`
	ID      string `json:"id"`
}

// acpAccepted acknowledges a command to the client that sent it.
type acpAccepted struct {
	Command string `json:"command"`
	ID      string `json:"id"`
}

// workspaceChat is a chat clients read and command: a running chat, or a
// dormant one whose agent starts for the first command that needs it.
type workspaceChat interface {
	Snapshot() ([]byte, error)
	History(beforeID string, limit int) ([]byte, error)
	Updates(after uint64) (localruntime.ACPBatch, error)
	Page(after, limit int) ([]byte, error)
	Subscribe() (<-chan struct{}, func())
	Command(localruntime.ACPCommand) error
}

// serveACP serves a chat over a websocket. A dormant chat's state comes from
// its saved session until its agent starts; the socket then sends the running
// chat's state in full and follows it.
func serveACP(w http.ResponseWriter, r *http.Request, agent workspaceChat) {
	dormant, _ := agent.(*localruntime.DormantACP)
	// The span covers the wait for the chat's first state, which a browser
	// needs before it can show the chat, and never the stream after it.
	_, span := workspaceACPTracer.Start(r.Context(), "workspace.acp.first_state", trace.WithAttributes(
		attribute.String("workspace.id", r.PathValue("id")),
		attribute.String("session.key", r.PathValue("session_key")),
		attribute.Bool("acp.dormant", dormant != nil),
	))
	var endOnce sync.Once
	endFirstState := func(outcome string, err error) {
		endOnce.Do(func() {
			span.SetAttributes(attribute.String("outcome", outcome))
			tracing.EndSpan(span, err)
		})
	}
	// A socket that closes before the first state still ends the span.
	defer endFirstState("closed", nil)
	conn, err := terminalwebsocket.Accept(w, r)
	if err != nil {
		endFirstState("accept_failed", err)
		return
	}
	defer conn.Close(websocket.StatusNormalClosure, "chat detached")
	conn.SetReadLimit(terminalwebsocket.ACPReadLimit)
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	// A Snapshot that never returns holds this handler, so the span also ends
	// when the connection does, not only when the handler returns.
	stopEndingWithConnection := context.AfterFunc(ctx, func() { endFirstState("closed", nil) })
	defer stopEndingWithConnection()
	feed := agent
	changes, unsubscribe := feed.Subscribe()
	defer func() { unsubscribe() }()
	var started <-chan struct{}
	if dormant != nil {
		started = dormant.Started()
	}
	run := func(command localruntime.ACPCommand) bool {
		err := agent.Command(command)
		var data []byte
		var marshalErr error
		switch {
		case err != nil:
			// Command failures belong to this caller, not every attached browser.
			data, marshalErr = json.Marshal(acpCommandError{Message: err.Error(), Command: command.Type, ID: command.ID})
		case command.Type == "prompt":
			// The sender learns its prompt was taken even when a retry changed
			// nothing, or the message has left the window it can see.
			data, marshalErr = json.Marshal(map[string]acpAccepted{"accepted": {Command: command.Type, ID: command.ID}})
		default:
			return true
		}
		return marshalErr == nil && conn.Write(ctx, websocket.MessageText, data) == nil
	}
	// Prompts, settings, and supervision claims can wait on the agent; they
	// run in order on their own goroutine so a stalled one never stops this
	// connection from reading a stop or an answer (Conn allows concurrent
	// writes). The backlog is unbounded: the reader never waits on it.
	var (
		slowMu  sync.Mutex
		backlog []localruntime.ACPCommand
	)
	slowReady := make(chan struct{}, 1)
	enqueue := func(command localruntime.ACPCommand) {
		slowMu.Lock()
		backlog = append(backlog, command)
		slowMu.Unlock()
		select {
		case slowReady <- struct{}{}:
		default:
		}
	}
	var readers sync.WaitGroup
	readers.Go(func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-slowReady:
			}
			for {
				slowMu.Lock()
				if len(backlog) == 0 {
					slowMu.Unlock()
					break
				}
				command := backlog[0]
				backlog = backlog[1:]
				slowMu.Unlock()
				if !run(command) {
					cancel()
					return
				}
			}
		}
	})
	readers.Go(func() {
		defer cancel()
		for {
			_, data, err := conn.Read(ctx)
			if err != nil {
				return
			}
			var command localruntime.ACPCommand
			if err := json.Unmarshal(data, &command); err != nil {
				return
			}
			switch command.Type {
			case "heartbeat":
				if conn.Write(ctx, websocket.MessageText, []byte(`{"type":"heartbeat"}`)) != nil {
					return
				}
			case "history":
				// Older transcript pages go only to the client that asked.
				data, err := agent.History(command.BeforeID, command.Limit)
				if err != nil {
					data, _ = json.Marshal(acpCommandError{Message: err.Error(), Command: command.Type})
				}
				if conn.Write(ctx, websocket.MessageText, acpMediaPreviews(data)) != nil {
					return
				}
			case "prompt", "config", "supervise":
				enqueue(command)
			default:
				// Until a dormant chat's agent runs, any command may wait
				// for it to start, like a prompt.
				if dormant != nil && dormant.Live() == nil {
					enqueue(command)
				} else if !run(command) {
					return
				}
			}
		}
	})
	defer readers.Wait()
	defer cancel()
	initialized := false
	var cursor uint64
	for {
		select {
		case <-ctx.Done():
			return
		case <-started:
			started = nil
			unsubscribe()
			feed = dormant.Live()
			changes, unsubscribe = feed.Subscribe()
			initialized = false
		case <-changes:
			if !initialized {
				data, err := feed.Snapshot()
				if err != nil {
					endFirstState("snapshot_failed", err)
					return
				}
				var state struct {
					MessageCursor uint64 `json:"messageCursor"`
				}
				if json.Unmarshal(data, &state) != nil || conn.Write(ctx, websocket.MessageText, acpMediaPreviews(data)) != nil {
					return
				}
				cursor = state.MessageCursor
				initialized = true
				endFirstState("sent", nil)
				continue
			}
			for {
				batch, err := feed.Updates(cursor)
				if err != nil {
					return
				}
				if conn.Write(ctx, websocket.MessageText, acpMediaPreviews(batch.Data)) != nil {
					return
				}
				cursor = batch.MessageCursor
				if !batch.More {
					break
				}
			}
		}
	}
}

var workspaceACPTracer = otel.Tracer("go.kenn.io/forge/internal/server/workspaceapi/acp")

// errWorkspaceACPNotAdmitted reports that setup or deletion owns the workspace.
var errWorkspaceACPNotAdmitted = errors.New("workspace setup or deletion is in progress")

// attachWorkspaceACP attaches the workspace's stored chats whose owners run a
// compatible protocol, which costs a socket dial each. Reads never start,
// replace, or reload an agent (maintainer decision): any other chat stays
// dormant until a command needs its agent. It never waits either: while a
// start, stop, or restore holds the workspace, or setup or deletion owns it,
// attaching is left to a later read.
func (s *Handler) attachWorkspaceACP(ctx context.Context, workspaceID, cwd string) {
	ctx, span := workspaceACPTracer.Start(ctx, "workspace.acp.attach",
		trace.WithAttributes(attribute.String("workspace.id", workspaceID)))
	defer span.End()
	unlock, ok := s.workspaceRuntimeLocks.TryLock(workspaceID)
	if !ok {
		span.AddEvent("workspace busy")
		return
	}
	defer unlock()
	done, admitted := s.beginWorkspaceSetup(workspaceID)
	if !admitted {
		return
	}
	defer s.finishWorkspaceSetup(workspaceID, done)
	stored, err := s.workspaces.RuntimeSessionsForWorkspace(ctx, workspaceID)
	if err != nil {
		slog.Warn("list ACP sessions to attach", "workspace_id", workspaceID, "err", err)
		return
	}
	idle := s.idle.Load()
	for _, item := range stored {
		if item.Kind != string(localruntime.LaunchTargetACP) || s.runtime.Exited(item.SessionKey) || idle.Stopped(ctx, item) {
			continue
		}
		restored := restoredRuntime(item, cwd)
		restored.AttachOnly = true
		err := s.runtime.RestoreRuntimeSessions(ctx, []localruntime.RestoredRuntimeSession{restored})
		if errors.Is(err, localruntime.ErrACPNotRunning) {
			continue
		}
		if err != nil {
			slog.Warn("attach ACP chat", "workspace_id", workspaceID, "session_key", item.SessionKey, "err", err)
			continue
		}
		// The agent can exit while the chat is still pending recovery, which the
		// exit hook skips, so ending recovery must handle that exit.
		s.finishChatRestore(item)
	}
}

// withWorkspaceACP calls then with the workspace's stored runtimes under its
// runtime lock and setup admission.
func (s *Handler) withWorkspaceACP(ctx context.Context, workspaceID string, then func([]db.WorkspaceRuntimeSession) error) error {
	defer s.workspaceRuntimeLocks.Lock(workspaceID)()
	done, admitted := s.beginWorkspaceSetup(workspaceID)
	if !admitted {
		return errWorkspaceACPNotAdmitted
	}
	defer s.finishWorkspaceSetup(workspaceID, done)
	stored, err := s.workspaces.RuntimeSessionsForWorkspace(ctx, workspaceID)
	if err != nil {
		return fmt.Errorf("list ACP sessions: %w", err)
	}
	return then(stored)
}

// storedWorkspaceChat serves a stored chat whose agent is not running from
// its saved session. The first command that needs the agent starts it; see
// startWorkspaceChat. It returns localruntime.ErrSessionNotFound when the
// workspace has no such chat, and errChatNotRunning for one this daemon saw
// exit.
func (s *Handler) storedWorkspaceChat(ctx context.Context, ws *db.Workspace, key string) (*localruntime.DormantACP, error) {
	stored, err := s.workspaces.RuntimeSessionsForWorkspace(ctx, ws.ID)
	if err != nil {
		return nil, fmt.Errorf("list runtime sessions: %w", err)
	}
	if _, ok := storedChat(stored, key); !ok {
		return nil, localruntime.ErrSessionNotFound
	}
	if s.runtime.Exited(key) {
		return nil, errChatNotRunning
	}
	return s.runtime.DormantACP(ws.ID, key, func() (localruntime.ACPChat, error) {
		return s.startWorkspaceChat(ctx, ws, key)
	})
}

// startWorkspaceChat starts a stored chat's agent for a command that needs
// it: it attaches a running owner, replaces an incompatible one, or starts one
// that loads the saved session, and resumes a chat idle stop stopped. A chat
// this daemon saw exit stays down. The start outlives its caller, so a closed
// socket cannot cut it short.
func (s *Handler) startWorkspaceChat(ctx context.Context, ws *db.Workspace, key string) (_ localruntime.ACPChat, err error) {
	ctx, span := workspaceACPTracer.Start(context.WithoutCancel(ctx), "workspace.acp.start",
		trace.WithAttributes(attribute.String("workspace.id", ws.ID), attribute.String("session.key", key)))
	defer func() { tracing.EndSpan(span, err) }()
	var label string
	err = s.withWorkspaceACP(ctx, ws.ID, func(stored []db.WorkspaceRuntimeSession) error {
		span.AddEvent("restore lock acquired")
		item, ok := storedChat(stored, key)
		if !ok {
			return localruntime.ErrSessionNotFound
		}
		label = item.Label
		if s.runtime.Exited(key) {
			return errChatNotRunning
		}
		if idle := s.idle.Load(); idle.Stopped(ctx, item) {
			return idle.Resume(ctx, ws, item)
		}
		return s.restoreACPRow(ctx, ws.WorktreePath, item)
	})
	if err != nil {
		slog.Warn("start ACP chat", "workspace_id", ws.ID, "session_key", key, "err", err)
		return nil, chatStartError(err, label)
	}
	s.setChatCannotReload(key, false)
	return s.runtime.ACP(ws.ID, key)
}

// acpOwnerUnresponsiveMessage is what a chat shows when its agent's process
// runs but never answered while the daemon waited; the log keeps the details.
const acpOwnerUnresponsiveMessage = "Agent not responding. Try again in a minute."

// chatStartError is the error the chat labeled label shows when its agent
// did not start.
func chatStartError(err error, label string) error {
	switch {
	case errors.Is(err, localruntime.ErrACPOwnerUnresponsive):
		return errors.New(acpOwnerUnresponsiveMessage) //nolint:staticcheck // ST1005: the chat shows this text verbatim as full sentences.
	case errors.Is(err, localruntime.ErrACPAgentStartTimeout):
		return errors.New(cmp.Or(label, "The agent") + " took too long to start. Try again.") //nolint:staticcheck // ST1005: the chat shows this text verbatim as full sentences.
	}
	return err
}

// reloadACP reconnects or reloads one stored chat. A chat its agent refuses
// to reload is remembered and reads as exited to the chat API until a start
// succeeds.
func (s *Handler) reloadACP(ctx context.Context, cwd string, item db.WorkspaceRuntimeSession) error {
	err := s.runtime.RestoreRuntimeSessions(ctx, []localruntime.RestoredRuntimeSession{restoredRuntime(item, cwd)})
	if errors.Is(err, localruntime.ErrACPCannotReload) {
		s.setChatCannotReload(item.SessionKey, true)
	}
	return err
}

// restoreACPRow restores one stored chat and records a replacement backend.
func (s *Handler) restoreACPRow(ctx context.Context, cwd string, item db.WorkspaceRuntimeSession) error {
	if err := s.reloadACP(ctx, cwd, item); err != nil {
		return err
	}
	if err := s.recordResumedACP(ctx, item); err != nil {
		return fmt.Errorf("record resumed ACP backend %q: %w", item.SessionKey, err)
	}
	s.finishChatRestore(item)
	return nil
}

// recordResumedACP records the backend of a resumed chat when it differs
// from the stored one.
func (s *Handler) recordResumedACP(ctx context.Context, item db.WorkspaceRuntimeSession) error {
	for _, info := range s.runtime.ListSessions(item.WorkspaceID) {
		if info.Key != item.SessionKey || info.TmuxSession == item.TmuxSession {
			continue
		}
		info.DisplayRegion = item.DisplayRegion
		return s.recordRuntimeSession(ctx, item.WorkspaceID, info, item.Scope)
	}
	return nil
}

// setChatCannotReload remembers, or forgets, that a chat's agent refused to
// reload it. The chat API reports such a chat exited; a person's next command
// or a coordinator's restore tries again.
func (s *Handler) setChatCannotReload(key string, cannot bool) {
	s.runtimeRecoveryMu.Lock()
	defer s.runtimeRecoveryMu.Unlock()
	if !cannot {
		delete(s.cannotReloadChats, key)
		return
	}
	if s.cannotReloadChats == nil {
		s.cannotReloadChats = make(map[string]bool)
	}
	s.cannotReloadChats[key] = true
}

func (s *Handler) chatCannotReload(key string) bool {
	s.runtimeRecoveryMu.Lock()
	defer s.runtimeRecoveryMu.Unlock()
	return s.cannotReloadChats[key]
}
