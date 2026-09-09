package landedwork_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/landedwork"
)

func TestExplicitSquashRejectsDifferentAggregate(t *testing.T) {
	f := buildFixture(t, true)
	f.repo.Checkout("-b", "different", f.base)
	f.head = f.repo.CommitFile("work.txt", "unrelated\n", "different change")
	ctx, p := f.prepare(t)
	r, err := landedwork.Analyze(ctx, p, fixtureEvidence(f, p, "squash"), fixtureLimits())
	require.NoError(t, err)
	assert := assert.New(t)
	assert.Empty(r.Landings)
	assert.Empty(r.DirectPushes)
	assert.False(r.Coverage.Complete)
	assert.Equal(f.base, r.Coverage.CertifiedHead)
	assert.Equal([]landedwork.Gap{{CandidateID: "7", ObjectID: f.head, Reason: "source_correspondence_unproven",
		Span: landedwork.Span{Before: f.base, Through: f.head}}}, r.Coverage.Gaps)
}

func TestSquashRequiresSourceChain(t *testing.T) {
	for _, tc := range []struct{ name, reason string }{
		{"order", "source_order_unproven"},
		{"omitted", "source_order_unproven"},
		{"head", "source_head_mismatch"},
		{"root", "topology_unproven"},
		{"merge", "topology_unproven"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := buildFixture(t, true)
			f.repo.Checkout("topic")
			last := f.repo.CommitFile("extra", "extra\n", "last source")
			f.source = append(f.source, last)
			if tc.name == "merge" {
				f.repo.Checkout("-b", "side", f.base)
				f.repo.CommitFile("side", "side\n", "side")
				f.repo.Checkout("topic")
				f.repo.Run("merge", "--no-ff", "side", "-m", "merge side")
				f.source = append(f.source, f.repo.Head())
			}
			f.repo.Checkout("-b", "target", f.base)
			f.repo.Run("merge", "--squash", "topic")
			f.repo.Run("commit", "-m", "squash")
			f.head = f.repo.Head()
			ctx, p := f.prepare(t)
			e := fixtureEvidence(f, p, "squash")
			switch tc.name {
			case "order":
				e.Candidates[0].Source = []string{f.source[1], f.source[0], last}
			case "omitted":
				e.Candidates[0].Source = []string{f.source[0], last}
			case "head":
				e.Candidates[0].SourceHead = f.source[0]
			case "root":
				e.Candidates[0].Source = append([]string{f.base}, f.source...)
			}
			r, err := landedwork.Analyze(ctx, p, e, fixtureLimits())
			require.NoError(t, err)
			require.Len(t, r.Coverage.Gaps, 1)
			assert := assert.New(t)
			assert.Equal(tc.reason, r.Coverage.Gaps[0].Reason)
			assert.Empty(r.Landings)
			assert.Empty(r.DirectPushes)
			assert.False(r.Coverage.Complete)
			assert.Equal(f.base, r.Coverage.CertifiedHead)
		})
	}
}
