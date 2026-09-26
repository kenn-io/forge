package db

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestParseSearchQuery(t *testing.T) {
	t.Parallel()

	tests := []struct {
		search string
		want   SearchQuery
	}{
		{search: "fix bug", want: SearchQuery{Include: []string{"fix", "bug"}}},
		{search: "!alice", want: SearchQuery{Exclude: []string{"alice"}}},
		{search: "NOT alice", want: SearchQuery{Exclude: []string{"alice"}}},
		{search: "! alice", want: SearchQuery{Exclude: []string{"alice"}}},
		{search: "fix NOT alice bug", want: SearchQuery{Include: []string{"fix", "bug"}, Exclude: []string{"alice"}}},
		{search: `!"needs review" fix`, want: SearchQuery{Include: []string{"fix"}, Exclude: []string{"needs review"}}},
		{search: `NOT "needs review"`, want: SearchQuery{Exclude: []string{"needs review"}}},
		// Lowercase "not" and quoted operators are ordinary words.
		{search: "do not merge", want: SearchQuery{Include: []string{"do", "not", "merge"}}},
		{search: `"NOT" "!"`, want: SearchQuery{Include: []string{"NOT", "!"}}},
		// "!" only negates at the start of a term.
		{search: "wow!", want: SearchQuery{Include: []string{"wow!"}}},
		// A dangling operator is ignored while the query is still being typed.
		{search: "fix NOT", want: SearchQuery{Include: []string{"fix"}}},
		{search: "fix !", want: SearchQuery{Include: []string{"fix"}}},
		{search: "can't", want: SearchQuery{Include: []string{"can't"}}},
	}
	for _, tt := range tests {
		t.Run(tt.search, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, ParseSearchQuery(tt.search))
		})
	}
}
