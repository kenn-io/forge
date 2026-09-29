package localruntime

import (
	"context"
	"encoding/json/v2"
	"io"
	"testing"
	"time"

	acpsdk "github.com/coder/acp-go-sdk"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/agentactivity"
)

func TestDeliverableTextPrefix(t *testing.T) {
	for _, tc := range []struct {
		name, text, ready string
	}{
		{"holds an unfinished paragraph", "Still typing this", ""},
		{"releases through the last blank line", "One.\n\nTwo.\n\nThree", "One.\n\nTwo.\n\n"},
		{"keeps an open code fence together, including blank lines inside it", "Intro.\n\n```ts\nconst a = 1\n\nconst b = 2\n", "Intro.\n\n"},
		{"releases a closed code fence without waiting for a blank line", "```ts\nconst a = 1\n```\nAfter", "```ts\nconst a = 1\n```\n"},
		{"releases finished list items at the start of the next item", "- first\n- second\n- thi", "- first\n- second\n"},
		{"does not treat a fence closer of a shorter marker as closing", "````\n```\ninner\n", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.ready, tc.text[:deliverableTextPrefix(tc.text)])
		})
	}
}

type discardWriteCloser struct{ io.Writer }

func (discardWriteCloser) Close() error { return nil }

// newDetachedACP is a chat whose agent never answers; tests drive its session
// updates directly.
func newDetachedACP(t *testing.T) *ACP {
	t.Helper()
	peerReader, peerWriter := io.Pipe()
	agent := &ACP{
		stdin: discardWriteCloser{io.Discard}, done: make(chan struct{}), sessionID: "session",
		permissions:  make(map[string]chan acpsdk.RequestPermissionOutcome),
		elicitations: make(map[string]chan acpsdk.UnstableCreateElicitationResponse),
		subscribers:  make(map[chan struct{}]struct{}),
		state:        ACPState{Connected: true},
	}
	agent.client = acpsdk.NewClientSideConnection(agent, agent, peerReader)
	t.Cleanup(func() {
		_ = peerWriter.Close()
		_ = peerReader.Close()
		select {
		case <-agent.done:
		default:
			close(agent.done)
		}
	})
	return agent
}

func acpUpdate(t *testing.T, agent *ACP, update acpsdk.SessionUpdate) {
	t.Helper()
	require.NoError(t, agent.SessionUpdate(t.Context(), acpsdk.SessionNotification{SessionId: "session", Update: update}))
}

func acpText(t *testing.T, agent *ACP, text string) {
	t.Helper()
	acpUpdate(t, agent, acpsdk.SessionUpdate{AgentMessageChunk: &acpsdk.SessionUpdateAgentMessageChunk{Content: acpsdk.TextBlock(text)}})
}

func publishedACPState(t *testing.T, agent *ACP) ACPState {
	t.Helper()
	data, err := agent.Snapshot()
	require.NoError(t, err)
	var state ACPState
	require.NoError(t, json.Unmarshal(data, &state))
	return state
}

func publishedTexts(t *testing.T, agent *ACP) []string {
	t.Helper()
	texts := []string{}
	for _, message := range publishedACPState(t, agent).Messages {
		texts = append(texts, message.Text)
	}
	return texts
}

// Streamed text is published in finished markdown blocks, not token by token.
func TestACPPublishesStreamedTextInBlocks(t *testing.T) {
	agent := newDetachedACP(t)

	acpText(t, agent, "Hello ")
	assert.Empty(t, publishedTexts(t, agent), "an unfinished paragraph stays held")
	acpText(t, agent, "world.\n\nNext")
	assert.Equal(t, []string{"Hello world.\n\n"}, publishedTexts(t, agent))
	acpText(t, agent, " paragraph.\n\nMore")
	assert.Equal(t, []string{"Hello world.\n\n"}, publishedTexts(t, agent), "blocks within the pacing window wait")

	// Tool activity follows the text the agent produced before it.
	acpUpdate(t, agent, acpsdk.SessionUpdate{ToolCall: &acpsdk.SessionUpdateToolCall{ToolCallId: "read", Title: "Read file"}})
	state := publishedACPState(t, agent)
	require.Len(t, state.Messages, 2)
	assert.Equal(t, "Hello world.\n\nNext paragraph.\n\nMore", state.Messages[0].Text)
	assert.Equal(t, "pending", state.Messages[1].Status, "a tool call without a status is pending")

	// An agent that goes quiet mid-block has that text shown.
	acpText(t, agent, "Quiet tail")
	assert.Len(t, publishedTexts(t, agent), 2)
	require.Eventually(t, func() bool {
		texts := publishedTexts(t, agent)
		return len(texts) == 3 && texts[2] == "Quiet tail"
	}, 2*time.Second, 10*time.Millisecond)
}

