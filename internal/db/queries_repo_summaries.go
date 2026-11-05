package db

import (
	"context"
	"database/sql"
	"encoding/json/v2"
	"fmt"
	"time"
)

// ListRepoSummaries returns one summary per tracked repo. The summaries are
// assembled from cached database state only; no live GitHub calls are made.
func (d *DB) ListRepoSummaries(ctx context.Context) ([]RepoSummary, error) {
	repos, err := d.ListRepos(ctx)
	if err != nil {
		return nil, err
	}

	summaries := make([]RepoSummary, 0, len(repos))
	summaryByRepoID := make(map[int64]*RepoSummary, len(repos))
	for _, repo := range repos {
		summaries = append(summaries, RepoSummary{Repo: repo})
		summaryByRepoID[repo.ID] = &summaries[len(summaries)-1]
	}

	if err := d.loadRepoSummaryStats(ctx, summaryByRepoID); err != nil {
		return nil, err
	}
	if err := d.loadRepoSummaryOverviews(ctx, summaryByRepoID); err != nil {
		return nil, err
	}
	if err := d.loadRepoSummaryAuthors(ctx, summaryByRepoID); err != nil {
		return nil, err
	}
	if err := d.loadRepoSummaryIssues(ctx, summaryByRepoID); err != nil {
		return nil, err
	}

	return summaries, nil
}

const repoSummaryStatsQuery = `
	WITH removed_pr AS (
	  SELECT mr.repo_id, COUNT(*) AS n,
	         SUM(CASE WHEN mr.state = 'open' AND NOT mr.is_locked THEN 1 ELSE 0 END) AS open_n,
	         SUM(CASE WHEN mr.state = 'open' AND NOT mr.is_locked AND mr.is_draft THEN 1 ELSE 0 END) AS draft_n
	  FROM forge_archive_items ai JOIN forge_merge_requests mr ON mr.repo_id = ai.repo_id AND mr.number = ai.item_number
	  WHERE ai.item_type = 'merge_request' AND ai.lifecycle_state = 'removed_upstream' GROUP BY mr.repo_id
	), removed_issue AS (
	  SELECT i.repo_id, COUNT(*) AS n, SUM(CASE WHEN i.state = 'open' THEN 1 ELSE 0 END) AS open_n
	  FROM forge_archive_items ai JOIN forge_issues i ON i.repo_id = ai.repo_id AND i.number = ai.item_number
	  WHERE ai.item_type = 'issue' AND ai.lifecycle_state = 'removed_upstream' GROUP BY i.repo_id
	), pr_open AS (
	  SELECT repo_id, SUM(CASE WHEN NOT is_locked THEN 1 ELSE 0 END) AS open_n,
	         SUM(CASE WHEN NOT is_locked AND is_draft THEN 1 ELSE 0 END) AS draft_n
	  FROM forge_merge_requests WHERE state = 'open' GROUP BY repo_id
	), s AS (
	  SELECT r.id, r.owner, r.name,
	    (SELECT COUNT(*) FROM forge_merge_requests WHERE repo_id = r.id) - COALESCE(rp.n, 0) AS pr_n,
	    COALESCE(o.open_n, 0) - COALESCE(rp.open_n, 0) AS pr_open_n,
	    COALESCE(o.draft_n, 0) - COALESCE(rp.draft_n, 0) AS pr_draft_n,
	    (SELECT COUNT(*) FROM forge_issues WHERE repo_id = r.id) - COALESCE(ri.n, 0) AS issue_n,
	    (SELECT COUNT(*) FROM forge_issues WHERE repo_id = r.id AND state = 'open') - COALESCE(ri.open_n, 0) AS issue_open_n,
	    (SELECT mr.last_activity_at FROM forge_merge_requests mr WHERE mr.repo_id = r.id
	       AND NOT EXISTS (SELECT 1 FROM forge_archive_items ai WHERE ai.repo_id = mr.repo_id AND ai.item_type = 'merge_request' AND ai.item_number = mr.number AND ai.lifecycle_state = 'removed_upstream')
	       ORDER BY mr.last_activity_at DESC LIMIT 1) AS pr_last,
	    (SELECT i.last_activity_at FROM forge_issues i WHERE i.repo_id = r.id
	       AND NOT EXISTS (SELECT 1 FROM forge_archive_items ai WHERE ai.repo_id = i.repo_id AND ai.item_type = 'issue' AND ai.item_number = i.number AND ai.lifecycle_state = 'removed_upstream')
	       ORDER BY i.last_activity_at DESC LIMIT 1) AS issue_last
	  FROM forge_repos r LEFT JOIN removed_pr rp ON rp.repo_id = r.id LEFT JOIN removed_issue ri ON ri.repo_id = r.id LEFT JOIN pr_open o ON o.repo_id = r.id
	)
	SELECT id, pr_n, pr_open_n, pr_draft_n, issue_n, issue_open_n,
	  CASE WHEN pr_last IS NULL THEN issue_last WHEN issue_last IS NULL THEN pr_last WHEN pr_last >= issue_last THEN pr_last ELSE issue_last END
	FROM s ORDER BY owner, name`

