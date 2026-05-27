package github

import (
	"testing"
	"time"

	gh "github.com/google/go-github/v84/github"
	"github.com/stretchr/testify/require"
	"go.kenn.io/middleman/internal/platform"
)

func TestNormalizeIssue_ExtractsAssignees(t *testing.T) {
	require := require.New(t)

	assignee1Login := "alice"
	assignee2Login := "bob"
	ghIssue := &gh.Issue{
		ID:      gh.Ptr(int64(123)),
		Number:  gh.Ptr(42),
		Title:   gh.Ptr("Test issue"),
		State:   gh.Ptr("open"),
		HTMLURL: gh.Ptr("https://github.com/owner/repo/issues/42"),
		Body:    gh.Ptr("Issue body"),
		User:    &gh.User{Login: gh.Ptr("author")},
		Assignees: []*gh.User{
			{Login: &assignee1Login},
			{Login: &assignee2Login},
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
		ID:        gh.Ptr(int64(123)),
		Number:    gh.Ptr(42),
		Title:     gh.Ptr("Test issue"),
		State:     gh.Ptr("open"),
		HTMLURL:   gh.Ptr("https://github.com/owner/repo/issues/42"),
		Body:      gh.Ptr("Issue body"),
		User:      &gh.User{Login: gh.Ptr("author")},
		CreatedAt: &gh.Timestamp{Time: time.Now()},
		UpdatedAt: &gh.Timestamp{Time: time.Now()},
	}

	issue, err := NormalizeIssue(platform.RepoRef{}, ghIssue)
	require.NoError(err)
	require.Empty(issue.Assignees)
}

func TestNormalizeIssue_NilAssigneeInList(t *testing.T) {
	require := require.New(t)

	assigneeLogin := "alice"
	ghIssue := &gh.Issue{
		ID:      gh.Ptr(int64(123)),
		Number:  gh.Ptr(42),
		Title:   gh.Ptr("Test issue"),
		State:   gh.Ptr("open"),
		HTMLURL: gh.Ptr("https://github.com/owner/repo/issues/42"),
		Body:    gh.Ptr("Issue body"),
		User:    &gh.User{Login: gh.Ptr("author")},
		Assignees: []*gh.User{
			nil,
			{Login: &assigneeLogin},
			{Login: nil},
		},
		CreatedAt: &gh.Timestamp{Time: time.Now()},
		UpdatedAt: &gh.Timestamp{Time: time.Now()},
	}

	issue, err := NormalizeIssue(platform.RepoRef{}, ghIssue)
	require.NoError(err)
	require.Equal([]string{"alice"}, issue.Assignees)
}
