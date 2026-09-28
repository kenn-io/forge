package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDecodeLegacyRepositoryID(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want int64
		ok   bool
	}{
		{name: "decimal", raw: " 7001 ", want: 7001, ok: true},
		{name: "github next node id", raw: "R_kgDOR00v_A", want: 1196240892, ok: true},
		{name: "github legacy node id", raw: "MDEwOlJlcG9zaXRvcnkxMjk2MjY5", want: 1296269, ok: true},
		{name: "github next node id small value", raw: "R_kgAq", want: 42, ok: true},
		{name: "zero", raw: "0", ok: false},
		{name: "negative", raw: "-4", ok: false},
		{name: "other github node type", raw: "PR_kwDOR00v_M5", ok: false},
		{name: "opaque text", raw: "enterprise-service", ok: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := decodeLegacyRepositoryID(tt.raw)
			assert.Equal(t, tt.ok, ok)
			if tt.ok {
				assert.Equal(t, tt.want, got)
			}
		})
	}
}

func TestLoadOrCreateUpgradesTextRepositoryIDs(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	t.Setenv("KENN_FORGE_HOME", t.TempDir())
	path := filepath.Join(t.TempDir(), "config.toml")
	require.NoError(os.WriteFile(path, []byte(`
[[repos]]
owner = "acme"
name = "widgets"
platform_repo_id = "R_kgDOR00v_A"

[[repos]]
owner = "acme"
name = "gadgets"
platform = "gitlab"
platform_host = "gitlab.com"
platform_repo_id = "7001"

[[repos]]
owner = "acme"
name = "tools"
platform_repo_id = "not-an-id"

[[repo_presets]]
name = "work"

[[repo_presets.repos]]
provider = "github"
platform_host = "github.com"
platform_repo_id = "MDEwOlJlcG9zaXRvcnkxMjk2MjY5"
repo_path = "acme/widgets"

[[repo_presets.repos]]
provider = "github"
platform_host = "github.com"
platform_repo_id = "not-an-id"
repo_path = "acme/tools"
`), 0o600))

	cfg, err := LoadOrCreate(path)
	require.NoError(err)

	require.Len(cfg.Repos, 3)
	assert.Equal(int64(1196240892), cfg.Repos[0].PlatformRepoID)
	assert.Equal(int64(7001), cfg.Repos[1].PlatformRepoID)
	assert.Zero(cfg.Repos[2].PlatformRepoID, "an undecodable pin is dropped, not fatal")
	require.Len(cfg.RepoPresets, 1)
	assert.Equal([]RepoPresetRepository{{
		Provider: "github", PlatformHost: "github.com",
		PlatformRepoID: 1296269, RepoPath: "acme/widgets",
	}}, cfg.RepoPresets[0].Repos)

	saved, err := os.ReadFile(path)
	require.NoError(err)
	assert.NotContains(string(saved), `platform_repo_id = "`)
	reloaded, err := Load(path)
	require.NoError(err)
	assert.False(reloaded.upgradedRepositoryIDs, "the saved file holds integer IDs")
	assert.Equal(cfg.Repos[0].PlatformRepoID, reloaded.Repos[0].PlatformRepoID)
	assert.Equal(cfg.RepoPresets, reloaded.RepoPresets)
}

func TestLoadAcceptsTextRepositoryIDsWithoutSaving(t *testing.T) {
	require := require.New(t)
	path := filepath.Join(t.TempDir(), "config.toml")
	original := "[[repos]]\nowner = \"acme\"\nname = \"widgets\"\nplatform_repo_id = \"42\"\n"
	require.NoError(os.WriteFile(path, []byte(original), 0o600))

	cfg, err := Load(path)
	require.NoError(err)

	assert.Equal(t, int64(42), cfg.Repos[0].PlatformRepoID)
	saved, err := os.ReadFile(path)
	require.NoError(err)
	assert.Equal(t, original, string(saved), "only daemon startup rewrites the file")
}
