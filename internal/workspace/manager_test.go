package workspace

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	Assert "github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	gitcmd "go.kenn.io/kit/git/cmd"
	"go.kenn.io/middleman/internal/db"
	"go.kenn.io/middleman/internal/gitclone"
	"go.kenn.io/middleman/internal/procutil"
	"go.kenn.io/middleman/internal/ptyowner"
	"go.kenn.io/middleman/internal/testutil/dbtest"
)

func openTestDB(t *testing.T) *db.DB {
	t.Helper()
	return dbtest.Open(t)
}

// staticBaseResolver returns a resolver that always reports path as the
// configured local worktree base.
func staticBaseResolver(path string) WorktreeBasePathResolver {
	return func(
		context.Context, string, string, string, string,
	) (string, bool, error) {
		return path, true, nil
	}
}

func seedRepo(
	t *testing.T, d *db.DB,
	host, owner, name string,
) int64 {
	t.Helper()
	id, err := d.UpsertRepo(
		t.Context(), db.GitHubRepoIdentity(host, owner, name),
	)
	require.NoError(t, err)
	return id
}

func seedMR(
	t *testing.T, d *db.DB,
	repoID int64, number int, headBranch string,
) {
	t.Helper()
	now := time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC)
	mr := &db.MergeRequest{
		RepoID:         repoID,
		PlatformID:     repoID*10000 + int64(number),
		Number:         number,
		Title:          "Test PR",
		Author:         "author",
		State:          "open",
		HeadBranch:     headBranch,
		BaseBranch:     "main",
		CreatedAt:      now,
		UpdatedAt:      now,
		LastActivityAt: now,
	}
	_, err := d.UpsertMergeRequest(t.Context(), mr)
	require.NoError(t, err)
}

func seedMRWithHeadRepo(
	t *testing.T, d *db.DB,
	repoID int64, number int,
	headBranch, cloneURL string,
) {
	t.Helper()
	now := time.Date(2024, 6, 1, 12, 0, 0, 0, time.UTC)
	mr := &db.MergeRequest{
		RepoID:           repoID,
		PlatformID:       repoID*10000 + int64(number),
		Number:           number,
		Title:            "PR with head repo",
		Author:           "contributor",
		State:            "open",
		HeadBranch:       headBranch,
		BaseBranch:       "main",
		HeadRepoCloneURL: cloneURL,
		CreatedAt:        now,
		UpdatedAt:        now,
		LastActivityAt:   now,
	}
	_, err := d.UpsertMergeRequest(t.Context(), mr)
	require.NoError(t, err)
}

func recordRuntimeTmuxSessionForTest(
	t *testing.T,
	d *db.DB,
	workspaceID string,
	sessionKey string,
	targetKey string,
	tmuxSession string,
	createdAt time.Time,
) {
	t.Helper()
	require.NoError(t, d.UpsertWorkspaceRuntimeSession(
		t.Context(),
		&db.WorkspaceRuntimeSession{
			WorkspaceID: workspaceID,
			SessionKey:  sessionKey,
			TargetKey:   targetKey,
			Label:       targetKey,
			Kind:        "agent",
			Scope:       "session",
			TmuxSession: tmuxSession,
			CreatedAt:   createdAt,
		},
	))
}

func TestCreate(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	wtDir := t.TempDir()

	repoID := seedRepo(
		t, d, "github.com", "acme", "widget",
	)
	seedMR(t, d, repoID, 42, "feature/thing")

	mgr := NewManager(d, wtDir)

	ws, err := mgr.Create(
		ctx, "github", "github.com", "acme", "widget", 42,
	)
	require.NoError(err)
	require.NotNil(ws)

	assert.NotEmpty(ws.ID)
	assert.Len(ws.ID, 16) // 8 bytes hex-encoded
	assert.Equal("creating", ws.Status)
	assert.Equal("github.com", ws.PlatformHost)
	assert.Equal("acme", ws.RepoOwner)
	assert.Equal("widget", ws.RepoName)
	assert.Equal(db.WorkspaceItemTypePullRequest, ws.ItemType)
	assert.Equal(42, ws.ItemNumber)
	assert.Equal("feature/thing", ws.GitHeadRef)
	assert.Nil(ws.MRHeadRepo)
	assert.Contains(ws.WorktreePath, "pr-42")
	assert.Equal("middleman-"+ws.ID, ws.TmuxSession)

	// Verify persisted in DB.
	got, err := d.GetWorkspace(ctx, ws.ID)
	require.NoError(err)
	require.NotNil(got)
	assert.Equal(ws.ID, got.ID)
	assert.Equal("creating", got.Status)
}

func TestListSummariesUsesCacheWhenStoreHasNoRows(t *testing.T) {
	t.Parallel()

	require := require.New(t)
	assert := Assert.New(t)
	ctx := t.Context()
	d := openTestDB(t)
	mgr := NewManager(d, t.TempDir())

	mgr.setWorkspaceSummaryCache([]WorkspaceSummary{{
		Workspace: Workspace{
			ID:           "cached-workspace",
			PlatformHost: "github.com",
			RepoOwner:    "acme",
			RepoName:     "widget",
			ItemType:     db.WorkspaceItemTypePullRequest,
			ItemNumber:   7,
			GitHeadRef:   "feature/cache-workspace",
			Status:       "ready",
			CreatedAt:    time.Now().UTC(),
		},
	}})
	got, err := mgr.ListSummaries(ctx)
	require.NoError(err)
	require.Len(got, 1)
	assert.Equal("cached-workspace", got[0].ID)
}

func TestWorkspaceSummaryCacheDoesNotResurrectDeletedWorkspace(t *testing.T) {
	t.Parallel()

	require := require.New(t)
	assert := Assert.New(t)
	ctx := t.Context()
	d := openTestDB(t)
	repoID := seedRepo(t, d, "github.com", "acme", "widget")
	seedMR(t, d, repoID, 7, "feature/cache-workspace")
	mgr := NewManager(d, t.TempDir())

	ws, err := mgr.Create(ctx, "github", "github.com", "acme", "widget", 7)
	require.NoError(err)
	first, err := mgr.ListSummaries(ctx)
	require.NoError(err)
	require.Len(first, 1)
	assert.Equal(ws.ID, first[0].ID)

	mgr.removeWorkspaceSummaryFromCache(ws.ID)
	mgr.setWorkspaceSummaryCache(first)
	assert.Empty(mgr.cachedWorkspaceSummaries())
}

func TestCreatePRHeadRepoClassification(t *testing.T) {
	tests := []struct {
		name           string
		platformHost   string
		number         int
		headBranch     string
		headRepoURL    string
		wantMRHeadRepo string
	}{
		{
			name:           "fork PR keeps head repo",
			number:         99,
			headBranch:     "fix/typo",
			headRepoURL:    "https://github.com/contributor/widget.git",
			wantMRHeadRepo: "https://github.com/contributor/widget.git",
		},
		{
			name:        "same-repo PR with populated head repo is not fork",
			number:      244,
			headBranch:  "feature/thing",
			headRepoURL: "git@GitHub.com:Acme/Widget.git",
		},
		{
			name:         "same-repo PR on enterprise host with port is not fork",
			platformHost: "ghe.example.com:8443",
			number:       246,
			headBranch:   "feature/enterprise",
			headRepoURL:  "https://GHE.example.com:8443/Acme/Widget.git",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert := Assert.New(t)
			d := openTestDB(t)
			platformHost := tt.platformHost
			if platformHost == "" {
				platformHost = "github.com"
			}
			repoID := seedRepo(
				t, d, platformHost, "acme", "widget",
			)
			seedMRWithHeadRepo(
				t, d, repoID, tt.number, tt.headBranch, tt.headRepoURL,
			)

			mgr := NewManager(d, t.TempDir())
			ws, err := mgr.Create(
				t.Context(), "github", platformHost, "acme", "widget", tt.number,
			)
			require.NoError(t, err)
			require.NotNil(t, ws)

			if tt.wantMRHeadRepo == "" {
				// Same-repo PRs still have head repo clone URLs in GitHub
				// payloads. Keeping MRHeadRepo nil sends workspace setup down
				// the origin/<branch> path instead of the refs/pull/<number>/head
				// path reserved for forks.
				assert.Nil(ws.MRHeadRepo)
				return
			}
			require.NotNil(t, ws.MRHeadRepo)
			assert.Equal(tt.wantMRHeadRepo, *ws.MRHeadRepo)
		})
	}
}

func TestCreateIssueDefaultBranchSluggified(t *testing.T) {
	tests := []struct {
		name      string
		title     string
		slugStyle bool
		want      string
	}{
		{
			name:      "slug style with usable title",
			title:     "Add foo to bar",
			slugStyle: true,
			want:      "middleman/issue-7-add-foo-to-bar",
		},
		{
			name:      "slug style with empty title falls back to bare",
			title:     "",
			slugStyle: true,
			want:      "middleman/issue-7",
		},
		{
			name:      "slug style with all-punctuation falls back to bare",
			title:     "?!@#",
			slugStyle: true,
			want:      "middleman/issue-7",
		},
		{
			name:      "bare style ignores title",
			title:     "Add foo to bar",
			slugStyle: false,
			want:      "middleman/issue-7",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert := Assert.New(t)
			require := require.New(t)

			d := openTestDB(t)
			ctx := t.Context()
			repoID := seedRepo(t, d, "github.com", "acme", "widget")
			seedIssue(t, d, repoID, 7, tt.title)

			mgr := NewManager(d, t.TempDir())
			mgr.SetIssueBranchSlugEnabled(tt.slugStyle)

			ws, err := mgr.CreateIssue(
				ctx, "github.com", "acme", "widget", 7,
				CreateIssueOptions{},
			)
			require.NoError(err)
			require.NotNil(ws)

			assert.Equal(tt.want, ws.GitHeadRef)
			assert.Equal(tt.want, ws.WorkspaceBranch)
		})
	}
}

func TestCreateIssueExplicitGitHeadRefBypassesSlug(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	d := openTestDB(t)
	ctx := t.Context()
	repoID := seedRepo(t, d, "github.com", "acme", "widget")
	seedIssue(t, d, repoID, 7, "Add foo to bar")

	mgr := NewManager(d, t.TempDir())

	ws, err := mgr.CreateIssue(
		ctx, "github.com", "acme", "widget", 7,
		CreateIssueOptions{GitHeadRef: "custom/branch"},
	)
	require.NoError(err)
	require.NotNil(ws)
	assert.Equal("custom/branch", ws.GitHeadRef)
}

func TestCreateIssueReuseLocalBaseBranchCheckedOutReturnsConflict(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	d := openTestDB(t)
	ctx := t.Context()
	repoID := seedRepo(t, d, "github.com", "acme", "widget")
	seedIssue(t, d, repoID, 7, "")

	const branch = "middleman/issue-7"
	localRepo := setupLocalWorktreeBaseForWorkspaceGitTest(t, "feature/thing")
	runWorkspaceTestGit(
		t, localRepo,
		"worktree", "add", filepath.Join(t.TempDir(), "existing"),
		"-b", branch, "HEAD",
	)

	mgr := NewManager(d, t.TempDir())
	mgr.SetWorktreeBasePathResolver(staticBaseResolver(localRepo))

	ws, err := mgr.CreateIssue(
		ctx, "github.com", "acme", "widget", 7,
		CreateIssueOptions{ReuseExistingBranch: true},
	)

	require.Nil(ws)
	var conflict *IssueWorkspaceBranchConflictError
	require.ErrorAs(err, &conflict)
	require.NotNil(conflict)
	assert.Equal(branch, conflict.Branch)
	assert.Equal(branch+"-2", conflict.SuggestedBranch)
}

func TestCreateRepoNotTracked(t *testing.T) {
	d := openTestDB(t)
	mgr := NewManager(d, t.TempDir())

	_, err := mgr.Create(
		t.Context(), "github", "github.com", "unknown", "repo", 1,
	)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrWorkspaceNotFound)
}

func TestCreateDuplicate(t *testing.T) {
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	wtDir := t.TempDir()

	repoID := seedRepo(
		t, d, "github.com", "acme", "widget",
	)
	seedMR(t, d, repoID, 42, "feature/thing")

	mgr := NewManager(d, wtDir)

	// First create succeeds.
	ws, err := mgr.Create(
		ctx, "github", "github.com", "acme", "widget", 42,
	)
	require.NoError(err)
	require.NotNil(ws)

	// Second create for same MR fails with unique constraint.
	_, err = mgr.Create(
		ctx, "github", "github.com", "acme", "widget", 42,
	)
	require.Error(err)
	require.ErrorIs(err, ErrWorkspaceDuplicate)
}

func TestCreateMRNotSynced(t *testing.T) {
	d := openTestDB(t)

	seedRepo(t, d, "github.com", "acme", "widget")

	mgr := NewManager(d, t.TempDir())

	_, err := mgr.Create(
		t.Context(), "github", "github.com", "acme", "widget", 999,
	)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrWorkspaceNotSynced)
}

func TestSetupFailurePersistsStatusWhenContextCanceled(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	wtDir := t.TempDir()

	repoID := seedRepo(
		t, d, "github.com", "acme", "widget",
	)
	seedMR(t, d, repoID, 42, "feature/thing")

	mgr := NewManager(d, wtDir)
	ws, err := mgr.Create(
		t.Context(), "github", "github.com", "acme", "widget", 42,
	)
	require.NoError(err)
	require.NotNil(ws)

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	err = mgr.Setup(ctx, ws)
	require.Error(err)
	require.Contains(err.Error(), "clone manager not set")

	got, err := d.GetWorkspace(t.Context(), ws.ID)
	require.NoError(err)
	require.NotNil(got)
	assert.Equal("error", got.Status)
	require.NotNil(got.ErrorMessage)
	assert.Contains(*got.ErrorMessage, "clone manager not set")

	events, err := d.ListWorkspaceSetupEvents(
		t.Context(), ws.ID,
	)
	require.NoError(err)
	require.Len(events, 2)
	assert.Equal("setup", events[0].Stage)
	assert.Equal("started", events[0].Outcome)
	assert.Equal("clone", events[1].Stage)
	assert.Equal("failure", events[1].Outcome)
	assert.Contains(events[1].Message, "clone manager not set")
}

func TestSetupUsesConfiguredWorktreeBasePath(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	wtDir := t.TempDir()

	localRepo, _, platformHost := setupHTTPWorktreeBaseForWorkspaceGitTest(
		t, "feature/thing",
	)
	repoID := seedRepo(t, d, platformHost, "acme", "widget")
	seedMR(t, d, repoID, 42, "feature/thing")

	tmuxScript, _ := writeRecorderScript(t)

	mgr := NewManager(d, wtDir)
	mgr.SetTmuxCommand([]string{tmuxScript})
	mgr.SetWorktreeBasePathResolver(staticBaseResolver(localRepo))

	ws, err := mgr.Create(t.Context(), "github", platformHost, "acme", "widget", 42)
	require.NoError(err)
	require.NoError(mgr.Setup(t.Context(), ws))

	got, err := d.GetWorkspace(t.Context(), ws.ID)
	require.NoError(err)
	require.NotNil(got)
	assert.Equal("ready", got.Status)
	assert.Equal("feature/thing", got.WorkspaceBranch)

	listOutput := string(runWorkspaceTestGit(t, localRepo, "worktree", "list", "--porcelain"))
	canonicalWorktreePath, err := filepath.EvalSymlinks(ws.WorktreePath)
	require.NoError(err)
	assert.Contains(listOutput, "worktree "+canonicalWorktreePath)

	headSHA, err := gitHeadSHA(t.Context(), ws.WorktreePath)
	require.NoError(err)
	sourceSHA := strings.TrimSpace(string(runWorkspaceTestGit(
		t, localRepo, "rev-parse", "refs/remotes/origin/feature/thing",
	)))
	assert.Equal(sourceSHA, headSHA)
}

func TestSetupReusesExistingWorkspaceWorktree(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	wtDir := t.TempDir()

	localRepo, _, platformHost := setupHTTPWorktreeBaseForWorkspaceGitTest(
		t, "feature/thing",
	)
	repoID := seedRepo(t, d, platformHost, "acme", "widget")
	seedMR(t, d, repoID, 42, "feature/thing")

	tmuxScript, tmuxRecord := writeRecorderScript(t)

	mgr := NewManager(d, wtDir)
	mgr.SetTmuxCommand([]string{tmuxScript})
	mgr.SetWorktreeBasePathResolver(staticBaseResolver(localRepo))

	ws, err := mgr.Create(t.Context(), "github", platformHost, "acme", "widget", 42)
	require.NoError(err)
	existingBranch := syntheticPRWorktreeBranch(42)
	runWorkspaceTestGit(
		t, localRepo,
		"worktree", "add", ws.WorktreePath, "-b", existingBranch, "HEAD",
	)

	require.NoError(mgr.Setup(t.Context(), ws))

	got, err := d.GetWorkspace(t.Context(), ws.ID)
	require.NoError(err)
	require.NotNil(got)
	assert.Equal("ready", got.Status)
	assert.Equal(existingBranch, got.WorkspaceBranch)
	headBranch := strings.TrimSpace(string(runWorkspaceTestGit(
		t, ws.WorktreePath, "branch", "--show-current",
	)))
	assert.Equal(existingBranch, headBranch)
	argvs := readRecorderArgv(t, tmuxRecord)
	require.NotEmpty(argvs)
	assert.Contains(argvs[0], ws.WorktreePath)
}

func TestSetupDoesNotReuseUnconfiguredMatchingOriginWorktree(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	wtDir := t.TempDir()

	localRepo, _, platformHost := setupHTTPWorktreeBaseForWorkspaceGitTest(
		t, "feature/thing",
	)
	repoID := seedRepo(t, d, platformHost, "acme", "widget")
	seedMR(t, d, repoID, 42, "feature/thing")

	tmuxScript, _ := writeRecorderScript(t)

	mgr := NewManager(d, wtDir)
	mgr.SetTmuxCommand([]string{tmuxScript})

	ws, err := mgr.Create(t.Context(), "github", platformHost, "acme", "widget", 42)
	require.NoError(err)
	runWorkspaceTestGit(
		t, localRepo,
		"worktree", "add", ws.WorktreePath,
		"-b", syntheticPRWorktreeBranch(42), "HEAD",
	)

	err = mgr.Setup(t.Context(), ws)

	require.Error(err)
	assert.Contains(err.Error(), "clone manager not set")
	got, getErr := d.GetWorkspace(t.Context(), ws.ID)
	require.NoError(getErr)
	require.NotNil(got)
	assert.Equal("error", got.Status)
	assert.Equal(workspaceBranchUnknown, got.WorkspaceBranch)
}

func TestSetupRejectsExistingLocalBaseWorktreeWithExecutableConfig(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	wtDir := t.TempDir()

	localRepo, _, platformHost := setupHTTPWorktreeBaseForWorkspaceGitTest(
		t, "feature/thing",
	)
	repoID := seedRepo(t, d, platformHost, "acme", "widget")
	seedMR(t, d, repoID, 42, "feature/thing")

	tmuxScript, _ := writeRecorderScript(t)

	mgr := NewManager(d, wtDir)
	mgr.SetTmuxCommand([]string{tmuxScript})
	mgr.SetWorktreeBasePathResolver(staticBaseResolver(localRepo))

	ws, err := mgr.Create(t.Context(), "github", platformHost, "acme", "widget", 42)
	require.NoError(err)
	runWorkspaceTestGit(
		t, localRepo,
		"worktree", "add", ws.WorktreePath,
		"-b", syntheticPRWorktreeBranch(42), "HEAD",
	)
	runWorkspaceTestGit(t, localRepo, "config", "extensions.worktreeConfig", "true")
	runWorkspaceTestGit(
		t, ws.WorktreePath,
		"config", "--worktree", "core.fsmonitor", "demo-fsmonitor",
	)

	err = mgr.Setup(t.Context(), ws)

	require.Error(err)
	assert.Contains(err.Error(), "local git config")
	assert.Contains(err.Error(), "core.fsmonitor")
	got, getErr := d.GetWorkspace(t.Context(), ws.ID)
	require.NoError(getErr)
	require.NotNil(got)
	assert.Equal("error", got.Status)
	assert.Equal(workspaceBranchUnknown, got.WorkspaceBranch)
}

func TestSetupRejectsExistingSyntheticPRWorktreeOnStaleHead(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	wtDir := t.TempDir()

	localRepo, remote, platformHost := setupHTTPWorktreeBaseForWorkspaceGitTest(
		t, "feature/thing",
	)
	repoID := seedRepo(t, d, platformHost, "acme", "widget")
	seedMR(t, d, repoID, 42, "feature/thing")

	tmuxScript, _ := writeRecorderScript(t)

	mgr := NewManager(d, wtDir)
	mgr.SetTmuxCommand([]string{tmuxScript})
	mgr.SetWorktreeBasePathResolver(staticBaseResolver(localRepo))

	ws, err := mgr.Create(t.Context(), "github", platformHost, "acme", "widget", 42)
	require.NoError(err)
	existingBranch := syntheticPRWorktreeBranch(42)
	runWorkspaceTestGit(
		t, localRepo,
		"worktree", "add", ws.WorktreePath, "-b", existingBranch, "HEAD",
	)

	require.NoError(os.WriteFile(
		filepath.Join(localRepo, "new-head.txt"), []byte("new head\n"), 0o644,
	))
	runWorkspaceTestGit(t, localRepo, "add", ".")
	runWorkspaceTestGit(t, localRepo, "commit", "-m", "new pr head")
	newHead := strings.TrimSpace(string(runWorkspaceTestGit(
		t, localRepo, "rev-parse", "HEAD",
	)))
	runWorkspaceTestGit(
		t, localRepo, "push", remote,
		"HEAD:refs/heads/feature/thing",
	)
	runWorkspaceTestGit(t, remote, "update-server-info")

	err = mgr.Setup(t.Context(), ws)

	require.Error(err)
	assert.Contains(err.Error(), "not current workspace head")
	got, getErr := d.GetWorkspace(t.Context(), ws.ID)
	require.NoError(getErr)
	require.NotNil(got)
	assert.Equal("error", got.Status)
	require.NotNil(got.ErrorMessage)
	assert.Contains(*got.ErrorMessage, "not current workspace head")
	gotHead := strings.TrimSpace(string(runWorkspaceTestGit(
		t, localRepo, "rev-parse", "refs/remotes/origin/feature/thing",
	)))
	assert.Equal(newHead, gotHead)
}

