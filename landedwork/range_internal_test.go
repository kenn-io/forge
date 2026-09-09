package landedwork

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/testutil/gitsafe"
	gittest "go.kenn.io/kit/git/test"
)

func TestRangeStopsAtConclusiveMismatch(t *testing.T) {
	r := gittest.NewRepo(t, gittest.Options{InitArgs: []string{"init", "-b", "main"}, ConfigureUser: true})
	r.Runner = gitsafe.Runner()
	base := r.CommitFile("text", "old\n", "base")
	r.Checkout("-b", "source")
	sources := []string{r.CommitFile("text", "source\n", "source first")}
	sources = append(sources, r.CommitFile("extra", "extra\n", "source second"))
	r.Checkout("-b", "target", base)
	landed := []string{r.CommitFile("text", "different\n", "target first")}
	landed = append(landed, r.CommitFile("extra", "extra\n", "target second"))
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	// The comparison's first pair needs six physical commit reads. Evaluating
	// the second pair would spend more, but cannot rescue the mismatched first.
	m := &meter{limits: Limits{Records: 100, Nodes: 6, InputBytes: 1 << 20}}
	v, err := openView(ctx, r.Root, m)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, v.close()) })
	object, err := rangeCorrespondence(ctx, v, Candidate{Method: "rebase", Source: sources}, landed)
	require.ErrorIs(t, err, errCorrespondence)
	assert.Equal(t, sources[0], object)
}
