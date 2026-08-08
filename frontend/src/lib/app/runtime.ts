import { Cause, Exit, Fiber, ManagedRuntime, Option } from "effect";
import type { Effect as EffectType } from "effect/Effect";
import type { Exit as ExitType } from "effect/Exit";
import type { Error as LayerError, Success as LayerSuccess } from "effect/Layer";
import type { ManagedRuntime as ManagedRuntimeType } from "effect/ManagedRuntime";
import { AppLiveLayer } from "./layer.js";

export type AppServices = LayerSuccess<typeof AppLiveLayer>;
export type AppLayerError = LayerError<typeof AppLiveLayer>;

export interface CommandRunOptions<E> {
  readonly operation: string;
  readonly safeContext: Readonly<Record<string, string | number | boolean>>;
  readonly onFailure: (failure: E | AppLayerError) => void;
}

export interface AppExecution<A, E> {
  readonly interrupt: () => void;
  readonly await: EffectType<ExitType<A, E | AppLayerError>>;
}

export interface AppRuntime {
  readonly runCommand: <A, E>(
    program: EffectType<A, E, AppServices>,
    options: CommandRunOptions<E>,
  ) => AppExecution<A, E>;
}

export type OwnedAppRuntime = AppRuntime & Pick<ManagedRuntimeType<AppServices, AppLayerError>, "disposeEffect">;

export function makeAppRuntimeBoundary(managed: ManagedRuntimeType<AppServices, AppLayerError>): OwnedAppRuntime {
  return {
    disposeEffect: managed.disposeEffect,
    runCommand: <A, E>(program: EffectType<A, E, AppServices>, options: CommandRunOptions<E>): AppExecution<A, E> => {
      const fiber = managed.runFork(program);
      fiber.addObserver((exit) => {
        if (Exit.isSuccess(exit) || Cause.hasInterruptsOnly(exit.cause)) {
          return;
        }
        if (Cause.hasDies(exit.cause)) {
          console.error("Frontend Effect command failed with a defect", {
            operation: options.operation,
            context: options.safeContext,
            cause: Cause.pretty(exit.cause),
          });
        }
        const failure = Cause.findErrorOption(exit.cause);
        if (Option.isSome(failure)) {
          options.onFailure(failure.value);
        }
      });
      return {
        interrupt: () => fiber.interruptUnsafe(),
        await: Fiber.await(fiber),
      };
    },
  };
}

export const makeAppRuntime = (): OwnedAppRuntime => makeAppRuntimeBoundary(ManagedRuntime.make(AppLiveLayer));
