package docs

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	Assert "github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/middleman/internal/config"
)

func newCrossFolderRegistry(t *testing.T) *Registry {
	t.Helper()
	rootA := t.TempDir()
	rootB := t.TempDir()
	must := func(root, rel, body string) {
		req := require.New(t)
		full := filepath.Join(root, rel)
		req.NoError(os.MkdirAll(filepath.Dir(full), 0o755))
		req.NoError(os.WriteFile(full, []byte(body), 0o644))
	}
	must(rootA, "README.md", "# Notes\n\nWelcome to budget planning.\n")
	must(rootA, "Daily/2026-05-15.md", "# 2026-05-15\n\nmet with team about the budget\n")
	must(rootA, "Daily/2026-05-14.md", "# 2026-05-14\n\nunrelated notes\n")
	must(rootB, "index.md", "# Engineering\n\nNo finance words here.\n")
	must(rootB, "decisions/budget.md", "# Budget decision\n\nThe project cost.\n")
	return NewRegistry([]config.DocFolder{
		{ID: "notes", Name: "Notes", Path: rootA},
		{ID: "eng", Name: "Engineering", Path: rootB},
	})
}

func TestSearchAllReturnsHitsAcrossFolders(t *testing.T) {
	assert := Assert.New(t)
	req := require.New(t)
	r := newCrossFolderRegistry(t)
	res, err := r.SearchAll(context.Background(), "budget", 50)
	req.NoError(err)
	req.NotEmpty(res.Hits, "expected at least one hit")

	byPath := map[string]CrossFolderHit{}
	for _, h := range res.Hits {
		byPath[h.Folder+"/"+h.RelPath] = h
	}

	// Filename hit on Engineering/decisions/budget.md - should be hit_type
	// "filename" (and may also carry the body snippet attached).
	bk, ok := byPath["eng/decisions/budget.md"]
	req.True(ok, "missing eng/decisions/budget.md hit")
	assert.Equal("filename", bk.HitType)

	// Body-only hits - README and the matching daily file in Notes.
	for _, key := range []string{"notes/README.md", "notes/Daily/2026-05-15.md"} {
		h, ok := byPath[key]
		if !assert.True(ok, "missing hit for %s", key) {
			continue
		}
		assert.Equal("body", h.HitType, "%s HitType", key)
		assert.NotZero(h.Line, "%s missing Line", key)
		assert.NotNil(h.Snippet, "%s missing Snippet", key)
	}

	// The file with no match must not appear.
	_, ok = byPath["notes/Daily/2026-05-14.md"]
	assert.False(ok, "Daily/2026-05-14.md should not match")
}

func TestSearchAllRankingBuckets(t *testing.T) {
	root := t.TempDir()
	must := func(rel, body string) {
		req := require.New(t)
		full := filepath.Join(root, rel)
		req.NoError(os.MkdirAll(filepath.Dir(full), 0o755))
		req.NoError(os.WriteFile(full, []byte(body), 0o644))
	}
	// filename hit (exact stem == "budget")
	must("budget.md", "unrelated body\n")
	// body-only hit
	must("notes.md", "we mentioned budget here\n")
	r := NewRegistry([]config.DocFolder{{ID: "f", Name: "F", Path: root}})

	res, err := r.SearchAll(context.Background(), "budget", 50)
	req := require.New(t)
	req.NoError(err)
	req.Len(res.Hits, 2)
	assert := Assert.New(t)
	assert.Equal("filename", res.Hits[0].HitType, "bucket should beat score")
	assert.Equal("body", res.Hits[1].HitType)
}

func TestSearchAllOneRowPerFile(t *testing.T) {
	root := t.TempDir()
	// Same file matches both name (substring) AND body - must produce
	// exactly one hit with HitType=filename and a snippet attached.
	full := filepath.Join(root, "budget-notes.md")
	req := require.New(t)
	req.NoError(os.WriteFile(full, []byte("budget budget budget\n"), 0o644))
	r := NewRegistry([]config.DocFolder{{ID: "f", Name: "F", Path: root}})
	res, err := r.SearchAll(context.Background(), "budget", 50)
	req.NoError(err)
	req.Len(res.Hits, 1)
	h := res.Hits[0]
	Assert.Equal(t, "filename", h.HitType)
	Assert.NotNil(t, h.Snippet, "snippet should be attached to the filename hit")
}

