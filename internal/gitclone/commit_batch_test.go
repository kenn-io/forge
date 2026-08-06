package gitclone

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFilterMissingCommits(t *testing.T) {
	assert := assert.New(t)
	mgr, shas := setupAncestryClone(t)
	clonePath, err := mgr.ClonePath("github", "example.com", "acme", "widgets")
	require.NoError(t, err)
	treeSHA := gitSHA(t, clonePath, shas["c2"]+"^{tree}")
	missingSHA := strings.Repeat("d", 40)

	missing, err := mgr.FilterMissingCommits(
		t.Context(), "github", "example.com", "acme", "widgets",
		[]string{shas["c1"], shas["c2"], shas["c3"], treeSHA, missingSHA},
	)
	require.NoError(t, err)
	assert.Equal(map[string]bool{treeSHA: true, missingSHA: true}, missing)

	empty, err := mgr.FilterMissingCommits(
		t.Context(), "github", "example.com", "acme", "widgets", nil,
	)
	require.NoError(t, err)
	assert.Empty(empty)
}

func TestUnreachableFrom(t *testing.T) {
	assert := assert.New(t)
	mgr, shas := setupAncestryClone(t)
	missingSHA := strings.Repeat("d", 40)

	unreachable, err := mgr.UnreachableFrom(
		t.Context(), "github", "example.com", "acme", "widgets", shas["c2"],
		[]string{shas["c1"], shas["c2"], shas["c3"]},
	)
	require.NoError(t, err)
	assert.Equal(map[string]bool{shas["c3"]: true}, unreachable)

	empty, err := mgr.UnreachableFrom(
		t.Context(), "github", "example.com", "acme", "widgets", shas["c2"], nil,
	)
	require.NoError(t, err)
	assert.Empty(empty)

	_, err = mgr.UnreachableFrom(
		t.Context(), "github", "example.com", "acme", "widgets", shas["c2"],
		[]string{missingSHA},
	)
	assert.Error(err)
}
