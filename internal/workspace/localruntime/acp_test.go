package localruntime

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	acpsdk "github.com/coder/acp-go-sdk"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/agentactivity"
	"go.kenn.io/forge/internal/config"
)

// This executable fixture is the foreign ACP peer. It verifies the host's
// initialization and workspace handoff before serving streamed turns.
func TestACPStdioHelper(t *testing.T) {
	if os.Getenv("KENN_FORGE_ACP_FIXTURE") != "1" {
		return
	}
	fixtureDir := os.Getenv("KENN_FORGE_ACP_CONTINUE_DIR")
	if fixtureDir != "" {
		_ = os.WriteFile(filepath.Join(fixtureDir, "pid"), []byte(strconv.Itoa(os.Getpid())), 0o600)
	}
	// Codex reports thread status; other steering agents may not.
	threadStatus := func(kind string) {
		if os.Getenv("KENN_FORGE_ACP_NO_THREAD_STATUS") != "1" {
			fmt.Printf(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"fixture-session","update":{"sessionUpdate":"session_info_update","_meta":{"codex":{"threadStatus":{"type":%q}}}}}}`+"\n", kind)
		}
	}
	// A late reporter's first status is about a turn it started itself.
	promptThreadStatus := func(kind string) {
		if os.Getenv("KENN_FORGE_ACP_LATE_THREAD_STATUS") != "1" {
			threadStatus(kind)
		}
	}
	scanner := bufio.NewScanner(os.Stdin)
	var promptID jsontext.Value
	elicitationOffered := false
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
				ProtocolVersion    int `json:"protocolVersion"`
				ClientCapabilities struct {
					Elicitation *struct {
						Form *struct{} `json:"form"`
					} `json:"elicitation"`
				} `json:"clientCapabilities"`
			}
			if json.Unmarshal(message.Params, &params) != nil || params.ProtocolVersion != 1 {
				os.Exit(3)
			}
			elicitationOffered = params.ClientCapabilities.Elicitation != nil && params.ClientCapabilities.Elicitation.Form != nil
			fmt.Printf(`{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":1,"agentCapabilities":{"loadSession":%t,"mcpCapabilities":{"http":%t}},"_meta":{"steering":{"supported":%t}}}}`+"\n", message.ID, os.Getenv("KENN_FORGE_ACP_NO_LOAD") != "1", os.Getenv("KENN_FORGE_ACP_NO_HTTP") != "1", os.Getenv("KENN_FORGE_ACP_STEERING") == "1")
		case "session/new", "session/load":
			if fixtureDir != "" {
				file, err := os.OpenFile(filepath.Join(fixtureDir, "sessions"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
				if err != nil {
					os.Exit(9)
				}
				_, _ = fmt.Fprintln(file, message.Method)
				_ = file.Close()
			}
			if message.Method == "session/load" {
				var params struct {
					SessionID string `json:"sessionId"`
				}
				if json.Unmarshal(message.Params, &params) != nil || params.SessionID != "fixture-session" {
					os.Exit(10)
				}
				fmt.Println(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"fixture-session","update":{"sessionUpdate":"user_message_chunk","content":{"type":"text","text":"remember"}}}}`)
				fmt.Println(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"fixture-session","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"restored answer"}}}}`)
				// Commands arrive during the replay, before the load answer.
				fmt.Println(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"fixture-session","update":{"sessionUpdate":"available_commands_update","availableCommands":[{"name":"review","description":"Review changes"}]}}}`)
			}
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
			if fixtureDir != "" {
				data, _ := json.Marshal(params.MCPServers)
				_ = os.WriteFile(filepath.Join(fixtureDir, "mcp.json"), data, 0o600)
			}
			configResponse(message.ID)
			if message.Method == "session/new" {
				fmt.Println(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"fixture-session","update":{"sessionUpdate":"available_commands_update","availableCommands":[{"name":"review","description":"Review changes","input":{"hint":"focus area"}},{"name":"compact","description":"Compact history"}]}}}`)
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
			if code, err := strconv.Atoi(os.Getenv("KENN_FORGE_ACP_EXIT_ON_PROMPT")); err == nil {
				os.Exit(code)
			}
			promptID = message.ID
			fmt.Println(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"fixture-session","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"Hello "}}}}`)
			fmt.Println(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"fixture-session","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"workspace"}}}}`)
			if params.Prompt[0].Text == "delegate" {
				fmt.Println(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"fixture-session","update":{"sessionUpdate":"tool_call","toolCallId":"agent-1","title":"Explore the parser","kind":"think","status":"in_progress","_meta":{"claudeCode":{"toolName":"Agent"}}}}}`)
				fmt.Println(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"fixture-session","update":{"sessionUpdate":"tool_call","toolCallId":"child-1","title":"Read parser.go","kind":"read","status":"completed","_meta":{"claudeCode":{"toolName":"Read","parentToolUseId":"agent-1"}}}}}`)
				fmt.Println(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"fixture-session","update":{"sessionUpdate":"tool_call","toolCallId":"codex-1","title":"Start subagent reviewer","kind":"other","status":"in_progress","rawInput":{"agentThreadId":"thread-2","agentPath":"reviewer","activityKind":"started"}}}}`)
				fmt.Println(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"fixture-session","update":{"sessionUpdate":"tool_call_update","toolCallId":"agent-1","status":"completed"}}}`)
				fmt.Printf(`{"jsonrpc":"2.0","id":%s,"result":{"stopReason":"end_turn"}}`+"\n", promptID)
			} else if params.Prompt[0].Text == "elicit" {
				if !elicitationOffered {
					os.Exit(11)
				}
				fmt.Println(`{"jsonrpc":"2.0","id":"elicit","method":"elicitation/create","params":{"sessionId":"fixture-session","mode":"form","message":"Pick one","requestedSchema":{"type":"object","title":"Choice","properties":{"choice":{"type":"string","enum":["a","b"]}},"required":["choice"]}}}`)
			} else if params.Prompt[0].Text == "permission" {
				if fixtureDir != "" {
					go func() {
						ticker := time.NewTicker(10 * time.Millisecond)
						defer ticker.Stop()
						for range ticker.C {
							if _, err := os.Stat(filepath.Join(fixtureDir, "continue")); err == nil {
								break
							}
						}
						fmt.Println(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"fixture-session","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"output while detached"}}}}`)
						_ = os.WriteFile(filepath.Join(fixtureDir, "continued"), nil, 0o600)
					}()
				}
				fmt.Println(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"fixture-session","update":{"sessionUpdate":"tool_call","toolCallId":"edit","title":"Edit file","status":"pending","content":[{"type":"diff","path":"/tmp/example.txt","oldText":"before","newText":"after"}]}}}`)
				fmt.Println(`{"jsonrpc":"2.0","id":"approval","method":"session/request_permission","params":{"sessionId":"fixture-session","toolCall":{"toolCallId":"edit","title":"Edit file"},"options":[{"optionId":"allow","name":"Allow once","kind":"allow_once"},{"optionId":"deny","name":"Reject","kind":"reject_once"}]}}`)
			} else if params.Prompt[0].Text != "wait" {
				// Like Codex, report the thread going idle when a turn ends.
				promptThreadStatus("active")
				promptThreadStatus("idle")
				fmt.Printf(`{"jsonrpc":"2.0","id":%s,"result":{"stopReason":"end_turn"}}`+"\n", promptID)
			}
		case "_session/steering":
			var params struct {
				Prompt []struct {
					Text string `json:"text"`
				} `json:"prompt"`
				Meta struct {
					Steering struct {
						IdleBehavior string `json:"idleBehavior"`
					} `json:"steering"`
				} `json:"_meta"`
			}
			if json.Unmarshal(message.Params, &params) != nil || len(params.Prompt) != 1 || params.Meta.Steering.IdleBehavior != "promptRequired" {
				os.Exit(13)
			}
			switch params.Prompt[0].Text {
			case "adjust":
				fmt.Printf(`{"jsonrpc":"2.0","id":%s,"result":{"outcome":"injected"}}`+"\n", message.ID)
			case "finish":
				// The turn ends before the text is taken, so Forge must send it.
				fmt.Printf(`{"jsonrpc":"2.0","id":%s,"result":{"outcome":"promptRequired"}}`+"\n", message.ID)
				fmt.Printf(`{"jsonrpc":"2.0","id":%s,"result":{"stopReason":"end_turn"}}`+"\n", promptID)
			case "takeover":
				// The agent starts and owns a new turn; the original prompt's
				// completion arrives after the steering answer.
				fmt.Printf(`{"jsonrpc":"2.0","id":%s,"result":{"outcome":"startedNewTurn"}}`+"\n", message.ID)
				time.Sleep(20 * time.Millisecond)
				fmt.Printf(`{"jsonrpc":"2.0","id":%s,"result":{"stopReason":"end_turn"}}`+"\n", promptID)
				if os.Getenv("KENN_FORGE_ACP_LATE_THREAD_STATUS") == "1" {
					// The first status arrives only after the prompt completes.
					time.Sleep(50 * time.Millisecond)
				}
				if os.Getenv("KENN_FORGE_ACP_TAKEOVER_IDLE_ONLY") != "1" {
					threadStatus("active")
				}
				go func() {
					time.Sleep(300 * time.Millisecond)
					threadStatus("idle")
				}()
			default:
				fmt.Printf(`{"jsonrpc":"2.0","id":%s,"result":{"outcome":"failed"}}`+"\n", message.ID)
			}
		case "session/cancel":
			fmt.Printf(`{"jsonrpc":"2.0","id":%s,"result":{"stopReason":"cancelled"}}`+"\n", promptID)
		case "":
			if string(message.ID) == `"elicit"` {
				var result struct {
					Action  string `json:"action"`
					Content struct {
						Choice string `json:"choice"`
					} `json:"content"`
				}
				if json.Unmarshal(message.Result, &result) != nil || result.Action != "accept" {
					os.Exit(12)
				}
				fmt.Printf(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"fixture-session","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"chose %s"}}}}`+"\n", result.Content.Choice)
				fmt.Printf(`{"jsonrpc":"2.0","id":%s,"result":{"stopReason":"end_turn"}}`+"\n", promptID)
				continue
			}
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
	manager := newACPTestManager(t, Options{Targets: targets})
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

