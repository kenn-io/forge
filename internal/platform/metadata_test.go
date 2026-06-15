package platform

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProviderMetadataForBuiltIns(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	github, ok := MetadataFor(KindGitHub)
	require.True(ok)
	assert.Equal(DefaultGitHubHost, github.DefaultHost)
	assert.False(github.AllowNestedOwner)
	assert.True(github.LowercaseRepoNames)

	gitlab, ok := MetadataFor(KindGitLab)
	require.True(ok)
	assert.Equal(DefaultGitLabHost, gitlab.DefaultHost)
	assert.True(gitlab.AllowNestedOwner)

	forgejo, ok := MetadataFor(KindForgejo)
	require.True(ok)
	assert.Equal(KindForgejo, forgejo.Kind)
	assert.Equal("Forgejo", forgejo.Label)
	assert.Equal(DefaultForgejoHost, forgejo.DefaultHost)
	assert.False(forgejo.AllowNestedOwner)
	assert.False(forgejo.LowercaseRepoNames)

	gitea, ok := MetadataFor(KindGitea)
	require.True(ok)
	assert.Equal(KindGitea, gitea.Kind)
	assert.Equal("Gitea", gitea.Label)
	assert.Equal(DefaultGiteaHost, gitea.DefaultHost)
	assert.False(gitea.AllowNestedOwner)
	assert.False(gitea.LowercaseRepoNames)
}

func TestNormalizeKindAllowsFutureProviderKinds(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	kind, err := NormalizeKind("Codeberg")
	require.NoError(err)
	assert.Equal(Kind("codeberg"), kind)
	assert.True(AllowsNestedOwner(kind))

	_, ok := DefaultHost(kind)
	assert.False(ok)

	fj, err := NormalizeKind("fj")
	require.NoError(err)
	assert.Equal(KindForgejo, fj)

	forgejo, err := NormalizeKind("Forgejo")
	require.NoError(err)
	assert.Equal(KindForgejo, forgejo)

	tea, err := NormalizeKind("tea")
	require.NoError(err)
	assert.Equal(KindGitea, tea)

	gitea, err := NormalizeKind("Gitea")
	require.NoError(err)
	assert.Equal(KindGitea, gitea)
}

func TestNormalizeKindCanonicalizesBuiltInShorthands(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)

	gh, err := NormalizeKind("GH")
	require.NoError(err)
	assert.Equal(KindGitHub, gh)

	gl, err := NormalizeKind(" gl ")
	require.NoError(err)
	assert.Equal(KindGitLab, gl)

	fj, err := NormalizeKind("FJ")
	require.NoError(err)
	assert.Equal(KindForgejo, fj)

	tea, err := NormalizeKind(" tea ")
	require.NoError(err)
	assert.Equal(KindGitea, tea)
}

func TestSupportedProviderListMatchesBuiltInMetadata(t *testing.T) {
	require := require.New(t)

	contents, err := os.ReadFile(filepath.Join("..", "..", "CLAUDE.md"))
	require.NoError(err)

	docProviders := supportedProvidersFromDoc(t, string(contents))
	codeProviders := make([]string, 0, len(builtInMetadata))
	for _, meta := range builtInMetadata {
		codeProviders = append(codeProviders, meta.Label)
	}

	require.ElementsMatch(codeProviders, docProviders)
}

func supportedProvidersFromDoc(t *testing.T, contents string) []string {
	t.Helper()

	section := providerSupportSection(t, contents)
	re := regexp.MustCompile(`(?m)^middleman supports ([^.]+)\.`)
	match := re.FindStringSubmatch(section)
	require.Len(t, match, 2, "Provider Support section must contain the supported-provider sentence")

	list := strings.ReplaceAll(match[1], ", and ", ", ")
	list = strings.ReplaceAll(list, " and ", ", ")
	parts := strings.Split(list, ",")
	providers := make([]string, 0, len(parts))
	for _, part := range parts {
		provider := strings.TrimSpace(part)
		if provider != "" {
			providers = append(providers, provider)
		}
	}
	require.NotEmpty(t, providers, "supported-provider sentence must name at least one provider")
	return providers
}

func providerSupportSection(t *testing.T, contents string) string {
	t.Helper()

	const heading = "## Provider Support\n"
	start := strings.Index(contents, heading)
	require.NotEqual(t, -1, start, "CLAUDE.md must contain a Provider Support section")
	section := contents[start+len(heading):]
	if end := strings.Index(section, "\n## "); end >= 0 {
		section = section[:end]
	}
	return section
}
