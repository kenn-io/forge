package db

import (
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func headRepoKindOf(t *testing.T, d *DB, repoID int64, number int) string {
	t.Helper()
	var kind string
	require.NoError(t, d.ReadDB().QueryRowContext(t.Context(),
		`SELECT head_repo_kind FROM forge_merge_requests WHERE repo_id = ? AND number = ?`, repoID, number,
	).Scan(&kind))
	return kind
}

func referenceIssueFromPR(t *testing.T, d *DB, issueID int64, number int, at time.Time) {
	t.Helper()
	require.NoError(t, d.UpsertIssueEvents(t.Context(), []IssueEvent{{
		IssueID:      issueID,
		EventType:    "cross_referenced",
		MetadataJSON: `{"source_type":"PullRequest","source_owner":"acme","source_repo":"client","source_number":42,"source_url":"https://github.com/acme/client/pull/42"}`,
		CreatedAt:    at,
		DedupeKey:    fmt.Sprintf("cross-reference-%d", number),
	}}))
}

// Sync stores each pull request's fork classification, so From fork runs in SQL. A snapshot
// that does not know the head repository keeps the stored URL and its classification.
func TestUpsertMergeRequestStoresHeadRepoKind(t *testing.T) {
	t.Parallel()
	assert := assert.New(t)
	d := openTestDB(t)
	repoID := insertTestRepo(t, d, "acme", "widget")
	gitlabID, err := seedTestRepo(t.Context(), d, verifiedTestRepoIdentity("gitlab", "gitlab.com", "group/sub", "widget"))
	require.NoError(t, err)

	withHead := func(url string) testMROpt { return func(mr *MergeRequest) { mr.HeadRepoCloneURL = url } }
	insertTestMRWithOptions(t, d, testMR(repoID, 1, withHead("https://github.com/contributor/widget.git")))
	insertTestMRWithOptions(t, d, testMR(repoID, 2, withHead("git@github.com:ACME/widget.git")))
	insertTestMRWithOptions(t, d, testMR(repoID, 3, withHead("")))
	insertTestMRWithOptions(t, d, testMR(gitlabID, 4, withHead("https://gitlab.com/group/sub/widget.git")))
	assert.Equal("fork", headRepoKindOf(t, d, repoID, 1))
	assert.Equal("same_repo", headRepoKindOf(t, d, repoID, 2))
	assert.Equal("unknown", headRepoKindOf(t, d, repoID, 3))
	assert.Equal("same_repo", headRepoKindOf(t, d, gitlabID, 4), "nested GitLab paths name their own project")

	later := func(mr *MergeRequest) { mr.UpdatedAt = mr.UpdatedAt.Add(time.Hour) }
	insertTestMRWithOptions(t, d, testMR(repoID, 1, later, withHead(""), func(mr *MergeRequest) { mr.HeadRepoCloneURLUnknown = true }))
	assert.Equal("fork", headRepoKindOf(t, d, repoID, 1), "an unknown head keeps the stored classification")
	insertTestMRWithOptions(t, d, testMR(repoID, 2, later, withHead("https://github.com/contributor/widget.git")))
	assert.Equal("fork", headRepoKindOf(t, d, repoID, 2), "a new head URL is classified again")
}

// Migration 96 classifies rows synced before it with the same Go classifier sync uses.
func TestHeadRepoKindMigrationBackfillsExistingPulls(t *testing.T) {
	t.Parallel()
	assert := assert.New(t)
	require := require.New(t)
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "head-repo-kind-v95.db")
	raw := migrateToVersionForTest(t, path, 95)
	require.NoError(raw.Close())

	previous, err := OpenPreparedForTest(path)
	require.NoError(err)
	t.Cleanup(func() { previous.Close() })
	repoID := insertTestRepo(t, previous, "acme", "widget")
	for number, url := range map[int]string{
		1: "https://github.com/contributor/widget.git",
		2: "https://github.com/acme/widget.git",
		3: "",
	} {
		_, err := previous.WriteDB().ExecContext(ctx, `
			INSERT INTO forge_merge_requests
				(repo_id, platform_id, number, url, title, author, state, head_branch, base_branch,
				 head_repo_clone_url, created_at, updated_at, last_activity_at)
			VALUES (?, ?, ?, '', 'PR', 'author', 'open', 'feature', 'main', ?, ?, ?, ?)`,
			repoID, number, number, url, baseTime(), baseTime(), baseTime())
		require.NoError(err)
	}
	require.NoError(previous.Close())

	database, err := Open(path)
	require.NoError(err)
	t.Cleanup(func() { database.Close() })
	assert.Equal("fork", headRepoKindOf(t, database, repoID, 1))
	assert.Equal("same_repo", headRepoKindOf(t, database, repoID, 2))
	assert.Equal("unknown", headRepoKindOf(t, database, repoID, 3))
}

