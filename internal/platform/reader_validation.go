package platform

import (
	"context"
	"fmt"
)

// readerContract carries the registered provider identity every validating
// reader wrapper enforces on requested references and provider-returned
// values. Exactly one implementation of each contract check lives on it; the
// canonical page-reader wrappers share these helpers.
type readerContract struct {
	kind Kind
	host string
}

// requireRequestedRef rejects a read whose repository reference is not
// canonical or does not belong to the registered provider identity.
func (c readerContract) requireRequestedRef(ref RepoRef) error {
	if err := ValidateCanonicalRepoRef(ref); err != nil {
		return c.invalidRequestedRef(err)
	}
	if ref.Platform != c.kind || ref.Host != c.host {
		return c.invalidRequestedRef(fmt.Errorf(
			"repository belongs to %s/%s, not registered provider %s/%s",
			ref.Platform, ref.Host, c.kind, c.host,
		))
	}
	return nil
}

// requirePositiveItemNumber rejects item-scoped reads before any provider
// call: several provider APIs route nonpositive numbers onto unrelated
// collection URLs instead of failing.
func (c readerContract) requirePositiveItemNumber(number int) error {
	if number > 0 {
		return nil
	}
	return &Error{
		Code: ErrCodeInvalidArgument, Provider: c.kind, PlatformHost: c.host,
		Field: "item_number", Err: fmt.Errorf("item number must be positive: %d", number),
	}
}

func (c readerContract) invalidRequestedRef(err error) error {
	return &Error{
		Code: ErrCodeInvalidRepoRef, Provider: c.kind, PlatformHost: c.host,
		Field: "repo", Err: err,
	}
}

// validateNumberedSource verifies a returned value's repository identity and
// item number against the requested read. expectedNumber zero means any
// positive number is acceptable (inventory pages); a positive expectedNumber
// pins the value to the requested parent item (detail datasets and lookups).
func (c readerContract) validateNumberedSource(
	requested RepoRef,
	returned RepoRef,
	returnedNumber int,
	expectedNumber int,
	sourceField string,
	numberField string,
	itemName string,
	parentName string,
) error {
	if err := c.validateSource(requested, returned, sourceField); err != nil {
		return err
	}
	if expectedNumber == 0 && returnedNumber > 0 || expectedNumber > 0 && returnedNumber == expectedNumber {
		return nil
	}
	var err error
	if expectedNumber == 0 {
		err = fmt.Errorf("provider returned nonpositive %s number %d", itemName, returnedNumber)
	} else if itemName == parentName {
		err = fmt.Errorf("provider returned %s %d for requested %s %d", itemName, returnedNumber, parentName, expectedNumber)
	} else {
		err = fmt.Errorf("provider returned %s for %d under requested %s %d", itemName, returnedNumber, parentName, expectedNumber)
	}
	return ProviderContract(c.kind, c.host, numberField, err)
}

func (c readerContract) validateSource(requested, returned RepoRef, field string) error {
	equal, err := CanonicalRepoRefsEqual(requested, returned)
	if err != nil {
		return ProviderContract(
			c.kind, c.host, field,
			fmt.Errorf("provider returned invalid repository identity: %v", err),
		)
	}
	if equal {
		return nil
	}
	return ProviderContract(
		c.kind, c.host, field,
		fmt.Errorf(
			"provider returned repository %s/%s/%s/%s for requested %s/%s/%s/%s",
			returned.Platform, returned.Host, returned.Owner, returned.Name,
			requested.Platform, requested.Host, requested.Owner, requested.Name,
		),
	)
}

// validateMovedDestination rejects a moved lookup outcome whose destination is
// missing, noncanonical, or identical to the source repository.
func (c readerContract) validateMovedDestination(
	requested RepoRef,
	destination *RepoRef,
	field string,
) error {
	if destination == nil {
		return ProviderContract(
			c.kind, c.host, field,
			fmt.Errorf("moved item has no destination repository"),
		)
	}
	equal, err := CanonicalRepoRefsEqual(requested, *destination)
	if err != nil {
		return ProviderContract(c.kind, c.host, field, err)
	}
	if equal {
		return ProviderContract(
			c.kind, c.host, field,
			fmt.Errorf("moved item destination equals its source repository"),
		)
	}
	return nil
}

// validateReaderPage applies the shared post-read page checks: page/cursor
// invariants (including echoed-cursor rejection) and per-item identity
// validation.
func validateReaderPage[T any](
	c readerContract,
	inputCursor string,
	page Page[T],
	validateItem func(T) error,
) error {
	if err := ValidatePage(c.kind, c.host, inputCursor, page); err != nil {
		return err
	}
	for _, item := range page.Items {
		if err := validateItem(item); err != nil {
			return err
		}
	}
	return nil
}

