import { assert, it } from "@effect/vitest";
import { Context, Deferred, Effect, Fiber, Layer, Queue, Ref } from "effect";
import { TestClock } from "effect/testing";
import { ApiProblemError } from "../api/effect-errors.js";
import { EventSourceFactory, type EventSourceLike } from "../browser/event-source.js";
import {
  ProviderEventsCheckpointLive,
  providerEventsProgram,
  type ProviderEvent,
  type ProviderEventsConnectionState,
} from "./provider-events-workflow.js";

class TestEventSource extends EventTarget implements EventSourceLike {
  readonly url: string;
  closed = false;

  constructor(url: string) {
    super();
    this.url = url;
  }

  close(): void {
    this.closed = true;
  }
}

class ProviderEventsProbe extends Context.Service<
  ProviderEventsProbe,
  {
    readonly awaitSource: Effect.Effect<TestEventSource>;
    readonly opened: Queue.Queue<TestEventSource>;
    readonly sourceCount: Effect.Effect<number>;
    readonly sources: Array<TestEventSource>;
  }
>()("kenn-forge/testing/ProviderEventsProbe") {}

const probeLayer = Layer.effect(
  ProviderEventsProbe,
  Effect.gen(function* () {
    const opened = yield* Queue.unbounded<TestEventSource>();
    const sources: Array<TestEventSource> = [];
    return {
      awaitSource: Queue.take(opened),
      sourceCount: Effect.sync(() => sources.length),
      sources,
      opened,
    };
  }),
);

const factoryLayer = Layer.effect(
  EventSourceFactory,
  Effect.gen(function* () {
    const probe = yield* ProviderEventsProbe;
    return {
      open: (url: string) =>
        Effect.gen(function* () {
          const source = new TestEventSource(url);
          probe.sources.push(source);
          yield* Queue.offer(probe.opened, source);
          return source;
        }),
    };
  }),
);

const ProviderEventsTest = Layer.mergeAll(Layer.provideMerge(factoryLayer, probeLayer), ProviderEventsCheckpointLive);

function emitOpen(source: TestEventSource): void {
  source.dispatchEvent(new Event("open"));
}

function emitFrame(source: TestEventSource, type: string, data: unknown, lastEventId: string): void {
  source.dispatchEvent(
    new MessageEvent(type, {
      data: JSON.stringify(data),
      lastEventId,
    }),
  );
}

function emitError(source: TestEventSource): void {
  source.dispatchEvent(new Event("error"));
}

it.layer(ProviderEventsTest)("provider event checkpoint resume", (it) => {
  it.effect("reconnects from the last accepted event id and exposes the reconnecting state", () =>
    Effect.gen(function* () {
      const probe = yield* ProviderEventsProbe;
      const states: Array<ProviderEventsConnectionState> = [];
      const events: Array<ProviderEvent> = [];
      const fiber = yield* Effect.forkChild(
        providerEventsProgram({
          url: "/api/v1/events",
          onState: (state) => states.push(state),
          onEvent: (event) =>
            Effect.sync(() => {
              events.push(event);
            }),
        }),
      );
      const first = yield* probe.awaitSource;

      emitOpen(first);
      emitFrame(first, "data_changed", {}, "7");
      emitError(first);
      yield* Effect.yieldNow;

      assert.deepStrictEqual(states, ["connecting", "connected", "reconnecting"]);
      assert.strictEqual(events.length, 1);
      assert.isTrue(first.closed);

      yield* TestClock.adjust("1 second");
      const second = yield* probe.awaitSource;

      assert.strictEqual(second.url, "/api/v1/events?since=7");
      yield* Fiber.interrupt(fiber);
    }),
  );
});

it.layer(ProviderEventsTest)("workspace lifecycle events", (it) => {
  it.effect("decodes workspace creation and status events", () =>
    Effect.gen(function* () {
      const probe = yield* ProviderEventsProbe;
      const events = yield* Queue.unbounded<ProviderEvent>();
      const fiber = yield* Effect.forkChild(
        providerEventsProgram({
          url: "/api/v1/events",
          onEvent: (event) => Queue.offer(events, event),
        }),
      );
      const source = yield* probe.awaitSource;
      emitOpen(source);

      emitFrame(source, "workspace_created", { id: "ws-1", created: false }, "1");
      emitFrame(source, "workspace_status", { id: "ws-1", status: "ready" }, "2");

      const created = yield* Queue.take(events);
      assert.strictEqual(created.type, "workspace_created");
      if (created.type === "workspace_created") {
        assert.strictEqual(created.payload.id, "ws-1");
        assert.isFalse(created.payload.created);
      }
      const status = yield* Queue.take(events);
      assert.strictEqual(status.type, "workspace_status");
      if (status.type === "workspace_status") {
        assert.strictEqual(status.payload.id, "ws-1");
        assert.strictEqual(status.payload.status, "ready");
      }
      yield* Fiber.interrupt(fiber);
    }),
  );
});

