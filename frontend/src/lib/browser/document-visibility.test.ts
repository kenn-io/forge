import { assert, it } from "@effect/vitest";
import { Effect, Fiber, Ref } from "effect";
import { TestClock } from "effect/testing";
import { afterEach, beforeEach, vi } from "vitest";

import { DocumentVisibility } from "./document-visibility.js";
import { pollWhileVisible } from "../effect/poll-while-visible.js";

// These tests drive the real document boundary under jsdom: they change
// document.visibilityState and dispatch visibilitychange instead of using the
// manual test source.
let visibilityState: DocumentVisibilityState = "visible";
const setVisibility = (state: DocumentVisibilityState) => {
  visibilityState = state;
  document.dispatchEvent(new Event("visibilitychange"));
};

beforeEach(() => {
  visibilityState = "visible";
  Object.defineProperty(document, "visibilityState", {
    configurable: true,
    get: () => visibilityState,
  });
});

afterEach(() => {
  vi.restoreAllMocks();
});

it.effect("waits on the real visibilitychange event and detaches every listener it added", () =>
  Effect.gen(function* () {
    const added = vi.spyOn(document, "addEventListener");
    const removed = vi.spyOn(document, "removeEventListener");
    const visibility = yield* DocumentVisibility;
    assert.isTrue(visibility.visible());

    const waiter = yield* Effect.forkChild(visibility.untilHidden);
    yield* Effect.yieldNow;
    assert.strictEqual(added.mock.calls.length, 1);
    assert.strictEqual(removed.mock.calls.length, 0);

    setVisibility("hidden");
    yield* Fiber.join(waiter);
    assert.isFalse(visibility.visible());
    assert.strictEqual(removed.mock.calls.length, 1, "a completed waiter removes its own listener");

    const interrupted = yield* Effect.forkChild(visibility.untilVisible);
    yield* Effect.yieldNow;
    yield* Fiber.interrupt(interrupted);
    assert.strictEqual(removed.mock.calls.length, 2, "an interrupted waiter removes its listener too");
  }),
);

it.effect("pauses a poll while the real document is hidden and resumes on visibilitychange", () =>
  Effect.gen(function* () {
    const polls = yield* Ref.make(0);
    const fiber = yield* Effect.forkChild(
      pollWhileVisible(
        Ref.update(polls, (count) => count + 1),
        "5 seconds",
      ),
    );
    yield* Effect.yieldNow;
    yield* TestClock.adjust("5 seconds");
    assert.strictEqual(yield* Ref.get(polls), 1);

    setVisibility("hidden");
    yield* Effect.yieldNow;
    yield* TestClock.adjust("30 seconds");
    assert.strictEqual(yield* Ref.get(polls), 1);

    setVisibility("visible");
    yield* Effect.repeat(Effect.yieldNow, { times: 3 });
    assert.strictEqual(yield* Ref.get(polls), 2);

    yield* Fiber.interrupt(fiber);
  }),
);
