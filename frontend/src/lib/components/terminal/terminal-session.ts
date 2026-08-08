import { Data, Duration, Effect, Fiber, Queue, Schedule } from "effect";
import type { Scope } from "effect/Scope";
import * as Socket from "effect/unstable/socket/Socket";

import { openWebSocket } from "../../browser/web-socket.js";

export type TerminalMessageDecision = "continue" | "restart" | "stop";
type TerminalConnectionOutcome = "reconnect" | "restart" | "stop";

export interface TerminalSessionOptions {
  readonly url: () => string | undefined;
  readonly initialStatus?: string | undefined;
  readonly onOpen?: (() => void) | undefined;
  readonly onMessage: (data: string | Uint8Array) => TerminalMessageDecision;
  readonly onDisconnected?: (() => void) | undefined;
}

export interface TerminalSessionController {
  readonly program: Effect.Effect<void, never, Socket.WebSocketConstructor | Scope>;
  readonly send: (data: string | Uint8Array) => void;
  readonly isConnected: () => boolean;
}

class TerminalRestart extends Data.TaggedError("TerminalRestart")<{
  readonly reason: "process-exited";
}> {}

class TerminalStop extends Data.TaggedError("TerminalStop")<{
  readonly reason: "process-exited";
}> {}

class TerminalDisconnected extends Data.TaggedError("TerminalDisconnected")<{
  readonly cause: Socket.SocketError | Error;
  readonly opened: boolean;
}> {}

const terminalReconnectSchedule = Schedule.exponential("1 second").pipe(
  Schedule.modifyDelay(({ duration }) => Effect.succeed(Duration.min(duration, Duration.seconds(30)))),
);

function isAttachableInitialStatus(status: string | undefined): boolean {
  return status === undefined || status === "running" || status === "starting";
}

function applyMessageDecision(decision: TerminalMessageDecision): Effect.Effect<void, TerminalRestart | TerminalStop> {
  switch (decision) {
    case "restart":
      return Effect.fail(new TerminalRestart({ reason: "process-exited" }));
    case "stop":
      return Effect.fail(new TerminalStop({ reason: "process-exited" }));
    case "continue":
      return Effect.void;
  }
}

function normalizeTerminalFrame(data: unknown): string | Uint8Array | null {
  if (typeof data === "string" || data instanceof Uint8Array) return data;
  if (data instanceof ArrayBuffer) return new Uint8Array(data);
  return null;
}

export function makeTerminalSessionController(options: TerminalSessionOptions): TerminalSessionController {
  let publish: (data: string | Uint8Array) => void = () => undefined;
  let connected = false;

  const connectOnce = Effect.fn("TerminalSession.connect")(function* () {
    const url = options.url();
    if (url === undefined) return yield* Effect.never;
    const outbound = yield* Queue.unbounded<string | Uint8Array>();
    const inbound = yield* Queue.unbounded<
      { readonly kind: "frame"; readonly data: string | Uint8Array } | { readonly kind: "done" }
    >();
    let opened = false;
    yield* Effect.addFinalizer(() =>
      Effect.sync(() => {
        publish = () => undefined;
      }).pipe(
        Effect.andThen(Queue.shutdown(outbound)),
        Effect.andThen(Queue.shutdown(inbound)),
        Effect.andThen(
          Effect.sync(() => {
            connected = false;
            options.onDisconnected?.();
          }),
        ),
      ),
    );
    yield* Effect.sync(() => {
      publish = (data) => {
        Queue.offerUnsafe(outbound, data);
      };
    });

    const socket = yield* openWebSocket(url, { openTimeout: "10 seconds" });
    const write = yield* socket.writer;
    const writeLoop = Effect.forever(Queue.take(outbound).pipe(Effect.flatMap(write)));
    const receiveLoop = socket.runRaw(
      (data) => {
        const frame = normalizeTerminalFrame(data);
        if (frame !== null) Queue.offerUnsafe(inbound, { kind: "frame", data: frame });
      },
      {
        onOpen: Effect.sync(() => {
          opened = true;
          connected = true;
          options.onOpen?.();
        }),
      },
    );
    const messageLoop = Effect.gen(function* () {
      while (true) {
        const message = yield* Queue.take(inbound);
        if (message.kind === "done") return;
        yield* Effect.sync(() => options.onMessage(message.data)).pipe(Effect.flatMap(applyMessageDecision));
      }
    });
    const readLoop = Effect.gen(function* () {
      const receiver = yield* Effect.forkChild(
        receiveLoop.pipe(Effect.ensuring(Effect.sync(() => Queue.offerUnsafe(inbound, { kind: "done" })))),
      );
      yield* messageLoop;
      yield* Fiber.join(receiver);
    });

    return yield* Effect.raceFirst(readLoop, writeLoop).pipe(
      Effect.andThen(
        Effect.suspend(() =>
          Effect.fail(new TerminalDisconnected({ cause: new Error("terminal socket closed"), opened })),
        ),
      ),
      Effect.catchTags({
        TerminalRestart: () => Effect.succeed<TerminalConnectionOutcome>("restart"),
        TerminalStop: () => Effect.succeed<TerminalConnectionOutcome>("stop"),
        TerminalDisconnected: (failure) =>
          failure.opened ? Effect.succeed<TerminalConnectionOutcome>("reconnect") : Effect.fail(failure),
        SocketError: (cause: Socket.SocketError) =>
          opened
            ? Effect.succeed<TerminalConnectionOutcome>("reconnect")
            : Effect.fail(new TerminalDisconnected({ cause, opened: false })),
      }),
    );
  });

  const program = Effect.gen(function* () {
    yield* Effect.addFinalizer(() =>
      Effect.sync(() => {
        publish = () => undefined;
        connected = false;
      }),
    );
    if (!isAttachableInitialStatus(options.initialStatus)) return yield* Effect.never;
    const connectUntilProcessExit = Effect.scoped(connectOnce()).pipe(
      Effect.retry(terminalReconnectSchedule),
      Effect.catchTag("TerminalDisconnected", () => Effect.never),
    );
    return yield* Effect.forever(
      connectUntilProcessExit.pipe(
        Effect.flatMap((decision) =>
          decision === "stop" ? Effect.never : Effect.sleep(decision === "restart" ? "2 seconds" : "1 second"),
        ),
      ),
    );
  });

  return {
    program,
    send: (data) => publish(data),
    isConnected: () => connected,
  };
}
