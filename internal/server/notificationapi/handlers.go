package notificationapi

import (
	context "context"
	time "time"

	config "go.kenn.io/forge/internal/config"
	dbpkg "go.kenn.io/forge/internal/db"
	ghclient "go.kenn.io/forge/internal/github"
)

// Handlers holds the server state and cross-package calls this package's
// handlers use. The server package builds it in wireHandlers and shares
// mutable server fields by pointer.
type Handlers struct {
	Cfg           **config.Config
	Db            *dbpkg.DB
	Now           *func() time.Time
	Syncer        **ghclient.Syncer
	RunBackground func(fn func(ctx context.Context)) bool
}
