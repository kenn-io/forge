package worklistapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/mcpserver"
	"go.kenn.io/forge/internal/server/httpapi"
	"go.kenn.io/forge/internal/server/issueapi"
	"go.kenn.io/forge/internal/server/pullapi"
	"go.kenn.io/forge/worklist"
)

// Import statuses name what happens to each item of a worklist link.
const (
	ImportStatusImportable     = "importable"
	ImportStatusRepoNotTracked = "repo_not_tracked"
	ImportStatusItemNotFound   = "item_not_found"
	ImportStatusDuplicate      = "duplicate"
)

func (h *Handler) registerImport(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID: "preview-worklist-import",
		Method:      http.MethodPost,
		Path:        "/worklists/import/preview",
		Summary:     "Preview the worklist a link carries",
		Tags:        []string{"Worklists"},
	}, h.previewImport)
	huma.Register(api, huma.Operation{
		OperationID: "import-worklist",
		Method:      http.MethodPost,
		Path:        "/worklists/import",
		Summary:     "Create the worklist a link carries",
		Tags:        []string{"Worklists"},
	}, h.importWorklist)
}

type importWorklistInput struct {
	Body ImportWorklistBody
}

func (h *Handler) previewImport(
	ctx context.Context, input *importWorklistInput,
) (*httpapi.BodyOutput[WorklistImportPreviewResponse], error) {
	list, err := parseImportFragment(input.Body.Fragment)
	if err != nil {
		return nil, err
	}
	resolved, err := h.resolveImport(ctx, list)
	if err != nil {
		return nil, err
	}
	preview := WorklistImportPreviewResponse{
		Title: strings.TrimSpace(list.Title), Recipe: list.Recipe, CreatedBy: strings.TrimSpace(list.CreatedBy),
		Items: make([]WorklistImportItemResponse, 0, len(list.Items)),
	}
	for i, item := range list.Items {
		preview.Items = append(preview.Items, WorklistImportItemResponse{
			Position: i, Provider: item.Item.Provider, PlatformHost: item.Item.PlatformHost,
			Owner: item.Item.Owner, Name: item.Item.Name, ItemType: item.Item.Type, Number: item.Item.Number,
			Score: item.Score, Section: strings.TrimSpace(item.Section), Note: item.Note, Caveat: item.Caveat,
			Status: resolved[i].status, Pull: resolved[i].pull, Issue: resolved[i].issue,
		})
		if resolved[i].status == ImportStatusImportable {
			preview.ImportableCount++
		}
	}
	return &httpapi.BodyOutput[WorklistImportPreviewResponse]{Body: preview}, nil
}

func (h *Handler) importWorklist(
	ctx context.Context, input *importWorklistInput,
) (*httpapi.BodyOutput[WorklistImportResponse], error) {
	list, err := parseImportFragment(input.Body.Fragment)
	if err != nil {
		return nil, err
	}
	resolved, err := h.resolveImport(ctx, list)
	if err != nil {
		return nil, err
	}
	put := mcpserver.WorklistPut{
		Title: strings.TrimSpace(list.Title), Recipe: list.Recipe, CreatedBy: strings.TrimSpace(list.CreatedBy),
	}
	for i, item := range list.Items {
		if resolved[i].status != ImportStatusImportable {
			continue
		}
		identity := resolved[i].repo.Identity()
		put.Items = append(put.Items, mcpserver.WorklistPutItem{
			Item: mcpserver.ItemIdentity{
				Type: item.Item.Type, Provider: identity.Provider, PlatformHost: identity.PlatformHost,
				RepoKey: identity.Key, Owner: resolved[i].repo.Owner, Name: resolved[i].repo.Name,
				Number: item.Item.Number,
			},
			Score: item.Score, Section: strings.TrimSpace(item.Section), Note: item.Note, Caveat: item.Caveat,
		})
	}
	if len(put.Items) == 0 {
		return nil, httpapi.Validation("body.fragment", "no item in the link is a pull request or issue this Forge tracks")
	}
	result, err := h.put(ctx, put)
	if err != nil {
		return nil, err
	}
	return &httpapi.BodyOutput[WorklistImportResponse]{Body: WorklistImportResponse{
		WorklistID: result.WorklistID, ItemCount: result.ItemCount,
		SkippedCount: len(list.Items) - len(put.Items),
	}}, nil
}

