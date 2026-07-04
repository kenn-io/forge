package mcpserver

import (
	"context"
	"fmt"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.kenn.io/middleman/internal/config"
)

type Options struct {
	ConfigPath    string
	Transport     string
	Addr          string
	HTTPTokenEnv  string
	DaemonTimeout time.Duration
	Version       string
}

type Server struct {
	opts   Options
	daemon *daemonClient
	mcp    *mcp.Server
	diffs  *diffFileStore
}

type diffFileStore struct{}

func (d *diffFileStore) Close() error {
	return nil
}

func New(opts Options) (*Server, error) {
	if opts.ConfigPath == "" {
		opts.ConfigPath = config.DefaultConfigPath()
	}
	if opts.DaemonTimeout <= 0 {
		opts.DaemonTimeout = 10 * time.Second
	}
	s := &Server{
		opts:   opts,
		daemon: newDaemonClient(opts.ConfigPath, opts.DaemonTimeout),
	}
	s.mcp = mcp.NewServer(&mcp.Implementation{Name: "middleman", Version: opts.Version}, nil)
	s.registerTools()
	return s, nil
}

func (s *Server) registerTools() {
	s.registerReadTools()
}

func (s *Server) RunStdio(ctx context.Context) error {
	return s.mcp.Run(ctx, &mcp.StdioTransport{})
}

func (s *Server) RunHTTP(context.Context) error {
	return fmt.Errorf("http transport not yet available")
}

func (s *Server) Close() error {
	if s.diffs != nil {
		return s.diffs.Close()
	}
	return nil
}