it.layer(ProviderEventsTest)("provider event acknowledgement", (it) => {
  it.effect("advances the checkpoint only after event handling succeeds", () =>
    Effect.gen(function* () {
      const probe = yield* ProviderEventsProbe;
      const accepted = yield* Deferred.make<void>();
      const fiber = yield* Effect.forkChild(
        providerEventsProgram({
          url: "/api/v1/events",
          onEvent: () => Deferred.await(accepted),
        }),
      );
      const first = yield* probe.awaitSource;

      emitOpen(first);
      emitFrame(first, "data_changed", {}, "11");
      emitError(first);
      yield* TestClock.adjust("1 second");
      assert.strictEqual(yield* probe.sourceCount, 1);

      yield* Deferred.succeed(accepted, undefined);
      yield* TestClock.adjust("1 second");
      const second = yield* probe.awaitSource;
      assert.strictEqual(second.url, "/api/v1/events?since=11");
      yield* Fiber.interrupt(fiber);
    }),
  );

  it.effect("reconnects from the last buffered checkpoint when callback pressure overflows", () =>
    Effect.gen(function* () {
      const probe = yield* ProviderEventsProbe;
      const firstHandling = yield* Deferred.make<void>();
      const releaseFirst = yield* Deferred.make<void>();
      let handled = 0;
      const fiber = yield* Effect.forkChild(
        providerEventsProgram({
          url: "/api/v1/events",
          onEvent: () => {
            handled++;
            return handled === 1
              ? Deferred.succeed(firstHandling, undefined).pipe(Effect.andThen(Deferred.await(releaseFirst)))
              : Effect.void;
          },
        }),
      );
      const first = yield* probe.awaitSource;
      emitOpen(first);
      emitFrame(first, "data_changed", {}, "1");
      yield* Deferred.await(firstHandling);
      for (let id = 2; id <= 66; id++) emitFrame(first, "data_changed", {}, String(id));

      yield* Deferred.succeed(releaseFirst, undefined);
      yield* Effect.yieldNow;
      yield* TestClock.adjust("1 second");
      const second = yield* probe.awaitSource;

      assert.strictEqual(handled, 65);
      assert.strictEqual(second.url, "/api/v1/events?since=65");
      yield* Fiber.interrupt(fiber);
    }),
  );
});

it.layer(ProviderEventsTest)("provider event reconnect reset", (it) => {
  it.effect("resets reconnect delay after a connection opens", () =>
    Effect.gen(function* () {
      const probe = yield* ProviderEventsProbe;
      const connected = yield* Deferred.make<void>();
      const fiber = yield* Effect.forkChild(
        providerEventsProgram({
          url: "/api/v1/events",
          onState: (state) => {
            if (state === "connected") Effect.runSync(Deferred.succeed(connected, undefined));
          },
        }),
      );
      const first = yield* probe.awaitSource;

      emitError(first);
      yield* TestClock.adjust("1 second");
      const second = yield* probe.awaitSource;
      emitError(second);
      yield* TestClock.adjust("2 seconds");
      const third = yield* probe.awaitSource;
      emitOpen(third);
      yield* Deferred.await(connected);
      emitError(third);
      yield* Effect.yieldNow;
      yield* TestClock.adjust("1 second");
      yield* Effect.yieldNow;

      assert.strictEqual(yield* probe.sourceCount, 4);
      yield* Fiber.interrupt(fiber);
    }),
  );
});

