package archive

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/middleman/internal/db"
	"go.kenn.io/middleman/internal/platform"
	"go.kenn.io/middleman/internal/testutil/dbtest"
)

// TestArchiveRequestCostsAreProviderAware pins the worst-case admission cost
// tables: only GitLab spends the extra merge-request lookup enrichment
// request and the dense-window inventory boundary probe, so only GitLab
// reserves them; every other provider's lookups and pages keep the shared
// baseline.
func TestArchiveRequestCostsAreProviderAware(t *testing.T) {
	tests := []struct {
		name      string
		kind      platform.Kind
		itemType  db.ArchiveItemType
		lookup    int
		inventory int
	}{
		{"gitlab merge request", platform.KindGitLab, db.ArchiveItemTypeMergeRequest, archiveAttemptCost(3), archiveAttemptCost(2)},
		{"gitlab issue", platform.KindGitLab, db.ArchiveItemTypeIssue, archiveAttemptCost(2), archiveAttemptCost(1)},
		{"github merge request", platform.KindGitHub, db.ArchiveItemTypeMergeRequest, archiveAttemptCost(2), archiveAttemptCost(1)},
		{"github issue", platform.KindGitHub, db.ArchiveItemTypeIssue, archiveAttemptCost(2), archiveAttemptCost(1)},
		{"forgejo merge request", platform.KindForgejo, db.ArchiveItemTypeMergeRequest, archiveAttemptCost(2), archiveAttemptCost(1)},
		{"gitea merge request", platform.KindGitea, db.ArchiveItemTypeMergeRequest, archiveAttemptCost(2), archiveAttemptCost(1)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.lookup, archiveLookupCost(tt.kind, tt.itemType))
			assert.Equal(t, tt.inventory, archiveInventoryPageCost(tt.kind, tt.itemType))
		})
	}
}

func TestArchiveSchedulerDoesNotSerializeSameHostOutsideAdmission(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	scheduler := NewScheduler()
	groups := map[string][]resolvedRepository{"github\x00github.test": {{Ref: archiveServiceRef(platform.KindGitHub, "github.test", "repo")}}}
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	var active atomic.Int32
	var maximum atomic.Int32
	work := func(context.Context, []resolvedRepository) error {
		current := active.Add(1)
		for {
			observed := maximum.Load()
			if current <= observed || maximum.CompareAndSwap(observed, current) {
				break
			}
		}
		entered <- struct{}{}
		<-release
		active.Add(-1)
		return nil
	}
	errCh := make(chan error, 2)
	go func() { errCh <- scheduler.Run(t.Context(), groups, work) }()
	go func() { errCh <- scheduler.Run(t.Context(), groups, work) }()
	require.Eventually(func() bool { return len(entered) == 2 }, time.Second, time.Millisecond)
	release <- struct{}{}
	release <- struct{}{}
	require.NoError(<-errCh)
	require.NoError(<-errCh)
	assert.Equal(int32(2), maximum.Load())
}

func TestArchiveSchedulerRunsIndependentHostsConcurrently(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	scheduler := NewScheduler()
	groups := map[string][]resolvedRepository{
		"github\x00one.test": {{Ref: archiveServiceRef(platform.KindGitHub, "one.test", "one")}},
		"github\x00two.test": {{Ref: archiveServiceRef(platform.KindGitHub, "two.test", "two")}},
	}
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	var active atomic.Int32
	var maximum atomic.Int32
	work := func(context.Context, []resolvedRepository) error {
		current := active.Add(1)
		for {
			observed := maximum.Load()
			if current <= observed || maximum.CompareAndSwap(observed, current) {
				break
			}
		}
		entered <- struct{}{}
		<-release
		active.Add(-1)
		return nil
	}
	done := make(chan error, 1)
	go func() { done <- scheduler.Run(t.Context(), groups, work) }()
	require.Eventually(func() bool { return len(entered) == 2 }, time.Second, time.Millisecond)
	release <- struct{}{}
	release <- struct{}{}
	require.NoError(<-done)
	assert.Equal(int32(2), maximum.Load())
}

