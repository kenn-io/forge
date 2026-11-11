import { Duration, Effect, Schedule } from "effect";
import { ApiProblemError, TransientTransportError } from "./effect-errors.js";

export const transientRetrySchedule = Schedule.exponential("500 millis").pipe(
  Schedule.jittered,
  Schedule.upTo({ times: 2 }),
);

export const reconnectSchedule = Schedule.exponential("500 millis").pipe(
  Schedule.modifyDelay(({ duration }) => Effect.succeed(Duration.min(duration, Duration.seconds(30)))),
);

export function isTransientFailure(failure: unknown): failure is TransientTransportError {
  return (
    typeof failure === "object" && failure !== null && "_tag" in failure && failure._tag === "TransientTransportError"
  );
}

export function isTransientPickerReadFailure(failure: unknown): boolean {
  return isTransientFailure(failure) || (failure instanceof ApiProblemError && failure.problem.status === 503);
}

export const retryIdempotentRead = <A, E, R>(
  effect: Effect.Effect<A, E, R>,
  isRetryable: (failure: unknown) => boolean = isTransientFailure,
) =>
  effect.pipe(
    Effect.retry({
      schedule: transientRetrySchedule,
      while: isRetryable,
    }),
  );
