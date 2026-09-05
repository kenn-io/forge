import { assert, it } from "@effect/vitest";
import { Deferred, Effect, Fiber, Queue, Ref, Stream } from "effect";
import { TestClock } from "effect/testing";

import { DocumentVisibility, makeManualDocumentVisibility } from "../../browser/document-visibility.js";
import {
  makeWorkspaceListWorkflow,
  makeWorkspaceRefreshHub,
  workspaceListLifecycle,
} from "./workspace-list-workflow.js";

it.effect("delivers refresh demand that arrives between component owners to the replacement", () =>
  Effect.scoped(
    Effect.gen(function* () {
      const workflow = yield* makeWorkspaceListWorkflow;
      let firstRefreshes = 0;
      let replacementRefreshes = 0;
      const firstOwner = yield* Effect.forkChild(
        Effect.scoped(
          workflow
            .claim("first", () => {
              firstRefreshes += 1;
            })
            .pipe(Effect.andThen(Effect.never)),
        ),
      );
      yield* Effect.yieldNow;
      yield* Fiber.interrupt(firstOwner);

      workflow.request();
      const replacementOwner = yield* Effect.forkChild(
        Effect.scoped(
          workflow
            .claim("replacement", () => {
              replacementRefreshes += 1;
            })
            .pipe(Effect.andThen(Effect.never)),
        ),
      );
      yield* Effect.yieldNow;

      assert.strictEqual(firstRefreshes, 0);
      assert.strictEqual(replacementRefreshes, 1);
      yield* Fiber.interrupt(replacementOwner);
    }),
  ),
);

it.effect("debounces each refresh burst without overlapping list loads", () =>
  Effect.gen(function* () {
    const started = yield* Ref.make(0);
    const active = yield* Ref.make(0);
    const maximumActive = yield* Ref.make(0);
    const firstRelease = yield* Deferred.make<void>();
    const secondRelease = yield* Deferred.make<void>();
    const hub = makeWorkspaceRefreshHub(
      Effect.gen(function* () {
        const attempt = yield* Ref.updateAndGet(started, (count) => count + 1);
        const activeCount = yield* Ref.updateAndGet(active, (count) => count + 1);
        yield* Ref.update(maximumActive, (maximum) => Math.max(maximum, activeCount));
        yield* Deferred.await(attempt === 1 ? firstRelease : secondRelease);
        yield* Ref.update(active, (count) => count - 1);
      }),
    );
    const fiber = yield* Effect.forkChild(Effect.scoped(hub.program));
    yield* Effect.yieldNow;

    hub.request();
    hub.request();
    hub.request();
    yield* Effect.yieldNow;
    assert.strictEqual(yield* Ref.get(started), 0);
    yield* TestClock.adjust("24 millis");
    assert.strictEqual(yield* Ref.get(started), 0);
    yield* TestClock.adjust("1 millis");
    assert.strictEqual(yield* Ref.get(started), 1);

    hub.request();
    hub.request();
    yield* Deferred.succeed(firstRelease, undefined);
    yield* Effect.yieldNow;
    assert.strictEqual(yield* Ref.get(started), 1);
    yield* TestClock.adjust("25 millis");
    assert.strictEqual(yield* Ref.get(started), 2);
    assert.strictEqual(yield* Ref.get(maximumActive), 1);

    yield* Deferred.succeed(secondRelease, undefined);
    yield* Fiber.interrupt(fiber);
  }),
);

it.effect("runs scheduled and event refreshes through the same hubs", () =>
  Effect.gen(function* () {
    const workspaceLoads = yield* Ref.make(0);
    const fleetLoads = yield* Ref.make(0);
    const events = yield* Queue.unbounded<void>();
    const refreshWorkspaces = makeWorkspaceRefreshHub(Ref.update(workspaceLoads, (count) => count + 1));
    const refreshFleet = makeWorkspaceRefreshHub(Ref.update(fleetLoads, (count) => count + 1));
    const fiber = yield* Effect.forkChild(
      Effect.scoped(
        workspaceListLifecycle({
          refreshWorkspaces,
          refreshFleet,
          workspaceEvents: Stream.fromQueue(events),
        }),
      ),
    );
    yield* Effect.yieldNow;

    assert.strictEqual(yield* Ref.get(workspaceLoads), 1);
    assert.strictEqual(yield* Ref.get(fleetLoads), 1);

    yield* Queue.offer(events, undefined);
    yield* Effect.repeat(Effect.yieldNow, { times: 5 });
    yield* TestClock.adjust("25 millis");
    assert.strictEqual(yield* Ref.get(workspaceLoads), 2);

    yield* TestClock.adjust("15 seconds");
    assert.strictEqual(yield* Ref.get(workspaceLoads), 5);
    assert.strictEqual(yield* Ref.get(fleetLoads), 2);

    yield* Fiber.interrupt(fiber);
  }),
);

it.effect("pauses timer polls while the document is hidden but still refreshes on workspace events", () =>
  Effect.gen(function* () {
    const visibility = makeManualDocumentVisibility();
    const workspaceLoads = yield* Ref.make(0);
    const fleetLoads = yield* Ref.make(0);
    const events = yield* Queue.unbounded<void>();
    const refreshWorkspaces = makeWorkspaceRefreshHub(Ref.update(workspaceLoads, (count) => count + 1));
    const refreshFleet = makeWorkspaceRefreshHub(Ref.update(fleetLoads, (count) => count + 1));
    const fiber = yield* Effect.forkChild(
      Effect.scoped(
        workspaceListLifecycle({
          refreshWorkspaces,
          refreshFleet,
          workspaceEvents: Stream.fromQueue(events),
        }),
      ).pipe(Effect.provideService(DocumentVisibility, visibility)),
    );
    yield* Effect.yieldNow;
    assert.strictEqual(yield* Ref.get(workspaceLoads), 1);
    assert.strictEqual(yield* Ref.get(fleetLoads), 1);

    visibility.hide();
    yield* Effect.yieldNow;
    yield* TestClock.adjust("1 minute");
    assert.strictEqual(yield* Ref.get(workspaceLoads), 1, "hidden documents must not poll the workspace list");
    assert.strictEqual(yield* Ref.get(fleetLoads), 1, "hidden documents must not poll the fleet snapshot");

    yield* Queue.offer(events, undefined);
    yield* Effect.repeat(Effect.yieldNow, { times: 5 });
    yield* TestClock.adjust("25 millis");
    assert.strictEqual(yield* Ref.get(workspaceLoads), 2, "SSE-driven refreshes keep working while hidden");

    visibility.show();
    yield* Effect.repeat(Effect.yieldNow, { times: 5 });
    yield* TestClock.adjust("25 millis");
    assert.strictEqual(yield* Ref.get(workspaceLoads), 3, "becoming visible refreshes the workspace list at once");
    assert.strictEqual(yield* Ref.get(fleetLoads), 2, "becoming visible refreshes the fleet snapshot at once");

    yield* Fiber.interrupt(fiber);
  }),
);
