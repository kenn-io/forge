package settingsservertest

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/federation"
	"go.kenn.io/forge/internal/server/spokeapi"
)

const (
	preparationHubNodeID    = "0123456789abcdef0123456789abcdef"
	preparationLocalNodeID  = "fedcba9876543210fedcba9876543210"
	preparationEnrollmentID = "11111111111111111111111111111111"
)

func TestHubPreparationSealMustMatchRequestedBinding(t *testing.T) {
	require := require.New(t)
	request := db.SpokePreparationSealRequest{
		EnrollmentID: preparationEnrollmentID, NodeID: preparationLocalNodeID,
		HubNodeID:        preparationHubNodeID,
		ProtocolVersion:  federation.ProtocolVersion,
		MigrationVersion: db.WorkspaceLaunchSpecMigrationVersion,
		ReceiptsDigest:   "receipts", DrainedAckGeneration: 1,
	}
	var err error
	request.PreparationDigest, err = db.SpokePreparationSealDigest(request)
	require.NoError(err)
	valid := db.SpokePreparationSeal{
		SpokePreparationSealRequest: request,
		Seal:                        "opaque-seal", CreatedAt: time.Now().UTC(),
	}
	require.NoError(spokeapi.ValidateHubPreparationSeal(request, valid))

	different := valid
	different.ReceiptsDigest = "different"
	require.Error(spokeapi.ValidateHubPreparationSeal(request, different))
	incomplete := valid
	incomplete.Seal = ""
	require.Error(spokeapi.ValidateHubPreparationSeal(request, incomplete))
}
