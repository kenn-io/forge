package operationapi

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/server/httpapi"
	"go.kenn.io/forge/internal/server/itemapi"
)

func TestIssueStateAvailabilityRequiresStateMutation(t *testing.T) {
	caps := httpapi.ProviderCapabilitiesResponse{IssueMutation: true}
	for _, op := range []OperationDescriptor{descCloseIssue, descReopenIssue} {
		got := DeriveOperationAvailabilityWithContext(op, caps, db.Repo{}, RateLimitAvailability{}, WriteCredentialGate{}, OperationAvailabilityContext{})
		assert.False(t, got.Available)
		assert.Equal(t, itemapi.CapabilityStateMutation, got.RequiredCapability)
	}
	got := DeriveOperationAvailabilityWithContext(descCreateIssue, caps, db.Repo{}, RateLimitAvailability{}, WriteCredentialGate{}, OperationAvailabilityContext{})
	assert.True(t, got.Available)
}
