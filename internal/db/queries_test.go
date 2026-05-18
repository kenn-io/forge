package db

import (
	"context"
	"database/sql"
	"sync"
	"testing"
	"time"

	Assert "github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStartWorkspaceRetryTransitionsOnlyOneConcurrentCaller(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	d := openTestDB(t)
	ctx := t.Context()
	errMsg := "ensure clone failed"
	ws := &Workspace{
		ID:              "ws-retry-race",
		PlatformHost:    "github.com",
		RepoOwner:       "acme",
		RepoName:        "widget",
		ItemType:        WorkspaceItemTypePullRequest,
		ItemNumber:      42,
		GitHeadRef:      "feature/retry",
		WorkspaceBranch: "middleman/pr-42",
		WorktreePath:    "/tmp/ws-retry-race",
		TmuxSession:     "middleman-ws-retry-race",
		Status:          "error",
		ErrorMessage:    &errMsg,
	}
	require.NoError(d.InsertWorkspace(ctx, ws))

	const callers = 16
	start := make(chan struct{})
	results := make(chan bool, callers)
	errs := make(chan error, callers)
	var wg sync.WaitGroup
	wg.Add(callers)
	for range callers {
		go func() {
			defer wg.Done()
			<-start
			ok, err := d.StartWorkspaceRetry(
				ctx, "ws-retry-race",
			)
			errs <- err
			results <- ok
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	close(errs)

	for err := range errs {
		require.NoError(err)
	}

	var successes int
	for ok := range results {
		if ok {
			successes++
		}
	}
	assert.Equal(1, successes)

	got, err := d.GetWorkspace(ctx, "ws-retry-race")
	require.NoError(err)
	require.NotNil(got)
	assert.Equal("creating", got.Status)
	assert.Nil(got.ErrorMessage)
	assert.Equal("middleman/pr-42", got.WorkspaceBranch)
}

func TestStartWorkspaceRetryPreservesBranchUntilCleanupSucceeds(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	d := openTestDB(t)
	ctx := context.Background()
	errMsg := "tmux new-session failed"
	ws := &Workspace{
		ID:              "ws-retry-preserve-branch",
		PlatformHost:    "github.com",
		RepoOwner:       "acme",
		RepoName:        "widget",
		ItemType:        WorkspaceItemTypePullRequest,
		ItemNumber:      42,
		GitHeadRef:      "feature/retry",
		WorkspaceBranch: "middleman/pr-42",
		WorktreePath:    "/tmp/ws-retry-preserve-branch",
		TmuxSession:     "middleman-ws-retry-preserve-branch",
		Status:          "error",
		ErrorMessage:    &errMsg,
	}
	require.NoError(d.InsertWorkspace(ctx, ws))

	started, err := d.StartWorkspaceRetry(ctx, ws.ID)
	require.NoError(err)
	assert.True(started)

	got, err := d.GetWorkspace(ctx, ws.ID)
	require.NoError(err)
	require.NotNil(got)
	assert.Equal("creating", got.Status)
	assert.Nil(got.ErrorMessage)
	assert.Equal("middleman/pr-42", got.WorkspaceBranch)
}

func insertTestRepo(t *testing.T, d *DB, owner, name string) int64 {
	t.Helper()
	id, err := d.UpsertRepo(t.Context(), GitHubRepoIdentity("github.com", owner, name))
	require.NoErrorf(t, err, "UpsertRepo(%s/%s)", owner, name)
	return id
}

// insertTestRepoWithHost inserts a repo with a specific platform_host.
func insertTestRepoWithHost(
	t *testing.T, d *DB, owner, name, host string,
) int64 {
	t.Helper()
	id, err := d.UpsertRepo(t.Context(), GitHubRepoIdentity(host, owner, name))
	require.NoErrorf(t, err, "UpsertRepo(%s/%s on %s)", owner, name, host)
	return id
}

func TestPurgeOtherHosts(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	base := baseTime()

	// Insert repos for two hosts.
	ghRepoID := insertTestRepoWithHost(
		t, d, "acme", "widget", "github.com",
	)
	gheRepoID := insertTestRepoWithHost(
		t, d, "corp", "internal", "ghes.company.com",
	)

	// Insert MRs for both hosts.
	ghMRID := insertTestMR(
		t, d, ghRepoID, 1, "gh PR", base,
	)
	gheMRID := insertTestMR(
		t, d, gheRepoID, 2, "ghe PR", base,
	)

	// Insert events for both MRs.
	require.NoError(d.UpsertMREvents(ctx, []MREvent{
		{
			MergeRequestID: ghMRID,
			EventType:      "comment",
			Author:         "alice",
			CreatedAt:      base,
			DedupeKey:      "gh-evt-1",
		},
	}))
	require.NoError(d.UpsertMREvents(ctx, []MREvent{
		{
			MergeRequestID: gheMRID,
			EventType:      "comment",
			Author:         "bob",
			CreatedAt:      base,
			DedupeKey:      "ghe-evt-1",
		},
	}))

	// Insert worktree links for both MRs.
	require.NoError(d.SetWorktreeLinks(ctx, []WorktreeLink{
		{
			MergeRequestID: ghMRID,
			WorktreeKey:    "wt-gh",
			LinkedAt:       base,
		},
		{
			MergeRequestID: gheMRID,
			WorktreeKey:    "wt-ghe",
			LinkedAt:       base,
		},
	}))

	// Insert starred items for both repos.
	require.NoError(d.SetStarred(ctx, "pr", ghRepoID, 1))
	require.NoError(d.SetStarred(ctx, "pr", gheRepoID, 2))

	// Insert rate limits for both hosts.
	require.NoError(d.UpsertRateLimit(
		"github.com", "rest", 10, base, 4990, -1, nil,
	))
	require.NoError(d.UpsertRateLimit(
		"ghes.company.com", "rest", 5, base, 4995, -1, nil,
	))

	// Purge all hosts except github.com.
	require.NoError(d.PurgeOtherHosts(ctx, "github.com"))

	// github.com data should remain.
	repos, err := d.ListRepos(ctx)
	require.NoError(err)
	require.Len(repos, 1)
	assert.Equal("github.com", repos[0].PlatformHost)
	assert.Equal("acme", repos[0].Owner)

	// github.com MR should remain.
	ghMR, err := d.GetMergeRequest(ctx, "acme", "widget", 1)
	require.NoError(err)
	require.NotNil(ghMR)

	// github.com events should remain.
	ghEvents, err := d.ListMREvents(ctx, ghMRID)
	require.NoError(err)
	assert.Len(ghEvents, 1)

	// github.com worktree links should remain.
	ghLinks, err := d.GetWorktreeLinksForMR(ctx, ghMRID)
	require.NoError(err)
	assert.Len(ghLinks, 1)

	// github.com starred items should remain.
	starred, err := d.IsStarred(ctx, "pr", ghRepoID, 1)
	require.NoError(err)
	assert.True(starred)

	// ghes.company.com repo should be gone.
	var gheCount int
	err = d.ReadDB().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM middleman_repos
		 WHERE platform_host = 'ghes.company.com'`,
	).Scan(&gheCount)
	require.NoError(err)
	assert.Equal(0, gheCount)

	// ghes.company.com MR should be gone.
	var gheMRCount int
	err = d.ReadDB().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM middleman_merge_requests
		 WHERE repo_id = ?`, gheRepoID,
	).Scan(&gheMRCount)
	require.NoError(err)
	assert.Equal(0, gheMRCount)

	// ghes.company.com events should be gone.
	var gheEvtCount int
	err = d.ReadDB().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM middleman_mr_events
		 WHERE dedupe_key = 'ghe-evt-1'`,
	).Scan(&gheEvtCount)
	require.NoError(err)
	assert.Equal(0, gheEvtCount)

	// github.com rate limits should remain.
	ghRL, err := d.GetRateLimit("github.com", "rest")
	require.NoError(err)
	require.NotNil(ghRL)
	assert.Equal(10, ghRL.RequestsHour)

	// ghes.company.com rate limits should be gone.
	gheRL, err := d.GetRateLimit("ghes.company.com", "rest")
	require.NoError(err)
	assert.Nil(gheRL)
}

// TestCascadeDeleteRepo verifies that deleting a repo on a fresh DB
// cascades to all dependent tables (mr_events, kanban_state, issue_events).
func TestCascadeDeleteRepo(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	base := baseTime()

	repoID := insertTestRepo(t, d, "acme", "widget")

	// Create MR with events and kanban state.
	mrID := insertTestMR(t, d, repoID, 1, "test PR", base)
	require.NoError(d.UpsertMREvents(ctx, []MREvent{
		{
			MergeRequestID: mrID,
			EventType:      "comment",
			Author:         "alice",
			CreatedAt:      base,
			DedupeKey:      "cascade-mr-evt",
		},
	}))
	require.NoError(d.SetKanbanState(ctx, mrID, "reviewing"))

	// Create issue with events.
	issueID := insertTestIssue(t, d, repoID, 10, "test issue", base)
	require.NoError(d.UpsertIssueEvents(ctx, []IssueEvent{
		{
			IssueID:   issueID,
			EventType: "comment",
			Author:    "bob",
			CreatedAt: base,
			DedupeKey: "cascade-issue-evt",
		},
	}))

	// Direct delete of the repo should cascade through all dependents.
	_, err := d.WriteDB().ExecContext(ctx,
		`DELETE FROM middleman_repos WHERE id = ?`, repoID,
	)
	require.NoError(err)

	// All dependent rows should be gone.
	var count int
	err = d.ReadDB().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM middleman_merge_requests`,
	).Scan(&count)
	require.NoError(err)
	assert.Equal(0, count)

	err = d.ReadDB().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM middleman_mr_events`,
	).Scan(&count)
	require.NoError(err)
	assert.Equal(0, count)

	err = d.ReadDB().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM middleman_kanban_state`,
	).Scan(&count)
	require.NoError(err)
	assert.Equal(0, count)

	err = d.ReadDB().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM middleman_issues`,
	).Scan(&count)
	require.NoError(err)
	assert.Equal(0, count)

	err = d.ReadDB().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM middleman_issue_events`,
	).Scan(&count)
	require.NoError(err)
	assert.Equal(0, count)
}

func TestUpsertMREventsUpdatesExistingEventBody(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	base := baseTime()

	repoID := insertTestRepo(t, d, "acme", "widget")
	mrID := insertTestMR(t, d, repoID, 1, "edited comment", base)
	platformID := int64(101)

	require.NoError(d.UpsertMREvents(ctx, []MREvent{{
		MergeRequestID: mrID,
		PlatformID:     &platformID,
		EventType:      "issue_comment",
		Author:         "alice",
		Body:           "original body",
		CreatedAt:      base,
		DedupeKey:      "comment-101",
	}}))

	require.NoError(d.UpsertMREvents(ctx, []MREvent{{
		MergeRequestID: mrID,
		PlatformID:     &platformID,
		EventType:      "issue_comment",
		Author:         "alice",
		Body:           "edited body",
		CreatedAt:      base,
		DedupeKey:      "comment-101",
	}}))

	events, err := d.ListMREvents(ctx, mrID)
	require.NoError(err)
	require.Len(events, 1)
	assert.Equal("edited body", events[0].Body)
}

func TestUpsertMREventsUpdatesExistingReviewState(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	base := baseTime()

	repoID := insertTestRepo(t, d, "acme", "widget")
	mrID := insertTestMR(t, d, repoID, 1, "dismissed review", base)
	platformID := int64(202)

	require.NoError(d.UpsertMREvents(ctx, []MREvent{{
		MergeRequestID: mrID,
		PlatformID:     &platformID,
		EventType:      "review",
		Author:         "carol",
		Summary:        "APPROVED",
		CreatedAt:      base,
		DedupeKey:      "review-202",
	}}))

	require.NoError(d.UpsertMREvents(ctx, []MREvent{{
		MergeRequestID: mrID,
		PlatformID:     &platformID,
		EventType:      "review",
		Author:         "carol",
		Summary:        "DISMISSED",
		CreatedAt:      base.Add(time.Hour),
		DedupeKey:      "review-202",
	}}))

	events, err := d.ListMREvents(ctx, mrID)
	require.NoError(err)
	require.Len(events, 1)
	assert.Equal("DISMISSED", events[0].Summary)
	assert.Equal(base.Add(time.Hour), events[0].CreatedAt)
}

func TestUpsertIssueEventsUpdatesExistingEventBody(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	base := baseTime()

	repoID := insertTestRepo(t, d, "acme", "widget")
	issueID, err := d.UpsertIssue(ctx, &Issue{
		RepoID:         repoID,
		PlatformID:     201,
		Number:         1,
		URL:            "https://github.com/acme/widget/issues/1",
		Title:          "edited issue comment",
		Author:         "alice",
		State:          "open",
		CreatedAt:      base,
		UpdatedAt:      base,
		LastActivityAt: base,
	})
	require.NoError(err)
	platformID := int64(202)

	require.NoError(d.UpsertIssueEvents(ctx, []IssueEvent{{
		IssueID:    issueID,
		PlatformID: &platformID,
		EventType:  "issue_comment",
		Author:     "alice",
		Body:       "original body",
		CreatedAt:  base,
		DedupeKey:  "issue-comment-202",
	}}))

	require.NoError(d.UpsertIssueEvents(ctx, []IssueEvent{{
		IssueID:    issueID,
		PlatformID: &platformID,
		EventType:  "issue_comment",
		Author:     "alice",
		Body:       "edited body",
		CreatedAt:  base,
		DedupeKey:  "issue-comment-202",
	}}))

	events, err := d.ListIssueEvents(ctx, issueID)
	require.NoError(err)
	require.Len(events, 1)
	assert.Equal("edited body", events[0].Body)
}

func TestIssueEventsDedupeIsScopedToIssue(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	base := baseTime()

	repoID := insertTestRepo(t, d, "o", "r")
	firstIssueID := insertTestIssue(t, d, repoID, 1, "issue one", base)
	secondIssueID := insertTestIssue(t, d, repoID, 2, "issue two", base.Add(time.Minute))
	firstPlatformID := int64(5001)
	secondPlatformID := int64(5002)

	sharedDedupeKey := "gitlab:gitlab.example.com:o/r:issue:note:shared"
	require.NoError(d.UpsertIssueEvents(ctx, []IssueEvent{
		{
			IssueID:            firstIssueID,
			PlatformID:         &firstPlatformID,
			PlatformExternalID: "gid://gitlab/Note/5001",
			EventType:          "issue_comment",
			Author:             "alice",
			CreatedAt:          base,
			DedupeKey:          sharedDedupeKey,
		},
		{
			IssueID:            secondIssueID,
			PlatformID:         &secondPlatformID,
			PlatformExternalID: "gid://gitlab/Note/5002",
			EventType:          "issue_comment",
			Author:             "bob",
			CreatedAt:          base.Add(time.Minute),
			DedupeKey:          sharedDedupeKey,
		},
	}))

	firstEvents, err := d.ListIssueEvents(ctx, firstIssueID)
	require.NoError(err)
	require.Len(firstEvents, 1)
	assert.Equal(sharedDedupeKey, firstEvents[0].DedupeKey)
	assert.Equal("gid://gitlab/Note/5001", firstEvents[0].PlatformExternalID)

	secondEvents, err := d.ListIssueEvents(ctx, secondIssueID)
	require.NoError(err)
	require.Len(secondEvents, 1)
	assert.Equal(sharedDedupeKey, secondEvents[0].DedupeKey)
	assert.Equal("gid://gitlab/Note/5002", secondEvents[0].PlatformExternalID)
}

func TestItemsPersistPlatformExternalID(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	base := baseTime()

	repoID := insertTestRepo(t, d, "acme", "widget")
	_, err := d.UpsertMergeRequest(ctx, &MergeRequest{
		RepoID:             repoID,
		PlatformID:         1001,
		PlatformExternalID: "gid://gitlab/MergeRequest/1001",
		Number:             1,
		URL:                "https://gitlab.example.com/acme/widget/-/merge_requests/1",
		Title:              "external mr",
		Author:             "alice",
		State:              "open",
		CreatedAt:          base,
		UpdatedAt:          base,
		LastActivityAt:     base,
	})
	require.NoError(err)
	_, err = d.UpsertIssue(ctx, &Issue{
		RepoID:             repoID,
		PlatformID:         2001,
		PlatformExternalID: "gid://gitlab/Issue/2001",
		Number:             2,
		URL:                "https://gitlab.example.com/acme/widget/-/issues/2",
		Title:              "external issue",
		Author:             "bob",
		State:              "open",
		CreatedAt:          base,
		UpdatedAt:          base,
		LastActivityAt:     base,
	})
	require.NoError(err)

	mr, err := d.GetMergeRequestByRepoIDAndNumber(ctx, repoID, 1)
	require.NoError(err)
	require.NotNil(mr)
	assert.Equal("gid://gitlab/MergeRequest/1001", mr.PlatformExternalID)

	issue, err := d.GetIssueByRepoIDAndNumber(ctx, repoID, 2)
	require.NoError(err)
	require.NotNil(issue)
	assert.Equal("gid://gitlab/Issue/2001", issue.PlatformExternalID)
}

func TestUpsertAndListRepos(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()

	id1, err := d.UpsertRepo(ctx, GitHubRepoIdentity("github.com", "alice", "alpha"))
	require.NoError(err)
	id2, err := d.UpsertRepo(ctx, GitHubRepoIdentity("github.com", "bob", "beta"))
	require.NoError(err)
	assert.NotEqual(id1, id2)

	// Idempotency: re-inserting should return the same ID.
	id1Again, err := d.UpsertRepo(ctx, GitHubRepoIdentity("github.com", "alice", "alpha"))
	require.NoError(err)
	assert.Equal(id1, id1Again)

	repos, err := d.ListRepos(ctx)
	require.NoError(err)
	require.Len(repos, 2)
	// Ordered by owner, name: alice/alpha, bob/beta.
	assert.Equal("alice", repos[0].Owner)
	assert.Equal("alpha", repos[0].Name)
	assert.Equal("bob", repos[1].Owner)
	assert.Equal("beta", repos[1].Name)
}

func TestUpsertRepoDefaultsToGitHubIdentity(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()

	id, err := d.UpsertRepo(ctx, GitHubRepoIdentity("github.com", "Alice", "Alpha"))
	require.NoError(err)

	repo, err := d.GetRepoByID(ctx, id)
	require.NoError(err)
	require.NotNil(repo)
	assert.Equal("github", repo.Platform)
	assert.Equal("github.com", repo.PlatformHost)
	assert.Equal("alice", repo.Owner)
	assert.Equal("alpha", repo.Name)
	assert.Equal("alice/alpha", repo.RepoPath)
	assert.Equal("alice", repo.OwnerKey)
	assert.Equal("alpha", repo.NameKey)
	assert.Equal("alice/alpha", repo.RepoPathKey)
	assert.Empty(repo.PlatformRepoID)
	assert.Empty(repo.WebURL)
	assert.Empty(repo.CloneURL)
	assert.Empty(repo.DefaultBranch)
}

func TestUpsertRepoSupportsProviderIdentity(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()

	githubID, err := d.UpsertRepo(ctx, RepoIdentity{
		Platform:     "github",
		PlatformHost: "example.com",
		Owner:        "acme",
		Name:         "widget",
	})
	require.NoError(err)
	gitlabID, err := d.UpsertRepo(ctx, RepoIdentity{
		Platform:     "gitlab",
		PlatformHost: "example.com",
		Owner:        "acme",
		Name:         "widget",
	})
	require.NoError(err)

	assert.NotEqual(githubID, gitlabID)

	repos, err := d.ListRepos(ctx)
	require.NoError(err)
	require.Len(repos, 2)
	assert.Equal("github", repos[0].Platform)
	assert.Equal("gitlab", repos[1].Platform)
	for _, repo := range repos {
		assert.Equal("example.com", repo.PlatformHost)
		assert.Equal("acme/widget", repo.RepoPath)
		assert.Equal("acme", repo.OwnerKey)
		assert.Equal("widget", repo.NameKey)
		assert.Equal("acme/widget", repo.RepoPathKey)
	}
}

