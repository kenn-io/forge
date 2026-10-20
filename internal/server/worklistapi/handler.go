// Package worklistapi serves agent-authored worklists to the UI. Agents write
// worklists only through MCP; this package reads, dismisses, and restores
// them, and marks items handled.
package worklistapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/federationauth"
	"go.kenn.io/forge/internal/server/httpapi"
	"go.kenn.io/forge/internal/server/issueapi"
	"go.kenn.io/forge/internal/server/pullapi"
	"go.kenn.io/forge/platform"
)

// ChangedEvent is the server-sent event published after every worklist
// write, dismissal, restore, or handled-mark change.
const ChangedEvent = "worklists_changed"

type ChangedEventData struct {
	WorklistID int64 `json:"worklist_id"`
}

// ProviderSource reads hub-owned worklists without workspace references.
type ProviderSource interface {
	GetWorklist(context.Context, int64) (WorklistResponse, error)
}

type Deps struct {
	DB       *db.DB
	Resolver *httpapi.RepositoryResolver
	Pulls    *pullapi.Handler
	Issues   *issueapi.Handler
	Now      func() time.Time
	// FilterRepos keeps the repositories this node tracks. Nil tracks every
	// active repository.
	FilterRepos func([]db.Repo) []db.Repo
	// ProviderSource is set on spokes, which read worklists from the hub.
	ProviderSource ProviderSource
	// Broadcast publishes an event to connected clients. It may be nil.
	Broadcast func(eventType string, data any)
}

type Handler struct {
	db             *db.DB
	resolver       *httpapi.RepositoryResolver
	pulls          *pullapi.Handler
	issues         *issueapi.Handler
	now            func() time.Time
	filterRepos    func([]db.Repo) []db.Repo
	providerSource ProviderSource
	broadcast      func(eventType string, data any)
}

func New(deps Deps) *Handler {
	return &Handler{
		db: deps.DB, resolver: deps.Resolver, pulls: deps.Pulls, issues: deps.Issues,
		now: deps.Now, filterRepos: deps.FilterRepos, providerSource: deps.ProviderSource,
		broadcast: deps.Broadcast,
	}
}

func (h *Handler) Register(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID: "list-worklists",
		Method:      http.MethodGet,
		Path:        "/worklists",
		Summary:     "List worklists",
		Tags:        []string{"Worklists"},
	}, h.listWorklists)
	huma.Register(api, huma.Operation{
		OperationID: "get-worklist",
		Method:      http.MethodGet,
		Path:        "/worklists/{id}",
		Summary:     "Get worklist",
		Tags:        []string{"Worklists"},
	}, h.getWorklist)
	huma.Register(api, huma.Operation{
		OperationID: "dismiss-worklist",
		Method:      http.MethodPost,
		Path:        "/worklists/{id}/dismiss",
		Summary:     "Dismiss worklist",
		Tags:        []string{"Worklists"},
	}, h.dismissWorklist)
	huma.Register(api, huma.Operation{
		OperationID: "undismiss-worklist",
		Method:      http.MethodPost,
		Path:        "/worklists/{id}/undismiss",
		Summary:     "Restore dismissed worklist",
		Tags:        []string{"Worklists"},
	}, h.undismissWorklist)
	huma.Register(api, huma.Operation{
		OperationID: "set-worklist-items-handled",
		Method:      http.MethodPost,
		Path:        "/worklists/{id}/handled",
		Summary:     "Mark worklist items handled or unhandled",
		Tags:        []string{"Worklists"},
	}, h.setItemsHandled)
}

type setItemsHandledInput struct {
	ID   int64 `path:"id"`
	Body SetWorklistItemsHandledBody
}

func (h *Handler) setItemsHandled(
	ctx context.Context, input *setItemsHandledInput,
) (*httpapi.BodyOutput[WorklistHandledResponse], error) {
	response, err := h.SetHandledService(ctx, input.ID, input.Body.Items, input.Body.Handled)
	if err != nil {
		return nil, err
	}
	if federationRequest(ctx) {
		response.Worklist.WorkspaceID = ""
	}
	return &httpapi.BodyOutput[WorklistHandledResponse]{Body: response}, nil
}

