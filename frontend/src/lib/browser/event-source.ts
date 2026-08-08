import { Cause, Context, Effect, Layer, Queue, Stream } from "effect";
import { TransientTransportError } from "../api/effect-errors.js";

export interface EventSourceLike extends EventTarget {
  close(): void;
}

export class EventSourceFactory extends Context.Service<
  EventSourceFactory,
  {
    readonly open: (url: string) => Effect.Effect<EventSourceLike, TransientTransportError>;
  }
>()("kenn-forge/browser/EventSourceFactory") {}

export const EventSourceFactoryLive = Layer.succeed(EventSourceFactory)({
  open: (url) =>
    Effect.try({
      try: () => new EventSource(url),
      catch: (cause) => TransientTransportError.make({ operation: `open EventSource ${url}`, cause }),
    }),
});

export const openEventSource = Effect.fn("EventSource.open")(function* (url: string) {
  const factory = yield* EventSourceFactory;
  return yield* Effect.acquireRelease(factory.open(url), (source) => Effect.sync(() => source.close()));
});

export const eventSourceStream = (url: string, eventType = "message") =>
  Stream.callback<MessageEvent, TransientTransportError, EventSourceFactory>(
    (queue) =>
      Effect.gen(function* () {
        const source = yield* openEventSource(url);
        const onMessage = (event: Event) => {
          if (!(event instanceof MessageEvent)) return;
          if (Queue.offerUnsafe(queue, event)) return;
          Queue.failCauseUnsafe(
            queue,
            Cause.fail(
              TransientTransportError.make({
                operation: `buffer EventSource ${url}`,
                cause: new Error("EventSource buffer overflow"),
              }),
            ),
          );
        };
        const onError = (cause: Event) => {
          Queue.failCauseUnsafe(
            queue,
            Cause.fail(TransientTransportError.make({ operation: `read EventSource ${url}`, cause })),
          );
        };
        source.addEventListener(eventType, onMessage);
        source.addEventListener("error", onError, { once: true });
        yield* Effect.addFinalizer(() =>
          Effect.sync(() => {
            source.removeEventListener(eventType, onMessage);
            source.removeEventListener("error", onError);
          }),
        );
      }),
    { bufferSize: 64, strategy: "dropping" },
  );