func TestUpsertRepoPreservesNonGitHubDisplayIdentity(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()

	id, err := d.UpsertRepo(ctx, RepoIdentity{
		Platform:     "gitlab",
		PlatformHost: "gitlab.example.com",
		Owner:        "Group/SubGroup",
		Name:         "ProjectName",
		RepoPath:     "Group/SubGroup/ProjectName",
	})
	require.NoError(err)

	repo, err := d.GetRepoByID(ctx, id)
	require.NoError(err)
	require.NotNil(repo)
	assert.Equal("Group/SubGroup", repo.Owner)
	assert.Equal("ProjectName", repo.Name)
	assert.Equal("Group/SubGroup/ProjectName", repo.RepoPath)
	assert.Equal("group/subgroup", repo.OwnerKey)
	assert.Equal("projectname", repo.NameKey)
	assert.Equal("group/subgroup/projectname", repo.RepoPathKey)
}

func TestProviderCanonicalReadPathsUseLookupKeys(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()

	repoID, err := d.UpsertRepo(ctx, RepoIdentity{
		Platform:     "gitlab",
		PlatformHost: "gitlab.example.com",
		Owner:        "Group/SubGroup",
		Name:         "ProjectName",
		RepoPath:     "Group/SubGroup/ProjectName",
	})
	require.NoError(err)
	mrID := insertTestMRWithOptions(t, d, testMR(repoID, 7, withMRTitle("GitLab PR")))
	issueID := insertTestIssueWithOptions(t, d, testIssue(repoID, 8, withIssueTitle("GitLab issue")))

	gotMR, err := d.GetMergeRequest(ctx, "group/subgroup", "projectname", 7)
	require.NoError(err)
	require.NotNil(gotMR)
	assert.Equal(mrID, gotMR.ID)

	listedMRs, err := d.ListMergeRequests(ctx, ListMergeRequestsOpts{
		PlatformHost: "gitlab.example.com",
		RepoOwner:    "GROUP/SubGroup",
		RepoName:     "PROJECTName",
	})
	require.NoError(err)
	require.Len(listedMRs, 1)
	assert.Equal(mrID, listedMRs[0].ID)

	gotMRID, err := d.GetMRIDByRepoAndNumber(ctx, "GROUP/SubGroup", "PROJECTName", 7)
	require.NoError(err)
	assert.Equal(mrID, gotMRID)

	require.NoError(d.UpdateDiffSHAs(ctx, repoID, 7, "head", "base", "merge"))
	shas, err := d.GetDiffSHAs(ctx, "group/subgroup", "projectname", 7)
	require.NoError(err)
	require.NotNil(shas)
	assert.Equal("head", shas.DiffHeadSHA)

	gotIssue, err := d.GetIssue(ctx, "group/subgroup", "projectname", 8)
	require.NoError(err)
	require.NotNil(gotIssue)
	assert.Equal(issueID, gotIssue.ID)

	listedIssues, err := d.ListIssues(ctx, ListIssuesOpts{
		PlatformHost: "gitlab.example.com",
		RepoOwner:    "GROUP/SubGroup",
		RepoName:     "PROJECTName",
	})
	require.NoError(err)
	require.Len(listedIssues, 1)
	assert.Equal(issueID, listedIssues[0].ID)

	gotIssueID, err := d.GetIssueIDByRepoAndNumber(ctx, "GROUP/SubGroup", "PROJECTName", 8)
	require.NoError(err)
	assert.Equal(issueID, gotIssueID)

	require.NoError(d.UpdateMRDetailFetched(ctx, "gitlab.example.com", "group/subgroup", "projectname", 7, true))
	refreshedMR, err := d.GetMergeRequestByRepoIDAndNumber(ctx, repoID, 7)
	require.NoError(err)
	require.NotNil(refreshedMR)
	require.NotNil(refreshedMR.DetailFetchedAt)
	assert.True(refreshedMR.CIHadPending)

	require.NoError(d.UpdateIssueDetailFetched(ctx, "gitlab.example.com", "group/subgroup", "projectname", 8))
	refreshedIssue, err := d.GetIssueByRepoIDAndNumber(ctx, repoID, 8)
	require.NoError(err)
	require.NotNil(refreshedIssue)
	assert.NotNil(refreshedIssue.DetailFetchedAt)

	users, err := d.ListCommentAutocompleteUsers(ctx, "gitlab.example.com", "group/subgroup", "projectname", "auth", 10)
	require.NoError(err)
	assert.Equal([]string{"author"}, users)

	refs, err := d.ListCommentAutocompleteReferences(ctx, "gitlab.example.com", "group/subgroup", "projectname", "GitLab", 10)
	require.NoError(err)
	require.Len(refs, 2)
	assert.Equal([]int{8, 7}, []int{refs[0].Number, refs[1].Number})

	activity, err := d.ListActivity(ctx, ListActivityOpts{
		Repo:  "gitlab.example.com/group/subgroup/projectname",
		Limit: 10,
	})
	require.NoError(err)
	require.Len(activity, 2)
	assert.ElementsMatch([]int{7, 8}, []int{activity[0].ItemNumber, activity[1].ItemNumber})

	stackID, err := d.UpsertStack(ctx, repoID, 7, "stack")
	require.NoError(err)
	require.NoError(d.ReplaceStackMembers(ctx, stackID, []StackMember{{
		MergeRequestID: mrID,
		Position:       0,
	}}))
	stacks, members, err := d.ListStacksWithMembers(ctx, "group/subgroup/projectname")
	require.NoError(err)
	require.Len(stacks, 1)
	assert.Equal(stackID, stacks[0].ID)
	require.Len(members[stackID], 1)
	assert.Equal(mrID, members[stackID][0].MergeRequestID)

	stack, stackMembers, err := d.GetStackForPR(ctx, "group/subgroup", "projectname", 7)
	require.NoError(err)
	require.NotNil(stack)
	assert.Equal(stackID, stack.ID)
	require.Len(stackMembers, 1)
	assert.Equal(mrID, stackMembers[0].MergeRequestID)
}

