import { Context, Effect, Layer, Option } from "effect";
import { withRequestParent } from "../instrumentation/traceContext.js";
import * as client from "./generated/index.js";
import { GeneratedProblemResponse, InvalidGeneratedErrorResponse } from "./runtime.js";
import { ApiProblemError, InvalidExternalPayload, TransientTransportError } from "./effect-errors.js";

export type GeneratedClient = typeof client;
// The span making a request, so the server's span for it joins this trace.
const requestParentSpan = Effect.map(Effect.option(Effect.currentSpan), Option.getOrNull);

export const executeGeneratedRequest = Effect.fn("GeneratedApi.execute")(function* <A>(
  operation: string,
  request: (signal: AbortSignal) => Promise<A>,
) {
  yield* Effect.annotateCurrentSpan("api.operation", operation);
  const parent = yield* requestParentSpan;
  return yield* Effect.tryPromise({
    try: (signal) => withRequestParent(parent, () => request(signal)),
    catch: (cause) =>
      cause instanceof GeneratedProblemResponse
        ? new ApiProblemError({ operation, problem: cause.problem })
        : TransientTransportError.make({ operation, cause }),
  });
});

export const executeOpaqueGeneratedApiRequest = Effect.fn("GeneratedApi.executeOpaque")(function* <A>(
  operation: string,
  request: (client: GeneratedClient, signal: AbortSignal) => Promise<A>,
) {
  const api = yield* GeneratedApi;
  yield* Effect.annotateCurrentSpan("api.operation", operation);
  const parent = yield* requestParentSpan;
  return yield* Effect.tryPromise({
    try: (signal) => withRequestParent(parent, () => request(api.client, signal)),
    catch: (cause) =>
      cause instanceof GeneratedProblemResponse
        ? new ApiProblemError({ operation, problem: cause.problem })
        : cause instanceof InvalidGeneratedErrorResponse
          ? InvalidExternalPayload.make({ operation: `decode ${operation} error response`, cause: cause.body })
          : TransientTransportError.make({ operation, cause }),
  });
});

export class GeneratedApi extends Context.Service<
  GeneratedApi,
  {
    readonly client: GeneratedClient;
    readonly execute: typeof executeGeneratedRequest;
  }
>()("kenn-forge/GeneratedApi") {}

export const executeGeneratedApiRequest = Effect.fn("GeneratedApi.executeWithClient")(function* <A>(
  operation: string,
  request: (client: GeneratedClient, signal: AbortSignal) => Promise<A>,
) {
  const api = yield* GeneratedApi;
  return yield* api.execute(operation, (signal) => request(api.client, signal));
});

export const makeGeneratedApiLayer = (client: GeneratedClient) =>
  Layer.succeed(GeneratedApi)({
    client,
    execute: executeGeneratedRequest,
  });

export const GeneratedApiLive = makeGeneratedApiLayer(client);
