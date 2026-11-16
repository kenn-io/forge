package localruntime

import (
	"bufio"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"io"
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

func (a *lineLimitAgent) readRequest() map[string]jsontext.Value {
	a.t.Helper()
	line, err := a.fromConn.ReadBytes('\n')
	require.NoError(a.t, err)
	var message map[string]jsontext.Value
	require.NoError(a.t, json.Unmarshal(line, &message))
	return message
}

func (a *lineLimitAgent) write(line string) {
	_, _ = io.WriteString(a.toConn, line+"\n")
}

// connectLineLimitAgent connects an SDK client to a scripted agent, reading
// the agent through wrap.
func connectLineLimitAgent(t *testing.T, wrap func(io.Reader) io.Reader) (*acpsdk.ClientSideConnection, *lineLimitClient, *lineLimitAgent) {
	toAgentRead, toAgentWrite := io.Pipe()
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
