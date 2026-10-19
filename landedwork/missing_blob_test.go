package landedwork_test

import (
	"fmt"
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
	assertDamagedBlobLeavesLaterLandingProvable(t, func(path string) {
		require.NoError(t, os.Remove(path))
	})
}

// Git stops partway through a corrupt blob's content. The broken read leaves
// its landing unproven without affecting a later landing.
func TestCorruptBlobLeavesLaterLandingsProvable(t *testing.T) {
	assertDamagedBlobLeavesLaterLandingProvable(t, func(path string) {
		data, err := os.ReadFile(path)
		require.NoError(t, err)
		// Keep the zlib header and object header readable so Git reports the
		// object before inflating the damaged content.
		for i := len(data) / 2; i < len(data)-4; i++ {
			data[i] ^= 0x55
		}
		require.NoError(t, os.Chmod(path, 0o600))
		require.NoError(t, os.WriteFile(path, data, 0o600))
	})
}

func assertDamagedBlobLeavesLaterLandingProvable(t *testing.T, damage func(path string)) {
	t.Helper()
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
	var first strings.Builder
	for i := range 2000 {
		fmt.Fprintf(&first, "line %d only in the first change\n", i)
	}
	firstSource, firstHead := squash("first", "a.txt", first.String())
	secondSource, second := squash("second", "b.txt", "second change\n")
	blob := strings.TrimSpace(r.Run("rev-parse", firstHead+":a.txt"))
	damage(filepath.Join(r.GitDir, "objects", blob[:2], blob[2:]))

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
		Candidates:   []landedwork.Candidate{candidate("7", firstSource, firstHead), candidate("8", secondSource, second)},
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
