import { Context, Effect, FiberHandle, Layer } from "effect";
import type { Scope } from "effect/Scope";

interface AnimationFrameFactories {
  readonly request: (callback: FrameRequestCallback) => number;
  readonly cancel: (handle: number) => void;
}

export class AnimationFrames extends Context.Service<AnimationFrames, AnimationFrameFactories>()(
  "kenn-forge/browser/AnimationFrames",
) {}

export const AnimationFramesLive = Layer.succeed(AnimationFrames)({
  request: (callback) => requestAnimationFrame(callback),
  cancel: (handle) => cancelAnimationFrame(handle),
});

export const nextAnimationFrame = Effect.gen(function* () {
  const frames = yield* AnimationFrames;
  return yield* Effect.callback<number>((resume) => {
    const handle = frames.request((timestamp) => resume(Effect.succeed(timestamp)));
    return Effect.sync(() => frames.cancel(handle));
  });
});

export const nextAnimationFrameOrDocumentHidden = Effect.suspend(() => {
  if (document.visibilityState === "hidden") return Effect.void;
  const documentHidden = Effect.callback<void>((resume) => {
    const handleVisibilityChange = () => {
      if (document.visibilityState === "hidden") resume(Effect.void);
    };
    document.addEventListener("visibilitychange", handleVisibilityChange);
    return Effect.sync(() => document.removeEventListener("visibilitychange", handleVisibilityChange));
  });
  return Effect.raceFirst(Effect.asVoid(nextAnimationFrame), documentHidden);
});

export interface AnimationFrameScheduler {
  readonly cancel: () => void;
  readonly schedule: () => boolean;
}

export function makeAnimationFrameScheduler<R>(
  onFrame: Effect.Effect<void, never, R>,
): Effect.Effect<AnimationFrameScheduler, never, AnimationFrames | R | Scope> {
  return Effect.gen(function* () {
    const runFrame = yield* FiberHandle.makeRuntime<AnimationFrames | R, never, void>();
    let generation = 0;
    let scheduled = false;
    let activeFrame: ReturnType<typeof runFrame> | undefined;
    return {
      cancel: () => {
        if (!scheduled) return;
        generation += 1;
        scheduled = false;
        activeFrame?.interruptUnsafe();
        activeFrame = undefined;
      },
      schedule: () => {
        if (scheduled) return false;
        scheduled = true;
        const requestGeneration = generation;
        activeFrame = runFrame(
          nextAnimationFrame.pipe(
            Effect.andThen(Effect.suspend(() => (requestGeneration === generation ? onFrame : Effect.void))),
            Effect.ensuring(
              Effect.sync(() => {
                if (requestGeneration !== generation) return;
                scheduled = false;
                activeFrame = undefined;
              }),
            ),
          ),
        );
        return true;
      },
    };
  });
}