// SetHandledService marks or unmarks items on a worklist. Every item must be
// on the list; otherwise nothing changes.
func (h *Handler) SetHandledService(
	ctx context.Context, id int64, refs []WorklistItemRef, handled bool,
) (WorklistHandledResponse, error) {
	keys := make([]db.WorklistItemKey, 0, len(refs))
	repos := make([]httpapi.RepoRefResponse, 0, len(refs))
	for i, ref := range refs {
		identity := platform.RepositoryIdentity{
			Provider: ref.Provider, PlatformHost: ref.PlatformHost, Key: ref.RepoKey,
		}.Canonical()
		if !identity.Valid() {
			return WorklistHandledResponse{}, httpapi.Validation(
				fmt.Sprintf("body.items[%d]", i), "a verified repository identity is required",
			)
		}
		repo, err := h.db.GetRepositoryByProviderID(ctx, identity)
		if err != nil {
			return WorklistHandledResponse{}, httpapi.Internal("read repository failed")
		}
		if repo == nil {
			return WorklistHandledResponse{}, httpapi.NotFound(
				httpapi.CodeRepoNotFound, "repository not found", map[string]any{"item_index": i},
			)
		}
		keys = append(keys, db.WorklistItemKey{RepoID: repo.Repository.ID, ItemType: ref.ItemType, ItemNumber: ref.Number})
		repos = append(repos, h.resolver.Ref(repo.Repository))
	}
	err := h.db.SetWorklistItemsHandled(ctx, id, keys, handled, h.now())
	if errors.Is(err, db.ErrWorklistNotFound) {
		return WorklistHandledResponse{}, NotFound(id)
	}
	if notOnList, ok := errors.AsType[*db.WorklistItemNotOnListError](err); ok {
		return WorklistHandledResponse{}, httpapi.NotFound(
			httpapi.CodeNotFound, "item is not on the worklist",
			map[string]any{"worklist_id": id, "item_index": notOnList.Index},
		)
	}
	if err != nil {
		return WorklistHandledResponse{}, httpapi.Internal("update worklist failed")
	}
	h.PublishChanged(id)
	tracked, err := h.TrackedRepoIDs(ctx)
	if err != nil {
		return WorklistHandledResponse{}, err
	}
	detail, err := h.db.GetWorklist(ctx, id, tracked)
	if errors.Is(err, db.ErrWorklistNotFound) {
		return WorklistHandledResponse{}, NotFound(id)
	}
	if err != nil {
		return WorklistHandledResponse{}, httpapi.Internal("read worklist failed")
	}
	marks := make(map[db.WorklistItemKey]*time.Time, len(detail.Items))
	for _, item := range detail.Items {
		marks[db.WorklistItemKey{RepoID: item.RepoID, ItemType: item.ItemType, ItemNumber: item.ItemNumber}] = item.HandledAt
	}
	response := WorklistHandledResponse{
		Worklist: summaryResponse(db.SummarizeWorklist(*detail)),
		Items:    make([]WorklistItemMarkResponse, 0, len(refs)),
	}
	for i, ref := range refs {
		response.Items = append(response.Items, WorklistItemMarkResponse{
			Repo: repos[i], ItemType: ref.ItemType, Number: ref.Number,
			HandledAt: formatTimePtr(marks[keys[i]]),
		})
	}
	return response, nil
}

type listWorklistsInput struct {
	IncludeDismissed bool `query:"include_dismissed" doc:"Include dismissed worklists."`
}

type worklistIDInput struct {
	ID int64 `path:"id"`
}

func (h *Handler) listWorklists(
	ctx context.Context, input *listWorklistsInput,
) (*httpapi.BodyOutput[ListWorklistsResponse], error) {
	worklists, err := h.ListService(ctx, input.IncludeDismissed)
	if err != nil {
		return nil, err
	}
	if federationRequest(ctx) {
		for i := range worklists {
			worklists[i].WorkspaceID = ""
		}
	}
	return &httpapi.BodyOutput[ListWorklistsResponse]{Body: ListWorklistsResponse{Worklists: worklists}}, nil
}

