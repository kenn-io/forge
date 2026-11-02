import { assert, it } from "@effect/vitest";
import { Context, Effect, Fiber, Layer, Tracer } from "effect";
import { TestClock } from "effect/testing";
import * as Socket from "effect/socket/Socket";

import { makeTerminalSessionController } from "./terminal-session.js";

class ControlledWebSocket extends EventTarget implements WebSocket {
  readonly CONNECTING = 0;
  readonly OPEN = 1;
  readonly CLOSING = 2;
  readonly CLOSED = 3;
  binaryType: BinaryType = "arraybuffer";
  readonly bufferedAmount = 0;
  readonly extensions = "";
  onclose: ((event: CloseEvent) => void) | null = null;
  onerror: ((event: Event) => void) | null = null;
  onmessage: ((event: MessageEvent) => void) | null = null;
  onopen: ((event: Event) => void) | null = null;
  readonly protocol = "";
  readyState: WebSocket["readyState"] = this.CONNECTING;
  readonly sent: Array<string | ArrayBufferLike | Blob | ArrayBufferView> = [];
  closeCount = 0;

  constructor(readonly url: string) {
    super();
  }

  close(): void {
    this.closeCount += 1;
    this.readyState = this.CLOSED;
  }

  send(data: string | ArrayBufferLike | Blob | ArrayBufferView): void {
    this.sent.push(data);
  }

  open(): void {
    this.readyState = this.OPEN;
    this.dispatchEvent(new Event("open"));
  }

  message(data: string | ArrayBuffer): void {
    this.dispatchEvent(new MessageEvent("message", { data }));
  }

  peerClose(code = 1006): void {
    this.readyState = this.CLOSED;
    this.dispatchEvent(new CloseEvent("close", { code }));
  }
}

class TerminalSocketProbe extends Context.Service<
  TerminalSocketProbe,
  { readonly sockets: Array<ControlledWebSocket> }
>()("kenn-forge/testing/TerminalSocketProbe") {}

function makeTerminalSocketTest() {
  return Layer.provideMerge(
    Layer.effect(Socket.WebSocketConstructor)(
      Effect.gen(function* () {
        const probe = yield* TerminalSocketProbe;
        return (url: string) => {
          const socket = new ControlledWebSocket(url);
          probe.sockets.push(socket);
          return socket;
        };
      }),
    ),
    Layer.succeed(TerminalSocketProbe)({ sockets: [] }),
  );
}

it.layer(makeTerminalSocketTest())("terminal socket interruption", (it) => {
  it.effect("closes a connecting socket when its owner is interrupted", () =>
    Effect.gen(function* () {
      const probe = yield* TerminalSocketProbe;
      probe.sockets.length = 0;
      const controller = makeTerminalSessionController({
        url: () => "wss://example.invalid/terminal",
        onMessage: () => "continue",
      });
      const fiber = yield* Effect.forkChild(Effect.scoped(controller.program));
      yield* Effect.repeat(Effect.yieldNow, { times: 5 });

      assert.lengthOf(probe.sockets, 1);
      assert.strictEqual(probe.sockets[0]?.readyState, WebSocket.CONNECTING);

      yield* Fiber.interrupt(fiber);

      assert.strictEqual(probe.sockets[0]?.closeCount, 1);
    }),
  );
});

