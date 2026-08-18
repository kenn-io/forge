package db

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
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
		assert := assert.New(t)
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
		assert.Equal(repoB, items[5].RepoID)
		assert.Equal("github.com", items[5].PlatformHost)
		assert.Equal("bob", items[5].RepoOwner)
		assert.Equal("new_pr", items[6].ActivityType)
		assert.Equal(repoA, items[6].RepoID)
		assert.Equal("alice", items[6].RepoOwner)
	})

	t.Run("repo filter", func(t *testing.T) {
		assert := assert.New(t)
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
		assert := assert.New(t)
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

	t.Run("allowed repo filters apply before the result limit", func(t *testing.T) {
		assert := assert.New(t)
		require := require.New(t)
		d := openTestDB(t)
		ctx := t.Context()
		base := baseTime()

		trackedRepo := insertTestRepo(t, d, "alice", "alpha")
		untrackedRepo := insertTestRepo(t, d, "bob", "beta")
		insertTestMRWithOptions(t, d, testMR(
			trackedRepo,
			1,
			withMRAuthor("Reviewer"),
			withMRActivity(base),
		))
		insertTestMRWithOptions(t, d, testMR(
			untrackedRepo,
			2,
			withMRAuthor("Reviewer"),
			withMRActivity(base.Add(time.Minute)),
		))
		insertTestMRWithOptions(t, d, testMR(
			untrackedRepo,
			3,
			withMRAuthor("Reviewer"),
			withMRActivity(base.Add(2*time.Minute)),
		))

		items, err := d.ListActivity(ctx, ListActivityOpts{
			AllowedRepoIDs: []int64{trackedRepo},
			Author:         "Reviewer",
			Limit:          2,
		})
		require.NoError(err)
		require.Len(items, 1)
		assert.Equal("alice", items[0].RepoOwner)
		assert.Equal("alpha", items[0].RepoName)
		assert.Equal(1, items[0].ItemNumber)
	})

	t.Run("provider qualified repo filter", func(t *testing.T) {
		assert := assert.New(t)
		require := require.New(t)
		d := openTestDB(t)
		ctx := t.Context()
		base := baseTime()

		githubRepo, err := d.UpsertRepo(ctx, RepoIdentity{
			Platform:       "github",
			PlatformHost:   "github.com",
			PlatformRepoID: "github-widgets",
			Owner:          "acme",
			Name:           "widgets",
		})
		require.NoError(err)
		giteaRepo, err := d.UpsertRepo(ctx, RepoIdentity{
			Platform:       "gitea",
			PlatformHost:   "github.com",
			PlatformRepoID: "gitea-widgets",
			Owner:          "acme",
			Name:           "widgets",
		})
		require.NoError(err)
		insertTestMR(t, d, githubRepo, 1, "github provider", base)
		insertTestMR(t, d, giteaRepo, 2, "gitea provider", base.Add(time.Hour))

		items, err := d.ListActivity(ctx, ListActivityOpts{
			Repo: "gitea|github.com/acme/widgets",
			RepoFilters: []RepoFilter{{
				Platform:     "gitea",
				PlatformHost: "github.com",
				RepoPath:     "acme/widgets",
			}},
			Limit: 50,
		})
		require.NoError(err)
		require.Len(items, 1)
		assert.Equal("gitea", items[0].Platform)
		assert.Equal("github.com", items[0].PlatformHost)
		assert.Equal(2, items[0].ItemNumber)
	})

	t.Run("type filter", func(t *testing.T) {
		assert := assert.New(t)
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

	t.Run("item type filter applies before the result limit", func(t *testing.T) {
		assert := assert.New(t)
		require := require.New(t)
		d := openTestDB(t)
		ctx := t.Context()
		base := baseTime()
		repoID := insertTestRepo(t, d, "alice", "alpha")
		prID := insertTestMR(t, d, repoID, 1, "Busy pull request", base)
		require.NoError(d.UpsertMREvents(ctx, []MREvent{{
			MergeRequestID: prID,
			EventType:      "commit",
			Author:         "alice",
			CreatedAt:      base.Add(2 * time.Minute),
			DedupeKey:      "newer-pr-commit",
		}}))
		require.NoError(d.UpsertBranchCommits(ctx, []BranchCommit{
			testBranchCommit(repoID, "main", "repo-sha", "repository commit", base.Add(time.Minute)),
		}))

		items, err := d.ListActivity(ctx, ListActivityOpts{
			Types:     []string{"commit", "default_branch_commit"},
			ItemTypes: []string{"repo"},
			Limit:     1,
		})
		require.NoError(err)
		require.Len(items, 1)
		assert.Equal("default_branch_commit", items[0].ActivityType)
		assert.Equal("repository commit", items[0].BodyPreview)
	})

	t.Run("force push events appear in the activity feed", func(t *testing.T) {
		assert := assert.New(t)
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
		assert := assert.New(t)
		items, err := d.ListActivity(ctx, ListActivityOpts{
			Search: "bug", Limit: 50,
		})
		require.NoError(t, err)
		require.NotEmpty(t, items)
		for _, it := range items {
			assert.Equal("Fix bug", it.ItemTitle)
		}
	})

	t.Run("search matches item numbers", func(t *testing.T) {
		for _, search := range []string{"10", "#10"} {
			t.Run(search, func(t *testing.T) {
				assert := assert.New(t)
				require := require.New(t)
				items, err := d.ListActivity(ctx, ListActivityOpts{
					Search: search, Limit: 50,
				})
				require.NoError(err)
				require.Len(items, 2)
				for _, item := range items {
					assert.Equal("issue", item.ItemType)
					assert.Equal(10, item.ItemNumber)
				}
			})
		}
	})

	t.Run("search matches activity actor and item author", func(t *testing.T) {
		assert := assert.New(t)
		require := require.New(t)
		d := openTestDB(t)
		ctx := t.Context()
		base := baseTime()
		repoID := insertTestRepo(t, d, "example", "activity-authors")
		prID := insertTestMRWithOptions(t, d, testMR(
			repoID,
			1,
			withMRTitle("Refactor cache invalidation"),
			withMRAuthor("item-author-one"),
			withMRActivity(base),
		))
		err := d.UpsertMREvents(ctx, []MREvent{{
			MergeRequestID: prID,
			EventType:      "issue_comment",
			Author:         "commenter-one",
			Body:           "Looks ready",
			CreatedAt:      base.Add(time.Minute),
			DedupeKey:      "comment-author-one",
		}})
		require.NoError(err)

		actorItems, err := d.ListActivity(ctx, ListActivityOpts{
			Search: "COMMENTER-ONE", Limit: 50,
		})
		require.NoError(err)
		require.Len(actorItems, 1)
		assert.Equal("comment", actorItems[0].ActivityType)
		assert.Equal("commenter-one", actorItems[0].Author)

		itemAuthorItems, err := d.ListActivity(ctx, ListActivityOpts{
			Search: "ITEM-AUTHOR-ONE", Limit: 50,
		})
		require.NoError(err)
		require.Len(itemAuthorItems, 2)
		for _, it := range itemAuthorItems {
			assert.Equal("item-author-one", it.ItemAuthor)
		}
	})

	t.Run("author filter matches the PR author instead of child activity actors", func(t *testing.T) {
		assert := assert.New(t)
		require := require.New(t)
		d := openTestDB(t)
		ctx := t.Context()
		base := baseTime()
		repoID := insertTestRepo(t, d, "example", "actor-filter")
		prID := insertTestMRWithOptions(t, d, testMR(
			repoID,
			1,
			withMRAuthor("Alice"),
			withMRActivity(base),
		))
		require.NoError(d.UpsertMREvents(ctx, []MREvent{
			{
				MergeRequestID: prID,
				EventType:      "issue_comment",
				Author:         "Reviewer",
				CreatedAt:      base.Add(2 * time.Minute),
				DedupeKey:      "newer-reviewer-comment",
			},
			{
				MergeRequestID: prID,
				EventType:      "issue_comment",
				Author:         "Another Participant",
				CreatedAt:      base.Add(time.Minute),
				DedupeKey:      "older-participant-comment",
			},
		}))

		items, err := d.ListActivity(ctx, ListActivityOpts{
			Author: "ALICE",
			Limit:  50,
		})
		require.NoError(err)
		require.Len(items, 3)
		for _, item := range items {
			assert.Equal("Alice", item.ItemAuthor)
		}

		commenterItems, err := d.ListActivity(ctx, ListActivityOpts{
			Author: "reviewer",
			Limit:  50,
		})
		require.NoError(err)
		assert.Empty(commenterItems)
	})

	t.Run("limit and before cursor", func(t *testing.T) {
		assert := assert.New(t)
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
		assert := assert.New(t)
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
		assert := assert.New(t)
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

	t.Run("includes branch commits and force pushes with stable cursor order", func(t *testing.T) {
		assert := assert.New(t)
		require := require.New(t)
		d := openTestDB(t)
		ctx := t.Context()
		base := baseTime()
		repoID := insertTestRepo(t, d, "alice", "alpha")
		prID := insertTestMR(t, d, repoID, 1, "Review branch", base.Add(-time.Hour))

		require.NoError(d.UpsertMREvents(ctx, []MREvent{{
			MergeRequestID: prID,
			EventType:      "issue_comment",
			Author:         "reviewer",
			Body:           "same timestamp comment",
			CreatedAt:      base,
			DedupeKey:      "same-time-comment",
		}}))
		require.NoError(d.UpsertBranchCommits(ctx, []BranchCommit{
			{
				RepoID:         repoID,
				BranchName:     "main",
				CommitSHA:      "1111111111111111111111111111111111111111",
				AuthorName:     "Alice",
				AuthorEmail:    "alice@example.com",
				AuthoredAt:     base.Add(-time.Minute),
				CommitterName:  "Alice",
				CommitterEmail: "alice@example.com",
				CommittedAt:    base,
				Subject:        "first branch commit",
			},
			{
				RepoID:         repoID,
				BranchName:     "main",
				CommitSHA:      "2222222222222222222222222222222222222222",
				AuthorName:     "Bob",
				AuthorEmail:    "bob@example.com",
				AuthoredAt:     base.Add(-30 * time.Second),
				CommitterName:  "Bob",
				CommitterEmail: "bob@example.com",
				CommittedAt:    base,
				Subject:        "second branch commit",
			},
		}))
		require.NoError(d.InsertBranchForcePush(ctx, BranchForcePush{
			RepoID:     repoID,
			BranchName: "main",
			BeforeSHA:  "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			AfterSHA:   "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
			DetectedAt: base,
		}))

		items, err := d.ListActivity(ctx, ListActivityOpts{Limit: 10})
		require.NoError(err)
		require.Len(items, 5)
		assert.Equal([]string{"pre", "bfp", "bc", "bc", "pr"}, activitySources(items))
		assert.Equal([]string{
			"comment",
			"default_branch_force_push",
			"default_branch_commit",
			"default_branch_commit",
			"new_pr",
		}, activityTypes(items))
		assert.Greater(items[2].SourceID, items[3].SourceID)
		assert.Equal("second branch commit", items[2].BodyPreview)
		assert.Equal("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa -> bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", items[1].BodyPreview)

		page1, err := d.ListActivity(ctx, ListActivityOpts{Limit: 2})
		require.NoError(err)
		require.Len(page1, 2)
		last := page1[1]
		page2, err := d.ListActivity(ctx, ListActivityOpts{
			Limit:          10,
			BeforeTime:     &last.CreatedAt,
			BeforeSource:   last.Source,
			BeforeSourceID: last.SourceID,
		})
		require.NoError(err)
		require.NotEmpty(page2)

		seen := make(map[string]bool)
		for _, item := range page1 {
			seen[activityKey(item)] = true
		}
		for _, item := range page2 {
			assert.False(seen[activityKey(item)], "duplicate across pages: %s", activityKey(item))
		}
	})

	t.Run("repo filters include branch activity only for matching repos", func(t *testing.T) {
		assert := assert.New(t)
		require := require.New(t)
		d := openTestDB(t)
		ctx := t.Context()
		base := baseTime()
		firstRepo := insertTestRepoWithHost(t, d, "alice", "alpha", "github.com")
		secondRepo := insertTestRepoWithHost(t, d, "bob", "beta", "ghe.example.com")
		require.NoError(d.UpsertBranchCommits(ctx, []BranchCommit{
			testBranchCommit(firstRepo, "main", "alice-sha", "alice branch work", base),
			testBranchCommit(secondRepo, "main", "bob-sha", "bob branch work", base.Add(time.Minute)),
		}))
		require.NoError(d.InsertBranchForcePush(ctx, BranchForcePush{
			RepoID:     secondRepo,
			BranchName: "main",
			BeforeSHA:  "before-bob",
			AfterSHA:   "after-bob",
			DetectedAt: base.Add(2 * time.Minute),
		}))

		items, err := d.ListActivity(ctx, ListActivityOpts{
			Repo: "ghe.example.com/bob/beta",
			RepoFilters: []RepoFilter{{
				PlatformHost: "ghe.example.com",
				RepoPath:     "bob/beta",
			}},
			Limit: 50,
		})
		require.NoError(err)
		require.Len(items, 2)
		for _, item := range items {
			assert.Equal("ghe.example.com", item.PlatformHost)
			assert.Equal("bob", item.RepoOwner)
			assert.Equal("beta", item.RepoName)
		}
	})

	t.Run("time window uses committed and detected timestamps", func(t *testing.T) {
		assert := assert.New(t)
		require := require.New(t)
		d := openTestDB(t)
		ctx := t.Context()
		base := baseTime()
		repoID := insertTestRepo(t, d, "alice", "alpha")
		require.NoError(d.UpsertBranchCommits(ctx, []BranchCommit{
			testBranchCommit(repoID, "main", "old-commit-sha", "old branch commit", base.Add(-time.Hour)),
			testBranchCommit(repoID, "main", "new-commit-sha", "new branch commit", base.Add(time.Hour)),
		}))
		require.NoError(d.InsertBranchForcePush(ctx, BranchForcePush{
			RepoID:     repoID,
			BranchName: "main",
			BeforeSHA:  "old-before",
			AfterSHA:   "old-after",
			DetectedAt: base.Add(-30 * time.Minute),
		}))
		require.NoError(d.InsertBranchForcePush(ctx, BranchForcePush{
			RepoID:     repoID,
			BranchName: "main",
			BeforeSHA:  "new-before",
			AfterSHA:   "new-after",
			DetectedAt: base.Add(30 * time.Minute),
		}))

		since := base
		items, err := d.ListActivity(ctx, ListActivityOpts{Limit: 50, Since: &since})
		require.NoError(err)
		require.Len(items, 2)
		assert.Equal([]string{"default_branch_commit", "default_branch_force_push"}, activityTypes(items))
		assert.Equal([]string{"new branch commit", "new-before -> new-after"}, activityBodies(items))
	})

	t.Run("caps oversized default branch commit metadata in activity projection", func(t *testing.T) {
		assert := assert.New(t)
		require := require.New(t)
		d := openTestDB(t)
		ctx := t.Context()
		base := baseTime()
		repoID := insertTestRepo(t, d, "alice", "alpha")
		require.NoError(insertOversizedBranchCommitRow(ctx, d, repoID, base))

		items, err := d.ListActivity(ctx, ListActivityOpts{Limit: 50})
		require.NoError(err)
		require.Len(items, 1)
		assert.Equal("default_branch_commit", items[0].ActivityType)
		assert.Len(items[0].BodyPreview, 200)
		assert.Len(items[0].Author, branchCommitIdentityMaxBytes)
		assert.Len(items[0].AuthorName, branchCommitIdentityMaxBytes)
		assert.Len(items[0].AuthorEmail, branchCommitIdentityMaxBytes)
		assert.Len(items[0].CommitterName, branchCommitIdentityMaxBytes)
		assert.Len(items[0].CommitterEmail, branchCommitIdentityMaxBytes)
	})

	t.Run("search matches branch commit metadata and sha prefixes", func(t *testing.T) {
		tests := []struct {
			name   string
			search string
		}{
			{name: "subject", search: "metadata subject"},
			{name: "branch", search: "release/v1"},
			{name: "commit sha prefix", search: "abc123"},
			{name: "author name", search: "Commit Author"},
			{name: "author email", search: "author@example.com"},
			{name: "committer name", search: "Committer Person"},
			{name: "committer email", search: "committer@example.com"},
		}
		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				require := require.New(t)
				d := openTestDB(t)
				ctx := t.Context()
				base := baseTime()
				repoID := insertTestRepo(t, d, "alice", "alpha")
				require.NoError(d.UpsertBranchCommits(ctx, []BranchCommit{{
					RepoID:         repoID,
					BranchName:     "release/v1",
					CommitSHA:      "abc123def456abc123def456abc123def456abcd",
					AuthorName:     "Commit Author",
					AuthorEmail:    "author@example.com",
					AuthoredAt:     base.Add(-time.Minute),
					CommitterName:  "Committer Person",
					CommitterEmail: "committer@example.com",
					CommittedAt:    base,
					Subject:        "metadata subject",
				}}))

				items, err := d.ListActivity(ctx, ListActivityOpts{
					Search: tc.search,
					Limit:  50,
				})
				require.NoError(err)
				require.Len(items, 1)
				require.Equal("default_branch_commit", items[0].ActivityType)
			})
		}
	})

	t.Run("search matches branch force push metadata and sha prefixes", func(t *testing.T) {
		tests := []struct {
			name   string
			search string
		}{
			{name: "branch", search: "release/v2"},
			{name: "before sha prefix", search: "before123"},
			{name: "after sha prefix", search: "after456"},
		}
		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				require := require.New(t)
				d := openTestDB(t)
				ctx := t.Context()
				base := baseTime()
				repoID := insertTestRepo(t, d, "alice", "alpha")
				require.NoError(d.InsertBranchForcePush(ctx, BranchForcePush{
					RepoID:     repoID,
					BranchName: "release/v2",
					BeforeSHA:  "before123abcdef",
					AfterSHA:   "after456abcdef",
					DetectedAt: base,
				}))

				items, err := d.ListActivity(ctx, ListActivityOpts{
					Search: tc.search,
					Limit:  50,
				})
				require.NoError(err)
				require.Len(items, 1)
				require.Equal("default_branch_force_push", items[0].ActivityType)
			})
		}
	})

	t.Run("type filter can hide default branch activity", func(t *testing.T) {
		assert := assert.New(t)
		require := require.New(t)
		d := openTestDB(t)
		ctx := t.Context()
		base := baseTime()
		repoID := insertTestRepo(t, d, "alice", "alpha")
		insertTestMR(t, d, repoID, 1, "Fix bug", base.Add(time.Minute))
		require.NoError(d.UpsertBranchCommits(ctx, []BranchCommit{
			testBranchCommit(repoID, "main", "branch-sha", "branch work", base.Add(2*time.Minute)),
		}))
		require.NoError(d.InsertBranchForcePush(ctx, BranchForcePush{
			RepoID:     repoID,
			BranchName: "main",
			BeforeSHA:  "before-sha",
			AfterSHA:   "after-sha",
			DetectedAt: base.Add(3 * time.Minute),
		}))

		items, err := d.ListActivity(ctx, ListActivityOpts{
			Types: []string{"new_pr"},
			Limit: 50,
		})
		require.NoError(err)
		require.Len(items, 1)
		assert.Equal("new_pr", items[0].ActivityType)
	})

	_ = prID2
}

