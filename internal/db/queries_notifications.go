package db

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

const (
	defaultNotificationLimit = 50
	maxNotificationLimit     = 200
)

func canonicalizeRequiredNotificationPlatform(platform string) (string, error) {
	platform = strings.ToLower(strings.TrimSpace(platform))
	if platform == "" {
		return "", fmt.Errorf("notification platform is required")
	}
	return platform, nil
}

func canonicalizeNotificationPlatformHost(platform, host string) (string, string, error) {
	canonicalPlatform, err := canonicalizeRequiredNotificationPlatform(platform)
	if err != nil {
		return "", "", err
	}
	canonicalHost, _, _ := canonicalRepoIdentifier(host, "", "")
	return canonicalPlatform, canonicalHost, nil
}

func canonicalizeNotification(n *Notification) error {
	if n == nil {
		return nil
	}
	platform, err := canonicalizeRequiredNotificationPlatform(n.Platform)
	if err != nil {
		return err
	}
	n.Platform = platform
	n.PlatformHost, n.RepoOwner, n.RepoName = canonicalRepoIdentifier(n.PlatformHost, n.RepoOwner, n.RepoName)
	n.SourceUpdatedAt = canonicalUTCTime(n.SourceUpdatedAt)
	n.SourceLastAcknowledgedAt = canonicalUTCTimePtr(n.SourceLastAcknowledgedAt)
	n.SyncedAt = canonicalUTCTime(n.SyncedAt)
	n.DoneAt = canonicalUTCTimePtr(n.DoneAt)
	n.SourceAckQueuedAt = canonicalUTCTimePtr(n.SourceAckQueuedAt)
	n.SourceAckSyncedAt = canonicalUTCTimePtr(n.SourceAckSyncedAt)
	n.SourceAckGenerationAt = canonicalUTCTimePtr(n.SourceAckGenerationAt)
	n.SourceAckLastAttemptAt = canonicalUTCTimePtr(n.SourceAckLastAttemptAt)
	n.SourceAckNextAttemptAt = canonicalUTCTimePtr(n.SourceAckNextAttemptAt)
	if n.ItemType == "" {
		n.ItemType = "other"
	}
	if !n.Unread && n.SourceAckGenerationAt == nil && !n.SourceUpdatedAt.IsZero() {
		generation := n.SourceUpdatedAt
		n.SourceAckGenerationAt = &generation
	}
	return nil
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func intBool(v int) bool { return v != 0 }

func nullableInt64(v *int64) any {
	if v == nil {
		return nil
	}
	return *v
}

func nullableInt(v *int) any {
	if v == nil {
		return nil
	}
	return *v
}

func nullableNotificationTime(v *time.Time) any {
	if v == nil {
		return nil
	}
	return canonicalUTCTime(*v)
}

func scanNotification(scanner interface{ Scan(dest ...any) error }) (Notification, error) {
	var n Notification
	var repoID sql.NullInt64
	var itemNumber sql.NullInt64
	var sourceUpdatedAt string
	var syncedAt string
	var lastRead sql.NullString
	var doneAt sql.NullString
	var queuedAt sql.NullString
	var readSyncedAt sql.NullString
	var readGenerationAt sql.NullString
	var lastAttemptAt sql.NullString
	var nextAttemptAt sql.NullString
	var unread int
	var participating int
	err := scanner.Scan(
		&n.ID, &n.Platform, &n.PlatformHost, &n.PlatformNotificationID, &repoID, &n.RepoOwner, &n.RepoName,
		&n.SubjectType, &n.SubjectTitle, &n.SubjectURL, &n.SubjectLatestCommentURL, &n.WebURL,
		&itemNumber, &n.ItemType, &n.ItemAuthor, &n.Reason, &unread, &participating,
		&sourceUpdatedAt, &lastRead, &syncedAt, &doneAt, &n.DoneReason,
		&queuedAt, &readSyncedAt, &readGenerationAt, &n.SourceAckError, &n.SourceAckAttempts, &lastAttemptAt, &nextAttemptAt,
	)
	if err != nil {
		return Notification{}, err
	}
	if repoID.Valid {
		n.RepoID = &repoID.Int64
	}
	if itemNumber.Valid {
		value := int(itemNumber.Int64)
		n.ItemNumber = &value
	}
	if sourceUpdatedAt != "" {
		t, err := parseDBTime(sourceUpdatedAt)
		if err != nil {
			return Notification{}, fmt.Errorf("parse notification source_updated_at: %w", err)
		}
		n.SourceUpdatedAt = t
	}
	if syncedAt != "" {
		t, err := parseDBTime(syncedAt)
		if err != nil {
			return Notification{}, fmt.Errorf("parse notification synced_at: %w", err)
		}
		n.SyncedAt = t
	}
	if n.SourceLastAcknowledgedAt, err = parseNullableNotificationTime("last_read_at", lastRead); err != nil {
		return Notification{}, err
	}
	if n.DoneAt, err = parseNullableNotificationTime("done_at", doneAt); err != nil {
		return Notification{}, err
	}
	if n.SourceAckQueuedAt, err = parseNullableNotificationTime("source_ack_queued_at", queuedAt); err != nil {
		return Notification{}, err
	}
	if n.SourceAckSyncedAt, err = parseNullableNotificationTime("source_ack_synced_at", readSyncedAt); err != nil {
		return Notification{}, err
	}
	if n.SourceAckGenerationAt, err = parseNullableNotificationTime("source_ack_generation_at", readGenerationAt); err != nil {
		return Notification{}, err
	}
	if n.SourceAckLastAttemptAt, err = parseNullableNotificationTime("source_ack_last_attempt_at", lastAttemptAt); err != nil {
		return Notification{}, err
	}
	if n.SourceAckNextAttemptAt, err = parseNullableNotificationTime("source_ack_next_attempt_at", nextAttemptAt); err != nil {
		return Notification{}, err
	}
	n.Unread = intBool(unread)
	n.Participating = intBool(participating)
	return n, nil
}

func parseNullableNotificationTime(field string, value sql.NullString) (*time.Time, error) {
	if !value.Valid {
		return nil, nil
	}
	t, err := parseDBTime(value.String)
	if err != nil {
		return nil, fmt.Errorf("parse notification %s: %w", field, err)
	}
	return &t, nil
}

const notificationSelectColumns = `n.id, n.platform, n.platform_host, n.platform_notification_id, n.repo_id, n.repo_owner, n.repo_name,
	n.subject_type, n.subject_title, n.subject_url, n.subject_latest_comment_url, n.web_url,
	n.item_number, n.item_type, n.item_author, n.reason, n.unread, n.participating,
	n.source_updated_at, n.source_last_acknowledged_at, n.synced_at, n.done_at, n.done_reason,
	n.source_ack_queued_at, n.source_ack_synced_at, n.source_ack_generation_at, n.source_ack_error, n.source_ack_attempts,
	n.source_ack_last_attempt_at, n.source_ack_next_attempt_at`

func (d *DB) UpsertNotifications(ctx context.Context, notifications []Notification) error {
	if len(notifications) == 0 {
		return nil
	}
	return d.Tx(ctx, func(tx *sql.Tx) error {
		for i := range notifications {
			n := notifications[i]
			if err := canonicalizeNotification(&n); err != nil {
				return err
			}
			if n.SyncedAt.IsZero() {
				n.SyncedAt = time.Now().UTC()
			}
			var repoID *int64
			if n.RepoID != nil {
				repoID = n.RepoID
			} else if id, found, err := lookupNotificationRepoIDTx(ctx, tx, n.Platform, n.PlatformHost, n.RepoOwner, n.RepoName); err != nil {
				return err
			} else if found {
				repoID = &id
			}

			_, err := tx.ExecContext(ctx, `
				INSERT INTO middleman_notification_items (
					platform, platform_host, platform_notification_id, repo_id, repo_owner, repo_name,
					subject_type, subject_title, subject_url, subject_latest_comment_url, web_url,
					item_number, item_type, item_author, reason, unread, participating,
					source_updated_at, source_last_acknowledged_at, synced_at, done_at, done_reason,
					source_ack_queued_at, source_ack_synced_at, source_ack_generation_at, source_ack_error, source_ack_attempts,
					source_ack_last_attempt_at, source_ack_next_attempt_at
				) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
				ON CONFLICT(platform, platform_host, platform_notification_id) DO UPDATE SET
					repo_id = COALESCE(excluded.repo_id, middleman_notification_items.repo_id),
					platform = excluded.platform,
					repo_owner = excluded.repo_owner,
					repo_name = excluded.repo_name,
					subject_type = excluded.subject_type,
					subject_title = excluded.subject_title,
					subject_url = excluded.subject_url,
					subject_latest_comment_url = excluded.subject_latest_comment_url,
					web_url = excluded.web_url,
					item_number = excluded.item_number,
					item_type = excluded.item_type,
					item_author = excluded.item_author,
					reason = excluded.reason,
					unread = CASE
						WHEN excluded.unread = 1
						 AND middleman_notification_items.source_ack_generation_at IS NOT NULL
						 AND excluded.source_updated_at <= middleman_notification_items.source_ack_generation_at THEN 0
						WHEN excluded.unread = 1
						 AND middleman_notification_items.source_ack_queued_at IS NOT NULL
						 AND middleman_notification_items.source_ack_synced_at IS NULL
						 AND excluded.source_updated_at <= COALESCE(middleman_notification_items.source_ack_generation_at, middleman_notification_items.source_ack_queued_at) THEN 0
						ELSE excluded.unread
					END,
					participating = excluded.participating,
					source_updated_at = excluded.source_updated_at,
					source_last_acknowledged_at = CASE
						WHEN excluded.unread = 1
						 AND middleman_notification_items.source_ack_generation_at IS NOT NULL
						 AND excluded.source_updated_at <= middleman_notification_items.source_ack_generation_at THEN middleman_notification_items.source_last_acknowledged_at
						ELSE excluded.source_last_acknowledged_at
					END,
					synced_at = excluded.synced_at,
					done_at = CASE
						WHEN excluded.unread = 1
						 AND middleman_notification_items.done_at IS NOT NULL
						 AND excluded.source_updated_at > COALESCE(middleman_notification_items.source_ack_generation_at, middleman_notification_items.done_at) THEN NULL
						ELSE middleman_notification_items.done_at
					END,
					done_reason = CASE
						WHEN excluded.unread = 1
						 AND middleman_notification_items.done_at IS NOT NULL
						 AND excluded.source_updated_at > COALESCE(middleman_notification_items.source_ack_generation_at, middleman_notification_items.done_at) THEN ''
						ELSE middleman_notification_items.done_reason
					END,
					source_ack_queued_at = CASE
						WHEN excluded.unread = 0 THEN NULL
						WHEN excluded.unread = 1
						 AND middleman_notification_items.source_ack_queued_at IS NOT NULL
						 AND middleman_notification_items.source_ack_synced_at IS NULL
						 AND excluded.source_updated_at > COALESCE(middleman_notification_items.source_ack_generation_at, middleman_notification_items.source_ack_queued_at) THEN NULL
						WHEN excluded.unread = 1 AND middleman_notification_items.done_at IS NOT NULL AND excluded.source_updated_at > COALESCE(middleman_notification_items.source_ack_generation_at, middleman_notification_items.done_at) THEN NULL
						ELSE middleman_notification_items.source_ack_queued_at
					END,
					source_ack_synced_at = CASE
						WHEN excluded.unread = 0 THEN COALESCE(excluded.source_last_acknowledged_at, excluded.synced_at)
						WHEN excluded.unread = 1
						 AND middleman_notification_items.source_ack_generation_at IS NOT NULL
						 AND excluded.source_updated_at > middleman_notification_items.source_ack_generation_at THEN NULL
						WHEN excluded.unread = 1
						 AND middleman_notification_items.source_ack_queued_at IS NOT NULL
						 AND middleman_notification_items.source_ack_synced_at IS NULL
						 AND excluded.source_updated_at > COALESCE(middleman_notification_items.source_ack_generation_at, middleman_notification_items.source_ack_queued_at) THEN NULL
						WHEN excluded.unread = 1 AND middleman_notification_items.done_at IS NOT NULL AND excluded.source_updated_at > COALESCE(middleman_notification_items.source_ack_generation_at, middleman_notification_items.done_at) THEN NULL
						ELSE middleman_notification_items.source_ack_synced_at
					END,
					source_ack_error = CASE
						WHEN excluded.unread = 0 THEN ''
						WHEN excluded.unread = 1
						 AND middleman_notification_items.source_ack_generation_at IS NOT NULL
						 AND excluded.source_updated_at > middleman_notification_items.source_ack_generation_at THEN ''
						WHEN excluded.unread = 1
						 AND middleman_notification_items.source_ack_queued_at IS NOT NULL
						 AND middleman_notification_items.source_ack_synced_at IS NULL
						 AND excluded.source_updated_at > COALESCE(middleman_notification_items.source_ack_generation_at, middleman_notification_items.source_ack_queued_at) THEN ''
						WHEN excluded.unread = 1 AND middleman_notification_items.done_at IS NOT NULL AND excluded.source_updated_at > COALESCE(middleman_notification_items.source_ack_generation_at, middleman_notification_items.done_at) THEN ''
						ELSE middleman_notification_items.source_ack_error
					END,
					source_ack_attempts = CASE
						WHEN excluded.unread = 0 THEN 0
						WHEN excluded.unread = 1
						 AND middleman_notification_items.source_ack_generation_at IS NOT NULL
						 AND excluded.source_updated_at > middleman_notification_items.source_ack_generation_at THEN 0
						WHEN excluded.unread = 1
						 AND middleman_notification_items.source_ack_queued_at IS NOT NULL
						 AND middleman_notification_items.source_ack_synced_at IS NULL
						 AND excluded.source_updated_at > COALESCE(middleman_notification_items.source_ack_generation_at, middleman_notification_items.source_ack_queued_at) THEN 0
						WHEN excluded.unread = 1 AND middleman_notification_items.done_at IS NOT NULL AND excluded.source_updated_at > COALESCE(middleman_notification_items.source_ack_generation_at, middleman_notification_items.done_at) THEN 0
						ELSE middleman_notification_items.source_ack_attempts
					END,
					source_ack_last_attempt_at = CASE
						WHEN excluded.unread = 0 THEN NULL
						WHEN excluded.unread = 1
						 AND middleman_notification_items.source_ack_generation_at IS NOT NULL
						 AND excluded.source_updated_at > middleman_notification_items.source_ack_generation_at THEN NULL
						WHEN excluded.unread = 1
						 AND middleman_notification_items.source_ack_queued_at IS NOT NULL
						 AND middleman_notification_items.source_ack_synced_at IS NULL
						 AND excluded.source_updated_at > COALESCE(middleman_notification_items.source_ack_generation_at, middleman_notification_items.source_ack_queued_at) THEN NULL
						ELSE middleman_notification_items.source_ack_last_attempt_at
					END,
					source_ack_next_attempt_at = CASE
						WHEN excluded.unread = 0 THEN NULL
						WHEN excluded.unread = 1
						 AND middleman_notification_items.source_ack_generation_at IS NOT NULL
						 AND excluded.source_updated_at > middleman_notification_items.source_ack_generation_at THEN NULL
						WHEN excluded.unread = 1
						 AND middleman_notification_items.source_ack_queued_at IS NOT NULL
						 AND middleman_notification_items.source_ack_synced_at IS NULL
						 AND excluded.source_updated_at > COALESCE(middleman_notification_items.source_ack_generation_at, middleman_notification_items.source_ack_queued_at) THEN NULL
						ELSE middleman_notification_items.source_ack_next_attempt_at
					END,
					source_ack_generation_at = CASE
						WHEN excluded.unread = 0 THEN excluded.source_updated_at
						WHEN middleman_notification_items.source_ack_generation_at IS NOT NULL
						 AND excluded.source_updated_at > middleman_notification_items.source_ack_generation_at THEN NULL
						ELSE middleman_notification_items.source_ack_generation_at
					END`,
				n.Platform, n.PlatformHost, n.PlatformNotificationID, nullableInt64(repoID), n.RepoOwner, n.RepoName,
				n.SubjectType, n.SubjectTitle, n.SubjectURL, n.SubjectLatestCommentURL, n.WebURL,
				nullableInt(n.ItemNumber), n.ItemType, n.ItemAuthor, n.Reason, boolInt(n.Unread), boolInt(n.Participating),
				n.SourceUpdatedAt, nullableNotificationTime(n.SourceLastAcknowledgedAt), n.SyncedAt, nullableNotificationTime(n.DoneAt), n.DoneReason,
				nullableNotificationTime(n.SourceAckQueuedAt), nullableNotificationTime(n.SourceAckSyncedAt), nullableNotificationTime(n.SourceAckGenerationAt), n.SourceAckError, n.SourceAckAttempts,
				nullableNotificationTime(n.SourceAckLastAttemptAt), nullableNotificationTime(n.SourceAckNextAttemptAt),
			)
			if err != nil {
				return fmt.Errorf("upsert notification %s: %w", n.PlatformNotificationID, err)
			}
		}
		return nil
	})
}

func (d *DB) FilterNotificationIDs(ctx context.Context, ids []int64, repos []NotificationRepoFilter) ([]int64, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	where, args, err := notificationWhere(ListNotificationsOpts{State: "all", Repos: repos})
	if err != nil {
		return nil, err
	}
	for _, id := range ids {
		args = append(args, id)
	}
	rows, err := d.ro.QueryContext(ctx, fmt.Sprintf("SELECT n.id FROM middleman_notification_items n WHERE %s AND n.id IN (%s)", where, sqlPlaceholders(len(ids))), args...)
	if err != nil {
		return nil, fmt.Errorf("filter notification ids: %w", err)
	}
	return scanReturnedNotificationIDs(rows, "filter notification")
}

func lookupNotificationRepoIDTx(ctx context.Context, tx *sql.Tx, platform, host, owner, name string) (int64, bool, error) {
	var id int64
	err := tx.QueryRowContext(ctx, `
		SELECT id FROM middleman_repos
		WHERE platform = ? AND platform_host = ? AND owner = ? AND name = ?`, platform, host, owner, name).Scan(&id)
	if err == nil {
		return id, true, nil
	}
	if err == sql.ErrNoRows {
		return 0, false, nil
	}
	return 0, false, fmt.Errorf("lookup notification repo: %w", err)
}

func notificationWhere(opts ListNotificationsOpts) (string, []any, error) {
	clauses := []string{}
	args := []any{}
	if len(opts.Repos) > 0 {
		repoClauses := make([]string, 0, len(opts.Repos))
		seen := make(map[string]struct{}, len(opts.Repos))
		for _, repo := range opts.Repos {
			platform, err := canonicalizeRequiredNotificationPlatform(repo.Platform)
			if err != nil {
				return "", nil, err
			}
			host, owner, name := canonicalRepoIdentifier(repo.PlatformHost, repo.RepoOwner, repo.RepoName)
			if owner == "" || name == "" {
				continue
			}
			key := platform + "\x00" + host + "\x00" + owner + "\x00" + name
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			repoClauses = append(repoClauses, "(n.platform = ? AND n.platform_host = ? AND n.repo_owner = ? AND n.repo_name = ?)")
			args = append(args, platform, host, owner, name)
		}
		if len(repoClauses) == 0 {
			clauses = append(clauses, "0 = 1")
		} else {
			clauses = append(clauses, "("+strings.Join(repoClauses, " OR ")+")")
		}
	} else {
		clauses = append(clauses, `EXISTS (
			SELECT 1 FROM middleman_repos r
			WHERE r.platform = n.platform
			  AND r.platform_host = n.platform_host
			  AND r.owner = n.repo_owner
			  AND r.name = n.repo_name
		)`)
	}
	if opts.Platform != "" {
		platform, err := canonicalizeRequiredNotificationPlatform(opts.Platform)
		if err != nil {
			return "", nil, err
		}
		clauses = append(clauses, "n.platform = ?")
		args = append(args, platform)
	}
	if opts.PlatformHost != "" {
		host, _, _ := canonicalRepoIdentifier(opts.PlatformHost, "", "")
		clauses = append(clauses, "n.platform_host = ?")
		args = append(args, host)
	}
	if opts.RepoOwner != "" {
		clauses = append(clauses, "n.repo_owner = ?")
		args = append(args, strings.ToLower(opts.RepoOwner))
	}
	if opts.RepoName != "" {
		clauses = append(clauses, "n.repo_name = ?")
		args = append(args, strings.ToLower(opts.RepoName))
	}
	switch opts.State {
	case "", "unread":
		clauses = append(clauses, "n.done_at IS NULL", "n.unread = 1")
	case "active":
		clauses = append(clauses, "n.done_at IS NULL")
	case "read":
		clauses = append(clauses, "n.done_at IS NULL", "n.unread = 0")
	case "done":
		clauses = append(clauses, "n.done_at IS NOT NULL")
	case "all":
	default:
		clauses = append(clauses, "n.done_at IS NULL", "n.unread = 1")
	}
	if len(opts.Reasons) > 0 {
		clauses = append(clauses, "n.reason IN ("+sqlPlaceholders(len(opts.Reasons))+")")
		for _, reason := range opts.Reasons {
			args = append(args, reason)
		}
	}
	if len(opts.ItemTypes) > 0 {
		clauses = append(clauses, "n.item_type IN ("+sqlPlaceholders(len(opts.ItemTypes))+")")
		for _, itemType := range opts.ItemTypes {
			args = append(args, itemType)
		}
	}
	if search := strings.TrimSpace(opts.Search); search != "" {
		clauses = append(clauses, `(lower(n.subject_title) LIKE ? OR lower(n.repo_owner || '/' || n.repo_name) LIKE ? OR lower(n.item_author) LIKE ? OR CAST(n.item_number AS TEXT) = ?)`)
		like := "%" + strings.ToLower(search) + "%"
		args = append(args, like, like, like, search)
	}
	return strings.Join(clauses, " AND "), args, nil
}

func notificationOrder(sort string) string {
	switch sort {
	case "updated_asc":
		return "n.source_updated_at ASC, n.id ASC"
	case "repo":
		return "n.repo_owner ASC, n.repo_name ASC, n.source_updated_at DESC, n.id DESC"
	case "priority":
		return `CASE n.reason
			WHEN 'mention' THEN 0
			WHEN 'team_mention' THEN 1
			WHEN 'review_requested' THEN 2
			WHEN 'assign' THEN 3
			WHEN 'author' THEN 4
			WHEN 'comment' THEN 5
			WHEN 'subscribed' THEN 6
			WHEN 'manual' THEN 7
			ELSE 8 END ASC, n.unread DESC, n.source_updated_at DESC, n.id DESC`
	default:
		return "n.source_updated_at DESC, n.id DESC"
	}
}

func normalizedNotificationLimit(limit int) int {
	if limit <= 0 {
		return defaultNotificationLimit
	}
	if limit > maxNotificationLimit {
		return maxNotificationLimit
	}
	return limit
}

func (d *DB) ListNotifications(ctx context.Context, opts ListNotificationsOpts) ([]Notification, error) {
	where, args, err := notificationWhere(opts)
	if err != nil {
		return nil, err
	}
	limit := normalizedNotificationLimit(opts.Limit)
	if opts.Offset < 0 {
		opts.Offset = 0
	}
	query := fmt.Sprintf("SELECT %s FROM middleman_notification_items n WHERE %s ORDER BY %s LIMIT ? OFFSET ?", notificationSelectColumns, where, notificationOrder(opts.Sort))
	args = append(args, limit, opts.Offset)
	rows, err := d.ro.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list notifications: %w", err)
	}
	defer rows.Close()
	var notifications []Notification
	for rows.Next() {
		n, err := scanNotification(rows)
		if err != nil {
			return nil, fmt.Errorf("scan notification: %w", err)
		}
		notifications = append(notifications, n)
	}
	return notifications, rows.Err()
}

