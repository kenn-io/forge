package localruntime

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"io"
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

// This executable fixture is the foreign ACP peer. It verifies the host's
// initialization and workspace handoff before serving streamed turns.
func TestACPStdioHelper(t *testing.T) {
	if os.Getenv("KENN_FORGE_ACP_FIXTURE") != "1" {
		return
	}
	scanner := bufio.NewScanner(os.Stdin)
	var promptID jsontext.Value
	model, effort := "fast", "low"
	configResponse := func(id jsontext.Value) {
		if os.Getenv("KENN_FORGE_ACP_MODEL_DEPENDENT") == "1" {
			options := fmt.Sprintf(`[{"id":"model","name":"Model","category":"model","type":"select","currentValue":%q,"options":[{"group":"models","name":"Models","options":[{"value":"fast","name":"Fast"},{"value":"deep","name":"Deep"}]}]}]`, model)
			if model == "deep" {
				options = fmt.Sprintf(`[{"id":"model","name":"Model","category":"model","type":"select","currentValue":%q,"options":[{"group":"models","name":"Models","options":[{"value":"fast","name":"Fast"},{"value":"deep","name":"Deep"}]}]},{"id":"effort","name":"Effort","category":"thought_level","type":"select","currentValue":%q,"options":[{"value":"low","name":"Low"},{"value":"high","name":"High"}]}]`, model, effort)
			}
			fmt.Printf(`{"jsonrpc":"2.0","id":%s,"result":{"sessionId":"fixture-session","configOptions":%s}}`+"\n", id, options)
			return
		}
		fmt.Printf(`{"jsonrpc":"2.0","id":%s,"result":{"sessionId":"fixture-session","configOptions":[{"id":"effort","name":"Effort","category":"thought_level","type":"select","currentValue":%q,"options":[{"value":"low","name":"Low"},{"value":"high","name":"High"}]},{"id":"model","name":"Model","category":"model","type":"select","currentValue":%q,"options":[{"group":"models","name":"Models","options":[{"value":"fast","name":"Fast"},{"value":"deep","name":"Deep"}]}]}]}}`+"\n", id, effort, model)
	}
	for scanner.Scan() {
		var message struct {
			ID     jsontext.Value `json:"id"`
			Method string         `json:"method"`
			Params jsontext.Value `json:"params"`
			Result jsontext.Value `json:"result"`
		}
		if json.Unmarshal(scanner.Bytes(), &message) != nil {
			os.Exit(2)
		}
		switch message.Method {
		case "initialize":
			if os.Getenv("KENN_FORGE_ACP_BAD_VERSION") == "1" {
				fmt.Printf(`{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":999}}`+"\n", message.ID)
				continue
			}
			var params struct {
				ProtocolVersion int `json:"protocolVersion"`
			}
			if json.Unmarshal(message.Params, &params) != nil || params.ProtocolVersion != 1 {
				os.Exit(3)
			}
			fmt.Printf(`{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":1,"agentCapabilities":{"mcpCapabilities":{"http":%t}}}}`+"\n", message.ID, os.Getenv("KENN_FORGE_ACP_NO_HTTP") != "1")
		case "session/new":
			var params struct {
				CWD        string `json:"cwd"`
				MCPServers []any  `json:"mcpServers"`
			}
			cwd, _ := os.Getwd()
			if json.Unmarshal(message.Params, &params) != nil || params.MCPServers == nil {
				os.Exit(4)
			}
			resolved, err := filepath.EvalSymlinks(params.CWD)
			if err != nil || resolved != cwd {
				os.Exit(4)
			}
			configResponse(message.ID)
			if code, err := strconv.Atoi(os.Getenv("KENN_FORGE_ACP_EXIT_AFTER_SESSION")); err == nil {
				time.Sleep(50 * time.Millisecond)
				os.Exit(code)
			}
		case "session/set_config_option":
			var params struct {
				ConfigID string `json:"configId"`
				Value    string `json:"value"`
			}
			if json.Unmarshal(message.Params, &params) != nil {
				os.Exit(7)
			}
			switch params.ConfigID {
			case "model":
				model, effort = params.Value, "low"
			case "effort":
				effort = params.Value
			default:
				os.Exit(8)
			}
			configResponse(message.ID)
		case "session/prompt":
			var params struct {
				SessionID string `json:"sessionId"`
				Prompt    []struct {
					Text string `json:"text"`
				} `json:"prompt"`
			}
			if json.Unmarshal(message.Params, &params) != nil || params.SessionID != "fixture-session" || len(params.Prompt) != 1 {
				os.Exit(5)
			}
			promptID = message.ID
			fmt.Println(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"fixture-session","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"Hello "}}}}`)
			fmt.Println(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"fixture-session","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"workspace"}}}}`)
			if params.Prompt[0].Text == "permission" {
				fmt.Println(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"fixture-session","update":{"sessionUpdate":"tool_call","toolCallId":"edit","title":"Edit file","status":"pending","content":[{"type":"diff","path":"/tmp/example.txt","oldText":"before","newText":"after"}]}}}`)
				fmt.Println(`{"jsonrpc":"2.0","id":"approval","method":"session/request_permission","params":{"sessionId":"fixture-session","toolCall":{"toolCallId":"edit","title":"Edit file"},"options":[{"optionId":"allow","name":"Allow once","kind":"allow_once"},{"optionId":"deny","name":"Reject","kind":"reject_once"}]}}`)
			} else if params.Prompt[0].Text != "wait" {
				fmt.Printf(`{"jsonrpc":"2.0","id":%s,"result":{"stopReason":"end_turn"}}`+"\n", promptID)
			}
		case "session/cancel":
			fmt.Printf(`{"jsonrpc":"2.0","id":%s,"result":{"stopReason":"cancelled"}}`+"\n", promptID)
		case "":
			var result struct {
				Outcome struct {
					Outcome  string `json:"outcome"`
					OptionID string `json:"optionId"`
				} `json:"outcome"`
			}
			if json.Unmarshal(message.Result, &result) != nil || string(message.ID) != `"approval"` || result.Outcome.Outcome != "selected" || result.Outcome.OptionID != "allow" {
				os.Exit(6)
			}
			fmt.Println(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"fixture-session","update":{"sessionUpdate":"tool_call_update","toolCallId":"edit","status":"completed","content":[{"type":"content","content":{"type":"text","text":"Updated file"}}]}}}`)
			fmt.Printf(`{"jsonrpc":"2.0","id":%s,"result":{"stopReason":"end_turn"}}`+"\n", promptID)
		}
	}
	os.Exit(0)
}

func TestACPWorkspaceConversation(t *testing.T) {
	t.Setenv("KENN_FORGE_LOCALRUNTIME_HELPER", "1")
	t.Setenv("KENN_FORGE_ACP_FIXTURE", "1")
	executable, err := os.Executable()
	require.NoError(t, err)
	targets := ResolveLaunchTargets([]config.Agent{{Key: "chat", Label: "Chat", Protocol: "acp", Command: []string{executable, "-test.run=^TestACPStdioHelper$"}}}, nil, nil)
	manager := NewManager(Options{Targets: targets})
	t.Cleanup(manager.Shutdown)
	cwd, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	info, err := manager.Launch(t.Context(), "workspace", cwd, "chat")
	require.NoError(t, err)
	assert.Equal(t, LaunchTargetACP, info.Kind)
	assert.Empty(t, info.TmuxSession)
	agent, err := manager.ACP("workspace", info.Key)
	require.NoError(t, err)
	_, err = manager.ACP("different-workspace", info.Key)
	require.ErrorIs(t, err, ErrSessionNotFound)
	changes, unsubscribe := agent.Subscribe()
	defer unsubscribe()
	require.NoError(t, agent.Command(ACPCommand{Type: "prompt", Text: "permission", ID: "submission-1"}))
	var state ACPState
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for len(state.Permissions) == 0 || len(state.Messages) < 3 {
		select {
		case <-changes:
			data, err := agent.Snapshot()
			require.NoError(t, err)
			require.NoError(t, json.Unmarshal(data, &state))
		case <-deadline.C:
			require.FailNow(t, "agent did not request permission")
		}
	}
	require.Len(t, state.Messages, 3)
	assert.Equal(t, "submission-1", state.Messages[0].SubmissionID)
	assert.Equal(t, "Hello workspace", state.Messages[1].Text)
	assert.Equal(t, "pending", state.Messages[2].Status)
	require.Error(t, agent.Command(ACPCommand{Type: "permission", ID: state.Permissions[0].ID, OptionID: "unknown"}))
	require.NoError(t, agent.Command(ACPCommand{Type: "permission", ID: state.Permissions[0].ID, OptionID: "allow"}))
	require.Eventually(t, func() bool {
		data, err := agent.Snapshot()
		if err != nil {
			return false
		}
		_ = json.Unmarshal(data, &state)
		return !state.Busy && state.Messages[2].Status == "completed"
	}, 5*time.Second, 10*time.Millisecond)
	require.NoError(t, agent.Command(ACPCommand{Type: "prompt", Text: "permission", ID: "submission-1"}))
	duplicateSnapshot, err := agent.Snapshot()
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(duplicateSnapshot, &state))
	assert.Len(t, state.Messages, 3, "reconnecting must not submit a second turn")
	assert.Empty(t, state.Error)
	// A new browser sees the entire transcript without starting another process.
	another, release := agent.Subscribe()
	defer release()
	<-another
	data, err := agent.Snapshot()
	require.NoError(t, err)
	assert.Contains(t, string(data), "Hello workspace")
	require.NoError(t, manager.SubmitInitialMessage(t.Context(), "workspace", info.Key, "wait"))
	require.NoError(t, agent.Command(ACPCommand{Type: "cancel"}))
	require.Eventually(t, func() bool { data, _ := agent.Snapshot(); _ = json.Unmarshal(data, &state); return !state.Busy }, 5*time.Second, 10*time.Millisecond)
	require.NoError(t, manager.Stop(context.Background(), "workspace", info.Key))
	assert.Empty(t, manager.ListSessions("workspace"))
}

