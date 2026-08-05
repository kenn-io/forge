package github

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gh "github.com/google/go-github/v89/github"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/gitclone"
	"go.kenn.io/forge/internal/platform"
	gitcmd "go.kenn.io/kit/git/cmd"
)

type obsoleteTestHistory struct {
	sourceDir string
	manager   *gitclone.Manager
	base      string
	a1        string
	a2        string
	a3        string
	b1        string
	b2        string
}

type obsoleteStampingFixture struct {
	syncer   *Syncer
	database *db.DB
	repo     RepoRef
	repoID   int64
	mrID     int64
	history  obsoleteTestHistory
}

func obsoleteTestGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, stderr, err := gitcmd.New().Run(t.Context(), dir, nil, args...)
	require.NoError(t, err, "git %v: %s%s", args, out, stderr)
	return strings.TrimSpace(string(out))
}

func setupObsoleteTestHistory(t *testing.T) obsoleteTestHistory {
	t.Helper()
	dir := t.TempDir()
	sourceDir := filepath.Join(dir, "source")
	require.NoError(t, os.MkdirAll(sourceDir, 0o755))
	run := func(args ...string) string {
		return obsoleteTestGit(t, sourceDir, args...)
	}
	run("init", "-b", "main")
	run("config", "user.email", "fixture@example.invalid")
	run("config", "user.name", "Fixture")

	require.NoError(t, os.WriteFile(filepath.Join(sourceDir, "base.txt"), []byte("m1\n"), 0o644))
	run("add", "base.txt")
	run("commit", "-m", "base m1")
	base := run("rev-parse", "HEAD")

	run("checkout", "-b", "feature")
	lineageCommit := func(path, contents, message string) string {
		require.NoError(t, os.WriteFile(filepath.Join(sourceDir, path), []byte(contents), 0o644))
		run("add", path)
		run("commit", "-m", message)
		return run("rev-parse", "HEAD")
	}
	a1 := lineageCommit("lineage-a.txt", "a1\n", "lineage a1")
	a2 := lineageCommit("lineage-a.txt", "a2\n", "lineage a2")
	a3 := lineageCommit("lineage-a.txt", "a3\n", "lineage a3")

	run("checkout", "-b", "feature-b", base)
	b1 := lineageCommit("lineage-b.txt", "b1\n", "lineage b1")
	b2 := lineageCommit("lineage-b.txt", "b2\n", "lineage b2")

	// Advance main after both feature lineages fork. Stamping must depend only
	// on reachability from the MR head, never on the moving base branch.
	run("checkout", "main")
	require.NoError(t, os.WriteFile(filepath.Join(sourceDir, "base.txt"), []byte("m2\n"), 0o644))
	run("add", "base.txt")
	run("commit", "-m", "advance base")

	manager := gitclone.New(filepath.Join(dir, "clones"), nil)
	barePath, err := manager.ClonePath("github", "github.com", "owner", "repo")
	require.NoError(t, err)
	obsoleteTestGit(t, "", "clone", "--bare", sourceDir, barePath)

	return obsoleteTestHistory{
		sourceDir: sourceDir,
		manager:   manager,
		base:      base,
		a1:        a1,
		a2:        a2,
		a3:        a3,
		b1:        b1,
		b2:        b2,
	}
}

func setupObsoleteStampingFixture(t *testing.T) obsoleteStampingFixture {
	t.Helper()
	history := setupObsoleteTestHistory(t)
	database := openTestDB(t)
	repo := RepoRef{
		Platform:           platform.KindGitHub,
		PlatformHost:       "github.com",
		PlatformExternalID: "repo-owner-repo",
		Owner:              "owner",
		Name:               "repo",
		RepoPath:           "owner/repo",
		CloneURL:           history.sourceDir,
	}
	repoID, err := database.UpsertRepo(t.Context(), verifiedDBRepoIdentity(platformRepoRef(repo)))
	require.NoError(t, err)
	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	mrID, err := database.UpsertMergeRequest(t.Context(), &db.MergeRequest{
		RepoID:             repoID,
		PlatformID:         1,
		PlatformExternalID: "mr-1",
		Number:             1,
		URL:                "https://github.com/owner/repo/pull/1",
		Title:              "Synthetic merge request",
		Author:             "developer",
		State:              db.MergeRequestStateOpen,
		HeadBranch:         "feature",
		BaseBranch:         "main",
		PlatformHeadSHA:    history.a3,
		PlatformBaseSHA:    history.base,
		CreatedAt:          now.Add(-time.Hour),
		UpdatedAt:          now,
		LastActivityAt:     now,
	})
	require.NoError(t, err)
	return obsoleteStampingFixture{
		syncer:   &Syncer{db: database, clones: history.manager},
		database: database,
		repo:     repo,
		repoID:   repoID,
		mrID:     mrID,
		history:  history,
	}
}

