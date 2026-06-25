package tokenauth

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestCanonicalSourceString(t *testing.T) {
	for _, tc := range []struct {
		name       string
		candidates []Candidate
		want       string
	}{
		{
			name: "empty is none",
			want: "none",
		},
		{
			name: "repeated env collapses to one",
			candidates: []Candidate{
				{Kind: SourceKindEnv, EnvName: "SHARED"},
				{Kind: SourceKindEnv, EnvName: "SHARED"},
			},
			want: "env:SHARED",
		},
		{
			name: "distinct chain preserves order",
			candidates: []Candidate{
				{Kind: SourceKindEnv, EnvName: "A"},
				{Kind: SourceKindEnv, EnvName: "B"},
			},
			want: "env:A -> env:B",
		},
		{
			name: "ignores fields irrelevant to the kind",
			candidates: []Candidate{
				{Kind: SourceKindEnv, EnvName: "A", Host: "ignored"},
				{Kind: SourceKindEnv, EnvName: "A", FilePath: "ignored"},
			},
			want: "env:A",
		},
		{
			name: "mixed kinds",
			candidates: []Candidate{
				{Kind: SourceKindFile, FilePath: "/run/token"},
				{Kind: SourceKindEnv, EnvName: "A"},
				{Kind: SourceKindGitHubCLI, Host: "github.com"},
			},
			want: "file:/run/token -> env:A -> github_cli:github.com",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			desc := Descriptor{Candidates: tc.candidates}
			assert.Equal(t, tc.want, desc.CanonicalSourceString())
		})
	}
}

// TestCanonicalSourceStringEqualityMirrorsResolution captures the property the
// same-host clone-token validators rely on: two descriptors whose raw chains
// differ only by duplicate/redundant candidates share one canonical string,
// while genuinely different chains do not. SafeString keeps the raw spelling.
func TestCanonicalSourceStringEqualityMirrorsResolution(t *testing.T) {
	repeated := Descriptor{Candidates: []Candidate{
		{Kind: SourceKindEnv, EnvName: "SHARED"},
		{Kind: SourceKindEnv, EnvName: "SHARED"},
	}}
	single := Descriptor{Candidates: []Candidate{
		{Kind: SourceKindEnv, EnvName: "SHARED"},
	}}
	different := Descriptor{Candidates: []Candidate{
		{Kind: SourceKindEnv, EnvName: "OTHER"},
	}}

	assert.Equal(t, single.CanonicalSourceString(), repeated.CanonicalSourceString())
	assert.NotEqual(t, single.CanonicalSourceString(), different.CanonicalSourceString())
	assert.Equal(t, "env:SHARED -> env:SHARED", repeated.SafeString())
}

// TestHasActiveGitHubAppForOwner pins the gate to the same owner scoping
// that githubAppToken applies when resolving a token: an account-scoped
// app serves only its installation account, an unscoped app serves every
// owner, and an app with no installation id mints nothing.
func TestHasActiveGitHubAppForOwner(t *testing.T) {
	scoped := Candidate{
		Kind: SourceKindGitHubApp, Host: "github.com",
		AppID: 1, InstallationID: 10, InstallationAccount: "kenn-io",
	}
	unscoped := Candidate{
		Kind: SourceKindGitHubApp, Host: "github.com",
		AppID: 2, InstallationID: 20,
	}
	uninstalled := Candidate{
		Kind: SourceKindGitHubApp, Host: "github.com", AppID: 3,
	}
	pat := Candidate{Kind: SourceKindEnv, EnvName: "TOKEN"}

	for _, tc := range []struct {
		name       string
		candidates []Candidate
		owner      string
		want       bool
	}{
		{name: "scoped app matches its account", candidates: []Candidate{scoped, pat}, owner: "kenn-io", want: true},
		{name: "scoped app match is case-insensitive", candidates: []Candidate{scoped}, owner: "Kenn-IO", want: true},
		{name: "scoped app skipped for other owner", candidates: []Candidate{scoped, pat}, owner: "acme", want: false},
		{name: "scoped app skipped without an owner", candidates: []Candidate{scoped}, owner: "", want: false},
		{name: "unscoped app serves any owner", candidates: []Candidate{unscoped}, owner: "acme", want: true},
		{name: "uninstalled app mints nothing", candidates: []Candidate{uninstalled}, owner: "kenn-io", want: false},
		{name: "pat-only chain has no app", candidates: []Candidate{pat}, owner: "kenn-io", want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			desc := Descriptor{Candidates: tc.candidates}
			assert.Equal(t, tc.want, desc.HasActiveGitHubAppForOwner(tc.owner))
		})
	}
}
