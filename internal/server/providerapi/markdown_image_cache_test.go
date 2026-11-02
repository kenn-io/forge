package providerapi

import (
	"context"
	"errors"
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

	c.lastSweep = time.Now().Add(-2 * markdownImageCacheSweepInterval)
	require.NoError(t, c.set("third", image))
	assert.NoFileExists(second)
}

func prefillMarkdownImageCache(tb testing.TB, c *MarkdownImageCache, image platform.MarkdownImage, entries int, size int64) {
	tb.Helper()
	entry := append([]byte(encodeMarkdownImageCacheHeader(image)), image.Content...)
	aged := time.Now().Add(-time.Hour)
	for i := range entries {
		path := c.path(fmt.Sprintf("prefill-%d", i))
		require.NoError(tb, os.WriteFile(path, entry, 0o600))
		if size > 0 {
			require.NoError(tb, os.Truncate(path, size))
		}
		modTime := aged.Add(time.Duration(i) * time.Millisecond)
		require.NoError(tb, os.Chtimes(path, modTime, modTime))
	}
}

func TestMarkdownImageCacheSweepsKeepFreshFilesUnderBudget(t *testing.T) {
	t.Parallel()
	for _, hourly := range []bool{false, true} {
		t.Run(fmt.Sprintf("hourly=%t", hourly), func(t *testing.T) {
			t.Parallel()
			assert := assert.New(t)
			c := NewMarkdownImageCache(t.TempDir())
			image := platform.MarkdownImage{ContentType: "image/png", Content: []byte("image")}
			prefillMarkdownImageCache(t, c, image, 60, 8<<20)
			if hourly {
				require.NoError(t, c.set("startup", image))
				c.lastSweep = time.Now().Add(-2 * markdownImageCacheSweepInterval)
			}
			require.NoError(t, c.set("miss", image))
			for i := range 60 {
				assert.FileExists(c.path(fmt.Sprintf("prefill-%d", i)))
			}
			got, err := c.load(t.Context(), "prefill-0", func(context.Context) (platform.MarkdownImage, error) {
				return platform.MarkdownImage{}, errors.New("provider offline")
			})
			require.NoError(t, err)
			assert.Equal("image/png", got.ContentType)
			assert.Equal([]byte("image"), got.Content[:5])
		})
	}
}

func TestMarkdownImageCacheEnforcesBudget(t *testing.T) {
	t.Parallel()
	assert := assert.New(t)
	c := NewMarkdownImageCache(t.TempDir())
	image := platform.MarkdownImage{ContentType: "image/png", Content: []byte("image")}
	entry := append([]byte(encodeMarkdownImageCacheHeader(image)), image.Content...)
	prefillMarkdownImageCache(t, c, image, 64, markdownImageCacheMaxBytes/64)

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
		path := c.path(fmt.Sprintf("prefill-%d", i))
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

	// Cross the budget while the startup sweep is still fresh.
	image.Content = make([]byte, 8<<20)
	for i := range 7 {
		require.NoError(t, c.set(fmt.Sprintf("large-%d", i), image))
	}
	total = 0
	files, err = os.ReadDir(c.Root)
	require.NoError(t, err)
	for _, file := range files {
		info, err := file.Info()
		require.NoError(t, err)
		total += info.Size()
	}
	assert.LessOrEqual(total, markdownImageCacheLowWaterBytes)
	for i := 7; i < 14; i++ {
		assert.NoFileExists(c.path(fmt.Sprintf("prefill-%d", i)))
	}
	assert.FileExists(c.path("prefill-14"))
}

func TestMarkdownImageCacheMutableRefreshesNearBudgetAvoidRepeatedSweeps(t *testing.T) {
	t.Parallel()
	assert := assert.New(t)
	c := NewMarkdownImageCache(t.TempDir())
	image := platform.MarkdownImage{ContentType: "image/png", Content: []byte("image")}
	prefillMarkdownImageCache(t, c, image, 64, (455<<20)/64)
	require.NoError(t, c.set("startup", image))
	image.Mutable = true
	image.Content = make([]byte, 7<<19)
	for i := range 16 {
		require.NoError(t, c.set(fmt.Sprintf("mutable-%d", i), image))
	}
	expired := c.path("expired-after-startup")
	require.NoError(t, os.WriteFile(expired, []byte("expired"), 0o600))
	aged := time.Now().Add(-15 * 24 * time.Hour)
	require.NoError(t, os.Chtimes(expired, aged, aged))

	for i := range 16 {
		key := fmt.Sprintf("mutable-%d", i)
		aged := time.Now().Add(-2 * MarkdownImageMutableTTL)
		require.NoError(t, os.Chtimes(c.path(key), aged, aged))
		fetches := 0
		got, err := c.load(t.Context(), key, func(context.Context) (platform.MarkdownImage, error) {
			fetches++
			return image, nil
		})
		require.NoError(t, err)
		assert.Equal(1, fetches)
		assert.Equal(image, got)
	}
	assert.FileExists(expired)
}
