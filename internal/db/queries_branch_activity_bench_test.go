package db

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func BenchmarkPruneBranchActivity(b *testing.B) {
	for _, repoCount := range []int{1, 10, 100} {
		b.Run(fmt.Sprintf("repos=%d", repoCount), func(b *testing.B) {
			d, err := Open(filepath.Join(b.TempDir(), "bench.db"))
			require.NoError(b, err)
			b.Cleanup(func() { require.NoError(b, d.Close()) })
			ctx := b.Context()
			base := baseTime()
			var firstRepo int64
			for i := range repoCount {
				identity := verifiedTestRepoIdentity("github", "github.com", "acme", fmt.Sprintf("widget-%03d", i))
				repoID, err := seedTestRepo(ctx, d, identity)
				require.NoError(b, err)
				if i == 0 {
					firstRepo = repoID
				}
				commits := make([]BranchCommit, 1000)
				for j := range commits {
					timestamp := base.Add(time.Duration(j) * time.Minute)
					commits[j] = testBranchCommit(repoID, "main", fmt.Sprintf("sha-%04d", j), "main subject", timestamp)
				}
				require.NoError(b, d.UpsertBranchCommits(ctx, commits))
			}

			before := base.Add(-24 * time.Hour)
			b.ResetTimer()
			for b.Loop() {
				require.NoError(b, d.PruneBranchActivity(ctx, firstRepo, before, 1000))
			}
		})
	}
}
