package server

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"go.kenn.io/forge/internal/db"
	"go.kenn.io/forge/internal/mcpserver"
	"go.kenn.io/forge/internal/providerplane"
	"go.kenn.io/forge/internal/server/httpapi"
	"go.kenn.io/forge/internal/server/mcpapi"
	"go.kenn.io/forge/internal/server/providerapi"
	"go.kenn.io/forge/internal/server/worklistapi"
)

func (b mcpBackend) PutWorklist(
	ctx context.Context, put mcpserver.WorklistPut,
) (mcpserver.WorklistPutResult, error) {
	if b.server.providerSource != nil {
		result, err := b.server.providerSource.PutWorklist(ctx, put)
		if err != nil {
			return mcpserver.WorklistPutResult{}, mcpapi.McpBackendMutationError(err)
		}
		return result, nil
	}
	return b.putWorklistLocal(ctx, put)
}

func (b mcpBackend) GetWorklist(ctx context.Context, id int64) (mcpserver.Worklist, error) {
	var (
		worklist worklistapi.WorklistResponse
		err      error
	)
	if b.server.providerSource != nil {
		worklist, err = b.server.providerSource.GetWorklist(ctx, id)
	} else if worklists, unavailable := b.localWorklists(); unavailable != nil {
		return mcpserver.Worklist{}, unavailable
	} else {
		worklist, err = worklists.GetService(ctx, id)
	}
	if err != nil {
		return mcpserver.Worklist{}, mcpapi.McpBackendError(err)
	}
	return mcpapi.McpWorklist(worklist), nil
}

func (b mcpBackend) ListWorklists(
	ctx context.Context, includeDismissed bool,
) ([]mcpserver.WorklistSummary, error) {
	var (
		summaries []worklistapi.WorklistSummaryResponse
		err       error
	)
	if b.server.providerSource != nil {
		summaries, err = b.server.providerSource.ListWorklists(ctx, includeDismissed)
	} else if worklists, unavailable := b.localWorklists(); unavailable != nil {
		return nil, unavailable
	} else {
		summaries, err = worklists.ListService(ctx, includeDismissed)
	}
	if err != nil {
		return nil, mcpapi.McpBackendError(err)
	}
	out := make([]mcpserver.WorklistSummary, 0, len(summaries))
	for _, summary := range summaries {
		out = append(out, mcpapi.McpWorklistSummary(summary))
	}
	return out, nil
}

func (b mcpBackend) DismissWorklist(
	ctx context.Context, id int64,
) (mcpserver.WorklistDismissal, error) {
	var (
		summary worklistapi.WorklistSummaryResponse
		err     error
	)
	if b.server.providerSource != nil {
		summary, err = b.server.providerSource.DismissWorklist(ctx, id)
		if err != nil {
			return mcpserver.WorklistDismissal{}, mcpapi.McpBackendMutationError(err)
		}
	} else {
		worklists, unavailable := b.localWorklists()
		if unavailable != nil {
			return mcpserver.WorklistDismissal{}, unavailable
		}
		release, admitErr := b.admitProviderWrite(ctx)
		if admitErr != nil {
			return mcpserver.WorklistDismissal{}, admitErr
		}
		defer release()
		summary, err = worklists.SetDismissedService(ctx, id, true)
		if err != nil {
			return mcpserver.WorklistDismissal{}, mcpapi.McpBackendMutationError(err)
		}
	}
	dismissal := mcpserver.WorklistDismissal{WorklistID: summary.ID}
	if summary.DismissedAt != nil {
		dismissal.DismissedAt = *summary.DismissedAt
	}
	return dismissal, nil
}

func (b mcpBackend) ClearWorklistMarks(
	ctx context.Context, id int64,
) (mcpserver.WorklistMarksCleared, error) {
	if b.server.providerSource != nil {
		result, err := b.server.providerSource.ClearWorklistMarks(ctx, id)
		if err != nil {
			return mcpserver.WorklistMarksCleared{}, mcpapi.McpBackendMutationError(err)
		}
		return result, nil
	}
	return b.clearWorklistMarksLocal(ctx, id)
}

func (b mcpBackend) clearWorklistMarksLocal(
	ctx context.Context, id int64,
) (mcpserver.WorklistMarksCleared, error) {
	worklists, unavailable := b.localWorklists()
	if unavailable != nil {
		return mcpserver.WorklistMarksCleared{}, unavailable
	}
	release, err := b.admitProviderWrite(ctx)
	if err != nil {
		return mcpserver.WorklistMarksCleared{}, err
	}
	defer release()
	cleared, err := worklists.ClearMarksService(ctx, id)
	if err != nil {
		return mcpserver.WorklistMarksCleared{}, mcpapi.McpBackendMutationError(err)
	}
	return mcpserver.WorklistMarksCleared{WorklistID: id, Cleared: cleared}, nil
}

// ImportWorklist creates the list a link's fragment carries through the same
// import as the import page, on the hub.
func (b mcpBackend) ImportWorklist(
	ctx context.Context, fragment string,
) (mcpserver.WorklistImportResult, error) {
	var (
		response worklistapi.WorklistImportResponse
		err      error
	)
	if b.server.providerSource != nil {
		response, err = b.server.providerSource.ImportWorklist(ctx, fragment)
	} else {
		worklists, unavailable := b.localWorklists()
		if unavailable != nil {
			return mcpserver.WorklistImportResult{}, unavailable
		}
		release, admitErr := b.admitProviderWrite(ctx)
		if admitErr != nil {
			return mcpserver.WorklistImportResult{}, admitErr
		}
		defer release()
		response, err = worklists.ImportService(ctx, fragment)
	}
	if err != nil {
		return mcpserver.WorklistImportResult{}, mcpapi.McpBackendMutationError(err)
	}
	return mcpserver.WorklistImportResult{
		WorklistID: response.WorklistID, ItemCount: response.ItemCount, SkippedCount: response.SkippedCount,
	}, nil
}

