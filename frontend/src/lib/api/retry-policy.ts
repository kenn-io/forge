import { Effect, Schedule } from "effect";
import { TransientTransportError } from "./effect-errors.js";

export const transientRetrySchedule = Schedule.exponential("500 millis").pipe(
  Schedule.jittered,
  Schedule.upTo({ times: 2 }),
);

export function isTransientFailure(failure: unknown): failure is TransientTransportError {
  return (
    typeof failure === "object" && failure !== null && "_tag" in failure && failure._tag === "TransientTransportError"
  );
}

export const retryIdempotentRead = <A, E, R>(effect: Effect.Effect<A, E, R>) =>
  effect.pipe(
    Effect.retry({
      schedule: transientRetrySchedule,
      while: isTransientFailure,
    }),
  );