func TestListActivityVisibilityFiltersApplyBeforeLimit(t *testing.T) {
	t.Run("hide closed merged uses notification subject state and keeps unknown state", func(t *testing.T) {
		require := require.New(t)
		assert := assert.New(t)
		d := openTestDB(t)
		ctx := t.Context()
		base := baseTime()
		repoID := insertTestRepo(t, d, "example", "visibility")

		insertTestMRWithOptions(t, d, testMR(repoID, 1,
			withMRState(MergeRequestStateClosed),
			withMRActivity(base.Add(6*time.Minute))))
		insertTestMRWithOptions(t, d, testMR(repoID, 2,
			withMRState(MergeRequestStateMerged),
			withMRActivity(base)))
		insertTestMRWithOptions(t, d, testMR(repoID, 4,
			withMRActivity(base.Add(time.Minute))))

		mergedNumber := 2
		unknownNumber := 3
		openNumber := 4
		require.NoError(d.UpsertNotifications(ctx, []Notification{
			{
				Platform: "github", PlatformHost: "github.com",
				PlatformNotificationID: "ntf-merged-limit",
				RepoOwner:              "example", RepoName: "visibility",
				SubjectType: "PullRequest", SubjectTitle: "Merged notification",
				WebURL:     "https://github.com/example/visibility/pull/2",
				ItemNumber: &mergedNumber, ItemType: "pr", ItemAuthor: "human-author",
				Reason: "mention", Unread: true,
				SourceUpdatedAt: base.Add(5 * time.Minute), SyncedAt: base.Add(5 * time.Minute),
			},
			{
				Platform: "github", PlatformHost: "github.com",
				PlatformNotificationID: "ntf-unknown-limit",
				RepoOwner:              "example", RepoName: "visibility",
				SubjectType: "PullRequest", SubjectTitle: "Unknown notification",
				WebURL:     "https://github.com/example/visibility/pull/3",
				ItemNumber: &unknownNumber, ItemType: "pr", ItemAuthor: "human-author",
				Reason: "mention", Unread: true,
				SourceUpdatedAt: base.Add(4 * time.Minute), SyncedAt: base.Add(4 * time.Minute),
			},
			{
				Platform: "github", PlatformHost: "github.com",
				PlatformNotificationID: "ntf-open-limit",
				RepoOwner:              "example", RepoName: "visibility",
				SubjectType: "PullRequest", SubjectTitle: "Open notification",
				WebURL:     "https://github.com/example/visibility/pull/4",
				ItemNumber: &openNumber, ItemType: "pr", ItemAuthor: "human-author",
				Reason: "mention", Unread: true,
				SourceUpdatedAt: base.Add(3 * time.Minute), SyncedAt: base.Add(3 * time.Minute),
			},
		}))

		items, err := d.ListActivity(ctx, ListActivityOpts{
			HideClosedMerged: true,
			Limit:            2,
		})
		require.NoError(err)
		require.Len(items, 2)
		assert.Equal([]int{3, 4}, []int{items[0].ItemNumber, items[1].ItemNumber})
		assert.Equal([]string{"", "open"}, []string{items[0].SubjectState, items[1].SubjectState})
	})

	t.Run("hide bots tests event actors and parent authors", func(t *testing.T) {
		require := require.New(t)
		assert := assert.New(t)
		d := openTestDB(t)
		ctx := t.Context()
		base := baseTime()
		repoID := insertTestRepo(t, d, "example", "actors")

		botParentID := insertTestMRWithOptions(t, d, testMR(repoID, 1,
			withMRAuthor("dependabot[bot]"),
			withMRActivity(base.Add(4*time.Minute))))
		humanParentWithBotEventID := insertTestMRWithOptions(t, d, testMR(repoID, 2,
			withMRAuthor("human-author"),
			withMRActivity(base.Add(2*time.Minute))))
		insertTestMRWithOptions(t, d, testMR(repoID, 3,
			withMRAuthor("human-author"),
			withMRActivity(base.Add(3*time.Minute))))
		require.NoError(d.UpsertMREvents(ctx, []MREvent{
			{
				MergeRequestID: humanParentWithBotEventID,
				EventType:      "issue_comment", Author: "review-bot",
				CreatedAt: base.Add(6 * time.Minute), DedupeKey: "bot-comment",
			},
			{
				MergeRequestID: botParentID,
				EventType:      "issue_comment", Author: "human-reviewer",
				CreatedAt: base.Add(5 * time.Minute), DedupeKey: "human-comment",
			},
		}))

		items, err := d.ListActivity(ctx, ListActivityOpts{HideBots: true, Limit: 2})
		require.NoError(err)
		require.Len(items, 2)
		assert.Equal([]string{"comment", "new_pr"}, activityTypes(items))
		assert.Equal([]int{1, 3}, []int{items[0].ItemNumber, items[1].ItemNumber})
		assert.Equal("human-reviewer", items[0].Author)
		assert.Equal("human-author", items[1].ItemAuthor)
	})
}

