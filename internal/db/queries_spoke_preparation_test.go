package db

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func spokePreparationBindingForTest() SpokePreparationBinding {
	return SpokePreparationBinding{
		EnrollmentID: "enrollment-1", HubNodeID: "hub-1",
		LocalNodeID: "spoke-1", ProtocolVersion: 3,
	}
}

func spokePreparationSealRequestForTest() SpokePreparationSealRequest {
	request := SpokePreparationSealRequest{
		EnrollmentID: "enrollment-1", NodeID: "spoke-1",
		HubNodeID: "hub-1", ProtocolVersion: 3,
		MigrationVersion: WorkspaceLaunchSpecMigrationVersion,
		ReceiptsDigest:   "receipts-digest", DrainedAckGeneration: 4,
	}
	digest, err := SpokePreparationSealDigest(request)
	if err != nil {
		panic(err)
	}
	request.PreparationDigest = digest
	return request
}

func TestSpokePreparationStateIsDurableBoundAndRetrySafe(t *testing.T) {
	t.Parallel()
	assert := assert.New(t)
	require := require.New(t)
	path := filepath.Join(t.TempDir(), "spoke-preparation.db")
	database, err := Open(path)
	require.NoError(err)
	state, err := database.GetSpokePreparation(t.Context())
	require.NoError(err)
	assert.Equal(SpokePreparationOpen, state.Phase)

	binding := spokePreparationBindingForTest()
	started, err := database.BeginSpokePreparation(t.Context(), binding)
	require.NoError(err)
	assert.Equal(SpokePreparationQuiescing, started.Phase)
	assert.Equal(binding, started.SpokePreparationBinding)
	retried, err := database.BeginSpokePreparation(t.Context(), binding)
	require.NoError(err)
	assert.Equal(started.StartedAt, retried.StartedAt)

	different := binding
	different.HubNodeID = "other-hub"
	_, err = database.BeginSpokePreparation(t.Context(), different)
	require.ErrorIs(err, ErrSpokePreparationConflict)
	require.NoError(database.Close())

	database, err = Open(path)
	require.NoError(err)
	t.Cleanup(func() { require.NoError(database.Close()) })
	restored, err := database.GetSpokePreparation(t.Context())
	require.NoError(err)
	assert.Equal(SpokePreparationQuiescing, restored.Phase)
	assert.Equal(binding, restored.SpokePreparationBinding)
}

func TestSpokePreparationReceiptsAndSealsRejectSemanticChanges(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	database := openTestDB(t)
	now := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	receipt := SpokePreparationReceipt{
		StateKind: ProviderStateReviewDraft, SourceKey: "repo/pr/7",
		ContentDigest: "content-digest", HubReceipt: "hub-receipt",
		ImportedAt: now,
	}
	require.NoError(database.RecordSpokePreparationReceipt(t.Context(), receipt))
	require.NoError(database.RecordSpokePreparationReceipt(t.Context(), receipt))
	changed := receipt
	changed.ContentDigest = "changed-digest"
	require.ErrorIs(database.RecordSpokePreparationReceipt(t.Context(), changed), ErrSpokePreparationConflict)
	receipts, err := database.ListSpokePreparationReceipts(t.Context())
	require.NoError(err)
	assert.Equal([]SpokePreparationReceipt{receipt}, receipts)

	request := spokePreparationSealRequestForTest()
	first, err := database.IssueSpokePreparationSeal(t.Context(), request)
	require.NoError(err)
	assert.NotEmpty(first.Seal)
	retry, err := database.IssueSpokePreparationSeal(t.Context(), request)
	require.NoError(err)
	assert.Equal(first, retry)
	different := request
	different.ReceiptsDigest = "changed-receipts"
	different.PreparationDigest, err = SpokePreparationSealDigest(different)
	require.NoError(err)
	_, err = database.IssueSpokePreparationSeal(t.Context(), different)
	require.ErrorIs(err, ErrSpokePreparationConflict)
}

func TestSpokePreparationSealRejectsDigestThatDoesNotCoverBinding(t *testing.T) {
	database := openTestDB(t)
	request := spokePreparationSealRequestForTest()
	request.ReceiptsDigest = "changed-after-digest"

	_, err := database.IssueSpokePreparationSeal(t.Context(), request)
	require.ErrorIs(t, err, ErrSpokePreparationConflict)
}

func TestSpokePreparationLocalSealCannotBeRebound(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	database := openTestDB(t)
	_, err := database.BeginSpokePreparation(t.Context(), spokePreparationBindingForTest())
	require.NoError(err)
	require.NoError(database.StoreLocalSpokePreparationSeal(
		t.Context(), "preparation-digest", "opaque-seal",
	))
	require.NoError(database.StoreLocalSpokePreparationSeal(
		t.Context(), "preparation-digest", "opaque-seal",
	))
	require.ErrorIs(database.StoreLocalSpokePreparationSeal(
		t.Context(), "other-digest", "other-seal",
	), ErrSpokePreparationConflict)
	state, err := database.GetSpokePreparation(t.Context())
	require.NoError(err)
	assert.Equal(SpokePreparationSealed, state.Phase)
	assert.Equal("preparation-digest", state.PreparationDigest)
	assert.Equal("opaque-seal", state.PreparationSeal)
}
