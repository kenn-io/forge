package localruntime

import (
	"bufio"
	"bytes"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"log/slog"

	acpsdk "github.com/coder/acp-go-sdk"
)

// acpMaxMessage is the longest message line the ACP SDK reads. At a longer
// one its receive loop stops, and every request in flight fails as if the
// agent had exited (acp-go-sdk Connection.receive, maxBufSize).
const acpMaxMessage = 10 << 20

// acpOversizedHead is how much of an oversized message is kept to tell what
// it was. Agents write the small routing fields first: the ID and method, and
// a tool call's ID and status before its output.
const acpOversizedHead = 64 << 10

// acpOversizedRequestMethod replaces the method of an oversized agent
// request. The SDK answers a method it does not know with an error, so the
// agent hears its request refused instead of waiting on it.
const acpOversizedRequestMethod = "kenn-forge/oversized_request"

// acpLineLimitReader reads an agent's messages for the SDK and replaces any
// message too long for the SDK with a short one, so one oversized message,
// such as the output of a command that printed a binary, costs that message
// and not the chat.
type acpLineLimitReader struct {
	src     *bufio.Reader
	limit   int
	pending []byte
	err     error
}

func newACPLineLimitReader(src io.Reader) *acpLineLimitReader {
	return &acpLineLimitReader{src: bufio.NewReaderSize(src, acpOversizedHead), limit: acpMaxMessage}
}

func (r *acpLineLimitReader) Read(p []byte) (int, error) {
	for len(r.pending) == 0 {
		if r.err != nil {
			return 0, r.err
		}
		r.pending, r.err = r.nextLine()
	}
	n := copy(p, r.pending)
	r.pending = r.pending[n:]
	return n, nil
}

// nextLine returns the next message line, or its replacement when the line
// reaches the limit. An oversized line is discarded as it is read, past the
// head kept to describe it.
func (r *acpLineLimitReader) nextLine() ([]byte, error) {
	var line []byte
	for {
		chunk, err := r.src.ReadSlice('\n')
		if len(line)+len(chunk) < r.limit {
			line = append(line, chunk...)
			if errors.Is(err, bufio.ErrBufferFull) {
				continue
			}
			return line, err
		}
		size := len(line) + len(chunk)
		head := append(line, chunk...)
		head = bytes.Clone(head[:min(len(head), acpOversizedHead)])
		for errors.Is(err, bufio.ErrBufferFull) {
			chunk, err = r.src.ReadSlice('\n')
			size += len(chunk)
		}
		return acpOversizedReplacement(head, size), err
	}
}

// acpOversizedMessage is what the head of an oversized message tells.
type acpOversizedMessage struct {
	ID            jsontext.Value
	Method        string
	SessionID     string
	SessionUpdate string
	ToolCallID    string
	Status        string
}

// acpOversizedReplacement returns the line that stands in for an oversized
// message, or nil when nothing can: an oversized notification other than a
// tool call's is dropped.
func acpOversizedReplacement(head []byte, size int) []byte {
	message := scanACPOversizedHead(head)
	notice := fmt.Sprintf("Output omitted: the agent sent %.1f MiB in one message, more than the %d MiB Forge accepts.", float64(size)/(1<<20), acpMaxMessage>>20)
	var replacement any
	switch {
	case len(message.ID) > 0 && message.Method == "":
		replacement = map[string]any{"jsonrpc": "2.0", "id": message.ID, "error": acpsdk.NewInternalError(map[string]any{"error": notice})}
	case len(message.ID) > 0:
		replacement = map[string]any{"jsonrpc": "2.0", "id": message.ID, "method": acpOversizedRequestMethod}
	case message.Method == acpsdk.ClientMethodSessionUpdate && message.SessionID != "" && message.ToolCallID != "" &&
		(message.SessionUpdate == "tool_call" || message.SessionUpdate == "tool_call_update"):
		update := map[string]any{
			"sessionUpdate": message.SessionUpdate,
			"toolCallId":    message.ToolCallID,
			"content":       []any{map[string]any{"type": "content", "content": map[string]any{"type": "text", "text": notice}}},
		}
		if message.SessionUpdate == "tool_call" {
			update["title"] = "Output omitted"
		}
		if message.Status != "" {
			update["status"] = message.Status
		}
		replacement = map[string]any{"jsonrpc": "2.0", "method": message.Method, "params": map[string]any{"sessionId": message.SessionID, "update": update}}
	}
	slog.Warn("replaced an ACP agent message too long to read", "bytes", size, "method", message.Method, "session_update", message.SessionUpdate, "replaced", replacement != nil)
	if replacement == nil {
		return nil
	}
	line, err := json.Marshal(replacement)
	if err != nil {
		return nil
	}
	return append(line, '\n')
}

// scanACPOversizedHead reads the routing fields from the head of a message
// cut short. It keeps what it found before the head ends.
func scanACPOversizedHead(head []byte) acpOversizedMessage {
	var message acpOversizedMessage
	decoder := jsontext.NewDecoder(bytes.NewReader(head))
	readString := func(into *string) error {
		token, err := decoder.ReadToken()
		if err == nil && token.Kind() == '"' {
			*into = token.String()
		}
		return err
	}
	_ = scanACPObject(decoder, func(name string) error {
		switch name {
		case "id":
			value, err := decoder.ReadValue()
			if err == nil {
				message.ID = value.Clone()
			}
			return err
		case "method":
			return readString(&message.Method)
		case "params":
			return scanACPObject(decoder, func(name string) error {
				switch name {
				case "sessionId":
					return readString(&message.SessionID)
				case "update":
					return scanACPObject(decoder, func(name string) error {
						switch name {
						case "sessionUpdate":
							return readString(&message.SessionUpdate)
						case "toolCallId":
							return readString(&message.ToolCallID)
						case "status":
							return readString(&message.Status)
						}
						return decoder.SkipValue()
					})
				}
				return decoder.SkipValue()
			})
		}
		return decoder.SkipValue()
	})
	return message
}

// scanACPObject reads one object, handing each member's name to visit, which
// must read the member's value.
func scanACPObject(decoder *jsontext.Decoder, visit func(name string) error) error {
	token, err := decoder.ReadToken()
	if err != nil {
		return err
	}
	if token.Kind() != '{' {
		return errors.New("not an object")
	}
	for decoder.PeekKind() != '}' {
		name, err := decoder.ReadToken()
		if err != nil {
			return err
		}
		if err := visit(name.String()); err != nil {
			return err
		}
	}
	_, err = decoder.ReadToken()
	return err
}
