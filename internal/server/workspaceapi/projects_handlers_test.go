package workspaceapi

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	gitcmd "go.kenn.io/kit/git/cmd"
	gitenv "go.kenn.io/kit/git/env"
	managedworktree "go.kenn.io/kit/git/managed"

	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/procutil"
	"go.kenn.io/forge/internal/providerplane"
	"go.kenn.io/forge/internal/server/httpapi"
	"go.kenn.io/forge/internal/testutil/dbtest"
)

func TestRegisterProjectUsesHubRepositoryIdentity(t *testing.T) {
	require := require.New(t)
	database := dbtest.Open(t)
	observedAt := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	handler := New(Deps{
		DB: database,
		ResolveProjectRepository: func(
			ctx context.Context, route providerplane.RepositoryRoute,
		) (*db.Repo, error) {
			entry, _, err := database.ReconcileRepositoryObservation(ctx, db.RepoIdentity{
				Platform: route.Provider, PlatformHost: route.PlatformHost,
				PlatformRepoID: "provider-repository-1",
				Owner:          route.Owner, Name: route.Name,
			}, observedAt)
			if err != nil {
				return nil, err
			}
			return &entry.Repository, nil
		},
	})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		require.NoError(handler.Shutdown(ctx))
	})
	created, err := handler.registerProjectAtPath(
		t.Context(), t.TempDir(), "Widget",
		&platformIdentityPayload{
			Platform: "github", PlatformHost: "github.com",
			Owner: "acme", Name: "widget",
		},
		"main",
	)
	require.NoError(err)
	var platformRepoID string
	require.NoError(database.ReadDB().QueryRowContext(t.Context(), `
		SELECT r.platform_repo_id
		FROM forge_projects p
		JOIN forge_repos r ON r.id = p.repo_id
		WHERE p.id = ?`, created.ID).Scan(&platformRepoID))
	require.Equal("provider-repository-1", platformRepoID)
}

func TestWorktreeLifecycleProblemMapsExistingBranch(t *testing.T) {
	err := worktreeLifecycleProblem(
		managedworktree.ErrBranchAlreadyExists, "body.setup_script",
	)

	problem, ok := err.(*httpapi.ProblemError)
	require.True(t, ok, "want *ProblemError, got %T", err)
	assert.Equal(t, http.StatusConflict, problem.Status)
	assert.Equal(t, httpapi.CodeBranchConflict, problem.Code)
}

func TestManagedWorktreeExecutionUsesSharedProcessLimiter(t *testing.T) {
	require := require.New(t)
	restore := procutil.SetDefaultLimiterForTest(
		procutil.NewLimiterWithAcquireTimeout(1, time.Millisecond),
	)
	t.Cleanup(restore)
	release, err := procutil.TryAcquire(context.Background(), "hold test slot")
	require.NoError(err)
	t.Cleanup(release)

	_, err = runManagedWorktreeGit(
		context.Background(), gitcmd.Runner{Env: os.Environ()}, t.TempDir(), "status",
	)
	require.ErrorIs(err, procutil.ErrProcessLimitReached)

	err = runManagedWorktreeHook(context.Background(), managedworktree.HookCommand{
		Script: "/bin/true", Dir: t.TempDir(), Env: os.Environ(),
	})
	require.ErrorIs(err, procutil.ErrProcessLimitReached)

	_, err = managedWorktreeIsDirty(context.Background(), t.TempDir())
	require.ErrorIs(err, procutil.ErrProcessLimitReached)
}

func TestCreateProjectWorktreeFromMergeRequestUsesHubFacts(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	gitConfig := filepath.Join(t.TempDir(), "gitconfig")
	require.NoError(os.WriteFile(gitConfig, nil, 0o600))
	t.Setenv("GIT_CONFIG_GLOBAL", gitConfig)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	runGit := func(dir string, args ...string) string {
		t.Helper()
		command := procutil.Command("git", args...)
		command.Dir = dir
		command.Env = append(
			gitenv.StripAll(os.Environ()),
			"GIT_CONFIG_GLOBAL="+gitConfig, "GIT_CONFIG_NOSYSTEM=1",
		)
		output, err := command.CombinedOutput()
		require.NoError(err, "git %v: %s", args, output)
		return strings.TrimSpace(string(output))
	}
	origin := filepath.Join(t.TempDir(), "origin")
	require.NoError(os.MkdirAll(origin, 0o755))
	runGit(origin, "init", "-q", "-b", "main")
	runGit(origin, "config", "user.email", "test@example.com")
	runGit(origin, "config", "user.name", "Test User")
	runGit(origin, "config", "commit.gpgsign", "false")
	runGit(origin, "commit", "--allow-empty", "-m", "initial")
	runGit(origin, "checkout", "-q", "-b", "feature/seven")
	runGit(origin, "commit", "--allow-empty", "-m", "pull request head")
	headSHA := runGit(origin, "rev-parse", "HEAD")
	runGit(origin, "update-ref", "refs/pull/7/head", headSHA)
	runGit(origin, "checkout", "-q", "main")
	projectRoot := filepath.Join(t.TempDir(), "project")
	runGit(filepath.Dir(projectRoot), "clone", "-q", origin, projectRoot)

	database := dbtest.Open(t)
	repoID, err := database.UpsertRepo(t.Context(), db.RepoIdentity{
		Platform: "github", PlatformHost: "github.com",
		PlatformRepoID: "repo-acme-widget", Owner: "acme", Name: "widget",
	})
	require.NoError(err)
	project, err := database.CreateProject(t.Context(), db.CreateProjectInput{
		DisplayName: "Widget", LocalPath: projectRoot,
		RepoID: sql.NullInt64{Int64: repoID, Valid: true}, DefaultBranch: "main",
	})
	require.NoError(err)
	facts := MergeRequestWorktreeFacts{
		Number: 7, URL: "https://github.com/acme/widget/pull/7",
		State: "open", Title: "Federated worktree", HeadBranch: "feature/seven",
		HeadRepoCloneURL: origin, ExpectedHeadSHA: headSHA,
	}
	resolver := stubLaunchSpecResolver{mergeRequestFacts: &facts}
	handler := New(Deps{
		DB:                         database,
		Resolver:                   httpapi.NewRepositoryResolver(httpapi.RepositoryResolverDeps{DB: database}),
		MergeRequestWorktreeSource: resolver,
		EnrichmentDisabled:         true,
	})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		require.NoError(handler.Shutdown(ctx))
	})
	mux := http.NewServeMux()
	api := humago.NewWithPrefix(mux, "/api/v1", huma.DefaultConfig("workspace test", "1"))
	handler.Register(api)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	destination := filepath.Join(t.TempDir(), "worktree")
	body, err := json.Marshal(map[string]any{
		"number": 7, "branch": "review/pr-7", "path": destination,
	})
	require.NoError(err)
	response, err := server.Client().Post(
		server.URL+"/api/v1/projects/"+project.ID+"/worktrees/from-merge-request",
		"application/json", bytes.NewReader(body),
	)
	require.NoError(err)
	defer response.Body.Close()

	responseBody, err := io.ReadAll(response.Body)
	require.NoError(err)
	require.Equal(http.StatusCreated, response.StatusCode, string(responseBody))
	assert.Equal(headSHA, runGit(destination, "rev-parse", "HEAD"))
	localMR, err := database.GetMergeRequestByRepoIDAndNumber(t.Context(), repoID, 7)
	require.NoError(err)
	assert.Nil(localMR)
}
