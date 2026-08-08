import { assert, it } from "@effect/vitest";
import { Deferred, Effect, Fiber, Layer, Ref } from "effect";
import { TestClock } from "effect/testing";
import { GeneratedApiLive } from "../api/generated-api.js";
import type { Issue, IssueDetail } from "../api/types.js";
import { IssuesWorkflow, IssuesWorkflowLive } from "./issues-workflow.js";

const IssuesWorkflowTest = Layer.provide(IssuesWorkflowLive, GeneratedApiLive);

function issue(id: number): Issue {
  return { ID: id } as Issue;
}

it.layer(IssuesWorkflowTest)("issue list reads", (it) => {
  it.effect("prevents an older query from replacing the latest list", () =>
    Effect.gen(function* () {
      const workflow = yield* IssuesWorkflow;
      const oldStarted = yield* Deferred.make<void>();
      const releaseOld = yield* Deferred.make<readonly Issue[]>();
      const projected = yield* Ref.make<readonly Issue[]>([]);

      const oldFiber = yield* Effect.forkChild(
        workflow
          .list(Deferred.succeed(oldStarted, undefined).pipe(Effect.andThen(Deferred.await(releaseOld))))
          .pipe(Effect.tap((result) => Ref.set(projected, result))),
      );
      yield* Deferred.await(oldStarted);
      const latestFiber = yield* Effect.forkChild(
        workflow.list(Effect.succeed([issue(2)])).pipe(Effect.tap((result) => Ref.set(projected, result))),
      );

      yield* Fiber.join(latestFiber);
      yield* Deferred.succeed(releaseOld, [issue(1)]);
      yield* Fiber.await(oldFiber);
      assert.deepStrictEqual(
        (yield* Ref.get(projected)).map((item) => item.ID),
        [2],
      );
    }),
  );

  it.effect("retries an event refresh superseded by a newer foreground query", () =>
    Effect.gen(function* () {
      const workflow = yield* IssuesWorkflow;
      const eventStarted = yield* Deferred.make<void>();
      const releaseEvent = yield* Deferred.make<readonly Issue[]>();
      const projected = yield* Ref.make<readonly Issue[]>([]);
      const eventFiber = yield* Effect.forkChild(
        workflow.reconcile(
          Deferred.succeed(eventStarted, undefined).pipe(Effect.andThen(Deferred.await(releaseEvent))),
          (result) => Ref.set(projected, result),
        ),
      );
      yield* Deferred.await(eventStarted);

      const latest = yield* workflow.list(Effect.succeed([issue(2)]));
      yield* Ref.set(projected, latest);
      yield* Deferred.succeed(releaseEvent, [issue(1)]);
      const failure = yield* Fiber.join(eventFiber).pipe(Effect.flip);

      assert.strictEqual(failure._tag, "TransientTransportError");
      assert.deepStrictEqual(
        (yield* Ref.get(projected)).map((item) => item.ID),
        [2],
      );
    }),
  );
});

it.layer(IssuesWorkflowTest)("issue detail reads", (it) => {
  it.effect("shares a detail read and promotes its single follow-up sync intent", () =>
    Effect.gen(function* () {
      const workflow = yield* IssuesWorkflow;
      const started = yield* Deferred.make<void>();
      const release = yield* Deferred.make<IssueDetail>();
      const reads = yield* Ref.make(0);
      const request = Ref.update(reads, (count) => count + 1).pipe(
        Effect.andThen(Deferred.succeed(started, undefined)),
        Effect.andThen(Deferred.await(release)),
      );
      const first = yield* Effect.forkChild(workflow.detail("issue", false, request));
      yield* Deferred.await(started);
      const promoted = yield* Effect.forkChild(workflow.detail("issue", "background", Effect.die("unused")));
      yield* Effect.yieldNow;

      yield* Deferred.succeed(release, { issue: { Number: 7 } } as IssueDetail);
      const results = [yield* Fiber.join(first), yield* Fiber.join(promoted)];

      assert.strictEqual(yield* Ref.get(reads), 1);
      assert.deepStrictEqual(
        results.map((result) => result.syncMode).filter((mode) => mode !== undefined),
        ["background"],
      );
    }),
  );
});

it.layer(IssuesWorkflowTest)("issue detail polling", (it) => {
  it.effect("waits for each refresh before scheduling the next one", () =>
    Effect.gen(function* () {
      const workflow = yield* IssuesWorkflow;
      const active = yield* Ref.make(0);
      const maximumActive = yield* Ref.make(0);
      const starts = yield* Ref.make(0);
      const refresh = Ref.updateAndGet(active, (count) => count + 1).pipe(
        Effect.tap((count) => Ref.update(maximumActive, (maximum) => Math.max(maximum, count))),
        Effect.tap(() => Ref.update(starts, (count) => count + 1)),
        Effect.andThen(Effect.sleep("2 seconds")),
        Effect.ensuring(Ref.update(active, (count) => count - 1)),
      );

      const polling = yield* Effect.forkChild(workflow.poll(1, refresh, "1 second"));
      yield* TestClock.adjust("10 seconds");

      assert.strictEqual(yield* Ref.get(maximumActive), 1);
      assert.isAtLeast(yield* Ref.get(starts), 2);
      yield* Fiber.interrupt(polling);
    }),
  );

  it.effect("does not let an older start replace the current poll", () =>
    Effect.gen(function* () {
      const workflow = yield* IssuesWorkflow;
      const currentStarts = yield* Ref.make(0);
      const staleStarts = yield* Ref.make(0);
      const current = yield* Effect.forkChild(
        workflow.poll(
          2,
          Ref.update(currentStarts, (count) => count + 1),
          "1 second",
        ),
      );
      yield* TestClock.adjust("2 seconds");

      yield* workflow.poll(
        1,
        Ref.update(staleStarts, (count) => count + 1),
        "1 second",
      );
      yield* TestClock.adjust("2 seconds");

      assert.strictEqual(yield* Ref.get(staleStarts), 0);
      assert.isAtLeast(yield* Ref.get(currentStarts), 1);
      yield* Fiber.interrupt(current);
    }),
  );
});
