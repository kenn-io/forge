package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestValidateRejectsTokenEnvNamesCollidingWithTerminalVars pins the
// disjointness contract: the tmux server permanently retains its spawn
// environment, so a variable in the non-secret terminal set could never
// be scrubbed once declared secret. Collisions are rejected up front,
// at boot and on reload alike.
func TestValidateRejectsTokenEnvNamesCollidingWithTerminalVars(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	require.NoError(t, os.WriteFile(path, []byte(`
github_token_env = "EDITOR"
`), 0o600))
	_, err := Load(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "collides with the non-secret environment")
}

func TestIsTmuxNonSecretEnvVarExactNamesOnly(t *testing.T) {
	assert := assert.New(t)
	assert.True(IsTmuxNonSecretEnvVar("EDITOR"))
	assert.True(IsTmuxNonSecretEnvVar("TMUX_TMPDIR"))
	// The rejection predicate folds case on every platform: token names
	// may never collide with a reserved name in any casing.
	assert.True(IsTmuxNonSecretEnvVar("path"))
	assert.True(IsTmuxNonSecretEnvVar("Editor"))
	assert.False(IsTmuxNonSecretEnvVar("XDG_API_TOKEN"))
	assert.False(IsTmuxNonSecretEnvVar("GITHUB_TOKEN"))
	// The admission predicate is exact: on case-sensitive platforms a
	// variable literally named "editor" is unrelated to EDITOR and must
	// not enter tmux's retained environment.
	assert.True(IsTmuxNonSecretEnvVarExact("EDITOR"))
	assert.False(IsTmuxNonSecretEnvVarExact("editor"))
	assert.False(IsTmuxNonSecretEnvVarExact("path"))
}

// TestValidateRejectsWhitespacePaddedCollisions pins normalization
// order: " PATH " normalizes to PATH later, so collision validation
// must compare trimmed names.
func TestValidateRejectsWhitespacePaddedCollisions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	require.NoError(t, os.WriteFile(path, []byte(`
[[repos]]
owner = "acme"
name = "widget"
token_env = " PATH "
`), 0o600))
	_, err := Load(path)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "collides with the non-secret environment")
}

// TestTokenEnvNamesIncludesExplicitDeclarationsForInvalidProviders pins
// that declared credential names survive provider-identity problems:
// descriptor resolution returns nothing for unknown platforms, but a
// rejected candidate's names must still reach strip accumulation.
func TestTokenEnvNamesIncludesExplicitDeclarationsForInvalidProviders(
	t *testing.T,
) {
	cfg := &Config{
		Platforms: []PlatformConfig{{
			Type: "unknown-provider", Host: "example.com",
			TokenEnv: " WKSP_DECLARED_PLATFORM_TOKEN ",
		}},
		Repos: []Repo{{
			Platform: "unknown-provider", PlatformHost: "example.com",
			Owner: "acme", Name: "widget",
			TokenEnv: "WKSP_DECLARED_REPO_TOKEN",
		}},
	}
	names := cfg.TokenEnvNames()
	assert.Contains(t, names, "WKSP_DECLARED_PLATFORM_TOKEN")
	assert.Contains(t, names, "WKSP_DECLARED_REPO_TOKEN")
}
