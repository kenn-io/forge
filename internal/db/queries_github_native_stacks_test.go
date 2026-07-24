package db

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReplaceGitHubNativeStackReplacesMemberSnapshot(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	database := openTestDB(t)
	repoID := insertTestRepo(t, database, "acme", "widgets")
	createdAt := time.Date(2026, time.July, 24, 12, 0, 0, 0, time.UTC)
	observedAt := createdAt.Add(time.Minute)

	stack := GitHubNativeStack{
		RepoID: repoID, GitHubID: 987, Number: 42, Size: 2,
		BaseRef: "main", IsOpen: true, GitHubCreatedAt: createdAt,
		ContentFingerprint: "first", LastObservedAt: observedAt,
		Members: []GitHubNativeStackMember{
			{Position: 1, PullRequestNumber: 101, State: "open", HeadRef: "feature/a", HeadSHA: "aaa"},
			{Position: 2, PullRequestNumber: 102, State: "open", Draft: true, HeadRef: "feature/b", HeadSHA: "bbb"},
		},
	}
	require.NoError(database.ReplaceGitHubNativeStack(t.Context(), stack))

	mergedAt := createdAt.Add(2 * time.Hour)
	stack.Size = 1
	stack.ContentFingerprint = "second"
	stack.LastObservedAt = observedAt.Add(time.Minute)
	stack.Members = []GitHubNativeStackMember{
		{Position: 1, PullRequestNumber: 102, State: "closed", MergedAt: &mergedAt, HeadRef: "feature/b", HeadSHA: "ccc"},
	}
	require.NoError(database.ReplaceGitHubNativeStack(t.Context(), stack))

	got, err := database.ListGitHubNativeStacks(t.Context(), repoID)
	require.NoError(err)
	require.Len(got, 1)
	assert.Equal("second", got[0].ContentFingerprint)
	assert.Equal(1, got[0].Size)
	require.Len(got[0].Members, 1)
	assert.Equal(102, got[0].Members[0].PullRequestNumber)
	assert.Equal("ccc", got[0].Members[0].HeadSHA)
	require.NotNil(got[0].Members[0].MergedAt)
	assert.True(got[0].Members[0].MergedAt.Equal(mergedAt))
}

func TestGitHubNativeStacksAreRepositoryScopedAndDeletable(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	database := openTestDB(t)
	repoA := insertTestRepo(t, database, "acme", "widgets")
	repoB := insertTestRepo(t, database, "acme", "tools")
	now := time.Date(2026, time.July, 24, 12, 0, 0, 0, time.UTC)

	for _, repoID := range []int64{repoA, repoB} {
		require.NoError(database.ReplaceGitHubNativeStack(t.Context(), GitHubNativeStack{
			RepoID: repoID, GitHubID: repoID, Number: 7, Size: 1,
			BaseRef: "main", IsOpen: true, GitHubCreatedAt: now,
			ContentFingerprint: "same-number", LastObservedAt: now,
			Members: []GitHubNativeStackMember{{
				Position: 1, PullRequestNumber: 10, State: "open",
				HeadRef: "feature", HeadSHA: "abc",
			}},
		}))
	}

	require.NoError(database.DeleteGitHubNativeStacks(t.Context(), repoA, []int{7}))
	stacksA, err := database.ListGitHubNativeStacks(t.Context(), repoA)
	require.NoError(err)
	stacksB, err := database.ListGitHubNativeStacks(t.Context(), repoB)
	require.NoError(err)
	assert.Empty(stacksA)
	assert.Len(stacksB, 1)
}
