package httpapi

import (
	"log/slog"
	"strings"
)

// SemanticSearchAvailable validates a list request that ranks by meaning: it
// needs q and an embedding index on this node.
func SemanticSearchAvailable(q string, indexed bool) error {
	if strings.TrimSpace(q) == "" {
		return Validation("query.q", "semantic ranking requires q")
	}
	if !indexed {
		return ServiceUnavailable("semantic search needs an [embeddings] endpoint on the Forge that syncs providers")
	}
	return nil
}

// SemanticSearchFailed reports a ranking failure, such as an unreachable
// embedding endpoint, without echoing endpoint details to the client.
func SemanticSearchFailed(err error) error {
	slog.Warn("semantic search failed", "err", err)
	return ServiceUnavailable("semantic search failed; check the embeddings endpoint")
}