// The owner is the durable reporter: activity must follow the ACP turn
// signals without any hook, and questions must block the turn until answered.
func TestACPReportsActivityCommandsAndElicitation(t *testing.T) {
	t.Setenv("KENN_FORGE_LOCALRUNTIME_HELPER", "1")
	t.Setenv("KENN_FORGE_ACP_FIXTURE", "1")
	executable, err := os.Executable()
	require.NoError(t, err)
	targets := ResolveLaunchTargets([]config.Agent{{Key: "chat", Label: "Chat", Protocol: "acp", Command: []string{executable, "-test.run=^TestACPStdioHelper$"}}}, nil, nil)
	activityDir := t.TempDir()
	manager := newACPTestManager(t, Options{Targets: targets, AgentActivityDir: activityDir})
	activity := agentactivity.NewStore(activityDir)
	cwd, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	info, err := manager.Launch(t.Context(), "workspace", cwd, "chat")
	require.NoError(t, err)
	agent, err := manager.ACP("workspace", info.Key)
	require.NoError(t, err)
	reportState := func() agentactivity.State {
		reports := activity.LiveReportsForWorkspace(cwd, []string{info.Key})
		if len(reports) != 1 {
			return ""
		}
		assert.Equal(t, ACPActivityAgent, reports[0].Agent)
		assert.Equal(t, "fixture-session", reports[0].SessionID)
		return reports[0].State
	}
	var state ACPState
	// Conditions run off the test goroutine, so they must not call require.
	snapshot := func() ACPState {
		var current ACPState
		data, err := agent.Snapshot()
		if err == nil {
			err = json.Unmarshal(data, &current)
		}
		if err != nil {
			current.Error = err.Error()
		}
		return current
	}
	require.Eventually(t, func() bool { return reportState() == agentactivity.StateIdle }, 5*time.Second, 10*time.Millisecond)
	require.Eventually(t, func() bool { state = snapshot(); return len(state.Commands) == 2 }, 5*time.Second, 10*time.Millisecond)
	assert.Equal(t, []ACPCommandInfo{
		{Name: "review", Description: "Review changes", InputHint: "focus area"},
		{Name: "compact", Description: "Compact history"},
	}, state.Commands)

	require.NoError(t, manager.SubmitAgentMessage(t.Context(), "workspace", info.Key, "elicit"))
	require.Eventually(t, func() bool { state = snapshot(); return len(state.Elicitations) == 1 }, 5*time.Second, 10*time.Millisecond)
	assert.Equal(t, "Pick one", state.Elicitations[0].Message)
	assert.Equal(t, "Choice", state.Elicitations[0].Schema.Title)
	assert.Equal(t, []string{"choice"}, state.Elicitations[0].Schema.Required)
	assert.Contains(t, state.Elicitations[0].Schema.Properties, "choice")
	require.Eventually(t, func() bool { return reportState() == agentactivity.StateInput }, 5*time.Second, 10*time.Millisecond)
	require.ErrorContains(t, agent.Command(ACPCommand{Type: "elicitation", ID: state.Elicitations[0].ID, Action: "maybe"}), "accept, decline, or cancel")
	require.NoError(t, agent.Command(ACPCommand{Type: "elicitation", ID: state.Elicitations[0].ID, Action: "accept", Content: jsontext.Value(`{"choice":"b"}`)}))
	require.Eventually(t, func() bool {
		state = snapshot()
		return !state.Busy && len(state.Elicitations) == 0 && len(state.Messages) > 0 && strings.HasSuffix(state.Messages[len(state.Messages)-1].Text, "chose b")
	}, 5*time.Second, 10*time.Millisecond)
	require.Eventually(t, func() bool { return reportState() == agentactivity.StateDone }, 5*time.Second, 10*time.Millisecond)

	require.NoError(t, manager.SubmitAgentMessage(t.Context(), "workspace", info.Key, "wait"))
	require.Eventually(t, func() bool { return reportState() == agentactivity.StateWorking }, 5*time.Second, 10*time.Millisecond)
	// A running turn never rejects input: the follow-up waits in the queue.
	require.NoError(t, manager.SubmitAgentMessage(t.Context(), "workspace", info.Key, "while busy"))
	state = snapshot()
	require.Len(t, state.Queue, 1)
	assert.Equal(t, "while busy", state.Queue[0].Text)
	require.NoError(t, agent.Command(ACPCommand{Type: "cancel"}))
	require.Eventually(t, func() bool { return reportState() == agentactivity.StateDone }, 5*time.Second, 10*time.Millisecond)
	state = snapshot()
	assert.True(t, state.QueuePaused, "stopping a turn must not start queued work")
	require.Len(t, state.Queue, 1)
	require.NoError(t, agent.Command(ACPCommand{Type: "resume"}))
	require.Eventually(t, func() bool {
		state = snapshot()
		return !state.Busy && len(state.Queue) == 0 && !state.QueuePaused &&
			slices.ContainsFunc(state.Messages, func(m ACPMessage) bool { return m.Role == "user" && m.Text == "while busy" })
	}, 5*time.Second, 10*time.Millisecond)

	require.NoError(t, manager.Stop(context.Background(), "workspace", info.Key))
	require.Eventually(t, func() bool {
		return len(activity.LiveReportsForWorkspace(cwd, []string{info.Key})) == 0
	}, 5*time.Second, 10*time.Millisecond)
}