func (d *DB) NotificationSummary(ctx context.Context, opts ListNotificationsOpts) (NotificationSummary, error) {
	opts.State = "all"
	where, args, err := notificationWhere(opts)
	if err != nil {
		return NotificationSummary{}, err
	}
	summary := NotificationSummary{ByReason: map[string]int{}, ByRepo: map[string]int{}}
	row := d.ro.QueryRowContext(ctx, fmt.Sprintf(`SELECT
		COALESCE(SUM(CASE WHEN n.done_at IS NULL THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN n.done_at IS NULL AND n.unread = 1 THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(CASE WHEN n.done_at IS NOT NULL THEN 1 ELSE 0 END), 0)
		FROM middleman_notification_items n WHERE %s`, where), args...)
	if err := row.Scan(&summary.TotalActive, &summary.Unread, &summary.Done); err != nil {
		return summary, fmt.Errorf("notification summary totals: %w", err)
	}
	if err := scanNotificationCounts(ctx, d.ro, fmt.Sprintf("SELECT n.reason, COUNT(*) FROM middleman_notification_items n WHERE %s GROUP BY n.reason", where), args, summary.ByReason); err != nil {
		return summary, err
	}
	if err := scanNotificationCounts(ctx, d.ro, fmt.Sprintf("SELECT n.platform_host || '/' || n.repo_owner || '/' || n.repo_name, COUNT(*) FROM middleman_notification_items n WHERE %s GROUP BY n.platform_host, n.repo_owner, n.repo_name", where), args, summary.ByRepo); err != nil {
		return summary, err
	}
	return summary, nil
}

