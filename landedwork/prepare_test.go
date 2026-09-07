package landedwork_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/landedwork"
)

func TestPrepareIncludesSideAncestry(t *testing.T) {
	for _, squash := range []bool{false, true} {
		t.Run(map[bool]string{false: "merge", true: "squash"}[squash], func(t *testing.T) {
			f := buildFixture(t, squash)
			_, p := f.prepare(t)
			q := p.Query()
			want := []string{f.head}
			if !squash {
				want = append(want, f.source...)
			}
			assert := assert.New(t)
			assert.True(q.Complete)
			assert.Equal(f.bounds(), q.Bounds)
			assert.ElementsMatch(want, q.Commits)
			assert.Empty(q.Gaps)
		})
	}
}

func TestPrepareGraphGaps(t *testing.T) {
	for _, name := range []string{"missing", "divergent", "side-base", "budget"} {
		t.Run(name, func(t *testing.T) {
			f := buildFixture(t, false)
			bounds, limits := f.bounds(), fixtureLimits()
			reason := "base_not_first_parent"
			switch name {
			case "missing":
				bounds.Head = strings.Repeat("a", 40)
				reason = "objects_unavailable"
			case "divergent":
				f.repo.Checkout("-b", "other", f.base)
				bounds.Base = f.repo.CommitFile("other.txt", "other\n", "other")
			case "side-base":
				bounds.Base = f.source[1]
			case "budget":
				limits.Nodes = 1
				reason = "input_budget_exhausted"
			}
			ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
			defer cancel()
			p, err := landedwork.Prepare(ctx, f.repo.Root, bounds, limits)
			require := require.New(t)
			assert := assert.New(t)
			require.NoError(err)
			assert.False(p.Query().Complete)
			require.NotEmpty(p.Query().Gaps)
			assert.Equal(reason, p.Query().Gaps[0].Reason)
		})
	}
}

func TestPrepareCanceled(t *testing.T) {
	f := buildFixture(t, false)
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	cancel()
	_, err := landedwork.Prepare(ctx, f.repo.Root, f.bounds(), fixtureLimits())
	require.ErrorIs(t, err, context.Canceled)
}