func (d *DB) loadRepoSummaryStats(
	ctx context.Context,
	summaryByRepoID map[int64]*RepoSummary,
) error {
	rows, err := d.roQueryContext(ctx, repoSummaryStatsQuery)
	if err != nil {
		return fmt.Errorf("list repo summary stats: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			repoID             int64
			cachedPRCount      int
			openPRCount        int
			draftPRCount       int
			cachedIssueCount   int
			openIssueCount     int
			mostRecentActivity *string
		)
		if err := rows.Scan(
			&repoID,
			&cachedPRCount,
			&openPRCount,
			&draftPRCount,
			&cachedIssueCount,
			&openIssueCount,
			&mostRecentActivity,
		); err != nil {
			return fmt.Errorf("scan repo summary stats: %w", err)
		}

		summary := summaryByRepoID[repoID]
		if summary == nil {
			continue
		}
		summary.CachedPRCount = cachedPRCount
		summary.OpenPRCount = openPRCount
		summary.DraftPRCount = draftPRCount
		summary.CachedIssueCount = cachedIssueCount
		summary.OpenIssueCount = openIssueCount
		if mostRecentActivity != nil {
			t, err := parseDBTime(*mostRecentActivity)
			if err != nil {
				return fmt.Errorf("parse repo summary activity %q: %w", *mostRecentActivity, err)
			}
			summary.MostRecentActivityAt = &t
		}
	}

	return rows.Err()
}

type repoCommitTimelineJSON struct {
	SHA         string `json:"sha"`
	Message     string `json:"message"`
	CommittedAt string `json:"committed_at"`
}

type repoReleaseJSON struct {
	TagName         string `json:"tag_name"`
	Name            string `json:"name"`
	URL             string `json:"url"`
	TargetCommitish string `json:"target_commitish"`
	Prerelease      bool   `json:"prerelease"`
	PublishedAt     string `json:"published_at,omitempty"`
}

