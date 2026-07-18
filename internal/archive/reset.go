package archive

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"go.kenn.io/middleman/internal/db"
	"go.kenn.io/middleman/internal/platform"
)

type ResetMode string

const (
	ResetModeRestart  ResetMode = "restart"
	ResetModeContinue ResetMode = "continue"
)

type ResetScope struct {
	Scan       *db.ArchiveScanKind
	ItemType   *db.ArchiveItemType
	ItemNumber *int
	Dataset    *db.ArchiveDataset
	Mode       ResetMode
	Force      bool
}

type ResetErrorReason string

const (
	ResetErrorInvalidScope                 ResetErrorReason = "invalid_scope"
	ResetErrorMissingTarget                ResetErrorReason = "missing_target"
	ResetErrorNotBlocked                   ResetErrorReason = "not_blocked"
	ResetErrorInvalidCursorRequiresRestart ResetErrorReason = "invalid_cursor_requires_restart"
	ResetErrorRestartRequired              ResetErrorReason = "restart_required"
)

type ResetError struct {
	Scope  string
	Reason ResetErrorReason
}

func (e *ResetError) Error() string { return fmt.Sprintf("cannot reset %s: %s", e.Scope, e.Reason) }

func (s *Service) ResetScan(ctx context.Context, ref platform.RepoRef, scope ResetScope) error {
	resolved, err := s.resolveRepositories(ctx, []platform.RepoRef{ref}, false)
	if err != nil {
		return err
	}
	if len(resolved) != 1 {
		return &ResetError{Scope: "archive progress", Reason: ResetErrorMissingTarget}
	}
	continueMode, err := validateResetScope(scope)
	if err != nil {
		return err
	}
	repoID := resolved[0].ID
	if scope.Scan != nil {
		states, stateErr := s.db.ListArchiveRepoStates(ctx, []int64{repoID})
		if stateErr != nil {
			return stateErr
		}
		if len(states) != 1 {
			return &ResetError{Scope: "repository scan", Reason: ResetErrorMissingTarget}
		}
		state := states[0].Scan(*scope.Scan)
		if err := validateResetTarget(
			state.Blocked(), pointerValue(state.LastErrorCode), continueMode, scope.Force,
			fmt.Sprintf("repository scan %s", *scope.Scan),
		); err != nil {
			return err
		}
		if continueMode {
			err = s.db.ContinueArchiveRepoScan(ctx, repoID, *scope.Scan)
		} else {
			err = s.db.ResetArchiveRepoScan(ctx, repoID, *scope.Scan)
		}
	} else {
		key := db.ArchiveDatasetProgressKey{
			RepoID: repoID, ItemType: *scope.ItemType,
			ItemNumber: *scope.ItemNumber, Dataset: *scope.Dataset,
		}
		progress, progressErr := s.db.GetDatasetProgress(
			ctx, repoID, key.ItemType, key.ItemNumber, key.Dataset,
		)
		if errors.Is(progressErr, sql.ErrNoRows) {
			return &ResetError{Scope: "item dataset", Reason: ResetErrorMissingTarget}
		}
		if progressErr != nil {
			return progressErr
		}
		if err := validateResetTarget(
			progress.Status == db.ArchiveDatasetProgressBlocked,
			pointerValue(progress.LastErrorCode), continueMode, scope.Force,
			fmt.Sprintf("%s %d dataset %s", key.ItemType, key.ItemNumber, key.Dataset),
		); err != nil {
			return err
		}
		if continueMode {
			err = s.db.ContinueDatasetProgress(ctx, key)
		} else {
			err = s.db.ResetDatasetProgress(ctx, key)
		}
	}
	if err != nil {
		return err
	}
	if s.wake != nil {
		s.wake()
	}
	return nil
}

func validateResetTarget(blocked bool, code string, continueMode, force bool, scope string) error {
	if continueMode && blocked {
		switch code {
		case "page_bound":
		case "invalid_cursor":
			return &ResetError{Scope: scope, Reason: ResetErrorInvalidCursorRequiresRestart}
		default:
			return &ResetError{Scope: scope, Reason: ResetErrorRestartRequired}
		}
	}
	if !force && !blocked {
		return &ResetError{Scope: scope, Reason: ResetErrorNotBlocked}
	}
	return nil
}

func pointerValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func validateResetScope(scope ResetScope) (bool, error) {
	continueMode := false
	switch scope.Mode {
	case ResetModeRestart:
	case ResetModeContinue:
		continueMode = true
	default:
		return false, &ResetError{
			Scope: "archive progress", Reason: ResetErrorInvalidScope,
		}
	}
	hasScan := scope.Scan != nil
	hasAnyItem := scope.ItemType != nil || scope.ItemNumber != nil || scope.Dataset != nil
	hasCompleteItem := scope.ItemType != nil && scope.ItemNumber != nil && scope.Dataset != nil
	if hasScan == hasAnyItem || (hasAnyItem && !hasCompleteItem) {
		return false, &ResetError{
			Scope: "archive progress", Reason: ResetErrorInvalidScope,
		}
	}
	if hasScan {
		switch *scope.Scan {
		case db.ArchiveScanIssueInventory, db.ArchiveScanMergeRequestInventory,
			db.ArchiveScanMaintenanceIssues, db.ArchiveScanMaintenanceMergeRequests:
		default:
			return false, &ResetError{Scope: "repository scan", Reason: ResetErrorInvalidScope}
		}
	}
	if hasCompleteItem && *scope.ItemNumber <= 0 {
		return false, &ResetError{
			Scope: fmt.Sprintf("%s item", *scope.ItemType), Reason: ResetErrorInvalidScope,
		}
	}
	if hasCompleteItem {
		if *scope.ItemType != db.ArchiveItemTypeIssue && *scope.ItemType != db.ArchiveItemTypeMergeRequest {
			return false, &ResetError{Scope: "item dataset", Reason: ResetErrorInvalidScope}
		}
		switch *scope.Dataset {
		case db.ArchiveDatasetLookup, db.ArchiveDatasetComments,
			db.ArchiveDatasetReviews, db.ArchiveDatasetInlineComments:
		default:
			return false, &ResetError{Scope: "item dataset", Reason: ResetErrorInvalidScope}
		}
	}
	return continueMode, nil
}