func TestACPConnectionProbe(t *testing.T) {
	manager := NewManager(Options{})
	t.Setenv("KENN_FORGE_LOCALRUNTIME_HELPER", "1")
	t.Setenv("KENN_FORGE_ACP_FIXTURE", "1")
	executable, err := os.Executable()
	require.NoError(t, err)
	command := []string{executable, "-test.run=^TestACPStdioHelper$"}
	require.NoError(t, manager.TestACP(t.Context(), command))
	require.ErrorContains(t, manager.TestACP(t.Context(), nil), "executable is required")
	require.Error(t, manager.TestACP(t.Context(), []string{filepath.Join(t.TempDir(), "missing-agent")}))
	withMCP := NewManager(Options{AgentMCPURL: "http://127.0.0.1:12345/agent-mcp", AgentMCPToken: "fixture-token"})
	t.Setenv("KENN_FORGE_ACP_NO_HTTP", "1")
	require.ErrorContains(t, withMCP.TestACP(t.Context(), command), "does not accept HTTP MCP servers")
	t.Setenv("KENN_FORGE_ACP_NO_HTTP", "")
	require.NoError(t, withMCP.TestACP(t.Context(), command))
	t.Setenv("KENN_FORGE_ACP_BAD_VERSION", "1")
	require.ErrorContains(t, manager.TestACP(t.Context(), command), "unsupported ACP protocol version")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	require.ErrorIs(t, manager.TestACP(ctx, command), context.Canceled)
}

