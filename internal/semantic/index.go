// Package semantic keeps an optional embedding index of pull request and
// issue prose and searches it by meaning.
//
// The index lives in a sidecar SQLite database beside Forge's own. It holds a
// mirror of the embeddable text (titles, descriptions, and comments, never
// code) plus kit's sqlitevec vector tables. Everything in it is derived from
// the Forge database: on a schema version mismatch the file is deleted and
// rebuilt, never migrated.
package semantic

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"

	_ "modernc.org/sqlite"     // registers the "sqlite" driver
	_ "modernc.org/sqlite/vec" // loads sqlite-vec into every modernc connection

	"go.kenn.io/kit/vector/sqlitevec"
)

// SidecarFile is the index's file name inside the data directory.
const SidecarFile = "semantic.db"

// schemaVersion guards the tables this package owns in the sidecar. Bump it
// when semantic_docs or semantic_meta change shape, or to discard vectors a
// since-fixed bug may have poisoned. A mismatch deletes the file and the
// indexer re-embeds from the Forge database.
const schemaVersion = "1"

const vectorsPrefix = "semantic_vectors"

// sidecarDSN keeps the sidecar's connection-local settings. modernc runs
// each _pragma on every pooled connection.
const sidecarDSN = "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=temp_store(MEMORY)"

// Index is the open sidecar database and kit's vector store bound to it.
type Index struct {
	db    *sql.DB
	store *sqlitevec.Store[string, string]
}

// Open opens or creates the sidecar at path, deleting and recreating it when
// its schema version does not match this build.
func Open(ctx context.Context, path string) (*Index, error) {
	db, err := openSidecar(path)
	if err != nil {
		return nil, err
	}
	current, err := schemaCurrent(ctx, db)
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
	if err := ensureSchema(ctx, db); err != nil {
		_ = db.Close()
		return nil, err
	}
	store, err := sqlitevec.New[string, string](ctx, db, sqlitevec.Schema{
		DocsTable:      "semantic_docs",
		IDColumn:       "doc_key",
		ContentColumn:  "content",
		EmbedGenColumn: "embed_gen",
		VectorsPrefix:  vectorsPrefix,
		RevisionColumn: "content_revision",
	})
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("semantic: init vector store: %w", err)
	}
	return &Index{db: db, store: store}, nil
}

// Close releases the sidecar.
func (ix *Index) Close() error {
	return ix.db.Close()
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

// schemaCurrent reports whether the sidecar records this build's schema
// version. A fresh file with no tables is current.
func schemaCurrent(ctx context.Context, db *sql.DB) (bool, error) {
	var tables int
	if err := db.QueryRowContext(ctx,
		`SELECT count(*) FROM sqlite_master WHERE type = 'table'`).Scan(&tables); err != nil {
		return false, fmt.Errorf("semantic: probe sidecar: %w", err)
	}
	if tables == 0 {
		return true, nil
	}
	var version string
	err := db.QueryRowContext(ctx,
		`SELECT value FROM semantic_meta WHERE key = 'schema_version'`).Scan(&version)
	if err != nil {
		// A missing table or row means an unknown layout; rebuild it.
		return false, nil //nolint:nilerr // any read failure means the sidecar is not ours to trust
	}
	return version == schemaVersion, nil
}

func ensureSchema(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS semantic_meta (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS semantic_docs (
    doc_key          TEXT PRIMARY KEY,
    repo_id          INTEGER NOT NULL,
    item_type        TEXT NOT NULL,
    item_number      INTEGER NOT NULL,
    item_key         TEXT NOT NULL,
    item_state       TEXT NOT NULL,
    content          TEXT NOT NULL,
    content_revision INTEGER NOT NULL,
    embed_gen        TEXT
);
CREATE INDEX IF NOT EXISTS semantic_docs_repo ON semantic_docs (repo_id);
INSERT INTO semantic_meta (key, value) VALUES ('schema_version', '`+schemaVersion+`')
ON CONFLICT(key) DO NOTHING;`)
	if err != nil {
		return fmt.Errorf("semantic: ensure schema: %w", err)
	}
	return nil
}
