package configreload

import (
	context "context"
	sync "sync"

	config "go.kenn.io/forge/internal/config"
	configwatch "go.kenn.io/forge/internal/configwatch"
	ghclient "go.kenn.io/forge/internal/github"
	ptyowner "go.kenn.io/forge/internal/ptyowner"
	docsapi "go.kenn.io/forge/internal/server/docsapi"
	syncevents "go.kenn.io/forge/internal/server/syncevents"
	tokenauth "go.kenn.io/forge/internal/tokenauth"
	workspace "go.kenn.io/forge/internal/workspace"
	localruntime "go.kenn.io/forge/internal/workspace/localruntime"
)

// Handlers holds the server state and cross-package calls this package's
// handlers use. The server package builds it in wireHandlers and shares
// mutable server fields by pointer.
type Handlers struct {
	BgCtx                                 context.Context
	BootCfgSnapshot                       *StartupConfigSnapshot
	Cfg                                   **config.Config
	CfgMu                                 *sync.Mutex
	CfgPath                               *string
	ConfigReloadMu                        *sync.Mutex
	ConfigWatcher                         **configwatch.Watcher
	DocsAPI                               **docsapi.Handler
	Hub                                   **syncevents.EventHub
	PtyOwnerClient                        **ptyowner.Client
	Runtime                               **localruntime.Manager
	RuntimeStripEnvVars                   *[]string
	Syncer                                **ghclient.Syncer
	TokenSources                          **tokenauth.SourceSet
	Workspaces                            **workspace.Manager
	ApplyFleetConfigLocked                func()
	ApplyIssueConfigLocked                func()
	ApplyKataConfigLocked                 func()
	ApplyPullConfigLocked                 func()
	ApplyTmuxGraphics                     func(ctx context.Context)
	ApplyTmuxMouse                        func(ctx context.Context)
	ApplyWorkspaceConfigLocked            func()
	ReconcileGitHubNativeStackProjection  func(previous bool, enabled bool)
	ReconcileOrphanedRepoVisibility       func(ctx context.Context) error
	RefreshRuntimeTargetsLocked           func()
	RunBackground                         func(fn func(ctx context.Context)) bool
	SwapGitHubNativeStackPreferenceLocked func(enabled bool) bool
	UpdateCatalogStripEnvVars             func(names []string)
	UpdateRuntimeStripEnvVars             func(cfg *config.Config)
}