func TestACPRemembersSettingsPerClientAndHost(t *testing.T) {
	t.Setenv("KENN_FORGE_LOCALRUNTIME_HELPER", "1")
	t.Setenv("KENN_FORGE_ACP_FIXTURE", "1")
	executable, err := os.Executable()
	require.NoError(t, err)
	command := []string{executable, "-test.run=^TestACPStdioHelper$"}
	targets := ResolveLaunchTargets([]config.Agent{{Key: "chat", Protocol: "acp", Command: command}, {Key: "other", Protocol: "acp", Command: command}}, nil, nil)
	preferences := filepath.Join(t.TempDir(), "preferences.json")
	first := NewManager(Options{Targets: targets, ACPPreferencesPath: preferences})
	t.Cleanup(first.Shutdown)
	cwd := t.TempDir()
	info, err := first.Launch(t.Context(), "first", cwd, "chat")
	require.NoError(t, err)
	agent, err := first.ACP("first", info.Key)
	require.NoError(t, err)
	require.NoError(t, agent.Command(ACPCommand{Type: "config", ID: "model", Value: "deep"}))
	require.NoError(t, agent.Command(ACPCommand{Type: "config", ID: "effort", Value: "high"}))
	require.ErrorContains(t, agent.Command(ACPCommand{Type: "config", ID: "model", Value: "unknown"}), "offered by the agent")
	first.Shutdown()
	for _, tc := range []struct{ name, path, target, model, effort string }{
		{"another workspace after restart", preferences, "chat", "deep", "high"},
		{"another client", preferences, "other", "fast", "low"},
		{"another host", filepath.Join(t.TempDir(), "preferences.json"), "chat", "fast", "low"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manager := NewManager(Options{Targets: targets, ACPPreferencesPath: tc.path})
			t.Cleanup(manager.Shutdown)
			info, err := manager.Launch(t.Context(), "second", cwd, tc.target)
			require.NoError(t, err)
			agent, err := manager.ACP("second", info.Key)
			require.NoError(t, err)
			data, err := agent.Snapshot()
			require.NoError(t, err)
			var state ACPState
			require.NoError(t, json.Unmarshal(data, &state))
			require.Len(t, state.ConfigOptions, 2)
			assert.Equal(t, tc.effort, state.ConfigOptions[0].CurrentValue)
			assert.Equal(t, tc.model, state.ConfigOptions[1].CurrentValue)
		})
	}
}

