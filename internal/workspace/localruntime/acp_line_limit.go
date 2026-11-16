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

// acpLineReadSize is the read buffer behind acpLineLimitReader.
const acpLineReadSize = 64 << 10

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
	return &acpLineLimitReader{src: bufio.NewReaderSize(src, acpLineReadSize), limit: acpMaxMessage}
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
// reaches the limit. An oversized line is discarded as it is read; only its
// routing fields are kept, wherever they sit in it.
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
		var scanner acpRoutingScanner
		scanner.write(line)
		scanner.write(chunk)
		size := len(line) + len(chunk)
		for errors.Is(err, bufio.ErrBufferFull) {
			chunk, err = r.src.ReadSlice('\n')
			scanner.write(chunk)
			size += len(chunk)
		}
		return acpOversizedReplacement(scanner.message, size), err
	}
}

// acpOversizedMessage is the routing an oversized message carries.
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
func acpOversizedReplacement(message acpOversizedMessage, size int) []byte {
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

// acpRoutingCapture bounds a kept routing value. An identifier longer than
// this names nothing Forge routes.
const acpRoutingCapture = acpLineReadSize

// acpRoutingKeyCapture bounds a kept member name, quoted as written. It
// fits the longest routing name, sessionUpdate, with every character
// escaped, so a longer name is not a routing name.
const acpRoutingKeyCapture = 2 + 6*len("sessionUpdate")

// acpRoutingChain names, for each routing object, the member holding the next
// one: the message holds params, and params holds update. Routing fields sit
// directly in one of these three objects and nowhere else.
var acpRoutingChain = [...]string{"params", "update"}

func acpRoutingString(raw []byte) string {
	var value string
	if json.Unmarshal(raw, &value) != nil {
		return ""
	}
	return value
}

// acpRoutingScanner reads the routing fields of one JSON message as its bytes
// pass, in any member order. It keeps no state for the payload beyond a
// nesting depth: the payload of an oversized message can be hundreds of MiB.
type acpRoutingScanner struct {
	message acpOversizedMessage
	// depth counts the open objects and arrays; the outermost route of them
	// are routing objects.
	depth, route int
	// expectKey and key describe the innermost routing object while the
	// scanner is directly inside it.
	expectKey bool
	key       string
	// token is the string ('"') or literal ('l') being read, or 0.
	token     byte
	escaped   bool
	isKey     bool
	capturing bool
	overflow  bool
	limit     int
	raw       []byte
}

func (s *acpRoutingScanner) write(data []byte) {
	for _, c := range data {
		s.next(c)
	}
}

func (s *acpRoutingScanner) next(c byte) {
	switch s.token {
	case '"':
		s.take(c)
		switch {
		case s.escaped:
			s.escaped = false
		case c == '\\':
			s.escaped = true
		case c == '"':
			s.endToken()
		}
		return
	case 'l':
		switch c {
		case ',', '}', ']', ' ', '\t', '\r', '\n':
			s.endToken()
		default:
			s.take(c)
			return
		}
	}
	inRoute := s.route > 0 && s.depth == s.route
	switch c {
	case '{', '[':
		if c == '{' && s.depth == s.route && (s.route == 0 || s.route <= len(acpRoutingChain) && s.key == acpRoutingChain[s.route-1]) {
			s.route++
			s.expectKey = true
		}
		s.depth++
	case '}', ']':
		if s.depth == 0 {
			return
		}
		if s.depth == s.route {
			s.route--
		}
		s.depth--
		s.expectKey = false
	case ':':
		if inRoute {
			s.expectKey = false
		}
	case ',':
		if inRoute {
			s.expectKey = true
		}
	case ' ', '\t', '\r', '\n':
	default:
		s.startToken(c, inRoute)
	}
}

// startToken begins a string or literal, keeping it when it is a member name
// or a routing value of the routing object the scanner is directly in.
func (s *acpRoutingScanner) startToken(c byte, inRoute bool) {
	s.token, s.isKey, s.overflow, s.raw = 'l', false, false, s.raw[:0]
	if c == '"' {
		s.token = '"'
		s.isKey = inRoute && s.expectKey
	}
	s.capturing = inRoute && (s.isKey || s.routingField() != nil)
	s.limit = acpRoutingCapture
	if s.isKey {
		s.limit = acpRoutingKeyCapture
	}
	s.take(c)
}

// routingField is where the value of the current member goes, or nil when
// the member is not a routing field of the routing object it is in.
func (s *acpRoutingScanner) routingField() func([]byte) {
	m := &s.message
	switch {
	case s.route == 1 && s.key == "id":
		return func(raw []byte) { m.ID = jsontext.Value(bytes.Clone(raw)) }
	case s.route == 1 && s.key == "method":
		return func(raw []byte) { m.Method = acpRoutingString(raw) }
	case s.route == 2 && s.key == "sessionId":
		return func(raw []byte) { m.SessionID = acpRoutingString(raw) }
	case s.route == 3 && s.key == "sessionUpdate":
		return func(raw []byte) { m.SessionUpdate = acpRoutingString(raw) }
	case s.route == 3 && s.key == "toolCallId":
		return func(raw []byte) { m.ToolCallID = acpRoutingString(raw) }
	case s.route == 3 && s.key == "status":
		return func(raw []byte) { m.Status = acpRoutingString(raw) }
	}
	return nil
}

func (s *acpRoutingScanner) take(c byte) {
	if !s.capturing {
		return
	}
	if len(s.raw) >= s.limit {
		s.capturing, s.overflow = false, true
		s.raw = s.raw[:0]
		return
	}
	s.raw = append(s.raw, c)
}

func (s *acpRoutingScanner) endToken() {
	s.token, s.escaped = 0, false
	if s.isKey {
		// A name too long to keep is not a routing name.
		s.key = ""
		if s.capturing {
			s.key = acpRoutingString(s.raw)
		}
		s.capturing = false
		return
	}
	if s.capturing {
		if field := s.routingField(); field != nil {
			field(s.raw)
		}
	}
	s.capturing = false
}
