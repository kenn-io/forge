package server

import (
	"context"
	"errors"
	"net/http"
	"slices"

	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/federation"
	"go.kenn.io/forge/internal/server/httpapi"

	"github.com/danielgtaylor/huma/v2"
)

func (s *Server) persistFleetMember(
	ctx context.Context, member config.FleetMember,
) error {
	return s.mutatePersistedFleet(ctx, func(fleet *config.Fleet) {
		for index := range fleet.Members {
			if fleet.Members[index].NodeID == member.NodeID {
				fleet.Members[index] = member
				return
			}
		}
		fleet.Members = append(fleet.Members, member)
	})
}

func (s *Server) persistHubBinding(
	ctx context.Context, hub config.FleetHub,
) error {
	return s.mutatePersistedFleet(ctx, func(fleet *config.Fleet) {
		fleet.Hub = &hub
	})
}

func (s *Server) resetPreparedSpokeBinding(ctx context.Context) error {
	return s.mutatePersistedFleet(ctx, func(fleet *config.Fleet) {
		fleet.Role = config.FleetRoleHub
		fleet.Hub = nil
	})
}

func (s *Server) removeFleetMember(ctx context.Context, nodeID string) error {
	return s.mutatePersistedFleet(ctx, func(fleet *config.Fleet) {
		fleet.Members = slices.DeleteFunc(
			fleet.Members,
			func(member config.FleetMember) bool { return member.NodeID == nodeID },
		)
	})
}

func (s *Server) mutatePersistedFleet(
	ctx context.Context, mutate func(*config.Fleet),
) error {
	return s.mutatePersistedFleetChecked(ctx, func(fleet *config.Fleet) error {
		mutate(fleet)
		return nil
	})
}

func (s *Server) mutatePersistedFleetChecked(
	ctx context.Context, mutate func(*config.Fleet) error,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.cfgPath == "" || s.cfg == nil {
		return errors.New("fleet settings persistence is unavailable")
	}
	s.configReloadMu.Lock()
	defer s.configReloadMu.Unlock()
	s.cfgMu.Lock()
	defer s.cfgMu.Unlock()
	candidate := cloneReloadedConfig(s.cfg)
	active := s.activeFleetConfigSnapshotLocked().Fleet
	candidate.Fleet.Role = active.Role
	candidate.Fleet.BaseURL = active.BaseURL
	candidate.Fleet.Hub = active.Hub
	if err := mutate(&candidate.Fleet); err != nil {
		return err
	}
	if err := candidate.Validate(); err != nil {
		return err
	}
	if err := candidate.Save(s.cfgPath); err != nil {
		return err
	}
	s.cfg.Fleet = candidate.Fleet
	s.applyFleetConfigLocked()
	return nil
}

type getSettingsOutput = httpapi.BodyOutput[settingsResponse]

type updateSettingsInput struct {
	Body updateSettingsRequest
}

type createRepoPresetInput struct {
	Body config.RepoPreset
}

type updateRepoPresetInput struct {
	Name string `path:"name"`
	Body struct {
		Repos []config.RepoPresetRepository `json:"repos" nullable:"false"`
	}
}

type deleteRepoPresetInput struct {
	Name string `path:"name"`
}

type addRepoInput struct {
	Body struct {
		Provider     string `json:"provider"`
		Host         string `json:"host,omitempty"`
		PlatformHost string `json:"platform_host,omitempty"`
		Owner        string `json:"owner"`
		Name         string `json:"name"`
	}
}

type repoConfigInput struct {
	Provider     string `path:"provider"`
	PlatformHost string
	Owner        string `path:"owner"`
	Name         string `path:"name"`
}

type repoConfigHostInput struct {
	Provider     string `path:"provider"`
	PlatformHost string `path:"platform_host"`
	Owner        string `path:"owner"`
	Name         string `path:"name"`
}

type repoWorktreeBaseRequest struct {
	WorktreeBasePath string `json:"worktree_base_path"`
}

type repoWorktreeBaseInput struct {
	Provider     string `path:"provider"`
	PlatformHost string
	Owner        string `path:"owner"`
	Name         string `path:"name"`
	Body         repoWorktreeBaseRequest
}

type repoWorktreeBaseHostInput struct {
	Provider     string `path:"provider"`
	PlatformHost string `path:"platform_host"`
	Owner        string `path:"owner"`
	Name         string `path:"name"`
	Body         repoWorktreeBaseRequest
}

type repoUIVisibilityRequest struct {
	Hidden bool `json:"hidden"`
}

type repoUIVisibilityInput struct {
	Provider     string `path:"provider"`
	PlatformHost string
	Owner        string `path:"owner"`
	Name         string `path:"name"`
	Body         repoUIVisibilityRequest
}

type repoUIVisibilityHostInput struct {
	Provider     string `path:"provider"`
	PlatformHost string `path:"platform_host"`
	Owner        string `path:"owner"`
	Name         string `path:"name"`
	Body         repoUIVisibilityRequest
}

type settingsOutput = httpapi.BodyOutput[settingsResponse]

