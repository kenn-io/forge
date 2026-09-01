package db

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const (
	ProviderStateReviewDraft   = "review_draft"
	ProviderStateWorkflowState = "workflow_state"
)

type ProviderStateRepository struct {
	Provider       string `json:"provider"`
	PlatformHost   string `json:"platform_host"`
	PlatformRepoID string `json:"platform_repo_id"`
	Owner          string `json:"owner"`
	Name           string `json:"name"`
}

type ProviderStateReviewComment struct {
	Body        string `json:"body"`
	Path        string `json:"path"`
	OldPath     string `json:"old_path,omitempty"`
	Side        string `json:"side"`
	StartSide   string `json:"start_side,omitempty"`
	StartLine   *int   `json:"start_line,omitempty"`
	Line        int    `json:"line"`
	OldLine     *int   `json:"old_line,omitempty"`
	NewLine     *int   `json:"new_line,omitempty"`
	LineType    string `json:"line_type"`
	DiffHeadSHA string `json:"diff_head_sha"`
	CommitSHA   string `json:"commit_sha"`
}

type ProviderStateReviewDraftPayload struct {
	Repository ProviderStateRepository      `json:"repository"`
	PullNumber int                          `json:"pull_number"`
	Body       string                       `json:"body"`
	Action     string                       `json:"action"`
	Comments   []ProviderStateReviewComment `json:"comments" nullable:"false"`
}

type ProviderStateWorkflowPayload struct {
	Repository    ProviderStateRepository `json:"repository"`
	ItemType      string                  `json:"item_type" enum:"pr,issue"`
	ItemNumber    int                     `json:"item_number"`
	Status        string                  `json:"status"`
	UpdatedSource string                  `json:"updated_source,omitempty"`
	UpdatedActor  string                  `json:"updated_actor,omitempty"`
	UpdatedReason string                  `json:"updated_reason,omitempty"`
}

type ProviderStateRecord struct {
	Kind          string                           `json:"kind" enum:"review_draft,workflow_state"`
	SourceKey     string                           `json:"source_key"`
	ContentDigest string                           `json:"content_digest"`
	ReviewDraft   *ProviderStateReviewDraftPayload `json:"review_draft,omitempty"`
	WorkflowState *ProviderStateWorkflowPayload    `json:"workflow_state,omitempty"`
}

type ProviderStateConflict struct {
	Kind         string `json:"kind"`
	SourceKey    string `json:"source_key"`
	SourceDigest string `json:"source_digest"`
	TargetDigest string `json:"target_digest"`
}

type ProviderStateImportResult struct {
	Receipt  string                 `json:"receipt,omitempty"`
	Imported bool                   `json:"imported"`
	Conflict *ProviderStateConflict `json:"conflict,omitempty"`
}

func (repository ProviderStateRepository) validate() error {
	fields := []struct {
		name  string
		value string
	}{
		{name: "provider", value: repository.Provider},
		{name: "platform_host", value: repository.PlatformHost},
		{name: "platform_repo_id", value: repository.PlatformRepoID},
		{name: "owner", value: repository.Owner},
		{name: "name", value: repository.Name},
	}
	for _, field := range fields {
		if strings.TrimSpace(field.value) == "" {
			return fmt.Errorf("provider state repository %s is required", field.name)
		}
	}
	return nil
}

func (payload ProviderStateReviewDraftPayload) Validate() error {
	if err := payload.Repository.validate(); err != nil {
		return err
	}
	if payload.PullNumber <= 0 {
		return errors.New("provider state pull number must be positive")
	}
	if strings.TrimSpace(payload.Action) == "" {
		return errors.New("provider state review action is required")
	}
	if payload.Comments == nil {
		return errors.New("provider state review comments must be present")
	}
	for index, comment := range payload.Comments {
		if strings.TrimSpace(comment.Body) == "" ||
			strings.TrimSpace(comment.Path) == "" ||
			comment.Line <= 0 || strings.TrimSpace(comment.DiffHeadSHA) == "" {
			return fmt.Errorf("provider state review comment %d is incomplete", index)
		}
		side := strings.ToLower(strings.TrimSpace(comment.Side))
		if side != "left" && side != "right" {
			return fmt.Errorf("provider state review comment %d side must be left or right", index)
		}
		switch strings.TrimSpace(comment.LineType) {
		case "context", "add", "delete":
		default:
			return fmt.Errorf("provider state review comment %d line type is invalid", index)
		}
		startSide := strings.ToLower(strings.TrimSpace(comment.StartSide))
		if comment.StartLine != nil && *comment.StartLine <= 0 {
			return fmt.Errorf("provider state review comment %d start line must be positive", index)
		}
		if (startSide == "") != (comment.StartLine == nil) {
			return fmt.Errorf("provider state review comment %d start side and line must be supplied together", index)
		}
		if startSide != "" && startSide != side {
			return fmt.Errorf("provider state review comment %d must stay on one side", index)
		}
		if comment.StartLine != nil && *comment.StartLine > comment.Line {
			return fmt.Errorf("provider state review comment %d start line must be before line", index)
		}
	}
	return nil
}

