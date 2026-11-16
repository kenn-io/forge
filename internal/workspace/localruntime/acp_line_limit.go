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

// acpRoutingCapture bounds a kept routing value. A longer one names nothing
// Forge can route, so it is not kept.
const acpRoutingCapture = 1 << 10

// acpRoutingPaths are the members an oversized message is routed by, keyed by
// their path from the message's top level.
var acpRoutingPaths = map[string]func(*acpOversizedMessage, []byte){
	"/id":     func(m *acpOversizedMessage, raw []byte) { m.ID = jsontext.Value(bytes.Clone(raw)) },
	"/method": func(m *acpOversizedMessage, raw []byte) { m.Method = acpRoutingString(raw) },
	"/params/sessionId": func(m *acpOversizedMessage, raw []byte) {
		m.SessionID = acpRoutingString(raw)
	},
	"/params/update/sessionUpdate": func(m *acpOversizedMessage, raw []byte) {
		m.SessionUpdate = acpRoutingString(raw)
	},
	"/params/update/toolCallId": func(m *acpOversizedMessage, raw []byte) {
		m.ToolCallID = acpRoutingString(raw)
	},
	"/params/update/status": func(m *acpOversizedMessage, raw []byte) { m.Status = acpRoutingString(raw) },
}

func acpRoutingString(raw []byte) string {
	var value string
	if json.Unmarshal(raw, &value) != nil {
		return ""
	}
	return value
}

// acpRoutingScanner reads the routing members of one JSON message as its
// bytes pass, in any member order, and keeps nothing else: the payload of an
// oversized message can be hundreds of MiB.
type acpRoutingScanner struct {
	message acpOversizedMessage
	frames  []acpRoutingFrame
	// token is the string or literal being read; raw holds it when kept.
	token   byte // 0, '"' for a string, or 'l' for a literal
	escaped bool
	isKey   bool
	keep    string // the path of the value being read
	keeping bool
	raw     []byte
}

// acpNoRoute is the path of a value no routing member can be inside: one in
// an array, or under a key too long to keep.
const acpNoRoute = "\x00"

// acpRoutingFrame is one open object or array.
type acpRoutingFrame struct {
	object    bool
	expectKey bool
	path      string // the container's own path; "" for the top level
	key       string
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
	switch c {
	case '"', '{', '[':
		path, kept := s.valuePath()
		if c != '"' {
			s.frames = append(s.frames, acpRoutingFrame{object: c == '{', expectKey: c == '{', path: path})
			return
		}
		s.token, s.raw = '"', s.raw[:0]
		s.isKey = len(s.frames) > 0 && s.frames[len(s.frames)-1].object && s.frames[len(s.frames)-1].expectKey
		s.keeping = s.isKey || kept
		s.keep = path
		s.take(c)
	case '}', ']':
		if len(s.frames) > 0 {
			s.frames = s.frames[:len(s.frames)-1]
		}
	case ':':
		if len(s.frames) > 0 {
			s.frames[len(s.frames)-1].expectKey = false
		}
	case ',':
		if len(s.frames) > 0 && s.frames[len(s.frames)-1].object {
			s.frames[len(s.frames)-1].expectKey = true
		}
	case ' ', '\t', '\r', '\n':
	default:
		path, kept := s.valuePath()
		s.token, s.raw, s.isKey, s.keeping, s.keep = 'l', s.raw[:0], false, kept, path
		s.take(c)
	}
}

// valuePath is the path of the value starting now, and whether it is a
// routing member. A value inside an array has no routing path.
func (s *acpRoutingScanner) valuePath() (string, bool) {
	if len(s.frames) == 0 {
		return "", false
	}
	top := s.frames[len(s.frames)-1]
	if !top.object || top.path == acpNoRoute || top.key == acpNoRoute {
		return acpNoRoute, false
	}
	path := top.path + "/" + top.key
	_, kept := acpRoutingPaths[path]
	return path, kept
}

func (s *acpRoutingScanner) take(c byte) {
	if !s.keeping {
		return
	}
	if len(s.raw) >= acpRoutingCapture {
		s.keeping = false
		s.raw = s.raw[:0]
		return
	}
	s.raw = append(s.raw, c)
}

func (s *acpRoutingScanner) endToken() {
	keeping := s.keeping
	s.token, s.escaped, s.keeping = 0, false, false
	if !keeping {
		if s.isKey && len(s.frames) > 0 {
			// A key too long to keep names no routing member.
			s.frames[len(s.frames)-1].key = acpNoRoute
		}
		return
	}
	if s.isKey {
		s.frames[len(s.frames)-1].key = acpRoutingString(s.raw)
		return
	}
	acpRoutingPaths[s.keep](&s.message, s.raw)
}
