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
//   - ItemOrderCreated traverses ascending by provider creation time with a
//     restart-stable resumption guarantee. Equal creation times break by a
//     documented provider tie-breaker that must be monotonic with record
//     creation (item number where the provider API can honor it across page
//     boundaries): a merely stable ordering is not enough, because an
//     equal-timestamp item inserted before the cursor position would be
//     permanently skipped. A provider that cannot guarantee a monotonic
//     page-spanning tie-break must instead resume inclusively at the boundary
//     creation time, so an interrupted scan never permanently skips an item
//     that shares its resume timestamp; consumers absorb the resulting
//     overlap through idempotent upserts.
//   - ItemOrderUpdated promises provider-efficient traversal by update time
//     with the direction unspecified: each provider uses whichever direction
//     its API serves without re-paging the whole dataset (GitHub's pulls API,
//     for example, has no since filter, so ascending-from-watermark would
//     re-enumerate the entire repository every maintenance pass). What it
//     does guarantee, combined with an inclusive UpdatedSince watermark taken
//     at scan start, is no permanent skips: an item updated mid-scan must be
//     returned either later in the same scan or by any subsequent scan whose
//     watermark predates that update.
//   - Consumers must not assume a traversal direction for ItemOrderUpdated,
//     and under either order items that change mid-traversal may be observed
//     more than once; consumers dedupe by provider identity through
//     idempotent upserts.
//   - For ItemStateOpen queries, Order is accepted but the traversal order is
//     contractually unspecified: an open scan is an exhaustive single-shot
//     drain of the current open set, so ordering is immaterial and providers
//     may serve their native default order.
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

// invalidItemPageQuery builds the typed invalid_argument error
// ValidateItemPageQuery returns. It carries no provider identity because the
// query is rejected before any provider is consulted.
func invalidItemPageQuery(field, format string, args ...any) error {
	return &Error{
		Code:  ErrCodeInvalidArgument,
		Field: field,
		Err:   fmt.Errorf(format, args...),
	}
}

// ValidateItemPageQuery rejects query shapes the canonical page contract does
// not define, so an unknown state or order can never silently dispatch some
// default traversal. Provider implementations call it at the top of their
// ListIssuesPage/ListMergeRequestsPage entry points. Open scans are
// single-shot current-index reads, so they carry neither a resumption cursor
// nor a watermark; UpdatedSince only has meaning for updated-order traversal.
func ValidateItemPageQuery(q ItemPageQuery) error {
	switch q.State {
	case ItemStateOpen, ItemStateAll:
	default:
		return invalidItemPageQuery("state", "unknown item state filter %q", q.State)
	}
	switch q.Order {
	case ItemOrderCreated, ItemOrderUpdated:
	default:
		return invalidItemPageQuery("order", "unknown item order %q", q.Order)
	}
	if q.State == ItemStateOpen {
		if q.Cursor != "" {
			return invalidItemPageQuery("cursor", "open scans are single-shot and do not accept a cursor")
		}
		if q.UpdatedSince != nil {
			return invalidItemPageQuery("updated_since", "open scans do not accept an updated_since watermark")
		}
	}
	if q.UpdatedSince != nil && q.Order != ItemOrderUpdated {
		return invalidItemPageQuery("updated_since", "updated_since requires order %q", ItemOrderUpdated)
	}
	return nil
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
		if _, ok := seen[cursor]; ok {
			// A detected cycle outranks the page budget: a cursor that first
			// recurs on the budget-exhausting fetch is still a contract
			// violation, not an oversized dataset.
			return nil, collectContractError(
				"collect_pages_cursor",
				"page collection revisited cursor %q", cursor,
			)
		}
		if pages >= MaxCollectPages {
			return nil, &Error{
				Code:  ErrCodePageLimit,
				Field: "collect_pages_bound",
				Err:   fmt.Errorf("page collection exceeded the maximum of %d pages", MaxCollectPages),
			}
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
