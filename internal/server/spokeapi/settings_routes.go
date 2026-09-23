package spokeapi

import (
	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/federation"
)

type FleetSettingsResponse struct {
	Enabled         bool                    `json:"enabled"`
	Role            config.FleetRole        `json:"role"`
	Hub             *config.FleetHub        `json:"hub,omitempty"`
	Members         []config.FleetMember    `json:"members" nullable:"false"`
	Enrollments     []federation.Enrollment `json:"enrollments" nullable:"false"`
	PeerTimeout     string                  `json:"peer_timeout,omitempty"`
	Sessions        config.FleetSessions    `json:"sessions"`
	RestartRequired bool                    `json:"restart_required"`
}
