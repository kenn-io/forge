package syncevents

import (
	"context"
	"log/slog"

	"go.kenn.io/forge/internal/activityrelay"
	"go.kenn.io/forge/internal/server/itemapi"
	"go.kenn.io/forge/internal/server/workspaceapi"
)

func (s *Handlers) BroadcastRelayRefresh(ctx context.Context, repoID int64, target string, number int) {
	if target == activityrelay.WorkflowRuns {
		repo, err := s.Db.GetRepoByID(ctx, repoID)
		if err != nil || repo == nil {
			return
		}
		(*s.Hub).Broadcast(Event{Type: "workflow_runs_changed", Data: struct {
			Provider       string `json:"provider"`
			PlatformHost   string `json:"platform_host"`
			PlatformRepoID string `json:"platform_repo_id"`
		}{repo.Platform, repo.PlatformHost, repo.PlatformRepoID}})
		return
	}
	(*s.Hub).Broadcast(Event{Type: "data_changed", Data: struct{}{}})
	if target == activityrelay.PullRequest || target == activityrelay.PullRequestChecks {
		s.BroadcastMergedActorDetailRefresh(ctx, repoID, number)
	}
}

func (s *Handlers) BroadcastMergedActorDetailRefresh(
	ctx context.Context,
	repoID int64,
	number int,
) {
	repo, err := s.Db.GetRepoByID(ctx, repoID)
	if err != nil || repo == nil {
		slog.Warn("load repository for scheduled merged-actor detail refresh",
			"repo_id", repoID, "number", number, "err", err)
		return
	}
	mr, err := s.Db.GetMergeRequestByRepoIDAndNumber(ctx, repoID, number)
	if err != nil || mr == nil {
		slog.Warn("load pull request for scheduled merged-actor detail refresh",
			"repo_id", repoID, "number", number, "err", err)
		return
	}
	(*s.Hub).Broadcast(Event{
		Type: "pr_detail_refreshed",
		Data: workspaceapi.PRDetailRefreshedPayload{
			Provider:     repo.Platform,
			PlatformHost: repo.PlatformHost,
			RepoPath:     repo.RepoPath,
			Owner:        repo.Owner,
			Name:         repo.Name,
			Number:       number,
			HeadSHA:      mr.PlatformHeadSHA,
			SyncedAt:     itemapi.FormatUTCRFC3339((*s.Now)().UTC()),
			Warnings:     []string{},
		},
	})
}
