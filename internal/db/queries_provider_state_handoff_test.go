package db

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func providerStateRepositoryForTest() ProviderStateRepository {
	identity := verifiedTestRepoIdentity("github", "github.com", "acme", "widget")
	return ProviderStateRepository{
		Provider: identity.Platform, PlatformHost: identity.PlatformHost,
		PlatformRepoID: identity.PlatformRepoID, Owner: identity.Owner, Name: identity.Name,
	}
}

func seedProviderStateTargetForTest(t *testing.T, database *DB) int64 {
	t.Helper()
	repoID := insertTestRepo(t, database, "acme", "widget")
	insertTestMR(t, database, repoID, 7, "Review me", baseTime())
	return repoID
}

func providerReviewDraftForTest(body string) ProviderStateReviewDraftPayload {
	return ProviderStateReviewDraftPayload{
		Repository: providerStateRepositoryForTest(), PullNumber: 7,
		Body: body, Action: "comment", Comments: []ProviderStateReviewComment{{
			Body: "Inline note", Path: "internal/example.go", Side: "right",
			Line: 12, LineType: "add", DiffHeadSHA: "diff-head", CommitSHA: "commit-head",
		}},
	}
}

func TestProviderStateHandoffReviewDraftIsIdempotentAndConflictSafe(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	database := openTestDB(t)
	seedProviderStateTargetForTest(t, database)
	source := providerReviewDraftForTest("node draft")

	first, err := database.ImportProviderReviewDraft(t.Context(), source)
	require.NoError(err)
	assert.True(first.Imported)
	assert.NotEmpty(first.Receipt)
	assert.Nil(first.Conflict)

	retry, err := database.ImportProviderReviewDraft(t.Context(), source)
	require.NoError(err)
	assert.False(retry.Imported)
	assert.Equal(first.Receipt, retry.Receipt)
	assert.Nil(retry.Conflict)

	different := providerReviewDraftForTest("hub must keep this draft")
	conflicted, err := database.ImportProviderReviewDraft(t.Context(), different)
	require.NoError(err)
	require.NotNil(conflicted.Conflict)
	sourceRecord, err := different.Record()
	require.NoError(err)
	targetRecord, err := source.Record()
	require.NoError(err)
	assert.Equal(sourceRecord.ContentDigest, conflicted.Conflict.SourceDigest)
	assert.Equal(targetRecord.ContentDigest, conflicted.Conflict.TargetDigest)

	unchanged, err := database.ImportProviderReviewDraft(t.Context(), source)
	require.NoError(err)
	assert.Equal(first.Receipt, unchanged.Receipt)
}

func TestProviderStateHandoffWorkflowStateIsIdempotentAndConflictSafe(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	database := openTestDB(t)
	seedProviderStateTargetForTest(t, database)
	source := ProviderStateWorkflowPayload{
		Repository: providerStateRepositoryForTest(), ItemType: ItemTypePR,
		ItemNumber: 7, Status: string(KanbanStatusReviewing),
		UpdatedSource: "user", UpdatedActor: "alice", UpdatedReason: "triage",
	}

	first, err := database.ImportProviderWorkflowState(t.Context(), source)
	require.NoError(err)
	assert.True(first.Imported)
	assert.NotEmpty(first.Receipt)
	retry, err := database.ImportProviderWorkflowState(t.Context(), source)
	require.NoError(err)
	assert.Equal(first.Receipt, retry.Receipt)

	different := source
	different.Status = string(KanbanStatusWaiting)
	conflicted, err := database.ImportProviderWorkflowState(t.Context(), different)
	require.NoError(err)
	require.NotNil(conflicted.Conflict)
	sourceRecord, err := different.Record()
	require.NoError(err)
	targetRecord, err := source.Record()
	require.NoError(err)
	assert.Equal(sourceRecord.ContentDigest, conflicted.Conflict.SourceDigest)
	assert.Equal(targetRecord.ContentDigest, conflicted.Conflict.TargetDigest)
}

func TestProviderStateHandoffInventoryUsesStableIdentityAndSemanticPayload(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	database := openTestDB(t)
	seedProviderStateTargetForTest(t, database)
	review := providerReviewDraftForTest("portable draft")
	_, err := database.ImportProviderReviewDraft(t.Context(), review)
	require.NoError(err)
	workflow := ProviderStateWorkflowPayload{
		Repository: providerStateRepositoryForTest(), ItemType: ItemTypePR,
		ItemNumber: 7, Status: string(KanbanStatusAwaitingMerge),
	}
	_, err = database.ImportProviderWorkflowState(t.Context(), workflow)
	require.NoError(err)

	records, err := database.ListProviderStateForHandoff(t.Context())
	require.NoError(err)
	require.Len(records, 2)
	assert.Equal(ProviderStateReviewDraft, records[0].Kind)
	assert.Equal(ProviderStateWorkflowState, records[1].Kind)
	assert.Contains(records[0].SourceKey, providerStateRepositoryForTest().PlatformRepoID)
	assert.NotEmpty(records[0].ContentDigest)
	assert.Equal(review.Body, records[0].ReviewDraft.Body)
	assert.Equal(workflow.Status, records[1].WorkflowState.Status)
}

func TestProviderStateHandoffInventorySkipsUntouchedWorkflowDefaults(t *testing.T) {
	require := require.New(t)
	database := openTestDB(t)
	repoID := seedProviderStateTargetForTest(t, database)
	require.NoError(database.EnsureItemWorkflowState(t.Context(), repoID, ItemTypePR, 7))

	records, err := database.ListProviderStateForHandoff(t.Context())
	require.NoError(err)
	require.Empty(records)

	_, err = database.SetItemWorkflowState(t.Context(), SetItemWorkflowStateParams{
		RepoID: repoID, ItemType: ItemTypePR, ItemNumber: 7,
		Status: string(KanbanStatusNew), Source: "ui",
	})
	require.NoError(err)
	records, err = database.ListProviderStateForHandoff(t.Context())
	require.NoError(err)
	require.Len(records, 1)
	require.Equal("ui", records[0].WorkflowState.UpdatedSource)
}

func TestProviderStateHandoffDigestUsesStableRepositoryIdentity(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	original := providerReviewDraftForTest("portable draft")
	routed := original
	routed.Repository.Provider = " GITHUB "
	routed.Repository.PlatformHost = " GITHUB.COM "
	routed.Repository.PlatformRepoID = " " + original.Repository.PlatformRepoID + " "
	routed.Repository.Owner = "renamed-owner"
	routed.Repository.Name = "renamed-repository"

	originalRecord, err := original.Record()
	require.NoError(err)
	routedRecord, err := routed.Record()
	require.NoError(err)
	assert.Equal(originalRecord.SourceKey, routedRecord.SourceKey)
	assert.Equal(originalRecord.ContentDigest, routedRecord.ContentDigest)
}

func TestProviderStateHandoffAcceptsReviewCommentWithoutCommitSHA(t *testing.T) {
	payload := providerReviewDraftForTest("portable draft")
	payload.Comments[0].CommitSHA = ""

	_, err := payload.Record()
	require.NoError(t, err)
}