type testWriteCloser struct{ io.Writer }

func (testWriteCloser) Close() error { return nil }

func TestACPPromptAcknowledgesOnlyAfterWrite(t *testing.T) {
	failedReader, failedWriter := io.Pipe()
	require.NoError(t, failedReader.Close())
	agent := &ACP{
		stdin: failedWriter, done: make(chan struct{}),
		subscribers: make(map[chan struct{}]struct{}),
		state:       ACPState{Connected: true},
	}

	peerReader, peerWriter := io.Pipe()
	t.Cleanup(func() { _ = peerWriter.Close(); _ = peerReader.Close() })
	agent.client = acpsdk.NewClientSideConnection(agent, agent, peerReader)
	err := agent.Command(ACPCommand{Type: "prompt", Text: "retry me", ID: "submission"})
	require.Error(t, err)
	assert.Empty(t, agent.state.Messages)

	var written bytes.Buffer
	agent.stdin = testWriteCloser{Writer: &written}
	require.NoError(t, agent.Command(ACPCommand{Type: "prompt", Text: "retry me", ID: "submission"}))
	require.Len(t, agent.state.Messages, 1)
	assert.Equal(t, "submission", agent.state.Messages[0].SubmissionID)
	assert.Contains(t, written.String(), `"method":"session/prompt"`)
	close(agent.done)
}

