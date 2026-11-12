import { Duration, Effect, Schedule } from "effect";
import { ApiProblemError, TransientTransportError } from "./effect-errors.js";
import { InvalidGeneratedErrorResponse } from "./runtime.js";

export const transientRetrySchedule = Schedule.exponential("500 millis").pipe(
  Schedule.jittered,
  Schedule.upTo({ times: 2 }),
);

export const onboardingRetrySchedule = Schedule.exponential("500 millis").pipe(
  Schedule.jittered,
  Schedule.upTo({ times: 5 }),
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
  if (isTransientFailure(failure)) {
    if (failure.cause instanceof InvalidGeneratedErrorResponse) return [502, 503, 504].includes(failure.cause.status);
    return !(failure.cause instanceof SyntaxError);
  }
  return (
    failure instanceof ApiProblemError &&
    ["serviceUnavailable", "hubUnavailable", "upstreamError"].includes(failure.problem.code)
  );
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
