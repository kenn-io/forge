package kataapi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
	"github.com/stretchr/testify/require"
	gitcmd "go.kenn.io/kit/git/cmd"
	"go.kenn.io/middleman/internal/config"
	"go.kenn.io/middleman/internal/db"
	"go.kenn.io/middleman/internal/server/httpapi"
	"go.kenn.io/middleman/internal/server/workspaceapi"
	"go.kenn.io/middleman/internal/testutil/dbtest"
	"go.kenn.io/middleman/internal/workspace"
)

type mockGH struct{}

type ServerOptions struct {
	HostCheckAllowLoopbackAnyPort      bool
	WorktreeDir                        string
	DisableWorkspaceBackgroundMonitors bool
}

type Server struct {
	*Handler
	http         http.Handler
	workspaceAPI *workspaceapi.Handler
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.http.ServeHTTP(w, r)
}

func setupTestServer(t *testing.T) (*Server, *db.DB) {
	t.Helper()
	database := dbtest.Open(t)
	return newKataTestServer(t, database, nil, ServerOptions{}), database
}

func setupTestServerWithConfigContent(
	t *testing.T,
	cfgContent string,
	_ *mockGH,
) (*Server, *db.DB, string) {
	return setupTestServerWithConfigContentAndOptions(
		t, cfgContent, nil, ServerOptions{},
	)
}

func setupTestServerWithConfig(t *testing.T) (*Server, *db.DB, string) {
	return setupTestServerWithConfigContent(t, `
sync_interval = "5m"
github_token_env = "MIDDLEMAN_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091

[[repos]]
owner = "acme"
name = "widget"
`, nil)
}

func setupTestServerWithConfigContentAndOptions(
	t *testing.T,
	cfgContent string,
	_ *mockGH,
	options ServerOptions,
) (*Server, *db.DB, string) {
	t.Helper()
	database := dbtest.Open(t)
	cfgPath := filepath.Join(t.TempDir(), "config.toml")
	require.NoError(t, os.WriteFile(cfgPath, []byte(cfgContent), 0o644))
	cfg, err := config.Load(cfgPath)
	require.NoError(t, err)
	return newKataTestServer(t, database, cfg, options), database, cfgPath
}

func newKataTestServer(
	t *testing.T,
	database *db.DB,
	cfg *config.Config,
	options ServerOptions,
) *Server {
	t.Helper()
	resolver := httpapi.NewRepositoryResolver(httpapi.RepositoryResolverDeps{DB: database})
	var workspaces *workspace.Manager
	if options.WorktreeDir != "" {
		workspaces = workspace.NewManager(database, options.WorktreeDir)
	}
	workspaceHandler := workspaceapi.New(workspaceapi.Deps{
		DB: database, Resolver: resolver, Workspaces: workspaces,
	})
	handler := New(Deps{
		DB:               database,
		Resolver:         resolver,
		Config:           testKataConfigSnapshot(cfg),
		Workspaces:       workspaces,
		WorkspaceAPI:     workspaceHandler.Workspaces(),
		SamePlatformHost: testSamePlatformHost,
		ConfigRepoPath:   testConfigRepoPath,
	})
	mux := http.NewServeMux()
	apiConfig := huma.DefaultConfig("middleman API", "0.1.0")
	apiConfig.OpenAPIPath = "/openapi"
	apiConfig.DocsPath = "/docs"
	apiConfig.SchemasPath = "/schemas"
	api := humago.NewWithPrefix(mux, "/api/v1", apiConfig)
	handler.Register(api)
	server := &Server{Handler: handler, http: mux, workspaceAPI: workspaceHandler}
	handler.Start(t.Context())
	t.Cleanup(func() {
		require.NoError(t, handler.Shutdown(context.Background()))
		require.NoError(t, workspaceHandler.Shutdown(context.Background()))
	})
	return server
}

func testKataConfigSnapshot(cfg *config.Config) ConfigSnapshot {
	if cfg == nil {
		return ConfigSnapshot{}
	}
	return ConfigSnapshot{Repos: cfg.Repos, KataProjects: cfg.KataProjects}
}

func testSamePlatformHost(left, right string) bool {
	if left == "" {
		left = "github.com"
	}
	if right == "" {
		right = "github.com"
	}
	return strings.EqualFold(left, right)
}