// Workspace, fork, and label filters on the pull request list all apply before the limit.
func TestListMergeRequestsWorkspaceForkAndLabelFiltersApplyBeforeLimit(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	d := openTestDB(t)
	repoID := insertTestRepo(t, d, "acme", "widget")
	for _, row := range []struct {
		number int
		head   string
		labels []string
	}{
		{number: 1, head: "https://github.com/contributor/widget.git", labels: []string{"Bug"}},
		{number: 2, head: "https://github.com/acme/widget.git", labels: []string{"bug", "WontFix"}},
		{number: 3, head: "", labels: []string{"Équipe", "Straße"}},
		{number: 4, head: "https://github.com/acme/widget.git", labels: []string{"ÉQUIPE"}},
	} {
		pr := testMR(repoID, row.number, withMRActivity(baseTime().Add(time.Duration(row.number)*time.Minute)))
		pr.HeadRepoCloneURL = row.head
		id := insertTestMRWithOptions(t, d, pr)
		labels := []Label{}
		for _, name := range row.labels {
			labels = append(labels, Label{Name: name, Color: "ffffff"})
		}
		require.NoError(t, d.ReplaceMergeRequestLabels(ctx, repoID, id, labels))
	}
	workspaces := []RepoItemNumber{{RepoID: repoID, Number: 1}}
	for _, tc := range []struct {
		name string
		opts ListMergeRequestsOpts
		want []int
	}{
		{"from a fork", ListMergeRequestsOpts{Attributes: []string{"fork"}, Limit: 1}, []int{1}},
		{"not from a fork, unknown heads included", ListMergeRequestsOpts{Attributes: []string{"not_fork"}}, []int{4, 3, 2}},
		{"with a workspace", ListMergeRequestsOpts{Attributes: []string{"has_workspace"}, WorkspaceItems: workspaces, Limit: 1}, []int{1}},
		{"without a workspace", ListMergeRequestsOpts{Attributes: []string{"no_workspace"}, WorkspaceItems: workspaces}, []int{4, 3, 2}},
		{"with a workspace when none exist", ListMergeRequestsOpts{Attributes: []string{"has_workspace"}}, []int{}},
		{"either workspace choice", ListMergeRequestsOpts{Attributes: []string{"has_workspace", "no_workspace"}, WorkspaceItems: workspaces}, []int{4, 3, 2, 1}},
		{"workspace before held merge", ListMergeRequestsOpts{
			Attributes:         []string{"has_workspace", "not_merge_queued"},
			WorkspaceItems:     []RepoItemNumber{{RepoID: repoID, Number: 2}},
			DeferredMergeItems: []RepoItemNumber{{RepoID: repoID, Number: 1}},
		}, []int{2}},
		{"held merge before workspace", ListMergeRequestsOpts{
			Attributes:         []string{"not_merge_queued", "has_workspace"},
			WorkspaceItems:     []RepoItemNumber{{RepoID: repoID, Number: 2}},
			DeferredMergeItems: []RepoItemNumber{{RepoID: repoID, Number: 1}},
		}, []int{2}},
		{"required label in any case", ListMergeRequestsOpts{Labels: []string{"BUG"}}, []int{2, 1}},
		{"required and excluded labels", ListMergeRequestsOpts{Labels: []string{"bug"}, ExcludeLabels: []string{"wontfix"}, Limit: 1}, []int{1}},
		{"excluded label only", ListMergeRequestsOpts{ExcludeLabels: []string{"BUG"}}, []int{4, 3}},
		{"exact label still exact", ListMergeRequestsOpts{Label: "bug"}, []int{2}},
		{"required label folds non-ASCII case", ListMergeRequestsOpts{Labels: []string{"équipe"}}, []int{4, 3}},
		{"excluded label folds non-ASCII case", ListMergeRequestsOpts{ExcludeLabels: []string{"équipe", "bug"}}, []int{}},
		{"required label uses full case folding", ListMergeRequestsOpts{Labels: []string{"STRASSE"}}, []int{3}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rows, err := d.ListMergeRequests(ctx, tc.opts)
			require.NoError(t, err)
			numbers := []int{}
			for _, row := range rows {
				numbers = append(numbers, row.Number)
			}
			assert.Equal(t, tc.want, numbers)
		})
	}
}

