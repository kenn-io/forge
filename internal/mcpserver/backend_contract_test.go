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

func TestFederatedBackendRoutesToolsToTheirOwners(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	provider := &recordingProviderBackend{}
	local := &recordingLocalBackend{}
	backend := NewFederatedBackend(provider, local)

	_, err := backend.ListPulls(t.Context(), ItemListQuery{})

	require.NoError(err)
	assert.Equal(1, provider.listPullCalls)
	assert.Equal(0, local.listPullCalls)

	_, err = backend.GetWorkspace(t.Context(), "ws-local")

	require.NoError(err)
	assert.Equal(0, provider.getWorkspaceCalls)
	assert.Equal(1, local.getWorkspaceCalls)
}

type recordingProviderBackend struct {
	ProviderBackend
	listPullCalls     int
	getWorkspaceCalls int
}

// GetWorkspace intentionally is not part of ProviderBackend. Keeping this
// method on the test double proves the federated backend cannot select it.
func (b *recordingProviderBackend) GetWorkspace(
	context.Context, string,
) (Workspace, error) {
	b.getWorkspaceCalls++
	return Workspace{}, nil
}

func (b *recordingProviderBackend) ListPulls(
	context.Context, ItemListQuery,
) ([]Pull, error) {
	b.listPullCalls++
	return nil, nil
}

type recordingLocalBackend struct {
	LocalBackend
	listPullCalls     int
	getWorkspaceCalls int
}

func (b *recordingLocalBackend) GetWorkspace(
	context.Context, string,
) (Workspace, error) {
	b.getWorkspaceCalls++
	return Workspace{}, nil
}

// ListPulls intentionally is not part of LocalBackend. Keeping this method on
// the test double proves NewFederatedBackend does not select it by accident.
func (b *recordingLocalBackend) ListPulls(
	context.Context, ItemListQuery,
) ([]Pull, error) {
	b.listPullCalls++
	return nil, nil
}