func (d *DB) UpsertRepoOverview(
	ctx context.Context,
	repoID int64,
	overview RepoOverview,
) error {
	timeline := make([]repoCommitTimelineJSON, 0, len(overview.CommitTimeline))
	for _, point := range overview.CommitTimeline {
		timeline = append(timeline, repoCommitTimelineJSON{
			SHA:         point.SHA,
			Message:     point.Message,
			CommittedAt: point.CommittedAt.UTC().Format(time.RFC3339),
		})
	}
	timelineJSON, err := json.Marshal(timeline)
	if err != nil {
		return fmt.Errorf("marshal repo overview timeline: %w", err)
	}
	releases := make([]repoReleaseJSON, 0, len(overview.Releases))
	for _, release := range overview.Releases {
		item := repoReleaseJSON{
			TagName:         release.TagName,
			Name:            release.Name,
			URL:             release.URL,
			TargetCommitish: release.TargetCommitish,
			Prerelease:      release.Prerelease,
		}
		if release.PublishedAt != nil {
			item.PublishedAt = release.PublishedAt.UTC().Format(time.RFC3339)
		}
		releases = append(releases, item)
	}
	releasesJSON, err := json.Marshal(releases)
	if err != nil {
		return fmt.Errorf("marshal repo overview releases: %w", err)
	}

	var (
		tagName         string
		releaseName     string
		releaseURL      string
		targetCommitish string
		prerelease      bool
		publishedAt     *time.Time
	)
	if overview.LatestRelease != nil {
		tagName = overview.LatestRelease.TagName
		releaseName = overview.LatestRelease.Name
		releaseURL = overview.LatestRelease.URL
		targetCommitish = overview.LatestRelease.TargetCommitish
		prerelease = overview.LatestRelease.Prerelease
		publishedAt = overview.LatestRelease.PublishedAt
	}

	_, err = d.execContext(ctx, `
		INSERT INTO forge_repo_overviews
		    (repo_id, latest_release_tag, latest_release_name,
		     latest_release_url, latest_release_target,
		     latest_release_prerelease, latest_release_published_at,
		     commits_since_release, commit_timeline_json,
		     releases_json, timeline_updated_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(repo_id) DO UPDATE SET
		    latest_release_tag = excluded.latest_release_tag,
		    latest_release_name = excluded.latest_release_name,
		    latest_release_url = excluded.latest_release_url,
		    latest_release_target = excluded.latest_release_target,
		    latest_release_prerelease = excluded.latest_release_prerelease,
		    latest_release_published_at = excluded.latest_release_published_at,
		    commits_since_release = CASE
		        WHEN excluded.timeline_updated_at IS NOT NULL
		        THEN excluded.commits_since_release
		        WHEN forge_repo_overviews.latest_release_tag IS excluded.latest_release_tag
		        THEN COALESCE(
		            excluded.commits_since_release,
		            forge_repo_overviews.commits_since_release
		        )
		        ELSE excluded.commits_since_release
		    END,
		    commit_timeline_json = CASE
		        WHEN excluded.timeline_updated_at IS NULL
		             AND forge_repo_overviews.latest_release_tag IS excluded.latest_release_tag
		        THEN forge_repo_overviews.commit_timeline_json
		        ELSE excluded.commit_timeline_json
		    END,
		    releases_json = excluded.releases_json,
		    timeline_updated_at = CASE
		        WHEN excluded.timeline_updated_at IS NOT NULL
		        THEN excluded.timeline_updated_at
		        WHEN forge_repo_overviews.latest_release_tag IS excluded.latest_release_tag
		        THEN forge_repo_overviews.timeline_updated_at
		        ELSE excluded.timeline_updated_at
		    END,
		    updated_at = excluded.updated_at`,
		repoID,
		tagName,
		releaseName,
		releaseURL,
		targetCommitish,
		prerelease,
		nullableTime(publishedAt),
		overview.CommitsSinceRelease,
		string(timelineJSON),
		string(releasesJSON),
		nullableTime(overview.TimelineUpdatedAt),
		time.Now().UTC(),
	)
	if err != nil {
		return fmt.Errorf("upsert repo overview: %w", err)
	}
	return nil
}

func nullableTime(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.UTC()
}

func (d *DB) loadRepoSummaryOverviews(
	ctx context.Context,
	summaryByRepoID map[int64]*RepoSummary,
) error {
	rows, err := d.roQueryContext(ctx, `
		SELECT repo_id,
		       latest_release_tag,
		       latest_release_name,
		       latest_release_url,
		       latest_release_target,
		       latest_release_prerelease,
		       latest_release_published_at,
		       commits_since_release,
		       commit_timeline_json,
		       releases_json,
		       timeline_updated_at
		FROM forge_repo_overviews`,
	)
	if err != nil {
		return fmt.Errorf("list repo summary overviews: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			repoID             int64
			tagName            string
			releaseName        string
			releaseURL         string
			targetCommitish    string
			prerelease         bool
			publishedAtStr     sql.NullString
			commitsSince       sql.NullInt64
			timelineJSON       string
			releasesJSON       string
			timelineUpdatedStr sql.NullString
		)
		if err := rows.Scan(
			&repoID,
			&tagName,
			&releaseName,
			&releaseURL,
			&targetCommitish,
			&prerelease,
			&publishedAtStr,
			&commitsSince,
			&timelineJSON,
			&releasesJSON,
			&timelineUpdatedStr,
		); err != nil {
			return fmt.Errorf("scan repo summary overview: %w", err)
		}

		summary := summaryByRepoID[repoID]
		if summary == nil {
			continue
		}

		if tagName != "" {
			release := &RepoRelease{
				TagName:         tagName,
				Name:            releaseName,
				URL:             releaseURL,
				TargetCommitish: targetCommitish,
				Prerelease:      prerelease,
			}
			if publishedAtStr.Valid {
				t, err := parseDBTime(publishedAtStr.String)
				if err != nil {
					return fmt.Errorf("parse repo release published_at %q: %w", publishedAtStr.String, err)
				}
				release.PublishedAt = &t
			}
			summary.Overview.LatestRelease = release
		}
		if commitsSince.Valid {
			count := int(commitsSince.Int64)
			summary.Overview.CommitsSinceRelease = &count
		}
		releases, err := parseRepoReleasesJSON(releasesJSON)
		if err != nil {
			return fmt.Errorf("parse repo releases json: %w", err)
		}
		summary.Overview.Releases = releases
		points, err := parseRepoTimelineJSON(timelineJSON)
		if err != nil {
			return fmt.Errorf("parse repo timeline json: %w", err)
		}
		summary.Overview.CommitTimeline = points
		if timelineUpdatedStr.Valid {
			t, err := parseDBTime(timelineUpdatedStr.String)
			if err != nil {
				return fmt.Errorf("parse repo timeline updated_at %q: %w", timelineUpdatedStr.String, err)
			}
			summary.Overview.TimelineUpdatedAt = &t
		}
	}

	return rows.Err()
}

