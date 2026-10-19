package landedwork_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/testutil/gitsafe"
	"go.kenn.io/forge/landedwork"
	gittest "go.kenn.io/kit/git/test"
)

// A blob missing from the object store leaves its landing unproven without
// affecting a later landing that reads other objects.
func TestMissingBlobLeavesLaterLandingsProvable(t *testing.T) {
	r := gittest.NewRepo(t, gittest.Options{InitArgs: []string{"init", "-b", "main"}, ConfigureUser: true})
	r.Runner = gitsafe.Runner()
	base := r.CommitFile("seed.txt", "seed\n", "seed")
	squash := func(branch, file, content string) (string, string) {
		r.Checkout("-b", branch, "main")
		source := r.CommitFile(file, content, "edit "+file)
		r.Checkout("main")
		r.Run("merge", "--squash", branch)
		r.Run("commit", "-m", "squash "+branch)
		return source, r.Head()
	}
	firstSource, first := squash("first", "a.txt", "only in the first change\n")
	secondSource, second := squash("second", "b.txt", "second change\n")
	blob := strings.TrimSpace(r.Run("rev-parse", first+":a.txt"))
	require.NoError(t, os.Remove(filepath.Join(r.GitDir, "objects", blob[:2], blob[2:])))

	f := fixture{repo: r, base: base, head: second}
	ctx, p := f.prepare(t)
	candidate := func(id, source, terminal string) landedwork.Candidate {
		return landedwork.Candidate{
			Repository: f.bounds().Repository, ID: id, Terminal: terminal, SourceHead: source,
			Source: []string{source}, SourceComplete: true, TerminalEvidence: "fixture-terminal",
		}
	}
	e := landedwork.Evidence{
		Query: p.Query(), Inventory: landedwork.Inventory{Supported: true, Complete: true},
		Capabilities: landedwork.Capabilities{SingleParentCorrespondence: true},
		Candidates:   []landedwork.Candidate{candidate("7", firstSource, first), candidate("8", secondSource, second)},
	}
	res, err := landedwork.Analyze(ctx, p, e, fixtureLimits())
	require.NoError(t, err)
	require.Len(t, res.Coverage.Gaps, 1)
	assert.Equal(t, "7", res.Coverage.Gaps[0].CandidateID)
	assert.Equal(t, "objects_unavailable", res.Coverage.Gaps[0].Reason)
	require.Len(t, res.Landings, 1)
	assert.Equal(t, "8", res.Landings[0].CandidateID)
	assert.Equal(t, []string{second}, res.Landings[0].Spine)
}
