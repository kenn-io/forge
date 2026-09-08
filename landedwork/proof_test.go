package landedwork_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/landedwork"
)

func fixtureEvidence(f fixture, p *landedwork.Interval, method string) landedwork.Evidence {
	return landedwork.Evidence{
		Query: p.Query(), Inventory: landedwork.Inventory{Supported: true, Complete: true},
		Capabilities: landedwork.Capabilities{Merge: true, Squash: true},
		Candidates: []landedwork.Candidate{{Repository: f.bounds().Repository, ID: "7",
			Terminal: f.head, SourceHead: f.source[len(f.source)-1], Source: f.source,
			SourceComplete: true, Method: method,
			MethodEvidence: "fixture-method", TerminalEvidence: "fixture-terminal"}},
	}
}

func TestAnalyzeProofSideAncestry(t *testing.T) {
	f := buildSideFixture(t)
	ctx, p := f.prepare(t)
	r, err := landedwork.Analyze(ctx, p, fixtureEvidence(f, p, "merge"), fixtureLimits())
	require := require.New(t)
	require.NoError(err)
	require.Len(r.Landings, 1)
	a := assert.New(t)
	l := r.Landings[0]
	a.Equal(f.source, l.Source)
	a.ElementsMatch([]string{f.source[0], f.source[2], f.source[3]}, l.Introduced)
	var authored []string
	for _, id := range l.Introduced {
		if len(strings.Fields(f.repo.Run("rev-list", "--parents", "-n", "1", id))) <= 2 {
			authored = append(authored, id)
		}
	}
	a.ElementsMatch([]string{f.source[0], f.source[3]}, authored)
	a.Equal("3\t1\twork.txt", f.repo.Run("diff", "--numstat", l.Before, l.Terminal, "--", "work.txt"))
	a.Equal(f.bounds(), r.Coverage.Bounds)
	a.True(r.Coverage.Complete)
	a.Equal(f.head, r.Coverage.CertifiedHead)
	a.Empty(r.Coverage.Gaps)
	a.Empty(r.Unattributed)
}

func TestAnalyzeProofIntermediateEditsDoNotChangeBoundary(t *testing.T) {
	for _, method := range []string{"merge", "squash"} {
		t.Run(method, func(t *testing.T) {
			f := buildFixture(t, method == "squash")
			f.repo.Checkout("topic")
			a := f.repo.CommitFile("work.txt", "new\nkeep\none\ntwo\ntemporary\n", "insert temporary")
			b := f.repo.CommitFile("work.txt", "new\nkeep\none\ntwo\n", "remove temporary")
			f.source = append(slices.Clone(f.source), a, b)
			f.repo.Checkout("-b", "target", f.base)
			if method == "squash" {
				f.repo.Run("merge", "--squash", "topic")
				f.repo.Run("commit", "-m", "squash topic")
			} else {
				f.repo.Run("merge", "--no-ff", "topic", "-m", "merge topic")
			}
			f.head = f.repo.Head()
			ctx, p := f.prepare(t)
			r, err := landedwork.Analyze(ctx, p, fixtureEvidence(f, p, method), fixtureLimits())
			require := require.New(t)
			assert := assert.New(t)
			require.NoError(err)
			require.Len(r.Landings, 1)
			l := r.Landings[0]
			assert.Equal("3\t1\twork.txt", f.repo.Run("diff", "--numstat", l.Before, l.Terminal, "--", "work.txt"))
			assert.Equal(f.bounds(), r.Coverage.Bounds)
			assert.True(r.Coverage.Complete)
			assert.Equal(f.head, r.Coverage.CertifiedHead)
			assert.Empty(r.Coverage.Gaps)
			assert.Empty(r.Unattributed)
		})
	}
}

func TestAnalyzeProof(t *testing.T) {
	for _, method := range []string{"merge", "squash", ""} {
		t.Run(method, func(t *testing.T) {
			f := buildFixture(t, method == "squash")
			ctx, p := f.prepare(t)
			e := fixtureEvidence(f, p, method)
			result, err := landedwork.Analyze(ctx, p, e, fixtureLimits())
			require := require.New(t)
			require.NoError(err)
			require.Len(result.Landings, 1)
			assert := assert.New(t)
			landing := result.Landings[0]
			assert.Equal("7", landing.CandidateID)
			assert.Equal(f.base, landing.Before)
			assert.Equal(f.head, landing.Terminal)
			assert.Equal(f.source, landing.Source)
			assert.Equal(f.bounds(), result.Coverage.Bounds)
			assert.True(result.Coverage.Complete)
			assert.Equal(f.head, result.Coverage.CertifiedHead)
			assert.Empty(result.Coverage.Gaps)
			assert.Empty(result.Unattributed)
			introduced := f.source
			if method == "squash" {
				introduced = []string{f.head}
			}
			assert.ElementsMatch(introduced, landing.Introduced)
			assert.Equal("3\t1\twork.txt", f.repo.Run("diff", "--no-ext-diff", "--no-textconv", "--no-renames", "--numstat", landing.Before, landing.Terminal, "--", "work.txt"))
		})
	}
}
