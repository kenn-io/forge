import { assert, it } from "@effect/vitest";
import { Cause, Deferred, Effect, Exit, Fiber, Option, Ref } from "effect";
import { ApiProblemError, TransientTransportError } from "../api/effect-errors.js";
import { ProblemCodes, type ProblemBody } from "../api/problems.js";
import { CommandQueueClosed } from "../effect/ordered-command-queue.js";
import { MutationNeedsReview, makeOrderedMutations, providerMutationFailureMessage } from "./ordered-mutations.js";

function staleProblem(): ProblemBody {
  return {
    code: ProblemCodes.conflict,
    type: "about:blank",
    title: "Conflict",
    detail: "pull request state changed",
    details: { reason: "stale_state" },
  };
}

it.effect("keeps the authoritative baseline when overlapping optimistic commands both fail", () =>
  Effect.scoped(
    Effect.gen(function* () {
      const mutations = yield* makeOrderedMutations<string>("labels");
      const visible = yield* Ref.make("baseline");
      const firstStarted = yield* Deferred.make<void>();
      const releaseFirst = yield* Deferred.make<void>();
      const first = yield* Effect.forkChild(
        Effect.exit(
          mutations.submit({
            key: "pull\u0000labels",
            baseline: "baseline",
            optimistic: "first",
            apply: (value) => Ref.set(visible, value),
            commit: Deferred.succeed(firstStarted, undefined).pipe(
              Effect.andThen(Deferred.await(releaseFirst)),
              Effect.andThen(
                Effect.fail(
                  TransientTransportError.make({ operation: "save first labels", cause: new Error("offline") }),
                ),
              ),
            ),
            refreshOnStale: Effect.succeed("refreshed"),
          }),
        ),
      );
      yield* Deferred.await(firstStarted);
      const second = yield* Effect.forkChild(
        Effect.exit(
          mutations.submit({
            key: "pull\u0000labels",
            baseline: "first",
            optimistic: "second",
            apply: (value) => Ref.set(visible, value),
            commit: Effect.fail(
              TransientTransportError.make({ operation: "save second labels", cause: new Error("offline") }),
            ),
            refreshOnStale: Effect.succeed("refreshed"),
          }),
        ),
      );

      yield* Effect.yieldNow;
      assert.strictEqual(yield* Ref.get(visible), "second");
      yield* Deferred.succeed(releaseFirst, undefined);
      yield* Fiber.join(first);
      yield* Fiber.join(second);

      assert.strictEqual(yield* Ref.get(visible), "baseline");
    }),
  ),
);

it.effect("does not let an older response overwrite a newer optimistic projection", () =>
  Effect.scoped(
    Effect.gen(function* () {
      const mutations = yield* makeOrderedMutations<string>("labels");
      const visible = yield* Ref.make("baseline");
      const firstStarted = yield* Deferred.make<void>();
      const releaseFirst = yield* Deferred.make<void>();
      const secondStarted = yield* Deferred.make<void>();
      const releaseSecond = yield* Deferred.make<void>();
      const first = yield* Effect.forkChild(
        mutations.submit({
          key: "pull\u0000labels",
          baseline: "baseline",
          optimistic: "first",
          apply: (value) => Ref.set(visible, value),
          commit: Deferred.succeed(firstStarted, undefined).pipe(
            Effect.andThen(Deferred.await(releaseFirst)),
            Effect.as("first confirmed"),
          ),
          refreshOnStale: Effect.succeed("refreshed"),
        }),
      );
      yield* Deferred.await(firstStarted);
      const second = yield* Effect.forkChild(
        mutations.submit({
          key: "pull\u0000labels",
          baseline: "first",
          optimistic: "second",
          apply: (value) => Ref.set(visible, value),
          commit: Deferred.succeed(secondStarted, undefined).pipe(
            Effect.andThen(Deferred.await(releaseSecond)),
            Effect.as("second confirmed"),
          ),
          refreshOnStale: Effect.succeed("refreshed"),
        }),
      );
      yield* Effect.yieldNow;
      assert.strictEqual(yield* Ref.get(visible), "second");

      yield* Deferred.succeed(releaseFirst, undefined);
      yield* Fiber.join(first);
      yield* Deferred.await(secondStarted);
      assert.strictEqual(yield* Ref.get(visible), "second");
      yield* Deferred.succeed(releaseSecond, undefined);
      yield* Fiber.join(second);
      assert.strictEqual(yield* Ref.get(visible), "second confirmed");
    }),
  ),
);

