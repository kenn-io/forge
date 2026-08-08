import { assert, it } from "@effect/vitest";
import { Effect, Fiber } from "effect";
import { TestClock } from "effect/testing";

import { transientClipboardFeedback } from "./clipboard-feedback.js";

it.effect("clears copied feedback when its owner interrupts the reset window", () =>
  Effect.gen(function* () {
    const states: boolean[] = [];
    const fiber = yield* Effect.forkChild(
      transientClipboardFeedback({
        text: "branch-name",
        write: () => Promise.resolve(true),
        isActive: () => true,
        onCopied: () => states.push(true),
        onExpired: () => states.push(false),
      }),
    );
    yield* TestClock.adjust("0 millis");
    assert.deepStrictEqual(states, [true]);

    yield* Fiber.interrupt(fiber);
    yield* TestClock.adjust("2 seconds");

    assert.deepStrictEqual(states, [true, false]);
  }),
);
