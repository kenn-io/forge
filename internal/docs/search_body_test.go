package docs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	Assert "github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func writeTempFile(t *testing.T, name, body string) string {
	t.Helper()
	full := filepath.Join(t.TempDir(), name)
	require.NoError(t, os.WriteFile(full, []byte(body), 0o644))
	return full
}

func TestScanBodyFindsFirstMatchingLine(t *testing.T) {
	assert := Assert.New(t)
	req := require.New(t)
	path := writeTempFile(t, "doc.md",
		"intro line\nsecond mentions budget here\nthird mentions budget again\n")
	hit, warn, err := scanBody(path, "budget")
	req.NoError(err)
	assert.Empty(warn, "unexpected warning")
	assert.Equal(2, hit.Line)
	assert.Contains(hit.Snippet.Text, "budget")
	// Two matching lines -> score 20 (capped at 100).
	assert.Equal(20, hit.Score)
	req.NotEmpty(hit.Snippet.Matches, "expected at least one match range")
	runes := []rune(hit.Snippet.Text)
	got := string(runes[hit.Snippet.Matches[0].Start:hit.Snippet.Matches[0].End])
	assert.True(strings.EqualFold(got, "budget"), "first match slice = %q, want case-insensitive 'budget'", got)
}

func TestScanBodySkipsOversizeFile(t *testing.T) {
	big := strings.Repeat("budget ", int(MaxBodyFileSize/7)+1)
	path := writeTempFile(t, "big.md", big)
	hit, warn, err := scanBody(path, "budget")
	require.NoError(t, err)
	assert := Assert.New(t)
	assert.Equal(0, hit.Line)
	assert.Contains(warn, "skipped")
}

func TestScanBodySkipsTokenTooLong(t *testing.T) {
	// One line longer than the buffer cap -> ErrTooLong from the scanner.
	long := strings.Repeat("a", MaxBodyScanBuf+10) + " budget"
	path := writeTempFile(t, "long.md", long)
	hit, warn, err := scanBody(path, "budget")
	require.NoError(t, err)
	assert := Assert.New(t)
	assert.Equal(0, hit.Line)
	assert.Contains(warn, "line too long")
}

func TestScanBodyReturnsCodePointOffsets(t *testing.T) {
	req := require.New(t)
	// "café" is 4 runes, 5 bytes (é is 2 bytes in UTF-8). Make sure the
	// match offsets are rune indices, not byte indices.
	path := writeTempFile(t, "u.md", "warm café budget today\n")
	hit, _, err := scanBody(path, "budget")
	req.NoError(err)
	req.Equal(1, hit.Line)
	req.Len(hit.Snippet.Matches, 1)
	// "warm café " is 10 runes; "budget" starts at rune index 10.
	assert := Assert.New(t)
	assert.Equal(10, hit.Snippet.Matches[0].Start)
	assert.Equal(16, hit.Snippet.Matches[0].End)
}

func TestScanBodyReturnsNoHitForEmptyQuery(t *testing.T) {
	path := writeTempFile(t, "e.md", "anything")
	hit, warn, err := scanBody(path, "")
	require.NoError(t, err)
	assert := Assert.New(t)
	assert.Empty(warn)
	assert.Equal(0, hit.Line)
}

func TestScanBodyCapsScoreAtMax(t *testing.T) {
	// 20 matching lines * 10 = 200, should cap at MaxScoreCap.
	body := strings.Repeat("budget here\n", 20)
	path := writeTempFile(t, "many.md", body)
	hit, _, err := scanBody(path, "budget")
	require.NoError(t, err)
	Assert.Equal(t, MaxScoreCap, hit.Score)
}

func TestScanBodyAcceptsLongLineUnderBufferCap(t *testing.T) {
	// 100 KB line, well under MaxBodyScanBuf (256 KiB), well under
	// MaxBodyFileSize (1 MiB). Should scan cleanly with a hit, no
	// token-too-long warning.
	long := strings.Repeat("x", 100*1024) + " budget"
	path := writeTempFile(t, "long.md", long)
	hit, warn, err := scanBody(path, "budget")
	require.NoError(t, err)
	assert := Assert.New(t)
	assert.Empty(warn, "unexpected warning")
	assert.Equal(1, hit.Line)
}
