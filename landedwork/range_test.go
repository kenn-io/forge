package landedwork_test

import (
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/landedwork"
)

func TestFastForward(t *testing.T) {
	for _, variant := range []string{"complete", "disabled", "reordered", "partial", "missing method", "missing terminal", "crosses base", "merge member"} {
		t.Run(variant, func(t *testing.T) {
			f := buildFixture(t, false)
			f.head = f.source[1]
			if variant == "crosses base" {
				f.base = f.source[0]
			}
			if variant == "merge member" {
				f.head = f.repo.Head()
				f.source = append(slices.Clone(f.source), f.head)
			}
			ctx, p := f.prepare(t)
			e := fixtureEvidence(f, p, "fast_forward")
			e.Capabilities.FastForward = true
			switch variant {
			case "disabled":
				e.Capabilities.FastForward = false
			case "reordered":
				e.Candidates[0].Source = []string{f.source[1], f.source[0]}
			case "partial":
				e.Candidates[0].SourceComplete = false
			case "missing method":
				e.Candidates[0].MethodEvidence = ""
			case "missing terminal":
				e.Candidates[0].TerminalEvidence = ""
			}
			r, err := landedwork.Analyze(ctx, p, e, fixtureLimits())
			require := require.New(t)
			assert := assert.New(t)
			require.NoError(err)
			if variant != "complete" {
				assert.Empty(r.Landings)
				assert.False(r.Coverage.Complete)
				assert.Equal(f.base, r.Coverage.CertifiedHead)
				assert.NotEmpty(r.Coverage.Gaps)
				return
			}
			assert.Equal([]landedwork.Landing{{CandidateID: "7", Method: "fast_forward", Before: f.base,
				Terminal: f.head, Source: f.source, Introduced: f.source}}, r.Landings)
			assert.True(r.Coverage.Complete)
			assert.Equal(f.head, r.Coverage.CertifiedHead)
			assert.Empty(r.Unattributed)
			assert.Equal("3\t1\twork.txt", f.repo.Run("diff", "--numstat", r.Landings[0].Before, r.Landings[0].Terminal))
		})
	}
}
