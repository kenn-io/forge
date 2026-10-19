import type { VisitEvent } from "@kenn-io/kit-ui/utils/browser-telemetry";
import { onTestFinished } from "vite-plus/test";
import type { AppRuntime } from "../app/runtime.js";
import { startRuntimeTelemetry } from "../app/telemetry.js";

export function startTestTelemetry(runtime: AppRuntime, capture: (event: VisitEvent, options?: unknown) => unknown) {
  const reporter = startRuntimeTelemetry(runtime, {
    storageKey: `test.telemetry.${crypto.randomUUID()}`,
    post: async (_route, event) => {
      if (event.event !== "app_opened") await capture(event, {});
      return { status: 202, data: { status: "queued" } };
    },
  });
  onTestFinished(() => reporter.stop());
  const ready = new Promise<void>((resolve) => {
    reporter.onQuietChange((quiet) => {
      if (!quiet) resolve();
    });
  });
  return { ...reporter, ready };
}
