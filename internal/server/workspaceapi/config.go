package workspaceapi

import (
	"slices"

	"go.kenn.io/middleman/internal/config"
	"go.kenn.io/middleman/internal/projects"
)

// ConfigSnapshot is the immutable, committed configuration state consumed by
// the Workspace and Projects API. The root server derives and publishes it
// only after its config transaction succeeds.
type ConfigSnapshot struct {
	KnownPlatformHosts []projects.KnownPlatformHost
	Agents             []config.Agent
	TmuxCommand        []string
	AutoAssignOnCreate bool
}

func cloneConfigSnapshot(snapshot ConfigSnapshot) ConfigSnapshot {
	out := ConfigSnapshot{
		KnownPlatformHosts: slices.Clone(snapshot.KnownPlatformHosts),
		Agents:             make([]config.Agent, len(snapshot.Agents)),
		TmuxCommand:        slices.Clone(snapshot.TmuxCommand),
		AutoAssignOnCreate: snapshot.AutoAssignOnCreate,
	}
	for i := range snapshot.Agents {
		out.Agents[i] = snapshot.Agents[i]
		out.Agents[i].Command = slices.Clone(snapshot.Agents[i].Command)
		if snapshot.Agents[i].Enabled != nil {
			enabled := *snapshot.Agents[i].Enabled
			out.Agents[i].Enabled = &enabled
		}
	}
	return out
}

// ApplyConfig atomically publishes a committed configuration snapshot.
func (h *Handler) ApplyConfig(snapshot ConfigSnapshot) {
	if h == nil {
		return
	}
	h.configMu.Lock()
	h.config = cloneConfigSnapshot(snapshot)
	h.configMu.Unlock()
}

func (h *Handler) configSnapshot() ConfigSnapshot {
	if h == nil {
		return ConfigSnapshot{}
	}
	h.configMu.RLock()
	defer h.configMu.RUnlock()
	return cloneConfigSnapshot(h.config)
}

func (h *Handler) tmuxCommand() []string {
	return h.configSnapshot().TmuxCommand
}
