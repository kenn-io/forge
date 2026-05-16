package e2etest

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	gh "github.com/google/go-github/v84/github"
	"github.com/stretchr/testify/require"
	"go.kenn.io/middleman/internal/config"
	"go.kenn.io/middleman/internal/db"
	ghclient "go.kenn.io/middleman/internal/github"
	"go.kenn.io/middleman/internal/server"
	"go.kenn.io/middleman/internal/testutil/dbtest"
)

type localHealthResponse struct {
	Status string `json:"status"`
}

func setupTestServer(t *testing.T) (*server.Server, *db.DB) {
	t.Helper()
	database := dbtest.Open(t)

	syncer := ghclient.NewSyncer(nil, database, nil, nil, time.Minute, nil, nil)
	t.Cleanup(syncer.Stop)

	srv := server.New(database, syncer, nil, "/", nil, server.ServerOptions{})
	return srv, database
}

// setupTestServerWithSSEBufferSize boots a server whose SSE replay
// buffer holds the configured number of recent events. Used to exercise
// the stale-cursor path with a small, deterministic ring.
func setupTestServerWithSSEBufferSize(
	t *testing.T, size int,
) (*server.Server, *db.DB, *config.Config) {
	t.Helper()
	database := dbtest.Open(t)

	syncer := ghclient.NewSyncer(nil, database, nil, nil, time.Minute, nil, nil)
	t.Cleanup(syncer.Stop)

	cfg := &config.Config{
		Host:          "127.0.0.1",
		Port:          8091,
		BasePath:      "/",
		SSEBufferSize: size,
	}
	srv := server.New(database, syncer, nil, "/", cfg, server.ServerOptions{})
	return srv, database, cfg
}

func setupWithBasePath(t *testing.T, basePath string, _ any) *server.Server {
	t.Helper()
	database := dbtest.Open(t)

	syncer := ghclient.NewSyncer(
		map[string]ghclient.Client{"github.com": &mockGH{}},
		database, nil, nil, time.Minute, nil, nil,
	)
	t.Cleanup(syncer.Stop)
	return server.New(database, syncer, nil, basePath, nil, server.ServerOptions{})
}

func setupTestServerWithConfig(t *testing.T) (*server.Server, *db.DB, string) {
	return setupTestServerWithConfigContent(t, `
sync_interval = "5m"
github_token_env = "MIDDLEMAN_GITHUB_TOKEN"
host = "127.0.0.1"
port = 8091

[[repos]]
owner = "acme"
name = "widget"
`, &mockGH{})
}

func setupTestServerWithConfigContent(
	t *testing.T,
	cfgContent string,
	mock *mockGH,
) (*server.Server, *db.DB, string) {
	srv, database, cfgPath, _ := setupTestServerWithConfigContentAndSyncer(
		t, cfgContent, mock,
	)
	return srv, database, cfgPath
}

func setupTestServerWithConfigContentAndSyncer(
	t *testing.T,
	cfgContent string,
	mock *mockGH,
) (*server.Server, *db.DB, string, *ghclient.Syncer) {
	t.Helper()

	dir := t.TempDir()
	database := dbtest.Open(t)

	cfgPath := filepath.Join(dir, "config.toml")
	require.NoError(t, os.WriteFile(cfgPath, []byte(cfgContent), 0o644))

	cfg, err := config.Load(cfgPath)
	require.NoError(t, err)

	clients := map[string]ghclient.Client{"github.com": mock}
	resolved := ghclient.ResolveConfiguredRepos(t.Context(), clients, cfg.Repos)
	syncer := ghclient.NewSyncer(
		clients, database, nil, resolved.Expanded, time.Minute, nil, nil,
	)
	t.Cleanup(syncer.Stop)

	srv := server.NewWithConfig(
		database, syncer, nil, nil, cfg, cfgPath, server.ServerOptions{},
	)
	return srv, database, cfgPath, syncer
}

func gracefulShutdown(t *testing.T, srv *server.Server) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, srv.Shutdown(ctx))
}

type mockGH struct {
	getRepositoryFn        func(context.Context, string, string) (*gh.Repository, error)
	listOpenPullRequestsFn func(context.Context, string, string) ([]*gh.PullRequest, error)
	listReposByOwnerFn     func(context.Context, string) ([]*gh.Repository, error)
}

func (m *mockGH) ListOpenPullRequests(ctx context.Context, owner, repo string) ([]*gh.PullRequest, error) {
	if m.listOpenPullRequestsFn != nil {
		return m.listOpenPullRequestsFn(ctx, owner, repo)
	}
	return nil, nil
}

func (m *mockGH) ListRepositoriesByOwner(ctx context.Context, owner string) ([]*gh.Repository, error) {
	if m.listReposByOwnerFn != nil {
		return m.listReposByOwnerFn(ctx, owner)
	}
	return nil, nil
}

