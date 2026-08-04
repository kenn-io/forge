package server

import (
	"context"
	"log/slog"

	"go.kenn.io/forge/internal/server/workspaceapi"
)

func (s *Server) broadcastMergedActorDetailRefresh(
	ctx context.Context,
	repoID int64,
	number int,
) {
	repo, err := s.db.GetRepoByID(ctx, repoID)
	if err != nil || repo == nil {
		slog.Warn("load repository for scheduled merged-actor detail refresh",
			"repo_id", repoID, "number", number, "err", err)
		return
	}
	mr, err := s.db.GetMergeRequestByRepoIDAndNumber(ctx, repoID, number)
	if err != nil || mr == nil {
		slog.Warn("load pull request for scheduled merged-actor detail refresh",
			"repo_id", repoID, "number", number, "err", err)
		return
	}
	s.hub.Broadcast(Event{
		Type: "pr_detail_refreshed",
		Data: workspaceapi.PRDetailRefreshedPayload{
			Provider:     repo.Platform,
			PlatformHost: repo.PlatformHost,
			RepoPath:     repo.RepoPath,
			Owner:        repo.Owner,
			Name:         repo.Name,
			Number:       number,
			HeadSHA:      mr.PlatformHeadSHA,
			SyncedAt:     formatUTCRFC3339(s.now().UTC()),
			Warnings:     []string{},
		},
	})
}
