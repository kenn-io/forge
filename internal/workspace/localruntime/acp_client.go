package localruntime

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"slices"
	"time"

	acpsdk "github.com/coder/acp-go-sdk"
)

var _ acpsdk.Client = (*ACP)(nil)

func (a *ACP) SessionUpdate(_ context.Context, params acpsdk.SessionNotification) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.sessionID != "" && string(params.SessionId) != a.sessionID {
		return nil
	}
	u := params.Update
	// A reload replays history; the saved transcript already holds it. Session
	// settings and commands the agent sends meanwhile are current, not history.
	if a.replaying && u.ConfigOptionUpdate == nil && u.AvailableCommandsUpdate == nil && u.SessionInfoUpdate == nil {
		return nil
	}
	switch {
	case u.ConfigOptionUpdate != nil:
		a.state.ConfigOptions = acpConfigOptions(u.ConfigOptionUpdate.ConfigOptions)
	case u.AvailableCommandsUpdate != nil:
		a.state.Commands = acpCommands(u.AvailableCommandsUpdate.AvailableCommands)
	case u.SessionInfoUpdate != nil:
		a.threadStatusLocked(u.SessionInfoUpdate.Meta)
		return nil
	case u.AgentMessageChunk != nil:
		if !a.appendContentLocked("assistant", u.AgentMessageChunk.Content, deref(u.AgentMessageChunk.MessageId)) {
			return nil
		}
	case u.UserMessageChunk != nil && a.importingHistory:
		if !a.appendContentLocked("user", u.UserMessageChunk.Content, deref(u.UserMessageChunk.MessageId)) {
			return nil
		}
	case u.AgentThoughtChunk != nil:
		if !a.appendContentLocked("thought", u.AgentThoughtChunk.Content, deref(u.AgentThoughtChunk.MessageId)) {
			return nil
		}
	case u.UsageUpdate != nil:
		if !a.usageLocked(u.UsageUpdate) {
			return nil
		}
	case u.Plan != nil:
		a.state.Plan = acpPlan(u.Plan.Entries)
	case u.ToolCall != nil:
		// Tool activity follows the text the agent produced before it.
		turn := a.agentOutputLocked()
		a.releaseHeldTextLocked()
		call := u.ToolCall
		a.trackTerminalLocked(string(call.ToolCallId), call.Meta)
		status := string(call.Status)
		if status == "" {
			status = string(acpsdk.ToolCallStatusPending)
		}
		lineage := toolCallLineage(call.Meta, call.RawInput)
		a.insertMessageLocked(len(a.state.Messages), ACPMessage{
			Role: "tool", Turn: turn, Text: call.Title, ToolCallID: string(call.ToolCallId), Status: status,
			Subagent: lineage.subagent, ParentToolCallID: lineage.parent, Kind: string(call.Kind),
			ToolContent: applyTerminalMeta(acpToolContent(call.Content), call.Meta), Locations: acpToolLocations(call.Locations),
			RawInput: acpRawJSON(call.RawInput), RawOutput: acpRawJSON(call.RawOutput),
			CreatedAt: time.Now().UTC().Format(time.RFC3339),
		})
		a.refreshBlockedLocked()
	case u.ToolCallUpdate != nil:
		a.releaseHeldTextLocked()
		a.trackTerminalLocked(string(u.ToolCallUpdate.ToolCallId), u.ToolCallUpdate.Meta)
		a.updateToolLocked(u.ToolCallUpdate)
		a.refreshBlockedLocked()
	default:
		// User echoes and mode updates are not shown.
		return nil
	}
	a.publishProgressLocked()
	return nil
}

