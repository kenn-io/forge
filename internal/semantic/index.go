// Package semantic keeps an optional embedding index of pull request and
// issue prose and ranks items by meaning.
//
// The index lives in a sidecar SQLite database beside Forge's own. It holds a
// mirror of the embeddable text (titles, descriptions, and comments, never
// code) plus kit's sqlitevec vector tables for the configured model.
// Everything in it is derived from the Forge database: when the schema or the
// configured model changes, the file is deleted and rebuilt, never migrated.
package semantic

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"time"

	_ "modernc.org/sqlite"     // registers the "sqlite" driver
	_ "modernc.org/sqlite/vec" // loads sqlite-vec into every modernc connection

	"go.kenn.io/kit/vector/sqlitevec"
)

// SidecarFile is the index's file name inside the data directory.
const SidecarFile = "semantic.db"

// schemaVersion guards the tables this package owns in the sidecar. Bump it
// when semantic_docs changes shape, or to discard vectors a since-fixed bug
// may have poisoned.
const schemaVersion = "2"

// sidecarDSN keeps the sidecar's connection-local settings. modernc runs
// each _pragma on every pooled connection.
const sidecarDSN = "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=temp_store(MEMORY)"

// Indexer keeps the sidecar mirror current, embeds pending documents, and
// ranks items against the configured model's vectors.
type Indexer struct {
	db       *sql.DB
	store    *sqlitevec.Store[string, string]
	encoder  *Encoder
	source   SourceReader
	interval time.Duration
	wake     chan struct{}
}

// Open opens or creates the sidecar at path for encoder's model. A sidecar
// written by another schema version or model is deleted first: its vectors
// cannot be compared with this model's queries. Run refreshes the index every
// interval and whenever Wake is called, but never starts passes less than a
// minute apart.
func Open(
	ctx context.Context, path string, encoder *Encoder, source SourceReader, interval time.Duration,
) (*Indexer, error) {
	identity := schemaVersion + " " + encoder.key
	db, err := openSidecar(path)
	if err != nil {
		return nil, err
	}
	current, err := sidecarCurrent(ctx, db, identity)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	if !current {
		_ = db.Close()
		if err := removeSidecar(path); err != nil {
			return nil, err
		}
		if db, err = openSidecar(path); err != nil {
			return nil, err
		}
	}
	x, err := initSidecar(ctx, db, identity, encoder)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	x.source, x.interval, x.wake = source, max(interval, minRefreshGap), make(chan struct{}, 1)
	return x, nil
}

// Close releases the sidecar.
func (x *Indexer) Close() error {
	return x.db.Close()
}

func initSidecar(ctx context.Context, db *sql.DB, identity string, encoder *Encoder) (*Indexer, error) {
	if _, err := db.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS semantic_meta (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS semantic_docs (
    doc_key          TEXT PRIMARY KEY,
    item_key         TEXT NOT NULL,
    content          TEXT NOT NULL,
    content_revision INTEGER NOT NULL,
    embed_gen        TEXT
);
INSERT INTO semantic_meta (key, value) VALUES ('identity', ?)
ON CONFLICT(key) DO NOTHING;`, identity); err != nil {
		return nil, fmt.Errorf("semantic: ensure schema: %w", err)
	}
	store, err := sqlitevec.New[string, string](ctx, db, sqlitevec.Schema{
		DocsTable:      "semantic_docs",
		IDColumn:       "doc_key",
		ContentColumn:  "content",
		EmbedGenColumn: "embed_gen",
		VectorsPrefix:  "semantic_vectors",
		RevisionColumn: "content_revision",
	})
	if err != nil {
		return nil, fmt.Errorf("semantic: init vector store: %w", err)
	}
	// The sidecar holds one generation, active from the start, so search
	// answers from the documents embedded so far during a backfill.
	if err := store.EnsureGeneration(ctx, encoder.key, encoder.generation, sqlitevec.StateActive); err != nil {
		return nil, fmt.Errorf("semantic: ensure generation: %w", err)
	}
	return &Indexer{db: db, store: store, encoder: encoder}, nil
}

func openSidecar(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", path+sidecarDSN)
	if err != nil {
		return nil, fmt.Errorf("semantic: open %s: %w", path, err)
	}
	return db, nil
}

func removeSidecar(path string) error {
	for _, p := range []string{path, path + "-wal", path + "-shm"} {
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("semantic: reset %s: %w", p, err)
		}
	}
	return nil
}

// sidecarCurrent reports whether the sidecar was built by this schema version
// for this model. A fresh file with no tables is current.
func sidecarCurrent(ctx context.Context, db *sql.DB, identity string) (bool, error) {
	var tables int
	if err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM sqlite_master WHERE type = 'table'`).Scan(&tables); err != nil {
		return false, fmt.Errorf("semantic: probe sidecar: %w", err)
	}
	if tables == 0 {
		return true, nil
	}
	var stored string
	err := db.QueryRowContext(ctx,
		`SELECT value FROM semantic_meta WHERE key = 'identity'`).Scan(&stored)
	if err != nil {
		// A missing table or row means an unknown layout; rebuild it.
		return false, nil //nolint:nilerr // any read failure means the sidecar is not ours to trust
	}
	return stored == identity, nil
}
