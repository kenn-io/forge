package db

import (
	"context"
	"database/sql"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// prActivityAtExpr is a pull request's provider-event recency: opening, the
// newest ledger event the feed can render, reopen, merge, or close, whichever
// is latest. Parent summaries layer visible notification recency on top.
// Provider updated_at is not activity: GitHub bumps it for mergeability
// recomputation after base pushes, head-branch deletion after merge, and other
// invisible bookkeeping, which surfaced phantom recency in the feed. The
// correlated lookup walks (merge_request_id, created_at DESC) and stops at the
// first rendered event.
func prActivityAtExpr(pr string) string {
	return fmt.Sprintf(
		"MAX(%[1]s.created_at, COALESCE((SELECT e.created_at FROM forge_mr_events e "+
			"WHERE e.merge_request_id = %[1]s.id "+
			"AND e.event_type IN ('issue_comment', 'review', 'commit', 'force_push', 'reopened') "+
			"ORDER BY e.created_at DESC LIMIT 1), %[1]s.created_at), "+
			"COALESCE(%[1]s.merged_at, %[1]s.created_at), COALESCE(%[1]s.closed_at, %[1]s.created_at))",
		pr)
}

// issueActivityAtExpr is an issue's provider-event recency: opening, the newest
// rendered ledger event, reopen, or close, whichever is latest. Parent
// summaries layer visible notification recency on top.
func issueActivityAtExpr(issue string) string {
	return fmt.Sprintf(
		"MAX(%[1]s.created_at, COALESCE((SELECT e.created_at FROM forge_issue_events e "+
			"WHERE e.issue_id = %[1]s.id AND e.event_type IN ('issue_comment', 'reopened') "+
			"ORDER BY e.created_at DESC LIMIT 1), %[1]s.created_at), "+
			"COALESCE(%[1]s.closed_at, %[1]s.created_at))",
		issue)
}

// activityNotificationAtExpr returns the newest visible notification for one
// parent. The canonical repo ID survives renames; legacy null IDs fall back to
// the current normalized route.
func activityNotificationAtExpr(parent, itemType string) string {
	return fmt.Sprintf(
		"COALESCE((SELECT MAX(n.source_updated_at) FROM forge_notification_items n "+
			"JOIN forge_repos nr ON nr.id = %[1]s.repo_id "+
			"WHERE n.item_type = '%[2]s' AND n.item_number = %[1]s.number "+
			"AND n.reason != 'author' AND (n.repo_id = nr.id OR (n.repo_id IS NULL "+
			"AND n.platform = nr.platform AND n.platform_host = nr.platform_host "+
			"AND n.repo_owner = nr.owner_key AND n.repo_name = nr.name_key))), %[1]s.created_at)",
		parent, itemType,
	)
}

func prActivitySubjectAtExpr(pr string) string {
	return fmt.Sprintf("MAX(%s, %s)", prActivityAtExpr(pr), activityNotificationAtExpr(pr, "pr"))
}

func issueActivitySubjectAtExpr(issue string) string {
	return fmt.Sprintf("MAX(%s, %s)", issueActivityAtExpr(issue), activityNotificationAtExpr(issue, "issue"))
}

// prEventLedgerRevisionExpr and issueEventLedgerRevisionExpr identify every
// child row a thread-events request can render. The parent mutation revision
// advances for inserts, edits, and deletes, including older backfills that do
// not change activity_at. Migration-owned triggers advance the same revision
// for visible notification mutations, avoiding a correlated notification scan
// for every projected parent.
func prEventLedgerRevisionExpr(pr string) string {
	return fmt.Sprintf("printf('pre:%%d', %s.activity_event_revision)", pr)
}

func issueEventLedgerRevisionExpr(issue string) string {
	return fmt.Sprintf("printf('ise:%%d', %s.activity_event_revision)", issue)
}

// ListActivity returns a unified, reverse-chronological feed of
// activity across all repos. It merges new PRs, new issues, PR
// events, issue events, default-branch commits/force-pushes, and
// notification threads into a single stream with cursor-based keyset
// pagination.
func (d *DB) ListActivity(
	ctx context.Context, opts ListActivityOpts,
) ([]ActivityItem, error) {
	return listActivityWithQueryer(ctx, d.ro, opts)
}

type activityQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func listActivityWithQueryer(
	ctx context.Context, queryer activityQueryer, opts ListActivityOpts,
) ([]ActivityItem, error) {
	limit := opts.Limit
	if limit <= 0 {
		limit = 50
	}

	var whereClauses []string
	var args []any

	if opts.Repo != "" {
		if cond := activityRepoFilterCondition(opts.RepoFilters, &args); cond != "" {
			whereClauses = append(whereClauses, cond)
		} else {
			host, pathKey := repoFilterHostAndPathKey(opts.Repo)
			if pathKey != "" {
				if host != "" {
					whereClauses = append(whereClauses, "platform_host = ?")
					args = append(args, host)
				}
				whereClauses = append(whereClauses, "repo_path_key = ?")
				args = append(args, pathKey)
			}
		}
	}
	if opts.AllowedRepoIDs != nil {
		cond := activityRepoIDCondition(opts.AllowedRepoIDs, &args)
		if cond == "" {
			cond = "0 = 1"
		}
		whereClauses = append(whereClauses, cond)
	}

	if len(opts.Types) > 0 {
		placeholders := make([]string, len(opts.Types))
		for i, t := range opts.Types {
			placeholders[i] = "?"
			args = append(args, t)
		}
		whereClauses = append(whereClauses,
			"activity_type IN ("+strings.Join(placeholders, ",")+")")
	}

	if len(opts.ItemTypes) > 0 {
		itemTypeClauses := make([]string, 0, len(opts.ItemTypes))
		for _, itemType := range opts.ItemTypes {
			if itemType == "repo" {
				itemTypeClauses = append(itemTypeClauses, "item_type = ''")
				continue
			}
			itemTypeClauses = append(itemTypeClauses, "item_type = ?")
			args = append(args, itemType)
		}
		whereClauses = append(whereClauses,
			"("+strings.Join(itemTypeClauses, " OR ")+")")
	}

	if opts.Search != "" {
		pattern := "%" + strings.ToLower(opts.Search) + "%"
		whereClauses = append(whereClauses,
			"((item_type IN ('pr', 'issue') AND LOWER('#' || item_number || ' ' || item_title) LIKE ?) OR "+
				"LOWER(item_title) LIKE ? OR LOWER(body_preview) LIKE ? OR LOWER(branch_name) LIKE ? OR "+
				"LOWER(commit_sha) LIKE ? OR LOWER(before_sha) LIKE ? OR LOWER(after_sha) LIKE ? OR "+
				"LOWER(author) LIKE ? OR LOWER(item_author) LIKE ? OR "+
				"LOWER(author_name) LIKE ? OR LOWER(author_email) LIKE ? OR "+
				"LOWER(committer_name) LIKE ? OR LOWER(committer_email) LIKE ?)")
		args = append(args,
			pattern, pattern, pattern, pattern, pattern, pattern, pattern,
			pattern, pattern, pattern, pattern, pattern, pattern)
	}

	if opts.Author != "" {
		whereClauses = append(whereClauses, "LOWER(item_author) = LOWER(?)")
		args = append(args, opts.Author)
	}
	if opts.HideClosedMerged {
		whereClauses = append(whereClauses,
			"((source = 'ntf' AND (subject_state = '' OR subject_state NOT IN ('closed', 'merged'))) OR "+
				"(source != 'ntf' AND item_state NOT IN ('closed', 'merged')))")
	}
	if opts.HideBots {
		whereClauses = append(whereClauses, activityNotBotCondition("author"))
	}
	if opts.HideDefaultBranch {
		whereClauses = append(whereClauses,
			"activity_type NOT IN ('default_branch_commit', 'default_branch_force_push')")
	}
	if opts.ParentRepoID != 0 {
		whereClauses = append(whereClauses, "repo_id = ?")
		args = append(args, opts.ParentRepoID)
	}
	if opts.ParentItemType != "" {
		whereClauses = append(whereClauses, "item_type = ?")
		args = append(args, opts.ParentItemType)
	}
	if opts.ParentItemNumber != 0 {
		whereClauses = append(whereClauses, "item_number = ?")
		args = append(args, opts.ParentItemNumber)
	}
	if opts.UnparentedOnly {
		whereClauses = append(whereClauses, "parent_id IS NULL")
	}
	if opts.ViewerLogins != nil {
		whereClauses = append(whereClauses, activityInvolvementCondition(opts.ViewerLogins, &args))
	}

	// Time window filter.
	if opts.Since != nil {
		whereClauses = append(whereClauses, "created_at >= ?")
		args = append(args, *opts.Since)
	}

	if opts.BeforeTime != nil {
		whereClauses = append(whereClauses,
			"(created_at < ? OR (created_at = ? AND "+
				"(source < ? OR (source = ? AND source_id < ?))))")
		args = append(args,
			*opts.BeforeTime, *opts.BeforeTime,
			opts.BeforeSource, opts.BeforeSource,
			opts.BeforeSourceID)
	}

	if opts.AfterTime != nil {
		whereClauses = append(whereClauses,
			"(created_at > ? OR (created_at = ? AND "+
				"(source > ? OR (source = ? AND source_id > ?))))")
		args = append(args,
			*opts.AfterTime, *opts.AfterTime,
			opts.AfterSource, opts.AfterSource,
			opts.AfterSourceID)
	}
	if opts.AtOrBeforeTime != nil {
		whereClauses = append(whereClauses,
			"(created_at < ? OR (created_at = ? AND "+
				"(source < ? OR (source = ? AND source_id <= ?))))")
		args = append(args,
			*opts.AtOrBeforeTime, *opts.AtOrBeforeTime,
			opts.AtOrBeforeSource, opts.AtOrBeforeSource,
			opts.AtOrBeforeSourceID)
	}

	where := ""
	if len(whereClauses) > 0 {
		where = "WHERE " + strings.Join(whereClauses, " AND ")
	}

	// Notifications join the union as their own source, filtered to
	// PR/issue-anchored, non-author rows. Excluding the whole branch here
	// (only when no config is loaded) rather than after the query keeps the
	// LIMIT window from being spent on rows the caller will not serve.
	// Title, URL, author, and state come from the linked PR or issue when it
	// is synced: the persisted notification keeps the route and title from
	// sync time, which go stale after a repository rename or title edit.
	notificationUnion := ""
	var notificationArgs []any
	if !opts.ExcludeNotifications {
		notificationScope := ""
		if opts.NotificationRepoFilters != nil {
			notificationScope = activityNotificationRepoFilterCondition(
				opts.NotificationRepoFilters, &notificationArgs,
			)
		}
		if notificationScope != "" {
			notificationScope = " AND " + notificationScope
		}
		notificationUnion = `
			UNION ALL
			SELECT 'notification', 'ntf', n.id, r.id,
			       r.platform, r.platform_host, r.platform_repo_id, r.repo_path,
			       r.owner, r.name, r.repo_path_key,
			       n.item_type, COALESCE(n.item_number, 0),
			       COALESCE(NULLIF(mr.title, ''), NULLIF(iss.title, ''), n.subject_title),
			       COALESCE(NULLIF(mr.url, ''), NULLIF(iss.url, ''), n.web_url),
			       CASE WHEN n.unread = 1 THEN 'unread' ELSE 'read' END,
			       COALESCE(NULLIF(mr.author, ''), NULLIF(iss.author, ''), n.item_author),
			       COALESCE(NULLIF(mr.author, ''), NULLIF(iss.author, ''), n.item_author),
			       n.source_updated_at,
			       COALESCE(mr.id, iss.id),
			       substr(n.reason, 1, 200),
			       '', '', '', '',
			       '', '',
			       '', '',
			       NULL, NULL,
			       COALESCE(NULLIF(mr.url, ''), NULLIF(iss.url, ''), n.web_url),
			       COALESCE(mr.state, iss.state, '')
			FROM forge_notification_items n
			JOIN forge_repos r
			       ON r.lifecycle_state = 'active'
			      AND (
			          n.repo_id = r.id
			          OR (n.repo_id IS NULL
			              AND r.platform = n.platform
			              AND r.platform_host = n.platform_host
			              AND r.owner_key = n.repo_owner
			              AND r.name_key = n.repo_name)
			      )
			LEFT JOIN forge_merge_requests mr
			       ON n.item_type = 'pr' AND mr.repo_id = r.id AND mr.number = n.item_number
			LEFT JOIN forge_issues iss
			       ON n.item_type = 'issue' AND iss.repo_id = r.id AND iss.number = n.item_number
			WHERE n.item_type IN ('pr', 'issue') AND n.item_number IS NOT NULL
			      AND n.reason != 'author'
			      AND NOT EXISTS (
			          SELECT 1 FROM forge_archive_items ai
			          WHERE ai.repo_id = r.id
			            AND ai.item_type = CASE n.item_type
			                WHEN 'pr' THEN 'merge_request' ELSE 'issue' END
			            AND ai.item_number = n.item_number
			            AND ai.lifecycle_state = 'removed_upstream'
			      )` + notificationScope
	}

	// The page is materialized before parent recency is derived so the ledger
	// lookup runs once per returned row rather than once per candidate row.
	query := fmt.Sprintf(`
		WITH page AS MATERIALIZED (
		SELECT activity_type, source, source_id, repo_id, platform, platform_host,
		       platform_repo_id, repo_path, repo_owner, repo_name,
		       item_type, item_number, item_title,
		       item_url, item_state, author, item_author,
		       created_at, parent_id, body_preview,
		       branch_name, commit_sha, before_sha, after_sha,
		       author_name, author_email, committer_name, committer_email,
		       authored_at, committed_at, activity_url,
		       subject_state
		FROM (
			SELECT 'new_pr' AS activity_type,
			       'pr' AS source, p.id AS source_id,
			       r.id AS repo_id,
			       r.platform, r.platform_host, r.platform_repo_id, r.repo_path,
			       r.owner AS repo_owner, r.name AS repo_name, r.repo_path_key,
			       'pr' AS item_type, p.number AS item_number,
			       p.title AS item_title,
			       p.url AS item_url, p.state AS item_state,
			       p.author, p.author AS item_author, p.created_at,
			       p.id AS parent_id,
			       '' AS body_preview,
			       '' AS branch_name, '' AS commit_sha, '' AS before_sha, '' AS after_sha,
			       '' AS author_name, '' AS author_email,
			       '' AS committer_name, '' AS committer_email,
			       NULL AS authored_at, NULL AS committed_at,
			       '' AS activity_url,
			       '' AS subject_state
			FROM forge_merge_requests p
			JOIN forge_repos r ON p.repo_id = r.id AND r.lifecycle_state = 'active'
			WHERE NOT EXISTS (
			    SELECT 1 FROM forge_archive_items ai
			    WHERE ai.repo_id = p.repo_id
			      AND ai.item_type = 'merge_request'
			      AND ai.item_number = p.number
			      AND ai.lifecycle_state = 'removed_upstream'
			)
			UNION ALL
			SELECT 'new_issue', 'issue', i.id, r.id,
			       r.platform, r.platform_host, r.platform_repo_id, r.repo_path,
			       r.owner, r.name, r.repo_path_key,
			       'issue', i.number, i.title,
			       i.url, i.state,
			       i.author, i.author, i.created_at,
			       i.id,
			       '',
			       '', '', '', '',
			       '', '',
			       '', '',
			       NULL, NULL,
			       '',
			       ''
			FROM forge_issues i
			JOIN forge_repos r ON i.repo_id = r.id AND r.lifecycle_state = 'active'
			WHERE NOT EXISTS (
			    SELECT 1 FROM forge_archive_items ai
			    WHERE ai.repo_id = i.repo_id
			      AND ai.item_type = 'issue'
			      AND ai.item_number = i.number
			      AND ai.lifecycle_state = 'removed_upstream'
			)
			UNION ALL
			SELECT CASE e.event_type
			           WHEN 'issue_comment' THEN 'comment'
			           ELSE e.event_type
			       END,
			       'pre', e.id,
			       r.id,
			       r.platform, r.platform_host, r.platform_repo_id, r.repo_path,
			       r.owner, r.name, r.repo_path_key,
			       'pr', p.number, p.title,
			       p.url, p.state,
			       e.author, p.author, e.created_at,
			       p.id,
			       substr(COALESCE(e.body, ''), 1, 200),
			       '', '', '', '',
			       '', '',
			       '', '',
			       NULL, NULL,
			       '',
			       ''
			FROM forge_mr_events e
			JOIN forge_merge_requests p ON e.merge_request_id = p.id
			JOIN forge_repos r ON p.repo_id = r.id AND r.lifecycle_state = 'active'
			WHERE e.event_type IN (
				'issue_comment', 'review', 'commit', 'force_push')
			  AND NOT EXISTS (
			      SELECT 1 FROM forge_archive_items ai
			      WHERE ai.repo_id = p.repo_id
			        AND ai.item_type = 'merge_request'
			        AND ai.item_number = p.number
			        AND ai.lifecycle_state = 'removed_upstream'
			  )
			UNION ALL
			SELECT 'comment', 'ise', e.id, r.id,
			       r.platform, r.platform_host, r.platform_repo_id, r.repo_path,
			       r.owner, r.name, r.repo_path_key,
			       'issue', i.number, i.title,
			       i.url, i.state,
			       e.author, i.author, e.created_at,
			       i.id,
			       substr(COALESCE(e.body, ''), 1, 200),
			       '', '', '', '',
			       '', '',
			       '', '',
			       NULL, NULL,
			       '',
			       ''
			FROM forge_issue_events e
			JOIN forge_issues i ON e.issue_id = i.id
			JOIN forge_repos r ON i.repo_id = r.id AND r.lifecycle_state = 'active'
			WHERE e.event_type = 'issue_comment'
			  AND NOT EXISTS (
			      SELECT 1 FROM forge_archive_items ai
			      WHERE ai.repo_id = i.repo_id
			        AND ai.item_type = 'issue'
			        AND ai.item_number = i.number
			        AND ai.lifecycle_state = 'removed_upstream'
			  )
			UNION ALL
			SELECT 'default_branch_commit', 'bc', bc.id, r.id,
			       r.platform, r.platform_host, r.platform_repo_id, r.repo_path,
			       r.owner, r.name, r.repo_path_key,
			       '', 0, '',
			       '', '',
			       substr(bc.author_name, 1, %[1]d), '', bc.committed_at,
			       NULL,
			       substr(bc.subject, 1, 200),
			       bc.branch_name, bc.commit_sha, '', '',
			       substr(bc.author_name, 1, %[1]d),
			       substr(bc.author_email, 1, %[1]d),
			       substr(bc.committer_name, 1, %[1]d),
			       substr(bc.committer_email, 1, %[1]d),
			       bc.authored_at, bc.committed_at,
			       '',
			       ''
			FROM forge_branch_commits bc
			JOIN forge_repos r ON bc.repo_id = r.id AND r.lifecycle_state = 'active'
			UNION ALL
			SELECT 'default_branch_force_push', 'bfp', bfp.id, r.id,
			       r.platform, r.platform_host, r.platform_repo_id, r.repo_path,
			       r.owner, r.name, r.repo_path_key,
			       '', 0, '',
			       '', '',
			       '', '', bfp.detected_at,
			       NULL,
			       bfp.before_sha || ' -> ' || bfp.after_sha,
			       bfp.branch_name, '', bfp.before_sha, bfp.after_sha,
			       '', '',
			       '', '',
			       NULL, NULL,
			       '',
			       ''
			FROM forge_branch_force_pushes bfp
			JOIN forge_repos r ON bfp.repo_id = r.id AND r.lifecycle_state = 'active'
			%[3]s
		) unified
		%[2]s
		ORDER BY created_at DESC, source DESC, source_id DESC
		LIMIT ?
		)
		SELECT activity_type, source, source_id, repo_id, platform, platform_host,
		       platform_repo_id, repo_path, repo_owner, repo_name,
		       item_type, item_number, item_title,
		       item_url, item_state, author, item_author,
		       created_at,
		       CASE item_type
		           WHEN 'pr' THEN (SELECT %[4]s FROM forge_merge_requests p WHERE p.id = page.parent_id)
		           WHEN 'issue' THEN (SELECT %[5]s FROM forge_issues i WHERE i.id = page.parent_id)
		       END AS item_last_activity_at,
		       body_preview,
		       branch_name, commit_sha, before_sha, after_sha,
		       author_name, author_email, committer_name, committer_email,
		       authored_at, committed_at, activity_url,
		       subject_state
		FROM page
		ORDER BY created_at DESC, source DESC, source_id DESC`,
		branchCommitIdentityMaxBytes, where, notificationUnion,
		prActivityAtExpr("p"), issueActivityAtExpr("i"))

	queryArgs := make([]any, 0, len(notificationArgs)+len(args)+1)
	queryArgs = append(queryArgs, notificationArgs...)
	queryArgs = append(queryArgs, args...)
	queryArgs = append(queryArgs, limit)

	rows, err := queryer.QueryContext(ctx, query, queryArgs...)
	if err != nil {
		return nil, fmt.Errorf("list activity: %w", err)
	}
	defer rows.Close()

	var items []ActivityItem
	for rows.Next() {
		var it ActivityItem
		var createdAtStr string
		var itemLastActivityAtStr sql.NullString
		var authoredAtStr sql.NullString
		var committedAtStr sql.NullString
		if err := rows.Scan(
			&it.ActivityType, &it.Source, &it.SourceID,
			&it.RepoID,
			&it.Platform, &it.PlatformHost, &it.PlatformRepoID, &it.RepoPath,
			&it.RepoOwner, &it.RepoName,
			&it.ItemType, &it.ItemNumber, &it.ItemTitle,
			&it.ItemURL, &it.ItemState, &it.Author, &it.ItemAuthor,
			&createdAtStr, &itemLastActivityAtStr, &it.BodyPreview,
			&it.BranchName, &it.CommitSHA, &it.BeforeSHA, &it.AfterSHA,
			&it.AuthorName, &it.AuthorEmail,
			&it.CommitterName, &it.CommitterEmail,
			&authoredAtStr, &committedAtStr, &it.ActivityURL,
			&it.SubjectState,
		); err != nil {
			return nil, fmt.Errorf("scan activity item: %w", err)
		}
		t, err := parseDBTime(createdAtStr)
		if err != nil {
			return nil, fmt.Errorf(
				"parse activity created_at %q: %w",
				createdAtStr, err)
		}
		it.CreatedAt = t
		if itemLastActivityAtStr.Valid && itemLastActivityAtStr.String != "" {
			itemLastActivityAt, err := parseDBTime(itemLastActivityAtStr.String)
			if err != nil {
				return nil, fmt.Errorf(
					"parse activity item_last_activity_at %q: %w",
					itemLastActivityAtStr.String, err)
			}
			it.ItemLastActivityAt = &itemLastActivityAt
		}
		if authoredAtStr.Valid && authoredAtStr.String != "" {
			authoredAt, err := parseDBTime(authoredAtStr.String)
			if err != nil {
				return nil, fmt.Errorf(
					"parse activity authored_at %q: %w",
					authoredAtStr.String, err)
			}
			it.AuthoredAt = &authoredAt
		}
		if committedAtStr.Valid && committedAtStr.String != "" {
			committedAt, err := parseDBTime(committedAtStr.String)
			if err != nil {
				return nil, fmt.Errorf(
					"parse activity committed_at %q: %w",
					committedAtStr.String, err)
			}
			it.CommittedAt = &committedAt
		}
		items = append(items, it)
	}
	return items, rows.Err()
}

// ListActivitySubjects returns a full parent snapshot ordered and filtered by
// ledger-derived pull-request and issue recency, including visible notification
// timestamps. Event filters and cursors do not participate because a hidden or
// behind-cursor event can still advance a parent's visible timestamp and position.
func (d *DB) ListActivitySubjects(
	ctx context.Context, opts ListActivitySubjectsOpts,
) ([]ActivitySubject, error) {
	return listActivitySubjectsWithQueryer(ctx, d.ro, opts)
}

func listActivitySubjectsWithQueryer(
	ctx context.Context, queryer activityQueryer, opts ListActivitySubjectsOpts,
) ([]ActivitySubject, error) {
	limit := opts.Limit
	if limit <= 0 {
		limit = 50
	}

	var whereClauses []string
	var args []any
	if opts.Repo != "" {
		if cond := activityRepoFilterCondition(opts.RepoFilters, &args); cond != "" {
			whereClauses = append(whereClauses, cond)
		} else {
			host, pathKey := repoFilterHostAndPathKey(opts.Repo)
			if pathKey != "" {
				if host != "" {
					whereClauses = append(whereClauses, "platform_host = ?")
					args = append(args, host)
				}
				whereClauses = append(whereClauses, "repo_path_key = ?")
				args = append(args, pathKey)
			}
		}
	}
	if opts.AllowedRepoIDs != nil {
		cond := activityRepoIDCondition(opts.AllowedRepoIDs, &args)
		if cond == "" {
			cond = "0 = 1"
		}
		whereClauses = append(whereClauses, cond)
	}
	if len(opts.ItemTypes) > 0 {
		itemTypeClauses := make([]string, 0, len(opts.ItemTypes))
		for _, itemType := range opts.ItemTypes {
			if itemType != "pr" && itemType != "issue" {
				continue
			}
			itemTypeClauses = append(itemTypeClauses, "item_type = ?")
			args = append(args, itemType)
		}
		if len(itemTypeClauses) == 0 {
			whereClauses = append(whereClauses, "0 = 1")
		} else {
			whereClauses = append(whereClauses, "("+strings.Join(itemTypeClauses, " OR ")+")")
		}
	}
	if opts.Search != "" {
		pattern := "%" + strings.ToLower(opts.Search) + "%"
		searchClauses := []string{
			"LOWER('#' || item_number || ' ' || item_title) LIKE ?",
			"LOWER(item_title) LIKE ?",
			"LOWER(item_author) LIKE ?",
		}
		args = append(args, pattern, pattern, pattern)

		matchedSubjectPlaceholders := make([]string, 0, len(opts.SearchMatchedSubjectKeys))
		seenMatchedSubjects := make(map[WorkspaceSubjectKey]struct{}, len(opts.SearchMatchedSubjectKeys))
		for _, key := range opts.SearchMatchedSubjectKeys {
			if key.ItemType != "pr" && key.ItemType != "issue" {
				continue
			}
			if _, seen := seenMatchedSubjects[key]; seen {
				continue
			}
			seenMatchedSubjects[key] = struct{}{}
			matchedSubjectPlaceholders = append(matchedSubjectPlaceholders, "(?, ?, ?)")
			args = append(args, key.RepoID, key.ItemType, key.ItemNumber)
		}
		if len(matchedSubjectPlaceholders) > 0 {
			searchClauses = append(searchClauses,
				"(repo_id, item_type, item_number) IN ("+
					strings.Join(matchedSubjectPlaceholders, ", ")+")")
		}
		whereClauses = append(whereClauses, "("+strings.Join(searchClauses, " OR ")+")")
	}
	if opts.Author != "" {
		whereClauses = append(whereClauses, "LOWER(item_author) = LOWER(?)")
		args = append(args, opts.Author)
	}
	if opts.HideClosedMerged {
		whereClauses = append(whereClauses, "item_state NOT IN ('closed', 'merged')")
	}
	if opts.HideBots {
		whereClauses = append(whereClauses, activityNotBotCondition("item_author"))
	}
	if opts.ViewerLogins != nil {
		whereClauses = append(whereClauses, activityInvolvementCondition(opts.ViewerLogins, &args))
	}
	if opts.Since != nil {
		whereClauses = append(whereClauses, "activity_at >= ?")
		args = append(args, *opts.Since)
	}

	where := ""
	if len(whereClauses) > 0 {
		where = "WHERE " + strings.Join(whereClauses, " AND ")
	}
	query := `
		SELECT repo_id, platform, platform_host, platform_repo_id, repo_path,
		       repo_owner, repo_name,
		       item_type, item_number, item_title, item_url, item_state,
		       item_author, activity_at, event_ledger_revision
		FROM (
			SELECT r.id AS repo_id, r.platform, r.platform_host,
			       r.platform_repo_id, r.repo_path,
			       r.owner AS repo_owner, r.name AS repo_name, r.repo_path_key,
			       'pr' AS item_type, p.number AS item_number, p.title AS item_title,
			       p.url AS item_url, p.state AS item_state, p.author AS item_author,
			       ` + prActivitySubjectAtExpr("p") + ` AS activity_at,
			       ` + prEventLedgerRevisionExpr("p") + ` AS event_ledger_revision
			FROM forge_merge_requests p
			JOIN forge_repos r ON p.repo_id = r.id AND r.lifecycle_state = 'active'
			WHERE NOT EXISTS (
			    SELECT 1 FROM forge_archive_items ai
			    WHERE ai.repo_id = p.repo_id
			      AND ai.item_type = 'merge_request'
			      AND ai.item_number = p.number
			      AND ai.lifecycle_state = 'removed_upstream'
			)
			UNION ALL
			SELECT r.id, r.platform, r.platform_host, r.platform_repo_id, r.repo_path,
			       r.owner, r.name, r.repo_path_key,
			       'issue', i.number, i.title, i.url, i.state, i.author,
			       ` + issueActivitySubjectAtExpr("i") + `,
			       ` + issueEventLedgerRevisionExpr("i") + `
			FROM forge_issues i
			JOIN forge_repos r ON i.repo_id = r.id AND r.lifecycle_state = 'active'
			WHERE NOT EXISTS (
			    SELECT 1 FROM forge_archive_items ai
			    WHERE ai.repo_id = i.repo_id
			      AND ai.item_type = 'issue'
			      AND ai.item_number = i.number
			      AND ai.lifecycle_state = 'removed_upstream'
			)
		) unified
		` + where + `
		ORDER BY activity_at DESC, repo_id DESC, item_type DESC, item_number DESC
		LIMIT ?`
	args = append(args, limit)

	rows, err := queryer.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list activity subjects: %w", err)
	}
	defer rows.Close()

	var subjects []ActivitySubject
	for rows.Next() {
		var subject ActivitySubject
		var activityAt string
		if err := rows.Scan(
			&subject.Subject.Key.RepoID,
			&subject.Subject.Platform,
			&subject.Subject.PlatformHost,
			&subject.Subject.PlatformRepoID,
			&subject.Subject.RepoPath,
			&subject.Subject.RepoOwner,
			&subject.Subject.RepoName,
			&subject.Subject.Key.ItemType,
			&subject.Subject.Key.ItemNumber,
			&subject.Subject.Title,
			&subject.Subject.URL,
			&subject.Subject.State,
			&subject.Subject.Author,
			&activityAt,
			&subject.EventLedgerRevision,
		); err != nil {
			return nil, fmt.Errorf("scan activity subject: %w", err)
		}
		subject.ActivityAt, err = parseDBTime(activityAt)
		if err != nil {
			return nil, fmt.Errorf("parse activity subject activity_at %q: %w", activityAt, err)
		}
		subjects = append(subjects, subject)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list activity subjects rows: %w", err)
	}
	return subjects, nil
}

