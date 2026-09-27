package tokenauth

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBitbucketEnvironmentCredentialPrecedenceAndHost(t *testing.T) {
	assert := assert.New(t)
	t.Setenv("BKT_HOST", "https://bitbucket.org")
	t.Setenv("BKT_TOKEN", "cli-secret")
	t.Setenv("BKT_USERNAME", "user@example.com")
	t.Setenv("BKT_AUTH_METHOD", "basic")
	t.Setenv("FORGE_TEST_BITBUCKET_TOKEN", "explicit-secret")
	source := NewManagedSource(Descriptor{Key: Key{Platform: "bitbucket", Host: "bitbucket.org"}, Candidates: []Candidate{
		{Kind: SourceKindEnv, EnvName: "FORGE_TEST_BITBUCKET_TOKEN"},
		{Kind: SourceKindBitbucketEnv, Host: "bitbucket.org"},
	}}, Options{})
	token, err := source.Token(t.Context())
	require.NoError(t, err)
	assert.Equal("explicit-secret", token)
	t.Setenv("FORGE_TEST_BITBUCKET_TOKEN", "")
	token, err = source.Token(t.Context())
	require.NoError(t, err)
	assert.Equal("user@example.com:cli-secret", token)
	t.Setenv("BKT_AUTH_METHOD", "bearer")
	token, err = source.Token(t.Context())
	require.NoError(t, err)
	assert.Equal("cli-secret", token)
	t.Setenv("BKT_HOST", "https://bitbucket.example.test")
	_, err = source.Token(t.Context())
	require.ErrorIs(t, err, ErrMissingToken)
}
