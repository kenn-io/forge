package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

func nullString(s string) sql.NullString {
	return sql.NullString{String: s, Valid: s != ""}
}

func nullInt(v *int) sql.NullInt64 {
	if v == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: int64(*v), Valid: true}
}

func intPtr(v sql.NullInt64) *int {
	if !v.Valid {
		return nil
	}
	n := int(v.Int64)
	return &n
}

func nullableReviewTime(t *time.Time) sql.NullString {
	if t == nil {
		return sql.NullString{}
	}
	return sql.NullString{String: t.UTC().Format(time.RFC3339), Valid: true}
}

func parseNullableTime(v sql.NullString) (*time.Time, error) {
	if !v.Valid || v.String == "" {
		return nil, nil
	}
	t, err := parseDBTime(v.String)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func (d *DB) GetOrCreateMRReviewDraft(ctx context.Context, mrID int64) (*MRReviewDraft, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := d.rw.ExecContext(ctx, `
		INSERT INTO middleman_mr_review_drafts (merge_request_id, created_at, updated_at)
		VALUES (?, ?, ?)
		ON CONFLICT(merge_request_id) DO NOTHING`,
		mrID, now, now,
	); err != nil {
		return nil, fmt.Errorf("create mr review draft: %w", err)
	}
	return d.GetMRReviewDraft(ctx, mrID)
}

func (d *DB) GetMRReviewDraft(ctx context.Context, mrID int64) (*MRReviewDraft, error) {
	var draft MRReviewDraft
	var createdAt, updatedAt string
	err := d.ro.QueryRowContext(ctx, `
		SELECT id, merge_request_id, body, action, created_at, updated_at
		FROM middleman_mr_review_drafts
		WHERE merge_request_id = ?`,
		mrID,
	).Scan(&draft.ID, &draft.MergeRequestID, &draft.Body, &draft.Action, &createdAt, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get mr review draft: %w", err)
	}
	var parseErr error
	draft.CreatedAt, parseErr = parseDBTime(createdAt)
	if parseErr != nil {
		return nil, fmt.Errorf("parse draft created_at: %w", parseErr)
	}
	draft.UpdatedAt, parseErr = parseDBTime(updatedAt)
	if parseErr != nil {
		return nil, fmt.Errorf("parse draft updated_at: %w", parseErr)
	}
	comments, err := d.ListMRReviewDraftComments(ctx, draft.ID)
	if err != nil {
		return nil, err
	}
	draft.Comments = comments
	return &draft, nil
}

func (d *DB) ListMRReviewDraftComments(ctx context.Context, draftID int64) ([]MRReviewDraftComment, error) {
	rows, err := d.ro.QueryContext(ctx, `
		SELECT id, draft_id, body, path, old_path, side, start_side, start_line,
			line, old_line, new_line, line_type, diff_head_sha, commit_sha,
			created_at, updated_at
		FROM middleman_mr_review_draft_comments
		WHERE draft_id = ?
		ORDER BY id`,
		draftID,
	)
	if err != nil {
		return nil, fmt.Errorf("list mr review draft comments: %w", err)
	}
	defer rows.Close()

	var comments []MRReviewDraftComment
	for rows.Next() {
		comment, err := scanReviewDraftComment(rows)
		if err != nil {
			return nil, err
		}
		comments = append(comments, comment)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate mr review draft comments: %w", err)
	}
	return comments, nil
}

func (d *DB) CreateMRReviewDraftComment(
	ctx context.Context,
	draftID int64,
	input MRReviewDraftCommentInput,
) (*MRReviewDraftComment, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	res, err := d.rw.ExecContext(ctx, `
		INSERT INTO middleman_mr_review_draft_comments (
			draft_id, body, path, old_path, side, start_side, start_line,
			line, old_line, new_line, line_type, diff_head_sha, commit_sha,
			created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		draftID, input.Body, input.Range.Path, nullString(input.Range.OldPath),
		input.Range.Side, nullString(input.Range.StartSide), nullInt(input.Range.StartLine),
		input.Range.Line, nullInt(input.Range.OldLine), nullInt(input.Range.NewLine),
		input.Range.LineType, input.Range.DiffHeadSHA, input.Range.CommitSHA,
		now, now,
	)
	if err != nil {
		return nil, fmt.Errorf("create mr review draft comment: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("get mr review draft comment id: %w", err)
	}
	if _, err := d.rw.ExecContext(ctx,
		`UPDATE middleman_mr_review_drafts SET updated_at = ? WHERE id = ?`,
		now, draftID,
	); err != nil {
		return nil, fmt.Errorf("touch mr review draft: %w", err)
	}
	return d.getMRReviewDraftComment(ctx, draftID, id)
}

func (d *DB) UpdateMRReviewDraftComment(
	ctx context.Context,
	draftID, commentID int64,
	input MRReviewDraftCommentInput,
) (*MRReviewDraftComment, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	res, err := d.rw.ExecContext(ctx, `
		UPDATE middleman_mr_review_draft_comments
		SET body = ?, path = ?, old_path = ?, side = ?, start_side = ?,
			start_line = ?, line = ?, old_line = ?, new_line = ?,
			line_type = ?, diff_head_sha = ?, commit_sha = ?, updated_at = ?
		WHERE draft_id = ? AND id = ?`,
		input.Body, input.Range.Path, nullString(input.Range.OldPath), input.Range.Side,
		nullString(input.Range.StartSide), nullInt(input.Range.StartLine),
		input.Range.Line, nullInt(input.Range.OldLine), nullInt(input.Range.NewLine),
		input.Range.LineType, input.Range.DiffHeadSHA, input.Range.CommitSHA,
		now, draftID, commentID,
	)
	if err != nil {
		return nil, fmt.Errorf("update mr review draft comment: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return nil, fmt.Errorf("get updated draft comment count: %w", err)
	} else if n == 0 {
		return nil, sql.ErrNoRows
	}
	if _, err := d.rw.ExecContext(ctx,
		`UPDATE middleman_mr_review_drafts SET updated_at = ? WHERE id = ?`,
		now, draftID,
	); err != nil {
		return nil, fmt.Errorf("touch mr review draft: %w", err)
	}
	return d.getMRReviewDraftComment(ctx, draftID, commentID)
}

func (d *DB) DeleteMRReviewDraftComment(ctx context.Context, draftID, commentID int64) error {
	res, err := d.rw.ExecContext(ctx,
		`DELETE FROM middleman_mr_review_draft_comments WHERE draft_id = ? AND id = ?`,
		draftID, commentID,
	)
	if err != nil {
		return fmt.Errorf("delete mr review draft comment: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return fmt.Errorf("get deleted draft comment count: %w", err)
	} else if n == 0 {
		return sql.ErrNoRows
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := d.rw.ExecContext(ctx,
		`UPDATE middleman_mr_review_drafts SET updated_at = ? WHERE id = ?`,
		now, draftID,
	); err != nil {
		return fmt.Errorf("touch mr review draft: %w", err)
	}
	return nil
}

func (d *DB) DeleteMRReviewDraft(ctx context.Context, mrID int64) error {
	if _, err := d.rw.ExecContext(ctx,
		`DELETE FROM middleman_mr_review_drafts WHERE merge_request_id = ?`,
		mrID,
	); err != nil {
		return fmt.Errorf("delete mr review draft: %w", err)
	}
	return nil
}

func (d *DB) getMRReviewDraftComment(ctx context.Context, draftID, commentID int64) (*MRReviewDraftComment, error) {
	row := d.ro.QueryRowContext(ctx, `
		SELECT id, draft_id, body, path, old_path, side, start_side, start_line,
			line, old_line, new_line, line_type, diff_head_sha, commit_sha,
			created_at, updated_at
		FROM middleman_mr_review_draft_comments
		WHERE draft_id = ? AND id = ?`,
		draftID, commentID,
	)
	comment, err := scanReviewDraftComment(row)
	if err != nil {
		return nil, err
	}
	return &comment, nil
}

type scanner interface {
	Scan(dest ...any) error
}

func scanReviewDraftComment(row scanner) (MRReviewDraftComment, error) {
	var comment MRReviewDraftComment
	var oldPath, startSide sql.NullString
	var startLine, oldLine, newLine sql.NullInt64
	var createdAt, updatedAt string
	if err := row.Scan(
		&comment.ID, &comment.DraftID, &comment.Body,
		&comment.Range.Path, &oldPath, &comment.Range.Side, &startSide,
		&startLine, &comment.Range.Line, &oldLine, &newLine,
		&comment.Range.LineType, &comment.Range.DiffHeadSHA,
		&comment.Range.CommitSHA, &createdAt, &updatedAt,
	); err != nil {
		return MRReviewDraftComment{}, fmt.Errorf("scan mr review draft comment: %w", err)
	}
	comment.Range.OldPath = oldPath.String
	comment.Range.StartSide = startSide.String
	comment.Range.StartLine = intPtr(startLine)
	comment.Range.OldLine = intPtr(oldLine)
	comment.Range.NewLine = intPtr(newLine)
	var err error
	comment.CreatedAt, err = parseDBTime(createdAt)
	if err != nil {
		return MRReviewDraftComment{}, fmt.Errorf("parse draft comment created_at: %w", err)
	}
	comment.UpdatedAt, err = parseDBTime(updatedAt)
	if err != nil {
		return MRReviewDraftComment{}, fmt.Errorf("parse draft comment updated_at: %w", err)
	}
	return comment, nil
}

func (d *DB) UpsertMRReviewThreads(ctx context.Context, mrID int64, threads []MRReviewThread) error {
	return d.Tx(ctx, func(tx *sql.Tx) error {
		for _, thread := range threads {
			providerThreadID := thread.ProviderThreadID
			if providerThreadID == "" {
				providerThreadID = thread.ProviderCommentID
			}
			if providerThreadID == "" {
				return fmt.Errorf("upsert mr review thread: provider thread id is empty")
			}
			resolvedAt := nullableReviewTime(thread.ResolvedAt)
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO middleman_mr_review_threads (
					merge_request_id, provider_thread_id, provider_review_id,
					provider_comment_id, path, old_path, side, start_side,
					start_line, line, old_line, new_line, line_type,
					diff_head_sha, commit_sha, body, author_login, resolved,
					created_at, updated_at, resolved_at, metadata_json
				) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
				ON CONFLICT(merge_request_id, provider_thread_id) DO UPDATE SET
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
					resolved = excluded.resolved,
					created_at = excluded.created_at,
					updated_at = excluded.updated_at,
					resolved_at = excluded.resolved_at,
					metadata_json = excluded.metadata_json`,
				mrID, providerThreadID, nullString(thread.ProviderReviewID),
				nullString(thread.ProviderCommentID), thread.Range.Path,
				nullString(thread.Range.OldPath), thread.Range.Side,
				nullString(thread.Range.StartSide), nullInt(thread.Range.StartLine),
				thread.Range.Line, nullInt(thread.Range.OldLine), nullInt(thread.Range.NewLine),
				thread.Range.LineType, thread.Range.DiffHeadSHA, thread.Range.CommitSHA,
				thread.Body, nullString(thread.AuthorLogin), thread.Resolved,
				thread.CreatedAt.UTC().Format(time.RFC3339),
				thread.UpdatedAt.UTC().Format(time.RFC3339),
				resolvedAt, nullString(thread.MetadataJSON),
			); err != nil {
				return fmt.Errorf("upsert mr review thread %s: %w", thread.ProviderThreadID, err)
			}
		}
		return nil
	})
}

