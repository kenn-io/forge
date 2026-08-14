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

  it.effect("retries an acknowledged reconciliation superseded by a foreground load", () =>
    Effect.gen(function* () {
      const workflow = yield* ActivityWorkflow;
      const reconciliationStarted = yield* Deferred.make<void>();
      const releaseReconciliation = yield* Deferred.make<void>();
      const reconciliation = yield* Effect.forkChild(
        workflow.reconcileRead(
          "activity",
          Deferred.succeed(reconciliationStarted, undefined).pipe(
            Effect.andThen(Deferred.await(releaseReconciliation)),
            Effect.as("event"),
          ),
          () => Effect.void,
        ),
      );
      yield* Deferred.await(reconciliationStarted);
      yield* workflow.load("activity", Effect.succeed("foreground"), () => Effect.void);
      yield* Deferred.succeed(releaseReconciliation, undefined);

      const failure = yield* Fiber.join(reconciliation).pipe(Effect.flip);
      assert.strictEqual(failure._tag, "TransientTransportError");
    }),
  );

  it.effect("projects reconciliation started after an older foreground read", () =>
    Effect.gen(function* () {
      const workflow = yield* ActivityWorkflow;
      const foregroundStarted = yield* Deferred.make<void>();
      const releaseForeground = yield* Deferred.make<void>();
      const reconciliationStarted = yield* Deferred.make<void>();
      const releaseReconciliation = yield* Deferred.make<void>();
      const projected = yield* Ref.make<string[]>([]);
      const project = (value: string) => Ref.update(projected, (values) => [...values, value]);

      const foreground = yield* Effect.forkChild(
        workflow.load(
          "activity",
          Deferred.succeed(foregroundStarted, undefined).pipe(
            Effect.andThen(Deferred.await(releaseForeground)),
            Effect.as("foreground"),
          ),
          project,
        ),
      );
      yield* Deferred.await(foregroundStarted);

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

      yield* Deferred.succeed(releaseForeground, undefined);
      yield* Fiber.join(foreground);
      yield* Deferred.succeed(releaseReconciliation, undefined);
      yield* Fiber.join(reconciliation);

      assert.deepStrictEqual(yield* Ref.get(projected), ["foreground", "reconciliation"]);
    }),
  );

  it.effect("projects foreground started after an older reconciliation", () =>
    Effect.gen(function* () {
      const workflow = yield* ActivityWorkflow;
      const reconciliationStarted = yield* Deferred.make<void>();
      const releaseReconciliation = yield* Deferred.make<void>();
      const foregroundStarted = yield* Deferred.make<void>();
      const releaseForeground = yield* Deferred.make<void>();
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

      const foreground = yield* Effect.forkChild(
        workflow.load(
          "activity",
          Deferred.succeed(foregroundStarted, undefined).pipe(
            Effect.andThen(Deferred.await(releaseForeground)),
            Effect.as("foreground"),
          ),
          project,
        ),
      );
      yield* Deferred.await(foregroundStarted);

      yield* Deferred.succeed(releaseReconciliation, undefined);
      yield* Fiber.join(reconciliation);
      yield* Deferred.succeed(releaseForeground, undefined);
      yield* Fiber.join(foreground);

      assert.deepStrictEqual(yield* Ref.get(projected), ["reconciliation", "foreground"]);
    }),
  );

  it.effect("projects poll started after an older reconciliation", () =>
    Effect.gen(function* () {
      const workflow = yield* ActivityWorkflow;
      const reconciliationStarted = yield* Deferred.make<void>();
      const releaseReconciliation = yield* Deferred.make<void>();
      const pollStarted = yield* Deferred.make<void>();
      const releasePoll = yield* Deferred.make<void>();
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

      const poll = yield* Effect.forkChild(
        workflow.pollRead(
          "activity",
          Deferred.succeed(pollStarted, undefined).pipe(Effect.andThen(Deferred.await(releasePoll)), Effect.as("poll")),
          project,
        ),
      );
      yield* Deferred.await(pollStarted);

      yield* Deferred.succeed(releaseReconciliation, undefined);
      yield* Fiber.join(reconciliation);
      yield* Deferred.succeed(releasePoll, undefined);
      yield* Fiber.join(poll);

      assert.deepStrictEqual(yield* Ref.get(projected), ["reconciliation", "poll"]);
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

  it.effect("rejects an older reconciliation after a newer replacement poll completes", () =>
    Effect.gen(function* () {
      const workflow = yield* ActivityWorkflow;
      const reconciliationStarted = yield* Deferred.make<void>();
      const releaseReconciliation = yield* Deferred.make<void>();
      const projected = yield* Ref.make<string[]>([]);
      const project = (value: string) => Ref.update(projected, (values) => [...values, value]);

      const reconciliation = yield* Effect.forkChild(
        Effect.exit(
          workflow.reconcileRead(
            "activity",
            Deferred.succeed(reconciliationStarted, undefined).pipe(
              Effect.andThen(Deferred.await(releaseReconciliation)),
              Effect.as("reconciliation"),
            ),
            project,
          ),
        ),
      );
      yield* Deferred.await(reconciliationStarted);

      yield* workflow.pollSnapshotRead("activity", Effect.succeed("replacement"), project);
      yield* Deferred.succeed(releaseReconciliation, undefined);

      const reconciliationExit = yield* Fiber.join(reconciliation);
      assert.isTrue(Exit.isFailure(reconciliationExit));
      assert.deepStrictEqual(yield* Ref.get(projected), ["replacement"]);
    }),
  );

  it.effect("projects reconciliation when a newer same-scope foreground read fails", () =>
    Effect.gen(function* () {
      const workflow = yield* ActivityWorkflow;
      const reconciliationStarted = yield* Deferred.make<void>();
      const releaseReconciliation = yield* Deferred.make<void>();
      const projected = yield* Ref.make<string[]>([]);
      const project = (value: string) => Ref.update(projected, (values) => [...values, value]);

      const reconciliation = yield* Effect.forkChild(
        Effect.exit(
          workflow.reconcileRead(
            "activity",
            Deferred.succeed(reconciliationStarted, undefined).pipe(
              Effect.andThen(Deferred.await(releaseReconciliation)),
              Effect.as("reconciliation"),
            ),
            project,
          ),
        ),
      );
      yield* Deferred.await(reconciliationStarted);

      const foregroundExit = yield* Effect.exit(workflow.load("activity", Effect.fail("foreground failed"), project));
      assert.isTrue(Exit.isFailure(foregroundExit));
      yield* Deferred.succeed(releaseReconciliation, undefined);

      const reconciliationExit = yield* Fiber.join(reconciliation);
      assert.isTrue(Exit.isSuccess(reconciliationExit));
      assert.deepStrictEqual(yield* Ref.get(projected), ["reconciliation"]);
    }),
  );

  it.effect("rejects reconciliation after a failed foreground read changes scope", () =>
    Effect.gen(function* () {
      const workflow = yield* ActivityWorkflow;
      const reconciliationStarted = yield* Deferred.make<void>();
      const releaseReconciliation = yield* Deferred.make<void>();
      const projected = yield* Ref.make<string[]>([]);
      const project = (value: string) => Ref.update(projected, (values) => [...values, value]);

      const reconciliation = yield* Effect.forkChild(
        Effect.exit(
          workflow.reconcileRead(
            "old-scope",
            Deferred.succeed(reconciliationStarted, undefined).pipe(
              Effect.andThen(Deferred.await(releaseReconciliation)),
              Effect.as("reconciliation"),
            ),
            project,
          ),
        ),
      );
      yield* Deferred.await(reconciliationStarted);

      const foregroundExit = yield* Effect.exit(workflow.load("new-scope", Effect.fail("foreground failed"), project));
      assert.isTrue(Exit.isFailure(foregroundExit));
      yield* Deferred.succeed(releaseReconciliation, undefined);

      const reconciliationExit = yield* Fiber.join(reconciliation);
      assert.isTrue(Exit.isFailure(reconciliationExit));
      assert.deepStrictEqual(yield* Ref.get(projected), []);
    }),
  );

  it.effect("prevents an older reconciliation from replacing the latest snapshot", () =>
    Effect.gen(function* () {
      const workflow = yield* ActivityWorkflow;
      const olderStarted = yield* Deferred.make<void>();
      const releaseOlder = yield* Deferred.make<void>();
      const projected = yield* Ref.make<string[]>([]);
      const project = (value: string) => Ref.update(projected, (values) => [...values, value]);

      const older = yield* Effect.forkChild(
        Effect.exit(
          workflow.reconcileRead(
            "activity",
            Deferred.succeed(olderStarted, undefined).pipe(
              Effect.andThen(Deferred.await(releaseOlder)),
              Effect.as("older"),
            ),
            project,
          ),
        ),
      );
      yield* Deferred.await(olderStarted);
      yield* workflow.reconcileRead("activity", Effect.succeed("latest"), project);
      yield* Deferred.succeed(releaseOlder, undefined);

      const olderExit = yield* Fiber.join(older);
      assert.isTrue(Exit.isFailure(olderExit));
      assert.deepStrictEqual(yield* Ref.get(projected), ["latest"]);
    }),
  );

  it.effect("projects an older successful reconciliation when a newer read fails", () =>
    Effect.gen(function* () {
      const workflow = yield* ActivityWorkflow;
      const olderStarted = yield* Deferred.make<void>();
      const releaseOlder = yield* Deferred.make<void>();
      const projected = yield* Ref.make<string[]>([]);
      const project = (value: string) => Ref.update(projected, (values) => [...values, value]);

      const older = yield* Effect.forkChild(
        Effect.exit(
          workflow.reconcileRead(
            "activity",
            Deferred.succeed(olderStarted, undefined).pipe(
              Effect.andThen(Deferred.await(releaseOlder)),
              Effect.as("older"),
            ),
            project,
          ),
        ),
      );
      yield* Deferred.await(olderStarted);

      const newerExit = yield* Effect.exit(workflow.reconcileRead("activity", Effect.fail("newer failed"), project));
      assert.isTrue(Exit.isFailure(newerExit));
      yield* Deferred.succeed(releaseOlder, undefined);

      const olderExit = yield* Fiber.join(older);
      assert.isTrue(Exit.isSuccess(olderExit));
      assert.deepStrictEqual(yield* Ref.get(projected), ["older"]);
    }),
  );

  it.effect("settles loading without projecting an old scope after reconciliation fails", () =>
    Effect.gen(function* () {
      const workflow = yield* ActivityWorkflow;
      const foregroundStarted = yield* Deferred.make<void>();
      const releaseForeground = yield* Deferred.make<void>();
      const loading = yield* Ref.make(true);
      const projected = yield* Ref.make<string[]>([]);
      const clearLoading = Ref.set(loading, false);
      const project = (value: string) =>
        Ref.update(projected, (values) => [...values, value]).pipe(Effect.andThen(clearLoading));

      const foreground = yield* Effect.forkChild(
        workflow.load(
          "old-scope",
          Deferred.succeed(foregroundStarted, undefined).pipe(
            Effect.andThen(Deferred.await(releaseForeground)),
            Effect.as("foreground"),
          ),
          project,
          clearLoading,
        ),
      );
      yield* Deferred.await(foregroundStarted);

      const reconciliation = yield* Effect.exit(
        workflow.reconcileRead("new-scope", Effect.fail("reconciliation failed"), project),
      );
      assert.isTrue(Exit.isFailure(reconciliation));
      yield* Deferred.succeed(releaseForeground, undefined);
      yield* Fiber.join(foreground);

      assert.isFalse(yield* Ref.get(loading));
      assert.deepStrictEqual(yield* Ref.get(projected), []);
    }),
  );

  it.effect("suppresses a foreground failure after successful reconciliation replaces it", () =>
    Effect.gen(function* () {
      const workflow = yield* ActivityWorkflow;
      const foregroundStarted = yield* Deferred.make<void>();
      const releaseForeground = yield* Deferred.make<void>();
      const failurePublished = yield* Ref.make(false);
      const projected = yield* Ref.make<string[]>([]);
      const project = (value: string) => Ref.update(projected, (values) => [...values, value]);

      const foreground = yield* Effect.forkChild(
        Effect.exit(
          workflow.load(
            "activity",
            Deferred.succeed(foregroundStarted, undefined).pipe(
              Effect.andThen(Deferred.await(releaseForeground)),
              Effect.andThen(Effect.fail("stale foreground failure")),
            ),
            project,
            Ref.set(failurePublished, true),
          ),
        ),
      );
      yield* Deferred.await(foregroundStarted);

      yield* workflow.reconcileRead("activity", Effect.succeed("reconciliation"), project);
      yield* Deferred.succeed(releaseForeground, undefined);

      const foregroundExit = yield* Fiber.join(foreground);
      assert.isTrue(Exit.isSuccess(foregroundExit));
      assert.isFalse(yield* Ref.get(failurePublished));
      assert.deepStrictEqual(yield* Ref.get(projected), ["reconciliation"]);
    }),
  );
});