func scanNotificationCounts(ctx context.Context, q queryer, query string, args []any, out map[string]int) error {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("notification summary counts: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var key string
		var count int
		if err := rows.Scan(&key, &count); err != nil {
			return fmt.Errorf("scan notification count: %w", err)
		}
		out[key] = count
	}
	return rows.Err()
}

type queryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

func (d *DB) MarkNotificationsDone(ctx context.Context, ids []int64, doneAt time.Time, markRead bool) ([]int64, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	doneAt = canonicalUTCTime(doneAt)
	args := make([]any, 0, len(ids)+2)
	args = append(args, doneAt)
	setRead := ""
	if markRead {
		setRead = ", unread = 0, source_ack_queued_at = ?, source_ack_synced_at = NULL, source_ack_generation_at = source_updated_at, source_ack_error = '', source_ack_attempts = 0, source_ack_last_attempt_at = NULL, source_ack_next_attempt_at = NULL"
		args = append(args, doneAt)
	}
	for _, id := range ids {
		args = append(args, id)
	}
	rows, err := d.rw.QueryContext(ctx, fmt.Sprintf("UPDATE middleman_notification_items SET done_at = ?, done_reason = CASE WHEN done_reason = '' THEN 'user' ELSE done_reason END%s WHERE id IN (%s) RETURNING id", setRead, sqlPlaceholders(len(ids))), args...)
	if err != nil {
		return nil, fmt.Errorf("mark notifications done: %w", err)
	}
	return scanReturnedNotificationIDs(rows, "mark notifications done")
}