func TestACPSteersAndDrainsQueuedPrompts(t *testing.T) {
	t.Setenv("KENN_FORGE_LOCALRUNTIME_HELPER", "1")
	t.Setenv("KENN_FORGE_ACP_FIXTURE", "1")
	t.Setenv("KENN_FORGE_ACP_STEERING", "1")
	executable, err := os.Executable()
	require.NoError(t, err)
	targets := ResolveLaunchTargets([]config.Agent{{Key: "chat", Label: "Chat", Protocol: "acp", Command: []string{executable, "-test.run=^TestACPStdioHelper$"}}}, nil, nil)
	manager := newACPTestManager(t, Options{Targets: targets})
	cwd, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	info, err := manager.Launch(t.Context(), "workspace", cwd, "chat")
	require.NoError(t, err)
	agent, err := manager.ACP("workspace", info.Key)
	require.NoError(t, err)
	snapshot := func() ACPState {
		var state ACPState
		data, err := agent.Snapshot()
		if err == nil {
			_ = json.Unmarshal(data, &state)
		}
		return state
	}
	userTexts := func(state ACPState) []string {
		var texts []string
		for _, message := range state.Messages {
			if message.Role == "user" {
				texts = append(texts, message.Text)
			}
		}
		return texts
	}
	require.True(t, snapshot().SteeringSupported)

	require.NoError(t, agent.Command(ACPCommand{Type: "prompt", Text: "wait", ID: "turn"}))
	require.NoError(t, agent.Command(ACPCommand{Type: "prompt", Mode: "queue", Text: "second", ID: "queued"}))
	require.NoError(t, agent.Command(ACPCommand{Type: "prompt", Mode: "queue", Text: "second", ID: "queued"}), "a retried submission is idempotent")
	require.NoError(t, agent.Command(ACPCommand{Type: "prompt", Mode: "steer", Text: "adjust", ID: "steer-1"}))
	state := snapshot()
	assert.True(t, state.Busy, "an injected steer belongs to the running turn")
	assert.Equal(t, []string{"wait", "adjust"}, userTexts(state))
	assert.Equal(t, []ACPQueuedPrompt{{ID: "queued", Text: "second"}}, state.Queue)
	require.ErrorContains(t, agent.Command(ACPCommand{Type: "prompt", Mode: "steer", Text: "refuse", ID: "steer-2"}), "did not accept")
	assert.True(t, snapshot().QueuePaused, "a refused steer must not let queued work run on")
	require.NoError(t, agent.Command(ACPCommand{Type: "resume"}))

	// The turn ends before "finish" is taken; it runs next, ahead of the queue.
	require.NoError(t, agent.Command(ACPCommand{Type: "prompt", Mode: "steer", Text: "finish", ID: "steer-3"}))
	require.Eventually(t, func() bool {
		state = snapshot()
		return !state.Busy && len(state.Queue) == 0 && len(userTexts(state)) == 4
	}, 5*time.Second, 10*time.Millisecond)
	assert.Equal(t, []string{"wait", "adjust", "finish", "second"}, userTexts(state))

	require.NoError(t, agent.Command(ACPCommand{Type: "prompt", Text: "wait", ID: "turn-2"}))
	require.NoError(t, agent.Command(ACPCommand{Type: "prompt", Text: "dropped", ID: "queued-2"}))
	require.NoError(t, agent.Command(ACPCommand{Type: "unqueue", ID: "queued-2"}))
	require.Error(t, agent.Command(ACPCommand{Type: "unqueue", ID: "queued-2"}))
	assert.Empty(t, snapshot().Queue)
	require.NoError(t, agent.Command(ACPCommand{Type: "cancel"}))
	require.Eventually(t, func() bool { return !snapshot().Busy }, 5*time.Second, 10*time.Millisecond)

	// A turn the agent starts after a steer keeps the chat busy, and queued
	// work waits until the agent reports its thread idle.
	require.NoError(t, agent.Command(ACPCommand{Type: "prompt", Text: "wait", ID: "turn-3"}))
	require.NoError(t, agent.Command(ACPCommand{Type: "prompt", Mode: "steer", Text: "takeover", ID: "steer-4"}))
	require.NoError(t, agent.Command(ACPCommand{Type: "prompt", Text: "after takeover", ID: "queued-3"}))
	time.Sleep(100 * time.Millisecond)
	state = snapshot()
	assert.True(t, state.Busy, "the agent's own turn is still running")
	assert.Equal(t, []ACPQueuedPrompt{{ID: "queued-3", Text: "after takeover"}}, state.Queue)
	require.Eventually(t, func() bool {
		state = snapshot()
		return !state.Busy && len(state.Queue) == 0 && slices.Contains(userTexts(state), "after takeover")
	}, 5*time.Second, 10*time.Millisecond)
	texts := userTexts(state)
	assert.Equal(t, []string{"wait", "takeover", "after takeover"}, texts[len(texts)-3:])
}

