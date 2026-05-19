package workspace

import (
	"strings"
	"testing"

	Assert "github.com/stretchr/testify/assert"
)

func TestIssueWorkspaceBranchSlug(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name        string
		number      int
		title       string
		want        string
		mustContain string
	}{
		{
			name:   "typical title",
			number: 1234,
			title:  "Add foo to bar",
			want:   "middleman/issue-1234-add-foo-to-bar",
		},
		{
			name:   "empty title falls back to bare",
			number: 7,
			title:  "",
			want:   "middleman/issue-7",
		},
		{
			name:   "whitespace title falls back to bare",
			number: 8,
			title:  "   \t\n",
			want:   "middleman/issue-8",
		},
		{
			name:   "all-punctuation title falls back to bare",
			number: 9,
			title:  "!@#$%^&*()",
			want:   "middleman/issue-9",
		},
		{
			name:   "slashes are sanitized",
			number: 10,
			title:  "feat/api: add /v2 endpoint",
			want:   "middleman/issue-10-feat-api-add-v2-endpoint",
		},
		{
			name:   "leading and trailing dots removed",
			number: 11,
			title:  "...refactor things...",
			want:   "middleman/issue-11-refactor-things",
		},
		{
			name:   "leading slash and dot are stripped from slug",
			number: 12,
			title:  "./.config bug",
			want:   "middleman/issue-12-config-bug",
		},
		{
			name:   "accented letters are reduced to ascii",
			number: 13,
			title:  "naïve café façade",
			want:   "middleman/issue-13-naive-cafe-facade",
		},
		{
			name:   "cjk characters are stripped",
			number: 14,
			title:  "添加 features please",
			want:   "middleman/issue-14-features-please",
		},
		{
			name:   "cjk-only title falls back to bare",
			number: 15,
			title:  "添加新功能",
			want:   "middleman/issue-15",
		},
		{
			name:   "emoji are stripped",
			number: 16,
			title:  "Fix: bug 🐛 hits everyone 🚀",
			want:   "middleman/issue-16-fix-bug-hits-everyone",
		},
		{
			name:   "emoji-only title falls back to bare",
			number: 17,
			title:  "🐛🚀🐢",
			want:   "middleman/issue-17",
		},
		{
			name:   "uppercase is lowercased",
			number: 18,
			title:  "FOO BAR BAZ",
			want:   "middleman/issue-18-foo-bar-baz",
		},
		{
			name:   "internal dots collapsed",
			number: 19,
			title:  "v1.2.3 release",
			want:   "middleman/issue-19-v1-2-3-release",
		},
		{
			name:   "consecutive separators collapsed",
			number: 20,
			title:  "a   --  b---c",
			want:   "middleman/issue-20-a-b-c",
		},
		{
			name:   "numbers preserved",
			number: 21,
			title:  "Bug 9000 in module 42",
			want:   "middleman/issue-21-bug-9000-in-module-42",
		},
		{
			name:   "ascii control characters dropped",
			number: 22,
			title:  "tab\tand\nnewline",
			want:   "middleman/issue-22-tab-and-newline",
		},
		{
			name:   "underscore mapped to dash",
			number: 23,
			title:  "fix snake_case parser",
			want:   "middleman/issue-23-fix-snake-case-parser",
		},
		{
			name:        "very long title truncated under cap",
			number:      999,
			title:       strings.Repeat("word ", 200),
			mustContain: "middleman/issue-999-word",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := issueWorkspaceBranchWithTitle(tc.number, tc.title)

			assert := Assert.New(t)
			if tc.want != "" {
				assert.Equal(tc.want, got)
			}
			if tc.mustContain != "" {
				assert.Contains(got, tc.mustContain)
			}
			assert.LessOrEqual(
				len(got), issueBranchMaxLen,
				"branch %q exceeds %d-char cap",
				got, issueBranchMaxLen,
			)
			assert.False(strings.HasSuffix(got, "-"),
				"branch %q should not end with -", got)
			assert.False(strings.HasSuffix(got, "."),
				"branch %q should not end with .", got)
			assert.NotContains(got, "..",
				"branch %q must not contain ..", got)
			assert.NotContains(got, "//",
				"branch %q must not contain //", got)
			assert.False(strings.HasPrefix(got, "/"),
				"branch %q must not start with /", got)
		})
	}
}

func TestIssueWorkspaceBranchSlugTruncatesAtWordBoundary(t *testing.T) {
	t.Parallel()
	assert := Assert.New(t)

	long := strings.Repeat("alpha-beta-gamma-delta ", 20)
	got := issueWorkspaceBranchWithTitle(42, long)

	assert.LessOrEqual(len(got), issueBranchMaxLen)
	// Truncation should land cleanly on a separator, not mid-word.
	assert.False(strings.HasSuffix(got, "-"),
		"trailing dash on %q signals raw truncation", got)
	// Should still carry the slug prefix, not just fall back to bare.
	assert.True(strings.HasPrefix(got, "middleman/issue-42-alpha"),
		"unexpected prefix in %q", got)
}

func TestIssueWorkspaceBranchSlugProducesValidGitRef(t *testing.T) {
	t.Parallel()
	assert := Assert.New(t)

	cases := []struct {
		number int
		title  string
	}{
		{1, "Add foo to bar"},
		{2, ""},
		{3, strings.Repeat("very long ", 50)},
		{4, "./.dotfile -- weird-- name"},
		{5, "naïve café résumé"},
		{6, "🚀 launch the rocket 🛰️"},
		{7, "中文 标题 with mixed text"},
	}

	for _, tc := range cases {
		branch := issueWorkspaceBranchWithTitle(tc.number, tc.title)
		err := validateLocalBranchName(t.Context(), "", branch)
		assert.NoError(err, "branch %q for title %q should be valid",
			branch, tc.title)
	}
}

func TestIssueWorkspaceBranchBareIgnoresTitle(t *testing.T) {
	t.Parallel()
	assert := Assert.New(t)

	assert.Equal(
		"middleman/issue-42",
		issueWorkspaceBranch(42),
	)
}
