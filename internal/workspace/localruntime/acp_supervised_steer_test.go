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

func TestACPSubmissionSteersOnlyItsOwnedTurn(t *testing.T) {
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

		require.NoError(t, agent.Command(ACPCommand{Type: "prompt", Text: "work", ID: "start", Generation: 1}))
		require.NoError(t, agent.Command(ACPCommand{Type: "prompt", Text: "next", ID: "target", Generation: 1}))
		steer.ID, steer.SteerTarget = "correction", "target"
		agent.recordPath = filepath.Join(t.TempDir(), "missing", "session.json")
		require.ErrorIs(t, agent.Command(steer), os.ErrNotExist)
		assert.NotContains(t, publishedACPState(t, agent).Withdrawn, "correction")
		agent.recordPath = filepath.Join(t.TempDir(), "session.json")
		require.ErrorIs(t, agent.Command(steer), errACPSubmissionWithdrawn)
		assert.Contains(t, readSavedSession(t, agent.recordPath).State.Withdrawn, "correction")
		agent.mu.Lock()
		agent.state.SteeringSupported = false
		agent.mu.Unlock()
		steer.ID, steer.SteerTarget = "unsupported", "start"
		require.ErrorIs(t, agent.Command(steer), errACPSubmissionWithdrawn)
		assert.Empty(t, peer.params(acpSteeringMethod))

		agent.mu.Lock()
		agent.state.SteeringSupported = true
		agent.mu.Unlock()
		steer.ID = "accepted"
		require.NoError(t, agent.Command(steer))
		synctest.Wait()
		require.Len(t, peer.params(acpSteeringMethod), 1)
		peer.answer(acpSteeringMethod, `{"outcome":"injected"}`)
		synctest.Wait()
		state := publishedACPState(t, agent)
		require.Len(t, state.Turns, 2)
		assert.Equal(t, "start", state.Turns[1].SubmissionID)
		assert.Empty(t, state.Turns[1].EndedAt)
		require.Len(t, state.Queue, 1)
		assert.Equal(t, "target", state.Queue[0].ID)
		assert.Equal(t, []string{"their-turn", "start", "accepted"}, userSubmissions(state))
		peer.endTurn(acpsdk.StopReasonEndTurn)
		synctest.Wait()
		require.NoError(t, agent.Command(steer))
		assert.Len(t, peer.params(acpSteeringMethod), 1)
		assert.Len(t, peer.params(acpsdk.AgentMethodSessionPrompt), 3)
		require.ErrorIs(t, agent.Command(ACPCommand{Type: "prompt", Mode: "steer", Text: "adjust", ID: "correction", SteerTarget: "target", Generation: 1}), errACPSubmissionWithdrawn)
		assert.Len(t, peer.params(acpSteeringMethod), 1)
		peer.endTurn(acpsdk.StopReasonEndTurn)
	})
}

func TestACPSubmissionSettlesMissedSteers(t *testing.T) {
	for _, tc := range []struct {
		name, outcome        string
		endFirst             bool
		withdrawn, uncertain []string
	}{
		{name: "prompt required", outcome: `{"outcome":"promptRequired"}`, endFirst: true, withdrawn: []string{"first", "second"}},
		{name: "uncertain", outcome: `{"outcome":"unknown"}`, withdrawn: []string{"second"}, uncertain: []string{"first"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
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
				if !tc.endFirst {
					saved := readSavedSession(t, agent.recordPath)
					data, err := newDormantACP(saved.State, nil).Snapshot()
					require.NoError(t, err)
					var restored ACPState
					require.NoError(t, json.Unmarshal(data, &restored))
					assert.Empty(t, restored.Queue)
					assert.Equal(t, []string{"first"}, restored.Uncertain)
					assert.Equal(t, []string{"second"}, restored.Withdrawn)
				}
				if tc.endFirst {
					peer.endTurn(acpsdk.StopReasonEndTurn)
					synctest.Wait()
				}
				peer.answer(acpSteeringMethod, tc.outcome)
				synctest.Wait()
				if !tc.endFirst {
					require.ErrorIs(t, agent.Command(first), errACPSubmissionUncertain)
					peer.endTurn(acpsdk.StopReasonEndTurn)
					synctest.Wait()
				}
				state := readSavedSession(t, agent.recordPath).State
				assert.Equal(t, tc.withdrawn, state.Withdrawn)
				assert.Equal(t, tc.uncertain, state.Uncertain)
				assert.Equal(t, []string{"start"}, userSubmissions(state))
				assert.False(t, state.Steering)
				assert.Empty(t, state.Queue)
				require.Len(t, state.Turns, 1)
				assert.NotEmpty(t, state.Turns[0].EndedAt)
				assert.Equal(t, "end_turn", state.Turns[0].StopReason)
				assert.Len(t, peer.params(acpsdk.AgentMethodSessionPrompt), 1)
				assert.Len(t, peer.params(acpSteeringMethod), 1)
			})
		})
	}
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

func TestACPSubmissionKeepsItsTurnWhenTheAgentStartsAReplacement(t *testing.T) {
	for _, tc := range []struct {
		name        string
		cancel      bool
		stopReason  acpsdk.StopReason
		wantCancels int
	}{
		{name: "ended normally", stopReason: acpsdk.StopReasonEndTurn},
		{name: "cancelled", cancel: true, stopReason: acpsdk.StopReasonCancelled, wantCancels: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
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
				if tc.cancel {
					require.NoError(t, agent.Command(ACPCommand{Type: "cancel", Generation: 1}))
					require.Equal(t, 1, peer.cancels())
				}
				peer.endTurn(tc.stopReason)
				synctest.Wait()
				state := publishedACPState(t, agent)
				require.Len(t, state.Turns, 1)
				assert.Empty(t, state.Turns[0].EndedAt)

				acpThreadStatus(t, agent, "active")
				peer.answer(acpSteeringMethod, `{"outcome":"startedNewTurn"}`)
				synctest.Wait()
				assert.Equal(t, tc.wantCancels, peer.cancels())
				state = publishedACPState(t, agent)
				require.Len(t, state.Turns, 1)
				assert.Equal(t, "start", state.Turns[0].SubmissionID)
				assert.Empty(t, state.Turns[0].EndedAt)
				assert.Equal(t, []string{"start", "first"}, userSubmissions(state))
				if !tc.cancel {
					acpText(t, agent, "corrected result")
				}
				acpThreadStatus(t, agent, "idle")
				synctest.Wait()
				state = publishedACPState(t, agent)
				assert.False(t, state.Busy)
				require.Len(t, state.Turns, 1)
				assert.NotEmpty(t, state.Turns[0].EndedAt)
				assert.Equal(t, string(tc.stopReason), state.Turns[0].StopReason)
			})
		})
	}
}