// ListActivityAuthors returns the distinct, non-empty PR and issue authors
// available in an activity scope. Candidates mirror Activity recency: opening,
// rendered ledger events, merge, close, and notifications, so every parent the
// feed can show offers its author. Identity is case-insensitive; when casing
// differs, the most recently active subject's spelling wins. Results are
// ordered by most recent activity so the typeahead puts currently active
// authors first.
func (d *DB) ListActivityAuthors(
	ctx context.Context, opts ListActivityAuthorsOpts,
) ([]string, error) {
	var whereClauses []string
	var args []any

	if len(opts.RepoFilters) > 0 {
		if cond := activityRepoFilterCondition(opts.RepoFilters, &args); cond != "" {
			whereClauses = append(whereClauses, cond)
		}
	}
	if opts.AllowedRepoIDs != nil {
		cond := activityRepoIDCondition(opts.AllowedRepoIDs, &args)
		if cond == "" {
			cond = "0 = 1"
		}
		whereClauses = append(whereClauses, cond)
	}
	if opts.Since != nil {
		whereClauses = append(whereClauses, "created_at >= ?")
		args = append(args, *opts.Since)
	}
	whereClauses = append(whereClauses, "TRIM(author) != ''")

	notificationUnion := ""
	var notificationArgs []any
	if !opts.ExcludeNotifications {
		notificationScope := ""
		if opts.NotificationRepoFilters != nil {
			notificationScope = activityNotificationRepoFilterCondition(
				opts.NotificationRepoFilters, &notificationArgs,
			)
		}
		if notificationScope != "" {
			notificationScope = " AND " + notificationScope
		}
		notificationUnion = `
			UNION ALL
			SELECT r.id, r.platform, r.platform_host, r.owner, r.name, r.repo_path_key,
			       COALESCE(NULLIF(mr.author, ''), NULLIF(iss.author, ''), n.item_author),
			       n.source_updated_at
			FROM forge_notification_items n
			JOIN forge_repos r
			       ON r.lifecycle_state = 'active'
			      AND (
			          n.repo_id = r.id
			          OR (n.repo_id IS NULL
			              AND r.platform = n.platform
			              AND r.platform_host = n.platform_host
			              AND r.owner_key = n.repo_owner
			              AND r.name_key = n.repo_name)
			      )
			LEFT JOIN forge_merge_requests mr
			       ON n.item_type = 'pr' AND mr.repo_id = r.id AND mr.number = n.item_number
			LEFT JOIN forge_issues iss
			       ON n.item_type = 'issue' AND iss.repo_id = r.id AND iss.number = n.item_number
			WHERE n.item_type IN ('pr', 'issue') AND n.item_number IS NOT NULL
			      AND n.reason != 'author'
			      AND NOT EXISTS (
			          SELECT 1 FROM forge_archive_items ai
			          WHERE ai.repo_id = r.id
			            AND ai.item_type = CASE n.item_type
			                WHEN 'pr' THEN 'merge_request' ELSE 'issue' END
			            AND ai.item_number = n.item_number
			            AND ai.lifecycle_state = 'removed_upstream'
			      )` + notificationScope
	}

	query := fmt.Sprintf(`
		WITH candidates AS (
			SELECT r.id AS repo_id, r.platform, r.platform_host, r.owner AS repo_owner,
			       r.name AS repo_name, r.repo_path_key,
			       p.author, p.created_at
			FROM forge_merge_requests p
			JOIN forge_repos r ON p.repo_id = r.id AND r.lifecycle_state = 'active'
			WHERE NOT EXISTS (
			    SELECT 1 FROM forge_archive_items ai
			    WHERE ai.repo_id = p.repo_id
			      AND ai.item_type = 'merge_request'
			      AND ai.item_number = p.number
			      AND ai.lifecycle_state = 'removed_upstream'
			)
			UNION ALL
			SELECT r.id, r.platform, r.platform_host, r.owner, r.name, r.repo_path_key,
			       i.author, i.created_at
			FROM forge_issues i
			JOIN forge_repos r ON i.repo_id = r.id AND r.lifecycle_state = 'active'
			WHERE NOT EXISTS (
			    SELECT 1 FROM forge_archive_items ai
			    WHERE ai.repo_id = i.repo_id
			      AND ai.item_type = 'issue'
			      AND ai.item_number = i.number
			      AND ai.lifecycle_state = 'removed_upstream'
			)
			UNION ALL
			SELECT r.id, r.platform, r.platform_host, r.owner, r.name, r.repo_path_key,
			       p.author, e.created_at
			FROM forge_mr_events e
			JOIN forge_merge_requests p ON e.merge_request_id = p.id
			JOIN forge_repos r ON p.repo_id = r.id AND r.lifecycle_state = 'active'
			WHERE e.event_type IN ('issue_comment', 'review', 'commit', 'force_push', 'reopened')
			  AND NOT EXISTS (
			      SELECT 1 FROM forge_archive_items ai
			      WHERE ai.repo_id = p.repo_id
			        AND ai.item_type = 'merge_request'
			        AND ai.item_number = p.number
			        AND ai.lifecycle_state = 'removed_upstream'
			  )
			UNION ALL
			SELECT r.id, r.platform, r.platform_host, r.owner, r.name, r.repo_path_key,
			       i.author, e.created_at
			FROM forge_issue_events e
			JOIN forge_issues i ON e.issue_id = i.id
			JOIN forge_repos r ON i.repo_id = r.id AND r.lifecycle_state = 'active'
			WHERE e.event_type IN ('issue_comment', 'reopened')
			  AND NOT EXISTS (
			      SELECT 1 FROM forge_archive_items ai
			      WHERE ai.repo_id = i.repo_id
			        AND ai.item_type = 'issue'
			        AND ai.item_number = i.number
			        AND ai.lifecycle_state = 'removed_upstream'
			  )
			UNION ALL
			SELECT r.id, r.platform, r.platform_host, r.owner, r.name, r.repo_path_key,
			       p.author, MAX(COALESCE(p.merged_at, p.closed_at), COALESCE(p.closed_at, p.merged_at))
			FROM forge_merge_requests p
			JOIN forge_repos r ON p.repo_id = r.id AND r.lifecycle_state = 'active'
			WHERE (p.merged_at IS NOT NULL OR p.closed_at IS NOT NULL)
			  AND NOT EXISTS (
			      SELECT 1 FROM forge_archive_items ai
			      WHERE ai.repo_id = p.repo_id
			        AND ai.item_type = 'merge_request'
			        AND ai.item_number = p.number
			        AND ai.lifecycle_state = 'removed_upstream'
			  )
			UNION ALL
			SELECT r.id, r.platform, r.platform_host, r.owner, r.name, r.repo_path_key,
			       i.author, i.closed_at
			FROM forge_issues i
			JOIN forge_repos r ON i.repo_id = r.id AND r.lifecycle_state = 'active'
			WHERE i.closed_at IS NOT NULL
			  AND NOT EXISTS (
			      SELECT 1 FROM forge_archive_items ai
			      WHERE ai.repo_id = i.repo_id
			        AND ai.item_type = 'issue'
			        AND ai.item_number = i.number
			        AND ai.lifecycle_state = 'removed_upstream'
			  )
			%[1]s
		), scoped AS (
			SELECT author, created_at
			FROM candidates
			WHERE %[2]s
		), ranked AS (
			SELECT author,
			       MAX(created_at) OVER (PARTITION BY LOWER(author)) AS last_seen,
			       ROW_NUMBER() OVER (
			           PARTITION BY LOWER(author)
			           ORDER BY created_at DESC, author COLLATE NOCASE, author
			       ) AS casing_rank
			FROM scoped
		)
		SELECT author
		FROM ranked
		WHERE casing_rank = 1
		ORDER BY last_seen DESC, LOWER(author), author`,
		notificationUnion,
		strings.Join(whereClauses, " AND "),
	)

	queryArgs := make([]any, 0, len(notificationArgs)+len(args))
	queryArgs = append(queryArgs, notificationArgs...)
	queryArgs = append(queryArgs, args...)
	rows, err := d.ro.QueryContext(ctx, query, queryArgs...)
	if err != nil {
		return nil, fmt.Errorf("list activity authors: %w", err)
	}
	defer rows.Close()

	authors := make([]string, 0)
	for rows.Next() {
		var author string
		if err := rows.Scan(&author); err != nil {
			return nil, fmt.Errorf("scan activity author: %w", err)
		}
		authors = append(authors, author)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list activity authors rows: %w", err)
	}
	return authors, nil
}