it.layer(makeTerminalSocketTest())("terminal socket reconnect", (it) => {
  it.effect("reconnects with Effect backoff after an unexpected close", () =>
    Effect.gen(function* () {
      const probe = yield* TerminalSocketProbe;
      probe.sockets.length = 0;
      let disconnects = 0;
      const controller = makeTerminalSessionController({
        url: () => "wss://example.invalid/terminal",
        onMessage: () => "continue",
        onDisconnected: () => {
          disconnects += 1;
        },
      });
      const fiber = yield* Effect.forkChild(Effect.scoped(controller.program));
      yield* Effect.repeat(Effect.yieldNow, { times: 5 });
      probe.sockets[0]?.open();
      probe.sockets[0]?.peerClose();
      yield* Effect.yieldNow;

      assert.lengthOf(probe.sockets, 1);
      assert.isFalse(controller.isConnected());
      assert.strictEqual(disconnects, 1);
      yield* TestClock.adjust("999 millis");
      assert.lengthOf(probe.sockets, 1);
      yield* TestClock.adjust("1 millis");
      yield* Effect.yieldNow;
      assert.lengthOf(probe.sockets, 2);
      assert.strictEqual(probe.sockets[0]?.closeCount, 1);

      yield* Fiber.interrupt(fiber);
    }),
  );

  it.effect("resets reconnect backoff after every successful open", () =>
    Effect.gen(function* () {
      const probe = yield* TerminalSocketProbe;
      probe.sockets.length = 0;
      const controller = makeTerminalSessionController({
        url: () => "wss://example.invalid/terminal",
        onMessage: () => "continue",
      });
      const fiber = yield* Effect.forkChild(Effect.scoped(controller.program));
      yield* Effect.repeat(Effect.yieldNow, { times: 5 });

      probe.sockets[0]?.open();
      probe.sockets[0]?.peerClose();
      yield* TestClock.adjust("1 second");
      yield* Effect.repeat(Effect.yieldNow, { times: 5 });
      probe.sockets[1]?.open();
      yield* Effect.yieldNow;
      probe.sockets[1]?.peerClose();
      yield* Effect.yieldNow;
      yield* TestClock.adjust("999 millis");
      assert.lengthOf(probe.sockets, 2);
      yield* TestClock.adjust("1 millis");
      yield* Effect.repeat(Effect.yieldNow, { times: 5 });
      assert.lengthOf(probe.sockets, 3);

      yield* Fiber.interrupt(fiber);
    }),
  );

  it.effect("resets reconnect backoff after repeated clean closes", () =>
    Effect.gen(function* () {
      const probe = yield* TerminalSocketProbe;
      probe.sockets.length = 0;
      const controller = makeTerminalSessionController({
        url: () => "wss://example.invalid/terminal",
        onMessage: () => "continue",
      });
      const fiber = yield* Effect.forkChild(Effect.scoped(controller.program));
      yield* Effect.repeat(Effect.yieldNow, { times: 5 });

      probe.sockets[0]?.open();
      while (!controller.isConnected()) yield* Effect.yieldNow;
      probe.sockets[0]?.peerClose(1000);
      while (controller.isConnected()) yield* Effect.yieldNow;
      yield* Effect.yieldNow;
      yield* TestClock.adjust("1 second");
      yield* Effect.repeat(Effect.yieldNow, { times: 5 });
      probe.sockets[1]?.open();
      while (!controller.isConnected()) yield* Effect.yieldNow;
      probe.sockets[1]?.peerClose(1000);
      while (controller.isConnected()) yield* Effect.yieldNow;
      yield* Effect.repeat(Effect.yieldNow, { times: 5 });
      yield* TestClock.adjust("999 millis");
      assert.lengthOf(probe.sockets, 2);
      yield* TestClock.adjust("1 millis");
      yield* Effect.repeat(Effect.yieldNow, { times: 5 });
      assert.lengthOf(probe.sockets, 3);

      yield* Fiber.interrupt(fiber);
    }),
  );

  it.effect("replaces a socket that stops answering heartbeat round trips", () =>
    Effect.gen(function* () {
      const probe = yield* TerminalSocketProbe;
      probe.sockets.length = 0;
      const controller = makeTerminalSessionController({
        url: () => "wss://example.invalid/terminal",
        onMessage: () => "continue",
      });
      const fiber = yield* Effect.forkChild(Effect.scoped(controller.program));
      yield* Effect.repeat(Effect.yieldNow, { times: 5 });
      probe.sockets[0]?.open();
      yield* Effect.repeat(Effect.yieldNow, { times: 5 });

      assert.include(probe.sockets[0]?.sent ?? [], '{"type":"heartbeat"}');
      yield* TestClock.adjust("44 seconds");
      probe.sockets[0]?.message('{"type":"heartbeat"}');
      yield* Effect.yieldNow;
      yield* TestClock.adjust("44 seconds");
      assert.isTrue(controller.isConnected());
      yield* TestClock.adjust("16 seconds");
      yield* Effect.repeat(Effect.yieldNow, { times: 5 });
      assert.isFalse(controller.isConnected());
      assert.strictEqual(probe.sockets[0]?.closeCount, 1);
      yield* TestClock.adjust("1 second");
      yield* Effect.repeat(Effect.yieldNow, { times: 5 });
      assert.lengthOf(probe.sockets, 2);

      yield* Fiber.interrupt(fiber);
    }),
  );

  it.effect("does not drop outbound frames while the socket is connected", () =>
    Effect.gen(function* () {
      const probe = yield* TerminalSocketProbe;
      probe.sockets.length = 0;
      const controller = makeTerminalSessionController({
        url: () => "wss://example.invalid/terminal",
        onMessage: () => "continue",
      });
      const fiber = yield* Effect.forkChild(Effect.scoped(controller.program));
      yield* Effect.repeat(Effect.yieldNow, { times: 5 });
      probe.sockets[0]?.open();
      yield* Effect.yieldNow;
      assert.isTrue(controller.isConnected());

      for (let index = 0; index < 300; index += 1) {
        controller.send(`frame-${index}`);
      }
      yield* Effect.repeat(Effect.yieldNow, { times: 300 });
      const dataFrames = probe.sockets[0]?.sent.filter((frame) => String(frame).startsWith("frame-"));
      assert.lengthOf(dataFrames ?? [], 300);

      yield* Fiber.interrupt(fiber);
    }),
  );
});