func TestSetupReusesExistingLocalBasePRHeadBranchWithoutManagingIt(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	wtDir := t.TempDir()

	localRepo, _, platformHost := setupHTTPWorktreeBaseForWorkspaceGitTest(
		t, "feature/thing",
	)
	repoID := seedRepo(t, d, platformHost, "acme", "widget")
	seedMR(t, d, repoID, 42, "feature/thing")

	tmuxScript, _ := writeRecorderScript(t)

	mgr := NewManager(d, wtDir)
	mgr.SetTmuxCommand([]string{tmuxScript})
	mgr.SetWorktreeBasePathResolver(staticBaseResolver(localRepo))

	ws, err := mgr.Create(t.Context(), "github", platformHost, "acme", "widget", 42)
	require.NoError(err)
	runWorkspaceTestGit(
		t, localRepo,
		"branch", "feature/thing", "refs/remotes/origin/feature/thing",
	)
	runWorkspaceTestGit(
		t, localRepo,
		"worktree", "add", ws.WorktreePath, "feature/thing",
	)

	require.NoError(mgr.Setup(t.Context(), ws))

	got, err := d.GetWorkspace(t.Context(), ws.ID)
	require.NoError(err)
	require.NotNil(got)
	assert.Equal("ready", got.Status)
	assert.Empty(got.WorkspaceBranch)

	require.NoError(mgr.cleanupWorkspaceArtifactsForDelete(t.Context(), got))
	exists, err := localBranchExists(t.Context(), localRepo, "feature/thing")
	require.NoError(err)
	assert.True(exists)
}

func TestSetupRejectsExistingPRWorktreeOnUnexpectedBranch(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	wtDir := t.TempDir()

	localRepo, _, platformHost := setupHTTPWorktreeBaseForWorkspaceGitTest(
		t, "feature/thing",
	)
	repoID := seedRepo(t, d, platformHost, "acme", "widget")
	seedMR(t, d, repoID, 42, "feature/thing")

	tmuxScript, _ := writeRecorderScript(t)

	mgr := NewManager(d, wtDir)
	mgr.SetTmuxCommand([]string{tmuxScript})
	mgr.SetWorktreeBasePathResolver(staticBaseResolver(localRepo))

	ws, err := mgr.Create(t.Context(), "github", platformHost, "acme", "widget", 42)
	require.NoError(err)
	runWorkspaceTestGit(
		t, localRepo,
		"worktree", "add", ws.WorktreePath, "-b", "wrong/main", "main",
	)

	err = mgr.Setup(t.Context(), ws)

	require.Error(err)
	got, getErr := d.GetWorkspace(t.Context(), ws.ID)
	require.NoError(getErr)
	require.NotNil(got)
	assert.Equal("error", got.Status)
	require.NotNil(got.ErrorMessage)
	assert.Contains(*got.ErrorMessage, "existing worktree branch")
	assert.Contains(*got.ErrorMessage, "wrong/main")
}

func TestValidateWorktreeBasePathRejectsLocalRemotes(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	tests := []struct {
		name      string
		remoteURL string
	}{
		{name: "absolute path", remoteURL: filepath.Join(t.TempDir(), "remote.git")},
		{name: "file URL", remoteURL: "file://" + filepath.ToSlash(filepath.Join(t.TempDir(), "remote.git"))},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			localRepo := setupLocalWorktreeBaseForWorkspaceGitTest(t, "feature/thing")
			runWorkspaceTestGit(
				t, localRepo, "remote", "set-url", "origin", tt.remoteURL,
			)

			got, err := ValidateWorktreeBasePath(
				t.Context(), localRepo, "github.com", "acme", "widget",
			)

			require.Empty(got)
			require.Error(err)
			assert.Contains(err.Error(), "origin remote must include a forge host")
		})
	}
}

func TestValidateWorktreeBasePathRejectsExecutableLocalConfig(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	tests := []struct {
		name  string
		key   string
		value string
	}{
		{name: "filter process", key: "filter.demo.process", value: "demo-filter"},
		{name: "filter smudge", key: "filter.demo.smudge", value: "demo-smudge"},
		{name: "filter clean", key: "filter.demo.clean", value: "demo-clean"},
		{name: "fsmonitor", key: "core.fsmonitor", value: "demo-fsmonitor"},
		{name: "alternate refs command", key: "core.alternateRefsCommand", value: "demo-alternates"},
		{name: "askpass", key: "core.askPass", value: "demo-askpass"},
		{name: "git proxy", key: "core.gitProxy", value: "demo-proxy"},
		{name: "ssh command", key: "core.sshCommand", value: "demo-ssh"},
		{name: "credential helper", key: "credential.helper", value: "!demo-helper"},
		{name: "diff external", key: "diff.external", value: "demo-diff"},
		{name: "diff driver command", key: "diff.demo.command", value: "demo-command"},
		{name: "diff textconv", key: "diff.demo.textconv", value: "demo-textconv"},
		{name: "fetch recurse submodules", key: "fetch.recurseSubmodules", value: "true"},
		{name: "http proxy", key: "http.proxy", value: "http://127.0.0.1:1"},
		{name: "http url proxy", key: "http.https://github.com.proxy", value: "http://127.0.0.1:1"},
		{name: "http ssl verify", key: "http.sslVerify", value: "false"},
		{name: "http extra header", key: "http.extraHeader", value: "Authorization: bearer secret"},
		{name: "http cookie file", key: "http.cookieFile", value: filepath.Join(t.TempDir(), "cookies")},
		{name: "remote proxy", key: "remote.origin.proxy", value: "http://127.0.0.1:1"},
		{name: "submodule recurse", key: "submodule.recurse", value: "true"},
		{name: "url rewrite", key: "url.https://example.invalid/.insteadOf", value: "https://github.com/"},
		{name: "include path", key: "include.path", value: filepath.Join(t.TempDir(), "config")},
		{name: "conditional include", key: "includeIf.gitdir:~/demo/.path", value: filepath.Join(t.TempDir(), "config")},
		{name: "protocol allow", key: "protocol.ext.allow", value: "always"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			localRepo := setupLocalWorktreeBaseForWorkspaceGitTest(t, "feature/thing")
			runWorkspaceTestGit(t, localRepo, "config", tt.key, tt.value)

			got, err := ValidateWorktreeBasePath(
				t.Context(), localRepo, "github.com", "acme", "widget",
			)

			require.Empty(got)
			require.Error(err)
			assert.Contains(
				strings.ToLower(err.Error()), strings.ToLower(tt.key),
			)
			assert.Contains(err.Error(), "may execute or rewrite git commands")
		})
	}
}

func TestValidateWorktreeBasePathAcceptsConfiguredHooksPath(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	localRepo := setupLocalWorktreeBaseForWorkspaceGitTest(t, "feature/thing")
	hooksPath := t.TempDir()
	runWorkspaceTestGit(t, localRepo, "config", "core.hooksPath", hooksPath)
	commonDir := strings.TrimSpace(string(runWorkspaceTestGit(
		t, localRepo, "rev-parse", "--path-format=absolute", "--git-common-dir",
	)))
	hookPath := filepath.Join(commonDir, "hooks", "post-commit")
	require.NoError(os.WriteFile(hookPath, []byte("#!/bin/sh\nexit 0\n"), 0o755))

	got, err := ValidateWorktreeBasePath(
		t.Context(), localRepo, "github.com", "acme", "widget",
	)

	require.NoError(err)
	canonicalLocalRepo, err := filepath.EvalSymlinks(localRepo)
	require.NoError(err)
	assert.Equal(canonicalLocalRepo, got)
}

func TestValidateWorktreeBasePathRejectsUnsafeOriginSchemes(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	tests := []struct {
		name       string
		remoteURL  string
		wantScheme string
	}{
		{
			name:       "git protocol",
			remoteURL:  "git://github.com/acme/widget.git",
			wantScheme: "git",
		},
		{
			name:       "plain http",
			remoteURL:  "http://github.com/acme/widget.git",
			wantScheme: "http",
		},
		{
			name:       "http with embedded credentials",
			remoteURL:  "http://oauth2:secret-token@github.com/acme/widget.git",
			wantScheme: "http",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			localRepo := setupLocalWorktreeBaseForWorkspaceGitTest(t, "feature/thing")
			runWorkspaceTestGit(
				t, localRepo, "remote", "set-url", "origin", tt.remoteURL,
			)

			got, err := ValidateWorktreeBasePath(
				t.Context(), localRepo, "github.com", "acme", "widget",
			)

			require.Empty(got)
			require.Error(err)
			assert.Contains(
				err.Error(),
				fmt.Sprintf(
					"origin remote scheme %q is not allowed (host %q)",
					tt.wantScheme, "github.com",
				),
			)
			// The validation error is persisted as workspace error state and
			// served through the API, so credentials must never appear.
			assert.NotContains(err.Error(), "secret-token")
			assert.NotContains(err.Error(), tt.remoteURL)
		})
	}
}

func TestValidateWorktreeBasePathAcceptsLoopbackHTTPOrigin(t *testing.T) {
	require := require.New(t)

	localRepo := setupLocalWorktreeBaseForWorkspaceGitTest(t, "feature/thing")
	runWorkspaceTestGit(
		t, localRepo, "remote", "set-url", "origin",
		"http://127.0.0.1/acme/widget.git",
	)

	got, err := ValidateWorktreeBasePath(
		t.Context(), localRepo, "127.0.0.1", "acme", "widget",
	)

	require.NoError(err)
	canonicalLocalRepo, err := filepath.EvalSymlinks(localRepo)
	require.NoError(err)
	require.Equal(canonicalLocalRepo, got)
}

func TestValidateWorktreeBasePathAcceptsSCPStyleSSHOrigin(t *testing.T) {
	require := require.New(t)

	localRepo := setupLocalWorktreeBaseForWorkspaceGitTest(t, "feature/thing")
	runWorkspaceTestGit(
		t, localRepo, "remote", "set-url", "origin",
		"git@github.com:acme/widget.git",
	)

	got, err := ValidateWorktreeBasePath(
		t.Context(), localRepo, "github.com", "acme", "widget",
	)

	require.NoError(err)
	canonicalLocalRepo, err := filepath.EvalSymlinks(localRepo)
	require.NoError(err)
	require.Equal(canonicalLocalRepo, got)
}

func TestValidateWorktreeBasePathCanonicalizesSymlinkPath(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	localRepo := setupLocalWorktreeBaseForWorkspaceGitTest(t, "feature/thing")
	canonicalLocalRepo, err := filepath.EvalSymlinks(localRepo)
	require.NoError(err)
	parentLink := filepath.Join(t.TempDir(), "parent-link")
	require.NoError(os.Symlink(filepath.Dir(localRepo), parentLink))
	tests := []struct {
		name string
		path string
	}{
		{
			name: "final component",
			path: func() string {
				linkPath := filepath.Join(t.TempDir(), "repo-link")
				require.NoError(os.Symlink(localRepo, linkPath))
				return linkPath
			}(),
		},
		{
			name: "parent component",
			path: filepath.Join(parentLink, filepath.Base(localRepo)),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ValidateWorktreeBasePath(
				t.Context(), tt.path, "github.com", "acme", "widget",
			)

			require.NoError(err)
			assert.Equal(canonicalLocalRepo, got)
		})
	}
}

func TestValidateWorktreeBasePathRejectsAdditionalOriginURLs(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	localRepo := setupLocalWorktreeBaseForWorkspaceGitTest(t, "feature/thing")
	runWorkspaceTestGit(
		t, localRepo, "config", "--add", "remote.origin.url",
		"https://github.com/evil/widget.git",
	)

	got, err := ValidateWorktreeBasePath(
		t.Context(), localRepo, "github.com", "acme", "widget",
	)

	require.Empty(got)
	require.Error(err)
	assert.Contains(err.Error(), "origin remote does not match repository")
}

func TestValidateWorktreeBasePathRejectsUnsafeOriginFetchRefspec(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	localRepo := setupLocalWorktreeBaseForWorkspaceGitTest(t, "feature/thing")
	runWorkspaceTestGit(
		t, localRepo,
		"config", "--unset-all", "remote.origin.fetch",
	)
	runWorkspaceTestGit(
		t, localRepo,
		"config", "--add", "remote.origin.fetch",
		"+refs/heads/*:refs/heads/*",
	)

	got, err := ValidateWorktreeBasePath(
		t.Context(), localRepo, "github.com", "acme", "widget",
	)

	require.Empty(got)
	require.Error(err)
	assert.Contains(err.Error(), "origin fetch refspec")
	assert.Contains(err.Error(), "may update unsafe refs")
}

func TestValidateWorktreeBasePathAcceptsSingleBranchOriginFetchRefspec(t *testing.T) {
	require := require.New(t)

	localRepo := setupLocalWorktreeBaseForWorkspaceGitTest(t, "feature/thing")
	runWorkspaceTestGit(
		t, localRepo,
		"config", "--unset-all", "remote.origin.fetch",
	)
	runWorkspaceTestGit(
		t, localRepo,
		"config", "--add", "remote.origin.fetch",
		"+refs/heads/main:refs/remotes/origin/main",
	)

	got, err := ValidateWorktreeBasePath(
		t.Context(), localRepo, "github.com", "acme", "widget",
	)

	require.NoError(err)
	canonicalLocalRepo, err := filepath.EvalSymlinks(localRepo)
	require.NoError(err)
	require.Equal(canonicalLocalRepo, got)
}

func TestValidateWorktreeBasePathRejectsBareRepositories(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	dir := t.TempDir()
	bareRepo := filepath.Join(dir, "repo.git")
	runWorkspaceTestGit(t, dir, "init", "--bare", bareRepo)
	runWorkspaceTestGit(
		t, bareRepo, "config", "remote.origin.url",
		"https://github.com/acme/widget.git",
	)

	got, err := ValidateWorktreeBasePath(
		t.Context(), bareRepo, "github.com", "acme", "widget",
	)

	require.Empty(got)
	require.Error(err)
	assert.Contains(err.Error(), "path is not a git worktree")
}

func TestValidateWorktreeBasePathRejectsExecutableWorktreeConfig(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	localRepo := setupLocalWorktreeBaseForWorkspaceGitTest(t, "feature/thing")
	runWorkspaceTestGit(t, localRepo, "config", "extensions.worktreeConfig", "true")
	runWorkspaceTestGit(
		t, localRepo, "config", "--worktree",
		"filter.demo.clean", "demo-clean",
	)

	got, err := ValidateWorktreeBasePath(
		t.Context(), localRepo, "github.com", "acme", "widget",
	)

	require.Empty(got)
	require.Error(err)
	assert.Contains(err.Error(), "filter.demo.clean")
	assert.Contains(err.Error(), "may execute or rewrite git commands")
}

func TestCreateIssueUsesProviderQualifiedRepo(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()

	_, err := d.UpsertRepo(ctx, db.RepoIdentity{
		Platform:     "github",
		PlatformHost: "forge.example.com",
		Owner:        "acme",
		Name:         "widget",
	})
	require.NoError(err)
	gitlabRepoID, err := d.UpsertRepo(ctx, db.RepoIdentity{
		Platform:     "gitlab",
		PlatformHost: "forge.example.com",
		Owner:        "acme",
		Name:         "widget",
	})
	require.NoError(err)
	seedIssue(t, d, gitlabRepoID, 7, "GitLab issue")

	mgr := NewManager(d, t.TempDir())
	ws, err := mgr.CreateIssue(
		ctx, "forge.example.com", "acme", "widget", 7,
		CreateIssueOptions{Provider: "gitlab"},
	)

	require.NoError(err)
	require.NotNil(ws)
	assert.Equal("gitlab", ws.Platform)
}

func TestCreateIssueUsesProviderCloneURLForNamespacedManagedClone(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	_, remote := setupLocalWorktreeBaseWithRemoteForWorkspaceGitTest(
		t, "feature/thing",
	)

	repoID, err := d.UpsertRepo(ctx, db.RepoIdentity{
		Platform:     "gitlab",
		PlatformHost: "gitlab.example.com",
		Owner:        "group",
		Name:         "project",
	})
	require.NoError(err)
	require.NoError(d.UpdateRepoProviderMetadata(
		ctx, repoID, db.RepoProviderMetadata{
			CloneURL:      remote,
			DefaultBranch: "main",
		},
	))
	seedIssue(t, d, repoID, 11, "GitLab issue")

	clones := gitclone.New(filepath.Join(t.TempDir(), "clones"), nil)
	mgr := NewManager(d, t.TempDir())
	mgr.SetClones(clones)

	ws, err := mgr.CreateIssue(
		ctx, "gitlab.example.com", "group", "project", 11,
		CreateIssueOptions{Provider: "gitlab"},
	)

	require.NoError(err)
	require.NotNil(ws)
	assert.Equal("gitlab", ws.Platform)
	cloneDir, err := clones.ClonePathInNamespace(
		"gitlab", "gitlab.example.com", "group", "project",
	)
	require.NoError(err)
	assert.DirExists(cloneDir)
	assert.Equal(
		remote,
		strings.TrimSpace(string(runWorkspaceTestGit(
			t, cloneDir, "config", "--get", "remote.origin.url",
		))),
	)
}

func TestCreateUsesProviderQualifiedRepo(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	worktreeDir := t.TempDir()

	_, err := d.UpsertRepo(ctx, db.RepoIdentity{
		Platform:     "github",
		PlatformHost: "forge.example.com",
		Owner:        "acme",
		Name:         "widget",
	})
	require.NoError(err)
	gitlabRepoID, err := d.UpsertRepo(ctx, db.RepoIdentity{
		Platform:     "gitlab",
		PlatformHost: "forge.example.com",
		Owner:        "acme",
		Name:         "widget",
	})
	require.NoError(err)
	seedMR(t, d, gitlabRepoID, 42, "feature/gitlab")

	mgr := NewManager(d, worktreeDir)
	ws, err := mgr.Create(
		ctx, "gitlab", "forge.example.com", "acme", "widget", 42,
	)

	require.NoError(err)
	require.NotNil(ws)
	assert.Equal("gitlab", ws.Platform)
	assert.Equal("feature/gitlab", ws.GitHeadRef)
	assert.Equal(
		filepath.Join(
			worktreeDir, "gitlab", "forge.example.com", "acme", "widget", "pr-42",
		),
		ws.WorktreePath,
	)
}

func TestSetupUsesManagedCloneForForkPRWithConfiguredWorktreeBasePath(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	wtDir := t.TempDir()
	cloneBaseDir := t.TempDir()

	const (
		host     = "github.com"
		owner    = "acme"
		name     = "widget"
		prNumber = 245
		branch   = "fork/thing"
	)
	repoID := seedRepo(t, d, host, owner, name)
	seedMRWithHeadRepo(
		t, d, repoID, prNumber, branch,
		"https://github.com/contributor/widget.git",
	)

	remote, pullSHA := setupRemoteForForkPRWorktreeTest(
		t, branch, prNumber,
	)
	clones := gitclone.New(cloneBaseDir, nil)
	cloneDir, err := clones.ClonePath(host, owner, name)
	require.NoError(err)
	require.NoError(os.MkdirAll(filepath.Dir(cloneDir), 0o755))
	runWorkspaceTestGit(t, cloneBaseDir, "clone", "--bare", remote, cloneDir)
	runWorkspaceTestGit(
		t, cloneDir, "remote", "set-url", "origin",
		"https://github.com/acme/widget.git",
	)
	runWorkspaceTestGit(
		t, cloneDir, "config", "--add",
		"url."+remote+".insteadOf", "https://github.com/acme/widget.git",
	)
	runWorkspaceTestGit(t, cloneDir, "update-ref", "-d", "refs/heads/"+branch)

	localRepo := setupLocalWorktreeBaseForWorkspaceGitTest(t, branch)
	tmuxScript, _ := writeRecorderScript(t)

	mgr := NewManager(d, wtDir)
	mgr.SetClones(clones)
	mgr.SetTmuxCommand([]string{tmuxScript})
	mgr.SetWorktreeBasePathResolver(staticBaseResolver(localRepo))

	ws, err := mgr.Create(t.Context(), "github", host, owner, name, prNumber)
	require.NoError(err)
	require.NoError(mgr.Setup(t.Context(), ws))

	got, err := d.GetWorkspace(t.Context(), ws.ID)
	require.NoError(err)
	require.NotNil(got)
	assert.Equal("ready", got.Status)
	assert.Equal(branch, got.WorkspaceBranch)

	headSHA, err := gitHeadSHA(t.Context(), ws.WorktreePath)
	require.NoError(err)
	assert.Equal(pullSHA, headSHA)

	canonicalWorktreePath, err := filepath.EvalSymlinks(ws.WorktreePath)
	require.NoError(err)
	managedList := string(runWorkspaceTestGit(
		t, cloneDir, "worktree", "list", "--porcelain",
	))
	assert.Contains(managedList, "worktree "+canonicalWorktreePath)
	localList := string(runWorkspaceTestGit(
		t, localRepo, "worktree", "list", "--porcelain",
	))
	assert.NotContains(localList, "worktree "+canonicalWorktreePath)
}

func TestSetupFetchesConfiguredWorktreeBasePathBeforeAdd(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	wtDir := t.TempDir()

	const branch = "feature/fetch-before-add"
	localRepo, remote, platformHost := setupHTTPWorktreeBaseForWorkspaceGitTest(
		t, branch,
	)
	repoID := seedRepo(t, d, platformHost, "acme", "widget")
	seedMR(t, d, repoID, 42, branch)

	remoteWork := filepath.Join(t.TempDir(), "remote-work")
	runWorkspaceTestGit(t, t.TempDir(), "clone", remote, remoteWork)
	runWorkspaceTestGit(t, remoteWork, "config", "user.email", "test@test.com")
	runWorkspaceTestGit(t, remoteWork, "config", "user.name", "Test")
	runWorkspaceTestGit(t, remoteWork, "checkout", branch)
	require.NoError(os.WriteFile(
		filepath.Join(remoteWork, "fresh.txt"), []byte("fresh\n"), 0o644,
	))
	runWorkspaceTestGit(t, remoteWork, "add", ".")
	runWorkspaceTestGit(t, remoteWork, "commit", "-m", "fresh branch commit")
	expectedSHA := strings.TrimSpace(string(runWorkspaceTestGit(
		t, remoteWork, "rev-parse", "HEAD",
	)))
	runWorkspaceTestGit(t, remoteWork, "push", "origin", "HEAD:refs/heads/"+branch)
	runWorkspaceTestGit(t, remote, "update-server-info")

	tmuxScript, _ := writeRecorderScript(t)
	mgr := NewManager(d, wtDir)
	mgr.SetTmuxCommand([]string{tmuxScript})
	mgr.SetWorktreeBasePathResolver(staticBaseResolver(localRepo))

	ws, err := mgr.Create(t.Context(), "github", platformHost, "acme", "widget", 42)
	require.NoError(err)
	require.NoError(mgr.Setup(t.Context(), ws))

	headSHA, err := gitHeadSHA(t.Context(), ws.WorktreePath)
	require.NoError(err)
	assert.Equal(expectedSHA, headSHA)
}

