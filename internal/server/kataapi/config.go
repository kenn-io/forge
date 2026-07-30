package kataapi

import (
	"slices"

	"go.kenn.io/forge/internal/config"
)

// ConfigSnapshot is the immutable, committed configuration consumed by Kata.
type ConfigSnapshot struct {
	Repos        []config.Repo
	KataProjects []config.KataProjectRepoMapping
}

func cloneConfigSnapshot(snapshot ConfigSnapshot) ConfigSnapshot {
	return ConfigSnapshot{
		Repos:        slices.Clone(snapshot.Repos),
		KataProjects: slices.Clone(snapshot.KataProjects),
	}
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
