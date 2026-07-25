package server

import (
	"context"
	"log/slog"

	"go.kenn.io/middleman/internal/db"
	"go.kenn.io/middleman/internal/platform"
	"go.kenn.io/middleman/internal/stacks"
)

// applyGitHubNativeStackPreference reconciles on the transition the swap itself
// performed rather than on a config snapshot read earlier: concurrent config
// writers each hold their own snapshot, and reconciling from those could run
// the restore twice or skip it entirely.
func (s *Server) applyGitHubNativeStackPreference(
	ctx context.Context,
	enabled bool,
) {
	if s.syncer == nil {
		return
	}
	previous := s.syncer.SetPreferGitHubNativeStacks(enabled)
	if !previous || enabled {
		return
	}

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