// Issue labels and Hide bots apply before the limit; bot authors follow bot-author.ts per provider.
func TestListIssuesLabelAndBotFiltersApplyBeforeLimit(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	d := openTestDB(t)
	githubID := insertTestRepo(t, d, "acme", "widget")
	gitlabID, err := seedTestRepo(ctx, d, verifiedTestRepoIdentity("gitlab", "gitlab.com", "acme", "widget"))
	require.NoError(t, err)
	for _, row := range []struct {
		repoID int64
		number int
		author string
		labels []string
	}{
		{githubID, 1, "human", []string{"Bug"}},
		{githubID, 2, "renovate[bot]", []string{"bug"}},
		{githubID, 3, "Deploy-Bot", nil},
		{githubID, 4, "project_12_bot_abc123", []string{"BUG", "Duplicate"}},
		{gitlabID, 5, "project_12_bot_abc123", nil},
		{gitlabID, 6, "Group_7_Bot_X9", nil},
		{gitlabID, 7, "project_12x_bot_abc", nil},
		{gitlabID, 8, "project_12_bot_a_b", nil},
		{gitlabID, 9, "group_7_bot_", nil},
		{gitlabID, 10, "someone[bot]", nil},
	} {
		issue := testIssue(row.repoID, row.number, withIssueAuthor(row.author),
			withIssueActivity(baseTime().Add(time.Duration(row.number)*time.Minute)))
		id := insertTestIssueWithOptions(t, d, issue)
		labels := []Label{}
		for _, name := range row.labels {
			labels = append(labels, Label{Name: name, Color: "ffffff"})
		}
		require.NoError(t, d.ReplaceIssueLabels(ctx, row.repoID, id, labels))
	}
	for _, tc := range []struct {
		name string
		opts ListIssuesOpts
		want []int
	}{
		{"hide bots", ListIssuesOpts{HideBots: true}, []int{10, 9, 8, 7, 4, 1}},
		{"hide bots before the limit", ListIssuesOpts{HideBots: true, Labels: []string{"bug"}, Limit: 1}, []int{4}},
		{"required label in any case", ListIssuesOpts{Labels: []string{"bUg"}}, []int{4, 2, 1}},
		{"required and excluded labels", ListIssuesOpts{Labels: []string{"bug"}, ExcludeLabels: []string{"DUPLICATE"}, Limit: 1}, []int{2}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rows, err := d.ListIssues(ctx, tc.opts)
			require.NoError(t, err)
			numbers := []int{}
			for _, row := range rows {
				numbers = append(numbers, row.Number)
			}
			assert.Equal(t, tc.want, numbers)
		})
	}
}

