package workspaceapi

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"strings"

	"go.kenn.io/forge/internal/mcpserver"
	"go.kenn.io/forge/internal/workspace/localruntime"
)

// errACPToolDetailsMoved reports that the entry before the one a client named
// is no longer the tool call it asked for, as after a steering insertion.
var errACPToolDetailsMoved = errors.New("the chat changed before the tool details loaded; try again")

// acpToolSummaries leaves tool call details out of an opening state or a
// history page. People rarely open them, and they make up most of a large
// transcript, so a page of them crowds out the conversation. A client asks
// for one tool call's details with a toolDetails command. Live updates keep
// them, so a tool call someone watches keeps streaming.
func acpToolSummaries(data []byte) []byte {
	var frame map[string]jsontext.Value
	if json.Unmarshal(data, &frame) != nil {
		return data
	}
	changed := false
	if raw, ok := frame["messages"]; ok {
		var messages []localruntime.ACPMessage
		if json.Unmarshal(raw, &messages) == nil && acpOmitToolDetails(messages) {
			if encoded, err := json.Marshal(messages); err == nil {
				frame["messages"] = encoded
				changed = true
			}
		}
	}
	if raw, ok := frame["history"]; ok {
		var history localruntime.ACPHistory
		if json.Unmarshal(raw, &history) == nil && acpOmitToolDetails(history.Messages) {
			if encoded, err := json.Marshal(history); err == nil {
				frame["history"] = encoded
				changed = true
			}
		}
	}
	if changed {
		if encoded, err := json.Marshal(frame); err == nil {
			return encoded
		}
	}
	return data
}

func acpOmitToolDetails(messages []localruntime.ACPMessage) bool {
	changed := false
	for i := range messages {
		message := &messages[i]
		if message.Role != "tool" {
			continue
		}
		var inline []localruntime.ACPToolContent
		for _, item := range message.ToolContent {
			if acpToolContentInline(item) {
				inline = append(inline, item)
			}
		}
		if message.RawInput == "" && message.RawOutput == "" && len(inline) == len(message.ToolContent) {
			continue
		}
		message.RawInput, message.RawOutput, message.ToolContent = "", "", inline
		message.DetailsOmitted = true
		changed = true
	}
	return changed
}

// acpToolContentInline matches the tool content the chat shows beside a
// collapsed tool call: media and generated apps
// (frontend/src/lib/components/acp/ChatToolGroup.svelte).
func acpToolContentInline(item localruntime.ACPToolContent) bool {
	content := item.Content
	if item.Type != "content" || content == nil {
		return false
	}
	mime := strings.ToLower(content.MimeType)
	switch content.Type {
	case "image":
		return true
	case "audio":
		return content.Data != ""
	case "resource":
		return content.Data != "" && (strings.HasPrefix(mime, "image/") || strings.HasPrefix(mime, "video/"))
	case "text":
		var app struct {
			Kind string `json:"kind"`
		}
		return json.Unmarshal([]byte(content.Text), &app) == nil && app.Kind == mcpserver.AppDescriptorKind
	default:
		return false
	}
}

// acpToolDetails returns one tool call with its details as a
// {"toolDetails": ...} frame. The owner pages history by the entry after a
// page, so the client names the entry after the tool call as BeforeID, or
// none for the last entry.
func acpToolDetails(chat workspaceChat, command localruntime.ACPCommand) ([]byte, error) {
	data, err := chat.History(command.BeforeID, 1)
	if err != nil {
		return nil, err
	}
	var page map[string]localruntime.ACPHistory
	if err := json.Unmarshal(data, &page); err != nil {
		return nil, err
	}
	messages := page["history"].Messages
	for i := range messages {
		if messages[i].ID == command.ID {
			acpMessageMediaPreviews(messages[i : i+1])
			return json.Marshal(map[string]localruntime.ACPMessage{"toolDetails": messages[i]})
		}
	}
	return nil, errACPToolDetailsMoved
}
