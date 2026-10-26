package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"go.kenn.io/forge/platform"
	"go.kenn.io/forge/worklist"
)

// putWorklistInput is the shared worklist shape that links also carry,
// plus the list to replace.
type putWorklistInput struct {
	WorklistID int64 `json:"worklist_id,omitempty" jsonschema:"existing worklist to replace; omit to create a new worklist"`
	worklist.List
}

type worklistIDInput struct {
	WorklistID int64 `json:"worklist_id"`
}

type importWorklistInput struct {
	Link string `json:"link" jsonschema:"a worklist link: kenn://kenn-forge/worklists/import#v1..., an http(s) URL ending in /worklists/import#v1..., or the v1... fragment alone"`
}

type listWorklistsInput struct {
	IncludeDismissed bool `json:"include_dismissed,omitempty"`
}

type listWorklistsOutput struct {
	Worklists []WorklistSummary `json:"worklists"`
}

// Worklist reads embed repository identities whose keys JSON flattens at
// marshal time, so their output schemas stay open objects.
func (s *Server) registerWorklistTools() {
	mcp.AddTool(s.mcp, &mcp.Tool{
		Name: "kenn_forge_put_worklist",
		Meta: mcp.Meta{"ui": map[string]any{"visibility": []string{"model"}}},
		Description: "Write a ranked worklist of synced PRs and issues for the maintainer to work through. " +
			"Passing worklist_id replaces that list's title, recipe and items. Omitting it creates a new list; " +
			"to make another one like an existing list, read its recipe with kenn_forge_get_worklist and put without an id. " +
			"Forge stores the recipe and shows it but never evaluates it. Every item must be a PR or issue Forge has synced; " +
			"copy identities from Forge reads. Forge records each item's current state so later reads report what changed. " +
			"Items the maintainer marked handled keep the mark when they stay on a replaced list; " +
			"call kenn_forge_clear_worklist_marks only when the maintainer asks to clear the marks. " +
			"Never calls provider APIs.",
	}, wrapTool(s.putWorklist))
	mcp.AddTool(s.mcp, &mcp.Tool{
		Name: "kenn_forge_get_worklist", OutputSchema: map[string]any{"type": "object"},
		Description: "Read a worklist with its recipe and, for each item, what Forge recorded when it was written, " +
			"its live synced state, freshness (fresh, changed or gone), the changes since, and handled_at, " +
			"when the maintainer marked it handled (null when unhandled). Handled is independent of freshness. " +
			"Freshness shows where to look first; to refresh, rerun the recipe's searches and put the whole list again. " +
			"Changes never certify mergeability.",
	}, wrapTool(s.getWorklist))
	mcp.AddTool(s.mcp, &mcp.Tool{
		Name: "kenn_forge_list_worklists", OutputSchema: map[string]any{"type": "object"},
		Description: "List worklists, most recently updated first, with item, changed, gone and handled counts, " +
			"and the count remaining: items neither handled nor merged, closed, or gone. " +
			"Dismissed lists are omitted unless include_dismissed is true.",
	}, wrapTool(s.listWorklists))
	mcp.AddTool(s.mcp, &mcp.Tool{
		Name: "kenn_forge_dismiss_worklist",
		Meta: mcp.Meta{"ui": map[string]any{"visibility": []string{"model"}}},
		Description: "Dismiss a worklist so it no longer appears by default. Repeating it is safe. " +
			"Writing the list again with kenn_forge_put_worklist brings it back.",
	}, wrapTool(s.dismissWorklist))
	mcp.AddTool(s.mcp, &mcp.Tool{
		Name: "kenn_forge_clear_worklist_marks",
		Meta: mcp.Meta{"ui": map[string]any{"visibility": []string{"model"}}},
		Description: "Clear every handled mark on a worklist. Call it only when the maintainer asks; " +
			"marks are the maintainer's record of what they dealt with. Returns how many marks were cleared.",
	}, wrapTool(s.clearWorklistMarks))
	mcp.AddTool(s.mcp, &mcp.Tool{
		Name: "kenn_forge_import_worklist",
		Meta: mcp.Meta{"ui": map[string]any{"visibility": []string{"model"}}},
		Description: "Create the worklist a worklist link carries, as opening the link in Forge does. " +
			"Pass a kenn:// link, an http(s) link ending in /worklists/import#v1..., or the v1... fragment. " +
			"Items Forge does not track or has not synced, and repeated items, are skipped and counted in skipped_count. " +
			"Importing the same link again returns the list it created, restoring it if dismissed. " +
			"Never calls provider APIs.",
	}, wrapTool(s.importWorklist))
}

