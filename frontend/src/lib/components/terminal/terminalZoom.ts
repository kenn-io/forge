import { Effect } from "effect";
import type { AppRuntime } from "../../app/runtime.js";
import { DEFAULT_TERMINAL_SETTINGS } from "../../api/types.js";
import type { SettingsError } from "../../stores/settings-workflow.js";
import { saveTerminalSettings, type TerminalSettingsStore } from "../../stores/terminal-settings-persistence.js";

export const MIN_TERMINAL_FONT_SIZE = 8;
export const MAX_TERMINAL_FONT_SIZE = 32;
export const RESET_TERMINAL_FONT_SIZE = DEFAULT_TERMINAL_SETTINGS.font_size;

interface TerminalZoomControllerOptions {
  runtime: AppRuntime;
  store: TerminalSettingsStore;
  reportError: (error: SettingsError) => void;
  onPendingChange?: (pending: boolean) => void;
}

export interface TerminalZoomController {
  decrease: () => void;
  dispose: () => void;
  increase: () => void;
  reset: () => void;
  setFontSize: (fontSize: number) => void;
  whenIdle: () => Effect.Effect<void>;
}

function clampFontSize(fontSize: number): number {
  return Math.min(MAX_TERMINAL_FONT_SIZE, Math.max(MIN_TERMINAL_FONT_SIZE, Math.round(fontSize)));
}

export function createTerminalZoomController({
  runtime,
  store,
  reportError,
  onPendingChange,
}: TerminalZoomControllerOptions): TerminalZoomController {
  let latestCompletion: Effect.Effect<void> = Effect.void;
  let pendingSaves = 0;
  let pendingChange = onPendingChange;
  let failureReporter: ((error: SettingsError) => void) | undefined = reportError;

  function setFontSize(fontSize: number): void {
    const nextFontSize = clampFontSize(fontSize);
    const current = store.getTerminalSettings();
    if (current.font_size === nextFontSize) return;

    pendingSaves += 1;
    if (pendingSaves === 1) pendingChange?.(true);
    const program = saveTerminalSettings({
      baseline: current,
      changes: { font_size: nextFontSize },
      store,
    }).pipe(
      Effect.ensuring(
        Effect.sync(() => {
          pendingSaves -= 1;
          if (pendingSaves === 0) pendingChange?.(false);
        }),
      ),
    );
    const execution = runtime.runCommand(program, {
      operation: "save terminal zoom",
      safeContext: { fontSize: nextFontSize },
      onFailure: (failure) => failureReporter?.(failure),
    });
    latestCompletion = execution.await.pipe(Effect.asVoid);
  }

  function increase(): void {
    setFontSize(store.getTerminalSettings().font_size + 1);
  }

  function decrease(): void {
    setFontSize(store.getTerminalSettings().font_size - 1);
  }

  function reset(): void {
    setFontSize(RESET_TERMINAL_FONT_SIZE);
  }

  return {
    decrease,
    dispose: () => {
      pendingChange = undefined;
      failureReporter = undefined;
    },
    increase,
    reset,
    setFontSize,
    whenIdle: () => latestCompletion,
  };
}