// threadStatusLocked follows Codex thread status so a turn the agent started
// after a steering request keeps the chat busy until the thread goes idle.
func (a *ACP) threadStatusLocked(meta map[string]any) {
	codex, _ := meta["codex"].(map[string]any)
	status, _ := codex["threadStatus"].(map[string]any)
	kind, _ := status["type"].(string)
	if kind != "active" && kind != "idle" {
		return
	}
	a.reportsThreadStatus = true
	// An idle before the replacement's active transition belongs to the old
	// turn; statuses are ordered but carry no turn correlation.
	if kind == "active" || a.threadStatus == "active" {
		a.threadStatus = kind
	}
	if kind == "active" && a.external == nil && a.takeoverPending {
		// The first status after a takeover confirms it, even when the
		// original prompt has already completed.
		a.beginExternalTurnLocked(ACPQueuedPrompt{Sender: ACPSender{Kind: ACPSenderUser}}, a.lastTurnLocked())
		a.changedLocked()
		return
	}
	a.reconcileExternalTurnLocked(kind)
}

// beginExternalTurnLocked confirms the takeover's existing start or the newly
// accepted steer. A takeover during a running prompt shares that prompt's range.
func (a *ACP) beginExternalTurnLocked(prompt ACPQueuedPrompt, first int) {
	a.takeoverPending = false
	if a.external == nil {
		a.external = &acpExternalTurn{promptDone: !a.state.Busy}
	}
	a.state.Busy = true
	if a.runningTurnRecordLocked() == nil {
		a.beginTurnRecordLocked(prompt, first)
	} else if a.state.Messages[first].Turn != nil {
		a.state.Messages[first].Turn = nil
		a.publishMessageLocked(first)
	}
}

// reconcileExternalTurnLocked applies observed status when a takeover or its
// prompt response arrives. Retained idle status has already followed active.
func (a *ACP) reconcileExternalTurnLocked(kind string) {
	if a.external == nil {
		return
	}
	if kind == "idle" && a.external.promptDone {
		a.external = nil
		// Idle ends the takeover without reporting its outcome.
		a.endTurnLocked("", nil)
		go a.drain()
	}
}

// Each update replaces the advertised set; it is not a delta.
func acpCommands(commands []acpsdk.AvailableCommand) []ACPCommandInfo {
	out := make([]ACPCommandInfo, 0, len(commands))
	for _, command := range commands {
		info := ACPCommandInfo{Name: command.Name, Description: command.Description}
		if command.Input != nil && command.Input.Unstructured != nil {
			info.InputHint = command.Input.Unstructured.Hint
		}
		out = append(out, info)
	}
	return out
}

// appendContentLocked adds one streamed agent or thought block. Text joins
// the current message of the same role and message ID and is held for block
// delivery; any other content is its own entry. It reports whether the chat
// must publish now; held text publishes on its own schedule.
func (a *ACP) appendContentLocked(role string, block acpsdk.ContentBlock, messageID string) bool {
	text, content := acpContent(block)
	if text == "" && content == nil {
		return false
	}
	turn := a.agentOutputLocked()
	last := len(a.state.Messages) - 1
	// Consecutive text of one role is one message while its message ID stays
	// the same; any change, including gaining or losing an ID, starts a new
	// message.
	continues := last >= 0 && a.state.Messages[last].Role == role && a.state.Messages[last].Content == nil &&
		a.state.Messages[last].MessageID == messageID && (a.promptIndex == nil || last >= *a.promptIndex) && turn == nil
	if content != nil && role == "user" && content.Type == "image" {
		// The chat shows a user's images, like a prompt sent from Forge, with
		// the prompt's text.
		a.releaseHeldTextLocked()
		if continues {
			a.state.Messages[last].Images = append(a.state.Messages[last].Images, *content)
			a.publishMessageLocked(last)
		} else {
			a.insertMessageLocked(len(a.state.Messages), ACPMessage{Role: role, Turn: turn, MessageID: messageID, Images: []ACPContent{*content}, CreatedAt: time.Now().UTC().Format(time.RFC3339)})
		}
		return true
	}
	if content != nil {
		a.releaseHeldTextLocked()
		a.insertMessageLocked(len(a.state.Messages), ACPMessage{Role: role, Turn: turn, MessageID: messageID, Content: content, CreatedAt: time.Now().UTC().Format(time.RFC3339)})
		return true
	}
	if continues {
		a.state.Messages[last].Text += text
		a.holdTextLocked(false, text)
		return false
	}
	if a.heldBytes > 0 {
		// The finished message becomes visible before the next one starts.
		a.releaseHeldTextLocked()
		a.publishProgressLocked()
	}
	a.insertMessageLocked(len(a.state.Messages), ACPMessage{Role: role, Turn: turn, MessageID: messageID, Text: text, CreatedAt: time.Now().UTC().Format(time.RFC3339)})
	a.holdTextLocked(true, text)
	return false
}

