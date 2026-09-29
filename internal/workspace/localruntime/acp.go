package localruntime

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	acpsdk "github.com/coder/acp-go-sdk"

	"go.kenn.io/forge/internal/procutil"
)

// ACPChat is the daemon attachment to an execution-host-owned conversation.
type ACPChat interface {
	Snapshot() ([]byte, error)
	Subscribe() (<-chan struct{}, func())
	Command(ACPCommand) error
	Prompt(string) error
	Stop(context.Context) error
	Detach()
	Done() <-chan struct{}
	ExitCode() int
}

// ACP owns the SDK connection inside the durable ACP owner process.
type ACP struct {
	turnMu         sync.Mutex
	cancelling     bool
	mu             sync.Mutex
	cmd            *exec.Cmd
	stdin          io.WriteCloser
	stdout         io.ReadCloser
	done           chan struct{}
	client         *acpsdk.ClientSideConnection
	promptWritten  chan error
	nextPermission int
	permissions    map[string]chan acpsdk.RequestPermissionOutcome
	elicitations   map[string]chan acpsdk.UnstableCreateElicitationResponse
	subscribers    map[chan struct{}]struct{}
	state          ACPState
	saveConfig     func(map[string]string) error
	sessionID      string
	exitCode       int
	promptIndex    *int
	recordPath     string
	revision       uint64
	// turnCompleted records a finished prompt turn in this process. A loaded
	// session starts idle so reopening it never announces a new completion.
	turnCompleted bool
}

const maxACPStateBytes = 4 << 20

// ErrACPAgentUnavailable rejects a prompt before anything is written to a
// disconnected agent. The owner RPC carries only its text, so attachments
// restore the sentinel. A running turn is never a reason to reject input.
var ErrACPAgentUnavailable = errors.New("ACP agent is disconnected")

// errACPNotIdle stops a turn from starting over another turn, a steering
// request, or a settings change; callers queue the prompt instead.
var errACPNotIdle = errors.New("ACP agent is not idle")

type ACPMessage struct {
	SubmissionID string `json:"submissionId,omitempty"`
	Role         string `json:"role"`
	Text         string `json:"text"`
	CreatedAt    string `json:"createdAt"`
	ToolCallID   string `json:"toolCallId,omitempty"`
	Status       string `json:"status,omitempty"`
	// Subagent marks a tool call that runs a delegated agent. ParentToolCallID
	// links a tool call made inside such a subagent back to it.
	Subagent         bool   `json:"subagent,omitempty"`
	ParentToolCallID string `json:"parentToolCallId,omitempty"`
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

// ACPElicitation is a pending form-mode elicitation. Its schema is the
// restricted primitive-property JSON Schema the agent sent.
type ACPElicitation struct {
	ID      string               `json:"id"`
	Message string               `json:"message"`
	Schema  ACPElicitationSchema `json:"schema"`
}
type ACPElicitationSchema struct {
	Title       string         `json:"title,omitempty"`
	Description string         `json:"description,omitempty"`
	Properties  map[string]any `json:"properties"`
	Required    []string       `json:"required,omitempty"`
}

// ACPCommandInfo is one slash command the agent currently advertises. Users
// invoke it by sending "/name" followed by any input as a prompt.
type ACPCommandInfo struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	InputHint   string `json:"inputHint,omitempty"`
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
	ConfigOptions    []ACPConfigOption `json:"configOptions"`
	Commands         []ACPCommandInfo  `json:"commands"`
	Configuring      bool              `json:"configuring"`
	Messages         []ACPMessage      `json:"messages"`
	Permissions      []ACPPermission   `json:"permissions"`
	Elicitations     []ACPElicitation  `json:"elicitations"`
	HistoryTruncated bool              `json:"historyTruncated"`
	Busy             bool              `json:"busy"`
	Connected        bool              `json:"connected"`
	Error            string            `json:"error"`
	// Queue holds prompts waiting for the running turn to end. It drains one
	// prompt per completed turn and pauses when a turn does not end normally.
	Queue       []ACPQueuedPrompt `json:"queue"`
	QueuePaused bool              `json:"queuePaused"`
	// SteeringSupported reports the agent's steering extension. Steering is
	// true while a steering request is in flight.
	SteeringSupported bool `json:"steeringSupported"`
	Steering          bool `json:"steering"`
}
type ACPQueuedPrompt struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}
type ACPCommand struct {
	Type string `json:"type"`
	// Mode chooses how a prompt is submitted: send (the default), queue, or
	// steer. A send while a turn is running queues instead of failing.
	Mode     string `json:"mode,omitempty"`
	Text     string `json:"text,omitempty"`
	ID       string `json:"id,omitempty"`
	OptionID string `json:"optionId,omitempty"`
	Value    string `json:"value,omitempty"`
	// Action and Content answer an elicitation. Content is the accepted form
	// values as a JSON object; it stays raw so the owner RPC can carry it.
	Action  string         `json:"action,omitempty"`
	Content jsontext.Value `json:"content,omitempty"`
}

