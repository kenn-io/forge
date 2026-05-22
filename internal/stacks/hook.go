package stacks

import (
	"context"
	"log/slog"

	"go.kenn.io/middleman/internal/db"
	ghclient "go.kenn.io/middleman/internal/github"
)

// SyncCompletedHook returns a callback for Syncer.SetOnSyncCompleted
// that runs stack detection for each synced repo.
func SyncCompletedHook(ctx context.Context, database *db.DB, next func([]ghclient.RepoSyncResult)) func([]ghclient.RepoSyncResult) {
	return func(results []ghclient.RepoSyncResult) {
		defer func() {
			if next != nil {
				next(results)
			}
		}()
		for _, result := range results {
			if ctx.Err() != nil {
				return
			}
			if result.Error != "" {
				continue
			}
			repo, err := database.GetRepoByIdentity(ctx, db.RepoIdentity{
				Platform:     string(result.Platform),
				PlatformHost: result.PlatformHost,
				Owner:        result.Owner,
				Name:         result.Name,
			})
			if err != nil {
				slog.Error("stack detection: repo lookup failed",
					"platform", result.Platform,
					"host", result.PlatformHost,
					"repo", result.Owner+"/"+result.Name, "err", err)
				continue
			}
			if repo == nil {
				continue
			}
			if err := RunDetection(ctx, database, repo.ID); err != nil {
				slog.Error("stack detection failed",
					"platform", result.Platform,
					"host", result.PlatformHost,
					"repo", result.Owner+"/"+result.Name, "err", err)
			}
		}
	}
}
