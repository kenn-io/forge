package workspace

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	Assert "github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/middleman/internal/gitclone"
	"go.kenn.io/middleman/internal/tokenauth"
)

// branchSyncTestManager builds a Manager with no clone manager configured, so
// PushWorktreeBranch and PullWorktreeBranch fall back to plain git against the
// worktree's existing remote. The networked-runner path is exercised
// separately by TestPushWorktreeBranchUsesAuthenticatedRunnerAndMutationAuth.
func branchSyncTestManager(t *testing.T) *Manager {
	t.Helper()
	return NewManager(nil, t.TempDir())
}

func TestPushWorktreeBranchPushesAheadCommitsAndRunsHooks(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)
	work := setupDivergenceWorktree(t)
	marker := filepath.Join(filepath.Dir(work), "pre-push-ran")
	hook := filepath.Join(work, ".git", "hooks", "pre-push")
	require.NoError(os.WriteFile(
		hook,
		[]byte("#!/bin/sh\nprintf ran > "+marker+"\n"),
		0o755,
	))
	require.NoError(os.WriteFile(
		filepath.Join(work, "f.txt"), []byte("ahead\n"), 0o644,
	))
	runWorkspaceTestGit(t, work, "add", ".")
	runWorkspaceTestGit(t, work, "commit", "-m", "ahead")

	require.NoError(branchSyncTestManager(t).PushWorktreeBranch(t.Context(), "", work))

	div, ok, err := WorktreeDivergence(t.Context(), work)
	require.NoError(err)
	require.True(ok)
	assert.Equal(Divergence{}, div)
	assert.FileExists(marker)
}

func TestPullWorktreeBranchFastForwardsBehindBranch(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)
	work := setupDivergenceWorktree(t)
	other := filepath.Join(filepath.Dir(work), "other")
	remote := filepath.Join(filepath.Dir(work), "remote.git")
	runWorkspaceTestGit(t, filepath.Dir(work), "clone", remote, other)
	runWorkspaceTestGit(t, other, "config", "user.email", "o@test.com")
	runWorkspaceTestGit(t, other, "config", "user.name", "Other")
	runWorkspaceTestGit(t, other, "checkout", "-b", "feature", "origin/feature")
	require.NoError(os.WriteFile(
		filepath.Join(other, "f.txt"), []byte("remote-extra\n"), 0o644,
	))
	runWorkspaceTestGit(t, other, "add", ".")
	runWorkspaceTestGit(t, other, "commit", "-m", "remote extra")
	runWorkspaceTestGit(t, other, "push", "origin", "feature")

	require.NoError(branchSyncTestManager(t).PullWorktreeBranch(t.Context(), "", work))

	contents, err := os.ReadFile(filepath.Join(work, "f.txt"))
	require.NoError(err)
	assert.Equal("remote-extra\n", string(contents))
	div, ok, err := WorktreeDivergence(t.Context(), work)
	require.NoError(err)
	require.True(ok)
	assert.Equal(Divergence{}, div)
}

func TestPushWorktreeBranchRejectsDivergedBranch(t *testing.T) {
	require := require.New(t)
	work := setupDivergenceWorktree(t)
	require.NoError(os.WriteFile(
		filepath.Join(work, "f.txt"), []byte("local\n"), 0o644,
	))
	runWorkspaceTestGit(t, work, "add", ".")
	runWorkspaceTestGit(t, work, "commit", "-m", "local")

	other := filepath.Join(filepath.Dir(work), "other")
	remote := filepath.Join(filepath.Dir(work), "remote.git")
	runWorkspaceTestGit(t, filepath.Dir(work), "clone", remote, other)
	runWorkspaceTestGit(t, other, "config", "user.email", "o@test.com")
	runWorkspaceTestGit(t, other, "config", "user.name", "Other")
	runWorkspaceTestGit(t, other, "checkout", "-b", "feature", "origin/feature")
	require.NoError(os.WriteFile(
		filepath.Join(other, "f.txt"), []byte("remote\n"), 0o644,
	))
	runWorkspaceTestGit(t, other, "add", ".")
	runWorkspaceTestGit(t, other, "commit", "-m", "remote")
	runWorkspaceTestGit(t, other, "push", "origin", "feature")

	err := branchSyncTestManager(t).PushWorktreeBranch(t.Context(), "", work)

	require.Error(err)
	Assert.New(t).ErrorIs(err, ErrWorktreeDiverged)
}

