import { assert, it } from "@effect/vitest";
import { Effect, Fiber } from "effect";
import { TestClock } from "effect/testing";
import { afterEach, vi } from "vite-plus/test";
import { makeGeneratedApiLayer } from "../api/generated-api.js";
import * as client from "../api/generated/index.js";
import { reportAppOpened } from "./app-opened.js";

interface RecordedRequest {
  readonly method: string;
  readonly pathname: string;
  readonly contentType: string | null;
  readonly body: string;
}

const lastMinuteOfDay = Date.UTC(2026, 9, 1, 23, 59);
const apiLayer = makeGeneratedApiLayer(client);

// Real timers let the stubbed fetch promises and fiber hand-offs settle.
const tick = Effect.promise(() => new Promise<void>((resolve) => setTimeout(resolve, 0)));
const settle = tick.pipe(Effect.andThen(tick), Effect.andThen(tick));

const focusWindow = Effect.sync(() => window.dispatchEvent(new Event("focus"))).pipe(Effect.andThen(settle));

function recordFetch(respond: (index: number) => Promise<Response> = () => accepted()): RecordedRequest[] {
  const requests: RecordedRequest[] = [];
  vi.stubGlobal("fetch", (async (input: RequestInfo | URL, init?: RequestInit) => {
    const request = new Request(input, init);
    requests.push({
      method: request.method,
      pathname: new URL(request.url).pathname,
      contentType: request.headers.get("Content-Type"),
      body: await request.text(),
    });
    return respond(requests.length - 1);
  }) satisfies typeof fetch);
  return requests;
}

const accepted = () => Promise.resolve(Response.json({ status: "queued" }, { status: 202 }));

const startReporter = Effect.gen(function* () {
  yield* TestClock.setTime(lastMinuteOfDay);
  const fiber = yield* Effect.forkChild(reportAppOpened.pipe(Effect.provide(apiLayer)));
  yield* settle;
  return fiber;
});

afterEach(() => {
  vi.unstubAllGlobals();
});

it.effect("posts app_opened once on load", () =>
  Effect.gen(function* () {
    const requests = recordFetch();
    const fiber = yield* startReporter;

    assert.deepStrictEqual(requests, [
      {
        method: "POST",
        pathname: "/api/v1/telemetry/events",
        contentType: "application/json",
        body: '{"event":"app_opened"}',
      },
    ]);
    yield* Fiber.interrupt(fiber);
  }),
);

it.effect("sends nothing more on focus the same UTC day", () =>
  Effect.gen(function* () {
    const requests = recordFetch();
    yield* TestClock.setTime(Date.UTC(2026, 9, 1, 0, 30));
    const fiber = yield* Effect.forkChild(reportAppOpened.pipe(Effect.provide(apiLayer)));
    yield* settle;
    yield* TestClock.adjust("23 hours");
    yield* focusWindow;
    yield* focusWindow;

    assert.strictEqual(requests.length, 1);
    yield* Fiber.interrupt(fiber);
  }),
);

it.effect("posts once on the first focus after UTC midnight", () =>
  Effect.gen(function* () {
    const requests = recordFetch();
    const fiber = yield* startReporter;
    yield* TestClock.adjust("2 minutes");
    yield* focusWindow;
    assert.strictEqual(requests.length, 2);

    yield* focusWindow;
    assert.strictEqual(requests.length, 2);
    yield* Fiber.interrupt(fiber);
  }),
);

it.effect("swallows a failed send and still reports the next day", () =>
  Effect.gen(function* () {
    const requests = recordFetch((index) =>
      index === 0 ? Promise.reject(new TypeError("Failed to fetch")) : accepted(),
    );
    const fiber = yield* startReporter;
    assert.strictEqual(requests.length, 1);
    assert.isUndefined(fiber.pollUnsafe());

    yield* TestClock.adjust("2 minutes");
    yield* focusWindow;
    assert.strictEqual(requests.length, 2);
    yield* Fiber.interrupt(fiber);
  }),
);

it.effect("stops reporting once interrupted", () =>
  Effect.gen(function* () {
    const requests = recordFetch();
    const fiber = yield* startReporter;
    yield* Fiber.interrupt(fiber);

    yield* TestClock.adjust("2 minutes");
    yield* focusWindow;
    assert.strictEqual(requests.length, 1);
  }),
);

it.effect("reports the next day while the load request is still pending", () =>
  Effect.gen(function* () {
    const pending = Promise.withResolvers<Response>();
    const requests = recordFetch((index) => (index === 0 ? pending.promise : accepted()));
    const fiber = yield* startReporter;
    assert.strictEqual(requests.length, 1);

    yield* TestClock.adjust("2 minutes");
    yield* focusWindow;
    assert.strictEqual(requests.length, 2);

    pending.resolve(Response.json({ status: "queued" }, { status: 202 }));
    yield* Fiber.interrupt(fiber);
  }),
);
