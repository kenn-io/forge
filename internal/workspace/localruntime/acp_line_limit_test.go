package localruntime

import (
	"bufio"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"io"
	"os"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	acpsdk "github.com/coder/acp-go-sdk"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// lineLimitClient records the session updates the SDK delivers. No test here
// sends it any other request.
type lineLimitClient struct {
	acpsdk.Client
	mu      sync.Mutex
	updates []acpsdk.SessionNotification
}

func (c *lineLimitClient) SessionUpdate(_ context.Context, update acpsdk.SessionNotification) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.updates = append(c.updates, update)
	return nil
}

func (c *lineLimitClient) recorded() []acpsdk.SessionNotification {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]acpsdk.SessionNotification(nil), c.updates...)
}

// lineLimitAgent is the agent end of a client connection: it reads what the
// client sends and writes raw lines back.
type lineLimitAgent struct {
	t        *testing.T
	fromConn *bufio.Reader
	toConn   *io.PipeWriter
}

// readRequest returns the next message the client sent, failing when none
// arrives: a client that dropped a message would otherwise hang the test.
func (a *lineLimitAgent) readRequest() map[string]jsontext.Value {
	a.t.Helper()
	type read struct {
		line []byte
		err  error
	}
	got := make(chan read, 1)
	go func() {
		line, err := a.fromConn.ReadBytes('\n')
		got <- read{line, err}
	}()
	select {
	case result := <-got:
		require.NoError(a.t, result.err)
		var message map[string]jsontext.Value
		require.NoError(a.t, json.Unmarshal(result.line, &message))
		return message
	case <-time.After(10 * time.Second):
		require.FailNow(a.t, "the client sent nothing")
		return nil
	}
}

func (a *lineLimitAgent) write(line string) {
	_, _ = io.WriteString(a.toConn, line+"\n")
}

// connectLineLimitAgent connects an SDK client to a scripted agent, reading
// the agent through wrap.
func connectLineLimitAgent(t *testing.T, wrap func(io.Reader) io.Reader) (*acpsdk.ClientSideConnection, *lineLimitClient, *lineLimitAgent) {
	// The kernel pipe buffers what the client sends, so a cancel the SDK sends
	// after a timed-out request does not block on an agent that is not reading.
	toAgentRead, toAgentWrite, err := os.Pipe()
	require.NoError(t, err)
	t.Cleanup(func() { _ = toAgentWrite.Close() })
	fromAgentRead, fromAgentWrite := io.Pipe()
	t.Cleanup(func() {
		_ = toAgentRead.Close()
		_ = fromAgentWrite.Close()
	})
	client := &lineLimitClient{}
	conn := acpsdk.NewClientSideConnection(client, toAgentWrite, wrap(fromAgentRead))
	return conn, client, &lineLimitAgent{t: t, fromConn: bufio.NewReader(toAgentRead), toConn: fromAgentWrite}
}

// oversizedToolOutput is a finished tool call whose output is longer than
// the SDK reads, as a command that printed a large binary produces.
func oversizedToolOutput(sessionID string) string {
	output := strings.Repeat(`\u0000x`, acpMaxMessage/7+1)
	return `{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"` + sessionID + `","update":{"sessionUpdate":"tool_call_update","toolCallId":"call-1","status":"completed","content":[{"type":"content","content":{"type":"text","text":"` + output + `"}}],"rawOutput":"` + output + `"}}}`
}

func promptAsync(ctx context.Context, conn *acpsdk.ClientSideConnection) <-chan error {
	done := make(chan error, 1)
	go func() {
		_, err := conn.Prompt(ctx, acpsdk.PromptRequest{SessionId: "s", Prompt: []acpsdk.ContentBlock{acpsdk.TextBlock("hi")}})
		done <- err
	}()
	return done
}

// Without the limit the SDK gives up on the whole connection at one
// oversized message, which is how a chat died when its agent printed a
// binary.
func TestACPOversizedMessageEndsAnUnlimitedConnection(t *testing.T) {
	conn, _, agent := connectLineLimitAgent(t, func(r io.Reader) io.Reader { return r })
	done := promptAsync(t.Context(), conn)
	request := agent.readRequest()
	go func() {
		agent.write(oversizedToolOutput("s"))
		agent.write(`{"jsonrpc":"2.0","id":` + string(request["id"]) + `,"result":{"stopReason":"end_turn"}}`)
	}()
	err := <-done
	require.Error(t, err)
	assert.Contains(t, err.Error(), "peer disconnected before response")
}

