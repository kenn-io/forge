import { Clock, Effect, Queue, Ref, Stream } from "effect";
import { executeOpaqueGeneratedApiRequest } from "../api/generated-api.js";

const windowFocus = Stream.callback<void>(
  (queue) =>
    Effect.gen(function* () {
      const onFocus = () => {
        Queue.offerUnsafe(queue, undefined);
      };
      window.addEventListener("focus", onFocus);
      yield* Effect.addFinalizer(() => Effect.sync(() => window.removeEventListener("focus", onFocus)));
    }),
  { bufferSize: 1, strategy: "dropping" },
);

const utcDay = (millis: number) => new Date(millis).toISOString().slice(0, 10);

/** Reports app_opened now and on the first window focus of each later UTC day, for the app's lifetime. */
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
  yield* reportIfNewDay;
  yield* Stream.runForEach(windowFocus, () => reportIfNewDay);
});
