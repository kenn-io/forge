package servertest

import (
	"encoding/json/v2"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/apiclient"
	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/federationauth"
	"go.kenn.io/forge/internal/gitclone"
	ghclient "go.kenn.io/forge/internal/github"
	"go.kenn.io/forge/internal/platformdb"
	"go.kenn.io/forge/internal/server"
	"go.kenn.io/forge/internal/server/authapi"
	"go.kenn.io/forge/internal/testutil"
	"go.kenn.io/forge/internal/testutil/dbtest"
	"go.kenn.io/forge/internal/testutil/gitfixture"
	"go.kenn.io/forge/internal/testutil/serverfake"
	"go.kenn.io/forge/platform"
)

func ListRepoNames(t *testing.T, srv *server.Server) []string {
	t.Helper()
	rr := testutil.DoJSON(t, srv, http.MethodGet, "/api/v1/repos", nil)
	require.Equal(t, http.StatusOK, rr.Code, rr.Body.String())
	var repos []struct {
		Name string `json:"name"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &repos, serverfake.V1JSON))
	names := make([]string, 0, len(repos))
	for _, repo := range repos {
		names = append(names, repo.Name)
	}
	return names
}

func NewFederationAuthTestServer(
	t *testing.T, scopes ...federationauth.Scope,
) (*httptest.Server, *federationauth.Store, string) {
	t.Helper()
	store, err := federationauth.Open(
		filepath.Join(t.TempDir(), "federation-credentials.json"),
	)
	require.NoError(t, err)
	token, err := store.MintInbound(
		"fedcba9876543210fedcba9876543210", scopes,
	)
	require.NoError(t, err)
	srv := server.New(dbtest.Open(t), nil, nil, "/", nil, server.ServerOptions{
		DaemonAccess: authapi.DaemonAccessOptions{
			Token: "local-secret", RequireAPIAuth: true,
		},
		FederationCredentials: store,
	})
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	return ts, store, token
}

func NewTelemetryTestServer(t *testing.T, telemetry *serverfake.FakeTelemetry) *server.Server {
	t.Helper()
	options := server.ServerOptions{}
	if telemetry != nil {
		options.Telemetry = telemetry
	}
	srv := server.New(
		serverfake.OpenTestDB(t), nil, nil, "/", nil,
		options,
	)
	t.Cleanup(func() { serverfake.GracefulShutdown(t, srv) })
	return srv
}

// setupGitLabIssueMutatorServer returns a server backed by a gitlab
// provider whose IssueMutator.CreateIssue returns the supplied error.
// The other capabilities are unchanged from setupGitLabCapabilityServer.
func SetupGitLabIssueMutatorServer(t *testing.T, createIssueErr error) *server.Server {
	t.Helper()
	require := require.New(t)
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Second)

	database := dbtest.Open(t)

	ref := platform.RepoRef{
		Platform:           platform.KindGitLab,
		Host:               "gitlab.example.com",
		Owner:              "group",
		Name:               "project",
		RepoPath:           "group/project",
		PlatformID:         4242,
		PlatformExternalID: "gid://gitlab/Project/4242",
		WebURL:             "https://gitlab.example.com/group/project",
		CloneURL:           "https://gitlab.example.com/group/project.git",
		DefaultBranch:      "main",
	}
	provider := &serverfake.IssueMutatorGitLabProvider{
		Ref: ref,
		MergeRequests: []platform.MergeRequest{{
			Repo:           ref,
			PlatformID:     7001,
			Number:         7,
			URL:            "https://gitlab.example.com/group/project/-/merge_requests/7",
			Title:          "Existing MR",
			Author:         "alice",
			State:          "open",
			HeadBranch:     "feature",
			BaseBranch:     "main",
			CreatedAt:      now,
			UpdatedAt:      now,
			LastActivityAt: now,
		}},
		Issues: []platform.Issue{{
			Repo:           ref,
			PlatformID:     8001,
			Number:         11,
			URL:            "https://gitlab.example.com/group/project/-/issues/11",
			Title:          "Existing",
			Author:         "alice",
			State:          "open",
			CreatedAt:      now,
			UpdatedAt:      now,
			LastActivityAt: now,
		}},
		ProviderErr: createIssueErr,
	}
	registry, err := platform.NewRegistry(provider)
	require.NoError(err)

	repo := ghclient.RepoRef{
		Platform:           platform.KindGitLab,
		Owner:              "group",
		Name:               "project",
		PlatformHost:       "gitlab.example.com",
		RepoPath:           "group/project",
		PlatformRepoID:     4242,
		PlatformExternalID: "gid://gitlab/Project/4242",
		WebURL:             "https://gitlab.example.com/group/project",
		CloneURL:           "https://gitlab.example.com/group/project.git",
		DefaultBranch:      "main",
	}
	syncer := ghclient.NewSyncerWithRegistry(
		registry, database, nil, []ghclient.RepoRef{repo}, time.Minute, nil, nil,
	)
	t.Cleanup(syncer.Stop)
	srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{})
	t.Cleanup(func() { serverfake.GracefulShutdown(t, srv) })

	syncer.RunOnce(ctx)
	return srv
}

func SetupSPAAssetServer(
	t *testing.T,
	basePath string,
	frontend fs.FS,
	options server.ServerOptions,
) *server.Server {
	t.Helper()
	database := dbtest.Open(t)

	mock := &serverfake.MockGH{}
	syncer := ghclient.NewSyncer(map[string]ghclient.Client{"github.com": mock}, database, nil, nil, time.Minute, nil, nil)
	t.Cleanup(syncer.Stop)
	return server.New(
		database,
		syncer,
		frontend,
		basePath,
		nil,
		options,
	)
}

func SetupTestClient(t *testing.T, srv *server.Server) *apiclient.Client {
	t.Helper()
	return SetupTestClientWithBaseURL(t, srv, "http://forge.test")
}

func SetupTestClientWithBaseURL(
	t *testing.T,
	srv *server.Server,
	baseURL string,
) *apiclient.Client {
	t.Helper()

	httpClient := &http.Client{
		Transport: serverfake.RoundTripFunc(func(req *http.Request) (*http.Response, error) {
			var body io.Reader = http.NoBody
			if req.Body != nil {
				payload, err := io.ReadAll(req.Body)
				if err != nil {
					return nil, err
				}
				_ = req.Body.Close()
				body = strings.NewReader(string(payload))
			}

			serverReq := httptest.NewRequest(req.Method, req.URL.String(), body)
			serverReq.Header = req.Header.Clone()
			serverReq = serverReq.WithContext(req.Context())

			rr := httptest.NewRecorder()
			srv.ServeHTTP(rr, serverReq)
			return rr.Result(), nil
		}),
	}

	client, err := apiclient.NewWithHTTPClient(baseURL, httpClient)
	require.NoError(t, err)

	return client
}

// setupTestServer opens a temp DB, builds a Server, and returns both.
func SetupTestServer(t *testing.T) (*server.Server, *db.DB) {
	t.Helper()
	return SetupTestServerWithMock(t, &serverfake.MockGH{})
}

func SetupTestServerWithClonesAndServer(t *testing.T) (
	client *apiclient.Client,
	database *db.DB,
	mergeBase string,
	headSHA string,
	commitSHAs []string,
	srv *server.Server,
) {
	t.Helper()
	serverfake.AcquireRootWorkspaceGitSlot(t)

	dir := t.TempDir()
	database = dbtest.Open(t)

	bareDir := filepath.Join(dir, "clones")
	require.NoError(t, os.MkdirAll(bareDir, 0o755))
	clones := gitclone.New(bareDir, nil)
	bare, err := clones.ClonePathForContext(
		gitclone.WithRepositoryIdentity(t.Context(), "repo-acme-widget"),
		"github", "github.com", "acme", "widget",
	)
	require.NoError(t, err)

	tmpWork := filepath.Join(dir, "work")
	gitfixture.Run(t, dir, "init", "--bare", "--initial-branch=main", bare)
	gitfixture.Run(t, dir, "clone", bare, tmpWork)
	gitfixture.Run(t, tmpWork, "config", "user.email", "test@test.com")
	gitfixture.Run(t, tmpWork, "config", "user.name", "Test")

	require.NoError(t, os.WriteFile(filepath.Join(tmpWork, "base.txt"), []byte("base\n"), 0o644))
	gitfixture.Run(t, tmpWork, "add", ".")
	gitfixture.Run(t, tmpWork, "commit", "-m", "base commit")
	gitfixture.Run(t, tmpWork, "push", "origin", "main")
	mergeBase = gitfixture.SHA(t, tmpWork, "HEAD")

	gitfixture.Run(t, tmpWork, "checkout", "-b", "pr")
	for i := 1; i <= 5; i++ {
		fname := fmt.Sprintf("file%d.txt", i)
		require.NoError(t, os.WriteFile(filepath.Join(tmpWork, fname), fmt.Appendf(nil, "content %d\n", i), 0o644))
		gitfixture.Run(t, tmpWork, "add", ".")
		gitfixture.Run(t, tmpWork, "commit", "-m", fmt.Sprintf("commit %d", i))
	}
	gitfixture.Run(t, tmpWork, "push", "origin", "pr")
	headSHA = gitfixture.SHA(t, tmpWork, "HEAD")

	// Collect SHAs newest-first.
	commitSHAs = make([]string, 5)
	sha := headSHA
	for i := range 5 {
		commitSHAs[i] = sha
		sha = gitfixture.SHA(t, tmpWork, sha+"^1")
	}

	mock := &serverfake.MockGH{}
	repos := []ghclient.RepoRef{{Platform: "github", Owner: "acme", Name: "widget", PlatformHost: "github.com"}}
	syncer := ghclient.NewSyncer(map[string]ghclient.Client{"github.com": mock}, database, nil, repos, time.Minute, nil, nil)
	t.Cleanup(syncer.Stop)
	srv = server.New(database, syncer, nil, "/", nil, server.ServerOptions{Clones: clones})

	serverfake.SeedPR(t, database, "acme", "widget", 1)
	ctx := t.Context()
	repoID, err := database.UpsertRepo(ctx, serverfake.VerifiedGitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(t, err)
	require.NoError(t, database.UpdateDiffSHAs(ctx, repoID, 1, headSHA, mergeBase, mergeBase))

	client = SetupTestClient(t, srv)
	return client, database, mergeBase, headSHA, commitSHAs, srv
}

func SetupTestServerWithConfig(
	t *testing.T,
) (*server.Server, *db.DB, string, *ghclient.Syncer) {
	return SetupTestServerWithConfigContent(t, serverfake.DefaultTestConfigContent, &serverfake.MockGH{})
}

func SetupTestServerWithConfigContent(
	t *testing.T,
	cfgContent string,
	mock *serverfake.MockGH,
) (*server.Server, *db.DB, string, *ghclient.Syncer) {
	return SetupTestServerWithConfigContentAndOptions(
		t, cfgContent, mock, server.ServerOptions{HostCheckAllowLoopbackAnyPort: true},
	)
}

func SetupTestServerWithConfigContentAndOptions(
	t *testing.T,
	cfgContent string,
	mock *serverfake.MockGH,
	options server.ServerOptions,
) (*server.Server, *db.DB, string, *ghclient.Syncer) {
	t.Helper()

	dir := t.TempDir()
	database := dbtest.Open(t)
	cfgPath := filepath.Join(dir, "config.toml")
	err := os.WriteFile(cfgPath, []byte(cfgContent), 0o644)
	require.NoError(t, err)

	cfg, err := config.Load(cfgPath)
	require.NoError(t, err)

	clients := map[string]ghclient.Client{"github.com": mock}
	resolved := ghclient.ResolveConfiguredRepos(
		t.Context(), clients, cfg.Repos,
	)
	syncer := ghclient.NewSyncer(
		clients, database, nil, resolved.Expanded,
		time.Minute, nil, nil,
	)
	t.Cleanup(syncer.Stop)
	srv := server.NewWithConfig(
		database, syncer, nil, nil, cfg, cfgPath,
		options,
	)
	t.Cleanup(func() { serverfake.GracefulShutdown(t, srv) })
	return srv, database, cfgPath, syncer
}

func SetupTestServerWithMock(t *testing.T, mock *serverfake.MockGH) (*server.Server, *db.DB) {
	t.Helper()
	return SetupTestServerWithRepos(t, mock, serverfake.DefaultTestRepos)
}

func SetupTestServerWithRepos(
	t *testing.T, mock *serverfake.MockGH, repos []ghclient.RepoRef,
) (*server.Server, *db.DB) {
	return SetupTestServerWithReposAndOptions(t, mock, repos, server.ServerOptions{})
}

func SetupTestServerWithReposAndOptions(
	t *testing.T, mock *serverfake.MockGH, repos []ghclient.RepoRef, options server.ServerOptions,
) (*server.Server, *db.DB) {
	t.Helper()

	database := dbtest.Open(t)
	repos = append([]ghclient.RepoRef(nil), repos...)
	for i := range repos {
		repo := &repos[i]
		if repo.PlatformExternalID == "" {
			repo.PlatformExternalID = "repo-" + repo.Owner + "-" + repo.Name
		}
		_, err := database.UpsertRepo(
			t.Context(), platformdb.DBRepoIdentity(platform.RepoRef{
				Platform:           platform.Kind(repo.Platform),
				Host:               repo.PlatformHost,
				Owner:              repo.Owner,
				Name:               repo.Name,
				RepoPath:           repo.RepoPath,
				PlatformExternalID: repo.PlatformExternalID,
			}),
		)
		require.NoError(t, err)
	}

	syncer := ghclient.NewSyncer(map[string]ghclient.Client{"github.com": mock}, database, nil, repos, time.Minute, nil, nil)
	// Drain any TriggerRun goroutines (fired by handlers like
	// POST /sync) before tests tear down. Registered after the DB
	// cleanup so LIFO ordering runs Stop first: without this, a
	// leaked goroutine from one test's handler can outlive its DB.
	t.Cleanup(syncer.Stop)
	var cfg *config.Config
	if options.WorktreeDir != "" {
		cfg = &config.Config{Tmux: config.Tmux{
			Command: []string{filepath.Join(t.TempDir(), "missing-tmux")},
		}}
	}
	srv := server.New(
		database, syncer, nil, "/",
		cfg, options,
	)
	// Registered after the DB cleanup so LIFO ordering runs Shutdown
	// first and lets background goroutines finish before DB close.
	t.Cleanup(func() { serverfake.GracefulShutdown(t, srv) })
	return srv, database
}

// setupTestServerWithRoborev creates a server with the roborev
// proxy configured to point at the given endpoint URL.
func SetupTestServerWithRoborev(
	t *testing.T, roborevEndpoint string,
) *server.Server {
	t.Helper()

	dir := t.TempDir()
	database := dbtest.Open(t)

	cfgContent := fmt.Sprintf(`
sync_interval = "5m"
github_token_env = "KENN_FORGE_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091

[[repos]]
owner = "acme"
name = "widget"

[roborev]
endpoint = %q
`, roborevEndpoint)

	cfgPath := filepath.Join(dir, "config.toml")
	err := os.WriteFile(cfgPath, []byte(cfgContent), 0o644)
	require.NoError(t, err)

	cfg, err := config.Load(cfgPath)
	require.NoError(t, err)

	mock := &serverfake.MockGH{}
	syncer := ghclient.NewSyncer(
		map[string]ghclient.Client{"github.com": mock},
		database, nil, nil, time.Minute, nil, nil,
	)
	t.Cleanup(syncer.Stop)
	return server.NewWithConfig(
		database, syncer, nil, nil, cfg, cfgPath,
		server.ServerOptions{HostCheckAllowLoopbackAnyPort: true},
	)
}
