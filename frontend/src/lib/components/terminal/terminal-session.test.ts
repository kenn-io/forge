import { assert, it } from "@effect/vitest";
import { Context, Effect, Fiber, Layer } from "effect";
import { TestClock } from "effect/testing";
import * as Socket from "effect/unstable/socket/Socket";

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