func (payload ProviderStateWorkflowPayload) Validate() error {
	if err := payload.Repository.validate(); err != nil {
		return err
	}
	if payload.ItemNumber <= 0 {
		return errors.New("provider state workflow item number must be positive")
	}
	if err := validateItemWorkflowType(payload.ItemType); err != nil {
		return err
	}
	return validateItemWorkflowStatus(payload.Status)
}

func providerStateCanonicalDigest(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("encode canonical provider state: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func providerStateRepositoryKey(repository ProviderStateRepository) string {
	return strings.ToLower(strings.TrimSpace(repository.Provider)) + "\x00" +
		strings.ToLower(strings.TrimSpace(repository.PlatformHost)) + "\x00" +
		strings.TrimSpace(repository.PlatformRepoID)
}

func canonicalProviderStateDigestRepository(repository ProviderStateRepository) ProviderStateRepository {
	return ProviderStateRepository{
		Provider:       strings.ToLower(strings.TrimSpace(repository.Provider)),
		PlatformHost:   strings.ToLower(strings.TrimSpace(repository.PlatformHost)),
		PlatformRepoID: strings.TrimSpace(repository.PlatformRepoID),
	}
}

func (payload ProviderStateReviewDraftPayload) Record() (ProviderStateRecord, error) {
	if err := payload.Validate(); err != nil {
		return ProviderStateRecord{}, err
	}
	canonical := payload
	canonical.Repository = canonicalProviderStateDigestRepository(payload.Repository)
	digest, err := providerStateCanonicalDigest(canonical)
	if err != nil {
		return ProviderStateRecord{}, err
	}
	copy := payload
	return ProviderStateRecord{
		Kind:          ProviderStateReviewDraft,
		SourceKey:     providerStateRepositoryKey(payload.Repository) + "\x00pr\x00" + strconv.Itoa(payload.PullNumber),
		ContentDigest: digest, ReviewDraft: &copy,
	}, nil
}

func (payload ProviderStateWorkflowPayload) Record() (ProviderStateRecord, error) {
	if err := payload.Validate(); err != nil {
		return ProviderStateRecord{}, err
	}
	canonical := payload
	canonical.Repository = canonicalProviderStateDigestRepository(payload.Repository)
	digest, err := providerStateCanonicalDigest(canonical)
	if err != nil {
		return ProviderStateRecord{}, err
	}
	copy := payload
	return ProviderStateRecord{
		Kind:          ProviderStateWorkflowState,
		SourceKey:     providerStateRepositoryKey(payload.Repository) + "\x00" + payload.ItemType + "\x00" + strconv.Itoa(payload.ItemNumber),
		ContentDigest: digest, WorkflowState: &copy,
	}, nil
}

func providerStateRepositoryFromRow(
	provider, host, platformRepoID, owner, name string,
) ProviderStateRepository {
	return ProviderStateRepository{
		Provider: provider, PlatformHost: host, PlatformRepoID: platformRepoID,
		Owner: owner, Name: name,
	}
}

func (d *DB) ListProviderStateForHandoff(
	ctx context.Context,
) ([]ProviderStateRecord, error) {
	records, err := d.listReviewDraftStateForHandoff(ctx)
	if err != nil {
		return nil, err
	}
	workflows, err := d.listWorkflowStateForHandoff(ctx)
	if err != nil {
		return nil, err
	}
	records = append(records, workflows...)
	return records, nil
}

func (d *DB) listReviewDraftStateForHandoff(
	ctx context.Context,
) ([]ProviderStateRecord, error) {
	rows, err := d.ro.QueryContext(ctx, `
		SELECT draft.id, r.platform, r.platform_host, r.platform_repo_id,
		       r.owner, r.name, mr.number, draft.body, draft.action
		FROM forge_mr_review_drafts draft
		JOIN forge_merge_requests mr ON mr.id = draft.merge_request_id
		JOIN forge_repos r ON r.id = mr.repo_id
		WHERE trim(r.platform_repo_id) <> ''
		ORDER BY r.platform, r.platform_host, r.platform_repo_id, mr.number`)
	if err != nil {
		return nil, fmt.Errorf("list review drafts for provider state handoff: %w", err)
	}
	defer rows.Close()
	var records []ProviderStateRecord
	for rows.Next() {
		var draftID int64
		var provider, host, repoID, owner, name string
		var payload ProviderStateReviewDraftPayload
		if err := rows.Scan(
			&draftID, &provider, &host, &repoID, &owner, &name,
			&payload.PullNumber, &payload.Body, &payload.Action,
		); err != nil {
			return nil, err
		}
		payload.Repository = providerStateRepositoryFromRow(provider, host, repoID, owner, name)
		comments, err := d.ListMRReviewDraftComments(ctx, draftID)
		if err != nil {
			return nil, err
		}
		payload.Comments = make([]ProviderStateReviewComment, 0, len(comments))
		for _, comment := range comments {
			payload.Comments = append(payload.Comments, ProviderStateReviewComment{
				Body: comment.Body, Path: comment.Range.Path, OldPath: comment.Range.OldPath,
				Side: comment.Range.Side, StartSide: comment.Range.StartSide,
				StartLine: comment.Range.StartLine, Line: comment.Range.Line,
				OldLine: comment.Range.OldLine, NewLine: comment.Range.NewLine,
				LineType: comment.Range.LineType, DiffHeadSHA: comment.Range.DiffHeadSHA,
				CommitSHA: comment.Range.CommitSHA,
			})
		}
		record, err := payload.Record()
		if err != nil {
			return nil, fmt.Errorf("canonicalize review draft for provider state handoff: %w", err)
		}
		records = append(records, record)
	}
	return records, rows.Err()
}

func (d *DB) listWorkflowStateForHandoff(
	ctx context.Context,
) ([]ProviderStateRecord, error) {
	rows, err := d.ro.QueryContext(ctx, `
		SELECT r.platform, r.platform_host, r.platform_repo_id,
		       r.owner, r.name, state.item_type, state.item_number,
		       state.status, state.updated_source, state.updated_actor,
		       state.updated_reason
		FROM forge_item_workflow_state state
		JOIN forge_repos r ON r.id = state.repo_id
		WHERE trim(r.platform_repo_id) <> ''
		  AND (state.status <> 'new' OR trim(state.updated_source) <> ''
		       OR trim(state.updated_actor) <> '' OR trim(state.updated_reason) <> '')
		ORDER BY r.platform, r.platform_host, r.platform_repo_id,
		         state.item_type, state.item_number`)
	if err != nil {
		return nil, fmt.Errorf("list workflow state for provider state handoff: %w", err)
	}
	defer rows.Close()
	var records []ProviderStateRecord
	for rows.Next() {
		var provider, host, repoID, owner, name string
		var payload ProviderStateWorkflowPayload
		if err := rows.Scan(
			&provider, &host, &repoID, &owner, &name,
			&payload.ItemType, &payload.ItemNumber, &payload.Status,
			&payload.UpdatedSource, &payload.UpdatedActor, &payload.UpdatedReason,
		); err != nil {
			return nil, err
		}
		payload.Repository = providerStateRepositoryFromRow(provider, host, repoID, owner, name)
		record, err := payload.Record()
		if err != nil {
			return nil, fmt.Errorf("canonicalize workflow state for provider state handoff: %w", err)
		}
		records = append(records, record)
	}
	return records, rows.Err()
}

func providerStateReceipt(kind, sourceKey, digest string) string {
	value := sha256.Sum256([]byte("forge-provider-state-receipt-v1\x00" + kind + "\x00" + sourceKey + "\x00" + digest))
	return hex.EncodeToString(value[:])
}

func providerStateConflictResult(record ProviderStateRecord, targetDigest string) ProviderStateImportResult {
	return ProviderStateImportResult{Conflict: &ProviderStateConflict{
		Kind: record.Kind, SourceKey: record.SourceKey,
		SourceDigest: record.ContentDigest, TargetDigest: targetDigest,
	}}
}

func lookupProviderStateRepoTx(
	ctx context.Context,
	tx *sql.Tx,
	repository ProviderStateRepository,
) (int64, error) {
	var repoID int64
	err := tx.QueryRowContext(ctx, `
		SELECT id FROM forge_repos
		WHERE platform = ? AND platform_host = ? AND platform_repo_id = ?
		  AND lifecycle_state = 'active'`,
		strings.ToLower(strings.TrimSpace(repository.Provider)),
		strings.ToLower(strings.TrimSpace(repository.PlatformHost)),
		strings.TrimSpace(repository.PlatformRepoID),
	).Scan(&repoID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, errors.New("provider state repository is not present on the hub")
	}
	return repoID, err
}

func (d *DB) ImportProviderReviewDraft(
	ctx context.Context,
	payload ProviderStateReviewDraftPayload,
) (ProviderStateImportResult, error) {
	record, err := payload.Record()
	if err != nil {
		return ProviderStateImportResult{}, err
	}
	var result ProviderStateImportResult
	err = d.Tx(ctx, func(tx *sql.Tx) error {
		repoID, err := lookupProviderStateRepoTx(ctx, tx, payload.Repository)
		if err != nil {
			return err
		}
		var mergeRequestID int64
		if err := tx.QueryRowContext(ctx, `
			SELECT id FROM forge_merge_requests
			WHERE repo_id = ? AND number = ?`, repoID, payload.PullNumber,
		).Scan(&mergeRequestID); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return errors.New("provider state pull request is not present on the hub")
			}
			return err
		}
		target, exists, err := readProviderReviewDraftTx(ctx, tx, payload.Repository, payload.PullNumber, mergeRequestID)
		if err != nil {
			return err
		}
		if exists {
			targetRecord, err := target.Record()
			if err != nil {
				return err
			}
			if targetRecord.ContentDigest != record.ContentDigest {
				result = providerStateConflictResult(record, targetRecord.ContentDigest)
				return nil
			}
			result.Receipt = providerStateReceipt(record.Kind, record.SourceKey, record.ContentDigest)
			return nil
		}
		now := time.Now().UTC().Format(time.RFC3339Nano)
		created, err := tx.ExecContext(ctx, `
			INSERT INTO forge_mr_review_drafts (
				merge_request_id, body, action, created_at, updated_at
			) VALUES (?, ?, ?, ?, ?)`,
			mergeRequestID, payload.Body, payload.Action, now, now,
		)
		if err != nil {
			return err
		}
		draftID, err := created.LastInsertId()
		if err != nil {
			return err
		}
		for _, comment := range payload.Comments {
			_, err := tx.ExecContext(ctx, `
				INSERT INTO forge_mr_review_draft_comments (
					draft_id, body, path, old_path, side, start_side,
					start_line, line, old_line, new_line, line_type,
					diff_head_sha, commit_sha, created_at, updated_at
				) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				draftID, comment.Body, comment.Path, nullString(comment.OldPath),
				comment.Side, nullString(comment.StartSide), nullInt(comment.StartLine),
				comment.Line, nullInt(comment.OldLine), nullInt(comment.NewLine),
				comment.LineType, comment.DiffHeadSHA, comment.CommitSHA, now, now,
			)
			if err != nil {
				return err
			}
		}
		result.Imported = true
		result.Receipt = providerStateReceipt(record.Kind, record.SourceKey, record.ContentDigest)
		return nil
	})
	if err != nil {
		return ProviderStateImportResult{}, fmt.Errorf("import provider review draft: %w", err)
	}
	return result, nil
}

func readProviderReviewDraftTx(
	ctx context.Context,
	tx *sql.Tx,
	repository ProviderStateRepository,
	pullNumber int,
	mergeRequestID int64,
) (ProviderStateReviewDraftPayload, bool, error) {
	payload := ProviderStateReviewDraftPayload{
		Repository: repository, PullNumber: pullNumber,
	}
	var draftID int64
	err := tx.QueryRowContext(ctx, `
		SELECT id, body, action FROM forge_mr_review_drafts
		WHERE merge_request_id = ?`, mergeRequestID,
	).Scan(&draftID, &payload.Body, &payload.Action)
	if errors.Is(err, sql.ErrNoRows) {
		return ProviderStateReviewDraftPayload{}, false, nil
	}
	if err != nil {
		return ProviderStateReviewDraftPayload{}, false, err
	}
	payload.Comments = []ProviderStateReviewComment{}
	rows, err := tx.QueryContext(ctx, `
		SELECT body, path, old_path, side, start_side, start_line,
		       line, old_line, new_line, line_type, diff_head_sha, commit_sha
		FROM forge_mr_review_draft_comments
		WHERE draft_id = ? ORDER BY id`, draftID)
	if err != nil {
		return ProviderStateReviewDraftPayload{}, false, err
	}
	defer rows.Close()
	for rows.Next() {
		var comment ProviderStateReviewComment
		var oldPath, startSide sql.NullString
		var startLine, oldLine, newLine sql.NullInt64
		if err := rows.Scan(
			&comment.Body, &comment.Path, &oldPath, &comment.Side, &startSide,
			&startLine, &comment.Line, &oldLine, &newLine,
			&comment.LineType, &comment.DiffHeadSHA, &comment.CommitSHA,
		); err != nil {
			return ProviderStateReviewDraftPayload{}, false, err
		}
		comment.OldPath = oldPath.String
		comment.StartSide = startSide.String
		comment.StartLine = intPtr(startLine)
		comment.OldLine = intPtr(oldLine)
		comment.NewLine = intPtr(newLine)
		payload.Comments = append(payload.Comments, comment)
	}
	return payload, true, rows.Err()
}

func (d *DB) ImportProviderWorkflowState(
	ctx context.Context,
	payload ProviderStateWorkflowPayload,
) (ProviderStateImportResult, error) {
	record, err := payload.Record()
	if err != nil {
		return ProviderStateImportResult{}, err
	}
	var result ProviderStateImportResult
	err = d.Tx(ctx, func(tx *sql.Tx) error {
		repoID, err := lookupProviderStateRepoTx(ctx, tx, payload.Repository)
		if err != nil {
			return err
		}
		itemTable := "forge_issues"
		if payload.ItemType == ItemTypePR {
			itemTable = "forge_merge_requests"
		}
		var exists int
		if err := tx.QueryRowContext(ctx,
			"SELECT 1 FROM "+itemTable+" WHERE repo_id = ? AND number = ?",
			repoID, payload.ItemNumber,
		).Scan(&exists); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return errors.New("provider state workflow item is not present on the hub")
			}
			return err
		}
		var target ProviderStateWorkflowPayload
		target.Repository = payload.Repository
		target.ItemType = payload.ItemType
		target.ItemNumber = payload.ItemNumber
		err = tx.QueryRowContext(ctx, `
			SELECT status, updated_source, updated_actor, updated_reason
			FROM forge_item_workflow_state
			WHERE repo_id = ? AND item_type = ? AND item_number = ?`,
			repoID, payload.ItemType, payload.ItemNumber,
		).Scan(&target.Status, &target.UpdatedSource, &target.UpdatedActor, &target.UpdatedReason)
		if err == nil {
			targetRecord, digestErr := target.Record()
			if digestErr != nil {
				return digestErr
			}
			if targetRecord.ContentDigest != record.ContentDigest {
				result = providerStateConflictResult(record, targetRecord.ContentDigest)
				return nil
			}
			result.Receipt = providerStateReceipt(record.Kind, record.SourceKey, record.ContentDigest)
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		_, err = tx.ExecContext(ctx, `
			INSERT INTO forge_item_workflow_state (
				repo_id, item_type, item_number, status, updated_at,
				updated_source, updated_actor, updated_reason
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			repoID, payload.ItemType, payload.ItemNumber, payload.Status,
			time.Now().UTC().Format(time.RFC3339Nano), payload.UpdatedSource,
			payload.UpdatedActor, payload.UpdatedReason,
		)
		if err != nil {
			return err
		}
		result.Imported = true
		result.Receipt = providerStateReceipt(record.Kind, record.SourceKey, record.ContentDigest)
		return nil
	})
	if err != nil {
		return ProviderStateImportResult{}, fmt.Errorf("import provider workflow state: %w", err)
	}
	return result, nil
}