func TestSearchAllTruncationProbe(t *testing.T) {
	req := require.New(t)
	root := t.TempDir()
	for i := range 5 {
		full := filepath.Join(root, fmt.Sprintf("note-%d.md", i))
		req.NoError(os.WriteFile(full, []byte("budget"), 0o644))
	}
	r := NewRegistry([]config.DocFolder{{ID: "f", Name: "F", Path: root}})

	// limit=3 -> 3 hits returned + Truncated=true.
	res, err := r.SearchAll(context.Background(), "budget", 3)
	req.NoError(err)
	assert := Assert.New(t)
	assert.Len(res.Hits, 3)
	assert.True(res.Truncated, "Truncated should be true when more hits existed than limit")

	// limit=10 -> all 5 returned + Truncated=false.
	res2, _ := r.SearchAll(context.Background(), "budget", 10)
	assert.Len(res2.Hits, 5)
	assert.False(res2.Truncated)
}

func TestSearchAllPerFolderWarning(t *testing.T) {
	req := require.New(t)
	rootGood := t.TempDir()
	req.NoError(os.WriteFile(filepath.Join(rootGood, "ok.md"), []byte("budget"), 0o644))
	r := NewRegistry([]config.DocFolder{
		{ID: "good", Name: "Good", Path: rootGood},
		{ID: "gone", Name: "Gone", Path: filepath.Join(t.TempDir(), "does-not-exist")},
	})
	res, err := r.SearchAll(context.Background(), "budget", 50)
	req.NoError(err, "partial failure shouldn't be a hard error")
	assert := Assert.New(t)
	assert.Len(res.Hits, 1, "good folder still scans")
	if assert.NotEmpty(res.Warnings) {
		assert.Contains(res.Warnings[0], "Gone")
	}
}

func TestSearchAllAllFoldersFailedReturnsError(t *testing.T) {
	r := NewRegistry([]config.DocFolder{
		{ID: "a", Name: "A", Path: filepath.Join(t.TempDir(), "missing-a")},
		{ID: "b", Name: "B", Path: filepath.Join(t.TempDir(), "missing-b")},
	})
	_, err := r.SearchAll(context.Background(), "budget", 50)
	require.Error(t, err, "expected error when every folder fails")
}

func TestSearchAllContextCancellation(t *testing.T) {
	r := newCrossFolderRegistry(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel before calling
	res, err := r.SearchAll(ctx, "budget", 50)
	Assert.NoError(t, err, "cancelled SearchAll should return cleanly")
	// We don't assert hit count - the goroutines may have raced past the
	// first ctx check. The important thing is no panic, no hang.
	_ = res
}

func TestSearchAllEmptyQueryReturnsEmpty(t *testing.T) {
	r := newCrossFolderRegistry(t)
	res, err := r.SearchAll(context.Background(), "  ", 50)
	require.NoError(t, err)
	Assert.Empty(t, res.Hits, "whitespace-only query")
}

// An in-folder .md symlink whose target lives outside the registered
// folder must not produce a body snippet - same containment rule
// ReadFile enforces. Without the resolve() check the scanner could
// leak text from anywhere the daemon process can read.
func TestSearchAllSymlinkEscapeIsContained(t *testing.T) {
	req := require.New(t)
	folderRoot := t.TempDir()
	outsideRoot := t.TempDir()
	// External file the symlink will point at.
	outsidePath := filepath.Join(outsideRoot, "secret.md")
	req.NoError(os.WriteFile(outsidePath, []byte("# secret\n\nhighly classified budget data\n"), 0o644))
	// Symlink inside the folder root pointing to the external file.
	linkPath := filepath.Join(folderRoot, "looks-inside.md")
	if err := os.Symlink(outsidePath, linkPath); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}
	// Plus one legitimate in-folder file so the test exercises a real hit
	// existing alongside the rejected symlink.
	req.NoError(os.WriteFile(filepath.Join(folderRoot, "ok.md"), []byte("# ok\n\nbudget item\n"), 0o644))
	r := NewRegistry([]config.DocFolder{{ID: "notes", Name: "Notes", Path: folderRoot}})

	res, err := r.SearchAll(context.Background(), "budget", 50)
	req.NoError(err)
	for _, h := range res.Hits {
		Assert.NotEqual(t, "looks-inside.md", h.RelPath, "symlink escape was not contained: %+v", h)
	}
}
