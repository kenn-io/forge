package localruntime

import (
	"bufio"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.kenn.io/forge/internal/procutil"
)

// ACP owns a stdio agent on the workspace's execution host. Browser connections
// subscribe to its state; disconnecting a browser does not stop an accepted turn.
type ACP struct {
	turnMu       sync.Mutex
	cancelling   bool
	mu           sync.Mutex
	writeMu      sync.Mutex
	cmd          *exec.Cmd
	stdin        io.WriteCloser
	stdout       io.ReadCloser
	done         chan struct{}
	nextID       int
	pending      map[string]chan acpEnvelope
	subscribers  map[chan struct{}]struct{}
	state        ACPState
	saveConfig   func(map[string]string) error
	configValues map[string]string
	sessionID    string
}

type ACPMessage struct {
	SubmissionID string `json:"submissionId,omitempty"`
	Role         string `json:"role"`
	Text         string `json:"text"`
	CreatedAt    string `json:"createdAt"`
	ToolCallID   string `json:"toolCallId,omitempty"`
	Status       string `json:"status,omitempty"`
}
type ACPPermissionOption struct {
	OptionID string `json:"optionId"`
	Name     string `json:"name"`
	Kind     string `json:"kind"`
}
type ACPPermission struct {
	ID      string                `json:"id"`
	Title   string                `json:"title"`
	Options []ACPPermissionOption `json:"options"`
}
type ACPConfigChoice struct {
	Value   string            `json:"value,omitempty"`
	Name    string            `json:"name"`
	Group   string            `json:"group,omitempty"`
	Options []ACPConfigChoice `json:"options,omitempty"`
}
type ACPConfigOption struct {
	ID           string            `json:"id"`
	Name         string            `json:"name"`
	Description  string            `json:"description,omitempty"`
	Category     string            `json:"category,omitempty"`
	Type         string            `json:"type"`
	CurrentValue string            `json:"currentValue"`
	Options      []ACPConfigChoice `json:"options"`
}
type ACPState struct {
	ConfigOptions []ACPConfigOption `json:"configOptions"`
	Configuring   bool              `json:"configuring"`
	Messages      []ACPMessage      `json:"messages"`
	Permissions   []ACPPermission   `json:"permissions"`
	Busy          bool              `json:"busy"`
	Connected     bool              `json:"connected"`
	Error         string            `json:"error"`
}
type ACPCommand struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	ID       string `json:"id,omitempty"`
	OptionID string `json:"optionId,omitempty"`
	Value    string `json:"value,omitempty"`
}
type acpEnvelope struct {
	JSONRPC string         `json:"jsonrpc"`
	ID      jsontext.Value `json:"id,omitempty"`
	Method  string         `json:"method,omitempty"`
	Params  jsontext.Value `json:"params,omitempty"`
	Result  jsontext.Value `json:"result,omitempty"`
	Error   *acpError      `json:"error,omitempty"`
}
type acpError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func startACPSession(ctx context.Context, info SessionInfo, command []string, cwd string, extraStrip []string, mcpServers []ACPMCPServer) (*session, error) {
	executable, err := resolveExecutable(command[0])
	if err != nil {
		return nil, err
	}
	cmd := procutil.Command(executable, command[1:]...)
	cmd.Dir = cwd
	cmd.Env = SessionEnvironment(os.Environ(), extraStrip)
	configureACPProcess(cmd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, err
	}
	// Diagnostics are not protocol messages and must never enter the chat stream.
	cmd.Stderr = os.Stderr
	a := &ACP{cmd: cmd, stdin: stdin, stdout: stdout, done: make(chan struct{}), pending: make(map[string]chan acpEnvelope), subscribers: make(map[chan struct{}]struct{})}
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return nil, err
	}
	go a.read()
	initCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	initialized, err := a.call(initCtx, "initialize", map[string]any{"protocolVersion": 1, "clientCapabilities": map[string]any{}, "clientInfo": map[string]string{"name": "kenn-forge", "version": "1"}})
	if err == nil {
		var result struct {
			ProtocolVersion   int `json:"protocolVersion"`
			AgentCapabilities struct {
				MCPCapabilities struct {
					HTTP bool `json:"http"`
				} `json:"mcpCapabilities"`
			} `json:"agentCapabilities"`
		}
		err = json.Unmarshal(initialized, &result)
		if err == nil && result.ProtocolVersion != 1 {
			err = fmt.Errorf("unsupported ACP protocol version %d (expected 1)", result.ProtocolVersion)
		}
		if err == nil && len(mcpServers) > 0 && !result.AgentCapabilities.MCPCapabilities.HTTP {
			err = errors.New("this ACP agent does not accept HTTP MCP servers required by Forge")
		}
	}
	if err == nil {
		var result jsontext.Value
		result, err = a.call(initCtx, "session/new", map[string]any{"cwd": cwd, "mcpServers": mcpServers})
		if err == nil {
			var created struct {
				SessionID     string            `json:"sessionId"`
				ConfigOptions []ACPConfigOption `json:"configOptions"`
			}
			err = json.Unmarshal(result, &created)
			if err == nil && created.SessionID == "" {
				err = errors.New("ACP agent returned no session ID")
			}
			a.sessionID = created.SessionID
			a.mu.Lock()
			a.state.ConfigOptions = created.ConfigOptions
			a.mu.Unlock()
		}
	}
	if err != nil {
		_ = a.Stop(context.Background())
		return nil, fmt.Errorf("initialize ACP agent: %w", err)
	}
	a.mu.Lock()
	a.state.Connected = true
	a.changedLocked()
	a.mu.Unlock()
	info.Status = SessionStatusRunning
	return &session{info: info, acp: a, lifecycle: a, done: make(chan struct{})}, nil
}

