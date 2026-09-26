package repoapi

import (
	sync "sync"
	time "time"

	config "go.kenn.io/forge/internal/config"
	dbpkg "go.kenn.io/forge/internal/db"
	ghclient "go.kenn.io/forge/internal/github"
	httpapi "go.kenn.io/forge/internal/server/httpapi"
)

// Handlers holds the server state and cross-package calls this package's
// handlers use. The server package builds it in wireHandlers and shares
// mutable server fields by pointer.
type Handlers struct {
	Cfg            **config.Config
	CfgMu          *sync.Mutex
	CfgPath        *string
	Db             *dbpkg.DB
	Now            *func() time.Time
	RepoResolver   *httpapi.RepositoryResolver
	Syncer         **ghclient.Syncer
	ToolingRun     *ToolingRunner
	ToolingStatus  *ToolingStatusCache
	RepoOperations func(repo dbpkg.Repo) httpapi.RepoOperations
}
