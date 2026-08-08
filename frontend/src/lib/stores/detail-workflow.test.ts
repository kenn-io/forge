import { assert, it } from "@effect/vitest";
import { Deferred, Effect, Fiber, Layer, Ref } from "effect";
import { GeneratedApiLive } from "../api/generated-api.js";
import type { PullDetail } from "../api/types.js";
import { DetailWorkflow, DetailWorkflowLive } from "./detail-workflow.js";

const DetailWorkflowTest = Layer.provide(DetailWorkflowLive, GeneratedApiLive);

function detail(head: string): PullDetail {
  return { platform_head_sha: head } as PullDetail;
}

it.layer(DetailWorkflowTest)("selected detail reads", (it) => {
  it.effect("does not commit an older selection after the latest detail", () =>
    Effect.gen(function* () {
      const workflow = yield* DetailWorkflow;
      const oldStarted = yield* Deferred.make<void>();
      const releaseOld = yield* Deferred.make<PullDetail>();
      const projected = yield* Ref.make<PullDetail | null>(null);
      const oldRead = Deferred.succeed(oldStarted, undefined).pipe(Effect.andThen(Deferred.await(releaseOld)));

      const oldFiber = yield* Effect.forkChild(
        workflow.read("old", oldRead).pipe(Effect.tap((value) => Ref.set(projected, value))),
      );
      yield* Deferred.await(oldStarted);
      const latestFiber = yield* Effect.forkChild(
        workflow
          .read("latest", Effect.succeed(detail("latest")))
          .pipe(Effect.tap((value) => Ref.set(projected, value))),
      );

      yield* Fiber.join(latestFiber);
      yield* Deferred.succeed(releaseOld, detail("old"));
      yield* Fiber.await(oldFiber);
      assert.strictEqual((yield* Ref.get(projected))?.platform_head_sha, "latest");
    }),
  );
});

it.layer(DetailWorkflowTest)("cleared detail reads", (it) => {
  it.effect("does not reuse a cached detail after the selection is cleared", () =>
    Effect.gen(function* () {
      const workflow = yield* DetailWorkflow;
      const calls = yield* Ref.make(0);
      const read = Ref.updateAndGet(calls, (count) => count + 1).pipe(Effect.map((count) => detail(`head-${count}`)));

      yield* workflow.read("selection", read);
      yield* workflow.clear;
      const reloaded = yield* workflow.read("selection", read);

      assert.strictEqual(reloaded.platform_head_sha, "head-2");
      assert.strictEqual(yield* Ref.get(calls), 2);
    }),
  );
});

it.layer(DetailWorkflowTest)("completed detail reads", (it) => {
  it.effect("does not retain a completed selection read", () =>
    Effect.gen(function* () {
      const workflow = yield* DetailWorkflow;
      const calls = yield* Ref.make(0);
      const read = Ref.updateAndGet(calls, (count) => count + 1).pipe(Effect.map((count) => detail(`head-${count}`)));

      const first = yield* workflow.read("selection", read);
      const second = yield* workflow.read("selection", read);

      assert.strictEqual(first.platform_head_sha, "head-1");
      assert.strictEqual(second.platform_head_sha, "head-2");
      assert.strictEqual(yield* Ref.get(calls), 2);
    }),
  );
});

it.layer(DetailWorkflowTest)("CI refreshes", (it) => {
  it.effect("shares concurrent work without retaining a completed response", () =>
    Effect.gen(function* () {
      const workflow = yield* DetailWorkflow;
      const started = yield* Deferred.make<void>();
      const release = yield* Deferred.make<PullDetail>();
      const firstRequest = Deferred.succeed(started, undefined).pipe(Effect.andThen(Deferred.await(release)));
      const first = yield* Effect.forkChild(workflow.refreshCI("pull", firstRequest));
      yield* Deferred.await(started);
      const joined = yield* Effect.forkChild(workflow.refreshCI("pull", Effect.succeed(detail("unused"))));
      yield* Effect.yieldNow;
      yield* Deferred.succeed(release, detail("first"));

      assert.strictEqual((yield* Fiber.join(first)).platform_head_sha, "first");
      assert.strictEqual((yield* Fiber.join(joined)).platform_head_sha, "first");
      const refreshed = yield* workflow.refreshCI("pull", Effect.succeed(detail("second")));
      assert.strictEqual(refreshed.platform_head_sha, "second");
    }),
  );

  it.effect("keeps shared CI work owned when one waiter is interrupted", () =>
    Effect.gen(function* () {
      const workflow = yield* DetailWorkflow;
      const started = yield* Deferred.make<void>();
      const release = yield* Deferred.make<PullDetail>();
      const replacementCalls = yield* Ref.make(0);
      const request = Deferred.succeed(started, undefined).pipe(Effect.andThen(Deferred.await(release)));
      const first = yield* Effect.forkChild(workflow.refreshCI("pull", request));
      yield* Deferred.await(started);
      const interruptedWaiter = yield* Effect.forkChild(workflow.refreshCI("pull", Effect.succeed(detail("unused"))));
      yield* Effect.yieldNow;
      yield* Fiber.interrupt(interruptedWaiter);
      const laterWaiter = yield* Effect.forkChild(
        workflow.refreshCI(
          "pull",
          Ref.update(replacementCalls, (count) => count + 1).pipe(Effect.as(detail("replacement"))),
        ),
      );
      yield* Effect.yieldNow;

      assert.strictEqual(yield* Ref.get(replacementCalls), 0);
      yield* Deferred.succeed(release, detail("shared"));
      assert.strictEqual((yield* Fiber.join(first)).platform_head_sha, "shared");
      assert.strictEqual((yield* Fiber.join(laterWaiter)).platform_head_sha, "shared");
    }),
  );
});