func (d *DB) MarkNotificationsUndone(ctx context.Context, ids []int64) ([]int64, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	args := make([]any, 0, len(ids))
	for _, id := range ids {
		args = append(args, id)
	}
	rows, err := d.rw.QueryContext(ctx, fmt.Sprintf("UPDATE middleman_notification_items SET done_at = NULL, done_reason = '' WHERE id IN (%s) RETURNING id", sqlPlaceholders(len(ids))), args...)
	if err != nil {
		return nil, fmt.Errorf("mark notifications undone: %w", err)
	}
	return scanReturnedNotificationIDs(rows, "mark notifications undone")
}

func (d *DB) MarkNotificationIDsReadLocal(ctx context.Context, ids []int64) ([]int64, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	args := make([]any, 0, len(ids))
	for _, id := range ids {
		args = append(args, id)
	}
	rows, err := d.rw.QueryContext(ctx, fmt.Sprintf(`UPDATE middleman_notification_items
		SET unread = 0, source_ack_queued_at = NULL, source_ack_synced_at = NULL, source_ack_generation_at = NULL,
		    source_ack_error = '', source_ack_attempts = 0, source_ack_last_attempt_at = NULL, source_ack_next_attempt_at = NULL
		WHERE id IN (%s)
		RETURNING id`, sqlPlaceholders(len(ids))), args...)
	if err != nil {
		return nil, fmt.Errorf("mark notification ids read local: %w", err)
	}
	return scanReturnedNotificationIDs(rows, "mark notification ids read local")
}

