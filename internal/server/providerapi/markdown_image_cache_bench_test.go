package providerapi

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/forge/platform"
)

func BenchmarkMarkdownImageCacheMissEntries(b *testing.B) {
	for _, entries := range []int{100, 1_000, 10_000} {
		b.Run(fmt.Sprintf("entries=%d", entries), func(b *testing.B) {
			c := NewMarkdownImageCache(b.TempDir())
			image := platform.MarkdownImage{ContentType: "image/png", Content: make([]byte, 4<<10)}
			prefillMarkdownImageCache(b, c, image, entries, 0)
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
}
