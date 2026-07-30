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
	assert.Zero(info.Size(), "shared test config must stay empty")
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
