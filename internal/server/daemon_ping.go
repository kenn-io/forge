package server

import (
	"context"
	"net/http"
	"os"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
	"go.kenn.io/forge/internal/daemonruntime"
	"go.kenn.io/forge/internal/server/httpapi"
	"go.kenn.io/kit/daemon"
)

type daemonPingResponse struct {
	daemon.PingInfo
	MCPURL string `json:"mcp_url,omitempty"`
}

type daemonPingOutput = httpapi.BodyOutput[daemonPingResponse]

func daemonPingAPIConfig() huma.Config {
	config := huma.DefaultConfig("kenn-forge daemon", "0.1.0")
	config.OpenAPIPath = ""
	config.DocsPath = ""
	config.SchemasPath = ""
	config.Servers = nil
	return config
}

func (s *Server) registerDaemonPing(mux *http.ServeMux) {
	api := humago.New(mux, daemonPingAPIConfig())
	api.UseMiddleware(otelSpanMiddleware)
	huma.Get(api, "/api/ping", s.daemonPing,
		httpapi.DocumentOperation("get-daemon-ping", "Get daemon readiness", "System"))
}

func (s *Server) daemonPing(
	_ context.Context, _ *struct{},
) (*daemonPingOutput, error) {
	return &daemonPingOutput{Body: daemonPingResponse{
		PingInfo: daemon.PingInfo{
			OK: true, Service: daemonruntime.Service,
			Version: s.buildInfo.Version, PID: os.Getpid(),
		},
		MCPURL: s.options.MCPURL,
	}}, nil
}
