package providerapi

import (
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/forge/platform"
)

func TestMarkdownImageCacheSweepsOnlyAtStartupBelowBudget(t *testing.T) {
	t.Parallel()
	c := NewMarkdownImageCache(t.TempDir())
	image := platform.MarkdownImage{ContentType: "image/png", Content: []byte("image")}
	entry := append([]byte(encodeMarkdownImageCacheHeader(image)), image.Content...)
	expired := time.Now().Add(-15 * 24 * time.Hour)

	first := c.path("expired-before-startup")
	require.NoError(t, os.WriteFile(first, entry, 0o600))
	require.NoError(t, os.Chtimes(first, expired, expired))
	require.NoError(t, c.set("first", image))
	assert.NoFileExists(t, first)

	second := c.path("expired-after-startup")
	require.NoError(t, os.WriteFile(second, entry, 0o600))
	require.NoError(t, os.Chtimes(second, expired, expired))
	require.NoError(t, c.set("second", image))
	assert.FileExists(t, second)
}

func TestMarkdownImageCacheEnforcesBudgetFromStartup(t *testing.T) {
	t.Parallel()
	assert := assert.New(t)
	c := NewMarkdownImageCache(t.TempDir())
	image := platform.MarkdownImage{ContentType: "image/png", Content: []byte("image")}
	entry := append([]byte(encodeMarkdownImageCacheHeader(image)), image.Content...)
	old := c.path("old")
	require.NoError(t, os.WriteFile(old, entry, 0o600))
	require.NoError(t, os.Truncate(old, markdownImageCacheMaxBytes-int64(len(entry))))
	aged := time.Now().Add(-time.Hour)
	require.NoError(t, os.Chtimes(old, aged, aged))

	require.NoError(t, c.set("first", image))
	assert.FileExists(old)
	require.NoError(t, c.set("second", image))
	assert.NoFileExists(old)
	for _, key := range []string{"first", "second"} {
		got, ok := c.get(key)
		assert.True(ok)
		assert.Equal(image, got)
	}
}