it.layer(ProviderEventsTest)("provider event checkpoint monotonicity", (it) => {
  it.effect("does not let an older overlapping owner move the shared checkpoint backward", () =>
    Effect.gen(function* () {
      const probe = yield* ProviderEventsProbe;
      const firstHandling = yield* Deferred.make<void>();
      const releaseFirst = yield* Deferred.make<void>();
      const firstFiber = yield* Effect.forkChild(
        providerEventsProgram({
          url: "/api/v1/events",
          onEvent: () => Deferred.succeed(firstHandling, undefined).pipe(Effect.andThen(Deferred.await(releaseFirst))),
        }),
      );
      const first = yield* probe.awaitSource;
      emitOpen(first);
      emitFrame(first, "data_changed", {}, "10");
      yield* Deferred.await(firstHandling);

      const secondAccepted = yield* Deferred.make<void>();
      const secondFiber = yield* Effect.forkChild(
        providerEventsProgram({
          url: "/api/v1/events",
          onEvent: () => Deferred.succeed(secondAccepted, undefined),
        }),
      );
      const second = yield* probe.awaitSource;
      emitOpen(second);
      emitFrame(second, "data_changed", {}, "20");
      yield* Deferred.await(secondAccepted);
      yield* Effect.yieldNow;

      yield* Deferred.succeed(releaseFirst, undefined);
      yield* Effect.yieldNow;
      yield* Fiber.interrupt(firstFiber);
      yield* Fiber.interrupt(secondFiber);

      const replacement = yield* Effect.forkChild(providerEventsProgram({ url: "/api/v1/events" }));
      const third = yield* probe.awaitSource;
      assert.strictEqual(third.url, "/api/v1/events?since=20");
      yield* Fiber.interrupt(replacement);
    }),
  );

  it.effect("lets a reconciled stale frame establish a lower server epoch", () =>
    Effect.gen(function* () {
      const probe = yield* ProviderEventsProbe;
      const firstAccepted = yield* Deferred.make<void>();
      const firstFiber = yield* Effect.forkChild(
        providerEventsProgram({
          url: "/api/v1/events",
          onEvent: () => Deferred.succeed(firstAccepted, undefined),
        }),
      );
      const first = yield* probe.awaitSource;
      emitOpen(first);
      emitFrame(first, "data_changed", {}, "100");
      yield* Deferred.await(firstAccepted);
      yield* Effect.yieldNow;
      yield* Fiber.interrupt(firstFiber);

      const staleAccepted = yield* Deferred.make<void>();
      const secondFiber = yield* Effect.forkChild(
        providerEventsProgram({
          url: "/api/v1/events",
          onEvent: () => Deferred.succeed(staleAccepted, undefined),
        }),
      );
      const second = yield* probe.awaitSource;
      assert.strictEqual(second.url, "/api/v1/events?since=100");
      emitOpen(second);
      emitFrame(second, "reconnect.stale", {}, "1");
      yield* Deferred.await(staleAccepted);
      yield* Effect.yieldNow;
      yield* Fiber.interrupt(secondFiber);

      const thirdFiber = yield* Effect.forkChild(providerEventsProgram({ url: "/api/v1/events" }));
      const third = yield* probe.awaitSource;
      assert.strictEqual(third.url, "/api/v1/events?since=1");
      yield* Fiber.interrupt(thirdFiber);
    }),
  );

  it.effect("interrupts an older owner before it can project after a replacement", () =>
    Effect.gen(function* () {
      const probe = yield* ProviderEventsProbe;
      const visible = yield* Ref.make("initial");
      const firstStarted = yield* Deferred.make<void>();
      const releaseFirst = yield* Deferred.make<void>();
      const firstFiber = yield* Effect.forkChild(
        providerEventsProgram({
          url: "/api/v1/events",
          onEvent: () =>
            Deferred.succeed(firstStarted, undefined).pipe(
              Effect.andThen(Deferred.await(releaseFirst)),
              Effect.andThen(Ref.set(visible, "old")),
            ),
        }),
      );
      const first = yield* probe.awaitSource;
      emitOpen(first);
      emitFrame(first, "data_changed", {}, "10");
      yield* Deferred.await(firstStarted);

      const secondAccepted = yield* Deferred.make<void>();
      const secondFiber = yield* Effect.forkChild(
        providerEventsProgram({
          url: "/api/v1/events",
          onEvent: () => Ref.set(visible, "new").pipe(Effect.andThen(Deferred.succeed(secondAccepted, undefined))),
        }),
      );
      const second = yield* probe.awaitSource;
      emitOpen(second);
      emitFrame(second, "data_changed", {}, "20");
      yield* Deferred.await(secondAccepted);
      yield* Deferred.succeed(releaseFirst, undefined);
      yield* Effect.yieldNow;

      assert.strictEqual(yield* Ref.get(visible), "new");
      assert.isTrue(first.closed);
      yield* Fiber.interrupt(firstFiber);
      yield* Fiber.interrupt(secondFiber);
    }),
  );
});

