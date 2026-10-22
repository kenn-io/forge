package worklistapi

import (
	"context"
	"crypto/sha256"
	"encoding/json/v2"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/danielgtaylor/huma/v2"

	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/mcpserver"
	"go.kenn.io/forge/internal/server/httpapi"
	"go.kenn.io/forge/worklist"
)

func (h *Handler) registerImport(api huma.API) {
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

// importWorklist creates the list a link's fragment carries, or returns the
// list an earlier import of the same list made. Items this Forge does not
// track or has not synced, and repeats of an earlier item, are skipped rather
// than rejecting the list.
func (h *Handler) importWorklist(
	ctx context.Context, input *importWorklistInput,
) (*httpapi.BodyOutput[WorklistImportResponse], error) {
	list, err := worklist.ParseFragment(input.Body.Fragment)
	if fieldErr, ok := errors.AsType[*worklist.FieldError](err); ok {
		return nil, httpapi.Validation("body.fragment", fmt.Sprintf("the link's %s", fieldErr.Error()))
	}
	if err != nil {
		return nil, httpapi.Validation("body.fragment", err.Error())
	}
	repos, err := h.importableRepos(ctx, list)
	if err != nil {
		return nil, err
	}
	// Two links carrying the same list are the same link, however their
	// producers compressed it.
	encoded, err := json.Marshal(list)
	if err != nil {
		return nil, httpapi.Internal("encode worklist link key failed")
	}
	put := mcpserver.WorklistPut{
		Title: strings.TrimSpace(list.Title), Recipe: list.Recipe, CreatedBy: strings.TrimSpace(list.CreatedBy),
		LinkKey: fmt.Sprintf("%x", sha256.Sum256(encoded)),
	}
	for i, item := range list.Items {
		if repos[i] == nil {
			continue
		}
		identity := repos[i].Identity()
		put.Items = append(put.Items, mcpserver.WorklistPutItem{
			Item: mcpserver.ItemIdentity{
				Type: item.Item.Type, Provider: identity.Provider, PlatformHost: identity.PlatformHost,
				RepoKey: identity.Key, Owner: repos[i].Owner, Name: repos[i].Name, Number: item.Item.Number,
			},
			Score: item.Score, Section: strings.TrimSpace(item.Section), Note: item.Note, Caveat: item.Caveat,
			CaveatLevel: item.CaveatLevel,
		})
	}
	var worklistID int64
	if len(put.Items) > 0 {
		result, err := h.put(ctx, put)
		if err != nil {
			return nil, err
		}
		worklistID = result.WorklistID
	} else {
		// A list made from this link stays reachable after its items go away.
		reopened, err := h.db.ReopenLinkedWorklist(ctx, put.LinkKey)
		if errors.Is(err, db.ErrWorklistNotFound) {
			return nil, httpapi.Validation("body.fragment", "no item in the link is a pull request or issue this Forge tracks")
		}
		if err != nil {
			return nil, httpapi.Internal("reopen linked worklist failed")
		}
		h.PublishChanged(reopened.ID)
		worklistID = reopened.ID
	}
	return &httpapi.BodyOutput[WorklistImportResponse]{Body: WorklistImportResponse{
		WorklistID: worklistID, ItemCount: len(put.Items), SkippedCount: len(list.Items) - len(put.Items),
	}}, nil
}

// importableRepos returns, per item, the tracked repository it names, or nil
// when the item is skipped: its repository is not tracked, the pull request
// or issue is not a visible synced row, or an earlier item names it.
func (h *Handler) importableRepos(ctx context.Context, list worklist.List) ([]*db.ActiveRepo, error) {
	tracked, err := h.TrackedRepoIDs(ctx)
	if err != nil {
		return nil, err
	}
	repos := make([]*db.ActiveRepo, len(list.Items))
	var pullItems, issueItems []db.RepoItemNumber
	for i, item := range list.Items {
		repo, err := h.lookupImportRepo(ctx, item.Item)
		if errors.Is(err, errImportRepoNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if tracked != nil && !slices.Contains(tracked, repo.ID) {
			continue
		}
		repos[i] = repo
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
	visible := make(map[db.WorkspaceSubjectKey]bool, len(pulls)+len(issues))
	for _, pull := range pulls {
		visible[db.WorkspaceSubjectKey{RepoID: pull.RepoID, ItemType: db.ItemTypePR, ItemNumber: pull.Number}] = true
	}
	for _, issue := range issues {
		visible[db.WorkspaceSubjectKey{RepoID: issue.RepoID, ItemType: db.ItemTypeIssue, ItemNumber: issue.Number}] = true
	}
	for i, item := range list.Items {
		if repos[i] == nil {
			continue
		}
		subject := db.WorkspaceSubjectKey{RepoID: repos[i].ID, ItemType: item.Item.Type, ItemNumber: item.Item.Number}
		if !visible[subject] {
			repos[i] = nil
			continue
		}
		// The first item naming a pull request or issue keeps it.
		visible[subject] = false
	}
	return repos, nil
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
