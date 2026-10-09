package localruntime

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// supervisedACP is a detached chat a coordinator has claimed. It returns the
// generation the coordinator holds.
func supervisedACP(t *testing.T) (*ACP, uint64) {
	t.Helper()
	agent := newDetachedACP(t)
	agent.recordPath = filepath.Join(t.TempDir(), "session.json")
	require.NoError(t, agent.Command(ACPCommand{Type: "supervise", Supervisor: "coordinator"}))
	supervision := publishedACPState(t, agent).Supervision
	require.NotNil(t, supervision)
	return agent, supervision.Generation
}

func TestACPSuperviseClaimsAnUnsupervisedChat(t *testing.T) {
	agent, generation := supervisedACP(t)

	supervision := publishedACPState(t, agent).Supervision
	require.NotNil(t, supervision)
	assert.Equal(t, "coordinator", supervision.Supervisor)
	assert.Equal(t, uint64(1), generation)
	assert.False(t, supervision.TakenOver)
	assert.NotEmpty(t, supervision.ChangedAt)
	assert.Equal(t, supervision, readSavedSession(t, agent.recordPath).State.Supervision, "the claim is saved")
}

func TestACPSuperviseRequiresASupervisor(t *testing.T) {
	agent := newDetachedACP(t)
	require.ErrorContains(t, agent.Command(ACPCommand{Type: "supervise"}), "supervisor is required")
	assert.Nil(t, publishedACPState(t, agent).Supervision)
}

func TestACPSuperviseRequiresTheCurrentGeneration(t *testing.T) {
	agent, generation := supervisedACP(t)

	require.ErrorIs(t, agent.Command(ACPCommand{Type: "supervise", Supervisor: "late"}), ErrACPStaleGeneration,
		"a delayed claim made before the first one is stale")
	require.NoError(t, agent.Command(ACPCommand{Type: "supervise", Supervisor: "successor", Generation: generation}))
	supervision := publishedACPState(t, agent).Supervision
	require.NotNil(t, supervision)
	assert.Equal(t, "successor", supervision.Supervisor)
	assert.Equal(t, generation+1, supervision.Generation)

	require.ErrorIs(t, agent.Command(ACPCommand{Type: "supervise", Supervisor: "coordinator", Generation: generation}),
		ErrACPStaleGeneration, "the claim was already re-issued")
}

func TestACPSupervisedChatRejectsInputWithoutTheGeneration(t *testing.T) {
	agent, _ := supervisedACP(t)

	for _, command := range []ACPCommand{
		{Type: "prompt", Text: "from the browser", ID: "ui"},
		{Type: "unqueue", ID: "queued"},
		{Type: "resume"},
		{Type: "permission", ID: "request", OptionID: "allow"},
		{Type: "elicitation", ID: "request", Action: "decline"},
		{Type: "config", ID: "model", Value: "deep"},
	} {
		require.ErrorIs(t, agent.Command(command), ErrACPSupervised, command.Type)
	}
	require.ErrorIs(t, agent.Prompt("from a tool"), ErrACPSupervised)

	state := publishedACPState(t, agent)
	assert.False(t, state.Busy)
	assert.Empty(t, state.Messages)
	assert.Empty(t, state.Queue)
}

func TestACPSupervisedPromptStartsATurnOrFails(t *testing.T) {
	agent, generation := supervisedACP(t)

	require.ErrorContains(t, agent.Command(ACPCommand{Type: "prompt", Text: "work", Generation: generation}),
		"supervised prompts require a submission ID")
	for _, mode := range []string{"queue", "steer"} {
		require.ErrorContains(t, agent.Command(ACPCommand{Type: "prompt", Mode: mode, Text: "work", ID: mode, Generation: generation}),
			"supervised prompts must be sent, not queued or steered", mode)
	}
	require.False(t, publishedACPState(t, agent).Busy, "a rejected prompt starts nothing")

	first := ACPCommand{Type: "prompt", Mode: "send", Text: "work", ID: "first", Generation: generation}
	require.NoError(t, agent.Command(first))
	require.True(t, publishedACPState(t, agent).Busy)
	require.NoError(t, agent.Command(first), "a retry of the running prompt succeeds")

	second := ACPCommand{Type: "prompt", Text: "more", ID: "second", Generation: generation}
	require.ErrorIs(t, agent.Command(second), ErrACPBusy)
	state := publishedACPState(t, agent)
	assert.Empty(t, state.Queue, "a supervised prompt never waits in the queue")
	require.Len(t, state.Messages, 1)
	assert.Equal(t, "first", state.Messages[0].SubmissionID)
}

