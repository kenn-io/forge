package localruntime

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"strings"
)

type claudeTranscriptReader struct{}

type claudeTranscriptRecord struct {
	Type             string `json:"type"`
	Timestamp        string `json:"timestamp"`
	IsMeta           bool   `json:"isMeta"`
	IsSidechain      bool   `json:"isSidechain"`
	IsCompactSummary bool   `json:"isCompactSummary"`
	Message          struct {
		ID      string         `json:"id"`
		Content jsontext.Value `json:"content"`
	} `json:"message"`
}

type claudeContentBlock struct {
	Type      string         `json:"type"`
	Text      string         `json:"text"`
	Thinking  string         `json:"thinking"`
	ID        string         `json:"id"`
	Name      string         `json:"name"`
	Input     jsontext.Value `json:"input"`
	ToolUseID string         `json:"tool_use_id"`
	Content   jsontext.Value `json:"content"`
	IsError   bool           `json:"is_error"`
	Source    struct {
		MediaType string `json:"media_type"`
		Data      string `json:"data"`
	} `json:"source"`
}

// apply reads one Claude Code transcript record. Records the agent does not
// show its user, and subagent records, have no chat entry.
func (claudeTranscriptReader) apply(offset int64, record []byte, transcript *acpMirrorTranscript) {
	var parsed claudeTranscriptRecord
	if json.Unmarshal(record, &parsed) != nil || parsed.IsSidechain || parsed.IsMeta || parsed.IsCompactSummary {
		return
	}
	transcript.createdAt = parsed.Timestamp
	switch parsed.Type {
	case "user":
		applyClaudeUser(offset, parsed.Message.Content, transcript)
	case "assistant":
		applyClaudeAssistant(offset, parsed.Message.ID, parsed.Message.Content, transcript)
	}
}

func claudeBlocks(content jsontext.Value) ([]claudeContentBlock, string) {
	var text string
	if json.Unmarshal(content, &text) == nil {
		return nil, text
	}
	var blocks []claudeContentBlock
	if json.Unmarshal(content, &blocks) != nil {
		return nil, ""
	}
	return blocks, ""
}

func applyClaudeUser(offset int64, content jsontext.Value, transcript *acpMirrorTranscript) {
	blocks, text := claudeBlocks(content)
	var images []ACPContent
	for _, block := range blocks {
		switch block.Type {
		case "text":
			text += block.Text
		case "image":
			images = append(images, ACPContent{Type: "image", MimeType: block.Source.MediaType, Data: block.Source.Data})
		case "tool_result":
			transcript.finishTool(block.ToolUseID, block.IsError, claudeToolResultText(block.Content))
		}
	}
	text = claudeUserText(text)
	if text == "" && len(images) == 0 {
		return
	}
	transcript.add(acpTranscriptEntryID(offset, 0), ACPMessage{Role: "user", Text: text, Images: images})
}

// claudeUserText undoes how Claude Code stores a slash command, and drops the
// output of a local command, which is not something the person said.
func claudeUserText(text string) string {
	if strings.HasPrefix(text, "<local-command-stdout>") {
		return ""
	}
	name, ok := claudeTagged(text, "command-name")
	if !ok {
		return text
	}
	args, _ := claudeTagged(text, "command-args")
	return strings.TrimSpace(name + " " + args)
}

func claudeTagged(text, tag string) (string, bool) {
	_, after, ok := strings.Cut(text, "<"+tag+">")
	if !ok {
		return "", false
	}
	value, _, ok := strings.Cut(after, "</"+tag+">")
	return value, ok
}

func claudeToolResultText(content jsontext.Value) string {
	blocks, text := claudeBlocks(content)
	for _, block := range blocks {
		if block.Type == "text" {
			text += block.Text
		}
	}
	return text
}

func applyClaudeAssistant(offset int64, messageID string, content jsontext.Value, transcript *acpMirrorTranscript) {
	blocks, text := claudeBlocks(content)
	if text != "" {
		transcript.add(acpTranscriptEntryID(offset, 0), ACPMessage{Role: "assistant", Text: text, MessageID: messageID})
	}
	for i, block := range blocks {
		id := acpTranscriptEntryID(offset, i)
		switch block.Type {
		case "text":
			if block.Text != "" {
				transcript.add(id, ACPMessage{Role: "assistant", Text: block.Text, MessageID: messageID})
			}
		case "thinking":
			if block.Thinking != "" {
				transcript.add(id, ACPMessage{Role: "thought", Text: block.Thinking, MessageID: messageID})
			}
		case "tool_use":
			transcript.add(id, ACPMessage{
				Role: "tool", Text: claudeToolTitle(block.Name, block.Input), ToolCallID: block.ID,
				Status: "in_progress", Kind: claudeToolKind(block.Name), RawInput: string(block.Input),
				Subagent: block.Name == "Task" || block.Name == "Agent",
			})
		}
	}
}

// claudeToolTitle names a tool call by what it acts on when its input says.
func claudeToolTitle(name string, input jsontext.Value) string {
	var fields struct {
		Description string `json:"description"`
		Command     string `json:"command"`
		FilePath    string `json:"file_path"`
		Pattern     string `json:"pattern"`
		URL         string `json:"url"`
	}
	if json.Unmarshal(input, &fields) != nil {
		return name
	}
	for _, detail := range []string{fields.Description, fields.Command, fields.FilePath, fields.Pattern, fields.URL} {
		if first, _, _ := strings.Cut(detail, "\n"); first != "" {
			return name + " " + first
		}
	}
	return name
}

func claudeToolKind(name string) string {
	switch name {
	case "Bash":
		return "execute"
	case "Read":
		return "read"
	case "Edit", "Write", "NotebookEdit":
		return "edit"
	case "Grep", "Glob":
		return "search"
	case "WebFetch", "WebSearch":
		return "fetch"
	}
	return "other"
}
