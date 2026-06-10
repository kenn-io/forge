package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	Assert "github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/kit/git/env"
	"go.kenn.io/middleman/internal/config"
	"go.kenn.io/middleman/internal/docs"
	"go.kenn.io/middleman/internal/procutil"
)

type docsGitRepo struct {
	dir    string
	remote string
}

func newDocsGitRepo(t *testing.T, upstream bool) docsGitRepo {
	t.Helper()
	if err := procutil.Command("git", "--version").Run(); err != nil {
		t.Skip("git binary unavailable")
	}
	dir := t.TempDir()
	remote := t.TempDir()
	runDocsGit(t, dir, "init", "-b", "main")
	runDocsGit(t, dir, "config", "user.email", "middleman-fixture@example.invalid")
	runDocsGit(t, dir, "config", "user.name", "Middleman Fixture")
	runDocsGit(t, dir, "config", "commit.gpgsign", "false")
	runDocsGit(t, dir, "config", "tag.gpgsign", "false")
	runDocsGit(t, dir, "config", "core.hooksPath", ".git/hooks")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "seed.md"), []byte("seed\n"), 0o644))
	runDocsGit(t, dir, "add", "seed.md")
	runDocsGit(t, dir, "commit", "-m", "seed")
	if upstream {
		runDocsGit(t, remote, "init", "--bare")
		runDocsGit(t, dir, "remote", "add", "origin", remote)
		runDocsGit(t, dir, "push", "-u", "origin", "main")
	}
	return docsGitRepo{dir: dir, remote: remote}
}

func runDocsGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := procutil.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = cleanDocsGitEnv(os.Environ())
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %v: %s", args, string(out))
	return string(out)
}

func cleanDocsGitEnv(env []string) []string {
	return gitenv.StripAll(env)
}

func TestDocsGitFixtureDoesNotLeakIntoInheritedGitConfig(t *testing.T) {
	require := require.New(t)

	host := t.TempDir()
	runDocsGit(t, host, "init", "-q", "-b", "main")
	hostConfig := filepath.Join(host, ".git", "config")
	before, err := os.ReadFile(hostConfig)
	require.NoError(err)

	t.Setenv("GIT_CONFIG", hostConfig)
	t.Setenv("GIT_DIR", filepath.Join(host, ".git"))
	t.Setenv("GIT_WORK_TREE", host)

	_ = newDocsGitRepo(t, false)

	after, err := os.ReadFile(hostConfig)
	require.NoError(err)
	require.Equal(string(before), string(after))
}

func (g docsGitRepo) write(t *testing.T, rel, body string) {
	t.Helper()
	full := filepath.Join(g.dir, filepath.FromSlash(rel))
	require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
	require.NoError(t, os.WriteFile(full, []byte(body), 0o644))
}

func setupDocsGitRouteServer(t *testing.T, root string) *Server {
	t.Helper()
	cfg := &config.Config{
		DocFolders: []config.DocFolder{{ID: "f", Name: "F", Path: root}},
	}
	srv := New(openTestDB(t), nil, nil, "/", cfg, ServerOptions{})
	t.Cleanup(func() { gracefulShutdown(t, srv) })
	return srv
}

func TestDocsGitStatusEndpointReturnsEntries(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	repo := newDocsGitRepo(t, true)
	repo.write(t, "new.md", "# new\n")
	srv := setupDocsGitRouteServer(t, repo.dir)

	rr := doDocsJSON(t, srv, http.MethodGet, "/api/v1/docs/folders/f/git", nil)

	require.Equal(http.StatusOK, rr.Code, rr.Body.String())
	var body docs.GitStatusResponse
	require.NoError(json.NewDecoder(rr.Body).Decode(&body))
	assert.True(body.IsRepo)
	require.NotEmpty(body.Entries)
	assert.Equal("new.md", body.Entries[0].Path)
	assert.Equal(docs.GitStatusUntracked, body.Entries[0].Status)
}

func TestDocsGitChangesEndpointReturnsPreview(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	repo := newDocsGitRepo(t, true)
	repo.write(t, "new.md", "# new\n")
	srv := setupDocsGitRouteServer(t, repo.dir)

	rr := doDocsJSON(t, srv, http.MethodGet, "/api/v1/docs/folders/f/git/changes", nil)

	require.Equal(http.StatusOK, rr.Code, rr.Body.String())
	var body docs.GitChangesResponse
	require.NoError(json.NewDecoder(rr.Body).Decode(&body))
	assert.True(body.IsRepo)
	assert.Equal("main", body.Branch)
	assert.Equal("origin/main", body.Upstream)
	require.Len(body.Changes, 1)
	assert.Equal("new.md", body.Changes[0].Path)
	assert.NotEmpty(body.SuggestedMessage)
}

