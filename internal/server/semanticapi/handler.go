// Package semanticapi serves semantic search over pull requests, issues, and
// their comments. The provider owner answers from its embedding index; a
// spoke forwards to it.
package semanticapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/semantic"
	"go.kenn.io/forge/internal/server/httpapi"
	"go.kenn.io/forge/internal/server/issueapi"
	"go.kenn.io/forge/internal/server/pullapi"
)

const (
	defaultLimit  = 20
	maxLimit      = 100
	searchTimeout = 30 * time.Second
)

// SearchRequest is one semantic search. Repo uses the pull request list's
// filter form, provider|platform_host/repo_path, comma-separated. State is
// open, closed (including merged), or all; empty means all.
type SearchRequest struct {
	Query    string
	Repo     string
	ItemType string
	State    string
	Limit    int
}

// ProviderSource forwards searches from a spoke to the provider owner.
type ProviderSource interface {
	SemanticSearch(context.Context, SearchRequest) (SemanticSearchResponse, error)
}

type Deps struct {
	DB       *db.DB
	Resolver *httpapi.RepositoryResolver
	Pulls    *pullapi.Handler
	Issues   *issueapi.Handler
	// FilterRepos keeps the repositories this node tracks. Nil tracks every
	// active repository.
	FilterRepos func([]db.Repo) []db.Repo
	// Indexer is nil when no [embeddings] endpoint is configured.
	Indexer *semantic.Indexer
	// ProviderSource is set on spokes, which search on the hub.
	ProviderSource ProviderSource
}

type Handler struct {
	db             *db.DB
	resolver       *httpapi.RepositoryResolver
	pulls          *pullapi.Handler
	issues         *issueapi.Handler
	filterRepos    func([]db.Repo) []db.Repo
	indexer        *semantic.Indexer
	providerSource ProviderSource
}

func New(deps Deps) *Handler {
	return &Handler{
		db: deps.DB, resolver: deps.Resolver, pulls: deps.Pulls, issues: deps.Issues,
		filterRepos: deps.FilterRepos, indexer: deps.Indexer, providerSource: deps.ProviderSource,
	}
}

func (h *Handler) Register(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID: "semantic-search",
		Method:      http.MethodGet,
		Path:        "/semantic-search",
		Summary:     "Search pull requests and issues by meaning",
		Description: "Ranks pull requests and issues by how closely their titles, descriptions, " +
			"and comments match the query. Requires an [embeddings] endpoint on the provider owner.",
		Tags: []string{"Search"},
	}, h.search)
	huma.Register(api, huma.Operation{
		OperationID: "get-semantic-search-status",
		Method:      http.MethodGet,
		Path:        "/semantic-search/status",
		Summary:     "Get semantic search index status",
		Tags:        []string{"Search"},
	}, h.status)
}

type searchInput struct {
	Q        string `query:"q" required:"true" minLength:"1" maxLength:"2000" doc:"Natural-language description of what to find."`
	Repo     string `query:"repo" doc:"Repository filter. Accepts provider|platform_host/repo_path, with comma-separated values for multiple repositories."`
	ItemType string `query:"item_type" enum:"pr,issue" doc:"Restrict to pull requests or issues; omit for both."`
	State    string `query:"state" enum:"open,closed,all" default:"all" doc:"closed includes merged pull requests."`
	Limit    int    `query:"limit" minimum:"1" maximum:"100" default:"20"`
}

func (h *Handler) search(
	ctx context.Context, input *searchInput,
) (*httpapi.BodyOutput[SemanticSearchResponse], error) {
	response, err := h.SearchService(ctx, SearchRequest{
		Query: input.Q, Repo: input.Repo, ItemType: input.ItemType,
		State: input.State, Limit: input.Limit,
	})
	if err != nil {
		return nil, err
	}
	return &httpapi.BodyOutput[SemanticSearchResponse]{Body: response}, nil
}

