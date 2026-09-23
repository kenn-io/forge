package streamapi

import (
	context "context"
	sync "sync"
	atomic "sync/atomic"

	config "go.kenn.io/forge/internal/config"
	dbpkg "go.kenn.io/forge/internal/db"
	ptyowner "go.kenn.io/forge/internal/ptyowner"
	authapi "go.kenn.io/forge/internal/server/authapi"
	configreload "go.kenn.io/forge/internal/server/configreload"
	fleetapi "go.kenn.io/forge/internal/server/fleetapi"
	issueapi "go.kenn.io/forge/internal/server/issueapi"
	kata "go.kenn.io/forge/internal/server/kata"
	pullapi "go.kenn.io/forge/internal/server/pullapi"
	spokeapi "go.kenn.io/forge/internal/server/spokeapi"
	syncevents "go.kenn.io/forge/internal/server/syncevents"
	workspaceapi "go.kenn.io/forge/internal/server/workspaceapi"
	workspace "go.kenn.io/forge/internal/workspace"
	localruntime "go.kenn.io/forge/internal/workspace/localruntime"
)

// Handlers holds the server state and cross-package calls this package's
// handlers use. The server package builds it in wireHandlers and shares
// mutable server fields by pointer.
type Handlers struct {
	AllowedHostMu             *sync.RWMutex
	AllowedHosts              *map[string]struct{}
	BasePath                  *string
	Bg                        *sync.WaitGroup
	BgCtx                     context.Context
	BgMu                      *sync.Mutex
	BootCfgSnapshot           *configreload.StartupConfigSnapshot
	Cfg                       **config.Config
	CfgMu                     *sync.Mutex
	ConnWG                    *sync.WaitGroup
	DaemonRequests            *authapi.DaemonRequestPolicy
	Db                        *dbpkg.DB
	FleetAPI                  **fleetapi.Handler
	FleetEnabledAtBoot        *bool
	HostOpts                  *atomic.Pointer[authapi.HostCheckOptions]
	Hub                       **syncevents.EventHub
	HubEvents                 **spokeapi.HubEventLifecycle
	IssueAPI                  **issueapi.Handler
	KataAPI                   **kata.Handler
	PtyOwnerClient            **ptyowner.Client
	PullAPI                   **pullapi.Handler
	Runtime                   **localruntime.Manager
	ShuttingDown              *bool
	SpokeActivationLease      **spokeapi.HubEventLifecycle
	TmuxCmd                   *[]string
	WorkspaceAPI              **workspaceapi.Handler
	WorkspaceDependentsCancel *context.CancelFunc
	WorkspaceDependentsCtx    *context.Context
	WorkspaceDependentsDone   chan struct{}
	WorkspaceDependentsOnce   *sync.Once
	WorkspaceDependentsWG     *sync.WaitGroup
	Workspaces                **workspace.Manager
	ReconnectStaleEvent       func() syncevents.Event
}
