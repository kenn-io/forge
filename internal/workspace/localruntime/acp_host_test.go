//go:build linux

// The daemon shares agent hosts only on Linux; see Options.ShareACPAgents.

package localruntime

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	acpsdk "github.com/coder/acp-go-sdk"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/forge/internal/config"
)

// TestACPHostHelper runs a shared agent host for the tests below, which also
// share its fixture agent.
func TestACPHostHelper(t *testing.T) {
	if os.Getenv("KENN_FORGE_ACP_HOST_FIXTURE") != "1" {
		return
	}
	acpSharedAgents = append(acpSharedAgents, "forge-shared-fixture")
	if err := RunACPHost(context.Background(), os.Args[len(os.Args)-1]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}

// TestACPSharedAgentHelper is an agent that serves several sessions at once.
// Each prompt answers with its session's cwd; agent.log in its fixture
// directory records starts, sessions, closes and cancels.
func TestACPSharedAgentHelper(t *testing.T) {
	if os.Getenv("KENN_FORGE_ACP_SHARED_FIXTURE") != "1" {
		return
	}
	logPath := filepath.Join(os.Getenv("KENN_FORGE_ACP_CONTINUE_DIR"), "agent.log")
	record := func(format string, args ...any) {
		file, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			os.Exit(9)
		}
		_, _ = fmt.Fprintf(file, format+"\n", args...)
		_ = file.Close()
	}
	record("start %d", os.Getpid())
	send := func(message any) {
		data, err := json.Marshal(message)
		if err != nil {
			os.Exit(3)
		}
		_, _ = os.Stdout.Write(append(data, '\n'))
	}
	name := cmp.Or(os.Getenv("KENN_FORGE_ACP_SHARED_NAME"), "forge-shared-fixture")
	cwds := make(map[string]string)
	reader := bufio.NewReader(os.Stdin)
	for {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			os.Exit(0)
		}
		var message struct {
			ID     jsontext.Value `json:"id"`
			Method string         `json:"method"`
			Params struct {
				SessionID string `json:"sessionId"`
				Cwd       string `json:"cwd"`
			} `json:"params"`
		}
		if json.Unmarshal(line, &message) != nil {
			os.Exit(2)
		}
		reply := func(result any) { send(map[string]any{"jsonrpc": "2.0", "id": message.ID, "result": result}) }
		switch message.Method {
		case "initialize":
			reply(map[string]any{
				"protocolVersion": 1, "agentInfo": map[string]any{"name": name, "version": "1"},
				"agentCapabilities": map[string]any{"loadSession": true, "sessionCapabilities": map[string]any{"close": map[string]any{}}},
			})
		case "session/new":
			id := "s" + strconv.Itoa(len(cwds)+1) + "-" + strconv.Itoa(os.Getpid())
			cwds[id] = message.Params.Cwd
			wd, _ := os.Getwd()
			record("new %s %s wd=%s", id, message.Params.Cwd, wd)
			reply(map[string]any{"sessionId": id})
		case "session/load":
			cwds[message.Params.SessionID] = message.Params.Cwd
			record("load %s %s", message.Params.SessionID, message.Params.Cwd)
			reply(map[string]any{})
		case "session/prompt":
			answer := "cwd=" + cwds[message.Params.SessionID]
			if mark := os.Getenv("FORGE_AGENT_MARK"); mark != "" {
				answer += " mark=" + mark + " token=" + os.Getenv("GITHUB_TOKEN")
			}
			send(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{
				"sessionId": message.Params.SessionID,
				"update":    map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": answer}},
			}})
			reply(map[string]any{"stopReason": "end_turn"})
		case "session/close":
			record("close %s", message.Params.SessionID)
			delete(cwds, message.Params.SessionID)
			reply(map[string]any{})
		case "session/cancel":
			record("cancel %s", message.Params.SessionID)
		default:
			if len(message.ID) > 0 {
				send(map[string]any{"jsonrpc": "2.0", "id": message.ID, "error": map[string]any{"code": -32601, "message": "unsupported"}})
			}
		}
	}
}

