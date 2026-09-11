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

// Tests dispose their application runtime before resetting this projection.
export function resetToolingStatusForTest(): void {
  fetched = undefined;
}
