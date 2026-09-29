package localruntime

import (
	"context"
	"encoding/json/v2"
	"errors"
	"slices"
	"strconv"
	"time"

	acpsdk "github.com/coder/acp-go-sdk"
)

var _ acpsdk.Client = (*ACP)(nil)

func (a *ACP) SessionUpdate(_ context.Context, params acpsdk.SessionNotification) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	u := params.Update
	switch {
	case u.ConfigOptionUpdate != nil:
		a.state.ConfigOptions = acpConfigOptions(u.ConfigOptionUpdate.ConfigOptions)
	case u.AvailableCommandsUpdate != nil:
		a.state.Commands = acpCommands(u.AvailableCommandsUpdate.AvailableCommands)
	case u.AgentMessageChunk != nil:
		if content := u.AgentMessageChunk.Content.Text; content != nil {
			a.appendTextLocked("assistant", content.Text)
		}
	case u.UserMessageChunk != nil:
		if content := u.UserMessageChunk.Content.Text; content != nil {
			a.appendTextLocked("user", content.Text)
		}
	case u.ToolCall != nil:
		a.updateToolLocked(string(u.ToolCall.ToolCallId), u.ToolCall.Title, string(u.ToolCall.Status), toolCallLineage(u.ToolCall.Meta, u.ToolCall.RawInput))
	case u.ToolCallUpdate != nil:
		title, status := "", ""
		if u.ToolCallUpdate.Title != nil {
			title = *u.ToolCallUpdate.Title
		}
		if u.ToolCallUpdate.Status != nil {
			status = string(*u.ToolCallUpdate.Status)
		}
		a.updateToolLocked(string(u.ToolCallUpdate.ToolCallId), title, status, toolCallLineage(u.ToolCallUpdate.Meta, u.ToolCallUpdate.RawInput))
	}
	a.trimStateLocked()
	a.changedLocked()
	return nil
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

func (a *ACP) appendTextLocked(role, text string) {
	last := len(a.state.Messages) - 1
	if last >= 0 && a.state.Messages[last].Role == role && (a.promptIndex == nil || last >= *a.promptIndex) {
		a.state.Messages[last].Text += text
	} else {
		a.state.Messages = append(a.state.Messages, ACPMessage{Role: role, Text: text, CreatedAt: time.Now().UTC().Format(time.RFC3339)})
	}
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

func (a *ACP) updateToolLocked(id, title, status string, lineage acpToolLineage) {
	for i := range a.state.Messages {
		item := &a.state.Messages[i]
		if item.Role == "tool" && item.ToolCallID == id {
			if title != "" {
				item.Text = title
			}
			if status != "" {
				item.Status = status
			}
			// Updates may omit the markers; never clear them once seen.
			item.Subagent = item.Subagent || lineage.subagent
			if item.ParentToolCallID == "" {
				item.ParentToolCallID = lineage.parent
			}
			return
		}
	}
	a.state.Messages = append(a.state.Messages, ACPMessage{Role: "tool", Text: title, ToolCallID: id, Status: status, Subagent: lineage.subagent, ParentToolCallID: lineage.parent, CreatedAt: time.Now().UTC().Format(time.RFC3339)})
}

func (a *ACP) RequestPermission(ctx context.Context, params acpsdk.RequestPermissionRequest) (acpsdk.RequestPermissionResponse, error) {
	a.mu.Lock()
	if a.cancelling {
		a.mu.Unlock()
		return acpsdk.RequestPermissionResponse{Outcome: acpsdk.NewRequestPermissionOutcomeCancelled()}, nil
	}
	a.nextPermission++
	permission := ACPPermission{ID: strconv.Itoa(a.nextPermission)}
	if params.ToolCall.Title != nil {
		permission.Title = *params.ToolCall.Title
	}
	for _, option := range params.Options {
		permission.Options = append(permission.Options, ACPPermissionOption{OptionID: string(option.OptionId), Name: option.Name, Kind: string(option.Kind)})
	}
	permissionBytes := acpPermissionBytes(permission)
	if permissionBytes <= maxACPStateBytes {
		a.trimStateToBytesLocked(maxACPStateBytes - permissionBytes)
	}
	if a.retainedStateBytesLocked()+permissionBytes > maxACPStateBytes {
		a.state.Error = "The agent requested more permission data than this chat can retain."
		a.changedLocked()
		a.mu.Unlock()
		return acpsdk.RequestPermissionResponse{Outcome: acpsdk.NewRequestPermissionOutcomeCancelled()}, nil
	}
	response := make(chan acpsdk.RequestPermissionOutcome, 1)
	a.permissions[permission.ID] = response
	a.state.Permissions = append(a.state.Permissions, permission)
	a.changedLocked()
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		defer a.mu.Unlock()
		delete(a.permissions, permission.ID)
		a.state.Permissions = slices.DeleteFunc(a.state.Permissions, func(p ACPPermission) bool { return p.ID == permission.ID })
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
	a.nextPermission++
	schema := params.Form.RequestedSchema
	elicitation := ACPElicitation{ID: "elicitation-" + strconv.Itoa(a.nextPermission), Message: params.Form.Message, Schema: ACPElicitationSchema{Properties: schema.Properties, Required: schema.Required}}
	if schema.Title != nil {
		elicitation.Schema.Title = *schema.Title
	}
	if schema.Description != nil {
		elicitation.Schema.Description = *schema.Description
	}
	if elicitation.Schema.Properties == nil {
		elicitation.Schema.Properties = map[string]any{}
	}
	elicitationBytes := acpElicitationBytes(elicitation)
	if elicitationBytes <= maxACPStateBytes {
		a.trimStateToBytesLocked(maxACPStateBytes - elicitationBytes)
	}
	if a.retainedStateBytesLocked()+elicitationBytes > maxACPStateBytes {
		a.state.Error = "The agent requested more input form data than this chat can retain."
		a.changedLocked()
		a.mu.Unlock()
		return acpsdk.NewUnstableCreateElicitationResponseCancel(), nil
	}
	response := make(chan acpsdk.UnstableCreateElicitationResponse, 1)
	a.elicitations[elicitation.ID] = response
	a.state.Elicitations = append(a.state.Elicitations, elicitation)
	a.changedLocked()
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		defer a.mu.Unlock()
		delete(a.elicitations, elicitation.ID)
		a.state.Elicitations = slices.DeleteFunc(a.state.Elicitations, func(e ACPElicitation) bool { return e.ID == elicitation.ID })
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
	switch command.Action {
	case "accept":
		content := map[string]any{}
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
	a.mu.Lock()
	defer a.mu.Unlock()
	response, ok := a.elicitations[command.ID]
	if !ok {
		return errors.New("elicitation is no longer pending")
	}
	delete(a.elicitations, command.ID)
	a.state.Elicitations = slices.DeleteFunc(a.state.Elicitations, func(e ACPElicitation) bool { return e.ID == command.ID })
	response <- answer
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