func TestDocsGitChangesEndpointNotARepoAndUnknownFolder(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	srv := setupDocsGitRouteServer(t, t.TempDir())

	notRepoRR := doDocsJSON(t, srv, http.MethodGet, "/api/v1/docs/folders/f/git/changes", nil)
	require.Equal(http.StatusOK, notRepoRR.Code, notRepoRR.Body.String())
	var notRepo docs.GitChangesResponse
	require.NoError(json.NewDecoder(notRepoRR.Body).Decode(&notRepo))
	assert.False(notRepo.IsRepo)
	assert.NotNil(notRepo.Changes)

	missingRR := doDocsJSON(t, srv, http.MethodGet, "/api/v1/docs/folders/missing/git/changes", nil)
	assert.Equal(http.StatusNotFound, missingRR.Code, missingRR.Body.String())
}

func TestDocsGitStatusAndChangesEndpointsRejectUnsafeAttributes(t *testing.T) {
	repo := newDocsGitRepo(t, true)
	repo.write(t, ".gitattributes", "*.md filter=evil\n")
	srv := setupDocsGitRouteServer(t, repo.dir)

	for _, path := range []string{
		"/api/v1/docs/folders/f/git",
		"/api/v1/docs/folders/f/git/changes",
	} {
		t.Run(path, func(t *testing.T) {
			assert := Assert.New(t)
			require := require.New(t)

			rr := doDocsJSON(t, srv, http.MethodGet, path, nil)

			require.Equal(http.StatusBadRequest, rr.Code, rr.Body.String())
			var problem ProblemError
			require.NoError(json.NewDecoder(rr.Body).Decode(&problem))
			assert.Equal(CodeBadRequest, problem.Code)
			assert.Equal("unsafeGitConfig", problem.Details["reason"])
		})
	}
}

func TestDocsGitChangesEndpointRejectsUnsafeLocalConfig(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	repo := newDocsGitRepo(t, true)
	repo.write(t, "new.md", "# new\n")
	// Command-bearing local config does not execute during the status
	// read, but the preview must still refuse it so the UI's publish
	// signal matches publish-time behavior.
	runDocsGit(t, repo.dir, "config", "gpg.program", "/tmp/evil")
	srv := setupDocsGitRouteServer(t, repo.dir)

	rr := doDocsJSON(t, srv, http.MethodGet, "/api/v1/docs/folders/f/git/changes", nil)

	require.Equal(http.StatusBadRequest, rr.Code, rr.Body.String())
	var problem ProblemError
	require.NoError(json.NewDecoder(rr.Body).Decode(&problem))
	assert.Equal(CodeBadRequest, problem.Code)
	assert.Equal("unsafeGitConfig", problem.Details["reason"])
}

func TestDocsGitReadEndpointsRejectNonLoopback(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	repo := newDocsGitRepo(t, true)
	srv := setupDocsGitRouteServer(t, repo.dir)
	for _, path := range []string{
		"/api/v1/docs/folders/f/git",
		"/api/v1/docs/folders/f/git/changes",
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.RemoteAddr = "203.0.113.7:54321"
		rr := httptest.NewRecorder()

		srv.ServeHTTP(rr, req)

		require.Equal(http.StatusForbidden, rr.Code, rr.Body.String())
		var problem ProblemError
		require.NoError(json.NewDecoder(rr.Body).Decode(&problem))
		assert.Equal(CodeForbidden, problem.Code)
		assert.Equal("loopbackOnly", problem.Details["reason"])
	}
}

func TestDocsGitPublishEndpointHappyPath(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	repo := newDocsGitRepo(t, true)
	repo.write(t, "new.md", "# new\n")
	srv := setupDocsGitRouteServer(t, repo.dir)

	rr := doDocsJSON(t, srv, http.MethodPost, "/api/v1/docs/folders/f/git/publish", map[string]string{
		"message": "docs: update new.md\n\n- new.md\n",
	})

	require.Equal(http.StatusOK, rr.Code, rr.Body.String())
	var body docs.PublishResponse
	require.NoError(json.NewDecoder(rr.Body).Decode(&body))
	assert.NotEmpty(body.Commit)
	assert.Equal(body.Commit[:7], body.ShortCommit)
	assert.Equal("main", body.Branch)
	assert.Equal("origin/main", body.Upstream)
	assert.True(body.Pushed)
	require.Len(body.Files, 1)
	assert.Equal("new.md", body.Files[0].Path)
}

