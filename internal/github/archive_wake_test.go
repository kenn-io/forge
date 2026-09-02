package github

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/forge/internal/archive"
	"go.kenn.io/forge/internal/platform"
	"go.kenn.io/forge/internal/testutil/dbtest"
)

func TestArchiveWaitPacesWorkAndSleepsUntilEligibility(t *testing.T) {
	assert := assert.New(t)
	now := time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)
	syncer := NewSyncerWithRegistry(nil, dbtest.Open(t), nil, nil, time.Hour, nil, nil)
	syncer.SetClock(func() time.Time { return now })
	syncer.SetArchivePollIntervalForTesting(2 * time.Second)

	assert.Equal(2*time.Second, syncer.archiveWait(archive.NextWake{}, errors.New("store failed")),
		"a failed pass retries at the work pacing interval")
	assert.Equal(2*time.Second, syncer.archiveWait(archive.NextWake{Worked: true}, nil),
		"a pass that attempted work re-runs at the work pacing interval")
	assert.Equal(archiveIdleWait, syncer.archiveWait(archive.NextWake{}, nil),
		"nothing scheduled sleeps for the idle bound")
	assert.Equal(90*time.Second, syncer.archiveWait(archive.NextWake{At: now.Add(90 * time.Second)}, nil),
		"an idle pass sleeps until the earliest eligibility time")
	assert.Equal(2*time.Second, syncer.archiveWait(archive.NextWake{At: now.Add(-time.Minute)}, nil),
		"an eligibility time already reached still paces by the work interval")
	assert.Equal(archiveIdleWait, syncer.archiveWait(archive.NextWake{At: now.Add(time.Hour)}, nil),
		"a distant eligibility time is bounded by the idle safety net")
}

// countingArchiveRunner records every worker pass made against the real
// archive service and the errors those passes returned.
type countingArchiveRunner struct {
	service *archive.Service
	passes  atomic.Int32
	errs    chan error
}

func (r *countingArchiveRunner) RunPass(ctx context.Context) (archive.NextWake, error) {
	wake, err := r.service.RunPass(ctx)
	if err != nil {
		select {
		case r.errs <- err:
		default:
		}
	}
	r.passes.Add(1)
	return wake, err
}

func TestArchiveWorkerIdleWakesRunNoRepositoryResolution(t *testing.T) {
	require := require.New(t)
	database := dbtest.Open(t)
	ref := platform.RepoRef{
		Platform: platform.KindGitHub, Host: "github.test",
		Owner: "acme", Name: "widget", RepoPath: "acme/widget",
		PlatformExternalID: "repo-1",
	}
	provider := &archiveWorkerProvider{ref: ref}
	registry, err := platform.NewRegistry(provider)
	require.NoError(err)
	syncer := NewSyncerWithRegistry(registry, database, nil, []RepoRef{{
		Platform: ref.Platform, PlatformHost: ref.Host,
		Owner: ref.Owner, Name: ref.Name, RepoPath: ref.RepoPath,
		PlatformExternalID: ref.PlatformExternalID,
	}}, time.Hour, nil, nil)
	service, err := archive.NewService(database, registry, nil, syncer, nil, nil)
	require.NoError(err)
	service.SetWake(syncer.WakeArchive)
	runner := &countingArchiveRunner{service: service, errs: make(chan error, 1)}
	syncer.SetArchiveService(runner)
	syncer.SetArchivePollIntervalForTesting(time.Millisecond)
	requireEnsureConfigured(t, service, []platform.RepoRef{ref})
	_, err = service.Start(t.Context(), []platform.RepoRef{ref})
	require.NoError(err)
	syncer.Start(t.Context())
	t.Cleanup(syncer.Stop)

	repo, err := database.GetRepoByIdentity(t.Context(), platform.DBRepoIdentity(ref))
	require.NoError(err)
	require.NotNil(repo)
	require.Eventually(func() bool {
		states, stateErr := database.ListArchiveRepoStates(t.Context(), []int64{repo.ID})
		return stateErr == nil && len(states) == 1 && states[0].IssueInventory.Complete()
	}, 2*time.Second, 5*time.Millisecond)

	// The worker is idle once every inventoried item's hydration has either
	// completed or is waiting on its retry time, and passes stop.
	require.Eventually(func() bool {
		summaries, summaryErr := database.SummarizeArchiveItemsDue(
			t.Context(), []int64{repo.ID}, time.Now(),
		)
		if summaryErr != nil {
			return false
		}
		for _, summary := range summaries {
			if summary.Due > 0 {
				return false
			}
		}
		before := runner.passes.Load()
		time.Sleep(50 * time.Millisecond)
		return runner.passes.Load() == before
	}, 5*time.Second, 10*time.Millisecond)

	// Hide the repository catalog so any resolution query fails loudly.
	_, err = database.WriteDB().ExecContext(t.Context(),
		`ALTER TABLE forge_repos RENAME TO forge_repos_hidden`)
	require.NoError(err)

	for range 3 {
		before := runner.passes.Load()
		syncer.WakeArchive()
		require.Eventually(func() bool {
			return runner.passes.Load() > before
		}, time.Second, time.Millisecond)
	}
	select {
	case err := <-runner.errs:
		require.NoError(err, "idle wakes must not resolve repositories")
	default:
	}
	before := runner.passes.Load()
	time.Sleep(50 * time.Millisecond)
	require.Equal(before, runner.passes.Load(), "an idle worker must not poll between wakes")
}
