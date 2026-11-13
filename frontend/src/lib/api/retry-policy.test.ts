import { assert, it } from "@effect/vitest";
import { Effect, Fiber, Ref } from "effect";
import { TestClock } from "effect/testing";
import { ApiProblemError, TransientTransportError } from "./effect-errors.js";
import { executeGeneratedRequest } from "./generated-api.js";
import { listRepos } from "./generated/repositories/repositories.js";
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

const formats = [
  { name: "problem", response: (status: number) => Response.json({ code: "serviceUnavailable", status }, { status }) },
  { name: "text", response: (status: number) => new Response("unavailable", { status }) },
  { name: "non-problem JSON", response: (status: number) => Response.json({ message: "unavailable" }, { status }) },
  {
    name: "malformed JSON",
    response: (status: number) => new Response("{", { status, headers: { "Content-Type": "application/json" } }),
  },
];
const cases = [
  ...[502, 503, 504, 400, 401, 403, 422].flatMap((status) =>
    formats.map((format) => ({
      name: `${status} ${format.name}`,
      response: () => format.response(status),
      attempts: status >= 500 ? 2 : 1,
    })),
  ),
  {
    name: "missing body status",
    response: () => Response.json({ code: "internalError" }, { status: 504 }),
    attempts: 2,
  },
  {
    name: "conflicting body status",
    response: () => Response.json({ code: "internalError", status: 403 }, { status: 504 }),
    attempts: 2,
  },
  {
    name: "transport failure",
    response: () => {
      throw new TypeError("offline");
    },
    attempts: 2,
  },
  {
    name: "malformed success",
    response: () => new Response("{", { headers: { "Content-Type": "application/json" } }),
    attempts: 1,
  },
  {
    name: "exhausted gateway",
    response: () => new Response("unavailable", { status: 503 }),
    attempts: 3,
    exhausted: true,
  },
];

for (const scenario of cases) {
  it.effect(`classifies picker reads: ${scenario.name}`, () =>
    Effect.gen(function* () {
      let attempts = 0;
      const request = executeGeneratedRequest("load repositories", (signal) =>
        listRepos({
          signal,
          fetch: async () => {
            attempts++;
            return attempts > 1 && !scenario.exhausted ? Response.json([]) : scenario.response();
          },
        }),
      );
      const fiber = yield* Effect.forkChild(Effect.exit(retryIdempotentRead(request, isTransientPickerReadFailure)));
      yield* Effect.yieldNow;
      yield* TestClock.adjust("5 seconds");
      const exit = yield* Fiber.join(fiber);
      assert.strictEqual(attempts, scenario.attempts);
      assert.strictEqual(exit._tag, scenario.attempts === 2 ? "Success" : "Failure");
    }),
  );
}
