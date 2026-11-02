package providerapi

import (
	"context"
	"fmt"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/forge/platform"
)

func BenchmarkMarkdownImageCacheMissEntries(b *testing.B) {
	for _, entries := range []int{100, 1_000, 10_000} {
		b.Run(fmt.Sprintf("entries=%d", entries), func(b *testing.B) {
			for _, saturated := range []bool{false, true} {
				b.Run(fmt.Sprintf("saturated=%t", saturated), func(b *testing.B) {
					c := NewMarkdownImageCache(b.TempDir())
					image := platform.MarkdownImage{ContentType: "image/png", Content: make([]byte, 4<<10)}
					entry := append([]byte(encodeMarkdownImageCacheHeader(image)), image.Content...)
					for i := range entries {
						path := c.path(fmt.Sprintf("prefill-%d", i))
						require.NoError(b, os.WriteFile(path, entry, 0o600))
						if saturated {
							require.NoError(b, os.Truncate(path, markdownImageCacheMaxBytes/int64(entries)))
						}
						aged := time.Now().Add(-time.Hour)
						require.NoError(b, os.Chtimes(path, aged, aged))
					}
					require.NoError(b, c.set("startup", image))
					ctx := b.Context()
					fetch := func(context.Context) (platform.MarkdownImage, error) { return image, nil }
					var next atomic.Uint64
					b.ResetTimer()
					b.RunParallel(func(pb *testing.PB) {
						for pb.Next() {
							_, err := c.load(ctx, fmt.Sprintf("miss-%d", next.Add(1)), fetch)
							if !assert.NoError(b, err) {
								return
							}
						}
					})
				})
			}
		})
	}
}
