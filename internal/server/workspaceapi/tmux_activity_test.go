package workspaceapi

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/kballard/go-shellquote"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/workspace"
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
	synctest.Test(t, func(t *testing.T) {
		assert := assert.New(t)
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

		first := tracker.StartProbe(t.Context(), "session-a")
		assert.True(first.Started)
		assert.True(first.HasFallback)
		assert.Equal(cached, first.Fallback)

		sameSession := tracker.StartProbe(t.Context(), "session-a")
		assert.False(sameSession.Started)
		assert.True(sameSession.HasFallback)
		assert.Equal(cached, sameSession.Fallback)

		started := make(chan tmuxProbeStart, 1)
		go func() {
			started <- tracker.StartProbe(t.Context(), "session-b")
		}()
		synctest.Wait()
		assert.Empty(started)

		updated := first.Probe.Finish(tmuxActivityObservation{
			PaneTitle: "workspace",
			Output:    "baseline\nnew output\n",
			HasOutput: true,
		})
		assert.True(updated.Working)
		assert.Equal(tmuxActivitySourceOutput, updated.Source)

		synctest.Wait()
		afterFinish := <-started
		assert.True(afterFinish.Started)
		afterFinish.Probe.Cancel()
	})
}

func TestProbeOneTmuxSessionWaitsForCoalescedProbeWithFallback(t *testing.T) {
	t.Parallel()
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
	inFlight := tracker.StartProbe(t.Context(), "session-a")
	require.True(inFlight.Started)

	type probeResult struct {
		activity tmuxActivityResult
		ok       bool
		err      error
	}
	result := make(chan probeResult, 1)
	srv := &Handler{tmuxActivity: tracker}
	ctx := &doneObservedContext{
		Context:      t.Context(),
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
	t.Parallel()
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
	inFlight := tracker.StartProbe(t.Context(), "session-a")
	require.True(inFlight.Started)
	t.Cleanup(inFlight.Probe.Cancel)

	baseCtx, cancel := context.WithCancel(t.Context())
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
	srv := &Handler{tmuxActivity: tracker}
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

func TestProbeWorkspaceTmuxActivityMakesProgressAcrossThreeSessions(t *testing.T) {
	t.Parallel()
	require := require.New(t)
	assert := assert.New(t)
	dir := t.TempDir()
	tracePath := filepath.Join(dir, "probes")
	scriptPath := filepath.Join(dir, "fake-tmux")
	script := "#!/bin/sh\ntrace=" + shellquote.Join(tracePath) + "\n" + `
mode="$1"
while [ "$#" -gt 0 ]; do
  if [ "$1" = "-t" ]; then shift; session="$1"; break; fi
  shift
done
printf '%s %s\n' "$session" "$mode" >> "$trace"
sleep 0.2
if [ "$mode" = "display-message" ]; then
  case "$session" in
    working) printf '⠴ agent-working\n' ;;
    *) printf 'idle\n' ;;
  esac
else
  printf 'stable\n'
fi
`
	require.NoError(os.WriteFile(scriptPath, []byte(script), 0o755))
	manager := workspace.NewManager(nil, dir, nil)
	manager.SetTmuxCommand([]string{"sh", scriptPath})
	now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	tracker := newTmuxActivityTracker(func() time.Time { return now })
	srv := &Handler{workspaces: manager, tmuxActivity: tracker}
	for attempt := range 3 {
		// The outer enrichment TTL exceeds the sample TTL, so a successful
		// early session cannot carry a later session into the next refresh.
		now = now.Add(workspaceTmuxEnrichmentTTL + time.Millisecond)
		started := time.Now()
		result, ok, _, err := srv.probeWorkspaceTmuxActivity(
			t.Context(), &db.WorkspaceSummary{}, []string{"base", "idle", "working"},
		)
		trace, readErr := os.ReadFile(tracePath)
		t.Logf("attempt=%d elapsed=%s context=%v probe_error=%v result=%+v trace_error=%v trace=\n%s",
			attempt, time.Since(started), t.Context().Err(), err, result, readErr, trace)
		require.NoError(err, "each session fits within the shared probe budget")
		require.NoError(readErr)
		assert.True(ok, "healthy sessions must produce an activity sample")
		assert.True(result.Working)
		assert.Equal(tmuxActivitySourceTitle, result.Source)
		assert.Equal("⠴ agent-working", result.PaneTitle)
		for _, session := range []string{"base", "idle", "working"} {
			_, cached := tracker.Cached(session)
			assert.True(cached, "session %s must complete on refresh %d", session, attempt)
		}
	}
}

func TestProbeWorkspaceTmuxActivityGivesFreeSlotsToWaitingSessions(t *testing.T) {
	t.Parallel()
	require := require.New(t)
	assert := assert.New(t)
	dir := t.TempDir()
	scriptPath := filepath.Join(dir, "fake-tmux")
	// The slow session outlasts the whole probe budget; the others answer
	// immediately.
	script := `#!/bin/sh
mode="$1"
while [ "$#" -gt 0 ]; do
  if [ "$1" = "-t" ]; then shift; session="$1"; break; fi
  shift
done
if [ "$session" = "slow" ]; then exec sleep 5; fi
if [ "$mode" = "display-message" ]; then
  case "$session" in
    working) printf '⠴ agent-working\n' ;;
    *) printf 'idle\n' ;;
  esac
else
  printf 'stable\n'
fi
`
	require.NoError(os.WriteFile(scriptPath, []byte(script), 0o755))
	manager := workspace.NewManager(nil, dir, nil)
	manager.SetTmuxCommand([]string{"sh", scriptPath})
	now := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	tracker := newTmuxActivityTracker(func() time.Time { return now })
	srv := &Handler{workspaces: manager, tmuxActivity: tracker}
	// Five sessions exceed the four probe slots. The fifth session must take
	// the first slot that frees up, not wait behind the slow first session.
	sessions := []string{"slow", "idle-a", "idle-b", "idle-c", "working"}
	started := time.Now()
	result, ok, _, err := srv.probeWorkspaceTmuxActivity(
		t.Context(), &db.WorkspaceSummary{}, sessions,
	)
	t.Logf("elapsed=%s probe_error=%v result=%+v", time.Since(started), err, result)
	require.ErrorContains(err, `tmux session "slow"`)
	assert.True(ok)
	assert.True(result.Working, "the working session must be sampled")
	assert.Equal("⠴ agent-working", result.PaneTitle)
	_, cached := tracker.Cached("working")
	assert.True(cached, "the working session must complete within the budget")
}

func TestProbeWorkspaceTmuxActivityPreservesDeadlines(t *testing.T) {
	t.Parallel()
	for _, timeout := range []time.Duration{100 * time.Millisecond, tmuxActivityProbeTimeout} {
		t.Run(timeout.String(), func(t *testing.T) {
			t.Parallel()
			synctest.Test(t, func(t *testing.T) {
				tracker := newTmuxActivityTracker(nil)
				sessions := []string{"base", "idle", "working"}
				for _, session := range sessions {
					probe := tracker.StartProbe(t.Context(), session)
					require.True(t, probe.Started)
					defer probe.Probe.Cancel()
				}
				ctx := t.Context()
				if timeout < tmuxActivityProbeTimeout {
					var cancel context.CancelFunc
					ctx, cancel = context.WithTimeout(ctx, timeout)
					defer cancel()
				}
				srv := &Handler{tmuxActivity: tracker}
				started := time.Now()
				_, ok, _, err := srv.probeWorkspaceTmuxActivity(ctx, &db.WorkspaceSummary{}, sessions)
				assert.False(t, ok)
				require.ErrorIs(t, err, context.DeadlineExceeded)
				assert.Equal(t, timeout, time.Since(started))
			})
		})
	}
}

func TestNormalizeTmuxOutputForFingerprinting(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
