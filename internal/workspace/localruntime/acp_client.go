package localruntime

import (
	"context"
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
	case u.AgentMessageChunk != nil:
		content := u.AgentMessageChunk.Content.Text
		if content == nil {
			return nil
		}
		last := len(a.state.Messages) - 1
		if last >= 0 && a.state.Messages[last].Role == "assistant" && (a.promptIndex == nil || last >= *a.promptIndex) {
			a.state.Messages[last].Text += content.Text
		} else {
			a.state.Messages = append(a.state.Messages, ACPMessage{Role: "assistant", Text: content.Text, CreatedAt: time.Now().UTC().Format(time.RFC3339)})
		}
	case u.ToolCall != nil:
		a.updateToolLocked(string(u.ToolCall.ToolCallId), u.ToolCall.Title, string(u.ToolCall.Status))
	case u.ToolCallUpdate != nil:
		title, status := "", ""
		if u.ToolCallUpdate.Title != nil {
			title = *u.ToolCallUpdate.Title
		}
		if u.ToolCallUpdate.Status != nil {
			status = string(*u.ToolCallUpdate.Status)
		}
		a.updateToolLocked(string(u.ToolCallUpdate.ToolCallId), title, status)
	}
	a.trimStateLocked()
	a.changedLocked()
	return nil
}

func (a *ACP) updateToolLocked(id, title, status string) {
	for i := range a.state.Messages {
		item := &a.state.Messages[i]
		if item.Role == "tool" && item.ToolCallID == id {
			if title != "" {
				item.Text = title
			}
			if status != "" {
				item.Status = status
			}
			return
		}
	}
	a.state.Messages = append(a.state.Messages, ACPMessage{Role: "tool", Text: title, ToolCallID: id, Status: status, CreatedAt: time.Now().UTC().Format(time.RFC3339)})
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
