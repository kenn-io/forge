package itemapi

import (
	context "context"
	sync "sync"

	dbpkg "go.kenn.io/forge/internal/db"
	ghclient "go.kenn.io/forge/internal/github"
	httpapi "go.kenn.io/forge/internal/server/httpapi"
	pullapi "go.kenn.io/forge/internal/server/pullapi"
)

// Handlers holds the server state and cross-package calls this package's
// handlers use. The server package builds it in wireHandlers and shares
// mutable server fields by pointer.
type Handlers struct {
	Db                      *dbpkg.DB
	LabelCatalogRefreshIDs  map[int64]struct{}
	LabelCatalogRefreshMu   *sync.Mutex
	PullAPI                 **pullapi.Handler
	RepoResolver            *httpapi.RepositoryResolver
	Syncer                  **ghclient.Syncer
	IsConfiguredRepoTracked func(repo dbpkg.Repo) bool
	RunBackground           func(fn func(ctx context.Context)) bool
}
