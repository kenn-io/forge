package authapi

import (
	sync "sync"
	atomic "sync/atomic"

	config "go.kenn.io/forge/internal/config"
	dbpkg "go.kenn.io/forge/internal/db"
	ghclient "go.kenn.io/forge/internal/github"
)

// Handlers holds the server state and cross-package calls this package's
// handlers use. The server package builds it in wireHandlers and shares
// mutable server fields by pointer.
type Handlers struct {
	BasePath              *string
	Cfg                   **config.Config
	DaemonRequests        *DaemonRequestPolicy
	Db                    *dbpkg.DB
	HostOpts              *atomic.Pointer[HostCheckOptions]
	Syncer                **ghclient.Syncer
	ViewerLoginCache      *map[string]ViewerLoginCacheEntry
	ViewerLoginInFlight   *map[string]*ViewerLoginCall
	ViewerLoginMu         *sync.Mutex
	FilterConfiguredRepos func(repos []dbpkg.Repo) []dbpkg.Repo
}
