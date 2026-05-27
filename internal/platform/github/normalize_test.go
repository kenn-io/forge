package github

import (
	"testing"
	"time"

	gh "github.com/google/go-github/v84/github"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

func TestNormalizeIssue_ExtractsAssignees(t *testing.T) {
	require := require.New(t)

	ghIssue := &gh.Issue{
		ID:      new(int64(123)),
		Number:  new(42),
		Title:   new("Test issue"),
		State:   new("open"),
		HTMLURL: new("https://github.com/owner/repo/issues/42"),
		Body:    new("Issue body"),
		User:    &gh.User{Login: new("author")},
		Assignees: []*gh.User{
			{Login: new("alice")},
			{Login: new("bob")},
		},
		CreatedAt: &gh.Timestamp{Time: time.Now()},
		UpdatedAt: &gh.Timestamp{Time: time.Now()},
	}

	issue, err := NormalizeIssue(platform.RepoRef{}, ghIssue)
	require.NoError(err)
	require.Equal([]string{"alice", "bob"}, issue.Assignees)
}

func TestNormalizeIssue_EmptyAssignees(t *testing.T) {
	require := require.New(t)

	ghIssue := &gh.Issue{
		ID:        new(int64(123)),
		Number:    new(42),
		Title:     new("Test issue"),
		State:     new("open"),
		HTMLURL:   new("https://github.com/owner/repo/issues/42"),
		Body:      new("Issue body"),
		User:      &gh.User{Login: new("author")},
		CreatedAt: &gh.Timestamp{Time: time.Now()},
		UpdatedAt: &gh.Timestamp{Time: time.Now()},
	}

	issue, err := NormalizeIssue(platform.RepoRef{}, ghIssue)
	require.NoError(err)
	require.Empty(issue.Assignees)
}

func TestNormalizeIssue_NilAssigneeInList(t *testing.T) {
	require := require.New(t)

	ghIssue := &gh.Issue{
		ID:      new(int64(123)),
		Number:  new(42),
		Title:   new("Test issue"),
		State:   new("open"),
		HTMLURL: new("https://github.com/owner/repo/issues/42"),
		Body:    new("Issue body"),
		User:    &gh.User{Login: new("author")},
		Assignees: []*gh.User{
			nil,
			{Login: new("alice")},
			{Login: nil},
		},
		CreatedAt: &gh.Timestamp{Time: time.Now()},
		UpdatedAt: &gh.Timestamp{Time: time.Now()},
	}

	issue, err := NormalizeIssue(platform.RepoRef{}, ghIssue)
	require.NoError(err)
	require.Equal([]string{"alice"}, issue.Assignees)
}
