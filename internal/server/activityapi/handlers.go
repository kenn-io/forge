package activityapi

import (
	context "context"
	sync "sync"
	time "time"

	config "go.kenn.io/forge/internal/config"
	dbpkg "go.kenn.io/forge/internal/db"
	gitclone "go.kenn.io/forge/internal/gitclone"
	ghclient "go.kenn.io/forge/internal/github"
	providerplane "go.kenn.io/forge/internal/providerplane"
	fleetapi "go.kenn.io/forge/internal/server/fleetapi"
	httpapi "go.kenn.io/forge/internal/server/httpapi"
	issueapi "go.kenn.io/forge/internal/server/issueapi"
	itemapi "go.kenn.io/forge/internal/server/itemapi"
	pullapi "go.kenn.io/forge/internal/server/pullapi"
	spokeapi "go.kenn.io/forge/internal/server/spokeapi"
	workspaceapi "go.kenn.io/forge/internal/server/workspaceapi"
)

// Handlers holds the server state and cross-package calls this package's
// handlers use. The server package builds it in wireHandlers and shares
// mutable server fields by pointer.
type Handlers struct {
	ActivityAfterItemsForTest             *func()
	Cfg                                   **config.Config
	CfgMu                                 *sync.Mutex
	Clones                                *gitclone.Manager
	Db                                    *dbpkg.DB
	FleetAPI                              **fleetapi.Handler
	IssueAPI                              **issueapi.Handler
	Now                                   *func() time.Time
	ProviderSource                        **spokeapi.HubProviderSource
	PullAPI                               **pullapi.Handler
	RepoResolver                          *httpapi.RepositoryResolver
	Syncer                                **ghclient.Syncer
	WorkspaceAPI                          **workspaceapi.Handler
	DefaultPlatformHost                   func() string
	EnqueueDetailSync                     func(key string, attrs []any, fn func(context.Context) error) bool
	EnqueueDetailSyncOrRerun              func(key string, attrs []any, fn func(context.Context) error) bool
	FilterConfiguredRepoSummaries         func(summaries []dbpkg.RepoSummary) []dbpkg.RepoSummary
	FilterConfiguredRepos                 func(repos []dbpkg.Repo) []dbpkg.Repo
	FilterHiddenRepoSummaries             func(ctx context.Context, summaries []dbpkg.RepoSummary) ([]dbpkg.RepoSummary, error)
	FilterHiddenRepos                     func(ctx context.Context, repos []dbpkg.Repo) ([]dbpkg.Repo, error)
	NotificationsEnabled                  func() bool
	RepoResponse                          func(repo dbpkg.Repo) itemapi.RepoResponse
	ResolveAuthenticatedViewerLogins      func(ctx context.Context, filters []dbpkg.RepoFilter) ([]dbpkg.RepoViewerLogin, error)
	RunBackground                         func(fn func(ctx context.Context)) bool
	ToRepoSummaryResponse                 func(summary dbpkg.RepoSummary, defaultPlatformHost string) itemapi.RepoSummaryResponse
	WorkspaceActivityRepositoryIdentities func(ctx context.Context, snapshot workspaceapi.WorkspaceSubjectSnapshot) (map[int64]providerplane.RepositoryIdentity, error)
	WorkspaceActivityResponse             func(input *itemapi.ListActivityInput, opts dbpkg.ListActivityOpts, snapshot workspaceapi.WorkspaceSubjectSnapshot, providerItems []dbpkg.ActivityItem, useWorkspaceActivityForRecency bool) []itemapi.WorkspaceActivitySubjectResponse
}