func TestACPSupervisedPromptIsBusyWhileSettingsChange(t *testing.T) {
	agent, generation := supervisedACP(t)
	agent.state.Configuring = true

	require.ErrorIs(t, agent.Command(ACPCommand{Type: "prompt", Text: "work", ID: "first", Generation: generation}), ErrACPBusy)
	state := publishedACPState(t, agent)
	assert.Empty(t, state.Queue)
	assert.Empty(t, state.Messages)
}

func TestACPCancelIsAllowedWhileSupervised(t *testing.T) {
	agent, generation := supervisedACP(t)
	require.NoError(t, agent.Command(ACPCommand{Type: "prompt", Text: "work", ID: "first", Generation: generation}))

	require.NoError(t, agent.Command(ACPCommand{Type: "cancel"}))
	assert.True(t, publishedACPState(t, agent).Stopping)
}

func TestACPTakeoverReturnsInputToPeople(t *testing.T) {
	agent, generation := supervisedACP(t)

	require.NoError(t, agent.Command(ACPCommand{Type: "takeover"}))
	supervision := publishedACPState(t, agent).Supervision
	require.NotNil(t, supervision)
	assert.True(t, supervision.TakenOver)
	assert.Equal(t, generation+1, supervision.Generation)
	assert.Equal(t, supervision, readSavedSession(t, agent.recordPath).State.Supervision, "the takeover is saved")

	require.NoError(t, agent.Command(ACPCommand{Type: "takeover"}))
	assert.Equal(t, supervision, publishedACPState(t, agent).Supervision, "a repeated takeover changes nothing")

	require.ErrorIs(t, agent.Command(ACPCommand{Type: "prompt", Text: "late", ID: "late", Generation: generation}),
		ErrACPStaleGeneration, "the former supervisor's input never runs")
	require.NoError(t, agent.Command(ACPCommand{Type: "prompt", Text: "from the browser", ID: "ui"}))
	require.NoError(t, agent.Prompt("from a tool"), "people queue behind a running turn again")

	state := publishedACPState(t, agent)
	require.Len(t, state.Messages, 1)
	assert.Equal(t, "ui", state.Messages[0].SubmissionID)
	require.Len(t, state.Queue, 1)
	assert.Equal(t, "from a tool", state.Queue[0].Text)
}

func TestACPTakeoverOfAnUnsupervisedChatChangesNothing(t *testing.T) {
	agent := newDetachedACP(t)
	require.NoError(t, agent.Command(ACPCommand{Type: "takeover"}))
	assert.Nil(t, publishedACPState(t, agent).Supervision)
}

func TestACPClaimAfterTakeoverNeedsTheCurrentGeneration(t *testing.T) {
	agent, claimed := supervisedACP(t)
	require.NoError(t, agent.Command(ACPCommand{Type: "takeover"}))

	require.ErrorIs(t, agent.Command(ACPCommand{Type: "supervise", Supervisor: "coordinator", Generation: claimed}),
		ErrACPStaleGeneration, "a claim delayed past the takeover is stale")
	require.NoError(t, agent.Command(ACPCommand{Type: "supervise", Supervisor: "coordinator", Generation: claimed + 1}))
	supervision := publishedACPState(t, agent).Supervision
	require.NotNil(t, supervision)
	assert.Equal(t, claimed+2, supervision.Generation)
	assert.False(t, supervision.TakenOver)
	require.ErrorIs(t, agent.Prompt("from a tool"), ErrACPSupervised)
}

