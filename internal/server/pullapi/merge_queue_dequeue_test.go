package pullapi

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/forge/internal/apiclient/generated"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/platform"
)

// queuedFixture is pull request 7 in the merge queue with auto-merge on and
// a kept workspace deletion, on a provider that can dequeue it.
func queuedFixture(t *testing.T) *autoMergeFixture {
	t.Helper()
	f := newAutoMergeFixture(t, autoMergeFixtureOptions{allowed: true})
	f.provider.mergeQueue = true
	f.provider.dequeueCh = make(chan int, 1)
	f.provider.setAutoMerge(true)
	require.NoError(t, f.database.SetMergeRequestAutoMergeEnabled(t.Context(), f.repoID, 7, true))
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	_, err := f.database.UpsertMergeQueueState(t.Context(), f.repoID, db.MergeQueueState{
		Number: 7, BaseBranch: "main", Enabled: true, Queued: true, Position: 2,
		State: "AWAITING_CHECKS", EnqueuedAt: &now, ObservedAt: now,
	})
	require.NoError(t, err)
	require.NoError(t, f.database.SaveMergeWorkspaceCleanup(t.Context(), db.MergeWorkspaceCleanup{
		RepoID: f.repoID, Number: 7, WorkspaceID: "ws-1", RequestedAt: now,
	}))
	return f
}

func (f *autoMergeFixture) dequeue(t *testing.T) *generated.DequeuePullOnHostResp {
	t.Helper()
	resp, _ := f.client.HTTP.DequeuePullOnHostWithResponse(t.Context(), &generated.DequeuePullOnHostRequestOptions{
		PathParams: &generated.DequeuePullOnHostPath{
			PlatformHost: f.ref.Host, Provider: "gitlab", Owner: f.ref.Owner, Name: f.ref.Name, Number: 7,
		},
	})
	require.NotNil(t, resp)
	return resp
}

func (f *autoMergeFixture) queued(t *testing.T) bool {
	t.Helper()
	state, err := f.database.GetMergeQueueState(t.Context(), f.repoID, 7)
	require.NoError(t, err)
	require.NotNil(t, state)
	return state.Queued
}

func TestDequeueRemovesThePullRequestAndTurnsAutoMergeOff(t *testing.T) {
	require := require.New(t)
	f := queuedFixture(t)
	require.True(f.pending(t))

	resp := f.dequeue(t)
	require.Equal(200, resp.StatusCode, string(resp.Body))

	select {
	case number := <-f.provider.dequeueCh:
		assert.Equal(t, 7, number)
	default:
		require.FailNow("the provider was not asked to dequeue the pull request")
	}
	state, err := f.provider.AutoMergeState(t.Context(), f.ref, 7)
	require.NoError(err)
	assert.False(t, state.Enabled, "auto-merge left on would put the pull request back in the queue")
	assert.False(t, f.queued(t), "the detail must stop showing the pull request in the queue")
	assert.False(t, f.pending(t))
	assert.Len(t, f.keptCleanups(t), 1, "the user's workspace deletion stays for a later merge")
}

func TestDequeueKeepsTheQueueEntryWhenTheProviderRefuses(t *testing.T) {
	f := queuedFixture(t)
	f.provider.dequeueErr = &platform.Error{Code: platform.ErrCodeConflict, Err: errors.New("cannot dequeue")}

	resp := f.dequeue(t)
	require.Equal(t, 409, resp.StatusCode, string(resp.Body))
	assert.True(t, f.queued(t))
}

func TestDequeueNeedsMergeQueueSupport(t *testing.T) {
	f := queuedFixture(t)
	f.provider.mergeQueue = false

	resp := f.dequeue(t)
	require.Equal(t, 409, resp.StatusCode, string(resp.Body))
	require.Contains(t, string(resp.Body), "unsupportedCapability")
	assert.True(t, f.queued(t))
	assert.Empty(t, f.provider.dequeueCh)
}
