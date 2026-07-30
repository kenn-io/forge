package server

import (
	"context"
	"net/http"
	"os"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
	"go.kenn.io/kit/daemon"
	"go.kenn.io/middleman/internal/daemonruntime"
	"go.kenn.io/middleman/internal/server/httpapi"
)

type daemonPingOutput = httpapi.BodyOutput[daemon.PingInfo]

func daemonPingAPIConfig() huma.Config {
	config := huma.DefaultConfig("middleman daemon", "0.1.0")
	config.OpenAPIPath = ""
	config.DocsPath = ""
	config.SchemasPath = ""
	config.Servers = nil
	return config
}

func (s *Server) registerDaemonPing(mux *http.ServeMux) {
	if s.options.DaemonProofHandler != nil {
		mux.Handle(daemonruntime.ProofPingPath, s.options.DaemonProofHandler)
	}
	api := humago.New(mux, daemonPingAPIConfig())
	api.UseMiddleware(otelSpanMiddleware)
	huma.Get(api, "/api/ping", s.daemonPing,
		httpapi.DocumentOperation("get-daemon-ping", "Get daemon readiness", "System"))
}

func (s *Server) daemonPing(
	_ context.Context, _ *struct{},
) (*daemonPingOutput, error) {
	return &daemonPingOutput{Body: daemon.PingInfo{
		OK:      true,
		Service: daemonruntime.Service,
		Version: s.buildInfo.Version,
		PID:     os.Getpid(),
	}}, nil
}