func testConfigRepoPath(repo config.Repo) string {
	if strings.TrimSpace(repo.RepoPath) != "" {
		return strings.TrimSpace(repo.RepoPath)
	}
	return repo.Owner + "/" + repo.Name
}

func doJSON(
	t *testing.T,
	srv *Server,
	method, path string,
	body any,
) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		require.NoError(t, json.NewEncoder(&buf).Encode(body))
	}
	req := httptest.NewRequest(method, path, &buf)
	if method != http.MethodGet {
		req.Header.Set("Content-Type", "application/json")
	}
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	return rr
}

func decodeProblem(t *testing.T, rr *httptest.ResponseRecorder) httpapi.ProblemError {
	t.Helper()
	var body httpapi.ProblemError
	require.NoError(t, json.NewDecoder(rr.Body).Decode(&body))
	return body
}

type sseFrame struct {
	ID    string
	Event string
	Data  string
}

type sseReadResult struct {
	frame sseFrame
	err   error
}

func readSSEFrameWithin(
	t *testing.T,
	scanner *bufio.Scanner,
	timeout time.Duration,
	stop func(),
) sseFrame {
	t.Helper()
	result := make(chan sseReadResult, 1)
	go func() {
		var frame sseFrame
		for scanner.Scan() {
			line := scanner.Text()
			switch {
			case strings.HasPrefix(line, "id: "):
				frame.ID = strings.TrimPrefix(line, "id: ")
			case strings.HasPrefix(line, "event: "):
				frame.Event = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				frame.Data = strings.TrimPrefix(line, "data: ")
			case line == "" && frame.Event != "":
				result <- sseReadResult{frame: frame}
				return
			}
		}
		if err := scanner.Err(); err != nil {
			result <- sseReadResult{err: err}
			return
		}
		result <- sseReadResult{err: io.ErrUnexpectedEOF}
	}()
	select {
	case got := <-result:
		require.NoError(t, got.err)
		return got.frame
	case <-time.After(timeout):
		if stop != nil {
			stop()
		}
		require.FailNow(t, "timed out reading SSE frame")
		return sseFrame{}
	}
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	runner := gitcmd.New().WithConfig("init.defaultBranch", "main")
	out, stderr, err := runner.Run(t.Context(), dir, nil, args...)
	require.NoError(t, err, "git %v failed: %s%s", args, out, stderr)
}

func gitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, stderr, err := gitcmd.New().Run(t.Context(), dir, nil, args...)
	require.NoError(t, err, "git %v failed: %s%s", args, out, stderr)
	return strings.TrimSpace(string(out))
}

func setupHTTPWorktreeBaseForServerTest(
	t *testing.T,
	branch string,
) (repo, remote, platformHost string) {
	t.Helper()
	root := t.TempDir()
	remote = filepath.Join(root, "acme", "widget.git")
	repo = filepath.Join(root, "repo")
	require.NoError(t, os.MkdirAll(filepath.Dir(remote), 0o755))
	runGit(t, root, "init", "--bare", "--initial-branch=main", remote)
	server := httptest.NewServer(http.FileServer(http.Dir(root)))
	t.Cleanup(server.Close)
	remoteURL := server.URL + "/acme/widget.git"
	parsed, err := url.Parse(server.URL)
	require.NoError(t, err)
	platformHost = parsed.Host

	runGit(t, root, "init", "--initial-branch=main", repo)
	runGit(t, repo, "config", "user.email", "test@test.com")
	runGit(t, repo, "config", "user.name", "Test")
	runGit(t, repo, "remote", "add", "origin", remote)
	require.NoError(t, os.WriteFile(filepath.Join(repo, "base.txt"), []byte("base\n"), 0o644))
	runGit(t, repo, "add", ".")
	runGit(t, repo, "commit", "-m", "base commit")
	runGit(t, repo, "push", "origin", "HEAD:refs/heads/main")
	runGit(t, remote, "symbolic-ref", "HEAD", "refs/heads/main")
	runGit(t, repo, "push", "origin", "HEAD:refs/heads/"+branch)
	runGit(t, remote, "update-server-info")
	runGit(t, repo, "remote", "set-url", "origin", remoteURL)
	runGit(t, repo, "fetch", "--prune", "origin")
	runGit(t, repo, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
	return repo, remote, platformHost
}