func TestUpdateRepoProviderMetadataPreservesIdentity(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()

	repoID, err := d.UpsertRepo(ctx, GitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)

	err = d.UpdateRepoProviderMetadata(ctx, repoID, RepoProviderMetadata{
		PlatformRepoID: "R_123",
		WebURL:         "https://github.com/acme/widget",
		CloneURL:       "https://github.com/acme/widget.git",
		DefaultBranch:  "main",
	})
	require.NoError(err)

	repo, err := d.GetRepoByID(ctx, repoID)
	require.NoError(err)
	require.NotNil(repo)
	assert.Equal("github", repo.Platform)
	assert.Equal("github.com", repo.PlatformHost)
	assert.Equal("acme", repo.Owner)
	assert.Equal("widget", repo.Name)
	assert.Equal("acme/widget", repo.RepoPath)
	assert.Equal("R_123", repo.PlatformRepoID)
	assert.Equal("https://github.com/acme/widget", repo.WebURL)
	assert.Equal("https://github.com/acme/widget.git", repo.CloneURL)
	assert.Equal("main", repo.DefaultBranch)

	sameID, err := d.UpsertRepo(ctx, GitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	assert.Equal(repoID, sameID)
}

func TestUpsertRepoByProviderIDUpdatesRenamedRepo(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()

	originalID, err := d.UpsertRepoByProviderID(ctx, RepoIdentity{
		Platform:       "gitlab",
		PlatformHost:   "gitlab.com",
		PlatformRepoID: "gid://gitlab/Project/42",
		Owner:          "old-group",
		Name:           "old-name",
	})
	require.NoError(err)

	renamedID, err := d.UpsertRepoByProviderID(ctx, RepoIdentity{
		Platform:       "gitlab",
		PlatformHost:   "gitlab.com",
		PlatformRepoID: "gid://gitlab/Project/42",
		Owner:          "New-Group/SubGroup",
		Name:           "New-Name",
		RepoPath:       "New-Group/SubGroup/New-Name",
	})
	require.NoError(err)

	assert.Equal(originalID, renamedID)
	repos, err := d.ListRepos(ctx)
	require.NoError(err)
	require.Len(repos, 1)
	assert.Equal("New-Group/SubGroup", repos[0].Owner)
	assert.Equal("New-Name", repos[0].Name)
	assert.Equal("New-Group/SubGroup/New-Name", repos[0].RepoPath)
	assert.Equal("new-group/subgroup", repos[0].OwnerKey)
	assert.Equal("new-name", repos[0].NameKey)
	assert.Equal("new-group/subgroup/new-name", repos[0].RepoPathKey)
}

func TestUpsertRepoByProviderIDUpdatesRenamedRepoWorkspaces(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()

	originalID, err := d.UpsertRepoByProviderID(ctx, RepoIdentity{
		Platform:       "gitlab",
		PlatformHost:   "gitlab.com",
		PlatformRepoID: "gid://gitlab/Project/42",
		Owner:          "Old-Group",
		Name:           "Old-Name",
		RepoPath:       "Old-Group/Old-Name",
	})
	require.NoError(err)
	insertTestMRWithOptions(t, d, testMR(originalID, 7, withMRTitle("renamed PR")))
	require.NoError(d.InsertWorkspace(ctx, &Workspace{
		ID:           "renamed-workspace",
		PlatformHost: "gitlab.com",
		RepoOwner:    "Old-Group",
		RepoName:     "Old-Name",
		ItemType:     WorkspaceItemTypePullRequest,
		ItemNumber:   7,
		GitHeadRef:   "feature",
		WorktreePath: "/tmp/renamed-workspace",
		TmuxSession:  "renamed-workspace",
	}))

	renamedID, err := d.UpsertRepoByProviderID(ctx, RepoIdentity{
		Platform:       "gitlab",
		PlatformHost:   "gitlab.com",
		PlatformRepoID: "gid://gitlab/Project/42",
		Owner:          "New-Group/SubGroup",
		Name:           "New-Name",
		RepoPath:       "New-Group/SubGroup/New-Name",
	})
	require.NoError(err)
	assert.Equal(originalID, renamedID)

	workspace, err := d.GetWorkspaceByMR(ctx, "gitlab.com", "new-group/subgroup", "new-name", 7)
	require.NoError(err)
	require.NotNil(workspace)
	assert.Equal("renamed-workspace", workspace.ID)
	assert.Equal("New-Group/SubGroup", workspace.RepoOwner)
	assert.Equal("New-Name", workspace.RepoName)

	summary, err := d.GetWorkspaceSummary(ctx, "renamed-workspace")
	require.NoError(err)
	require.NotNil(summary)
	require.NotNil(summary.MRTitle)
	assert.Equal("renamed PR", *summary.MRTitle)
}

func TestUpsertRepoByProviderIDMergesExistingDestinationPathRow(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()

	sourceID, err := d.UpsertRepoByProviderID(ctx, RepoIdentity{
		Platform:       "gitlab",
		PlatformHost:   "gitlab.com",
		PlatformRepoID: "gid://gitlab/Project/42",
		Owner:          "old-group",
		Name:           "old-name",
	})
	require.NoError(err)
	insertTestMRWithOptions(t, d, testMR(sourceID, 7, withMRTitle("source PR")))

	destinationID, err := d.UpsertRepo(ctx, RepoIdentity{
		Platform:     "gitlab",
		PlatformHost: "gitlab.com",
		Owner:        "new-group",
		Name:         "new-name",
	})
	require.NoError(err)
	assert.NotEqual(sourceID, destinationID)
	insertTestIssueWithOptions(t, d, testIssue(destinationID, 8, withIssueTitle("destination issue")))

	gotID, err := d.UpsertRepoByProviderID(ctx, RepoIdentity{
		Platform:       "gitlab",
		PlatformHost:   "gitlab.com",
		PlatformRepoID: "gid://gitlab/Project/42",
		Owner:          "new-group",
		Name:           "new-name",
	})
	require.NoError(err)
	assert.Equal(destinationID, gotID)

	repos, err := d.ListRepos(ctx)
	require.NoError(err)
	require.Len(repos, 1)
	assert.Equal(destinationID, repos[0].ID)
	assert.Equal("gid://gitlab/Project/42", repos[0].PlatformRepoID)
	assert.Equal("new-group", repos[0].Owner)
	assert.Equal("new-name", repos[0].Name)
	assert.Equal("new-group/new-name", repos[0].RepoPath)

	sourceAfterMerge, err := d.GetRepoByID(ctx, sourceID)
	require.NoError(err)
	assert.Nil(sourceAfterMerge)

	var mergeRequestRepoID int64
	err = d.ReadDB().QueryRowContext(ctx,
		`SELECT repo_id FROM middleman_merge_requests WHERE number = ?`,
		7,
	).Scan(&mergeRequestRepoID)
	require.NoError(err)
	assert.Equal(destinationID, mergeRequestRepoID)

	var issueRepoID int64
	err = d.ReadDB().QueryRowContext(ctx,
		`SELECT repo_id FROM middleman_issues WHERE number = ?`,
		8,
	).Scan(&issueRepoID)
	require.NoError(err)
	assert.Equal(destinationID, issueRepoID)
}

func TestUpsertRepoByProviderIDMergesExistingDestinationPathRowWorkspaces(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()

	sourceID, err := d.UpsertRepoByProviderID(ctx, RepoIdentity{
		Platform:       "gitlab",
		PlatformHost:   "gitlab.com",
		PlatformRepoID: "gid://gitlab/Project/42",
		Owner:          "Old-Group",
		Name:           "Old-Name",
		RepoPath:       "Old-Group/Old-Name",
	})
	require.NoError(err)
	insertTestMRWithOptions(t, d, testMR(sourceID, 7, withMRTitle("merged PR")))
	require.NoError(d.InsertWorkspace(ctx, &Workspace{
		ID:           "merged-workspace",
		PlatformHost: "gitlab.com",
		RepoOwner:    "Old-Group",
		RepoName:     "Old-Name",
		ItemType:     WorkspaceItemTypePullRequest,
		ItemNumber:   7,
		GitHeadRef:   "feature",
		WorktreePath: "/tmp/merged-workspace",
		TmuxSession:  "merged-workspace",
	}))

	destinationID, err := d.UpsertRepo(ctx, RepoIdentity{
		Platform:     "gitlab",
		PlatformHost: "gitlab.com",
		Owner:        "New-Group/SubGroup",
		Name:         "New-Name",
		RepoPath:     "New-Group/SubGroup/New-Name",
	})
	require.NoError(err)
	require.NotEqual(sourceID, destinationID)

	gotID, err := d.UpsertRepoByProviderID(ctx, RepoIdentity{
		Platform:       "gitlab",
		PlatformHost:   "gitlab.com",
		PlatformRepoID: "gid://gitlab/Project/42",
		Owner:          "New-Group/SubGroup",
		Name:           "New-Name",
		RepoPath:       "New-Group/SubGroup/New-Name",
	})
	require.NoError(err)
	assert.Equal(destinationID, gotID)

	workspace, err := d.GetWorkspaceByMR(ctx, "gitlab.com", "new-group/subgroup", "new-name", 7)
	require.NoError(err)
	require.NotNil(workspace)
	assert.Equal("merged-workspace", workspace.ID)
	assert.Equal("New-Group/SubGroup", workspace.RepoOwner)
	assert.Equal("New-Name", workspace.RepoName)

	summary, err := d.GetWorkspaceSummary(ctx, "merged-workspace")
	require.NoError(err)
	require.NotNil(summary)
	require.NotNil(summary.MRTitle)
	assert.Equal("merged PR", *summary.MRTitle)
}

func TestUpsertRepoByProviderIDMergesCollidingWorkspaceRows(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()

	sourceID, err := d.UpsertRepoByProviderID(ctx, RepoIdentity{
		Platform:       "gitlab",
		PlatformHost:   "gitlab.com",
		PlatformRepoID: "gid://gitlab/Project/42",
		Owner:          "Old-Group",
		Name:           "Old-Name",
		RepoPath:       "Old-Group/Old-Name",
	})
	require.NoError(err)
	insertTestMRWithOptions(t, d, testMR(sourceID, 7, withMRTitle("merged PR")))
	require.NoError(d.InsertWorkspace(ctx, &Workspace{
		ID:           "source-workspace",
		PlatformHost: "gitlab.com",
		RepoOwner:    "Old-Group",
		RepoName:     "Old-Name",
		ItemType:     WorkspaceItemTypePullRequest,
		ItemNumber:   7,
		GitHeadRef:   "feature",
		WorktreePath: "/tmp/source-workspace",
		TmuxSession:  "source-workspace",
	}))
	require.NoError(d.InsertWorkspaceSetupEvent(ctx, &WorkspaceSetupEvent{
		WorkspaceID: "source-workspace",
		Stage:       "clone",
		Outcome:     "ok",
		Message:     "source event",
	}))
	require.NoError(d.UpsertWorkspaceTmuxSession(ctx, &WorkspaceTmuxSession{
		WorkspaceID: "source-workspace",
		SessionName: "source-session",
		TargetKey:   "source",
	}))

	destinationID, err := d.UpsertRepo(ctx, RepoIdentity{
		Platform:     "gitlab",
		PlatformHost: "gitlab.com",
		Owner:        "New-Group/SubGroup",
		Name:         "New-Name",
		RepoPath:     "New-Group/SubGroup/New-Name",
	})
	require.NoError(err)
	require.NotEqual(sourceID, destinationID)
	require.NoError(d.InsertWorkspace(ctx, &Workspace{
		ID:           "destination-workspace",
		PlatformHost: "gitlab.com",
		RepoOwner:    "New-Group/SubGroup",
		RepoName:     "New-Name",
		ItemType:     WorkspaceItemTypePullRequest,
		ItemNumber:   7,
		GitHeadRef:   "feature",
		WorktreePath: "/tmp/destination-workspace",
		TmuxSession:  "destination-workspace",
	}))
	require.NoError(d.InsertWorkspaceSetupEvent(ctx, &WorkspaceSetupEvent{
		WorkspaceID: "destination-workspace",
		Stage:       "deps",
		Outcome:     "ok",
		Message:     "destination event",
	}))
	require.NoError(d.UpsertWorkspaceTmuxSession(ctx, &WorkspaceTmuxSession{
		WorkspaceID: "destination-workspace",
		SessionName: "destination-session",
		TargetKey:   "destination",
	}))

	gotID, err := d.UpsertRepoByProviderID(ctx, RepoIdentity{
		Platform:       "gitlab",
		PlatformHost:   "gitlab.com",
		PlatformRepoID: "gid://gitlab/Project/42",
		Owner:          "New-Group/SubGroup",
		Name:           "New-Name",
		RepoPath:       "New-Group/SubGroup/New-Name",
	})
	require.NoError(err)
	assert.Equal(destinationID, gotID)

	workspace, err := d.GetWorkspaceByMR(ctx, "gitlab.com", "new-group/subgroup", "new-name", 7)
	require.NoError(err)
	require.NotNil(workspace)
	assert.Equal("destination-workspace", workspace.ID)
	assert.Equal("New-Group/SubGroup", workspace.RepoOwner)
	assert.Equal("New-Name", workspace.RepoName)

	sourceWorkspace, err := d.GetWorkspace(ctx, "source-workspace")
	require.NoError(err)
	assert.Nil(sourceWorkspace)

	events, err := d.ListWorkspaceSetupEvents(ctx, "destination-workspace")
	require.NoError(err)
	require.Len(events, 2)
	assert.Equal("source event", events[0].Message)
	assert.Equal("destination event", events[1].Message)

	tmuxSessions, err := d.ListWorkspaceTmuxSessions(ctx, "destination-workspace")
	require.NoError(err)
	require.Len(tmuxSessions, 2)
	assert.Equal("destination-session", tmuxSessions[0].SessionName)
	assert.Equal("source-session", tmuxSessions[1].SessionName)

	summary, err := d.GetWorkspaceSummary(ctx, "destination-workspace")
	require.NoError(err)
	require.NotNil(summary)
	require.NotNil(summary.MRTitle)
	assert.Equal("merged PR", *summary.MRTitle)
}

func TestUpsertRepoByProviderIDMergesMovedItemLabelLinksIntoDestinationLabels(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	now := baseTime()

	sourceID, err := d.UpsertRepoByProviderID(ctx, RepoIdentity{
		Platform:       "gitlab",
		PlatformHost:   "gitlab.com",
		PlatformRepoID: "gid://gitlab/Project/42",
		Owner:          "old-group",
		Name:           "old-name",
	})
	require.NoError(err)
	sourceMRID := insertTestMRWithOptions(t, d, testMR(sourceID, 7, withMRTitle("source PR")))
	sourceIssueID := insertTestIssueWithOptions(t, d, testIssue(sourceID, 8, withIssueTitle("source issue")))

	require.NoError(d.ReplaceMergeRequestLabels(ctx, sourceID, sourceMRID, []Label{{
		PlatformID:  300,
		Name:        "bug",
		Description: "source label",
		Color:       "d73a4a",
		UpdatedAt:   now,
	}}))
	require.NoError(d.ReplaceIssueLabels(ctx, sourceID, sourceIssueID, []Label{{
		PlatformID:  300,
		Name:        "bug",
		Description: "source label",
		Color:       "d73a4a",
		UpdatedAt:   now,
	}}))

	destinationID, err := d.UpsertRepo(ctx, RepoIdentity{
		Platform:     "gitlab",
		PlatformHost: "gitlab.com",
		Owner:        "new-group",
		Name:         "new-name",
	})
	require.NoError(err)
	require.NoError(d.UpsertLabels(ctx, destinationID, []Label{{
		PlatformID:  300,
		Name:        "bug",
		Description: "destination label",
		Color:       "b60205",
		IsDefault:   true,
		UpdatedAt:   now.Add(time.Minute),
	}}))

	var destinationLabelID int64
	err = d.ReadDB().QueryRowContext(ctx,
		`SELECT id FROM middleman_labels WHERE repo_id = ? AND name = ?`,
		destinationID, "bug",
	).Scan(&destinationLabelID)
	require.NoError(err)

	gotID, err := d.UpsertRepoByProviderID(ctx, RepoIdentity{
		Platform:       "gitlab",
		PlatformHost:   "gitlab.com",
		PlatformRepoID: "gid://gitlab/Project/42",
		Owner:          "new-group",
		Name:           "new-name",
	})
	require.NoError(err)
	assert.Equal(destinationID, gotID)

	mr, err := d.GetMergeRequestByRepoIDAndNumber(ctx, destinationID, 7)
	require.NoError(err)
	require.NotNil(mr)
	require.Len(mr.Labels, 1)
	assert.Equal(destinationLabelID, mr.Labels[0].ID)
	assert.Equal("bug", mr.Labels[0].Name)

	issue, err := d.GetIssueByRepoIDAndNumber(ctx, destinationID, 8)
	require.NoError(err)
	require.NotNil(issue)
	require.Len(issue.Labels, 1)
	assert.Equal(destinationLabelID, issue.Labels[0].ID)
	assert.Equal("bug", issue.Labels[0].Name)
}

func TestReplaceRepoLabelCatalogKeepsAssignedHistoricalLabels(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	repoID := insertTestRepo(t, d, "acme", "widget")
	now := baseTime()

	require.NoError(d.ReplaceRepoLabelCatalog(ctx, repoID, []Label{
		{PlatformID: 1, Name: "bug", Description: "Broken", Color: "d73a4a", IsDefault: true, UpdatedAt: now},
		{PlatformID: 2, Name: "triage", Description: "Needs triage", Color: "fbca04", UpdatedAt: now},
	}, now))

	catalog, freshness, err := d.ListRepoLabelCatalog(ctx, repoID)
	require.NoError(err)
	require.Len(catalog, 2)
	assert.Equal("bug", catalog[0].Name)
	assert.Equal("triage", catalog[1].Name)
	require.NotNil(freshness.SyncedAt)
	assert.Equal(now, *freshness.SyncedAt)

	issueID := insertTestIssueWithOptions(t, d, testIssue(repoID, 7, withIssueTitle("issue")))
	require.NoError(d.ReplaceIssueLabels(ctx, repoID, issueID, []Label{
		{PlatformID: 3, Name: "old", Description: "Removed upstream", Color: "cccccc", UpdatedAt: now},
	}))

	next := now.Add(time.Minute)
	require.NoError(d.ReplaceRepoLabelCatalog(ctx, repoID, []Label{
		{PlatformID: 1, Name: "bug", Description: "Broken", Color: "d73a4a", IsDefault: true, UpdatedAt: next},
	}, next))

	catalog, _, err = d.ListRepoLabelCatalog(ctx, repoID)
	require.NoError(err)
	require.Len(catalog, 1)
	assert.Equal("bug", catalog[0].Name)

	issue, err := d.GetIssueByRepoIDAndNumber(ctx, repoID, 7)
	require.NoError(err)
	require.NotNil(issue)
	require.Len(issue.Labels, 1)
	assert.Equal("old", issue.Labels[0].Name)
}

func TestLabelMergePreservesCatalogMembership(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	repoID := insertTestRepo(t, d, "acme", "widget")
	now := baseTime()
	issueID := insertTestIssueWithOptions(t, d, testIssue(repoID, 7, withIssueTitle("issue")))

	require.NoError(d.ReplaceIssueLabels(ctx, repoID, issueID, []Label{{
		PlatformID: 7,
		Name:       "provider-label",
		Color:      "cccccc",
		UpdatedAt:  now,
	}}))
	require.NoError(d.ReplaceRepoLabelCatalog(ctx, repoID, []Label{{
		Name:      "bug",
		Color:     "d73a4a",
		UpdatedAt: now,
	}}, now))

	require.NoError(d.UpsertLabels(ctx, repoID, []Label{{
		PlatformID: 7,
		Name:       "bug",
		Color:      "d73a4a",
		UpdatedAt:  now.Add(time.Minute),
	}}))

	catalog, _, err := d.ListRepoLabelCatalog(ctx, repoID)
	require.NoError(err)
	require.Len(catalog, 1)
	assert.Equal("bug", catalog[0].Name)
	assert.True(catalog[0].CatalogPresent)
	require.NotNil(catalog[0].CatalogSeenAt)
	assert.Equal(now, *catalog[0].CatalogSeenAt)
}

func TestRepoMergePreservesLabelCatalogFreshnessAndMembership(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	syncedAt := baseTime()

	sourceID, err := d.UpsertRepoByProviderID(ctx, RepoIdentity{
		Platform:       "gitlab",
		PlatformHost:   "gitlab.com",
		PlatformRepoID: "gid://gitlab/Project/42",
		Owner:          "old-group",
		Name:           "old-name",
	})
	require.NoError(err)
	require.NoError(d.ReplaceRepoLabelCatalog(ctx, sourceID, []Label{{
		PlatformID: 300,
		Name:       "bug",
		Color:      "d73a4a",
		UpdatedAt:  syncedAt,
	}}, syncedAt))

	destinationID, err := d.UpsertRepo(ctx, RepoIdentity{
		Platform:     "gitlab",
		PlatformHost: "gitlab.com",
		Owner:        "new-group",
		Name:         "new-name",
	})
	require.NoError(err)
	require.NoError(d.UpsertLabels(ctx, destinationID, []Label{{
		PlatformID: 300,
		Name:       "bug",
		Color:      "b60205",
		UpdatedAt:  syncedAt.Add(time.Minute),
	}}))

	gotID, err := d.UpsertRepoByProviderID(ctx, RepoIdentity{
		Platform:       "gitlab",
		PlatformHost:   "gitlab.com",
		PlatformRepoID: "gid://gitlab/Project/42",
		Owner:          "new-group",
		Name:           "new-name",
	})
	require.NoError(err)
	assert.Equal(destinationID, gotID)

	catalog, freshness, err := d.ListRepoLabelCatalog(ctx, destinationID)
	require.NoError(err)
	require.Len(catalog, 1)
	assert.Equal("bug", catalog[0].Name)
	assert.True(catalog[0].CatalogPresent)
	require.NotNil(freshness.SyncedAt)
	assert.Equal(syncedAt, *freshness.SyncedAt)
}

func TestLabelMergeDoesNotLetItemRowOverwriteCatalogMetadata(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	repoID := insertTestRepo(t, d, "acme", "widget")
	catalogAt := baseTime()
	itemAt := catalogAt.Add(time.Hour)

	require.NoError(d.ReplaceRepoLabelCatalog(ctx, repoID, []Label{{
		PlatformID:  7,
		Name:        "catalog",
		Description: "catalog metadata",
		Color:       "0e8a16",
		UpdatedAt:   catalogAt,
	}}, catalogAt))
	require.NoError(d.UpsertLabels(ctx, repoID, []Label{{Name: "stale", Description: "item metadata", Color: "d73a4a", UpdatedAt: itemAt}}))
	require.NoError(d.UpsertLabels(ctx, repoID, []Label{{PlatformID: 7, Name: "stale", Description: "item metadata", Color: "d73a4a", UpdatedAt: itemAt}}))

	catalog, _, err := d.ListRepoLabelCatalog(ctx, repoID)
	require.NoError(err)
	require.Len(catalog, 1)
	assert.Equal("catalog", catalog[0].Name)
	assert.Equal("catalog metadata", catalog[0].Description)
	assert.Equal("0e8a16", catalog[0].Color)
}

func TestCatalogMetadataOverridesItemMetadata(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	repoID := insertTestRepo(t, d, "acme", "widget")
	issueID := insertTestIssueWithOptions(t, d, testIssue(repoID, 7))
	catalogUpdated := baseTime()
	itemUpdated := catalogUpdated.Add(time.Hour)
	syncedAt := itemUpdated.Add(time.Hour)

	require.NoError(d.ReplaceIssueLabels(ctx, repoID, issueID, []Label{{
		PlatformID:  7,
		Name:        "bug",
		Description: "item snapshot",
		Color:       "cccccc",
		IsDefault:   false,
		UpdatedAt:   itemUpdated,
	}}))
	require.NoError(d.ReplaceRepoLabelCatalog(ctx, repoID, []Label{{
		PlatformID:  7,
		Name:        "bug",
		Description: "catalog",
		Color:       "0e8a16",
		IsDefault:   true,
		UpdatedAt:   catalogUpdated,
	}}, syncedAt))
	require.NoError(d.ReplaceIssueLabels(ctx, repoID, issueID, []Label{{
		PlatformID:  7,
		Name:        "bug",
		Description: "newer item snapshot",
		Color:       "d73a4a",
		UpdatedAt:   syncedAt.Add(time.Hour),
	}}))

	catalog, _, err := d.ListRepoLabelCatalog(ctx, repoID)
	require.NoError(err)
	require.Len(catalog, 1)
	assert.Equal("catalog", catalog[0].Description)
	assert.Equal("0e8a16", catalog[0].Color)
	assert.True(catalog[0].IsDefault)
}

func TestLabelMergeKeepsFresherCatalogMetadata(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	repoID := insertTestRepo(t, d, "acme", "widget")
	oldSeenAt := baseTime()
	newSeenAt := oldSeenAt.Add(time.Hour)

	require.NoError(d.ReplaceRepoLabelCatalog(ctx, repoID, []Label{{
		PlatformID: 7,
		Name:       "provider-label",
		Color:      "cccccc",
		UpdatedAt:  oldSeenAt,
	}}, oldSeenAt))
	require.NoError(d.ReplaceRepoLabelCatalog(ctx, repoID, []Label{{
		Name:        "bug",
		Description: "fresh catalog",
		Color:       "0e8a16",
		UpdatedAt:   newSeenAt,
	}}, newSeenAt))

	require.NoError(d.UpsertLabels(ctx, repoID, []Label{{
		PlatformID:  7,
		Name:        "bug",
		Description: "stale item",
		Color:       "d73a4a",
		UpdatedAt:   oldSeenAt,
	}}))

	catalog, _, err := d.ListRepoLabelCatalog(ctx, repoID)
	require.NoError(err)
	require.Len(catalog, 1)
	require.NotNil(catalog[0].CatalogSeenAt)
	assert.Equal(newSeenAt, *catalog[0].CatalogSeenAt)
	assert.Equal("bug", catalog[0].Name)
	assert.Equal("fresh catalog", catalog[0].Description)
	assert.Equal("0e8a16", catalog[0].Color)
}

func TestRepoMergeUsesNewerSourceLabelCatalogFreshness(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	oldCheck := baseTime()
	newSync := oldCheck.Add(time.Hour)

	sourceID, err := d.UpsertRepoByProviderID(ctx, RepoIdentity{
		Platform:       "gitlab",
		PlatformHost:   "gitlab.com",
		PlatformRepoID: "gid://gitlab/Project/42",
		Owner:          "old-group",
		Name:           "old-name",
	})
	require.NoError(err)
	require.NoError(d.ReplaceRepoLabelCatalog(ctx, sourceID, []Label{{
		PlatformID: 300,
		Name:       "bug",
		Color:      "d73a4a",
		UpdatedAt:  newSync,
	}}, newSync))

	destinationID, err := d.UpsertRepo(ctx, RepoIdentity{
		Platform:     "gitlab",
		PlatformHost: "gitlab.com",
		Owner:        "new-group",
		Name:         "new-name",
	})
	require.NoError(err)
	require.NoError(d.UpdateRepoLabelCatalogCheck(ctx, destinationID, oldCheck, "provider down"))

	_, err = d.UpsertRepoByProviderID(ctx, RepoIdentity{
		Platform:       "gitlab",
		PlatformHost:   "gitlab.com",
		PlatformRepoID: "gid://gitlab/Project/42",
		Owner:          "new-group",
		Name:           "new-name",
	})
	require.NoError(err)

	_, freshness, err := d.ListRepoLabelCatalog(ctx, destinationID)
	require.NoError(err)
	require.NotNil(freshness.SyncedAt)
	assert.Equal(newSync, *freshness.SyncedAt)
	require.NotNil(freshness.CheckedAt)
	assert.Equal(newSync, *freshness.CheckedAt)
	assert.Empty(freshness.SyncError)
}

func TestRepoMergeUsesFresherSourceCatalogMembership(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	oldSync := baseTime()
	newSync := oldSync.Add(time.Hour)

	sourceID, err := d.UpsertRepoByProviderID(ctx, RepoIdentity{
		Platform:       "gitlab",
		PlatformHost:   "gitlab.com",
		PlatformRepoID: "gid://gitlab/Project/42",
		Owner:          "old-group",
		Name:           "old-name",
	})
	require.NoError(err)
	require.NoError(d.ReplaceRepoLabelCatalog(ctx, sourceID, []Label{{Name: "bug", Color: "d73a4a", UpdatedAt: newSync}}, newSync))

	destinationID, err := d.UpsertRepo(ctx, RepoIdentity{Platform: "gitlab", PlatformHost: "gitlab.com", Owner: "new-group", Name: "new-name"})
	require.NoError(err)
	require.NoError(d.ReplaceRepoLabelCatalog(ctx, destinationID, []Label{{Name: "old-destination", Color: "cccccc", UpdatedAt: oldSync}}, oldSync))

	_, err = d.UpsertRepoByProviderID(ctx, RepoIdentity{
		Platform:       "gitlab",
		PlatformHost:   "gitlab.com",
		PlatformRepoID: "gid://gitlab/Project/42",
		Owner:          "new-group",
		Name:           "new-name",
	})
	require.NoError(err)

	catalog, _, err := d.ListRepoLabelCatalog(ctx, destinationID)
	require.NoError(err)
	require.Len(catalog, 1)
	assert.Equal("bug", catalog[0].Name)
}

func TestRepoMergeUsesFresherDestinationCatalogMembership(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	oldSync := baseTime()
	newSync := oldSync.Add(time.Hour)

	sourceID, err := d.UpsertRepoByProviderID(ctx, RepoIdentity{
		Platform:       "gitlab",
		PlatformHost:   "gitlab.com",
		PlatformRepoID: "gid://gitlab/Project/42",
		Owner:          "old-group",
		Name:           "old-name",
	})
	require.NoError(err)
	require.NoError(d.ReplaceRepoLabelCatalog(ctx, sourceID, []Label{{Name: "old-source", Color: "cccccc", UpdatedAt: oldSync}}, oldSync))

	destinationID, err := d.UpsertRepo(ctx, RepoIdentity{Platform: "gitlab", PlatformHost: "gitlab.com", Owner: "new-group", Name: "new-name"})
	require.NoError(err)
	require.NoError(d.ReplaceRepoLabelCatalog(ctx, destinationID, []Label{{Name: "triage", Color: "fbca04", UpdatedAt: newSync}}, newSync))

	_, err = d.UpsertRepoByProviderID(ctx, RepoIdentity{
		Platform:       "gitlab",
		PlatformHost:   "gitlab.com",
		PlatformRepoID: "gid://gitlab/Project/42",
		Owner:          "new-group",
		Name:           "new-name",
	})
	require.NoError(err)

	catalog, _, err := d.ListRepoLabelCatalog(ctx, destinationID)
	require.NoError(err)
	require.Len(catalog, 1)
	assert.Equal("triage", catalog[0].Name)
}

func TestRepoMergeKeepsSuccessfulSyncWhenNewerCheckFailed(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	oldSync := baseTime()
	newCheck := oldSync.Add(time.Hour)

	sourceID, err := d.UpsertRepoByProviderID(ctx, RepoIdentity{
		Platform:       "gitlab",
		PlatformHost:   "gitlab.com",
		PlatformRepoID: "gid://gitlab/Project/42",
		Owner:          "old-group",
		Name:           "old-name",
	})
	require.NoError(err)
	require.NoError(d.UpdateRepoLabelCatalogCheck(ctx, sourceID, newCheck, "provider down"))

	destinationID, err := d.UpsertRepo(ctx, RepoIdentity{Platform: "gitlab", PlatformHost: "gitlab.com", Owner: "new-group", Name: "new-name"})
	require.NoError(err)
	require.NoError(d.ReplaceRepoLabelCatalog(ctx, destinationID, []Label{{Name: "bug", Color: "d73a4a", UpdatedAt: oldSync}}, oldSync))

	_, err = d.UpsertRepoByProviderID(ctx, RepoIdentity{
		Platform:       "gitlab",
		PlatformHost:   "gitlab.com",
		PlatformRepoID: "gid://gitlab/Project/42",
		Owner:          "new-group",
		Name:           "new-name",
	})
	require.NoError(err)

	_, freshness, err := d.ListRepoLabelCatalog(ctx, destinationID)
	require.NoError(err)
	require.NotNil(freshness.SyncedAt)
	assert.Equal(oldSync, *freshness.SyncedAt)
	require.NotNil(freshness.CheckedAt)
	assert.Equal(newCheck, *freshness.CheckedAt)
	assert.Equal("provider down", freshness.SyncError)
}

func TestRepoMergeCopiesFresherDuplicateLabelMetadata(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	oldSync := baseTime()
	newSync := oldSync.Add(time.Hour)

	sourceID, err := d.UpsertRepoByProviderID(ctx, RepoIdentity{Platform: "gitlab", PlatformHost: "gitlab.com", PlatformRepoID: "gid://gitlab/Project/42", Owner: "old-group", Name: "old-name"})
	require.NoError(err)
	require.NoError(d.ReplaceRepoLabelCatalog(ctx, sourceID, []Label{{PlatformID: 300, Name: "new-name", Description: "new", Color: "0e8a16", IsDefault: true, UpdatedAt: newSync}}, newSync))

	destinationID, err := d.UpsertRepo(ctx, RepoIdentity{Platform: "gitlab", PlatformHost: "gitlab.com", Owner: "new-group", Name: "new-name"})
	require.NoError(err)
	require.NoError(d.ReplaceRepoLabelCatalog(ctx, destinationID, []Label{{PlatformID: 300, Name: "old-name", Description: "old", Color: "cccccc", UpdatedAt: oldSync}}, oldSync))

	_, err = d.UpsertRepoByProviderID(ctx, RepoIdentity{Platform: "gitlab", PlatformHost: "gitlab.com", PlatformRepoID: "gid://gitlab/Project/42", Owner: "new-group", Name: "new-name"})
	require.NoError(err)

	catalog, _, err := d.ListRepoLabelCatalog(ctx, destinationID)
	require.NoError(err)
	require.Len(catalog, 1)
	assert.Equal("new-name", catalog[0].Name)
	assert.Equal("new", catalog[0].Description)
	assert.Equal("0e8a16", catalog[0].Color)
	assert.True(catalog[0].IsDefault)
}

func TestRepoMergeCoalescesPlatformExternalIDDuplicateLabels(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	oldSync := baseTime()
	newSync := oldSync.Add(time.Hour)
	externalID := "gid://gitlab/ProjectLabel/300"

	sourceID, err := d.UpsertRepoByProviderID(ctx, RepoIdentity{Platform: "gitlab", PlatformHost: "gitlab.com", PlatformRepoID: "gid://gitlab/Project/42", Owner: "old-group", Name: "old-name"})
	require.NoError(err)
	require.NoError(d.ReplaceRepoLabelCatalog(ctx, sourceID, []Label{{PlatformExternalID: externalID, Name: "new-name", Description: "fresh", Color: "0e8a16", UpdatedAt: newSync}}, newSync))

	destinationID, err := d.UpsertRepo(ctx, RepoIdentity{Platform: "gitlab", PlatformHost: "gitlab.com", Owner: "new-group", Name: "new-name"})
	require.NoError(err)
	require.NoError(d.ReplaceRepoLabelCatalog(ctx, destinationID, []Label{{PlatformExternalID: externalID, Name: "old-name", Description: "stale", Color: "cccccc", UpdatedAt: oldSync}}, oldSync))

	_, err = d.UpsertRepoByProviderID(ctx, RepoIdentity{Platform: "gitlab", PlatformHost: "gitlab.com", PlatformRepoID: "gid://gitlab/Project/42", Owner: "new-group", Name: "new-name"})
	require.NoError(err)

	catalog, _, err := d.ListRepoLabelCatalog(ctx, destinationID)
	require.NoError(err)
	require.Len(catalog, 1)
	assert.Equal("new-name", catalog[0].Name)
	assert.Equal("fresh", catalog[0].Description)
	assert.Equal("0e8a16", catalog[0].Color)
}

func TestRepoMergeCoalescesDestinationNameConflictBeforeCatalogRename(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	oldSync := baseTime()
	newSync := oldSync.Add(time.Hour)

	sourceID, err := d.UpsertRepoByProviderID(ctx, RepoIdentity{Platform: "gitlab", PlatformHost: "gitlab.com", PlatformRepoID: "gid://gitlab/Project/42", Owner: "old-group", Name: "old-name"})
	require.NoError(err)
	require.NoError(d.ReplaceRepoLabelCatalog(ctx, sourceID, []Label{{PlatformID: 300, Name: "new-name", Description: "fresh", Color: "0e8a16", UpdatedAt: newSync}}, newSync))

	destinationID, err := d.UpsertRepo(ctx, RepoIdentity{Platform: "gitlab", PlatformHost: "gitlab.com", Owner: "new-group", Name: "new-name"})
	require.NoError(err)
	require.NoError(d.ReplaceRepoLabelCatalog(ctx, destinationID, []Label{{PlatformID: 300, Name: "old-name", Description: "stale", Color: "cccccc", UpdatedAt: oldSync}}, oldSync))
	require.NoError(d.UpsertLabels(ctx, destinationID, []Label{{Name: "new-name", Description: "historical", Color: "d73a4a", UpdatedAt: oldSync}}))

	_, err = d.UpsertRepoByProviderID(ctx, RepoIdentity{Platform: "gitlab", PlatformHost: "gitlab.com", PlatformRepoID: "gid://gitlab/Project/42", Owner: "new-group", Name: "new-name"})
	require.NoError(err)

	catalog, _, err := d.ListRepoLabelCatalog(ctx, destinationID)
	require.NoError(err)
	require.Len(catalog, 1)
	assert.Equal("new-name", catalog[0].Name)
	assert.Equal("fresh", catalog[0].Description)
	assert.Equal("0e8a16", catalog[0].Color)
}

func TestRepoMergeCopiesFresherNameOnlyDuplicateLabelMetadata(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	oldSync := baseTime()
	newSync := oldSync.Add(time.Hour)

	sourceID, err := d.UpsertRepoByProviderID(ctx, RepoIdentity{Platform: "gitlab", PlatformHost: "gitlab.com", PlatformRepoID: "gid://gitlab/Project/42", Owner: "old-group", Name: "old-name"})
	require.NoError(err)
	require.NoError(d.ReplaceRepoLabelCatalog(ctx, sourceID, []Label{{Name: "triage", Description: "fresh", Color: "0e8a16", IsDefault: true, UpdatedAt: newSync}}, newSync))

	destinationID, err := d.UpsertRepo(ctx, RepoIdentity{Platform: "gitlab", PlatformHost: "gitlab.com", Owner: "new-group", Name: "new-name"})
	require.NoError(err)
	require.NoError(d.ReplaceRepoLabelCatalog(ctx, destinationID, []Label{{Name: "triage", Description: "stale", Color: "cccccc", UpdatedAt: oldSync}}, oldSync))

	_, err = d.UpsertRepoByProviderID(ctx, RepoIdentity{Platform: "gitlab", PlatformHost: "gitlab.com", PlatformRepoID: "gid://gitlab/Project/42", Owner: "new-group", Name: "new-name"})
	require.NoError(err)

	catalog, _, err := d.ListRepoLabelCatalog(ctx, destinationID)
	require.NoError(err)
	require.Len(catalog, 1)
	assert.Equal("triage", catalog[0].Name)
	assert.Equal("fresh", catalog[0].Description)
	assert.Equal("0e8a16", catalog[0].Color)
	assert.True(catalog[0].IsDefault)
}

func TestRepoLabelCatalogWritesIgnoreStaleResults(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	repoID := insertTestRepo(t, d, "acme", "widget")
	oldTime := baseTime()
	newTime := oldTime.Add(time.Hour)

	require.NoError(d.ReplaceRepoLabelCatalog(ctx, repoID, []Label{{Name: "new", Color: "0e8a16", UpdatedAt: newTime}}, newTime))
	require.NoError(d.ReplaceRepoLabelCatalog(ctx, repoID, []Label{{Name: "old", Color: "cccccc", UpdatedAt: oldTime}}, oldTime))
	catalog, freshness, err := d.ListRepoLabelCatalog(ctx, repoID)
	require.NoError(err)
	require.Len(catalog, 1)
	assert.Equal("new", catalog[0].Name)
	require.NotNil(freshness.SyncedAt)
	assert.Equal(newTime, *freshness.SyncedAt)

	require.NoError(d.UpdateRepoLabelCatalogCheck(ctx, repoID, oldTime, "old failure"))
	_, freshness, err = d.ListRepoLabelCatalog(ctx, repoID)
	require.NoError(err)
	assert.Empty(freshness.SyncError)
	require.NotNil(freshness.CheckedAt)
	assert.Equal(newTime, *freshness.CheckedAt)
}

func TestRepoLabelCatalogOlderSuccessKeepsNewerFailedCheck(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	repoID := insertTestRepo(t, d, "acme", "widget")
	oldSuccess := baseTime()
	newFailure := oldSuccess.Add(time.Hour)

	require.NoError(d.UpdateRepoLabelCatalogCheck(ctx, repoID, newFailure, "provider down"))
	require.NoError(d.ReplaceRepoLabelCatalog(ctx, repoID, []Label{{Name: "bug", Color: "d73a4a", UpdatedAt: oldSuccess}}, oldSuccess))

	catalog, freshness, err := d.ListRepoLabelCatalog(ctx, repoID)
	require.NoError(err)
	require.Len(catalog, 1)
	assert.Equal("bug", catalog[0].Name)
	require.NotNil(freshness.SyncedAt)
	assert.Equal(oldSuccess, *freshness.SyncedAt)
	require.NotNil(freshness.CheckedAt)
	assert.Equal(newFailure, *freshness.CheckedAt)
	assert.Equal("provider down", freshness.SyncError)
}

func TestRepoLabelCatalogFreshnessTracksCheckedSyncedAndErrors(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	repoID := insertTestRepo(t, d, "acme", "widget")
	checked := baseTime()
	synced := checked.Add(time.Second)

	require.NoError(d.UpdateRepoLabelCatalogCheck(ctx, repoID, checked, "provider down"))
	_, freshness, err := d.ListRepoLabelCatalog(ctx, repoID)
	require.NoError(err)
	require.NotNil(freshness.CheckedAt)
	assert.Equal(checked, *freshness.CheckedAt)
	assert.Nil(freshness.SyncedAt)
	assert.Equal("provider down", freshness.SyncError)

	require.NoError(d.MarkRepoLabelCatalogSynced(ctx, repoID, synced))
	_, freshness, err = d.ListRepoLabelCatalog(ctx, repoID)
	require.NoError(err)
	require.NotNil(freshness.SyncedAt)
	assert.Equal(synced, *freshness.SyncedAt)
	assert.Empty(freshness.SyncError)
}

func TestUpsertRepoCasefoldsOwnerAndName(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()

	id, err := d.UpsertRepo(ctx, GitHubRepoIdentity("github.com", "Org", "Foo"))
	require.NoError(err)

	sameID, err := d.UpsertRepo(ctx, GitHubRepoIdentity("github.com", "org", "foo"))
	require.NoError(err)
	assert.Equal(id, sameID)

	repos, err := d.ListRepos(ctx)
	require.NoError(err)
	require.Len(repos, 1)
	assert.Equal("org", repos[0].Owner)
	assert.Equal("foo", repos[0].Name)
}

func TestGetRepoByOwnerName(t *testing.T) {
	assert := Assert.New(t)
	d := openTestDB(t)
	ctx := t.Context()

	id := insertTestRepo(t, d, "owner", "repo")

	r, err := d.GetRepoByOwnerName(ctx, "owner", "repo")
	require.NoError(t, err)
	require.NotNil(t, r)
	assert.Equal(id, r.ID)

	missing, err := d.GetRepoByOwnerName(ctx, "no", "such")
	require.NoError(t, err)
	assert.Nil(missing)
}

func TestGetRepoByOwnerNameUsesLookupKeysForNonGitHubRows(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()

	firstID, err := d.UpsertRepo(ctx, RepoIdentity{
		Platform:     "gitlab",
		PlatformHost: "gitlab-a.example.com",
		Owner:        "Group/SubGroup",
		Name:         "ProjectName",
		RepoPath:     "Group/SubGroup/ProjectName",
	})
	require.NoError(err)
	secondID, err := d.UpsertRepo(ctx, RepoIdentity{
		Platform:     "gitlab",
		PlatformHost: "gitlab-b.example.com",
		Owner:        "GROUP/SubGroup",
		Name:         "PROJECTName",
		RepoPath:     "GROUP/SubGroup/PROJECTName",
	})
	require.NoError(err)
	assert.NotEqual(firstID, secondID)

	repo, err := d.GetRepoByOwnerName(ctx, "group/subgroup", "projectname")
	require.NoError(err)
	require.NotNil(repo)
	assert.Equal(firstID, repo.ID)
	assert.Equal("gitlab-a.example.com", repo.PlatformHost)
	assert.Equal("Group/SubGroup", repo.Owner)
	assert.Equal("ProjectName", repo.Name)
}

func TestUpdateRepoSync(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()

	id := insertTestRepo(t, d, "o", "r")
	now := baseTime()

	require.NoError(d.UpdateRepoSyncStarted(ctx, id, now))
	later := now.Add(time.Minute)
	require.NoError(d.UpdateRepoSyncCompleted(ctx, id, later, ""))

	r, err := d.GetRepoByOwnerName(ctx, "o", "r")
	require.NoError(err)
	require.NotNil(r)
	require.NotNil(r.LastSyncStartedAt)
	require.NotNil(r.LastSyncCompletedAt)
	assert.True(r.LastSyncStartedAt.Equal(now))
	assert.True(r.LastSyncCompletedAt.Equal(later))
	assert.Empty(r.LastSyncError)

	// Record a sync error.
	require.NoError(d.UpdateRepoSyncCompleted(ctx, id, later, "rate limited"))
	r2, _ := d.GetRepoByOwnerName(ctx, "o", "r")
	require.NotNil(r2)
	assert.Equal("rate limited", r2.LastSyncError)
}

func TestUpsertAndGetPullRequest(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()

	repoID := insertTestRepo(t, d, "owner", "repo")
	now := baseTime()

	pr := &MergeRequest{
		RepoID:         repoID,
		PlatformID:     42,
		Number:         7,
		URL:            "https://github.com/owner/repo/pull/7",
		Title:          "fix: something",
		Author:         "alice",
		State:          "open",
		IsDraft:        false,
		IsLocked:       true,
		Body:           "body text",
		HeadBranch:     "fix/something",
		BaseBranch:     "main",
		Additions:      10,
		Deletions:      3,
		CommentCount:   2,
		ReviewDecision: "APPROVED",
		CIStatus:       "SUCCESS",
		CreatedAt:      now,
		UpdatedAt:      now,
		LastActivityAt: now,
	}

	id, err := d.UpsertMergeRequest(ctx, pr)
	require.NoError(err)
	assert.NotZero(id)

	got, err := d.GetMergeRequest(ctx, "owner", "repo", 7)
	require.NoError(err)
	require.NotNil(got)
	assert.Equal(id, got.ID)
	assert.Equal(pr.Title, got.Title)
	assert.Equal(pr.Author, got.Author)
	assert.Equal(pr.Additions, got.Additions)
	assert.True(got.IsLocked)
	assert.Empty(got.KanbanStatus)

	// Update via upsert — change title and additions.
	pr.Title = "fix: something updated"
	pr.Additions = 20
	pr.UpdatedAt = now.Add(time.Hour)
	pr.LastActivityAt = now.Add(time.Hour)

	id2, err := d.UpsertMergeRequest(ctx, pr)
	require.NoError(err)
	assert.Equal(id, id2)

	got2, _ := d.GetMergeRequest(ctx, "owner", "repo", 7)
	require.NotNil(got2)
	assert.Equal("fix: something updated", got2.Title)
	assert.Equal(20, got2.Additions)
	assert.True(got2.IsLocked)
	// created_at must not change.
	assert.True(got2.CreatedAt.Equal(now))

	// Missing PR returns nil.
	missing, err := d.GetMergeRequest(ctx, "owner", "repo", 999)
	require.NoError(err)
	assert.Nil(missing)
}

func TestListPullRequests(t *testing.T) {
	d := openTestDB(t)

	repoID := insertTestRepo(t, d, "owner", "repo")
	base := baseTime()

	// Insert 3 PRs with different last_activity_at.
	insertTestMR(t, d, repoID, 1, "oldest", base)
	insertTestMR(t, d, repoID, 2, "middle", base.Add(time.Hour))
	insertTestMR(t, d, repoID, 3, "newest", base.Add(2*time.Hour))

	prs, err := d.ListMergeRequests(t.Context(), ListMergeRequestsOpts{})
	require.NoError(t, err)
	require.Len(t, prs, 3)
	// Newest first.
	Assert.Equal(t, []int{3, 2, 1}, []int{prs[0].Number, prs[1].Number, prs[2].Number})
}

func TestListPullRequestsFilterByRepo(t *testing.T) {
	d := openTestDB(t)

	repo1 := insertTestRepo(t, d, "owner", "repo1")
	repo2 := insertTestRepo(t, d, "owner", "repo2")
	base := baseTime()

	insertTestMR(t, d, repo1, 1, "pr in repo1", base)
	insertTestMR(t, d, repo2, 1, "pr in repo2", base)

	prs, err := d.ListMergeRequests(t.Context(), ListMergeRequestsOpts{RepoOwner: "owner", RepoName: "repo1"})
	require.NoError(t, err)
	require.Len(t, prs, 1)
	Assert.Equal(t, repo1, prs[0].RepoID)
}

func TestListPullRequestsFilterByRepoIncludesAllHostsByDefault(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	base := baseTime()

	githubRepo := insertTestRepo(t, d, "owner", "repo")
	enterpriseRepo := insertTestRepoWithHost(
		t, d, "owner", "repo", "ghe.example.com",
	)
	insertTestMR(t, d, githubRepo, 1, "github pr", base)
	insertTestMR(t, d, enterpriseRepo, 2, "enterprise pr", base.Add(time.Hour))

	allHosts, err := d.ListMergeRequests(ctx, ListMergeRequestsOpts{
		RepoOwner: "owner",
		RepoName:  "repo",
	})
	require.NoError(err)
	require.Len(allHosts, 2)
	assert.Equal([]int64{enterpriseRepo, githubRepo}, []int64{
		allHosts[0].RepoID,
		allHosts[1].RepoID,
	})

	enterpriseOnly, err := d.ListMergeRequests(ctx, ListMergeRequestsOpts{
		PlatformHost: "ghe.example.com",
		RepoOwner:    "owner",
		RepoName:     "repo",
	})
	require.NoError(err)
	require.Len(enterpriseOnly, 1)
	assert.Equal(enterpriseRepo, enterpriseOnly[0].RepoID)
}

func TestPullRequestRepoScopedQueriesCanonicalizeOwnerName(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()

	repoID := insertTestRepo(t, d, "owner", "repo")
	prID := insertTestMR(t, d, repoID, 7, "mixed case path", baseTime())
	require.NoError(d.UpdateDiffSHAs(ctx, repoID, 7, "head", "base", "merge"))

	got, err := d.GetMergeRequest(ctx, "Owner", "Repo", 7)
	require.NoError(err)
	require.NotNil(got)
	assert.Equal(prID, got.ID)

	filtered, err := d.ListMergeRequests(ctx, ListMergeRequestsOpts{
		RepoOwner: "Owner",
		RepoName:  "Repo",
	})
	require.NoError(err)
	require.Len(filtered, 1)
	assert.Equal(prID, filtered[0].ID)

	gotID, err := d.GetMRIDByRepoAndNumber(ctx, "Owner", "Repo", 7)
	require.NoError(err)
	assert.Equal(prID, gotID)

	shas, err := d.GetDiffSHAs(ctx, "Owner", "Repo", 7)
	require.NoError(err)
	require.NotNil(shas)
	assert.Equal("head", shas.DiffHeadSHA)
}

func TestListPullRequestsFilterBySearch(t *testing.T) {
	d := openTestDB(t)

	repoID := insertTestRepo(t, d, "owner", "repo")
	base := baseTime()

	insertTestMR(t, d, repoID, 1, "add feature", base)
	insertTestMR(t, d, repoID, 2, "fix bug", base.Add(time.Hour))

	prs, err := d.ListMergeRequests(t.Context(), ListMergeRequestsOpts{Search: "feature"})
	require.NoError(t, err)
	require.Len(t, prs, 1)
	Assert.Equal(t, 1, prs[0].Number)
}

func TestListPullRequestsFilterBySearchNumber(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)
	d := openTestDB(t)

	repoID := insertTestRepo(t, d, "owner", "repo")
	base := baseTime()

	insertTestMR(t, d, repoID, 12, "add feature", base)
	insertTestMR(t, d, repoID, 278, "fix bug", base.Add(time.Hour))
	insertTestMR(t, d, repoID, 290, "another change", base.Add(2*time.Hour))

	prs, err := d.ListMergeRequests(t.Context(), ListMergeRequestsOpts{Search: "278"})
	require.NoError(err)
	require.Len(prs, 1)
	assert.Equal(278, prs[0].Number)

	prs, err = d.ListMergeRequests(t.Context(), ListMergeRequestsOpts{Search: "#278"})
	require.NoError(err)
	require.Len(prs, 1)
	assert.Equal(278, prs[0].Number)

	// Substring of number matches multiple.
	prs, err = d.ListMergeRequests(t.Context(), ListMergeRequestsOpts{Search: "2"})
	require.NoError(err)
	require.Len(prs, 3)
}

