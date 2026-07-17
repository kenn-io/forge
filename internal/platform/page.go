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

// ItemOrder selects the traversal order a page query requests.
//
// The canonical contract every provider implementation must honor:
//
//   - ItemOrderCreated traverses ascending by provider creation time, so an
//     interrupted historical scan resumes without missing older items.
//   - ItemOrderUpdated traverses ascending by provider update time, so a
//     maintenance scan reaches a stable watermark.
//   - Ties break ascending by item number, giving every traversal a total,
//     restart-stable order.
//   - Items that change mid-traversal may be observed twice but must not be
//     skipped; consumers dedupe by provider identity through idempotent
//     upserts.
type ItemOrder string

const (
	ItemOrderCreated ItemOrder = "created"
	ItemOrderUpdated ItemOrder = "updated"
)

// ItemPageQuery parameterizes a canonical inventory page read.
//
// Valid combinations: State and Order are required (no zero values).
// UpdatedSince is a UTC watermark, valid only with ItemOrderUpdated, and
// inclusive — items updated exactly at the watermark are returned, so
// overlapped rescans are expected and consumers dedupe by identity. Cursor is
// opaque to callers and bound to the repository and the other query fields
// that produced it; reusing a cursor with different query parameters is a
// contract violation a provider may reject.
type ItemPageQuery struct {
	State        ItemStateFilter
	Order        ItemOrder
	UpdatedSince *time.Time
	Cursor       string
}

// MaxCollectPages bounds how many pages a single CollectPages drain may fetch
// before it refuses to spend further provider requests. Finite cursor cycles
// are caught by the seen-set; this bound covers what the seen-set cannot — a
// provider emitting endless unique cursors — and caps how much of a
// legitimately oversized dataset a single in-memory drain will pull. Datasets
// beyond it belong on the durable-cursor archive path.
const MaxCollectPages = 1000

// CollectPages drains fetch from cursor into a flat slice of items. It stops
// and returns a typed platform.ErrProviderContract error when a page repeats
// any previously seen cursor (an immediate or longer cursor cycle) or when a
// non-exhausted page returns no next cursor, and a typed platform.ErrPageLimit
// error when the drain exceeds MaxCollectPages — the latter is a caller-side
// resource bound, not a provider violation. Provider and context errors
// surface unchanged.
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
			return nil, &Error{
				Code:  ErrCodePageLimit,
				Field: "collect_pages_bound",
				Err:   fmt.Errorf("page collection exceeded the maximum of %d pages", MaxCollectPages),
			}
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