func TestDocsGitPublishEndpointAcceptsLargeMessageBelowRouteLimit(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	repo := newDocsGitRepo(t, true)
	repo.write(t, "new.md", "# new\n")
	srv := setupDocsGitRouteServer(t, repo.dir)

	rr := doDocsJSON(t, srv, http.MethodPost, "/api/v1/docs/folders/f/git/publish", map[string]string{
		"message": "docs: " + strings.Repeat("large ", 2<<18),
	})

	require.Equal(http.StatusOK, rr.Code, rr.Body.String())
	var body docs.PublishResponse
	require.NoError(json.NewDecoder(rr.Body).Decode(&body))
	assert.NotEmpty(body.Commit)
	assert.Equal(body.Commit, strings.TrimSpace(runDocsGit(t, repo.remote, "rev-parse", "main")))
}

func TestDocsGitPublishEndpointPushesConfiguredUpstreamDespitePushDefaults(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	repo := newDocsGitRepo(t, true)
	backup := t.TempDir()
	runDocsGit(t, backup, "init", "--bare")
	runDocsGit(t, repo.dir, "remote", "add", "backup", backup)
	runDocsGit(t, repo.dir, "push", "backup", "main:main")
	backupInitial := strings.TrimSpace(runDocsGit(t, backup, "rev-parse", "main"))
	runDocsGit(t, repo.dir, "config", "remote.pushDefault", "backup")
	runDocsGit(t, repo.dir, "config", "push.default", "current")
	repo.write(t, "new.md", "# new\n")
	srv := setupDocsGitRouteServer(t, repo.dir)

	rr := doDocsJSON(t, srv, http.MethodPost, "/api/v1/docs/folders/f/git/publish", map[string]string{
		"message": "docs: explicit upstream",
	})

	require.Equal(http.StatusOK, rr.Code, rr.Body.String())
	var body docs.PublishResponse
	require.NoError(json.NewDecoder(rr.Body).Decode(&body))
	assert.Equal("origin/main", body.Upstream)
	assert.True(body.Pushed)
	assert.Equal(body.Commit, strings.TrimSpace(runDocsGit(t, repo.remote, "rev-parse", "main")))
	assert.Equal(backupInitial, strings.TrimSpace(runDocsGit(t, backup, "rev-parse", "main")))
}

func TestDocsGitPublishEndpointRejectsNonLoopback(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	repo := newDocsGitRepo(t, true)
	srv := setupDocsGitRouteServer(t, repo.dir)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/docs/folders/f/git/publish", strings.NewReader(`{"message":"docs: x"}`))
	req.RemoteAddr = "203.0.113.7:54321"
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	srv.ServeHTTP(rr, req)

	require.Equal(http.StatusForbidden, rr.Code, rr.Body.String())
	var problem ProblemError
	require.NoError(json.NewDecoder(rr.Body).Decode(&problem))
	assert.Equal(CodeForbidden, problem.Code)
	assert.Equal("loopbackOnly", problem.Details["reason"])
}

func TestDocsGitPublishEndpointRejectsNonJSONContentType(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	srv := setupDocsGitRouteServer(t, t.TempDir())
	req := httptest.NewRequest(http.MethodPost, "/api/v1/docs/folders/f/git/publish", strings.NewReader("docs: x"))
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("Content-Type", "text/plain")
	rr := httptest.NewRecorder()

	srv.ServeHTTP(rr, req)

	resp := rr.Result()
	defer resp.Body.Close()
	require.Equal(http.StatusUnsupportedMediaType, resp.StatusCode)
	assert.Equal("application/json", resp.Header.Get("Content-Type"))

	var body map[string]string
	require.NoError(json.NewDecoder(resp.Body).Decode(&body))
	assert.Contains(body["error"], "Content-Type must be application/json")
}