func TestSetupRefreshesConfiguredWorktreeBaseOriginHead(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	wtDir := t.TempDir()

	localRepo, _, platformHost := setupHTTPWorktreeBaseForWorkspaceGitTest(
		t, "feature/thing",
	)
	runWorkspaceTestGit(t, localRepo, "symbolic-ref", "--delete", "refs/remotes/origin/HEAD")
	repoID := seedRepo(t, d, platformHost, "acme", "widget")
	seedIssue(t, d, repoID, 7, "")

	tmuxScript, _ := writeRecorderScript(t)
	mgr := NewManager(d, wtDir)
	mgr.SetTmuxCommand([]string{tmuxScript})
	mgr.SetWorktreeBasePathResolver(staticBaseResolver(localRepo))

	ws, err := mgr.CreateIssue(
		t.Context(), platformHost, "acme", "widget", 7, CreateIssueOptions{},
	)
	require.NoError(err)
	require.NoError(mgr.Setup(t.Context(), ws))

	got, err := d.GetWorkspace(t.Context(), ws.ID)
	require.NoError(err)
	require.NotNil(got)
	assert.Equal("ready", got.Status)
	assert.Equal("middleman/issue-7", got.WorkspaceBranch)
	ref := strings.TrimSpace(string(runWorkspaceTestGit(
		t, localRepo, "symbolic-ref", "refs/remotes/origin/HEAD",
	)))
	assert.Equal("refs/remotes/origin/main", ref)
}

func TestFetchWorkspaceBaseRequiresOriginHeadOnlyForIssueWorkspaces(t *testing.T) {
	require := require.New(t)

	const branch = "feature/no-head"
	root := t.TempDir()
	remote := filepath.Join(root, "acme", "widget.git")
	localRepo := filepath.Join(root, "repo")
	require.NoError(os.MkdirAll(filepath.Dir(remote), 0o755))
	runWorkspaceTestGit(t, root, "init", "--bare", "--initial-branch=trunk", remote)
	runWorkspaceTestGit(t, root, "init", "--initial-branch=trunk", localRepo)
	runWorkspaceTestGit(t, localRepo, "config", "user.email", "test@test.com")
	runWorkspaceTestGit(t, localRepo, "config", "user.name", "Test")
	runWorkspaceTestGit(t, localRepo, "remote", "add", "origin", remote)
	require.NoError(os.WriteFile(
		filepath.Join(localRepo, "base.txt"), []byte("base\n"), 0o644,
	))
	runWorkspaceTestGit(t, localRepo, "add", ".")
	runWorkspaceTestGit(t, localRepo, "commit", "-m", "base commit")
	runWorkspaceTestGit(t, localRepo, "push", "origin", "HEAD:refs/heads/trunk")
	runWorkspaceTestGit(t, localRepo, "push", "origin", "HEAD:refs/heads/"+branch)
	server := httptest.NewServer(http.FileServer(http.Dir(root)))
	t.Cleanup(server.Close)
	runWorkspaceTestGit(
		t, localRepo, "remote", "set-url", "origin",
		server.URL+"/acme/widget.git",
	)
	runWorkspaceTestGit(t, remote, "update-server-info")
	runWorkspaceTestGit(t, localRepo, "fetch", "--prune", "origin")
	runWorkspaceTestGit(t, remote, "symbolic-ref", "HEAD", "refs/heads/missing")
	runWorkspaceTestGit(t, remote, "update-server-info")
	runWorkspaceTestGit(
		t, localRepo,
		"symbolic-ref", "--delete", "refs/remotes/origin/HEAD",
	)

	require.NoError(fetchWorkspaceBaseWithGit(t.Context(), runGitWithoutHooks, localRepo, false))
	require.Error(fetchWorkspaceBaseWithGit(t.Context(), runGitWithoutHooks, localRepo, true))
}

func TestFetchWorkspaceBaseConstrainsNegotiationTips(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	var calls [][]string
	run := func(_ context.Context, _ string, args ...string) error {
		calls = append(calls, slices.Clone(args))
		return nil
	}

	require.NoError(fetchWorkspaceBaseWithGit(
		t.Context(), run, t.TempDir(), false,
	))
	require.NotEmpty(calls)
	fetchArgs := calls[0]
	assert.Contains(fetchArgs, "--negotiation-tip=refs/remotes/origin/*")
	assert.Contains(fetchArgs, "--recurse-submodules=no")
	assert.Contains(fetchArgs, "--no-tags")
}

func TestFetchWorkspaceBaseDisablesGitHooks(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	var calls [][]string
	run := func(_ context.Context, _ string, args ...string) error {
		calls = append(calls, slices.Clone(args))
		return nil
	}

	require.NoError(fetchWorkspaceBaseWithGit(
		t.Context(), run, t.TempDir(), false,
	))
	require.Len(calls, 2)
	for _, args := range calls {
		require.GreaterOrEqual(len(args), 2)
		assert.Equal("-c", args[0])
		assert.Equal("core.hooksPath=/dev/null", args[1])
	}
	assert.Contains(calls[0], "fetch")
	assert.Contains(calls[1], "remote")
	assert.Contains(calls[1], "set-head")
}

func TestCleanupUsesExistingWorktreeGitDirWhenConfiguredBaseChanges(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	const branch = "middleman/pr-99"
	actualRepo := setupLocalWorktreeBaseForWorkspaceGitTest(t, "feature/thing")
	wrongRepo := setupLocalWorktreeBaseForWorkspaceGitTest(t, "feature/thing")
	worktreePath := filepath.Join(t.TempDir(), "workspace")
	runWorkspaceTestGit(
		t, actualRepo,
		"worktree", "add", worktreePath, "-b", branch, "HEAD",
	)
	runWorkspaceTestGit(t, wrongRepo, "branch", branch, "HEAD")

	mgr := NewManager(openTestDB(t), t.TempDir())
	mgr.SetWorktreeBasePathResolver(staticBaseResolver(wrongRepo))
	ws := &Workspace{
		ID:              "ws-cleanup-existing-worktree",
		PlatformHost:    "github.com",
		RepoOwner:       "acme",
		RepoName:        "widget",
		ItemType:        db.WorkspaceItemTypePullRequest,
		ItemNumber:      99,
		GitHeadRef:      "feature/thing",
		WorkspaceBranch: branch,
		WorktreePath:    worktreePath,
	}

	require.NoError(mgr.cleanupWorkspaceArtifactsForDelete(t.Context(), ws))

	_, err := os.Stat(worktreePath)
	assert.True(os.IsNotExist(err), "cleanup should remove original worktree")
	actualExists, err := localBranchExists(t.Context(), actualRepo, branch)
	require.NoError(err)
	assert.False(actualExists)
	wrongExists, err := localBranchExists(t.Context(), wrongRepo, branch)
	require.NoError(err)
	assert.True(wrongExists, "cleanup must not delete branch from current settings repo")
}

func TestCleanupDoesNotTrustReplacementCloneAtWorkspacePath(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	const branch = "middleman/pr-42"
	_, remote := setupLocalWorktreeBaseWithRemoteForWorkspaceGitTest(t, "feature/thing")
	worktreePath := filepath.Join(t.TempDir(), "workspace")
	runWorkspaceTestGit(t, t.TempDir(), "clone", remote, worktreePath)
	runWorkspaceTestGit(
		t, worktreePath, "remote", "set-url", "origin",
		"https://github.com/acme/widget.git",
	)
	runWorkspaceTestGit(t, worktreePath, "branch", branch, "HEAD")

	mgr := NewManager(openTestDB(t), t.TempDir())
	ws := &Workspace{
		ID:              "ws-replaced-clone",
		Platform:        "github",
		PlatformHost:    "github.com",
		RepoOwner:       "acme",
		RepoName:        "widget",
		ItemType:        db.WorkspaceItemTypePullRequest,
		ItemNumber:      42,
		GitHeadRef:      "feature/thing",
		WorkspaceBranch: branch,
		WorktreePath:    worktreePath,
	}

	gitDir, ok, err := mgr.workspaceCleanupGitDir(t.Context(), ws)

	require.NoError(err)
	assert.False(ok)
	assert.Empty(gitDir)
	branchExists, err := localBranchExists(t.Context(), worktreePath, branch)
	require.NoError(err)
	assert.True(branchExists)
}

func TestCleanupDoesNotTrustStaleLocalBaseRegistrationForReplacementClone(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	const branch = "middleman/pr-42"
	localRepo, remote := setupLocalWorktreeBaseWithRemoteForWorkspaceGitTest(
		t, "feature/thing",
	)
	worktreePath := filepath.Join(t.TempDir(), "workspace")
	runWorkspaceTestGit(
		t, localRepo,
		"worktree", "add", worktreePath, "-b", branch, "HEAD",
	)
	require.NoError(os.RemoveAll(worktreePath))
	runWorkspaceTestGit(t, t.TempDir(), "clone", remote, worktreePath)
	runWorkspaceTestGit(
		t, worktreePath, "remote", "set-url", "origin",
		"https://github.com/acme/widget.git",
	)
	runWorkspaceTestGit(t, worktreePath, "branch", branch, "HEAD")

	mgr := NewManager(openTestDB(t), t.TempDir())
	mgr.SetWorktreeBasePathResolver(staticBaseResolver(localRepo))
	ws := &Workspace{
		ID:              "ws-stale-local-base-replaced-clone",
		Platform:        "github",
		PlatformHost:    "github.com",
		RepoOwner:       "acme",
		RepoName:        "widget",
		ItemType:        db.WorkspaceItemTypePullRequest,
		ItemNumber:      42,
		GitHeadRef:      "feature/thing",
		WorkspaceBranch: branch,
		WorktreePath:    worktreePath,
	}

	gitDir, ok, err := mgr.workspaceCleanupGitDir(t.Context(), ws)

	require.NoError(err)
	assert.False(ok)
	assert.Empty(gitDir)
	branchExists, err := localBranchExists(t.Context(), worktreePath, branch)
	require.NoError(err)
	assert.True(branchExists)
	_, err = os.Stat(worktreePath)
	require.NoError(err)
}

func TestCleanupIgnoresInvalidConfiguredBaseWhenWorktreeAbsent(t *testing.T) {
	require := require.New(t)

	mgr := NewManager(openTestDB(t), t.TempDir())
	mgr.SetWorktreeBasePathResolver(staticBaseResolver(filepath.Join(t.TempDir(), "missing")))
	ws := &Workspace{
		ID:              "ws-cleanup-invalid-base",
		Platform:        "github",
		PlatformHost:    "github.com",
		RepoOwner:       "acme",
		RepoName:        "widget",
		ItemType:        db.WorkspaceItemTypePullRequest,
		ItemNumber:      99,
		GitHeadRef:      "feature/thing",
		WorkspaceBranch: "middleman/pr-99",
		WorktreePath:    filepath.Join(t.TempDir(), "already-removed"),
	}

	require.NoError(mgr.cleanupWorkspaceArtifactsForDelete(t.Context(), ws))
}

func TestCleanupSucceedsWhenWorkspacePathReplacedByNonGitDirectory(t *testing.T) {
	tests := []struct {
		name    string
		corrupt func(t *testing.T, worktreePath string)
	}{
		{
			name: "plain directory",
			corrupt: func(t *testing.T, worktreePath string) {
				t.Helper()
				require.NoError(t, os.WriteFile(
					filepath.Join(worktreePath, "leftover.txt"),
					[]byte("not a repo"), 0o644,
				))
			},
		},
		{
			name: "stale .git file from a removed repo",
			corrupt: func(t *testing.T, worktreePath string) {
				t.Helper()
				gone := filepath.Join(t.TempDir(), "gone", ".git", "worktrees", "x")
				require.NoError(t, os.WriteFile(
					filepath.Join(worktreePath, ".git"),
					[]byte("gitdir: "+gone+"\n"), 0o644,
				))
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require := require.New(t)

			localRepo := setupLocalWorktreeBaseForWorkspaceGitTest(t, "feature/thing")
			worktreePath := filepath.Join(t.TempDir(), "workspace")
			require.NoError(os.MkdirAll(worktreePath, 0o755))
			tt.corrupt(t, worktreePath)

			mgr := NewManager(openTestDB(t), t.TempDir())
			mgr.SetWorktreeBasePathResolver(staticBaseResolver(localRepo))
			ws := &Workspace{
				ID:              "ws-cleanup-non-git-dir",
				Platform:        "github",
				PlatformHost:    "github.com",
				RepoOwner:       "acme",
				RepoName:        "widget",
				ItemType:        db.WorkspaceItemTypePullRequest,
				ItemNumber:      99,
				GitHeadRef:      "feature/thing",
				WorkspaceBranch: "middleman/pr-99",
				WorktreePath:    worktreePath,
			}

			require.NoError(mgr.cleanupWorkspaceArtifactsForDelete(t.Context(), ws))
		})
	}
}

func TestCleanupFallsBackToManagedCloneWhenConfiguredBaseInvalid(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	const branch = "middleman/pr-99"
	cloneBaseDir := t.TempDir()
	clones := gitclone.New(cloneBaseDir, nil)
	cloneDir, err := clones.ClonePath("github.com", "acme", "widget")
	require.NoError(err)
	require.NoError(os.MkdirAll(filepath.Dir(cloneDir), 0o755))
	runWorkspaceTestGit(
		t, cloneBaseDir, "clone", "--bare",
		setupLocalWorktreeBaseForWorkspaceGitTest(t, "feature/thing"),
		cloneDir,
	)
	worktreePath := filepath.Join(t.TempDir(), "workspace")
	runWorkspaceTestGit(
		t, cloneDir, "worktree", "add", worktreePath, "-b", branch, "HEAD",
	)
	require.NoError(os.RemoveAll(worktreePath))

	mgr := NewManager(openTestDB(t), t.TempDir())
	mgr.SetClones(clones)
	mgr.SetWorktreeBasePathResolver(staticBaseResolver(filepath.Join(t.TempDir(), "missing")))
	ws := &Workspace{
		ID:              "ws-cleanup-managed-fallback",
		Platform:        "github",
		PlatformHost:    "github.com",
		RepoOwner:       "acme",
		RepoName:        "widget",
		ItemType:        db.WorkspaceItemTypePullRequest,
		ItemNumber:      99,
		GitHeadRef:      "feature/thing",
		WorkspaceBranch: branch,
		WorktreePath:    worktreePath,
	}

	require.NoError(mgr.cleanupWorkspaceArtifactsForDelete(t.Context(), ws))

	exists, err := localBranchExists(t.Context(), cloneDir, branch)
	require.NoError(err)
	assert.False(exists)
}

func TestCleanupUsesProviderScopedManagedClone(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	const branch = "middleman/pr-99"
	const host = "forge.example.com"
	cloneBaseDir := t.TempDir()
	clones := gitclone.New(cloneBaseDir, nil)
	cloneDir, err := clones.ClonePathInNamespace(
		workspaceCloneNamespace("gitlab"), host, "acme", "widget",
	)
	require.NoError(err)
	require.NoError(os.MkdirAll(filepath.Dir(cloneDir), 0o755))
	runWorkspaceTestGit(
		t, cloneBaseDir, "clone", "--bare",
		setupLocalWorktreeBaseForWorkspaceGitTest(t, "feature/thing"),
		cloneDir,
	)
	worktreePath := filepath.Join(t.TempDir(), "workspace")
	runWorkspaceTestGit(
		t, cloneDir, "worktree", "add", worktreePath, "-b", branch, "HEAD",
	)
	require.NoError(os.RemoveAll(worktreePath))

	mgr := NewManager(openTestDB(t), t.TempDir())
	mgr.SetClones(clones)
	ws := &Workspace{
		ID:              "ws-cleanup-provider-scoped-managed",
		Platform:        "gitlab",
		PlatformHost:    host,
		RepoOwner:       "acme",
		RepoName:        "widget",
		ItemType:        db.WorkspaceItemTypePullRequest,
		ItemNumber:      99,
		GitHeadRef:      "feature/thing",
		WorkspaceBranch: branch,
		WorktreePath:    worktreePath,
	}

	require.NoError(mgr.cleanupWorkspaceArtifactsForDelete(t.Context(), ws))

	exists, err := localBranchExists(t.Context(), cloneDir, branch)
	require.NoError(err)
	assert.False(exists)
}

func TestCleanupSkipsReplacedWorktreeFromWrongRepo(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	const branch = "middleman/pr-99"
	unrelatedRepo := setupLocalWorktreeBaseForWorkspaceGitTest(t, "feature/thing")
	runWorkspaceTestGit(
		t, unrelatedRepo, "remote", "set-url", "origin",
		"https://github.com/evil/widget.git",
	)
	runWorkspaceTestGit(t, unrelatedRepo, "branch", branch, "HEAD")

	mgr := NewManager(openTestDB(t), t.TempDir())
	ws := &Workspace{
		ID:              "ws-cleanup-replaced-worktree",
		Platform:        "github",
		PlatformHost:    "github.com",
		RepoOwner:       "acme",
		RepoName:        "widget",
		ItemType:        db.WorkspaceItemTypePullRequest,
		ItemNumber:      99,
		GitHeadRef:      "feature/thing",
		WorkspaceBranch: branch,
		WorktreePath:    unrelatedRepo,
	}

	require.NoError(mgr.cleanupWorkspaceArtifactsForDelete(t.Context(), ws))

	exists, err := localBranchExists(t.Context(), unrelatedRepo, branch)
	require.NoError(err)
	assert.True(exists)
}

func TestFailSetupUsesSinglePersistenceBudget(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	wtDir := t.TempDir()

	repoID := seedRepo(
		t, d, "github.com", "acme", "widget",
	)
	seedMR(t, d, repoID, 42, "feature/thing")

	mgr := NewManager(d, wtDir)
	ws, err := mgr.Create(
		t.Context(), "github", "github.com", "acme", "widget", 42,
	)
	require.NoError(err)
	require.NotNil(ws)

	origTimeout := workspacePersistTimeout
	workspacePersistTimeout = 200 * time.Millisecond
	t.Cleanup(func() { workspacePersistTimeout = origTimeout })

	tx, err := d.WriteDB().BeginTx(t.Context(), nil)
	require.NoError(err)
	t.Cleanup(func() { _ = tx.Rollback() })

	start := time.Now()
	err = mgr.failSetup(
		t.Context(),
		ws.ID, workspaceSetupStageClone,
		errors.New("forced persistence timeout"),
	)
	elapsed := time.Since(start)

	require.Error(err)
	assert.Contains(err.Error(), "forced persistence timeout")
	assert.Less(
		elapsed,
		workspacePersistTimeout+(workspacePersistTimeout/2),
	)
}

func TestFailSetupRespectsParentDeadline(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	wtDir := t.TempDir()

	repoID := seedRepo(
		t, d, "github.com", "acme", "widget",
	)
	seedMR(t, d, repoID, 42, "feature/thing")

	mgr := NewManager(d, wtDir)
	ws, err := mgr.Create(
		t.Context(), "github", "github.com", "acme", "widget", 42,
	)
	require.NoError(err)
	require.NotNil(ws)

	origTimeout := workspacePersistTimeout
	workspacePersistTimeout = time.Second
	t.Cleanup(func() { workspacePersistTimeout = origTimeout })

	tx, err := d.WriteDB().BeginTx(t.Context(), nil)
	require.NoError(err)
	t.Cleanup(func() { _ = tx.Rollback() })

	parent, cancel := context.WithTimeout(
		t.Context(), 100*time.Millisecond,
	)
	defer cancel()

	start := time.Now()
	err = mgr.failSetup(
		parent,
		ws.ID, workspaceSetupStageClone,
		errors.New("forced persistence timeout"),
	)
	elapsed := time.Since(start)

	require.Error(err)
	assert.Contains(err.Error(), "forced persistence timeout")
	assert.Less(elapsed, 300*time.Millisecond)
}

func TestAddPreferredWorktreeRejectsUnsafeBranchName(t *testing.T) {
	require := require.New(t)

	cloneDir := setupBareCloneForWorkspaceGitTest(t)
	mgr := NewManager(openTestDB(t), t.TempDir())
	ws := &Workspace{
		ItemType:     db.WorkspaceItemTypePullRequest,
		ItemNumber:   42,
		GitHeadRef:   "-unsafe",
		WorktreePath: filepath.Join(t.TempDir(), "worktree"),
	}

	_, err := mgr.addPreferredWorktree(
		t.Context(), cloneDir, false, ws,
	)
	require.Error(err)
	require.Contains(err.Error(), "invalid branch name")
}

func TestValidateLocalBranchNameIgnoresBrokenWorkingTreeCwd(t *testing.T) {
	require := require.New(t)
	if os.Getenv("MIDDLEMAN_TEST_VALIDATE_BRANCH_CWD") == "1" {
		require.NoError(os.Chdir(os.Getenv("MIDDLEMAN_TEST_BROKEN_CWD")))
		require.NoError(validateLocalBranchName(
			t.Context(), "", "middleman/issue-23-federation-test",
		))
		return
	}

	brokenCwd := t.TempDir()
	require.NoError(os.WriteFile(
		filepath.Join(brokenCwd, ".git"),
		[]byte("gitdir: /definitely/not/a/git/worktree\n"),
		0o644,
	))

	cmd := procutil.Command(
		os.Args[0],
		"-test.run=^TestValidateLocalBranchNameIgnoresBrokenWorkingTreeCwd$",
	)
	cmd.Env = append(
		os.Environ(),
		"MIDDLEMAN_TEST_VALIDATE_BRANCH_CWD=1",
		"MIDDLEMAN_TEST_BROKEN_CWD="+brokenCwd,
	)
	out, err := cmd.CombinedOutput()
	require.NoError(err, string(out))
}

func TestAddWorktreeUsesFallbackWhenLocalBasePreferredBranchCheckedOut(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	const branch = "feature/thing"
	localRepo := setupLocalWorktreeBaseForWorkspaceGitTest(t, branch)
	existingWorktree := filepath.Join(t.TempDir(), "existing")
	runWorkspaceTestGit(
		t, localRepo, "worktree", "add", existingWorktree,
		"-b", branch, "refs/remotes/origin/"+branch,
	)
	mgr := NewManager(openTestDB(t), t.TempDir())
	ws := &Workspace{
		ItemType:     db.WorkspaceItemTypePullRequest,
		ItemNumber:   42,
		GitHeadRef:   branch,
		WorktreePath: filepath.Join(t.TempDir(), "worktree"),
	}

	gotBranch, err := mgr.addWorktreeLocked(t.Context(), localRepo, true, ws)

	require.NoError(err)
	assert.Equal(syntheticPRWorktreeBranch(42), gotBranch)
	headSHA, err := gitHeadSHA(t.Context(), ws.WorktreePath)
	require.NoError(err)
	originSHA := strings.TrimSpace(string(runWorkspaceTestGit(
		t, localRepo, "rev-parse", "refs/remotes/origin/"+branch,
	)))
	assert.Equal(originSHA, headSHA)
}

