package main

import (
	"context"
	"net"
	"net/http"
	"time"

	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/server"
)

// Agents run on this host even when the browser-facing listener is bound to a
// LAN address or behind a reverse proxy. Their MCP transport stays on loopback.
func newAgentMCPHTTP(ctx context.Context, token string) (net.Listener, *http.Server, *server.SwitchHandler, error) {
	listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		return nil, nil, nil, err
	}
	bind, err := config.ParseHostKey(listener.Addr().String())
	if err != nil {
		_ = listener.Close()
		return nil, nil, nil, err
	}
	switcher := server.NewSwitchHandler(newMCPStartupHandler())
	httpServer := &http.Server{
		Handler: server.NewMCPHTTPGuard(switcher, server.MCPHTTPGuardOptions{
			Bind: bind, Token: token, RequireAuth: true,
		}),
		ReadHeaderTimeout: 5 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return ctx },
	}
	return listener, httpServer, switcher, nil
}