func activityRepoFilterCondition(filters []RepoFilter, args *[]any) string {
	var groups []string
	for _, filter := range filters {
		var clauses []string
		if filter.RepoPath != "" {
			pathKey := canonicalRepoPathKey(filter.RepoPath)
			if pathKey == "" {
				continue
			}
			if filter.Platform != "" {
				clauses = append(clauses, "platform = ?")
				*args = append(*args, strings.ToLower(strings.TrimSpace(filter.Platform)))
			}
			if filter.PlatformHost != "" {
				host, _, _ := canonicalRepoLookupIdentifier(filter.PlatformHost, "", "")
				clauses = append(clauses, "platform_host = ?")
				*args = append(*args, host)
			}
			clauses = append(clauses, "repo_path_key = ?")
			*args = append(*args, pathKey)
		} else if filter.RepoOwner != "" && filter.RepoName != "" {
			if filter.Platform != "" {
				clauses = append(clauses, "platform = ?")
				*args = append(*args, strings.ToLower(strings.TrimSpace(filter.Platform)))
			}
			if filter.PlatformHost != "" {
				host, _, _ := canonicalRepoLookupIdentifier(filter.PlatformHost, "", "")
				clauses = append(clauses, "platform_host = ?")
				*args = append(*args, host)
			}
			clauses = append(clauses, "repo_path_key = ?")
			*args = append(*args, canonicalRepoPathKey(filter.RepoOwner+"/"+filter.RepoName))
		}
		if len(clauses) > 0 {
			groups = append(groups, "("+strings.Join(clauses, " AND ")+")")
		}
	}
	if len(groups) == 0 {
		return ""
	}
	return "(" + strings.Join(groups, " OR ") + ")"
}

