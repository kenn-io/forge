package github

import "time"

// gqlReviewThread represents a GitHub pull request review thread from GraphQL.
type gqlReviewThread struct {
	ID         string
	IsResolved bool
	Path       string
	Line       *int
	StartLine  *int
	Comments   struct {
		Nodes    []gqlReviewComment
		PageInfo pageInfo
	} `graphql:"comments(first: 100)"`
}

// gqlReviewComment represents a comment within a review thread.
type gqlReviewComment struct {
	DatabaseId int64
	Author     struct{ Login string }
	Body       string
	CreatedAt  time.Time
	DiffHunk   string
}

// ReviewThread is the exported type for review thread data.
type ReviewThread struct {
	ID         string
	IsResolved bool
	Path       string
	Line       *int
	StartLine  *int
	Comments   []ReviewComment
}

// ReviewComment is the exported type for review comment data.
type ReviewComment struct {
	DatabaseID int64
	Author     string
	Body       string
	CreatedAt  time.Time
	DiffHunk   string
}

// adaptReviewThread converts a GraphQL review thread to the exported type.
//
//nolint:unused // Will be used in upcoming review thread sync implementation
func adaptReviewThread(gql *gqlReviewThread) ReviewThread {
	thread := ReviewThread{
		ID:         gql.ID,
		IsResolved: gql.IsResolved,
		Path:       gql.Path,
		Line:       gql.Line,
		StartLine:  gql.StartLine,
		Comments:   make([]ReviewComment, 0, len(gql.Comments.Nodes)),
	}
	for i := range gql.Comments.Nodes {
		c := &gql.Comments.Nodes[i]
		thread.Comments = append(thread.Comments, ReviewComment{
			DatabaseID: c.DatabaseId,
			Author:     c.Author.Login,
			Body:       c.Body,
			CreatedAt:  c.CreatedAt,
			DiffHunk:   c.DiffHunk,
		})
	}
	return thread
}
