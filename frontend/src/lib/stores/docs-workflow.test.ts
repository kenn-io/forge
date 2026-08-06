import { assert, it } from "@effect/vitest";
import { Cause, Deferred, Effect, Exit, Fiber, Ref } from "effect";
import { DocsRequestError } from "../api/docs/api.js";
import { makeDocsWorkflow } from "./docs-workflow.js";

it.effect("interrupts a superseded Docs read and publishes only its replacement", () =>
  Effect.scoped(
    Effect.gen(function* () {
      const workflow = yield* makeDocsWorkflow;
      const firstStarted = yield* Deferred.make<void>();
      const published = yield* Ref.make<ReadonlyArray<string>>([]);
      const first = yield* workflow
        .read(
          "docs-view",
          "document",
          Deferred.succeed(firstStarted, undefined).pipe(
            Effect.andThen(Effect.never),
            Effect.tap((value: string) => Ref.update(published, (values) => [...values, value])),
          ),
        )
        .pipe(Effect.forkChild);
      yield* Deferred.await(firstStarted);

      yield* workflow
        .read(
          "docs-view",
          "document",
          Effect.succeed("replacement").pipe(
            Effect.tap((value) => Ref.update(published, (values) => [...values, value])),
          ),
        )
        .pipe(Effect.forkChild, Effect.flatMap(Fiber.join));

      const firstExit = yield* Fiber.await(first);
      assert.isTrue(Exit.isFailure(firstExit) && Cause.hasInterruptsOnly(firstExit.cause));
      assert.deepStrictEqual(yield* Ref.get(published), ["replacement"]);
    }),
  ),
);

it.effect("runs accepted Docs mutations once in submission order", () =>
  Effect.scoped(
    Effect.gen(function* () {
      const workflow = yield* makeDocsWorkflow;
      const firstStarted = yield* Deferred.make<void>();
      const releaseFirst = yield* Deferred.make<void>();
      const observed = yield* Ref.make<ReadonlyArray<string>>([]);

      const first = yield* workflow
        .mutate(
          Deferred.succeed(firstStarted, undefined).pipe(
            Effect.andThen(Ref.update(observed, (values) => [...values, "write"])),
            Effect.andThen(Deferred.await(releaseFirst)),
          ),
        )
        .pipe(Effect.forkChild);
      yield* Deferred.await(firstStarted);
      const second = yield* workflow
        .mutate(Ref.update(observed, (values) => [...values, "publish"]))
        .pipe(Effect.forkChild);
      yield* Effect.yieldNow;

      assert.deepStrictEqual(yield* Ref.get(observed), ["write"]);
      yield* Deferred.succeed(releaseFirst, undefined);
      yield* Fiber.join(first);
      yield* Fiber.join(second);
      assert.deepStrictEqual(yield* Ref.get(observed), ["write", "publish"]);
    }),
  ),
);

it.effect("moves an unfinished Docs publish outcome to the remounted folder surface", () =>
  Effect.scoped(
    Effect.gen(function* () {
      const workflow = yield* makeDocsWorkflow;
      const requestStarted = yield* Deferred.make<void>();
      const releaseRequest = yield* Deferred.make<void>();
      const firstStates = yield* Ref.make<ReadonlyArray<string>>([]);
      const secondStates = yield* Ref.make<ReadonlyArray<string>>([]);
      const record = (states: Ref.Ref<ReadonlyArray<string>>) => (state: { readonly kind: string }) =>
        Ref.update(states, (values) => [...values, state.kind]);

      yield* workflow.claimPublisher("notes", "first-session", record(firstStates));
      const publish = yield* workflow
        .publish({
          folderID: "notes",
          sessionID: "first-session",
          message: "docs: publish notes",
          request: Deferred.succeed(requestStarted, undefined).pipe(
            Effect.andThen(Deferred.await(releaseRequest)),
            Effect.andThen(
              Effect.fail(
                DocsRequestError.make({
                  operation: "publish Docs",
                  message: "push failed",
                  status: 500,
                  code: "push_failed_after_commit",
                  commit: "abcdef1234567890",
                  cause: new Error("response unavailable"),
                }),
              ),
            ),
          ),
        })
        .pipe(Effect.forkChild);
      yield* Deferred.await(requestStarted);
      yield* workflow.releasePublisher("first-session");
      yield* workflow.claimPublisher("notes", "second-session", record(secondStates));
      yield* Deferred.succeed(releaseRequest, undefined);
      yield* Fiber.join(publish);

      assert.deepStrictEqual(yield* Ref.get(firstStates), ["pending"]);
      assert.deepStrictEqual(yield* Ref.get(secondStates), ["pending", "failed"]);
    }),
  ),
);

it.effect("does not deliver a completed Docs publish success to a later dialog session", () =>
  Effect.scoped(
    Effect.gen(function* () {
      const workflow = yield* makeDocsWorkflow;
      const firstStates = yield* Ref.make<ReadonlyArray<string>>([]);
      const secondStates = yield* Ref.make<ReadonlyArray<string>>([]);
      const record = (states: Ref.Ref<ReadonlyArray<string>>) => (state: { readonly kind: string }) =>
        Ref.update(states, (values) => [...values, state.kind]);

      yield* workflow.claimPublisher("notes", "first-session", record(firstStates));
      yield* workflow.publish({
        folderID: "notes",
        sessionID: "first-session",
        message: "docs: publish notes",
        request: Effect.succeed({
          commit: "abcdef1234567890",
          short_commit: "abcdef1",
          branch: "main",
          pushed: true,
          files: [],
        }),
      });
      yield* workflow.releasePublisher("first-session");
      yield* workflow.claimPublisher("notes", "second-session", record(secondStates));

      assert.deepStrictEqual(yield* Ref.get(firstStates), ["pending", "succeeded"]);
      assert.deepStrictEqual(yield* Ref.get(secondStates), []);
    }),
  ),
);

it.effect("delivers an accepted publish to the replacement surface even when its submitter starts late", () =>
  Effect.scoped(
    Effect.gen(function* () {
      const workflow = yield* makeDocsWorkflow;
      const firstStates = yield* Ref.make<ReadonlyArray<string>>([]);
      const secondStates = yield* Ref.make<ReadonlyArray<string>>([]);
      const requests = yield* Ref.make(0);
      const record = (states: Ref.Ref<ReadonlyArray<string>>) => (state: { readonly kind: string }) =>
        Ref.update(states, (values) => [...values, state.kind]);

      yield* workflow.claimPublisher("notes", "first-session", record(firstStates));
      yield* workflow.claimPublisher("notes", "second-session", record(secondStates));
      yield* workflow.publish({
        folderID: "notes",
        sessionID: "first-session",
        message: "docs: publish notes",
        request: Ref.update(requests, (count) => count + 1).pipe(
          Effect.andThen(
            Effect.fail(
              DocsRequestError.make({
                operation: "publish Docs",
                message: "push failed",
                status: 500,
                code: "push_failed_after_commit",
                commit: "abcdef1234567890",
                cause: new Error("response unavailable"),
              }),
            ),
          ),
        ),
      });

      assert.deepStrictEqual(yield* Ref.get(firstStates), []);
      assert.deepStrictEqual(yield* Ref.get(secondStates), ["pending", "failed"]);
      assert.strictEqual(yield* Ref.get(requests), 1);
    }),
  ),
);