type setActiveWorktreeInput struct {
	Body struct {
		// Key is the focused worktree's scoped key; empty clears
		// the focus.
		Key string `json:"key"`
	}
}

// setActiveWorktree records which worktree has focus in the client
// driving this daemon (a native panel, an embedding shell). The SPA
// reads the key from its served config to scope navigations to the
// focused worktree's repository.
func (s *Server) setActiveWorktree(
	_ context.Context, in *setActiveWorktreeInput,
) (*struct{}, error) {
	s.SetActiveWorktreeKey(in.Body.Key)
	return &struct{}{}, nil
}

type fleetSettingsResponse struct {
	Enabled         bool                    `json:"enabled"`
	Role            config.FleetRole        `json:"role"`
	Hub             *config.FleetHub        `json:"hub,omitempty"`
	Members         []config.FleetMember    `json:"members" nullable:"false"`
	Enrollments     []federation.Enrollment `json:"enrollments" nullable:"false"`
	PeerTimeout     string                  `json:"peer_timeout,omitempty"`
	Sessions        config.FleetSessions    `json:"sessions"`
	RestartRequired bool                    `json:"restart_required"`
}

type getFleetSettingsOutput = httpapi.BodyOutput[fleetSettingsResponse]

type updateFleetSettingsInput struct {
	Body struct {
		Enabled     bool                 `json:"enabled"`
		PeerTimeout string               `json:"peer_timeout,omitempty"`
		Sessions    config.FleetSessions `json:"sessions"`
	}
}

func (s *Server) buildFleetSettingsResponseLocked() fleetSettingsResponse {
	fleet := s.cfg.Fleet
	return fleetSettingsResponse{
		Enabled:         fleet.Enabled,
		Role:            fleet.RoleOrDefault(),
		Hub:             cloneFleetHub(fleet.Hub),
		Members:         append([]config.FleetMember{}, fleet.Members...),
		Enrollments:     append([]federation.Enrollment{}, s.fleetAPI.Enrollments()...),
		PeerTimeout:     fleet.PeerTimeout,
		Sessions:        fleet.Sessions,
		RestartRequired: s.fleetSettingsRestartRequiredLocked(fleet),
	}
}

func (s *Server) fleetSettingsRestartRequiredLocked(fleet config.Fleet) bool {
	candidate := cloneReloadedConfig(s.cfg)
	candidate.Fleet = fleet
	return s.bootCfgSnapshot.restartRequiredFor(&candidate)
}

// getFleetSettings returns the complete fleet federation settings shape.
func (s *Server) getFleetSettings(
	_ context.Context, _ *struct{},
) (*getFleetSettingsOutput, error) {
	if s.cfgPath == "" {
		return nil, httpapi.NotFound(
			httpapi.CodeSettingsUnavailable, "settings not available", nil,
		)
	}
	s.cfgMu.Lock()
	out := s.buildFleetSettingsResponseLocked()
	s.cfgMu.Unlock()
	return &getFleetSettingsOutput{Body: out}, nil
}

// updateFleetSettings changes only operator preferences. Enrollment owns role,
// hub binding, and membership, so an ordinary settings save cannot
// overwrite those lifecycle fields with a stale browser snapshot.
func (s *Server) updateFleetSettings(
	_ context.Context, input *updateFleetSettingsInput,
) (*getFleetSettingsOutput, error) {
	if s.cfgPath == "" {
		return nil, httpapi.NotFound(
			httpapi.CodeSettingsUnavailable, "settings not available", nil,
		)
	}
	s.configReloadMu.Lock()
	defer s.configReloadMu.Unlock()

	s.cfgMu.Lock()
	candidate := cloneReloadedConfig(s.cfg)
	candidate.Fleet.Enabled = input.Body.Enabled
	candidate.Fleet.PeerTimeout = input.Body.PeerTimeout
	candidate.Fleet.Sessions = input.Body.Sessions
	if err := candidate.Validate(); err != nil {
		s.cfgMu.Unlock()
		return nil, httpapi.BadRequest(httpapi.CodeBadRequest, err.Error(), nil)
	}
	if err := candidate.Save(s.cfgPath); err != nil {
		s.cfgMu.Unlock()
		return nil, httpapi.Internal("save config: " + err.Error())
	}
	s.cfg.Fleet = candidate.Fleet
	s.applyFleetConfigLocked()
	out := s.buildFleetSettingsResponseLocked()
	s.cfgMu.Unlock()
	return &getFleetSettingsOutput{Body: out}, nil
}

func cloneFleetHub(in *config.FleetHub) *config.FleetHub {
	if in == nil {
		return nil
	}
	clone := *in
	return &clone
}