// An agent that never reports thread status gives no signal for when a turn
// it started after a steer ends, so that turn must not keep the chat busy.
func TestACPSteerTakeoverWithoutThreadStatusStillDrains(t *testing.T) {
	t.Setenv("KENN_FORGE_LOCALRUNTIME_HELPER", "1")
	t.Setenv("KENN_FORGE_ACP_FIXTURE", "1")
	t.Setenv("KENN_FORGE_ACP_STEERING", "1")
	t.Setenv("KENN_FORGE_ACP_NO_THREAD_STATUS", "1")
	executable, err := os.Executable()
	require.NoError(t, err)
	targets := ResolveLaunchTargets([]config.Agent{{Key: "chat", Label: "Chat", Protocol: "acp", Command: []string{executable, "-test.run=^TestACPStdioHelper$"}}}, nil, nil)
	manager := newACPTestManager(t, Options{Targets: targets})
	cwd, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	info, err := manager.Launch(t.Context(), "workspace", cwd, "chat")
	require.NoError(t, err)
	agent, err := manager.ACP("workspace", info.Key)
	require.NoError(t, err)

	require.NoError(t, agent.Command(ACPCommand{Type: "prompt", Text: "wait", ID: "turn"}))
	require.NoError(t, agent.Command(ACPCommand{Type: "prompt", Mode: "steer", Text: "takeover", ID: "steer"}))
	require.NoError(t, agent.Command(ACPCommand{Type: "prompt", Text: "after takeover", ID: "queued"}))
	var state ACPState
	require.Eventually(t, func() bool {
		data, err := agent.Snapshot()
		if err != nil || json.Unmarshal(data, &state) != nil || state.Busy || len(state.Queue) != 0 {
			return false
		}
		return slices.ContainsFunc(state.Messages, func(message ACPMessage) bool { return message.Text == "after takeover" })
	}, 5*time.Second, 10*time.Millisecond)
}

