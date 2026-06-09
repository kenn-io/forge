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
