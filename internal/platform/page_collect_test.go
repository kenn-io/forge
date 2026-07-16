package platform

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCollectArchivePages(t *testing.T) {
	assert := assert.New(t)
	calls := []string{}
	items, err := CollectArchivePages(t.Context(), "", func(_ context.Context, cursor string) (ArchivePage[int], error) {
		calls = append(calls, cursor)
		switch cursor {
		case "":
			return ArchivePage[int]{Items: []int{1, 2}, NextCursor: "page-2"}, nil
		case "page-2":
			return ArchivePage[int]{ProgressOnly: true, NextCursor: "page-3"}, nil
		case "page-3":
			return ArchivePage[int]{Items: []int{3}, Exhausted: true}, nil
		default:
			return ArchivePage[int]{}, errors.New("unexpected cursor")
		}
	})
	require.NoError(t, err)
	assert.Equal([]int{1, 2, 3}, items)
	assert.Equal([]string{"", "page-2", "page-3"}, calls)
}

func TestCollectArchivePagesRejectsRepeatedCursor(t *testing.T) {
	_, err := CollectArchivePages(t.Context(), "cursor", func(_ context.Context, cursor string) (ArchivePage[int], error) {
		return ArchivePage[int]{Items: []int{1}, NextCursor: cursor}, nil
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "repeated")
}

func TestCollectArchivePagesStopsOnProviderError(t *testing.T) {
	want := errors.New("provider failed")
	_, err := CollectArchivePages(t.Context(), "", func(context.Context, string) (ArchivePage[int], error) {
		return ArchivePage[int]{}, want
	})

	require.ErrorIs(t, err, want)
}

func TestCollectArchivePagesHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	calls := 0
	_, err := CollectArchivePages(ctx, "", func(context.Context, string) (ArchivePage[int], error) {
		calls++
		return ArchivePage[int]{Exhausted: true}, nil
	})

	require.ErrorIs(t, err, context.Canceled)
	assert.Zero(t, calls)
}
