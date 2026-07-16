package archive

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/middleman/internal/platform"
)

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
