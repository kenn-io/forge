package github

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"go.kenn.io/middleman/internal/platform"
)

func TestNormalizeReviewThreads(t *testing.T) {
	assert := assert.New(t)

	repo := platform.RepoRef{
		Platform: platform.KindGitHub,
		Host:     "github.com",
		Owner:    "owner",
		Name:     "repo",
		RepoPath: "owner/repo",
	}

	threads := []ReviewThread{
		{
			ID:         "PRRT_thread1",
			IsResolved: false,
			Path:       "src/main.go",
			Line:       new(42),
			StartLine:  nil,
			Comments: []ReviewComment{
				{
					DatabaseID: 1001,
					Author:     "reviewer",
					Body:       "This needs a fix",
					CreatedAt:  time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC),
					DiffHunk:   "@@ -40,6 +40,8 @@",
				},
				{
					DatabaseID: 1002,
					Author:     "author",
					Body:       "Fixed!",
					CreatedAt:  time.Date(2024, 1, 15, 11, 0, 0, 0, time.UTC),
					DiffHunk:   "@@ -40,6 +40,8 @@",
				},
			},
		},
		{
			ID:         "PRRT_thread2",
			IsResolved: true,
			Path:       "src/util.go",
			Line:       new(10),
			StartLine:  new(8),
			Comments: []ReviewComment{
				{
					DatabaseID: 1003,
					Author:     "reviewer",
					Body:       "Consider refactoring",
					CreatedAt:  time.Date(2024, 1, 15, 12, 0, 0, 0, time.UTC),
					DiffHunk:   "@@ -5,10 +5,12 @@",
				},
			},
		},
	}

	events := NormalizeReviewThreads(repo, 123, threads)

	assert.Len(events, 3)

	// First comment from first thread
	assert.Equal("review_comment", events[0].EventType)
	assert.Equal(int64(1001), events[0].PlatformID)
	assert.Equal("reviewer", events[0].Author)
	assert.Equal("This needs a fix", events[0].Body)
	assert.Equal("PRRT_thread1", events[0].ThreadID)
	assert.True(events[0].Resolvable)
	assert.False(events[0].Resolved)
	assert.Contains(events[0].PositionJSON, "src/main.go")
	assert.Contains(events[0].PositionJSON, "42")
	assert.Equal("review-comment-1001", events[0].DedupeKey)

	// Second comment from first thread (same ThreadID)
	assert.Equal("PRRT_thread1", events[1].ThreadID)
	assert.Equal(int64(1002), events[1].PlatformID)
	assert.Equal("author", events[1].Author)
	assert.False(events[1].Resolved)

	// Comment from second thread (resolved)
	assert.Equal("PRRT_thread2", events[2].ThreadID)
	assert.Equal(int64(1003), events[2].PlatformID)
	assert.True(events[2].Resolved)
	assert.Contains(events[2].PositionJSON, "src/util.go")
	assert.Contains(events[2].PositionJSON, "start_line")
}
