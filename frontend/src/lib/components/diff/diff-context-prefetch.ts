import { Cause, Context, Deferred, Effect, FiberSet, Layer, Order, Ref, Semaphore, TxPriorityQueue } from "effect";

export type DiffContextPrefetchPriority = "foreground" | "background";

export interface DiffContextPrefetchRequest {
  readonly generation: string;
  readonly id: string;
  readonly priority: DiffContextPrefetchPriority;
  readonly task: (
    isCancelled: Effect.Effect<boolean>,
    priority: Effect.Effect<DiffContextPrefetchPriority>,
  ) => Effect.Effect<void>;
}

interface DiffContextPrefetchShape {
  readonly run: (request: DiffContextPrefetchRequest) => Effect.Effect<void>;
  readonly setGeneration: (identity: string) => Effect.Effect<void>;
  readonly setPriority: (generation: string, id: string, priority: DiffContextPrefetchPriority) => Effect.Effect<void>;
}

export class DiffContextPrefetch extends Context.Service<DiffContextPrefetch, DiffContextPrefetchShape>()(
  "kenn-forge/DiffContextPrefetch",
) {}

interface DiffContextPrefetchLayerOptions {
  readonly concurrency: number;
  readonly deferBackground: Effect.Effect<void>;
}

type PrefetchTaskState = "deferred" | "queued" | "active" | "cancelled" | "settled";

interface PrefetchTask {
  readonly cancelled: Ref.Ref<boolean>;
  readonly completion: Deferred.Deferred<void>;
  readonly generation: string;
  readonly id: string;
  readonly key: string;
  readonly sequence: number;
  readonly task: DiffContextPrefetchRequest["task"];
  epoch: number;
  priority: DiffContextPrefetchPriority;
  state: PrefetchTaskState;
}

const taskOrder = Order.make<PrefetchTask>((left, right) => {
  const leftPriority = left.priority === "foreground" ? 0 : 1;
  const rightPriority = right.priority === "foreground" ? 0 : 1;
  if (leftPriority < rightPriority) return -1;
  if (leftPriority > rightPriority) return 1;
  if (left.sequence < right.sequence) return -1;
  if (left.sequence > right.sequence) return 1;
  return 0;
});

function taskKey(generation: string, id: string): string {
  return `${generation}\0${id}`;
}

