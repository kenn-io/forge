package platform

import (
	"context"
	"fmt"
	"time"
)

type validatingArchiveReader struct {
	reader ArchiveReader
	kind   Kind
	host   string
	caps   ArchiveCapabilities
}

func (r *validatingArchiveReader) ListHistoricalIssues(
	ctx context.Context,
	ref RepoRef,
	cursor string,
) (ArchivePage[Issue], error) {
	return validateArchivePageRead(
		ctx, r, ref, cursor, []ArchiveCapability{ArchiveCapabilityHistoricalIssues},
		r.reader.ListHistoricalIssues,
		func(issue Issue) error {
			return r.validateNumberedSource(ref, issue.Repo, issue.Number, 0, "archive_item_repo", "archive_item_number", "issue", "issue")
		},
	)
}

func (r *validatingArchiveReader) ListHistoricalMergeRequests(
	ctx context.Context,
	ref RepoRef,
	cursor string,
) (ArchivePage[MergeRequest], error) {
	return validateArchivePageRead(
		ctx, r, ref, cursor, []ArchiveCapability{ArchiveCapabilityHistoricalMergeRequests},
		r.reader.ListHistoricalMergeRequests,
		func(mr MergeRequest) error {
			return r.validateNumberedSource(ref, mr.Repo, mr.Number, 0, "archive_item_repo", "archive_item_number", "merge request", "merge request")
		},
	)
}

func (r *validatingArchiveReader) ListUpdatedIssues(
	ctx context.Context,
	ref RepoRef,
	watermark time.Time,
	cursor string,
) (ArchivePage[Issue], error) {
	return validateArchivePageRead(
		ctx, r, ref, cursor, []ArchiveCapability{ArchiveCapabilityHistoricalIssues},
		func(ctx context.Context, ref RepoRef, cursor string) (ArchivePage[Issue], error) {
			return r.reader.ListUpdatedIssues(ctx, ref, watermark, cursor)
		},
		func(issue Issue) error {
			return r.validateNumberedSource(ref, issue.Repo, issue.Number, 0, "archive_item_repo", "archive_item_number", "issue", "issue")
		},
	)
}

func (r *validatingArchiveReader) ListUpdatedMergeRequests(
	ctx context.Context,
	ref RepoRef,
	watermark time.Time,
	cursor string,
) (ArchivePage[MergeRequest], error) {
	return validateArchivePageRead(
		ctx, r, ref, cursor, []ArchiveCapability{ArchiveCapabilityHistoricalMergeRequests},
		func(ctx context.Context, ref RepoRef, cursor string) (ArchivePage[MergeRequest], error) {
			return r.reader.ListUpdatedMergeRequests(ctx, ref, watermark, cursor)
		},
		func(mr MergeRequest) error {
			return r.validateNumberedSource(ref, mr.Repo, mr.Number, 0, "archive_item_repo", "archive_item_number", "merge request", "merge request")
		},
	)
}

func (r *validatingArchiveReader) GetArchiveIssue(
	ctx context.Context,
	ref RepoRef,
	number int,
) (ArchiveItemResult[Issue], error) {
	return validateArchiveItemRead(
		ctx, r, ref, number, ArchiveCapabilityHistoricalIssues,
		r.reader.GetArchiveIssue,
		func(issue Issue) (RepoRef, int) { return issue.Repo, issue.Number },
		"issue",
	)
}

func (r *validatingArchiveReader) GetArchiveMergeRequest(
	ctx context.Context,
	ref RepoRef,
	number int,
) (ArchiveItemResult[MergeRequest], error) {
	return validateArchiveItemRead(
		ctx, r, ref, number, ArchiveCapabilityHistoricalMergeRequests,
		r.reader.GetArchiveMergeRequest,
		func(mr MergeRequest) (RepoRef, int) { return mr.Repo, mr.Number },
		"merge request",
	)
}

