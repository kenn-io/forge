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

it.effect("classifies generated repository responses before retrying", () =>
  Effect.gen(function* () {
    type Case = [name: string, responses: Array<Response | Error>, succeeds: boolean];
    const malformed = (status: number) =>
      new Response("{", { status, headers: { "Content-Type": "application/json" } });
    const cases: Case[] = [
      ["text 502", [new Response("unavailable", { status: 502 }), Response.json([])], true],
      ["non-problem JSON 503", [Response.json({ message: "unavailable" }, { status: 503 }), Response.json([])], true],
      ["malformed JSON 504", [malformed(504), Response.json([])], true],
      ["text 403", [new Response("denied", { status: 403 })], false],
      ["malformed JSON 403", [malformed(403)], false],
      ["typed forbidden", [Response.json({ code: "forbidden", status: 403 }, { status: 403 })], false],
      ...(
        [
          ["upstreamError", 502],
          ["serviceUnavailable", 503],
          ["hubUnavailable", 503],
        ] as const
      ).map(([code, status]): Case => [
        `typed ${code}`,
        [Response.json({ code, status }, { status }), Response.json([])],
        true,
      ]),
      ["malformed successful JSON", [malformed(200)], false],
      ["transport recovery", [new TypeError("offline"), Response.json([])], true],
    ];
    for (const [name, responses, succeeds] of cases) {
      let attempts = 0;
      const fetch: typeof globalThis.fetch = async () => {
        const response = responses[attempts++]!;
        if (response instanceof Error) throw response;
        return response;
      };
      const read = executeGeneratedRequest("load repositories", (signal) =>
        listRepos({ baseURL: "http://forge.test", fetch, signal }),
      );
      const fiber = yield* Effect.forkChild(Effect.exit(retryIdempotentRead(read, isTransientPickerReadFailure)));
      yield* Effect.yieldNow;
      yield* TestClock.adjust("10 seconds");
      const exit = yield* Fiber.join(fiber);
      assert.strictEqual(attempts, responses.length, name);
      assert.strictEqual(exit._tag, succeeds ? "Success" : "Failure", name);
      if (exit._tag === "Success") assert.deepStrictEqual(exit.value, [], name);
    }
  }),
);