func TestACPLineLimitKeepsTheChatPastAnOversizedToolOutput(t *testing.T) {
	conn, client, agent := connectLineLimitAgent(t, func(r io.Reader) io.Reader { return newACPLineLimitReader(r) })
	done := promptAsync(t.Context(), conn)
	request := agent.readRequest()
	go func() {
		agent.write(oversizedToolOutput("s"))
		agent.write(`{"jsonrpc":"2.0","id":` + string(request["id"]) + `,"result":{"stopReason":"end_turn"}}`)
	}()
	require.NoError(t, <-done, "the turn ends normally")

	require.Eventually(t, func() bool { return len(client.recorded()) == 1 }, 5*time.Second, 10*time.Millisecond)
	update := client.recorded()[0]
	assert.Equal(t, acpsdk.SessionId("s"), update.SessionId)
	toolCall := update.Update.ToolCallUpdate
	require.NotNil(t, toolCall, "the tool call is updated, not dropped")
	assert.Equal(t, acpsdk.ToolCallId("call-1"), toolCall.ToolCallId)
	require.NotNil(t, toolCall.Status)
	assert.Equal(t, acpsdk.ToolCallStatusCompleted, *toolCall.Status, "the tool call still finishes")
	require.Len(t, toolCall.Content, 1)
	require.NotNil(t, toolCall.Content[0].Content)
	text := toolCall.Content[0].Content.Content.Text
	require.NotNil(t, text)
	assert.Contains(t, text.Text, "Output omitted")
	assert.Nil(t, toolCall.RawOutput, "none of the oversized output reaches the chat")

	// The connection still carries the next turn.
	done = promptAsync(t.Context(), conn)
	request = agent.readRequest()
	agent.write(`{"jsonrpc":"2.0","id":` + string(request["id"]) + `,"result":{"stopReason":"end_turn"}}`)
	require.NoError(t, <-done)
}

func TestACPLineLimitFailsOnlyTheRequestAnOversizedAnswerBelongsTo(t *testing.T) {
	conn, _, agent := connectLineLimitAgent(t, func(r io.Reader) io.Reader { return newACPLineLimitReader(r) })
	done := promptAsync(t.Context(), conn)
	request := agent.readRequest()
	go agent.write(`{"jsonrpc":"2.0","id":` + string(request["id"]) + `,"result":{"stopReason":"end_turn","_meta":{"padding":"` + strings.Repeat("x", acpMaxMessage) + `"}}}`)
	err := <-done
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Output omitted")

	done = promptAsync(t.Context(), conn)
	request = agent.readRequest()
	agent.write(`{"jsonrpc":"2.0","id":` + string(request["id"]) + `,"result":{"stopReason":"end_turn"}}`)
	require.NoError(t, <-done, "the connection carries the next request")
}

func TestACPLineLimitRefusesAnOversizedAgentRequest(t *testing.T) {
	_, _, agent := connectLineLimitAgent(t, func(r io.Reader) io.Reader { return newACPLineLimitReader(r) })
	go agent.write(`{"jsonrpc":"2.0","id":41,"method":"session/request_permission","params":{"sessionId":"s","toolCall":{"toolCallId":"call-2","rawInput":"` + strings.Repeat("x", acpMaxMessage) + `"},"options":[]}}`)
	answer := agent.readRequest()
	assert.Equal(t, "41", string(answer["id"]))
	assert.Contains(t, string(answer["error"]), "Method not found", "the agent hears its request refused instead of waiting on it")
}

// The routing members can follow the payload: JSON member order is free, and
// the shared host writes the answers it forwards in map order.
func TestACPLineLimitRoutesOversizedMessagesInAnyMemberOrder(t *testing.T) {
	payload := strings.Repeat(`\u0000x`, acpMaxMessage/7+1)
	answer := func(agent *lineLimitAgent, request map[string]jsontext.Value) {
		agent.write(`{"jsonrpc":"2.0","id":` + string(request["id"]) + `,"result":{"stopReason":"end_turn"}}`)
	}
	prompt := func(t *testing.T, conn *acpsdk.ClientSideConnection) <-chan error {
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		t.Cleanup(cancel)
		return promptAsync(ctx, conn)
	}

	t.Run("tool update", func(t *testing.T) {
		conn, client, agent := connectLineLimitAgent(t, func(r io.Reader) io.Reader { return newACPLineLimitReader(r) })
		done := prompt(t, conn)
		request := agent.readRequest()
		go func() {
			agent.write(`{"params":{"update":{"rawOutput":"` + payload + `","content":[{"type":"content","content":{"type":"text","text":"` + payload + `"}}],"status":"failed","toolCallId":"call-9","sessionUpdate":"tool_call_update"},"sessionId":"s"},"method":"session/update","jsonrpc":"2.0"}`)
			answer(agent, request)
		}()
		require.NoError(t, <-done)
		require.Eventually(t, func() bool { return len(client.recorded()) == 1 }, 5*time.Second, 10*time.Millisecond)
		toolCall := client.recorded()[0].Update.ToolCallUpdate
		require.NotNil(t, toolCall)
		assert.Equal(t, acpsdk.ToolCallId("call-9"), toolCall.ToolCallId)
		require.NotNil(t, toolCall.Status)
		assert.Equal(t, acpsdk.ToolCallStatusFailed, *toolCall.Status, "a status after the output still settles the tool call")
		require.Len(t, toolCall.Content, 1)
		assert.Contains(t, toolCall.Content[0].Content.Content.Text.Text, "Output omitted")
	})

	t.Run("answer", func(t *testing.T) {
		conn, _, agent := connectLineLimitAgent(t, func(r io.Reader) io.Reader { return newACPLineLimitReader(r) })
		done := prompt(t, conn)
		request := agent.readRequest()
		go agent.write(`{"result":{"stopReason":"end_turn","_meta":{"padding":"` + payload + `","id":"not-this-one"}},"jsonrpc":"2.0","id":` + string(request["id"]) + `}`)
		err := <-done
		require.Error(t, err)
		assert.Contains(t, err.Error(), "Output omitted", "the request fails instead of waiting forever")

		done = prompt(t, conn)
		answer(agent, agent.readRequest())
		require.NoError(t, <-done, "the connection carries the next request")
	})

	t.Run("agent request", func(t *testing.T) {
		_, _, agent := connectLineLimitAgent(t, func(r io.Reader) io.Reader { return newACPLineLimitReader(r) })
		go agent.write(`{"params":{"sessionId":"s","toolCall":{"toolCallId":"call-3","rawInput":"` + payload + `"},"options":[]},"method":"session/request_permission","id":"req-7","jsonrpc":"2.0"}`)
		refused := agent.readRequest()
		assert.Equal(t, `"req-7"`, string(refused["id"]))
		assert.Contains(t, string(refused["error"]), "Method not found")
	})
}