// validateReaderLookup applies the shared post-read lookup checks: typed
// outcome validation, moved-destination identity validation, and present-item
// identity validation against the requested reference and number.
func validateReaderLookup[T any](
	c readerContract,
	ref RepoRef,
	number int,
	result ItemLookup[T],
	identity func(T) (RepoRef, int),
	sourceField string,
	numberField string,
	destinationField string,
	itemName string,
) error {
	if err := ValidateItemLookup(c.kind, c.host, result); err != nil {
		return err
	}
	if result.Outcome == LookupMoved {
		return c.validateMovedDestination(ref, result.Destination, destinationField)
	}
	if result.Outcome != LookupPresent {
		return nil
	}
	itemRef, itemNumber := identity(result.Item)
	return c.validateNumberedSource(
		ref, itemRef, itemNumber, number,
		sourceField, numberField, itemName, itemName,
	)
}

// requireLiveCapability refuses a live canonical read the provider does not
// declare.
func requireLiveCapability(c readerContract, supported bool, capability string) error {
	if supported {
		return nil
	}
	return UnsupportedCapability(c.kind, c.host, capability)
}

type pageReaderValidation struct {
	contract readerContract
	caps     Capabilities
}

func (r pageReaderValidation) prepareItem(ref RepoRef, number int) error {
	if err := r.contract.requireRequestedRef(ref); err != nil {
		return err
	}
	return r.contract.requirePositiveItemNumber(number)
}

func (r pageReaderValidation) requireArchive(capabilities ...ArchiveCapability) error {
	for _, capability := range capabilities {
		if err := r.caps.Archive.Require(r.contract.kind, r.contract.host, capability); err != nil {
			return err
		}
	}
	return nil
}

func (r pageReaderValidation) requireScanCapability(
	query ItemPageQuery,
	liveSupported bool,
	liveCapability string,
	archiveCapability ArchiveCapability,
) error {
	if query.Order == ItemOrderCreated {
		return r.requireArchive(archiveCapability)
	}
	return requireLiveCapability(r.contract, liveSupported, liveCapability)
}

// validatingIssuePageReader wraps a provider's canonical issue reader with the
// provider-neutral contract checks, so no caller ever consumes an unvalidated
// canonical read.
type validatingIssuePageReader struct {
	reader IssuePageReader
	pageReaderValidation
}

func (r *validatingIssuePageReader) ListIssuesPage(
	ctx context.Context,
	ref RepoRef,
	query ItemPageQuery,
) (Page[Issue], error) {
	if err := r.contract.requireRequestedRef(ref); err != nil {
		return Page[Issue]{}, err
	}
	if err := ValidateItemPageQuery(query); err != nil {
		return Page[Issue]{}, err
	}
	if err := r.requireScanCapability(
		query, r.caps.ReadIssues, "read_issues", ArchiveCapabilityHistoricalIssues,
	); err != nil {
		return Page[Issue]{}, err
	}
	page, err := r.reader.ListIssuesPage(ctx, ref, query)
	if err != nil {
		return Page[Issue]{}, err
	}
	if err := validateReaderPage(r.contract, query.Cursor, page, func(issue Issue) error {
		return r.contract.validateNumberedSource(
			ref, issue.Repo, issue.Number, 0, "item_repo", "item_number", "issue", "issue",
		)
	}); err != nil {
		return page, err
	}
	return page, nil
}

func (r *validatingIssuePageReader) LookupIssue(
	ctx context.Context,
	ref RepoRef,
	number int,
) (ItemLookup[Issue], error) {
	if err := r.prepareItem(ref, number); err != nil {
		return ItemLookup[Issue]{}, err
	}
	if err := requireLiveCapability(r.contract, r.caps.ReadIssues, "read_issues"); err != nil {
		return ItemLookup[Issue]{}, err
	}
	result, err := r.reader.LookupIssue(ctx, ref, number)
	if err != nil {
		return ItemLookup[Issue]{}, err
	}
	if err := validateReaderLookup(
		r.contract, ref, number, result,
		func(issue Issue) (RepoRef, int) { return issue.Repo, issue.Number },
		"item_repo", "item_number", "lookup_destination", "issue",
	); err != nil {
		return result, err
	}
	return result, nil
}

func (r *validatingIssuePageReader) ListIssueCommentsPage(
	ctx context.Context,
	ref RepoRef,
	number int,
	cursor string,
) (Page[IssueEvent], error) {
	if err := r.prepareItem(ref, number); err != nil {
		return Page[IssueEvent]{}, err
	}
	if err := r.requireArchive(
		ArchiveCapabilityHistoricalIssues, ArchiveCapabilityOrdinaryComments,
	); err != nil {
		return Page[IssueEvent]{}, err
	}
	page, err := r.reader.ListIssueCommentsPage(ctx, ref, number, cursor)
	if err != nil {
		return Page[IssueEvent]{}, err
	}
	if err := validateReaderPage(r.contract, cursor, page, func(event IssueEvent) error {
		return r.contract.validateNumberedSource(
			ref, event.Repo, event.IssueNumber, number,
			"event_repo", "event_number", "issue event", "issue",
		)
	}); err != nil {
		return page, err
	}
	return page, nil
}

// validatingMergeRequestPageReader is the merge-request counterpart to
// validatingIssuePageReader.
type validatingMergeRequestPageReader struct {
	reader MergeRequestPageReader
	pageReaderValidation
}