it.effect("preserves stale review context when authoritative refresh fails", () =>
  Effect.scoped(
    Effect.gen(function* () {
      const mutations = yield* makeOrderedMutations<string>("labels");
      const visible = yield* Ref.make("baseline");
      const exit = yield* Effect.exit(
        mutations.submit({
          key: "pull\u0000labels",
          baseline: "baseline",
          optimistic: "pending",
          apply: (value) => Ref.set(visible, value),
          commit: Effect.fail(new ApiProblemError({ operation: "save labels", problem: staleProblem() })),
          refreshOnStale: Effect.fail(
            TransientTransportError.make({ operation: "refresh labels", cause: new Error("offline") }),
          ),
        }),
      );

      assert.strictEqual(yield* Ref.get(visible), "baseline");
      assert.isTrue(Exit.isFailure(exit));
      if (Exit.isFailure(exit)) {
        const failure = Cause.findErrorOption(exit.cause);
        assert.isTrue(Option.isSome(failure));
        if (Option.isSome(failure)) {
          assert.instanceOf(failure.value, MutationNeedsReview);
          if (failure.value instanceof MutationNeedsReview) {
            assert.strictEqual(failure.value.problem.details?.["reason"], "stale_state");
            assert.strictEqual(failure.value.refreshFailure?._tag, "TransientTransportError");
            assert.strictEqual(
              providerMutationFailureMessage(failure.value, "labels changed"),
              "pull request state changed. The latest state could not be refreshed; review it before trying again.",
            );
          }
        }
      }
    }),
  ),
);

it.effect("rebases a failed optimistic command onto a newer authoritative read", () =>
  Effect.scoped(
    Effect.gen(function* () {
      const mutations = yield* makeOrderedMutations<string>("labels");
      const visible = yield* Ref.make("old baseline");
      const commitStarted = yield* Deferred.make<void>();
      const releaseCommit = yield* Deferred.make<void>();
      const command = yield* Effect.forkChild(
        Effect.exit(
          mutations.submit({
            key: "pull\u0000labels",
            baseline: "old baseline",
            optimistic: "pending",
            apply: (value) => Ref.set(visible, value),
            commit: Deferred.succeed(commitStarted, undefined).pipe(
              Effect.andThen(Deferred.await(releaseCommit)),
              Effect.andThen(
                Effect.fail(TransientTransportError.make({ operation: "save labels", cause: new Error("offline") })),
              ),
            ),
            refreshOnStale: Effect.succeed("refreshed"),
          }),
        ),
      );
      yield* Deferred.await(commitStarted);

      yield* mutations.rebase("pull\u0000labels", "new baseline");
      assert.strictEqual(yield* Ref.get(visible), "pending");
      yield* Deferred.succeed(releaseCommit, undefined);
      yield* Fiber.join(command);

      assert.strictEqual(yield* Ref.get(visible), "new baseline");
    }),
  ),
);

it.effect("installs an authoritative envelope and rebases pending projections atomically", () =>
  Effect.scoped(
    Effect.gen(function* () {
      const mutations = yield* makeOrderedMutations<string>("labels");
      const visible = yield* Ref.make({ title: "old title", labels: "old labels" });
      const commitStarted = yield* Deferred.make<void>();
      const releaseCommit = yield* Deferred.make<void>();
      const envelopeInstalled = yield* Deferred.make<void>();
      const releaseEnvelope = yield* Deferred.make<void>();
      const command = yield* Effect.forkChild(
        Effect.exit(
          mutations.submit({
            key: "pull\u0000labels",
            baseline: "old labels",
            optimistic: "pending labels",
            apply: (labels) => Ref.update(visible, (current) => ({ ...current, labels })),
            commit: Deferred.succeed(commitStarted, undefined).pipe(
              Effect.andThen(Deferred.await(releaseCommit)),
              Effect.andThen(
                Effect.fail(TransientTransportError.make({ operation: "save labels", cause: new Error("offline") })),
              ),
            ),
            refreshOnStale: Effect.succeed("refreshed labels"),
          }),
        ),
      );
      yield* Deferred.await(commitStarted);

      const rebase = yield* Effect.forkChild(
        mutations.rebaseAll(
          Deferred.succeed(envelopeInstalled, undefined).pipe(
            Effect.andThen(Ref.set(visible, { title: "new title", labels: "new labels" })),
            Effect.andThen(Deferred.await(releaseEnvelope)),
            Effect.as(true),
          ),
          [{ key: "pull\u0000labels", confirmed: "new labels" }],
        ),
      );
      yield* Deferred.await(envelopeInstalled);
      yield* Deferred.succeed(releaseCommit, undefined);
      yield* Effect.yieldNow;
      yield* Deferred.succeed(releaseEnvelope, undefined);
      yield* Fiber.join(rebase);
      yield* Fiber.join(command);

      assert.deepStrictEqual(yield* Ref.get(visible), { title: "new title", labels: "new labels" });
    }),
  ),
);

