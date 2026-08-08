import { Context, Effect, Layer } from "effect";

export interface ObserverFactories {
  readonly resize: (callback: ResizeObserverCallback) => ResizeObserver;
  readonly mutation: (callback: MutationCallback) => MutationObserver;
  readonly intersection: (
    callback: IntersectionObserverCallback,
    options?: IntersectionObserverInit,
  ) => IntersectionObserver;
}

export class BrowserObservers extends Context.Service<BrowserObservers, ObserverFactories>()(
  "kenn-forge/browser/Observers",
) {}

export const BrowserObserversLive = Layer.succeed(BrowserObservers)({
  resize: (callback) => new ResizeObserver(callback),
  mutation: (callback) => new MutationObserver(callback),
  intersection: (callback, options) => new IntersectionObserver(callback, options),
});

export const observeResize = Effect.fn("Observers.resize")(function* (
  target: Element,
  callback: ResizeObserverCallback,
) {
  const factories = yield* BrowserObservers;
  return yield* Effect.acquireRelease(
    Effect.sync(() => factories.resize(callback)).pipe(
      Effect.tap((observer) => Effect.sync(() => observer.observe(target))),
    ),
    (observer) => Effect.sync(() => observer.disconnect()),
  );
});

export const observeMutation = Effect.fn("Observers.mutation")(function* (
  target: Node,
  callback: MutationCallback,
  options: MutationObserverInit,
) {
  const factories = yield* BrowserObservers;
  return yield* Effect.acquireRelease(
    Effect.sync(() => factories.mutation(callback)).pipe(
      Effect.tap((observer) => Effect.sync(() => observer.observe(target, options))),
    ),
    (observer) => Effect.sync(() => observer.disconnect()),
  );
});

export const observeIntersection = Effect.fn("Observers.intersection")(function* (
  target: Element,
  callback: IntersectionObserverCallback,
  options?: IntersectionObserverInit,
) {
  const factories = yield* BrowserObservers;
  return yield* Effect.acquireRelease(
    Effect.sync(() => factories.intersection(callback, options)).pipe(
      Effect.tap((observer) => Effect.sync(() => observer.observe(target))),
    ),
    (observer) => Effect.sync(() => observer.disconnect()),
  );
});
