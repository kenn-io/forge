import { assert, it } from "@effect/vitest";
import { Effect, Fiber, Ref } from "effect";
import { TestClock } from "effect/testing";

import { DocumentVisibility, makeManualDocumentVisibility } from "../browser/document-visibility.js";
import { pollWhileVisible } from "./poll-while-visible.js";

it.effect("polls on the interval while visible and runs nothing while hidden", () =>
  Effect.gen(function* () {
    const visibility = makeManualDocumentVisibility();
    const polls = yield* Ref.make(0);
    const fiber = yield* Effect.forkChild(
      pollWhileVisible(
        Ref.update(polls, (count) => count + 1),
        "5 seconds",
      ).pipe(Effect.provideService(DocumentVisibility, visibility)),
    );
    yield* Effect.yieldNow;
    assert.strictEqual(yield* Ref.get(polls), 0);

    yield* TestClock.adjust("10 seconds");
    assert.strictEqual(yield* Ref.get(polls), 2);

    visibility.hide();
    yield* TestClock.adjust("1 minute");
    assert.strictEqual(yield* Ref.get(polls), 2, "a hidden document must not poll");

    yield* Fiber.interrupt(fiber);
  }),
);

it.effect("refreshes immediately when the document becomes visible and restarts the interval", () =>
  Effect.gen(function* () {
    const visibility = makeManualDocumentVisibility();
    const polls = yield* Ref.make(0);
    const fiber = yield* Effect.forkChild(
      pollWhileVisible(
        Ref.update(polls, (count) => count + 1),
        "5 seconds",
      ).pipe(Effect.provideService(DocumentVisibility, visibility)),
    );
    yield* Effect.yieldNow;
    yield* TestClock.adjust("3 seconds");
    visibility.hide();
    yield* Effect.yieldNow;
    yield* TestClock.adjust("3 seconds");
    assert.strictEqual(yield* Ref.get(polls), 0, "the tick due during the hidden span must not fire");

    visibility.show();
    yield* Effect.yieldNow;
    assert.strictEqual(yield* Ref.get(polls), 1, "becoming visible refreshes at once");

    yield* TestClock.adjust("4 seconds");
    assert.strictEqual(yield* Ref.get(polls), 1, "the interval restarts from the visible refresh");
    yield* TestClock.adjust("1 seconds");
    assert.strictEqual(yield* Ref.get(polls), 2);

    yield* Fiber.interrupt(fiber);
  }),
);

it.effect("starts hidden without polling until the document is shown", () =>
  Effect.gen(function* () {
    const visibility = makeManualDocumentVisibility(false);
    const polls = yield* Ref.make(0);
    const fiber = yield* Effect.forkChild(
      pollWhileVisible(
        Ref.update(polls, (count) => count + 1),
        "5 seconds",
        { immediate: true },
      ).pipe(Effect.provideService(DocumentVisibility, visibility)),
    );
    yield* Effect.yieldNow;
    yield* TestClock.adjust("30 seconds");
    assert.strictEqual(yield* Ref.get(polls), 0);

    visibility.show();
    yield* Effect.yieldNow;
    assert.strictEqual(yield* Ref.get(polls), 1);

    yield* Fiber.interrupt(fiber);
  }),
);

it.effect("runs an immediate poll first when requested and never overlaps polls", () =>
  Effect.gen(function* () {
    const visibility = makeManualDocumentVisibility();
    const active = yield* Ref.make(0);
    const maximumActive = yield* Ref.make(0);
    const polls = yield* Ref.make(0);
    const pollOnce = Effect.gen(function* () {
      const count = yield* Ref.updateAndGet(active, (value) => value + 1);
      yield* Ref.update(maximumActive, (maximum) => Math.max(maximum, count));
      yield* Effect.sleep("2 seconds");
      yield* Ref.update(active, (value) => value - 1);
      yield* Ref.update(polls, (value) => value + 1);
    });
    const fiber = yield* Effect.forkChild(
      pollWhileVisible(pollOnce, "1 second", { immediate: true }).pipe(
        Effect.provideService(DocumentVisibility, visibility),
      ),
    );
    yield* Effect.yieldNow;
    yield* TestClock.adjust("2 seconds");
    assert.strictEqual(yield* Ref.get(polls), 1, "the immediate poll completes after its own duration");

    yield* TestClock.adjust("9 seconds");
    assert.strictEqual(yield* Ref.get(maximumActive), 1);
    assert.strictEqual(yield* Ref.get(polls), 4);

    yield* Fiber.interrupt(fiber);
  }),
);

it.effect("still refreshes immediately when the document is hidden and shown before the poller resumes", () =>
  Effect.gen(function* () {
    const visibility = makeManualDocumentVisibility();
    const polls = yield* Ref.make(0);
    const fiber = yield* Effect.forkChild(
      pollWhileVisible(
        Ref.update(polls, (count) => count + 1),
        "5 seconds",
      ).pipe(Effect.provideService(DocumentVisibility, visibility)),
    );
    yield* Effect.yieldNow;
    yield* TestClock.adjust("3 seconds");

    // No scheduler yield between the two transitions: the poller observes
    // "hidden" only after the document is already visible again.
    visibility.hide();
    visibility.show();
    yield* Effect.repeat(Effect.yieldNow, { times: 3 });
    assert.strictEqual(yield* Ref.get(polls), 1, "the visible transition still owes an immediate poll");

    yield* TestClock.adjust("5 seconds");
    assert.strictEqual(yield* Ref.get(polls), 2);

    yield* Fiber.interrupt(fiber);
  }),
);
