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
)

// TestArchiveRequestCostsAreProviderAware pins provider-specific lookup costs.
func TestArchiveRequestCostsAreProviderAware(t *testing.T) {
	tests := []struct {
		name     string
		kind     platform.Kind
		itemType db.ArchiveItemType
		lookup   int
	}{
		{"gitlab merge request", platform.KindGitLab, db.ArchiveItemTypeMergeRequest, archiveAttemptCost(3)},
		{"gitlab issue", platform.KindGitLab, db.ArchiveItemTypeIssue, archiveAttemptCost(2)},
		{"github merge request", platform.KindGitHub, db.ArchiveItemTypeMergeRequest, archiveAttemptCost(2)},
		{"github issue", platform.KindGitHub, db.ArchiveItemTypeIssue, archiveAttemptCost(2)},
		{"forgejo merge request", platform.KindForgejo, db.ArchiveItemTypeMergeRequest, archiveAttemptCost(2)},
		{"gitea merge request", platform.KindGitea, db.ArchiveItemTypeMergeRequest, archiveAttemptCost(2)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.lookup, archiveLookupCost(tt.kind, tt.itemType))
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

func TestArchiveWorkPrioritiesPreserveForegroundOrdering(t *testing.T) {
	assert := assert.New(t)
	assert.Less(PriorityNormalIndex, PriorityNotificationRefresh)
	assert.Less(PriorityNotificationRefresh, PriorityActiveDetail)
	assert.Less(PriorityActiveDetail, PriorityFullArchive)
	assert.Less(PriorityFullArchive, PriorityDiscoveryInventory)
}