func TestDocsGitPublishEndpointErrors(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	repo := newDocsGitRepo(t, true)
	repo.write(t, "new.md", "# new\n")
	srv := setupDocsGitRouteServer(t, repo.dir)

	emptyRR := doDocsJSON(t, srv, http.MethodPost, "/api/v1/docs/folders/f/git/publish", map[string]string{
		"message": "   \n\t",
	})
	require.Equal(http.StatusBadRequest, emptyRR.Code, emptyRR.Body.String())
	var empty ProblemError
	require.NoError(json.NewDecoder(emptyRR.Body).Decode(&empty))
	assert.Equal("emptyMessage", empty.Details["reason"])

	missingRR := doDocsJSON(t, srv, http.MethodPost, "/api/v1/docs/folders/missing/git/publish", map[string]string{
		"message": "docs: x",
	})
	assert.Equal(http.StatusNotFound, missingRR.Code, missingRR.Body.String())
}

func TestDocsGitPublishEndpointNoUpstreamAndCommitFailure(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	noUpstream := newDocsGitRepo(t, false)
	noUpstream.write(t, "new.md", "# new\n")
	noUpstreamSrv := setupDocsGitRouteServer(t, noUpstream.dir)

	noUpstreamRR := doDocsJSON(t, noUpstreamSrv, http.MethodPost, "/api/v1/docs/folders/f/git/publish", map[string]string{
		"message": "docs: x",
	})
	require.Equal(http.StatusBadRequest, noUpstreamRR.Code, noUpstreamRR.Body.String())
	var noUpstreamProblem ProblemError
	require.NoError(json.NewDecoder(noUpstreamRR.Body).Decode(&noUpstreamProblem))
	assert.Equal("noUpstream", noUpstreamProblem.Details["reason"])
	assert.Contains(noUpstreamProblem.Detail, "git push -u origin main")

	// Force the commit failure with a pre-existing ref lock rather than a
	// command-bearing signer (which the publish safety gate would reject):
	// staging the markdown succeeds, then the commit cannot lock the branch
	// ref. This keeps coverage for git stderr passing through to the detail.
	repo := newDocsGitRepo(t, true)
	repo.write(t, "new.md", "# new\n")
	lockPath := filepath.Join(repo.dir, ".git", "refs", "heads", "main.lock")
	require.NoError(os.WriteFile(lockPath, nil, 0o644))
	commitFailSrv := setupDocsGitRouteServer(t, repo.dir)

	commitFailRR := doDocsJSON(t, commitFailSrv, http.MethodPost, "/api/v1/docs/folders/f/git/publish", map[string]string{
		"message": "docs: x",
	})
	require.Equal(http.StatusInternalServerError, commitFailRR.Code, commitFailRR.Body.String())
	var commitFailProblem ProblemError
	require.NoError(json.NewDecoder(commitFailRR.Body).Decode(&commitFailProblem))
	assert.Equal("commitFailed", commitFailProblem.Details["reason"])
	assert.Contains(strings.ToLower(commitFailProblem.Detail), "lock")
	assert.NotContains(commitFailProblem.Detail, "exit status")
}

func TestDocsGitPublishEndpointRejectsUnsafeGitConfig(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	repo := newDocsGitRepo(t, true)
	repo.write(t, "new.md", "# new\n")
	runDocsGit(t, repo.dir, "config", "filter.evil.clean", "/bin/sh -c evil")
	srv := setupDocsGitRouteServer(t, repo.dir)

	rr := doDocsJSON(t, srv, http.MethodPost, "/api/v1/docs/folders/f/git/publish", map[string]string{
		"message": "docs: x",
	})

	require.Equal(http.StatusBadRequest, rr.Code, rr.Body.String())
	var problem ProblemError
	require.NoError(json.NewDecoder(rr.Body).Decode(&problem))
	assert.Equal("unsafeGitConfig", problem.Details["reason"])
}

func TestDocsGitPublishEndpointIgnoresDocsRepoHooks(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	repo := newDocsGitRepo(t, true)
	repo.write(t, "new.md", "# new\n")
	marker := filepath.Join(t.TempDir(), "hook-ran")
	hookDir := filepath.Join(repo.dir, ".git", "hooks")
	require.NoError(os.MkdirAll(hookDir, 0o755))
	hook := "#!/bin/sh\necho hooked > \"" + marker + "\"\nexit 1\n"
	require.NoError(os.WriteFile(filepath.Join(hookDir, "pre-commit"), []byte(hook), 0o755))
	srv := setupDocsGitRouteServer(t, repo.dir)

	rr := doDocsJSON(t, srv, http.MethodPost, "/api/v1/docs/folders/f/git/publish", map[string]string{
		"message": "docs: x",
	})

	assert.Equal(http.StatusOK, rr.Code, rr.Body.String())
	assert.NoFileExists(marker, "docs repo hook executed during publish")
}