func activityRepoIDCondition(repoIDs []int64, args *[]any) string {
	placeholders := make([]string, 0, len(repoIDs))
	seen := make(map[int64]struct{}, len(repoIDs))
	for _, repoID := range repoIDs {
		if repoID <= 0 {
			continue
		}
		if _, ok := seen[repoID]; ok {
			continue
		}
		seen[repoID] = struct{}{}
		placeholders = append(placeholders, "?")
		*args = append(*args, repoID)
	}
	if len(placeholders) == 0 {
		return ""
	}
	return "repo_id IN (" + strings.Join(placeholders, ",") + ")"
}

// activityNotificationRepoFilterCondition scopes the notification union to
// the requested repositories. It matches on the joined canonical repository
// r, not the notification's cached route fields, so linked rows stay visible
// after a rename; unlinked legacy rows join r through those same cached
// fields, which keeps them equivalent.
func activityNotificationRepoFilterCondition(filters []NotificationRepoFilter, args *[]any) string {
	var groups []string
	for _, filter := range filters {
		platform := strings.ToLower(strings.TrimSpace(filter.Platform))
		host, owner, name := canonicalRepoIdentifier(
			filter.PlatformHost, filter.RepoOwner, filter.RepoName,
		)
		if platform == "" || owner == "" || name == "" {
			continue
		}
		groups = append(groups, "(r.platform = ? AND r.platform_host = ? AND r.owner_key = ? AND r.name_key = ?)")
		*args = append(*args, platform, host, owner, name)
	}
	if len(groups) == 0 {
		return "0 = 1"
	}
	return "(" + strings.Join(groups, " OR ") + ")"
}

