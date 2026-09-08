package landedwork_test

import (
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/landedwork"
)

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
