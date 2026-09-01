package e2etest

import (
	"fmt"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"go.kenn.io/forge/internal/config"
	"go.kenn.io/forge/internal/federation"
	"go.kenn.io/forge/internal/testutil/federationtest"
)

func activeHubEnrollmentsForTest(
	t *testing.T,
	hubNodeID, hubBaseURL string,
	members []config.FleetMember,
) *federation.Store {
	t.Helper()
	if hubBaseURL == "" {
		hubBaseURL = "https://hub.example"
	}
	store, err := federation.Open(
		filepath.Join(t.TempDir(), "federation-enrollments.json"),
		federation.StoreOptions{},
	)
	require.NoError(t, err)
	for index, member := range members {
		_, err := federationtest.SeedActiveHubEnrollment(
			t.Context(), store,
			federation.Identity{NodeID: hubNodeID, BaseURL: hubBaseURL},
			federation.Identity{
				NodeID: member.NodeID, Name: member.Name, BaseURL: member.BaseURL,
			},
			fmt.Sprintf("%032x", index+1),
		)
		require.NoError(t, err)
	}
	return store
}
