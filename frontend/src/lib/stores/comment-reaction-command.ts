import { Effect } from "effect";
import type { AppRuntime, AppServices } from "../app/runtime.js";
import { showFlash } from "./flash.svelte.js";
import { invokeMutationCallback, invokeMutationFailure, providerMutationFailureMessage } from "./ordered-mutations.js";
import type { MutationCallbacks, ProviderMutationFailure } from "./ordered-mutations.js";

export function runCommentReaction(
  runtime: AppRuntime,
  action: Effect.Effect<void, ProviderMutationFailure, AppServices>,
  operation: string,
  safeContext: { owner: string; name: string; number: number; commentID: number },
  callbacks: MutationCallbacks,
): void {
  let settled = false;
  const program = Effect.gen(function* () {
    yield* action;
    yield* Effect.sync(() => {
      settled = true;
      showFlash("Reaction saved.", { tone: "success" });
    });
    yield* invokeMutationCallback(callbacks.onSuccess);
    yield* invokeMutationCallback(callbacks.onSettled);
  }).pipe(
    Effect.ensuring(
      Effect.gen(function* () {
        if (!settled) yield* invokeMutationCallback(callbacks.onSettled);
      }),
    ),
  );
  runtime.runCommand(program, {
    operation,
    safeContext,
    onFailure: (failure) => {
      const message = providerMutationFailureMessage(failure, "failed to add reaction");
      showFlash(message, { tone: "danger" });
      invokeMutationFailure(callbacks.onFailure, message);
    },
  });
}
