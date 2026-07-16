package db

import "time"

// canonicalUTCTime converts application timestamps to UTC before they cross
// the SQLite write boundary. This keeps raw text storage stable so SQL
// ordering/filtering on DATETIME columns reflects the actual instant.
func canonicalUTCTime(t time.Time) time.Time {
	if t.IsZero() {
		return t
	}
	return t.UTC()
}

func canonicalUTCTimePtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	utc := canonicalUTCTime(*t)
	return &utc
}

// canonicalizeMergeRequestTimestamps enforces the repo-wide contract that PR
// timestamps are stored in UTC, even if the caller constructed local-zone
// fixtures or legacy values upstream.
func canonicalizeMergeRequestTimestamps(mr *MergeRequest) {
	if mr == nil {
		return
	}
	mr.CreatedAt = canonicalUTCTime(mr.CreatedAt)
	mr.UpdatedAt = canonicalUTCTime(mr.UpdatedAt)
	mr.LastActivityAt = canonicalUTCTime(mr.LastActivityAt)
	mr.MergedAt = canonicalUTCTimePtr(mr.MergedAt)
	mr.ClosedAt = canonicalUTCTimePtr(mr.ClosedAt)
	mr.DetailFetchedAt = canonicalUTCTimePtr(mr.DetailFetchedAt)
}

// canonicalizeIssueTimestamps applies the same UTC storage contract to issue
// timestamps before insert/update statements execute.
func canonicalizeIssueTimestamps(issue *Issue) {
	if issue == nil {
		return
	}
	issue.CreatedAt = canonicalUTCTime(issue.CreatedAt)
	issue.UpdatedAt = canonicalUTCTime(issue.UpdatedAt)
	issue.LastActivityAt = canonicalUTCTime(issue.LastActivityAt)
	issue.ClosedAt = canonicalUTCTimePtr(issue.ClosedAt)
	issue.DetailFetchedAt = canonicalUTCTimePtr(issue.DetailFetchedAt)
}

// canonicalizeMREventTimestamps normalizes event times before activity rows
// reach SQLite so raw created_at text stays chronologically sortable.
func canonicalizeMREventTimestamps(event *MREvent) {
	if event == nil {
		return
	}
	event.CreatedAt = canonicalUTCTime(event.CreatedAt)
}

// canonicalizeIssueEventTimestamps normalizes issue activity timestamps before
// they are persisted.
func canonicalizeIssueEventTimestamps(event *IssueEvent) {
	if event == nil {
		return
	}
	event.CreatedAt = canonicalUTCTime(event.CreatedAt)
}
