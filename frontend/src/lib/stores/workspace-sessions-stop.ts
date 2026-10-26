import { Effect, Result } from "effect";
import type { GeneratedApi } from "../api/generated-api.js";
import type { AppRuntime } from "../app/runtime.js";
import {
  WorkspaceRuntimePortLive,
  type WorkspaceRuntimePortFailure,
  type WorkspaceRuntimeTarget,
} from "../components/terminal/workspace-runtime-workflow.js";
import { showFlash } from "./flash.svelte.js";

const CONCURRENCY = 4;

/** A workspace whose sessions are stopped; hostKey is set for Fleet and devbox workspaces. */
export interface SessionStopWorkspace extends WorkspaceRuntimeTarget {
  readonly label: string;
}

/** How stopping one workspace's sessions ended. */
export interface SessionStopOutcome {
  readonly stopped: number;
  readonly failures: readonly string[];
}

function failureReason(failure: WorkspaceRuntimePortFailure, fallback: string): string {
  return failure._tag === "ApiProblemError"
    ? (failure.problem.detail ?? failure.problem.title ?? fallback)
    : "could not reach Kenn Forge";
}

/**
 * Stops every launched session in a workspace: agents, ACP chats, shells,
 * and commands, the same way closing each tab does. The workspace, its
 * worktree, and its base terminal stay. A session that is already gone
 * counts as stopped by someone else, not as a failure.
 */
export function stopWorkspaceSessions(
  target: WorkspaceRuntimeTarget,
): Effect.Effect<SessionStopOutcome, never, GeneratedApi> {
  return Effect.gen(function* () {
    const port = yield* WorkspaceRuntimePortLive;
    const runtime = yield* Effect.result(port.read(target));
    if (Result.isFailure(runtime)) {
      return { stopped: 0, failures: [failureReason(runtime.failure, "could not list its sessions")] };
    }
    const results = yield* Effect.forEach(
      runtime.success.sessions,
      (session) => Effect.result(port.stop(target, session.key)).pipe(Effect.map((result) => ({ session, result }))),
      { concurrency: CONCURRENCY },
    );
    let stopped = 0;
    const failures: string[] = [];
    for (const { session, result } of results) {
      if (Result.isSuccess(result)) stopped++;
      else if (!(result.failure._tag === "ApiProblemError" && result.failure.problem.status === 404)) {
        failures.push(`${session.label || session.key}: ${failureReason(result.failure, "stop failed")}`);
      }
    }
    return { stopped, failures };
  });
}

function sessionCount(count: number): string {
  return `${count} ${count === 1 ? "session" : "sessions"}`;
}

function summary(outcomes: readonly { workspace: SessionStopWorkspace; outcome: SessionStopOutcome }[]) {
  const stopped = outcomes.reduce((total, { outcome }) => total + outcome.stopped, 0);
  const where = outcomes.length === 1 ? `"${outcomes[0]!.workspace.label}"` : `${outcomes.length} workspaces`;
  const failed = outcomes.filter(({ outcome }) => outcome.failures.length > 0);
  if (failed.length === 0) {
    return {
      message:
        stopped === 0 ? `No sessions were running in ${where}.` : `Stopped ${sessionCount(stopped)} in ${where}.`,
      tone: "success" as const,
    };
  }
  const problems = failed.map(({ workspace, outcome }) => `${workspace.label}: ${outcome.failures.join("; ")}.`);
  return {
    message: `Stopped ${sessionCount(stopped)} in ${where}, but some could not be stopped. ${problems.join(" ")}`,
    tone: stopped === 0 ? ("danger" as const) : ("warning" as const),
  };
}

/**
 * Stops every launched session in each workspace, a few workspaces at a
 * time. onWorkspaceDone runs as each workspace finishes; one message then
 * reports how all of them ended.
 */
export function stopSessionsInWorkspaces<W extends SessionStopWorkspace>(
  runtime: AppRuntime,
  workspaces: readonly W[],
  onWorkspaceDone: (workspace: W) => void,
): void {
  if (workspaces.length === 0) return;
  runtime.runCommand(
    Effect.forEach(
      workspaces,
      (workspace) =>
        stopWorkspaceSessions(workspace).pipe(
          Effect.map((outcome) => ({ workspace, outcome })),
          Effect.ensuring(Effect.sync(() => onWorkspaceDone(workspace))),
        ),
      { concurrency: CONCURRENCY },
    ).pipe(
      Effect.flatMap((outcomes) =>
        Effect.sync(() => {
          const { message, tone } = summary(outcomes);
          showFlash(message, { tone });
        }),
      ),
    ),
    {
      operation: "stop workspace sessions",
      safeContext: { count: workspaces.length },
      onFailure: () => showFlash("Could not stop the sessions.", { tone: "danger" }),
    },
  );
}
