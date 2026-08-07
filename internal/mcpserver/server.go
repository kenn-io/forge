package mcpserver

import (
	"context"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.kenn.io/forge/internal/config"
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
	opts     Options
	daemon   *daemonClient
	mcp      *mcp.Server
	diffMu   sync.Mutex
	diffs    *diffFileStore
	httpMu   sync.RWMutex
	httpAddr string
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
	s.mcp = mcp.NewServer(&mcp.Implementation{Name: "kenn-forge", Version: opts.Version}, nil)
	s.registerTools()
	return s, nil
}

func (s *Server) registerTools() {
	s.registerReadTools()
	s.registerCandidateTools()
	s.registerItemTools()
	s.registerDiffTools()
	s.registerStackTools()
	s.registerWorkflowTools()
	s.registerGuidance()
}

func (s *Server) RunStdio(ctx context.Context) error {
	return s.mcp.Run(ctx, &mcp.StdioTransport{})
}

func (s *Server) Close() error {
	s.diffMu.Lock()
	defer s.diffMu.Unlock()
	if s.diffs != nil {
		return s.diffs.Close()
	}
	return nil
}
