// Package acpfixture provides the ACP subprocess used by runtime tests.
package acpfixture

import (
	"bufio"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// recordFixtureResponse logs each answer the fixture receives, so tests can
// tell how many reached the agent.
func recordFixtureResponse(dir, response string) {
	if dir == "" {
		return
	}
	file, err := os.OpenFile(filepath.Join(dir, "responses"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		os.Exit(14)
	}
	_, _ = fmt.Fprintln(file, response)
	_ = file.Close()
}

// This executable fixture is the foreign ACP peer. It verifies the host's
// initialization and workspace handoff before serving streamed turns.
func Agent(ctx context.Context) {
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
	waitForParent := func(gate string) {
		if gate == "" {
			return
		}
		if err := os.WriteFile(gate+".ready", nil, 0o600); err != nil {
			os.Exit(15)
		}
		deadline := time.Now().Add(5 * time.Second)
		for {
			if _, err := os.Stat(gate); err == nil {
				return
			}
			if time.Now().After(deadline) {
				os.Exit(16)
			}
			time.Sleep(5 * time.Millisecond) //nolint:kennlint // sleeptest: the ACP subprocess polls a parent-controlled file gate for at most five seconds
		}
	}
	scanner := bufio.NewScanner(os.Stdin)
	var promptID jsontext.Value
	elicitationOffered := false
	loadMissed := false
	model, effort := "fast", "low"
	mode := "agent"
	configResponse := func(id jsontext.Value) {
		if os.Getenv("KENN_FORGE_ACP_CODEX") == "1" {
			fmt.Printf(`{"jsonrpc":"2.0","id":%s,"result":{"sessionId":"fixture-session","configOptions":[{"id":"mode","name":"Mode","type":"select","currentValue":%q,"options":[{"value":"agent","name":"Agent"},{"value":"agent-full-access","name":"Full access"},{"value":"read-only","name":"Read-only"}]}]}}`+"\n", id, mode)
			return
		}
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
			// A slow start keeps the owner's socket closed while the daemon waits.
			if delay, err := time.ParseDuration(os.Getenv("KENN_FORGE_ACP_INITIALIZE_DELAY")); err == nil {
				time.Sleep(delay) //nolint:kennlint // sleeptest: delays the ACP subprocess initialization response to exercise startup cancellation
			}
			if os.Getenv("KENN_FORGE_ACP_BAD_VERSION") == "1" {
				fmt.Printf(`{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":999}}`+"\n", message.ID)
				continue
			}
			// A wrapper script that fails explains itself only on stderr.
			if diagnostic := os.Getenv("KENN_FORGE_ACP_EXIT_ON_INITIALIZE"); diagnostic != "" {
				fmt.Fprintln(os.Stderr, diagnostic)
				os.Exit(17)
			}
			if os.Getenv("KENN_FORGE_ACP_CODEX") == "1" {
				fmt.Printf(`{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":1,"agentInfo":{"name":"@agentclientprotocol/codex-acp","version":"fixture"},"agentCapabilities":{"loadSession":true}}}`+"\n", message.ID)
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
			if os.Getenv("KENN_FORGE_ACP_EMPTY_SESSION") == "1" {
				fmt.Printf(`{"jsonrpc":"2.0","id":%s,"result":{"sessionId":""}}`+"\n", message.ID)
				continue
			}
			// The session answers only once the test writes to this socket.
			if socket := os.Getenv("KENN_FORGE_ACP_SESSION_AWAITS"); socket != "" {
				conn, err := (&net.Dialer{}).DialContext(ctx, "unix", socket)
				if err == nil {
					_, err = conn.Read(make([]byte, 1))
					_ = conn.Close()
				}
				if err != nil {
					os.Exit(11)
				}
			}
			if details := os.Getenv("KENN_FORGE_ACP_SESSION_ERROR"); details != "" {
				fmt.Fprintln(os.Stderr, "fixture diagnostic before refusing the session")
				fmt.Printf(`{"jsonrpc":"2.0","id":%s,"error":{"code":-32603,"message":"Internal error","data":{"details":%q}}}`+"\n", message.ID, details)
				continue
			}
			if fixtureDir != "" {
				file, err := os.OpenFile(filepath.Join(fixtureDir, "sessions"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
				if err != nil {
					os.Exit(9)
				}
				_, _ = fmt.Fprintln(file, message.Method)
				_ = file.Close()
			}
			// Claude Code stores a session only at its first prompt, so its
			// agent cannot find a session that never received one.
			if message.Method == "session/load" && os.Getenv("KENN_FORGE_ACP_LOAD_NOT_FOUND") == "1" {
				fmt.Printf(`{"jsonrpc":"2.0","id":%s,"error":{"code":-32002,"message":"Resource not found: fixture-session"}}`+"\n", message.ID)
				loadMissed = true
				continue
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
			if json.Unmarshal(message.Params, &params) != nil || params.MCPServers == nil {
				os.Exit(4)
			}
			cwd, cwdErr := os.Stat(".")
			requested, err := os.Stat(params.CWD)
			if cwdErr != nil || err != nil || !os.SameFile(cwd, requested) {
				os.Exit(4)
			}
			if fixtureDir != "" {
				data, _ := json.Marshal(params.MCPServers)
				_ = os.WriteFile(filepath.Join(fixtureDir, "mcp.json"), data, 0o600)
			}
			// The session that replaces a missed load announces its commands
			// before the client learns its ID.
			if message.Method == "session/new" && loadMissed {
				fmt.Println(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"fixture-session","update":{"sessionUpdate":"available_commands_update","availableCommands":[{"name":"fresh","description":"Fresh session command"}]}}}`)
				configResponse(message.ID)
				continue
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
			if diagnostic := os.Getenv("KENN_FORGE_ACP_EXIT_ON_SET_CONFIG"); diagnostic != "" {
				fmt.Fprintln(os.Stderr, diagnostic)
				os.Exit(18)
			}
			switch params.ConfigID {
			case "model":
				model, effort = params.Value, "low"
			case "mode":
				if os.Getenv("KENN_FORGE_ACP_REJECT_MODE") == "1" {
					fmt.Printf(`{"jsonrpc":"2.0","id":%s,"error":{"code":-32603,"message":"mode rejected"}}`+"\n", message.ID)
					continue
				}
				mode = params.Value
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
			if os.Getenv("KENN_FORGE_ACP_RECORD_PROMPTS") == "1" {
				recordFixtureResponse(fixtureDir, "prompt:"+params.Prompt[0].Text)
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
			recordFixtureResponse(fixtureDir, "steer:"+params.Prompt[0].Text)
			switch params.Prompt[0].Text {
			case "hold":
				// Holds the answer while the host accepts more steers, then refuses.
				waitForParent(os.Getenv("KENN_FORGE_ACP_STEER_GATE"))
				fmt.Printf(`{"jsonrpc":"2.0","id":%s,"result":{"outcome":"failed"}}`+"\n", message.ID)
			case "adjust", "adjust again":
				fmt.Printf(`{"jsonrpc":"2.0","id":%s,"result":{"outcome":"injected"}}`+"\n", message.ID)
			case "finish":
				// The turn ends before the text is taken, so Forge must send it.
				fmt.Printf(`{"jsonrpc":"2.0","id":%s,"result":{"outcome":"promptRequired"}}`+"\n", message.ID)
				fmt.Printf(`{"jsonrpc":"2.0","id":%s,"result":{"stopReason":"end_turn"}}`+"\n", promptID)
			case "takeover":
				// The agent starts and owns a new turn; the original prompt's
				// completion arrives after the steering answer.
				fmt.Printf(`{"jsonrpc":"2.0","id":%s,"result":{"outcome":"startedNewTurn"}}`+"\n", message.ID)
				waitForParent(os.Getenv("KENN_FORGE_ACP_PROMPT_COMPLETION_GATE"))
				fmt.Printf(`{"jsonrpc":"2.0","id":%s,"result":{"stopReason":"end_turn"}}`+"\n", promptID)
				waitForParent(os.Getenv("KENN_FORGE_ACP_THREAD_STATUS_GATE"))
				if os.Getenv("KENN_FORGE_ACP_TAKEOVER_IDLE_ONLY") != "1" {
					threadStatus("active")
				}
				go func() {
					waitForParent(os.Getenv("KENN_FORGE_ACP_TAKEOVER_IDLE_GATE"))
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
				recordFixtureResponse(fixtureDir, "elicitation:"+result.Content.Choice)
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
			recordFixtureResponse(fixtureDir, "permission:"+result.Outcome.OptionID)
			fmt.Println(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"fixture-session","update":{"sessionUpdate":"tool_call_update","toolCallId":"edit","status":"completed","content":[{"type":"content","content":{"type":"text","text":"Updated file"}}]}}}`)
			fmt.Printf(`{"jsonrpc":"2.0","id":%s,"result":{"stopReason":"end_turn"}}`+"\n", promptID)
		}
	}
	os.Exit(0)
}

// Owner runs the real ACP owner in a fixture subprocess.
func Owner(ctx context.Context, run func(context.Context, string) error) {
	if os.Getenv("KENN_FORGE_ACP_OWNER_FIXTURE") != "1" {
		return
	}
	if dir := os.Getenv("KENN_FORGE_ACP_CONTINUE_DIR"); dir != "" {
		_ = os.WriteFile(filepath.Join(dir, "owner-pid"), []byte(strconv.Itoa(os.Getpid())), 0o600)
	}
	if err := run(ctx, os.Args[len(os.Args)-1]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}
