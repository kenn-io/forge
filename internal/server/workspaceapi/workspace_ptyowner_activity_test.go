package workspaceapi

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/ptyowner"
	"go.kenn.io/forge/internal/testutil/dbtest"
	"go.kenn.io/forge/internal/workspace"
)

func TestTmuxEnrichmentTreatsDormantPtyOwnerAsIdle(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"owner absent", "incomplete state"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			assert := assert.New(t)
			require := require.New(t)
			ctx := t.Context()
			database := dbtest.Open(t)
			root := t.TempDir()
			const session = "forge-dormant"
			require.NoError(database.InsertWorkspace(ctx, &db.Workspace{
				ID: "ws-dormant", Platform: "github", PlatformHost: "github.com",
				RepoOwner: "acme", RepoName: "widgets",
				ItemType: db.WorkspaceItemTypePullRequest, ItemNumber: 7,
				GitHeadRef: "feature/dormant", WorkspaceBranch: "feature/dormant",
				WorktreePath: t.TempDir(), TmuxSession: session, Status: "ready",
				TerminalBackend: workspace.TerminalBackendPtyOwner,
			}))
			paths, err := ptyowner.NewSessionPaths(root, session)
			require.NoError(err)
			if name == "incomplete state" {
				require.NoError(os.MkdirAll(paths.Dir, 0o700))
				require.NoError(os.WriteFile(paths.StatePath, []byte("{}"), 0o600))
			}
			manager := workspace.NewManager(database, t.TempDir())
			manager.SetPtyOwnerClient(&ptyowner.Client{Root: root, InProcess: true})
			handler := New(Deps{DB: database, Workspaces: manager})
			summary, err := database.GetWorkspaceSummary(ctx, "ws-dormant")
			require.NoError(err)

			result := handler.workspaceResponseWithTmuxEnrichment(ctx, summary)

			if name == "incomplete state" {
				assert.Error(result.tmuxErr)
				assert.False(result.tmuxComplete)
			} else {
				assert.NoError(result.tmuxErr)
				assert.True(result.tmuxComplete)
				assert.False(result.response.TmuxWorking)
				assert.NoFileExists(paths.StatePath)
			}
		})
	}
}
