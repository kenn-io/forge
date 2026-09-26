package tokenauth

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestManagedSourceReadsTokenFileEachCall(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	path := filepath.Join(t.TempDir(), "token")
	require.NoError(os.WriteFile(path, []byte("first\n"), 0o600))
	src := NewManagedSource(Descriptor{
		Key:        Key{Platform: "github", Host: "github.com"},
		Candidates: []Candidate{{Kind: SourceKindFile, FilePath: path}},
	}, Options{})

	got, err := src.Token(t.Context())
	require.NoError(err)
	assert.Equal("first", got)

	require.NoError(os.WriteFile(path, []byte("second\n"), 0o600))
	got, err = src.Token(t.Context())
	require.NoError(err)
	assert.Equal("second", got)
}

func TestManagedSourceFallsThroughEmptyFileAndEnv(t *testing.T) {
	path := filepath.Join(t.TempDir(), "token")
	require.NoError(t, os.WriteFile(path, []byte("\n"), 0o600))
	t.Setenv("PRIMARY_TOKEN", "")
	t.Setenv("SECONDARY_TOKEN", "from-env")
	src := NewManagedSource(Descriptor{
		Key: Key{Platform: "gitlab", Host: "gitlab.com"},
		Candidates: []Candidate{
			{Kind: SourceKindFile, FilePath: path},
			{Kind: SourceKindEnv, EnvName: "PRIMARY_TOKEN"},
			{Kind: SourceKindEnv, EnvName: "SECONDARY_TOKEN"},
		},
	}, Options{})

	got, err := src.Token(t.Context())
	require.NoError(t, err)
	assert.Equal(t, "from-env", got)
}

func TestManagedSourceUnreadableFileDoesNotExposeToken(t *testing.T) {
	src := NewManagedSource(Descriptor{
		Key: Key{Platform: "github", Host: "github.com"},
		Candidates: []Candidate{
			{Kind: SourceKindFile, FilePath: filepath.Join(t.TempDir(), "missing")},
		},
	}, Options{})

	_, err := src.Token(t.Context())
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "ghp_sentinel_secret")
	assert.Contains(t, err.Error(), "token file")
}

func TestManagedSourceGitHubCLIInvalidatesCache(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	calls := 0
	runner := func(context.Context, string) (string, error) {
		calls++
		return []string{"first", "second"}[calls-1], nil
	}
	src := NewManagedSource(Descriptor{
		Key:        Key{Platform: "github", Host: "github.com"},
		Candidates: []Candidate{{Kind: SourceKindGitHubCLI, Host: "github.com"}},
	}, Options{GitHubCLI: runner})

	first, err := src.Token(t.Context())
	require.NoError(err)
	second, err := src.Token(t.Context())
	require.NoError(err)
	src.Invalidate("first")
	third, err := src.Token(t.Context())
	require.NoError(err)

	assert.Equal("first", first)
	assert.Equal("first", second)
	assert.Equal("second", third)
	assert.Equal(2, calls)
}

func TestManagedSourceUpdateChangesDescriptor(t *testing.T) {
	t.Setenv("OLD_TOKEN", "old")
	t.Setenv("NEW_TOKEN", "new")
	src := NewManagedSource(Descriptor{
		Key:        Key{Platform: "github", Host: "github.com"},
		Candidates: []Candidate{{Kind: SourceKindEnv, EnvName: "OLD_TOKEN"}},
	}, Options{})

	src.Update(Descriptor{
		Key:        Key{Platform: "github", Host: "github.com"},
		Candidates: []Candidate{{Kind: SourceKindEnv, EnvName: "NEW_TOKEN"}},
	})

	got, err := src.Token(t.Context())
	require.NoError(t, err)
	assert.Equal(t, "new", got)
}

func TestSourceSetUpsertReusesExistingSource(t *testing.T) {
	set := NewSourceSet(Options{})
	key := Key{Platform: "github", Host: "github.com"}
	first := set.Upsert(Descriptor{
		Key:        key,
		Candidates: []Candidate{{Kind: SourceKindEnv, EnvName: "OLD_TOKEN"}},
	})
	second := set.Upsert(Descriptor{
		Key:        key,
		Candidates: []Candidate{{Kind: SourceKindEnv, EnvName: "NEW_TOKEN"}},
	})

	assert.Same(t, first, second)
	assert.Equal(t, "env:NEW_TOKEN", second.Descriptor().SafeString())
}

