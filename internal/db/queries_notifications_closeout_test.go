package db

import (
	"context"
	"database/sql"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// closeoutFixture seeds one active repository with a closed PR #7, an open
// PR #8, a closed issue #9, and an open issue #10.
type closeoutFixture struct {
	repoID int64
	now    time.Time
}

func seedCloseoutFixture(t *testing.T, d *DB) closeoutFixture {
	t.Helper()
	require := require.New(t)
	now := time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC)
	repoID, err := d.UpsertRepo(t.Context(), verifiedTestRepoIdentity(
		"github", "github.com", "acme", "widget",
	))
	require.NoError(err)
	for _, mr := range []struct {
		number int
		state  MergeRequestState
		closed *time.Time
	}{
		{number: 7, state: "closed", closed: &now},
		{number: 8, state: "open"},
	} {
		_, err = d.UpsertMergeRequest(t.Context(), &MergeRequest{
			RepoID: repoID, PlatformID: int64(100 + mr.number), Number: mr.number,
			URL:   "https://github.com/acme/widget/pull/" + strconv.Itoa(mr.number),
			Title: "PR", Author: "octocat", State: mr.state,
			CreatedAt: now, UpdatedAt: now, LastActivityAt: now, ClosedAt: mr.closed,
		})
		require.NoError(err)
	}
	for _, issue := range []struct {
		number int
		state  string
		closed *time.Time
	}{
		{number: 9, state: "closed", closed: &now},
		{number: 10, state: "open"},
	} {
		_, err = d.UpsertIssue(t.Context(), &Issue{
			RepoID: repoID, PlatformID: int64(200 + issue.number), Number: issue.number,
			URL:   "https://github.com/acme/widget/issues/" + strconv.Itoa(issue.number),
			Title: "Issue", State: issue.state,
			CreatedAt: now, UpdatedAt: now, LastActivityAt: now, ClosedAt: issue.closed,
		})
		require.NoError(err)
	}
	return closeoutFixture{repoID: repoID, now: now}
}

// closeoutNotification builds an active notification for one item. A nil
// repoID produces a legacy row that must resolve through repository keys.
func closeoutNotification(threadID, itemType string, number int, repoID *int64, at time.Time) Notification {
	n := notificationFixture(threadID, "mention", at)
	n.ItemType = itemType
	n.ItemNumber = &number
	n.RepoID = repoID
	if itemType == "issue" {
		n.SubjectType = "Issue"
	}
	return n
}

type closeoutState struct {
	doneAt     *time.Time
	doneReason string
}

func readCloseoutState(t *testing.T, d *DB, threadID string) closeoutState {
	t.Helper()
	var doneAt sql.NullString
	var reason string
	require.NoError(t, d.ReadDB().QueryRowContext(t.Context(), `
		SELECT done_at, done_reason FROM forge_notification_items
		WHERE platform_notification_id = ?`, threadID).Scan(&doneAt, &reason))
	state := closeoutState{doneReason: reason}
	if doneAt.Valid {
		parsed, err := parseDBTime(doneAt.String)
		require.NoError(t, err)
		state.doneAt = &parsed
	}
	return state
}

