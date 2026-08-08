import { assert, it } from "@effect/vitest";
import { Deferred, Effect, Layer } from "effect";
import { GeneratedApiLive } from "../api/generated-api.js";
import { RepoBrowserWorkflow, RepoBrowserWorkflowLive } from "./repo-browser-workflow.js";

const RepoBrowserWorkflowTest = Layer.provide(RepoBrowserWorkflowLive, GeneratedApiLive);

it.layer(RepoBrowserWorkflowTest)("repository-browser route ownership", (it) => {
  it.effect("does not let a stale route teardown interrupt its successor", () =>
    Effect.gen(function* () {
      const workflow = yield* RepoBrowserWorkflow;
      const staleOwner = "stale-route";
      const successorOwner = "successor-route";
      const interrupted = yield* Deferred.make<void>();
      yield* Effect.forkChild(
        workflow.path(
          successorOwner,
          Effect.never.pipe(Effect.onInterrupt(() => Deferred.succeed(interrupted, undefined))),
        ),
      );
      yield* Effect.yieldNow;

      yield* workflow.stop(staleOwner);

      assert.isFalse(yield* Deferred.isDone(interrupted));
      yield* workflow.stop(successorOwner);
    }),
  );
});
