package mcpserver

import (
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type Options struct {
	Backend Backend
	Version string
}

type Server struct {
	backend                  Backend
	mcp                      *mcp.Server
	agentHandoffPollInterval time.Duration
	diffMu                   sync.Mutex
	diffs                    *diffFileStore
}

func New(opts Options) (*Server, error) {
	if opts.Backend == nil {
		return nil, fmt.Errorf("MCP backend is required")
	}
	s := &Server{
		backend:                  opts.Backend,
		agentHandoffPollInterval: defaultAgentHandoffPollInterval,
	}
	s.mcp = mcp.NewServer(
		&mcp.Implementation{Name: "kenn-forge", Version: opts.Version},
		&mcp.ServerOptions{Capabilities: &mcp.ServerCapabilities{
			Prompts:   &mcp.PromptCapabilities{},
			Resources: &mcp.ResourceCapabilities{},
			Tools:     &mcp.ToolCapabilities{},
		}},
	)
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
	s.registerAgentTools()
	s.registerGuidance()
}

// HTTPHandler serves the single stateless Streamable HTTP MCP endpoint.
func (s *Server) HTTPHandler() http.Handler {
	stream := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return s.mcp },
		&mcp.StreamableHTTPOptions{Stateless: true},
	)
	mux := http.NewServeMux()
	mux.Handle("/mcp", stream)
	return mux
}

func (s *Server) Close() error {
	s.diffMu.Lock()
	defer s.diffMu.Unlock()
	if s.diffs != nil {
		return s.diffs.Close()
	}
	return nil
}
