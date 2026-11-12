import { assert, it } from "@effect/vitest";
import { Effect, Fiber, Ref } from "effect";
import { TestClock } from "effect/testing";
import { ApiProblemError, TransientTransportError } from "./effect-errors.js";
import { InvalidGeneratedErrorResponse } from "./runtime.js";
import { isTransientPickerReadFailure, retryIdempotentRead } from "./retry-policy.js";

it.effect("retries a transient idempotent read at most twice", () =>
  Effect.gen(function* () {
    const attempts = yield* Ref.make(0);
    const operation = Ref.updateAndGet(attempts, (count) => count + 1).pipe(
      Effect.flatMap(() =>
        Effect.fail(
          TransientTransportError.make({
            operation: "load settings",
            cause: new Error("temporarily unavailable"),
          }),
        ),
      ),
    );

    const fiber = yield* Effect.forkChild(Effect.exit(retryIdempotentRead(operation)));
    yield* Effect.yieldNow;
    yield* TestClock.adjust("5 seconds");
    const exit = yield* Fiber.join(fiber);

    assert.strictEqual(exit._tag, "Failure");
    assert.strictEqual(yield* Ref.get(attempts), 3);
  }),
);

it.effect("does not retry a generated validation problem", () =>
  Effect.gen(function* () {
    const attempts = yield* Ref.make(0);
    const problem = new ApiProblemError({
      operation: "load settings",
      problem: { code: "validationError", detail: "invalid filter" },
    });
    const operation = Ref.updateAndGet(attempts, (count) => count + 1).pipe(Effect.flatMap(() => Effect.fail(problem)));

    const exit = yield* Effect.exit(retryIdempotentRead(operation));

    assert.strictEqual(exit._tag, "Failure");
    assert.strictEqual(yield* Ref.get(attempts), 1);
  }),
);

it("classifies picker read failures", () => {
  const transport = (cause: unknown) => TransientTransportError.make({ operation: "load repositories", cause });
  for (const status of [502, 503, 504]) {
    assert.isTrue(isTransientPickerReadFailure(transport(new InvalidGeneratedErrorResponse("unavailable", status))));
  }
  assert.isFalse(isTransientPickerReadFailure(transport(new InvalidGeneratedErrorResponse("denied", 403))));
  for (const code of ["upstreamError", "serviceUnavailable", "hubUnavailable"] as const) {
    assert.isTrue(
      isTransientPickerReadFailure(new ApiProblemError({ operation: "load repositories", problem: { code } })),
    );
  }
  assert.isFalse(
    isTransientPickerReadFailure(
      new ApiProblemError({ operation: "load repositories", problem: { code: "forbidden" } }),
    ),
  );
  assert.isTrue(isTransientPickerReadFailure(transport(new TypeError("offline"))));
  assert.isFalse(isTransientPickerReadFailure(transport(new SyntaxError("invalid JSON"))));
});
