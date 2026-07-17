package platform

import (
	"context"
	"time"
)

// validatingArchiveReader wraps the archive-prefixed reader interface with the
// shared readerContract checks. The contract check implementations live in
// reader_validation.go and are shared with the canonical page-reader
// wrappers; this wrapper only maps them onto the archive method set and the
// archive error field names.
type validatingArchiveReader struct {
	reader   ArchiveReader
	contract readerContract
	caps     ArchiveCapabilities
}

func (r *validatingArchiveReader) ListHistoricalIssues(
	ctx context.Context,
	ref RepoRef,
	cursor string,
) (Page[Issue], error) {
	return validateArchivePageRead(
		ctx, r, ref, cursor, []ArchiveCapability{ArchiveCapabilityHistoricalIssues},
		r.reader.ListHistoricalIssues,
		func(issue Issue) error {
			return r.contract.validateNumberedSource(ref, issue.Repo, issue.Number, 0, "archive_item_repo", "archive_item_number", "issue", "issue")
		},
	)
}

func (r *validatingArchiveReader) ListHistoricalMergeRequests(
	ctx context.Context,
	ref RepoRef,
	cursor string,
) (Page[MergeRequest], error) {
	return validateArchivePageRead(
		ctx, r, ref, cursor, []ArchiveCapability{ArchiveCapabilityHistoricalMergeRequests},
		r.reader.ListHistoricalMergeRequests,
		func(mr MergeRequest) error {
			return r.contract.validateNumberedSource(ref, mr.Repo, mr.Number, 0, "archive_item_repo", "archive_item_number", "merge request", "merge request")
		},
	)
}

func (r *validatingArchiveReader) ListUpdatedIssues(
	ctx context.Context,
	ref RepoRef,
	watermark time.Time,
	cursor string,
) (Page[Issue], error) {
	return validateArchivePageRead(
		ctx, r, ref, cursor, []ArchiveCapability{ArchiveCapabilityHistoricalIssues},
		func(ctx context.Context, ref RepoRef, cursor string) (Page[Issue], error) {
			return r.reader.ListUpdatedIssues(ctx, ref, watermark, cursor)
		},
		func(issue Issue) error {
			return r.contract.validateNumberedSource(ref, issue.Repo, issue.Number, 0, "archive_item_repo", "archive_item_number", "issue", "issue")
		},
	)
}

func (r *validatingArchiveReader) ListUpdatedMergeRequests(
	ctx context.Context,
	ref RepoRef,
	watermark time.Time,
	cursor string,
) (Page[MergeRequest], error) {
	return validateArchivePageRead(
		ctx, r, ref, cursor, []ArchiveCapability{ArchiveCapabilityHistoricalMergeRequests},
		func(ctx context.Context, ref RepoRef, cursor string) (Page[MergeRequest], error) {
			return r.reader.ListUpdatedMergeRequests(ctx, ref, watermark, cursor)
		},
		func(mr MergeRequest) error {
			return r.contract.validateNumberedSource(ref, mr.Repo, mr.Number, 0, "archive_item_repo", "archive_item_number", "merge request", "merge request")
		},
	)
}

func (r *validatingArchiveReader) GetArchiveIssue(
	ctx context.Context,
	ref RepoRef,
	number int,
) (ItemLookup[Issue], error) {
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
) (ItemLookup[MergeRequest], error) {
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
) (Page[IssueEvent], error) {
	return validateArchivePageRead(
		ctx, r, ref, cursor,
		[]ArchiveCapability{ArchiveCapabilityHistoricalIssues, ArchiveCapabilityOrdinaryComments},
		func(ctx context.Context, ref RepoRef, cursor string) (Page[IssueEvent], error) {
			return r.reader.ListArchiveIssueComments(ctx, ref, number, cursor)
		},
		func(event IssueEvent) error {
			return r.contract.validateNumberedSource(ref, event.Repo, event.IssueNumber, number, "archive_event_repo", "archive_event_number", "issue event", "issue")
		},
	)
}

func (r *validatingArchiveReader) ListArchiveMergeRequestComments(
	ctx context.Context,
	ref RepoRef,
	number int,
	cursor string,
) (Page[MergeRequestEvent], error) {
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
) (Page[MergeRequestEvent], error) {
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
) (Page[MergeRequestReviewThread], error) {
	return validateArchivePageRead(
		ctx, r, ref, cursor,
		[]ArchiveCapability{ArchiveCapabilityHistoricalMergeRequests, ArchiveCapabilityInlineReviewComments},
		func(ctx context.Context, ref RepoRef, cursor string) (Page[MergeRequestReviewThread], error) {
			return r.reader.ListArchiveReviewThreads(ctx, ref, number, cursor)
		},
		func(thread MergeRequestReviewThread) error {
			return r.contract.validateNumberedSource(ref, thread.Repo, thread.MergeRequestNumber, number, "archive_thread_repo", "archive_thread_number", "review thread", "merge request")
		},
	)
}

func validateArchivePageRead[T any](
	ctx context.Context,
	r *validatingArchiveReader,
	ref RepoRef,
	cursor string,
	capabilities []ArchiveCapability,
	read func(context.Context, RepoRef, string) (Page[T], error),
	validateItem func(T) error,
) (Page[T], error) {
	if err := r.prepare(ref, capabilities...); err != nil {
		return Page[T]{}, err
	}
	page, err := read(ctx, ref, cursor)
	if err != nil {
		return Page[T]{}, err
	}
	if err := validateReaderPage(r.contract, cursor, page, validateItem); err != nil {
		return page, err
	}
	return page, nil
}

func validateArchiveItemRead[T any](
	ctx context.Context,
	r *validatingArchiveReader,
	ref RepoRef,
	number int,
	capability ArchiveCapability,
	read func(context.Context, RepoRef, int) (ItemLookup[T], error),
	identity func(T) (RepoRef, int),
	itemName string,
) (ItemLookup[T], error) {
	if err := r.prepareItem(ref, number, capability); err != nil {
		return ItemLookup[T]{}, err
	}
	result, err := read(ctx, ref, number)
	if err != nil {
		return ItemLookup[T]{}, err
	}
	if err := validateReaderLookup(
		r.contract, ref, number, result, identity,
		"archive_item_repo", "archive_item_number", "archive_lookup_destination", itemName,
	); err != nil {
		return result, err
	}
	return result, nil
}

func (r *validatingArchiveReader) listMergeRequestEvents(
	ctx context.Context,
	ref RepoRef,
	number int,
	cursor string,
	dataset ArchiveCapability,
	read func(context.Context, RepoRef, int, string) (Page[MergeRequestEvent], error),
) (Page[MergeRequestEvent], error) {
	return validateArchivePageRead(
		ctx, r, ref, cursor,
		[]ArchiveCapability{ArchiveCapabilityHistoricalMergeRequests, dataset},
		func(ctx context.Context, ref RepoRef, cursor string) (Page[MergeRequestEvent], error) {
			return read(ctx, ref, number, cursor)
		},
		func(event MergeRequestEvent) error {
			return r.contract.validateNumberedSource(ref, event.Repo, event.MergeRequestNumber, number, "archive_event_repo", "archive_event_number", "merge request event", "merge request")
		},
	)
}

func (r *validatingArchiveReader) prepare(ref RepoRef, capabilities ...ArchiveCapability) error {
	if err := r.contract.requireRequestedRef(ref); err != nil {
		return err
	}
	for _, capability := range capabilities {
		if err := r.caps.Require(r.contract.kind, r.contract.host, capability); err != nil {
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
	return r.contract.requirePositiveItemNumber(number)
}