// sharedACPChats launches a chat per worktree on an agent that shares hosts.
func sharedACPChats(t *testing.T, worktrees ...string) (*Manager, string, map[string]SessionInfo, chan SessionInfo) {
	t.Helper()
	t.Setenv("KENN_FORGE_ACP_SHARED_FIXTURE", "1")
	t.Setenv("KENN_FORGE_ACP_HOST_FIXTURE", "1")
	dir := t.TempDir()
	t.Setenv("KENN_FORGE_ACP_CONTINUE_DIR", dir)
	executable, err := os.Executable()
	require.NoError(t, err)
	exits := make(chan SessionInfo, 8)
	manager := newACPTestManager(t, Options{
		ACPSessionsDir:     filepath.Join(dir, "state", "acp"),
		ShareACPAgents:     true,
		ACPHostCommand:     []string{executable, "-test.run=^TestACPHostHelper$", "--"},
		ACPHostIdleTimeout: 200 * time.Millisecond,
		OnSessionExit:      func(info SessionInfo) { exits <- info },
		Targets:            ResolveLaunchTargets([]config.Agent{{Key: "chat", Protocol: "acp", Command: []string{executable, "-test.run=^TestACPSharedAgentHelper$"}}}, nil, nil),
	})
	chats := make(map[string]SessionInfo)
	for _, name := range worktrees {
		cwd := filepath.Join(dir, name)
		require.NoError(t, os.MkdirAll(cwd, 0o700))
		info, err := manager.Launch(t.Context(), "workspace", cwd, "chat")
		require.NoError(t, err)
		chats[name] = info
	}
	return manager, dir, chats, exits
}

func agentLog(t *testing.T, dir string) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "agent.log"))
	require.NoError(t, err)
	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

func agentLogLines(t *testing.T, dir, prefix string) []string {
	t.Helper()
	var lines []string
	for _, line := range agentLog(t, dir) {
		if strings.HasPrefix(line, prefix) {
			lines = append(lines, line)
		}
	}
	return lines
}

// askACPChat prompts a chat and returns the agent's answer.
func askACPChat(t *testing.T, manager *Manager, info SessionInfo) string {
	t.Helper()
	chat, err := manager.ACP("workspace", info.Key)
	require.NoError(t, err)
	before := len(publishedACPState(t, chat).Messages)
	require.NoError(t, chat.Command(ACPCommand{Type: "prompt", Text: "where are you?"}))
	var state ACPState
	require.Eventually(t, func() bool { state = publishedACPState(t, chat); return !state.Busy && len(state.Messages) == before+2 }, 5*time.Second, 10*time.Millisecond)
	return state.Messages[len(state.Messages)-1].Text
}

// Chats of an agent that shares a process run on one agent, each in its own
// worktree. Stopping a chat closes only its session; losing the agent parks
// the other chat, which reloads its session on a new host.
func TestACPChatsShareOneAgentHost(t *testing.T) {
	manager, dir, chats, exits := sharedACPChats(t, "alpha", "bravo")
	assert := assert.New(t)
	assert.Len(agentLogLines(t, dir, "start "), 1, "both chats share one agent process")
	for name, info := range chats {
		assert.Equal("cwd="+filepath.Join(dir, name), askACPChat(t, manager, info))
	}

	var alphaSession string
	for _, line := range agentLogLines(t, dir, "new ") {
		if fields := strings.Fields(line); fields[2] == filepath.Join(dir, "alpha") {
			alphaSession = fields[1]
		}
	}
	require.NoError(t, manager.Stop(t.Context(), "workspace", chats["alpha"].Key))
	require.Eventually(t, func() bool { return len(agentLogLines(t, dir, "close ")) == 1 }, 5*time.Second, 10*time.Millisecond)
	assert.Equal("close "+alphaSession, agentLogLines(t, dir, "close ")[0], "stopping a chat closes only its session")
	assert.Equal("cwd="+filepath.Join(dir, "bravo"), askACPChat(t, manager, chats["bravo"]))

	var pid int
	_, err := fmt.Sscanf(agentLogLines(t, dir, "start ")[0], "start %d", &pid)
	require.NoError(t, err)
	agent, err := os.FindProcess(pid)
	require.NoError(t, err)
	require.NoError(t, agent.Kill())
	var parked SessionInfo
	require.Eventually(t, func() bool {
		select {
		case parked = <-exits:
			return parked.Key == chats["bravo"].Key
		default:
			return false
		}
	}, 10*time.Second, 10*time.Millisecond)
	assert.Equal(SessionStatusParked, parked.Status, "losing the shared agent parks its chats")
	assert.False(manager.Exited(chats["bravo"].Key))

	bravo := chats["bravo"]
	require.Eventually(t, func() bool { return restoreACPChat(manager, bravo, filepath.Join(dir, "bravo")) == nil }, 10*time.Second, 50*time.Millisecond)
	assert.Len(agentLogLines(t, dir, "start "), 2, "the reload starts a new host")
	require.Len(t, agentLogLines(t, dir, "load "), 1)
	assert.Contains(agentLogLines(t, dir, "load ")[0], filepath.Join(dir, "bravo"))
	assert.Equal("cwd="+filepath.Join(dir, "bravo"), askACPChat(t, manager, chats["bravo"]))

	// A host with no chats left exits after its idle timeout.
	_, err = fmt.Sscanf(agentLogLines(t, dir, "start ")[1], "start %d", &pid)
	require.NoError(t, err)
	require.NoError(t, manager.Stop(t.Context(), "workspace", bravo.Key))
	require.Eventually(t, func() bool { return !processAlive(pid) }, 10*time.Second, 10*time.Millisecond)
}

