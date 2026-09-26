package syncevents

import (
	"context"
	"slices"
	"strings"
	"time"

	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/server/httpapi"
	"go.kenn.io/forge/internal/server/spokeapi"
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
	seen := make(map[string]struct{})
	for _, configured := range settings.Repos {
		platformRepoID := strings.TrimSpace(configured.PlatformRepoID)
		if platformRepoID == "" || configured.IsGlob {
			continue
		}
		key := strings.ToLower(configured.Provider) + "\x00" +
			strings.ToLower(configured.PlatformHost) + "\x00" + platformRepoID
		if _, ok := seen[key]; ok {
			continue
		}
		entry, err := s.Db.GetRepositoryByProviderID(
			ctx, configured.Provider, configured.PlatformHost, platformRepoID,
		)
		if err != nil {
			return spokeapi.ProviderSettingsResponse{}, err
		}
		if entry == nil || entry.Lifecycle != db.RepositoryLifecycleActive {
			continue
		}
		var observedAt time.Time
		for _, route := range entry.Routes {
			if route.Current {
				observedAt = route.LastSeenAt
				break
			}
		}
		if observedAt.IsZero() {
			continue
		}
		seen[key] = struct{}{}
		projection.RepositoryObservations = append(
			projection.RepositoryObservations,
			spokeapi.ProviderRepositoryObservation{
				Provider: entry.Repository.Platform, PlatformHost: entry.Repository.PlatformHost,
				PlatformRepoID: entry.Repository.PlatformRepoID,
				Owner:          entry.Repository.Owner, Name: entry.Repository.Name,
				RepoPath: entry.Repository.RepoPath, ObservedAt: observedAt,
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