func (r *validatingArchiveReader) ListArchiveIssueComments(
	ctx context.Context,
	ref RepoRef,
	number int,
	cursor string,
) (ArchivePage[IssueEvent], error) {
	return validateArchivePageRead(
		ctx, r, ref, cursor,
		[]ArchiveCapability{ArchiveCapabilityHistoricalIssues, ArchiveCapabilityOrdinaryComments},
		func(ctx context.Context, ref RepoRef, cursor string) (ArchivePage[IssueEvent], error) {
			return r.reader.ListArchiveIssueComments(ctx, ref, number, cursor)
		},
		func(event IssueEvent) error {
			return r.validateNumberedSource(ref, event.Repo, event.IssueNumber, number, "archive_event_repo", "archive_event_number", "issue event", "issue")
		},
	)
}

func (r *validatingArchiveReader) ListArchiveMergeRequestComments(
	ctx context.Context,
	ref RepoRef,
	number int,
	cursor string,
) (ArchivePage[MergeRequestEvent], error) {
	return r.listMergeRequestEvents(
		ctx, ref, number, cursor, ArchiveCapabilityOrdinaryComments,
		r.reader.ListArchiveMergeRequestComments,
	)
}

func (r *validatingArchiveReader) ListArchiveSubmittedReviews(
	ctx context.Context,
	ref RepoRef,
	number int,
	cursor string,
) (ArchivePage[MergeRequestEvent], error) {
	return r.listMergeRequestEvents(
		ctx, ref, number, cursor, ArchiveCapabilitySubmittedReviews,
		r.reader.ListArchiveSubmittedReviews,
	)
}

func (r *validatingArchiveReader) ListArchiveReviewThreads(
	ctx context.Context,
	ref RepoRef,
	number int,
	cursor string,
) (ArchivePage[MergeRequestReviewThread], error) {
	return validateArchivePageRead(
		ctx, r, ref, cursor,
		[]ArchiveCapability{ArchiveCapabilityHistoricalMergeRequests, ArchiveCapabilityInlineReviewComments},
		func(ctx context.Context, ref RepoRef, cursor string) (ArchivePage[MergeRequestReviewThread], error) {
			return r.reader.ListArchiveReviewThreads(ctx, ref, number, cursor)
		},
		func(thread MergeRequestReviewThread) error {
			return r.validateNumberedSource(ref, thread.Repo, thread.MergeRequestNumber, number, "archive_thread_repo", "archive_thread_number", "review thread", "merge request")
		},
	)
}

func validateArchivePageRead[T any](
	ctx context.Context,
	r *validatingArchiveReader,
	ref RepoRef,
	cursor string,
	capabilities []ArchiveCapability,
	read func(context.Context, RepoRef, string) (ArchivePage[T], error),
	validateItem func(T) error,
) (ArchivePage[T], error) {
	if err := r.prepare(ref, capabilities...); err != nil {
		return ArchivePage[T]{}, err
	}
	page, err := read(ctx, ref, cursor)
	if err != nil {
		return ArchivePage[T]{}, err
	}
	if err := ValidateArchivePage(r.kind, r.host, cursor, page); err != nil {
		return page, err
	}
	for _, item := range page.Items {
		if err := validateItem(item); err != nil {
			return page, err
		}
	}
	return page, nil
}

func validateArchiveItemRead[T any](
	ctx context.Context,
	r *validatingArchiveReader,
	ref RepoRef,
	number int,
	capability ArchiveCapability,
	read func(context.Context, RepoRef, int) (ArchiveItemResult[T], error),
	identity func(T) (RepoRef, int),
	itemName string,
) (ArchiveItemResult[T], error) {
	if err := r.prepareItem(ref, number, capability); err != nil {
		return ArchiveItemResult[T]{}, err
	}
	result, err := read(ctx, ref, number)
	if err != nil {
		return ArchiveItemResult[T]{}, err
	}
	if err := ValidateArchiveItemResult(r.kind, r.host, result); err != nil {
		return result, err
	}
	if result.Outcome == ArchiveLookupMoved {
		return result, r.validateMovedDestination(ref, result.Destination)
	}
	if result.Outcome != ArchiveLookupPresent {
		return result, nil
	}
	itemRef, itemNumber := identity(result.Item)
	return result, r.validateNumberedSource(
		ref, itemRef, itemNumber, number,
		"archive_item_repo", "archive_item_number", itemName, itemName,
	)
}

