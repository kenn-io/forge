package operationapi

import (
	context "context"
	sync "sync"
	time "time"

	dbpkg "go.kenn.io/forge/internal/db"
	httpapi "go.kenn.io/forge/internal/server/httpapi"
)

// Handlers holds the server state and cross-package calls this package's
// handlers use. The server package builds it in wireHandlers and shares
// mutable server fields by pointer.
type Handlers struct {
	BgCtx                        context.Context
	Now                          *func() time.Time
	RepoResolver                 *httpapi.RepositoryResolver
	WriteCredProbeInFlight       *map[string]chan struct{}
	WriteCredProbeMu             *sync.Mutex
	WriteCredProbes              *map[string]WriteCredentialProbe
	MergeRequestAuthoredByViewer func(ctx context.Context, repo dbpkg.Repo, mr dbpkg.MergeRequest) bool
	MutationRateLimitedReason    func(repo dbpkg.Repo, bucket ApiBucket) RateLimitAvailability
	OperationRateLimitBuckets    func(repo dbpkg.Repo, op OperationDescriptor) ([]ApiBucket, bool)
	WriteCredentialGateForRepo   func(repo dbpkg.Repo) WriteCredentialGate
}
