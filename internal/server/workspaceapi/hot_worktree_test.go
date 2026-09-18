package workspaceapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/gitclone"
	"go.kenn.io/forge/internal/testutil/dbtest"
	"go.kenn.io/forge/internal/workspace"
	gitcmd "go.kenn.io/kit/git/cmd"
)

func TestHotWorktreeLifecycleWarmsClaimsRefillsAndStops(t *testing.T) {
	for _, previousWorkspace := range []string{"existing", "deleted"} {
		t.Run(previousWorkspace, func(t *testing.T) {
			require := require.New(t)
			root := t.TempDir()
			remote := filepath.Join(root, "acme", "widget.git")
			seed := filepath.Join(root, "seed")
			require.NoError(os.MkdirAll(filepath.Dir(remote), 0o755))
			runGit(t, root, "init", "--bare", "--initial-branch=main", remote)
			runGit(t, root, "init", "--initial-branch=main", seed)
			runGit(t, seed, "config", "user.email", "test@example.test")
			runGit(t, seed, "config", "user.name", "Test User")
			require.NoError(os.WriteFile(filepath.Join(seed, "base.txt"), []byte("base\n"), 0o644))
			runGit(t, seed, "add", "base.txt")
			runGit(t, seed, "commit", "-m", "base")
			runGit(t, seed, "remote", "add", "origin", remote)
			runGit(t, seed, "push", "-u", "origin", "main")
			runGit(t, remote, "update-server-info")
			server := httptest.NewServer(http.FileServer(http.Dir(root)))
			t.Cleanup(server.Close)
			serverURL, err := url.Parse(server.URL)
			require.NoError(err)

			database := dbtest.Open(t)
			identity := db.GitHubRepoIdentity(serverURL.Host, "acme", "widget")
			identity.PlatformRepoID = "repo-acme-widget"
			repoID, err := database.UpsertRepo(t.Context(), identity)
			require.NoError(err)
			require.NoError(database.UpdateRepoProviderMetadata(t.Context(), repoID, db.RepoProviderMetadata{
				CloneURL: server.URL + "/acme/widget.git", DefaultBranch: "main",
			}))

			clones := gitclone.New(filepath.Join(root, "clones"), nil)
			manager := workspace.NewManager(database, filepath.Join(root, "worktrees"))
			manager.SetClones(clones)
			manager.SetTmuxCommand([]string{os.Args[0], "-test.run=^$", "--"})
			first, err := manager.CreateAdHoc(
				t.Context(), "github", serverURL.Host, "acme", "widget",
				workspace.CreateAdHocOptions{BranchName: "first"},
			)
			require.NoError(err)
			require.NoError(manager.Setup(t.Context(), first))
			commonDir, stderr, err := gitcmd.New().Run(t.Context(), first.WorktreePath, nil, "rev-parse", "--git-common-dir")
			require.NoError(err, string(stderr))
			gitDir := strings.TrimSpace(string(commonDir))
			if !filepath.IsAbs(gitDir) {
				gitDir = filepath.Join(first.WorktreePath, gitDir)
			}
			if previousWorkspace == "existing" {
				// Simulate a workspace created before hot checkouts were enabled.
				require.NoError(os.RemoveAll(filepath.Join(root, "worktrees", ".hot-repositories")))
			} else {
				_, err = manager.Delete(t.Context(), first.ID, true, nil)
				require.NoError(err)
				remaining, err := database.ListWorkspaces(t.Context())
				require.NoError(err)
				require.Empty(remaining)
			}
			// Enrollment survives deleting the last workspace and restarting Forge,
			// even when no warming pass ran before deletion.
			manager = workspace.NewManager(database, filepath.Join(root, "worktrees"))
			manager.SetClones(clones)
			manager.SetTmuxCommand([]string{os.Args[0], "-test.run=^$", "--"})

			var hotPath string
			findHotWorktree := func() string {
				out, stderr, err := gitcmd.New().Run(
					t.Context(), gitDir, nil, "worktree", "list", "--porcelain",
				)
				if err != nil {
					t.Logf("list worktrees: %v: %s", err, stderr)
					return ""
				}
				for line := range strings.SplitSeq(string(out), "\n") {
					path, ok := strings.CutPrefix(line, "worktree ")
					if ok && strings.Contains(path, ".kenn-forge-hot-") {
						return path
					}
				}
				return ""
			}

			parent, cancelParent := context.WithCancel(t.Context())
			handler := New(Deps{DB: database, Workspaces: manager})
			handler.Start(parent, false)
			t.Cleanup(func() {
				cancelParent()
				shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				require.NoError(handler.Shutdown(shutdownCtx))
			})

			require.Eventually(func() bool {
				hotPath = findHotWorktree()
				if hotPath == "" {
					return false
				}
				_, statErr := os.Stat(filepath.Join(hotPath, "base.txt"))
				return statErr == nil
			}, 10*time.Second, 20*time.Millisecond)
			require.NoError(manager.WarmWorktrees(t.Context()))
			warmedFile, err := os.Stat(filepath.Join(hotPath, "base.txt"))
			require.NoError(err)
			// The spare may sit idle while the default branch advances.
			require.NoError(os.WriteFile(filepath.Join(seed, "later.txt"), []byte("new upstream commit\n"), 0o644))
			runGit(t, seed, "add", "later.txt")
			runGit(t, seed, "commit", "-m", "advance main after warming")
			runGit(t, seed, "push", "origin", "main")
			runGit(t, remote, "update-server-info")
			latest := runGitOutput(t, seed, "rev-parse", "HEAD")

			second, err := manager.CreateAdHoc(
				t.Context(), "github", serverURL.Host, "acme", "widget",
				workspace.CreateAdHocOptions{BranchName: "second"},
			)
			require.NoError(err)
			secondID, secondPath := second.ID, second.WorktreePath
			handler.runWorkspaceSetup(second)
			require.Eventually(func() bool {
				stored, getErr := database.GetWorkspace(t.Context(), secondID)
				return getErr == nil && stored != nil && stored.Status == "ready"
			}, 10*time.Second, 20*time.Millisecond)
			claimedFile, err := os.Stat(filepath.Join(secondPath, "base.txt"))
			require.NoError(err)
			require.True(os.SameFile(warmedFile, claimedFile))
			require.Equal(latest, runGitOutput(t, secondPath, "rev-parse", "HEAD"))
			contents, err := os.ReadFile(filepath.Join(secondPath, "later.txt"))
			require.NoError(err)
			require.Equal("new upstream commit\n", string(contents))

			require.Eventually(func() bool {
				_, statErr := os.Stat(filepath.Join(hotPath, "base.txt"))
				return statErr == nil
			}, 10*time.Second, 20*time.Millisecond)
			require.NoError(manager.WarmWorktrees(t.Context()))

			cancelParent()
			shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancelShutdown()
			require.NoError(handler.Shutdown(shutdownCtx))
		})
	}
}