func (r *validatingArchiveReader) listMergeRequestEvents(
	ctx context.Context,
	ref RepoRef,
	number int,
	cursor string,
	dataset ArchiveCapability,
	read func(context.Context, RepoRef, int, string) (ArchivePage[MergeRequestEvent], error),
) (ArchivePage[MergeRequestEvent], error) {
	return validateArchivePageRead(
		ctx, r, ref, cursor,
		[]ArchiveCapability{ArchiveCapabilityHistoricalMergeRequests, dataset},
		func(ctx context.Context, ref RepoRef, cursor string) (ArchivePage[MergeRequestEvent], error) {
			return read(ctx, ref, number, cursor)
		},
		func(event MergeRequestEvent) error {
			return r.validateNumberedSource(ref, event.Repo, event.MergeRequestNumber, number, "archive_event_repo", "archive_event_number", "merge request event", "merge request")
		},
	)
}

func (r *validatingArchiveReader) prepare(ref RepoRef, capabilities ...ArchiveCapability) error {
	if err := ValidateCanonicalRepoRef(ref); err != nil {
		return r.invalidRequestedRef(err)
	}
	if ref.Platform != r.kind || ref.Host != r.host {
		return r.invalidRequestedRef(fmt.Errorf(
			"repository belongs to %s/%s, not registered provider %s/%s",
			ref.Platform, ref.Host, r.kind, r.host,
		))
	}
	for _, capability := range capabilities {
		if err := r.caps.Require(r.kind, r.host, capability); err != nil {
			return err
		}
	}
	return nil
}

func (r *validatingArchiveReader) prepareItem(
	ref RepoRef,
	number int,
	capabilities ...ArchiveCapability,
) error {
	if err := r.prepare(ref, capabilities...); err != nil {
		return err
	}
	if number <= 0 {
		return &Error{
			Code: ErrCodeInvalidArgument, Provider: r.kind, PlatformHost: r.host,
			Field: "item_number", Err: fmt.Errorf("item number must be positive: %d", number),
		}
	}
	return nil
}

func (r *validatingArchiveReader) invalidRequestedRef(err error) error {
	return &Error{
		Code: ErrCodeInvalidRepoRef, Provider: r.kind, PlatformHost: r.host,
		Field: "repo", Err: err,
	}
}

func (r *validatingArchiveReader) validateNumberedSource(
	requested RepoRef,
	returned RepoRef,
	returnedNumber int,
	expectedNumber int,
	sourceField string,
	numberField string,
	itemName string,
	parentName string,
) error {
	if err := r.validateSource(requested, returned, sourceField); err != nil {
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
	return ProviderContract(r.kind, r.host, numberField, err)
}

func (r *validatingArchiveReader) validateSource(requested, returned RepoRef, field string) error {
	equal, err := CanonicalRepoRefsEqual(requested, returned)
	if err != nil {
		return ProviderContract(
			r.kind, r.host, field,
			fmt.Errorf("provider returned invalid repository identity: %v", err),
		)
	}
	if equal {
		return nil
	}
	return ProviderContract(
		r.kind, r.host, field,
		fmt.Errorf(
			"provider returned repository %s/%s/%s/%s for requested %s/%s/%s/%s",
			returned.Platform, returned.Host, returned.Owner, returned.Name,
			requested.Platform, requested.Host, requested.Owner, requested.Name,
		),
	)
}

func (r *validatingArchiveReader) validateMovedDestination(
	requested RepoRef,
	destination *RepoRef,
) error {
	if destination == nil {
		return ProviderContract(
			r.kind, r.host, "archive_lookup_destination",
			fmt.Errorf("moved archive item has no destination repository"),
		)
	}
	equal, err := CanonicalRepoRefsEqual(requested, *destination)
	if err != nil {
		return ProviderContract(r.kind, r.host, "archive_lookup_destination", err)
	}
	if equal {
		return ProviderContract(
			r.kind, r.host, "archive_lookup_destination",
			fmt.Errorf("moved archive item destination equals its source repository"),
		)
	}
	return nil
}
