import { Effect } from "effect";
import { SvelteMap, SvelteSet } from "svelte/reactivity";
import type { LaunchTarget, QuickAction, RuntimeSession } from "../api/types.js";
import type { AppRuntime } from "../app/runtime.js";
import type { WorkspaceRuntimeLaunchLeaf } from "../components/terminal/workspace-runtime-workflow.js";
import { executeGeneratedApiRequest } from "../api/generated-api.js";
import { showFlash } from "./flash.svelte.js";

/** Quick actions ordered by label, ignoring case; ties keep their settings order. */
export function sortQuickActionsByLabel(actions: readonly QuickAction[]): QuickAction[] {
  return actions.toSorted((a, b) => a.label.localeCompare(b.label, undefined, { sensitivity: "base" }));
}

/** The agent target a quick action launches, when it is configured. */
export function quickActionTarget(
  action: QuickAction,
  launchTargets: readonly LaunchTarget[],
): LaunchTarget | undefined {
  return launchTargets.find(
    (target) => target.key === action.agent && (target.kind === "agent" || target.kind === "acp"),
  );
}

/** Why a quick action cannot run right now, or "" when it can. */
export function quickActionDisabledReason(action: QuickAction, launchTargets: readonly LaunchTarget[]): string {
  const target = quickActionTarget(action, launchTargets);
  if (!target) return `Agent "${action.agent}" is not configured`;
  if (!target.available) return target.disabled_reason || `Agent "${action.agent}" is not available`;
  return "";
}

// Choosing a quick action replaces the automatic session picker for this workspace.
// Entries are keyed by quickActionWorkspaceKey.
export const quickActionWorkspaces = new SvelteSet<string>();

// Keep the returned session until a workspace view presents it, including
// when the handoff finishes before that view mounts.
export const quickActionLaunches = new SvelteMap<
  string,
  {
    targetKey: string;
    session?: RuntimeSession;
    leaf?: WorkspaceRuntimeLaunchLeaf;
    explicitlyMovedSessionKeys?: Set<string>;
  }
>();

/** Identifies a workspace in quickActionWorkspaces; devbox workspace IDs are only unique per host. */
export function quickActionWorkspaceKey(workspaceId: string, hostKey?: string): string {
  return hostKey ? `${hostKey}\0${workspaceId}` : workspaceId;
}

/**
 * Runs a configured quick action against a workspace that was just created (or
 * reused): the server waits for the workspace to become ready, launches the
 * action's agent, and delivers the prompt as its initial message. An open
 * workspace can select the returned session immediately via onLaunched;
 * ordinary workspace runtime events reconcile it for other views.
 */
export function runWorkspaceQuickAction(
  runtime: AppRuntime,
  workspaceId: string,
  action: QuickAction,
  hostKey?: string,
  onLaunched?: (session: RuntimeSession) => void,
  leaf?: WorkspaceRuntimeLaunchLeaf,
): void {
  const workspaceKey = quickActionWorkspaceKey(workspaceId, hostKey);
  if (quickActionLaunches.has(workspaceKey)) return;
  quickActionWorkspaces.add(workspaceKey);
  const launch = { targetKey: action.agent, ...(leaf === undefined ? {} : { leaf }) };
  quickActionLaunches.set(workspaceKey, launch);
  const program = executeGeneratedApiRequest("POST workspace agent handoff", (client, signal) =>
    hostKey?.startsWith("devbox:")
      ? client.DevboxesService.launchDevboxHandoff(
          { connectionId: hostKey.slice(7), id: workspaceId },
          { target_key: action.agent, message: action.prompt },
          { signal },
        )
      : client.WorkspacesService.launchWorkspaceAgentHandoff(
          { id: workspaceId },
          { target_key: action.agent, message: action.prompt },
          { signal },
        ),
  ).pipe(
    Effect.tap((result) =>
      Effect.sync(() => {
        if (quickActionLaunches.get(workspaceKey) === launch) {
          quickActionLaunches.set(workspaceKey, { ...launch, session: result.session });
        }
        onLaunched?.(result.session);
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
      if (quickActionLaunches.get(workspaceKey) === launch) quickActionLaunches.delete(workspaceKey);
      if (failure._tag !== "ApiProblemError") {
        // The request itself failed. The server keeps running an accepted
        // handoff, so this is reported as unknown rather than as a failure.
        showFlash(`"${action.label}": lost contact with Kenn Forge; check the workspace for the agent.`, {
          tone: "warning",
        });
        return;
      }
      const detail = failure.problem.detail ?? failure.problem.title ?? "agent handoff failed";
      // A post-launch failure carries the live session: the agent is running
      // but did not get its prompt, which is a different situation from a
      // handoff that never started.
      const sessionKey = failure.problem.details?.["session_key"];
      if (typeof sessionKey === "string" && sessionKey !== "") {
        const state = failure.problem.details?.["initial_message_state"];
        // "not_delivered" is the only definite outcome. Anything else means
        // the write may have happened, so sending the prompt again could
        // duplicate the instructions.
        const message =
          state === "not_delivered"
            ? `"${action.label}" launched its agent, but the prompt was not delivered: ${detail}`
            : `"${action.label}" launched its agent, but prompt delivery is unconfirmed: ${detail}. Check the agent before sending the prompt again.`;
        showFlash(message, { tone: "warning" });
        return;
      }
      showFlash(`"${action.label}" could not start: ${detail}`, { tone: "danger" });
    },
  });
}