// An agent not known to keep sessions apart serves one chat per process.
func TestACPUnsharedAgentRunsAProcessPerChat(t *testing.T) {
	t.Setenv("KENN_FORGE_ACP_SHARED_NAME", "unknown-agent")
	manager, dir, chats, _ := sharedACPChats(t, "alpha", "bravo")
	// Session IDs name the agent process that created them.
	created := agentLogLines(t, dir, "new ")
	require.Len(t, created, 2)
	assert.NotEqual(t, strings.Split(strings.Fields(created[0])[1], "-")[1], strings.Split(strings.Fields(created[1])[1], "-")[1], "each chat runs its own agent")
	for _, line := range created {
		fields := strings.Fields(line)
		assert.Equal(t, "wd="+fields[2], fields[3], "each chat's agent runs in its worktree")
	}
	for name, info := range chats {
		assert.Equal(t, "cwd="+filepath.Join(dir, name), askACPChat(t, manager, info))
	}
}

// A host's agent is stale once its command resolves elsewhere or a file it
// ran changed, judged from file metadata alone.
func TestACPHostFingerprintNoticesChangedAgent(t *testing.T) {
	agent := exec.CommandContext(t.Context(), "sleep", "30")
	require.NoError(t, agent.Start())
	t.Cleanup(func() { _ = agent.Process.Kill(); _ = agent.Wait() })
	first, second := t.TempDir(), t.TempDir()
	t.Setenv("PATH", first+string(os.PathListSeparator)+second)
	script := filepath.Join(second, "agent")
	require.NoError(t, os.WriteFile(script, []byte("#!/bin/sh\n"), 0o700))
	fingerprint := recordACPHostFingerprint([]string{"agent"}, second, agent.Process.Pid)
	assert := assert.New(t)
	assert.Empty(acpHostStale(t.Context(), fingerprint, []string{"agent"}, second, nil))

	replacement := filepath.Join(second, "agent.new")
	require.NoError(t, os.WriteFile(replacement, []byte("#!/bin/sh\n"), 0o700))
	require.NoError(t, os.Rename(replacement, script))
	assert.Contains(acpHostStale(t.Context(), fingerprint, []string{"agent"}, second, nil), "changed")

	fingerprint = recordACPHostFingerprint([]string{"agent"}, second, agent.Process.Pid)
	require.NoError(t, os.WriteFile(filepath.Join(first, "agent"), []byte("#!/bin/sh\n"), 0o700))
	assert.Equal("the command resolves elsewhere", acpHostStale(t.Context(), fingerprint, []string{"agent"}, second, nil))
}

// A host whose agent ran from a mise install mise no longer selects is
// stale, even when only the agent's own PATH holds mise.
func TestACPHostMiseCheckUsesTheAgentPath(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	bin := t.TempDir()
	selected := filepath.Join(bin, "installs", "agent", "2.0")
	script := "#!/bin/sh\nprintf '{\"agent\":[{\"install_path\":\"" + selected + "\"}]}'\n"
	require.NoError(t, os.WriteFile(filepath.Join(bin, "mise"), []byte(script), 0o700))
	fingerprint := acpHostFingerprint{Files: []acpFileStamp{{Real: filepath.Join(bin, "installs", "agent", "1.0", "bin", "agent")}}}

	reason := miseSelectionChanged(t.Context(), fingerprint, t.TempDir(), []string{"PATH=" + bin})
	assert.Contains(t, reason, "mise no longer selects")
}

