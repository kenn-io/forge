package localruntime

import (
	"context"
	"encoding/json/v2"
	"path/filepath"
	"strings"
	"testing"
	"time"

	acpsdk "github.com/coder/acp-go-sdk"
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

// People chat in a supervised chat without taking it over. An agent that
// does not hold the chat cannot add to it.
func TestACPPeopleChatInASupervisedChat(t *testing.T) {
	agent, _ := supervisedACP(t)

	require.NoError(t, agent.Command(ACPCommand{Type: "prompt", Text: "from the browser", ID: "ui"}))
	require.ErrorIs(t, agent.Prompt("from a tool", ACPSender{Kind: ACPSenderCoordinator}), ErrACPSupervised)

	state := publishedACPState(t, agent)
	assert.True(t, state.Busy)
	require.Len(t, state.Messages, 1)
	assert.Equal(t, &ACPSender{Kind: ACPSenderUser}, state.Messages[0].Sender)
	require.Len(t, state.Turns, 1)
	assert.Equal(t, ACPSender{Kind: ACPSenderUser}, state.Turns[0].Sender)
	assert.Zero(t, state.Turns[0].Generation, "a person's turn is not the coordinator's")
	assert.False(t, state.Supervision.TakenOver, "chatting does not end supervision")
}

func TestACPCoordinatorPromptsQueueBehindARunningTurn(t *testing.T) {
	agent, generation := supervisedACP(t)

	require.ErrorContains(t, agent.Command(ACPCommand{Type: "prompt", Text: "work", Generation: generation}),
		"a coordinator's prompts require a submission ID")
	require.ErrorIs(t, agent.Command(ACPCommand{Type: "prompt", Mode: "steer", Text: "work", ID: "steer", Generation: generation}),
		ErrACPPersonOnly)
	require.False(t, publishedACPState(t, agent).Busy, "a rejected prompt starts nothing")

	first := ACPCommand{Type: "prompt", Mode: "send", Text: "work", ID: "first", Generation: generation}
	require.NoError(t, agent.Command(first))
	require.True(t, publishedACPState(t, agent).Busy)
	require.NoError(t, agent.Command(first), "a retry of the running prompt succeeds")

	second := ACPCommand{Type: "prompt", Text: "more", ID: "second", Generation: generation, AllowanceMillis: 5000}
	require.NoError(t, agent.Command(second))
	coordinator := ACPSender{Kind: ACPSenderCoordinator, Name: "coordinator"}
	state := publishedACPState(t, agent)
	require.Len(t, state.Messages, 1)
	assert.Equal(t, "first", state.Messages[0].SubmissionID)
	assert.Equal(t, &coordinator, state.Messages[0].Sender)
	require.Len(t, state.Turns, 1)
	assert.Equal(t, coordinator, state.Turns[0].Sender)
	assert.Equal(t, generation, state.Turns[0].Generation)
	assert.Equal(t, []ACPQueuedPrompt{{ID: "second", Text: "more", Sender: coordinator, Generation: generation, Allowance: 5000}},
		state.Queue, "the queued prompt keeps its sender and allowance")
}

func TestACPCancelIsAllowedWhileSupervised(t *testing.T) {
	agent, generation := supervisedACP(t)
	require.NoError(t, agent.Command(ACPCommand{Type: "prompt", Text: "work", ID: "first", Generation: generation}))

	require.NoError(t, agent.Command(ACPCommand{Type: "cancel"}))
	assert.True(t, publishedACPState(t, agent).Stopping)
}

func TestACPCoordinatorCancelNeedsTheGenerationItHolds(t *testing.T) {
	agent, generation := supervisedACP(t)
	require.NoError(t, agent.Command(ACPCommand{Type: "prompt", Text: "work", ID: "first", Generation: generation}))

	require.ErrorIs(t, agent.Command(ACPCommand{Type: "cancel", Generation: generation + 1}), ErrACPStaleGeneration)
	require.NoError(t, agent.Command(ACPCommand{Type: "takeover"}))
	assert.False(t, publishedACPState(t, agent).Stopping)

	require.NoError(t, agent.Command(ACPCommand{Type: "cancel"}), "a person can still stop the turn")
	assert.True(t, publishedACPState(t, agent).Stopping)
}

func TestACPCoordinatorCancelStopsItsTurn(t *testing.T) {
	agent, generation := supervisedACP(t)
	require.NoError(t, agent.Command(ACPCommand{Type: "prompt", Text: "work", ID: "first", Generation: generation}))

	require.NoError(t, agent.Command(ACPCommand{Type: "cancel", Generation: generation}))
	assert.True(t, publishedACPState(t, agent).Stopping)
}

// A coordinator stops only turns its own prompts started. With no turn
// running, its retried cancel has nothing to stop.
func TestACPCoordinatorCannotStopAPersonsTurn(t *testing.T) {
	agent, generation := supervisedACP(t)
	require.NoError(t, agent.Command(ACPCommand{Type: "cancel", Generation: generation}), "nothing is running")
	assert.False(t, publishedACPState(t, agent).QueuePaused)

	require.NoError(t, agent.Command(ACPCommand{Type: "prompt", Text: "a person's work", ID: "ui"}))
	require.ErrorIs(t, agent.Command(ACPCommand{Type: "cancel", Generation: generation}), ErrACPNotOwned)
	state := publishedACPState(t, agent)
	assert.True(t, state.Busy)
	assert.False(t, state.Stopping)
}

// A takeover ends supervision and keeps queued prompts.
func TestACPTakeoverEndsSupervision(t *testing.T) {
	agent, generation := supervisedACP(t)
	require.NoError(t, agent.Command(ACPCommand{Type: "prompt", Text: "work", ID: "first", Generation: generation}))
	require.NoError(t, agent.Command(ACPCommand{Type: "prompt", Text: "more", ID: "second", Generation: generation}))
	require.NoError(t, agent.Command(ACPCommand{Type: "prompt", Text: "mine", ID: "ui"}))

	require.NoError(t, agent.Command(ACPCommand{Type: "takeover"}))
	state := publishedACPState(t, agent)
	require.NotNil(t, state.Supervision)
	assert.True(t, state.Supervision.TakenOver)
	assert.Equal(t, generation+1, state.Supervision.Generation)
	assert.Equal(t, state.Supervision, readSavedSession(t, agent.recordPath).State.Supervision, "the takeover is saved")
	assert.Equal(t, []string{"second", "ui"}, readQueueIDs(state.Queue), "queued prompts stay queued")
	assert.True(t, state.Busy, "the coordinator's running turn keeps running")

	require.NoError(t, agent.Command(ACPCommand{Type: "takeover"}))
	assert.Equal(t, state.Supervision, publishedACPState(t, agent).Supervision, "a repeated takeover changes nothing")

	require.NoError(t, agent.Prompt("from a tool", ACPSender{Kind: ACPSenderCoordinator}), "agents may send to an unsupervised chat")
	queue := publishedACPState(t, agent).Queue
	require.Len(t, queue, 3)
	assert.Equal(t, ACPSender{Kind: ACPSenderCoordinator}, queue[2].Sender)
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
	require.ErrorIs(t, agent.Prompt("from a tool", ACPSender{Kind: ACPSenderCoordinator}), ErrACPSupervised)
}

// The owner RPC carries every supervision failure as a code, and the claim
// and the queue's senders survive a replacement owner through session.json.
func TestACPSupervisionThroughTheOwner(t *testing.T) {
	c := launchRequestChat(t)
	require.NoError(t, c.chat.Command(ACPCommand{Type: "prompt", Text: "wait", ID: "running"}))
	require.NoError(t, c.chat.Command(ACPCommand{Type: "prompt", Text: "hello", ID: "queued"}))

	require.NoError(t, c.chat.Command(ACPCommand{Type: "supervise", Supervisor: "coordinator"}), "a claim waits for nothing")
	supervision := c.state().Supervision
	require.NotNil(t, supervision)
	generation := supervision.Generation

	require.ErrorIs(t, c.chat.Command(ACPCommand{Type: "supervise", Supervisor: "late"}), ErrACPStaleGeneration)
	require.ErrorIs(t, c.manager.SubmitAgentMessage(t.Context(), "workspace", c.info.Key, "hello", ACPSender{Kind: ACPSenderCoordinator}), ErrACPSupervised)
	require.ErrorIs(t, c.chat.Command(ACPCommand{Type: "cancel", Generation: generation}), ErrACPNotOwned)
	require.ErrorIs(t, c.chat.Command(ACPCommand{Type: "unqueue", ID: "queued", Generation: generation}), ErrACPNotOwned)
	require.NoError(t, c.chat.Command(ACPCommand{Type: "prompt", Text: "next", ID: "supervised", Generation: generation}))
	require.ErrorIs(t, c.chat.Command(ACPCommand{Type: "move", ID: "supervised", Generation: generation}), ErrACPPersonOnly)
	require.NoError(t, c.chat.Command(ACPCommand{Type: "move", ID: "supervised", Index: 0}), "people order the queue")
	require.NoError(t, c.chat.Command(ACPCommand{Type: "cancel"}))
	c.awaitState(func(s ACPState) bool { return !s.Busy })

	c.restart()
	state := c.state()
	assert.Equal(t, supervision, state.Supervision)
	require.Len(t, state.Queue, 2)
	assert.Equal(t, "supervised", state.Queue[0].ID)
	assert.Equal(t, ACPSender{Kind: ACPSenderCoordinator, Name: "coordinator"}, state.Queue[0].Sender)
	assert.Equal(t, ACPSender{Kind: ACPSenderUser}, state.Queue[1].Sender)
	require.ErrorIs(t, c.manager.SubmitAgentMessage(t.Context(), "workspace", c.info.Key, "hello", ACPSender{Kind: ACPSenderCoordinator}), ErrACPSupervised)
	require.ErrorIs(t, c.chat.Command(ACPCommand{Type: "resume", Generation: generation}), ErrACPPersonOnly)
}

func TestACPTakeoverRejectsStaleGenerations(t *testing.T) {
	for name, offset := range map[string]uint64{"original": 0, "taken over": 1} {
		t.Run(name, func(t *testing.T) {
			agent, generation := supervisedACP(t)
			require.NoError(t, agent.Command(ACPCommand{Type: "takeover"}))
			generation += offset
			for _, command := range []ACPCommand{
				{Type: "cancel", Generation: generation},
				{Type: "prompt", Text: "work", ID: "late", Generation: generation},
				{Type: "config", ID: "model", Value: "deep", Generation: generation},
				{Type: "permission", ID: "request", OptionID: "allow", Generation: generation},
				{Type: "elicitation", ID: "request", Action: "decline", Generation: generation},
				{Type: "unqueue", ID: "queued", Generation: generation},
			} {
				require.ErrorIs(t, agent.Command(command), ErrACPStaleGeneration, command.Type)
			}
			state := publishedACPState(t, agent)
			assert.False(t, state.Busy)
			assert.Empty(t, state.Messages)
			assert.Empty(t, state.Queue)
		})
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

func TestACPPeopleChangeSettingsWhileSupervised(t *testing.T) {
	agent := newInProcessChat(t).start(nil)
	require.NoError(t, agent.Command(ACPCommand{Type: "supervise", Supervisor: "coordinator"}))

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
	contended := make(chan struct{})
	agent.onConfigLockContention = func() { close(contended) }
	result := make(chan error, 1)
	go func() {
		result <- agent.Command(ACPCommand{Type: "config", ID: "model", Value: "deep", Generation: generation})
	}()
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	select {
	case <-contended:
	case <-ctx.Done():
		agent.turnMu.Unlock()
		require.FailNow(t, "settings command did not contend for the turn lock")
	}
	err := agent.Command(ACPCommand{Type: "takeover"})
	agent.turnMu.Unlock()
	require.NoError(t, err)

	require.ErrorIs(t, <-result, ErrACPStaleGeneration)
	assert.Equal(t, before, fixtureModel(t, agent))
	assert.NotEqual(t, "deep", before)
}

// A takeover needs only the state lock, so it can land while a supervised
// prompt is being written. The turn that starts then belongs to people, and
// the supervisor's allowance must not cancel it.
func TestACPTakeoverDuringAPromptWriteDisarmsItsAllowance(t *testing.T) {
	agent, generation := supervisedACP(t)
	agent.beforePromptWrite = func() {
		agent.beforePromptWrite = nil
		require.NoError(t, agent.Command(ACPCommand{Type: "takeover"}))
	}

	require.NoError(t, agent.Command(ACPCommand{Type: "prompt", Text: "work", ID: "first", Generation: generation, AllowanceMillis: 60000}))
	state := publishedACPState(t, agent)
	require.True(t, state.Busy)
	require.Len(t, state.Turns, 1)
	assert.Zero(t, state.Turns[0].Allowance, "the person's turn carries the supervisor's allowance")
	require.NotNil(t, state.Supervision)
	assert.True(t, state.Supervision.TakenOver)
}

// A claim may land while a person's turn runs. The turn stays the person's:
// the coordinator cannot stop it or answer its questions.
func TestACPSuperviseLeavesARunningTurnToItsSender(t *testing.T) {
	agent := newDetachedACP(t)
	require.NoError(t, agent.Prompt("a person's work", ACPSender{Kind: ACPSenderUser}))
	require.NoError(t, agent.Command(ACPCommand{Type: "supervise", Supervisor: "coordinator"}))
	generation := publishedACPState(t, agent).Supervision.Generation
	agent.mu.Lock()
	agent.permissions["p1"] = make(chan acpsdk.RequestPermissionOutcome, 1)
	agent.state.Permissions = []ACPPermission{{ID: "p1", Options: []ACPPermissionOption{{OptionID: "allow"}}}}
	agent.mu.Unlock()

	require.ErrorIs(t, agent.Command(ACPCommand{Type: "cancel", Generation: generation}), ErrACPNotOwned)
	err := agent.Command(ACPCommand{Type: "permission", ID: "p1", OptionID: "allow", Generation: generation})
	require.ErrorIs(t, err, ErrACPNotOwned)
	assert.Equal(t, "not_owned", ACPErrorCode(err))
	require.Len(t, publishedACPState(t, agent).Permissions, 1)
	require.NoError(t, agent.Command(ACPCommand{Type: "permission", ID: "p1", OptionID: "allow"}), "the person answers")
}

// A person who took a chat over can keep using it when its agent cannot
// reload the saved session: it continues in a new session like any person's
// chat, instead of refusing every start.
func TestACPTakenOverChatContinuesWhenTheAgentCannotReload(t *testing.T) {
	t.Setenv("KENN_FORGE_ACP_NO_LOAD", "1")
	c := launchRequestChat(t)
	require.NoError(t, c.chat.Prompt("earlier", ACPSender{Kind: ACPSenderUser}))
	c.awaitIdle()
	require.NoError(t, c.chat.Command(ACPCommand{Type: "supervise", Supervisor: "coordinator"}))
	require.NoError(t, c.chat.Command(ACPCommand{Type: "takeover"}))
	before := c.state()
	require.NotNil(t, before.Supervision)
	require.True(t, before.Supervision.TakenOver)

	c.restart()
	state := c.state()
	assert.Equal(t, before.Messages, state.Messages)
	assert.Equal(t, before.Supervision, state.Supervision)
	require.Len(t, state.Notices, 1)
	assert.Contains(t, state.Notices[0], "cannot reload its previous session")
	require.NoError(t, c.chat.Prompt("continue", ACPSender{Kind: ACPSenderUser}), "people can send again")
}

// Every prompt tells the agent who sent it under _meta.forge, since ACP has
// no notion of several senders.
func TestACPPromptsNameTheirSenderToTheAgent(t *testing.T) {
	agent, peer := newScriptedACP(t)
	agent.recordPath = filepath.Join(t.TempDir(), "session.json")
	require.NoError(t, agent.Command(ACPCommand{Type: "prompt", Text: "mine", ID: "ui"}))
	peer.endTurn(acpsdk.StopReasonEndTurn)
	require.Eventually(t, func() bool { return !publishedACPState(t, agent).Busy }, time.Second, time.Millisecond)
	require.NoError(t, agent.Command(ACPCommand{Type: "supervise", Supervisor: "planner"}))
	require.NoError(t, agent.Command(ACPCommand{Type: "prompt", Text: "theirs", ID: "c1", Generation: 1}))

	var meta []map[string]any
	for _, params := range peer.params(acpsdk.AgentMethodSessionPrompt) {
		var request struct {
			Meta map[string]map[string]any `json:"_meta"`
		}
		require.NoError(t, json.Unmarshal(params, &request))
		meta = append(meta, request.Meta["forge"])
	}
	assert.Equal(t, []map[string]any{
		{"sender": "user", "submissionId": "ui"},
		{"sender": "coordinator", "senderName": "planner", "submissionId": "c1"},
	}, meta)
}

func TestACPSteeringInvalidatesIndicesBeforeOversizedOutput(t *testing.T) {
	agent, peer := newScriptedACP(t)
	agent.state.SteeringSupported = true
	require.NoError(t, agent.Command(ACPCommand{Type: "prompt", Text: "work", ID: "first"}))
	steered := make(chan error, 1)
	go func() { steered <- agent.Command(ACPCommand{Type: "prompt", Mode: "steer", Text: "also", ID: "steer"}) }()
	require.Eventually(t, func() bool { return len(peer.params(acpSteeringMethod)) == 1 }, time.Second, time.Millisecond)
	acpUpdate(t, agent, acpsdk.SessionUpdate{ToolCall: &acpsdk.SessionUpdateToolCall{
		ToolCallId: "large", Title: "Output", RawOutput: strings.Repeat("x", 300_000),
	}})
	before := publishedACPState(t, agent)
	require.Equal(t, 1, before.MessageOffset)
	peer.answer(acpSteeringMethod, `{"outcome":"injected"}`)
	require.NoError(t, <-steered)
	after := publishedACPState(t, agent)
	require.Equal(t, 2, after.MessageOffset)
	assert.Greater(t, after.HistoryRevision, before.MessageRevision)
	data, err := agent.History(after.MessageOffset, 100)
	require.NoError(t, err)
	var frame map[string]ACPHistory
	require.NoError(t, json.Unmarshal(data, &frame))
	messages := append(frame["history"].Messages, after.Messages...)
	require.Len(t, messages, 3)
	assert.Equal(t, "first", messages[0].SubmissionID)
	assert.Equal(t, "steer", messages[1].SubmissionID)
	assert.Equal(t, "large", messages[2].ToolCallID)
}

func TestACPSteeringNamesThePersonToTheAgent(t *testing.T) {
	agent, peer := newScriptedACP(t)
	agent.state.SteeringSupported = true
	require.NoError(t, agent.Command(ACPCommand{Type: "prompt", Text: "work", ID: "first"}))
	steered := make(chan error, 1)
	go func() { steered <- agent.Command(ACPCommand{Type: "prompt", Mode: "steer", Text: "also", ID: "steer"}) }()
	require.Eventually(t, func() bool { return len(peer.params(acpSteeringMethod)) == 1 }, time.Second, time.Millisecond)
	var request struct {
		Meta map[string]map[string]any `json:"_meta"`
	}
	require.NoError(t, json.Unmarshal(peer.params(acpSteeringMethod)[0], &request))
	assert.Equal(t, map[string]any{"sender": "user", "submissionId": "steer"}, request.Meta["forge"])
	peer.answer(acpSteeringMethod, `{"outcome":"injected"}`)
	require.NoError(t, <-steered)
	messages := publishedACPState(t, agent).Messages
	require.Len(t, messages, 2)
	assert.Equal(t, &ACPSender{Kind: ACPSenderUser}, messages[1].Sender)
}

// People order the queue and remove anything in it. A coordinator removes
// only its own prompts and orders nothing.
func TestACPQueueBelongsToPeople(t *testing.T) {
	agent, generation := supervisedACP(t)
	require.NoError(t, agent.Command(ACPCommand{Type: "prompt", Text: "work", ID: "running", Generation: generation}))
	for _, id := range []string{"c1", "c2"} {
		require.NoError(t, agent.Command(ACPCommand{Type: "prompt", Text: id, ID: id, Generation: generation}))
	}
	require.NoError(t, agent.Command(ACPCommand{Type: "prompt", Text: "mine", ID: "ui"}))
	queued := func() []string {
		var ids []string
		for _, prompt := range publishedACPState(t, agent).Queue {
			ids = append(ids, prompt.ID)
		}
		return ids
	}
	require.Equal(t, []string{"c1", "c2", "ui"}, queued())

	require.ErrorIs(t, agent.Command(ACPCommand{Type: "move", ID: "c2", Index: 0, Generation: generation}), ErrACPPersonOnly)
	require.ErrorIs(t, agent.Command(ACPCommand{Type: "unqueue", ID: "ui", Generation: generation}), ErrACPNotOwned)
	require.ErrorIs(t, agent.Command(ACPCommand{Type: "resume", Generation: generation}), ErrACPPersonOnly)
	assert.Equal(t, []string{"c1", "c2", "ui"}, queued())

	require.NoError(t, agent.Command(ACPCommand{Type: "move", ID: "ui", Index: 0}))
	assert.Equal(t, []string{"ui", "c1", "c2"}, queued())
	assert.Equal(t, []string{"ui", "c1", "c2"}, readQueueIDs(readSavedSession(t, agent.recordPath).State.Queue), "the new order is saved")
	require.NoError(t, agent.Command(ACPCommand{Type: "move", ID: "ui", Index: 99}), "a position past the end is the end")
	assert.Equal(t, []string{"c1", "c2", "ui"}, queued())
	require.ErrorContains(t, agent.Command(ACPCommand{Type: "move", ID: "missing", Index: 0}), "no longer queued")

	require.NoError(t, agent.Command(ACPCommand{Type: "unqueue", ID: "c1", Generation: generation}), "a coordinator withdraws its own prompt")
	require.NoError(t, agent.Command(ACPCommand{Type: "unqueue", ID: "c2"}), "a person withdraws a coordinator's prompt")
	assert.Equal(t, []string{"ui"}, queued())
	require.ErrorIs(t, agent.Command(ACPCommand{Type: "prompt", Text: "c2", ID: "c2", Generation: generation}), errACPSubmissionWithdrawn,
		"a withdrawn prompt never runs on retry")
}

// A new claim leaves the queue as it is. The former coordinator can no
// longer remove its prompt; a person can.
func TestACPNewClaimKeepsTheQueue(t *testing.T) {
	agent, generation := supervisedACP(t)
	require.NoError(t, agent.Command(ACPCommand{Type: "prompt", Text: "work", ID: "running", Generation: generation}))
	require.NoError(t, agent.Command(ACPCommand{Type: "prompt", Text: "later", ID: "later", Generation: generation}))
	require.NoError(t, agent.Command(ACPCommand{Type: "prompt", Text: "mine", ID: "ui"}))

	require.NoError(t, agent.Command(ACPCommand{Type: "supervise", Supervisor: "successor", Generation: generation}))
	assert.Equal(t, []string{"later", "ui"}, readQueueIDs(publishedACPState(t, agent).Queue))
	assert.NotContains(t, readSavedSession(t, agent.recordPath).State.Withdrawn, "later")
	require.ErrorIs(t, agent.Command(ACPCommand{Type: "unqueue", ID: "later", Generation: generation}), ErrACPStaleGeneration)
	require.NoError(t, agent.Command(ACPCommand{Type: "unqueue", ID: "later"}))
}

func readQueueIDs(queue []ACPQueuedPrompt) []string {
	ids := make([]string, 0, len(queue))
	for _, prompt := range queue {
		ids = append(ids, prompt.ID)
	}
	return ids
}

// A coordinator's queued prompt stays queued when supervision ends, and runs
// in its turn without the allowance of a coordinator that no longer holds
// the chat.
func TestACPQueuedCoordinatorPromptRunsAfterSupervisionEnds(t *testing.T) {
	agent, peer := newScriptedACP(t)
	require.NoError(t, agent.Command(ACPCommand{Type: "supervise", Supervisor: "coordinator"}))
	require.NoError(t, agent.Command(ACPCommand{Type: "prompt", Text: "work", ID: "running", Generation: 1}))
	require.NoError(t, agent.Command(ACPCommand{Type: "prompt", Text: "c1", ID: "c1", Generation: 1, AllowanceMillis: 60000}))
	require.NoError(t, agent.Command(ACPCommand{Type: "prompt", Text: "mine", ID: "ui"}))
	require.NoError(t, agent.Command(ACPCommand{Type: "takeover"}))
	require.Equal(t, []string{"c1", "ui"}, readQueueIDs(publishedACPState(t, agent).Queue))

	peer.endTurn(acpsdk.StopReasonEndTurn)
	require.Eventually(t, func() bool { return len(publishedACPState(t, agent).Messages) == 2 }, time.Second, time.Millisecond)
	state := publishedACPState(t, agent)
	assert.Equal(t, "c1", state.Messages[1].SubmissionID)
	assert.Equal(t, &ACPSender{Kind: ACPSenderCoordinator, Name: "coordinator"}, state.Messages[1].Sender)
	assert.Zero(t, runningTurn(t, agent).Allowance)
	assert.False(t, state.QueuePaused)
	assert.Equal(t, []string{"ui"}, readQueueIDs(state.Queue))
}

// Ending supervision while a coordinator's queued prompt is being sent leaves
// it sent; only its allowance is dropped.
func TestACPTakeoverDuringAQueuedWriteLeavesItSent(t *testing.T) {
	agent, peer := newScriptedACP(t)
	agent.recordPath = filepath.Join(t.TempDir(), "session.json")
	require.NoError(t, agent.Command(ACPCommand{Type: "supervise", Supervisor: "coordinator"}))
	require.NoError(t, agent.Command(ACPCommand{Type: "prompt", Text: "work", ID: "running", Generation: 1}))
	require.NoError(t, agent.Command(ACPCommand{Type: "prompt", Text: "next", ID: "c1", Generation: 1, AllowanceMillis: 60000}))
	agent.beforePromptWrite = func() {
		agent.beforePromptWrite = nil
		require.NoError(t, agent.Command(ACPCommand{Type: "takeover"}))
	}

	peer.endTurn(acpsdk.StopReasonEndTurn)
	require.Eventually(t, func() bool { return len(publishedACPState(t, agent).Messages) == 2 }, time.Second, time.Millisecond)
	state := readSavedSession(t, agent.recordPath).State
	assert.NotContains(t, state.Withdrawn, "c1", "a prompt the agent received is not withdrawn")
	assert.Empty(t, state.Queue)
	assert.Equal(t, "c1", runningTurn(t, agent).SubmissionID)
	assert.Zero(t, runningTurn(t, agent).Allowance, "the coordinator no longer holds the chat")
}

// A coordinator releasing its claim names its generation, so a delayed
// release never ends a later claim.
func TestACPEndingSupervisionChecksTheCoordinatorsGeneration(t *testing.T) {
	agent, generation := supervisedACP(t)
	require.NoError(t, agent.Command(ACPCommand{Type: "supervise", Supervisor: "successor", Generation: generation}))
	require.NoError(t, agent.Command(ACPCommand{Type: "prompt", Text: "work", ID: "running", Generation: generation + 1}))
	require.NoError(t, agent.Command(ACPCommand{Type: "prompt", Text: "queued", ID: "s1", Generation: generation + 1}))

	require.ErrorIs(t, agent.Command(ACPCommand{Type: "takeover", Generation: generation}), ErrACPStaleGeneration)
	state := publishedACPState(t, agent)
	assert.False(t, state.Supervision.TakenOver)
	assert.Equal(t, "successor", state.Supervision.Supervisor)
	assert.Equal(t, []string{"s1"}, readQueueIDs(state.Queue))

	require.NoError(t, agent.Command(ACPCommand{Type: "takeover", Generation: generation + 1}), "the holder releases its claim")
	assert.True(t, publishedACPState(t, agent).Supervision.TakenOver)
}

// A coordinator's prompt that finds the chat busy at the last moment queues
// under the same lock that checks its generation, so it queues only while its
// coordinator still holds the chat.
func TestACPCoordinatorPromptQueuesWhenTheAgentStartsATurnFirst(t *testing.T) {
	agent := newDetachedACP(t)
	require.NoError(t, agent.Command(ACPCommand{Type: "supervise", Supervisor: "coordinator"}))
	agent.beforePromptStart = func() {
		agent.mu.Lock()
		// The agent starts a turn of its own.
		agent.state.Busy = true
		agent.mu.Unlock()
	}

	require.NoError(t, agent.Command(ACPCommand{Type: "prompt", Text: "later", ID: "later", Generation: 1}))

	queue := publishedACPState(t, agent).Queue
	require.Len(t, queue, 1)
	assert.Equal(t, "later", queue[0].ID)
	assert.Equal(t, uint64(1), queue[0].Generation)
}

func TestACPCoordinatorPromptIsRefusedAfterATakeoverBeforeItStarts(t *testing.T) {
	agent := newDetachedACP(t)
	require.NoError(t, agent.Command(ACPCommand{Type: "supervise", Supervisor: "coordinator"}))
	agent.beforePromptStart = func() {
		agent.mu.Lock()
		agent.state.Busy = true
		agent.mu.Unlock()
		require.NoError(t, agent.Command(ACPCommand{Type: "takeover"}))
	}

	err := agent.Command(ACPCommand{Type: "prompt", Text: "late", ID: "late", Generation: 1})

	require.ErrorIs(t, err, ErrACPStaleGeneration)
	assert.Empty(t, publishedACPState(t, agent).Queue, "a former coordinator's prompt never queues")
}

// An agent's prompt through a Forge tool is checked against supervision when
// it is submitted. A coordinator claiming the chat before the prompt starts
// still keeps it out.
func TestACPAgentPromptIsRefusedWhenACoordinatorClaimsTheChatFirst(t *testing.T) {
	agent := newDetachedACP(t)
	agent.beforePromptStart = func() {
		require.NoError(t, agent.Command(ACPCommand{Type: "supervise", Supervisor: "coordinator"}))
	}

	err := agent.Prompt("from another agent", ACPSender{Kind: ACPSenderCoordinator})

	require.ErrorIs(t, err, ErrACPSupervised)
	state := publishedACPState(t, agent)
	assert.False(t, state.Busy, "the agent's prompt never started")
	assert.Empty(t, state.Queue)
	assert.Empty(t, state.Messages)
}