func TestListPullRequestsFilterBySearchLabel(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)
	d := openTestDB(t)
	ctx := t.Context()

	repoID := insertTestRepo(t, d, "owner", "repo")
	base := baseTime()

	insertTestMR(t, d, repoID, 1, "add feature", base)
	prID := insertTestMR(t, d, repoID, 2, "fix bug", base.Add(time.Hour))
	require.NoError(d.ReplaceMergeRequestLabels(ctx, repoID, prID, []Label{{
		PlatformID: 200,
		Name:       "needs-review",
		Color:      "fbca04",
		UpdatedAt:  base,
	}}))

	prs, err := d.ListMergeRequests(ctx, ListMergeRequestsOpts{Search: "needs-review"})
	require.NoError(err)
	require.Len(prs, 1)
	assert.Equal(2, prs[0].Number)
}

func TestListPullRequestsFilterByKanban(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()

	repoID := insertTestRepo(t, d, "owner", "repo")
	base := baseTime()

	id1 := insertTestMR(t, d, repoID, 1, "pr 1", base)
	id2 := insertTestMR(t, d, repoID, 2, "pr 2", base.Add(time.Hour))
	id3 := insertTestMR(t, d, repoID, 3, "pr 3", base.Add(2*time.Hour))

	// Set PR 2 to "reviewing".
	require.NoError(d.SetKanbanState(ctx, id2, "reviewing"))
	// Ensure kanban for PR 1 and 3 (status = "new").
	require.NoError(d.EnsureKanbanState(ctx, id1))
	require.NoError(d.EnsureKanbanState(ctx, id3))

	prs, err := d.ListMergeRequests(ctx, ListMergeRequestsOpts{KanbanState: "reviewing"})
	require.NoError(err)
	require.Len(prs, 1)
	assert.Equal(2, prs[0].Number)
	assert.Equal(KanbanStatusReviewing, prs[0].KanbanStatus)
}

