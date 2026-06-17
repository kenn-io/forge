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

// ListActivity returns a unified, reverse-chronological feed of
// activity across all repos. It merges new PRs, new issues, PR
// events, and issue events into a single stream with cursor-based
// keyset pagination.
func (d *DB) ListActivity(
	ctx context.Context, opts ListActivityOpts,
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

	if len(opts.Types) > 0 {
		placeholders := make([]string, len(opts.Types))
		for i, t := range opts.Types {
			placeholders[i] = "?"
			args = append(args, t)
		}
		whereClauses = append(whereClauses,
			"activity_type IN ("+strings.Join(placeholders, ",")+")")
	}

	if opts.Search != "" {
		pattern := "%" + opts.Search + "%"
		whereClauses = append(whereClauses,
			"(item_title LIKE ? OR body_preview LIKE ? OR branch_name LIKE ? OR "+
				"commit_sha LIKE ? OR before_sha LIKE ? OR after_sha LIKE ? OR "+
				"author_name LIKE ? OR author_email LIKE ? OR "+
				"committer_name LIKE ? OR committer_email LIKE ?)")
		args = append(args,
			pattern, pattern, pattern, pattern, pattern, pattern,
			pattern, pattern, pattern, pattern)
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

	where := ""
	if len(whereClauses) > 0 {
		where = "WHERE " + strings.Join(whereClauses, " AND ")
	}

	query := fmt.Sprintf(`
		SELECT activity_type, source, source_id, platform, platform_host,
		       repo_owner, repo_name,
		       item_type, item_number, item_title,
		       item_url, item_state, author, item_author,
		       created_at, body_preview,
		       branch_name, commit_sha, before_sha, after_sha,
		       author_name, author_email, committer_name, committer_email,
		       authored_at, committed_at, activity_url
		FROM (
			SELECT 'new_pr' AS activity_type,
			       'pr' AS source, p.id AS source_id,
			       r.platform, r.platform_host, r.owner AS repo_owner, r.name AS repo_name,
			       r.repo_path_key,
			       'pr' AS item_type, p.number AS item_number,
			       p.title AS item_title,
			       p.url AS item_url, p.state AS item_state,
			       p.author, p.author AS item_author, p.created_at,
			       '' AS body_preview,
			       '' AS branch_name, '' AS commit_sha, '' AS before_sha, '' AS after_sha,
			       '' AS author_name, '' AS author_email,
			       '' AS committer_name, '' AS committer_email,
			       NULL AS authored_at, NULL AS committed_at,
			       '' AS activity_url
			FROM middleman_merge_requests p
			JOIN middleman_repos r ON p.repo_id = r.id
			UNION ALL
			SELECT 'new_issue', 'issue', i.id,
			       r.platform, r.platform_host, r.owner, r.name, r.repo_path_key,
			       'issue', i.number, i.title,
			       i.url, i.state,
			       i.author, i.author, i.created_at,
			       '',
			       '', '', '', '',
			       '', '',
			       '', '',
			       NULL, NULL,
			       ''
			FROM middleman_issues i
			JOIN middleman_repos r ON i.repo_id = r.id
			UNION ALL
			SELECT CASE e.event_type
			           WHEN 'issue_comment' THEN 'comment'
			           ELSE e.event_type
			       END,
			       'pre', e.id,
			       r.platform, r.platform_host, r.owner, r.name, r.repo_path_key,
			       'pr', p.number, p.title,
			       p.url, p.state,
			       e.author, p.author, e.created_at,
			       substr(COALESCE(e.body, ''), 1, 200),
			       '', '', '', '',
			       '', '',
			       '', '',
			       NULL, NULL,
			       ''
			FROM middleman_mr_events e
			JOIN middleman_merge_requests p ON e.merge_request_id = p.id
			JOIN middleman_repos r ON p.repo_id = r.id
			WHERE e.event_type IN (
				'issue_comment', 'review', 'commit', 'force_push')
			UNION ALL
			SELECT 'comment', 'ise', e.id,
			       r.platform, r.platform_host, r.owner, r.name, r.repo_path_key,
			       'issue', i.number, i.title,
			       i.url, i.state,
			       e.author, i.author, e.created_at,
			       substr(COALESCE(e.body, ''), 1, 200),
			       '', '', '', '',
			       '', '',
			       '', '',
			       NULL, NULL,
			       ''
			FROM middleman_issue_events e
			JOIN middleman_issues i ON e.issue_id = i.id
			JOIN middleman_repos r ON i.repo_id = r.id
			WHERE e.event_type = 'issue_comment'
			UNION ALL
			SELECT 'default_branch_commit', 'bc', bc.id,
			       r.platform, r.platform_host, r.owner, r.name, r.repo_path_key,
			       '', 0, '',
			       '', '',
			       substr(bc.author_name, 1, %[1]d), '', bc.committed_at,
			       substr(bc.subject, 1, 200),
			       bc.branch_name, bc.commit_sha, '', '',
			       substr(bc.author_name, 1, %[1]d),
			       substr(bc.author_email, 1, %[1]d),
			       substr(bc.committer_name, 1, %[1]d),
			       substr(bc.committer_email, 1, %[1]d),
			       bc.authored_at, bc.committed_at,
			       ''
			FROM middleman_branch_commits bc
			JOIN middleman_repos r ON bc.repo_id = r.id
			UNION ALL
			SELECT 'default_branch_force_push', 'bfp', bfp.id,
			       r.platform, r.platform_host, r.owner, r.name, r.repo_path_key,
			       '', 0, '',
			       '', '',
			       '', '', bfp.detected_at,
			       bfp.before_sha || ' -> ' || bfp.after_sha,
			       bfp.branch_name, '', bfp.before_sha, bfp.after_sha,
			       '', '',
			       '', '',
			       NULL, NULL,
			       ''
			FROM middleman_branch_force_pushes bfp
			JOIN middleman_repos r ON bfp.repo_id = r.id
		) unified
		%[2]s
		ORDER BY created_at DESC, source DESC, source_id DESC
		LIMIT ?`, branchCommitIdentityMaxBytes, where)

	args = append(args, limit)

	rows, err := d.ro.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list activity: %w", err)
	}
	defer rows.Close()

	var items []ActivityItem
	for rows.Next() {
		var it ActivityItem
		var createdAtStr string
		var authoredAtStr sql.NullString
		var committedAtStr sql.NullString
		if err := rows.Scan(
			&it.ActivityType, &it.Source, &it.SourceID,
			&it.Platform, &it.PlatformHost, &it.RepoOwner, &it.RepoName,
			&it.ItemType, &it.ItemNumber, &it.ItemTitle,
			&it.ItemURL, &it.ItemState, &it.Author, &it.ItemAuthor,
			&createdAtStr, &it.BodyPreview,
			&it.BranchName, &it.CommitSHA, &it.BeforeSHA, &it.AfterSHA,
			&it.AuthorName, &it.AuthorEmail,
			&it.CommitterName, &it.CommitterEmail,
			&authoredAtStr, &committedAtStr, &it.ActivityURL,
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

// dbTimeLayouts lists timestamp encodings that may already exist in SQLite.
// Middleman now writes UTC timestamps consistently, but older databases may
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
		createdAt.UnixMilli(), source, sourceID)
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
	ms, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return time.Time{}, "", 0,
			fmt.Errorf("invalid cursor timestamp: %w", err)
	}
	sourceID, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil {
		return time.Time{}, "", 0,
			fmt.Errorf("invalid cursor source_id: %w", err)
	}
	return time.UnixMilli(ms).UTC(), parts[1], sourceID, nil
}
