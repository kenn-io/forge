package httpapi

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/db"
	ghclient "go.kenn.io/forge/internal/github"
	"go.kenn.io/forge/platform"
)

func NormalizeImportPlatform(provider, host string) (platform.Kind, string, error) {
	if strings.TrimSpace(provider) == "" {
		return "", "", fmt.Errorf("provider is required")
	}
	kind, err := platform.NormalizeKind(provider)
	if err != nil {
		return "", "", err
	}
	normalizedHost, err := config.NormalizePlatformHost(string(kind), host)
	if err != nil {
		return "", "", err
	}
	return kind, normalizedHost, nil
}

func NormalizeUntrackedRepo(provider, host, owner, name string) (db.UntrackedRepoOpen, error) {
	kind, host, err := NormalizeImportPlatform(provider, host)
	if err != nil {
		return db.UntrackedRepoOpen{}, err
	}
	repo := db.UntrackedRepoOpen{Provider: string(kind), Host: host, Owner: strings.Trim(owner, "/ "), Name: strings.Trim(name, "/ ")}
	if err := platform.ValidateCanonicalRepoRef(platform.RepoRef{Platform: kind, Host: host, Owner: repo.Owner, Name: repo.Name}); err != nil {
		return db.UntrackedRepoOpen{}, err
	}
	return repo, nil
}

func RecordUntrackedRepoOpen(ctx context.Context, database *db.DB, syncer *ghclient.Syncer, provider, host, owner, name, itemType string, number int) error {
	repo, err := NormalizeUntrackedRepo(provider, host, owner, name)
	if err != nil {
		slog.Warn("record untracked repository open", "error", err)
		return err
	}
	if syncer.IsTrackedRepoByIdentity(platform.Kind(repo.Provider), repo.Owner, repo.Name, repo.Host) {
		return nil
	}
	slog.Info("opened untracked repository", "provider", repo.Provider, "host", repo.Host, "owner", repo.Owner, "name", repo.Name, "item_type", itemType, "number", number)
	if err := database.RecordUntrackedRepoOpen(ctx, repo); err != nil {
		slog.Warn("record untracked repository open", "error", err)
		return err
	}
	return nil
}