// Activity applies PR attributes only to pull request rows and issue filters only to issue rows,
// in SQL before the limit, on the flat feed, the subject list, and the collapsed projection.
func TestActivityItemFiltersApplyBeforeLimitToTheirItemType(t *testing.T) {
	t.Parallel()
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	now := baseTime()
	since := now.Add(-24 * time.Hour)
	repoID := insertTestRepo(t, d, "acme", "widget")

	// Older rows have smaller numbers, so a limit of one keeps the newest match.
	at := func(number int) time.Time { return now.Add(time.Duration(number-20) * time.Minute) }
	pull := func(number int, head string, labels ...string) {
		pr := testMR(repoID, number, withMRActivity(at(number)))
		pr.HeadRepoCloneURL = head
		id := insertTestMRWithOptions(t, d, pr)
		set := []Label{}
		for _, name := range labels {
			set = append(set, Label{Name: name, Color: "ffffff"})
		}
		require.NoError(d.ReplaceMergeRequestLabels(ctx, repoID, id, set))
	}
	issue := func(number int, author string, labels ...string) int64 {
		id := insertTestIssueWithOptions(t, d, testIssue(repoID, number, withIssueAuthor(author), withIssueActivity(at(number))))
		set := []Label{}
		for _, name := range labels {
			set = append(set, Label{Name: name, Color: "ffffff"})
		}
		require.NoError(d.ReplaceIssueLabels(ctx, repoID, id, set))
		return id
	}
	pull(1, "https://github.com/contributor/widget.git", "Bug")
	pull(2, "https://github.com/acme/widget.git", "bug", "wontfix")
	pull(3, "")
	issue(4, "human", "BUG")
	referencedID := issue(5, "dependabot[bot]")
	referenceIssueFromPR(t, d, referencedID, 5, at(5))
	issue(6, "human")
	require.NoError(d.SetStarred(ctx, ItemTypePR, repoID, 1))
	require.NoError(d.SetStarred(ctx, ItemTypeIssue, repoID, 4))

	keysOf := func(itemType string, number int) string { return fmt.Sprintf("%s:%d", itemType, number) }
	feed := func(opts ListActivityOpts, limit int) []string {
		opts.Since, opts.Limit = &since, limit
		items, err := d.ListActivity(ctx, opts)
		require.NoError(err)
		keys := []string{}
		for _, item := range items {
			if key := keysOf(item.ItemType, item.ItemNumber); !slices.Contains(keys, key) {
				keys = append(keys, key)
			}
		}
		return keys
	}
	subjects := func(opts ListActivityOpts, limit int) []string {
		subjectOpts := ActivitySubjectsOpts(opts, nil, limit)
		subjectOpts.Since = &since
		rows, err := d.ListActivitySubjects(ctx, subjectOpts)
		require.NoError(err)
		keys := []string{}
		for _, row := range rows {
			keys = append(keys, keysOf(row.Subject.Key.ItemType, row.Subject.Key.ItemNumber))
		}
		return keys
	}
	collapsed := func(opts ListActivityOpts, limit int) []string {
		opts.Since, opts.Limit = &since, 50
		projection, err := d.ListCollapsedActivityProjection(ctx, ListActivityProjectionOpts{ListActivityOpts: opts, SubjectLimit: limit})
		require.NoError(err)
		keys := []string{}
		for _, row := range projection.Subjects {
			keys = append(keys, keysOf(row.Subject.Key.ItemType, row.Subject.Key.ItemNumber))
		}
		return keys
	}
	workspaces := []RepoItemNumber{{RepoID: repoID, Number: 2}}
	issues := []string{"issue:6", "issue:5", "issue:4"}
	pulls := []string{"pr:3", "pr:2", "pr:1"}
	for _, tc := range []struct {
		name  string
		opts  ListActivityOpts
		all   []string
		first string
	}{
		{"from a fork narrows only PRs", ListActivityOpts{PullAttributes: []string{"fork"}}, append(slices.Clone(issues), "pr:1"), "issue:6"},
		{"not from a fork keeps unknown heads", ListActivityOpts{PullAttributes: []string{"not_fork"}}, append(slices.Clone(issues), "pr:3", "pr:2"), "issue:6"},
		{"with a workspace narrows only PRs", ListActivityOpts{PullAttributes: []string{"has_workspace"}, WorkspaceItems: workspaces}, append(slices.Clone(issues), "pr:2"), "issue:6"},
		{"without a workspace", ListActivityOpts{PullAttributes: []string{"no_workspace"}, WorkspaceItems: workspaces}, append(slices.Clone(issues), "pr:3", "pr:1"), "issue:6"},
		{"Hide bots narrows only issues", ListActivityOpts{HideBotAuthoredIssues: true}, append([]string{"issue:6", "issue:4"}, pulls...), "issue:6"},
		{"referenced by a PR narrows only issues", ListActivityOpts{ReferencedByPR: true}, append([]string{"issue:5"}, pulls...), "issue:5"},
		{"not referenced by a PR", ListActivityOpts{NotReferencedByPR: true}, append([]string{"issue:6", "issue:4"}, pulls...), "issue:6"},
		{"starred covers both types", ListActivityOpts{Starred: true}, []string{"issue:4", "pr:1"}, "issue:4"},
		{"required label in any case", ListActivityOpts{Labels: []string{"bUG"}}, []string{"issue:4", "pr:2", "pr:1"}, "issue:4"},
		{"required and excluded labels", ListActivityOpts{Labels: []string{"bug"}, ExcludeLabels: []string{"WONTFIX"}}, []string{"issue:4", "pr:1"}, "issue:4"},
		{"PR filter and issue filter together", ListActivityOpts{PullAttributes: []string{"fork"}, ReferencedByPR: true}, []string{"issue:5", "pr:1"}, "issue:5"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.ElementsMatch(t, tc.all, feed(tc.opts, 50), "flat")
			assert.Equal(t, []string{tc.first}, feed(tc.opts, 1), "flat, limited")
			assert.ElementsMatch(t, tc.all, subjects(tc.opts, 50), "subjects")
			assert.Equal(t, []string{tc.first}, subjects(tc.opts, 1), "subjects, limited")
			assert.ElementsMatch(t, tc.all, collapsed(tc.opts, 50), "collapsed")
			assert.Equal(t, []string{tc.first}, collapsed(tc.opts, 1), "collapsed, limited")
		})
	}

	// The limited reads above start with issues; these show a PR-only match reaches past newer rows.
	assert.Equal(t, []string{"pr:1"}, feed(ListActivityOpts{
		PullAttributes: []string{"fork"}, ItemTypes: []string{"pr"},
	}, 1))
	assert.Equal(t, []string{"pr:1"}, subjects(ListActivityOpts{
		Labels: []string{"bug"}, ExcludeLabels: []string{"wontfix", "BUG-x"},
		ItemTypes: []string{"pr"},
	}, 1))
}

