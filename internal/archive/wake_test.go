package archive

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/platform"
	"go.kenn.io/forge/internal/testutil/dbtest"
)

func TestRunPassIdleRepositoriesReportNoWorkWithoutRepositoryResolution(t *testing.T) {
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

	// Drive passes until inventory, hydration, and the first maintenance
	// scan are done and a pass reports no attempted work.
	worked := true
	for range 20 {
		worked, err = service.RunPass(t.Context())
		require.NoError(err)
		if !worked {
			break
		}
	}
	require.False(worked, "archive worker did not become idle")
	states, err := database.ListArchiveRepoStates(t.Context(), []int64{firstID, secondID})
	require.NoError(err)
	require.Len(states, 2)
	for _, state := range states {
		assert.NotNil(state.InitialCompletedAt)
		assert.NotNil(state.MaintenanceSucceededAt)
	}

	// Hide the repository catalog: any repository resolution query from the
	// following idle passes now fails, so a clean pass proves the cached
	// resolution was used.
	_, err = database.WriteDB().ExecContext(t.Context(),
		`ALTER TABLE forge_repos RENAME TO forge_repos_hidden`)
	require.NoError(err)
	for range 3 {
		worked, err = service.RunPass(t.Context())
		require.NoError(err)
		assert.False(worked)
	}

	// A cold service must resolve and therefore fail, which proves the hidden
	// catalog is what the cache avoided.
	cold := newArchiveTestService(
		t, database, registry, []platform.RepoRef{first, second}, &archiveTestAdmission{}, now,
	)
	_, err = cold.RunPass(t.Context())
	require.Error(err)
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
}
