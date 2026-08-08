import { assert, it } from "@effect/vitest";
import { Deferred, Effect, Fiber, Ref } from "effect";
import type { RateLimitsResponse, SyncStatus } from "../api/types.js";
import { SyncWorkflow, SyncWorkflowLive } from "./sync-workflow.js";

it.layer(SyncWorkflowLive)("sync status refreshes", (it) => {
  it.effect("shares one pair of reads between concurrent refresh demand", () =>
    Effect.gen(function* () {
      const workflow = yield* SyncWorkflow;
      const statusCalls = yield* Ref.make(0);
      const rateCalls = yield* Ref.make(0);
      const started = yield* Deferred.make<void>();
      const release = yield* Deferred.make<void>();
      const status: SyncStatus = { running: false, last_run_at: "", last_error: "" };
      const rateLimits: RateLimitsResponse = { provider_pools: {}, local_ceilings: {} };
      const statusRead = Ref.update(statusCalls, (count) => count + 1).pipe(
        Effect.andThen(Deferred.succeed(started, undefined)),
        Effect.andThen(Deferred.await(release)),
        Effect.as(status),
      );
      const rateRead = Ref.update(rateCalls, (count) => count + 1).pipe(
        Effect.andThen(Deferred.await(release)),
        Effect.as(rateLimits),
      );

      const first = yield* Effect.forkChild(workflow.refresh(0, statusRead, rateRead));
      yield* Deferred.await(started);
      const second = yield* Effect.forkChild(workflow.refresh(0, statusRead, rateRead));
      yield* Effect.yieldNow;

      assert.strictEqual(yield* Ref.get(statusCalls), 1);
      assert.strictEqual(yield* Ref.get(rateCalls), 1);
      yield* Deferred.succeed(release, undefined);
      yield* Fiber.join(first);
      yield* Fiber.join(second);
    }),
  );
});
