package settingsapi

import (
	sync "sync"

	config "go.kenn.io/forge/internal/config"
	dbpkg "go.kenn.io/forge/internal/db"
	ghclient "go.kenn.io/forge/internal/github"
	ptyowner "go.kenn.io/forge/internal/ptyowner"
	configreload "go.kenn.io/forge/internal/server/configreload"
	fleetapi "go.kenn.io/forge/internal/server/fleetapi"
	spokeapi "go.kenn.io/forge/internal/server/spokeapi"
	workspace "go.kenn.io/forge/internal/workspace"
	localruntime "go.kenn.io/forge/internal/workspace/localruntime"
)

// Handlers holds the server state and cross-package calls this package's
// handlers use. The server package builds it in wireHandlers and shares
// mutable server fields by pointer.
type Handlers struct {
	BootCfgSnapshot                 *configreload.StartupConfigSnapshot
	Cfg                             **config.Config
	CfgMu                           *sync.Mutex
	CfgPath                         *string
	ConfigReloadMu                  *sync.Mutex
	Db                              *dbpkg.DB
	FleetAPI                        **fleetapi.Handler
	ProviderSource                  **spokeapi.HubProviderSource
	PtyOwnerClient                  **ptyowner.Client
	RepoVisibilityMu                *sync.Mutex
	Runtime                         **localruntime.Manager
	Syncer                          **ghclient.Syncer
	Workspaces                      **workspace.Manager
	ActiveFleetConfigSnapshotLocked func() fleetapi.ConfigSnapshot
	ApplyFleetConfigLocked          func()
	ApplyWorkspaceConfigLocked      func()
}