func (s *Server) registerSettingsAPI(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID: "get-fleet-settings",
		Method:      http.MethodGet,
		Path:        "/settings/fleet",
		Summary:     "Get fleet settings",
		Tags:        []string{"Settings"},
	}, s.getFleetSettings)
	huma.Register(api, huma.Operation{
		OperationID: "update-fleet-settings",
		Method:      http.MethodPut,
		Path:        "/settings/fleet",
		Summary:     "Update fleet settings",
		Tags:        []string{"Settings"},
	}, s.updateFleetSettings)
	huma.Register(api, huma.Operation{
		OperationID:   "set-active-worktree",
		Method:        http.MethodPut,
		Path:          "/ui/active-worktree",
		DefaultStatus: http.StatusNoContent,
		Summary:       "Set the focused worktree",
		Tags:          []string{"Settings"},
	}, s.setActiveWorktree)
	huma.Register(api, huma.Operation{
		OperationID: "get-settings",
		Method:      http.MethodGet,
		Path:        "/settings",
		Summary:     "Get settings",
		Tags:        []string{"Settings"},
	}, s.getSettings)
	huma.Register(api, huma.Operation{
		OperationID: "update-settings",
		Method:      http.MethodPut,
		Path:        "/settings",
		Summary:     "Update settings",
		Tags:        []string{"Settings"},
	}, s.updateSettings)
	huma.Register(api, huma.Operation{
		OperationID: "create-repo-preset",
		Method:      http.MethodPost, Path: "/settings/repo-presets",
		DefaultStatus: http.StatusCreated,
		Summary:       "Create repository preset", Tags: []string{"Settings"},
	}, s.createRepoPreset)
	huma.Register(api, huma.Operation{
		OperationID: "update-repo-preset",
		Method:      http.MethodPut, Path: "/settings/repo-presets/{name}",
		Summary: "Update repository preset", Tags: []string{"Settings"},
	}, s.updateRepoPreset)
	huma.Register(api, huma.Operation{
		OperationID: "delete-repo-preset",
		Method:      http.MethodDelete, Path: "/settings/repo-presets/{name}",
		Summary: "Delete repository preset", Tags: []string{"Settings"},
	}, s.deleteRepoPreset)
	huma.Register(api, huma.Operation{
		OperationID:   "add-repo",
		Method:        http.MethodPost,
		Path:          "/repos",
		DefaultStatus: http.StatusCreated,
		Summary:       "Add repository",
		Tags:          []string{"Settings"},
	}, s.addConfiguredRepo)
	huma.Register(api, huma.Operation{
		OperationID: "refresh-repo",
		Method:      http.MethodPost,
		Path:        "/repo/{provider}/{owner}/{name}/refresh",
		Summary:     "Refresh repository",
		Tags:        []string{"Settings"},
	}, s.refreshConfiguredRepo)
	huma.Register(api, huma.Operation{
		OperationID: "refresh-repo-on-host",
		Method:      http.MethodPost,
		Path:        "/host/{platform_host}/repo/{provider}/{owner}/{name}/refresh",
		Summary:     "Refresh repository",
		Tags:        []string{"Settings"},
	}, s.refreshConfiguredRepoOnHost)
	huma.Register(api, huma.Operation{
		OperationID: "update-repo-worktree-base",
		Method:      http.MethodPut,
		Path:        "/repo/{provider}/{owner}/{name}/worktree-base",
		Summary:     "Update repository worktree base",
		Tags:        []string{"Settings"},
	}, s.updateConfiguredRepoWorktreeBase)
	huma.Register(api, huma.Operation{
		OperationID: "update-repo-worktree-base-on-host",
		Method:      http.MethodPut,
		Path:        "/host/{platform_host}/repo/{provider}/{owner}/{name}/worktree-base",
		Summary:     "Update repository worktree base",
		Tags:        []string{"Settings"},
	}, s.updateConfiguredRepoWorktreeBaseOnHost)
	huma.Register(api, huma.Operation{
		OperationID: "update-repo-ui-visibility",
		Method:      http.MethodPut,
		Path:        "/repo/{provider}/{owner}/{name}/ui-visibility",
		Summary:     "Update repository UI visibility",
		Tags:        []string{"Settings"},
	}, s.updateConfiguredRepoUIVisibility)
	huma.Register(api, huma.Operation{
		OperationID: "update-repo-ui-visibility-on-host",
		Method:      http.MethodPut,
		Path:        "/host/{platform_host}/repo/{provider}/{owner}/{name}/ui-visibility",
		Summary:     "Update repository UI visibility",
		Tags:        []string{"Settings"},
	}, s.updateConfiguredRepoUIVisibilityOnHost)
	huma.Register(api, huma.Operation{
		OperationID:   "delete-repo",
		Method:        http.MethodDelete,
		Path:          "/repo/{provider}/{owner}/{name}",
		DefaultStatus: http.StatusNoContent,
		Summary:       "Delete repository",
		Tags:          []string{"Settings"},
	}, s.deleteConfiguredRepo)
	huma.Register(api, huma.Operation{
		OperationID:   "delete-repo-on-host",
		Method:        http.MethodDelete,
		Path:          "/host/{platform_host}/repo/{provider}/{owner}/{name}",
		DefaultStatus: http.StatusNoContent,
		Summary:       "Delete repository",
		Tags:          []string{"Settings"},
	}, s.deleteConfiguredRepoOnHost)
}
