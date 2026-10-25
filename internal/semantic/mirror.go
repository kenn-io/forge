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

// MirrorStats reports what one mirror refresh changed. Changed counts
// documents whose text differs and so need embedding again.
type MirrorStats struct {
	Added   int
	Changed int
	Removed int
}

type mirrorDoc struct {
	key       string
	repoID    int64
	itemType  string
	number    int
	itemState string
	content   string
}

func docKey(kind db.SemanticSourceKind, id int64) string {
	return string(kind) + ":" + strconv.FormatInt(id, 10)
}

func itemKey(repoID int64, itemType string, number int) string {
	return strconv.FormatInt(repoID, 10) + ":" + itemType + ":" + strconv.Itoa(number)
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

func mirrorDocFor(source db.SemanticSource) mirrorDoc {
	return mirrorDoc{
		key: docKey(source.Kind, source.ID), repoID: source.RepoID,
		itemType: source.ItemType, number: source.ItemNumber,
		itemState: source.ItemState, content: documentContent(source),
	}
}

// SyncMirror makes the sidecar's documents match the Forge database. A
// document whose text changed gets a new revision, which leaves its vectors
// unsearchable until the next fill re-embeds it. Item state and identity are
// refreshed without a new revision. Documents no longer in the Forge
// database are removed with their vectors.
func (ix *Index) SyncMirror(ctx context.Context, source SourceReader) (MirrorStats, error) {
	var stats MirrorStats
	seen := make(map[string]struct{})
	for _, kind := range db.SemanticSourceKinds {
		var afterID int64
		for {
			page, err := source.ListSemanticSources(ctx, kind, afterID, mirrorPageSize)
			if err != nil {
				return stats, err
			}
			if len(page) == 0 {
				break
			}
			docs := make([]mirrorDoc, len(page))
			for i, row := range page {
				docs[i] = mirrorDocFor(row)
				seen[docs[i].key] = struct{}{}
			}
			if err := ix.applyMirrorPage(ctx, docs, &stats); err != nil {
				return stats, err
			}
			afterID = page[len(page)-1].ID
			if len(page) < mirrorPageSize {
				break
			}
		}
	}
	removed, err := ix.removeUnseen(ctx, seen)
	stats.Removed = removed
	return stats, err
}

func (ix *Index) applyMirrorPage(ctx context.Context, docs []mirrorDoc, stats *MirrorStats) error {
	keys := make([]string, len(docs))
	for i, doc := range docs {
		keys[i] = doc.key
	}
	encoded, err := json.Marshal(keys)
	if err != nil {
		return fmt.Errorf("semantic: encode mirror keys: %w", err)
	}
	existing, err := ix.loadMirrorDocs(ctx, string(encoded))
	if err != nil {
		return err
	}

	tx, err := ix.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("semantic: begin mirror page: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	for _, doc := range docs {
		current, found := existing[doc.key]
		switch {
		case !found:
			if _, err := tx.ExecContext(ctx, `
INSERT INTO semantic_docs (doc_key, repo_id, item_type, item_number, item_key, item_state, content, content_revision)
VALUES (?, ?, ?, ?, ?, ?, ?, 1)`,
				doc.key, doc.repoID, doc.itemType, doc.number,
				itemKey(doc.repoID, doc.itemType, doc.number), doc.itemState, doc.content); err != nil {
				return fmt.Errorf("semantic: insert %s: %w", doc.key, err)
			}
			stats.Added++
		case current.content != doc.content:
			if _, err := tx.ExecContext(ctx, `
UPDATE semantic_docs
   SET repo_id = ?, item_type = ?, item_number = ?, item_key = ?, item_state = ?,
       content = ?, content_revision = content_revision + 1
 WHERE doc_key = ?`,
				doc.repoID, doc.itemType, doc.number,
				itemKey(doc.repoID, doc.itemType, doc.number), doc.itemState, doc.content, doc.key); err != nil {
				return fmt.Errorf("semantic: update %s: %w", doc.key, err)
			}
			stats.Changed++
		case current.repoID != doc.repoID || current.itemType != doc.itemType ||
			current.number != doc.number || current.itemState != doc.itemState:
			if _, err := tx.ExecContext(ctx, `
UPDATE semantic_docs
   SET repo_id = ?, item_type = ?, item_number = ?, item_key = ?, item_state = ?
 WHERE doc_key = ?`,
				doc.repoID, doc.itemType, doc.number,
				itemKey(doc.repoID, doc.itemType, doc.number), doc.itemState, doc.key); err != nil {
				return fmt.Errorf("semantic: update %s: %w", doc.key, err)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("semantic: commit mirror page: %w", err)
	}
	return nil
}

func (ix *Index) loadMirrorDocs(ctx context.Context, keys string) (map[string]mirrorDoc, error) {
	rows, err := ix.db.QueryContext(ctx, `
SELECT doc_key, repo_id, item_type, item_number, item_state, content
  FROM semantic_docs
 WHERE doc_key IN (SELECT value FROM json_each(?))`, keys)
	if err != nil {
		return nil, fmt.Errorf("semantic: read mirror page: %w", err)
	}
	defer func() { _ = rows.Close() }()
	docs := make(map[string]mirrorDoc)
	for rows.Next() {
		var doc mirrorDoc
		if err := rows.Scan(&doc.key, &doc.repoID, &doc.itemType, &doc.number, &doc.itemState, &doc.content); err != nil {
			return nil, fmt.Errorf("semantic: scan mirror page: %w", err)
		}
		docs[doc.key] = doc
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("semantic: read mirror page: %w", err)
	}
	return docs, nil
}

func (ix *Index) removeUnseen(ctx context.Context, seen map[string]struct{}) (int, error) {
	stale, err := ix.unseenKeys(ctx, seen)
	if err != nil {
		return 0, err
	}
	for i, key := range stale {
		// Vectors go first: a document row without vectors is simply
		// removed again on the next pass, while vectors without a row
		// would keep occupying nearest-neighbor slots.
		if err := ix.store.DeleteVectors(ctx, key); err != nil {
			return i, fmt.Errorf("semantic: delete vectors for %s: %w", key, err)
		}
		if _, err := ix.db.ExecContext(ctx, `DELETE FROM semantic_docs WHERE doc_key = ?`, key); err != nil {
			return i, fmt.Errorf("semantic: delete %s: %w", key, err)
		}
	}
	return len(stale), nil
}

func (ix *Index) unseenKeys(ctx context.Context, seen map[string]struct{}) ([]string, error) {
	rows, err := ix.db.QueryContext(ctx, `SELECT doc_key FROM semantic_docs`)
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
