package github

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/platform"
)

// visibleProbeInterval is how soon a pull request can be probed again for a
// visible-set refresh. Clicking through a list reprobes the same rows; one
// probe a half minute is fresh enough for a list and keeps clicks cheap.
const visibleProbeInterval = 30 * time.Second

// ProbedMR is a pull request a visible-set refresh found stale, with the
// fingerprint to record once its detail read succeeds.
type ProbedMR struct {
	WatchedMR
	Fingerprint string
}

type visibleProbeTimes struct {
	mu sync.Mutex
	at map[string]time.Time
}

// claim reports whether key is due for a probe, and marks it probed.
func (v *visibleProbeTimes) claim(key string, now time.Time) bool {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.at == nil {
		v.at = make(map[string]time.Time)
	}
	for k, at := range v.at {
		if now.Sub(at) >= visibleProbeInterval {
			delete(v.at, k)
		}
	}
	if _, recent := v.at[key]; recent {
		return false
	}
	v.at[key] = now
	return true
}

// StaleVisibleMRs probes the open GitHub pull requests a user is probably
// looking at and returns the ones whose stored state is behind GitHub. The
// set is the visible rows, the focused pull request and its stack, and the
// pull requests linked to a worktree. Everything goes through batched probes,
// 25 pull requests per GraphQL point; only stale ones need a detail read.
func (s *Syncer) StaleVisibleMRs(ctx context.Context, focus *WatchedMR, visible []WatchedMR) []ProbedMR {
	set := newWatchedMRSet(visible)
	if focus != nil {
		set.add(*focus)
		for _, member := range s.stackMembers(ctx, *focus) {
			set.add(member)
		}
	}
	s.watchMu.Lock()
	for _, linked := range s.watchedMRs {
		set.add(linked)
	}
	s.watchMu.Unlock()

	now := s.nowUTC()
	stored := make(map[string]db.MergeRequest)
	var candidates []WatchedMR
	for _, mr := range set.slice() {
		if watchedMRPlatform(mr) != platform.KindGitHub {
			continue
		}
		repo, ok := s.trackedRepoByIdentity(platform.KindGitHub, mr.Owner, mr.Name, mr.PlatformHost)
		// A stale pull request costs REST detail reads, so a credential
		// inside its foreground reserve neither probes nor refreshes.
		if !ok || repo.Archived || s.backgroundReserveExhausted(repo, QuotaResourceREST, false) {
			continue
		}
		row, err := s.db.GetMergeRequest(
			ctx, string(platform.KindGitHub), watchedMRHost(mr), mr.Owner, mr.Name, mr.Number,
		)
		if err != nil || row == nil || row.State != db.MergeRequestStateOpen {
			continue
		}
		key := watchedMRKey(mr)
		if !s.visibleProbes.claim(key, now) {
			continue
		}
		stored[key] = *row
		candidates = append(candidates, mr)
	}
	if len(candidates) == 0 {
		return nil
	}

	probes := s.probeMRFreshness(ctx, candidates)
	var stale []ProbedMR
	for _, mr := range candidates {
		key := watchedMRKey(mr)
		probe, ok := probes[key]
		if !ok {
			continue
		}
		switch {
		case probe.Fingerprint != "" && s.prFreshness.matches(key, probe.Fingerprint, now):
			continue
		case probe.Fingerprint != "" && s.prFreshness.known(key, now),
			storedBehindProbe(stored[key], probe):
			stale = append(stale, ProbedMR{WatchedMR: mr, Fingerprint: probe.Fingerprint})
		}
	}
	return stale
}

// SyncProbedMR reads a stale pull request in full and, when the read kept
// no stored data for a dropped child refresh, records its probe fingerprint
// so the next probe can vouch for it.
func (s *Syncer) SyncProbedMR(ctx context.Context, kind platform.Kind, host string, mr ProbedMR) error {
	syncCtx, degraded := withDegradedSyncTracking(ctx)
	err := s.SyncMROnProviderIfChanged(syncCtx, kind, host, mr.Owner, mr.Name, mr.Number)
	if err == nil && !degraded.Load() && mr.Fingerprint != "" {
		s.prFreshness.record(watchedMRKey(mr.WatchedMR), mr.Fingerprint, s.nowUTC())
	}
	return err
}

// storedBehindProbe compares a probe with the stored row when no baseline
// exists, as after a restart. It checks what a list row shows: GitHub's
// update time, state, head, and whether checks are still running.
func storedBehindProbe(row db.MergeRequest, probe PullRequestFreshness) bool {
	return !row.LastActivityAt.Equal(probe.UpdatedAt) ||
		!strings.EqualFold(string(row.State), probe.State) ||
		row.PlatformHeadSHA != probe.HeadSHA ||
		(row.CIStatus == "pending") != probe.ChecksPending
}

func (s *Syncer) stackMembers(ctx context.Context, focus WatchedMR) []WatchedMR {
	_, members, err := s.db.GetStackForPR(
		ctx, string(watchedMRPlatform(focus)), watchedMRHost(focus), focus.Owner, focus.Name, focus.Number,
	)
	if err != nil {
		slog.Debug("visible refresh stack lookup failed", "number", focus.Number, "err", err)
		return nil
	}
	out := make([]WatchedMR, 0, len(members))
	for _, member := range members {
		out = append(out, WatchedMR{
			Platform: focus.Platform, PlatformHost: focus.PlatformHost,
			Owner: focus.Owner, Name: focus.Name, Number: member.Number,
		})
	}
	return out
}