func TestAddWorktreeLocalBaseFetchesPullRefWhenHeadBranchDeleted(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	const branch = "feature/deleted"
	const prNumber = 43
	localRepo, remote, platformHost := setupHTTPWorktreeBaseForWorkspaceGitTest(
		t, branch,
	)
	wantSHA := strings.TrimSpace(string(runWorkspaceTestGit(
		t, localRepo, "rev-parse", "refs/remotes/origin/"+branch,
	)))
	runWorkspaceTestGit(
		t, remote, "update-ref",
		fmt.Sprintf("refs/pull/%d/head", prNumber), wantSHA,
	)
	runWorkspaceTestGit(t, remote, "update-ref", "-d", "refs/heads/"+branch)
	runWorkspaceTestGit(t, remote, "update-server-info")
	runWorkspaceTestGit(t, localRepo, "fetch", "--prune", "origin")
	_, exists, err := gitRefSHA(
		t.Context(), localRepo, "refs/remotes/origin/"+branch,
	)
	require.NoError(err)
	require.False(exists)
	_, exists, err = gitRefSHA(
		t.Context(), localRepo,
		fmt.Sprintf("refs/pull/%d/head", prNumber),
	)
	require.NoError(err)
	require.False(exists)

	mgr := NewManager(openTestDB(t), t.TempDir())
	ws := &Workspace{
		Platform:     "github",
		PlatformHost: platformHost,
		ItemType:     db.WorkspaceItemTypePullRequest,
		ItemNumber:   prNumber,
		GitHeadRef:   branch,
		WorktreePath: filepath.Join(t.TempDir(), "worktree"),
	}

	gotBranch, err := mgr.addWorktree(t.Context(), localRepo, true, ws)

	require.NoError(err)
	assert.Equal(syntheticPRWorktreeBranch(prNumber), gotBranch)
	headSHA, err := gitHeadSHA(t.Context(), ws.WorktreePath)
	require.NoError(err)
	assert.Equal(wantSHA, headSHA)
}

func TestAddWorktreeLocalBaseIgnoresStalePullRefWhenFetchFails(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	const branch = "feature/live"
	const prNumber = 44
	localRepo, _, _ := setupHTTPWorktreeBaseForWorkspaceGitTest(t, branch)
	originSHA := strings.TrimSpace(string(runWorkspaceTestGit(
		t, localRepo, "rev-parse", "refs/remotes/origin/"+branch,
	)))
	treeSHA := strings.TrimSpace(string(runWorkspaceTestGit(
		t, localRepo, "rev-parse", "main^{tree}",
	)))
	staleSHA := strings.TrimSpace(string(runWorkspaceTestGit(
		t, localRepo,
		"commit-tree", treeSHA,
		"-p", originSHA,
		"-m", "stale pull head",
	)))
	require.NotEqual(originSHA, staleSHA)
	runWorkspaceTestGit(
		t, localRepo, "update-ref",
		fmt.Sprintf("refs/pull/%d/head", prNumber), staleSHA,
	)
	existingWorktree := filepath.Join(t.TempDir(), "existing")
	runWorkspaceTestGit(
		t, localRepo, "worktree", "add", existingWorktree,
		"-b", branch, "refs/remotes/origin/"+branch,
	)
	mgr := NewManager(openTestDB(t), t.TempDir())
	ws := &Workspace{
		Platform:     "github",
		PlatformHost: "127.0.0.1",
		ItemType:     db.WorkspaceItemTypePullRequest,
		ItemNumber:   prNumber,
		GitHeadRef:   branch,
		WorktreePath: filepath.Join(t.TempDir(), "worktree"),
	}

	gotBranch, err := mgr.addWorktree(t.Context(), localRepo, true, ws)

	require.NoError(err)
	assert.Equal(syntheticPRWorktreeBranch(prNumber), gotBranch)
	headSHA, err := gitHeadSHA(t.Context(), ws.WorktreePath)
	require.NoError(err)
	assert.Equal(originSHA, headSHA)
}

func TestLocalBaseExistingPRBranchIsNotDeletedOnCleanup(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	const branch = "feature/thing"
	localRepo := setupLocalWorktreeBaseForWorkspaceGitTest(t, branch)
	runWorkspaceTestGit(t, localRepo, "branch", branch, "refs/remotes/origin/"+branch)
	mgr := NewManager(openTestDB(t), t.TempDir())
	ws := &Workspace{
		ID:              "ws-existing-local-pr-branch",
		Platform:        "github",
		PlatformHost:    "github.com",
		RepoOwner:       "acme",
		RepoName:        "widget",
		ItemType:        db.WorkspaceItemTypePullRequest,
		ItemNumber:      42,
		GitHeadRef:      branch,
		WorkspaceBranch: workspaceBranchUnknown,
		WorktreePath:    filepath.Join(t.TempDir(), "worktree"),
		TmuxSession:     "ws-existing-local-pr-branch",
		Status:          "ready",
	}

	managedBranch, err := mgr.addWorktreeLocked(t.Context(), localRepo, true, ws)
	require.NoError(err)
	require.Empty(managedBranch)
	ws.WorkspaceBranch = managedBranch

	require.NoError(mgr.cleanupWorkspaceArtifactsForDelete(t.Context(), ws))

	exists, err := localBranchExists(t.Context(), localRepo, branch)
	require.NoError(err)
	assert.True(exists)
}

func TestLocalBaseExistingPRBranchPreservesUpstream(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	const branch = "feature/thing"
	localRepo := setupLocalWorktreeBaseForWorkspaceGitTest(t, branch)
	runWorkspaceTestGit(t, localRepo, "branch", branch, "refs/remotes/origin/"+branch)
	runWorkspaceTestGit(t, localRepo, "config", "branch."+branch+".remote", "upstream")
	runWorkspaceTestGit(t, localRepo, "config", "branch."+branch+".merge", "refs/heads/main")
	mgr := NewManager(openTestDB(t), t.TempDir())
	ws := &Workspace{
		ID:              "ws-existing-local-pr-branch-upstream",
		Platform:        "github",
		PlatformHost:    "github.com",
		RepoOwner:       "acme",
		RepoName:        "widget",
		ItemType:        db.WorkspaceItemTypePullRequest,
		ItemNumber:      42,
		GitHeadRef:      branch,
		WorkspaceBranch: workspaceBranchUnknown,
		WorktreePath:    filepath.Join(t.TempDir(), "worktree"),
		TmuxSession:     "ws-existing-local-pr-branch-upstream",
		Status:          "ready",
	}

	managedBranch, err := mgr.addWorktreeLocked(t.Context(), localRepo, true, ws)

	require.NoError(err)
	assert.Empty(managedBranch)
	remote, err := gitConfigValue(t.Context(), localRepo, "branch."+branch+".remote")
	require.NoError(err)
	assert.Equal("upstream", remote)
	mergeRef, err := gitConfigValue(t.Context(), localRepo, "branch."+branch+".merge")
	require.NoError(err)
	assert.Equal("refs/heads/main", mergeRef)
}

func TestAddPreferredWorktreeHeadRepoRouting(t *testing.T) {
	type worktreeExpectation struct {
		headSHA  string
		remote   string
		mergeRef string
	}

	tests := []struct {
		name        string
		number      int
		headBranch  string
		headRepoURL string
		configure   func(*testing.T, string, string, int) worktreeExpectation
	}{
		{
			name:        "same-repo PR tracks real remote branch",
			number:      244,
			headBranch:  "feature/thing",
			headRepoURL: "https://github.com/acme/widget.git",
			configure: func(
				t *testing.T, cloneDir, branch string, prNumber int,
			) worktreeExpectation {
				// Reproduce the dangerous repo state from issue #256: the real
				// branch and GitHub's synthetic pull ref both exist and point at
				// the same commit. Starting from refs/pull/<number>/head lets Git
				// auto-configure that synthetic ref as the upstream, which breaks
				// tools that inspect @{u}.
				sha := configureSameRepoPRRefs(
					t, cloneDir, branch, prNumber,
				)
				return worktreeExpectation{
					headSHA:  sha,
					remote:   "origin",
					mergeRef: "refs/heads/" + branch,
				}
			},
		},
		{
			name:        "fork PR prefers pull ref over same-named origin branch",
			number:      245,
			headBranch:  "fork/thing",
			headRepoURL: "https://github.com/contributor/widget.git",
			configure: func(
				t *testing.T, cloneDir, branch string, prNumber int,
			) worktreeExpectation {
				// A base repo can have a branch with the same name as a fork PR
				// branch, but that origin branch is not the fork head. Fork
				// workspaces must prefer the GitHub pull ref over any same-named
				// origin branch.
				originSHA, pullSHA := configureForkPRRefs(
					t, cloneDir, branch, prNumber,
				)
				gotOriginSHA, exists, err := gitRefSHA(
					t.Context(), cloneDir, "refs/remotes/origin/"+branch,
				)
				require.NoError(t, err)
				require.True(t, exists)
				require.NotEqual(t, originSHA, pullSHA)
				require.Equal(t, originSHA, gotOriginSHA)
				return worktreeExpectation{headSHA: pullSHA}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert := Assert.New(t)
			require := require.New(t)
			cloneDir := setupBareCloneForWorkspaceGitTest(t)
			want := tt.configure(t, cloneDir, tt.headBranch, tt.number)

			d := openTestDB(t)
			repoID := seedRepo(t, d, "github.com", "acme", "widget")
			seedMRWithHeadRepo(
				t, d, repoID, tt.number, tt.headBranch, tt.headRepoURL,
			)
			mgr := NewManager(d, t.TempDir())
			ws, err := mgr.Create(
				t.Context(), "github", "github.com", "acme", "widget", tt.number,
			)
			require.NoError(err)

			branch, err := mgr.addPreferredWorktree(t.Context(), cloneDir, false, ws)
			require.NoError(err)
			assert.Equal(tt.headBranch, branch)

			headSHA, err := gitHeadSHA(t.Context(), ws.WorktreePath)
			require.NoError(err)
			assert.Equal(want.headSHA, headSHA)

			if want.remote == "" && want.mergeRef == "" {
				return
			}
			remote, err := gitConfigValue(
				t.Context(), ws.WorktreePath,
				"branch."+tt.headBranch+".remote",
			)
			require.NoError(err)
			mergeRef, err := gitConfigValue(
				t.Context(), ws.WorktreePath,
				"branch."+tt.headBranch+".merge",
			)
			require.NoError(err)
			assert.Equal(want.remote, remote)
			assert.Equal(want.mergeRef, mergeRef)
		})
	}
}

func TestAddWorktreeGitLabForkMRFetchesHeadBeforePreferredBranch(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	cloneDir := setupBareCloneForWorkspaceGitTest(t)
	mrNumber := 547
	headBranch := "contributor/gitlab-fork"
	treeSHA := strings.TrimSpace(string(runWorkspaceTestGit(
		t, cloneDir, "rev-parse", "main^{tree}",
	)))
	headSHA := strings.TrimSpace(string(runWorkspaceTestGit(
		t, cloneDir, "commit-tree", treeSHA, "-p", "main",
		"-m", "remote fork mr head",
	)))
	runWorkspaceTestGit(
		t, cloneDir, "push", "origin",
		fmt.Sprintf("%s:refs/merge-requests/%d/head", headSHA, mrNumber),
	)
	headRepo := "https://gitlab.com/contributor/widget.git"
	ws := &Workspace{
		ID:              "ws-gitlab-fork-mr-preferred",
		Platform:        "gitlab",
		PlatformHost:    "gitlab.com",
		RepoOwner:       "acme",
		RepoName:        "widget",
		ItemType:        db.WorkspaceItemTypePullRequest,
		ItemNumber:      mrNumber,
		GitHeadRef:      headBranch,
		MRHeadRepo:      &headRepo,
		WorkspaceBranch: workspaceBranchUnknown,
		WorktreePath:    filepath.Join(t.TempDir(), "worktree"),
		TmuxSession:     "ws-gitlab-fork-mr-preferred",
		Status:          "creating",
	}
	mgr := NewManager(openTestDB(t), t.TempDir())

	branch, err := mgr.addWorktreeLocked(t.Context(), cloneDir, false, ws)

	require.NoError(err)
	assert.Equal(headBranch, branch)
	gotSHA, err := gitHeadSHA(t.Context(), ws.WorktreePath)
	require.NoError(err)
	assert.Equal(headSHA, gotSHA)
}

func TestAddWorktreeMergedSameRepoPRUsesPullRefWhenHeadBranchDeleted(
	t *testing.T,
) {
	assert := Assert.New(t)
	require := require.New(t)

	cloneDir := setupBareCloneForWorkspaceGitTest(t)
	prNumber := 545
	headBranch := "codex/wildcard-promote-local-clones"
	headSHA := configureSameRepoPRRefs(t, cloneDir, headBranch, prNumber)
	runWorkspaceTestGit(
		t, cloneDir, "push", "origin",
		fmt.Sprintf("%s:refs/pull/%d/head", headSHA, prNumber),
	)
	runWorkspaceTestGit(
		t, cloneDir, "update-ref", "-d",
		"refs/remotes/origin/"+headBranch,
	)

	ws := &Workspace{
		ID:              "ws-merged-same-repo-pr",
		Platform:        "github",
		PlatformHost:    "github.com",
		RepoOwner:       "middleman",
		RepoName:        "middleman",
		ItemType:        db.WorkspaceItemTypePullRequest,
		ItemNumber:      prNumber,
		GitHeadRef:      headBranch,
		WorkspaceBranch: workspaceBranchUnknown,
		WorktreePath:    filepath.Join(t.TempDir(), "worktree"),
		TmuxSession:     "ws-merged-same-repo-pr",
		Status:          "creating",
	}
	mgr := NewManager(openTestDB(t), t.TempDir())

	branch, err := mgr.addWorktreeLocked(t.Context(), cloneDir, false, ws)

	require.NoError(err)
	assert.Equal(syntheticPRWorktreeBranch(prNumber), branch)
	gotSHA, err := gitHeadSHA(t.Context(), ws.WorktreePath)
	require.NoError(err)
	assert.Equal(headSHA, gotSHA)
}

func TestAddWorktreeGitLabMRUsesMergeRequestRefWhenHeadBranchDeleted(
	t *testing.T,
) {
	assert := Assert.New(t)
	require := require.New(t)

	cloneDir := setupBareCloneForWorkspaceGitTest(t)
	mrNumber := 546
	headBranch := "feature/gitlab-merged"
	headSHA := configureGitLabMRRefs(t, cloneDir, headBranch, mrNumber)
	runWorkspaceTestGit(
		t, cloneDir, "push", "origin",
		fmt.Sprintf("%s:refs/merge-requests/%d/head", headSHA, mrNumber),
	)
	runWorkspaceTestGit(
		t, cloneDir, "update-ref", "-d",
		"refs/remotes/origin/"+headBranch,
	)

	ws := &Workspace{
		ID:              "ws-merged-gitlab-mr",
		Platform:        "gitlab",
		PlatformHost:    "gitlab.com",
		RepoOwner:       "middleman",
		RepoName:        "middleman",
		ItemType:        db.WorkspaceItemTypePullRequest,
		ItemNumber:      mrNumber,
		GitHeadRef:      headBranch,
		WorkspaceBranch: workspaceBranchUnknown,
		WorktreePath:    filepath.Join(t.TempDir(), "worktree"),
		TmuxSession:     "ws-merged-gitlab-mr",
		Status:          "creating",
	}
	mgr := NewManager(openTestDB(t), t.TempDir())

	branch, err := mgr.addWorktreeLocked(t.Context(), cloneDir, false, ws)

	require.NoError(err)
	assert.Equal(syntheticPRWorktreeBranch(mrNumber), branch)
	gotSHA, err := gitHeadSHA(t.Context(), ws.WorktreePath)
	require.NoError(err)
	assert.Equal(headSHA, gotSHA)
}

func TestAddWorktreeGitLabMRFetchesSpecificMergeRequestRef(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	cloneDir := setupBareCloneForWorkspaceGitTest(t)
	mrNumber := 546
	headBranch := "feature/gitlab-merged"
	treeSHA := strings.TrimSpace(string(runWorkspaceTestGit(
		t, cloneDir, "rev-parse", "main^{tree}",
	)))
	headSHA := strings.TrimSpace(string(runWorkspaceTestGit(
		t, cloneDir, "commit-tree", treeSHA, "-p", "main",
		"-m", "remote mr head",
	)))
	runWorkspaceTestGit(
		t, cloneDir, "push", "origin",
		fmt.Sprintf("%s:refs/merge-requests/%d/head", headSHA, mrNumber),
	)

	ws := &Workspace{
		ID:              "ws-merged-gitlab-mr-specific-fetch",
		Platform:        "gitlab",
		PlatformHost:    "gitlab.com",
		RepoOwner:       "middleman",
		RepoName:        "middleman",
		ItemType:        db.WorkspaceItemTypePullRequest,
		ItemNumber:      mrNumber,
		GitHeadRef:      headBranch,
		WorkspaceBranch: workspaceBranchUnknown,
		WorktreePath:    filepath.Join(t.TempDir(), "worktree"),
		TmuxSession:     "ws-merged-gitlab-mr-specific-fetch",
		Status:          "creating",
	}
	mgr := NewManager(openTestDB(t), t.TempDir())

	branch, err := mgr.addWorktreeLocked(t.Context(), cloneDir, false, ws)

	require.NoError(err)
	assert.Equal(syntheticPRWorktreeBranch(mrNumber), branch)
	gotSHA, err := gitHeadSHA(t.Context(), ws.WorktreePath)
	require.NoError(err)
	assert.Equal(headSHA, gotSHA)
	gotRef := strings.TrimSpace(string(runWorkspaceTestGit(
		t, cloneDir, "rev-parse",
		fmt.Sprintf("refs/merge-requests/%d/head", mrNumber),
	)))
	assert.Equal(headSHA, gotRef)
}

func TestRollbackWorktreeDeletesBranchWhenContextCanceled(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	cloneDir := setupBareCloneForWorkspaceGitTest(t)
	branch := syntheticPRWorktreeBranch(42)
	require.NoError(runGitWithoutHooks(
		t.Context(), cloneDir,
		"branch", branch, "main",
	))

	ws := &Workspace{
		ItemType:     db.WorkspaceItemTypePullRequest,
		ItemNumber:   42,
		WorktreePath: filepath.Join(t.TempDir(), "missing-worktree"),
	}
	mgr := NewManager(openTestDB(t), t.TempDir())

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	mgr.rollbackWorktree(ctx, cloneDir, ws, workspaceBranchUnknown)

	_, exists, err := gitRefSHA(
		t.Context(), cloneDir, "refs/heads/"+branch,
	)
	require.NoError(err)
	assert.False(exists)
}

func TestLocalBranchExistsIgnoresInheritedGitEnv(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	targetClone := setupBareCloneForWorkspaceGitTest(t)
	poisonClone := setupBareCloneForWorkspaceGitTest(t)
	require.NoError(runGitWithoutHooks(
		context.Background(), poisonClone,
		"branch", "middleman/issue-7", "main",
	))

	t.Setenv("GIT_DIR", poisonClone)
	t.Setenv("GIT_WORK_TREE", t.TempDir())

	exists, err := localBranchExists(
		context.Background(), targetClone, "middleman/issue-7",
	)

	require.NoError(err)
	assert.False(exists)
}

func TestCleanupContextRespectsParentDeadline(t *testing.T) {
	require := require.New(t)

	parent, cancel := context.WithTimeout(
		t.Context(), 100*time.Millisecond,
	)
	defer cancel()

	cleanupCtx, cleanupCancel := cleanupContext(parent)
	defer cleanupCancel()

	deadline, ok := cleanupCtx.Deadline()
	require.True(ok)

	remaining := time.Until(deadline)
	require.LessOrEqual(remaining, 100*time.Millisecond)
	require.Greater(remaining, 0*time.Millisecond)
}

func setupBareCloneForWorkspaceGitTest(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	remote := filepath.Join(dir, "remote.git")
	work := filepath.Join(dir, "work")
	cloneDir := filepath.Join(dir, "clone.git")

	runWorkspaceTestGit(
		t, dir, "init", "--bare", "--initial-branch=main", remote,
	)
	runWorkspaceTestGit(t, dir, "clone", remote, work)
	runWorkspaceTestGit(
		t, work, "config", "user.email", "test@test.com",
	)
	runWorkspaceTestGit(
		t, work, "config", "user.name", "Test",
	)
	require.NoError(t, os.WriteFile(
		filepath.Join(work, "base.txt"), []byte("base\n"), 0o644,
	))
	runWorkspaceTestGit(t, work, "add", ".")
	runWorkspaceTestGit(t, work, "commit", "-m", "base commit")
	runWorkspaceTestGit(t, work, "push", "origin", "main")
	runWorkspaceTestGit(t, dir, "clone", "--bare", remote, cloneDir)
	runWorkspaceTestGit(t, cloneDir, "config", "user.email", "test@test.com")
	runWorkspaceTestGit(t, cloneDir, "config", "user.name", "Test")

	return cloneDir
}

func seedWorkspaceBareCloneAt(t *testing.T, cloneDir string) {
	t.Helper()
	dir := t.TempDir()
	remote := filepath.Join(dir, "remote.git")
	work := filepath.Join(dir, "work")

	runWorkspaceTestGit(
		t, dir, "init", "--bare", "--initial-branch=main", remote,
	)
	runWorkspaceTestGit(t, dir, "clone", remote, work)
	runWorkspaceTestGit(
		t, work, "config", "user.email", "test@test.com",
	)
	runWorkspaceTestGit(
		t, work, "config", "user.name", "Test",
	)
	require.NoError(t, os.WriteFile(
		filepath.Join(work, "base.txt"), []byte("base\n"), 0o644,
	))
	runWorkspaceTestGit(t, work, "add", ".")
	runWorkspaceTestGit(t, work, "commit", "-m", "base commit")
	runWorkspaceTestGit(t, work, "push", "origin", "main")
	require.NoError(t, os.MkdirAll(filepath.Dir(cloneDir), 0o755))
	runWorkspaceTestGit(t, dir, "clone", "--bare", remote, cloneDir)
	runWorkspaceTestGit(t, cloneDir, "config", "user.email", "test@test.com")
	runWorkspaceTestGit(t, cloneDir, "config", "user.name", "Test")
}

func configureOriginHeadForIssueWorkspace(t *testing.T, cloneDir string) {
	t.Helper()
	out, err := gitOutput(t.Context(), cloneDir, "rev-parse", "main")
	require.NoError(t, err)
	sha := strings.TrimSpace(out)
	require.NotEmpty(t, sha)
	runWorkspaceTestGit(
		t, cloneDir, "update-ref", "refs/remotes/origin/main", sha,
	)
	runWorkspaceTestGit(
		t, cloneDir, "symbolic-ref",
		"refs/remotes/origin/HEAD", "refs/remotes/origin/main",
	)
}

func setupLocalWorktreeBaseForWorkspaceGitTest(
	t *testing.T, branch string,
) string {
	t.Helper()
	repo, _ := setupLocalWorktreeBaseWithRemoteForWorkspaceGitTest(t, branch)
	return repo
}