func TestPullWorktreeBranchRejectsDirtyWorktree(t *testing.T) {
	require := require.New(t)
	work := setupDivergenceWorktree(t)
	require.NoError(os.WriteFile(
		filepath.Join(work, "dirty.txt"), []byte("dirty\n"), 0o644,
	))

	err := branchSyncTestManager(t).PullWorktreeBranch(t.Context(), "", work)

	require.Error(err)
	Assert.New(t).ErrorIs(err, ErrWorktreeDirty)
}

// TestPushWorktreeBranchUsesAuthenticatedRunnerAndMutationAuth proves that
// when clone management is configured the networked branch-sync steps route
// through the host's authenticated git runner so a provider credential is
// always injected, and that the push specifically resolves the user's own PAT
// chain (mutation auth) while the fetches resolve the read-preferred GitHub App
// installation token. A fake git on PATH stands in for an authenticated
// remote: it rejects any push or fetch that arrives without a credential and
// records which token each networked operation carried.
func TestPushWorktreeBranchUsesAuthenticatedRunnerAndMutationAuth(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)
	work := setupDivergenceWorktree(t)
	require.NoError(os.WriteFile(
		filepath.Join(work, "f.txt"), []byte("ahead\n"), 0o644,
	))
	runWorkspaceTestGit(t, work, "add", ".")
	runWorkspaceTestGit(t, work, "commit", "-m", "ahead")

	realGit, err := exec.LookPath("git")
	require.NoError(err)
	fakeDir := t.TempDir()
	capturePath := filepath.Join(fakeDir, "credentials.txt")
	require.NoError(os.WriteFile(filepath.Join(fakeDir, "git"), []byte(`#!/bin/sh
set -eu
real="${MIDDLEMAN_TEST_REAL_GIT:?}"
capture="${MIDDLEMAN_TEST_GIT_CAPTURE:?}"
op="${1:-}"
case "$op" in
push|fetch)
	helper=""
	i=0
	count="${GIT_CONFIG_COUNT:-0}"
	while [ "$i" -lt "$count" ]; do
		eval "key=\${GIT_CONFIG_KEY_$i:-}"
		eval "value=\${GIT_CONFIG_VALUE_$i:-}"
		if [ "$key" = "credential.helper" ]; then
			helper="$value"
		fi
		i=$((i + 1))
	done
	if [ -z "$helper" ]; then
		echo "fatal: Authentication failed: no credential helper" >&2
		exit 128
	fi
	password="$("$helper" get | sed -n 's/^password=//p')"
	if [ -z "$password" ]; then
		echo "fatal: Authentication failed: empty credential" >&2
		exit 128
	fi
	printf '%s:%s\n' "$op" "$password" >> "$capture"
	;;
esac
exec "$real" "$@"
`), 0o755))
	t.Setenv("MIDDLEMAN_TEST_REAL_GIT", realGit)
	t.Setenv("MIDDLEMAN_TEST_GIT_CAPTURE", capturePath)
	t.Setenv("PATH", fakeDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	const patEnv = "MIDDLEMAN_TEST_BRANCH_SYNC_PAT"
	t.Setenv(patEnv, "pat-token")
	source := tokenauth.NewManagedSource(
		tokenauth.Descriptor{
			Key: tokenauth.Key{Platform: "github", Host: "github.com"},
			Candidates: []tokenauth.Candidate{
				{
					Kind:           tokenauth.SourceKindGitHubApp,
					Host:           "github.com",
					AppID:          1,
					InstallationID: 123,
				},
				{Kind: tokenauth.SourceKindEnv, EnvName: patEnv},
			},
		},
		tokenauth.Options{
			GitHubApp: func(context.Context, tokenauth.Candidate) (string, time.Time, error) {
				return "app-token", time.Now().Add(time.Hour), nil
			},
		},
	)
	mgr := NewManager(nil, t.TempDir())
	mgr.SetClones(gitclone.New(
		t.TempDir(), map[string]tokenauth.Source{"github.com": source},
	))

	require.NoError(mgr.PushWorktreeBranch(t.Context(), "github.com", work))

	data, err := os.ReadFile(capturePath)
	require.NoError(err)
	ops := strings.Split(strings.TrimSpace(string(data)), "\n")
	// The push must carry the user's PAT, never the app installation token,
	// so the pushed commits stay attributed to the user rather than the bot.
	assert.Contains(ops, "push:pat-token")
	assert.NotContains(ops, "push:app-token")
	// Reads (the upstream refresh fetches) prefer the app installation token.
	assert.Contains(ops, "fetch:app-token")
	assert.NotContains(ops, "fetch:pat-token")

	div, ok, err := WorktreeDivergence(t.Context(), work)
	require.NoError(err)
	require.True(ok)
	assert.Equal(Divergence{}, div)
}