func seedObsoleteCommitEvents(t *testing.T, fixture obsoleteStampingFixture, shas ...string) {
	t.Helper()
	events := make([]db.MREvent, 0, len(shas))
	createdAt := time.Date(2026, 8, 5, 10, 0, 0, 0, time.UTC)
	for i, sha := range shas {
		events = append(events, db.MREvent{
			MergeRequestID: fixture.mrID,
			EventType:      "commit",
			Summary:        sha,
			Body:           fmt.Sprintf("synthetic commit %d", i+1),
			MetadataJSON:   fmt.Sprintf(`{"commit_order_key":%d}`, i+1),
			CreatedAt:      createdAt.Add(time.Duration(i) * time.Minute),
			DedupeKey:      sha,
		})
	}
	require.NoError(t, fixture.database.UpsertMREvents(t.Context(), events))
}

func assertObsoleteCommitFlags(
	t *testing.T,
	fixture obsoleteStampingFixture,
	want map[string]bool,
) {
	t.Helper()
	assert := assert.New(t)
	events, err := fixture.database.ListMREvents(t.Context(), fixture.mrID)
	require.NoError(t, err)
	byKey := make(map[string]db.MREvent, len(events))
	for _, event := range events {
		byKey[event.DedupeKey] = event
	}
	for key, obsolete := range want {
		event, ok := byKey[key]
		require.True(t, ok, "missing event %s", key)
		var metadata map[string]any
		require.NoError(t, json.Unmarshal([]byte(event.MetadataJSON), &metadata))
		assert.Contains(metadata, "commit_order_key", "event %s lost existing metadata", key)
		value, present := metadata["obsolete"]
		if obsolete {
			assert.True(present, "event %s has no obsolete flag", key)
			assert.Equal(true, value, "event %s has the wrong obsolete value", key)
		} else {
			assert.False(present, "event %s unexpectedly has an obsolete flag", key)
		}
	}
}

func TestStampObsoleteCommitEventsReplaceAndRestore(t *testing.T) {
	fixture := setupObsoleteStampingFixture(t)
	h := fixture.history
	seedObsoleteCommitEvents(t, fixture, h.a1, h.a2, h.a3, h.b1, h.b2)

	require.NoError(t, fixture.syncer.stampObsoleteCommitEvents(t.Context(), fixture.repo, fixture.mrID, h.b2))
	assertObsoleteCommitFlags(t, fixture, map[string]bool{
		h.a1: true, h.a2: true, h.a3: true, h.b1: false, h.b2: false,
	})

	require.NoError(t, fixture.syncer.stampObsoleteCommitEvents(t.Context(), fixture.repo, fixture.mrID, h.a3))
	assertObsoleteCommitFlags(t, fixture, map[string]bool{
		h.a1: false, h.a2: false, h.a3: false, h.b1: true, h.b2: true,
	})

	require.NoError(t, fixture.syncer.stampObsoleteCommitEvents(t.Context(), fixture.repo, fixture.mrID, h.a2))
	assertObsoleteCommitFlags(t, fixture, map[string]bool{
		h.a1: false, h.a2: false, h.a3: true, h.b1: true, h.b2: true,
	})
}

func TestStampObsoleteCommitEventsIgnoresBaseAdvance(t *testing.T) {
	fixture := setupObsoleteStampingFixture(t)
	h := fixture.history
	seedObsoleteCommitEvents(t, fixture, h.a1, h.a2, h.a3)

	require.NoError(t, fixture.syncer.stampObsoleteCommitEvents(t.Context(), fixture.repo, fixture.mrID, h.a3))
	assertObsoleteCommitFlags(t, fixture, map[string]bool{h.a1: false, h.a2: false, h.a3: false})
}

func TestStampObsoleteCommitEventsSkipsWhenHeadMissing(t *testing.T) {
	fixture := setupObsoleteStampingFixture(t)
	h := fixture.history
	require.NoError(t, fixture.database.UpsertMREvents(t.Context(), []db.MREvent{{
		MergeRequestID: fixture.mrID,
		EventType:      "commit",
		Summary:        h.a1,
		MetadataJSON:   `{"commit_order_key":1,"obsolete":true}`,
		CreatedAt:      time.Date(2026, 8, 5, 10, 0, 0, 0, time.UTC),
		DedupeKey:      h.a1,
	}}))

	require.NoError(t, fixture.syncer.stampObsoleteCommitEvents(
		t.Context(), fixture.repo, fixture.mrID, strings.Repeat("d", 40),
	))
	assertObsoleteCommitFlags(t, fixture, map[string]bool{h.a1: true})
}