func TestListCollapsedActivityProjection(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	base := baseTime()
	repoID := insertTestRepo(t, d, "alice", "alpha")
	prID := insertTestMR(t, d, repoID, 1, "Fix projection", base)
	require.NoError(d.UpsertMREvents(ctx, []MREvent{{
		MergeRequestID: prID,
		EventType:      "issue_comment",
		Author:         "reviewer",
		Body:           "original comment",
		CreatedAt:      base.Add(time.Minute),
		DedupeKey:      "projection-comment",
	}}))
	unsyncedNumber := 2
	require.NoError(d.UpsertNotifications(ctx, []Notification{{
		Platform:               "github",
		PlatformHost:           "github.com",
		PlatformNotificationID: "projection-unsynced",
		RepoOwner:              "alice",
		RepoName:               "alpha",
		SubjectType:            "PullRequest",
		SubjectTitle:           "Unsynced projection",
		WebURL:                 "https://github.com/alice/alpha/pull/2",
		ItemNumber:             &unsyncedNumber,
		ItemType:               "pr",
		ItemAuthor:             "contributor",
		Reason:                 "mention",
		Unread:                 true,
		SourceUpdatedAt:        base.Add(30 * time.Second),
		SyncedAt:               base.Add(30 * time.Second),
	}}))

	projection, err := d.ListCollapsedActivityProjection(ctx, ListActivityProjectionOpts{
		ListActivityOpts: ListActivityOpts{Limit: 50},
		SubjectLimit:     50,
	})
	require.NoError(err)
	require.Len(projection.DirectRows, 1)
	assert.Equal("notification", projection.DirectRows[0].ActivityType)
	assert.Equal("pr", projection.DirectRows[0].ItemType)
	assert.Equal(2, projection.DirectRows[0].ItemNumber)
	require.Len(projection.Subjects, 1)
	assert.Equal(1, projection.Subjects[0].Subject.Key.ItemNumber)
	assert.Equal(EncodeCursor(base.Add(time.Minute), "pre", 1), projection.EventCursor)
	initialLedgerRevision := projection.Subjects[0].EventLedgerRevision
	assert.NotEmpty(initialLedgerRevision)

	require.NoError(d.UpsertMREvents(ctx, []MREvent{{
		MergeRequestID: prID,
		EventType:      "issue_comment",
		Author:         "late-sync",
		CreatedAt:      base.Add(30 * time.Second),
		DedupeKey:      "projection-backfilled-comment",
	}}))

	refreshed, err := d.ListCollapsedActivityProjection(ctx, ListActivityProjectionOpts{
		ListActivityOpts: ListActivityOpts{Limit: 50},
		SubjectLimit:     50,
	})
	require.NoError(err)
	require.Len(refreshed.Subjects, 1)
	assert.Equal(projection.Subjects[0].ActivityAt, refreshed.Subjects[0].ActivityAt,
		"an older backfill must not change display recency")
	assert.NotEqual(initialLedgerRevision, refreshed.Subjects[0].EventLedgerRevision,
		"the per-parent ledger revision must detect an older backfill")

	backfilledLedgerRevision := refreshed.Subjects[0].EventLedgerRevision
	require.NoError(d.UpsertMREvents(ctx, []MREvent{{
		MergeRequestID: prID,
		EventType:      "issue_comment",
		Author:         "reviewer",
		Body:           "edited comment",
		CreatedAt:      base.Add(time.Minute),
		DedupeKey:      "projection-comment",
	}}))

	edited, err := d.ListCollapsedActivityProjection(ctx, ListActivityProjectionOpts{
		ListActivityOpts: ListActivityOpts{Limit: 50},
		SubjectLimit:     50,
	})
	require.NoError(err)
	require.Len(edited.Subjects, 1)
	assert.Equal(refreshed.Subjects[0].ActivityAt, edited.Subjects[0].ActivityAt,
		"editing an existing event must not change display recency")
	assert.NotEqual(backfilledLedgerRevision, edited.Subjects[0].EventLedgerRevision,
		"the per-parent ledger revision must detect edits to existing events")

	editedLedgerRevision := edited.Subjects[0].EventLedgerRevision
	require.NoError(d.UpsertMREvents(ctx, []MREvent{{
		MergeRequestID: prID,
		EventType:      "issue_comment",
		Author:         "reviewer",
		Body:           "edited comment",
		CreatedAt:      base.Add(time.Minute),
		DedupeKey:      "projection-comment",
	}}))
	unchanged, err := d.ListCollapsedActivityProjection(ctx, ListActivityProjectionOpts{
		ListActivityOpts: ListActivityOpts{Limit: 50},
		SubjectLimit:     50,
	})
	require.NoError(err)
	require.Len(unchanged.Subjects, 1)
	assert.Equal(editedLedgerRevision, unchanged.Subjects[0].EventLedgerRevision,
		"an unchanged event upsert must not invalidate the thread cache")
}

