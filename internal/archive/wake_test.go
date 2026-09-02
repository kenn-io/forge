package archive

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/platform"
	"go.kenn.io/forge/internal/testutil/dbtest"
)

// runUntilIdle drives passes until one reports no attempted work and returns
// that pass's wake computation.
func runUntilIdle(t *testing.T, service *Service) NextWake {
	t.Helper()
	for range 20 {
		wake, err := service.RunPass(t.Context())
		require.NoError(t, err)
		if !wake.Worked {
			return wake
		}
	}
	require.Fail(t, "archive worker did not become idle")
	return NextWake{}
}

func TestRunPassIdleRepositoriesSleepUntilMaintenanceWithoutRepositoryResolution(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	database := dbtest.Open(t)
	now := archiveTestTime()
	first := archiveServiceRef(platform.KindGitHub, "github.test", "first")
	second := archiveServiceRef(platform.KindGitHub, "github.test", "second")
	firstID := archiveServiceSeedRepo(t, database, first)
	secondID := archiveServiceSeedRepo(t, database, second)
	provider := newArchiveServiceProvider(first.Platform, first.Host)
	registry, err := platform.NewRegistry(provider)
	require.NoError(err)
	service := newArchiveTestService(
		t, database, registry, []platform.RepoRef{first, second}, &archiveTestAdmission{}, now,
	)
	requireEnsureConfigured(t, service, []platform.RepoRef{first, second})
	_, err = service.Start(t.Context(), []platform.RepoRef{first, second})
	require.NoError(err)

	wake := runUntilIdle(t, service)
	states, err := database.ListArchiveRepoStates(t.Context(), []int64{firstID, secondID})
	require.NoError(err)
	require.Len(states, 2)
	for _, state := range states {
		require.NotNil(state.InitialCompletedAt)
		require.NotNil(state.MaintenanceSucceededAt)
	}
	assert.False(wake.Worked)
	assert.Equal(states[0].MaintenanceSucceededAt.Add(service.maintenanceInterval), wake.At)

	// Hide the repository catalog: any repository resolution query from the
	// following idle passes now fails, so a clean pass proves the cached
	// resolution was used.
	_, err = database.WriteDB().ExecContext(t.Context(),
		`ALTER TABLE forge_repos RENAME TO forge_repos_hidden`)
	require.NoError(err)
	for range 3 {
		wake, err = service.RunPass(t.Context())
		require.NoError(err)
		assert.False(wake.Worked)
		assert.Equal(states[0].MaintenanceSucceededAt.Add(service.maintenanceInterval), wake.At)
	}

	// A cold service must resolve and therefore fail, which proves the hidden
	// catalog is what the cache avoided.
	cold := newArchiveTestService(
		t, database, registry, []platform.RepoRef{first, second}, &archiveTestAdmission{}, now,
	)
	_, err = cold.RunPass(t.Context())
	require.Error(err)
}

func TestRunPassMaintenanceBecomesDueAtInterval(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	database := dbtest.Open(t)
	now := archiveTestTime()
	ref := archiveServiceRef(platform.KindGitHub, "github.test", "repo")
	archiveServiceSeedRepo(t, database, ref)
	provider := newArchiveServiceProvider(ref.Platform, ref.Host)
	registry, err := platform.NewRegistry(provider)
	require.NoError(err)
	service := newArchiveTestService(
		t, database, registry, []platform.RepoRef{ref}, &archiveTestAdmission{}, now,
	)
	requireEnsureConfigured(t, service, []platform.RepoRef{ref})
	_, err = service.Start(t.Context(), []platform.RepoRef{ref})
	require.NoError(err)
	idle := runUntilIdle(t, service)
	require.Equal(now.Add(service.maintenanceInterval), idle.At)

	before := len(provider.updatedIssueSince)
	service.clock = fixedClock{value: idle.At}
	wake, err := service.RunPass(t.Context())
	require.NoError(err)
	assert.True(wake.Worked, "maintenance must run once its interval elapses")
	assert.Len(provider.updatedIssueSince, before+1)
}

func TestRunPassReportsRepositoryDeferralDeadline(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	database := dbtest.Open(t)
	now := archiveTestTime()
	ref := archiveServiceRef(platform.KindGitHub, "github.test", "repo")
	archiveServiceSeedRepo(t, database, ref)
	provider := newArchiveServiceProvider(ref.Platform, ref.Host)
	registry, err := platform.NewRegistry(provider)
	require.NoError(err)
	retryAt := now.Add(17 * time.Minute)
	admission := &archiveTestAdmission{deny: true, retryAt: retryAt}
	service := newArchiveTestService(t, database, registry, []platform.RepoRef{ref}, admission, now)
	requireEnsureConfigured(t, service, []platform.RepoRef{ref})
	_, err = service.Start(t.Context(), []platform.RepoRef{ref})
	require.NoError(err)

	wake, err := service.RunPass(t.Context())
	require.NoError(err)
	assert.True(wake.Worked, "a denied admission records the deferral as attempted work")

	wake, err = service.RunPass(t.Context())
	require.NoError(err)
	assert.False(wake.Worked)
	assert.Equal(retryAt, wake.At)
	assert.Equal(1, admission.calls, "a deferred repository must not re-enter admission")
}

