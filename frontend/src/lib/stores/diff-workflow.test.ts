import { assert, it } from "@effect/vitest";
import { Deferred, Effect, Fiber, Ref } from "effect";
import type { DiffResponseWire, FilesResponseWire } from "../api/types.js";
import { DiffWorkflow, DiffWorkflowLive, type ProviderDiffRead } from "./diff-workflow.js";

function diff(path: string): ProviderDiffRead {
  return {
    diff: { files: [{ path }] } as DiffResponseWire,
    files: { files: [{ path }] } as FilesResponseWire,
  };
}

it.layer(DiffWorkflowLive)("selected diff reads", (it) => {
  it.effect("interrupts the old request when the diff scope changes", () =>
    Effect.gen(function* () {
      const workflow = yield* DiffWorkflow;
      const oldStarted = yield* Deferred.make<void>();
      const interrupted = yield* Ref.make(false);
      const projected = yield* Ref.make<ProviderDiffRead | null>(null);
      const oldRead = Deferred.succeed(oldStarted, undefined).pipe(
        Effect.andThen(Effect.never),
        Effect.onInterrupt(() => Ref.set(interrupted, true)),
      );

      const oldFiber = yield* Effect.forkChild(workflow.read("old", oldRead));
      yield* Deferred.await(oldStarted);
      const latestFiber = yield* Effect.forkChild(
        workflow
          .read("latest", Effect.succeed(diff("latest.ts")))
          .pipe(Effect.tap((value) => Ref.set(projected, value))),
      );

      yield* Fiber.join(latestFiber);
      yield* Fiber.await(oldFiber);
      assert.isTrue(yield* Ref.get(interrupted));
      assert.strictEqual((yield* Ref.get(projected))?.diff.files?.[0]?.path, "latest.ts");
    }),
  );
});

it.layer(DiffWorkflowLive)("shared diff reads", (it) => {
  it.effect("shares one request between concurrent demand for the same diff", () =>
    Effect.gen(function* () {
      const workflow = yield* DiffWorkflow;
      const calls = yield* Ref.make(0);
      const started = yield* Deferred.make<void>();
      const release = yield* Deferred.make<void>();
      const result = diff("shared.ts");
      const read = Ref.update(calls, (count) => count + 1).pipe(
        Effect.andThen(Deferred.succeed(started, undefined)),
        Effect.andThen(Deferred.await(release)),
        Effect.as(result),
      );

      const first = yield* Effect.forkChild(workflow.read("selection", read));
      yield* Deferred.await(started);
      const second = yield* Effect.forkChild(workflow.read("selection", read));
      yield* Effect.yieldNow;

      assert.strictEqual(yield* Ref.get(calls), 1);
      yield* Deferred.succeed(release, undefined);
      assert.deepStrictEqual(yield* Fiber.join(first), result);
      assert.deepStrictEqual(yield* Fiber.join(second), result);
    }),
  );
});
