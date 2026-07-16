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
	if err := r.prepare(ref, ArchiveCapabilityHistoricalIssues); err != nil {
		return ArchivePage[Issue]{}, err
	}
	page, err := r.reader.ListHistoricalIssues(ctx, ref, cursor)
	if err != nil {
		return ArchivePage[Issue]{}, err
	}
	return page, r.validateIssuePage(ref, cursor, page)
}

func (r *validatingArchiveReader) ListHistoricalMergeRequests(
	ctx context.Context,
	ref RepoRef,
	cursor string,
) (ArchivePage[MergeRequest], error) {
	if err := r.prepare(ref, ArchiveCapabilityHistoricalMergeRequests); err != nil {
		return ArchivePage[MergeRequest]{}, err
	}
	page, err := r.reader.ListHistoricalMergeRequests(ctx, ref, cursor)
	if err != nil {
		return ArchivePage[MergeRequest]{}, err
	}
	return page, r.validateMergeRequestPage(ref, cursor, page)
}

func (r *validatingArchiveReader) ListUpdatedIssues(
	ctx context.Context,
	ref RepoRef,
	watermark time.Time,
	cursor string,
) (ArchivePage[Issue], error) {
	if err := r.prepare(ref, ArchiveCapabilityHistoricalIssues); err != nil {
		return ArchivePage[Issue]{}, err
	}
	page, err := r.reader.ListUpdatedIssues(ctx, ref, watermark, cursor)
	if err != nil {
		return ArchivePage[Issue]{}, err
	}
	return page, r.validateIssuePage(ref, cursor, page)
}

func (r *validatingArchiveReader) ListUpdatedMergeRequests(
	ctx context.Context,
	ref RepoRef,
	watermark time.Time,
	cursor string,
) (ArchivePage[MergeRequest], error) {
	if err := r.prepare(ref, ArchiveCapabilityHistoricalMergeRequests); err != nil {
		return ArchivePage[MergeRequest]{}, err
	}
	page, err := r.reader.ListUpdatedMergeRequests(ctx, ref, watermark, cursor)
	if err != nil {
		return ArchivePage[MergeRequest]{}, err
	}
	return page, r.validateMergeRequestPage(ref, cursor, page)
}

