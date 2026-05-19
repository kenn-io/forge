package platform

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestClaudeMdProviderListMatchesRegistry pins the supported-provider list in
// CLAUDE.md to the providers registered in builtInMetadata. CLAUDE.md
// designates one paragraph as the single place that enumerates supported
// providers. When the registry and that paragraph drift, downstream readers
// (contributors, capability discussions, env-var docs) misjudge project scope,
// so we treat the doc as load-bearing and check it from a test.
func TestClaudeMdProviderListMatchesRegistry(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)

	doc, err := os.ReadFile(claudeMdPath(t))
	require.NoError(err, "reading CLAUDE.md")

	enumeration := extractEnumerationParagraph(t, string(doc))

	docProviders := extractProperNouns(enumeration)
	registryProviders := registeredProviderLabels()

	missingFromDoc := setDifference(registryProviders, docProviders)
	extraInDoc := setDifference(docProviders, registryProviders)

	assert.Emptyf(
		missingFromDoc,
		"providers are registered in internal/platform but missing from the "+
			"CLAUDE.md supported-provider paragraph: %v\n"+
			"enumeration paragraph parsed:\n%s\n"+
			"add the provider label to CLAUDE.md's Provider Support paragraph.",
		missingFromDoc, enumeration,
	)
	assert.Emptyf(
		extraInDoc,
		"providers appear in the CLAUDE.md supported-provider paragraph but are "+
			"not registered in internal/platform builtInMetadata: %v\n"+
			"enumeration paragraph parsed:\n%s\n"+
			"either register the provider or remove it from CLAUDE.md.",
		extraInDoc, enumeration,
	)
}

// claudeMdPath resolves CLAUDE.md by walking up from this test file's location
// until it finds go.mod, then returning the CLAUDE.md sibling. This avoids
// depending on the working directory `go test` is invoked from.
func claudeMdPath(t *testing.T) string {
	t.Helper()

	_, thisFile, _, ok := runtime.Caller(0)
	require.True(t, ok, "runtime.Caller(0) returned !ok")

	dir := filepath.Dir(thisFile)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return filepath.Join(dir, "CLAUDE.md")
		}
		parent := filepath.Dir(dir)
		require.NotEqualf(t, parent, dir, "did not find go.mod walking up from %s", thisFile)
		dir = parent
	}
}

// anchorSentence identifies the dedicated supported-provider paragraph in
// CLAUDE.md. The literal text below is asserted to appear in the doc.
const anchorSentence = "This paragraph is the single place CLAUDE.md enumerates supported providers"

// extractEnumerationParagraph locates the supported-provider paragraph by
// anchoring on the self-identifying sentence and returning the paragraph
// directly above it. CLAUDE.md uses one paragraph to enumerate providers and
// the next paragraph to assert the enumeration is unique, so the enumeration
// lives in the paragraph preceding the anchor.
func extractEnumerationParagraph(t *testing.T, doc string) string {
	t.Helper()

	require.Equalf(
		t, 1, strings.Count(doc, anchorSentence),
		"expected exactly one occurrence of the anchor sentence in CLAUDE.md: %q",
		anchorSentence,
	)

	idx := strings.Index(doc, anchorSentence)
	require.GreaterOrEqual(t, idx, 0, "anchor sentence not found")

	// Walk back to the blank line preceding the anchor paragraph.
	prefix := doc[:idx]
	prefix = strings.TrimRight(prefix, " \t\r\n")

	blank := strings.LastIndex(prefix, "\n\n")
	require.GreaterOrEqualf(
		t, blank, 0,
		"could not find a blank line above the anchor sentence; doc structure changed",
	)

	enumeration := strings.TrimSpace(prefix[blank:])
	require.NotEmpty(t, enumeration, "enumeration paragraph was empty")
	return enumeration
}

// properNounRe matches words that look like provider names: an uppercase
// letter followed by one or more letters. This is intentionally permissive so
// that adding a real provider (Codeberg, Bitbucket) lights up the test.
var properNounRe = regexp.MustCompile(`\b[A-Z][a-zA-Z]+\b`)

// docStopwords are non-provider capitalized tokens that legitimately appear in
// the enumeration paragraph. Anything outside this list is treated as a
// candidate provider name and must match a registered Label.
var docStopwords = map[string]struct{}{
	"The": {},
}

func extractProperNouns(paragraph string) map[string]struct{} {
	nouns := map[string]struct{}{}
	for _, match := range properNounRe.FindAllString(paragraph, -1) {
		if _, skip := docStopwords[match]; skip {
			continue
		}
		nouns[match] = struct{}{}
	}
	return nouns
}

func registeredProviderLabels() map[string]struct{} {
	labels := make(map[string]struct{}, len(builtInMetadata))
	for _, meta := range builtInMetadata {
		labels[meta.Label] = struct{}{}
	}
	return labels
}

// setDifference returns the elements in a that are not in b, sorted for stable
// test output.
func setDifference(a, b map[string]struct{}) []string {
	var diff []string
	for k := range a {
		if _, ok := b[k]; !ok {
			diff = append(diff, k)
		}
	}
	sort.Strings(diff)
	return diff
}
