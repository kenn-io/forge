package providerapi

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/forge/platform"
)

func prefillMarkdownImageCache(tb testing.TB, c *MarkdownImageCache, entries int, size int64) {
	tb.Helper()
	aged := time.Now().Add(-time.Hour)
	for i := range entries {
		path := c.path(fmt.Sprintf("prefill-%d", i))
		require.NoError(tb, os.WriteFile(path, nil, 0o600))
		require.NoError(tb, os.Truncate(path, size))
		modTime := aged.Add(time.Duration(i) * time.Millisecond)
		require.NoError(tb, os.Chtimes(path, modTime, modTime))
	}
}

func plantExpiredMarkdownImage(t *testing.T, c *MarkdownImageCache, key string) string {
	t.Helper()
	path := c.path(key)
	require.NoError(t, os.WriteFile(path, []byte("expired"), 0o600))
	aged := time.Now().Add(-15 * 24 * time.Hour)
	require.NoError(t, os.Chtimes(path, aged, aged))
	return path
}

func TestMarkdownImageCacheSweepsExpiredFilesHourly(t *testing.T) {
	t.Parallel()
	c := NewMarkdownImageCache(t.TempDir())
	image := platform.MarkdownImage{ContentType: "image/png", Content: []byte("image")}

	first := plantExpiredMarkdownImage(t, c, "expired-before-startup")
	require.NoError(t, c.set("first", image))
	assert.NoFileExists(t, first)

	second := plantExpiredMarkdownImage(t, c, "expired-after-startup")
	require.NoError(t, c.set("second", image))
	assert.FileExists(t, second)

	c.lastSweep = time.Now().Add(-2 * markdownImageCacheSweepInterval)
	require.NoError(t, c.set("third", image))
	assert.NoFileExists(t, second)
}

func TestMarkdownImageCacheTrimsOnlyOverBudget(t *testing.T) {
	t.Parallel()
	c := NewMarkdownImageCache(t.TempDir())
	image := platform.MarkdownImage{ContentType: "image/png", Content: []byte("image")}
	prefillMarkdownImageCache(t, c, 60, 8<<20)

	require.NoError(t, c.set("startup", image))
	c.lastSweep = time.Now().Add(-2 * markdownImageCacheSweepInterval)
	require.NoError(t, c.set("hourly", image))
	for i := range 60 {
		assert.FileExists(t, c.path(fmt.Sprintf("prefill-%d", i)))
	}

	// Cross the budget while the hourly sweep is still fresh.
	image.Content = make([]byte, 8<<20)
	for i := range 4 {
		require.NoError(t, c.set(fmt.Sprintf("large-%d", i), image))
	}
	for i := range 7 {
		assert.NoFileExists(t, c.path(fmt.Sprintf("prefill-%d", i)))
	}
	assert.FileExists(t, c.path("prefill-7"))
}

func TestMarkdownImageCacheMutableRefreshesNearBudgetAvoidRepeatedSweeps(t *testing.T) {
	t.Parallel()
	assert := assert.New(t)
	c := NewMarkdownImageCache(t.TempDir())
	image := platform.MarkdownImage{ContentType: "image/png", Content: make([]byte, 7<<19), Mutable: true}
	prefillMarkdownImageCache(t, c, 1, 455<<20)
	for i := range 16 {
		require.NoError(t, c.set(fmt.Sprintf("mutable-%d", i), image))
	}
	expired := plantExpiredMarkdownImage(t, c, "expired-after-startup")

	fetch := func(context.Context) (platform.MarkdownImage, error) { return image, nil }
	for i := range 16 {
		key := fmt.Sprintf("mutable-%d", i)
		aged := time.Now().Add(-2 * MarkdownImageMutableTTL)
		require.NoError(t, os.Chtimes(c.path(key), aged, aged))
		_, ok := c.get(key)
		assert.False(ok)
		assert.NoFileExists(c.path(key))
		_, err := c.load(t.Context(), key, fetch)
		require.NoError(t, err)
	}
	assert.FileExists(expired)

	// An entry without a content type is corrupt.
	require.NoError(t, c.set("corrupt", platform.MarkdownImage{Content: []byte("corrupt")}))
	_, ok := c.get("corrupt")
	assert.False(ok)
	assert.NoFileExists(c.path("corrupt"))
}
