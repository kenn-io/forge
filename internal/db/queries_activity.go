package db

import (
	"context"
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
			"(item_title LIKE ? OR body_preview LIKE ?)")
		args = append(args, pattern, pattern)
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
		       item_url, item_state, author,
		       created_at, body_preview
		FROM (
			SELECT 'new_pr' AS activity_type,
			       'pr' AS source, p.id AS source_id,
			       r.platform, r.platform_host, r.owner AS repo_owner, r.name AS repo_name,
			       r.repo_path_key,
			       'pr' AS item_type, p.number AS item_number,
			       p.title AS item_title,
			       p.url AS item_url, p.state AS item_state,
			       p.author, p.created_at,
			       '' AS body_preview
			FROM middleman_merge_requests p
			JOIN middleman_repos r ON p.repo_id = r.id
			UNION ALL
			SELECT 'new_issue', 'issue', i.id,
			       r.platform, r.platform_host, r.owner, r.name, r.repo_path_key,
			       'issue', i.number, i.title,
			       i.url, i.state,
			       i.author, i.created_at,
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
			       e.author, e.created_at,
			       substr(COALESCE(e.body, ''), 1, 200)
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
			       e.author, e.created_at,
			       substr(COALESCE(e.body, ''), 1, 200)
			FROM middleman_issue_events e
			JOIN middleman_issues i ON e.issue_id = i.id
			JOIN middleman_repos r ON i.repo_id = r.id
			WHERE e.event_type = 'issue_comment'
		) unified
		%s
		ORDER BY created_at DESC, source DESC, source_id DESC
		LIMIT ?`, where)

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
		if err := rows.Scan(
			&it.ActivityType, &it.Source, &it.SourceID,
			&it.Platform, &it.PlatformHost, &it.RepoOwner, &it.RepoName,
			&it.ItemType, &it.ItemNumber, &it.ItemTitle,
			&it.ItemURL, &it.ItemState, &it.Author,
			&createdAtStr, &it.BodyPreview,
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
			if filter.PlatformHost != "" {
				host, _, _ := canonicalRepoLookupIdentifier(filter.PlatformHost, "", "")
				clauses = append(clauses, "platform_host = ?")
				*args = append(*args, host)
			}
			clauses = append(clauses, "repo_path_key = ?")
			*args = append(*args, pathKey)
		} else if filter.RepoOwner != "" && filter.RepoName != "" {
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