func TestStampObsoleteCommitEventsFlagsShaAbsentFromClone(t *testing.T) {
	fixture := setupObsoleteStampingFixture(t)
	absentSHA := strings.Repeat("f", 40)
	seedObsoleteCommitEvents(t, fixture, absentSHA)

	require.NoError(t, fixture.syncer.stampObsoleteCommitEvents(
		t.Context(), fixture.repo, fixture.mrID, fixture.history.a3,
	))
	assertObsoleteCommitFlags(t, fixture, map[string]bool{absentSHA: true})
}

func TestStampObsoleteCommitEventsSkipsNonShaSummaries(t *testing.T) {
	assert := assert.New(t)
	fixture := setupObsoleteStampingFixture(t)
	h := fixture.history
	events := []db.MREvent{
		{
			MergeRequestID: fixture.mrID,
			EventType:      "commit",
			Summary:        "not a commit SHA",
			MetadataJSON:   `{"commit_order_key":1}`,
			CreatedAt:      time.Date(2026, 8, 5, 10, 0, 0, 0, time.UTC),
			DedupeKey:      "non-sha",
		},
		{
			MergeRequestID: fixture.mrID,
			EventType:      "review",
			Summary:        h.a1,
			MetadataJSON:   `{"review":"approved"}`,
			CreatedAt:      time.Date(2026, 8, 5, 10, 1, 0, 0, time.UTC),
			DedupeKey:      "non-commit",
		},
	}
	require.NoError(t, fixture.database.UpsertMREvents(t.Context(), events))

	require.NoError(t, fixture.syncer.stampObsoleteCommitEvents(t.Context(), fixture.repo, fixture.mrID, h.b2))
	stored, err := fixture.database.ListMREvents(t.Context(), fixture.mrID)
	require.NoError(t, err)
	require.Len(t, stored, 2)
	byKey := map[string]db.MREvent{stored[0].DedupeKey: stored[0], stored[1].DedupeKey: stored[1]}
	assert.JSONEq(`{"commit_order_key":1}`, byKey["non-sha"].MetadataJSON)
	assert.JSONEq(`{"review":"approved"}`, byKey["non-commit"].MetadataJSON)
}

func TestStampObsoleteCommitEventsUsesPlatformExternalID(t *testing.T) {
	fixture := setupObsoleteStampingFixture(t)
	h := fixture.history
	require.NoError(t, fixture.database.UpsertMREvents(t.Context(), []db.MREvent{{
		MergeRequestID:     fixture.mrID,
		PlatformExternalID: strings.ToUpper(h.a1),
		EventType:          "commit",
		Summary:            "synthetic commit message",
		MetadataJSON:       `{"commit_order_key":1}`,
		CreatedAt:          time.Date(2026, 8, 5, 10, 0, 0, 0, time.UTC),
		DedupeKey:          "gitealike-commit",
	}}))

	require.NoError(t, fixture.syncer.stampObsoleteCommitEvents(t.Context(), fixture.repo, fixture.mrID, h.b2))
	assertObsoleteCommitFlags(t, fixture, map[string]bool{"gitealike-commit": true})
}

func TestStampObsoleteCommitEventsSkipsUnparseableMetadata(t *testing.T) {
	assert := assert.New(t)
	fixture := setupObsoleteStampingFixture(t)
	h := fixture.history
	require.NoError(t, fixture.database.UpsertMREvents(t.Context(), []db.MREvent{{
		MergeRequestID: fixture.mrID,
		EventType:      "commit",
		Summary:        h.a1,
		MetadataJSON:   `[1,2]`,
		CreatedAt:      time.Date(2026, 8, 5, 10, 0, 0, 0, time.UTC),
		DedupeKey:      h.a1,
	}}))

	require.NoError(t, fixture.syncer.stampObsoleteCommitEvents(t.Context(), fixture.repo, fixture.mrID, h.b2))
	stored, err := fixture.database.ListMREvents(t.Context(), fixture.mrID)
	require.NoError(t, err)
	require.Len(t, stored, 1)
	assert.Equal(`[1,2]`, stored[0].MetadataJSON)
}

