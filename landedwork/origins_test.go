package landedwork_test

import (
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/landedwork"
)

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
	for _, reverse := range []bool{false, true} {
		f := buildFixture(t, false)
		f.head = f.source[1]
		ctx, p := f.prepare(t)
		e := fixtureEvidence(f, p, "fast_forward")
		e.Capabilities.FastForward = true
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
		assert.Equal(f.base, r.Coverage.CertifiedHead)
		require.Len(t, r.Coverage.Gaps, 2)
		assert.Equal(landedwork.Span{Before: f.base, Through: f.head}, r.Coverage.Gaps[0].Span)
		assert.Equal("candidate_conflict", r.Coverage.Gaps[0].Reason)
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
