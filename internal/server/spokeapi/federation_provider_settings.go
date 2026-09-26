package spokeapi

import (
	"time"

	"go.kenn.io/forge/internal/config"
	ghclient "go.kenn.io/forge/internal/github"
)

type ProviderSettingsResponse struct {
	Repos                  []ghclient.ConfiguredRepoStatus `json:"repos" nullable:"false"`
	RepositoryObservations []ProviderRepositoryObservation `json:"repository_observations" nullable:"false"`
	RepoPresets            []config.RepoPreset             `json:"repo_presets" nullable:"false"`
	Activity               config.Activity                 `json:"activity"`
	Detail                 config.Detail                   `json:"detail"`
	PullRequests           config.PullRequests             `json:"pull_requests"`
	Issues                 config.Issues                   `json:"issues"`
	Notifications          NotificationsSettingsResponse   `json:"notifications"`
	Sync                   SyncSettingsResponse            `json:"sync"`
}

type ProviderRepositoryObservation struct {
	Provider       string    `json:"provider"`
	PlatformHost   string    `json:"platform_host"`
	PlatformRepoID string    `json:"platform_repo_id"`
	Owner          string    `json:"owner"`
	Name           string    `json:"name"`
	RepoPath       string    `json:"repo_path"`
	ObservedAt     time.Time `json:"observed_at"`
}

type ProviderSettingsProjection struct {
	Settings               SettingsResponse
	RepositoryObservations []ProviderRepositoryObservation
}

func (settings ProviderSettingsResponse) projection() ProviderSettingsProjection {
	return ProviderSettingsProjection{
		Settings: SettingsResponse{
			Repos: settings.Repos, RepoPresets: settings.RepoPresets,
			Activity: settings.Activity, Detail: settings.Detail,
			PullRequests: settings.PullRequests, Issues: settings.Issues,
			Notifications: settings.Notifications, Sync: settings.Sync,
		},
		RepositoryObservations: settings.RepositoryObservations,
	}
}
