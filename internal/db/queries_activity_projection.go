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

	highWaterOpts := opts.ListActivityOpts
	highWaterOpts.Limit = 1
	highWaterRows, err := listActivityWithQueryer(ctx, tx, highWaterOpts)
	if err != nil {
		return ActivityProjection{}, err
	}
	eventCursor := ""
	if len(highWaterRows) > 0 {
		row := highWaterRows[0]
		eventCursor = EncodeCursor(row.CreatedAt, row.Source, row.SourceID)
	}

	directOpts := opts.ListActivityOpts
	directOpts.DirectProjectionOnly = true
	directRows, err := listActivityWithQueryer(ctx, tx, directOpts)
	if err != nil {
		return ActivityProjection{}, err
	}

	var searchMatched []WorkspaceSubjectKey
	if opts.Search != "" {
		searchRows, searchErr := listActivityWithQueryer(ctx, tx, opts.ListActivityOpts)
		if searchErr != nil {
			return ActivityProjection{}, searchErr
		}
		searchMatched = make([]WorkspaceSubjectKey, 0, len(searchRows))
		for _, row := range searchRows {
			if row.ItemType == "pr" || row.ItemType == "issue" {
				searchMatched = append(searchMatched, WorkspaceSubjectKey{
					RepoID: row.RepoID, ItemType: row.ItemType, ItemNumber: row.ItemNumber,
				})
			}
		}
	}

	subjectLimit := opts.SubjectLimit
	if subjectLimit <= 0 {
		subjectLimit = opts.Limit
	}
	subjects, err := listActivitySubjectsWithQueryer(ctx, tx, ListActivitySubjectsOpts{
		Repo:           opts.Repo,
		RepoFilters:    opts.RepoFilters,
		AllowedRepoIDs: opts.AllowedRepoIDs,
		ItemTypes:      opts.ItemTypes,
		ExcludeNotificationRecency: opts.ExcludeNotifications ||
			activityTypesExcludeNotification(opts.Types),
		Search:                   opts.Search,
		SearchMatchedSubjectKeys: searchMatched,
		Author:                   opts.Author,
		ViewerLogins:             opts.ViewerLogins,
		HideClosedMerged:         opts.HideClosedMerged,
		HideBots:                 opts.HideBots,
		Limit:                    subjectLimit,
		Since:                    opts.Since,
	})
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
