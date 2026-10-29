package localruntime

import (
	"bufio"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	acpsdk "github.com/coder/acp-go-sdk"

	"go.kenn.io/forge/internal/procutil"
	"go.kenn.io/kit/atomicfile"
)

// A shared agent host runs one agent process for many chats. ACP keeps the
// slow part of a start, initialize, per process, while each chat's
// session/new or session/load carries its own cwd and Forge MCP endpoint, and
// every request and update names its session. Each chat keeps its own owner,
// which connects to the host instead of starting an agent; the host answers
// initialize from its own and routes every other message by session.
//
// Only agents tested to keep concurrent sessions apart share a process; any
// other agent's first host serves that chat alone and later chats start their
// own agent as before. A host that a newer one replaced keeps serving its
// chats and exits when the last one leaves.

// acpSharedAgents names agents that honor each session's cwd and keep
// concurrent turns, cancels and closes to their own session.
var acpSharedAgents = []string{"@agentclientprotocol/claude-agent-acp", "@agentclientprotocol/codex-acp", "omp"}

// acpHostIdleTimeout keeps a host with no chats warm for the next one.
const acpHostIdleTimeout = 10 * time.Minute

// acpHostConfig is the host process's configuration file.
type acpHostConfig struct {
	Command []string
	Strip   []string
	// Dir holds the host's state and is the agent's working directory; no
	// chat's worktree is, since each session names its own.
	Dir    string
	Socket string
	// Pointer is the record naming the current host for this agent; a host
	// it no longer names exits once its last chat leaves.
	Pointer           string
	Generation        string
	IdleTimeoutMillis int64
}

// acpHostReady is written once the agent is initialized, before the host
// listens: an owner that can dial the socket can read it.
type acpHostReady struct {
	Shareable   bool
	Fingerprint acpHostFingerprint
}

const acpHostReadyFile = "ready.json"

// acpHostMessage is the routing view of one JSON-RPC message.
type acpHostMessage struct {
	ID        jsontext.Value
	Method    string
	SessionID string // from params, or from the result of session/new
}

func parseACPHostMessage(line []byte) (acpHostMessage, bool) {
	var raw struct {
		ID     jsontext.Value `json:"id"`
		Method string         `json:"method"`
		Params jsontext.Value `json:"params"`
		Result jsontext.Value `json:"result"`
	}
	if json.Unmarshal(line, &raw) != nil {
		return acpHostMessage{}, false
	}
	var session struct {
		SessionID string `json:"sessionId"`
	}
	if raw.Method != "" {
		_ = json.Unmarshal(raw.Params, &session)
	} else {
		_ = json.Unmarshal(raw.Result, &session)
	}
	return acpHostMessage{ID: raw.ID, Method: raw.Method, SessionID: session.SessionID}, true
}

type acpHostOwner struct {
	conn     net.Conn
	writeMu  sync.Mutex
	sessions map[string]struct{}
}

func (o *acpHostOwner) send(line []byte) {
	o.writeMu.Lock()
	defer o.writeMu.Unlock()
	if _, err := o.conn.Write(line); err != nil {
		_ = o.conn.Close()
	}
}

type acpHostRequest struct {
	owner  *acpHostOwner // nil for the host's own requests
	id     jsontext.Value
	method string
}

type acpHost struct {
	cfg       acpHostConfig
	agentIn   io.Writer
	agentMu   sync.Mutex
	init      []byte // the agent's initialize result
	shareable bool

	mu       sync.Mutex
	owners   map[*acpHostOwner]struct{}
	sessions map[string]*acpHostOwner
	pending  map[string]acpHostRequest
	// agentRequests maps the agent's own request IDs to the owner asked.
	agentRequests map[string]*acpHostOwner
	nextID        int
	// binding is the owner whose session/new or session/load is in flight;
	// bind serializes them, so an update for a session not yet known goes
	// to it.
	binding *acpHostOwner
	bind    chan struct{}
	served  bool
	idle    chan struct{}
}

