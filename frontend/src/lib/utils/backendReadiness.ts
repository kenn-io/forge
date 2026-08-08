import { Effect, Schedule, Stream } from "effect";
import { openStreamingResponse } from "../browser/streaming-fetch.js";
import { getBasePath } from "../stores/router.svelte.js";

const BACKEND_READY_POLL_INTERVAL = "750 millis";

function readinessURL(): URL {
  const base = getBasePath().replace(/\/$/, "");
  return new URL(`${base}/healthz`, window.location.origin);
}

const probeBackendReadiness = Effect.fn("StartupWorkflow.probeBackendReadiness")(function* () {
  const response = yield* Effect.scoped(
    openStreamingResponse("GET /healthz", readinessURL(), {
      cache: "no-store",
      headers: { Accept: "application/json" },
    }),
  ).pipe(Effect.catchTag("TransientTransportError", () => Effect.succeed(undefined)));
  return response?.ok === true;
});

const pollBackendReadiness = Effect.fn("StartupWorkflow.waitUntilBackendReady")(function* () {
  yield* Stream.fromEffectSchedule(probeBackendReadiness(), Schedule.spaced(BACKEND_READY_POLL_INTERVAL)).pipe(
    Stream.filter((ready) => ready),
    Stream.runHead,
  );
});

export const waitUntilBackendReady = pollBackendReadiness();
