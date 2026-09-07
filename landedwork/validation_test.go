package landedwork_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/landedwork"
)

func TestAnalyzeSquashRequiresIndependentProof(t *testing.T) {
	for _, name := range []string{"unknown method", "no method evidence", "unsupported", "merge topology"} {
		t.Run(name, func(t *testing.T) {
			f := buildFixture(t, true)
			ctx, p := f.prepare(t)
			e := fixtureEvidence(f, p, "squash")
			reason := "method_unproven"
			switch name {
			case "unknown method":
				e.Candidates[0].Method = ""
			case "no method evidence":
				e.Candidates[0].MethodEvidence = ""
			case "unsupported":
				e.Capabilities.Squash = false
				reason = "method_unsupported"
			case "merge topology":
				e.Candidates[0].Method = "merge"
				reason = "topology_unproven"
			}
			r, err := landedwork.Analyze(ctx, p, e, fixtureLimits())
			require.NoError(t, err)
			assert := assert.New(t)
			assert.Empty(r.Landings)
			assert.Equal(f.bounds(), r.Coverage.Bounds)
			assert.Equal([]landedwork.Gap{{CandidateID: "7", ObjectID: f.head, Reason: reason}}, r.Coverage.Gaps)
			assert.Equal([]string{f.head}, r.Unattributed)
			assert.False(r.Coverage.Complete)
			assert.Equal(f.base, r.Coverage.CertifiedHead)
		})
	}
}

func TestInvalidInputsAndOutputLimits(t *testing.T) {
	f := buildFixture(t, false)
	ctx, p := f.prepare(t)
	for _, name := range []string{"deadline", "zero limit", "short SHA", "path", "output"} {
		t.Run(name, func(t *testing.T) {
			callCtx, path, bounds, limits := ctx, f.repo.Root, f.bounds(), fixtureLimits()
			switch name {
			case "deadline":
				callCtx = context.Background()
			case "zero limit":
				limits.Records = 0
			case "short SHA":
				bounds.Head = "123abc"
			case "path":
				path = ""
			case "output":
				limits.OutputBytes = 1
			}
			_, err := landedwork.Prepare(callCtx, path, bounds, limits)
			require.Error(t, err)
			if name == "output" {
				require.ErrorIs(t, err, landedwork.ErrOutputBudget)
			}
		})
	}
	limits := fixtureLimits()
	limits.OutputBytes = 1
	r, err := landedwork.Analyze(ctx, p, fixtureEvidence(f, p, "merge"), limits)
	require.ErrorIs(t, err, landedwork.ErrOutputBudget)
	assert.Equal(t, landedwork.Result{}, r)
}
