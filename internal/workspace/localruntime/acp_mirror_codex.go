package localruntime

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"strings"
)

type codexTranscriptReader struct{}

type codexTranscriptRecord struct {
	Type      string `json:"type"`
	Timestamp string `json:"timestamp"`
	Payload   struct {
		Type string     `json:"type"`
		Item *codexItem `json:"item"`
	} `json:"payload"`
}

type codexItem struct {
	Type    string `json:"type"`
	ID      string `json:"id"`
	Content []struct {
		Text string `json:"text"`
	} `json:"content"`
	SummaryText      []string       `json:"summary_text"`
	Command          []string       `json:"command"`
	AggregatedOutput string         `json:"aggregated_output"`
	ExitCode         *int           `json:"exit_code"`
	Status           string         `json:"status"`
	Server           string         `json:"server"`
	Tool             string         `json:"tool"`
	Arguments        jsontext.Value `json:"arguments"`
	Result           jsontext.Value `json:"result"`
}

func (item codexItem) text() string {
	parts := make([]string, 0, len(item.Content))
	for _, part := range item.Content {
		parts = append(parts, part.Text)
	}
	return strings.Join(parts, "")
}

// apply reads one Codex rollout record. Finished items are the rollout's
// conversation; its other records repeat them for the model with injected
// context mixed in, so a tool call appears once it finishes.
func (codexTranscriptReader) apply(offset int64, record []byte, transcript *acpMirrorTranscript) {
	var parsed codexTranscriptRecord
	if json.Unmarshal(record, &parsed) != nil || parsed.Type != "event_msg" ||
		parsed.Payload.Type != "item_completed" || parsed.Payload.Item == nil {
		return
	}
	transcript.createdAt = parsed.Timestamp
	item := *parsed.Payload.Item
	id := acpTranscriptEntryID(offset, 0)
	switch item.Type {
	case "UserMessage":
		transcript.add(id, ACPMessage{Role: "user", Text: item.text()})
	case "AgentMessage":
		transcript.add(id, ACPMessage{Role: "assistant", Text: item.text(), MessageID: item.ID})
	case "Reasoning":
		if summary := strings.Join(item.SummaryText, "\n\n"); summary != "" {
			transcript.add(id, ACPMessage{Role: "thought", Text: summary, MessageID: item.ID})
		}
	case "CommandExecution":
		command := ""
		if len(item.Command) > 0 {
			command = item.Command[len(item.Command)-1]
		}
		transcript.add(id, ACPMessage{Role: "tool", Text: command, ToolCallID: item.ID, Status: "in_progress", Kind: "execute"})
		transcript.finishTool(item.ID, item.ExitCode != nil && *item.ExitCode != 0, item.AggregatedOutput)
	case "McpToolCall":
		transcript.add(id, ACPMessage{
			Role: "tool", Text: item.Server + " " + item.Tool, ToolCallID: item.ID, Status: "in_progress",
			Kind: "other", RawInput: string(item.Arguments), RawOutput: string(item.Result),
		})
		transcript.finishTool(item.ID, item.Status == "failed", "")
	}
}