func TestListMergeRequests_AttachesLabels(t *testing.T) {
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	now := baseTime()

	repoID, err := d.UpsertRepo(ctx, GitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)

	_, err = d.UpsertMergeRequest(ctx, &MergeRequest{
		RepoID:         repoID,
		PlatformID:     101,
		Number:         7,
		URL:            "https://github.com/acme/widget/pull/7",
		Title:          "Add labels",
		Author:         "alice",
		State:          "open",
		CreatedAt:      now,
		UpdatedAt:      now,
		LastActivityAt: now,
	})
	require.NoError(err)

	mrID, err := d.GetMRIDByRepoAndNumber(ctx, "acme", "widget", 7)
	require.NoError(err)
	require.NoError(d.ReplaceMergeRequestLabels(ctx, repoID, mrID, []Label{{
		PlatformID:  5001,
		Name:        "needs-review",
		Description: "Needs another reviewer",
		Color:       "fbca04",
		IsDefault:   true,
		UpdatedAt:   now,
	}}))

	mrs, err := d.ListMergeRequests(ctx, ListMergeRequestsOpts{})
	require.NoError(err)
	require.Len(mrs, 1)
	require.Len(mrs[0].Labels, 1)
	require.Equal("needs-review", mrs[0].Labels[0].Name)
	require.Equal("Needs another reviewer", mrs[0].Labels[0].Description)
	require.Equal("fbca04", mrs[0].Labels[0].Color)
	require.True(mrs[0].Labels[0].IsDefault)
	require.True(mrs[0].Labels[0].UpdatedAt.Equal(now))
}

func TestGetMergeRequest_AttachesLabels(t *testing.T) {
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	now := baseTime()

	repoID := insertTestRepo(t, d, "acme", "widget")
	_, err := d.UpsertMergeRequest(ctx, &MergeRequest{
		RepoID:         repoID,
		PlatformID:     102,
		Number:         8,
		URL:            "https://github.com/acme/widget/pull/8",
		Title:          "Detail labels",
		Author:         "alice",
		State:          "open",
		CreatedAt:      now,
		UpdatedAt:      now,
		LastActivityAt: now,
	})
	require.NoError(err)

	mrID, err := d.GetMRIDByRepoAndNumber(ctx, "acme", "widget", 8)
	require.NoError(err)
	require.NoError(d.ReplaceMergeRequestLabels(ctx, repoID, mrID, []Label{{
		PlatformID:  5002,
		Name:        "backend",
		Description: "Backend changes",
		Color:       "5319e7",
		IsDefault:   false,
		UpdatedAt:   now,
	}}))

	mr, err := d.GetMergeRequest(ctx, "acme", "widget", 8)
	require.NoError(err)
	require.NotNil(mr)
	require.Len(mr.Labels, 1)
	require.Equal("backend", mr.Labels[0].Name)
	require.Equal("Backend changes", mr.Labels[0].Description)
	require.Equal("5319e7", mr.Labels[0].Color)
	require.False(mr.Labels[0].IsDefault)
	require.True(mr.Labels[0].UpdatedAt.Equal(now))
}

func TestReplaceMergeRequestLabels_RejectsWrongRepoID(t *testing.T) {
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	now := baseTime()

	repoA := insertTestRepo(t, d, "acme", "widget")
	repoB := insertTestRepo(t, d, "acme", "gadget")
	mrID := insertTestMR(t, d, repoA, 9, "repo guarded", now)

	err := d.ReplaceMergeRequestLabels(ctx, repoB, mrID, []Label{{
		PlatformID:  9009,
		Name:        "wrong-repo",
		Description: "should fail",
		Color:       "ff0000",
		UpdatedAt:   now,
	}})
	require.Error(err)

	mr, err := d.GetMergeRequest(ctx, "acme", "widget", 9)
	require.NoError(err)
	require.NotNil(mr)
	require.Empty(mr.Labels)
}

func TestUpsertLabels_UsesPlatformIDForRename(t *testing.T) {
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	now := baseTime()

	repoID := insertTestRepo(t, d, "acme", "widget")
	require.NoError(d.UpsertLabels(ctx, repoID, []Label{{
		PlatformID:  41,
		Name:        "old-name",
		Description: "before rename",
		Color:       "111111",
		UpdatedAt:   now,
	}}))
	require.NoError(d.UpsertLabels(ctx, repoID, []Label{{
		PlatformID:  41,
		Name:        "new-name",
		Description: "after rename",
		Color:       "222222",
		IsDefault:   true,
		UpdatedAt:   now.Add(time.Minute),
	}}))

	var count int
	err := d.ReadDB().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM middleman_labels WHERE repo_id = ?`,
		repoID,
	).Scan(&count)
	require.NoError(err)
	require.Equal(1, count)

	var name, description, color string
	var isDefault bool
	var updatedAt time.Time
	err = d.ReadDB().QueryRowContext(ctx,
		`SELECT name, description, color, is_default, updated_at
		 FROM middleman_labels
		 WHERE repo_id = ? AND platform_id = ?`,
		repoID, 41,
	).Scan(&name, &description, &color, &isDefault, &updatedAt)
	require.NoError(err)
	require.Equal("new-name", name)
	require.Equal("after rename", description)
	require.Equal("222222", color)
	require.True(isDefault)
	require.True(updatedAt.Equal(now.Add(time.Minute)))
}

func TestUpsertLabels_UsesPlatformExternalIDForRename(t *testing.T) {
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	now := baseTime()

	repoID := insertTestRepo(t, d, "acme", "widget")
	require.NoError(d.UpsertLabels(ctx, repoID, []Label{{
		PlatformExternalID: "gid://gitlab/Label/bug",
		Name:               "old-name",
		Description:        "before rename",
		Color:              "111111",
		UpdatedAt:          now,
	}}))
	require.NoError(d.UpsertLabels(ctx, repoID, []Label{{
		PlatformExternalID: "gid://gitlab/Label/bug",
		Name:               "new-name",
		Description:        "after rename",
		Color:              "222222",
		IsDefault:          true,
		UpdatedAt:          now.Add(time.Minute),
	}}))

	var count int
	err := d.ReadDB().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM middleman_labels WHERE repo_id = ?`,
		repoID,
	).Scan(&count)
	require.NoError(err)
	require.Equal(1, count)

	var name, externalID string
	err = d.ReadDB().QueryRowContext(ctx,
		`SELECT name, platform_external_id
		 FROM middleman_labels
		 WHERE repo_id = ? AND platform_external_id = ?`,
		repoID, "gid://gitlab/Label/bug",
	).Scan(&name, &externalID)
	require.NoError(err)
	require.Equal("new-name", name)
	require.Equal("gid://gitlab/Label/bug", externalID)
}

func TestUpsertLabels_MergesStaleNameOnlyRowIntoPlatformRow(t *testing.T) {
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	now := baseTime()

	repoID := insertTestRepo(t, d, "acme", "widget")
	mrID := insertTestMR(t, d, repoID, 17, "rename labels", now)
	issueID := insertTestIssue(t, d, repoID, 23, "rename labels", now)

	require.NoError(d.UpsertLabels(ctx, repoID, []Label{{
		PlatformID:  200,
		Name:        "old-name",
		Description: "old platform label",
		Color:       "111111",
		UpdatedAt:   now,
	}}))
	require.NoError(d.ReplaceMergeRequestLabels(ctx, repoID, mrID, []Label{{
		Name:        "new-name",
		Description: "stale name-only label",
		Color:       "222222",
		UpdatedAt:   now,
	}}))
	require.NoError(d.ReplaceIssueLabels(ctx, repoID, issueID, []Label{{
		Name:        "new-name",
		Description: "stale name-only label",
		Color:       "222222",
		UpdatedAt:   now,
	}}))

	require.NoError(d.UpsertLabels(ctx, repoID, []Label{{
		PlatformID:  200,
		Name:        "new-name",
		Description: "renamed label",
		Color:       "333333",
		IsDefault:   true,
		UpdatedAt:   now.Add(time.Minute),
	}}))

	var count int
	err := d.ReadDB().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM middleman_labels WHERE repo_id = ?`,
		repoID,
	).Scan(&count)
	require.NoError(err)
	require.Equal(1, count)

	var labelID int64
	var platformID int64
	var name, description, color string
	var isDefault bool
	err = d.ReadDB().QueryRowContext(ctx, `
		SELECT id, platform_id, name, description, color, is_default
		FROM middleman_labels
		WHERE repo_id = ?`, repoID,
	).Scan(&labelID, &platformID, &name, &description, &color, &isDefault)
	require.NoError(err)
	require.Equal(int64(200), platformID)
	require.Equal("new-name", name)
	require.Equal("renamed label", description)
	require.Equal("333333", color)
	require.True(isDefault)

	mr, err := d.GetMergeRequest(ctx, "acme", "widget", 17)
	require.NoError(err)
	require.NotNil(mr)
	require.Len(mr.Labels, 1)
	require.Equal(labelID, mr.Labels[0].ID)
	require.Equal("new-name", mr.Labels[0].Name)

	issue, err := d.GetIssue(ctx, "acme", "widget", 23)
	require.NoError(err)
	require.NotNil(issue)
	require.Len(issue.Labels, 1)
	require.Equal(labelID, issue.Labels[0].ID)
	require.Equal("new-name", issue.Labels[0].Name)
}

func TestUpsertLabels_RejectsAmbiguousNameAndPlatformIDMatch(t *testing.T) {
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	now := baseTime()

	repoID := insertTestRepo(t, d, "acme", "widget")
	require.NoError(d.UpsertLabels(ctx, repoID, []Label{{
		PlatformID:  100,
		Name:        "bug",
		Description: "by name",
		Color:       "111111",
		UpdatedAt:   now,
	}}))
	require.NoError(d.UpsertLabels(ctx, repoID, []Label{{
		PlatformID:  200,
		Name:        "renamed",
		Description: "by platform",
		Color:       "222222",
		UpdatedAt:   now,
	}}))

	err := d.UpsertLabels(ctx, repoID, []Label{{
		PlatformID:  200,
		Name:        "bug",
		Description: "ambiguous",
		Color:       "333333",
		UpdatedAt:   now.Add(time.Minute),
	}})
	require.Error(err)
}

func TestKanbanState(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()

	repoID := insertTestRepo(t, d, "o", "r")
	prID := insertTestMR(t, d, repoID, 1, "pr", baseTime())

	// Before EnsureKanbanState, GetKanbanState returns nil.
	k, err := d.GetKanbanState(ctx, prID)
	require.NoError(err)
	assert.Nil(k)

	// EnsureKanbanState creates "new".
	require.NoError(d.EnsureKanbanState(ctx, prID))
	k, err = d.GetKanbanState(ctx, prID)
	require.NoError(err)
	require.NotNil(k)
	assert.Equal("new", k.Status)

	// SetKanbanState changes the status.
	require.NoError(d.SetKanbanState(ctx, prID, "reviewing"))
	k, _ = d.GetKanbanState(ctx, prID)
	require.NotNil(k)
	assert.Equal("reviewing", k.Status)

	// EnsureKanbanState does NOT overwrite an existing row.
	require.NoError(d.EnsureKanbanState(ctx, prID))
	k, _ = d.GetKanbanState(ctx, prID)
	require.NotNil(k)
	assert.Equal("reviewing", k.Status)
}

func TestPREvents(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()

	repoID := insertTestRepo(t, d, "o", "r")
	prID := insertTestMR(t, d, repoID, 1, "pr", baseTime())
	base := baseTime()

	events := []MREvent{
		{
			MergeRequestID: prID,
			EventType:      "comment",
			Author:         "alice",
			Summary:        "LGTM",
			CreatedAt:      base,
			DedupeKey:      "comment-1",
		},
		{
			MergeRequestID: prID,
			EventType:      "review",
			Author:         "bob",
			Summary:        "approved",
			CreatedAt:      base.Add(time.Hour),
			DedupeKey:      "review-1",
		},
	}

	require.NoError(d.UpsertMREvents(ctx, events))

	got, err := d.ListMREvents(ctx, prID)
	require.NoError(err)
	require.Len(got, 2)
	// Newest first.
	assert.Equal("review-1", got[0].DedupeKey)
	assert.Equal("comment-1", got[1].DedupeKey)

	// Inserting duplicate dedupe_key must be silently ignored.
	dup := []MREvent{
		{
			MergeRequestID: prID,
			EventType:      "comment",
			Author:         "alice",
			Summary:        "dupe",
			CreatedAt:      base,
			DedupeKey:      "comment-1",
		},
	}
	require.NoError(d.UpsertMREvents(ctx, dup))
	got2, _ := d.ListMREvents(ctx, prID)
	assert.Len(got2, 2)
}

func TestMREventsDedupeIsScopedToMergeRequest(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	base := baseTime()

	repoID := insertTestRepo(t, d, "o", "r")
	firstMRID := insertTestMR(t, d, repoID, 1, "pr one", base)
	secondMRID := insertTestMR(t, d, repoID, 2, "pr two", base.Add(time.Minute))

	sharedDedupeKey := "force-push-before-sha-after-sha"
	require.NoError(d.UpsertMREvents(ctx, []MREvent{
		{
			MergeRequestID: firstMRID,
			EventType:      "force_push",
			Author:         "alice",
			CreatedAt:      base,
			DedupeKey:      sharedDedupeKey,
		},
		{
			MergeRequestID: secondMRID,
			EventType:      "force_push",
			Author:         "bob",
			CreatedAt:      base.Add(time.Minute),
			DedupeKey:      sharedDedupeKey,
		},
	}))

	firstEvents, err := d.ListMREvents(ctx, firstMRID)
	require.NoError(err)
	require.Len(firstEvents, 1)
	assert.Equal(sharedDedupeKey, firstEvents[0].DedupeKey)

	secondEvents, err := d.ListMREvents(ctx, secondMRID)
	require.NoError(err)
	require.Len(secondEvents, 1)
	assert.Equal(sharedDedupeKey, secondEvents[0].DedupeKey)

	var total int
	err = d.ReadDB().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM middleman_mr_events WHERE dedupe_key = ?`,
		sharedDedupeKey,
	).Scan(&total)
	require.NoError(err)
	assert.Equal(2, total)
}

func TestMREventsPersistPlatformExternalID(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	base := baseTime()

	repoID := insertTestRepo(t, d, "o", "r")
	mrID := insertTestMR(t, d, repoID, 1, "pr one", base)
	platformID := int64(5001)
	require.NoError(d.UpsertMREvents(ctx, []MREvent{{
		MergeRequestID:     mrID,
		PlatformID:         &platformID,
		PlatformExternalID: "gid://gitlab/Note/5001",
		EventType:          "issue_comment",
		Author:             "alice",
		CreatedAt:          base,
		DedupeKey:          "gitlab:gitlab.example.com:o/r:mr:1:note:5001",
	}}))

	got, err := d.ListMREvents(ctx, mrID)
	require.NoError(err)
	require.Len(got, 1)
	assert.Equal("gid://gitlab/Note/5001", got[0].PlatformExternalID)
}

func TestListMREventsHandlesNonUTCTimes(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()

	repoID := insertTestRepo(t, d, "o", "r")
	prID := insertTestMR(t, d, repoID, 1, "pr one", baseTime())

	// Insert events with times in various non-UTC zones,
	// reproducing the formats the sqlite driver stores.
	//nolint:forbidigo // Test fixtures intentionally use non-UTC zones to verify normalization.
	edt := time.FixedZone("EDT", -4*3600)
	//nolint:forbidigo // Test fixtures intentionally use non-UTC zones to verify normalization.
	cdt := time.FixedZone("CDT", -5*3600)
	//nolint:forbidigo // Test fixtures intentionally use non-UTC zones to verify normalization.
	jst := time.FixedZone("JST", 9*3600)
	zones := []struct {
		key  string
		zone *time.Location
	}{
		{"commit-utc", time.UTC},
		{"commit-edt", edt},
		{"commit-cdt", cdt},
		{"commit-jst", jst},
	}
	var events []MREvent
	base := baseTime()
	for i, z := range zones {
		events = append(events, MREvent{
			MergeRequestID: prID,
			EventType:      "commit",
			Author:         "alice",
			CreatedAt:      base.Add(time.Duration(i) * time.Hour).In(z.zone),
			DedupeKey:      z.key,
		})
	}
	require.NoError(d.UpsertMREvents(ctx, events))

	got, err := d.ListMREvents(ctx, prID)
	require.NoError(err)
	require.Len(got, len(zones))

	for _, e := range got {
		assert.Equal(time.UTC, e.CreatedAt.Location(),
			"event %s should be returned in UTC", e.DedupeKey)
	}
}

