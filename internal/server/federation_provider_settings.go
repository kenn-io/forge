package server

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"go.kenn.io/forge/internal/server/httpapi"
	"go.kenn.io/forge/internal/server/settingsapi"
	"go.kenn.io/forge/internal/server/syncevents"
)

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
) (*syncevents.FederationProviderSettingsOutput, error) {
	settings, err := s.buildLocalSettingsResponse(ctx)
	if err != nil {
		return nil, httpapi.Internal(err.Error())
	}
	projection, err := s.syncevents.BuildProviderSettingsProjection(ctx, settings)
	if err != nil {
		return nil, httpapi.Internal(err.Error())
	}
	return &syncevents.FederationProviderSettingsOutput{Body: projection}, nil
}

func (s *Server) federationUpdateProviderSettings(
	ctx context.Context, input *syncevents.FederationUpdateProviderSettingsInput,
) (*syncevents.FederationProviderSettingsOutput, error) {
	if _, err := s.updateSettings(ctx, &settingsapi.UpdateSettingsInput{Body: input.Body.SettingsUpdate()}); err != nil {
		return nil, err
	}
	return s.federationGetProviderSettings(ctx, nil)
}
