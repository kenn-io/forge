package db

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func baseTime() time.Time {
	return time.Date(2024, 1, 15, 12, 0, 0, 0, time.UTC)
}

type testMROpt func(*MergeRequest)

func testMR(repoID int64, number int, opts ...testMROpt) *MergeRequest {
	now := baseTime()
	mr := &MergeRequest{
		RepoID:         repoID,
		PlatformID:     repoID*10000 + int64(number),
		Number:         number,
		URL:            fmt.Sprintf("https://github.com/example/repo/pull/%d", number),
		Title:          fmt.Sprintf("PR %d", number),
		Author:         "author",
		State:          "open",
		HeadBranch:     "feature",
		BaseBranch:     "main",
		CreatedAt:      now,
		UpdatedAt:      now,
		LastActivityAt: now,
	}
	for _, opt := range opts {
		opt(mr)
	}
	return mr
}

func withMRTitle(title string) testMROpt {
	return func(mr *MergeRequest) { mr.Title = title }
}

func withMRActivity(activity time.Time) testMROpt {
	return func(mr *MergeRequest) {
		mr.CreatedAt = activity
		mr.UpdatedAt = activity
		mr.LastActivityAt = activity
	}
}

func withMRBranches(head, base string) testMROpt {
	return func(mr *MergeRequest) {
		mr.HeadBranch = head
		mr.BaseBranch = base
	}
}

func withMRState(state MergeRequestState) testMROpt {
	return func(mr *MergeRequest) { mr.State = state }
}

func insertTestMRWithOptions(t *testing.T, d *DB, mr *MergeRequest) int64 {
	t.Helper()
	id, err := d.UpsertMergeRequest(t.Context(), mr)
	require.NoErrorf(t, err, "UpsertMergeRequest %d", mr.Number)
	return id
}

func insertTestMR(t *testing.T, d *DB, repoID int64, number int, title string, activity time.Time) int64 {
	t.Helper()
	return insertTestMRWithOptions(t, d, testMR(repoID, number, withMRTitle(title), withMRActivity(activity)))
}

type testIssueOpt func(*Issue)

func testIssue(repoID int64, number int, opts ...testIssueOpt) *Issue {
	now := baseTime()
	issue := &Issue{
		RepoID:         repoID,
		PlatformID:     repoID*10000 + int64(number),
		Number:         number,
		URL:            fmt.Sprintf("https://github.com/example/repo/issues/%d", number),
		Title:          fmt.Sprintf("Issue %d", number),
		Author:         "author",
		State:          "open",
		CreatedAt:      now,
		UpdatedAt:      now,
		LastActivityAt: now,
	}
	for _, opt := range opts {
		opt(issue)
	}
	return issue
}

func withIssueTitle(title string) testIssueOpt {
	return func(issue *Issue) { issue.Title = title }
}

func withIssueActivity(activity time.Time) testIssueOpt {
	return func(issue *Issue) {
		issue.CreatedAt = activity
		issue.UpdatedAt = activity
		issue.LastActivityAt = activity
	}
}

func insertTestIssueWithOptions(t *testing.T, d *DB, issue *Issue) int64 {
	t.Helper()
	id, err := d.UpsertIssue(t.Context(), issue)
	require.NoErrorf(t, err, "UpsertIssue %d", issue.Number)
	return id
}

func insertTestIssue(t *testing.T, d *DB, repoID int64, number int, title string, activity time.Time) int64 {
	t.Helper()
	return insertTestIssueWithOptions(t, d, testIssue(repoID, number, withIssueTitle(title), withIssueActivity(activity)))
}
