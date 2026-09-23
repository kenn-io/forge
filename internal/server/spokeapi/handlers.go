package spokeapi

import (
	time "time"

	dbpkg "go.kenn.io/forge/internal/db"
	gitclone "go.kenn.io/forge/internal/gitclone"
)

// Handlers holds the server state and cross-package calls this package's
// handlers use. The server package builds it in wireHandlers and shares
// mutable server fields by pointer.
type Handlers struct {
	Clones *gitclone.Manager
	Db     *dbpkg.DB
	Now    *func() time.Time
}
