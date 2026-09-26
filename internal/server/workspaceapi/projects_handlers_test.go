package workspaceapi

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
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
	"go.kenn.io/forge/internal/testutil/reposeed"
)

func TestRegisterProjectUsesHubRepositoryIdentity(t *testing.T) {
	require := require.New(t)
	database := dbtest.Open(t)
	handler := New(Deps{
		DB: database,
		ResolveRepository: func(
			ctx context.Context, route providerplane.RepositoryRoute,
		) (*db.Repo, error) {
			entry, err := database.ObserveRepository(ctx, db.RepoIdentity{
				Platform: route.Provider, PlatformHost: route.PlatformHost,
				PlatformRepoID: 1001,
				Owner:          route.Owner, Name: route.Name,
			})
			if err != nil {
				return nil, err
			}
			return &entry.Repository, nil
		},
	})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), time.Second)
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
	var platformRepoID int64
	require.NoError(database.ReadDB().QueryRowContext(t.Context(), `
		SELECT r.platform_repo_id
		FROM forge_projects p
		JOIN forge_repos r ON r.id = p.repo_id
		WHERE p.id = ?`, created.ID).Scan(&platformRepoID))
	require.Equal(int64(1001), platformRepoID)
}

func TestRegisterProjectWithoutHubLinksOnlyTrackedRepository(t *testing.T) {
	tests := []struct {
		name       string
		trackRoute bool
	}{
		{name: "tracked route links repository", trackRoute: true},
		{name: "untracked route stays unlinked"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert := assert.New(t)
			require := require.New(t)
			database := dbtest.Open(t)
			var trackedRepoID int64
			if tt.trackRoute {
				var err error
				trackedRepoID, err = reposeed.Seed(t.Context(), database, db.RepoIdentity{
					Platform: "github", PlatformHost: "github.com",
					PlatformRepoID: 1001, Owner: "acme", Name: "widget",
				})
				require.NoError(err)
			}
			handler := New(Deps{DB: database})
			t.Cleanup(func() {
				ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), time.Second)
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

			var repoID sql.NullInt64
			require.NoError(database.ReadDB().QueryRowContext(t.Context(),
				`SELECT repo_id FROM forge_projects WHERE id = ?`, created.ID,
			).Scan(&repoID))
			repos, err := database.ListRepos(t.Context())
			require.NoError(err)
			if tt.trackRoute {
				assert.Equal(sql.NullInt64{Int64: trackedRepoID, Valid: true}, repoID)
				assert.Len(repos, 1)
				return
			}
			assert.False(repoID.Valid)
			assert.Empty(repos, "registration must not create a repository row")
		})
	}
}

func TestWorktreeLifecycleProblemMapsExistingBranch(t *testing.T) {
	err := worktreeLifecycleProblem(
		managedworktree.ErrBranchAlreadyExists, "body.setup_script",
	)

	problem, ok := errors.AsType[*httpapi.ProblemError](err)
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
	repoID, err := reposeed.Seed(t.Context(), database, db.RepoIdentity{
		Platform: "github", PlatformHost: "github.com",
		Owner: "acme", Name: "widget",
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
		ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), time.Second)
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
