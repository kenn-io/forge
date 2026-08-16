// Package server exposes HTTP routes and, in this file, tracks passive tmux
// activity for workspace responses.
//
// The workspace sidebar polls /workspaces frequently, but tmux has no durable
// "agent is working" flag for a pane. The tracker turns cheap observations from
// a tmux pane snapshot into a short-lived activity signal for the UI. A snapshot
// includes the active pane title and recent scrollback. The title path catches
// explicit spinner titles set by tools such as Codex; the output path catches
// tools that do not update the title but are still producing terminal output.
//
// Samples are keyed by tmux session. Each sample stores the last pane title, a
// fingerprint of normalized recent output, and the time that fingerprint last
// changed. A title that matches the known spinner protocol marks the workspace
// as working immediately. Otherwise, output is considered active when its
// fingerprint changed within tmuxActivityTTL. Cached samples remain usable for
// tmuxSampleMinInterval so the UI poll loop does not shell out to tmux on every
// request unless the previous sample is old enough to refresh. Refresh probes
// are bounded globally and coalesced per session; if a probe is already running
// or the limit is full, callers reuse the last cached result when one exists.
package workspaceapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync"
	"time"
)

const (
	tmuxActivityTTL            = 30 * time.Second
	tmuxSampleMinInterval      = 4 * time.Second
	tmuxActivityProbeTimeout   = 750 * time.Millisecond
	tmuxCaptureScrollbackLines = 160
	tmuxProbeMaxConcurrency    = 4

	tmuxActivitySourceTitle   = "title"
	tmuxActivitySourceOutput  = "output"
	tmuxActivitySourceNone    = "none"
	tmuxActivitySourceUnknown = "unknown"
)

// TmuxActivitySourceTitle identifies pane-title-based activity.
const TmuxActivitySourceTitle = tmuxActivitySourceTitle

// IsWorkingTmuxTitle reports whether a pane title carries the working spinner.
func IsWorkingTmuxTitle(title string) bool { return isWorkingTmuxTitle(title) }

type TmuxActivitySample struct {
	Session        string
	PaneTitle      string
	Fingerprint    string
	HasFingerprint bool
	LastChangedAt  time.Time
	LastSampledAt  time.Time
	Source         string
	Working        bool
}

type tmuxActivityObservation struct {
	PaneTitle string
	Output    string
	HasOutput bool
}

type tmuxActivityResult struct {
	PaneTitle    string
	Working      bool
	Source       string
	LastOutputAt *time.Time
}

type tmuxActivityTracker struct {
	mu         sync.Mutex
	clock      func() time.Time
	samples    map[string]TmuxActivitySample
	probeSlots chan struct{}
	inFlight   map[string]chan struct{}
}

type tmuxProbeStart struct {
	Probe       tmuxActivityProbe
	Fallback    tmuxActivityResult
	HasFallback bool
	Wait        <-chan struct{}
	Started     bool
}

type tmuxActivityProbe struct {
	tracker *tmuxActivityTracker
	session string
}

func newTmuxActivityTracker(clock func() time.Time) *tmuxActivityTracker {
	return newTmuxActivityTrackerWithProbeLimit(
		clock, tmuxProbeMaxConcurrency,
	)
}

func newTmuxActivityTrackerWithProbeLimit(
	clock func() time.Time,
	maxConcurrentProbes int,
) *tmuxActivityTracker {
	if clock == nil {
		clock = time.Now
	}
	if maxConcurrentProbes < 1 {
		maxConcurrentProbes = 1
	}
	return &tmuxActivityTracker{
		clock:      clock,
		samples:    make(map[string]TmuxActivitySample),
		probeSlots: make(chan struct{}, maxConcurrentProbes),
		inFlight:   make(map[string]chan struct{}),
	}
}

func (t *tmuxActivityTracker) Cached(
	session string,
) (tmuxActivityResult, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()

	return t.cachedLocked(session, true)
}

func (t *tmuxActivityTracker) Update(
	session string,
	obs tmuxActivityObservation,
) tmuxActivityResult {
	t.mu.Lock()
	defer t.mu.Unlock()

	return t.updateLocked(session, obs)
}

func (t *tmuxActivityTracker) StartProbe(
	ctx context.Context,
	session string,
) tmuxProbeStart {
	for {
		t.mu.Lock()
		fallback, hasFallback := t.cachedLocked(session, false)
		if wait, ok := t.inFlight[session]; ok {
			t.mu.Unlock()
			return tmuxProbeStart{
				Fallback:    fallback,
				HasFallback: hasFallback,
				Wait:        wait,
			}
		}
		t.mu.Unlock()

		select {
		case t.probeSlots <- struct{}{}:
			t.mu.Lock()
			if wait, ok := t.inFlight[session]; ok {
				<-t.probeSlots
				t.mu.Unlock()
				return tmuxProbeStart{
					Fallback:    fallback,
					HasFallback: hasFallback,
					Wait:        wait,
				}
			}
			wait := make(chan struct{})
			t.inFlight[session] = wait
			t.mu.Unlock()
			return tmuxProbeStart{
				Probe: tmuxActivityProbe{
					tracker: t,
					session: session,
				},
				Fallback:    fallback,
				HasFallback: hasFallback,
				Started:     true,
			}
		case <-ctx.Done():
			t.mu.Lock()
			fallback, hasFallback = t.cachedLocked(session, false)
			t.mu.Unlock()
			return tmuxProbeStart{
				Fallback:    fallback,
				HasFallback: hasFallback,
			}
		}
	}
}