func (h *Handler) getWorklist(
	ctx context.Context, input *worklistIDInput,
) (*httpapi.BodyOutput[WorklistResponse], error) {
	var (
		worklist WorklistResponse
		err      error
	)
	if federationRequest(ctx) {
		worklist, err = h.GetProviderService(ctx, input.ID)
	} else {
		worklist, err = h.GetService(ctx, input.ID)
	}
	if err != nil {
		return nil, err
	}
	return &httpapi.BodyOutput[WorklistResponse]{Body: worklist}, nil
}

func (h *Handler) dismissWorklist(
	ctx context.Context, input *worklistIDInput,
) (*httpapi.BodyOutput[WorklistSummaryResponse], error) {
	summary, err := h.SetDismissedService(ctx, input.ID, true)
	if err != nil {
		return nil, err
	}
	if federationRequest(ctx) {
		summary.WorkspaceID = ""
	}
	return &httpapi.BodyOutput[WorklistSummaryResponse]{Body: summary}, nil
}

func (h *Handler) undismissWorklist(
	ctx context.Context, input *worklistIDInput,
) (*httpapi.BodyOutput[WorklistSummaryResponse], error) {
	summary, err := h.SetDismissedService(ctx, input.ID, false)
	if err != nil {
		return nil, err
	}
	if federationRequest(ctx) {
		summary.WorkspaceID = ""
	}
	return &httpapi.BodyOutput[WorklistSummaryResponse]{Body: summary}, nil
}

// federationRequest reports a request from another node. That node cannot
// tell which machine a workspace ID names, so responses to it carry none.
func federationRequest(ctx context.Context) bool {
	_, ok := federationauth.PrincipalFromContext(ctx)
	return ok
}

func (h *Handler) PublishChanged(worklistID int64) {
	if h != nil && h.broadcast != nil {
		h.broadcast(ChangedEvent, ChangedEventData{WorklistID: worklistID})
	}
}

func NotFound(id int64) error {
	return httpapi.NotFound(httpapi.CodeWorklistNotFound, "worklist not found", map[string]any{"worklist_id": id})
}

// ListService returns worklist summaries, most recently updated first.
func (h *Handler) ListService(ctx context.Context, includeDismissed bool) ([]WorklistSummaryResponse, error) {
	tracked, err := h.TrackedRepoIDs(ctx)
	if err != nil {
		return nil, err
	}
	summaries, err := h.db.ListWorklists(ctx, includeDismissed, tracked)
	if err != nil {
		return nil, httpapi.Internal("list worklists failed")
	}
	out := make([]WorklistSummaryResponse, 0, len(summaries))
	for _, summary := range summaries {
		out = append(out, summaryResponse(summary))
	}
	return out, nil
}

func (h *Handler) SetDismissedService(
	ctx context.Context, id int64, dismissed bool,
) (WorklistSummaryResponse, error) {
	if err := h.db.SetWorklistDismissed(ctx, id, dismissed, h.now()); err != nil {
		if errors.Is(err, db.ErrWorklistNotFound) {
			return WorklistSummaryResponse{}, NotFound(id)
		}
		return WorklistSummaryResponse{}, httpapi.Internal("update worklist failed")
	}
	h.PublishChanged(id)
	tracked, err := h.TrackedRepoIDs(ctx)
	if err != nil {
		return WorklistSummaryResponse{}, err
	}
	summary, err := h.db.GetWorklistSummary(ctx, id, tracked)
	if errors.Is(err, db.ErrWorklistNotFound) {
		return WorklistSummaryResponse{}, NotFound(id)
	}
	if err != nil {
		return WorklistSummaryResponse{}, httpapi.Internal("read worklist failed")
	}
	return summaryResponse(*summary), nil
}

// ClearMarksService unmarks every handled item on a worklist and returns how
// many it unmarked.
func (h *Handler) ClearMarksService(ctx context.Context, id int64) (int, error) {
	cleared, err := h.db.ClearWorklistMarks(ctx, id)
	if errors.Is(err, db.ErrWorklistNotFound) {
		return 0, NotFound(id)
	}
	if err != nil {
		return 0, httpapi.Internal("clear worklist marks failed")
	}
	h.PublishChanged(id)
	return cleared, nil
}

