import { Context, Effect, Layer } from "effect";
import type { ProblemBody } from "./problems.js";
import { createRuntimeClient } from "./runtime.js";
import { ApiProblemError, TransientTransportError } from "./effect-errors.js";

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
  request: () => Promise<GeneratedRequestResult<A>>,
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

export class GeneratedApi extends Context.Service<
  GeneratedApi,
  {
    readonly client: GeneratedClient;
    readonly execute: typeof executeGeneratedRequest;
  }
>()("kenn-forge/GeneratedApi") {}

export const GeneratedApiLive = Layer.succeed(GeneratedApi)({
  client: createRuntimeClient(),
  execute: executeGeneratedRequest,
});
