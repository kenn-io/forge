package tokenauth

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func githubAppDescriptor(installationID int64) Descriptor {
	return Descriptor{
		Key: Key{Platform: "github", Host: "github.com"},
		Candidates: []Candidate{
			{
				Kind:           SourceKindGitHubApp,
				Host:           "github.com",
				FilePath:       "/keys/app.pem",
				AppID:          77,
				InstallationID: installationID,
			},
			{Kind: SourceKindEnv, EnvName: "TEST_GITHUB_APP_FALLBACK"},
		},
	}
}

func TestGitHubAppTokenMintAndCache(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	var mints atomic.Int64
	src := NewManagedSource(githubAppDescriptor(42), Options{
		GitHubApp: func(_ context.Context, c Candidate) (string, time.Time, error) {
			mints.Add(1)
			assert.Equal(int64(77), c.AppID)
			assert.Equal(int64(42), c.InstallationID)
			assert.Equal("/keys/app.pem", c.FilePath)
			return "ghs_minted", time.Now().Add(time.Hour), nil
		},
	})

	token, err := src.Token(context.Background())
	require.NoError(err)
	assert.Equal("ghs_minted", token)

	// A second resolve inside the expiry window reuses the cache.
	token, err = src.Token(context.Background())
	require.NoError(err)
	assert.Equal("ghs_minted", token)
	assert.Equal(int64(1), mints.Load())

	// Invalidate (e.g. a 401 retry in AuthTransport) forces a re-mint.
	src.Invalidate()
	_, err = src.Token(context.Background())
	require.NoError(err)
	assert.Equal(int64(2), mints.Load())
}

func TestGitHubAppTokenRemintsNearExpiry(t *testing.T) {
	var mints atomic.Int64
	src := NewManagedSource(githubAppDescriptor(42), Options{
		GitHubApp: func(context.Context, Candidate) (string, time.Time, error) {
			mints.Add(1)
			// Always within the refresh skew, so every resolve re-mints.
			return "ghs_shortlived", time.Now().Add(time.Minute), nil
		},
	})
	for range 2 {
		_, err := src.Token(context.Background())
		require.NoError(t, err)
	}
	assert.Equal(t, int64(2), mints.Load())
}

func TestGitHubAppNotInstalledFallsThrough(t *testing.T) {
	t.Setenv("TEST_GITHUB_APP_FALLBACK", "pat-token")
	src := NewManagedSource(githubAppDescriptor(0), Options{
		GitHubApp: func(context.Context, Candidate) (string, time.Time, error) {
			return "", time.Time{}, errors.New("must not be called for installation 0")
		},
	})
	token, err := src.Token(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "pat-token", token)
}

func TestGitHubAppNilMinterFallsThrough(t *testing.T) {
	t.Setenv("TEST_GITHUB_APP_FALLBACK", "pat-token")
	src := NewManagedSource(githubAppDescriptor(42), Options{})
	token, err := src.Token(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "pat-token", token)
}

func TestGitHubAppRequiresMatchingOwnerScope(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	t.Setenv("TEST_GITHUB_APP_FALLBACK", "pat-token")
	var mints atomic.Int64
	desc := githubAppDescriptor(42)
	desc.Candidates[0].InstallationAccount = "kenn-io"
	src := NewManagedSource(desc, Options{
		GitHubApp: func(context.Context, Candidate) (string, time.Time, error) {
			mints.Add(1)
			return "ghs_minted", time.Now().Add(time.Hour), nil
		},
	})

	token, err := src.Token(context.Background())
	require.NoError(err)
	assert.Equal("pat-token", token)

	token, err = src.Token(WithGitHubOwner(context.Background(), "mariusvniekerk"))
	require.NoError(err)
	assert.Equal("pat-token", token)

	token, err = src.Token(WithGitHubOwner(context.Background(), "Kenn-IO"))
	require.NoError(err)
	assert.Equal("ghs_minted", token)
	assert.Equal(int64(1), mints.Load())
}