func TestBlockedMaintenanceBoundaryDoesNotStarveOtherRepositories(t *testing.T) {
	assert := assert.New(t)
	require := require.New(t)
	database := dbtest.Open(t)
	now := archiveTestTime()
	blockedRef := archiveServiceRef(platform.KindGitHub, "github.test", "alpha")
	otherRef := archiveServiceRef(platform.KindGitHub, "github.test", "beta")
	blockedID := archiveServiceSeedRepo(t, database, blockedRef)
	otherID := archiveServiceSeedRepo(t, database, otherRef)
	provider := newArchiveServiceProvider(blockedRef.Platform, blockedRef.Host)
	registry, err := platform.NewRegistry(provider)
	require.NoError(err)
	refs := []platform.RepoRef{blockedRef, otherRef}
	service := newArchiveTestService(t, database, registry, refs, nil, now)
	require.NoError(service.EnsureConfigured(t.Context(), refs))
	_, err = service.Start(t.Context(), refs)
	require.NoError(err)
	for range 20 {
		statuses, statusErr := service.Status(t.Context(), refs)
		require.NoError(statusErr)
		if statuses[0].Progress.Status == db.ArchiveStatusCurrent &&
			statuses[1].Progress.Status == db.ArchiveStatusCurrent {
			break
		}
		require.NoError(service.RunEligible(t.Context()))
	}

	// The first repository holds a durable maintenance boundary whose streams
	// are both durably blocked: nothing about that boundary can advance until
	// an explicit reset.
	_, err = database.WriteDB().ExecContext(t.Context(), `
		UPDATE middleman_archive_repos
		SET prompt_scan_started_at = ?, prompt_since = ?, maintenance_succeeded_at = NULL
		WHERE repo_id = ?`, now, now.Add(-time.Hour), blockedID)
	require.NoError(err)
	_, err = database.WriteDB().ExecContext(t.Context(), `
		UPDATE middleman_archive_repo_scans
		SET status = 'blocked', last_error_code = 'invalid_cursor'
		WHERE repo_id = ? AND scan IN ('maintenance_issues', 'maintenance_merge_requests')`, blockedID)
	require.NoError(err)
	// The second repository has due hydration work again.
	require.NoError(database.ResetDatasetProgress(t.Context(), db.ArchiveDatasetProgressKey{
		RepoID: otherID, ItemType: db.ArchiveItemTypeIssue, ItemNumber: 1,
		Dataset: db.ArchiveDatasetComments,
	}))
	provider.mu.Lock()
	provider.calls = nil
	provider.mu.Unlock()

	// One poll on the shared provider host must run the other repository's
	// hydration instead of spinning on the unactionable blocked boundary.
	require.NoError(service.RunEligible(t.Context()))
	provider.mu.Lock()
	calls := append([]string(nil), provider.calls...)
	provider.mu.Unlock()
	assert.Contains(calls, "issue_comments:", "the other repository's hydration must proceed")

	// The unresolved boundary stays durable for reporting.
	states, err := database.ListArchiveRepoStates(t.Context(), []int64{blockedID})
	require.NoError(err)
	require.Len(states, 1)
	assert.NotNil(states[0].PromptScanStartedAt)
	assert.True(states[0].MaintenanceIssues.Blocked())
	assert.True(states[0].MaintenanceMergeRequests.Blocked())
}

func TestArchiveWorkPrioritiesPreserveForegroundOrdering(t *testing.T) {
	assert := assert.New(t)
	assert.Less(PriorityNormalIndex, PriorityNotificationRefresh)
	assert.Less(PriorityNotificationRefresh, PriorityActiveDetail)
	assert.Less(PriorityActiveDetail, PriorityFullArchive)
	assert.Less(PriorityFullArchive, PriorityDiscoveryInventory)
}
