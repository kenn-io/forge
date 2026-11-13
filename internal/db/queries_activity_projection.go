package db

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
)

// ListCollapsedActivityProjection reads the hidden-event high-water mark,
// directly rendered rows, and authoritative parents from one SQLite snapshot.
func (d *DB) ListCollapsedActivityProjection(
	ctx context.Context,
	opts ListActivityProjectionOpts,
) (ActivityProjection, error) {
	tx, err := d.ro.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return ActivityProjection{}, fmt.Errorf("begin collapsed activity projection: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	eventCursor, err := activityEventCursorWithQueryer(ctx, tx, opts.ListActivityOpts)
	if err != nil {
		return ActivityProjection{}, err
	}

	directOpts := opts.ListActivityOpts
	directOpts.DirectProjectionOnly = true
	directRows, err := listActivityWithQueryer(ctx, tx, directOpts)
	if err != nil {
		return ActivityProjection{}, err
	}

	subjectLimit := opts.SubjectLimit
	if subjectLimit <= 0 {
		subjectLimit = opts.Limit
	}
	subjects, err := listActivitySubjectsWithQueryer(ctx, tx, ActivitySubjectsOpts(opts.ListActivityOpts, opts.SubjectBefore, subjectLimit))
	if err != nil {
		return ActivityProjection{}, err
	}
	if err := tx.Commit(); err != nil {
		return ActivityProjection{}, fmt.Errorf("commit collapsed activity projection: %w", err)
	}
	return ActivityProjection{
		DirectRows:  directRows,
		Subjects:    subjects,
		EventCursor: eventCursor,
	}, nil
}

func activityTypesExcludeNotification(types []string) bool {
	return len(types) > 0 && !slices.Contains(types, "notification")
}

// ActivitySubjectsOpts builds the parent-level filters for one subjects read
// of newest-first subjects from before.
func ActivitySubjectsOpts(
	opts ListActivityOpts, before *ActivitySubjectCursor, limit int,
) ListActivitySubjectsOpts {
	searchOpts := opts
	searchOpts.AfterTime, searchOpts.BeforeTime, searchOpts.AtOrBeforeTime = nil, nil, nil
	return ListActivitySubjectsOpts{
		Repo:           opts.Repo,
		RepoFilters:    opts.RepoFilters,
		AllowedRepoIDs: opts.AllowedRepoIDs,
		ItemTypes:      opts.ItemTypes,
		ExcludeNotificationRecency: opts.ExcludeNotifications ||
			activityTypesExcludeNotification(opts.Types),
		Search:              opts.Search,
		SearchEventOpts:     &searchOpts,
		Author:              opts.Author,
		Unassigned:          opts.Unassigned,
		Assigned:            opts.Assigned,
		ViewerLogins:        opts.ViewerLogins,
		ViewerUninvolved:    opts.ViewerUninvolved,
		HideClosedMerged:    opts.HideClosedMerged,
		OnlyClosedMerged:    opts.OnlyClosedMerged,
		HideBots:            opts.HideBots,
		OnlyBots:            opts.OnlyBots,
		HideDrafts:          opts.HideDrafts,
		HideMergeQueued:     opts.HideMergeQueued,
		OnlyMergeQueued:     opts.OnlyMergeQueued,
		PullAttributes:      opts.PullAttributes,
		ActivityItemFilters: opts.ActivityItemFilters,
		DeferredMergeItems:  opts.DeferredMergeItems,
		WorkspaceItems:      opts.WorkspaceItems,
		Limit:               limit,
		Since:               opts.Since,
		Before:              before,
	}
}
