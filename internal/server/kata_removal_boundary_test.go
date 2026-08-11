package server

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestKataIntegrationRemovalBoundary(t *testing.T) {
	t.Parallel()

	paths := NewOpenAPI().Paths
	for _, removed := range []string{
		"/kata/proxy/{path}",
		"/kata/tasks/snapshot",
		"/kata/tasks/events",
	} {
		assert.NotContains(t, paths, removed)
	}
}
