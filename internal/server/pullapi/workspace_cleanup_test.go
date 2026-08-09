package pullapi

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDeferredMergeDeletedWorkspaceIDRequiresConfirmedCleanup(t *testing.T) {
	assert := assert.New(t)
	assert.Equal("ws-1", deferredMergeDeletedWorkspaceID(true, "ws-1", ""))
	assert.Empty(deferredMergeDeletedWorkspaceID(true, "ws-1", "workspace has changes"))
	assert.Empty(deferredMergeDeletedWorkspaceID(false, "ws-1", ""))
	assert.Empty(deferredMergeDeletedWorkspaceID(true, "", ""))
}

func TestCleanupMergedWorkspaceCallsExistingDeletionHandler(t *testing.T) {
	var deletedID string
	handler := New(Deps{
		DeleteWorkspace: func(_ context.Context, id string) error {
			deletedID = id
			return nil
		},
	})

	warning := handler.cleanupMergedWorkspace(t.Context(), "ws-1")

	assert.Empty(t, warning)
	assert.Equal(t, "ws-1", deletedID)
}

func TestCleanupMergedWorkspaceReturnsDeletionWarning(t *testing.T) {
	handler := New(Deps{
		DeleteWorkspace: func(context.Context, string) error {
			return errors.New("workspace has uncommitted changes: notes.txt")
		},
	})

	assert.Equal(
		t,
		"workspace has uncommitted changes: notes.txt",
		handler.cleanupMergedWorkspace(t.Context(), "ws-1"),
	)
}
