import { assert, it } from "@effect/vitest";
import { Deferred, Effect, Fiber, Layer, Stream } from "effect";
import { makeGeneratedApiLayer } from "../generated-api.js";
import { createRuntimeClient } from "../runtime.js";
import { kataEventStream } from "./eventStream.js";

function streamingFetchLayer(fetchImpl: typeof fetch) {
  return makeGeneratedApiLayer(createRuntimeClient(fetchImpl));
}

it.effect("interrupts a Kata event stream before response headers arrive", () =>
  Effect.scoped(
    Effect.gen(function* () {
      const requestStarted = yield* Deferred.make<void>();
      const requestAborted = yield* Deferred.make<void>();
      const fetchImpl: typeof fetch = (input, init) =>
        new Promise<Response>((_resolve, reject) => {
          const request = input instanceof Request ? input : new Request(input, init);
          Deferred.doneUnsafe(requestStarted, Effect.void);
          request.signal.addEventListener(
            "abort",
            () => {
              Deferred.doneUnsafe(requestAborted, Effect.void);
              reject(new DOMException("request aborted", "AbortError"));
            },
            { once: true },
          );
        });
      const fiber = yield* kataEventStream({ daemonId: "work", lastEventID: 51 }).pipe(
        Stream.runDrain,
        Effect.provide(streamingFetchLayer(fetchImpl)),
        Effect.forkChild,
      );

      yield* Deferred.await(requestStarted);
      yield* Fiber.interrupt(fiber);

      assert.isTrue(yield* Deferred.isDone(requestAborted));
    }),
  ),
);

it.effect("cancels the active Kata event reader when its owner is interrupted", () =>
  Effect.scoped(
    Effect.gen(function* () {
      const readerWaiting = yield* Deferred.make<void>();
      const readerCancelled = yield* Deferred.make<void>();
      let pulls = 0;
      const fetchImpl: typeof fetch = () =>
        Promise.resolve(
          new Response(
            new ReadableStream<Uint8Array>({
              pull(controller) {
                pulls += 1;
                if (pulls === 1) {
                  controller.enqueue(new TextEncoder().encode(": connected\n\n"));
                  return;
                }
                Deferred.doneUnsafe(readerWaiting, Effect.void);
              },
              cancel() {
                Deferred.doneUnsafe(readerCancelled, Effect.void);
              },
            }),
            { status: 200, headers: { "Content-Type": "text/event-stream" } },
          ),
        );
      const fiber = yield* kataEventStream({ daemonId: "work", lastEventID: 51 }).pipe(
        Stream.runDrain,
        Effect.provide(streamingFetchLayer(fetchImpl)),
        Effect.forkChild,
      );

      yield* Deferred.await(readerWaiting);
      yield* Fiber.interrupt(fiber);

      assert.isTrue(yield* Deferred.isDone(readerCancelled));
    }),
  ),
);
