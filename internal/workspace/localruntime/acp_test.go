package localruntime

import (
	"bufio"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

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
		fmt.Printf(`{"jsonrpc":"2.0","id":%s,"result":{"sessionId":"fixture-session","configOptions":[{"id":"effort","name":"Effort","category":"thought_level","type":"select","currentValue":%q,"options":[{"value":"low","name":"Low"},{"value":"high","name":"High"}]},{"id":"model","name":"Model","category":"model","type":"select","currentValue":%q,"options":[{"group":"models","name":"Models","options":[{"value":"fast","name":"Fast"},{"value":"deep","name":"Deep"}]}]}]}}`+"\n", id, effort, model)
	}
	for scanner.Scan() {
		var message acpEnvelope
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
			if json.Unmarshal(message.Params, &params) != nil {
				os.Exit(4)
			}
			resolved, err := filepath.EvalSymlinks(params.CWD)
			if err != nil || resolved != cwd {
				os.Exit(4)
			}
			configResponse(message.ID)
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
				fmt.Println(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"fixture-session","update":{"sessionUpdate":"tool_call","toolCallId":"edit","title":"Edit file","status":"pending"}}}`)
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
			fmt.Println(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"fixture-session","update":{"sessionUpdate":"tool_call_update","toolCallId":"edit","status":"completed"}}}`)
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
	for len(state.Permissions) == 0 {
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
	require.Error(t, agent.Command(ACPCommand{Type: "permission", ID: `"approval"`, OptionID: "unknown"}))
	require.NoError(t, agent.Command(ACPCommand{Type: "permission", ID: `"approval"`, OptionID: "allow"}))
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
