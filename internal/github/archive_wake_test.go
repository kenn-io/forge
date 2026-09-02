package github

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/require"
)

// pacedArchiveRunner records when each pass ran and answers with a
// configurable "attempted work" result.
type pacedArchiveRunner struct {
	worked atomic.Bool
	mu     sync.Mutex
	passes []time.Time
}

func (r *pacedArchiveRunner) RunPass(context.Context) (bool, error) {
	r.mu.Lock()
	r.passes = append(r.passes, time.Now())
	r.mu.Unlock()
	return r.worked.Load(), nil
}

// reset clears the recorded passes and returns the new origin for offsets.
func (r *pacedArchiveRunner) reset() time.Time {
	r.mu.Lock()
	r.passes = nil
	r.mu.Unlock()
	return time.Now()
}

func (r *pacedArchiveRunner) offsetsFrom(origin time.Time) []time.Duration {
	r.mu.Lock()
	defer r.mu.Unlock()
	offsets := make([]time.Duration, 0, len(r.passes))
	for _, at := range r.passes {
		offsets = append(offsets, at.Sub(origin))
	}
	return offsets
}

func (r *pacedArchiveRunner) recorded() []time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]time.Time(nil), r.passes...)
}

// startPacedArchiveLoop runs the worker loop inside the current synctest
// bubble so the syncer's channels and the loop's timers are all virtual.
func startPacedArchiveLoop(t *testing.T, runner *pacedArchiveRunner) (*Syncer, func()) {
	t.Helper()
	syncer := NewSyncerWithRegistry(nil, nil, nil, nil, time.Hour, nil, nil)
	syncer.SetArchiveService(runner)
	syncer.SetArchivePollIntervalForTesting(time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan struct{})
	close(ready)
	done := make(chan struct{})
	go func() {
		defer close(done)
		syncer.runArchiveLoop(ctx, ready)
	}()
	return syncer, func() {
		cancel()
		<-done
	}
}

func TestArchiveLoopBacksOffWhileIdleAndResetsOnWake(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		require := require.New(t)
		runner := &pacedArchiveRunner{}
		start := time.Now()
		syncer, stop := startPacedArchiveLoop(t, runner)
		defer stop()

		// Idle passes double their spacing: 1s, 2s, 4s, 8s, ...
		time.Sleep(16 * time.Second)
		synctest.Wait()
		require.Equal([]time.Duration{
			0, time.Second, 3 * time.Second, 7 * time.Second, 15 * time.Second,
		}, runner.offsetsFrom(start))

		// A wake runs a pass immediately and restarts the backoff from the
		// pacing interval.
		wakeAt := runner.reset()
		syncer.WakeArchive()
		time.Sleep(3 * time.Second)
		synctest.Wait()
		require.Equal([]time.Duration{0, time.Second, 3 * time.Second}, runner.offsetsFrom(wakeAt))
	})
}

func TestArchiveLoopKeepsPacingIntervalWhileWorkFlows(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		require := require.New(t)
		runner := &pacedArchiveRunner{}
		runner.worked.Store(true)
		start := time.Now()
		_, stop := startPacedArchiveLoop(t, runner)
		defer stop()

		time.Sleep(5 * time.Second)
		synctest.Wait()
		require.Equal([]time.Duration{
			0, time.Second, 2 * time.Second, 3 * time.Second, 4 * time.Second, 5 * time.Second,
		}, runner.offsetsFrom(start))

		// Once work dries up the loop backs off again: 1s, then 2s.
		runner.worked.Store(false)
		last := runner.reset()
		time.Sleep(4 * time.Second)
		synctest.Wait()
		require.Equal([]time.Duration{time.Second, 3 * time.Second}, runner.offsetsFrom(last))
	})
}

func TestArchiveLoopIdleBackoffIsCapped(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		require := require.New(t)
		runner := &pacedArchiveRunner{}
		_, stop := startPacedArchiveLoop(t, runner)
		defer stop()

		time.Sleep(time.Hour)
		synctest.Wait()
		passes := runner.recorded()
		require.GreaterOrEqual(len(passes), 3)
		last := passes[len(passes)-1]
		previous := passes[len(passes)-2]
		require.Equal(archiveIdleWait, last.Sub(previous))
	})
}
