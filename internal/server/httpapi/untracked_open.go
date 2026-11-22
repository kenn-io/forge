package httpapi

import (
	"context"
	"log/slog"
	"strings"

	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/platform"
)

func NormalizeUntrackedRepo(provider, host, owner, name string) (db.UntrackedRepoOpen, error) {
	kind, err := platform.NormalizeKind(provider)
	if err != nil {
		return db.UntrackedRepoOpen{}, err
	}
	host, err = config.NormalizePlatformHost(string(kind), host)
	if err != nil {
		return db.UntrackedRepoOpen{}, err
	}
	repo := db.UntrackedRepoOpen{Provider: string(kind), Host: host, Owner: strings.Trim(owner, "/ "), Name: strings.Trim(name, "/ ")}
	if err := platform.ValidateCanonicalRepoRef(platform.RepoRef{Platform: kind, Host: host, Owner: repo.Owner, Name: repo.Name}); err != nil {
		return db.UntrackedRepoOpen{}, err
	}
	return repo, nil
}

func RecordUntrackedRepoOpen(ctx context.Context, database *db.DB, provider, host, owner, name, itemType string, number int) error {
	repo, err := NormalizeUntrackedRepo(provider, host, owner, name)
	if err != nil {
		slog.Warn("record untracked repository open", "error", err)
		return err
	}
	return RecordNormalizedUntrackedRepoOpen(ctx, database, repo, itemType, number)
}

func RecordNormalizedUntrackedRepoOpen(ctx context.Context, database *db.DB, repo db.UntrackedRepoOpen, itemType string, number int) error {
	slog.Info("opened untracked repository", "provider", repo.Provider, "host", repo.Host, "owner", repo.Owner, "name", repo.Name, "item_type", itemType, "number", number)
	if err := database.RecordUntrackedRepoOpen(ctx, repo); err != nil {
		slog.Warn("record untracked repository open", "error", err)
		return err
	}
	return nil
}
