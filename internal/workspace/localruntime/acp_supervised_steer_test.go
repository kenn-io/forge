package localruntime

import (
	"encoding/json/v2"
	"os"
	"path/filepath"
	"testing"
	"testing/synctest"

	acpsdk "github.com/coder/acp-go-sdk"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestACPSubmissionRefusedSteerSaveFailureLeavesNoOutcome(t *testing.T) {
	agent, _ := newScriptedACP(t)
	agent.state.SteeringSupported = true
	require.NoError(t, agent.Command(ACPCommand{Type: "supervise", Supervisor: "planner"}))
	agent.recordPath = filepath.Join(t.TempDir(), "missing", "session.json")
	steer := ACPCommand{Type: "prompt", Mode: "steer", Text: "adjust", ID: "correction", SteerTarget: "target", Generation: 1}

	require.ErrorIs(t, agent.Command(steer), os.ErrNotExist)
	assert.Empty(t, publishedACPState(t, agent).Withdrawn)

	agent.recordPath = filepath.Join(t.TempDir(), "session.json")
	require.ErrorIs(t, agent.Command(steer), errACPSubmissionWithdrawn)
	assert.Equal(t, []string{"correction"}, readSavedSession(t, agent.recordPath).State.Withdrawn)
}

func TestACPSubmissionRefusedSteerStaysWithdrawnWhenTargetStarts(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		agent, peer := newScriptedACP(t)
		agent.state.SteeringSupported = true
		agent.recordPath = filepath.Join(t.TempDir(), "session.json")
		require.NoError(t, agent.Command(ACPCommand{Type: "supervise", Supervisor: "planner"}))
		require.NoError(t, agent.Command(ACPCommand{Type: "prompt", Text: "first", ID: "first", Generation: 1}))
		require.NoError(t, agent.Command(ACPCommand{Type: "prompt", Text: "next", ID: "target", Generation: 1}))
		steer := ACPCommand{Type: "prompt", Mode: "steer", Text: "adjust", ID: "correction", SteerTarget: "target", Generation: 1}
		require.ErrorIs(t, agent.Command(steer), errACPSubmissionWithdrawn)
		assert.Equal(t, []string{"correction"}, readSavedSession(t, agent.recordPath).State.Withdrawn)
		assert.Equal(t, []string{"correction"}, publishedACPState(t, agent).Withdrawn)

		peer.endTurn(acpsdk.StopReasonEndTurn)
		synctest.Wait()
		require.Len(t, peer.params(acpsdk.AgentMethodSessionPrompt), 2)
		require.ErrorIs(t, agent.Command(steer), errACPSubmissionWithdrawn)
		assert.Empty(t, peer.params(acpSteeringMethod))
		peer.endTurn(acpsdk.StopReasonEndTurn)
	})
}

func TestACPSubmissionSteersItsSupervisedTurnInOrder(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		agent, peer := newScriptedACP(t)
		agent.state.SteeringSupported = true
		require.NoError(t, agent.Command(ACPCommand{Type: "supervise", Supervisor: "planner"}))
		require.NoError(t, agent.Command(ACPCommand{Type: "prompt", Text: "work", ID: "start", Generation: 1}))

		first := ACPCommand{Type: "prompt", Mode: "steer", Text: "correct that", ID: "first", SteerTarget: "start", Generation: 1}
		second := ACPCommand{Type: "prompt", Mode: "steer", Text: "one more correction", ID: "second", SteerTarget: "start", Generation: 1}
		require.NoError(t, agent.Command(first))
		require.NoError(t, agent.Command(second))
		require.NoError(t, agent.Command(first))
		synctest.Wait()

		require.Len(t, peer.params(acpSteeringMethod), 1)
		peer.answer(acpSteeringMethod, `{"outcome":"injected"}`)
		synctest.Wait()
		require.Len(t, peer.params(acpSteeringMethod), 2)
		peer.answer(acpSteeringMethod, `{"outcome":"injected"}`)
		synctest.Wait()

		state := publishedACPState(t, agent)
		assert.Equal(t, []string{"start", "first", "second"}, userSubmissions(state))
		require.Len(t, state.Turns, 1)
		assert.Equal(t, "start", state.Turns[0].SubmissionID)
		assert.Empty(t, state.Turns[0].EndedAt)
		require.Len(t, peer.params(acpsdk.AgentMethodSessionPrompt), 1)

		peer.endTurn(acpsdk.StopReasonEndTurn)
		synctest.Wait()
		require.NoError(t, agent.Command(first), "a retry is recognized even after the original turn ended")
		assert.Len(t, peer.params(acpSteeringMethod), 2)
		assert.Len(t, peer.params(acpsdk.AgentMethodSessionPrompt), 1)
	})
}

