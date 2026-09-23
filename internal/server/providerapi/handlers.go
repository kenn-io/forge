package providerapi

import (
	context "context"
	time "time"

	dbpkg "go.kenn.io/forge/internal/db"
	ghclient "go.kenn.io/forge/internal/github"
	activityapi "go.kenn.io/forge/internal/server/activityapi"
	httpapi "go.kenn.io/forge/internal/server/httpapi"
	itemapi "go.kenn.io/forge/internal/server/itemapi"
	workspaceapi "go.kenn.io/forge/internal/server/workspaceapi"
)

// Handlers holds the server state and cross-package calls this package's
// handlers use. The server package builds it in wireHandlers and shares
// mutable server fields by pointer.
type Handlers struct {
	Db                                      *dbpkg.DB
	MarkdownImages                          **MarkdownImageCache
	Now                                     *func() time.Time
	ProviderDescriptorBeforeSnapshotForTest *func()
	RepoResolver                            *httpapi.RepositoryResolver
	Syncer                                  **ghclient.Syncer
	WorkspaceAPI                            **workspaceapi.Handler
	EnqueueIssueSync                        func(ctx context.Context, input *itemapi.IssueRepoNumberInput) (*itemapi.AcceptedOutput, error)
	EnqueuePRSync                           func(ctx context.Context, input *itemapi.RepoNumberInput) (*itemapi.AcceptedOutput, error)
	GetCommentAutocomplete                  func(ctx context.Context, input *itemapi.CommentAutocompleteInput) (*itemapi.CommentAutocompleteOutput, error)
	GetRepo                                 func(ctx context.Context, input *itemapi.GetRepoInput) (*itemapi.GetRepoOutput, error)
	GetRepoCommitDiff                       func(ctx context.Context, input *activityapi.GetRepoCommitDiffInput) (*activityapi.GetRepoCommitDiffOutput, error)
	GetRepoCommitDiffOnHost                 func(ctx context.Context, input *activityapi.GetRepoCommitDiffHostInput) (*activityapi.GetRepoCommitDiffOutput, error)
	ListRepoLabels                          func(ctx context.Context, input *itemapi.GetRepoInput) (*itemapi.ListRepoLabelsOutput, error)
	ResolveItem                             func(ctx context.Context, input *itemapi.ResolveItemInput) (*itemapi.ResolveItemOutput, error)
	SyncIssue                               func(ctx context.Context, input *itemapi.IssueRepoNumberInput) (*itemapi.SyncIssueOutput, error)
	SyncPR                                  func(ctx context.Context, input *itemapi.RepoNumberInput) (*itemapi.SyncPROutput, error)
	SyncPRCI                                func(ctx context.Context, input *itemapi.RepoNumberInput) (*itemapi.SyncPRCIOutput, error)
}
