import { assert, it } from "@effect/vitest";
import { Effect, Fiber, Ref } from "effect";
import { TestClock } from "effect/testing";
import { makeSetupController, SetupEnvironmentLive } from "./setup-program.js";
import { SetupBrowserInvalidTest, SetupBrowserTest, SetupProbe } from "./setup-program.test-layer.js";

it.layer(SetupBrowserTest)("automatic setup submission", (it) => {
  it.effect("submits once after the visible countdown", () =>
    Effect.gen(function* () {
      const probe = yield* SetupProbe;
      const controller = makeSetupController({ onSecondsLeft: probe.recordSeconds });
      const fiber = yield* Effect.forkChild(Effect.scoped(controller.program));
      yield* Effect.yieldNow;

      yield* TestClock.adjust("2500 millis");
      yield* Effect.yieldNow;

      assert.strictEqual(yield* Ref.get(probe.submits), 1);
      assert.strictEqual(probe.seconds[0], 3);
      assert.isTrue(probe.seconds.includes(2));
      assert.isTrue(probe.seconds.includes(1));
      yield* Fiber.interrupt(fiber);
      yield* TestClock.adjust("10 seconds");
      assert.strictEqual(yield* Ref.get(probe.submits), 1);
    }),
  );
});

it.layer(SetupBrowserTest)("manual setup submission", (it) => {
  it.effect("routes repeated Continue clicks through the same submit guard", () =>
    Effect.gen(function* () {
      const probe = yield* SetupProbe;
      const controller = makeSetupController({ onSecondsLeft: probe.recordSeconds });
      const fiber = yield* Effect.forkChild(Effect.scoped(controller.program));
      yield* Effect.yieldNow;

      controller.continue();
      controller.continue();
      yield* Effect.yieldNow;

      assert.strictEqual(yield* Ref.get(probe.submits), 1);
      yield* TestClock.adjust("2500 millis");
      assert.strictEqual(yield* Ref.get(probe.submits), 1);
      yield* Fiber.interrupt(fiber);
    }),
  );
});

it.layer(SetupBrowserTest)("interrupted setup countdown", (it) => {
  it.effect("does not submit after interruption before the deadline", () =>
    Effect.gen(function* () {
      const probe = yield* SetupProbe;
      const controller = makeSetupController({ onSecondsLeft: probe.recordSeconds });
      const fiber = yield* Effect.forkChild(Effect.scoped(controller.program));
      yield* Effect.yieldNow;

      yield* TestClock.adjust("1 second");
      yield* Fiber.interrupt(fiber);
      yield* TestClock.adjust("10 seconds");

      assert.strictEqual(yield* Ref.get(probe.submits), 0);
    }),
  );
});

it.layer(SetupBrowserInvalidTest)("setup flow decoding", (it) => {
  it.effect("reports an invalid nested manifest without submitting", () =>
    Effect.gen(function* () {
      const probe = yield* SetupProbe;
      const controller = makeSetupController({ onFailure: probe.recordFailure });

      const exit = yield* Effect.exit(Effect.scoped(controller.program));

      assert.strictEqual(exit._tag, "Failure");
      assert.strictEqual(probe.failures[0]?._tag, "SetupInvalidPayload");
      assert.strictEqual(yield* Ref.get(probe.submits), 0);
    }),
  );
});

it.effect("aborts the flow request when setup is interrupted", () =>
  Effect.gen(function* () {
    const originalFetch = globalThis.fetch;
    let aborted = false;
    globalThis.fetch = (_input: RequestInfo | URL, init?: RequestInit) =>
      new Promise<Response>((_resolve, reject) => {
        init?.signal?.addEventListener(
          "abort",
          () => {
            aborted = true;
            reject(new DOMException("Aborted", "AbortError"));
          },
          { once: true },
        );
      });

    yield* Effect.gen(function* () {
      const controller = makeSetupController();
      const fiber = yield* Effect.forkChild(
        Effect.scoped(controller.program.pipe(Effect.provide(SetupEnvironmentLive))),
      );
      yield* Effect.yieldNow;

      yield* Fiber.interrupt(fiber);

      assert.isTrue(aborted);
    }).pipe(
      Effect.ensuring(
        Effect.sync(() => {
          globalThis.fetch = originalFetch;
        }),
      ),
    );
  }),
);
