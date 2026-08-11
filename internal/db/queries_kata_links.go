package db

import (
	"context"
	"database/sql"
	"fmt"
)

const kataIssueLinkSelect = `
	SELECT id, subject_kind, repo_id, provider_item_external_id, workspace_id,
	       daemon_id, project_uid, issue_uid,
	       CAST(created_at AS TEXT), CAST(updated_at AS TEXT)
	FROM kata_issue_links`

func (d *DB) CreateKataIssueLink(
	ctx context.Context,
	link KataIssueLink,
) (KataIssueLink, error) {
	link, err := link.normalized()
	if err != nil {
		return KataIssueLink{}, fmt.Errorf("create Kata issue link: %w", err)
	}

	var row *sql.Row
	switch link.Subject.Kind {
	case KataLinkSubjectPullRequest, KataLinkSubjectIssue:
		row = d.rw.QueryRowContext(ctx, `
			INSERT INTO kata_issue_links (
				subject_kind, repo_id, provider_item_external_id, workspace_id,
				daemon_id, project_uid, issue_uid
			) VALUES (?, ?, ?, NULL, ?, ?, ?)
			ON CONFLICT(
				subject_kind, repo_id, provider_item_external_id, daemon_id, issue_uid
			) WHERE subject_kind IN ('pull_request', 'issue')
			DO UPDATE SET
				project_uid = excluded.project_uid,
				updated_at = CURRENT_TIMESTAMP
			RETURNING id, subject_kind, repo_id, provider_item_external_id, workspace_id,
			          daemon_id, project_uid, issue_uid,
			          CAST(created_at AS TEXT), CAST(updated_at AS TEXT)`,
			link.Subject.Kind, link.Subject.RepoID, link.Subject.ProviderItemExternalID,
			link.DaemonID, link.ProjectUID, link.IssueUID,
		)
	case KataLinkSubjectWorkspace:
		row = d.rw.QueryRowContext(ctx, `
			INSERT INTO kata_issue_links (
				subject_kind, repo_id, provider_item_external_id, workspace_id,
				daemon_id, project_uid, issue_uid
			) VALUES (?, NULL, NULL, ?, ?, ?, ?)
			ON CONFLICT(workspace_id, daemon_id, issue_uid)
			WHERE subject_kind = 'workspace'
			DO UPDATE SET
				project_uid = excluded.project_uid,
				updated_at = CURRENT_TIMESTAMP
			RETURNING id, subject_kind, repo_id, provider_item_external_id, workspace_id,
			          daemon_id, project_uid, issue_uid,
			          CAST(created_at AS TEXT), CAST(updated_at AS TEXT)`,
			link.Subject.Kind, link.Subject.WorkspaceID,
			link.DaemonID, link.ProjectUID, link.IssueUID,
		)
	default:
		return KataIssueLink{}, fmt.Errorf("create Kata issue link: unsupported subject kind %q", link.Subject.Kind)
	}

	created, err := scanKataIssueLink(row)
	if err != nil {
		return KataIssueLink{}, fmt.Errorf("create Kata issue link: %w", err)
	}
	return created, nil
}

func (d *DB) ListKataIssueLinks(
	ctx context.Context,
	subject KataLinkSubject,
) ([]KataIssueLink, error) {
	subject, err := subject.normalized()
	if err != nil {
		return nil, fmt.Errorf("list Kata issue links: %w", err)
	}

	var rows *sql.Rows
	switch subject.Kind {
	case KataLinkSubjectPullRequest, KataLinkSubjectIssue:
		rows, err = d.ro.QueryContext(ctx, kataIssueLinkSelect+`
			WHERE subject_kind = ?
			  AND repo_id = ?
			  AND provider_item_external_id = ?
			  AND workspace_id IS NULL
			ORDER BY id`, subject.Kind, subject.RepoID, subject.ProviderItemExternalID)
	case KataLinkSubjectWorkspace:
		rows, err = d.ro.QueryContext(ctx, kataIssueLinkSelect+`
			WHERE subject_kind = ?
			  AND repo_id IS NULL
			  AND provider_item_external_id IS NULL
			  AND workspace_id = ?
			ORDER BY id`, subject.Kind, subject.WorkspaceID)
	default:
		return nil, fmt.Errorf("list Kata issue links: unsupported subject kind %q", subject.Kind)
	}
	if err != nil {
		return nil, fmt.Errorf("list Kata issue links: %w", err)
	}
	defer rows.Close()

	links := make([]KataIssueLink, 0)
	for rows.Next() {
		link, err := scanKataIssueLink(rows)
		if err != nil {
			return nil, fmt.Errorf("list Kata issue links: %w", err)
		}
		links = append(links, link)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list Kata issue links: %w", err)
	}
	return links, nil
}

func (d *DB) DeleteKataIssueLink(
	ctx context.Context,
	subject KataLinkSubject,
	linkID int64,
) (bool, error) {
	subject, err := subject.normalized()
	if err != nil {
		return false, fmt.Errorf("delete Kata issue link: %w", err)
	}
	if linkID <= 0 {
		return false, fmt.Errorf("delete Kata issue link: invalid link id")
	}

	var result sql.Result
	switch subject.Kind {
	case KataLinkSubjectPullRequest, KataLinkSubjectIssue:
		result, err = d.rw.ExecContext(ctx, `
			DELETE FROM kata_issue_links
			WHERE id = ?
			  AND subject_kind = ?
			  AND repo_id = ?
			  AND provider_item_external_id = ?
			  AND workspace_id IS NULL`,
			linkID, subject.Kind, subject.RepoID, subject.ProviderItemExternalID,
		)
	case KataLinkSubjectWorkspace:
		result, err = d.rw.ExecContext(ctx, `
			DELETE FROM kata_issue_links
			WHERE id = ?
			  AND subject_kind = ?
			  AND repo_id IS NULL
			  AND provider_item_external_id IS NULL
			  AND workspace_id = ?`,
			linkID, subject.Kind, subject.WorkspaceID,
		)
	default:
		return false, fmt.Errorf("delete Kata issue link: unsupported subject kind %q", subject.Kind)
	}
	if err != nil {
		return false, fmt.Errorf("delete Kata issue link: %w", err)
	}
	deleted, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("delete Kata issue link: %w", err)
	}
	return deleted > 0, nil
}

type kataIssueLinkScanner interface {
	Scan(...any) error
}

func scanKataIssueLink(scanner kataIssueLinkScanner) (KataIssueLink, error) {
	var link KataIssueLink
	var repoID sql.NullInt64
	var providerItemExternalID sql.NullString
	var workspaceID sql.NullString
	var createdAt string
	var updatedAt string
	if err := scanner.Scan(
		&link.ID, &link.Subject.Kind, &repoID, &providerItemExternalID, &workspaceID,
		&link.DaemonID, &link.ProjectUID, &link.IssueUID, &createdAt, &updatedAt,
	); err != nil {
		return KataIssueLink{}, err
	}
	if repoID.Valid {
		link.Subject.RepoID = repoID.Int64
	}
	if providerItemExternalID.Valid {
		link.Subject.ProviderItemExternalID = providerItemExternalID.String
	}
	if workspaceID.Valid {
		link.Subject.WorkspaceID = workspaceID.String
	}
	var err error
	link.CreatedAt, err = parseDBTime(createdAt)
	if err != nil {
		return KataIssueLink{}, fmt.Errorf("parse created_at: %w", err)
	}
	link.UpdatedAt, err = parseDBTime(updatedAt)
	if err != nil {
		return KataIssueLink{}, fmt.Errorf("parse updated_at: %w", err)
	}
	return link, nil
}
