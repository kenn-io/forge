package mcpserver

import (
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const defaultDiffCacheBytes int64 = 128 << 20

type Options struct {
	Backend        Backend
	Version        string
	DiffCacheBytes int64
}

type Server struct {
	backend                  Backend
	mcp                      *mcp.Server
	agentHandoffPollInterval time.Duration
	diffCacheBytes           int64
	diffMu                   sync.Mutex
	diffs                    *diffFileStore
}

func New(opts Options) (*Server, error) {
	if opts.Backend == nil {
		return nil, fmt.Errorf("MCP backend is required")
	}
	diffCacheBytes := opts.DiffCacheBytes
	if diffCacheBytes <= 0 {
		diffCacheBytes = defaultDiffCacheBytes
	}
	s := &Server{
		backend:                  opts.Backend,
		agentHandoffPollInterval: defaultAgentHandoffPollInterval,
		diffCacheBytes:           diffCacheBytes,
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
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/mcp" {
			http.NotFound(w, r)
			return
		}
		stream.ServeHTTP(w, r)
	})
}

func (s *Server) Close() error {
	s.diffMu.Lock()
	defer s.diffMu.Unlock()
	if s.diffs != nil {
		return s.diffs.Close()
	}
	return nil
}