func (s *Server) putWorklist(ctx context.Context, in putWorklistInput) (WorklistPutResult, error) {
	put := WorklistPut{
		WorklistID: in.WorklistID, Title: strings.TrimSpace(in.Title), Recipe: in.Recipe,
		CreatedBy: strings.TrimSpace(in.CreatedBy),
		Items:     make([]WorklistPutItem, 0, len(in.Items)),
	}
	if scoped, _ := ctx.Value(workspaceScopeKey{}).(string); scoped != "" {
		put.WorkspaceID = scoped
	}
	// Validate the whole put before resolving any route, so invalid input
	// fails without backend calls.
	for i, item := range in.Items {
		ref := item.Item
		identity, err := itemRefInput{
			Type: ref.Type, Provider: ref.Provider, PlatformHost: ref.PlatformHost,
			PlatformRepoID: ref.PlatformRepoID, BitbucketRepositoryUUID: ref.BitbucketRepositoryUUID,
			Owner: ref.Owner, Name: ref.Name, Number: ref.Number,
		}.normalize(fmt.Sprintf("items[%d].item.", i))
		if err != nil {
			return WorklistPutResult{}, err
		}
		put.Items = append(put.Items, WorklistPutItem{
			Item: identity, Score: item.Score,
			Section: strings.TrimSpace(item.Section), Note: item.Note, Caveat: item.Caveat,
			CaveatLevel: item.CaveatLevel,
		})
	}
	if err := validateWorklistLimits(put); err != nil {
		return WorklistPutResult{}, err
	}
	for i := range put.Items {
		identity, err := s.resolveItemKey(ctx, put.Items[i].Item)
		if err != nil {
			return WorklistPutResult{}, err
		}
		put.Items[i].Item = identity
	}
	if err := ValidateWorklistPut(put); err != nil {
		return WorklistPutResult{}, err
	}
	return s.backend.PutWorklist(ctx, put)
}

func (s *Server) getWorklist(ctx context.Context, in worklistIDInput) (Worklist, error) {
	if in.WorklistID <= 0 {
		return Worklist{}, FieldError("worklist_id", "worklist_id is required")
	}
	return s.backend.GetWorklist(ctx, in.WorklistID)
}

func (s *Server) listWorklists(ctx context.Context, in listWorklistsInput) (listWorklistsOutput, error) {
	worklists, err := s.backend.ListWorklists(ctx, in.IncludeDismissed)
	if err != nil {
		return listWorklistsOutput{}, err
	}
	if worklists == nil {
		worklists = []WorklistSummary{}
	}
	return listWorklistsOutput{Worklists: worklists}, nil
}

func (s *Server) dismissWorklist(ctx context.Context, in worklistIDInput) (WorklistDismissal, error) {
	if in.WorklistID <= 0 {
		return WorklistDismissal{}, FieldError("worklist_id", "worklist_id is required")
	}
	return s.backend.DismissWorklist(ctx, in.WorklistID)
}

func (s *Server) clearWorklistMarks(ctx context.Context, in worklistIDInput) (WorklistMarksCleared, error) {
	if in.WorklistID <= 0 {
		return WorklistMarksCleared{}, FieldError("worklist_id", "worklist_id is required")
	}
	return s.backend.ClearWorklistMarks(ctx, in.WorklistID)
}

