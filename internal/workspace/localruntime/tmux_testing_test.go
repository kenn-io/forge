package localruntime

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTmuxRuntimeFixtureRejectsKillWithoutPrivateServer(t *testing.T) {
	fixture := NewTmuxRuntimeFixture(t, nil)

	err := fixture.killServerAndWait(t.Context(), nil)

	require.EqualError(t, err, "fixture has no private tmux server command")
}
