package db

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
