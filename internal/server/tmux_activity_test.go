package server

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/middleman/internal/db"
)

type doneObservedContext struct {
	context.Context
	doneObserved chan struct{}
	once         sync.Once
}

func (c *doneObservedContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.doneObserved) })
	return c.Context.Done()
}

func TestTmuxActivityTrackerUsesOutputFingerprintChanges(t *testing.T) {
	assert := assert.New(t)
	now := time.Date(2026, 4, 23, 12, 0, 0, 0, time.UTC)
	tracker := newTmuxActivityTracker(func() time.Time { return now })

	first := tracker.Update("session-a", tmuxActivityObservation{
		PaneTitle: "workspace",
		Output:    "initial line\n",
		HasOutput: true,
	})
	assert.False(first.Working)
	assert.Equal(tmuxActivitySourceNone, first.Source)
	assert.Nil(first.LastOutputAt)

	now = now.Add(tmuxSampleMinInterval + time.Second)
	changed := tracker.Update("session-a", tmuxActivityObservation{
		PaneTitle: "workspace",
		Output:    "initial line\nnew line\n",
		HasOutput: true,
	})
	assert.True(changed.Working)
	assert.Equal(tmuxActivitySourceOutput, changed.Source)
	assert.NotNil(changed.LastOutputAt)
	assert.Equal(now, *changed.LastOutputAt)

	now = now.Add(5 * time.Second)
	stillRecent := tracker.Update("session-a", tmuxActivityObservation{
		PaneTitle: "workspace",
		Output:    "initial line\nnew line\n",
		HasOutput: true,
	})
	assert.True(stillRecent.Working)
	assert.Equal(tmuxActivitySourceOutput, stillRecent.Source)
	assert.NotNil(stillRecent.LastOutputAt)
	assert.Equal(*changed.LastOutputAt, *stillRecent.LastOutputAt)

	now = now.Add(tmuxActivityTTL + time.Second)
	expired := tracker.Update("session-a", tmuxActivityObservation{
		PaneTitle: "workspace",
		Output:    "initial line\nnew line\n",
		HasOutput: true,
	})
	assert.False(expired.Working)
	assert.Equal(tmuxActivitySourceNone, expired.Source)
	assert.NotNil(expired.LastOutputAt)
	assert.Equal(*changed.LastOutputAt, *expired.LastOutputAt)
}

func TestTmuxActivityTrackerPrefersTitleProtocol(t *testing.T) {
	assert := assert.New(t)
	now := time.Date(2026, 4, 23, 12, 0, 0, 0, time.UTC)
	tracker := newTmuxActivityTracker(func() time.Time { return now })

	result := tracker.Update("session-a", tmuxActivityObservation{
		PaneTitle: "⠴ t3code-b5014b03",
		Output:    "stable\n",
		HasOutput: true,
	})

	assert.True(result.Working)
	assert.Equal(tmuxActivitySourceTitle, result.Source)
	assert.Nil(result.LastOutputAt)
}

func TestTmuxActivityTrackerCachesFreshSamples(t *testing.T) {
	assert := assert.New(t)
	now := time.Date(2026, 4, 23, 12, 0, 0, 0, time.UTC)
	tracker := newTmuxActivityTracker(func() time.Time { return now })

	_, ok := tracker.Cached("session-a")
	assert.False(ok)

	baseline := tracker.Update("session-a", tmuxActivityObservation{
		PaneTitle: "workspace",
		Output:    "baseline\n",
		HasOutput: true,
	})

	now = now.Add(tmuxSampleMinInterval - time.Second)
	cached, ok := tracker.Cached("session-a")
	assert.True(ok)
	assert.Equal(baseline, cached)

	now = now.Add(2 * time.Second)
	_, ok = tracker.Cached("session-a")
	assert.False(ok)
}

func TestTmuxActivityTrackerBoundsAndCoalescesProbes(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	now := time.Date(2026, 4, 23, 12, 0, 0, 0, time.UTC)
	tracker := newTmuxActivityTrackerWithProbeLimit(
		func() time.Time { return now }, 1,
	)
	cached := tracker.Update("session-a", tmuxActivityObservation{
		PaneTitle: "workspace",
		Output:    "baseline\n",
		HasOutput: true,
	})
	now = now.Add(tmuxSampleMinInterval + time.Second)

	first := tracker.StartProbe(context.Background(), "session-a")
	assert.True(first.Started)
	assert.True(first.HasFallback)
	assert.Equal(cached, first.Fallback)

	sameSession := tracker.StartProbe(context.Background(), "session-a")
	assert.False(sameSession.Started)
	assert.True(sameSession.HasFallback)
	assert.Equal(cached, sameSession.Fallback)

	started := make(chan tmuxProbeStart, 1)
	go func() {
		started <- tracker.StartProbe(context.Background(), "session-b")
	}()
	assert.Never(func() bool {
		return len(started) > 0
	}, 50*time.Millisecond, 5*time.Millisecond)

	updated := first.Probe.Finish(tmuxActivityObservation{
		PaneTitle: "workspace",
		Output:    "baseline\nnew output\n",
		HasOutput: true,
	})
	assert.True(updated.Working)
	assert.Equal(tmuxActivitySourceOutput, updated.Source)

	require.Eventually(func() bool {
		return len(started) > 0
	}, time.Second, 5*time.Millisecond)
	afterFinish := <-started
	assert.True(afterFinish.Started)
	afterFinish.Probe.Cancel()
}

