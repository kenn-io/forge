package server

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	Assert "github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go/modules/compose"
	"github.com/testcontainers/testcontainers-go/wait"
	"go.kenn.io/middleman/internal/db"
	ghclient "go.kenn.io/middleman/internal/github"
	"go.kenn.io/middleman/internal/platform"
	platformforgejo "go.kenn.io/middleman/internal/platform/forgejo"
	platformgitea "go.kenn.io/middleman/internal/platform/gitea"
	"go.kenn.io/middleman/internal/procutil"
	"go.kenn.io/middleman/internal/testutil/dbtest"
)

type giteaLikeContainerClient interface {
	platform.Provider
	platform.RepositoryReader
	platform.MergeRequestReader
	platform.IssueReader
	platform.ReleaseReader
	platform.TagReader
	platform.CIReader
}

type giteaLikeFixtureConfig struct {
	Kind        platform.Kind
	Service     string
	ScriptDir   string
	StackID     compose.StackIdentifier
	Image       string
	HTTPPort    string
	KeepEnv     string
	EnvPrefix   string
	TitlePrefix string
}

type giteaLikeContainerManifest struct {
	BaseURL            string `json:"base_url"`
	APIURL             string `json:"api_url"`
	Host               string `json:"host"`
	Token              string `json:"token"`
	Owner              string `json:"owner"`
	Name               string `json:"name"`
	RepoPath           string `json:"repo_path"`
	WebURL             string `json:"web_url"`
	CloneURL           string `json:"clone_url"`
	DefaultBranch      string `json:"default_branch"`
	RepositoryID       int64  `json:"repository_id"`
	RepositoryIDString string `json:"repository_id_string"`
	PullRequestIndex   int    `json:"pull_request_index"`
	IssueIndex         int    `json:"issue_index"`
	Label              string `json:"label"`
	ReleaseTag         string `json:"release_tag"`
	StatusContext      string `json:"status_context"`
}

func TestForgejoContainerSync(t *testing.T) {
	if os.Getenv("MIDDLEMAN_FORGEJO_CONTAINER_TESTS") != "1" {
		t.Skip("set MIDDLEMAN_FORGEJO_CONTAINER_TESTS=1 to run Forgejo container e2e")
	}

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Minute)
	defer cancel()
	manifest := runGiteaLikeContainerFixture(t, ctx, giteaLikeFixtureConfig{
		Kind:        platform.KindForgejo,
		Service:     "forgejo",
		ScriptDir:   "forgejo",
		StackID:     compose.StackIdentifier(envOrDefault("MIDDLEMAN_FORGEJO_COMPOSE_PROJECT", "middleman-forgejo-e2e")),
		Image:       envOrDefault("MIDDLEMAN_FORGEJO_IMAGE", "codeberg.org/forgejo/forgejo:12"),
		HTTPPort:    envOrDefault("FORGEJO_HTTP_PORT", freeLoopbackPort(t)),
		KeepEnv:     "MIDDLEMAN_KEEP_FORGEJO_FIXTURE",
		EnvPrefix:   "FORGEJO",
		TitlePrefix: "Forgejo",
	})

	client, err := platformforgejo.NewClient(
		manifest.Host,
		testTokenSource(manifest.Token),
		platformforgejo.WithBaseURLForTesting(manifest.BaseURL),
		platformforgejo.WithForegroundTimeoutForTesting(time.Minute),
	)
	require.NoError(t, err)
	assertGiteaLikeContainerSync(t, ctx, platform.KindForgejo, manifest, client)
}

func TestGiteaContainerSync(t *testing.T) {
	if os.Getenv("MIDDLEMAN_GITEA_CONTAINER_TESTS") != "1" {
		t.Skip("set MIDDLEMAN_GITEA_CONTAINER_TESTS=1 to run Gitea container e2e")
	}

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Minute)
	defer cancel()
	manifest := runGiteaLikeContainerFixture(t, ctx, giteaLikeFixtureConfig{
		Kind:        platform.KindGitea,
		Service:     "gitea",
		ScriptDir:   "gitea",
		StackID:     compose.StackIdentifier(envOrDefault("MIDDLEMAN_GITEA_COMPOSE_PROJECT", "middleman-gitea-e2e")),
		Image:       envOrDefault("MIDDLEMAN_GITEA_IMAGE", "gitea/gitea:1.24.6"),
		HTTPPort:    envOrDefault("GITEA_HTTP_PORT", freeLoopbackPort(t)),
		KeepEnv:     "MIDDLEMAN_KEEP_GITEA_FIXTURE",
		EnvPrefix:   "GITEA",
		TitlePrefix: "Gitea",
	})

	client, err := platformgitea.NewClient(
		manifest.Host,
		testTokenSource(manifest.Token),
		platformgitea.WithBaseURLForTesting(manifest.BaseURL),
		platformgitea.WithForegroundTimeoutForTesting(time.Minute),
	)
	require.NoError(t, err)
	assertGiteaLikeContainerSync(t, ctx, platform.KindGitea, manifest, client)
}

