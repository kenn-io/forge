package semantic

import (
	"context"
	"database/sql"
	"encoding/json/v2"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"go.kenn.io/kit/search/sqlquery"
	"go.kenn.io/kit/vector"
	"go.kenn.io/kit/vector/sqlitevec"
)

// ErrNotReady reports that the configured model has no published generation
// yet, so there is nothing its query vectors can be compared with.
var ErrNotReady = errors.New("semantic index has not embedded any documents yet")

// excerptRunes bounds the matched text returned with a hit.
const excerptRunes = 280

// Query selects items by similarity to Text. RepoIDs restricts the search
// to those repositories; nil searches every repository. ItemType is "pr",
// "issue", or empty for both. States restricts item state; empty allows any.
type Query struct {
	Text     string
	RepoIDs  []int64
	ItemType string
	States   []string
	Limit    int
}

// Hit is one pull request or issue, ranked by its closest document. Score
// is cosine similarity, higher is closer. Match names that document.
type Hit struct {
	RepoID   int64
	ItemType string
	Number   int
	Score    float32
	Match    Match
}

// Match is the document that ranked an item: its description ("pr" or
// "issue") or one of its comments ("pr_comment" or "issue_comment").
// SourceID is the item or comment row ID in the Forge database.
type Match struct {
	Kind     string
	SourceID int64
	Excerpt  string
}

// Search embeds the query and returns the closest items, best first, one hit
// per item. It searches the active generation exactly: every eligible
// document is scored, so filters never hide closer matches behind a
// candidate window.
func (x *Indexer) Search(ctx context.Context, query Query) ([]Hit, error) {
	text := strings.TrimSpace(query.Text)
	if text == "" || query.Limit <= 0 || (query.RepoIDs != nil && len(query.RepoIDs) == 0) {
		return nil, nil
	}
	active, ok, err := x.index.store.ActiveGeneration(ctx)
	if err != nil {
		return nil, err
	}
	if !ok || !x.encoder.owns(active.Key) {
		return nil, ErrNotReady
	}
	vec, err := x.encoder.query(ctx, text)
	if err != nil {
		return nil, fmt.Errorf("semantic: embed query: %w", err)
	}
	predicate, err := searchPredicate(query)
	if err != nil {
		return nil, err
	}
	built, err := x.index.store.BuildFilteredQuery(ctx, x.index.db, active.Key, vec, sqlitevec.FilteredQuery{
		ResultLimit: query.Limit, SourcePredicate: predicate, GroupColumn: "item_key",
	})
	if err != nil {
		return nil, fmt.Errorf("semantic: build query: %w", err)
	}
	hits, err := built.All(ctx, x.index.db, func(rows *sql.Rows) (vector.Hit[string], error) {
		var hit vector.Hit[string]
		var distance float64
		if err := rows.Scan(&hit.Doc, &hit.ChunkIndex, &hit.Revision, &distance); err != nil {
			return hit, err
		}
		hit.Score, err = sqlitevec.ScoreFromDistance(distance)
		return hit, err
	})
	if err != nil {
		return nil, fmt.Errorf("semantic: search: %w", err)
	}
	return x.index.hydrate(ctx, hits)
}

func searchPredicate(query Query) (sqlquery.Predicate, error) {
	var clauses []string
	var args []any
	if query.RepoIDs != nil {
		ids, err := json.Marshal(query.RepoIDs)
		if err != nil {
			return sqlquery.Predicate{}, fmt.Errorf("semantic: encode repositories: %w", err)
		}
		clauses = append(clauses, "d.repo_id IN (SELECT value FROM json_each(?))")
		args = append(args, string(ids))
	}
	if query.ItemType != "" {
		clauses = append(clauses, "d.item_type = ?")
		args = append(args, query.ItemType)
	}
	if len(query.States) > 0 {
		states, err := json.Marshal(query.States)
		if err != nil {
			return sqlquery.Predicate{}, fmt.Errorf("semantic: encode states: %w", err)
		}
		clauses = append(clauses, "d.item_state IN (SELECT value FROM json_each(?))")
		args = append(args, string(states))
	}
	return sqlquery.Predicate{SQL: strings.Join(clauses, " AND "), Args: args}, nil
}

// hydrate pairs each hit with its document. A hit whose document changed
// since the query read it is dropped rather than shown with text its score
// does not describe.
func (ix *Index) hydrate(ctx context.Context, hits []vector.Hit[string]) ([]Hit, error) {
	if len(hits) == 0 {
		return nil, nil
	}
	keys := make([]string, len(hits))
	for i, hit := range hits {
		keys[i] = hit.Doc
	}
	encoded, err := json.Marshal(keys)
	if err != nil {
		return nil, fmt.Errorf("semantic: encode hit keys: %w", err)
	}
	rows, err := ix.db.QueryContext(ctx, `
SELECT doc_key, repo_id, item_type, item_number, content, content_revision
  FROM semantic_docs
 WHERE doc_key IN (SELECT value FROM json_each(?))`, string(encoded))
	if err != nil {
		return nil, fmt.Errorf("semantic: read hit documents: %w", err)
	}
	defer func() { _ = rows.Close() }()
	type document struct {
		repoID   int64
		itemType string
		number   int
		content  string
		revision int64
	}
	docs := make(map[string]document, len(hits))
	for rows.Next() {
		var key string
		var doc document
		if err := rows.Scan(&key, &doc.repoID, &doc.itemType, &doc.number, &doc.content, &doc.revision); err != nil {
			return nil, fmt.Errorf("semantic: scan hit document: %w", err)
		}
		docs[key] = doc
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("semantic: read hit documents: %w", err)
	}

	out := make([]Hit, 0, len(hits))
	for _, hit := range hits {
		doc, ok := docs[hit.Doc]
		if !ok || !sameRevision(hit.Revision, doc.revision) {
			continue
		}
		kind, sourceID, err := parseDocKey(hit.Doc)
		if err != nil {
			return nil, err
		}
		out = append(out, Hit{
			RepoID: doc.repoID, ItemType: doc.itemType, Number: doc.number, Score: hit.Score,
			Match: Match{Kind: kind, SourceID: sourceID, Excerpt: excerpt(doc.content, hit.ChunkIndex)},
		})
	}
	return out, nil
}

func sameRevision(stamped any, current int64) bool {
	revision, ok := stamped.(int64)
	return ok && revision == current
}

func parseDocKey(key string) (string, int64, error) {
	kind, id, ok := strings.Cut(key, ":")
	if !ok {
		return "", 0, fmt.Errorf("semantic: malformed document key %q", key)
	}
	sourceID, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		return "", 0, fmt.Errorf("semantic: malformed document key %q: %w", key, err)
	}
	return kind, sourceID, nil
}

// excerpt returns the start of the chunk that matched, cut to excerptRunes.
func excerpt(content string, chunkIndex int) string {
	text := content
	for _, chunk := range vector.Split(content, splitOptions) {
		if chunk.Index == chunkIndex {
			text = chunk.Text
			break
		}
	}
	text = strings.Join(strings.Fields(text), " ")
	runes := []rune(text)
	if len(runes) <= excerptRunes {
		return text
	}
	return strings.TrimSpace(string(runes[:excerptRunes])) + "..."
}