// TrackedRepoIDs returns the repositories whose items can be live, or nil
// when every active repository is tracked.
func (h *Handler) TrackedRepoIDs(ctx context.Context) ([]int64, error) {
	if h.filterRepos == nil {
		return nil, nil
	}
	repos, err := h.db.ListRepos(ctx)
	if err != nil {
		return nil, httpapi.Internal("list repositories failed")
	}
	repos = h.filterRepos(repos)
	ids := make([]int64, 0, len(repos))
	for _, repo := range repos {
		ids = append(ids, repo.ID)
	}
	return ids, nil
}

// GetService returns a worklist with every item's freshness and its list
// row, carrying this machine's workspace references. A spoke reads the
// worklist from the hub.
func (h *Handler) GetService(ctx context.Context, id int64) (WorklistResponse, error) {
	var (
		worklist WorklistResponse
		err      error
	)
	if h.providerSource != nil {
		worklist, err = h.providerSource.GetWorklist(ctx, id)
	} else {
		worklist, err = h.readWorklist(ctx, id)
	}
	if err != nil {
		return WorklistResponse{}, err
	}
	return h.overlayLocalWorkspaces(ctx, worklist)
}

// GetProviderService returns a worklist for another node, without workspace
// references.
func (h *Handler) GetProviderService(ctx context.Context, id int64) (WorklistResponse, error) {
	worklist, err := h.readWorklist(ctx, id)
	if err != nil {
		return WorklistResponse{}, err
	}
	worklist.WorkspaceID = ""
	return worklist, nil
}

func (h *Handler) overlayLocalWorkspaces(
	ctx context.Context, worklist WorklistResponse,
) (WorklistResponse, error) {
	var (
		pulls      []pullapi.MergeRequestResponse
		pullIndex  []int
		issues     []issueapi.IssueResponse
		issueIndex []int
	)
	for i, item := range worklist.Items {
		switch {
		case item.Pull != nil:
			pulls, pullIndex = append(pulls, *item.Pull), append(pullIndex, i)
		case item.Issue != nil:
			issues, issueIndex = append(issues, *item.Issue), append(issueIndex, i)
		}
	}
	if len(pulls) > 0 {
		overlaid, err := h.pulls.OverlayLocalWorkspaces(ctx, pulls)
		if err != nil {
			return WorklistResponse{}, err
		}
		for i, index := range pullIndex {
			worklist.Items[index].Pull = &overlaid[i]
		}
	}
	if len(issues) > 0 {
		overlaid, err := h.issues.OverlayLocalWorkspaces(ctx, issues)
		if err != nil {
			return WorklistResponse{}, err
		}
		for i, index := range issueIndex {
			worklist.Items[index].Issue = &overlaid[i]
		}
	}
	return worklist, nil
}

