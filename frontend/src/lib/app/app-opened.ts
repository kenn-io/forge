import { Clock, Effect, Ref, Stream } from "effect";
import { executeOpaqueGeneratedApiRequest } from "../api/generated-api.js";
import { waitUntilBackendReady } from "../utils/backendReadiness.js";

const utcDay = (millis: number) => new Date(millis).toISOString().slice(0, 10);

/** Reports app_opened once the daemon is ready and on the first window focus of each later UTC day, for the app's lifetime. */
export const reportAppOpened = Effect.gen(function* () {
  const lastDay = yield* Ref.make("");
  const reportIfNewDay = Effect.gen(function* () {
    const day = utcDay(yield* Clock.currentTimeMillis);
    if ((yield* Ref.getAndSet(lastDay, day)) === day) return;
    // Never block focus observation on a slow POST; the child dies with the reporter.
    yield* Effect.forkChild(
      executeOpaqueGeneratedApiRequest("POST /telemetry/events", (client, signal) =>
        client.SystemService.captureTelemetryEvent({ event: "app_opened" }, { signal }),
      ).pipe(Effect.ignore),
    );
  });
  // A daemon still starting answers 503, which would spend the day on a lost event.
  yield* waitUntilBackendReady;
  yield* reportIfNewDay;
  yield* Stream.runForEach(Stream.fromEventListener(window, "focus", { bufferSize: 1 }), () => reportIfNewDay);
});