func TestDocsGitPublishEndpointProblemMappings(t *testing.T) {
	cases := []struct {
		name       string
		setup      func(t *testing.T) *Server
		wantStatus int
		wantReason string
	}{
		{
			name: "no markdown changes",
			setup: func(t *testing.T) *Server {
				repo := newDocsGitRepo(t, true)
				repo.write(t, "code.go", "package x\n")
				return setupDocsGitRouteServer(t, repo.dir)
			},
			wantStatus: http.StatusBadRequest,
			wantReason: "noMarkdownChanges",
		},
		{
			name: "not a git repo",
			setup: func(t *testing.T) *Server {
				return setupDocsGitRouteServer(t, t.TempDir())
			},
			wantStatus: http.StatusBadRequest,
			wantReason: "notGitRepo",
		},
		{
			name: "index not clean",
			setup: func(t *testing.T) *Server {
				repo := newDocsGitRepo(t, true)
				repo.write(t, "new.md", "# new\n")
				repo.write(t, "code.go", "package x\n")
				runDocsGit(t, repo.dir, "add", "code.go")
				return setupDocsGitRouteServer(t, repo.dir)
			},
			wantStatus: http.StatusConflict,
			wantReason: "indexNotClean",
		},
		{
			name: "conflict",
			setup: func(t *testing.T) *Server {
				repo := newDocsGitRepo(t, true)
				runDocsGit(t, repo.dir, "checkout", "-b", "side")
				repo.write(t, "seed.md", "side version\n")
				runDocsGit(t, repo.dir, "commit", "-am", "side")
				runDocsGit(t, repo.dir, "checkout", "main")
				repo.write(t, "seed.md", "main version\n")
				runDocsGit(t, repo.dir, "commit", "-am", "main")
				cmd := procutil.Command("git", "merge", "side")
				cmd.Dir = repo.dir
				cmd.Env = cleanDocsGitEnv(os.Environ())
				out, mergeErr := cmd.CombinedOutput()
				require.Error(t, mergeErr, "expected merge conflict, got clean merge: %s", out)
				return setupDocsGitRouteServer(t, repo.dir)
			},
			wantStatus: http.StatusConflict,
			wantReason: "conflict",
		},
		{
			name: "push target inside docs folder",
			setup: func(t *testing.T) *Server {
				repo := newDocsGitRepo(t, true)
				repo.write(t, "new.md", "# new\n")
				runDocsGit(t, repo.dir, "init", "--bare", "evil.git")
				runDocsGit(t, repo.dir, "remote", "set-url", "origin", "./evil.git")
				return setupDocsGitRouteServer(t, repo.dir)
			},
			wantStatus: http.StatusBadRequest,
			wantReason: "unsafeGitConfig",
		},
		{
			name: "push failed after commit",
			setup: func(t *testing.T) *Server {
				repo := newDocsGitRepo(t, true)
				repo.write(t, "new.md", "# new\n")
				runDocsGit(t, repo.dir, "remote", "set-url", "origin", filepath.Join(t.TempDir(), "missing-origin"))
				return setupDocsGitRouteServer(t, repo.dir)
			},
			wantStatus: http.StatusBadGateway,
			wantReason: "pushFailedAfterCommit",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert := Assert.New(t)
			require := require.New(t)
			srv := tc.setup(t)

			rr := doDocsJSON(t, srv, http.MethodPost, "/api/v1/docs/folders/f/git/publish", map[string]string{
				"message": "docs: x",
			})

			require.Equal(tc.wantStatus, rr.Code, rr.Body.String())
			var problem ProblemError
			require.NoError(json.NewDecoder(rr.Body).Decode(&problem))
			assert.Equal(tc.wantReason, problem.Details["reason"])
			if tc.wantReason == "pushFailedAfterCommit" {
				assert.NotEmpty(problem.Details["commit"])
				assert.NotContains(problem.Detail, "exit status")
			}
		})
	}
}

func TestDocsPublishLockSetReleasesAndScopesPerFolder(t *testing.T) {
	assert := Assert.New(t)
	locks := newDocsPublishLockSet()

	assert.True(locks.tryAcquire("a"))
	assert.False(locks.tryAcquire("a"))
	assert.True(locks.tryAcquire("b"))
	locks.release("a")
	assert.True(locks.tryAcquire("a"))
	locks.release("a")
	locks.release("b")
}

