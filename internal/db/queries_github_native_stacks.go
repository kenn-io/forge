package db

import (
	"context"
	"database/sql"
	"fmt"
)

// ReplaceGitHubNativeStack atomically upserts one cached GitHub stack and
// replaces its ordered member snapshot.
func (d *DB) ReplaceGitHubNativeStack(ctx context.Context, stack GitHubNativeStack) error {
	return d.Tx(ctx, func(tx *sql.Tx) error {
		var stackID int64
		err := tx.QueryRowContext(ctx, `
			INSERT INTO github_native_stacks (
				repo_id, github_id, stack_number, size, base_ref, is_open,
				github_created_at, content_fingerprint, last_observed_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(repo_id, stack_number) DO UPDATE SET
				github_id = excluded.github_id,
				size = excluded.size,
				base_ref = excluded.base_ref,
				is_open = excluded.is_open,
				github_created_at = excluded.github_created_at,
				content_fingerprint = excluded.content_fingerprint,
				last_observed_at = excluded.last_observed_at
			RETURNING id`,
			stack.RepoID, stack.GitHubID, stack.Number, stack.Size,
			stack.BaseRef, stack.IsOpen, stack.GitHubCreatedAt,
			stack.ContentFingerprint, stack.LastObservedAt,
		).Scan(&stackID)
		if err != nil {
			return fmt.Errorf("upsert github native stack: %w", err)
		}

		if _, err := tx.ExecContext(ctx,
			`DELETE FROM github_native_stack_members WHERE stack_id = ?`, stackID,
		); err != nil {
			return fmt.Errorf("delete github native stack members: %w", err)
		}
		if len(stack.Members) == 0 {
			return nil
		}

		stmt, err := tx.PrepareContext(ctx, `
			INSERT INTO github_native_stack_members (
				stack_id, position, pull_request_number, state, is_draft,
				merged_at, head_ref, head_sha
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`)
		if err != nil {
			return fmt.Errorf("prepare github native stack member insert: %w", err)
		}
		defer stmt.Close()
		for _, member := range stack.Members {
			if _, err := stmt.ExecContext(ctx,
				stackID, member.Position, member.PullRequestNumber,
				member.State, member.Draft, member.MergedAt,
				member.HeadRef, member.HeadSHA,
			); err != nil {
				return fmt.Errorf("insert github native stack member: %w", err)
			}
		}
		return nil
	})
}

// ListGitHubNativeStacks returns cached native stacks for one repository,
// newest stack number first and members bottom-to-top.
func (d *DB) ListGitHubNativeStacks(ctx context.Context, repoID int64) ([]GitHubNativeStack, error) {
	rows, err := d.ro.QueryContext(ctx, `
		SELECT id, repo_id, github_id, stack_number, size, base_ref, is_open,
		       github_created_at, content_fingerprint, last_observed_at
		FROM github_native_stacks
		WHERE repo_id = ?
		ORDER BY stack_number DESC`, repoID)
	if err != nil {
		return nil, fmt.Errorf("list github native stacks: %w", err)
	}
	defer rows.Close()

	var stacks []GitHubNativeStack
	stackIndex := make(map[int64]int)
	for rows.Next() {
		var stack GitHubNativeStack
		if err := rows.Scan(
			&stack.ID, &stack.RepoID, &stack.GitHubID, &stack.Number,
			&stack.Size, &stack.BaseRef, &stack.IsOpen, &stack.GitHubCreatedAt,
			&stack.ContentFingerprint, &stack.LastObservedAt,
		); err != nil {
			return nil, fmt.Errorf("scan github native stack: %w", err)
		}
		stackIndex[stack.ID] = len(stacks)
		stacks = append(stacks, stack)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(stacks) == 0 {
		return stacks, nil
	}

	memberRows, err := d.ro.QueryContext(ctx, `
		SELECT m.stack_id, m.position, m.pull_request_number, m.state,
		       m.is_draft, m.merged_at, m.head_ref, m.head_sha
		FROM github_native_stack_members m
		JOIN github_native_stacks s ON s.id = m.stack_id
		WHERE s.repo_id = ?
		ORDER BY s.stack_number DESC, m.position`, repoID)
	if err != nil {
		return nil, fmt.Errorf("list github native stack members: %w", err)
	}
	defer memberRows.Close()
	for memberRows.Next() {
		var member GitHubNativeStackMember
		if err := memberRows.Scan(
			&member.StackID, &member.Position, &member.PullRequestNumber,
			&member.State, &member.Draft, &member.MergedAt,
			&member.HeadRef, &member.HeadSHA,
		); err != nil {
			return nil, fmt.Errorf("scan github native stack member: %w", err)
		}
		if index, ok := stackIndex[member.StackID]; ok {
			stacks[index].Members = append(stacks[index].Members, member)
		}
	}
	return stacks, memberRows.Err()
}

// DeleteGitHubNativeStacks removes specific repository-scoped stack numbers.
func (d *DB) DeleteGitHubNativeStacks(ctx context.Context, repoID int64, numbers []int) error {
	if len(numbers) == 0 {
		return nil
	}
	args := make([]any, 0, len(numbers)+1)
	args = append(args, repoID)
	for _, number := range numbers {
		args = append(args, number)
	}
	if _, err := d.rw.ExecContext(ctx,
		`DELETE FROM github_native_stacks WHERE repo_id = ? AND stack_number IN (`+
			sqlPlaceholders(len(numbers))+`)`, args...,
	); err != nil {
		return fmt.Errorf("delete github native stacks: %w", err)
	}
	return nil
}