func TestACPPromptPrecedesConcurrentOutputAfterHistoryTrim(t *testing.T) {
	reader, writer := io.Pipe()
	agent := &ACP{
		stdin: writer, done: make(chan struct{}),
		subscribers: make(map[chan struct{}]struct{}),
		state:       ACPState{Connected: true, Messages: []ACPMessage{{Role: "assistant", Text: strings.Repeat("old", 2<<20)}}},
	}
	t.Cleanup(func() { _ = reader.Close(); _ = writer.Close(); close(agent.done) })
	peerReader, peerWriter := io.Pipe()
	t.Cleanup(func() { _ = peerWriter.Close(); _ = peerReader.Close() })
	agent.client = acpsdk.NewClientSideConnection(agent, agent, peerReader)
	sent := make(chan error, 1)
	go func() { sent <- agent.Command(ACPCommand{Type: "prompt", Text: "new question", ID: "new-submission"}) }()
	// The write has started but cannot complete while only one byte is read.
	_, err := reader.Read(make([]byte, 1))
	require.NoError(t, err)
	require.NoError(t, agent.SessionUpdate(t.Context(), acpsdk.SessionNotification{Update: acpsdk.SessionUpdate{AgentMessageChunk: &acpsdk.SessionUpdateAgentMessageChunk{Content: acpsdk.TextBlock("new answer")}}}))
	_, err = bufio.NewReader(reader).ReadString('\n')
	require.NoError(t, err)
	require.NoError(t, <-sent)
	data, err := agent.Snapshot()
	require.NoError(t, err)
	var state ACPState
	require.NoError(t, json.Unmarshal(data, &state))
	require.Len(t, state.Messages, 2)
	assert.Equal(t, "new question", state.Messages[0].Text)
	assert.Equal(t, "new-submission", state.Messages[0].SubmissionID)
	assert.Equal(t, "new answer", state.Messages[1].Text)
}

func TestACPStopClosesStdoutReader(t *testing.T) {
	executable, err := os.Executable()
	require.NoError(t, err)
	stdinReader, stdinWriter := io.Pipe()
	stdoutReader, stdoutWriter := io.Pipe()
	t.Cleanup(func() {
		_ = stdinReader.Close()
		_ = stdoutWriter.Close()
	})
	cmd := exec.CommandContext(t.Context(), executable, "-test.run=^TestACPStdioHelper$")
	cmd.Env = append(os.Environ(), "KENN_FORGE_ACP_FIXTURE=1")
	cmd.Stdin = stdinReader
	require.NoError(t, cmd.Start())
	agent := &ACP{
		cmd: cmd, stdin: stdinWriter, stdout: stdoutReader, done: make(chan struct{}),
		subscribers: make(map[chan struct{}]struct{}),
		exitCode:    -1,
	}
	agent.client = acpsdk.NewClientSideConnection(agent, agent, stdoutReader)
	go agent.wait()

	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	require.NoError(t, agent.Stop(ctx))
}

func TestACPReportsNaturalExitCode(t *testing.T) {
	t.Setenv("KENN_FORGE_LOCALRUNTIME_HELPER", "1")
	t.Setenv("KENN_FORGE_ACP_FIXTURE", "1")
	t.Setenv("KENN_FORGE_ACP_EXIT_AFTER_SESSION", "7")
	executable, err := os.Executable()
	require.NoError(t, err)
	exits := make(chan SessionInfo, 1)
	targets := ResolveLaunchTargets([]config.Agent{{Key: "chat", Protocol: "acp", Command: []string{executable, "-test.run=^TestACPStdioHelper$"}}}, nil, nil)
	manager := NewManager(Options{Targets: targets, OnSessionExit: func(info SessionInfo) { exits <- info }})
	t.Cleanup(manager.Shutdown)
	_, err = manager.Launch(t.Context(), "workspace", t.TempDir(), "chat")
	require.NoError(t, err)
	select {
	case info := <-exits:
		require.NotNil(t, info.ExitCode)
		assert.Equal(t, 7, *info.ExitCode)
	case <-time.After(5 * time.Second):
		require.FailNow(t, "ACP exit was not reported")
	}
}

func TestACPBoundsRetainedTranscript(t *testing.T) {
	for _, text := range []string{"x", "\x00", "語"} {
		t.Run(fmt.Sprintf("character-%x", text), func(t *testing.T) {
			agent := &ACP{subscribers: make(map[chan struct{}]struct{}), state: ACPState{Messages: []ACPMessage{{Role: "user", Text: "question", SubmissionID: "accepted"}}}}
			for range 5 {
				require.NoError(t, agent.SessionUpdate(t.Context(), acpsdk.SessionNotification{Update: acpsdk.SessionUpdate{AgentMessageChunk: &acpsdk.SessionUpdateAgentMessageChunk{Content: acpsdk.TextBlock(strings.Repeat(text, 1<<20))}}}))
			}
			data, err := agent.Snapshot()
			require.NoError(t, err)
			// History has a 4 MiB wire budget; the fixed snapshot fields fit in 512 B.
			require.LessOrEqual(t, len(data), (4<<20)+512)
			var state ACPState
			require.NoError(t, json.Unmarshal(data, &state))
			require.True(t, state.HistoryTruncated)
			require.Len(t, state.Messages, 2)
			assert.Equal(t, "accepted", state.Messages[0].SubmissionID)
			assert.Equal(t, "question", state.Messages[0].Text)
			assert.True(t, strings.HasSuffix(state.Messages[1].Text, text))
		})
	}
}