// RunACPHost is the hidden executable entry point for a shared agent host.
func RunACPHost(ctx context.Context, configPath string) (err error) {
	data, err := os.ReadFile(configPath)
	if err != nil {
		return err
	}
	var cfg acpHostConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return err
	}
	listening := false
	defer func() {
		if err != nil && !listening {
			err = errors.Join(err, recordACPStartError(cfg.Dir, err))
		}
	}()
	executable, err := resolveExecutable(cfg.Command[0])
	if err != nil {
		return err
	}
	cmd := procutil.Command(executable, cfg.Command[1:]...)
	cmd.Dir = cfg.Dir
	cmd.Env = acpHostEnvironment(SessionEnvironment(os.Environ(), cfg.Strip), cfg.Dir)
	// Only the end of the agent's diagnostics is kept, to explain a failed
	// start; a host can run for days.
	stderr := &acpStderrTail{out: io.Discard}
	cmd.Stderr = stderr
	configureACPProcess(cmd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	defer func() { _ = killSessionProcess(cmd.Process); _ = cmd.Wait() }()
	host := &acpHost{cfg: cfg, agentIn: stdin, owners: make(map[*acpHostOwner]struct{}), sessions: make(map[string]*acpHostOwner), pending: make(map[string]acpHostRequest), agentRequests: make(map[string]*acpHostOwner), bind: make(chan struct{}, 1), idle: make(chan struct{}, 1)}
	agentOut := bufio.NewReader(stdout)
	initCtx, cancel := context.WithTimeout(ctx, acpHandshakeTimeout)
	defer cancel()
	initialized, err := host.initialize(initCtx, agentOut)
	if err != nil {
		if tail := stderr.String(); tail != "" {
			err = fmt.Errorf("%w; agent stderr: %s", err, tail)
		}
		return fmt.Errorf("initialize ACP agent: %w", err)
	}
	host.shareable = initialized.AgentInfo != nil && slices.Contains(acpSharedAgents, initialized.AgentInfo.Name) &&
		initialized.AgentCapabilities.LoadSession && initialized.AgentCapabilities.SessionCapabilities.Close != nil
	ready := acpHostReady{Shareable: host.shareable, Fingerprint: recordACPHostFingerprint(cfg.Command, cfg.Dir, cmd.Process.Pid)}
	// A Forge upgrade replaces the host too: hosts outlive the daemon.
	if executable, err := os.Executable(); err == nil {
		if stamp, ok := stampACPFile(executable); ok {
			ready.Fingerprint.Files = append(ready.Fingerprint.Files, stamp)
		}
	}
	data, err = json.Marshal(ready)
	if err != nil {
		return err
	}
	if err := atomicfile.WriteFile(filepath.Join(cfg.Dir, acpHostReadyFile), data, atomicfile.WithPerm(0o600)); err != nil && !errors.Is(err, atomicfile.ErrPublished) {
		return err
	}
	if err := os.Remove(cfg.Socket); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	listener, err := (&net.ListenConfig{}).Listen(ctx, "unix", cfg.Socket)
	if err != nil {
		return err
	}
	listening = true
	defer listener.Close()
	// A host that served its chats leaves nothing behind; a failed start
	// keeps its error for the owner waiting on it.
	defer func() {
		_ = os.RemoveAll(cfg.Dir)
		if socketDir := filepath.Dir(cfg.Socket); socketDir != filepath.Dir(cfg.Dir) {
			_ = os.RemoveAll(socketDir)
		}
	}()
	agentDone := make(chan struct{})
	go func() {
		defer close(agentDone)
		host.readAgent(agentOut)
	}()
	go host.accept(listener)
	idleTimeout := time.Duration(cfg.IdleTimeoutMillis) * time.Millisecond
	if idleTimeout <= 0 {
		idleTimeout = acpHostIdleTimeout
	}
	timer := time.NewTimer(idleTimeout)
	defer timer.Stop()
	check := time.NewTicker(acpOwnerReapInterval)
	defer check.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-check.C:
			// A host whose state was removed can never be reached again.
			if _, err := os.Stat(cfg.Dir); errors.Is(err, os.ErrNotExist) {
				return nil
			}
		case <-agentDone:
			return errors.New("ACP agent exited")
		case <-host.idle:
			if host.retired() {
				return nil
			}
			timer.Reset(idleTimeout)
		case <-timer.C:
			if host.ownerCount() == 0 {
				return nil
			}
		}
	}
}

