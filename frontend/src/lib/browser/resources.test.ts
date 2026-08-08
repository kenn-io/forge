import { assert, it } from "@effect/vitest";
import { Deferred, Effect, Fiber, Stream } from "effect";
import { eventSourceStream, openEventSource } from "./event-source.js";
import { byteStreamFromReader, type ByteReader } from "./streaming-fetch.js";
import { observeResize } from "./observers.js";
import { makeWebSocketWithArrayBufferFrames, openWebSocket } from "./web-socket.js";
import {
  EventSourceFactoryTest,
  EventSourceProbe,
  BrowserObserversTest,
  ObserverProbe,
  WebSocketFailureTest,
  WebSocketInterruptionTest,
  WebSocketProbe,
  WebSocketSuccessTest,
} from "../testing/effect-layers.js";

class ReaderProbe implements ReadableStreamDefaultReader<Uint8Array> {
  readonly closed = Promise.resolve();
  cancelCount = 0;
  releaseCount = 0;
  readCount = 0;
  readonly waitAfterFirst: boolean;

  constructor(waitAfterFirst = false) {
    this.waitAfterFirst = waitAfterFirst;
  }

  cancel(): Promise<void> {
    this.cancelCount += 1;
    return Promise.resolve();
  }

  read(): Promise<ReadableStreamReadResult<Uint8Array>> {
    this.readCount += 1;
    if (this.readCount === 1) {
      return Promise.resolve({ done: false, value: new Uint8Array([7]) });
    }
    if (this.waitAfterFirst) {
      return new Promise(() => undefined);
    }
    return Promise.resolve({ done: true, value: undefined });
  }

  releaseLock(): void {
    this.releaseCount += 1;
  }
}

it.effect("configures browser sockets to preserve binary frame order", () =>
  Effect.sync(() => {
    const socket: { binaryType: BinaryType } = { binaryType: "blob" };
    const result = makeWebSocketWithArrayBufferFrames(() => socket, "wss://example.invalid/events");

    assert.strictEqual(result, socket);
    assert.strictEqual(socket.binaryType, "arraybuffer");
  }),
);

it.layer(EventSourceFactoryTest)("EventSource release after success", (it) => {
  it.effect("closes the source when the scoped owner succeeds", () =>
    Effect.gen(function* () {
      const probe = yield* EventSourceProbe;

      yield* Effect.scoped(openEventSource("/api/v1/events"));

      assert.strictEqual(yield* probe.closeCount, 1);
    }),
  );
});

it.layer(EventSourceFactoryTest)("EventSource release after failure", (it) => {
  it.effect("closes the source when its scoped owner fails", () =>
    Effect.gen(function* () {
      const probe = yield* EventSourceProbe;

      yield* Effect.exit(Effect.scoped(openEventSource("/api/v1/events").pipe(Effect.andThen(Effect.fail("stop")))));

      assert.strictEqual(yield* probe.closeCount, 1);
    }),
  );
});

it.layer(EventSourceFactoryTest)("EventSource release after interruption", (it) => {
  it.effect("closes the source when its scoped owner is interrupted", () =>
    Effect.gen(function* () {
      const probe = yield* EventSourceProbe;
      const fiber = yield* Effect.forkChild(
        Effect.scoped(openEventSource("/api/v1/events").pipe(Effect.andThen(Effect.never))),
      );
      yield* probe.awaitOpened;

      yield* Fiber.interrupt(fiber);

      assert.strictEqual(yield* probe.closeCount, 1);
    }),
  );
});

it.layer(EventSourceFactoryTest)("EventSource overflow", (it) => {
  it.effect("fails the stream and closes the source when a callback burst fills the buffer", () =>
    Effect.gen(function* () {
      const probe = yield* EventSourceProbe;
      const fiber = yield* Effect.forkChild(Stream.runDrain(eventSourceStream("/api/v1/events")));
      yield* probe.awaitOpened;
      yield* Effect.yieldNow;

      yield* probe.emitMessages(65);

      const failure = yield* Fiber.join(fiber).pipe(Effect.flip);
      assert.strictEqual(failure._tag, "TransientTransportError");
      assert.strictEqual(failure.operation, "buffer EventSource /api/v1/events");
      assert.strictEqual(yield* probe.closeCount, 1);
    }),
  );
});

it.effect("cancels and releases a byte reader after successful consumption", () =>
  Effect.gen(function* () {
    const reader = new ReaderProbe();

    const bytes = yield* Stream.runCollect(byteStreamFromReader(Effect.succeed(reader), "read diff"));

    assert.deepStrictEqual(Array.from(bytes), [new Uint8Array([7])]);
    assert.strictEqual(reader.cancelCount, 1);
    assert.strictEqual(reader.releaseCount, 1);
  }),
);

