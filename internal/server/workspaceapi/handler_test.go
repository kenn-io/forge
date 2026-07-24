package workspaceapi

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewExposesWorkspaceAndProjectServices(t *testing.T) {
	t.Parallel()

	handler := New(Deps{})
	require.NotNil(t, handler)
	assert.Same(t, handler, handler.Workspaces())
	assert.Same(t, handler, handler.Projects())
}
