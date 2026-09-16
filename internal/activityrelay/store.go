package activityrelay

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"uuid"

	_ "modernc.org/sqlite"
)

type Store struct {
	db     *sql.DB
	stream uuid.UUID
}

var ErrInvalidCursor = errors.New("invalid cursor")

func Open(path string) (*Store, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve relay database: %w", err)
	}
	dsn := (&url.URL{Scheme: "file", Path: absolute}).String() + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(FULL)"
	database, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open relay database: %w", err)
	}
	// The feed is small; one connection serializes appends and snapshot reads.
	database.SetMaxOpenConns(1)
	_, err = database.Exec(`
		CREATE TABLE IF NOT EXISTS relay_stream (
			id INTEGER PRIMARY KEY CHECK (id = 1),
			identity TEXT NOT NULL,
			pruned INTEGER NOT NULL DEFAULT 0
		);
		CREATE TABLE IF NOT EXISTS relay_events (
			sequence INTEGER PRIMARY KEY AUTOINCREMENT,
			repository_id TEXT NOT NULL CHECK (length(repository_id) BETWEEN 1 AND 256),
			target TEXT NOT NULL CHECK (target IN ('pull_request', 'pull_request_checks', 'issue', 'repository_refs', 'repository')),
			number INTEGER NOT NULL,
			received_at INTEGER NOT NULL,
			CHECK ((target IN ('pull_request', 'pull_request_checks', 'issue') AND number > 0)
				OR (target IN ('repository_refs', 'repository') AND number = 0))
		);
		INSERT INTO relay_stream (id, identity) VALUES (1, ?) ON CONFLICT(id) DO NOTHING;
	`, uuid.New().String())
	if err != nil {
		_ = database.Close()
		return nil, fmt.Errorf("initialize relay database: %w", err)
	}
	var identity string
	err = database.QueryRow("SELECT identity FROM relay_stream WHERE id = 1").Scan(&identity)
	if err != nil {
		_ = database.Close()
		return nil, fmt.Errorf("read relay stream: %w", err)
	}
	stream, err := uuid.Parse(identity)
	if err != nil {
		_ = database.Close()
		return nil, fmt.Errorf("parse stored stream identity: %w", err)
	}
	return &Store{db: database, stream: stream}, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) Append(ctx context.Context, hints []Hint) error {
	for _, hint := range hints {
		if err := hint.Validate(); err != nil {
			return err
		}
	}
	if len(hints) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin relay append: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	now := time.Now().UTC().UnixNano()
	for _, hint := range hints {
		if _, err := tx.ExecContext(ctx, `INSERT INTO relay_events (repository_id, target, number, received_at) VALUES (?, ?, ?, ?)`,
			hint.RepositoryID, hint.Target, hint.Number, now); err != nil {
			return fmt.Errorf("append relay hint: %w", err)
		}
	}
	return tx.Commit()
}

func (s *Store) cursor(sequence int64) string {
	return s.stream.String() + ":" + strconv.FormatInt(sequence, 10)
}

func (s *Store) Read(ctx context.Context, after string, limit int) (Page, error) {
	if limit < 1 || limit > 1000 {
		return Page{}, errors.New("page limit must be between 1 and 1000")
	}
	var stream uuid.UUID
	var sequence int64
	if after != "" {
		identity, seq, ok := strings.Cut(after, ":")
		var err error
		stream, err = uuid.Parse(identity)
		if !ok || err != nil {
			return Page{}, ErrInvalidCursor
		}
		sequence, err = strconv.ParseInt(seq, 10, 64)
		if err != nil || sequence < 0 || seq != strconv.FormatInt(sequence, 10) {
			return Page{}, ErrInvalidCursor
		}
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return Page{}, fmt.Errorf("begin relay page: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var pruned, head int64
	err = tx.QueryRowContext(ctx, `SELECT pruned, MAX(pruned, COALESCE((SELECT MAX(sequence) FROM relay_events), 0)) FROM relay_stream WHERE id = 1`).Scan(&pruned, &head)
	if err != nil {
		return Page{}, fmt.Errorf("read relay boundary: %w", err)
	}
	page := Page{Events: []Event{}, NextCursor: after}
	if after == "" {
		page.NextCursor = s.cursor(head)
		page.ResyncRequired = true
		return page, nil
	}
	if stream != s.stream || sequence < pruned || sequence > head {
		return Page{Events: []Event{}, Code: "cursor_expired", ResetCursor: s.cursor(head)}, nil
	}
	rows, err := tx.QueryContext(ctx, `SELECT sequence, repository_id, target, number FROM relay_events WHERE sequence > ? ORDER BY sequence LIMIT ?`, sequence, limit+1)
	if err != nil {
		return Page{}, fmt.Errorf("read relay events: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var event Event
		if err := rows.Scan(&sequence, &event.RepositoryID, &event.Target, &event.Number); err != nil {
			return Page{}, fmt.Errorf("scan relay event: %w", err)
		}
		if len(page.Events) == limit {
			page.HasMore = true
			break
		}
		event.Provider, event.Host = "github", "github.com"
		event.Cursor = s.cursor(sequence)
		page.Events = append(page.Events, event)
		page.NextCursor = event.Cursor
	}
	if err := rows.Err(); err != nil {
		return Page{}, fmt.Errorf("iterate relay events: %w", err)
	}
	return page, nil
}

func (s *Store) Prune(ctx context.Context, before time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin relay retention: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	// Delete a prefix, preserving a contiguous cursor boundary even if wall time
	// moved backward between deliveries.
	_, err = tx.ExecContext(ctx, `UPDATE relay_stream SET pruned = MAX(pruned, COALESCE(
		(SELECT MIN(sequence) - 1 FROM relay_events WHERE received_at >= ?),
		(SELECT MAX(sequence) FROM relay_events), 0)) WHERE id = 1`, before.UTC().UnixNano())
	if err != nil {
		return fmt.Errorf("advance relay retention: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM relay_events WHERE sequence <= (SELECT pruned FROM relay_stream WHERE id = 1)`); err != nil {
		return fmt.Errorf("prune relay events: %w", err)
	}
	return tx.Commit()
}