// acpToolLineage is what a tool call says about delegated agents. ACP has no
// stable subagent session model yet, so this reads the markers that adapters
// put on ordinary tool calls when native subagent sessions are not negotiated.
type acpToolLineage struct {
	subagent bool
	parent   string
}

func toolCallLineage(meta map[string]any, rawInput any) acpToolLineage {
	var lineage acpToolLineage
	// claude-agent-acp names the Claude Code tool and the Agent/Task tool use
	// that a child tool call ran under.
	if claude, ok := meta["claudeCode"].(map[string]any); ok {
		if name, _ := claude["toolName"].(string); name == "Agent" || name == "Task" {
			lineage.subagent = true
		}
		lineage.parent, _ = claude["parentToolUseId"].(string)
	}
	// codex-acp reports subagent activity and collaboration spawns through
	// these rawInput keys.
	if input, ok := rawInput.(map[string]any); ok {
		_, thread := input["agentThreadId"]
		_, path := input["agentPath"]
		_, receivers := input["receiverThreadIds"]
		_, states := input["agentsStates"]
		if (thread && path) || (receivers && states) {
			lineage.subagent = true
		}
	}
	return lineage
}

// updateToolLocked changes the newest tool call with this ID. An update for a
// tool call the chat never saw is ignored rather than invented. Present
// fields replace the stored ones, as ACP specifies; absent fields are kept.
func (a *ACP) updateToolLocked(update *acpsdk.SessionToolCallUpdate) {
	for i := len(a.state.Messages) - 1; i >= 0; i-- {
		item := &a.state.Messages[i]
		if item.Role != "tool" || item.ToolCallID != string(update.ToolCallId) {
			continue
		}
		if update.Title != nil && *update.Title != "" {
			item.Text = *update.Title
		}
		if update.Status != nil {
			item.Status = string(*update.Status)
		}
		if update.Kind != nil {
			item.Kind = string(*update.Kind)
		}
		if update.Content != nil {
			item.ToolContent = replaceToolContent(item.ToolContent, acpToolContent(update.Content))
		}
		item.ToolContent = applyTerminalMeta(item.ToolContent, update.Meta)
		if update.Locations != nil {
			item.Locations = acpToolLocations(update.Locations)
		}
		if update.RawInput != nil {
			item.RawInput = acpRawJSON(update.RawInput)
		}
		if update.RawOutput != nil {
			item.RawOutput = acpRawJSON(update.RawOutput)
		}
		// Updates may omit the markers; never clear them once seen.
		lineage := toolCallLineage(update.Meta, update.RawInput)
		item.Subagent = item.Subagent || lineage.subagent
		if item.ParentToolCallID == "" {
			item.ParentToolCallID = lineage.parent
		}
		a.publishMessageLocked(i)
		return
	}
}

func (a *ACP) RequestPermission(ctx context.Context, params acpsdk.RequestPermissionRequest) (acpsdk.RequestPermissionResponse, error) {
	a.mu.Lock()
	if a.cancelling {
		a.mu.Unlock()
		return acpsdk.RequestPermissionResponse{Outcome: acpsdk.NewRequestPermissionOutcomeCancelled()}, nil
	}
	permission := ACPPermission{ID: a.newRequestIDLocked("p"), toolCallID: string(params.ToolCall.ToolCallId)}
	if params.ToolCall.Title != nil {
		permission.Title = *params.ToolCall.Title
	}
	for _, option := range params.Options {
		permission.Options = append(permission.Options, ACPPermissionOption{OptionID: string(option.OptionId), Name: option.Name, Kind: string(option.Kind)})
	}
	response := make(chan acpsdk.RequestPermissionOutcome, 1)
	a.permissions[permission.ID] = response
	a.state.Permissions = append(a.state.Permissions, permission)
	a.refreshBlockedLocked()
	a.changedLocked()
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		defer a.mu.Unlock()
		delete(a.permissions, permission.ID)
		a.state.Permissions = slices.DeleteFunc(a.state.Permissions, func(p ACPPermission) bool { return p.ID == permission.ID })
		a.refreshBlockedLocked()
		a.changedLocked()
	}()
	select {
	case outcome := <-response:
		return acpsdk.RequestPermissionResponse{Outcome: outcome}, nil
	case <-ctx.Done():
		return acpsdk.RequestPermissionResponse{Outcome: acpsdk.NewRequestPermissionOutcomeCancelled()}, nil
	case <-a.done:
		return acpsdk.RequestPermissionResponse{Outcome: acpsdk.NewRequestPermissionOutcomeCancelled()}, nil
	}
}

