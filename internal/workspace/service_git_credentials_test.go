package workspace

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	shellquote "github.com/kballard/go-shellquote"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/testutil/gitsafe"
)

func TestConfigureServiceGitCredentialsOnBareLinkedWorktree(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	root := t.TempDir()
	source := filepath.Join(root, "source")
	commonDir := filepath.Join(root, "clone.git")
	worktree := filepath.Join(root, "worktree")
	sibling := filepath.Join(root, "sibling")
	runWorkspaceTestGit(t, root, "init", "--initial-branch=main", source)
	runWorkspaceTestGit(t, source, "config", "user.email", "test@example.test")
	runWorkspaceTestGit(t, source, "config", "user.name", "Test")
	runWorkspaceTestGit(t, source, "commit", "--allow-empty", "-m", "initial")
	runWorkspaceTestGit(t, root, "clone", "--bare", source, commonDir)
	runWorkspaceTestGit(t, commonDir, "worktree", "add", "-b", "service", worktree, "main")
	runWorkspaceTestGit(t, commonDir, "worktree", "add", "-b", "sibling", sibling, "main")
	runWorkspaceTestGit(t, commonDir, "remote", "set-url", "origin", "git@github.com:acme/widget.git")

	executable := filepath.Join(root, "bin", "kenn forge")
	configPath := filepath.Join(root, "config", "service.toml")
	manager := NewManager(nil, "")
	manager.SetServiceGitCredentials(executable, configPath)
	require.NoError(manager.configureServiceGitCredentials(t.Context(), worktree))

	assert.Equal("false", strings.TrimSpace(string(runWorkspaceTestGit(
		t, worktree, "rev-parse", "--is-bare-repository",
	))))
	assert.Equal("false", strings.TrimSpace(string(runWorkspaceTestGit(
		t, sibling, "rev-parse", "--is-bare-repository",
	))))
	assert.Equal("https://github.com/acme/widget.git", strings.TrimSpace(string(runWorkspaceTestGit(
		t, worktree, "remote", "get-url", "origin",
	))))
	helpers := strings.Split(strings.TrimSuffix(string(runWorkspaceTestGit(
		t, worktree, "config", "--worktree", "--get-all", "credential.helper",
	)), "\n"), "\n")
	require.Len(helpers, 2)
	assert.Empty(helpers[0])
	assert.Equal("!"+shellquote.Join(
		executable, "github", "credential", "--config", configPath,
	), helpers[1])

	if runtime.GOOS == "windows" {
		return
	}
	sshMarker := filepath.Join(root, "ssh-ran")
	fakeSSH := filepath.Join(root, "fake-ssh")
	require.NoError(os.WriteFile(fakeSSH, []byte("#!/bin/sh\ntouch \"$SSH_MARKER\"\nexit 1\n"), 0o700))
	runner := gitsafe.MutableRunner(t)
	_, stderr, err := runner.Run(
		t.Context(), root, nil, "config", "--global",
		"url.git@github.com:.insteadOf", "https://github.com/",
	)
	require.NoError(err, string(stderr))
	runner.Env = append(runner.Env, "GIT_SSH_COMMAND="+fakeSSH, "SSH_MARKER="+sshMarker)
	_, _, err = runner.Run(t.Context(), worktree, nil, "ls-remote", "origin")
	require.Error(err)
	assert.NoFileExists(sshMarker, "protocol.ssh.allow=never must reject inherited HTTPS-to-SSH rewrites")
}