func TestStampObsoleteCommitEventsSkipsPreviouslyStampedHead(t *testing.T) {
	assert := assert.New(t)
	fixture := setupObsoleteStampingFixture(t)
	h := fixture.history
	seedObsoleteCommitEvents(t, fixture, h.a1, h.a2, h.a3)

	require.NoError(t, fixture.syncer.stampObsoleteCommitEvents(
		t.Context(), fixture.repo, fixture.mrID, h.b2,
	))
	assertObsoleteCommitFlags(t, fixture, map[string]bool{
		h.a1: true, h.a2: true, h.a3: true,
	})
	before, err := fixture.database.ListMREvents(t.Context(), fixture.mrID)
	require.NoError(t, err)
	clonePath, err := h.manager.ClonePath("github", "github.com", "owner", "repo")
	require.NoError(t, err)
	require.NoError(t, os.RemoveAll(clonePath))

	// A successful stamp caches this exact head, so the steady-state call must
	// not touch the now-missing clone or rewrite any event rows.
	require.NoError(t, fixture.syncer.stampObsoleteCommitEvents(
		t.Context(), fixture.repo, fixture.mrID, h.b2,
	))
	after, err := fixture.database.ListMREvents(t.Context(), fixture.mrID)
	require.NoError(t, err)
	assert.Equal(before, after)

	// A different head is not covered by the cache and therefore attempts a
	// fresh clone verification, which surfaces the missing clone as an error.
	err = fixture.syncer.stampObsoleteCommitEvents(
		t.Context(), fixture.repo, fixture.mrID, h.a3,
	)
	assert.Error(err)
}

func TestStampObsoleteCommitEventsViaFetchProviderMRDetail(t *testing.T) {
	assert := assert.New(t)
	fixture := setupObsoleteStampingFixture(t)
	h := fixture.history
	providerRepo := RepoRef{
		Platform:           platform.KindForgejo,
		PlatformHost:       platform.DefaultForgejoHost,
		PlatformExternalID: "repo-1",
		Owner:              "owner",
		Name:               "repo",
		RepoPath:           "owner/repo",
		CloneURL:           h.sourceDir,
	}
	barePath, err := h.manager.ClonePath(
		string(platform.KindForgejo), platform.DefaultForgejoHost, "owner", "repo",
	)
	require.NoError(t, err)
	obsoleteTestGit(t, "", "clone", "--bare", h.sourceDir, barePath)
	providerRepoID, err := fixture.database.UpsertRepo(
		t.Context(), verifiedDBRepoIdentity(platformRepoRef(providerRepo)),
	)
	require.NoError(t, err)
	now := time.Date(2026, 8, 5, 12, 1, 0, 0, time.UTC)
	providerMRID, err := fixture.database.UpsertMergeRequest(t.Context(), &db.MergeRequest{
		RepoID:             providerRepoID,
		PlatformID:         101,
		PlatformExternalID: "mr-1",
		Number:             1,
		URL:                "https://codeberg.org/owner/repo/pulls/1",
		Title:              "Synthetic merge request",
		Author:             "developer",
		State:              db.MergeRequestStateOpen,
		HeadBranch:         "feature",
		BaseBranch:         "main",
		PlatformHeadSHA:    h.a3,
		PlatformBaseSHA:    h.base,
		CreatedAt:          now.Add(-time.Hour),
		UpdatedAt:          now.Add(-time.Minute),
		LastActivityAt:     now.Add(-time.Minute),
	})
	require.NoError(t, err)
	providerFixture := fixture
	providerFixture.repo = providerRepo
	providerFixture.repoID = providerRepoID
	providerFixture.mrID = providerMRID
	seedObsoleteCommitEvents(t, providerFixture, h.a1, h.a2, h.a3)

	providerRef := platformRepoRef(providerRepo)
	provider := &syncTestReadProvider{
		syncTestProvider: syncTestProvider{
			kind: platform.KindForgejo,
			host: platform.DefaultForgejoHost,
		},
		mergeRequests: []platform.MergeRequest{{
			Repo:               providerRef,
			PlatformID:         101,
			PlatformExternalID: "mr-1",
			Number:             1,
			URL:                "https://codeberg.org/owner/repo/pulls/1",
			Title:              "Synthetic merge request",
			Author:             "developer",
			State:              "open",
			HeadBranch:         "feature",
			BaseBranch:         "main",
			HeadSHA:            h.b2,
			BaseSHA:            h.base,
			CreatedAt:          now.Add(-time.Hour),
			UpdatedAt:          now,
			LastActivityAt:     now,
		}},
		listMRMergeEvents: []platform.MergeRequestEvent{
			{
				Repo:               providerRef,
				PlatformExternalID: h.b1,
				MergeRequestNumber: 1,
				EventType:          "commit",
				Summary:            "lineage b1",
				CreatedAt:          now.Add(-2 * time.Minute),
				DedupeKey:          h.b1,
			},
			{
				Repo:               providerRef,
				PlatformExternalID: h.b2,
				MergeRequestNumber: 1,
				EventType:          "commit",
				Summary:            "lineage b2",
				CreatedAt:          now.Add(-time.Minute),
				DedupeKey:          h.b2,
			},
		},
	}
	registry, err := platform.NewRegistry(provider)
	require.NoError(t, err)
	syncer := NewSyncerWithRegistry(
		registry, fixture.database, h.manager, []RepoRef{providerRepo},
		time.Minute, nil, nil,
	)
	t.Cleanup(syncer.Stop)

	_, err = syncer.fetchProviderMRDetail(
		t.Context(), provider, providerRepo, providerRepoID, 1,
	)
	require.NoError(t, err)
	assertObsoleteCommitFlags(t, providerFixture, map[string]bool{
		h.a1: true, h.a2: true, h.a3: true, h.b1: false, h.b2: false,
	})
	events, err := fixture.database.ListMREvents(t.Context(), providerMRID)
	require.NoError(t, err)
	seenReplacement := map[string]bool{h.b1: false, h.b2: false}
	for _, event := range events {
		if _, ok := seenReplacement[event.PlatformExternalID]; ok {
			seenReplacement[event.PlatformExternalID] = true
		}
	}
	assert.True(seenReplacement[h.b1], "replacement commit b1 must be persisted before stamping")
	assert.True(seenReplacement[h.b2], "replacement commit b2 must be persisted before stamping")
}