func (r *validatingArchiveReader) GetArchiveIssue(
	ctx context.Context,
	ref RepoRef,
	number int,
) (ArchiveItemResult[Issue], error) {
	if err := r.prepareItem(ref, number, ArchiveCapabilityHistoricalIssues); err != nil {
		return ArchiveItemResult[Issue]{}, err
	}
	result, err := r.reader.GetArchiveIssue(ctx, ref, number)
	if err != nil {
		return ArchiveItemResult[Issue]{}, err
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
	if err := r.validateSource(ref, result.Item.Repo, "archive_item_repo"); err != nil {
		return result, err
	}
	if result.Item.Number != number {
		return result, ProviderContract(
			r.kind,
			r.host,
			"archive_item_number",
			fmt.Errorf("provider returned issue %d for requested issue %d", result.Item.Number, number),
		)
	}
	return result, nil
}

func (r *validatingArchiveReader) GetArchiveMergeRequest(
	ctx context.Context,
	ref RepoRef,
	number int,
) (ArchiveItemResult[MergeRequest], error) {
	if err := r.prepareItem(ref, number, ArchiveCapabilityHistoricalMergeRequests); err != nil {
		return ArchiveItemResult[MergeRequest]{}, err
	}
	result, err := r.reader.GetArchiveMergeRequest(ctx, ref, number)
	if err != nil {
		return ArchiveItemResult[MergeRequest]{}, err
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
	if err := r.validateSource(ref, result.Item.Repo, "archive_item_repo"); err != nil {
		return result, err
	}
	if result.Item.Number != number {
		return result, ProviderContract(
			r.kind,
			r.host,
			"archive_item_number",
			fmt.Errorf(
				"provider returned merge request %d for requested merge request %d",
				result.Item.Number,
				number,
			),
		)
	}
	return result, nil
}

func (r *validatingArchiveReader) ListArchiveIssueComments(
	ctx context.Context,
	ref RepoRef,
	number int,
	cursor string,
) (ArchivePage[IssueEvent], error) {
	if err := r.prepareItem(
		ref,
		number,
		ArchiveCapabilityHistoricalIssues,
		ArchiveCapabilityOrdinaryComments,
	); err != nil {
		return ArchivePage[IssueEvent]{}, err
	}
	page, err := r.reader.ListArchiveIssueComments(ctx, ref, number, cursor)
	if err != nil {
		return ArchivePage[IssueEvent]{}, err
	}
	if err := ValidateArchivePage(r.kind, r.host, cursor, page); err != nil {
		return page, err
	}
	for _, event := range page.Items {
		if err := r.validateSource(ref, event.Repo, "archive_event_repo"); err != nil {
			return page, err
		}
		if event.IssueNumber != number {
			return page, ProviderContract(
				r.kind,
				r.host,
				"archive_event_number",
				fmt.Errorf(
					"provider returned issue event for %d under requested issue %d",
					event.IssueNumber,
					number,
				),
			)
		}
	}
	return page, nil
}

func (r *validatingArchiveReader) ListArchiveMergeRequestComments(
	ctx context.Context,
	ref RepoRef,
	number int,
	cursor string,
) (ArchivePage[MergeRequestEvent], error) {
	return r.listMergeRequestEvents(
		ctx,
		ref,
		number,
		cursor,
		ArchiveCapabilityOrdinaryComments,
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
		ctx,
		ref,
		number,
		cursor,
		ArchiveCapabilitySubmittedReviews,
		r.reader.ListArchiveSubmittedReviews,
	)
}

func (r *validatingArchiveReader) ListArchiveReviewThreads(
	ctx context.Context,
	ref RepoRef,
	number int,
	cursor string,
) (ArchivePage[MergeRequestReviewThread], error) {
	if err := r.prepareItem(
		ref,
		number,
		ArchiveCapabilityHistoricalMergeRequests,
		ArchiveCapabilityInlineReviewComments,
	); err != nil {
		return ArchivePage[MergeRequestReviewThread]{}, err
	}
	page, err := r.reader.ListArchiveReviewThreads(ctx, ref, number, cursor)
	if err != nil {
		return ArchivePage[MergeRequestReviewThread]{}, err
	}
	if err := ValidateArchivePage(r.kind, r.host, cursor, page); err != nil {
		return page, err
	}
	for _, thread := range page.Items {
		if err := r.validateSource(ref, thread.Repo, "archive_thread_repo"); err != nil {
			return page, err
		}
		if thread.MergeRequestNumber != number {
			return page, ProviderContract(
				r.kind,
				r.host,
				"archive_thread_number",
				fmt.Errorf(
					"provider returned review thread for %d under requested merge request %d",
					thread.MergeRequestNumber,
					number,
				),
			)
		}
	}
	return page, nil
}

func (r *validatingArchiveReader) prepare(ref RepoRef, capabilities ...ArchiveCapability) error {
	if err := ValidateCanonicalRepoRef(ref); err != nil {
		return r.invalidRequestedRef(err)
	}
	if ref.Platform != r.kind || ref.Host != r.host {
		return r.invalidRequestedRef(fmt.Errorf(
			"repository belongs to %s/%s, not registered provider %s/%s",
			ref.Platform,
			ref.Host,
			r.kind,
			r.host,
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
			Code:         ErrCodeInvalidArgument,
			Provider:     r.kind,
			PlatformHost: r.host,
			Field:        "item_number",
			Err:          fmt.Errorf("item number must be positive: %d", number),
		}
	}
	return nil
}

func (r *validatingArchiveReader) invalidRequestedRef(err error) error {
	return &Error{
		Code:         ErrCodeInvalidRepoRef,
		Provider:     r.kind,
		PlatformHost: r.host,
		Field:        "repo",
		Err:          err,
	}
}

func (r *validatingArchiveReader) validateIssuePage(
	ref RepoRef,
	cursor string,
	page ArchivePage[Issue],
) error {
	if err := ValidateArchivePage(r.kind, r.host, cursor, page); err != nil {
		return err
	}
	for _, issue := range page.Items {
		if err := r.validateSource(ref, issue.Repo, "archive_item_repo"); err != nil {
			return err
		}
		if issue.Number <= 0 {
			return ProviderContract(
				r.kind,
				r.host,
				"archive_item_number",
				fmt.Errorf("provider returned nonpositive issue number %d", issue.Number),
			)
		}
	}
	return nil
}

func (r *validatingArchiveReader) validateMergeRequestPage(
	ref RepoRef,
	cursor string,
	page ArchivePage[MergeRequest],
) error {
	if err := ValidateArchivePage(r.kind, r.host, cursor, page); err != nil {
		return err
	}
	for _, mergeRequest := range page.Items {
		if err := r.validateSource(ref, mergeRequest.Repo, "archive_item_repo"); err != nil {
			return err
		}
		if mergeRequest.Number <= 0 {
			return ProviderContract(
				r.kind,
				r.host,
				"archive_item_number",
				fmt.Errorf(
					"provider returned nonpositive merge request number %d",
					mergeRequest.Number,
				),
			)
		}
	}
	return nil
}

func (r *validatingArchiveReader) listMergeRequestEvents(
	ctx context.Context,
	ref RepoRef,
	number int,
	cursor string,
	dataset ArchiveCapability,
	list func(context.Context, RepoRef, int, string) (ArchivePage[MergeRequestEvent], error),
) (ArchivePage[MergeRequestEvent], error) {
	if err := r.prepareItem(
		ref,
		number,
		ArchiveCapabilityHistoricalMergeRequests,
		dataset,
	); err != nil {
		return ArchivePage[MergeRequestEvent]{}, err
	}
	page, err := list(ctx, ref, number, cursor)
	if err != nil {
		return ArchivePage[MergeRequestEvent]{}, err
	}
	if err := ValidateArchivePage(r.kind, r.host, cursor, page); err != nil {
		return page, err
	}
	for _, event := range page.Items {
		if err := r.validateSource(ref, event.Repo, "archive_event_repo"); err != nil {
			return page, err
		}
		if event.MergeRequestNumber != number {
			return page, ProviderContract(
				r.kind,
				r.host,
				"archive_event_number",
				fmt.Errorf(
					"provider returned merge request event for %d under requested merge request %d",
					event.MergeRequestNumber,
					number,
				),
			)
		}
	}
	return page, nil
}

func (r *validatingArchiveReader) validateSource(requested, returned RepoRef, field string) error {
	equal, err := CanonicalRepoRefsEqual(requested, returned)
	if err != nil {
		return ProviderContract(
			r.kind,
			r.host,
			field,
			fmt.Errorf("provider returned invalid repository identity: %v", err),
		)
	}
	if !equal {
		return ProviderContract(
			r.kind,
			r.host,
			field,
			fmt.Errorf(
				"provider returned repository %s/%s/%s/%s for requested %s/%s/%s/%s",
				returned.Platform,
				returned.Host,
				returned.Owner,
				returned.Name,
				requested.Platform,
				requested.Host,
				requested.Owner,
				requested.Name,
			),
		)
	}
	return nil
}

func (r *validatingArchiveReader) validateMovedDestination(
	requested RepoRef,
	destination *RepoRef,
) error {
	if destination == nil {
		return ProviderContract(
			r.kind,
			r.host,
			"archive_lookup_destination",
			fmt.Errorf("moved archive item has no destination repository"),
		)
	}
	equal, err := CanonicalRepoRefsEqual(requested, *destination)
	if err != nil {
		return ProviderContract(r.kind, r.host, "archive_lookup_destination", err)
	}
	if equal {
		return ProviderContract(
			r.kind,
			r.host,
			"archive_lookup_destination",
			fmt.Errorf("moved archive item destination equals its source repository"),
		)
	}
	return nil
}
