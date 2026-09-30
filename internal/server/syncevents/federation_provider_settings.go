package syncevents

import (
	"context"
	"slices"

	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/server/httpapi"
	"go.kenn.io/forge/internal/server/spokeapi"
	"go.kenn.io/forge/platform"
)

type ProviderSettingsUpdate struct {
	Activity     *config.Activity             `json:"activity,omitempty"`
	Detail       *config.Detail               `json:"detail,omitempty"`
	PullRequests *config.PullRequests         `json:"pull_requests,omitempty"`
	Issues       *config.Issues               `json:"issues,omitempty"`
	Sync         *spokeapi.SyncSettingsUpdate `json:"sync,omitempty"`
}

type FederationProviderSettingsOutput = httpapi.BodyOutput[spokeapi.ProviderSettingsResponse]

type FederationUpdateProviderSettingsInput struct {
	Body ProviderSettingsUpdate
}

func providerSettingsFrom(settings spokeapi.SettingsResponse) spokeapi.ProviderSettingsResponse {
	repos := slices.Clone(settings.Repos)
	for i := range repos {
		repos[i].WorktreeBasePath = ""
	}
	return spokeapi.ProviderSettingsResponse{
		Repos:                  repos,
		RepositoryObservations: make([]spokeapi.ProviderRepositoryObservation, 0),
		RepoPresets:            spokeapi.CloneRepoPresets(settings.RepoPresets),
		Activity:               settings.Activity, Detail: settings.Detail,
		PullRequests: settings.PullRequests, Issues: settings.Issues,
		Notifications: settings.Notifications, Sync: settings.Sync,
	}
}

func (s *Handlers) BuildProviderSettingsProjection(
	ctx context.Context,
	settings spokeapi.SettingsResponse,
) (spokeapi.ProviderSettingsResponse, error) {
	projection := providerSettingsFrom(settings)
	if s.Db == nil {
		return projection, nil
	}
	seen := make(map[platform.RepositoryIdentity]struct{})
	for _, configured := range settings.Repos {
		if configured.Key.IsZero() || configured.IsGlob {
			continue
		}
		identity := platform.RepositoryIdentity{
			Provider: configured.Provider, PlatformHost: configured.PlatformHost,
			Key: configured.Key,
		}.Canonical()
		if _, ok := seen[identity]; ok {
			continue
		}
		entry, err := s.Db.GetRepositoryByProviderID(ctx, identity)
		if err != nil {
			return spokeapi.ProviderSettingsResponse{}, err
		}
		if entry == nil || entry.Lifecycle != db.RepositoryLifecycleActive {
			continue
		}
		seen[identity] = struct{}{}
		projection.RepositoryObservations = append(
			projection.RepositoryObservations,
			spokeapi.ProviderRepositoryObservation{
				Provider: entry.Repository.Platform, PlatformHost: entry.Repository.PlatformHost,
				Key:   entry.Repository.Key,
				Owner: entry.Repository.Owner, Name: entry.Repository.Name,
				RepoPath: entry.Repository.RepoPath,
			},
		)
	}
	return projection, nil
}

func (update ProviderSettingsUpdate) SettingsUpdate() spokeapi.UpdateSettingsRequest {
	return spokeapi.UpdateSettingsRequest{
		Activity: update.Activity, Detail: update.Detail,
		PullRequests: update.PullRequests, Issues: update.Issues,
		Sync: update.Sync,
	}
}
