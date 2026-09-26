package server

import (
	"context"
	"net/http"

	"go.kenn.io/forge/internal/server/settingsapi"

	"github.com/danielgtaylor/huma/v2"
)

// setActiveWorktree records which worktree has focus in the client
// driving this daemon (a native panel, an embedding shell). The SPA
// reads the key from its served config to scope navigations to the
// focused worktree's repository.
func (s *Server) setActiveWorktree(
	_ context.Context, in *settingsapi.SetActiveWorktreeInput,
) (*struct{}, error) {
	s.SetActiveWorktreeKey(in.Body.Key)
	return &struct{}{}, nil
}

func (s *Server) registerSettingsAPI(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID: "get-fleet-settings",
		Method:      http.MethodGet,
		Path:        "/settings/fleet",
		Summary:     "Get fleet settings",
		Tags:        []string{"Settings"},
	}, s.settingsapi.GetFleetSettings)
	huma.Register(api, huma.Operation{
		OperationID: "update-fleet-settings",
		Method:      http.MethodPut,
		Path:        "/settings/fleet",
		Summary:     "Update fleet settings",
		Tags:        []string{"Settings"},
	}, s.settingsapi.UpdateFleetSettings)
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
		OperationID: "get-local-settings",
		Method:      http.MethodGet,
		Path:        "/settings/local",
		Summary:     "Get settings owned by this Forge without contacting a fleet hub",
		Tags:        []string{"Settings"},
	}, s.getLocalSettings)
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
	}, s.settingsapi.DeleteConfiguredRepo)
	huma.Register(api, huma.Operation{
		OperationID:   "delete-repo-on-host",
		Method:        http.MethodDelete,
		Path:          "/host/{platform_host}/repo/{provider}/{owner}/{name}",
		DefaultStatus: http.StatusNoContent,
		Summary:       "Delete repository",
		Tags:          []string{"Settings"},
	}, s.settingsapi.DeleteConfiguredRepoOnHost)
}