func parseRepoTimelineJSON(value string) ([]RepoCommitTimelinePoint, error) {
	if value == "" {
		return nil, nil
	}
	var raw []repoCommitTimelineJSON
	if err := json.Unmarshal([]byte(value), &raw); err != nil {
		return nil, err
	}
	points := make([]RepoCommitTimelinePoint, 0, len(raw))
	for _, item := range raw {
		t, err := time.Parse(time.RFC3339, item.CommittedAt)
		if err != nil {
			return nil, fmt.Errorf("parse commit timeline date %q: %w", item.CommittedAt, err)
		}
		points = append(points, RepoCommitTimelinePoint{
			SHA:         item.SHA,
			Message:     item.Message,
			CommittedAt: t,
		})
	}
	return points, nil
}

func parseRepoReleasesJSON(value string) ([]RepoRelease, error) {
	if value == "" {
		return nil, nil
	}
	var raw []repoReleaseJSON
	if err := json.Unmarshal([]byte(value), &raw); err != nil {
		return nil, err
	}
	releases := make([]RepoRelease, 0, len(raw))
	for _, item := range raw {
		release := RepoRelease{
			TagName:         item.TagName,
			Name:            item.Name,
			URL:             item.URL,
			TargetCommitish: item.TargetCommitish,
			Prerelease:      item.Prerelease,
		}
		if item.PublishedAt != "" {
			t, err := time.Parse(time.RFC3339, item.PublishedAt)
			if err != nil {
				return nil, fmt.Errorf("parse release date %q: %w", item.PublishedAt, err)
			}
			release.PublishedAt = &t
		}
		releases = append(releases, release)
	}
	return releases, nil
}

const repoSummaryAuthorsQuery = `
	WITH counts AS (
	  SELECT repo_id, author, COUNT(*) AS n FROM forge_merge_requests WHERE author <> '' GROUP BY repo_id, author
	  UNION ALL
	  SELECT repo_id, author, COUNT(*) FROM forge_issues WHERE author <> '' GROUP BY repo_id, author
	  UNION ALL
	  SELECT mr.repo_id, mr.author, -COUNT(*) FROM forge_archive_items ai
	  JOIN forge_merge_requests mr ON mr.repo_id = ai.repo_id AND mr.number = ai.item_number
	  WHERE ai.item_type = 'merge_request' AND ai.lifecycle_state = 'removed_upstream' AND mr.author <> ''
	  GROUP BY mr.repo_id, mr.author
	  UNION ALL
	  SELECT i.repo_id, i.author, -COUNT(*) FROM forge_archive_items ai
	  JOIN forge_issues i ON i.repo_id = ai.repo_id AND i.number = ai.item_number
	  WHERE ai.item_type = 'issue' AND ai.lifecycle_state = 'removed_upstream' AND i.author <> ''
	  GROUP BY i.repo_id, i.author
	), author_totals AS (
	  SELECT repo_id, author, SUM(n) AS item_count, MIN(n) < 0 AS has_removed FROM counts GROUP BY repo_id, author HAVING SUM(n) > 0
	), contenders AS (
	  SELECT repo_id, author, item_count, has_removed, RANK() OVER (PARTITION BY repo_id ORDER BY item_count DESC) AS by_count FROM author_totals
	), candidates AS (
	  SELECT t.repo_id, t.author, t.item_count,
	    MAX(
	      COALESCE((SELECT mr.last_activity_at FROM forge_merge_requests mr WHERE mr.repo_id = t.repo_id AND mr.author = t.author
	        AND (NOT t.has_removed OR NOT EXISTS (SELECT 1 FROM forge_archive_items ai WHERE ai.repo_id = mr.repo_id AND ai.item_type = 'merge_request' AND ai.item_number = mr.number AND ai.lifecycle_state = 'removed_upstream'))
	        ORDER BY mr.last_activity_at DESC LIMIT 1), ''),
	      COALESCE((SELECT i.last_activity_at FROM forge_issues i WHERE i.repo_id = t.repo_id AND i.author = t.author
	        AND (NOT t.has_removed OR NOT EXISTS (SELECT 1 FROM forge_archive_items ai WHERE ai.repo_id = i.repo_id AND ai.item_type = 'issue' AND ai.item_number = i.number AND ai.lifecycle_state = 'removed_upstream'))
	        ORDER BY i.last_activity_at DESC LIMIT 1), '')
	    ) AS most_recent_activity_at
	  FROM contenders t WHERE t.by_count <= 3
	), ranked AS (
	  SELECT repo_id, author, item_count, ROW_NUMBER() OVER (PARTITION BY repo_id ORDER BY item_count DESC, most_recent_activity_at DESC, author ASC) AS rank FROM candidates
	)
	SELECT repo_id, author, item_count FROM ranked WHERE rank <= 3 ORDER BY repo_id, rank`

