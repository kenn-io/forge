package platform

import (
	"context"
	"fmt"
)

// ListOpenIssues drains the canonical open-issue scan into a flat slice. Open
// scans are exhaustive single-shot reads of the current open set; traversal
// order is contractually unspecified, so the collector requests the
// provider-efficient updated order.
func ListOpenIssues(ctx context.Context, r IssuePageReader, ref RepoRef) ([]Issue, error) {
	return CollectPages(ctx, "", func(ctx context.Context, cursor string) (Page[Issue], error) {
		return r.ListIssuesPage(ctx, ref, ItemPageQuery{
			State: ItemStateOpen, Order: ItemOrderUpdated, Cursor: cursor,
		})
	})
}

// ListOpenMergeRequests is the merge-request counterpart to ListOpenIssues.
func ListOpenMergeRequests(
	ctx context.Context,
	r MergeRequestPageReader,
	ref RepoRef,
) ([]MergeRequest, error) {
	return CollectPages(ctx, "", func(ctx context.Context, cursor string) (Page[MergeRequest], error) {
		return r.ListMergeRequestsPage(ctx, ref, ItemPageQuery{
			State: ItemStateOpen, Order: ItemOrderUpdated, Cursor: cursor,
		})
	})
}

// RequireIssue resolves a single issue through the canonical lookup and
// requires a present outcome, mapping every other typed outcome onto the
// standard live error codes via lookupNotPresent.
func RequireIssue(ctx context.Context, r IssuePageReader, ref RepoRef, number int) (Issue, error) {
	lookup, err := r.LookupIssue(ctx, ref, number)
	if err != nil {
		return Issue{}, err
	}
	if lookup.Outcome != LookupPresent {
		return Issue{}, lookupNotPresent(ref, number, lookup.Outcome, lookup.Destination)
	}
	return lookup.Item, nil
}

// RequireMergeRequest is the merge-request counterpart to RequireIssue.
func RequireMergeRequest(
	ctx context.Context,
	r MergeRequestPageReader,
	ref RepoRef,
	number int,
) (MergeRequest, error) {
	lookup, err := r.LookupMergeRequest(ctx, ref, number)
	if err != nil {
		return MergeRequest{}, err
	}
	if lookup.Outcome != LookupPresent {
		return MergeRequest{}, lookupNotPresent(ref, number, lookup.Outcome, lookup.Destination)
	}
	return lookup.Item, nil
}

// lookupNotPresent renders the typed error a live caller receives when a
// single-item lookup resolves to a non-present outcome. Live callers require
// present; archive callers inspect the outcome instead. The outcomes must not
// collapse: removed is not_found, inaccessible is permission_denied, and
// moved is not_found carrying the destination repository so callers can
// retarget the reference.
func lookupNotPresent(
	ref RepoRef,
	number int,
	outcome LookupOutcome,
	destination *RepoRef,
) error {
	code := ErrCodeNotFound
	if outcome == LookupInaccessible {
		code = ErrCodePermissionDenied
	}
	return &Error{
		Code:         code,
		Provider:     ref.Platform,
		PlatformHost: ref.Host,
		Destination:  destination,
		Err:          fmt.Errorf("%s#%d is not present (%s)", ref.DisplayName(), number, outcome),
	}
}
