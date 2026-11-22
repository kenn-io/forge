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

func TestPrepareProcessRestoresEnvironment(t *testing.T) {
	t.Setenv("GIT_DIR", "inherited-repository")
	t.Setenv("SSH_ASKPASS", "inherited-askpass")
	t.Setenv("GIT_SSH_COMMAND", "")
	require.NoError(t, os.Unsetenv("GIT_SSH_COMMAND"))
	before := make(map[string]string)
	for _, key := range []string{"HOME", "PATH", "GOCACHE", "GOMODCACHE", "GOPATH"} {
		before[key] = os.Getenv(key)
	}
	oldGlobal := os.Getenv("GIT_CONFIG_GLOBAL")
	cleanup, err := PrepareProcess()
	require.NoError(t, err)
	config := os.Getenv("GIT_CONFIG_GLOBAL")
	contents, err := os.ReadFile(config)
	require.NoError(t, err)
	assert.Equal(t, SharedConfig, string(contents))
	assert.Equal(t, "1", os.Getenv("GIT_CONFIG_NOSYSTEM"))
	assert.Empty(t, os.Getenv("GIT_DIR"))
	assert.Empty(t, os.Getenv("SSH_ASKPASS"))
	for key, value := range before {
		assert.Equal(t, value, os.Getenv(key), key)
	}
	require.NoError(t, cleanup())
	assert.Equal(t, "inherited-repository", os.Getenv("GIT_DIR"))
	assert.Equal(t, "inherited-askpass", os.Getenv("SSH_ASKPASS"))
	_, set := os.LookupEnv("GIT_SSH_COMMAND")
	assert.False(t, set)
	assert.Equal(t, oldGlobal, os.Getenv("GIT_CONFIG_GLOBAL"))
	_, err = os.Stat(config)
	assert.True(t, os.IsNotExist(err))
}

func TestPrepareProcessFailurePreservesEnvironment(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")
	for _, key := range []string{"TMPDIR", "TMP", "TEMP"} {
		t.Setenv(key, missing)
	}
	t.Setenv("GIT_DIR", "inherited-repository")
	before := os.Environ()
	_, err := PrepareProcess()
	require.Error(t, err)
	assert.Equal(t, before, os.Environ())
}