// UnstableCreateElicitation holds a form elicitation until the chat answers it,
// the turn is cancelled, or the agent exits. Forge advertises only form mode.
func (a *ACP) UnstableCreateElicitation(ctx context.Context, params acpsdk.UnstableCreateElicitationRequest) (acpsdk.UnstableCreateElicitationResponse, error) {
	if params.Form == nil {
		return acpsdk.NewUnstableCreateElicitationResponseDecline(), nil
	}
	a.mu.Lock()
	if a.cancelling {
		a.mu.Unlock()
		return acpsdk.NewUnstableCreateElicitationResponseCancel(), nil
	}
	schema := params.Form.RequestedSchema
	elicitation := ACPElicitation{ID: a.newRequestIDLocked("e"), Message: params.Form.Message, Schema: ACPElicitationSchema{Properties: schema.Properties, Required: schema.Required}}
	if schema.Title != nil {
		elicitation.Schema.Title = *schema.Title
	}
	if schema.Description != nil {
		elicitation.Schema.Description = *schema.Description
	}
	if elicitation.Schema.Properties == nil {
		elicitation.Schema.Properties = map[string]any{}
	}
	response := make(chan acpsdk.UnstableCreateElicitationResponse, 1)
	a.elicitations[elicitation.ID] = response
	a.state.Elicitations = append(a.state.Elicitations, elicitation)
	a.refreshBlockedLocked()
	a.changedLocked()
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		defer a.mu.Unlock()
		delete(a.elicitations, elicitation.ID)
		a.state.Elicitations = slices.DeleteFunc(a.state.Elicitations, func(e ACPElicitation) bool { return e.ID == elicitation.ID })
		a.refreshBlockedLocked()
		a.changedLocked()
	}()
	select {
	case answer := <-response:
		return answer, nil
	case <-ctx.Done():
		return acpsdk.NewUnstableCreateElicitationResponseCancel(), nil
	case <-a.done:
		return acpsdk.NewUnstableCreateElicitationResponseCancel(), nil
	}
}

// UnstableCompleteElicitation reports a finished URL-mode elicitation, which
// Forge never offers.
func (*ACP) UnstableCompleteElicitation(context.Context, acpsdk.UnstableCompleteElicitationNotification) error {
	return nil
}

func (a *ACP) answerElicitation(command ACPCommand) error {
	var answer acpsdk.UnstableCreateElicitationResponse
	content := map[string]any{}
	switch command.Action {
	case "accept":
		if len(command.Content) > 0 {
			if err := json.Unmarshal(command.Content, &content); err != nil || content == nil {
				return errors.New("elicitation content must be a JSON object")
			}
		}
		answer = acpsdk.NewUnstableCreateElicitationResponseAccept()
		answer.Accept.Content = content
	case "decline":
		answer = acpsdk.NewUnstableCreateElicitationResponseDecline()
	case "cancel":
		answer = acpsdk.NewUnstableCreateElicitationResponseCancel()
	default:
		return errors.New("elicitation action must be accept, decline, or cancel")
	}
	key, err := elicitationAnswerKey(command.Action, content)
	if err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.supervisionGateLocked(command.Generation); err != nil {
		return err
	}
	response, ok := a.elicitations[command.ID]
	if !ok {
		if replay, err := a.answeredLocked(command.ID, key); replay || err != nil {
			return err
		}
		return errACPNotPending
	}
	// A coordinator answers only the requests of its own turn.
	if err := a.turnGateLocked(command.Generation); err != nil {
		return err
	}
	delete(a.elicitations, command.ID)
	a.state.Elicitations = slices.DeleteFunc(a.state.Elicitations, func(e ACPElicitation) bool { return e.ID == command.ID })
	response <- answer
	a.refreshBlockedLocked()
	a.recordAnswerLocked(command.ID, key)
	a.saveTakenEffectLocked("answering an elicitation")
	a.changedLocked()
	return nil
}

