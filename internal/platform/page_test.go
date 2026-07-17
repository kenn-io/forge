package platform

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCollectPagesDrainsMultiplePages(t *testing.T) {
	assert := assert.New(t)
	calls := []string{}
	items, err := CollectPages(t.Context(), "", func(_ context.Context, cursor string) (Page[int], error) {
		calls = append(calls, cursor)
		switch cursor {
		case "":
			return Page[int]{Items: []int{1, 2}, NextCursor: "page-2"}, nil
		case "page-2":
			return Page[int]{ProgressOnly: true, NextCursor: "page-3"}, nil
		case "page-3":
			return Page[int]{Items: []int{3}, Exhausted: true}, nil
		default:
			return Page[int]{}, errors.New("unexpected cursor")
		}
	})
	require.NoError(t, err)
	assert.Equal([]int{1, 2, 3}, items)
	assert.Equal([]string{"", "page-2", "page-3"}, calls)
}

func TestCollectPagesRejectsMissingCursor(t *testing.T) {
	_, err := CollectPages(t.Context(), "", func(context.Context, string) (Page[int], error) {
		return Page[int]{Items: []int{1}}, nil
	})

	require.Error(t, err)
	require.ErrorIs(t, err, ErrProviderContract)
}

func TestCollectPagesRejectsImmediateRepeat(t *testing.T) {
	_, err := CollectPages(t.Context(), "cursor", func(_ context.Context, cursor string) (Page[int], error) {
		return Page[int]{Items: []int{1}, NextCursor: cursor}, nil
	})

	require.Error(t, err)
	require.ErrorIs(t, err, ErrProviderContract)
}

func TestCollectPagesDetectsAlternatingCycle(t *testing.T) {
	calls := 0
	_, err := CollectPages(t.Context(), "a", func(_ context.Context, cursor string) (Page[int], error) {
		calls++
		switch cursor {
		case "a":
			return Page[int]{Items: []int{1}, NextCursor: "b"}, nil
		default:
			return Page[int]{Items: []int{2}, NextCursor: "a"}, nil
		}
	})

	require.ErrorIs(t, err, ErrProviderContract)
	// a -> b -> a is detected the moment the seen "a" cursor recurs.
	assert.Equal(t, 2, calls)
}

func TestCollectPagesEnforcesPageBound(t *testing.T) {
	assert := assert.New(t)
	calls := 0
	_, err := CollectPages(t.Context(), "0", func(_ context.Context, cursor string) (Page[int], error) {
		calls++
		return Page[int]{Items: []int{calls}, NextCursor: cursor + "-next"}, nil
	})

	require.ErrorIs(t, err, ErrProviderContract)
	// Distinct advancing cursors never repeat, so only the page bound stops it.
	assert.Equal(MaxCollectPages, calls)
}

func TestCollectPagesAllowsBoundedProgressOnlyPages(t *testing.T) {
	assert := assert.New(t)
	items, err := CollectPages(t.Context(), "", func(_ context.Context, cursor string) (Page[int], error) {
		switch cursor {
		case "":
			return Page[int]{ProgressOnly: true, NextCursor: "p1"}, nil
		case "p1":
			return Page[int]{ProgressOnly: true, NextCursor: "p2"}, nil
		case "p2":
			return Page[int]{Items: []int{7}, Exhausted: true}, nil
		default:
			return Page[int]{}, errors.New("unexpected cursor")
		}
	})
	require.NoError(t, err)
	assert.Equal([]int{7}, items)
}

func TestCollectPagesBoundsProgressOnlyCycle(t *testing.T) {
	_, err := CollectPages(t.Context(), "", func(_ context.Context, cursor string) (Page[int], error) {
		if cursor == "" {
			return Page[int]{ProgressOnly: true, NextCursor: "loop"}, nil
		}
		return Page[int]{ProgressOnly: true, NextCursor: "loop"}, nil
	})

	require.ErrorIs(t, err, ErrProviderContract)
}

func TestCollectPagesStopsOnProviderError(t *testing.T) {
	want := errors.New("provider failed")
	_, err := CollectPages(t.Context(), "", func(context.Context, string) (Page[int], error) {
		return Page[int]{}, want
	})

	require.ErrorIs(t, err, want)
}

func TestCollectPagesHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	calls := 0
	_, err := CollectPages(ctx, "", func(context.Context, string) (Page[int], error) {
		calls++
		return Page[int]{Exhausted: true}, nil
	})

	require.ErrorIs(t, err, context.Canceled)
	assert.Zero(t, calls)
}