// localWorklists returns the hub's worklist handler. Execution workers serve
// MCP without one, and a worker that is not a spoke has no hub to forward to.
func (b mcpBackend) localWorklists() (*worklistapi.Handler, error) {
	if b.server.worklistAPI == nil {
		return nil, mcpapi.McpBackendError(httpapi.ServiceUnavailable(
			"worklists are not available on this server",
		))
	}
	return b.server.worklistAPI, nil
}

func (b mcpBackend) admitProviderWrite(ctx context.Context) (func(), error) {
	if b.server.providerWriteGate == nil {
		return func() {}, nil
	}
	release, err := b.server.providerWriteGate.Admit(ctx)
	if err != nil {
		if errors.Is(err, providerplane.ErrSpokePreparationInProgress) {
			return nil, mcpapi.McpBackendError(providerapi.SpokePreparationProblem())
		}
		return nil, mcpapi.McpBackendMutationError(err)
	}
	return release, nil
}

func (b mcpBackend) putWorklistLocal(
	ctx context.Context, put mcpserver.WorklistPut,
) (mcpserver.WorklistPutResult, error) {
	if err := mcpserver.ValidateWorklistPut(put); err != nil {
		return mcpserver.WorklistPutResult{}, err
	}
	worklists, unavailable := b.localWorklists()
	if unavailable != nil {
		return mcpserver.WorklistPutResult{}, unavailable
	}
	release, err := b.admitProviderWrite(ctx)
	if err != nil {
		return mcpserver.WorklistPutResult{}, err
	}
	defer release()
	params := db.PutWorklistParams{
		ID: put.WorklistID, Title: put.Title, Recipe: put.Recipe,
		CreatedBy: put.CreatedBy, WorkspaceID: put.WorkspaceID, LinkKey: put.LinkKey,
		Items: make([]db.WorklistItemParams, 0, len(put.Items)), Now: b.server.now(),
	}
	tracked, err := worklists.TrackedRepoIDs(ctx)
	if err != nil {
		return mcpserver.WorklistPutResult{}, mcpapi.McpBackendError(err)
	}
	seen := make(map[db.WorkspaceSubjectKey]int, len(put.Items))
	for i, item := range put.Items {
		repo, err := b.resolveRepository(ctx, mcpapi.ItemRepositoryIdentity(item.Item))
		if err != nil {
			return mcpserver.WorklistPutResult{}, withWorklistItemIndex(err, i)
		}
		// An untracked repository's items would read back as Gone at once.
		if tracked != nil && !slices.Contains(tracked, repo.ID) {
			return mcpserver.WorklistPutResult{}, mcpapi.McpBackendError(httpapi.NotFound(
				httpapi.CodeRepoNotFound, "repository is not tracked", map[string]any{"item_index": i},
			))
		}
		key := db.WorkspaceSubjectKey{RepoID: repo.ID, ItemType: item.Item.Type, ItemNumber: item.Item.Number}
		if first, ok := seen[key]; ok {
			return mcpserver.WorklistPutResult{}, &mcpserver.Error{
				Kind: "invalid_request", Code: string(httpapi.CodeValidationError),
				Message: "item appears more than once",
				Details: map[string]any{"field": fmt.Sprintf("items[%d].item", i), "duplicate_of": first},
			}
		}
		seen[key] = i
		params.Items = append(params.Items, db.WorklistItemParams{
			RepoID: repo.ID, ItemType: item.Item.Type, ItemNumber: item.Item.Number,
			Score: item.Score, Section: item.Section, Note: item.Note, Caveat: item.Caveat,
			CaveatLevel: string(item.CaveatLevel.OrDefault()),
		})
	}
	result, err := b.server.db.PutWorklist(ctx, params)
	if errors.Is(err, db.ErrWorklistNotFound) {
		return mcpserver.WorklistPutResult{}, mcpapi.McpBackendError(worklistapi.NotFound(put.WorklistID))
	}
	if notFound, ok := errors.AsType[*db.WorklistItemNotFoundError](err); ok {
		code, message := httpapi.CodePullNotFound, "pull request not found"
		if notFound.ItemType == db.ItemTypeIssue {
			code, message = httpapi.CodeIssueNotFound, "issue not found"
		}
		return mcpserver.WorklistPutResult{}, mcpapi.McpBackendError(httpapi.NotFound(
			code, message, map[string]any{"item_index": notFound.Index},
		))
	}
	if err != nil {
		return mcpserver.WorklistPutResult{}, mcpapi.McpBackendMutationError(err)
	}
	worklists.PublishChanged(result.ID)
	return mcpserver.WorklistPutResult{
		WorklistID: result.ID, ItemCount: result.ItemCount, Created: result.Created,
	}, nil
}

func withWorklistItemIndex(err error, index int) error {
	backendErr, ok := errors.AsType[*mcpserver.Error](err)
	if !ok {
		return err
	}
	annotated := *backendErr
	annotated.Details = mcpapi.CloneMCPErrorDetails(backendErr.Details)
	annotated.Details["item_index"] = index
	return &annotated
}
