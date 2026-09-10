package landedwork_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/landedwork"
)

func TestDirectPushAfterBoundedGap(t *testing.T) {
	for _, variant := range []string{"bounded", "unknown terminal", "incomplete inventory"} {
		t.Run(variant, func(t *testing.T) {
			f, candidates := twoLandings(t)
			before := f.head
			f.head = f.repo.CommitFile("direct", "direct\n", "direct")
			candidates[0].SourceComplete = false
			ctx, p := f.prepare(t)
			e := fixtureEvidence(f, p, "merge")
			e.Candidates = candidates
			if variant == "unknown terminal" {
				e.Candidates[0].TerminalEvidence = ""
			}
			if variant == "incomplete inventory" {
				e.Inventory.Complete = false
			}
			r, err := landedwork.Analyze(ctx, p, e, fixtureLimits())
			require := require.New(t)
			assert := assert.New(t)
			require.NoError(err)
			require.Len(r.Landings, 1)
			assert.Equal("8", r.Landings[0].CandidateID)
			assert.False(r.Coverage.Complete)
			assert.Equal(f.base, r.Coverage.CertifiedHead)
			if variant == "bounded" {
				assert.Equal([]landedwork.DirectPush{{Before: before, Terminal: f.head, Introduced: []string{f.head}}}, r.DirectPushes)
				assert.Equal([]string{candidates[0].Terminal}, r.Unattributed)
			} else {
				assert.Empty(r.DirectPushes)
			}
		})
	}
}

func TestRebaseExhaustionPreservesGaps(t *testing.T) {
	f := buildFixture(t, false)
	f.repo.Checkout("topic")
	f.source = append(slices.Clone(f.source), f.repo.CommitFile("large", strings.Repeat("large edit\n", 2048), "large"))
	f.repo.Checkout("-b", "large-replay", f.base)
	f.base = f.repo.CommitFile("other", "context\n", "advance")
	for _, id := range f.source {
		f.repo.Run("cherry-pick", id)
	}
	f.head = f.repo.Head()
	ctx, p := f.prepare(t)
	e := fixtureEvidence(f, p, "rebase")
	e.Capabilities.Rebase = true
	unknown := e.Candidates[0]
	unknown.ID = "0"
	unknown.Terminal = ""
	unknown.TerminalEvidence = ""
	e.Candidates = append(e.Candidates, unknown)
	limits := fixtureLimits()
	limits.InputBytes = 8192 // Fits metadata and small edits, not the large blob.
	r, err := landedwork.Analyze(ctx, p, e, limits)
	require := require.New(t)
	assert := assert.New(t)
	require.NoError(err)
	assert.Empty(r.DirectPushes)
	assert.False(r.Coverage.Complete)
	assert.Equal(f.base, r.Coverage.CertifiedHead)
	require.Len(r.Coverage.Gaps, 2)
	assert.Equal("terminal_unproven", r.Coverage.Gaps[0].Reason)
	assert.Equal("input_budget_exhausted", r.Coverage.Gaps[1].Reason)
	assert.Equal(landedwork.Span{Before: f.base, Through: f.head}, r.Coverage.Gaps[1].Span)
}

func TestDirectPushes(t *testing.T) {
	for _, merge := range []bool{false, true} {
		f := buildFixture(t, false)
		if !merge {
			f.head = f.source[1]
		}
		ctx, p := f.prepare(t)
		e := fixtureEvidence(f, p, "merge")
		e.Candidates = nil
		r, err := landedwork.Analyze(ctx, p, e, fixtureLimits())
		require := require.New(t)
		assert := assert.New(t)
		require.NoError(err)
		if merge {
			require.Len(r.DirectPushes, 1)
			assert.Equal(f.base, r.DirectPushes[0].Before)
			assert.Equal(f.head, r.DirectPushes[0].Terminal)
			assert.ElementsMatch(append(slices.Clone(f.source), f.head), r.DirectPushes[0].Introduced)
		} else {
			assert.Equal([]landedwork.DirectPush{{Before: f.base, Terminal: f.source[0], Introduced: f.source[:1]},
				{Before: f.source[0], Terminal: f.head, Introduced: f.source[1:]}}, r.DirectPushes)
		}
		assert.Empty(r.Landings)
		assert.Empty(r.Unattributed)
		assert.True(r.Coverage.Complete)
		assert.Equal(f.head, r.Coverage.CertifiedHead)
	}
}

func TestDirectPushMissingObjects(t *testing.T) {
	for _, target := range []string{"base", "source", "terminal"} {
		t.Run(target, func(t *testing.T) {
			f := buildFixture(t, false)
			f.repo.Run("commit-graph", "write", "--reachable")
			ctx, p := f.prepare(t)
			missing := f.base
			if target == "source" {
				missing = f.source[0]
			}
			if target == "terminal" {
				missing = f.head
			}
			require := require.New(t)
			assert := assert.New(t)
			require.NoError(os.Remove(filepath.Join(f.repo.GitDir, "objects", missing[:2], missing[2:])))
			e := fixtureEvidence(f, p, "merge")
			e.Candidates = nil
			r, err := landedwork.Analyze(ctx, p, e, fixtureLimits())
			require.NoError(err)
			assert.Empty(r.DirectPushes)
			assert.False(r.Coverage.Complete)
			assert.Equal(f.base, r.Coverage.CertifiedHead)
			require.Len(r.Coverage.Gaps, 1)
			assert.Equal(missing, r.Coverage.Gaps[0].ObjectID)
			assert.Equal("objects_unavailable", r.Coverage.Gaps[0].Reason)
		})
	}
}

