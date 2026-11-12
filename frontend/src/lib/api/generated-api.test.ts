import { assert, it } from "@effect/vitest";
import { Effect } from "effect";
import type { ProblemBody } from "./problems.js";
import { executeGeneratedRequest, executeOpaqueGeneratedApiRequest, makeGeneratedApiLayer } from "./generated-api.js";
import { GeneratedProblemResponse, InvalidGeneratedErrorResponse, orvalRequest } from "./runtime.js";
import { makeGeneratedClient } from "../testing/generated-client.js";

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

for (const problem of [
  {
    code: "validationError",
    detail: "repository is required",
    details: { field: "repository" },
    status: 422,
    title: "Invalid request",
  },
  { code: "internalError" },
  { code: "internalError", status: 403 },
] satisfies ProblemBody[]) {
  it.effect(`preserves the problem body and HTTP status ${JSON.stringify(problem)}`, () =>
    Effect.gen(function* () {
      const failure = yield* Effect.flip(
        executeGeneratedRequest("load repositories", () =>
          Promise.reject(new GeneratedProblemResponse(problem, new Response(null, { status: 504 }))),
        ),
      );
      assert.strictEqual(failure._tag, "ApiProblemError");
      if (failure._tag === "ApiProblemError") {
        assert.strictEqual(failure.operation, "load repositories");
        assert.strictEqual(failure.httpStatus, 504);
        assert.deepStrictEqual(failure.problem, problem);
      }
    }),
  );
}

it.effect("rejects an untyped generated error body at the API boundary", () =>
  Effect.gen(function* () {
    const failure = yield* Effect.flip(
      executeOpaqueGeneratedApiRequest("load fleet workspaces", () =>
        Promise.reject(new InvalidGeneratedErrorResponse({ message: "not a problem envelope" }, 502)),
      ).pipe(Effect.provide(makeGeneratedApiLayer(makeGeneratedClient()))),
    );

    assert.strictEqual(failure._tag, "InvalidExternalPayload");
    assert.strictEqual(failure.operation, "decode load fleet workspaces error response");
  }),
);

it.effect("names the Effect span making a request as the server span's parent", () =>
  Effect.gen(function* () {
    let traceparent: string | null = null;
    const capture = (input: RequestInfo | URL, init?: RequestInit) => {
      traceparent = new Request(input, init).headers.get("traceparent");
      return Promise.resolve(new Response(null, { status: 204 }));
    };
    const outer = yield* Effect.currentSpan;

    yield* executeGeneratedRequest("load sync status", (signal) =>
      orvalRequest("/sync/status", { baseURL: "http://forge.test/api/v1", fetch: capture, signal }),
    );

    const [, traceId, parentId] = traceparent!.split("-");
    assert.strictEqual(traceId, outer.traceId);
    // The generated request's own span sits between the caller and the server.
    assert.notStrictEqual(parentId, outer.spanId);
  }).pipe(Effect.withSpan("load sync status")),
);
