package collect_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/landedwork"
	"go.kenn.io/forge/landedwork/collect"
	"go.kenn.io/forge/platform"
)

// A description and labels are presentation metadata, like the title. Editing
// them between the detail read and its recheck keeps the proof, and the newer
// values are retained in an observation that owns its copies.
func TestCollectDescriptionAndLabelsRecheck(t *testing.T) {
	assert, require := assert.New(t), require.New(t)
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	s := mergedScript(t)
	initial := s.steps[4].value.(platform.LandingChange)
	initial.Description, initial.Labels = new("First description"), []string{"bug"}
	s.steps[4].value = initial
	final := s.steps[6].value.(platform.LandingChange)
	final.Description, final.Labels = new("Edited description"), []string{"bug", "docs"}
	s.steps[6].value = final
	got, err := collect.Collect(ctx, s, route, query, limits)
	require.NoError(err)
	require.Len(got.Observations, 1)
	o := got.Observations[0]
	assert.True(o.SourceComplete)
	assert.Empty(o.Reason)
	assert.Equal(new("Edited description"), o.Change.Description)
	assert.Equal([]string{"bug", "docs"}, o.Change.Labels)
	*final.Description, final.Labels[1] = "local edit", "local"
	assert.Equal(new("Edited description"), o.Change.Description, "the observation owns its description")
	assert.Equal([]string{"bug", "docs"}, o.Change.Labels, "the observation owns its labels")
}

func TestCollectDescriptionAndLabelsCountTowardOutput(t *testing.T) {
	// 515 string bytes without metadata, plus an 11-byte description and two
	// labels of three and four bytes, once.
	for _, tc := range []struct {
		name  string
		bytes int64
		fits  bool
	}{{"exact fit", 533, true}, {"one short", 532, false}} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
			defer cancel()
			s := mergedScript(t)
			d := s.steps[4].value.(platform.LandingChange)
			d.Description, d.Labels = new("Description"), []string{"bug", "docs"}
			s.steps[4].value, s.steps[6].value = d, d
			l := limits
			l.OutputBytes = tc.bytes
			got, err := collect.Collect(ctx, s, route, query, l)
			if !tc.fits {
				require.ErrorIs(t, err, landedwork.ErrOutputBudget)
				return
			}
			require.NoError(t, err)
			require.Len(t, got.Observations, 1)
			assert.Equal(t, new("Description"), got.Observations[0].Change.Description)
		})
	}
}
