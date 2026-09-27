package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/platform"
)

func TestBitbucketRepoAndCredentials(t *testing.T) {
	assert := assert.New(t)
	repo := Repo{Owner: "Team", Name: "Widgets", Platform: "bitbucket"}
	require.NoError(t, repo.normalize("github.com"))
	assert.Equal("bitbucket.org", repo.PlatformHost)
	assert.Equal("Team", repo.Owner)
	assert.Equal("Widgets", repo.Name)
	cfg := &Config{Repos: []Repo{repo}}
	desc := cfg.TokenSourceForPlatformHost("bitbucket", "bitbucket.org", "", "")
	assert.Contains(desc.SafeString(), "env:KENN_FORGE_BITBUCKET_TOKEN")
	assert.Contains(cfg.TokenEnvNames(), "BKT_TOKEN")
	kind, ok := platformForRepoRefHost("bitbucket.org", "github")
	assert.True(ok)
	assert.Equal("bitbucket", kind)
	assert.Equal("refs/pull-requests/42/from", platform.MergeRequestHeadRef(platform.KindBitbucket, 42))
}

func TestBitbucketDataCenterUsesExplicitHostAndCredentials(t *testing.T) {
	repo := Repo{Platform: "bitbucket", PlatformHost: "bitbucket.example.com", Owner: "PROJECT", Name: "widgets", TokenEnv: "BITBUCKET_DC_TEST_TOKEN"}
	require.NoError(t, repo.normalize("github.com"))
	t.Setenv("BITBUCKET_DC_TEST_TOKEN", "user:token")
	cfg := &Config{Repos: []Repo{repo}}
	assert.Equal(t, "bitbucket.example.com", repo.PlatformHost)
	assert.Equal(t, "user:token", cfg.TokenForPlatformHost("bitbucket", repo.PlatformHost, repo.TokenEnv))
	desc := cfg.TokenSourceForPlatformHost("bitbucket", repo.PlatformHost, repo.TokenEnv, "")
	assert.NotContains(t, desc.SafeString(), "bitbucket_env")
}