func TestACPRestoresOptionsIntroducedBySavedModel(t *testing.T) {
	t.Setenv("KENN_FORGE_LOCALRUNTIME_HELPER", "1")
	t.Setenv("KENN_FORGE_ACP_FIXTURE", "1")
	t.Setenv("KENN_FORGE_ACP_MODEL_DEPENDENT", "1")
	executable, err := os.Executable()
	require.NoError(t, err)
	preferences := filepath.Join(t.TempDir(), "preferences.json")
	require.NoError(t, os.WriteFile(preferences, []byte(`{"chat":{"model":"deep","effort":"high"}}`), 0o600))
	targets := ResolveLaunchTargets([]config.Agent{{Key: "chat", Protocol: "acp", Command: []string{executable, "-test.run=^TestACPStdioHelper$"}}}, nil, nil)
	manager := NewManager(Options{Targets: targets, ACPPreferencesPath: preferences})
	t.Cleanup(manager.Shutdown)
	info, err := manager.Launch(t.Context(), "workspace", t.TempDir(), "chat")
	require.NoError(t, err)
	agent, err := manager.ACP("workspace", info.Key)
	require.NoError(t, err)
	assert.Equal(t, "deep", acpOptionValue(t, agent, "model"))
	assert.Equal(t, "high", acpOptionValue(t, agent, "effort"))
}

func TestACPConcurrentSessionsMergeRememberedOptions(t *testing.T) {
	t.Setenv("KENN_FORGE_LOCALRUNTIME_HELPER", "1")
	t.Setenv("KENN_FORGE_ACP_FIXTURE", "1")
	executable, err := os.Executable()
	require.NoError(t, err)
	command := []string{executable, "-test.run=^TestACPStdioHelper$"}
	targets := ResolveLaunchTargets([]config.Agent{{Key: "chat", Protocol: "acp", Command: command}}, nil, nil)
	manager := NewManager(Options{Targets: targets, ACPPreferencesPath: filepath.Join(t.TempDir(), "preferences.json")})
	t.Cleanup(manager.Shutdown)
	firstInfo, err := manager.Launch(t.Context(), "first", t.TempDir(), "chat")
	require.NoError(t, err)
	secondInfo, err := manager.Launch(t.Context(), "second", t.TempDir(), "chat")
	require.NoError(t, err)
	first, err := manager.ACP("first", firstInfo.Key)
	require.NoError(t, err)
	second, err := manager.ACP("second", secondInfo.Key)
	require.NoError(t, err)
	require.NoError(t, first.Command(ACPCommand{Type: "config", ID: "model", Value: "deep"}))
	require.NoError(t, second.Command(ACPCommand{Type: "config", ID: "effort", Value: "high"}))

	thirdInfo, err := manager.Launch(t.Context(), "third", t.TempDir(), "chat")
	require.NoError(t, err)
	third, err := manager.ACP("third", thirdInfo.Key)
	require.NoError(t, err)
	assert.Equal(t, "deep", acpOptionValue(t, third, "model"))
	assert.Equal(t, "high", acpOptionValue(t, third, "effort"))
}

func acpOptionValue(t *testing.T, agent *ACP, id string) string {
	t.Helper()
	data, err := agent.Snapshot()
	require.NoError(t, err)
	var state ACPState
	require.NoError(t, json.Unmarshal(data, &state))
	for _, option := range state.ConfigOptions {
		if option.ID == id {
			return option.CurrentValue
		}
	}
	require.FailNow(t, "ACP option was not returned", id)
	return ""
}
