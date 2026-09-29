package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCheckSourceFlagsRawGitInTests(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	src := `package widget

import (
	"os"
	"os/exec"

	run "go.kenn.io/forge/internal/procutil"
	gitcmd "go.kenn.io/kit/git/cmd"
)

const gitBin = "git"

func setup(t *testing.T) {
	exec.Command("git", "init")
	exec.CommandContext(t.Context(), "/usr/bin/git", "status")
	run.Command(gitBin, "config", "user.name", "x")
	run.CommandContext(t.Context(), "git.exe", "log")
	_ = os.Getenv("GIT_SAFE_REAL_GIT")
	_ = gitcmd.Runner{StripEnv: true}
	_ = &gitcmd.Runner{}
	var zero gitcmd.Runner
	_ = zero
	_ = gitcmd.New()
}
`
	diagnostics, err := checkSource("internal/widget/widget_test.go", src)
	require.NoError(err)

	lines := make([]int, 0, len(diagnostics))
	for _, diagnostic := range diagnostics {
		lines = append(lines, diagnostic.Line)
	}
	assert.Equal([]int{14, 15, 16, 17, 18, 19, 20, 21, 23}, lines)
	assert.Equal(rawGitMessage, diagnostics[0].Message)
	assert.Equal(realGitMessage, diagnostics[4].Message)
	assert.Equal(runnerMessage, diagnostics[5].Message)
}

func TestCheckSourceAllowsFixturesAndOtherPrograms(t *testing.T) {
	require := require.New(t)

	src := `package widget

import (
	"os/exec"

	"go.kenn.io/forge/internal/procutil"
	"go.kenn.io/forge/internal/testutil/gitfixture"
	"go.kenn.io/forge/internal/testutil/gitsafe"
)

func setup(t *testing.T) {
	repo := gitfixture.NewRepository(t, false)
	gitfixture.Run(t, repo.Dir, "status")
	_, _ = gitsafe.Runner().Output(t.Context(), repo.Dir, "log")
	_, _ = exec.LookPath("git")
	exec.Command("tmux", "new-session")
	procutil.Command("go", "list")
	_ = "git"
}
`
	diagnostics, err := checkSource("internal/widget/widget_test.go", src)
	require.NoError(err)
	require.Empty(diagnostics)
}

func TestCheckSourceScope(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	src := `package widget

import "os/exec"

func run() { exec.Command("git", "status") }
`
	// Production code runs Git through its own runners; only test code is
	// in scope, plus the shared test helper packages.
	for _, tc := range []struct {
		path    string
		flagged bool
	}{
		{"internal/widget/widget.go", false},
		{"internal/widget/widget_test.go", true},
		{"internal/testutil/helpers.go", true},
		{"internal/testutil/reposeed/seed.go", true},
		{"internal/testutil/gitsafe/gitsafe.go", false},
		{"internal/testutil/gitfixture/history_test.go", false},
	} {
		diagnostics, err := checkSource(tc.path, src)
		require.NoError(err)
		assert.Equal(tc.flagged, len(diagnostics) == 1, tc.path)
	}
}

func TestCheckSourcesRequiresIsolatedMainForFixturePackages(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	usesFixture := `package widget

import "go.kenn.io/forge/internal/testutil/gitfixture"

func TestRepo(t *testing.T) { gitfixture.NewRepository(t, false) }
`
	isolatedMain := `package widget

import "go.kenn.io/forge/internal/testutil/gitsafe"

func TestMain(m *testing.M) { os.Exit(gitsafe.RunIsolatedMain(m)) }
`
	diagnostics, err := checkSources(map[string]string{
		"internal/widget/repo_test.go": usesFixture,
	})
	require.NoError(err)
	require.Len(diagnostics, 1)
	assert.Equal(isolatedMainMessage, diagnostics[0].Message)
	assert.Equal("internal/widget/repo_test.go", diagnostics[0].Path)

	diagnostics, err = checkSources(map[string]string{
		"internal/widget/repo_test.go": usesFixture,
		"internal/widget/main_test.go": isolatedMain,
	})
	require.NoError(err)
	assert.Empty(diagnostics)
}
