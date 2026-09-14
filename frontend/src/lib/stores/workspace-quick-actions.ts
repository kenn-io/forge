import { Effect } from "effect";
import type { QuickAction } from "../api/types.js";
import type { AppRuntime } from "../app/runtime.js";
import { executeGeneratedApiRequest } from "../api/generated-api.js";
import { showFlash } from "./flash.svelte.js";

/**
 * Runs a configured quick action against a workspace that was just created (or
 * reused): the server waits for the workspace to become ready, launches the
 * action's agent, and delivers the prompt as its initial message. Nothing is
 * queued in the frontend launch state; the runtime session arrives through the
 * ordinary workspace runtime events.
 */
export function runWorkspaceQuickAction(runtime: AppRuntime, workspaceId: string, action: QuickAction): void {
  const program = executeGeneratedApiRequest("POST workspace agent handoff", (client, signal) =>
    client.WorkspacesService.launchWorkspaceAgentHandoff(
      { id: workspaceId },
      { target_key: action.agent, message: action.prompt },
      { signal },
    ),
  ).pipe(
    Effect.tap((result) =>
      Effect.sync(() => {
        if (result.initial_message.state !== "delivered") {
          showFlash(`"${action.label}" launched, but the prompt delivery is ${result.initial_message.state}.`, {
            tone: "warning",
          });
        }
      }),
    ),
  );
  runtime.runCommand(program, {
    operation: "run workspace quick action",
    safeContext: { workspaceId, agent: action.agent },
    onFailure: (failure) => {
      const detail =
        failure._tag === "ApiProblemError"
          ? (failure.problem.detail ?? failure.problem.title ?? "agent handoff failed")
          : "Could not reach Kenn Forge";
      showFlash(`"${action.label}" could not start: ${detail}`, { tone: "danger" });
    },
  });
}
