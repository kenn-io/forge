package testutil

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/testutil/dbtest"
	gitcmd "go.kenn.io/kit/git/cmd"
)

// TestSetupDiffRepoDoesNotLeakIntoHostGitDir guards against regression
// of the `.git/config` contamination bug. SetupDiffRepo shells out to
// git several times; if the test binary is invoked from a git hook
// (prek pre-commit fires `go test`), the outer git exports GIT_DIR,
// GIT_WORK_TREE, and friends. Without stripping, `git config
// user.email kenn-forge-fixture@example.invalid` inside the fixture's workrepo honors
// the inherited GIT_DIR and writes to the hosting repo's .git/config
// instead, leaving a stray [user] block behind.
//
// This test simulates the hook context by pointing GIT_DIR /
// GIT_WORK_TREE at a throwaway "host" repo and asserts SetupDiffRepo
// leaves it untouched.
func TestSetupDiffRepoDoesNotLeakIntoHostGitDir(t *testing.T) {
	r := require.New(t)

	host := t.TempDir()
	initCmd := gitcmd.New().Command(t.Context(), "", "init", "-q", "-b", "main", host)
	initCmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + os.Getenv("HOME"),
	}
	r.NoError(initCmd.Run(), "seed host repo")

	hostConfig := filepath.Join(host, ".git", "config")
	before, err := os.ReadFile(hostConfig)
	r.NoError(err)

	t.Setenv("GIT_DIR", filepath.Join(host, ".git"))
	t.Setenv("GIT_WORK_TREE", host)

	fixtureDir := t.TempDir()
	database := dbtest.OpenAt(t, filepath.Join(fixtureDir, "test.db"))

	// Run the fixture. Ignore any error so the contamination check
	// still runs even if the leak makes a subsequent git operation
	// crash partway through — a regressed strip helper tends to
	// blow up at `git commit` long before returning.
	_, setupErr := SetupDiffRepo(t.Context(), fixtureDir, database)

	after, err := os.ReadFile(hostConfig)
	r.NoError(err)
	r.Equal(string(before), string(after),
		"SetupDiffRepo mutated the host .git/config via leaked GIT_DIR")
	r.NoError(setupErr, "SetupDiffRepo should succeed under hook-simulated env")
}

func TestSetupDiffRepoUsesRepositoryIncarnationClonePath(t *testing.T) {
	r := require.New(t)

	fixtureDir := t.TempDir()
	database := dbtest.OpenAt(t, filepath.Join(fixtureDir, "test.db"))

	result, err := SetupDiffRepo(t.Context(), fixtureDir, database)
	r.NoError(err)

	repo, err := database.GetRepoByIdentity(
		t.Context(),
		db.GitHubRepoIdentity("github.com", "acme", "widgets"),
	)
	r.NoError(err)
	r.NotNil(repo)

	clonePath, err := result.Manager.RepositoryClone(
		repo.ID, "github", "github.com", "acme", "widgets",
	).ClonePath()
	r.NoError(err)
	r.Equal(
		filepath.Join(
			fixtureDir,
			"clones",
			"repository-incarnations",
			"local",
			"repositories",
			fmt.Sprintf("repo-%d.git", repo.ID),
		),
		clonePath,
	)
	r.FileExists(filepath.Join(clonePath, "HEAD"))
}
