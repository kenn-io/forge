package gitclone

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

// Keep the change fixed at five modified and two added files while the tree grows.
func BenchmarkDiffTreeSize(b *testing.B) {
	for _, files := range []int{1_000, 10_000, 100_000} {
		b.Run(fmt.Sprintf("files=%d", files), func(b *testing.B) {
			dir, shas := benchmarkCommitHistory(b, files, 1)
			ctx := b.Context()
			mgr := New(b.TempDir(), nil)
			b.Cleanup(mgr.Wait)
			require.NoError(b, mgr.EnsureClone(ctx, "github", "example.com", "test", "repo", dir))
			b.Run("files", func(b *testing.B) {
				for b.Loop() {
					result, err := mgr.DiffFiles(ctx, "github", "example.com", "test", "repo", shas[0], shas[1])
					require.NoError(b, err)
					require.Len(b, result, 7)
				}
			})
			b.Run("patch", func(b *testing.B) {
				for b.Loop() {
					result, err := mgr.Diff(ctx, "github", "example.com", "test", "repo", shas[0], shas[1], false)
					require.NoError(b, err)
					require.Len(b, result.Files, 7)
				}
			})
		})
	}
}
