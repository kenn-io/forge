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

	acpsdk "github.com/coder/acp-go-sdk"

	"go.kenn.io/forge/internal/procutil"
)

// ACPChat is the daemon attachment to an execution-host-owned conversation.
type ACPChat interface {
	Snapshot() ([]byte, error)
	// History returns earlier transcript messages [before-limit, before) as
	// a {"history": ...} client frame.
	History(before, limit int) ([]byte, error)
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
	turnMu          sync.Mutex
	cancelling      bool
	mu              sync.Mutex
	cmd             *exec.Cmd
	stdin           io.WriteCloser
	stdout          io.ReadCloser
	done            chan struct{}
	client          *acpsdk.ClientSideConnection
	promptWritten   chan error
	nextPermission  int
	permissions     map[string]chan acpsdk.RequestPermissionOutcome
	elicitations    map[string]chan acpsdk.UnstableCreateElicitationResponse
	subscribers     map[chan struct{}]struct{}
	state           ACPState
	imagesSupported bool
	saveConfig      func(map[string]string) error
	sessionID       string
	exitCode        int
	promptIndex     *int
	recordPath      string
	revision        uint64
	// turnCompleted records a finished prompt turn in this process. A loaded
	// session starts idle so reopening it never announces a new completion.
	turnCompleted bool
	// heldBytes is the unpublished suffix of the last assistant message; see
	// acp_delivery.go. heldSeq changes whenever a new message starts holding.
	heldBytes        int
	heldSeq          uint64
	lastTextDelivery time.Time
	textDelivery     *time.Timer
	// replaying drops the agent's history replay during session/load: the
	// saved transcript, not the replay, is the conversation of record.
	replaying bool
	// external is a turn the agent started itself after a steering request.
	// It ends when the agent reports an idle thread after an active one.
	external     *acpExternalTurn
	threadStatus string
	// reportsThreadStatus records that the agent reports Codex thread
	// status, the only signal for when a turn it started itself ends.
	reportsThreadStatus bool
	// takeoverPending records a turn the agent started after a steer that
	// no thread status has confirmed yet; the next active status claims it.
	takeoverPending bool
}

// acpExternalTurn is a turn the agent started after a steer. Until it reports
// active, an idle status may still belong to the original turn; once the
// original prompt has completed, any idle is this turn's, because the SDK
// handles every status the agent sent before a response before returning it.
type acpExternalTurn struct {
	active     bool
	promptDone bool
}

// ErrACPAgentUnavailable rejects a prompt before anything is written to a
// disconnected agent. The owner RPC carries only its text, so attachments
// restore the sentinel. A running turn is never a reason to reject input.
var ErrACPAgentUnavailable = errors.New("ACP agent is disconnected")

// errACPNotIdle stops a turn from starting over another turn, a steering
// request, or a settings change; callers queue the prompt instead.
var errACPNotIdle = errors.New("ACP agent is not idle")

