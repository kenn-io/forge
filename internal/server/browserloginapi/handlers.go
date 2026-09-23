package browserloginapi

import (
	browserlogin "go.kenn.io/forge/internal/browserlogin"
)

// Handlers holds the server state and cross-package calls this package's
// handlers use. The server package builds it in wireHandlers and shares
// mutable server fields by pointer.
type Handlers struct {
	BrowserLoginTickets **browserlogin.Store
}
