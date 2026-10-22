package worklistapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/server/httpapi"
	"go.kenn.io/forge/worklist"
)

func (h *Handler) registerLink(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID: "get-worklist-link",
		Method:      http.MethodGet,
		Path:        "/worklists/{id}/link",
		Summary:     "Get a link that carries the worklist",
		Tags:        []string{"Worklists"},
	}, h.getWorklistLink)
}

func (h *Handler) getWorklistLink(
	ctx context.Context, input *worklistIDInput,
) (*httpapi.BodyOutput[WorklistLinkResponse], error) {
	link, err := h.LinkService(ctx, input.ID)
	if err != nil {
		return nil, err
	}
	return &httpapi.BodyOutput[WorklistLinkResponse]{Body: link}, nil
}

// LinkService returns a kenn:// link carrying the worklist's title, recipe,
// author label, and items. Items name their repository's current route,
// because the recipient's Forge resolves its own stable identity. Handled
// marks and what Forge recorded for each item stay behind.
func (h *Handler) LinkService(ctx context.Context, id int64) (WorklistLinkResponse, error) {
	detail, err := h.db.GetWorklist(ctx, id, nil)
	if errors.Is(err, db.ErrWorklistNotFound) {
		return WorklistLinkResponse{}, NotFound(id)
	}
	if err != nil {
		return WorklistLinkResponse{}, httpapi.Internal("read worklist failed")
	}
	if len(detail.Items) == 0 {
		return WorklistLinkResponse{}, httpapi.Conflict(
			httpapi.CodeConflict, "a worklist with no items has no link", map[string]any{"worklist_id": id},
		)
	}
	repos := make(map[int64]httpapi.RepoRefResponse)
	list := worklist.List{
		Title: detail.Title, Recipe: detail.Recipe, CreatedBy: detail.CreatedBy,
		Items: make([]worklist.Item, 0, len(detail.Items)),
	}
	for _, item := range detail.Items {
		repo, ok := repos[item.RepoID]
		if !ok {
			row, err := h.db.GetRepoByID(ctx, item.RepoID)
			if err != nil || row == nil {
				return WorklistLinkResponse{}, httpapi.Internal("read worklist repository failed")
			}
			repo = h.resolver.Ref(*row)
			repos[item.RepoID] = repo
		}
		list.Items = append(list.Items, worklist.Item{
			Item: worklist.ItemRef{
				Type: item.ItemType, Provider: repo.Provider, PlatformHost: repo.PlatformHost,
				Owner: repo.Owner, Name: repo.Name, Number: item.ItemNumber,
			},
			Score: item.Score, Section: item.Section, Note: item.Note, Caveat: item.Caveat,
		})
	}
	link, err := worklist.KennLink(list)
	if errors.Is(err, worklist.ErrTooLarge) {
		return WorklistLinkResponse{}, httpapi.PayloadTooLarge("worklist is too large for a link", 0)
	}
	if err != nil {
		return WorklistLinkResponse{}, httpapi.Internal("encode worklist link failed")
	}
	return WorklistLinkResponse{Link: link}, nil
}
