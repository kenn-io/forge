package server

import (
	"net"
	"net/http"

	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/server/authapi"
	"go.kenn.io/forge/internal/server/hostapi"
	"go.kenn.io/forge/internal/server/routepolicy"
	"go.kenn.io/forge/internal/server/streamapi"
)

// NewStartupHandler returns a minimal handler for the window between listener
// bind and full backend readiness. It serves daemon identity proof and
// liveness, while every other route reports service unavailable until the
// full server is swapped in.
func NewStartupHandler(
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

	return &hostapi.StartupHandler{
		HostOpts:       hostOpts,
		AllowedHosts:   streamapi.AllowedHostsForListener(ln),
		DaemonRequests: authapi.NewDaemonRequestPolicy(options.DaemonAccess),
		BasePath:       basePath,
		Health:         routepolicy.HealthyResponse(buildInfo.Version, buildInfo.Commit),
	}
}
