package platform_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/platform"
)

func TestNormalizeHost(t *testing.T) {
	for _, tc := range []struct {
		kind        platform.Kind
		input, want string
	}{
		{platform.KindGitHub, "", "github.com"},
		{platform.KindGitHub, " HTTPS://GitHub.COM:443/ ", "github.com"},
		{platform.KindGitHub, "Code.Example.org", "code.example.org"},
		{platform.KindGitLab, "Git.Example.org:8443", "git.example.org:8443"},
		{platform.KindGitLab, "https://git.example.org/", "git.example.org"},
		{platform.KindGitea, "[2001:DB8::1]:8443", "[2001:db8::1]:8443"},
		{platform.KindForgejo, "[2001:DB8::1]:443", "[2001:db8::1]"},
	} {
		t.Run(tc.input, func(t *testing.T) {
			require := require.New(t)
			assert := assert.New(t)
			host, err := platform.NormalizeHost(tc.kind, tc.input)
			require.NoError(err)
			assert.Equal(tc.want, host)
			again, err := platform.NormalizeHost(tc.kind, host)
			require.NoError(err)
			assert.Equal(host, again)
			assert.NoError(platform.ValidateCanonicalRepoRef(platform.RepoRef{Platform: tc.kind, Host: host, Owner: "team-a", Name: "project-a"}))
		})
	}
	for _, host := range []string{"example.org:0", "example.org:", "example.org:65536", "example.org:+80", "user@example.org", "https://example.org/a", "example.org?query", "https://example.org/?", "example.org#fragment", "https://example.org/#", "example.org\t:80", "2001:db8::1"} {
		t.Run("reject/"+host, func(t *testing.T) {
			_, err := platform.NormalizeHost(platform.KindGitHub, host)
			assert.Error(t, err)
		})
	}
	_, err := platform.NormalizeHost("", "github.com")
	assert.Error(t, err)
}

func TestNormalizeGitEmail(t *testing.T) {
	for _, tc := range []struct{ input, want string }{
		{" \t<ALICE@EXAMPLE.ORG>\r\n", "alice@example.org"},
		{"< Alice@Example.org >", "alice@example.org"},
		{"Älice+TAG@EXAMPLE.ORG", "Älice+tag@example.org"},
		{"a.b+tag@gmail.com", "a.b+tag@gmail.com"},
		{"<<A@B>>", "<a@b>"},
		{"<A@B", "<a@b"},
		{" < \t > ", ""},
		{"\xff@EXAMPLE.ORG", "\xff@example.org"},
		{"\u00a0A@B\u00a0", "\u00a0a@b\u00a0"},
	} {
		t.Run(tc.input, func(t *testing.T) {
			assert.Equal(t, tc.want, platform.NormalizeGitEmail(tc.input))
		})
	}
}
