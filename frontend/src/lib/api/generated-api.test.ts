import { assert, it } from "@effect/vitest";
import { Effect } from "effect";
import type { ProblemBody } from "./problems.js";
import { executeGeneratedRequest } from "./generated-api.js";

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
