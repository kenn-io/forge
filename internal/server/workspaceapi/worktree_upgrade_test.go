package workspaceapi

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/testutil/dbtest"
	"go.kenn.io/forge/internal/testutil/gitfixture"
	"go.kenn.io/forge/internal/workspace"
	"go.kenn.io/forge/internal/workspace/localruntime"
)

type deletionStopObservation struct {
	key, status string
	pathExists  bool
	err         error
}

type deletionPTYOwner struct {
	*initialMessagePTYOwner
	observe func(string)
}

func (o deletionPTYOwner) Stop(ctx context.Context, key string) error {
	o.observe(key)
	return o.initialMessagePTYOwner.Stop(ctx, key)
}

func TestDeletionOrderingSurvivesLifecycleMigration(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name          string
		queued, force bool
	}{
		{name: "interactive dirty"},
		{name: "queued dirty", queued: true},
		{name: "interactive force", force: true},
		{name: "queued force", queued: true, force: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			require := require.New(t)
			database := dbtest.Open(t)
			repo := gitfixture.NewRepository(t, false)
			gitfixture.Run(t, repo.Dir, "remote", "add", "origin", "https://github.com/acme/widget.git")
			path := filepath.Join(t.TempDir(), "checkout")
			gitfixture.Run(t, repo.Dir, "worktree", "add", path, "-b", "kenn-forge/pr-42", "HEAD")
			require.NoError(os.WriteFile(filepath.Join(path, "notes.txt"), []byte("keep"), 0o600))
			const id = "workspace-upgrade"
			ws := &db.Workspace{
				ID: id, Platform: "github", PlatformHost: "github.com", RepoOwner: "acme", RepoName: "widget",
				ItemType: db.WorkspaceItemTypePullRequest, ItemNumber: 42, GitHeadRef: "topic",
				WorkspaceBranch: "kenn-forge/pr-42", WorktreePath: path, Status: "ready",
			}
			require.NoError(database.InsertWorkspace(t.Context(), ws))
			manager := newWorkspaceTestManager(t, database, t.TempDir())
			manager.SetWorktreeBasePathResolver(func(context.Context, workspace.WorktreeBaseRepository) (string, bool, error) {
				return repo.Dir, true, nil
			})
			stops := make(chan deletionStopObservation, 8)
			owner := deletionPTYOwner{initialMessagePTYOwner: newInitialMessagePTYOwner(), observe: func(key string) {
				stored, err := database.GetWorkspace(t.Context(), id)
				observation := deletionStopObservation{key: key, err: err}
				if stored != nil {
					observation.status = stored.Status
				}
				_, statErr := os.Stat(path)
				observation.pathExists = statErr == nil
				stops <- observation
			}}
			runtime := localruntime.NewManager(localruntime.Options{
				Targets:         []localruntime.LaunchTarget{{Key: "agent", Kind: localruntime.LaunchTargetAgent, Command: []string{"unused"}, Available: true}},
				PtyOwnerRuntime: owner,
			})
			h := New(Deps{DB: database, Workspaces: manager, Runtime: runtime})
			h.Start(t.Context(), true)
			t.Cleanup(func() {
				ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), time.Second)
				defer cancel()
				require.NoError(h.Shutdown(ctx))
				runtime.Shutdown()
			})
			session, err := runtime.Launch(t.Context(), id, path, "agent")
			require.NoError(err)
			if tt.queued {
				if tt.force {
					require.NoError(h.queueWorkspaceForceDeletion(id))
				} else {
					require.NoError(h.QueueWorkspaceDeletion(id))
				}
				require.Eventually(func() bool {
					stored, err := database.GetWorkspace(t.Context(), id)
					return err == nil && (stored == nil || stored.Status == "deletion_failed")
				}, 5*time.Second, 10*time.Millisecond)
			} else {
				_, err = h.DeleteWorkspace(t.Context(), &DeleteWorkspaceInput{ID: id, Force: tt.force})
				if tt.force {
					require.NoError(err)
				} else {
					require.Error(err)
				}
			}
			stored, err := database.GetWorkspace(t.Context(), id)
			require.NoError(err)
			if tt.force {
				require.Nil(stored)
				require.Empty(runtime.ListSessions(id))
				require.NoDirExists(path)
				select {
				case observed := <-stops:
					require.NoError(observed.err)
					require.Equal(session.Key, observed.key)
					require.Equal("deleting", observed.status)
					require.True(observed.pathExists, "runtime stops before Git removes the checkout")
				default:
					require.Fail("runtime was not stopped")
				}
			} else {
				require.NotNil(stored)
				want := "ready"
				if tt.queued {
					want = "deletion_failed"
				}
				require.Equal(want, stored.Status)
				require.Len(runtime.ListSessions(id), 1)
				require.Empty(stops, "dirty refusal must leave the running session alone")
				require.FileExists(filepath.Join(path, "notes.txt"))
			}
		})
	}
}
