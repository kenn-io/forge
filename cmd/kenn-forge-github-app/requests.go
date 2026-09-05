package main

import (
	"context"
	"time"

	"go.kenn.io/forge/githubapp"
	"go.kenn.io/forge/internal/archive/report"
	"go.kenn.io/forge/platform"
)

// Each API operation (including a complete paged discovery) shares the archive
// report envelope and the setup client's existing 30-second request deadline.
// Browser interaction happens outside this scope.
func appRequest[T any](ctx context.Context, read func(context.Context, *platform.Meter) (T, error)) (T, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	meter, err := platform.NewMeter(ctx, platform.Budget{MaxRecords: report.MaxDetailedRecords, MaxNodes: report.MaxDetailedRecords, MaxBytes: report.MaxDetailedTextBytes, MaxOutputBytes: report.MaxDetailedTextBytes})
	if err != nil {
		var zero T
		return zero, err
	}
	return read(ctx, meter)
}

func discoverInstallations(ctx context.Context, client *githubapp.Client, jwt string, appID int64) ([]githubapp.Installation, error) {
	return appRequest(ctx, func(ctx context.Context, meter *platform.Meter) ([]githubapp.Installation, error) {
		return platform.CollectAllPages(ctx, "", func(ctx context.Context, cursor string) (platform.Page[githubapp.Installation], error) {
			return client.ListInstallationsPage(ctx, jwt, appID, githubapp.PageQuery{Size: 100, Cursor: cursor}, meter)
		})
	})
}

func discoverRepositoryNames(ctx context.Context, client *githubapp.Client, token string, installationID int64) ([]string, error) {
	return appRequest(ctx, func(ctx context.Context, meter *platform.Meter) ([]string, error) {
		repos, err := platform.CollectAllPages(ctx, "", func(ctx context.Context, cursor string) (platform.Page[githubapp.Repository], error) {
			return client.ListInstallationRepositoriesPage(ctx, token, installationID, githubapp.PageQuery{Size: 100, Cursor: cursor}, meter)
		})
		if err != nil {
			return nil, err
		}
		names := make([]string, len(repos))
		for i, repo := range repos {
			names[i] = repo.FullName
		}
		return names, nil
	})
}