type ACPMessage struct {
	SubmissionID string       `json:"submissionId,omitempty"`
	Role         string       `json:"role"`
	Text         string       `json:"text"`
	Images       []ACPContent `json:"images,omitempty"`
	CreatedAt    string       `json:"createdAt"`
	ToolCallID   string       `json:"toolCallId,omitempty"`
	Status       string       `json:"status,omitempty"`
	// Subagent marks a tool call that runs a delegated agent. ParentToolCallID
	// links a tool call made inside such a subagent back to it.
	Subagent         bool   `json:"subagent,omitempty"`
	ParentToolCallID string `json:"parentToolCallId,omitempty"`
	// MessageID is the agent's message identity; a new ID starts a new message.
	MessageID string `json:"messageId,omitempty"`
	// Content is a non-text block the agent sent, shown as its own entry.
	Content *ACPContent `json:"content,omitempty"`
	// Tool call details.
	Kind        string            `json:"kind,omitempty"`
	ToolContent []ACPToolContent  `json:"toolContent,omitempty"`
	Locations   []ACPToolLocation `json:"locations,omitempty"`
	RawInput    string            `json:"rawInput,omitempty"`
	RawOutput   string            `json:"rawOutput,omitempty"`
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
	ConfigOptions []ACPConfigOption `json:"configOptions"`
	Commands      []ACPCommandInfo  `json:"commands"`
	// Plan is the agent's current plan; each update replaces it.
	Plan        []ACPPlanEntry `json:"plan"`
	Configuring bool           `json:"configuring"`
	Messages    []ACPMessage   `json:"messages"`
	// MessageOffset is the transcript index of Messages[0] in a published
	// update; MessageCount is the length of the whole transcript.
	MessageOffset int              `json:"messageOffset"`
	MessageCount  int              `json:"messageCount"`
	Permissions   []ACPPermission  `json:"permissions"`
	Elicitations  []ACPElicitation `json:"elicitations"`
	Busy          bool             `json:"busy"`
	// Stopping is true from a stop request until the turn ends.
	Stopping  bool `json:"stopping"`
	Connected bool `json:"connected"`
	// Notices explain a degraded but usable session start. They are not
	// errors: the chat works, and they last for the life of the session.
	Notices []string `json:"notices,omitempty"`
	Error   string   `json:"error"`
	// ErrorCode and ErrorData keep an agent's JSON-RPC error details.
	ErrorCode *int   `json:"errorCode,omitempty"`
	ErrorData string `json:"errorData,omitempty"`
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
	ID     string       `json:"id"`
	Text   string       `json:"text"`
	Images []ACPContent `json:"images,omitempty"`
}
type ACPCommand struct {
	Type string `json:"type"`
	// Mode chooses how a prompt is submitted: send (the default), queue, or
	// steer. A send while a turn is running queues instead of failing.
	Mode     string       `json:"mode,omitempty"`
	Text     string       `json:"text,omitempty"`
	Images   []ACPContent `json:"images,omitempty"`
	ID       string       `json:"id,omitempty"`
	OptionID string       `json:"optionId,omitempty"`
	Value    string       `json:"value,omitempty"`
	// Before and Limit select a history page.
	Before int `json:"before,omitempty"`
	Limit  int `json:"limit,omitempty"`
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
		// Forge renders form elicitations in chat; URL mode is not offered.
		// Command output streams as terminal_output_delta tool call metadata.
		ClientCapabilities: acpsdk.ClientCapabilities{
			Elicitation: &acpsdk.ElicitationCapabilities{Form: &acpsdk.ElicitationFormCapabilities{}},
			Meta:        map[string]any{"terminal_output_delta": true},
		},
	})
	if err == nil && initialized.ProtocolVersion != acpsdk.ProtocolVersionNumber {
		err = fmt.Errorf("unsupported ACP protocol version %d (expected %d)", initialized.ProtocolVersion, acpsdk.ProtocolVersionNumber)
	}
	var notices []string
	if err == nil && len(mcpServers) > 0 && !initialized.AgentCapabilities.McpCapabilities.Http {
		// Forge tools are optional; the agent still works with its own tools.
		mcpServers = []acpsdk.McpServer{}
		notices = append(notices, acpNoHTTPMCPNotice)
	}
	if err == nil {
		steering, _ := initialized.Meta["steering"].(map[string]any)
		a.state.SteeringSupported = steering["supported"] == true
		a.imagesSupported = initialized.AgentCapabilities.PromptCapabilities.Image
		// An agent that cannot load sessions continues the saved conversation in
		// a new session rather than failing to start.
		if saved == nil || !initialized.AgentCapabilities.LoadSession {
			var created acpsdk.NewSessionResponse
			created, err = a.client.NewSession(initCtx, acpsdk.NewSessionRequest{Cwd: cwd, McpServers: mcpServers})
			if err == nil && created.SessionId == "" {
				err = errors.New("ACP agent returned no session ID")
			}
			a.mu.Lock()
			a.sessionID = string(created.SessionId)
			a.state.ConfigOptions = acpConfigOptions(created.ConfigOptions)
			if saved != nil {
				a.restoreTranscriptLocked(saved.State)
				notices = append(notices, "This agent cannot reload its previous session, so it continues in a new session without that context.")
			}
			a.mu.Unlock()
		} else {
			a.mu.Lock()
			a.sessionID = saved.SessionID
			a.replaying = true
			a.mu.Unlock()
			var loaded acpsdk.LoadSessionResponse
			loaded, err = a.client.LoadSession(initCtx, acpsdk.LoadSessionRequest{Cwd: cwd, McpServers: mcpServers, SessionId: acpsdk.SessionId(saved.SessionID)})
			a.mu.Lock()
			a.replaying = false
			if err == nil {
				a.state.ConfigOptions = acpConfigOptions(loaded.ConfigOptions)
				a.restoreTranscriptLocked(saved.State)
			}
			a.mu.Unlock()
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
	a.state.Notices = notices
	a.changedLocked()
	a.mu.Unlock()
	return a, nil
}

func (a *ACP) Done() <-chan struct{} { return a.done }
func (a *ACP) ExitCode() int         { a.mu.Lock(); defer a.mu.Unlock(); return a.exitCode }

const acpNoHTTPMCPNotice = "This agent does not accept HTTP MCP servers, so Forge tools are unavailable in its chats."

// TestACP checks the same handshake used by workspace launches without retaining
// a process or creating a workspace. The agent gets an empty temporary directory.
// A usable agent with reduced capabilities returns a warning and no error.
func (m *Manager) TestACP(ctx context.Context, command []string) (string, error) {
	if len(command) == 0 || strings.TrimSpace(command[0]) == "" {
		return "", errors.New("ACP executable is required")
	}
	cwd, err := os.MkdirTemp("", "forge-acp-test-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(cwd)
	agent, err := startACPSession(ctx, command, cwd, m.currentStripEnvVars(), m.agentMCPServers(), nil)
	if err != nil {
		return "", err
	}
	agent.mu.Lock()
	warning := strings.Join(agent.state.Notices, " ")
	agent.mu.Unlock()
	return warning, agent.Stop(context.Background())
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
	a.state.Stopping = false
	a.state.Permissions = nil
	a.state.Elicitations = nil
	a.state.Steering = false
	a.external = nil
	a.takeoverPending = false
	a.state.QueuePaused = len(a.state.Queue) > 0
	a.releaseHeldTextLocked()
	a.publishProgressLocked()
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
	return json.Marshal(a.publishedStateLocked(), json.Deterministic(true))
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
		// Stop never waits for a prompt write, steering request, or settings
		// change; a prompt written concurrently is cancelled after its write.
		a.mu.Lock()
		a.cancelling = true
		a.state.QueuePaused = true
		a.state.Stopping = a.state.Busy || a.state.Steering
		a.clearPendingLocked()
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
func (a *ACP) startPromptLocked(text, submissionID string, images []ACPContent) error {
	a.mu.Lock()
	if !a.state.Connected {
		a.mu.Unlock()
		return ErrACPAgentUnavailable
	}
	if a.state.Busy || a.state.Configuring || a.state.Steering {
		a.mu.Unlock()
		return errACPNotIdle
	}
	if len(images) > 0 && !a.imagesSupported {
		a.mu.Unlock()
		return errors.New("this agent does not accept image prompts")
	}
	a.state.Busy = true
	a.cancelling = false
	// Status from here on describes this prompt, not an earlier takeover.
	a.takeoverPending = false
	a.setErrorLocked(nil)
	a.promptIndex = new(len(a.state.Messages))
	written := make(chan error, 1)
	a.promptWritten = written
	a.mu.Unlock()
	completed := make(chan acpTurnResult, 1)
	go func() {
		response, err := a.client.Prompt(context.Background(), acpsdk.PromptRequest{
			SessionId: acpsdk.SessionId(a.sessionID), Prompt: acpPromptContent(text, images),
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
		a.setErrorLocked(err)
		a.changedLocked()
		a.mu.Unlock()
		return err
	}
	a.mu.Lock()
	if a.cancelling {
		// Stop arrived while the prompt was being written, so the agent may
		// have received that cancel before this turn existed.
		go func() {
			_ = a.client.Cancel(context.Background(), acpsdk.CancelNotification{SessionId: acpsdk.SessionId(a.sessionID)})
		}()
	}
	message := ACPMessage{Role: "user", Text: text, Images: images, SubmissionID: submissionID, CreatedAt: time.Now().UTC().Format(time.RFC3339)}
	messageIndex := *a.promptIndex
	a.promptIndex = nil
	a.state.Messages = append(a.state.Messages, ACPMessage{})
	copy(a.state.Messages[messageIndex+1:], a.state.Messages[messageIndex:])
	a.state.Messages[messageIndex] = message
	if submissionID != "" {
		a.state.Queue = slices.DeleteFunc(a.state.Queue, func(queued ACPQueuedPrompt) bool { return queued.ID == submissionID })
	}
	persistErr := a.persistLocked()
	a.changedLocked()
	a.mu.Unlock()
	go a.finishTurn(completed)
	return persistErr
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