func TestMarkClosedLinkedNotificationsDoneSweep(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	d := openTestDB(t)
	fx := seedCloseoutFixture(t, d)
	repoID := fx.repoID

	// A second, inactive repository shares the route keys of nothing and owns
	// its own closed PR #7; its notification must stay active.
	inactiveRepoID, err := d.UpsertRepo(t.Context(), verifiedTestRepoIdentity(
		"github", "github.com", "acme", "retired",
	))
	require.NoError(err)
	_, err = d.UpsertMergeRequest(t.Context(), &MergeRequest{
		RepoID: inactiveRepoID, PlatformID: 900, Number: 7,
		URL: "https://github.com/acme/retired/pull/7", Title: "Closed in retired repo",
		Author: "octocat", State: "closed",
		CreatedAt: fx.now, UpdatedAt: fx.now, LastActivityAt: fx.now, ClosedAt: &fx.now,
	})
	require.NoError(err)
	_, err = d.rw.ExecContext(t.Context(),
		`UPDATE forge_repos SET lifecycle_state = 'inactive' WHERE id = ?`, inactiveRepoID)
	require.NoError(err)

	inactiveItem := closeoutNotification("inactive-pr", "pr", 7, &inactiveRepoID, fx.now)
	inactiveItem.RepoName = "retired"
	unlinkedInactive := closeoutNotification("unlinked-inactive-pr", "pr", 7, nil, fx.now)
	unlinkedInactive.RepoName = "retired"

	require.NoError(d.UpsertNotifications(t.Context(), []Notification{
		closeoutNotification("linked-closed-pr", "pr", 7, &repoID, fx.now),
		closeoutNotification("linked-open-pr", "pr", 8, &repoID, fx.now),
		closeoutNotification("linked-closed-issue", "issue", 9, &repoID, fx.now),
		closeoutNotification("linked-open-issue", "issue", 10, &repoID, fx.now),
		inactiveItem,
		unlinkedInactive,
	}))
	// Legacy rows without repo_id must be inserted after UpsertNotifications
	// would have resolved them, so clear the resolved id directly.
	require.NoError(d.UpsertNotifications(t.Context(), []Notification{
		closeoutNotification("unlinked-closed-pr", "pr", 7, nil, fx.now),
		closeoutNotification("unlinked-open-pr", "pr", 8, nil, fx.now),
		closeoutNotification("unlinked-closed-issue", "issue", 9, nil, fx.now),
	}))
	_, err = d.rw.ExecContext(t.Context(), `UPDATE forge_notification_items SET repo_id = NULL
		WHERE platform_notification_id LIKE 'unlinked-%'`)
	require.NoError(err)

	first := fx.now.Add(time.Minute)
	require.NoError(d.MarkClosedLinkedNotificationsDone(t.Context(), first))

	for _, threadID := range []string{
		"linked-closed-pr", "linked-closed-issue", "unlinked-closed-pr", "unlinked-closed-issue",
	} {
		state := readCloseoutState(t, d, threadID)
		if assert.NotNil(state.doneAt, threadID) {
			assert.Equal(first, *state.doneAt, threadID)
		}
		assert.Equal("closed", state.doneReason, threadID)
	}
	for _, threadID := range []string{
		"linked-open-pr", "linked-open-issue", "unlinked-open-pr", "inactive-pr", "unlinked-inactive-pr",
	} {
		state := readCloseoutState(t, d, threadID)
		assert.Nil(state.doneAt, threadID)
		assert.Empty(state.doneReason, threadID)
	}

	// A second sweep must not move done_at forward.
	require.NoError(d.MarkClosedLinkedNotificationsDone(t.Context(), first.Add(time.Hour)))
	state := readCloseoutState(t, d, "linked-closed-pr")
	require.NotNil(state.doneAt)
	assert.Equal(first, *state.doneAt)
}

func TestMarkClosedLinkedItemNotificationsDoneScopesToOneItem(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	d := openTestDB(t)
	fx := seedCloseoutFixture(t, d)
	repoID := fx.repoID

	// A sibling repository with the same numbers must never be touched by a
	// call scoped to the fixture repository.
	otherRepoID, err := d.UpsertRepo(t.Context(), verifiedTestRepoIdentity(
		"github", "github.com", "acme", "gadget",
	))
	require.NoError(err)
	_, err = d.UpsertMergeRequest(t.Context(), &MergeRequest{
		RepoID: otherRepoID, PlatformID: 700, Number: 7,
		URL: "https://github.com/acme/gadget/pull/7", Title: "Closed in gadget",
		Author: "octocat", State: "merged",
		CreatedAt: fx.now, UpdatedAt: fx.now, LastActivityAt: fx.now, ClosedAt: &fx.now,
	})
	require.NoError(err)
	otherItem := closeoutNotification("other-repo-pr", "pr", 7, &otherRepoID, fx.now)
	otherItem.RepoName = "gadget"

	require.NoError(d.UpsertNotifications(t.Context(), []Notification{
		closeoutNotification("linked-closed-pr", "pr", 7, &repoID, fx.now),
		closeoutNotification("linked-open-pr", "pr", 8, &repoID, fx.now),
		closeoutNotification("linked-closed-issue", "issue", 9, &repoID, fx.now),
		closeoutNotification("issue-nine-as-pr", "pr", 9, &repoID, fx.now),
		otherItem,
	}))
	require.NoError(d.UpsertNotifications(t.Context(), []Notification{
		closeoutNotification("unlinked-closed-pr", "pr", 7, nil, fx.now),
		closeoutNotification("unlinked-closed-issue", "issue", 9, nil, fx.now),
	}))
	_, err = d.rw.ExecContext(t.Context(), `UPDATE forge_notification_items SET repo_id = NULL
		WHERE platform_notification_id LIKE 'unlinked-%'`)
	require.NoError(err)

	at := fx.now.Add(time.Minute)

	// Closing out an open PR changes nothing.
	require.NoError(d.MarkClosedLinkedPRNotificationsDone(t.Context(), at, repoID, 8))
	for _, threadID := range []string{
		"linked-closed-pr", "linked-open-pr", "linked-closed-issue", "unlinked-closed-pr",
		"unlinked-closed-issue", "other-repo-pr", "issue-nine-as-pr",
	} {
		assert.Nil(readCloseoutState(t, d, threadID).doneAt, threadID)
	}

	require.NoError(d.MarkClosedLinkedPRNotificationsDone(t.Context(), at, repoID, 7))
	for _, threadID := range []string{"linked-closed-pr", "unlinked-closed-pr"} {
		state := readCloseoutState(t, d, threadID)
		if assert.NotNil(state.doneAt, threadID) {
			assert.Equal(at, *state.doneAt, threadID)
		}
		assert.Equal("closed", state.doneReason, threadID)
	}
	for _, threadID := range []string{
		"linked-open-pr", "linked-closed-issue", "unlinked-closed-issue", "other-repo-pr", "issue-nine-as-pr",
	} {
		assert.Nil(readCloseoutState(t, d, threadID).doneAt, threadID)
	}

	require.NoError(d.MarkClosedLinkedIssueNotificationsDone(t.Context(), at, repoID, 9))
	for _, threadID := range []string{"linked-closed-issue", "unlinked-closed-issue"} {
		state := readCloseoutState(t, d, threadID)
		if assert.NotNil(state.doneAt, threadID) {
			assert.Equal(at, *state.doneAt, threadID)
		}
		assert.Equal("closed", state.doneReason, threadID)
	}
	// A PR-typed notification with the closed issue's number is not an issue.
	assert.Nil(readCloseoutState(t, d, "issue-nine-as-pr").doneAt)
	assert.Nil(readCloseoutState(t, d, "other-repo-pr").doneAt)

	// Re-running for the same item keeps the original done_at.
	require.NoError(d.MarkClosedLinkedPRNotificationsDone(t.Context(), at.Add(time.Hour), repoID, 7))
	state := readCloseoutState(t, d, "linked-closed-pr")
	require.NotNil(state.doneAt)
	assert.Equal(at, *state.doneAt)
}