func setupLocalWorktreeBaseWithRemoteForWorkspaceGitTest(
	t *testing.T, branch string,
) (string, string) {
	t.Helper()
	dir := t.TempDir()
	remote := filepath.Join(dir, "remote.git")
	repo := filepath.Join(dir, "repo")
	runWorkspaceTestGit(
		t, dir, "init", "--bare", "--initial-branch=main", remote,
	)
	runWorkspaceTestGit(t, dir, "init", "--initial-branch=main", repo)
	runWorkspaceTestGit(t, repo, "config", "user.email", "test@test.com")
	runWorkspaceTestGit(t, repo, "config", "user.name", "Test")
	runWorkspaceTestGit(
		t, repo, "remote", "add", "origin",
		remote,
	)
	require.NoError(t, os.WriteFile(
		filepath.Join(repo, "base.txt"), []byte("base\n"), 0o644,
	))
	runWorkspaceTestGit(t, repo, "add", ".")
	runWorkspaceTestGit(t, repo, "commit", "-m", "base commit")
	runWorkspaceTestGit(t, repo, "push", "origin", "HEAD:refs/heads/main")
	runWorkspaceTestGit(t, remote, "symbolic-ref", "HEAD", "refs/heads/main")
	runWorkspaceTestGit(t, repo, "push", "origin", "HEAD:refs/heads/"+branch)
	runWorkspaceTestGit(
		t, repo, "remote", "set-url", "origin",
		"https://github.com/acme/widget.git",
	)
	runWorkspaceTestGit(
		t, repo, "update-ref", "refs/remotes/origin/"+branch, "HEAD",
	)
	runWorkspaceTestGit(
		t, repo, "symbolic-ref",
		"refs/remotes/origin/HEAD", "refs/remotes/origin/main",
	)
	return repo, remote
}

func setupHTTPWorktreeBaseForWorkspaceGitTest(
	t *testing.T, branch string,
) (repo, remote, platformHost string) {
	t.Helper()
	root := t.TempDir()
	remote = filepath.Join(root, "acme", "widget.git")
	repo = filepath.Join(root, "repo")
	require.NoError(t, os.MkdirAll(filepath.Dir(remote), 0o755))
	runWorkspaceTestGit(
		t, root, "init", "--bare", "--initial-branch=main", remote,
	)
	server := httptest.NewServer(http.FileServer(http.Dir(root)))
	t.Cleanup(server.Close)
	remoteURL := server.URL + "/acme/widget.git"
	parsed, err := url.Parse(server.URL)
	require.NoError(t, err)
	platformHost = parsed.Host

	runWorkspaceTestGit(t, root, "init", "--initial-branch=main", repo)
	runWorkspaceTestGit(t, repo, "config", "user.email", "test@test.com")
	runWorkspaceTestGit(t, repo, "config", "user.name", "Test")
	runWorkspaceTestGit(t, repo, "remote", "add", "origin", remote)
	require.NoError(t, os.WriteFile(
		filepath.Join(repo, "base.txt"), []byte("base\n"), 0o644,
	))
	runWorkspaceTestGit(t, repo, "add", ".")
	runWorkspaceTestGit(t, repo, "commit", "-m", "base commit")
	runWorkspaceTestGit(t, repo, "push", "origin", "HEAD:refs/heads/main")
	runWorkspaceTestGit(t, remote, "symbolic-ref", "HEAD", "refs/heads/main")
	runWorkspaceTestGit(t, repo, "push", "origin", "HEAD:refs/heads/"+branch)
	runWorkspaceTestGit(t, remote, "update-server-info")
	runWorkspaceTestGit(t, repo, "remote", "set-url", "origin", remoteURL)
	runWorkspaceTestGit(t, repo, "fetch", "--prune", "origin")
	runWorkspaceTestGit(
		t, repo, "symbolic-ref",
		"refs/remotes/origin/HEAD", "refs/remotes/origin/main",
	)
	return repo, remote, platformHost
}

func setupRemoteForForkPRWorktreeTest(
	t *testing.T, branch string, prNumber int,
) (remote, pullSHA string) {
	t.Helper()
	dir := t.TempDir()
	work := filepath.Join(dir, "work")
	remote = filepath.Join(dir, "remote.git")
	runWorkspaceTestGit(t, dir, "init", "--initial-branch=main", work)
	runWorkspaceTestGit(t, work, "config", "user.email", "test@test.com")
	runWorkspaceTestGit(t, work, "config", "user.name", "Test")
	require.NoError(t, os.WriteFile(
		filepath.Join(work, "base.txt"), []byte("base\n"), 0o644,
	))
	runWorkspaceTestGit(t, work, "add", ".")
	runWorkspaceTestGit(t, work, "commit", "-m", "base commit")
	runWorkspaceTestGit(t, dir, "init", "--bare", "--initial-branch=main", remote)
	runWorkspaceTestGit(t, work, "remote", "add", "origin", remote)
	runWorkspaceTestGit(t, work, "push", "origin", "main")

	originSHA := strings.TrimSpace(string(runWorkspaceTestGit(
		t, work, "rev-parse", "HEAD",
	)))
	require.NoError(t, os.WriteFile(
		filepath.Join(work, "fork.txt"), []byte("fork\n"), 0o644,
	))
	runWorkspaceTestGit(t, work, "add", ".")
	runWorkspaceTestGit(t, work, "commit", "-m", "fork head")
	pullSHA = strings.TrimSpace(string(runWorkspaceTestGit(
		t, work, "rev-parse", "HEAD",
	)))
	require.NotEqual(t, originSHA, pullSHA)
	runWorkspaceTestGit(
		t, work, "push", "origin",
		originSHA+":refs/heads/"+branch,
		pullSHA+":refs/pull/"+strconv.Itoa(prNumber)+"/head",
	)
	return remote, pullSHA
}

func configureSameRepoPRRefs(
	t *testing.T, cloneDir, branch string, prNumber int,
) string {
	t.Helper()
	out, err := gitOutput(t.Context(), cloneDir, "rev-parse", "main")
	require.NoError(t, err)
	sha := strings.TrimSpace(out)
	require.NotEmpty(t, sha)
	runWorkspaceTestGit(
		t, cloneDir, "update-ref", "refs/remotes/origin/"+branch, sha,
	)
	runWorkspaceTestGit(
		t, cloneDir, "update-ref",
		fmt.Sprintf("refs/pull/%d/head", prNumber), sha,
	)
	return sha
}

func configureGitLabMRRefs(
	t *testing.T, cloneDir, branch string, mrNumber int,
) string {
	t.Helper()
	out, err := gitOutput(t.Context(), cloneDir, "rev-parse", "main")
	require.NoError(t, err)
	sha := strings.TrimSpace(out)
	require.NotEmpty(t, sha)
	runWorkspaceTestGit(
		t, cloneDir, "update-ref", "refs/remotes/origin/"+branch, sha,
	)
	runWorkspaceTestGit(
		t, cloneDir, "update-ref",
		fmt.Sprintf("refs/merge-requests/%d/head", mrNumber), sha,
	)
	return sha
}

func configureForkPRRefs(
	t *testing.T, cloneDir, branch string, prNumber int,
) (originSHA, pullSHA string) {
	t.Helper()
	out, err := gitOutput(t.Context(), cloneDir, "rev-parse", "main")
	require.NoError(t, err)
	originSHA = strings.TrimSpace(out)
	require.NotEmpty(t, originSHA)
	treeOut, err := gitOutput(t.Context(), cloneDir, "rev-parse", "main^{tree}")
	require.NoError(t, err)
	runWorkspaceTestGit(t, cloneDir, "config", "user.email", "test@test.com")
	runWorkspaceTestGit(t, cloneDir, "config", "user.name", "Test")
	commitOut, err := gitOutput(
		t.Context(), cloneDir,
		"commit-tree", strings.TrimSpace(treeOut),
		"-p", originSHA, "-m", "fork head",
	)
	require.NoError(t, err)
	pullSHA = strings.TrimSpace(commitOut)
	require.NotEmpty(t, pullSHA)
	runWorkspaceTestGit(
		t, cloneDir, "update-ref", "refs/remotes/origin/"+branch, originSHA,
	)
	runWorkspaceTestGit(
		t, cloneDir, "update-ref",
		fmt.Sprintf("refs/pull/%d/head", prNumber), pullSHA,
	)
	return originSHA, pullSHA
}

func runWorkspaceTestGit(t *testing.T, dir string, args ...string) []byte {
	t.Helper()
	out, stderr, err := gitcmd.New().Run(t.Context(), dir, nil, args...)
	require.NoError(t, err, "git %v failed: %s%s", args, out, stderr)
	return out
}

func TestShellFromPasswdLine(t *testing.T) {
	tests := []struct {
		name string
		line string
		want string
	}{
		{
			"normal zsh",
			"wesm:x:501:20:Wes McKinney:/Users/wesm:/bin/zsh",
			"/bin/zsh",
		},
		{
			"normal bash",
			"dev:x:1000:1000::/home/dev:/bin/bash",
			"/bin/bash",
		},
		{
			"nologin filtered",
			"nobody:x:65534:65534:Nobody:/nonexistent:/sbin/nologin",
			"",
		},
		{
			"false filtered",
			"git:x:998:998::/home/git:/usr/bin/false",
			"",
		},
		{
			"bin/false filtered",
			"svc:x:999:999::/srv:/bin/false",
			"",
		},
		{
			"empty shell",
			"user:x:1000:1000::/home/user:",
			"",
		},
		{
			"too few fields",
			"broken:line",
			"",
		},
		{
			"empty line",
			"",
			"",
		},
		{
			"trailing whitespace",
			"user:x:1000:1000::/home/user:/bin/zsh\n",
			"/bin/zsh",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shellFromPasswdLine(tt.line)
			require.Equal(t, tt.want, got)
		})
	}
}

// writeRecorderScript creates an executable shell script at a
// fresh path under t.TempDir() that appends the count and each
// argument, NUL-delimited, to TMUX_RECORD. Returns the script path
// and the record file path.
func writeRecorderScript(t *testing.T) (scriptPath, recordPath string) {
	t.Helper()
	dir := t.TempDir()
	recordPath = filepath.Join(dir, "record")
	scriptPath = filepath.Join(dir, "fake-tmux")
	body := "#!/bin/sh\n" +
		`printf '%s\0' "$#" "$@" >> "$TMUX_RECORD"` + "\n" +
		"exit 0\n"
	require.NoError(t, os.WriteFile(scriptPath, []byte(body), 0o755))
	t.Setenv("TMUX_RECORD", recordPath)
	return scriptPath, recordPath
}

// readRecorderArgv reads the NUL-delimited record file and returns
// each recorded invocation as a []string. Each invocation is stored
// as "<argc>\0<arg0>\0<arg1>...\0", so this reads argc then slurps
// that many args per invocation.
func readRecorderArgv(t *testing.T, path string) [][]string {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	// Split on NUL. Each record is "<argc>\0<arg0>\0<arg1>\0...\0",
	// so the flushed stream always ends with a trailing \0 and Split
	// produces a final empty element after it. Strip exactly one
	// trailing empty so we don't mistake it for part of the next
	// record. Interior empty elements are real args (the NUL framing
	// exists to preserve them) and must NOT be skipped.
	parts := strings.Split(string(data), "\x00")
	if len(parts) > 0 && parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	var out [][]string
	for i := 0; i < len(parts); {
		n, err := strconv.Atoi(parts[i])
		require.NoError(t, err)
		i++
		argv := parts[i : i+n]
		for j := range argv {
			argv[j] = normalizeRecordedTmuxArg(argv[j])
		}
		out = append(out, argv)
		i += n
	}
	return out
}

func normalizeRecordedTmuxArg(arg string) string {
	if runtime.GOOS != "windows" {
		return arg
	}
	switch arg {
	case "#session_name":
		return "#{session_name}"
	case "#pane_title":
		return "#{pane_title}"
	default:
		return arg
	}
}

func TestManagerEnsureTmuxHasSessionPrefix(t *testing.T) {
	assert := Assert.New(t)

	script, record := writeRecorderScript(t)

	d := openTestDB(t)
	mgr := NewManager(d, t.TempDir())
	mgr.SetTmuxCommand([]string{script, "wrap"})

	// Script exits 0 for every invocation, so EnsureTmux observes
	// "session exists" after the has-session call and returns
	// without running new-session.
	require.NoError(t, mgr.EnsureTmux(t.Context(), "sess-A", t.TempDir()))

	argvs := readRecorderArgv(t, record)
	require.Len(t, argvs, 1)
	assert.Equal(
		[]string{"wrap", "has-session", "-t", "sess-A"},
		argvs[0],
	)
}

func TestManagerEnsureTerminalUsesPtyOwnerWhenConfigured(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	script, record := writeRecorderScript(t)
	owner := &fakePtyOwnerClient{}
	mgr := NewManager(openTestDB(t), t.TempDir())
	mgr.SetTmuxCommand([]string{script, "wrap"})
	mgr.SetPtyOwnerClient(owner)

	require.NoError(mgr.EnsureTerminal(t.Context(), &db.Workspace{
		TmuxSession:     "sess-owner",
		WorktreePath:    "/tmp/ws",
		TerminalBackend: TerminalBackendPtyOwner,
	}))

	assert.Equal([]fakePtyOwnerCall{{
		Op: "ensure", Session: "sess-owner", Cwd: "/tmp/ws",
	}}, owner.Calls)
	_, err := os.Stat(record)
	assert.True(os.IsNotExist(err))
}

func TestManagerTerminalPaneSnapshotIncludesPtyOwnerTitle(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)

	owner := &fakePtyOwnerClient{
		SnapshotOutput: []byte("recent output"),
		SnapshotTitle:  "⠴ t3code-b5014b03",
	}
	mgr := NewManager(nil, t.TempDir())
	mgr.SetPtyOwnerClient(owner)
	ws := &db.Workspace{
		ID:              "ws-1",
		TmuxSession:     "middleman-ws-1",
		TerminalBackend: TerminalBackendPtyOwner,
	}

	snapshot, err := mgr.TerminalPaneSnapshot(
		context.Background(), ws, ws.TmuxSession,
	)

	require.NoError(err)
	assert.Equal("⠴ t3code-b5014b03", snapshot.Title)
	assert.Equal("recent output", snapshot.Output)
}

func TestManagerCleanupTerminalUsesPtyOwnerForBaseSession(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	script, record := writeRecorderScript(t)
	owner := &fakePtyOwnerClient{}
	d := openTestDB(t)
	repoID := seedRepo(t, d, "github.com", "acme", "widget")
	seedMR(t, d, repoID, 42, "feature/thing")
	mgr := NewManager(d, t.TempDir())
	mgr.SetTmuxCommand([]string{script, "wrap"})
	mgr.SetPtyOwnerClient(owner)

	ws, err := mgr.Create(t.Context(), "github", "github.com", "acme", "widget", 42)
	require.NoError(err)
	_, err = mgr.Delete(t.Context(), ws.ID, true, nil)
	require.NoError(err)

	assert.Equal([]fakePtyOwnerCall{{
		Op: "stop", Session: ws.TmuxSession,
	}}, owner.Calls)
	_, err = os.Stat(record)
	assert.True(os.IsNotExist(err))
}

func TestManagerCleanupPtyOwnerWorkspaceStopsStoredRuntimeTmuxSessions(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	script, record := writeRecorderScript(t)
	owner := &fakePtyOwnerClient{}
	d := openTestDB(t)
	repoID := seedRepo(t, d, "github.com", "acme", "widget")
	seedMR(t, d, repoID, 42, "feature/thing")
	mgr := NewManager(d, t.TempDir())
	mgr.SetTmuxCommand([]string{script, "wrap"})
	mgr.SetPtyOwnerClient(owner)

	ws, err := mgr.Create(t.Context(), "github", "github.com", "acme", "widget", 42)
	require.NoError(err)
	recordRuntimeTmuxSessionForTest(
		t, d, ws.ID, "ws-runtime-session", "agent-1",
		"middleman-runtime-session",
		time.Date(2026, 4, 29, 1, 0, 0, 0, time.UTC),
	)

	_, err = mgr.Delete(t.Context(), ws.ID, true, nil)
	require.NoError(err)

	assert.Equal([]fakePtyOwnerCall{{
		Op: "stop", Session: ws.TmuxSession,
	}}, owner.Calls)
	argvs := readRecorderArgv(t, record)
	require.Len(argvs, 1)
	assert.Equal(
		[]string{"wrap", "kill-session", "-t", "middleman-runtime-session"},
		argvs[0],
	)
	stored, err := d.ListWorkspaceRuntimeTmuxSessions(t.Context(), ws.ID)
	require.NoError(err)
	assert.Empty(stored)
}

func TestManagerDeleteUsesTmuxPrefix(t *testing.T) {
	assert := Assert.New(t)

	script, record := writeRecorderScript(t)

	d := openTestDB(t)
	repoID := seedRepo(t, d, "github.com", "acme", "widget")
	seedMR(t, d, repoID, 42, "feature/thing")

	mgr := NewManager(d, t.TempDir())
	mgr.SetTmuxCommand([]string{script, "wrap"})

	ctx := t.Context()
	ws, err := mgr.Create(ctx, "github", "github.com", "acme", "widget", 42)
	require.NoError(t, err)

	// force=true skips the dirty-files check. m.clones is nil, so
	// Delete takes the clones==nil short-circuit after killing the
	// tmux session — no git operations are required.
	_, err = mgr.Delete(ctx, ws.ID, true, nil)
	require.NoError(t, err)

	// Delete invokes exactly one tmux command on this path
	// (kill-session). It ignores the exit code because the session
	// may not exist, but our script exits 0 so the invocation is
	// still recorded.
	argvs := readRecorderArgv(t, record)
	require.Len(t, argvs, 1)
	assert.Equal(
		[]string{"wrap", "kill-session", "-t", ws.TmuxSession},
		argvs[0],
	)
}

func TestManagerDeleteAllowsMissingTmuxSession(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	dir := t.TempDir()
	record := filepath.Join(dir, "record")
	script := filepath.Join(dir, "fake-tmux")
	body := "#!/bin/sh\n" +
		`printf '%s\0' "$#" "$@" >> "$TMUX_RECORD"` + "\n" +
		`for a in "$@"; do` + "\n" +
		`  if [ "$a" = "kill-session" ]; then` + "\n" +
		`    echo "can't find session: missing" >&2` + "\n" +
		`    exit 1` + "\n" +
		`  fi` + "\n" +
		"done\n" +
		"exit 0\n"
	require.NoError(os.WriteFile(script, []byte(body), 0o755))
	t.Setenv("TMUX_RECORD", record)

	d := openTestDB(t)
	repoID := seedRepo(t, d, "github.com", "acme", "widget")
	seedMR(t, d, repoID, 42, "feature/thing")

	mgr := NewManager(d, t.TempDir())
	mgr.SetTmuxCommand([]string{script, "wrap"})

	ctx := context.Background()
	ws, err := mgr.Create(ctx, "github", "github.com", "acme", "widget", 42)
	require.NoError(err)

	dirty, err := mgr.Delete(ctx, ws.ID, true, nil)
	require.NoError(err)
	assert.Nil(dirty)

	got, err := mgr.Get(ctx, ws.ID)
	require.NoError(err)
	assert.Nil(got)

	argvs := readRecorderArgv(t, record)
	require.Len(argvs, 1)
	assert.Equal(
		[]string{"wrap", "kill-session", "-t", ws.TmuxSession},
		argvs[0],
	)
}

func TestManagerDeleteFailsWhenTmuxKillFails(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	dir := t.TempDir()
	record := filepath.Join(dir, "record")
	script := filepath.Join(dir, "fake-tmux")
	body := "#!/bin/sh\n" +
		`printf '%s\0' "$#" "$@" >> "$TMUX_RECORD"` + "\n" +
		`for a in "$@"; do` + "\n" +
		`  if [ "$a" = "kill-session" ]; then` + "\n" +
		`    echo "permission denied" >&2` + "\n" +
		`    exit 1` + "\n" +
		`  fi` + "\n" +
		"done\n" +
		"exit 0\n"
	require.NoError(os.WriteFile(script, []byte(body), 0o755))
	t.Setenv("TMUX_RECORD", record)

	d := openTestDB(t)
	repoID := seedRepo(t, d, "github.com", "acme", "widget")
	seedMR(t, d, repoID, 42, "feature/thing")

	mgr := NewManager(d, t.TempDir())
	mgr.SetTmuxCommand([]string{script, "wrap"})

	ctx := context.Background()
	ws, err := mgr.Create(ctx, "github", "github.com", "acme", "widget", 42)
	require.NoError(err)
	require.NoError(d.UpdateWorkspaceStatus(ctx, ws.ID, "ready", nil))

	dirty, err := mgr.Delete(ctx, ws.ID, true, nil)
	assert.Nil(dirty)
	require.Error(err)
	assert.Contains(err.Error(), "kill tmux session")
	assert.Contains(err.Error(), "permission denied")

	got, getErr := mgr.Get(ctx, ws.ID)
	require.NoError(getErr)
	require.NotNil(got)
	assert.Equal(ws.ID, got.ID)

	argvs := readRecorderArgv(t, record)
	require.Len(argvs, 1)
	assert.Equal(
		[]string{"wrap", "kill-session", "-t", ws.TmuxSession},
		argvs[0],
	)
}

func TestManagerDeleteTreatsTmuxServerExitDuringKillAsGone(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	dir := t.TempDir()
	record := filepath.Join(dir, "record")
	script := filepath.Join(dir, "fake-tmux")
	body := "#!/bin/sh\n" +
		`printf '%s\0' "$#" "$@" >> "$TMUX_RECORD"` + "\n" +
		`for a in "$@"; do` + "\n" +
		`  if [ "$a" = "kill-session" ]; then` + "\n" +
		`    echo "server exited unexpectedly" >&2` + "\n" +
		`    exit 1` + "\n" +
		`  fi` + "\n" +
		"done\n" +
		"exit 0\n"
	require.NoError(os.WriteFile(script, []byte(body), 0o755))
	t.Setenv("TMUX_RECORD", record)

	d := openTestDB(t)
	repoID := seedRepo(t, d, "github.com", "acme", "widget")
	seedMR(t, d, repoID, 42, "feature/thing")

	mgr := NewManager(d, t.TempDir())
	mgr.SetTmuxCommand([]string{script, "wrap"})

	ctx := context.Background()
	ws, err := mgr.Create(ctx, "github", "github.com", "acme", "widget", 42)
	require.NoError(err)
	require.NoError(d.UpdateWorkspaceStatus(ctx, ws.ID, "ready", nil))

	dirty, err := mgr.Delete(ctx, ws.ID, true, nil)
	assert.Nil(dirty)
	require.NoError(err)

	got, getErr := mgr.Get(ctx, ws.ID)
	require.NoError(getErr)
	assert.Nil(got)

	argvs := readRecorderArgv(t, record)
	require.Len(argvs, 1)
	assert.Equal(
		[]string{"wrap", "kill-session", "-t", ws.TmuxSession},
		argvs[0],
	)
}

