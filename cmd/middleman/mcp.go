package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"go.kenn.io/middleman/internal/config"
	"go.kenn.io/middleman/internal/mcpserver"
)

func runMCPCLI(args []string) error {
	fs := flag.NewFlagSet("middleman mcp", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	configPath := fs.String("config", config.DefaultConfigPath(), "path to config file")
	transport := fs.String("transport", "stdio", "MCP transport: stdio or http")
	addr := fs.String("addr", "127.0.0.1:0", "HTTP listen address (http transport only)")
	tokenEnv := fs.String("http-token-env", "", "environment variable holding the HTTP bearer token")
	daemonTimeout := fs.Duration("daemon-timeout", 10*time.Second, "per-request daemon timeout")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *transport != "stdio" && *transport != "http" {
		return fmt.Errorf("unsupported transport %q: use stdio or http", *transport)
	}

	srv, err := mcpserver.New(mcpserver.Options{
		ConfigPath:    *configPath,
		Transport:     *transport,
		Addr:          *addr,
		HTTPTokenEnv:  *tokenEnv,
		DaemonTimeout: *daemonTimeout,
		Version:       version,
	})
	if err != nil {
		return err
	}
	defer func() { _ = srv.Close() }()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if *transport == "http" {
		return srv.RunHTTP(ctx)
	}
	return srv.RunStdio(ctx)
}