func TestDocsGitPublishEndpointLockHeldReturnsConflict(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	repo := newDocsGitRepo(t, true)
	repo.write(t, "new.md", "# new\n")
	srv := setupDocsGitRouteServer(t, repo.dir)
	require.True(srv.docsPublishLocks.tryAcquire("f"))
	defer srv.docsPublishLocks.release("f")

	rr := doDocsJSON(t, srv, http.MethodPost, "/api/v1/docs/folders/f/git/publish", map[string]string{
		"message": "docs: x",
	})

	require.Equal(http.StatusConflict, rr.Code, rr.Body.String())
	var problem ProblemError
	require.NoError(json.NewDecoder(rr.Body).Decode(&problem))
	assert.Equal(CodeConflict, problem.Code)
	assert.Equal("publishInProgress", problem.Details["reason"])
}

func TestDocsGitPublishEndpointRejectsConcurrentInFlightPublish(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	repo := newDocsGitRepo(t, true)
	repo.write(t, "blocked.md", "# blocked\n")
	srv := setupDocsGitRouteServer(t, repo.dir)

	// The publish safety gate forbids command-bearing config, so hold the
	// publish in-flight by hanging its push: point origin at an HTTP server
	// that blocks the initial ref advertisement until released. The commit
	// succeeds first, then push parks here while the single-flight lock is
	// held, letting a concurrent request observe the 409.
	gotPush := make(chan struct{})
	release := make(chan struct{})
	var gotPushOnce, releaseOnce sync.Once
	doRelease := func() { releaseOnce.Do(func() { close(release) }) }
	hung := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/info/refs") {
			gotPushOnce.Do(func() { close(gotPush) })
			<-release
		}
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer hung.Close()
	defer doRelease()
	runDocsGit(t, repo.dir, "remote", "set-url", "origin", hung.URL+"/repo.git")

	publishAsync := func(message string) <-chan *httptest.ResponseRecorder {
		done := make(chan *httptest.ResponseRecorder, 1)
		go func() {
			req := httptest.NewRequest(
				http.MethodPost,
				"/api/v1/docs/folders/f/git/publish",
				strings.NewReader(`{"message":`+strconv.Quote(message)+`}`),
			)
			req.RemoteAddr = "127.0.0.1:12345"
			req.Header.Set("Content-Type", "application/json")
			rr := httptest.NewRecorder()
			srv.ServeHTTP(rr, req)
			done <- rr
		}()
		return done
	}

	firstDone := publishAsync("docs: blocked")
	firstConsumed := false
	defer func() {
		doRelease()
		if !firstConsumed {
			select {
			case <-firstDone:
			case <-time.After(5 * time.Second):
				require.Fail("timed out waiting for first publish cleanup")
			}
		}
	}()

	select {
	case <-gotPush:
	case <-time.After(5 * time.Second):
		require.FailNow("timed out waiting for first publish to reach push")
	}

	secondDone := publishAsync("docs: concurrent")
	var conflictRR *httptest.ResponseRecorder
	select {
	case conflictRR = <-secondDone:
	case <-time.After(2 * time.Second):
		require.FailNow("timed out waiting for concurrent publish conflict")
	}

	require.Equal(http.StatusConflict, conflictRR.Code, conflictRR.Body.String())
	var problem ProblemError
	require.NoError(json.NewDecoder(conflictRR.Body).Decode(&problem))
	assert.Equal(CodeConflict, problem.Code)
	assert.Equal("publishInProgress", problem.Details["reason"])

	// Release the hung push; the first publish commits but fails to push.
	doRelease()
	var firstRR *httptest.ResponseRecorder
	select {
	case firstRR = <-firstDone:
		firstConsumed = true
	case <-time.After(5 * time.Second):
		require.Fail("timed out waiting for first publish")
	}
	require.Equal(http.StatusBadGateway, firstRR.Code, firstRR.Body.String())
	var firstProblem ProblemError
	require.NoError(json.NewDecoder(firstRR.Body).Decode(&firstProblem))
	assert.Equal("pushFailedAfterCommit", firstProblem.Details["reason"])

	// With the lock released and a working remote, a fresh publish succeeds.
	runDocsGit(t, repo.dir, "remote", "set-url", "origin", repo.remote)
	repo.write(t, "after.md", "# after\n")
	afterRR := doDocsJSON(t, srv, http.MethodPost, "/api/v1/docs/folders/f/git/publish", map[string]string{
		"message": "docs: after",
	})
	assert.Equal(http.StatusOK, afterRR.Code, afterRR.Body.String())
}