func TestACPSubmissionCannotSteerAnUnownedOrMissingTurn(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		agent, peer := newScriptedACP(t)
		agent.state.SteeringSupported = true
		require.NoError(t, agent.Command(ACPCommand{Type: "supervise", Supervisor: "planner"}))
		steer := ACPCommand{Type: "prompt", Mode: "steer", Text: "adjust", ID: "idle", SteerTarget: "their-turn", Generation: 1}
		require.ErrorIs(t, agent.Command(steer), errACPSubmissionWithdrawn)

		require.NoError(t, agent.Command(ACPCommand{Type: "prompt", Text: "browser input", ID: "their-turn"}))
		steer.ID = "human"
		require.ErrorIs(t, agent.Command(steer), errACPSubmissionWithdrawn)
		peer.endTurn(acpsdk.StopReasonEndTurn)
		synctest.Wait()

		require.NoError(t, agent.Command(ACPCommand{Type: "prompt", Text: "new request", ID: "new-turn", Generation: 1}))
		steer.ID = "different"
		require.ErrorIs(t, agent.Command(steer), errACPSubmissionWithdrawn)
		steer.ID, steer.SteerTarget, steer.Generation = "stale", "new-turn", 2
		require.ErrorIs(t, agent.Command(steer), ErrACPStaleGeneration)

		agent.mu.Lock()
		agent.state.SteeringSupported = false
		agent.mu.Unlock()
		steer.ID, steer.Generation = "unsupported", 1
		require.ErrorIs(t, agent.Command(steer), errACPSubmissionWithdrawn)

		assert.Empty(t, peer.params(acpSteeringMethod))
		assert.Empty(t, publishedACPState(t, agent).Queue)
		assert.Equal(t, []string{"their-turn", "new-turn"}, userSubmissions(publishedACPState(t, agent)))
		peer.endTurn(acpsdk.StopReasonEndTurn)
	})
}

func TestACPSubmissionDoesNotStartAnotherTurnAfterSteerMisses(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		agent, peer := newScriptedACP(t)
		agent.state.SteeringSupported = true
		agent.recordPath = filepath.Join(t.TempDir(), "session.json")
		require.NoError(t, agent.Command(ACPCommand{Type: "supervise", Supervisor: "planner"}))
		require.NoError(t, agent.Command(ACPCommand{Type: "prompt", Text: "work", ID: "start", Generation: 1}))
		first := ACPCommand{Type: "prompt", Mode: "steer", Text: "correct that", ID: "first", SteerTarget: "start", Generation: 1}
		second := ACPCommand{Type: "prompt", Mode: "steer", Text: "one more correction", ID: "second", SteerTarget: "start", Generation: 1}
		require.NoError(t, agent.Command(first))
		require.NoError(t, agent.Command(second))
		synctest.Wait()
		require.Len(t, peer.params(acpSteeringMethod), 1)
		peer.endTurn(acpsdk.StopReasonEndTurn)
		synctest.Wait()
		peer.answer(acpSteeringMethod, `{"outcome":"promptRequired"}`)
		synctest.Wait()

		assert.Equal(t, []string{"first", "second"}, readSavedSession(t, agent.recordPath).State.Withdrawn)
		state := publishedACPState(t, agent)
		assert.Equal(t, []string{"start"}, userSubmissions(state))
		assert.False(t, state.Steering)
		assert.Empty(t, state.Queue)
		assert.Len(t, peer.params(acpsdk.AgentMethodSessionPrompt), 1)
		require.ErrorIs(t, agent.Command(first), errACPSubmissionWithdrawn)
	})
}

