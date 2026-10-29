package server

import (
	"context"
	"net/http"
	"os"
	"strings"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
	"go.kenn.io/kit/daemon"

	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/daemonruntime"
	"go.kenn.io/forge/internal/server/authapi"
	"go.kenn.io/forge/internal/server/configreload"
	"go.kenn.io/forge/internal/server/httpapi"
	"go.kenn.io/forge/internal/server/otelmiddleware"
)

func (s *Server) registerDaemonPing(mux *http.ServeMux) {
	api := humago.New(mux, configreload.DaemonPingAPIConfig())
	api.UseMiddleware(otelmiddleware.OtelSpanMiddleware)
	huma.Get(api, "/api/ping", s.daemonPing,
		httpapi.DocumentOperation("get-daemon-ping", "Get daemon readiness", "System"))
}

// daemonPingInput carries the raw request so ping can advertise the MCP
// endpoint on the origin the caller used.
type daemonPingInput struct {
	request *http.Request
}

func (i *daemonPingInput) Resolve(ctx huma.Context) []error {
	i.request, _ = humago.Unwrap(ctx)
	return nil
}

func (s *Server) daemonPing(
	_ context.Context, input *daemonPingInput,
) (*configreload.DaemonPingOutput, error) {
	return &configreload.DaemonPingOutput{Body: configreload.DaemonPingResponse{
		PingInfo: daemon.PingInfo{
			OK: true, Service: daemonruntime.Service,
			Version: s.buildInfo.Version, PID: os.Getpid(),
		},
		MCPURL: s.advertisedMCPURL(input.request),
	}}, nil
}

// advertisedMCPURL returns the loopback companion URL to local callers. A
// caller that reached Forge through another authority, such as Tailscale
// Serve, gets /mcp on that same origin when the main listener serves it there.
func (s *Server) advertisedMCPURL(r *http.Request) string {
	if r == nil || s.tailnetMCP.Load() == nil {
		return s.options.MCPURL
	}
	host, err := config.ParseHostKey(r.Host)
	if err != nil || config.IsLoopbackHostname(host.Host) {
		return s.options.MCPURL
	}
	scheme := "http"
	if s.authapi.RequestArrivedOverHTTPS(r) || s.arrivedThroughTailscaleServeTLS(r) {
		scheme = "https"
	}
	return scheme + "://" + r.Host + strings.TrimSuffix(s.basePath, "/") + "/mcp"
}

// arrivedThroughTailscaleServeTLS reports whether a loopback proxy request
// carries the scheme Tailscale Serve records after terminating TLS.
func (s *Server) arrivedThroughTailscaleServeTLS(r *http.Request) bool {
	if !s.daemonRequests.TailscaleServeEnabled() || !authapi.IsLoopbackRemoteAddr(r.RemoteAddr) {
		return false
	}
	values := r.Header.Values("X-Forwarded-Proto")
	return len(values) == 1 && strings.EqualFold(strings.TrimSpace(values[0]), "https")
}
