import { assert, it } from "@effect/vitest";
import { Deferred, Effect, Fiber, Ref } from "effect";
import { TestClock } from "effect/testing";
import { ActivityWorkflow, ActivityWorkflowLive } from "./activity-workflow.js";

it.layer(ActivityWorkflowLive)("activity polling", (it) => {
  it.effect("waits for each poll before scheduling the next one", () =>
    Effect.gen(function* () {
      const workflow = yield* ActivityWorkflow;
      const active = yield* Ref.make(0);
      const maximumActive = yield* Ref.make(0);
      const starts = yield* Ref.make(0);
      const pollOnce = Ref.updateAndGet(active, (count) => count + 1).pipe(
        Effect.tap((count) => Ref.update(maximumActive, (maximum) => Math.max(maximum, count))),
        Effect.tap(() => Ref.update(starts, (count) => count + 1)),
        Effect.andThen(Effect.sleep("2 seconds")),
        Effect.ensuring(Ref.update(active, (count) => count - 1)),
      );

      const fiber = yield* Effect.forkChild(workflow.poll(pollOnce, "1 second"));
      yield* TestClock.adjust("10 seconds");

      assert.strictEqual(yield* Ref.get(maximumActive), 1);
      assert.isAtLeast(yield* Ref.get(starts), 2);
      yield* Fiber.interrupt(fiber);
    }),
  );

  it.effect("keeps polling when a foreground load replaces an overlapping poll read", () =>
    Effect.gen(function* () {
      const workflow = yield* ActivityWorkflow;
      const pollStarted = yield* Deferred.make<void>();
      const releasePoll = yield* Deferred.make<void>();
      const pollStarts = yield* Ref.make(0);
      const pollOnce = workflow
        .pollRead(
          Ref.update(pollStarts, (count) => count + 1).pipe(
            Effect.andThen(Deferred.succeed(pollStarted, undefined)),
            Effect.andThen(Deferred.await(releasePoll)),
            Effect.as({ items: [], capped: false }),
          ),
          () => Effect.void,
        )
        .pipe(Effect.asVoid);

      const polling = yield* Effect.forkChild(workflow.poll(pollOnce, "1 second"));
      yield* Deferred.await(pollStarted);
      yield* workflow.load(Effect.succeed({ items: [], capped: false }), () => Effect.void);
      yield* Deferred.succeed(releasePoll, undefined);
      yield* TestClock.adjust("2 seconds");

      assert.isAtLeast(yield* Ref.get(pollStarts), 2);
      yield* Fiber.interrupt(polling);
    }),
  );

  it.effect("retries an acknowledged reconciliation superseded by a foreground load", () =>
    Effect.gen(function* () {
      const workflow = yield* ActivityWorkflow;
      const reconciliationStarted = yield* Deferred.make<void>();
      const releaseReconciliation = yield* Deferred.make<void>();
      const reconciliation = yield* Effect.forkChild(
        workflow.reconcileRead(
          Deferred.succeed(reconciliationStarted, undefined).pipe(
            Effect.andThen(Deferred.await(releaseReconciliation)),
            Effect.as("event"),
          ),
          () => Effect.void,
        ),
      );
      yield* Deferred.await(reconciliationStarted);
      yield* workflow.load(Effect.succeed("foreground"), () => Effect.void);
      yield* Deferred.succeed(releaseReconciliation, undefined);

      const failure = yield* Fiber.join(reconciliation).pipe(Effect.flip);
      assert.strictEqual(failure._tag, "TransientTransportError");
    }),
  );
});
