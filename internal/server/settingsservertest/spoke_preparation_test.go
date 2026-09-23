package settingsservertest

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/federation"
	"go.kenn.io/forge/internal/server/spokeapi"
	serverfake "go.kenn.io/forge/internal/testutil/serverfake"
)

func TestHubPreparationSealMustMatchRequestedBinding(t *testing.T) {
	require := require.New(t)
	request := db.SpokePreparationSealRequest{
		EnrollmentID: serverfake.PreparationEnrollmentID, NodeID: serverfake.PreparationLocalNodeID,
		HubNodeID:        serverfake.PreparationHubNodeID,
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
