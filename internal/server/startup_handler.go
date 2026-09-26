package server

import (
	"encoding/json/v2"
	"io/fs"
	"net"
	"net/http"

	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/server/authapi"
	"go.kenn.io/forge/internal/server/compression"
	"go.kenn.io/forge/internal/server/hostapi"
	"go.kenn.io/forge/internal/server/routepolicy"
	"go.kenn.io/forge/internal/server/streamapi"
)

// NewStartupHandler returns a minimal handler for the window between listener
// bind and full backend readiness. It serves daemon identity proof, the real
// SPA shell, and frontend assets immediately, while other API and websocket
// routes report service unavailable until the full server is swapped in.
func NewStartupHandler(
	frontend fs.FS,
	cfg *config.Config,
	options ServerOptions,
	ln net.Listener,
	buildInfo BuildInfo,
) http.Handler {
	basePath := "/"
	if cfg != nil && cfg.BasePath != "" {
		basePath = cfg.BasePath
	}
	hostOpts := streamapi.ResolveHostCheckOptions(
		cfg,
		options.HostCheck,
		options.HostCheckAllowLoopbackAnyPort,
	)
	if bind, ok := authapi.ListenerHostKey(ln); ok {
		hostOpts.Bind = bind
	}

	var spa http.Handler
	if frontend != nil {
		spa = compression.NewSPAAssetHandler(frontend, basePath, func() string {
			safeBase, _ := json.Marshal(basePath)
			return `window.__BASE_PATH__=` + streamapi.ScriptSafe(string(safeBase)) + `;`
		})
	}

	return &hostapi.StartupHandler{
		HostOpts:       hostOpts,
		AllowedHosts:   streamapi.AllowedHostsForListener(ln),
		DaemonRequests: authapi.NewDaemonRequestPolicy(options.DaemonAccess),
		BasePath:       basePath,
		Spa:            spa,
		Health:         routepolicy.HealthyResponse(buildInfo.Version, buildInfo.Commit),
	}
}
