import { assert, it } from "@effect/vitest";
import { Deferred, Effect, Exit, Fiber, Ref } from "effect";
import { TestClock } from "effect/testing";
import { makeGeneratedApiLayer } from "../../api/generated-api.js";
import { KataMutationOutcomeUnknownError } from "../../api/kata/taskClient.js";
import type { KataWorkspaceSnapshotResponse } from "../../api/kata/snapshot.js";
import { normalizeKataWorkspaceSnapshot } from "../../api/kata/snapshotProjection.js";
import { createRuntimeClient } from "../../api/runtime.js";
import { createKataAuthorityStore } from "../../stores/kata-authority.svelte.js";
import { KataMutationError, makeKataWorkflow } from "./kata-workflow.js";

function snapshot(generation: number): KataWorkspaceSnapshotResponse {
  return {
    server_instance_id: "server-a",
    daemon_id: "home",
    intent: { scope: "global", authority: "open" },
    generation,
    invalidation_epoch: generation,
    event_cursor: generation,
    fetched_at: "2026-08-04T10:00:00Z",
    projects: [],
    member_issue_uids: [],
    issues: [],
    enrichment: {},
  };
}

it.effect("interrupts a superseded Kata snapshot before accepting the replacement", () =>
  Effect.scoped(
    Effect.gen(function* () {
      const workflow = yield* makeKataWorkflow;
      const store = createKataAuthorityStore();
      const interrupted = yield* Deferred.make<void>();
      const intent = { daemon_id: "home", scope: "global", authority: "open" } as const;
      const first = yield* Effect.forkChild(
        workflow.latestSnapshot(
          "workspace",
          store,
          intent,
          Effect.never.pipe(Effect.onInterrupt(() => Deferred.succeed(interrupted, undefined))),
        ),
      );
      yield* Effect.yieldNow;

      const accepted = yield* workflow.latestSnapshot("workspace", store, intent, Effect.succeed(snapshot(2)));
      yield* Deferred.await(interrupted);

      assert.isTrue(accepted);
      assert.strictEqual(store.snapshot?.generation, 2);
      assert.isTrue(Exit.isFailure(yield* Fiber.await(first)));
    }),
  ),
);

it.effect("supersedes snapshot publication as part of the authority transaction", () =>
  Effect.scoped(
    Effect.gen(function* () {
      const workflow = yield* makeKataWorkflow;
      const store = createKataAuthorityStore();
      const firstPublicationStarted = yield* Deferred.make<void>();
      const firstPublicationInterrupted = yield* Deferred.make<void>();
      const publications = yield* Ref.make<ReadonlyArray<number>>([]);
      const intent = { daemon_id: "home", scope: "global", authority: "open" } as const;
      const first = yield* Effect.forkChild(
        workflow.latestSnapshot(
          "workspace",
          store,
          intent,
          Effect.succeed(snapshot(1)),
          Deferred.succeed(firstPublicationStarted, undefined).pipe(
            Effect.andThen(Effect.never),
            Effect.onInterrupt(() => Deferred.succeed(firstPublicationInterrupted, undefined)),
          ),
        ),
      );
      yield* Effect.yieldNow;
      assert.isTrue(yield* Deferred.isDone(firstPublicationStarted));

      const accepted = yield* workflow.latestSnapshot(
        "workspace",
        store,
        intent,
        Effect.succeed(snapshot(2)),
        Ref.update(publications, (seen) => [...seen, 2]),
      );
      yield* Deferred.await(firstPublicationInterrupted);

      assert.isTrue(accepted);
      assert.deepStrictEqual(yield* Ref.get(publications), [2]);
      assert.strictEqual(store.snapshot?.generation, 2);
      assert.isTrue(Exit.isFailure(yield* Fiber.await(first)));
    }),
  ),
);

