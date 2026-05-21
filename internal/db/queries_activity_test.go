package db

import (
	"fmt"
	"testing"
	"time"

	Assert "github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestListActivity(t *testing.T) {
	d := openTestDB(t)
	ctx := t.Context()
	base := baseTime()

	repoA := insertTestRepo(t, d, "alice", "alpha")
	repoB := insertTestRepo(t, d, "bob", "beta")

	prID1 := insertTestMR(t, d, repoA, 1, "Fix bug", base)
	prID2 := insertTestMR(
		t, d, repoB, 2, "Add feature", base.Add(1*time.Minute))
	issueID1 := insertTestIssue(
		t, d, repoA, 10, "Crash on startup", base.Add(2*time.Minute))

	err := d.UpsertMREvents(ctx, []MREvent{
		{MergeRequestID: prID1, EventType: "issue_comment", Author: "carol",
			Body:      "Looks good to me",
			CreatedAt: base.Add(3 * time.Minute),
			DedupeKey: "comment-1"},
		{MergeRequestID: prID2, EventType: "review", Author: "dave",
			Summary:   "APPROVED",
			CreatedAt: base.Add(4 * time.Minute),
			DedupeKey: "review-1"},
		{MergeRequestID: prID1, EventType: "commit", Author: "alice",
			Summary: "abc123", Body: "fix: handle nil",
			CreatedAt: base.Add(5 * time.Minute),
			DedupeKey: "commit-abc123"},
		{MergeRequestID: prID1, EventType: "review_comment", Author: "eve",
			Body:      "nit: rename var",
			CreatedAt: base.Add(6 * time.Minute),
			DedupeKey: "review_comment-1"},
	})
	require.NoError(t, err)

	err = d.UpsertIssueEvents(ctx, []IssueEvent{
		{IssueID: issueID1, EventType: "issue_comment", Author: "frank",
			Body:      "Can reproduce on macOS",
			CreatedAt: base.Add(7 * time.Minute),
			DedupeKey: "icomment-1"},
	})
	require.NoError(t, err)

	t.Run("unfiltered returns all types in desc order", func(t *testing.T) {
		assert := Assert.New(t)
		items, err := d.ListActivity(
			ctx, ListActivityOpts{Limit: 50})
		require.NoError(t, err)
		// Expected order (newest first):
		// 1. issue comment (base+7m) - review_comment excluded
		// 2. commit (base+5m)
		// 3. review (base+4m)
		// 4. PR comment (base+3m)
		// 5. new issue (base+2m)
		// 6. new PR bob/beta#2 (base+1m)
		// 7. new PR alice/alpha#1 (base)
		require.Len(t, items, 7)
		assert.Equal("comment", items[0].ActivityType)
		assert.Equal("issue", items[0].ItemType)
		assert.Equal("commit", items[1].ActivityType)
		assert.Equal("review", items[2].ActivityType)
		assert.Equal("comment", items[3].ActivityType)
		assert.Equal("pr", items[3].ItemType)
		assert.Equal("new_issue", items[4].ActivityType)
		assert.Equal("new_pr", items[5].ActivityType)
		assert.Equal("github.com", items[5].PlatformHost)
		assert.Equal("bob", items[5].RepoOwner)
		assert.Equal("new_pr", items[6].ActivityType)
		assert.Equal("alice", items[6].RepoOwner)
	})

	t.Run("repo filter", func(t *testing.T) {
		assert := Assert.New(t)
		items, err := d.ListActivity(ctx, ListActivityOpts{
			Repo: "alice/alpha", Limit: 50,
		})
		require.NoError(t, err)
		for _, it := range items {
			assert.Equal("alice", it.RepoOwner)
			assert.Equal("alpha", it.RepoName)
		}
	})

	t.Run("multiple repo filters", func(t *testing.T) {
		assert := Assert.New(t)
		require := require.New(t)
		d := openTestDB(t)
		ctx := t.Context()
		base := baseTime()

		firstRepo := insertTestRepoWithHost(t, d, "alice", "alpha", "github.com")
		secondRepo := insertTestRepoWithHost(t, d, "bob", "beta", "ghe.example.com")
		thirdRepo := insertTestRepoWithHost(t, d, "carol", "gamma", "github.com")
		insertTestMR(t, d, firstRepo, 1, "first", base)
		insertTestMR(t, d, secondRepo, 2, "second", base.Add(time.Hour))
		insertTestMR(t, d, thirdRepo, 3, "third", base.Add(2*time.Hour))

		items, err := d.ListActivity(ctx, ListActivityOpts{
			Repo: "github.com/alice/alpha,ghe.example.com/bob/beta",
			RepoFilters: []RepoFilter{
				{PlatformHost: "github.com", RepoPath: "alice/alpha"},
				{PlatformHost: "ghe.example.com", RepoPath: "bob/beta"},
			},
			Limit: 50,
		})
		require.NoError(err)
		require.Len(items, 2)
		assert.Equal([]string{"bob", "alice"}, []string{
			items[0].RepoOwner,
			items[1].RepoOwner,
		})
	})

	t.Run("type filter", func(t *testing.T) {
		assert := Assert.New(t)
		items, err := d.ListActivity(ctx, ListActivityOpts{
			Types: []string{"new_pr", "new_issue"},
			Limit: 50,
		})
		require.NoError(t, err)
		require.Len(t, items, 3)
		for _, it := range items {
			assert.Contains([]string{"new_pr", "new_issue"}, it.ActivityType)
		}
	})

	t.Run("force push events appear in the activity feed", func(t *testing.T) {
		assert := Assert.New(t)
		d := openTestDB(t)
		ctx := t.Context()
		base := baseTime()
		repoID := insertTestRepo(t, d, "alice", "alpha")
		prID := insertTestMR(t, d, repoID, 1, "Rewrite branch", base)

		err := d.UpsertMREvents(ctx, []MREvent{{
			MergeRequestID: prID,
			EventType:      "force_push",
			Author:         "alice",
			Summary:        "abc1234 -> def5678",
			CreatedAt:      base.Add(5 * time.Minute),
			DedupeKey:      "force-push-abc1234-def5678",
		}})
		require.NoError(t, err)

		items, err := d.ListActivity(ctx, ListActivityOpts{Limit: 50})
		require.NoError(t, err)
		require.NotEmpty(t, items)
		assert.Equal("force_push", items[0].ActivityType)
		assert.Equal("alice", items[0].Author)
		assert.Equal("Rewrite branch", items[0].ItemTitle)
	})

	t.Run("search filter", func(t *testing.T) {
		assert := Assert.New(t)
		items, err := d.ListActivity(ctx, ListActivityOpts{
			Search: "bug", Limit: 50,
		})
		require.NoError(t, err)
		require.NotEmpty(t, items)
		for _, it := range items {
			assert.Equal("Fix bug", it.ItemTitle)
		}
	})

	t.Run("limit and before cursor", func(t *testing.T) {
		assert := Assert.New(t)
		require := require.New(t)
		page1, err := d.ListActivity(
			ctx, ListActivityOpts{Limit: 3})
		require.NoError(err)
		require.Len(page1, 3)

		last := page1[2]
		page2, err := d.ListActivity(ctx, ListActivityOpts{
			Limit:          3,
			BeforeTime:     &last.CreatedAt,
			BeforeSource:   last.Source,
			BeforeSourceID: last.SourceID,
		})
		require.NoError(err)
		require.Len(page2, 3)

		seen := make(map[string]bool)
		for _, it := range page1 {
			key := fmt.Sprintf("%s:%d", it.Source, it.SourceID)
			seen[key] = true
		}
		for _, it := range page2 {
			key := fmt.Sprintf("%s:%d", it.Source, it.SourceID)
			assert.False(seen[key], "duplicate across pages: %s", key)
		}
	})

	t.Run("after cursor for polling", func(t *testing.T) {
		assert := Assert.New(t)
		require := require.New(t)
		all, err := d.ListActivity(
			ctx, ListActivityOpts{Limit: 50})
		require.NoError(err)
		newest := all[0]

		err = d.UpsertMREvents(ctx, []MREvent{
			{MergeRequestID: prID1, EventType: "issue_comment", Author: "grace",
				Body:      "New comment",
				CreatedAt: base.Add(10 * time.Minute),
				DedupeKey: "comment-new"},
		})
		require.NoError(err)

		newItems, err := d.ListActivity(ctx, ListActivityOpts{
			Limit:         50,
			AfterTime:     &newest.CreatedAt,
			AfterSource:   newest.Source,
			AfterSourceID: newest.SourceID,
		})
		require.NoError(err)
		require.Len(newItems, 1)
		assert.Equal("grace", newItems[0].Author)
	})

	t.Run("since time window", func(t *testing.T) {
		assert := Assert.New(t)
		since := base.Add(4 * time.Minute)
		items, err := d.ListActivity(ctx, ListActivityOpts{
			Limit: 50,
			Since: &since,
		})
		require.NoError(t, err)
		for _, it := range items {
			assert.Condition(func() bool {
				return !it.CreatedAt.Before(since)
			}, "item %s:%d has created_at %v before since %v", it.Source, it.SourceID, it.CreatedAt, since)
		}
		// base+4m is review, base+5m is commit, base+7m is issue comment,
		// base+10m is comment-new from after cursor test = 4 items
		assert.Len(items, 4)
	})

	_ = prID2
}