// An owner that stops waiting for a request cancels it at the agent under
// the ID the host gave the request, not the owner's own.
func TestACPHostForwardsCancelUnderTheHostRequestID(t *testing.T) {
	agentOut, hostToAgent := io.Pipe()
	host := &acpHost{agentIn: hostToAgent, owners: make(map[*acpHostOwner]struct{}), sessions: make(map[string]*acpHostOwner), pending: make(map[string]acpHostRequest), agentRequests: make(map[string]*acpHostOwner), idle: make(chan struct{}, 1), shareable: true}
	ownerSide, hostSide := net.Pipe()
	owner := &acpHostOwner{conn: hostSide, sessions: make(map[string]struct{}), requests: make(map[string]string)}
	host.owners[owner] = struct{}{}
	go host.serve(owner)
	t.Cleanup(func() { _ = ownerSide.Close(); _ = agentOut.Close() })

	client := acpsdk.NewConnection(nil, ownerSide, ownerSide)
	ctx, cancel := context.WithCancel(t.Context())
	go func() {
		_, _ = acpsdk.SendRequest[map[string]any](client, ctx, "_forge/wait", map[string]any{"sessionId": "s1"})
	}()
	agent := bufio.NewReader(agentOut)
	readMessage := func() acpHostMessage {
		t.Helper()
		line, err := agent.ReadBytes('\n')
		require.NoError(t, err)
		message, ok := parseACPHostMessage(line)
		require.True(t, ok)
		return message
	}
	request := readMessage()
	require.Equal(t, "_forge/wait", request.Method)
	cancel()
	canceled := readMessage()
	assert.Equal(t, acpCancelRequest, canceled.Method)
	assert.Equal(t, string(request.ID), string(canceled.RequestID))
}

// An agent withdrawing a request it sent one chat reaches only that chat.
func TestACPHostRoutesAnAgentCancelToItsChat(t *testing.T) {
	host := &acpHost{agentIn: io.Discard, owners: make(map[*acpHostOwner]struct{}), sessions: make(map[string]*acpHostOwner), pending: make(map[string]acpHostRequest), agentRequests: make(map[string]*acpHostOwner), idle: make(chan struct{}, 1), shareable: true}
	chats := make(map[string]net.Conn)
	for _, session := range []string{"alpha", "bravo"} {
		chatSide, hostSide := net.Pipe()
		t.Cleanup(func() { _ = chatSide.Close() })
		owner := &acpHostOwner{conn: hostSide, sessions: map[string]struct{}{session: {}}, requests: make(map[string]string)}
		host.owners[owner] = struct{}{}
		host.sessions[session] = owner
		chats[session] = chatSide
	}
	agentOut, agent := io.Pipe()
	go host.readAgent(bufio.NewReader(agentOut))
	go func() {
		_, _ = io.WriteString(agent, `{"jsonrpc":"2.0","id":7,"method":"session/request_permission","params":{"sessionId":"alpha"}}`+"\n")
		_, _ = io.WriteString(agent, `{"jsonrpc":"2.0","method":"$/cancel_request","params":{"requestId":7}}`+"\n")
		_, _ = io.WriteString(agent, `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"bravo"}}`+"\n")
		_ = agent.Close()
	}()
	read := func(chat string) acpHostMessage {
		t.Helper()
		line, err := bufio.NewReader(chats[chat]).ReadBytes('\n')
		require.NoError(t, err)
		message, ok := parseACPHostMessage(line)
		require.True(t, ok)
		return message
	}
	assert.Equal(t, "session/request_permission", read("alpha").Method)
	canceled := read("alpha")
	assert.Equal(t, acpCancelRequest, canceled.Method)
	assert.Equal(t, "7", string(canceled.RequestID))
	assert.Equal(t, "session/update", read("bravo").Method, "the other chat sees only its own session")
}

