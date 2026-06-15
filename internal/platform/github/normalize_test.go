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

func TestNormalizeCommentEventsPreserveHTMLURL(t *testing.T) {
	assert := assert.New(t)
	commentID := int64(123)
	commentURL := "https://github.com/acme/widget/pull/7#issuecomment-123"
	issueComment := &gh.IssueComment{
		ID:      &commentID,
		HTMLURL: &commentURL,
	}

	prEvent := NormalizeCommentEvent(platform.RepoRef{Owner: "acme", Name: "widget"}, 7, issueComment)
	issueEvent := NormalizeIssueCommentEvent(platform.RepoRef{Owner: "acme", Name: "widget"}, 9, issueComment)

	assert.Equal(commentURL, prEvent.DirectURL)
	assert.Equal(commentURL, issueEvent.DirectURL)
}

func TestNormalizeReviewCommentEventPreservesHTMLURL(t *testing.T) {
	commentID := int64(456)
	commentURL := "https://github.com/acme/widget/pull/7#discussion_r456"
	comment := &gh.PullRequestComment{
		ID:      &commentID,
		HTMLURL: &commentURL,
	}

	event := NormalizeReviewCommentEvent(platform.RepoRef{Owner: "acme", Name: "widget"}, 7, comment)

	assert.Equal(t, commentURL, event.DirectURL)
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

func TestNormalizePullRequestExtractsAssigneesAndRequestedReviewers(t *testing.T) {
	tests := []struct {
		name          string
		assignees     []*gh.User
		reviewers     []*gh.User
		wantAssignees []string
		wantReviewers []string
	}{
		{
			name:          "fields absent stay nil so persistence preserves prior values",
			assignees:     nil,
			reviewers:     nil,
			wantAssignees: nil,
			wantReviewers: nil,
		},
		{
			name:          "explicit empty arrays become known-empty sets",
			assignees:     []*gh.User{},
			reviewers:     []*gh.User{},
			wantAssignees: []string{},
			wantReviewers: []string{},
		},
		{
			name: "logins extracted and nil or empty entries skipped",
			assignees: []*gh.User{
				{Login: new("alice")},
				nil,
				{Login: new("")},
				{Login: new("bob")},
			},
			reviewers:     []*gh.User{{Login: new("carol")}},
			wantAssignees: []string{"alice", "bob"},
			wantReviewers: []string{"carol"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ghPR := &gh.PullRequest{
				ID:                 new(int64(1)),
				Number:             new(7),
				Assignees:          tc.assignees,
				RequestedReviewers: tc.reviewers,
			}
			mr, err := NormalizePullRequest(platform.RepoRef{Owner: "acme", Name: "widget"}, ghPR)
			require.NoError(t, err)
			assert.Equal(t, tc.wantAssignees, mr.Assignees)
			assert.Equal(t, tc.wantReviewers, mr.RequestedReviewers)
		})
	}
}