func (d *DB) QueueNotificationIDsRead(ctx context.Context, ids []int64, readAt time.Time) ([]int64, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	readAt = canonicalUTCTime(readAt)
	args := []any{readAt}
	for _, id := range ids {
		args = append(args, id)
	}
	rows, err := d.rw.QueryContext(ctx, fmt.Sprintf(`UPDATE middleman_notification_items
		SET unread = 0, source_ack_queued_at = ?, source_ack_synced_at = NULL, source_ack_generation_at = source_updated_at,
		    source_ack_error = '', source_ack_attempts = 0, source_ack_last_attempt_at = NULL, source_ack_next_attempt_at = NULL
		WHERE id IN (%s)
		RETURNING id`, sqlPlaceholders(len(ids))), args...)
	if err != nil {
		return nil, fmt.Errorf("queue notification ids read: %w", err)
	}
	return scanReturnedNotificationIDs(rows, "queue notification ids read")
}

func scanReturnedNotificationIDs(rows *sql.Rows, action string) ([]int64, error) {
	defer rows.Close()
	ids := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan %s id: %w", action, err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("scan %s ids: %w", action, err)
	}
	return ids, nil
}

func (d *DB) GetNotificationSyncWatermark(ctx context.Context, platform, host string, trackedReposKey string) (*NotificationSyncWatermark, error) {
	var err error
	platform, host, err = canonicalizeNotificationPlatformHost(platform, host)
	if err != nil {
		return nil, err
	}
	var rawLastSuccessful string
	var rawLastFull sql.NullString
	var syncCursor string
	var storedTrackedReposKey string
	err = d.ro.QueryRowContext(ctx, `
		SELECT last_successful_sync_at, last_full_sync_at, sync_cursor, tracked_repos_key
		FROM middleman_notification_sync_watermarks
		WHERE platform = ? AND platform_host = ?`, platform, host).Scan(&rawLastSuccessful, &rawLastFull, &syncCursor, &storedTrackedReposKey)
	if err == nil {
		if storedTrackedReposKey != trackedReposKey {
			return nil, nil
		}
		lastSuccessful, parseErr := parseDBTime(rawLastSuccessful)
		if parseErr != nil {
			return nil, fmt.Errorf("parse notification sync watermark: %w", parseErr)
		}
		state := NotificationSyncWatermark{
			Platform:             platform,
			LastSuccessfulSyncAt: canonicalUTCTime(lastSuccessful),
			SyncCursor:           syncCursor,
			TrackedReposKey:      storedTrackedReposKey,
		}
		if rawLastFull.Valid && rawLastFull.String != "" {
			lastFull, parseErr := parseDBTime(rawLastFull.String)
			if parseErr != nil {
				return nil, fmt.Errorf("parse notification full sync watermark: %w", parseErr)
			}
			lastFull = canonicalUTCTime(lastFull)
			state.LastFullSyncAt = &lastFull
		}
		return &state, nil
	}
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return nil, fmt.Errorf("get notification sync watermark: %w", err)
}

