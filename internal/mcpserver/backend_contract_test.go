package mcpserver

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFindReviewCandidatesForwardsItemTypesToActivity(t *testing.T) {
	var activityQuery ActivityQuery
	backend := &fakeBackend{listActivityFn: func(
		_ context.Context, query ActivityQuery,
	) (ActivityPage, error) {
		activityQuery = query
		return ActivityPage{}, nil
	}}
	srv := newMCPTestServer(t, backend)

	_, err := srv.findReviewCandidates(t.Context(), findCandidatesInput{
		ItemTypes: []string{"pr"},
	})

	require.NoError(t, err)
	assert.Equal(t, []string{"pr"}, activityQuery.ItemTypes)
}

func TestNewRequiresBackend(t *testing.T) {
	_, err := New(Options{Version: "test"})

	require.ErrorContains(t, err, "backend")
}
