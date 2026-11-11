import { assert, it } from "@effect/vitest";
import { Deferred, Effect, Exit, Fiber, Ref } from "effect";
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
          "activity",
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
      yield* workflow.load("activity", Effect.succeed({ items: [], capped: false }), () => Effect.void);
      yield* Deferred.succeed(releasePoll, undefined);
      yield* TestClock.adjust("2 seconds");

      assert.isAtLeast(yield* Ref.get(pollStarts), 2);
      yield* Fiber.interrupt(polling);
    }),
  );

  it.effect("projects reconciliation after a newer incremental poll completes", () =>
    Effect.gen(function* () {
      const workflow = yield* ActivityWorkflow;
      const reconciliationStarted = yield* Deferred.make<void>();
      const releaseReconciliation = yield* Deferred.make<void>();
      const pollStarted = yield* Deferred.make<void>();
      const projected = yield* Ref.make<string[]>([]);
      const project = (value: string) => Ref.update(projected, (values) => [...values, value]);

      const reconciliation = yield* Effect.forkChild(
        workflow.reconcileRead(
          "activity",
          Deferred.succeed(reconciliationStarted, undefined).pipe(
            Effect.andThen(Deferred.await(releaseReconciliation)),
            Effect.as("reconciliation"),
          ),
          project,
        ),
      );
      yield* Deferred.await(reconciliationStarted);

      yield* workflow.pollRead("activity", Deferred.succeed(pollStarted, undefined).pipe(Effect.as("poll")), project);
      yield* Deferred.await(pollStarted);
      yield* Deferred.succeed(releaseReconciliation, undefined);
      yield* Fiber.join(reconciliation);

      assert.deepStrictEqual(yield* Ref.get(projected), ["poll", "reconciliation"]);
    }),
  );

  it.effect("runs a queued page after the full snapshot publishes", () =>
    Effect.gen(function* () {
      const workflow = yield* ActivityWorkflow;
      const started = yield* Deferred.make<void>();
      const release = yield* Deferred.make<void>();
      const results = yield* Ref.make<string[]>([]);
      const load = yield* Effect.forkChild(
        workflow.load(
          "activity",
          Deferred.succeed(started, undefined).pipe(Effect.andThen(Deferred.await(release)), Effect.as("snapshot")),
          (value) => Ref.update(results, (rows) => [...rows, value]),
        ),
      );
      yield* Deferred.await(started);
      const page = yield* Effect.forkChild(workflow.page(Ref.update(results, (rows) => [...rows, "page"])));
      yield* Deferred.succeed(release, undefined);
      yield* Fiber.join(load);
      yield* Fiber.join(page);
      assert.deepStrictEqual(yield* Ref.get(results), ["snapshot", "page"]);
    }),
  );

  it.effect("interrupts active and queued background snapshots for a foreground load", () =>
    Effect.gen(function* () {
      const workflow = yield* ActivityWorkflow;
      const started = yield* Deferred.make<void>();
      const aborted = yield* Ref.make(false);
      const queuedStarted = yield* Ref.make(false);
      const active = yield* Effect.forkChild(
        workflow.reconcileRead(
          "old",
          Deferred.succeed(started, undefined).pipe(
            Effect.andThen(Effect.never),
            Effect.onInterrupt(() => Ref.set(aborted, true)),
          ),
          () => Effect.void,
        ),
      );
      yield* Deferred.await(started);
      const queued = yield* Effect.forkChild(
        workflow.pollSnapshotRead("old", Ref.set(queuedStarted, true), () => Effect.void),
      );
      yield* Effect.yieldNow;
      yield* workflow.load("new", Effect.void, () => Effect.void);
      yield* Fiber.join(active);
      yield* Fiber.join(queued);
      assert.isTrue(yield* Ref.get(aborted));
      assert.isFalse(yield* Ref.get(queuedStarted));
    }),
  );

  it.effect("interrupts an owned background read when its caller stops", () =>
    Effect.gen(function* () {
      const workflow = yield* ActivityWorkflow;
      const started = yield* Deferred.make<void>();
      const aborted = yield* Ref.make(false);
      const read = yield* Effect.forkChild(
        workflow.reconcileRead(
          "activity",
          Deferred.succeed(started, undefined).pipe(
            Effect.andThen(Effect.never),
            Effect.onInterrupt(() => Ref.set(aborted, true)),
          ),
          () => Effect.void,
        ),
      );
      yield* Deferred.await(started);
      yield* Fiber.interrupt(read);
      assert.isTrue(yield* Ref.get(aborted));
      yield* workflow.page(Effect.void);
    }),
  );

  it.effect("releases the permit after a page failure", () =>
    Effect.gen(function* () {
      const workflow = yield* ActivityWorkflow;
      const failed = yield* Effect.exit(workflow.page(Effect.fail("page failed")));
      assert.isTrue(Exit.isFailure(failed));
      const projected = yield* Ref.make(false);
      yield* workflow.load("activity", Effect.void, () => Ref.set(projected, true));
      assert.isTrue(yield* Ref.get(projected));
    }),
  );

  it.effect("replaces an in-flight foreground load", () =>
    Effect.gen(function* () {
      const workflow = yield* ActivityWorkflow;
      const started = yield* Deferred.make<void>();
      const aborted = yield* Ref.make(false);
      const projected = yield* Ref.make<string[]>([]);
      const first = yield* Effect.forkDetach(
        Effect.exit(
          workflow.load(
            "old",
            Deferred.succeed(started, undefined).pipe(
              Effect.andThen(Effect.never),
              Effect.onInterrupt(() => Ref.set(aborted, true)),
            ),
            (value: string) => Ref.update(projected, (items) => [...items, value]),
          ),
        ),
      );
      yield* Deferred.await(started);
      yield* workflow.load("new", Effect.succeed("new"), (value) =>
        Ref.update(projected, (items) => [...items, value]),
      );
      yield* Fiber.await(first);
      assert.isTrue(yield* Ref.get(aborted));
      assert.deepStrictEqual(yield* Ref.get(projected), ["new"]);
    }),
  );

  it.effect("replaces a foreground load while it interrupts an older snapshot", () =>
    Effect.gen(function* () {
      const workflow = yield* ActivityWorkflow;
      const started = yield* Deferred.make<void>();
      const interrupted = yield* Deferred.make<void>();
      const release = yield* Deferred.make<void>();
      const projected = yield* Ref.make<string[]>([]);
      const background = yield* Effect.forkChild(
        workflow.reconcileRead(
          "old",
          Deferred.succeed(started, undefined).pipe(
            Effect.andThen(Effect.never),
            Effect.onInterrupt(() =>
              Deferred.succeed(interrupted, undefined).pipe(Effect.andThen(Deferred.await(release))),
            ),
          ),
          () => Effect.void,
        ),
      );
      yield* Deferred.await(started);
      const first = yield* Effect.forkDetach(
        workflow.load("first", Effect.succeed("first"), (value) => Ref.update(projected, (rows) => [...rows, value])),
      );
      yield* Deferred.await(interrupted);
      const second = yield* Effect.forkDetach(
        workflow.load("second", Effect.succeed("second"), (value) => Ref.update(projected, (rows) => [...rows, value])),
      );
      yield* Effect.yieldNow;
      yield* Deferred.succeed(release, undefined);
      yield* Fiber.await(first);
      yield* Fiber.join(second);
      yield* Fiber.join(background);
      assert.deepStrictEqual(yield* Ref.get(projected), ["second"]);
    }),
  );
});
