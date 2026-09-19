import { assert, it } from "@effect/vitest";
import { Deferred, Effect, Fiber, Layer, Ref } from "effect";
import { GeneratedApiLive } from "../api/generated-api.js";
import type { PullRequest } from "../api/types.js";
import { providerItemKey } from "./provider-key.js";
import { PullsWorkflow, PullsWorkflowLive, type FetchPullResult } from "./pulls-workflow.js";

function pull(id: number): PullRequest {
  return { ID: id } as PullRequest;
}

it.layer(Layer.provide(PullsWorkflowLive, GeneratedApiLive))("pull list reads", (it) => {
  it.effect("prevents an older query from replacing the latest list", () =>
    Effect.gen(function* () {
      const workflow = yield* PullsWorkflow;
      const oldStarted = yield* Deferred.make<void>();
      const releaseOld = yield* Deferred.make<PullRequest[]>();
      const projected = yield* Ref.make<readonly PullRequest[]>([]);

      const oldFiber = yield* Effect.forkChild(
        workflow
          .list("old", Deferred.succeed(oldStarted, undefined).pipe(Effect.andThen(Deferred.await(releaseOld))))
          .pipe(Effect.tap((result) => Ref.set(projected, result))),
      );
      yield* Deferred.await(oldStarted);

      const latestFiber = yield* Effect.forkChild(
        workflow.list("latest", Effect.succeed([pull(2)])).pipe(Effect.tap((result) => Ref.set(projected, result))),
      );
      yield* Fiber.join(latestFiber);
      yield* Deferred.succeed(releaseOld, [pull(1)]);
      yield* Fiber.await(oldFiber);

      assert.deepStrictEqual(
        (yield* Ref.get(projected)).map((item) => item.ID),
        [2],
      );
    }),
  );

  it.effect("lets repeated reads of a slow query finish without restarting it", () =>
    Effect.gen(function* () {
      const workflow = yield* PullsWorkflow;
      const started = yield* Deferred.make<void>();
      const release = yield* Deferred.make<PullRequest[]>();
      const calls = yield* Ref.make(0);
      const read = Ref.update(calls, (count) => count + 1).pipe(
        Effect.andThen(Deferred.succeed(started, undefined)),
        Effect.andThen(Deferred.await(release)),
      );
      const first = yield* Effect.forkChild(workflow.list("same", read));
      yield* Deferred.await(started);
      const second = yield* Effect.forkChild(workflow.list("same", read));
      yield* Effect.yieldNow;
      assert.strictEqual(yield* Ref.get(calls), 1);
      yield* Deferred.succeed(release, [pull(7)]);
      assert.deepStrictEqual(yield* Fiber.join(first), [pull(7)]);
      assert.deepStrictEqual(yield* Fiber.join(second), [pull(7)]);
    }),
  );

  it.effect("retries an event refresh superseded by a newer foreground query", () =>
    Effect.gen(function* () {
      const workflow = yield* PullsWorkflow;
      const eventStarted = yield* Deferred.make<void>();
      const releaseEvent = yield* Deferred.make<readonly PullRequest[]>();
      const projected = yield* Ref.make<readonly PullRequest[]>([]);
      const eventFiber = yield* Effect.forkChild(
        workflow.reconcile(
          Deferred.succeed(eventStarted, undefined).pipe(Effect.andThen(Deferred.await(releaseEvent))),
          (result) => Ref.set(projected, result),
        ),
      );
      yield* Deferred.await(eventStarted);

      const latest = yield* workflow.list("latest", Effect.succeed([pull(2)]));
      yield* Ref.set(projected, latest);
      yield* Deferred.succeed(releaseEvent, [pull(1)]);
      const failure = yield* Fiber.join(eventFiber).pipe(Effect.flip);

      assert.strictEqual(failure._tag, "TransientTransportError");
      assert.deepStrictEqual(
        (yield* Ref.get(projected)).map((item) => item.ID),
        [2],
      );
    }),
  );
});

it.layer(Layer.provide(PullsWorkflowLive, GeneratedApiLive))("pull item refreshes", (it) => {
  it.effect("shares one request between concurrent refreshes of the same provider item", () =>
    Effect.gen(function* () {
      const workflow = yield* PullsWorkflow;
      const calls = yield* Ref.make(0);
      const started = yield* Deferred.make<void>();
      const release = yield* Deferred.make<void>();
      const result: FetchPullResult = { status: "found", pull: pull(7) };
      const key = providerItemKey({
        provider: "gitlab",
        platformHost: "gitlab.example.com",
        owner: "acme",
        name: "widgets",
        number: 7,
      });
      const request = Ref.update(calls, (count) => count + 1).pipe(
        Effect.andThen(Deferred.succeed(started, undefined)),
        Effect.andThen(Deferred.await(release)),
        Effect.as(result),
      );

      const first = yield* Effect.forkChild(workflow.refresh(key, request));
      yield* Deferred.await(started);
      const second = yield* Effect.forkChild(workflow.refresh(key, request));
      yield* Effect.yieldNow;

      assert.strictEqual(yield* Ref.get(calls), 1);
      yield* Deferred.succeed(release, undefined);
      assert.deepStrictEqual(yield* Fiber.join(first), result);
      assert.deepStrictEqual(yield* Fiber.join(second), result);
    }),
  );
});
