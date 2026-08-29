package pullapi

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"go.kenn.io/forge/internal/federationauth"
)

func TestQueueMergedWorkspaceCleanupAcknowledgesPendingDeletion(t *testing.T) {
	type queuedCleanup struct {
		hostKey     string
		workspaceID string
	}
	queued := make(chan queuedCleanup, 1)
	handler := New(Deps{
		QueueWorkspaceDeletion: func(_ context.Context, hostKey, id string) error {
			queued <- queuedCleanup{hostKey: hostKey, workspaceID: id}
			return nil
		},
	})
	ctx := federationauth.WithPrincipal(t.Context(), federationauth.Principal{
		NodeID: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
	})
	body := mergePRInputBody{}
	body.bindWorkspaceHost(ctx)

	result := handler.queueMergedWorkspaceCleanup(ctx, body.workspaceHostKey, "ws-1")

	assert.True(t, result.Pending)
	assert.Empty(t, result.Warning)
	assert.Equal(t, queuedCleanup{
		hostKey:     "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		workspaceID: "ws-1",
	}, <-queued)
}

func TestQueueMergedWorkspaceCleanupReturnsAdmissionWarning(t *testing.T) {
	handler := New(Deps{
		QueueWorkspaceDeletion: func(context.Context, string, string) error {
			return errors.New("workspace setup is still in progress")
		},
	})

	result := handler.queueMergedWorkspaceCleanup(t.Context(), "", "ws-1")

	assert.False(t, result.Pending)
	assert.Equal(t, "workspace setup is still in progress", result.Warning)
}

func TestQueueMergedWorkspaceCleanupWithoutWorkspaceIsComplete(t *testing.T) {
	result := New(Deps{}).queueMergedWorkspaceCleanup(t.Context(), "", "")

	assert.False(t, result.Pending)
	assert.Empty(t, result.Warning)
}
