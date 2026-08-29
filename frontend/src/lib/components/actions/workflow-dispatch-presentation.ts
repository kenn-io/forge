import { ProblemCodes } from "../../api/problems.js";
import { apiErrorMessage } from "../../api/runtime.js";
import type { components } from "../../api/generated/schema.js";
import type { WorkflowActionsError, WorkflowActionsSnapshot } from "../../stores/workflow-actions-workflow.js";

type WorkflowRun = components["schemas"]["WorkflowRunResponse"];

export type WorkflowDispatchPresentationState =
  | { readonly kind: "idle" }
  | { readonly kind: "pending" }
  | { readonly kind: "locating" }
  | { readonly kind: "succeeded"; readonly run?: WorkflowRun; readonly message?: string }
  | { readonly kind: "failed"; readonly message: string }
  | {
      readonly kind: "uncertain";
      readonly message: string;
      readonly candidates: readonly WorkflowRun[];
    }
  | { readonly kind: "conflict"; readonly reloadError?: string };

const outcomeFallback = "The workflow outcome could not be confirmed.";
const reloadFallback = "Workflow data could not be refreshed.";

export function workflowActionsErrorMessage(error: WorkflowActionsError, fallback: string): string {
  if (error._tag === "ApiProblemError") {
    return apiErrorMessage(error.problem, fallback);
  }
  if ("cause" in error && error.cause instanceof Error) return error.cause.message;
  return fallback;
}

export function workflowDispatchPresentation(
  snapshot: WorkflowActionsSnapshot | null,
  workflowId: string | null,
): WorkflowDispatchPresentationState {
  if (!workflowId) return { kind: "idle" };
  const dispatch = [...(snapshot?.dispatches ?? [])]
    .reverse()
    .find((candidate) => candidate.request.workflowId === workflowId);
  if (!dispatch) return { kind: "idle" };
  if (dispatch.kind === "pending") return { kind: "pending" };
  if (dispatch.kind === "locating") return { kind: "locating" };
  if (dispatch.kind === "succeeded") {
    return dispatch.run === undefined ? { kind: "succeeded" } : { kind: "succeeded", run: dispatch.run };
  }
  if (
    dispatch.kind === "failed" &&
    dispatch.error._tag === "ApiProblemError" &&
    dispatch.error.problem.code === ProblemCodes.conflict &&
    dispatch.error.problem.details?.["reason"] === "workflow_definition_changed"
  ) {
    const reloadError = snapshot?.catalogRefreshErrors[workflowId];
    return reloadError
      ? { kind: "conflict", reloadError: workflowActionsErrorMessage(reloadError, reloadFallback) }
      : { kind: "conflict" };
  }
  if (dispatch.kind === "failed") {
    return { kind: "failed", message: workflowActionsErrorMessage(dispatch.error, outcomeFallback) };
  }
  if (dispatch.kind === "locating_timed_out") {
    return {
      kind: "succeeded",
      message: "The provider accepted the workflow, but its run was not observed.",
    };
  }
  return {
    kind: "uncertain",
    message: workflowActionsErrorMessage(dispatch.error, outcomeFallback),
    candidates: dispatch.candidates,
  };
}
