package collect_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/testutil/gitsafe"
	"go.kenn.io/forge/landedwork"
	"go.kenn.io/forge/landedwork/collect"
	"go.kenn.io/forge/platform"
	gittest "go.kenn.io/kit/git/test"
)

func TestCollectFromRoot(t *testing.T) {
	r := gittest.NewRepo(t, gittest.Options{InitArgs: []string{"init", "-b", "main"}, ConfigureUser: true})
	r.Runner = gitsafe.Runner()
	root := r.CommitFile("work.txt", "initial content\n", "initial commit")
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	proofLimits := landedwork.Limits{Records: 100, Nodes: 100, InputBytes: 1 << 20, OutputBytes: 1 << 20}
	p, err := landedwork.Prepare(ctx, r.Root, landedwork.Bounds{Repository: query.Bounds.Repository, Head: root, FromRoot: true}, proofLimits)
	require := require.New(t)
	assert := assert.New(t)
	require.NoError(err)
	s := &script{t: t, steps: []step{
		{key: "repository", value: platform.Repository{Ref: route, PlatformID: 12}},
		{key: "association/" + root + "/", value: platform.Page[platform.LandingChangeRef]{Exhausted: true}},
	}}
	// The root query, repository and association page each consume one record.
	l := limits
	l.Records = 3
	got, err := collect.Collect(ctx, s, route, p.Query(), l)
	require.NoError(err)
	assert.Empty(s.steps)
	assert.True(got.Evidence.Query.Bounds.FromRoot)
	assert.True(got.Evidence.Inventory.Complete)
	result, err := landedwork.Analyze(ctx, p, got.Evidence, proofLimits)
	require.NoError(err)
	assert.True(result.Coverage.Complete)
	assert.Equal(root, result.Coverage.CertifiedHead)
	assert.Equal([]landedwork.DirectPush{{Terminal: root, Introduced: []string{root}}}, result.DirectPushes)

	for _, bounds := range []landedwork.Bounds{
		{Repository: query.Bounds.Repository, Head: root},
		{Repository: query.Bounds.Repository, Base: root, Head: root, FromRoot: true},
	} {
		invalid := p.Query()
		invalid.Bounds = bounds
		_, err = collect.Collect(ctx, s, route, invalid, l)
		require.Error(err)
	}
}