func TestListCollapsedActivityProjectionIncludesParentsRecentOnlyByNotification(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	now := baseTime()
	since := now.Add(-7 * 24 * time.Hour)
	oldActivity := now.Add(-30 * 24 * time.Hour)
	repoID := insertTestRepo(t, d, "alice", "alpha")
	insertTestMR(t, d, repoID, 7, "Old pull request", oldActivity)
	insertTestIssueWithOptions(t, d, testIssue(repoID, 8,
		withIssueTitle("Old issue"),
		withIssueActivity(oldActivity)))
	pullNumber := 7
	issueNumber := 8
	pullNotificationAt := now.Add(-time.Hour)
	issueNotificationAt := now.Add(-2 * time.Hour)
	require.NoError(d.UpsertNotifications(ctx, []Notification{
		{
			Platform: "github", PlatformHost: "github.com",
			PlatformNotificationID: "recent-old-pull",
			RepoOwner:              "alice", RepoName: "alpha",
			SubjectType: "PullRequest", SubjectTitle: "Old pull request",
			WebURL:     "https://github.com/alice/alpha/pull/7",
			ItemNumber: &pullNumber, ItemType: "pr", ItemAuthor: "contributor",
			Reason: "mention", Unread: true,
			SourceUpdatedAt: pullNotificationAt, SyncedAt: pullNotificationAt,
		},
		{
			Platform: "github", PlatformHost: "github.com",
			PlatformNotificationID: "recent-old-issue",
			RepoOwner:              "alice", RepoName: "alpha",
			SubjectType: "Issue", SubjectTitle: "Old issue",
			WebURL:     "https://github.com/alice/alpha/issues/8",
			ItemNumber: &issueNumber, ItemType: "issue", ItemAuthor: "reporter",
			Reason: "subscribed", Unread: true,
			SourceUpdatedAt: issueNotificationAt, SyncedAt: issueNotificationAt,
		},
	}))

	projection, err := d.ListCollapsedActivityProjection(ctx, ListActivityProjectionOpts{
		ListActivityOpts: ListActivityOpts{Since: &since, Limit: 50},
		SubjectLimit:     50,
	})
	require.NoError(err)
	assert.Empty(projection.DirectRows, "anchored notifications must collapse into their parent summaries")
	require.Len(projection.Subjects, 2)
	assert.Equal(7, projection.Subjects[0].Subject.Key.ItemNumber)
	assert.Equal(pullNotificationAt, projection.Subjects[0].ActivityAt)
	assert.Equal(8, projection.Subjects[1].Subject.Key.ItemNumber)
	assert.Equal(issueNotificationAt, projection.Subjects[1].ActivityAt)

	withoutNotifications, err := d.ListCollapsedActivityProjection(ctx, ListActivityProjectionOpts{
		ListActivityOpts: ListActivityOpts{Since: &since, Limit: 50, Types: []string{"comment"}},
		SubjectLimit:     50,
	})
	require.NoError(err)
	assert.Empty(withoutNotifications.DirectRows)
	assert.Empty(withoutNotifications.Subjects,
		"hidden notifications must not pull otherwise-old parents into the window")
}

func TestListCollapsedActivityProjectionDetectsIssueCommentEdit(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	base := baseTime()
	repoID := insertTestRepo(t, d, "alice", "alpha")
	issueID := insertTestIssueWithOptions(t, d, testIssue(repoID, 9,
		withIssueTitle("Fix issue projection"),
		withIssueActivity(base)))
	require.NoError(d.UpsertIssueEvents(ctx, []IssueEvent{{
		IssueID:   issueID,
		EventType: "issue_comment",
		Author:    "reporter",
		Body:      "original issue comment",
		CreatedAt: base.Add(time.Minute),
		DedupeKey: "projection-issue-comment",
	}}))

	projection, err := d.ListCollapsedActivityProjection(ctx, ListActivityProjectionOpts{
		ListActivityOpts: ListActivityOpts{Limit: 50},
		SubjectLimit:     50,
	})
	require.NoError(err)
	require.Len(projection.Subjects, 1)
	initialLedgerRevision := projection.Subjects[0].EventLedgerRevision

	require.NoError(d.UpsertIssueEvents(ctx, []IssueEvent{{
		IssueID:   issueID,
		EventType: "issue_comment",
		Author:    "reporter",
		Body:      "edited issue comment",
		CreatedAt: base.Add(time.Minute),
		DedupeKey: "projection-issue-comment",
	}}))

	edited, err := d.ListCollapsedActivityProjection(ctx, ListActivityProjectionOpts{
		ListActivityOpts: ListActivityOpts{Limit: 50},
		SubjectLimit:     50,
	})
	require.NoError(err)
	require.Len(edited.Subjects, 1)
	assert.Equal(projection.Subjects[0].ActivityAt, edited.Subjects[0].ActivityAt)
	assert.NotEqual(initialLedgerRevision, edited.Subjects[0].EventLedgerRevision,
		"the issue ledger revision must detect edits to existing comments")
}

func TestListCollapsedActivityProjectionDetectsNonNewestNotificationMutation(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	base := baseTime()
	repoID := insertTestRepo(t, d, "alice", "alpha")
	insertTestMR(t, d, repoID, 7, "Fix notification projection", base)
	number := 7
	older := Notification{
		Platform:               "github",
		PlatformHost:           "github.com",
		PlatformNotificationID: "projection-older-notification",
		RepoOwner:              "alice",
		RepoName:               "alpha",
		SubjectType:            "PullRequest",
		SubjectTitle:           "Original notification",
		WebURL:                 "https://github.com/alice/alpha/pull/7",
		ItemNumber:             &number,
		ItemType:               "pr",
		ItemAuthor:             "contributor",
		Reason:                 "mention",
		Unread:                 true,
		SourceUpdatedAt:        base.Add(time.Minute),
		SyncedAt:               base.Add(time.Minute),
	}
	newer := older
	newer.PlatformNotificationID = "projection-newer-notification"
	newer.SubjectTitle = "Newer notification"
	newer.SourceUpdatedAt = base.Add(4 * time.Minute)
	newer.SyncedAt = base.Add(4 * time.Minute)
	require.NoError(d.UpsertNotifications(ctx, []Notification{older, newer}))

	projection, err := d.ListCollapsedActivityProjection(ctx, ListActivityProjectionOpts{
		ListActivityOpts: ListActivityOpts{Limit: 50},
		SubjectLimit:     50,
	})
	require.NoError(err)
	require.Len(projection.Subjects, 1)
	initialLedgerRevision := projection.Subjects[0].EventLedgerRevision

	older.SubjectTitle = "Edited notification"
	older.Reason = "review_requested"
	older.SourceUpdatedAt = base.Add(2 * time.Minute)
	older.SyncedAt = base.Add(5 * time.Minute)
	require.NoError(d.UpsertNotifications(ctx, []Notification{older}))

	edited, err := d.ListCollapsedActivityProjection(ctx, ListActivityProjectionOpts{
		ListActivityOpts: ListActivityOpts{Limit: 50},
		SubjectLimit:     50,
	})
	require.NoError(err)
	require.Len(edited.Subjects, 1)
	assert.NotEqual(initialLedgerRevision, edited.Subjects[0].EventLedgerRevision,
		"the parent revision must detect mutation of a non-newest notification")
}

