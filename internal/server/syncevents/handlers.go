package syncevents

import (
	context "context"
	sync "sync"
	time "time"

	dbpkg "go.kenn.io/forge/internal/db"
	ghclient "go.kenn.io/forge/internal/github"
	spokeapi "go.kenn.io/forge/internal/server/spokeapi"
	workspaceapi "go.kenn.io/forge/internal/server/workspaceapi"
)

// Handlers holds the server state and cross-package calls this package's
// handlers use. The server package builds it in wireHandlers and shares
// mutable server fields by pointer.
type Handlers struct {
	BgCtx                 context.Context
	Db                    *dbpkg.DB
	DetailSyncInFlight    *map[string]struct{}
	DetailSyncMu          *sync.Mutex
	DetailSyncPending     *map[string]DetailSyncJob
	FederationStreams     *map[string]map[uint64]context.CancelFunc
	FederationStreamsMu   *sync.Mutex
	FederationStreamsNext *uint64
	Hub                   **EventHub
	Now                   *func() time.Time
	ProviderSource        **spokeapi.HubProviderSource
	Syncer                **ghclient.Syncer
	WorkspaceAPI          **workspaceapi.Handler
	FederationEnabled     func() bool
	RunBackground         func(fn func(ctx context.Context)) bool
}
