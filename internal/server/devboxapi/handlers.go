package devboxapi

import (
	sync "sync"

	config "go.kenn.io/forge/internal/config"
	tokenauth "go.kenn.io/forge/internal/tokenauth"
)

// Handlers holds the server state and cross-package calls this package's
// handlers use. The server package builds it in wireHandlers and shares
// mutable server fields by pointer.
type Handlers struct {
	Cfg          **config.Config
	CfgMu        *sync.Mutex
	TokenSources **tokenauth.SourceSet
}