// Workspace subjects take Activity's PR attributes and item filters through a key lookup.
func TestListWorkspaceSubjectsMatchingFilters(t *testing.T) {
	t.Parallel()
	assert := assert.New(t)
	require := require.New(t)
	d := openTestDB(t)
	ctx := t.Context()
	repoID := insertTestRepo(t, d, "acme", "widget")
	insertTestMRWithOptions(t, d, testMR(repoID, 1, func(mr *MergeRequest) {
		mr.ReviewDecision = "APPROVED"
		mr.HeadRepoCloneURL = "https://github.com/contributor/widget.git"
	}))
	insertTestMRWithOptions(t, d, testMR(repoID, 2, func(mr *MergeRequest) { mr.IsDraft = true }))
	insertTestIssueWithOptions(t, d, testIssue(repoID, 3, withIssueAuthor("renovate[bot]")))
	insertTestIssueWithOptions(t, d, testIssue(repoID, 4, withIssueAuthor("human")))
	keys := []WorkspaceSubjectKey{
		{RepoID: repoID, ItemType: WorkspaceItemTypePullRequest, ItemNumber: 1},
		{RepoID: repoID, ItemType: WorkspaceItemTypePullRequest, ItemNumber: 2},
		{RepoID: repoID, ItemType: WorkspaceItemTypeIssue, ItemNumber: 3},
		{RepoID: repoID, ItemType: WorkspaceItemTypeIssue, ItemNumber: 4},
	}
	matching := func(opts ListActivityOpts) []WorkspaceSubjectKey {
		out, err := d.ListWorkspaceSubjectsMatchingFilters(ctx, keys, opts)
		require.NoError(err)
		return slices.Collect(maps.Keys(out))
	}

	assert.ElementsMatch(keys, matching(ListActivityOpts{}), "no filters keep every subject")
	assert.ElementsMatch([]WorkspaceSubjectKey{keys[0], keys[2], keys[3]}, matching(ListActivityOpts{PullAttributes: []string{"approved"}}))
	assert.ElementsMatch([]WorkspaceSubjectKey{keys[0], keys[2], keys[3]}, matching(ListActivityOpts{PullAttributes: []string{"fork"}}))
	assert.ElementsMatch([]WorkspaceSubjectKey{keys[0], keys[1], keys[3]},
		matching(ListActivityOpts{HideBotAuthoredIssues: true}))

	cloneURLs, err := d.ListPullHeadRepoCloneURLs(ctx, keys)
	require.NoError(err)
	assert.Equal(map[WorkspaceSubjectKey]string{
		keys[0]: "https://github.com/contributor/widget.git",
		keys[1]: "",
	}, cloneURLs)
}
