package fleet

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEntityIDDeterministicAndHostScoped(t *testing.T) {
	assert := assert.New(t)
	a := EntityID("studio", "worktree:/srv/app")
	assert.Equal(a, EntityID("studio", "worktree:/srv/app"), "EntityID must be deterministic for the same inputs")
	assert.NotEqual(a, EntityID("mbp", "worktree:/srv/app"), "same scoped key on different hosts must yield different IDs")
	assert.NotEqual(HostID("studio"), HostID("mbp"), "distinct host keys must yield distinct host IDs")
	assert.Len(a, 36, "expected a 36-char UUID")
}

func TestDefaultIdentityMatchesPackageFuncs(t *testing.T) {
	// The package-level wrappers must stay byte-identical to the
	// default-namespace Identity so existing callers are unaffected.
	assert.Equal(t, DefaultIdentity().HostID("studio"), HostID("studio"))
	assert.Equal(t, DefaultIdentity().EntityID("studio", "wt:/a"), EntityID("studio", "wt:/a"))
}

func TestIdentityUUIDIsValidV5Shape(t *testing.T) {
	got := DefaultIdentity().UUID("host:")
	require.Len(t, got, 36)
	assert.Equal(t, byte('5'), got[14], "version nibble must be 5")
	assert.Contains(t, "89ab", string(got[19]), "variant nibble must be 8-b")
}
