package messages

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	Assert "github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSavedSearchesPathOverride(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(SavedSearchesEnv, filepath.Join(dir, "x.toml"))

	got, err := SavedSearchesPath()

	require.NoError(t, err)
	Assert.Equal(t, filepath.Join(dir, "x.toml"), got)
}

func TestLoadSavedSearchesMissingFileReturnsEmpty(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(SavedSearchesEnv, filepath.Join(dir, "absent.toml"))

	got, err := LoadSavedSearches()

	require.NoError(t, err)
	Assert.Empty(t, got)
}

func TestSaveSavedSearchesRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "saved.toml")
	t.Setenv(SavedSearchesEnv, path)
	in := []SavedSearch{
		{Name: "Recent", Query: "newer_than:7d"},
		{Name: "From alice", Query: "from:alice@example.com"},
	}

	require.NoError(t, SaveSavedSearches(in))
	got, err := LoadSavedSearches()

	require.NoError(t, err)
	Assert.Equal(t, in, got)
}

func TestCanonicalizeSavedSearches(t *testing.T) {
	long := strings.Repeat("x", 600)
	in := []SavedSearch{
		{Name: "", Query: "  has:attachment  "},
		{Name: "  ", Query: ""},
		{Name: "Recent", Query: "newer_than:7d"},
		{Name: "RECENT", Query: "from:alice@example.com"},
		{Name: "Long", Query: long},
		{Name: strings.Repeat("n", 250), Query: "q"},
	}

	out := CanonicalizeSavedSearches(in)

	require.Len(t, out, 4)
	assert := Assert.New(t)
	assert.Equal(SavedSearch{Name: "has:attachment", Query: "has:attachment"}, out[0])
	assert.Equal(SavedSearch{Name: "RECENT", Query: "from:alice@example.com"}, out[1])
	assert.Len(out[2].Query, 500)
	assert.Len(out[3].Name, 200)
}

func TestCanonicalizeSavedSearchesCap(t *testing.T) {
	in := make([]SavedSearch, 60)
	for i := range in {
		in[i] = SavedSearch{Name: "n" + string(rune('A'+i%26)) + string(rune('A'+(i/26)%26)) + string(rune('A'+i)), Query: "q"}
	}

	out := CanonicalizeSavedSearches(in)

	require.Len(t, out, 50)
	Assert.Equal(t, in[len(in)-50:], out)
}

func TestSavedSearchesETagDeterministicAndChanges(t *testing.T) {
	a := []SavedSearch{{Name: "x", Query: "y"}}
	b := []SavedSearch{{Name: "x", Query: "z"}}

	ea1 := SavedSearchesETag(a)
	ea2 := SavedSearchesETag(a)
	eb := SavedSearchesETag(b)

	assert := Assert.New(t)
	assert.Equal(ea1, ea2)
	assert.NotEqual(ea1, eb)
	assert.True(strings.HasPrefix(ea1, `"sha256:`), "etag shape = %q", ea1)
	assert.True(strings.HasSuffix(ea1, `"`), "etag shape = %q", ea1)
}

func TestCanonicalizeSavedSearchesReappearingDuplicateAfterCap(t *testing.T) {
	in := make([]SavedSearch, 0, 61)
	for i := range 60 {
		in = append(in, SavedSearch{Name: fmt.Sprintf("n%d", i), Query: "q"})
	}
	in = append(in, SavedSearch{Name: "n0", Query: "REAPPEARED"})

	out := CanonicalizeSavedSearches(in)

	require.Len(t, out, 50)
	assert := Assert.New(t)
	assert.Equal(SavedSearch{Name: "n0", Query: "REAPPEARED"}, out[len(out)-1])
	for _, e := range out {
		assert.NotEqual(SavedSearch{Name: "n0", Query: "q"}, e)
	}
}

func TestCanonicalizeSavedSearchesTruncatesOnRuneBoundary(t *testing.T) {
	multibyteName := strings.Repeat("✓", 600)
	multibyteQuery := strings.Repeat("✓", 700)

	out := CanonicalizeSavedSearches([]SavedSearch{
		{Name: multibyteName, Query: multibyteQuery},
	})

	require.Len(t, out, 1)
	assert := Assert.New(t)
	assert.NotContains(out[0].Name, "�")
	assert.NotContains(out[0].Query, "�")
	assert.Len([]rune(out[0].Name), 200)
	assert.Len([]rune(out[0].Query), 500)
}

func TestSavedSearchesETagNilAndEmptyEquivalent(t *testing.T) {
	Assert.Equal(t, SavedSearchesETag(nil), SavedSearchesETag([]SavedSearch{}))
}

func TestSaveSavedSearchesAtomicNoHalfFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "saved.toml")
	t.Setenv(SavedSearchesEnv, path)

	require.NoError(t, SaveSavedSearches([]SavedSearch{{Name: "x", Query: "y"}}))
	require.NoError(t, SaveSavedSearches([]SavedSearch{{Name: "x", Query: "z"}}))
	entries, err := os.ReadDir(dir)

	require.NoError(t, err)
	assert := Assert.New(t)
	for _, e := range entries {
		assert.NotContains(e.Name(), ".tmp")
	}
}
