// Tooling status with a standalone fallback. Embedded, the host
// pushes its own probe into the embed config slot
// (__kenn_forge_update_tooling) and that value wins. Standalone, no
// embedder exists to fill the slot, so the first read lazily fetches
// the server's native probe from GET /api/v1/tooling-status. The
// fetch fires once per page load — the server caches probes
// internally, and tool/auth state changes rarely.

import { Effect } from "effect";
import type { AppRuntime } from "../app/runtime.js";
import { getToolingStatus, isEmbedded, type ToolingStatusValue } from "./embed-config.svelte.ts";
import { ToolingStatusWorkflow } from "./tooling-status-workflow.js";

let fetched = $state<ToolingStatusValue | undefined>(undefined);

// resolveToolingStatus returns the embedder's tooling status when one
// is configured, falling back to the server's native probe in
// standalone mode. Reading it from a $derived keeps consumers
// reactive to both the embed-config push and the fetch completing.
export function resolveToolingStatus(runtime: AppRuntime): ToolingStatusValue | undefined {
  if (isEmbedded()) {
    return getToolingStatus();
  }
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
