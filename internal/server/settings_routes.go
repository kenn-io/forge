package server

import (
	"context"
	"net/http"

	"go.kenn.io/middleman/internal/config"

	"github.com/danielgtaylor/huma/v2"
)

type getSettingsOutput = bodyOutput[settingsResponse]

type updateSettingsInput struct {
	Body updateSettingsRequest
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

type settingsOutput = bodyOutput[settingsResponse]

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

type fleetSSHPeersBody struct {
	SSHPeers []config.FleetSSHPeer `json:"ssh_peers" nullable:"false"`
	// RestartRequired reports whether the persisted peer set differs
	// from the one the running SSH transport was wired with — the
	// transport is constructed at startup, so edits apply on the
	// next daemon start.
	RestartRequired bool `json:"restart_required"`
}

type fleetSSHPeersOutput = bodyOutput[fleetSSHPeersBody]

type updateFleetSSHPeersInput struct {
	Body struct {
		SSHPeers []config.FleetSSHPeer `json:"ssh_peers" nullable:"false"`
	}
}

// getFleetSSHPeers lists the configured ssh fleet peers.
func (s *Server) getFleetSSHPeers(
	_ context.Context, _ *struct{},
) (*fleetSSHPeersOutput, error) {
	if s.cfgPath == "" {
		return nil, problemNotFound(
			CodeSettingsUnavailable, "settings not available", nil,
		)
	}
	s.cfgMu.Lock()
	peers := append(
		[]config.FleetSSHPeer(nil), s.cfg.Fleet.SSHPeers...,
	)
	s.cfgMu.Unlock()
	return &fleetSSHPeersOutput{Body: fleetSSHPeersBody{
		SSHPeers:        peers,
		RestartRequired: s.fleetSSHPeersRestartRequired(peers),
	}}, nil
}

// updateFleetSSHPeers replaces the configured ssh peer set. The set
// is validated as a whole and persisted; a failed validation or save
// rolls the in-memory config back.
func (s *Server) updateFleetSSHPeers(
	_ context.Context, input *updateFleetSSHPeersInput,
) (*fleetSSHPeersOutput, error) {
	if s.cfgPath == "" {
		return nil, problemNotFound(
			CodeSettingsUnavailable, "settings not available", nil,
		)
	}
	next := append(
		[]config.FleetSSHPeer(nil), input.Body.SSHPeers...,
	)
	s.cfgMu.Lock()
	prev := s.cfg.Fleet.SSHPeers
	s.cfg.Fleet.SSHPeers = next
	if err := s.cfg.Validate(); err != nil {
		s.cfg.Fleet.SSHPeers = prev
		s.cfgMu.Unlock()
		return nil, problemBadRequest(CodeBadRequest, err.Error(), nil)
	}
	if err := s.cfg.Save(s.cfgPath); err != nil {
		s.cfg.Fleet.SSHPeers = prev
		s.cfgMu.Unlock()
		return nil, problemInternal("save config: " + err.Error())
	}
	persisted := append([]config.FleetSSHPeer(nil), s.cfg.Fleet.SSHPeers...)
	s.cfgMu.Unlock()
	return &fleetSSHPeersOutput{Body: fleetSSHPeersBody{
		SSHPeers:        persisted,
		RestartRequired: s.fleetSSHPeersRestartRequired(persisted),
	}}, nil
}

// fleetSSHPeersRestartRequired compares the persisted peer set with
// the one the running transport was constructed from.
func (s *Server) fleetSSHPeersRestartRequired(
	persisted []config.FleetSSHPeer,
) bool {
	var running []config.FleetSSHPeer
	if s.sshFleet != nil {
		running = s.sshFleet.snapshotPeers()
	}
	if len(persisted) != len(running) {
		return len(persisted) != 0 || len(running) != 0
	}
	for i := range persisted {
		if persisted[i] != running[i] {
			return true
		}
	}
	return false
}

func (s *Server) registerSettingsAPI(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID: "get-fleet-ssh-peers",
		Method:      http.MethodGet,
		Path:        "/settings/fleet/ssh-peers",
		Summary:     "List SSH fleet peers",
		Tags:        []string{"Settings"},
	}, s.getFleetSSHPeers)
	huma.Register(api, huma.Operation{
		OperationID: "update-fleet-ssh-peers",
		Method:      http.MethodPut,
		Path:        "/settings/fleet/ssh-peers",
		Summary:     "Replace SSH fleet peers",
		Tags:        []string{"Settings"},
	}, s.updateFleetSSHPeers)
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