func (h *Handler) readWorklist(ctx context.Context, id int64) (WorklistResponse, error) {
	tracked, err := h.TrackedRepoIDs(ctx)
	if err != nil {
		return WorklistResponse{}, err
	}
	detail, err := h.db.GetWorklist(ctx, id, tracked)
	if errors.Is(err, db.ErrWorklistNotFound) {
		return WorklistResponse{}, NotFound(id)
	}
	if err != nil {
		return WorklistResponse{}, httpapi.Internal("read worklist failed")
	}
	repos := make(map[int64]httpapi.RepoRefResponse)
	var pullItems, issueItems []db.RepoItemNumber
	for _, item := range detail.Items {
		if _, ok := repos[item.RepoID]; !ok {
			repo, err := h.db.GetRepoByID(ctx, item.RepoID)
			if err != nil || repo == nil {
				return WorklistResponse{}, httpapi.Internal("read worklist repository failed")
			}
			repos[item.RepoID] = h.resolver.Ref(*repo)
		}
		if item.Live == nil {
			continue
		}
		key := db.RepoItemNumber{RepoID: item.RepoID, Number: item.ItemNumber}
		if item.ItemType == db.ItemTypePR {
			pullItems = append(pullItems, key)
		} else {
			issueItems = append(issueItems, key)
		}
	}
	pulls, err := h.pulls.RowsForItems(ctx, pullItems)
	if err != nil {
		return WorklistResponse{}, err
	}
	issues, err := h.issues.RowsForItems(ctx, issueItems)
	if err != nil {
		return WorklistResponse{}, err
	}
	pullRows := make(map[db.RepoItemNumber]*pullapi.MergeRequestResponse, len(pulls))
	for i := range pulls {
		pullRows[db.RepoItemNumber{RepoID: pulls[i].RepoID, Number: pulls[i].Number}] = &pulls[i]
	}
	issueRows := make(map[db.RepoItemNumber]*issueapi.IssueResponse, len(issues))
	for i := range issues {
		issueRows[db.RepoItemNumber{RepoID: issues[i].RepoID, Number: issues[i].Number}] = &issues[i]
	}

	out := WorklistResponse{
		ID: detail.ID, Title: detail.Title, Recipe: detail.Recipe, CreatedBy: detail.CreatedBy,
		WorkspaceID: detail.WorkspaceID, CreatedAt: formatTime(detail.CreatedAt),
		UpdatedAt: formatTime(detail.UpdatedAt), DismissedAt: formatTimePtr(detail.DismissedAt),
		Items: make([]WorklistItemResponse, 0, len(detail.Items)),
	}
	for _, item := range detail.Items {
		key := db.RepoItemNumber{RepoID: item.RepoID, Number: item.ItemNumber}
		response := WorklistItemResponse{
			Position: item.Position, Repo: repos[item.RepoID], ItemType: item.ItemType,
			Number: item.ItemNumber, Title: item.Title, URL: item.URL, Score: item.Score,
			Section: item.Section, Note: item.Note, Caveat: item.Caveat,
			WrittenAt: formatTime(item.WrittenAt), Snapshot: stateResponse(item.Snapshot),
			Freshness: string(item.Freshness), Changes: changesResponse(item.Changes),
			HandledAt: formatTimePtr(item.HandledAt),
		}
		if item.Live != nil {
			live := stateResponse(*item.Live)
			response.Live = &live
			if item.ItemType == db.ItemTypePR {
				response.Pull = pullRows[key]
			} else {
				response.Issue = issueRows[key]
			}
		}
		out.Items = append(out.Items, response)
	}
	return out, nil
}

func summaryResponse(summary db.WorklistSummary) WorklistSummaryResponse {
	return WorklistSummaryResponse{
		ID: summary.ID, Title: summary.Title, Recipe: summary.Recipe, CreatedBy: summary.CreatedBy,
		WorkspaceID: summary.WorkspaceID, ItemCount: summary.ItemCount,
		ChangedCount: summary.ChangedCount, GoneCount: summary.GoneCount,
		HandledCount: summary.HandledCount,
		CreatedAt:    formatTime(summary.CreatedAt), UpdatedAt: formatTime(summary.UpdatedAt),
		DismissedAt: formatTimePtr(summary.DismissedAt),
	}
}

func stateResponse(state db.WorklistItemState) WorklistItemStateResponse {
	return WorklistItemStateResponse{
		State: state.State, HeadSHA: state.HeadSHA, CommentCount: state.CommentCount,
		CIStatus: state.CIStatus, ReviewDecision: state.ReviewDecision,
		LastActivityAt: formatTimePtr(state.LastActivityAt),
	}
}

func changesResponse(changes db.WorklistChanges) WorklistChangesResponse {
	return WorklistChangesResponse{
		StateFrom: changes.StateFrom, StateTo: changes.StateTo,
		NewCommits: changes.NewCommits, NewComments: changes.NewComments,
		CIFrom: changes.CIFrom, CITo: changes.CITo,
		ReviewFrom: changes.ReviewFrom, ReviewTo: changes.ReviewTo,
	}
}

func formatTime(t time.Time) string {
	return t.UTC().Format(time.RFC3339)
}

func formatTimePtr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	return new(formatTime(*t))
}
