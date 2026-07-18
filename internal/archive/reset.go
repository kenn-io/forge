package archive

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"go.kenn.io/middleman/internal/db"
	"go.kenn.io/middleman/internal/platform"
)

type ResetScope struct {
	Scan       *db.ArchiveScanKind
	ItemType   *db.ArchiveItemType
	ItemNumber *int
	Dataset    *db.ArchiveDataset
	Force      bool
}

type ResetErrorReason string

const (
	ResetErrorInvalidScope  ResetErrorReason = "invalid_scope"
	ResetErrorMissingTarget ResetErrorReason = "missing_target"
	ResetErrorNotBlocked    ResetErrorReason = "not_blocked"
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
	if err := validateResetScope(scope); err != nil {
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
			state.Blocked(), scope.Force, fmt.Sprintf("repository scan %s", *scope.Scan),
		); err != nil {
			return err
		}
		err = s.db.ResetArchiveRepoScan(ctx, repoID, *scope.Scan)
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
			progress.Status == db.ArchiveDatasetProgressBlocked, scope.Force,
			fmt.Sprintf("%s %d dataset %s", key.ItemType, key.ItemNumber, key.Dataset),
		); err != nil {
			return err
		}
		err = s.db.ResetDatasetProgress(ctx, key)
	}
	if err != nil {
		return err
	}
	if s.wake != nil {
		s.wake()
	}
	return nil
}

func validateResetTarget(blocked, force bool, scope string) error {
	if !force && !blocked {
		return &ResetError{Scope: scope, Reason: ResetErrorNotBlocked}
	}
	return nil
}

func validateResetScope(scope ResetScope) error {
	hasScan := scope.Scan != nil
	hasAnyItem := scope.ItemType != nil || scope.ItemNumber != nil || scope.Dataset != nil
	hasCompleteItem := scope.ItemType != nil && scope.ItemNumber != nil && scope.Dataset != nil
	if hasScan == hasAnyItem || (hasAnyItem && !hasCompleteItem) {
		return &ResetError{
			Scope: "archive progress", Reason: ResetErrorInvalidScope,
		}
	}
	if hasScan {
		switch *scope.Scan {
		case db.ArchiveScanIssueInventory, db.ArchiveScanMergeRequestInventory,
			db.ArchiveScanMaintenanceIssues, db.ArchiveScanMaintenanceMergeRequests:
		default:
			return &ResetError{Scope: "repository scan", Reason: ResetErrorInvalidScope}
		}
	}
	if hasCompleteItem && *scope.ItemNumber <= 0 {
		return &ResetError{
			Scope: fmt.Sprintf("%s item", *scope.ItemType), Reason: ResetErrorInvalidScope,
		}
	}
	if hasCompleteItem {
		if *scope.ItemType != db.ArchiveItemTypeIssue && *scope.ItemType != db.ArchiveItemTypeMergeRequest {
			return &ResetError{Scope: "item dataset", Reason: ResetErrorInvalidScope}
		}
		switch *scope.Dataset {
		case db.ArchiveDatasetLookup, db.ArchiveDatasetComments,
			db.ArchiveDatasetReviews, db.ArchiveDatasetInlineComments:
		default:
			return &ResetError{Scope: "item dataset", Reason: ResetErrorInvalidScope}
		}
	}
	return nil
}
