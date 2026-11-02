package gitclone

import (
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/testutil/gitsafe"
)

func BenchmarkReadCommitStatsTreeSize(b *testing.B) {
	for _, files := range []int{1_000, 10_000, 100_000} {
		b.Run(fmt.Sprintf("files=%d", files), func(b *testing.B) {
			dir := isolatedCommitFixtureDir(b)
			ctx := b.Context()
			runner := gitsafe.Runner()
			_, stderr, err := runner.Run(ctx, dir, nil, "init", "--initial-branch=main")
			require.NoError(b, err, string(stderr))

			var stream strings.Builder
			rng := rand.New(rand.NewPCG(1, 2))
			writeFile := func(path string, id int) {
				var content strings.Builder
				for content.Len() < 400 {
					fmt.Fprintf(&content, "line %d %d\n", id, rng.Uint64())
				}
				fmt.Fprintf(&stream, "M 100644 inline %s\ndata %d\n%s\n", path, content.Len(), content.String())
			}
			const commits = 20
			for c := range commits + 1 {
				message := fmt.Sprintf("commit %d", c)
				fmt.Fprintf(&stream, "commit refs/heads/main\nmark :%d\n", c+1)
				stream.WriteString("committer Test <test@example.test> 1700000000 +0000\n")
				fmt.Fprintf(&stream, "data %d\n%s\n", len(message), message)
				if c == 0 {
					for i := range files {
						writeFile(fmt.Sprintf("src/pkg%03d/file%06d.txt", i%200, i), i)
					}
				} else {
					for k := range 5 {
						i := ((c-1)*7 + k*13) % files
						writeFile(fmt.Sprintf("src/pkg%03d/file%06d.txt", i%200, i), 1_000_000+c*10+k)
					}
					for k := range 2 {
						writeFile(fmt.Sprintf("added/c%04d_%d.txt", c, k), 2_000_000+c*10+k)
					}
				}
				stream.WriteByte('\n')
			}
			for c := range commits {
				fmt.Fprintf(&stream, "get-mark :%d\n", c+2)
			}
			stdout, stderr, err := runner.Run(ctx, dir, strings.NewReader(stream.String()), "fast-import", "--quiet")
			require.NoError(b, err, string(stderr))
			shas := strings.Fields(string(stdout))
			require.Len(b, shas, commits)
			rows := make([]Commit, 0, commits)
			for _, sha := range shas {
				rows = append(rows, Commit{SHA: sha})
			}
			for b.Loop() {
				_, err := ReadCommitStats(ctx, dir, rows)
				require.NoError(b, err)
			}
		})
	}
}
