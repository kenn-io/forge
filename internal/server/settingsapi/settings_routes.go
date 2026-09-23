package settingsapi

import (
	"context"
	"errors"
	"slices"

	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/federation"
	"go.kenn.io/forge/internal/server/configreload"
	"go.kenn.io/forge/internal/server/httpapi"
	"go.kenn.io/forge/internal/server/spokeapi"
)

func (s *Handlers) PersistFleetMember(
	ctx context.Context, member config.FleetMember,
) error {
	return s.mutatePersistedFleet(ctx, func(fleet *config.Fleet) {
		for index := range fleet.Members {
			if fleet.Members[index].NodeID == member.NodeID {
				member.OutboundDisabled = fleet.Members[index].OutboundDisabled
				fleet.Members[index] = member
				return
			}
		}
		fleet.Members = append(fleet.Members, member)
	})
}

func (s *Handlers) PersistHubBinding(
	ctx context.Context, hub config.FleetHub,
) error {
	return s.mutatePersistedFleet(ctx, func(fleet *config.Fleet) {
		fleet.Hub = &hub
	})
}

func (s *Handlers) ResetPreparedSpokeBinding(ctx context.Context) error {
	return s.mutatePersistedFleet(ctx, func(fleet *config.Fleet) {
		fleet.Role = config.FleetRoleHub
		fleet.Hub = nil
	})
}

func (s *Handlers) RemoveFleetMember(ctx context.Context, nodeID string) error {
	return s.mutatePersistedFleet(ctx, func(fleet *config.Fleet) {
		fleet.Members = slices.DeleteFunc(
			fleet.Members,
			func(member config.FleetMember) bool { return member.NodeID == nodeID },
		)
	})
}

func (s *Handlers) mutatePersistedFleet(
	ctx context.Context, mutate func(*config.Fleet),
) error {
	return s.mutatePersistedFleetChecked(ctx, func(fleet *config.Fleet) error {
		mutate(fleet)
		return nil
	})
}

func (s *Handlers) mutatePersistedFleetChecked(
	ctx context.Context, mutate func(*config.Fleet) error,
) error {
	return s.mutatePersistedFleetCandidateChecked(ctx, false, mutate)
}

func (s *Handlers) MutatePersistedEnrollmentFleetChecked(
	ctx context.Context, mutate func(*config.Fleet) error,
) error {
	return s.mutatePersistedFleetCandidateChecked(ctx, true, mutate)
}

func (s *Handlers) mutatePersistedFleetCandidateChecked(
	ctx context.Context,
	keepEnrollmentHub bool,
	mutate func(*config.Fleet) error,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if (*s.CfgPath) == "" || (*s.Cfg) == nil {
		return errors.New("fleet settings persistence is unavailable")
	}
	s.ConfigReloadMu.Lock()
	defer s.ConfigReloadMu.Unlock()
	s.CfgMu.Lock()
	defer s.CfgMu.Unlock()
	candidate := configreload.CloneReloadedConfig((*s.Cfg))
	active := s.ActiveFleetConfigSnapshotLocked().Fleet
	candidate.Fleet.Role = active.Role
	candidate.Fleet.BaseURL = active.BaseURL
	if !keepEnrollmentHub {
		candidate.Fleet.Hub = active.Hub
	}
	if err := mutate(&candidate.Fleet); err != nil {
		return err
	}
	if err := candidate.Validate(); err != nil {
		return err
	}
	if err := candidate.Save((*s.CfgPath)); err != nil {
		return err
	}
	(*s.Cfg).Fleet = candidate.Fleet
	s.ApplyFleetConfigLocked()
	return nil
}

type GetSettingsOutput = httpapi.BodyOutput[spokeapi.SettingsResponse]

type UpdateSettingsInput struct {
	Body spokeapi.UpdateSettingsRequest
}

type CreateRepoPresetInput struct {
	Body config.RepoPreset
}

type UpdateRepoPresetInput struct {
	Name string `path:"name"`
	Body struct {
		Repos []config.RepoPresetRepository `json:"repos" nullable:"false"`
	}
}

type DeleteRepoPresetInput struct {
	Name string `path:"name"`
}

type AddRepoInput struct {
	Body struct {
		Provider     string `json:"provider"`
		Host         string `json:"host,omitempty"`
		PlatformHost string `json:"platform_host,omitempty"`
		Owner        string `json:"owner"`
		Name         string `json:"name"`
	}
}

type RepoConfigInput struct {
	Provider     string `path:"provider"`
	PlatformHost string
	Owner        string `path:"owner"`
	Name         string `path:"name"`
}

type RepoConfigHostInput struct {
	Provider     string `path:"provider"`
	PlatformHost string `path:"platform_host"`
	Owner        string `path:"owner"`
	Name         string `path:"name"`
}

type RepoWorktreeBaseRequest struct {
	WorktreeBasePath string `json:"worktree_base_path"`
}