// SearchService runs a search on the provider owner, forwarding from a
// spoke. Rows carry no workspace references.
func (h *Handler) SearchService(ctx context.Context, req SearchRequest) (SemanticSearchResponse, error) {
	if h.providerSource != nil {
		return h.providerSource.SemanticSearch(ctx, req)
	}
	return h.searchLocal(ctx, req)
}

func (h *Handler) searchLocal(ctx context.Context, req SearchRequest) (SemanticSearchResponse, error) {
	if h.indexer == nil {
		return SemanticSearchResponse{}, httpapi.ServiceUnavailable(
			"semantic search is not configured; add an [embeddings] endpoint to the provider owner's config")
	}
	query, err := h.query(ctx, req)
	if err != nil {
		return SemanticSearchResponse{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, searchTimeout)
	defer cancel()
	response := SemanticSearchResponse{Ready: h.indexer.Status().Ready, Items: []SemanticSearchItemResponse{}}
	hits, err := h.indexer.Search(ctx, query)
	if errors.Is(err, semantic.ErrNotReady) {
		response.Ready = false
		return response, nil
	}
	if err != nil {
		slog.Warn("semantic search failed", "err", err)
		return SemanticSearchResponse{}, httpapi.ServiceUnavailable("semantic search failed; check the embeddings endpoint")
	}
	response.Items, err = h.items(ctx, hits)
	if err != nil {
		return SemanticSearchResponse{}, err
	}
	return response, nil
}

func (h *Handler) query(ctx context.Context, req SearchRequest) (semantic.Query, error) {
	text := strings.TrimSpace(req.Query)
	if text == "" {
		return semantic.Query{}, httpapi.Validation("q", "query is required")
	}
	limit := req.Limit
	if limit <= 0 {
		limit = defaultLimit
	}
	limit = min(limit, maxLimit)
	query := semantic.Query{Text: text, Limit: limit}
	switch req.ItemType {
	case "", db.ItemTypePR, db.ItemTypeIssue:
		query.ItemType = req.ItemType
	default:
		return semantic.Query{}, httpapi.Validation("item_type", "item_type must be pr or issue", "pr", "issue")
	}
	switch req.State {
	case "", "all":
	case "open":
		query.States = []string{"open"}
	case "closed":
		query.States = []string{"closed", "merged"}
	default:
		return semantic.Query{}, httpapi.Validation("state", "state must be open, closed, or all", "open", "closed", "all")
	}
	repoIDs, err := h.repoIDs(ctx, req.Repo)
	if err != nil {
		return semantic.Query{}, err
	}
	query.RepoIDs = repoIDs
	return query, nil
}

// repoIDs returns the tracked repositories to search: every tracked
// repository, or those the filter names. A filter naming an untracked
// repository is rejected rather than silently searching nothing.
func (h *Handler) repoIDs(ctx context.Context, filter string) ([]int64, error) {
	repos, err := h.db.ListRepos(ctx)
	if err != nil {
		return nil, httpapi.Internal("list repositories failed")
	}
	if h.filterRepos != nil {
		repos = h.filterRepos(repos)
	}
	tracked := make(map[int64]bool, len(repos))
	ids := make([]int64, 0, len(repos))
	for _, repo := range repos {
		tracked[repo.ID] = true
		ids = append(ids, repo.ID)
	}
	if strings.TrimSpace(filter) == "" {
		return ids, nil
	}
	ids = ids[:0]
	for part := range strings.SplitSeq(filter, ",") {
		part = strings.Trim(part, "/ ")
		if part == "" {
			continue
		}
		provider, hostedPath, ok := strings.Cut(part, "|")
		host, repoPath, hasPath := strings.Cut(strings.Trim(hostedPath, "/ "), "/")
		if !ok || !hasPath {
			return nil, httpapi.Validation("repo", "repo must be provider|platform_host/repo_path")
		}
		repo, err := h.resolver.Lookup(ctx, provider, host, repoPath)
		if err != nil || repo == nil || !tracked[repo.ID] {
			return nil, httpapi.Validation("repo", "repository "+part+" is not tracked")
		}
		ids = append(ids, repo.ID)
	}
	return ids, nil
}

// items pairs each hit with its pull request or issue list row, in rank
// order. A hit whose item is no longer a visible row is dropped.
func (h *Handler) items(ctx context.Context, hits []semantic.Hit) ([]SemanticSearchItemResponse, error) {
	var pullItems, issueItems []db.RepoItemNumber
	for _, hit := range hits {
		key := db.RepoItemNumber{RepoID: hit.RepoID, Number: hit.Number}
		if hit.ItemType == db.ItemTypePR {
			pullItems = append(pullItems, key)
		} else {
			issueItems = append(issueItems, key)
		}
	}
	pulls, err := h.pulls.RowsForItems(ctx, pullItems)
	if err != nil {
		return nil, err
	}
	issues, err := h.issues.RowsForItems(ctx, issueItems)
	if err != nil {
		return nil, err
	}
	pullRows := make(map[db.RepoItemNumber]*pullapi.MergeRequestResponse, len(pulls))
	for i := range pulls {
		pullRows[db.RepoItemNumber{RepoID: pulls[i].RepoID, Number: pulls[i].Number}] = &pulls[i]
	}
	issueRows := make(map[db.RepoItemNumber]*issueapi.IssueResponse, len(issues))
	for i := range issues {
		issueRows[db.RepoItemNumber{RepoID: issues[i].RepoID, Number: issues[i].Number}] = &issues[i]
	}

	out := make([]SemanticSearchItemResponse, 0, len(hits))
	for _, hit := range hits {
		key := db.RepoItemNumber{RepoID: hit.RepoID, Number: hit.Number}
		item := SemanticSearchItemResponse{
			ItemType: hit.ItemType, Number: hit.Number, Score: hit.Score,
			Match: matchResponse(hit.Match),
		}
		if row := pullRows[key]; hit.ItemType == db.ItemTypePR && row != nil {
			item.Repo, item.Title, item.URL, item.Pull = row.Repo, row.Title, row.URL, row
		} else if row := issueRows[key]; hit.ItemType == db.ItemTypeIssue && row != nil {
			item.Repo, item.Title, item.URL, item.Issue = row.Repo, row.Title, row.URL, row
		} else {
			continue
		}
		out = append(out, item)
	}
	return out, nil
}

func matchResponse(match semantic.Match) SemanticSearchMatchResponse {
	switch db.SemanticSourceKind(match.Kind) {
	case db.SemanticSourcePullComment, db.SemanticSourceIssueComment:
		return SemanticSearchMatchResponse{Kind: "comment", CommentID: match.SourceID, Excerpt: match.Excerpt}
	default:
		return SemanticSearchMatchResponse{Kind: "description", Excerpt: match.Excerpt}
	}
}

func (h *Handler) status(
	context.Context, *struct{},
) (*httpapi.BodyOutput[SemanticSearchStatusResponse], error) {
	return &httpapi.BodyOutput[SemanticSearchStatusResponse]{Body: h.StatusService()}, nil
}

// StatusService reports the provider owner's index coverage and health.
func (h *Handler) StatusService() SemanticSearchStatusResponse {
	if h.indexer == nil {
		return SemanticSearchStatusResponse{}
	}
	status := h.indexer.Status()
	return SemanticSearchStatusResponse{
		Enabled: true, Model: status.Model, Ready: status.Ready,
		Embedded: status.Embedded, Skipped: status.Skipped, Backlog: status.Backlog,
		LastSuccessAt: formatTime(status.LastSuccessAt),
		LastError:     status.LastError, LastErrorAt: formatTime(status.LastErrorAt),
	}
}

func formatTime(t *time.Time) *string {
	if t == nil {
		return nil
	}
	value := t.UTC().Format(time.RFC3339)
	return &value
}
