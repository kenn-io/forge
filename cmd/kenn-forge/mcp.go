package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"time"

	"github.com/spf13/cobra"
	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/mcpserver"
)

type mcpRunner func(
	context.Context,
	mcpserver.Options,
	io.Reader,
	io.Writer,
) error

func newMCPCommand(run mcpRunner, stdin io.Reader, stdout io.Writer) *cobra.Command {
	opts := mcpserver.Options{
		ConfigPath:    config.DefaultConfigPath(),
		Transport:     "stdio",
		Addr:          "127.0.0.1:0",
		DaemonTimeout: 10 * time.Second,
		Version:       version,
	}
	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "Expose cached maintainer workflows to MCP clients",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if opts.DaemonTimeout <= 0 {
				return fmt.Errorf("--daemon-timeout must be positive")
			}
			switch opts.Transport {
			case "stdio":
				if cmd.Flags().Changed("addr") {
					return fmt.Errorf("--addr requires --transport=http")
				}
				if cmd.Flags().Changed("http-token-env") {
					return fmt.Errorf("--http-token-env requires --transport=http")
				}
			case "http":
				_, port, err := net.SplitHostPort(opts.Addr)
				portNumber, portErr := strconv.Atoi(port)
				if !cmd.Flags().Changed("addr") || err != nil || !asciiDigits(port) || portErr != nil ||
					portNumber < 1 || portNumber > 65535 {
					return fmt.Errorf("--addr with an explicit nonzero port is required for --transport=http")
				}
			default:
				return fmt.Errorf("unsupported transport %q: use stdio or http", opts.Transport)
			}
			return run(cmd.Context(), opts, stdin, stdout)
		},
	}
	cmd.Flags().StringVar(&opts.ConfigPath, "config", opts.ConfigPath, "path to config file")
	cmd.Flags().StringVar(&opts.Transport, "transport", opts.Transport, "MCP transport: stdio or http")
	cmd.Flags().StringVar(&opts.Addr, "addr", opts.Addr, "HTTP listen address (http transport only)")
	cmd.Flags().StringVar(&opts.HTTPTokenEnv, "http-token-env", "", "environment variable holding the HTTP bearer token")
	cmd.Flags().DurationVar(&opts.DaemonTimeout, "daemon-timeout", opts.DaemonTimeout, "per-request daemon timeout")
	return cmd
}

func asciiDigits(value string) bool {
	if value == "" {
		return false
	}
	for index := range value {
		if value[index] < '0' || value[index] > '9' {
			return false
		}
	}
	return true
}

func runMCP(
	ctx context.Context,
	opts mcpserver.Options,
	stdin io.Reader,
	stdout io.Writer,
) error {
	srv, err := mcpserver.New(opts)
	if err != nil {
		return err
	}
	var runErr error
	if opts.Transport == "http" {
		runErr = srv.RunHTTP(ctx)
	} else {
		runErr = srv.RunStdio(ctx, stdin, stdout)
	}
	return errors.Join(runErr, srv.Close())
}