// A status-reporting agent whose takeover turn reports only idle, with no
// fresh active, still ends that turn once the original prompt completed.
func TestACPSteerTakeoverEndsOnIdleWithoutActive(t *testing.T) {
	t.Setenv("KENN_FORGE_LOCALRUNTIME_HELPER", "1")
	t.Setenv("KENN_FORGE_ACP_FIXTURE", "1")
	t.Setenv("KENN_FORGE_ACP_STEERING", "1")
	t.Setenv("KENN_FORGE_ACP_TAKEOVER_IDLE_ONLY", "1")
	executable, err := os.Executable()
	require.NoError(t, err)
	targets := ResolveLaunchTargets([]config.Agent{{Key: "chat", Label: "Chat", Protocol: "acp", Command: []string{executable, "-test.run=^TestACPStdioHelper$"}}}, nil, nil)
	manager := newACPTestManager(t, Options{Targets: targets})
	cwd, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	info, err := manager.Launch(t.Context(), "workspace", cwd, "chat")
	require.NoError(t, err)
	agent, err := manager.ACP("workspace", info.Key)
	require.NoError(t, err)
	snapshot := func() ACPState {
		var state ACPState
		if data, err := agent.Snapshot(); err == nil {
			_ = json.Unmarshal(data, &state)
		}
		return state
	}

	// An earlier turn shows the agent reports thread status.
	require.NoError(t, agent.Command(ACPCommand{Type: "prompt", Text: "hello", ID: "first"}))
	require.Eventually(t, func() bool { return !snapshot().Busy }, 5*time.Second, 10*time.Millisecond)
	require.NoError(t, agent.Command(ACPCommand{Type: "prompt", Text: "wait", ID: "turn"}))
	require.NoError(t, agent.Command(ACPCommand{Type: "prompt", Mode: "steer", Text: "takeover", ID: "steer"}))
	require.NoError(t, agent.Command(ACPCommand{Type: "prompt", Text: "after takeover", ID: "queued"}))
	time.Sleep(100 * time.Millisecond)
	state := snapshot()
	assert.True(t, state.Busy, "the agent's own turn is still running")
	assert.Equal(t, []ACPQueuedPrompt{{ID: "queued", Text: "after takeover"}}, state.Queue)
	require.Eventually(t, func() bool {
		state := snapshot()
		return !state.Busy && len(state.Queue) == 0 && slices.ContainsFunc(state.Messages, func(message ACPMessage) bool { return message.Text == "after takeover" })
	}, 5*time.Second, 10*time.Millisecond)
}

// An agent whose first thread status arrives only after the steer answered,
// and even after the original prompt completed, still has its takeover turn
// tracked: the chat is busy again until the thread goes idle.
func TestACPSteerTakeoverTrackedByLateThreadStatus(t *testing.T) {
	t.Setenv("KENN_FORGE_LOCALRUNTIME_HELPER", "1")
	t.Setenv("KENN_FORGE_ACP_FIXTURE", "1")
	t.Setenv("KENN_FORGE_ACP_STEERING", "1")
	t.Setenv("KENN_FORGE_ACP_LATE_THREAD_STATUS", "1")
	executable, err := os.Executable()
	require.NoError(t, err)
	targets := ResolveLaunchTargets([]config.Agent{{Key: "chat", Label: "Chat", Protocol: "acp", Command: []string{executable, "-test.run=^TestACPStdioHelper$"}}}, nil, nil)
	manager := newACPTestManager(t, Options{Targets: targets})
	cwd, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	info, err := manager.Launch(t.Context(), "workspace", cwd, "chat")
	require.NoError(t, err)
	agent, err := manager.ACP("workspace", info.Key)
	require.NoError(t, err)
	snapshot := func() ACPState {
		var state ACPState
		if data, err := agent.Snapshot(); err == nil {
			_ = json.Unmarshal(data, &state)
		}
		return state
	}

	require.NoError(t, agent.Command(ACPCommand{Type: "prompt", Text: "wait", ID: "turn"}))
	require.NoError(t, agent.Command(ACPCommand{Type: "prompt", Mode: "steer", Text: "takeover", ID: "steer"}))
	require.Eventually(t, func() bool { return !snapshot().Busy }, 5*time.Second, time.Millisecond, "the prompt completes first")
	require.Eventually(t, func() bool { return snapshot().Busy }, 5*time.Second, time.Millisecond, "the late status claims the takeover")
	require.NoError(t, agent.Command(ACPCommand{Type: "prompt", Text: "after takeover", ID: "queued"}))
	assert.Equal(t, []ACPQueuedPrompt{{ID: "queued", Text: "after takeover"}}, snapshot().Queue, "queued work waits for the agent's turn")
	require.Eventually(t, func() bool {
		state := snapshot()
		return !state.Busy && len(state.Queue) == 0 && slices.ContainsFunc(state.Messages, func(message ACPMessage) bool { return message.Text == "after takeover" })
	}, 5*time.Second, 10*time.Millisecond)
}

func TestACPMarksSubagentToolCalls(t *testing.T) {
	t.Setenv("KENN_FORGE_LOCALRUNTIME_HELPER", "1")
	t.Setenv("KENN_FORGE_ACP_FIXTURE", "1")
	executable, err := os.Executable()
	require.NoError(t, err)
	targets := ResolveLaunchTargets([]config.Agent{{Key: "chat", Label: "Chat", Protocol: "acp", Command: []string{executable, "-test.run=^TestACPStdioHelper$"}}}, nil, nil)
	manager := newACPTestManager(t, Options{Targets: targets})
	cwd, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	info, err := manager.Launch(t.Context(), "workspace", cwd, "chat")
	require.NoError(t, err)
	agent, err := manager.ACP("workspace", info.Key)
	require.NoError(t, err)
	require.NoError(t, agent.Prompt("delegate"))
	tools := map[string]ACPMessage{}
	require.Eventually(t, func() bool {
		data, err := agent.Snapshot()
		var state ACPState
		if err != nil || json.Unmarshal(data, &state) != nil || state.Busy {
			return false
		}
		for _, message := range state.Messages {
			if message.Role == "tool" {
				tools[message.ToolCallID] = message
			}
		}
		return len(tools) == 3
	}, 5*time.Second, 10*time.Millisecond)
	assert.True(t, tools["agent-1"].Subagent)
	assert.Equal(t, "completed", tools["agent-1"].Status, "a status update must not drop the subagent marker")
	assert.False(t, tools["child-1"].Subagent)
	assert.Equal(t, "agent-1", tools["child-1"].ParentToolCallID)
	assert.True(t, tools["codex-1"].Subagent)
}

