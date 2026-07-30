package docsapi

import (
	"log/slog"
	"strings"

	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/kata"
)

func WarnDaemonBindings(folders []config.DocFolder) {
	if !HasDaemonBinding(folders) {
		return
	}
	catalog, err := kata.LoadCatalog()
	if err != nil {
		slog.Warn("validate doc folder Kata daemon bindings failed", "err", err)
		return
	}
	ids := make(map[string]struct{}, len(catalog.Daemons))
	for _, d := range catalog.Daemons {
		ids[d.ID] = struct{}{}
	}
	for _, folder := range folders {
		daemon := strings.TrimSpace(folder.Daemon)
		if daemon == "" {
			continue
		}
		if _, ok := ids[daemon]; ok {
			continue
		}
		slog.Warn(
			"doc folder references missing Kata daemon",
			"folder", folder.ID,
			"daemon", daemon,
		)
	}
}

func HasDaemonBinding(folders []config.DocFolder) bool {
	for _, folder := range folders {
		if strings.TrimSpace(folder.Daemon) != "" {
			return true
		}
	}
	return false
}
