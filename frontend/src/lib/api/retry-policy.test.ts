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
    const cases: Array<{ name: string; responses: Array<Response | Error>; attempts: number; succeeds: boolean }> = [
      ...[502, 503, 504].flatMap((status) => [
        {
          name: `text ${status}`,
          responses: [new Response("unavailable", { status }), Response.json([])],
          attempts: 2,
          succeeds: true,
        },
        {
          name: `non-problem JSON ${status}`,
          responses: [Response.json({ message: "unavailable" }, { status }), Response.json([])],
          attempts: 2,
          succeeds: true,
        },
        {
          name: `malformed JSON ${status}`,
          responses: [
            new Response("{", { status, headers: { "Content-Type": "application/json" } }),
            Response.json([]),
          ],
          attempts: 2,
          succeeds: true,
        },
      ]),
      ...[401, 403].flatMap((status) => [
        { name: `text ${status}`, responses: [new Response("denied", { status })], attempts: 1, succeeds: false },
        {
          name: `malformed JSON ${status}`,
          responses: [new Response("{", { status, headers: { "Content-Type": "application/json" } })],
          attempts: 1,
          succeeds: false,
        },
      ]),
      {
        name: "typed forbidden",
        responses: [Response.json({ code: "forbidden", status: 403 }, { status: 403 })],
        attempts: 1,
        succeeds: false,
      },
      {
        name: "typed validation",
        responses: [Response.json({ code: "validationError", status: 400 }, { status: 400 })],
        attempts: 1,
        succeeds: false,
      },
      ...(
        [
          ["upstreamError", 502],
          ["serviceUnavailable", 503],
          ["hubUnavailable", 503],
        ] as const
      ).map(([code, status]) => ({
        name: `typed ${code}`,
        responses: [Response.json({ code, status }, { status }), Response.json([])],
        attempts: 2,
        succeeds: true,
      })),
      {
        name: "malformed successful JSON",
        responses: [new Response("{", { status: 200, headers: { "Content-Type": "application/json" } })],
        attempts: 1,
        succeeds: false,
      },
      {
        name: "transport recovery",
        responses: [new TypeError("offline"), Response.json([])],
        attempts: 2,
        succeeds: true,
      },
      {
        name: "transport exhaustion",
        responses: [new TypeError("offline"), new TypeError("offline"), new TypeError("offline")],
        attempts: 3,
        succeeds: false,
      },
      {
        name: "gateway exhaustion",
        responses: [
          new Response("unavailable", { status: 503 }),
          new Response("unavailable", { status: 503 }),
          new Response("unavailable", { status: 503 }),
        ],
        attempts: 3,
        succeeds: false,
      },
      {
        name: "failed body read",
        responses: [
          new Response(
            new ReadableStream({
              start(controller) {
                controller.error(new Error("interrupted"));
              },
            }),
            { status: 502 },
          ),
          Response.json([]),
        ],
        attempts: 2,
        succeeds: true,
      },
    ];
    for (const fixture of cases) {
      let attempts = 0;
      const fetch: typeof globalThis.fetch = async () => {
        const response = fixture.responses[attempts++]!;
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
      assert.strictEqual(attempts, fixture.attempts, fixture.name);
      assert.strictEqual(exit._tag, fixture.succeeds ? "Success" : "Failure", fixture.name);
      if (exit._tag === "Success") assert.deepStrictEqual(exit.value, [], fixture.name);
    }
  }),
);