func parseImportFragment(fragment string) (worklist.List, error) {
	list, err := worklist.ParseFragment(fragment)
	if fieldErr, ok := errors.AsType[*worklist.FieldError](err); ok {
		return worklist.List{}, httpapi.Validation("body.fragment", fmt.Sprintf("the link's %s", fieldErr.Error()))
	}
	if err != nil {
		return worklist.List{}, httpapi.Validation("body.fragment", err.Error())
	}
	return list, nil
}

type importResolution struct {
	status string
	repo   *db.ActiveRepo
	pull   *pullapi.MergeRequestResponse
	issue  *issueapi.IssueResponse
}

// resolveImport finds each item's tracked repository by its route, or by its
// stable key when the link carries one, and its visible list row. Items that
// resolve to the same pull request or issue import once, at the first rank.
func (h *Handler) resolveImport(ctx context.Context, list worklist.List) ([]importResolution, error) {
	tracked, err := h.TrackedRepoIDs(ctx)
	if err != nil {
		return nil, err
	}
	resolved := make([]importResolution, len(list.Items))
	var pullItems, issueItems []db.RepoItemNumber
	for i, item := range list.Items {
		repo, err := h.lookupImportRepo(ctx, item.Item)
		if errors.Is(err, errImportRepoNotFound) {
			resolved[i].status = ImportStatusRepoNotTracked
			continue
		}
		if err != nil {
			return nil, err
		}
		if tracked != nil && !slices.Contains(tracked, repo.ID) {
			resolved[i].status = ImportStatusRepoNotTracked
			continue
		}
		resolved[i].repo = repo
		key := db.RepoItemNumber{RepoID: repo.ID, Number: item.Item.Number}
		if item.Item.Type == db.ItemTypePR {
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
	seen := make(map[db.WorkspaceSubjectKey]bool, len(list.Items))
	for i, item := range list.Items {
		if resolved[i].repo == nil {
			continue
		}
		key := db.RepoItemNumber{RepoID: resolved[i].repo.ID, Number: item.Item.Number}
		if item.Item.Type == db.ItemTypePR {
			resolved[i].pull = pullRows[key]
		} else {
			resolved[i].issue = issueRows[key]
		}
		subject := db.WorkspaceSubjectKey{RepoID: key.RepoID, ItemType: item.Item.Type, ItemNumber: key.Number}
		switch {
		case resolved[i].pull == nil && resolved[i].issue == nil:
			resolved[i].status = ImportStatusItemNotFound
		case seen[subject]:
			resolved[i].status = ImportStatusDuplicate
		default:
			resolved[i].status = ImportStatusImportable
			seen[subject] = true
		}
	}
	return resolved, nil
}

var errImportRepoNotFound = errors.New("no active repository at the link's route or key")

// lookupImportRepo returns the active repository an item names, or
// errImportRepoNotFound when this Forge has none at that route or key.
func (h *Handler) lookupImportRepo(ctx context.Context, ref worklist.ItemRef) (*db.ActiveRepo, error) {
	key, err := ref.RepositoryKey()
	if err != nil {
		// ParseFragment validated the key.
		return nil, httpapi.Internal("read repository key failed")
	}
	repo, err := h.resolver.LookupSelection(ctx, ref.Provider, ref.PlatformHost, ref.Owner, ref.Name, key)
	switch {
	case errors.Is(err, httpapi.ErrRepoNotFound),
		errors.Is(err, httpapi.ErrUnsupportedPlatform),
		errors.Is(err, httpapi.ErrPlatformHostRequired):
		return nil, errImportRepoNotFound
	case err != nil:
		return nil, httpapi.Internal("look up repository failed")
	}
	return repo, nil
}