func (s *Server) importWorklist(ctx context.Context, in importWorklistInput) (WorklistImportResult, error) {
	fragment, err := worklistLinkFragment(in.Link)
	if err != nil {
		return WorklistImportResult{}, FieldError("link", err.Error())
	}
	result, err := s.backend.ImportWorklist(ctx, fragment)
	// The import names the link by the HTTP body field that carries it.
	if backendErr, ok := errors.AsType[*Error](err); ok && backendErr.Details["field"] == "body.fragment" {
		renamed := *backendErr
		renamed.Details = maps.Clone(backendErr.Details)
		renamed.Details["field"] = "link"
		return WorklistImportResult{}, &renamed
	}
	return result, err
}

// worklistLinkFragment returns the fragment a worklist link carries after
// '#'. It accepts a kenn:// link, a Forge import page URL, or the fragment
// alone; the import decodes and validates the fragment itself.
func worklistLinkFragment(link string) (string, error) {
	link = strings.TrimSpace(link)
	base, fragment, found := strings.Cut(link, "#")
	switch {
	case !found && strings.Contains(link, "://"):
		return "", errors.New("the link carries no worklist after '#'")
	case !found:
		fragment = link
	case base != "" && base != worklist.KennImportURL && !isWorklistImportPage(base):
		return "", fmt.Errorf("not a worklist link: expected %s#v1..., an http(s) URL ending in %s#v1..., or the v1... fragment",
			worklist.KennImportURL, worklist.ImportPath)
	}
	if fragment == "" {
		return "", errors.New("the link carries no worklist after '#'")
	}
	return fragment, nil
}

func isWorklistImportPage(raw string) bool {
	page, err := url.Parse(raw)
	if err != nil {
		return false
	}
	return (page.Scheme == "http" || page.Scheme == "https") && page.Host != "" &&
		strings.HasSuffix(page.Path, worklist.ImportPath)
}

// ValidateWorklistPut checks a resolved put against the shared worklist
// limits and rejects an item that appears twice under one repository
// identity.
func ValidateWorklistPut(put WorklistPut) error {
	if err := validateWorklistLimits(put); err != nil {
		return err
	}
	seen := make(map[worklistItemKey]int, len(put.Items))
	for i, item := range put.Items {
		key := newWorklistItemKey(item.Item)
		if first, ok := seen[key]; ok {
			err := FieldError(fmt.Sprintf("items[%d].item", i), "item appears more than once")
			err.Details["duplicate_of"] = first
			return err
		}
		seen[key] = i
	}
	return nil
}

// validateWorklistLimits checks the shared worklist limits. It needs no
// repository keys, so a put runs it before resolving any route.
func validateWorklistLimits(put WorklistPut) error {
	if put.WorklistID < 0 {
		return FieldError("worklist_id", "worklist_id must be positive")
	}
	list := worklist.List{Title: put.Title, Recipe: put.Recipe, Items: make([]worklist.Item, 0, len(put.Items))}
	for _, item := range put.Items {
		list.Items = append(list.Items, worklist.Item{
			Item: worklist.ItemRef{
				Type: item.Item.Type, Provider: item.Item.Provider, PlatformHost: item.Item.PlatformHost,
				Owner: item.Item.Owner, Name: item.Item.Name, Number: item.Item.Number,
			},
			Score: item.Score, Section: item.Section, Note: item.Note, Caveat: item.Caveat,
			CaveatLevel: item.CaveatLevel,
		})
	}
	if err := list.Validate(); err != nil {
		if fieldErr, ok := errors.AsType[*worklist.FieldError](err); ok {
			return FieldError(fieldErr.Field, fieldErr.Message)
		}
		return err
	}
	return nil
}

type worklistItemKey struct {
	repository platform.RepositoryIdentity
	itemType   string
	number     int
}

func newWorklistItemKey(item ItemIdentity) worklistItemKey {
	repository := platform.RepositoryIdentity{
		Provider: item.Provider, PlatformHost: item.PlatformHost, Key: item.RepoKey,
	}.Canonical()
	if repository.PlatformHost == "" {
		repository.PlatformHost, _ = platform.DefaultHost(platform.Kind(repository.Provider))
	}
	return worklistItemKey{repository: repository, itemType: item.Type, number: item.Number}
}
