package providerapi

import (
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/forge/platform"
)

func TestMarkdownImageCacheSweepsExpiredFilesHourly(t *testing.T) {
	t.Parallel()
	assert := assert.New(t)
	c := NewMarkdownImageCache(t.TempDir())
	image := platform.MarkdownImage{ContentType: "image/png", Content: []byte("image")}
	entry := append([]byte(encodeMarkdownImageCacheHeader(image)), image.Content...)
	expired := time.Now().Add(-15 * 24 * time.Hour)

	first := c.path("expired-before-startup")
	require.NoError(t, os.WriteFile(first, entry, 0o600))
	require.NoError(t, os.Chtimes(first, expired, expired))
	require.NoError(t, c.set("first", image))
	assert.NoFileExists(first)

	second := c.path("expired-after-startup")
	require.NoError(t, os.WriteFile(second, entry, 0o600))
	require.NoError(t, os.Chtimes(second, expired, expired))
	require.NoError(t, c.set("second", image))
	assert.FileExists(second)

	c.lastSweep = time.Now().Add(-2 * time.Hour)
	require.NoError(t, c.set("third", image))
	assert.NoFileExists(second)
}

func TestMarkdownImageCacheEnforcesBudgetFromStartup(t *testing.T) {
	t.Parallel()
	assert := assert.New(t)
	c := NewMarkdownImageCache(t.TempDir())
	image := platform.MarkdownImage{ContentType: "image/png", Content: []byte("image")}
	entry := append([]byte(encodeMarkdownImageCacheHeader(image)), image.Content...)
	for i := range 64 {
		old := c.path(fmt.Sprintf("old-%d", i))
		require.NoError(t, os.WriteFile(old, entry, 0o600))
		require.NoError(t, os.Truncate(old, markdownImageCacheMaxBytes/64))
		aged := time.Now().Add(-time.Hour).Add(time.Duration(i) * time.Second)
		require.NoError(t, os.Chtimes(old, aged, aged))
	}

	require.NoError(t, c.set("first", image))
	var total int64
	files, err := os.ReadDir(c.Root)
	require.NoError(t, err)
	for _, file := range files {
		info, err := file.Info()
		require.NoError(t, err)
		total += info.Size()
	}
	assert.LessOrEqual(total, markdownImageCacheLowWaterBytes)
	assert.Greater(total, markdownImageCacheLowWaterBytes-markdownImageCacheMaxBytes/64)
	for i := range 64 {
		path := c.path(fmt.Sprintf("old-%d", i))
		if i < 7 {
			assert.NoFileExists(path)
		} else {
			assert.FileExists(path)
		}
	}

	expired := c.path("expired-after-trim")
	require.NoError(t, os.WriteFile(expired, entry, 0o600))
	aged := time.Now().Add(-15 * 24 * time.Hour)
	require.NoError(t, os.Chtimes(expired, aged, aged))
	for i := range 16 {
		key := fmt.Sprintf("miss-%d", i)
		require.NoError(t, c.set(key, image))
		got, ok := c.get(key)
		assert.True(ok)
		assert.Equal(image, got)
	}
	assert.FileExists(expired)
}