func TestMarkClosedLinkedItemNotificationsDoneSkipsInactiveRepository(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	d := openTestDB(t)
	fx := seedCloseoutFixture(t, d)
	require.NoError(d.UpsertNotifications(t.Context(), []Notification{
		closeoutNotification("linked-closed-pr", "pr", 7, &fx.repoID, fx.now),
	}))
	_, err := d.rw.ExecContext(t.Context(),
		`UPDATE forge_repos SET lifecycle_state = 'inactive' WHERE id = ?`, fx.repoID)
	require.NoError(err)

	require.NoError(d.MarkClosedLinkedPRNotificationsDone(t.Context(), fx.now.Add(time.Minute), fx.repoID, 7))
	assert.Nil(readCloseoutState(t, d, "linked-closed-pr").doneAt)
}

// TestClosedLinkedNotificationStatementsUseItemIndexes guards the plan shape:
// every merge-request or issue access must be an index search, never a scan.
// The previous single-statement form scanned the entire item table once per
// active notification.
func TestClosedLinkedNotificationStatementsUseItemIndexes(t *testing.T) {
	d := openTestDB(t)
	fx := seedCloseoutFixture(t, d)
	now := fx.now
	sweepArgs := func(itemType string) []any { return []any{now, itemType} }
	itemArgs := func(itemType string) []any {
		return []any{now, fx.repoID, itemType, 7, fx.repoID, 7}
	}

	type statement struct {
		name string
		sql  string
		args []any
	}
	var statements []statement
	for _, subject := range closedLinkedNotificationSubjects {
		linked, unlinked := subject.sweepStatements()
		statements = append(statements,
			statement{subject.itemType + " sweep linked", linked, sweepArgs(subject.itemType)},
			statement{subject.itemType + " sweep unlinked", unlinked, sweepArgs(subject.itemType)},
		)
		linked, unlinked = subject.itemStatements()
		statements = append(statements,
			statement{subject.itemType + " item linked", linked, itemArgs(subject.itemType)},
			statement{subject.itemType + " item unlinked", unlinked, itemArgs(subject.itemType)},
		)
	}

	for _, st := range statements {
		t.Run(st.name, func(t *testing.T) {
			assert := assert.New(t)
			plan := explainQueryPlan(t, d.ReadDB(), st.sql, st.args...)
			t.Logf("plan: %s", strings.Join(plan, " | "))
			assert.NotEmpty(plan)
			var itemSearches int
			for _, detail := range plan {
				assert.False(strings.HasPrefix(detail, "SCAN item"), "plan step: %s", detail)
				assert.False(strings.HasPrefix(detail, "SCAN forge_merge_requests"), "plan step: %s", detail)
				assert.False(strings.HasPrefix(detail, "SCAN forge_issues"), "plan step: %s", detail)
				assert.False(strings.HasPrefix(detail, "SCAN forge_notification_items"), "plan step: %s", detail)
				if strings.HasPrefix(detail, "SEARCH item USING") &&
					strings.Contains(detail, "INDEX") &&
					strings.Contains(detail, "repo_id=? AND number=?") {
					itemSearches++
				}
			}
			assert.Equal(1, itemSearches, "expected exactly one unique-index item lookup, plan: %s",
				strings.Join(plan, " | "))
		})
	}
}

func explainQueryPlan(t *testing.T, ro *sql.DB, query string, args ...any) []string {
	t.Helper()
	rows, err := ro.QueryContext(context.Background(), "EXPLAIN QUERY PLAN "+query, args...)
	require.NoError(t, err)
	defer rows.Close()
	var details []string
	for rows.Next() {
		var id, parent, notUsed int
		var detail string
		require.NoError(t, rows.Scan(&id, &parent, &notUsed, &detail))
		details = append(details, detail)
	}
	require.NoError(t, rows.Err())
	return details
}
