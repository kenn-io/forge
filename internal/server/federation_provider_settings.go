package server

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/db"
	ghclient "go.kenn.io/forge/internal/github"
	"go.kenn.io/forge/internal/server/httpapi"
)

type providerSettingsResponse struct {
	Repos                  []ghclient.ConfiguredRepoStatus `json:"repos" nullable:"false"`
	RepositoryObservations []providerRepositoryObservation `json:"repository_observations" nullable:"false"`
	RepoPresets            []config.RepoPreset             `json:"repo_presets" nullable:"false"`
	Activity               config.Activity                 `json:"activity"`
	Detail                 config.Detail                   `json:"detail"`
	PullRequests           config.PullRequests             `json:"pull_requests"`
	Issues                 config.Issues                   `json:"issues"`
	Notifications          notificationsSettingsResponse   `json:"notifications"`
}

type providerRepositoryObservation struct {
	Provider       string    `json:"provider"`
	PlatformHost   string    `json:"platform_host"`
	PlatformRepoID string    `json:"platform_repo_id"`
	Owner          string    `json:"owner"`
	Name           string    `json:"name"`
	RepoPath       string    `json:"repo_path"`
	ObservedAt     time.Time `json:"observed_at"`
}

type providerSettingsProjection struct {
	Settings               settingsResponse
	RepositoryObservations []providerRepositoryObservation
}

type providerSettingsUpdate struct {
	Activity     *config.Activity     `json:"activity,omitempty"`
	Detail       *config.Detail       `json:"detail,omitempty"`
	PullRequests *config.PullRequests `json:"pull_requests,omitempty"`
	Issues       *config.Issues       `json:"issues,omitempty"`
}

type federationProviderSettingsOutput = httpapi.BodyOutput[providerSettingsResponse]

type federationUpdateProviderSettingsInput struct {
	Body providerSettingsUpdate
}

func (s *Server) registerFederationProviderSettingsAPI(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID: "federation-get-provider-settings",
		Method:      http.MethodGet,
		Path:        "/federation/provider/settings",
		Summary:     "Get hub-owned settings for a Forge spoke",
		Tags:        []string{"Fleet"},
	}, s.federationGetProviderSettings)
	huma.Register(api, huma.Operation{
		OperationID: "federation-update-provider-settings",
		Method:      http.MethodPut,
		Path:        "/federation/provider/settings",
		Summary:     "Update hub-owned settings for a Forge spoke",
		Tags:        []string{"Fleet"},
	}, s.federationUpdateProviderSettings)
}

func (s *Server) federationGetProviderSettings(
	ctx context.Context, _ *struct{},
) (*federationProviderSettingsOutput, error) {
	settings, err := s.buildLocalSettingsResponse(ctx)
	if err != nil {
		return nil, httpapi.Internal(err.Error())
	}
	projection, err := s.buildProviderSettingsProjection(ctx, settings)
	if err != nil {
		return nil, httpapi.Internal(err.Error())
	}
	return &federationProviderSettingsOutput{Body: projection}, nil
}

func (s *Server) federationUpdateProviderSettings(
	ctx context.Context, input *federationUpdateProviderSettingsInput,
) (*federationProviderSettingsOutput, error) {
	if _, err := s.updateSettings(ctx, &updateSettingsInput{Body: input.Body.settingsUpdate()}); err != nil {
		return nil, err
	}
	return s.federationGetProviderSettings(ctx, nil)
}

func providerSettingsFrom(settings settingsResponse) providerSettingsResponse {
	repos := slices.Clone(settings.Repos)
	for i := range repos {
		repos[i].WorktreeBasePath = ""
	}
	return providerSettingsResponse{
		Repos:                  repos,
		RepositoryObservations: make([]providerRepositoryObservation, 0),
		RepoPresets:            cloneRepoPresets(settings.RepoPresets),
		Activity:               settings.Activity, Detail: settings.Detail,
		PullRequests: settings.PullRequests, Issues: settings.Issues,
		Notifications: settings.Notifications,
	}
}

func (s *Server) buildProviderSettingsProjection(
	ctx context.Context,
	settings settingsResponse,
) (providerSettingsResponse, error) {
	projection := providerSettingsFrom(settings)
	if s.db == nil {
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
		entry, err := s.db.GetRepositoryByProviderID(
			ctx, configured.Provider, configured.PlatformHost, platformRepoID,
		)
		if err != nil {
			return providerSettingsResponse{}, err
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
			providerRepositoryObservation{
				Provider: entry.Repository.Platform, PlatformHost: entry.Repository.PlatformHost,
				PlatformRepoID: entry.Repository.PlatformRepoID,
				Owner:          entry.Repository.Owner, Name: entry.Repository.Name,
				RepoPath: entry.Repository.RepoPath, ObservedAt: observedAt,
			},
		)
	}
	return projection, nil
}

func (settings providerSettingsResponse) projection() providerSettingsProjection {
	return providerSettingsProjection{
		Settings: settingsResponse{
			Repos: settings.Repos, RepoPresets: settings.RepoPresets,
			Activity: settings.Activity, Detail: settings.Detail,
			PullRequests: settings.PullRequests, Issues: settings.Issues,
			Notifications: settings.Notifications,
		},
		RepositoryObservations: settings.RepositoryObservations,
	}
}

func providerSettingsUpdateFrom(update updateSettingsRequest) providerSettingsUpdate {
	return providerSettingsUpdate{
		Activity: update.Activity, Detail: update.Detail,
		PullRequests: update.PullRequests, Issues: update.Issues,
	}
}

func (update providerSettingsUpdate) settingsUpdate() updateSettingsRequest {
	return updateSettingsRequest{
		Activity: update.Activity, Detail: update.Detail,
		PullRequests: update.PullRequests, Issues: update.Issues,
	}
}