func (d *DB) UpdateNotificationSyncWatermark(ctx context.Context, platform, host string, syncedAt time.Time, lastFullSyncedAt *time.Time, syncCursor string, trackedReposKey string) error {
	var err error
	platform, host, err = canonicalizeNotificationPlatformHost(platform, host)
	if err != nil {
		return err
	}
	syncedAt = canonicalUTCTime(syncedAt)
	lastFullValue := nullableNotificationTime(lastFullSyncedAt)
	_, err = d.rw.ExecContext(ctx, `
		INSERT INTO middleman_notification_sync_watermarks (platform, platform_host, last_successful_sync_at, last_full_sync_at, sync_cursor, tracked_repos_key)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(platform, platform_host) DO UPDATE SET
			last_successful_sync_at = excluded.last_successful_sync_at,
			last_full_sync_at = excluded.last_full_sync_at,
			sync_cursor = excluded.sync_cursor,
			tracked_repos_key = excluded.tracked_repos_key`, platform, host, syncedAt, lastFullValue, syncCursor, trackedReposKey)
	if err != nil {
		return fmt.Errorf("update notification sync watermark: %w", err)
	}
	return nil
}

func (d *DB) MarkNotificationsAcknowledged(ctx context.Context, platform, host string, notificationIDs []string, acknowledgedAt time.Time) error {
	if len(notificationIDs) == 0 {
		return nil
	}
	var err error
	platform, host, err = canonicalizeNotificationPlatformHost(platform, host)
	if err != nil {
		return err
	}
	acknowledgedAt = canonicalUTCTime(acknowledgedAt)
	args := []any{acknowledgedAt, acknowledgedAt, platform, host}
	for _, id := range notificationIDs {
		args = append(args, id)
	}
	_, err = d.rw.ExecContext(ctx, fmt.Sprintf(`UPDATE middleman_notification_items
		SET unread = 0, source_last_acknowledged_at = ?, source_ack_synced_at = ?, source_ack_queued_at = NULL, source_ack_generation_at = NULL,
		    source_ack_error = '', source_ack_attempts = 0, source_ack_last_attempt_at = NULL, source_ack_next_attempt_at = NULL
		WHERE platform = ? AND platform_host = ? AND platform_notification_id IN (%s)`, sqlPlaceholders(len(notificationIDs))), args...)
	if err != nil {
		return fmt.Errorf("mark notifications acknowledged: %w", err)
	}
	return nil
}

