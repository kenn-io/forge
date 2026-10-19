package landedwork_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/testutil/gitsafe"
	"go.kenn.io/forge/landedwork"
	gittest "go.kenn.io/kit/git/test"
)

// BenchmarkAnalyzeSquashLandings proves an interval of multi-commit squash
// merges, each editing several files, as a typical collection batch does.
func BenchmarkAnalyzeSquashLandings(b *testing.B) {
	const landings, commits, files = 20, 3, 4
	r := gittest.NewRepo(b, gittest.Options{InitArgs: []string{"init", "-b", "main"}, ConfigureUser: true})
	r.Runner = gitsafe.Runner()
	for file := range files {
		r.CommitFile(fmt.Sprintf("f%d.txt", file), strings.Repeat("line\n", 40), "seed")
	}
	base := r.Head()
	repo := landedwork.Repository{Provider: "github", Host: "github.com", ID: "101"}
	var candidates []landedwork.Candidate
	for landing := range landings {
		branch := fmt.Sprintf("topic-%d", landing)
		r.Checkout("-b", branch, "main")
		var source []string
		for commit := range commits {
			for file := range files {
				name := fmt.Sprintf("f%d.txt", file)
				content := r.Run("show", "HEAD:"+name)
				r.WriteFile(name, fmt.Sprintf("%d-%d\n", landing, commit)+content)
			}
			r.Run("add", ".")
			r.Run("commit", "-m", fmt.Sprintf("%s commit %d", branch, commit))
			source = append(source, r.Head())
		}
		r.Checkout("main")
		r.Run("merge", "--squash", branch)
		r.Run("commit", "-m", "squash "+branch)
		candidates = append(candidates, landedwork.Candidate{
			Repository: repo, ID: fmt.Sprintf("%03d", landing), Terminal: r.Head(), SourceHead: source[len(source)-1],
			Source: source, SourceComplete: true, TerminalEvidence: "fixture-terminal",
		})
	}
	bounds := landedwork.Bounds{Repository: repo, Base: base, Head: r.Head()}
	limits := landedwork.Limits{Records: 1 << 20, Nodes: 1 << 20, InputBytes: 1 << 30, OutputBytes: 1 << 20}
	ctx, cancel := context.WithTimeout(b.Context(), 10*time.Minute)
	defer cancel()
	p, err := landedwork.Prepare(ctx, r.Root, bounds, limits)
	require.NoError(b, err)
	e := landedwork.Evidence{
		Query: p.Query(), Inventory: landedwork.Inventory{Supported: true, Complete: true},
		Capabilities: landedwork.Capabilities{SingleParentCorrespondence: true}, Candidates: candidates,
	}
	for b.Loop() {
		res, err := landedwork.Analyze(ctx, p, e, limits)
		require.NoError(b, err)
		require.Len(b, res.Landings, landings)
	}
}