func TestACPSubmissionPreservesAnAmbiguousSteerWithoutRetryingIt(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		agent, peer := newScriptedACP(t)
		agent.state.SteeringSupported = true
		agent.recordPath = filepath.Join(t.TempDir(), "session.json")
		require.NoError(t, agent.Command(ACPCommand{Type: "supervise", Supervisor: "planner"}))
		require.NoError(t, agent.Command(ACPCommand{Type: "prompt", Text: "work", ID: "start", Generation: 1}))
		first := ACPCommand{Type: "prompt", Mode: "steer", Text: "correct that", ID: "first", SteerTarget: "start", Generation: 1}
		second := ACPCommand{Type: "prompt", Mode: "steer", Text: "one more correction", ID: "second", SteerTarget: "start", Generation: 1}
		require.NoError(t, agent.Command(first))
		require.NoError(t, agent.Command(second))
		synctest.Wait()
		require.Len(t, peer.params(acpSteeringMethod), 1)
		peer.answer(acpSteeringMethod, `{"outcome":"unknown"}`)
		synctest.Wait()

		state := publishedACPState(t, agent)
		assert.Equal(t, []string{"second"}, state.Withdrawn)
		assert.Equal(t, []string{"first"}, state.Uncertain)
		assert.Empty(t, state.Queue)
		assert.Equal(t, "uncertain", ACPErrorCode(agent.Command(first)))
		assert.Len(t, peer.params(acpSteeringMethod), 1)
		peer.endTurn(acpsdk.StopReasonEndTurn)
	})
}

func TestACPSubmissionWithdrawsBeforeSendingWhenItsTargetEnds(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		agent, peer := newScriptedACP(t)
		agent.state.SteeringSupported = true
		require.NoError(t, agent.Command(ACPCommand{Type: "supervise", Supervisor: "planner"}))
		require.NoError(t, agent.Command(ACPCommand{Type: "prompt", Text: "work", ID: "start", Generation: 1}))

		allowWrite := make(chan struct{})
		agent.beforePromptWrite = func() { <-allowWrite }
		steer := ACPCommand{Type: "prompt", Mode: "steer", Text: "correct that", ID: "first", SteerTarget: "start", Generation: 1}
		require.NoError(t, agent.Command(steer))
		synctest.Wait()
		peer.endTurn(acpsdk.StopReasonEndTurn)
		synctest.Wait()
		close(allowWrite)
		synctest.Wait()

		require.ErrorIs(t, agent.Command(steer), errACPSubmissionWithdrawn)
		assert.Empty(t, peer.params(acpSteeringMethod))
		assert.Len(t, peer.params(acpsdk.AgentMethodSessionPrompt), 1)
		assert.Empty(t, publishedACPState(t, agent).Queue)
	})
}

func TestACPSubmissionRestoresInterruptedSteersWithoutStartingThem(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		agent, peer := newScriptedACP(t)
		agent.state.SteeringSupported = true
		agent.recordPath = filepath.Join(t.TempDir(), "session.json")
		require.NoError(t, agent.Command(ACPCommand{Type: "supervise", Supervisor: "planner"}))
		require.NoError(t, agent.Command(ACPCommand{Type: "prompt", Text: "work", ID: "start", Generation: 1}))
		first := ACPCommand{Type: "prompt", Mode: "steer", Text: "correct that", ID: "first", SteerTarget: "start", Generation: 1}
		second := ACPCommand{Type: "prompt", Mode: "steer", Text: "one more correction", ID: "second", SteerTarget: "start", Generation: 1}
		require.NoError(t, agent.Command(first))
		require.NoError(t, agent.Command(second))
		synctest.Wait()
		require.Len(t, peer.params(acpSteeringMethod), 1)

		saved := readSavedSession(t, agent.recordPath)
		data, err := newDormantACP(saved.State, nil).Snapshot()
		require.NoError(t, err)
		var state ACPState
		require.NoError(t, json.Unmarshal(data, &state))
		assert.Empty(t, state.Queue)
		assert.Equal(t, []string{"first"}, state.Uncertain)
		assert.Equal(t, []string{"second"}, state.Withdrawn)

		peer.answer(acpSteeringMethod, `{"outcome":"promptRequired"}`)
		peer.endTurn(acpsdk.StopReasonEndTurn)
	})
}

