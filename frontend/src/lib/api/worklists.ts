import { Effect } from "effect";
import { ApiProblemError, type TransientTransportError } from "./effect-errors.js";
import { executeGeneratedApiRequest, type GeneratedApi } from "./generated-api.js";
import type {
  WorklistChangesResponse,
  WorklistItemResponse,
  WorklistResponse,
  WorklistSummaryResponse,
} from "./generated/models/index.js";
import { ProblemCodes } from "./problems.js";
import { retryIdempotentRead } from "./retry-policy.js";
import { apiErrorMessage } from "./runtime.js";

export type WorklistSummary = WorklistSummaryResponse;
export type Worklist = WorklistResponse;
export type WorklistItem = WorklistItemResponse;
export type WorklistChanges = WorklistChangesResponse;

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

export function isWorklistNotFound(failure: unknown): boolean {
  return failure instanceof ApiProblemError && failure.problem.code === ProblemCodes.worklistNotFound;
}

export function worklistFailureMessage(failure: WorklistFailure, fallback: string): string {
  return failure instanceof ApiProblemError ? apiErrorMessage(failure.problem, fallback) : fallback;
}