func TestACPConnectionProbe(t *testing.T) {
	manager := NewManager(Options{})
	t.Setenv("KENN_FORGE_LOCALRUNTIME_HELPER", "1")
	t.Setenv("KENN_FORGE_ACP_FIXTURE", "1")
	executable, err := os.Executable()
	require.NoError(t, err)
	command := []string{executable, "-test.run=^TestACPStdioHelper$"}
	warning, err := manager.TestACP(t.Context(), command)
	require.NoError(t, err)
	assert.Empty(t, warning)
	_, err = manager.TestACP(t.Context(), nil)
	require.ErrorContains(t, err, "executable is required")
	_, err = manager.TestACP(t.Context(), []string{filepath.Join(t.TempDir(), "missing-agent")})
	require.Error(t, err)
	withMCP := NewManager(Options{AgentMCPURL: "http://127.0.0.1:12345/agent-mcp", AgentMCPToken: "fixture-token"})
	fixtureDir := t.TempDir()
	t.Setenv("KENN_FORGE_ACP_CONTINUE_DIR", fixtureDir)
	t.Setenv("KENN_FORGE_ACP_NO_HTTP", "1")
	warning, err = withMCP.TestACP(t.Context(), command)
	require.NoError(t, err, "Forge MCP tools are optional for an agent without HTTP MCP support")
	assert.Contains(t, warning, "Forge tools are unavailable")
	offered, err := os.ReadFile(filepath.Join(fixtureDir, "mcp.json"))
	require.NoError(t, err)
	assert.JSONEq(t, `[]`, string(offered), "an agent without HTTP MCP support must not be offered HTTP servers")
	degraded, err := startACPSession(t.Context(), command, t.TempDir(), nil, withMCP.agentMCPServers(), nil)
	require.NoError(t, err)
	state := publishedACPState(t, degraded)
	require.NoError(t, degraded.Stop(context.Background()))
	assert.Equal(t, []string{acpNoHTTPMCPNotice}, state.Notices)
	assert.Empty(t, state.Error, "a usable agent without Forge tools is not in an error state")
	t.Setenv("KENN_FORGE_ACP_NO_HTTP", "")
	t.Setenv("KENN_FORGE_ACP_CONTINUE_DIR", "")
	warning, err = withMCP.TestACP(t.Context(), command)
	require.NoError(t, err)
	assert.Empty(t, warning)
	t.Setenv("KENN_FORGE_ACP_BAD_VERSION", "1")
	_, err = manager.TestACP(t.Context(), command)
	require.ErrorContains(t, err, "unsupported ACP protocol version")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = manager.TestACP(ctx, command)
	require.ErrorIs(t, err, context.Canceled)
}

