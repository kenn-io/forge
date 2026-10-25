package server

import (
	"context"

	"go.kenn.io/forge/internal/mcpserver"
	"go.kenn.io/forge/internal/server/httpapi"
	"go.kenn.io/forge/internal/server/mcpapi"
	"go.kenn.io/forge/internal/server/semanticapi"
)

func (b mcpBackend) SemanticSearch(
	ctx context.Context, query mcpserver.SemanticSearchQuery,
) (mcpserver.SemanticSearchResult, error) {
	if b.server.semanticAPI == nil {
		return mcpserver.SemanticSearchResult{}, mcpapi.McpBackendError(httpapi.ServiceUnavailable(
			"semantic search is not available on this server",
		))
	}
	if query.Repository.Provider != "" {
		resolved, err := b.resolveProviderRepository(ctx, query.Repository)
		if err != nil {
			return mcpserver.SemanticSearchResult{}, err
		}
		query.Repository = mcpapi.RepositoryAtResolvedRoute(query.Repository, resolved.Repo)
	}
	response, err := b.server.semanticAPI.SearchService(ctx, semanticapi.SearchRequest{
		Query: query.Query, Repo: mcpapi.McpRepositoryFilter(query.Repository),
		ItemType: query.ItemType, State: query.State, Limit: query.Limit,
	})
	if err != nil {
		return mcpserver.SemanticSearchResult{}, mcpapi.McpBackendError(err)
	}
	out := mcpserver.SemanticSearchResult{
		Ready: response.Ready, Items: make([]mcpserver.SemanticSearchItem, 0, len(response.Items)),
	}
	for _, item := range response.Items {
		hit := mcpserver.SemanticSearchItem{
			Repository: mcpapi.RepositoryIdentityFromResponse(item.Repo),
			ItemType:   item.ItemType, Number: item.Number, Title: item.Title, URL: item.URL,
			Score: item.Score, MatchKind: item.Match.Kind, CommentID: item.Match.CommentID,
			Excerpt: item.Match.Excerpt,
		}
		switch {
		case item.Pull != nil:
			hit.State, hit.Author = string(item.Pull.State), item.Pull.Author
		case item.Issue != nil:
			hit.State, hit.Author = item.Issue.State, item.Issue.Author
		}
		out.Items = append(out.Items, hit)
	}
	return out, nil
}
