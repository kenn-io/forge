package archiveapi

import (
	archive "go.kenn.io/forge/internal/archive"
	ghclient "go.kenn.io/forge/internal/github"
)

// Handlers holds the server state and cross-package calls this package's
// handlers use. The server package builds it in wireHandlers and shares
// mutable server fields by pointer.
type Handlers struct {
	Archive     archive.Controller
	Syncer      **ghclient.Syncer
	RequireSync func() error
}