// answerPermission selects one option of a pending permission request.
func (a *ACP) answerPermission(command ACPCommand) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.supervisionGateLocked(command.Generation); err != nil {
		return err
	}
	response, ok := a.permissions[command.ID]
	if !ok {
		if replay, err := a.answeredLocked(command.ID, command.OptionID); replay || err != nil {
			return err
		}
		return errACPNotPending
	}
	// A coordinator answers only the requests of its own turn.
	if err := a.turnGateLocked(command.Generation); err != nil {
		return err
	}
	index := slices.IndexFunc(a.state.Permissions, func(p ACPPermission) bool {
		return p.ID == command.ID && slices.ContainsFunc(p.Options, func(o ACPPermissionOption) bool { return o.OptionID == command.OptionID })
	})
	if index < 0 {
		return fmt.Errorf("permission request has no option %q", command.OptionID)
	}
	delete(a.permissions, command.ID)
	a.state.Permissions = slices.Delete(a.state.Permissions, index, index+1)
	response <- acpsdk.NewRequestPermissionOutcomeSelected(acpsdk.PermissionOptionId(command.OptionID))
	a.refreshBlockedLocked()
	a.recordAnswerLocked(command.ID, command.OptionID)
	a.saveTakenEffectLocked("answering a permission")
	a.changedLocked()
	return nil
}

// Forge does not advertise file or terminal capabilities. Agents own these
// operations on the execution host; the SDK returns method-not-found if asked.
func (*ACP) ReadTextFile(context.Context, acpsdk.ReadTextFileRequest) (acpsdk.ReadTextFileResponse, error) {
	return acpsdk.ReadTextFileResponse{}, acpsdk.NewMethodNotFound(acpsdk.ClientMethodFsReadTextFile)
}

func (*ACP) WriteTextFile(context.Context, acpsdk.WriteTextFileRequest) (acpsdk.WriteTextFileResponse, error) {
	return acpsdk.WriteTextFileResponse{}, acpsdk.NewMethodNotFound(acpsdk.ClientMethodFsWriteTextFile)
}

func (*ACP) CreateTerminal(context.Context, acpsdk.CreateTerminalRequest) (acpsdk.CreateTerminalResponse, error) {
	return acpsdk.CreateTerminalResponse{}, acpsdk.NewMethodNotFound(acpsdk.ClientMethodTerminalCreate)
}

func (*ACP) KillTerminal(context.Context, acpsdk.KillTerminalRequest) (acpsdk.KillTerminalResponse, error) {
	return acpsdk.KillTerminalResponse{}, acpsdk.NewMethodNotFound(acpsdk.ClientMethodTerminalKill)
}

func (*ACP) TerminalOutput(context.Context, acpsdk.TerminalOutputRequest) (acpsdk.TerminalOutputResponse, error) {
	return acpsdk.TerminalOutputResponse{}, acpsdk.NewMethodNotFound(acpsdk.ClientMethodTerminalOutput)
}

func (*ACP) ReleaseTerminal(context.Context, acpsdk.ReleaseTerminalRequest) (acpsdk.ReleaseTerminalResponse, error) {
	return acpsdk.ReleaseTerminalResponse{}, acpsdk.NewMethodNotFound(acpsdk.ClientMethodTerminalRelease)
}

func (*ACP) WaitForTerminalExit(context.Context, acpsdk.WaitForTerminalExitRequest) (acpsdk.WaitForTerminalExitResponse, error) {
	return acpsdk.WaitForTerminalExitResponse{}, acpsdk.NewMethodNotFound(acpsdk.ClientMethodTerminalWaitForExit)
}