func (d *DB) ListQueuedNotificationAcks(ctx context.Context, platform, host string, limit int, now time.Time) ([]Notification, error) {
	var err error
	platform, host, err = canonicalizeNotificationPlatformHost(platform, host)
	if err != nil {
		return nil, err
	}
	limit = normalizedNotificationLimit(limit)
	rows, err := d.ro.QueryContext(ctx, fmt.Sprintf(`SELECT %s FROM middleman_notification_items n
		WHERE n.platform = ?
		  AND n.platform_host = ?
		  AND n.source_ack_queued_at IS NOT NULL
		  AND n.source_ack_synced_at IS NULL
		  AND n.source_ack_error != 'max_attempts_exceeded'
		  AND COALESCE(n.source_ack_next_attempt_at, n.source_ack_queued_at) <= ?
		ORDER BY n.source_ack_queued_at ASC, n.id ASC LIMIT ?`, notificationSelectColumns), platform, host, canonicalUTCTime(now), limit)
	if err != nil {
		return nil, fmt.Errorf("list queued notification acks: %w", err)
	}
	defer rows.Close()
	var notifications []Notification
	for rows.Next() {
		n, err := scanNotification(rows)
		if err != nil {
			return nil, fmt.Errorf("scan queued notification ack: %w", err)
		}
		notifications = append(notifications, n)
	}
	return notifications, rows.Err()
}

func (d *DB) NotificationAckPropagationCurrent(ctx context.Context, id int64, queuedAt *time.Time, sourceUpdatedAt time.Time) (bool, error) {
	var matched int
	err := d.ro.QueryRowContext(ctx, `SELECT 1 FROM middleman_notification_items
		WHERE id = ?
		  AND source_ack_queued_at = ?
		  AND source_updated_at = ?
		  AND source_ack_synced_at IS NULL
		  AND source_ack_error != 'max_attempts_exceeded'`, id, nullableNotificationTime(queuedAt), canonicalUTCTime(sourceUpdatedAt)).Scan(&matched)
	if err == nil {
		return true, nil
	}
	if err == sql.ErrNoRows {
		return false, nil
	}
	return false, fmt.Errorf("check notification ack propagation generation: %w", err)
}

