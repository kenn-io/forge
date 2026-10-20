package mcpserver

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"go.kenn.io/forge/platform"
)

const (
	worklistMaxTitle   = 200
	worklistMaxRecipe  = 10000
	worklistMaxItems   = 200
	worklistMaxSection = 80
	worklistMaxText    = 4000
)

type putWorklistItemInput struct {
	Item    itemRefInput `json:"item"`
	Score   *int         `json:"score,omitempty" jsonschema:"importance from 1 to 10; omit when unscored"`
	Section string       `json:"section,omitempty" jsonschema:"group label such as Ready or Needs review; readiness belongs here, not in the score"`
	Note    string       `json:"note,omitempty" jsonschema:"why the item is on the list"`
	Caveat  string       `json:"caveat,omitempty" jsonschema:"what the maintainer should check before acting"`
}

type putWorklistInput struct {
	WorklistID int64                  `json:"worklist_id,omitempty" jsonschema:"existing worklist to replace; omit to create a new worklist"`
	Title      string                 `json:"title"`
	Recipe     string                 `json:"recipe" jsonschema:"the criteria in plain text and the searches you ran, so a later agent can refresh or copy the list"`
	CreatedBy  string                 `json:"created_by,omitempty" jsonschema:"short label for the agent writing the list"`
	Items      []putWorklistItemInput `json:"items" jsonschema:"items in rank order, 1 to 200"`
}

type worklistIDInput struct {
	WorklistID int64 `json:"worklist_id"`
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
			"and the count remaining: items neither handled nor gone. " +
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
	for i, item := range in.Items {
		identity, err := item.Item.itemIdentity()
		if err != nil {
			return WorklistPutResult{}, worklistFieldError(fmt.Sprintf("items[%d].item", i), err.Error())
		}
		put.Items = append(put.Items, WorklistPutItem{
			Item: identity, Score: item.Score,
			Section: strings.TrimSpace(item.Section), Note: item.Note, Caveat: item.Caveat,
		})
	}
	if err := ValidateWorklistPut(put); err != nil {
		return WorklistPutResult{}, err
	}
	return s.backend.PutWorklist(ctx, put)
}

func (s *Server) getWorklist(ctx context.Context, in worklistIDInput) (Worklist, error) {
	if in.WorklistID <= 0 {
		return Worklist{}, worklistFieldError("worklist_id", "worklist_id is required")
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
		return WorklistDismissal{}, worklistFieldError("worklist_id", "worklist_id is required")
	}
	return s.backend.DismissWorklist(ctx, in.WorklistID)
}

func (s *Server) clearWorklistMarks(ctx context.Context, in worklistIDInput) (WorklistMarksCleared, error) {
	if in.WorklistID <= 0 {
		return WorklistMarksCleared{}, worklistFieldError("worklist_id", "worklist_id is required")
	}
	return s.backend.ClearWorklistMarks(ctx, in.WorklistID)
}

func worklistFieldError(field, message string) *Error {
	return &Error{
		Kind: "invalid_request", Code: ErrorCodeValidation, Message: message,
		Details: map[string]any{"field": field},
	}
}

func ValidateWorklistPut(put WorklistPut) error {
	if put.WorklistID < 0 {
		return worklistFieldError("worklist_id", "worklist_id must be positive")
	}
	if strings.TrimSpace(put.Title) == "" {
		return worklistFieldError("title", "title is required")
	}
	if utf8.RuneCountInString(put.Title) > worklistMaxTitle {
		return worklistFieldError("title", fmt.Sprintf("title must be at most %d characters", worklistMaxTitle))
	}
	if utf8.RuneCountInString(put.Recipe) > worklistMaxRecipe {
		return worklistFieldError("recipe", fmt.Sprintf("recipe must be at most %d characters", worklistMaxRecipe))
	}
	if len(put.Items) == 0 || len(put.Items) > worklistMaxItems {
		return worklistFieldError("items", fmt.Sprintf("items must contain 1 to %d entries", worklistMaxItems))
	}
	seen := make(map[worklistItemKey]int, len(put.Items))
	for i, item := range put.Items {
		field := fmt.Sprintf("items[%d]", i)
		if item.Item.Type != "pr" && item.Item.Type != "issue" {
			return worklistFieldError(field+".item.type", "item type must be pr or issue")
		}
		if item.Score != nil && (*item.Score < 1 || *item.Score > 10) {
			return worklistFieldError(field+".score", "score must be from 1 to 10")
		}
		if utf8.RuneCountInString(item.Section) > worklistMaxSection {
			return worklistFieldError(field+".section", fmt.Sprintf("section must be at most %d characters", worklistMaxSection))
		}
		if utf8.RuneCountInString(item.Note) > worklistMaxText {
			return worklistFieldError(field+".note", fmt.Sprintf("note must be at most %d characters", worklistMaxText))
		}
		if utf8.RuneCountInString(item.Caveat) > worklistMaxText {
			return worklistFieldError(field+".caveat", fmt.Sprintf("caveat must be at most %d characters", worklistMaxText))
		}
		key := newWorklistItemKey(item.Item)
		if first, ok := seen[key]; ok {
			err := worklistFieldError(field+".item", "item appears more than once")
			err.Details["duplicate_of"] = first
			return err
		}
		seen[key] = i
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