// initialize sends Forge's initialize and returns the agent's answer, which
// the host gives every owner.
func (h *acpHost) initialize(ctx context.Context, agentOut *bufio.Reader) (acpsdk.InitializeResponse, error) {
	var response acpsdk.InitializeResponse
	request, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": "host-init", "method": acpsdk.AgentMethodInitialize, "params": acpInitializeRequest()})
	if err != nil {
		return response, err
	}
	if _, err := h.agentIn.Write(append(request, '\n')); err != nil {
		return response, err
	}
	lines := make(chan []byte)
	failed := make(chan error, 1)
	go func() {
		for {
			line, err := agentOut.ReadBytes('\n')
			if err != nil {
				failed <- err
				return
			}
			var message struct {
				ID     jsontext.Value `json:"id"`
				Result jsontext.Value `json:"result"`
				Error  jsontext.Value `json:"error"`
			}
			if json.Unmarshal(line, &message) == nil && string(message.ID) == `"host-init"` {
				lines <- line
				return
			}
		}
	}()
	select {
	case <-ctx.Done():
		return response, ctx.Err()
	case err := <-failed:
		return response, err
	case line := <-lines:
		var message struct {
			Result jsontext.Value `json:"result"`
			Error  jsontext.Value `json:"error"`
		}
		if err := json.Unmarshal(line, &message); err != nil {
			return response, err
		}
		if len(message.Error) > 0 {
			return response, fmt.Errorf("agent refused initialize: %s", message.Error)
		}
		h.init = message.Result
		return response, json.Unmarshal(message.Result, &response)
	}
}

func (h *acpHost) accept(listener net.Listener) {
	for {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		owner := &acpHostOwner{conn: conn, sessions: make(map[string]struct{})}
		h.mu.Lock()
		// An agent that cannot share serves only the chat that started it.
		refuse := !h.shareable && h.served
		if !refuse {
			h.owners[owner] = struct{}{}
			h.served = true
		}
		h.mu.Unlock()
		if refuse {
			_ = conn.Close()
			continue
		}
		go h.serve(owner)
	}
}

// serve forwards one owner's messages to the agent until it disconnects,
// then cancels and closes its sessions so the agent frees them.
func (h *acpHost) serve(owner *acpHostOwner) {
	defer h.disconnect(owner)
	reader := bufio.NewReader(owner.conn)
	for {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			return
		}
		message, ok := parseACPHostMessage(line)
		if !ok {
			continue
		}
		if message.Method == "" {
			// The owner answers a request the agent sent.
			h.mu.Lock()
			delete(h.agentRequests, string(message.ID))
			h.mu.Unlock()
			h.toAgent(line)
			continue
		}
		if len(message.ID) == 0 {
			h.toAgent(line)
			continue
		}
		if message.Method == acpsdk.AgentMethodInitialize {
			owner.send(acpHostLine(map[string]any{"jsonrpc": "2.0", "id": message.ID, "result": jsontext.Value(h.init)}))
			continue
		}
		binds := message.Method == acpsdk.AgentMethodSessionNew || message.Method == acpsdk.AgentMethodSessionLoad
		if binds {
			h.bind <- struct{}{}
		}
		h.mu.Lock()
		if binds {
			h.binding = owner
			if message.Method == acpsdk.AgentMethodSessionLoad {
				h.claimLocked(owner, message.SessionID)
			}
		}
		h.nextID++
		id := "h" + strconv.Itoa(h.nextID)
		h.pending[id] = acpHostRequest{owner: owner, id: message.ID, method: message.Method}
		h.mu.Unlock()
		forwarded, err := acpHostReplaceID(line, jsontext.Value(strconv.Quote(id)))
		if err != nil {
			continue
		}
		h.toAgent(forwarded)
	}
}

func (h *acpHost) claimLocked(owner *acpHostOwner, sessionID string) {
	if sessionID == "" {
		return
	}
	h.sessions[sessionID] = owner
	owner.sessions[sessionID] = struct{}{}
}

func (h *acpHost) disconnect(owner *acpHostOwner) {
	_ = owner.conn.Close()
	h.mu.Lock()
	delete(h.owners, owner)
	if h.binding == owner {
		h.binding = nil
		select {
		case <-h.bind:
		default:
		}
	}
	var sessions []string
	for sessionID := range owner.sessions {
		delete(h.sessions, sessionID)
		sessions = append(sessions, sessionID)
	}
	var unanswered []string
	for id, asked := range h.agentRequests {
		if asked == owner {
			delete(h.agentRequests, id)
			unanswered = append(unanswered, id)
		}
	}
	for id, request := range h.pending {
		if request.owner == owner {
			h.pending[id] = acpHostRequest{method: request.method}
		}
	}
	h.mu.Unlock()
	for _, id := range unanswered {
		h.toAgent(acpHostLine(map[string]any{"jsonrpc": "2.0", "id": jsontext.Value(id), "error": map[string]any{"code": -32603, "message": "chat closed"}}))
	}
	for _, sessionID := range sessions {
		h.toAgent(acpHostLine(map[string]any{"jsonrpc": "2.0", "method": acpsdk.AgentMethodSessionCancel, "params": map[string]any{"sessionId": sessionID}}))
		h.mu.Lock()
		h.nextID++
		id := "h" + strconv.Itoa(h.nextID)
		h.pending[id] = acpHostRequest{method: acpsdk.AgentMethodSessionClose}
		h.mu.Unlock()
		h.toAgent(acpHostLine(map[string]any{"jsonrpc": "2.0", "id": id, "method": acpsdk.AgentMethodSessionClose, "params": map[string]any{"sessionId": sessionID}}))
	}
	select {
	case h.idle <- struct{}{}:
	default:
	}
}