func (d *DB) MarkNotificationAckPropagationResult(ctx context.Context, id int64, queuedAt *time.Time, sourceUpdatedAt time.Time, syncedAt *time.Time, errText string, nextAttemptAt *time.Time) error {
	if syncedAt != nil {
		synced := canonicalUTCTime(*syncedAt)
		queuedAtValue := nullableNotificationTime(queuedAt)
		sourceUpdatedAt = canonicalUTCTime(sourceUpdatedAt)
		_, err := d.rw.ExecContext(ctx, `UPDATE middleman_notification_items
			SET unread = 0,
			    source_last_acknowledged_at = ?,
			    source_ack_synced_at = ?,
			    source_ack_generation_at = ?,
			    source_ack_queued_at = NULL,
			    source_ack_error = '',
			    source_ack_attempts = 0,
			    source_ack_last_attempt_at = NULL,
			    source_ack_next_attempt_at = NULL
			WHERE id = ? AND source_ack_queued_at = ? AND source_updated_at = ?`,
			synced, synced, sourceUpdatedAt, id, queuedAtValue, sourceUpdatedAt)
		if err != nil {
			return fmt.Errorf("record notification ack propagation success: %w", err)
		}
		return nil
	}
	now := time.Now().UTC()
	_, err := d.rw.ExecContext(ctx, `UPDATE middleman_notification_items
		SET source_ack_error = ?, source_ack_attempts = source_ack_attempts + 1,
		    source_ack_last_attempt_at = ?, source_ack_next_attempt_at = ?
		WHERE id = ? AND source_ack_queued_at = ? AND source_updated_at = ?`, errText, now, nullableNotificationTime(nextAttemptAt), id, nullableNotificationTime(queuedAt), canonicalUTCTime(sourceUpdatedAt))
	if err != nil {
		return fmt.Errorf("record notification ack propagation failure: %w", err)
	}
	return nil
}

func (d *DB) DeferQueuedNotificationAcks(ctx context.Context, platform, host string, nextAttemptAt time.Time, errText string) error {
	var err error
	platform, host, err = canonicalizeNotificationPlatformHost(platform, host)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	nextAttemptAt = canonicalUTCTime(nextAttemptAt)
	_, err = d.rw.ExecContext(ctx, `UPDATE middleman_notification_items
		SET source_ack_error = ?, source_ack_last_attempt_at = ?,
		    source_ack_next_attempt_at = CASE
			    WHEN source_ack_next_attempt_at IS NULL OR source_ack_next_attempt_at < ? THEN ?
			    ELSE source_ack_next_attempt_at
		    END
		WHERE platform = ?
		  AND platform_host = ?
		  AND source_ack_queued_at IS NOT NULL
		  AND source_ack_synced_at IS NULL
		  AND source_ack_error != 'max_attempts_exceeded'`, errText, now, nextAttemptAt, nextAttemptAt, platform, host)
	if err != nil {
		return fmt.Errorf("defer queued notification acks: %w", err)
	}
	return nil
}

func (d *DB) MarkClosedLinkedNotificationsDone(ctx context.Context, now time.Time) error {
	now = canonicalUTCTime(now)
	_, err := d.rw.ExecContext(ctx, `
		UPDATE middleman_notification_items
		SET done_at = COALESCE(done_at, ?), done_reason = 'closed'
		WHERE done_at IS NULL
		  AND item_type = 'pr'
		  AND item_number IS NOT NULL
		  AND EXISTS (
		    SELECT 1 FROM middleman_repos r
		    JOIN middleman_merge_requests mr ON mr.repo_id = r.id AND mr.number = middleman_notification_items.item_number
		    WHERE r.platform = middleman_notification_items.platform
		      AND r.platform_host = middleman_notification_items.platform_host
		      AND r.owner = middleman_notification_items.repo_owner
		      AND r.name = middleman_notification_items.repo_name
		      AND (mr.state IN ('closed', 'merged') OR mr.merged_at IS NOT NULL OR mr.closed_at IS NOT NULL)
		  )`, now)
	if err != nil {
		return fmt.Errorf("mark closed pr notifications done: %w", err)
	}
	_, err = d.rw.ExecContext(ctx, `
		UPDATE middleman_notification_items
		SET done_at = COALESCE(done_at, ?), done_reason = 'closed'
		WHERE done_at IS NULL
		  AND item_type = 'issue'
		  AND item_number IS NOT NULL
		  AND EXISTS (
		    SELECT 1 FROM middleman_repos r
		    JOIN middleman_issues i ON i.repo_id = r.id AND i.number = middleman_notification_items.item_number
		    WHERE r.platform = middleman_notification_items.platform
		      AND r.platform_host = middleman_notification_items.platform_host
		      AND r.owner = middleman_notification_items.repo_owner
		      AND r.name = middleman_notification_items.repo_name
		      AND (i.state = 'closed' OR i.closed_at IS NOT NULL)
		  )`, now)
	if err != nil {
		return fmt.Errorf("mark closed issue notifications done: %w", err)
	}
	return nil
}

func ParseNotificationRepo(repo string) (host string, owner string, name string, ok bool) {
	parts := strings.Split(repo, "/")
	switch len(parts) {
	case 2:
		if parts[0] == "" || parts[1] == "" {
			return "", "", "", false
		}
		host, owner, name = canonicalRepoIdentifier("", parts[0], parts[1])
		return host, owner, name, true
	case 3:
		if parts[0] == "" || parts[1] == "" || parts[2] == "" {
			return "", "", "", false
		}
		host, owner, name = canonicalRepoIdentifier(parts[0], parts[1], parts[2])
		return host, owner, name, true
	default:
		return "", "", "", false
	}
}
