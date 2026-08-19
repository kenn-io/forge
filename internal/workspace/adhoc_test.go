package workspace

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/db"
)

func TestCreateAdHocGeneratesBranchWhenUnnamed(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	d := openTestDB(t)
	seedRepo(t, d, "github.com", "acme", "widget")
	mgr := NewManager(d, t.TempDir())

	ws, err := mgr.CreateAdHoc(
		t.Context(), "github", "github.com", "acme", "widget",
		CreateAdHocOptions{},
	)
	require.NoError(err)
	require.NotNil(ws)

	assert.Equal(db.WorkspaceItemTypeAdHoc, ws.ItemType)
	assert.Equal(0, ws.ItemNumber)
	assert.True(strings.HasPrefix(ws.GitHeadRef, "kenn-forge/work-"),
		"generated branch %q should carry the work prefix", ws.GitHeadRef)
	assert.Equal(ws.GitHeadRef, ws.WorkspaceBranch)
	assert.Equal(db.AdHocWorkspaceItemKey(ws.GitHeadRef), ws.ItemKey)
	assert.Contains(ws.WorktreePath, filepath.Join("acme", "widget"))
}

func TestCreateAdHocUsesRequestedBranch(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	d := openTestDB(t)
	seedRepo(t, d, "github.com", "acme", "widget")
	mgr := NewManager(d, t.TempDir())

	ws, err := mgr.CreateAdHoc(
		t.Context(), "github", "github.com", "acme", "widget",
		CreateAdHocOptions{BranchName: "  spike/rate-limits  "},
	)
	require.NoError(err)
	require.NotNil(ws)

	assert.Equal("spike/rate-limits", ws.GitHeadRef)
	assert.Equal("adhoc:spike/rate-limits", ws.ItemKey)
	assert.Contains(filepath.Base(ws.WorktreePath), "work-spike-rate-limits-")
}

func TestCreateAdHocRejectsInvalidBranch(t *testing.T) {
	d := openTestDB(t)
	seedRepo(t, d, "github.com", "acme", "widget")
	mgr := NewManager(d, t.TempDir())

	_, err := mgr.CreateAdHoc(
		t.Context(), "github", "github.com", "acme", "widget",
		CreateAdHocOptions{BranchName: "bad branch..name"},
	)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid branch name")
}

func TestCreateAdHocRejectsUntrackedRepo(t *testing.T) {
	d := openTestDB(t)
	mgr := NewManager(d, t.TempDir())

	_, err := mgr.CreateAdHoc(
		t.Context(), "github", "github.com", "acme", "widget",
		CreateAdHocOptions{BranchName: "spike/thing"},
	)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not tracked")
}

func TestCreateAdHocSameBranchTwiceIsDuplicate(t *testing.T) {
	require := require.New(t)

	d := openTestDB(t)
	seedRepo(t, d, "github.com", "acme", "widget")
	mgr := NewManager(d, t.TempDir())

	_, err := mgr.CreateAdHoc(
		t.Context(), "github", "github.com", "acme", "widget",
		CreateAdHocOptions{BranchName: "spike/thing"},
	)
	require.NoError(err)

	_, err = mgr.CreateAdHoc(
		t.Context(), "github", "github.com", "acme", "widget",
		CreateAdHocOptions{BranchName: "spike/thing"},
	)
	require.Error(err)
	require.ErrorIs(err, ErrWorkspaceDuplicate)
}

// Two ad-hoc workspaces whose branch names slugify identically must not share
// a worktree directory.
func TestCreateAdHocDistinctBranchesGetDistinctWorktrees(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	d := openTestDB(t)
	seedRepo(t, d, "github.com", "acme", "widget")
	mgr := NewManager(d, t.TempDir())

	first, err := mgr.CreateAdHoc(
		t.Context(), "github", "github.com", "acme", "widget",
		CreateAdHocOptions{BranchName: "spike/rate-limits"},
	)
	require.NoError(err)
	second, err := mgr.CreateAdHoc(
		t.Context(), "github", "github.com", "acme", "widget",
		CreateAdHocOptions{BranchName: "spike-rate-limits"},
	)
	require.NoError(err)

	assert.NotEqual(first.ItemKey, second.ItemKey)
	assert.NotEqual(first.WorktreePath, second.WorktreePath)
}

func TestCreateAdHocExistingLocalBranchIsUniquified(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	const branch = "spike/thing"
	localRepo := setupLocalWorktreeBaseForWorkspaceGitTest(t, "feature/other")
	runWorkspaceTestGit(t, localRepo, "branch", branch)

	d := openTestDB(t)
	seedRepo(t, d, "github.com", "acme", "widget")
	mgr := NewManager(d, t.TempDir())
	mgr.SetWorktreeBasePathResolver(staticBaseResolver(localRepo))

	ws, err := mgr.CreateAdHoc(
		t.Context(), "github", "github.com", "acme", "widget",
		CreateAdHocOptions{BranchName: branch},
	)

	require.NoError(err)
	require.NotNil(ws)
	assert.Equal(branch+"-2", ws.GitHeadRef)
	assert.Equal(branch+"-2", ws.WorkspaceBranch)
	assert.Equal(db.AdHocWorkspaceItemKey(branch+"-2"), ws.ItemKey)
}

