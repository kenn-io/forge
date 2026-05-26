package github

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestAdaptReviewThread(t *testing.T) {
	assert := assert.New(t)

	gql := &gqlReviewThread{
		ID:         "PRRT_abc123",
		IsResolved: true,
		Path:       "src/main.go",
		Line:       new(42),
		StartLine:  new(40),
	}
	gql.Comments.Nodes = []gqlReviewComment{
		{
			DatabaseId: 1001,
			Author:     struct{ Login string }{Login: "reviewer"},
			Body:       "Needs fix",
			CreatedAt:  time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC),
			DiffHunk:   "@@ -40,6 +40,8 @@",
		},
		{
			DatabaseId: 1002,
			Author:     struct{ Login string }{Login: "author"},
			Body:       "Fixed",
			CreatedAt:  time.Date(2024, 1, 15, 11, 0, 0, 0, time.UTC),
			DiffHunk:   "@@ -40,6 +40,8 @@",
		},
	}

	thread := adaptReviewThread(gql)

	assert.Equal("PRRT_abc123", thread.ID)
	assert.True(thread.IsResolved)
	assert.Equal("src/main.go", thread.Path)
	assert.Equal(42, *thread.Line)
	assert.Equal(40, *thread.StartLine)
	assert.Len(thread.Comments, 2)

	assert.Equal(int64(1001), thread.Comments[0].DatabaseID)
	assert.Equal("reviewer", thread.Comments[0].Author)
	assert.Equal("Needs fix", thread.Comments[0].Body)
	assert.Equal("@@ -40,6 +40,8 @@", thread.Comments[0].DiffHunk)

	assert.Equal(int64(1002), thread.Comments[1].DatabaseID)
	assert.Equal("author", thread.Comments[1].Author)
}

func TestAdaptReviewThread_EmptyComments(t *testing.T) {
	assert := assert.New(t)

	gql := &gqlReviewThread{
		ID:         "PRRT_empty",
		IsResolved: false,
		Path:       "README.md",
	}

	thread := adaptReviewThread(gql)

	assert.Equal("PRRT_empty", thread.ID)
	assert.False(thread.IsResolved)
	assert.Empty(thread.Comments)
}

func TestAdaptReviewThread_NilLineNumbers(t *testing.T) {
	assert := assert.New(t)

	gql := &gqlReviewThread{
		ID:        "PRRT_nolines",
		Path:      "file.txt",
		Line:      nil,
		StartLine: nil,
	}

	thread := adaptReviewThread(gql)

	assert.Nil(thread.Line)
	assert.Nil(thread.StartLine)
}
