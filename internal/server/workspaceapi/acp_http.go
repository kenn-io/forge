package workspaceapi

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"net/rpc"
	"slices"

	"github.com/danielgtaylor/huma/v2"

	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/server/httpapi"
	"go.kenn.io/forge/internal/workspace/localruntime"
)

// These routes let a coordinator drive a chat over plain HTTP: read its state
// and transcript after a cursor, send commands, and restore a supervised chat
// whose agent exited.

// WorkspaceChatMessage is one transcript message with its transcript index.
type WorkspaceChatMessage struct {
	Index int `json:"index" doc:"Position of the message in the whole transcript."`
	localruntime.ACPMessage
}

// WorkspaceChatState is a chat's published state carrying one page of
// transcript messages instead of the recent window. The answered-request
// ledger stays with the chat's owner, so it is not part of this body.
type WorkspaceChatState struct {
	localruntime.ACPState
	Messages []WorkspaceChatMessage `json:"messages"`
	Answered struct{}               `json:"-"`
}

// WorkspaceChat is a chat snapshot. A chat whose owner has exited carries
// only Exited; a coordinator may restore it.
type WorkspaceChat struct {
	Exited bool `json:"exited" doc:"The chat's agent has exited; no other field is present."`
	*WorkspaceChatState
}

// TransformSchema documents that an exited chat carries no other field.
func (WorkspaceChat) TransformSchema(_ huma.Registry, schema *huma.Schema) *huma.Schema {
	schema.Required = []string{"exited"}
	return schema
}

func newWorkspaceChat(state localruntime.ACPState) WorkspaceChat {
	messages := make([]WorkspaceChatMessage, len(state.Messages))
	for i, message := range state.Messages {
		messages[i] = WorkspaceChatMessage{Index: state.MessageOffset + i, ACPMessage: message}
	}
	state.Messages = nil
	return WorkspaceChat{WorkspaceChatState: &WorkspaceChatState{ACPState: state, Messages: messages}}
}

type getWorkspaceChatInput struct {
	ID         string `path:"id"`
	SessionKey string `path:"session_key"`
	After      int    `query:"after" minimum:"0" default:"0" doc:"Transcript index of the first message to return."`
	Limit      int    `query:"limit" minimum:"1" maximum:"500" default:"100" doc:"Most messages to return."`
}

type getWorkspaceChatOutput struct{ Body WorkspaceChat }

// WorkspaceChatCommand is a chat command. Content answers an elicitation with
// its form values. History paging belongs to the websocket, so Before and
// Limit are not part of this body.
type WorkspaceChatCommand struct {
	localruntime.ACPCommand
	Type    string         `json:"type" enum:"supervise,takeover,config,prompt,unqueue,resume,cancel,permission,elicitation"`
	Content map[string]any `json:"content,omitempty"`
	Before  struct{}       `json:"-"`
	Limit   struct{}       `json:"-"`
}

func (c WorkspaceChatCommand) acpCommand() (localruntime.ACPCommand, error) {
	command := c.ACPCommand
	command.Type = c.Type
	if c.Content != nil {
		content, err := json.Marshal(c.Content, json.Deterministic(true))
		if err != nil {
			return command, httpapi.Validation("body.content", "content must be a JSON object")
		}
		command.Content = jsontext.Value(content)
	}
	return command, nil
}

type runWorkspaceChatCommandInput struct {
	ID         string `path:"id"`
	SessionKey string `path:"session_key"`
	Body       WorkspaceChatCommand
}

type runWorkspaceChatCommandOutput struct {
	Body struct {
		Accepted bool `json:"accepted"`
	}
}

type restoreWorkspaceChatInput struct {
	ID         string `path:"id"`
	SessionKey string `path:"session_key"`
}

type restoreWorkspaceChatOutput struct {
	Body struct {
		Restored bool `json:"restored"`
	}
}

// errChatNotRunning reports a stored chat whose owner is not running.
var errChatNotRunning = errors.New("chat agent is not running; restore the chat")