func (r *validatingMergeRequestPageReader) ListMergeRequestsPage(
	ctx context.Context,
	ref RepoRef,
	query ItemPageQuery,
) (Page[MergeRequest], error) {
	if err := r.contract.requireRequestedRef(ref); err != nil {
		return Page[MergeRequest]{}, err
	}
	if err := ValidateItemPageQuery(query); err != nil {
		return Page[MergeRequest]{}, err
	}
	if err := r.requireScanCapability(
		query, r.caps.ReadMergeRequests, "read_merge_requests",
		ArchiveCapabilityHistoricalMergeRequests,
	); err != nil {
		return Page[MergeRequest]{}, err
	}
	page, err := r.reader.ListMergeRequestsPage(ctx, ref, query)
	if err != nil {
		return Page[MergeRequest]{}, err
	}
	if err := validateReaderPage(r.contract, query.Cursor, page, func(mr MergeRequest) error {
		return r.contract.validateNumberedSource(
			ref, mr.Repo, mr.Number, 0,
			"item_repo", "item_number", "merge request", "merge request",
		)
	}); err != nil {
		return page, err
	}
	return page, nil
}

func (r *validatingMergeRequestPageReader) LookupMergeRequest(
	ctx context.Context,
	ref RepoRef,
	number int,
) (ItemLookup[MergeRequest], error) {
	if err := r.prepareItem(ref, number); err != nil {
		return ItemLookup[MergeRequest]{}, err
	}
	if err := requireLiveCapability(
		r.contract, r.caps.ReadMergeRequests, "read_merge_requests",
	); err != nil {
		return ItemLookup[MergeRequest]{}, err
	}
	result, err := r.reader.LookupMergeRequest(ctx, ref, number)
	if err != nil {
		return ItemLookup[MergeRequest]{}, err
	}
	if err := validateReaderLookup(
		r.contract, ref, number, result,
		func(mr MergeRequest) (RepoRef, int) { return mr.Repo, mr.Number },
		"item_repo", "item_number", "lookup_destination", "merge request",
	); err != nil {
		return result, err
	}
	return result, nil
}

func (r *validatingMergeRequestPageReader) ListMergeRequestCommentsPage(
	ctx context.Context,
	ref RepoRef,
	number int,
	cursor string,
) (Page[MergeRequestEvent], error) {
	return r.eventPage(
		ctx, ref, number, cursor, ArchiveCapabilityOrdinaryComments,
		r.reader.ListMergeRequestCommentsPage,
	)
}

func (r *validatingMergeRequestPageReader) ListSubmittedReviewsPage(
	ctx context.Context,
	ref RepoRef,
	number int,
	cursor string,
) (Page[MergeRequestEvent], error) {
	return r.eventPage(
		ctx, ref, number, cursor, ArchiveCapabilitySubmittedReviews,
		r.reader.ListSubmittedReviewsPage,
	)
}

func (r *validatingMergeRequestPageReader) ListReviewThreadsPage(
	ctx context.Context,
	ref RepoRef,
	number int,
	cursor string,
) (Page[MergeRequestReviewThread], error) {
	if err := r.prepareItem(ref, number); err != nil {
		return Page[MergeRequestReviewThread]{}, err
	}
	if err := r.requireArchive(
		ArchiveCapabilityHistoricalMergeRequests, ArchiveCapabilityInlineReviewComments,
	); err != nil {
		return Page[MergeRequestReviewThread]{}, err
	}
	page, err := r.reader.ListReviewThreadsPage(ctx, ref, number, cursor)
	if err != nil {
		return Page[MergeRequestReviewThread]{}, err
	}
	if err := validateReaderPage(r.contract, cursor, page, func(thread MergeRequestReviewThread) error {
		return r.contract.validateNumberedSource(
			ref, thread.Repo, thread.MergeRequestNumber, number,
			"thread_repo", "thread_number", "review thread", "merge request",
		)
	}); err != nil {
		return page, err
	}
	return page, nil
}

func (r *validatingMergeRequestPageReader) eventPage(
	ctx context.Context,
	ref RepoRef,
	number int,
	cursor string,
	dataset ArchiveCapability,
	read func(context.Context, RepoRef, int, string) (Page[MergeRequestEvent], error),
) (Page[MergeRequestEvent], error) {
	if err := r.prepareItem(ref, number); err != nil {
		return Page[MergeRequestEvent]{}, err
	}
	if err := r.requireArchive(
		ArchiveCapabilityHistoricalMergeRequests, dataset,
	); err != nil {
		return Page[MergeRequestEvent]{}, err
	}
	page, err := read(ctx, ref, number, cursor)
	if err != nil {
		return Page[MergeRequestEvent]{}, err
	}
	if err := validateReaderPage(r.contract, cursor, page, func(event MergeRequestEvent) error {
		return r.contract.validateNumberedSource(
			ref, event.Repo, event.MergeRequestNumber, number,
			"event_repo", "event_number", "merge request event", "merge request",
		)
	}); err != nil {
		return page, err
	}
	return page, nil
}
