package docs

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strings"
	"unicode"
)

// Hard limits enforced by the body scanner. Constants so tests can
// reference the same numbers; tune as one place.
const (
	// MaxBodyFileSize skips files larger than this before opening them.
	MaxBodyFileSize int64 = 1 << 20 // 1 MiB
	// MaxBodyScanBuf is the per-line cap for bufio.Scanner. Raised
	// above bufio's default 64 KiB so long markdown lines (e.g. a
	// pasted code dump on one line) don't fail. Must stay strictly
	// less than MaxBodyFileSize so a file can pass the size check yet
	// still trigger ErrTooLong in tests.
	MaxBodyScanBuf = 256 * 1024 // 256 KiB
	// MaxSnippetRunes caps the snippet text returned to clients. The
	// snippet is centered on the first match.
	MaxSnippetRunes = 200
	// MaxScoreCap caps a body hit's score so a runaway file can't drown
	// out filename hits.
	MaxScoreCap = 100
)

// SnippetRange is a Unicode code-point range inside a Snippet.Text,
// half-open: [Start, End). Clients render <mark> boundaries by slicing
// the text into code-point arrays - NEVER by bytes - so non-ASCII
// content (emoji, accented letters) doesn't misalign the highlight.
type SnippetRange struct {
	Start int `json:"start"`
	End   int `json:"end"`
}

// BodySnippet is the line-or-window of text returned for a body hit
// plus the positions inside that text where the query matched.
type BodySnippet struct {
	Text    string         `json:"text"`
	Matches []SnippetRange `json:"matches"`
}

// BodyHit is the first body match for a single file plus an aggregate
// score across all matching lines in that file.
type BodyHit struct {
	Line    int // 1-based; 0 means no match
	Snippet BodySnippet
	Score   int // 10 * matching-line count, capped at MaxScoreCap
}

// scanBody opens absPath and returns the first matching line for the
// lowercased query, plus an aggregate score. A non-empty warning
// indicates a recoverable per-file condition (oversize, token-too-long)
// that callers should surface to the user.
//
// query MUST already be lowercased - scanBody does the case-folding on
// the file side only.
func scanBody(absPath, lowerQuery string) (BodyHit, string, error) {
	if lowerQuery == "" {
		return BodyHit{}, "", nil
	}
	info, err := os.Stat(absPath)
	if err != nil {
		return BodyHit{}, "", err
	}
	if !info.Mode().IsRegular() {
		return BodyHit{}, "", fmt.Errorf("%w: path %q is not a regular file", ErrInvalidFolder, absPath)
	}
	if info.Size() > MaxBodyFileSize {
		return BodyHit{}, fmt.Sprintf("%s: skipped (file over %d bytes)", absPath, MaxBodyFileSize), nil
	}
	f, err := os.Open(absPath)
	if err != nil {
		return BodyHit{}, "", err
	}
	defer func() { _ = f.Close() }()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, MaxBodyScanBuf), MaxBodyScanBuf)

	var first BodyHit
	matched := 0
	line := 0
	for scanner.Scan() {
		line++
		text := scanner.Text()
		if !containsFold(text, lowerQuery) {
			continue
		}
		matched++
		if matched > 1 {
			continue
		}
		first = BodyHit{Line: line, Snippet: buildSnippet(text, lowerQuery)}
	}
	if err := scanner.Err(); err != nil {
		if errors.Is(err, bufio.ErrTooLong) {
			return BodyHit{}, fmt.Sprintf("%s: line too long, body scan skipped", absPath), nil
		}
		return BodyHit{}, "", err
	}
	if matched == 0 {
		return BodyHit{}, "", nil
	}
	first.Score = min(matched*10, MaxScoreCap)
	return first, "", nil
}

// containsFold reports whether text contains lowerQuery, case-insensitively.
func containsFold(text, lowerQuery string) bool {
	return strings.Contains(strings.ToLower(text), lowerQuery)
}

// buildSnippet produces a code-point-windowed view of text centered on
// the first match. Match offsets are code-point indices relative to
// the returned Snippet.Text, so the frontend can render <mark> spans
// via Array.from(text) without byte/rune misalignment.
func buildSnippet(text, lowerQuery string) BodySnippet {
	matches := findRuneMatches(text, lowerQuery)
	runes := []rune(text)
	if len(runes) <= MaxSnippetRunes || len(matches) == 0 {
		return BodySnippet{Text: text, Matches: matches}
	}
	first := matches[0].Start
	// A bit of left context, the rest right context.
	start := max(first-MaxSnippetRunes/4, 0)
	end := start + MaxSnippetRunes
	if end > len(runes) {
		end = len(runes)
		start = max(end-MaxSnippetRunes, 0)
	}
	snipText := string(runes[start:end])
	adjusted := make([]SnippetRange, 0, len(matches))
	for _, m := range matches {
		if m.Start < start || m.End > end {
			continue
		}
		adjusted = append(adjusted, SnippetRange{
			Start: m.Start - start,
			End:   m.End - start,
		})
	}
	return BodySnippet{Text: snipText, Matches: adjusted}
}

// findRuneMatches walks text by rune and returns all positions where a
// case-folded match for lowerQuery begins. Offsets are code-point
// indices, not byte indices, so the frontend doesn't have to deal with
// Go's mixed byte/rune model.
func findRuneMatches(text, lowerQuery string) []SnippetRange {
	textRunes := []rune(text)
	queryRunes := []rune(lowerQuery)
	if len(queryRunes) == 0 || len(textRunes) < len(queryRunes) {
		return nil
	}
	var out []SnippetRange
	i := 0
	for i+len(queryRunes) <= len(textRunes) {
		ok := true
		for j, qr := range queryRunes {
			if unicode.ToLower(textRunes[i+j]) != qr {
				ok = false
				break
			}
		}
		if ok {
			out = append(out, SnippetRange{Start: i, End: i + len(queryRunes)})
			i += len(queryRunes)
			continue
		}
		i++
	}
	return out
}
