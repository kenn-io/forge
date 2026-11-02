import { Data, Deferred, Duration, Effect, Exit, Fiber, Queue, Schedule } from "effect";
import type { Scope } from "effect/Scope";
import * as Socket from "effect/socket/Socket";

import { openWebSocket } from "../../browser/web-socket.js";

export type TerminalMessageDecision = "continue" | "restart" | "stop";
type TerminalConnectionOutcome = "reconnect" | "restart" | "stop";

// How an attempt's first-frame phase ended, recorded on its span.
function firstFrameOutcome(exit: Exit.Exit<TerminalConnectionOutcome | "first_frame", unknown>): string {
  if (Exit.isSuccess(exit)) return exit.value === "first_frame" ? "first_frame" : `closed_${exit.value}`;
  if (exit.cause.reasons.every((reason) => reason._tag === "Interrupt")) return "interrupted";
  return exit.cause.reasons.some(
    (reason) => reason._tag === "Fail" && reason.error instanceof TerminalFirstFrameTimeout,
  )
    ? "timeout"
    : "connect_failed";
}

export interface TerminalSessionOptions {
  readonly url: () => string | undefined;
  readonly initialStatus?: string | undefined;
  readonly onOpen?: (() => void) | undefined;
  readonly onMessage: (data: string | Uint8Array) => TerminalMessageDecision;
  readonly onDisconnected?: (() => void) | undefined;
  /**
   * Bounds and traces each connection's wait for its first frame. The span
   * covers one attempt from dialing to that frame, so it never lives as long as
   * the stream. A connection that opens but sends nothing within the timeout
   * stops for good and calls onTimeout; one that fails before it retries.
   */
  readonly firstFrame?: TerminalFirstFrameOptions | undefined;
}

export interface TerminalFirstFrameOptions {
  readonly timeout: Duration.Input;
  /** Span name for the first attempt; later attempts use reconnectSpan. */
  readonly connectSpan: string;
  readonly reconnectSpan: string;
  readonly attributes: Record<string, string>;
  readonly onTimeout: () => void;
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

class TerminalFirstFrameTimeout extends Data.TaggedError("TerminalFirstFrameTimeout")<{}> {}

class TerminalDisconnected extends Data.TaggedError("TerminalDisconnected")<{
  readonly cause: Socket.SocketError | Error;
  readonly opened: boolean;
}> {}

const terminalReconnectSchedule = Schedule.exponential("1 second").pipe(
  Schedule.modifyDelay(({ duration }) => Effect.succeed(Duration.min(duration, Duration.seconds(30)))),
);
const terminalHeartbeatInterval = Duration.seconds(15);
const terminalLivenessTimeout = Duration.seconds(45);
const terminalHeartbeatMessage = '{"type":"heartbeat"}';

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
  let attempts = 0;

  const connectOnce = Effect.fn("TerminalSession.connect")(function* () {
    attempts += 1;
    const firstFrame = yield* Deferred.make<void>();
    const url = options.url();
    if (url === undefined) return yield* Effect.never;
    const outbound = yield* Queue.unbounded<string | Uint8Array>();
    const inbound = yield* Queue.unbounded<
      { readonly kind: "frame"; readonly data: string | Uint8Array } | { readonly kind: "done" }
    >();
    const heartbeatReplies = yield* Queue.unbounded<void>();
    let opened = false;
    yield* Effect.addFinalizer(() =>
      Effect.sync(() => {
        publish = () => undefined;
      }).pipe(
        Effect.andThen(Queue.shutdown(outbound)),
        Effect.andThen(Queue.shutdown(inbound)),
        Effect.andThen(Queue.shutdown(heartbeatReplies)),
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
    const writeLoop = Effect.forever(Queue.take(outbound).pipe(Effect.flatMap(write.write)));
    const receiveLoop = Effect.gen(function* () {
      const reader = yield* socket.reader;
      yield* Effect.sync(() => {
        opened = true;
        connected = true;
        options.onOpen?.();
      });
      while (true) {
        const frames = yield* reader.pull;
        yield* Effect.sync(() => {
          for (const data of frames) {
            const frame = normalizeTerminalFrame(data);
            if (frame === terminalHeartbeatMessage) {
              Queue.offerUnsafe(heartbeatReplies, undefined);
            } else if (frame !== null) {
              Queue.offerUnsafe(inbound, { kind: "frame", data: frame });
            }
          }
        });
      }
    });
    const messageLoop = Effect.gen(function* () {
      while (true) {
        const message = yield* Queue.take(inbound);
        if (message.kind === "done") return;
        yield* Effect.sync(() => options.onMessage(message.data)).pipe(
          Effect.tap(() => Deferred.succeed(firstFrame, undefined)),
          Effect.flatMap(applyMessageDecision),
        );
      }
    });
    const readLoop = Effect.gen(function* () {
      const receiver = yield* Effect.forkChild(
        receiveLoop.pipe(Effect.ensuring(Effect.sync(() => Queue.offerUnsafe(inbound, { kind: "done" })))),
      );
      yield* messageLoop;
      return yield* Fiber.join(receiver);
    });
    const heartbeatLoop = Effect.forever(
      Queue.offer(outbound, terminalHeartbeatMessage).pipe(
        Effect.andThen(
          Queue.take(heartbeatReplies).pipe(
            Effect.timeoutOrElse({
              duration: terminalLivenessTimeout,
              orElse: () =>
                Effect.fail(
                  new TerminalDisconnected({
                    cause: new Error("terminal heartbeat timed out"),
                    opened,
                  }),
                ),
            }),
          ),
        ),
        Effect.andThen(Effect.sleep(terminalHeartbeatInterval)),
      ),
    );

    const connection = Effect.raceAllFirst([readLoop, writeLoop, heartbeatLoop]).pipe(
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
    const ready = options.firstFrame;
    if (ready === undefined) return yield* connection;
    const running = yield* Effect.forkScoped(connection, { startImmediately: true });
    const attached = yield* Effect.raceFirst(
      Deferred.await(firstFrame).pipe(
        Effect.as("first_frame" as const),
        Effect.timeoutOrElse({
          duration: ready.timeout,
          orElse: () => Effect.fail(new TerminalFirstFrameTimeout()),
        }),
      ),
      Fiber.join(running),
    ).pipe(
      Effect.onExit((exit) => Effect.annotateCurrentSpan("outcome", firstFrameOutcome(exit))),
      Effect.withSpan(attempts === 1 ? ready.connectSpan : ready.reconnectSpan, {
        attributes: { ...ready.attributes, attempt: attempts },
      }),
      Effect.catchTag("TerminalFirstFrameTimeout", () =>
        Effect.sync(ready.onTimeout).pipe(Effect.as<TerminalConnectionOutcome>("stop")),
      ),
    );
    return attached === "first_frame" ? yield* Fiber.join(running) : attached;
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