// A message for a session no chat owns reaches no chat, even one whose
// session/new is still waiting, and the agent hears its request refused.
func TestACPHostDropsMessagesForUnknownSessions(t *testing.T) {
	toAgent, hostToAgent := io.Pipe()
	host := &acpHost{agentIn: hostToAgent, owners: make(map[*acpHostOwner]struct{}), sessions: make(map[string]*acpHostOwner), pending: make(map[string]acpHostRequest), agentRequests: make(map[string]*acpHostOwner), idle: make(chan struct{}, 1), shareable: true}
	chatSide, hostSide := net.Pipe()
	owner := &acpHostOwner{conn: hostSide, sessions: make(map[string]struct{}), requests: make(map[string]string)}
	host.owners[owner] = struct{}{}
	go host.serve(owner)
	agentOut, agentSide := io.Pipe()
	t.Cleanup(func() { _ = chatSide.Close(); _ = toAgent.Close(); _ = agentSide.Close() })
	// Both sides are read as messages arrive, so a misrouted message fails
	// the test instead of blocking the host.
	lines := func(from io.Reader) <-chan string {
		out := make(chan string, 8)
		go func() {
			reader := bufio.NewReader(from)
			for {
				line, err := reader.ReadBytes('\n')
				if err != nil {
					close(out)
					return
				}
				out <- string(line)
			}
		}()
		return out
	}
	toAgentLines, chatLines := lines(toAgent), lines(chatSide)
	next := func(from <-chan string) (acpHostMessage, string) {
		t.Helper()
		select {
		case line := <-from:
			message, ok := parseACPHostMessage([]byte(line))
			require.True(t, ok, line)
			return message, line
		case <-time.After(5 * time.Second):
			require.FailNow(t, "no message arrived")
			return acpHostMessage{}, ""
		}
	}
	_, err := io.WriteString(chatSide, `{"jsonrpc":"2.0","id":1,"method":"session/new","params":{"cwd":"/work","mcpServers":[]}}`+"\n")
	require.NoError(t, err)
	request, _ := next(toAgentLines)
	require.Equal(t, acpsdk.AgentMethodSessionNew, request.Method)

	go host.readAgent(bufio.NewReader(agentOut))
	go func() {
		_, _ = io.WriteString(agentSide, `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"closed"}}`+"\n")
		_, _ = io.WriteString(agentSide, `{"jsonrpc":"2.0","id":9,"method":"session/request_permission","params":{"sessionId":"closed"}}`+"\n")
		_, _ = io.WriteString(agentSide, `{"jsonrpc":"2.0","id":`+string(request.ID)+`,"result":{"sessionId":"alpha"}}`+"\n")
		_, _ = io.WriteString(agentSide, `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"alpha"}}`+"\n")
	}()
	created, _ := next(chatLines)
	assert.Equal(t, "1", string(created.ID), "the chat hears its session/new answer first")
	assert.Equal(t, "alpha", created.SessionID)
	update, _ := next(chatLines)
	assert.Equal(t, "session/update", update.Method)
	assert.Equal(t, "alpha", update.SessionID, "the update for an unknown session is dropped")
	refused, line := next(toAgentLines)
	assert.Equal(t, "9", string(refused.ID))
	assert.Contains(t, line, `"error"`, "the agent's request for an unknown session is refused")
}