func TestParseDBTime(t *testing.T) {
	assert := Assert.New(t)
	tests := []struct {
		name  string
		input string
		want  time.Time
	}{
		{
			name:  "go time.String format",
			input: "2026-04-09 21:27:11 +0000 UTC",
			want:  time.Date(2026, 4, 9, 21, 27, 11, 0, time.UTC),
		},
		{
			name:  "ISO 8601 UTC",
			input: "2026-04-09T21:27:11Z",
			want:  time.Date(2026, 4, 9, 21, 27, 11, 0, time.UTC),
		},
		{
			name:  "RFC3339 with offset",
			input: "2026-04-09T21:27:11+00:00",
			want:  time.Date(2026, 4, 9, 21, 27, 11, 0, time.UTC),
		},
		{
			name:  "RFC3339Nano",
			input: "2026-04-09T21:27:11.123456Z",
			want:  time.Date(2026, 4, 9, 21, 27, 11, 123456000, time.UTC),
		},
		{
			name:  "local tz with repeated numeric offset",
			input: "2026-04-10 18:48:35 -0400 -0400",
			want:  time.Date(2026, 4, 10, 22, 48, 35, 0, time.UTC),
		},
		{
			name:  "local tz with named zone",
			input: "2026-04-10 18:48:35 -0400 EDT",
			want:  time.Date(2026, 4, 10, 22, 48, 35, 0, time.UTC),
		},
		{
			name:  "bare datetime",
			input: "2026-04-09 21:27:11",
			want:  time.Date(2026, 4, 9, 21, 27, 11, 0, time.UTC),
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseDBTime(tc.input)
			require.NoError(t, err)
			assert.True(tc.want.Equal(got),
				"want %v, got %v", tc.want, got)
		})
	}

	t.Run("parsed values use UTC location", func(t *testing.T) {
		got, err := parseDBTime("2026-04-10 18:48:35 -0400 EDT")
		require.NoError(t, err)
		assert.Equal(time.UTC, got.Location())
		assert.Equal(
			time.Date(2026, 4, 10, 22, 48, 35, 0, time.UTC),
			got,
		)
	})

	t.Run("invalid format returns error", func(t *testing.T) {
		_, err := parseDBTime("not-a-date")
		assert.Error(err)
	})
}