func TestGetPRIDByRepoAndNumber(t *testing.T) {
	d := openTestDB(t)
	ctx := t.Context()

	repoID := insertTestRepo(t, d, "o", "r")
	insertTestMR(t, d, repoID, 5, "pr five", baseTime())

	id, err := d.GetMRIDByRepoAndNumber(ctx, "o", "r", 5)
	require.NoError(t, err)
	Assert.NotZero(t, id)

	_, err = d.GetMRIDByRepoAndNumber(ctx, "o", "r", 999)
	require.Error(t, err)
}

func TestGetDiffSHAsByRepoIDScopesDuplicateProviderRepos(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	d := openTestDB(t)
	githubID, err := d.UpsertRepo(ctx, RepoIdentity{
		Platform:     "github",
		PlatformHost: "code.example.com",
		Owner:        "acme",
		Name:         "widget",
	})
	require.NoError(err)
	gitlabID, err := d.UpsertRepo(ctx, RepoIdentity{
		Platform:     "gitlab",
		PlatformHost: "code.example.com",
		Owner:        "acme",
		Name:         "widget",
	})
	require.NoError(err)
	now := time.Now().UTC()
	_, err = d.UpsertMergeRequest(ctx, &MergeRequest{
		RepoID:          githubID,
		PlatformID:      1001,
		Number:          7,
		Title:           "github",
		State:           "merged",
		PlatformHeadSHA: "github-head",
		PlatformBaseSHA: "github-base",
		CreatedAt:       now,
		UpdatedAt:       now,
		LastActivityAt:  now,
	})
	require.NoError(err)
	_, err = d.UpsertMergeRequest(ctx, &MergeRequest{
		RepoID:          gitlabID,
		PlatformID:      2001,
		Number:          7,
		Title:           "gitlab",
		State:           "merged",
		PlatformHeadSHA: "gitlab-head",
		PlatformBaseSHA: "gitlab-base",
		CreatedAt:       now,
		UpdatedAt:       now,
		LastActivityAt:  now,
	})
	require.NoError(err)
	require.NoError(d.UpdateDiffSHAs(
		ctx, githubID, 7,
		"github-diff-head", "github-diff-base", "github-merge-base",
	))

	gitlabSHAs, err := d.GetDiffSHAsByRepoID(ctx, gitlabID, 7)
	require.NoError(err)
	require.NotNil(gitlabSHAs)
	assert.Equal("gitlab-head", gitlabSHAs.PlatformHeadSHA)
	assert.Empty(gitlabSHAs.DiffHeadSHA)

	githubSHAs, err := d.GetDiffSHAsByRepoID(ctx, githubID, 7)
	require.NoError(err)
	require.NotNil(githubSHAs)
	assert.Equal("github-head", githubSHAs.PlatformHeadSHA)
	assert.Equal("github-diff-head", githubSHAs.DiffHeadSHA)
}

func TestUpdateMRCIStatusForHeadSkipsStaleHead(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	d := openTestDB(t)
	repoID := insertTestRepo(t, d, "o", "r")
	now := baseTime()
	_, err := d.UpsertMergeRequest(ctx, &MergeRequest{
		RepoID:          repoID,
		PlatformID:      1001,
		Number:          7,
		Title:           "ci guarded",
		State:           "open",
		PlatformHeadSHA: "new-head",
		CIStatus:        "pending",
		CIChecksJSON:    `[{"name":"old","status":"in_progress"}]`,
		CreatedAt:       now,
		UpdatedAt:       now,
		LastActivityAt:  now,
	})
	require.NoError(err)

	require.NoError(d.UpdateMRCIStatusForHead(ctx, repoID, 7, "old-head", "success", `[]`, false))
	stale, err := d.GetMergeRequestByRepoIDAndNumber(ctx, repoID, 7)
	require.NoError(err)
	require.NotNil(stale)
	assert.Equal("pending", stale.CIStatus)

	require.NoError(d.UpdateMRCIStatusForHead(ctx, repoID, 7, "new-head", "pending", `[{"name":"build","status":"in_progress"}]`, true))
	fresh, err := d.GetMergeRequestByRepoIDAndNumber(ctx, repoID, 7)
	require.NoError(err)
	require.NotNil(fresh)
	assert.Equal("pending", fresh.CIStatus)
	assert.True(fresh.CIHadPending)

	require.NoError(d.UpdateMRCIStatusForHead(ctx, repoID, 7, "new-head", "success", `[]`, false))
	done, err := d.GetMergeRequestByRepoIDAndNumber(ctx, repoID, 7)
	require.NoError(err)
	require.NotNil(done)
	assert.Equal("success", done.CIStatus)
	assert.True(done.CIHadPending)
}

func TestGetPreviouslyOpenPRNumbers(t *testing.T) {
	d := openTestDB(t)

	repoID := insertTestRepo(t, d, "o", "r")
	base := baseTime()
	insertTestMR(t, d, repoID, 1, "pr1", base)
	insertTestMR(t, d, repoID, 2, "pr2", base.Add(time.Hour))
	insertTestMR(t, d, repoID, 3, "pr3", base.Add(2*time.Hour))

	// PRs 1 and 3 are still open; 2 was closed externally.
	stillOpen := map[int]bool{1: true, 3: true}
	closed, err := d.GetPreviouslyOpenMRNumbers(t.Context(), repoID, stillOpen)
	require.NoError(t, err)
	Assert.Equal(t, []int{2}, closed)
}

func TestUpsertPullRequestMergeableState(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	d := openTestDB(t)

	repoID := insertTestRepo(t, d, "acme", "widget")
	now := baseTime()
	pr := &MergeRequest{
		RepoID:         repoID,
		PlatformID:     9001,
		Number:         42,
		State:          "open",
		MergeableState: "dirty",
		CreatedAt:      now,
		UpdatedAt:      now,
		LastActivityAt: now,
	}

	_, err := d.UpsertMergeRequest(ctx, pr)
	require.NoError(err)

	got, err := d.GetMergeRequest(ctx, "acme", "widget", 42)
	require.NoError(err)
	require.NotNil(got)
	assert.Equal("dirty", got.MergeableState)

	pr.MergeableState = "clean"
	_, err = d.UpsertMergeRequest(ctx, pr)
	require.NoError(err)

	got, err = d.GetMergeRequest(ctx, "acme", "widget", 42)
	require.NoError(err)
	assert.Equal("clean", got.MergeableState)
}

func TestRateLimitCRUD(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	d := openTestDB(t)

	host := "github.com"
	hourStart := baseTime()
	resetAt := hourStart.Add(30 * time.Minute)

	// Insert REST
	require.NoError(d.UpsertRateLimit(host, "rest", 5, hourStart, 4995, -1, &resetAt))

	got, err := d.GetRateLimit(host, "rest")
	require.NoError(err)
	require.NotNil(got)
	assert.Equal(host, got.PlatformHost)
	assert.Equal("rest", got.APIType)
	assert.Equal(5, got.RequestsHour)
	assert.True(got.HourStart.Equal(hourStart))
	assert.Equal(4995, got.RateRemaining)
	require.NotNil(got.RateResetAt)
	assert.True(got.RateResetAt.Equal(resetAt))

	// Insert GraphQL for same host — separate row
	require.NoError(d.UpsertRateLimit(host, "graphql", 2, hourStart, 4998, 5000, nil))

	gql, err := d.GetRateLimit(host, "graphql")
	require.NoError(err)
	require.NotNil(gql)
	assert.Equal("graphql", gql.APIType)
	assert.Equal(2, gql.RequestsHour)
	assert.Equal(4998, gql.RateRemaining)

	// REST row unchanged
	rest, err := d.GetRateLimit(host, "rest")
	require.NoError(err)
	require.NotNil(rest)
	assert.Equal(5, rest.RequestsHour)

	// Update via upsert
	laterStart := hourStart.Add(time.Hour)
	require.NoError(d.UpsertRateLimit(host, "rest", 10, laterStart, 4990, -1, nil))

	got2, err := d.GetRateLimit(host, "rest")
	require.NoError(err)
	require.NotNil(got2)
	assert.Equal(10, got2.RequestsHour)
	assert.True(got2.HourStart.Equal(laterStart))
	assert.Equal(4990, got2.RateRemaining)
	assert.Nil(got2.RateResetAt)

	// Not found
	missing, err := d.GetRateLimit("no.such.host", "rest")
	require.NoError(err)
	assert.Nil(missing)
}

func TestRateLimitCRUDScopesByPlatform(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	d := openTestDB(t)

	host := "gitlab.example.com"
	hourStart := baseTime()

	require.NoError(d.UpsertPlatformRateLimit("github", host, "rest", 1, hourStart, 4999, 5000, nil))
	require.NoError(d.UpsertPlatformRateLimit("github", host, "graphql", 2, hourStart, 4998, 5000, nil))
	require.NoError(d.UpsertPlatformRateLimit("gitlab", host, "rest", 3, hourStart, 599, 600, nil))

	ghRest, err := d.GetPlatformRateLimit("github", host, "rest")
	require.NoError(err)
	require.NotNil(ghRest)
	assert.Equal("github", ghRest.Platform)
	assert.Equal("rest", ghRest.APIType)
	assert.Equal(1, ghRest.RequestsHour)

	ghGraphQL, err := d.GetPlatformRateLimit("github", host, "graphql")
	require.NoError(err)
	require.NotNil(ghGraphQL)
	assert.Equal("github", ghGraphQL.Platform)
	assert.Equal("graphql", ghGraphQL.APIType)
	assert.Equal(2, ghGraphQL.RequestsHour)

	glRest, err := d.GetPlatformRateLimit("gitlab", host, "rest")
	require.NoError(err)
	require.NotNil(glRest)
	assert.Equal("gitlab", glRest.Platform)
	assert.Equal("rest", glRest.APIType)
	assert.Equal(3, glRest.RequestsHour)

	require.NoError(d.UpsertPlatformRateLimit("gitlab", host, "rest", 7, hourStart.Add(time.Hour), 593, 600, nil))
	ghRest, err = d.GetPlatformRateLimit("github", host, "rest")
	require.NoError(err)
	require.NotNil(ghRest)
	assert.Equal(1, ghRest.RequestsHour)
	glRest, err = d.GetPlatformRateLimit("gitlab", host, "rest")
	require.NoError(err)
	require.NotNil(glRest)
	assert.Equal(7, glRest.RequestsHour)
}

func TestUpdatePRState(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()

	repoID := insertTestRepo(t, d, "o", "r")
	insertTestMR(t, d, repoID, 1, "pr", baseTime())

	mergedAt := baseTime().Add(time.Hour)
	require.NoError(d.UpdateMRState(ctx, repoID, 1, "merged", &mergedAt, nil))

	pr, err := d.GetMergeRequest(ctx, "o", "r", 1)
	require.NoError(err)
	require.NotNil(pr)
	assert.Equal(MergeRequestStateMerged, pr.State)
	require.NotNil(pr.MergedAt)
	assert.True(pr.MergedAt.Equal(mergedAt))
}

func TestListIssues_AttachesLabels(t *testing.T) {
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	now := baseTime()

	repoID, err := d.UpsertRepo(ctx, GitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)

	issueID, err := d.UpsertIssue(ctx, &Issue{
		RepoID:         repoID,
		PlatformID:     201,
		Number:         3,
		URL:            "https://github.com/acme/widget/issues/3",
		Title:          "Bug",
		Author:         "bob",
		State:          "open",
		CreatedAt:      now,
		UpdatedAt:      now,
		LastActivityAt: now,
	})
	require.NoError(err)
	require.NoError(d.ReplaceIssueLabels(ctx, repoID, issueID, []Label{{
		PlatformID:  11,
		Name:        "bug",
		Description: "Something is broken",
		Color:       "d73a4a",
		IsDefault:   true,
		UpdatedAt:   now,
	}}))

	issues, err := d.ListIssues(ctx, ListIssuesOpts{})
	require.NoError(err)
	require.Len(issues, 1)
	require.Len(issues[0].Labels, 1)
	require.Equal("bug", issues[0].Labels[0].Name)
	require.Equal("Something is broken", issues[0].Labels[0].Description)
	require.Equal("d73a4a", issues[0].Labels[0].Color)
	require.True(issues[0].Labels[0].IsDefault)
	require.True(issues[0].Labels[0].UpdatedAt.Equal(now))
}

func TestGetIssue_AttachesLabels(t *testing.T) {
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	now := baseTime()

	repoID := insertTestRepo(t, d, "acme", "widget")
	issueID, err := d.UpsertIssue(ctx, &Issue{
		RepoID:         repoID,
		PlatformID:     202,
		Number:         4,
		URL:            "https://github.com/acme/widget/issues/4",
		Title:          "Docs",
		Author:         "bob",
		State:          "open",
		CreatedAt:      now,
		UpdatedAt:      now,
		LastActivityAt: now,
	})
	require.NoError(err)
	require.NoError(d.ReplaceIssueLabels(ctx, repoID, issueID, []Label{{
		PlatformID:  12,
		Name:        "documentation",
		Description: "Docs updates",
		Color:       "0075ca",
		IsDefault:   false,
		UpdatedAt:   now,
	}}))

	issue, err := d.GetIssue(ctx, "acme", "widget", 4)
	require.NoError(err)
	require.NotNil(issue)
	require.Len(issue.Labels, 1)
	require.Equal("documentation", issue.Labels[0].Name)
	require.Equal("Docs updates", issue.Labels[0].Description)
	require.Equal("0075ca", issue.Labels[0].Color)
	require.False(issue.Labels[0].IsDefault)
	require.True(issue.Labels[0].UpdatedAt.Equal(now))
}

func TestIssueRepoScopedQueriesCanonicalizeOwnerName(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()

	repoID := insertTestRepo(t, d, "owner", "repo")
	issueID := insertTestIssue(t, d, repoID, 7, "mixed case issue", baseTime())

	got, err := d.GetIssue(ctx, "Owner", "Repo", 7)
	require.NoError(err)
	require.NotNil(got)
	assert.Equal(issueID, got.ID)

	filtered, err := d.ListIssues(ctx, ListIssuesOpts{
		RepoOwner: "Owner",
		RepoName:  "Repo",
	})
	require.NoError(err)
	require.Len(filtered, 1)
	assert.Equal(issueID, filtered[0].ID)

	gotID, err := d.GetIssueIDByRepoAndNumber(ctx, "Owner", "Repo", 7)
	require.NoError(err)
	assert.Equal(issueID, gotID)
}

func TestListIssuesFilterBySearch(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)
	d := openTestDB(t)

	repoID := insertTestRepo(t, d, "owner", "repo")
	base := baseTime()

	insertTestIssue(t, d, repoID, 12, "report a bug", base)
	insertTestIssue(t, d, repoID, 278, "filter broken", base.Add(time.Hour))
	insertTestIssue(t, d, repoID, 290, "another change", base.Add(2*time.Hour))

	issues, err := d.ListIssues(t.Context(), ListIssuesOpts{Search: "broken"})
	require.NoError(err)
	require.Len(issues, 1)
	assert.Equal(278, issues[0].Number)

	issues, err = d.ListIssues(t.Context(), ListIssuesOpts{Search: "278"})
	require.NoError(err)
	require.Len(issues, 1)
	assert.Equal(278, issues[0].Number)

	issues, err = d.ListIssues(t.Context(), ListIssuesOpts{Search: "#278"})
	require.NoError(err)
	require.Len(issues, 1)
	assert.Equal(278, issues[0].Number)

	// Substring of number matches multiple.
	issues, err = d.ListIssues(t.Context(), ListIssuesOpts{Search: "2"})
	require.NoError(err)
	require.Len(issues, 3)
}

func TestListIssuesFilterBySearchLabel(t *testing.T) {
	require := require.New(t)
	assert := Assert.New(t)
	d := openTestDB(t)
	ctx := t.Context()

	repoID := insertTestRepo(t, d, "owner", "repo")
	base := baseTime()

	insertTestIssue(t, d, repoID, 12, "report a bug", base)
	issueID := insertTestIssue(t, d, repoID, 278, "filter broken", base.Add(time.Hour))
	require.NoError(d.ReplaceIssueLabels(ctx, repoID, issueID, []Label{{
		PlatformID: 300,
		Name:       "needs-triage",
		Color:      "d73a4a",
		UpdatedAt:  base,
	}}))

	issues, err := d.ListIssues(ctx, ListIssuesOpts{Search: "needs-triage"})
	require.NoError(err)
	require.Len(issues, 1)
	assert.Equal(278, issues[0].Number)
}

func TestReplaceIssueLabels_RejectsWrongRepoID(t *testing.T) {
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	now := baseTime()

	repoA := insertTestRepo(t, d, "acme", "widget")
	repoB := insertTestRepo(t, d, "acme", "gadget")
	issueID, err := d.UpsertIssue(ctx, &Issue{
		RepoID:         repoA,
		PlatformID:     204,
		Number:         6,
		URL:            "https://github.com/acme/widget/issues/6",
		Title:          "repo guarded issue",
		Author:         "bob",
		State:          "open",
		CreatedAt:      now,
		UpdatedAt:      now,
		LastActivityAt: now,
	})
	require.NoError(err)

	err = d.ReplaceIssueLabels(ctx, repoB, issueID, []Label{{
		PlatformID:  220,
		Name:        "wrong-repo",
		Description: "should fail",
		Color:       "ff0000",
		UpdatedAt:   now,
	}})
	require.Error(err)

	issue, err := d.GetIssue(ctx, "acme", "widget", 6)
	require.NoError(err)
	require.NotNil(issue)
	require.Empty(issue.Labels)
}

func TestListIssues_UsesRepoScopedLabels(t *testing.T) {
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	now := baseTime()

	repoA, err := d.UpsertRepo(ctx, GitHubRepoIdentity("github.com", "acme", "widget"))
	require.NoError(err)
	repoB, err := d.UpsertRepo(ctx, GitHubRepoIdentity("github.com", "acme", "gadget"))
	require.NoError(err)

	issueID, err := d.UpsertIssue(ctx, &Issue{
		RepoID:         repoA,
		PlatformID:     203,
		Number:         5,
		URL:            "https://github.com/acme/widget/issues/5",
		Title:          "Repo scoped bug",
		Author:         "bob",
		State:          "open",
		CreatedAt:      now,
		UpdatedAt:      now,
		LastActivityAt: now,
	})
	require.NoError(err)

	require.NoError(d.ReplaceIssueLabels(ctx, repoA, issueID, []Label{{
		PlatformID:  21,
		Name:        "bug",
		Description: "Widget bug",
		Color:       "d73a4a",
		UpdatedAt:   now,
	}}))
	require.NoError(d.UpsertLabels(ctx, repoB, []Label{{
		PlatformID:  22,
		Name:        "bug",
		Description: "Gadget bug",
		Color:       "0e8a16",
		UpdatedAt:   now,
	}}))

	issues, err := d.ListIssues(ctx, ListIssuesOpts{})
	require.NoError(err)
	require.Len(issues, 1)
	require.Len(issues[0].Labels, 1)
	require.Equal("bug", issues[0].Labels[0].Name)
	require.Equal("d73a4a", issues[0].Labels[0].Color)
}