func assertGiteaLikeContainerSync(
	t *testing.T,
	ctx context.Context,
	kind platform.Kind,
	manifest giteaLikeContainerManifest,
	client giteaLikeContainerClient,
) {
	t.Helper()
	assert := Assert.New(t)
	require := require.New(t)

	registry, err := platform.NewRegistry(client)
	require.NoError(err)
	database := dbtest.Open(t)
	repo := ghclient.RepoRef{
		Platform:           kind,
		PlatformHost:       manifest.Host,
		Owner:              manifest.Owner,
		Name:               manifest.Name,
		RepoPath:           manifest.RepoPath,
		PlatformRepoID:     manifest.RepositoryID,
		PlatformExternalID: manifest.RepositoryIDString,
		WebURL:             manifest.WebURL,
		CloneURL:           manifest.CloneURL,
		DefaultBranch:      manifest.DefaultBranch,
	}
	syncer := ghclient.NewSyncerWithRegistry(
		registry, database, nil, []ghclient.RepoRef{repo}, time.Minute, nil, nil,
	)
	t.Cleanup(syncer.Stop)

	syncer.RunOnce(ctx)
	require.NoError(syncer.SyncMROnProvider(ctx, kind, manifest.Host, manifest.Owner, manifest.Name, manifest.PullRequestIndex))
	require.NoError(syncer.SyncIssueOnProvider(ctx, kind, manifest.Host, manifest.Owner, manifest.Name, manifest.IssueIndex))

	repoRow, err := database.GetRepoByIdentity(ctx, db.RepoIdentity{
		Platform:       string(kind),
		PlatformHost:   manifest.Host,
		PlatformRepoID: manifest.RepositoryIDString,
		Owner:          manifest.Owner,
		Name:           manifest.Name,
		RepoPath:       manifest.RepoPath,
	})
	require.NoError(err)
	require.NotNil(repoRow)
	assert.Equal(manifest.RepoPath, repoRow.RepoPath)

	mr, err := database.GetMergeRequestByRepoIDAndNumber(ctx, repoRow.ID, manifest.PullRequestIndex)
	require.NoError(err)
	require.NotNil(mr)
	assert.Equal(string(kind)+" container PR", mr.Title)
	assert.Equal("success", mr.CIStatus)
	assert.Contains(mr.CIChecksJSON, manifest.StatusContext)
	require.NotEmpty(mr.Labels)
	assert.Equal(manifest.Label, mr.Labels[0].Name)
	mrEvents, err := database.ListMREvents(ctx, mr.ID)
	require.NoError(err)
	assert.NotEmpty(mrEvents)

	issue, err := database.GetIssueByRepoIDAndNumber(ctx, repoRow.ID, manifest.IssueIndex)
	require.NoError(err)
	require.NotNil(issue)
	assert.Equal(string(kind)+" container issue", issue.Title)
	issueEvents, err := database.ListIssueEvents(ctx, issue.ID)
	require.NoError(err)
	assert.NotEmpty(issueEvents)

	summaries, err := database.ListRepoSummaries(ctx)
	require.NoError(err)
	require.Len(summaries, 1)
	require.NotNil(summaries[0].Overview.LatestRelease)
	assert.Equal(manifest.ReleaseTag, summaries[0].Overview.LatestRelease.TagName)
	assert.NotEmpty(summaries[0].Overview.Releases)

	// Live validation of the pinned-merge rejection contract:
	// isHeadMismatchConflict matches the provider's "head target does
	// not match" message text, so probe the real API with a bogus pin
	// and assert the rejection still classifies as stale_state. The
	// probe must not merge the fixture PR.
	merger, ok := client.(platform.MergeMutator)
	require.True(ok, "container client must support merge mutations")
	ref := platform.RepoRef{Owner: manifest.Owner, Name: manifest.Name}
	_, mergeErr := merger.MergeMergeRequest(
		ctx, ref, manifest.PullRequestIndex,
		"stale pin probe", "must not merge", "merge",
		"0000000000000000000000000000000000000000",
	)
	require.Error(mergeErr, "a merge pinned to a bogus head must be rejected")
	require.ErrorIs(mergeErr, platform.ErrStaleState,
		"the live head-mismatch rejection must classify as stale_state")
	require.NoError(syncer.SyncMROnProvider(ctx, kind, manifest.Host, manifest.Owner, manifest.Name, manifest.PullRequestIndex))
	mrAfterProbe, err := database.GetMergeRequestByRepoIDAndNumber(ctx, repoRow.ID, manifest.PullRequestIndex)
	require.NoError(err)
	require.NotNil(mrAfterProbe)
	assert.Equal(db.MergeRequestStateOpen, mrAfterProbe.State,
		"the rejected probe must leave the fixture PR open")
}