func TestGitHubAppCacheIsScopedToInstallationAccount(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	desc := githubAppDescriptor(42)
	desc.Candidates = []Candidate{
		{
			Kind:                SourceKindGitHubApp,
			Host:                "github.com",
			FilePath:            "/tmp/app.pem",
			AppID:               77,
			InstallationID:      42,
			InstallationAccount: "kenn-io",
		},
		{
			Kind:                SourceKindGitHubApp,
			Host:                "github.com",
			FilePath:            "/tmp/app.pem",
			AppID:               77,
			InstallationID:      43,
			InstallationAccount: "other-org",
		},
	}
	minted := make(map[int64]int)
	src := NewManagedSource(desc, Options{
		GitHubApp: func(_ context.Context, c Candidate) (string, time.Time, error) {
			minted[c.InstallationID]++
			return fmt.Sprintf("ghs_%d", c.InstallationID), time.Now().Add(time.Hour), nil
		},
	})

	token, err := src.Token(WithGitHubOwner(context.Background(), "kenn-io"))
	require.NoError(err)
	assert.Equal("ghs_42", token)

	token, err = src.Token(WithGitHubOwner(context.Background(), "other-org"))
	require.NoError(err)
	assert.Equal("ghs_43", token)

	token, err = src.Token(WithGitHubOwner(context.Background(), "kenn-io"))
	require.NoError(err)
	assert.Equal("ghs_42", token)
	assert.Equal(map[int64]int{42: 1, 43: 1}, minted)
}

func TestGitHubAppMintFailureSurfacesError(t *testing.T) {
	t.Setenv("TEST_GITHUB_APP_FALLBACK", "pat-token")
	src := NewManagedSource(githubAppDescriptor(42), Options{
		GitHubApp: func(context.Context, Candidate) (string, time.Time, error) {
			return "", time.Time{}, errors.New("key rejected")
		},
	})
	// Mint failures must not silently degrade to the PAT chain: the
	// app exists because the PAT budget is exhausted.
	_, err := src.Token(context.Background())
	require.Error(t, err)
	require.ErrorContains(t, err, "key rejected")
	require.ErrorContains(t, err, "github_app:77@github.com")
}

func TestMutationAuthSkipsGitHubAppCandidate(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	t.Setenv("TEST_GITHUB_APP_FALLBACK", "user-pat")
	var mints atomic.Int64
	src := NewManagedSource(githubAppDescriptor(42), Options{
		GitHubApp: func(context.Context, Candidate) (string, time.Time, error) {
			mints.Add(1)
			return "ghs_minted", time.Now().Add(time.Hour), nil
		},
	})

	// Mutation-marked resolution must bypass the app and land on the
	// user's PAT so writes are attributed to the user.
	token, err := src.Token(WithMutationAuth(context.Background()))
	require.NoError(err)
	assert.Equal("user-pat", token)
	assert.Zero(mints.Load())

	// Unmarked resolution still mints the app token.
	token, err = src.Token(context.Background())
	require.NoError(err)
	assert.Equal("ghs_minted", token)
	assert.Equal(int64(1), mints.Load())
}

func TestGitHubAppDescriptorUpdateClearsCache(t *testing.T) {
	var mints atomic.Int64
	src := NewManagedSource(githubAppDescriptor(42), Options{
		GitHubApp: func(context.Context, Candidate) (string, time.Time, error) {
			mints.Add(1)
			return "ghs_minted", time.Now().Add(time.Hour), nil
		},
	})
	_, err := src.Token(context.Background())
	require.NoError(t, err)

	// Pointing the source at a different installation must drop the
	// cached token: it was scoped to the old installation's repos.
	src.Update(githubAppDescriptor(43))
	_, err = src.Token(context.Background())
	require.NoError(t, err)
	assert.Equal(t, int64(2), mints.Load())
}
