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

// A title is presentation metadata. Renaming the pull request between the
// detail read and its recheck keeps the proof, and the newer title is retained.
func TestCollectTitleRecheck(t *testing.T) {
	assert, require := assert.New(t), require.New(t)
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	s := mergedScript(t)
	initial := s.steps[4].value.(platform.LandingChange)
	initial.Title = new("First title")
	s.steps[4].value = initial
	final := s.steps[6].value.(platform.LandingChange)
	final.Title = new("Renamed title")
	s.steps[6].value = final
	got, err := collect.Collect(ctx, s, route, query, limits)
	require.NoError(err)
	require.Len(got.Observations, 1)
	o := got.Observations[0]
	assert.True(o.SourceComplete)
	assert.Empty(o.Reason)
	assert.True(got.Evidence.Inventory.Complete)
	assert.Equal(new("Renamed title"), o.Change.Title)
	*final.Title = "local edit"
	assert.Equal(new("Renamed title"), o.Change.Title, "the observation owns its title")
}

func TestCollectTitleCountsTowardOutput(t *testing.T) {
	// 515 string bytes without metadata, plus the five-byte title once.
	for _, tc := range []struct {
		name  string
		bytes int64
		fits  bool
	}{{"exact fit", 520, true}, {"one short", 519, false}} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
			defer cancel()
			s := mergedScript(t)
			d := s.steps[4].value.(platform.LandingChange)
			d.Title = new("Title")
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
			assert.Equal(t, new("Title"), got.Observations[0].Change.Title)
		})
	}
}
