import { assert, it } from "@effect/vitest";
import { Effect } from "effect";
import type { ProblemBody } from "./problems.js";
import { executeGeneratedRequest, executeOpaqueGeneratedApiRequest, makeGeneratedApiLayer } from "./generated-api.js";
import { createRuntimeClient } from "./runtime.js";

it.effect("turns a rejected generated request into a transient transport failure", () =>
  Effect.gen(function* () {
    const failure = yield* Effect.flip(
      executeGeneratedRequest<{ readonly id: string }>("load repositories", () =>
        Promise.reject(new Error("connection reset")),
      ),
    );

    assert.strictEqual(failure._tag, "TransientTransportError");
    assert.strictEqual(failure.operation, "load repositories");
  }),
);

it.effect("preserves the generated problem body from a failed request", () =>
  Effect.gen(function* () {
    const problem: ProblemBody = {
      code: "validationError",
      detail: "repository is required",
      details: { field: "repository" },
      status: 422,
      title: "Invalid request",
    };
    const failure = yield* Effect.flip(
      executeGeneratedRequest<{ readonly id: string }>("create workspace", () =>
        Promise.resolve({
          error: problem,
          response: new Response(null, { status: 422 }),
        }),
      ),
    );

    assert.strictEqual(failure._tag, "ApiProblemError");
    assert.strictEqual(failure.operation, "create workspace");
    assert.deepStrictEqual(failure.problem, problem);
  }),
);

it.effect("rejects an untyped generated error body at the API boundary", () =>
  Effect.gen(function* () {
    const failure = yield* Effect.flip(
      executeOpaqueGeneratedApiRequest("load fleet workspaces", () =>
        Promise.resolve({
          error: { message: "not a problem envelope" },
          response: new Response(null, { status: 502 }),
        }),
      ).pipe(Effect.provide(makeGeneratedApiLayer(createRuntimeClient()))),
    );

    assert.strictEqual(failure._tag, "InvalidExternalPayload");
    assert.strictEqual(failure.operation, "decode load fleet workspaces error response");
  }),
);
