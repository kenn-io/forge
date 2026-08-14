package pullapi

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestQueueMergedWorkspaceCleanupAcknowledgesPendingDeletion(t *testing.T) {
	queued := make(chan string, 1)
	handler := New(Deps{
		QueueWorkspaceDeletion: func(id string) error {
			queued <- id
			return nil
		},
	})

	result := handler.queueMergedWorkspaceCleanup("ws-1")

	assert.True(t, result.Pending)
	assert.Empty(t, result.Warning)
	assert.Equal(t, "ws-1", <-queued)
}

func TestQueueMergedWorkspaceCleanupReturnsAdmissionWarning(t *testing.T) {
	handler := New(Deps{
		QueueWorkspaceDeletion: func(string) error {
			return errors.New("workspace setup is still in progress")
		},
	})

	result := handler.queueMergedWorkspaceCleanup("ws-1")

	assert.False(t, result.Pending)
	assert.Equal(t, "workspace setup is still in progress", result.Warning)
}

func TestQueueMergedWorkspaceCleanupWithoutWorkspaceIsComplete(t *testing.T) {
	result := New(Deps{}).queueMergedWorkspaceCleanup("")

	assert.False(t, result.Pending)
	assert.Empty(t, result.Warning)
}