it.effect("publishes mutation acknowledgement before revalidation finishes", () =>
  Effect.scoped(
    Effect.gen(function* () {
      const workflow = yield* makeKataWorkflow;
      const releaseRevalidation = yield* Deferred.make<void>();
      const acknowledgementPublished = yield* Deferred.make<void>();
      const returned = yield* Deferred.make<void>();
      const fiber = yield* Effect.forkChild(
        workflow
          .mutateAndRevalidate(Effect.succeed("saved"), Deferred.await(releaseRevalidation).pipe(Effect.as(true)), () =>
            Deferred.succeed(acknowledgementPublished, undefined),
          )
          .pipe(Effect.tap(() => Deferred.succeed(returned, undefined))),
      );
      yield* Deferred.await(acknowledgementPublished);
      yield* Effect.yieldNow;

      assert.isTrue(yield* Deferred.isDone(returned));
      yield* Deferred.succeed(releaseRevalidation, undefined);
      yield* Fiber.join(fiber);
    }),
  ),
);

it.effect("keeps mutation revalidation ahead of later mutations", () =>
  Effect.scoped(
    Effect.gen(function* () {
      const workflow = yield* makeKataWorkflow;
      const firstMutationStarted = yield* Deferred.make<void>();
      const releaseFirstMutation = yield* Deferred.make<void>();
      const observed = yield* Ref.make<ReadonlyArray<string>>([]);

      const first = yield* Effect.forkChild(
        workflow.mutateAndRevalidate(
          Deferred.succeed(firstMutationStarted, undefined).pipe(
            Effect.andThen(Ref.update(observed, (events) => [...events, "first mutation"])),
            Effect.andThen(Deferred.await(releaseFirstMutation)),
            Effect.as("first acknowledgement"),
          ),
          Ref.update(observed, (events) => [...events, "first revalidation"]).pipe(Effect.as(true)),
        ),
      );
      yield* Deferred.await(firstMutationStarted);

      const second = yield* Effect.forkChild(
        workflow.mutateAndRevalidate(
          Ref.update(observed, (events) => [...events, "second mutation"]).pipe(Effect.as("second acknowledgement")),
          Ref.update(observed, (events) => [...events, "second revalidation"]).pipe(Effect.as(true)),
        ),
      );
      yield* Effect.yieldNow;

      assert.deepStrictEqual(yield* Ref.get(observed), ["first mutation"]);
      yield* Deferred.succeed(releaseFirstMutation, undefined);
      const firstResult = yield* Fiber.join(first);
      const secondResult = yield* Fiber.join(second);
      yield* firstResult.replacement;
      yield* secondResult.replacement;

      assert.deepStrictEqual(yield* Ref.get(observed), [
        "first mutation",
        "first revalidation",
        "second mutation",
        "second revalidation",
      ]);
      assert.strictEqual(firstResult.acknowledgement, "first acknowledgement");
      assert.strictEqual(secondResult.acknowledgement, "second acknowledgement");
    }),
  ),
);

it.effect("keeps an accepted Kata mutation running when its submitter is interrupted", () =>
  Effect.scoped(
    Effect.gen(function* () {
      const workflow = yield* makeKataWorkflow;
      const mutationStarted = yield* Deferred.make<void>();
      const releaseMutation = yield* Deferred.make<void>();
      const revalidationFinished = yield* Deferred.make<void>();
      const submitter = yield* workflow
        .mutateAndRevalidate(
          Deferred.succeed(mutationStarted, undefined).pipe(
            Effect.andThen(Deferred.await(releaseMutation)),
            Effect.as("saved"),
          ),
          Deferred.succeed(revalidationFinished, undefined).pipe(Effect.as(true)),
        )
        .pipe(Effect.forkChild);
      yield* Deferred.await(mutationStarted);

      yield* Fiber.interrupt(submitter);
      yield* Deferred.succeed(releaseMutation, undefined);
      yield* Deferred.await(revalidationFinished);

      assert.isTrue(yield* Deferred.isDone(revalidationFinished));
    }),
  ),
);

