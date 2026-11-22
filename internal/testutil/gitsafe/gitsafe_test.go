package gitsafe

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/procutil"
)

func TestMain(m *testing.M) {
	os.Exit(RunIsolatedMain(m))
}

// If package-level Git isolation stops running, real Git can read a developer's
// identity or system settings and inherited repository bindings during tests.
func TestRunIsolatedMainProtectsRealGitWithPortableConfig(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	globalConfig := os.Getenv("GIT_CONFIG_GLOBAL")
	require.NotEmpty(globalConfig)
	info, err := os.Stat(globalConfig)
	require.NoError(err)
	assert.True(info.Mode().IsRegular(), "global config must be a regular file")
	contents, err := os.ReadFile(globalConfig)
	require.NoError(err)
	assert.Equal(SharedConfig, string(contents),
		"shared test config holds only the background-maintenance settings")
	assert.Equal("1", os.Getenv("GIT_CONFIG_NOSYSTEM"))
	assert.Equal("0", os.Getenv("GIT_TERMINAL_PROMPT"))
	require.DirExists(os.Getenv("XDG_CONFIG_HOME"))

	for _, key := range []string{"GIT_DIR", "GIT_WORK_TREE", "GIT_CONFIG_SYSTEM"} {
		_, present := os.LookupEnv(key)
		assert.False(present, "%s leaked into the test process", key)
	}

	externalHome := t.TempDir()
	externalHomeConfig := filepath.Join(externalHome, ".gitconfig")
	externalContents := []byte("[user]\n\tname = External User\n")
	require.NoError(os.WriteFile(externalHomeConfig, externalContents, 0o600))

	cmd := procutil.Command("git", "config", "--get", "user.name")
	cmd.Dir = externalHome
	cmd.Env = replaceEnv(os.Environ(), map[string]string{
		"HOME": externalHome,
	})
	_, err = cmd.Output()
	require.Error(err, "Git unexpectedly read external home config")

	homeContents, err := os.ReadFile(externalHomeConfig)
	require.NoError(err)
	assert.Equal(externalContents, homeContents)
}

// If go commands that tests run treat the isolated HOME as their own, every
// test binary downloads a read-only module cache there that its cleanup cannot
// remove, and the developer's go env file stops applying.
func TestRunIsolatedMainKeepsGoDirectoriesOutOfIsolatedHome(t *testing.T) {
	home := os.Getenv("HOME")
	require.NotEmpty(t, home)

	out, err := procutil.Command("go", "env", "GOENV", "GOPATH", "GOMODCACHE", "GOCACHE").Output()
	require.NoError(t, err)
	for dir := range strings.Lines(string(out)) {
		dir = strings.TrimSpace(dir)
		assert.NotContains(t, dir, home, "go directory %q is inside the isolated HOME", dir)
	}
}

// If a test that writes global Git config shares the package config, parallel
// tests can observe or overwrite that setting.
func TestMutableRunnerKeepsGlobalWritesPrivate(t *testing.T) {
	require := require.New(t)

	runner := MutableRunner(t)
	_, _, err := runner.Run(
		t.Context(), "", nil, "config", "--global", "user.name", "Private Test User",
	)
	require.NoError(err)

	out, err := runner.Output(t.Context(), "", "config", "--global", "--get", "user.name")
	require.NoError(err)
	assert.Equal(t, "Private Test User", strings.TrimSpace(string(out)))

	_, err = Runner().Output(t.Context(), "", "config", "--global", "--get", "user.name")
	require.Error(err, "private global config leaked into the package config")
}

func TestPrepareProcessIsolatesGitAndKeepsHome(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	callerHome := t.TempDir()
	callerXDG := t.TempDir()
	tempName := "git scratch"
	if runtime.GOOS != "windows" {
		tempName += "\"\\\n\t\b"
	}
	tempRoot := filepath.Join(t.TempDir(), tempName)
	require.NoError(os.Mkdir(tempRoot, 0o700))
	for _, key := range []string{"TMPDIR", "TMP", "TEMP"} {
		t.Setenv(key, tempRoot)
	}
	t.Setenv("HOME", callerHome)
	t.Setenv("XDG_CONFIG_HOME", callerXDG)
	for _, key := range []string{"GIT_CONFIG_GLOBAL", "GIT_CONFIG_NOSYSTEM", "GIT_TERMINAL_PROMPT"} {
		t.Setenv(key, os.Getenv(key))
	}

	cleanup, err := PrepareProcess()
	require.NoError(err)
	t.Cleanup(func() { _ = cleanup() })

	assert.Equal(callerHome, os.Getenv("HOME"))
	assert.Equal(callerXDG, os.Getenv("XDG_CONFIG_HOME"))
	root := filepath.Dir(os.Getenv("GIT_CONFIG_GLOBAL"))
	for _, key := range []string{"core.excludesFile", "core.attributesFile"} {
		out, err := Runner().Output(t.Context(), t.TempDir(), "config", "--global", "--path", "--get", key)
		require.NoError(err, key)
		path := strings.TrimSpace(string(out))
		assert.Equal(root, filepath.Dir(path), "%s is outside the scratch root", key)
		contents, err := os.ReadFile(path)
		require.NoError(err, key)
		assert.Empty(contents, key)
	}

	// The receive-pack hook below needs a POSIX shell.
	if runtime.GOOS != "windows" {
		repos := t.TempDir()
		_, err := Runner().Output(t.Context(), repos, "rev-parse", "--absolute-git-dir")
		require.Error(err, "fixture must be outside any repository or worktree")
		remote := filepath.Join(repos, "remote.git")
		local := filepath.Join(repos, "local")
		_, stderr, err := Runner().Run(t.Context(), repos, nil, "init", "--bare", remote)
		require.NoError(err, string(stderr))
		_, stderr, err = Runner().Run(t.Context(), repos, nil, "init", local)
		require.NoError(err, string(stderr))
		hook := `#!/bin/sh
git config --get maintenance.auto > "$(dirname "$0")/config"
git config --get gc.auto >> "$(dirname "$0")/config"
`
		require.NoError(os.WriteFile(filepath.Join(remote, "hooks", "pre-receive"), []byte(hook), 0o755))
		_, stderr, err = Runner().Run(t.Context(), local, nil, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "--allow-empty", "-m", "fixture")
		require.NoError(err, string(stderr))
		_, stderr, err = Runner().Run(t.Context(), local, nil, "push", remote, "HEAD:refs/heads/main")
		require.NoError(err, string(stderr))
		config, err := os.ReadFile(filepath.Join(remote, "hooks", "config"))
		require.NoError(err)
		assert.Equal("false\n0\n", string(config))
	}

	require.NoError(cleanup())
	assert.NoDirExists(root)
}

func replaceEnv(base []string, replacements map[string]string) []string {
	out := make([]string, 0, len(base)+len(replacements))
	for _, entry := range base {
		key, _, _ := strings.Cut(entry, "=")
		if _, replaced := replacements[strings.ToUpper(key)]; replaced {
			continue
		}
		out = append(out, entry)
	}
	for key, value := range replacements {
		out = append(out, key+"="+value)
	}
	return out
}