export function makeDiffContextPrefetchLayer(
  options: DiffContextPrefetchLayerOptions,
): Layer.Layer<DiffContextPrefetch> {
  if (!Number.isInteger(options.concurrency) || options.concurrency < 1) {
    throw new RangeError("Diff context prefetch concurrency must be a positive integer");
  }

  return Layer.effect(DiffContextPrefetch)(
    Effect.gen(function* () {
      const queue = yield* TxPriorityQueue.empty(taskOrder);
      const stateLock = yield* Semaphore.make(1);
      const promoters = yield* FiberSet.make<void, never>();
      const tasks = new Map<string, PrefetchTask>();
      let currentGeneration: string | undefined;
      let nextSequence = 0;

      const cancelUnlocked = Effect.fn("DiffContextPrefetch.cancel")(function* (task: PrefetchTask) {
        if (task.state === "cancelled" || task.state === "settled") return;
        const wasActive = task.state === "active";
        const wasQueued = task.state === "queued";
        task.epoch += 1;
        if (!wasActive) task.state = "cancelled";
        yield* Ref.set(task.cancelled, true);
        if (wasQueued) {
          yield* TxPriorityQueue.removeIf(queue, (candidate) => candidate.sequence === task.sequence);
        }
        if (!wasActive && tasks.get(task.key) === task) tasks.delete(task.key);
        yield* Deferred.succeed(task.completion, undefined);
      });

      const cancel = Effect.fn("DiffContextPrefetch.cancelSynchronized")(function* (task: PrefetchTask) {
        yield* stateLock.withPermit(cancelUnlocked(task));
      });

      const settle = Effect.fn("DiffContextPrefetch.settle")(function* (task: PrefetchTask) {
        yield* stateLock.withPermit(
          Effect.gen(function* () {
            task.state = "settled";
            if (tasks.get(task.key) === task) tasks.delete(task.key);
            yield* Deferred.succeed(task.completion, undefined);
          }),
        );
      });

      const launchDeferred = Effect.fn("DiffContextPrefetch.deferBackground")(function* (task: PrefetchTask) {
        const epoch = task.epoch;
        yield* FiberSet.run(
          promoters,
          options.deferBackground.pipe(
            Effect.andThen(
              stateLock.withPermit(
                Effect.gen(function* () {
                  if (
                    task.state !== "deferred" ||
                    task.epoch !== epoch ||
                    currentGeneration !== task.generation ||
                    (yield* Ref.get(task.cancelled))
                  )
                    return;
                  task.state = "queued";
                  yield* TxPriorityQueue.offer(queue, task);
                }),
              ),
            ),
          ),
        );
      });

      const runWorker = Effect.forever(
        Effect.gen(function* () {
          const task = yield* TxPriorityQueue.take(queue);
          const shouldRun = yield* stateLock.withPermit(
            Effect.gen(function* () {
              if (task.state !== "queued" || currentGeneration !== task.generation || (yield* Ref.get(task.cancelled)))
                return false;
              task.state = "active";
              return true;
            }),
          );
          if (!shouldRun) return;

          yield* task
            .task(
              Ref.get(task.cancelled),
              Effect.sync(() => task.priority),
            )
            .pipe(
              Effect.catchCause((cause) =>
                Cause.hasInterruptsOnly(cause)
                  ? Effect.failCause(cause)
                  : Effect.logError("Diff context prefetch task failed", cause),
              ),
              Effect.ensuring(settle(task)),
            );
        }),
      );

      yield* Effect.forEach(Array.from({ length: options.concurrency }), () => Effect.forkScoped(runWorker), {
        discard: true,
      });

      const run = Effect.fn("DiffContextPrefetch.run")(function* (request: DiffContextPrefetchRequest) {
        return yield* Effect.uninterruptibleMask((restore) =>
          Effect.gen(function* () {
            const cancelled = yield* Ref.make(false);
            const completion = yield* Deferred.make<void>();
            const sequence = nextSequence;
            nextSequence += 1;
            const task: PrefetchTask = {
              cancelled,
              completion,
              generation: request.generation,
              id: request.id,
              key: taskKey(request.generation, request.id),
              sequence,
              task: request.task,
              epoch: 0,
              priority: request.priority,
              state: request.priority === "foreground" ? "queued" : "deferred",
            };

            const shouldDefer = yield* stateLock.withPermit(
              Effect.gen(function* () {
                if (currentGeneration === undefined) currentGeneration = request.generation;
                if (currentGeneration !== request.generation) {
                  task.state = "cancelled";
                  yield* Ref.set(cancelled, true);
                  yield* Deferred.succeed(completion, undefined);
                  return false;
                }
                const previous = tasks.get(task.key);
                if (previous !== undefined) yield* cancelUnlocked(previous);
                tasks.set(task.key, task);
                if (task.priority === "foreground") {
                  yield* TxPriorityQueue.offer(queue, task);
                  return false;
                }
                return true;
              }),
            );
            if (shouldDefer) yield* launchDeferred(task);

            return yield* restore(Deferred.await(completion)).pipe(Effect.onInterrupt(() => cancel(task)));
          }),
        );
      });

      const setGeneration = Effect.fn("DiffContextPrefetch.setGeneration")(function* (identity: string) {
        yield* stateLock.withPermit(
          Effect.gen(function* () {
            if (currentGeneration === identity) return;
            currentGeneration = identity;
            for (const task of tasks.values()) {
              if (task.generation !== identity) yield* cancelUnlocked(task);
            }
          }),
        );
      });

      const setPriority = Effect.fn("DiffContextPrefetch.setPriority")(function* (
        generation: string,
        id: string,
        priority: DiffContextPrefetchPriority,
      ) {
        const task = tasks.get(taskKey(generation, id));
        if (task === undefined || task.priority === priority) return;
        const shouldDefer = yield* stateLock.withPermit(
          Effect.gen(function* () {
            if (task.state === "cancelled" || task.state === "settled" || task.priority === priority) return false;
            if (task.state === "active") {
              task.priority = priority;
              return false;
            }
            if (task.state === "queued") {
              yield* TxPriorityQueue.removeIf(queue, (candidate) => candidate.sequence === task.sequence);
            }
            task.epoch += 1;
            task.priority = priority;
            if (priority === "foreground") {
              task.state = "queued";
              yield* TxPriorityQueue.offer(queue, task);
              return false;
            }
            task.state = "deferred";
            return true;
          }),
        );
        if (shouldDefer) yield* launchDeferred(task);
      });

      return { run, setGeneration, setPriority };
    }),
  );
}

export const nextBrowserBackgroundTurn = Effect.callback<void>((resume) => {
  if (typeof globalThis.requestIdleCallback === "function") {
    const id = globalThis.requestIdleCallback(() => resume(Effect.void), { timeout: 500 });
    return Effect.sync(() => globalThis.cancelIdleCallback(id));
  }
  const id = globalThis.setTimeout(() => resume(Effect.void), 50);
  return Effect.sync(() => globalThis.clearTimeout(id));
});

export const DiffContextPrefetchLive = makeDiffContextPrefetchLayer({
  concurrency: 4,
  deferBackground: nextBrowserBackgroundTurn,
});