it.effect("retains an uncertain mutation fence before allowing another write to the same target", () =>
  Effect.scoped(
    Effect.gen(function* () {
      const workflow = yield* makeKataWorkflow;
      const attempts = yield* Ref.make(0);
      const baseline = normalizeKataWorkspaceSnapshot(snapshot(1));
      const uncertainty = {
        identity: {
          key: "home:issue:issue-1",
          daemonId: "home",
          operation: "add Kata comment",
          target: "issue-1",
        },
        baseline,
        readFresh: Effect.succeed(snapshot(2)),
      };
      const first = yield* workflow
        .mutateAndRevalidate(
          Ref.update(attempts, (count) => count + 1).pipe(
            Effect.andThen(
              Effect.fail(
                new KataMutationError({
                  message: "Kata could not confirm whether the mutation was applied.",
                  cause: KataMutationOutcomeUnknownError.make({
                    operation: "add Kata comment",
                    message: "Kata could not confirm whether the mutation was applied.",
                    cause: new Error("response lost"),
                  }),
                }),
              ),
            ),
          ),
          Effect.succeed(true),
          undefined,
          uncertainty,
        )
        .pipe(Effect.exit);
      const second = yield* workflow
        .mutateAndRevalidate(
          Ref.update(attempts, (count) => count + 1).pipe(Effect.as("replacement")),
          Effect.succeed(true),
          undefined,
          uncertainty,
        )
        .pipe(Effect.exit);

      assert.isTrue(first._tag === "Failure");
      assert.isTrue(second._tag === "Failure");
      if (second._tag === "Failure") assert.include(String(second.cause), "KataMutationBlocked");
      assert.strictEqual(yield* Ref.get(attempts), 1);
    }),
  ),
);

it.effect("delivers a retained fence to a remounted surface and resolves it from fresh authority", () =>
  Effect.scoped(
    Effect.gen(function* () {
      const workflow = yield* makeKataWorkflow;
      const states = yield* Ref.make<ReadonlyArray<string>>([]);
      const baseline = normalizeKataWorkspaceSnapshot(snapshot(1));
      const uncertainty = {
        identity: {
          key: "home:issue:issue-1",
          daemonId: "home",
          operation: "add Kata comment",
          target: "issue-1",
        },
        baseline,
        readFresh: Effect.succeed(snapshot(2)),
        isApplied: () => true,
      };
      yield* workflow
        .mutateAndRevalidate(
          Effect.fail(
            KataMutationOutcomeUnknownError.make({
              operation: "add Kata comment",
              message: "Kata could not confirm whether the mutation was applied.",
              cause: new Error("response lost"),
            }),
          ),
          Effect.succeed(true),
          undefined,
          uncertainty,
        )
        .pipe(Effect.exit);

      yield* workflow.claimMutation(uncertainty.identity.key, "replacement-surface", (state) =>
        Ref.update(states, (seen) => [...seen, `${state.kind}:${state.kind === "resolved" ? state.resolution : ""}`]),
      );
      const resolution = yield* workflow.reconcileMutation(uncertainty.identity.key);

      assert.strictEqual(resolution, "applied");
      assert.deepStrictEqual(yield* Ref.get(states), ["unknown:", "reconciling:", "resolved:applied"]);
    }),
  ),
);

it.effect("reconnects Kata events from the latest accepted snapshot checkpoint", () =>
  Effect.scoped(
    Effect.gen(function* () {
      const seenCursors: number[] = [];
      const firstDisconnect = yield* Deferred.make<void>();
      const fetchImpl: typeof fetch = (input, init) => {
        const request = input instanceof Request ? input : new Request(input, init);
        seenCursors.push(Number(request.headers.get("Last-Event-ID") ?? 0));
        return Promise.resolve(
          new Response(
            new ReadableStream<Uint8Array>({
              start(controller) {
                controller.close();
              },
            }),
            { status: 200, headers: { "Content-Type": "text/event-stream" } },
          ),
        );
      };
      const workflow = yield* makeKataWorkflow;
      yield* workflow
        .connectEvents({
          owner: "workspace",
          daemonId: "work",
          checkpoint: 51,
          onOpen: Effect.void,
          onFrame: () => Effect.void,
          onError: () => Deferred.succeed(firstDisconnect, undefined),
        })
        .pipe(Effect.provide(makeGeneratedApiLayer(createRuntimeClient(fetchImpl))));
      yield* Deferred.await(firstDisconnect);

      yield* workflow.updateEventSource("workspace", "work", 52);
      yield* TestClock.adjust("500 millis");
      yield* Effect.yieldNow;

      assert.deepStrictEqual(seenCursors, [51, 52]);
      yield* workflow.disconnectEvents("workspace");
    }),
  ),
);

