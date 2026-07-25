package server

import (
	"log/slog"

	"go.kenn.io/middleman/internal/db"
	"go.kenn.io/middleman/internal/platform"
	"go.kenn.io/middleman/internal/stacks"
)

// swapGitHubNativeStackPreferenceLocked publishes the preference while the
// caller still holds cfgMu, so the order the syncer observes matches the order
// the config was persisted in. Applying it after the unlock let two concurrent
// writers with opposite values land out of order and leave the runtime
// preference disagreeing with the saved config.
//
// It returns the previous value. Reconciliation keys on that transition rather
// than on a config snapshot read earlier: concurrent writers each hold their own
// snapshot, and reconciling from those could run the restore twice or skip it.
func (s *Server) swapGitHubNativeStackPreferenceLocked(enabled bool) bool {
	if s.syncer == nil {
		return false
	}
	return s.syncer.SetPreferGitHubNativeStacks(enabled)
}

// reconcileGitHubNativeStackProjection restores branch-derived projections after
// the preference was swapped off. It runs after cfgMu is released and must not
// borrow the request context: the setting is already persisted and the syncer
// already switched, so a client that disconnects mid-request would otherwise
// leave native ordering in place until some later sync happened to re-detect.
func (s *Server) reconcileGitHubNativeStackProjection(previous, enabled bool) {
	if s.syncer == nil || !previous || enabled {
		return
	}
	ctx := s.bgCtx

	// Serialize with the sync completion hook: an in-flight sync that captured
	// the preview as enabled must not write native ordering after this
	// reconciliation restores branch inference.
	reconciled := false
	s.syncer.RunUnderStackProjection(func() {
		for _, ref := range s.syncer.TrackedRepos() {
			kind, err := platform.NormalizeKind(string(ref.Platform))
			if err != nil || kind != platform.KindGitHub {
				continue
			}
			host, ok := platform.HostOrDefault(kind, ref.PlatformHost)
			if !ok {
				continue
			}
			repo, err := s.db.GetRepoByIdentity(ctx, db.RepoIdentity{
				Platform: string(kind), PlatformHost: host,
				Owner: ref.Owner, Name: ref.Name,
			})
			if err != nil {
				slog.Warn("reconcile stacks after disabling github native metadata",
					"platform", kind, "host", host,
					"repo", ref.Owner+"/"+ref.Name, "err", err)
				continue
			}
			if repo == nil {
				continue
			}
			if err := stacks.RunDetection(ctx, s.db, repo.ID); err != nil {
				slog.Warn("reconcile stacks after disabling github native metadata",
					"platform", kind, "host", host,
					"repo", ref.Owner+"/"+ref.Name, "err", err)
				continue
			}
			reconciled = true
		}
	})
	if reconciled {
		s.hub.Broadcast(Event{Type: "data_changed", Data: struct{}{}})
	}
}