func TestNextAvailableBranchNameAvoidsRefNamespaceConflicts(t *testing.T) {
	tests := []struct {
		name      string
		existing  []string
		requested string
		want      string
	}{
		{
			name:      "descendant",
			existing:  []string{"docs/agent-context-routing"},
			requested: "docs",
			want:      "docs-2",
		},
		{
			name:      "numbered descendant",
			existing:  []string{"docs/agent-context-routing", "docs-2/claimed"},
			requested: "docs",
			want:      "docs-3",
		},
		{
			name:      "ancestor",
			existing:  []string{"docs"},
			requested: "docs/agent-context-routing",
			want:      "docs-2/agent-context-routing",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			localRepo := setupLocalWorktreeBaseForWorkspaceGitTest(t, "feature/other")
			for _, branch := range tt.existing {
				runWorkspaceTestGit(t, localRepo, "branch", branch)
			}

			got, err := nextAvailableBranchName(t.Context(), localRepo, tt.requested)

			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestCreateAdHocReuseExistingLocalBranch(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	const branch = "spike/thing"
	localRepo := setupLocalWorktreeBaseForWorkspaceGitTest(t, "feature/other")
	runWorkspaceTestGit(t, localRepo, "branch", branch)

	d := openTestDB(t)
	seedRepo(t, d, "github.com", "acme", "widget")
	mgr := NewManager(d, t.TempDir())
	mgr.SetWorktreeBasePathResolver(staticBaseResolver(localRepo))

	ws, err := mgr.CreateAdHoc(
		t.Context(), "github", "github.com", "acme", "widget",
		CreateAdHocOptions{BranchName: branch, ReuseExistingBranch: true},
	)
	require.NoError(err)
	require.NotNil(ws)

	// An empty workspace branch means kenn-forge did not create the branch, so
	// rollback and delete leave the user's pre-existing branch alone.
	assert.Empty(ws.WorkspaceBranch)
	assert.Equal(branch, ws.GitHeadRef)
}

func TestSetupAdHocWorkspaceBranchesFromOriginHead(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	d := openTestDB(t)
	localRepo, _, platformHost := setupHTTPWorktreeBaseForWorkspaceGitTest(
		t, "feature/other",
	)
	seedRepo(t, d, platformHost, "acme", "widget")

	tmuxScript, _ := writeRecorderScript(t)
	mgr := NewManager(d, t.TempDir())
	mgr.SetTmuxCommand([]string{tmuxScript})
	mgr.SetWorktreeBasePathResolver(staticBaseResolver(localRepo))

	ws, err := mgr.CreateAdHoc(
		t.Context(), "github", platformHost, "acme", "widget",
		CreateAdHocOptions{BranchName: "spike/thing"},
	)
	require.NoError(err)
	require.NoError(mgr.Setup(t.Context(), ws))

	got, err := d.GetWorkspace(t.Context(), ws.ID)
	require.NoError(err)
	require.NotNil(got)
	assert.Equal("ready", got.Status)
	assert.Equal("spike/thing", got.WorkspaceBranch)

	head := strings.TrimSpace(string(runWorkspaceTestGit(
		t, got.WorktreePath, "rev-parse", "--abbrev-ref", "HEAD",
	)))
	assert.Equal("spike/thing", head)
	originHead := strings.TrimSpace(string(runWorkspaceTestGit(
		t, localRepo, "rev-parse", "refs/remotes/origin/HEAD",
	)))
	worktreeHead := strings.TrimSpace(string(runWorkspaceTestGit(
		t, got.WorktreePath, "rev-parse", "HEAD",
	)))
	assert.Equal(originHead, worktreeHead)
}

func TestSetupAdHocWorkspaceUniquifiesBranchPrefixConflict(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	d := openTestDB(t)
	localRepo, _, platformHost := setupHTTPWorktreeBaseForWorkspaceGitTest(
		t, "feature/other",
	)
	runWorkspaceTestGit(t, localRepo, "branch", "docs/agent-context-routing")
	seedRepo(t, d, platformHost, "acme", "widget")

	tmuxScript, _ := writeRecorderScript(t)
	mgr := NewManager(d, t.TempDir())
	mgr.SetTmuxCommand([]string{tmuxScript})
	mgr.SetWorktreeBasePathResolver(staticBaseResolver(localRepo))

	ws, err := mgr.CreateAdHoc(
		t.Context(), "github", platformHost, "acme", "widget",
		CreateAdHocOptions{BranchName: "docs"},
	)
	require.NoError(err)
	require.NoError(mgr.Setup(t.Context(), ws))

	got, err := d.GetWorkspace(t.Context(), ws.ID)
	require.NoError(err)
	require.NotNil(got)
	assert.Equal("ready", got.Status)
	assert.Equal("docs-2", got.GitHeadRef)
	assert.Equal("docs-2", got.WorkspaceBranch)

	head := strings.TrimSpace(string(runWorkspaceTestGit(
		t, got.WorktreePath, "rev-parse", "--abbrev-ref", "HEAD",
	)))
	assert.Equal("docs-2", head)
}

func TestAgentContextForAdHocWorkspace(t *testing.T) {
	assert := assert.New(t)

	summary := WorkspaceSummary{}
	summary.ID = "ws-adhoc"
	summary.Platform = "github"
	summary.PlatformHost = "github.com"
	summary.RepoOwner = "acme"
	summary.RepoName = "widget"
	summary.ItemType = db.WorkspaceItemTypeAdHoc
	summary.GitHeadRef = "spike/thing"
	summary.WorkspaceBranch = "spike/thing"

	rendered := RenderAgentContext(BuildAgentContext(summary))

	assert.Contains(rendered, "Source kind: "+AgentSourceKindAdHoc)
	assert.Contains(rendered, "Working branch: spike/thing")
	assert.Contains(rendered, "No linked pull request, issue, or task")
	assert.NotContains(rendered, "- PR:")
	assert.NotContains(rendered, "- Issue:")
}

func TestAgentContextForAdHocWorkspaceWithDetectedPR(t *testing.T) {
	assert := assert.New(t)

	prNumber := 41
	summary := WorkspaceSummary{}
	summary.ID = "ws-adhoc"
	summary.Platform = "github"
	summary.PlatformHost = "github.com"
	summary.RepoOwner = "acme"
	summary.RepoName = "widget"
	summary.ItemType = db.WorkspaceItemTypeAdHoc
	summary.GitHeadRef = "spike/thing"
	summary.WorkspaceBranch = "spike/thing"
	summary.AssociatedPRNumber = &prNumber
	summary.AssociatedPRVisible = true

	rendered := RenderAgentContext(BuildAgentContext(summary))

	assert.Contains(rendered, "Associated PR: #41")
	assert.NotContains(rendered, "No linked pull request")
}

// Before setup materializes the worktree the workspace branch is the unknown
// sentinel, which must never leak into the generated context.
func TestAgentContextForAdHocWorkspaceBeforeSetup(t *testing.T) {
	assert := assert.New(t)

	summary := WorkspaceSummary{}
	summary.ID = "ws-adhoc"
	summary.Platform = "github"
	summary.PlatformHost = "github.com"
	summary.RepoOwner = "acme"
	summary.RepoName = "widget"
	summary.ItemType = db.WorkspaceItemTypeAdHoc
	summary.GitHeadRef = "spike/thing"
	summary.WorkspaceBranch = workspaceBranchUnknown

	rendered := RenderAgentContext(BuildAgentContext(summary))

	assert.Contains(rendered, "Working branch: spike/thing")
	assert.NotContains(rendered, workspaceBranchUnknown)
}

// TestSetupFailsClosedWhenRouteReplacedMidSetup interleaves a route
// replacement between setup's initial collision check and its final
// persist, verifying the re-validation catches replacements that land
// while the clone and worktree work is in flight.
func TestSetupFailsClosedWhenRouteReplacedMidSetup(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	d := openTestDB(t)
	localRepo, _, platformHost := setupHTTPWorktreeBaseForWorkspaceGitTest(
		t, "feature/other",
	)
	seedRepo(t, d, platformHost, "acme", "widget")

	tmuxScript, _ := writeRecorderScript(t)
	mgr := NewManager(d, t.TempDir())
	mgr.SetTmuxCommand([]string{tmuxScript})
	mgr.SetWorktreeBasePathResolver(staticBaseResolver(localRepo))

	ws, err := mgr.CreateAdHoc(
		t.Context(), "github", platformHost, "acme", "widget",
		CreateAdHocOptions{BranchName: "spike/replaced"},
	)
	require.NoError(err)

	mgr.beforeSetupRouteRevalidation = func() {
		_, _, replaceErr := d.ReconcileRepositoryObservation(
			t.Context(), db.RepoIdentity{
				Platform:       "github",
				PlatformHost:   platformHost,
				PlatformRepoID: "repo-acme-widget-replacement",
				Owner:          "acme",
				Name:           "widget",
			}, time.Now().UTC(),
		)
		require.NoError(replaceErr)
	}

	err = mgr.Setup(t.Context(), ws)
	require.ErrorContains(err, "historical occupants")
	stored, getErr := d.GetWorkspace(t.Context(), ws.ID)
	require.NoError(getErr)
	require.NotNil(stored)
	assert.Equal("error", stored.Status)
}
