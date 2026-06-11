package tokenauth

import (
	"context"
	"os"
	"path/filepath"
	"testing"

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

	got, err := src.Token(context.Background())
	require.NoError(err)
	assert.Equal("first", got)

	require.NoError(os.WriteFile(path, []byte("second\n"), 0o600))
	got, err = src.Token(context.Background())
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

	got, err := src.Token(context.Background())
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

	_, err := src.Token(context.Background())
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

	first, err := src.Token(context.Background())
	require.NoError(err)
	second, err := src.Token(context.Background())
	require.NoError(err)
	src.Invalidate()
	third, err := src.Token(context.Background())
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

	got, err := src.Token(context.Background())
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

func TestMissingTokenErrorIsDetectable(t *testing.T) {
	src := NewManagedSource(Descriptor{
		Key: Key{Platform: "gitlab", Host: "gitlab.com"},
	}, Options{})
	_, err := src.Token(context.Background())
	require.ErrorIs(t, err, ErrMissingToken)
	assert.NotContains(t, err.Error(), "secret")
}
