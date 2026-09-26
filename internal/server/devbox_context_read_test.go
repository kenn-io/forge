package server

import (
	"context"
	"encoding/json/v2"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/devbox"
	"go.kenn.io/forge/internal/server/httpapi"
	"go.kenn.io/forge/internal/server/workspaceapi"
	"go.kenn.io/forge/internal/testutil/dbtest"
	"go.kenn.io/forge/internal/testutil/gitfixture"
	"go.kenn.io/forge/internal/testutil/reposeed"
	"go.kenn.io/forge/internal/workspace"
)

func TestDevboxReadsRenewExpiredContextOnce(t *testing.T) {
	acquireRootWorkspaceGitSlot(t)
	assert := assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	database := dbtest.Open(t)
	worktree := gitfixture.DivergenceWorktree(t)
	commit := gitfixture.SHA(t, worktree, "HEAD")
	identity := db.GitHubRepoIdentity("github.com", "example-org", "project")
	identity.PlatformRepoID = 1001
	_, err := reposeed.Seed(ctx, database, identity)
	require.NoError(err)
	issuedAt := time.Now().UTC().Truncate(time.Second)
	ws := &db.Workspace{ID: "work-a", Platform: "github", PlatformHost: "github.com", RepoOwner: "example-org", RepoName: "project", ItemType: db.WorkspaceItemTypePullRequest, ItemNumber: 7, ItemKey: "7", GitHeadRef: "feature", WorkspaceBranch: "feature", WorktreePath: worktree, Status: "ready"}
	spec := db.WorkspaceLaunchSpec{
		Version:    db.WorkspaceLaunchSpecVersion,
		Repository: db.WorkspaceLaunchRepository{Provider: "github", PlatformHost: "github.com", PlatformRepoID: identity.PlatformRepoID, Owner: "example-org", Name: "project", CloneURL: "https://github.com/example-org/project.git", DefaultBranch: "main"},
		ItemType:   db.WorkspaceItemTypePullRequest, ItemNumber: 7, ItemKey: "7", GitHeadRef: "feature",
		Pull:          &db.WorkspaceLaunchPull{HeadBranch: "feature", BaseBranch: "main", HeadRepoKind: "same_repo", SnapshotRevision: 1},
		SourceVisible: true, IssuedAt: issuedAt, SourceVisibleUntil: issuedAt.Add(db.WorkspaceLaunchSpecVisibilityLease),
	}
	require.NoError(database.CreateWorkspaceWithLaunchSpec(ctx, ws, spec))
	socket := filepath.Join(t.TempDir(), "broker.sock")
	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "unix", socket)
	require.NoError(err)
	var denyCredential atomic.Bool
	broker := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !assert.Equal("POST /v1/credentials", r.Method+" "+r.URL.Path) {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var request devbox.CredentialRequest
		if !assert.NoError(json.UnmarshalRead(r.Body, &request)) {
			return
		}
		assert.Equal("example-org/project", request.Repository)
		assert.Equal("git", request.Profile)
		if denyCredential.Load() {
			http.Error(w, "repository access denied", http.StatusForbidden)
			return
		}
		assert.NoError(json.MarshalWrite(w, devbox.Credential{Token: "fixture-token", ExpiresAt: time.Now().Add(time.Hour), GitHubUserID: 1234, RepositoryID: identity.PlatformRepoID, DefaultBranch: "main"}))
	})}
	go func() { _ = broker.Serve(listener) }()
	t.Cleanup(func() { _ = broker.Close() })
	manager := workspace.NewManager(database, t.TempDir())
	var elapsed atomic.Int64
	worker := workspaceapi.New(workspaceapi.Deps{
		DB: database, Workspaces: manager, ExecutionWorker: config.ExecutionWorker{Enabled: true, BrokerSocket: socket, GitHubUserID: 1234}, EnrichmentDisabled: true,
		Now: func() time.Time { return issuedAt.Add(time.Duration(elapsed.Load())) },
	})
	t.Cleanup(func() { require.NoError(worker.Shutdown(context.Background())) })
	workerMux := http.NewServeMux()
	workerAPI := humago.NewWithPrefix(workerMux, "/api/v1", huma.DefaultConfig("worker", "1"))
	worker.RegisterExecution(workerAPI)
	worker.RegisterWorker(workerAPI)
	var reads, renewals atomic.Int64
	var expireAgain atomic.Bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !assert.Equal("Bearer fixture-bearer", r.Header.Get("Authorization")) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		renewal := r.Method == "PUT" && r.URL.Path == "/api/v1/worker/workspaces/work-a/context"
		if renewal {
			renewals.Add(1)
		} else if r.Method == "GET" && r.URL.Path != "/api/v1/workspaces/work-a" {
			reads.Add(1)
		}
		workerMux.ServeHTTP(w, r)
		if renewal && expireAgain.Load() {
			elapsed.Add(int64(db.WorkspaceLaunchSpecVisibilityLease))
		}
	}))
	t.Cleanup(upstream.Close)
	directory := t.TempDir()
	saved, err := json.Marshal([]any{map[string]any{"id": "compute-a", "profile": devbox.Profile{Assignment: devbox.Assignment{URL: upstream.URL}, Token: "fixture-bearer"}}})
	require.NoError(err)
	require.NoError(os.WriteFile(filepath.Join(directory, "devbox-connections.json"), saved, 0o600))
	connections, err := devbox.OpenConnections(directory)
	require.NoError(err)
	t.Cleanup(connections.Close)
	controllerDB := dbtest.Open(t)
	repoID, err := reposeed.Seed(ctx, controllerDB, identity)
	require.NoError(err)
	require.NoError(controllerDB.UpdateRepoProviderObservation(ctx, repoID, db.RepoProviderMetadata{CloneURL: spec.Repository.CloneURL, DefaultBranch: "main"}, nil, nil))
	_, err = controllerDB.UpsertMergeRequest(ctx, &db.MergeRequest{
		RepoID: repoID, PlatformID: 7, Number: 7, Title: "Update project", Author: "developer-a", State: "open",
		URL: "https://github.com/example-org/project/pull/7", HeadBranch: "feature", BaseBranch: "main", SnapshotRevision: 1,
		CreatedAt: issuedAt, UpdatedAt: issuedAt, LastActivityAt: issuedAt,
	})
	require.NoError(err)
	controller := &Server{
		options: ServerOptions{Devboxes: connections}, db: controllerDB,
		repoResolver: httpapi.NewRepositoryResolver(httpapi.RepositoryResolverDeps{DB: controllerDB}),
		now:          func() time.Time { return issuedAt.Add(time.Duration(elapsed.Load())) },
	}
	mux := http.NewServeMux()
	controller.registerDevboxAPI(humago.New(mux, huma.DefaultConfig("controller", "1")))
	request := func(path string) *httptest.ResponseRecorder {
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/devboxes/compute-a/workspaces/work-a"+path, nil))
		return response
	}
	routes := []string{
		"/commits", "/diff?commit=" + commit,
		"/files?from=" + commit + "&to=" + commit,
		"/file-preview?path=f.txt&base=merge-target",
	}
	for _, path := range routes {
		response := request(path)
		require.Equal(http.StatusOK, response.Code, "fresh %s: %s", path, response.Body.String())
	}
	assert.Zero(renewals.Load(), "fresh reads must not renew context")
	assert.Equal(int64(len(routes)), reads.Load())

	elapsed.Store(int64(db.WorkspaceLaunchSpecVisibilityLease))
	for _, path := range routes {
		// Each route starts with the original expired lease, independently of earlier reads.
		require.NoError(database.PutWorkspaceLaunchSpec(ctx, ws.ID, spec))
		reads.Store(0)
		renewals.Store(0)
		response := request(path)
		require.Equal(http.StatusOK, response.Code, "expired %s: %s", path, response.Body.String())
		assert.Equal(int64(1), renewals.Load(), path)
		assert.Equal(int64(2), reads.Load(), path)
		response = request(path)
		require.Equal(http.StatusOK, response.Code, "renewed %s: %s", path, response.Body.String())
		assert.Equal(int64(1), renewals.Load(), "a renewed read must not renew again: %s", path)
		assert.Equal(int64(3), reads.Load(), path)
	}

	// These purely local scopes remain readable without renewal while the lease is expired.
	require.NoError(database.PutWorkspaceLaunchSpec(ctx, ws.ID, spec))
	renewals.Store(0)
	for _, path := range []string{"/diff", "/diff?base=pushed", "/diff/watch"} {
		response := request(path)
		require.Equal(http.StatusOK, response.Code, "%s: %s", path, response.Body.String())
	}
	assert.Zero(renewals.Load())

	denyCredential.Store(true)
	reads.Store(0)
	response := request("/commits")
	assert.Equal(http.StatusConflict, response.Code, response.Body.String())
	assert.Equal(int64(1), renewals.Load())
	assert.Equal(int64(1), reads.Load(), "failed renewal must not retry the read")
	stored, err := database.GetWorkspaceLaunchSpec(ctx, ws.ID)
	require.NoError(err)
	require.NotNil(stored)
	assert.Equal(spec.SourceVisibleUntil, stored.SourceVisibleUntil, "failed admission must not renew the lease")

	denyCredential.Store(false)
	expireAgain.Store(true)
	reads.Store(0)
	renewals.Store(0)
	response = request("/commits")
	assert.Equal(http.StatusConflict, response.Code, response.Body.String())
	assert.Equal(int64(1), renewals.Load())
	assert.Equal(int64(2), reads.Load(), "an expired retry must not start another renewal")

	expireAgain.Store(false)
	require.NoError(database.UpdateWorkspaceStatus(ctx, ws.ID, "creating", nil))
	reads.Store(0)
	renewals.Store(0)
	response = request("/commits")
	assert.Equal(http.StatusConflict, response.Code, response.Body.String())
	assert.Equal(int64(1), reads.Load())
	assert.Zero(renewals.Load(), "an unrelated 409 must be returned without renewal")
}