func TestIntegratedMerges(t *testing.T) {
	for _, variant := range []string{"nested", "reverse", "partial inner", "rejected outer", "unsupported inner"} {
		t.Run(variant, func(t *testing.T) {
			f := buildFixture(t, false)
			inner := f.head
			f.repo.Checkout("-b", "integration", f.base)
			side := f.repo.CommitFile("integration.txt", "side\n", "integration starts")
			f.repo.Run("merge", "--no-ff", "main", "-m", "middle merge")
			middle := f.repo.Head()
			f.repo.Checkout("-b", "final", f.base)
			f.repo.Run("merge", "--no-ff", "integration", "-m", "outer merge")
			f.head = f.repo.Head()
			ctx, p := f.prepare(t)
			e := fixtureEvidence(f, p, "merge")
			innerCandidate := e.Candidates[0]
			innerCandidate.ID = "inner"
			innerCandidate.Terminal = inner
			middleCandidate := innerCandidate
			middleCandidate.ID = "middle"
			middleCandidate.Terminal = middle
			middleCandidate.SourceHead = inner
			middleCandidate.Source = append(slices.Clone(f.source), inner)
			outer := middleCandidate
			outer.ID = "outer"
			outer.Terminal = f.head
			outer.SourceHead = middle
			outer.Source = append(slices.Clone(middleCandidate.Source), side, middle)
			e.Candidates = []landedwork.Candidate{innerCandidate, middleCandidate, outer}
			switch variant {
			case "reverse":
				slices.Reverse(e.Candidates)
			case "partial inner":
				e.Candidates[0].SourceComplete = false
			case "rejected outer":
				e.Candidates[2].SourceComplete = false
			case "unsupported inner":
				e.Candidates[0].Method = "squash"
			}
			r, err := landedwork.Analyze(ctx, p, e, fixtureLimits())
			require := require.New(t)
			assert := assert.New(t)
			require.NoError(err)
			if variant != "nested" && variant != "reverse" {
				assert.False(r.Coverage.Complete)
				assert.Equal(f.base, r.Coverage.CertifiedHead)
				require.NotEmpty(r.Coverage.Gaps)
				assert.Equal(landedwork.Span{Before: f.base, Through: f.head}, r.Coverage.Gaps[0].Span)
				return
			}
			assert.Equal([]landedwork.IntegratedCandidate{{CandidateID: "inner", ThroughCandidateID: "outer"}, {CandidateID: "middle", ThroughCandidateID: "outer"}}, r.Integrated)
			require.Len(r.Landings, 1)
			assert.Equal("outer", r.Landings[0].CandidateID)
			assert.True(r.Coverage.Complete)
			assert.Equal(f.head, r.Coverage.CertifiedHead)
			assert.Empty(r.Coverage.Gaps)
		})
	}
}

func TestOriginOverlaps(t *testing.T) {
	for _, automatic := range []bool{false, true} {
		for _, reverse := range []bool{false, true} {
			f := buildFixture(t, false)
			f.repo.Checkout("-b", "overlap", f.base)
			direct := f.repo.CommitFile("other", "direct\n", "direct")
			f.source = []string{f.repo.CommitFile("work.txt", "new\nkeep\n", "first")}
			f.source = append(f.source, f.repo.CommitFile("work.txt", "new\nkeep\nlast\n", "last"))
			f.head = f.repo.Head()
			ctx, p := f.prepare(t)
			e := fixtureEvidence(f, p, "fast_forward")
			e.Capabilities.FastForward = true
			if automatic {
				e.Candidates[0].Method, e.Candidates[0].MethodEvidence = "", ""
				e.Capabilities = landedwork.Capabilities{SingleParentCorrespondence: true}
			}
			c := e.Candidates[0]
			c.ID = "8"
			c.Terminal = f.source[0]
			c.SourceHead = f.source[0]
			c.Source = c.Source[:1]
			e.Candidates = append(e.Candidates, c)
			if reverse {
				slices.Reverse(e.Candidates)
			}
			r, err := landedwork.Analyze(ctx, p, e, fixtureLimits())
			require.NoError(t, err)
			assert := assert.New(t)
			assert.Empty(r.Landings)
			assert.False(r.Coverage.Complete)
			assert.Equal(direct, r.Coverage.CertifiedHead)
			require.Len(t, r.Coverage.Gaps, 2)
			assert.Equal(landedwork.Span{Before: direct, Through: f.head}, r.Coverage.Gaps[0].Span)
			assert.Equal("candidate_conflict", r.Coverage.Gaps[0].Reason)
			assert.Equal([]landedwork.DirectPush{{Before: f.base, Terminal: direct, Introduced: []string{direct}}}, r.DirectPushes)
		}
	}

}

func TestBlockedSpan(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	for _, terminalKnown := range []bool{true, false} {
		f := buildFixture(t, true)
		terminal := f.head
		f.head = f.repo.CommitFile("later", "later\n", "later")
		ctx, p := f.prepare(t)
		e := fixtureEvidence(f, p, "squash")
		e.Candidates[0].Terminal = terminal
		e.Candidates[0].SourceComplete = false
		if !terminalKnown {
			e.Candidates[0].TerminalEvidence = ""
		}
		r, err := landedwork.Analyze(ctx, p, e, fixtureLimits())
		require.NoError(err)
		require.NotEmpty(r.Coverage.Gaps)
		through := terminal
		if !terminalKnown {
			through = f.head
		}
		assert.Equal(landedwork.Span{Before: f.base, Through: through}, r.Coverage.Gaps[0].Span)
		assert.Equal(f.base, r.Coverage.CertifiedHead)
	}
}