func (p tmuxActivityProbe) Finish(
	obs tmuxActivityObservation,
) tmuxActivityResult {
	p.tracker.mu.Lock()
	defer p.tracker.mu.Unlock()
	defer p.tracker.finishProbeLocked(p.session)

	return p.tracker.updateLocked(p.session, obs)
}

func (p tmuxActivityProbe) Cancel() {
	p.tracker.mu.Lock()
	defer p.tracker.mu.Unlock()

	p.tracker.finishProbeLocked(p.session)
}

func (t *tmuxActivityTracker) cachedLocked(
	session string,
	requireFresh bool,
) (tmuxActivityResult, bool) {
	sample, ok := t.samples[session]
	if !ok {
		return tmuxActivityResult{}, false
	}
	now := t.clock().UTC()
	if requireFresh &&
		now.Sub(sample.LastSampledAt) >= tmuxSampleMinInterval {
		return tmuxActivityResult{}, false
	}
	return tmuxActivityResultFromSample(sample, now), true
}

func (t *tmuxActivityTracker) updateLocked(
	session string,
	obs tmuxActivityObservation,
) tmuxActivityResult {
	now := t.clock().UTC()
	sample := t.samples[session]
	sample.Session = session
	sample.PaneTitle = strings.TrimSpace(obs.PaneTitle)
	sample.LastSampledAt = now

	if obs.HasOutput {
		nextFingerprint := tmuxOutputFingerprint(obs.Output)
		if !sample.HasFingerprint {
			sample.Fingerprint = nextFingerprint
			sample.HasFingerprint = true
		} else if sample.Fingerprint != nextFingerprint {
			sample.Fingerprint = nextFingerprint
			sample.LastChangedAt = now
		}
	}

	result := tmuxActivityResultFromSample(sample, now)
	sample.Source = result.Source
	sample.Working = result.Working
	t.samples[session] = sample
	return result
}

func (t *tmuxActivityTracker) finishProbeLocked(session string) {
	if wait, ok := t.inFlight[session]; ok {
		close(wait)
	}
	delete(t.inFlight, session)
	<-t.probeSlots
}

func tmuxActivityResultFromSample(
	sample TmuxActivitySample,
	now time.Time,
) tmuxActivityResult {
	result := tmuxActivityResult{
		PaneTitle: sample.PaneTitle,
		Source:    tmuxActivitySourceNone,
	}
	if !sample.LastChangedAt.IsZero() {
		lastOutputAt := sample.LastChangedAt.UTC()
		result.LastOutputAt = &lastOutputAt
	}

	if isWorkingTmuxTitle(sample.PaneTitle) {
		result.Working = true
		result.Source = tmuxActivitySourceTitle
		return result
	}

	if !sample.LastChangedAt.IsZero() &&
		now.Sub(sample.LastChangedAt) <= tmuxActivityTTL {
		result.Working = true
		result.Source = tmuxActivitySourceOutput
		return result
	}

	return result
}

func mergeTmuxActivityResults(
	results []tmuxActivityResult,
) (tmuxActivityResult, bool) {
	var merged tmuxActivityResult
	for _, result := range results {
		merged = mergeTmuxActivityResult(merged, result)
	}
	if merged.PaneTitle == "" &&
		merged.Source == "" &&
		merged.LastOutputAt == nil &&
		!merged.Working {
		return tmuxActivityResult{}, false
	}
	if merged.Source == "" {
		merged.Source = tmuxActivitySourceNone
	}
	return merged, true
}

func mergeTmuxActivityResult(
	current tmuxActivityResult,
	next tmuxActivityResult,
) tmuxActivityResult {
	if current.Source == "" {
		return next
	}
	if next.Source == "" {
		return current
	}
	lastOutputAt := current.LastOutputAt
	if lastOutputAt == nil ||
		(next.LastOutputAt != nil && next.LastOutputAt.After(*lastOutputAt)) {
		lastOutputAt = next.LastOutputAt
	}

	switch {
	case !current.Working && next.Working:
		next.LastOutputAt = lastOutputAt
		return next
	case current.Working && !next.Working:
		current.LastOutputAt = lastOutputAt
		return current
	case activitySourceRank(next.Source) > activitySourceRank(current.Source):
		next.LastOutputAt = lastOutputAt
		return next
	case activitySourceRank(next.Source) == activitySourceRank(current.Source) &&
		next.PaneTitle != "" &&
		current.PaneTitle == "":
		current.PaneTitle = next.PaneTitle
	}
	current.LastOutputAt = lastOutputAt
	return current
}

func activitySourceRank(source string) int {
	switch source {
	case tmuxActivitySourceTitle:
		return 3
	case tmuxActivitySourceOutput:
		return 2
	case tmuxActivitySourceNone:
		return 1
	default:
		return 0
	}
}

func normalizeTmuxOutput(output string) string {
	output = strings.ReplaceAll(output, "\r\n", "\n")
	output = strings.ReplaceAll(output, "\r", "\n")

	lines := strings.Split(output, "\n")
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], " ")
	}
	return strings.Join(lines, "\n")
}

func tmuxOutputFingerprint(output string) string {
	sum := sha256.Sum256([]byte(normalizeTmuxOutput(output)))
	return hex.EncodeToString(sum[:])
}