func TestRunPassReportsFeatureCooldownDeadline(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	database := dbtest.Open(t)
	now := archiveTestTime()
	ref := archiveServiceRef(platform.KindGitHub, "github.test", "repo")
	archiveServiceSeedRepo(t, database, ref)
	provider := newArchiveServiceProvider(ref.Platform, ref.Host)
	registry, err := platform.NewRegistry(provider)
	require.NoError(err)
	admission := &archiveFeatureDeferringAdmission{
		enabled: true, repoName: ref.Name,
		itemTypes: map[db.ArchiveItemType]bool{
			db.ArchiveItemTypeIssue: true, db.ArchiveItemTypeMergeRequest: true,
		},
	}
	service := newArchiveTestService(t, database, registry, []platform.RepoRef{ref}, admission, now)
	requireEnsureConfigured(t, service, []platform.RepoRef{ref})
	_, err = service.Start(t.Context(), []platform.RepoRef{ref})
	require.NoError(err)

	wake, err := service.RunPass(t.Context())
	require.NoError(err)
	assert.False(wake.Worked)
	assert.Equal(archiveTestTime().Add(24*time.Hour), wake.At)
}

type retryAtClassifier struct{ retryAt time.Time }

func (c retryAtClassifier) Classify(error, int, time.Time) RetryDecision {
	retryAt := c.retryAt
	return RetryDecision{Code: db.ArchiveErrorCodeTransient, RetryAt: &retryAt}
}

func TestRunPassReportsHydrationRetryTimeWithoutClaiming(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	database := dbtest.Open(t)
	now := archiveTestTime()
	ref := archiveServiceRef(platform.KindGitHub, "github.test", "repo")
	repoID := archiveServiceSeedRepo(t, database, ref)
	provider := newArchiveServiceProvider(ref.Platform, ref.Host)
	provider.historicalIssuePages = map[string]platform.Page[platform.Issue]{
		"": {Items: []platform.Issue{archiveTestIssue(ref)}, Exhausted: true},
	}
	registry, err := platform.NewRegistry(provider)
	require.NoError(err)
	retryAt := now.Add(3 * time.Minute)
	service, err := NewService(
		database, registry, &archiveTestAdmission{},
		archiveFailingItemSource{archiveTestSource{refs: []platform.RepoRef{ref}}},
		retryAtClassifier{retryAt: retryAt}, fixedClock{value: now},
	)
	require.NoError(err)
	requireEnsureConfigured(t, service, []platform.RepoRef{ref})
	_, err = service.Start(t.Context(), []platform.RepoRef{ref})
	require.NoError(err)

	// Inventory both streams, then every hydration attempt fails and is
	// scheduled for retry until nothing is due.
	failures := 0
	var wake NextWake
	for range 10 {
		wake, err = service.RunPass(t.Context())
		if err != nil {
			failures++
			continue
		}
		if !wake.Worked {
			break
		}
	}
	require.Positive(failures, "hydration failures must surface from the pass")
	require.False(wake.Worked)
	assert.Equal(retryAt, wake.At)

	summaries, err := database.SummarizeArchiveItemsDue(t.Context(), []int64{repoID}, now)
	require.NoError(err)
	require.Len(summaries, 1)
	assert.Equal(0, summaries[0].Due)
	require.NotNil(summaries[0].NextRetryAt)
	assert.Equal(retryAt, *summaries[0].NextRetryAt)

	service.clock = fixedClock{value: retryAt}
	summaries, err = database.SummarizeArchiveItemsDue(t.Context(), []int64{repoID}, retryAt)
	require.NoError(err)
	require.Len(summaries, 1)
	assert.Equal(failures, summaries[0].Due)
	assert.Nil(summaries[0].NextRetryAt)
	_, err = service.RunPass(t.Context())
	require.Error(err, "the item must be claimed and retried once its retry time arrives")
}

func TestWorkerRepositoriesCacheFollowsConfigurationAndReconciliation(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	database := dbtest.Open(t)
	now := archiveTestTime()
	first := archiveServiceRef(platform.KindGitHub, "github.test", "first")
	second := archiveServiceRef(platform.KindGitHub, "github.test", "second")
	archiveServiceSeedRepo(t, database, first)
	provider := newArchiveServiceProvider(first.Platform, first.Host)
	registry, err := platform.NewRegistry(provider)
	require.NoError(err)
	source := &archiveMutableSource{refs: []platform.RepoRef{first, second}}
	service, err := NewService(database, registry, &archiveTestAdmission{}, source, nil, fixedClock{value: now})
	require.NoError(err)

	resolved, err := service.workerRepositories(t.Context())
	require.NoError(err)
	require.Len(resolved, 1, "an unseeded configured repository is skipped")
	again, err := service.workerRepositories(t.Context())
	require.NoError(err)
	require.Len(again, 1)
	assert.Same(&resolved[0], &again[0], "unchanged configuration reuses the cached resolution")

	// Seeding the second repository is a repository reconciliation write and
	// must make the next pass resolve it.
	archiveServiceSeedRepo(t, database, second)
	resolved, err = service.workerRepositories(t.Context())
	require.NoError(err)
	require.Len(resolved, 2)

	// Dropping a configured repository takes effect without a store change.
	source.refs = []platform.RepoRef{second}
	resolved, err = service.workerRepositories(t.Context())
	require.NoError(err)
	require.Len(resolved, 1)
	assert.Equal(second.Name, resolved[0].Ref.Name)

	// Archive state changes invalidate explicitly.
	_, err = service.workerRepositories(t.Context())
	require.NoError(err)
	service.InvalidateRepositories()
	assert.Nil(service.repos)
}

func TestRunPassWithoutConfiguredRepositoriesSchedulesNothing(t *testing.T) {
	require := require.New(t)
	database := dbtest.Open(t)
	provider := newArchiveServiceProvider(platform.KindGitHub, "github.test")
	registry, err := platform.NewRegistry(provider)
	require.NoError(err)
	service := newArchiveTestService(t, database, registry, nil, &archiveTestAdmission{}, archiveTestTime())

	wake, err := service.RunPass(context.Background())
	require.NoError(err)
	require.Equal(NextWake{}, wake)
}
