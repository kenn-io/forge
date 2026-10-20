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
	} else {
		worklist, err = b.server.worklistAPI.GetService(ctx, id)
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
	} else {
		summaries, err = b.server.worklistAPI.ListService(ctx, includeDismissed)
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
		release, admitErr := b.admitProviderWrite(ctx)
		if admitErr != nil {
			return mcpserver.WorklistDismissal{}, admitErr
		}
		defer release()
		summary, err = b.server.worklistAPI.SetDismissedService(ctx, id, true)
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
	release, err := b.admitProviderWrite(ctx)
	if err != nil {
		return mcpserver.WorklistPutResult{}, err
	}
	defer release()
	params := db.PutWorklistParams{
		ID: put.WorklistID, Title: put.Title, Recipe: put.Recipe,
		CreatedBy: put.CreatedBy, WorkspaceID: put.WorkspaceID,
		Items: make([]db.WorklistItemParams, 0, len(put.Items)), Now: b.server.now(),
	}
	tracked, err := b.server.worklistAPI.TrackedRepoIDs(ctx)
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
	b.server.worklistAPI.PublishChanged(result.ID)
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
