import type { VisitEvent } from "@kenn-io/kit-ui/utils/browser-telemetry";
import { onTestFinished } from "vite-plus/test";
import { getCaptureTelemetryEventUrl } from "../api/generated/system/system.js";
import type { AppRuntime } from "../app/runtime.js";
import { startRuntimeTelemetry } from "../app/telemetry.js";

export function startTestTelemetry(runtime: AppRuntime, capture: (event: VisitEvent, options?: unknown) => unknown) {
  const reporter = startRuntimeTelemetry(runtime, {
    route: getCaptureTelemetryEventUrl(),
    surface: "web",
    storageKey: `test.telemetry.${crypto.randomUUID()}`,
    post: async (_route, event) => {
      if (event.event !== "app_opened") await capture(event, {});
      return { status: 202, data: { status: "queued" } };
    },
  });
  onTestFinished(() => reporter.stop());
  return reporter;
}
