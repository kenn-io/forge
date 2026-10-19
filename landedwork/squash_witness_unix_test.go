//go:build unix

package landedwork_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/landedwork"
)

// Git 2.38 and 2.39 pin the merge base with synthesized commits; the witness
// and the replay checks must not depend on merge-tree --merge-base.
func TestSquashWithEditsAlreadyOnTargetWithoutMergeBaseOption(t *testing.T) {
	f := independentOverlap(t)
	olderGit(t, "--merge-base")
	ctx, p := f.prepare(t)
	r, err := landedwork.Analyze(ctx, p, automaticEvidence(f, p, ""), fixtureLimits())
	require.NoError(t, err)
	require.Empty(t, r.Coverage.Gaps)
	require.Len(t, r.Landings, 1)
	assert.Equal(t, []string{f.head}, r.Landings[0].Spine)
}

// Without merge-tree there is no witness; the edit mismatch stands.
func TestSquashWithEditsAlreadyOnTargetWithoutMergeTree(t *testing.T) {
	f := independentOverlap(t)
	olderGit(t, "--write-tree")
	ctx, p := f.prepare(t)
	r, err := landedwork.Analyze(ctx, p, automaticEvidence(f, p, ""), fixtureLimits())
	require.NoError(t, err)
	assert.Empty(t, r.Landings)
	require.Len(t, r.Coverage.Gaps, 1)
	assert.Equal(t, "source_correspondence_unproven", r.Coverage.Gaps[0].Reason)
}