func TestACPRemembersSettingsPerClientAndHost(t *testing.T) {
	t.Setenv("KENN_FORGE_LOCALRUNTIME_HELPER", "1")
	t.Setenv("KENN_FORGE_ACP_FIXTURE", "1")
	executable, err := os.Executable()
	require.NoError(t, err)
	command := []string{executable, "-test.run=^TestACPStdioHelper$"}
	targets := ResolveLaunchTargets([]config.Agent{{Key: "chat", Protocol: "acp", Command: command}, {Key: "other", Protocol: "acp", Command: command}}, nil, nil)
	preferences := filepath.Join(t.TempDir(), "preferences.json")
	first := newACPTestManager(t, Options{Targets: targets, ACPPreferencesPath: preferences})
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
			manager := newACPTestManager(t, Options{Targets: targets, ACPPreferencesPath: tc.path})
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

func TestACPPromptPrecedesConcurrentOutput(t *testing.T) {
	reader, writer := io.Pipe()
	agent := &ACP{
		stdin: writer, done: make(chan struct{}),
		subscribers: make(map[chan struct{}]struct{}),
		state:       ACPState{Connected: true, Messages: []ACPMessage{{Role: "assistant", Text: "old answer"}}},
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
	// The unfinished answer is held until the agent goes quiet.
	var state ACPState
	require.Eventually(t, func() bool {
		data, err := agent.Snapshot()
		return err == nil && json.Unmarshal(data, &state) == nil && len(state.Messages) == 3
	}, 5*time.Second, 10*time.Millisecond)
	assert.Equal(t, "old answer", state.Messages[0].Text)
	assert.Equal(t, "new question", state.Messages[1].Text)
	assert.Equal(t, "new-submission", state.Messages[1].SubmissionID)
	assert.Equal(t, "new answer", state.Messages[2].Text)
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

func TestACPStopTrustsWaiterWhenKillFails(t *testing.T) {
	newAgent := func() *ACP {
		stdinReader, stdinWriter := io.Pipe()
		stdoutReader, stdoutWriter := io.Pipe()
		t.Cleanup(func() {
			_ = stdinReader.Close()
			_ = stdoutWriter.Close()
		})
		return &ACP{
			cmd: &exec.Cmd{}, stdin: stdinWriter, stdout: stdoutReader, done: make(chan struct{}),
			kill: func(*os.Process) error { return errors.New("TerminateProcess: Access is denied.") },
		}
	}

	exited := newAgent()
	close(exited.done)
	require.NoError(t, exited.Stop(t.Context()), "a confirmed exit is a successful stop")

	pending := newAgent()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	require.ErrorIs(t, pending.Stop(ctx), context.Canceled)
}

func TestACPReportsNaturalExitCode(t *testing.T) {
	t.Setenv("KENN_FORGE_LOCALRUNTIME_HELPER", "1")
	t.Setenv("KENN_FORGE_ACP_FIXTURE", "1")
	t.Setenv("KENN_FORGE_ACP_EXIT_ON_PROMPT", "7")
	executable, err := os.Executable()
	require.NoError(t, err)
	exits := make(chan SessionInfo, 1)
	targets := ResolveLaunchTargets([]config.Agent{{Key: "chat", Protocol: "acp", Command: []string{executable, "-test.run=^TestACPStdioHelper$"}}}, nil, nil)
	manager := newACPTestManager(t, Options{Targets: targets, OnSessionExit: func(info SessionInfo) { exits <- info }})
	info, err := manager.Launch(t.Context(), "workspace", t.TempDir(), "chat")
	require.NoError(t, err)
	agent, err := manager.ACP("workspace", info.Key)
	require.NoError(t, err)
	// Exit only after attachment, so this checks exit reporting rather than
	// racing the owner startup against a fixture timer.
	require.NoError(t, agent.Command(ACPCommand{Type: "prompt", Text: "exit"}))
	select {
	case info := <-exits:
		require.NotNil(t, info.ExitCode)
		assert.Equal(t, 7, *info.ExitCode)
	case <-time.After(5 * time.Second):
		require.FailNow(t, "ACP exit was not reported")
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
	manager := newACPTestManager(t, Options{Targets: targets, ACPPreferencesPath: preferences})
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
	manager := newACPTestManager(t, Options{Targets: targets, ACPPreferencesPath: filepath.Join(t.TempDir(), "preferences.json")})
	firstInfo, err := manager.Launch(t.Context(), "first", t.TempDir(), "chat")
	require.NoError(t, err)
	secondInfo, err := manager.Launch(t.Context(), "second", t.TempDir(), "chat")
	require.NoError(t, err)
	first, err := manager.ACP("first", firstInfo.Key)
	require.NoError(t, err)
	second, err := manager.ACP("second", secondInfo.Key)
	require.NoError(t, err)
	configured := make(chan error, 2)
	go func() { configured <- first.Command(ACPCommand{Type: "config", ID: "model", Value: "deep"}) }()
	go func() { configured <- second.Command(ACPCommand{Type: "config", ID: "effort", Value: "high"}) }()
	require.NoError(t, <-configured)
	require.NoError(t, <-configured)

	thirdInfo, err := manager.Launch(t.Context(), "third", t.TempDir(), "chat")
	require.NoError(t, err)
	third, err := manager.ACP("third", thirdInfo.Key)
	require.NoError(t, err)
	assert.Equal(t, "deep", acpOptionValue(t, third, "model"))
	assert.Equal(t, "high", acpOptionValue(t, third, "effort"))
}

func acpOptionValue(t *testing.T, agent ACPChat, id string) string {
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

func TestACPOwnerHelper(t *testing.T) {
	if os.Getenv("KENN_FORGE_ACP_OWNER_FIXTURE") != "1" {
		return
	}
	err := RunACPOwner(context.Background(), os.Args[len(os.Args)-1])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}

func newACPTestManager(t *testing.T, options Options) *Manager {
	t.Helper()
	t.Setenv("KENN_FORGE_ACP_OWNER_FIXTURE", "1")
	executable, err := os.Executable()
	require.NoError(t, err)
	if options.ACPSessionsDir == "" {
		options.ACPSessionsDir = filepath.Join(t.TempDir(), "acp")
	}
	options.ACPOwnerCommand = []string{executable, "-test.run=^TestACPOwnerHelper$", "--"}
	// A nil tmux command in ResolveLaunchTargets discovers the live default
	// server. ACP tests must select a private server explicitly or use ptyowner.
	options.Targets = slices.Clone(options.Targets)
	for i := range options.Targets {
		if options.Targets[i].Kind == LaunchTargetShell && len(options.TmuxCommand) == 0 {
			options.Targets[i].Available = false
		}
	}
	if options.PtyOwnerRuntime == nil {
		options = withTestPtyOwnerRuntime(t, options)
	}
	manager := NewManager(options)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		paths, err := filepath.Glob(filepath.Join(options.ACPSessionsDir, "*", "config.json"))
		require.NoError(t, err)
		for _, path := range paths {
			data, err := os.ReadFile(path)
			require.NoError(t, err)
			var cfg acpOwnerConfig
			require.NoError(t, json.Unmarshal(data, &cfg))
			err = manager.Stop(ctx, cfg.Info.WorkspaceID, cfg.Info.Key)
			if errors.Is(err, ErrSessionNotFound) {
				err = manager.StopDormantACP(ctx, cfg.Info.WorkspaceID, cfg.Info.Key)
			}
			assert.NoError(t, err)
			if len(options.TmuxCommand) > 0 {
				assert.NoError(t, manager.killTmuxSession(ctx, tmuxSessionName(cfg.Info.WorkspaceID, cfg.Info.Key)))
			}
			assert.NoError(t, options.PtyOwnerRuntime.Stop(ctx, cfg.Info.Key))
		}
		manager.Shutdown()
	})
	return manager
}

// The owner keeps the whole transcript. Updates carry the recent window, and
// earlier messages page in by absolute index.
func TestACPSnapshotWindowsAndHistoryPagesTheWholeTranscript(t *testing.T) {
	messages := make([]ACPMessage, 250)
	for i := range messages {
		messages[i] = ACPMessage{Role: "assistant", Text: fmt.Sprintf("m%d", i)}
	}
	agent := &ACP{state: ACPState{Messages: messages}}

	data, err := agent.Snapshot()
	require.NoError(t, err)
	var state ACPState
	require.NoError(t, json.Unmarshal(data, &state))
	assert.Equal(t, 250, state.MessageCount)
	assert.Equal(t, 50, state.MessageOffset)
	require.Len(t, state.Messages, acpMessageWindow)
	assert.Equal(t, "m50", state.Messages[0].Text)
	assert.Equal(t, "m249", state.Messages[len(state.Messages)-1].Text)
	assert.Len(t, agent.state.Messages, 250, "the owner must keep every message")

	page := func(before, limit int) ACPHistory {
		t.Helper()
		data, err := agent.History(before, limit)
		require.NoError(t, err)
		var frame map[string]ACPHistory
		require.NoError(t, json.Unmarshal(data, &frame))
		return frame["history"]
	}
	earlier := page(state.MessageOffset, 0)
	assert.Equal(t, 0, earlier.Offset)
	require.Len(t, earlier.Messages, 50)
	assert.Equal(t, "m0", earlier.Messages[0].Text)
	assert.Equal(t, "m49", earlier.Messages[49].Text)

	middle := page(200, 30)
	assert.Equal(t, 170, middle.Offset)
	require.Len(t, middle.Messages, 30)
	assert.Equal(t, "m170", middle.Messages[0].Text)

	assert.Equal(t, 250-acpHistoryPage, page(1000, 0).Offset, "a past-the-end cursor pages from the end")
	assert.Empty(t, page(-5, 10).Messages)
}

// A message that is still entirely held is not part of the published
// transcript, so the count and pages agree with what clients have seen.
func TestACPWindowCountsOnlyPublishedMessages(t *testing.T) {
	agent := &ACP{state: ACPState{Messages: []ACPMessage{{Role: "user", Text: "question"}, {Role: "assistant", Text: "held"}}}, heldBytes: len("held")}
	data, err := agent.Snapshot()
	require.NoError(t, err)
	var state ACPState
	require.NoError(t, json.Unmarshal(data, &state))
	assert.Equal(t, 1, state.MessageCount)
	assert.Equal(t, 0, state.MessageOffset)
	require.Len(t, state.Messages, 1)

	data, err = agent.History(5, 10)
	require.NoError(t, err)
	var frame map[string]ACPHistory
	require.NoError(t, json.Unmarshal(data, &frame))
	require.Len(t, frame["history"].Messages, 1)
	assert.Equal(t, "question", frame["history"].Messages[0].Text)
}

// A stopped or failed prompt ends a takeover turn too, instead of waiting for
// an idle thread status that may never come.
func TestACPStoppedPromptEndsATakeoverTurn(t *testing.T) {
	for name, result := range map[string]acpTurnResult{
		"cancelled": {stopReason: acpsdk.StopReasonCancelled},
		"failed":    {err: errors.New("agent failed")},
	} {
		t.Run(name, func(t *testing.T) {
			agent := newDetachedACP(t)
			agent.state.Busy, agent.state.Stopping = true, true
			agent.state.Queue = []ACPQueuedPrompt{{ID: "queued", Text: "next"}}
			agent.external = &acpExternalTurn{}
			completed := make(chan acpTurnResult, 1)
			completed <- result
			agent.finishTurn(completed)

			agent.mu.Lock()
			defer agent.mu.Unlock()
			assert.Nil(t, agent.external)
			assert.False(t, agent.state.Busy)
			assert.False(t, agent.state.Stopping)
			assert.True(t, agent.state.QueuePaused, "queued work still waits after a stop")
			assert.Len(t, agent.state.Queue, 1)
		})
	}
}

func TestACPRestoredImageQueuePausesWhenAgentDoesNotSupportImages(t *testing.T) {
	assert := assert.New(t)
	agent := newDetachedACP(t)
	var written bytes.Buffer
	agent.stdin = discardWriteCloser{&written}
	queued := ACPQueuedPrompt{ID: "saved-image", Images: []ACPContent{{Type: "image", MimeType: "image/png", Data: "aW1hZ2U="}}}
	agent.restoreTranscriptLocked(ACPState{Queue: []ACPQueuedPrompt{queued}})
	// Resume the restored queue against the current agent's capabilities.
	agent.state.QueuePaused = false
	agent.drain()

	state := publishedACPState(t, agent)
	assert.True(state.QueuePaused)
	assert.Equal([]ACPQueuedPrompt{queued}, state.Queue)
	assert.Contains(state.Error, "does not accept image prompts")
	assert.False(state.Busy)
	assert.Empty(written.String(), "unsupported images must not reach the agent")
}

func TestACPImagePrompts(t *testing.T) {
	for _, mode := range []string{"send", "queue", "steer"} {
		t.Run(mode, func(t *testing.T) {
			assert := assert.New(t)
			peerReader, peerWriter := io.Pipe()
			t.Cleanup(func() { _ = peerWriter.Close(); _ = peerReader.Close() })
			var written bytes.Buffer
			agent := &ACP{imagesSupported: true, stdin: testWriteCloser{Writer: &written}, done: make(chan struct{}), subscribers: make(map[chan struct{}]struct{}), state: ACPState{Connected: true}}
			agent.client = acpsdk.NewClientSideConnection(agent, agent, peerReader)
			var command ACPCommand
			require.NoError(t, json.Unmarshal([]byte(`{"type":"prompt","id":"image-prompt","images":[{"type":"image","mimeType":"image/png","data":"aW1hZ2U=","name":"screenshot.png"}]}`), &command))
			command.Mode = mode
			if mode == "queue" {
				agent.state.Busy = true
			}
			if mode == "steer" {
				agent.state.Busy = true
				agent.state.SteeringSupported = true
				hostReader, hostWriter := io.Pipe()
				agent.stdin = hostWriter
				t.Cleanup(func() { _ = hostReader.Close(); _ = hostWriter.Close() })
				received := make(chan []byte, 1)
				go func() {
					line, _ := bufio.NewReader(hostReader).ReadBytes('\n')
					var request struct {
						ID jsontext.Value `json:"id"`
					}
					_ = json.Unmarshal(line, &request)
					_, _ = fmt.Fprintf(peerWriter, `{"jsonrpc":"2.0","id":%s,"result":{"outcome":"injected"}}`+"\n", request.ID)
					received <- line
				}()
				require.NoError(t, agent.Command(command))
				assert.Contains(string(<-received), `"method":"_session/steering"`)
			} else {
				require.NoError(t, agent.Command(command))
				if mode == "queue" {
					require.Len(t, agent.state.Queue, 1)
					assert.Equal(command.Images, agent.state.Queue[0].Images)
					agent.mu.Lock()
					agent.state.Busy = false
					agent.mu.Unlock()
					agent.drain()
				}
				assert.Contains(written.String(), `"type":"image"`)
				assert.Contains(written.String(), `"data":"aW1hZ2U="`)
			}
			require.Len(t, agent.state.Messages, 1)
			assert.Equal(command.Images, agent.state.Messages[0].Images)
			require.NoError(t, agent.Command(command), "the identical image retry is acknowledged")
			changed := command
			changed.Images = []ACPContent{{Type: "image", MimeType: "image/png", Data: "bmV3"}}
			require.ErrorContains(t, agent.Command(changed), "submission ID already belongs to another message")
			close(agent.done)
		})
	}
}