func insertOversizedBranchCommitRow(
	ctx context.Context,
	d *DB,
	repoID int64,
	committedAt time.Time,
) error {
	_, err := d.rw.ExecContext(ctx, `
		INSERT INTO forge_branch_commits (
		    repo_id, branch_name, commit_sha, author_name, author_email,
		    authored_at, committer_name, committer_email, committed_at,
		    subject
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		repoID,
		"main",
		"oversized-branch-sha",
		strings.Repeat("a", branchCommitIdentityMaxBytes+20),
		strings.Repeat("e", branchCommitIdentityMaxBytes+20),
		committedAt.Add(-time.Minute),
		strings.Repeat("c", branchCommitIdentityMaxBytes+20),
		strings.Repeat("m", branchCommitIdentityMaxBytes+20),
		committedAt,
		strings.Repeat("s", branchCommitSubjectMaxBytes+20),
	)
	return err
}

func TestListActivityItemAuthor(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	base := baseTime()

	repoID := insertTestRepo(t, d, "alice", "alpha")
	prID := insertTestMRWithOptions(t, d, testMR(repoID, 1,
		withMRTitle("Fix bug"), withMRActivity(base),
		withMRAuthor("pr-author")))
	issueID := insertTestIssueWithOptions(t, d, testIssue(repoID, 10,
		withIssueTitle("Crash on startup"),
		withIssueActivity(base.Add(time.Minute)),
		withIssueAuthor("issue-author")))

	require.NoError(d.UpsertMREvents(ctx, []MREvent{{
		MergeRequestID: prID,
		EventType:      "issue_comment",
		Author:         "pr-commenter",
		Body:           "looks good",
		CreatedAt:      base.Add(2 * time.Minute),
		DedupeKey:      "pr-comment-1",
	}}))
	require.NoError(d.UpsertIssueEvents(ctx, []IssueEvent{{
		IssueID:   issueID,
		EventType: "issue_comment",
		Author:    "issue-commenter",
		Body:      "me too",
		CreatedAt: base.Add(3 * time.Minute),
		DedupeKey: "issue-comment-1",
	}}))
	require.NoError(d.UpsertBranchCommits(ctx, []BranchCommit{
		testBranchCommit(repoID, "main",
			"1111111111111111111111111111111111111111",
			"branch commit", base.Add(4*time.Minute)),
	}))

	items, err := d.ListActivity(ctx, ListActivityOpts{Limit: 50})
	require.NoError(err)

	// Source uniquely identifies each seeded row: pr=new_pr, issue=new_issue,
	// pre=PR event, ise=issue event, bc=branch commit.
	bySource := make(map[string]ActivityItem, len(items))
	for _, it := range items {
		bySource[it.Source] = it
	}

	prComment := bySource["pre"]
	assert.Equal("comment", prComment.ActivityType)
	assert.Equal("pr-commenter", prComment.Author)
	assert.Equal("pr-author", prComment.ItemAuthor)

	newPR := bySource["pr"]
	assert.Equal("new_pr", newPR.ActivityType)
	assert.Equal("pr-author", newPR.ItemAuthor)

	issueComment := bySource["ise"]
	assert.Equal("comment", issueComment.ActivityType)
	assert.Equal("issue-commenter", issueComment.Author)
	assert.Equal("issue-author", issueComment.ItemAuthor)

	newIssue := bySource["issue"]
	assert.Equal("new_issue", newIssue.ActivityType)
	assert.Equal("issue-author", newIssue.ItemAuthor)

	branchCommit := bySource["bc"]
	assert.Equal("default_branch_commit", branchCommit.ActivityType)
	assert.Empty(branchCommit.ItemAuthor)
}

func TestListActivityCarriesParentRecencyWhenNewerEventsAreFiltered(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	base := baseTime()

	repoID := insertTestRepo(t, d, "alice", "alpha")
	// The filtered commit is the newest ledger event and must still define
	// the parent's recency on the visible comment row.
	prActivityAt := base.Add(19 * time.Minute)
	prID := insertTestMRWithOptions(t, d, testMR(repoID, 1, withMRActivity(base)))
	issueActivityAt := base.Add(11 * time.Minute)
	issueID := insertTestIssueWithOptions(t, d, testIssue(repoID, 2, withIssueActivity(base)))

	require.NoError(d.UpsertMREvents(ctx, []MREvent{
		{
			MergeRequestID: prID,
			EventType:      "issue_comment",
			Author:         "reviewer",
			CreatedAt:      base.Add(10 * time.Minute),
			DedupeKey:      "visible-comment",
		},
		{
			MergeRequestID: prID,
			EventType:      "commit",
			Author:         "alice",
			CreatedAt:      base.Add(19 * time.Minute),
			DedupeKey:      "filtered-commit",
		},
	}))
	require.NoError(d.UpsertIssueEvents(ctx, []IssueEvent{{
		IssueID:   issueID,
		EventType: "issue_comment",
		Author:    "reporter",
		CreatedAt: base.Add(11 * time.Minute),
		DedupeKey: "issue-comment",
	}}))

	items, err := d.ListActivity(ctx, ListActivityOpts{
		Types: []string{"comment"},
		Limit: 50,
	})
	require.NoError(err)
	require.Len(items, 2)

	byType := make(map[string]ActivityItem, len(items))
	for _, item := range items {
		byType[item.ItemType] = item
	}
	require.NotNil(byType["pr"].ItemLastActivityAt)
	assert.Equal(prActivityAt, *byType["pr"].ItemLastActivityAt)
	require.NotNil(byType["issue"].ItemLastActivityAt)
	assert.Equal(issueActivityAt, *byType["issue"].ItemLastActivityAt)
	assert.NotContains(activityTypes(items), "commit")
}

func TestListActivitySubjectsUsesAuthoritativeRecencyForWindowAndLimit(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	now := baseTime()
	since := now.Add(-7 * 24 * time.Hour)

	repoID := insertTestRepo(t, d, "alice", "alpha")
	oldCreatedAt := now.Add(-30 * 24 * time.Hour)
	recentActivityAt := now.Add(-time.Hour)
	recentParent := testMR(repoID, 1,
		withMRTitle("Old pull with recent hidden activity"),
		withMRActivity(oldCreatedAt),
	)
	recentParent.UpdatedAt = recentActivityAt
	recentParent.LastActivityAt = recentActivityAt
	recentParentID := insertTestMRWithOptions(t, d, recentParent)
	require.NoError(d.UpsertMREvents(ctx, []MREvent{{
		MergeRequestID: recentParentID,
		EventType:      "issue_comment",
		Author:         "reviewer",
		CreatedAt:      recentActivityAt,
		DedupeKey:      "recent-hidden-comment",
	}}))

	olderActivityAt := now.Add(-2 * time.Hour)
	insertTestMRWithOptions(t, d, testMR(repoID, 2,
		withMRTitle("Newer pull with older activity"),
		withMRActivity(olderActivityAt),
	))

	visibleEvents, err := d.ListActivity(ctx, ListActivityOpts{
		Types:     []string{"commit"},
		ItemTypes: []string{"pr", "repo"},
		Since:     &since,
		Limit:     1,
	})
	require.NoError(err)
	assert.Empty(visibleEvents, "the comment is hidden by the event filter")

	subjects, err := d.ListActivitySubjects(ctx, ListActivitySubjectsOpts{
		ItemTypes: []string{"pr", "repo"},
		Since:     &since,
		Limit:     1,
	})
	require.NoError(err)
	require.Len(subjects, 1)
	assert.Equal(1, subjects[0].Subject.Key.ItemNumber)
	assert.Equal("pr", subjects[0].Subject.Key.ItemType)
	assert.Equal("Old pull with recent hidden activity", subjects[0].Subject.Title)
	assert.Equal(recentActivityAt, subjects[0].ActivityAt)
}

func TestListActivitySubjectsIncludesParentsWhoseEventsMatchSearch(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	now := baseTime()
	repoID := insertTestRepo(t, d, "alice", "alpha")

	insertTestMRWithOptions(t, d, testMR(repoID, 1,
		withMRTitle("Unrelated parent matched through an event"),
		withMRActivity(now.Add(-time.Hour)),
	))
	insertTestMRWithOptions(t, d, testMR(repoID, 2,
		withMRTitle("Needle in the parent title"),
		withMRActivity(now.Add(-2*time.Hour)),
	))
	insertTestMRWithOptions(t, d, testMR(repoID, 3,
		withMRTitle("Unrelated parent without a matching event"),
		withMRActivity(now.Add(-3*time.Hour)),
	))

	subjects, err := d.ListActivitySubjects(ctx, ListActivitySubjectsOpts{
		Search: "needle",
		SearchMatchedSubjectKeys: []WorkspaceSubjectKey{
			{RepoID: repoID, ItemType: "pr", ItemNumber: 1},
		},
		Limit: 50,
	})
	require.NoError(err)
	require.Len(subjects, 2)
	assert.ElementsMatch([]int{1, 2}, []int{
		subjects[0].Subject.Key.ItemNumber,
		subjects[1].Subject.Key.ItemNumber,
	})
}

func testBranchCommit(
	repoID int64,
	branch string,
	sha string,
	subject string,
	committedAt time.Time,
) BranchCommit {
	return BranchCommit{
		RepoID:         repoID,
		BranchName:     branch,
		CommitSHA:      sha,
		AuthorName:     "Test Author",
		AuthorEmail:    "author@example.com",
		AuthoredAt:     committedAt.Add(-time.Minute),
		CommitterName:  "Test Committer",
		CommitterEmail: "committer@example.com",
		CommittedAt:    committedAt,
		Subject:        subject,
	}
}

func activityKey(item ActivityItem) string {
	return fmt.Sprintf("%s:%d", item.Source, item.SourceID)
}

func activitySources(items []ActivityItem) []string {
	sources := make([]string, len(items))
	for i, item := range items {
		sources[i] = item.Source
	}
	return sources
}

func activityTypes(items []ActivityItem) []string {
	types := make([]string, len(items))
	for i, item := range items {
		types[i] = item.ActivityType
	}
	return types
}

func activityBodies(items []ActivityItem) []string {
	bodies := make([]string, len(items))
	for i, item := range items {
		bodies[i] = item.BodyPreview
	}
	return bodies
}

func TestParseDBTime(t *testing.T) {
	assert := assert.New(t)
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
		INSERT INTO forge_mr_events
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
		`SELECT created_at FROM forge_mr_events WHERE merge_request_id = ? AND dedupe_key = ?`,
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

func TestUpsertMREventsPreservesDirectURLWhenPartialRefreshOmitsIt(t *testing.T) {
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	base := baseTime()
	repoID := insertTestRepo(t, d, "alice", "alpha")
	prID := insertTestMR(t, d, repoID, 1, "Preserve direct URL", base)

	err := d.UpsertMREvents(ctx, []MREvent{{
		MergeRequestID: prID,
		PlatformID:     new(int64(101)),
		EventType:      "issue_comment",
		Author:         "reviewer",
		Body:           "first",
		CreatedAt:      base,
		DedupeKey:      "comment-direct-url",
		DirectURL:      "https://github.com/alice/alpha/pull/1#issuecomment-101",
	}})
	require.NoError(err)

	err = d.UpsertMREvents(ctx, []MREvent{{
		MergeRequestID: prID,
		PlatformID:     new(int64(101)),
		EventType:      "issue_comment",
		Author:         "reviewer",
		Body:           "edited",
		CreatedAt:      base.Add(time.Minute),
		DedupeKey:      "comment-direct-url",
	}})
	require.NoError(err)

	events, err := d.ListMREvents(ctx, prID)
	require.NoError(err)
	require.Len(events, 1)
	require.Equal("edited", events[0].Body)
	require.Equal("https://github.com/alice/alpha/pull/1#issuecomment-101", events[0].DirectURL)
}

func TestUpsertIssueEventsRewritesLegacyCreatedAtOnConflict(t *testing.T) {
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	base := baseTime()
	repoID := insertTestRepo(t, d, "alice", "alpha")
	issueID := insertTestIssue(t, d, repoID, 7, "Rewrite timestamps", base)

	_, err := d.WriteDB().ExecContext(ctx, `
		INSERT INTO forge_issue_events
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
		`SELECT created_at FROM forge_issue_events WHERE issue_id = ? AND dedupe_key = ?`,
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

func TestUpsertIssueEventsPreservesDirectURLWhenPartialRefreshOmitsIt(t *testing.T) {
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	base := baseTime()
	repoID := insertTestRepo(t, d, "alice", "alpha")
	issueID := insertTestIssue(t, d, repoID, 7, "Preserve issue direct URL", base)

	err := d.UpsertIssueEvents(ctx, []IssueEvent{{
		IssueID:    issueID,
		PlatformID: new(int64(202)),
		EventType:  "issue_comment",
		Author:     "reporter",
		Body:       "first",
		CreatedAt:  base,
		DedupeKey:  "issue-comment-direct-url",
		DirectURL:  "https://github.com/alice/alpha/issues/7#issuecomment-202",
	}})
	require.NoError(err)

	err = d.UpsertIssueEvents(ctx, []IssueEvent{{
		IssueID:    issueID,
		PlatformID: new(int64(202)),
		EventType:  "issue_comment",
		Author:     "reporter",
		Body:       "edited",
		CreatedAt:  base.Add(time.Minute),
		DedupeKey:  "issue-comment-direct-url",
	}})
	require.NoError(err)

	events, err := d.ListIssueEvents(ctx, issueID)
	require.NoError(err)
	require.Len(events, 1)
	require.Equal("edited", events[0].Body)
	require.Equal("https://github.com/alice/alpha/issues/7#issuecomment-202", events[0].DirectURL)
}

func TestListActivityIncludesNotifications(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	base := baseTime()

	repoID := insertTestRepo(t, d, "alice", "alpha")
	_ = repoID
	number := 7
	err := d.UpsertNotifications(ctx, []Notification{{
		Platform:               "github",
		PlatformHost:           "github.com",
		PlatformNotificationID: "ntf-1",
		RepoOwner:              "alice",
		RepoName:               "alpha",
		SubjectType:            "PullRequest",
		SubjectTitle:           "Review my change",
		WebURL:                 "https://github.com/alice/alpha/pull/7",
		ItemNumber:             &number,
		ItemType:               "pr",
		ItemAuthor:             "carol",
		Reason:                 "review_requested",
		Unread:                 true,
		SourceUpdatedAt:        base.Add(10 * time.Minute),
		SyncedAt:               base.Add(10 * time.Minute),
	}})
	require.NoError(err)

	items, err := d.ListActivity(ctx, ListActivityOpts{Limit: 50})
	require.NoError(err)

	var notif *ActivityItem
	for i := range items {
		if items[i].ActivityType == "notification" {
			notif = &items[i]
			break
		}
	}
	require.NotNil(notif, "notification should appear in the activity feed")
	assert.Equal("ntf", notif.Source)
	assert.Equal("alice", notif.RepoOwner)
	assert.Equal("alpha", notif.RepoName)
	assert.Equal("pr", notif.ItemType)
	assert.Equal(7, notif.ItemNumber)
	assert.Equal("Review my change", notif.ItemTitle)
	assert.Equal("review_requested", notif.BodyPreview)
	assert.Equal("unread", notif.ItemState)
	assert.Equal("https://github.com/alice/alpha/pull/7", notif.ActivityURL)

	// The notification type participates in the type filter like any
	// other activity source.
	filtered, err := d.ListActivity(ctx, ListActivityOpts{Limit: 50, Types: []string{"notification"}})
	require.NoError(err)
	require.Len(filtered, 1)
	assert.Equal("notification", filtered[0].ActivityType)

	// ExcludeNotifications drops them from the union entirely (before
	// the limit), so a disabled feed never lists notification rows.
	excluded, err := d.ListActivity(ctx, ListActivityOpts{Limit: 50, ExcludeNotifications: true})
	require.NoError(err)
	for _, it := range excluded {
		assert.NotEqual("notification", it.ActivityType)
	}
}

func TestListActivityAuthors(t *testing.T) {
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	base := baseTime()
	alphaID := insertTestRepo(t, d, "alice", "alpha")
	betaID := insertTestRepo(t, d, "bob", "beta")

	alphaPRID := insertTestMRWithOptions(t, d, testMR(
		alphaID,
		1,
		withMRAuthor("Alice"),
		withMRActivity(base),
	))
	insertTestIssueWithOptions(t, d, testIssue(
		alphaID,
		2,
		withIssueAuthor("bob"),
		withIssueActivity(base.Add(2*time.Minute)),
	))
	insertTestIssueWithOptions(t, d, testIssue(
		alphaID,
		3,
		withIssueAuthor("Old Actor"),
		withIssueActivity(base.Add(-time.Hour)),
	))
	insertTestMRWithOptions(t, d, testMR(
		betaID,
		1,
		withMRAuthor("Excluded Actor"),
		withMRActivity(base.Add(10*time.Minute)),
	))
	require.NoError(d.UpsertMREvents(ctx, []MREvent{
		{
			MergeRequestID: alphaPRID,
			EventType:      "issue_comment",
			Author:         "Commenter",
			CreatedAt:      base.Add(3 * time.Minute),
			DedupeKey:      "recent-alice-casing",
		},
		{
			MergeRequestID: alphaPRID,
			EventType:      "issue_comment",
			Author:         "",
			CreatedAt:      base.Add(4 * time.Minute),
			DedupeKey:      "empty-actor",
		},
	}))
	commit := testBranchCommit(
		alphaID,
		"main",
		"activity-author-sha",
		"candidate author",
		base.Add(4*time.Minute),
	)
	commit.AuthorName = "Carol"
	require.NoError(d.UpsertBranchCommits(ctx, []BranchCommit{commit}))

	number := 99
	require.NoError(d.UpsertNotifications(ctx, []Notification{
		{
			Platform:               "github",
			PlatformHost:           "github.com",
			PlatformNotificationID: "activity-author-notification",
			RepoOwner:              "alice",
			RepoName:               "alpha",
			SubjectType:            "PullRequest",
			SubjectTitle:           "Review requested",
			WebURL:                 "https://github.com/alice/alpha/pull/99",
			ItemNumber:             &number,
			ItemType:               "pr",
			ItemAuthor:             "Dana",
			Reason:                 "review_requested",
			SourceUpdatedAt:        base.Add(5 * time.Minute),
			SyncedAt:               base.Add(5 * time.Minute),
		},
		{
			Platform:               "github",
			PlatformHost:           "github.com",
			PlatformNotificationID: "activity-author-self-notification",
			RepoOwner:              "alice",
			RepoName:               "alpha",
			SubjectType:            "PullRequest",
			SubjectTitle:           "Own thread",
			WebURL:                 "https://github.com/alice/alpha/pull/99",
			ItemNumber:             &number,
			ItemType:               "pr",
			ItemAuthor:             "Self Actor",
			Reason:                 "author",
			SourceUpdatedAt:        base.Add(6 * time.Minute),
			SyncedAt:               base.Add(6 * time.Minute),
		},
	}))

	repoFilter := RepoFilter{
		Platform:     "github",
		PlatformHost: "github.com",
		RepoOwner:    "alice",
		RepoName:     "alpha",
	}
	since := base.Add(-time.Minute)
	authors, err := d.ListActivityAuthors(ctx, ListActivityAuthorsOpts{
		RepoFilters:    []RepoFilter{repoFilter},
		AllowedRepoIDs: []int64{alphaID},
		NotificationRepoFilters: []NotificationRepoFilter{{
			Platform:     "github",
			PlatformHost: "github.com",
			RepoOwner:    "alice",
			RepoName:     "alpha",
		}},
		Since: &since,
	})
	require.NoError(err)
	require.Equal([]string{"Dana", "Alice", "bob"}, authors)

	authors, err = d.ListActivityAuthors(ctx, ListActivityAuthorsOpts{
		RepoFilters:    []RepoFilter{repoFilter},
		AllowedRepoIDs: []int64{betaID},
		Since:          &since,
	})
	require.NoError(err)
	require.Empty(authors, "explicit repo scope must intersect the allowed repo scope")
}

func TestListActivityNotificationCarriesSubjectState(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	base := baseTime()

	repoID := insertTestRepo(t, d, "alice", "alpha")
	number := 7
	// The notification's linked PR is merged.
	insertTestMRWithOptions(t, d, testMR(repoID, number,
		withMRTitle("Merged change"),
		withMRState(MergeRequestStateMerged),
		withMRActivity(base)))
	require.NoError(d.UpsertNotifications(ctx, []Notification{{
		Platform:               "github",
		PlatformHost:           "github.com",
		PlatformNotificationID: "ntf-merged",
		RepoOwner:              "alice",
		RepoName:               "alpha",
		SubjectType:            "PullRequest",
		SubjectTitle:           "Merged change",
		WebURL:                 "https://github.com/alice/alpha/pull/7",
		ItemNumber:             &number,
		ItemType:               "pr",
		ItemAuthor:             "carol",
		Reason:                 "review_requested",
		Unread:                 true,
		SourceUpdatedAt:        base.Add(10 * time.Minute),
		SyncedAt:               base.Add(10 * time.Minute),
	}}))

	items, err := d.ListActivity(ctx, ListActivityOpts{Limit: 50, Types: []string{"notification"}})
	require.NoError(err)
	require.Len(items, 1)
	// item_state still carries the notification's unread/read state; the
	// linked PR's merged state rides in subject_state so the feed can hide
	// closed/merged notifications even with no PR row present.
	assert.Equal("unread", items[0].ItemState)
	assert.Equal("merged", items[0].SubjectState)
}

func TestListActivityNotificationMatchesRepoByIdentity(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	base := baseTime()
	number := 7

	require.NoError(d.UpsertNotifications(ctx, []Notification{{
		Platform:               "github",
		PlatformHost:           "github.com",
		PlatformNotificationID: "ntf-before-repo",
		RepoOwner:              "alice",
		RepoName:               "alpha",
		SubjectType:            "PullRequest",
		SubjectTitle:           "Matched after repo sync",
		WebURL:                 "https://github.com/alice/alpha/pull/7",
		ItemNumber:             &number,
		ItemType:               "pr",
		ItemAuthor:             "carol",
		Reason:                 "mention",
		Unread:                 true,
		SourceUpdatedAt:        base.Add(10 * time.Minute),
		SyncedAt:               base.Add(10 * time.Minute),
	}}))

	repoID := insertTestRepo(t, d, "alice", "alpha")
	insertTestMRWithOptions(t, d, testMR(repoID, number,
		withMRTitle("Matched after repo sync"),
		withMRState(MergeRequestStateMerged),
		withMRActivity(base)))

	items, err := d.ListActivity(ctx, ListActivityOpts{Limit: 50, Types: []string{"notification"}})
	require.NoError(err)
	require.Len(items, 1)
	assert.Equal("notification", items[0].ActivityType)
	assert.Equal("alice", items[0].RepoOwner)
	assert.Equal("alpha", items[0].RepoName)
	assert.Equal("merged", items[0].SubjectState)
}

func TestListActivityNotificationRepoFiltersApplyBeforeUnionLimit(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	base := baseTime()
	number := 7

	trackedRepoID := insertTestRepo(t, d, "alice", "alpha")
	removedRepoID := insertTestRepo(t, d, "alice", "removed")
	insertTestMRWithOptions(t, d, testMR(trackedRepoID, number,
		withMRTitle("Tracked notification"),
		withMRActivity(base)))
	insertTestMRWithOptions(t, d, testMR(removedRepoID, number,
		withMRTitle("Removed notification"),
		withMRActivity(base)))
	require.NoError(d.UpsertNotifications(ctx, []Notification{
		{
			Platform:               "github",
			PlatformHost:           "github.com",
			PlatformNotificationID: "ntf-tracked",
			RepoOwner:              "alice",
			RepoName:               "alpha",
			SubjectType:            "PullRequest",
			SubjectTitle:           "Tracked notification",
			WebURL:                 "https://github.com/alice/alpha/pull/7",
			ItemNumber:             &number,
			ItemType:               "pr",
			ItemAuthor:             "carol",
			Reason:                 "mention",
			Unread:                 true,
			SourceUpdatedAt:        base.Add(10 * time.Minute),
			SyncedAt:               base.Add(10 * time.Minute),
		},
		{
			Platform:               "github",
			PlatformHost:           "github.com",
			PlatformNotificationID: "ntf-removed",
			RepoOwner:              "alice",
			RepoName:               "removed",
			SubjectType:            "PullRequest",
			SubjectTitle:           "Removed notification",
			WebURL:                 "https://github.com/alice/removed/pull/7",
			ItemNumber:             &number,
			ItemType:               "pr",
			ItemAuthor:             "carol",
			Reason:                 "mention",
			Unread:                 true,
			SourceUpdatedAt:        base.Add(11 * time.Minute),
			SyncedAt:               base.Add(11 * time.Minute),
		},
	}))

	items, err := d.ListActivity(ctx, ListActivityOpts{
		Limit: 50,
		Types: []string{"notification"},
		NotificationRepoFilters: []NotificationRepoFilter{{
			Platform:     "github",
			PlatformHost: "github.com",
			RepoOwner:    "alice",
			RepoName:     "alpha",
		}},
	})
	require.NoError(err)
	require.Len(items, 1)
	assert.Equal("notification", items[0].ActivityType)
	assert.Equal("alice", items[0].RepoOwner)
	assert.Equal("alpha", items[0].RepoName)
	assert.Equal("Tracked notification", items[0].ItemTitle)

	none, err := d.ListActivity(ctx, ListActivityOpts{
		Limit:                   50,
		Types:                   []string{"notification"},
		NotificationRepoFilters: []NotificationRepoFilter{{}},
	})
	require.NoError(err)
	assert.Empty(none)
}

func TestListActivityNotificationRepoFilterFollowsRename(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	base := baseTime()
	number := 7

	_, _, err := d.ReconcileRepositoryObservation(ctx, RepoIdentity{
		Platform: "github", PlatformHost: "github.com",
		PlatformRepoID: "R_widget", Owner: "acme", Name: "widget",
	}, base)
	require.NoError(err)
	require.NoError(d.UpsertNotifications(ctx, []Notification{{
		Platform:               "github",
		PlatformHost:           "github.com",
		PlatformNotificationID: "ntf-renamed",
		RepoOwner:              "acme",
		RepoName:               "widget",
		SubjectType:            "PullRequest",
		SubjectTitle:           "Renamed notification",
		WebURL:                 "https://github.com/acme/widget/pull/7",
		ItemNumber:             &number,
		ItemType:               "pr",
		ItemAuthor:             "carol",
		Reason:                 "mention",
		Unread:                 true,
		SourceUpdatedAt:        base.Add(10 * time.Minute),
		SyncedAt:               base.Add(10 * time.Minute),
	}}))
	_, _, err = d.ReconcileRepositoryObservation(ctx, RepoIdentity{
		Platform: "github", PlatformHost: "github.com",
		PlatformRepoID: "R_widget", Owner: "acme", Name: "gadget",
	}, base.Add(time.Hour))
	require.NoError(err)

	renamed, err := d.ListActivity(ctx, ListActivityOpts{
		Limit: 50,
		Types: []string{"notification"},
		NotificationRepoFilters: []NotificationRepoFilter{{
			Platform:     "github",
			PlatformHost: "github.com",
			RepoOwner:    "acme",
			RepoName:     "gadget",
		}},
	})
	require.NoError(err)
	require.Len(renamed, 1,
		"linked notification must be filterable by the current route")
	assert.Equal("gadget", renamed[0].RepoName)

	stale, err := d.ListActivity(ctx, ListActivityOpts{
		Limit: 50,
		Types: []string{"notification"},
		NotificationRepoFilters: []NotificationRepoFilter{{
			Platform:     "github",
			PlatformHost: "github.com",
			RepoOwner:    "acme",
			RepoName:     "widget",
		}},
	})
	require.NoError(err)
	assert.Empty(stale,
		"linked notifications must not answer to historical routes")
}

func TestListActivityNotificationUsesLinkedParentMetadata(t *testing.T) {
	tests := []struct {
		name         string
		itemType     string
		staleURL     string
		currentURL   string
		insertParent func(t *testing.T, d *DB, repoID int64, number int, url string)
	}{
		{
			name:       "pull request",
			itemType:   "pr",
			staleURL:   "https://github.com/acme/widget/pull/7",
			currentURL: "https://github.com/acme/gadget/pull/7",
			insertParent: func(t *testing.T, d *DB, repoID int64, number int, url string) {
				mr := testMR(repoID, number, withMRTitle("Current parent title"))
				mr.URL = url
				insertTestMRWithOptions(t, d, mr)
			},
		},
		{
			name:       "issue",
			itemType:   "issue",
			staleURL:   "https://github.com/acme/widget/issues/7",
			currentURL: "https://github.com/acme/gadget/issues/7",
			insertParent: func(t *testing.T, d *DB, repoID int64, number int, url string) {
				issue := testIssue(repoID, number, withIssueTitle("Current parent title"))
				issue.URL = url
				insertTestIssueWithOptions(t, d, issue)
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			assert := assert.New(t)
			d := openTestDB(t)
			ctx := t.Context()
			base := baseTime()
			number := 7

			entry, _, err := d.ReconcileRepositoryObservation(ctx, RepoIdentity{
				Platform: "github", PlatformHost: "github.com",
				PlatformRepoID: "R_widget", Owner: "acme", Name: "widget",
			}, base)
			require.NoError(err)
			require.NoError(d.UpsertNotifications(ctx, []Notification{{
				Platform:               "github",
				PlatformHost:           "github.com",
				PlatformNotificationID: "ntf-stale-parent",
				RepoOwner:              "acme",
				RepoName:               "widget",
				SubjectType:            "PullRequest",
				SubjectTitle:           "Title at notification time",
				WebURL:                 tc.staleURL,
				ItemNumber:             &number,
				ItemType:               tc.itemType,
				ItemAuthor:             "carol",
				Reason:                 "mention",
				Unread:                 true,
				SourceUpdatedAt:        base.Add(10 * time.Minute),
				SyncedAt:               base.Add(10 * time.Minute),
			}}))
			_, _, err = d.ReconcileRepositoryObservation(ctx, RepoIdentity{
				Platform: "github", PlatformHost: "github.com",
				PlatformRepoID: "R_widget", Owner: "acme", Name: "gadget",
			}, base.Add(time.Hour))
			require.NoError(err)
			tc.insertParent(t, d, entry.Repository.ID, number, tc.currentURL)

			items, err := d.ListActivity(ctx, ListActivityOpts{Limit: 50, Types: []string{"notification"}})
			require.NoError(err)
			require.Len(items, 1)
			// The persisted notification keeps the route and title from when
			// it was synced. The feed row must report the linked parent's
			// current metadata so a renamed or reused route never leaks
			// through the notification, whether or not the frontend later
			// reconciles it against the capped parent snapshot.
			assert.Equal("gadget", items[0].RepoName)
			assert.Equal("Current parent title", items[0].ItemTitle)
			assert.Equal(tc.currentURL, items[0].ItemURL)
			assert.Equal(tc.currentURL, items[0].ActivityURL)
			assert.Equal("unread", items[0].ItemState)
			assert.Equal("open", items[0].SubjectState)
		})
	}
}

func TestActivityRecencyDerivesFromRenderedEventLedger(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	now := baseTime()
	since := now.Add(-7 * 24 * time.Hour)
	repoID := insertTestRepo(t, d, "acme", "widget")

	// The provider bumped updated_at 16 minutes ago (mergeability recompute
	// after a base push), but the last visible timeline entry is a comment
	// from 14 hours ago. A cross reference an hour ago is in the ledger but
	// never renders in the feed, so it must not count either.
	stackedPR := testMR(repoID, 1347, withMRTitle("Stacked PR"), withMRActivity(now.Add(-10*24*time.Hour)))
	stackedPR.UpdatedAt = now.Add(-16 * time.Minute)
	stackedPR.LastActivityAt = now.Add(-16 * time.Minute)
	stackedPRID := insertTestMRWithOptions(t, d, stackedPR)
	lastComment := now.Add(-14 * time.Hour)
	require.NoError(d.UpsertMREvents(ctx, []MREvent{
		{MergeRequestID: stackedPRID, EventType: "issue_comment", Author: "reviewer",
			CreatedAt: lastComment, DedupeKey: "stack-comment"},
		{MergeRequestID: stackedPRID, EventType: "cross_referenced", Author: "bot",
			CreatedAt: now.Add(-time.Hour), DedupeKey: "stack-xref"},
	}))

	// Only the provider timestamp is inside the window; the ledger is stale.
	dormantPR := testMR(repoID, 2, withMRTitle("Dormant PR"), withMRActivity(now.Add(-30*24*time.Hour)))
	dormantPR.UpdatedAt = now.Add(-time.Hour)
	dormantPR.LastActivityAt = now.Add(-time.Hour)
	dormantPRID := insertTestMRWithOptions(t, d, dormantPR)
	require.NoError(d.UpsertMREvents(ctx, []MREvent{{
		MergeRequestID: dormantPRID, EventType: "issue_comment", Author: "reviewer",
		CreatedAt: now.Add(-20 * 24 * time.Hour), DedupeKey: "dormant-comment",
	}}))

	// A merge is activity even without a rendered event row.
	mergedAt := now.Add(-2 * time.Hour)
	mergedPR := testMR(repoID, 3, withMRTitle("Merged PR"), withMRState(MergeRequestStateMerged),
		withMRActivity(now.Add(-30*24*time.Hour)))
	mergedPR.MergedAt = &mergedAt
	mergedPR.ClosedAt = &mergedAt
	insertTestMRWithOptions(t, d, mergedPR)

	// A reopen is a lifecycle action even though the feed renders no row for it.
	reopenedAt := now.Add(-3 * time.Hour)
	reopenedPRID := insertTestMRWithOptions(t, d, testMR(repoID, 5, withMRTitle("Reopened PR"),
		withMRActivity(now.Add(-30*24*time.Hour))))
	require.NoError(d.UpsertMREvents(ctx, []MREvent{{
		MergeRequestID: reopenedPRID, EventType: "reopened", Author: "author",
		CreatedAt: reopenedAt, DedupeKey: "reopened-pr",
	}}))

	issue := testIssue(repoID, 4, withIssueTitle("Issue"), withIssueActivity(now.Add(-9*24*time.Hour)))
	issue.UpdatedAt = now.Add(-10 * time.Minute)
	issue.LastActivityAt = now.Add(-10 * time.Minute)
	issueID := insertTestIssueWithOptions(t, d, issue)
	lastIssueComment := now.Add(-5 * time.Hour)
	require.NoError(d.UpsertIssueEvents(ctx, []IssueEvent{
		{IssueID: issueID, EventType: "issue_comment", Author: "reporter",
			CreatedAt: lastIssueComment, DedupeKey: "issue-comment"},
		{IssueID: issueID, EventType: "assigned", Author: "triager",
			CreatedAt: now.Add(-30 * time.Minute), DedupeKey: "issue-assigned"},
	}))

	subjects, err := d.ListActivitySubjects(ctx, ListActivitySubjectsOpts{Since: &since, Limit: 50})
	require.NoError(err)
	activityByNumber := make(map[int]time.Time, len(subjects))
	for _, subject := range subjects {
		activityByNumber[subject.Subject.Key.ItemNumber] = subject.ActivityAt
	}
	assert.Equal(map[int]time.Time{
		1347: lastComment,
		3:    mergedAt,
		4:    lastIssueComment,
		5:    reopenedAt,
	}, activityByNumber, "provider updated_at must not admit or date parents")
	require.Len(subjects, 4)
	assert.Equal(3, subjects[0].Subject.Key.ItemNumber, "ordered by ledger recency")
	assert.Equal(5, subjects[1].Subject.Key.ItemNumber)
	assert.Equal(4, subjects[2].Subject.Key.ItemNumber)
	assert.Equal(1347, subjects[3].Subject.Key.ItemNumber)

	items, err := d.ListActivity(ctx, ListActivityOpts{Since: &since, Limit: 50})
	require.NoError(err)
	for _, item := range items {
		switch item.ItemNumber {
		case 1347:
			require.NotNil(item.ItemLastActivityAt, "%s row", item.ActivityType)
			assert.Equal(lastComment, *item.ItemLastActivityAt, "%s row", item.ActivityType)
		case 4:
			require.NotNil(item.ItemLastActivityAt, "%s row", item.ActivityType)
			assert.Equal(lastIssueComment, *item.ItemLastActivityAt, "%s row", item.ActivityType)
		}
	}
}

func TestListActivityAuthorsIncludeParentsRecentOnlyByCloseOrMerge(t *testing.T) {
	require := require.New(t)
	assert := assert.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	now := baseTime()
	since := now.Add(-7 * 24 * time.Hour)
	repoID := insertTestRepo(t, d, "acme", "widget")

	mergedAt := now.Add(-time.Hour)
	mergedPR := testMR(repoID, 1, withMRAuthor("Merger"), withMRState(MergeRequestStateMerged),
		withMRActivity(now.Add(-30*24*time.Hour)))
	mergedPR.MergedAt = &mergedAt
	mergedPR.ClosedAt = &mergedAt
	insertTestMRWithOptions(t, d, mergedPR)

	closedAt := now.Add(-2 * time.Hour)
	closedIssue := testIssue(repoID, 2, withIssueAuthor("Closer"), withIssueActivity(now.Add(-30*24*time.Hour)))
	closedIssue.State = "closed"
	closedIssue.ClosedAt = &closedAt
	insertTestIssueWithOptions(t, d, closedIssue)

	reopenedIssueID := insertTestIssueWithOptions(t, d, testIssue(repoID, 4, withIssueAuthor("Reopener"),
		withIssueActivity(now.Add(-30*24*time.Hour))))
	require.NoError(d.UpsertIssueEvents(ctx, []IssueEvent{{
		IssueID: reopenedIssueID, EventType: "reopened", Author: "Reopener",
		CreatedAt: now.Add(-3 * time.Hour), DedupeKey: "reopened-issue",
	}}))

	dormant := testMR(repoID, 3, withMRAuthor("Dormant"), withMRActivity(now.Add(-30*24*time.Hour)))
	dormant.LastActivityAt = now.Add(-time.Minute)
	insertTestMRWithOptions(t, d, dormant)

	subjects, err := d.ListActivitySubjects(ctx, ListActivitySubjectsOpts{Since: &since, Limit: 50})
	require.NoError(err)
	require.Len(subjects, 3, "merge, close, and reopen admit parents to the window")

	authors, err := d.ListActivityAuthors(ctx, ListActivityAuthorsOpts{Since: &since})
	require.NoError(err)
	assert.Equal([]string{"Merger", "Closer", "Reopener"}, authors,
		"every parent visible in Activity must offer its author as a filter candidate")
}
