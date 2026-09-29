package localruntime

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"unicode/utf8"

	acpsdk "github.com/coder/acp-go-sdk"
)

// maxACPContentDataBytes bounds one media or blob payload kept in the chat.
// Larger payloads keep their metadata and are marked omitted, so a single
// image cannot consume the transcript budget.
const maxACPContentDataBytes = 2 << 20

// maxACPRawBytes bounds raw tool input or output kept for display.
const maxACPRawBytes = 64 << 10

// ACPContent is one non-text content block from the agent: an image, audio
// clip, resource link, or embedded resource.
type ACPContent struct {
	Type        string `json:"type"`
	Text        string `json:"text,omitempty"`
	MimeType    string `json:"mimeType,omitempty"`
	Data        string `json:"data,omitempty"`
	URI         string `json:"uri,omitempty"`
	Name        string `json:"name,omitempty"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	Size        *int   `json:"size,omitempty"`
	// Omitted marks a payload dropped because it exceeded the size limit.
	Omitted bool `json:"omitted,omitempty"`
}

// ACPToolContent is what a tool call produced: content, a file diff, or a
// reference to an agent-side terminal.
type ACPToolContent struct {
	Type       string      `json:"type"`
	Content    *ACPContent `json:"content,omitempty"`
	Path       string      `json:"path,omitempty"`
	OldText    *string     `json:"oldText,omitempty"`
	NewText    string      `json:"newText,omitempty"`
	TerminalID string      `json:"terminalId,omitempty"`
	// Output and ExitCode are a terminal command's streamed output and exit.
	Output   string `json:"output,omitempty"`
	ExitCode *int   `json:"exitCode,omitempty"`
}

// maxACPTerminalOutputBytes keeps the latest output of one command.
const maxACPTerminalOutputBytes = 64 << 10

// applyTerminalMeta records command output and exit that agents stream in
// tool call _meta under the Zed terminal conventions (terminal_output_delta,
// terminal_output, terminal_exit), keyed by terminal ID.
func applyTerminalMeta(items []ACPToolContent, meta map[string]any) []ACPToolContent {
	terminal := func(id string) *ACPToolContent {
		for i := range items {
			if items[i].Type == "terminal" && items[i].TerminalID == id {
				return &items[i]
			}
		}
		items = append(items, ACPToolContent{Type: "terminal", TerminalID: id})
		return &items[len(items)-1]
	}
	for _, key := range []string{"terminal_output_delta", "terminal_output"} {
		chunk, _ := meta[key].(map[string]any)
		id, _ := chunk["terminal_id"].(string)
		data, _ := chunk["data"].(string)
		if id == "" || data == "" {
			continue
		}
		item := terminal(id)
		item.Output += data
		if extra := len(item.Output) - maxACPTerminalOutputBytes; extra > 0 {
			for extra < len(item.Output) && !utf8.RuneStart(item.Output[extra]) {
				extra++
			}
			item.Output = item.Output[extra:]
		}
	}
	if exit, _ := meta["terminal_exit"].(map[string]any); exit != nil {
		id, _ := exit["terminal_id"].(string)
		if code, ok := exit["exit_code"].(float64); ok && id != "" {
			value := int(code)
			terminal(id).ExitCode = &value
		}
	}
	return items
}

// replaceToolContent applies an update's content collection while keeping
// output already streamed for the same terminals.
func replaceToolContent(previous, next []ACPToolContent) []ACPToolContent {
	for i := range next {
		if next[i].Type != "terminal" {
			continue
		}
		for _, old := range previous {
			if old.Type == "terminal" && old.TerminalID == next[i].TerminalID {
				next[i].Output, next[i].ExitCode = old.Output, old.ExitCode
			}
		}
	}
	return next
}

type ACPToolLocation struct {
	Path string `json:"path"`
	Line *int   `json:"line,omitempty"`
}

type ACPPlanEntry struct {
	Content  string `json:"content"`
	Priority string `json:"priority"`
	Status   string `json:"status"`
}

// acpContent converts a block into chat content. Text blocks return text and
// a nil content; callers stream them as message text.
func acpContent(block acpsdk.ContentBlock) (string, *ACPContent) {
	switch {
	case block.Text != nil:
		return block.Text.Text, nil
	case block.Image != nil:
		content := &ACPContent{Type: "image", MimeType: block.Image.MimeType}
		if block.Image.Uri != nil {
			content.URI = *block.Image.Uri
		}
		content.setData(block.Image.Data)
		return "", content
	case block.Audio != nil:
		content := &ACPContent{Type: "audio", MimeType: block.Audio.MimeType}
		content.setData(block.Audio.Data)
		return "", content
	case block.ResourceLink != nil:
		link := block.ResourceLink
		return "", &ACPContent{
			Type: "resource_link", URI: link.Uri, Name: link.Name, Size: link.Size,
			MimeType: deref(link.MimeType), Title: deref(link.Title), Description: deref(link.Description),
		}
	case block.Resource != nil:
		resource := block.Resource.Resource
		switch {
		case resource.TextResourceContents != nil:
			text := resource.TextResourceContents
			return "", &ACPContent{Type: "resource", URI: text.Uri, MimeType: deref(text.MimeType), Text: text.Text}
		case resource.BlobResourceContents != nil:
			blob := resource.BlobResourceContents
			content := &ACPContent{Type: "resource", URI: blob.Uri, MimeType: deref(blob.MimeType)}
			content.setData(blob.Blob)
			return "", content
		}
	}
	return "", nil
}

func (c *ACPContent) setData(data string) {
	if len(data) > maxACPContentDataBytes {
		c.Omitted = true
		return
	}
	c.Data = data
}

func acpToolContent(items []acpsdk.ToolCallContent) []ACPToolContent {
	out := make([]ACPToolContent, 0, len(items))
	for _, item := range items {
		switch {
		case item.Content != nil:
			text, content := acpContent(item.Content.Content)
			if content == nil {
				content = &ACPContent{Type: "text", Text: text}
			}
			out = append(out, ACPToolContent{Type: "content", Content: content})
		case item.Diff != nil:
			out = append(out, ACPToolContent{Type: "diff", Path: item.Diff.Path, OldText: item.Diff.OldText, NewText: item.Diff.NewText})
		case item.Terminal != nil:
			out = append(out, ACPToolContent{Type: "terminal", TerminalID: item.Terminal.TerminalId})
		}
	}
	return out
}

func acpToolLocations(locations []acpsdk.ToolCallLocation) []ACPToolLocation {
	out := make([]ACPToolLocation, 0, len(locations))
	for _, location := range locations {
		out = append(out, ACPToolLocation{Path: location.Path, Line: location.Line})
	}
	return out
}

func acpPlan(entries []acpsdk.PlanEntry) []ACPPlanEntry {
	entries = entries[:min(len(entries), maxACPListEntries)]
	out := make([]ACPPlanEntry, 0, len(entries))
	for _, entry := range entries {
		out = append(out, ACPPlanEntry{Content: boundText(entry.Content, maxACPPlanEntryBytes), Priority: string(entry.Priority), Status: string(entry.Status)})
	}
	return out
}

// Agent-controlled lists and labels outside the transcript budget are bounded
// here, so one oversized update cannot bloat every snapshot and the saved
// session.
const (
	maxACPListEntries    = 500
	maxACPLabelBytes     = 1 << 10
	maxACPPlanEntryBytes = 4 << 10
	maxACPErrorDataBytes = 16 << 10
)

// boundText cuts text to at most limit bytes on a rune boundary.
func boundText(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut] + "…"
}

// acpRawJSON renders raw tool input or output for display, bounded in size.
func acpRawJSON(value any) string {
	if value == nil {
		return ""
	}
	data, err := json.Marshal(value, jsontext.WithIndent("  "))
	if err != nil {
		return ""
	}
	if len(data) > maxACPRawBytes {
		cut := maxACPRawBytes
		for cut > 0 && !utf8.RuneStart(data[cut]) {
			cut--
		}
		return string(data[:cut]) + "\n…"
	}
	return string(data)
}

func deref[T any](value *T) T {
	var zero T
	if value == nil {
		return zero
	}
	return *value
}