func TestACPSubmissionKeepsItsTurnWhenTheAgentStartsAReplacement(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		agent, peer := newScriptedACP(t)
		agent.state.SteeringSupported = true
		acpThreadStatus(t, agent, "idle")
		require.NoError(t, agent.Command(ACPCommand{Type: "supervise", Supervisor: "planner"}))
		require.NoError(t, agent.Command(ACPCommand{Type: "prompt", Text: "work", ID: "start", Generation: 1}))
		steer := ACPCommand{Type: "prompt", Mode: "steer", Text: "correct that", ID: "first", SteerTarget: "start", Generation: 1}
		require.NoError(t, agent.Command(steer))
		synctest.Wait()
		require.Len(t, peer.params(acpSteeringMethod), 1)

		peer.endTurn(acpsdk.StopReasonEndTurn)
		synctest.Wait()
		state := publishedACPState(t, agent)
		require.Len(t, state.Turns, 1)
		assert.Empty(t, state.Turns[0].EndedAt, "the steer can still continue this turn")

		acpThreadStatus(t, agent, "active")
		peer.answer(acpSteeringMethod, `{"outcome":"startedNewTurn"}`)
		synctest.Wait()
		state = publishedACPState(t, agent)
		require.Len(t, state.Turns, 1)
		assert.Equal(t, "start", state.Turns[0].SubmissionID)
		assert.Empty(t, state.Turns[0].EndedAt)
		assert.Equal(t, []string{"start", "first"}, userSubmissions(state))

		acpText(t, agent, "corrected result")
		acpThreadStatus(t, agent, "idle")
		synctest.Wait()
		state = publishedACPState(t, agent)
		assert.False(t, state.Busy)
		require.Len(t, state.Turns, 1)
		assert.NotEmpty(t, state.Turns[0].EndedAt)
		assert.Equal(t, "end_turn", state.Turns[0].StopReason)
	})
}

func TestACPSubmissionStopsTheReplacementWhenTheOriginalWasCancelled(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		agent, peer := newScriptedACP(t)
		agent.state.SteeringSupported = true
		acpThreadStatus(t, agent, "idle")
		require.NoError(t, agent.Command(ACPCommand{Type: "supervise", Supervisor: "planner"}))
		require.NoError(t, agent.Command(ACPCommand{Type: "prompt", Text: "work", ID: "start", Generation: 1}))
		require.NoError(t, agent.Command(ACPCommand{
			Type: "prompt", Mode: "steer", Text: "correct that", ID: "first", SteerTarget: "start", Generation: 1,
		}))
		synctest.Wait()

		require.NoError(t, agent.Command(ACPCommand{Type: "cancel", Generation: 1}))
		require.Equal(t, 1, peer.cancels())
		peer.endTurn(acpsdk.StopReasonCancelled)
		synctest.Wait()
		state := publishedACPState(t, agent)
		require.Len(t, state.Turns, 1)
		assert.Empty(t, state.Turns[0].EndedAt, "the pending steer might still start a continuation")

		acpThreadStatus(t, agent, "active")
		peer.answer(acpSteeringMethod, `{"outcome":"startedNewTurn"}`)
		synctest.Wait()
		require.Equal(t, 2, peer.cancels(), "a cancellation during the steer also stops the continuation")
		state = publishedACPState(t, agent)
		require.Len(t, state.Turns, 1)
		assert.Equal(t, "start", state.Turns[0].SubmissionID)
		assert.Empty(t, state.Turns[0].EndedAt, "the continuation has not confirmed it stopped")

		acpThreadStatus(t, agent, "idle")
		synctest.Wait()
		state = publishedACPState(t, agent)
		require.Len(t, state.Turns, 1)
		assert.NotEmpty(t, state.Turns[0].EndedAt)
		assert.Equal(t, "cancelled", state.Turns[0].StopReason)
		assert.False(t, state.Busy)
	})
}