// The owner RPC carries every supervision failure as a code, and the claim
// survives a replacement owner through session.json.
func TestACPSupervisionThroughTheOwner(t *testing.T) {
	c := launchRequestChat(t)
	require.NoError(t, c.chat.Command(ACPCommand{Type: "prompt", Text: "wait", ID: "running"}))
	require.NoError(t, c.chat.Command(ACPCommand{Type: "prompt", Text: "hello", ID: "queued"}))
	require.NoError(t, c.chat.Command(ACPCommand{Type: "cancel"}))
	c.awaitState(func(s ACPState) bool { return !s.Busy && len(s.Messages) > 0 })

	require.ErrorIs(t, c.chat.Command(ACPCommand{Type: "supervise", Supervisor: "coordinator"}), ErrACPQueuePending)
	state := c.state()
	assert.Nil(t, state.Supervision)
	require.Len(t, state.Queue, 1, "the refused claim leaves earlier input queued")
	assert.Equal(t, "queued", state.Queue[0].ID)

	require.NoError(t, c.chat.Command(ACPCommand{Type: "unqueue", ID: "queued"}))
	require.NoError(t, c.chat.Command(ACPCommand{Type: "supervise", Supervisor: "coordinator"}))
	supervision := c.state().Supervision
	require.NotNil(t, supervision)
	generation := supervision.Generation

	require.ErrorIs(t, c.chat.Command(ACPCommand{Type: "supervise", Supervisor: "late"}), ErrACPStaleGeneration)
	require.ErrorIs(t, c.chat.Command(ACPCommand{Type: "prompt", Text: "hello", ID: "ui"}), ErrACPSupervised)
	require.ErrorIs(t, c.manager.SubmitAgentMessage(t.Context(), "workspace", c.info.Key, "hello"), ErrACPSupervised)
	require.NoError(t, c.chat.Command(ACPCommand{Type: "prompt", Text: "wait", ID: "supervised", Generation: generation}))
	require.ErrorIs(t, c.chat.Command(ACPCommand{Type: "prompt", Text: "hello", ID: "next", Generation: generation}), ErrACPBusy)
	require.NoError(t, c.chat.Command(ACPCommand{Type: "cancel"}))
	c.awaitState(func(s ACPState) bool { return !s.Busy })

	c.restart()
	assert.Equal(t, supervision, c.state().Supervision)
	require.ErrorIs(t, c.chat.Command(ACPCommand{Type: "prompt", Text: "hello", ID: "ui"}), ErrACPSupervised)
	require.ErrorIs(t, c.manager.SubmitAgentMessage(t.Context(), "workspace", c.info.Key, "hello"), ErrACPSupervised)
	for _, message := range c.state().Messages {
		assert.NotEqual(t, "queued", message.SubmissionID, "the unqueued input never ran")
	}
}

func TestACPTakeoverMakesTheFormerSupervisorsCommandsStale(t *testing.T) {
	agent, generation := supervisedACP(t)
	require.NoError(t, agent.Command(ACPCommand{Type: "takeover"}))

	for _, command := range []ACPCommand{
		{Type: "config", ID: "model", Value: "deep", Generation: generation},
		{Type: "permission", ID: "request", OptionID: "allow", Generation: generation},
		{Type: "elicitation", ID: "request", Action: "decline", Generation: generation},
		{Type: "unqueue", ID: "queued", Generation: generation},
		{Type: "resume", Generation: generation},
	} {
		require.ErrorIs(t, agent.Command(command), ErrACPStaleGeneration, command.Type)
	}
}

// fixtureModel is the model setting the stdio fixture currently reports.
func fixtureModel(t *testing.T, agent *ACP) string {
	t.Helper()
	for _, option := range publishedACPState(t, agent).ConfigOptions {
		if option.ID == "model" {
			return option.CurrentValue
		}
	}
	require.FailNow(t, "the fixture reports no model setting")
	return ""
}

func TestACPSettingsChangeFromAPersonAfterTakeover(t *testing.T) {
	agent := newInProcessChat(t).start(nil)
	require.NoError(t, agent.Command(ACPCommand{Type: "supervise", Supervisor: "coordinator"}))
	require.ErrorIs(t, agent.Command(ACPCommand{Type: "config", ID: "model", Value: "deep"}), ErrACPSupervised)
	require.NoError(t, agent.Command(ACPCommand{Type: "takeover"}))

	require.NoError(t, agent.Command(ACPCommand{Type: "config", ID: "model", Value: "deep"}))
	assert.Equal(t, "deep", fixtureModel(t, agent))
}

// A settings change waits for the turn lock. A takeover that lands while it
// waits makes it stale: the supervisor that sent it no longer holds the chat.
func TestACPSettingsChangeWaitingDuringTakeoverIsStale(t *testing.T) {
	agent := newInProcessChat(t).start(nil)
	require.NoError(t, agent.Command(ACPCommand{Type: "supervise", Supervisor: "coordinator"}))
	generation := publishedACPState(t, agent).Supervision.Generation
	before := fixtureModel(t, agent)

	agent.turnMu.Lock()
	result := make(chan error, 1)
	go func() {
		result <- agent.Command(ACPCommand{Type: "config", ID: "model", Value: "deep", Generation: generation})
	}()
	// Give the command time to reach the turn lock; the outcome must not
	// depend on whether it did.
	time.Sleep(50 * time.Millisecond)
	require.NoError(t, agent.Command(ACPCommand{Type: "takeover"}))
	agent.turnMu.Unlock()

	require.ErrorIs(t, <-result, ErrACPStaleGeneration)
	assert.Equal(t, before, fixtureModel(t, agent))
	assert.NotEqual(t, "deep", before)
}