func (m *mockGH) GetPullRequest(context.Context, string, string, int) (*gh.PullRequest, error) {
	return nil, nil
}
func (m *mockGH) GetUser(context.Context, string) (*gh.User, error) { return nil, nil }
func (m *mockGH) ListReleases(context.Context, string, string, int) ([]*gh.RepositoryRelease, error) {
	return nil, nil
}
func (m *mockGH) ListTags(context.Context, string, string, int) ([]*gh.RepositoryTag, error) {
	return nil, nil
}
func (m *mockGH) ListOpenIssues(context.Context, string, string) ([]*gh.Issue, error) {
	return nil, nil
}
func (m *mockGH) GetIssue(context.Context, string, string, int) (*gh.Issue, error) {
	return nil, nil
}
func (m *mockGH) CreateIssue(context.Context, string, string, string, string) (*gh.Issue, error) {
	return nil, nil
}
func (m *mockGH) ListIssueComments(context.Context, string, string, int) ([]*gh.IssueComment, error) {
	return nil, nil
}
func (m *mockGH) ListIssueCommentsIfChanged(context.Context, string, string, int) ([]*gh.IssueComment, error) {
	return nil, nil
}
func (m *mockGH) ListReviews(context.Context, string, string, int) ([]*gh.PullRequestReview, error) {
	return nil, nil
}
func (m *mockGH) ListCommits(context.Context, string, string, int) ([]*gh.RepositoryCommit, error) {
	return nil, nil
}
func (m *mockGH) ListPullRequestTimelineEvents(context.Context, string, string, int) ([]ghclient.PullRequestTimelineEvent, error) {
	return nil, nil
}
func (m *mockGH) ListForcePushEvents(context.Context, string, string, int) ([]ghclient.ForcePushEvent, error) {
	return nil, nil
}
func (m *mockGH) GetCombinedStatus(context.Context, string, string, string) (*gh.CombinedStatus, error) {
	return nil, nil
}
func (m *mockGH) ListCheckRunsForRef(context.Context, string, string, string) ([]*gh.CheckRun, error) {
	return nil, nil
}
func (m *mockGH) ListWorkflowRunsForHeadSHA(context.Context, string, string, string) ([]*gh.WorkflowRun, error) {
	return nil, nil
}
func (m *mockGH) ApproveWorkflowRun(context.Context, string, string, int64) error {
	return nil
}
func (m *mockGH) CreateIssueComment(context.Context, string, string, int, string) (*gh.IssueComment, error) {
	return nil, nil
}
func (m *mockGH) EditIssueComment(context.Context, string, string, int64, string) (*gh.IssueComment, error) {
	return nil, nil
}
func (m *mockGH) GetRepository(
	ctx context.Context, owner, repo string,
) (*gh.Repository, error) {
	if m.getRepositoryFn != nil {
		return m.getRepositoryFn(ctx, owner, repo)
	}
	id := int64(1)
	nodeID := "repo-" + owner + "-" + repo
	return &gh.Repository{
		ID:       &id,
		NodeID:   &nodeID,
		Name:     &repo,
		Owner:    &gh.User{Login: &owner},
		Archived: new(bool),
	}, nil
}
func (m *mockGH) CreateReview(context.Context, string, string, int, string, string) (*gh.PullRequestReview, error) {
	return nil, nil
}
func (m *mockGH) CreateReviewWithComments(
	context.Context,
	string,
	string,
	int,
	string,
	string,
	string,
	[]*gh.DraftReviewComment,
) (*gh.PullRequestReview, error) {
	return nil, nil
}
func (m *mockGH) MarkPullRequestReadyForReview(context.Context, string, string, int) (*gh.PullRequest, error) {
	return nil, nil
}
func (m *mockGH) MergePullRequest(context.Context, string, string, int, string, string, string) (*gh.PullRequestMergeResult, error) {
	return nil, nil
}
func (m *mockGH) EditPullRequest(context.Context, string, string, int, ghclient.EditPullRequestOpts) (*gh.PullRequest, error) {
	return nil, nil
}
func (m *mockGH) EditIssue(context.Context, string, string, int, string) (*gh.Issue, error) {
	return nil, nil
}
func (m *mockGH) EditIssueContent(context.Context, string, string, int, *string, *string) (*gh.Issue, error) {
	return nil, nil
}
func (m *mockGH) ListPullRequestsPage(context.Context, string, string, string, int) ([]*gh.PullRequest, bool, error) {
	return nil, false, nil
}
func (m *mockGH) ListIssuesPage(context.Context, string, string, string, int) ([]*gh.Issue, bool, error) {
	return nil, false, nil
}
func (m *mockGH) InvalidateListETagsForRepo(string, string, ...string) {}
