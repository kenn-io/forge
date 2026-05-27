package github

import (
	"testing"

	gh "github.com/google/go-github/v84/github"
	"github.com/stretchr/testify/assert"
	"go.kenn.io/middleman/internal/platform"
)

func TestNormalizeReviewCommentEventUsesReviewThreadDedupeKey(t *testing.T) {
	id := int64(222)
	comment := &gh.PullRequestComment{ID: &id}

	event := NormalizeReviewCommentEvent(platform.RepoRef{
		Owner: "acme",
		Name:  "widget",
	}, 7, comment)

	assert.Equal(t, "review_comment:222", event.DedupeKey)
}
