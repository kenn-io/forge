package server

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