it.effect("does not reapply a rejected optimistic value after a concurrent rebase", () =>
  Effect.scoped(
    Effect.gen(function* () {
      const mutations = yield* makeOrderedMutations<string>("labels");
      const visible = yield* Ref.make("old baseline");
      const pendingApplications = yield* Ref.make(0);
      const commitStarted = yield* Deferred.make<void>();
      const releaseCommit = yield* Deferred.make<void>();
      const rebaseProjectionStarted = yield* Deferred.make<void>();
      const releaseRebaseProjection = yield* Deferred.make<void>();
      const apply = (value: string) =>
        value !== "pending"
          ? Ref.set(visible, value)
          : Ref.updateAndGet(pendingApplications, (count) => count + 1).pipe(
              Effect.flatMap((count) =>
                count === 2
                  ? Deferred.succeed(rebaseProjectionStarted, undefined).pipe(
                      Effect.andThen(Deferred.await(releaseRebaseProjection)),
                      Effect.andThen(Ref.set(visible, value)),
                    )
                  : Ref.set(visible, value),
              ),
            );
      const command = yield* Effect.forkChild(
        Effect.exit(
          mutations.submit({
            key: "pull\u0000labels",
            baseline: "old baseline",
            optimistic: "pending",
            apply,
            commit: Deferred.succeed(commitStarted, undefined).pipe(
              Effect.andThen(Deferred.await(releaseCommit)),
              Effect.andThen(
                Effect.fail(TransientTransportError.make({ operation: "save labels", cause: new Error("offline") })),
              ),
            ),
            refreshOnStale: Effect.succeed("refreshed"),
          }),
        ),
      );
      yield* Deferred.await(commitStarted);
      const rebase = yield* Effect.forkChild(mutations.rebase("pull\u0000labels", "new baseline"));
      yield* Deferred.await(rebaseProjectionStarted);

      yield* Deferred.succeed(releaseCommit, undefined);
      yield* Deferred.succeed(releaseRebaseProjection, undefined);
      yield* Fiber.join(command);
      yield* Fiber.join(rebase);

      assert.strictEqual(yield* Ref.get(visible), "new baseline");
    }),
  ),
);

it.effect("rejects commands accepted behind a stale conflict until the key drains", () =>
  Effect.scoped(
    Effect.gen(function* () {
      const mutations = yield* makeOrderedMutations<string>("labels");
      const visible = yield* Ref.make("baseline");
      const firstStarted = yield* Deferred.make<void>();
      const releaseFirst = yield* Deferred.make<void>();
      const secondCommits = yield* Ref.make(0);
      const first = yield* Effect.forkChild(
        Effect.exit(
          mutations.submit({
            key: "pull\u0000labels",
            baseline: "baseline",
            optimistic: "first",
            apply: (value) => Ref.set(visible, value),
            commit: Deferred.succeed(firstStarted, undefined).pipe(
              Effect.andThen(Deferred.await(releaseFirst)),
              Effect.andThen(Effect.fail(new ApiProblemError({ operation: "save labels", problem: staleProblem() }))),
            ),
            refreshOnStale: Effect.succeed("provider baseline"),
          }),
        ),
      );
      yield* Deferred.await(firstStarted);
      const second = yield* Effect.forkChild(
        Effect.exit(
          mutations.submit({
            key: "pull\u0000labels",
            baseline: "first",
            optimistic: "second",
            apply: (value) => Ref.set(visible, value),
            commit: Ref.update(secondCommits, (count) => count + 1).pipe(Effect.as("second confirmed")),
            refreshOnStale: Effect.succeed("provider baseline"),
          }),
        ),
      );
      yield* Effect.yieldNow;
      assert.strictEqual(yield* Ref.get(visible), "second");

      yield* Deferred.succeed(releaseFirst, undefined);
      const firstExit = yield* Fiber.join(first);
      const secondExit = yield* Fiber.join(second);

      assert.isTrue(Exit.isFailure(firstExit));
      assert.isTrue(Exit.isFailure(secondExit));
      assert.strictEqual(yield* Ref.get(secondCommits), 0);
      assert.strictEqual(yield* Ref.get(visible), "provider baseline");
    }),
  ),
);

