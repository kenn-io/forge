package semantic

import (
	"context"
	"database/sql"
	"encoding/json/v2"
	"fmt"
	"strings"

	"go.kenn.io/forge/internal/db"
	"go.kenn.io/kit/search/sqlquery"
	"go.kenn.io/kit/vector/sqlitevec"
)

// Ranked is one row and its cosine similarity to the query.
type Ranked[T any] struct {
	Row   T
	Score float32
}

// Rank orders rows by how closely their items' descriptions and comments
// match text, best first, and returns the page at offset of at most limit
// rows (zero means no limit). Rows whose item has nothing embedded yet are
// dropped. Every row's documents are scored, so the caller's filters, not a
// candidate window, decide what can match.
func Rank[T any](
	ctx context.Context, x *Indexer, text, itemType string, rows []T, item func(T) db.RepoItemNumber,
	offset, limit int,
) ([]Ranked[T], error) {
	text = strings.TrimSpace(text)
	if text == "" || len(rows) == 0 {
		return nil, nil
	}
	byKey := make(map[string]T, len(rows))
	keys := make([]string, 0, len(rows))
	for _, row := range rows {
		key := itemKey(itemType, item(row))
		byKey[key] = row
		keys = append(keys, key)
	}
	scores, err := x.rank(ctx, text, keys)
	if err != nil {
		return nil, err
	}
	scores = scores[min(max(offset, 0), len(scores)):]
	if limit > 0 {
		scores = scores[:min(limit, len(scores))]
	}
	out := make([]Ranked[T], len(scores))
	for i, score := range scores {
		out[i] = Ranked[T]{Row: byKey[score.itemKey], Score: score.value}
	}
	return out, nil
}

type itemScore struct {
	itemKey string
	value   float32
}

// rank scores each item among keys by its closest current document.
func (x *Indexer) rank(ctx context.Context, text string, keys []string) ([]itemScore, error) {
	vec, err := x.encoder.query(ctx, text)
	if err != nil {
		return nil, fmt.Errorf("semantic: embed query: %w", err)
	}
	encoded, err := json.Marshal(keys)
	if err != nil {
		return nil, fmt.Errorf("semantic: encode items: %w", err)
	}
	built, err := x.store.BuildFilteredQuery(ctx, x.db, x.encoder.key, vec, sqlitevec.FilteredQuery{
		ResultLimit: len(keys), GroupColumn: "item_key",
		SourcePredicate: sqlquery.Predicate{
			SQL: "d.item_key IN (SELECT value FROM json_each(?))", Args: []any{string(encoded)},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("semantic: build query: %w", err)
	}
	ranked := sqlquery.Query{
		SQL: `SELECT d.item_key, q.distance FROM (` + built.SQL + `) q
JOIN semantic_docs d ON d.doc_key = q.doc_key
ORDER BY q.distance, q.doc_key, q.chunk_index`,
		Args: built.Args,
	}
	scores, err := ranked.All(ctx, x.db, func(rows *sql.Rows) (itemScore, error) {
		var score itemScore
		var distance float64
		if err := rows.Scan(&score.itemKey, &distance); err != nil {
			return score, err
		}
		score.value, err = sqlitevec.ScoreFromDistance(distance)
		return score, err
	})
	if err != nil {
		return nil, fmt.Errorf("semantic: rank: %w", err)
	}
	return scores, nil
}
