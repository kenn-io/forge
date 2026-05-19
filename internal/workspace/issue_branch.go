package workspace

import (
	"fmt"
	"strings"
	"unicode"

	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"
)

// issueBranchMaxLen caps the full middleman/issue-<n>-<slug> branch
// name. The ceiling is well below git's 255-byte ref limit and leaves
// a small headroom for nextAvailableBranchName to append a "-N"
// disambiguator on collision.
const issueBranchMaxLen = 100

// issueBranchSlugBudget is the upper bound the disambiguator helper
// can append onto issueBranchMaxLen-derived names. Five chars is
// enough for `-NNNN` collision suffixes up to 9999.
const issueBranchSlugBudget = 5

// issueWorkspaceBranch returns middleman's conventional issue-only
// branch name without a title slug. Use this for the "bare" branch
// style and as a fallback when a title cannot be slugified.
func issueWorkspaceBranch(issueNumber int) string {
	return fmt.Sprintf("middleman/issue-%d", issueNumber)
}

// issueWorkspaceBranchWithTitle returns a slugified branch name for an
// issue workspace. It appends a lowercase ASCII slug derived from the
// issue title to the bare form, capped at issueBranchMaxLen and
// truncated at a separator boundary when possible. Titles that
// slugify to an empty string fall back to issueWorkspaceBranch.
func issueWorkspaceBranchWithTitle(issueNumber int, title string) string {
	bare := issueWorkspaceBranch(issueNumber)
	slug := slugifyIssueTitle(title)
	if slug == "" {
		return bare
	}
	budget := issueBranchMaxLen - issueBranchSlugBudget - len(bare) - len("-")
	if budget <= 0 {
		return bare
	}
	if len(slug) > budget {
		slug = truncateSlug(slug, budget)
		if slug == "" {
			return bare
		}
	}
	return bare + "-" + slug
}

// slugifyIssueTitle turns an arbitrary issue title into a safe slug:
// lowercase ASCII letters, digits, and dashes. Non-ASCII letters with
// canonical ASCII decompositions (Latin accented chars) are folded
// down; unconvertible runes (CJK, emoji, other symbols) are dropped.
// Consecutive separators collapse into a single dash, leading and
// trailing separators are trimmed.
func slugifyIssueTitle(title string) string {
	folded := foldToASCII(title)
	var b strings.Builder
	b.Grow(len(folded))
	prevDash := true
	for _, r := range folded {
		switch {
		case r >= 'A' && r <= 'Z':
			b.WriteRune(unicode.ToLower(r))
			prevDash = false
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			prevDash = false
		default:
			if !prevDash {
				b.WriteByte('-')
				prevDash = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}

// foldToASCII normalizes the input via NFKD and drops combining marks
// so that "café" becomes "cafe" and "naïve" becomes "naive". Runes
// that have no ASCII decomposition (CJK, emoji) survive the
// transform but are dropped by the slugifier's allow-list.
func foldToASCII(s string) string {
	t := transform.Chain(
		norm.NFKD,
		runes.Remove(runes.In(unicode.Mn)),
		norm.NFC,
	)
	out, _, err := transform.String(t, s)
	if err != nil {
		return s
	}
	return out
}

// truncateSlug shortens an ASCII slug to at most maxLen bytes,
// preferring to cut at the last dash so a partial word is not left at
// the end. Returns "" if no separator-aligned cut is possible and
// the head chunk would itself be empty after dash trimming.
func truncateSlug(slug string, maxLen int) string {
	if maxLen <= 0 {
		return ""
	}
	if len(slug) <= maxLen {
		return slug
	}
	cut := slug[:maxLen]
	if i := strings.LastIndexByte(cut, '-'); i > 0 {
		cut = cut[:i]
	}
	return strings.Trim(cut, "-")
}
