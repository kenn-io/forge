package pullapi

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"go.kenn.io/forge/internal/providerplane"
	"go.kenn.io/forge/internal/server/workspaceapi"
)

// failingPullSource fails each read once the local snapshot read has started,
// so the failure always lands while that read is still in flight.
type failingPullSource struct {
	snapshotStarted <-chan struct{}
	err             error
}

func (f failingPullSource) ListPulls(context.Context, ListQuery) ([]MergeRequestResponse, error) {
	<-f.snapshotStarted
	return nil, f.err
}

func (f failingPullSource) GetPull(context.Context, ItemIdentity) (MergeRequestDetailResponse, error) {
	<-f.snapshotStarted
	return MergeRequestDetailResponse{}, f.err
}

func (failingPullSource) GetDiffDescriptor(context.Context, ItemIdentity) (providerplane.DiffDescriptor, error) {
	return providerplane.DiffDescriptor{}, errors.New("not used")
}

func TestFailedProviderReadCancelsLocalWorkspaceSnapshot(t *testing.T) {
	tests := []struct {
		name string
		call func(context.Context, *Handler) error
	}{
		{name: "list", call: func(ctx context.Context, h *Handler) error {
			_, err := h.ListService(ctx, ListQuery{})
			return err
		}},
		{name: "detail", call: func(ctx context.Context, h *Handler) error {
			_, err := h.GetService(ctx, ItemIdentity{})
			return err
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			providerErr := errors.New("hub read failed")
			started := make(chan struct{})
			var snapshotErr error
			h := &Handler{
				providerSource: failingPullSource{snapshotStarted: started, err: providerErr},
				// The snapshot read only returns once it is canceled, so the
				// call below completes only if the failed provider read cancels it.
				workspaceSubjects: func(ctx context.Context) (workspaceapi.WorkspaceSubjectSnapshot, error) {
					close(started)
					<-ctx.Done()
					snapshotErr = ctx.Err()
					return workspaceapi.WorkspaceSubjectSnapshot{}, snapshotErr
				},
			}

			err := tt.call(t.Context(), h)

			require.ErrorIs(t, err, providerErr)
			require.ErrorIs(t, snapshotErr, context.Canceled)
			require.NoError(t, t.Context().Err())
		})
	}
}
