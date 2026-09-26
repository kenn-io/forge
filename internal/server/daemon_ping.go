package server

import (
	"context"
	"net/http"
	"os"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
	"go.kenn.io/forge/internal/daemonruntime"
	"go.kenn.io/forge/internal/server/configreload"
	"go.kenn.io/forge/internal/server/httpapi"
	"go.kenn.io/forge/internal/server/otelmiddleware"
	"go.kenn.io/kit/daemon"
)

func (s *Server) registerDaemonPing(mux *http.ServeMux) {
	api := humago.New(mux, configreload.DaemonPingAPIConfig())
	api.UseMiddleware(otelmiddleware.OtelSpanMiddleware)
	huma.Get(api, "/api/ping", s.daemonPing,
		httpapi.DocumentOperation("get-daemon-ping", "Get daemon readiness", "System"))
}

func (s *Server) daemonPing(
	_ context.Context, _ *struct{},
) (*configreload.DaemonPingOutput, error) {
	return &configreload.DaemonPingOutput{Body: configreload.DaemonPingResponse{
		PingInfo: daemon.PingInfo{
			OK: true, Service: daemonruntime.Service,
			Version: s.buildInfo.Version, PID: os.Getpid(),
		},
		MCPURL: s.options.MCPURL,
	}}, nil
}
