package github

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFetchAllPagesSinglePage(t *testing.T) {
	assert := assert.New(t)

	items, err := fetchAllPages(
		t.Context(),
		func(_ context.Context, cursor *string) ([]int, pageInfo, error) {
			assert.Nil(cursor)
			return []int{1, 2, 3}, pageInfo{HasNextPage: false}, nil
		},
	)
	require.NoError(t, err)
	assert.Equal([]int{1, 2, 3}, items)
}

func TestFetchAllPagesMultiPage(t *testing.T) {
	assert := assert.New(t)
	calls := 0

	items, err := fetchAllPages(
		t.Context(),
		func(_ context.Context, cursor *string) ([]string, pageInfo, error) {
			calls++
			switch calls {
			case 1:
				assert.Nil(cursor)
				return []string{"a", "b"}, pageInfo{
					HasNextPage: true,
					EndCursor:   "cursor1",
				}, nil
			case 2:
				require.NotNil(t, cursor)
				assert.Equal("cursor1", *cursor)
				return []string{"c"}, pageInfo{
					HasNextPage: false,
				}, nil
			default:
				require.Fail(t, "too many calls")
				return nil, pageInfo{}, nil
			}
		},
	)
	require.NoError(t, err)
	assert.Equal([]string{"a", "b", "c"}, items)
	assert.Equal(2, calls)
}

func TestFetchAllPagesError(t *testing.T) {
	assert := assert.New(t)

	// Test error on first page
	_, err := fetchAllPages(
		t.Context(),
		func(_ context.Context, cursor *string) ([]int, pageInfo, error) {
			return nil, pageInfo{}, fmt.Errorf("graphql: rate limited")
		},
	)
	require.Error(t, err)
	assert.Contains(err.Error(), "rate limited")
}

func TestFetchAllPagesContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := fetchAllPages(
		ctx,
		func(ctx context.Context, cursor *string) ([]int, pageInfo, error) {
			return nil, pageInfo{}, ctx.Err()
		},
	)
	require.Error(t, err)
}

func TestFetchAllPagesEmptyCursor(t *testing.T) {
	assert := assert.New(t)

	items, err := fetchAllPages(
		t.Context(),
		func(_ context.Context, cursor *string) ([]int, pageInfo, error) {
			return []int{1}, pageInfo{
				HasNextPage: true,
				EndCursor:   "",
			}, nil
		},
	)
	require.Error(t, err)
	assert.Contains(err.Error(), "endCursor empty")
	assert.Equal([]int{1}, items)
}

func TestFetchAllPagesRepeatedCursor(t *testing.T) {
	assert := assert.New(t)
	calls := 0

	items, err := fetchAllPages(
		t.Context(),
		func(_ context.Context, cursor *string) ([]int, pageInfo, error) {
			calls++
			return []int{calls}, pageInfo{
				HasNextPage: true,
				EndCursor:   "stuck",
			}, nil
		},
	)
	require.Error(t, err)
	assert.Contains(err.Error(), "endCursor unchanged")
	assert.Equal([]int{1, 2}, items)
}

func TestFetchAllPagesPartialResultsOnError(t *testing.T) {
	assert := assert.New(t)
	calls := 0

	items, err := fetchAllPages(
		t.Context(),
		func(_ context.Context, cursor *string) ([]int, pageInfo, error) {
			calls++
			if calls == 1 {
				return []int{1, 2}, pageInfo{
					HasNextPage: true,
					EndCursor:   "c1",
				}, nil
			}
			return nil, pageInfo{}, fmt.Errorf("page 2 failed")
		},
	)
	require.Error(t, err)
	assert.Contains(err.Error(), "page 2 failed")
	assert.Equal([]int{1, 2}, items)
}