it.layer(ProviderEventsTest)("provider event consequence recovery", (it) => {
  it.effect("replays a transient API consequence before advancing its checkpoint", () =>
    Effect.gen(function* () {
      const probe = yield* ProviderEventsProbe;
      const attempts = yield* Ref.make(0);
      const replayAccepted = yield* Deferred.make<void>();
      const fiber = yield* Effect.forkChild(
        providerEventsProgram({
          url: "/api/v1/events",
          onEvent: () =>
            Ref.updateAndGet(attempts, (count) => count + 1).pipe(
              Effect.flatMap((attempt) =>
                attempt <= 3
                  ? Effect.fail(
                      new ApiProblemError({
                        operation: "refresh pull request after provider event",
                        problem: {
                          code: "upstreamError",
                          detail: "provider is temporarily unavailable",
                          title: "Upstream unavailable",
                          type: "about:blank",
                        },
                      }),
                    )
                  : Deferred.succeed(replayAccepted, undefined),
              ),
            ),
        }),
      );
      const first = yield* probe.awaitSource;
      emitOpen(first);
      emitFrame(first, "data_changed", {}, "7");
      yield* TestClock.adjust("1 minute");

      const replay = yield* probe.awaitSource;
      assert.isTrue(first.closed);
      assert.strictEqual(replay.url, "/api/v1/events");
      emitOpen(replay);
      emitFrame(replay, "data_changed", {}, "7");
      yield* Deferred.await(replayAccepted);
      yield* Effect.yieldNow;
      yield* Fiber.interrupt(fiber);

      const replacement = yield* Effect.forkChild(providerEventsProgram({ url: "/api/v1/events" }));
      const resumed = yield* probe.awaitSource;
      assert.strictEqual(resumed.url, "/api/v1/events?since=7");
      yield* Fiber.interrupt(replacement);
    }),
  );

  it.effect("quarantines a permanent API consequence failure and continues with later events", () =>
    Effect.gen(function* () {
      const probe = yield* ProviderEventsProbe;
      const handled = yield* Ref.make(0);
      const nextAccepted = yield* Deferred.make<void>();
      const fiber = yield* Effect.forkChild(
        providerEventsProgram({
          url: "/api/v1/events",
          onEvent: () =>
            Ref.getAndUpdate(handled, (count) => count + 1).pipe(
              Effect.flatMap((count) =>
                count === 0
                  ? Effect.fail(
                      new ApiProblemError({
                        operation: "refresh removed pull request",
                        problem: {
                          code: "pullNotFound",
                          detail: "pull request no longer exists",
                          title: "Not found",
                          type: "about:blank",
                        },
                      }),
                    )
                  : Deferred.succeed(nextAccepted, undefined),
              ),
            ),
        }),
      );
      const source = yield* probe.awaitSource;
      emitOpen(source);
      emitFrame(source, "data_changed", {}, "7");
      yield* Effect.yieldNow;
      emitFrame(source, "data_changed", {}, "8");
      yield* Deferred.await(nextAccepted);
      yield* Effect.yieldNow;

      assert.isFalse(source.closed);
      yield* Fiber.interrupt(fiber);
      const replacement = yield* Effect.forkChild(providerEventsProgram({ url: "/api/v1/events" }));
      const resumed = yield* probe.awaitSource;
      assert.strictEqual(resumed.url, "/api/v1/events?since=8");
      yield* Fiber.interrupt(replacement);
    }),
  );
});

it.layer(ProviderEventsTest)("provider event invalid payload", (it) => {
  it.effect("stops reconnecting and exposes disconnected state for a non-transient decode failure", () =>
    Effect.gen(function* () {
      const probe = yield* ProviderEventsProbe;
      const states: Array<ProviderEventsConnectionState> = [];
      const fiber = yield* Effect.forkChild(
        providerEventsProgram({
          url: "/api/v1/events",
          onState: (state) => states.push(state),
        }),
      );
      const source = yield* probe.awaitSource;
      emitOpen(source);

      emitFrame(source, "sync_status", { running: "yes" }, "8");
      const failure = yield* Fiber.join(fiber).pipe(Effect.flip);

      assert.strictEqual(failure._tag, "InvalidExternalPayload");
      assert.strictEqual(states.at(-1), "disconnected");
      assert.isTrue(source.closed);
      yield* TestClock.adjust("1 minute");
      assert.strictEqual(yield* probe.sourceCount, 1);
    }),
  );
});

it.layer(ProviderEventsTest)("provider event open-source interruption", (it) => {
  it.effect("closes an open source when its owner is interrupted", () =>
    Effect.gen(function* () {
      const probe = yield* ProviderEventsProbe;
      const fiber = yield* Effect.forkChild(providerEventsProgram({ url: "/api/v1/events" }));
      const source = yield* probe.awaitSource;
      emitOpen(source);

      yield* Fiber.interrupt(fiber);

      assert.isTrue(source.closed);
    }),
  );
});

it.layer(ProviderEventsTest)("provider event reconnect interruption", (it) => {
  it.effect("cancels a reconnect delay when its owner is interrupted", () =>
    Effect.gen(function* () {
      const probe = yield* ProviderEventsProbe;
      const fiber = yield* Effect.forkChild(providerEventsProgram({ url: "/api/v1/events" }));
      const source = yield* probe.awaitSource;
      emitOpen(source);
      emitError(source);
      yield* Effect.yieldNow;
      assert.isTrue(source.closed);

      yield* Fiber.interrupt(fiber);
      yield* TestClock.adjust("1 minute");

      assert.strictEqual(yield* probe.sourceCount, 1);
    }),
  );
});