// readAgent routes the agent's messages: answers to the owner that asked,
// and requests and updates to the owner of their session.
func (h *acpHost) readAgent(agentOut *bufio.Reader) {
	defer func() {
		h.mu.Lock()
		owners := make([]*acpHostOwner, 0, len(h.owners))
		for owner := range h.owners {
			owners = append(owners, owner)
		}
		h.mu.Unlock()
		for _, owner := range owners {
			_ = owner.conn.Close()
		}
	}()
	for {
		line, err := agentOut.ReadBytes('\n')
		if err != nil {
			return
		}
		message, ok := parseACPHostMessage(line)
		if !ok {
			continue
		}
		if message.Method == "" {
			h.answer(line, message)
			continue
		}
		h.mu.Lock()
		owner := h.sessions[message.SessionID]
		if owner == nil {
			owner = h.binding
		}
		if owner != nil && len(message.ID) > 0 {
			h.agentRequests[string(message.ID)] = owner
		}
		h.mu.Unlock()
		switch {
		case owner != nil:
			owner.send(line)
		case len(message.ID) > 0:
			h.toAgent(acpHostLine(map[string]any{"jsonrpc": "2.0", "id": message.ID, "error": map[string]any{"code": -32603, "message": "no chat owns this session"}}))
		}
	}
}

func (h *acpHost) answer(line []byte, message acpHostMessage) {
	var id string
	if json.Unmarshal(message.ID, &id) != nil {
		return
	}
	h.mu.Lock()
	request, ok := h.pending[id]
	delete(h.pending, id)
	binds := request.method == acpsdk.AgentMethodSessionNew || request.method == acpsdk.AgentMethodSessionLoad
	if ok && binds && request.owner != nil {
		if request.method == acpsdk.AgentMethodSessionNew {
			h.claimLocked(request.owner, message.SessionID)
		}
		if h.binding == request.owner {
			h.binding = nil
			select {
			case <-h.bind:
			default:
			}
		}
	}
	h.mu.Unlock()
	if !ok || request.owner == nil {
		return
	}
	reply, err := acpHostReplaceID(line, request.id)
	if err == nil {
		request.owner.send(reply)
	}
}

func (h *acpHost) toAgent(line []byte) {
	h.agentMu.Lock()
	defer h.agentMu.Unlock()
	if _, err := h.agentIn.Write(line); err != nil {
		slog.Warn("write to shared ACP agent", "err", err)
	}
}

func (h *acpHost) ownerCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.owners)
}

// retired reports, once the host has no chats, whether it should exit now:
// it served an agent that cannot share, or a newer host replaced it.
func (h *acpHost) retired() bool {
	h.mu.Lock()
	empty, exclusive := len(h.owners) == 0, !h.shareable && h.served
	h.mu.Unlock()
	if !empty {
		return false
	}
	if exclusive {
		return true
	}
	pointer, err := readACPHostPointer(h.cfg.Pointer)
	return err != nil || pointer.Generation != h.cfg.Generation
}

// acpHostEnvironment is the agent's environment on a host. The host inherits
// the environment of the chat owner that started it; the agent must not see
// that chat's worktree or terminal pane as its own.
func acpHostEnvironment(env []string, dir string) []string {
	env = slices.DeleteFunc(env, func(entry string) bool {
		name, _, _ := strings.Cut(entry, "=")
		return name == "PWD" || name == "OLDPWD" || name == "TMUX_PANE"
	})
	return append(env, "PWD="+dir)
}

func acpHostReplaceID(line []byte, id jsontext.Value) ([]byte, error) {
	var message map[string]jsontext.Value
	if err := json.Unmarshal(line, &message); err != nil {
		return nil, err
	}
	message["id"] = id
	return acpHostLine(message), nil
}

func acpHostLine(message any) []byte {
	data, err := json.Marshal(message)
	if err != nil {
		return nil
	}
	return append(data, '\n')
}