type RepoWorktreeBaseInput struct {
	Provider     string `path:"provider"`
	PlatformHost string
	Owner        string `path:"owner"`
	Name         string `path:"name"`
	Body         RepoWorktreeBaseRequest
}

type RepoWorktreeBaseHostInput struct {
	Provider     string `path:"provider"`
	PlatformHost string `path:"platform_host"`
	Owner        string `path:"owner"`
	Name         string `path:"name"`
	Body         RepoWorktreeBaseRequest
}

type repoUIVisibilityRequest struct {
	Hidden bool `json:"hidden"`
}

type RepoUIVisibilityInput struct {
	Provider     string `path:"provider"`
	PlatformHost string
	Owner        string `path:"owner"`
	Name         string `path:"name"`
	Body         repoUIVisibilityRequest
}

type RepoUIVisibilityHostInput struct {
	Provider     string `path:"provider"`
	PlatformHost string `path:"platform_host"`
	Owner        string `path:"owner"`
	Name         string `path:"name"`
	Body         repoUIVisibilityRequest
}

type SettingsOutput = httpapi.BodyOutput[spokeapi.SettingsResponse]

type SetActiveWorktreeInput struct {
	Body struct {
		// Key is the focused worktree's scoped key; empty clears
		// the focus.
		Key string `json:"key"`
	}
}

type getFleetSettingsOutput = httpapi.BodyOutput[spokeapi.FleetSettingsResponse]

type updateFleetSettingsInput struct {
	Body struct {
		Enabled     bool                 `json:"enabled"`
		PeerTimeout string               `json:"peer_timeout,omitempty"`
		Sessions    config.FleetSessions `json:"sessions"`
	}
}

func (s *Handlers) BuildFleetSettingsResponseLocked() spokeapi.FleetSettingsResponse {
	fleet := (*s.Cfg).Fleet
	return spokeapi.FleetSettingsResponse{
		Enabled:         fleet.Enabled,
		Role:            fleet.RoleOrDefault(),
		Hub:             cloneFleetHub(fleet.Hub),
		Members:         append([]config.FleetMember{}, fleet.Members...),
		Enrollments:     append([]federation.Enrollment{}, (*s.FleetAPI).Enrollments()...),
		PeerTimeout:     fleet.PeerTimeout,
		Sessions:        fleet.Sessions,
		RestartRequired: s.fleetSettingsRestartRequiredLocked(fleet),
	}
}

func (s *Handlers) fleetSettingsRestartRequiredLocked(fleet config.Fleet) bool {
	candidate := configreload.CloneReloadedConfig((*s.Cfg))
	candidate.Fleet = fleet
	return s.BootCfgSnapshot.RestartRequiredFor(&candidate)
}

// getFleetSettings returns the complete fleet federation settings shape.
func (s *Handlers) GetFleetSettings(
	_ context.Context, _ *struct{},
) (*getFleetSettingsOutput, error) {
	if (*s.CfgPath) == "" {
		return nil, httpapi.NotFound(
			httpapi.CodeSettingsUnavailable, "settings not available", nil,
		)
	}
	s.CfgMu.Lock()
	out := s.BuildFleetSettingsResponseLocked()
	s.CfgMu.Unlock()
	return &getFleetSettingsOutput{Body: out}, nil
}

// updateFleetSettings changes only operator preferences. Enrollment owns role,
// hub binding, and membership, so an ordinary settings save cannot
// overwrite those lifecycle fields with a stale browser snapshot.
func (s *Handlers) UpdateFleetSettings(
	_ context.Context, input *updateFleetSettingsInput,
) (*getFleetSettingsOutput, error) {
	if (*s.CfgPath) == "" {
		return nil, httpapi.NotFound(
			httpapi.CodeSettingsUnavailable, "settings not available", nil,
		)
	}
	s.ConfigReloadMu.Lock()
	defer s.ConfigReloadMu.Unlock()

	s.CfgMu.Lock()
	candidate := configreload.CloneReloadedConfig((*s.Cfg))
	candidate.Fleet.Enabled = input.Body.Enabled
	candidate.Fleet.PeerTimeout = input.Body.PeerTimeout
	candidate.Fleet.Sessions = input.Body.Sessions
	if err := candidate.Validate(); err != nil {
		s.CfgMu.Unlock()
		return nil, httpapi.BadRequest(httpapi.CodeBadRequest, err.Error(), nil)
	}
	if err := candidate.Save((*s.CfgPath)); err != nil {
		s.CfgMu.Unlock()
		return nil, httpapi.Internal("save config: " + err.Error())
	}
	(*s.Cfg).Fleet = candidate.Fleet
	s.ApplyFleetConfigLocked()
	out := s.BuildFleetSettingsResponseLocked()
	s.CfgMu.Unlock()
	return &getFleetSettingsOutput{Body: out}, nil
}

func cloneFleetHub(in *config.FleetHub) *config.FleetHub {
	if in == nil {
		return nil
	}
	clone := *in
	return &clone
}