it.effect("disconnects the active Kata event reader", () =>
  Effect.scoped(
    Effect.gen(function* () {
      const readerWaiting = yield* Deferred.make<void>();
      const readerCancelled = yield* Deferred.make<void>();
      let pulls = 0;
      const fetchImpl: typeof fetch = () =>
        Promise.resolve(
          new Response(
            new ReadableStream<Uint8Array>({
              pull(controller) {
                pulls += 1;
                if (pulls === 1) {
                  controller.enqueue(new TextEncoder().encode(": connected\n\n"));
                  return;
                }
                Deferred.doneUnsafe(readerWaiting, Effect.void);
              },
              cancel() {
                Deferred.doneUnsafe(readerCancelled, Effect.void);
              },
            }),
            { status: 200, headers: { "Content-Type": "text/event-stream" } },
          ),
        );
      const apiLayer = makeGeneratedApiLayer(createRuntimeClient(fetchImpl));
      const workflow = yield* makeKataWorkflow;
      yield* workflow
        .connectEvents({
          owner: "workspace",
          daemonId: "work",
          checkpoint: 51,
          onOpen: Effect.void,
          onFrame: () => Effect.void,
          onError: () => Effect.void,
        })
        .pipe(Effect.provide(apiLayer));
      yield* Deferred.await(readerWaiting);

      yield* workflow.disconnectEvents("workspace");

      assert.isTrue(yield* Deferred.isDone(readerCancelled));
    }),
  ),
);

it.effect("keeps independent Kata authorities connected at the same time", () =>
  Effect.scoped(
    Effect.gen(function* () {
      const readersWaiting = yield* Deferred.make<void>();
      const cancelledDaemons: string[] = [];
      let waitingReaders = 0;
      const fetchImpl: typeof fetch = (input, init) => {
        const request = input instanceof Request ? new Request(input, init) : new Request(input, init);
        const daemon = request.headers.get("X-Kenn-Forge-Kata-Daemon") ?? "default";
        let pulls = 0;
        return Promise.resolve(
          new Response(
            new ReadableStream<Uint8Array>({
              pull(controller) {
                pulls += 1;
                if (pulls === 1) {
                  controller.enqueue(new TextEncoder().encode(": connected\n\n"));
                  return;
                }
                waitingReaders += 1;
                if (waitingReaders === 2) Deferred.doneUnsafe(readersWaiting, Effect.void);
              },
              cancel() {
                cancelledDaemons.push(daemon);
              },
            }),
            { status: 200, headers: { "Content-Type": "text/event-stream" } },
          ),
        );
      };
      const apiLayer = makeGeneratedApiLayer(createRuntimeClient(fetchImpl));
      const workflow = yield* makeKataWorkflow;
      const callbacks = {
        checkpoint: 1,
        onOpen: Effect.void,
        onFrame: () => Effect.void,
        onError: () => Effect.void,
      };

      yield* workflow
        .connectEvents({ ...callbacks, owner: "workspace", daemonId: "home" })
        .pipe(Effect.provide(apiLayer));
      yield* workflow
        .connectEvents({ ...callbacks, owner: "auxiliary", daemonId: "docs" })
        .pipe(Effect.provide(apiLayer));
      yield* Deferred.await(readersWaiting);

      assert.deepStrictEqual(cancelledDaemons, []);
      yield* workflow.disconnectEvents("workspace");
      assert.deepStrictEqual(cancelledDaemons, ["home"]);
      yield* workflow.disconnectEvents("auxiliary");
      assert.deepStrictEqual(cancelledDaemons, ["home", "docs"]);
    }),
  ),
);
