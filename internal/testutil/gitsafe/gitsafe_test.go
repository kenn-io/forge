package gitsafe

import (
	"os"
	"path/filepath"
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

// A push into a local bare repository runs receive-pack without the pusher's
// -c options. Unless the shared config disables it, receive-pack starts a
// detached `git maintenance run --auto` that can outlive the test and fail its
// temporary-directory cleanup.
func TestPushIntoBareRepositoryStartsNoBackgroundMaintenance(t *testing.T) {
	require := require.New(t)
	require.Empty(os.Getenv("GIT_DIR"), "TestMain must remove inherited repository bindings")
	require.Equal("1", os.Getenv("GIT_CONFIG_NOSYSTEM"), "system Git config must stay disabled")
	globalConfig, err := os.ReadFile(os.Getenv("GIT_CONFIG_GLOBAL"))
	require.NoError(err, "TestMain must install a scratch global config")
	require.Equal(SharedConfig, string(globalConfig))
	dir := t.TempDir()
	runner := Runner()
	_, err = runner.Output(t.Context(), dir, "rev-parse", "--absolute-git-dir")
	require.Error(err, "fixture must be outside all repositories and worktrees")
	run := func(dir string, args ...string) {
		t.Helper()
		_, stderr, err := runner.Run(t.Context(), dir, nil, args...)
		require.NoError(err, "git %v: %s", args, stderr)
	}
	run(dir, "init", "--bare", "--initial-branch=trunk", "remote.git")
	run(dir, "clone", "remote.git", "work")
	work := filepath.Join(dir, "work")
	run(work, "-c", "user.name=Test", "-c", "user.email=test@example.com",
		"commit", "--allow-empty", "-m", "first")

	traced := runner
	traced.Env = append(append([]string{}, runner.Env...), "GIT_TRACE=1")
	_, stderr, err := traced.Run(t.Context(), work, nil, "push", "origin", "trunk")
	require.NoError(err, "git push: %s", stderr)
	assert.NotContains(t, string(stderr), "maintenance run")
}