func TestProbeOneTmuxSessionWaitsForCoalescedProbeWithFallback(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	tracker := newTmuxActivityTracker(func() time.Time { return now })
	tracker.Update("session-a", tmuxActivityObservation{
		PaneTitle: "old title",
		Output:    "old output\n",
		HasOutput: true,
	})
	now = now.Add(tmuxSampleMinInterval + time.Second)
	inFlight := tracker.StartProbe(context.Background(), "session-a")
	require.True(inFlight.Started)

	type probeResult struct {
		activity tmuxActivityResult
		ok       bool
		err      error
	}
	result := make(chan probeResult, 1)
	srv := &Server{tmuxActivity: tracker}
	ctx := &doneObservedContext{
		Context:      context.Background(),
		doneObserved: make(chan struct{}),
	}
	go func() {
		activity, ok, err := srv.probeOneTmuxSession(
			ctx,
			tracker,
			&db.WorkspaceSummary{},
			"session-a",
		)
		result <- probeResult{activity: activity, ok: ok, err: err}
	}()

	select {
	case early := <-result:
		require.Fail("coalesced probe returned before the active probe completed",
			"result: %+v", early)
	case <-ctx.doneObserved:
	case <-time.After(time.Second):
		require.Fail("coalesced probe did not begin waiting")
	}

	want := inFlight.Probe.Finish(tmuxActivityObservation{
		PaneTitle: "new title",
		Output:    "old output\nnew output\n",
		HasOutput: true,
	})
	select {
	case got := <-result:
		require.NoError(got.err)
		assert.True(got.ok)
		assert.Equal(want, got.activity)
	case <-time.After(time.Second):
		require.Fail("coalesced probe did not return after completion")
	}
}

func TestProbeOneTmuxSessionReturnsFallbackWhenCoalescedWaitTimesOut(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	tracker := newTmuxActivityTracker(func() time.Time { return now })
	fallback := tracker.Update("session-a", tmuxActivityObservation{
		PaneTitle: "old title",
		Output:    "old output\n",
		HasOutput: true,
	})
	now = now.Add(tmuxSampleMinInterval + time.Second)
	inFlight := tracker.StartProbe(context.Background(), "session-a")
	require.True(inFlight.Started)
	t.Cleanup(inFlight.Probe.Cancel)

	baseCtx, cancel := context.WithCancel(context.Background())
	ctx := &doneObservedContext{
		Context:      baseCtx,
		doneObserved: make(chan struct{}),
	}
	type probeResult struct {
		activity tmuxActivityResult
		ok       bool
		err      error
	}
	result := make(chan probeResult, 1)
	srv := &Server{tmuxActivity: tracker}
	go func() {
		activity, ok, err := srv.probeOneTmuxSession(
			ctx,
			tracker,
			&db.WorkspaceSummary{},
			"session-a",
		)
		result <- probeResult{activity: activity, ok: ok, err: err}
	}()

	select {
	case <-ctx.doneObserved:
	case <-time.After(time.Second):
		require.Fail("coalesced probe did not begin waiting")
	}
	cancel()

	select {
	case got := <-result:
		require.ErrorIs(got.err, context.Canceled)
		assert.True(got.ok)
		assert.Equal(fallback, got.activity)
	case <-time.After(time.Second):
		require.Fail("coalesced probe did not return after cancellation")
	}
}

func TestNormalizeTmuxOutputForFingerprinting(t *testing.T) {
	assert := assert.New(t)

	assert.Equal(
		"one\ntwo\t\nthree\n",
		normalizeTmuxOutput("one  \r\ntwo\t \rthree\n"),
	)
	assert.Equal(
		tmuxOutputFingerprint("one\ntwo\n"),
		tmuxOutputFingerprint("one  \r\ntwo  \n"),
	)
}

func TestMergeTmuxActivityPrefersWorkingSession(t *testing.T) {
	assert := assert.New(t)
	lastOutput := time.Date(2026, 4, 23, 12, 0, 0, 0, time.UTC)
	merged, ok := mergeTmuxActivityResults([]tmuxActivityResult{
		{
			PaneTitle: "idle",
			Source:    tmuxActivitySourceNone,
		},
		{
			PaneTitle:    "codex",
			Working:      true,
			Source:       tmuxActivitySourceOutput,
			LastOutputAt: &lastOutput,
		},
	})

	assert.True(ok)
	assert.True(merged.Working)
	assert.Equal(tmuxActivitySourceOutput, merged.Source)
	assert.Equal("codex", merged.PaneTitle)
	assert.Equal(&lastOutput, merged.LastOutputAt)
}

func TestMergeTmuxActivityPrefersTitleOverOutput(t *testing.T) {
	assert := assert.New(t)
	merged, ok := mergeTmuxActivityResults([]tmuxActivityResult{
		{
			PaneTitle: "agent output",
			Working:   true,
			Source:    tmuxActivitySourceOutput,
		},
		{
			PaneTitle: "⠴ t3code-b5014b03",
			Working:   true,
			Source:    tmuxActivitySourceTitle,
		},
	})

	assert.True(ok)
	assert.True(merged.Working)
	assert.Equal(tmuxActivitySourceTitle, merged.Source)
	assert.Equal("⠴ t3code-b5014b03", merged.PaneTitle)
}