it.layer(makeTerminalSocketTest())("terminal process restart", (it) => {
  it.effect("normalizes browser ArrayBuffer frames before delivering them", () =>
    Effect.gen(function* () {
      const probe = yield* TerminalSocketProbe;
      probe.sockets.length = 0;
      const seen: Array<string | Uint8Array> = [];
      const controller = makeTerminalSessionController({
        url: () => "wss://example.invalid/terminal",
        onMessage: (data) => {
          seen.push(data);
          return "continue";
        },
      });
      const fiber = yield* Effect.forkChild(Effect.scoped(controller.program));
      yield* Effect.repeat(Effect.yieldNow, { times: 5 });
      probe.sockets[0]?.open();

      const frame = Uint8Array.from([0x1b, 0x5b, 0x3f, 0x31, 0x68]);
      probe.sockets[0]?.message(frame.buffer);
      yield* Effect.repeat(Effect.yieldNow, { times: 5 });

      assert.lengthOf(seen, 1);
      assert.instanceOf(seen[0], Uint8Array);
      assert.deepStrictEqual(seen[0], frame);

      yield* Fiber.interrupt(fiber);
    }),
  );

  it.effect("drains frames queued immediately before close", () =>
    Effect.gen(function* () {
      const probe = yield* TerminalSocketProbe;
      probe.sockets.length = 0;
      const seen: Array<string | Uint8Array> = [];
      const controller = makeTerminalSessionController({
        url: () => "wss://example.invalid/terminal",
        onMessage: (data) => {
          seen.push(data);
          return data === "exited" ? "restart" : "continue";
        },
      });
      const fiber = yield* Effect.forkChild(Effect.scoped(controller.program));
      yield* Effect.repeat(Effect.yieldNow, { times: 5 });
      probe.sockets[0]?.open();

      probe.sockets[0]?.message("final output");
      probe.sockets[0]?.message("exited");
      probe.sockets[0]?.peerClose(1000);
      yield* Effect.repeat(Effect.yieldNow, { times: 5 });

      assert.deepStrictEqual(seen, ["final output", "exited"]);
      yield* TestClock.adjust("2 seconds");
      yield* Effect.yieldNow;
      assert.lengthOf(probe.sockets, 2);

      yield* Fiber.interrupt(fiber);
    }),
  );

  it.effect("waits two seconds before reconnecting after an exited frame", () =>
    Effect.gen(function* () {
      const probe = yield* TerminalSocketProbe;
      probe.sockets.length = 0;
      const controller = makeTerminalSessionController({
        url: () => "wss://example.invalid/terminal",
        onMessage: (data) => (data === "exited" ? "restart" : "continue"),
      });
      const fiber = yield* Effect.forkChild(Effect.scoped(controller.program));
      yield* Effect.repeat(Effect.yieldNow, { times: 5 });
      probe.sockets[0]?.open();
      probe.sockets[0]?.message("exited");
      yield* Effect.yieldNow;

      yield* TestClock.adjust("1999 millis");
      assert.lengthOf(probe.sockets, 1);
      yield* TestClock.adjust("1 millis");
      yield* Effect.yieldNow;
      assert.lengthOf(probe.sockets, 2);

      yield* Fiber.interrupt(fiber);
    }),
  );
});