it.effect("cancels and releases a byte reader after a typed consumer failure", () =>
  Effect.gen(function* () {
    const reader = new ReaderProbe(true);

    yield* Effect.exit(
      Stream.runForEach(byteStreamFromReader(Effect.succeed(reader), "read diff"), () => Effect.fail("stop")),
    );

    assert.strictEqual(reader.cancelCount, 1);
    assert.strictEqual(reader.releaseCount, 1);
  }),
);

it.effect("cancels and releases a byte reader after interruption", () =>
  Effect.gen(function* () {
    const reader = new ReaderProbe(true);
    const firstRead = yield* Deferred.make<void>();
    const fiber = yield* Effect.forkChild(
      Stream.runForEach(byteStreamFromReader(Effect.succeed(reader), "read diff"), () =>
        Deferred.succeed(firstRead, undefined),
      ),
    );
    yield* Deferred.await(firstRead);

    yield* Fiber.interrupt(fiber);

    assert.strictEqual(reader.cancelCount, 1);
    assert.strictEqual(reader.releaseCount, 1);
  }),
);

it.effect("stops reading when the byte buffer is full until the consumer pulls", () =>
  Effect.gen(function* () {
    const seventeenthRead = yield* Deferred.make<void>();
    const eighteenthRead = yield* Deferred.make<void>();
    let readCount = 0;
    const reader: ByteReader = {
      closed: Promise.resolve(),
      cancel: () => Promise.resolve(),
      read: () => {
        readCount += 1;
        if (readCount === 17) Deferred.doneUnsafe(seventeenthRead, Effect.void);
        if (readCount === 18) Deferred.doneUnsafe(eighteenthRead, Effect.void);
        return Promise.resolve({ done: false, value: new Uint8Array([readCount]) });
      },
      releaseLock: () => undefined,
    };
    const pull = yield* Stream.toPull(byteStreamFromReader(Effect.succeed(reader), "read diff"));
    yield* Deferred.await(seventeenthRead);

    assert.strictEqual(readCount, 17);
    yield* pull;
    yield* Deferred.await(eighteenthRead);
    assert.isAbove(readCount, 17);
  }),
);

it.layer(WebSocketSuccessTest)("WebSocket release after success", (it) => {
  it.effect("closes the socket after a clean peer close", () =>
    Effect.gen(function* () {
      const probe = yield* WebSocketProbe;
      const socket = yield* openWebSocket("wss://example.invalid/events");

      yield* socket.runString(() => undefined);

      assert.strictEqual(yield* probe.closeCount, 1);
    }),
  );
});

it.layer(WebSocketFailureTest)("WebSocket release after failure", (it) => {
  it.effect("closes the socket after a typed consumer failure", () =>
    Effect.gen(function* () {
      const probe = yield* WebSocketProbe;
      const socket = yield* openWebSocket("wss://example.invalid/events");

      const exit = yield* Effect.exit(socket.runString(() => Effect.fail("stop")));

      assert.strictEqual(exit._tag, "Failure");
      assert.strictEqual(yield* probe.closeCount, 1);
    }),
  );
});

it.layer(WebSocketInterruptionTest)("WebSocket release after interruption", (it) => {
  it.effect("closes the socket when its run loop is interrupted", () =>
    Effect.gen(function* () {
      const probe = yield* WebSocketProbe;
      const socket = yield* openWebSocket("wss://example.invalid/events");
      const fiber = yield* Effect.forkChild(socket.runString(() => undefined));
      yield* probe.awaitOpened;

      yield* Fiber.interrupt(fiber);

      assert.strictEqual(yield* probe.closeCount, 1);
    }),
  );
});

it.layer(BrowserObserversTest)("ResizeObserver release", (it) => {
  it.effect("disconnects the observer after success, failure, and interruption", () =>
    Effect.gen(function* () {
      const probe = yield* ObserverProbe;
      const target = document.createElement("div");

      yield* Effect.scoped(observeResize(target, () => undefined));
      yield* Effect.exit(
        Effect.scoped(observeResize(target, () => undefined).pipe(Effect.andThen(Effect.fail("stop")))),
      );
      const fiber = yield* Effect.forkChild(
        Effect.scoped(observeResize(target, () => undefined).pipe(Effect.andThen(Effect.never))),
      );
      yield* Effect.yieldNow;
      yield* Fiber.interrupt(fiber);

      assert.strictEqual(yield* probe.disconnectCount, 3);
    }),
  );
});
