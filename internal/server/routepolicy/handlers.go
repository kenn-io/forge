package routepolicy

import (
	dbpkg "go.kenn.io/forge/internal/db"
)

// Handlers holds the server state and cross-package calls this package's
// handlers use. The server package builds it in wireHandlers and shares
// mutable server fields by pointer.
type Handlers struct {
	Db           *dbpkg.DB
	BuildVersion func() string
	BuildCommit  func() string
}