// Records every span the program starts, so a test can read how each
// connection attempt's first-frame phase ended.
function recordingTracer() {
  const spans: Tracer.NativeSpan[] = [];
  const tracer = Tracer.make({
    span(options) {
      const span = new Tracer.NativeSpan(options);
      spans.push(span);
      return span;
    },
  });
  const named = (name: string) => spans.filter((span) => span.name === name);
  return { tracer, named };
}

function readyOptions(onTimeout: () => void, isReady: () => boolean = () => true) {
  return {
    timeout: "15 seconds",
    isReady,
    connectSpan: "Chat.connect",
    reconnectSpan: "Chat.reconnect",
    attributes: { "acp.session_key": "ws-1//chat/1" },
    onTimeout,
  } as const;
}

it.layer(makeTerminalSocketTest())("terminal ready deadline", (it) => {
  it.effect("stops a socket that opens but sends nothing before the deadline", () =>
    Effect.gen(function* () {
      const probe = yield* TerminalSocketProbe;
      probe.sockets.length = 0;
      const { tracer, named } = recordingTracer();
      let timeouts = 0;
      const controller = makeTerminalSessionController({
        url: () => "wss://example.invalid/chat",
        onMessage: () => "continue",
        ready: readyOptions(() => {
          timeouts += 1;
        }),
      });
      const fiber = yield* Effect.forkChild(Effect.scoped(controller.program).pipe(Effect.withTracer(tracer)));
      yield* Effect.repeat(Effect.yieldNow, { times: 5 });
      probe.sockets[0]?.open();
      yield* Effect.repeat(Effect.yieldNow, { times: 5 });
      assert.isTrue(controller.isConnected());

      yield* TestClock.adjust("14999 millis");
      assert.strictEqual(timeouts, 0);
      yield* TestClock.adjust("1 millis");
      yield* Effect.repeat(Effect.yieldNow, { times: 5 });

      assert.strictEqual(timeouts, 1);
      assert.isFalse(controller.isConnected());
      assert.strictEqual(probe.sockets[0]?.closeCount, 1);
      const [span] = named("Chat.connect");
      assert.strictEqual(span?.attributes.get("outcome"), "timeout");
      assert.strictEqual(span?.attributes.get("acp.session_key"), "ws-1//chat/1");
      assert.strictEqual(span?.attributes.get("attempt"), 1);
      assert.strictEqual(span?.status._tag, "Ended");

      // Timing out is final: the pane offers a retry instead.
      yield* TestClock.adjust("2 minutes");
      yield* Effect.repeat(Effect.yieldNow, { times: 5 });
      assert.lengthOf(probe.sockets, 1);
      assert.strictEqual(timeouts, 1);

      yield* Fiber.interrupt(fiber);
    }),
  );

  it.effect("ends the attach span at the first frame and keeps streaming", () =>
    Effect.gen(function* () {
      const probe = yield* TerminalSocketProbe;
      probe.sockets.length = 0;
      const { tracer, named } = recordingTracer();
      const frames: string[] = [];
      let timeouts = 0;
      const controller = makeTerminalSessionController({
        url: () => "wss://example.invalid/chat",
        onMessage: (data) => {
          if (typeof data === "string") frames.push(data);
          return "continue";
        },
        ready: readyOptions(() => {
          timeouts += 1;
        }),
      });
      const fiber = yield* Effect.forkChild(Effect.scoped(controller.program).pipe(Effect.withTracer(tracer)));
      yield* Effect.repeat(Effect.yieldNow, { times: 5 });
      probe.sockets[0]?.open();
      yield* Effect.repeat(Effect.yieldNow, { times: 5 });
      assert.strictEqual(named("Chat.connect")[0]?.status._tag, "Started");

      probe.sockets[0]?.message('{"state":1}');
      yield* Effect.repeat(Effect.yieldNow, { times: 5 });
      const [span] = named("Chat.connect");
      assert.strictEqual(span?.status._tag, "Ended");
      assert.strictEqual(span?.attributes.get("outcome"), "ready");

      yield* TestClock.adjust("20 seconds");
      probe.sockets[0]?.message('{"state":2}');
      yield* Effect.repeat(Effect.yieldNow, { times: 5 });
      assert.deepStrictEqual(frames, ['{"state":1}', '{"state":2}']);
      assert.strictEqual(timeouts, 0);
      assert.isTrue(controller.isConnected());

      yield* Fiber.interrupt(fiber);
    }),
  );

  it.effect("starts the deadline only once a slow handshake opens the socket", () =>
    Effect.gen(function* () {
      const probe = yield* TerminalSocketProbe;
      probe.sockets.length = 0;
      let timeouts = 0;
      const controller = makeTerminalSessionController({
        url: () => "wss://example.invalid/chat",
        onMessage: () => "continue",
        ready: readyOptions(() => {
          timeouts += 1;
        }),
      });
      const fiber = yield* Effect.forkChild(Effect.scoped(controller.program));
      yield* Effect.repeat(Effect.yieldNow, { times: 5 });
      // Inside the handshake's own 10 second limit.
      yield* TestClock.adjust("9 seconds");
      probe.sockets[0]?.open();
      yield* Effect.repeat(Effect.yieldNow, { times: 5 });
      yield* TestClock.adjust("14 seconds");
      probe.sockets[0]?.message('{"state":1}');
      yield* Effect.repeat(Effect.yieldNow, { times: 5 });
      yield* TestClock.adjust("5 seconds");
      yield* Effect.repeat(Effect.yieldNow, { times: 5 });

      assert.strictEqual(timeouts, 0);
      assert.isTrue(controller.isConnected());
      assert.lengthOf(probe.sockets, 1);

      yield* Fiber.interrupt(fiber);
    }),
  );

  it.effect("keeps the deadline through frames that leave the consumer not ready", () =>
    Effect.gen(function* () {
      const probe = yield* TerminalSocketProbe;
      probe.sockets.length = 0;
      const { tracer, named } = recordingTracer();
      let state = false;
      let timeouts = 0;
      const controller = makeTerminalSessionController({
        url: () => "wss://example.invalid/chat",
        onMessage: (data) => {
          if (data === '{"state":1}') state = true;
          return "continue";
        },
        ready: readyOptions(
          () => {
            timeouts += 1;
          },
          () => state,
        ),
      });
      const fiber = yield* Effect.forkChild(Effect.scoped(controller.program).pipe(Effect.withTracer(tracer)));
      yield* Effect.repeat(Effect.yieldNow, { times: 5 });
      probe.sockets[0]?.open();
      yield* Effect.repeat(Effect.yieldNow, { times: 5 });
      // A command reply arrives, but the state the consumer needs never does.
      probe.sockets[0]?.message('{"commandError":"busy","command":"cancel","id":""}');
      yield* Effect.repeat(Effect.yieldNow, { times: 5 });
      yield* TestClock.adjust("15 seconds");
      yield* Effect.repeat(Effect.yieldNow, { times: 5 });

      assert.strictEqual(timeouts, 1);
      assert.isFalse(controller.isConnected());
      assert.strictEqual(named("Chat.connect")[0]?.attributes.get("outcome"), "timeout");

      yield* Fiber.interrupt(fiber);
    }),
  );

  it.effect("traces a reconnect separately after a socket closes before its first frame", () =>
    Effect.gen(function* () {
      const probe = yield* TerminalSocketProbe;
      probe.sockets.length = 0;
      const { tracer, named } = recordingTracer();
      const controller = makeTerminalSessionController({
        url: () => "wss://example.invalid/chat",
        onMessage: () => "continue",
        ready: readyOptions(() => undefined),
      });
      const fiber = yield* Effect.forkChild(Effect.scoped(controller.program).pipe(Effect.withTracer(tracer)));
      yield* Effect.repeat(Effect.yieldNow, { times: 5 });
      probe.sockets[0]?.open();
      yield* Effect.repeat(Effect.yieldNow, { times: 5 });
      probe.sockets[0]?.peerClose();
      yield* Effect.repeat(Effect.yieldNow, { times: 5 });
      assert.strictEqual(named("Chat.connect")[0]?.attributes.get("outcome"), "closed_reconnect");

      yield* TestClock.adjust("1 second");
      yield* Effect.repeat(Effect.yieldNow, { times: 5 });
      probe.sockets[1]?.open();
      yield* Effect.repeat(Effect.yieldNow, { times: 5 });
      probe.sockets[1]?.message('{"state":1}');
      yield* Effect.repeat(Effect.yieldNow, { times: 5 });

      const [reconnect] = named("Chat.reconnect");
      assert.strictEqual(reconnect?.attributes.get("outcome"), "ready");
      assert.strictEqual(reconnect?.attributes.get("attempt"), 2);
      assert.lengthOf(named("Chat.connect"), 1);

      yield* Fiber.interrupt(fiber);
    }),
  );
});