func TestSyncMRForRepoStampsObsoleteCommitEventsAfterTimelinePersistence(t *testing.T) {
	assert := assert.New(t)
	fixture := setupObsoleteStampingFixture(t)
	h := fixture.history
	seedObsoleteCommitEvents(t, fixture, h.a1, h.a2, h.a3)

	now := time.Date(2026, 8, 5, 12, 1, 0, 0, time.UTC)
	pr := buildOpenPRWithSHA(1, now, h.b2)
	pr.Base.SHA = &h.base
	commit := func(sha, message string, createdAt time.Time) *gh.RepositoryCommit {
		return &gh.RepositoryCommit{
			SHA: &sha,
			Commit: &gh.Commit{
				Message: &message,
				Author:  &gh.CommitAuthor{Name: new("developer"), Date: makeTimestamp(createdAt)},
			},
		}
	}
	ciState := "success"
	client := &mockClient{
		singlePR: pr,
		comments: []*gh.IssueComment{},
		reviews:  []*gh.PullRequestReview{},
		commits: []*gh.RepositoryCommit{
			commit(h.b1, "lineage b1", now.Add(-2*time.Minute)),
			commit(h.b2, "lineage b2", now.Add(-time.Minute)),
		},
		ciStatus:  &gh.CombinedStatus{State: &ciState},
		checkRuns: []*gh.CheckRun{},
	}
	syncer := NewSyncer(
		map[string]Client{"github.com": client},
		fixture.database,
		h.manager,
		[]RepoRef{fixture.repo},
		time.Minute,
		nil,
		testBudget(500),
	)
	t.Cleanup(syncer.Stop)

	require.NoError(t, syncer.syncMRForRepo(t.Context(), fixture.repo, 1, false, nil))
	assertObsoleteCommitFlags(t, fixture, map[string]bool{
		h.a1: true, h.a2: true, h.a3: true,
	})
	events, err := fixture.database.ListMREvents(t.Context(), fixture.mrID)
	require.NoError(t, err)
	seenB := map[string]bool{h.b1: false, h.b2: false}
	for _, event := range events {
		if _, ok := seenB[event.Summary]; ok {
			var metadata map[string]any
			require.NoError(t, json.Unmarshal([]byte(event.MetadataJSON), &metadata))
			_, obsolete := metadata["obsolete"]
			assert.False(obsolete, "replacement commit %s must remain live", event.Summary)
			seenB[event.Summary] = true
		}
	}
	assert.True(seenB[h.b1], "replacement commit b1 must be persisted before stamping")
	assert.True(seenB[h.b2], "replacement commit b2 must be persisted before stamping")
}
