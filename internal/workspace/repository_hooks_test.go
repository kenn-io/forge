package workspace

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	gitcmd "go.kenn.io/kit/git/cmd"
)

func TestRoborevManagedCloneServerAddress(t *testing.T) {
	tests := []struct {
		endpoint string
		want     string
		wantErr  bool
	}{
		{endpoint: "http://127.0.0.1:7373", want: "127.0.0.1:7373"},
		{endpoint: "http://localhost:7373", want: "localhost:7373"},
		{endpoint: "http://[::1]:7373", want: "[::1]:7373"},
		{endpoint: "https://127.0.0.1:7373", wantErr: true},
		{endpoint: "http://roborev.example:7373", wantErr: true},
		{endpoint: "unix:///tmp/roborev.sock", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.endpoint, func(t *testing.T) {
			got, err := roborevManagedCloneServerAddress(tt.endpoint)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestAllowedRoborevSnapshotDir(t *testing.T) {
	assert := assert.New(t)
	assert.True(roborevSnapshotDirAllowed("reviews", "reviews"))
	assert.True(roborevSnapshotDirAllowed(".roborev", "reviews"))
	assert.False(roborevSnapshotDirAllowed("branch-controlled", "reviews"))
	assert.False(roborevSnapshotDirAllowed("../outside", "reviews"))
	assert.False(roborevSnapshotDirAllowed(".git/reviews", ".git/reviews"))
	assert.False(roborevSnapshotDirAllowed("bad\npath", "bad\npath"))
}

func TestManagedCloneExcludeIsWorktreeScoped(t *testing.T) {
	require := require.New(t)
	root := t.TempDir()
	source := filepath.Join(root, "source")
	commonDir := filepath.Join(root, "managed.git")
	firstWorktree := filepath.Join(root, "first")
	secondWorktree := filepath.Join(root, "second")
	userHome := t.TempDir()
	t.Setenv("HOME", userHome)
	userGit := gitcmd.Runner{StripEnv: true}
	globalExclude := filepath.Join(userHome, "global-exclude")
	require.NoError(os.WriteFile(globalExclude, []byte("/editor-cache/\n"), 0o644))
	_, stderr, err := userGit.Run(
		t.Context(), root, nil, "config", "--global", "core.excludesFile", globalExclude,
	)
	require.NoError(err, string(stderr))

	runWorkspaceTestGit(t, root, "init", "--initial-branch=main", source)
	runWorkspaceTestGit(t, source, "config", "user.email", "test@example.com")
	runWorkspaceTestGit(t, source, "config", "user.name", "Test")
	runWorkspaceTestGit(t, source, "commit", "--allow-empty", "-m", "initial")
	runWorkspaceTestGit(t, root, "clone", "--bare", source, commonDir)
	runWorkspaceTestGit(t, commonDir, "worktree", "add", "-b", "first", firstWorktree, "main")
	_, stderr, err = userGit.Run(
		t.Context(), firstWorktree, nil,
		"check-ignore", "--quiet", "--", "editor-cache/settings.json",
	)
	require.NoError(err, string(stderr))

	require.NoError(ensureManagedCloneExclude(t.Context(), commonDir, firstWorktree, "review*data"))
	assertGitIgnored(t, firstWorktree, "review*data/snapshot.json")
	assertGitNotIgnored(t, firstWorktree, "review-public-data/snapshot.json")
	_, stderr, err = userGit.Run(
		t.Context(), firstWorktree, nil,
		"check-ignore", "--quiet", "--", "editor-cache/settings.json",
	)
	require.NoError(err, string(stderr))

	runWorkspaceTestGit(t, commonDir, "worktree", "add", "-b", "second", secondWorktree, "main")
	assertGitNotIgnored(t, secondWorktree, "review*data/snapshot.json")

	worktreeExclude := filepath.Join(userHome, "worktree-exclude")
	require.NoError(os.WriteFile(worktreeExclude, []byte("/workspace-cache/\n"), 0o644))
	_, stderr, err = userGit.Run(
		t.Context(), firstWorktree, nil,
		"config", "--worktree", "core.excludesFile", worktreeExclude,
	)
	require.NoError(err, string(stderr))
	require.NoError(ensureManagedCloneExclude(t.Context(), commonDir, firstWorktree, "new-reviews"))
	assertGitNotIgnored(t, firstWorktree, "review*data/snapshot.json")
	assertGitIgnored(t, firstWorktree, "new-reviews/snapshot.json")
	_, stderr, err = userGit.Run(
		t.Context(), firstWorktree, nil,
		"check-ignore", "--quiet", "--", "workspace-cache/settings.json",
	)
	require.NoError(err, string(stderr))

	require.NoError(ensureManagedCloneExclude(t.Context(), commonDir, firstWorktree, "final-reviews"))
	assertGitNotIgnored(t, firstWorktree, "new-reviews/snapshot.json")
	assertGitIgnored(t, firstWorktree, "final-reviews/snapshot.json")
	_, stderr, err = userGit.Run(
		t.Context(), firstWorktree, nil,
		"check-ignore", "--quiet", "--", "workspace-cache/settings.json",
	)
	require.NoError(err, string(stderr))
}

func TestManagedCloneExcludePreservesImplicitGlobalIgnore(t *testing.T) {
	tests := []struct {
		name    string
		useXDG  bool
		setPath func(home, xdg string) string
	}{
		{
			name: "home config",
			setPath: func(home, _ string) string {
				return filepath.Join(home, ".config", "git", "ignore")
			},
		},
		{
			name:   "XDG config",
			useXDG: true,
			setPath: func(_, xdg string) string {
				return filepath.Join(xdg, "git", "ignore")
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require := require.New(t)
			root := t.TempDir()
			source := filepath.Join(root, "source")
			commonDir := filepath.Join(root, "managed.git")
			worktree := filepath.Join(root, "worktree")
			userHome := t.TempDir()
			xdgHome := ""
			if tt.useXDG {
				xdgHome = t.TempDir()
			}
			t.Setenv("HOME", userHome)
			t.Setenv("XDG_CONFIG_HOME", xdgHome)
			ignorePath := tt.setPath(userHome, xdgHome)
			require.NoError(os.MkdirAll(filepath.Dir(ignorePath), 0o755))
			require.NoError(os.WriteFile(ignorePath, []byte("/editor-cache/\n"), 0o644))

			runWorkspaceTestGit(t, root, "init", "--initial-branch=main", source)
			runWorkspaceTestGit(t, source, "config", "user.email", "test@example.com")
			runWorkspaceTestGit(t, source, "config", "user.name", "Test")
			runWorkspaceTestGit(t, source, "commit", "--allow-empty", "-m", "initial")
			runWorkspaceTestGit(t, root, "clone", "--bare", source, commonDir)
			runWorkspaceTestGit(t, commonDir, "worktree", "add", "-b", "first", worktree, "main")

			userGit := gitcmd.Runner{StripEnv: true}
			_, stderr, err := userGit.Run(
				t.Context(), worktree, nil,
				"check-ignore", "--quiet", "--", "editor-cache/settings.json",
			)
			require.NoError(err, string(stderr))

			require.NoError(ensureManagedCloneExclude(t.Context(), commonDir, worktree, "reviews"))
			_, stderr, err = userGit.Run(
				t.Context(), worktree, nil,
				"check-ignore", "--quiet", "--", "editor-cache/settings.json",
			)
			require.NoError(err, string(stderr))

			require.NoError(ensureManagedCloneExclude(t.Context(), commonDir, worktree, "new-reviews"))
			_, stderr, err = userGit.Run(
				t.Context(), worktree, nil,
				"check-ignore", "--quiet", "--", "editor-cache/settings.json",
			)
			require.NoError(err, string(stderr))
		})
	}
}

func TestValidateManagedCloneHooksDirRejectsExistingEffectiveHooks(t *testing.T) {
	require := require.New(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")
	root := t.TempDir()
	source := filepath.Join(root, "source")
	commonDir := filepath.Join(root, "managed.git")
	firstWorktree := filepath.Join(root, "first")
	secondWorktree := filepath.Join(root, "second")
	customHooks := filepath.Join(root, "custom-hooks")
	require.NoError(os.MkdirAll(customHooks, 0o755))
	hookPath := filepath.Join(customHooks, "pre-push")
	hookContent := []byte("#!/bin/sh\n# existing security hook\n")
	require.NoError(os.WriteFile(hookPath, hookContent, 0o755))

	runWorkspaceTestGit(t, root, "init", "--initial-branch=main", source)
	runWorkspaceTestGit(t, source, "config", "user.email", "test@example.com")
	runWorkspaceTestGit(t, source, "config", "user.name", "Test")
	runWorkspaceTestGit(t, source, "commit", "--allow-empty", "-m", "initial")
	runWorkspaceTestGit(t, root, "clone", "--bare", source, commonDir)
	runWorkspaceTestGit(t, commonDir, "worktree", "add", "-b", "first", firstWorktree, "main")
	runWorkspaceTestGit(t, commonDir, "worktree", "add", "-b", "second", secondWorktree, "main")
	runWorkspaceTestGit(t, commonDir, "config", "--local", "core.hooksPath", customHooks)

	err := validateManagedCloneHooksDir(t.Context(), commonDir, firstWorktree)
	require.ErrorContains(err, "existing hooks directory")
	resolved, resolveErr := effectiveHooksDir(t.Context(), secondWorktree)
	require.NoError(resolveErr)
	canonicalCustomHooks, canonicalErr := canonicalFilesystemPath(customHooks)
	require.NoError(canonicalErr)
	assert.Equal(t, canonicalCustomHooks, resolved)
	content, readErr := os.ReadFile(hookPath)
	require.NoError(readErr)
	assert.Equal(t, hookContent, content)
}