it.effect("runs unrelated mutation keys without head-of-line blocking", () =>
  Effect.scoped(
    Effect.gen(function* () {
      const mutations = yield* makeOrderedMutations<string>("provider mutations");
      const firstStarted = yield* Deferred.make<void>();
      const releaseFirst = yield* Deferred.make<void>();
      const secondStarted = yield* Deferred.make<void>();
      const apply = () => Effect.void;
      const first = yield* Effect.forkChild(
        mutations.submit({
          key: "pull-a",
          baseline: "a",
          optimistic: "pending-a",
          apply,
          commit: Deferred.succeed(firstStarted, undefined).pipe(
            Effect.andThen(Deferred.await(releaseFirst)),
            Effect.as("confirmed-a"),
          ),
          refreshOnStale: Effect.succeed("a"),
        }),
      );
      yield* Deferred.await(firstStarted);
      const second = yield* Effect.forkChild(
        mutations.submit({
          key: "pull-b",
          baseline: "b",
          optimistic: "pending-b",
          apply,
          commit: Deferred.succeed(secondStarted, undefined).pipe(Effect.as("confirmed-b")),
          refreshOnStale: Effect.succeed("b"),
        }),
      );

      yield* Deferred.await(secondStarted);
      yield* Fiber.join(second);
      yield* Deferred.succeed(releaseFirst, undefined);
      yield* Fiber.join(first);
    }),
  ),
);

it.effect("acknowledges commands in acceptance order and continues after failure", () =>
  Effect.scoped(
    Effect.gen(function* () {
      const mutations = yield* makeOrderedMutations<number>("provider mutations");
      const visible = yield* Ref.make(0);
      const observed = yield* Ref.make<ReadonlyArray<number>>([]);
      const exits = yield* Effect.forEach([1, 2, 3], (value) =>
        Effect.exit(
          mutations.submit({
            key: `item-${value}`,
            baseline: 0,
            optimistic: value,
            apply: (next) => Ref.set(visible, next),
            commit: Ref.update(observed, (values) => [...values, value]).pipe(
              Effect.andThen(
                value === 2
                  ? Effect.fail(
                      TransientTransportError.make({ operation: "save mutation", cause: new Error("rejected") }),
                    )
                  : Effect.succeed(value),
              ),
            ),
            refreshOnStale: Effect.succeed(0),
          }),
        ),
      );

      assert.deepStrictEqual(yield* Ref.get(observed), [1, 2, 3]);
      assert.isTrue(Exit.isSuccess(exits[0]));
      assert.isTrue(Exit.isFailure(exits[1]));
      assert.isTrue(Exit.isSuccess(exits[2]));
    }),
  ),
);

it.effect("completes accepted callers when the mutation queue shuts down", () =>
  Effect.scoped(
    Effect.gen(function* () {
      const mutations = yield* makeOrderedMutations<string>("closing mutations", 1);
      const visible = yield* Ref.make("baseline");
      const activeStarted = yield* Deferred.make<void>();
      const first = yield* Effect.forkChild(
        Effect.exit(
          mutations.submit({
            key: "first",
            baseline: "baseline",
            optimistic: "first",
            apply: (value) => Ref.set(visible, value),
            commit: Deferred.succeed(activeStarted, undefined).pipe(Effect.andThen(Effect.never)),
            refreshOnStale: Effect.succeed("refreshed"),
          }),
        ),
      );
      yield* Deferred.await(activeStarted);
      const second = yield* Effect.forkChild(
        Effect.exit(
          mutations.submit({
            key: "second",
            baseline: "baseline",
            optimistic: "second",
            apply: (value) => Ref.set(visible, value),
            commit: Effect.succeed("second"),
            refreshOnStale: Effect.succeed("refreshed"),
          }),
        ),
      );

      yield* mutations.shutdown;
      for (const exit of [yield* Fiber.join(first), yield* Fiber.join(second)]) {
        assert.isTrue(Exit.isFailure(exit));
        if (Exit.isFailure(exit)) {
          const failure = Cause.findErrorOption(exit.cause);
          assert.isTrue(Option.isSome(failure));
          if (Option.isSome(failure)) assert.instanceOf(failure.value, CommandQueueClosed);
        }
      }
    }),
  ),
);
