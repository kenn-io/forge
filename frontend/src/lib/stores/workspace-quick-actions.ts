import { Deferred, Effect } from "effect";
import { SvelteMap, SvelteSet } from "svelte/reactivity";
import type { LaunchTarget, QuickAction, RuntimeSession } from "../api/types.js";
import type { AppRuntime } from "../app/runtime.js";
import { reportWorkspaceOpened } from "../app/telemetry.js";
import { showWorkspaceLaunchError } from "../app/workspace-telemetry.js";
import type { WorkspaceDetail } from "../components/terminal/workspace-detail.js";
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

/** How one quick action launch ended. */
export type QuickActionOutcome =
  | { type: "started" }
  /** The agent may be running, but its prompt was not confirmed. */
  | { type: "unconfirmed"; reason: string; message: string }
  | { type: "failed"; reason: string; message: string };

// Launches for one workspace run one after another, in the order requested.
const launchQueues = new Map<string, Deferred.Deferred<void>>();

function enqueueLaunch<A, R>(workspaceKey: string, launch: Effect.Effect<A, never, R>): Effect.Effect<A, never, R> {
  return Effect.gen(function* () {
    const previous = launchQueues.get(workspaceKey);
    const done = yield* Deferred.make<void>();
    launchQueues.set(workspaceKey, done);
    return yield* (previous ? Deferred.await(previous) : Effect.void).pipe(
      Effect.andThen(launch),
      Effect.ensuring(
        Effect.gen(function* () {
          yield* Deferred.succeed(done, undefined);
          if (launchQueues.get(workspaceKey) === done) launchQueues.delete(workspaceKey);
        }),
      ),
    );
  });
}

/**
 * Launches a quick action's agent in a workspace and delivers its prompt as
 * the initial message; the server waits for the workspace to become ready.
 * Another launch for the same workspace waits its turn instead of being
 * dropped. An open workspace can select the returned session immediately via
 * onLaunched; ordinary workspace runtime events reconcile it for other views.
 */
export function launchWorkspaceQuickAction(
  workspaceId: string,
  action: QuickAction,
  hostKey?: string,
  onLaunched?: (session: RuntimeSession) => void,
  leaf?: WorkspaceRuntimeLaunchLeaf,
) {
  const workspaceKey = quickActionWorkspaceKey(workspaceId, hostKey);
  quickActionWorkspaces.add(workspaceKey);
  const launch = { targetKey: action.agent, ...(leaf === undefined ? {} : { leaf }) };
  const handoff = Effect.sync(() => quickActionLaunches.set(workspaceKey, launch)).pipe(
    Effect.andThen(
      executeGeneratedApiRequest("POST workspace agent handoff", (client, signal) =>
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
      ),
    ),
    Effect.match({
      onSuccess: (result): QuickActionOutcome => {
        if (quickActionLaunches.get(workspaceKey) === launch) {
          quickActionLaunches.set(workspaceKey, { ...launch, session: result.session });
        }
        onLaunched?.(result.session);
        const state = result.initial_message.state;
        if (state === "delivered") return { type: "started" };
        return {
          type: "unconfirmed",
          reason: `the prompt delivery is ${state}`,
          message: `"${action.label}" launched, but the prompt delivery is ${state}.`,
        };
      },
      onFailure: (failure): QuickActionOutcome => {
        if (quickActionLaunches.get(workspaceKey) === launch) quickActionLaunches.delete(workspaceKey);
        if (failure._tag !== "ApiProblemError") {
          // The request itself failed. The server keeps running an accepted
          // handoff, so this is reported as unknown rather than as a failure.
          return {
            type: "unconfirmed",
            reason: "lost contact with Kenn Forge; check the workspace for the agent",
            message: `"${action.label}": lost contact with Kenn Forge; check the workspace for the agent.`,
          };
        }
        const detail = failure.problem.detail ?? failure.problem.title ?? "agent handoff failed";
        // A post-launch failure carries the live session: the agent is running
        // but did not get its prompt, which is a different situation from a
        // handoff that never started.
        const sessionKey = failure.problem.details?.["session_key"];
        if (typeof sessionKey === "string" && sessionKey !== "") {
          // "not_delivered" is the only definite outcome. Anything else means
          // the write may have happened, so sending the prompt again could
          // duplicate the instructions.
          return failure.problem.details?.["initial_message_state"] === "not_delivered"
            ? {
                type: "unconfirmed",
                reason: `the agent launched, but the prompt was not delivered: ${detail}`,
                message: `"${action.label}" launched its agent, but the prompt was not delivered: ${detail}`,
              }
            : {
                type: "unconfirmed",
                reason: `the agent launched, but prompt delivery is unconfirmed: ${detail}`,
                message: `"${action.label}" launched its agent, but prompt delivery is unconfirmed: ${detail}. Check the agent before sending the prompt again.`,
              };
        }
        return { type: "failed", reason: detail, message: `"${action.label}" could not start: ${detail}` };
      },
    }),
  );
  return enqueueLaunch(workspaceKey, handoff);
}

/** Runs a quick action in the background and reports anything but a clean start. */
export function runWorkspaceQuickAction(
  runtime: AppRuntime,
  workspaceId: string,
  itemType: WorkspaceDetail["item_type"] | undefined,
  action: QuickAction,
  hostKey?: string,
  onLaunched?: (session: RuntimeSession) => void,
  leaf?: WorkspaceRuntimeLaunchLeaf,
): void {
  if (itemType !== undefined) reportWorkspaceOpened(runtime, itemType);
  runtime.runCommand(
    launchWorkspaceQuickAction(workspaceId, action, hostKey, onLaunched, leaf).pipe(
      Effect.flatMap((outcome) =>
        Effect.sync(() => {
          if (outcome.type === "started") return;
          if (outcome.type === "failed") showWorkspaceLaunchError(outcome.message);
          else showFlash(outcome.message, { tone: "warning" });
        }),
      ),
    ),
    {
      operation: "run workspace quick action",
      safeContext: { workspaceId, agent: action.agent },
      onFailure: () => {
        showWorkspaceLaunchError(`"${action.label}" could not start.`);
      },
    },
  );
}