func TestUpsertMREventsRewritesLegacyCreatedAtOnConflict(t *testing.T) {
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	base := baseTime()
	repoID := insertTestRepo(t, d, "alice", "alpha")
	prID := insertTestMR(t, d, repoID, 1, "Rewrite timestamps", base)

	_, err := d.WriteDB().ExecContext(ctx, `
		INSERT INTO middleman_mr_events
		    (merge_request_id, platform_id, event_type, author, summary, body,
		     metadata_json, created_at, dedupe_key)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		prID,
		101,
		"issue_comment",
		"reviewer",
		"",
		"legacy row",
		"",
		"2026-04-11 08:00:00 -0400 EDT",
		"comment-legacy",
	)
	require.NoError(err)

	canonical := time.Date(2026, 4, 11, 12, 0, 0, 0, time.UTC)
	err = d.UpsertMREvents(ctx, []MREvent{{
		MergeRequestID: prID,
		EventType:      "issue_comment",
		Author:         "reviewer",
		Body:           "rewritten",
		CreatedAt:      canonical,
		DedupeKey:      "comment-legacy",
	}})
	require.NoError(err)

	var raw string
	err = d.ReadDB().QueryRowContext(ctx,
		`SELECT created_at FROM middleman_mr_events WHERE merge_request_id = ? AND dedupe_key = ?`,
		prID,
		"comment-legacy",
	).Scan(&raw)
	require.NoError(err)
	require.NotContains(raw, "EDT")
	require.NotContains(raw, "-0400")

	events, err := d.ListMREvents(ctx, prID)
	require.NoError(err)
	require.Len(events, 1)
	require.Equal(canonical, events[0].CreatedAt)
}

func TestUpsertIssueEventsRewritesLegacyCreatedAtOnConflict(t *testing.T) {
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	base := baseTime()
	repoID := insertTestRepo(t, d, "alice", "alpha")
	issueID := insertTestIssue(t, d, repoID, 7, "Rewrite timestamps", base)

	_, err := d.WriteDB().ExecContext(ctx, `
		INSERT INTO middleman_issue_events
		    (issue_id, platform_id, event_type, author, summary, body,
		     metadata_json, created_at, dedupe_key)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		issueID,
		202,
		"issue_comment",
		"reporter",
		"",
		"legacy row",
		"",
		"2026-04-11 09:00:00 -0400 EDT",
		"issue-comment-legacy",
	)
	require.NoError(err)

	canonical := time.Date(2026, 4, 11, 13, 0, 0, 0, time.UTC)
	err = d.UpsertIssueEvents(ctx, []IssueEvent{{
		IssueID:   issueID,
		EventType: "issue_comment",
		Author:    "reporter",
		Body:      "rewritten",
		CreatedAt: canonical,
		DedupeKey: "issue-comment-legacy",
	}})
	require.NoError(err)

	var raw string
	err = d.ReadDB().QueryRowContext(ctx,
		`SELECT created_at FROM middleman_issue_events WHERE issue_id = ? AND dedupe_key = ?`,
		issueID,
		"issue-comment-legacy",
	).Scan(&raw)
	require.NoError(err)
	require.NotContains(raw, "EDT")
	require.NotContains(raw, "-0400")

	events, err := d.ListIssueEvents(ctx, issueID)
	require.NoError(err)
	require.Len(events, 1)
	require.Equal(canonical, events[0].CreatedAt)
}