func TestSetWorktreeLinks(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()

	repoID := insertTestRepo(t, d, "o", "r")
	mrID1 := insertTestMR(t, d, repoID, 1, "pr1", baseTime())
	mrID2 := insertTestMR(t, d, repoID, 2, "pr2", baseTime().Add(time.Hour))

	now := baseTime()
	links := []WorktreeLink{
		{MergeRequestID: mrID1, WorktreeKey: "wt-1", WorktreePath: "/tmp/wt1", WorktreeBranch: "feature-1", LinkedAt: now},
		{MergeRequestID: mrID2, WorktreeKey: "wt-2", WorktreePath: "/tmp/wt2", WorktreeBranch: "feature-2", LinkedAt: now.Add(time.Hour)},
	}
	require.NoError(d.SetWorktreeLinks(ctx, links))

	all, err := d.GetAllWorktreeLinks(ctx)
	require.NoError(err)
	require.Len(all, 2)
	// Newest first.
	assert.Equal("wt-2", all[0].WorktreeKey)
	assert.Equal("wt-1", all[1].WorktreeKey)
	assert.Equal("/tmp/wt1", all[1].WorktreePath)
	assert.Equal("feature-1", all[1].WorktreeBranch)

	// Bulk replace with a different set.
	replacement := []WorktreeLink{
		{MergeRequestID: mrID1, WorktreeKey: "wt-3", WorktreePath: "/tmp/wt3", WorktreeBranch: "hotfix", LinkedAt: now},
	}
	require.NoError(d.SetWorktreeLinks(ctx, replacement))

	all2, err := d.GetAllWorktreeLinks(ctx)
	require.NoError(err)
	require.Len(all2, 1)
	assert.Equal("wt-3", all2[0].WorktreeKey)
}

func TestGetWorktreeLinksForMR(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()

	repoID := insertTestRepo(t, d, "o", "r")
	mrID1 := insertTestMR(t, d, repoID, 1, "pr1", baseTime())
	mrID2 := insertTestMR(t, d, repoID, 2, "pr2", baseTime().Add(time.Hour))

	now := baseTime()
	links := []WorktreeLink{
		{MergeRequestID: mrID1, WorktreeKey: "wt-a", LinkedAt: now},
		{MergeRequestID: mrID2, WorktreeKey: "wt-b", LinkedAt: now},
	}
	require.NoError(d.SetWorktreeLinks(ctx, links))

	forMR1, err := d.GetWorktreeLinksForMR(ctx, mrID1)
	require.NoError(err)
	require.Len(forMR1, 1)
	assert.Equal("wt-a", forMR1[0].WorktreeKey)

	// Empty when no links for a given MR.
	forMR999, err := d.GetWorktreeLinksForMR(ctx, 999)
	require.NoError(err)
	assert.Empty(forMR999)
}

func TestListCommentAutocompleteUsers(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	base := baseTime()

	repoID := insertTestRepo(t, d, "acme", "widget")
	mrID := insertTestMR(t, d, repoID, 12, "Polish mentions", base.Add(2*time.Hour))
	issueID := insertTestIssue(t, d, repoID, 7, "Mention bug", base.Add(time.Hour))

	_, err := d.UpsertMergeRequest(ctx, &MergeRequest{
		RepoID:         repoID,
		PlatformID:     9001,
		Number:         13,
		URL:            "https://github.com/acme/widget/pull/13",
		Title:          "Secondary author",
		Author:         "alex",
		State:          "open",
		HeadBranch:     "feature-13",
		BaseBranch:     "main",
		CreatedAt:      base.Add(3 * time.Hour),
		UpdatedAt:      base.Add(3 * time.Hour),
		LastActivityAt: base.Add(3 * time.Hour),
	})
	require.NoError(err)
	_, err = d.UpsertIssue(ctx, &Issue{
		RepoID:         repoID,
		PlatformID:     9002,
		Number:         8,
		URL:            "https://github.com/acme/widget/issues/8",
		Title:          "Issue author",
		Author:         "alice",
		State:          "open",
		CreatedAt:      base.Add(4 * time.Hour),
		UpdatedAt:      base.Add(4 * time.Hour),
		LastActivityAt: base.Add(4 * time.Hour),
	})
	require.NoError(err)
	require.NoError(d.UpsertMREvents(ctx, []MREvent{{
		MergeRequestID: mrID,
		EventType:      "comment",
		Author:         "albert",
		CreatedAt:      base.Add(5 * time.Hour),
		DedupeKey:      "mr-comment-1",
	}}))
	require.NoError(d.UpsertIssueEvents(ctx, []IssueEvent{{
		IssueID:   issueID,
		EventType: "comment",
		Author:    "alice",
		CreatedAt: base.Add(6 * time.Hour),
		DedupeKey: "issue-comment-1",
	}}))

	users, err := d.ListCommentAutocompleteUsers(ctx, "github.com", "acme", "widget", "al", 10)
	require.NoError(err)
	assert.Equal([]string{"alice", "albert", "alex"}, users)

	users, err = d.ListCommentAutocompleteUsers(ctx, "github.com", "acme", "widget", "bert", 10)
	require.NoError(err)
	assert.Equal([]string{"albert"}, users)
}

func TestListCommentAutocompleteReferences(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	base := baseTime()

	repoID := insertTestRepo(t, d, "acme", "widget")
	insertTestMR(t, d, repoID, 12, "Polish mentions", base.Add(3*time.Hour))
	insertTestMR(t, d, repoID, 3, "Add docs", base)
	insertTestIssue(t, d, repoID, 17, "Mention bug", base.Add(2*time.Hour))
	insertTestIssue(t, d, repoID, 101, "Numbered item", base.Add(time.Hour))

	refs, err := d.ListCommentAutocompleteReferences(ctx, "github.com", "acme", "widget", "1", 10)
	require.NoError(err)
	require.Len(refs, 3)
	assert.Equal(CommentAutocompleteReference{Kind: "pull", Number: 12, Title: "Polish mentions", State: "open"}, refs[0])
	assert.Equal(CommentAutocompleteReference{Kind: "issue", Number: 17, Title: "Mention bug", State: "open"}, refs[1])
	assert.Equal(CommentAutocompleteReference{Kind: "issue", Number: 101, Title: "Numbered item", State: "open"}, refs[2])

	refs, err = d.ListCommentAutocompleteReferences(ctx, "github.com", "acme", "widget", "doc", 10)
	require.NoError(err)
	require.Len(refs, 1)
	assert.Equal(CommentAutocompleteReference{Kind: "pull", Number: 3, Title: "Add docs", State: "open"}, refs[0])
}

func TestWorktreeLinksCascadeOnMRDelete(t *testing.T) {
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()

	repoID := insertTestRepo(t, d, "o", "r")
	mrID := insertTestMR(t, d, repoID, 1, "pr1", baseTime())

	links := []WorktreeLink{
		{MergeRequestID: mrID, WorktreeKey: "wt-del", LinkedAt: baseTime()},
	}
	require.NoError(d.SetWorktreeLinks(ctx, links))

	all, err := d.GetAllWorktreeLinks(ctx)
	require.NoError(err)
	require.Len(all, 1)

	// Delete the MR; the ON DELETE CASCADE should remove the link.
	_, err = d.WriteDB().ExecContext(ctx,
		`DELETE FROM middleman_merge_requests WHERE id = ?`, mrID)
	require.NoError(err)

	after, err := d.GetAllWorktreeLinks(ctx)
	require.NoError(err)
	require.Empty(after)
}

// TestWorktreeAndPurgeRespectCanceledContext verifies a
// pre-canceled context aborts the database/sql call rather
// than running the query. Locks in the cancellation guarantee
// the ctx plumbing added for worktree-link and purge queries.
func TestWorktreeAndPurgeRespectCanceledContext(t *testing.T) {
	require := require.New(t)
	d := openTestDB(t)

	canceled, cancel := context.WithCancel(t.Context())
	cancel()

	err := d.PurgeOtherHosts(canceled, "github.com")
	require.ErrorIs(err, context.Canceled)

	err = d.SetWorktreeLinks(canceled, nil)
	require.ErrorIs(err, context.Canceled)

	_, err = d.GetWorktreeLinksForMR(canceled, 1)
	require.ErrorIs(err, context.Canceled)

	_, err = d.GetWorktreeLinksForMRs(canceled, []int64{1, 2})
	require.ErrorIs(err, context.Canceled)

	_, err = d.GetAllWorktreeLinks(canceled)
	require.ErrorIs(err, context.Canceled)
}

func TestGetRepoByHostOwnerName(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()

	// Insert two repos with same owner/name but different hosts.
	ghID := insertTestRepoWithHost(t, d, "acme", "widget", "github.com")
	gheID := insertTestRepoWithHost(
		t, d, "acme", "widget", "ghes.corp.com",
	)

	// Each found by its host.
	gh, err := d.GetRepoByHostOwnerName(
		ctx, "github.com", "acme", "widget",
	)
	require.NoError(err)
	require.NotNil(gh)
	assert.Equal(ghID, gh.ID)
	assert.Equal("github.com", gh.PlatformHost)

	ghe, err := d.GetRepoByHostOwnerName(
		ctx, "ghes.corp.com", "acme", "widget",
	)
	require.NoError(err)
	require.NotNil(ghe)
	assert.Equal(gheID, ghe.ID)
	assert.Equal("ghes.corp.com", ghe.PlatformHost)

	// Missing host returns nil.
	missing, err := d.GetRepoByHostOwnerName(
		ctx, "gitlab.com", "acme", "widget",
	)
	require.NoError(err)
	assert.Nil(missing)
}

func TestGetRepoByHostOwnerNameUsesLookupKeysForNonGitHubRows(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()

	id, err := d.UpsertRepo(ctx, RepoIdentity{
		Platform:     "gitlab",
		PlatformHost: "gitlab.example.com",
		Owner:        "Group/SubGroup",
		Name:         "ProjectName",
		RepoPath:     "Group/SubGroup/ProjectName",
	})
	require.NoError(err)

	repo, err := d.GetRepoByHostOwnerName(
		ctx, "gitlab.example.com", "group/subgroup", "projectname",
	)
	require.NoError(err)
	require.NotNil(repo)
	assert.Equal(id, repo.ID)
	assert.Equal("gitlab", repo.Platform)
	assert.Equal("gitlab.example.com", repo.PlatformHost)
	assert.Equal("Group/SubGroup", repo.Owner)
	assert.Equal("ProjectName", repo.Name)
	assert.Equal("Group/SubGroup/ProjectName", repo.RepoPath)
	assert.Equal("group/subgroup", repo.OwnerKey)
	assert.Equal("projectname", repo.NameKey)
}

func TestRepoIdentifierCasefoldTriggers(t *testing.T) {
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()

	_, err := d.WriteDB().ExecContext(ctx, `
		INSERT INTO middleman_repos (platform, platform_host, owner, name)
		VALUES ('github', 'github.com', 'Acme', 'widget')`)
	require.Error(err)
	require.Contains(err.Error(), "repo identifiers must be provider-canonical")

	repoID := insertTestRepo(t, d, "acme", "widget")
	_, err = d.WriteDB().ExecContext(ctx, `
		UPDATE middleman_repos SET name = 'Widget' WHERE id = ?`,
		repoID,
	)
	require.Error(err)
	require.Contains(err.Error(), "repo identifiers must be provider-canonical")
}

func TestWorkspaceCRUD(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()

	ws := &Workspace{
		ID:              "ws-abc-123",
		PlatformHost:    "github.com",
		RepoOwner:       "acme",
		RepoName:        "widget",
		ItemType:        WorkspaceItemTypePullRequest,
		ItemNumber:      42,
		GitHeadRef:      "feature/thing",
		WorkspaceBranch: "middleman/pr-42",
		WorktreePath:    "/tmp/ws-abc-123",
		TmuxSession:     "ws-abc-123",
		Status:          "creating",
	}

	// Insert
	require.NoError(d.InsertWorkspace(ctx, ws))

	// Get by ID
	got, err := d.GetWorkspace(ctx, "ws-abc-123")
	require.NoError(err)
	require.NotNil(got)
	assert.Equal("ws-abc-123", got.ID)
	assert.Equal("github.com", got.PlatformHost)
	assert.Equal("acme", got.RepoOwner)
	assert.Equal("widget", got.RepoName)
	assert.Equal(WorkspaceItemTypePullRequest, got.ItemType)
	assert.Equal(42, got.ItemNumber)
	assert.Equal("feature/thing", got.GitHeadRef)
	assert.Nil(got.MRHeadRepo)
	assert.Equal("middleman/pr-42", got.WorkspaceBranch)
	assert.Equal("/tmp/ws-abc-123", got.WorktreePath)
	assert.Equal("ws-abc-123", got.TmuxSession)
	assert.Equal("creating", got.Status)
	assert.Nil(got.ErrorMessage)
	assert.False(got.CreatedAt.IsZero())

	// Get by MR coordinates
	byMR, err := d.GetWorkspaceByMR(
		ctx, "github.com", "acme", "widget", 42,
	)
	require.NoError(err)
	require.NotNil(byMR)
	assert.Equal("ws-abc-123", byMR.ID)
	assert.Equal("middleman/pr-42", byMR.WorkspaceBranch)

	// GetWorkspaceByMR miss
	missMR, err := d.GetWorkspaceByMR(
		ctx, "github.com", "acme", "widget", 999,
	)
	require.NoError(err)
	assert.Nil(missMR)

	// List (ordered by created_at DESC).
	// Force ws2 to have a later created_at.
	_, err = d.WriteDB().ExecContext(ctx, `
		INSERT INTO middleman_workspaces
		    (id, platform_host, repo_owner, repo_name,
		     item_type, item_number, git_head_ref,
		     worktree_path, tmux_session, status,
		     created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?,
		        datetime('now', '+1 minute'))`,
		"ws-def-456", "github.com", "acme", "gadget",
		WorkspaceItemTypePullRequest, 7, "fix/bug",
		"/tmp/ws-def-456", "ws-def-456", "ready",
	)
	require.NoError(err)

	list, err := d.ListWorkspaces(ctx)
	require.NoError(err)
	require.Len(list, 2)
	// Newest first.
	assert.Equal("ws-def-456", list[0].ID)
	assert.Equal("ws-abc-123", list[1].ID)

	// UpdateWorkspaceStatus
	errMsg := "clone failed"
	require.NoError(d.UpdateWorkspaceStatus(
		ctx, "ws-abc-123", "error", &errMsg,
	))
	updated, err := d.GetWorkspace(ctx, "ws-abc-123")
	require.NoError(err)
	require.NotNil(updated)
	assert.Equal("error", updated.Status)
	require.NotNil(updated.ErrorMessage)
	assert.Equal("clone failed", *updated.ErrorMessage)

	require.NoError(d.UpdateWorkspaceBranch(
		ctx, "ws-abc-123", "feature/thing",
	))
	updated, err = d.GetWorkspace(ctx, "ws-abc-123")
	require.NoError(err)
	require.NotNil(updated)
	assert.Equal("feature/thing", updated.WorkspaceBranch)

	require.NoError(d.InsertWorkspaceSetupEvent(
		ctx,
		&WorkspaceSetupEvent{
			WorkspaceID: "ws-abc-123",
			Stage:       "clone",
			Outcome:     "failure",
			Message:     "ensure clone: clone failed",
		},
	))
	events, err := d.ListWorkspaceSetupEvents(
		ctx, "ws-abc-123",
	)
	require.NoError(err)
	require.Len(events, 1)
	assert.Equal("ws-abc-123", events[0].WorkspaceID)
	assert.Equal("clone", events[0].Stage)
	assert.Equal("failure", events[0].Outcome)
	assert.Equal("ensure clone: clone failed", events[0].Message)
	assert.False(events[0].CreatedAt.IsZero())

	require.NoError(d.UpsertWorkspaceTmuxSession(
		ctx,
		&WorkspaceTmuxSession{
			WorkspaceID: "ws-abc-123",
			SessionName: "middleman-ws-abc-123-codex",
			TargetKey:   "codex",
		},
	))
	require.NoError(d.UpsertWorkspaceTmuxSession(
		ctx,
		&WorkspaceTmuxSession{
			WorkspaceID: "ws-abc-123",
			SessionName: "middleman-ws-abc-123-claude",
			TargetKey:   "claude",
		},
	))
	tmuxSessions, err := d.ListWorkspaceTmuxSessions(ctx, "ws-abc-123")
	require.NoError(err)
	require.Len(tmuxSessions, 2)
	assert.Equal("middleman-ws-abc-123-claude", tmuxSessions[0].SessionName)
	assert.Equal("claude", tmuxSessions[0].TargetKey)
	assert.False(tmuxSessions[0].CreatedAt.IsZero())

	allTmuxSessions, err := d.ListAllWorkspaceTmuxSessions(ctx)
	require.NoError(err)
	require.Len(allTmuxSessions, 2)

	require.NoError(d.DeleteWorkspaceTmuxSession(
		ctx, "ws-abc-123", "middleman-ws-abc-123-claude",
	))
	tmuxSessions, err = d.ListWorkspaceTmuxSessions(ctx, "ws-abc-123")
	require.NoError(err)
	require.Len(tmuxSessions, 1)
	assert.Equal("middleman-ws-abc-123-codex", tmuxSessions[0].SessionName)

	require.NoError(d.DeleteWorkspaceTmuxSessions(ctx, "ws-abc-123"))
	tmuxSessions, err = d.ListWorkspaceTmuxSessions(ctx, "ws-abc-123")
	require.NoError(err)
	assert.Empty(tmuxSessions)

	// Clear error message
	require.NoError(d.UpdateWorkspaceStatus(
		ctx, "ws-abc-123", "ready", nil,
	))
	cleared, err := d.GetWorkspace(ctx, "ws-abc-123")
	require.NoError(err)
	assert.Equal("ready", cleared.Status)
	assert.Nil(cleared.ErrorMessage)

	// Delete
	require.NoError(d.DeleteWorkspace(ctx, "ws-abc-123"))
	gone, err := d.GetWorkspace(ctx, "ws-abc-123")
	require.NoError(err)
	assert.Nil(gone)

	// Get missing ID returns nil
	noSuch, err := d.GetWorkspace(ctx, "nonexistent")
	require.NoError(err)
	assert.Nil(noSuch)
}

func TestFreshWorkspaceTmuxSessionSchemaIncludesCreatedAt(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)

	d := openTestDB(t)
	rows, err := d.ReadDB().QueryContext(
		context.Background(),
		`PRAGMA table_info(middleman_workspace_tmux_sessions)`,
	)
	require.NoError(err)
	defer rows.Close()

	columns := make(map[string]string)
	for rows.Next() {
		var (
			cid        int
			name       string
			columnType string
			notNull    int
			defaultVal sql.NullString
			pk         int
		)
		require.NoError(rows.Scan(
			&cid, &name, &columnType, &notNull, &defaultVal, &pk,
		))
		columns[name] = columnType
	}
	require.NoError(rows.Err())

	assert.Equal("DATETIME", columns["created_at"])
}

func TestWorkspaceIdentifierCasefoldTriggers(t *testing.T) {
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()

	_, err := d.WriteDB().ExecContext(ctx, `
		INSERT INTO middleman_workspaces
		    (id, platform_host, repo_owner, repo_name,
		     item_type, item_number, git_head_ref, worktree_path, tmux_session)
		VALUES ('mixed', 'github.com', 'Acme', 'widget', 'pull_request', 1, 'feature',
		        '/tmp/mixed', 'mixed')`)
	require.Error(err)
	require.Contains(err.Error(), "workspace repo identifiers must be provider-canonical")

	ws := &Workspace{
		ID:           "lower",
		PlatformHost: "github.com",
		RepoOwner:    "acme",
		RepoName:     "widget",
		ItemType:     WorkspaceItemTypePullRequest,
		ItemNumber:   1,
		GitHeadRef:   "feature",
		WorktreePath: "/tmp/lower",
		TmuxSession:  "lower",
	}
	require.NoError(d.InsertWorkspace(ctx, ws))

	_, err = d.WriteDB().ExecContext(ctx, `
		UPDATE middleman_workspaces SET repo_name = 'Widget' WHERE id = 'lower'`)
	require.Error(err)
	require.Contains(err.Error(), "workspace repo identifiers must be provider-canonical")
}