func TestManagerDeleteAllowsErroredWorkspaceWhenTmuxUnavailable(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	d := openTestDB(t)
	mgr := NewManager(d, t.TempDir())
	mgr.SetTmuxCommand([]string{
		filepath.Join(t.TempDir(), "missing-tmux"),
	})

	ctx := context.Background()
	ws := &Workspace{
		ID:              "ws-tmux-unavailable",
		PlatformHost:    "github.com",
		RepoOwner:       "acme",
		RepoName:        "widget",
		ItemType:        db.WorkspaceItemTypePullRequest,
		ItemNumber:      42,
		GitHeadRef:      "feature/thing",
		WorkspaceBranch: workspaceBranchUnknown,
		WorktreePath:    filepath.Join(t.TempDir(), "worktree"),
		TmuxSession:     "middleman-0000000000000042",
		Status:          "error",
	}
	require.NoError(d.InsertWorkspace(ctx, ws))

	dirty, err := mgr.Delete(ctx, ws.ID, true, nil)
	require.NoError(err)
	assert.Nil(dirty)

	got, err := mgr.Get(ctx, ws.ID)
	require.NoError(err)
	assert.Nil(got)
}

func TestManagerReapOrphanTmuxSessionsIgnoresUnavailableTmux(t *testing.T) {
	require := require.New(t)

	d := openTestDB(t)
	mgr := NewManager(d, t.TempDir())
	mgr.SetTmuxCommand([]string{filepath.Join(t.TempDir(), "missing-tmux")})

	require.NoError(mgr.ReapOrphanTmuxSessions(context.Background()))
}

func TestManagerReapOrphanTmuxSessionsKillsUnknownManagedSessions(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	dir := t.TempDir()
	record := filepath.Join(dir, "record")
	script := filepath.Join(dir, "fake-tmux")
	body := "#!/bin/sh\n" +
		`printf '%s\0' "$#" "$@" >> "$TMUX_RECORD"` + "\n" +
		`for a in "$@"; do` + "\n" +
		`  if [ "$a" = "list-sessions" ]; then` + "\n" +
		`    printf 'middleman-0000000000000001:%s\n' "$MIDDLEMAN_TMUX_OWNER"` + "\n" +
		`    printf 'middleman-ffffffffffffffff\n'` + "\n" +
		`    printf 'middleman-aaaaaaaaaaaaaaaa-0123456789abcdef:%s\n' "$MIDDLEMAN_TMUX_OWNER"` + "\n" +
		`    printf 'middleman-aaaaaaaaaaaaaaaa-claude:%s\n' "$MIDDLEMAN_TMUX_OWNER"` + "\n" +
		`    printf 'middleman-notes\nother-session\n'` + "\n" +
		`    exit 0` + "\n" +
		`  fi` + "\n" +
		"done\n" +
		"exit 0\n"
	require.NoError(os.WriteFile(script, []byte(body), 0o755))
	t.Setenv("TMUX_RECORD", record)

	d := openTestDB(t)
	mgr := NewManager(d, t.TempDir())
	mgr.SetTmuxCommand([]string{script, "wrap"})
	t.Setenv("MIDDLEMAN_TMUX_OWNER", mgr.tmuxOwnerMarker())

	live := &Workspace{
		ID:           "ws-live",
		PlatformHost: "github.com",
		RepoOwner:    "acme",
		RepoName:     "widget",
		ItemType:     db.WorkspaceItemTypePullRequest,
		ItemNumber:   1,
		GitHeadRef:   "feature/live",
		WorktreePath: filepath.Join(t.TempDir(), "live"),
		TmuxSession:  "middleman-0000000000000001",
		Status:       "ready",
	}
	require.NoError(d.InsertWorkspace(context.Background(), live))

	require.NoError(mgr.ReapOrphanTmuxSessions(context.Background()))

	argvs := readRecorderArgv(t, record)
	require.Len(argvs, 2)
	assert.Equal(
		[]string{
			"wrap", "list-sessions", "-F",
			"#{session_name}:#{@middleman_owner}",
		},
		argvs[0],
	)
	assert.Equal(
		[]string{
			"wrap", "kill-session", "-t",
			"middleman-aaaaaaaaaaaaaaaa-0123456789abcdef",
		},
		argvs[1],
	)
	assert.NotContains(argvs, []string{
		"wrap", "show-options", "-qv", "-t",
		"middleman-aaaaaaaaaaaaaaaa-claude", "@middleman_owner",
	})
	assert.NotContains(argvs, []string{
		"wrap", "kill-session", "-t",
		"middleman-aaaaaaaaaaaaaaaa-claude",
	})
}

func TestManagerReapOrphanTmuxSessionsKeepsStoredRuntimeSessions(
	t *testing.T,
) {
	assert := Assert.New(t)
	require := require.New(t)

	dir := t.TempDir()
	record := filepath.Join(dir, "record")
	script := filepath.Join(dir, "fake-tmux")
	body := "#!/bin/sh\n" +
		`printf '%s\0' "$#" "$@" >> "$TMUX_RECORD"` + "\n" +
		`for a in "$@"; do` + "\n" +
		`  if [ "$a" = "list-sessions" ]; then` + "\n" +
		`    printf 'middleman-0000000000000001:%s\n' "$MIDDLEMAN_TMUX_OWNER"` + "\n" +
		`    printf 'middleman-0000000000000001-57de4cf40144bdf7:%s\n' "$MIDDLEMAN_TMUX_OWNER"` + "\n" +
		`    printf 'middleman-aaaaaaaaaaaaaaaa-c857d09db23e6822:%s\n' "$MIDDLEMAN_TMUX_OWNER"` + "\n" +
		`    exit 0` + "\n" +
		`  fi` + "\n" +
		"done\n" +
		"exit 0\n"
	require.NoError(os.WriteFile(script, []byte(body), 0o755))
	t.Setenv("TMUX_RECORD", record)

	d := openTestDB(t)
	mgr := NewManager(d, t.TempDir())
	mgr.SetTmuxCommand([]string{script, "wrap"})
	t.Setenv("MIDDLEMAN_TMUX_OWNER", mgr.tmuxOwnerMarker())

	require.NoError(d.InsertWorkspace(context.Background(), &Workspace{
		ID:           "0000000000000001",
		PlatformHost: "github.com",
		RepoOwner:    "acme",
		RepoName:     "widget",
		ItemType:     db.WorkspaceItemTypePullRequest,
		ItemNumber:   1,
		GitHeadRef:   "feature/live",
		WorktreePath: filepath.Join(t.TempDir(), "live"),
		TmuxSession:  "middleman-0000000000000001",
		Status:       "ready",
	}))
	recordRuntimeTmuxSessionForTest(
		t, d, "0000000000000001", "0000000000000001_codex",
		"codex", "middleman-0000000000000001-57de4cf40144bdf7",
		time.Time{},
	)

	require.NoError(mgr.ReapOrphanTmuxSessions(context.Background()))

	argvs := readRecorderArgv(t, record)
	assert.Contains(argvs, []string{
		"wrap", "kill-session", "-t",
		"middleman-aaaaaaaaaaaaaaaa-c857d09db23e6822",
	})
	assert.NotContains(argvs, []string{
		"wrap", "kill-session", "-t",
		"middleman-0000000000000001-57de4cf40144bdf7",
	})
}

func TestManagerPruneMissingTmuxSessionsRemovesStaleRecords(
	t *testing.T,
) {
	assert := Assert.New(t)
	require := require.New(t)

	dir := t.TempDir()
	record := filepath.Join(dir, "record")
	script := filepath.Join(dir, "fake-tmux")
	body := "#!/bin/sh\n" +
		`printf '%s\0' "$#" "$@" >> "$TMUX_RECORD"` + "\n" +
		`for a in "$@"; do` + "\n" +
		`  if [ "$a" = "list-sessions" ]; then` + "\n" +
		`    printf 'middleman-0000000000000001\nmiddleman-0000000000000001-57de4cf40144bdf7\n'` + "\n" +
		`    exit 0` + "\n" +
		`  fi` + "\n" +
		"done\n" +
		"exit 0\n"
	require.NoError(os.WriteFile(script, []byte(body), 0o755))
	t.Setenv("TMUX_RECORD", record)

	d := openTestDB(t)
	mgr := NewManager(d, t.TempDir())
	mgr.SetTmuxCommand([]string{script, "wrap"})
	mgr.SetPtyOwnerFallbackClient(&fakePtyOwnerClient{
		StateSessions: map[string]bool{
			"middleman-0000000000000003": true,
		},
	})
	ctx := context.Background()

	require.NoError(d.InsertWorkspace(ctx, &Workspace{
		ID:           "0000000000000001",
		PlatformHost: "github.com",
		RepoOwner:    "acme",
		RepoName:     "widget",
		ItemType:     db.WorkspaceItemTypePullRequest,
		ItemNumber:   1,
		GitHeadRef:   "feature/live",
		WorktreePath: filepath.Join(t.TempDir(), "live"),
		TmuxSession:  "middleman-0000000000000001",
		Status:       "ready",
	}))
	require.NoError(d.InsertWorkspace(ctx, &Workspace{
		ID:           "0000000000000002",
		PlatformHost: "github.com",
		RepoOwner:    "acme",
		RepoName:     "gadget",
		ItemType:     db.WorkspaceItemTypePullRequest,
		ItemNumber:   2,
		GitHeadRef:   "feature/stale",
		WorktreePath: filepath.Join(t.TempDir(), "stale"),
		TmuxSession:  "middleman-0000000000000002",
		Status:       "ready",
	}))
	require.NoError(d.InsertWorkspace(ctx, &Workspace{
		ID:           "0000000000000003",
		PlatformHost: "github.com",
		RepoOwner:    "acme",
		RepoName:     "legacy-owner",
		ItemType:     db.WorkspaceItemTypePullRequest,
		ItemNumber:   3,
		GitHeadRef:   "feature/owner",
		WorktreePath: filepath.Join(t.TempDir(), "owner"),
		TmuxSession:  "middleman-0000000000000003",
		Status:       "ready",
	}))
	_, err := d.WriteDB().ExecContext(
		ctx,
		`UPDATE middleman_workspaces SET terminal_backend = '' WHERE id = ?`,
		"0000000000000003",
	)
	require.NoError(err)
	recordRuntimeTmuxSessionForTest(
		t, d, "0000000000000001", "0000000000000001_codex",
		"codex", "middleman-0000000000000001-57de4cf40144bdf7",
		time.Time{},
	)
	recordRuntimeTmuxSessionForTest(
		t, d, "0000000000000001", "0000000000000001_claude",
		"claude", "middleman-0000000000000001-c857d09db23e6822",
		time.Time{},
	)

	require.NoError(mgr.PruneMissingTmuxSessions(ctx))

	stored, err := d.ListWorkspaceRuntimeTmuxSessions(ctx, "0000000000000001")
	require.NoError(err)
	require.Len(stored, 1)
	assert.Equal(
		"middleman-0000000000000001-57de4cf40144bdf7",
		stored[0].TmuxSession,
	)

	live, err := d.GetWorkspace(ctx, "0000000000000001")
	require.NoError(err)
	require.NotNil(live)
	assert.Equal("ready", live.Status)

	stale, err := d.GetWorkspace(ctx, "0000000000000002")
	require.NoError(err)
	require.NotNil(stale)
	assert.Equal("error", stale.Status)
	require.NotNil(stale.ErrorMessage)
	assert.Contains(*stale.ErrorMessage, "tmux session is no longer running")
	assert.Contains(*stale.ErrorMessage, "middleman-0000000000000002")

	legacyOwner, err := d.GetWorkspace(ctx, "0000000000000003")
	require.NoError(err)
	require.NotNil(legacyOwner)
	assert.Equal("ready", legacyOwner.Status)
}

