package workspaceapi

import (
	"context"
	"crypto/sha256"
	"fmt"
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
	"go.kenn.io/forge/internal/testutil/gitsafe"
	"go.kenn.io/forge/internal/testutil/reposeed"
	"go.kenn.io/forge/internal/workspace"
)

func TestHotWorktreeLifecycleWarmsClaimsRefillsAndStops(t *testing.T) {
	t.Parallel()
	for _, previousWorkspace := range []string{"existing", "deleted", "pre-library spare"} {
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
			repoID, err := reposeed.Seed(t.Context(), database, identity)
			require.NoError(err)
			require.NoError(database.UpdateRepoProviderObservation(t.Context(), repoID, db.RepoProviderMetadata{
				CloneURL: server.URL + "/acme/widget.git", DefaultBranch: "main",
			}, nil, nil))

			clones := gitclone.New(filepath.Join(root, "clones"), nil)
			manager := newWorkspaceTestManager(t, database, filepath.Join(root, "worktrees"))
			manager.SetClones(clones)
			manager.SetTmuxCommand([]string{os.Args[0], "-test.run=^$", "--"})
			first, err := manager.CreateAdHoc(
				t.Context(), "github", serverURL.Host, "acme", "widget",
				workspace.CreateAdHocOptions{BranchName: "first"},
			)
			require.NoError(err)
			require.NoError(manager.Setup(t.Context(), first))
			commonDir, stderr, err := gitsafe.Runner().Run(t.Context(), first.WorktreePath, nil, "rev-parse", "--git-common-dir")
			require.NoError(err, string(stderr))
			gitDir := strings.TrimSpace(string(commonDir))
			if !filepath.IsAbs(gitDir) {
				gitDir = filepath.Join(first.WorktreePath, gitDir)
			}
			var existingHead, existingStatus string
			if previousWorkspace != "deleted" {
				runGit(t, first.WorktreePath, "-c", "user.name=Test User", "-c", "user.email=test@example.test", "commit", "--allow-empty", "-m", "saved workspace commit")
				require.NoError(os.WriteFile(filepath.Join(first.WorktreePath, "staged.txt"), []byte("staged work\n"), 0o600))
				runGit(t, first.WorktreePath, "add", "staged.txt")
				require.NoError(os.WriteFile(filepath.Join(first.WorktreePath, "base.txt"), []byte("unfinished edits\n"), 0o600))
				require.NoError(os.WriteFile(filepath.Join(first.WorktreePath, "notes.txt"), []byte("untracked notes\n"), 0o600))
				existingHead = runGitOutput(t, first.WorktreePath, "rev-parse", "HEAD")
				existingStatus = runGitOutput(t, first.WorktreePath, "status", "--porcelain")
			}
			var previousSpare os.FileInfo
			switch previousWorkspace {
			case "pre-library spare":
				// Seed the former Forge layout directly, without the new library's writer.
				common, err := filepath.EvalSymlinks(gitDir)
				require.NoError(err)
				sum := sha256.Sum256([]byte(common))
				spare := filepath.Join(filepath.Dir(first.WorktreePath), fmt.Sprintf(".kenn-forge-hot-%x", sum[:8]), "checkout")
				runGit(t, gitDir, "worktree", "add", "--detach", "--no-checkout", spare, "HEAD")
				runGit(t, spare, "reset", "--hard", "HEAD")
				registration := runGitOutput(t, spare, "rev-parse", "--absolute-git-dir")
				require.NoError(os.WriteFile(filepath.Join(registration, "kenn-forge-hot-worktree"), []byte("ready\n"), 0o600))
				previousSpare, err = os.Stat(filepath.Join(spare, "base.txt"))
				require.NoError(err)
			case "existing":
				// Simulate a workspace created before hot checkouts were enabled.
				require.NoError(os.RemoveAll(filepath.Join(root, "worktrees", ".hot-repositories")))
			case "deleted":
				_, err = manager.Delete(t.Context(), first.ID, true, nil)
				require.NoError(err)
				remaining, err := database.ListWorkspaces(t.Context())
				require.NoError(err)
				require.Empty(remaining)
			}
			// Enrollment survives deleting the last workspace and restarting Forge,
			// even when no warming pass ran before deletion.
			manager = newWorkspaceTestManager(t, database, filepath.Join(root, "worktrees"))
			manager.SetClones(clones)
			manager.SetTmuxCommand([]string{os.Args[0], "-test.run=^$", "--"})

			var hotPath string
			findHotWorktree := func() string {
				out, stderr, err := gitsafe.Runner().Run(
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
				shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 5*time.Second)
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
			if previousSpare != nil {
				require.True(os.SameFile(previousSpare, claimedFile), "setup must claim the spare left by the previous Forge")
			}
			registration := runGitOutput(t, secondPath, "rev-parse", "--absolute-git-dir")
			workspaceID, err := os.ReadFile(filepath.Join(registration, "kenn-forge-workspace-id"))
			require.NoError(err)
			require.Equal(secondID, strings.TrimSpace(string(workspaceID)))
			require.Equal(latest, runGitOutput(t, secondPath, "rev-parse", "HEAD"))
			contents, err := os.ReadFile(filepath.Join(secondPath, "later.txt"))
			require.NoError(err)
			require.Equal("new upstream commit\n", string(contents))

			require.Eventually(func() bool {
				_, statErr := os.Stat(filepath.Join(hotPath, "base.txt"))
				return statErr == nil
			}, 10*time.Second, 20*time.Millisecond)
			require.NoError(manager.WarmWorktrees(t.Context()))
			if previousWorkspace != "deleted" {
				require.Equal("first", runGitOutput(t, first.WorktreePath, "branch", "--show-current"))
				require.Equal(existingHead, runGitOutput(t, first.WorktreePath, "rev-parse", "HEAD"))
				require.Equal(existingStatus, runGitOutput(t, first.WorktreePath, "status", "--porcelain"))
				for name, want := range map[string]string{
					"base.txt": "unfinished edits\n", "staged.txt": "staged work\n", "notes.txt": "untracked notes\n",
				} {
					contents, err := os.ReadFile(filepath.Join(first.WorktreePath, name))
					require.NoError(err)
					require.Equal(want, string(contents))
				}
				stored, err := database.GetWorkspace(t.Context(), first.ID)
				require.NoError(err)
				require.NotNil(stored)
				require.Equal("ready", stored.Status)
				require.Equal(first.WorktreePath, stored.WorktreePath)
			}

			cancelParent()
			shutdownCtx, cancelShutdown := context.WithTimeout(context.WithoutCancel(t.Context()), 5*time.Second)
			defer cancelShutdown()
			require.NoError(handler.Shutdown(shutdownCtx))
		})
	}
}
