package github

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	gh "github.com/google/go-github/v84/github"
	"github.com/shurcooL/githubv4"
	Assert "github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/middleman/internal/db"
	"go.kenn.io/middleman/internal/gitclone"
	"go.kenn.io/middleman/internal/gitenv"
	"go.kenn.io/middleman/internal/platform"
	"go.kenn.io/middleman/internal/procutil"
	"go.kenn.io/middleman/internal/testutil/dbtest"
)

// openTestDB opens a temporary SQLite database for the duration of the test.
func openTestDB(t *testing.T) *db.DB {
	t.Helper()
	return dbtest.Open(t)
}

func setupBareRemoteForSyncTest(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	remote := filepath.Join(dir, "remote.git")
	cmd := procutil.Command("git", "init", "--bare", "--initial-branch=main", remote)
	cmd.Dir = dir
	cmd.Env = append(gitenv.StripAll(os.Environ()),
		"GIT_CONFIG_GLOBAL="+os.DevNull,
		"GIT_CONFIG_SYSTEM="+os.DevNull,
	)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git init --bare failed: %s", out)
	return remote
}

type syncBranchActivityFixture struct {
	DB       *db.DB
	Repo     RepoRef
	Remote   string
	Work     string
	Provider *syncTestRepositoryReadProvider
	Syncer   *Syncer
}

func setupSyncBranchActivityFixture(t *testing.T, defaultBranch string) syncBranchActivityFixture {
	t.Helper()
	dir := t.TempDir()
	remote := filepath.Join(dir, "remote.git")
	syncActivityGitRun(t, dir, "init", "--bare", "--initial-branch=main", remote)
	work := filepath.Join(dir, "work")
	syncActivityGitRun(t, dir, "clone", remote, work)
	syncActivityGitRun(t, work, "config", "user.email", "alice@example.com")
	syncActivityGitRun(t, work, "config", "user.name", "Alice")

	d := openTestDB(t)
	clones := gitclone.New(t.TempDir(), nil)
	repo := RepoRef{
		Platform:           platform.KindGitLab,
		PlatformHost:       "gitlab.example.com",
		Owner:              "group",
		Name:               "project",
		RepoPath:           "group/project",
		PlatformExternalID: "gid://gitlab/Project/branch-activity",
		CloneURL:           remote,
		DefaultBranch:      defaultBranch,
	}
	provider := &syncTestRepositoryReadProvider{
		syncTestReadProvider: &syncTestReadProvider{
			syncTestProvider: syncTestProvider{
				kind: platform.KindGitLab,
				host: "gitlab.example.com",
			},
		},
		repository: platform.Repository{
			Ref: platform.RepoRef{
				Platform:           platform.KindGitLab,
				Host:               "gitlab.example.com",
				Owner:              "group",
				Name:               "project",
				RepoPath:           "group/project",
				PlatformExternalID: "gid://gitlab/Project/branch-activity",
				CloneURL:           remote,
				DefaultBranch:      defaultBranch,
			},
			PlatformExternalID: "gid://gitlab/Project/branch-activity",
			CloneURL:           remote,
			DefaultBranch:      defaultBranch,
		},
	}
	registry, err := platform.NewRegistry(provider)
	require.NoError(t, err)
	syncer := NewSyncerWithRegistry(registry, d, clones, []RepoRef{repo}, time.Minute, nil, nil)
	return syncBranchActivityFixture{
		DB:       d,
		Repo:     repo,
		Remote:   remote,
		Work:     work,
		Provider: provider,
		Syncer:   syncer,
	}
}

func syncActivityGitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := procutil.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(gitenv.StripAll(os.Environ()),
		"GIT_CONFIG_GLOBAL="+os.DevNull,
		"GIT_CONFIG_SYSTEM="+os.DevNull,
	)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %v failed: %s", args, out)
	return strings.TrimSpace(string(out))
}

func syncActivityCommit(t *testing.T, work, fileName, contents, subject string) string {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(work, fileName), []byte(contents), 0o644))
	syncActivityGitRun(t, work, "add", fileName)
	syncActivityGitRun(t, work, "commit", "-m", subject)
	return syncActivityGitRun(t, work, "rev-parse", "HEAD")
}

func syncActivityCommitAndPush(
	t *testing.T,
	work, fileName, contents, subject, branch string,
) string {
	t.Helper()
	sha := syncActivityCommit(t, work, fileName, contents, subject)
	syncActivityGitRun(t, work, "push", "origin", "HEAD:"+branch)
	return sha
}

func syncActivityItems(
	t *testing.T,
	d *db.DB,
	types ...string,
) []db.ActivityItem {
	t.Helper()
	items, err := d.ListActivity(t.Context(), db.ListActivityOpts{
		Limit: 50,
		Types: types,
	})
	require.NoError(t, err)
	return items
}

func syncActivityBranchCommits(t *testing.T, d *db.DB) []db.ActivityItem {
	t.Helper()
	return syncActivityItems(t, d, "default_branch_commit")
}

func syncActivityForcePushes(t *testing.T, d *db.DB) []db.ActivityItem {
	t.Helper()
	return syncActivityItems(t, d, "default_branch_force_push")
}

func requireSyncActivityRepoRow(t *testing.T, d *db.DB) db.Repo {
	t.Helper()
	repoRow, err := d.GetRepoByOwnerName(t.Context(), "group", "project")
	require.NoError(t, err)
	require.NotNil(t, repoRow)
	return *repoRow
}

func TestSyncRepoRecordsDefaultBranchCommits(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	fixture := setupSyncBranchActivityFixture(t, "main")
	sha := syncActivityCommitAndPush(
		t,
		fixture.Work,
		"direct.txt",
		"direct work\n",
		"direct work",
		"main",
	)

	require.NoError(fixture.Syncer.syncRepo(t.Context(), fixture.Repo))

	commits := syncActivityBranchCommits(t, fixture.DB)
	require.NotEmpty(commits)
	var direct db.ActivityItem
	for _, item := range commits {
		if item.CommitSHA == sha {
			direct = item
			break
		}
	}
	require.NotEmpty(direct.CommitSHA)
	assert.Equal("default_branch_commit", direct.ActivityType)
	assert.Equal("main", direct.BranchName)
	assert.Equal("direct work", direct.BodyPreview)
	assert.Equal("Alice", direct.AuthorName)
	assert.Equal("alice@example.com", direct.AuthorEmail)
	assert.Equal("Alice", direct.CommitterName)
	assert.Equal("alice@example.com", direct.CommitterEmail)
	assert.NotNil(direct.AuthoredAt)
	assert.NotNil(direct.CommittedAt)

	repoRow := requireSyncActivityRepoRow(t, fixture.DB)
	tip, err := fixture.DB.GetBranchTip(t.Context(), repoRow.ID, "main")
	require.NoError(err)
	require.NotNil(tip)
	assert.Equal(sha, tip.TipSHA)
	assert.Empty(syncActivityForcePushes(t, fixture.DB))
}

func TestSyncRepoCapsDefaultBranchCommits(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	fixture := setupSyncBranchActivityFixture(t, "main")
	fixture.Syncer.SetBranchActivityLimits(90*24*time.Hour, 2)

	var shas []string
	for i := range 4 {
		suffix := string(rune('0' + i))
		shas = append(shas, syncActivityCommit(
			t,
			fixture.Work,
			"direct-"+suffix+".txt",
			"direct "+suffix+"\n",
			"direct work "+suffix,
		))
	}
	syncActivityGitRun(t, fixture.Work, "push", "origin", "HEAD:main")

	require.NoError(fixture.Syncer.syncRepo(t.Context(), fixture.Repo))

	commits := syncActivityBranchCommits(t, fixture.DB)
	require.Len(commits, 2)
	var got []string
	for _, item := range commits {
		got = append(got, item.CommitSHA)
	}
	assert.ElementsMatch([]string{shas[3], shas[2]}, got)
}

func TestSyncRepoRecordsDefaultBranchForcePushBeforeUpdatingTip(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	fixture := setupSyncBranchActivityFixture(t, "main")
	beforeSHA := syncActivityCommitAndPush(
		t,
		fixture.Work,
		"before.txt",
		"before\n",
		"before rewrite",
		"main",
	)
	require.NoError(fixture.Syncer.syncRepo(t.Context(), fixture.Repo))

	syncActivityGitRun(t, fixture.Work, "checkout", "--orphan", "rewrite")
	syncActivityGitRun(t, fixture.Work, "rm", "-r", "--cached", ".")
	afterSHA := syncActivityCommit(t, fixture.Work, "after.txt", "after\n", "after rewrite")
	syncActivityGitRun(t, fixture.Work, "push", "--force", "origin", "HEAD:main")

	require.NoError(fixture.Syncer.syncRepo(t.Context(), fixture.Repo))

	forcePushes := syncActivityForcePushes(t, fixture.DB)
	require.Len(forcePushes, 1)
	assert.Equal("default_branch_force_push", forcePushes[0].ActivityType)
	assert.Equal("main", forcePushes[0].BranchName)
	assert.Equal(beforeSHA, forcePushes[0].BeforeSHA)
	assert.Equal(afterSHA, forcePushes[0].AfterSHA)

	repoRow := requireSyncActivityRepoRow(t, fixture.DB)
	tip, err := fixture.DB.GetBranchTip(t.Context(), repoRow.ID, "main")
	require.NoError(err)
	require.NotNil(tip)
	assert.Equal(afterSHA, tip.TipSHA)
}

func TestSyncRepoSkipsBranchActivityWhenCloneFetchFails(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	fixture := setupSyncBranchActivityFixture(t, "main")
	initialSHA := syncActivityCommitAndPush(
		t,
		fixture.Work,
		"initial.txt",
		"initial\n",
		"initial branch activity",
		"main",
	)
	require.NoError(fixture.Syncer.syncRepo(t.Context(), fixture.Repo))
	repoRow := requireSyncActivityRepoRow(t, fixture.DB)
	initialTip, err := fixture.DB.GetBranchTip(t.Context(), repoRow.ID, "main")
	require.NoError(err)
	require.NotNil(initialTip)
	initialCommitCount := len(syncActivityBranchCommits(t, fixture.DB))

	newSHA := syncActivityCommitAndPush(
		t,
		fixture.Work,
		"new.txt",
		"new\n",
		"new branch activity",
		"main",
	)
	offlineRemote := fixture.Remote + ".offline"
	require.NoError(os.Rename(fixture.Remote, offlineRemote))
	defer func() {
		if _, err := os.Stat(fixture.Remote); errors.Is(err, os.ErrNotExist) {
			require.NoError(os.Rename(offlineRemote, fixture.Remote))
		}
	}()

	require.NoError(fixture.Syncer.syncRepo(t.Context(), fixture.Repo))

	tip, err := fixture.DB.GetBranchTip(t.Context(), repoRow.ID, "main")
	require.NoError(err)
	require.NotNil(tip)
	assert.Equal(initialSHA, tip.TipSHA)
	assert.Equal(initialTip.ObservedAt, tip.ObservedAt)
	assert.Len(syncActivityBranchCommits(t, fixture.DB), initialCommitCount)
	for _, item := range syncActivityBranchCommits(t, fixture.DB) {
		assert.NotEqual(newSHA, item.CommitSHA)
	}
	assert.Empty(syncActivityForcePushes(t, fixture.DB))
}

func TestSyncRepoDefaultBranchRenameDoesNotRecordForcePush(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	fixture := setupSyncBranchActivityFixture(t, "main")
	mainSHA := syncActivityCommitAndPush(
		t,
		fixture.Work,
		"main.txt",
		"main\n",
		"main branch work",
		"main",
	)
	require.NoError(fixture.Syncer.syncRepo(t.Context(), fixture.Repo))

	syncActivityGitRun(t, fixture.Work, "checkout", "--orphan", "trunk")
	syncActivityGitRun(t, fixture.Work, "rm", "-r", "--cached", ".")
	trunkSHA := syncActivityCommit(t, fixture.Work, "trunk.txt", "trunk\n", "trunk branch work")
	syncActivityGitRun(t, fixture.Work, "push", "origin", "HEAD:trunk")
	syncActivityGitRun(t, fixture.Remote, "symbolic-ref", "HEAD", "refs/heads/trunk")

	renamedRepo := fixture.Repo
	fixture.Provider.repository.Ref.DefaultBranch = "trunk"
	fixture.Provider.repository.DefaultBranch = "trunk"
	require.NoError(fixture.Syncer.syncRepo(t.Context(), renamedRepo))

	assert.Empty(syncActivityForcePushes(t, fixture.DB))
	repoRow := requireSyncActivityRepoRow(t, fixture.DB)
	mainTip, err := fixture.DB.GetBranchTip(t.Context(), repoRow.ID, "main")
	require.NoError(err)
	require.NotNil(mainTip)
	assert.Equal(mainSHA, mainTip.TipSHA)
	trunkTip, err := fixture.DB.GetBranchTip(t.Context(), repoRow.ID, "trunk")
	require.NoError(err)
	require.NotNil(trunkTip)
	assert.Equal(trunkSHA, trunkTip.TipSHA)
}

// testBudget builds a per-host budget map for use in NewSyncer calls.
func testBudget(limit int) map[string]*SyncBudget {
	return map[string]*SyncBudget{
		"github.com": NewSyncBudget(limit),
	}
}

// mockClient implements Client with configurable canned responses.
type mockClient struct {
	budget                          *SyncBudget // optional: simulates transport counting
	openPRs                         []*gh.PullRequest
	openIssues                      []*gh.Issue
	listOpenPRsErr                  error
	listOpenIssuesErr               error
	singlePR                        *gh.PullRequest
	createIssueFn                   func(context.Context, string, string, string, string) (*gh.Issue, error)
	getRepositoryFn                 func(context.Context, string, string) (*gh.Repository, error)
	getPullRequestFn                func(context.Context, string, string, int) (*gh.PullRequest, error)
	getIssueFn                      func(context.Context, string, string, int) (*gh.Issue, error)
	getUserFn                       func(context.Context, string) (*gh.User, error)
	listReposByOwnerFn              func(context.Context, string) ([]*gh.Repository, error)
	listReleases                    []*gh.RepositoryRelease
	listReleasesErr                 error
	listReleasesFn                  func(context.Context, string, string, int) ([]*gh.RepositoryRelease, error)
	listTags                        []*gh.RepositoryTag
	listTagsErr                     error
	listTagsFn                      func(context.Context, string, string, int) ([]*gh.RepositoryTag, error)
	listOpenPRsFn                   func(context.Context, string, string) ([]*gh.PullRequest, error)
	listPullRequestsPageFn          func(context.Context, string, string, string, int) ([]*gh.PullRequest, bool, error)
	listIssuesPageFn                func(context.Context, string, string, string, int) ([]*gh.Issue, bool, error)
	comments                        []*gh.IssueComment
	reviews                         []*gh.PullRequestReview
	commits                         []*gh.RepositoryCommit
	timelineEvents                  []PullRequestTimelineEvent
	timelineEventsErr               error
	forcePushEvents                 []ForcePushEvent
	forcePushEventsErr              error
	ciStatus                        *gh.CombinedStatus
	ciStatusErr                     error
	checkRuns                       []*gh.CheckRun
	checkRunsErr                    error
	workflowRuns                    []*gh.WorkflowRun
	approveWorkflowRunFn            func(context.Context, string, string, int64) error
	listOpenPRsCalled               bool
	getUserCalls                    atomic.Int32
	getCombinedCalls                atomic.Int32
	invalidateCalls                 atomic.Int32
	listIssueCommentsCalled         atomic.Int32
	listIssueCommentsIfChangedCalls atomic.Int32
	listIssueCommentsErr            error
	listIssueCommentsFn             func(context.Context, string, string, int) ([]*gh.IssueComment, error)
	listIssueCommentsIfChangedFn    func(context.Context, string, string, int) ([]*gh.IssueComment, error)
}

type labelCatalogTestClient struct {
	*mockClient
	labels []*gh.Label
	calls  atomic.Int32
}

func (c *labelCatalogTestClient) ListRepoLabels(
	_ context.Context, _, _ string,
) ([]*gh.Label, error) {
	c.calls.Add(1)
	return append([]*gh.Label(nil), c.labels...), nil
}

func (c *labelCatalogTestClient) ReplaceIssueLabels(
	_ context.Context, _, _ string, _ int, names []string,
) ([]*gh.Label, error) {
	byName := make(map[string]*gh.Label, len(c.labels))
	for _, label := range c.labels {
		byName[label.GetName()] = label
	}
	labels := make([]*gh.Label, 0, len(names))
	for _, name := range names {
		labels = append(labels, byName[name])
	}
	return labels, nil
}

type syncTestProvider struct {
	kind platform.Kind
	host string
}

func (p syncTestProvider) Platform() platform.Kind {
	return p.kind
}

func (p syncTestProvider) Host() string {
	return p.host
}

func (p syncTestProvider) Capabilities() platform.Capabilities {
	return platform.Capabilities{}
}

type syncTestReadProvider struct {
	syncTestProvider
	mergeRequests       []platform.MergeRequest
	issues              []platform.Issue
	listMRCalls         atomic.Int32
	listIssueCalls      atomic.Int32
	getMRCalls          atomic.Int32
	getIssueCalls       atomic.Int32
	listMRMergeEvents   []platform.MergeRequestEvent
	listIssueReadEvents []platform.IssueEvent
}

type syncTestRepositoryReadProvider struct {
	*syncTestReadProvider
	repository         platform.Repository
	getRepositoryCalls atomic.Int32
}

type syncTestMergeRequestOnlyProvider struct {
	syncTestProvider
	mergeRequests []platform.MergeRequest
	listMRCalls   atomic.Int32
}

func (p *syncTestMergeRequestOnlyProvider) Capabilities() platform.Capabilities {
	return platform.Capabilities{ReadMergeRequests: true}
}

func (p *syncTestMergeRequestOnlyProvider) ListOpenMergeRequests(
	context.Context,
	platform.RepoRef,
) ([]platform.MergeRequest, error) {
	p.listMRCalls.Add(1)
	return p.mergeRequests, nil
}

func (p *syncTestMergeRequestOnlyProvider) GetMergeRequest(
	context.Context,
	platform.RepoRef,
	int,
) (platform.MergeRequest, error) {
	return platform.MergeRequest{}, nil
}

type syncTestIssueOnlyProvider struct {
	syncTestProvider
	issues         []platform.Issue
	listIssueCalls atomic.Int32
}

func (p *syncTestIssueOnlyProvider) Capabilities() platform.Capabilities {
	return platform.Capabilities{ReadIssues: true}
}

func (p *syncTestIssueOnlyProvider) ListOpenIssues(
	context.Context,
	platform.RepoRef,
) ([]platform.Issue, error) {
	p.listIssueCalls.Add(1)
	return p.issues, nil
}

func (p *syncTestIssueOnlyProvider) GetIssue(
	_ context.Context,
	_ platform.RepoRef,
	number int,
) (platform.Issue, error) {
	for _, issue := range p.issues {
		if issue.Number == number {
			return issue, nil
		}
	}
	return platform.Issue{}, errors.New("missing issue")
}

func (p *syncTestIssueOnlyProvider) ListIssueEvents(
	context.Context,
	platform.RepoRef,
	int,
) ([]platform.IssueEvent, error) {
	return nil, nil
}

func (p *syncTestMergeRequestOnlyProvider) ListMergeRequestEvents(
	context.Context,
	platform.RepoRef,
	int,
) ([]platform.MergeRequestEvent, error) {
	return nil, nil
}

func (p *syncTestReadProvider) Capabilities() platform.Capabilities {
	return platform.Capabilities{
		ReadMergeRequests: true,
		ReadIssues:        true,
	}
}

func (p *syncTestRepositoryReadProvider) GetRepository(
	context.Context,
	platform.RepoRef,
) (platform.Repository, error) {
	p.getRepositoryCalls.Add(1)
	return p.repository, nil
}

func (p *syncTestRepositoryReadProvider) ListRepositories(
	context.Context,
	string,
	platform.RepositoryListOptions,
) ([]platform.Repository, error) {
	return nil, nil
}

func (p *syncTestReadProvider) ListOpenMergeRequests(
	context.Context,
	platform.RepoRef,
) ([]platform.MergeRequest, error) {
	p.listMRCalls.Add(1)
	return p.mergeRequests, nil
}

func (p *syncTestReadProvider) GetMergeRequest(
	_ context.Context,
	_ platform.RepoRef,
	number int,
) (platform.MergeRequest, error) {
	p.getMRCalls.Add(1)
	for _, mr := range p.mergeRequests {
		if mr.Number == number {
			return mr, nil
		}
	}
	return platform.MergeRequest{}, errors.New("missing merge request")
}

func (p *syncTestReadProvider) ListMergeRequestEvents(
	context.Context,
	platform.RepoRef,
	int,
) ([]platform.MergeRequestEvent, error) {
	return p.listMRMergeEvents, nil
}

func (p *syncTestReadProvider) ListOpenIssues(
	context.Context,
	platform.RepoRef,
) ([]platform.Issue, error) {
	p.listIssueCalls.Add(1)
	return p.issues, nil
}

func (p *syncTestReadProvider) GetIssue(
	_ context.Context,
	_ platform.RepoRef,
	number int,
) (platform.Issue, error) {
	p.getIssueCalls.Add(1)
	for _, issue := range p.issues {
		if issue.Number == number {
			return issue, nil
		}
	}
	return platform.Issue{}, errors.New("missing issue")
}

func (p *syncTestReadProvider) ListIssueEvents(
	context.Context,
	platform.RepoRef,
	int,
) ([]platform.IssueEvent, error) {
	return p.listIssueReadEvents, nil
}

func (m *mockClient) trackCall() {
	if m.budget != nil {
		m.budget.Spend(1)
	}
}

func (m *mockClient) InvalidateListETagsForRepo(_, _ string, _ ...string) {
	m.invalidateCalls.Add(1)
}

func (m *mockClient) ListOpenPullRequests(ctx context.Context, owner, repo string) ([]*gh.PullRequest, error) {
	m.trackCall()
	m.listOpenPRsCalled = true
	if m.listOpenPRsFn != nil {
		return m.listOpenPRsFn(ctx, owner, repo)
	}
	if m.listOpenPRsErr != nil {
		return nil, m.listOpenPRsErr
	}
	return m.openPRs, nil
}

func (m *mockClient) ListOpenIssues(_ context.Context, _, _ string) ([]*gh.Issue, error) {
	m.trackCall()
	if m.listOpenIssuesErr != nil {
		return nil, m.listOpenIssuesErr
	}
	return m.openIssues, nil
}

func (m *mockClient) GetIssue(
	ctx context.Context, owner, repo string, number int,
) (*gh.Issue, error) {
	m.trackCall()
	if m.getIssueFn != nil {
		return m.getIssueFn(ctx, owner, repo, number)
	}
	return nil, nil
}

func (m *mockClient) CreateIssue(
	ctx context.Context, owner, repo, title, body string,
) (*gh.Issue, error) {
	m.trackCall()
	if m.createIssueFn != nil {
		return m.createIssueFn(ctx, owner, repo, title, body)
	}
	return nil, nil
}

func (m *mockClient) GetUser(ctx context.Context, login string) (*gh.User, error) {
	m.trackCall()
	m.getUserCalls.Add(1)
	if m.getUserFn != nil {
		return m.getUserFn(ctx, login)
	}
	name := "Display " + login
	return &gh.User{Login: &login, Name: &name}, nil
}

func (m *mockClient) ListRepositoriesByOwner(
	ctx context.Context, owner string,
) ([]*gh.Repository, error) {
	m.trackCall()
	if m.listReposByOwnerFn != nil {
		return m.listReposByOwnerFn(ctx, owner)
	}
	return nil, nil
}

func (m *mockClient) ListReleases(
	ctx context.Context, owner, repo string, perPage int,
) ([]*gh.RepositoryRelease, error) {
	m.trackCall()
	if m.listReleasesFn != nil {
		return m.listReleasesFn(ctx, owner, repo, perPage)
	}
	if m.listReleasesErr != nil {
		return nil, m.listReleasesErr
	}
	return m.listReleases, nil
}

func (m *mockClient) ListTags(
	ctx context.Context, owner, repo string, perPage int,
) ([]*gh.RepositoryTag, error) {
	m.trackCall()
	if m.listTagsFn != nil {
		return m.listTagsFn(ctx, owner, repo, perPage)
	}
	if m.listTagsErr != nil {
		return nil, m.listTagsErr
	}
	return m.listTags, nil
}

func (m *mockClient) GetPullRequest(
	ctx context.Context, owner, repo string, number int,
) (*gh.PullRequest, error) {
	m.trackCall()
	if m.getPullRequestFn != nil {
		return m.getPullRequestFn(ctx, owner, repo, number)
	}
	if m.singlePR != nil {
		return m.singlePR, nil
	}
	// Fall back to matching from the open PRs list
	for _, pr := range m.openPRs {
		if pr.GetNumber() == number {
			return pr, nil
		}
	}
	return nil, nil
}

func (m *mockClient) ListIssueComments(ctx context.Context, owner, repo string, number int) ([]*gh.IssueComment, error) {
	m.trackCall()
	m.listIssueCommentsCalled.Add(1)
	if m.listIssueCommentsFn != nil {
		return m.listIssueCommentsFn(ctx, owner, repo, number)
	}
	if m.listIssueCommentsErr != nil {
		return nil, m.listIssueCommentsErr
	}
	return m.comments, nil
}

func (m *mockClient) ListIssueCommentsIfChanged(
	ctx context.Context, owner, repo string, number int,
) ([]*gh.IssueComment, error) {
	m.listIssueCommentsIfChangedCalls.Add(1)
	if m.listIssueCommentsIfChangedFn != nil {
		return m.listIssueCommentsIfChangedFn(ctx, owner, repo, number)
	}
	if m.listIssueCommentsErr != nil {
		return nil, m.listIssueCommentsErr
	}
	if m.comments == nil {
		return nil, notModifiedErr()
	}
	return m.ListIssueComments(ctx, owner, repo, number)
}

func (m *mockClient) ListReviews(_ context.Context, _, _ string, _ int) ([]*gh.PullRequestReview, error) {
	m.trackCall()
	return m.reviews, nil
}

func (m *mockClient) ListCommits(_ context.Context, _, _ string, _ int) ([]*gh.RepositoryCommit, error) {
	m.trackCall()
	return m.commits, nil
}

func (m *mockClient) ListForcePushEvents(_ context.Context, _, _ string, _ int) ([]ForcePushEvent, error) {
	m.trackCall()
	return m.forcePushEvents, m.forcePushEventsErr
}

func (m *mockClient) GetCombinedStatus(_ context.Context, _, _, _ string) (*gh.CombinedStatus, error) {
	m.trackCall()
	m.getCombinedCalls.Add(1)
	if m.ciStatusErr != nil {
		return nil, m.ciStatusErr
	}
	return m.ciStatus, nil
}

func (m *mockClient) ListCheckRunsForRef(_ context.Context, _, _, _ string) ([]*gh.CheckRun, error) {
	m.trackCall()
	if m.checkRunsErr != nil {
		return nil, m.checkRunsErr
	}
	return m.checkRuns, nil
}

func (m *mockClient) ListWorkflowRunsForHeadSHA(
	_ context.Context, _, _, _ string,
) ([]*gh.WorkflowRun, error) {
	m.trackCall()
	return m.workflowRuns, nil
}

func (m *mockClient) ApproveWorkflowRun(
	ctx context.Context, owner, repo string, runID int64,
) error {
	m.trackCall()
	if m.approveWorkflowRunFn != nil {
		return m.approveWorkflowRunFn(ctx, owner, repo, runID)
	}
	return nil
}

func (m *mockClient) CreateIssueComment(
	_ context.Context, _, _ string, _ int, _ string,
) (*gh.IssueComment, error) {
	m.trackCall()
	return nil, nil
}

func (m *mockClient) EditIssueComment(
	_ context.Context, _, _ string, _ int64, _ string,
) (*gh.IssueComment, error) {
	m.trackCall()
	return nil, nil
}

func (m *mockClient) GetRepository(
	ctx context.Context, owner, repo string,
) (*gh.Repository, error) {
	m.trackCall()
	if m.getRepositoryFn != nil {
		return m.getRepositoryFn(ctx, owner, repo)
	}
	id := int64(1)
	nodeID := "repo-" + owner + "-" + repo
	return &gh.Repository{
		ID:     &id,
		NodeID: &nodeID,
		Name:   &repo,
		Owner:  &gh.User{Login: &owner},
	}, nil
}

func (m *mockClient) CreateReview(
	_ context.Context, _, _ string, _ int, _ string, _ string,
) (*gh.PullRequestReview, error) {
	m.trackCall()
	id := int64(1)
	state := "APPROVED"
	return &gh.PullRequestReview{ID: &id, State: &state}, nil
}

func (m *mockClient) MarkPullRequestReadyForReview(
	_ context.Context, _, _ string, number int,
) (*gh.PullRequest, error) {
	m.trackCall()
	draft := false
	return &gh.PullRequest{Number: &number, Draft: &draft}, nil
}

func (m *mockClient) MergePullRequest(
	_ context.Context, _, _ string, _ int, _, _, _ string,
) (*gh.PullRequestMergeResult, error) {
	m.trackCall()
	merged := true
	sha := "abc123"
	msg := "merged"
	return &gh.PullRequestMergeResult{
		Merged: &merged, SHA: &sha, Message: &msg,
	}, nil
}

func (m *mockClient) EditPullRequest(
	_ context.Context, _, _ string, _ int, opts EditPullRequestOpts,
) (*gh.PullRequest, error) {
	m.trackCall()
	pr := &gh.PullRequest{}
	if opts.State != nil {
		pr.State = opts.State
	}
	return pr, nil
}

func (m *mockClient) EditIssue(
	_ context.Context, _, _ string, _ int, state string,
) (*gh.Issue, error) {
	m.trackCall()
	return &gh.Issue{State: &state}, nil
}

func (m *mockClient) EditIssueContent(
	_ context.Context, _, _ string, _ int, title *string, body *string,
) (*gh.Issue, error) {
	m.trackCall()
	out := &gh.Issue{}
	if title != nil {
		out.Title = title
	}
	if body != nil {
		out.Body = body
	}
	return out, nil
}

func (m *mockClient) ListPullRequestsPage(
	ctx context.Context, owner, repo, state string, page int,
) ([]*gh.PullRequest, bool, error) {
	m.trackCall()
	if m.listPullRequestsPageFn != nil {
		return m.listPullRequestsPageFn(ctx, owner, repo, state, page)
	}
	return nil, false, nil
}

func (m *mockClient) ListIssuesPage(
	ctx context.Context, owner, repo, state string, page int,
) ([]*gh.Issue, bool, error) {
	m.trackCall()
	if m.listIssuesPageFn != nil {
		return m.listIssuesPageFn(ctx, owner, repo, state, page)
	}
	return nil, false, nil
}

// makeTimestamp is a helper for constructing go-github Timestamp values.
func makeTimestamp(t time.Time) *gh.Timestamp {
	return &gh.Timestamp{Time: t}
}

// buildOpenPR constructs a minimal open *gh.PullRequest for tests.
func buildOpenPR(number int, updatedAt time.Time) *gh.PullRequest {
	sha := "abc123def456"
	state := "open"
	title := "test PR"
	url := "https://github.com/owner/repo/pull/1"
	id := int64(number) * 1000
	headRef := "feature-branch"
	baseRef := "main"
	return &gh.PullRequest{
		ID:        &id,
		Number:    &number,
		Title:     &title,
		HTMLURL:   &url,
		State:     &state,
		UpdatedAt: makeTimestamp(updatedAt),
		CreatedAt: makeTimestamp(updatedAt),
		Head: &gh.PullRequestBranch{
			Ref: &headRef,
			SHA: &sha,
		},
		Base: &gh.PullRequestBranch{
			Ref: &baseRef,
		},
	}
}

func buildOpenIssue(number int, updatedAt time.Time) *gh.Issue {
	state := "open"
	title := fmt.Sprintf("test issue %d", number)
	url := fmt.Sprintf("https://github.com/owner/repo/issues/%d", number)
	id := int64(number) * 1000
	author := "alice"
	return &gh.Issue{
		ID:        &id,
		Number:    &number,
		Title:     &title,
		HTMLURL:   &url,
		State:     &state,
		User:      &gh.User{Login: &author},
		UpdatedAt: makeTimestamp(updatedAt),
		CreatedAt: makeTimestamp(updatedAt),
	}
}

func buildGitHubLabel(id int64, name, description, color string, isDefault bool) *gh.Label {
	return &gh.Label{
		ID:          &id,
		Name:        &name,
		Description: &description,
		Color:       &color,
		Default:     &isDefault,
	}
}

func TestDiffSyncErrorUserMessageSanitized(t *testing.T) {
	assert := Assert.New(t)
	// A representative leak: clone path, ref, SHA, and command stderr.
	leaky := fmt.Errorf(
		"rev-parse refs/pull/42/head for merged PR #42: " +
			"exec /home/user/.middleman/clones/github.com/owner/repo.git: " +
			"fatal: ambiguous argument 'deadbeefdeadbeefdeadbeefdeadbeefdeadbeef'")

	cases := []struct {
		name string
		code DiffSyncErrorCode
	}{
		{"clone unavailable", DiffSyncCodeCloneUnavailable},
		{"commit unreachable", DiffSyncCodeCommitUnreachable},
		{"merge base failed", DiffSyncCodeMergeBaseFailed},
		{"internal", DiffSyncCodeInternal},
		{"unknown code", DiffSyncErrorCode("not_a_real_code")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := &DiffSyncError{Code: tc.code, Err: leaky}
			msg := e.UserMessage()
			assert.NotEmpty(msg, "user message should never be empty")
			assert.NotContains(msg, "/home/user", "user message must not leak filesystem paths")
			assert.NotContains(msg, "refs/pull/", "user message must not leak git refs")
			assert.NotContains(msg, "deadbeef", "user message must not leak SHAs")
			assert.NotContains(msg, "rev-parse", "user message must not leak git command names")
			assert.NotContains(msg, "fatal:", "user message must not leak git stderr")
		})
	}

	// Error() (used for server-side logs) is allowed to include the
	// underlying detail; only UserMessage() is the public surface.
	e := &DiffSyncError{Code: DiffSyncCodeCommitUnreachable, Err: leaky}
	assert.Contains(e.Error(), "commit_unreachable",
		"server-side Error() should include the categorization")
	assert.Contains(e.Error(), "deadbeef",
		"server-side Error() may include underlying detail for debugging")
}

func TestSyncCreatesAndUpdatesPRs(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	d := openTestDB(t)

	now := time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC)
	commitMsg := "initial commit"
	commitSHA := "abc123def456"
	commitDate := makeTimestamp(now.Add(-1 * time.Hour))
	ciState := "success"

	mc := &mockClient{
		openPRs: []*gh.PullRequest{buildOpenPR(1, now)},
		commits: []*gh.RepositoryCommit{
			{
				SHA: &commitSHA,
				Commit: &gh.Commit{
					Message: &commitMsg,
					Author: &gh.CommitAuthor{
						Name: new("dev"),
						Date: commitDate,
					},
				},
			},
		},
		reviews:  []*gh.PullRequestReview{},
		comments: []*gh.IssueComment{},
		ciStatus: &gh.CombinedStatus{State: &ciState},
	}

	syncer := NewSyncer(map[string]Client{"github.com": mc}, d, nil, []RepoRef{{Owner: "owner", Name: "repo", PlatformHost: "github.com"}}, time.Minute, nil, testBudget(500))
	syncer.RunOnce(ctx)

	// PR should be in the DB.
	pr, err := d.GetMergeRequest(ctx, "owner", "repo", 1)
	require.NoError(err)
	require.NotNil(pr)
	assert.Equal(1, pr.Number)

	// Kanban state should have been created.
	ks, err := d.GetKanbanState(ctx, pr.ID)
	require.NoError(err)
	require.NotNil(ks)
	assert.Equal("new", ks.Status)

	// Commit event should have been stored (via detail drain).
	events, err := d.ListMREvents(ctx, pr.ID)
	require.NoError(err)
	require.NotEmpty(events)
	found := false
	for _, e := range events {
		if e.EventType == "commit" {
			found = true
			break
		}
	}
	assert.True(found)
}

func TestSyncRepoOverviewPreservesTimelineWhenCloneUnavailable(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	d := openTestDB(t)
	repoID, err := d.UpsertRepo(ctx, db.GitHubRepoIdentity("github.com", "owner", "repo"))
	require.NoError(err)

	oldPublishedAt := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	oldTimelineUpdatedAt := time.Date(2026, 3, 2, 12, 0, 0, 0, time.UTC)
	oldCommitsSince := 7
	err = d.UpsertRepoOverview(ctx, repoID, db.RepoOverview{
		LatestRelease: &db.RepoRelease{
			TagName:     "v1.0.0",
			Name:        "Version 1.0.0",
			URL:         "https://github.com/owner/repo/releases/tag/v1.0.0",
			PublishedAt: &oldPublishedAt,
		},
		Releases: []db.RepoRelease{{
			TagName:     "v1.0.0",
			Name:        "Version 1.0.0",
			URL:         "https://github.com/owner/repo/releases/tag/v1.0.0",
			PublishedAt: &oldPublishedAt,
		}},
		CommitsSinceRelease: &oldCommitsSince,
		CommitTimeline: []db.RepoCommitTimelinePoint{{
			SHA:         "abc123",
			Message:     "Keep cached timeline",
			CommittedAt: time.Date(2026, 3, 2, 10, 0, 0, 0, time.UTC),
		}},
		TimelineUpdatedAt: &oldTimelineUpdatedAt,
	})
	require.NoError(err)

	tagName := "v1.0.0"
	releaseName := "Version 1.0.0"
	releaseURL := "https://github.com/owner/repo/releases/tag/v1.0.0"
	client := &mockClient{
		listReleases: []*gh.RepositoryRelease{{
			TagName:     &tagName,
			Name:        &releaseName,
			HTMLURL:     &releaseURL,
			PublishedAt: &gh.Timestamp{Time: oldPublishedAt},
		}},
	}
	syncer := NewSyncer(
		map[string]Client{"github.com": client},
		d,
		nil,
		nil,
		time.Minute,
		nil,
		nil,
	)

	syncer.syncRepoOverview(
		ctx,
		client,
		RepoRef{PlatformHost: "github.com", Owner: "owner", Name: "repo"},
		repoID,
		false,
	)

	summaries, err := d.ListRepoSummaries(ctx)
	require.NoError(err)
	require.Len(summaries, 1)
	overview := summaries[0].Overview
	require.NotNil(overview.LatestRelease)
	require.NotNil(overview.CommitsSinceRelease)
	require.Len(overview.CommitTimeline, 1)
	require.NotNil(overview.TimelineUpdatedAt)

	assert.Equal("v1.0.0", overview.LatestRelease.TagName)
	assert.Equal(oldPublishedAt, *overview.LatestRelease.PublishedAt)
	assert.Equal(oldCommitsSince, *overview.CommitsSinceRelease)
	assert.Equal("abc123", overview.CommitTimeline[0].SHA)
	assert.Equal("Keep cached timeline", overview.CommitTimeline[0].Message)
	assert.Equal(oldTimelineUpdatedAt, *overview.TimelineUpdatedAt)
}

func TestSyncRepoOverviewUsesTagsWhenRepoHasNoReleases(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	d := openTestDB(t)
	repoID, err := d.UpsertRepo(ctx, db.GitHubRepoIdentity("github.com", "owner", "repo"))
	require.NoError(err)

	tagName := "v1.2.3"
	sha := "abcdef1234567890abcdef1234567890abcdef12"
	client := &mockClient{
		listReleases: []*gh.RepositoryRelease{},
		listTags: []*gh.RepositoryTag{{
			Name: &tagName,
			Commit: &gh.Commit{
				SHA: &sha,
			},
		}},
	}
	syncer := NewSyncer(
		map[string]Client{"github.com": client},
		d,
		nil,
		nil,
		time.Minute,
		nil,
		nil,
	)

	syncer.syncRepoOverview(
		ctx,
		client,
		RepoRef{PlatformHost: "github.com", Owner: "owner", Name: "repo"},
		repoID,
		false,
	)

	summaries, err := d.ListRepoSummaries(ctx)
	require.NoError(err)
	require.Len(summaries, 1)
	overview := summaries[0].Overview
	require.NotNil(overview.LatestRelease)
	require.Len(overview.Releases, 1)

	assert.Equal("v1.2.3", overview.LatestRelease.TagName)
	assert.Equal("v1.2.3", overview.LatestRelease.Name)
	assert.Equal("https://github.com/owner/repo/tree/v1.2.3", overview.LatestRelease.URL)
	assert.Equal(sha, overview.LatestRelease.TargetCommitish)
	assert.Nil(overview.LatestRelease.PublishedAt)
	assert.False(overview.LatestRelease.Prerelease)
}

func TestSyncRepoOverviewClearsReleasesWhenTagFallbackFails(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	d := openTestDB(t)
	repoID, err := d.UpsertRepo(ctx, db.GitHubRepoIdentity("github.com", "owner", "repo"))
	require.NoError(err)

	publishedAt := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	commitsSince := 9
	err = d.UpsertRepoOverview(ctx, repoID, db.RepoOverview{
		LatestRelease: &db.RepoRelease{
			TagName:     "v1.0.0",
			Name:        "Version 1.0.0",
			URL:         "https://github.com/owner/repo/releases/tag/v1.0.0",
			PublishedAt: &publishedAt,
		},
		Releases: []db.RepoRelease{{
			TagName:     "v1.0.0",
			Name:        "Version 1.0.0",
			URL:         "https://github.com/owner/repo/releases/tag/v1.0.0",
			PublishedAt: &publishedAt,
		}},
		CommitsSinceRelease: &commitsSince,
		CommitTimeline: []db.RepoCommitTimelinePoint{{
			SHA:         "abc123",
			Message:     "Old release timeline",
			CommittedAt: time.Date(2026, 3, 2, 10, 0, 0, 0, time.UTC),
		}},
	})
	require.NoError(err)

	client := &mockClient{
		listReleases: []*gh.RepositoryRelease{},
		listTagsErr:  errors.New("tags unavailable"),
	}
	syncer := NewSyncer(
		map[string]Client{"github.com": client},
		d,
		nil,
		nil,
		time.Minute,
		nil,
		nil,
	)

	syncer.syncRepoOverview(
		ctx,
		client,
		RepoRef{PlatformHost: "github.com", Owner: "owner", Name: "repo"},
		repoID,
		false,
	)

	summaries, err := d.ListRepoSummaries(ctx)
	require.NoError(err)
	require.Len(summaries, 1)
	overview := summaries[0].Overview

	assert.Nil(overview.LatestRelease)
	assert.Empty(overview.Releases)
	assert.Nil(overview.CommitsSinceRelease)
	assert.Empty(overview.CommitTimeline)
}

func TestSyncStoresForcePushEvent(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	d := openTestDB(t)
	now := time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC)
	commitSHA := "abc123def456"
	commitMsg := "fix: tighten validation"
	ciState := "success"

	mc := &mockClient{
		openPRs: []*gh.PullRequest{buildOpenPR(1, now)},
		commits: []*gh.RepositoryCommit{{
			SHA: &commitSHA,
			Commit: &gh.Commit{
				Message: &commitMsg,
				Author:  &gh.CommitAuthor{Name: new("dev"), Date: makeTimestamp(now.Add(-1 * time.Hour))},
			},
		}},
		timelineEvents: []PullRequestTimelineEvent{{
			EventType: "force_push",
			Actor:     "alice",
			BeforeSHA: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			AfterSHA:  "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
			Ref:       "feature",
			CreatedAt: now.Add(-30 * time.Minute),
		}},
		reviews:  []*gh.PullRequestReview{},
		comments: []*gh.IssueComment{},
		ciStatus: &gh.CombinedStatus{State: &ciState},
	}

	syncer := NewSyncer(map[string]Client{"github.com": mc}, d, nil, []RepoRef{{Owner: "owner", Name: "repo", PlatformHost: "github.com"}}, time.Minute, nil, testBudget(500))
	syncer.RunOnce(ctx)

	pr, err := d.GetMergeRequest(ctx, "owner", "repo", 1)
	require.NoError(err)
	require.NotNil(pr)

	events, err := d.ListMREvents(ctx, pr.ID)
	require.NoError(err)
	require.NotEmpty(events)

	var forcePush *db.MREvent
	for i := range events {
		if events[i].EventType == "force_push" {
			forcePush = &events[i]
			break
		}
	}
	require.NotNil(forcePush)
	assert.Equal("alice", forcePush.Author)
	assert.Equal("aaaaaaa -> bbbbbbb", forcePush.Summary)
	assert.Contains(forcePush.MetadataJSON, `"ref":"feature"`)
}

func TestSyncStoresPullRequestTimelineEvents(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	d := openTestDB(t)
	now := time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC)

	mc := &mockClient{
		openPRs:  []*gh.PullRequest{buildOpenPR(1, now)},
		comments: []*gh.IssueComment{},
		reviews:  []*gh.PullRequestReview{},
		commits:  []*gh.RepositoryCommit{},
		timelineEvents: []PullRequestTimelineEvent{
			{
				NodeID:            "CRE_1",
				EventType:         "cross_referenced",
				Actor:             "alice",
				CreatedAt:         now.Add(-3 * time.Minute),
				SourceType:        "Issue",
				SourceOwner:       "other",
				SourceRepo:        "repo",
				SourceNumber:      77,
				SourceTitle:       "Related bug",
				SourceURL:         "https://github.com/other/repo/issues/77",
				IsCrossRepository: true,
			},
			{
				NodeID:          "BRC_1",
				EventType:       "base_ref_changed",
				Actor:           "bob",
				CreatedAt:       now.Add(-2 * time.Minute),
				PreviousRefName: "main",
				CurrentRefName:  "release",
			},
			{
				NodeID:        "RTE_1",
				EventType:     "renamed_title",
				Actor:         "carol",
				CreatedAt:     now.Add(-1 * time.Minute),
				PreviousTitle: "Old",
				CurrentTitle:  "New",
			},
			{
				NodeID:               "CDE_1",
				EventType:            "comment_deleted",
				Actor:                "maintainer",
				DeletedCommentAuthor: "reviewer",
				CreatedAt:            now.Add(-30 * time.Second),
			},
		},
	}

	syncer := NewSyncer(map[string]Client{"github.com": mc}, d, nil, []RepoRef{{Owner: "owner", Name: "repo", PlatformHost: "github.com"}}, time.Minute, nil, testBudget(500))
	syncer.RunOnce(ctx)

	pr, err := d.GetMergeRequest(ctx, "owner", "repo", 1)
	require.NoError(err)
	require.NotNil(pr)

	events, err := d.ListMREvents(ctx, pr.ID)
	require.NoError(err)

	byType := map[string]db.MREvent{}
	for _, event := range events {
		byType[event.EventType] = event
	}
	assert.Contains(byType, "cross_referenced")
	assert.Contains(byType, "base_ref_changed")
	assert.Contains(byType, "renamed_title")
	assert.Contains(byType, "comment_deleted")
	assert.Contains(byType["cross_referenced"].MetadataJSON, `"source_title":"Related bug"`)
	assert.Equal("main -> release", byType["base_ref_changed"].Summary)
	assert.Equal(`"Old" -> "New"`, byType["renamed_title"].Summary)
	assert.Equal("deleted a comment from reviewer", byType["comment_deleted"].Summary)
}

func TestSyncIgnoresPullRequestTimelineFetchFailures(t *testing.T) {
	require := require.New(t)
	ctx := t.Context()
	d := openTestDB(t)
	now := time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC)
	commentID := int64(123)
	body := "human comment"
	user := &gh.User{Login: new("alice")}

	mc := &mockClient{
		openPRs: []*gh.PullRequest{buildOpenPR(1, now)},
		comments: []*gh.IssueComment{{
			ID:        &commentID,
			User:      user,
			Body:      &body,
			CreatedAt: makeTimestamp(now.Add(-time.Minute)),
		}},
		reviews:           []*gh.PullRequestReview{},
		commits:           []*gh.RepositoryCommit{},
		timelineEventsErr: errors.New("graphql unavailable"),
	}

	syncer := NewSyncer(map[string]Client{"github.com": mc}, d, nil, []RepoRef{{Owner: "owner", Name: "repo", PlatformHost: "github.com"}}, time.Minute, nil, testBudget(500))
	syncer.RunOnce(ctx)

	pr, err := d.GetMergeRequest(ctx, "owner", "repo", 1)
	require.NoError(err)
	require.NotNil(pr)
	events, err := d.ListMREvents(ctx, pr.ID)
	require.NoError(err)
	require.NotEmpty(events)
	require.Equal("issue_comment", events[0].EventType)
}

func TestSyncStoresPRLabels(t *testing.T) {
	require := require.New(t)
	ctx := t.Context()
	d := openTestDB(t)

	now := time.Date(2024, 6, 2, 12, 0, 0, 0, time.UTC)
	pr := buildOpenPR(1, now)
	pr.Labels = []*gh.Label{
		buildGitHubLabel(501, "needs-review", "Needs another reviewer", "fbca04", true),
	}

	mc := &mockClient{
		openPRs:  []*gh.PullRequest{pr},
		comments: []*gh.IssueComment{},
		reviews:  []*gh.PullRequestReview{},
		commits:  []*gh.RepositoryCommit{},
	}

	syncer := NewSyncer(map[string]Client{"github.com": mc}, d, nil, []RepoRef{{Owner: "owner", Name: "repo", PlatformHost: "github.com"}}, time.Minute, nil, testBudget(500))
	syncer.RunOnce(ctx)

	stored, err := d.GetMergeRequest(ctx, "owner", "repo", 1)
	require.NoError(err)
	require.NotNil(stored)
	require.Len(stored.Labels, 1)
	require.Equal("needs-review", stored.Labels[0].Name)
	require.Equal("Needs another reviewer", stored.Labels[0].Description)
	require.Equal("fbca04", stored.Labels[0].Color)
	require.True(stored.Labels[0].IsDefault)
	require.Equal(int64(501), stored.Labels[0].PlatformID)
	require.True(stored.Labels[0].UpdatedAt.Equal(now))
}

func TestSyncRefreshesRepoLabelCatalog(t *testing.T) {
	require := require.New(t)
	ctx := t.Context()
	d := openTestDB(t)

	client := &labelCatalogTestClient{
		mockClient: &mockClient{
			openPRs:   []*gh.PullRequest{},
			comments:  []*gh.IssueComment{},
			reviews:   []*gh.PullRequestReview{},
			commits:   []*gh.RepositoryCommit{},
			ciStatus:  &gh.CombinedStatus{},
			checkRuns: []*gh.CheckRun{},
		},
		labels: []*gh.Label{buildGitHubLabel(901, "triage", "Needs review", "fbca04", false)},
	}

	syncer := NewSyncer(map[string]Client{"github.com": client}, d, nil, []RepoRef{{Owner: "owner", Name: "repo", PlatformHost: "github.com"}}, time.Minute, nil, testBudget(500))
	syncer.RunOnce(ctx)

	repo, err := d.GetRepoByOwnerName(ctx, "owner", "repo")
	require.NoError(err)
	require.NotNil(repo)
	labels, freshness, err := d.ListRepoLabelCatalog(ctx, repo.ID)
	require.NoError(err)
	require.Len(labels, 1)
	require.Equal("triage", labels[0].Name)
	require.NotNil(freshness.CheckedAt)
	require.NotNil(freshness.SyncedAt)
	require.Equal(int32(1), client.calls.Load())
}

func TestSyncMRReplacesLabelsOnResync(t *testing.T) {
	require := require.New(t)
	ctx := t.Context()
	d := openTestDB(t)

	now := time.Date(2024, 6, 3, 12, 0, 0, 0, time.UTC)
	pr := buildOpenPR(1, now)
	pr.Labels = []*gh.Label{
		buildGitHubLabel(701, "bug", "Bug fix", "d73a4a", true),
	}

	mc := &mockClient{singlePR: pr, comments: []*gh.IssueComment{}, reviews: []*gh.PullRequestReview{}, commits: []*gh.RepositoryCommit{}}
	syncer := NewSyncer(map[string]Client{"github.com": mc}, d, nil, []RepoRef{{Owner: "owner", Name: "repo", PlatformHost: "github.com"}}, time.Minute, nil, testBudget(500))

	require.NoError(syncer.SyncMR(ctx, "owner", "repo", 1))

	pr.Labels = []*gh.Label{
		buildGitHubLabel(702, "feature", "New feature", "0e8a16", false),
	}
	pr.UpdatedAt = makeTimestamp(now.Add(time.Minute))

	require.NoError(syncer.SyncMR(ctx, "owner", "repo", 1))

	stored, err := d.GetMergeRequest(ctx, "owner", "repo", 1)
	require.NoError(err)
	require.NotNil(stored)
	require.Len(stored.Labels, 1)
	require.Equal("feature", stored.Labels[0].Name)
	require.Equal("New feature", stored.Labels[0].Description)
	require.Equal("0e8a16", stored.Labels[0].Color)
	require.False(stored.Labels[0].IsDefault)
	require.Equal(int64(702), stored.Labels[0].PlatformID)
}

func TestSyncIssueReplacesLabelsOnResync(t *testing.T) {
	require := require.New(t)
	ctx := t.Context()
	d := openTestDB(t)

	now := time.Date(2024, 6, 4, 12, 0, 0, 0, time.UTC)
	issueNumber := 42
	issueTitle := "broken thing"
	issueState := "open"
	issueURL := "https://github.com/owner/repo/issues/42"
	issueBody := ""
	issueID := int64(900042)
	issue := &gh.Issue{
		ID:        &issueID,
		Number:    &issueNumber,
		Title:     &issueTitle,
		State:     &issueState,
		HTMLURL:   &issueURL,
		Body:      &issueBody,
		CreatedAt: makeTimestamp(now),
		UpdatedAt: makeTimestamp(now),
		Labels: []*gh.Label{
			buildGitHubLabel(801, "bug", "Something is broken", "d73a4a", true),
		},
	}

	mc := &mockClient{getIssueFn: func(context.Context, string, string, int) (*gh.Issue, error) {
		return issue, nil
	}, comments: []*gh.IssueComment{}}
	syncer := NewSyncer(map[string]Client{"github.com": mc}, d, nil, []RepoRef{{Owner: "owner", Name: "repo", PlatformHost: "github.com"}}, time.Minute, nil, nil)

	require.NoError(syncer.SyncIssue(ctx, "owner", "repo", issueNumber))

	issue.Labels = []*gh.Label{
		buildGitHubLabel(802, "docs", "Documentation", "0075ca", false),
	}
	issue.UpdatedAt = makeTimestamp(now.Add(time.Minute))

	require.NoError(syncer.SyncIssue(ctx, "owner", "repo", issueNumber))

	stored, err := d.GetIssue(ctx, "owner", "repo", issueNumber)
	require.NoError(err)
	require.NotNil(stored)
	require.Len(stored.Labels, 1)
	require.Equal("docs", stored.Labels[0].Name)
	require.Equal("Documentation", stored.Labels[0].Description)
	require.Equal("0075ca", stored.Labels[0].Color)
	require.False(stored.Labels[0].IsDefault)
	require.Equal(int64(802), stored.Labels[0].PlatformID)
}

// TestSyncIssueNilUpdatedAt verifies refreshIssueTimeline
// tolerates a GitHub issue whose updated_at is null. Before
// the nil guard this panicked the sync goroutine when GitHub
// occasionally returned missing timestamps.
func TestSyncIssueNilUpdatedAt(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)
	ctx := t.Context()
	d := openTestDB(t)

	now := time.Date(2024, 6, 4, 12, 0, 0, 0, time.UTC)
	issueNumber := 7
	issueTitle := "no updated_at"
	issueState := "open"
	issueURL := "https://github.com/owner/repo/issues/7"
	issueBody := ""
	issueID := int64(900007)
	issue := &gh.Issue{
		ID:        &issueID,
		Number:    &issueNumber,
		Title:     &issueTitle,
		State:     &issueState,
		HTMLURL:   &issueURL,
		Body:      &issueBody,
		CreatedAt: makeTimestamp(now),
		UpdatedAt: nil, // the case under test
	}

	commentID := int64(9001)
	commentBody := "later comment"
	commentURL := "https://github.com/owner/repo/issues/7#issuecomment-9001"
	commentTime := now.Add(2 * time.Hour)
	commentAuthor := "alice"
	comment := &gh.IssueComment{
		ID:        &commentID,
		Body:      &commentBody,
		HTMLURL:   &commentURL,
		CreatedAt: makeTimestamp(commentTime),
		UpdatedAt: makeTimestamp(commentTime),
		User:      &gh.User{Login: &commentAuthor},
	}

	mc := &mockClient{
		getIssueFn: func(
			context.Context, string, string, int,
		) (*gh.Issue, error) {
			return issue, nil
		},
		comments: []*gh.IssueComment{comment},
	}
	syncer := NewSyncer(
		map[string]Client{"github.com": mc}, d, nil,
		[]RepoRef{{
			Owner:        "owner",
			Name:         "repo",
			PlatformHost: "github.com",
		}},
		time.Minute, nil, nil,
	)

	// Must not panic and must succeed.
	require.NoError(
		syncer.SyncIssue(ctx, "owner", "repo", issueNumber),
	)

	stored, err := d.GetIssue(ctx, "owner", "repo", issueNumber)
	require.NoError(err)
	require.NotNil(stored)
	// last_activity_at should track the comment timestamp
	// even though the issue had no updated_at.
	assert.Equal(commentTime.UTC(), stored.LastActivityAt.UTC())
}

// TestSyncIssueNilUpdatedAtNoComments verifies the CreatedAt
// fallback when UpdatedAt is nil and there are no comments.
// Without the fallback, lastActivity would be zero time and
// the issue would sort incorrectly in activity views.
func TestSyncIssueNilUpdatedAtNoComments(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)
	ctx := t.Context()
	d := openTestDB(t)

	created := time.Date(2024, 6, 4, 12, 0, 0, 0, time.UTC)
	issueNumber := 8
	issueTitle := "no updated_at, no comments"
	issueState := "open"
	issueURL := "https://github.com/owner/repo/issues/8"
	issueBody := ""
	issueID := int64(900008)
	issue := &gh.Issue{
		ID:        &issueID,
		Number:    &issueNumber,
		Title:     &issueTitle,
		State:     &issueState,
		HTMLURL:   &issueURL,
		Body:      &issueBody,
		CreatedAt: makeTimestamp(created),
		UpdatedAt: nil,
	}

	mc := &mockClient{
		getIssueFn: func(
			context.Context, string, string, int,
		) (*gh.Issue, error) {
			return issue, nil
		},
		comments: []*gh.IssueComment{},
	}
	syncer := NewSyncer(
		map[string]Client{"github.com": mc}, d, nil,
		[]RepoRef{{
			Owner:        "owner",
			Name:         "repo",
			PlatformHost: "github.com",
		}},
		time.Minute, nil, nil,
	)

	require.NoError(
		syncer.SyncIssue(ctx, "owner", "repo", issueNumber),
	)

	stored, err := d.GetIssue(ctx, "owner", "repo", issueNumber)
	require.NoError(err)
	require.NotNil(stored)
	assert.Equal(created.UTC(), stored.LastActivityAt.UTC(),
		"lastActivity should fall back to CreatedAt, not zero time")
}

// TestHostForConcurrentSetRepos verifies that concurrent
// SetRepos calls don't race with hostFor readers. Run under
// go test -race to catch regressions in the reposMu locking
// inside hostFor. Readers exercise all three hostFor return
// paths (tracked-with-host, tracked-with-empty-host, not-found)
// so a future refactor that reintroduces unsynchronized access
// on any branch is caught.
func TestHostForConcurrentSetRepos(t *testing.T) {
	syncer := NewSyncer(
		map[string]Client{"github.com": &mockClient{}}, nil, nil,
		[]RepoRef{{Owner: "o", Name: "r", PlatformHost: "github.com"}},
		time.Minute, nil, nil,
	)

	start := make(chan struct{})
	var wg sync.WaitGroup
	const iterations = 1000

	// Writer: rotate between three shapes so readers see each
	// hostFor branch at some point in the run.
	wg.Go(func() {
		<-start
		withHost := []RepoRef{
			{Owner: "o", Name: "r", PlatformHost: "ghe.example.com"},
			{Owner: "o2", Name: "r2", PlatformHost: "github.com"},
		}
		emptyHost := []RepoRef{
			{Owner: "o", Name: "r", PlatformHost: ""},
		}
		orig := []RepoRef{
			{Owner: "o", Name: "r", PlatformHost: "github.com"},
		}
		for range iterations {
			syncer.SetRepos(withHost)
			syncer.SetRepos(emptyHost)
			syncer.SetRepos(orig)
		}
	})

	// Readers: hit every unlocked hostFor caller, including
	// the not-found branch (ghost/missing) and the empty-host
	// branch driven by the writer's emptyHost state.
	for range 4 {
		wg.Go(func() {
			<-start
			for range iterations {
				_ = syncer.HostForRepo("o", "r")
				_ = syncer.HostForRepo("ghost", "missing")
				_ = syncer.IsTrackedRepo("o", "r")
			}
		})
	}

	close(start)
	wg.Wait()
}

func TestSyncIgnoresForcePushFetchFailures(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	d := openTestDB(t)
	now := time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC)
	commitSHA := "abc123def456"
	commitMsg := "fix: tighten validation"
	ciState := "success"
	commentBody := "Looks good to me"
	commentID := int64(41)
	commentURL := "https://github.com/owner/repo/pull/1#issuecomment-41"
	forcePushErr := errors.New("graphql unavailable")

	mc := &mockClient{
		openPRs: []*gh.PullRequest{buildOpenPR(1, now)},
		comments: []*gh.IssueComment{{
			ID:        &commentID,
			Body:      &commentBody,
			HTMLURL:   &commentURL,
			CreatedAt: makeTimestamp(now.Add(-45 * time.Minute)),
			User:      &gh.User{Login: new("alice")},
		}},
		commits: []*gh.RepositoryCommit{{
			SHA: &commitSHA,
			Commit: &gh.Commit{
				Message: &commitMsg,
				Author:  &gh.CommitAuthor{Name: new("dev"), Date: makeTimestamp(now.Add(-1 * time.Hour))},
			},
		}},
		forcePushEventsErr: forcePushErr,
		reviews:            []*gh.PullRequestReview{},
		ciStatus:           &gh.CombinedStatus{State: &ciState},
	}

	syncer := NewSyncer(map[string]Client{"github.com": mc}, d, nil, []RepoRef{{Owner: "owner", Name: "repo", PlatformHost: "github.com"}}, time.Minute, nil, testBudget(500))
	syncer.RunOnce(ctx)

	pr, err := d.GetMergeRequest(ctx, "owner", "repo", 1)
	require.NoError(err)
	require.NotNil(pr)

	events, err := d.ListMREvents(ctx, pr.ID)
	require.NoError(err)
	require.Len(events, 2)

	var sawCommit bool
	var sawComment bool
	for _, event := range events {
		if event.EventType == "commit" {
			sawCommit = true
		}
		if event.EventType == "issue_comment" {
			sawComment = true
		}
		assert.NotEqual("force_push", event.EventType)
	}
	assert.True(sawCommit)
	assert.True(sawComment)
	assert.Equal(1, pr.CommentCount)
	assert.Equal(now, pr.LastActivityAt)
}

func TestSyncSingleFlight(t *testing.T) {
	ctx := t.Context()
	d := openTestDB(t)

	callCount := 0
	mc := &mockClient{
		openPRs: []*gh.PullRequest{},
	}
	// Wrap in a counter client to detect calls.
	_ = mc

	syncer := NewSyncer(map[string]Client{"github.com": mc}, d, nil, []RepoRef{{Owner: "owner", Name: "repo", PlatformHost: "github.com"}}, time.Minute, nil, testBudget(500))

	// Simulate a concurrent run already in progress.
	syncer.running.Store(true)
	syncer.RunOnce(ctx) // should be a no-op
	syncer.running.Store(false)

	// Verify no DB side-effects: repo row should not exist because the RunOnce was skipped.
	repo, err := d.GetRepoByOwnerName(ctx, "owner", "repo")
	require.NoError(t, err)
	Assert.Nil(t, repo)

	_ = callCount
}

func TestSyncPreservesMergeableState(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	d := openTestDB(t)

	now := time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC)

	pr := buildOpenPR(1, now)
	additions := 10
	deletions := 5
	baseSHA := "base123"
	mergeableState := "dirty"
	pr.Additions = &additions
	pr.Deletions = &deletions
	pr.MergeableState = &mergeableState
	pr.Base.SHA = &baseSHA

	mc := &mockClient{
		openPRs:  []*gh.PullRequest{pr},
		comments: []*gh.IssueComment{},
		reviews:  []*gh.PullRequestReview{},
		commits:  []*gh.RepositoryCommit{},
	}

	syncer := NewSyncer(map[string]Client{"github.com": mc}, d, nil, []RepoRef{{Owner: "owner", Name: "repo", PlatformHost: "github.com"}}, time.Minute, nil, testBudget(500))

	// First sync: full fetch occurs, MergeableState is stored.
	syncer.RunOnce(ctx)

	stored, err := d.GetMergeRequest(ctx, "owner", "repo", 1)
	require.NoError(err)
	require.NotNil(stored)
	assert.Equal("dirty", stored.MergeableState)

	// Second sync: same UpdatedAt means no full fetch. The list endpoint
	// does not return MergeableState, so the preservation branch runs.
	// Reset the mock so the list PR has no MergeableState (as the real
	// list endpoint would return).
	listPR := buildOpenPR(1, now) // same UpdatedAt, no MergeableState set
	listPR.Additions = nil
	listPR.Deletions = nil
	listPR.Base.SHA = &baseSHA
	mc.openPRs = []*gh.PullRequest{listPR}
	// Ensure full fetch would return empty MergeableState if it ran.
	mc.getPullRequestFn = func(_ context.Context, _, _ string, _ int) (*gh.PullRequest, error) {
		p := buildOpenPR(1, now)
		p.Base.SHA = &baseSHA
		return p, nil
	}

	syncer.RunOnce(ctx)

	stored2, err := d.GetMergeRequest(ctx, "owner", "repo", 1)
	require.NoError(err)
	require.NotNil(stored2)
	assert.Equal("dirty", stored2.MergeableState, "MergeableState should be preserved when full fetch is skipped")
}

func TestIndexUpsertMergeRequestUpdatesKnownMergeableState(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	d := openTestDB(t)

	now := time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC)
	repoID, err := d.UpsertRepo(ctx, db.GitHubRepoIdentity("github.com", "owner", "repo"))
	require.NoError(err)

	baseMR := platform.MergeRequest{
		PlatformID:     1001,
		Number:         1,
		URL:            "https://github.com/owner/repo/pull/1",
		Title:          "Conflicted PR",
		State:          "open",
		HeadBranch:     "feature",
		BaseBranch:     "main",
		HeadSHA:        "abc123",
		CreatedAt:      now,
		UpdatedAt:      now,
		LastActivityAt: now,
	}
	_, err = d.UpsertMergeRequest(ctx, platform.DBMergeRequest(repoID, baseMR))
	require.NoError(err)

	syncer := NewSyncer(nil, d, nil, nil, time.Minute, nil, testBudget(500))
	incoming := baseMR
	incoming.MergeableState = "dirty"
	err = syncer.indexUpsertMergeRequest(
		ctx,
		RepoRef{Owner: "owner", Name: "repo", PlatformHost: "github.com"},
		repoID,
		incoming,
	)
	require.NoError(err)

	stored, err := d.GetMergeRequest(ctx, "owner", "repo", 1)
	require.NoError(err)
	require.NotNil(stored)
	assert.Equal("dirty", stored.MergeableState)
}

func TestIndexUpsertMergeRequestPreservesCachedCIForSameHead(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	d := openTestDB(t)

	now := time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC)
	repoID, err := d.UpsertRepo(ctx, db.GitHubRepoIdentity("github.com", "owner", "repo"))
	require.NoError(err)

	_, err = d.UpsertMergeRequest(ctx, &db.MergeRequest{
		RepoID:          repoID,
		PlatformID:      1001,
		Number:          1,
		URL:             "https://github.com/owner/repo/pull/1",
		Title:           "Cached CI PR",
		State:           "open",
		HeadBranch:      "feature",
		BaseBranch:      "main",
		PlatformHeadSHA: "same-head",
		CIStatus:        "failure",
		CIChecksJSON:    `[{"name":"build","status":"completed","conclusion":"failure"}]`,
		CreatedAt:       now,
		UpdatedAt:       now,
		LastActivityAt:  now,
	})
	require.NoError(err)

	syncer := NewSyncer(nil, d, nil, nil, time.Minute, nil, testBudget(500))
	err = syncer.indexUpsertMergeRequest(
		ctx,
		RepoRef{Owner: "owner", Name: "repo", PlatformHost: "github.com"},
		repoID,
		platform.MergeRequest{
			PlatformID:     1001,
			Number:         1,
			URL:            "https://github.com/owner/repo/pull/1",
			Title:          "Cached CI PR",
			State:          "open",
			HeadBranch:     "feature",
			BaseBranch:     "main",
			HeadSHA:        "same-head",
			CreatedAt:      now,
			UpdatedAt:      now.Add(time.Minute),
			LastActivityAt: now.Add(time.Minute),
		},
	)
	require.NoError(err)

	stored, err := d.GetMergeRequest(ctx, "owner", "repo", 1)
	require.NoError(err)
	require.NotNil(stored)
	assert.Equal("failure", stored.CIStatus)
	assert.Contains(stored.CIChecksJSON, "build")
}

func TestPreserveMergeableStateSkipsChangedOrUnknownHeadOrBase(t *testing.T) {
	assert := Assert.New(t)
	tests := []struct {
		name       string
		normalized db.MergeRequest
		existing   db.MergeRequest
	}{
		{
			name:       "head changed",
			normalized: db.MergeRequest{PlatformHeadSHA: "new-head"},
			existing:   db.MergeRequest{PlatformHeadSHA: "old-head", MergeableState: "dirty"},
		},
		{
			name:       "base changed",
			normalized: db.MergeRequest{PlatformHeadSHA: "same-head", PlatformBaseSHA: "new-base"},
			existing:   db.MergeRequest{PlatformHeadSHA: "same-head", PlatformBaseSHA: "old-base", MergeableState: "dirty"},
		},
		{
			name:       "refreshed head missing",
			normalized: db.MergeRequest{PlatformBaseSHA: "same-base"},
			existing:   db.MergeRequest{PlatformHeadSHA: "same-head", PlatformBaseSHA: "same-base", MergeableState: "dirty"},
		},
		{
			name:       "existing head missing",
			normalized: db.MergeRequest{PlatformHeadSHA: "same-head", PlatformBaseSHA: "same-base"},
			existing:   db.MergeRequest{PlatformBaseSHA: "same-base", MergeableState: "dirty"},
		},
		{
			name:       "refreshed base missing",
			normalized: db.MergeRequest{PlatformHeadSHA: "same-head"},
			existing:   db.MergeRequest{PlatformHeadSHA: "same-head", PlatformBaseSHA: "same-base", MergeableState: "dirty"},
		},
		{
			name:       "existing base missing",
			normalized: db.MergeRequest{PlatformHeadSHA: "same-head", PlatformBaseSHA: "same-base"},
			existing:   db.MergeRequest{PlatformHeadSHA: "same-head", MergeableState: "dirty"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			preserveMergeableStateIfOmitted(&tt.normalized, &tt.existing)
			assert.Empty(tt.normalized.MergeableState)
		})
	}
}

func TestPreserveCIStateSkipsChangedOrUnknownHead(t *testing.T) {
	assert := Assert.New(t)
	tests := []struct {
		name       string
		normalized db.MergeRequest
		existing   db.MergeRequest
	}{
		{
			name:       "head changed",
			normalized: db.MergeRequest{PlatformHeadSHA: "new-head"},
			existing:   db.MergeRequest{PlatformHeadSHA: "old-head", CIStatus: "success", CIChecksJSON: `[{"name":"build"}]`},
		},
		{
			name:       "refreshed head missing",
			normalized: db.MergeRequest{},
			existing:   db.MergeRequest{PlatformHeadSHA: "same-head", CIStatus: "success", CIChecksJSON: `[{"name":"build"}]`},
		},
		{
			name:       "existing head missing",
			normalized: db.MergeRequest{PlatformHeadSHA: "same-head"},
			existing:   db.MergeRequest{CIStatus: "success", CIChecksJSON: `[{"name":"build"}]`},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			preserveCIStateIfOmitted(&tt.normalized, &tt.existing)
			assert.Empty(tt.normalized.CIStatus)
			assert.Empty(tt.normalized.CIChecksJSON)
		})
	}
}

func TestPreserveCIStateKeepsOmittedStateForMatchingHead(t *testing.T) {
	assert := Assert.New(t)
	normalized := db.MergeRequest{PlatformHeadSHA: "same-head"}
	existing := db.MergeRequest{
		PlatformHeadSHA: "same-head",
		CIStatus:        "success",
		CIChecksJSON:    `[{"name":"build","status":"completed","conclusion":"success"}]`,
	}

	preserveCIStateIfOmitted(&normalized, &existing)

	assert.Equal("success", normalized.CIStatus)
	assert.Contains(normalized.CIChecksJSON, "build")
}

func TestPreserveCIStateClearsCachedChecksWhenStatusChanges(t *testing.T) {
	assert := Assert.New(t)
	normalized := db.MergeRequest{
		PlatformHeadSHA: "same-head",
		CIStatus:        "success",
	}
	existing := db.MergeRequest{
		PlatformHeadSHA: "same-head",
		CIStatus:        "failure",
		CIChecksJSON:    `[{"name":"build","status":"completed","conclusion":"failure"}]`,
	}

	needsCIDetailRefresh := preserveCIStateIfOmitted(&normalized, &existing)

	assert.True(needsCIDetailRefresh)
	assert.Equal("success", normalized.CIStatus)
	assert.Empty(normalized.CIChecksJSON)
}

func TestPreserveMergeableStateKeepsOmittedStateForMatchingKnownIdentity(t *testing.T) {
	assert := Assert.New(t)
	normalized := db.MergeRequest{
		PlatformHeadSHA: "same-head",
		PlatformBaseSHA: "same-base",
	}
	existing := db.MergeRequest{
		PlatformHeadSHA: "same-head",
		PlatformBaseSHA: "same-base",
		MergeableState:  "dirty",
	}

	preserveMergeableStateIfOmitted(&normalized, &existing)

	assert.Equal("dirty", normalized.MergeableState)
}

func TestSyncTriggersFullFetchForUnknownMergeableState(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	d := openTestDB(t)

	now := time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC)

	// Build a list PR with diff stats set so the zero-stats condition
	// doesn't trigger the full fetch independently.
	listPR := buildOpenPR(1, now)
	additions := 10
	deletions := 5
	listPR.Additions = &additions
	listPR.Deletions = &deletions

	// First full-fetch returns "unknown".
	fetchCount := 0
	mc := &mockClient{
		openPRs:  []*gh.PullRequest{listPR},
		comments: []*gh.IssueComment{},
		reviews:  []*gh.PullRequestReview{},
		commits:  []*gh.RepositoryCommit{},
	}
	mc.getPullRequestFn = func(_ context.Context, _, _ string, _ int) (*gh.PullRequest, error) {
		fetchCount++
		p := buildOpenPR(1, now)
		a, d2 := 10, 5
		p.Additions = &a
		p.Deletions = &d2
		state := "unknown"
		if fetchCount >= 2 {
			state = "clean"
		}
		p.MergeableState = &state
		return p, nil
	}

	syncer := NewSyncer(map[string]Client{"github.com": mc}, d, nil, []RepoRef{{Owner: "owner", Name: "repo", PlatformHost: "github.com"}}, time.Minute, nil, testBudget(500))

	// First sync: index scan upserts list data, detail drain fetches
	// full PR (returns "unknown" MergeableState).
	syncer.RunOnce(ctx)

	stored, err := d.GetMergeRequest(ctx, "owner", "repo", 1)
	require.NoError(err)
	require.NotNil(stored)
	assert.Equal("unknown", stored.MergeableState)
	assert.Equal(1, fetchCount, "first sync should trigger one full fetch via detail drain")
}

func TestSyncPreservesFieldsOnFullFetchFailure(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	d := openTestDB(t)

	now := time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC)

	// First sync: full fetch succeeds, sets fields.
	pr := buildOpenPR(1, now)
	additions := 10
	deletions := 5
	baseSHA := "base123"
	mergeableState := "dirty"
	pr.Additions = &additions
	pr.Deletions = &deletions
	pr.MergeableState = &mergeableState
	pr.Base.SHA = &baseSHA

	mc := &mockClient{
		openPRs:  []*gh.PullRequest{pr},
		comments: []*gh.IssueComment{},
		reviews:  []*gh.PullRequestReview{},
		commits:  []*gh.RepositoryCommit{},
	}

	syncer := NewSyncer(map[string]Client{"github.com": mc}, d, nil, []RepoRef{{Owner: "owner", Name: "repo", PlatformHost: "github.com"}}, time.Minute, nil, testBudget(500))
	syncer.RunOnce(ctx)

	stored, err := d.GetMergeRequest(ctx, "owner", "repo", 1)
	require.NoError(err)
	require.Equal("dirty", stored.MergeableState)
	require.Equal(10, stored.Additions)

	// Second sync: bump UpdatedAt so needsTimeline triggers, but full
	// fetch fails. Fields from the existing row should be preserved.
	later := now.Add(time.Hour)
	listPR := buildOpenPR(1, later)
	listPR.Base.SHA = &baseSHA
	mc.openPRs = []*gh.PullRequest{listPR}
	mc.getPullRequestFn = func(_ context.Context, _, _ string, _ int) (*gh.PullRequest, error) {
		return nil, fmt.Errorf("transient network error")
	}

	syncer.RunOnce(ctx)

	stored2, err := d.GetMergeRequest(ctx, "owner", "repo", 1)
	require.NoError(err)
	assert.Equal("dirty", stored2.MergeableState, "MergeableState should survive a failed full fetch")
	assert.Equal(10, stored2.Additions, "Additions should survive a failed full fetch")
	assert.Equal(5, stored2.Deletions, "Deletions should survive a failed full fetch")
}

func TestSyncStatusUpdated(t *testing.T) {
	assert := Assert.New(t)
	d := openTestDB(t)

	mc := &mockClient{
		openPRs:  []*gh.PullRequest{},
		comments: []*gh.IssueComment{},
		reviews:  []*gh.PullRequestReview{},
		commits:  []*gh.RepositoryCommit{},
	}

	syncer := NewSyncer(map[string]Client{"github.com": mc}, d, nil, []RepoRef{{Owner: "owner", Name: "repo", PlatformHost: "github.com"}}, time.Minute, nil, testBudget(500))

	before := time.Now()
	syncer.RunOnce(t.Context())
	after := time.Now()

	status := syncer.Status()
	assert.False(status.Running)
	assert.False(status.LastRunAt.IsZero())
	assert.Condition(func() bool {
		return !status.LastRunAt.Before(before) && !status.LastRunAt.After(after)
	}, "status.LastRunAt %v should be between %v and %v", status.LastRunAt, before, after)
	assert.Empty(status.LastError)
}

func setTestLocalEDT(t *testing.T) {
	t.Helper()
	//nolint:forbidigo // Tests intentionally override the process local zone to verify UTC normalization.
	oldLocal := time.Local
	//nolint:forbidigo // Tests intentionally override the process local zone to verify UTC normalization.
	time.Local = time.FixedZone("EDT", -4*60*60)
	t.Cleanup(func() {
		//nolint:forbidigo // Tests intentionally restore the overridden process local zone.
		time.Local = oldLocal
	})
}

func TestSyncStatusUpdatedUsesUTC(t *testing.T) {
	setTestLocalEDT(t)

	d := openTestDB(t)
	mc := &mockClient{
		openPRs:  []*gh.PullRequest{},
		comments: []*gh.IssueComment{},
		reviews:  []*gh.PullRequestReview{},
		commits:  []*gh.RepositoryCommit{},
	}

	syncer := NewSyncer(map[string]Client{"github.com": mc}, d, nil, []RepoRef{{Owner: "owner", Name: "repo", PlatformHost: "github.com"}}, time.Minute, nil, testBudget(500))
	syncer.RunOnce(t.Context())

	status := syncer.Status()
	Assert.Equal(t, time.UTC, status.LastRunAt.Location())
}

// syncedWriter wraps an io.Writer with a mutex for safe concurrent
// writes from multiple goroutines, used to capture slog output in
// tests where workers run in parallel.
type syncedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (sw *syncedWriter) Write(p []byte) (int, error) {
	sw.mu.Lock()
	defer sw.mu.Unlock()
	return sw.w.Write(p)
}

// TestSyncMRReturnsErrorOnNilPullRequest verifies SyncMR returns
// a clear error when a Client returns (nil, nil) from
// GetPullRequest, instead of dereferencing nil in NormalizePR.
func TestSyncMRReturnsErrorOnNilPullRequest(t *testing.T) {
	require := require.New(t)
	d := openTestDB(t)

	mc := &mockClient{
		getPullRequestFn: func(_ context.Context, _, _ string, _ int) (*gh.PullRequest, error) {
			return nil, nil
		},
	}
	syncer := NewSyncer(
		map[string]Client{"github.com": mc}, d, nil,
		[]RepoRef{{Owner: "owner", Name: "repo", PlatformHost: "github.com"}},
		time.Minute, nil, nil,
	)

	err := syncer.SyncMR(t.Context(), "owner", "repo", 1)
	require.Error(err)
	require.Contains(err.Error(), "nil pull request")
}

// TestRunOnceSyncOpenMRSurvivesNilFullPR verifies the periodic
// sync path does not panic when GetPullRequest returns (nil, nil)
// during syncOpenMR's full-PR fetch. It must fall back to the
// list-derived data and complete the sync.
func TestRunOnceSyncOpenMRSurvivesNilFullPR(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	d := openTestDB(t)

	now := time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC)
	mc := &mockClient{
		openPRs: []*gh.PullRequest{buildOpenPR(7, now)},
		getPullRequestFn: func(_ context.Context, _, _ string, _ int) (*gh.PullRequest, error) {
			// Contract violation: return (nil, nil). Periodic
			// sync must not panic on this.
			return nil, nil
		},
	}
	syncer := NewSyncer(
		map[string]Client{"github.com": mc}, d, nil,
		[]RepoRef{{Owner: "owner", Name: "repo", PlatformHost: "github.com"}},
		time.Minute, nil, nil,
	)

	// Must complete without panic.
	syncer.RunOnce(ctx)

	// The list-derived PR should still be persisted because the
	// nil full-PR fetch is non-fatal.
	pr, err := d.GetMergeRequest(ctx, "owner", "repo", 7)
	require.NoError(err)
	require.NotNil(pr)
	assert.Equal(7, pr.Number)
}

// trackingClient records every ListOpenPullRequests invocation
// so a test can assert that runWorker did not start any work.
type trackingClient struct {
	mockClient
	listCalls atomic.Int32
}

func (c *trackingClient) ListOpenPullRequests(
	_ context.Context, _, _ string,
) ([]*gh.PullRequest, error) {
	c.listCalls.Add(1)
	return nil, nil
}

// TestRunWorkerBailsOnCanceledCtx verifies the worker-side ctx
// check fires before any work is done. The dispatch race fix
// pre-checks ctx before the select, but a cancel can still land
// in the micro-window between pre-check and send and Go may pick
// the send branch. The worker must then discard the repo before
// logging "syncing repo" or calling syncRepo.
//
// This test exercises that path directly: it pre-loads a buffered
// work channel, cancels ctx, and calls runWorker with the
// canceled ctx. With the worker-side check the function returns
// without invoking the client; without the check it would log
// "syncing repo" and increment the completed counter.
func TestRunWorkerBailsOnCanceledCtx(t *testing.T) {
	assert := Assert.New(t)
	d := openTestDB(t)

	var buf bytes.Buffer
	sw := &syncedWriter{w: &buf}
	h := slog.NewTextHandler(sw, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	})
	orig := slog.Default()
	slog.SetDefault(slog.New(h))
	t.Cleanup(func() { slog.SetDefault(orig) })

	tc := &trackingClient{}
	syncer := NewSyncer(
		map[string]Client{"github.com": tc}, d, nil,
		[]RepoRef{{Owner: "o", Name: "r", PlatformHost: "github.com"}},
		time.Minute, nil, nil,
	)

	// Pre-load three repos so the worker would naturally drain
	// all three if the ctx check were missing.
	work := make(chan repoWork, 3)
	for i := range 3 {
		work <- repoWork{
			index: i,
			repo: RepoRef{
				Owner:        "o",
				Name:         fmt.Sprintf("r%d", i),
				PlatformHost: "github.com",
			},
		}
	}
	close(work)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	var (
		completed atomic.Int32
		maxShown  atomic.Int32
		errMu     sync.Mutex
		lastErr   string
		canceled  atomic.Bool
	)
	state := &runState{
		completed: &completed,
		maxShown:  &maxShown,
		errMu:     &errMu,
		lastErr:   &lastErr,
		canceled:  &canceled,
		total:     3,
		results:   make([]RepoSyncResult, 3),
	}
	syncer.runWorker(ctx, work, state)

	sw.mu.Lock()
	output := buf.String()
	sw.mu.Unlock()

	assert.Zero(strings.Count(output, `msg="syncing repo"`),
		"runWorker must not log 'syncing repo' when ctx is canceled")
	assert.Zero(int(completed.Load()),
		"runWorker must not increment completed when ctx is canceled")
	assert.Zero(int(tc.listCalls.Load()),
		"runWorker must not call the GitHub client when ctx is canceled")
	assert.Empty(lastErr, "runWorker must not record an error when bailing on ctx")
}

// dedupGetUserClient blocks on GetUser calls to force concurrent
// display-name lookups into a race. It also tracks how many
// GetUser calls actually hit it.
type dedupGetUserClient struct {
	mockClient
	getUserCount atomic.Int32
	block        chan struct{}
}

func (c *dedupGetUserClient) GetUser(
	_ context.Context, login string,
) (*gh.User, error) {
	c.getUserCount.Add(1)
	<-c.block
	name := "Display " + login
	return &gh.User{Login: &login, Name: &name}, nil
}

func TestResolveDisplayNameDedupsConcurrentLookups(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	d := openTestDB(t)

	author := "alice"
	now := time.Date(2026, 4, 8, 12, 0, 0, 0, time.UTC)

	// Build two PRs in two repos, both authored by "alice".
	buildAuthoredPR := func(num int) *gh.PullRequest {
		pr := buildOpenPR(num, now)
		pr.User = &gh.User{Login: &author}
		return pr
	}

	mc := &dedupGetUserClient{block: make(chan struct{})}
	mc.openPRs = []*gh.PullRequest{
		buildAuthoredPR(1),
		buildAuthoredPR(2),
	}

	syncer := NewSyncer(
		map[string]Client{"github.com": mc}, d, nil,
		[]RepoRef{
			{Owner: "o", Name: "r1", PlatformHost: "github.com"},
			{Owner: "o", Name: "r2", PlatformHost: "github.com"},
		},
		time.Minute, nil, nil,
	)
	syncer.SetParallelism(2)

	done := make(chan struct{})
	go func() {
		syncer.RunOnce(t.Context())
		close(done)
	}()

	// Wait until at least one worker has entered GetUser. Sleeping
	// does not prove the second worker has arrived yet, but the
	// blocked fn holds the singleflight slot open until we release
	// it, so any arriving worker will be coalesced.
	require.Eventually(func() bool {
		return mc.getUserCount.Load() >= 1
	}, 2*time.Second, 5*time.Millisecond,
		"no worker reached GetUser")

	// Give the second worker plenty of time to enter singleflight.
	time.Sleep(100 * time.Millisecond)

	close(mc.block)

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		require.Fail("RunOnce did not complete")
	}

	assert.Equal(int32(1), mc.getUserCount.Load(),
		"concurrent display-name lookups for same author "+
			"should coalesce into one GetUser call")
}

func TestIsTrackedRepo(t *testing.T) {
	assert := Assert.New(t)
	database := openTestDB(t)
	mc := &mockClient{}

	syncer := NewSyncer(map[string]Client{"github.com": mc}, database, nil, []RepoRef{
		{Owner: "acme", Name: "widget", PlatformHost: "github.com"},
		{Owner: "corp", Name: "lib", PlatformHost: "github.com"},
	}, time.Minute, nil, nil)

	assert.True(syncer.IsTrackedRepo("acme", "widget"))
	assert.True(syncer.IsTrackedRepo("Acme", "Widget"))
	assert.True(syncer.IsTrackedRepo("corp", "lib"))
	assert.False(syncer.IsTrackedRepo("acme", "other"))
	assert.False(syncer.IsTrackedRepo("nobody", "widget"))
}

func TestClientForRepoMatchesCaseInsensitively(t *testing.T) {
	require := require.New(t)
	database := openTestDB(t)
	mc := &mockClient{}

	syncer := NewSyncer(map[string]Client{"github.com": mc}, database, nil, []RepoRef{
		{Owner: "Acme", Name: "Widget", PlatformHost: "github.com"},
	}, time.Minute, nil, nil)

	client, err := syncer.ClientForRepo("acme", "widget")
	require.NoError(err)
	require.Same(mc, client)
}

func TestSyncerClientLookupReportsMissingProvider(t *testing.T) {
	require := require.New(t)
	syncer := NewSyncer(nil, openTestDB(t), nil, []RepoRef{{
		Platform:     platform.KindGitLab,
		PlatformHost: "gitlab.com",
		Owner:        "acme",
		Name:         "widget",
	}}, time.Minute, nil, nil)

	_, err := syncer.mergeRequestReaderFor(RepoRef{
		Platform:     platform.KindGitLab,
		PlatformHost: "gitlab.com",
		Owner:        "acme",
		Name:         "widget",
	})

	var platformErr *platform.Error
	require.ErrorAs(err, &platformErr)
	require.ErrorIs(err, platform.ErrProviderNotConfigured)
	require.Equal(platform.ErrCodeProviderNotConfigured, platformErr.Code)
	require.Equal(platform.KindGitLab, platformErr.Provider)
	require.Equal("gitlab.com", platformErr.PlatformHost)
}

func TestSyncerClientLookupReportsMissingOptionalReader(t *testing.T) {
	require := require.New(t)
	syncer := NewSyncer(nil, openTestDB(t), nil, []RepoRef{{
		Platform:     platform.KindGitLab,
		PlatformHost: "gitlab.com",
		Owner:        "acme",
		Name:         "widget",
	}}, time.Minute, nil, nil)
	registry, err := platform.NewRegistry(syncTestProvider{
		kind: platform.KindGitLab,
		host: "gitlab.com",
	})
	require.NoError(err)
	syncer.clients = registry

	_, err = syncer.mergeRequestReaderFor(RepoRef{
		Platform:     platform.KindGitLab,
		PlatformHost: "gitlab.com",
		Owner:        "acme",
		Name:         "widget",
	})

	var platformErr *platform.Error
	require.ErrorAs(err, &platformErr)
	require.ErrorIs(err, platform.ErrUnsupportedCapability)
	require.Equal(platform.ErrCodeUnsupportedCapability, platformErr.Code)
	require.Equal("read_merge_requests", platformErr.Capability)
}

func TestSyncItemByNumber_Issue(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	database := openTestDB(t)
	ctx := t.Context()

	number := 42
	title := "Bug report"
	state := "closed"
	author := "testuser"
	now := time.Now()
	ghTime := &gh.Timestamp{Time: now}

	mc := &mockClient{
		getIssueFn: func(_ context.Context, _, _ string, n int) (*gh.Issue, error) {
			if n != number {
				return nil, fmt.Errorf("unexpected number %d", n)
			}
			return &gh.Issue{
				ID:        new(int64(9999)),
				Number:    &number,
				Title:     &title,
				State:     &state,
				User:      &gh.User{Login: &author},
				HTMLURL:   new("https://github.com/acme/widget/issues/42"),
				CreatedAt: ghTime,
				UpdatedAt: ghTime,
			}, nil
		},
	}

	syncer := NewSyncer(map[string]Client{"github.com": mc}, database, nil, []RepoRef{
		{Owner: "acme", Name: "widget", PlatformHost: "github.com"},
	}, time.Minute, nil, nil)

	itemType, err := syncer.SyncItemByNumber(ctx, "acme", "widget", number)
	require.NoError(err)
	assert.Equal("issue", itemType)

	issue, err := database.GetIssue(ctx, "acme", "widget", number)
	require.NoError(err)
	assert.NotNil(issue)
	assert.Equal(title, issue.Title)
}

func TestSyncItemByNumber_PR(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	database := openTestDB(t)
	ctx := t.Context()

	number := 10
	title := "Add feature"
	state := "open"
	author := "testuser"
	now := time.Now()
	ghTime := &gh.Timestamp{Time: now}
	prURL := "https://github.com/acme/widget/pull/10"

	mc := &mockClient{
		getIssueFn: func(_ context.Context, _, _ string, n int) (*gh.Issue, error) {
			return &gh.Issue{
				ID:      new(int64(8888)),
				Number:  &number,
				Title:   &title,
				State:   &state,
				User:    &gh.User{Login: &author},
				HTMLURL: new(prURL),
				PullRequestLinks: &gh.PullRequestLinks{
					URL: &prURL,
				},
				CreatedAt: ghTime,
				UpdatedAt: ghTime,
			}, nil
		},
		singlePR: &gh.PullRequest{
			ID:      new(int64(8888)),
			Number:  &number,
			Title:   &title,
			State:   &state,
			User:    &gh.User{Login: &author},
			HTMLURL: &prURL,
			Head: &gh.PullRequestBranch{
				Ref: new("feature"),
				SHA: new("abc123"),
			},
			Base:      &gh.PullRequestBranch{Ref: new("main")},
			CreatedAt: ghTime,
			UpdatedAt: ghTime,
		},
	}

	syncer := NewSyncer(map[string]Client{"github.com": mc}, database, nil, []RepoRef{
		{Owner: "acme", Name: "widget", PlatformHost: "github.com"},
	}, time.Minute, nil, nil)

	itemType, err := syncer.SyncItemByNumber(ctx, "acme", "widget", number)
	require.NoError(err)
	assert.Equal("pr", itemType)

	pr, err := database.GetMergeRequest(ctx, "acme", "widget", number)
	require.NoError(err)
	assert.NotNil(pr)
	assert.Equal(title, pr.Title)
}

func TestRepoFailKeyIncludesProvider(t *testing.T) {
	assert := Assert.New(t)
	githubRepo := RepoRef{
		Platform:     platform.KindGitHub,
		PlatformHost: "code.example.com",
		Owner:        "acme",
		Name:         "widget",
	}
	gitlabRepo := RepoRef{
		Platform:     platform.KindGitLab,
		PlatformHost: "code.example.com",
		Owner:        "acme",
		Name:         "widget",
	}

	assert.NotEqual(repoFailKey(githubRepo), repoFailKey(gitlabRepo))
	assert.Equal("github/code.example.com/acme/widget", repoFailKey(githubRepo))
	assert.Equal("gitlab/code.example.com/acme/widget", repoFailKey(gitlabRepo))
}

func TestPlatformRepoRefPreservesFullProviderRef(t *testing.T) {
	assert := Assert.New(t)
	repo := RepoRef{
		Platform:           platform.KindGitLab,
		PlatformHost:       "gitlab.example.com",
		Owner:              "Group/SubGroup",
		Name:               "Project",
		RepoPath:           "Group/SubGroup/Project",
		PlatformRepoID:     42,
		PlatformExternalID: "gid://gitlab/Project/42",
		WebURL:             "https://gitlab.example.com/Group/SubGroup/Project",
		CloneURL:           "https://gitlab.example.com/Group/SubGroup/Project.git",
		DefaultBranch:      "main",
	}

	ref := platformRepoRef(repo)

	assert.Equal(platform.KindGitLab, ref.Platform)
	assert.Equal("gitlab.example.com", ref.Host)
	assert.Equal("Group/SubGroup", ref.Owner)
	assert.Equal("Project", ref.Name)
	assert.Equal("Group/SubGroup/Project", ref.RepoPath)
	assert.Equal(int64(42), ref.PlatformID)
	assert.Equal("gid://gitlab/Project/42", ref.PlatformExternalID)
	assert.Equal("https://gitlab.example.com/Group/SubGroup/Project", ref.WebURL)
	assert.Equal("https://gitlab.example.com/Group/SubGroup/Project.git", ref.CloneURL)
	assert.Equal("main", ref.DefaultBranch)
}

func TestCloneRemoteURLUsesProviderCloneURLAndRepoPath(t *testing.T) {
	assert := Assert.New(t)

	gitlabRepo := RepoRef{
		Platform:     platform.KindGitLab,
		PlatformHost: "gitlab.example.com",
		Owner:        "Group/SubGroup",
		Name:         "Project",
		RepoPath:     "Group/SubGroup/Project",
		CloneURL:     "https://gitlab.example.com/Group/SubGroup/Project.git",
	}
	assert.Equal(
		"https://gitlab.example.com/Group/SubGroup/Project.git",
		cloneRemoteURL(gitlabRepo),
	)

	gitlabRepo.CloneURL = ""
	assert.Equal(
		"https://gitlab.example.com/Group/SubGroup/Project.git",
		cloneRemoteURL(gitlabRepo),
	)

	githubRepo := RepoRef{
		Platform:     platform.KindGitHub,
		PlatformHost: "github.com",
		Owner:        "acme",
		Name:         "widget",
	}
	assert.Equal("https://github.com/acme/widget.git", cloneRemoteURL(githubRepo))
}

func TestFetcherForSkipsNonGitHubRepoOnSameHost(t *testing.T) {
	assert := Assert.New(t)
	d := openTestDB(t)
	fetcher := NewGraphQLFetcher("token", "code.example.com", nil, nil)
	syncer := NewSyncer(nil, d, nil, nil, time.Minute, nil, nil)
	syncer.SetFetchers(map[string]*GraphQLFetcher{
		"code.example.com": fetcher,
	})

	assert.Same(fetcher, syncer.fetcherFor(RepoRef{
		Platform:     platform.KindGitHub,
		PlatformHost: "code.example.com",
		Owner:        "acme",
		Name:         "widget",
	}))
	assert.Nil(syncer.fetcherFor(RepoRef{
		Platform:     platform.KindGitLab,
		PlatformHost: "code.example.com",
		Owner:        "acme",
		Name:         "widget",
	}))
}

func TestSyncRepoUsesProviderIDToPreserveRenamedRepo(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	d := openTestDB(t)
	originalID, err := d.UpsertRepoByProviderID(ctx, db.RepoIdentity{
		Platform:       "gitlab",
		PlatformHost:   "gitlab.example.com",
		PlatformRepoID: "gid://gitlab/Project/42",
		Owner:          "old-group",
		Name:           "old-project",
		RepoPath:       "old-group/old-project",
	})
	require.NoError(err)
	repo := RepoRef{
		Platform:           platform.KindGitLab,
		PlatformHost:       "gitlab.example.com",
		Owner:              "new-group",
		Name:               "new-project",
		RepoPath:           "new-group/new-project",
		PlatformExternalID: "gid://gitlab/Project/42",
	}
	provider := &syncTestReadProvider{
		syncTestProvider: syncTestProvider{
			kind: platform.KindGitLab,
			host: "gitlab.example.com",
		},
	}
	registry, err := platform.NewRegistry(provider)
	require.NoError(err)
	syncer := NewSyncerWithRegistry(registry, d, nil, []RepoRef{repo}, time.Minute, nil, nil)

	require.NoError(syncer.syncRepo(ctx, repo))

	repos, err := d.ListRepos(ctx)
	require.NoError(err)
	require.Len(repos, 1)
	assert.Equal(originalID, repos[0].ID)
	assert.Equal("new-group", repos[0].Owner)
	assert.Equal("new-project", repos[0].Name)
	assert.Equal("new-group/new-project", repos[0].RepoPath)
}

func TestSyncRepoUpdatesViewerCanMergeWithoutMergeSettings(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	d := openTestDB(t)
	repoID, err := d.UpsertRepo(ctx, db.RepoIdentity{
		Platform:     "gitlab",
		PlatformHost: "gitlab.example.com",
		Owner:        "group",
		Name:         "project",
		RepoPath:     "group/project",
	})
	require.NoError(err)
	require.NoError(d.UpdateRepoSettings(ctx, repoID, true, false, true, true))
	syncer := &Syncer{db: d}
	viewerCanMerge := false

	syncer.updateRepoSettingsFromProviderRepo(ctx, repoID, platform.Repository{
		ViewerCanMerge: &viewerCanMerge,
	})

	repos, err := d.ListRepos(ctx)
	require.NoError(err)
	require.Len(repos, 1)
	assert.True(repos[0].AllowSquashMerge)
	assert.False(repos[0].AllowMergeCommit)
	assert.True(repos[0].AllowRebaseMerge)
	assert.False(repos[0].ViewerCanMerge)
}

func TestRefreshRepoSettingsPreservesViewerCanMergeWhenGitHubOmitsPermissions(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	d := openTestDB(t)
	repoID, err := d.UpsertRepo(ctx, db.RepoIdentity{
		Platform:     "github",
		PlatformHost: "github.com",
		Owner:        "acme",
		Name:         "widgets",
		RepoPath:     "acme/widgets",
	})
	require.NoError(err)
	require.NoError(d.UpdateRepoSettings(ctx, repoID, true, true, true, false))
	client := &mockClient{getRepositoryFn: func(
		context.Context,
		string,
		string,
	) (*gh.Repository, error) {
		return &gh.Repository{
			Name:             new("widgets"),
			AllowSquashMerge: new(false),
			AllowMergeCommit: new(true),
			AllowRebaseMerge: new(false),
		}, nil
	}}
	syncer := NewSyncer(map[string]Client{"github.com": client}, d, nil, []RepoRef{{
		Platform:     platform.KindGitHub,
		PlatformHost: "github.com",
		Owner:        "acme",
		Name:         "widgets",
		RepoPath:     "acme/widgets",
	}}, time.Minute, nil, nil)

	syncer.refreshRepoSettings(ctx, syncer.repos[0], repoID, nil)

	repos, err := d.ListRepos(ctx)
	require.NoError(err)
	require.Len(repos, 1)
	assert.False(repos[0].AllowSquashMerge)
	assert.True(repos[0].AllowMergeCommit)
	assert.False(repos[0].AllowRebaseMerge)
	assert.False(repos[0].ViewerCanMerge)
}

func TestSyncRepoPreservesViewerCanMergeWhenMergeSettingsOmitPermission(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	d := openTestDB(t)
	repoID, err := d.UpsertRepo(ctx, db.RepoIdentity{
		Platform:     "gitlab",
		PlatformHost: "gitlab.example.com",
		Owner:        "group",
		Name:         "project",
		RepoPath:     "group/project",
	})
	require.NoError(err)
	require.NoError(d.UpdateRepoSettings(ctx, repoID, true, true, true, false))
	syncer := &Syncer{db: d}

	syncer.updateRepoSettingsFromProviderRepo(ctx, repoID, platform.Repository{
		MergeSettings: &platform.RepositoryMergeSettings{
			AllowSquashMerge: false,
			AllowMergeCommit: true,
			AllowRebaseMerge: false,
		},
	})

	repos, err := d.ListRepos(ctx)
	require.NoError(err)
	require.Len(repos, 1)
	assert.False(repos[0].AllowSquashMerge)
	assert.True(repos[0].AllowMergeCommit)
	assert.False(repos[0].AllowRebaseMerge)
	assert.False(repos[0].ViewerCanMerge)
}

func TestSyncRepoRefreshesProviderRepoSettingsWhenIdentityKnown(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	d := openTestDB(t)
	repoID, err := d.UpsertRepoByProviderID(ctx, db.RepoIdentity{
		Platform:       "gitlab",
		PlatformHost:   "gitlab.example.com",
		PlatformRepoID: "gid://gitlab/Project/42",
		Owner:          "group",
		Name:           "project",
		RepoPath:       "group/project",
	})
	require.NoError(err)
	require.NoError(d.UpdateRepoSettings(ctx, repoID, true, true, true, true))
	viewerCanMerge := false
	provider := &syncTestRepositoryReadProvider{
		syncTestReadProvider: &syncTestReadProvider{
			syncTestProvider: syncTestProvider{
				kind: platform.KindGitLab,
				host: "gitlab.example.com",
			},
		},
		repository: platform.Repository{
			ViewerCanMerge: &viewerCanMerge,
		},
	}
	registry, err := platform.NewRegistry(provider)
	require.NoError(err)
	syncer := NewSyncerWithRegistry(registry, d, nil, []RepoRef{{
		Platform:           platform.KindGitLab,
		PlatformHost:       "gitlab.example.com",
		Owner:              "group",
		Name:               "project",
		RepoPath:           "group/project",
		PlatformExternalID: "gid://gitlab/Project/42",
	}}, time.Minute, nil, nil)

	require.NoError(syncer.syncRepo(ctx, syncer.repos[0]))

	repos, err := d.ListRepos(ctx)
	require.NoError(err)
	require.Len(repos, 1)
	assert.Equal(int32(1), provider.getRepositoryCalls.Load())
	assert.False(repos[0].ViewerCanMerge)
}

func TestSyncRepoUsesProviderCloneURLForNestedGitLabRepo(t *testing.T) {
	require := require.New(t)
	ctx := t.Context()
	d := openTestDB(t)
	remote := setupBareRemoteForSyncTest(t)
	clones := gitclone.New(t.TempDir(), nil)
	repo := RepoRef{
		Platform:           platform.KindGitLab,
		PlatformHost:       "gitlab.example.com",
		Owner:              "group/subgroup",
		Name:               "project",
		RepoPath:           "group/subgroup/project",
		PlatformExternalID: "gid://gitlab/Project/43",
		CloneURL:           remote,
	}
	provider := &syncTestReadProvider{
		syncTestProvider: syncTestProvider{
			kind: platform.KindGitLab,
			host: "gitlab.example.com",
		},
	}
	registry, err := platform.NewRegistry(provider)
	require.NoError(err)
	syncer := NewSyncerWithRegistry(registry, d, clones, []RepoRef{repo}, time.Minute, nil, nil)

	require.NoError(syncer.syncRepo(ctx, repo))
	clonePath, err := clones.ClonePath("gitlab.example.com", "group/subgroup", "project")
	require.NoError(err)
	require.FileExists(filepath.Join(clonePath, "HEAD"))
}

func TestDetailDrainUsesProviderCloneURLForNestedGitLabRepo(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	d := openTestDB(t)
	now := time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC)
	remote := setupBareRemoteForSyncTest(t)
	clones := gitclone.New(t.TempDir(), nil)
	repo := RepoRef{
		Platform:     platform.KindGitLab,
		PlatformHost: "gitlab.example.com",
		Owner:        "group/subgroup",
		Name:         "project",
		RepoPath:     "group/subgroup/project",
		CloneURL:     remote,
	}
	repoID, err := d.UpsertRepo(ctx, platform.DBRepoIdentity(platformRepoRef(repo)))
	require.NoError(err)
	_, err = d.UpsertMergeRequest(ctx, &db.MergeRequest{
		RepoID:         repoID,
		PlatformID:     1001,
		Number:         7,
		URL:            "https://gitlab.example.com/group/subgroup/project/-/merge_requests/7",
		Title:          "stale MR",
		Author:         "ada",
		State:          "open",
		HeadBranch:     "feature",
		BaseBranch:     "main",
		CreatedAt:      now,
		UpdatedAt:      now,
		LastActivityAt: now,
	})
	require.NoError(err)
	provider := &syncTestReadProvider{
		syncTestProvider: syncTestProvider{
			kind: platform.KindGitLab,
			host: "gitlab.example.com",
		},
		mergeRequests: []platform.MergeRequest{{
			Repo:           platformRepoRef(repo),
			PlatformID:     1001,
			Number:         7,
			URL:            "https://gitlab.example.com/group/subgroup/project/-/merge_requests/7",
			Title:          "fresh MR",
			Author:         "ada",
			State:          "open",
			HeadBranch:     "feature",
			BaseBranch:     "main",
			CreatedAt:      now,
			UpdatedAt:      now,
			LastActivityAt: now,
		}},
	}
	registry, err := platform.NewRegistry(provider)
	require.NoError(err)
	rateKey := RateBucketKey("gitlab", "gitlab.example.com")
	syncer := NewSyncerWithRegistry(registry, d, clones, []RepoRef{repo}, time.Minute, nil, map[string]*SyncBudget{
		rateKey: NewSyncBudget(100),
	})

	syncer.drainDetailQueue(ctx, map[string]bool{rateKey: true})

	assert.Equal(int32(1), provider.getMRCalls.Load())
	clonePath, err := clones.ClonePath("gitlab.example.com", "group/subgroup", "project")
	require.NoError(err)
	require.FileExists(filepath.Join(clonePath, "HEAD"))
}

func TestSyncMRUsesConfiguredProviderRegistry(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	database := openTestDB(t)
	ctx := t.Context()

	provider := &syncTestReadProvider{
		syncTestProvider: syncTestProvider{
			kind: platform.KindGitLab,
			host: "gitlab.com",
		},
		mergeRequests: []platform.MergeRequest{{
			PlatformID: 42,
			Number:     10,
			Title:      "gitlab mr",
			State:      "open",
			Author:     "author",
			CreatedAt:  time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC),
			UpdatedAt:  time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC),
		}},
	}
	registry, err := platform.NewRegistry(provider)
	require.NoError(err)
	syncer := NewSyncerWithRegistry(
		registry,
		database,
		nil,
		[]RepoRef{{
			Platform:     platform.KindGitLab,
			PlatformHost: "gitlab.com",
			Owner:        "acme",
			Name:         "widget",
		}},
		time.Minute,
		nil,
		nil,
	)

	require.NoError(syncer.SyncMR(ctx, "acme", "widget", 10))

	mr, err := database.GetMergeRequest(ctx, "acme", "widget", 10)
	require.NoError(err)
	require.NotNil(mr)
	assert.Equal("gitlab mr", mr.Title)
	assert.Equal(int32(1), provider.getMRCalls.Load())
}

func TestSyncItemByNumberRejectsNonGitHubProviderWithoutForcingGitHub(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	database := openTestDB(t)
	ctx := t.Context()

	provider := &syncTestReadProvider{
		syncTestProvider: syncTestProvider{
			kind: platform.KindGitLab,
			host: "gitlab.com",
		},
	}
	registry, err := platform.NewRegistry(provider)
	require.NoError(err)
	syncer := NewSyncerWithRegistry(
		registry,
		database,
		nil,
		[]RepoRef{{
			Platform:     platform.KindGitLab,
			PlatformHost: "gitlab.com",
			Owner:        "acme",
			Name:         "widget",
		}},
		time.Minute,
		nil,
		nil,
	)

	_, err = syncer.SyncItemByNumber(ctx, "acme", "widget", 10)

	require.Error(err)
	assert.Contains(err.Error(), "requires an item type")
	assert.Equal(int32(0), provider.getIssueCalls.Load())
	assert.Equal(int32(0), provider.getMRCalls.Load())
}

func TestSyncMRRejectsAmbiguousProviderIdentity(t *testing.T) {
	require := require.New(t)
	database := openTestDB(t)
	ctx := t.Context()

	registry, err := platform.NewRegistry(
		&syncTestReadProvider{
			syncTestProvider: syncTestProvider{
				kind: platform.KindGitLab,
				host: "code.example.com",
			},
		},
	)
	require.NoError(err)
	syncer := NewSyncerWithRegistry(
		registry,
		database,
		nil,
		[]RepoRef{
			{
				Platform:     platform.KindGitHub,
				PlatformHost: "code.example.com",
				Owner:        "acme",
				Name:         "widget",
			},
			{
				Platform:     platform.KindGitLab,
				PlatformHost: "code.example.com",
				Owner:        "acme",
				Name:         "widget",
			},
		},
		time.Minute,
		nil,
		nil,
	)

	err = syncer.SyncMR(ctx, "acme", "widget", 10)

	require.Error(err)
	require.Contains(err.Error(), "ambiguous")
}

func TestIndexUpsertMRReadsExistingByRepoID(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	now := time.Date(2026, 4, 20, 12, 0, 0, 0, time.UTC)

	githubRepo := RepoRef{
		Platform:     platform.KindGitHub,
		PlatformHost: "code.example.com",
		Owner:        "acme",
		Name:         "widget",
	}
	gitlabRepo := RepoRef{
		Platform:     platform.KindGitLab,
		PlatformHost: "code.example.com",
		Owner:        "acme",
		Name:         "widget",
	}
	githubRepoID, err := d.UpsertRepo(ctx, platform.DBRepoIdentity(platformRepoRef(githubRepo)))
	require.NoError(err)
	gitlabRepoID, err := d.UpsertRepo(ctx, platform.DBRepoIdentity(platformRepoRef(gitlabRepo)))
	require.NoError(err)

	detailFetchedAt := now.Add(-time.Minute)
	_, err = d.UpsertMergeRequest(ctx, &db.MergeRequest{
		RepoID:          gitlabRepoID,
		PlatformID:      7001,
		Number:          7,
		URL:             "https://code.example.com/acme/widget/-/merge_requests/7",
		Title:           "gitlab MR",
		Author:          "ada",
		State:           "open",
		Additions:       123,
		Deletions:       45,
		MergeableState:  "checking",
		HeadBranch:      "feature",
		BaseBranch:      "main",
		PlatformHeadSHA: "gitlab-head",
		PlatformBaseSHA: "base",
		CreatedAt:       now,
		UpdatedAt:       now,
		LastActivityAt:  now,
		DetailFetchedAt: &detailFetchedAt,
	})
	require.NoError(err)

	syncer := NewSyncer(nil, d, nil, []RepoRef{githubRepo, gitlabRepo}, time.Minute, nil, nil)
	require.NoError(syncer.indexUpsertMR(ctx, &mockClient{}, githubRepo, githubRepoID, buildOpenPR(7, now)))

	githubMR, err := d.GetMergeRequestByRepoIDAndNumber(ctx, githubRepoID, 7)
	require.NoError(err)
	require.NotNil(githubMR)
	assert.Zero(githubMR.Additions)
	assert.Zero(githubMR.Deletions)
	assert.Empty(githubMR.MergeableState)
	assert.Nil(githubMR.DetailFetchedAt)

	gitlabMR, err := d.GetMergeRequestByRepoIDAndNumber(ctx, gitlabRepoID, 7)
	require.NoError(err)
	require.NotNil(gitlabMR)
	assert.Equal(123, gitlabMR.Additions)
	assert.NotNil(gitlabMR.DetailFetchedAt)
}

func TestFetchMRDetailUsesRepoIDForPendingAndCallback(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	now := time.Date(2026, 4, 20, 12, 0, 0, 0, time.UTC)

	githubRepo := RepoRef{
		Platform:     platform.KindGitHub,
		PlatformHost: "code.example.com",
		Owner:        "acme",
		Name:         "widget",
	}
	gitlabRepo := RepoRef{
		Platform:     platform.KindGitLab,
		PlatformHost: "code.example.com",
		Owner:        "acme",
		Name:         "widget",
	}
	githubRepoID, err := d.UpsertRepo(ctx, platform.DBRepoIdentity(platformRepoRef(githubRepo)))
	require.NoError(err)
	gitlabRepoID, err := d.UpsertRepo(ctx, platform.DBRepoIdentity(platformRepoRef(gitlabRepo)))
	require.NoError(err)

	_, err = d.UpsertMergeRequest(ctx, &db.MergeRequest{
		RepoID:         gitlabRepoID,
		PlatformID:     7001,
		Number:         7,
		URL:            "https://code.example.com/acme/widget/-/merge_requests/7",
		Title:          "gitlab MR",
		Author:         "ada",
		State:          "open",
		CreatedAt:      now,
		UpdatedAt:      now,
		LastActivityAt: now,
		CIChecksJSON:   `[{"status":"in_progress"}]`,
	})
	require.NoError(err)

	pr := buildOpenPR(7, now)
	pr.Title = new("github MR")
	mc := &mockClient{
		singlePR: pr,
		comments: []*gh.IssueComment{},
		reviews:  []*gh.PullRequestReview{},
		commits:  []*gh.RepositoryCommit{},
		ciStatus: &gh.CombinedStatus{State: new("success")},
	}
	syncer := NewSyncer(
		map[string]Client{"code.example.com": mc},
		d, nil,
		[]RepoRef{githubRepo, gitlabRepo},
		time.Minute,
		nil,
		nil,
	)
	var callbackMR *db.MergeRequest
	syncer.onMRSynced = func(_, _ string, mr *db.MergeRequest) {
		callbackMR = mr
	}

	_, err = syncer.fetchMRDetail(ctx, githubRepo, githubRepoID, 7, true)
	require.NoError(err)

	githubMR, err := d.GetMergeRequestByRepoIDAndNumber(ctx, githubRepoID, 7)
	require.NoError(err)
	require.NotNil(githubMR)
	assert.False(githubMR.CIHadPending)
	assert.NotNil(githubMR.DetailFetchedAt)
	require.NotNil(callbackMR)
	assert.Equal(githubMR.ID, callbackMR.ID)

	gitlabMR, err := d.GetMergeRequestByRepoIDAndNumber(ctx, gitlabRepoID, 7)
	require.NoError(err)
	require.NotNil(gitlabMR)
	assert.False(gitlabMR.CIHadPending)
	assert.Nil(gitlabMR.DetailFetchedAt)
}

// TestFetchMRDetailPersistsWorkflowApproval verifies the budgeted
// detail drain (the path the periodic sync uses) also persists the
// workflow approval snapshot. Without this, the Approve workflows
// button would stay hidden for any PR whose detail came in through
// the queue rather than an explicit POST /sync.
func TestFetchMRDetailPersistsWorkflowApproval(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	now := time.Date(2026, 4, 20, 12, 0, 0, 0, time.UTC)

	repo := RepoRef{
		Platform:     platform.KindGitHub,
		PlatformHost: "github.com",
		Owner:        "acme",
		Name:         "widget",
	}
	repoID, err := d.UpsertRepo(ctx, platform.DBRepoIdentity(platformRepoRef(repo)))
	require.NoError(err)

	pr := buildOpenPR(7, now)
	headSHA := pr.GetHead().GetSHA()
	require.NotEmpty(headSHA)
	mc := &mockClient{
		singlePR: pr,
		comments: []*gh.IssueComment{},
		reviews:  []*gh.PullRequestReview{},
		commits:  []*gh.RepositoryCommit{},
		ciStatus: &gh.CombinedStatus{State: new("success")},
		workflowRuns: []*gh.WorkflowRun{{
			ID:           new(int64(4242)),
			HeadSHA:      &headSHA,
			Event:        new("pull_request"),
			PullRequests: []*gh.PullRequest{{Number: new(7)}},
		}},
	}
	syncer := NewSyncer(
		map[string]Client{"github.com": mc},
		d, nil,
		[]RepoRef{repo},
		time.Minute,
		nil,
		nil,
	)

	_, err = syncer.fetchMRDetail(ctx, repo, repoID, 7, true)
	require.NoError(err)

	got, err := d.GetMergeRequestByRepoIDAndNumber(ctx, repoID, 7)
	require.NoError(err)
	require.NotNil(got)
	require.NotNil(got.WorkflowApprovalCheckedAt,
		"detail drain must populate workflow_approval_checked_at")
	assert.Equal(headSHA, got.WorkflowApprovalHeadSHA)
	assert.True(got.WorkflowApprovalRequired)
	assert.Equal(1, got.WorkflowApprovalCount)
}

func TestSyncOpenIssueReadsExistingByRepoID(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	now := time.Date(2026, 4, 20, 12, 0, 0, 0, time.UTC)

	githubRepo := RepoRef{
		Platform:     platform.KindGitHub,
		PlatformHost: "code.example.com",
		Owner:        "acme",
		Name:         "widget",
	}
	gitlabRepo := RepoRef{
		Platform:     platform.KindGitLab,
		PlatformHost: "code.example.com",
		Owner:        "acme",
		Name:         "widget",
	}
	githubRepoID, err := d.UpsertRepo(ctx, platform.DBRepoIdentity(platformRepoRef(githubRepo)))
	require.NoError(err)
	gitlabRepoID, err := d.UpsertRepo(ctx, platform.DBRepoIdentity(platformRepoRef(gitlabRepo)))
	require.NoError(err)

	detailFetchedAt := now.Add(-time.Minute)
	_, err = d.UpsertIssue(ctx, &db.Issue{
		RepoID:          gitlabRepoID,
		PlatformID:      7001,
		Number:          7,
		URL:             "https://code.example.com/acme/widget/-/issues/7",
		Title:           "gitlab issue",
		Author:          "ada",
		State:           "open",
		CommentCount:    12,
		CreatedAt:       now,
		UpdatedAt:       now,
		LastActivityAt:  now,
		DetailFetchedAt: &detailFetchedAt,
	})
	require.NoError(err)

	issueNumber := 7
	issueTitle := "github issue"
	issueState := "open"
	mc := &mockClient{
		comments: []*gh.IssueComment{},
	}
	syncer := NewSyncer(
		map[string]Client{"code.example.com": mc},
		d, nil,
		[]RepoRef{githubRepo, gitlabRepo},
		time.Minute,
		nil,
		nil,
	)

	err = syncer.syncOpenIssue(ctx, mc, githubRepo, githubRepoID, &gh.Issue{
		ID:        new(int64(1007)),
		Number:    &issueNumber,
		Title:     &issueTitle,
		State:     &issueState,
		HTMLURL:   new("https://code.example.com/acme/widget/issues/7"),
		CreatedAt: makeTimestamp(now),
		UpdatedAt: makeTimestamp(now),
	}, false)
	require.NoError(err)

	githubIssue, err := d.GetIssueByRepoIDAndNumber(ctx, githubRepoID, 7)
	require.NoError(err)
	require.NotNil(githubIssue)
	assert.Zero(githubIssue.CommentCount)
	assert.Nil(githubIssue.DetailFetchedAt)

	gitlabIssue, err := d.GetIssueByRepoIDAndNumber(ctx, gitlabRepoID, 7)
	require.NoError(err)
	require.NotNil(gitlabIssue)
	assert.Equal(12, gitlabIssue.CommentCount)
	assert.NotNil(gitlabIssue.DetailFetchedAt)
}

func TestSyncMRReturnsErrorWhenClientReturnsNilPR(t *testing.T) {
	require := require.New(t)
	database := openTestDB(t)
	ctx := t.Context()

	mc := &mockClient{
		getPullRequestFn: func(context.Context, string, string, int) (*gh.PullRequest, error) {
			return nil, nil
		},
	}
	syncer := NewSyncer(map[string]Client{"github.com": mc}, database, nil, []RepoRef{{
		Owner: "acme", Name: "widget", PlatformHost: "github.com",
	}}, time.Minute, nil, nil)

	err := syncer.SyncMR(ctx, "acme", "widget", 10)
	require.Error(err)
	require.ErrorContains(err, "client returned nil pull request")

	stored, getErr := database.GetMergeRequest(ctx, "acme", "widget", 10)
	require.NoError(getErr)
	require.Nil(stored)
}

func TestSyncIssueReturnsErrorWhenClientReturnsNilIssue(t *testing.T) {
	require := require.New(t)
	database := openTestDB(t)
	ctx := t.Context()

	mc := &mockClient{
		getIssueFn: func(context.Context, string, string, int) (*gh.Issue, error) {
			return nil, nil
		},
	}
	syncer := NewSyncer(map[string]Client{"github.com": mc}, database, nil, []RepoRef{{
		Owner: "acme", Name: "widget", PlatformHost: "github.com",
	}}, time.Minute, nil, nil)

	err := syncer.SyncIssue(ctx, "acme", "widget", 5)
	require.Error(err)
	require.ErrorContains(err, "client returned nil issue")

	stored, getErr := database.GetIssue(ctx, "acme", "widget", 5)
	require.NoError(getErr)
	require.Nil(stored)
}

func TestSyncItemByNumber_UntrackedRepo(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	database := openTestDB(t)

	mc := &mockClient{}
	syncer := NewSyncer(map[string]Client{"github.com": mc}, database, nil, []RepoRef{
		{Owner: "acme", Name: "widget", PlatformHost: "github.com"},
	}, time.Minute, nil, nil)

	_, err := syncer.SyncItemByNumber(t.Context(), "other", "repo", 1)
	require.Error(err)
	assert.Contains(err.Error(), "not tracked")
}

func TestSyncerMultiHostClientDispatch(t *testing.T) {
	assert := Assert.New(t)
	d := openTestDB(t)

	ghMock := &mockClient{
		openPRs:  []*gh.PullRequest{},
		comments: []*gh.IssueComment{},
		reviews:  []*gh.PullRequestReview{},
		commits:  []*gh.RepositoryCommit{},
	}
	gheMock := &mockClient{
		openPRs:  []*gh.PullRequest{},
		comments: []*gh.IssueComment{},
		reviews:  []*gh.PullRequestReview{},
		commits:  []*gh.RepositoryCommit{},
	}

	clients := map[string]Client{
		"github.com":   ghMock,
		"ghe.corp.com": gheMock,
	}
	repos := []RepoRef{
		{Owner: "pub", Name: "repo", PlatformHost: "github.com"},
		{Owner: "corp", Name: "internal", PlatformHost: "ghe.corp.com"},
	}

	syncer := NewSyncer(clients, d, nil, repos, time.Minute, nil, nil)
	syncer.RunOnce(t.Context())

	assert.True(ghMock.listOpenPRsCalled,
		"github.com mock should have been called")
	assert.True(gheMock.listOpenPRsCalled,
		"ghe.corp.com mock should have been called")
}

func TestSyncRunUsesProviderReadersForIndexSync(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	repo := RepoRef{
		Platform:           platform.KindGitLab,
		PlatformHost:       "gitlab.com",
		Owner:              "acme",
		Name:               "widget",
		PlatformExternalID: "gid://gitlab/Project/100",
	}
	provider := &syncTestReadProvider{
		syncTestProvider: syncTestProvider{
			kind: platform.KindGitLab,
			host: "gitlab.com",
		},
		mergeRequests: []platform.MergeRequest{{
			Repo:           platformRepoRef(repo),
			PlatformID:     1001,
			Number:         7,
			URL:            "https://gitlab.com/acme/widget/-/merge_requests/7",
			Title:          "Provider MR",
			Author:         "ada",
			State:          "open",
			HeadBranch:     "feature",
			BaseBranch:     "main",
			HeadSHA:        "abc123",
			BaseSHA:        "def456",
			CreatedAt:      now,
			UpdatedAt:      now,
			LastActivityAt: now,
		}},
		issues: []platform.Issue{{
			Repo:           platformRepoRef(repo),
			PlatformID:     2001,
			Number:         11,
			URL:            "https://gitlab.com/acme/widget/-/issues/11",
			Title:          "Provider issue",
			Author:         "grace",
			State:          "open",
			CreatedAt:      now,
			UpdatedAt:      now,
			LastActivityAt: now,
		}},
	}
	registry, err := platform.NewRegistry(provider)
	require.NoError(err)

	syncer := NewSyncer(nil, d, nil, []RepoRef{repo}, time.Minute, nil, nil)
	syncer.clients = registry
	syncer.RunOnce(t.Context())

	assert.Equal(int32(1), provider.listMRCalls.Load())
	assert.Equal(int32(1), provider.listIssueCalls.Load())
	mr, err := d.GetMergeRequest(t.Context(), "acme", "widget", 7)
	require.NoError(err)
	require.NotNil(mr)
	assert.Equal("Provider MR", mr.Title)
	assert.Equal("abc123", mr.PlatformHeadSHA)
	issue, err := d.GetIssue(t.Context(), "acme", "widget", 11)
	require.NoError(err)
	require.NotNil(issue)
	assert.Equal("Provider issue", issue.Title)
}

func TestSyncRunAllowsMergeRequestOnlyProvider(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	repo := RepoRef{
		Platform:           platform.KindGitLab,
		PlatformHost:       "gitlab.com",
		Owner:              "acme",
		Name:               "widget",
		PlatformExternalID: "gid://gitlab/Project/101",
	}
	provider := &syncTestMergeRequestOnlyProvider{
		syncTestProvider: syncTestProvider{
			kind: platform.KindGitLab,
			host: "gitlab.com",
		},
		mergeRequests: []platform.MergeRequest{{
			Repo:           platformRepoRef(repo),
			PlatformID:     1001,
			Number:         7,
			URL:            "https://gitlab.com/acme/widget/-/merge_requests/7",
			Title:          "MR-only provider",
			Author:         "ada",
			State:          "open",
			HeadBranch:     "feature",
			BaseBranch:     "main",
			HeadSHA:        "abc123",
			BaseSHA:        "def456",
			CreatedAt:      now,
			UpdatedAt:      now,
			LastActivityAt: now,
		}},
	}
	registry, err := platform.NewRegistry(provider)
	require.NoError(err)

	syncer := NewSyncer(nil, d, nil, []RepoRef{repo}, time.Minute, nil, nil)
	syncer.clients = registry

	var results []RepoSyncResult
	syncer.SetOnSyncCompleted(func(r []RepoSyncResult) {
		results = r
	})
	syncer.RunOnce(t.Context())

	require.Len(results, 1)
	require.Equal(platform.KindGitLab, results[0].Platform)
	require.Equal("gitlab.com", results[0].PlatformHost)
	require.Empty(results[0].Error)
	assert.Equal(int32(1), provider.listMRCalls.Load())
	mr, err := d.GetMergeRequest(t.Context(), "acme", "widget", 7)
	require.NoError(err)
	require.NotNil(mr)
	assert.Equal("MR-only provider", mr.Title)
}

func TestSyncRunAllowsIssueOnlyProvider(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	repo := RepoRef{
		Platform:           platform.KindGitLab,
		PlatformHost:       "gitlab.com",
		Owner:              "acme",
		Name:               "widget",
		PlatformExternalID: "gid://gitlab/Project/102",
	}
	provider := &syncTestIssueOnlyProvider{
		syncTestProvider: syncTestProvider{
			kind: platform.KindGitLab,
			host: "gitlab.com",
		},
		issues: []platform.Issue{{
			Repo:           platformRepoRef(repo),
			PlatformID:     2001,
			Number:         11,
			URL:            "https://gitlab.com/acme/widget/-/issues/11",
			Title:          "Issue-only provider",
			Author:         "grace",
			State:          "open",
			CreatedAt:      now,
			UpdatedAt:      now,
			LastActivityAt: now,
		}},
	}
	registry, err := platform.NewRegistry(provider)
	require.NoError(err)

	syncer := NewSyncer(nil, d, nil, []RepoRef{repo}, time.Minute, nil, nil)
	syncer.clients = registry

	var results []RepoSyncResult
	syncer.SetOnSyncCompleted(func(r []RepoSyncResult) {
		results = r
	})
	syncer.RunOnce(t.Context())

	require.Len(results, 1)
	require.Equal(platform.KindGitLab, results[0].Platform)
	require.Equal("gitlab.com", results[0].PlatformHost)
	require.Empty(results[0].Error)
	assert.Equal(int32(1), provider.listIssueCalls.Load())
	issue, err := d.GetIssue(t.Context(), "acme", "widget", 11)
	require.NoError(err)
	require.NotNil(issue)
	assert.Equal("Issue-only provider", issue.Title)
}

func TestSyncMRUsesProviderMergeRequestReader(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	repo := RepoRef{
		Platform:     platform.KindGitLab,
		PlatformHost: "gitlab.com",
		Owner:        "acme",
		Name:         "widget",
	}
	provider := &syncTestReadProvider{
		syncTestProvider: syncTestProvider{
			kind: platform.KindGitLab,
			host: "gitlab.com",
		},
		mergeRequests: []platform.MergeRequest{{
			Repo:           platformRepoRef(repo),
			PlatformID:     1001,
			Number:         7,
			URL:            "https://gitlab.com/acme/widget/-/merge_requests/7",
			Title:          "Provider MR detail",
			Author:         "ada",
			State:          "open",
			HeadBranch:     "feature",
			BaseBranch:     "main",
			HeadSHA:        "abc123",
			BaseSHA:        "def456",
			CreatedAt:      now,
			UpdatedAt:      now,
			LastActivityAt: now,
		}},
	}
	registry, err := platform.NewRegistry(provider)
	require.NoError(err)
	syncer := NewSyncer(nil, d, nil, []RepoRef{repo}, time.Minute, nil, nil)
	syncer.clients = registry

	err = syncer.SyncMR(t.Context(), "acme", "widget", 7)

	require.NoError(err)
	assert.Equal(int32(1), provider.getMRCalls.Load())
	mr, err := d.GetMergeRequest(t.Context(), "acme", "widget", 7)
	require.NoError(err)
	require.NotNil(mr)
	assert.Equal("Provider MR detail", mr.Title)
	assert.Equal("abc123", mr.PlatformHeadSHA)
}

func TestSyncIssueUsesProviderIssueReader(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	repo := RepoRef{
		Platform:     platform.KindGitLab,
		PlatformHost: "gitlab.com",
		Owner:        "acme",
		Name:         "widget",
	}
	provider := &syncTestReadProvider{
		syncTestProvider: syncTestProvider{
			kind: platform.KindGitLab,
			host: "gitlab.com",
		},
		issues: []platform.Issue{{
			Repo:           platformRepoRef(repo),
			PlatformID:     2001,
			Number:         11,
			URL:            "https://gitlab.com/acme/widget/-/issues/11",
			Title:          "Provider issue detail",
			Author:         "grace",
			State:          "open",
			CreatedAt:      now,
			UpdatedAt:      now,
			LastActivityAt: now,
		}},
	}
	registry, err := platform.NewRegistry(provider)
	require.NoError(err)
	syncer := NewSyncer(nil, d, nil, []RepoRef{repo}, time.Minute, nil, nil)
	syncer.clients = registry

	err = syncer.SyncIssue(t.Context(), "acme", "widget", 11)

	require.NoError(err)
	assert.Equal(int32(1), provider.getIssueCalls.Load())
	issue, err := d.GetIssue(t.Context(), "acme", "widget", 11)
	require.NoError(err)
	require.NotNil(issue)
	assert.Equal("Provider issue detail", issue.Title)
	assert.NotNil(issue.DetailFetchedAt)
}

func TestOnMRSyncedCalledDuringSync(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	d := openTestDB(t)

	now := time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC)
	mc := &mockClient{
		openPRs:  []*gh.PullRequest{buildOpenPR(1, now)},
		comments: []*gh.IssueComment{},
		reviews:  []*gh.PullRequestReview{},
		commits:  []*gh.RepositoryCommit{},
	}

	syncer := NewSyncer(
		map[string]Client{"github.com": mc}, d, nil,
		[]RepoRef{{Owner: "owner", Name: "repo", PlatformHost: "github.com"}},
		time.Minute, nil, testBudget(500),
	)

	type hookCall struct {
		owner        string
		name         string
		number       int
		ciChecksJSON string
		updatedAt    time.Time
	}
	var called []hookCall
	syncer.SetOnMRSynced(func(owner, name string, mr *db.MergeRequest) {
		called = append(called, hookCall{
			owner:        owner,
			name:         name,
			number:       mr.Number,
			ciChecksJSON: mr.CIChecksJSON,
			updatedAt:    mr.UpdatedAt,
		})
	})

	syncer.RunOnce(t.Context())

	require.Len(called, 1)
	assert.Equal("owner", called[0].owner)
	assert.Equal("repo", called[0].name)
	assert.Equal(1, called[0].number)
	assert.True(called[0].updatedAt.Equal(now),
		"UpdatedAt should match the PR's UpdatedAt")
}

func TestOnMRSyncedIncludesCIChecksJSON(t *testing.T) {
	assert := Assert.New(t)
	d := openTestDB(t)

	now := time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC)
	ciState := "success"
	checkName := "build"
	checkStatus := "completed"
	checkConclusion := "success"
	mc := &mockClient{
		openPRs:  []*gh.PullRequest{buildOpenPR(1, now)},
		comments: []*gh.IssueComment{},
		reviews:  []*gh.PullRequestReview{},
		commits:  []*gh.RepositoryCommit{},
		ciStatus: &gh.CombinedStatus{State: &ciState},
	}
	mc.checkRuns = []*gh.CheckRun{
		{
			Name:       &checkName,
			Status:     &checkStatus,
			Conclusion: &checkConclusion,
		},
	}

	syncer := NewSyncer(
		map[string]Client{"github.com": mc}, d, nil,
		[]RepoRef{{
			Owner: "owner", Name: "repo",
			PlatformHost: "github.com",
		}},
		time.Minute, nil, testBudget(500),
	)

	var gotJSON string
	syncer.SetOnMRSynced(
		func(_ string, _ string, mr *db.MergeRequest) {
			gotJSON = mr.CIChecksJSON
		},
	)

	syncer.RunOnce(t.Context())

	assert.Contains(gotJSON, "build",
		"CIChecksJSON should contain check run name")
}

func TestOnSyncCompletedCalledAfterSync(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	d := openTestDB(t)

	mc := &mockClient{
		openPRs:  []*gh.PullRequest{},
		comments: []*gh.IssueComment{},
		reviews:  []*gh.PullRequestReview{},
		commits:  []*gh.RepositoryCommit{},
	}

	syncer := NewSyncer(
		map[string]Client{"github.com": mc}, d, nil,
		[]RepoRef{
			{Owner: "acme", Name: "widget", PlatformHost: "github.com"},
			{Owner: "acme", Name: "lib", PlatformHost: "github.com"},
		},
		time.Minute, nil, nil,
	)

	var gotResults []RepoSyncResult
	syncer.SetOnSyncCompleted(func(results []RepoSyncResult) {
		gotResults = results
	})

	syncer.RunOnce(t.Context())

	require.Len(gotResults, 2)
	assert.Equal("acme", gotResults[0].Owner)
	assert.Equal("widget", gotResults[0].Name)
	assert.Equal(platform.KindGitHub, gotResults[0].Platform)
	assert.Equal("github.com", gotResults[0].PlatformHost)
	assert.Empty(gotResults[0].Error)
	assert.Equal("acme", gotResults[1].Owner)
	assert.Equal("lib", gotResults[1].Name)
	assert.Equal(platform.KindGitHub, gotResults[1].Platform)
	assert.Equal("github.com", gotResults[1].PlatformHost)
	assert.Empty(gotResults[1].Error)
}

func TestNilHooksNoOp(t *testing.T) {
	d := openTestDB(t)

	mc := &mockClient{
		openPRs:  []*gh.PullRequest{},
		comments: []*gh.IssueComment{},
		reviews:  []*gh.PullRequestReview{},
		commits:  []*gh.RepositoryCommit{},
	}

	syncer := NewSyncer(
		map[string]Client{"github.com": mc}, d, nil,
		[]RepoRef{{Owner: "o", Name: "r", PlatformHost: "github.com"}},
		time.Minute, nil, nil,
	)

	// No hooks set -- should not panic.
	syncer.RunOnce(t.Context())
}

func TestWatchedMRsSyncedOnFastInterval(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	d := openTestDB(t)

	now := time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC)
	pr := buildOpenPR(7, now)

	mc := &mockClient{
		openPRs:  []*gh.PullRequest{},
		singlePR: pr,
		comments: []*gh.IssueComment{},
		reviews:  []*gh.PullRequestReview{},
		commits:  []*gh.RepositoryCommit{},
	}

	syncer := NewSyncer(
		map[string]Client{"github.com": mc}, d, nil,
		[]RepoRef{{
			Owner: "acme", Name: "app",
			PlatformHost: "github.com",
		}},
		time.Hour, nil, nil, // bulk sync at 1h -- won't fire during test
	)
	syncer.SetWatchInterval(50 * time.Millisecond)

	var mu sync.Mutex
	var hookCalls []int
	syncer.SetOnMRSynced(
		func(_ string, _ string, mr *db.MergeRequest) {
			mu.Lock()
			hookCalls = append(hookCalls, mr.Number)
			mu.Unlock()
		},
	)

	syncer.SetWatchedMRs([]WatchedMR{
		{Owner: "acme", Name: "app", Number: 7},
	})

	syncer.Start(ctx)
	defer syncer.Stop()

	// Wait for at least one fast-sync tick.
	assert.Eventually(func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(hookCalls) >= 1
	}, 2*time.Second, 20*time.Millisecond)

	// Verify the MR was persisted.
	mr, err := d.GetMergeRequest(ctx, "acme", "app", 7)
	require.NoError(err)
	require.NotNil(mr)
	assert.Equal(7, mr.Number)
}

func TestEmptyWatchListNoOp(t *testing.T) {
	d := openTestDB(t)

	mc := &mockClient{
		openPRs: []*gh.PullRequest{},
	}

	syncer := NewSyncer(
		map[string]Client{"github.com": mc}, d, nil,
		[]RepoRef{{
			Owner: "acme", Name: "app",
			PlatformHost: "github.com",
		}},
		time.Hour, nil, nil,
	)
	callCount := 0
	syncer.SetOnMRSynced(
		func(_ string, _ string, _ *db.MergeRequest) {
			callCount++
		},
	)

	syncer.syncWatchedMRs(t.Context())

	Assert.Equal(t, 0, callCount,
		"empty watch list should not trigger any MR syncs")
}

func TestSetWatchedMRsReplacesList(t *testing.T) {
	assert := Assert.New(t)
	d := openTestDB(t)

	now := time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC)

	mc := &mockClient{
		openPRs:  []*gh.PullRequest{},
		comments: []*gh.IssueComment{},
		reviews:  []*gh.PullRequestReview{},
		commits:  []*gh.RepositoryCommit{},
	}
	// Return different PRs based on the requested number.
	mc.getPullRequestFn = func(
		_ context.Context, _, _ string, number int,
	) (*gh.PullRequest, error) {
		return buildOpenPR(number, now), nil
	}

	syncer := NewSyncer(
		map[string]Client{"github.com": mc}, d, nil,
		[]RepoRef{{
			Owner: "acme", Name: "app",
			PlatformHost: "github.com",
		}},
		time.Hour, nil, nil,
	)
	syncer.SetWatchInterval(50 * time.Millisecond)

	var mu sync.Mutex
	syncedNumbers := map[int]int{} // number -> count
	syncer.SetOnMRSynced(
		func(_ string, _ string, mr *db.MergeRequest) {
			mu.Lock()
			syncedNumbers[mr.Number]++
			mu.Unlock()
		},
	)

	// Start with PR #1 on the watch list.
	syncer.SetWatchedMRs([]WatchedMR{
		{Owner: "acme", Name: "app", Number: 1},
	})
	syncer.Start(t.Context())
	defer syncer.Stop()

	// Wait for PR #1 to be synced.
	assert.Eventually(func() bool {
		mu.Lock()
		defer mu.Unlock()
		return syncedNumbers[1] >= 1
	}, 2*time.Second, 20*time.Millisecond)

	// Replace with PR #2 only.
	mu.Lock()
	countPR1Before := syncedNumbers[1]
	mu.Unlock()

	syncer.SetWatchedMRs([]WatchedMR{
		{Owner: "acme", Name: "app", Number: 2},
	})

	// Wait for PR #2 to be synced.
	assert.Eventually(func() bool {
		mu.Lock()
		defer mu.Unlock()
		return syncedNumbers[2] >= 1
	}, 2*time.Second, 20*time.Millisecond)

	// PR #1 should not accumulate many more syncs after replacement.
	// Allow at most 1 extra (for an in-flight tick at replacement time).
	mu.Lock()
	countPR1After := syncedNumbers[1]
	mu.Unlock()
	assert.LessOrEqual(countPR1After, countPR1Before+1,
		"PR #1 should stop being synced after watch list replacement")
}

func TestWatchedMRsSkipRateLimitedHost(t *testing.T) {
	assert := Assert.New(t)
	d := openTestDB(t)

	now := time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC)
	mc := &mockClient{
		openPRs:  []*gh.PullRequest{},
		singlePR: buildOpenPR(5, now),
		comments: []*gh.IssueComment{},
		reviews:  []*gh.PullRequestReview{},
		commits:  []*gh.RepositoryCommit{},
	}

	rt := NewRateTracker(d, "github.com", "rest")
	// Exhaust the rate limit with a future reset.
	futureReset := time.Now().Add(30 * time.Minute)
	rt.UpdateFromRate(Rate{
		Remaining: 0,
		Reset:     futureReset,
	})

	syncer := NewSyncer(
		map[string]Client{"github.com": mc}, d, nil,
		[]RepoRef{{
			Owner: "acme", Name: "app",
			PlatformHost: "github.com",
		}},
		time.Hour,
		map[string]*RateTracker{"github.com": rt}, nil,
	)
	syncer.SetWatchInterval(50 * time.Millisecond)

	callCount := 0
	syncer.SetOnMRSynced(
		func(_ string, _ string, _ *db.MergeRequest) {
			callCount++
		},
	)

	syncer.SetWatchedMRs([]WatchedMR{
		{
			Owner: "acme", Name: "app",
			Number: 5, PlatformHost: "github.com",
		},
	})

	// Call syncWatchedMRs directly to avoid the bulk RunOnce goroutine.
	syncer.syncWatchedMRs(t.Context())

	assert.Equal(0, callCount,
		"watched MRs should be skipped when host is rate-limited")
}

func TestWatchedMROnGHEHost(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()

	now := time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC)

	gheMC := &mockClient{
		openPRs:  []*gh.PullRequest{},
		singlePR: buildOpenPR(3, now),
		comments: []*gh.IssueComment{},
		reviews:  []*gh.PullRequestReview{},
		commits:  []*gh.RepositoryCommit{},
	}

	syncer := NewSyncer(
		map[string]Client{"ghes.corp.com": gheMC}, d, nil,
		[]RepoRef{{
			Owner: "corp", Name: "internal",
			PlatformHost: "ghes.corp.com",
		}},
		time.Hour, nil, nil,
	)

	var hookedOwner, hookedName string
	syncer.SetOnMRSynced(
		func(owner, name string, _ *db.MergeRequest) {
			hookedOwner = owner
			hookedName = name
		},
	)

	syncer.SetWatchedMRs([]WatchedMR{
		{
			Owner: "corp", Name: "internal",
			Number: 3, PlatformHost: "ghes.corp.com",
		},
	})

	syncer.syncWatchedMRs(ctx)

	// The MR should have been synced via the GHE client.
	mr, err := d.GetMergeRequest(ctx, "corp", "internal", 3)
	require.NoError(err)
	require.NotNil(mr)
	assert.Equal(3, mr.Number)
	assert.Equal("corp", hookedOwner)
	assert.Equal("internal", hookedName)

	// Verify the MR is associated with the GHE repo row, not github.com.
	repo, err := d.GetRepoByOwnerName(ctx, "corp", "internal")
	require.NoError(err)
	require.NotNil(repo)
	assert.Equal("ghes.corp.com", repo.PlatformHost)
	assert.Equal(repo.ID, mr.RepoID)
}

func TestWatchedMRRejectsUnmatchedHost(t *testing.T) {
	d := openTestDB(t)

	now := time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC)
	mc := &mockClient{
		openPRs:  []*gh.PullRequest{},
		singlePR: buildOpenPR(1, now),
		comments: []*gh.IssueComment{},
		reviews:  []*gh.PullRequestReview{},
		commits:  []*gh.RepositoryCommit{},
	}

	// Track acme/app only on github.com.
	syncer := NewSyncer(
		map[string]Client{"github.com": mc}, d, nil,
		[]RepoRef{{
			Owner: "acme", Name: "app",
			PlatformHost: "github.com",
		}},
		time.Hour, nil, nil,
	)

	callCount := 0
	syncer.SetOnMRSynced(
		func(_ string, _ string, _ *db.MergeRequest) {
			callCount++
		},
	)

	// Watch the same owner/name but on a different host.
	syncer.SetWatchedMRs([]WatchedMR{
		{
			Owner: "acme", Name: "app",
			Number: 1, PlatformHost: "ghes.other.com",
		},
	})

	syncer.syncWatchedMRs(t.Context())

	Assert.Equal(t, 0, callCount,
		"watched MR on untracked host should not be synced")
}

func TestRunOnceSkipsThrottledHosts(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	d := openTestDB(t)

	now := time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC)

	ghMock := &mockClient{
		openPRs:  []*gh.PullRequest{},
		comments: []*gh.IssueComment{},
		reviews:  []*gh.PullRequestReview{},
		commits:  []*gh.RepositoryCommit{},
	}
	gheMock := &mockClient{
		openPRs:  []*gh.PullRequest{buildOpenPR(1, now)},
		comments: []*gh.IssueComment{},
		reviews:  []*gh.PullRequestReview{},
		commits:  []*gh.RepositoryCommit{},
	}

	// Set up GHE tracker with remaining below reserve buffer.
	gheTracker := NewRateTracker(d, "ghe.corp.com", "rest")
	gheTracker.UpdateFromRate(Rate{
		Limit:     5000,
		Remaining: 100, // below RateReserveBuffer (200)
		Reset:     time.Now().Add(30 * time.Minute),
	})

	clients := map[string]Client{
		"github.com":   ghMock,
		"ghe.corp.com": gheMock,
	}
	trackers := map[string]*RateTracker{
		"ghe.corp.com": gheTracker,
	}
	repos := []RepoRef{
		{Owner: "pub", Name: "repo", PlatformHost: "github.com"},
		{Owner: "corp", Name: "internal", PlatformHost: "ghe.corp.com"},
	}

	syncer := NewSyncer(clients, d, nil, repos, time.Minute, trackers, nil)

	var gotResults []RepoSyncResult
	syncer.SetOnSyncCompleted(func(results []RepoSyncResult) {
		gotResults = results
	})

	syncer.RunOnce(t.Context())

	require.Len(gotResults, 2)

	// github.com repo should have synced (no error).
	assert.Equal("pub", gotResults[0].Owner)
	assert.Equal("repo", gotResults[0].Name)
	assert.Equal(platform.KindGitHub, gotResults[0].Platform)
	assert.Equal("github.com", gotResults[0].PlatformHost)
	assert.Empty(gotResults[0].Error,
		"github.com repo should sync normally")

	// ghe.corp.com repo should be skipped due to throttle.
	assert.Equal("corp", gotResults[1].Owner)
	assert.Equal("internal", gotResults[1].Name)
	assert.Equal(platform.KindGitHub, gotResults[1].Platform)
	assert.Equal("ghe.corp.com", gotResults[1].PlatformHost)
	assert.Equal("skipped: rate limit throttled", gotResults[1].Error,
		"ghe.corp.com repo should be skipped when paused")

	// github.com mock should have been called, GHE should not.
	assert.True(ghMock.listOpenPRsCalled,
		"github.com client should have been called")
	assert.False(gheMock.listOpenPRsCalled,
		"ghe.corp.com client should NOT have been called")
}

// ignoresCancelClient embeds mockClient and triggers an outer
// cancel() on the first ListOpenIssues call while still returning
// (nil, nil) successfully. This simulates a Client implementation
// that ignores ctx cancellation mid-call -- the defensive case
// the RunOnce cancel latch must handle.
type ignoresCancelClient struct {
	mockClient
	cancel context.CancelFunc
	once   sync.Once
}

func (c *ignoresCancelClient) ListOpenIssues(
	_ context.Context, _, _ string,
) ([]*gh.Issue, error) {
	c.once.Do(c.cancel)
	return nil, nil
}

// TestRunOnceLatchesCancelWhenSyncRepoIgnoresCtx covers the
// defense-in-depth gap flagged on commit 45a5421: if a Client
// ignores ctx cancellation mid-sync and every call still returns
// success, syncRepo will return nil after ctx has been canceled.
// Under the old completed-count heuristic (`completed < total`)
// the run was misreported as a clean completion -- onSyncCompleted
// fired even though the user had asked to cancel. The latched
// cancel flag must catch this case and route through the cancel
// status path instead.
func TestRunOnceLatchesCancelWhenSyncRepoIgnoresCtx(t *testing.T) {
	assert := Assert.New(t)
	d := openTestDB(t)

	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)

	c := &ignoresCancelClient{cancel: cancel}

	syncer := NewSyncer(
		map[string]Client{"github.com": c}, d, nil,
		[]RepoRef{{Owner: "o", Name: "r", PlatformHost: "github.com"}},
		time.Minute, nil, nil,
	)

	var syncCompletedCalls atomic.Int32
	syncer.SetOnSyncCompleted(func(_ []RepoSyncResult) {
		syncCompletedCalls.Add(1)
	})

	syncer.RunOnce(ctx)

	assert.Zero(int(syncCompletedCalls.Load()),
		"onSyncCompleted must not fire when ctx was canceled "+
			"during the run, even if syncRepo returned success")
	status := syncer.Status()
	assert.False(status.Running, "sync must stop")
	assert.NotEmpty(status.LastError,
		"status must record the cancel as an error")
}

// --- Index/Detail Split Tests ---

// detailTrackingClient tracks which API methods are called so tests
// can verify that the index phase does NOT call GetPullRequest while
// the detail drain does.
type detailTrackingClient struct {
	mockClient
	getPRCalls atomic.Int32
}

func (c *detailTrackingClient) GetPullRequest(
	ctx context.Context, owner, repo string, number int,
) (*gh.PullRequest, error) {
	c.trackCall()
	c.getPRCalls.Add(1)
	return c.mockClient.GetPullRequest(ctx, owner, repo, number)
}

type conditionalPRTrackingClient struct {
	detailTrackingClient
	receivedETag     string
	conditionalCalls atomic.Int32
	notModified      bool
	nextETag         string
}

func (c *conditionalPRTrackingClient) GetPullRequestIfChanged(
	ctx context.Context,
	owner, repo string,
	number int,
	etag string,
) (*gh.PullRequest, string, bool, error) {
	c.conditionalCalls.Add(1)
	c.receivedETag = etag
	if c.notModified {
		return nil, etag, true, nil
	}
	c.getPRCalls.Add(1)
	pr, err := c.mockClient.GetPullRequest(ctx, owner, repo, number)
	return pr, c.nextETag, false, err
}

type conditionalIssueTrackingClient struct {
	mockClient
	receivedETag     string
	conditionalCalls atomic.Int32
	notModified      bool
	nextETag         string
}

func (c *conditionalIssueTrackingClient) GetIssueIfChanged(
	ctx context.Context,
	owner, repo string,
	number int,
	etag string,
) (*gh.Issue, string, bool, error) {
	c.conditionalCalls.Add(1)
	c.receivedETag = etag
	if c.notModified {
		return nil, etag, true, nil
	}
	issue, err := c.GetIssue(ctx, owner, repo, number)
	return issue, c.nextETag, false, err
}

func TestRunOnceIndexOnly(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	d := openTestDB(t)

	now := time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC)
	mc := &detailTrackingClient{}
	mc.openPRs = []*gh.PullRequest{
		buildOpenPR(1, now),
		buildOpenPR(2, now),
	}

	// Budget=0 disables detail drain entirely.
	syncer := NewSyncer(
		map[string]Client{"github.com": mc}, d, nil,
		[]RepoRef{{
			Owner: "owner", Name: "repo",
			PlatformHost: "github.com",
		}},
		time.Minute, nil, nil,
	)

	syncer.RunOnce(ctx)

	// ListOpenPullRequests should have been called.
	assert.True(mc.listOpenPRsCalled,
		"index scan should call ListOpenPullRequests")

	// GetPullRequest should NOT have been called (no detail fetch).
	assert.Zero(int(mc.getPRCalls.Load()),
		"index-only sync should not call GetPullRequest")

	// PRs should be in DB with nil detail_fetched_at.
	pr1, err := d.GetMergeRequest(ctx, "owner", "repo", 1)
	require.NoError(err)
	require.NotNil(pr1)
	assert.Equal(1, pr1.Number)
	assert.Nil(pr1.DetailFetchedAt,
		"detail_fetched_at should be nil after index-only sync")

	pr2, err := d.GetMergeRequest(ctx, "owner", "repo", 2)
	require.NoError(err)
	require.NotNil(pr2)
	assert.Equal(2, pr2.Number)
	assert.Nil(pr2.DetailFetchedAt,
		"detail_fetched_at should be nil after index-only sync")

	// No timeline events should exist (no detail fetch).
	events, err := d.ListMREvents(ctx, pr1.ID)
	require.NoError(err)
	assert.Empty(events,
		"no events should exist after index-only sync")
}

func TestRunOnceDetailDrain(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	d := openTestDB(t)

	now := time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC)
	ciState := "success"

	mc := &detailTrackingClient{}
	mc.openPRs = []*gh.PullRequest{
		buildOpenPR(1, now),
		buildOpenPR(2, now),
	}
	mc.comments = []*gh.IssueComment{}
	mc.reviews = []*gh.PullRequestReview{}
	mc.commits = []*gh.RepositoryCommit{}
	mc.ciStatus = &gh.CombinedStatus{State: &ciState}

	// Budget=500 allows detail drain to run.
	syncer := NewSyncer(
		map[string]Client{"github.com": mc}, d, nil,
		[]RepoRef{{
			Owner: "owner", Name: "repo",
			PlatformHost: "github.com",
		}},
		time.Minute, nil, testBudget(500),
	)

	syncer.RunOnce(ctx)

	// GetPullRequest should have been called for each PR
	// during detail drain.
	assert.GreaterOrEqual(int(mc.getPRCalls.Load()), 2,
		"detail drain should call GetPullRequest for open PRs")

	// Both PRs should have detail_fetched_at set.
	pr1, err := d.GetMergeRequest(ctx, "owner", "repo", 1)
	require.NoError(err)
	require.NotNil(pr1)
	assert.NotNil(pr1.DetailFetchedAt,
		"detail_fetched_at should be set after detail drain")

	pr2, err := d.GetMergeRequest(ctx, "owner", "repo", 2)
	require.NoError(err)
	require.NotNil(pr2)
	assert.NotNil(pr2.DetailFetchedAt,
		"detail_fetched_at should be set after detail drain")
}

func TestFetchMRDetailUsesPersistedPullRequestETag(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	d := openTestDB(t)

	repo := RepoRef{Owner: "owner", Name: "repo", PlatformHost: "github.com"}
	repoID, err := d.UpsertRepo(ctx, db.GitHubRepoIdentity("github.com", repo.Owner, repo.Name))
	require.NoError(err)
	updatedAt := time.Date(2024, 6, 1, 10, 0, 0, 0, time.UTC)
	detailFetchedAt := time.Date(2024, 6, 1, 9, 0, 0, 0, time.UTC)
	_, err = d.UpsertMergeRequest(ctx, &db.MergeRequest{
		RepoID:          repoID,
		PlatformID:      1000,
		Number:          1,
		URL:             "https://github.com/owner/repo/pull/1",
		Title:           "test PR",
		Author:          "alice",
		State:           "open",
		HeadBranch:      "feature-branch",
		BaseBranch:      "main",
		PlatformHeadSHA: "abc123def456",
		CreatedAt:       updatedAt,
		UpdatedAt:       updatedAt,
		LastActivityAt:  updatedAt,
		DetailFetchedAt: &detailFetchedAt,
	})
	require.NoError(err)
	require.NoError(d.UpsertHTTPEtag(
		ctx, "github", "github.com", "owner", "repo",
		"pull_request", 1, `"etag-v1"`,
	))

	mc := &conditionalPRTrackingClient{notModified: true}
	mc.comments = []*gh.IssueComment{{ID: new(int64)}}
	mc.reviews = []*gh.PullRequestReview{{ID: new(int64)}}
	mc.commits = []*gh.RepositoryCommit{{SHA: new(string)}}
	syncer := NewSyncer(
		map[string]Client{"github.com": mc}, d, nil,
		[]RepoRef{repo},
		time.Minute, nil, testBudget(1000),
	)

	_, err = syncer.fetchMRDetail(ctx, repo, repoID, 1, false)
	require.NoError(err)

	assert.Equal(int32(1), mc.conditionalCalls.Load())
	assert.Equal(`"etag-v1"`, mc.receivedETag)
	assert.Zero(int(mc.getPRCalls.Load()),
		"304 should skip the unconditional PR detail fetch")
	assert.Zero(int(mc.listIssueCommentsCalled.Load()),
		"304 should skip timeline/comment refresh")
}

func TestFetchMRDetailPersistsPullRequestETag(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	d := openTestDB(t)

	repo := RepoRef{Owner: "owner", Name: "repo", PlatformHost: "github.com"}
	repoID, err := d.UpsertRepo(ctx, db.GitHubRepoIdentity("github.com", repo.Owner, repo.Name))
	require.NoError(err)
	updatedAt := time.Date(2024, 6, 1, 10, 0, 0, 0, time.UTC)

	mc := &conditionalPRTrackingClient{nextETag: `"etag-v2"`}
	mc.singlePR = buildOpenPR(1, updatedAt)
	mc.comments = []*gh.IssueComment{}
	mc.reviews = []*gh.PullRequestReview{}
	mc.commits = []*gh.RepositoryCommit{}
	mc.ciStatus = &gh.CombinedStatus{State: new(string)}
	syncer := NewSyncer(
		map[string]Client{"github.com": mc}, d, nil,
		[]RepoRef{repo},
		time.Minute, nil, testBudget(1000),
	)

	_, err = syncer.fetchMRDetail(ctx, repo, repoID, 1, false)
	require.NoError(err)

	etag, err := d.GetHTTPEtag(
		ctx, "github", "github.com", "owner", "repo",
		"pull_request", 1,
	)
	require.NoError(err)
	assert.Equal(`"etag-v2"`, etag)
}

func TestFetchMRDetailDoesNotPersistPullRequestETagWhenDetailRefreshFails(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	d := openTestDB(t)

	repo := RepoRef{Owner: "owner", Name: "repo", PlatformHost: "github.com"}
	repoID, err := d.UpsertRepo(ctx, db.GitHubRepoIdentity("github.com", repo.Owner, repo.Name))
	require.NoError(err)
	require.NoError(d.UpsertHTTPEtag(
		ctx, "github", "github.com", "owner", "repo",
		"pull_request", 1, `"etag-v1"`,
	))

	updatedAt := time.Date(2024, 6, 1, 10, 0, 0, 0, time.UTC)
	mc := &conditionalPRTrackingClient{nextETag: `"etag-v2"`}
	mc.singlePR = buildOpenPR(1, updatedAt)
	mc.listIssueCommentsErr = fmt.Errorf("transient comments failure")
	syncer := NewSyncer(
		map[string]Client{"github.com": mc}, d, nil,
		[]RepoRef{repo},
		time.Minute, nil, testBudget(1000),
	)

	_, err = syncer.fetchMRDetail(ctx, repo, repoID, 1, false)
	require.Error(err)

	etag, err := d.GetHTTPEtag(
		ctx, "github", "github.com", "owner", "repo",
		"pull_request", 1,
	)
	require.NoError(err)
	assert.Equal(`"etag-v1"`, etag)
}

func TestFetchIssueDetailUsesPersistedIssueETag(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	d := openTestDB(t)

	repo := RepoRef{Owner: "owner", Name: "repo", PlatformHost: "github.com"}
	repoID, err := d.UpsertRepo(ctx, db.GitHubRepoIdentity("github.com", repo.Owner, repo.Name))
	require.NoError(err)
	updatedAt := time.Date(2024, 6, 1, 10, 0, 0, 0, time.UTC)
	detailFetchedAt := time.Date(2024, 6, 1, 9, 0, 0, 0, time.UTC)
	_, err = d.UpsertIssue(ctx, &db.Issue{
		RepoID:          repoID,
		PlatformID:      1000,
		Number:          1,
		URL:             "https://github.com/owner/repo/issues/1",
		Title:           "test issue",
		Author:          "alice",
		State:           "open",
		CreatedAt:       updatedAt,
		UpdatedAt:       updatedAt,
		LastActivityAt:  updatedAt,
		DetailFetchedAt: &detailFetchedAt,
	})
	require.NoError(err)
	require.NoError(d.UpsertHTTPEtag(
		ctx, "github", "github.com", "owner", "repo",
		"issue", 1, `"issue-etag-v1"`,
	))

	mc := &conditionalIssueTrackingClient{notModified: true}
	mc.comments = []*gh.IssueComment{{ID: new(int64)}}
	syncer := NewSyncer(
		map[string]Client{"github.com": mc}, d, nil,
		[]RepoRef{repo},
		time.Minute, nil, testBudget(1000),
	)

	_, err = syncer.fetchIssueDetail(ctx, repo, repoID, 1)
	require.NoError(err)

	assert.Equal(int32(1), mc.conditionalCalls.Load())
	assert.Equal(`"issue-etag-v1"`, mc.receivedETag)
	assert.Zero(int(mc.listIssueCommentsCalled.Load()),
		"304 should skip issue comment refresh")
}

func TestFetchIssueDetailPersistsIssueETag(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	d := openTestDB(t)

	repo := RepoRef{Owner: "owner", Name: "repo", PlatformHost: "github.com"}
	repoID, err := d.UpsertRepo(ctx, db.GitHubRepoIdentity("github.com", repo.Owner, repo.Name))
	require.NoError(err)
	updatedAt := time.Date(2024, 6, 1, 10, 0, 0, 0, time.UTC)
	issueID := int64(1000)
	issueNumber := 1
	issueTitle := "test issue"
	issueState := "open"
	issueURL := "https://github.com/owner/repo/issues/1"

	mc := &conditionalIssueTrackingClient{nextETag: `"issue-etag-v2"`}
	mc.getIssueFn = func(context.Context, string, string, int) (*gh.Issue, error) {
		return &gh.Issue{
			ID:        &issueID,
			Number:    &issueNumber,
			Title:     &issueTitle,
			State:     &issueState,
			HTMLURL:   &issueURL,
			CreatedAt: makeTimestamp(updatedAt),
			UpdatedAt: makeTimestamp(updatedAt),
		}, nil
	}
	mc.comments = []*gh.IssueComment{}
	syncer := NewSyncer(
		map[string]Client{"github.com": mc}, d, nil,
		[]RepoRef{repo},
		time.Minute, nil, testBudget(1000),
	)

	_, err = syncer.fetchIssueDetail(ctx, repo, repoID, 1)
	require.NoError(err)

	etag, err := d.GetHTTPEtag(
		ctx, "github", "github.com", "owner", "repo",
		"issue", 1,
	)
	require.NoError(err)
	assert.Equal(`"issue-etag-v2"`, etag)
}

func TestFetchIssueDetailDoesNotPersistIssueETagWhenDetailRefreshFails(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	d := openTestDB(t)

	repo := RepoRef{Owner: "owner", Name: "repo", PlatformHost: "github.com"}
	repoID, err := d.UpsertRepo(ctx, db.GitHubRepoIdentity("github.com", repo.Owner, repo.Name))
	require.NoError(err)
	require.NoError(d.UpsertHTTPEtag(
		ctx, "github", "github.com", "owner", "repo",
		"issue", 1, `"issue-etag-v1"`,
	))

	updatedAt := time.Date(2024, 6, 1, 10, 0, 0, 0, time.UTC)
	issueID := int64(1000)
	issueNumber := 1
	issueTitle := "test issue"
	issueState := "open"
	issueURL := "https://github.com/owner/repo/issues/1"

	mc := &conditionalIssueTrackingClient{nextETag: `"issue-etag-v2"`}
	mc.getIssueFn = func(context.Context, string, string, int) (*gh.Issue, error) {
		return &gh.Issue{
			ID:        &issueID,
			Number:    &issueNumber,
			Title:     &issueTitle,
			State:     &issueState,
			HTMLURL:   &issueURL,
			CreatedAt: makeTimestamp(updatedAt),
			UpdatedAt: makeTimestamp(updatedAt),
		}, nil
	}
	mc.listIssueCommentsErr = fmt.Errorf("transient comments failure")
	syncer := NewSyncer(
		map[string]Client{"github.com": mc}, d, nil,
		[]RepoRef{repo},
		time.Minute, nil, testBudget(1000),
	)

	_, err = syncer.fetchIssueDetail(ctx, repo, repoID, 1)
	require.Error(err)

	etag, err := d.GetHTTPEtag(
		ctx, "github", "github.com", "owner", "repo",
		"issue", 1,
	)
	require.NoError(err)
	assert.Equal(`"issue-etag-v1"`, etag)
}

func TestBulkGraphQLGateUsesLocalMergeRequestCount(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	d := openTestDB(t)

	repo := RepoRef{Owner: "owner", Name: "repo", PlatformHost: "github.com"}
	repoID, err := d.UpsertRepo(ctx, db.GitHubRepoIdentity("github.com", repo.Owner, repo.Name))
	require.NoError(err)

	now := time.Date(2024, 6, 1, 10, 0, 0, 0, time.UTC)
	for number := 1; number <= largeRepoBulkGraphQLThreshold; number++ {
		_, err := d.UpsertMergeRequest(ctx, &db.MergeRequest{
			RepoID:         repoID,
			PlatformID:     int64(number * 1000),
			Number:         number,
			URL:            fmt.Sprintf("https://github.com/owner/repo/pull/%d", number),
			Title:          fmt.Sprintf("test PR %d", number),
			Author:         "alice",
			State:          "open",
			CreatedAt:      now,
			UpdatedAt:      now,
			LastActivityAt: now,
		})
		require.NoError(err)
	}

	syncer := NewSyncer(nil, d, nil, []RepoRef{repo}, time.Minute, nil, nil)

	assert.False(syncer.shouldUseBulkGraphQLForMRs(ctx, repo, repoID, 1),
		"local open count should gate large-repo bulk behavior even when the fetched set is small")
}

func TestBulkGraphQLGateUsesLocalIssueCount(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	d := openTestDB(t)

	repo := RepoRef{Owner: "owner", Name: "repo", PlatformHost: "github.com"}
	repoID, err := d.UpsertRepo(ctx, db.GitHubRepoIdentity("github.com", repo.Owner, repo.Name))
	require.NoError(err)

	now := time.Date(2024, 6, 1, 10, 0, 0, 0, time.UTC)
	for number := 1; number <= largeRepoBulkGraphQLThreshold; number++ {
		_, err := d.UpsertIssue(ctx, &db.Issue{
			RepoID:         repoID,
			PlatformID:     int64(number * 1000),
			Number:         number,
			URL:            fmt.Sprintf("https://github.com/owner/repo/issues/%d", number),
			Title:          fmt.Sprintf("test issue %d", number),
			Author:         "alice",
			State:          "open",
			CreatedAt:      now,
			UpdatedAt:      now,
			LastActivityAt: now,
		})
		require.NoError(err)
	}

	syncer := NewSyncer(nil, d, nil, []RepoRef{repo}, time.Minute, nil, nil)

	assert.False(syncer.shouldUseBulkGraphQLForIssues(ctx, repo, repoID, 1),
		"local open count should gate large-repo bulk behavior even when the fetched set is small")
}

func TestRunOnceLargeExistingRepoSkipsBulkGraphQLAndFetchesChangedPRDetail(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	d := openTestDB(t)

	repo := RepoRef{
		Owner:        "owner",
		Name:         "repo",
		PlatformHost: "github.com",
	}
	repoID, err := d.UpsertRepo(ctx, db.GitHubRepoIdentity("github.com", repo.Owner, repo.Name))
	require.NoError(err)

	unchangedAt := time.Date(2024, 6, 1, 10, 0, 0, 0, time.UTC)
	changedAt := time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC)
	detailFetchedAt := time.Date(2024, 6, 1, 11, 0, 0, 0, time.UTC)
	openPRs := make([]*gh.PullRequest, 0, syncProgressLogInterval+1)
	for number := 1; number <= syncProgressLogInterval+1; number++ {
		updatedAt := unchangedAt
		if number == 1 {
			updatedAt = changedAt
		}
		openPRs = append(openPRs, buildOpenPR(number, updatedAt))
		_, err := d.UpsertMergeRequest(ctx, &db.MergeRequest{
			RepoID:          repoID,
			PlatformID:      int64(number * 1000),
			Number:          number,
			URL:             fmt.Sprintf("https://github.com/owner/repo/pull/%d", number),
			Title:           fmt.Sprintf("test PR %d", number),
			Author:          "alice",
			State:           "open",
			HeadBranch:      "feature-branch",
			BaseBranch:      "main",
			PlatformHeadSHA: "abc123def456",
			CreatedAt:       unchangedAt,
			UpdatedAt:       unchangedAt,
			LastActivityAt:  unchangedAt,
			DetailFetchedAt: &detailFetchedAt,
		})
		require.NoError(err)
	}

	mc := &detailTrackingClient{}
	mc.openPRs = openPRs
	mc.listOpenIssuesErr = notModifiedErr()
	mc.comments = []*gh.IssueComment{}
	mc.reviews = []*gh.PullRequestReview{}
	mc.commits = []*gh.RepositoryCommit{}
	mc.ciStatus = &gh.CombinedStatus{State: new(string)}

	var graphQLPRCalls atomic.Int32
	gqlSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), "pullRequests") {
			graphQLPRCalls.Add(1)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"errors":[{"message":"bulk PR fetch should be skipped"}]}`))
	}))
	defer gqlSrv.Close()

	syncer := NewSyncer(
		map[string]Client{"github.com": mc}, d, nil,
		[]RepoRef{repo},
		time.Minute, nil, testBudget(10000),
	)
	syncer.SetFetchers(map[string]*GraphQLFetcher{
		"github.com": NewGraphQLFetcherWithClient(
			githubv4.NewEnterpriseClient(gqlSrv.URL, gqlSrv.Client()),
			nil,
		),
	})

	syncer.RunOnce(ctx)

	assert.True(mc.listOpenPRsCalled,
		"large repo refresh should still read the open PR index")
	assert.Zero(int(graphQLPRCalls.Load()),
		"large existing repo refresh should not bulk-fetch every PR through GraphQL")
	assert.Equal(int32(1), mc.getPRCalls.Load(),
		"only the changed PR should be fetched by the detail drain")
	pr, err := d.GetMergeRequest(ctx, "owner", "repo", 1)
	require.NoError(err)
	require.NotNil(pr)
	assert.True(pr.UpdatedAt.Equal(changedAt))
}

func TestDetailDrainUsesProviderReadersForNonGitHub(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	d := openTestDB(t)
	now := time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC)
	repo := RepoRef{
		Platform:     platform.KindGitLab,
		PlatformHost: "gitlab.com",
		Owner:        "acme",
		Name:         "widget",
	}
	repoID, err := d.UpsertRepo(ctx, platform.DBRepoIdentity(platformRepoRef(repo)))
	require.NoError(err)
	_, err = d.UpsertMergeRequest(ctx, &db.MergeRequest{
		RepoID:          repoID,
		PlatformID:      1001,
		Number:          7,
		URL:             "https://gitlab.com/acme/widget/-/merge_requests/7",
		Title:           "stale MR",
		Author:          "ada",
		State:           "open",
		HeadBranch:      "feature",
		BaseBranch:      "main",
		PlatformHeadSHA: "old",
		PlatformBaseSHA: "base",
		CreatedAt:       now.Add(-time.Hour),
		UpdatedAt:       now.Add(-time.Hour),
		LastActivityAt:  now.Add(-time.Hour),
	})
	require.NoError(err)
	_, err = d.UpsertIssue(ctx, &db.Issue{
		RepoID:         repoID,
		PlatformID:     2001,
		Number:         11,
		URL:            "https://gitlab.com/acme/widget/-/issues/11",
		Title:          "stale issue",
		Author:         "grace",
		State:          "open",
		CreatedAt:      now.Add(-time.Hour),
		UpdatedAt:      now.Add(-time.Hour),
		LastActivityAt: now.Add(-time.Hour),
	})
	require.NoError(err)

	provider := &syncTestReadProvider{
		syncTestProvider: syncTestProvider{
			kind: platform.KindGitLab,
			host: "gitlab.com",
		},
		mergeRequests: []platform.MergeRequest{{
			Repo:           platformRepoRef(repo),
			PlatformID:     1001,
			Number:         7,
			URL:            "https://gitlab.com/acme/widget/-/merge_requests/7",
			Title:          "fresh MR detail",
			Author:         "ada",
			State:          "open",
			HeadBranch:     "feature",
			BaseBranch:     "main",
			HeadSHA:        "new",
			BaseSHA:        "base",
			CreatedAt:      now,
			UpdatedAt:      now,
			LastActivityAt: now,
		}},
		issues: []platform.Issue{{
			Repo:           platformRepoRef(repo),
			PlatformID:     2001,
			Number:         11,
			URL:            "https://gitlab.com/acme/widget/-/issues/11",
			Title:          "fresh issue detail",
			Author:         "grace",
			State:          "open",
			CreatedAt:      now,
			UpdatedAt:      now,
			LastActivityAt: now,
		}},
	}
	registry, err := platform.NewRegistry(provider)
	require.NoError(err)
	rateKey := RateBucketKey("gitlab", "gitlab.com")
	syncer := NewSyncer(nil, d, nil, []RepoRef{repo}, time.Minute, nil, map[string]*SyncBudget{
		rateKey: NewSyncBudget(100),
	})
	syncer.clients = registry

	syncer.drainDetailQueue(ctx, map[string]bool{rateKey: true})

	assert.Equal(int32(1), provider.getMRCalls.Load())
	assert.Equal(int32(1), provider.getIssueCalls.Load())
	mr, err := d.GetMergeRequest(ctx, "acme", "widget", 7)
	require.NoError(err)
	require.NotNil(mr)
	assert.Equal("fresh MR detail", mr.Title)
	assert.NotNil(mr.DetailFetchedAt)
	issue, err := d.GetIssue(ctx, "acme", "widget", 11)
	require.NoError(err)
	require.NotNil(issue)
	assert.Equal("fresh issue detail", issue.Title)
	assert.NotNil(issue.DetailFetchedAt)
}

func TestDetailDrainDisambiguatesSameHostOwnerNameAcrossProviders(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	d := openTestDB(t)
	now := time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC)
	host := "code.example.com"
	githubRepo := RepoRef{
		Platform:     platform.KindGitHub,
		PlatformHost: host,
		Owner:        "acme",
		Name:         "widget",
	}
	gitlabRepo := RepoRef{
		Platform:     platform.KindGitLab,
		PlatformHost: host,
		Owner:        "acme",
		Name:         "widget",
	}
	githubRepoID, err := d.UpsertRepo(ctx, platform.DBRepoIdentity(platformRepoRef(githubRepo)))
	require.NoError(err)
	gitlabRepoID, err := d.UpsertRepo(ctx, platform.DBRepoIdentity(platformRepoRef(gitlabRepo)))
	require.NoError(err)
	require.NotEqual(githubRepoID, gitlabRepoID)

	_, err = d.UpsertMergeRequest(ctx, &db.MergeRequest{
		RepoID:          gitlabRepoID,
		PlatformID:      7001,
		Number:          7,
		URL:             "https://code.example.com/acme/widget/-/merge_requests/7",
		Title:           "stale gitlab MR",
		Author:          "ada",
		State:           "open",
		HeadBranch:      "feature",
		BaseBranch:      "main",
		PlatformHeadSHA: "old",
		PlatformBaseSHA: "base",
		CreatedAt:       now.Add(-time.Hour),
		UpdatedAt:       now.Add(-time.Hour),
		LastActivityAt:  now.Add(-time.Hour),
	})
	require.NoError(err)

	gitlabProvider := &syncTestReadProvider{
		syncTestProvider: syncTestProvider{
			kind: platform.KindGitLab,
			host: host,
		},
		mergeRequests: []platform.MergeRequest{{
			Repo:           platformRepoRef(gitlabRepo),
			PlatformID:     7001,
			Number:         7,
			URL:            "https://code.example.com/acme/widget/-/merge_requests/7",
			Title:          "fresh gitlab MR",
			Author:         "ada",
			State:          "open",
			HeadBranch:     "feature",
			BaseBranch:     "main",
			HeadSHA:        "new",
			BaseSHA:        "base",
			CreatedAt:      now,
			UpdatedAt:      now,
			LastActivityAt: now,
		}},
	}
	githubClient := &mockClient{
		getPullRequestFn: func(context.Context, string, string, int) (*gh.PullRequest, error) {
			return nil, errors.New("wrong provider")
		},
	}
	registry, err := platform.NewRegistry(
		gitHubClientProvider{host: host, client: githubClient},
		gitlabProvider,
	)
	require.NoError(err)
	rateKey := RateBucketKey("gitlab", host)
	syncer := NewSyncer(nil, d, nil, []RepoRef{
		githubRepo,
		gitlabRepo,
	}, time.Minute, nil, map[string]*SyncBudget{
		rateKey: NewSyncBudget(100),
	})
	syncer.clients = registry

	syncer.drainDetailQueue(ctx, map[string]bool{rateKey: true})

	assert.Equal(int32(1), gitlabProvider.getMRCalls.Load())
	mr, err := d.GetMergeRequestByRepoIDAndNumber(ctx, gitlabRepoID, 7)
	require.NoError(err)
	require.NotNil(mr)
	assert.Equal("fresh gitlab MR", mr.Title)
	assert.NotNil(mr.DetailFetchedAt)
}

func TestDetailQueueWatchedKeyIncludesProviderIdentity(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	d := openTestDB(t)
	host := "code.example.com"
	now := time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC)
	githubRepo := RepoRef{
		Platform:     platform.KindGitHub,
		PlatformHost: host,
		Owner:        "acme",
		Name:         "widget",
	}
	gitlabRepo := RepoRef{
		Platform:     platform.KindGitLab,
		PlatformHost: host,
		Owner:        "acme",
		Name:         "widget",
	}
	githubRepoID, err := d.UpsertRepo(ctx, platform.DBRepoIdentity(platformRepoRef(githubRepo)))
	require.NoError(err)
	gitlabRepoID, err := d.UpsertRepo(ctx, platform.DBRepoIdentity(platformRepoRef(gitlabRepo)))
	require.NoError(err)
	for _, repoID := range []int64{githubRepoID, gitlabRepoID} {
		_, err = d.UpsertMergeRequest(ctx, &db.MergeRequest{
			RepoID:         repoID,
			PlatformID:     repoID * 100,
			Number:         7,
			Title:          "same number",
			Author:         "ada",
			State:          "open",
			HeadBranch:     "feature",
			BaseBranch:     "main",
			CreatedAt:      now,
			UpdatedAt:      now,
			LastActivityAt: now,
		})
		require.NoError(err)
	}
	syncer := NewSyncer(nil, d, nil, []RepoRef{
		githubRepo,
		gitlabRepo,
	}, time.Minute, nil, nil)
	syncer.SetWatchedMRs([]WatchedMR{{
		Platform:     platform.KindGitLab,
		PlatformHost: host,
		Owner:        "acme",
		Name:         "widget",
		Number:       7,
	}})

	items := syncer.buildDetailQueueItems(ctx)

	require.Len(items, 2)
	watchedByPlatform := map[platform.Kind]bool{}
	for _, item := range items {
		watchedByPlatform[item.Platform] = item.Watched
	}
	assert.False(watchedByPlatform[platform.KindGitHub])
	assert.True(watchedByPlatform[platform.KindGitLab])
}

func TestDetailQueueDerivesPendingCIFromCachedChecks(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	d := openTestDB(t)

	now := time.Date(2026, 5, 21, 10, 0, 0, 0, time.UTC)
	fetchedAt := now.Add(-5 * time.Minute)
	repo := RepoRef{
		Platform:     platform.KindGitHub,
		PlatformHost: "github.com",
		Owner:        "owner",
		Name:         "repo",
	}
	repoID, err := d.UpsertRepo(ctx, platform.DBRepoIdentity(platformRepoRef(repo)))
	require.NoError(err)
	_, err = d.UpsertMergeRequest(ctx, &db.MergeRequest{
		RepoID:          repoID,
		PlatformID:      1001,
		Number:          1,
		URL:             "https://github.com/owner/repo/pull/1",
		Title:           "pending ci",
		State:           "open",
		HeadBranch:      "feature",
		BaseBranch:      "main",
		PlatformHeadSHA: "head-sha",
		CIChecksJSON:    `[{"name":"build","status":"in_progress","conclusion":""}]`,
		CIHadPending:    false,
		CreatedAt:       now,
		UpdatedAt:       now,
		LastActivityAt:  now,
		DetailFetchedAt: &fetchedAt,
	})
	require.NoError(err)

	syncer := NewSyncer(nil, d, nil, []RepoRef{repo}, time.Minute, nil, nil)

	items := syncer.buildDetailQueueItems(ctx)
	require.Len(items, 1)
	assert.True(items[0].CIHadPending)
	queue := BuildQueue(items, now)
	require.Len(queue, 1)
	assert.Equal(1, queue[0].Number)
}

func TestDetailDrainRespectsBudget(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	d := openTestDB(t)

	now := time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC)
	ciState := "success"

	// Create 5 PRs.
	var prs []*gh.PullRequest
	for i := 1; i <= 5; i++ {
		prs = append(prs, buildOpenPR(i, now))
	}

	// Index overhead: GetRepo(1) + ListPRs(1) + ListIssues(1) +
	// GetUser(1, deduplicated by singleflight) = 4 calls. One PR
	// detail = 9 calls. Budget of 15 covers index + 1 detail (13)
	// with 2 remaining, which is below the 9 needed for a 2nd.
	budget := testBudget(15)
	mc := &detailTrackingClient{}
	mc.budget = budget["github.com"]
	mc.openPRs = prs
	mc.comments = []*gh.IssueComment{}
	mc.reviews = []*gh.PullRequestReview{}
	mc.commits = []*gh.RepositoryCommit{}
	mc.ciStatus = &gh.CombinedStatus{State: &ciState}

	// Budget covers index overhead + 1 PR detail fetch, not 2.
	syncer := NewSyncer(
		map[string]Client{"github.com": mc}, d, nil,
		[]RepoRef{{
			Owner: "owner", Name: "repo",
			PlatformHost: "github.com",
		}},
		time.Minute, nil, budget,
	)

	syncer.RunOnce(ctx)

	// All 5 PRs should be in DB (index scan).
	for i := 1; i <= 5; i++ {
		pr, err := d.GetMergeRequest(
			ctx, "owner", "repo", i,
		)
		require.NoError(err)
		require.NotNil(pr, "PR #%d should exist", i)
	}

	// Only 1 PR should have detail_fetched_at set (budget
	// allows at most 1 full detail fetch).
	detailCount := 0
	for i := 1; i <= 5; i++ {
		pr, _ := d.GetMergeRequest(
			ctx, "owner", "repo", i,
		)
		if pr != nil && pr.DetailFetchedAt != nil {
			detailCount++
		}
	}
	assert.Equal(1, detailCount,
		"budget should limit detail fetches to 1 PR")

	// Budget should be spent.
	hostBudget := syncer.Budgets()["github.com"]
	require.NotNil(hostBudget)
	assert.Positive(hostBudget.Spent(),
		"budget should have been spent")
}

func TestBudgetResetOnRateWindowReset(t *testing.T) {
	assert := Assert.New(t)
	d := openTestDB(t)

	rt := NewRateTracker(d, "github.com", "rest")
	budget := NewSyncBudget(100)
	rt.SetOnWindowReset(budget.Reset)

	// Simulate some spending.
	budget.Spend(50)
	assert.Equal(50, budget.Spent())

	// First rate update sets remaining to 4999.
	rt.UpdateFromRate(Rate{
		Remaining: 4999,
		Limit:     5000,
		Reset:     time.Now().Add(time.Hour),
	})

	// No window reset yet (first contact).
	assert.Equal(50, budget.Spent(),
		"budget should not reset on first contact")

	// Simulate rate decrease (normal usage).
	rt.UpdateFromRate(Rate{
		Remaining: 4990,
		Limit:     5000,
		Reset:     time.Now().Add(time.Hour),
	})
	assert.Equal(50, budget.Spent(),
		"budget should not reset on normal decrease")

	// Simulate window expiry: move resetAt to the past.
	pastReset := time.Now().Add(-1 * time.Second)
	rt.SetResetAtForTesting(pastReset)

	// Simulate window reset (remaining jumps up + old resetAt passed).
	rt.UpdateFromRate(Rate{
		Remaining: 5000,
		Limit:     5000,
		Reset:     time.Now().Add(2 * time.Hour),
	})
	assert.Equal(0, budget.Spent(),
		"budget should reset when rate window resets")
}

func TestSyncMRSkipsGetUserWhenDisplayNameCached(t *testing.T) {
	assert := Assert.New(t)
	d := openTestDB(t)

	sha := "abc123"
	number := 1
	author := "testuser"
	title := "Test PR"
	state := "open"
	url := "https://github.com/acme/widget/pull/1"
	now := &gh.Timestamp{Time: time.Now()}

	mock := &mockClient{
		singlePR: &gh.PullRequest{
			Number:    &number,
			Title:     &title,
			State:     &state,
			HTMLURL:   &url,
			User:      &gh.User{Login: &author},
			UpdatedAt: now,
			CreatedAt: now,
			Head:      &gh.PullRequestBranch{SHA: &sha, Ref: new("feature")},
			Base:      &gh.PullRequestBranch{Ref: new("main")},
		},
		checkRuns: []*gh.CheckRun{{
			Name:       new("ci"),
			Status:     new("completed"),
			Conclusion: new("success"),
		}},
	}

	syncer := NewSyncer(
		map[string]Client{"github.com": mock},
		d, nil,
		[]RepoRef{{Owner: "acme", Name: "widget", PlatformHost: "github.com"}},
		time.Minute,
		nil,
		nil,
	)

	// First sync: GetUser should be called to resolve display name
	err := syncer.SyncMR(t.Context(), "acme", "widget", 1)
	require.NoError(t, err)
	assert.Equal(int32(1), mock.getUserCalls.Load())

	// Second sync: display name is in DB, GetUser should be skipped
	err = syncer.SyncMR(t.Context(), "acme", "widget", 1)
	require.NoError(t, err)
	assert.Equal(int32(1), mock.getUserCalls.Load(),
		"GetUser should not be called again when display name is cached")
}

func TestRefreshCIStatusAlwaysFetchesCombined(t *testing.T) {
	assert := Assert.New(t)
	d := openTestDB(t)

	mock := &mockClient{
		checkRuns: []*gh.CheckRun{{
			Name:       new("ci"),
			Status:     new("completed"),
			Conclusion: new("success"),
		}},
		ciStatus: &gh.CombinedStatus{
			State:      new("success"),
			TotalCount: new(1),
		},
	}

	syncer := NewSyncer(
		map[string]Client{"github.com": mock},
		d, nil,
		[]RepoRef{{Owner: "acme", Name: "widget", PlatformHost: "github.com"}},
		time.Minute,
		nil,
		nil,
	)

	repoID, _ := d.UpsertRepo(t.Context(), db.GitHubRepoIdentity("github.com", "acme", "widget"))
	err := syncer.refreshCIStatus(
		t.Context(),
		RepoRef{Owner: "acme", Name: "widget", PlatformHost: "github.com"},
		repoID,
		1,
		"abc123",
	)
	require.NoError(t, err)

	// GetCombinedStatus should always be called for correctness
	// (legacy commit statuses exist alongside check runs).
	assert.Equal(int32(1), mock.getCombinedCalls.Load(),
		"GetCombinedStatus should always be called")
}

func TestRefreshCIStatusPreservesExistingStatusWhenChecksFail(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	repoID, err := d.UpsertRepo(t.Context(), db.GitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	now := time.Now().UTC()
	_, err = d.UpsertMergeRequest(t.Context(), &db.MergeRequest{
		RepoID:          repoID,
		PlatformID:      1001,
		Number:          1,
		Title:           "pending",
		State:           "open",
		PlatformHeadSHA: "abc123",
		CIStatus:        "pending",
		CIChecksJSON:    `[{"name":"build","status":"in_progress"}]`,
		CreatedAt:       now,
		UpdatedAt:       now,
		LastActivityAt:  now,
	})
	require.NoError(err)

	mock := &mockClient{checkRunsErr: errors.New("temporary provider failure")}
	syncer := NewSyncer(
		map[string]Client{"github.com": mock},
		d, nil,
		[]RepoRef{{Owner: "acme", Name: "widget", PlatformHost: "github.com"}},
		time.Minute,
		nil,
		nil,
	)

	err = syncer.refreshCIStatus(
		t.Context(),
		RepoRef{Owner: "acme", Name: "widget", PlatformHost: "github.com"},
		repoID,
		1,
		"abc123",
	)
	require.NoError(err)

	mr, err := d.GetMergeRequestByRepoIDAndNumber(t.Context(), repoID, 1)
	require.NoError(err)
	require.NotNil(mr)
	assert.Equal("pending", mr.CIStatus)
	assert.Contains(mr.CIChecksJSON, "in_progress")
}

func TestRefreshCIStatusFallsBackToCombinedWhenNoCheckRuns(t *testing.T) {
	assert := Assert.New(t)
	d := openTestDB(t)

	mock := &mockClient{
		checkRuns: nil,
		ciStatus: &gh.CombinedStatus{
			State:      new("success"),
			TotalCount: new(1),
		},
	}

	syncer := NewSyncer(
		map[string]Client{"github.com": mock},
		d, nil,
		[]RepoRef{{Owner: "acme", Name: "widget", PlatformHost: "github.com"}},
		time.Minute,
		nil,
		nil,
	)

	repoID, _ := d.UpsertRepo(t.Context(), db.GitHubRepoIdentity("github.com", "acme", "widget"))
	err := syncer.refreshCIStatus(
		t.Context(),
		RepoRef{Owner: "acme", Name: "widget", PlatformHost: "github.com"},
		repoID,
		1,
		"abc123",
	)
	require.NoError(t, err)

	// No check runs: GetCombinedStatus should be called as fallback
	assert.Equal(int32(1), mock.getCombinedCalls.Load(),
		"GetCombinedStatus should be called when no check runs exist")
}

// TestSyncer_OnStatusChangeCallback verifies the onStatusChange
// callback fires for each status transition during RunOnce. The
// SSE server uses this to broadcast live sync state.
func TestSyncer_OnStatusChangeCallback(t *testing.T) {
	assert := Assert.New(t)
	mock := &mockClient{openPRs: []*gh.PullRequest{}}
	d := openTestDB(t)
	_, err := d.UpsertRepo(t.Context(), db.GitHubRepoIdentity("github.com", "o", "n"))
	require.NoError(t, err)
	repos := []RepoRef{{Owner: "o", Name: "n", PlatformHost: "github.com"}}
	s := NewSyncer(
		map[string]Client{"github.com": mock},
		d, nil, repos, time.Hour, nil, nil,
	)

	var mu sync.Mutex
	var statuses []*SyncStatus
	s.SetOnStatusChange(func(status *SyncStatus) {
		mu.Lock()
		statuses = append(statuses, status)
		mu.Unlock()
	})

	s.RunOnce(t.Context())

	mu.Lock()
	defer mu.Unlock()
	require.GreaterOrEqual(t, len(statuses), 2,
		"should fire at least started + completed")
	assert.True(statuses[0].Running,
		"first callback should be running=true")
	assert.False(statuses[len(statuses)-1].Running,
		"last callback should be running=false")
}

// notModifiedErr returns the error shape go-github surfaces when the
// HTTP transport receives a 304 Not Modified response. The etag
// transport intercepts list-endpoint requests and adds If-None-Match
// headers; on a cache hit GitHub responds 304, which go-github wraps
// as *gh.ErrorResponse. The sync code calls IsNotModified to detect
// this and treat it as a no-op.
func notModifiedErr() error {
	return &gh.ErrorResponse{
		Response: &http.Response{StatusCode: http.StatusNotModified},
	}
}

// TestSyncerHandles304OnPRList verifies that a 304 response from
// the open-PR list is treated as "list unchanged, nothing to do"
// rather than a fatal sync error. Before the fix, IsNotModified
// was unused at the call site and the wrapped 304 was returned
// as "list open PRs: ...", failing the repo sync entirely.
func TestSyncerHandles304OnPRList(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	d := openTestDB(t)

	mc := &mockClient{
		listOpenPRsErr: notModifiedErr(),
	}

	syncer := NewSyncer(
		map[string]Client{"github.com": mc},
		d,
		nil,
		[]RepoRef{{Owner: "owner", Name: "repo", PlatformHost: "github.com"}},
		time.Minute,
		nil,
		nil,
	)

	var (
		results   []RepoSyncResult
		gotResult sync.WaitGroup
	)
	gotResult.Add(1)
	syncer.SetOnSyncCompleted(func(r []RepoSyncResult) {
		results = r
		gotResult.Done()
	})

	syncer.RunOnce(t.Context())
	gotResult.Wait()

	require.Len(results, 1)
	assert.Empty(results[0].Error,
		"304 on open-PR list must not surface as a sync error")
}

// TestSyncerHandles304OnIssueList verifies the same short-circuit
// for the open-issue list endpoint. syncIssues is called from
// doSyncRepo with its error treated as non-fatal (logged only),
// so even before the fix the repo would not be marked failed —
// but the per-issue upserts and closure detection would still be
// skipped erroneously due to the early return path. After the
// fix, the function explicitly returns nil on 304 and the
// happy-path PR sync still completes cleanly.
func TestSyncerHandles304OnIssueList(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	d := openTestDB(t)

	mc := &mockClient{
		openPRs:           []*gh.PullRequest{},
		listOpenIssuesErr: notModifiedErr(),
	}

	syncer := NewSyncer(
		map[string]Client{"github.com": mc},
		d,
		nil,
		[]RepoRef{{Owner: "owner", Name: "repo", PlatformHost: "github.com"}},
		time.Minute,
		nil,
		nil,
	)

	var (
		results   []RepoSyncResult
		gotResult sync.WaitGroup
	)
	gotResult.Add(1)
	syncer.SetOnSyncCompleted(func(r []RepoSyncResult) {
		results = r
		gotResult.Done()
	})

	syncer.RunOnce(t.Context())
	gotResult.Wait()

	require.Len(results, 1)
	assert.Empty(results[0].Error,
		"304 on open-issue list must not surface as a sync error")
}

// TestSyncerPRList304MakesNoAPICalls verifies that a 304 on the open-PR
// list endpoint triggers zero additional API calls for that repo's PRs.
// CI freshness for unchanged PRs is handled by the detail drain's
// priority scoring (ci_had_pending items get expedited refetches).
func TestSyncerPRList304MakesNoAPICalls(t *testing.T) {
	require := require.New(t)
	ctx := t.Context()
	d := openTestDB(t)

	// Seed one open PR with pending CI via a full sync.
	now := time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC)
	inProgress := "in_progress"
	seedClient := &mockClient{
		openPRs:   []*gh.PullRequest{buildOpenPR(1, now)},
		checkRuns: []*gh.CheckRun{{Status: &inProgress}},
	}
	repos := []RepoRef{{Owner: "owner", Name: "repo", PlatformHost: "github.com"}}
	seedSyncer := NewSyncer(
		map[string]Client{"github.com": seedClient},
		d, nil, repos, time.Minute, nil, testBudget(10000),
	)
	seedSyncer.RunOnce(ctx)

	pr, err := d.GetMergeRequest(ctx, "owner", "repo", 1)
	require.NoError(err)
	require.Equal("pending", pr.CIStatus)

	// Second sync: PR list returns 304. The mock has CI data that
	// would change the status if called, but the 304 path must not
	// call any CI endpoints.
	completed := "completed"
	success := "success"
	spy := &callCountingClient{
		mockClient: mockClient{
			listOpenPRsErr: notModifiedErr(),
			checkRuns: []*gh.CheckRun{
				{Status: &completed, Conclusion: &success},
			},
		},
	}
	// budgetPerHour=0 disables detail drain so only index phase runs.
	refreshSyncer := NewSyncer(
		map[string]Client{"github.com": spy},
		d, nil, repos, time.Minute, nil, nil,
	)
	refreshSyncer.RunOnce(ctx)

	require.Equal(0, spy.ciCalls,
		"304 on PR list must not trigger any CI API calls")

	// CI state should be unchanged — still pending from seed.
	pr, err = d.GetMergeRequest(ctx, "owner", "repo", 1)
	require.NoError(err)
	require.Equal("pending", pr.CIStatus,
		"CI should remain stale until detail drain refreshes it")
}

// callCountingClient wraps mockClient and counts CI-related API calls.
type callCountingClient struct {
	mockClient
	ciCalls int
}

func (c *callCountingClient) ListCheckRunsForRef(
	ctx context.Context, owner, repo, ref string,
) ([]*gh.CheckRun, error) {
	c.ciCalls++
	return c.mockClient.ListCheckRunsForRef(ctx, owner, repo, ref)
}

func (c *callCountingClient) GetCombinedStatus(
	ctx context.Context, owner, repo, ref string,
) (*gh.CombinedStatus, error) {
	c.ciCalls++
	return c.mockClient.GetCombinedStatus(ctx, owner, repo, ref)
}

// TestSyncerSyncsIssuesOnPRList304 verifies that a 304 on the open-PR
// list does not short-circuit issue sync. Issues have an independent
// ETag and their own open-list endpoint, so a PR-list 304 must not
// prevent new issues from being picked up.
func TestSyncerSyncsIssuesOnPRList304(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	d := openTestDB(t)

	now := time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC)
	issueNumber := 42
	issueTitle := "broken thing"
	issueState := "open"
	issueURL := "https://github.com/owner/repo/issues/42"
	issueBody := ""
	issueID := int64(900042)
	mc := &mockClient{
		listOpenPRsErr: notModifiedErr(),
		openIssues: []*gh.Issue{
			{
				ID:        &issueID,
				Number:    &issueNumber,
				Title:     &issueTitle,
				State:     &issueState,
				HTMLURL:   &issueURL,
				Body:      &issueBody,
				CreatedAt: makeTimestamp(now),
				UpdatedAt: makeTimestamp(now),
			},
		},
	}

	syncer := NewSyncer(
		map[string]Client{"github.com": mc},
		d, nil,
		[]RepoRef{{Owner: "owner", Name: "repo", PlatformHost: "github.com"}},
		time.Minute, nil, nil,
	)
	syncer.RunOnce(ctx)

	issue, err := d.GetIssue(ctx, "owner", "repo", issueNumber)
	require.NoError(err)
	require.NotNil(issue, "issue sync must run even when PR list returns 304")
	assert.Equal(issueNumber, issue.Number)
	assert.Equal(issueTitle, issue.Title)
}

func TestSyncStoresIssueLabels(t *testing.T) {
	require := require.New(t)
	ctx := t.Context()
	d := openTestDB(t)

	now := time.Date(2024, 6, 4, 12, 0, 0, 0, time.UTC)
	issueNumber := 42
	issueTitle := "broken thing"
	issueState := "open"
	issueURL := "https://github.com/owner/repo/issues/42"
	issueBody := ""
	issueID := int64(900042)
	mc := &mockClient{
		openIssues: []*gh.Issue{{
			ID:        &issueID,
			Number:    &issueNumber,
			Title:     &issueTitle,
			State:     &issueState,
			HTMLURL:   &issueURL,
			Body:      &issueBody,
			CreatedAt: makeTimestamp(now),
			UpdatedAt: makeTimestamp(now),
			Labels: []*gh.Label{
				buildGitHubLabel(801, "bug", "Something is broken", "d73a4a", true),
			},
		}},
		comments: []*gh.IssueComment{},
	}

	syncer := NewSyncer(
		map[string]Client{"github.com": mc},
		d, nil,
		[]RepoRef{{Owner: "owner", Name: "repo", PlatformHost: "github.com"}},
		time.Minute, nil, nil,
	)
	syncer.RunOnce(ctx)

	issue, err := d.GetIssue(ctx, "owner", "repo", issueNumber)
	require.NoError(err)
	require.NotNil(issue)
	require.Len(issue.Labels, 1)
	require.Equal("bug", issue.Labels[0].Name)
	require.Equal("Something is broken", issue.Labels[0].Description)
	require.Equal("d73a4a", issue.Labels[0].Color)
	require.True(issue.Labels[0].IsDefault)
	require.Equal(int64(801), issue.Labels[0].PlatformID)
	require.True(issue.Labels[0].UpdatedAt.Equal(now))
}

func TestFetchAndUpdateClosedRefreshesPRLabels(t *testing.T) {
	require := require.New(t)
	ctx := t.Context()
	d := openTestDB(t)

	repoID, err := d.UpsertRepo(ctx, db.GitHubRepoIdentity("github.com", "owner", "repo"))
	require.NoError(err)
	now := time.Date(2024, 6, 5, 12, 0, 0, 0, time.UTC)
	pr := buildOpenPR(7, now)
	pr.State = new("closed")
	closedAt := makeTimestamp(now)
	pr.ClosedAt = closedAt
	pr.Labels = []*gh.Label{buildGitHubLabel(901, "bug", "Old bug", "d73a4a", true)}
	normalizedPR, err := NormalizePR(repoID, pr)
	require.NoError(err)
	_, err = d.UpsertMergeRequest(ctx, normalizedPR)
	require.NoError(err)
	storedBefore, err := d.GetMergeRequest(ctx, "owner", "repo", 7)
	require.NoError(err)
	require.NoError(d.ReplaceMergeRequestLabels(ctx, repoID, storedBefore.ID, []db.Label{{
		PlatformID:  901,
		Name:        "bug",
		Description: "Old bug",
		Color:       "d73a4a",
		IsDefault:   true,
		UpdatedAt:   now,
	}}))

	pr.Labels = []*gh.Label{buildGitHubLabel(902, "release", "Ready to release", "5319e7", false)}
	pr.UpdatedAt = makeTimestamp(now.Add(time.Minute))
	mc := &mockClient{singlePR: pr}
	syncer := NewSyncer(map[string]Client{"github.com": mc}, d, nil, []RepoRef{{Owner: "owner", Name: "repo", PlatformHost: "github.com"}}, time.Minute, nil, nil)

	require.NoError(syncer.fetchAndUpdateClosed(ctx, RepoRef{Owner: "owner", Name: "repo", PlatformHost: "github.com"}, repoID, 7, false))

	storedAfter, err := d.GetMergeRequest(ctx, "owner", "repo", 7)
	require.NoError(err)
	require.Len(storedAfter.Labels, 1)
	require.Equal("release", storedAfter.Labels[0].Name)
	require.Equal(int64(902), storedAfter.Labels[0].PlatformID)
}

func TestFetchAndUpdateClosedRefreshesPRLabelsWithSameRepoOnAnotherHost(t *testing.T) {
	require := require.New(t)
	ctx := t.Context()
	d := openTestDB(t)

	otherRepoID, err := d.UpsertRepo(ctx, db.GitHubRepoIdentity("ghe.corp.com", "owner", "repo"))
	require.NoError(err)
	repoID, err := d.UpsertRepo(ctx, db.GitHubRepoIdentity("github.com", "owner", "repo"))
	require.NoError(err)
	now := time.Date(2024, 6, 5, 12, 0, 0, 0, time.UTC)

	otherPR := buildOpenPR(7, now)
	otherPR.State = new("closed")
	otherPR.ClosedAt = makeTimestamp(now)
	otherPR.Labels = []*gh.Label{buildGitHubLabel(990, "other-host", "Other host label", "333333", false)}
	otherNormalizedPR, err := NormalizePR(otherRepoID, otherPR)
	require.NoError(err)
	otherMRID, err := d.UpsertMergeRequest(ctx, otherNormalizedPR)
	require.NoError(err)
	require.NoError(d.ReplaceMergeRequestLabels(ctx, otherRepoID, otherMRID, []db.Label{{
		PlatformID:  990,
		Name:        "other-host",
		Description: "Other host label",
		Color:       "333333",
		UpdatedAt:   now,
	}}))

	pr := buildOpenPR(7, now)
	pr.State = new("closed")
	pr.ClosedAt = makeTimestamp(now)
	pr.Labels = []*gh.Label{buildGitHubLabel(901, "bug", "Old bug", "d73a4a", true)}
	targetNormalizedPR, err := NormalizePR(repoID, pr)
	require.NoError(err)
	targetMRID, err := d.UpsertMergeRequest(ctx, targetNormalizedPR)
	require.NoError(err)
	require.NoError(d.ReplaceMergeRequestLabels(ctx, repoID, targetMRID, []db.Label{{
		PlatformID:  901,
		Name:        "bug",
		Description: "Old bug",
		Color:       "d73a4a",
		IsDefault:   true,
		UpdatedAt:   now,
	}}))

	pr.Labels = []*gh.Label{buildGitHubLabel(902, "release", "Ready to release", "5319e7", false)}
	pr.UpdatedAt = makeTimestamp(now.Add(time.Minute))
	mc := &mockClient{singlePR: pr}
	syncer := NewSyncer(map[string]Client{"github.com": mc}, d, nil, []RepoRef{{Owner: "owner", Name: "repo", PlatformHost: "github.com"}}, time.Minute, nil, nil)

	require.NoError(syncer.fetchAndUpdateClosed(ctx, RepoRef{Owner: "owner", Name: "repo", PlatformHost: "github.com"}, repoID, 7, false))

	var labelName string
	err = d.ReadDB().QueryRowContext(ctx, `
		SELECT l.name
		FROM middleman_merge_request_labels ml
		JOIN middleman_labels l ON l.id = ml.label_id
		WHERE ml.merge_request_id = ?`, targetMRID,
	).Scan(&labelName)
	require.NoError(err)
	require.Equal("release", labelName)

	err = d.ReadDB().QueryRowContext(ctx, `
		SELECT l.name
		FROM middleman_merge_request_labels ml
		JOIN middleman_labels l ON l.id = ml.label_id
		WHERE ml.merge_request_id = ?`, otherMRID,
	).Scan(&labelName)
	require.NoError(err)
	require.Equal("other-host", labelName)
}

func TestFetchAndUpdateClosedRefreshesIssueLabels(t *testing.T) {
	require := require.New(t)
	ctx := t.Context()
	d := openTestDB(t)

	repoID, err := d.UpsertRepo(ctx, db.GitHubRepoIdentity("github.com", "owner", "repo"))
	require.NoError(err)
	now := time.Date(2024, 6, 6, 12, 0, 0, 0, time.UTC)
	issueNumber := 9
	issueTitle := "closed issue"
	issueState := "open"
	issueURL := "https://github.com/owner/repo/issues/9"
	issueBody := ""
	issueID := int64(900009)
	issue := &gh.Issue{ID: &issueID, Number: &issueNumber, Title: &issueTitle, State: &issueState, HTMLURL: &issueURL, Body: &issueBody, CreatedAt: makeTimestamp(now), UpdatedAt: makeTimestamp(now), Labels: []*gh.Label{buildGitHubLabel(1001, "bug", "Old bug", "d73a4a", true)}}
	normalizedIssue, err := NormalizeIssue(repoID, issue)
	require.NoError(err)
	issueRowID, err := d.UpsertIssue(ctx, normalizedIssue)
	require.NoError(err)
	require.NoError(d.ReplaceIssueLabels(ctx, repoID, issueRowID, []db.Label{{PlatformID: 1001, Name: "bug", Description: "Old bug", Color: "d73a4a", IsDefault: true, UpdatedAt: now}}))

	closedState := "closed"
	issue.State = &closedState
	issue.UpdatedAt = makeTimestamp(now.Add(time.Minute))
	issue.Labels = []*gh.Label{buildGitHubLabel(1002, "docs", "Documentation", "0075ca", false)}
	closedAt := makeTimestamp(now.Add(2 * time.Minute))
	issue.ClosedAt = closedAt
	mc := &mockClient{getIssueFn: func(context.Context, string, string, int) (*gh.Issue, error) { return issue, nil }}
	syncer := NewSyncer(map[string]Client{"github.com": mc}, d, nil, []RepoRef{{Owner: "owner", Name: "repo", PlatformHost: "github.com"}}, time.Minute, nil, nil)

	require.NoError(syncer.fetchAndUpdateClosedIssue(ctx, RepoRef{Owner: "owner", Name: "repo", PlatformHost: "github.com"}, repoID, issueNumber))

	stored, err := d.GetIssue(ctx, "owner", "repo", issueNumber)
	require.NoError(err)
	require.Len(stored.Labels, 1)
	require.Equal("docs", stored.Labels[0].Name)
	require.Equal(int64(1002), stored.Labels[0].PlatformID)
}

func TestFetchAndUpdateClosedRefreshesIssueLabelsWithSameRepoOnAnotherHost(t *testing.T) {
	require := require.New(t)
	ctx := t.Context()
	d := openTestDB(t)

	otherRepoID, err := d.UpsertRepo(ctx, db.GitHubRepoIdentity("ghe.corp.com", "owner", "repo"))
	require.NoError(err)
	repoID, err := d.UpsertRepo(ctx, db.GitHubRepoIdentity("github.com", "owner", "repo"))
	require.NoError(err)
	now := time.Date(2024, 6, 6, 12, 0, 0, 0, time.UTC)
	issueNumber := 9

	otherState := "open"
	otherTitle := "other closed issue"
	otherURL := "https://ghe.corp.com/owner/repo/issues/9"
	otherBody := ""
	otherID := int64(800009)
	otherIssue := &gh.Issue{ID: &otherID, Number: &issueNumber, Title: &otherTitle, State: &otherState, HTMLURL: &otherURL, Body: &otherBody, CreatedAt: makeTimestamp(now), UpdatedAt: makeTimestamp(now), Labels: []*gh.Label{buildGitHubLabel(1901, "other-host", "Other host label", "333333", false)}}
	otherNormalizedIssue, err := NormalizeIssue(otherRepoID, otherIssue)
	require.NoError(err)
	otherIssueRowID, err := d.UpsertIssue(ctx, otherNormalizedIssue)
	require.NoError(err)
	require.NoError(d.ReplaceIssueLabels(ctx, otherRepoID, otherIssueRowID, []db.Label{{PlatformID: 1901, Name: "other-host", Description: "Other host label", Color: "333333", UpdatedAt: now}}))

	issueTitle := "closed issue"
	issueState := "open"
	issueURL := "https://github.com/owner/repo/issues/9"
	issueBody := ""
	issueID := int64(900009)
	issue := &gh.Issue{ID: &issueID, Number: &issueNumber, Title: &issueTitle, State: &issueState, HTMLURL: &issueURL, Body: &issueBody, CreatedAt: makeTimestamp(now), UpdatedAt: makeTimestamp(now), Labels: []*gh.Label{buildGitHubLabel(1001, "bug", "Old bug", "d73a4a", true)}}
	normalizedIssue, err := NormalizeIssue(repoID, issue)
	require.NoError(err)
	issueRowID, err := d.UpsertIssue(ctx, normalizedIssue)
	require.NoError(err)
	require.NoError(d.ReplaceIssueLabels(ctx, repoID, issueRowID, []db.Label{{PlatformID: 1001, Name: "bug", Description: "Old bug", Color: "d73a4a", IsDefault: true, UpdatedAt: now}}))

	closedState := "closed"
	issue.State = &closedState
	issue.UpdatedAt = makeTimestamp(now.Add(time.Minute))
	issue.Labels = []*gh.Label{buildGitHubLabel(1002, "docs", "Documentation", "0075ca", false)}
	closedAt := makeTimestamp(now.Add(2 * time.Minute))
	issue.ClosedAt = closedAt
	mc := &mockClient{getIssueFn: func(context.Context, string, string, int) (*gh.Issue, error) { return issue, nil }}
	syncer := NewSyncer(map[string]Client{"github.com": mc}, d, nil, []RepoRef{{Owner: "owner", Name: "repo", PlatformHost: "github.com"}}, time.Minute, nil, nil)

	require.NoError(syncer.fetchAndUpdateClosedIssue(ctx, RepoRef{Owner: "owner", Name: "repo", PlatformHost: "github.com"}, repoID, issueNumber))

	var labelName string
	err = d.ReadDB().QueryRowContext(ctx, `
		SELECT l.name
		FROM middleman_issue_labels il
		JOIN middleman_labels l ON l.id = il.label_id
		WHERE il.issue_id = ?`, issueRowID,
	).Scan(&labelName)
	require.NoError(err)
	require.Equal("docs", labelName)

	err = d.ReadDB().QueryRowContext(ctx, `
		SELECT l.name
		FROM middleman_issue_labels il
		JOIN middleman_labels l ON l.id = il.label_id
		WHERE il.issue_id = ?`, otherIssueRowID,
	).Scan(&labelName)
	require.NoError(err)
	require.Equal("other-host", labelName)
}

func TestBackfillRepoPersistsPRLabels(t *testing.T) {
	require := require.New(t)
	ctx := t.Context()
	d := openTestDB(t)

	repoID, err := d.UpsertRepo(ctx, db.GitHubRepoIdentity("github.com", "owner", "repo"))
	require.NoError(err)
	repoRow, err := d.GetRepoByOwnerName(ctx, "owner", "repo")
	require.NoError(err)
	require.NotNil(repoRow)
	now := time.Date(2024, 6, 7, 12, 0, 0, 0, time.UTC)
	pr := buildOpenPR(21, now)
	pr.State = new("closed")
	pr.Labels = []*gh.Label{buildGitHubLabel(1101, "backfill-pr", "Backfilled PR label", "5319e7", false)}

	mc := &mockClient{listPullRequestsPageFn: func(context.Context, string, string, string, int) ([]*gh.PullRequest, bool, error) {
		return []*gh.PullRequest{pr}, false, nil
	}}
	syncer := NewSyncer(map[string]Client{"github.com": mc}, d, nil, []RepoRef{{Owner: "owner", Name: "repo", PlatformHost: "github.com"}}, time.Minute, nil, testBudget(10))

	syncer.backfillRepo(ctx, RepoRef{Owner: "owner", Name: "repo", PlatformHost: "github.com"}, repoRow, NewSyncBudget(10))

	stored, err := d.GetMergeRequest(ctx, "owner", "repo", 21)
	require.NoError(err)
	require.NotNil(stored)
	require.Equal(repoID, stored.RepoID)
	require.Len(stored.Labels, 1)
	require.Equal("backfill-pr", stored.Labels[0].Name)
}

func TestBackfillRepoPersistsIssueLabels(t *testing.T) {
	require := require.New(t)
	ctx := t.Context()
	d := openTestDB(t)

	_, err := d.UpsertRepo(ctx, db.GitHubRepoIdentity("github.com", "owner", "repo"))
	require.NoError(err)
	repoRow, err := d.GetRepoByOwnerName(ctx, "owner", "repo")
	require.NoError(err)
	require.NotNil(repoRow)
	now := time.Date(2024, 6, 8, 12, 0, 0, 0, time.UTC)
	issueNumber := 22
	issueTitle := "backfilled issue"
	issueState := "closed"
	issueURL := "https://github.com/owner/repo/issues/22"
	issueBody := ""
	issueID := int64(900022)
	issue := &gh.Issue{ID: &issueID, Number: &issueNumber, Title: &issueTitle, State: &issueState, HTMLURL: &issueURL, Body: &issueBody, CreatedAt: makeTimestamp(now), UpdatedAt: makeTimestamp(now), Labels: []*gh.Label{buildGitHubLabel(1201, "backfill-issue", "Backfilled issue label", "0052cc", false)}}

	mc := &mockClient{listIssuesPageFn: func(context.Context, string, string, string, int) ([]*gh.Issue, bool, error) {
		return []*gh.Issue{issue}, false, nil
	}}
	syncer := NewSyncer(map[string]Client{"github.com": mc}, d, nil, []RepoRef{{Owner: "owner", Name: "repo", PlatformHost: "github.com"}}, time.Minute, nil, testBudget(10))

	syncer.backfillRepo(ctx, RepoRef{Owner: "owner", Name: "repo", PlatformHost: "github.com"}, repoRow, NewSyncBudget(10))

	stored, err := d.GetIssue(ctx, "owner", "repo", issueNumber)
	require.NoError(err)
	require.NotNil(stored)
	require.Len(stored.Labels, 1)
	require.Equal("backfill-issue", stored.Labels[0].Name)
}

func TestBackfillRepoSkipsNonGitHubProviders(t *testing.T) {
	require := require.New(t)
	ctx := t.Context()
	d := openTestDB(t)
	repo := RepoRef{
		Platform:     platform.KindGitLab,
		PlatformHost: "gitlab.com",
		Owner:        "owner",
		Name:         "repo",
	}
	_, err := d.UpsertRepo(ctx, platform.DBRepoIdentity(platformRepoRef(repo)))
	require.NoError(err)
	repoRow, err := d.GetRepoByOwnerName(ctx, "owner", "repo")
	require.NoError(err)
	require.NotNil(repoRow)

	provider := &syncTestReadProvider{
		syncTestProvider: syncTestProvider{
			kind: platform.KindGitLab,
			host: "gitlab.com",
		},
	}
	registry, err := platform.NewRegistry(provider)
	require.NoError(err)
	syncer := NewSyncer(nil, d, nil, []RepoRef{repo}, time.Minute, nil, map[string]*SyncBudget{
		"gitlab.com": NewSyncBudget(10),
	})
	syncer.clients = registry

	syncer.backfillRepo(ctx, repo, repoRow, NewSyncBudget(10))

	repoAfter, err := d.GetRepoByOwnerName(ctx, "owner", "repo")
	require.NoError(err)
	require.NotNil(repoAfter)
	require.False(repoAfter.BackfillPRComplete)
	require.False(repoAfter.BackfillIssueComplete)
	require.Zero(provider.getMRCalls.Load())
	require.Zero(provider.getIssueCalls.Load())
}

func TestRunBackfillDiscoveryUsesProviderQualifiedRepo(t *testing.T) {
	require := require.New(t)
	ctx := t.Context()
	d := openTestDB(t)

	gitLabRepo := RepoRef{
		Platform:     platform.KindGitLab,
		PlatformHost: "github.com",
		Owner:        "owner",
		Name:         "repo",
	}
	_, err := d.UpsertRepo(ctx, platform.DBRepoIdentity(platformRepoRef(gitLabRepo)))
	require.NoError(err)

	gitHubRepo := RepoRef{
		Platform:     platform.KindGitHub,
		PlatformHost: "github.com",
		Owner:        "owner",
		Name:         "repo",
	}
	_, err = d.UpsertRepo(ctx, platform.DBRepoIdentity(platformRepoRef(gitHubRepo)))
	require.NoError(err)

	mc := &mockClient{
		listPullRequestsPageFn: func(context.Context, string, string, string, int) ([]*gh.PullRequest, bool, error) {
			return nil, false, nil
		},
		listIssuesPageFn: func(context.Context, string, string, string, int) ([]*gh.Issue, bool, error) {
			return nil, false, nil
		},
	}
	syncer := NewSyncer(
		map[string]Client{"github.com": mc},
		d, nil,
		[]RepoRef{gitHubRepo},
		time.Minute, nil,
		map[string]*SyncBudget{"github.com": NewSyncBudget(10)},
	)

	syncer.runBackfillDiscovery(ctx, "github.com", []RepoRef{gitHubRepo})

	gitHubAfter, err := d.GetRepoByIdentity(ctx, platform.DBRepoIdentity(platformRepoRef(gitHubRepo)))
	require.NoError(err)
	require.NotNil(gitHubAfter)
	require.True(gitHubAfter.BackfillPRComplete)
	require.True(gitHubAfter.BackfillIssueComplete)

	gitLabAfter, err := d.GetRepoByIdentity(ctx, platform.DBRepoIdentity(platformRepoRef(gitLabRepo)))
	require.NoError(err)
	require.NotNil(gitLabAfter)
	require.False(gitLabAfter.BackfillPRComplete)
	require.False(gitLabAfter.BackfillIssueComplete)
}

func TestBackfillRepoStoresCompletionTimestampsInUTC(t *testing.T) {
	require := require.New(t)
	setTestLocalEDT(t)

	ctx := t.Context()
	d := openTestDB(t)

	_, err := d.UpsertRepo(ctx, db.GitHubRepoIdentity("github.com", "owner", "repo"))
	require.NoError(err)
	repoRow, err := d.GetRepoByOwnerName(ctx, "owner", "repo")
	require.NoError(err)
	require.NotNil(repoRow)
	now := time.Date(2024, 6, 8, 12, 0, 0, 0, time.UTC)

	pr := buildOpenPR(41, now)
	pr.State = new("closed")
	issueNumber := 42
	issueTitle := "backfilled issue"
	issueState := "closed"
	issueURL := "https://github.com/owner/repo/issues/42"
	issueBody := ""
	issueID := int64(900042)
	issue := &gh.Issue{ID: &issueID, Number: &issueNumber, Title: &issueTitle, State: &issueState, HTMLURL: &issueURL, Body: &issueBody, CreatedAt: makeTimestamp(now), UpdatedAt: makeTimestamp(now)}

	mc := &mockClient{
		listPullRequestsPageFn: func(context.Context, string, string, string, int) ([]*gh.PullRequest, bool, error) {
			return []*gh.PullRequest{pr}, false, nil
		},
		listIssuesPageFn: func(context.Context, string, string, string, int) ([]*gh.Issue, bool, error) {
			return []*gh.Issue{issue}, false, nil
		},
	}
	syncer := NewSyncer(map[string]Client{"github.com": mc}, d, nil, []RepoRef{{Owner: "owner", Name: "repo", PlatformHost: "github.com"}}, time.Minute, nil, testBudget(10))

	syncer.backfillRepo(ctx, RepoRef{Owner: "owner", Name: "repo", PlatformHost: "github.com"}, repoRow, NewSyncBudget(10))

	repoAfter, err := d.GetRepoByOwnerName(ctx, "owner", "repo")
	require.NoError(err)
	require.NotNil(repoAfter)
	require.True(repoAfter.BackfillPRComplete)
	require.True(repoAfter.BackfillIssueComplete)
	require.NotNil(repoAfter.BackfillPRCompletedAt)
	require.NotNil(repoAfter.BackfillIssueCompletedAt)
	require.Equal(time.UTC, repoAfter.BackfillPRCompletedAt.Location())
	require.Equal(time.UTC, repoAfter.BackfillIssueCompletedAt.Location())
}

func TestBackfillRepoDoesNotAdvancePRCursorWhenLabelPersistenceFails(t *testing.T) {
	require := require.New(t)
	ctx := t.Context()
	d := openTestDB(t)

	repoID, err := d.UpsertRepo(ctx, db.GitHubRepoIdentity("github.com", "owner", "repo"))
	require.NoError(err)
	repoRow, err := d.GetRepoByOwnerName(ctx, "owner", "repo")
	require.NoError(err)
	require.NotNil(repoRow)
	now := time.Date(2024, 6, 9, 12, 0, 0, 0, time.UTC)

	require.NoError(d.UpsertLabels(ctx, repoID, []db.Label{{
		PlatformID:  100,
		Name:        "bug",
		Description: "name row",
		Color:       "111111",
		UpdatedAt:   now,
	}}))
	require.NoError(d.UpsertLabels(ctx, repoID, []db.Label{{
		PlatformID:  200,
		Name:        "renamed",
		Description: "platform row",
		Color:       "222222",
		UpdatedAt:   now,
	}}))

	pr := buildOpenPR(31, now)
	pr.State = new("closed")
	pr.Labels = []*gh.Label{buildGitHubLabel(200, "bug", "ambiguous", "333333", false)}

	mc := &mockClient{listPullRequestsPageFn: func(context.Context, string, string, string, int) ([]*gh.PullRequest, bool, error) {
		return []*gh.PullRequest{pr}, false, nil
	}}
	syncer := NewSyncer(map[string]Client{"github.com": mc}, d, nil, []RepoRef{{Owner: "owner", Name: "repo", PlatformHost: "github.com"}}, time.Minute, nil, testBudget(10))

	syncer.backfillRepo(ctx, RepoRef{Owner: "owner", Name: "repo", PlatformHost: "github.com"}, repoRow, NewSyncBudget(10))

	repoAfter, err := d.GetRepoByOwnerName(ctx, "owner", "repo")
	require.NoError(err)
	require.NotNil(repoAfter)
	require.Equal(0, repoAfter.BackfillPRPage)
	require.False(repoAfter.BackfillPRComplete)
	require.Nil(repoAfter.BackfillPRCompletedAt)

	stored, err := d.GetMergeRequest(ctx, "owner", "repo", 31)
	require.NoError(err)
	require.NotNil(stored)
	require.Empty(stored.Labels)
}

func TestBackfillRepoDoesNotAdvanceIssueCursorWhenLabelPersistenceFails(t *testing.T) {
	require := require.New(t)
	ctx := t.Context()
	d := openTestDB(t)

	repoID, err := d.UpsertRepo(ctx, db.GitHubRepoIdentity("github.com", "owner", "repo"))
	require.NoError(err)
	repoRow, err := d.GetRepoByOwnerName(ctx, "owner", "repo")
	require.NoError(err)
	require.NotNil(repoRow)
	now := time.Date(2024, 6, 10, 12, 0, 0, 0, time.UTC)

	require.NoError(d.UpsertLabels(ctx, repoID, []db.Label{{
		PlatformID:  100,
		Name:        "bug",
		Description: "name row",
		Color:       "111111",
		UpdatedAt:   now,
	}}))
	require.NoError(d.UpsertLabels(ctx, repoID, []db.Label{{
		PlatformID:  200,
		Name:        "renamed",
		Description: "platform row",
		Color:       "222222",
		UpdatedAt:   now,
	}}))

	issueNumber := 32
	issueTitle := "ambiguous backfill issue"
	issueState := "closed"
	issueURL := "https://github.com/owner/repo/issues/32"
	issueBody := ""
	issueID := int64(900032)
	issue := &gh.Issue{ID: &issueID, Number: &issueNumber, Title: &issueTitle, State: &issueState, HTMLURL: &issueURL, Body: &issueBody, CreatedAt: makeTimestamp(now), UpdatedAt: makeTimestamp(now), Labels: []*gh.Label{buildGitHubLabel(200, "bug", "ambiguous", "333333", false)}}

	mc := &mockClient{listIssuesPageFn: func(context.Context, string, string, string, int) ([]*gh.Issue, bool, error) {
		return []*gh.Issue{issue}, false, nil
	}}
	syncer := NewSyncer(map[string]Client{"github.com": mc}, d, nil, []RepoRef{{Owner: "owner", Name: "repo", PlatformHost: "github.com"}}, time.Minute, nil, testBudget(10))

	syncer.backfillRepo(ctx, RepoRef{Owner: "owner", Name: "repo", PlatformHost: "github.com"}, repoRow, NewSyncBudget(10))

	repoAfter, err := d.GetRepoByOwnerName(ctx, "owner", "repo")
	require.NoError(err)
	require.NotNil(repoAfter)
	require.Equal(0, repoAfter.BackfillIssuePage)
	require.False(repoAfter.BackfillIssueComplete)
	require.Nil(repoAfter.BackfillIssueCompletedAt)

	stored, err := d.GetIssue(ctx, "owner", "repo", issueNumber)
	require.NoError(err)
	require.NotNil(stored)
	require.Empty(stored.Labels)
}

// partialFailureMock embeds mockClient and simulates ETag-like
// behavior for issues: after a successful list fetch, subsequent
// calls return 304 (not-modified) unless InvalidateListETagsForRepo
// was called. This proves invalidation is load-bearing — without it
// the retry never fires and stale state persists.
type partialFailureMock struct {
	mockClient
	issuesCached         bool
	prsCached            bool
	listOpenPRsErr       error // injected error for ListOpenPullRequests
	listIssueCommentsErr error // injected error for ListIssueComments
	listReviewsErr       error // injected error for ListReviews (MR timeline)
	getIssueErr          error // injected error for GetIssue (closure path)
}

func (m *partialFailureMock) ListOpenPullRequests(_ context.Context, _, _ string) ([]*gh.PullRequest, error) {
	if m.listOpenPRsErr != nil {
		return nil, m.listOpenPRsErr
	}
	if m.prsCached {
		return nil, notModifiedErr()
	}
	m.prsCached = true
	return m.openPRs, nil
}

func (m *partialFailureMock) ListOpenIssues(_ context.Context, _, _ string) ([]*gh.Issue, error) {
	if m.listOpenIssuesErr != nil {
		return nil, m.listOpenIssuesErr
	}
	if m.issuesCached {
		return nil, notModifiedErr()
	}
	m.issuesCached = true
	return m.openIssues, nil
}

func (m *partialFailureMock) ListIssueComments(_ context.Context, _, _ string, _ int) ([]*gh.IssueComment, error) {
	if m.listIssueCommentsErr != nil {
		return nil, m.listIssueCommentsErr
	}
	return m.comments, nil
}

func (m *partialFailureMock) ListIssueCommentsIfChanged(
	ctx context.Context, owner, repo string, number int,
) ([]*gh.IssueComment, error) {
	if m.listIssueCommentsErr != nil {
		return nil, m.listIssueCommentsErr
	}
	if m.comments == nil {
		return nil, notModifiedErr()
	}
	return m.ListIssueComments(ctx, owner, repo, number)
}

func (m *partialFailureMock) ListReviews(_ context.Context, _, _ string, _ int) ([]*gh.PullRequestReview, error) {
	if m.listReviewsErr != nil {
		return nil, m.listReviewsErr
	}
	return m.reviews, nil
}

func (m *partialFailureMock) GetIssue(ctx context.Context, owner, repo string, number int) (*gh.Issue, error) {
	if m.getIssueErr != nil {
		return nil, m.getIssueErr
	}
	if m.getIssueFn != nil {
		return m.getIssueFn(ctx, owner, repo, number)
	}
	return nil, nil
}

func (m *partialFailureMock) InvalidateListETagsForRepo(_, _ string, endpoints ...string) {
	m.invalidateCalls.Add(1)
	if len(endpoints) == 0 {
		m.prsCached = false
		m.issuesCached = false
		return
	}
	for _, ep := range endpoints {
		switch ep {
		case "pulls":
			m.prsCached = false
		case "issues":
			m.issuesCached = false
		}
	}
}

// TestSyncerSyncOpenIssueFailureMarksRepoFailed verifies that when
// the open-issue list succeeds but syncOpenIssue fails for an
// individual item (here via a ListIssueComments error during timeline
// refresh), syncIssues returns an error, doSyncRepo calls
// markFailure, and the next cycle forces a timeline refresh via
// forceRefresh even though UpdatedAt hasn't changed.
func TestSyncerSyncOpenIssueFailureMarksRepoFailed(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	d := openTestDB(t)

	now := time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC)
	repos := []RepoRef{{Owner: "owner", Name: "repo", PlatformHost: "github.com"}}

	issueNumber := 7
	issueTitle := "per-item failure issue"
	issueState := "open"
	issueURL := "https://github.com/owner/repo/issues/7"
	issueBody := ""
	issueID := int64(777)
	openIssue := &gh.Issue{
		ID:        &issueID,
		Number:    &issueNumber,
		Title:     &issueTitle,
		State:     &issueState,
		HTMLURL:   &issueURL,
		Body:      &issueBody,
		CreatedAt: makeTimestamp(now),
		UpdatedAt: makeTimestamp(now),
	}

	commentID := int64(999)
	commentBody := "recovery comment"
	commentUser := "commenter"
	recoveryComment := &gh.IssueComment{
		ID:        &commentID,
		Body:      &commentBody,
		CreatedAt: makeTimestamp(now),
		UpdatedAt: makeTimestamp(now),
		User:      &gh.User{Login: &commentUser},
	}

	mc := &partialFailureMock{}
	mc.openPRs = []*gh.PullRequest{buildOpenPR(1, now)}
	mc.openIssues = []*gh.Issue{openIssue}
	mc.comments = []*gh.IssueComment{}
	mc.reviews = []*gh.PullRequestReview{}
	mc.commits = []*gh.RepositoryCommit{}
	// Issue list succeeds, but timeline refresh fails for the item.
	mc.listIssueCommentsErr = fmt.Errorf("transient comments failure")

	syncer := NewSyncer(
		map[string]Client{"github.com": mc},
		d, nil, repos, time.Minute, nil, nil,
	)

	// Cycle 1: issue list succeeds, issue is upserted to DB, but
	// refreshIssueTimeline fails → syncOpenIssue returns error →
	// hadItemFailure → syncIssues returns error → markFailure.
	syncer.RunOnce(ctx)

	// Issue row lands in DB (upsert happened before timeline).
	issue, err := d.GetIssue(ctx, "owner", "repo", issueNumber)
	require.NoError(err)
	require.NotNil(issue, "issue should be upserted even though timeline failed")

	// No events should exist because timeline refresh failed.
	events, err := d.ListIssueEvents(ctx, issue.ID)
	require.NoError(err)
	assert.Empty(events, "no events should exist after failed timeline refresh")

	_, flagged := syncer.failedRepos.Load(repoFailKey(repos[0]))
	assert.True(flagged, "failedRepos must be set after per-item syncOpenIssue failure")

	// Clear the error, provide a comment, simulate warm cache.
	mc.listIssueCommentsErr = nil
	mc.comments = []*gh.IssueComment{recoveryComment}
	mc.issuesCached = true

	invalidateBefore := mc.invalidateCalls.Load()

	// Cycle 2: forceRefresh overrides needsTimeline even though
	// UpdatedAt hasn't changed → timeline refresh retried → comment lands.
	syncer.RunOnce(ctx)

	assert.Greater(mc.invalidateCalls.Load(), invalidateBefore,
		"next cycle should call InvalidateListETagsForRepo")

	// Verify timeline was actually refreshed: the comment should be in DB.
	issue, err = d.GetIssue(ctx, "owner", "repo", issueNumber)
	require.NoError(err)
	events, err = d.ListIssueEvents(ctx, issue.ID)
	require.NoError(err)
	assert.Len(events, 1, "comment should be persisted after forced timeline retry")

	_, flagged = syncer.failedRepos.Load(repoFailKey(repos[0]))
	assert.False(flagged, "failedRepos must be cleared after successful retry")
}

// TestSyncerClosedIssueFailureMarksRepoFailed verifies that when
// the open-issue list succeeds but fetchAndUpdateClosedIssue fails
// for a previously-open issue (here via a GetIssue API error),
// syncIssues returns an error, doSyncRepo marks the repo failed,
// and the next cycle retries after ETag invalidation.
func TestSyncerClosedIssueFailureMarksRepoFailed(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	d := openTestDB(t)

	now := time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC)
	repos := []RepoRef{{Owner: "owner", Name: "repo", PlatformHost: "github.com"}}

	issueNumber := 7
	issueTitle := "will-close issue"
	issueState := "open"
	issueURL := "https://github.com/owner/repo/issues/7"
	issueBody := ""
	issueID := int64(777)
	openIssue := &gh.Issue{
		ID:        &issueID,
		Number:    &issueNumber,
		Title:     &issueTitle,
		State:     &issueState,
		HTMLURL:   &issueURL,
		Body:      &issueBody,
		CreatedAt: makeTimestamp(now),
		UpdatedAt: makeTimestamp(now),
	}

	// Seed issue #7 as open in DB via an initial sync with the
	// issue present in the open list.
	seedMC := &mockClient{
		openPRs:    []*gh.PullRequest{buildOpenPR(1, now)},
		openIssues: []*gh.Issue{openIssue},
		comments:   []*gh.IssueComment{},
		reviews:    []*gh.PullRequestReview{},
		commits:    []*gh.RepositoryCommit{},
	}

	seedSyncer := NewSyncer(
		map[string]Client{"github.com": seedMC},
		d, nil, repos, time.Minute, nil, nil,
	)
	seedSyncer.RunOnce(ctx)

	seeded, err := d.GetIssue(ctx, "owner", "repo", issueNumber)
	require.NoError(err)
	require.NotNil(seeded, "seed cycle should persist issue #7")

	// Now build the real mock: open list returns EMPTY (issue #7
	// no longer open) → closure detection finds #7. GetIssue for
	// the closure path fails.
	mc := &partialFailureMock{}
	mc.openPRs = []*gh.PullRequest{buildOpenPR(1, now)}
	mc.openIssues = []*gh.Issue{} // issue #7 not in open list
	mc.comments = []*gh.IssueComment{}
	mc.reviews = []*gh.PullRequestReview{}
	mc.commits = []*gh.RepositoryCommit{}
	mc.getIssueErr = fmt.Errorf("transient API failure fetching closed issue")

	syncer := NewSyncer(
		map[string]Client{"github.com": mc},
		d, nil, repos, time.Minute, nil, nil,
	)

	// Cycle 1: list succeeds (empty), closure detection finds #7,
	// fetchAndUpdateClosedIssue fails → hadItemFailure → markFailure.
	syncer.RunOnce(ctx)

	_, flagged := syncer.failedRepos.Load(repoFailKey(repos[0]))
	assert.True(flagged, "failedRepos must be set after fetchAndUpdateClosedIssue failure")

	// Verify issue is still open in DB (closure update failed).
	stillOpen, err := d.GetIssue(ctx, "owner", "repo", issueNumber)
	require.NoError(err)
	require.NotNil(stillOpen)
	assert.Equal("open", stillOpen.State, "issue should still be open because closure update failed")

	// Clear error, simulate warm cache, provide closed issue data.
	mc.getIssueErr = nil
	closedState := "closed"
	closedIssue := &gh.Issue{
		ID:        &issueID,
		Number:    &issueNumber,
		Title:     &issueTitle,
		State:     &closedState,
		HTMLURL:   &issueURL,
		Body:      &issueBody,
		CreatedAt: makeTimestamp(now),
		UpdatedAt: makeTimestamp(now.Add(time.Hour)),
	}
	mc.getIssueFn = func(_ context.Context, _, _ string, n int) (*gh.Issue, error) {
		if n == issueNumber {
			return closedIssue, nil
		}
		return nil, nil
	}
	mc.issuesCached = true

	invalidateBefore := mc.invalidateCalls.Load()

	// Cycle 2: invalidation → fresh list (empty) → closure
	// detection re-finds #7 → fetchAndUpdateClosedIssue succeeds.
	syncer.RunOnce(ctx)

	assert.Greater(mc.invalidateCalls.Load(), invalidateBefore,
		"next cycle should call InvalidateListETagsForRepo")

	updated, err := d.GetIssue(ctx, "owner", "repo", issueNumber)
	require.NoError(err)
	require.NotNil(updated)
	assert.Equal("closed", updated.State, "issue should be closed after successful retry")

	_, flagged = syncer.failedRepos.Load(repoFailKey(repos[0]))
	assert.False(flagged, "failedRepos must be cleared after successful retry")
}

// TestSyncerMRListFailureMarksRepoFailed verifies that when the
// PR list fails, the MR path is marked failed, and the next cycle
// invalidates the ETag and retries. Also verifies issue path is NOT
// force-refreshed when only MR path failed (scoped failure tracking).
func TestSyncerMRListFailureMarksRepoFailed(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	d := openTestDB(t)

	now := time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC)
	repos := []RepoRef{{Owner: "owner", Name: "repo", PlatformHost: "github.com"}}

	mc := &partialFailureMock{}
	mc.openPRs = []*gh.PullRequest{buildOpenPR(1, now)}
	mc.openIssues = []*gh.Issue{}
	mc.comments = []*gh.IssueComment{}
	mc.reviews = []*gh.PullRequestReview{}
	mc.commits = []*gh.RepositoryCommit{}
	// PR list fails on first call.
	mc.listOpenPRsErr = fmt.Errorf("transient PR list failure")

	syncer := NewSyncer(
		map[string]Client{"github.com": mc},
		d, nil, repos, time.Minute, nil, nil,
	)

	// Cycle 1: PR list fails → failMR set, issues unaffected.
	syncer.RunOnce(ctx)

	mr, err := d.GetMergeRequest(ctx, "owner", "repo", 1)
	require.NoError(err)
	assert.Nil(mr, "MR should not be upserted when PR list failed")

	v, flagged := syncer.failedRepos.Load(repoFailKey(repos[0]))
	assert.True(flagged, "failedRepos must be set after PR list failure")
	assert.Equal(failMR, v.(failScope), "only failMR scope should be set")

	// Clear error, simulate warm caches.
	mc.listOpenPRsErr = nil
	mc.prsCached = false // allow next list to succeed
	mc.issuesCached = true

	invalidateBefore := mc.invalidateCalls.Load()

	// Cycle 2: ETag invalidated for pulls only → fresh PR list → MR upserted.
	// Issue cache should remain warm (only pulls invalidated).
	syncer.RunOnce(ctx)

	assert.Greater(mc.invalidateCalls.Load(), invalidateBefore,
		"next cycle should call InvalidateListETagsForRepo")

	// Issue cache must still be warm — MR-only failure should not
	// invalidate issue ETags.
	assert.True(mc.issuesCached,
		"issue cache should stay warm when only MR path failed")

	mr, err = d.GetMergeRequest(ctx, "owner", "repo", 1)
	require.NoError(err)
	require.NotNil(mr, "MR should be upserted after successful retry")

	_, flagged = syncer.failedRepos.Load(repoFailKey(repos[0]))
	assert.False(flagged, "failedRepos must be cleared after successful retry")
}

// TestSyncerMRDetailFailureRetries verifies that when fetchMRDetail
// fails during timeline refresh (via ListReviews error), the MR's
// detail_fetched_at stays nil so the detail queue picks it up again
// on the next cycle.
func TestSyncerMRDetailFailureRetries(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	d := openTestDB(t)

	now := time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC)
	repos := []RepoRef{{Owner: "owner", Name: "repo", PlatformHost: "github.com"}}
	ciState := "success"

	mc := &partialFailureMock{}
	mc.openPRs = []*gh.PullRequest{buildOpenPR(1, now)}
	mc.openIssues = []*gh.Issue{}
	mc.comments = []*gh.IssueComment{}
	mc.reviews = []*gh.PullRequestReview{}
	mc.commits = []*gh.RepositoryCommit{}
	mc.ciStatus = &gh.CombinedStatus{State: &ciState}
	// Timeline refresh fails at ListReviews during detail fetch.
	mc.listReviewsErr = fmt.Errorf("transient reviews failure")

	syncer := NewSyncer(
		map[string]Client{"github.com": mc},
		d, nil, repos, time.Minute, nil, testBudget(10000),
	)

	// Cycle 1: index upserts MR, detail drain calls fetchMRDetail →
	// refreshTimeline fails at ListReviews → detail_fetched_at stays nil.
	syncer.RunOnce(ctx)

	mr, err := d.GetMergeRequest(ctx, "owner", "repo", 1)
	require.NoError(err)
	require.NotNil(mr, "MR should be upserted by index phase")
	assert.Nil(mr.DetailFetchedAt,
		"detail_fetched_at should be nil after failed detail fetch")

	events, err := d.ListMREvents(ctx, mr.ID)
	require.NoError(err)
	assert.Empty(events, "no events should exist after failed timeline refresh")

	// Clear error, add a review for cycle 2.
	mc.listReviewsErr = nil
	reviewID := int64(500)
	reviewState := "APPROVED"
	reviewUser := "reviewer"
	reviewBody := "lgtm"
	mc.reviews = []*gh.PullRequestReview{{
		ID:          &reviewID,
		State:       &reviewState,
		Body:        &reviewBody,
		SubmittedAt: makeTimestamp(now),
		User:        &gh.User{Login: &reviewUser},
	}}

	// Cycle 2: detail drain picks up MR again (detail_fetched_at nil)
	// → fetchMRDetail succeeds → timeline events land.
	syncer.RunOnce(ctx)

	mr, err = d.GetMergeRequest(ctx, "owner", "repo", 1)
	require.NoError(err)
	require.NotNil(mr)
	assert.NotNil(mr.DetailFetchedAt,
		"detail_fetched_at should be set after successful detail fetch")

	events, err = d.ListMREvents(ctx, mr.ID)
	require.NoError(err)
	assert.NotEmpty(events, "review event should be persisted after detail retry")
}

func TestSyncerRefreshesEditedPRCommentWhenPRListIsUnchanged(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	d := openTestDB(t)

	now := time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC)
	repos := []RepoRef{{Owner: "owner", Name: "repo", PlatformHost: "github.com"}}

	commentID := int64(7001)
	commentUser := "reviewer"
	commentBody := "original body"
	commentUpdatedAt := now.Add(2 * time.Minute)

	mc := &mockClient{
		openIssues: []*gh.Issue{},
		comments: []*gh.IssueComment{{
			ID:        &commentID,
			Body:      &commentBody,
			User:      &gh.User{Login: &commentUser},
			CreatedAt: makeTimestamp(commentUpdatedAt),
			UpdatedAt: makeTimestamp(commentUpdatedAt),
		}},
	}
	mc.getPullRequestFn = func(_ context.Context, _, _ string, number int) (*gh.PullRequest, error) {
		require.Equal(1, number)
		return buildOpenPR(1, now), nil
	}
	prListCalls := 0
	mc.listOpenPRsFn = func(_ context.Context, _, _ string) ([]*gh.PullRequest, error) {
		prListCalls++
		if prListCalls == 1 {
			return []*gh.PullRequest{buildOpenPR(1, now)}, nil
		}
		return nil, &gh.ErrorResponse{
			Response: &http.Response{StatusCode: http.StatusNotModified},
		}
	}

	syncer := NewSyncer(
		map[string]Client{"github.com": mc},
		d, nil, repos, time.Minute, nil, testBudget(10000),
	)

	syncer.RunOnce(ctx)

	mr, err := d.GetMergeRequest(ctx, "owner", "repo", 1)
	require.NoError(err)
	require.NotNil(mr)

	events, err := d.ListMREvents(ctx, mr.ID)
	require.NoError(err)
	require.Len(events, 1)
	assert.Equal("original body", events[0].Body)

	editedBody := "edited body"
	editedAt := now.Add(4 * time.Minute)
	mc.comments = []*gh.IssueComment{{
		ID:        &commentID,
		Body:      &editedBody,
		User:      &gh.User{Login: &commentUser},
		CreatedAt: makeTimestamp(commentUpdatedAt),
		UpdatedAt: makeTimestamp(editedAt),
	}}

	syncer.RunOnce(ctx)

	mr, err = d.GetMergeRequest(ctx, "owner", "repo", 1)
	require.NoError(err)
	require.NotNil(mr)

	events, err = d.ListMREvents(ctx, mr.ID)
	require.NoError(err)
	require.Len(events, 1)
	assert.Equal("edited body", events[0].Body)
}

func TestSyncerRemovesDeletedPRCommentWhenPRListIsUnchanged(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	d := openTestDB(t)

	now := time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC)
	repos := []RepoRef{{Owner: "owner", Name: "repo", PlatformHost: "github.com"}}

	commentID := int64(7002)
	commentUser := "reviewer"
	commentBody := "to be deleted"
	commentTime := now.Add(2 * time.Minute)

	mc := &mockClient{
		openIssues: []*gh.Issue{},
		comments: []*gh.IssueComment{{
			ID:        &commentID,
			Body:      &commentBody,
			User:      &gh.User{Login: &commentUser},
			CreatedAt: makeTimestamp(commentTime),
			UpdatedAt: makeTimestamp(commentTime),
		}},
	}
	mc.getPullRequestFn = func(_ context.Context, _, _ string, number int) (*gh.PullRequest, error) {
		require.Equal(1, number)
		return buildOpenPR(1, now), nil
	}
	prListCalls := 0
	mc.listOpenPRsFn = func(_ context.Context, _, _ string) ([]*gh.PullRequest, error) {
		prListCalls++
		if prListCalls == 1 {
			return []*gh.PullRequest{buildOpenPR(1, now)}, nil
		}
		return nil, &gh.ErrorResponse{
			Response: &http.Response{StatusCode: http.StatusNotModified},
		}
	}

	syncer := NewSyncer(
		map[string]Client{"github.com": mc},
		d, nil, repos, time.Minute, nil, testBudget(10000),
	)

	syncer.RunOnce(ctx)

	mr, err := d.GetMergeRequest(ctx, "owner", "repo", 1)
	require.NoError(err)
	require.NotNil(mr)
	require.Equal(1, mr.CommentCount)

	events, err := d.ListMREvents(ctx, mr.ID)
	require.NoError(err)
	require.Len(events, 1)

	mc.comments = []*gh.IssueComment{}

	syncer.RunOnce(ctx)

	mr, err = d.GetMergeRequest(ctx, "owner", "repo", 1)
	require.NoError(err)
	require.NotNil(mr)
	assert.Equal(0, mr.CommentCount)

	events, err = d.ListMREvents(ctx, mr.ID)
	require.NoError(err)
	assert.Empty(events)
	assert.Equal(now.UTC(), mr.LastActivityAt.UTC())
}

func TestSyncerRemovesDeletedIssueCommentWhenIssueListIsUnchanged(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	d := openTestDB(t)

	now := time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC)
	repos := []RepoRef{{Owner: "owner", Name: "repo", PlatformHost: "github.com"}}

	issueID := int64(801)
	issueNumber := 8
	issueTitle := "edited issue"
	issueState := "open"
	issueURL := "https://github.com/owner/repo/issues/8"
	openIssue := &gh.Issue{
		ID:        &issueID,
		Number:    &issueNumber,
		Title:     &issueTitle,
		State:     &issueState,
		HTMLURL:   &issueURL,
		CreatedAt: makeTimestamp(now),
		UpdatedAt: makeTimestamp(now),
	}

	commentID := int64(810)
	commentUser := "reviewer"
	commentBody := "issue comment"
	commentTime := now.Add(2 * time.Minute)

	mc := &partialFailureMock{}
	mc.openPRs = []*gh.PullRequest{}
	mc.openIssues = []*gh.Issue{openIssue}
	mc.comments = []*gh.IssueComment{{
		ID:        &commentID,
		Body:      &commentBody,
		User:      &gh.User{Login: &commentUser},
		CreatedAt: makeTimestamp(commentTime),
		UpdatedAt: makeTimestamp(commentTime),
	}}
	mc.listOpenPRsErr = notModifiedErr()
	mc.getIssueFn = func(_ context.Context, _, _ string, number int) (*gh.Issue, error) {
		require.Equal(issueNumber, number)
		return openIssue, nil
	}

	syncer := NewSyncer(
		map[string]Client{"github.com": mc},
		d, nil, repos, time.Minute, nil, testBudget(10000),
	)

	syncer.RunOnce(ctx)

	issue, err := d.GetIssue(ctx, "owner", "repo", issueNumber)
	require.NoError(err)
	require.NotNil(issue)
	require.Equal(1, issue.CommentCount)

	events, err := d.ListIssueEvents(ctx, issue.ID)
	require.NoError(err)
	require.Len(events, 1)

	mc.comments = []*gh.IssueComment{}
	mc.issuesCached = true

	syncer.RunOnce(ctx)

	issue, err = d.GetIssue(ctx, "owner", "repo", issueNumber)
	require.NoError(err)
	require.NotNil(issue)
	assert.Equal(0, issue.CommentCount)

	events, err = d.ListIssueEvents(ctx, issue.ID)
	require.NoError(err)
	assert.Empty(events)
	assert.Equal(now.UTC(), issue.LastActivityAt.UTC())
}

func TestFetchMRDetailRemovesDeletedCommentDuringFullRefresh(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	d := openTestDB(t)

	repoID, err := d.UpsertRepo(ctx, db.GitHubRepoIdentity("github.com", "owner", "repo"))
	require.NoError(err)

	now := time.Date(2024, 6, 2, 12, 0, 0, 0, time.UTC)
	firstUpdatedAt := now
	secondUpdatedAt := now.Add(time.Minute)
	commentID := int64(7101)
	commentAuthor := "reviewer"
	commentBody := "full refresh comment"
	commentTime := now.Add(2 * time.Minute)

	fetches := 0
	mc := &mockClient{
		comments: []*gh.IssueComment{{
			ID:        &commentID,
			Body:      &commentBody,
			User:      &gh.User{Login: &commentAuthor},
			CreatedAt: makeTimestamp(commentTime),
			UpdatedAt: makeTimestamp(commentTime),
		}},
		getPullRequestFn: func(_ context.Context, _, _ string, number int) (*gh.PullRequest, error) {
			require.Equal(1, number)
			fetches++
			if fetches == 1 {
				return buildOpenPR(1, firstUpdatedAt), nil
			}
			return buildOpenPR(1, secondUpdatedAt), nil
		},
	}
	syncer := NewSyncer(
		map[string]Client{"github.com": mc},
		d, nil, []RepoRef{{Owner: "owner", Name: "repo", PlatformHost: "github.com"}},
		time.Minute, nil, nil,
	)

	_, err = syncer.fetchMRDetail(
		ctx, RepoRef{Owner: "owner", Name: "repo", PlatformHost: "github.com"},
		repoID, 1, false,
	)
	require.NoError(err)

	mr, err := d.GetMergeRequest(ctx, "owner", "repo", 1)
	require.NoError(err)
	require.NotNil(mr)
	require.Equal(1, mr.CommentCount)
	assert.Equal(commentTime.UTC(), mr.LastActivityAt.UTC())

	events, err := d.ListMREvents(ctx, mr.ID)
	require.NoError(err)
	require.Len(events, 1)

	mc.comments = []*gh.IssueComment{}

	_, err = syncer.fetchMRDetail(
		ctx, RepoRef{Owner: "owner", Name: "repo", PlatformHost: "github.com"},
		repoID, 1, false,
	)
	require.NoError(err)

	mr, err = d.GetMergeRequest(ctx, "owner", "repo", 1)
	require.NoError(err)
	require.NotNil(mr)
	assert.Equal(0, mr.CommentCount)
	assert.Equal(secondUpdatedAt.UTC(), mr.LastActivityAt.UTC())

	events, err = d.ListMREvents(ctx, mr.ID)
	require.NoError(err)
	assert.Empty(events)
}

func TestFetchIssueDetailRemovesDeletedCommentDuringFullRefresh(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	d := openTestDB(t)

	repoID, err := d.UpsertRepo(ctx, db.GitHubRepoIdentity("github.com", "owner", "repo"))
	require.NoError(err)

	now := time.Date(2024, 6, 2, 12, 0, 0, 0, time.UTC)
	firstUpdatedAt := now
	secondUpdatedAt := now.Add(time.Minute)
	issueID := int64(820)
	issueNumber := 8
	issueTitle := "full refresh issue"
	issueState := "open"
	issueURL := "https://github.com/owner/repo/issues/8"
	commentID := int64(821)
	commentAuthor := "reviewer"
	commentBody := "full refresh issue comment"
	commentTime := now.Add(2 * time.Minute)

	fetches := 0
	mc := &mockClient{
		comments: []*gh.IssueComment{{
			ID:        &commentID,
			Body:      &commentBody,
			User:      &gh.User{Login: &commentAuthor},
			CreatedAt: makeTimestamp(commentTime),
			UpdatedAt: makeTimestamp(commentTime),
		}},
		getIssueFn: func(_ context.Context, _, _ string, number int) (*gh.Issue, error) {
			require.Equal(issueNumber, number)
			fetches++
			updatedAt := firstUpdatedAt
			if fetches > 1 {
				updatedAt = secondUpdatedAt
			}
			return &gh.Issue{
				ID:        &issueID,
				Number:    &issueNumber,
				Title:     &issueTitle,
				State:     &issueState,
				HTMLURL:   &issueURL,
				CreatedAt: makeTimestamp(now),
				UpdatedAt: makeTimestamp(updatedAt),
			}, nil
		},
	}
	syncer := NewSyncer(
		map[string]Client{"github.com": mc},
		d, nil, []RepoRef{{Owner: "owner", Name: "repo", PlatformHost: "github.com"}},
		time.Minute, nil, nil,
	)

	_, err = syncer.fetchIssueDetail(
		ctx, RepoRef{Owner: "owner", Name: "repo", PlatformHost: "github.com"},
		repoID, issueNumber,
	)
	require.NoError(err)

	issue, err := d.GetIssue(ctx, "owner", "repo", issueNumber)
	require.NoError(err)
	require.NotNil(issue)
	require.Equal(1, issue.CommentCount)
	assert.Equal(commentTime.UTC(), issue.LastActivityAt.UTC())

	events, err := d.ListIssueEvents(ctx, issue.ID)
	require.NoError(err)
	require.Len(events, 1)

	mc.comments = []*gh.IssueComment{}

	_, err = syncer.fetchIssueDetail(
		ctx, RepoRef{Owner: "owner", Name: "repo", PlatformHost: "github.com"},
		repoID, issueNumber,
	)
	require.NoError(err)

	issue, err = d.GetIssue(ctx, "owner", "repo", issueNumber)
	require.NoError(err)
	require.NotNil(issue)
	assert.Equal(0, issue.CommentCount)
	assert.Equal(secondUpdatedAt.UTC(), issue.LastActivityAt.UTC())

	events, err = d.ListIssueEvents(ctx, issue.ID)
	require.NoError(err)
	assert.Empty(events)
}

func TestSyncOpenMRFromBulkRemovesDeletedCommentsWhenCommentsAreComplete(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	d := openTestDB(t)

	repoID, err := d.UpsertRepo(ctx, db.GitHubRepoIdentity("github.com", "owner", "repo"))
	require.NoError(err)

	now := time.Date(2024, 6, 3, 12, 0, 0, 0, time.UTC)
	firstUpdatedAt := now
	secondUpdatedAt := now.Add(time.Minute)
	commentID := int64(9101)
	commentAuthor := "reviewer"
	commentBody := "bulk PR comment"
	commentTime := gh.Timestamp{Time: now.Add(2 * time.Minute)}

	commentTotal := 1
	syncer := NewSyncer(
		map[string]Client{"github.com": &mockClient{}},
		d, nil, []RepoRef{{Owner: "owner", Name: "repo", PlatformHost: "github.com"}},
		time.Minute, nil, nil,
	)
	repo := RepoRef{Owner: "owner", Name: "repo", PlatformHost: "github.com"}

	err = syncer.syncOpenMRFromBulk(ctx, repo, repoID, &BulkPR{
		PR: buildOpenPR(1, firstUpdatedAt),
		Comments: []*gh.IssueComment{{
			ID:        &commentID,
			Body:      &commentBody,
			User:      &gh.User{Login: &commentAuthor},
			CreatedAt: &commentTime,
			UpdatedAt: &commentTime,
		}},
		CommentsComplete: true,
		ReviewsComplete:  true,
		CommitsComplete:  true,
		TimelineComplete: true,
		CIComplete:       true,
	}, false)
	require.NoError(err)

	mr, err := d.GetMergeRequest(ctx, "owner", "repo", 1)
	require.NoError(err)
	require.NotNil(mr)
	require.Equal(1, mr.CommentCount)
	assert.Equal(commentTime.UTC(), mr.LastActivityAt.UTC())

	events, err := d.ListMREvents(ctx, mr.ID)
	require.NoError(err)
	require.Len(events, 1)

	err = syncer.syncOpenMRFromBulk(ctx, repo, repoID, &BulkPR{
		PR:               buildOpenPR(1, secondUpdatedAt),
		Comments:         []*gh.IssueComment{},
		CommentsComplete: true,
		ReviewsComplete:  true,
		CommitsComplete:  true,
		TimelineComplete: true,
		CIComplete:       true,
	}, false)
	require.NoError(err)

	mr, err = d.GetMergeRequest(ctx, "owner", "repo", 1)
	require.NoError(err)
	require.NotNil(mr)
	assert.Equal(0, mr.CommentCount)
	assert.Equal(secondUpdatedAt.UTC(), mr.LastActivityAt.UTC())

	events, err = d.ListMREvents(ctx, mr.ID)
	require.NoError(err)
	assert.Empty(events)
	_ = commentTotal
}

// TestSyncOpenMRFromBulkPersistsWorkflowApproval verifies the GraphQL
// bulk path persists the workflow approval snapshot on fully-synced
// PRs. Without this, the periodic GraphQL sync would mark a PR as
// detail-fetched while leaving workflow_approval_checked_at nil, so
// the DB-only GET would hide the Approve workflows button.
func TestSyncOpenMRFromBulkPersistsWorkflowApproval(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	d := openTestDB(t)

	repoID, err := d.UpsertRepo(ctx, db.GitHubRepoIdentity("github.com", "owner", "repo"))
	require.NoError(err)

	now := time.Date(2026, 4, 21, 12, 0, 0, 0, time.UTC)
	pr := buildOpenPR(1, now)
	headSHA := pr.GetHead().GetSHA()
	require.NotEmpty(headSHA)

	budgets := testBudget(1)
	mc := &mockClient{
		budget: budgets["github.com"],
		workflowRuns: []*gh.WorkflowRun{{
			ID:           new(int64(9001)),
			HeadSHA:      &headSHA,
			Event:        new("pull_request"),
			PullRequests: []*gh.PullRequest{{Number: new(1)}},
		}},
	}
	syncer := NewSyncer(
		map[string]Client{"github.com": mc},
		d, nil,
		[]RepoRef{{Owner: "owner", Name: "repo", PlatformHost: "github.com"}},
		time.Minute, nil, budgets,
	)
	repo := RepoRef{Owner: "owner", Name: "repo", PlatformHost: "github.com"}

	err = syncer.syncOpenMRFromBulk(ctx, repo, repoID, &BulkPR{
		PR:               pr,
		Comments:         []*gh.IssueComment{},
		CommentsComplete: true,
		ReviewsComplete:  true,
		CommitsComplete:  true,
		TimelineComplete: true,
		CIComplete:       true,
	}, false)
	require.NoError(err)

	got, err := d.GetMergeRequest(ctx, "owner", "repo", 1)
	require.NoError(err)
	require.NotNil(got)
	require.NotNil(got.WorkflowApprovalCheckedAt,
		"bulk allComplete must populate workflow_approval_checked_at")
	assert.Equal(headSHA, got.WorkflowApprovalHeadSHA)
	assert.True(got.WorkflowApprovalRequired)
	assert.Equal(1, got.WorkflowApprovalCount)
	assert.Equal(1, budgets["github.com"].Spent())
}

func TestSyncOpenMRFromBulkSkipsWorkflowApprovalWhenBudgetExhausted(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	d := openTestDB(t)

	repoID, err := d.UpsertRepo(ctx, db.GitHubRepoIdentity("github.com", "owner", "repo"))
	require.NoError(err)

	now := time.Date(2026, 4, 21, 12, 30, 0, 0, time.UTC)
	pr := buildOpenPR(1, now)
	headSHA := pr.GetHead().GetSHA()
	require.NotEmpty(headSHA)

	budgets := testBudget(1)
	budgets["github.com"].Spend(1)
	mc := &mockClient{
		budget: budgets["github.com"],
		workflowRuns: []*gh.WorkflowRun{{
			ID:           new(int64(9001)),
			HeadSHA:      &headSHA,
			Event:        new("pull_request"),
			PullRequests: []*gh.PullRequest{{Number: new(1)}},
		}},
	}
	syncer := NewSyncer(
		map[string]Client{"github.com": mc},
		d, nil,
		[]RepoRef{{Owner: "owner", Name: "repo", PlatformHost: "github.com"}},
		time.Minute, nil, budgets,
	)
	repo := RepoRef{Owner: "owner", Name: "repo", PlatformHost: "github.com"}

	err = syncer.syncOpenMRFromBulk(ctx, repo, repoID, &BulkPR{
		PR:               pr,
		Comments:         []*gh.IssueComment{},
		CommentsComplete: true,
		ReviewsComplete:  true,
		CommitsComplete:  true,
		TimelineComplete: true,
		CIComplete:       true,
	}, false)
	require.NoError(err)

	got, err := d.GetMergeRequest(ctx, "owner", "repo", 1)
	require.NoError(err)
	require.NotNil(got)
	assert.Nil(got.WorkflowApprovalCheckedAt)
	assert.Equal(1, budgets["github.com"].Spent())
}

// TestSyncOpenMRFromBulkSkipsWorkflowApprovalWhenIncomplete verifies
// that a partial bulk sync (CI not complete) does not advance the
// workflow approval snapshot. Such PRs stay eligible for REST detail
// drain, which is the path that refreshes the snapshot.
func TestSyncOpenMRFromBulkSkipsWorkflowApprovalWhenIncomplete(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	d := openTestDB(t)

	repoID, err := d.UpsertRepo(ctx, db.GitHubRepoIdentity("github.com", "owner", "repo"))
	require.NoError(err)

	now := time.Date(2026, 4, 21, 13, 0, 0, 0, time.UTC)
	pr := buildOpenPR(1, now)
	headSHA := pr.GetHead().GetSHA()
	require.NotEmpty(headSHA)

	// workflowRuns is populated so that, if the refresh were
	// triggered, the snapshot would land with required=true. The
	// allComplete gate must prevent that.
	mc := &mockClient{
		workflowRuns: []*gh.WorkflowRun{{
			ID:           new(int64(9001)),
			HeadSHA:      &headSHA,
			Event:        new("pull_request"),
			PullRequests: []*gh.PullRequest{{Number: new(1)}},
		}},
	}
	syncer := NewSyncer(
		map[string]Client{"github.com": mc},
		d, nil,
		[]RepoRef{{Owner: "owner", Name: "repo", PlatformHost: "github.com"}},
		time.Minute, nil, nil,
	)
	repo := RepoRef{Owner: "owner", Name: "repo", PlatformHost: "github.com"}

	err = syncer.syncOpenMRFromBulk(ctx, repo, repoID, &BulkPR{
		PR:               pr,
		Comments:         []*gh.IssueComment{},
		CommentsComplete: true,
		ReviewsComplete:  true,
		CommitsComplete:  true,
		TimelineComplete: true,
		CIComplete:       false, // partial — skip workflow approval
	}, false)
	require.NoError(err)

	got, err := d.GetMergeRequest(ctx, "owner", "repo", 1)
	require.NoError(err)
	require.NotNil(got)
	assert.Nil(got.WorkflowApprovalCheckedAt,
		"partial bulk sync must not advance workflow_approval_checked_at")
	assert.False(got.WorkflowApprovalRequired)
	assert.Equal(0, got.WorkflowApprovalCount)
}

func TestSyncOpenMRFromBulkUpdatesCommentFieldsWhenOnlyCommentsAreComplete(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	d := openTestDB(t)

	repoID, err := d.UpsertRepo(ctx, db.GitHubRepoIdentity("github.com", "owner", "repo"))
	require.NoError(err)

	now := time.Date(2024, 6, 3, 13, 0, 0, 0, time.UTC)
	firstUpdatedAt := now
	secondUpdatedAt := now.Add(time.Minute)
	commentID := int64(9301)
	commentAuthor := "reviewer"
	commentBody := "partial bulk PR comment"
	commentTime := gh.Timestamp{Time: now.Add(2 * time.Minute)}

	syncer := NewSyncer(
		map[string]Client{"github.com": &mockClient{}},
		d, nil, []RepoRef{{Owner: "owner", Name: "repo", PlatformHost: "github.com"}},
		time.Minute, nil, nil,
	)
	repo := RepoRef{Owner: "owner", Name: "repo", PlatformHost: "github.com"}

	err = syncer.syncOpenMRFromBulk(ctx, repo, repoID, &BulkPR{
		PR: buildOpenPR(1, firstUpdatedAt),
		Comments: []*gh.IssueComment{{
			ID:        &commentID,
			Body:      &commentBody,
			User:      &gh.User{Login: &commentAuthor},
			CreatedAt: &commentTime,
			UpdatedAt: &commentTime,
		}},
		CommentsComplete: true,
		ReviewsComplete:  false,
		CommitsComplete:  false,
		CIComplete:       false,
	}, false)
	require.NoError(err)

	mr, err := d.GetMergeRequest(ctx, "owner", "repo", 1)
	require.NoError(err)
	require.NotNil(mr)
	assert.Equal(1, mr.CommentCount)
	assert.Equal(commentTime.UTC(), mr.LastActivityAt.UTC())

	events, err := d.ListMREvents(ctx, mr.ID)
	require.NoError(err)
	require.Len(events, 1)

	err = syncer.syncOpenMRFromBulk(ctx, repo, repoID, &BulkPR{
		PR:               buildOpenPR(1, secondUpdatedAt),
		Comments:         []*gh.IssueComment{},
		CommentsComplete: true,
		ReviewsComplete:  false,
		CommitsComplete:  false,
		CIComplete:       false,
	}, false)
	require.NoError(err)

	mr, err = d.GetMergeRequest(ctx, "owner", "repo", 1)
	require.NoError(err)
	require.NotNil(mr)
	assert.Equal(0, mr.CommentCount)
	assert.Equal(secondUpdatedAt.UTC(), mr.LastActivityAt.UTC())

	events, err = d.ListMREvents(ctx, mr.ID)
	require.NoError(err)
	assert.Empty(events)
}

func TestSyncOpenMRFromBulkStoresTimelineEvents(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	d := openTestDB(t)

	repoID, err := d.UpsertRepo(ctx, db.GitHubRepoIdentity("github.com", "owner", "repo"))
	require.NoError(err)

	now := time.Date(2024, 6, 3, 14, 0, 0, 0, time.UTC)
	timelineAt := now.Add(3 * time.Minute)
	syncer := NewSyncer(
		map[string]Client{"github.com": &mockClient{}},
		d, nil, []RepoRef{{Owner: "owner", Name: "repo", PlatformHost: "github.com"}},
		time.Minute, nil, nil,
	)
	repo := RepoRef{Owner: "owner", Name: "repo", PlatformHost: "github.com"}

	err = syncer.syncOpenMRFromBulk(ctx, repo, repoID, &BulkPR{
		PR: buildOpenPR(1, now),
		TimelineEvents: []PullRequestTimelineEvent{{
			NodeID:          "BRC_1",
			EventType:       "base_ref_changed",
			Actor:           "alice",
			PreviousRefName: "main",
			CurrentRefName:  "release",
			CreatedAt:       timelineAt,
		}, {
			NodeID:               "CDE_1",
			EventType:            "comment_deleted",
			Actor:                "maintainer",
			DeletedCommentAuthor: "reviewer",
			CreatedAt:            timelineAt.Add(time.Minute),
		}},
		CommentsComplete: true,
		ReviewsComplete:  true,
		CommitsComplete:  true,
		TimelineComplete: true,
		CIComplete:       true,
	}, false)
	require.NoError(err)

	mr, err := d.GetMergeRequest(ctx, "owner", "repo", 1)
	require.NoError(err)
	require.NotNil(mr)
	require.NotNil(mr.DetailFetchedAt)
	assert.Equal(now.UTC(), mr.LastActivityAt.UTC())

	events, err := d.ListMREvents(ctx, mr.ID)
	require.NoError(err)
	require.Len(events, 2)
	assert.Equal("comment_deleted", events[0].EventType)
	assert.Equal("deleted a comment from reviewer", events[0].Summary)
	assert.Equal("base_ref_changed", events[1].EventType)
	assert.Equal("main -> release", events[1].Summary)
}

// buildOpenPRWithSHA mirrors buildOpenPR but lets the caller set the
// head SHA so head-change scenarios can be exercised.
func buildOpenPRWithSHA(number int, updatedAt time.Time, headSHA string) *gh.PullRequest {
	pr := buildOpenPR(number, updatedAt)
	pr.Head.SHA = &headSHA
	return pr
}

func TestSyncOpenMRFromBulkClearsCIWhenHeadSHAChanges(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)
	ctx := t.Context()
	d := openTestDB(t)

	repoID, err := d.UpsertRepo(ctx, db.GitHubRepoIdentity("github.com", "owner", "repo"))
	require.NoError(err)

	// Seed an existing MR with the old head SHA and stored CI status.
	now := time.Date(2026, 4, 28, 12, 0, 0, 0, time.UTC)
	_, err = d.UpsertMergeRequest(ctx, &db.MergeRequest{
		RepoID:          repoID,
		PlatformID:      int64(1) * 1000,
		Number:          1,
		URL:             "https://github.com/owner/repo/pull/1",
		Title:           "test PR",
		Author:          "alice",
		State:           "open",
		PlatformHeadSHA: "oldhead",
		CIStatus:        "success",
		CIChecksJSON:    `[{"name":"tests","status":"completed","conclusion":"success"}]`,
		// Seed as true so the cleared assertion below catches the case
		// where syncOpenMRFromBulk carries the stale flag forward.
		CIHadPending:   true,
		CreatedAt:      now,
		UpdatedAt:      now,
		LastActivityAt: now,
	})
	require.NoError(err)

	syncer := NewSyncer(
		map[string]Client{"github.com": &mockClient{}},
		d, nil, []RepoRef{{Owner: "owner", Name: "repo", PlatformHost: "github.com"}},
		time.Minute, nil, nil,
	)
	repo := RepoRef{Owner: "owner", Name: "repo", PlatformHost: "github.com"}

	// New bulk fetch reports a new head SHA with CIComplete=false (the
	// CI page was truncated). Without the head-SHA guard, the upsert
	// would carry the old CI fields forward onto the new commit.
	err = syncer.syncOpenMRFromBulk(ctx, repo, repoID, &BulkPR{
		PR:               buildOpenPRWithSHA(1, now.Add(time.Minute), "newhead"),
		Comments:         []*gh.IssueComment{},
		CommentsComplete: true,
		ReviewsComplete:  true,
		CommitsComplete:  true,
		CIComplete:       false,
	}, false)
	require.NoError(err)

	mr, err := d.GetMergeRequest(ctx, "owner", "repo", 1)
	require.NoError(err)
	require.NotNil(mr)
	assert.Equal("newhead", mr.PlatformHeadSHA)
	assert.Empty(mr.CIStatus)
	assert.Empty(mr.CIChecksJSON)
	assert.False(mr.CIHadPending)
}

func TestSyncOpenMRFromBulkPreservesCIWhenHeadSHAUnchanged(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)
	ctx := t.Context()
	d := openTestDB(t)

	repoID, err := d.UpsertRepo(ctx, db.GitHubRepoIdentity("github.com", "owner", "repo"))
	require.NoError(err)

	// Same head SHA as buildOpenPR uses by default.
	const sameSHA = "abc123def456"
	now := time.Date(2026, 4, 28, 12, 0, 0, 0, time.UTC)
	_, err = d.UpsertMergeRequest(ctx, &db.MergeRequest{
		RepoID:          repoID,
		PlatformID:      int64(1) * 1000,
		Number:          1,
		URL:             "https://github.com/owner/repo/pull/1",
		Title:           "test PR",
		Author:          "alice",
		State:           "open",
		PlatformHeadSHA: sameSHA,
		CIStatus:        "success",
		CIChecksJSON:    `[{"name":"tests","status":"completed","conclusion":"success"}]`,
		// Seed as true so the preserved assertion below distinguishes
		// the preserve path from a default-zero pending flag.
		CIHadPending:   true,
		CreatedAt:      now,
		UpdatedAt:      now,
		LastActivityAt: now,
	})
	require.NoError(err)

	syncer := NewSyncer(
		map[string]Client{"github.com": &mockClient{}},
		d, nil, []RepoRef{{Owner: "owner", Name: "repo", PlatformHost: "github.com"}},
		time.Minute, nil, nil,
	)
	repo := RepoRef{Owner: "owner", Name: "repo", PlatformHost: "github.com"}

	// CIComplete=false would normally skip the CI write. The existing
	// CI must be preserved because the head SHA is unchanged.
	err = syncer.syncOpenMRFromBulk(ctx, repo, repoID, &BulkPR{
		PR:               buildOpenPR(1, now.Add(time.Minute)),
		Comments:         []*gh.IssueComment{},
		CommentsComplete: true,
		ReviewsComplete:  true,
		CommitsComplete:  true,
		CIComplete:       false,
	}, false)
	require.NoError(err)

	mr, err := d.GetMergeRequest(ctx, "owner", "repo", 1)
	require.NoError(err)
	require.NotNil(mr)
	assert.Equal(sameSHA, mr.PlatformHeadSHA)
	assert.Equal("success", mr.CIStatus)
	assert.Contains(mr.CIChecksJSON, "tests")
	assert.True(mr.CIHadPending)
}

func TestSyncOpenIssueFromBulkRemovesDeletedCommentsWhenCommentsAreComplete(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	d := openTestDB(t)

	repoID, err := d.UpsertRepo(ctx, db.GitHubRepoIdentity("github.com", "owner", "repo"))
	require.NoError(err)

	now := time.Date(2024, 6, 3, 12, 0, 0, 0, time.UTC)
	firstUpdatedAt := gh.Timestamp{Time: now}
	secondUpdatedAt := gh.Timestamp{Time: now.Add(time.Minute)}
	issueID := int64(9201)
	issueNumber := 9
	issueTitle := "bulk issue"
	issueState := "open"
	issueURL := "https://github.com/owner/repo/issues/9"
	issueAuthor := "alice"
	commentID := int64(9202)
	commentAuthor := "reviewer"
	commentBody := "bulk issue comment"
	commentTime := gh.Timestamp{Time: now.Add(2 * time.Minute)}
	commentTotal := 1

	syncer := NewSyncer(
		map[string]Client{"github.com": &mockClient{}},
		d, nil, []RepoRef{{Owner: "owner", Name: "repo", PlatformHost: "github.com"}},
		time.Minute, nil, nil,
	)
	repo := RepoRef{Owner: "owner", Name: "repo", PlatformHost: "github.com"}

	err = syncer.syncOpenIssueFromBulk(ctx, repo, repoID, &BulkIssue{
		Issue: &gh.Issue{
			ID:        &issueID,
			Number:    &issueNumber,
			Title:     &issueTitle,
			State:     &issueState,
			HTMLURL:   &issueURL,
			Comments:  &commentTotal,
			User:      &gh.User{Login: &issueAuthor},
			CreatedAt: &firstUpdatedAt,
			UpdatedAt: &firstUpdatedAt,
		},
		Comments: []*gh.IssueComment{{
			ID:        &commentID,
			Body:      &commentBody,
			User:      &gh.User{Login: &commentAuthor},
			CreatedAt: &commentTime,
			UpdatedAt: &commentTime,
		}},
		CommentsComplete: true,
		TimelineComplete: true,
	})
	require.NoError(err)

	issue, err := d.GetIssue(ctx, "owner", "repo", issueNumber)
	require.NoError(err)
	require.NotNil(issue)
	require.Equal(1, issue.CommentCount)
	assert.Equal(commentTime.UTC(), issue.LastActivityAt.UTC())

	events, err := d.ListIssueEvents(ctx, issue.ID)
	require.NoError(err)
	require.Len(events, 1)

	zeroComments := 0
	err = syncer.syncOpenIssueFromBulk(ctx, repo, repoID, &BulkIssue{
		Issue: &gh.Issue{
			ID:        &issueID,
			Number:    &issueNumber,
			Title:     &issueTitle,
			State:     &issueState,
			HTMLURL:   &issueURL,
			Comments:  &zeroComments,
			User:      &gh.User{Login: &issueAuthor},
			CreatedAt: &firstUpdatedAt,
			UpdatedAt: &secondUpdatedAt,
		},
		Comments:         []*gh.IssueComment{},
		CommentsComplete: true,
		TimelineComplete: true,
	})
	require.NoError(err)

	issue, err = d.GetIssue(ctx, "owner", "repo", issueNumber)
	require.NoError(err)
	require.NotNil(issue)
	assert.Equal(0, issue.CommentCount)
	assert.Equal(secondUpdatedAt.UTC(), issue.LastActivityAt.UTC())

	events, err = d.ListIssueEvents(ctx, issue.ID)
	require.NoError(err)
	assert.Empty(events)
}

func TestSyncIssuesFromListLogsProgressForLargeIssueSets(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)
	ctx := t.Context()
	d := openTestDB(t)

	repo := RepoRef{Owner: "owner", Name: "repo", PlatformHost: "github.com"}
	repoID, err := d.UpsertRepo(ctx, db.GitHubRepoIdentity("github.com", repo.Owner, repo.Name))
	require.NoError(err)

	now := time.Now().UTC().Truncate(time.Second)
	issues := make([]*gh.Issue, 0, 201)
	for number := 1; number <= 201; number++ {
		issues = append(issues, buildOpenIssue(number, now))
	}

	var buf bytes.Buffer
	sw := &syncedWriter{w: &buf}
	h := slog.NewTextHandler(sw, &slog.HandlerOptions{Level: slog.LevelInfo})
	orig := slog.Default()
	slog.SetDefault(slog.New(h))
	t.Cleanup(func() { slog.SetDefault(orig) })

	client := &mockClient{}
	syncer := NewSyncer(
		map[string]Client{"github.com": client},
		d, nil, []RepoRef{repo}, time.Minute, nil, nil,
	)

	err = syncer.syncIssuesFromList(ctx, client, repo, repoID, issues, false)
	require.NoError(err)

	logs := buf.String()
	assert.Contains(logs, `msg="issue sync started"`)
	assert.Contains(logs, "repo=owner/repo")
	assert.Contains(logs, "platform=github")
	assert.Contains(logs, "host=github.com")
	assert.Contains(logs, "source=rest")
	assert.Contains(logs, "total=201")
	assert.Contains(logs, `msg="issue sync progress"`)
	assert.Contains(logs, "processed=100")
	assert.Contains(logs, "processed=200")
	assert.Contains(logs, `msg="issue sync completed"`)
	assert.Contains(logs, "processed=201")
}

func TestSyncRepoGraphQLIssues(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)
	ctx := t.Context()
	d := openTestDB(t)

	repoID, err := d.UpsertRepo(ctx, db.GitHubRepoIdentity("github.com", "owner", "repo"))
	require.NoError(err)

	now := time.Now().UTC().Truncate(time.Second)
	mock := &mockClient{}
	syncer := NewSyncer(
		map[string]Client{"github.com": mock},
		d, nil,
		[]RepoRef{{Owner: "owner", Name: "repo", PlatformHost: "github.com"}},
		time.Minute, nil, nil,
	)

	issueID := int64(10000)
	issueNumber := 10
	issueTitle := "Bug report"
	issueState := "open"
	issueBody := "Something broke"
	issueURL := "https://github.com/owner/repo/issues/10"
	issueAuthor := "alice"
	commentID := int64(501)
	commentBody := "I see this too"
	commentLogin := "bob"
	commentTime := gh.Timestamp{Time: now}
	// TotalCount (5) deliberately > len(nodes) (1). Proves the sync
	// uses GraphQL's TotalCount, not node length.
	issueCommentTotal := 5
	result := &RepoBulkResult{
		Issues: []BulkIssue{
			{
				Issue: &gh.Issue{
					ID:        &issueID,
					Number:    &issueNumber,
					Title:     &issueTitle,
					State:     &issueState,
					Body:      &issueBody,
					HTMLURL:   &issueURL,
					Comments:  &issueCommentTotal,
					User:      &gh.User{Login: &issueAuthor},
					CreatedAt: &commentTime,
					UpdatedAt: &commentTime,
				},
				Comments: []*gh.IssueComment{
					{
						ID:        &commentID,
						Body:      &commentBody,
						User:      &gh.User{Login: &commentLogin},
						CreatedAt: &commentTime,
						UpdatedAt: &commentTime,
					},
				},
				CommentsComplete: true,
				TimelineComplete: true,
			},
		},
	}

	err = syncer.doSyncRepoGraphQLIssues(ctx,
		RepoRef{Owner: "owner", Name: "repo", PlatformHost: "github.com"},
		repoID, result,
	)
	require.NoError(err)

	// Verify issue in DB.
	issue, err := d.GetIssue(ctx, "owner", "repo", 10)
	require.NoError(err)
	require.NotNil(issue)
	assert.Equal("Bug report", issue.Title)
	assert.Equal("alice", issue.Author)
	assert.Equal("open", issue.State)
	// Count comes from GraphQL TotalCount (5), not len(Nodes) (1).
	assert.Equal(5, issue.CommentCount)

	// Verify comment event.
	events, err := d.ListIssueEvents(ctx, issue.ID)
	require.NoError(err)
	assert.Len(events, 1)
	assert.Equal("I see this too", events[0].Body)

	// Comments were complete — ListIssueComments should NOT be called.
	assert.Equal(int32(0), mock.listIssueCommentsCalled.Load())

	// detail_fetched_at should be set for complete bulk issues.
	assert.NotNil(issue.DetailFetchedAt)
}

func TestResolveDisplayName(t *testing.T) {
	ctx := t.Context()

	tests := []struct {
		name          string
		login         string
		getUserFn     func(context.Context, string) (*gh.User, error)
		wantName      string
		wantOK        bool
		wantAPICalled bool
	}{
		{
			name:  "regular user with display name",
			login: "alice",
			getUserFn: func(_ context.Context, login string) (*gh.User, error) {
				name := "Alice Smith"
				return &gh.User{Login: &login, Name: &name}, nil
			},
			wantName:      "Alice Smith",
			wantOK:        true,
			wantAPICalled: true,
		},
		{
			name:  "regular user without display name",
			login: "bob",
			getUserFn: func(_ context.Context, login string) (*gh.User, error) {
				return &gh.User{Login: &login}, nil
			},
			wantName:      "",
			wantOK:        true,
			wantAPICalled: true,
		},
		{
			name:  "bot login skips API call",
			login: "renovate[bot]",
			getUserFn: func(_ context.Context, _ string) (*gh.User, error) {
				return nil, nil
			},
			wantName:      "renovate[bot]",
			wantOK:        true,
			wantAPICalled: false,
		},
		{
			name:  "API-returned bot uses login as display name",
			login: "ci-helper",
			getUserFn: func(_ context.Context, login string) (*gh.User, error) {
				botType := "Bot"
				return &gh.User{Login: &login, Type: &botType}, nil
			},
			wantName:      "ci-helper",
			wantOK:        true,
			wantAPICalled: true,
		},
		{
			name:  "user not found returns false",
			login: "ghost",
			getUserFn: func(_ context.Context, _ string) (*gh.User, error) {
				return nil, fmt.Errorf("GET https://api.github.com/users/ghost: 404 Not Found")
			},
			wantName:      "",
			wantOK:        false,
			wantAPICalled: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert := Assert.New(t)
			apiCalled := false
			mc := &mockClient{getUserFn: func(ctx context.Context, login string) (*gh.User, error) {
				apiCalled = true
				return tt.getUserFn(ctx, login)
			}}
			syncer := NewSyncer(
				map[string]Client{"github.com": mc}, nil, nil, nil,
				time.Minute, nil, nil,
			)
			name, ok := syncer.resolveDisplayName(ctx, mc, "github.com", tt.login)
			assert.Equal(tt.wantName, name)
			assert.Equal(tt.wantOK, ok)
			assert.Equal(tt.wantAPICalled, apiCalled, "GetUser call expectation")
		})
	}
}

func TestResolveDisplayName_CachesNegativeResult(t *testing.T) {
	assert := Assert.New(t)
	ctx := t.Context()

	callCount := 0
	mc := &mockClient{
		getUserFn: func(_ context.Context, _ string) (*gh.User, error) {
			callCount++
			return nil, fmt.Errorf("404 Not Found")
		},
	}
	syncer := NewSyncer(
		map[string]Client{"github.com": mc}, nil, nil, nil,
		time.Minute, nil, nil,
	)

	// First call: hits API, returns failure.
	name1, ok1 := syncer.resolveDisplayName(ctx, mc, "github.com", "renovate")
	assert.Empty(name1)
	assert.False(ok1)
	assert.Equal(1, callCount)

	// Second call: should use cache, no additional API call.
	name2, ok2 := syncer.resolveDisplayName(ctx, mc, "github.com", "renovate")
	assert.Empty(name2)
	assert.False(ok2)
	assert.Equal(1, callCount, "GetUser should not be called again for cached failure")
}

func TestResolveDisplayName_CachesSuccessfulEmptyName(t *testing.T) {
	assert := Assert.New(t)
	ctx := t.Context()

	callCount := 0
	mc := &mockClient{
		getUserFn: func(_ context.Context, login string) (*gh.User, error) {
			callCount++
			return &gh.User{Login: &login}, nil // no display name
		},
	}
	syncer := NewSyncer(
		map[string]Client{"github.com": mc}, nil, nil, nil,
		time.Minute, nil, nil,
	)

	// First call: hits API, succeeds with empty name.
	name1, ok1 := syncer.resolveDisplayName(ctx, mc, "github.com", "no-profile")
	assert.Empty(name1)
	assert.True(ok1, "successful lookup of empty name should return ok=true")
	assert.Equal(1, callCount)

	// Second call: cache hit must still return ok=true, not flip to false.
	name2, ok2 := syncer.resolveDisplayName(ctx, mc, "github.com", "no-profile")
	assert.Empty(name2)
	assert.True(ok2, "cached empty name must remain ok=true")
	assert.Equal(1, callCount, "GetUser should not be called again for cached success")
}

func TestSyncRepoGraphQLIssuesCommentsIncomplete(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)
	ctx := t.Context()
	d := openTestDB(t)

	repoID, err := d.UpsertRepo(ctx, db.GitHubRepoIdentity("github.com", "owner", "repo"))
	require.NoError(err)

	now := time.Now().UTC().Truncate(time.Second)
	commentTime := gh.Timestamp{Time: now}

	commentID := int64(777)
	commentBody := "REST comment"
	commentLogin := "carol"

	mock := &mockClient{
		comments: []*gh.IssueComment{
			{
				ID:        &commentID,
				Body:      &commentBody,
				User:      &gh.User{Login: &commentLogin},
				CreatedAt: &commentTime,
				UpdatedAt: &commentTime,
			},
		},
	}
	syncer := NewSyncer(
		map[string]Client{"github.com": mock},
		d, nil,
		[]RepoRef{{Owner: "owner", Name: "repo", PlatformHost: "github.com"}},
		time.Minute, nil, nil,
	)

	issueID := int64(20000)
	issueNumber := 20
	issueTitle := "Lots of comments"
	issueState := "open"
	issueURL := "https://github.com/owner/repo/issues/20"
	issueLogin := "dave"
	result := &RepoBulkResult{
		Issues: []BulkIssue{
			{
				Issue: &gh.Issue{
					ID:        &issueID,
					Number:    &issueNumber,
					Title:     &issueTitle,
					State:     &issueState,
					HTMLURL:   &issueURL,
					User:      &gh.User{Login: &issueLogin},
					CreatedAt: &commentTime,
					UpdatedAt: &commentTime,
				},
				CommentsComplete: false,
			},
		},
	}

	err = syncer.doSyncRepoGraphQLIssues(ctx,
		RepoRef{Owner: "owner", Name: "repo", PlatformHost: "github.com"},
		repoID, result,
	)
	require.NoError(err)

	// REST fallback should have been called
	assert.Equal(int32(1), mock.listIssueCommentsCalled.Load())

	// Verify the REST comment landed
	issue, err := d.GetIssue(ctx, "owner", "repo", 20)
	require.NoError(err)
	require.NotNil(issue)

	events, err := d.ListIssueEvents(ctx, issue.ID)
	require.NoError(err)
	assert.Len(events, 1)
	assert.Equal("REST comment", events[0].Body)
}

func TestSyncRepoGraphQLIssuesClosureDetection(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)
	ctx := t.Context()
	d := openTestDB(t)

	repoID, err := d.UpsertRepo(ctx, db.GitHubRepoIdentity("github.com", "owner", "repo"))
	require.NoError(err)

	now := time.Now().UTC().Truncate(time.Second)

	// Pre-seed an open issue that will not appear in GraphQL results
	_, err = d.UpsertIssue(ctx, &db.Issue{
		RepoID:         repoID,
		PlatformID:     30000,
		Number:         30,
		URL:            "https://github.com/owner/repo/issues/30",
		Title:          "Will be closed",
		Author:         "eve",
		State:          "open",
		CreatedAt:      now,
		UpdatedAt:      now,
		LastActivityAt: now,
	})
	require.NoError(err)

	closedAt := gh.Timestamp{Time: now}
	closedState := "closed"
	closedIssueID := int64(30000)
	closedNumber := 30
	closedTitle := "Will be closed"

	mock := &mockClient{
		getIssueFn: func(_ context.Context, _, _ string, number int) (*gh.Issue, error) {
			if number == 30 {
				return &gh.Issue{
					ID:       &closedIssueID,
					Number:   &closedNumber,
					Title:    &closedTitle,
					State:    &closedState,
					ClosedAt: &closedAt,
				}, nil
			}
			return nil, fmt.Errorf("unexpected issue %d", number)
		},
	}

	syncer := NewSyncer(
		map[string]Client{"github.com": mock},
		d, nil,
		[]RepoRef{{Owner: "owner", Name: "repo", PlatformHost: "github.com"}},
		time.Minute, nil, nil,
	)

	// GraphQL returns no issues (issue #30 was closed)
	result := &RepoBulkResult{Issues: []BulkIssue{}}

	err = syncer.doSyncRepoGraphQLIssues(ctx,
		RepoRef{Owner: "owner", Name: "repo", PlatformHost: "github.com"},
		repoID, result,
	)
	require.NoError(err)

	// Issue should now be closed
	issue, err := d.GetIssue(ctx, "owner", "repo", 30)
	require.NoError(err)
	require.NotNil(issue)
	assert.Equal("closed", issue.State)
	assert.NotNil(issue.ClosedAt)
}

func TestSyncRepoGraphQLIssuesPreservesExistingFields(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)
	ctx := t.Context()
	d := openTestDB(t)

	repoID, err := d.UpsertRepo(ctx, db.GitHubRepoIdentity("github.com", "owner", "repo"))
	require.NoError(err)

	now := time.Now().UTC().Truncate(time.Second)
	fetchedAt := now.Add(-time.Hour)

	// Pre-seed issue with existing derived fields
	_, err = d.UpsertIssue(ctx, &db.Issue{
		RepoID:          repoID,
		PlatformID:      40000,
		Number:          40,
		URL:             "https://github.com/owner/repo/issues/40",
		Title:           "Existing issue",
		Author:          "frank",
		State:           "open",
		CommentCount:    5,
		DetailFetchedAt: &fetchedAt,
		CreatedAt:       now,
		UpdatedAt:       now,
		LastActivityAt:  now,
	})
	require.NoError(err)

	commentTime := gh.Timestamp{Time: now}
	mock := &mockClient{}
	syncer := NewSyncer(
		map[string]Client{"github.com": mock},
		d, nil,
		[]RepoRef{{Owner: "owner", Name: "repo", PlatformHost: "github.com"}},
		time.Minute, nil, nil,
	)

	// GraphQL returns the same issue with no comments (incomplete)
	issueID := int64(40000)
	issueNumber := 40
	issueTitle := "Existing issue"
	issueState := "open"
	issueURL := "https://github.com/owner/repo/issues/40"
	issueLogin := "frank"
	result := &RepoBulkResult{
		Issues: []BulkIssue{
			{
				Issue: &gh.Issue{
					ID:        &issueID,
					Number:    &issueNumber,
					Title:     &issueTitle,
					State:     &issueState,
					HTMLURL:   &issueURL,
					User:      &gh.User{Login: &issueLogin},
					CreatedAt: &commentTime,
					UpdatedAt: &commentTime,
				},
				CommentsComplete: false,
			},
		},
	}

	err = syncer.doSyncRepoGraphQLIssues(ctx,
		RepoRef{Owner: "owner", Name: "repo", PlatformHost: "github.com"},
		repoID, result,
	)
	require.NoError(err)

	// DetailFetchedAt is cleared before REST fallback, then re-set
	// after successful refreshIssueTimeline. CommentCount is updated
	// by the REST fallback (0 comments returned by the mock).
	issue, err := d.GetIssue(ctx, "owner", "repo", 40)
	require.NoError(err)
	require.NotNil(issue)
	assert.NotNil(issue.DetailFetchedAt)
	assert.Equal(0, issue.CommentCount)
}

func TestSyncRepoGraphQLIssuesClearsDetailFetchedAtOnFailedFallback(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)
	ctx := t.Context()
	d := openTestDB(t)

	repoID, err := d.UpsertRepo(ctx, db.GitHubRepoIdentity("github.com", "owner", "repo"))
	require.NoError(err)

	now := time.Now().UTC().Truncate(time.Second)
	fetchedAt := now.Add(-time.Hour)

	// Pre-seed issue with non-nil DetailFetchedAt (previously fetched).
	_, err = d.UpsertIssue(ctx, &db.Issue{
		RepoID:          repoID,
		PlatformID:      45000,
		Number:          45,
		URL:             "https://github.com/owner/repo/issues/45",
		Title:           "Previously fetched",
		Author:          "grace",
		State:           "open",
		CommentCount:    3,
		DetailFetchedAt: &fetchedAt,
		CreatedAt:       now,
		UpdatedAt:       now,
		LastActivityAt:  now,
	})
	require.NoError(err)

	commentTime := gh.Timestamp{Time: now}
	mock := &mockClient{
		listIssueCommentsErr: fmt.Errorf("transient API failure"),
	}
	syncer := NewSyncer(
		map[string]Client{"github.com": mock},
		d, nil,
		[]RepoRef{{Owner: "owner", Name: "repo", PlatformHost: "github.com"}},
		time.Minute, nil, nil,
	)

	issueID := int64(45000)
	issueNumber := 45
	issueTitle := "Previously fetched"
	issueState := "open"
	issueURL := "https://github.com/owner/repo/issues/45"
	issueLogin := "grace"
	result := &RepoBulkResult{
		Issues: []BulkIssue{
			{
				Issue: &gh.Issue{
					ID:        &issueID,
					Number:    &issueNumber,
					Title:     &issueTitle,
					State:     &issueState,
					HTMLURL:   &issueURL,
					User:      &gh.User{Login: &issueLogin},
					CreatedAt: &commentTime,
					UpdatedAt: &commentTime,
				},
				CommentsComplete: false, // triggers REST fallback
			},
		},
	}

	// REST fallback will fail due to listIssueCommentsErr.
	err = syncer.doSyncRepoGraphQLIssues(ctx,
		RepoRef{Owner: "owner", Name: "repo", PlatformHost: "github.com"},
		repoID, result,
	)
	// Partial failure expected.
	require.Error(err)

	// DetailFetchedAt must be nil so the detail drain re-queues this issue.
	issue, err := d.GetIssue(ctx, "owner", "repo", 45)
	require.NoError(err)
	require.NotNil(issue)
	assert.Nil(issue.DetailFetchedAt)
}

func TestSyncRepoGraphQLIssuesFallbackToREST(t *testing.T) {
	assert := Assert.New(t)
	ctx := t.Context()
	d := openTestDB(t)

	now := time.Now().UTC().Truncate(time.Second)
	issueTime := makeTimestamp(now)
	issueID := int64(50000)
	issueNumber := 50
	issueTitle := "REST issue"
	issueState := "open"
	issueURL := "https://github.com/owner/repo/issues/50"
	issueLogin := "grace"

	ghIssue := &gh.Issue{
		ID:        &issueID,
		Number:    &issueNumber,
		Title:     &issueTitle,
		State:     &issueState,
		HTMLURL:   &issueURL,
		User:      &gh.User{Login: &issueLogin},
		CreatedAt: issueTime,
		UpdatedAt: issueTime,
	}

	mock := &mockClient{
		listOpenPRsErr: notModifiedErr(),
		openIssues:     []*gh.Issue{ghIssue},
		getIssueFn: func(_ context.Context, _, _ string, _ int) (*gh.Issue, error) {
			return ghIssue, nil
		},
	}

	syncer := NewSyncer(
		map[string]Client{"github.com": mock},
		d, nil,
		[]RepoRef{{Owner: "owner", Name: "repo", PlatformHost: "github.com"}},
		time.Minute, nil, testBudget(1000),
	)

	// Configure a GraphQL fetcher that returns errors. The HTTP server
	// responds with a GraphQL error, so FetchRepoIssues fails and the
	// sync engine falls back to REST using the already-fetched issue list.
	errSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"errors":[{"message":"server error"}]}`))
	}))
	defer errSrv.Close()
	gqlClient := githubv4.NewEnterpriseClient(errSrv.URL, errSrv.Client())
	syncer.SetFetchers(map[string]*GraphQLFetcher{
		"github.com": {client: gqlClient},
	})

	syncer.RunOnce(ctx)

	issue, err := d.GetIssue(ctx, "owner", "repo", 50)
	require.NoError(t, err)
	require.NotNil(t, issue)
	assert.Equal("REST issue", issue.Title)
	assert.Equal("grace", issue.Author)
}

// TestSyncRepoGraphQLIssuesFullFlow exercises the full GraphQL issue
// sync path end-to-end: real GraphQLFetcher with a real HTTP backend
// returning canned JSON, through JSON parsing → gqlIssue adapter →
// NormalizeIssue → UpsertIssue. Validates that struct tags, adapter
// mapping, and the full data flow work together.
func TestSyncRepoGraphQLIssuesFullFlow(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)
	ctx := t.Context()
	d := openTestDB(t)

	now := time.Now().UTC().Truncate(time.Second).Format(time.RFC3339)

	// GraphQL server responds with canned issue data. The request
	// body distinguishes PR queries from issue queries; respond with
	// empty PRs and a single issue.
	gqlSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(200)
		if bytes.Contains(body, []byte("pullRequests")) {
			_, _ = w.Write([]byte(`{"data":{"repository":{"pullRequests":{"nodes":[],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}`))
			return
		}
		resp := `{"data":{"repository":{"issues":{"nodes":[{
			"databaseId":70000,
			"number":70,
			"title":"Full flow issue",
			"state":"OPEN",
			"body":"End to end test",
			"url":"https://github.com/owner/repo/issues/70",
			"author":{"login":"heidi"},
			"createdAt":"` + now + `",
			"updatedAt":"` + now + `",
			"closedAt":null,
			"labels":{"nodes":[{"name":"bug","color":"d73a4a","description":"","isDefault":false}]},
			"comments":{"totalCount":1,"nodes":[{"databaseId":701,"author":{"login":"commenter"},"body":"Full flow comment","createdAt":"` + now + `","updatedAt":"` + now + `"}],"pageInfo":{"hasNextPage":false,"endCursor":""}}
		}],"pageInfo":{"hasNextPage":false,"endCursor":""}}}}}`
		_, _ = w.Write([]byte(resp))
	}))
	defer gqlSrv.Close()

	// REST mock: returns the same issue in list (for ETag gate pass),
	// and also lists PRs as 304 to focus on issues.
	issueID := int64(70000)
	issueNumber := 70
	issueTitle := "Full flow issue"
	issueState := "open"
	issueURL := "https://github.com/owner/repo/issues/70"
	issueLogin := "heidi"
	issueTime := gh.Timestamp{Time: time.Now().UTC().Truncate(time.Second)}
	ghIssue := &gh.Issue{
		ID:        &issueID,
		Number:    &issueNumber,
		Title:     &issueTitle,
		State:     &issueState,
		HTMLURL:   &issueURL,
		User:      &gh.User{Login: &issueLogin},
		CreatedAt: &issueTime,
		UpdatedAt: &issueTime,
	}
	mock := &mockClient{
		listOpenPRsErr: notModifiedErr(),
		openIssues:     []*gh.Issue{ghIssue},
	}

	syncer := NewSyncer(
		map[string]Client{"github.com": mock},
		d, nil,
		[]RepoRef{{Owner: "owner", Name: "repo", PlatformHost: "github.com"}},
		time.Minute, nil, testBudget(1000),
	)

	gqlClient := githubv4.NewEnterpriseClient(gqlSrv.URL, gqlSrv.Client())
	syncer.SetFetchers(map[string]*GraphQLFetcher{
		"github.com": {client: gqlClient},
	})

	syncer.RunOnce(ctx)

	// Verify issue persisted with GraphQL data.
	issue, err := d.GetIssue(ctx, "owner", "repo", 70)
	require.NoError(err)
	require.NotNil(issue)
	assert.Equal("Full flow issue", issue.Title)
	assert.Equal("heidi", issue.Author)
	assert.Equal("open", issue.State)
	assert.Equal("End to end test", issue.Body)
	assert.Equal(1, issue.CommentCount)
	assert.NotNil(issue.DetailFetchedAt)

	// Labels persisted from GraphQL.
	require.Len(issue.Labels, 1)
	assert.Equal("bug", issue.Labels[0].Name)

	// Comment events persisted from GraphQL bulk (no REST fallback).
	events, err := d.ListIssueEvents(ctx, issue.ID)
	require.NoError(err)
	require.Len(events, 1)
	assert.Equal("Full flow comment", events[0].Body)
	assert.Equal("commenter", events[0].Author)

	// GraphQL path skipped REST ListIssueComments.
	assert.Equal(int32(0), mock.listIssueCommentsCalled.Load())
}

// TestComputePRCommentRefreshLastActivity_PreservesNonCommentEvents
// guards the comment-only refresh path against regressing a PR whose
// latest activity came from a review, commit, or force push — data
// the comment list can't see. The non-comment timestamp is supplied
// by the caller from the DB's stored events.
func TestComputePRCommentRefreshLastActivity_PreservesNonCommentEvents(t *testing.T) {
	assert := Assert.New(t)

	created := time.Date(2026, 4, 20, 10, 0, 0, 0, time.UTC)
	updated := created.Add(1 * time.Hour)
	commentAt := created.Add(2 * time.Hour)
	reviewAt := created.Add(3 * time.Hour)

	pr := &db.MergeRequest{CreatedAt: created, UpdatedAt: updated}
	comments := []*gh.IssueComment{{
		CreatedAt: &gh.Timestamp{Time: commentAt},
		UpdatedAt: &gh.Timestamp{Time: commentAt},
	}}

	assert.Equal(reviewAt, computePRCommentRefreshLastActivity(pr, comments, reviewAt),
		"stored non-comment event activity must win over comment timestamp")

	newerComment := reviewAt.Add(30 * time.Minute)
	comments[0].UpdatedAt = &gh.Timestamp{Time: newerComment}
	assert.Equal(newerComment, computePRCommentRefreshLastActivity(pr, comments, reviewAt),
		"a strictly newer comment should advance activity past stored events")

	assert.Equal(updated, computePRCommentRefreshLastActivity(pr, nil, time.Time{}),
		"no comments and no stored events should fall back to PR UpdatedAt")
}

func TestRefreshRepoPRCommentsUsesFullFetchForLargeThreads(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)
	ctx := t.Context()
	d := openTestDB(t)

	repoID, err := d.UpsertRepo(ctx, db.GitHubRepoIdentity("github.com", "owner", "repo"))
	require.NoError(err)

	now := time.Now().UTC().Truncate(time.Second)
	detailFetchedAt := now.Add(time.Minute)
	_, err = d.UpsertMergeRequest(ctx, &db.MergeRequest{
		RepoID:          repoID,
		PlatformID:      101,
		Number:          1,
		URL:             "https://github.com/owner/repo/pull/1",
		Title:           "Large thread",
		Author:          "alice",
		State:           "open",
		CommentCount:    100,
		CreatedAt:       now,
		UpdatedAt:       now,
		LastActivityAt:  now,
		DetailFetchedAt: &detailFetchedAt,
	})
	require.NoError(err)

	mock := &mockClient{
		listIssueCommentsFn: func(_ context.Context, _, _ string, number int) ([]*gh.IssueComment, error) {
			require.Equal(1, number)
			return []*gh.IssueComment{}, nil
		},
		listIssueCommentsIfChangedFn: func(_ context.Context, _, _ string, _ int) ([]*gh.IssueComment, error) {
			require.FailNow("conditional comment refresh should not be used for 100+ comment PRs")
			return nil, nil
		},
	}

	syncer := NewSyncer(
		map[string]Client{"github.com": mock},
		d, nil,
		[]RepoRef{{Owner: "owner", Name: "repo", PlatformHost: "github.com"}},
		time.Minute, nil, nil,
	)

	syncer.refreshRepoPRComments(ctx, RepoRef{Owner: "owner", Name: "repo", PlatformHost: "github.com"})

	assert.Equal(int32(1), mock.listIssueCommentsCalled.Load())
	assert.Equal(int32(0), mock.listIssueCommentsIfChangedCalls.Load())
}

func TestRefreshRepoIssueCommentsUsesFullFetchForLargeThreads(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)
	ctx := t.Context()
	d := openTestDB(t)

	repoID, err := d.UpsertRepo(ctx, db.GitHubRepoIdentity("github.com", "owner", "repo"))
	require.NoError(err)

	now := time.Now().UTC().Truncate(time.Second)
	detailFetchedAt := now.Add(time.Minute)
	_, err = d.UpsertIssue(ctx, &db.Issue{
		RepoID:          repoID,
		PlatformID:      201,
		Number:          2,
		URL:             "https://github.com/owner/repo/issues/2",
		Title:           "Large thread issue",
		Author:          "bob",
		State:           "open",
		CommentCount:    100,
		CreatedAt:       now,
		UpdatedAt:       now,
		LastActivityAt:  now,
		DetailFetchedAt: &detailFetchedAt,
	})
	require.NoError(err)

	mock := &mockClient{
		listIssueCommentsFn: func(_ context.Context, _, _ string, number int) ([]*gh.IssueComment, error) {
			require.Equal(2, number)
			return []*gh.IssueComment{}, nil
		},
		listIssueCommentsIfChangedFn: func(_ context.Context, _, _ string, _ int) ([]*gh.IssueComment, error) {
			require.FailNow("conditional comment refresh should not be used for 100+ comment issues")
			return nil, nil
		},
	}

	syncer := NewSyncer(
		map[string]Client{"github.com": mock},
		d, nil,
		[]RepoRef{{Owner: "owner", Name: "repo", PlatformHost: "github.com"}},
		time.Minute, nil, nil,
	)

	syncer.refreshRepoIssueComments(ctx, RepoRef{Owner: "owner", Name: "repo", PlatformHost: "github.com"})

	assert.Equal(int32(1), mock.listIssueCommentsCalled.Load())
	assert.Equal(int32(0), mock.listIssueCommentsIfChangedCalls.Load())
}

func TestDrainPendingCommentSyncsReadsQueuedItemsByProviderIdentity(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	d := openTestDB(t)
	now := time.Date(2026, 4, 20, 12, 0, 0, 0, time.UTC)
	detailFetchedAt := now.Add(-time.Minute)

	codeRepo := RepoRef{
		Platform:     platform.KindGitHub,
		PlatformHost: "code.example.com",
		Owner:        "acme",
		Name:         "widget",
	}
	githubRepo := RepoRef{
		Platform:     platform.KindGitHub,
		PlatformHost: "github.com",
		Owner:        "acme",
		Name:         "widget",
	}
	codeRepoID, err := d.UpsertRepo(ctx, platform.DBRepoIdentity(platformRepoRef(codeRepo)))
	require.NoError(err)
	githubRepoID, err := d.UpsertRepo(ctx, platform.DBRepoIdentity(platformRepoRef(githubRepo)))
	require.NoError(err)

	codeMRID, err := d.UpsertMergeRequest(ctx, &db.MergeRequest{
		RepoID:          codeRepoID,
		PlatformID:      1001,
		Number:          7,
		URL:             "https://code.example.com/acme/widget/pull/7",
		Title:           "code host MR",
		Author:          "ada",
		State:           "open",
		CommentCount:    1,
		CreatedAt:       now,
		UpdatedAt:       now,
		LastActivityAt:  now,
		DetailFetchedAt: &detailFetchedAt,
	})
	require.NoError(err)
	githubMRID, err := d.UpsertMergeRequest(ctx, &db.MergeRequest{
		RepoID:          githubRepoID,
		PlatformID:      7001,
		Number:          7,
		URL:             "https://github.com/acme/widget/pull/7",
		Title:           "github.com MR",
		Author:          "ada",
		State:           "open",
		CommentCount:    1,
		CreatedAt:       now,
		UpdatedAt:       now,
		LastActivityAt:  now,
		DetailFetchedAt: &detailFetchedAt,
	})
	require.NoError(err)
	_, err = d.UpsertIssue(ctx, &db.Issue{
		RepoID:          codeRepoID,
		PlatformID:      1002,
		Number:          8,
		URL:             "https://code.example.com/acme/widget/issues/8",
		Title:           "code host issue",
		Author:          "ada",
		State:           "open",
		CommentCount:    1,
		CreatedAt:       now,
		UpdatedAt:       now,
		LastActivityAt:  now,
		DetailFetchedAt: &detailFetchedAt,
	})
	require.NoError(err)
	githubIssueID, err := d.UpsertIssue(ctx, &db.Issue{
		RepoID:          githubRepoID,
		PlatformID:      7002,
		Number:          8,
		URL:             "https://github.com/acme/widget/issues/8",
		Title:           "github.com issue",
		Author:          "ada",
		State:           "open",
		CommentCount:    1,
		CreatedAt:       now,
		UpdatedAt:       now,
		LastActivityAt:  now,
		DetailFetchedAt: &detailFetchedAt,
	})
	require.NoError(err)

	commentBody := "code host refreshed"
	commentID := int64(501)
	mock := &mockClient{
		listIssueCommentsIfChangedFn: func(_ context.Context, _, _ string, number int) ([]*gh.IssueComment, error) {
			require.Contains([]int{7, 8}, number)
			return []*gh.IssueComment{{
				ID:        &commentID,
				Body:      &commentBody,
				User:      &gh.User{Login: new("reviewer")},
				CreatedAt: makeTimestamp(now),
				UpdatedAt: makeTimestamp(now),
			}}, nil
		},
	}
	syncer := NewSyncer(
		map[string]Client{"code.example.com": mock},
		d, nil,
		[]RepoRef{codeRepo, githubRepo},
		time.Minute, nil, nil,
	)
	syncer.queuePRCommentSync(codeRepo, 7)
	syncer.queueIssueCommentSync(codeRepo, 8)

	syncer.drainPendingCommentSyncs(ctx, map[string]bool{"code.example.com": true})

	codeMREvents, err := d.ListMREvents(ctx, codeMRID)
	require.NoError(err)
	require.Len(codeMREvents, 1)
	assert.Equal(commentBody, codeMREvents[0].Body)
	githubMREvents, err := d.ListMREvents(ctx, githubMRID)
	require.NoError(err)
	assert.Empty(githubMREvents)

	codeIssue, err := d.GetIssueByRepoIDAndNumber(ctx, codeRepoID, 8)
	require.NoError(err)
	require.NotNil(codeIssue)
	codeIssueEvents, err := d.ListIssueEvents(ctx, codeIssue.ID)
	require.NoError(err)
	require.Len(codeIssueEvents, 1)
	assert.Equal(commentBody, codeIssueEvents[0].Body)
	githubIssueEvents, err := d.ListIssueEvents(ctx, githubIssueID)
	require.NoError(err)
	assert.Empty(githubIssueEvents)
}

func TestRefreshRepoCommentsFiltersByHost(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	d := openTestDB(t)
	now := time.Date(2026, 4, 20, 12, 0, 0, 0, time.UTC)
	detailFetchedAt := now.Add(-time.Minute)

	codeRepo := RepoRef{
		Platform:     platform.KindGitHub,
		PlatformHost: "code.example.com",
		Owner:        "acme",
		Name:         "widget",
	}
	githubRepo := RepoRef{
		Platform:     platform.KindGitHub,
		PlatformHost: "github.com",
		Owner:        "acme",
		Name:         "widget",
	}
	codeRepoID, err := d.UpsertRepo(ctx, platform.DBRepoIdentity(platformRepoRef(codeRepo)))
	require.NoError(err)
	githubRepoID, err := d.UpsertRepo(ctx, platform.DBRepoIdentity(platformRepoRef(githubRepo)))
	require.NoError(err)

	codeMRID, err := d.UpsertMergeRequest(ctx, &db.MergeRequest{
		RepoID:          codeRepoID,
		PlatformID:      1001,
		Number:          7,
		URL:             "https://code.example.com/acme/widget/pull/7",
		Title:           "code host MR",
		Author:          "ada",
		State:           "open",
		CommentCount:    1,
		CreatedAt:       now,
		UpdatedAt:       now,
		LastActivityAt:  now,
		DetailFetchedAt: &detailFetchedAt,
	})
	require.NoError(err)
	githubMRID, err := d.UpsertMergeRequest(ctx, &db.MergeRequest{
		RepoID:          githubRepoID,
		PlatformID:      7001,
		Number:          7,
		URL:             "https://github.com/acme/widget/pull/7",
		Title:           "github.com MR",
		Author:          "ada",
		State:           "open",
		CommentCount:    1,
		CreatedAt:       now,
		UpdatedAt:       now,
		LastActivityAt:  now,
		DetailFetchedAt: &detailFetchedAt,
	})
	require.NoError(err)
	codeIssueID, err := d.UpsertIssue(ctx, &db.Issue{
		RepoID:          codeRepoID,
		PlatformID:      1002,
		Number:          8,
		URL:             "https://code.example.com/acme/widget/issues/8",
		Title:           "code host issue",
		Author:          "ada",
		State:           "open",
		CommentCount:    1,
		CreatedAt:       now,
		UpdatedAt:       now,
		LastActivityAt:  now,
		DetailFetchedAt: &detailFetchedAt,
	})
	require.NoError(err)
	githubIssueID, err := d.UpsertIssue(ctx, &db.Issue{
		RepoID:          githubRepoID,
		PlatformID:      7002,
		Number:          8,
		URL:             "https://github.com/acme/widget/issues/8",
		Title:           "github.com issue",
		Author:          "ada",
		State:           "open",
		CommentCount:    1,
		CreatedAt:       now,
		UpdatedAt:       now,
		LastActivityAt:  now,
		DetailFetchedAt: &detailFetchedAt,
	})
	require.NoError(err)

	commentBody := "code host refreshed"
	commentID := int64(501)
	mock := &mockClient{
		listIssueCommentsIfChangedFn: func(_ context.Context, _, _ string, number int) ([]*gh.IssueComment, error) {
			require.Contains([]int{7, 8}, number)
			return []*gh.IssueComment{{
				ID:        &commentID,
				Body:      &commentBody,
				User:      &gh.User{Login: new("reviewer")},
				CreatedAt: makeTimestamp(now),
				UpdatedAt: makeTimestamp(now),
			}}, nil
		},
	}
	syncer := NewSyncer(
		map[string]Client{"code.example.com": mock},
		d, nil,
		[]RepoRef{codeRepo, githubRepo},
		time.Minute, nil, nil,
	)

	syncer.refreshRepoPRComments(ctx, codeRepo)
	syncer.refreshRepoIssueComments(ctx, codeRepo)

	codeMREvents, err := d.ListMREvents(ctx, codeMRID)
	require.NoError(err)
	require.Len(codeMREvents, 1)
	assert.Equal(commentBody, codeMREvents[0].Body)
	githubMREvents, err := d.ListMREvents(ctx, githubMRID)
	require.NoError(err)
	assert.Empty(githubMREvents)

	codeIssueEvents, err := d.ListIssueEvents(ctx, codeIssueID)
	require.NoError(err)
	require.Len(codeIssueEvents, 1)
	assert.Equal(commentBody, codeIssueEvents[0].Body)
	githubIssueEvents, err := d.ListIssueEvents(ctx, githubIssueID)
	require.NoError(err)
	assert.Empty(githubIssueEvents)
}

func TestDeferredCommentRefreshYieldsBudgetToDetailDrain(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)
	ctx := t.Context()
	d := openTestDB(t)

	now := time.Date(2026, 4, 20, 12, 0, 0, 0, time.UTC)
	budget := testBudget(12)
	repoID, err := d.UpsertRepo(ctx, db.GitHubRepoIdentity("github.com", "owner", "repo"))
	require.NoError(err)
	repo := RepoRef{Owner: "owner", Name: "repo", PlatformHost: "github.com"}

	pr1UpdatedAt := now.Add(-10 * time.Minute)
	detailFetchedAt := now.Add(-5 * time.Minute)
	_, err = d.UpsertMergeRequest(ctx, &db.MergeRequest{
		RepoID:          repoID,
		PlatformID:      1001,
		Number:          1,
		URL:             "https://github.com/owner/repo/pull/1",
		Title:           "Large unchanged thread",
		Author:          "alice",
		State:           "open",
		CommentCount:    100,
		HeadBranch:      "feature/large-thread",
		BaseBranch:      "main",
		PlatformHeadSHA: "1111111",
		PlatformBaseSHA: "aaaaaaa",
		CreatedAt:       pr1UpdatedAt,
		UpdatedAt:       pr1UpdatedAt,
		LastActivityAt:  pr1UpdatedAt,
		DetailFetchedAt: &detailFetchedAt,
	})
	require.NoError(err)
	pr2ID, err := d.UpsertMergeRequest(ctx, &db.MergeRequest{
		RepoID:          repoID,
		PlatformID:      1002,
		Number:          2,
		URL:             "https://github.com/owner/repo/pull/2",
		Title:           "Needs detail drain",
		Author:          "alice",
		State:           "open",
		CommentCount:    0,
		HeadBranch:      "feature/detail-drain",
		BaseBranch:      "main",
		PlatformHeadSHA: "2222222",
		PlatformBaseSHA: "aaaaaaa",
		CreatedAt:       now,
		UpdatedAt:       now,
		LastActivityAt:  now,
	})
	require.NoError(err)
	require.NoError(d.EnsureKanbanState(ctx, pr2ID))

	var commentCalls []int
	mc := &mockClient{
		budget: budget["github.com"],
		getPullRequestFn: func(_ context.Context, _, _ string, number int) (*gh.PullRequest, error) {
			require.Equal(2, number)
			return &gh.PullRequest{
				ID:        new(int64(1002)),
				Number:    new(2),
				Title:     new("Needs detail drain"),
				State:     new("open"),
				HTMLURL:   new("https://github.com/owner/repo/pull/2"),
				User:      &gh.User{Login: new("alice")},
				CreatedAt: &gh.Timestamp{Time: now},
				UpdatedAt: &gh.Timestamp{Time: now},
				Head:      &gh.PullRequestBranch{Ref: new("feature/detail-drain"), SHA: new("2222222")},
				Base:      &gh.PullRequestBranch{Ref: new("main"), SHA: new("aaaaaaa")},
			}, nil
		},
		listIssueCommentsFn: func(_ context.Context, _, _ string, number int) ([]*gh.IssueComment, error) {
			commentCalls = append(commentCalls, number)
			return []*gh.IssueComment{}, nil
		},
		reviews:  []*gh.PullRequestReview{},
		commits:  []*gh.RepositoryCommit{},
		ciStatus: &gh.CombinedStatus{State: new("success")},
	}

	syncer := NewSyncer(
		map[string]Client{"github.com": mc},
		d, nil,
		[]RepoRef{repo},
		time.Minute, nil, budget,
	)

	syncer.queuePRCommentSync(repo, 1)
	budget["github.com"].Spend(3)
	syncer.drainDetailQueue(ctx, map[string]bool{"github.com": true})
	syncer.drainPendingCommentSyncs(ctx, map[string]bool{"github.com": true})

	pr, err := d.GetMergeRequest(ctx, "owner", "repo", 2)
	require.NoError(err)
	require.NotNil(pr)
	assert.NotNil(pr.DetailFetchedAt,
		"detail drain should win before unchanged large-thread refresh")
	assert.Equal([]int{2}, commentCalls,
		"only the detail-drain PR should spend the remaining budget")
}

func TestSyncerGQLRateTrackers(t *testing.T) {
	assert := Assert.New(t)
	d := openTestDB(t)

	rt := NewRateTracker(d, "github.com", "rest")
	gqlRT := NewRateTracker(d, "github.com", "graphql")

	syncer := NewSyncer(
		map[string]Client{"github.com": &mockClient{}},
		d, nil,
		[]RepoRef{{Owner: "acme", Name: "widget", PlatformHost: "github.com"}},
		time.Minute,
		map[string]*RateTracker{"github.com": rt},
		nil,
	)

	fetcher := NewGraphQLFetcher("token", "github.com", gqlRT, nil)
	syncer.SetFetchers(map[string]*GraphQLFetcher{"github.com": fetcher})

	gqlTrackers := syncer.GQLRateTrackers()
	assert.Len(gqlTrackers, 1)
	assert.Same(gqlRT, gqlTrackers["github.com"])
}

func TestSyncerGQLRateTrackersSkipsNil(t *testing.T) {
	assert := Assert.New(t)
	d := openTestDB(t)

	syncer := NewSyncer(
		map[string]Client{"github.com": &mockClient{}},
		d, nil,
		[]RepoRef{{Owner: "acme", Name: "widget", PlatformHost: "github.com"}},
		time.Minute,
		nil, nil,
	)

	// Nil fetcher entry and a fetcher with no tracker both skipped.
	syncer.SetFetchers(map[string]*GraphQLFetcher{
		"github.com":           nil,
		"ghe.corp.example.com": NewGraphQLFetcher("tok", "ghe.corp.example.com", nil, nil),
	})

	assert.Empty(syncer.GQLRateTrackers())
}

func TestSyncerGQLRateTrackersMixed(t *testing.T) {
	assert := Assert.New(t)
	d := openTestDB(t)

	validRT := NewRateTracker(d, "github.com", "graphql")

	syncer := NewSyncer(
		map[string]Client{"github.com": &mockClient{}},
		d, nil,
		[]RepoRef{{Owner: "acme", Name: "widget", PlatformHost: "github.com"}},
		time.Minute,
		nil, nil,
	)

	// Mix of nil fetcher, fetcher-without-tracker, and valid fetcher.
	syncer.SetFetchers(map[string]*GraphQLFetcher{
		"nil.example.com":        nil,
		"no-tracker.example.com": NewGraphQLFetcher("tok", "no-tracker.example.com", nil, nil),
		"github.com":             NewGraphQLFetcher("tok", "github.com", validRT, nil),
	})

	got := syncer.GQLRateTrackers()
	assert.Len(got, 1)
	assert.Same(validRT, got["github.com"])
}

// TestDisplayNameCacheSurvivesRunOnce verifies the key
// behavioral change: the cache persists across RunOnce
// invocations instead of being reset. With the old per-run
// map, the second RunOnce would re-fetch every author. With
// the TTL cache, the second RunOnce sees a fresh cache hit
// and makes zero /users calls.
func TestDisplayNameCacheSurvivesRunOnce(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()

	now := time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC)
	prNumber := 1
	prTitle := "test"
	prState := "open"
	prURL := "https://github.com/owner/repo/pull/1"
	prBody := ""
	prAuthor := "alice"
	prDisplayName := "Alice Smith"

	getUserCalls := 0
	mc := &mockClient{
		openPRs: []*gh.PullRequest{buildOpenPR(prNumber, now)},
		getUserFn: func(_ context.Context, login string) (*gh.User, error) {
			getUserCalls++
			return &gh.User{Login: &login, Name: &prDisplayName}, nil
		},
	}
	// Patch the open PR to have the author we care about.
	mc.openPRs[0].User = &gh.User{Login: &prAuthor}
	mc.openPRs[0].Title = &prTitle
	mc.openPRs[0].State = &prState
	mc.openPRs[0].HTMLURL = &prURL
	mc.openPRs[0].Body = &prBody

	syncer := NewSyncer(
		map[string]Client{"github.com": mc}, d, nil,
		[]RepoRef{{Owner: "owner", Name: "repo", PlatformHost: "github.com"}},
		time.Minute, nil, nil,
	)

	// First RunOnce: resolves display name for "alice".
	syncer.RunOnce(ctx)
	firstRunCalls := getUserCalls
	assert.Positive(firstRunCalls,
		"first RunOnce should have fetched the display name")

	// Verify the display name landed in SQLite.
	mr, err := d.GetMergeRequest(ctx, "owner", "repo", prNumber)
	require.NoError(err)
	require.NotNil(mr)
	assert.Equal("Alice Smith", mr.AuthorDisplayName,
		"AuthorDisplayName must be persisted to SQLite after first sync")

	// Second RunOnce: cache hit, no new GetUser calls.
	syncer.RunOnce(ctx)
	assert.Equal(firstRunCalls, getUserCalls,
		"second RunOnce must not re-fetch cached display names")

	// DB still has the name after the cache-hit sync pass.
	mr2, err := d.GetMergeRequest(ctx, "owner", "repo", prNumber)
	require.NoError(err)
	require.NotNil(mr2)
	assert.Equal("Alice Smith", mr2.AuthorDisplayName,
		"AuthorDisplayName must survive a cache-hit sync pass")
}

// TestResolveDisplayName_StaleWhileErrorBacksOff verifies the
// behavior when a successful cache entry has expired and the
// refresh call keeps failing:
//
//  1. Stale name is returned instead of "" (stale-while-error).
//  2. Follow-up calls within failureTTL do NOT hit the API — the
//     expiry is rewritten to failureTTL so retries back off.
//  3. After failureTTL elapses, one retry fires again.
//
// Without the backoff step 2, every subsequent sync would hit
// /users while the outage persists, defeating the cache.
func TestResolveDisplayName_StaleWhileErrorBacksOff(t *testing.T) {
	assert := Assert.New(t)
	ctx := t.Context()

	callCount := 0
	shouldFail := false
	mc := &mockClient{
		getUserFn: func(_ context.Context, login string) (*gh.User, error) {
			callCount++
			if shouldFail {
				return nil, fmt.Errorf("upstream outage")
			}
			name := "Alice Smith"
			return &gh.User{Login: &login, Name: &name}, nil
		},
	}
	syncer := NewSyncer(
		map[string]Client{"github.com": mc}, nil, nil, nil,
		time.Minute, nil, nil,
	)

	// Inject a fake clock into the cache so we can expire
	// entries without waiting 24 hours.
	fakeNow := time.Unix(1_700_000_000, 0)
	syncer.displayNames.now = func() time.Time { return fakeNow }

	// Warm the cache with a successful lookup.
	name, ok := syncer.resolveDisplayName(ctx, mc, "github.com", "alice")
	assert.Equal("Alice Smith", name)
	assert.True(ok)
	assert.Equal(1, callCount)

	// Flip upstream to failing and expire the successful entry.
	shouldFail = true
	fakeNow = fakeNow.Add(displayNameSuccessTTL + time.Second)

	// First refresh: API hit fails, stale name is returned.
	name, ok = syncer.resolveDisplayName(ctx, mc, "github.com", "alice")
	assert.Equal("Alice Smith", name,
		"stale name must be returned on refresh failure")
	assert.True(ok)
	assert.Equal(2, callCount, "refresh should hit the API once")

	// Second refresh inside failureTTL: no API call, still
	// serves stale name.
	fakeNow = fakeNow.Add(displayNameFailureTTL / 2)
	name, ok = syncer.resolveDisplayName(ctx, mc, "github.com", "alice")
	assert.Equal("Alice Smith", name)
	assert.True(ok)
	assert.Equal(2, callCount,
		"retries within failureTTL must reuse the cached stale entry",
	)

	// Past failureTTL: one more API attempt is allowed.
	fakeNow = fakeNow.Add(displayNameFailureTTL + time.Second)
	name, ok = syncer.resolveDisplayName(ctx, mc, "github.com", "alice")
	assert.Equal("Alice Smith", name)
	assert.True(ok)
	assert.Equal(3, callCount,
		"a retry should fire once failureTTL has elapsed",
	)

	// Recovered upstream: next call refreshes successfully.
	shouldFail = false
	fakeNow = fakeNow.Add(displayNameFailureTTL + time.Second)
	name, ok = syncer.resolveDisplayName(ctx, mc, "github.com", "alice")
	assert.Equal("Alice Smith", name)
	assert.True(ok)
	assert.Equal(4, callCount)
}
