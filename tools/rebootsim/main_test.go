package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/testutil/gitfixture"
	"go.kenn.io/forge/internal/testutil/gitsafe"
)

func TestMain(m *testing.M) {
	os.Exit(gitsafe.RunIsolatedMain(m))
}

const firstFixtureID = "5e5e5e5e00000001"

// The seeder must never run Git inside an existing repository, where a fixture
// commit could take that repository's staged changes.
func TestSeedRefusesDestinationsInsideRepositories(t *testing.T) {
	t.Parallel()
	cases := map[string]func(t *testing.T, repo *gitfixture.Repository) string{
		"data folder inside a checkout": func(t *testing.T, repo *gitfixture.Repository) string {
			return mkdir(t, filepath.Join(repo.Dir, "data"))
		},
		"symlinked data folder": func(t *testing.T, repo *gitfixture.Repository) string {
			link := filepath.Join(t.TempDir(), "data")
			require.NoError(t, os.Symlink(mkdir(t, filepath.Join(repo.Dir, "data")), link))
			return link
		},
		"symlinked fixture folder": func(t *testing.T, repo *gitfixture.Repository) string {
			data := t.TempDir()
			mkdir(t, filepath.Join(data, "worktrees"))
			require.NoError(t, os.Symlink(mkdir(t, filepath.Join(repo.Dir, "sim")),
				filepath.Join(data, "worktrees", "sim")))
			return data
		},
		"linked worktree": func(t *testing.T, repo *gitfixture.Repository) string {
			worktree := filepath.Join(t.TempDir(), "linked")
			gitfixture.Run(t, repo.Dir, "worktree", "add", "-b", "linked", worktree)
			return mkdir(t, filepath.Join(worktree, "data"))
		},
	}
	for name, destination := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			repo := gitfixture.NewRepository(t, false)
			repo.Write(t, "staged.md", "staged\n")
			gitfixture.Run(t, repo.Dir, "add", "staged.md")
			head := gitfixture.SHA(t, repo.Dir, "HEAD")

			err := seed(t.Context(), destination(t, repo), 1, "/bin/true")

			require.ErrorContains(t, err, "inside the Git repository")
			assert.Equal(t, head, gitfixture.SHA(t, repo.Dir, "HEAD"))
			assert.Equal(t, "staged.md", strings.TrimSpace(string(
				gitfixture.Run(t, repo.Dir, "diff", "--cached", "--name-only"))))
		})
	}
}

func TestSeedRefusesBareRepository(t *testing.T) {
	t.Parallel()
	bare := scratchOutsideRepositories(t)
	gitfixture.Run(t, bare, "init", "--bare")

	err := seed(t.Context(), mkdir(t, filepath.Join(bare, "data")), 1, "/bin/true")

	require.ErrorContains(t, err, "inside the Git repository")
	assert.Empty(t, gitfixture.Run(t, bare, "for-each-ref"))
}

func TestSeedRefusesExistingFixtureFolder(t *testing.T) {
	t.Parallel()
	data := scratchOutsideRepositories(t)
	existing := mkdir(t, filepath.Join(data, "worktrees", "sim", firstFixtureID))
	gitfixture.Run(t, existing, "init")
	require.NoError(t, os.WriteFile(filepath.Join(existing, "staged.md"), []byte("staged\n"), 0o600))
	gitfixture.Run(t, existing, "add", "staged.md")

	err := seed(t.Context(), data, 1, "/bin/true")

	require.ErrorContains(t, err, "create fresh worktree")
	assert.Empty(t, gitfixture.Run(t, existing, "for-each-ref"), "no commit was made")
	assert.Equal(t, "staged.md", strings.TrimSpace(string(
		gitfixture.Run(t, existing, "diff", "--cached", "--name-only"))))
}

// Inherited Git variables must not redirect fixture commands into another
// repository.
func TestSeedIgnoresInheritedGitDir(t *testing.T) { //nolint:paralleltest // t.Setenv sets GIT_DIR
	decoy := gitfixture.NewRepository(t, false)
	head := gitfixture.SHA(t, decoy.Dir, "HEAD")
	data := t.TempDir()
	t.Setenv("GIT_DIR", filepath.Join(decoy.Dir, ".git"))

	require.NoError(t, seed(t.Context(), data, 2, "/bin/true"))

	// Inspect the decoy with the variable gone, as a fresh shell would.
	require.NoError(t, os.Unsetenv("GIT_DIR"))
	assert.Equal(t, head, gitfixture.SHA(t, decoy.Dir, "HEAD"))
	for _, id := range []string{firstFixtureID, "5e5e5e5e00000002"} {
		fixture := filepath.Join(data, "worktrees", "sim", id)
		assert.Equal(t, "seed", strings.TrimSpace(string(
			gitfixture.Run(t, fixture, "log", "-1", "--format=%s"))))
	}
}

// scratchOutsideRepositories returns a temporary directory that Git confirms
// is outside every repository, so fixtures this test initializes there can
// never nest inside a checkout, such as when TMPDIR points into one.
func scratchOutsideRepositories(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, refuseInsideCheckout(t.Context(), dir))
	return dir
}

func mkdir(t *testing.T, dir string) string {
	t.Helper()
	require.NoError(t, os.MkdirAll(dir, 0o755))
	return dir
}
