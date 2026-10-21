import { Effect } from "effect";
import { ApiProblemError, type TransientTransportError } from "./effect-errors.js";
import { executeGeneratedApiRequest, type GeneratedApi } from "./generated-api.js";
import type {
  WorklistChangesResponse,
  WorklistHandledResponse,
  WorklistImportItemResponse,
  WorklistImportPreviewResponse,
  WorklistImportResponse,
  WorklistItemMarkResponse,
  WorklistItemRef,
  WorklistItemResponse,
  WorklistResponse,
  WorklistSummaryResponse,
} from "./generated/models/index.js";
import { ProblemCodes } from "./problems.js";
import { repositoryKeyFromWire, repositoryKeyToWire } from "./repository-key.js";
import { retryIdempotentRead } from "./retry-policy.js";
import { apiErrorMessage } from "./runtime.js";

export type WorklistSummary = WorklistSummaryResponse;
export type Worklist = WorklistResponse;
export type WorklistItem = WorklistItemResponse;
export type WorklistChanges = WorklistChangesResponse;
export type WorklistMarks = WorklistHandledResponse;
export type WorklistItemMark = WorklistItemMarkResponse;
export type WorklistImportPreview = WorklistImportPreviewResponse;
export type WorklistImportItem = WorklistImportItemResponse;
export type WorklistImport = WorklistImportResponse;

export type WorklistFailure = ApiProblemError | TransientTransportError;

export function listWorklists(): Effect.Effect<WorklistSummary[], WorklistFailure, GeneratedApi> {
  return executeGeneratedApiRequest("list worklists", (client, signal) =>
    client.WorklistsService.listWorklists(undefined, { signal }),
  ).pipe(
    retryIdempotentRead,
    Effect.map((response) => response.worklists),
  );
}

export function getWorklist(id: number) {
  return executeGeneratedApiRequest("get worklist", (client, signal) =>
    client.WorklistsService.getWorklist({ id }, { signal }),
  ).pipe(retryIdempotentRead);
}

export function dismissWorklist(id: number) {
  return executeGeneratedApiRequest("dismiss worklist", (client, signal) =>
    client.WorklistsService.dismissWorklist({ id }, { signal }),
  );
}

export function undismissWorklist(id: number) {
  return executeGeneratedApiRequest("undismiss worklist", (client, signal) =>
    client.WorklistsService.undismissWorklist({ id }, { signal }),
  );
}

/** Reads the list a worklist link's fragment carries. Nothing is written. */
export function previewWorklistImport(
  fragment: string,
): Effect.Effect<WorklistImportPreview, WorklistFailure, GeneratedApi> {
  return executeGeneratedApiRequest("preview worklist import", (client, signal) =>
    client.WorklistsService.previewWorklistImport({ fragment }, { signal }),
  ).pipe(retryIdempotentRead);
}

/** Creates the list a worklist link's fragment carries. Each call creates a new list. */
export function importWorklist(fragment: string): Effect.Effect<WorklistImport, WorklistFailure, GeneratedApi> {
  return executeGeneratedApiRequest("import worklist", (client, signal) =>
    client.WorklistsService.importWorklist({ fragment }, { signal }),
  );
}

export function worklistItemRef(item: WorklistItem): WorklistItemRef {
  return {
    provider: item.repo.provider,
    platform_host: item.repo.platform_host,
    ...repositoryKeyToWire(repositoryKeyFromWire(item.repo)),
    item_type: item.item_type,
    number: item.number,
  };
}

export function setWorklistItemsHandled(
  id: number,
  items: readonly WorklistItem[],
  handled: boolean,
): Effect.Effect<WorklistMarks, WorklistFailure, GeneratedApi> {
  return executeGeneratedApiRequest("set worklist items handled", (client, signal) =>
    client.WorklistsService.setWorklistItemsHandled({ id }, { handled, items: items.map(worklistItemRef) }, { signal }),
  );
}

export function isWorklistNotFound(failure: unknown): boolean {
  return failure instanceof ApiProblemError && failure.problem.code === ProblemCodes.worklistNotFound;
}

export function worklistFailureMessage(failure: WorklistFailure, fallback: string): string {
  return failure instanceof ApiProblemError ? apiErrorMessage(failure.problem, fallback) : fallback;
}
