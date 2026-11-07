import { Effect } from "effect";
import type { AppRuntime } from "../app/runtime.js";
import type { ServerToolingStatus } from "./tooling-status-workflow.js";

export type ToolingStatusValue = ServerToolingStatus;
import { ToolingStatusWorkflow } from "./tooling-status-workflow.js";

let fetched = $state<ToolingStatusValue | undefined>(undefined);

export function resolveToolingStatus(runtime: AppRuntime): ToolingStatusValue | undefined {
  runtime.runCommand(
    Effect.gen(function* () {
      const workflow = yield* ToolingStatusWorkflow;
      const status = yield* workflow.load;
      yield* Effect.sync(() => {
        if (status !== undefined) fetched = status;
      });
    }),
    { operation: "load tooling status", safeContext: {}, onFailure: () => {} },
  );
  return fetched;
}

// A token the credential chain selects decides GitHub readiness, as it does for
// sync; gh decides only when no token is selected.
export function githubAccount(tooling: ToolingStatusValue | undefined): { ready: boolean; user?: string | undefined } {
  if (tooling?.github_token?.source) return { ready: tooling.github_token.ready };
  const gh = tooling?.gh;
  return { ready: gh?.available === true && gh.authenticated === true, user: gh?.user };
}

// Tests dispose their application runtime before resetting this projection.
export function resetToolingStatusForTest(): void {
  fetched = undefined;
}