// TestManagerTmuxSessionListSurvivesTmux36Sanitization guards against
// tmux 3.6+'s format sanitization: control characters in -F output print
// as "_", so a literal tab separator corrupts every line into
// "<name>_<owner>", making prune mark live workspaces as errored and reap
// skip owned orphans. The fake tmux expands the requested format the way
// tmux 3.6 does, including the tab-to-underscore substitution, so this
// fails if the list format ever reverts to a control-character separator.
func TestManagerTmuxSessionListSurvivesTmux36Sanitization(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	dir := t.TempDir()
	record := filepath.Join(dir, "record")
	script := filepath.Join(dir, "fake-tmux")
	body := "#!/bin/sh\n" +
		`printf '%s\0' "$#" "$@" >> "$TMUX_RECORD"` + "\n" +
		`fmt=''` + "\n" +
		`prev=''` + "\n" +
		`for a in "$@"; do` + "\n" +
		`  if [ "$prev" = "-F" ]; then fmt="$a"; fi` + "\n" +
		`  prev="$a"` + "\n" +
		`done` + "\n" +
		`for a in "$@"; do` + "\n" +
		`  if [ "$a" = "list-sessions" ]; then` + "\n" +
		`    for name in middleman-0000000000000001 middleman-aaaaaaaaaaaaaaaa; do` + "\n" +
		`      printf '%s\n' "$fmt" \` + "\n" +
		`        | sed -e "s|#{session_name}|$name|" \` + "\n" +
		`              -e "s|#{@middleman_owner}|$MIDDLEMAN_TMUX_OWNER|" \` + "\n" +
		`        | tr '\t' '_'` + "\n" +
		`    done` + "\n" +
		`    exit 0` + "\n" +
		`  fi` + "\n" +
		`done` + "\n" +
		"exit 0\n"
	require.NoError(os.WriteFile(script, []byte(body), 0o755))
	t.Setenv("TMUX_RECORD", record)

	d := openTestDB(t)
	mgr := NewManager(d, t.TempDir())
	mgr.SetTmuxCommand([]string{script, "wrap"})
	t.Setenv("MIDDLEMAN_TMUX_OWNER", mgr.tmuxOwnerMarker())
	ctx := context.Background()

	require.NoError(d.InsertWorkspace(ctx, &Workspace{
		ID:           "0000000000000001",
		PlatformHost: "github.com",
		RepoOwner:    "acme",
		RepoName:     "widget",
		ItemType:     db.WorkspaceItemTypePullRequest,
		ItemNumber:   1,
		GitHeadRef:   "feature/live",
		WorktreePath: filepath.Join(t.TempDir(), "live"),
		TmuxSession:  "middleman-0000000000000001",
		Status:       "ready",
	}))

	require.NoError(mgr.PruneMissingTmuxSessions(ctx))
	live, err := d.GetWorkspace(ctx, "0000000000000001")
	require.NoError(err)
	require.NotNil(live)
	assert.Equal("ready", live.Status)

	require.NoError(mgr.ReapOrphanTmuxSessions(ctx))
	argvs := readRecorderArgv(t, record)
	assert.Contains(argvs, []string{
		"wrap", "kill-session", "-t", "middleman-aaaaaaaaaaaaaaaa",
	})
	assert.NotContains(argvs, []string{
		"wrap", "kill-session", "-t", "middleman-0000000000000001",
	})
}

// TestManagerListTmuxSessionInfosRealTmux lists sessions through an
// actual tmux server on an isolated socket. tmux 3.6+ sanitizes control
// characters in -F output, which silently broke a tab-separated list
// format on real servers while fake-script tests kept passing; running
// against the installed binary catches any future sanitization drift.
func TestManagerListTmuxSessionInfosRealTmux(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("real tmux listing test uses Unix tmux")
	}
	tmuxPath, err := exec.LookPath("tmux")
	if err != nil {
		t.Skipf("tmux unavailable in this test environment: %v", err)
	}

	assert := Assert.New(t)
	require := require.New(t)

	dir, err := os.MkdirTemp("/tmp", "middleman-tmux-list-*")
	require.NoError(err)
	t.Cleanup(func() {
		_ = os.RemoveAll(dir)
	})
	socket := filepath.Join(dir, "tmux.sock")
	t.Cleanup(func() {
		_ = procutil.Command(
			tmuxPath, "-f", "/dev/null", "-S", socket, "kill-server",
		).Run()
	})
	run := func(args ...string) {
		t.Helper()
		cmd := procutil.Command(
			tmuxPath,
			append([]string{"-f", "/dev/null", "-S", socket}, args...)...,
		)
		cmd.Env = append(os.Environ(), "TERM=xterm-256color")
		out, err := cmd.CombinedOutput()
		require.NoError(err, string(out))
	}

	const owned = "middleman-0123456789abcdef"
	const unowned = "middleman-fedcba9876543210"
	run("new-session", "-d", "-s", owned, "sleep 30")
	run("new-session", "-d", "-s", unowned, "sleep 30")

	d := openTestDB(t)
	mgr := NewManager(d, t.TempDir())
	mgr.SetTmuxCommand([]string{tmuxPath, "-f", "/dev/null", "-S", socket})
	run("set-option", "-t", owned, "@middleman_owner", mgr.tmuxOwnerMarker())

	infos, err := mgr.listTmuxSessionInfos(context.Background())
	require.NoError(err)
	owners := make(map[string]string, len(infos))
	for _, info := range infos {
		owners[info.name] = info.owner
	}
	require.Len(owners, 2)
	assert.Equal(mgr.tmuxOwnerMarker(), owners[owned])
	assert.Contains(owners, unowned)
	assert.Empty(owners[unowned])
}

func TestManagerTmuxSessionsForWorkspaceReadsStoredRuntimeSessions(
	t *testing.T,
) {
	assert := Assert.New(t)
	require := require.New(t)

	d := openTestDB(t)
	mgr := NewManager(d, t.TempDir())
	require.NoError(d.InsertWorkspace(context.Background(), &Workspace{
		ID:           "0000000000000001",
		PlatformHost: "github.com",
		RepoOwner:    "acme",
		RepoName:     "widget",
		ItemType:     db.WorkspaceItemTypePullRequest,
		ItemNumber:   1,
		GitHeadRef:   "feature/live",
		WorktreePath: filepath.Join(t.TempDir(), "live"),
		TmuxSession:  "middleman-0000000000000001",
		Status:       "ready",
	}))
	createdAt := time.Date(2026, 5, 30, 12, 0, 0, 0, time.UTC)
	recordRuntimeTmuxSessionForTest(
		t, d, "0000000000000001", "0000000000000001_codex",
		"codex", "middleman-0000000000000001-57de4cf40144bdf7",
		createdAt.Add(time.Minute),
	)
	recordRuntimeTmuxSessionForTest(
		t, d, "0000000000000001", "0000000000000001_claude",
		"claude", "middleman-0000000000000001-c857d09db23e6822",
		createdAt,
	)
	require.NoError(d.InsertWorkspace(context.Background(), &Workspace{
		ID:           "0000000000000002",
		PlatformHost: "github.com",
		RepoOwner:    "acme",
		RepoName:     "gadget",
		ItemType:     db.WorkspaceItemTypePullRequest,
		ItemNumber:   2,
		GitHeadRef:   "feature/other",
		WorktreePath: filepath.Join(t.TempDir(), "other"),
		TmuxSession:  "middleman-0000000000000002",
		Status:       "ready",
	}))
	recordRuntimeTmuxSessionForTest(
		t, d, "0000000000000002", "0000000000000002_codex",
		"codex", "middleman-0000000000000002-57de4cf40144bdf7",
		createdAt,
	)

	sessions, err := mgr.TmuxSessionsForWorkspace(
		context.Background(),
		"0000000000000001",
		"middleman-0000000000000001",
	)
	require.NoError(err)

	assert.Equal([]string{
		"middleman-0000000000000001",
		"middleman-0000000000000001-c857d09db23e6822",
		"middleman-0000000000000001-57de4cf40144bdf7",
	}, sessions)

	sessions, err = mgr.TmuxSessionsForWorkspace(
		context.Background(),
		"0000000000000001",
		"",
	)
	require.NoError(err)
	assert.Equal([]string{
		"middleman-0000000000000001-c857d09db23e6822",
		"middleman-0000000000000001-57de4cf40144bdf7",
	}, sessions)
}

func TestManagerCleanupTmuxSessionKillsRuntimeSessionsForWorkspace(
	t *testing.T,
) {
	assert := Assert.New(t)
	require := require.New(t)

	dir := t.TempDir()
	record := filepath.Join(dir, "record")
	script := filepath.Join(dir, "fake-tmux")
	body := "#!/bin/sh\n" +
		`printf '%s\0' "$#" "$@" >> "$TMUX_RECORD"` + "\n" +
		"exit 0\n"
	require.NoError(os.WriteFile(script, []byte(body), 0o755))
	t.Setenv("TMUX_RECORD", record)

	d := openTestDB(t)
	mgr := NewManager(d, t.TempDir())
	mgr.SetTmuxCommand([]string{script})
	ws := &Workspace{
		ID:           "0000000000000001",
		TmuxSession:  "middleman-0000000000000001",
		Status:       "ready",
		PlatformHost: "github.com",
		RepoOwner:    "acme",
		RepoName:     "widget",
		ItemType:     db.WorkspaceItemTypePullRequest,
		ItemNumber:   1,
		GitHeadRef:   "feature/live",
		WorktreePath: filepath.Join(t.TempDir(), "live"),
	}
	require.NoError(d.InsertWorkspace(context.Background(), ws))
	recordRuntimeTmuxSessionForTest(
		t, d, ws.ID, "0000000000000001_codex", "codex",
		"middleman-0000000000000001-57de4cf40144bdf7",
		time.Time{},
	)
	recordRuntimeTmuxSessionForTest(
		t, d, ws.ID, "0000000000000001_claude", "claude",
		"middleman-0000000000000001-c857d09db23e6822",
		time.Time{},
	)

	require.NoError(mgr.cleanupTmuxSession(context.Background(), ws))

	argvs := readRecorderArgv(t, record)
	assert.Contains(argvs, []string{
		"kill-session", "-t", "middleman-0000000000000001",
	})
	assert.Contains(argvs, []string{
		"kill-session", "-t",
		"middleman-0000000000000001-c857d09db23e6822",
	})
	assert.Contains(argvs, []string{
		"kill-session", "-t",
		"middleman-0000000000000001-57de4cf40144bdf7",
	})
	assert.NotContains(argvs, []string{
		"kill-session", "-t",
		"middleman-0000000000000002-57de4cf40144bdf7",
	})
	stored, err := d.ListWorkspaceRuntimeTmuxSessions(context.Background(), ws.ID)
	require.NoError(err)
	assert.Empty(stored)
}

func TestManagerCleanupTmuxSessionPreservesStoredRowsAfterRuntimeKillFailure(
	t *testing.T,
) {
	assert := Assert.New(t)
	require := require.New(t)

	dir := t.TempDir()
	record := filepath.Join(dir, "record")
	script := filepath.Join(dir, "fake-tmux")
	body := "#!/bin/sh\n" +
		`printf '%s\0' "$#" "$@" >> "$TMUX_RECORD"` + "\n" +
		`target=""` + "\n" +
		`prev=""` + "\n" +
		`for a in "$@"; do` + "\n" +
		`  if [ "$prev" = "-t" ]; then target="$a"; fi` + "\n" +
		`  prev="$a"` + "\n" +
		`done` + "\n" +
		`if [ "$1" = "kill-session" ]; then` + "\n" +
		`  case "$target" in` + "\n" +
		`    middleman-0000000000000001)` + "\n" +
		`      echo "can't find session: $target" >&2` + "\n" +
		`      exit 1` + "\n" +
		`      ;;` + "\n" +
		`    middleman-0000000000000001-57de4cf40144bdf7)` + "\n" +
		`      echo "permission denied" >&2` + "\n" +
		`      exit 42` + "\n" +
		`      ;;` + "\n" +
		`  esac` + "\n" +
		`fi` + "\n" +
		"exit 0\n"
	require.NoError(os.WriteFile(script, []byte(body), 0o755))
	t.Setenv("TMUX_RECORD", record)

	d := openTestDB(t)
	mgr := NewManager(d, t.TempDir())
	mgr.SetTmuxCommand([]string{script})
	ws := &Workspace{
		ID:           "0000000000000001",
		TmuxSession:  "middleman-0000000000000001",
		Status:       "error",
		PlatformHost: "github.com",
		RepoOwner:    "acme",
		RepoName:     "widget",
		ItemType:     db.WorkspaceItemTypePullRequest,
		ItemNumber:   1,
		GitHeadRef:   "feature/live",
		WorktreePath: filepath.Join(t.TempDir(), "live"),
	}
	require.NoError(d.InsertWorkspace(context.Background(), ws))
	for _, targetKey := range []string{"codex", "claude"} {
		recordRuntimeTmuxSessionForTest(
			t,
			d,
			ws.ID,
			ws.ID+"_"+targetKey,
			targetKey,
			map[string]string{
				"codex":  "middleman-0000000000000001-57de4cf40144bdf7",
				"claude": "middleman-0000000000000001-c857d09db23e6822",
			}[targetKey],
			time.Time{},
		)
	}

	err := mgr.cleanupTmuxSession(context.Background(), ws)
	require.Error(err)
	assert.Contains(err.Error(), "middleman-0000000000000001-57de4cf40144bdf7")

	argvs := readRecorderArgv(t, record)
	assert.Contains(argvs, []string{
		"kill-session", "-t", "middleman-0000000000000001",
	})
	assert.Contains(argvs, []string{
		"kill-session", "-t",
		"middleman-0000000000000001-57de4cf40144bdf7",
	})
	assert.Contains(argvs, []string{
		"kill-session", "-t",
		"middleman-0000000000000001-c857d09db23e6822",
	})

	stored, err := d.ListWorkspaceRuntimeTmuxSessions(context.Background(), ws.ID)
	require.NoError(err)
	require.Len(stored, 2)
}

func TestManagerForgetRuntimeSessionCreatedAtPreservesRecreatedRow(
	t *testing.T,
) {
	assert := Assert.New(t)
	require := require.New(t)

	d := openTestDB(t)
	mgr := NewManager(d, t.TempDir())
	require.NoError(d.InsertWorkspace(context.Background(), &Workspace{
		ID:           "ws-1",
		TmuxSession:  "middleman-ws-1",
		Status:       "ready",
		PlatformHost: "github.com",
		RepoOwner:    "acme",
		RepoName:     "widget",
		ItemType:     db.WorkspaceItemTypePullRequest,
		ItemNumber:   1,
		GitHeadRef:   "feature/live",
		WorktreePath: filepath.Join(t.TempDir(), "live"),
	}))
	oldCreatedAt := time.Date(2026, 4, 29, 1, 0, 0, 0, time.UTC)
	newCreatedAt := time.Date(2026, 4, 29, 1, 1, 0, 0, time.UTC)
	sessionKey := "ws-1_helper"
	recordRuntimeTmuxSessionForTest(
		t, d, "ws-1", sessionKey, "helper", "middleman-ws-1-helper",
		oldCreatedAt,
	)
	recordRuntimeTmuxSessionForTest(
		t, d, "ws-1", sessionKey, "helper", "middleman-ws-1-helper",
		newCreatedAt,
	)

	deleted, err := mgr.ForgetRuntimeSessionCreatedAt(
		context.Background(), "ws-1", sessionKey, oldCreatedAt,
	)
	require.NoError(err)
	assert.False(deleted)

	stored, err := d.ListWorkspaceRuntimeTmuxSessions(context.Background(), "ws-1")
	require.NoError(err)
	require.Len(stored, 1)
	assert.Equal(newCreatedAt, stored[0].CreatedAt)
}

func TestManagerForgetRuntimeSessionAfterExitKeepsLiveTmuxSession(
	t *testing.T,
) {
	assert := Assert.New(t)
	require := require.New(t)

	d := openTestDB(t)
	mgr := NewManager(d, t.TempDir())
	require.NoError(d.InsertWorkspace(context.Background(), &Workspace{
		ID:           "ws-1",
		TmuxSession:  "middleman-ws-1",
		Status:       "ready",
		PlatformHost: "github.com",
		RepoOwner:    "acme",
		RepoName:     "widget",
		ItemType:     db.WorkspaceItemTypePullRequest,
		ItemNumber:   1,
		GitHeadRef:   "feature/live",
		WorktreePath: filepath.Join(t.TempDir(), "live"),
	}))
	createdAt := time.Date(2026, 4, 29, 1, 0, 0, 0, time.UTC)
	sessionKey := "ws-1_helper"
	tmuxSession := "middleman-ws-1-helper"
	recordRuntimeTmuxSessionForTest(
		t, d, "ws-1", sessionKey, "helper", tmuxSession, createdAt,
	)

	dir := t.TempDir()
	record := filepath.Join(dir, "record")
	existsFile := filepath.Join(dir, "exists")
	script := filepath.Join(dir, "fake-tmux")
	body := "#!/bin/sh\n" +
		`printf '%s\0' "$#" "$@" >> "$TMUX_RECORD"` + "\n" +
		`if [ "$1" = "has-session" ]; then` + "\n" +
		`  if [ -f "$TMUX_EXISTS_FILE" ]; then exit 0; fi` + "\n" +
		`  echo "can't find session: $3" >&2` + "\n" +
		`  exit 1` + "\n" +
		`fi` + "\n" +
		`exit 0` + "\n"
	require.NoError(os.WriteFile(script, []byte(body), 0o755))
	t.Setenv("TMUX_RECORD", record)
	t.Setenv("TMUX_EXISTS_FILE", existsFile)
	mgr.SetTmuxCommand([]string{script})

	require.NoError(os.WriteFile(existsFile, []byte("1"), 0o644))
	deleted, err := mgr.ForgetRuntimeSessionAfterExit(
		context.Background(), "ws-1", sessionKey, createdAt, tmuxSession,
	)
	require.NoError(err)
	assert.False(deleted)
	stored, err := d.ListWorkspaceRuntimeTmuxSessions(context.Background(), "ws-1")
	require.NoError(err)
	require.Len(stored, 1)
	assert.Equal(sessionKey, stored[0].SessionKey)

	require.NoError(os.Remove(existsFile))
	deleted, err = mgr.ForgetRuntimeSessionAfterExit(
		context.Background(), "ws-1", sessionKey, createdAt, tmuxSession,
	)
	require.NoError(err)
	assert.True(deleted)
	stored, err = d.ListWorkspaceRuntimeTmuxSessions(context.Background(), "ws-1")
	require.NoError(err)
	assert.Empty(stored)

	argvs := readRecorderArgv(t, record)
	assert.Contains(argvs, []string{"has-session", "-t", tmuxSession})
}

func TestManagerRequestRetryFailsWhenTmuxCleanupFails(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	dir := t.TempDir()
	record := filepath.Join(dir, "record")
	script := filepath.Join(dir, "fake-tmux")
	body := "#!/bin/sh\n" +
		`printf '%s\0' "$#" "$@" >> "$TMUX_RECORD"` + "\n" +
		`for a in "$@"; do` + "\n" +
		`  if [ "$a" = "kill-session" ]; then` + "\n" +
		`    echo "permission denied" >&2` + "\n" +
		`    exit 1` + "\n" +
		`  fi` + "\n" +
		"done\n" +
		"exit 0\n"
	require.NoError(os.WriteFile(script, []byte(body), 0o755))
	t.Setenv("TMUX_RECORD", record)

	d := openTestDB(t)
	mgr := NewManager(d, t.TempDir())
	mgr.SetTmuxCommand([]string{script, "wrap"})
	ctx := context.Background()
	errMsg := "tmux new-session failed"
	ws := &Workspace{
		ID:              "ws-retry-cleanup-fails",
		PlatformHost:    "github.com",
		RepoOwner:       "acme",
		RepoName:        "widget",
		ItemType:        db.WorkspaceItemTypePullRequest,
		ItemNumber:      42,
		GitHeadRef:      "feature/retry",
		WorkspaceBranch: "middleman/pr-42",
		WorktreePath:    "/tmp/ws-retry-cleanup-fails",
		TmuxSession:     "middleman-retry-cleanup-fails",
		Status:          "error",
		ErrorMessage:    &errMsg,
	}
	require.NoError(d.InsertWorkspace(ctx, ws))
	require.NoError(d.InsertWorkspaceSetupEvent(ctx, &db.WorkspaceSetupEvent{
		WorkspaceID: ws.ID,
		Stage:       workspaceSetupStageTmuxSession,
		Outcome:     "success",
		Message:     "tmux session started",
	}))

	next, startNow, err := mgr.RequestRetry(ctx, ws.ID)
	assert.Nil(next)
	assert.False(startNow)
	require.Error(err)
	assert.Contains(err.Error(), "cleanup workspace artifacts before retry")
	assert.Contains(err.Error(), "kill tmux session")
	assert.Contains(err.Error(), "permission denied")

	got, err := d.GetWorkspace(ctx, ws.ID)
	require.NoError(err)
	require.NotNil(got)
	assert.Equal("error", got.Status)
	require.NotNil(got.ErrorMessage)
	assert.Contains(*got.ErrorMessage, "permission denied")
	assert.Equal("middleman/pr-42", got.WorkspaceBranch)

	argvs := readRecorderArgv(t, record)
	require.Len(argvs, 1)
	assert.Equal(
		[]string{"wrap", "kill-session", "-t", ws.TmuxSession},
		argvs[0],
	)
}

func TestManagerRequestRetryConsumesQueuedRetryWhenCleanupFails(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	dir := t.TempDir()
	started := filepath.Join(dir, "started")
	release := filepath.Join(dir, "release")
	count := filepath.Join(dir, "count")
	script := filepath.Join(dir, "fake-tmux")
	body := "#!/bin/sh\n" +
		`for a in "$@"; do` + "\n" +
		`  if [ "$a" = "kill-session" ]; then` + "\n" +
		`    n=0` + "\n" +
		`    if [ -f "$TMUX_COUNT" ]; then n=$(cat "$TMUX_COUNT"); fi` + "\n" +
		`    n=$((n + 1))` + "\n" +
		`    printf '%s' "$n" > "$TMUX_COUNT"` + "\n" +
		`    if [ "$n" -eq 1 ]; then` + "\n" +
		`      : > "$TMUX_STARTED"` + "\n" +
		`      while [ ! -f "$TMUX_RELEASE" ]; do sleep 0.01; done` + "\n" +
		`      echo "permission denied" >&2` + "\n" +
		`      exit 1` + "\n" +
		`    fi` + "\n" +
		`  fi` + "\n" +
		"done\n" +
		"exit 0\n"
	require.NoError(os.WriteFile(script, []byte(body), 0o755))
	t.Setenv("TMUX_STARTED", started)
	t.Setenv("TMUX_RELEASE", release)
	t.Setenv("TMUX_COUNT", count)

	d := openTestDB(t)
	mgr := NewManager(d, t.TempDir())
	mgr.SetTmuxCommand([]string{script, "wrap"})
	ctx := context.Background()
	errMsg := "tmux new-session failed"
	ws := &Workspace{
		ID:              "ws-retry-cleanup-queued",
		PlatformHost:    "github.com",
		RepoOwner:       "acme",
		RepoName:        "widget",
		ItemType:        db.WorkspaceItemTypePullRequest,
		ItemNumber:      42,
		GitHeadRef:      "feature/retry",
		WorkspaceBranch: "middleman/pr-42",
		WorktreePath:    "/tmp/ws-retry-cleanup-queued",
		TmuxSession:     "middleman-retry-cleanup-queued",
		Status:          "error",
		ErrorMessage:    &errMsg,
	}
	require.NoError(d.InsertWorkspace(ctx, ws))
	require.NoError(d.InsertWorkspaceSetupEvent(ctx, &db.WorkspaceSetupEvent{
		WorkspaceID: ws.ID,
		Stage:       workspaceSetupStageTmuxSession,
		Outcome:     "success",
		Message:     "tmux session started",
	}))

	type retryResult struct {
		ws       *Workspace
		startNow bool
		err      error
	}
	firstResult := make(chan retryResult, 1)
	go func() {
		next, startNow, err := mgr.RequestRetry(ctx, ws.ID)
		firstResult <- retryResult{ws: next, startNow: startNow, err: err}
	}()

	const retryWait = 5 * time.Second

	require.Eventually(func() bool {
		_, err := os.Stat(started)
		return err == nil
	}, retryWait, 10*time.Millisecond)
	require.Eventually(func() bool {
		got, err := d.GetWorkspace(ctx, ws.ID)
		return err == nil && got != nil && got.Status == "creating"
	}, retryWait, 10*time.Millisecond)

	queuedWS, startNow, err := mgr.RequestRetry(ctx, ws.ID)
	require.NoError(err)
	require.NotNil(queuedWS)
	assert.False(startNow)
	assert.Equal("creating", queuedWS.Status)

	require.NoError(os.WriteFile(release, []byte("1"), 0o644))
	var first retryResult
	require.Eventually(func() bool {
		select {
		case first = <-firstResult:
			return true
		default:
			return false
		}
	}, retryWait, 10*time.Millisecond)
	assert.Nil(first.ws)
	assert.False(first.startNow)
	require.Error(first.err)
	assert.Contains(first.err.Error(), "permission denied")

	next, queued, err := mgr.StartQueuedRetryIfErrored(ctx, ws.ID)
	require.NoError(err)
	assert.Nil(next)
	assert.False(queued)

	got, err := d.GetWorkspace(ctx, ws.ID)
	require.NoError(err)
	require.NotNil(got)
	assert.Equal("error", got.Status)
}

func TestManagerRequestRetrySkipsGitCleanupWhenCloneMissing(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	dir := t.TempDir()
	script := filepath.Join(dir, "fake-tmux")
	body := "#!/bin/sh\n" +
		`for a in "$@"; do` + "\n" +
		`  if [ "$a" = "kill-session" ]; then` + "\n" +
		`    echo "can't find session: missing" >&2` + "\n" +
		`    exit 1` + "\n" +
		`  fi` + "\n" +
		"done\n" +
		"exit 0\n"
	require.NoError(os.WriteFile(script, []byte(body), 0o755))

	d := openTestDB(t)
	mgr := NewManager(d, t.TempDir())
	mgr.SetTmuxCommand([]string{script, "wrap"})
	mgr.SetClones(gitclone.New(filepath.Join(dir, "clones"), nil))
	ctx := context.Background()
	errMsg := "ensure clone failed"
	ws := &Workspace{
		ID:              "ws-retry-missing-clone",
		PlatformHost:    "github.com",
		RepoOwner:       "acme",
		RepoName:        "widget",
		ItemType:        db.WorkspaceItemTypePullRequest,
		ItemNumber:      42,
		GitHeadRef:      "feature/retry",
		WorkspaceBranch: "middleman/pr-42",
		WorktreePath:    filepath.Join(dir, "missing-worktree"),
		TmuxSession:     "middleman-retry-missing-clone",
		Status:          "error",
		ErrorMessage:    &errMsg,
	}
	require.NoError(d.InsertWorkspace(ctx, ws))

	next, startNow, err := mgr.RequestRetry(ctx, ws.ID)
	require.NoError(err)
	require.NotNil(next)
	assert.True(startNow)
	assert.Equal("creating", next.Status)
	assert.Equal(workspaceBranchUnknown, next.WorkspaceBranch)

	got, err := d.GetWorkspace(ctx, ws.ID)
	require.NoError(err)
	require.NotNil(got)
	assert.Equal("creating", got.Status)
	assert.Equal(workspaceBranchUnknown, got.WorkspaceBranch)
	assert.Nil(got.ErrorMessage)
}

func TestIssueRetryCleansLeakedUnknownBranchAndUsesIssueBranch(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	host, owner, name := "github.com", "acme", "widget"
	baseDir := t.TempDir()
	mgr := NewManager(openTestDB(t), t.TempDir())
	mgr.SetClones(gitclone.New(baseDir, nil))

	cloneDir, err := mgr.clones.ClonePath(host, owner, name)
	require.NoError(err)
	seedWorkspaceBareCloneAt(t, cloneDir)
	configureOriginHeadForIssueWorkspace(t, cloneDir)

	staleWorktree := filepath.Join(t.TempDir(), "stale-unknown-worktree")
	runWorkspaceTestGit(
		t, cloneDir,
		"worktree", "add", staleWorktree,
		"-b", workspaceBranchUnknown, "origin/HEAD",
	)
	exists, err := localBranchExists(
		t.Context(), cloneDir, workspaceBranchUnknown,
	)
	require.NoError(err)
	require.True(exists)

	ws := &Workspace{
		ID:              "ws-issue-retry-unknown",
		PlatformHost:    host,
		RepoOwner:       owner,
		RepoName:        name,
		ItemType:        db.WorkspaceItemTypeIssue,
		ItemNumber:      23,
		GitHeadRef:      "middleman/issue-23-federation-test",
		WorkspaceBranch: workspaceBranchUnknown,
		WorktreePath:    staleWorktree,
		Status:          "error",
	}
	require.NoError(mgr.cleanupWorkspaceArtifactsForRetry(t.Context(), ws))

	exists, err = localBranchExists(
		t.Context(), cloneDir, workspaceBranchUnknown,
	)
	require.NoError(err)
	assert.False(exists)

	branch, err := mgr.addIssueWorktree(t.Context(), cloneDir, ws)
	require.NoError(err)
	assert.Equal(ws.GitHeadRef, branch)

	exists, err = localBranchExists(t.Context(), cloneDir, ws.GitHeadRef)
	require.NoError(err)
	assert.True(exists)
	exists, err = localBranchExists(
		t.Context(), cloneDir, workspaceBranchUnknown,
	)
	require.NoError(err)
	assert.False(exists)
}

func TestManagerRequestRetryQueuesWhileCreatingAndStartsIfErrored(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	d := openTestDB(t)
	mgr := NewManager(d, t.TempDir())
	ctx := context.Background()
	ws := &Workspace{
		ID:              "ws-queued-retry",
		PlatformHost:    "github.com",
		RepoOwner:       "acme",
		RepoName:        "widget",
		ItemType:        db.WorkspaceItemTypePullRequest,
		ItemNumber:      42,
		GitHeadRef:      "feature/retry",
		WorkspaceBranch: workspaceBranchUnknown,
		WorktreePath:    "/tmp/ws-queued-retry",
		TmuxSession:     "middleman-ws-queued-retry",
		Status:          "creating",
	}
	require.NoError(d.InsertWorkspace(ctx, ws))

	current, startNow, err := mgr.RequestRetry(ctx, ws.ID)
	require.NoError(err)
	require.NotNil(current)
	assert.False(startNow)
	assert.Equal("creating", current.Status)

	errMsg := "ensure clone failed"
	require.NoError(d.UpdateWorkspaceStatus(ctx, ws.ID, "error", &errMsg))

	next, queued, err := mgr.StartQueuedRetryIfErrored(ctx, ws.ID)
	require.NoError(err)
	require.NotNil(next)
	assert.True(queued)
	assert.Equal("creating", next.Status)
	assert.Nil(next.ErrorMessage)

	stored, err := d.GetWorkspace(ctx, ws.ID)
	require.NoError(err)
	require.NotNil(stored)
	assert.Equal("creating", stored.Status)
	assert.Nil(stored.ErrorMessage)
	assert.Equal(workspaceBranchUnknown, stored.WorkspaceBranch)

	next, queued, err = mgr.StartQueuedRetryIfErrored(ctx, ws.ID)
	require.NoError(err)
	assert.Nil(next)
	assert.False(queued)
}

func TestManagerRequestRetryPreservesReusedIssueBranchSentinel(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	d := openTestDB(t)
	mgr := NewManager(d, t.TempDir())
	ctx := context.Background()
	errMsg := "setup failed"
	ws := &Workspace{
		ID:              "ws-reused-issue-retry",
		PlatformHost:    "github.com",
		RepoOwner:       "acme",
		RepoName:        "widget",
		ItemType:        db.WorkspaceItemTypeIssue,
		ItemNumber:      7,
		GitHeadRef:      "feature/reused",
		WorkspaceBranch: "",
		WorktreePath:    "/tmp/ws-reused-issue-retry",
		TmuxSession:     "middleman-ws-reused-issue-retry",
		Status:          "error",
		ErrorMessage:    &errMsg,
	}
	require.NoError(d.InsertWorkspace(ctx, ws))

	next, startNow, err := mgr.RequestRetry(ctx, ws.ID)
	require.NoError(err)
	require.NotNil(next)
	assert.True(startNow)
	assert.Equal("creating", next.Status)
	assert.Empty(next.WorkspaceBranch)
	assert.Nil(next.ErrorMessage)

	stored, err := d.GetWorkspace(ctx, ws.ID)
	require.NoError(err)
	require.NotNil(stored)
	assert.Equal("creating", stored.Status)
	assert.Empty(stored.WorkspaceBranch)
	assert.Nil(stored.ErrorMessage)
}

func TestManagerRequestRetryStartsWhenSetupFailedBeforeQueue(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	d := openTestDB(t)
	mgr := NewManager(d, t.TempDir())
	ctx := context.Background()
	errMsg := "ensure clone failed"
	ws := &Workspace{
		ID:              "ws-raced-retry",
		PlatformHost:    "github.com",
		RepoOwner:       "acme",
		RepoName:        "widget",
		ItemType:        db.WorkspaceItemTypePullRequest,
		ItemNumber:      42,
		GitHeadRef:      "feature/retry",
		WorkspaceBranch: "middleman/pr-42",
		WorktreePath:    "/tmp/ws-raced-retry",
		TmuxSession:     "middleman-ws-raced-retry",
		Status:          "error",
		ErrorMessage:    &errMsg,
	}
	require.NoError(d.InsertWorkspace(ctx, ws))

	next, startNow, err := mgr.queueRetryOrStartErrored(ctx, ws.ID)
	require.NoError(err)
	require.NotNil(next)
	assert.True(startNow)
	assert.Equal("creating", next.Status)
	assert.Nil(next.ErrorMessage)
	assert.Equal(workspaceBranchUnknown, next.WorkspaceBranch)

	stored, err := d.GetWorkspace(ctx, ws.ID)
	require.NoError(err)
	require.NotNil(stored)
	assert.Equal("creating", stored.Status)
	assert.Nil(stored.ErrorMessage)
	assert.Equal(workspaceBranchUnknown, stored.WorkspaceBranch)

	next, queued, err := mgr.StartQueuedRetryIfErrored(ctx, ws.ID)
	require.NoError(err)
	assert.Nil(next)
	assert.False(queued)
}

func TestManagerRequestRetryDiscardsQueuedRetryWhenSetupSucceeds(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	d := openTestDB(t)
	mgr := NewManager(d, t.TempDir())
	ctx := context.Background()
	ws := &Workspace{
		ID:              "ws-discard-retry",
		PlatformHost:    "github.com",
		RepoOwner:       "acme",
		RepoName:        "widget",
		ItemType:        db.WorkspaceItemTypePullRequest,
		ItemNumber:      42,
		GitHeadRef:      "feature/retry",
		WorkspaceBranch: workspaceBranchUnknown,
		WorktreePath:    "/tmp/ws-discard-retry",
		TmuxSession:     "middleman-ws-discard-retry",
		Status:          "creating",
	}
	require.NoError(d.InsertWorkspace(ctx, ws))

	current, startNow, err := mgr.RequestRetry(ctx, ws.ID)
	require.NoError(err)
	require.NotNil(current)
	assert.False(startNow)

	require.NoError(d.UpdateWorkspaceStatus(ctx, ws.ID, "ready", nil))

	next, queued, err := mgr.StartQueuedRetryIfErrored(ctx, ws.ID)
	require.NoError(err)
	require.NotNil(next)
	assert.False(queued)
	assert.Equal("ready", next.Status)

	stored, err := d.GetWorkspace(ctx, ws.ID)
	require.NoError(err)
	require.NotNil(stored)
	assert.Equal("ready", stored.Status)
}

func TestManagerEnsureTmuxCreatesSessionOnMiss(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)

	// Script: "has-session" emits tmux's canonical "can't find
	// session" stderr and exits 1 (so isTmuxSessionAbsent classifies
	// it as session-missing rather than wrapper failure); everything
	// else succeeds, so EnsureTmux calls newTmuxSession.
	dir := t.TempDir()
	record := filepath.Join(dir, "record")
	script := filepath.Join(dir, "fake-tmux")
	body := "#!/bin/sh\n" +
		`printf '%s\0' "$#" "$@" >> "$TMUX_RECORD"` + "\n" +
		`for a in "$@"; do` + "\n" +
		`  if [ "$a" = "has-session" ]; then` + "\n" +
		`    echo "can't find session: sim" >&2` + "\n" +
		`    exit 1` + "\n" +
		`  fi` + "\n" +
		"done\n" +
		"exit 0\n"
	require.NoError(os.WriteFile(script, []byte(body), 0o755))
	t.Setenv("TMUX_RECORD", record)

	d := openTestDB(t)
	mgr := NewManager(d, t.TempDir())
	mgr.SetTmuxCommand([]string{script})
	mgr.SetHideTmuxStatus(true)

	require.NoError(mgr.EnsureTmux(t.Context(), "sess-B", "/tmp/cwd"))

	argvs := readRecorderArgv(t, record)
	require.Len(argvs, 4)
	assert.Equal(
		[]string{"has-session", "-t", "sess-B"},
		argvs[0],
	)
	// new-session argv: "new-session -d -s sess-B -c /tmp/cwd <shell> -l"
	// We check the prefix up to the shell; the shell resolves per
	// runtime so just assert it is non-empty and ends with "-l".
	require.GreaterOrEqual(len(argvs[1]), 8)
	assert.Equal("new-session", argvs[1][0])
	assert.Equal("-d", argvs[1][1])
	assert.Equal("-s", argvs[1][2])
	assert.Equal("sess-B", argvs[1][3])
	assert.Equal("-c", argvs[1][4])
	assert.Equal("/tmp/cwd", argvs[1][5])
	assert.NotEmpty(argvs[1][6])
	assert.Equal("-l", argvs[1][7])
	assert.Equal(
		[]string{
			"set-option", "-t", "sess-B",
			"@middleman_owner", mgr.tmuxOwnerMarker(),
		},
		argvs[2],
	)
	assert.Equal(
		[]string{"set-option", "-q", "-t", "sess-B", "status", "off"},
		argvs[3],
	)
}

func TestManagerEnsureTmuxCreatesSessionOnMacOSMissingServer(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)

	dir := t.TempDir()
	record := filepath.Join(dir, "record")
	script := filepath.Join(dir, "fake-tmux")
	body := "#!/bin/sh\n" +
		`printf '%s\0' "$#" "$@" >> "$TMUX_RECORD"` + "\n" +
		`for a in "$@"; do` + "\n" +
		`  if [ "$a" = "has-session" ]; then` + "\n" +
		`    echo "error connecting to /private/tmp/tmux-501/default (No such file or directory)" >&2` + "\n" +
		`    exit 1` + "\n" +
		`  fi` + "\n" +
		"done\n" +
		"exit 0\n"
	require.NoError(os.WriteFile(script, []byte(body), 0o755))
	t.Setenv("TMUX_RECORD", record)

	d := openTestDB(t)
	mgr := NewManager(d, t.TempDir())
	mgr.SetTmuxCommand([]string{script})

	require.NoError(mgr.EnsureTmux(context.Background(), "sess-macos", "/tmp/cwd"))

	argvs := readRecorderArgv(t, record)
	require.Len(argvs, 3)
	assert.Equal(
		[]string{"has-session", "-t", "sess-macos"},
		argvs[0],
	)
	assert.Equal("new-session", argvs[1][0])
	assert.Equal("sess-macos", argvs[1][3])
	assert.Equal(
		[]string{
			"set-option", "-t", "sess-macos",
			"@middleman_owner", mgr.tmuxOwnerMarker(),
		},
		argvs[2],
	)
}

// TestReadRecorderArgvPreservesEmptyArgs pins down the parser's
// empty-arg handling. The NUL-delimited record format was chosen to
// round-trip argv with empty-string elements unambiguously; the
// parser must keep interior and trailing empties rather than
// collapsing them.
func TestReadRecorderArgvPreservesEmptyArgs(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	path := filepath.Join(t.TempDir(), "record")

	// First record: 3 args with an interior empty ("a", "", "b").
	// Second record: 2 args with a trailing empty ("x", "").
	body := "3\x00a\x00\x00b\x00" + "2\x00x\x00\x00"
	require.NoError(os.WriteFile(path, []byte(body), 0o644))

	argvs := readRecorderArgv(t, path)
	require.Len(argvs, 2)
	assert.Equal([]string{"a", "", "b"}, argvs[0])
	assert.Equal([]string{"x", ""}, argvs[1])
}

// TestManagerEnsureTmuxPropagatesBinaryError verifies that a wrapper
// misconfiguration (binary not on disk) surfaces as an error rather
// than being silently conflated with "session does not exist, please
// create one." The previous boolean-only tmuxSessionExists swallowed
// this case — EnsureTmux would proceed to run new-session with the
// same broken wrapper and the error would only surface on the second
// exec, masking the real cause.
func TestManagerEnsureTmuxPropagatesBinaryError(t *testing.T) {
	require := require.New(t)
	d := openTestDB(t)
	mgr := NewManager(d, t.TempDir())
	// Path that cannot possibly exist — exec returns a non-exit
	// error (ENOENT), not an *exec.ExitError.
	mgr.SetTmuxCommand(
		[]string{filepath.Join(t.TempDir(), "does-not-exist")},
	)

	err := mgr.EnsureTmux(t.Context(), "sess-X", "/tmp")
	require.Error(err)
	require.Contains(err.Error(), "tmux has-session")
}

// TestManagerEnsureTmuxPropagatesNon1ExitCode pins down the
// exit-code-1 carve-out in tmuxSessionExists. tmux's has-session
// exits 1 specifically when the session is not found; wrappers that
// fail for their own reasons typically exit with other codes (127
// "command not found", 203 "exec failed", etc.). A wrapper exiting
// with a non-1 code used to be silently treated as "session absent"
// because the old check matched any *exec.ExitError. Now it must
// propagate to the caller so misconfiguration surfaces cleanly.
func TestManagerEnsureTmuxPropagatesNon1ExitCode(t *testing.T) {
	require := require.New(t)
	dir := t.TempDir()
	script := filepath.Join(dir, "fake-tmux")
	// exit 127 mimics "command not found" — a common wrapper failure
	// signal that is NOT tmux's own "session missing" response.
	body := "#!/bin/sh\nexit 127\n"
	require.NoError(os.WriteFile(script, []byte(body), 0o755))

	d := openTestDB(t)
	mgr := NewManager(d, t.TempDir())
	mgr.SetTmuxCommand([]string{script})

	err := mgr.EnsureTmux(t.Context(), "sess-Y", "/tmp")
	require.Error(err)
	require.Contains(err.Error(), "tmux has-session")
}

// TestManagerEnsureTmuxPropagatesExit1NonTmuxError covers the
// second half of the session-absent heuristic: exit code 1 alone is
// not enough, the output must match tmux's canonical "session
// missing" phrases too. Many real wrappers and shell scripts use
// exit 1 as a generic failure signal — treating that as "session
// absent" would mask the wrapper bug by immediately trying
// new-session through the same broken wrapper.
func TestManagerEnsureTmuxPropagatesExit1NonTmuxError(t *testing.T) {
	require := require.New(t)
	dir := t.TempDir()
	script := filepath.Join(dir, "fake-tmux")
	body := "#!/bin/sh\necho 'wrapper blew up' >&2\nexit 1\n"
	require.NoError(os.WriteFile(script, []byte(body), 0o755))

	d := openTestDB(t)
	mgr := NewManager(d, t.TempDir())
	mgr.SetTmuxCommand([]string{script})

	err := mgr.EnsureTmux(t.Context(), "sess-Q", "/tmp")
	require.Error(err)
	require.Contains(err.Error(), "tmux has-session")
	require.Contains(err.Error(), "wrapper blew up")
}

// TestManagerEnsureTmuxIgnoresAbsencePhraseOnStdout pins down the
// stdout vs. stderr distinction. A wrapper that exits 1 with the
// tmux phrase on stdout (e.g. one that mirrors stderr to stdout for
// logging, or a script that coincidentally prints the phrase for
// unrelated reasons) must NOT be treated as session-absent — only
// stderr carries the authoritative tmux signal.
func TestManagerEnsureTmuxIgnoresAbsencePhraseOnStdout(t *testing.T) {
	require := require.New(t)
	dir := t.TempDir()
	script := filepath.Join(dir, "fake-tmux")
	body := "#!/bin/sh\n" +
		`echo "can't find session: sim"` + "\n" + // stdout only
		`echo "real failure" >&2` + "\n" +
		"exit 1\n"
	require.NoError(os.WriteFile(script, []byte(body), 0o755))

	d := openTestDB(t)
	mgr := NewManager(d, t.TempDir())
	mgr.SetTmuxCommand([]string{script})

	err := mgr.EnsureTmux(t.Context(), "sess-R", "/tmp")
	require.Error(err)
	require.Contains(err.Error(), "tmux has-session")
	require.Contains(err.Error(), "real failure")
}

type fakePtyOwnerCall struct {
	Op      string
	Session string
	Cwd     string
}

type fakePtyOwnerClient struct {
	Calls          []fakePtyOwnerCall
	StateExists    bool
	StateSessions  map[string]bool
	SnapshotOutput []byte
	SnapshotTitle  string
}

func (f *fakePtyOwnerClient) HasState(session string) bool {
	return f.StateExists || f.StateSessions[session]
}

func (f *fakePtyOwnerClient) Ensure(
	_ context.Context,
	session string,
	cwd string,
) error {
	f.Calls = append(f.Calls, fakePtyOwnerCall{
		Op: "ensure", Session: session, Cwd: cwd,
	})
	return nil
}

func (f *fakePtyOwnerClient) Attach(
	context.Context,
	string,
	int,
	int,
) (*ptyowner.Attachment, error) {
	return nil, nil
}

func (f *fakePtyOwnerClient) Stop(
	_ context.Context,
	session string,
) error {
	f.Calls = append(f.Calls, fakePtyOwnerCall{
		Op: "stop", Session: session,
	})
	return nil
}

func (f *fakePtyOwnerClient) Snapshot(
	context.Context,
	string,
) (ptyowner.Status, error) {
	return ptyowner.Status{
		Output: f.SnapshotOutput,
		Title:  f.SnapshotTitle,
	}, nil
}

func TestWorkspaceBranchCandidatesDoesNotIncludeBareForSluggedWorkspace(t *testing.T) {
	// Slug-style issue workspace whose bare-form branch name might
	// be a user-owned local branch unrelated to middleman. Cleanup
	// must return only the persisted GitHeadRef so the unrelated
	// branch is not deleted.
	assert := Assert.New(t)
	ws := &Workspace{
		ItemType:   db.WorkspaceItemTypeIssue,
		ItemNumber: 10,
		GitHeadRef: "middleman/issue-10-widget-rendering-broken",
	}
	got := workspaceBranchCandidates(ws, workspaceBranchUnknown)
	assert.Equal([]string{"middleman/issue-10-widget-rendering-broken"}, got)
}

func TestWorkspaceBranchCandidatesUsesBareFallbackOnlyForLegacyWorkspace(t *testing.T) {
	// Pre-feature issue workspaces have no recorded GitHeadRef.
	// Cleanup must still find the bare middleman/issue-<n> branch
	// those workspaces actually use.
	assert := Assert.New(t)
	ws := &Workspace{
		ItemType:   db.WorkspaceItemTypeIssue,
		ItemNumber: 10,
		GitHeadRef: "",
	}
	got := workspaceBranchCandidates(ws, workspaceBranchUnknown)
	assert.Equal([]string{"middleman/issue-10"}, got)
}

func TestIsGitWorktreeAbsentClassifiesCorruptGitfile(t *testing.T) {
	// A "git worktree add" interrupted mid-write (e.g. the daemon
	// canceling background setup at shutdown) leaves a worktree whose
	// .git gitfile is empty or partial. Cleanup must treat such a
	// dead worktree as absent rather than failing, so the workspace
	// stays deletable. These are the verbatim phrases git emits,
	// wrapped the way runGit wraps subprocess failures.
	assert := Assert.New(t)
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{
			"rev-parse on corrupt gitfile",
			fmt.Errorf(
				"%w: %s", errors.New("exit status 128"),
				"fatal: invalid gitfile format: /tmp/wt/.git",
			),
			true,
		},
		{
			"worktree remove on corrupt gitfile",
			fmt.Errorf(
				"%w: %s", errors.New("exit status 128"),
				"fatal: validation failed, cannot remove working "+
					"tree: '/tmp/wt/.git' is not a .git file, error code 5",
			),
			true,
		},
		{
			"missing worktree directory",
			errors.New("fatal: '/tmp/wt' is not a working tree"),
			true,
		},
		{"nil error", nil, false},
		{
			"unrelated git failure",
			fmt.Errorf(
				"%w: %s", errors.New("exit status 128"),
				"fatal: could not read Username for 'https://github.com'",
			),
			false,
		},
	}
	for _, tc := range cases {
		assert.Equalf(
			tc.want, isGitWorktreeAbsent(tc.err), "case %s", tc.name,
		)
	}
}

func TestFileLockManagerAcquireRelease(t *testing.T) {
	require := require.New(t)
	mgr := NewFileLockManager()
	ctx := t.Context()
	repo := t.TempDir()

	first, err := mgr.Acquire(ctx, repo)
	require.NoError(err)
	require.NoError(first.Unlock())

	second, err := mgr.Acquire(ctx, repo)
	require.NoError(err)
	require.NoError(second.Unlock())
}

func TestFileLockManagerSerializesGoroutines(t *testing.T) {
	require := require.New(t)
	mgr := NewFileLockManager()
	ctx := t.Context()
	repo := t.TempDir()

	const goroutines = 6
	var inCritical atomic.Int32
	var maxObserved atomic.Int32
	var overlap atomic.Int32

	var wg sync.WaitGroup
	for range goroutines {
		wg.Go(func() {
			lock, err := mgr.Acquire(ctx, repo)
			if err != nil {
				return
			}
			defer func() { _ = lock.Unlock() }()
			current := inCritical.Add(1)
			defer inCritical.Add(-1)
			if current > 1 {
				overlap.Add(1)
			}
			for {
				prev := maxObserved.Load()
				if current <= prev || maxObserved.CompareAndSwap(prev, current) {
					break
				}
			}
			time.Sleep(15 * time.Millisecond)
		})
	}
	wg.Wait()

	require.Equal(int32(1), maxObserved.Load(),
		"only one goroutine should hold the lock at a time")
	require.Equal(int32(0), overlap.Load(),
		"no goroutine should observe another holder in its critical section")
	require.Equal(int32(0), inCritical.Load())
}

func TestFileLockManagerCtxCancelWhileWaiting(t *testing.T) {
	require := require.New(t)
	mgr := NewFileLockManager()
	repo := t.TempDir()

	held, err := mgr.Acquire(t.Context(), repo)
	require.NoError(err)
	defer func() { _ = held.Unlock() }()

	ctx, cancel := context.WithCancel(t.Context())
	gotErr := make(chan error, 1)
	started := make(chan struct{})
	go func() {
		close(started)
		_, err := mgr.Acquire(ctx, repo)
		gotErr <- err
	}()
	<-started
	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case err := <-gotErr:
		require.ErrorIs(err, context.Canceled)
	case <-time.After(2 * time.Second):
		require.FailNow("Acquire did not return after ctx cancel")
	}
}

func TestFileLockManagerDoubleUnlock(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	mgr := NewFileLockManager()
	lock, err := mgr.Acquire(t.Context(), t.TempDir())
	require.NoError(err)
	require.NoError(lock.Unlock())
	assert.Error(lock.Unlock())
}

func TestManagerWithRepoLockReleaseOnSuccess(t *testing.T) {
	require := require.New(t)
	mgr := NewManager(openTestDB(t), t.TempDir())
	repo := t.TempDir()

	calls := 0
	require.NoError(mgr.withRepoLock(t.Context(), repo, func() error {
		calls++
		return nil
	}))
	require.Equal(1, calls)

	again, err := mgr.locks.Acquire(t.Context(), repo)
	require.NoError(err)
	require.NoError(again.Unlock())
}

func TestManagerWithRepoLockReleaseOnError(t *testing.T) {
	require := require.New(t)
	mgr := NewManager(openTestDB(t), t.TempDir())
	repo := t.TempDir()

	sentinel := errors.New("inner failed")
	err := mgr.withRepoLock(t.Context(), repo, func() error {
		return sentinel
	})
	require.ErrorIs(err, sentinel)

	again, err := mgr.locks.Acquire(t.Context(), repo)
	require.NoError(err)
	require.NoError(again.Unlock())
}

func TestManagerAddWorktreeAcquiresRepoLock(t *testing.T) {
	require := require.New(t)
	cloneDir := setupBareCloneForWorkspaceGitTest(t)
	configureSameRepoPRRefs(t, cloneDir, "feature/lock-probe", 7)
	mgr := NewManager(openTestDB(t), t.TempDir())

	// Hold the per-repo lock from outside addWorktree; it must wait.
	held, err := mgr.locks.Acquire(t.Context(), cloneDir)
	require.NoError(err)

	ws := &Workspace{
		ItemType:     db.WorkspaceItemTypePullRequest,
		ItemNumber:   7,
		GitHeadRef:   "feature/lock-probe",
		WorktreePath: filepath.Join(t.TempDir(), "wt"),
	}
	done := make(chan error, 1)
	go func() {
		_, err := mgr.addWorktree(t.Context(), cloneDir, false, ws)
		done <- err
	}()

	select {
	case <-done:
		require.FailNow("addWorktree completed while the per-repo lock was held")
	case <-time.After(80 * time.Millisecond):
	}

	require.NoError(held.Unlock())
	select {
	case err := <-done:
		require.NoError(err)
	case <-time.After(5 * time.Second):
		require.FailNow("addWorktree did not finish after lock release")
	}
}

func TestManagerCleanupForDeleteAcquiresRepoLock(t *testing.T) {
	require := require.New(t)

	host, owner, name := "github.com", "acme", "widget"
	baseDir := t.TempDir()
	cloneDir := filepath.Join(baseDir, host, owner, name+".git")
	require.NoError(os.MkdirAll(filepath.Dir(cloneDir), 0o755))
	work := filepath.Join(t.TempDir(), "source")
	runWorkspaceTestGit(t, baseDir, "init", "--initial-branch=main", work)
	runWorkspaceTestGit(t, work, "config", "user.email", "test@test.com")
	runWorkspaceTestGit(t, work, "config", "user.name", "Test")
	require.NoError(os.WriteFile(
		filepath.Join(work, "base.txt"), []byte("base\n"), 0o644,
	))
	runWorkspaceTestGit(t, work, "add", ".")
	runWorkspaceTestGit(t, work, "commit", "-m", "base commit")
	runWorkspaceTestGit(
		t, baseDir, "clone", "--bare", work, cloneDir,
	)

	mgr := NewManager(openTestDB(t), t.TempDir())
	mgr.SetClones(gitclone.New(baseDir, nil))
	worktreePath := filepath.Join(t.TempDir(), "missing-wt")
	runWorkspaceTestGit(
		t, cloneDir, "worktree", "add", worktreePath, "HEAD",
	)
	require.NoError(os.RemoveAll(worktreePath))

	ws := &Workspace{
		ID:           "ws-cleanup-lock",
		PlatformHost: host,
		RepoOwner:    owner,
		RepoName:     name,
		WorktreePath: worktreePath,
	}

	held, err := mgr.locks.Acquire(t.Context(), cloneDir)
	require.NoError(err)
	done := make(chan error, 1)
	go func() { done <- mgr.cleanupWorkspaceArtifactsForDelete(t.Context(), ws) }()

	select {
	case <-done:
		require.FailNow("cleanupWorkspaceArtifactsForDelete proceeded under held lock")
	case <-time.After(80 * time.Millisecond):
	}
	require.NoError(held.Unlock())
	select {
	case err := <-done:
		require.NoError(err)
	case <-time.After(5 * time.Second):
		require.FailNow("cleanupWorkspaceArtifactsForDelete did not finish after release")
	}
}
