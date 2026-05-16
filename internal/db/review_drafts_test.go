package db

import (
	"testing"
	"time"

	Assert "github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testReviewLineRange() ReviewLineRange {
	oldLine := 10
	newLine := 12
	return ReviewLineRange{
		Path:        "internal/example.go",
		Side:        "right",
		Line:        12,
		OldLine:     &oldLine,
		NewLine:     &newLine,
		LineType:    "add",
		DiffHeadSHA: "diff-head",
		CommitSHA:   "commit-head",
	}
}

func TestMRReviewDraftCRUD(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	repoID := insertTestRepo(t, d, "acme", "widget")
	mrID := insertTestMR(t, d, repoID, 7, "review me", baseTime())

	draft, err := d.GetOrCreateMRReviewDraft(ctx, mrID)
	require.NoError(err)
	require.NotNil(draft)
	assert.Equal(mrID, draft.MergeRequestID)
	assert.Equal("comment", draft.Action)

	comment, err := d.CreateMRReviewDraftComment(ctx, draft.ID, MRReviewDraftCommentInput{
		Body:  "Consider renaming this.",
		Range: testReviewLineRange(),
	})
	require.NoError(err)
	require.NotNil(comment)
	assert.Equal("Consider renaming this.", comment.Body)
	assert.Equal("internal/example.go", comment.Range.Path)
	assert.Equal("diff-head", comment.Range.DiffHeadSHA)

	comments, err := d.ListMRReviewDraftComments(ctx, draft.ID)
	require.NoError(err)
	require.Len(comments, 1)
	assert.Equal(comment.ID, comments[0].ID)

	updatedRange := testReviewLineRange()
	updatedRange.Line = 13
	updated, err := d.UpdateMRReviewDraftComment(ctx, draft.ID, comment.ID, MRReviewDraftCommentInput{
		Body:  "Updated body.",
		Range: updatedRange,
	})
	require.NoError(err)
	assert.Equal("Updated body.", updated.Body)
	assert.Equal(13, updated.Range.Line)

	require.NoError(d.DeleteMRReviewDraftComment(ctx, draft.ID, comment.ID))
	comments, err = d.ListMRReviewDraftComments(ctx, draft.ID)
	require.NoError(err)
	assert.Empty(comments)

	require.NoError(d.DeleteMRReviewDraft(ctx, mrID))
	draft, err = d.GetMRReviewDraft(ctx, mrID)
	require.NoError(err)
	assert.Nil(draft)
}

func TestMRReviewThreadsUpsertAndResolve(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	repoID := insertTestRepo(t, d, "acme", "widget")
	mrID := insertTestMR(t, d, repoID, 8, "thread me", baseTime())
	created := baseTime().Add(time.Hour)
	updated := created.Add(time.Minute)

	require.NoError(d.UpsertMRReviewThreads(ctx, mrID, []MRReviewThread{
		{
			MergeRequestID:    mrID,
			ProviderThreadID:  "thread-1",
			ProviderReviewID:  "review-1",
			ProviderCommentID: "comment-1",
			Body:              "Inline note",
			AuthorLogin:       "alice",
			Range:             testReviewLineRange(),
			CreatedAt:         created,
			UpdatedAt:         updated,
			MetadataJSON:      `{"node_id":"thread-node"}`,
		},
	}))

	threads, err := d.ListMRReviewThreads(ctx, mrID)
	require.NoError(err)
	require.Len(threads, 1)
	thread := threads[0]
	assert.Equal("thread-1", thread.ProviderThreadID)
	assert.Equal("Inline note", thread.Body)
	assert.Equal("alice", thread.AuthorLogin)
	assert.False(thread.Resolved)
	assert.Equal("commit-head", thread.Range.CommitSHA)

	resolvedAt := updated.Add(time.Hour)
	require.NoError(d.SetMRReviewThreadResolved(ctx, mrID, thread.ID, true, &resolvedAt))
	got, err := d.GetMRReviewThread(ctx, mrID, thread.ID)
	require.NoError(err)
	require.NotNil(got)
	assert.True(got.Resolved)
	if assert.NotNil(got.ResolvedAt) {
		assert.Equal(resolvedAt, *got.ResolvedAt)
	}
}

func TestMRReviewThreadsUseCommentIDWhenProviderThreadIDIsMissing(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	repoID := insertTestRepo(t, d, "acme", "widget")
	mrID := insertTestMR(t, d, repoID, 8, "thread me", baseTime())

	firstRange := testReviewLineRange()
	firstRange.Line = 10
	secondRange := testReviewLineRange()
	secondRange.Line = 11

	require.NoError(d.UpsertMRReviewThreads(ctx, mrID, []MRReviewThread{
		{
			MergeRequestID:    mrID,
			ProviderReviewID:  "review-1",
			ProviderCommentID: "comment-1",
			Body:              "First inline note",
			Range:             firstRange,
			CreatedAt:         baseTime(),
			UpdatedAt:         baseTime(),
		},
		{
			MergeRequestID:    mrID,
			ProviderReviewID:  "review-1",
			ProviderCommentID: "comment-2",
			Body:              "Second inline note",
			Range:             secondRange,
			CreatedAt:         baseTime(),
			UpdatedAt:         baseTime(),
		},
	}))

	threads, err := d.ListMRReviewThreads(ctx, mrID)
	require.NoError(err)
	require.Len(threads, 2)
	assert.Equal("comment-1", threads[0].ProviderThreadID)
	assert.Equal("comment-2", threads[1].ProviderThreadID)
	assert.Equal("First inline note", threads[0].Body)
	assert.Equal("Second inline note", threads[1].Body)
}