func (a *ACP) Detach() { _ = a.Stop(context.Background()) }

// TestACP checks the same handshake used by workspace launches without retaining
// a process or creating a workspace. The agent gets an empty temporary directory.
func (m *Manager) TestACP(ctx context.Context, command []string) error {
	if len(command) == 0 || strings.TrimSpace(command[0]) == "" {
		return errors.New("ACP executable is required")
	}
	cwd, err := os.MkdirTemp("", "forge-acp-test-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(cwd)
	session, err := startACPSession(ctx, SessionInfo{}, command, cwd, m.currentStripEnvVars(), m.agentMCPServers())
	if err != nil {
		return err
	}
	return session.acp.Stop(context.Background())
}

func (a *ACP) Stop(ctx context.Context) error {
	_ = a.stdin.Close()
	err := killSessionProcess(a.cmd.Process)
	if err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}
	select {
	case <-a.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (a *ACP) write(message acpEnvelope) error {
	message.JSONRPC = "2.0"
	data, err := json.Marshal(message)
	if err != nil {
		return err
	}
	a.writeMu.Lock()
	defer a.writeMu.Unlock()
	_, err = a.stdin.Write(append(data, '\n'))
	return err
}

func (a *ACP) startCall(method string, params any) (string, <-chan acpEnvelope, error) {
	data, err := json.Marshal(params)
	if err != nil {
		return "", nil, err
	}
	a.mu.Lock()
	a.nextID++
	id := strconv.Itoa(a.nextID)
	response := make(chan acpEnvelope, 1)
	a.pending[id] = response
	a.mu.Unlock()
	if err := a.write(acpEnvelope{ID: jsontext.Value(id), Method: method, Params: data}); err != nil {
		a.mu.Lock()
		delete(a.pending, id)
		a.mu.Unlock()
		return "", nil, err
	}
	return id, response, nil
}

func (a *ACP) awaitCall(ctx context.Context, id string, response <-chan acpEnvelope) (jsontext.Value, error) {
	defer func() { a.mu.Lock(); delete(a.pending, id); a.mu.Unlock() }()
	select {
	case result := <-response:
		if result.Error != nil {
			return nil, errors.New(result.Error.Message)
		}
		return result.Result, nil
	case <-a.done:
		return nil, errors.New("ACP agent disconnected")
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (a *ACP) call(ctx context.Context, method string, params any) (jsontext.Value, error) {
	id, response, err := a.startCall(method, params)
	if err != nil {
		return nil, err
	}
	return a.awaitCall(ctx, id, response)
}

func (a *ACP) read() {
	scanner := bufio.NewScanner(a.stdout)
	scanner.Buffer(make([]byte, 64<<10), 8<<20)
	for scanner.Scan() {
		var message acpEnvelope
		if err := json.Unmarshal(scanner.Bytes(), &message); err != nil {
			a.fail(fmt.Errorf("invalid ACP response: %w", err))
			break
		}
		if message.Method != "" {
			a.receive(message)
			continue
		}
		a.mu.Lock()
		response := a.pending[string(message.ID)]
		a.mu.Unlock()
		if response != nil {
			response <- message
		}
	}
	if err := scanner.Err(); err != nil {
		a.fail(err)
	}
	_ = a.stdout.Close()
	_ = killSessionProcess(a.cmd.Process)
	_ = a.cmd.Wait()
	a.mu.Lock()
	a.state.Connected = false
	a.state.Busy = false
	a.state.Permissions = nil
	a.changedLocked()
	a.mu.Unlock()
	close(a.done)
}

func (a *ACP) fail(err error) {
	a.mu.Lock()
	a.state.Error = err.Error()
	a.changedLocked()
	a.mu.Unlock()
}

func (a *ACP) changedLocked() {
	for ch := range a.subscribers {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

func (a *ACP) Snapshot() ([]byte, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return json.Marshal(a.state)
}

func (a *ACP) Subscribe() (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	a.mu.Lock()
	a.subscribers[ch] = struct{}{}
	ch <- struct{}{}
	a.mu.Unlock()
	return ch, func() { a.mu.Lock(); delete(a.subscribers, ch); a.mu.Unlock() }
}

func (a *ACP) receive(message acpEnvelope) {
	switch message.Method {
	case "session/update":
		var params struct {
			SessionID string `json:"sessionId"`
			Update    struct {
				SessionUpdate string            `json:"sessionUpdate"`
				ConfigOptions []ACPConfigOption `json:"configOptions"`
				Content       struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
				ToolCallID string `json:"toolCallId"`
				Title      string `json:"title"`
				Status     string `json:"status"`
			} `json:"update"`
		}
		if err := json.Unmarshal(message.Params, &params); err != nil {
			a.fail(err)
			return
		}
		a.mu.Lock()
		defer a.mu.Unlock()
		u := params.Update
		switch u.SessionUpdate {
		case "config_option_update":
			a.state.ConfigOptions = u.ConfigOptions
		case "agent_message_chunk":
			if u.Content.Type != "text" {
				return
			}
			last := len(a.state.Messages) - 1
			if last >= 0 && a.state.Messages[last].Role == "assistant" {
				a.state.Messages[last].Text += u.Content.Text
			} else {
				a.state.Messages = append(a.state.Messages, ACPMessage{Role: "assistant", Text: u.Content.Text, CreatedAt: time.Now().UTC().Format(time.RFC3339)})
			}
		case "tool_call", "tool_call_update":
			for i := range a.state.Messages {
				item := &a.state.Messages[i]
				if item.Role == "tool" && item.ToolCallID == u.ToolCallID {
					if u.Title != "" {
						item.Text = u.Title
					}
					if u.Status != "" {
						item.Status = u.Status
					}
					a.changedLocked()
					return
				}
			}
			a.state.Messages = append(a.state.Messages, ACPMessage{Role: "tool", Text: u.Title, ToolCallID: u.ToolCallID, Status: u.Status, CreatedAt: time.Now().UTC().Format(time.RFC3339)})
		}
		a.changedLocked()
	case "session/request_permission":
		var params struct {
			ToolCall struct {
				Title string `json:"title"`
			} `json:"toolCall"`
			Options []ACPPermissionOption `json:"options"`
		}
		if err := json.Unmarshal(message.Params, &params); err != nil {
			a.fail(err)
			return
		}
		a.mu.Lock()
		if a.cancelling {
			a.mu.Unlock()
			_ = a.write(acpEnvelope{ID: message.ID, Result: jsontext.Value(`{"outcome":{"outcome":"cancelled"}}`)})
			return
		}
		a.state.Permissions = append(a.state.Permissions, ACPPermission{ID: string(message.ID), Title: params.ToolCall.Title, Options: params.Options})
		a.changedLocked()
		a.mu.Unlock()
	default:
		if len(message.ID) > 0 {
			_ = a.write(acpEnvelope{ID: message.ID, Error: &acpError{Code: -32601, Message: "Client method not supported"}})
		}
	}
}

func (a *ACP) Command(command ACPCommand) error {
	switch command.Type {
	case "config":
		return a.configure(command.ID, command.Value)
	case "prompt":
		return a.prompt(command.Text, command.ID)
	case "cancel":
		a.turnMu.Lock()
		defer a.turnMu.Unlock()
		a.mu.Lock()
		a.cancelling = true
		a.mu.Unlock()
		params, err := json.Marshal(map[string]string{"sessionId": a.sessionID})
		if err != nil {
			return err
		}
		a.mu.Lock()
		permissions := a.state.Permissions
		a.state.Permissions = nil
		a.changedLocked()
		a.mu.Unlock()
		for _, permission := range permissions {
			if err := a.write(acpEnvelope{ID: jsontext.Value(permission.ID), Result: jsontext.Value(`{"outcome":{"outcome":"cancelled"}}`)}); err != nil {
				return err
			}
		}
		return a.write(acpEnvelope{Method: "session/cancel", Params: params})
	case "permission":
		a.mu.Lock()
		index := -1
		for i, permission := range a.state.Permissions {
			if permission.ID == command.ID {
				for _, option := range permission.Options {
					if option.OptionID == command.OptionID {
						index = i
					}
				}
			}
		}
		if index < 0 {
			a.mu.Unlock()
			return errors.New("permission option is no longer pending")
		}
		a.state.Permissions = append(a.state.Permissions[:index], a.state.Permissions[index+1:]...)
		a.changedLocked()
		a.mu.Unlock()
		result, err := json.Marshal(map[string]any{"outcome": map[string]string{"outcome": "selected", "optionId": command.OptionID}})
		if err != nil {
			return err
		}
		return a.write(acpEnvelope{ID: jsontext.Value(command.ID), Result: result})
	default:
		return errors.New("unknown ACP command")
	}
}
func (a *ACP) Prompt(text string) error { return a.prompt(text, "") }
func (a *ACP) prompt(text, submissionID string) error {
	a.turnMu.Lock()
	defer a.turnMu.Unlock()
	if strings.TrimSpace(text) == "" || len(text) > 64<<10 {
		return errors.New("message must contain between 1 and 65536 bytes")
	}
	a.mu.Lock()
	if submissionID != "" {
		for _, message := range a.state.Messages {
			if message.SubmissionID == submissionID {
				a.mu.Unlock()
				if message.Text != text {
					return errors.New("submission ID already belongs to another message")
				}
				return nil
			}
		}
	}
	if !a.state.Connected || a.state.Busy || a.state.Configuring {
		a.mu.Unlock()
		return errors.New("ACP agent is disconnected or busy")
	}
	a.state.Busy = true
	a.cancelling = false
	a.state.Error = ""
	a.state.Messages = append(a.state.Messages, ACPMessage{Role: "user", Text: text, SubmissionID: submissionID, CreatedAt: time.Now().UTC().Format(time.RFC3339)})
	a.changedLocked()
	a.mu.Unlock()
	id, response, err := a.startCall("session/prompt", map[string]any{"sessionId": a.sessionID, "prompt": []map[string]string{{"type": "text", "text": text}}})
	if err != nil {
		a.mu.Lock()
		a.state.Busy = false
		a.state.Error = err.Error()
		a.changedLocked()
		a.mu.Unlock()
		return err
	}
	go func() {
		_, err := a.awaitCall(context.Background(), id, response)
		a.mu.Lock()
		defer a.mu.Unlock()
		a.state.Busy = false
		if err != nil {
			a.state.Error = err.Error()
		}
		a.changedLocked()
	}()
	return nil
}

func (m *Manager) ACP(workspaceID, key string) (*ACP, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.sessions[key]
	if s == nil || s.info.WorkspaceID != workspaceID || s.acp == nil {
		return nil, ErrSessionNotFound
	}
	return s.acp, nil
}