// A session the agent creates after its chat disconnected is closed, so the
// agent frees it.
func TestACPHostClosesASessionCreatedForADisconnectedChat(t *testing.T) {
	toAgent, hostToAgent := io.Pipe()
	host := &acpHost{agentIn: hostToAgent, owners: make(map[*acpHostOwner]struct{}), sessions: make(map[string]*acpHostOwner), pending: make(map[string]acpHostRequest), agentRequests: make(map[string]*acpHostOwner), idle: make(chan struct{}, 1), shareable: true}
	chatSide, hostSide := net.Pipe()
	owner := &acpHostOwner{conn: hostSide, sessions: make(map[string]struct{}), requests: make(map[string]string)}
	host.owners[owner] = struct{}{}
	go host.serve(owner)
	agent := bufio.NewReader(toAgent)
	readMessage := func() acpHostMessage {
		t.Helper()
		line, err := agent.ReadBytes('\n')
		require.NoError(t, err)
		message, ok := parseACPHostMessage(line)
		require.True(t, ok)
		return message
	}
	_, err := io.WriteString(chatSide, `{"jsonrpc":"2.0","id":1,"method":"session/new","params":{"cwd":"/work","mcpServers":[]}}`+"\n")
	require.NoError(t, err)
	request := readMessage()
	require.Equal(t, acpsdk.AgentMethodSessionNew, request.Method)
	require.NoError(t, chatSide.Close())
	require.Eventually(t, func() bool { return host.ownerCount() == 0 }, 5*time.Second, 10*time.Millisecond)

	agentOut, agentReplies := io.Pipe()
	go host.readAgent(bufio.NewReader(agentOut))
	_, err = io.WriteString(agentReplies, `{"jsonrpc":"2.0","id":`+string(request.ID)+`,"result":{"sessionId":"late"}}`+"\n")
	require.NoError(t, err)
	canceled := readMessage()
	assert.Equal(t, acpsdk.AgentMethodSessionCancel, canceled.Method)
	assert.Equal(t, "late", canceled.SessionID)
	closed := readMessage()
	assert.Equal(t, acpsdk.AgentMethodSessionClose, closed.Method)
	assert.Equal(t, "late", closed.SessionID)
	_ = agentReplies.Close()
}

// An owner that disconnects after its chat reloaded the session on a new
// owner leaves the session, and its route, to the new owner.
func TestACPHostDisconnectKeepsAReloadedSession(t *testing.T) {
	var toAgent bytes.Buffer
	host := &acpHost{agentIn: &toAgent, owners: make(map[*acpHostOwner]struct{}), sessions: make(map[string]*acpHostOwner), pending: make(map[string]acpHostRequest), agentRequests: make(map[string]*acpHostOwner), idle: make(chan struct{}, 1), shareable: true}
	owners := make([]*acpHostOwner, 2)
	for i := range owners {
		chatSide, hostSide := net.Pipe()
		t.Cleanup(func() { _ = chatSide.Close() })
		owners[i] = &acpHostOwner{conn: hostSide, sessions: map[string]struct{}{"s1": {}}, requests: make(map[string]string)}
		host.owners[owners[i]] = struct{}{}
	}
	host.sessions["s1"] = owners[1]

	host.disconnect(owners[0])
	assert.Same(t, owners[1], host.sessions["s1"], "the reloaded session keeps its new route")
	assert.Empty(t, toAgent.String(), "the session is neither cancelled nor closed")
}

// An agent's configured environment reaches its process over Forge's
// credential stripping, and agents that differ only in environment never
// share a host.
func TestACPAgentEnvReachesTheAgentAndKeepsHostsApart(t *testing.T) {
	t.Setenv("KENN_FORGE_ACP_SHARED_FIXTURE", "1")
	t.Setenv("KENN_FORGE_ACP_HOST_FIXTURE", "1")
	t.Setenv("GITHUB_TOKEN", "inherited")
	dir := t.TempDir()
	t.Setenv("KENN_FORGE_ACP_CONTINUE_DIR", dir)
	executable, err := os.Executable()
	require.NoError(t, err)
	command := []string{executable, "-test.run=^TestACPSharedAgentHelper$"}
	manager := newACPTestManager(t, Options{
		ACPSessionsDir:     filepath.Join(dir, "state", "acp"),
		ShareACPAgents:     true,
		ACPHostCommand:     []string{executable, "-test.run=^TestACPHostHelper$", "--"},
		ACPHostIdleTimeout: 200 * time.Millisecond,
		Targets: ResolveLaunchTargets([]config.Agent{
			{Key: "alpha", Protocol: "acp", Command: command, Env: map[string]string{"FORGE_AGENT_MARK": "alpha", "GITHUB_TOKEN": "configured"}},
			{Key: "bravo", Protocol: "acp", Command: command, Env: map[string]string{"FORGE_AGENT_MARK": "bravo"}},
		}, nil, nil),
	})
	for _, target := range []string{"alpha", "bravo"} {
		info, err := manager.Launch(t.Context(), "workspace", dir, target)
		require.NoError(t, err)
		want := "cwd=" + dir + " mark=" + target + " token="
		if target == "alpha" {
			want += "configured"
		}
		assert.Equal(t, want, askACPChat(t, manager, info))
	}
	assert.Len(t, agentLogLines(t, dir, "start "), 2)
}
