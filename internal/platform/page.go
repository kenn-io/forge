package platform

import (
	"context"
	"fmt"
	"time"
)

// collectContractError builds the typed provider-contract error CollectPages
// returns when a drain detects a cursor cycle, a missing next cursor, or the
// page bound. It carries no provider identity because CollectPages is a neutral
// collector over any provider's page method.
func collectContractError(field, format string, args ...any) error {
	return &Error{
		Code:  ErrCodeProviderContract,
		Field: field,
		Err:   fmt.Errorf(format, args...),
	}
}

// ItemStateFilter selects which lifecycle states a page query returns. Open
// sync uses ItemStateOpen; historical and maintenance scans use ItemStateAll.
type ItemStateFilter string

const (
	ItemStateOpen ItemStateFilter = "open"
	ItemStateAll  ItemStateFilter = "all"
)

// ItemOrder selects the traversal order a page query requests. Providers map
// this to their own stable sort fence.
type ItemOrder string

const (
	ItemOrderCreated ItemOrder = "created"
	ItemOrderUpdated ItemOrder = "updated"
)

// ItemPageQuery parameterizes a canonical inventory page read. Cursor is opaque
// to callers; UpdatedSince is a UTC maintenance watermark.
type ItemPageQuery struct {
	State        ItemStateFilter
	Order        ItemOrder
	UpdatedSince *time.Time
	Cursor       string
}

// MaxCollectPages bounds how many pages a single CollectPages drain may fetch
// before it refuses to spend further provider requests. It stops an
// alternating or longer cursor cycle that the seen-set could not observe within
// one drain, as well as a legitimately oversized whole-dataset read.
const MaxCollectPages = 1000

// CollectPages drains fetch from cursor into a flat slice of items. It stops and
// returns a typed platform.ErrProviderContract error when a page repeats any
// previously seen cursor (an immediate or longer cursor cycle), when a
// non-exhausted page returns no next cursor, or when the drain exceeds
// MaxCollectPages. Provider and context errors surface unchanged.
func CollectPages[T any](
	ctx context.Context,
	cursor string,
	fetch func(context.Context, string) (Page[T], error),
) ([]T, error) {
	var items []T
	seen := make(map[string]struct{})
	for pages := 0; ; pages++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if pages >= MaxCollectPages {
			return nil, collectContractError(
				"collect_pages_bound",
				"page collection exceeded the maximum of %d pages", MaxCollectPages,
			)
		}
		if _, ok := seen[cursor]; ok {
			return nil, collectContractError(
				"collect_pages_cursor",
				"page collection revisited cursor %q", cursor,
			)
		}
		seen[cursor] = struct{}{}
		page, err := fetch(ctx, cursor)
		if err != nil {
			return nil, err
		}
		if page.Exhausted {
			return append(items, page.Items...), nil
		}
		if page.NextCursor == "" {
			return nil, collectContractError(
				"collect_pages_cursor",
				"page did not return a next cursor or exhaustion",
			)
		}
		items = append(items, page.Items...)
		cursor = page.NextCursor
	}
}