// dbTimeLayouts lists timestamp encodings that may already exist in SQLite.
// Kenn Forge now writes UTC timestamps consistently, but older databases may
// still contain local-offset strings from earlier builds or SQLite-built
// values from migrations/defaults. The parser accepts both so read paths and
// startup repair can recover the original instant before normalizing to UTC.
var dbTimeLayouts = []string{
	"2006-01-02 15:04:05 +0000 UTC",
	"2006-01-02 15:04:05 -0700 -0700",
	"2006-01-02 15:04:05 -0700 MST",
	"2006-01-02T15:04:05Z",
	time.RFC3339,
	time.RFC3339Nano,
	"2006-01-02 15:04:05",
}

func parseDBTime(s string) (time.Time, error) {
	for _, layout := range dbTimeLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("unrecognized time format: %q", s)
}

// EncodeCursor encodes a sort position into an opaque cursor string.
func EncodeCursor(
	createdAt time.Time, source string, sourceID int64,
) string {
	raw := fmt.Sprintf("%d:%s:%d",
		createdAt.UnixNano(), source, sourceID)
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

// DecodeCursor parses an opaque cursor string into its components.
func DecodeCursor(cursor string) (
	time.Time, string, int64, error,
) {
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return time.Time{}, "", 0,
			fmt.Errorf("decode cursor base64: %w", err)
	}
	parts := strings.SplitN(string(raw), ":", 3)
	if len(parts) != 3 {
		return time.Time{}, "", 0,
			fmt.Errorf("invalid cursor: expected 3 parts, got %d",
				len(parts))
	}
	ns, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return time.Time{}, "", 0,
			fmt.Errorf("invalid cursor timestamp: %w", err)
	}
	sourceID, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil {
		return time.Time{}, "", 0,
			fmt.Errorf("invalid cursor source_id: %w", err)
	}
	return time.Unix(0, ns).UTC(), parts[1], sourceID, nil
}