func (s *Handler) getWorkspaceChat(ctx context.Context, input *getWorkspaceChatInput) (*getWorkspaceChatOutput, error) {
	chat, err := s.openWorkspaceChat(ctx, input.ID, input.SessionKey)
	if errors.Is(err, errChatNotRunning) {
		return &getWorkspaceChatOutput{Body: WorkspaceChat{Exited: true}}, nil
	}
	if err != nil {
		return nil, err
	}
	data, err := chat.Page(input.After, input.Limit)
	if err != nil {
		return nil, httpapi.ServiceUnavailable("read chat: " + err.Error())
	}
	var state localruntime.ACPState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, httpapi.Internal("decode chat: " + err.Error())
	}
	return &getWorkspaceChatOutput{Body: newWorkspaceChat(state)}, nil
}

func (s *Handler) runWorkspaceChatCommand(ctx context.Context, input *runWorkspaceChatCommandInput) (*runWorkspaceChatCommandOutput, error) {
	command, err := input.Body.acpCommand()
	if err != nil {
		return nil, err
	}
	chat, err := s.openWorkspaceChat(ctx, input.ID, input.SessionKey)
	if errors.Is(err, errChatNotRunning) {
		return nil, httpapi.ServiceUnavailable(err.Error())
	}
	if err != nil {
		return nil, err
	}
	if err := chat.Command(command); err != nil {
		return nil, chatCommandProblem(err)
	}
	output := &runWorkspaceChatCommandOutput{}
	output.Body.Accepted = true
	return output, nil
}

// chatCommandProblem maps a refused command to a problem. Conflicts carry the
// owner's error code as details.reason.
func chatCommandProblem(err error) error {
	if code := localruntime.ACPErrorCode(err); code != "" {
		return httpapi.Conflict(httpapi.CodeConflict, err.Error(), map[string]any{"reason": code})
	}
	if errors.Is(err, localruntime.ErrACPAgentUnavailable) {
		return httpapi.ServiceUnavailable(err.Error())
	}
	// The owner answered and refused the command; anything else means it
	// could not answer.
	if _, refused := errors.AsType[rpc.ServerError](err); refused {
		return httpapi.BadRequest(httpapi.CodeBadRequest, err.Error(), nil)
	}
	return httpapi.ServiceUnavailable("send chat command: " + err.Error())
}