func startACPSession(ctx context.Context, command []string, cwd string, extraStrip []string, mcpServers []acpsdk.McpServer, saved *acpSavedSession) (*ACP, error) {
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
	a := &ACP{cmd: cmd, stdin: stdin, stdout: stdout, done: make(chan struct{}), permissions: make(map[string]chan acpsdk.RequestPermissionOutcome), elicitations: make(map[string]chan acpsdk.UnstableCreateElicitationResponse), subscribers: make(map[chan struct{}]struct{}), exitCode: -1}
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return nil, err
	}
	a.client = acpsdk.NewClientSideConnection(a, a, stdout)
	go a.wait()
	initCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	initialized, err := a.client.Initialize(initCtx, acpsdk.InitializeRequest{
		ProtocolVersion: acpsdk.ProtocolVersionNumber,
		ClientInfo:      &acpsdk.Implementation{Name: "kenn-forge", Version: "1"},
		// Forge renders form elicitations in chat. URL mode is not offered.
		ClientCapabilities: acpsdk.ClientCapabilities{Elicitation: &acpsdk.ElicitationCapabilities{Form: &acpsdk.ElicitationFormCapabilities{}}},
	})
	if err == nil && initialized.ProtocolVersion != acpsdk.ProtocolVersionNumber {
		err = fmt.Errorf("unsupported ACP protocol version %d (expected %d)", initialized.ProtocolVersion, acpsdk.ProtocolVersionNumber)
	}
	if err == nil && len(mcpServers) > 0 && !initialized.AgentCapabilities.McpCapabilities.Http {
		err = errors.New("this ACP agent does not accept HTTP MCP servers required by Forge")
	}
	if err == nil {
		steering, _ := initialized.Meta["steering"].(map[string]any)
		a.state.SteeringSupported = steering["supported"] == true
		if saved == nil {
			var created acpsdk.NewSessionResponse
			created, err = a.client.NewSession(initCtx, acpsdk.NewSessionRequest{Cwd: cwd, McpServers: mcpServers})
			if err == nil && created.SessionId == "" {
				err = errors.New("ACP agent returned no session ID")
			}
			a.sessionID = string(created.SessionId)
			a.mu.Lock()
			a.state.ConfigOptions = acpConfigOptions(created.ConfigOptions)
			a.mu.Unlock()
		} else {
			a.sessionID = saved.SessionID
			var loaded acpsdk.LoadSessionResponse
			loaded, err = a.client.LoadSession(initCtx, acpsdk.LoadSessionRequest{Cwd: cwd, McpServers: mcpServers, SessionId: acpsdk.SessionId(saved.SessionID)})
			if err == nil {
				a.mu.Lock()
				a.state.ConfigOptions = acpConfigOptions(loaded.ConfigOptions)
				a.restoreSubmissionIDsLocked(saved.State.Messages)
				// A reloaded conversation never starts queued work on its own.
				a.state.Queue = saved.State.Queue
				a.state.QueuePaused = len(a.state.Queue) > 0
				a.mu.Unlock()
			}
		}
	}
	if err != nil {
		if initCtx.Err() != nil {
			err = initCtx.Err()
		}
		_ = a.Stop(context.Background())
		return nil, fmt.Errorf("initialize ACP agent: %w", err)
	}
	a.mu.Lock()
	a.state.Connected = true
	a.changedLocked()
	a.mu.Unlock()
	return a, nil
}

func (a *ACP) Done() <-chan struct{} { return a.done }
func (a *ACP) ExitCode() int         { a.mu.Lock(); defer a.mu.Unlock(); return a.exitCode }

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
	agent, err := startACPSession(ctx, command, cwd, m.currentStripEnvVars(), m.agentMCPServers(), nil)
	if err != nil {
		return err
	}
	return agent.Stop(context.Background())
}