func runGiteaLikeContainerFixture(
	t *testing.T,
	ctx context.Context,
	cfg giteaLikeFixtureConfig,
) giteaLikeContainerManifest {
	t.Helper()
	assert := Assert.New(t)
	require := require.New(t)

	stack, err := compose.NewDockerComposeWith(
		compose.WithStackFiles(filepath.Join(repoRoot(t), "scripts/e2e", cfg.ScriptDir, "docker-compose.yml")),
		cfg.StackID,
	)
	require.NoError(err)
	composeStack := stack.
		WithEnv(map[string]string{
			"MIDDLEMAN_" + cfg.EnvPrefix + "_IMAGE": cfg.Image,
			cfg.EnvPrefix + "_HTTP_PORT":            cfg.HTTPPort,
		}).
		WaitForService(cfg.Service, waitForGiteaLikeHTTP())
	err = composeStack.Up(ctx, compose.Wait(true))
	container, containerErr := composeStack.ServiceContainer(ctx, cfg.Service)
	if err != nil {
		if containerErr == nil {
			require.NoError(err, containerLogs(ctx, container))
		}
		require.NoError(err)
	}
	require.NoError(containerErr)
	if os.Getenv(cfg.KeepEnv) == "1" {
		t.Logf("keeping %s Compose stack %s at http://127.0.0.1:%s", cfg.TitlePrefix, cfg.StackID, cfg.HTTPPort)
	} else {
		t.Cleanup(func() {
			assert.NoError(composeStack.Down(
				context.Background(),
				compose.RemoveOrphans(true),
				compose.RemoveVolumes(true),
			))
		})
	}

	baseURL, err := container.PortEndpoint(ctx, "3000/tcp", "http")
	require.NoError(err)

	manifestPath := filepath.Join(t.TempDir(), cfg.ScriptDir+"-manifest.json")
	cmd := procutil.CommandContext(
		ctx,
		filepath.Join(repoRoot(t), "scripts/e2e", cfg.ScriptDir, "bootstrap.sh"),
		manifestPath,
	)
	cmd.Env = append(os.Environ(),
		cfg.EnvPrefix+"_URL="+baseURL,
		cfg.EnvPrefix+"_CONTAINER_ID="+container.GetContainerID(),
		cfg.EnvPrefix+"_TITLE_PREFIX="+cfg.TitlePrefix,
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		require.NoError(err, string(output)+"\n"+containerLogs(ctx, container))
	}

	return readGiteaLikeManifest(t, manifestPath)
}

func waitForGiteaLikeHTTP() *wait.HTTPStrategy {
	return wait.ForHTTP("/api/v1/version").
		WithPort("3000/tcp").
		WithStartupTimeout(5 * time.Minute).
		WithStatusCodeMatcher(func(status int) bool {
			return status == http.StatusOK
		})
}

func readGiteaLikeManifest(t *testing.T, manifestPath string) giteaLikeContainerManifest {
	t.Helper()
	manifestFile, err := os.Open(manifestPath)
	require.NoError(t, err)
	defer manifestFile.Close()
	var manifest giteaLikeContainerManifest
	require.NoError(t, json.NewDecoder(manifestFile).Decode(&manifest))
	require.NotEmpty(t, manifest.BaseURL)
	require.NotEmpty(t, manifest.APIURL)
	return manifest
}