func TestSourceSetKeepsScopedSourcesSeparate(t *testing.T) {
	set := NewSourceSet(Options{})
	first := set.Upsert(Descriptor{
		Key: Key{
			Platform: "github",
			Host:     "github.com",
			Scope:    "owner:acme",
		},
		Candidates: []Candidate{{Kind: SourceKindEnv, EnvName: "ACME_TOKEN"}},
	})
	second := set.Upsert(Descriptor{
		Key: Key{
			Platform: "github",
			Host:     "github.com",
			Scope:    "owner:example",
		},
		Candidates: []Candidate{{Kind: SourceKindEnv, EnvName: "EXAMPLE_TOKEN"}},
	})

	assert.NotSame(t, first, second)
	assert.Equal(t, []Key{
		{Platform: "github", Host: "github.com", Scope: "owner:acme"},
		{Platform: "github", Host: "github.com", Scope: "owner:example"},
	}, set.Keys())
}

func TestProbeBatchMintsFreshInstallationTokens(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	var mints int
	set := NewSourceSet(Options{
		GitHubApp: func(context.Context, Candidate) (string, time.Time, error) {
			mints++
			return fmt.Sprintf("token-%d", mints), time.Now().Add(time.Hour), nil
		},
	})
	desc := Descriptor{
		Key: Key{Platform: "github", Host: "github.com", Scope: "owner:acme"},
		Candidates: []Candidate{{
			Kind: SourceKindGitHubApp, Host: "github.com",
			AppID: 7, InstallationID: 11, InstallationAccount: "acme",
		}},
	}
	source := set.Upsert(desc)
	ctx := WithGitHubOwner(t.Context(), "acme")
	token, err := source.Token(ctx)
	require.NoError(err)
	require.Equal("token-1", token)

	batch := set.NewProbeBatch()
	probed, err := batch.ProbeToken(ctx, desc)
	require.NoError(err)
	assert.Equal("token-2", probed,
		"a probe batch must re-mint instead of trusting the live cache")
	again, err := batch.ProbeToken(ctx, desc)
	require.NoError(err)
	assert.Equal("token-2", again, "one mint is shared within the batch")

	live, err := source.Token(ctx)
	require.NoError(err)
	assert.Equal("token-1", live, "probes must not overwrite the live cache")
}

func TestMissingTokenErrorIsDetectable(t *testing.T) {
	src := NewManagedSource(Descriptor{
		Key: Key{Platform: "gitlab", Host: "gitlab.com"},
	}, Options{})
	_, err := src.Token(t.Context())
	require.ErrorIs(t, err, ErrMissingToken)
	assert.NotContains(t, err.Error(), "secret")
}

func TestManagedSourceProviderCLIsUseTheirOwnRunnerAndCache(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	glabCalls, fjCalls := 0, 0
	options := Options{
		GitHubCLI: func(context.Context, string) (string, error) {
			return "", errors.New("github runner must not serve other CLI kinds")
		},
		GitLabCLI: func(_ context.Context, host string) (string, error) {
			glabCalls++
			return "glab-" + host, nil
		},
		ForgejoCLI: func(_ context.Context, host string) (string, error) {
			fjCalls++
			return "fj-" + host, nil
		},
	}

	gitlab := NewManagedSource(Descriptor{
		Key: Key{Platform: "gitlab", Host: "gitlab.example.test"},
		Candidates: []Candidate{
			{Kind: SourceKindEnv, EnvName: "TOKENAUTH_UNSET_GITLAB_ENV"},
			{Kind: SourceKindGitLabCLI, Host: "gitlab.example.test"},
		},
	}, options)
	forgejo := NewManagedSource(Descriptor{
		Key:        Key{Platform: "gitea", Host: "gitea.example.test"},
		Candidates: []Candidate{{Kind: SourceKindForgejoCLI, Host: "gitea.example.test"}},
	}, options)

	for range 2 {
		got, err := gitlab.Token(t.Context())
		require.NoError(err)
		assert.Equal("glab-gitlab.example.test", got)
		got, err = forgejo.Token(t.Context())
		require.NoError(err)
		assert.Equal("fj-gitea.example.test", got)
	}
	assert.Equal(1, glabCalls, "glab lookup is cached per source")
	assert.Equal(1, fjCalls, "fj lookup is cached per source")

	gitlab.Invalidate("glab-gitlab.example.test")
	_, err := gitlab.Token(t.Context())
	require.NoError(err)
	assert.Equal(2, glabCalls, "a rejected token re-runs only its own CLI")
	assert.Equal(1, fjCalls)
}

func TestManagedSourceProviderCLIWithoutRunnerIsMissingToken(t *testing.T) {
	src := NewManagedSource(Descriptor{
		Key:        Key{Platform: "gitlab", Host: "gitlab.example.test"},
		Candidates: []Candidate{{Kind: SourceKindGitLabCLI, Host: "gitlab.example.test"}},
	}, Options{})

	_, err := src.Token(t.Context())
	require.ErrorIs(t, err, ErrMissingToken)
	assert.Contains(t, err.Error(), "gitlab_cli:gitlab.example.test")
}
