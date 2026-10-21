import type { AppRuntime } from "./runtime.js";
import { reportOutcome } from "./telemetry.js";
import { showFlash } from "../stores/flash.svelte.js";

export function showWorkspaceLaunchError(runtime: AppRuntime, message: string): void {
  showFlash(message, { tone: "danger" });
  reportWorkspaceLaunchError(runtime);
}

export function reportWorkspaceLaunchError(runtime: AppRuntime): void {
  reportOutcome(runtime, { event: "error_shown", kind: "workspace_opened" });
}