func scanACPRouting(message string) acpOversizedMessage {
	var scanner acpRoutingScanner
	scanner.write([]byte(message))
	return scanner.message
}

// The payload's own structure costs the scanner nothing: a deeply nested
// output with long keys must not grow what it keeps.
func TestACPRoutingScannerKeepsNothingOfANestedPayload(t *testing.T) {
	key := strings.Repeat("k", 1000)
	const depth = 2000
	var payload strings.Builder
	for range depth {
		payload.WriteString(`{"` + key + `":`)
	}
	payload.WriteString(`"x \" } ] { [ \\"`)
	payload.WriteString(strings.Repeat("}", depth))
	message := []byte(`{"jsonrpc":"2.0","method":"session/update","params":{"update":{"rawOutput":` + payload.String() + `,"toolCallId":"call-5","status":"completed","sessionUpdate":"tool_call_update"},"sessionId":"s"}}`)

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	var scanner acpRoutingScanner
	scanner.write(message)
	runtime.ReadMemStats(&after)

	assert.Less(t, after.TotalAlloc-before.TotalAlloc, uint64(1<<20), "scanning a %d-byte payload allocates", payload.Len())
	assert.Equal(t, acpOversizedMessage{Method: "session/update", SessionID: "s", SessionUpdate: "tool_call_update", ToolCallID: "call-5", Status: "completed"}, scanner.message)
}

// Routing fields are matched by their place in the message, not by a name
// that spells that place.
func TestACPRoutingScannerMatchesMembersNotSpelledPaths(t *testing.T) {
	message := scanACPRouting(`{"method":"session/update","params":{"sessionId":"s","update":{"sessionUpdate":"tool_call_update","toolCallId":"call-1","status":"completed","rawOutput":{"status":"failed","id":7}},"update/status":"failed","params":{"sessionId":"other"}},"result":{"id":9}}`)
	assert.Equal(t, "completed", message.Status)
	assert.Equal(t, "s", message.SessionID)
	assert.Empty(t, message.ID, "an id inside the payload is not the message's")
}

func TestACPRoutingScannerKeepsLongIdentifiers(t *testing.T) {
	toolCallID := strings.Repeat("t", 1023)
	requestID := `"` + strings.Repeat("r", 2048) + `"`
	message := scanACPRouting(`{"params":{"sessionId":"s","update":{"toolCallId":"` + toolCallID + `","sessionUpdate":"tool_call_update"}},"id":` + requestID + `,"method":"session/request_permission"}`)
	assert.Equal(t, toolCallID, message.ToolCallID)
	assert.Equal(t, requestID, string(message.ID))
}

func TestACPLineLimitRefusesAnOversizedRequestWithALongID(t *testing.T) {
	_, _, agent := connectLineLimitAgent(t, func(r io.Reader) io.Reader { return newACPLineLimitReader(r) })
	requestID := `"` + strings.Repeat("r", 2048) + `"`
	go agent.write(`{"jsonrpc":"2.0","params":{"sessionId":"s","toolCall":{"toolCallId":"call-3","rawInput":"` + strings.Repeat("x", acpMaxMessage) + `"},"options":[]},"id":` + requestID + `,"method":"session/request_permission"}`)
	refused := agent.readRequest()
	assert.Equal(t, requestID, string(refused["id"]))
	assert.Contains(t, string(refused["error"]), "Method not found")
}
