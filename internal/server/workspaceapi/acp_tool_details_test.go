package workspaceapi

import (
	"context"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/workspace/localruntime"
)

// A chat opens with its tool calls' details left out, keeps inline media and
// generated apps, and returns one tool call's details when a client asks.
func TestChatSocketLoadsToolDetailsOnRequest(t *testing.T) {
	t.Parallel()
	f := newChatFixture(t, localruntime.Options{})
	f.saveState(`{"messages":[
		{"id":"m1","role":"user","text":"Check the build"},
		{"id":"m2","previousId":"m1","role":"tool","text":"Run tests","kind":"execute","rawInput":"{\"command\":\"make test\"}","rawOutput":"ok 12 passed",
		 "locations":[{"path":"/repo/main.go"}],
		 "toolContent":[
			{"type":"terminal","terminalId":"t1","output":"ok 12 passed"},
			{"type":"diff","path":"/repo/main.go","newText":"package main"},
			{"type":"content","content":{"type":"image","mimeType":"image/png","data":"iVBORw0KGgo="}},
			{"type":"content","content":{"type":"text","text":"{\"kind\":\"kenn-forge://apps/generated\",\"title\":\"Chart\",\"html\":\"<p>chart</p>\"}"}}
		 ]},
		{"id":"m3","previousId":"m2","role":"assistant","text":"Tests pass"}
	]}`)
	// An older owner protocol keeps the chat on its saved transcript.
	owner := newChatOwner("old")
	owner.protocol = 0
	f.serve(owner)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	conn := f.chatSocket(ctx)

	first := readChatFrame(ctx, t, conn)
	require.Len(t, first.Messages, 3)
	summary := first.Messages[1]
	assert.True(t, summary.DetailsOmitted)
	assert.Empty(t, summary.RawInput)
	assert.Empty(t, summary.RawOutput)
	assert.Equal(t, "Run tests", summary.Text)
	assert.Equal(t, "execute", summary.Kind)
	assert.Equal(t, []localruntime.ACPToolLocation{{Path: "/repo/main.go"}}, summary.Locations)
	require.Len(t, summary.ToolContent, 2)
	assert.Equal(t, "image", summary.ToolContent[0].Content.Type)
	assert.Contains(t, summary.ToolContent[1].Content.Text, "kenn-forge://apps/generated")
	assert.False(t, first.Messages[0].DetailsOmitted)

	// The client names the entry after the tool call, as for a history page.
	require.NoError(t, conn.Write(ctx, websocket.MessageText, []byte(`{"type":"toolDetails","id":"m2","beforeId":"m3"}`)))
	details := readToolDetails(ctx, t, conn)
	assert.False(t, details.DetailsOmitted)
	assert.Equal(t, "ok 12 passed", details.RawOutput)
	assert.JSONEq(t, `{"command":"make test"}`, details.RawInput)
	require.Len(t, details.ToolContent, 4)
	assert.Equal(t, "terminal", details.ToolContent[0].Type)
	assert.Equal(t, "diff", details.ToolContent[1].Type)

	// History pages leave details out too.
	require.NoError(t, conn.Write(ctx, websocket.MessageText, []byte(`{"type":"history","beforeId":"m3","limit":1}`)))
	_, data, err := conn.Read(ctx)
	require.NoError(t, err)
	page := decodeChat[map[string]localruntime.ACPHistory](t, data)["history"]
	require.Len(t, page.Messages, 1)
	assert.True(t, page.Messages[0].DetailsOmitted)
	assert.Empty(t, page.Messages[0].RawOutput)

	// A request whose next entry no longer follows the tool call fails by name.
	require.NoError(t, conn.Write(ctx, websocket.MessageText, []byte(`{"type":"toolDetails","id":"m2"}`)))
	refused := readChatFrame(ctx, t, conn)
	assert.Equal(t, "toolDetails", refused.Command)
	assert.Equal(t, "m2", refused.ID)
	assert.Equal(t, errACPToolDetailsMoved.Error(), refused.CommandError)
}

func readToolDetails(ctx context.Context, t *testing.T, conn *websocket.Conn) localruntime.ACPMessage {
	t.Helper()
	_, data, err := conn.Read(ctx)
	require.NoError(t, err)
	frame := decodeChat[map[string]localruntime.ACPMessage](t, data)
	require.Contains(t, frame, "toolDetails", string(data))
	return frame["toolDetails"]
}
