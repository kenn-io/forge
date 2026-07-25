package db

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

func (d *DB) ReplaceMRReviewHydrationStage(
	ctx context.Context,
	key MRReviewHydrationStageKey,
	reviewIDs []string,
) (*MRReviewHydrationStage, error) {
	reviewIDsJSON, err := json.Marshal(reviewIDs)
	if err != nil {
		return nil, fmt.Errorf("encode review hydration ids: %w", err)
	}
	var stage *MRReviewHydrationStage
	err = d.Tx(ctx, func(tx *sql.Tx) error {
		var generation int64
		err := tx.QueryRowContext(ctx, `
			SELECT generation
			FROM middleman_mr_review_hydration_stages
			WHERE merge_request_id = ?`, key.MergeRequestID).Scan(&generation)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("read review hydration generation: %w", err)
		}
		generation++
		if _, err := tx.ExecContext(ctx, `
			DELETE FROM middleman_mr_review_hydration_stages
			WHERE merge_request_id = ?`, key.MergeRequestID); err != nil {
			return fmt.Errorf("replace review hydration stage: %w", err)
		}
		now := time.Now().UTC().Format(time.RFC3339Nano)
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO middleman_mr_review_hydration_stages (
				merge_request_id, provider_updated_at, head_sha, generation,
				review_ids_json, next_review_index, created_at, updated_at
			) VALUES (?, ?, ?, ?, ?, 0, ?, ?)`,
			key.MergeRequestID, formatHydrationTime(key.ProviderUpdatedAt), key.HeadSHA,
			generation, string(reviewIDsJSON), now, now,
		); err != nil {
			return fmt.Errorf("insert review hydration stage: %w", err)
		}
		stage = &MRReviewHydrationStage{
			MRReviewHydrationStageKey: key,
			Generation:                generation,
			ReviewIDs:                 append([]string(nil), reviewIDs...),
		}
		return nil
	})
	return stage, err
}

func (d *DB) GetMRReviewHydrationStage(
	ctx context.Context,
	mergeRequestID int64,
) (*MRReviewHydrationStage, error) {
	stage, err := scanMRReviewHydrationStage(d.ro.QueryRowContext(ctx, `
		SELECT merge_request_id, provider_updated_at, head_sha, generation,
			review_ids_json, next_review_index
		FROM middleman_mr_review_hydration_stages
		WHERE merge_request_id = ?`, mergeRequestID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	threads, err := listMRReviewHydrationThreads(
		ctx, d.ro, stage.MergeRequestID, stage.Generation,
	)
	if err != nil {
		return nil, err
	}
	stage.Threads = threads
	return &stage, nil
}

func (d *DB) AppendMRReviewHydrationStage(
	ctx context.Context,
	stage MRReviewHydrationStage,
	threads []MRReviewHydrationThread,
	nextReviewIndex int,
) (bool, error) {
	applied := false
	err := d.Tx(ctx, func(tx *sql.Tx) error {
		current, err := scanMRReviewHydrationStage(tx.QueryRowContext(ctx, `
			SELECT merge_request_id, provider_updated_at, head_sha, generation,
				review_ids_json, next_review_index
			FROM middleman_mr_review_hydration_stages
			WHERE merge_request_id = ?`, stage.MergeRequestID))
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if !sameMRReviewHydrationStage(current, stage) ||
			current.NextReviewIndex != stage.NextReviewIndex {
			return nil
		}
		for _, thread := range threads {
			if err := upsertMRReviewHydrationThreadTx(ctx, tx, stage, thread); err != nil {
				return err
			}
		}
		result, err := tx.ExecContext(ctx, `
			UPDATE middleman_mr_review_hydration_stages
			SET next_review_index = ?, updated_at = ?
			WHERE merge_request_id = ? AND generation = ?
				AND provider_updated_at = ? AND head_sha = ? AND next_review_index = ?`,
			nextReviewIndex, time.Now().UTC().Format(time.RFC3339Nano),
			stage.MergeRequestID, stage.Generation,
			formatHydrationTime(stage.ProviderUpdatedAt), stage.HeadSHA,
			stage.NextReviewIndex,
		)
		if err != nil {
			return fmt.Errorf("advance review hydration stage: %w", err)
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("read review hydration advance result: %w", err)
		}
		applied = changed == 1
		return nil
	})
	return applied, err
}

func (d *DB) CommitMRReviewHydrationStage(
	ctx context.Context,
	stage MRReviewHydrationStage,
	expectedRevision int64,
) (bool, error) {
	applied := false
	err := d.Tx(ctx, func(tx *sql.Tx) error {
		currentParent, err := domainParentSnapshotCurrentTx(
			ctx, tx, "middleman_merge_requests", stage.MergeRequestID, expectedRevision,
		)
		if err != nil || !currentParent {
			return err
		}
		current, err := scanMRReviewHydrationStage(tx.QueryRowContext(ctx, `
			SELECT merge_request_id, provider_updated_at, head_sha, generation,
				review_ids_json, next_review_index
			FROM middleman_mr_review_hydration_stages
			WHERE merge_request_id = ?`, stage.MergeRequestID))
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if !sameMRReviewHydrationStage(current, stage) ||
			current.NextReviewIndex < len(current.ReviewIDs) {
			return nil
		}
		threads, err := listMRReviewHydrationThreads(
			ctx, tx, current.MergeRequestID, current.Generation,
		)
		if err != nil {
			return err
		}
		liveThreads := make([]MRReviewThread, 0, len(threads))
		threadIDs := make([]string, 0, len(threads))
		events := make([]MREvent, 0, len(threads))
		commentKeys := make([]string, 0, len(threads))
		for _, staged := range threads {
			thread := staged.MRReviewThread
			thread.MergeRequestID = stage.MergeRequestID
			liveThreads = append(liveThreads, thread)
			threadIDs = append(threadIDs, thread.ProviderThreadID)
			externalID := thread.ProviderCommentID
			if externalID == "" {
				externalID = thread.ProviderThreadID
			}
			threadID := thread.ProviderThreadID
			dedupeKey := "review_comment:" + externalID
			commentKeys = append(commentKeys, dedupeKey)
			events = append(events, MREvent{
				MergeRequestID: stage.MergeRequestID, PlatformExternalID: externalID,
				EventType: "review_comment", Author: thread.AuthorLogin, Body: thread.Body,
				CreatedAt: thread.CreatedAt, DedupeKey: dedupeKey,
				DirectURL: staged.DirectURL, ThreadID: &threadID,
			})
		}
		if err := deleteMissingMRReviewThreadsTx(
			ctx, tx, stage.MergeRequestID, threadIDs, commentKeys,
		); err != nil {
			return err
		}
		if err := upsertMRReviewThreadsTx(ctx, tx, stage.MergeRequestID, liveThreads); err != nil {
			return err
		}
		if err := upsertMREventsTx(ctx, tx, events); err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, `
			DELETE FROM middleman_mr_review_hydration_stages
			WHERE merge_request_id = ? AND generation = ?`,
			stage.MergeRequestID, stage.Generation,
		)
		if err != nil {
			return fmt.Errorf("delete completed review hydration stage: %w", err)
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("read completed review hydration result: %w", err)
		}
		applied = changed == 1
		return nil
	})
	return applied, err
}

func scanMRReviewHydrationStage(row scanner) (MRReviewHydrationStage, error) {
	var stage MRReviewHydrationStage
	var providerUpdatedAt, reviewIDsJSON string
	if err := row.Scan(
		&stage.MergeRequestID, &providerUpdatedAt, &stage.HeadSHA,
		&stage.Generation, &reviewIDsJSON, &stage.NextReviewIndex,
	); err != nil {
		return MRReviewHydrationStage{}, err
	}
	updatedAt, err := parseDBTime(providerUpdatedAt)
	if err != nil {
		return MRReviewHydrationStage{}, fmt.Errorf("parse review hydration provider time: %w", err)
	}
	stage.ProviderUpdatedAt = updatedAt
	if err := json.Unmarshal([]byte(reviewIDsJSON), &stage.ReviewIDs); err != nil {
		return MRReviewHydrationStage{}, fmt.Errorf("decode review hydration ids: %w", err)
	}
	return stage, nil
}

func sameMRReviewHydrationStage(left, right MRReviewHydrationStage) bool {
	return left.MergeRequestID == right.MergeRequestID &&
		left.Generation == right.Generation &&
		left.HeadSHA == right.HeadSHA &&
		left.ProviderUpdatedAt.Equal(right.ProviderUpdatedAt)
}

func upsertMRReviewHydrationThreadTx(
	ctx context.Context,
	tx *sql.Tx,
	stage MRReviewHydrationStage,
	thread MRReviewHydrationThread,
) error {
	providerThreadID := thread.ProviderThreadID
	if providerThreadID == "" {
		providerThreadID = thread.ProviderCommentID
	}
	if providerThreadID == "" {
		return fmt.Errorf("stage mr review thread: provider thread id is empty")
	}
	_, err := tx.ExecContext(ctx, `
		INSERT INTO middleman_mr_review_hydration_threads (
			merge_request_id, generation, provider_thread_id, provider_review_id,
			provider_comment_id, path, old_path, side, start_side, start_line,
			line, old_line, new_line, line_type, diff_head_sha, commit_sha,
			body, author_login, direct_url, resolved, created_at, updated_at,
			resolved_at, metadata_json
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(merge_request_id, generation, provider_thread_id) DO UPDATE SET
			provider_review_id = excluded.provider_review_id,
			provider_comment_id = excluded.provider_comment_id,
			path = excluded.path,
			old_path = excluded.old_path,
			side = excluded.side,
			start_side = excluded.start_side,
			start_line = excluded.start_line,
			line = excluded.line,
			old_line = excluded.old_line,
			new_line = excluded.new_line,
			line_type = excluded.line_type,
			diff_head_sha = excluded.diff_head_sha,
			commit_sha = excluded.commit_sha,
			body = excluded.body,
			author_login = excluded.author_login,
			direct_url = excluded.direct_url,
			resolved = excluded.resolved,
			created_at = excluded.created_at,
			updated_at = excluded.updated_at,
			resolved_at = excluded.resolved_at,
			metadata_json = excluded.metadata_json`,
		stage.MergeRequestID, stage.Generation, providerThreadID,
		nullString(thread.ProviderReviewID), nullString(thread.ProviderCommentID),
		thread.Range.Path, nullString(thread.Range.OldPath), thread.Range.Side,
		nullString(thread.Range.StartSide), nullInt(thread.Range.StartLine),
		thread.Range.Line, nullInt(thread.Range.OldLine), nullInt(thread.Range.NewLine),
		thread.Range.LineType, thread.Range.DiffHeadSHA, thread.Range.CommitSHA,
		thread.Body, nullString(thread.AuthorLogin), thread.DirectURL, thread.Resolved,
		thread.CreatedAt.UTC().Format(time.RFC3339Nano),
		thread.UpdatedAt.UTC().Format(time.RFC3339Nano),
		nullableReviewTime(thread.ResolvedAt), nullString(thread.MetadataJSON),
	)
	if err != nil {
		return fmt.Errorf("stage mr review thread %s: %w", providerThreadID, err)
	}
	return nil
}

func listMRReviewHydrationThreads(
	ctx context.Context,
	q queryer,
	mergeRequestID int64,
	generation int64,
) ([]MRReviewHydrationThread, error) {
	rows, err := q.QueryContext(ctx, `
		SELECT provider_thread_id, provider_review_id, provider_comment_id,
			path, old_path, side, start_side, start_line, line, old_line, new_line,
			line_type, diff_head_sha, commit_sha, body, author_login, direct_url,
			resolved, created_at, updated_at, resolved_at, metadata_json
		FROM middleman_mr_review_hydration_threads
		WHERE merge_request_id = ? AND generation = ?
		ORDER BY created_at, id`, mergeRequestID, generation)
	if err != nil {
		return nil, fmt.Errorf("list staged mr review threads: %w", err)
	}
	defer rows.Close()
	var threads []MRReviewHydrationThread
	for rows.Next() {
		thread, err := scanMRReviewHydrationThread(rows)
		if err != nil {
			return nil, err
		}
		threads = append(threads, thread)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate staged mr review threads: %w", err)
	}
	return threads, nil
}

func scanMRReviewHydrationThread(row scanner) (MRReviewHydrationThread, error) {
	var thread MRReviewHydrationThread
	var providerReviewID, providerCommentID, oldPath, startSide sql.NullString
	var startLine, oldLine, newLine sql.NullInt64
	var authorLogin, resolvedAt, metadataJSON sql.NullString
	var createdAt, updatedAt string
	if err := row.Scan(
		&thread.ProviderThreadID, &providerReviewID, &providerCommentID,
		&thread.Range.Path, &oldPath, &thread.Range.Side, &startSide, &startLine,
		&thread.Range.Line, &oldLine, &newLine, &thread.Range.LineType,
		&thread.Range.DiffHeadSHA, &thread.Range.CommitSHA, &thread.Body,
		&authorLogin, &thread.DirectURL, &thread.Resolved, &createdAt, &updatedAt,
		&resolvedAt, &metadataJSON,
	); err != nil {
		return MRReviewHydrationThread{}, fmt.Errorf("scan staged mr review thread: %w", err)
	}
	thread.ProviderReviewID = providerReviewID.String
	thread.ProviderCommentID = providerCommentID.String
	thread.Range.OldPath = oldPath.String
	thread.Range.StartSide = startSide.String
	thread.Range.StartLine = intPtr(startLine)
	thread.Range.OldLine = intPtr(oldLine)
	thread.Range.NewLine = intPtr(newLine)
	thread.AuthorLogin = authorLogin.String
	thread.MetadataJSON = metadataJSON.String
	var err error
	thread.CreatedAt, err = parseDBTime(createdAt)
	if err != nil {
		return MRReviewHydrationThread{}, fmt.Errorf("parse staged review thread created_at: %w", err)
	}
	thread.UpdatedAt, err = parseDBTime(updatedAt)
	if err != nil {
		return MRReviewHydrationThread{}, fmt.Errorf("parse staged review thread updated_at: %w", err)
	}
	thread.ResolvedAt, err = parseNullableTime(resolvedAt)
	if err != nil {
		return MRReviewHydrationThread{}, fmt.Errorf("parse staged review thread resolved_at: %w", err)
	}
	return thread, nil
}

func formatHydrationTime(value time.Time) string {
	return value.UTC().Format(time.RFC3339Nano)
}
