import { assert, it } from "@effect/vitest";
import { Effect, Fiber } from "effect";
import { TestClock } from "effect/testing";

import { makeTmuxMouseDragAutoscroll } from "./tmuxMouseDragAutoscroll.js";

const leftDown = "\x1b[<0;10;5M";
const leftDrag = "\x1b[<32;10;5M";
const leftUp = "\x1b[<0;10;5m";
const bounds = {
  left: 100,
  right: 900,
  top: 200,
  bottom: 600,
  width: 800,
  height: 400,
};

it.effect("sends repeated wheel-up and edge-drag reports while a tmux drag is above the terminal", () =>
  Effect.scoped(
    Effect.gen(function* () {
      const sent: string[] = [];
      const autoscroll = yield* makeTmuxMouseDragAutoscroll({ send: (data) => sent.push(data) });
      autoscroll.observeTerminalData(leftDown + leftDrag);
      autoscroll.updatePointer({ clientX: 500, clientY: 180, bounds, cols: 80, rows: 24 });

      yield* TestClock.adjust("240 millis");

      assert.include(sent, "\x1b[<64;41;1M\x1b[<32;41;1M");
      assert.isAtLeast(sent.length, 3);
    }),
  ),
);

it.effect("uses wheel-down and the last row below the terminal", () =>
  Effect.scoped(
    Effect.gen(function* () {
      const sent: string[] = [];
      const autoscroll = yield* makeTmuxMouseDragAutoscroll({ send: (data) => sent.push(data) });
      autoscroll.observeTerminalData(leftDown);
      autoscroll.updatePointer({ clientX: 899, clientY: 620, bounds, cols: 80, rows: 24 });

      yield* TestClock.adjust("80 millis");

      assert.include(sent, "\x1b[<65;80;24M\x1b[<32;80;24M");
    }),
  ),
);

it.effect("stops when the pointer returns inside or tmux reports button release", () =>
  Effect.scoped(
    Effect.gen(function* () {
      const sent: string[] = [];
      const autoscroll = yield* makeTmuxMouseDragAutoscroll({ send: (data) => sent.push(data) });
      autoscroll.observeTerminalData(leftDown);
      autoscroll.updatePointer({ clientX: 500, clientY: 620, bounds, cols: 80, rows: 24 });
      yield* TestClock.adjust("80 millis");
      const afterFirstScroll = sent.length;

      autoscroll.updatePointer({ clientX: 500, clientY: 300, bounds, cols: 80, rows: 24 });
      yield* TestClock.adjust("240 millis");
      assert.strictEqual(sent.length, afterFirstScroll);

      autoscroll.updatePointer({ clientX: 500, clientY: 620, bounds, cols: 80, rows: 24 });
      autoscroll.observeTerminalData(leftUp);
      yield* TestClock.adjust("240 millis");
      assert.strictEqual(sent.length, afterFirstScroll);
    }),
  ),
);

it.effect("finalizes the tmux drag when the browser reports pointer release outside the terminal", () =>
  Effect.scoped(
    Effect.gen(function* () {
      const sent: string[] = [];
      const autoscroll = yield* makeTmuxMouseDragAutoscroll({ send: (data) => sent.push(data) });
      autoscroll.observeTerminalData(leftDown);
      autoscroll.updatePointer({ clientX: 500, clientY: 180, bounds, cols: 80, rows: 24 });
      yield* TestClock.adjust("80 millis");
      const afterFirstScroll = sent.length;

      autoscroll.endPointerGesture();
      yield* TestClock.adjust("240 millis");

      assert.strictEqual(sent.length, afterFirstScroll + 1);
      assert.strictEqual(sent.at(-1), "\x1b[<0;41;1m");
    }),
  ),
);

it.effect("ignores edge movement unless terminal output established a tmux left-button drag", () =>
  Effect.scoped(
    Effect.gen(function* () {
      const sent: string[] = [];
      const autoscroll = yield* makeTmuxMouseDragAutoscroll({ send: (data) => sent.push(data) });
      autoscroll.updatePointer({ clientX: 500, clientY: 620, bounds, cols: 80, rows: 24 });
      yield* TestClock.adjust("240 millis");
      autoscroll.observeTerminalData("\x1b[<64;10;5M");
      autoscroll.updatePointer({ clientX: 500, clientY: 620, bounds, cols: 80, rows: 24 });
      yield* TestClock.adjust("240 millis");

      assert.isEmpty(sent);
    }),
  ),
);

it.effect("scope interruption stops an active edge drag", () =>
  Effect.gen(function* () {
    const sent: string[] = [];
    const fiber = yield* Effect.forkChild(
      Effect.scoped(
        Effect.gen(function* () {
          const autoscroll = yield* makeTmuxMouseDragAutoscroll({ send: (data) => sent.push(data) });
          autoscroll.observeTerminalData(leftDown);
          autoscroll.updatePointer({ clientX: 500, clientY: 180, bounds, cols: 80, rows: 24 });
          return yield* Effect.never;
        }),
      ),
    );
    yield* Effect.yieldNow;
    yield* Fiber.interrupt(fiber);
    yield* TestClock.adjust("240 millis");

    assert.isEmpty(sent);
  }),
);

it.effect("resets an active edge drag without sending through a disconnected socket", () =>
  Effect.scoped(
    Effect.gen(function* () {
      const sent: string[] = [];
      const autoscroll = yield* makeTmuxMouseDragAutoscroll({ send: (data) => sent.push(data) });
      autoscroll.observeTerminalData(leftDown);
      autoscroll.updatePointer({ clientX: 500, clientY: 180, bounds, cols: 80, rows: 24 });
      autoscroll.reset();
      yield* TestClock.adjust("240 millis");

      assert.isEmpty(sent);
    }),
  ),
);