func TestWorkspaceCanonicalizationPreservesGitLabRepoDisplay(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()

	_, err := d.UpsertRepo(ctx, RepoIdentity{
		Platform:     "gitlab",
		PlatformHost: "gitlab.example.com",
		Owner:        "Group/SubGroup",
		Name:         "ProjectName",
		RepoPath:     "Group/SubGroup/ProjectName",
	})
	require.NoError(err)

	ws := &Workspace{
		ID:           "gitlab-workspace",
		PlatformHost: "gitlab.example.com",
		RepoOwner:    "group/subgroup",
		RepoName:     "projectname",
		ItemType:     WorkspaceItemTypePullRequest,
		ItemNumber:   7,
		GitHeadRef:   "feature",
		WorktreePath: "/tmp/gitlab-workspace",
		TmuxSession:  "gitlab-workspace",
	}
	require.NoError(d.InsertWorkspace(ctx, ws))

	got, err := d.GetWorkspace(ctx, ws.ID)
	require.NoError(err)
	require.NotNil(got)
	assert.Equal("Group/SubGroup", got.RepoOwner)
	assert.Equal("ProjectName", got.RepoName)

	byMR, err := d.GetWorkspaceByMR(ctx, "gitlab.example.com", "GROUP/SubGroup", "PROJECTName", 7)
	require.NoError(err)
	require.NotNil(byMR)
	assert.Equal(ws.ID, byMR.ID)

	duplicate := *ws
	duplicate.ID = "gitlab-workspace-duplicate"
	duplicate.RepoOwner = "GROUP/SubGroup"
	duplicate.RepoName = "PROJECTName"
	err = d.InsertWorkspace(ctx, &duplicate)
	require.Error(err)
	require.Contains(err.Error(), "UNIQUE constraint failed")
}

func TestWorkspaceUniqueConstraint(t *testing.T) {
	d := openTestDB(t)
	ctx := t.Context()

	t.Run("pull request duplicates conflict", func(t *testing.T) {
		ws := &Workspace{
			ID:           "ws-pr-1",
			PlatformHost: "github.com",
			RepoOwner:    "acme",
			RepoName:     "widget",
			ItemType:     WorkspaceItemTypePullRequest,
			ItemNumber:   42,
			GitHeadRef:   "feat/pr-1",
			WorktreePath: "/tmp/ws-pr-1",
			TmuxSession:  "ws-pr-1",
			Status:       "creating",
		}
		require.NoError(t, d.InsertWorkspace(ctx, ws))

		dup := &Workspace{
			ID:           "ws-pr-2",
			PlatformHost: "github.com",
			RepoOwner:    "acme",
			RepoName:     "widget",
			ItemType:     WorkspaceItemTypePullRequest,
			ItemNumber:   42,
			GitHeadRef:   "feat/pr-2",
			WorktreePath: "/tmp/ws-pr-2",
			TmuxSession:  "ws-pr-2",
			Status:       "creating",
		}
		err := d.InsertWorkspace(ctx, dup)
		require.Error(t, err)
	})

	t.Run("issue duplicates conflict", func(t *testing.T) {
		ws := &Workspace{
			ID:           "ws-issue-1",
			PlatformHost: "github.com",
			RepoOwner:    "acme",
			RepoName:     "widget-issues",
			ItemType:     WorkspaceItemTypeIssue,
			ItemNumber:   42,
			GitHeadRef:   "middleman/issue-42",
			WorktreePath: "/tmp/ws-issue-1",
			TmuxSession:  "ws-issue-1",
			Status:       "creating",
		}
		require.NoError(t, d.InsertWorkspace(ctx, ws))

		dup := &Workspace{
			ID:           "ws-issue-2",
			PlatformHost: "github.com",
			RepoOwner:    "acme",
			RepoName:     "widget-issues",
			ItemType:     WorkspaceItemTypeIssue,
			ItemNumber:   42,
			GitHeadRef:   "middleman/issue-42-copy",
			WorktreePath: "/tmp/ws-issue-2",
			TmuxSession:  "ws-issue-2",
			Status:       "creating",
		}
		err := d.InsertWorkspace(ctx, dup)
		require.Error(t, err)
	})

	t.Run("pull request and issue can coexist", func(t *testing.T) {
		pr := &Workspace{
			ID:           "ws-mixed-pr",
			PlatformHost: "github.com",
			RepoOwner:    "acme",
			RepoName:     "widget-mixed",
			ItemType:     WorkspaceItemTypePullRequest,
			ItemNumber:   7,
			GitHeadRef:   "feat/mixed-pr",
			WorktreePath: "/tmp/ws-mixed-pr",
			TmuxSession:  "ws-mixed-pr",
			Status:       "creating",
		}
		require.NoError(t, d.InsertWorkspace(ctx, pr))

		issue := &Workspace{
			ID:           "ws-mixed-issue",
			PlatformHost: "github.com",
			RepoOwner:    "acme",
			RepoName:     "widget-mixed",
			ItemType:     WorkspaceItemTypeIssue,
			ItemNumber:   7,
			GitHeadRef:   "middleman/issue-7",
			WorktreePath: "/tmp/ws-mixed-issue",
			TmuxSession:  "ws-mixed-issue",
			Status:       "creating",
		}
		require.NoError(t, d.InsertWorkspace(ctx, issue))
	})
}

func TestWorkspaceUniqueConstraintIncludesPlatform(t *testing.T) {
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()

	_, err := d.UpsertRepo(ctx, RepoIdentity{
		Platform:     "github",
		PlatformHost: "code.example.com",
		Owner:        "acme",
		Name:         "widget",
	})
	require.NoError(err)
	_, err = d.UpsertRepo(ctx, RepoIdentity{
		Platform:     "gitlab",
		PlatformHost: "code.example.com",
		Owner:        "acme",
		Name:         "widget",
	})
	require.NoError(err)

	require.NoError(d.InsertWorkspace(ctx, &Workspace{
		ID:           "github-workspace",
		Platform:     "github",
		PlatformHost: "code.example.com",
		RepoOwner:    "acme",
		RepoName:     "widget",
		ItemType:     WorkspaceItemTypePullRequest,
		ItemNumber:   7,
		GitHeadRef:   "feature",
		WorktreePath: "/tmp/github-workspace",
		TmuxSession:  "github-workspace",
	}))
	require.NoError(d.InsertWorkspace(ctx, &Workspace{
		ID:           "gitlab-workspace",
		Platform:     "gitlab",
		PlatformHost: "code.example.com",
		RepoOwner:    "acme",
		RepoName:     "widget",
		ItemType:     WorkspaceItemTypePullRequest,
		ItemNumber:   7,
		GitHeadRef:   "feature",
		WorktreePath: "/tmp/gitlab-workspace",
		TmuxSession:  "gitlab-workspace",
	}))
}

func TestWorkspaceSummariesDoNotJoinAcrossProviders(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()

	githubRepoID, err := d.UpsertRepo(ctx, RepoIdentity{
		Platform:     "github",
		PlatformHost: "code.example.com",
		Owner:        "acme",
		Name:         "widget",
	})
	require.NoError(err)
	gitlabRepoID, err := d.UpsertRepo(ctx, RepoIdentity{
		Platform:     "gitlab",
		PlatformHost: "code.example.com",
		Owner:        "acme",
		Name:         "widget",
	})
	require.NoError(err)
	insertTestMRWithOptions(t, d, testMR(githubRepoID, 7, withMRTitle("github PR")))
	insertTestMRWithOptions(t, d, testMR(gitlabRepoID, 7, withMRTitle("gitlab MR")))

	require.NoError(d.InsertWorkspace(ctx, &Workspace{
		ID:           "gitlab-workspace",
		Platform:     "gitlab",
		PlatformHost: "code.example.com",
		RepoOwner:    "acme",
		RepoName:     "widget",
		ItemType:     WorkspaceItemTypePullRequest,
		ItemNumber:   7,
		GitHeadRef:   "feature",
		WorktreePath: "/tmp/gitlab-workspace",
		TmuxSession:  "gitlab-workspace",
	}))

	summary, err := d.GetWorkspaceSummary(ctx, "gitlab-workspace")
	require.NoError(err)
	require.NotNil(summary)
	assert.Equal("gitlab", summary.Platform)
	require.NotNil(summary.MRTitle)
	assert.Equal("gitlab MR", *summary.MRTitle)
}

func TestWorkspaceSummaries(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	base := baseTime()

	// Seed repo, issue, and PR.
	repoID := insertTestRepo(t, d, "acme", "widget")
	insertTestIssue(
		t, d, repoID, 7,
		"Track workspace association",
		base.Add(-time.Minute),
	)
	_, err := d.UpsertMergeRequest(ctx, &MergeRequest{
		RepoID:         repoID,
		PlatformID:     5001,
		Number:         42,
		URL:            "https://github.com/acme/widget/pull/42",
		Title:          "Add workspace support",
		Author:         "alice",
		State:          "open",
		IsDraft:        true,
		CIStatus:       "pending",
		ReviewDecision: "REVIEW_REQUIRED",
		Additions:      100,
		Deletions:      20,
		CreatedAt:      base,
		UpdatedAt:      base,
		LastActivityAt: base,
	})
	require.NoError(err)

	// PR workspace with matching PR (earlier created_at).
	_, err = d.WriteDB().ExecContext(ctx, `
		INSERT INTO middleman_workspaces
		    (id, platform_host, repo_owner, repo_name,
		     item_type, item_number, git_head_ref,
		     worktree_path, tmux_session, status,
		     created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"ws-with-mr", "github.com", "acme", "widget",
		WorkspaceItemTypePullRequest, 42, "feat/workspace",
		"/tmp/ws-with-mr", "ws-with-mr", "ready",
		base,
	)
	require.NoError(err)

	// Issue workspace with owner issue metadata and associated PR metadata.
	_, err = d.WriteDB().ExecContext(ctx, `
		INSERT INTO middleman_workspaces
		    (id, platform_host, repo_owner, repo_name,
		     item_type, item_number, associated_pr_number, git_head_ref,
		     worktree_path, tmux_session, status,
		     created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"ws-issue-with-pr", "github.com", "acme", "widget",
		WorkspaceItemTypeIssue, 7, 42, "feature/from-issue",
		"/tmp/ws-issue-with-pr", "ws-issue-with-pr", "ready",
		base.Add(30*time.Minute),
	)
	require.NoError(err)

	// Workspace without matching PR (later created_at, no repo).
	_, err = d.WriteDB().ExecContext(ctx, `
		INSERT INTO middleman_workspaces
		    (id, platform_host, repo_owner, repo_name,
		     item_type, item_number, git_head_ref,
		     worktree_path, tmux_session, status,
		     created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"ws-no-mr", "github.com", "acme", "gadget",
		WorkspaceItemTypePullRequest, 99, "fix/thing",
		"/tmp/ws-no-mr", "ws-no-mr", "creating",
		base.Add(time.Hour),
	)
	require.NoError(err)

	// ListWorkspaceSummaries
	summaries, err := d.ListWorkspaceSummaries(ctx)
	require.NoError(err)
	require.Len(summaries, 3)

	// Newest first.
	noMR := summaries[0]
	issueWithPR := summaries[1]
	withMR := summaries[2]
	assert.Equal("ws-no-mr", noMR.ID)
	assert.Equal("ws-issue-with-pr", issueWithPR.ID)
	assert.Equal("ws-with-mr", withMR.ID)

	// Owner-derived fields nil when no owner match.
	assert.Nil(noMR.MRTitle)
	assert.Nil(noMR.MRState)
	assert.Nil(noMR.MRIsDraft)
	assert.Nil(noMR.MRCIStatus)
	assert.Nil(noMR.MRReviewDecision)
	assert.Nil(noMR.MRAdditions)
	assert.Nil(noMR.MRDeletions)
	assert.Nil(noMR.AssociatedPRNumber)

	// Issue workspace keeps issue-owned header metadata and the linked PR number.
	require.NotNil(issueWithPR.MRTitle)
	assert.Equal("Track workspace association", *issueWithPR.MRTitle)
	require.NotNil(issueWithPR.MRState)
	assert.Equal("open", *issueWithPR.MRState)
	assert.Nil(issueWithPR.MRIsDraft)
	assert.Nil(issueWithPR.MRCIStatus)
	assert.Nil(issueWithPR.MRReviewDecision)
	assert.Nil(issueWithPR.MRAdditions)
	assert.Nil(issueWithPR.MRDeletions)
	require.NotNil(issueWithPR.AssociatedPRNumber)
	assert.Equal(42, *issueWithPR.AssociatedPRNumber)

	// PR workspace exposes PR metadata in the owner slots.
	require.NotNil(withMR.MRTitle)
	assert.Equal("Add workspace support", *withMR.MRTitle)
	require.NotNil(withMR.MRState)
	assert.Equal("open", *withMR.MRState)
	require.NotNil(withMR.MRIsDraft)
	assert.True(*withMR.MRIsDraft)
	require.NotNil(withMR.MRCIStatus)
	assert.Equal("pending", *withMR.MRCIStatus)
	require.NotNil(withMR.MRReviewDecision)
	assert.Equal("REVIEW_REQUIRED", *withMR.MRReviewDecision)
	require.NotNil(withMR.MRAdditions)
	assert.Equal(100, *withMR.MRAdditions)
	require.NotNil(withMR.MRDeletions)
	assert.Equal(20, *withMR.MRDeletions)
	assert.Nil(withMR.AssociatedPRNumber)

	// GetWorkspaceSummary by ID
	single, err := d.GetWorkspaceSummary(ctx, "ws-issue-with-pr")
	require.NoError(err)
	require.NotNil(single)
	assert.Equal("ws-issue-with-pr", single.ID)
	require.NotNil(single.MRTitle)
	assert.Equal("Track workspace association", *single.MRTitle)
	require.NotNil(single.AssociatedPRNumber)
	assert.Equal(42, *single.AssociatedPRNumber)

	// GetWorkspaceSummary miss
	missSum, err := d.GetWorkspaceSummary(ctx, "nonexistent")
	require.NoError(err)
	assert.Nil(missSum)
}

func TestSetWorkspaceAssociatedPRNumberIfNull(t *testing.T) {
	assert := Assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := context.Background()

	_, err := d.WriteDB().ExecContext(ctx, `
		INSERT INTO middleman_workspaces
		    (id, platform_host, repo_owner, repo_name,
		     item_type, item_number, git_head_ref,
		     worktree_path, tmux_session, status)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"ws-issue", "github.com", "acme", "widget",
		WorkspaceItemTypeIssue, 7, "feature/issue-7",
		"/tmp/ws-issue", "ws-issue", "ready",
	)
	require.NoError(err)

	changed, err := d.SetWorkspaceAssociatedPRNumberIfNull(
		ctx, "ws-issue", 42,
	)
	require.NoError(err)
	assert.True(changed)

	ws, err := d.GetWorkspace(ctx, "ws-issue")
	require.NoError(err)
	require.NotNil(ws)
	require.NotNil(ws.AssociatedPRNumber)
	assert.Equal(42, *ws.AssociatedPRNumber)

	changed, err = d.SetWorkspaceAssociatedPRNumberIfNull(
		ctx, "ws-issue", 99,
	)
	require.NoError(err)
	assert.False(changed)

	ws, err = d.GetWorkspace(ctx, "ws-issue")
	require.NoError(err)
	require.NotNil(ws)
	require.NotNil(ws.AssociatedPRNumber)
	assert.Equal(42, *ws.AssociatedPRNumber)
}

func TestUpdateMRTitleBody(t *testing.T) {
	assert := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	base := baseTime()

	repoID := insertTestRepo(t, d, "owner", "repo")
	mr := &MergeRequest{
		RepoID:         repoID,
		PlatformID:     12345,
		Number:         1,
		URL:            "https://github.com/owner/repo/pull/1",
		Title:          "original title",
		Author:         "author",
		State:          "open",
		Body:           "original body",
		HeadBranch:     "feature",
		BaseBranch:     "main",
		CommentCount:   5,
		CIStatus:       "success",
		ReviewDecision: "APPROVED",
		CreatedAt:      base,
		UpdatedAt:      base,
		LastActivityAt: base,
	}
	id, err := d.UpsertMergeRequest(ctx, mr)
	assert.NoError(err)

	ghUpdatedAt := base.Add(10 * time.Minute)
	assert.NoError(d.UpdateMRTitleBody(ctx, id, "new title", "new body", ghUpdatedAt))

	got, err := d.GetMergeRequestByRepoIDAndNumber(ctx, repoID, 1)
	assert.NoError(err)
	assert.NotNil(got)
	assert.Equal("new title", got.Title)
	assert.Equal("new body", got.Body)
	assert.True(got.UpdatedAt.Equal(ghUpdatedAt), "UpdatedAt should be ghUpdatedAt")
	assert.True(got.LastActivityAt.Equal(ghUpdatedAt), "LastActivityAt should be ghUpdatedAt")
	// Derived fields must be preserved.
	assert.Equal(5, got.CommentCount)
	assert.Equal("success", got.CIStatus)
	assert.Equal("APPROVED", got.ReviewDecision)
}

func TestUpdateMRTitleBodyPreservesNewerActivity(t *testing.T) {
	assert := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	base := baseTime()

	repoID := insertTestRepo(t, d, "owner2", "repo2")
	futureActivity := base.Add(1 * time.Hour)
	mr := &MergeRequest{
		RepoID:         repoID,
		PlatformID:     99999,
		Number:         2,
		URL:            "https://github.com/owner2/repo2/pull/2",
		Title:          "initial title",
		Author:         "author",
		State:          "open",
		HeadBranch:     "feature",
		BaseBranch:     "main",
		CreatedAt:      base,
		UpdatedAt:      base,
		LastActivityAt: futureActivity,
	}
	id, err := d.UpsertMergeRequest(ctx, mr)
	assert.NoError(err)

	// updatedAt is 30 min, newer than base so the update applies.
	updatedAt := base.Add(30 * time.Minute)
	assert.NoError(d.UpdateMRTitleBody(ctx, id, "new title", "new body", updatedAt))

	got, err := d.GetMergeRequestByRepoIDAndNumber(ctx, repoID, 2)
	assert.NoError(err)
	assert.NotNil(got)
	// UpdatedAt gets the 30-min value.
	assert.True(got.UpdatedAt.Equal(updatedAt), "UpdatedAt should be updatedAt")
	// LastActivityAt keeps the newer 1-hour value.
	assert.True(got.LastActivityAt.Equal(futureActivity), "LastActivityAt should keep newer value")
}

func TestUpdateMRTitleBodyIgnoresStaleUpdate(t *testing.T) {
	assert := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	base := baseTime()

	repoID := insertTestRepo(t, d, "owner3", "repo3")
	newerUpdatedAt := base.Add(1 * time.Hour)
	mr := &MergeRequest{
		RepoID:         repoID,
		PlatformID:     77777,
		Number:         3,
		URL:            "https://github.com/owner3/repo3/pull/3",
		Title:          "current title",
		Author:         "author",
		State:          "open",
		Body:           "current body",
		HeadBranch:     "feature",
		BaseBranch:     "main",
		CreatedAt:      base,
		UpdatedAt:      newerUpdatedAt,
		LastActivityAt: newerUpdatedAt,
	}
	id, err := d.UpsertMergeRequest(ctx, mr)
	assert.NoError(err)

	// Stale update: updatedAt is older than existing row.
	staleAt := base.Add(30 * time.Minute)
	assert.NoError(d.UpdateMRTitleBody(ctx, id, "stale title", "stale body", staleAt))

	got, err := d.GetMergeRequestByRepoIDAndNumber(ctx, repoID, 3)
	assert.NoError(err)
	assert.NotNil(got)
	assert.Equal("current title", got.Title, "stale update should be ignored")
	assert.Equal("current body", got.Body, "stale update should be ignored")
	assert.True(got.UpdatedAt.Equal(newerUpdatedAt), "updated_at should not regress")
}
