// Exports the frontend's Effect spans through the daemon, which forwards them
// to its own trace collector. The daemon sets the window flag only while it
// exports traces; otherwise spans stay in the browser as before.
import { Layer } from "effect";
import { FetchHttpClient } from "effect/http";
import { OtlpExporter, OtlpSerialization, OtlpTracer } from "effect/observability";
import { configuredAPIBaseURL } from "../api/runtime-base.js";

type TraceExportWindow = Window & { __KENN_FORGE_TRACE_EXPORT__?: boolean };

function daemonExportsTraces(): boolean {
  return typeof window !== "undefined" && (window as TraceExportWindow).__KENN_FORGE_TRACE_EXPORT__ === true;
}

export function makeTraceExportLayer(enabled = daemonExportsTraces()) {
  if (!enabled) return OtlpExporter.layerFlusher;
  return OtlpTracer.layer({
    url: `${configuredAPIBaseURL()}/telemetry/traces`,
    resource: { serviceName: "kenn-forge-web" },
  }).pipe(Layer.provide(Layer.merge(OtlpSerialization.layerJson, FetchHttpClient.layer)));
}