// openWorkspaceChat reconnects the workspace's chats, as opening the workspace
// does, and returns the running chat. It returns errChatNotRunning for a
// stored chat whose owner is not running.
func (s *Handler) openWorkspaceChat(ctx context.Context, workspaceID, key string) (localruntime.ACPChat, error) {
	summary, err := s.getRuntimeWorkspace(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	s.restoreWorkspaceACP(ctx, summary.ID, summary.WorktreePath)
	if chat, err := s.runtime.ACP(summary.ID, key); err == nil {
		return chat, nil
	}
	stored, err := s.workspaces.RuntimeSessionsForWorkspace(ctx, summary.ID)
	if err != nil {
		return nil, httpapi.Internal("list runtime sessions: " + err.Error())
	}
	if _, ok := storedChat(stored, key); ok {
		return nil, errChatNotRunning
	}
	return nil, httpapi.NotFound(httpapi.CodeNotFound, "chat not found", nil)
}

func storedChat(stored []db.WorkspaceRuntimeSession, key string) (db.WorkspaceRuntimeSession, bool) {
	index := slices.IndexFunc(stored, func(item db.WorkspaceRuntimeSession) bool {
		return item.SessionKey == key && item.Kind == string(localruntime.LaunchTargetACP)
	})
	if index < 0 {
		return db.WorkspaceRuntimeSession{}, false
	}
	return stored[index], true
}

func (s *Handler) restoreWorkspaceChat(ctx context.Context, input *restoreWorkspaceChatInput) (*restoreWorkspaceChatOutput, error) {
	summary, err := s.getRuntimeWorkspace(ctx, input.ID)
	if err != nil {
		return nil, err
	}
	err = s.openWorkspaceACP(ctx, summary.ID, summary.WorktreePath, func(stored []db.WorkspaceRuntimeSession) error {
		return s.restoreSupervisedChat(ctx, summary.WorktreePath, stored, input.SessionKey)
	})
	if errors.Is(err, errWorkspaceACPNotAdmitted) {
		return nil, httpapi.Conflict(httpapi.CodeWorkspaceSetupInProgress, errWorkspaceACPNotAdmitted.Error(), nil)
	}
	if err != nil {
		if _, ok := errors.AsType[huma.StatusError](err); ok {
			return nil, err
		}
		return nil, httpapi.Internal("restore chat: " + err.Error())
	}
	output := &restoreWorkspaceChatOutput{}
	output.Body.Restored = true
	return output, nil
}

// restoreSupervisedChat restores a supervised chat from the daemon's stored
// record, even after its agent exited, and records a changed backend.
func (s *Handler) restoreSupervisedChat(ctx context.Context, cwd string, stored []db.WorkspaceRuntimeSession, key string) error {
	item, ok := storedChat(stored, key)
	if !ok {
		return httpapi.NotFound(httpapi.CodeNotFound, "chat not found", nil)
	}
	supervised, err := s.runtime.SupervisedACP(item.WorkspaceID, key)
	switch {
	case errors.Is(err, localruntime.ErrSessionNotFound):
		return httpapi.NotFound(httpapi.CodeNotFound, "chat not found", nil)
	case err != nil:
		return httpapi.Internal("read saved chat: " + err.Error())
	case !supervised:
		return httpapi.Conflict(httpapi.CodeConflict, "only a supervised chat can be restored", map[string]any{"reason": "not_supervised"})
	}
	err = s.runtime.RestoreSupervisedACP(ctx, localruntime.RestoredRuntimeSession{
		WorkspaceID: item.WorkspaceID, SessionKey: key, TargetKey: item.TargetKey, Label: item.Label,
		Kind: localruntime.LaunchTargetACP, TmuxSession: item.TmuxSession, CWD: cwd, CreatedAt: item.CreatedAt,
	})
	if err != nil {
		return restoreChatProblem(err)
	}
	if err := s.recordResumedACP(ctx, item); err != nil {
		return httpapi.Internal("record restored chat: " + err.Error())
	}
	s.finishChatRestore(item)
	return nil
}

// finishChatRestore ends a restored chat's recovery. The exit hook skips a
// chat pending recovery, so an agent that exited before the flag cleared is
// handled here; one that exits later is handled by the hook. The runtime marks
// a session exited before it calls the hook, so one of the two always sees
// the exit, and handling it twice is harmless.
func (s *Handler) finishChatRestore(item db.WorkspaceRuntimeSession) {
	s.setRuntimeRecoveryPending(item.SessionKey, false)
	if s.runtime.Exited(item.SessionKey) {
		s.HandleRuntimeSessionExit(localruntime.SessionInfo{
			Key: item.SessionKey, WorkspaceID: item.WorkspaceID, TargetKey: item.TargetKey,
			Kind: localruntime.LaunchTargetACP, CreatedAt: item.CreatedAt, TmuxSession: item.TmuxSession,
		})
	}
}

func restoreChatProblem(err error) error {
	switch {
	case errors.Is(err, localruntime.ErrACPCannotReload):
		return httpapi.Conflict(httpapi.CodeConflict, err.Error(), map[string]any{"reason": "cannot_reload"})
	case errors.Is(err, localruntime.ErrSessionNotFound):
		return httpapi.NotFound(httpapi.CodeNotFound, "chat not found", nil)
	case errors.Is(err, localruntime.ErrSessionUnavailable):
		return httpapi.ServiceUnavailable(err.Error())
	default:
		return httpapi.Internal("restore chat: " + err.Error())
	}
}
