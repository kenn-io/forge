import { Context, Effect, Layer } from "effect";
import { isProblem, type ProblemBody } from "./problems.js";
import { createRuntimeClient } from "./runtime.js";
import { ApiProblemError, InvalidExternalPayload, TransientTransportError } from "./effect-errors.js";

export type GeneratedClient = ReturnType<typeof createRuntimeClient>;

export type GeneratedRequestResult<A> =
  | {
      readonly data: A;
      readonly error?: never;
      readonly response: Response;
    }
  | {
      readonly data?: never;
      readonly error: ProblemBody;
      readonly response: Response;
    };

export const executeGeneratedRequest = Effect.fn("GeneratedApi.execute")(function* <A>(
  operation: string,
  request: (signal: AbortSignal) => Promise<GeneratedRequestResult<A>>,
) {
  const result = yield* Effect.tryPromise({
    try: request,
    catch: (cause) => TransientTransportError.make({ operation, cause }),
  });
  if ("data" in result) {
    return result.data;
  }
  return yield* Effect.fail(new ApiProblemError({ operation, problem: result.error }));
});

type OpaqueGeneratedRequestResult<A> =
  | { readonly data: A; readonly error?: never; readonly response: Response }
  | { readonly data?: never; readonly error: unknown; readonly response: Response };

export const executeOpaqueGeneratedApiRequest = Effect.fn("GeneratedApi.executeOpaque")(function* <A>(
  operation: string,
  request: (client: GeneratedClient, signal: AbortSignal) => Promise<OpaqueGeneratedRequestResult<A>>,
) {
  const api = yield* GeneratedApi;
  const result = yield* Effect.tryPromise({
    try: (signal) => request(api.client, signal),
    catch: (cause) => TransientTransportError.make({ operation, cause }),
  });
  if ("data" in result) return result.data;
  if (isProblem(result.error)) {
    return yield* Effect.fail(new ApiProblemError({ operation, problem: result.error }));
  }
  return yield* Effect.fail(
    InvalidExternalPayload.make({
      operation: `decode ${operation} error response`,
      cause: result.error,
    }),
  );
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
  request: (client: GeneratedClient, signal: AbortSignal) => Promise<GeneratedRequestResult<A>>,
) {
  const api = yield* GeneratedApi;
  return yield* api.execute(operation, (signal) => request(api.client, signal));
});

export const makeGeneratedApiLayer = (client: GeneratedClient) =>
  Layer.succeed(GeneratedApi)({
    client,
    execute: executeGeneratedRequest,
  });

export const GeneratedApiLive = makeGeneratedApiLayer(createRuntimeClient());
