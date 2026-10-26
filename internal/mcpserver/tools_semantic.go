package mcpserver

import (
	"context"
	"errors"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type semanticSearchInput struct {
	Query    string          `json:"query" jsonschema:"natural-language description of the work to find, such as a bug, feature, or topic"`
	Repo     repoFilterInput `json:"repo,omitzero"`
	ItemType string          `json:"item_type,omitempty" jsonschema:"pr or issue; omit for both"`
	State    string          `json:"state,omitempty" jsonschema:"open, closed (includes merged), or all; defaults to all"`
	Limit    int             `json:"limit,omitempty" jsonschema:"maximum results, default 20, at most 100"`
}

type semanticSearchMatch struct {
	Kind      string `json:"kind" jsonschema:"description (title and body) or comment"`
	CommentID int64  `json:"comment_id,omitempty"`
	Excerpt   string `json:"excerpt"`
}

type semanticSearchHit struct {
	Item  itemRef             `json:"item"`
	Score float32             `json:"score" jsonschema:"cosine similarity; compare only within one response"`
	Match semanticSearchMatch `json:"match"`
}

type semanticSearchOutput struct {
	Ready   bool                `json:"ready" jsonschema:"false while the index is still embedding; results then cover only part of the cache"`
	Results []semanticSearchHit `json:"results"`
}

func (s *Server) registerSemanticTools() {
	mcp.AddTool(s.mcp, &mcp.Tool{
		Name: "kenn_forge_semantic_search",
		Description: "Find cached PRs and issues by meaning rather than keywords: ranks items by how " +
			"closely their titles, descriptions, and comments match a natural-language query, one " +
			"result per item with the passage that matched. Use it to gather candidates for a " +
			"worklist or to find related or duplicate work. Requires an embeddings endpoint on the " +
			"Forge that syncs providers; returns serviceUnavailable when none is configured.",
	}, wrapTool(s.semanticSearch))
}

func (s *Server) semanticSearch(ctx context.Context, in semanticSearchInput) (semanticSearchOutput, error) {
	query := strings.TrimSpace(in.Query)
	if query == "" {
		return semanticSearchOutput{}, errors.New("query is required")
	}
	repo, err := s.repositoryFilter(ctx, in.Repo, "repo.")
	if err != nil {
		return semanticSearchOutput{}, err
	}
	switch in.ItemType {
	case "", "pr", "issue":
	default:
		return semanticSearchOutput{}, errors.New("item_type must be pr or issue")
	}
	switch in.State {
	case "", "open", "closed", "all":
	default:
		return semanticSearchOutput{}, errors.New("state must be open, closed, or all")
	}
	result, err := s.backend.SemanticSearch(ctx, SemanticSearchQuery{
		Query: query, Repository: repo, ItemType: in.ItemType, State: in.State,
		Limit: clampLimit(in.Limit, 20, 100),
	})
	if err != nil {
		return semanticSearchOutput{}, err
	}
	out := semanticSearchOutput{Ready: result.Ready, Results: make([]semanticSearchHit, 0, len(result.Items))}
	for _, item := range result.Items {
		ref := candidateKey{
			provider: item.Repository.Provider, platformHost: item.Repository.PlatformHost,
			repoKey: item.Repository.Key, repoPath: repositoryPath(item.Repository),
			owner: item.Repository.Owner, name: item.Repository.Name,
			itemType: item.ItemType, number: item.Number,
		}.itemRef()
		ref.Title, ref.URL, ref.State, ref.Author = item.Title, item.URL, item.State, item.Author
		out.Results = append(out.Results, semanticSearchHit{
			Item: ref, Score: item.Score,
			Match: semanticSearchMatch{Kind: item.MatchKind, CommentID: item.CommentID, Excerpt: item.Excerpt},
		})
	}
	return out, nil
}
