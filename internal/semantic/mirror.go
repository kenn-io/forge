package semantic

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"strconv"
	"strings"

	"go.kenn.io/forge/internal/db"
)

// mirrorPageSize bounds how many source rows one read and one sidecar
// transaction cover, so neither database holds a long transaction.
const mirrorPageSize = 500

// SourceReader pages through the Forge database's embeddable prose.
type SourceReader interface {
	ListSemanticSources(
		ctx context.Context, kind db.SemanticSourceKind, afterID int64, limit int,
	) ([]db.SemanticSource, error)
}

type mirrorDoc struct {
	key     string
	itemKey string
	content string
}

func docKey(kind db.SemanticSourceKind, id int64) string {
	return string(kind) + ":" + strconv.FormatInt(id, 10)
}

func itemKey(itemType string, item db.RepoItemNumber) string {
	return strconv.FormatInt(item.RepoID, 10) + ":" + itemType + ":" + strconv.Itoa(item.Number)
}

// documentContent is the text embedded for a source. An item embeds its
// title and description together; a comment embeds its body alone, so
// renaming an item does not re-embed every comment on it.
func documentContent(source db.SemanticSource) string {
	switch source.Kind {
	case db.SemanticSourcePull, db.SemanticSourceIssue:
		return strings.TrimSpace(source.Title + "\n\n" + source.Body)
	default:
		return strings.TrimSpace(source.Body)
	}
}

// syncMirror makes the sidecar's documents match the Forge database. A
// document whose text changed gets a new revision, which leaves its vectors
// unsearchable until the next fill re-embeds it; a document that only moved
// to another item keeps its revision. Documents no longer in the Forge
// database are removed with their vectors.
func (x *Indexer) syncMirror(ctx context.Context) error {
	seen := make(map[string]struct{})
	for _, kind := range db.SemanticSourceKinds {
		var afterID int64
		for {
			page, err := x.source.ListSemanticSources(ctx, kind, afterID, mirrorPageSize)
			if err != nil {
				return err
			}
			if len(page) == 0 {
				break
			}
			docs := make([]mirrorDoc, len(page))
			for i, row := range page {
				docs[i] = mirrorDoc{
					key:     docKey(row.Kind, row.ID),
					itemKey: itemKey(row.ItemType, db.RepoItemNumber{RepoID: row.RepoID, Number: row.ItemNumber}),
					content: documentContent(row),
				}
				seen[docs[i].key] = struct{}{}
			}
			if err := x.applyMirrorPage(ctx, docs); err != nil {
				return err
			}
			afterID = page[len(page)-1].ID
			if len(page) < mirrorPageSize {
				break
			}
		}
	}
	return x.removeUnseen(ctx, seen)
}

func (x *Indexer) applyMirrorPage(ctx context.Context, docs []mirrorDoc) error {
	keys := make([]string, len(docs))
	for i, doc := range docs {
		keys[i] = doc.key
	}
	existing, err := x.loadMirrorDocs(ctx, keys)
	if err != nil {
		return err
	}
	tx, err := x.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("semantic: begin mirror page: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	for _, doc := range docs {
		current, found := existing[doc.key]
		switch {
		case !found:
			_, err = tx.ExecContext(ctx, `
INSERT INTO semantic_docs (doc_key, item_key, content, content_revision) VALUES (?, ?, ?, 1)`,
				doc.key, doc.itemKey, doc.content)
		case current.content != doc.content:
			_, err = tx.ExecContext(ctx, `
UPDATE semantic_docs SET item_key = ?, content = ?, content_revision = content_revision + 1 WHERE doc_key = ?`,
				doc.itemKey, doc.content, doc.key)
		case current.itemKey != doc.itemKey:
			_, err = tx.ExecContext(ctx, `UPDATE semantic_docs SET item_key = ? WHERE doc_key = ?`, doc.itemKey, doc.key)
		}
		if err != nil {
			return fmt.Errorf("semantic: mirror %s: %w", doc.key, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("semantic: commit mirror page: %w", err)
	}
	return nil
}

func (x *Indexer) loadMirrorDocs(ctx context.Context, keys []string) (map[string]mirrorDoc, error) {
	encoded, err := json.Marshal(keys)
	if err != nil {
		return nil, fmt.Errorf("semantic: encode mirror keys: %w", err)
	}
	rows, err := x.db.QueryContext(ctx, `
SELECT doc_key, item_key, content FROM semantic_docs WHERE doc_key IN (SELECT value FROM json_each(?))`,
		string(encoded))
	if err != nil {
		return nil, fmt.Errorf("semantic: read mirror page: %w", err)
	}
	defer func() { _ = rows.Close() }()
	docs := make(map[string]mirrorDoc)
	for rows.Next() {
		var doc mirrorDoc
		if err := rows.Scan(&doc.key, &doc.itemKey, &doc.content); err != nil {
			return nil, fmt.Errorf("semantic: scan mirror page: %w", err)
		}
		docs[doc.key] = doc
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("semantic: read mirror page: %w", err)
	}
	return docs, nil
}

func (x *Indexer) removeUnseen(ctx context.Context, seen map[string]struct{}) error {
	stale, err := x.unseenKeys(ctx, seen)
	if err != nil {
		return err
	}
	for _, key := range stale {
		// Vectors go first: a document row without vectors is simply
		// removed again on the next pass, while vectors without a row
		// would keep occupying nearest-neighbor slots.
		if err := x.store.DeleteVectors(ctx, key); err != nil {
			return fmt.Errorf("semantic: delete vectors for %s: %w", key, err)
		}
		if _, err := x.db.ExecContext(ctx, `DELETE FROM semantic_docs WHERE doc_key = ?`, key); err != nil {
			return fmt.Errorf("semantic: delete %s: %w", key, err)
		}
	}
	return nil
}

func (x *Indexer) unseenKeys(ctx context.Context, seen map[string]struct{}) ([]string, error) {
	rows, err := x.db.QueryContext(ctx, `SELECT doc_key FROM semantic_docs`)
	if err != nil {
		return nil, fmt.Errorf("semantic: list mirror keys: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var stale []string
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			return nil, fmt.Errorf("semantic: scan mirror key: %w", err)
		}
		if _, ok := seen[key]; !ok {
			stale = append(stale, key)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("semantic: list mirror keys: %w", err)
	}
	return stale, nil
}