func (d *DB) loadRepoSummaryAuthors(
	ctx context.Context,
	summaryByRepoID map[int64]*RepoSummary,
) error {
	rows, err := d.roQueryContext(ctx, repoSummaryAuthorsQuery)
	if err != nil {
		return fmt.Errorf("list repo summary authors: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			repoID    int64
			login     string
			itemCount int
		)
		if err := rows.Scan(&repoID, &login, &itemCount); err != nil {
			return fmt.Errorf("scan repo summary author: %w", err)
		}

		summary := summaryByRepoID[repoID]
		if summary == nil {
			continue
		}
		summary.ActiveAuthors = append(summary.ActiveAuthors, RepoActivityAuthor{
			Login:     login,
			ItemCount: itemCount,
		})
	}

	return rows.Err()
}

const repoSummaryIssuesQuery = `
		SELECT i.repo_id, i.number, i.title, i.author, i.state, i.url, i.last_activity_at
		FROM forge_repos r
		JOIN forge_issues i ON i.id IN (
			SELECT recent.id
			FROM forge_issues recent INDEXED BY idx_issues_repo_state_activity
			WHERE recent.repo_id = r.id AND recent.state = 'open'
			  AND NOT EXISTS (
			      SELECT 1 FROM forge_archive_items ai
			      WHERE ai.repo_id = recent.repo_id
			        AND ai.item_type = 'issue'
			        AND ai.item_number = recent.number
			        AND ai.lifecycle_state = 'removed_upstream'
			  )
			ORDER BY recent.last_activity_at DESC, recent.number DESC
			LIMIT 3
		)
		ORDER BY i.repo_id, i.last_activity_at DESC, i.number DESC`

func (d *DB) loadRepoSummaryIssues(
	ctx context.Context,
	summaryByRepoID map[int64]*RepoSummary,
) error {
	rows, err := d.roQueryContext(ctx, repoSummaryIssuesQuery)
	if err != nil {
		return fmt.Errorf("list repo summary issues: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			repoID         int64
			issue          RepoIssueHeadline
			lastActivityAt string
		)
		if err := rows.Scan(
			&repoID,
			&issue.Number,
			&issue.Title,
			&issue.Author,
			&issue.State,
			&issue.URL,
			&lastActivityAt,
		); err != nil {
			return fmt.Errorf("scan repo summary issue: %w", err)
		}
		t, err := parseDBTime(lastActivityAt)
		if err != nil {
			return fmt.Errorf("parse repo summary issue activity %q: %w", lastActivityAt, err)
		}
		issue.LastActivityAt = t

		summary := summaryByRepoID[repoID]
		if summary == nil {
			continue
		}
		summary.RecentIssues = append(summary.RecentIssues, issue)
	}

	return rows.Err()
}