func (d *DB) ListMRReviewThreads(ctx context.Context, mrID int64) ([]MRReviewThread, error) {
	rows, err := d.ro.QueryContext(ctx, `
		SELECT id, merge_request_id, provider_thread_id, provider_review_id,
			provider_comment_id, path, old_path, side, start_side, start_line,
			line, old_line, new_line, line_type, diff_head_sha, commit_sha,
			body, author_login, resolved, created_at, updated_at,
			resolved_at, metadata_json
		FROM middleman_mr_review_threads
		WHERE merge_request_id = ?
		ORDER BY created_at, id`,
		mrID,
	)
	if err != nil {
		return nil, fmt.Errorf("list mr review threads: %w", err)
	}
	defer rows.Close()

	var threads []MRReviewThread
	for rows.Next() {
		thread, err := scanMRReviewThread(rows)
		if err != nil {
			return nil, err
		}
		threads = append(threads, thread)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate mr review threads: %w", err)
	}
	return threads, nil
}

func (d *DB) GetMRReviewThread(ctx context.Context, mrID, threadID int64) (*MRReviewThread, error) {
	row := d.ro.QueryRowContext(ctx, `
		SELECT id, merge_request_id, provider_thread_id, provider_review_id,
			provider_comment_id, path, old_path, side, start_side, start_line,
			line, old_line, new_line, line_type, diff_head_sha, commit_sha,
			body, author_login, resolved, created_at, updated_at,
			resolved_at, metadata_json
		FROM middleman_mr_review_threads
		WHERE merge_request_id = ? AND id = ?`,
		mrID, threadID,
	)
	thread, err := scanMRReviewThread(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &thread, nil
}

func (d *DB) SetMRReviewThreadResolved(
	ctx context.Context,
	mrID, threadID int64,
	resolved bool,
	resolvedAt *time.Time,
) error {
	res, err := d.rw.ExecContext(ctx, `
		UPDATE middleman_mr_review_threads
		SET resolved = ?, resolved_at = ?, updated_at = ?
		WHERE merge_request_id = ? AND id = ?`,
		resolved, nullableReviewTime(resolvedAt), time.Now().UTC().Format(time.RFC3339),
		mrID, threadID,
	)
	if err != nil {
		return fmt.Errorf("set mr review thread resolved: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return fmt.Errorf("get resolved review thread count: %w", err)
	} else if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func scanMRReviewThread(row scanner) (MRReviewThread, error) {
	var thread MRReviewThread
	var providerReviewID, providerCommentID, oldPath, startSide, authorLogin sql.NullString
	var startLine, oldLine, newLine sql.NullInt64
	var resolvedAt, metadataJSON sql.NullString
	var createdAt, updatedAt string
	if err := row.Scan(
		&thread.ID, &thread.MergeRequestID, &thread.ProviderThreadID,
		&providerReviewID, &providerCommentID, &thread.Range.Path,
		&oldPath, &thread.Range.Side, &startSide, &startLine,
		&thread.Range.Line, &oldLine, &newLine, &thread.Range.LineType,
		&thread.Range.DiffHeadSHA, &thread.Range.CommitSHA,
		&thread.Body, &authorLogin, &thread.Resolved, &createdAt,
		&updatedAt, &resolvedAt, &metadataJSON,
	); err != nil {
		return MRReviewThread{}, fmt.Errorf("scan mr review thread: %w", err)
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
		return MRReviewThread{}, fmt.Errorf("parse review thread created_at: %w", err)
	}
	thread.UpdatedAt, err = parseDBTime(updatedAt)
	if err != nil {
		return MRReviewThread{}, fmt.Errorf("parse review thread updated_at: %w", err)
	}
	thread.ResolvedAt, err = parseNullableTime(resolvedAt)
	if err != nil {
		return MRReviewThread{}, fmt.Errorf("parse review thread resolved_at: %w", err)
	}
	return thread, nil
}
