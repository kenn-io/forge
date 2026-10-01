package gitfixture

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/testutil/gitsafe"
)

func TestMain(m *testing.M) {
	os.Exit(gitsafe.RunIsolatedMain(m))
}

// A push to the fixture's bare origin runs receive-pack without the pusher's
// -c options. Unless the shared test config disables it, receive-pack starts a
// detached `git maintenance run --auto` that can outlive the test and fail its
// temporary-directory cleanup.
func TestPushToFixtureOriginStartsNoBackgroundMaintenance(t *testing.T) {
	repo := NewRepository(t, true)
	repo.Write(t, "next.md", "next\n")
	repo.Stage(t, "next.md")
	Run(t, repo.Dir, "commit", "-m", "next")

	traced := gitsafe.Runner()
	traced.Env = append(traced.Env, "GIT_TRACE=1")
	_, stderr, err := traced.Run(t.Context(), repo.Dir, nil, "push", "origin", "main")
	require.NoError(t, err, "git push: %s", stderr)
	assert.NotContains(t, string(stderr), "maintenance run")
}

// Fixtures refuse to initialize inside an existing checkout, where Git would
// act on the enclosing repository.
func TestFixtureDirectoriesMustBeOutsideRepositories(t *testing.T) {
	require.NoError(t, outsideRepositories(t.Context(), t.TempDir()))

	repo := NewRepository(t, false)
	nested := filepath.Join(repo.Dir, "nested")
	require.NoError(t, os.Mkdir(nested, 0o755))
	require.ErrorContains(t, outsideRepositories(t.Context(), t.TempDir(), nested),
		"inside a Git repository or worktree")
}
