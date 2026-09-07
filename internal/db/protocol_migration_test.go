package db

import (
	"crypto/sha256"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFleetProtocolMigrationConvertsHistoricalCoordinatorSealDigest(t *testing.T) {
	// This is the shipped protocol-3 encoding, before coordinator_node_id
	// became hub_node_id. Keep the fixture independent of the current encoder.
	historical := `{"enrollment_id":"historical-enrollment","node_id":"spoke-1","coordinator_node_id":"hub-1","protocol_version":%d,"migration_version":54,"receipts_digest":"receipts-digest","drained_ack_generation":4,"preparation_digest":""}`
	for _, test := range []struct {
		name     string
		protocol int
		hubID    string
		valid    bool
	}{
		{name: "historical protocol 3", protocol: 3, hubID: "hub-1", valid: true},
		{name: "changed identity", protocol: 3, hubID: "other-hub"},
		{name: "historical digest on protocol 4", protocol: 4, hubID: "hub-1"},
	} {
		t.Run(test.name, func(t *testing.T) {
			require := require.New(t)
			assert := assert.New(t)
			database := openTestDB(t)
			canonical, err := database.IssueSpokePreparationSeal(t.Context(), spokePreparationSealRequestForTest())
			require.NoError(err)
			_, err = database.WriteDB().ExecContext(t.Context(), `
				INSERT INTO forge_spoke_preparation_seals (
				    enrollment_id, node_id, hub_node_id, protocol_version,
				    migration_version, receipts_digest, drained_ack_generation,
				    preparation_digest, preparation_seal, created_at
				) VALUES ('historical-enrollment', 'spoke-1', ?, ?, 54,
				          'receipts-digest', 4, ?, 'historical-seal', '2026-08-01T00:00:00Z')`,
				test.hubID, test.protocol, fmt.Sprintf("%x", sha256.Sum256([]byte(fmt.Sprintf(historical, test.protocol)))))
			require.NoError(err)
			before, err := database.GetSpokePreparationSeal(t.Context(), "historical-enrollment")
			require.NoError(err)
			_, err = database.MigrateFleetProtocol3To4(t.Context(), nil, "", "")
			if !test.valid {
				require.ErrorIs(err, ErrSpokePreparationConflict)
				after, err := database.GetSpokePreparationSeal(t.Context(), before.EnrollmentID)
				require.NoError(err)
				assert.Equal(before, after)
				unchanged, err := database.GetSpokePreparationSeal(t.Context(), canonical.EnrollmentID)
				require.NoError(err)
				assert.Equal(canonical, *unchanged)
				return
			}
			require.NoError(err)
			_, err = database.MigrateFleetProtocol3To4(t.Context(), nil, "", "")
			require.NoError(err)
			after, err := database.GetSpokePreparationSeal(t.Context(), before.EnrollmentID)
			require.NoError(err)
			before.ProtocolVersion = 4
			before.PreparationDigest, err = SpokePreparationSealDigest(before.SpokePreparationSealRequest)
			require.NoError(err)
			assert.Equal(before, after)
			require.NoError(validateSpokePreparationSealRequest(after.SpokePreparationSealRequest))
			assertDatabaseIntegrityForTest(t, database.ReadDB())
		})
	}
}

func TestFleetProtocolMigrationPreservesSealsAndResumesBeforeEnrollmentPublish(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	database := openTestDB(t)
	require.NoError(database.InsertWorkspace(t.Context(), &Workspace{
		ID: "workspace-1", PlatformHost: "github.com", RepoOwner: "acme", RepoName: "widget",
		ItemType: WorkspaceItemTypePullRequest, ItemNumber: 42,
		GitHeadRef: "feature", WorkspaceBranch: "feature", WorktreePath: t.TempDir(),
		TmuxSession: "workspace-1", Status: "ready",
	}))
	require.NoError(database.RecordWorkspaceRuntimeSession(t.Context(), &WorkspaceRuntimeSession{
		WorkspaceID: "workspace-1", SessionKey: "agent-1", TargetKey: "agent",
		Label: "Agent", Kind: "agent", Scope: "session", CreatedAt: time.Now().UTC(),
	}))
	workspace, err := database.GetWorkspace(t.Context(), "workspace-1")
	require.NoError(err)
	runtimes, err := database.ListWorkspaceRuntimeSessions(t.Context(), "workspace-1")
	require.NoError(err)
	binding := spokePreparationBindingForTest()
	_, err = database.BeginSpokePreparation(t.Context(), binding)
	require.NoError(err)
	_, err = database.FreezeSpokePreparationAckGeneration(t.Context())
	require.NoError(err)
	receipt := SpokePreparationReceipt{StateKind: "review_draft", SourceKey: "draft-1", ContentDigest: "content", HubReceipt: "receipt"}
	require.NoError(database.RecordSpokePreparationReceipt(t.Context(), receipt))
	receipts, err := database.ListSpokePreparationReceipts(t.Context())
	require.NoError(err)
	digest, err := SpokePreparationReceiptsDigest(receipts)
	require.NoError(err)
	request := spokePreparationSealRequestForTest()
	request.ReceiptsDigest = digest
	request.DrainedAckGeneration = 0
	request.PreparationDigest, err = SpokePreparationSealDigest(request)
	require.NoError(err)
	seal, err := database.IssueSpokePreparationSeal(t.Context(), request)
	require.NoError(err)
	require.NoError(database.StoreLocalSpokePreparationSeal(t.Context(), request.PreparationDigest, seal.Seal))

	updated, err := database.MigrateFleetProtocol3To4(t.Context(), &binding, request.PreparationDigest, seal.Seal)
	require.NoError(err)
	repeated, err := database.MigrateFleetProtocol3To4(t.Context(), &binding, request.PreparationDigest, seal.Seal)
	require.NoError(err)
	assert.Equal(updated, repeated)
	request.ProtocolVersion = 4
	expected, err := SpokePreparationSealDigest(request)
	require.NoError(err)
	assert.Equal(expected, updated)
	current, err := database.GetSpokePreparation(t.Context())
	require.NoError(err)
	assert.Equal(4, current.ProtocolVersion)
	assert.Equal(seal.Seal, current.PreparationSeal)
	assert.Equal(updated, current.PreparationDigest)
	hubSeal, err := database.GetSpokePreparationSeal(t.Context(), request.EnrollmentID)
	require.NoError(err)
	seal.ProtocolVersion = 4
	seal.PreparationDigest = updated
	assert.Equal(seal, *hubSeal)
	remaining, err := database.ListSpokePreparationReceipts(t.Context())
	require.NoError(err)
	assert.Equal(receipts, remaining)
	retained, err := database.GetWorkspace(t.Context(), "workspace-1")
	require.NoError(err)
	assert.Equal(workspace, retained)
	retainedRuntimes, err := database.ListWorkspaceRuntimeSessions(t.Context(), "workspace-1")
	require.NoError(err)
	assert.Equal(runtimes, retainedRuntimes)
	assertDatabaseIntegrityForTest(t, database.ReadDB())
}

func TestFleetProtocolMigrationRejectsUnsupportedSealWithoutPartialWrites(t *testing.T) {
	require := require.New(t)
	database := openTestDB(t)
	request := spokePreparationSealRequestForTest()
	original, err := database.IssueSpokePreparationSeal(t.Context(), request)
	require.NoError(err)
	request.EnrollmentID = "future-enrollment"
	request.ProtocolVersion = 5
	request.PreparationDigest, err = SpokePreparationSealDigest(request)
	require.NoError(err)
	_, err = database.IssueSpokePreparationSeal(t.Context(), request)
	require.NoError(err)
	_, err = database.MigrateFleetProtocol3To4(t.Context(), nil, "", "")
	require.Error(err)
	actual, err := database.GetSpokePreparationSeal(t.Context(), original.EnrollmentID)
	require.NoError(err)
	assert.Equal(t, original, *actual)
}