func (a *ACP) Stop(ctx context.Context) error {
	_ = a.stdin.Close()
	err := killSessionProcess(a.cmd.Process)
	_ = a.stdout.Close()
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

// Write observes the SDK's outgoing prompt write so reconnect acknowledgements
// are recorded only after the executable received the request. The SDK owns
// framing, IDs, dispatch, and all inbound protocol decoding.
func (a *ACP) Write(data []byte) (int, error) {
	n, err := a.stdin.Write(data)
	if err == nil && n != len(data) {
		err = io.ErrShortWrite
	}
	var request acpsdk.ClientRequest
	if json.Unmarshal(data, &request) == nil && request.Method == acpsdk.AgentMethodSessionPrompt {
		a.mu.Lock()
		if a.promptWritten != nil {
			a.promptWritten <- err
			a.promptWritten = nil
		}
		a.mu.Unlock()
	}
	return n, err
}

func (a *ACP) wait() {
	<-a.client.Done()
	_ = a.stdout.Close()
	_ = killSessionProcess(a.cmd.Process)
	exitCode := waitExitCode(a.cmd.Wait())
	a.mu.Lock()
	a.exitCode = exitCode
	a.state.Connected = false
	a.state.Busy = false
	a.state.Permissions = nil
	a.state.Elicitations = nil
	a.state.Steering = false
	a.state.QueuePaused = len(a.state.Queue) > 0
	a.changedLocked()
	a.mu.Unlock()
	close(a.done)
}

func (a *ACP) changedLocked() {
	a.revision++
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

func (a *ACP) Command(command ACPCommand) error {
	switch command.Type {
	case "config":
		return a.configure(command.ID, command.Value)
	case "prompt":
		return a.submit(command)
	case "unqueue":
		return a.unqueue(command.ID)
	case "resume":
		return a.resumeQueue()
	case "cancel":
		a.turnMu.Lock()
		defer a.turnMu.Unlock()
		a.mu.Lock()
		a.cancelling = true
		a.state.QueuePaused = len(a.state.Queue) > 0
		for id, response := range a.permissions {
			response <- acpsdk.NewRequestPermissionOutcomeCancelled()
			delete(a.permissions, id)
		}
		a.state.Permissions = nil
		for id, response := range a.elicitations {
			response <- acpsdk.NewUnstableCreateElicitationResponseCancel()
			delete(a.elicitations, id)
		}
		a.state.Elicitations = nil
		a.changedLocked()
		a.mu.Unlock()
		return a.client.Cancel(context.Background(), acpsdk.CancelNotification{SessionId: acpsdk.SessionId(a.sessionID)})
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
		response := a.permissions[command.ID]
		delete(a.permissions, command.ID)
		a.state.Permissions = slices.Delete(a.state.Permissions, index, index+1)
		response <- acpsdk.NewRequestPermissionOutcomeSelected(acpsdk.PermissionOptionId(command.OptionID))
		a.changedLocked()
		a.mu.Unlock()
		return nil
	case "elicitation":
		return a.answerElicitation(command)
	default:
		return errors.New("unknown ACP command")
	}
}

// Prompt submits text as a send: it starts a turn when idle and queues behind
// a running one.
func (a *ACP) Prompt(text string) error { return a.submit(ACPCommand{Type: "prompt", Text: text}) }

// startPromptLocked starts a turn. The caller holds turnMu. A queued prompt
// leaves the queue in the same persisted update that records it as sent.
func (a *ACP) startPromptLocked(text, submissionID string) error {
	a.mu.Lock()
	if !a.state.Connected {
		a.mu.Unlock()
		return ErrACPAgentUnavailable
	}
	if a.state.Busy || a.state.Configuring || a.state.Steering {
		a.mu.Unlock()
		return errACPNotIdle
	}
	a.state.Busy = true
	a.cancelling = false
	a.state.Error = ""
	a.promptIndex = new(len(a.state.Messages))
	written := make(chan error, 1)
	a.promptWritten = written
	a.mu.Unlock()
	completed := make(chan acpTurnResult, 1)
	go func() {
		response, err := a.client.Prompt(context.Background(), acpsdk.PromptRequest{
			SessionId: acpsdk.SessionId(a.sessionID), Prompt: []acpsdk.ContentBlock{acpsdk.TextBlock(text)},
		})
		completed <- acpTurnResult{stopReason: response.StopReason, err: err}
	}()
	var err error
	select {
	case err = <-written:
	case result := <-completed:
		err = result.err
		// A disconnected SDK can reject a request without attempting a write.
		select {
		case writeErr := <-written:
			completed <- result
			err = writeErr
		default:
		}
	}
	if err != nil {
		a.mu.Lock()
		a.state.Busy = false
		a.promptIndex = nil
		a.promptWritten = nil
		a.state.Error = err.Error()
		a.changedLocked()
		a.mu.Unlock()
		return err
	}
	a.mu.Lock()
	message := ACPMessage{Role: "user", Text: text, SubmissionID: submissionID, CreatedAt: time.Now().UTC().Format(time.RFC3339)}
	messageIndex := *a.promptIndex
	a.promptIndex = nil
	a.state.Messages = append(a.state.Messages, ACPMessage{})
	copy(a.state.Messages[messageIndex+1:], a.state.Messages[messageIndex:])
	a.state.Messages[messageIndex] = message
	if submissionID != "" {
		a.state.Queue = slices.DeleteFunc(a.state.Queue, func(queued ACPQueuedPrompt) bool { return queued.ID == submissionID })
	}
	a.trimStateLocked()
	persistErr := a.persistLocked()
	a.changedLocked()
	a.mu.Unlock()
	go a.finishTurn(completed)
	return persistErr
}

// Include wire overhead and escaping so even many tiny tool updates or control
// characters stay within the retained history budget.
func acpMessageBytes(message ACPMessage) int {
	data, _ := json.Marshal(message)
	return len(data) + 1
}

func acpPermissionBytes(permission ACPPermission) int {
	data, _ := json.Marshal(permission)
	return len(data) + 1
}

func acpElicitationBytes(elicitation ACPElicitation) int {
	data, _ := json.Marshal(elicitation)
	return len(data) + 1
}

func (a *ACP) retainedStateBytesLocked() int {
	size := 0
	for _, message := range a.state.Messages {
		size += acpMessageBytes(message)
	}
	for _, permission := range a.state.Permissions {
		size += acpPermissionBytes(permission)
	}
	for _, elicitation := range a.state.Elicitations {
		size += acpElicitationBytes(elicitation)
	}
	for _, queued := range a.state.Queue {
		size += acpQueuedBytes(queued)
	}
	return size
}

func (a *ACP) trimStateLocked() {
	a.trimStateToBytesLocked(maxACPStateBytes)
}

func (a *ACP) trimStateToBytesLocked(limit int) {
	size := a.retainedStateBytesLocked()
	// Keep the latest accepted prompt so reconnects can acknowledge/deduplicate
	// it even when the agent produces more output than the history budget.
	latestUser := -1
	for i, message := range slices.Backward(a.state.Messages) {
		if message.Role == "user" {
			latestUser = i
			break
		}
	}
	last := len(a.state.Messages) - 1
	index := -1
	removedBeforePrompt := 0
	a.state.Messages = slices.DeleteFunc(a.state.Messages, func(message ACPMessage) bool {
		index++
		if size <= limit || index == latestUser || index == last {
			return false
		}
		size -= acpMessageBytes(message)
		if a.promptIndex != nil && index < *a.promptIndex {
			removedBeforePrompt++
		}
		a.state.HistoryTruncated = true
		return true
	})
	if a.promptIndex != nil {
		*a.promptIndex -= removedBeforePrompt
	}
	for size > limit && len(a.state.Messages) > 0 {
		last = len(a.state.Messages) - 1
		message := &a.state.Messages[last]
		if message.Role == "user" {
			break
		}
		messageBytes := acpMessageBytes(*message)
		if len(message.Text) > 0 {
			// Removing this many UTF-8 bytes removes at least as many JSON bytes.
			// Cap each cut at half the remaining text: JSON escaping can
			// make the wire overflow larger than the entire raw string.
			start := min(size-limit, max(len(message.Text)/2, 1))
			for start < len(message.Text) && !utf8.RuneStart(message.Text[start]) {
				start++
			}
			message.Text = strings.Clone(message.Text[start:])
			size += acpMessageBytes(*message) - messageBytes
		} else {
			size -= messageBytes
			a.state.Messages = slices.Delete(a.state.Messages, last, last+1)
			if a.promptIndex != nil {
				*a.promptIndex = min(*a.promptIndex, len(a.state.Messages))
			}
		}
		a.state.HistoryTruncated = true
	}
}

func (m *Manager) ACP(workspaceID, key string) (ACPChat, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.sessions[key]
	if s == nil || s.info.WorkspaceID != workspaceID || s.acp == nil {
		return nil, ErrSessionNotFound
	}
	return s.acp, nil
}
