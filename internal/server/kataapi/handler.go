// Package kataapi owns the Kata daemon HTTP boundary.
package kataapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"

	"github.com/danielgtaylor/huma/v2"
	"go.kenn.io/middleman/internal/config"
	"go.kenn.io/middleman/internal/db"
	"go.kenn.io/middleman/internal/kata"
	"go.kenn.io/middleman/internal/platform"
	"go.kenn.io/middleman/internal/server/httpapi"
	"go.kenn.io/middleman/internal/server/workspaceapi"
	"go.kenn.io/middleman/internal/workspace"
)

// Deps contains the committed state and shared services consumed by Kata.
// Defaults preserve the normal local Kata discovery behavior while allowing
// tests and alternate composition roots to supply owned transports/catalogs.
type Deps struct {
	DB                     *db.DB
	Resolver               *httpapi.RepositoryResolver
	Config                 ConfigSnapshot
	Workspaces             *workspace.Manager
	WorkspaceAPI           *workspaceapi.Handler
	LoadCatalog            func() (kata.Catalog, error)
	ResolveDaemon          func(kata.Daemon) (kata.Daemon, error)
	DiscoverLocalDaemonURL func() string
	NewHTTPTransport       func() http.RoundTripper
	SamePlatformHost       func(string, string) bool
	ConfigRepoPath         func(config.Repo) string
	InvalidateDaemon       func(string)
}

// Handler owns Kata routes, daemon caches, and their lifecycle.
type Handler struct {
	db           *db.DB
	resolver     *httpapi.RepositoryResolver
	workspaces   *workspace.Manager
	workspaceAPI *workspaceapi.Handler

	configMu sync.RWMutex
	config   ConfigSnapshot

	loadCatalog            func() (kata.Catalog, error)
	resolveDaemon          func(kata.Daemon) (kata.Daemon, error)
	discoverLocalDaemonURL func() string
	newHTTPTransport       func() http.RoundTripper
	samePlatformHost       func(string, string) bool
	configRepoPath         func(config.Repo) string
	invalidateDaemon       func(string)

	kataHealthMu       sync.Mutex
	kataHealthCache    map[string]kataDaemonHealthCacheEntry
	kataHealthInFlight map[string]*kataDaemonInflightProbe
	kataProxyMu        sync.Mutex
	kataProxyCache     map[kataProxyCacheKey]kataProxyCacheEntry

	lifecycleMu     sync.Mutex
	lifecycleCancel context.CancelFunc
	lifecycleWG     sync.WaitGroup
	lifecycleDone   chan struct{}
	started         bool
	stopping        bool
}

// New constructs the Kata API handler from immutable state and explicit
// shared-domain dependencies.
func New(deps Deps) *Handler {
	loadCatalog := deps.LoadCatalog
	if loadCatalog == nil {
		loadCatalog = kata.LoadCatalog
	}
	resolveDaemon := deps.ResolveDaemon
	if resolveDaemon == nil {
		resolveDaemon = kata.ResolveDaemon
	}
	discoverLocalDaemonURL := deps.DiscoverLocalDaemonURL
	if discoverLocalDaemonURL == nil {
		discoverLocalDaemonURL = kata.DiscoverLocalDaemonURL
	}
	newHTTPTransport := deps.NewHTTPTransport
	if newHTTPTransport == nil {
		newHTTPTransport = newDefaultKataDaemonTransport
	}
	return &Handler{
		db:                     deps.DB,
		resolver:               deps.Resolver,
		config:                 cloneConfigSnapshot(deps.Config),
		workspaces:             deps.Workspaces,
		workspaceAPI:           deps.WorkspaceAPI,
		loadCatalog:            loadCatalog,
		resolveDaemon:          resolveDaemon,
		discoverLocalDaemonURL: discoverLocalDaemonURL,
		newHTTPTransport:       newHTTPTransport,
		samePlatformHost:       deps.SamePlatformHost,
		configRepoPath:         deps.ConfigRepoPath,
		invalidateDaemon:       deps.InvalidateDaemon,
		lifecycleDone:          make(chan struct{}),
	}
}

// Register registers all documented and passthrough Kata routes.
func (h *Handler) Register(api huma.API) {
	huma.Get(api, "/kata/daemons", h.listKataDaemons,
		httpapi.DocumentOperation("list-kata-daemons", "List Kata daemons", "Kata"))
	registerKataWorkspaceAPI(api, h)
	h.registerKataProxyAPI(api)
}

func (h *Handler) repoRefFromParts(
	provider, host, owner, name string,
) httpapi.RepoRefResponse {
	provider = strings.TrimSpace(provider)
	if provider == "" {
		provider = string(platform.KindGitHub)
	}
	response := httpapi.RepoRefResponse{
		Provider: provider, PlatformHost: host,
		RepoPath: owner + "/" + name, Owner: owner, Name: name,
	}
	if h.resolver != nil {
		response.Capabilities = h.resolver.Capabilities(platform.Kind(provider), host)
	}
	return response
}

func writeProblemResponse(w http.ResponseWriter, problem *httpapi.ProblemError) {
	if problem == nil {
		problem = httpapi.NewProblem(
			http.StatusInternalServerError,
			httpapi.CodeInternalError,
			"internal error",
			nil,
		)
	}
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(problem.Status)
	_ = json.NewEncoder(w).Encode(problem)
}