func TestACPUpdatesOnlyKnownToolCallsNewestFirst(t *testing.T) {
	agent := newDetachedACP(t)
	completed := acpsdk.ToolCallStatusCompleted
	acpUpdate(t, agent, acpsdk.SessionUpdate{ToolCall: &acpsdk.SessionUpdateToolCall{ToolCallId: "run", Title: "First run", Status: acpsdk.ToolCallStatusCompleted}})
	acpUpdate(t, agent, acpsdk.SessionUpdate{ToolCall: &acpsdk.SessionUpdateToolCall{ToolCallId: "run", Title: "Second run", Status: acpsdk.ToolCallStatusInProgress}})
	acpUpdate(t, agent, acpsdk.SessionUpdate{ToolCallUpdate: &acpsdk.SessionToolCallUpdate{ToolCallId: "run", Status: &completed}})
	acpUpdate(t, agent, acpsdk.SessionUpdate{ToolCallUpdate: &acpsdk.SessionToolCallUpdate{ToolCallId: "unknown", Status: &completed}})

	state := publishedACPState(t, agent)
	require.Len(t, state.Messages, 2, "an update for an unseen tool call adds nothing")
	assert.Equal(t, "completed", state.Messages[0].Status)
	assert.Equal(t, "Second run", state.Messages[1].Text)
	assert.Equal(t, "completed", state.Messages[1].Status)
}

func TestACPTurnEndCancelsOpenPermissions(t *testing.T) {
	agent := newDetachedACP(t)
	agent.state.Busy = true
	outcome := make(chan acpsdk.RequestPermissionResponse, 1)
	go func() {
		response, _ := agent.RequestPermission(context.Background(), acpsdk.RequestPermissionRequest{
			ToolCall: acpsdk.ToolCallUpdate{ToolCallId: "edit"},
			Options:  []acpsdk.PermissionOption{{OptionId: "allow", Name: "Allow", Kind: acpsdk.PermissionOptionKindAllowOnce}},
		})
		outcome <- response
	}()
	require.Eventually(t, func() bool { return len(publishedACPState(t, agent).Permissions) == 1 }, 2*time.Second, 10*time.Millisecond)

	completed := make(chan acpTurnResult, 1)
	completed <- acpTurnResult{stopReason: acpsdk.StopReasonEndTurn}
	agent.finishTurn(completed)

	select {
	case response := <-outcome:
		assert.NotNil(t, response.Outcome.Cancelled)
	case <-time.After(2 * time.Second):
		require.FailNow(t, "the finished turn left its permission request open")
	}
	state := publishedACPState(t, agent)
	assert.Empty(t, state.Permissions)
	assert.False(t, state.Busy)
}

func TestACPStopDoesNotWaitForOtherControls(t *testing.T) {
	agent := newDetachedACP(t)
	agent.state.Busy = true
	// A steering request or settings change holds the turn lock.
	agent.turnMu.Lock()
	defer agent.turnMu.Unlock()
	stopped := make(chan error, 1)
	go func() { stopped <- agent.Command(ACPCommand{Type: "cancel"}) }()
	select {
	case err := <-stopped:
		require.NoError(t, err)
	case <-time.After(2 * time.Second):
		require.FailNow(t, "stop waited for another control")
	}
	state := publishedACPState(t, agent)
	assert.True(t, state.Stopping)
	assert.True(t, state.QueuePaused)
}

// An owner that crashed leaves its report behind; the replacement owner, maybe
// under a new ACP session ID, must be the only report for the runtime.
func TestACPActivityReplacesReportsLeftByAnEarlierOwner(t *testing.T) {
	store := agentactivity.NewStore(t.TempDir())
	cwd := t.TempDir()
	require.NoError(t, store.Record(ACPActivityAgent, "crashed-session", "runtime", cwd, agentactivity.StateApproval))
	agent := newDetachedACP(t)
	reported := make(chan struct{})
	go func() {
		defer close(reported)
		reportACPActivity(store, agent, "runtime", cwd)
	}()

	require.Eventually(t, func() bool {
		reports := store.LiveReportsForWorkspace(cwd, []string{"runtime"})
		return len(reports) == 1 && reports[0].SessionID == "session" && reports[0].State == agentactivity.StateIdle
	}, 2*time.Second, 10*time.Millisecond)
	close(agent.done)
	<-reported
	assert.Empty(t, store.LiveReportsForWorkspace(cwd, []string{"runtime"}))
}
