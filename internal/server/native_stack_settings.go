package server

import (
	"context"
	"log/slog"

	"go.kenn.io/middleman/internal/db"
	"go.kenn.io/middleman/internal/platform"
	"go.kenn.io/middleman/internal/stacks"
)

func (s *Server) applyGitHubNativeStackPreference(
	ctx context.Context,
	previous, enabled bool,
) {
	if s.syncer == nil {
		return
	}
	s.syncer.SetPreferGitHubNativeStacks(enabled)
	if !previous || enabled {
		return
	}

	reconciled := false
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
	if reconciled {
		s.hub.Broadcast(Event{Type: "data_changed", Data: struct{}{}})
	}
}
